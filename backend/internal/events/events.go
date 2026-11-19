// Package events defines WebSocket event name constants as a single source of truth.
// Both backend and frontend must reference these constants to avoid name mismatches (G4 fix).
package events

// WebSocket event names — noun_verb format, lowercase, underscore-separated
const (
	// Node events
	NodeStatus           = "node_status"
	NodeConfigSynced     = "node_config_synced"
	NodeConfigChanged    = "node_config_changed"
	NodeResourcesUpdated = "node_resources_updated"

	// EdgeDevice events
	EdgeDeviceStatus      = "edge_device_status"
	DeviceOperationUpdate = "device_operation_update"
	// EdgeDevicesLive 是**1Hz 状态快照**（不是事件流）：
	// 一次带全体边缘设备的 {id, status, last_data_at, data}。
	//
	// 为什么与 data_update 并存而不是替代它：
	//   · data_update 是按帧的**事件流**（一台设备可能每秒数条），详情页/图表要的是
	//     "刚收到了什么"，必须逐帧；
	//   · 列表/看板要的是"现在是什么状态"，逐帧流对它们只是噪声 —— 快照把客户端
	//     从"解析并合并 N 条/秒"变成"每秒读 1 条"。
	// 无客户端连接时不发（见 livedevices.Publisher），因此空闲系统零成本。
	EdgeDevicesLive = "edge_devices_live"

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

	// v2.5: System log stream
	NodeLog = "node_log"

	// Peripheral events (v3.0: GPIO/PWM control)
	PeriphResult = "periph_result" // GPIO/PWM 操作结果
	PeriphState  = "periph_state"  // GPIO/PWM 状态变更
)
