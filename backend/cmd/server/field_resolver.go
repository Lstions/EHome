package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"ehome/backend/internal/config"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/logger"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 数据字段解析层的进程级接线（设计/数据字段解析层-方案-2026-10-21.md v0.3
// §3.3 / §4.2 / §9.2 #10 / §11）。
//
// 本文件只做"装配与 IO"：领域逻辑在 internal/sensorfield，HTTP 在 internal/api，
// 表模型在 internal/models。三条红线在此落地：
//  1. 数据面零 import：databus 一个字节都不改；观测在既有 SetLatestSinkFn 注入点
//     包装（main.go 里的 func(rec){ api.SetLatestValue(rec); registry.Observe(...) }）。
//  2. 观测落库只在 projector（默认 60s 批量 UPSERT），求值路径零 DB。
//  3. 配置（别名/派生/开关）随镜像分发、启动加载一次；变更需重启（§11 无热加载）。
//
// ⚠ 设备 ID 口径（DS-14）：本文件所有 device_id 一律是 EdgeDevice.ID
// （= unified_data.device_id）。unified_data.edge_device_id 全表 NULL，禁止使用。

// fieldResolverRuntime 是启动期装配结果。
type fieldResolverRuntime struct {
	registry  *sensorfield.Registry
	projector *fieldResolverProjector
}

// setupFieldResolver 构造 Registry、注入声明/别名/派生、冷加载投影表并做启动校验。
//
// 失败语义（方案 §12.1）：
//   - 配置无效（别名冲突/死候选/派生环/参数非法）⇒ **拒绝启动**（fail-closed）；
//   - 投影表读失败 / 单条声明非法 ⇒ 降级"仅声明" + WARN（fail-open，不阻塞启动）。
func setupFieldResolver(db *gorm.DB, cfg *config.Config, driverRegistry *drivers.Registry) (*fieldResolverRuntime, error) {
	frCfg := cfg.FieldResolverSettings()
	scfg := sensorfield.Config{
		Enabled:          frCfg.Enabled,
		MaxSelectorChars: frCfg.MaxSelectorChars,
		MaxFamilySize:    frCfg.MaxFamilySize,
		MaxVirtualDepth:  frCfg.MaxVirtualDepth,
		SnapshotTTL:      frCfg.SnapshotTTL.Duration,
	}
	registry := sensorfield.New(scfg)

	// 1) 静态声明：driver 的 GetSensorDefinitions()（启动期可信输入）。
	registerFieldResolverDeclarations(registry, driverRegistry)

	// 2) 别名与派生（唯一来源 = 随镜像分发的 YAML；本包不 import yaml，IO 在调用方）。
	aliasRules, aliasErr := loadFieldAliasRules(frCfg.AliasesFile)
	if aliasErr != nil {
		// 文件缺失/格式错误是**配置问题**：开关打开时必须拒绝启动；
		// 关闭态（默认）只 WARN —— 此时特殊 selector 本就不可用（ErrDisabled），
		// 缺失别名不会造成"看起来能用但静默失效"。
		if frCfg.Enabled {
			return nil, fmt.Errorf("field_resolver.aliases_file %q: %w", frCfg.AliasesFile, aliasErr)
		}
		logger.Warnf("[field-resolver] aliases not loaded (%v); resolver disabled by default so this is tolerated", aliasErr)
	} else if len(aliasRules) > 0 {
		if err := registry.SetAliases(aliasRules); err != nil {
			return nil, fmt.Errorf("field_resolver aliases invalid: %w", err)
		}
	}
	virtualDefs, virtualErr := loadFieldVirtualDefs(frCfg.VirtualFile)
	if virtualErr != nil {
		if frCfg.Enabled {
			return nil, fmt.Errorf("field_resolver.virtual_file %q: %w", frCfg.VirtualFile, virtualErr)
		}
		logger.Warnf("[field-resolver] virtual defs not loaded (%v); resolver disabled by default so this is tolerated", virtualErr)
	} else if len(virtualDefs) > 0 {
		if err := registry.SetVirtuals(virtualDefs); err != nil {
			return nil, fmt.Errorf("field_resolver virtual defs invalid: %w", err)
		}
	}

	// 3) 冷加载（M3）：device→type、观测投影、丰富层。失败只降级不阻塞（§12.1）。
	if err := warmupFieldResolver(db, registry); err != nil {
		logger.Warnf("[field-resolver] cold load failed, degrading to declarations-only: %v", err)
	}

	// 4) 启动校验（v0.3 分级裁决）：
	//    A 级（Validate）：只查配置自相矛盾（别名冲突/单位类不符/派生环/引用未知语义）；
	//    enabled=true 时不通过 ⇒ **拒绝启动**（fail-closed）。
	//    B 级（Warnings）：死候选/无候选/观测表为空等**数据依赖**问题 ⇒ 只 WARN + 指标，
	//    绝不 Fatal（全新部署或动态族尚未上报时服务必须能起来）。
	if err := registry.Validate(); err != nil {
		if frCfg.Enabled {
			return nil, fmt.Errorf("field_resolver config invalid (fail-closed): %w", err)
		}
		logger.Warnf("[field-resolver] config validation warnings (disabled mode, not fatal): %v", err)
	}
	for _, warning := range registry.Warnings() {
		logger.Warnf("[field-resolver] %s", warning)
	}

	projector := &fieldResolverProjector{
		db:       db,
		registry: registry,
		sink:     &fieldObsFlushSink{db: db},
		interval: frCfg.ObserveInterval.Duration,
	}
	logger.Infof("[field-resolver] enabled=%v observe_interval=%s snapshot_ttl=%s epoch=%d",
		frCfg.Enabled, frCfg.ObserveInterval.Duration, frCfg.SnapshotTTL.Duration, registry.Epoch())
	return &fieldResolverRuntime{registry: registry, projector: projector}, nil
}

// registerFieldResolverDeclarations 遍历已注册驱动，把 GetSensorDefinitions() 变成
// 按 device_type 的静态声明（方案 §2 "声明"；新硬件型号零中心端改动）。
func registerFieldResolverDeclarations(registry *sensorfield.Registry, driverRegistry *drivers.Registry) {
	if driverRegistry == nil {
		return
	}
	for _, deviceType := range driverRegistry.List() {
		drv, err := driverRegistry.Get(deviceType)
		if err != nil {
			continue
		}
		defs := drv.GetSensorDefinitions()
		fields := make([]sensorfield.Field, 0, len(defs))
		seen := make(map[string]struct{}, len(defs))
		for _, def := range defs {
			name := strings.TrimSpace(def.Name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			fields = append(fields, sensorfield.Field{Name: name, Unit: def.Unit})
		}
		if len(fields) > 0 {
			registry.RegisterDeclarations(deviceType, fields)
		}
	}
}

// warmupFieldResolver 冷加载（方案 §4.2 "冷启动：SELECT * FROM sensor_field_obs 灌 snapshot"）。
//
// 顺序固定（impl-core 契约）：SetDeviceType → SetObservations → SetFieldMeta。
func warmupFieldResolver(db *gorm.DB, registry *sensorfield.Registry) error {
	if err := refreshFieldResolverDeviceTypes(db, registry); err != nil {
		return err
	}
	var obsRows []models.SensorFieldObs
	if err := db.Find(&obsRows).Error; err != nil {
		return fmt.Errorf("load sensor_field_obs: %w", err)
	}
	byDevice := make(map[uint][]sensorfield.Field, 16)
	for _, row := range obsRows {
		byDevice[row.DeviceID] = append(byDevice[row.DeviceID], sensorfield.Field{
			Name:     row.SensorName,
			Unit:     row.Unit,
			LastSeen: row.LastSeen,
			Samples:  row.Samples,
			Observed: true,
		})
	}
	for deviceID, fields := range byDevice {
		registry.SetObservations(deviceID, fields)
	}
	var metas []models.SensorFieldMeta
	if err := db.Find(&metas).Error; err != nil {
		return fmt.Errorf("load sensor_field_meta: %w", err)
	}
	if len(metas) > 0 {
		fields := make([]sensorfield.Field, 0, len(metas))
		for _, meta := range metas {
			fields = append(fields, sensorfield.Field{
				Name:      meta.SensorName,
				Unit:      meta.Unit,
				ValueType: sensorfield.ValueType(meta.ValueType),
			})
		}
		registry.SetFieldMeta(fields)
	}
	logger.Infof("[field-resolver] cold load: %d edge device(s), %d observed field(s), %d meta row(s)",
		len(byDevice), len(obsRows), len(metas))
	return nil
}

// ── 配置加载（别名 / 派生）──────────────────────────────────────────────────

// aliasFileSpec 是 config/field_aliases.yaml 的单项形状（方案 §6.1）。
//
// 文件按 key 整体替换（不追加），每个语义声明期望单位类；候选顺序即优先级。
type aliasFileSpec struct {
	ExpectedUnit string   `yaml:"expected_unit"`
	Candidates   []string `yaml:"candidates"`
	AllowOverlap bool     `yaml:"allow_overlap"`
}

// loadFieldAliasRules 读取并展开别名表。
//
// 为什么由 cmd/server 解析 YAML：sensorfield 领域包的红线 2 只允许 stdlib + logger +
// metrics（不 import yaml、不做 IO）；而别名表的"唯一默认表"必须来自配置文件，
// 不能在代码里再存一份（方案 §6.1/§8.4）。
func loadFieldAliasRules(path string) ([]sensorfield.AliasRule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("aliases_file is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var table map[string]aliasFileSpec
	if err := yaml.Unmarshal(raw, &table); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	// 排序遍历：同一份配置每次启动得到的别名顺序一致（/schema 快照测试可依赖）。
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rules := make([]sensorfield.AliasRule, 0, len(keys))
	for _, semantic := range keys {
		spec := table[semantic]
		if strings.TrimSpace(semantic) == "" {
			continue
		}
		rule := sensorfield.AliasRule{
			Semantic:     strings.TrimSpace(semantic),
			ExpectedUnit: strings.TrimSpace(spec.ExpectedUnit),
			AllowOverlap: spec.AllowOverlap,
			Candidates:   make([]string, 0, len(spec.Candidates)),
		}
		for _, candidate := range spec.Candidates {
			if trimmed := strings.TrimSpace(candidate); trimmed != "" {
				rule.Candidates = append(rule.Candidates, trimmed)
			}
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("no alias entries")
	}
	return rules, nil
}

// virtualFileSpec 是 config/virtual_fields.yaml 的单项定义（方案 §6.2）。
type virtualFileSpec struct {
	Fn    string   `yaml:"fn"`
	Args  []string `yaml:"args"`
	Scale float64  `yaml:"scale"`
}

// virtualFileDoc 是 config/virtual_fields.yaml 的顶层形状。
type virtualFileDoc struct {
	Virtual map[string]virtualFileSpec `yaml:"virtual"`
}

// loadFieldVirtualDefs 读取派生字段定义（排序遍历保证确定性）。
func loadFieldVirtualDefs(path string) ([]sensorfield.VirtualDef, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("virtual_file is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc virtualFileDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	keys := make([]string, 0, len(doc.Virtual))
	for key := range doc.Virtual {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	defs := make([]sensorfield.VirtualDef, 0, len(keys))
	for _, name := range keys {
		spec := doc.Virtual[name]
		if strings.TrimSpace(name) == "" || strings.TrimSpace(spec.Fn) == "" {
			continue
		}
		def := sensorfield.VirtualDef{
			Name:  strings.TrimSpace(name),
			Fn:    strings.TrimSpace(spec.Fn),
			Args:  make([]string, 0, len(spec.Args)),
			Scale: spec.Scale,
		}
		for _, arg := range spec.Args {
			if trimmed := strings.TrimSpace(arg); trimmed != "" {
				def.Args = append(def.Args, trimmed)
			}
		}
		defs = append(defs, def)
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("no virtual definitions")
	}
	return defs, nil
}

// ── 观测投影（projector）───────────────────────────────────────────────────

// fieldObsFlushSink 实现 sensorfield.FlushSink：把观测增量 UPSERT 到 sensor_field_obs。
//
// 增量语义（impl-core 契约，写错会重复计数）：
//   - Observation.Samples 是**自上次成功 Flush 以来的增量**，故 samples = 旧值 + EXCLUDED.samples；
//   - first_seen 取最小、last_seen 取最大（FirstSeen 只是本进程首次，靠 MIN 保单调）；
//   - unit 为空时保留原值（不让"本次没解析出单位"覆盖已观测到的单位）。
type fieldObsFlushSink struct {
	db *gorm.DB
}

// UpsertObservations 批量 UPSERT（GORM clause.OnConflict 方言无关，函数名按方言选）。
func (s *fieldObsFlushSink) UpsertObservations(obs []sensorfield.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	rows := make([]models.SensorFieldObs, 0, len(obs))
	for _, o := range obs {
		if o.DeviceID == 0 || strings.TrimSpace(o.Name) == "" {
			// device_id=0 的行不能进投影（DS-14 的另一面：血缘列可能为空）。
			continue
		}
		rows = append(rows, models.SensorFieldObs{
			DeviceID:   o.DeviceID,
			SensorName: o.Name,
			Unit:       o.Unit,
			FirstSeen:  o.FirstSeen,
			LastSeen:   o.LastSeen,
			Samples:    o.Samples,
			UpdatedAt:  time.Now(),
		})
	}
	if len(rows) == 0 {
		return nil
	}
	minFn, maxFn := "LEAST", "GREATEST"
	if s.db.Dialector.Name() == "sqlite" {
		// SQLite 无 LEAST/GREATEST，两参数标量函数是 MIN()/MAX()。
		minFn, maxFn = "MIN", "MAX"
	}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "device_id"}, {Name: "sensor_name"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"unit":       gorm.Expr("CASE WHEN excluded.unit <> '' THEN excluded.unit ELSE sensor_field_obs.unit END"),
			"first_seen": gorm.Expr(minFn + "(sensor_field_obs.first_seen, excluded.first_seen)"),
			"last_seen":  gorm.Expr(maxFn + "(sensor_field_obs.last_seen, excluded.last_seen)"),
			"samples":    gorm.Expr("sensor_field_obs.samples + excluded.samples"),
			"updated_at": time.Now(),
		}),
	}).CreateInBatches(&rows, 500).Error
}

// fieldResolverProjector 周期刷新 device→type 并把观测增量落库（方案 §4.2）。
type fieldResolverProjector struct {
	db       *gorm.DB
	registry *sensorfield.Registry
	sink     sensorfield.FlushSink
	interval time.Duration
}

// Run 阻塞运行到 ctx 取消（main.go 用 goroutine 启动并在退出时 cancel）。
func (p *fieldResolverProjector) Run(ctx context.Context) {
	if p.interval <= 0 {
		p.interval = 60 * time.Second
	}
	// 启动先刷一次 device_type：冷加载与 projector 之间设备可能已变化（幂等）。
	if err := refreshFieldResolverDeviceTypes(p.db, p.registry); err != nil {
		logger.Warnf("[field-resolver] projector refresh device types failed: %v", err)
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 退出前做最后一次 flush：避免进程重启丢掉最多一个周期的观测增量。
			// 用 WithoutCancel 语义的独立 ctx：调用方 cancel 后仍允许完成这次写。
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.registry.Flush(flushCtx, p.sink); err != nil {
				logger.Warnf("[field-resolver] final flush failed: %v", err)
			}
			cancel()
			return
		case <-ticker.C:
			if err := refreshFieldResolverDeviceTypes(p.db, p.registry); err != nil {
				logger.Warnf("[field-resolver] projector refresh device types failed: %v", err)
			}
			// Flush 内部：sink 失败保留增量下次重试、不合并内存 known（方案 §12.1
			// "观测投影不可用 ⇒ 降级仅声明"）；同时刷新 snapshot_age 指标。
			if err := p.registry.Flush(ctx, p.sink); err != nil {
				logger.Warnf("[field-resolver] observation flush failed (will retry): %v", err)
			}
		}
	}
}

// refreshFieldResolverDeviceTypes 刷新 device→device_type 绑定（声明归属依据）。
//
// 为什么要周期刷新而不是只在启动时做：设备可在进程生命周期内创建/改型号，而
// api handler 域不在本文件的装配范围（避免跨界改 handler）；projector 每个
// observe_interval 刷一次即可把"已知集合=声明∪观测"的滞后控制在投影间隔内（§7.2）。
func refreshFieldResolverDeviceTypes(db *gorm.DB, registry *sensorfield.Registry) error {
	type deviceTypeRow struct {
		ID         uint
		DeviceType string
	}
	var rows []deviceTypeRow
	err := db.Table("edge_devices AS ed").
		Select("ed.id AS id, COALESCE(NULLIF(dc.device_type, ''), ed.type) AS device_type").
		Joins("LEFT JOIN device_configs dc ON dc.id = ed.device_config_id").
		Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load edge device types: %w", err)
	}
	for _, row := range rows {
		registry.SetDeviceType(row.ID, strings.TrimSpace(row.DeviceType))
	}
	return nil
}
