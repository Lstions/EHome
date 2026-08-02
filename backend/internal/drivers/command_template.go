package drivers

import "time"

// CommandTemplate defines a protocol command exposed by a device driver.
// Each driver can provide a list of command templates.
//
// Two modes:
//
//	Schedulable=true  → polling command, encoded in ConfigManifest with interval.
//	Schedulable=false → trigger command, executed only on user request via API.
type CommandTemplate struct {
	ID          string `json:"id"`          // e.g. "read_basic_info", "close_discharge_mos"
	Name        string `json:"name"`        // "读取基本信息", "关放电MOS"
	Type        string `json:"type"`        // "read" | "write"
	CmdByte     byte   `json:"cmd_byte"`    // protocol command byte (0x03, 0x04, etc.)
	WriteData   string `json:"write_data"`  // hex string of the command frame to send
	ReadLength  uint32 `json:"read_length"` // expected response length
	DelayMs     uint32 `json:"delay_ms"`    // post-TX delay
	IntervalMs  int    `json:"interval_ms"` // default polling interval, only meaningful when Schedulable
	Schedulable bool   `json:"schedulable"` // true = polling command (has interval), false = one-shot trigger
	Description string `json:"description"` // human-readable description
}

// CommandTemplateProvider is an optional interface that drivers can implement
// to expose their protocol commands as templates.
type CommandTemplateProvider interface {
	GetCommandTemplates() []CommandTemplate
}

// InitStep describes a single initialization step for a device driver.
// Role is a stable semantic tag (e.g. "calib") that callers use to dispatch
// side-effects (calibration persistence) instead of matching on step.Name,
// which is driver-specific and fragile.
type InitStep struct {
	Name     string
	Data     []byte
	ReadSize uint32
	Timeout  time.Duration
	Role     string
}

// InitSequenceProvider is an optional interface that drivers can implement to
// declare their device's initialization sequence. When implemented, this takes
// priority over DeviceConfig.InitFlow JSONB and the hardcoded fallback switch.
type InitSequenceProvider interface {
	GetInitSequence() []InitStep
}
