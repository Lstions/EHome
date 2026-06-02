package ota

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	logger.Init("warn")
}

func setupOTATestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.AutoMigrate(&models.OTATask{}, &models.Firmware{})
	return db
}

func TestNewManager(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestCreateTaskNoFirmware(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	_, err := mgr.CreateTask(1, 999)
	if err == nil {
		t.Error("expected error for non-existent firmware")
	}
}

func TestCreateTaskWithFirmware(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	// Create firmware first
	fw := models.Firmware{
		Version:   "1.0.0",
		URL:       "http://example.com/fw.bin",
		SizeBytes: 1024,
		Checksum:  "abc123",
	}
	db.Create(&fw)

	task, err := mgr.CreateTask(1, fw.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.CollectorID != 1 {
		t.Errorf("expected collector_id=1, got %d", task.CollectorID)
	}
	if task.FirmwareID != fw.ID {
		t.Errorf("expected firmware_id=%d, got %d", fw.ID, task.FirmwareID)
	}
}

func TestHandleOtaProgressNoDB(t *testing.T) {
	// HandleOtaProgress with nil DB panics — needs valid DB
	// Test that it doesn't crash with valid DB and invalid payload
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)
	mgr.HandleOtaProgress("test_device", []byte{0x01, 0x02, 0x03})
}
