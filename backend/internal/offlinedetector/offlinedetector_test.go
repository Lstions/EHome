package offlinedetector

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	logger.Init("warn") // suppress logs during tests
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{})
	return db
}

func TestNewDetector(t *testing.T) {
	d := NewDetector(nil, nil)
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
}

func TestDetectorStartStop(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{})
	d := NewDetector(db, nil)
	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()
}

func TestMarkOfflineNoDB(t *testing.T) {
	// markOffline with nil DB panics — that's expected since Detector requires DB
	// Test only with valid DB below
}

func TestMarkOfflineWithDB(t *testing.T) {
	db := setupTestDB(t)

	wsHub := websocket.NewHub()
	go wsHub.Run()

	d := NewDetector(db, wsHub)

	col := models.Node{
		NodeID: "1001",
		Status: "online",
	}
	db.Create(&col)

	d.markOffline(db, "1001", "redis_ttl_expired")

	var updated models.Node
	db.Where("node_id = ?", "1001").First(&updated)
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}
}

// 走生产判据（checkDBLastSeen → isNodeOffline → NodeOfflineThreshold），
// 不再在测试里复刻阈值字面量：阈值一变，本用例跟着变，不会被"测试与生产不同步"掩盖。
func TestCheckDBLastSeenTimeout(t *testing.T) {
	db := setupTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()
	d := NewDetector(db, wsHub)

	// 超出阈值 1s，且与阈值有明确区分度。
	oldTime := time.Now().Add(-(NodeOfflineThreshold + time.Second))
	col := models.Node{
		NodeID:   "1002",
		Status:   "online",
		LastSeen: &oldTime,
	}
	db.Create(&col)

	d.checkDBLastSeen(db.Session(&gorm.Session{}))

	var updated models.Node
	db.Where("node_id = ?", "1002").First(&updated)
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}

	var event models.NodeEvent
	if err := db.Where("node_id = ? AND event_type = ?", "1002", "offline").First(&event).Error; err != nil {
		t.Errorf("expected an offline NodeEvent to be written: %v", err)
	}
}

// 分辨力在线的另一半：阈值内的 last_seen 必须保持 online。
// 注意旧实现用的是 -10s，在新阈值 3s 下这属于过期，本用例现在能真正区分两侧。
func TestCheckDBLastSeenRecent(t *testing.T) {
	db := setupTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()
	d := NewDetector(db, wsHub)

	recentTime := time.Now().Add(-NodeOfflineThreshold / 2)
	col := models.Node{
		NodeID:   "1003",
		Status:   "online",
		LastSeen: &recentTime,
	}
	db.Create(&col)

	d.checkDBLastSeen(db.Session(&gorm.Session{}))

	var updated models.Node
	db.Where("node_id = ?", "1003").First(&updated)
	if updated.Status != "online" {
		t.Errorf("expected online, got %s", updated.Status)
	}

	var count int64
	db.Model(&models.NodeEvent{}).Where("node_id = ? AND event_type = ?", "1003", "offline").Count(&count)
	if count != 0 {
		t.Errorf("expected no offline NodeEvent for an in-threshold node, got %d", count)
	}
}

// 边界语义明确为「严格大于」：now-last_seen 恰好等于 NodeOfflineThreshold 时仍算在线，
// 必须再多 1ns 才判离线。用固定 now 直接测判据，避免 time.Now() 抖动干扰边界。
func TestIsNodeOffline_ThresholdBoundary(t *testing.T) {
	now := time.Now()
	atThreshold := now.Add(-NodeOfflineThreshold)
	if isNodeOffline(now, &atThreshold) {
		t.Errorf("exactly at NodeOfflineThreshold (%s) must stay online: 判据是 > 而非 >=", NodeOfflineThreshold)
	}

	justOver := now.Add(-NodeOfflineThreshold - time.Nanosecond)
	if !isNodeOffline(now, &justOver) {
		t.Errorf("1ns beyond NodeOfflineThreshold (%s) must be offline", NodeOfflineThreshold)
	}

	if isNodeOffline(now, nil) {
		t.Error("nil last_seen must be skipped (unchanged semantics)")
	}
}

// 离线可见时延预算 = 固件上报周期 + 节点阈值 + 检测 ticker，SLA <=5s。
// 任何一个常数被单独放大都会在这里变红，防止指标被静默破坏。
func TestOfflineLatencyBudget(t *testing.T) {
	if OfflineLatencyBudget > 5*time.Second {
		t.Errorf("offline visibility budget %s exceeds the 5s SLA (firmware %s + threshold %s + ticker %s)",
			OfflineLatencyBudget, FirmwareStatusReportPeriod, NodeOfflineThreshold, OfflineCheckInterval)
	}
	if OfflineLatencyBudget != 5*time.Second {
		t.Errorf("expected the budget to be exactly 5s, got %s", OfflineLatencyBudget)
	}
	if OfflineCheckInterval != 1*time.Second {
		t.Errorf("expected 1s check interval, got %s", OfflineCheckInterval)
	}
	if NodeOfflineThreshold != 3*time.Second {
		t.Errorf("expected 3s node threshold, got %s", NodeOfflineThreshold)
	}
	if EdgeDeviceOfflineThreshold <= 5*time.Second {
		t.Errorf("edge-device threshold %s is too close to the 5s BMS polling period — "+
			"normal polling gaps would be misread as offline", EdgeDeviceOfflineThreshold)
	}
}
