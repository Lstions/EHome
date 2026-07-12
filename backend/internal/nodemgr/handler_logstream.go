package nodemgr

import (
	"ehome/backend/internal/logstream"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
)

// handleLogStream parses a MsgLogStream (0x1D) frame and publishes it to the LogEventBus.
// The bus fans out to all registered consumers (WS push, DB persist, etc.).
// This handler is intentionally lightweight — all heavy work happens in consumers.
func (m *Manager) handleLogStream(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Warnf("[%s] LogStream: decoder init failed: %v", deviceID, err)
		return
	}

	var batch logstream.LogBatch
	batch.NodeID = deviceID

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}

		switch field.FieldNum {
		case 1: // count (informational)
		case 2: // seq
			if v, ok := field.Value.(uint64); ok {
				batch.Seq = int(v)
			}
		case 3: // repeated log entry sub-frame
			if data, ok := field.Value.([]byte); ok {
				entry := parseLogEntry(data)
				if entry != nil {
					entry.NodeID = deviceID
					batch.Logs = append(batch.Logs, *entry)
				}
			}
		}
	}

	if len(batch.Logs) == 0 {
		logger.Warnf("[%s] LogStream: parsed zero entries from %d-byte payload", deviceID, len(payload))
		return
	}

	logger.Infof("[%s] LogStream: parsed %d entries seq=%d", deviceID, len(batch.Logs), batch.Seq)
	// Publish to event bus — consumers handle the rest
	m.logBus.Publish(batch)
}

// parseLogEntry decodes a single log entry sub-frame.
// Sub-frame fields: 1=level(varint), 2=ts(varint), 3=tag(string), 4=msg(string)
func parseLogEntry(data []byte) *logstream.LogEntry {
	sub, err := frame.NewSubDecoder(data)
	if err != nil {
		return nil
	}

	entry := &logstream.LogEntry{}

	for {
		field, err := sub.NextField()
		if err != nil {
			break
		}

		switch field.FieldNum {
		case 1: // level
			if v, ok := field.Value.(uint64); ok {
				entry.Level = int(v)
			}
		case 2: // ts (microseconds)
			if v, ok := field.Value.(uint64); ok {
				entry.Ts = int64(v)
			}
		case 3: // tag
			if s, ok := field.Value.([]byte); ok {
				entry.Tag = string(s)
			}
		case 4: // message
			if s, ok := field.Value.([]byte); ok {
				entry.Message = string(s)
			}
		}
	}

	return entry
}
