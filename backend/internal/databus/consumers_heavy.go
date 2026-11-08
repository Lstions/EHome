package databus

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/deviceinit"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/ingest"
	"ehome/backend/internal/models"
	"ehome/backend/internal/pendingwrite"
	"ehome/backend/internal/telemetry"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// Reassembler is the interface for frame reassembly (implemented by nodemgr.streamReassembler).
// Buffering is scoped per (deviceID, requestID): requestID alone is not
// unique across devices once consumers run concurrently.
type Reassembler interface {
	Append(deviceID string, requestID uint32, data []byte) []byte
	Consume(deviceID string, requestID uint32)
}

// PendingWriteConsumer routes command responses to the pendingWrite manager
// for WriteCmd acknowledgment and device initialization tracking.
type PendingWriteConsumer struct {
	pendingWrite *pendingwrite.Manager
	deviceInit   *deviceinit.Orchestrator
	db           *gorm.DB
}

func NewPendingWriteConsumer(pw *pendingwrite.Manager, di *deviceinit.Orchestrator, db *gorm.DB) *PendingWriteConsumer {
	return &PendingWriteConsumer{pendingWrite: pw, deviceInit: di, db: db}
}

func (c *PendingWriteConsumer) Name() string { return "pending_write" }
func (c *PendingWriteConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.RequestID != 0
}
func (c *PendingWriteConsumer) Handle(evt DataEvent) {
	if c.pendingWrite != nil {
		c.pendingWrite.HandleDataReportResult(uint32(evt.RequestID), evt.RawData, evt.ErrorCode)
	}
	// Device-init correlation is EdgeDevice-scoped.  A BMP280 response must
	// never satisfy another BMP280's initialization on the same node.
	if c.deviceInit != nil && evt.EdgeDeviceID > 0 && evt.RequestID != 0 {
		c.deviceInit.HandleDataReportAck(evt.DeviceID, uint(evt.EdgeDeviceID), uint32(evt.RequestID), evt.ErrorCode, evt.RawData)
	}
}

// DBPersistConsumer writes raw data to device_data table for audit/history.
// It persists command responses and scheduler samples; uncorrelated terminal
// RX data remains memory/WS-only.
//
// ==================== S3: 为什么 device_data 会有"两行" ====================
// 同一个 DataEvent 会落到 device_data **两行**, 由两个消费者各写一行:
//
//	· 本消费者 (db_persist)  → DataJSON={"raw":<hex>,"channel":...}   device_id=0
//	· SensorParserConsumer   → DataJSON={"raw_hex":...,"sensors":[...]} device_id=<真实 id>
//
// **这不是重复, 不要"顺手去重"删掉任何一个** —— 实测 (2026-10-07,
// s3_duplicate_device_data_probe_test.go) 证明两者**覆盖面不同**:
//
//	输入                                  | db_persist | sensor_parser
//	可解析的调度采样 (ErrorCode=0, 有 raw)   |    写      |     写
//	ErrorCode != 0 (关键样本)              |    写      |    不写  ← ShouldParse() 为假
//	RawData 为空                           |    写      |    不写  ← ShouldParse() 为假
//	驱动/校准在 Handle 内拒收                |    写      |    不写  ← ShouldHandle 为真, 运行期才失败
//
// 即: 后三类样本**只有本消费者会留下原始字节**。删掉本行 ⇒ 告警类样本的取证数据永久
// 丢失 (本类型职责原文就是 "for audit/history"); 删掉解析行 ⇒ /edge-devices/:id/data
// 与 latest-data 拿不到 data_json.sensors, 前端 DataPanel 直接空 (读契约见
// internal/api/s3_read_shape_probe_test.go)。
//
// 注意最后一行: "驱动/校准在 Handle 内拒收"这一类**无法**通过收窄 ShouldHandle 覆盖,
// 因为是否写入取决于 Handle 内部的运行期结果。所以两个写入点必须各自保留。
//
// 已知遗留 (本轮**故意未动**, 见 Lead task-16 裁决): GET /nodes/:id/data 按 node_id
// 过滤, 两种形状都会命中 ⇒ 该端点返回的 items 里混着 raw 与 parsed 两形状。
// 它**没有任何生产调用者** (全仓 grep + docs/分析/功能模块与业务功能实测盘点-2026-09-23.md:163
// 「无包装、无调用者」), 为一个无人调用的端点改接口只承担风险而无收益, 故留作待裁决项。
// 结构性收敛 (原始字节与解析结果分表) 是独立议题, 见审计 S3 的 3.0 方向。
// ==========================================================================
type DBPersistConsumer struct {
	db *gorm.DB
}

func NewDBPersistConsumer(db *gorm.DB) *DBPersistConsumer {
	return &DBPersistConsumer{db: db}
}

func (c *DBPersistConsumer) Name() string { return "db_persist" }
func (c *DBPersistConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldPersist()
}
func (c *DBPersistConsumer) Handle(evt DataEvent) {
	if c.db == nil {
		return
	}
	dataJSON, err := json.Marshal(map[string]interface{}{
		"raw":            fmt.Sprintf("%x", evt.RawData),
		"channel":        evt.ChannelID,
		"sequence":       evt.Sequence,
		"error_code":     evt.ErrorCode,
		"request_id":     evt.RequestID,
		"edge_device_id": evt.EdgeDeviceID,
		"command_index":  evt.CommandIndex,
	})
	if err != nil {
		logger.Warn("databus: failed to marshal raw device data", "consumer", c.Name(), "node_id", evt.DeviceID, "error", err)
		return
	}
	if err := c.db.Session(&gorm.Session{}).Create(&models.DeviceData{
		NodeID:    evt.DeviceID,
		DataJSON:  string(dataJSON),
		Timestamp: evt.ReceivedAt,
	}).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "device_data").Inc()
		logger.Warn("databus: failed to persist raw device data", "consumer", c.Name(), "node_id", evt.DeviceID, "error", err)
	}
}

// SensorParserConsumer parses sensor data, stores to unified_data,
// updates edge_device status and broadcasts
// data_update WebSocket events. This is the heaviest consumer.
// Only handles command responses with valid data (not passive, not error).
type SensorParserConsumer struct {
	db             *gorm.DB
	wsHub          *websocket.Hub
	reassembler    Reassembler
	deviceActivity func(uint)
	driverRegistry *drivers.Registry
	// latestSink 数据层时序化 (v3.4 §3.2.4): 最新值缓存更新回调 (api.SetLatestValue)。
	latestSink func(models.UnifiedData)
	// alertSink 阈值告警引擎 (方案 v0.4 §5 任务C): 解析后回调注入 alert.Evaluator。
	// 复用 latestSink 回调先例, 只对解析成功的物理量求值, nil 时跳过。
	alertSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
	// automationSink 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 解析后回调注入
	// automation.Evaluator, 与 alertSink 并列不合并。nil 时跳过。
	automationSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
	// sourceHealthSink 数据源健康 (设计/数据源主备与故障转移.md §4): 解析成功回调。
	// 参数一: 边缘设备 ID; 参数二: 本次解析出的敏感量名; 参数三: 时间。
	sourceHealthSink func(edgeDeviceID uint, sensorNames []string, at time.Time)
	// telemetryRollupSink 是 L3 分钟级宽行预聚合的**旁路**采样回调
	// (设计/遥测存储优化方案-2026-11-06.md §5.2, 接线 patch: docs/分析/遥测rollup-L3-接线patch-2026-11-06.md)。
	//
	// 与上面四个 sink 的差别: 它只在内存里累加, **不碰数据库** (分钟切换才落库,
	// 由 collector 自己的 flush 驱动)。因此可以在「不写库」的分支上调用。
	//
	// 与 L1 收窄的关系 (接错位置会静默错数): L1 跳过 INSERT 的帧**仍然必须**调它 ——
	// 把它理解成「喂给统计器」而不是「落库」。若接在「写库成功」分支里, 被跳过的帧
	// 就不进分钟均值 ⇒ rollup 与原始表口径漂移, 而这个漂移没有任何告警会暴露
	// (图表看起来正常, 只是数字偏了)。
	telemetryRollupSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
	// ingestResolver 是 L1 接入层的解析入口 (数据流架构 v1 §3)。
	//
	// 为什么是接口而不是直接 import internal/ingest 的两个 adapter：
	// 让本文件不知道「有哪些解析后端」—— 新增后端时本文件零改动（改造目标）。
	// nil 时退化为 fail-closed（no_adapter 审计），绝不静默丢弃。
	ingestResolver *ingest.Resolver
	// writeGate 是 L1「值不变不入库 + 心跳」的决策器与进程内状态表
	// (设计/遥测存储优化方案-2026-11-06.md §3, 阶段1)。
	//
	// nil 安全: 未装配时 filter 退化为「全量写」，与改造前行为逐字节相同
	// (手工构造 consumer 的测试不会被静默改变语义)。
	writeGate *telemetryWriteGate
	// telemetrySink 是 L2 帧级增量编码的双写旁路
	// (设计/遥测存储-L2双写接线patch-2026-11-06.md; 方案 §7 阶段2 第2步)。
	//
	// nil 安全: 未接线时整条旁路不存在, 行为与改造前逐字节相同。
	// 接线方构造方式: telemetry.SinkOrNil(dualWriter) —— 开关关闭时得到 nil。
	//
	// 类型用 telemetry.Sink (= func(uint, time.Time, int64, []Reading)) 而不是
	// []parser.Field: 该包不能 import pkg/parser (依赖方向), 转换在本文件做一次。
	telemetrySink telemetry.Sink
	// unifiedWriteEnabled 是 **unified_data 历史写入**的停写开关（EHOME_TELEMETRY_UNIFIED_WRITE）。
	//
	// 停的**只**是 unified_data 的 INSERT（以及随之的 L1 commit）。L2 帧表、L3 分钟桶、
	// 最新值缓存、告警/自动化回调**全部照常** —— 它们的调用点在门控之前。
	//
	// 为什么门控位置必须在 latestSink 之后、writeGate.filter 之前：
	//
	//	A. 只跳 db.Create => writeGate.commit 永不调用 => g.last 不推进
	//	   => 每帧都被判 First => 指标失真（filter 的统计也失去意义）
	//	B. 跳 writeGate.filter + db.Create => 缓存/帧表/rollup 全活 => **正确**
	//	C. 跳过整段（含 telemetrySink/rollupSink/latestSink）=> L2/L3 一起死
	//	   => **正是优化的意义被干掉**，而症状「图表没数据」看起来像「停写生效了」
	//
	// 构造时读取（不运行时可写）：避免运行时热切换引入 data race
	// （2026-11-07 DualWriter 那个教训：字段被并发读写）。
	unifiedWriteEnabled bool
}

func NewSensorParserConsumer(db *gorm.DB, wsHub *websocket.Hub, reassembler Reassembler, deviceActivity ...func(uint)) *SensorParserConsumer {
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(driverRegistry)
	return NewSensorParserConsumerWithRegistry(db, wsHub, reassembler, driverRegistry, deviceActivity...)
}

func NewSensorParserConsumerWithRegistry(db *gorm.DB, wsHub *websocket.Hub, reassembler Reassembler, driverRegistry *drivers.Registry, deviceActivity ...func(uint)) *SensorParserConsumer {
	consumer := &SensorParserConsumer{
		db:             db,
		wsHub:          wsHub,
		reassembler:    reassembler,
		driverRegistry: driverRegistry,
		// 默认装配接入层解析器。
		//
		// # 为什么是默认而不是可选（这是踩过坑后的设计决定）
		//
		// 最初把 resolver 做成纯可选（nil 时按 no_adapter 处理）。实测后果：
		// 既有测试直接构造 consumer 而不注入 ⇒ 所有解析失败 ⇒
		// TestS3_Probe_OneEventWritesTwoRows 从绿变红（基线验证过是本次引入）。
		//
		// 教训：接入层是**数据流的必经路径**，不是可插拔的旁路。任何「忘接线」
		// 都会让解析静默全部失败 —— 而这种失败在测试里表现为"行数少了一条"，
		// 在生产里表现为"数据不动了"，两者都极难归因。
		// 因此默认装配内置两个 adapter，main.go 的 SetIngestResolver 只用于
		// 定制顺序或注入额外 adapter（而非"是否启用"）。
		ingestResolver: defaultIngestResolver(driverRegistry),
		// L1 写入收窄 (方案 §7 阶段1): 策略从 config 解析一次 (本仓无热加载)。
		// 默认 write_on_change=false ⇒ **影子模式**: 照常全量写, 只统计
		// 「本可跳过多少行」, 升级镜像后写入行为与改造前完全一致。
		writeGate:           newTelemetryWriteGate(resolveTelemetryWritePolicy()),
		unifiedWriteEnabled: resolveUnifiedWriteEnabled(),
	}
	if len(deviceActivity) > 0 {
		consumer.deviceActivity = deviceActivity[0]
	}
	return consumer
}

// defaultIngestResolver 装配内置 adapter。
//
// 注册顺序即优先级，且**有语义**：ConfigParser 必须在 Driver 之前
// （配置解析优先于驱动解析；标定设备则由 AdapterConfigParser.Supports 自行排除）。
func defaultIngestResolver(driverRegistry *drivers.Registry) *ingest.Resolver {
	return ingest.NewResolver(
		ingest.NewAdapterConfigParser(),
		ingest.NewAdapterDriver(driverRegistry),
	)
}

// SetLatestSink 注入最新值缓存更新回调 (数据层时序化 v3.4 §3.2.4)。
func (c *SensorParserConsumer) SetLatestSink(sink func(models.UnifiedData)) {
	c.latestSink = sink
}

// SetAlertSink 注入阈值告警求值回调 (方案 v0.4 §5.1.2, main.go 接线)。
func (c *SensorParserConsumer) SetAlertSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.alertSink = sink
}

// SetAutomationSink 注入自动化策略求值回调 (设计/自动化策略引擎方案.md v0.1,
// main.go 接线), 与 alertSink 并列不合并。
func (c *SensorParserConsumer) SetAutomationSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.automationSink = sink
}

// SetSourceHealthSink 注入数据源健康成功回调 (设计/数据源主备与故障转移.md §4,
// main.go 经 nodemgr 二阶段接线), 与 alertSink/automationSink 同点并列。
func (c *SensorParserConsumer) SetSourceHealthSink(sink func(edgeDeviceID uint, sensorNames []string, at time.Time)) {
	c.sourceHealthSink = sink
}

// SetTelemetryRollupSink 注入 L3 分钟级预聚合采样回调
// (方案 §5.2; 接线 patch: docs/分析/遥测rollup-L3-接线patch-2026-11-06.md)。
//
// 与 alertSink/automationSink 同形 (同一次解析后的字段 + 同一时刻) —— 复用既有范式,
// 不新创机制。nil 时跳过 (未接线 ⇒ 行为与今天完全一致)。
//
// 命名注意: **不要**取名 SetRollupSink / rollupSink —— 那两个名字是
// datalifecycle/rollup_retirement_gate_test.go 的 rollupGateRetiredSymbols 里的
// 已退役符号 (旧 unified_data_rollup_1m 的写入件), 用它会把源码门禁判红。
func (c *SensorParserConsumer) SetTelemetryRollupSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.telemetryRollupSink = sink
}

// SetTelemetrySink 注入 L2 帧级编码的双写旁路
// (方案 §7 阶段2 第2步; 接线 patch: docs/设计/遥测存储-L2双写接线patch-2026-11-06.md)。
//
// 为什么是 setter 而不是构造函数参数: 与既有 4 个 Set*Sink 同一范式。
// 为什么零成本: 这里不做任何构造/查库, 只存一个函数值 —— 接线方（main.go）
// 无条件构造双写器并注入, 数据面不为它付任何每帧开销。
func (c *SensorParserConsumer) SetTelemetrySink(sink telemetry.Sink) {
	c.telemetrySink = sink
}

// SetIngestResolver 注入 L1 接入层解析器 (数据流架构 v1 §3.2, main.go 接线)。
//
// 为什么用 setter 而不是构造函数参数：既有 4 个 Set*Sink 都是这个模式，
// 且 nodemgr.Manager 需要在 consumer 构建后统一推送 —— 保持一致。
// nil 表示未接线：consumer 会按 no_adapter 落审计，不会 panic、不会静默丢弃。
func (c *SensorParserConsumer) SetIngestResolver(r *ingest.Resolver) {
	c.ingestResolver = r
}

// SetTelemetryHeartbeat 覆盖 L1 写入收窄的心跳间隔（方案 §3.1）。
//
// 为什么既读 config 又留 setter: 生产路径从 config.Load() 取（与
// nodemgr.parserShardCount 同范式，本仓无热加载）；而这个 setter 让测试能固定
// 时间语义（包级 override 会跨用例串味）。
//
// 没有「关闭收窄」这个入参：2026-11-07 起 L1 无条件生效（契约 §1），收窄没有关闭态。
// heartbeat <= 0 时回落到 60s（调用方不该靠 0 表达"无心跳"，那会让跳过无上界）。
func (c *SensorParserConsumer) SetTelemetryHeartbeat(heartbeat time.Duration) {
	c.writeGate = newTelemetryWriteGate(telemetryWritePolicy{heartbeat: heartbeat})
}

// HasSourceHealthSink 报告解析成功健康回调是否已注入 (接线测试只读辅助;
// 项目历史上缺此类断言导致 alert/automation 引擎"从未触发"而单测全绿)。
func (c *SensorParserConsumer) HasSourceHealthSink() bool {
	return c.sourceHealthSink != nil
}

func (c *SensorParserConsumer) Name() string { return "sensor_parser" }
func (c *SensorParserConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldParse()
}
func (c *SensorParserConsumer) Handle(evt DataEvent) {
	if c.db == nil || c.reassembler == nil {
		return
	}

	// Frame reassembly for multi-frame protocols
	merged := c.reassembler.Append(evt.DeviceID, uint32(evt.RequestID), evt.RawData)

	// Find edge device. Preserve both firmware encodings: explicit edge_device_id,
	// real channels.id, and legacy 0-based channel-list index.
	var device models.EdgeDevice
	if evt.EdgeDeviceID > 0 {
		// 摄入边界必须同时按**节点**约束，不能只按 id 查（纵深防御）。
		//
		// 缺口（docs/设计/场景仿真验证框架.md §4.1b 记载为「留待裁决」）：原先只写
		// `Where("id = ?", evt.EdgeDeviceID)`，于是任何能向 `nodes/<任意>/up` 发布的
		// MQTT 客户端，只要猜中一个数字 edge_device_id，就能**以别的节点的名义**写数据 ——
		// 落库、告警、自动化、WS 推送全部按那台受害设备走（实测已复现）。
		// 生产靠 broker 认证限制发布者，但那是唯一一道防线。
		//
		// 加固：把帧来源主题里的节点（evt.DeviceID）一起作为查询条件。
		// 命中失败即 return（与既有各分支同一失败语义：静默丢弃，不打日志刷屏）。
		// 注意 evt.DeviceID 是**节点字符串 ID**（如 "F0F5BDFFFE02"），与下面 else 分支同源。
		if err := c.db.Preload("Node").Where("id = ? AND node_id = ?", evt.EdgeDeviceID, evt.DeviceID).First(&device).Error; err != nil {
			return
		}
	} else {
		if err := c.db.Preload("Node").Where("channel_id = ? AND node_id = ?", evt.ChannelID, evt.DeviceID).First(&device).Error; err != nil {
			var channels []models.Channel
			if err := c.db.Where("node_id = ?", evt.DeviceID).Order("id ASC").Find(&channels).Error; err != nil {
				return
			}
			idx := int(evt.ChannelID)
			if idx < 0 || idx >= len(channels) {
				return
			}
			if err := c.db.Preload("Node").Where("channel_id = ? AND node_id = ?", channels[idx].ID, evt.DeviceID).First(&device).Error; err != nil {
				return
			}
		}
	}

	// ---- 摄入边界 fail-closed 门: 已注销节点不得再产生历史数据/WS 推送 ----
	// 节点注销是 nodes 行的软删除 (docs/设计/节点.md §注销), 注销时该节点的
	// channels/edge_devices 行按设计保留 (历史归属不能丢), 因此"edge_device 命中"
	// 绝不等于"节点仍存活": 旧通道上的上报仍会走到这里。
	// Preload("Node") 带软删范围, 对已注销节点返回零值 (Node.ID == 0, 已实测),
	// 对确实不存在的节点同样为零值 —— 两种情形统一按"节点不可用"拒收, 与
	// commandexec 的 fail-closed 门同风格。门内拦截范围: 解析 / unified_data /
	// device_data / 最新值缓存 / 告警 / 自动化 / 数据源健康 / HA / WS。
	// 注意: 这里不额外查库 (零值即拒收), 健康路径查询次数不变。
	if device.Node.ID == 0 {
		logger.Warn("databus: node retired; refusing sample at ingest boundary",
			"consumer", c.Name(),
			"node_id", device.NodeID,
			"reported_node_id", evt.DeviceID,
			"edge_device_id", device.ID,
			"channel_id", evt.ChannelID,
			"request_id", evt.RequestID,
			"reason", c.nodeIngestDropReason(device.NodeID),
		)
		// 帧已完整重组, 丢弃前释放重组缓冲, 避免已注销节点持续上报堆积内存。
		c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
		return
	}

	// ── 解析（L1 接入层：internal/ingest，数据流架构 v1 §3）──
	//
	// 改造前这里是两段内联 if/else（ConfigParser 优先、Driver 兜底，含标定/命令
	// 两个子分支），新增解析后端必须改本文件。现在所有后端细节收敛到
	// ingest.Adapter 后面，本文件只负责「查设备上下文 → 调 Resolver → 分类失败」。
	//
	// 等价性要守住的三点（M1 验收判据，见 docs/设计/数据流架构-接口设计-2026-10-23.md）：
	//   1. 标定驱动绝不被 ConfigParser 绕过（adapter 注册顺序 + Supports 保证）
	//   2. 标定/命令上下文的查询只在对应分支发生（DecodeContext.DriverContextFn 惰性）
	//   3. Sample.Method 字符串与原 parseMethod 完全一致（历史日志可比）
	outcome := c.decodeFrame(device, merged, time.Now(), evt.CommandTemplateID)
	if !outcome.ok() {
		reason := outcome.FailReason
		if ingestReasonIsConfigDefect(reason) {
			// 配置缺陷（缺适配器/缺标定/缺命令模板）：需要人去补配置 ⇒ WARN。
			logger.Warn("databus: decode failed (config defect); refusing to persist sample",
				"node_id", evt.DeviceID,
				"edge_device_id", device.ID,
				"device_type", device.Type,
				"reason", string(reason),
				"error", outcome.Err)
		} else {
			// 数据问题（帧格式不符/零字段）：重发即可 ⇒ INFO（保持改造前级别）。
			logger.Infof("[%s] Failed to parse data: %v", evt.DeviceID, outcome.Err)
		}
		c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
		return
	}
	sensorData := toParserFields(outcome.Sample.Fields)
	parseMethod := outcome.Sample.Method
	// Parse succeeded — consume reassembly buffer
	c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))

	// 写入口径（2026-10-23 逻辑设备层移除后）：
	//
	// unified_data/device_data 只有 device_id = **边缘设备 ID** 一个归属口径。
	// 历史上有第二个列 logical_device_id（= 经 followMergeChain 解析到的逻辑身份），
	// 它带来两个固定成本：每帧一次 SQL 往返（ResolveMergeTarget），以及
	// 「写入列与查询列可能不一致」的双口径风险。逻辑设备概念层删除后两列一并
	// DROP（.contract/logical-device-removal.md §2），写入路径不再有任何身份解析。
	//
	// 换节点连续性由 edge_devices.id 保证：原地改 node/channel 不换 id ⇒ 历史天然连续；
	// 换成了新设备行则由 merge-history 做数据级迁移（§4.3）。

	// Store parsed data
	//
	// ==================== L1 写入收窄（方案 §7 阶段1）====================
	// 规则（docs/设计/遥测存储优化方案-2026-11-06.md §3.1）: 某 (设备, 字段) 的值与
	// 「上次**已落库**值」相同、且距上次落库不足心跳间隔 ⇒ 跳过 INSERT；
	// 值变化 ⇒ 必写；距上次落库 ≥ 心跳 ⇒ 强制写一帧。
	//
	// 三处**必须**如此的原因（每一处都对应方案 §3.3 的一行副作用）:
	//  1. latestSink 提到 INSERT **之前**且不受写库结果门控 —— 被跳过的帧同样要更新
	//     最新值缓存/告警/自动化/WS。改造前它挂在「Create 成功的 else 分支」上，
	//     跳过就会让缓存冻结在旧值（用户看到"数据不动了"，而 WS 还在推新值）。
	//     这一条与写库成败无关: 缓存表达的是"设备当前状态"，不是"库里有几行"。
	//  2. 写库**失败不推进状态** —— 否则新值被记成"已落库"，后续同值帧全被判成
	//     "未变化"而跳过，该值永久丢失（方案 §3.3「缓存一致性」）。
	//  3. 首帧全量写 —— 状态表是进程内的，重启后为空 ⇒ 每个字段第一次见到都必写
	//     （方案 §3.3「进程重启」）。
	//
	// 默认 write_on_change=false = **影子模式**: 照常全量写，只统计「本可跳过多少行」
	// 并打点（ehome_telemetry_write_gate_rows_total{mode="shadow",decision="skip"}），
	// 核对命中率后再开（方案 §7 阶段1）。
	// =================================================================
	now := time.Now()

	// ==================== 解析成功路径上的两个旁路 sink ====================
	// 位置刻意选在「now 算好之后、写库之前」，**不能**放进下面 Create 的成功分支:
	//
	//   · L2 帧级编码 (§7 阶段2 第2步): L1 开启 write_on_change 后被跳过入库的帧
	//     仍然是一次真实采样，必须进帧表 —— 否则增量链出现空洞，60s 内心跳窗口
	//     内的变化值**永久丢失**（是数据损失，不是压缩损失）。
	//   · L3 分钟桶聚合 (§5.2): 同理，被跳过的帧若不进分钟桶，rollup 的
	//     avg/min/max 会漏掉真实样本，与原始表口径漂移且**没有任何告警**会暴露。
	//
	// 三条共同不变量: ① sink 为 nil 即完全跳过（默认关闭，零成本）；
	// ② 两者内部都是有界队列/内存累加，绝不阻塞本函数；③ 失败只计数+日志，
	// 不影响主写入。因此这里既不判返回值、也不需要 recover。
	// ===================================================================
	if c.telemetrySink != nil && len(sensorData) > 0 {
		readings := make([]telemetry.Reading, len(sensorData))
		for i := range sensorData {
			readings[i] = telemetry.Reading{Name: sensorData[i].Name, Value: sensorData[i].Value}
		}
		// commandTemplateID 不能省: 它是 device15 那 7 种帧类型（3/14/2/5/5/9/4 字段）
		// 的唯一可靠判据，省掉会把不同指令的值写进同一 slot（方案 §4.4 的 ⚠）。
		c.telemetrySink(device.ID, now, int64(evt.CommandTemplateID), readings)
	}
	if c.telemetryRollupSink != nil && len(sensorData) > 0 {
		c.telemetryRollupSink(device.ID, sensorData, now)
	}

	records := make([]models.UnifiedData, 0, len(sensorData))
	for _, sd := range sensorData {
		records = append(records, models.UnifiedData{
			DeviceID:   device.ID,
			SensorName: sd.Name,
			Value:      sd.Value,
			Unit:       sd.Unit,
			Timestamp:  now,
		})
	}
	if len(records) > 0 {
		// (1) 先更新最新值缓存（与写库结果无关；被跳过的帧也必须更新）。
		// 注: 原 rollupSink 分支已于 2026-09-15 随 rollup 表退役删除; 注意该分支
		// 原本还**门控**着 latestSink —— 退役时有意把 latestSink 提为独立条件,
		// 消除"未注入 rollup ⇒ 最新值缓存永不更新"的隐藏耦合 (裁决见
		// docs/分析/rollup-退役裁决-2026-09-15.md §5)。
		if c.latestSink != nil {
			for i := range records {
				c.latestSink(records[i])
			}
		}

		// ── 停写开关（EHOME_TELEMETRY_UNIFIED_WRITE=false，2026-11-08）──
		//
		// 门控位置**必须在 latestSink 之后**：最新值缓存是「运行时状态」，要继续更新，
		// 否则停写后 /latest-data 会退化成「最后一次上报的值」，而那个值会越来越旧。
		//
		// 也**必须**在 telemetrySink / telemetryRollupSink 之后：那两条是 L2 帧表与 L3 分钟桶，
		// 正是「停掉 unified_data」之后要接手的数据源。把它们一起闸掉就是
		// 「优化的意义被干掉」，而症状「图表没数据」看起来却像「停写生效了」。
		//
		// 同时跳过 writeGate.filter/commit：
		//	· filter 只产生「要写哪些行」的决策，既然不写就不必算；
		//	· commit 的语义是「这些行已落库」。不写就不能 commit，
		//	  否则 g.last 会记下一个从未落库的值 ⇒ 后续同值帧全被判「未变化」。
		if !c.unifiedWriteEnabled {
			// 计数器是必须的：否则「按预期停了」与「上游断了」在 unified_data 行数上同形。
			metrics.UnifiedWritesSkippedTotal.Add(float64(len(records)))
		} else {
			// (2) 收窄决策（L1，2026-11-07 起无条件生效）: 返回的就是真正要写库的行。
			persist, stat := c.writeGate.filter(device.ID, records, now)
			c.writeGate.record(stat)

			if len(persist) > 0 {
				if err := c.db.Session(&gorm.Session{}).Create(&persist).Error; err != nil {
					metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "unified_data").Inc()
					logger.Warn("databus: failed to persist parsed sensor data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
				} else {
					// (3) 只有写成功才推进「最后已落库值」（方案 §3.3 缓存一致性）:
					// 写失败不推进 —— 否则新值被记成"已落库"，后续同值帧全被判成
					// "未变化"而跳过，该值永久丢失。
					c.writeGate.commit(device.ID, persist, now)
				}
			} else {
				// "整帧都跳过"是一条**正常**路径（值未变且未到心跳）:
				// 不写库也不 commit —— 状态时间戳必须保持为上次落库时间，否则心跳
				// 永远不会到点（commit 会把它推到 now）。
				logger.Debugf("[%s] telemetry write gate skipped all %d rows (unchanged, heartbeat not due)",
					evt.DeviceID, stat.Skipped)
			}
		}
	}

	// 阈值告警引擎 (方案 v0.4 §5.1.2): 解析成功后对物理量求值 (解析后回调,
	// 非独立 consumer — 避免每事件重复解析 RawData)。独立于持久化结果:
	// 解析成功即求值, 与 ShouldHandle 语义等价。
	if c.alertSink != nil && len(sensorData) > 0 {
		c.alertSink(device.ID, sensorData, now)
	}
	// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 与 alertSink 同点挂接,
	// 同一批解析后物理量 (裁决: 避免独立 consumer 的重复解析开销)。
	if c.automationSink != nil && len(sensorData) > 0 {
		c.automationSink(device.ID, sensorData, now)
	}
	// 数据源健康 (设计/数据源主备与故障转移.md §4): 解析成功回调, 与
	// alertSink/automationSink 同点挂接。空集合也调用——服务层按 category
	// 匹配, 不命中则不更新。sink 失败/panic 只 Warn, 绝不影响入库/推送。
	if c.sourceHealthSink != nil {
		sensorNames := make([]string, 0, len(sensorData))
		for i := range sensorData {
			sensorNames = append(sensorNames, sensorData[i].Name)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Warn("databus: source health sink panicked", "consumer", c.Name(), "edge_device_id", device.ID, "panic", r)
				}
			}()
			c.sourceHealthSink(device.ID, sensorNames, now)
		}()
	}

	// Update edge device status. Keep last_data_at fresh for every successful
	// sample, and notify the offline detector even when no status transition
	// occurs so its active-device cache cannot age out a healthy device.
	//
	// error_code 清零（2026-09-30）：固件只在「命令有错」时才把设备放进
	// StatusReport 的 EdgeDeviceHealth 子帧，恢复后该子帧消失、服务端收不到
	// 「已恢复」的显式信号。而**成功采到数据本身就是最强的恢复证据**，
	// 所以在这里清零 —— 否则一次超时留下的 error_code 会永久粘住。
	//
	// 判定条件从 status = "offline" 放宽为 status <> "active"（2026-10-03 缺陷 5）：
	// 新建设备的初始状态是 "pending"（无数据），首个数据帧必须把它提升为
	// "active"。若仍只匹配 offline，pending 设备即使一直正常采数也会永远停在
	// pending —— 那是把「显示不准」修成「永远不显示在线」，比原缺陷更糟。
	// 用 <> 而非枚举 pending/offline，是为了让任何历史遗留的陌生状态值
	// （"error"/"warning"/手工改库的脏值）都能被真实数据纠正回来。
	result := c.db.Model(&device).Where("status <> ?", models.EdgeDeviceStatusActive).Updates(map[string]interface{}{
		"last_data_at": now,
		"status":       models.EdgeDeviceStatusActive,
		"error_code":   0,
	})
	if result.RowsAffected == 0 {
		c.db.Model(&device).Updates(map[string]interface{}{
			"last_data_at": now,
			"error_code":   0,
		})
	}
	if c.deviceActivity != nil {
		c.deviceActivity(device.ID)
	}
	if result.RowsAffected > 0 && c.wsHub != nil {
		c.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": device.ID,
			"device_id":      device.ID,
			"device_name":    device.Name,
			"node_id":        device.NodeID,
			"channel_id":     device.ChannelID,
			"status":         models.EdgeDeviceStatusActive,
			"reason":         "data_received",
		})
	}

	// Store raw data for this edge device.
	//
	// S3: 本行与 DBPersistConsumer 写的行**不是重复** —— 见该消费者类型上方的完整
	// 对照表。要点: ErrorCode!=0 / 空 raw / 本函数提前 return 的三类样本
	// **只有 DBPersistConsumer 会写**。删任何一行都会丢数据。
	//
	// ==================== 停写 "sensors"（task-19，2026-11-07）====================
	// 改前本行形状:
	//   {"raw_hex":"...","sensors":[{Name,Value,Unit,StringValue}...],"channel_id":1,"timestamp":...}
	// 其中 sensors 与 unified_data / telemetry_frame 存的是**同一份 (name,value,unit)**:
	// 生产实测 device_data 单行 1,251 B 里 **83%（约 1,038 B）** 就是这个数组,
	// 折合 147 MB/天、占全库日增（344 MB）的 43%。同一份物理量存三份
	// （unified_data 1,294 B + 本数组 1,251 B + 帧表 129 B）是当前最大的一块冗余。
	//
	// 现在改为**读时合成**（internal/api/device_data_sensors.go 的
	// deviceDataJSONWithSensors）: /latest-data 返回前按「每 sensor 各取最新」把
	// sensors 补回，前端零改动。合成口径不能用 WHERE timestamp = X ——
	// L1 收窄后同一时间戳只有**变化**字段会入库，等值查会缺值。
	//
	// ⚠ 唯一不能丢的是 StringValue: unified_data 与 telemetry_frame **都没有**这一列,
	// 所以 device_data 是 BMS hardware_version("V19") / serial_number 的**唯一**落库位置。
	// 故保留 "str_values" 映射，且**仅在有非空 StringValue 时出现**（绝大多数帧没有这个键,
	// 典型的 read_basic_info 帧就是零额外字节）。
	// ==========================================================================
	dataJSON, err := marshalParsedDeviceData(merged, evt.ChannelID, now, sensorData)
	if err != nil {
		logger.Warn("databus: failed to marshal parsed device data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
	} else if err := c.db.Session(&gorm.Session{}).Create(&models.DeviceData{
		DeviceID:  device.ID,
		NodeID:    evt.DeviceID,
		DataJSON:  string(dataJSON),
		Timestamp: now,
	}).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "device_data").Inc()
		logger.Warn("databus: failed to persist parsed device data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
	}

	// HomeAssistant publish was REMOVED with MQTT (2026-10-08). It published
	// sensor state over MQTT and has no 3.0 equivalent; the edge-device status,
	// WS broadcast and DB persistence below are unaffected.

	// Broadcast the legacy-compatible parsed channel_data payload. Terminal clients
	// still receive raw uncorrelated RX reports through WSPushConsumer.
	dataMap := make(map[string]interface{}, len(sensorData))
	for _, sd := range sensorData {
		dataMap[sd.Name] = sd.Value
	}
	if c.wsHub != nil {
		channelEvent := map[string]interface{}{
			"device_id":        evt.DeviceID,
			"node_id":          evt.DeviceID,
			"channel_id":       evt.ChannelID,
			"raw_hex":          fmt.Sprintf("%x", evt.RawData),
			"timestamp":        now.Unix(),
			"error_code":       evt.ErrorCode,
			"request_id":       evt.RequestID,
			"edge_device_id":   device.ID,
			"edge_device_name": device.Name,
			"command_index":    evt.CommandIndex,
			"data":             dataMap,
		}
		c.wsHub.BroadcastEvent(events.ChannelData, channelEvent)
	}

	// Broadcast data_update with canonical terminology: node = node_id/node_name,
	// edge device = edge_device_id/edge_device_name. The legacy collector_* names
	// and the duplicate device_id alias are no longer emitted.
	//
	// node_id carries the STRING node serial (device.NodeID). It used to carry
	// device.Node.ID, the numeric primary key, so the same field name meant two
	// different things inside this one function: channel_data above (evt.DeviceID),
	// edge_device_status and the REST API (handler_data.go, handler_node.go) all
	// use the string serial. No consumer needs the primary key - the frontend only
	// resolves node_id against serials (NodeList/NodeDetail/NodeOverview/
	// ChannelPanel compare it to a serial; stores/websocket.ts types it as
	// number-or-string; the data_update subscribers Dashboard.vue and
	// useRealtimeData.ts ignore node_id entirely) - and row identity is already
	// addressable through edge_device_id. Hence the unification, without adding a
	// separate node_db_id field that nothing consumes.
	if c.wsHub != nil && len(sensorData) > 0 {
		c.wsHub.BroadcastEvent(events.DataUpdate, map[string]interface{}{
			"edge_device_id":   device.ID,
			"edge_device_name": device.Name,
			"node_id":          device.NodeID,
			"node_name":        device.Node.Name,
			"channel_id":       evt.ChannelID,
			"data":             dataMap,
			"collected_at":     now.Format(time.RFC3339),
		})
	}

	logger.Debugf("[%s] Parsed %d sensors using %s", evt.DeviceID, len(sensorData), parseMethod)
}

// nodeIngestDropReason 为摄入边界 fail-closed 门给出可排查的丢弃原因。
// 只在丢弃路径调用 (冷路径), 健康摄入路径不增加查询; 正因为默认范围恰好
// 隐藏了要找的那一行, 这里必须 Unscoped。
func (c *SensorParserConsumer) nodeIngestDropReason(nodeID string) string {
	if nodeID == "" {
		return "node_id_empty"
	}
	var node models.Node
	if err := c.db.Unscoped().Select("id", "deleted_at").
		Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return "node_missing"
	}
	if node.DeletedAt.Valid {
		return "node_soft_deleted"
	}
	// 节点行仍存活却走到丢弃路径: Preload 未回填 (理论上不可达)。仍然 fail-closed,
	// 由运维按该原因继续排查。
	return "node_unresolved"
}

// stringValuesOf 抽取 StringValue 非空的字段（BMS 的 hardware_version / serial_number）。
//
// 为什么单独抽这个函数：2026-11-07 起 device_data 不再写 sensors 数组（读时按
// 「每 sensor 取最新」合成），但 StringValue **只存在于 device_data** ——
// unified_data 与 telemetry_frame 都没有这一列。若一并丢弃，BMS 的
// hardware_version("V19") 与 serial_number 会永久消失，且**没有任何其他副本**可恢复。
//
// 成本：只有 read_hardware_version(0x05) / write_sn(0xA2) 这两类帧才非空，
// 且这两条命令默认 interval=0（不同步），所以绝大多数帧连这个 key 都不会出现。
// 返回空 map 时调用方**不写该 key**，避免给每帧加 ~15 字节。
func stringValuesOf(fields []parser.Field) map[string]string {
	var out map[string]string
	for i := range fields {
		if fields[i].StringValue == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, 2)
		}
		out[fields[i].Name] = fields[i].StringValue
	}
	return out
}

// marshalParsedDeviceData 构造 sensor_parser 写进 device_data 的 data_json。
//
// 抽成独立函数是为了**能被单独测**：这段形状是前端契约的一部分（前端读
// data_json.sensors / raw_hex），埋在 Handle 里就只能靠总线上端到端跑一遍
// 才能验证，而端到端跑会同时依赖驱动、校准缓存、重组器三件与形状无关的东西。
//
// 形状（2026-11-07 task-19 起）:
//
//	{"raw_hex":"<hex>","channel_id":N,"timestamp":<ms>}                  绝大多数帧
//	{...同上...,"str_values":{"hardware_version":"V19"}}                  仅字符串读数帧
//
// **没有 sensors 键** —— 它由读路径按「每 sensor 取最新」合成
// （internal/api/device_data_sensors.go），这是本次改造省下 83% 行字节的来源。
func marshalParsedDeviceData(raw []byte, channelID uint64, at time.Time, fields []parser.Field) ([]byte, error) {
	payload := map[string]interface{}{
		"raw_hex":    fmt.Sprintf("%x", raw),
		"channel_id": channelID,
		"timestamp":  at.UnixMilli(),
	}
	if sv := stringValuesOf(fields); len(sv) > 0 {
		payload["str_values"] = sv
	}
	return json.Marshal(payload)
}
