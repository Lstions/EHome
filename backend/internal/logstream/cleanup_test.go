package logstream

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newCleanupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE node_logs (
		id integer PRIMARY KEY AUTOINCREMENT,
		node_id varchar(64) NOT NULL,
		level smallint NOT NULL,
		ts bigint NOT NULL,
		tag varchar(64) NOT NULL,
		message text NOT NULL,
		created_at datetime NOT NULL,
		seq integer NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLogCleanup_DeletesOnlyExpiredCreatedAt(t *testing.T) {
	db := newCleanupTestDB(t)
	now := time.Now()
	logs := []models.NodeLog{
		{NodeID: "n", Level: 2, Ts: 999999, Tag: "OLD", Message: "expired", CreatedAt: now.Add(-3 * time.Hour)},
		// Ts intentionally looks old/low: cleanup must rely on CreatedAt, not uptime ts.
		{NodeID: "n", Level: 2, Ts: 1, Tag: "NEW", Message: "keep", CreatedAt: now.Add(-10 * time.Minute)},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	cleanup := NewLogCleanup(db, time.Hour, time.Hour)
	cleanup.cleanup()

	var remaining []models.NodeLog
	if err := db.Order("id").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Tag != "NEW" {
		t.Fatalf("remaining logs = %+v, want only NEW", remaining)
	}
}

func TestLogCleanup_StopBeforeStartIsSafe(t *testing.T) {
	cleanup := NewLogCleanup(nil, time.Hour, time.Hour)
	cleanup.Stop()
	cleanup.Stop()
}
