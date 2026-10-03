package nodemgr

import (
	"errors"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm/clause"
)

// Diagnostic report types (MSG_DIAG_REPORT field 2). Frozen wire values.
const (
	diagReportTypeBoot  = 1
	diagReportTypeCrash = 2
)

// diagMaxStackBytes bounds field 12: the firmware sends a 16-word (64-byte)
// little-endian uint32 stack window (CRASH_DIAG_STACK_WORDS = 16). A larger
// field is malformed input, not a longer stack.
const diagMaxStackBytes = 64

// diagReport is the decoded MSG_DIAG_REPORT payload. Field presence is
// tracked separately (haveRecordID/haveReportType) so a missing required
// field is distinguishable from one that is present and zero.
type diagReport struct {
	RecordID       uint32
	ReportType     uint8
	ResetReason    int32
	UptimeSec      uint64
	ResetReasonStr string
	CRC32          uint32
	Core           uint8
	Exception      int32
	PC             uint32
	Exccause       int32
	TaskName       string
	Stack          []byte
	FwVersion      string
	RebootReason   string
}

// handleDiagReport processes MSG_DIAG_REPORT (0x1E, ESP→SVR).
//
// This is the one message type whose persistence is confirmed back to the
// device: the ESP32 keeps its NVS crash record until it receives
// MSG_DIAG_ACK(record_id, accepted=true), so this handler must never ACK a
// report it did not commit. A malformed frame or a failed insert ACKs
// accepted=false (or, when the record id itself is unknown, logs and drops)
// and the device retries on its next uplink.
func (m *Manager) handleDiagReport(deviceID string, payload []byte) {
	report, err := decodeDiagReport(payload)
	if err != nil {
		logger.Warnf("[%s] Rejecting malformed DiagReport: %v", deviceID, err)
		return
	}

	// Field 1 (record_id) is mandatory: without it we cannot correlate the ACK
	// with the device's NVS slot, so a CRASH report must never be persisted
	// half-identified. The firmware always emits fields 1 and 2.
	if report.RecordID == 0 && report.ReportType == diagReportTypeCrash {
		logger.Warnf("[%s] Rejecting DiagReport: CRASH report with record_id=0", deviceID)
		return
	}

	// Requirement 5: make reboots explainable at info level, with the device id
	// and enough context to attribute an otherwise-unattributable restart.
	if report.ReportType == diagReportTypeCrash {
		logger.Infof("[%s] DiagReport CRASH: record_id=%08X reset_reason=%d(%s) uptime=%ds pc=%08X exception=%d core=%d exccause=%d task=%q fw=%s reboot=%q",
			deviceID, report.RecordID, report.ResetReason, report.ResetReasonStr, report.UptimeSec,
			report.PC, report.Exception, report.Core, report.Exccause, report.TaskName,
			report.FwVersion, report.RebootReason)
	} else {
		logger.Infof("[%s] DiagReport BOOT: reset_reason=%d(%s) uptime=%ds fw=%s reboot=%q",
			deviceID, report.ResetReason, report.ResetReasonStr, report.UptimeSec,
			report.FwVersion, report.RebootReason)
	}

	if err := m.persistDiagReport(deviceID, report); err != nil {
		logger.Errorf("[%s] Failed to persist DiagReport record_id=%08X: %v", deviceID, report.RecordID, err)
		// Persistence failed: tell the device to KEEP its NVS copy so the crash
		// evidence survives. Only crash reports are ACKed.
		if report.ReportType == diagReportTypeCrash {
			if ackErr := m.SendDiagAck(deviceID, report.RecordID, false); ackErr != nil {
				logger.Errorf("[%s] Failed to send DiagAck(false) record_id=%08X: %v", deviceID, report.RecordID, ackErr)
			}
		}
		return
	}

	// Persisted OK. A duplicate record_id is an idempotent upsert and still
	// ACKs accepted=true (the device just re-sends until it hears an ACK).
	if report.ReportType == diagReportTypeCrash {
		if err := m.SendDiagAck(deviceID, report.RecordID, true); err != nil {
			logger.Errorf("[%s] Failed to send DiagAck(true) record_id=%08X: %v", deviceID, report.RecordID, err)
		}
	}
	// BOOT reports (record_id=0) do not occupy NVS and are never ACKed.
}

// persistDiagReport upserts one diag row keyed by (device_id, record_id).
//
// The ON CONFLICT upsert (same mechanism as commandexec.AccumulateMetricsBaseline)
// is what makes retries idempotent: SQLite and PostgreSQL both support
// ON CONFLICT DO UPDATE, so a retransmitted crash record updates its row
// instead of failing the UNIQUE constraint or inserting a duplicate.
func (m *Manager) persistDiagReport(deviceID string, report diagReport) error {
	row := models.DeviceDiagReport{
		DeviceID:       deviceID,
		RecordID:       report.RecordID,
		ReportType:     report.ReportType,
		ResetReason:    report.ResetReason,
		ResetReasonStr: report.ResetReasonStr,
		UptimeSec:      report.UptimeSec,
		CRC32:          report.CRC32,
		Core:           report.Core,
		Exception:      report.Exception,
		PC:             report.PC,
		Exccause:       report.Exccause,
		TaskName:       report.TaskName,
		Stack:          report.Stack,
		FwVersion:      report.FwVersion,
		RebootReason:   report.RebootReason,
		CreatedAt:      time.Now().UTC(),
	}
	// The created_at column doubles as "first seen" on the upsert path: a
	// retry must not move the original crash timestamp forward.
	return m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "record_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"reset_reason_str", "reboot_reason"}),
	}).Create(&row).Error
}

// decodeDiagReport parses a MSG_DIAG_REPORT frame. It never panics: every
// decode error is returned and every field is length/range checked.
//
// Wire layout (frozen, see frame.go / firmware crash_diag.c):
//
//	1  record_id        varint
//	2  report_type      varint (1=BOOT, 2=CRASH)
//	3  reset_reason     varint
//	4  uptime_sec       varint
//	5  reset_reason_str string
//	6  crc32            varint
//	7  core             varint
//	8  exception        varint
//	9  pc               varint
//	10 exccause         varint
//	11 task_name        bytes
//	12 stack            bytes (LE uint32 array, 16 words)
//	13 fw_version       string
//	14 reboot_reason    string
func decodeDiagReport(payload []byte) (diagReport, error) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return diagReport{}, err
	}

	var report diagReport
	var haveRecordID, haveReportType bool
	var reportType uint64

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return diagReport{}, err
		}

		switch field.FieldNum {
		case 1: // record_id
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("record_id wire type %d, want varint", field.WireType)
			}
			v := frame.GetUint64(field)
			if v > 0xFFFFFFFF {
				return diagReport{}, fmt.Errorf("record_id %d exceeds uint32", v)
			}
			report.RecordID = uint32(v)
			haveRecordID = true
		case 2: // report_type
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("report_type wire type %d, want varint", field.WireType)
			}
			reportType = frame.GetUint64(field)
			if reportType != diagReportTypeBoot && reportType != diagReportTypeCrash {
				return diagReport{}, fmt.Errorf("unknown report_type %d", reportType)
			}
			report.ReportType = uint8(reportType)
			haveReportType = true
		case 3: // reset_reason
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("reset_reason wire type %d, want varint", field.WireType)
			}
			report.ResetReason = int32(frame.GetUint64(field))
		case 4: // uptime_sec
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("uptime_sec wire type %d, want varint", field.WireType)
			}
			report.UptimeSec = frame.GetUint64(field)
		case 5: // reset_reason_str
			if field.WireType != frame.WireLengthDelimited {
				return diagReport{}, fmt.Errorf("reset_reason_str wire type %d, want length-delimited", field.WireType)
			}
			report.ResetReasonStr = frame.GetString(field)
		case 6: // crc32
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("crc32 wire type %d, want varint", field.WireType)
			}
			v := frame.GetUint64(field)
			if v > 0xFFFFFFFF {
				return diagReport{}, fmt.Errorf("crc32 %d exceeds uint32", v)
			}
			report.CRC32 = uint32(v)
		case 7: // core
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("core wire type %d, want varint", field.WireType)
			}
			v := frame.GetUint64(field)
			if v > 0xFF {
				return diagReport{}, fmt.Errorf("core %d exceeds uint8", v)
			}
			report.Core = uint8(v)
		case 8: // exception
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("exception wire type %d, want varint", field.WireType)
			}
			report.Exception = int32(frame.GetUint64(field))
		case 9: // pc
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("pc wire type %d, want varint", field.WireType)
			}
			v := frame.GetUint64(field)
			if v > 0xFFFFFFFF {
				return diagReport{}, fmt.Errorf("pc %d exceeds uint32", v)
			}
			report.PC = uint32(v)
		case 10: // exccause
			if field.WireType != frame.WireVarint {
				return diagReport{}, fmt.Errorf("exccause wire type %d, want varint", field.WireType)
			}
			report.Exccause = int32(frame.GetUint64(field))
		case 11: // task_name
			if field.WireType != frame.WireLengthDelimited {
				return diagReport{}, fmt.Errorf("task_name wire type %d, want length-delimited", field.WireType)
			}
			report.TaskName = frame.GetString(field)
		case 12: // stack
			if field.WireType != frame.WireLengthDelimited {
				return diagReport{}, fmt.Errorf("stack wire type %d, want length-delimited", field.WireType)
			}
			stack := frame.GetBytes(field)
			// Frozen wire rule: stack is a little-endian uint32 array, so its
			// byte length must be a multiple of 4 and at most 16 words (64 B).
			// An absent/short stack still carries the rest of the crash context
			// (PC/exception/core), so length 0 is not treated as malformed.
			if len(stack)%4 != 0 {
				return diagReport{}, fmt.Errorf("stack length %d is not a multiple of 4", len(stack))
			}
			if len(stack) > diagMaxStackBytes {
				return diagReport{}, fmt.Errorf("stack length %d exceeds %d bytes", len(stack), diagMaxStackBytes)
			}
			// Copy: the decoder slices the caller's buffer, which may be reused.
			report.Stack = append([]byte(nil), stack...)
		case 13: // fw_version
			if field.WireType != frame.WireLengthDelimited {
				return diagReport{}, fmt.Errorf("fw_version wire type %d, want length-delimited", field.WireType)
			}
			report.FwVersion = frame.GetString(field)
		case 14: // reboot_reason
			if field.WireType != frame.WireLengthDelimited {
				return diagReport{}, fmt.Errorf("reboot_reason wire type %d, want length-delimited", field.WireType)
			}
			report.RebootReason = frame.GetString(field)
		default:
			// Forward compatibility: ignore unknown fields rather than reject a
			// report the firmware may already be sending.
		}
	}

	if !haveReportType {
		return diagReport{}, fmt.Errorf("missing report_type")
	}
	if !haveRecordID && reportType == diagReportTypeCrash {
		return diagReport{}, fmt.Errorf("missing record_id for CRASH report")
	}
	return report, nil
}
