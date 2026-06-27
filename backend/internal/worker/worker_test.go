package worker

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestNewPool(t *testing.T) {
	handler := func(j Job) {}
	pool := NewPool(3, handler)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
	if pool.workers != 3 {
		t.Errorf("expected 3 workers, got %d", pool.workers)
	}
}

func TestPoolStartStop(t *testing.T) {
	handler := func(j Job) {}
	pool := NewPool(2, handler)
	pool.Start()
	time.Sleep(50 * time.Millisecond)
	pool.Stop()
}

func TestPoolSubmitAndProcess(t *testing.T) {
	var processed int32
	handler := func(j Job) {
		atomic.AddInt32(&processed, 1)
	}

	pool := NewPool(2, handler)
	pool.Start()
	defer pool.Stop()

	for i := 0; i < 10; i++ {
		pool.Submit(Job{Type: "test", Payload: i})
	}

	// Wait for all jobs to be processed
	time.Sleep(200 * time.Millisecond)

	if atomic.LoadInt32(&processed) != 10 {
		t.Errorf("expected 10 processed, got %d", atomic.LoadInt32(&processed))
	}
}

func TestPoolJobPayload(t *testing.T) {
	receivedCh := make(chan Job, 1)
	handler := func(j Job) {
		receivedCh <- j
	}

	pool := NewPool(1, handler)
	pool.Start()
	defer pool.Stop()

	pool.Submit(Job{Type: "my_type", Payload: "my_data"})

	var received Job
	select {
	case received = <-receivedCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for job to be processed")
	}

	if received.Type != "my_type" {
		t.Errorf("expected type my_type, got %s", received.Type)
	}
	if received.Payload != "my_data" {
		t.Errorf("expected payload my_data, got %v", received.Payload)
	}
}

func TestPoolCancel(t *testing.T) {
	handler := func(j Job) {}
	pool := NewPool(1, handler)
	pool.Start()

	// Cancel the context
	pool.cancel()

	// Verify context is cancelled
	select {
	case <-pool.ctx.Done():
		// Expected
	case <-time.After(1 * time.Second):
		t.Error("context should be cancelled")
	}
}

func TestPoolQueueFull(t *testing.T) {
	// Create pool with tiny queue and slow handler
	slowHandler := func(j Job) {
		time.Sleep(1 * time.Second)
	}
	pool := NewPool(1, slowHandler)
	pool.Start()
	defer pool.Stop()

	// Fill the queue (capacity 100)
	submitted := 0
	for i := 0; i < 120; i++ {
		pool.Submit(Job{Type: "test", Payload: i})
		submitted++
	}
	// Some jobs should be dropped (queue full), but no panic
	_ = submitted
}
