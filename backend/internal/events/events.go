// Package events defines WebSocket event name constants as a single source of truth.
// Both backend and frontend must reference these constants to avoid name mismatches (G4 fix).
package events

// WebSocket event names — noun_verb format, lowercase, underscore-separated
const (
	// Collector events
	CollectorStatus       = "collector_status"
	CollectorConfigSynced = "collector_config_synced"
	CollectorConfigChanged = "collector_config_changed"

	// Data events
	DataUpdate = "data_update"

	// Device events
	DeviceStatus = "device_status"

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
