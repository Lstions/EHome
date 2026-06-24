package pendingwrite

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
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
}

// nextRequestID is an atomic counter for generating unique request IDs,
// initialized from the current nanosecond timestamp to avoid collisions.
var nextRequestID uint32

func init() {
	nextRequestID = uint32(time.Now().UnixNano())
}

// NewManager creates a new pending write manager
func NewManager(mqttClient *mqtt.Client) *Manager {
	return &Manager{
		pending: make(map[uint32]*Entry),
		mqtt:    mqttClient,
	}
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

	defer m.removeEntry(requestID)

	// Send the command
	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
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
		return
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
		return
	}

	// DataReport ack implies success (device responded with data)
	entry.resolve(&Response{
		Success:   true,
		ErrorCode: 0,
		ErrorMsg:  "",
		RawData:   rawData,
	})
}

func (m *Manager) removeEntry(requestID uint32) {
	m.mu.Lock()
	delete(m.pending, requestID)
	m.mu.Unlock()
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
}
