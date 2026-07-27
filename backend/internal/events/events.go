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
