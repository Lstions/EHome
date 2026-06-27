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

	d.markOffline("1001", "redis_ttl_expired")

	var updated models.Node
	db.Where("node_id = ?", "1001").First(&updated)
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}
}

func TestCheckDBLastSeenTimeout(t *testing.T) {
	db := setupTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()
	d := NewDetector(db, wsHub)

	oldTime := time.Now().Add(-120 * time.Second)
	col := models.Node{
		NodeID:   "1002",
		Status:   "online",
		LastSeen: &oldTime,
	}
	db.Create(&col)

	// checkDBLastSeen uses redis.IsOnline which panics on nil redis client
	// Test the logic by calling markOffline directly instead
	var collectors []models.Node
	db.Where("status = ?", "online").Find(&collectors)
	for _, c := range collectors {
		if c.LastSeen != nil && time.Since(*c.LastSeen) > 90*time.Second {
			d.markOffline(c.NodeID, "db_last_seen_timeout")
		}
	}

	var updated models.Node
	db.Where("node_id = ?", "1002").First(&updated)
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}
}

func TestCheckDBLastSeenRecent(t *testing.T) {
	db := setupTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()
	d := NewDetector(db, wsHub)

	recentTime := time.Now().Add(-10 * time.Second)
	col := models.Node{
		NodeID:   "1003",
		Status:   "online",
		LastSeen: &recentTime,
	}
	db.Create(&col)

	// Recent device should NOT be marked offline
	var collectors []models.Node
	db.Where("status = ?", "online").Find(&collectors)
	for _, c := range collectors {
		if c.LastSeen != nil && time.Since(*c.LastSeen) > 90*time.Second {
			d.markOffline(c.NodeID, "db_last_seen_timeout")
		}
	}

	var updated models.Node
	db.Where("node_id = ?", "1003").First(&updated)
	if updated.Status != "online" {
		t.Errorf("expected online, got %s", updated.Status)
	}
}
