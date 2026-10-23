//go:build e2e

// 场景 4：BMS 边缘设备 + 数据源管理 + 规则引擎 → 日出/日落开关 BMS MOS。
//
// 这是用户要求的完整业务闭环：
//
//	BMS 上报（真实 0x03 帧）
//	  → 数据源域（MarkSuccess 更新 last_success/fail_count）
//	  → 规则引擎（sun_event 日出/日落触发）
//	  → device_action「set_mos_policy」（高风险，需规则级授权 O-2）
//	  → commandexec 九道门禁 + 幂等键
//	  → dispatcher 出队 → 下行 Publish（用假 Publisher 捕获真实 wire 帧）
//
// 为什么必须端到端：这四段的"接线"（sink 注入、plan provider、risk 授权、
// dispatcher 出队、transport 编码）任何一处漏接都会表现为"规则触发了但设备没动作"，
// 而每一段的单测都各自全绿。

package e2e

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/downlink"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"
)

// capturePublisher 捕获下行的真实 wire 帧（替代 TLS session），并记录节点 ID。
type capturePublisher struct {
	mu        sync.Mutex
	published []publishedFrame
}

type publishedFrame struct {
	nodeID  string
	payload []byte
}

func (p *capturePublisher) Publish(nodeID string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	p.published = append(p.published, publishedFrame{nodeID: nodeID, payload: cp})
	return nil
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

func (p *capturePublisher) last() (publishedFrame, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.published) == 0 {
		return publishedFrame{}, false
	}
	return p.published[len(p.published)-1], true
}

var _ downlink.Publisher = (*capturePublisher)(nil)

// buildSunMOSRule 造一条"日出关充电 MOS / 日落关放电 MOS"的规则。
//
// set_mos_policy 是 Risk=high 的 set 语义动作：按 O-2 裁决，自动路径只有在规则持有
// risk_acknowledged_at 时才跳过确认门 —— 这里显式写入该列，模拟"用户已授权"。
func buildSunMOSRule(edgeID uint, kind string, chargeClosed, dischargeClosed bool, acked bool) *models.AutomationRule {
	params, _ := json.Marshal(map[string]interface{}{
		"charge_software_closed":    chargeClosed,
		"discharge_software_closed": dischargeClosed,
		"priority":                  "operator",
	})
	rule := &models.AutomationRule{
		Name: "日出日落开关 BMS MOS", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerEdgeDeviceID: edgeID,
		TriggerSunKind:      kind,
		TriggerSunOffsetMin: 0,
		CooldownSec:         0,
		ActionType:          models.AutomationActionDeviceAction,
		ActionDeviceID:      edgeID,
		ActionID:            "set_mos_policy",
		ActionParamsJSON:    string(params),
	}
	if acked {
		now := time.Now().UTC()
		rule.RiskAcknowledgedAt = &now
	}
	return rule
}

// TestBMS_SunEventSwitchesMOS 是完整业务闭环验证。
func TestBMS_SunEventSwitchesMOS(t *testing.T) {
	pub := &capturePublisher{}
	c := newE2EChainWithPublisher(t, pub)

	// 节点定位：北京（日出日落时刻可算），时区 Asia/Shanghai
	setNodeLocation(t, c, 39.9042, 116.4074, "Asia/Shanghai")

	// 规则：日出 → 关充电 MOS（保留放电），并显式授权（O-2）
	rule := buildSunMOSRule(c.device.ID, models.AutomationSunKindSunrise, true, false, true)
	if err := c.db.Create(rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	c.attachPlanner(rule) // Planner 已由 newE2EChainWithPublisher 构建，这里装载规则

	// ── ① BMS 真实帧上报：数据源健康 + 规则引擎共享同一帧 ──
	bms := bmsSample{
		totalVoltageDeciV: 5120, currentCentiA: 1000,
		remainingCentiAh: 1100, nominalCentiAh: 5000, cycles: 42,
		rsoc: 27, cellCount: 4, temps: []int16{2981, 2992},
	}
	c.publish(t, bms.frame(), 1)

	// ── 数据源域断言（M3 后的语义）──
	//
	// M3（2026-10-23）删除了 legacy datasource.Service.MarkSuccess，因此
	// data_sources.last_success 不再由"每帧成功回调"直接写入，而是由
	// **投影器**从 source_group_members.last_success 回填。
	//
	// 本 e2e 的链没有接投影器（它验的是"解析→观测→规则→commandexec"，不是台账派生），
	// 所以这里**不应**断言 last_success 被写入 —— 那会要求 e2e 里跑一个后台对账循环，
	// 属测试夹具过度扩张。
	//
	// 改为断言本次改动真正关心的点：台账行存在、且其状态/来源字段合法
	// （即数据面与台账域**共存不冲突**）。last_success 的回填逻辑由
	// internal/datasource/projection_lastsync_test.go 的三个用例专门覆盖
	// （含"状态未变也要更新"这个最易漏的路径）。
	var src models.DataSource
	if err := c.db.First(&src, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	switch src.ManagedBy {
	case models.DataSourceManagedAuto, models.DataSourceManagedManual:
	default:
		t.Errorf("台账 managed_by=%q 非法（数据面与台账域可能未打通）", src.ManagedBy)
	}

	// ── ② 日出时刻 tick：规则触发 → device_action → commandexec ──
	eventAt := fireSunEvent(t, c, models.AutomationSunKindSunrise)

	// ── ③ 断言：automation_events 落 executed + command_executions 有记录 ──
	var events []models.AutomationEvent
	if err := c.db.Where("rule_id = ?", rule.ID).Find(&events).Error; err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("automation_events = %d, want 1: %+v", len(events), events)
	}
	if events[0].Result != models.AutomationResultExecuted {
		t.Fatalf("event result = %q (detail=%q), want executed", events[0].Result, events[0].Detail)
	}
	if events[0].CommandID == "" {
		t.Error("executed 事件应带 command_id（否则执行链断了）")
	}

	var exec models.CommandExecution
	if err := c.db.Where("command_id = ?", events[0].CommandID).First(&exec).Error; err != nil {
		t.Fatalf("load command execution %q: %v", events[0].CommandID, err)
	}
	if exec.ActionID != "set_mos_policy" {
		t.Errorf("command action = %q, want set_mos_policy", exec.ActionID)
	}
	switch exec.Status {
	case commandexec.StatusQueued, commandexec.StatusDispatched, commandexec.StatusDeviceAccepted, commandexec.StatusVerifying:
	default:
		t.Errorf("command status = %q, want QUEUED/DISPATCHED/SENT", exec.Status)
	}

	// ── ④ dispatcher 出队 → 下行 Publish：捕获真实 wire 帧 ──
	dispatched := c.dispatchOnce(t)
	if !dispatched {
		t.Fatalf("dispatcher 未出队（outbox 无待发记录）；command=%s", exec.CommandID)
	}
	if pub.count() != 1 {
		t.Fatalf("下行帧数 = %d, want 1", pub.count())
	}
	last, _ := pub.last()
	if last.nodeID != c.node.NodeID {
		t.Errorf("下行 nodeID = %q, want %q", last.nodeID, c.node.NodeID)
	}
	// 帧头必须是 ChannelCmdV2（0x15）—— 证明走的是受控指令协议而不是别的通道
	if len(last.payload) == 0 {
		t.Fatal("下行 payload 为空")
	}
	// capturePublisher 注入在 Transport 与 downlink.Bridge 之间 ⇒ 拿到的是 2.x 内层
	// payload（其首字节就是消息类型）。3.0 外框由 Bridge 负责，下面用假 sessionSender
	// 单独验证，两者都断言才算"命令真的被编成了节点能收的帧"。
	if len(last.payload) == 0 || last.payload[0] != frame.MsgChannelCmdV2 {
		t.Fatalf("内层 payload 类型 = 0x%02X, want 0x%02X (ChannelCmdV2)", firstByte(last.payload), frame.MsgChannelCmdV2)
	}
	// 3.0 外框：把同一 payload 过一遍真实 downlink.Bridge，断言头部可解且类型一致。
	sender := &captureSender{}
	bridge := downlink.New(sender)
	if err := bridge.Publish(c.node.NodeID, last.payload); err != nil {
		t.Fatalf("downlink bridge publish: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("bridge 未投递（frames=%d）", sender.count())
	}
	hdr, err := protoframe.DecodeHeader(sender.last())
	if err != nil {
		t.Fatalf("decode 3.0 header: %v", err)
	}
	if hdr.Type != frame.MsgChannelCmdV2 {
		t.Errorf("3.0 头部类型 = 0x%02X, want 0x%02X", hdr.Type, frame.MsgChannelCmdV2)
	}
	if int(hdr.PayloadLen) != len(last.payload) {
		t.Errorf("3.0 payloadLen = %d, want %d", hdr.PayloadLen, len(last.payload))
	}

	// ── ⑤ 幂等：同一日出事件重复 tick 不再产生第二个命令 ──
	firstCount := countCommands(t, c)
	c.tickSunAt(t, eventAt.Add(30*time.Second))
	if got := countCommands(t, c); got != firstCount {
		t.Errorf("同日重复 tick 产生了新命令：%d → %d（应被 sunStates 每日本地日一次拦住）", firstCount, got)
	}
}

// TestBMS_SunEventMOS_RequiresRiskAck 是上面场景的**负对照**：
// 同一条规则若未显式授权（risk_acknowledged_at 为空），自动路径必须落
// confirmation_required 而**不执行真实动作**。没有这条，"已授权能跑"就证明不了
// 授权门真的存在。
func TestBMS_SunEventMOS_RequiresRiskAck(t *testing.T) {
	pub := &capturePublisher{}
	c := newE2EChainWithPublisher(t, pub)
	setNodeLocation(t, c, 39.9042, 116.4074, "Asia/Shanghai")

	rule := buildSunMOSRule(c.device.ID, models.AutomationSunKindSunrise, true, false, false) // 未授权
	if err := c.db.Create(rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	c.attachPlanner(rule)

	fireSunEvent(t, c, models.AutomationSunKindSunrise)

	var events []models.AutomationEvent
	if err := c.db.Where("rule_id = ?", rule.ID).Find(&events).Error; err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("automation_events = %d, want 1", len(events))
	}
	if events[0].Result != models.AutomationResultConfirmationRequired {
		t.Fatalf("未授权高风险动作 result = %q, want %q（detail=%q）",
			events[0].Result, models.AutomationResultConfirmationRequired, events[0].Detail)
	}
	c.dispatchOnce(t)
	if pub.count() != 0 {
		t.Errorf("未授权的风险动作产生了 %d 个下行帧，应该为 0", pub.count())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────────────────────────────────────

func setNodeLocation(t *testing.T, c *e2eChain, lat, lon float64, tz string) {
	t.Helper()
	if err := c.db.Model(&models.Node{}).Where("node_id = ?", c.node.NodeID).
		Updates(map[string]interface{}{"latitude": lat, "longitude": lon, "timezone": tz}).Error; err != nil {
		t.Fatalf("set node location: %v", err)
	}
	if err := c.db.First(&c.node, c.node.ID).Error; err != nil {
		t.Fatalf("reload node: %v", err)
	}
}

// fireSunEvent 计算当日日出时刻并在事件后 1 分钟 tick 一次，返回事件时刻。
func fireSunEvent(t *testing.T, c *e2eChain, kind string) time.Time {
	t.Helper()
	tz, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	date := time.Date(2026, 6, 21, 12, 0, 0, 0, tz)
	eventAt, status, err := sun.SunEvent(39.9042, 116.4074, tz, date, sun.Kind(kind))
	if err != nil || status != sun.StatusOK {
		t.Fatalf("sun event fixture failed: %s %v", status, err)
	}
	// 首 tick 保守不触发（A-3），先做一次"事件前"的 tick 把 firstSunTick 消耗掉
	c.tickSunAt(t, eventAt.Add(-2*time.Hour))
	c.tickSunAt(t, eventAt.Add(time.Minute))
	return eventAt
}

func (c *e2eChain) tickSunAt(t *testing.T, at time.Time) {
	t.Helper()
	c.eval.EvaluateClockRulesAt(at)
}

func countCommands(t *testing.T, c *e2eChain) int64 {
	t.Helper()
	var n int64
	if err := c.db.Model(&models.CommandExecution{}).Count(&n).Error; err != nil {
		t.Fatalf("count commands: %v", err)
	}
	return n
}

// decodeOuterMsgType 解出 3.0 外框的消息类型字节。
//
// 3.0 帧 = magic(0xEB) + msgType + ...；downlink.Bridge 负责加外框，因此这里只需
// 校验第二字节。若外框格式变化，此断言会失败 —— 那正是我们要知道的。
// captureSender 实现 downlink 的 sessionSender（真实 3.0 外框的投递端）。
type captureSender struct {
	mu     sync.Mutex
	frames [][]byte
}

func (s *captureSender) SendToNode(_ string, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(frame))
	copy(cp, frame)
	s.frames = append(s.frames, cp)
	return nil
}

func (s *captureSender) HasSession(string) bool { return true }

func (s *captureSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *captureSender) last() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return nil
	}
	return s.frames[len(s.frames)-1]
}

func firstByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

var _ = binary.BigEndian
var _ = drivers.NewRegistry
var _ = deviceaction.NewRegistry
var _ = commandexec.NewService
var _ = automation.NewPlanner
var _ = context.Background
