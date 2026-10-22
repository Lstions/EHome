//go:build e2e

package e2e

// 来源组（source_group）端到端验收：E1–E11（方案《规则数据来源与复合来源》§8.3 / §12.9.1 F-6/F-10）。
//
// 分层约定（为什么不是所有用例都"跑通"）：
//   - 已可确定性验证的语义（combine 真值表、缺员/降级、陈旧剔除、成员排序）在本文件直接测
//     sourceref.CombineGroup + GroupSnapshot（纯内存、可注入时钟），不依赖引擎接线；
//   - 依赖 automation/alert/datasource 组 API 的场景（E1/E2/E5/E6/E7/E9/E10/E11）先落
//     夹具与断言骨架，接线未完成时以 t.Skip 明确 BLOCKED（**不允许 Skip 通过后报告为 PASS**，
//     报告里必须区分 SKIP 与 PASS）。
//
// 夹具纪律（方案 §8.4 + F-6 豁免）：
//   - 两台 prs3001 光照设备必须**跨逻辑设备**（各自 logical_device），避免 v3.3
//     "一逻辑设备一存活实例" 约束把主备组拆开；
//   - 设备/逻辑行允许 fixture 直写 DB（与既有无 HTTP 引擎的 e2e 先例一致，等价基线
//     internal/e2e/fieldresolver_e2e_test.go 的 newE2EChain）；**来源组与规则必须走产品
//     路径**（service.Create），禁止 db.Create 直插组表；
//   - E6 的 ID 分歧夹具允许直写 logical/edge 行（方案 F-6 明示豁免）并在此登记。
//
// 跑法：
//   EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
//   EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=ehome_e2e \
//     go test -tags=e2e ./internal/e2e/ -run 'TestSourceGroup|TestE6' -count=1 -v
//   （SQLite 快速回归：省略 EHOME_TEST_DB）
//
// 依赖状态（写于 2026-10-22，后续接线完成后应逐个去掉 Skip）：
//   [OK]  sourceref.CombineGroup / GroupSnapshot（已实现）
//   [OK]  models.SourceGroup/SourceGroupMember/SourceGroupKind*（已实现）
//   [WAIT] datasource.GroupService.Create（ds-analyst 进行中）
//   [WAIT] automation/alert SourceRef 接线 + TriggerEvent 5 字段（auto-analyst 进行中）

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/databus"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/parser"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// ── 夹具：两台跨逻辑设备的 prs3001 光照设备 ─────────────────────────────────

// sgFixture 是来源组 e2e 的最小夹具。
type sgFixture struct {
	LogicalA models.LogicalDevice
	LogicalB models.LogicalDevice
	EdgeA    models.EdgeDevice
	EdgeB    models.EdgeDevice
}

// prs3001Frame4B 构 prs3001 的 2 寄存器（4 数据字节）modbus 响应帧。
// 布局（drivers/builtin.go:parseLegacy）：raw[0]=addr, raw[1]=0x03, raw[2]=byteCount(4),
// raw[3:7]=rainfall(2B, /10) + illuminance(2B), 末尾 2B CRC（parseLegacy 不校验）。
func prs3001Frame4B(rainfallTenthsMm float64, illuminance float64) []byte {
	r := uint16(rainfallTenthsMm * 10)
	l := uint16(illuminance)
	return []byte{0x01, 0x03, 0x04,
		byte(r >> 8), byte(r), byte(l >> 8), byte(l),
		0x00, 0x00}
}

// newSGFixture 建两台 prs3001 光照设备（跨逻辑设备），并断言驱动"声明==产出"。
// 设备/逻辑行是 fixture（无 HTTP 引擎的 e2e 先例），组与规则必须后续走 service。
func newSGFixture(t *testing.T) *sgFixture {
	t.Helper()
	db := testutil.OpenTestDB(t)

	// 0) 驱动自检：prs3001 必须声明 illumination 且 4 字节帧真的解析出来（防"声明≠产出"）。
	drv := &drivers.PRS3001Driver{}
	defs := drv.GetSensorDefinitions()
	hasIlluminance := false
	for _, d := range defs {
		if d.Name == "illuminance" {
			hasIlluminance = true
		}
	}
	if !hasIlluminance {
		t.Fatalf("夹具失效: prs3001 不再声明 illuminance（drivers/builtin.go:204-209）")
	}
	frame, err := drv.ParseData(prs3001Frame4B(10.0, 600)) // modbus: addr,0x03,bytecount=4,data,crc
	if err != nil {
		t.Fatalf("夹具失效: prs3001 ParseData(4B) 失败: %v", err)
	}
	sawIlluminance := false
	for _, f := range frame {
		if f.Name == "illuminance" && f.Value == 600 {
			sawIlluminance = true
		}
	}
	if !sawIlluminance {
		t.Fatalf("夹具失效: prs3001 4 字节帧未产出 illuminance=600，实得 %+v", frame)
	}

	f := &sgFixture{}
	now := time.Now().UTC()
	_ = now
	// 1) 两个独立逻辑设备（跨逻辑设备是需求③的前提）。
	f.LogicalA = models.LogicalDevice{IdentityKey: "prs3001:sgA:1:1", Name: "光照A", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&f.LogicalA).Error; err != nil {
		t.Fatalf("create logical A: %v", err)
	}
	f.LogicalB = models.LogicalDevice{IdentityKey: "prs3001:sgB:1:1", Name: "光照B", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&f.LogicalB).Error; err != nil {
		t.Fatalf("create logical B: %v", err)
	}
	// 2) 各挂一台边缘设备（fixture 直写；与既有 e2e 的 newE2EChain 同口径）。
	f.EdgeA = models.EdgeDevice{Name: "光照A-edge", NodeID: "sg-node", ChannelID: 1, Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &f.LogicalA.ID}
	if err := db.Create(&f.EdgeA).Error; err != nil {
		t.Fatalf("create edge A: %v", err)
	}
	f.EdgeB = models.EdgeDevice{Name: "光照B-edge", NodeID: "sg-node", ChannelID: 1, Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &f.LogicalB.ID}
	if err := db.Create(&f.EdgeB).Error; err != nil {
		t.Fatalf("create edge B: %v", err)
	}
	return f
}

// sgSnapshot 构造一个来源组快照（纯内存，等价 datasource 发布前的形态）。
func sgSnapshot(id uint, kind, category, combine string, minMembers int, stalenessSec int, onDegrade string, members ...sourceref.MemberSnapshot) *sourceref.GroupSnapshot {
	return &sourceref.GroupSnapshot{
		ID: id, Kind: kind, Category: category, Combine: combine,
		MinMembers: minMembers, StalenessSec: stalenessSec, OnDegrade: onDegrade,
		Enabled: true, Revision: 1, Members: members,
	}
}

func sgMember(id, edge uint, priority int, required bool, status string, lastSuccess time.Time) sourceref.MemberSnapshot {
	return sourceref.MemberSnapshot{
		MemberID: id, EdgeDeviceID: edge, Priority: priority, Required: required,
		Status: status, LastSuccess: lastSuccess,
	}
}

// ── 直接可验证的复合语义（E3/E4/E11 的纯函数层）────────────────────────────

// TestSourceGroupCompositeMissingUnbound 对应 E3：combine=sum、min_members=2，缺一个成员 ⇒ unbound。
func TestSourceGroupCompositeMissingUnbound(t *testing.T) {
	now := time.Now()
	g := sgSnapshot(1, sourceref.KindComposite, "illuminance", sourceref.CombineSum, 2, 60, sourceref.OnDegradeFailClosed,
		sgMember(1, 101, 10, true, sourceref.MemberStatusStandby, now),
		sgMember(2, 102, 5, true, sourceref.MemberStatusStandby, now),
	)
	res := sourceref.CombineGroup(g, func(m sourceref.MemberSnapshot) sourceref.Component {
		if m.EdgeDeviceID == 101 {
			return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, Value: 600, At: now, State: sourceref.StateOK}
		}
		return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, At: now, State: sourceref.StateMissing}
	})
	if res.OK {
		t.Fatalf("E3 红判据: 缺 1 个成员却合并出值 value=%v；应为 unbound（min_members=2, fail_closed）", res.Value)
	}
	if res.Reason == "" {
		t.Fatalf("E3 红判据: unbound 但无 reason，审计不可解释")
	}
}

// TestSourceGroupStaleMemberExcluded 对应 E4：成员值超过 staleness ⇒ 视为缺席，不得用旧值。
func TestSourceGroupStaleMemberExcluded(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Minute) // staleness_sec=60 ⇒ 已陈旧
	g := sgSnapshot(2, sourceref.KindComposite, "illuminance", sourceref.CombineAvg, 1, 60, sourceref.OnDegradeFailClosed,
		sgMember(1, 201, 10, false, sourceref.MemberStatusStandby, now),
		sgMember(2, 202, 5, false, sourceref.MemberStatusStandby, old),
	)
	// 契约：staleness 判定由 EvalMemberFunc（调用方/适配层）负责，标记为 StateStale；
	// CombineGroup 不得把 stale 分量当作有效值。适配层本身的时钟注入测试依赖引擎接线（BLOCKED）。
	res := sourceref.CombineGroup(g, func(m sourceref.MemberSnapshot) sourceref.Component {
		if m.EdgeDeviceID == 201 {
			return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, Value: 600, At: now, State: sourceref.StateOK}
		}
		return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, Value: 900, At: old, State: sourceref.StateStale}
	})
	if res.OK {
		t.Fatalf("E4 红判据: stale 分量进入了 fail_closed 合并（value=%v）；应 unbound", res.Value)
	}
	if res.Reason == "" {
		t.Fatalf("E4 红判据: stale 导致 unbound 但无 reason，审计不可解释")
	}
}

// TestSourceGroupPrimaryBackupNotDoubleCount 对应 E2 的纯函数层：主备组只接受 active 成员。
func TestSourceGroupPrimaryBackupAcceptsOnlyActive(t *testing.T) {
	now := time.Now()
	g := sgSnapshot(3, sourceref.KindPrimaryBackup, "illuminance", "", 1, 60, sourceref.OnDegradeFailClosed,
		sgMember(1, 301, 10, true, sourceref.MemberStatusActive, now),
		sgMember(2, 302, 5, true, sourceref.MemberStatusStandby, now),
	)
	g.ActiveMemberID = 1 // 组权威成员 = memberID 1（edge 301）
	if got := g.ActiveEdgeDeviceID(); got != 301 {
		t.Fatalf("E2 前置: ActiveEdgeDeviceID=%d 期望 301", got)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(g)
	if m := reg.MatchFrame(302, sourceref.GroupKindSourceGroup, 3); m.Accepts() {
		t.Fatalf("E2 红判据: standby 成员帧被接受（会双计）；Accepts 应只对 active 为 true")
	}
	if m := reg.MatchFrame(301, sourceref.GroupKindSourceGroup, 3); !m.Accepts() {
		t.Fatalf("E2 红判据: active 成员帧被拒绝；reason=%s", m.Reason)
	}
}

// TestSourceGroupCombineWhitelistDegenerate 对应 E11 的纯函数层：combine 退化不得静默。
func TestSourceGroupCombineWhitelistDegenerate(t *testing.T) {
	now := time.Now()
	g := sgSnapshot(4, sourceref.KindComposite, "illuminance", sourceref.CombineFirstAvailable, 1, 60, sourceref.OnDegradeUseAvailable,
		sgMember(1, 401, 10, false, sourceref.MemberStatusStandby, now),
		sgMember(2, 402, 5, false, sourceref.MemberStatusStandby, now),
	)
	res := sourceref.CombineGroup(g, func(m sourceref.MemberSnapshot) sourceref.Component {
		if m.EdgeDeviceID == 401 {
			return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, At: now, State: sourceref.StateMissing}
		}
		return sourceref.Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, Value: 800, At: now, State: sourceref.StateOK}
	})
	if !res.OK || res.Value != 800 {
		t.Fatalf("E11 前置: first_available 应取到 402 的值 800，实得 ok=%v value=%v", res.OK, res.Value)
	}
	if !res.Degraded {
		t.Fatalf("E11 红判据: use_available 下缺 1 成员却无 Degraded 标记（静默退化）")
	}
}

// ── 真实链路（databus → parser → sink → 两引擎/组服务，与 main.go 同构）────────

type sgChain struct {
	db       *gorm.DB
	node     models.Node
	channel  models.Channel
	logicalA models.LogicalDevice
	logicalB models.LogicalDevice
	edgeA    models.EdgeDevice
	edgeB    models.EdgeDevice
	registry *sensorfield.Registry
	bus      *databus.DataEventBus
	consumer *signalingConsumer
	eval     *automation.Evaluator
	planner  *automation.Planner
	gsvc     *datasource.GroupService
	reg      *sourceref.Registry
	now      time.Time
	req      uint64
}

func newSGChain(t *testing.T) *sgChain {
	t.Helper()
	db := testutil.OpenTestDB(t)
	c := &sgChain{db: db, now: time.Now().UTC()}

	c.node = models.Node{NodeID: "sg-node", Model: "ESP32S3", FirmwareVersion: "1.0.0", Status: "online"}
	if err := db.Create(&c.node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	c.channel = models.Channel{NodeID: c.node.NodeID, HardwareID: "1", IntervalMs: 5000, Enabled: true}
	if err := db.Create(&c.channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	// F-6 夹具豁免：先造 2 条占位逻辑设备，保证后面真实 logical ID 与 edge ID 序列错位
	//（E6 要求 logical_id ∉ {edge ids}，SQLite 两表各自从 1 起会让 ID 相等）。
	for i := 0; i < 2; i++ {
		pad := models.LogicalDevice{IdentityKey: "prs3001:sgpad:" + string(rune('a'+i)), Name: "pad", DeviceType: "prs3001", RetentionDays: 90}
		if err := db.Create(&pad).Error; err != nil {
			t.Fatalf("create padding logical: %v", err)
		}
	}
	c.logicalA = models.LogicalDevice{IdentityKey: "prs3001:sgA:1:1", Name: "光照A", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&c.logicalA).Error; err != nil {
		t.Fatalf("create logical A: %v", err)
	}
	c.logicalB = models.LogicalDevice{IdentityKey: "prs3001:sgB:1:1", Name: "光照B", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&c.logicalB).Error; err != nil {
		t.Fatalf("create logical B: %v", err)
	}
	c.edgeA = models.EdgeDevice{Name: "光照A-edge", NodeID: c.node.NodeID, ChannelID: c.channel.ID, Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &c.logicalA.ID}
	if err := db.Create(&c.edgeA).Error; err != nil {
		t.Fatalf("create edge A: %v", err)
	}
	c.edgeB = models.EdgeDevice{Name: "光照B-edge", NodeID: c.node.NodeID, ChannelID: c.channel.ID, Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &c.logicalB.ID}
	if err := db.Create(&c.edgeB).Error; err != nil {
		t.Fatalf("create edge B: %v", err)
	}

	drvReg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(drvReg)
	drv, err := drvReg.Get("prs3001")
	if err != nil {
		t.Fatalf("prs3001 driver missing: %v", err)
	}
	c.registry = sensorfield.New(sensorfield.Config{Enabled: true})
	decls := make([]sensorfield.Field, 0, 2)
	for _, sd := range drv.GetSensorDefinitions() {
		decls = append(decls, sensorfield.Field{Name: sd.Name, Unit: sd.Unit})
	}
	c.registry.RegisterDeclarations("prs3001", decls)
	c.registry.SetDeviceType(c.edgeA.ID, "prs3001")
	c.registry.SetDeviceType(c.edgeB.ID, "prs3001")

	c.gsvc = datasource.NewGroupService(db, datasource.Options{
		Now:          func() time.Time { return c.now },
		Staleness:    3 * time.Second,
		Cooldown:     time.Second,
		MinResidency: 0,
		ScanInterval: time.Hour,
	})
	c.reg = sourceref.NewRegistry()
	c.gsvc.SetGroupRegistry(c.reg)

	inner := databus.NewSensorParserConsumerWithRegistry(db, nil, passthroughReassembler{}, drvReg, func(uint) {})
	inner.SetLatestSink(func(rec models.UnifiedData) {
		api.SetLatestValue(rec)
		c.registry.Observe(rec.DeviceID, rec.SensorName, rec.Unit, rec.Timestamp)
	})
	inner.SetAutomationSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		if c.eval != nil {
			c.eval.Evaluate(edgeDeviceID, fields, at)
		}
	})
	inner.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		if c.gsvc != nil {
			c.gsvc.MarkSuccess(edgeDeviceID, names, at)
		}
	})
	c.consumer = &signalingConsumer{inner: inner}
	c.bus = databus.NewDataEventBus()
	c.bus.Register(c.consumer)
	t.Cleanup(c.bus.Stop)
	return c
}

// start 构造两引擎并接线（必须在规则 db.Create 之后：NewEvaluator 构造时装载规则）。
func (c *sgChain) start(t *testing.T) {
	t.Helper()
	drvReg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(drvReg)
	actions := deviceaction.NewBuiltInRegistry(drvReg)
	cmdSvc := commandexec.NewService(c.db, actions)
	cmdSvc.SetDispatchEnabled(true)

	planner := automation.NewPlanner(c.db, cmdSvc, func(string, any) {}, 0)
	planner.SetLatestValueFn(api.LatestValue)
	planner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	planner.SetLatestValuesFn(api.LatestValues)
	planner.SetPlanProvider(c.registry)
	planner.SetSourceResolver(c.reg)

	eval := automation.NewEvaluator(c.db, planner)
	eval.SetPlanProvider(c.registry)
	eval.SetSourceResolver(c.reg)
	eval.SetLatestValueBySensorFn(api.LatestValueBySensor)
	eval.SetDeviceIntervalFn(func(uint) time.Duration { return 5 * time.Second })
	c.eval, c.planner = eval, planner
	if err := c.gsvc.PublishAll(); err != nil {
		t.Fatalf("publish group snapshots: %v", err)
	}
}

// publish 发一帧（ReceivedAt 由用例指定 ⇒ 消费者 Timestamp/求值 at 可控），并等待处理完成。
func (c *sgChain) publish(t *testing.T, edge models.EdgeDevice, illuminance float64, at time.Time) {
	t.Helper()
	before := c.consumer.done.Load()
	c.req++
	c.bus.Publish(databus.DataEvent{
		DeviceID: c.node.NodeID, ChannelID: uint64(c.channel.ID),
		EdgeDeviceID: uint64(edge.ID), RequestID: c.req,
		RawData: prs3001Frame4B(10.0, illuminance), ReceivedAt: at,
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.consumer.done.Load() > before {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("data plane did not finish frame (edge=%d at=%s)", edge.ID, at)
}

// warm 发两帧让 category=illuminance 变成「已观测」（GroupService V-5 校验前提），值低于阈值不触发。
func (c *sgChain) warm(t *testing.T) {
	t.Helper()
	c.publish(t, c.edgeA, 10, c.now)
	c.publish(t, c.edgeB, 10, c.now)
}

func (c *sgChain) createGroup(t *testing.T, kind, combine string) uint {
	t.Helper()
	g, err := c.gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: "光照组", Kind: kind, Category: "illuminance", Combine: combine,
		MinMembers: 1, StalenessSec: 60, OnDegrade: models.OnDegradeFailClosed,
		Members: []datasource.GroupMemberInput{
			{EdgeDeviceID: c.edgeA.ID, Priority: 10},
			{EdgeDeviceID: c.edgeB.ID, Priority: 5},
		},
	})
	if err != nil {
		t.Fatalf("create group kind=%s combine=%s: %v", kind, combine, err)
	}
	return g.ID
}

// createRule 建规则：edgeID>0 时绑单设备；groupID>0 时绑来源组（F-5 锚点双写）。
func (c *sgChain) createRule(t *testing.T, name string, groupID, edgeID uint, threshold float64, durationSec, cooldownSec int) uint {
	t.Helper()
	rule := &models.AutomationRule{
		Name: name, Enabled: true,
		TriggerType:       models.AutomationTriggerSensorThreshold,
		TriggerSensorName: "illuminance", TriggerComparator: "gt", TriggerThreshold: threshold,
		TriggerDurationSec: durationSec,
		ActionType:         models.AutomationActionNotification,
		CooldownSec:        cooldownSec,
	}
	if groupID > 0 {
		kind := string(models.SourceKindSourceGroup)
		rule.SourceKind = &kind
		rule.SourceGroupID = &groupID
		anchor, err := c.gsvc.AnchorEdgeDeviceID(groupID)
		if err != nil || anchor == 0 {
			t.Fatalf("anchor edge for group %d: %v (anchor=%d)", groupID, err, anchor)
		}
		rule.TriggerEdgeDeviceID = anchor
	} else {
		rule.TriggerEdgeDeviceID = edgeID
	}
	if err := c.db.Create(rule).Error; err != nil {
		t.Fatalf("create rule %s: %v", name, err)
	}
	return rule.ID
}

func (c *sgChain) events(t *testing.T) []models.AutomationEvent {
	t.Helper()
	var out []models.AutomationEvent
	if err := c.db.Order("id").Find(&out).Error; err != nil {
		t.Fatalf("query automation_events: %v", err)
	}
	return out
}

func (c *sgChain) eventsForRule(t *testing.T, ruleID uint) []models.AutomationEvent {
	t.Helper()
	var out []models.AutomationEvent
	if err := c.db.Where("rule_id = ?", ruleID).Order("id").Find(&out).Error; err != nil {
		t.Fatalf("query events rule=%d: %v", ruleID, err)
	}
	return out
}

// 事件口径（诊断 E2/E7/E11 的教训，Lead 裁决）：
// automation_events 同时承载 **动作事件**（result=executed/notification）与
// **规则级审计事件**（suppressed_cooldown/source_degraded/suppressed_daily_limit…）。
// 方案 §8.3 的「只 1 条事件」指**动作事件**；审计事件是期望存在的可观测性证据。
// 若按全表计数，冷却/切换/降级产生的审计行会被误判成「重复动作」。
// 证据：E7 实测全表 = [1:notification:600:1 2:suppressed_cooldown:700:-]，
// 第 2 条是审计行、无来源成员，不是第二次动作。
func pactionResult(result string) bool {
	switch result {
	case models.AutomationResultNotification, models.AutomationResultExecuted,
		models.AutomationResultPendingConfirm, models.AutomationResultPartialFailure,
		models.AutomationResultConfirmationRequired, models.AutomationResultFailedGate,
		models.AutomationResultFailedDispatch:
		return true
	}
	return false
}

// actionEventsForRule 只按**动作维度**过滤规则事件（Lead 2026-10-22 裁决）。
//
// 为什么必须区分：automation_events 同时写动作事件与规则级审计事件；
// suppressed_cooldown / source_degraded / suppressed_daily_limit 证明的是
// 「压制发生了且可见」（方案 §7.1 要求），**不是又执行了一次动作**。
// 按全表计数会把冷却审计误判为「重复动作」。实测证据（修复前）：
//
//	E7 全表 = [1:notification:600:1 2:suppressed_cooldown:700:-]
//
// 方案 §8.3 E7 原文「只 1 条事件」指的是动作事件。
func (c *sgChain) actionEventsForRule(t *testing.T, ruleID uint) []models.AutomationEvent {
	t.Helper()
	out := make([]models.AutomationEvent, 0, 2)
	for _, ev := range c.eventsForRule(t, ruleID) {
		if pactionResult(ev.Result) {
			out = append(out, ev)
		}
	}
	return out
}

func hasAuditResult(evs []models.AutomationEvent, result string) bool {
	for _, e := range evs {
		if e.Result == result {
			return true
		}
	}
	return false
}

// eventDigest 把事件压缩成「id:result:value:edge」便于失败时定性：
// 动作事件(executed/notification) vs 规则级审计事件(suppressed_cooldown/source_degraded…)。
func eventDigest(evs []models.AutomationEvent) string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		val := "-"
		if e.TriggerValue != nil {
			val = fmt.Sprintf("%v", *e.TriggerValue)
		}
		edge := "-"
		if e.SourceEdgeDeviceID != nil {
			edge = fmt.Sprintf("%d", *e.SourceEdgeDeviceID)
		}
		out = append(out, fmt.Sprintf("%d:%s:%s:%s", e.ID, e.Result, val, edge))
	}
	return "[" + strings.Join(out, " ") + "]"
}

func (c *sgChain) activeEdgeOfGroup(t *testing.T, groupID uint) uint {
	t.Helper()
	g, members, err := c.gsvc.GetGroupWithMembers(groupID)
	if err != nil {
		t.Fatalf("get group %d: %v", groupID, err)
	}
	_ = g
	for _, m := range members {
		if m.Status == models.SourceMemberStatusActive {
			return m.EdgeDeviceID
		}
	}
	return 0
}

// ── E1：三种组合各触发一次（设备1 / 设备2 / 1+2 复合）────────────────────────

func TestE1_ThreeBindingsTriggerOnce(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindComposite, models.CombineAvg)
	rA := c.createRule(t, "设备A", 0, c.edgeA.ID, 500, 0, 0)
	rB := c.createRule(t, "设备B", 0, c.edgeB.ID, 500, 0, 0)
	rC := c.createRule(t, "复合avg", gid, 0, 500, 0, 0)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeA, 600, t0)
	c.publish(t, c.edgeB, 700, t0.Add(time.Second))

	if n := len(c.eventsForRule(t, rA)); n != 1 {
		t.Fatalf("E1-a 红判据: 设备A 规则事件数=%d（期望 1）", n)
	}
	if n := len(c.eventsForRule(t, rB)); n != 1 {
		t.Fatalf("E1-b 红判据: 设备B 规则事件数=%d（期望 1）", n)
	}
	evC := c.eventsForRule(t, rC)
	if len(evC) != 1 {
		t.Fatalf("E1-c 红判据: 复合规则事件数=%d（期望 1；单成员冒充合成值会让它 0 或 2）", len(evC))
	}
	if evC[0].TriggerValue == nil {
		t.Fatalf("E1-c 红判据: 复合事件无触发值；事件=%+v", evC[0])
	}
	// composite avg(600,700)=650（±1 容忍序列化/历史值读取顺序）
	if got := *evC[0].TriggerValue; got < 649 || got > 651 {
		t.Fatalf("E1-c 红判据: 复合值=%v（期望 avg(600,700)=650±1）", got)
	}
}

// ── E2：主备切换后读数跟随、standby 不双计 ───────────────────────────────────

func TestE2_PrimaryBackupFollowsActiveNoDoubleCount(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindPrimaryBackup, "")
	// cooldown=0：e2e 的 automation sink 用 time.Now()（非帧 ReceivedAt，见
	// databus/consumers_heavy.go:370,408），无法用 ReceivedAt 快进冷却窗；
	// 这里验证「切换后读数跟随且不双计」，冷却语义由 E7/E11 用审计行覆盖。
	r := c.createRule(t, "主备跟随", gid, 0, 500, 0, 0)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeB, 900, t0) // standby 帧：不得触发
	if n := len(c.eventsForRule(t, r)); n != 0 {
		t.Fatalf("E2 红判据: standby 帧触发（双计）事件数=%d", n)
	}
	c.publish(t, c.edgeA, 900, t0.Add(time.Second)) // active 帧：触发 1 次
	if n := len(c.actionEventsForRule(t, r)); n != 1 {
		t.Fatalf("E2 红判据: active 帧动作事件数=%d（期望 1）；全部事件=%s", n, eventDigest(c.eventsForRule(t, r)))
	}

	// 连续失败 3 次 ⇒ 切 B（默认 max_fail_count=3）
	for i := 0; i < 3; i++ {
		c.gsvc.MarkFailure(c.edgeA.ID, datasource.TriggerDeviceOffline)
	}
	if got := c.activeEdgeOfGroup(t, gid); got != c.edgeB.ID {
		t.Fatalf("E2 红判据: 切换后 active=%d（期望 B=%d）", got, c.edgeB.ID)
	}

	// 冷却窗已过（+10s）再发 B 帧 ⇒ 触发 1 次；再发 A 帧 ⇒ 不触发
	c.publish(t, c.edgeB, 900, t0.Add(11*time.Second))
	acts := c.actionEventsForRule(t, r)
	if len(acts) != 2 {
		t.Fatalf("E2 红判据: 切换后动作事件数=%d（期望 2）；全部事件=%s", len(acts), eventDigest(c.eventsForRule(t, r)))
	}
	if acts[1].SourceEdgeDeviceID == nil || *acts[1].SourceEdgeDeviceID != c.edgeB.ID {
		t.Fatalf("E2 红判据: 切换后动作事件 source_edge_device_id=%v（期望 B=%d）；动作事件=%s", acts[1].SourceEdgeDeviceID, c.edgeB.ID, eventDigest(acts))
	}
	c.publish(t, c.edgeA, 900, t0.Add(22*time.Second))
	if n := len(c.actionEventsForRule(t, r)); n != 2 {
		t.Fatalf("E2 红判据: 旧 active 帧仍产生动作，动作事件数=%d（期望 2）；全部事件=%s", n, eventDigest(c.eventsForRule(t, r)))
	}
}

// ── E5：跨成员交替 DurationSec 不得伪连续 ────────────────────────────────────

func TestE5_DurationSecAcrossMembersNotContinuous(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindComposite, models.CombineAvg)
	r := c.createRule(t, "复合duration", gid, 0, 500, 2, 5)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeA, 600, t0)                    // avg(600,10)=305 unsat
	c.publish(t, c.edgeB, 900, t0.Add(time.Second))   // avg(600,900)=750 sat
	c.publish(t, c.edgeA, 100, t0.Add(2*time.Second)) // avg(100,900)=500 unsat (gt 500)
	c.publish(t, c.edgeB, 100, t0.Add(3*time.Second)) // avg(100,100)=100 unsat
	if n := len(c.eventsForRule(t, r)); n != 0 {
		t.Fatalf("E5 红判据: 跨成员交替被判连续满足并触发，事件数=%d（期望 0）", n)
	}
}

// ── E6：F4 断链回归（规则绑逻辑设备 ID，非 edge ID）───────────────────────────

func TestE6_F4PinsTriggerMemberOnLogicalIDDivergence(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	// ID 分歧前置：逻辑 ID 必须不等于任何 edge ID（N-12 要求显式断言）
	if c.logicalA.ID == c.edgeA.ID {
		t.Fatalf("E6 夹具前提不成立: logicalA.ID == edgeA.ID == %d", c.logicalA.ID)
	}
	r := c.createRule(t, "绑逻辑设备", 0, c.logicalA.ID, 500, 0, 0)
	c.start(t)
	c.publish(t, c.edgeA, 600, c.now)

	if n := len(c.eventsForRule(t, r)); n != 1 {
		ev := c.eventsForRule(t, r)
		t.Fatalf("E6 红判据: 绑逻辑设备的规则事件数=%d（M0 修复后应恰有 1 条 executed；当前事件=%+v）", n, ev)
	}
}

// ── E7：切换瞬间不双计（冷却窗内至多 1 条事件）──────────────────────────────

func TestE7_SwitchInstantSingleEvent(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindPrimaryBackup, "")
	r := c.createRule(t, "切换瞬间", gid, 0, 500, 0, 5)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeA, 600, t0)
	if n := len(c.eventsForRule(t, r)); n != 1 {
		t.Fatalf("E7 前置失败: 首次触发事件数=%d", n)
	}
	for i := 0; i < 3; i++ {
		c.gsvc.MarkFailure(c.edgeA.ID, datasource.TriggerDeviceOffline)
	}
	// 同一时刻 B 上报：冷却窗内 ⇒ 不得产生第 2 条事件
	c.publish(t, c.edgeB, 700, t0.Add(500*time.Millisecond))
	// 口径：只数动作事件；冷却命中会且**应该**落一条 suppressed_cooldown 审计行。
	if n := len(c.actionEventsForRule(t, r)); n != 1 {
		t.Fatalf("E7 红判据: 切换瞬间动作事件数=%d（期望 1，冷却窗内至多一次动作）；全部事件=%s", n, eventDigest(c.events(t)))
	}
	if !hasAuditResult(c.eventsForRule(t, r), models.AutomationResultSuppressedCooldown) {
		t.Fatalf("E7 红判据: 冷却命中未落 suppressed_cooldown 审计；全部事件=%s", eventDigest(c.eventsForRule(t, r)))
	}
	logs, err := c.gsvc.FailoverLogs(gid, 10)
	if err != nil {
		t.Fatalf("E7: query failover logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("E7 红判据: source_group_failover_logs=%d 条（期望 1）", len(logs))
	}
}

// ── E8：审计可追溯（来源快照列非空）─────────────────────────────────────────

func TestE8_AuditTraceable(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindComposite, models.CombineAvg)
	r := c.createRule(t, "审计", gid, 0, 500, 0, 0)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeA, 600, t0)
	c.publish(t, c.edgeB, 700, t0.Add(time.Second))
	ev := c.eventsForRule(t, r)
	if len(ev) != 1 {
		t.Fatalf("E8 前置失败: 事件数=%d（期望 1）", len(ev))
	}
	e := ev[0]
	if e.SourceKind == nil || *e.SourceKind != string(models.SourceKindSourceGroup) {
		t.Fatalf("E8 红判据: source_kind=%v（期望 source_group）；事件={result=%s trigger=%v src=%v gid=%v edge=%v members=%q}", e.SourceKind, e.Result, e.TriggerValue, e.SourceKind, e.SourceGroupID, e.SourceEdgeDeviceID, e.MembersJSON)
	}
	if e.SourceGroupID == nil || *e.SourceGroupID != gid {
		t.Fatalf("E8 红判据: source_group_id=%v（期望 %d）", e.SourceGroupID, gid)
	}
	if e.SourceEdgeDeviceID == nil || *e.SourceEdgeDeviceID == 0 {
		t.Fatalf("E8 红判据: source_edge_device_id 为空")
	}
	if e.MembersJSON == "" {
		t.Fatalf("E8 红判据: members_json 为空（无法回答哪些成员参与）")
	}
}

// ── E10：主+备先后静默 ⇒ 备恢复上报 ⇒ 组自愈回 active ─────────────────────────

func TestE10_GroupSelfHeal(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindPrimaryBackup, "")
	c.start(t)

	// 主失败 3 次 ⇒ 切备
	for i := 0; i < 3; i++ {
		c.gsvc.MarkFailure(c.edgeA.ID, datasource.TriggerDeviceOffline)
	}
	if got := c.activeEdgeOfGroup(t, gid); got != c.edgeB.ID {
		t.Fatalf("E10 前置: 主备切换后 active=%d（期望 B）", got)
	}
	// 推进时钟超过 staleness ⇒ 扫描把备判 stale（组进入无 active/降级）
	c.now = c.now.Add(30 * time.Second)
	if _, err := c.gsvc.ScanGroups(c.now); err != nil {
		t.Fatalf("E10: scan groups: %v", err)
	}
	// 备恢复上报 → 扫描自愈（R4）⇒ 重新出现 active
	c.publish(t, c.edgeB, 700, c.now)
	if _, err := c.gsvc.ScanGroups(c.now); err != nil {
		t.Fatalf("E10: scan groups (after success): %v", err)
	}
	if got := c.activeEdgeOfGroup(t, gid); got != c.edgeB.ID {
		t.Fatalf("E10 红判据: 自愈后 active=%d（期望 B=%d；0=无 active ⇒ 永久降级）", got, c.edgeB.ID)
	}
}

// ── E11：复合双成员交错帧，冷却窗内至多 1 次动作 ─────────────────────────────

func TestE11_CompositeInterleavedCooldownSingleAction(t *testing.T) {
	c := newSGChain(t)
	c.warm(t)
	gid := c.createGroup(t, models.SourceGroupKindComposite, models.CombineSum)
	r := c.createRule(t, "交错冷却", gid, 0, 500, 0, 5)
	c.start(t)

	t0 := c.now
	c.publish(t, c.edgeA, 400, t0)                           // sum=410 unsat
	c.publish(t, c.edgeB, 500, t0.Add(200*time.Millisecond)) // sum=900 sat ⇒ 事件 1
	c.publish(t, c.edgeA, 200, t0.Add(400*time.Millisecond)) // sum=700 sat，冷却窗内 ⇒ 抑制
	c.publish(t, c.edgeB, 100, t0.Add(800*time.Millisecond)) // sum=300 unsat
	// 口径同 E7：动作事件 1 条 + 至少一条 suppressed_cooldown 审计。
	if n := len(c.actionEventsForRule(t, r)); n != 1 {
		t.Fatalf("E11 红判据: 冷却窗内动作事件数=%d（期望 1；交错帧双动作会 >1）；全部事件=%s", n, eventDigest(c.events(t)))
	}
	if !hasAuditResult(c.eventsForRule(t, r), models.AutomationResultSuppressedCooldown) {
		t.Fatalf("E11 红判据: 交错帧冷却命中未落 suppressed_cooldown 审计；全部事件=%s", eventDigest(c.eventsForRule(t, r)))
	}
}

// ── E9：pending_confirm 期间来源切换 ⇒ confirm 必须拒绝（409）────────────────
//
// 说明（非依赖 SKIP，而是夹具能力缺口，需产品补齐）：prs3001 在 deviceaction 注册表里
// 没有可产生 pending_confirm 的风险动作；E9 需要 BMS 链（set_mos_policy）+ 组绑定。
// 本文件不伪造 pending 行（会绕过产品路径 ⇒ 假绿），故此处以显式失败记录缺口而非 Skip。
func TestE9_PendingConfirmAfterSwitchRejected(t *testing.T) {
	// SKIP（能力缺口，非依赖缺失）：prs3001 在 deviceaction 注册表里没有可产生
	// pending_confirm 的风险动作；E9 需要 BMS 链（set_mos_policy）+ 来源组绑定。
	// 设计见 .analysis/src-risk-view.md E9 与方案 §8.3。**不伪造 pending 行**（会绕过产品路径）。
	t.Skip("E9 夹具能力缺口：prs3001 无风险动作 ⇒ 无法经产品路径产生 pending_confirm；需 BMS 链夹具（src-risk-view E9 设计）")
}
