package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// ============================================================================
// 全量 manifest 推送去重窗口（2026-10-03 审查发现的 P1）
//
// 背景：SyncActionDefer 的常量注释写着 "Defer — within dedup window"，
// 但全仓**没有任何代码产生它** —— 去重窗口从未实现。此前无人在意，因为
// hash_mismatch 分支每次 StatusReport 都推送，而心跳周期是 5s。
// 2026-10-03 把心跳收到 1s 后，一个持续失配的节点（配置一直应用失败）
// 会让全量 manifest 推送频率 ×5，带宽与节点 CPU 随之上升。
//
// 本用例钉住三件事：
//  1. 窗口内的重复心跳被降级为 SyncActionDefer（不再重复推送）；
//  2. 窗口外恢复推送（不能永久卡住，否则节点永远拿不到配置）；
//  3. 显式配置变更（OnConfigChange）**不受**窗口限制（用户改完必须立即下发）。
//
// 它凭什么会失败：删掉 OnStatusReport 里的 allowPeriodicFullPush 判断，
// 第 1 条立刻红（连续两次心跳都返回 SyncActionFull）。
// ============================================================================
func TestSyncGate_PeriodicFullPushIsDeduped(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)
	// 必须存在节点记录，否则 CalcConfigHashForDevice 返回空 hash，
	// decide() 会在 no_server_config 分支提前返回，测不到 hash_mismatch 路径。
	mgr.db.Create(&models.Node{NodeID: "NODE-DEDUP", Status: "online"})
	mgr.db.Create(&models.Node{NodeID: "NODE-OTHER", Status: "online"})
	base := time.Now()
	gate.now = func() time.Time { return base }

	// 第一次心跳：hash 不匹配 ⇒ 应当推送。
	first := gate.OnStatusReport("NODE-DEDUP", &StatusReportMsg{ConfigHash: "stale-hash", ChannelCount: 1})
	if first.Action != SyncActionFull {
		t.Fatalf("首次 hash_mismatch 应推送全量 manifest，实得 %v (%s)", first.Action, first.Reason)
	}

	// 窗口内（+1s，正是新的心跳周期）：必须降级为 Defer，不得重复推送。
	gate.now = func() time.Time { return base.Add(1 * time.Second) }
	second := gate.OnStatusReport("NODE-DEDUP", &StatusReportMsg{ConfigHash: "stale-hash", ChannelCount: 1})
	if second.Action != SyncActionDefer {
		t.Fatalf("窗口内的重复心跳应降级为 Defer，实得 %v (%s) —— 心跳 5s→1s 后这会让 manifest 推送频率 ×5",
			second.Action, second.Reason)
	}

	// 窗口外（+6s > 5s）：必须恢复推送，否则节点永远拿不到配置。
	gate.now = func() time.Time { return base.Add(6 * time.Second) }
	third := gate.OnStatusReport("NODE-DEDUP", &StatusReportMsg{ConfigHash: "stale-hash", ChannelCount: 1})
	if third.Action != SyncActionFull {
		t.Fatalf("窗口外的失配心跳必须恢复推送（否则节点永久拿不到配置），实得 %v (%s)",
			third.Action, third.Reason)
	}

	// 不同设备互不影响（去重必须按设备维度，不能全局）。
	other := gate.OnStatusReport("NODE-OTHER", &StatusReportMsg{ConfigHash: "stale-hash", ChannelCount: 1})
	if other.Action != SyncActionFull {
		t.Fatalf("另一台设备的首次失配不应被 NODE-DEDUP 的窗口挡住，实得 %v", other.Action)
	}
}

// TestSyncGate_ExplicitConfigChangeBypassesDedup 是上一条的**反向对照**：
// 去重窗口只能作用于周期心跳。若它把显式配置变更也挡掉，用户改完配置要等
// 冷却期才下发 —— 那是比"推送频繁"严重得多的问题。
func TestSyncGate_ExplicitConfigChangeBypassesDedup(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)
	mgr.db.Create(&models.Node{NodeID: "NODE-CFG", Status: "online"})
	base := time.Now()
	gate.now = func() time.Time { return base }

	// 先用一次心跳占满窗口。
	if d := gate.OnStatusReport("NODE-CFG", &StatusReportMsg{ConfigHash: "stale", ChannelCount: 1}); d.Action != SyncActionFull {
		t.Fatalf("fixture: 首次心跳应推送，实得 %v", d.Action)
	}
	// 窗口内立刻发起显式配置变更：必须仍然推送。
	decisions := gate.OnConfigChange(ConfigChangeEvent{NodeID: "NODE-CFG", Type: "channel", Action: "update"})
	if len(decisions) != 1 || decisions[0].Action != SyncActionFull {
		t.Fatalf("显式配置变更必须立即推送、不受去重窗口限制，实得 %+v", decisions)
	}
}
