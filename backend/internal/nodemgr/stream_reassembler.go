package nodemgr

import (
	"sync"
	"time"

	"ehome/backend/pkg/logger"
)

// reassemblyKey scopes a reassembly buffer to one origin device. requestID
// alone is not unique across devices (each ESP32 numbers its own requests),
// so a shared buffer keyed only by requestID lets concurrent frames from
// different devices interleave into garbage. The composite key is a hard
// prerequisite for running multiple parser shards concurrently.
type reassemblyKey struct {
	deviceID  string
	requestID uint32
}

// streamReassembler accumulates partial DataReport payloads by
// (deviceID, requestID) and reassembles them into complete frames for
// protocol parsing.
//
// P1-8 complement: ESP32 rx_task uses UART idle detection to report complete
// responses most of the time. This buffer is a safety net for edge cases:
// very slow baud rates, multi-frame protocols, or UART noise causing
// premature idle detection.
type streamReassembler struct {
	mu       sync.Mutex
	buffers  map[reassemblyKey]*reassemblyBuffer
	maxAge   time.Duration // discard buffers older than this
	maxBytes int           // hard cap per buffer
}

type reassemblyBuffer struct {
	data   []byte
	lastRx time.Time
}

func newStreamReassembler() *streamReassembler {
	return &streamReassembler{
		buffers:  make(map[reassemblyKey]*reassemblyBuffer),
		maxAge:   2 * time.Second,
		maxBytes: 2048,
	}
}

// Append adds raw bytes to the buffer for the given device/request pair and
// returns the accumulated bytes. If requestID is 0 (CMD_SAMPLE, no
// correlation), returns data unchanged (no buffering).
func (sr *streamReassembler) Append(deviceID string, requestID uint32, data []byte) []byte {
	if requestID == 0 || len(data) == 0 {
		return data
	}

	sr.mu.Lock()
	defer sr.mu.Unlock()

	// Garbage collect expired buffers
	now := time.Now()
	for key, buf := range sr.buffers {
		if now.Sub(buf.lastRx) > sr.maxAge {
			logger.Debugf("[stream] discarding expired buffer for %s requestID=%d (%d bytes, age=%v)",
				key.deviceID, key.requestID, len(buf.data), now.Sub(buf.lastRx))
			delete(sr.buffers, key)
		}
	}

	key := reassemblyKey{deviceID: deviceID, requestID: requestID}
	buf, exists := sr.buffers[key]
	if !exists {
		buf = &reassemblyBuffer{}
		sr.buffers[key] = buf
	}
	buf.lastRx = now

	// Append new data
	buf.data = append(buf.data, data...)
	if len(buf.data) > sr.maxBytes {
		// Hard cap: keep tail
		buf.data = buf.data[len(buf.data)-sr.maxBytes:]
		logger.Warnf("[stream] %s requestID=%d buffer capped at %d bytes", deviceID, requestID, sr.maxBytes)
	}

	return buf.data
}

// Consume discards the buffer for the given device/request pair after
// successful parse.
func (sr *streamReassembler) Consume(deviceID string, requestID uint32) {
	sr.discard(deviceID, requestID)
}

// discard removes the buffer for the given device/request pair without
// consuming (e.g., on parse error that should not be retried).
func (sr *streamReassembler) discard(deviceID string, requestID uint32) {
	if requestID == 0 {
		return
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.buffers, reassemblyKey{deviceID: deviceID, requestID: requestID})
}

// pending returns the number of active reassembly buffers.
func (sr *streamReassembler) pending() int {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return len(sr.buffers)
}
