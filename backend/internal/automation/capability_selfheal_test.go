package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// fakeRefresher 记录"向节点要一次资源快照"的调用，并把节点行的
// resource_reported_at 推到当前时刻（复刻真机 100ms 内回 ResourceReport 的实测行为）。
type fakeRefresher struct {
	db    *gorm.DB
	calls []string
	fail  bool
}

func (f *fakeRefresher) SendQueryResources(deviceID string) (string, error) {
	f.calls = append(f.calls, deviceID)
	if f.fail {
		return "", errors.New("downlink unavailable")
	}
	if err := f.db.Model(&models.Node{}).Where("node_id = ?", deviceID).
		Update("resource_reported_at", time.Now().UTC()).Error; err != nil {
		return "", err
	}
	return "res-test", nil
}

// setupCapabilityFixture 造 node + channel + edge（channel_cmd_v2 路径所需的最小链）。
func setupCapabilityFixture(t *testing.T, age time.Duration) (*Planner, *fakeRefresher, uint) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC().Add(-age)
	node := models.Node{NodeID: "node-caps", Name: "caps", Status: "online", ResourceReportedAt: &reported,
		BootID: "boot-caps", CommandEngineRevision: 1,
		CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "bms", NodeID: node.NodeID, ChannelID: ch.ID,
		Type: "jiabaida_bms", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	p := NewPlanner(db, nil, nil, 0)
	f := &fakeRefresher{db: db}
	p.SetCapabilityRefresher(f)
	p.SetCapabilityRefreshWait(2*time.Second, 5*time.Millisecond)
	return p, f, edge.ID
}

// TestEnsureNodeCapabilityFresh_StaleRefreshes：过期 ⇒ 发一次查询并等到落库。
//
// 变异自证：删掉 ensureNodeCapabilityFresh 里的 SendQueryResources 调用 → 本用例变红
// （刷新器调用次数为 0，节点时间不变）。
func TestEnsureNodeCapabilityFresh_StaleRefreshes(t *testing.T) {
	p, refresher, edgeID := setupCapabilityFixture(t, commandexec.MaxCapabilityAge+time.Minute)
	var before models.Node
	if err := p.db.First(&before, "node_id = ?", "node-caps").Error; err != nil {
		t.Fatal(err)
	}
	p.ensureNodeCapabilityFresh(context.Background(), "set_mos_policy", edgeID)
	if len(refresher.calls) != 1 || refresher.calls[0] != "node-caps" {
		t.Fatalf("过期快照必须向节点要一次资源上报, got %v", refresher.calls)
	}
	var after models.Node
	if err := p.db.First(&after, "node_id = ?", "node-caps").Error; err != nil {
		t.Fatal(err)
	}
	if after.ResourceReportedAt == nil || !after.ResourceReportedAt.After(*before.ResourceReportedAt) {
		t.Fatalf("刷新后 resource_reported_at 未前进: before=%v after=%v",
			before.ResourceReportedAt, after.ResourceReportedAt)
	}
}

// TestEnsureNodeCapabilityFresh_FreshIsUntouched：快照新鲜 ⇒ 不打扰节点。
func TestEnsureNodeCapabilityFresh_FreshIsUntouched(t *testing.T) {
	p, refresher, edgeID := setupCapabilityFixture(t, time.Minute)
	p.ensureNodeCapabilityFresh(context.Background(), "set_mos_policy", edgeID)
	if len(refresher.calls) != 0 {
		t.Fatalf("新鲜快照不得再发 QueryResources, got %v", refresher.calls)
	}
}

// TestEnsureNodeCapabilityFresh_NilRefresherIsNoop：未接线时保持改造前行为（不崩、不刷）。
func TestEnsureNodeCapabilityFresh_NilRefresherIsNoop(t *testing.T) {
	p, _, edgeID := setupCapabilityFixture(t, commandexec.MaxCapabilityAge+time.Minute)
	p.SetCapabilityRefresher(nil)
	p.ensureNodeCapabilityFresh(context.Background(), "set_mos_policy", edgeID) // 不得 panic
}

// TestManualTriggerMintsConfirmationToken 钉死 2026-11-18 的手动触发修复：
//
// 生产事故：规则 action 是 risk=elevated/high 的 set_mos_policy，手动点「触发」
// 恒落 confirmation_required（旧实现只传 ActorUserID，不带确认令牌，也不带
// system actor/规则级授权）。修法 = 与 ConfirmEvent 同族：**即铸即销**一次性令牌。
//
// 变异自证：把 executeManualDeviceAction 里的 IssueConfirmation 块删掉 → 本用例
// 退回 confirmation_required 而变红。
func TestManualTriggerMintsConfirmationToken(t *testing.T) {
	p, nodeID := setupPeriphConfirmPlanner(t)
	rule := models.AutomationRule{
		Name: "手动触发高风险动作", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: nodeID, TriggerSensorName: "illuminance",
		TriggerComparator: "gt", TriggerThreshold: 500,
		ActionType: models.AutomationActionDeviceAction, ActionDeviceID: nodeID,
		ActionID: "gpio_set", ActionParamsJSON: `{"pin":5,"level":1}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("手动触发报错: %v", err)
	}
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("手动触发高风险动作 result=%s detail=%q —— 必须 executed（即铸即销确认令牌）",
			ev.Result, ev.Detail)
	}
	if ev.CommandID == "" {
		t.Fatal("executed 事件没有命令 ID")
	}
}
