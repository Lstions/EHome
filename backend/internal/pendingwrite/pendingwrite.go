package pendingwrite

import (
	"fmt"
	"sync"
	"time"

	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
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

// NewManager creates a new pending write manager
func NewManager(mqttClient *mqtt.Client) *Manager {
	return &Manager{
		pending: make(map[uint32]*Entry),
		mqtt:    mqttClient,
	}
}

// SendWriteCommand sends a WriteCommand and waits for response
func (m *Manager) SendWriteCommand(deviceID string, channelID uint32, data []byte, readSize uint32, timeout time.Duration) (*Response, error) {
	requestID := uint32(time.Now().UnixNano())

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

	// Send the command
	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		m.removeEntry(requestID)
		return nil, fmt.Errorf("failed to publish: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-entry.Response:
		m.removeEntry(requestID)
		return resp, nil
	case <-time.After(timeout):
		m.removeEntry(requestID)
		return nil, fmt.Errorf("write command timeout after %v", timeout)
	}
}

// HandleResponse processes a WriteResponse from a device
func (m *Manager) HandleResponse(requestID uint32, success bool, errorCode uint32, errorMsg string) {
	m.mu.Lock()
	entry, ok := m.pending[requestID]
	m.mu.Unlock()

	if !ok {
		return
	}

	entry.Response <- &Response{
		Success:   success,
		ErrorCode: errorCode,
		ErrorMsg:  errorMsg,
	}
}

// HandleDataReportAck processes a DataReport that carries a non-zero request_id,
// indicating it is an ack/response to a prior WriteCommand with read_size > 0.
// The raw_data in the DataReport is the read-back data.
func (m *Manager) HandleDataReportAck(requestID uint32, rawData []byte) {
	m.mu.Lock()
	entry, ok := m.pending[requestID]
	m.mu.Unlock()

	if !ok {
		return
	}

	// DataReport ack implies success (device responded with data)
	entry.Response <- &Response{
		Success:   true,
		ErrorCode: 0,
		ErrorMsg:  "",
		RawData:   rawData,
	}
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
