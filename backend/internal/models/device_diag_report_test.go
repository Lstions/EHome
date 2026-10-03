package models

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func openDiagModelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&DeviceDiagReport{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestDeviceDiagReportTableName(t *testing.T) {
	if got := (DeviceDiagReport{}).TableName(); got != "device_diag_reports" {
		t.Fatalf("TableName() = %q, want device_diag_reports", got)
	}
}

// TestDeviceDiagReportUniqueIndexRejectsDuplicate proves the (device_id,
// record_id) UNIQUE index exists: a plain second insert must fail, which is
// what forces the handler to use an ON CONFLICT upsert for idempotent retries.
func TestDeviceDiagReportUniqueIndexRejectsDuplicate(t *testing.T) {
	db := openDiagModelDB(t)

	row := DeviceDiagReport{DeviceID: "d1", RecordID: 42, ReportType: 2, CreatedAt: time.Now().UTC()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	dup := DeviceDiagReport{DeviceID: "d1", RecordID: 42, ReportType: 2, CreatedAt: time.Now().UTC()}
	if err := db.Create(&dup).Error; err == nil {
		t.Fatal("duplicate (device_id, record_id) was accepted — UNIQUE index missing")
	}

	// A different device with the same record_id is a distinct row.
	if err := db.Create(&DeviceDiagReport{DeviceID: "d2", RecordID: 42, ReportType: 2, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("different device same record_id should insert: %v", err)
	}

	var count int64
	db.Model(&DeviceDiagReport{}).Count(&count)
	if count != 2 {
		t.Fatalf("row count = %d, want 2", count)
	}
}

// TestDeviceDiagReportUpsertIsIdempotent mirrors the handler's persistence
// clause: retrying the same record_id must leave exactly one row.
func TestDeviceDiagReportUpsertIsIdempotent(t *testing.T) {
	db := openDiagModelDB(t)

	base := DeviceDiagReport{
		DeviceID: "d1", RecordID: 7, ReportType: 2,
		ResetReasonStr: "PANIC", RebootReason: "esp_restart",
		Stack: []byte{1, 2, 3, 4}, CreatedAt: time.Now().UTC(),
	}
	upsert := func(r DeviceDiagReport) error {
		return db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "device_id"}, {Name: "record_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"reset_reason_str", "reboot_reason"}),
		}).Create(&r).Error
	}

	if err := upsert(base); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Retry with an updated reboot_reason — must update, not duplicate.
	retry := base
	retry.RebootReason = "esp_restart in ota"
	if err := upsert(retry); err != nil {
		t.Fatalf("retry upsert: %v", err)
	}

	var rows []DeviceDiagReport
	if err := db.Where("device_id = ? AND record_id = ?", "d1", 7).Find(&rows).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("upsert produced %d rows, want 1", len(rows))
	}
	if rows[0].RebootReason != "esp_restart in ota" {
		t.Fatalf("reboot_reason = %q, want updated value", rows[0].RebootReason)
	}
}
