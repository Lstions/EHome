package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ensureTemplatesFromDriver 按驱动命令表**确保通道拥有对应模板**（幂等）。
//
// 为什么不能直接用 createTemplatesFromDriver（runtime 2026-10-25 R-03/R-06）：
// 那个函数只 Create、不去重。multi-drop 通道（同通道还有另一台同型存活设备）上
// 删除设备时模板被保留是**共享语义**（见 handleConfigTemplateOnDelete），而 restore
// 旧实现无条件再插一份 ⇒ 每次「删→恢复」净增一条模板；16 次后节点模板数超过
// 固件 MAX_TEMPLATES=16，整份 ConfigManifest 被节点拒绝（全通道停摆、config sync 500）。
//
// 本函数按 (node_id, write_data, read_length) 复用节点上已存在的模板：命中就只把
// 它的 id 补进当前通道的 template_ids；没有才创建。因此 create / update(换绑定) /
// restore 三条路径都可以安全地反复调用（换通道时也能把模板补到新通道上）。
func ensureTemplatesFromDriver(tx *gorm.DB, driverRegistry *drivers.Registry, ch *models.Channel, dev *models.EdgeDevice) error {
	if ch == nil || dev == nil || !dev.Enabled {
		return nil
	}
	drv, err := driverRegistry.Get(dev.Type)
	if err != nil {
		return nil // 没有驱动 ⇒ 没有模板语义（历史类型/通用类型）
	}
	provider, ok := drv.(drivers.CommandTemplateProvider)
	if !ok {
		return nil
	}
	ids := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, s := range strings.Split(ch.TemplateIDs, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		ids = append(ids, s)
	}
	changed := false
	for _, cmd := range provider.GetCommandTemplates() {
		// 与 createTemplatesFromDriver 同一口径：只给「真正会被轮询」的命令建模板。
		if !cmd.Schedulable || cmd.IntervalMs <= 0 || cmd.WriteData == "" {
			continue
		}
		var tmpl models.ConfigTemplate
		err := tx.Where("node_id = ? AND write_data = ? AND read_length = ?", ch.NodeID, cmd.WriteData, cmd.ReadLength).
			Order("id").First(&tmpl).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := models.ConfigTemplate{
				NodeID:     ch.NodeID,
				WriteData:  cmd.WriteData,
				ReadLength: cmd.ReadLength,
				DelayMs:    cmd.DelayMs,
			}
			if dev.ID > 0 {
				edgeID := dev.ID
				row.EdgeDeviceID = &edgeID
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create ConfigTemplate for %s: %w", cmd.ID, err)
			}
			tmpl = row
		case err != nil:
			return fmt.Errorf("lookup ConfigTemplate for %s: %w", cmd.ID, err)
		}
		key := strconv.FormatUint(uint64(tmpl.ID), 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, key)
		changed = true
	}
	if !changed {
		return nil
	}
	joined := strings.Join(ids, ",")
	if err := tx.Model(&models.Channel{}).Where("id = ?", ch.ID).
		Update("template_ids", joined).Error; err != nil {
		return fmt.Errorf("update channel %d template_ids: %w", ch.ID, err)
	}
	ch.TemplateIDs = joined
	return nil
}

// createSingleTemplate inserts one ConfigTemplate and appends its ID to the channel's template_ids.
// edgeDeviceID records the owning edge device (方案 v3.3 §2.4); 0 leaves the
// ownership column NULL (self-healing / callers without a device instance).
func createSingleTemplate(tx *gorm.DB, ch *models.Channel, edgeDeviceID uint, writeData string, readLength uint32, delayMs uint32) error {
	if writeData == "" {
		return nil
	}
	tmpl := models.ConfigTemplate{
		NodeID:     ch.NodeID,
		WriteData:  writeData,
		ReadLength: readLength,
		DelayMs:    delayMs,
	}
	if edgeDeviceID > 0 {
		tmpl.EdgeDeviceID = &edgeDeviceID
	}
	if err := tx.Create(&tmpl).Error; err != nil {
		return err
	}
	newTmplID := strconv.FormatUint(uint64(tmpl.ID), 10)
	if err := tx.Model(ch).Update("template_ids",
		gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newTmplID, newTmplID),
	).Error; err != nil {
		return err
	}
	logger.Infof("[edge-device-create] ConfigTemplate id=%d tx_hex_chars=%d channel=%d", tmpl.ID, len(writeData), ch.ID)
	return nil
}

// checkDeviceUniqueness guards against two devices of the same model sharing one
// slave address on a channel. Multi-drop buses (SPI with several CS lines, I2C
// with several addresses) stay valid because they map to distinct channels or
// distinct hardware_id values. When hardware_id is empty we fall back to
// (channel_id, type) so address-less devices are still de-duplicated.
// excludeID (non-zero) excludes the device being updated from the check.
func checkDeviceUniqueness(tx *gorm.DB, channelID uint, devType, hardwareID string, excludeID uint) error {
	addr := strings.TrimSpace(hardwareID)
	dupQuery := tx.Model(&models.EdgeDevice{}).
		Where("channel_id = ? AND type = ?", channelID, devType)
	if addr != "" {
		dupQuery = dupQuery.Where("hardware_id = ?", addr)
	}
	if excludeID != 0 {
		dupQuery = dupQuery.Where("id <> ?", excludeID)
	}
	var dupCount int64
	if err := dupQuery.Count(&dupCount).Error; err != nil {
		return err
	}
	if dupCount > 0 {
		if addr != "" {
			return fmt.Errorf("channel %d already hosts a %q device at address %q", channelID, devType, addr)
		}
		return fmt.Errorf("channel %d already hosts a %q device", channelID, devType)
	}
	return nil
}

// validateEdgeDeviceAddress is the create/update gate for
// EdgeDevice.hardware_id — the field that holds a PHYSICAL DEVICE ADDRESS
// (Modbus unit id / I2C address), not a bus name.
//
// Why it exists (2026-09-20 production incident): the creation wizard copied
// Channel.hardware_id ("UART1", a bus name) into EdgeDevice.hardware_id. Nothing
// rejected it at write time, so every dispatch failed inside
// deviceaction.ParseHardwareAddress and the operation silently stayed QUEUED
// until its deadline expired ~120s later. The user only ever saw "FAILED /
// deadline expired before dispatch".
//
// 口径 is deliberately NOT re-implemented here: it delegates to
// deviceaction.ParseHardwareAddress (single truth source), so this gate and
// dispatch can never drift apart.
//
// Scope / backward compatibility:
//   - Only types whose controlled actions actually embed an address are gated
//     (Definition.RequiresTargetAddress). A pure I2C/SPI collector that has no
//     addressed action keeps accepting any identifier.
//   - An absent field, an empty value or a whitespace-only value means the
//     legacy default address 1 (exactly ParseHardwareAddress's own semantics),
//     so historical rows and existing API callers are unaffected.
//   - An address-requiring device may legitimately have NO controlled action at
//     all (e.g. it is still disabled / its last controlled action was removed
//     from the catalog). That is not evidence of a bad address, so it is
//     skipped rather than rejected — but then the address cannot be validated
//     either, which is exactly why the wizard must always require one.
func validateEdgeDeviceAddress(driverRegistry *drivers.Registry, devType, hardwareID string) error {
	value := strings.TrimSpace(hardwareID)
	if value == "" || value == "0" {
		return nil
	}
	requiresAddress, cataloged := driverRequiresTargetAddress(driverRegistry, devType)
	if !cataloged {
		// G7 (2026-09-21): a type that is not in the driver registry used to be
		// treated as "no signature information, so do not gate on a guess" and was
		// passed unconditionally. That made an unregistered type a universal key:
		// POST /device-configs accepted device_type="no_such_driver_xyz" (it only
		// checked non-empty), PUT /nodes/:id/config derived edge_devices.type from
		// that config, and from then on EVERY address write for the device was
		// waved through — the very "UART1 as hardware_id" incident this gate exists
		// to prevent, reached in three public HTTP steps.
		//
		// Failing closed is safe because the device type is not free-form data: the
		// create and update paths require a registered driver whenever no
		// DeviceConfig is bound (driverRegistry.Get), and POST /device-configs now
		// rejects an unregistered device_type as well. The only rows that can still
		// carry an unregistered type are historical ones, and they keep working:
		// this gate only runs when the request actually puts an address on the
		// dispatch path (hardware_id supplied, or the type changes), exactly the
		// reviewed R2 trigger contract. Renaming / re-enabling / changing the
		// interval of such a row still succeeds, so legacy data is never turned
		// into "cannot even be renamed"; the repair path is to bind a registered
		// driver type or to send an address a registered type accepts.
		return fmt.Errorf("device type %q is not registered: unknown driver types have no address semantics, so hardware_id %q cannot be validated", strings.TrimSpace(devType), value)
	}
	if !requiresAddress {
		return nil
	}
	if _, err := deviceaction.ParseHardwareAddress(value); err != nil {
		return fmt.Errorf("hardware_id %q is not a valid device address: %v (该值必须是 1-254 的十进制或 0xNN 形式；若这是总线名如 \"UART1\"，请填写总线上该设备的实际地址)", value, err)
	}
	return nil
}

// driverRequiresTargetAddress reports whether any controlled action of devType
// parses EdgeDevice.hardware_id as a physical address. The second return value
// distinguishes "type is known to the catalog" from "no signature information
// available".
//
// cataloged == false means the type is empty or not registered and therefore
// carries NO address semantics at all. Callers must treat that as "cannot
// validate" and fail closed rather than as "nothing to validate" (G7,
// 2026-09-21): an unregistered type was reachable through public HTTP and
// turned the address gate off wholesale.
func driverRequiresTargetAddress(driverRegistry *drivers.Registry, devType string) (req, cataloged bool) {
	devType = strings.TrimSpace(devType)
	if devType == "" || driverRegistry == nil {
		return false, false
	}
	if _, err := driverRegistry.Get(devType); err != nil {
		return false, false
	}
	for _, def := range builtInActionRegistry().List(devType) {
		if def.RequiresTargetAddress() {
			return true, true
		}
	}
	return false, true
}

var (
	actionRegistryOnce sync.Once
	actionRegistryVal  *deviceaction.Registry
)

// builtInActionRegistry builds the Action Catalog over the built-in drivers.
// The catalog only carries static definitions plus the composition root's
// rollout overrides, both of which are stable for the lifetime of the process,
// so it is cached here instead of being rebuilt on every edge-device write.
func builtInActionRegistry() *deviceaction.Registry {
	actionRegistryOnce.Do(func() {
		registry := drivers.NewRegistry()
		drivers.RegisterBuiltInDrivers(registry)
		actionRegistryVal = deviceaction.NewBuiltInRegistry(registry)
	})
	return actionRegistryVal
}

func validateDeviceConfigForChannel(db *gorm.DB, deviceConfigID uint, channel *models.Channel) (models.DeviceConfig, error) {
	if deviceConfigID == 0 {
		// 0 = no template; caller resolves type/hardware_types from the driver registry.
		return models.DeviceConfig{}, nil
	}
	var config models.DeviceConfig
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", deviceConfigID, "active").First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.DeviceConfig{}, fmt.Errorf("active device config not found")
		}
		return models.DeviceConfig{}, err
	}
	configHardwareType := strings.TrimSpace(config.HardwareType)
	if configHardwareType == "" {
		// v2.2: fall back to Connection JSONB bus_type when legacy hardware_type is absent.
		if len(config.Connection) > 0 {
			var conn map[string]any
			if err := json.Unmarshal(config.Connection, &conn); err == nil {
				if bt, ok := conn["bus_type"].(string); ok {
					configHardwareType = strings.TrimSpace(bt)
				}
			}
		}
	}
	if configHardwareType == "" {
		return models.DeviceConfig{}, fmt.Errorf("device config has no hardware type")
	}
	if !strings.EqualFold(configHardwareType, channel.HardwareType) && !strings.EqualFold(configHardwareType, channel.BusType) {
		return models.DeviceConfig{}, fmt.Errorf("device config hardware type %q is incompatible with channel type %q", configHardwareType, channel.HardwareType)
	}
	return config, nil
}

// registerEdgeDeviceRoutes sets up edge-device CRUD routes
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, driverRegistry *drivers.Registry) {
	driverRegistry = resolveDriverRegistry(driverRegistry)
	eventBus := nodeMgr.EventBus()

	// List edge devices (v2.2 path for /devices)
	//
	// 分页契约 (架构与接口评估 P1.2 裁决: items + total; 参数语义与
	// /automation-events、/logical-devices、/nodes 一致):
	// 查询参数 page (默认 1, <1 归 1) / page_size (默认 20, 超出 [1,200] 归 20),
	// 响应 data = {items, total, page, page_size}。
	// total 语义: **过滤后全量**条数, 不是当前页条数。
	//
	// 历史 (为什么必须改, 负债 I-11「假分页」): 本端点原为无参全量 Find + 3 Preload
	// + 裸数组返回 —— 前端 EdgeDeviceList.vue 一直发 page/page_size, 后端**静默丢弃**,
	// :data 绑定的是本地全量数组, el-pagination 纯装饰。search/status/hardware
	// 三个筛选也只在前端当前页本地过滤 (§3.3.5)。
	//
	// Order("id") 不是装饰: 真分页必须有稳定全序 (见 handler_node.go 同段说明)。
	v1.GET("/edge-devices", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 20
		}

		// 过滤条件必须在 Count 与 Find **两侧同时**生效, 否则 total 会变成未过滤的
		// 全量、分页器算出多余页数 (用户翻到空页)。
		// 过滤链刻意**不含 Preload**: Count 只需要 WHERE, 带 Preload 的 Count 会
		// 退化成 join 计数; Find 时再挂 Preload 链。
		query := db.WithContext(c.Request.Context()).Model(&models.EdgeDevice{})

		// Apply optional node_id filter
		nodeID := strings.TrimSpace(c.Query("node_id"))
		if nodeID != "" {
			query = query.Where("node_id = ?", nodeID)
		}

		// Apply optional device_type & status filters
		if dt := strings.TrimSpace(c.Query("device_type")); dt != "" {
			query = query.Where("type = ?", dt)
		}
		if st := strings.TrimSpace(c.Query("status")); st != "" {
			query = query.Where("status = ?", st)
		}

		// hardware_type 过滤: edge_devices 表没有该列, 真源在 channels.hardware_type
		// (GET /edge-devices/:id 的响应里 hardware_type 也是从 Channel 归一化来的)。
		// 用 EXISTS 子查询而不是 JOIN: JOIN edge_devices 必须配合 DISTINCT, 否则
		// 同一设备多通道时会重复行, Count/Offset 全错。大小写归一 —— DB 存 'UART',
		// 前端筛选值是小写 'uart'。
		if hw := strings.TrimSpace(c.Query("hardware_type")); hw != "" {
			query = query.Where(
				"EXISTS (SELECT 1 FROM channels ch WHERE ch.id = edge_devices.channel_id AND ch.deleted_at IS NULL AND LOWER(ch.hardware_type) = ?)",
				strings.ToLower(hw),
			)
		}

		// search 是**服务端**全库检索 (§3.3.5): 与前端本地过滤同口径
		// (名称 / 设备类型 / 设备类型中文标签)。LOWER+LIKE 在 PostgreSQL 与 SQLite
		// 测试库都可用 (LOWER+LIKE 是两方言的交集写法)。
		// 注: 中文标签 (如 "BMS 电池管理系统") 由前端 getDeviceTypeLabel 派生,
		// 服务端搜不到中文标签 —— 类型筛选请走 device_type, 此处覆盖 name/type。
		if search := strings.TrimSpace(c.Query("search")); search != "" {
			like := "%" + strings.ToLower(search) + "%"
			query = query.Where("LOWER(name) LIKE ? OR LOWER(type) LIKE ?", like, like)
		}

		var total int64
		if err := query.Count(&total).Error; err != nil {
			logger.Warnf("[edge-devices-list] count failed: %v", err)
			Error(c, http.StatusInternalServerError, "failed to query edge devices")
			return
		}

		// 非 nil 空切片: 空集序列化为 [] 而非 null (与 handler_data_source.go 同约定)。
		devices := make([]models.EdgeDevice, 0)
		if err := query.
			Preload("Channel").Preload("Node").Preload("DeviceConfig").
			Order("edge_devices.id").
			Offset((page - 1) * pageSize).Limit(pageSize).
			Find(&devices).Error; err != nil {
			logger.Warnf("[edge-devices-list] query failed: %v", err)
			Error(c, http.StatusInternalServerError, "failed to query edge devices")
			return
		}

		// Enrich each device with latest sensor data from unified_data (C1 fix: batch query)
		//
		// 富化范围**只限当前页** (本任务的关键约束): 这里收集的 deviceIDs 是上面
		// Limit 之后的当前页, 而不是全表 —— 否则「分页」了但仍在全表捞数据,
		// 首屏开销与改前一样, 等于没优化 (DISTINCT ON 全表扫描 + 缓存逐设备查找)。
		type lastDataEntry struct {
			DeviceID   uint    `json:"device_id"`
			SensorName string  `json:"sensor_name"`
			Value      float64 `json:"value"`
			Unit       string  `json:"unit"`
		}
		// Collect all device IDs — current page only.
		deviceIDs := make([]uint, len(devices))
		for i, d := range devices {
			deviceIDs[i] = d.ID
		}
		// Single query: get latest 10 rows per device using DISTINCT ON (PostgreSQL)
		var allEntries []lastDataEntry
		if len(deviceIDs) > 0 {
			// 数据层时序化 (v3.4 §3.2.4): 缓存优先, miss 的设备回落原 DISTINCT ON SQL。
			var missed []uint
			cacheByDevice := make(map[uint][]lastDataEntry)
			for _, did := range deviceIDs {
				// 同 handler_overview.go：必须用 LatestValues 取回**全部**物理量，
				// 否则缓存命中时每设备只报 1 个物理量，与回落 SQL 的形状不一致。
				recs := LatestValues(did)
				if len(recs) > 0 {
					for _, rec := range recs {
						cacheByDevice[did] = append(cacheByDevice[did], lastDataEntry{DeviceID: rec.DeviceID, SensorName: rec.SensorName, Value: rec.Value, Unit: rec.Unit})
					}
				} else {
					missed = append(missed, did)
				}
			}
			// ============ 缓存 miss 不再回落 unified_data（2026-11-08，DROP 之后）============
			//
			// 原实现：对 missed 设备发一条 DISTINCT ON 查询去 unified_data 捞最新行。
			// DROP 后那条查询报 42P01，且 `Find` 的**错误没被检查** => 静默拿到空 fallback =>
			// 那些设备的 latest_data 为空，仅此而已（正好也是「没有数据」的诚实结果）。
			//
			// 为什么不改成查 telemetry_frame：**这里不需要它**。
			//	· 缓存由 SensorParserConsumer 每帧 SetLatestValue 持续更新（正常运行时是热的）；
			//	· 启动预热有帧表那一趟（WarmupLatestValuesFromFrames，覆盖所有有帧的设备）；
			//	· 所以 miss 只在「刚启动 + 预热未完成」或「该设备确实没数据」时发生 ——
			//	  两种情况下返回空都诚实。
			//
			// 若将来 miss 频率不可接受，正确修法是**加强预热**或给缓存加 TTL 刷新，
			// 而不是在这里补一条「从另一张表现查」的路径 —— 那会让「缓存」这个抽象不成立。
			_ = missed // 保留：上面的分组逻辑仍填好了 cacheByDevice
			for _, entries := range cacheByDevice {
				allEntries = append(allEntries, entries...)
			}
		}
		// Group by device ID
		dataByDevice := make(map[uint]map[string]float64, len(devices))
		for _, e := range allEntries {
			if dataByDevice[e.DeviceID] == nil {
				dataByDevice[e.DeviceID] = make(map[string]float64)
			}
			dataByDevice[e.DeviceID][e.SensorName] = e.Value
		}
		for i := range devices {
			if dm, ok := dataByDevice[devices[i].ID]; ok && len(dm) > 0 {
				devices[i].LastData = dm
			}
		}

		Success(c, gin.H{"items": devices, "total": total, "page": page, "page_size": pageSize})
	})

	// Get single edge device by id (v2.2 path for /devices/:id)
	// 方案 v3.3 T5 P2: candidates 静态路由 (§1.3 §九) — gin 静态段优先于
	// :id 通配符, 必须先注册以显式声明共存关系。
	// 注: /edge-devices/candidates（创建时选择继承哪个逻辑设备）已随逻辑设备
	// 概念层一起删除（2026-10-23）。换节点 / 恢复 / 合并历史三个新入口在本函数末尾。

	v1.GET("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// 2026-10-15：附带**轮询指令上限**，供前端在保存前给出可行动约束。
		//
		// 背景（用户报障"改频率→保存→刷新没变化"）：PUT /edge-devices/:id/commands
		// 有容量门（ValidateManifestCommandCapacity），开启条数 > MaxCommandsPerEdgeDevice
		// 时整单 400 拒绝、不落库；而前端此前只在 toast 里闪一下 → 用户刷新后看到旧值，
		// 以为"保存没生效"。把上限随设备详情下发，前端即可即时提示
		// "最多 N 条可同时轮询（已启用 M 条）"，把事后报错变成事前约束。
		//
		// 用**嵌入**而非包一层对象：models.EdgeDevice 无自定义 MarshalJSON，
		// 嵌入会将其字段平铺，响应形状对既有调用方完全不变，只多一个字段。
		// ⚠ 必须与**容量门用同一个来源**（节点申报值）。此前这里写的是常量，
		// 而闸门已改为读申报值 ⇒ 设备申报 32 时，闸门放行 5 条、这里却仍显示 3，
		// 前端会渲染出"已启用 5/3"这种自相矛盾的状态。
		// d 已 Preload("Node")，直接用它的 HardwareInfo，不再多查一次库。
		nodeLimit := nodemgr.MaxCommandsPerEdgeDevice
		if d.Node.NodeID != "" {
			nodeLimit = nodemgr.ManifestCommandLimitForNode(d.Node)
		}
		Success(c, struct {
			models.EdgeDevice
			MaxPollingCommands int `json:"max_polling_commands"`
		}{EdgeDevice: d, MaxPollingCommands: nodeLimit})
	})

	// Create edge device (v2.2 path for POST /devices)
	v1.POST("/edge-devices", func(c *gin.Context) {
		// B1 fix: bind to a separate DTO, then construct from allowed fields only
		var dto struct {
			Name           *string `json:"name"`
			Type           *string `json:"type"`
			NodeID         *string `json:"node_id"`
			ChannelID      *uint   `json:"channel_id"`
			Enabled        *bool   `json:"enabled"`
			IntervalMs     *int    `json:"interval_ms"`
			HardwareID     *string `json:"hardware_id"`
			DeviceConfigID *uint   `json:"device_config_id"`
			// CommandIntervals (optional): per-command polling interval map
			// (command_id → interval_ms, 0 = disabled). Validated against the
			// device type's schedulable command templates once dev.Type is
			// resolved (after DeviceConfig derivation, if any).
			CommandIntervals map[string]int `json:"command_intervals"`
			// F: inline channel creation — when channel_id is 0/absent and
			// channel is provided, the channel is created inside the same
			// transaction, eliminating the two-phase-commit orphan risk.
			Channel *struct {
				HardwareType *string          `json:"hardware_type"`
				HardwareID   *string          `json:"hardware_id"`
				Address      *string          `json:"address"`
				Config       *json.RawMessage `json:"config"`
				// bus_config carries the hex-encoded pin-route payload (the
				// same shape as Channel.BusConfig). The device wizard's inline
				// path typically omits it (no pin route to validate); a
				// caller that supplies one gets the same
				// validateChannelPeripheralConflicts gate as the dedicated
				// channel-create path.
				BusConfig *string `json:"bus_config"`
			} `json:"channel"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if dto.Type != nil && dto.DeviceConfigID != nil && *dto.DeviceConfigID != 0 {
			Error(c, http.StatusBadRequest, "type is derived from device_config_id")
			return
		}
		// channel_id is required unless an inline channel object is provided
		hasInlineChannel := dto.Channel != nil && dto.Channel.HardwareType != nil
		if dto.ChannelID == nil && !hasInlineChannel {
			Error(c, http.StatusBadRequest, "channel_id or channel is required")
			return
		}
		if dto.Name == nil || dto.NodeID == nil {
			Error(c, http.StatusBadRequest, "name and node_id are required")
			return
		}
		// Resolve effective device_config_id (0 when absent or explicitly 0)
		var cfgID uint
		if dto.DeviceConfigID != nil {
			cfgID = *dto.DeviceConfigID
		}
		// When no DeviceConfig template is provided, type must be supplied by the caller
		// and validated against the driver registry.
		if cfgID == 0 && (dto.Type == nil || strings.TrimSpace(*dto.Type) == "") {
			Error(c, http.StatusBadRequest, "type is required when device_config_id is not provided")
			return
		}
		// Resolve the effective channel ID: use channel_id when provided,
		// otherwise the inline channel object will be created inside the transaction.
		var effectiveChannelID uint
		if dto.ChannelID != nil {
			effectiveChannelID = *dto.ChannelID
		}
		dev := models.EdgeDevice{Name: *dto.Name, NodeID: *dto.NodeID}
		if dto.Enabled != nil {
			dev.Enabled = *dto.Enabled
		}
		if dto.IntervalMs != nil {
			dev.IntervalMs = *dto.IntervalMs
		}
		if dto.HardwareID != nil {
			dev.HardwareID = *dto.HardwareID
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			// Node 存在性必须先判：设计文档 §7.2 要求"node 不存在 → 404"。
			// 此前缺这一步，不存在的 node 会先撞上 channel 归属校验，报
			// "channel does not belong to node" 400，与文档口径不符
			// （2026-10-11 真机验证发现）。用 node_exists 哨兵把 404 与
			// 其它 400 错误在事务出口区分开。
			var nodeCount int64
			if err := tx.Model(&models.Node{}).Where("node_id = ?", *dto.NodeID).Count(&nodeCount).Error; err != nil {
				return err
			}
			if nodeCount == 0 {
				return errNodeNotFound
			}
			var bindingChannel models.Channel
			if effectiveChannelID > 0 {
				// Existing channel path
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", effectiveChannelID, *dto.NodeID).First(&bindingChannel).Error; err != nil {
					return fmt.Errorf("channel does not belong to node")
				}
				if err := validateTransportChannel(&bindingChannel); err != nil {
					return err
				}
			} else {
				// F: inline channel creation path
				chInput := dto.Channel
				hwType := strings.TrimSpace(*chInput.HardwareType)
				busType := strings.ToUpper(hwType) // hardware_type == bus_type for transport channels
				bindingChannel = models.Channel{
					NodeID:       *dto.NodeID,
					HardwareType: hwType,
					BusType:      busType,
					Enabled:      true,
				}
				if chInput.HardwareID != nil {
					bindingChannel.HardwareID = *chInput.HardwareID
				}
				if chInput.Address != nil && *chInput.Address != "" {
					bindingChannel.HardwareID = *chInput.Address
				}
				if chInput.Config != nil {
					bindingChannel.Config = string(*chInput.Config)
				}
				if chInput.BusConfig != nil {
					bindingChannel.BusConfig = *chInput.BusConfig
				}
				if err := validateTransportChannelType(&bindingChannel); err != nil {
					return err
				}
				// Lock the node row to prevent concurrent channel creation races
				var node models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", bindingChannel.NodeID).First(&node).Error; err != nil {
					return err
				}
				// 与 POST /channels 同一兜底（2026-10-03）：向导内联建 UART 通道时调用方
				// 通常不传 bus_config（原实现只在显式传入时才写，否则落空串），同样会
				// 造出"以后改不了波特率"的通道。这里按节点资源能力补齐。
				if strings.EqualFold(strings.TrimSpace(bindingChannel.HardwareType), "UART") {
					if err := ensureBusConfigExtras(&node, &bindingChannel); err != nil {
						return err
					}
				}
				// 引脚冲突校验以"该通道确有引脚路由"为前提。原缺陷是：向导内联路径
				// 原本不传 bus_config，于是这里被整段跳过 —— 但上面的
				// ensureUARTBusConfig 现在会按节点能力把 UART 补齐，所以 UART 走到
				// 这里**必然有** bus_config，校验不再被绕过（2026-10-03 P0 的路径入口）。
				//
				// 这里刻意**不**改成无条件调用：I2C/SPI 在内联路径上仍可能不带
				// bus_config，无条件调用会让 channelRoutePins 直接报
				// "I2C bus_config requires at least 2 bytes"，把既有的合法创建路径
				// 挡死（改错方向加严）。"建了一条 bus_config 为空的非 UART 通道"
				// 是另一类缺陷（节点端会拒收整份 manifest），不在本次引脚仲裁范围内。
				if bindingChannel.BusConfig != "" {
					if err := validateChannelPeripheralConflicts(tx, bindingChannel); err != nil {
						return err
					}
				}
				if err := tx.Create(&bindingChannel).Error; err != nil {
					return err
				}
				effectiveChannelID = bindingChannel.ID
			}
			dev.ChannelID = effectiveChannelID
			config, err := validateDeviceConfigForChannel(tx, cfgID, &bindingChannel)
			if err != nil {
				return err
			}
			if cfgID > 0 {
				// DeviceConfig path: type derived from config
				dev.Type = config.DeviceType
				dev.DeviceConfigID = config.ID
			} else {
				// No template: validate type against the driver registry
				devType := strings.TrimSpace(*dto.Type)
				if _, derr := driverRegistry.Get(devType); derr != nil {
					return fmt.Errorf("device type %q is not registered: %w", devType, derr)
				}
				dev.Type = devType
				dev.DeviceConfigID = 0
			}
			// Address gate: reject a bus name masquerading as a device address at
			// write time. Without this, the bad value is accepted silently and
			// only fails ~120s later inside dispatch (2026-09-20 incident).
			if err := validateEdgeDeviceAddress(driverRegistry, dev.Type, dev.HardwareID); err != nil {
				return err
			}
			// 总线族校验（2026-10-25，runtime R-12）：驱动声明的 HardwareTypes 必须
			// 包含当前通道。否则 UART 设备会被允许建在 I2C 通道上 —— dispatch 侧只按
			// 通道总线族选编码，会把 Modbus RTU 帧按 I2C 发出去（静默错配）。
			if err := channelSupportsDriverType(driverRegistry, dev.Type, &bindingChannel); err != nil {
				return err
			}
			// Uniqueness guard: the same slave address must not host two devices of the
			// same model on one channel. Multi-drop buses (e.g. SPI with several CS
			// lines, I2C with several addresses) stay valid because they map to
			// distinct channels or distinct hardware_id values. When hardware_id is
			// empty we fall back to (channel_id, type) so address-less devices are
			// still de-duplicated.
			if err := checkDeviceUniqueness(tx, dev.ChannelID, dev.Type, dev.HardwareID, 0); err != nil {
				// 三元重复是冲突语义 → 409（设计文档 §7.2"三元重复 → 409"，
				// 与本函数出口 errors.As(conflictError) 的既有映射一致）。
				// 2026-10-11 真机验证发现此前走 400。
				return newConflictError("%s", err.Error())
			}
			// Step 1: Create EdgeDevice (inside transaction)
			// command_intervals validation must run after dev.Type is resolved
			// (DeviceConfig-derived or explicit type). Unknown ids and
			// non-schedulable ids are rejected; negatives are normalized to 0.
			if len(dto.CommandIntervals) > 0 {
				intervalsJSON, vErr := validateAndNormalizeCommandIntervals(driverRegistry, dev.Type, dto.CommandIntervals)
				if vErr != nil {
					return vErr
				}
				// Capacity gate: same authority as the update path, so a device
				// can never be created with a configuration the collector will
				// silently refuse to apply.
				if vErr := ValidateManifestCommandCapacity(driverRegistry, dev.Type, dto.CommandIntervals, 0, NodeCommandLimitForDevice(tx, &dev)); vErr != nil {
					return vErr
				}
				dev.CommandIntervals = intervalsJSON
			}
			if err := tx.Create(&dev).Error; err != nil {
				return err
			}
			// 原位置重建场景: 同节点同通道同型号同地址的旧实例若存在 (刚删过又
			// 重建), 把它的标定数据复制到新实例。标定是"物理个体"的工厂值,
			// 跨设备继承在业务上是错的, 因此只认同通道同地址。
			if err := copyCalibrationIfInPlace(tx, &dev); err != nil {
				return err
			}
			// An explicit zero interval means "do not schedule".  The model has a
			// historical database default of 5000, which GORM otherwise substitutes
			// for a zero value during INSERT.
			if dto.IntervalMs != nil && *dto.IntervalMs == 0 {
				if err := tx.Model(&dev).UpdateColumn("interval_ms", 0).Error; err != nil {
					return err
				}
				dev.IntervalMs = 0
			}

			// Step 2: Ensure ConfigTemplates from driver's CommandTemplates (single source
			// of truth)。用 ensure 而不是 create：同通道 multi-drop 上已有同型设备时
			// 不会重复插入（否则每次创建/恢复都净增一条，最终撑爆固件 16 条上限）。
			var ch models.Channel
			if err := tx.First(&ch, dev.ChannelID).Error; err == nil {
				if err := ensureTemplatesFromDriver(tx, driverRegistry, &ch, &dev); err != nil {
					logger.Warnf("[edge-device-create] ensure ConfigTemplates: %v", err)
				}
			}

			return nil // commit transaction
		}); err != nil {
			if errors.Is(err, errNodeNotFound) {
				Error(c, http.StatusNotFound, "node not found")
				return
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			var conflict conflictError
			if errors.As(err, &conflict) {
				// §3.3-2/§3.3-3: 继承校验失败与存活实例冲突为 409 语义。
				Error(c, http.StatusConflict, err.Error())
				return
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// EmitConfigChange (outside transaction — event emission should not block rollback)
		var chForEvent models.Channel
		if db.First(&chForEvent, dev.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionCreate, chForEvent.NodeID, fmt.Sprint(dev.ID))
		}

		// Reload with associations for response
		db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&dev, dev.ID)
		SuccessWithCode(c, http.StatusCreated, dev)
	})

	// Update edge device (v2.2 path for PUT /devices/:id)
	v1.PUT("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		// Bind to a separate DTO, then validate and update the complete candidate state atomically.
		var dto struct {
			Name           *string `json:"name"`
			Type           *string `json:"type"`
			Enabled        *bool   `json:"enabled"`
			IntervalMs     *int    `json:"interval_ms"`
			HardwareID     *string `json:"hardware_id"`
			DeviceConfigID *uint   `json:"device_config_id"`
			ChannelID      *uint   `json:"channel_id"`
			NodeID         *string `json:"node_id"`
			Status         *string `json:"status"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// B2/I6：停用被规则引用的组成员 ⇒ 409（与删除守卫同精神）。
		if dto.Enabled != nil && !*dto.Enabled {
			if edgeID, perr := strconv.ParseUint(id, 10, 64); perr == nil {
				if !guardEdgeDeviceDisable(c, db, uint(edgeID)) {
					return
				}
			}
		}
		var d models.EdgeDevice
		var bindingChanged bool
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; err != nil {
				return err
			}
			targetNodeID, targetChannelID, configID := d.NodeID, d.ChannelID, d.DeviceConfigID
			if dto.NodeID != nil {
				targetNodeID = *dto.NodeID
			}
			if dto.ChannelID != nil {
				targetChannelID = *dto.ChannelID
			}
			if dto.DeviceConfigID != nil {
				configID = *dto.DeviceConfigID
			}
			bindingChanged = targetNodeID != d.NodeID || targetChannelID != d.ChannelID
			// G1: type may only be supplied by the caller on the driver-registry path (no DeviceConfig template).
			if dto.Type != nil && configID != 0 {
				return fmt.Errorf("type is derived from device_config_id")
			}
			// When there is no template, a caller-supplied type must be a registered driver.
			if configID == 0 {
				if dto.Type != nil {
					devType := strings.TrimSpace(*dto.Type)
					if devType == "" {
						return fmt.Errorf("type must not be empty")
					}
					if _, derr := driverRegistry.Get(devType); derr != nil {
						return fmt.Errorf("device type %q is not registered: %w", devType, derr)
					}
				} else if d.Type == "" {
					return fmt.Errorf("type is required when device_config_id is not provided")
				}
			}
			var targetChannel models.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", targetChannelID, targetNodeID).First(&targetChannel).Error; err != nil {
				return fmt.Errorf("channel does not belong to node")
			}
			if err := validateTransportChannel(&targetChannel); err != nil {
				return err
			}
			config, err := validateDeviceConfigForChannel(tx, configID, &targetChannel)
			if err != nil {
				return err
			}
			updates := map[string]interface{}{}
			if configID > 0 {
				updates["device_config_id"] = config.ID
				updates["type"] = config.DeviceType
			} else {
				updates["device_config_id"] = uint(0)
				if dto.Type != nil {
					updates["type"] = strings.TrimSpace(*dto.Type)
				}
			}
			if dto.Name != nil {
				updates["name"] = *dto.Name
			}
			if dto.Enabled != nil {
				updates["enabled"] = *dto.Enabled
			}
			if dto.IntervalMs != nil {
				updates["interval_ms"] = *dto.IntervalMs
			}
			if dto.HardwareID != nil {
				updates["hardware_id"] = *dto.HardwareID
			}
			if dto.NodeID != nil {
				updates["node_id"] = targetNodeID
			}
			if dto.ChannelID != nil {
				updates["channel_id"] = targetChannelID
			}
			if dto.Status != nil {
				updates["status"] = *dto.Status
			}
			// 换节点/通道 = 到了一个新的物理位置（2026-10-25）：旧 status/错误/init 进度
			// 都是上一处绑定的结论，必须复位；否则列表仍显示旧状态、新节点也不会重新
			// 走 init/校准（deviceinit 缓存以 edge_device id 为键，id 没有变）。
			if bindingChanged {
				if dto.Status == nil {
					updates["status"] = models.EdgeDeviceStatusPending
				}
				updates["error_code"] = 0
				updates["last_error"] = ""
				updates["init_state"] = "pending"
				updates["init_last_step"] = 0
				updates["init_total_steps"] = 0
				// 同上：旧的 last_data_at 属于上一个位置，留着会让 offlinedetector
				// 立刻把刚到新位置的设备判离线。
				updates["last_data_at"] = nil
				updates["config_version"] = ""
			}
			// Uniqueness guard on update: compute the candidate (channel_id, type,
			// hardware_id) triple after applying the requested changes, then reject
			// if it would collide with another device. excludeID = d.ID so the device
			// does not collide with itself. This closes the bypass where a PUT could
			// move a device onto a channel/address/type already taken by another.
			candidateType := d.Type
			if v, ok := updates["type"]; ok {
				candidateType, _ = v.(string)
			}
			candidateHardwareID := d.HardwareID
			if dto.HardwareID != nil {
				candidateHardwareID = *dto.HardwareID
			}
			if err := checkDeviceUniqueness(tx, targetChannelID, candidateType, candidateHardwareID, d.ID); err != nil {
				return err
			}
			// Address gate on the CANDIDATE values, before any write, so a rejected
			// update leaves the row untouched.
			//
			// It fires only when THIS write puts an address on the dispatch path:
			//   - the request explicitly supplies hardware_id, or
			//   - the device type changes, which re-binds the stored address to a
			//     different action catalog.
			// A write that touches neither (rename / enable / interval change on a
			// row created before this gate existed) is deliberately NOT re-validated:
			// rejecting it would turn pre-existing data into "cannot even rename
			// this device", and the contract requires existing rows keep working.
			// Repairing such a row stays possible by sending a legal hardware_id.
			if dto.HardwareID != nil || candidateType != d.Type {
				if err := validateEdgeDeviceAddress(driverRegistry, candidateType, candidateHardwareID); err != nil {
					return err
				}
			}
			// 总线族校验（同上）：换绑定后设备类型声明的总线必须覆盖目标通道。
			if err := channelSupportsDriverType(driverRegistry, candidateType, &targetChannel); err != nil {
				return err
			}
			// 命令容量门禁（2026-10-25 审核 F-03）：换节点后 command_intervals 不得
			// 超过**目标节点**申报的每设备上限，否则该节点整份 ConfigManifest 会被
			// 拒发（所有设备静默跑旧配置）。create 路径一直有这道门禁，PUT 曾缺失。
			candDev := d
			candDev.Type = candidateType
			candDev.ChannelID = targetChannelID
			if dto.IntervalMs != nil {
				candDev.IntervalMs = *dto.IntervalMs
			}
			if err := validateCommandCapacityForDevice(tx, driverRegistry, &candDev, targetChannelID); err != nil {
				return err
			}
			if err := tx.Model(&d).Updates(updates).Error; err != nil {
				return err
			}

			// 确保绑定的**目标通道**拥有驱动声明的模板。
			//
			// 为什么必须在这里也做一次：模板只在创建时生成，于是"设备先建好、驱动
			// 之后才获得可调度模板"的设备永远拿不到模板（现场逆变器 id=4 曾因此 0 命令）。
			//
			// 2026-10-25 两处修正（runtime R-03/R-06）：
			//   · 用 ensureTemplatesFromDriver（按 (node_id, write_data, read_length) 复用）
			//     替代 createTemplatesFromDriver：后者只 Create，重复保存/换通道都会
			//     重复插入，把 template_ids 撑爆（固件上限 16 条 ⇒ 整份 manifest 被拒）；
			//   · 用 **targetChannelID/candidateType** 而不是 d.ChannelID/d.Type ——
			//     Updates(map) 不会回写结构体，换通道时旧代码会把模板建到**旧通道**上，
			//     新通道反而没有模板（设备换节点后不轮询）。
			var tmplDev = d
			tmplDev.ChannelID = targetChannelID
			tmplDev.Type = candidateType
			if dto.Enabled != nil {
				tmplDev.Enabled = *dto.Enabled
			}
			var chForTmpl models.Channel
			if err := tx.First(&chForTmpl, targetChannelID).Error; err == nil {
				if err := ensureTemplatesFromDriver(tx, driverRegistry, &chForTmpl, &tmplDev); err != nil {
					// 与 POST 路径一致：模板生成失败记警告但不阻断设备更新。
					logger.Warnf("[edge-device-update] ensure ConfigTemplates: %v", err)
				}
			}

			return tx.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, d.ID).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
			} else {
				Error(c, http.StatusBadRequest, err.Error())
			}
			return
		}
		// 换绑定后清 deviceinit 进程内缓存：不清理的话新节点上的自动初始化会被
		// reserve() 以「already completed」拒绝（恢复端点同一处理）。
		if bindingChanged {
			if di := nodeMgr.DeviceInit(); di != nil {
				di.ClearCache(d.ID)
			}
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, ch.NodeID, fmt.Sprint(d.ID))
		}
		Success(c, d)
	})

	// Init edge device (trigger InitDevice via deviceinit.Orchestrator)
	v1.POST("/edge-devices/:id/init", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))

		// Load the edge device to get device type, channel, and node info
		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if !dev.Enabled {
			Error(c, http.StatusBadRequest, "edge device is disabled")
			return
		}
		if _, err := loadTransportChannel(db, dev.ChannelID, dev.NodeID); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// Resolve the MQTT device ID from the associated Node
		node, err := findNodeByID(db, dev.NodeID)
		if err != nil {
			Error(c, http.StatusBadRequest, "associated node not found")
			return
		}
		deviceID := node.NodeID

		// Use the deviceinit Orchestrator to trigger init
		orchestrator := nodeMgr.DeviceInit()
		if orchestrator == nil {
			Error(c, http.StatusInternalServerError, "device init orchestrator not available")
			return
		}

		// G3K: devices without an init sequence must not be marked "running"
		// with a 0/N progress bar. Short-circuit to "completed"/"not_required"
		// before touching the orchestrator's reservation cache.
		steps := orchestrator.GetInitSequence(dev.Type)
		if len(steps) == 0 {
			db.Model(&dev).Updates(map[string]interface{}{
				"init_state":       "completed",
				"init_last_step":   0,
				"init_total_steps": 0,
			})
			SuccessMsg(c, gin.H{
				"id": id, "status": "not_required", "message": "no init sequence for this device type",
				"device_type": dev.Type, "device_id": deviceID,
			}, "no init sequence for this device type")
			return
		}

		// Reserve and trigger through the orchestrator's single entry point. This
		// closes the API path's race with automatic online initialization.
		if !orchestrator.InitIfNeeded(dev, deviceID) {
			Error(c, http.StatusConflict, "device initialization already active or completed")
			return
		}

		// Update init state on the device record
		db.Model(&dev).Updates(map[string]interface{}{
			"init_state":       "running",
			"init_last_step":   0,
			"init_total_steps": len(steps),
		})

		SuccessMsg(c, gin.H{
			"id": id, "status": "running", "message": "init triggered",
			"device_type": dev.Type, "device_id": deviceID,
		}, "init triggered")
	})

	// Delete edge device (v2.2 path for DELETE /devices/:id)
	//
	// +delete_data 查询参数 (默认 false)。事务内: 处理 ConfigTemplate 归属 →
	// GORM 软删设备 → delete_data=true 时置 edge_devices.data_purge_requested
	// 标记; 数据硬删由 purge 后台任务异步分批执行，不在 API 事务里删数据。
	//
	// 2026-10-23: 不再有「缺逻辑身份则补建」这一步 —— 逻辑设备概念层已移除，
	// 删除标记直接落在设备自身 (data_purge_requested)。
	v1.DELETE("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		deleteData := c.Query("delete_data") == "true" || c.Query("delete_data") == "1"

		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// V-13 / SG-20：被来源组引用（或引用组上有规则）的边缘设备禁止删除。
		if !guardEdgeDeviceDelete(c, db, d.ID) {
			return
		}
		// Find node before deletion for event emission
		var ch models.Channel
		hasNode := db.First(&ch, d.ChannelID).Error == nil

		err := db.Transaction(func(tx *gorm.DB) error {
			var inst models.EdgeDevice
			if err := tx.First(&inst, d.ID).Error; err != nil {
				return err
			}
			if err := handleConfigTemplateOnDelete(tx, &inst); err != nil {
				return err
			}
			if err := tx.Delete(&models.EdgeDevice{}, inst.ID).Error; err != nil {
				return err
			}
			// data_purge_requested 是**单向闩锁**，必须两个方向都显式写（2026-10-24 P0）：
			//   - delete_data=true  → true（交给 purge 任务分批硬删）
			//   - delete_data=false → **false**（显式撤回既有删除意图）
			// 只写 true 会让「曾经勾选过删数据」的设备永久带着闩锁：用户之后
			// 「恢复 → 再不删数据地删除」时，purge 仍会硬删他要求保留的历史。
			if err := tx.Model(&models.EdgeDevice{}).Unscoped().
				Where("id = ?", inst.ID).
				Update("data_purge_requested", deleteData).Error; err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// EmitConfigChange
		if hasNode {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionDelete, ch.NodeID, fmt.Sprint(d.ID))
		}
		// 应答里的 data_purge_requested 必须**回读库**，不能回显入参 ——
		// 回显会让「库里的真实状态」与「用户看到的状态」在闩锁残留时分叉（原 P0 的一部分）。
		actualPurge := false
		var persisted models.EdgeDevice
		if err := db.Unscoped().Select("data_purge_requested").Where("id = ?", d.ID).
			First(&persisted).Error; err == nil {
			actualPurge = persisted.DataPurgeRequested
		}
		SuccessMsg(c, gin.H{
			"deleted":              id,
			"delete_data":          deleteData,
			"data_purge_requested": actualPurge,
		}, "deleted")
	})

	// Edge device routes group for :id sub-resources
	e := v1.Group("/edge-devices")

	// POST /api/v1/edge-devices/batch-delete — 批量删除边缘设备
	// 批量删除复用单删逻辑 (事务内逐条处理), 返回每条结果汇总。
	// delete_data 语义与单删一致。
	e.POST("/batch-delete", func(c *gin.Context) {
		var req struct {
			IDs        []uint `json:"ids" binding:"required,min=1,max=100"`
			DeleteData bool   `json:"delete_data"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// V-13 / SG-20：批量删除前**整体**守卫（任一台被来源组引用 ⇒ 409，不做部分删除）。
		if !guardEdgeDevicesBatchDelete(c, db, req.IDs) {
			return
		}

		type result struct {
			ID      uint   `json:"id"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		results := make([]result, 0, len(req.IDs))

		for _, id := range req.IDs {
			r := result{ID: id}
			var d models.EdgeDevice
			if err := db.First(&d, id).Error; err != nil {
				r.Error = err.Error()
				results = append(results, r)
				continue
			}

			err := db.Transaction(func(tx *gorm.DB) error {
				var inst models.EdgeDevice
				if err := tx.First(&inst, d.ID).Error; err != nil {
					return err
				}
				if err := handleConfigTemplateOnDelete(tx, &inst); err != nil {
					return err
				}
				if err := tx.Delete(&models.EdgeDevice{}, inst.ID).Error; err != nil {
					return err
				}
				// 与单删同一语义：两个方向都显式写（2026-10-24 P0，见单删处注释）。
				if err := tx.Model(&models.EdgeDevice{}).Unscoped().
					Where("id = ?", inst.ID).
					Update("data_purge_requested", req.DeleteData).Error; err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Success = true
			}
			results = append(results, r)
		}

		succeeded := 0
		for _, r := range results {
			if r.Success {
				succeeded++
			}
		}
		Success(c, gin.H{
			"total":     len(results),
			"succeeded": succeeded,
			"failed":    len(results) - succeeded,
			"results":   results,
		})
	})

	// POST /api/v1/edge-devices/batch-update — 批量管理：启用 / 停用 / 设置采集间隔。
	//
	// 产品语义（docs/分析/边缘设备批量管理与历史设备-2026-10-25.md）：
	//   · 逐条独立执行并返回逐条结果（与 batch-delete 一致；不做"一条不合规整批回滚"）；
	//   · 已删除/不存在的 id → 该条失败；被来源组规则引用的设备**停用** → 该条失败
	//     （沿用单台 PUT 停用的守卫，错误里带组名/规则名），其余设备照常成功；
	//   · enabled / interval_ms 至少提供一个；ids 1..100（去重）；interval 0..3600000（0=不调度）；
	//   · 每条成功后向该设备所在节点发配置变更事件（节点需要重新拉 manifest）。
	e.POST("/batch-update", func(c *gin.Context) {
		var req struct {
			IDs        []uint `json:"ids"`
			Enabled    *bool  `json:"enabled"`
			IntervalMs *int   `json:"interval_ms"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Enabled == nil && req.IntervalMs == nil {
			Error(c, http.StatusBadRequest, "enabled or interval_ms is required")
			return
		}
		if req.IntervalMs != nil && (*req.IntervalMs < 0 || *req.IntervalMs > 3600000) {
			Error(c, http.StatusBadRequest, "interval_ms must be between 0 and 3600000")
			return
		}
		ids := normalizeBatchIDs(req.IDs)
		if len(ids) == 0 {
			Error(c, http.StatusBadRequest, "ids is required")
			return
		}
		if len(ids) > 100 {
			Error(c, http.StatusBadRequest, "at most 100 ids per request")
			return
		}

		type batchUpdateResult struct {
			ID         uint   `json:"id"`
			Success    bool   `json:"success"`
			Error      string `json:"error,omitempty"`
			Enabled    *bool  `json:"enabled,omitempty"`
			IntervalMs *int   `json:"interval_ms,omitempty"`
		}
		results := make([]batchUpdateResult, 0, len(ids))
		for _, id := range ids {
			r := batchUpdateResult{ID: id}
			var nodeID string
			err := db.Transaction(func(tx *gorm.DB) error {
				var inst models.EdgeDevice
				if err := tx.First(&inst, id).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return fmt.Errorf("设备不存在或已删除")
					}
					return err
				}
				updates := map[string]interface{}{}
				if req.Enabled != nil {
					if !*req.Enabled && inst.Enabled {
						block, gerr := edgeDeviceDisableBlockReason(tx, inst.ID)
						if gerr != nil {
							return gerr
						}
						if block != "" {
							return errors.New(block)
						}
					}
					updates["enabled"] = *req.Enabled
				}
				if req.IntervalMs != nil {
					updates["interval_ms"] = *req.IntervalMs
				}
				if err := tx.Model(&models.EdgeDevice{}).Where("id = ?", inst.ID).
					Updates(updates).Error; err != nil {
					return err
				}
				// 配置变更事件需要节点 id：在事务内读通道（失败不阻断更新）。
				var ch models.Channel
				if err := tx.First(&ch, inst.ChannelID).Error; err == nil {
					nodeID = ch.NodeID
				}
				return nil
			})
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Success = true
				if req.Enabled != nil {
					v := *req.Enabled
					r.Enabled = &v
				}
				if req.IntervalMs != nil {
					v := *req.IntervalMs
					r.IntervalMs = &v
				}
				if nodeID != "" {
					nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice,
						nodemgr.CfgActionUpdate, nodeID, fmt.Sprint(id))
				}
			}
			results = append(results, r)
		}
		succeeded := 0
		for _, r := range results {
			if r.Success {
				succeeded++
			}
		}
		Success(c, gin.H{
			"total":     len(results),
			"succeeded": succeeded,
			"failed":    len(results) - succeeded,
			"results":   results,
		})
	})

	// POST /api/v1/edge-devices/deleted/batch-delete — 历史设备的「彻底删除」。
	//
	// 产品语义（2026-10-25 批量管理/历史设备）：
	//   · 只接受**已软删**设备；提交的是「记录 + 全部历史数据」的后台清理队列
	//     （data_purge_requested=true），由 DevicePurger 分批硬删，**开始后不可恢复**；
	//   · 提交成功后立即 Wake purger —— 否则用户最长要等 24h 才真正删掉；
	//   · 逐条结果：不存在 / 未删除 → 该条失败；已提交清理 / 正在清理 → 幂等成功（queued）；
	//   · 明确不提供"只删记录保留数据"（会制造无主孤儿数据）。
	e.POST("/deleted/batch-delete", func(c *gin.Context) {
		var req struct {
			IDs []uint `json:"ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		ids := normalizeBatchIDs(req.IDs)
		if len(ids) == 0 {
			Error(c, http.StatusBadRequest, "ids is required")
			return
		}
		if len(ids) > 100 {
			Error(c, http.StatusBadRequest, "at most 100 ids per request")
			return
		}

		type historyDeleteResult struct {
			ID      uint   `json:"id"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
			Queued  bool   `json:"queued,omitempty"`
		}
		results := make([]historyDeleteResult, 0, len(ids))
		wake := false
		for _, id := range ids {
			r := historyDeleteResult{ID: id}
			var inst models.EdgeDevice
			err := db.Unscoped().First(&inst, id).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				r.Error = "设备不存在"
			case err != nil:
				r.Error = err.Error()
			case !inst.DeletedAt.Valid:
				r.Error = "设备未被删除；在役设备请在设备列表用「删除」（软删）"
			case inst.DataPurgeStartedAt != nil:
				// 后台已认领：幂等成功（数据正在删，不可恢复）。
				r.Success, r.Queued = true, true
			default:
				if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", id).
					Update("data_purge_requested", true).Error; err != nil {
					r.Error = err.Error()
				} else {
					r.Success, r.Queued = true, true
					wake = true
				}
			}
			results = append(results, r)
		}
		if wake {
			// 立即唤醒后台清理（非阻塞；未接线时由定时任务兜底）。
			wakeDevicePurger()
		}
		succeeded := 0
		for _, r := range results {
			if r.Success {
				succeeded++
			}
		}
		Success(c, gin.H{
			"total":     len(results),
			"succeeded": succeeded,
			"failed":    len(results) - succeeded,
			"results":   results,
		})
	})

	// GET /api/v1/edge-devices/:id/data-summary — 删除确认弹窗 + 合并历史
	// 的**数据量估算**（不精确计数）。
	//
	// 2026-10-25 口径修正（用户反馈「合并历史/恢复设备功能逻辑还有大问题」）：
	//
	// 旧响应把「目标设备」和「源设备」混在同一个 items 里，total_rows 是两者
	// 之和。合并弹窗直接把它当「预计迁移行数」展示 ⇒ 数字系统性虚高（把目标
	// 自己的历史也算成要迁移的），明细表里目标设备还被列成一行「源设备」。
	// 现在响应拆成互不混用的三块：
	//   target      —— 目标设备自身的数据量/时间范围（删除确认弹窗用）
	//   sources     —— 每个源设备一项，state ∈ deleted/live/missing
	//   target_rows / source_rows —— 两组各自的合计；只有该组全部估算成功才出现
	//
	// 估算降级（契约不变）：超过 EstimateTimeout 或解析失败时省略 rows 字段，
	// 绝不阻塞端点。区别是现在**整个请求共用一个 3s 截止时间**，而不是每个来源
	// 各开一个 3s（旧实现 100 个源最坏 300s，是一个可被前端拖死的端点）。
	e.GET("/:id/data-summary", func(c *gin.Context) {
		id, convErr := strconv.ParseUint(c.Param("id"), 10, 32)
		if convErr != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		// 目标允许是软删设备：删除确认弹窗也要能查「这台待删设备有多少数据」。
		// 合并动作的目标必须存活 —— 那条校验在 merge-history 自己做。
		var d models.EdgeDevice
		if err := db.Unscoped().First(&d, uint(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// source_ids 解析：逗号分隔整数；去重；上限与 merge-history 一致（100）。
		sourceIDs := make([]uint, 0, 8)
		seen := map[uint]struct{}{d.ID: {}}
		if raw := strings.TrimSpace(c.Query("source_ids")); raw != "" {
			for _, part := range strings.Split(raw, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				v, perr := strconv.ParseUint(part, 10, 32)
				if perr != nil {
					Error(c, http.StatusBadRequest, "invalid source_ids (comma-separated integers expected)")
					return
				}
				if v == 0 {
					continue
				}
				if _, dup := seen[uint(v)]; dup {
					continue
				}
				seen[uint(v)] = struct{}{}
				sourceIDs = append(sourceIDs, uint(v))
			}
		}
		if len(sourceIDs) > 100 {
			Error(c, http.StatusBadRequest, "at most 100 source_ids per request")
			return
		}

		// 全请求共享一个估算截止时间（见上方注释）。
		estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
		defer cancel()

		// 时间列清单只查一次（HasTable 是每表一次系统目录查询）。summarize 会被
		// 调用最多 101 次（目标 + 100 个源），放在闭包里就是 300+ 次多余查询。
		tsTables := datalifecycle.ExistingTimeseriesTables(db)

		summarize := func(deviceID uint) (gin.H, bool) {
			item := gin.H{
				"source_id":  deviceID,
				"rows":       nil,
				"first_time": nil,
				"last_time":  nil,
			}
			var named models.EdgeDevice
			if err := db.Unscoped().Select("id", "name", "type", "deleted_at").First(&named, deviceID).Error; err == nil {
				item["device_name"] = named.Name
				// device_type 供合并弹窗做「同型号」校验/展示（2026-10-25 产品重做）。
				item["device_type"] = named.Type
				if named.DeletedAt.Valid {
					item["state"] = "deleted"
				} else {
					item["state"] = "live"
				}
			} else {
				// 没有设备行 ≠ 没有数据：设备行已被硬删的「孤儿历史」正是合并历史
				// 要救回的对象。state=missing 只表示「没有设备行」，前端据此提示。
				item["state"] = "missing"
			}
			rowsKnown := false
			if item["state"] == "missing" {
				// 没有设备行（孤儿历史）：计划器估算会凭空给出十几行（runtime R-07），
				// 这条路径本来就低频，改用有界精确计数。
				if rows, ok := datalifecycle.CountDeviceRowsCapped(estCtx, db, deviceID); ok {
					item["rows"] = rows
					rowsKnown = true
				}
			} else if rows, ok := datalifecycle.EstimateDeviceRows(estCtx, db, deviceID); ok {
				item["rows"] = rows
				rowsKnown = true
			}
			// 时间范围: 两端各取一行（ORDER BY timestamp ASC/DESC LIMIT 1），
			// 走 (device_id, timestamp) 索引，成本 O(log n)。两个坑都已避开：
			//  1. 不用 min()/max() 聚合 —— SQLite 把 timestamp 聚合成文本，GORM
			//     扫回 time.Time 会静默失败（实测 first_time 恒为 null）；
			//  2. 不复用同一个 struct 做两次 Find —— GORM 的 Find 在 struct 已带
			//     主键时会自动追加 WHERE id = ?（实测 last_time 恒等于 first_time）。
			//
			// 三张时序表的时间列**不同名**（unified_data/device_data 是 timestamp，
			// L2 帧表 telemetry_frame 是 ts），必须按表名分支取列 —— 把表名塞进
			// 同一个 "ORDER BY timestamp" 循环会让帧表那条 SQL 报列不存在。
			// 帧表已随 L2 成为主路径（评审 B-3），漏掉它的后果是「设备明明有帧数据，
			// 删除确认弹窗却显示未开始采集」。
			var first, last *time.Time
			for _, table := range tsTables {
				var lo, hi []time.Time
				if err := db.WithContext(estCtx).Table(table.Name).
					Where("device_id = ?", deviceID).
					Order(table.TSColumn+" ASC").Limit(1).Pluck(table.TSColumn, &lo).Error; err == nil && len(lo) > 0 {
					if first == nil || lo[0].Before(*first) {
						t := lo[0]
						first = &t
					}
				}
				if err := db.WithContext(estCtx).Table(table.Name).
					Where("device_id = ?", deviceID).
					Order(table.TSColumn+" DESC").Limit(1).Pluck(table.TSColumn, &hi).Error; err == nil && len(hi) > 0 {
					if last == nil || hi[0].After(*last) {
						t := hi[0]
						last = &t
					}
				}
			}
			if first != nil {
				item["first_time"] = first
			}
			if last != nil {
				item["last_time"] = last
			}
			return item, rowsKnown
		}

		targetItem, targetKnown := summarize(d.ID)
		sourceItems := make([]gin.H, 0, len(sourceIDs))
		var sourceRows int64
		sourceRowsKnown := len(sourceIDs) > 0
		for _, sid := range sourceIDs {
			item, known := summarize(sid)
			sourceItems = append(sourceItems, item)
			if known {
				sourceRows += item["rows"].(int64)
			} else {
				sourceRowsKnown = false
			}
		}
		out := gin.H{
			"target":  targetItem,
			"sources": sourceItems,
		}
		if targetKnown {
			out["target_rows"] = targetItem["rows"]
		}
		// source_rows 只在**全部源**都估算成功时出现：报一个偏小的合计比不报更危险
		// （会把「百万行」说成「几十万行」）。目标自己的行数走 target_rows，两者绝不相加。
		if sourceRowsKnown {
			out["source_rows"] = sourceRows
		}
		Success(c, out)
	})

	// GET /api/v1/edge-devices/:id/latest-values
	//
	// 返回该设备**每个传感器**各自的最新值（一帧里可能有 N 个物理量，
	// 也可能来自**多条命令**）。
	//
	// 为什么需要它（2026-10-18 实测缺陷）：
	//   /latest-data 返回的是 device_data 的**一条**原始记录。对 Modbus 类
	//   设备（JBD BMS）没问题 —— 一条命令读一段寄存器，解析出全部 29 个物理量。
	//   但对 **ASCII 一问一答**类设备（Techfine 逆变器）就致命：它每个轮询周期
	//   要发 9 条独立命令，每条产生**一条** device_data，各带 2~14 个物理量。
	//   前端取"最新那一条" ⇒ 只能看到**最后执行的那条命令**的产物，另外 33 个
	//   字段在库里但界面永远看不到 —— 表现为"逆变器页面大部分是空的"。
	//
	// 实现与 /overview 同口径（复用 latestValueCache）：缓存优先，miss 的设备
	// 回落按 sensor_name 分组取各自最新。**必须用 LatestValues 而不是
	// LatestValue** —— 后者只返回最后写入的那一条，会重现同一缺陷。
	e.GET("/:id/latest-values", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))

		type latestValue struct {
			SensorName  string    `json:"sensor_name"`
			Value       float64   `json:"value"`
			Unit        string    `json:"unit"`
			StringValue string    `json:"string_value,omitempty"`
			Timestamp   time.Time `json:"timestamp"`
		}
		out := make([]latestValue, 0, 32)

		// 缓存优先。缓存键是 edge_device_id —— 即本端点的 :id。
		for _, rec := range LatestValues(uint(id)) {
			out = append(out, latestValue{
				SensorName: rec.SensorName,
				Value:      rec.Value,
				Unit:       rec.Unit,
				Timestamp:  rec.Timestamp,
			})
		}

		if len(out) == 0 {
			// 缓存 miss：按**传感器**各取最新一条。
			//
			// ⚠ 这里**不能**照抄 /overview 的回落 SQL：那条用的是
			//   DISTINCT ON (device_id) ... ORDER BY created_at DESC，
			// 即"取该设备最新的一条记录"——对多命令设备正是本端点要修的那个
			// 缺陷。必须按 sensor_name 分组取各自的最新时间戳。
			// 缓存 miss：按**传感器**各取最新一条。
			//
			// ⚠ 这里**不能**照抄 /overview 的回落 SQL：那条用的是
			//   DISTINCT ON (device_id) ... ORDER BY created_at DESC，
			// 即"取该设备最新的一条记录"——对多命令设备正是本端点要修的那个
			// 缺陷。必须按 sensor_name 分组取各自的最新时间戳。
			//
			// 2026-11-07 (task-25, P0 潜伏 OOM)：原实现是
			//
			//	Where("device_id = ?").Order("sensor_name ASC, timestamp DESC").Find(&rows)
			//
			// **没有 Limit、没有时间窗** ⇒ 把该设备全部历史行拉进 Go 内存再在
			// 应用侧去重。生产实测 device_id=14 有 15,212,588 行：
			//
			//	15.2M 行 × ~100 B ≈ 1.5 GB 常驻内存
			//	SQL 侧实测 36 秒（EXPLAIN ANALYZE：Parallel Seq Scan + 15M 行 Sort）
			//
			// 结果**完全正确**，所以只验结果的用例抓不到它 —— 这是典型的
			// "对的答案、错的过程"。触发条件又很隐蔽（缓存 miss 才进），
			// 重启后第一次请求 / 缓存被清空时才暴露。
			//
			// 改成 SQL 侧去重 + 时间窗，见 latestValuesMissRows。
			//
			// ── 2026-11-08 硬切换：先帧表，再 unified_data ──
			//
			// unified_data 已于 2026-11-08 停写。它**不是空的**，而是保留期内的旧数据，
			// 所以 latestValuesMissRows 现在返回的是「停写那一刻的值」，且越来越旧 ——
			// 但**不会报错**。缓存 miss（重启后首次请求 / 显式清缓存）时用户会看到
			// 一个不随时间变化的时间戳，界面看起来正常。
			//
			// 必须与 /latest-data 的 latestSensorViews 走**同一个数据源顺序**：
			// 那边的注释写着「两者若不一致，详情页与数据面板会显示不同的数值，
			// 且这种不一致只在缓存冷热切换时出现，极难复现」—— 现在正是那个时刻。
			rows, err := latestValuesMissRowsFromFramesOrDB(c.Request.Context(), db, uint(id))
			if err != nil {
				// 与旧实现一致：查询错误不 500，记日志后返回空（前端显示"—"）。
				logger.Warnf("[latest-values] miss fallback failed device=%d: %v", id, err)
			}
			for _, r := range rows {
				out = append(out, latestValue{
					SensorName: r.SensorName,
					Value:      r.Value,
					Unit:       r.Unit,
					Timestamp:  r.Timestamp,
				})
			}
		}

		Success(c, out)
	})

	// GET /api/v1/edge-devices/:id/data —— **已删除**（2026-11-11，破坏性更新）
	//
	// 它读的是 device_data 的分页原始记录（含读时合成的 sensors）。device_data 整表退役后
	// 这个端点没有任何数据来源 —— 不是暂时空，是永远空。
	//
	// 前端替代路径（另一个 teammate 同步改前端）：
	//   · 数据面板表格 → GET /devices/:id/history（一次返回 []UnifiedData，一字段一行）
	//   · 实时读数     → GET /:id/latest-values（本来就有，且是本仓唯一可信的实时口径）
	//
	// 保留这条注释而不是干脆删干净：handler 按路由前缀注册，后人 grep /data 时
	// 需要一眼看到它被人有意删了、替代在哪，否则很容易照着老文档再加回来（加回来
	// 只会得到 42P01）。
	// 方案：docs/设计/原始数据退役-改造方案-2026-11-11.md §1.3 读路径表第 1 行。

	// GET /api/v1/edge-devices/deleted — 可恢复的已删除设备列表。
	//
	// 这是「真正需求」的另一半：设备从节点 A 换到节点 B 时，用户可能先把旧
	// 记录删了（换线/重装/误删）。删掉的是**设备行**，历史数据还在（软删不删
	// 数据）。恢复语义 = 把同一条 edge_devices 行恢复回来并改绑新节点 ——
	// 因为 unified_data.device_id 指向的就是这条行的 id，历史天然连续。
	//
	// 2026-10-25（用户反馈「恢复已删除设备功能逻辑还有大问题」）：
	//   · 分页：旧实现 Limit 200 且没有 total/offset，超过 200 台后更早的旧设备
	//     永远翻不到，调用方也不知道被截断；
	//   · 整个列表共用一个估算截止时间：旧实现每行各开一个 3s，200 行最坏 600s；
	//   · 暴露 purge 状态（data_purge_requested / purge_in_progress）：正在清理
	//     数据的设备不能再恢复（见 restore 的互斥说明），前端必须提前看得到。
	e.GET("/deleted", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 50
		}
		query := db.Unscoped().Model(&models.EdgeDevice{}).Where("deleted_at IS NOT NULL")
		// device_type 过滤：合并历史弹窗只列「与目标同型号」的已删除设备（2026-10-25），
		// 服务端过滤避免其它型号把首页占满、同型号历史翻不到。
		if dt := strings.TrimSpace(c.Query("device_type")); dt != "" {
			query = query.Where("type = ?", dt)
		}
		// search：名称/型号模糊（大小写不敏感）—— 历史设备列表的搜索框（2026-10-25）。
		if search := strings.TrimSpace(c.Query("search")); search != "" {
			like := "%" + strings.ToLower(search) + "%"
			query = query.Where("LOWER(name) LIKE ? OR LOWER(type) LIKE ?", like, like)
		}
		var total int64
		if err := query.Count(&total).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		var devices []models.EdgeDevice
		if err := query.Order("deleted_at DESC").Order("id").
			Offset((page - 1) * pageSize).Limit(pageSize).Find(&devices).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// 全列表共享一个估算截止时间（见上方注释）。
		estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
		defer cancel()
		items := make([]gin.H, 0, len(devices))
		for i := range devices {
			d := devices[i]
			item := gin.H{
				"id":                   d.ID,
				"name":                 d.Name,
				"type":                 d.Type,
				"device_type":          d.Type,
				"hardware_id":          d.HardwareID,
				"node_id":              d.NodeID,
				"channel_id":           d.ChannelID,
				"last_data_at":         d.LastDataAt,
				"data_purge_requested": d.DataPurgeRequested,
				"purge_in_progress":    d.DataPurgeStartedAt != nil,
			}
			if d.DeletedAt.Valid {
				item["deleted_at"] = d.DeletedAt.Time
			}
			if d.DataPurgeStartedAt != nil {
				item["purge_started_at"] = *d.DataPurgeStartedAt
			}
			// 数据量估算（超时/失败省略该字段，不阻塞列表）。
			if rows, ok := datalifecycle.EstimateDeviceRows(estCtx, db, d.ID); ok {
				item["row_estimate"] = rows
			}
			items = append(items, item)
		}
		Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
	})

	// POST /api/v1/edge-devices/:id/restore — 恢复已删除设备并改绑节点/通道。
	//
	// 语义: 清除 deleted_at + 改 node_id/channel_id (+ 可选 name/interval_ms)。
	// 因为 device_id 不变，历史数据自动连续 —— 这就是用户最初要的「直接复用」。
	//
	// 校验: 目标设备必须处于软删状态 (否则 409，避免把「恢复」当成「改绑定」的
	// 后门)；新通道必须属于新节点；通道唯一性守卫与创建/更新路径同一套。
	//
	// 2026-10-25 补齐（用户反馈「恢复设备功能逻辑还有大问题」）:
	//   · 与 create/update 对齐的校验：通道必须是已启用的**传输**通道（GPIO/PWM
	//     等外设资源不能挂设备）、device_config 与通道硬件类型必须兼容、设备类型
	//     声明的总线类型必须与通道匹配。旧实现只校验「通道属于节点」，于是可以把
	//     UART 设备恢复到 I2C/GPIO 通道上 —— 恢复成功但永远采不到数据（静默失败）。
	//   · 与 DevicePurger 互斥：清理任务已认领（data_purge_started_at 非 nil）时
	//     409。行锁保证「认领」与「复活」只有一个生效，不会出现 purge 删掉刚恢复
	//     设备的历史、再把它物理删掉。
	//   · 复位运行时状态（status/error/init_*）并清 deviceinit 内存缓存：否则换到
	//     新节点后 orchestrator 仍认为「这台设备已初始化完成」（缓存以 edge_device
	//     id 为键，恢复复用了同一个 id），新节点上的 init/校准流程会被直接拒绝。
	e.POST("/:id/restore", func(c *gin.Context) {
		id, convErr := strconv.ParseUint(c.Param("id"), 10, 32)
		if convErr != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		var dto struct {
			NodeID     *string `json:"node_id"`
			ChannelID  *uint   `json:"channel_id"`
			Name       *string `json:"name"`
			IntervalMs *int    `json:"interval_ms"`
			// HardwareID 可选：历史行可能带着非法地址（如把总线名 "UART1" 当从站
			// 地址）。恢复时允许一并修正，否则只能恢复出一个 dispatch 永远失败的设备。
			HardwareID *string `json:"hardware_id"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if dto.NodeID == nil || strings.TrimSpace(*dto.NodeID) == "" {
			Error(c, http.StatusBadRequest, "node_id is required")
			return
		}
		if dto.ChannelID == nil || *dto.ChannelID == 0 {
			Error(c, http.StatusBadRequest, "channel_id is required")
			return
		}

		var out models.EdgeDevice
		err := db.Transaction(func(tx *gorm.DB) error {
			var inst models.EdgeDevice
			// 行锁：与 DevicePurger 的认领 UPDATE 串行化（见上方注释）。
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Unscoped().First(&inst, uint(id)).Error; err != nil {
				return err
			}
			if !inst.DeletedAt.Valid {
				return newConflictError("设备 #%d 未被删除，无需恢复", inst.ID)
			}
			if inst.DataPurgeStartedAt != nil {
				return newConflictError("设备 #%d 的历史数据正在被后台清理，此时无法恢复；清理开始后删除不可撤销", inst.ID)
			}
			nodeID := strings.TrimSpace(*dto.NodeID)
			var node models.Node
			if err := tx.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errNodeNotFound
				}
				return err
			}
			var ch models.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND node_id = ?", *dto.ChannelID, nodeID).First(&ch).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return newConflictError("通道 #%d 不属于节点 %s", *dto.ChannelID, nodeID)
				}
				return err
			}
			// 传输通道门禁（与创建/更新同一套）：禁用通道、GPIO/PWM 外设资源、
			// hardware_type 与 bus_type 不一致都在这里拦下，避免「恢复成功但采不到数」。
			if err := validateTransportChannel(&ch); err != nil {
				return err
			}
			// 有 device_config 模板时，模板声明的硬件类型必须与通道兼容。
			if _, err := validateDeviceConfigForChannel(tx, inst.DeviceConfigID, &ch); err != nil {
				return err
			}
			// 驱动声明的总线类型必须包含这条通道（只对注册了驱动的类型生效；
			// 历史/未注册驱动的行不因此被永久锁死）。
			if err := channelSupportsDriverType(driverRegistry, inst.Type, &ch); err != nil {
				return err
			}
			// 地址门禁（与 create/update 同一套，2026-10-25）：历史行可能带着非法
			// 地址，恢复后要过 ~120s 才在 dispatch 静默失败。允许在恢复请求里直接
			// 传 hardware_id 修正它（设备处于软删状态时无法走普通编辑）。
			candidateHW := inst.HardwareID
			if dto.HardwareID != nil {
				candidateHW = strings.TrimSpace(*dto.HardwareID)
			}
			if _, derr := driverRegistry.Get(inst.Type); derr == nil {
				if err := validateEdgeDeviceAddress(driverRegistry, inst.Type, candidateHW); err != nil {
					return fmt.Errorf("%w（该设备历史地址为 %q；可在恢复请求中带 hardware_id 指定合法地址）", err, candidateHW)
				}
			}
			// 命令容量门禁（与创建/PUT 同一套，2026-10-25 审核 F-03）：设备的
			// command_intervals 不能超过目标节点申报的「每设备最大命令数」，否则该
			// 节点整份 ConfigManifest 会被拒发（所有设备静默跑旧配置）。
			if err := validateCommandCapacityForDevice(tx, driverRegistry, &inst, ch.ID); err != nil {
				return err
			}
			if err := checkDeviceUniqueness(tx, ch.ID, inst.Type, candidateHW, inst.ID); err != nil {
				return newConflictError("%s；如果目的是把旧设备的历史并入正常在用的设备，请使用「合并历史」", err.Error())
			}
			updates := map[string]interface{}{
				"deleted_at": nil,
				"node_id":    nodeID,
				"channel_id": ch.ID,
				"status":     models.EdgeDeviceStatusPending,
				"error_code": 0,
				"last_error": "",
				// ⚠ 必须清掉删除数据标记（2026-10-24 审核 P0）。
				//
				// data_purge_requested 是**单向闩锁**：DELETE?delete_data=true 置 true，
				// 之后只有 purge 任务在「发现设备已复活」时才会清回 false。若恢复端点
				// 不清它，就会出现这条路径：
				//   删（勾选删数据）→ 恢复 → 再删（不勾选）
				// 第二次 DELETE 因为 delete_data=false **不写**该列，标记仍是 true，
				// 于是 purge 任务照旧硬删用户明确要求保留的历史 —— 静默且不可恢复。
				// 恢复的语义就是「撤回删除意图」，与 purge 的 AbandonedRevived 分支同义。
				"data_purge_requested":  false,
				"data_purge_started_at": nil,
				// 运行时状态复位（2026-10-25）：新节点必须重新走 init/校准流程。
				"init_state":       "pending",
				"init_last_step":   0,
				"init_total_steps": 0,
				// 恢复 = 回到可用状态。删除前若被停用过（enabled=false），不显式复位
				// 的话恢复出来的设备永远不会被节点调度（用户看到「恢复成功但一直没数据」）。
				"enabled": true,
				// last_data_at 是**上一个位置**的事实。不清掉的话 offlinedetector 下一
				// 个 tick 就会用这条陈旧时间把 pending 设备判成 offline（并触发
				// source-offline hook）；清成 NULL 后由「pending 以 updated_at 为起点」
				// 的规则重新计时（见 offlinedetector.checkEdgeDevicesOffline）。
				"last_data_at":   nil,
				"config_version": "",
			}
			if dto.HardwareID != nil {
				updates["hardware_id"] = candidateHW
			}
			if dto.Name != nil && strings.TrimSpace(*dto.Name) != "" {
				updates["name"] = strings.TrimSpace(*dto.Name)
			}
			if dto.IntervalMs != nil && *dto.IntervalMs >= 0 {
				updates["interval_ms"] = *dto.IntervalMs
			}
			if err := tx.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", inst.ID).
				Updates(updates).Error; err != nil {
				return err
			}
			// 恢复回来时按驱动补齐 ConfigTemplate（原模板可能已在删除时清理）。
			// 只在**没有任何归属模板**时补建 —— 与 PUT 路径同一幂等守卫，避免重复
			// 插入把 channel.template_ids 撑爆（固件上限 16）。
			// 使用幂等的 ensure（按 (node_id, write_data, read_length) 复用）：
			// 旧实现无条件 Create，multi-drop 通道上每次「删→恢复」净增一条模板，
			// 16 次后节点模板数超过固件 MAX_TEMPLATES=16，整份 ConfigManifest 被拒
			// （config sync 500、全通道停摆）。
			inst.ChannelID = ch.ID
			inst.NodeID = nodeID
			if err := ensureTemplatesFromDriver(tx, driverRegistry, &ch, &inst); err != nil {
				logger.Warnf("[edge-device-restore] ensure ConfigTemplates failed: %v", err)
			}
			return tx.Unscoped().Preload("Channel").Preload("Node").Preload("DeviceConfig").
				First(&out, inst.ID).Error
		})
		if err != nil {
			if errors.Is(err, errNodeNotFound) {
				Error(c, http.StatusNotFound, "node not found")
				return
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			var conflict conflictError
			if errors.As(err, &conflict) {
				Error(c, http.StatusConflict, err.Error())
				return
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// 清 deviceinit 进程内缓存（不在 DB 事务里）：不清理的话，同一 id 的
		// 「已初始化完成」状态会被新节点上的自动初始化直接拒绝（reserve 返回 false）。
		if di := nodeMgr.DeviceInit(); di != nil {
			di.ClearCache(uint(id))
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionCreate, out.NodeID, fmt.Sprint(out.ID))
		Success(c, out)
	})

	// POST /api/v1/edge-devices/:id/merge-history — 把**同一型号**的历史设备
	// （已删除的旧记录）的数据并到本设备。
	//
	// 产品语义（2026-10-25 重做，见 docs/分析/合并历史-产品设计重做-2026-10-25.md）：
	// 合并只是「旧设备行已删除、无法/不想恢复，但历史要保住」的**收尾工具**：
	//   · 目标：必须存活；它的 type 是源设备的准入基准；
	//   · 源：必须是 edge_devices 行存在、**已软删**、且 type 与目标**完全相同**。
	//     不同型号的驱动/字段集合/单位语义不同，跨型号合并会把 A 型号的物理量挂到
	//     B 型号名下（unified_data.sensor_name / sensor_field_obs / 图表/告警全被污染）；
	//   · 源在役 → 409：在役设备的历史被搬走会变成空壳、历史永久分裂。要收尾就先
	//     删除该设备（保留数据）再合并；只是换位置请用「编辑」改节点（路径①）；
	//   · 无设备行的孤儿 id → 400：无法验证型号，「同型号」不变量不可证；
	//   · 与 DevicePurger 认领互斥（清理中 409）；仅「请求删数据」未认领的先撤回意图；
	//   · 幂等：源已无行时第二次返回 rows_migrated=0；台账失败 200 + warnings。
	e.POST("/:id/merge-history", func(c *gin.Context) {
		id, convErr := strconv.ParseUint(c.Param("id"), 10, 32)
		if convErr != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		var req struct {
			SourceIDs []uint `json:"source_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		targetID := uint(id)
		// 合并可能跑几分钟（百万行级）。所有 DB 调用绑定请求上下文：客户端断开
		// 时中止而不是继续无谓地搬数据（分批事务已提交的部分保持已迁移，幂等可重试）。
		mdb := db.WithContext(c.Request.Context())
		// 目标必须存活（软删设备不能作为合并目标 —— 并进去等于埋掉数据）。
		var target models.EdgeDevice
		if err := db.First(&target, targetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "target edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if strings.TrimSpace(target.Type) == "" {
			Error(c, http.StatusBadRequest, fmt.Sprintf("目标设备 #%d 没有设备型号（type 为空），无法校验同型号合并", targetID))
			return
		}
		if len(req.SourceIDs) == 0 {
			Error(c, http.StatusBadRequest, "source_ids is required")
			return
		}
		if len(req.SourceIDs) > 100 {
			Error(c, http.StatusBadRequest, "at most 100 source_ids per request")
			return
		}

		// ── 1. 规范化 + 自我合并拒绝 ──
		sources := make([]uint, 0, len(req.SourceIDs))
		seen := make(map[uint]struct{}, len(req.SourceIDs))
		for _, sid := range req.SourceIDs {
			if sid == 0 {
				continue
			}
			if sid == targetID {
				Error(c, http.StatusBadRequest, fmt.Sprintf("source_ids 不能包含目标设备自身 (#%d)：自我合并没有意义", targetID))
				return
			}
			if _, dup := seen[sid]; dup {
				continue
			}
			seen[sid] = struct{}{}
			sources = append(sources, sid)
		}
		if len(sources) == 0 {
			Error(c, http.StatusBadRequest, "source_ids 至少需要一个非零、且不等于目标设备的 id")
			return
		}

		// ── 2. 源准入：存在 + 已软删 + 同型号（2026-10-25 产品重做）──
		var srcDevices []models.EdgeDevice
		if err := mdb.Unscoped().Select("id", "name", "type", "deleted_at", "data_purge_started_at").
			Where("id IN ?", sources).Find(&srcDevices).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		byID := make(map[uint]models.EdgeDevice, len(srcDevices))
		for _, d := range srcDevices {
			byID[d.ID] = d
		}
		var missingIDs, liveIDs []uint
		typeMismatch := make([]string, 0, len(sources))
		for _, sid := range sources {
			d, ok := byID[sid]
			if !ok {
				missingIDs = append(missingIDs, sid)
				continue
			}
			if !d.DeletedAt.Valid {
				liveIDs = append(liveIDs, sid)
				continue
			}
			if d.Type != target.Type {
				typeMismatch = append(typeMismatch, fmt.Sprintf("#%d（%s）", sid, d.Type))
			}
		}
		if len(missingIDs) > 0 {
			Error(c, http.StatusBadRequest, fmt.Sprintf(
				"源设备不存在：%v（没有设备记录就无法验证型号，不能合并；拼错的 id 也不会被静默忽略）", missingIDs))
			return
		}
		if len(typeMismatch) > 0 {
			Error(c, http.StatusBadRequest, fmt.Sprintf(
				"只能合并同型号设备：目标设备 #%d 的型号是 %s，以下源设备型号不一致：%s。不同类型的设备字段/单位/解析语义不同，合并会把数据挂错语义",
				targetID, target.Type, strings.Join(typeMismatch, "、")))
			return
		}
		if len(liveIDs) > 0 {
			Error(c, http.StatusConflict, fmt.Sprintf(
				"源设备 %v 仍在役（未删除），不能合并：在役设备的历史被搬走会让它变成空壳，之后的新数据仍归它自己。只是想换节点/位置请用「编辑」改绑定（历史天然连续）；确实要收尾，请先删除该设备（保留数据）再合并", liveIDs))
			return
		}

		// ── 2b. 与 DevicePurger 互斥 + 撤回删除意图 ──
		//
		// 合并是「救回历史」的救援路径，必须与 purge 的认领协议对齐：
		//   · 源已被认领（data_purge_started_at != nil）⇒ 409。否则 purge 边删、
		//     merge 边搬，用户只救回一部分历史却拿到 200；
		//   · 源只是「请求删数据」但尚未被认领 ⇒ 在短事务里加行锁并撤回标记，
		//     之后 purge 的认领条件（data_purge_requested=true）不再成立。
		// 行锁顺序与 purge 一致（先 edge_devices 行，再数据表），避免死锁。
		{
			claimed := make([]uint, 0)
			changed := make([]uint, 0)
			revokeErr := mdb.Transaction(func(tx *gorm.DB) error {
				for _, sid := range sources {
					var inst models.EdgeDevice
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Unscoped().First(&inst, sid).Error; err != nil {
						if errors.Is(err, gorm.ErrRecordNotFound) {
							// 并发下刚被 purge 硬删：与「状态变化」同类，返回 409 让用户刷新。
							changed = append(changed, sid)
							continue
						}
						return err
					}
					// 事务内复核准入条件（并发 PUT 改类型/恢复设备等边缘情况）。
					if !inst.DeletedAt.Valid || inst.Type != target.Type {
						changed = append(changed, sid)
						continue
					}
					if inst.DataPurgeStartedAt != nil {
						claimed = append(claimed, sid)
						continue
					}
					if !inst.DataPurgeRequested {
						continue
					}
					if err := tx.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", sid).
						Update("data_purge_requested", false).Error; err != nil {
						return err
					}
				}
				return nil
			})
			if revokeErr != nil {
				Error(c, http.StatusInternalServerError, "failed to check source purge state: "+revokeErr.Error())
				return
			}
			if len(claimed) > 0 {
				Error(c, http.StatusConflict, fmt.Sprintf(
					"源设备 %v 的历史数据正在被后台清理（purge 已认领），无法合并；请等清理结束后再试。若还想保留这些历史，请尽快用「恢复已删除设备」在清理开始前撤回", claimed))
				return
			}
			if len(changed) > 0 {
				Error(c, http.StatusConflict, fmt.Sprintf(
					"源设备 %v 的状态刚刚发生变化（被恢复或型号被修改），请刷新后重试", changed))
				return
			}
		}

		// ── 5. 迁移时序表（分批，独立事务 + 批间 sleep）──
		//
		// ============ 表清单改为**动态**（2026-11-11 device_data 退役）============
		//
		// 这里原本是硬编码的 []string{"unified_data", "device_data"}：
		//
		//	· unified_data 已于 2026-11-08 停写、DROP 后不存在；
		//	· device_data 于 2026-11-11 整表退役（原始数据退役方案 §2.1）。
		//
		// 硬编码的后果不是"多搬一张空表"这么轻：**表不存在时该 SQL 直接报 42P01**，
		// 于是一次正常的合并历史会返回 500，而用户看到的是"合并失败"。
		// 这正是方案 §5.3 冒烟清单第 12 项（合并历史成功、无 42P01）要挡的东西。
		//
		// 改法：走 datalifecycle.ExistingTimeseriesTables(db)，与 purge / retention
		// / data-summary 共用同一个清单源（telemetry_frame_lifecycle.go 的
		// TimeseriesTables），它已经自动过滤掉不存在的表。新增/退役时序表时
		// 只改那一处，这里零改动 —— 这也正是"清单集中"的目的。
		//
		// ⚠ 仍然只处理 HasID=true 的表（UPDATE device_id 直接改归属）。
		// **帧表不行** —— 主键是 (device_id, ts, seq)，直接把源行改成目标 device_id
		// 会在目标同 (ts,seq) 已有行时撞主键、整批失败。它走下面的
		// datalifecycle.MigrateTelemetryFramesToDevice（复制 + ON CONFLICT DO NOTHING
		// + 精确删源，评审 B-3）。HasID 字段就是为了让这两条路径能自动分流：
		// 这里的 if 不是防御性冗余，而是"帧表永远不该进这个 UPDATE 循环"的表达。
		batchSize := 10000
		if db.Dialector != nil && db.Dialector.Name() != "postgres" {
			batchSize = 1000
		}
		var migrated int64
		for _, table := range datalifecycle.ExistingTimeseriesTables(mdb) {
			if !table.HasID {
				// 复合主键表（telemetry_frame）另有迁移函数，见下方。
				continue
			}
			for {
				var affected int64
				err := mdb.Transaction(func(tx *gorm.DB) error {
					res := tx.Exec(
						fmt.Sprintf("UPDATE %s SET device_id = ? WHERE id IN (SELECT id FROM %s WHERE device_id IN ? LIMIT ?)", table.Name, table.Name),
						targetID, sources, batchSize)
					affected = res.RowsAffected
					return res.Error
				})
				if err != nil {
					logger.Warnf("[merge-history] update %s failed target=%d sources=%v: %v", table.Name, targetID, sources, err)
					// 已提交的批次保持已迁移（分批事务各自提交，重试幂等）。
					Error(c, http.StatusInternalServerError, "failed to migrate "+table.Name+"（已迁移的行保持迁移，重试是幂等的）")
					return
				}
				migrated += affected
				if affected < int64(batchSize) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		// 帧表：复制到目标设备 + 幂等插入 + 删源。撞主键的帧（目标同 ts/seq 已有行）
		// 被丢弃并计数 —— 那是**同一物理设备同一时刻的重复采样**，保留目标那份即可。
		framesMoved, framesSkipped, ferr := datalifecycle.MigrateTelemetryFramesToDevice(
			c.Request.Context(), mdb, targetID, sources, batchSize)
		if ferr != nil {
			logger.Warnf("[merge-history] migrate telemetry_frame failed target=%d sources=%v: %v", targetID, sources, ferr)
			Error(c, http.StatusInternalServerError, "failed to migrate telemetry_frame（已迁移的帧保持迁移，重试是幂等的）")
			return
		}
		migrated += framesMoved
		if framesSkipped > 0 {
			logger.Infof("[merge-history] telemetry_frame: %d 帧与目标设备同 (ts,seq) 冲突已丢弃 target=%d", framesSkipped, targetID)
		}

		// ── 6. 台账/投影表（sensor_field_obs / data_sources）──
		// 两类数据各自幂等：台账失败只降级为 warning，不把已完成的时序迁移说成失败。
		warnings := make([]string, 0)
		if ledgerWarnings, err := migrateHistoryLedger(mdb, targetID, sources); err != nil {
			logger.Warnf("[merge-history] ledger migrate failed target=%d sources=%v: %v", targetID, sources, err)
			warnings = append(warnings, "台账表迁移失败（可重试补齐全）: "+err.Error())
		} else {
			warnings = append(warnings, ledgerWarnings...)
		}

		// ── 7. 目标设备 last_data_at 回写 ──
		// 合并进来的历史可能比目标现有数据更新；不回写会让列表「最后采集」永远停在
		// 旧值（而历史图表已经能看到更新的数据，两处自相矛盾）。
		if latest, err := latestDeviceTimestamp(mdb, targetID); err != nil {
			warnings = append(warnings, "读取目标设备最后采集时间失败: "+err.Error())
		} else if latest != nil {
			if err := mdb.Model(&models.EdgeDevice{}).Where("id = ?", targetID).
				Update("last_data_at", *latest).Error; err != nil {
				warnings = append(warnings, "目标设备最后采集时间回写失败: "+err.Error())
			}
		}

		// ── 8. 响应（源全部是「已删除 + 同型号」，state 只有 deleted 一种）──
		outSources := make([]gin.H, 0, len(sources))
		for _, sid := range sources {
			outSources = append(outSources, gin.H{
				"id":          sid,
				"state":       "deleted",
				"device_type": byID[sid].Type,
			})
		}
		resp := gin.H{
			"rows_migrated": migrated,
			"sources":       outSources,
		}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		Success(c, resp)
	})
}

// validateCommandCapacityForDevice 对**已存在的设备**做命令容量门禁（2026-10-25 F-03）。
//
// create 路径在绑定 dto.CommandIntervals 之前就校验了；但「换节点」（PUT 改
// node/channel）与 restore 会把同一份 command_intervals 带到**另一个节点**上，
// 而节点申报的每设备命令上限可能更小。不校验的后果不是这台设备不工作，而是该
// 节点**整份** ConfigManifest 被拒发（nodemgr/sender.go 的容量校验失败即整单拒绝），
// 所有设备静默跑旧配置 —— 正是 2026-10-15 加容量门禁要消灭的事故类型。
//
// 历史坏值（无法反序列化）不阻断：与 PUT 对存量行的兼容策略一致，避免把「老数据」
// 变成「永远不能换节点」。
func validateCommandCapacityForDevice(tx *gorm.DB, driverRegistry *drivers.Registry, dev *models.EdgeDevice, channelID uint) error {
	if dev == nil {
		return nil
	}
	raw := bytes.TrimSpace(dev.CommandIntervals)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("{}")) {
		return nil
	}
	var intervals map[string]int
	if err := json.Unmarshal(raw, &intervals); err != nil {
		return nil
	}
	if len(intervals) == 0 {
		return nil
	}
	candidate := *dev
	candidate.ChannelID = channelID
	return ValidateManifestCommandCapacity(driverRegistry, candidate.Type, intervals, candidate.IntervalMs,
		NodeCommandLimitForDevice(tx, &candidate))
}

// channelSupportsDriverType 校验「设备类型声明的总线类型」是否覆盖这条通道。
//
// 为什么需要（2026-10-25 用户反馈「恢复设备功能逻辑还有大问题」）：恢复已删除
// 设备 / 换节点时用户可以从任意节点挑任意通道，而设备类型是固定的。把 UART 设备
// 绑到 I2C/GPIO 通道上，写路径全部「成功」，但节点永远不会 polling 它 —— 典型的
// 静默失败。只对**注册了驱动**的类型生效：历史行 / 未注册驱动的类型不在这里判死
// （否则这类设备一旦被删就永远无法恢复）。
func channelSupportsDriverType(driverRegistry *drivers.Registry, devType string, ch *models.Channel) error {
	if ch == nil {
		return fmt.Errorf("channel is required")
	}
	drv, err := driverRegistry.Get(devType)
	if err != nil {
		return nil
	}
	types := drv.HardwareTypes()
	if len(types) == 0 {
		return nil
	}
	hw := strings.TrimSpace(ch.HardwareType)
	bus := strings.TrimSpace(ch.BusType)
	for _, t := range types {
		t = strings.TrimSpace(t)
		if strings.EqualFold(t, hw) || strings.EqualFold(t, bus) {
			return nil
		}
	}
	return fmt.Errorf("设备类型 %q 只支持 %s 总线，不能绑定到 %s 通道", devType, strings.Join(types, "/"), hw)
}

// latestDeviceTimestamp 返回该设备在**全部**时序表里的最新时间（都无数据时 nil）。
//
// 时间列**按表不同名**：unified_data/device_data 是 timestamp，L2 帧表是 ts。
// 因此按 datalifecycle.TimeseriesTables() 的 spec 分支取列，而不是把表名塞进
// 同一个 "ORDER BY timestamp" 循环（帧表那条会报列不存在）。评审 B-3。
//
// 为什么必须包含帧表：merge-history 用本函数回写目标设备的 last_data_at。
// 漏掉帧表 ⇒ 只从源设备搬来帧数据时，列表里的「最后采集」会停在旧值，
// 而图表已经能看到新数据（自相矛盾）。
func latestDeviceTimestamp(db *gorm.DB, deviceID uint) (*time.Time, error) {
	var latest *time.Time
	for _, table := range datalifecycle.ExistingTimeseriesTables(db) {
		var ts []time.Time
		if err := db.Table(table.Name).Where("device_id = ?", deviceID).
			Order(table.TSColumn+" DESC").Limit(1).Pluck(table.TSColumn, &ts).Error; err != nil {
			return nil, fmt.Errorf("query latest %s from %s: %w", table.TSColumn, table.Name, err)
		}
		if len(ts) > 0 && (latest == nil || ts[0].After(*latest)) {
			t := ts[0]
			latest = &t
		}
	}
	return latest, nil
}

// migrateHistoryLedger 把台账/投影表（sensor_field_obs / data_sources）改挂到 target。
//
// 两张表都有复合唯一键，不能简单 UPDATE device_id（会撞键）。**必须按源逐个处理**：
// 若把多个源放进同一条 UPDATE，两个源有同名 sensor_name（「同型号的多台旧设备」
// 这一最常见场景）时会同时被改成目标 id ⇒ 撞复合主键 (device_id, sensor_name)，
// 整个台账事务回滚。旧实现正是这样，实测表现为「时序数据已迁移、台账永久不迁移，
// 重试必然再撞」（runtime / 后端审核 F-04）。
//
// 每个源内部仍然两步：
//  1. 与目标**不冲突**的行 → 改挂到目标（这部分字段不能丢）；
//  2. 与目标**冲突**的行 → 合并统计量进目标行后删除。
//
// data_sources 的 manual 行是人工配置（投影器永不覆盖，契约 §12.2）：与目标冲突时
// **保留在源设备上**并返回 warning，绝不静默删除；auto 行冲突则删源行（目标行即事实）。
//
// 返回 warnings（用户可行动的、不应让整个合并失败的情况）与 error（事务失败）。
// 失败时调用方降级为 warning：时序数据已迁移且幂等，重试可补齐台账。
func migrateHistoryLedger(db *gorm.DB, targetID uint, sources []uint) ([]string, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	hasSFO := db.Migrator().HasTable("sensor_field_obs")
	hasDS := db.Migrator().HasTable("data_sources")
	if !hasSFO && !hasDS {
		return nil, nil
	}
	warnings := make([]string, 0)
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, sid := range sources {
			if hasSFO {
				if err := migrateSensorFieldObsForSource(tx, targetID, sid); err != nil {
					return err
				}
			}
			if hasDS {
				w, err := migrateDataSourcesForSource(tx, targetID, sid)
				if err != nil {
					return err
				}
				warnings = append(warnings, w...)
			}
		}
		return nil
	})
	return warnings, err
}

// migrateSensorFieldObsForSource 把一个源设备的 sensor_field_obs 并入目标设备。
func migrateSensorFieldObsForSource(tx *gorm.DB, targetID, sourceID uint) error {
	// ① 目标没有的 sensor_name：直接改挂（单源处理 ⇒ 不会自撞复合主键）。
	if err := tx.Exec(`
		UPDATE sensor_field_obs
		   SET device_id = ?
		 WHERE device_id = ?
		   AND sensor_name NOT IN (
		       SELECT sensor_name FROM sensor_field_obs WHERE device_id = ?)`,
		targetID, sourceID, targetID).Error; err != nil {
		return fmt.Errorf("move sensor_field_obs from %d: %w", sourceID, err)
	}
	// ② 剩下的与目标同名：合并统计量（first 取更早、last 取更晚、samples 累加）。
	var left []models.SensorFieldObs
	if err := tx.Where("device_id = ?", sourceID).Find(&left).Error; err != nil {
		return fmt.Errorf("load leftover sensor_field_obs of %d: %w", sourceID, err)
	}
	for i := range left {
		src := left[i]
		var dst models.SensorFieldObs
		err := tx.Where("device_id = ? AND sensor_name = ?", targetID, src.SensorName).First(&dst).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 理论上不该发生（①已搬完不冲突的），保守处理：直接改挂。
			if err := tx.Model(&models.SensorFieldObs{}).
				Where("device_id = ? AND sensor_name = ?", src.DeviceID, src.SensorName).
				Update("device_id", targetID).Error; err != nil {
				return fmt.Errorf("rebind sensor_field_obs: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("load target sensor_field_obs: %w", err)
		}
		if src.FirstSeen.Before(dst.FirstSeen) {
			dst.FirstSeen = src.FirstSeen
		}
		if src.LastSeen.After(dst.LastSeen) {
			dst.LastSeen = src.LastSeen
		}
		dst.Samples += src.Samples
		// 单位以目标为准（目标仍在线，更新鲜）；目标为空时取源的。
		if dst.Unit == "" {
			dst.Unit = src.Unit
		}
		if err := tx.Save(&dst).Error; err != nil {
			return fmt.Errorf("merge target sensor_field_obs: %w", err)
		}
		if err := tx.Where("device_id = ? AND sensor_name = ?", src.DeviceID, src.SensorName).
			Delete(&models.SensorFieldObs{}).Error; err != nil {
			return fmt.Errorf("delete merged sensor_field_obs: %w", err)
		}
	}
	return nil
}

// migrateDataSourcesForSource 把一个源设备的 data_sources 并入目标设备。
func migrateDataSourcesForSource(tx *gorm.DB, targetID, sourceID uint) ([]string, error) {
	warnings := make([]string, 0)
	// ① 目标没有的 category：整行改挂（保留 managed_by 与配置）。
	if err := tx.Exec(`
		UPDATE data_sources
		   SET edge_device_id = ?
		 WHERE edge_device_id = ?
		   AND category NOT IN (
		       SELECT category FROM data_sources WHERE edge_device_id = ?)`,
		targetID, sourceID, targetID).Error; err != nil {
		return nil, fmt.Errorf("move data_sources from %d: %w", sourceID, err)
	}
	// ② 同 category 冲突：auto 行删掉（目标行是投影事实）；manual 行保留在源设备上。
	var left []models.DataSource
	if err := tx.Where("edge_device_id = ?", sourceID).Find(&left).Error; err != nil {
		return nil, fmt.Errorf("load leftover data_sources of %d: %w", sourceID, err)
	}
	for i := range left {
		row := left[i]
		if row.ManagedBy == models.DataSourceManagedManual {
			warnings = append(warnings, fmt.Sprintf(
				"源设备 #%d 的人工数据源配置 %q 与目标设备同 category，已保留在源设备上（未删除）",
				sourceID, row.Category))
			continue
		}
		if err := tx.Exec("DELETE FROM data_sources WHERE id = ?", row.ID).Error; err != nil {
			return nil, fmt.Errorf("delete leftover data_sources row %d: %w", row.ID, err)
		}
	}
	return warnings, nil
}

// normalizeBatchIDs 批量接口的 id 列表归一：去掉 0、去重、保持原顺序。
func normalizeBatchIDs(ids []uint) []uint {
	out := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// edgeDeviceDisableBlockReason 返回「禁止停用」的原因（空串 = 允许）。
//
// 与单台 PUT 停用的守卫 guardEdgeDeviceDisable 同一判定（只有**被规则引用的来源组
// 成员**才禁止停用；单纯在组里没有被规则引用不拦），区别只是返回错误串而不是
// 直接写 HTTP 响应 —— 批量接口需要逐条收集失败原因、其余设备继续执行。
func edgeDeviceDisableBlockReason(db *gorm.DB, edgeDeviceID uint) (string, error) {
	info, err := findGroupReferencesByEdgeDevices(db, []uint{edgeDeviceID})
	if err != nil {
		return "", err
	}
	if len(info.RuleNames) == 0 {
		return "", nil
	}
	return fmt.Sprintf("设备 #%d 是来源组成员且该组被规则引用，禁止停用：引用组=[%s]；引用规则=[%s]。请先解除规则绑定。",
		edgeDeviceID, strings.Join(info.GroupNames, "、"), strings.Join(info.RuleNames, "、")), nil
}

// ============ /latest-values 缓存 miss 的取数入口（2026-11-08 硬切换后）============

// latestValuesMissRowsFromFramesOrDB 是 /latest-values 缓存 miss 的唯一取数入口。
//
// 为什么要抽这一层而不是在 handler 里写 if：
//
//	/latest-data（详情页数据面板）走 latestSensorViews；/latest-values（详情页顶部
//	实时值）走这里。两者是**同一个语义在不同端点上的两次实现**，而
//	device_data_sensors.go 的注释明确要求它们口径一致。把「用哪个数据源」收敛到
//	一个函数里，这两个端点就不可能漂移。
//
// ============ 2026-11-08 退役：unified_data 回落已删除 ============
//
// 曾经的行为：帧表不可用（字典 nil / 该设备无帧 / 解码失败）时**静默回落**
// unified_data —— 那时它是「过渡期的正常路径」，不该产生日志噪音。
//
// 而现在 unified_data 已 DROP，回落目标不存在。原实现走到回落分支时必然 42P01，
// 上层 logger.Warnf 后 rows 为 nil，用户看到「—」。
// 直接返回空集，用户**同样**看到「—」，区别只是少一条必现的 42P01 日志。
// 所以删掉回落是行为等价 + 消除噪音，不是功能退化。
//
// ⚠ 这个能力（离线设备仍显示最后已知值）曾经存在，同日随 unified_data DROP 退役。
//
//	如果将来需要它，正确做法是**扩展帧表**（让帧解码能枚举该设备的全部
//	sensor_name），而不是把 unified_data 加回来 —— 加回来意味着「DROP 后重启会
//	重建空表」那个陷阱一起回来。
func latestValuesMissRowsFromFramesOrDB(
	ctx context.Context, db *gorm.DB, deviceID uint,
) ([]models.UnifiedData, error) {
	rows, _ := LatestRowsFromFrames(db, deviceID)
	return rows, nil
}
