package databus

import (
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
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
		// ==================== 为什么这里必须打日志（S1，2026-11-11）====================
		//
		// 改造前本分支**只** +1 指标，零日志。而 device_data 退役后（原始数据不入库，
		// 见 docs/设计/原始数据退役-改造方案-2026-11-11.md §4.2.1），日志是这类
		// 「设备上报了错误码 / 空数据」的**唯一**取证来源：
		//
		//	错误样本（生产 24h 17,278 条，全部来自设备 16）在原 device_data 里有行
		//	（db_persist 消费者写 raw + error_code）。现在没有行了 —— 不打日志就等于
		//	「设备 16 从来没数据」这个现象**无法归因**（§4.3 排查手册第 3 行）。
		//
		// 指标仍然保留：它回答「有多少」，日志回答「哪台设备、哪条命令、错误码几」。
		// 两者不可互相替代，所以不是把 Inc() 换成日志，而是**并列**。
		//
		// 字段口径与解析失败日志（consumers_heavy.go 的 "decode failed"）对齐，
		// 同一个 edge_device_id / channel_id / command_index 可以跨两类日志串联。
		// raw_hex 走 rawHexForLog（前缀截断 128 B）：这类样本生产实测 raw 就是空串，
		// 但 ErrorCode!=0 且带 raw 的形态也存在（固件会回带出错时的原始帧）。
		logger.Warn("databus: data report rejected at ingest",
			"node_id", evt.DeviceID,
			"edge_device_id", evt.EdgeDeviceID,
			"channel_id", evt.ChannelID,
			"command_index", evt.CommandIndex,
			"error_code", evt.ErrorCode,
			"request_id", evt.RequestID,
			"raw_len", len(evt.RawData),
			"raw_hex", rawHexForLog(evt.RawData),
		)
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
