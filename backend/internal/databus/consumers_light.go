package databus

import (
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/metrics"
)

// TerminalConsumer records RX data in the terminal manager.
// Handles ALL events (passive + command response). No DB operations.
type TerminalConsumer struct {
	termMgr *terminal.Manager
}

func NewTerminalConsumer(termMgr *terminal.Manager) *TerminalConsumer {
	return &TerminalConsumer{termMgr: termMgr}
}

func (c *TerminalConsumer) Name() string                    { return "terminal" }
func (c *TerminalConsumer) ShouldHandle(evt DataEvent) bool { return true }
func (c *TerminalConsumer) Handle(evt DataEvent) {
	if c.termMgr != nil {
		c.termMgr.RecordRX(evt.DeviceID, uint(evt.ChannelID), evt.RawData)
	}
}

// WSPushConsumer broadcasts channel_data WebSocket events for uncorrelated
// terminal/RX reports. Parsed scheduled samples publish their richer compatible
// channel_data payload from SensorParserConsumer instead.
// This is the fast path for passive/terminal data.
type WSPushConsumer struct {
	wsHub *websocket.Hub
}

// DataMetricsConsumer restores scheduler report outcome accounting that
// previously lived in the node worker. Command responses are accounted by
// pending-write instead.
type DataMetricsConsumer struct{}

func NewDataMetricsConsumer() *DataMetricsConsumer { return &DataMetricsConsumer{} }
func (c *DataMetricsConsumer) Name() string        { return "data_metrics" }
func (c *DataMetricsConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.IsScheduledSample()
}
func (c *DataMetricsConsumer) Handle(evt DataEvent) {
	if evt.IsError() || len(evt.RawData) == 0 {
		metrics.DataReportErrors.Inc()
		return
	}
	metrics.DataReportsProcessed.Inc()
}

func NewWSPushConsumer(wsHub *websocket.Hub) *WSPushConsumer {
	return &WSPushConsumer{wsHub: wsHub}
}

func (c *WSPushConsumer) Name() string { return "ws_push" }
func (c *WSPushConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.IsPassive()
}
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
