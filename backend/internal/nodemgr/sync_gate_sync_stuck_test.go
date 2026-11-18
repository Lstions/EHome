package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// ============================================================================
// 回归：设备自报 sync_state 长期非 idle 时被 hash 相等"静默卡死"（2026-11-18 现场）
//
// 现场：C6 节点 F0F5BD02DD84（fw 2.8.0）连续 11 小时每条 StatusReport 都是
//   sync_state=1(syncing)、epoch=0；12 小时内只有 1 次 Hello（后端重启后的重连）。
//   同一时期服务端日志里"Hello skip: reason=hash_match"，没有 sent / rejected。
//
// 死锁链条：
//   ① 设备：request_sync → 置 SYNCING → 发 Hello；等一条能让状态回 IDLE 的下行帧。
//   ② 服务端：Hello 的 manifest 与服务端一致 ⇒ SyncActionNone（skip）。
//   ③ 于是设备永远停在 SYNCING，服务端永远认为"同步没问题" ⇒ 双向互等。
//
// 修复：用**设备自述**的卡死状态打破对称（与 2026-10-05 config_failed 同族），
// 强制重下发一份 manifest，让设备走完事务回到 IDLE。
//
// 它凭什么会失败（变异自证）：删掉 OnStatusReport 里的 reconcileStuckSync 分支 ⇒
// 第一条用例退回 hash_match/none，正是现场卡死态。
// ============================================================================

// stuckFixture 造一个 hash 与服务端一致、状态健康的节点，并注入可控时钟。
func stuckFixture(t *testing.T, nodeID string) (*SyncGate, string, *time.Time) {
	t.Helper()
	mgr, gate, _ := newTestManagerAndGate(t)
	node := models.Node{NodeID: nodeID, Status: "online", ConfigStatus: "applied", ConfigSyncState: "in_sync"}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatalf("fixture: create node: %v", err)
	}
	server := mgr.CalcConfigHashForDevice(node.NodeID)
	if server.ManifestID == "" {
		t.Fatal("fixture: 服务端 manifest_id 为空，测不到 hash_match 路径")
	}
	clock := time.Now()
	gate.now = func() time.Time { return clock }
	return gate, server.ManifestID, &clock
}

// TestOnStatusReport_StuckSyncForcesRepushDespiteHashMatch 是本次缺陷的核心用例。
func TestOnStatusReport_StuckSyncForcesRepushDespiteHashMatch(t *testing.T) {
	gate, manifestID, clock := stuckFixture(t, "NODE-STUCK")
	rpt := &StatusReportMsg{ConfigHash: manifestID, ChannelCount: 1, SyncState: "syncing"}

	// 宽限期内：设备可能只是正在同步，服务端不得插手（否则每次心跳都推全量）。
	if d := gate.OnStatusReport("NODE-STUCK", rpt); d.Action != SyncActionNone || d.Reason != "hash_match" {
		t.Fatalf("宽限期内不应重下发, 实得 action=%v reason=%s", d.Action, d.Reason)
	}

	// 超过宽限期仍是 syncing：hash 相等不能证明同步事务走完了。
	*clock = clock.Add(stuckSyncGrace + time.Second)
	d := gate.OnStatusReport("NODE-STUCK", rpt)
	if d.Action != SyncActionFull || d.Reason != "hash_match_but_sync_stuck" {
		// 否则设备永远等不到回 IDLE 的帧，双方互等。
		t.Fatalf("设备自报 syncing 超过 %s 且 hash 相等时必须强制重下发 manifest；实得 action=%v reason=%s",
			stuckSyncGrace, d.Action, d.Reason)
	}
}

// TestOnStatusReport_StuckRepushIsThrottled 钉住"自愈不会打爆节点"。
//
// 卡死是稳态：设备每秒一条 StatusReport，若每次都推全量 manifest，一台卡死节点会
// 被灌成总线过载 —— 把"配置卡死"放大成"设备失联"。
func TestOnStatusReport_StuckRepushIsThrottled(t *testing.T) {
	gate, manifestID, clock := stuckFixture(t, "NODE-STUCK-THROTTLE")
	rpt := &StatusReportMsg{ConfigHash: manifestID, ChannelCount: 1, SyncState: "syncing"}
	// 第一次上报只建立"开始卡死"的时刻（宽限期内静默），之后才谈得上越过宽限期。
	gate.OnStatusReport("NODE-STUCK-THROTTLE", rpt)
	*clock = clock.Add(stuckSyncGrace + time.Second)
	if d := gate.OnStatusReport("NODE-STUCK-THROTTLE", rpt); d.Action != SyncActionFull {
		t.Fatalf("首次越过宽限期应重下发, 实得 action=%v reason=%s", d.Action, d.Reason)
	}
	*clock = clock.Add(stuckSyncRepushInterval - time.Second)
	if d := gate.OnStatusReport("NODE-STUCK-THROTTLE", rpt); d.Action == SyncActionFull {
		t.Fatalf("节流窗口内不得再次全量下发（节点每 1s 一条 StatusReport），实得 reason=%s", d.Reason)
	}
	*clock = clock.Add(2 * time.Second)
	d := gate.OnStatusReport("NODE-STUCK-THROTTLE", rpt)
	if d.Action != SyncActionFull || d.Reason != "hash_match_but_sync_stuck" {
		t.Fatalf("越过节流窗口应恢复重下发, 实得 action=%v reason=%s", d.Action, d.Reason)
	}
}

// TestStuckSyncReasonNames 钉住 reason 必须**如实描述判据**。
//
// 审查指出：早期实现无条件写 hash_match_but_sync_stuck，而代码根本没比较 hash ——
// 日志/metric 会给出假的"hash 相等"结论，排障时把人带到错误的假设上。
func TestStuckSyncReasonNames(t *testing.T) {
	t.Run("hash 不等时不得谎称 hash_match", func(t *testing.T) {
		gate, _, clock := stuckFixture(t, "NODE-STUCK-MISMATCH")
		rpt := &StatusReportMsg{ConfigHash: "v2-deadbeef", ChannelCount: 1, SyncState: "syncing"}
		gate.OnStatusReport("NODE-STUCK-MISMATCH", rpt)
		*clock = clock.Add(stuckSyncGrace + time.Second)
		d := gate.OnStatusReport("NODE-STUCK-MISMATCH", rpt)
		if d.Reason != "sync_stuck_hash_mismatch" {
			t.Fatalf("reason=%q, hash 不等时必须用 sync_stuck_hash_mismatch", d.Reason)
		}
	})

	t.Run("sync_state=error 与 syncing 分列", func(t *testing.T) {
		gate, manifestID, clock := stuckFixture(t, "NODE-STUCK-ERRSTATE")
		rpt := &StatusReportMsg{ConfigHash: manifestID, ChannelCount: 1, SyncState: "error"}
		gate.OnStatusReport("NODE-STUCK-ERRSTATE", rpt)
		*clock = clock.Add(stuckSyncGrace + time.Second)
		d := gate.OnStatusReport("NODE-STUCK-ERRSTATE", rpt)
		if d.Reason != "sync_error_repush" {
			t.Fatalf("reason=%q, error 状态必须与 syncing 分列（前者是请求失败）", d.Reason)
		}
	})
}

// TestOnStatusReport_IdleResetsStuckTimer：设备回到 idle 必须清掉卡死观测，
// 否则下次偶发一次 syncing 会**立刻**触发重下发（宽限期形同虚设）。
func TestOnStatusReport_IdleResetsStuckTimer(t *testing.T) {
	gate, manifestID, clock := stuckFixture(t, "NODE-STUCK-RESET")
	syncing := &StatusReportMsg{ConfigHash: manifestID, ChannelCount: 1, SyncState: "syncing"}
	idle := &StatusReportMsg{ConfigHash: manifestID, ChannelCount: 1, SyncState: "idle"}

	gate.OnStatusReport("NODE-STUCK-RESET", syncing) // 建立开始卡死的时刻
	*clock = clock.Add(stuckSyncGrace + time.Second)
	if d := gate.OnStatusReport("NODE-STUCK-RESET", syncing); d.Action != SyncActionFull {
		t.Fatalf("前置：应触发重下发, 实得 %v/%s", d.Action, d.Reason)
	}
	*clock = clock.Add(stuckSyncRepushInterval)
	if d := gate.OnStatusReport("NODE-STUCK-RESET", idle); d.Action != SyncActionNone || d.Reason != "hash_match" {
		t.Fatalf("idle 且 hash 相等必须保持静默, 实得 action=%v reason=%s", d.Action, d.Reason)
	}
	// 刚恢复又抖动一次：计时必须从零开始，不能沿用之前累计的卡死时长。
	*clock = clock.Add(time.Second)
	if d := gate.OnStatusReport("NODE-STUCK-RESET", syncing); d.Action == SyncActionFull {
		t.Fatalf("idle 之后重新累计：宽限期内不得重下发, 实得 reason=%s", d.Reason)
	}
}
