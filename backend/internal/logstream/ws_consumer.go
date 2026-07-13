package logstream

import (
	"ehome/backend/internal/events"
)

// WSBroadcaster is the minimal interface for role-restricted WebSocket events.
type WSBroadcaster interface {
	BroadcastEventToRole(eventType string, payload interface{}, role string)
}

// WSConsumer pushes log batches to connected WebSocket clients as "node_log" events.
// It is always active when registered. The hub limits these sensitive events
// to administrators; the frontend may additionally filter by node_id.
type WSConsumer struct {
	hub WSBroadcaster
}

// NewWSConsumer creates a new WebSocket log consumer.
func NewWSConsumer(hub WSBroadcaster) *WSConsumer {
	return &WSConsumer{hub: hub}
}

func (c *WSConsumer) Name() string   { return "websocket" }
func (c *WSConsumer) IsActive() bool { return true }

func (c *WSConsumer) Consume(batch LogBatch) {
	type logLine struct {
		Level int    `json:"level"`
		Ts    int64  `json:"ts"`
		Tag   string `json:"tag"`
		Msg   string `json:"msg"`
	}

	lines := make([]logLine, len(batch.Logs))
	for i, log := range batch.Logs {
		lines[i] = logLine{
			Level: log.Level,
			Ts:    log.Ts,
			Tag:   log.Tag,
			Msg:   log.Message,
		}
	}

	c.hub.BroadcastEventToRole(events.NodeLog, map[string]interface{}{
		"node_id": batch.NodeID,
		"lines":   lines,
	}, "admin")
}
