package ota

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
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
	db.AutoMigrate(&models.OTATask{}, &models.Firmware{}, &models.Node{})
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

	_, err := mgr.CreateTask("1", 999)
	if err == nil {
		t.Error("expected error for non-existent firmware")
	}
}

func TestCreateTaskWithFirmware(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	fw := models.Firmware{
		Version:   "1.0.0",
		URL:       "http://example.com/fw.bin",
		SizeBytes: 1024,
		Checksum:  "abc123",
	}
	db.Create(&fw)

	task, err := mgr.CreateTask("1", fw.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.NodeID != "1" {
		t.Errorf("expected node_id=1, got %s", task.NodeID)
	}
	if task.FirmwareID != fw.ID {
		t.Errorf("expected firmware_id=%d, got %d", fw.ID, task.FirmwareID)
	}
	if task.ToVersion != fw.Version {
		t.Errorf("expected to_version=%s, got %s", fw.Version, task.ToVersion)
	}
	if task.Status != StatusPending {
		t.Errorf("expected status=%s, got %s", StatusPending, task.Status)
	}
}

func TestCreateTaskSupersedesPrior(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	fw1 := models.Firmware{Version: "1.0.0", URL: "u1", SizeBytes: 1, Checksum: "a"}
	fw2 := models.Firmware{Version: "2.0.0", URL: "u2", SizeBytes: 2, Checksum: "b"}
	db.Create(&fw1)
	db.Create(&fw2)

	// First task should be in pending
	t1, err := mgr.CreateTask("1", fw1.ID)
	if err != nil {
		t.Fatalf("first task: %v", err)
	}
	if t1.Status != StatusPending {
		t.Fatalf("first task expected pending, got %s", t1.Status)
	}

	// Manually push it to downloading to simulate mid-flight
	now := time.Now()
	t1.Status = StatusDownloading
	t1.StartedAt = &now
	db.Save(t1)

	// Second task should supersede the first
	t2, err := mgr.CreateTask("1", fw2.ID)
	if err != nil {
		t.Fatalf("second task: %v", err)
	}
	if t2.Status != StatusPending {
		t.Errorf("second task expected pending, got %s", t2.Status)
	}

	// Reload t1 and verify it was marked failed
	db.First(t1, t1.ID)
	if t1.Status != StatusFailed {
		t.Errorf("first task should be superseded to failed, got %s", t1.Status)
	}
	if t1.ErrorMsg != "Superseded by new attempt" {
		t.Errorf("unexpected error_msg: %s", t1.ErrorMsg)
	}
	if t1.CompletedAt == nil {
		t.Error("superseded task should have completed_at set")
	}
}

func TestHandleOtaProgressNoDB(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)
	mgr.HandleOtaProgress("test_device", []byte{0x01, 0x02, 0x03})
}

func TestHandleOtaProgressStateMapping(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	fw := models.Firmware{Version: "1.0.0", URL: "u", SizeBytes: 1, Checksum: "a"}
	db.Create(&fw)
	task, _ := mgr.CreateTask("1", fw.ID)
	if task == nil {
		t.Fatal("task is nil")
	}
	otaID := task.OtaID

	// Helper to encode a minimal OtaProgress frame:
	// Field 1 (string ota_id) = otaID
	// Field 2 (varint status)  = status
	// Field 3 (varint pct)     = progressPct
	encodeProgress := func(id string, status, pct uint64) []byte {
		enc := wireEncoderForTest()
		enc.EncodeString(1, id)
		enc.EncodeVarint(2, status)
		enc.EncodeVarint(3, pct)
		return enc.Bytes()
	}

	cases := []struct {
		wireStatus uint64
		wantStatus string
	}{
		{WireDownloading, StatusDownloading},
		{WireInstalling, StatusInstalling},
		{WireSuccess, StatusSuccess},
		{WireFailed, StatusFailed},
	}

	for _, c := range cases {
		// Reset task to a fresh state for each case
		db.Exec("DELETE FROM ota_tasks")
		task, _ := mgr.CreateTask("1", fw.ID)
		otaID = task.OtaID

		payload := encodeProgress(otaID, c.wireStatus, 50)
		mgr.HandleOtaProgress("dev1", payload)

		var got models.OTATask
		db.Where("ota_id = ?", otaID).First(&got)
		if got.Status != c.wantStatus {
			t.Errorf("wire %d: want %s, got %s", c.wireStatus, c.wantStatus, got.Status)
		}
		if c.wireStatus == WireSuccess || c.wireStatus == WireFailed {
			if got.CompletedAt == nil {
				t.Errorf("wire %d: completed_at should be set", c.wireStatus)
			}
		}
	}
}

func TestHandleHelloOTACompletionSuccess(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	fw := models.Firmware{Version: "2.0.0", URL: "u", SizeBytes: 1, Checksum: "a"}
	db.Create(&fw)
	task, _ := mgr.CreateTask("1", fw.ID)
	// Push to mid-flight
	now := time.Now()
	task.Status = StatusInstalling
	task.StartedAt = &now
	db.Save(task)

	// Hello reports target version → should mark success
	mgr.HandleHelloOTACompletion("1", "dev1", "2.0.0")

	var got models.OTATask
	db.Where("ota_id = ?", task.OtaID).First(&got)
	if got.Status != StatusSuccess {
		t.Errorf("expected success, got %s", got.Status)
	}
	if got.Progress != 100 {
		t.Errorf("expected progress 100, got %d", got.Progress)
	}
}

func TestHandleHelloOTACompletionTimeout(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	fw := models.Firmware{Version: "2.0.0", URL: "u", SizeBytes: 1, Checksum: "a"}
	db.Create(&fw)
	task, _ := mgr.CreateTask("1", fw.ID)
	// Started 20 min ago, still in flight
	oldStart := time.Now().Add(-20 * time.Minute)
	task.Status = StatusDownloading
	task.StartedAt = &oldStart
	db.Save(task)

	// Hello reports old firmware (mismatch) — HandleHelloOTACompletion only
	// marks success when versions match; it does NOT mark failed on mismatch.
	// Timeout/failure detection is handled by timeoutScanner and ack retries.
	// So the task should remain in downloading status.
	mgr.HandleHelloOTACompletion("1", "dev1", "1.0.0")

	var got models.OTATask
	db.Where("ota_id = ?", task.OtaID).First(&got)
	if got.Status != StatusDownloading {
		t.Errorf("expected downloading (Hello mismatch is no-op), got %s", got.Status)
	}
}

func TestHandleHelloOTACompletionNoOp(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)

	// No in-flight task — should be no-op
	mgr.HandleHelloOTACompletion("99", "dev1", "1.0.0")
	// No assertions, just shouldn't panic
}

// wireEncoderForTest is a tiny helper to encode wire frames for tests.
// Uses the same frame package that ota.go uses.
func wireEncoderForTest() *frame.Encoder {
	return frame.NewEncoder(0)
}

// Test CancelTask: in-flight → failed with reason "cancelled by user"
func TestCancelTask(t *testing.T) {
	db := setupOTATestDB(t)

	// Create collector and firmware
	col := models.Node{
		NodeID:          "2001",
		Model:           "ESP32S3",
		FirmwareVersion: "1.0.0",
		Status:          "online",
	}
	db.Create(&col)

	fw := models.Firmware{
		Version:   "1.1.0",
		URL:       "u1",
		SizeBytes: 1024,
		Checksum:  "abc",
	}
	db.Create(&fw)

	mgr := NewManager(db, nil, nil)
	task, err := mgr.CreateTask(col.NodeID, fw.ID)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Status != StatusPending {
		t.Errorf("expected pending, got %s", task.Status)
	}

	// Cancel the task
	if err := mgr.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	// Verify it's now failed
	var updated models.OTATask
	db.First(&updated, task.ID)
	if updated.Status != StatusFailed {
		t.Errorf("expected failed, got %s", updated.Status)
	}
	if updated.ErrorMsg != "cancelled by user" {
		t.Errorf("expected 'cancelled by user', got %s", updated.ErrorMsg)
	}
	if updated.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

// Test CancelTask: terminal state task → error
func TestCancelTask_AlreadyTerminal(t *testing.T) {
	db := setupOTATestDB(t)
	col := models.Node{NodeID: "2002", Status: "online"}
	db.Create(&col)
	fw := models.Firmware{Version: "1.0", URL: "u", SizeBytes: 1, Checksum: "a"}
	db.Create(&fw)

	mgr := NewManager(db, nil, nil)
	task, _ := mgr.CreateTask(col.NodeID, fw.ID)

	// Manually mark as success
	task.Status = StatusSuccess
	db.Save(&task)

	// Try to cancel
	err := mgr.CancelTask(task.ID)
	if err == nil {
		t.Error("expected error when cancelling terminal task")
	}
}

// Test CancelTask: nonexistent task → error
func TestCancelTask_NotFound(t *testing.T) {
	db := setupOTATestDB(t)
	mgr := NewManager(db, nil, nil)
	err := mgr.CancelTask(99999)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}
