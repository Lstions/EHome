package datalifecycle

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
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

	rows, ok := EstimateRowCount(db, 1)
	if ok {
		t.Fatalf("expected degradation (ok=false) when data tables are missing, got rows=%d", rows)
	}
	if rows != 0 {
		t.Errorf("degraded estimate must be 0, got %d", rows)
	}
}
