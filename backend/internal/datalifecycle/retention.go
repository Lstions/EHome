package datalifecycle

import "sync/atomic"

// systemRetentionDays holds the system-level retention snapshot source
// (方案 §4.1). main.go sets it from config at startup; logical device
// creation paths read it. Snapshot semantics: the value is captured at
// creation time, later config changes never retroact on existing rows.
var systemRetentionDays atomic.Int64

func init() {
	systemRetentionDays.Store(365)
}

// SetSystemRetentionDays updates the snapshot source (values <= 0 ignored).
func SetSystemRetentionDays(days int) {
	if days > 0 {
		systemRetentionDays.Store(int64(days))
	}
}

// SystemRetentionDays returns the current system-level retention (days)
// applied to newly created logical devices.
func SystemRetentionDays() int {
	return int(systemRetentionDays.Load())
}
