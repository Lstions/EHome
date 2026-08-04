package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// TestEstimate_DegradesOnQueryFailure covers the §1.3 降级 contract: when
// row estimation cannot run (here: data tables missing), the estimate
// returns ok=false so the API endpoint omits row_estimate instead of
// failing or returning a wrong figure.
func TestEstimate_DegradesOnQueryFailure(t *testing.T) {
	// Bare SQLite without unified_data/device_data migrated.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, ok := EstimateRowCount(context.Background(), db, 1)
	if ok {
		t.Fatalf("expected degradation (ok=false) when data tables are missing, got rows=%d", rows)
	}
	if rows != 0 {
		t.Errorf("degraded estimate must be 0, got %d", rows)
	}
}

// TestEstimate_DegradesOnContextDeadline covers the T1.1 endpoint-level
// timeout backstop: an already-expired ctx must make the estimation fail
// fast and degrade (ok=false) instead of running queries.
func TestEstimate_DegradesOnContextDeadline(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// Seed real rows so the only reason for failure is the deadline.
	dev := &models.EdgeDevice{Name: "d", Type: "t", NodeID: "NODE001"}
	if err := db.FirstOrCreate(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "v", Value: 1}).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), -time.Millisecond) // already expired
	defer cancel()
	rows, ok := EstimateScopeRows(ctx, db, &Scope{InstanceIDs: []uint{dev.ID}})
	if ok {
		t.Fatalf("expected degradation (ok=false) on expired ctx, got rows=%d", rows)
	}
	if rows != 0 {
		t.Errorf("degraded estimate must be 0, got %d", rows)
	}
}
