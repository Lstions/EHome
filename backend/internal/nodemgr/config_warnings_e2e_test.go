package nodemgr

import (
	"encoding/json"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
)

// configResultFrameWithWarnings 与 configResultFrame 同构，额外编 field 5
// （repeated varint = 降级通道 id）。
//
// ⚠ 刻意**另写**而不改 configResultFrame：后者被"失败恢复"一族用例复用，
// 给它加参数会污染那些用例的意图。
func configResultFrameWithWarnings(manifestID, syncID string, degraded []uint64) []byte {
	enc := frame.NewEncoder(frame.MsgConfigRslt)
	enc.EncodeString(1, manifestID)
	enc.EncodeVarint(2, 1) // success
	enc.EncodeString(4, syncID)
	for _, ch := range degraded {
		enc.EncodeVarint(5, ch)
	}
	return enc.Bytes()
}

// ⚠⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）—— **端到端**测试。
//
// 为什么必须有这个测试（而不是只测 buildConfigWarnings）：
//
//	我先写了一个只调 buildConfigWarnings 的单元测试，然后做变异自证
//	—— 把 handler_config.go 里写入 config_warnings 的那一行改成 "[]"，
//	**测试依然全绿**。因为那个测试只验证了"函数算得对"，
//	完全没覆盖"算出来的东西有没有被写进 DB"。
//	⇒ 这正是本仓记录的"假绿"形态：测了零件，没测接线。
//	本用例走真实的 handleConfigResult + 真实 DB，能抓住那一行被改坏。
func TestConfigResult_PersistsDMAWarnings(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	const nodeID = "node-warn-1"
	const syncID = "sync-warn-1"
	node := models.Node{
		NodeID:          nodeID,
		Name:            "node",
		Status:          "online",
		ConfigVersion:   "v2-warn",
		ConfigStatus:    "pending",
		ConfigSyncState: "syncing",
		LastSyncID:      syncID,
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	// 设备报"通道 49 降级了"。
	mgr.handleConfigResult(nodeID, configResultFrameWithWarnings("v2-warn", syncID, []uint64{49}))

	var stored models.Node
	if err := mgr.db.Where("node_id = ?", nodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}

	// ① 配置本身必须仍然算**成功**（降级不是失败）。
	if stored.ConfigStatus != "applied" || stored.ConfigSyncState != "in_sync" {
		t.Fatalf("降级不应让配置变成失败：status=%q sync=%q",
			stored.ConfigStatus, stored.ConfigSyncState)
	}

	// ② 告警必须真的落到 DB（这是变异自证抓不到的那一环）。
	if strings.TrimSpace(stored.ConfigWarnings) == "" {
		t.Fatal("config_warnings 为空 —— 设备上报了降级通道，但没有持久化。" +
			"这正是只测 buildConfigWarnings 时漏掉的那一环")
	}
	var warnings []map[string]any
	if err := json.Unmarshal([]byte(stored.ConfigWarnings), &warnings); err != nil {
		t.Fatalf("config_warnings 不是合法 JSON: %v (%q)", err, stored.ConfigWarnings)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %d (%q)", len(warnings), stored.ConfigWarnings)
	}
	if warnings[0]["code"] != "dma_degraded" || warnings[0]["channel_id"] != float64(49) {
		t.Fatalf("告警内容不对: %v", warnings[0])
	}
	msg, _ := warnings[0]["message"].(string)
	if !strings.Contains(msg, "DMA") || !strings.Contains(msg, "功能正常") {
		t.Fatalf("message 应可读且说明功能正常，实际：%s", msg)
	}
}

// TestConfigResult_ClearsStaleWarnings 是上例的**反向对照**：
// 一次"无降级"的成功回执必须把上一次的告警清掉，否则提示会永久粘住
// （用户会一直看到一条早已不成立的告警）。
func TestConfigResult_ClearsStaleWarnings(t *testing.T) {
	mgr, _ := newStatusTestManager(t)

	const nodeID = "node-warn-2"
	node := models.Node{
		NodeID:          nodeID,
		Name:            "node",
		Status:          "online",
		ConfigVersion:   "v2-w1",
		ConfigStatus:    "pending",
		ConfigSyncState: "syncing",
		LastSyncID:      "sync-w1",
	}
	if err := mgr.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	// 第一次：有降级。
	mgr.handleConfigResult(nodeID, configResultFrameWithWarnings("v2-w1", "sync-w1", []uint64{49}))
	var afterFirst models.Node
	if err := mgr.db.Where("node_id = ?", nodeID).First(&afterFirst).Error; err != nil {
		t.Fatal(err)
	}
	if afterFirst.ConfigWarnings == "" || afterFirst.ConfigWarnings == "[]" {
		t.Fatalf("第一次就应写入告警，实得 %q", afterFirst.ConfigWarnings)
	}

	// 第二次：用户关掉了 DMA 冲突 ⇒ 设备不再降级 ⇒ 告警必须清掉。
	if err := mgr.db.Model(&models.Node{}).Where("node_id = ?", nodeID).
		Updates(map[string]any{"config_version": "v2-w2", "last_sync_id": "sync-w2",
			"config_sync_state": "syncing"}).Error; err != nil {
		t.Fatal(err)
	}
	mgr.handleConfigResult(nodeID, configResultFrameWithWarnings("v2-w2", "sync-w2", nil))

	var afterSecond models.Node
	if err := mgr.db.Where("node_id = ?", nodeID).First(&afterSecond).Error; err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(afterSecond.ConfigWarnings) != "[]" {
		t.Fatalf("无降级的成功回执应把告警清成 \"[]\"，实得 %q —— "+
			"否则用户会一直看到一条早已不成立的降级提示", afterSecond.ConfigWarnings)
	}
}
