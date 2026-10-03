package nodemgr

import (
	"errors"
	"sync"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// =====================================================================
// MSG_DIAG_REPORT (0x1E) / MSG_DIAG_ACK (0x1F) server-side contract.
//
// The firmware keeps its NVS crash record until it receives
// MSG_DIAG_ACK(record_id, accepted=true). These tests pin the two
// observable halves of that contract: what gets persisted, and exactly
// when an ACK (and which accepted value) is published.
// =====================================================================

// diagRecordingPublisher records every published frame (unlike the shared
// mockMQTTPublisher, which keeps only the last one) so idempotent retries can
// be counted.
type diagRecordingPublisher struct {
	mu       sync.Mutex
	topics   []string
	payloads [][]byte
	err      error
}

func (p *diagRecordingPublisher) Publish(topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return p.err
}

func (p *diagRecordingPublisher) PublishQoS2(string, []byte) error     { return p.err }
func (p *diagRecordingPublisher) PublishRetained(string, []byte) error { return p.err }

func (p *diagRecordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.payloads)
}

func (p *diagRecordingPublisher) at(i int) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.payloads[i]
}

func newDiagTestManager(t *testing.T, db *gorm.DB, pub mqtt.Publisher) *Manager {
	t.Helper()
	return &Manager{db: db, mqtt: pub}
}

func openDiagTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.OpenTestDB(t)
	if err := db.AutoMigrate(&models.DeviceDiagReport{}); err != nil {
		t.Fatalf("automigrate device_diag_reports: %v", err)
	}
	return db
}

// leStack builds the little-endian uint32 stack window the firmware sends.
func leStack(words ...uint32) []byte {
	out := make([]byte, 0, len(words)*4)
	for _, w := range words {
		out = append(out, byte(w), byte(w>>8), byte(w>>16), byte(w>>24))
	}
	return out
}

func buildDiagCrashFrame(recordID uint32, stack []byte) []byte {
	enc := frame.NewEncoder(frame.MsgDiagReport)
	enc.EncodeVarint(1, uint64(recordID))
	enc.EncodeVarint(2, diagReportTypeCrash)
	enc.EncodeVarint(3, 4) // reset_reason
	enc.EncodeVarint(4, 123)
	enc.EncodeString(5, "PANIC")
	enc.EncodeVarint(6, 0xDEADBEEF)
	enc.EncodeVarint(7, 1)
	enc.EncodeVarint(8, 6)
	enc.EncodeVarint(9, 0x40081234)
	enc.EncodeVarint(10, 28)
	enc.EncodeBytes(11, []byte("main"))
	enc.EncodeBytes(12, stack)
	enc.EncodeString(13, "1.2.3")
	enc.EncodeString(14, "esp_restart in ota")
	return enc.Bytes()
}

func buildDiagBootFrame() []byte {
	enc := frame.NewEncoder(frame.MsgDiagReport)
	enc.EncodeVarint(1, 0) // BOOT reports carry record_id=0
	enc.EncodeVarint(2, diagReportTypeBoot)
	enc.EncodeVarint(3, 1)
	enc.EncodeVarint(4, 55)
	enc.EncodeString(5, "POWERON")
	enc.EncodeVarint(6, 0)
	enc.EncodeString(13, "1.2.3")
	enc.EncodeString(14, "")
	return enc.Bytes()
}

// decodeDiagAck decodes an ACK frame and returns (record_id, accepted).
func decodeDiagAck(t *testing.T, payload []byte) (uint32, bool) {
	t.Helper()
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatalf("ack decode: %v", err)
	}
	if dec.MsgType() != frame.MsgDiagAck {
		t.Fatalf("ack msg type = 0x%02X, want 0x%02X", dec.MsgType(), frame.MsgDiagAck)
	}
	var id uint32
	var accepted bool
	var haveID, haveAcc bool
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			t.Fatalf("ack field: %v", err)
		}
		switch field.FieldNum {
		case 1:
			id = uint32(frame.GetUint64(field))
			haveID = true
		case 2:
			accepted = frame.GetBool(field)
			haveAcc = true
		}
	}
	if !haveID || !haveAcc {
		t.Fatalf("ack missing fields: haveID=%v haveAccepted=%v", haveID, haveAcc)
	}
	return id, accepted
}

// ---------------------------------------------------------------------
// decode: valid frames
// ---------------------------------------------------------------------

func TestDecodeDiagReport_ValidCrash(t *testing.T) {
	stack := leStack(0x11111111, 0x22222222, 0x33333333, 0x44444444)
	payload := buildDiagCrashFrame(0xA1B2C3D4, stack)

	report, err := decodeDiagReport(payload)
	if err != nil {
		t.Fatalf("decode valid crash: %v", err)
	}
	if report.RecordID != 0xA1B2C3D4 {
		t.Errorf("RecordID = %08X, want A1B2C3D4", report.RecordID)
	}
	if report.ReportType != diagReportTypeCrash {
		t.Errorf("ReportType = %d, want %d", report.ReportType, diagReportTypeCrash)
	}
	if report.ResetReason != 4 {
		t.Errorf("ResetReason = %d, want 4", report.ResetReason)
	}
	if report.UptimeSec != 123 {
		t.Errorf("UptimeSec = %d, want 123", report.UptimeSec)
	}
	if report.ResetReasonStr != "PANIC" {
		t.Errorf("ResetReasonStr = %q, want PANIC", report.ResetReasonStr)
	}
	if report.CRC32 != 0xDEADBEEF {
		t.Errorf("CRC32 = %08X, want DEADBEEF", report.CRC32)
	}
	if report.Core != 1 {
		t.Errorf("Core = %d, want 1", report.Core)
	}
	if report.Exception != 6 {
		t.Errorf("Exception = %d, want 6", report.Exception)
	}
	if report.PC != 0x40081234 {
		t.Errorf("PC = %08X, want 40081234", report.PC)
	}
	if report.Exccause != 28 {
		t.Errorf("Exccause = %d, want 28", report.Exccause)
	}
	if report.TaskName != "main" {
		t.Errorf("TaskName = %q, want main", report.TaskName)
	}
	if string(report.Stack) != string(stack) {
		t.Errorf("Stack = %X, want %X", report.Stack, stack)
	}
	if report.FwVersion != "1.2.3" {
		t.Errorf("FwVersion = %q, want 1.2.3", report.FwVersion)
	}
	if report.RebootReason != "esp_restart in ota" {
		t.Errorf("RebootReason = %q, want esp_restart in ota", report.RebootReason)
	}
}

func TestDecodeDiagReport_ValidBoot(t *testing.T) {
	report, err := decodeDiagReport(buildDiagBootFrame())
	if err != nil {
		t.Fatalf("decode valid boot: %v", err)
	}
	if report.RecordID != 0 {
		t.Errorf("RecordID = %d, want 0", report.RecordID)
	}
	if report.ReportType != diagReportTypeBoot {
		t.Errorf("ReportType = %d, want %d", report.ReportType, diagReportTypeBoot)
	}
	if report.ResetReason != 1 {
		t.Errorf("ResetReason = %d, want 1", report.ResetReason)
	}
	if report.UptimeSec != 55 {
		t.Errorf("UptimeSec = %d, want 55", report.UptimeSec)
	}
	if report.ResetReasonStr != "POWERON" {
		t.Errorf("ResetReasonStr = %q, want POWERON", report.ResetReasonStr)
	}
	if report.CRC32 != 0 {
		t.Errorf("CRC32 = %d, want 0", report.CRC32)
	}
	if report.FwVersion != "1.2.3" {
		t.Errorf("FwVersion = %q, want 1.2.3", report.FwVersion)
	}
	if report.RebootReason != "" {
		t.Errorf("RebootReason = %q, want empty", report.RebootReason)
	}
}

// ---------------------------------------------------------------------
// decode: malformed input must error (never panic), and the handler must
// drop it without persisting or ACKing.
// ---------------------------------------------------------------------

func TestDecodeDiagReport_Malformed(t *testing.T) {
	validStack := leStack(1, 2, 3, 4)

	cases := []struct {
		name    string
		payload []byte
	}{
		{
			// field 12 tag + declared length 16 but zero data bytes.
			name:    "truncated_stack_payload",
			payload: []byte{frame.MsgDiagReport, 0x08, 0x01, 0x10, 0x02, 0x62, 0x10},
		},
		{
			name:    "empty_payload",
			payload: []byte{},
		},
		{
			name: "stack_length_not_multiple_of_4",
			payload: buildDiagCrashFrame(0x11, func() []byte {
				s := leStack(1, 2, 3, 4)
				return s[:len(s)-2] // 14 bytes
			}()),
		},
		{
			name:    "stack_exceeds_64_bytes",
			payload: buildDiagCrashFrame(0x12, make([]byte, diagMaxStackBytes+4)),
		},
		{
			name: "unknown_report_type",
			payload: func() []byte {
				enc := frame.NewEncoder(frame.MsgDiagReport)
				enc.EncodeVarint(1, 7)
				enc.EncodeVarint(2, 99)
				return enc.Bytes()
			}(),
		},
		{
			name:    "crash_without_record_id",
			payload: []byte{frame.MsgDiagReport, 0x10, 0x02},
		},
		{
			name: "wrong_wire_type_for_record_id",
			payload: func() []byte {
				enc := frame.NewEncoder(frame.MsgDiagReport)
				enc.EncodeString(1, "not-a-varint")
				enc.EncodeVarint(2, diagReportTypeCrash)
				enc.EncodeBytes(12, validStack)
				return enc.Bytes()
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeDiagReport(tc.payload); err == nil {
				t.Fatalf("decodeDiagReport accepted malformed payload %X", tc.payload)
			}

			// Handler path: no persist, no ACK, no panic.
			db := openDiagTestDB(t)
			pub := &diagRecordingPublisher{}
			mgr := newDiagTestManager(t, db, pub)
			mgr.handleDiagReport("diag-dev", tc.payload)

			var rows int64
			if err := db.Model(&models.DeviceDiagReport{}).Count(&rows).Error; err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if rows != 0 {
				t.Fatalf("malformed report persisted %d row(s), want 0", rows)
			}
			if pub.count() != 0 {
				t.Fatalf("malformed report emitted %d ACK(s), want 0", pub.count())
			}
		})
	}
}

// TestDecodeDiagReport_NeverPanicsOnEveryPrefix fuzzes truncation at every
// byte boundary of a valid CRASH frame: the decoder must return, not panic.
func TestDecodeDiagReport_NeverPanicsOnEveryPrefix(t *testing.T) {
	full := buildDiagCrashFrame(0xABCD, leStack(1, 2, 3, 4))
	for i := 0; i <= len(full); i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodeDiagReport panicked on prefix %d: %v", i, r)
				}
			}()
			_, _ = decodeDiagReport(full[:i])
		}()
	}
}

// TestDecodeDiagReport_CrashRecordIDZeroRejectedByHandler pins the rule that a
// CRASH report with record_id=0 is a protocol violation: dropped, not stored,
// not ACKed (there is no NVS slot to correlate with).
func TestDecodeDiagReport_CrashRecordIDZeroRejectedByHandler(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	// A frame that decodes cleanly but violates the CRASH record_id rule.
	enc := frame.NewEncoder(frame.MsgDiagReport)
	enc.EncodeVarint(1, 0)
	enc.EncodeVarint(2, diagReportTypeCrash)
	enc.EncodeBytes(12, leStack(1, 2, 3, 4))
	payload := enc.Bytes()

	if _, err := decodeDiagReport(payload); err != nil {
		t.Fatalf("frame should decode (handler enforces record_id rule): %v", err)
	}
	mgr.handleDiagReport("diag-dev", payload)

	var rows int64
	db.Model(&models.DeviceDiagReport{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("CRASH record_id=0 persisted %d row(s), want 0", rows)
	}
	if pub.count() != 0 {
		t.Fatalf("CRASH record_id=0 emitted %d ACK(s), want 0", pub.count())
	}
}

// ---------------------------------------------------------------------
// persist + ACK contract
// ---------------------------------------------------------------------

func TestHandleDiagReport_CrashPersistsAndAcksAccepted(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	stack := leStack(0xAAAA, 0xBBBB)
	mgr.handleDiagReport("diag-dev", buildDiagCrashFrame(0x1234, stack))

	var row models.DeviceDiagReport
	if err := db.Where("device_id = ? AND record_id = ?", "diag-dev", 0x1234).First(&row).Error; err != nil {
		t.Fatalf("crash report not persisted: %v", err)
	}
	if row.ReportType != diagReportTypeCrash || row.PC != 0x40081234 || row.ResetReasonStr != "PANIC" {
		t.Fatalf("persisted row wrong: %+v", row)
	}
	if string(row.Stack) != string(stack) {
		t.Fatalf("persisted stack = %X, want %X", row.Stack, stack)
	}

	if pub.count() != 1 {
		t.Fatalf("ACK count = %d, want 1", pub.count())
	}
	id, accepted := decodeDiagAck(t, pub.at(0))
	if id != 0x1234 || !accepted {
		t.Fatalf("ACK = (id=%08X, accepted=%v), want (1234, true)", id, accepted)
	}
}

func TestHandleDiagReport_AcksRejectedWhenPersistFails(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	// Force the device_diag_reports INSERT to fail.
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_diag_create", func(tx *gorm.DB) {
		if tx.Statement.Table == "device_diag_reports" {
			tx.AddError(errors.New("injected diag insert failure"))
		}
	}); err != nil {
		t.Fatalf("register create-failure callback: %v", err)
	}

	mgr.handleDiagReport("diag-dev", buildDiagCrashFrame(0x55, leStack(1, 2, 3, 4)))

	var rows int64
	db.Model(&models.DeviceDiagReport{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("persist-failure path stored %d row(s), want 0", rows)
	}
	if pub.count() != 1 {
		t.Fatalf("ACK count = %d, want 1 (accepted=false so device keeps NVS)", pub.count())
	}
	id, accepted := decodeDiagAck(t, pub.at(0))
	if id != 0x55 || accepted {
		t.Fatalf("ACK = (id=%08X, accepted=%v), want (55, false)", id, accepted)
	}
}

func TestHandleDiagReport_DuplicateRecordIDIsIdempotent(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	payload := buildDiagCrashFrame(0x77, leStack(1, 2, 3, 4))
	mgr.handleDiagReport("diag-dev", payload)
	mgr.handleDiagReport("diag-dev", payload) // device retried after a lost ACK

	var rows int64
	db.Model(&models.DeviceDiagReport{}).Where("device_id = ? AND record_id = ?", "diag-dev", 0x77).Count(&rows)
	if rows != 1 {
		t.Fatalf("duplicate record_id produced %d rows, want exactly 1", rows)
	}
	if pub.count() != 2 {
		t.Fatalf("ACK count = %d, want 2 (each retry must be ACKed)", pub.count())
	}
	for i := 0; i < 2; i++ {
		id, accepted := decodeDiagAck(t, pub.at(i))
		if id != 0x77 || !accepted {
			t.Fatalf("ACK[%d] = (id=%08X, accepted=%v), want (77, true)", i, id, accepted)
		}
	}
}

func TestHandleDiagReport_BootPersistsWithoutAck(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	mgr.handleDiagReport("diag-dev", buildDiagBootFrame())

	var rows int64
	db.Model(&models.DeviceDiagReport{}).Where("device_id = ?", "diag-dev").Count(&rows)
	if rows != 1 {
		t.Fatalf("BOOT report persisted %d row(s), want 1", rows)
	}
	var row models.DeviceDiagReport
	if err := db.Where("device_id = ?", "diag-dev").First(&row).Error; err != nil {
		t.Fatalf("load boot row: %v", err)
	}
	if row.ReportType != diagReportTypeBoot || row.RecordID != 0 {
		t.Fatalf("boot row wrong: %+v", row)
	}
	if pub.count() != 0 {
		t.Fatalf("BOOT report emitted %d ACK(s), want 0 (record_id=0 occupies no NVS)", pub.count())
	}
}

// TestHandleMessage_DispatchesDiagReport pins the manager wiring: msg type
// 0x1E must reach handleDiagReport through the production dispatch switch.
func TestHandleMessage_DispatchesDiagReport(t *testing.T) {
	db := openDiagTestDB(t)
	pub := &diagRecordingPublisher{}
	mgr := newDiagTestManager(t, db, pub)

	mgr.HandleMessage("nodes/diag-dev/up", buildDiagCrashFrame(0x99, leStack(1, 2, 3, 4)))

	var rows int64
	db.Model(&models.DeviceDiagReport{}).Where("device_id = ?", "diag-dev").Count(&rows)
	if rows != 1 {
		t.Fatalf("HandleMessage dispatched 0x1E to %d row(s), want 1", rows)
	}
	if pub.count() != 1 {
		t.Fatalf("HandleMessage ACK count = %d, want 1", pub.count())
	}
	if _, accepted := decodeDiagAck(t, pub.at(0)); !accepted {
		t.Fatalf("HandleMessage ACK accepted = false, want true")
	}
}
