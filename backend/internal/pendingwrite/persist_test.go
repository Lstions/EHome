package pendingwrite

import (
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	return db
}

func TestPersistAndRecover(t *testing.T) {
	db := openTestDB(t)

	// Create a manager, add an entry, then shut it down
	mgr1 := NewManager(nil, db)

	entry := &Entry{
		RequestID: 100,
		DeviceID:  "device_A",
		ChannelID: 1,
		Data:      []byte{0xAA, 0xBB},
		ReadSize:  4,
		SentAt:    time.Now(),
		Response:  make(chan *Response, 1),
	}
	mgr1.mu.Lock()
	mgr1.pending[100] = entry
	mgr1.mu.Unlock()

	// Persist it
	mgr1.persistEntry(entry, 100, 30*time.Second)

	// Verify it's in the DB
	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 persisted record, got %d", count)
	}

	// Simulate shutdown (persisted entries are NOT removed)
	mgr1.Shutdown(0)

	// Create a new manager — should recover the entry
	mgr2 := NewManager(nil, db)

	mgr2.mu.RLock()
	recovered, ok := mgr2.pending[100]
	mgr2.mu.RUnlock()

	if !ok {
		t.Fatal("expected entry 100 to be recovered")
	}
	if recovered.DeviceID != "device_A" {
		t.Errorf("expected device_id=device_A, got %s", recovered.DeviceID)
	}
	if recovered.ChannelID != 1 {
		t.Errorf("expected channel_id=1, got %d", recovered.ChannelID)
	}
	if recovered.ReadSize != 4 {
		t.Errorf("expected read_size=4, got %d", recovered.ReadSize)
	}
	if len(recovered.Data) != 2 || recovered.Data[0] != 0xAA || recovered.Data[1] != 0xBB {
		t.Errorf("expected data=[0xAA,0xBB], got %v", recovered.Data)
	}

	// nextRequestID should be > 100 to avoid collision
	if nextRequestID <= 100 {
		t.Errorf("expected nextRequestID > 100, got %d", nextRequestID)
	}
}

func TestRecoverExpiredEntries(t *testing.T) {
	db := openTestDB(t)

	// Auto-migrate first so we can insert directly
	db.AutoMigrate(&models.PendingWriteRecord{})

	// Insert an already-expired record directly
	db.Create(&models.PendingWriteRecord{
		RequestID: 200,
		DeviceID:  "device_expired",
		ChannelID: 2,
		Data:      []byte{0x01},
		SentAt:    time.Now().Add(-2 * time.Minute),
		TimeoutAt: time.Now().Add(-1 * time.Minute), // already timed out
	})

	// New manager should NOT recover expired entries
	mgr := NewManager(nil, db)

	mgr.mu.RLock()
	_, ok := mgr.pending[200]
	mgr.mu.RUnlock()

	if ok {
		t.Error("expired entry should not be recovered")
	}

	// The expired record should be cleaned from DB
	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 persisted records after cleanup, got %d", count)
	}
}

func TestRemovePersistedEntry(t *testing.T) {
	db := openTestDB(t)

	mgr := NewManager(nil, db)

	// Persist an entry
	entry := &Entry{
		RequestID: 300,
		DeviceID:  "device_B",
		ChannelID: 3,
		Data:      []byte{0xCC},
		SentAt:    time.Now(),
		Response:  make(chan *Response, 1),
	}
	mgr.persistEntry(entry, 300, 10*time.Second)

	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 record, got %d", count)
	}

	// Remove it
	mgr.removePersistedEntry(300)

	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 records after removal, got %d", count)
	}
}

func TestNilDBNoPanic(t *testing.T) {
	mgr := NewManager(nil, nil)

	// These should all be no-ops without panicking
	mgr.persistEntry(&Entry{Response: make(chan *Response, 1)}, 1, time.Second)
	mgr.removePersistedEntry(1)
	mgr.recoverPending()
}

func TestHandleResponseRemovesPersistedEntry(t *testing.T) {
	db := openTestDB(t)
	mgr := NewManager(nil, db)

	entry := &Entry{
		RequestID: 400,
		DeviceID:  "device_C",
		ChannelID: 4,
		SentAt:    time.Now(),
		Response:  make(chan *Response, 1),
	}
	mgr.mu.Lock()
	mgr.pending[400] = entry
	mgr.mu.Unlock()
	mgr.persistEntry(entry, 400, 10*time.Second)

	// Handle response — should remove persisted entry
	mgr.HandleResponse(400, true, 0, "ok")

	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 persisted records after HandleResponse, got %d", count)
	}
}

func TestHandleDataReportAckRemovesPersistedEntry(t *testing.T) {
	db := openTestDB(t)
	mgr := NewManager(nil, db)

	entry := &Entry{
		RequestID: 500,
		DeviceID:  "device_D",
		ChannelID: 5,
		ReadSize:  2,
		SentAt:    time.Now(),
		Response:  make(chan *Response, 1),
	}
	mgr.mu.Lock()
	mgr.pending[500] = entry
	mgr.mu.Unlock()
	mgr.persistEntry(entry, 500, 10*time.Second)

	// Handle DataReportAck — should remove persisted entry
	mgr.HandleDataReportAck(500, []byte{0x00, 0x5A})

	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 persisted records after HandleDataReportAck, got %d", count)
	}
}

func TestShutdownPresistsEntries(t *testing.T) {
	db := openTestDB(t)
	mgr := NewManager(nil, db)

	entry := &Entry{
		RequestID: 600,
		DeviceID:  "device_E",
		ChannelID: 6,
		SentAt:    time.Now(),
		Response:  make(chan *Response, 1),
	}
	mgr.mu.Lock()
	mgr.pending[600] = entry
	mgr.mu.Unlock()
	mgr.persistEntry(entry, 600, 30*time.Second)

	// Shutdown should NOT remove persisted entries
	mgr.Shutdown(0)

	var count int64
	db.Model(&models.PendingWriteRecord{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 persisted record after shutdown (for recovery), got %d", count)
	}
}
