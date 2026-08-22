package nodemgr

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPingTracker_Track(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	pt.Track("device-1", 12345, nil)
	if pt.PendingCount() != 1 {
		t.Errorf("pending count: got %d, want 1", pt.PendingCount())
	}
}

func TestPingTracker_Complete(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	pt.Track("device-1", 12345, nil)
	rec, ok := pt.Complete("device-1")
	if !ok {
		t.Fatal("expected to find record")
	}
	if rec.timestamp != 12345 {
		t.Errorf("timestamp: got %d, want 12345", rec.timestamp)
	}
	if pt.PendingCount() != 0 {
		t.Errorf("pending count after complete: got %d, want 0", pt.PendingCount())
	}
}

func TestPingTracker_CompleteNotFound(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	_, ok := pt.Complete("nonexistent")
	if ok {
		t.Error("expected not to find record for nonexistent device")
	}
}

func TestPingTracker_ShouldRetry(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	pt.Track("device-1", 12345, nil)
	attempt, shouldRetry := pt.ShouldRetry("device-1")
	if !shouldRetry {
		t.Error("expected first attempt to allow retry")
	}
	if attempt != 1 {
		t.Errorf("attempt: got %d, want 1", attempt)
	}

	// Increment to max
	pt.IncrementAttempt("device-1")
	pt.IncrementAttempt("device-1")
	_, shouldRetry = pt.ShouldRetry("device-1")
	if shouldRetry {
		t.Error("expected not to retry after max attempts (3)")
	}
}

func TestPingTracker_Timeout(t *testing.T) {
	pt := NewPingTracker()
	pt.timeout = 50 * time.Millisecond // short timeout for test
	pt.maxRetry = 1                    // only 1 retry for fast test
	pt.StartWithInterval(50 * time.Millisecond)
	defer pt.Stop()

	var callbackCalled int32
	var resultSuccess atomic.Bool
	pt.Track("device-1", 12345, func(latencyMs int64, success bool) {
		atomic.StoreInt32(&callbackCalled, 1)
		resultSuccess.Store(success)
	})

	// Wait for at least 2 timeout cycles
	time.Sleep(300 * time.Millisecond)

	if atomic.LoadInt32(&callbackCalled) == 0 {
		t.Error("expected callback to be called on timeout")
	}
	if resultSuccess.Load() {
		t.Error("expected success=false on timeout")
	}
}

func TestPingTracker_Config(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	if pt.maxRetry != 3 {
		t.Errorf("default maxRetry: got %d, want 3", pt.maxRetry)
	}
	if pt.Timeout() != 10*time.Second {
		t.Errorf("default timeout: got %v, want 10s", pt.Timeout())
	}
}

// TestPingTracker_Peek covers the anti-forgery helper added by
// Redis 退役步骤 1/4: Peek returns the pending record WITHOUT consuming it
// (Complete 仍是唯一消费点)。
func TestPingTracker_Peek(t *testing.T) {
	pt := NewPingTracker()
	defer pt.Stop()

	if _, ok := pt.Peek("missing"); ok {
		t.Error("Peek on unknown device must return ok=false")
	}

	pt.Track("device-peek", 777, nil)

	// Peek 不得消费记录。
	for i := 0; i < 3; i++ {
		rec, ok := pt.Peek("device-peek")
		if !ok {
			t.Fatalf("Peek #%d must find the tracked record", i)
		}
		if rec.timestamp != 777 {
			t.Errorf("Peek #%d timestamp = %d, want 777", i, rec.timestamp)
		}
	}
	if pt.PendingCount() != 1 {
		t.Errorf("pending after repeated Peek = %d, want 1 (Peek must not consume)", pt.PendingCount())
	}

	// Complete 消费后 Peek 必须 miss。
	if _, ok := pt.Complete("device-peek"); !ok {
		t.Fatal("Complete must find the record")
	}
	if _, ok := pt.Peek("device-peek"); ok {
		t.Error("Peek after Complete must miss (record consumed)")
	}
}
