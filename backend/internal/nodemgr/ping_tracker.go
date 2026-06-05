package nodemgr

import (
	"sync"
	"time"
)

// PingTracker tracks in-flight pings and triggers retries
// F7.6 from docs/v2.0/acceptance-criteria
type PingTracker struct {
	mu       sync.RWMutex
	pending  map[string]*pingRecord
	maxRetry int
	timeout  time.Duration
	stop     chan struct{}
}

type pingRecord struct {
	deviceID  string
	timestamp int64
	attempt   int
	createdAt time.Time
	callback  func(latencyMs int64, success bool)
}

// NewPingTracker creates a new ping tracker
func NewPingTracker() *PingTracker {
	return &PingTracker{
		pending:  make(map[string]*pingRecord),
		maxRetry: 3,                // retry up to 3 times
		timeout:  10 * time.Second, // 10s timeout per attempt
		stop:     make(chan struct{}),
	}
}

// Start begins the timeout checker loop
func (pt *PingTracker) Start() {
	go pt.loopWithInterval(1 * time.Second)
}

// StartWithInterval starts with custom check interval (for tests)
func (pt *PingTracker) StartWithInterval(d time.Duration) {
	go pt.loopWithInterval(d)
}

// Stop stops the timeout checker
func (pt *PingTracker) Stop() {
	close(pt.stop)
}

// Track records a pending ping
func (pt *PingTracker) Track(deviceID string, timestamp int64, callback func(latencyMs int64, success bool)) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.pending[deviceID] = &pingRecord{
		deviceID:  deviceID,
		timestamp: timestamp,
		attempt:   1,
		createdAt: time.Now(),
		callback:  callback,
	}
}

// Complete marks a ping as completed (Pong received)
// Returns the record if it existed
func (pt *PingTracker) Complete(deviceID string) (*pingRecord, bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	rec, ok := pt.pending[deviceID]
	if ok {
		delete(pt.pending, deviceID)
	}
	return rec, ok
}

// ShouldRetry checks if a ping should be retried
// Returns the retry attempt number and whether to retry
func (pt *PingTracker) ShouldRetry(deviceID string) (int, bool) {
	pt.mu.RLock()
	rec, ok := pt.pending[deviceID]
	pt.mu.RUnlock()
	if !ok {
		return 0, false
	}
	if rec.attempt >= pt.maxRetry {
		return rec.attempt, false
	}
	return rec.attempt, true
}

// IncrementAttempt increments the attempt counter
func (pt *PingTracker) IncrementAttempt(deviceID string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if rec, ok := pt.pending[deviceID]; ok {
		rec.attempt++
		rec.createdAt = time.Now()
	}
}

// Timeout returns the configured timeout duration
func (pt *PingTracker) Timeout() time.Duration {
	return pt.timeout
}

// loop checks for timed-out pings
func (pt *PingTracker) loop() {
	pt.loopWithInterval(1 * time.Second)
}

func (pt *PingTracker) loopWithInterval(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-pt.stop:
			return
		case <-ticker.C:
			pt.checkTimeouts()
		}
	}
}

// checkTimeouts finds expired pings
func (pt *PingTracker) checkTimeouts() {
	now := time.Now()
	pt.mu.Lock()
	var expired []string
	for deviceID, rec := range pt.pending {
		if now.Sub(rec.createdAt) > pt.timeout {
			expired = append(expired, deviceID)
		}
	}
	pt.mu.Unlock()

	for _, deviceID := range expired {
		attempt, shouldRetry := pt.ShouldRetry(deviceID)
		if shouldRetry {
			// Mark for retry (caller checks and resends)
			pt.IncrementAttempt(deviceID)
		} else {
			// Max retries exhausted, mark as failed
			if rec, ok := pt.Complete(deviceID); ok {
				if rec.callback != nil {
					rec.callback(-1, false) // -1 indicates timeout
				}
			}
		}
		_ = attempt
	}
}

// PendingCount returns the number of pending pings (for tests/metrics)
func (pt *PingTracker) PendingCount() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return len(pt.pending)
}
