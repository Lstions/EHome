package automation

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
)

// 本文件钉住**接线**本身（2026-11-18 审查 P1）：
//
// 自愈能力 100% 来自"自动 / 手动 / 确认三条路径在 Create（与铸造令牌）之前调用
// ensureNodeCapabilityFresh"。helper 单测再全，删掉这三行接线也不会红 —— 而一旦
// 被删，生产事故（capability_stale ⇒ 日落动作整日失败）会原样复发。
//
// 变异自证：删掉任意一条 `p.ensureNodeCapabilityFresh(...)` 调用 ⇒ 对应用例红
//（刷新器调用次数为 0）。

// staleNode 把夹具节点的能力快照推到过期，返回陈旧时刻。
func staleNode(t *testing.T, p *Planner, nodeID string) {
	t.Helper()
	stale := time.Now().UTC().Add(-2 * commandexec.MaxCapabilityAge)
	if err := p.db.Model(&models.Node{}).Where("node_id = ?", nodeID).
		Update("resource_reported_at", stale).Error; err != nil {
		t.Fatal(err)
	}
}

// selfHealRule 造一条 channel_cmd_v2 低风险动作规则（low_read 是夹具里唯一能过
// 命令引擎门禁、又不需要确认的动作）。
func selfHealRule(t *testing.T, p *Planner, edgeID uint, name string, requireConfirmed bool) models.AutomationRule {
	t.Helper()
	rule := models.AutomationRule{
		Name:                name,
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edgeID,
		TriggerSensorName:   "humidity",
		TriggerComparator:   "gt",
		TriggerThreshold:    80,
		CooldownSec:         0,
		RequireConfirmed:    requireConfirmed,
		ActionType:          models.AutomationActionDeviceAction,
		ActionDeviceID:      edgeID,
		ActionID:            "low_read",
		ActionParamsJSON:    `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return rule
}

// TestAutoPathRefreshesStaleCapabilityBeforeCreate 覆盖**自动路径**接线。
func TestAutoPathRefreshesStaleCapabilityBeforeCreate(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	staleNode(t, p, edge.NodeID)
	refresher := &fakeRefresher{db: p.db}
	p.SetCapabilityRefresher(refresher)
	p.SetCapabilityRefreshWait(2*time.Second, 5*time.Millisecond)

	rule := selfHealRule(t, p, edge.ID, "自愈-自动", false)
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	if len(refresher.calls) != 1 {
		t.Fatalf("自动路径必须且只能刷新一次资源快照, calls=%v —— 接线被删即复现生产事故", refresher.calls)
	}
	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("刷新快照后应执行成功, result=%s detail=%q（failed_gate 说明刷新没生效）", ev.Result, ev.Detail)
	}
}

// TestManualPathRefreshesStaleCapabilityBeforeCreate 覆盖**手动触发路径**接线。
func TestManualPathRefreshesStaleCapabilityBeforeCreate(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	staleNode(t, p, edge.NodeID)
	refresher := &fakeRefresher{db: p.db}
	p.SetCapabilityRefresher(refresher)
	p.SetCapabilityRefreshWait(2*time.Second, 5*time.Millisecond)

	rule := selfHealRule(t, p, edge.ID, "自愈-手动", false)
	ev, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("手动触发报错: %v", err)
	}
	if len(refresher.calls) != 1 {
		t.Fatalf("手动路径必须且只能刷新一次, calls=%v", refresher.calls)
	}
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("刷新后应执行成功, result=%s detail=%q", ev.Result, ev.Detail)
	}
}

// TestConfirmPathRefreshesStaleCapabilityBeforeMint 覆盖**确认制路径**接线。
//
// 这条路径的断言只能停在"刷新发生过"：channel_cmd_v2 的 medium+ 动作在当前
// CurrentEngineAllows 下过不了命令引擎门禁（那是既有的"未来高风险引擎"闸），
// 所以最终仍会 failed_gate。但**刷新在铸造之前**这一点是可观测的，也正是本用例的靶点：
// 不刷新则 IssueConfirmation 的内联能力检查会先拒，事件里就看不到任何刷新。
func TestConfirmPathRefreshesStaleCapabilityBeforeMint(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	staleNode(t, p, edge.NodeID)
	refresher := &fakeRefresher{db: p.db}
	p.SetCapabilityRefresher(refresher)
	p.SetCapabilityRefreshWait(2*time.Second, 5*time.Millisecond)

	rule := selfHealRule(t, p, edge.ID, "自愈-确认", true)
	rule.ActionID = "confirm_reset"
	if err := p.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("action_id", "confirm_reset").Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	var pending models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultPendingConfirm).
		First(&pending).Error; err != nil {
		t.Fatalf("前置: 应落一条 pending_confirm: %v", err)
	}
	// 预期最终仍因命令引擎门禁失败（见函数注释），这里只关心"刷新发生在铸造之前"。
	if _, err := p.ConfirmEvent(context.Background(), pending.ID, 7, "127.0.0.1"); err != nil {
		t.Logf("确认路径按预期拒绝: %v", err)
	}
	if len(refresher.calls) != 1 {
		t.Fatalf("确认路径必须在铸造令牌前刷新一次, calls=%v", refresher.calls)
	}
}

// TestEnsureNodeCapabilityFreshSkipsPeriphAction 钉住审查 P1-1：
//
// periph_cmd 动作的 action_device_id 是 **nodes 主键**，不是 edge_devices.id；
// 且 periph 门禁集不含 current_capabilities。不跳过就会按 edge_devices 查到一个
// 同号的**无关**设备，把 QueryResources 发给错误节点、还可能白等满超时。
func TestEnsureNodeCapabilityFreshSkipsPeriphAction(t *testing.T) {
	tp, refresher, _ := setupCapabilityFixture(t, commandexec.MaxCapabilityAge+time.Minute)
	// 把夹具的 edge 行 id 恰好当作"节点 id"传进来（真实 periph 就是这种形状）。
	var edge models.EdgeDevice
	if err := tp.db.First(&edge).Error; err != nil {
		t.Fatal(err)
	}
	tp.ensureNodeCapabilityFresh(context.Background(), "gpio_set", edge.ID)
	if len(refresher.calls) != 0 {
		t.Fatalf("periph 动作不得触发能力快照刷新, calls=%v", refresher.calls)
	}
}

// TestEnsureNodeCapabilityFreshSkipsOfflineNode 钉住离线早退。
//
// 离线节点不会回应 QueryResources：不等早退的话，每条动作都要白等满 2s 超时
//（下发无活会话时 downlink 会立即报错，但"连着却不回帧"会吃满超时）。
func TestEnsureNodeCapabilityFreshSkipsOfflineNode(t *testing.T) {
	p, refresher, edgeID := setupCapabilityFixture(t, commandexec.MaxCapabilityAge+time.Minute)
	if err := p.db.Model(&models.Node{}).Where("node_id = ?", "node-caps").
		Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	p.ensureNodeCapabilityFresh(context.Background(), "set_mos_policy", edgeID)
	if len(refresher.calls) != 0 {
		t.Fatalf("离线节点不得发 QueryResources（会白等满超时）, calls=%v", refresher.calls)
	}
}
