package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// statusFrame 构造一条最小合法的 StatusReport（字段 1/2/5 为必填）。
func statusFrame(uptimeSec uint64) []byte {
	enc := frame.NewEncoder(frame.MsgStatusRpt)
	enc.EncodeVarint(1, uptimeSec)
	enc.EncodeString(2, "online")
	enc.EncodeVarint(3, 1)
	enc.EncodeVarint(5, 0) // sync_state=idle
	return enc.Bytes()
}

// 回归背景（2026-09-30 实机定位）：
// 设备重启（OTA/看门狗/掉电）到重新上报通常只要几秒，远小于离线检测阈值 90s，
// 因此不会产生 offline→online 跳变。旧实现只在跳变时写 last_online_time，
// 导致该字段跨重启累加：实测设备当天重启 5 次，字段仍显示 7 天，
// 而固件 uptime 已归零 —— 「在线时长」与「固件在线时长」自相矛盾。
//
// 修复：uptime 回退即视为重启，重置本次上线时间。
func TestStatusReportRebootResetsLastOnlineTime(t *testing.T) {
	db := testutil.OpenTestDB(t)
	longAgo := time.Now().Add(-7 * 24 * time.Hour)
	node := models.Node{
		NodeID:         "node-reboot",
		Name:           "node",
		Status:         "online",
		UptimeSeconds:  40000, // 已运行很久
		LastOnlineTime: &longAgo,
		HardwareInfo:   `{"channels":[]}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	before := time.Now()
	// 设备重启后第一条上报：uptime 归零（远小于旧的 40000）
	manager.handleStatusReport(node.NodeID, statusFrame(3))

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastOnlineTime == nil {
		t.Fatal("last_online_time 不应为 nil")
	}
	if stored.LastOnlineTime.Before(before.Add(-2 * time.Second)) {
		t.Fatalf("检测到 uptime 回退后应重置 last_online_time，实际仍为 %v（7 天前）",
			stored.LastOnlineTime)
	}
	if stored.UptimeSeconds != 3 {
		t.Fatalf("uptime_seconds 应更新为 3，实际 %d", stored.UptimeSeconds)
	}
}

// 守卫不得过度触发：uptime 正常递增（在线期间）**不得**重置 last_online_time。
// 否则「上线时间」会每秒被刷新成"现在"，用户永远看不到真实的本次上线时刻。
func TestStatusReportNormalUptimeKeepsLastOnlineTime(t *testing.T) {
	db := testutil.OpenTestDB(t)
	started := time.Now().Add(-3 * time.Hour)
	node := models.Node{
		NodeID:         "node-steady",
		Name:           "node",
		Status:         "online",
		UptimeSeconds:  10800,
		LastOnlineTime: &started,
		HardwareInfo:   `{"channels":[]}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	// uptime 正常前进（10800 -> 10805），不是重启
	manager.handleStatusReport(node.NodeID, statusFrame(10805))

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastOnlineTime == nil {
		t.Fatal("last_online_time 不应为 nil")
	}
	if !stored.LastOnlineTime.Truncate(time.Second).Equal(started.Truncate(time.Second)) {
		t.Fatalf("uptime 正常递增时不得重置 last_online_time：期望 %v，实际 %v",
			started, stored.LastOnlineTime)
	}
}

// offline→online 跳变仍须重置（原有行为不能因本次修复而回退）。
func TestStatusReportOfflineToOnlineStillResetsLastOnlineTime(t *testing.T) {
	db := testutil.OpenTestDB(t)
	longAgo := time.Now().Add(-48 * time.Hour)
	node := models.Node{
		NodeID:         "node-reconnect",
		Name:           "node",
		Status:         "offline",
		UptimeSeconds:  500,
		LastOnlineTime: &longAgo,
		HardwareInfo:   `{"channels":[]}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	before := time.Now()
	manager.handleStatusReport(node.NodeID, statusFrame(600))

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastOnlineTime == nil || stored.LastOnlineTime.Before(before.Add(-2*time.Second)) {
		t.Fatalf("offline→online 应重置 last_online_time，实际 %v", stored.LastOnlineTime)
	}
}
