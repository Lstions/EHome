//go:build e2e

// Package e2e 是"边缘设备上报 → 数据流 → 功能反馈"的端到端模拟验证。
//
// 为什么需要这一层：单测覆盖函数、集成测试覆盖接口，但**接线**（parser 消费者是否被
// 注册、sink 是否被注入、观测是否被投影、规则是否真的拿到解析后的字段）只在完整
// 组合根上才暴露。本仓历史上就有"引擎从未触发而单测全绿"的教训。
//
// 与 simulation/ 的区别：simulation 是黑盒（起真实 cmd/server 子进程 + MQTT），
// 而 MQTT 已于 2026-10-08 移除，TestScenarios 已作废。本测试在**进程内**组装
// 与 cmd/server 相同的生产数据面（databus 消费者 + 真实 sink 注入 + 真实 PG），
// 只跳过 TLS 传输层：设备帧用真实协议布局构造，走 HandleFrame 同源的 DataEvent。
package e2e

import (
	"context"
	"math"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/databus"
	"errors"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/downlink"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"
	"ehome/backend/testutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─────────────────────────────────────────────────────────────────────────────
// 真实协议帧构造（jiabaida 0x03 基本信息响应）
// 布局：0xDD | CMD | STATUS | LEN | DATA[LEN] | CHK_H | CHK_L | 0x77
// 校验：response = ~sum(LEN+DATA)+1
// DATA：见 drivers/jiabaida_parse.go parse0x03
// ─────────────────────────────────────────────────────────────────────────────

type bmsSample struct {
	totalVoltageDeciV uint16 // 0.01V
	currentCentiA     int16  // 0.01A
	remainingCentiAh  uint16
	nominalCentiAh    uint16
	cycles            uint16
	protectStatus     uint16
	version           byte
	rsoc              byte    // %
	fetStatus         byte
	cellCount         byte    // 串
	temps             []int16 // 0.1°C 原始值
}

func (s bmsSample) frame() []byte {
	data := make([]byte, 0, 32)
	data = append(data, byte(s.totalVoltageDeciV>>8), byte(s.totalVoltageDeciV))
	data = append(data, byte(uint16(s.currentCentiA)>>8), byte(uint16(s.currentCentiA)))
	data = append(data, byte(s.remainingCentiAh>>8), byte(s.remainingCentiAh))
	data = append(data, byte(s.nominalCentiAh>>8), byte(s.nominalCentiAh))
	data = append(data, byte(s.cycles>>8), byte(s.cycles))
	data = append(data, 0, 0, 0, 0, 0, 0) // 10..15 保留
	data = append(data, byte(s.protectStatus>>8), byte(s.protectStatus))
	data = append(data, s.version, s.rsoc, s.fetStatus, s.cellCount, byte(len(s.temps)))
	for _, tv := range s.temps {
		data = append(data, byte(uint16(tv)>>8), byte(uint16(tv)))
	}

	var sum uint16
	sum += uint16(len(data))
	for _, b := range data {
		sum += uint16(b)
	}
	chk := ^sum + 1

	frame := []byte{0xDD, 0x03, 0x00, byte(len(data))}
	frame = append(frame, data...)
	frame = append(frame, byte(chk>>8), byte(chk))
	frame = append(frame, 0x77)
	return frame
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试替身
// ─────────────────────────────────────────────────────────────────────────────

type passthroughReassembler struct{}

func (passthroughReassembler) Append(_ string, _ uint32, data []byte) []byte { return data }
func (passthroughReassembler) Consume(_ string, _ uint32)                    {}

// signalingConsumer 在真实消费者 Handle 返回后计数，用于等待数据面处理完成。
type signalingConsumer struct {
	inner databus.DataConsumer
	done  atomic.Int64
}

func (c *signalingConsumer) Name() string                     { return c.inner.Name() }
func (c *signalingConsumer) ShouldHandle(e databus.DataEvent) bool { return c.inner.ShouldHandle(e) }
func (c *signalingConsumer) Handle(e databus.DataEvent) {
	c.inner.Handle(e)
	c.done.Add(1)
}

// gormObsSink 把观测增量 UPSERT 到 sensor_field_obs（与 cmd/server 的 projector 同语义）。
type gormObsSink struct{ db *gorm.DB }

func (s gormObsSink) UpsertObservations(obs []sensorfield.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	rows := make([]models.SensorFieldObs, 0, len(obs))
	for _, o := range obs {
		rows = append(rows, models.SensorFieldObs{
			DeviceID: o.DeviceID, SensorName: o.Name, Unit: o.Unit,
			FirstSeen: o.FirstSeen, LastSeen: o.LastSeen, Samples: o.Samples,
		})
	}
	// 方言差异：PostgreSQL 用 GREATEST/LEAST，SQLite 用 MAX/MIN（生产为 PG，本地默认 SQLite）。
	first, last := "MIN(sensor_field_obs.first_seen, excluded.first_seen)", "MAX(sensor_field_obs.last_seen, excluded.last_seen)"
	if s.db.Dialector.Name() == "postgres" {
		first, last = "LEAST(sensor_field_obs.first_seen, excluded.first_seen)", "GREATEST(sensor_field_obs.last_seen, excluded.last_seen)"
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "sensor_name"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"last_seen": gorm.Expr(last), "first_seen": gorm.Expr(first), "samples": gorm.Expr("sensor_field_obs.samples + excluded.samples"), "unit": gorm.Expr("excluded.unit")}),
	}).Create(&rows).Error
}

// ─────────────────────────────────────────────────────────────────────────────
// 组合根：与 cmd/server 相同的数据面接线（只跳过 TLS 传输）
// ─────────────────────────────────────────────────────────────────────────────

type e2eChain struct {
	db         *gorm.DB
	node       models.Node
	channel    models.Channel
	device     models.EdgeDevice
	logical    models.LogicalDevice
	dataSource models.DataSource
	registry   *sensorfield.Registry
	dsSvc      *datasource.Service
	bus        *databus.DataEventBus
	consumer   *signalingConsumer
	eval       *automation.Evaluator
	pub        downlink.Publisher
	cmdSvc     *commandexec.Service
	disp       *commandexec.Dispatcher
}

func newE2EChain(t *testing.T, rules ...*models.AutomationRule) *e2eChain {
	return newE2EChainWithPublisher(t, nil, rules...)
}

// newE2EChainWithPublisher 额外注入下行 Publisher（用于验证命令真正被编成 wire 帧）。
// pub 为 nil 时使用拒绝型 stub（无 session 语义），与生产"节点不在线"一致。
func newE2EChainWithPublisher(t *testing.T, pub downlink.Publisher, rules ...*models.AutomationRule) *e2eChain {
	t.Helper()
	db := testutil.OpenTestDB(t)
	c := &e2eChain{db: db}

	// 节点夹具必须带齐 commandexec 九道门禁读取的字段。
	reported := time.Now().UTC()
	c.node = models.Node{NodeID: "3001-e2e", Model: "ESP32S3", FirmwareVersion: "1.0.0", Status: "online", ConfigVersion: "manifest-e2e", ConfigStatus: "applied", ConfigSyncState: "in_sync", BootID: "boot-e2e", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: E2ECapabilitiesJSON}
	if err := db.Create(&c.node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	c.channel = models.Channel{NodeID: c.node.NodeID, HardwareID: "1", IntervalMs: 5000, Enabled: true}
	if err := db.Create(&c.channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	// 通道必须出现在节点上报的 hardware_info 里（gateReportedActionChannel）。
	hw := fmt.Sprintf("{\"channels\":[{\"id\":%d,\"enabled\":true}]}", c.channel.ID)
	if err := db.Model(&models.Node{}).Where("id = ?", c.node.ID).Update("hardware_info", hw).Error; err != nil {
		t.Fatalf("set hardware_info: %v", err)
	}

	// 系统管理员：自动化 device_action 需要 system actor（resolveSystemActorID）。
	adminNow := time.Now().UTC()
	sk := models.SystemAdminSubjectKey
	if err := db.Create(&models.User{Username: "e2e-admin", PasswordHash: "hash", Enabled: true, SubjectKey: &sk, SessionVersion: 1, LastLoginAt: &adminNow}).Error; err != nil {
		t.Fatalf("create system admin: %v", err)
	}
	c.logical = models.LogicalDevice{IdentityKey: "jiabaida_bms:3001-e2e:1:1", Name: "BMS-e2e", DeviceType: "jiabaida_bms", RetentionDays: 90}
	if err := db.Create(&c.logical).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	c.device = models.EdgeDevice{Name: "BMS-e2e", NodeID: c.node.NodeID, ChannelID: c.channel.ID, Type: "jiabaida_bms", Enabled: true, Status: "active", LogicalDeviceID: &c.logical.ID}
	if err := db.Create(&c.device).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}

	// 数据源域：一个 active 来源，category = 物理名 rsoc（与 unified_data.sensor_name 同域）
	c.dataSource = models.DataSource{
		DeviceID: c.logical.ID, Category: "rsoc", EdgeDeviceID: c.device.ID,
		SourceType: "edge_device", Name: "BMS-RSOC", Priority: 100, IsPrimary: true,
		MaxFailCount: 3, Status: datasource.StatusActive,
	}
	if err := db.Create(&c.dataSource).Error; err != nil {
		t.Fatalf("create data source: %v", err)
	}
	c.dsSvc = datasource.New(db, datasource.Options{})

	// ── 字段解析层 ──
	c.registry = sensorfield.New(sensorfield.Config{Enabled: true})
	drvReg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(drvReg)
	if drv, err := drvReg.Get("jiabaida_bms"); err == nil {
		var decls []sensorfield.Field
		for _, sd := range drv.GetSensorDefinitions() {
			decls = append(decls, sensorfield.Field{Name: sd.Name, Unit: sd.Unit})
		}
		c.registry.RegisterDeclarations("jiabaida_bms", decls)
	}
	c.registry.SetDeviceType(c.device.ID, "jiabaida_bms")
	if err := c.registry.SetAliases([]sensorfield.AliasRule{
		{Semantic: "soc", Candidates: []string{"rsoc", "battery_capacity"}, ExpectedUnit: "%"},
	}); err != nil {
		t.Fatalf("set aliases: %v", err)
	}

	// ── 自动化：先落规则（Evaluator 构造时装载），再接线 ──
	for _, rule := range rules {
		if err := db.Create(rule).Error; err != nil {
			t.Fatalf("create rule: %v", err)
		}
	}
	// 动作目录用真实内置注册表（含 jiabaida set_mos_policy）
	actions := deviceaction.NewBuiltInRegistry(drvReg)
	c.cmdSvc = commandexec.NewService(db, actions)
	c.cmdSvc.SetDispatchEnabled(true)
	planner := automation.NewPlanner(db, c.cmdSvc, func(string, any) {}, 0)
	planner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	planner.SetLatestValuesFn(api.LatestValues)
	planner.SetPlanProvider(c.registry)
	c.eval = automation.NewEvaluator(db, planner)
	c.eval.SetPlanProvider(c.registry)
	c.disp = nil

	// 下行 Publisher：默认拒绝型（无 live session ⇒ 生产语义），测试可注入捕获型。
	if pub == nil {
		pub = rejectingPublisher{}
	}
	c.pub = pub
	transport := commandexec.NewChannelCmdV2Transport(db, c.pub, actions)
	c.disp = commandexec.NewDispatcher(db, transport, commandexec.NewDispatcherOwner("e2e"))

	// ── 数据面：真实消费者 + 真实 sink 注入（对齐 main.go） ──
	inner := databus.NewSensorParserConsumerWithRegistry(db, nil, passthroughReassembler{}, drvReg, func(uint) {})
	inner.SetLatestSink(func(rec models.UnifiedData) {
		api.SetLatestValue(rec)
		c.registry.Observe(rec.DeviceID, rec.SensorName, rec.Unit, rec.Timestamp)
	})
	inner.SetAutomationSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		c.eval.Evaluate(edgeDeviceID, fields, at)
	})
	// 数据源健康回调：与 main.go 的 nodeMgr.SetSourceHealthSink 同语义
	inner.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		c.dsSvc.MarkSuccess(edgeDeviceID, names, at)
	})
	c.consumer = &signalingConsumer{inner: inner}
	c.bus = databus.NewDataEventBus()
	c.bus.Register(c.consumer)
	t.Cleanup(c.bus.Stop)
	return c
}

func (c *e2eChain) publish(t *testing.T, frame []byte, requestID uint64) {
	t.Helper()
	before := c.consumer.done.Load()
	c.bus.Publish(databus.DataEvent{
		DeviceID: c.node.NodeID, ChannelID: uint64(c.channel.ID),
		EdgeDeviceID: uint64(c.device.ID), RequestID: requestID,
		RawData: frame, ReceivedAt: time.Now(),
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.consumer.done.Load() > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("data plane did not finish processing frame (request_id=%d)", requestID)
}

// ─────────────────────────────────────────────────────────────────────────────
// 场景 1：完整链路 —— 帧 → 解析 → 入库 → 最新值 → 观测投影 → 解析层可见 → 规则触发
// ─────────────────────────────────────────────────────────────────────────────

func TestEdgeDeviceDataFlow_FullChain(t *testing.T) {
	rule := &models.AutomationRule{
		Name: "SOC 低电量通知", Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 0, // any-device（验证 R3：deviceID 必须取 Evaluate 入参）
		TriggerSensorName: "@soc", TriggerComparator: "lt", TriggerThreshold: 30,
		ActionType: models.AutomationActionNotification,
	}
	c := newE2EChain(t, rule)

	sample := bmsSample{
		totalVoltageDeciV: 5120, currentCentiA: 1000,
		remainingCentiAh: 1100, nominalCentiAh: 5000, cycles: 42,
		rsoc: 27, cellCount: 4, temps: []int16{2981, 2992}, // 24.95℃ / 26.05℃
	}
	c.publish(t, sample.frame(), 1)

	// ① 入库：10 个固定字段 + 2 个 NTC 温度
	var rows []models.UnifiedData
	if err := c.db.Where("device_id = ?", c.device.ID).Find(&rows).Error; err != nil {
		t.Fatalf("query unified_data: %v", err)
	}
	if len(rows) != 12 {
		t.Fatalf("unified_data rows = %d, want 12 (10 fixed + 2 NTC): %+v", len(rows), rows)
	}
	byName := map[string]float64{}
	for _, r := range rows {
		byName[r.SensorName] = r.Value
	}
	for name, want := range map[string]float64{
		"rsoc": 27, "total_voltage": 51.2, "current": 10, "cell_count": 4,
		"temperature_1": 2981.0/10 - 273.15, "temperature_2": 2992.0/10 - 273.15,
	} {
		got, ok := byName[name]
		if !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("unified_data[%q] = %v (present=%v), want %v", name, got, ok, want)
		}
	}

	// ② 最新值缓存：单槽语义（AU-12 根因）——最后写入的是 temperature_2
	if last, ok := api.LatestValue(c.device.ID); !ok || last.SensorName != "temperature_2" {
		t.Errorf("LatestValue single-slot = %q (ok=%v), want temperature_2 —— 这正是 AU-12 的根因", last.SensorName, ok)
	}
	// 按名取值修复了该缺陷
	if v, ok := api.LatestValueBySensor(c.device.ID, "rsoc"); !ok || v.Value != 27 {
		t.Errorf("LatestValueBySensor(rsoc) = %v (ok=%v), want 27", v.Value, ok)
	}

	// ③ 观测投影：Flush 后 sensor_field_obs 应有 12 行，可被 /sensor-fields 读取
	if err := c.registry.Flush(context.Background(), gormObsSink{db: c.db}); err != nil {
		t.Fatalf("flush observations: %v", err)
	}
	var obs []models.SensorFieldObs
	if err := c.db.Where("device_id = ?", c.device.ID).Find(&obs).Error; err != nil {
		t.Fatalf("query sensor_field_obs: %v", err)
	}
	if len(obs) != 12 {
		t.Fatalf("sensor_field_obs rows = %d, want 12", len(obs))
	}
	if obs[0].FirstSeen.IsZero() || obs[0].Samples == 0 {
		t.Errorf("observation first_seen/samples 未填充: %+v", obs[0])
	}

	// ④ 解析层可见性：已知字段 = 声明 ∪ 观测
	known := map[string]bool{}
	for _, f := range c.registry.KnownFields(c.device.ID) {
		known[f.Name] = true
	}
	for _, want := range []string{"rsoc", "temperature_1", "temperature_2", "cell_count"} {
		if !known[want] {
			t.Errorf("KnownFields 缺少 %q", want)
		}
	}
	// 语义解析：@soc → rsoc
	resolved, err := c.registry.Resolve(c.device.ID, "@soc")
	if err != nil || resolved.Status != sensorfield.StatusResolved || len(resolved.Matched) != 1 || resolved.Matched[0] != "rsoc" {
		t.Fatalf("Resolve(@soc) = %+v, err=%v; want resolved rsoc", resolved, err)
	}

	// ⑤ 功能反馈：规则触发 → automation_events(result=notification) + notifications 行
	var events []models.AutomationEvent
	if err := c.db.Where("rule_id = ?", rule.ID).Find(&events).Error; err != nil {
		t.Fatalf("query automation_events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("automation_events = %d, want 1: %+v", len(events), events)
	}
	if events[0].Result != models.AutomationResultNotification {
		t.Errorf("automation event result = %q, want %q (detail=%s)", events[0].Result, models.AutomationResultNotification, events[0].Detail)
	}
	var n int64
	if err := c.db.Model(&models.Notification{}).Where("source = ?", "automation_rule").Count(&n).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if n != 1 {
		t.Errorf("notifications(source=automation_rule) = %d, want 1", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 场景 2：族选择器 + 缺员语义 —— temperature_*:max 在 2 探头帧上仍求值（降级），
// 且只有真正超温的帧才触发
// ─────────────────────────────────────────────────────────────────────────────

func TestEdgeDeviceDataFlow_FamilySelectorAndDegraded(t *testing.T) {
	rule := &models.AutomationRule{
		Name: "过温保护", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 0,
		TriggerSensorName:   "temperature_*:max", TriggerComparator: "gt", TriggerThreshold: 45,
		ActionType: models.AutomationActionNotification,
	}
	c := newE2EChain(t, rule)

	normal := bmsSample{totalVoltageDeciV: 5120, remainingCentiAh: 1100, nominalCentiAh: 5000, rsoc: 80, cellCount: 4, temps: []int16{2981, 2992}} // 24.95/26.05℃

	// 第 1 帧：冷启动 —— 该型号未静态声明 temperature_N（动态族），解析层"已知集合"还是空，
	// 故族选择器判 unbound 并**落审计事件**（不是静默）。这验证了"允许未知 selector 存在，
	// 但执行期不猜测"的失败语义。
	c.publish(t, normal.frame(), 1)
	if n := c.countByResult(t, rule.ID, automation.ResultUnresolvedSelector); n != 1 {
		t.Fatalf("冷启动首帧应落 1 条 unresolved_selector 审计，实际=%d", n)
	}
	if n := c.countByResult(t, rule.ID, models.AutomationResultNotification); n != 0 {
		t.Fatalf("冷启动帧不应触发通知，实际=%d", n)
	}

	// 投影刷新（生产每 60s 一次）：观测集进入解析层，"已知集合"此时才包含 temperature_1/2。
	// 这正是设计 §16 R-3 登记的滞后：动态族无静态声明时必须依赖观测投影。
	c.flushAndReloadObs(t)

	// 第 2 帧：族已可解析，常温 ⇒ 不触发
	c.publish(t, normal.frame(), 2)
	if n := c.countByResult(t, rule.ID, models.AutomationResultNotification); n != 0 {
		t.Fatalf("常温帧不应触发，通知数=%d", n)
	}

	// 第 3 帧：超温 ⇒ 触发 1 次
	hot := bmsSample{totalVoltageDeciV: 5120, remainingCentiAh: 1100, nominalCentiAh: 5000, rsoc: 78, cellCount: 4, temps: []int16{3191, 3202}} // 45.95/47.05℃
	c.publish(t, hot.frame(), 3)
	if n := c.countByResult(t, rule.ID, models.AutomationResultNotification); n != 1 {
		var evs []models.AutomationEvent
		_ = c.db.Where("rule_id = ?", rule.ID).Order("id").Find(&evs).Error
		for _, e := range evs {
			t.Logf("event: result=%s detail=%s value=%v", e.Result, e.Detail, e.TriggerValue)
		}
		t.Fatalf("超温帧应触发通知 1 次，实际=%d（事件明细见上）", n)
	}

	// 降级语义：已知集合比本帧多（声明含 temperature_1..N），max 仍求值而非 unbound
	plan, err := c.registry.Compile(c.device.ID, "temperature_*:max")
	if err != nil {
		t.Fatalf("compile family: %v", err)
	}
	frame := []parser.Field{{Name: "temperature_1", Value: 50}, {Name: "temperature_2", Value: 30}}
	verdict := sensorfield.EvalPredicate(plan, []sensorfield.Reading{{Name: "temperature_1", Value: 50}, {Name: "temperature_2", Value: 30}}, sensorfield.CmpGT, 45)
	if !verdict.OK || !verdict.Satisfied {
		t.Errorf("family max(50,30) > 45 = %+v, want OK+Satisfied（缺员应降级求值而非 unbound）", verdict)
	}
	_ = frame
}

func (c *e2eChain) countByResult(t *testing.T, ruleID uint, result string) int64 {
	t.Helper()
	var n int64
	if err := c.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", ruleID, result).Count(&n).Error; err != nil {
		t.Fatalf("count events by result: %v", err)
	}
	return n
}

var _ = binary.BigEndian
var _ = fmt.Sprintf

func (c *e2eChain) flushAndReloadObs(t *testing.T) {
	t.Helper()
	if err := c.registry.Flush(context.Background(), gormObsSink{db: c.db}); err != nil {
		t.Fatalf("flush observations: %v", err)
	}
	var obs []models.SensorFieldObs
	if err := c.db.Where("device_id = ?", c.device.ID).Find(&obs).Error; err != nil {
		t.Fatalf("query sensor_field_obs: %v", err)
	}
	fields := make([]sensorfield.Field, 0, len(obs))
	for _, o := range obs {
		fields = append(fields, sensorfield.Field{Name: o.SensorName, Unit: o.Unit, Observed: true})
	}
	c.registry.SetObservations(c.device.ID, fields)
}

// rejectingPublisher 模拟"节点没有 live session"：Publish 返回错误。
// 与生产一致（downlink.Bridge 无 session 时返回 error，绝不假装成功）。
type rejectingPublisher struct{}

func (rejectingPublisher) Publish(string, []byte) error {
	return errNoLiveSession
}

var errNoLiveSession = errors.New("e2e: node has no live session")

// attachPlanner 让 Planner 重新装载规则（规则通常在 chain 构造后创建）。
func (c *e2eChain) attachPlanner(rules ...*models.AutomationRule) {
	c.eval.LoadRules()
	for _, r := range rules {
		_ = r
	}
}

// dispatchOnce 驱动一次 dispatcher 出队，返回是否有记录被处理。
func (c *e2eChain) dispatchOnce(t *testing.T) bool {
	t.Helper()
	ok, err := c.disp.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("dispatcher ProcessOnce: %v", err)
	}
	return ok
}


// E2ECapabilitiesJSON 节点上报的命令引擎能力（commandexec 门禁读取）。
const E2ECapabilitiesJSON = `{"supports_channel_cmd_v2":true,"supports_bounded_batch":true,"supports_finally":true,"max_batch_steps":8,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`
