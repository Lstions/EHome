package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// ============================================================================
// 回归：配置应用失败的节点被 hash 相等"永久卡死"（2026-10-05 现场缺陷）
//
// 现场：S3 节点 30EDA0A9A808（fw 2.8.0）
//   nodes.config_status = failed, config_sync_state = failed，持续 45+ 分钟；
//   服务端 30 分钟窗口内 **0 条 sent / 0 条 rejected**（分母=该设备相关日志）；
//   设备 uptime 单调增长（未重启），MQTT 在线。
//
// 死锁链条（两侧都有代码证据）：
//   ① 固件 app_callbacks.c:390-393 —— 配置事务失败时
//      config_mgr_discard_staged_manifest()，active 仍是**旧** manifest，
//      然后回 ConfigResult(success=false)。
//   ② 固件 handler_data.c:117 —— StatusReport 的 config_hash 取自
//      config_mgr_get_manifest_id()，即 active（旧）manifest_id。
//   ③ 服务端 sync_gate.go decide() —— 旧逻辑先比 hash：device_hash ==
//      server manifest_id ⇒ hash_match ⇒ SyncActionNone，**永不下发**。
//
// 于是设备在等一份新 manifest，服务端在等设备改变 hash，双方互等。
// 两侧"都没做错动作"，所以日志里既没有 sent 也没有 rejected —— 这正是现场
// 观测到的现象。
//
// 修复：SyncGate 用**服务端权威**的 failed 状态打破对称 —— failed 表示上一次
// 配置事务已被判失败，此时 hash 相等不能证明"应用成功"，必须重新下发。
//
// 它凭什么会失败（变异自证）：把 sync_gate.go 里 rpt.NodeConfigFailed 分支
// 删掉（或把 failed 判定挪到 decide() 之后），本用例第一条立即红：
// 决策变成 SyncActionNone/hash_match，正是现场的卡死态。
// ============================================================================

// TestOnStatusReport_FailedNodeIsNotPinnedByMatchingHash 是本缺陷的**核心**用例。
//
// 构造方式刻意还原现场：设备上报的 hash **等于**服务端 manifest_id
// （失败后 active 仍是旧配置，而旧配置正是服务端此前成功下发的那一份），
// 同时服务端行是 failed。旧代码在此返回 hash_match/none ⇒ 永久卡死。
func TestOnStatusReport_FailedNodeIsNotPinnedByMatchingHash(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	node := models.Node{
		NodeID:          "NODE-CFG-FAILED",
		Status:          "online",
		ConfigStatus:    "failed",
		ConfigSyncState: "failed",
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatalf("fixture: create node: %v", err)
	}

	// 设备上报的正是服务端当前 manifest_id —— 即"hash 相等"。
	server := mgr.CalcConfigHashForDevice(node.NodeID)
	if server.ManifestID == "" {
		t.Fatal("fixture: 服务端 manifest_id 为空，测不到 hash_match 路径")
	}

	base := time.Now()
	gate.now = func() time.Time { return base }

	d := gate.OnStatusReport(node.NodeID, &StatusReportMsg{
		ConfigHash:       server.ManifestID, // 关键：与失败前成功下发的一致
		ChannelCount:     1,
		SyncState:        "error",
		NodeConfigFailed: true,
	})
	if d.Action != SyncActionFull {
		t.Fatalf("服务端已判 config_status=failed 的节点，即使设备上报的 hash 与服务端相等，"+
			"也必须重新下发 manifest（否则设备 active 仍是旧配置、双方互等，现场 0 sent/0 rejected）；"+
			"实得 action=%v reason=%s —— 这正是 2026-10-05 S3 的卡死态", d.Action, d.Reason)
	}
	if d.Reason != "hash_match_but_config_failed" {
		t.Fatalf("重下发必须带可归因的 reason，实得 %q", d.Reason)
	}
}

// TestOnStatusReport_HealthyMatchingHashIsNotRepushed 是上一条的**反向对照**。
//
// 修复不能矫枉过正：hash 相等且服务端未判 failed 的节点必须保持静默，
// 否则每次 1s 心跳都会推一份全量 manifest（带宽 ×N，且掩盖真实失配）。
func TestOnStatusReport_HealthyMatchingHashIsNotRepushed(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	node := models.Node{
		NodeID:          "NODE-CFG-OK",
		Status:          "online",
		ConfigStatus:    "applied",
		ConfigSyncState: "in_sync",
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatalf("fixture: create node: %v", err)
	}
	server := mgr.CalcConfigHashForDevice(node.NodeID)
	if server.ManifestID == "" {
		t.Fatal("fixture: 服务端 manifest_id 为空")
	}
	base := time.Now()
	gate.now = func() time.Time { return base }

	d := gate.OnStatusReport(node.NodeID, &StatusReportMsg{
		ConfigHash:       server.ManifestID,
		ChannelCount:     1,
		SyncState:        "idle",
		NodeConfigFailed: false,
	})
	if d.Action != SyncActionNone || d.Reason != "hash_match" {
		t.Fatalf("健康且 hash 相等的节点不得被重复下发，实得 action=%v reason=%s", d.Action, d.Reason)
	}
}

// TestOnStatusReport_FailedNodeRepushIsRateLimited 钉住"修复不会打爆节点"。
//
// failed 节点走的是周期路径，必须与"持续失配节点"共用 5s 去重窗口：
// 窗口内降级为 Defer，窗口外恢复 Full。否则一台应用失败的节点会被
// 1s 心跳以 ×N 频率灌全量 manifest —— 那会把"配置失败"放大成"总线过载"。
func TestOnStatusReport_FailedNodeRepushIsRateLimited(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	node := models.Node{
		NodeID:          "NODE-CFG-FAILED-RATE",
		Status:          "online",
		ConfigStatus:    "failed",
		ConfigSyncState: "failed",
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatalf("fixture: create node: %v", err)
	}
	base := time.Now()
	gate.now = func() time.Time { return base }
	rpt := func() *StatusReportMsg {
		return &StatusReportMsg{ConfigHash: "any", ChannelCount: 1, SyncState: "error", NodeConfigFailed: true}
	}

	if d := gate.OnStatusReport(node.NodeID, rpt()); d.Action != SyncActionFull {
		t.Fatalf("首次应下发，实得 %v (%s)", d.Action, d.Reason)
	}
	gate.now = func() time.Time { return base.Add(1 * time.Second) }
	if d := gate.OnStatusReport(node.NodeID, rpt()); d.Action != SyncActionDefer {
		t.Fatalf("窗口内的 failed 重下发必须降级为 Defer（否则心跳 1s 会 ×N 灌全量），实得 %v (%s)",
			d.Action, d.Reason)
	}
	gate.now = func() time.Time { return base.Add(6 * time.Second) }
	if d := gate.OnStatusReport(node.NodeID, rpt()); d.Action != SyncActionFull {
		t.Fatalf("窗口外必须恢复下发，否则 failed 节点永久拿不到配置，实得 %v (%s)", d.Action, d.Reason)
	}
}
