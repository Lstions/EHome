package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// statusFrameWithHash 构造一条带 config_hash(field 6) 与 sync_id(field 8) 的
// StatusReport，用来驱动"服务端自愈"路径（handler_status.go 的 recoveryAttempted）。
func statusFrameWithHash(uptimeSec uint64, syncState uint64, configHash, syncID string) []byte {
	enc := frame.NewEncoder(frame.MsgStatusRpt)
	enc.EncodeVarint(1, uptimeSec)
	enc.EncodeString(2, "online")
	enc.EncodeVarint(3, 5)
	enc.EncodeVarint(5, syncState)
	if configHash != "" {
		enc.EncodeString(6, configHash)
	}
	if syncID != "" {
		enc.EncodeString(8, syncID)
	}
	return enc.Bytes()
}

// newStatusTestManager 建一个可用于 handleStatusReport 的最小 Manager。
// 用真实 NewManager（它负责装配 syncGate 等字段），再替换成记录型 MQTT，
// 这样既能断言落库、也能断言"是否真的下发了 manifest"。
func newStatusTestManager(t *testing.T) (*Manager, *senderMockDownlink) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	mock := &senderMockDownlink{}
	hub := websocket.NewHub()
	go hub.Run()
	mgr := NewManager(db, nil, hub, nil, nil, nil)
	mgr.downlink = mock
	return mgr, mock
}

// seedFailedNodeWithCurrentManifest 建一个"服务端已判 failed、且 config_version
// 就是服务端当前这一代"的节点，返回该 manifest_id。
//
// 必须用**服务端真实算出的** manifest_id 而不是字面量：否则 SyncGate 会走
// hash_mismatch 正常下发（那是正确行为），测不到自愈路径。现场也正是"设备持有
// 的 hash == 服务端 config_version"这一档。
func seedFailedNodeWithCurrentManifest(t *testing.T, mgr *Manager, nodeID string, syncID string) string {
	t.Helper()
	node := models.Node{
		NodeID:          nodeID,
		Name:            "node",
		Status:          "online",
		UptimeSeconds:   100,
		ConfigStatus:    "failed",
		ConfigSyncState: "failed",
		LastSyncID:      syncID,
		HardwareInfo:    `{"channels":[]}`,
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	server := mgr.CalcConfigHashForDevice(nodeID)
	if server.ManifestID == "" {
		t.Fatal("fixture: 服务端 manifest_id 为空")
	}
	if err := mgr.db.Model(&models.Node{}).Where("node_id = ?", nodeID).
		Update("config_version", server.ManifestID).Error; err != nil {
		t.Fatal(err)
	}
	return server.ManifestID
}

// ============================================================================
// 回归：failed 是"没有出口的终态"（2026-10-05 S3 现场）
//
// 现场：nodes.config_status=failed / config_sync_state=failed 持续 45+ 分钟；
// 45 分钟窗口 0 条 sent / 0 条 rejected；设备 uptime 单调、MQTT 在线。
//
// 关键事实（决定了归属）：config_status / config_sync_state 是**服务端自己写的列**，
// 设备协议里没有这两个字段（设备只上报 sync_state(varint) 与 config_hash）。
// 所以"设备自述 failed"不成立 —— failed 是服务端写下后**没人复位**的状态位。
//
// 三条"无出口"缺口，本文件与 sync_gate_config_failed_test.go 逐条钉住：
//   缺口 A（handler_status.go 自愈）: 只认 syncing，漏掉 failed；
//   缺口 B（handler_config.go 回执）: 要求 syncing，同代迟到成功回执被当 stale 丢弃；
//   缺口 C（sync_gate.go 决策）: hash 相等即 hash_match，不重下发。
//
// 它凭什么会失败（变异自证）：把任一处的 "failed" 分支去掉，对应用例立即红。
// ============================================================================

// TestStatusReport_SelfHealsFailedNode 钉住缺口 A。
//
// 设备报 sync_state=idle(0) + config_hash==config_version + sync_id==last_sync_id
// —— 这是"这一代配置确实在设备上生效了"的最强证据。旧实现因为要求
// config_sync_state=="syncing"，对这个 failed 行**什么都不做**，failed 永久粘住。
func TestStatusReport_SelfHealsFailedNode(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	const syncID = "sync-abc-123"
	manifestID := seedFailedNodeWithCurrentManifest(t, mgr, "node-selfheal", syncID)

	mgr.handleStatusReport("node-selfheal", statusFrameWithHash(120, 0 /*idle*/, manifestID, syncID))

	var stored models.Node
	if err := mgr.db.Where("node_id = ?", "node-selfheal").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ConfigSyncState != "in_sync" {
		t.Fatalf("设备已 idle 且持有当前这一代配置时，failed 必须自愈为 in_sync；"+
			"实得 config_sync_state=%q —— failed 成了没有出口的终态（现场 45+ 分钟）",
			stored.ConfigSyncState)
	}
	if stored.ConfigStatus != "applied" {
		t.Fatalf("config_status 必须与 config_sync_state 一起复位，否则留下 "+
			"failed+in_sync 的自相矛盾行、前端仍显示失败；实得 config_status=%q", stored.ConfigStatus)
	}
}

// TestStatusReport_DoesNotSelfHealWrongGeneration 是缺口 A 的**反向对照**。
//
// 自愈不能放宽判据：sync_id 对不上（即设备报的是**更早**那一代），
// 必须**不得**被标成 in_sync —— 否则会把"设备其实没拿到新配置"粉饰成已同步。
// （该行仍会因 NodeConfigFailed 触发一次重下发，落到 syncing；这正是期望行为。）
func TestStatusReport_DoesNotSelfHealWrongGeneration(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	manifestID := seedFailedNodeWithCurrentManifest(t, mgr, "node-stale-gen", "sync-current")

	// sync_id 属于更早一代 —— 不能自愈。
	mgr.handleStatusReport("node-stale-gen", statusFrameWithHash(120, 0, manifestID, "sync-OLDER"))

	var stored models.Node
	if err := mgr.db.Where("node_id = ?", "node-stale-gen").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ConfigSyncState == "in_sync" {
		t.Fatalf("世代不匹配（sync_id 属于更早一代）时**不得**标为 in_sync，"+
			"否则会把「设备没拿到新配置」粉饰成已同步；实得 config_sync_state=%q", stored.ConfigSyncState)
	}
}

// TestStatusReport_SelfHealPersistsBothFieldsAtomically 确认自愈是**落库**的、
// 且不会被随后的 SyncGate 重下发踩回 syncing（本会话已多次出现"写了但没生效"）。
func TestStatusReport_SelfHealPersistsBothFieldsAtomically(t *testing.T) {
	mgr, mock := newStatusTestManager(t)

	const syncID = "sync-persist"
	manifestID := seedFailedNodeWithCurrentManifest(t, mgr, "node-persist", syncID)

	mgr.handleStatusReport("node-persist", statusFrameWithHash(60, 0, manifestID, syncID))

	// 用一条全新查询读回，绕开任何内存态。
	var reread models.Node
	if err := mgr.db.Where("node_id = ?", "node-persist").First(&reread).Error; err != nil {
		t.Fatal(err)
	}
	if reread.ConfigStatus != "applied" || reread.ConfigSyncState != "in_sync" {
		t.Fatalf("自愈必须同时落库两个字段且不被重下发踩回，实得 status=%q sync=%q",
			reread.ConfigStatus, reread.ConfigSyncState)
	}
	// 自愈成功后不应再下发 manifest（hash 相等 ⇒ 服务端确实已同步）。
	if len(mock.records) != 0 {
		t.Fatalf("自愈成功后不得再下发 manifest（否则每次心跳都重下发），实得 %d 条发布", len(mock.records))
	}
}

// configResultFrame 构造 ConfigResult(0x05)：1=manifest_id, 2=success, 4=sync_id。
func configResultFrame(manifestID string, success bool, syncID string) []byte {
	enc := frame.NewEncoder(frame.MsgConfigRslt)
	enc.EncodeString(1, manifestID)
	var s uint64
	if success {
		s = 1
	}
	enc.EncodeVarint(2, s)
	enc.EncodeString(4, syncID)
	return enc.Bytes()
}

// TestConfigResult_RecoversFailedNode 钉住缺口 B（handler_config.go）。
//
// 场景：一次失败把服务端行写成 failed 之后，**同一代**（同 manifest_id + 同
// sync_id）的迟到成功回执到达。旧实现要求 config_sync_state=="syncing"，
// 于是这条回执被当成 stale 丢弃 —— 设备其实已经应用成功，服务端却拒绝相信，
// failed 永久粘住。
//
// 世代判据由 manifest_id + sync_id 承担（sync_id 是每次决策新生成的 UUID），
// 因此放宽状态判据不会误收更早世代的回执。
func TestConfigResult_RecoversFailedNode(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	const syncID = "sync-cr-1"
	node := models.Node{
		NodeID:          "node-cr-failed",
		Name:            "node",
		Status:          "online",
		ConfigVersion:   "v2-cr",
		ConfigStatus:    "failed",
		ConfigSyncState: "failed",
		LastSyncID:      syncID,
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	mgr.handleConfigResult("node-cr-failed", configResultFrame("v2-cr", true, syncID))

	var stored models.Node
	if err := mgr.db.Where("node_id = ?", "node-cr-failed").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ConfigStatus != "applied" || stored.ConfigSyncState != "in_sync" {
		t.Fatalf("同一代（同 manifest+sync_id）的成功回执必须能把 failed 复位为 applied/in_sync；"+
			"实得 status=%q sync=%q —— failed 成了没有出口的终态",
			stored.ConfigStatus, stored.ConfigSyncState)
	}
}

// TestConfigResult_RejectsOlderGeneration 是缺口 B 的**反向对照**：
// 更早世代的成功回执仍必须被丢弃，否则会把旧代的成功误认成新代已同步。
func TestConfigResult_RejectsOlderGeneration(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	node := models.Node{
		NodeID:          "node-cr-oldgen",
		Name:            "node",
		Status:          "online",
		ConfigVersion:   "v2-new",
		ConfigStatus:    "failed",
		ConfigSyncState: "failed",
		LastSyncID:      "sync-new",
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	// 上一代的回执：manifest 与 sync_id 都对不上。
	mgr.handleConfigResult("node-cr-oldgen", configResultFrame("v2-old", true, "sync-old"))

	var stored models.Node
	if err := mgr.db.Where("node_id = ?", "node-cr-oldgen").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ConfigStatus != "failed" || stored.ConfigSyncState != "failed" {
		t.Fatalf("更早世代的回执必须被丢弃，不得复位当前 failed 行；实得 status=%q sync=%q",
			stored.ConfigStatus, stored.ConfigSyncState)
	}
}
