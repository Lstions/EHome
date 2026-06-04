package terminal

import (
	"encoding/hex"
	"sync"
	"time"

	"ehome/backend/pkg/logger"
)

const (
	// RingBuffer capacity per channel
	ringBufferSize = 256
)

// Direction indicates TX or RX
type Direction int

const (
	DirectionTX Direction = iota
	DirectionRX
)

// Entry is a single terminal log entry
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Direction string    `json:"direction"` // "tx" or "rx"
	DataHex   string    `json:"data_hex"`
	DataASCII string    `json:"data_ascii"`
	Length    int       `json:"length"`
}

// ChannelTerminal manages a ring buffer of TX/RX entries for a channel
type ChannelTerminal struct {
	mu      sync.RWMutex
	entries []Entry
	head    int
	count   int
	cap     int
}

// NewChannelTerminal creates a new terminal with ring buffer
func NewChannelTerminal() *ChannelTerminal {
	return &ChannelTerminal{
		entries: make([]Entry, ringBufferSize),
		cap:     ringBufferSize,
	}
}

// Append adds a new entry to the ring buffer
func (t *ChannelTerminal) Append(dir Direction, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry := Entry{
		Timestamp: time.Now(),
		Direction: directionString(dir),
		DataHex:   hex.EncodeToString(data),
		DataASCII: safeASCII(data),
		Length:    len(data),
	}

	t.entries[t.head] = entry
	t.head = (t.head + 1) % t.cap
	if t.count < t.cap {
		t.count++
	}
}

// History returns the last n entries (most recent last)
func (t *ChannelTerminal) History(n int) []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if n > t.count {
		n = t.count
	}

	result := make([]Entry, 0, n)
	// Calculate start position
	start := (t.head - n + t.cap) % t.cap
	for i := 0; i < n; i++ {
		idx := (start + i) % t.cap
		result = append(result, t.entries[idx])
	}

	return result
}

// Count returns current number of entries
func (t *ChannelTerminal) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.count
}

// Manager manages terminals for all channels
type Manager struct {
	mu        sync.RWMutex
	terminals map[uint]*ChannelTerminal // key: channel_id
	wsCh      chan TerminalEvent        // WebSocket broadcast channel
}

// TerminalEvent is sent via WebSocket on new TX/RX
type TerminalEvent struct {
	DeviceID  string `json:"device_id"`
	ChannelID uint   `json:"channel_id"`
	Direction string `json:"direction"`
	DataHex   string `json:"data_hex"`
	DataASCII string `json:"data_ascii"`
	Timestamp int64  `json:"timestamp"`
}

// NewManager creates a new terminal manager
func NewManager() *Manager {
	return &Manager{
		terminals: make(map[uint]*ChannelTerminal),
		wsCh:      make(chan TerminalEvent, 64),
	}
}

// Events returns the channel for WebSocket broadcasting
func (m *Manager) Events() <-chan TerminalEvent {
	return m.wsCh
}

// RecordTX records a transmitted command
func (m *Manager) RecordTX(deviceID string, channelID uint, data []byte) {
	m.record(deviceID, channelID, DirectionTX, data)
}

// RecordRX records received data
func (m *Manager) RecordRX(deviceID string, channelID uint, data []byte) {
	m.record(deviceID, channelID, DirectionRX, data)
}

func (m *Manager) record(deviceID string, channelID uint, dir Direction, data []byte) {
	m.mu.RLock()
	t, ok := m.terminals[channelID]
	m.mu.RUnlock()

	if !ok {
		m.mu.Lock()
		t, ok = m.terminals[channelID]
		if !ok {
			t = NewChannelTerminal()
			m.terminals[channelID] = t
		}
		m.mu.Unlock()
	}

	t.Append(dir, data)

	// Send WebSocket event
	evt := TerminalEvent{
		DeviceID:  deviceID,
		ChannelID: channelID,
		Direction: directionString(dir),
		DataHex:   hex.EncodeToString(data),
		DataASCII: safeASCII(data),
		Timestamp: time.Now().UnixMilli(),
	}

	select {
	case m.wsCh <- evt:
	default:
		logger.Warnf("Terminal WS channel full, dropping event for ch=%d", channelID)
	}
}

// GetHistory returns terminal history for a channel
func (m *Manager) GetHistory(channelID uint, count int) []Entry {
	m.mu.RLock()
	t, ok := m.terminals[channelID]
	m.mu.RUnlock()

	if !ok {
		return []Entry{}
	}
	return t.History(count)
}

// Helper functions

func directionString(dir Direction) string {
	if dir == DirectionTX {
		return "tx"
	}
	return "rx"
}

func safeASCII(data []byte) string {
	result := make([]byte, len(data))
	for i, b := range data {
		if b >= 32 && b <= 126 {
			result[i] = b
		} else {
			result[i] = '.'
		}
	}
	return string(result)
}
