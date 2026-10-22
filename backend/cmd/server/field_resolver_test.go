package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/config"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/testutil"
)

// ==================== 字段解析层装配/投影测试 ====================

// TestSetupFieldResolver_ShippedConfigLoads 用**仓库里随镜像分发的真实 YAML** 装配一次，
// 证明 enabled=true 时默认配置能通过 Validate A 级（别名冲突/单位类/派生引用）。
//
// 空驱动注册表 ⇒ 所有别名候选都是"死候选"，走 Warnings() B 级（不阻塞启动）——
// 这正是全新部署/动态族未上报时的预期行为。
func TestSetupFieldResolver_ShippedConfigLoads(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := &config.Config{FieldResolver: config.FieldResolverConfig{
		Enabled: true,
		// go test 的 CWD 是包目录 backend/cmd/server；仓库配置文件在 backend/config/。
		AliasesFile: filepath.Join("..", "..", "config", "field_aliases.yaml"),
		VirtualFile: filepath.Join("..", "..", "config", "virtual_fields.yaml"),
	}}
	rt, err := setupFieldResolver(db, cfg, drivers.NewRegistry())
	if err != nil {
		t.Fatalf("shipped config must pass startup validation (enabled=true): %v", err)
	}
	if rt == nil || rt.registry == nil {
		t.Fatalf("runtime/registry is nil")
	}
	if !rt.registry.Enabled() {
		t.Fatalf("registry.Enabled() = false want true")
	}
	if len(rt.registry.Aliases()) != 8 {
		t.Fatalf("aliases = %d want 8 (v0.3 §6.1)", len(rt.registry.Aliases()))
	}
	if len(rt.registry.Virtuals()) != 4 {
		t.Fatalf("virtuals = %d want 4 (v0.3 §6.2)", len(rt.registry.Virtuals()))
	}
	// 空注册表 ⇒ 应有 Warnings（死候选），但不得因此拒绝启动。
	if len(rt.registry.Warnings()) == 0 {
		t.Fatalf("expected B-level warnings with empty driver registry")
	}
}

// TestSetupFieldResolver_MissingFileFailsClosed enabled=true 但配置文件缺失 ⇒ 拒绝启动。
func TestSetupFieldResolver_MissingFileFailsClosed(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := &config.Config{FieldResolver: config.FieldResolverConfig{
		Enabled:     true,
		AliasesFile: filepath.Join("..", "..", "config", "does_not_exist_aliases.yaml"),
		VirtualFile: filepath.Join("..", "..", "config", "virtual_fields.yaml"),
	}}
	if _, err := setupFieldResolver(db, cfg, drivers.NewRegistry()); err == nil {
		t.Fatalf("missing aliases file with enabled=true must fail closed")
	}
}

// TestFieldResolverWiringGate 是"接线不得被静默删除"的源码级门禁（本仓既有范式，见
// notify_wiring_gate_test.go）。它防的是**无意重构**，不证明运行时成功（运行时由
// api 契约测试 + 集成测试覆盖）。
//
// 断言 main.go 中同时存在以下接线（逐条都是"引擎孤儿化"的真实风险点）：
//  1. setupFieldResolver(...)            Registry 被构造
//  2. SetLatestSinkFn(func(...)          观测采集点包装既有注入点（databus 零改动）
//  3. fieldResolver.registry.Observe(    观测真的被调用
//  4. api.SetFieldSelectorResolver(      保存期校验拿到控制面
//  5. SetLatestValueBySensorFn / SetLatestValuesFn / SetPlanProvider  automation 四处注入
//  6. alertEvaluator.SetPlanProvider(    告警求值接入
func TestFieldResolverWiringGate(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(raw)
	required := []string{
		"setupFieldResolver(db, cfg, driverRegistry)",
		"nodeMgr.SetLatestSinkFn(func(record models.UnifiedData)",
		"fieldResolver.registry.Observe(record.DeviceID, record.SensorName, record.Unit, record.Timestamp)",
		"api.SetFieldSelectorResolver(fieldResolver.registry)",
		"automationPlanner.SetLatestValueBySensorFn(api.LatestValueBySensor)",
		"automationPlanner.SetLatestValuesFn(api.LatestValues)",
		// provider 必须**受开关保护**注入：无条件注入会让 enabled=false 的纯字面量
		// 求值多付 ~58ns/次（impl-core 基准实测），故断言门闩存在。
		"if fieldResolver.registry.Enabled() {",
		"automationPlanner.SetPlanProvider(fieldResolver.registry)",
		"automationEvaluator.SetPlanProvider(fieldResolver.registry)",
		"alertEvaluator.SetPlanProvider(fieldResolver.registry)",
		"api.SetupRoutes(",
		"fieldResolver.registry",
	}
	for _, want := range required {
		if !strings.Contains(src, want) {
			t.Errorf("main.go 缺少字段解析层接线片段（接线被删除或重命名）: %q", want)
		}
	}

	// 反向断言：观测不能在 databus 内实现（红线 1：databus 零 import）。
	databusDir := filepath.Join("..", "..", "internal", "databus")
	entries, err := os.ReadDir(databusDir)
	if err != nil {
		t.Fatalf("read databus dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(databusDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(body), "sensorfield") {
			t.Errorf("internal/databus/%s 引用了 sensorfield（违反方案 §3.3 红线 1：数据面零依赖）", entry.Name())
		}
	}
}

// TestFieldObsFlushSink_UpsertSemantics 覆盖方案 §4.2 的增量 UPSERT 语义：
// samples 累加（不是覆盖）、first_seen 取 min、last_seen 取 max、空 unit 不覆盖。
func TestFieldObsFlushSink_UpsertSemantics(t *testing.T) {
	db := testutil.OpenTestDB(t)
	sink := &fieldObsFlushSink{db: db}

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	t0 := base
	t1 := base.Add(time.Hour)
	t2 := base.Add(2 * time.Hour)
	t3 := base.Add(3 * time.Hour)

	// 第一批：单位 %、样本 5。
	if err := sink.UpsertObservations([]sensorfield.Observation{{
		DeviceID: 7, Name: "rsoc", Unit: "%", FirstSeen: t1, LastSeen: t2, Samples: 5,
	}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// 第二批：单位空（必须保留 %）、first_seen 更早（取 min）、last_seen 更晚（取 max）、样本累加。
	if err := sink.UpsertObservations([]sensorfield.Observation{{
		DeviceID: 7, Name: "rsoc", Unit: "", FirstSeen: t0, LastSeen: t3, Samples: 3,
	}}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var row models.SensorFieldObs
	if err := db.Where("device_id = ? AND sensor_name = ?", 7, "rsoc").First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.Samples != 8 {
		t.Fatalf("samples = %d want 8 (increment, not overwrite)", row.Samples)
	}
	if row.Unit != "%" {
		t.Fatalf("unit = %q want %q (empty unit must not overwrite)", row.Unit, "%")
	}
	if !row.FirstSeen.Equal(t0) {
		t.Fatalf("first_seen = %v want %v (must take min)", row.FirstSeen, t0)
	}
	if !row.LastSeen.Equal(t3) {
		t.Fatalf("last_seen = %v want %v (must take max)", row.LastSeen, t3)
	}

	// 第三批：非空单位覆盖 + 样本继续累加。
	if err := sink.UpsertObservations([]sensorfield.Observation{{
		DeviceID: 7, Name: "rsoc", Unit: "percent", FirstSeen: t2, LastSeen: t3, Samples: 1,
	}}); err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	if err := db.Where("device_id = ? AND sensor_name = ?", 7, "rsoc").First(&row).Error; err != nil {
		t.Fatalf("reload row: %v", err)
	}
	if row.Unit != "percent" || row.Samples != 9 {
		t.Fatalf("after override: unit=%q samples=%d want percent/9", row.Unit, row.Samples)
	}

	// device_id=0 / name 空的行必须被丢弃（DS-14：血缘列可能为空，不能污染投影）。
	if err := sink.UpsertObservations([]sensorfield.Observation{
		{DeviceID: 0, Name: "ghost", Samples: 1},
		{DeviceID: 1, Name: "  ", Samples: 1},
	}); err != nil {
		t.Fatalf("noise upsert: %v", err)
	}
	var count int64
	if err := db.Model(&models.SensorFieldObs{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d want 1 (noise rows must be dropped)", count)
	}
}

// TestLoadFieldAliasRules 覆盖 §6.1 的 YAML 形状与确定性排序。
func TestLoadFieldAliasRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field_aliases.yaml")
	raw := "soc:\n  expected_unit: \"%\"\n  candidates: [rsoc, battery_capacity]\ncurrent:\n  expected_unit: A\n  candidates: [current]\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write aliases: %v", err)
	}
	rules, err := loadFieldAliasRules(path)
	if err != nil {
		t.Fatalf("load aliases: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %d want 2", len(rules))
	}
	// 排序遍历 ⇒ 字母序：current 在前。
	if rules[0].Semantic != "current" || rules[1].Semantic != "soc" {
		t.Fatalf("order = %q,%q want current,soc (deterministic sort)", rules[0].Semantic, rules[1].Semantic)
	}
	if rules[1].ExpectedUnit != "%" || len(rules[1].Candidates) != 2 || rules[1].Candidates[0] != "rsoc" {
		t.Fatalf("soc rule = %+v", rules[1])
	}
	// 缺文件是错误（开关打开时调用方据此拒绝启动）。
	if _, err := loadFieldAliasRules(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatalf("missing file must return error")
	}
}

// TestLoadFieldVirtualDefs 覆盖 §6.2 的 YAML 形状。
func TestLoadFieldVirtualDefs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "virtual_fields.yaml")
	raw := "virtual:\n  dc_power:\n    fn: product\n    args: [\"@total_voltage\", \"@current\"]\n  cell_delta:\n    fn: diff\n    args: [cell_voltage_max, cell_voltage_min]\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write virtuals: %v", err)
	}
	defs, err := loadFieldVirtualDefs(path)
	if err != nil {
		t.Fatalf("load virtuals: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("defs = %d want 2", len(defs))
	}
	if defs[0].Name != "cell_delta" || defs[0].Fn != "diff" || len(defs[0].Args) != 2 {
		t.Fatalf("defs[0] = %+v", defs[0])
	}
	if defs[1].Name != "dc_power" || defs[1].Fn != "product" || defs[1].Args[0] != "@total_voltage" {
		t.Fatalf("defs[1] = %+v", defs[1])
	}
}

// TestWarmupFieldResolver 覆盖 §4.2 冷启动：device→type、观测、meta 三处灌入，
// 且 KnownFields = 声明 ∪ 观测（证明 DS-14 的 device_id 口径接对了）。
func TestWarmupFieldResolver(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := models.DeviceConfig{Name: "测试型号", DeviceType: "jiabaida_bms"}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed config: %v", err)
	}
	dev := models.EdgeDevice{Name: "冷加载设备", NodeID: "N1", ChannelID: 1, DeviceConfigID: cfg.ID, IntervalMs: 5000, Enabled: true, Status: "active"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Create(&models.SensorFieldObs{
		DeviceID: dev.ID, SensorName: "rsoc", Unit: "%", FirstSeen: now.Add(-time.Hour), LastSeen: now, Samples: 12,
	}).Error; err != nil {
		t.Fatalf("seed obs: %v", err)
	}
	precision := 1
	if err := db.Create(&models.SensorFieldMeta{
		SensorName: "rsoc", LabelZh: "SOC", Unit: "%", ValueType: "number", Precision: &precision, Status: "active",
	}).Error; err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	reg := sensorfield.New(sensorfield.Config{Enabled: true})
	reg.RegisterDeclarations("jiabaida_bms", []sensorfield.Field{{Name: "temperature_1", Unit: "°C"}})
	if err := warmupFieldResolver(db, reg); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	known := reg.KnownFields(dev.ID)
	declared, observed, metaMerged := false, false, false
	for _, field := range known {
		switch field.Name {
		case "temperature_1":
			declared = field.Declared
		case "rsoc":
			observed = field.Observed
			metaMerged = field.ValueType == sensorfield.TypeNumber && field.Unit == "%"
		}
	}
	if !declared {
		t.Fatalf("declaration for jiabaida_bms not attributed to device %d: %+v", dev.ID, known)
	}
	if !observed {
		t.Fatalf("observation for device %d not loaded: %+v", dev.ID, known)
	}
	if !metaMerged {
		t.Fatalf("field meta not merged (value_type/unit): %+v", known)
	}
	if reg.SnapshotAge() < 0 {
		t.Fatalf("snapshot age must be non-negative")
	}
}
