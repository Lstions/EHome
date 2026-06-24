package pendingwrite

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// Entry represents a pending write operation
type Entry struct {
	RequestID  uint32
	DeviceID   string
	ChannelID  uint32
	Data       []byte
	ReadSize   uint32
	SentAt     time.Time
	RetryCount int
	Response   chan *Response
	once       sync.Once // guarantees exactly-once delivery to Response channel
}

// Response represents a write response
type Response struct {
	Success   bool
	ErrorCode uint32
	ErrorMsg  string
	RawData   []byte // Response data from DataReportAck (for read operations)
}

// Manager handles pending write operations with timeout and retry
type Manager struct {
	mu      sync.RWMutex
	pending map[uint32]*Entry
	mqtt    *mqtt.Client
	db      *gorm.DB // P3-4: SQLite persistence
}

// nextRequestID is an atomic counter for generating unique request IDs,
// initialized from the current nanosecond timestamp to avoid collisions.
var nextRequestID uint32

func init() {
	nextRequestID = uint32(time.Now().UnixNano())
}

// NewManager creates a new pending write manager
func NewManager(mqttClient *mqtt.Client, db *gorm.DB) *Manager {
	m := &Manager{
		pending: make(map[uint32]*Entry),
		mqtt:    mqttClient,
		db:      db,
	}

	// P3-4: Auto-migrate persistence table and ensure WAL mode
	if db != nil {
		db.AutoMigrate(&models.PendingWriteRecord{})
		db.Exec("PRAGMA journal_mode=WAL")
	}

	// P3-4: Recover pending entries from previous session
	m.recoverPending()

	go m.cleanupLoop()
	return m
}

// resolve delivers a response to the entry's channel exactly once.
// The channel is buffered(1), so this never blocks.
func (e *Entry) resolve(resp *Response) {
	e.once.Do(func() {
		e.Response <- resp
	})
}

// SendWriteCommand sends a WriteCommand and waits for response.
// The ctx parameter allows cancellation (e.g. from HTTP request context).
func (m *Manager) SendWriteCommand(ctx context.Context, deviceID string, channelID uint32, data []byte, readSize uint32, timeout time.Duration) (*Response, error) {
	requestID := atomic.AddUint32(&nextRequestID, 1)

	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(requestID))
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	entry := &Entry{
		RequestID:  requestID,
		DeviceID:   deviceID,
		ChannelID:  channelID,
		Data:       data,
		ReadSize:   readSize,
		SentAt:     time.Now(),
		RetryCount: 0,
		Response:   make(chan *Response, 1),
	}

	m.mu.Lock()
	m.pending[requestID] = entry
	m.mu.Unlock()

	// 8.1: Track active entries
	metrics.PendingWriteActiveEntries.Inc()

	// P3-4: Persist entry to SQLite
	m.persistEntry(entry, requestID, timeout)

	defer func() {
		m.removeEntry(requestID)
		// 8.1: Decrement active entries on exit
		metrics.PendingWriteActiveEntries.Dec()
	}()

	// Send the command (P3-5: QoS 2 for critical write operations)
	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.PublishQoS2(topic, enc.Bytes()); err != nil {
		entry.resolve(&Response{Success: false, ErrorMsg: fmt.Sprintf("failed to publish: %v", err)})
		return nil, fmt.Errorf("failed to publish: %w", err)
	}

	// Wait for response with timeout or context cancellation
	select {
	case resp := <-entry.Response:
		if !resp.Success {
			return resp, fmt.Errorf("device error: code=%d msg=%s", resp.ErrorCode, resp.ErrorMsg)
		}
		return resp, nil
	case <-time.After(timeout):
		entry.resolve(&Response{Success: false, ErrorMsg: "timeout"})
		return nil, fmt.Errorf("write command timeout after %v", timeout)
	case <-ctx.Done():
		entry.resolve(&Response{Success: false, ErrorMsg: "cancelled"})
		return nil, fmt.Errorf("write command cancelled: %w", ctx.Err())
	}
}

// HandleResponse processes a WriteResponse from a device.
// For read operations (ReadSize > 0), WriteRsp only confirms the command was received
// by the firmware — the actual data will arrive via DataReportAck.
// We must NOT resolve the pending entry on WriteRsp for reads, otherwise we lose the RawData.
func (m *Manager) HandleResponse(requestID uint32, success bool, errorCode uint32, errorMsg string) {
	m.mu.Lock()
	entry, ok := m.pending[requestID]
	m.mu.Unlock()

	if !ok {
		logger.Infof("[pendingwrite] late WriteResponse for requestID=%d (entry already removed)", requestID)
		// 8.1: Track late responses
		metrics.PendingWriteLateResponseTotal.Inc()
		return
	}

	// 8.1: Observe response latency
	if !entry.SentAt.IsZero() {
		metrics.PendingWriteDuration.Observe(time.Since(entry.SentAt).Seconds())
	}

	// If this is a read operation (readSize > 0), WriteRsp is just an ACK.
	// The real response comes via HandleDataReportAck with the raw data.
	// Only fail early if the firmware reported an error.
	if entry.ReadSize > 0 {
		if !success {
			entry.resolve(&Response{
				Success:   false,
				ErrorCode: errorCode,
				ErrorMsg:  errorMsg,
			})
			// P3-4: Remove persisted entry on error resolution
			m.removePersistedEntry(requestID)
		}
		// success: don't resolve, wait for DataReportAck
		return
	}

	// For write-only operations (readSize == 0), WriteRsp is the final response.
	entry.resolve(&Response{
		Success:   success,
		ErrorCode: errorCode,
		ErrorMsg:  errorMsg,
	})
	// P3-4: Remove persisted entry on resolution
	m.removePersistedEntry(requestID)
}

// HandleDataReportAck processes a DataReport that carries a non-zero request_id,
// indicating it is an ack/response to a prior WriteCommand with read_size > 0.
// The raw_data in the DataReport is the read-back data.
func (m *Manager) HandleDataReportAck(requestID uint32, rawData []byte) {
	m.mu.Lock()
	entry, ok := m.pending[requestID]
	m.mu.Unlock()

	if !ok {
		logger.Infof("[pendingwrite] late DataReportAck for requestID=%d (entry already removed, likely timed out)", requestID)
		// 8.1: Track late responses
		metrics.PendingWriteLateResponseTotal.Inc()
		return
	}

	// 8.1: Observe response latency
	if !entry.SentAt.IsZero() {
		metrics.PendingWriteDuration.Observe(time.Since(entry.SentAt).Seconds())
	}

	// DataReport ack implies success (device responded with data)
	entry.resolve(&Response{
		Success:   true,
		ErrorCode: 0,
		ErrorMsg:  "",
		RawData:   rawData,
	})
	// P3-4: Remove persisted entry on resolution
	m.removePersistedEntry(requestID)
}

func (m *Manager) removeEntry(requestID uint32) {
	m.mu.Lock()
	delete(m.pending, requestID)
	m.mu.Unlock()
	// P3-4: Remove persisted entry
	m.removePersistedEntry(requestID)
}

// RetryFailed retries failed write commands (called periodically)
func (m *Manager) RetryFailed(maxRetries int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, entry := range m.pending {
		if entry.RetryCount >= maxRetries {
			delete(m.pending, id)
			continue
		}
		if now.Sub(entry.SentAt) > 5*time.Second {
			// Retry
			entry.RetryCount++
			entry.SentAt = now
			// Re-send would go here
		}
	}
}

// Shutdown resolves all pending entries with a shutdown error and clears the map.
// This is called during graceful server shutdown to unblock any waiting goroutines.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	for reqID, entry := range m.pending {
		entry.resolve(&Response{Success: false, ErrorMsg: "server shutting down"})
		delete(m.pending, reqID)
	}
	m.mu.Unlock()
	// P3-4: Persisted entries are intentionally NOT removed on shutdown —
	// they will be recovered on next startup.
}

// P3-4: persistEntry writes the entry metadata to SQLite for crash recovery.
func (m *Manager) persistEntry(entry *Entry, requestID uint32, timeout time.Duration) {
	if m.db == nil {
		return
	}
	record := models.PendingWriteRecord{
		RequestID: requestID,
		DeviceID:  entry.DeviceID,
		ChannelID: entry.ChannelID,
		Data:      entry.Data,
		ReadSize:  entry.ReadSize,
		SentAt:    entry.SentAt,
		TimeoutAt: entry.SentAt.Add(timeout),
	}
	if err := m.db.Create(&record).Error; err != nil {
		logger.Warnf("P3-4: failed to persist entry reqID=%d: %v", requestID, err)
	}
}

// P3-4: removePersistedEntry deletes the persisted record for a completed entry.
func (m *Manager) removePersistedEntry(requestID uint32) {
	if m.db == nil {
		return
	}
	if err := m.db.Delete(&models.PendingWriteRecord{}, requestID).Error; err != nil {
		logger.Warnf("P3-4: failed to remove persisted entry reqID=%d: %v", requestID, err)
	}
}

// P3-4: recoverPending restores pending entries from a previous session.
// Recovered entries get a fresh Response channel; if the device already responded
// during the downtime, that response is lost — the entry will simply time out.
func (m *Manager) recoverPending() {
	if m.db == nil {
		return
	}

	now := time.Now()
	var records []models.PendingWriteRecord

	// Find entries that haven't timed out yet
	if err := m.db.Where("timeout_at > ?", now).Find(&records).Error; err != nil {
		logger.Warnf("P3-4: failed to recover pending entries: %v", err)
		return
	}

	// Clean up expired entries
	m.db.Where("timeout_at <= ?", now).Delete(&models.PendingWriteRecord{})

	if len(records) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range records {
		// Only recover if not already in memory
		if _, exists := m.pending[r.RequestID]; exists {
			continue
		}

		remaining := r.TimeoutAt.Sub(now)
		if remaining <= 0 {
			continue
		}

		m.pending[r.RequestID] = &Entry{
			Response:   make(chan *Response, 1),
			ReadSize:   r.ReadSize,
			SentAt:     r.SentAt,
			DeviceID:   r.DeviceID,
			ChannelID:  r.ChannelID,
			Data:       r.Data,
		}

		// Update nextRequestID to avoid collision with recovered IDs
		if r.RequestID >= nextRequestID {
			atomic.StoreUint32(&nextRequestID, r.RequestID+1)
		}

		logger.Infof("P3-4: recovered pending entry reqID=%d device=%s (remaining=%v)",
			r.RequestID, r.DeviceID, remaining)
	}
}

// cleanupLoop periodically cleans up timed-out entries from both memory and persistence.
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for id, entry := range m.pending {
			// Entries without an active waiter (Response channel not being read)
			// that have been pending for over 5 minutes are considered stale
			if now.Sub(entry.SentAt) > 5*time.Minute {
				entry.resolve(&Response{Success: false, ErrorMsg: "cleanup: stale entry"})
				delete(m.pending, id)
				// 8.1: Track timeout events
				metrics.PendingWriteTimeoutTotal.Inc()
			}
		}
		m.mu.Unlock()

		// P3-4: Clean up persisted timed-out entries
		if m.db != nil {
			m.db.Where("timeout_at <= ?", time.Now()).Delete(&models.PendingWriteRecord{})
		}
	}
}
