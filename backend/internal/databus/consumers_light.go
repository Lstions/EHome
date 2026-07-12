package databus

import (
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"
)

// TerminalConsumer records RX data in the terminal manager.
// Handles ALL events (passive + command response). No DB operations.
type TerminalConsumer struct {
	termMgr *terminal.Manager
}

func NewTerminalConsumer(termMgr *terminal.Manager) *TerminalConsumer {
	return &TerminalConsumer{termMgr: termMgr}
}

func (c *TerminalConsumer) Name() string                  { return "terminal" }
func (c *TerminalConsumer) ShouldHandle(evt DataEvent) bool { return true }
func (c *TerminalConsumer) Handle(evt DataEvent) {
	if c.termMgr != nil {
		c.termMgr.RecordRX(evt.DeviceID, uint(evt.ChannelID), evt.RawData)
	}
}

// WSPushConsumer broadcasts channel_data WebSocket events to frontend.
// Handles ALL events. No DB operations — pure memory.
// This is the fast path for passive/terminal data.
type WSPushConsumer struct {
	wsHub *websocket.Hub
}

func NewWSPushConsumer(wsHub *websocket.Hub) *WSPushConsumer {
	return &WSPushConsumer{wsHub: wsHub}
}

func (c *WSPushConsumer) Name() string                  { return "ws_push" }
func (c *WSPushConsumer) ShouldHandle(evt DataEvent) bool { return true }
func (c *WSPushConsumer) Handle(evt DataEvent) {
	if c.wsHub == nil {
		return
	}
	event := map[string]interface{}{
		"device_id":      evt.DeviceID,
		"node_id":        evt.DeviceID,
		"channel_id":     evt.ChannelID,
		"raw_hex":        fmt.Sprintf("%x", evt.RawData),
		"timestamp":      time.Now().Unix(),
		"error_code":     evt.ErrorCode,
		"request_id":     evt.RequestID,
		"edge_device_id": evt.EdgeDeviceID,
		"command_index":  evt.CommandIndex,
	}
	c.wsHub.BroadcastEvent(events.ChannelData, event)
}
