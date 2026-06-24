package pendingwrite

import (
	"testing"
	"time"

	"ehome/backend/pkg/logger"
)

func TestMain(m *testing.M) {
	logger.Init("info")
	m.Run()
}

func TestNewManager(t *testing.T) {
	mgr := NewManager(nil)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if len(mgr.pending) != 0 {
		t.Fatalf("expected 0 pending entries, got %d", len(mgr.pending))
	}
}

func TestHandleResponseNoEntry(t *testing.T) {
	mgr := NewManager(nil)
	// Should not panic on unknown request_id
	mgr.HandleResponse(99999, true, 0, "")
}

func TestHandleDataReportAckNoEntry(t *testing.T) {
	mgr := NewManager(nil)
	// Should not panic on unknown request_id
	mgr.HandleDataReportAck(99999, []byte{0x01, 0x02})
}

func TestHandleResponseWithEntry(t *testing.T) {
	mgr := NewManager(nil)

	// Manually insert a pending entry
	entry := &Entry{
		RequestID: 12345,
		DeviceID:  "test_device",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	mgr.pending[12345] = entry

	// Handle response
	mgr.HandleResponse(12345, true, 0, "ok")

	// Check response was delivered
	select {
	case resp := <-entry.Response:
		if !resp.Success {
			t.Error("expected success=true")
		}
		if resp.ErrorMsg != "ok" {
			t.Errorf("expected error_msg=ok, got %s", resp.ErrorMsg)
		}
	default:
		t.Fatal("expected response on channel")
	}
}

func TestHandleDataReportAckWithEntry(t *testing.T) {
	mgr := NewManager(nil)

	entry := &Entry{
		RequestID: 54321,
		DeviceID:  "test_device",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	mgr.pending[54321] = entry

	mgr.HandleDataReportAck(54321, []byte{0x00, 0x5A})

	select {
	case resp := <-entry.Response:
		if !resp.Success {
			t.Error("DataReport ack should imply success")
		}
		if resp.ErrorCode != 0 {
			t.Errorf("expected error_code=0, got %d", resp.ErrorCode)
		}
	default:
		t.Fatal("expected response on channel")
	}
}

func TestHandleResponseFailure(t *testing.T) {
	mgr := NewManager(nil)

	entry := &Entry{
		RequestID: 11111,
		DeviceID:  "test_device",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	mgr.pending[11111] = entry

	mgr.HandleResponse(11111, false, 3, "device busy")

	select {
	case resp := <-entry.Response:
		if resp.Success {
			t.Error("expected success=false")
		}
		if resp.ErrorCode != 3 {
			t.Errorf("expected error_code=3, got %d", resp.ErrorCode)
		}
	default:
		t.Fatal("expected response on channel")
	}
}

func TestRemoveEntry(t *testing.T) {
	mgr := NewManager(nil)

	entry := &Entry{
		RequestID: 22222,
		DeviceID:  "test_device",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	mgr.pending[22222] = entry

	mgr.removeEntry(22222)

	if _, ok := mgr.pending[22222]; ok {
		t.Error("entry should be removed")
	}
}

func TestRetryFailedExpired(t *testing.T) {
	mgr := NewManager(nil)

	entry := &Entry{
		RequestID:  33333,
		DeviceID:   "test_device",
		Response:   make(chan *Response, 1),
		SentAt:     time.Now().Add(-10 * time.Second), // 10s ago
		RetryCount: 0,
	}
	mgr.pending[33333] = entry

	mgr.RetryFailed(3)

	// Entry should still exist (retry count < max)
	if _, ok := mgr.pending[33333]; !ok {
		t.Error("entry should still exist after retry")
	}
}

func TestRetryFailedMaxRetries(t *testing.T) {
	mgr := NewManager(nil)

	entry := &Entry{
		RequestID:  44444,
		DeviceID:   "test_device",
		Response:   make(chan *Response, 1),
		SentAt:     time.Now().Add(-10 * time.Second),
		RetryCount: 3, // already at max
	}
	mgr.pending[44444] = entry

	mgr.RetryFailed(3)

	// Entry should be removed (retry count >= max)
	if _, ok := mgr.pending[44444]; ok {
		t.Error("entry should be removed after max retries")
	}
}

func TestResolveExactlyOnce(t *testing.T) {
	entry := &Entry{
		RequestID: 55555,
		DeviceID:  "test_device",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}

	// Multiple resolve calls — only the first should succeed
	entry.resolve(&Response{Success: true, ErrorMsg: "first"})
	entry.resolve(&Response{Success: false, ErrorMsg: "second"})
	entry.resolve(&Response{Success: false, ErrorMsg: "third"})

	// Should get exactly one response — the first one
	resp := <-entry.Response
	if !resp.Success {
		t.Error("expected success=true from first resolve")
	}
	if resp.ErrorMsg != "first" {
		t.Errorf("expected error_msg=first, got %s", resp.ErrorMsg)
	}

	// Channel should now be empty (only one write despite three resolve calls)
	select {
	case <-entry.Response:
		t.Error("should not have a second response on channel")
	default:
		// correct: channel is empty
	}
}

func TestShutdown(t *testing.T) {
	mgr := NewManager(nil)

	entry1 := &Entry{
		RequestID: 66666,
		DeviceID:  "test_device_1",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	entry2 := &Entry{
		RequestID: 77777,
		DeviceID:  "test_device_2",
		Response:  make(chan *Response, 1),
		SentAt:    time.Now(),
	}
	mgr.pending[66666] = entry1
	mgr.pending[77777] = entry2

	mgr.Shutdown(5 * time.Second)

	// All entries should be resolved with shutdown error
	for _, entry := range []*Entry{entry1, entry2} {
		select {
		case resp := <-entry.Response:
			if resp.Success {
				t.Error("expected success=false for shutdown response")
			}
			if resp.ErrorMsg != "server shutting down" {
				t.Errorf("expected error_msg='server shutting down', got %s", resp.ErrorMsg)
			}
		default:
			t.Fatal("expected response on channel after shutdown")
		}
	}

	// Pending map should be empty
	if len(mgr.pending) != 0 {
		t.Errorf("expected 0 pending entries after shutdown, got %d", len(mgr.pending))
	}
}
