package drivers

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	"ehome/backend/pkg/logger"
)

func init() {
	logger.Init("warn")
}

// ============================================================================
// 1. TestChecksum_8KnownFrames — 8 command frame checksum verification
// ============================================================================

func TestChecksum_8KnownFrames(t *testing.T) {
	// All 8 frames from the protocol docs verification table.
	tests := []struct {
		name       string
		frame      []byte
		wantSum    uint16
		checkRange func([]byte) uint16 // which bytes to checksum
	}{
		{"0x03 read basic info", []byte{0xDD, 0xA5, 0x03, 0x00, 0xFF, 0xFD, 0x77}, 0xFFFD, func(f []byte) uint16 { return jiabaidaChecksum(f[2:4]) }},
		{"0x04 read cell voltage", []byte{0xDD, 0xA5, 0x04, 0x00, 0xFF, 0xFC, 0x77}, 0xFFFC, func(f []byte) uint16 { return jiabaidaChecksum(f[2:4]) }},
		{"0x05 read hardware ver", []byte{0xDD, 0xA5, 0x05, 0x00, 0xFF, 0xFB, 0x77}, 0xFFFB, func(f []byte) uint16 { return jiabaidaChecksum(f[2:4]) }},
		{"0x0F read comprehensive", []byte{0xDD, 0xA5, 0x0F, 0x00, 0xFF, 0xF1, 0x77}, 0xFFF1, func(f []byte) uint16 { return jiabaidaChecksum(f[2:4]) }},
		{"0xAA read protection count", []byte{0xDD, 0xA5, 0xAA, 0x00, 0xFF, 0x56, 0x77}, 0xFF56, func(f []byte) uint16 { return jiabaidaChecksum(f[2:4]) }},
		{"0xE1 close dis MOS", []byte{0xDD, 0x5A, 0xE1, 0x02, 0x00, 0x02, 0xFF, 0x1B, 0x77}, 0xFF1B, func(f []byte) uint16 { return jiabaidaChecksum(f[2:6]) }},
		{"0xE1 close chg MOS", []byte{0xDD, 0x5A, 0xE1, 0x02, 0x00, 0x01, 0xFF, 0x1C, 0x77}, 0xFF1C, func(f []byte) uint16 { return jiabaidaChecksum(f[2:6]) }},
		{"0xE1 release MOS", []byte{0xDD, 0x5A, 0xE1, 0x02, 0x00, 0x00, 0xFF, 0x1D, 0x77}, 0xFF1D, func(f []byte) uint16 { return jiabaidaChecksum(f[2:6]) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.checkRange(tt.frame)
			if got != tt.wantSum {
				t.Errorf("checksum: got 0x%04X, want 0x%04X", got, tt.wantSum)
			}
			// Also verify via verifyJiabaidaChecksum
			if !verifyJiabaidaChecksum(tt.frame) {
				t.Errorf("verifyJiabaidaChecksum returned false for valid frame")
			}
		})
	}
}

// ============================================================================
// 2. TestChecksum_SendFrame — send frame checksum range (CMD+LEN+DATA)
// ============================================================================

func TestChecksum_SendFrame(t *testing.T) {
	// Test the send frame checksum range explicitly.
	// Frame: 0xDD | 0xA5 | CMD(0x03) | LEN(0x02) | DATA(0x01,0x02) | CHKSUM_H | CHKSUM_L | 0x77
	// CMD+LEN+DATA = 0x03+0x02+0x01+0x02 = 0x08 → ^0x08+1 = 0xFFF8
	expectedSum := uint16(^uint16(0x03+0x02+0x01+0x02) + 1)
	frame := []byte{0xDD, 0xA5, 0x03, 0x02, 0x01, 0x02}
	frame = append(frame, byte(expectedSum>>8), byte(expectedSum&0xFF), 0x77)

	if !verifyJiabaidaChecksum(frame) {
		t.Error("send frame checksum verification failed")
	}

	// Break the checksum
	broken := make([]byte, len(frame))
	copy(broken, frame)
	broken[len(broken)-3]++ // flip a checksum byte
	if verifyJiabaidaChecksum(broken) {
		t.Error("verifyJiabaidaChecksum should fail for broken checksum")
	}
}

// ============================================================================
// 3. TestChecksum_RespFrame — response frame checksum range (LEN+DATA)
// ============================================================================

func TestChecksum_RespFrame(t *testing.T) {
	// Response: 0xDD | CMD(0x03) | STATUS(0x00) | LEN(0x03) | DATA(0x01,0x02,0x03) | CHKSUM
	// LEN+DATA = 0x03+0x01+0x02+0x03 = 0x09 → ^0x09+1 = 0xFFF7
	expectedSum := uint16(^uint16(0x03+0x01+0x02+0x03) + 1)
	frame := []byte{0xDD, 0x03, 0x00, 0x03, 0x01, 0x02, 0x03}
	frame = append(frame, byte(expectedSum>>8), byte(expectedSum&0xFF), 0x77)

	if !verifyJiabaidaChecksum(frame) {
		t.Error("response frame checksum verification failed")
	}
}

// ============================================================================
// 4. TestParse0x03_KnownResponse — known 0x03 frame field validation
// ============================================================================

func TestParse0x03_KnownResponse(t *testing.T) {
	d := &JiabaidaBMSDriver{}

	// Build a known 0x03 response:
	// Total voltage: 52130mV → 521.30V (raw: 0xCBB2)
	// Current: -12340mA → -123.40A (raw: 0xCFCC as int16)
	// Remaining capacity: 85000mAh → 850.00Ah (raw: 0x14C08 — wait, 85000=0x14C08, but uint16 max=65535)
	// Let's use realistic values:
	// TotalV=52130mV(0xCBB2), Current=5000mA(0x1388), RemCap=50000mAh(0xC350), NomCap=60000mAh(0xEA60)
	// Cycle=100(0x0064), Protect=0x0000, Version=1, RSOC=85, FET=0x00, CellCount=16, NTCCount=3
	// Temp1=2982(0x0BA6) → 298.2K-273.15=25.05°C
	// Temp2=3032(0x0BD8) → 303.2K-273.15=30.05°C
	// Temp3=2932(0x0B74) → 293.2K-273.15=20.05°C

	data := make([]byte, 29)                     // 23 base + 3×2 temps
	binary.BigEndian.PutUint16(data[0:2], 52130) // total voltage: 10mV
	binary.BigEndian.PutUint16(data[2:4], 5000)  // current: int16 +5000 → 50.00A
	binary.BigEndian.PutUint16(data[4:6], 50000) // remaining capacity: 10mAh
	binary.BigEndian.PutUint16(data[6:8], 60000) // nominal capacity: 10mAh
	binary.BigEndian.PutUint16(data[8:10], 100)  // cycle count
	// Skip [10:16] date + balance
	binary.BigEndian.PutUint16(data[16:18], 0x0001) // protection: bit0 = cell OV
	data[18] = 1                                    // software version
	data[19] = 85                                   // RSOC 85%
	data[20] = 0                                    // FET status
	data[21] = 16                                   // cell count
	data[22] = 3                                    // NTC count
	binary.BigEndian.PutUint16(data[23:25], 0x0BA6) // T1: 2982
	binary.BigEndian.PutUint16(data[25:27], 0x0BD8) // T2: 3032
	binary.BigEndian.PutUint16(data[27:29], 0x0B74) // T3: 2932

	result, err := d.parse0x03(data)
	if err != nil {
		t.Fatalf("parse0x03: %v", err)
	}

	// Verify key fields
	checks := map[string]float64{
		"total_voltage":      521.30,
		"current":            50.00,
		"remaining_capacity": 500.00,
		"nominal_capacity":   600.00,
		"cycle_count":        100,
		"rsoc":               85,
		"protection_status":  1,
		"cell_count":         16,
	}
	for _, s := range result {
		if expected, ok := checks[s.Name]; ok {
			if s.Value != expected {
				t.Errorf("%s: got %f, want %f", s.Name, s.Value, expected)
			}
			delete(checks, s.Name)
		}
	}
	for name := range checks {
		t.Errorf("missing field: %s", name)
	}

	// Verify temperatures
	tempFound := 0
	for _, s := range result {
		if s.Name == "temperature_1" {
			tempFound++
			if s.Value < 24.9 || s.Value > 25.2 {
				t.Errorf("temperature_1: got %f, want ~25.05", s.Value)
			}
		}
		if s.Name == "temperature_2" {
			tempFound++
		}
		if s.Name == "temperature_3" {
			tempFound++
		}
	}
	if tempFound != 3 {
		t.Errorf("expected 3 temperature sensors, found %d", tempFound)
	}
}

// ============================================================================
// 5. TestParse0x04_15Cells — 15 cell voltages + max/min/avg
// ============================================================================

func TestParse0x04_15Cells(t *testing.T) {
	d := &JiabaidaBMSDriver{}

	// 15 cells: 12 at 3250mV, 1 at 3280, 1 at 3220, 1 at 3260
	data := make([]byte, 30)
	voltages := []uint16{3250, 3250, 3250, 3250, 3250, 3250, 3250, 3250, 3250, 3250, 3250, 3250, 3280, 3220, 3260}
	for i, v := range voltages {
		binary.BigEndian.PutUint16(data[i*2:], v)
	}

	result, err := d.parse0x04(data)
	if err != nil {
		t.Fatalf("parse0x04: %v", err)
	}

	// 15 cell voltages + 3 aggregate = 18
	if len(result) != 18 {
		t.Errorf("expected 18 entries (15 cells + 3 stats), got %d", len(result))
	}

	// Find aggregates
	var maxV, minV, avgV float64
	for _, s := range result {
		switch s.Name {
		case "cell_voltage_max":
			maxV = s.Value
		case "cell_voltage_min":
			minV = s.Value
		case "cell_voltage_avg":
			avgV = s.Value
		}
	}
	if maxV != 3.28 {
		t.Errorf("max: got %f, want 3.28", maxV)
	}
	if minV != 3.22 {
		t.Errorf("min: got %f, want 3.22", minV)
	}
	// avg: (12×3.25 + 3.28 + 3.22 + 3.26)/15 = 48.76/15 = 3.25066...
	if avgV < 3.250 || avgV > 3.251 {
		t.Errorf("avg: got %f, want ~3.2507", avgV)
	}
}

// ============================================================================
// 6. TestParse0x04_Empty — empty data error handling
// ============================================================================

func TestParse0x04_Empty(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	_, err := d.parse0x04([]byte{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

// ============================================================================
// 7. TestParse0xAA_ProtectionHistory — 12 protection counts
// ============================================================================

func TestParse0xAA_ProtectionHistory(t *testing.T) {
	d := &JiabaidaBMSDriver{}

	// 12 × uint16 counts
	data := make([]byte, 24)
	for i := 0; i < 12; i++ {
		binary.BigEndian.PutUint16(data[i*2:], uint16(i+1))
	}

	result, err := d.parse0xAA(data)
	if err != nil {
		t.Fatalf("parse0xAA: %v", err)
	}
	if len(result) != 12 {
		t.Errorf("expected 12 protection counts, got %d", len(result))
	}
	expectedNames := []string{
		"short_circuit_count", "charge_overcurrent_count", "discharge_overcurrent_count",
		"cell_overvoltage_count", "cell_undervoltage_count",
		"charge_overtemp_count", "charge_undertemp_count",
		"discharge_overtemp_count", "discharge_undertemp_count",
		"pack_overvoltage_count", "pack_undervoltage_count", "restart_count",
	}
	for i, s := range result {
		if s.Name != expectedNames[i] {
			t.Errorf("result[%d] name: got %s, want %s", i, s.Name, expectedNames[i])
		}
		if s.Value != float64(i+1) {
			t.Errorf("result[%d] value: got %f, want %f", i, s.Value, float64(i+1))
		}
	}
}

// ============================================================================
// 8. TestTemperatureConversion — 0x0B76 → 20.25°C
// ============================================================================

func TestTemperatureConversion(t *testing.T) {
	// 0x0B76 = 2934 → 2934/10 - 273.15 = 293.4 - 273.15 = 20.25°C
	got := jiabaidaTemperature(0x0B76)
	want := 20.25
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("temperature conversion: got %f, want %f", got, want)
	}

	// Test freezing: 2731 = 273.1K → 273.15-273.1 = -0.05°C
	got2 := jiabaidaTemperature(2731)
	if got2 > 0 {
		t.Errorf("freezing temp should be negative, got %f", got2)
	}
}

// ============================================================================
// 9. TestInvalidFrame_Short — frame too short → ErrInvalidFrameStart
// ============================================================================

func TestInvalidFrame_Short(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	_, err := d.ParseData([]byte{0xDD, 0x03})
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrInvalidFrameStart {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrInvalidFrameStart)
	}
}

// ============================================================================
// 10. TestInvalidFrame_BadChecksum — wrong checksum → ErrChecksumMismatch
// ============================================================================

func TestInvalidFrame_BadChecksum(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// Valid frame structure but wrong checksum
	// 0xDD | 0x03 | 0x00 | 0x02 | 0xAA 0xBB | 0x00 0x00 | 0x77
	frame := []byte{0xDD, 0x03, 0x00, 0x02, 0xAA, 0xBB, 0x00, 0x00, 0x77}
	_, err := d.ParseData(frame)
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrChecksumMismatch {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrChecksumMismatch)
	}
}

// ============================================================================
// 11. TestInvalidFrame_BadStopByte — wrong stop byte → ErrInvalidStopByte
// ============================================================================

func TestInvalidFrame_BadStopByte(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// Build a valid frame then corrupt the stop byte
	// 0xDD | 0x03 | 0x00 | 0x02 | 0x00 0x00 → checksum over LEN+DATA=0x02+0x00+0x00=0x02 → ^0x02+1=0xFFFE
	frame := []byte{0xDD, 0x03, 0x00, 0x02, 0x00, 0x00, 0xFF, 0xFE, 0x88} // stop byte=0x88 (bad)
	_, err := d.ParseData(frame)
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrInvalidStopByte {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrInvalidStopByte)
	}
}

// ============================================================================
// 12. TestParseData_UnknownCmd — unknown command code → ErrUnknownCommand
// ============================================================================

func TestParseData_UnknownCmd(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// Valid frame with unknown CMD=0x99
	// Checksum over LEN+DATA: 0x02+0x00+0x00=0x02 → ^0x02+1 = 0xFFFE
	frame := []byte{0xDD, 0x99, 0x00, 0x02, 0x00, 0x00, 0xFF, 0xFE, 0x77}
	_, err := d.ParseData(frame)
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrUnknownCommand {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrUnknownCommand)
	}
}

// ============================================================================
// 13. TestParseData_BMSStatusError — STATUS=0x80 → ErrBMSStatusError
// ============================================================================

func TestParseData_BMSStatusError(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// Response with STATUS=0x80 (error indicator)
	// Frame: 0xDD | 0x80/0x81/0x82 | LEN(0) | CHECKSUM_H | CHECKSUM_L | 0x77
	// LEN=0x00 → DATA empty → LEN+DATA = 0x00 → ^0x00+1 = 0x0000
	frame := []byte{0xDD, 0x80, 0x00, 0x00, 0x00, 0x77}
	_, err := d.ParseData(frame)
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrBMSStatusError {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrBMSStatusError)
	}
}

func TestParseData_BMSStatusErrorWithCallback(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// The V19 source permits a four-byte callback after 0x77. It must not make
	// an otherwise valid error response look malformed.
	frame := []byte{0xDD, 0x81, 0x00, 0x00, 0x00, 0x77, 0x12, 0x34, 0x56, 0x78}
	_, err := d.ParseData(frame)
	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Code != ErrBMSStatusError {
		t.Fatalf("ParseData(callback error) = %v, want ErrBMSStatusError", err)
	}
}

func TestParseData_BMSStatusErrorRejectsBadChecksum(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	_, err := d.ParseData([]byte{0xDD, 0x82, 0x00, 0x00, 0x01, 0x77})
	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Code != ErrChecksumMismatch {
		t.Fatalf("ParseData(bad error checksum) = %v, want ErrChecksumMismatch", err)
	}
}

// ============================================================================
// 14. TestCurrentSign — negative current (int16) parsed correctly
// ============================================================================

func TestCurrentSign(t *testing.T) {
	d := &JiabaidaBMSDriver{}

	// Build a 0x03 frame with negative current: -5000mA = -50.00A
	// int16 -5000 = 0xEC78
	data := make([]byte, 23)
	binary.BigEndian.PutUint16(data[0:2], 52130) // total voltage
	// int16 -5000 = 0xEC78 as bytes
	binary.BigEndian.PutUint16(data[2:4], 0xEC78) // current (int16 -5000)
	binary.BigEndian.PutUint16(data[4:6], 50000)  // remaining
	binary.BigEndian.PutUint16(data[6:8], 60000)  // nominal
	// cycle, date, balance... zeroed
	data[19] = 85
	data[21] = 16
	data[22] = 0 // no NTC

	result, err := d.parse0x03(data)
	if err != nil {
		t.Fatalf("parse0x03: %v", err)
	}

	for _, s := range result {
		if s.Name == "current" {
			if s.Value >= 0 {
				t.Errorf("current should be negative, got %f", s.Value)
			}
			if s.Value < -51 || s.Value > -49 {
				t.Errorf("current value: got %f, want ~ -50.0", s.Value)
			}
		}
	}
}

// ============================================================================
// Additional: ParseData full frame integration tests
// ============================================================================

func TestJiabaidaDriver_ParseData_Valid0x03Response(t *testing.T) {
	d := &JiabaidaBMSDriver{}

	// Build a complete 0x03 response frame
	data := make([]byte, 29)
	binary.BigEndian.PutUint16(data[0:2], 52130)
	binary.BigEndian.PutUint16(data[2:4], 5000)
	binary.BigEndian.PutUint16(data[4:6], 50000)
	binary.BigEndian.PutUint16(data[6:8], 60000)
	binary.BigEndian.PutUint16(data[8:10], 100)
	binary.BigEndian.PutUint16(data[16:18], 0x0001)
	data[18] = 1
	data[19] = 85
	data[20] = 0
	data[21] = 16
	data[22] = 3
	binary.BigEndian.PutUint16(data[23:25], 0x0BA6)
	binary.BigEndian.PutUint16(data[25:27], 0x0BD8)
	binary.BigEndian.PutUint16(data[27:29], 0x0B74)

	// Build full frame: 0xDD | 0x03 | 0x00 | LEN(29) | data... | checksum | 0x77
	frame := []byte{0xDD, 0x03, 0x00, byte(len(data))}
	frame = append(frame, data...)

	// Checksum over LEN+DATA: 29 + sum(data)
	checksumData := append([]byte{byte(len(data))}, data...)
	cs := jiabaidaChecksum(checksumData)
	frame = append(frame, byte(cs>>8), byte(cs&0xFF), 0x77)

	result, err := d.ParseData(frame)
	if err != nil {
		t.Fatalf("ParseData full frame: %v", err)
	}
	if len(result) < 10 {
		t.Errorf("expected at least 10 sensors, got %d: %v", len(result), result)
	}
}

func TestJiabaidaDriver_Metadata(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	if d.DeviceType() != "jiabaida_bms" {
		t.Errorf("DeviceType: got %s, want jiabaida_bms", d.DeviceType())
	}
	if d.DeviceName() != "嘉佰达 BMS 电池管理系统" {
		t.Errorf("DeviceName: got %s", d.DeviceName())
	}
	if d.OEM() != "嘉佰达" {
		t.Errorf("OEM: got %s", d.OEM())
	}
	if d.Category() != "BMS" {
		t.Errorf("Category: got %s", d.Category())
	}
	ht := d.HardwareTypes()
	if len(ht) != 1 || ht[0] != "uart" {
		t.Errorf("HardwareTypes: got %v", ht)
	}
	defs := d.GetSensorDefinitions()
	if len(defs) != 12 {
		t.Errorf("GetSensorDefinitions: got %d, want 12", len(defs))
	}
}

func TestJiabaidaPublicTemplatesAreReadOnly(t *testing.T) {
	templates := (&JiabaidaBMSDriver{}).GetCommandTemplates()
	if len(templates) != 5 {
		t.Fatalf("GetCommandTemplates returned %d templates, want 5 documented reads", len(templates))
	}
	for _, template := range templates {
		if template.Type != "read" || !template.Schedulable {
			t.Fatalf("unsafe public BMS template exposed: %+v", template)
		}
		if template.CmdByte == 0xE1 || template.WriteData == "DD5AE1020002FF1B77" || template.WriteData == "DD5AE1020001FF1C77" || template.WriteData == "DD5AE1020000FF1D77" {
			t.Fatalf("legacy MOS write template leaked: %+v", template)
		}
	}
}

func TestJiabaidaControlActionsAreDisabledReads(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	var _ ControlActionProvider = d
	var _ ControlActionVerifier = d
	actions := d.ControlActions()
	if len(actions) < 8 {
		t.Fatalf("got %d control actions, want reads plus guarded operations", len(actions))
	}
	want := map[string][]byte{
		"read_basic_info":       {0xDD, 0xA5, 0x03, 0x00, 0xFF, 0xFD, 0x77},
		"read_cell_voltage":     {0xDD, 0xA5, 0x04, 0x00, 0xFF, 0xFC, 0x77},
		"read_hardware_version": {0xDD, 0xA5, 0x05, 0x00, 0xFF, 0xFB, 0x77},
		"read_comprehensive":    {0xDD, 0xA5, 0x0F, 0x00, 0xFF, 0xF1, 0x77},
		"read_protection_count": {0xDD, 0xA5, 0xAA, 0x00, 0xFF, 0x56, 0x77},
	}
	for _, action := range actions {
		if action.ID == "set_mos_policy" || action.ID == "read_protection_parameters" || action.ID == "read_system_parameters" || action.ID == "bms_restart" {
			if action.Enabled || action.AvailabilityCode == "" {
				t.Fatalf("guarded action became available: %+v", action)
			}
			continue
		}
		frame, ok := want[action.ID]
		if !ok {
			t.Fatalf("unexpected action %q", action.ID)
		}
		if action.Enabled || action.Semantics != "read" || action.Risk != "low" || string(action.TXData) != string(frame) || action.ReadSize == 0 || action.RXTimeoutMS == 0 {
			t.Fatalf("unsafe or malformed action %+v", action)
		}
		delete(want, action.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing actions: %v", want)
	}
}

func TestJiabaidaMOSPolicyCompilerGoldenVector(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	step, err := d.CompileControlAction("set_mos_policy", json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`))
	if err != nil {
		t.Fatalf("CompileControlAction error = %v", err)
	}
	// DD 5A E1 02 00 01 FF 1C 77; checksum is two's complement of E1+02+00+01.
	want := []byte{0xDD, 0x5A, 0xE1, 0x02, 0x00, 0x01, 0xFF, 0x1C, 0x77}
	if string(step.TXData) != string(want) {
		t.Fatalf("MOS frame = % X, want % X", step.TXData, want)
	}
	if step.ReadSize != 7 || step.RXTimeoutMS == 0 {
		t.Fatalf("MOS response bounds = %+v", step)
	}
}

func TestJiabaidaVerifyControlActionBindsResponseCommand(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// DD 05 00 03 'V' '1' '9' FF 3D 77; the response checksum is LEN+DATA.
	raw := []byte{0xDD, 0x05, 0x00, 0x03, 'V', '1', '9', 0xFF, 0x3D, 0x77}
	data, err := d.VerifyControlAction("read_hardware_version", json.RawMessage(`{}`), raw)
	if err != nil {
		t.Fatalf("VerifyControlAction(valid) error = %v", err)
	}
	if len(data) != 1 || data[0].Name != "hardware_version" || data[0].StringValue != "V19" {
		t.Fatalf("VerifyControlAction(valid) = %+v", data)
	}
	if _, err := d.VerifyControlAction("read_cell_voltage", json.RawMessage(`{}`), raw); err == nil {
		t.Fatal("wrong response command accepted")
	}
	if _, err := d.VerifyControlAction("read_hardware_version", json.RawMessage(`{"unexpected":true}`), raw); err == nil {
		t.Fatal("parameterized read accepted")
	}
}

func TestJiabaidaMOSPolicyPlanCompilerGoldenVector(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	plan, err := d.CompileControlActionPlan("set_mos_policy", json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`))
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if !plan.AtMostOnce || len(plan.Steps) != 2 {
		t.Fatalf("plan metadata = %+v", plan)
	}
	wantWrite := []byte{0xDD, 0x5A, 0xE1, 0x02, 0x00, 0x01, 0xFF, 0x1C, 0x77}
	if got := plan.Steps[0].TXData; string(got) != string(wantWrite) {
		t.Fatalf("write step = % X, want % X", got, wantWrite)
	}
	wantReadback := []byte{0xDD, 0xA5, 0x03, 0x00, 0xFF, 0xFD, 0x77}
	if got := plan.Steps[1].TXData; string(got) != string(wantReadback) {
		t.Fatalf("readback step = % X, want % X", got, wantReadback)
	}
	if plan.Steps[0].Kind != "write" || plan.Steps[1].Kind != "readback" {
		t.Fatalf("step kinds = %q/%q", plan.Steps[0].Kind, plan.Steps[1].Kind)
	}
}

func TestJiabaidaBMSRestartPlanCompilerGoldenVector(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	plan, err := d.CompileControlActionPlan("bms_restart", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CompileControlActionPlan(bms_restart) error = %v", err)
	}
	if !plan.AtMostOnce || len(plan.Steps) != 2 {
		t.Fatalf("plan metadata = %+v", plan)
	}
	// DD 5A 0E 00 81 18 CHK_H CHK_L 77; checksum is two's complement of 0E+00+81+18 = 0xFF59.
	wantWrite := []byte{0xDD, 0x5A, 0x0E, 0x00, 0x81, 0x18, 0xFF, 0x59, 0x77}
	if got := plan.Steps[0].TXData; string(got) != string(wantWrite) {
		t.Fatalf("reboot write step = % X, want % X", got, wantWrite)
	}
	wantReadback := []byte{0xDD, 0xA5, 0xAA, 0x00, 0xFF, 0x56, 0x77}
	if got := plan.Steps[1].TXData; string(got) != string(wantReadback) {
		t.Fatalf("restart_count readback step = % X, want % X", got, wantReadback)
	}
	if plan.Steps[0].Kind != "write" || plan.Steps[1].Kind != "readback" {
		t.Fatalf("step kinds = %q/%q", plan.Steps[0].Kind, plan.Steps[1].Kind)
	}
	if _, err := d.CompileControlActionPlan("read_protection_parameters", json.RawMessage(`{}`)); err == nil {
		t.Fatal("read_protection_parameters plan compiled without a frozen factory workflow")
	}
}

func TestJiabaidaVerifyMOSPolicyAckAndReadback(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`)
	// ACK frame: DD E1 00 00 00 00 77 (checksum of LEN=0 is 0x0000).
	ack := []byte{0xDD, 0xE1, 0x00, 0x00, 0x00, 0x00, 0x77}
	// Readback: 0x03 basic info frame (fet_status byte at offset 20 of payload).
	payload := make([]byte, 31)
	payload[20] = 0x01 // fet_status = charge MOS closed
	ckraw := append([]byte{0x1F}, payload...)
	ck := jiabaidaChecksum(ckraw)
	readback := append([]byte{0xDD, 0x03, 0x00, 0x1F}, payload...)
	readback = append(readback, byte(ck>>8), byte(ck&0xFF), 0x77)
	// Build the step-count envelope: [count][kind(1) + len_le(2) + data...] per step.
	envelope := []byte{2}
	envelope = append(envelope, 0x00) // step 1 kind
	envelope = append(envelope, byte(len(ack))&0xFF, byte(len(ack)>>8))
	envelope = append(envelope, ack...)
	envelope = append(envelope, 0x00) // step 2 kind
	envelope = append(envelope, byte(len(readback))&0xFF, byte(len(readback)>>8))
	envelope = append(envelope, readback...)

	data, err := d.VerifyControlAction("set_mos_policy", params, envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(envelope) error = %v", err)
	}
	if len(data) < 2 || data[0].Name != "mos_ack" || data[0].Value != 1 {
		t.Fatalf("VerifyControlAction(envelope) = %+v", data)
	}
	found := false
	for _, s := range data[1:] {
		if s.Name == "fet_status" {
			found = true
			if s.Value != 1 {
				t.Fatalf("fet_status = %v, want 1", s.Value)
			}
		}
	}
	if !found {
		t.Fatalf("readback result missing fet_status: %+v", data)
	}
	// Wrong step count must be rejected.
	bad := []byte{1, 0x00}
	bad = append(bad, byte(len(ack))&0xFF, byte(len(ack)>>8))
	bad = append(bad, ack...)
	if _, err := d.VerifyControlAction("set_mos_policy", params, bad); err == nil {
		t.Fatal("wrong step count accepted for set_mos_policy")
	}
	// Malformed ACK (wrong command byte) must be rejected.
	wrongAck := []byte{0xDD, 0x04, 0x00, 0x00, 0x00, 0x00, 0x77}
	badAck := []byte{2, 0x00}
	badAck = append(badAck, byte(len(wrongAck))&0xFF, byte(len(wrongAck)>>8))
	badAck = append(badAck, wrongAck...)
	badAck = append(badAck, 0x00) // step 2 kind
	badAck = append(badAck, byte(len(readback))&0xFF, byte(len(readback)>>8))
	badAck = append(badAck, readback...)
	if _, err := d.VerifyControlAction("set_mos_policy", params, badAck); err == nil {
		t.Fatal("wrong ACK command accepted for set_mos_policy")
	}
}

func TestJiabaidaVerifyBMSRestartAckAndReadback(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// 0x0E reset ACK: DD 0E 00 00 00 00 77 (LEN=0, checksum of LEN+DATA = 0x0000).
	ack := []byte{0xDD, 0x0E, 0x00, 0x00, 0x00, 0x00, 0x77}
	// 0xAA protection history readback: 12×uint16, restart_count=5 at offset 22.
	payload := make([]byte, 24)
	payload[22], payload[23] = 0x00, 0x05
	ckraw := append([]byte{0x18}, payload...)
	ck := jiabaidaChecksum(ckraw)
	readback := append([]byte{0xDD, 0xAA, 0x00, 0x18}, payload...)
	readback = append(readback, byte(ck>>8), byte(ck&0xFF), 0x77)
	// Batch envelope: [count][kind, len_le, ack][kind, len_le, readback].
	envelope := []byte{2, 0x00}
	envelope = append(envelope, byte(len(ack))&0xFF, byte(uint16(len(ack))>>8))
	envelope = append(envelope, ack...)
	envelope = append(envelope, 0x00)
	envelope = append(envelope, byte(len(readback))&0xFF, byte(uint16(len(readback))>>8))
	envelope = append(envelope, readback...)

	data, err := d.VerifyControlAction("bms_restart", json.RawMessage(`{}`), envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(bms_restart) error = %v", err)
	}
	if len(data) < 2 || data[0].Name != "reboot_ack" || data[0].Value != 1 {
		t.Fatalf("VerifyControlAction(bms_restart) = %+v", data)
	}
	found := false
	for _, s := range data[1:] {
		if s.Name == "restart_count" {
			found = true
			if s.Value != 5 {
				t.Fatalf("restart_count = %v, want 5", s.Value)
			}
		}
	}
	if !found {
		t.Fatalf("readback result missing restart_count: %+v", data)
	}
	// Malformed ACK (wrong command byte) must be rejected.
	wrongAck := []byte{0xDD, 0x04, 0x00, 0x00, 0x00, 0x00, 0x77}
	bad := []byte{2, 0x00}
	bad = append(bad, byte(len(wrongAck))&0xFF, byte(uint16(len(wrongAck))>>8))
	bad = append(bad, wrongAck...)
	bad = append(bad, 0x00)
	bad = append(bad, byte(len(readback))&0xFF, byte(uint16(len(readback))>>8))
	bad = append(bad, readback...)
	if _, err := d.VerifyControlAction("bms_restart", json.RawMessage(`{}`), bad); err == nil {
		t.Fatal("wrong ACK command accepted for bms_restart")
	}
}

func TestCRC16Modbus(t *testing.T) {
	// Known test vector: Modbus CRC-16
	data := []byte{0x01, 0x03, 0x02, 0x00, 0x01}
	crc := CRC16Modbus(data)
	// Expected CRC for this sequence with poly 0xA001
	if crc == 0 {
		t.Error("CRC16 should not be zero")
	}
	// Verify CRC is non-zero and deterministic
	if CRC16Modbus(data) != crc {
		t.Error("CRC16 should be deterministic")
	}
}

func TestFactoryModeHelpers(t *testing.T) {
	// Verify factory mode helpers produce frames with valid checksums
	enter := FactoryModeEnterCmd()
	if !verifyJiabaidaChecksum(enter) {
		t.Error("FactoryModeEnterCmd has invalid checksum")
	}
	if enter[0] != 0xDD || enter[len(enter)-1] != 0x77 {
		t.Error("FactoryModeEnterCmd missing frame delimiters")
	}

	exitRead := FactoryModeExitForRead()
	if !verifyJiabaidaChecksum(exitRead) {
		t.Error("FactoryModeExitForRead has invalid checksum")
	}

	exitWrite := FactoryModeExitForWrite()
	if !verifyJiabaidaChecksum(exitWrite) {
		t.Error("FactoryModeExitForWrite has invalid checksum")
	}
}
