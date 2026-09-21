package ota

import (
	"errors"
	"path/filepath"
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
		mgr.HandleOtaProgress("1", payload)

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

// Test CancelTask: in-flight (pending) → failed with reason cancelErrorMsg
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
	if updated.ErrorMsg != cancelErrorMsg {
		t.Errorf("expected %q, got %q", cancelErrorMsg, updated.ErrorMsg)
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

// ==================== CancelTask: 取消窗口 + 条件更新 ====================

// newCancelTestTask 建库 + 建 collector/firmware，并返回一个已落库的 pending 任务。
func newCancelTestTask(t *testing.T, nodeID string) (*gorm.DB, *Manager, *models.OTATask) {
	t.Helper()
	db := setupOTATestDB(t)
	db.Create(&models.Node{NodeID: nodeID, Model: "ESP32S3", FirmwareVersion: "1.0.0", Status: "online"})
	fw := models.Firmware{Version: "1.1.0", URL: "u1", SizeBytes: 1024, Checksum: "abc"}
	db.Create(&fw)
	mgr := NewManager(db, nil, nil)
	task, err := mgr.CreateTask(nodeID, fw.ID)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return db, mgr, task
}

// reloadTask 从 DB 重新读取任务行。
func reloadTask(t *testing.T, db *gorm.DB, id uint) models.OTATask {
	t.Helper()
	var got models.OTATask
	if err := db.First(&got, id).Error; err != nil {
		t.Fatalf("reload task %d: %v", id, err)
	}
	return got
}

// TestCancelTask_RejectedDuringInstalling: installing 阶段拒绝取消，且 DB 行不被改写。
func TestCancelTask_RejectedDuringInstalling(t *testing.T) {
	db, mgr, task := newCancelTestTask(t, "3001")

	if err := db.Model(&models.OTATask{}).Where("id = ?", task.ID).
		Update("status", StatusInstalling).Error; err != nil {
		t.Fatalf("set installing: %v", err)
	}

	err := mgr.CancelTask(task.ID)
	if err == nil {
		t.Fatal("expected error cancelling an installing task, got nil")
	}
	if !errors.Is(err, ErrTaskNotCancellable) {
		t.Errorf("expected errors.Is(err, ErrTaskNotCancellable), got %v", err)
	}

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusInstalling {
		t.Errorf("DB row must stay %q, got %q", StatusInstalling, got.Status)
	}
	if got.ErrorMsg != "" {
		t.Errorf("error_msg must not be written, got %q", got.ErrorMsg)
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at must not be written, got %v", got.CompletedAt)
	}
}

// TestCancelTask_RejectedDuringVerifying: verifying 阶段拒绝取消，且 DB 行不被改写。
func TestCancelTask_RejectedDuringVerifying(t *testing.T) {
	db, mgr, task := newCancelTestTask(t, "3002")

	if err := db.Model(&models.OTATask{}).Where("id = ?", task.ID).
		Update("status", StatusVerifying).Error; err != nil {
		t.Fatalf("set verifying: %v", err)
	}

	err := mgr.CancelTask(task.ID)
	if err == nil {
		t.Fatal("expected error cancelling a verifying task, got nil")
	}
	if !errors.Is(err, ErrTaskNotCancellable) {
		t.Errorf("expected errors.Is(err, ErrTaskNotCancellable), got %v", err)
	}

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusVerifying {
		t.Errorf("DB row must stay %q, got %q", StatusVerifying, got.Status)
	}
	if got.ErrorMsg != "" {
		t.Errorf("error_msg must not be written, got %q", got.ErrorMsg)
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at must not be written, got %v", got.CompletedAt)
	}
}

// TestCancelTask_DownloadingAllowed: downloading 阶段仍可取消。
func TestCancelTask_DownloadingAllowed(t *testing.T) {
	db, mgr, task := newCancelTestTask(t, "3003")

	if err := db.Model(&models.OTATask{}).Where("id = ?", task.ID).
		Update("status", StatusDownloading).Error; err != nil {
		t.Fatalf("set downloading: %v", err)
	}

	if err := mgr.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask during downloading must succeed, got %v", err)
	}

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected %q, got %q", StatusFailed, got.Status)
	}
	if got.ErrorMsg != cancelErrorMsg {
		t.Errorf("expected error_msg %q, got %q", cancelErrorMsg, got.ErrorMsg)
	}
	if got.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// TestCancelTask_ConditionalUpdateRejectsStaleState 证明实现使用的是条件更新
// (WHERE id=? AND status=<读到的状态>) 而不是 "先读后写整行 Save"。
//
// 造法 (真实交错, 不是人为注入错误): 先把行置为 pending; 再注册一个只在
// First() 之后触发一次的 query callback, 在 CancelTask 读完 pending **之后、发出 UPDATE
// 之前** 用一个绕过当前事务的新会话把该行改成 installing (模拟并发写入者: 设备推进到安装阶段)。
// 于是 CancelTask 手里是 pending, DB 里是 installing。
//
// 判别力:
//
//	· 正确实现 (条件更新): UPDATE ... WHERE id=? AND status='pending' → RowsAffected==0
//	  → 返回 ErrTaskNotCancellable, 且 DB 行保持 installing。
//	· 错误实现 (db.Save(&task) 整行写回): 会把 installing 覆盖成 failed
//	  → 本用例在 "DB 行仍是 installing" 处变红。
func TestCancelTask_ConditionalUpdateRejectsStaleState(t *testing.T) {
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "stale.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.OTATask{}, &models.Firmware{}, &models.Node{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	db.Create(&models.Node{NodeID: "3004", Status: "online"})
	fw := models.Firmware{Version: "1.1.0", URL: "u", SizeBytes: 1, Checksum: "a"}
	db.Create(&fw)
	mgr := NewManager(db, nil, nil)
	task, err := mgr.CreateTask("3004", fw.ID)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Status != StatusPending {
		t.Fatalf("precondition: expected pending, got %s", task.Status)
	}

	// 交错点: CancelTask 内部的 db.First 读完之后, 把行改成 installing。
	// 只触发一次, 且只对 ota_tasks 生效 (CreateTask 的 supersede UPDATE 走 update callback,
	// 不经过 query callback, 因此不会被误触发)。
	flipped := false
	cbName := "test:flip_to_installing_after_read"
	_ = db.Callback().Query().After("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if flipped || tx.Statement.Table != "ota_tasks" {
			return
		}
		flipped = true
		// 独立会话: 出现在 CancelTask 读出 task 之后、条件 UPDATE 之前
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&models.OTATask{}).
			Where("id = ?", task.ID).Update("status", StatusInstalling).Error; err != nil {
			t.Errorf("interleave: flip to installing: %v", err)
		}
	})
	defer func() { _ = db.Callback().Query().Remove(cbName) }()

	err = mgr.CancelTask(task.ID)
	if !flipped {
		t.Fatal("interleave hook did not fire: CancelTask never read ota_tasks (test is not testing anything)")
	}
	if err == nil {
		t.Fatal("expected ErrTaskNotCancellable for stale state, got nil")
	}
	if !errors.Is(err, ErrTaskNotCancellable) {
		t.Errorf("expected errors.Is(err, ErrTaskNotCancellable), got %v", err)
	}

	got := reloadTask(t, db, task.ID)
	if got.Status != StatusInstalling {
		t.Errorf("conditional update must not overwrite concurrently-changed row: want %q, got %q",
			StatusInstalling, got.Status)
	}
	if got.ErrorMsg != "" {
		t.Errorf("error_msg must not be written, got %q", got.ErrorMsg)
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at must not be written, got %v", got.CompletedAt)
	}
}

// TestCancelTask_ClearsPendingAckChannel: 取消必须清理 ack 等待通道, 否则等待协程
// 会一直阻塞到 ackTimeout×重试 (最长 90s) 才退出 —— 每次取消泄漏一个长生命周期协程。
func TestCancelTask_ClearsPendingAckChannel(t *testing.T) {
	_, mgr, task := newCancelTestTask(t, "3005")

	// 模拟 SendOtaCommand 已注册的等待通道
	ackCh := make(chan struct{})
	_bridge.pendingMu.Lock()
	_bridge.pendingCmds[task.OtaID] = ackCh
	_bridge.pendingMu.Unlock()

	if err := mgr.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	_bridge.pendingMu.Lock()
	_, still := _bridge.pendingCmds[task.OtaID]
	_bridge.pendingMu.Unlock()
	if still {
		t.Fatal("pendingCmds entry must be deleted on cancel")
	}

	// 通道必须已被 close (否则等待协程仍会阻塞)
	select {
	case <-ackCh:
	default:
		t.Fatal("ack channel must be closed on cancel so the waiter goroutine exits immediately")
	}
}
