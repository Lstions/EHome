package models

import "time"

// DeviceDiagReport is one boot/crash diagnostics record uploaded by an ESP32
// over MSG_DIAG_REPORT (0x1E). Unlike NodeLog (0x1D, a rolling best-effort
// system-log stream), a crash report is a durable evidence artifact: the
// device keeps its NVS copy until the server ACKs persistence, so this table
// must retain the exact pre-reboot context (record id, PC, stack window,
// exception/core/exccause) needed to explain an otherwise unattributable
// reboot.
//
// The (device_id, record_id) unique index makes retries idempotent: a device
// that re-uploads the same crash record (e.g. the ACK was lost) upserts the
// same row instead of accumulating duplicates.
type DeviceDiagReport struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	DeviceID       string `gorm:"column:device_id;type:varchar(32);not null;uniqueIndex:idx_device_diag_device_record,priority:1" json:"device_id"`
	RecordID       uint32 `gorm:"column:record_id;not null;uniqueIndex:idx_device_diag_device_record,priority:2" json:"record_id"`
	ReportType     uint8  `gorm:"column:report_type;not null" json:"report_type"` // 1=BOOT, 2=CRASH
	ResetReason    int32  `gorm:"column:reset_reason;not null;default:0" json:"reset_reason"`
	ResetReasonStr string `gorm:"column:reset_reason_str;type:varchar(64);not null;default:''" json:"reset_reason_str"`
	UptimeSec      uint64 `gorm:"column:uptime_sec;not null;default:0" json:"uptime_sec"`
	CRC32          uint32 `gorm:"column:crc32;not null;default:0" json:"crc32"`
	Core           uint8  `gorm:"column:core;not null;default:0" json:"core"`
	Exception      int32  `gorm:"column:exception;not null;default:0" json:"exception"`
	PC             uint32 `gorm:"column:pc;not null;default:0" json:"pc"`
	Exccause       int32  `gorm:"column:exccause;not null;default:0" json:"exccause"`
	TaskName       string `gorm:"column:task_name;type:varchar(32);not null;default:''" json:"task_name"`
	// Stack is the little-endian uint32 stack window (16 words = 64 bytes).
	// bytea keeps the raw firmware bytes; interpretation is a reader concern.
	Stack        []byte    `gorm:"column:stack;type:bytea" json:"stack"`
	FwVersion    string    `gorm:"column:fw_version;type:varchar(32);not null;default:''" json:"fw_version"`
	RebootReason string    `gorm:"column:reboot_reason;type:varchar(64);not null;default:''" json:"reboot_reason"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;index:idx_device_diag_created" json:"created_at"`
}

func (DeviceDiagReport) TableName() string { return "device_diag_reports" }
