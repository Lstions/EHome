// Package events defines WebSocket event name constants as a single source of truth.
// Both backend and frontend must reference these constants to avoid name mismatches (G4 fix).
package events

// WebSocket event names — noun_verb format, lowercase, underscore-separated
//
// v2.2 兼容策略: 保留 v2.1 事件名 + 新增 v2.2 事件名, 6 个月后删除 v2.1
// 后端双发: collector_status + node_status (同一 payload)
// 前端双订阅: 订阅两个事件名, 或只订阅新名
const (
	// ============================================================
	// v2.1 事件名 (保留 6 个月, 之后删除)
	// ============================================================

	// Collector events (v2.1)
	CollectorStatus        = "collector_status"
	CollectorConfigSynced  = "collector_config_synced"
	CollectorConfigChanged = "collector_config_changed"

	// Device events (v2.1)
	DeviceStatus = "device_status"

	// ============================================================
	// v2.2 事件名 (新增, 推荐)
	// ============================================================

	// Node events (v2.2 替代 collector)
	NodeStatus        = "node_status"
	NodeConfigSynced  = "node_config_synced"
	NodeConfigChanged = "node_config_changed"

	// EdgeDevice events (v2.2 替代 device)
	EdgeDeviceStatus = "edge_device_status"

	// ============================================================
	// 通用事件 (v2.1 + v2.2 共用, 不改名)
	// ============================================================

	// Data events
	DataUpdate = "data_update"

	// OTA events
	OTAProgress  = "ota_progress"
	OTACompleted = "ota_completed"

	// Notification events
	Notification = "notification"

	// Diagnostic events
	PingResult  = "ping_result"
	ScanResult  = "scan_result"
	ChannelData = "channel_data"
	TerminalAck = "terminal_ack"
)
