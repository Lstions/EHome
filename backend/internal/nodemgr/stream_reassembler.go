package nodemgr

import (
	"sync"
	"time"

	"ehome/backend/pkg/logger"
)

// streamReassembler accumulates partial DataReport payloads by requestID
// and reassembles them into complete frames for protocol parsing.
//
// P1-8 complement: ESP32 rx_task uses UART idle detection to report complete
// responses most of the time. This buffer is a safety net for edge cases:
// very slow baud rates, multi-frame protocols, or UART noise causing
// premature idle detection.
type streamReassembler struct {
	mu       sync.Mutex
	buffers  map[uint32]*reassemblyBuffer // key = requestID
	maxAge   time.Duration                // discard buffers older than this
	maxBytes int                          // hard cap per buffer
}

type reassemblyBuffer struct {
	data   []byte
	lastRx time.Time
}

func newStreamReassembler() *streamReassembler {
	return &streamReassembler{
		buffers:  make(map[uint32]*reassemblyBuffer),
		maxAge:   2 * time.Second,
		maxBytes: 2048,
	}
}

// append adds raw bytes to the buffer for the given requestID and returns
// the accumulated bytes. If requestID is 0 (CMD_SAMPLE, no correlation),
// returns data unchanged (no buffering).
func (sr *streamReassembler) append(requestID uint32, data []byte) []byte {
	if requestID == 0 || len(data) == 0 {
		return data
	}

	sr.mu.Lock()
	defer sr.mu.Unlock()

	// Garbage collect expired buffers
	now := time.Now()
	for rid, buf := range sr.buffers {
		if now.Sub(buf.lastRx) > sr.maxAge {
			logger.Debugf("[stream] discarding expired buffer for requestID=%d (%d bytes, age=%v)",
				rid, len(buf.data), now.Sub(buf.lastRx))
			delete(sr.buffers, rid)
		}
	}

	buf, exists := sr.buffers[requestID]
	if !exists {
		buf = &reassemblyBuffer{}
		sr.buffers[requestID] = buf
	}
	buf.lastRx = now

	// Append new data
	buf.data = append(buf.data, data...)
	if len(buf.data) > sr.maxBytes {
		// Hard cap: keep tail
		buf.data = buf.data[len(buf.data)-sr.maxBytes:]
		logger.Warnf("[stream] requestID=%d buffer capped at %d bytes", requestID, sr.maxBytes)
	}

	return buf.data
}

// consume removes the buffer for the given requestID after successful parse.
func (sr *streamReassembler) consume(requestID uint32) {
	if requestID == 0 {
		return
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.buffers, requestID)
}

// discard removes the buffer for the given requestID without consuming
// (e.g., on parse error that should not be retried).
func (sr *streamReassembler) discard(requestID uint32) {
	if requestID == 0 {
		return
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.buffers, requestID)
}

// pending returns the number of active reassembly buffers.
func (sr *streamReassembler) pending() int {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return len(sr.buffers)
}
