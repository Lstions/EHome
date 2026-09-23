package drivers

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

// TestJiabaidaReadFrameGoldenVectors locks jiabaidaReadFrame against the
// protocol's known read-request vectors.  Checksums are the two's-complement
// sum over frame[2:4] (CMD + 0x00), i.e. 0x10000 - CMD; the vectors below are
// derived with that rule (0xA2 → 0xFF5E, NOT 0xFF5D).
func TestJiabaidaReadFrameGoldenVectors(t *testing.T) {
	cases := []struct {
		cmd  byte
		want string
	}{
		{0x03, "DDA50300FFFD77"},
		{0x04, "DDA50400FFFC77"},
		{0x05, "DDA50500FFFB77"},
		{0x0F, "DDA50F00FFF177"},
		{0xAA, "DDA5AA00FF5677"},
		{0x0C, "DDA50C00FFF477"},
		{0xF0, "DDA5F000FF1077"},
		{0xF2, "DDA5F200FF0E77"},
		{0xF3, "DDA5F300FF0D77"},
		{0xF6, "DDA5F600FF0A77"},
		{0xA2, "DDA5A200FF5E77"},
	}
	for _, tc := range cases {
		frame := jiabaidaReadFrame(tc.cmd)
		if got := strings.ToUpper(hex.EncodeToString(frame)); got != tc.want {
			t.Errorf("jiabaidaReadFrame(0x%02X) = %s, want %s", tc.cmd, got, tc.want)
		}
		if len(frame) != 7 || frame[0] != 0xDD || frame[1] != 0xA5 || frame[2] != tc.cmd || frame[3] != 0x00 || frame[6] != 0x77 {
			t.Errorf("jiabaidaReadFrame(0x%02X) malformed: % X", tc.cmd, frame)
		}
		if chk := jiabaidaChecksum(frame[2:4]); chk != binary.BigEndian.Uint16(frame[4:6]) {
			t.Errorf("jiabaidaReadFrame(0x%02X) checksum = %04X, want %04X", tc.cmd, binary.BigEndian.Uint16(frame[4:6]), chk)
		}
		if !verifyJiabaidaChecksum(frame) {
			t.Errorf("jiabaidaReadFrame(0x%02X) fails verifyJiabaidaChecksum", tc.cmd)
		}
	}
	// GetCommandTemplates' hex WriteData must be exactly the frame builder's
	// output — no second hand-written copy of the golden vectors.
	templates := (&JiabaidaBMSDriver{}).GetCommandTemplates()
	for _, tmpl := range templates {
		want := hex.EncodeToString(jiabaidaReadFrame(tmpl.CmdByte))
		if tmpl.WriteData != want {
			t.Errorf("template %s WriteData = %s, want frame-built %s", tmpl.ID, tmpl.WriteData, want)
		}
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

// TestJiabaidaControlActionCatalog pins the whole jiabaida action catalogue.
//
// 2026-09-23 (T1)：原本 18 个动作挂在 AvailabilityCode="protocol_unverified" 后面
// 保持 fail-closed；本轮解除该门禁，26 个动作全部 Enabled。本测试随之把
// 「断言被门禁挡住」换成「断言已启用且结构未变」—— 即 ExecutionShape 与
// Verification 仍逐条钉死（这两项是启用后真正的安全契约：写操作必须声明对账
// 语义，且必须走 bounded_sequence 计划）。
func TestJiabaidaControlActionCatalog(t *testing.T) {
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
		if action.ID == "set_mos_policy" {
			// Default-enablement: bounded compiler + verifier + readback
			// declaration means the MOS policy switch is usable without an
			// allowlist (2026-08-14).
			if !action.Enabled || action.AvailabilityCode != "" || action.ExecutionShape != "bounded_sequence" || action.Verification != "readback" {
				t.Fatalf("MOS policy must be default-enabled bounded action: %+v", action)
			}
			continue
		}
		if action.ID == "read_protection_parameters" || action.ID == "read_system_parameters" || action.ID == "bms_restart" {
			// 2026-09-23 (T1): the protocol_unverified gate was lifted, so these
			// three are now enabled. Structure is still pinned: the two factory
			// reads are bounded_sequence/readback, the restart is
			// bounded_sequence/observation (ACK alone must never prove success).
			wantShape, wantVerification := "bounded_sequence", "readback"
			if action.ID == "bms_restart" {
				wantVerification = "observation"
			}
			if !action.Enabled || action.AvailabilityCode != "" ||
				action.ExecutionShape != wantShape || action.Verification != wantVerification {
				t.Fatalf("formerly guarded action must now be enabled with intact structure: %+v", action)
			}
			continue
		}
		// The V19 write/factory-mode actions added 2026-08-16 were catalog-visible
		// but fail-closed behind protocol_unverified.  2026-09-23 (T1) lifted that
		// gate, so every one of them must now be Enabled.  The structural
		// contract is unchanged and still pinned per action: each keeps its
		// declared bounded_sequence shape and verification semantics, and none
		// may retain an AvailabilityCode (which would silently re-gate it).
		guardedWrites := map[string]struct{ shape, verification string }{
			"write_protection_parameters": {"bounded_sequence", "readback"},
			"write_system_parameters":     {"bounded_sequence", "readback"},
			"test_charge_mos":             {"bounded_sequence", "readback"},
			"test_discharge_mos":          {"bounded_sequence", "readback"},
			"force_balance":               {"bounded_sequence", "readback"},
			"find_car":                    {"bounded_sequence", "ack"},
			"clear_alarm":                 {"bounded_sequence", "ack"},
			"auto_test_edv":               {"bounded_sequence", "ack"},
			"write_custom_attributes":     {"bounded_sequence", "readback"},
			"write_internal_resistance":   {"bounded_sequence", "readback"},
			"set_static_correction_time":  {"bounded_sequence", "ack"},
			"set_report_interval":         {"bounded_sequence", "ack"},
			"set_charge_time_window":      {"bounded_sequence", "ack"},
			"set_discharge_time_limit":    {"bounded_sequence", "ack"},
			"write_sn":                    {"bounded_sequence", "readback"},
		}
		if wantShape, ok := guardedWrites[action.ID]; ok {
			if !action.Enabled || action.AvailabilityCode != "" || action.ExecutionShape != wantShape.shape || action.Verification != wantShape.verification {
				t.Fatalf("formerly guarded write action is not enabled with intact structure: %+v", action)
			}
			continue
		}
		if action.ID == "read_test_mos_status" || action.ID == "read_custom_attributes" {
			// Manual reads are enabled (2026-09-23): they ride the same verified
			// single-step telemetry path and perform no write.
			if !action.Enabled || action.AvailabilityCode != "" || action.Semantics != "read" || action.Risk != "low" || len(action.TXData) == 0 || action.ReadSize == 0 || action.RXTimeoutMS == 0 {
				t.Fatalf("unsafe or malformed extended read action %+v", action)
			}
			continue
		}
		frame, ok := want[action.ID]
		if !ok {
			t.Fatalf("unexpected action %q", action.ID)
		}
		// Manual reads are enabled (2026-09-23).  The safety property that
		// matters here is structural: exact frozen TX frame, a read window, a
		// timeout, no AvailabilityCode, and read/low-risk semantics.  The gate
		// that must stay fail-closed is the WRITE gate, asserted above.
		if !action.Enabled || action.AvailabilityCode != "" || action.Semantics != "read" || action.Risk != "low" || string(action.TXData) != string(frame) || action.ReadSize == 0 || action.RXTimeoutMS == 0 {
			t.Fatalf("unsafe or malformed action %+v", action)
		}
		delete(want, action.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing actions: %v", want)
	}
}

// TestJiabaidaFormerlyGuardedActionsAreEnabledAtDriverLayer pins the 18 actions
// whose protocol_unverified gate was lifted on 2026-09-23 (T1).
//
// 为什么必须断言**驱动字面量**而不是 deviceaction 层：
// deviceaction/definition.go 的默认启用重算只覆盖 set/reset —— 它会把这两个
// 语义的 Enabled 直接改写为 true（只要 verifier/Verification/AvailabilityCode
// 满足）。于是"驱动层没写 Enabled: true"这种缺陷会被重算**掩盖**，在
// deviceaction 层看起来一切正常；只有 read 语义会暴露（它沿用字面量）。
// 本测试把 18 个动作的字面量状态逐个钉死，使下面两类回归立即变红：
//  1. 有人重新加上 AvailabilityCode → 该动作被静默重新门禁；
//  2. 有人删掉 read_protection_parameters / read_system_parameters 的
//     Enabled: true → 这两条会静默变回 disabled（且 deviceaction 层同样
//     disabled，因为 read 不走重算）。
func TestJiabaidaFormerlyGuardedActionsAreEnabledAtDriverLayer(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// 18 个动作：2026-09-23 前全部挂在 AvailabilityCode="protocol_unverified" 后。
	formerlyGuarded := []string{
		"read_protection_parameters", "read_system_parameters", "bms_restart",
		"write_protection_parameters", "write_system_parameters", "test_charge_mos",
		"test_discharge_mos", "force_balance", "find_car", "clear_alarm",
		"auto_test_edv", "write_custom_attributes", "write_internal_resistance",
		"set_static_correction_time", "set_report_interval", "set_charge_time_window",
		"set_discharge_time_limit", "write_sn",
	}
	if len(formerlyGuarded) != 18 {
		t.Fatalf("denominator drift: want 18 formerly guarded actions, got %d", len(formerlyGuarded))
	}
	byID := map[string]ControlAction{}
	for _, action := range d.ControlActions() {
		byID[action.ID] = action
	}
	if len(byID) != 26 {
		t.Fatalf("want 26 jiabaida control actions, got %d", len(byID))
	}
	for _, id := range formerlyGuarded {
		action, ok := byID[id]
		if !ok {
			t.Fatalf("formerly guarded action %q vanished from the catalogue", id)
		}
		if !action.Enabled {
			t.Errorf("%s: driver literal Enabled=false; deviceaction only recomputes set/reset, so this action is not actually reachable", id)
		}
		if action.AvailabilityCode != "" {
			t.Errorf("%s: AvailabilityCode=%q silently re-gates a formerly enabled action", id, action.AvailabilityCode)
		}
		if action.ExecutionShape != "bounded_sequence" || action.Verification == "" {
			t.Errorf("%s: lost its bounded/verified shape: %+v", id, action)
		}
	}
	// 其余 8 个（5 个单步读 + set_mos_policy + 2 个扩展读）也必须仍然启用。
	for _, id := range []string{"read_basic_info", "read_cell_voltage", "read_hardware_version",
		"read_comprehensive", "read_protection_count", "read_test_mos_status",
		"read_custom_attributes", "set_mos_policy"} {
		action, ok := byID[id]
		if !ok {
			t.Fatalf("previously enabled action %q vanished", id)
		}
		if !action.Enabled || action.AvailabilityCode != "" {
			t.Errorf("%s regressed out of the enabled set: %+v", id, action)
		}
	}
}

// TestJiabaidaEnabledSingleStepReadsAreVerifiable guards the drift that shipped
// 2026-09-23: read_test_mos_status and read_custom_attributes were enabled in
// the catalogue but missing from VerifyControlAction's command table, so each
// execution performed a real USB round-trip and then failed with "unknown
// jiabaida control action" -- a green-looking physical path with a red result.
// Every enabled single-step read must be verifiable against its own frozen TX
// frame, so adding a read to the catalogue without teaching the verifier about
// its response command fails here instead of in production.
func TestJiabaidaEnabledSingleStepReadsAreVerifiable(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	for _, action := range d.ControlActions() {
		if !action.Enabled || action.Semantics != "read" ||
			action.ExecutionShape == "bounded_sequence" || len(action.TXData) < 3 {
			continue
		}
		cmd := action.TXData[2]
		// A frame carrying zero DATA bytes.  The verifier MUST bind the response
		// command before parsing, so the only acceptable failure here is a
		// data-shortage error from the command-specific parser -- never the
		// "unknown control action" drift this test exists to catch.
		raw := []byte{0xDD, cmd, 0x00, 0x00, 0x00, 0x00, 0x77}
		_, err := d.VerifyControlAction(action.ID, json.RawMessage("{}"), raw)
		if err == nil {
			continue // parser tolerated the empty payload too
		}
		if strings.Contains(err.Error(), "unknown jiabaida control action") {
			t.Errorf("enabled read %q has no verifier binding (catalogue/verifier drift): %v", action.ID, err)
		}
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
	// The F2 factory-mode read workflow is now compiled (enter → read → finally
	// exit).  The action itself remains catalog-gated on physical evidence.
	readPlan, err := d.CompileControlActionPlan("read_protection_parameters", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CompileControlActionPlan(read_protection_parameters) error = %v", err)
	}
	if len(readPlan.Steps) != 3 || !readPlan.RequiresFinally || readPlan.Steps[2].Kind != "finally" {
		t.Fatalf("read_protection_parameters plan = %+v", readPlan)
	}
	if got := readPlan.Steps[0].TXData; string(got) != string(FactoryModeEnterCmd()) {
		t.Fatalf("enter factory step = % X, want % X", got, FactoryModeEnterCmd())
	}
	if got := readPlan.Steps[2].TXData; string(got) != string(FactoryModeExitForRead()) {
		t.Fatalf("exit factory step = % X, want % X", got, FactoryModeExitForRead())
	}
}

func TestJiabaidaVerifyMOSPolicyAckAndReadback(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`)
	// ACK frame: DD E1 00 00 00 00 77 (checksum of LEN=0 is 0x0000).
	ack := []byte{0xDD, 0xE1, 0x00, 0x00, 0x00, 0x00, 0x77}
	// Readback: 0x03 basic info frame (fet_status byte at offset 20 of payload).
	payload := make([]byte, 31)
	payload[20] = 0x02 // fet_status: bit1=1 discharge open, bit0=0 charge closed (matches charge_software_closed:true)
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
			if s.Value != 2 {
				t.Fatalf("fet_status = %v, want 2", s.Value)
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

// jiabaidaTestResponse builds a checksum-valid response frame
// DD CMD 00 LEN DATA... CHK_H CHK_L 77 (response checksum covers LEN+DATA).
func jiabaidaTestResponse(t *testing.T, cmd byte, data []byte) []byte {
	t.Helper()
	frame := []byte{0xDD, cmd, 0x00, byte(len(data))}
	frame = append(frame, data...)
	chk := jiabaidaChecksum(frame[3:])
	return append(frame, byte(chk>>8), byte(chk), 0x77)
}

// jiabaidaZeroAck is the zero-length write ACK for cmd: DD CMD 00 00 00 00 77.
func jiabaidaZeroAck(cmd byte) []byte {
	return []byte{0xDD, cmd, 0x00, 0x00, 0x00, 0x00, 0x77}
}

// jiabaidaTestEnvelope encodes steps into the ChannelCmdV2 batch envelope:
// [count] then per step [kind][len_le_lo][len_le_hi][response...].
func jiabaidaTestEnvelope(t *testing.T, steps ...[]byte) []byte {
	t.Helper()
	env := []byte{byte(len(steps))}
	for _, step := range steps {
		if len(step) > 0xFFFF {
			t.Fatalf("test step too long: %d", len(step))
		}
		env = append(env, 0x00, byte(len(step)), byte(len(step)>>8))
		env = append(env, step...)
	}
	return env
}

// jiabaidaMOSEnvelope builds the two-step set_mos_policy response envelope:
// the E1 write ACK plus a 0x03 basic-info readback whose FET control byte
// (payload offset 20) is set to fet.
func jiabaidaMOSEnvelope(t *testing.T, ack []byte, fet byte) []byte {
	t.Helper()
	payload := make([]byte, 31)
	payload[20] = fet
	ckraw := append([]byte{0x1F}, payload...)
	ck := jiabaidaChecksum(ckraw)
	readback := append([]byte{0xDD, 0x03, 0x00, 0x1F}, payload...)
	readback = append(readback, byte(ck>>8), byte(ck&0xFF), 0x77)
	return jiabaidaTestEnvelope(t, ack, readback)
}

// jiabaidaHardwareVersionReadback is the known-good 0x05 response used by the
// readback step of setter workflows whose verification contract is ACK plus a
// parseable follow-up sample (mirrors the MOS policy verifier semantics).
func jiabaidaHardwareVersionReadback() []byte {
	return []byte{0xDD, 0x05, 0x00, 0x03, 'V', '1', '9', 0xFF, 0x3D, 0x77}
}

// jiabaidaMOSStatusReadback builds a 0x0C response whose tested MOS reports OK.
// parse0x0C reads DATA as [discharge_test_status, charge_test_status] with
// 0=untested 1=OK 2=NG 3=timeout, so the tested side is the one under test.
func jiabaidaMOSStatusReadback(tested byte) []byte {
	discharge, charge := byte(0), byte(0)
	if tested == 0x02 {
		discharge = 1
	} else {
		charge = 1
	}
	body := []byte{0x02, discharge, charge}
	ck := jiabaidaChecksum(body)
	return []byte{0xDD, 0x0C, 0x00, body[0], body[1], body[2], byte(ck >> 8), byte(ck), 0x77}
}

func jiabaidaF2TestParams(t *testing.T) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"cell_ov_protect": 3650, "cell_ov_release": 3550,
		"cell_uv_protect": 2800, "cell_uv_release": 2900,
		"pack_ov_protect": 5840, "pack_ov_release": 5680,
		"pack_uv_protect": 4480, "pack_uv_release": 4640,
		"cell_ov_delay": 30, "cell_uv_delay": 30,
		"pack_ov_delay": 30, "pack_uv_delay": 30,
		"chg_ot_protect": 3181, "chg_ot_release": 3131,
		"chg_ut_protect": 2731, "chg_ut_release": 2761,
		"dis_ot_protect": 3281, "dis_ot_release": 3231,
		"dis_ut_protect": 2631, "dis_ut_release": 2681,
		"chg_ot_delay": 10, "chg_ut_delay": 10,
		"dis_ot_delay": 10, "dis_ut_delay": 10,
		"chg_oc_protect": 20000, "chg_oc_delay": 5, "chg_oc_release_delay": 10,
		"dis_oc_protect": 25000, "dis_oc_delay": 5, "dis_oc_release_delay": 10,
		"short_circuit_protect": 3, "hardware_oc_protect": 2, "short_circuit_release": 30,
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal F2 params: %v", err)
	}
	return raw
}

func jiabaidaF3TestParams(t *testing.T) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"function_config": 255, "ntc_config": 3, "cell_count_config": 15,
		"shunt_resistance": 100, "balance_start_voltage": 3400, "balance_diff": 30,
		"gps_shutdown_voltage": 2800, "gps_shutdown_delay": 60,
		"nominal_capacity_cfg": 10000, "cycle_capacity_cfg": 9800,
		"cell_full_voltage": 3650, "cell_empty_voltage": 2800,
		"self_discharge_rate": 50, "soc100_voltage": 3600, "soc0_voltage": 2900,
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal F3 params: %v", err)
	}
	return raw
}

// TestJiabaidaFactoryReadPlanGoldenVectors anchors the factory-mode read
// request frames to the checksums printed in the V19 protocol document
// (§7.11: F2 read FF0E, F3 read FF0D).
func TestJiabaidaFactoryReadPlanGoldenVectors(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	cases := []struct {
		action string
		want   []byte
	}{
		{"read_protection_parameters", []byte{0xDD, 0xA5, 0xF2, 0x00, 0xFF, 0x0E, 0x77}},
		{"read_system_parameters", []byte{0xDD, 0xA5, 0xF3, 0x00, 0xFF, 0x0D, 0x77}},
	}
	for _, tc := range cases {
		plan, err := d.CompileControlActionPlan(tc.action, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("CompileControlActionPlan(%s) error = %v", tc.action, err)
		}
		if len(plan.Steps) != 3 || !plan.RequiresFinally || plan.Steps[2].Kind != "finally" {
			t.Fatalf("%s plan shape = %+v", tc.action, plan)
		}
		if got := plan.Steps[1].TXData; string(got) != string(tc.want) {
			t.Fatalf("%s read step = % X, want % X", tc.action, got, tc.want)
		}
	}
}

func TestJiabaidaWriteProtectionParametersPlanAndVerifier(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := jiabaidaF2TestParams(t)
	plan, err := d.CompileControlActionPlan("write_protection_parameters", params)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if !plan.AtMostOnce || !plan.RequiresFinally || len(plan.Steps) != 4 {
		t.Fatalf("plan metadata = %+v", plan)
	}
	kinds := []string{plan.Steps[0].Kind, plan.Steps[1].Kind, plan.Steps[2].Kind, plan.Steps[3].Kind}
	if kinds[0] != "write" || kinds[1] != "write" || kinds[2] != "readback" || kinds[3] != "finally" {
		t.Fatalf("step kinds = %v", kinds)
	}
	if string(plan.Steps[0].TXData) != string(FactoryModeEnterCmd()) {
		t.Fatalf("enter factory step = % X", plan.Steps[0].TXData)
	}
	if string(plan.Steps[3].TXData) != string(FactoryModeExitForWrite()) {
		t.Fatalf("exit factory step = % X", plan.Steps[3].TXData)
	}
	writeFrame := plan.Steps[1].TXData
	if len(writeFrame) != 60 || writeFrame[0] != 0xDD || writeFrame[1] != 0x5A || writeFrame[2] != 0xF2 || writeFrame[3] != 53 {
		t.Fatalf("F2 write frame header = % X", writeFrame[:4])
	}
	if !verifyJiabaidaChecksum(writeFrame) {
		t.Fatal("F2 write frame checksum invalid")
	}
	block := writeFrame[4:57]
	// Round-trip: parse0xF2 is the inverse mapping of compileF2Block.
	fields, err := d.parse0xF2(block)
	if err != nil {
		t.Fatalf("parse0xF2(compiled block) error = %v", err)
	}
	got := map[string]float64{}
	for _, f := range fields {
		got[f.Name] = f.Value
	}
	// chg_oc_protect is stored raw (10mA) but parsed as /100 → A, so raw
	// 20000 round-trips to 200 A.  short_circuit_release is a raw u8.
	if got["cell_ov_protect"] != 3650 || got["chg_oc_protect"] != 200 || got["short_circuit_release"] != 30 {
		t.Fatalf("F2 round-trip mismatch: %+v", got)
	}
	// Verifier accepts the exact readback of the written block.
	readback := jiabaidaTestResponse(t, 0xF2, block)
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xF2), readback, jiabaidaZeroAck(0x01))
	data, err := d.VerifyControlAction("write_protection_parameters", params, envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(write_protection_parameters) error = %v", err)
	}
	if len(data) < 2 || data[0].Name != "write_ack" || data[0].Value != 1 {
		t.Fatalf("verified result = %+v", data)
	}
	// A readback that does not match the written block must be rejected.
	tampered := append([]byte(nil), block...)
	tampered[0] ^= 0xFF
	badEnvelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xF2),
		jiabaidaTestResponse(t, 0xF2, tampered), jiabaidaZeroAck(0x01))
	if _, err := d.VerifyControlAction("write_protection_parameters", params, badEnvelope); err == nil {
		t.Fatal("mismatched F2 readback accepted")
	}
	// Missing parameters are rejected at compile time.
	if _, err := d.CompileControlActionPlan("write_protection_parameters", json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty F2 params accepted")
	}
}

func TestJiabaidaWriteSystemParametersPlanAndVerifier(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := jiabaidaF3TestParams(t)
	plan, err := d.CompileControlActionPlan("write_system_parameters", params)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if len(plan.Steps) != 4 || !plan.RequiresFinally {
		t.Fatalf("plan metadata = %+v", plan)
	}
	writeFrame := plan.Steps[1].TXData
	if len(writeFrame) != 59 || writeFrame[2] != 0xF3 || writeFrame[3] != 52 || !verifyJiabaidaChecksum(writeFrame) {
		t.Fatalf("F3 write frame = % X", writeFrame)
	}
	block := writeFrame[4:56]
	fields, err := d.parse0xF3(block)
	if err != nil {
		t.Fatalf("parse0xF3(compiled block) error = %v", err)
	}
	for _, f := range fields {
		// nominal_capacity_cfg is written raw (10000 = 100.00 Ah units of 0.01Ah).
		if f.Name == "nominal_capacity_cfg" && f.Value != 100 {
			t.Fatalf("nominal_capacity_cfg round-trip = %v, want 100", f.Value)
		}
		if f.Name == "cell_count_config" && f.Value != 15 {
			t.Fatalf("cell_count_config round-trip = %v, want 15", f.Value)
		}
	}
	readback := jiabaidaTestResponse(t, 0xF3, block)
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xF3), readback, jiabaidaZeroAck(0x01))
	data, err := d.VerifyControlAction("write_system_parameters", params, envelope)
	if err != nil || len(data) < 2 || data[0].Name != "write_ack" {
		t.Fatalf("VerifyControlAction(write_system_parameters) = %+v, err = %v", data, err)
	}
}

// TestJiabaidaFieldTableCoversAllDeclaredBytes locks the P1-2 field tables:
// the F2 table must cover bytes 0..50 continuously with no holes/overlaps,
// the F3 table must cover exactly {0-15, 20-31, 48-49} (reserved ranges
// 16-19 / 32-47 and the CRC tail stay uncovered), the derived reconciliation
// spans must match, and compile/schema must be driven by the same tables.
func TestJiabaidaFieldTableCoversAllDeclaredBytes(t *testing.T) {
	cover := func(fields []jiabaidaField) (map[int]bool, map[string]int) {
		covered := map[int]bool{}
		byName := map[string]int{}
		for _, f := range fields {
			if f.Width != 1 && f.Width != 2 {
				t.Fatalf("field %s has invalid width %d", f.Name, f.Width)
			}
			if f.Min < 0 || f.Max < f.Min {
				t.Fatalf("field %s has invalid Min/Max %v/%v", f.Name, f.Min, f.Max)
			}
			for i := f.Offset; i < f.Offset+f.Width; i++ {
				if covered[i] {
					t.Fatalf("field %s overlaps at byte %d", f.Name, i)
				}
				covered[i] = true
			}
			byName[f.Name] = f.Offset
		}
		return covered, byName
	}

	// F2: bytes 0..50 all declared, no holes.
	f2Covered, f2ByName := cover(jiabaidaF2Fields())
	for i := 0; i <= 50; i++ {
		if !f2Covered[i] {
			t.Fatalf("F2 byte %d not covered by field table", i)
		}
	}
	if len(f2ByName) != 33 {
		t.Fatalf("F2 table has %d fields, want 33", len(f2ByName))
	}
	wantF2Spans := [][2]int{{0, 50}}
	if got := jiabaidaF2FieldSpans(); !reflect.DeepEqual(got, wantF2Spans) {
		t.Fatalf("F2 spans = %v, want %v", got, wantF2Spans)
	}

	// F3: declared bytes exactly {0-15, 20-31, 48-49}.
	f3Covered, f3ByName := cover(jiabaidaF3Fields())
	declared3 := func(lo, hi int) {
		for i := lo; i <= hi; i++ {
			if !f3Covered[i] {
				t.Fatalf("F3 declared byte %d not covered by field table", i)
			}
		}
	}
	declared3(0, 15)
	declared3(20, 31)
	declared3(48, 49)
	for _, i := range []int{16, 17, 18, 19, 32, 33, 47, 50, 51} {
		if f3Covered[i] {
			t.Fatalf("F3 byte %d is reserved/CRC but covered by field table", i)
		}
	}
	if len(f3ByName) != 15 {
		t.Fatalf("F3 table has %d fields, want 15", len(f3ByName))
	}
	wantF3Spans := [][2]int{{0, 15}, {20, 31}, {48, 49}}
	if got := jiabaidaF3FieldSpans(); !reflect.DeepEqual(got, wantF3Spans) {
		t.Fatalf("F3 spans = %v, want %v", got, wantF3Spans)
	}

	// compileF3Block must write exactly the table's fields and leave the
	// reserved byte ranges zero (CRC covers only the 50 field bytes).
	params := jiabaidaF3TestParams(t)
	block, err := compileF3Block(params)
	if err != nil {
		t.Fatalf("compileF3Block error = %v", err)
	}
	if len(block) != 52 {
		t.Fatalf("F3 block length = %d, want 52", len(block))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		t.Fatalf("unmarshal F3 params: %v", err)
	}
	for _, f := range jiabaidaF3Fields() {
		want, err := jiabaidaUint16Param(fields, f.Name)
		if err != nil {
			t.Fatalf("decode %s: %v", f.Name, err)
		}
		if got := binary.BigEndian.Uint16(block[f.Offset : f.Offset+2]); got != want {
			t.Fatalf("compileF3Block wrote %s = %d, want %d", f.Name, got, want)
		}
	}
	for _, i := range []int{16, 17, 18, 19, 32, 33, 47} {
		if block[i] != 0 {
			t.Fatalf("F3 reserved byte %d = %02X, want 0", i, block[i])
		}
	}

	// Schema must be generated from the table: same names, same order, same
	// Min/Max.
	for name, params := range map[string][]ControlParameter{
		"F2": jiabaidaF2Parameters(),
		"F3": jiabaidaF3Parameters(),
	} {
		var table []jiabaidaField
		if name == "F2" {
			table = jiabaidaF2Fields()
		} else {
			table = jiabaidaF3Fields()
		}
		if len(params) != len(table) {
			t.Fatalf("%s schema has %d params, table has %d fields", name, len(params), len(table))
		}
		for i, f := range table {
			p := params[i]
			if p.Name != f.Name || p.Type != "integer" || !p.Required ||
				p.Minimum == nil || *p.Minimum != f.Min || p.Maximum == nil || *p.Maximum != f.Max {
				t.Fatalf("%s schema[%d] = %+v, want table field %+v", name, i, p, f)
			}
		}
	}

	// Parse projection must be exactly the legacy emission: F2 emits 23 of
	// its 33 fields (delay/protection counters excluded) and F3 emits all 15,
	// in table (= legacy) order.
	d := &JiabaidaBMSDriver{}
	f2Fields, err := d.parse0xF2(f2Block(t, jiabaidaF2TestParams(t)))
	if err != nil {
		t.Fatalf("parse0xF2 error = %v", err)
	}
	f2Names := make([]string, 0, len(f2Fields))
	for _, f := range f2Fields {
		f2Names = append(f2Names, f.Name)
	}
	wantF2Names := []string{
		"cell_ov_protect", "cell_ov_release", "cell_uv_protect", "cell_uv_release",
		"pack_ov_protect", "pack_ov_release", "pack_uv_protect", "pack_uv_release",
		"cell_ov_delay", "cell_uv_delay", "pack_ov_delay", "pack_uv_delay",
		"chg_ot_protect", "chg_ot_release", "chg_ut_protect", "chg_ut_release",
		"dis_ot_protect", "dis_ot_release", "dis_ut_protect", "dis_ut_release",
		"chg_oc_protect", "dis_oc_protect", "short_circuit_release",
	}
	if !reflect.DeepEqual(f2Names, wantF2Names) {
		t.Fatalf("parse0xF2 names = %v, want %v", f2Names, wantF2Names)
	}
	f3Fields, err := d.parse0xF3(f3Block(t, jiabaidaF3TestParams(t)))
	if err != nil {
		t.Fatalf("parse0xF3 error = %v", err)
	}
	f3Names := make([]string, 0, len(f3Fields))
	for _, f := range f3Fields {
		f3Names = append(f3Names, f.Name)
	}
	wantF3Names := []string{
		"function_config", "ntc_config", "cell_count_config", "shunt_resistance",
		"balance_start_voltage", "balance_diff", "gps_shutdown_voltage", "gps_shutdown_delay",
		"nominal_capacity_cfg", "cycle_capacity_cfg", "cell_full_voltage", "cell_empty_voltage",
		"self_discharge_rate", "soc100_voltage", "soc0_voltage",
	}
	if !reflect.DeepEqual(f3Names, wantF3Names) {
		t.Fatalf("parse0xF3 names = %v, want %v", f3Names, wantF3Names)
	}
}

// f2Block compiles the F2 test params into the 53-byte block (helper).
func f2Block(t *testing.T, params json.RawMessage) []byte {
	t.Helper()
	block, err := compileF2Block(params)
	if err != nil {
		t.Fatalf("compileF2Block error = %v", err)
	}
	return block
}

// f3Block compiles the F3 test params into the 52-byte block (helper).
func f3Block(t *testing.T, params json.RawMessage) []byte {
	t.Helper()
	block, err := compileF3Block(params)
	if err != nil {
		t.Fatalf("compileF3Block error = %v", err)
	}
	return block
}

// TestJiabaidaF2F3ReadbackDeclaredFieldSpans locks the P0-1 readback
// reconciliation: only DECLARED field bytes are compared, so reserved bytes
// (F3 16-19 / 32-47) and CRC bytes may differ on readback without failing,
// while any declared-field difference still fails hard.
func TestJiabaidaF2F3ReadbackDeclaredFieldSpans(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	verify := func(action string, params json.RawMessage, block []byte, cmd byte) error {
		t.Helper()
		envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(cmd),
			jiabaidaTestResponse(t, cmd, block), jiabaidaZeroAck(0x01))
		_, err := d.VerifyControlAction(action, params, envelope)
		return err
	}

	// F3: reserved bytes differ (byte 17 in 16-19, byte 33 in 32-47) but
	// every declared field matches → verification must pass.
	f3Params := jiabaidaF3TestParams(t)
	f3Block, err := compileF3Block(f3Params)
	if err != nil {
		t.Fatalf("compileF3Block error = %v", err)
	}
	reserved := append([]byte(nil), f3Block...)
	reserved[17] ^= 0xFF
	reserved[33] ^= 0xFF
	if err := verify("write_system_parameters", f3Params, reserved, 0xF3); err != nil {
		t.Fatalf("F3 readback with differing reserved bytes rejected: %v", err)
	}
	// F3: CRC bytes differ (declared fields identical) → still passes, since
	// a real BMS may recompute the CRC.
	recrc := append([]byte(nil), f3Block...)
	recrc[50] ^= 0xFF
	recrc[51] ^= 0xFF
	if err := verify("write_system_parameters", f3Params, recrc, 0xF3); err != nil {
		t.Fatalf("F3 readback with differing CRC bytes rejected: %v", err)
	}
	// F3: a declared field differs (balance_diff at bytes 10-11) → fail.
	declared := append([]byte(nil), f3Block...)
	declared[10] ^= 0xFF
	if err := verify("write_system_parameters", f3Params, declared, 0xF3); err == nil {
		t.Fatal("F3 readback with differing declared field accepted")
	}
	// F3: truncated readback (declared span out of bounds) → fail.
	trunc := append([]byte(nil), f3Block...)
	if err := verify("write_system_parameters", f3Params, trunc[:45], 0xF3); err == nil {
		t.Fatal("F3 truncated readback accepted")
	}

	// F2: any declared byte differs → fail (byte 25 sits inside chg_ut_protect).
	f2Params := jiabaidaF2TestParams(t)
	f2Block, err := compileF2Block(f2Params)
	if err != nil {
		t.Fatalf("compileF2Block error = %v", err)
	}
	bad := append([]byte(nil), f2Block...)
	bad[25] ^= 0xFF
	if err := verify("write_protection_parameters", f2Params, bad, 0xF2); err == nil {
		t.Fatal("F2 readback with differing declared field accepted")
	}
	// F2: CRC bytes differ (declared fields identical) → passes.
	f2crc := append([]byte(nil), f2Block...)
	f2crc[51] ^= 0xFF
	f2crc[52] ^= 0xFF
	if err := verify("write_protection_parameters", f2Params, f2crc, 0xF2); err != nil {
		t.Fatalf("F2 readback with differing CRC bytes rejected: %v", err)
	}
}

func TestJiabaidaSimpleSetterPlansAndVerifier(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	cases := []struct {
		action     string
		params     string
		cmd        byte
		data       []byte
		atMostOnce bool
	}{
		{"force_balance", `{}`, 0xF5, []byte{0x00, 0x01}, true},
		{"test_charge_mos", `{}`, 0x0C, []byte{0x00, 0x01}, true},
		{"test_discharge_mos", `{}`, 0x0C, []byte{0x00, 0x02}, true},
		{"find_car", `{"enabled":true}`, 0xF1, []byte{0x18, 0x01}, false},
		{"find_car", `{"enabled":false}`, 0xF1, []byte{0x18, 0x00}, false},
		{"clear_alarm", `{}`, 0xE6, []byte{0x18, 0x81}, false},
		{"auto_test_edv", `{"rest_minutes":30}`, 0x0D, []byte{0x00, 0x1E}, true},
		{"set_static_correction_time", `{"minutes":120}`, 0xF7, []byte{0x00, 0x78}, false},
		{"set_report_interval", `{"static_interval_s":60,"charge_interval_s":10,"discharge_interval_s":30}`, 0xF8, []byte{0x00, 0x3C, 0x00, 0x0A, 0x00, 0x1E}, false},
		{"set_charge_time_window", `{"delay_s":100,"duration_s":200}`, 0xFA, []byte{0x00, 0x64, 0x00, 0xC8}, false},
		{"set_discharge_time_limit", `{"enabled":true,"days":100}`, 0xFB, []byte{0x01, 0x00, 0x64}, true},
		{"set_discharge_time_limit", `{"enabled":false,"days":0}`, 0xFB, []byte{0x00, 0x00, 0x00}, true},
	}
	for _, tc := range cases {
		plan, err := d.CompileControlActionPlan(tc.action, json.RawMessage(tc.params))
		if err != nil {
			t.Fatalf("CompileControlActionPlan(%s, %s) error = %v", tc.action, tc.params, err)
		}
		// AtMostOnce must match the catalog definition: only high/critical-risk
		// actions may be at-most-once (deviceaction registry invariant); the
		// idempotent low/medium-risk setters are safely retryable.
		if len(plan.Steps) != 2 || plan.Steps[0].Kind != "write" || plan.Steps[1].Kind != "readback" || plan.AtMostOnce != tc.atMostOnce {
			t.Fatalf("%s plan shape = %+v, want atMostOnce=%v", tc.action, plan, tc.atMostOnce)
		}
		frame := plan.Steps[0].TXData
		if frame[0] != 0xDD || frame[1] != 0x5A || frame[2] != tc.cmd || int(frame[3]) != len(tc.data) ||
			string(frame[4:4+len(tc.data)]) != string(tc.data) || !verifyJiabaidaChecksum(frame) {
			t.Fatalf("%s write frame = % X, want cmd %02X data % X", tc.action, frame, tc.cmd, tc.data)
		}
		// Verifier: ACK for the setter cmd plus the readback the action actually
		// reconciles.  test_charge_mos / test_discharge_mos read 0x0C and require
		// the tested MOS to report 1 (OK), so a generic sample would not exercise
		// the reconciliation those actions promise.
		readback := jiabaidaHardwareVersionReadback()
		if tc.cmd == 0x0C {
			readback = jiabaidaMOSStatusReadback(tc.data[1])
		}
		envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(tc.cmd), readback)
		data, err := d.VerifyControlAction(tc.action, json.RawMessage(tc.params), envelope)
		if err != nil {
			t.Fatalf("VerifyControlAction(%s) error = %v", tc.action, err)
		}
		if len(data) == 0 || data[0].Value != 1 {
			t.Fatalf("%s verified result = %+v", tc.action, data)
		}
		// A wrong ACK command must be rejected.
		badAck := tc.cmd ^ 0xFF
		bad := jiabaidaTestEnvelope(t, jiabaidaZeroAck(badAck), jiabaidaHardwareVersionReadback())
		if _, err := d.VerifyControlAction(tc.action, json.RawMessage(tc.params), bad); err == nil {
			t.Fatalf("%s accepted wrong ACK cmd %02X", tc.action, badAck)
		}
	}
}

func TestJiabaidaTestMOSReadbackSurfacesStatus(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	plan, err := d.CompileControlActionPlan("test_charge_mos", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	_ = plan
	// 0x0C readback: discharge status = 2 (NG), charge status = 1 (OK).
	readback := jiabaidaTestResponse(t, 0x0C, []byte{0x02, 0x01})
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x0C), readback)
	data, err := d.VerifyControlAction("test_charge_mos", json.RawMessage(`{}`), envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(test_charge_mos) error = %v", err)
	}
	got := map[string]float64{}
	for _, f := range data {
		got[f.Name] = f.Value
	}
	if got["discharge_mos_test_status"] != 2 || got["charge_mos_test_status"] != 1 {
		t.Fatalf("MOS test readback = %+v", got)
	}

	// Reconciliation, not mere parsing: the MOS under test must report 1 (OK).
	// Before 2026-09-23 the verifier only parsed the frame, so a device
	// reporting NG (2) or timeout (3) was accepted as a successful test.
	for _, bad := range []struct {
		name   string
		action string
		data   []byte
	}{
		{"charge reports NG", "test_charge_mos", []byte{0x00, 0x02}},
		{"charge reports timeout", "test_charge_mos", []byte{0x00, 0x03}},
		{"charge never tested", "test_charge_mos", []byte{0x00, 0x00}},
		{"discharge reports NG", "test_discharge_mos", []byte{0x02, 0x00}},
		{"discharge never tested", "test_discharge_mos", []byte{0x00, 0x00}},
	} {
		rb := jiabaidaTestResponse(t, 0x0C, bad.data)
		env := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x0C), rb)
		if _, err := d.VerifyControlAction(bad.action, json.RawMessage("{}"), env); err == nil {
			t.Errorf("%s: %s must fail verification", bad.action, bad.name)
		}
	}
}

func TestJiabaidaWriteCustomAttributesRoundTrip(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"custom_1":1,"custom_2":2,"custom_3":65535}`)
	plan, err := d.CompileControlActionPlan("write_custom_attributes", params)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("plan shape = %+v", plan)
	}
	frame := plan.Steps[0].TXData
	wantData := []byte{0x00, 0x01, 0x00, 0x02, 0xFF, 0xFF}
	if frame[2] != 0xF0 || string(frame[4:10]) != string(wantData) || !verifyJiabaidaChecksum(frame) {
		t.Fatalf("F0 write frame = % X", frame)
	}
	readback := jiabaidaTestResponse(t, 0xF0, wantData)
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0xF0), readback)
	data, err := d.VerifyControlAction("write_custom_attributes", params, envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(write_custom_attributes) error = %v", err)
	}
	for _, f := range data {
		if f.Name == "custom_attr_3" && f.Value != 65535 {
			t.Fatalf("custom_attr_3 = %v, want 65535", f.Value)
		}
	}
	// Readback carrying different values must be rejected.
	mismatch := jiabaidaTestResponse(t, 0xF0, []byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x00})
	bad := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0xF0), mismatch)
	if _, err := d.VerifyControlAction("write_custom_attributes", params, bad); err == nil {
		t.Fatal("mismatched F0 readback accepted")
	}
}

func TestJiabaidaWriteInternalResistance(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	// The catalog schema is scalar-only, so the fixed-30 F6 block is passed
	// as resistance_1..resistance_30.
	resistances := make([]int, 30)
	for i := range resistances {
		resistances[i] = 100
	}
	resistances[29] = -5 // negative 0.1mΩ is legal per protocol
	params := map[string]any{}
	for i, v := range resistances {
		params[fmt.Sprintf("resistance_%d", i+1)] = v
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal resistances: %v", err)
	}
	plan, err := d.CompileControlActionPlan("write_internal_resistance", raw)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	frame := plan.Steps[0].TXData
	if frame[2] != 0xF6 || frame[3] != 60 || !verifyJiabaidaChecksum(frame) {
		t.Fatalf("F6 write frame = % X", frame[:6])
	}
	if frame[4+58] != 0xFF || frame[4+59] != 0xFB {
		t.Fatalf("resistance[29] encoding = % X, want FF FB (-5)", frame[4+58:4+60])
	}
	readback := jiabaidaTestResponse(t, 0xF6, frame[4:64])
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0xF6), readback)
	data, err := d.VerifyControlAction("write_internal_resistance", raw, envelope)
	if err != nil || len(data) < 31 {
		t.Fatalf("VerifyControlAction(write_internal_resistance) = %d fields, err = %v", len(data), err)
	}
	// A missing scalar parameter is rejected at compile time.
	shortParams := map[string]any{}
	for i := 0; i < 29; i++ {
		shortParams[fmt.Sprintf("resistance_%d", i+1)] = 100
	}
	short, _ := json.Marshal(shortParams)
	if _, err := d.CompileControlActionPlan("write_internal_resistance", short); err == nil {
		t.Fatal("29-parameter resistance set accepted")
	}
}

func TestJiabaidaWriteSN(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"sn":"BMS-2026-001"}`)
	plan, err := d.CompileControlActionPlan("write_sn", params)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if len(plan.Steps) != 4 || !plan.RequiresFinally || plan.Steps[3].Kind != "finally" {
		t.Fatalf("write_sn plan = %+v", plan)
	}
	writeFrame := plan.Steps[1].TXData
	if writeFrame[2] != 0xA2 || writeFrame[3] != 13 || writeFrame[4] != 12 || string(writeFrame[5:17]) != "BMS-2026-001" {
		t.Fatalf("SN write frame = % X", writeFrame)
	}
	// Verifier: enter ack, write ack, SN readback matching, exit ack.
	snData := append([]byte{12}, []byte("BMS-2026-001")...)
	readback := jiabaidaTestResponse(t, 0xA2, snData)
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xA2), readback, jiabaidaZeroAck(0x01))
	data, err := d.VerifyControlAction("write_sn", params, envelope)
	if err != nil {
		t.Fatalf("VerifyControlAction(write_sn) error = %v", err)
	}
	foundSN := ""
	for _, f := range data {
		if f.Name == "serial_number" {
			foundSN = f.StringValue
		}
	}
	if foundSN != "BMS-2026-001" {
		t.Fatalf("write_sn readback sn = %q", foundSN)
	}
	// A readback carrying a different SN must be rejected.
	otherData := append([]byte{9}, []byte("DIFFERENT")...)
	bad := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xA2),
		jiabaidaTestResponse(t, 0xA2, otherData), jiabaidaZeroAck(0x01))
	if _, err := d.VerifyControlAction("write_sn", params, bad); err == nil {
		t.Fatal("mismatched SN readback accepted")
	}
	// Non-printable ASCII and oversized SN are rejected at compile time.
	if _, err := d.CompileControlActionPlan("write_sn", json.RawMessage(`{"sn":"bad	tab"}`)); err == nil {
		t.Fatal("non-printable SN accepted")
	}
	if _, err := d.CompileControlActionPlan("write_sn", json.RawMessage(`{"sn":"01234567890123456789012345678901"}`)); err == nil {
		t.Fatal("32-char SN accepted")
	}
}

func TestJiabaidaParse0x0CAnd0xF0(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	frame0C := jiabaidaTestResponse(t, 0x0C, []byte{0x02, 0x01})
	data, err := d.ParseData(frame0C)
	if err != nil {
		t.Fatalf("ParseData(0x0C) error = %v", err)
	}
	got := map[string]float64{}
	for _, f := range data {
		got[f.Name] = f.Value
	}
	if got["discharge_mos_test_status"] != 2 || got["charge_mos_test_status"] != 1 {
		t.Fatalf("0x0C parse = %+v", got)
	}
	frameF0 := jiabaidaTestResponse(t, 0xF0, []byte{0x00, 0x0A, 0x00, 0x14, 0x00, 0x1E})
	data, err = d.ParseData(frameF0)
	if err != nil {
		t.Fatalf("ParseData(0xF0) error = %v", err)
	}
	got = map[string]float64{}
	for _, f := range data {
		got[f.Name] = f.Value
	}
	if got["custom_attr_1"] != 10 || got["custom_attr_2"] != 20 || got["custom_attr_3"] != 30 {
		t.Fatalf("0xF0 parse = %+v", got)
	}
	// Short payloads are rejected.
	if _, err := d.parse0x0C([]byte{0x01}); err == nil {
		t.Fatal("short 0x0C accepted")
	}
	if _, err := d.parse0xF0([]byte{0x00, 0x01}); err == nil {
		t.Fatal("short 0xF0 accepted")
	}
}

// ============================================================================
// P0-3: ACK checksum verification — negative paths
// ============================================================================

func TestJiabaidaVerifyMOSPolicyRejectsBadAckChecksum(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`)
	// Structurally the frame is a valid zero-length ACK (DD E1 00 00 ?? ?? 77),
	// but the checksum bytes must be 00 00 for LEN=0 — FF FF must be rejected.
	badAck := []byte{0xDD, 0xE1, 0x00, 0x00, 0xFF, 0xFF, 0x77}
	envelope := jiabaidaMOSEnvelope(t, badAck, 0x02)
	if _, err := d.VerifyControlAction("set_mos_policy", params, envelope); err == nil {
		t.Fatal("set_mos_policy accepted ACK with invalid checksum")
	}
	// Sanity: the same envelope with the correct checksum passes.
	good := jiabaidaMOSEnvelope(t, jiabaidaZeroAck(0xE1), 0x02)
	if _, err := d.VerifyControlAction("set_mos_policy", params, good); err != nil {
		t.Fatalf("set_mos_policy valid envelope rejected: %v", err)
	}
}

func TestJiabaidaFactoryWriteRejectsBadEnterFactoryChecksum(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := jiabaidaF2TestParams(t)
	plan, err := d.CompileControlActionPlan("write_protection_parameters", params)
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	block := plan.Steps[1].TXData[4:57]
	// Enter-factory ACK with a structurally valid shape but wrong checksum.
	badEnter := []byte{0xDD, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0x77}
	envelope := jiabaidaTestEnvelope(t, badEnter, jiabaidaZeroAck(0xF2),
		jiabaidaTestResponse(t, 0xF2, block), jiabaidaZeroAck(0x01))
	if _, err := d.VerifyControlAction("write_protection_parameters", params, envelope); err == nil {
		t.Fatal("write_protection_parameters accepted enter-factory ACK with invalid checksum")
	}
	// Sanity: the same envelope with the correct enter-factory checksum passes.
	good := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0x00), jiabaidaZeroAck(0xF2),
		jiabaidaTestResponse(t, 0xF2, block), jiabaidaZeroAck(0x01))
	if _, err := d.VerifyControlAction("write_protection_parameters", params, good); err != nil {
		t.Fatalf("write_protection_parameters valid envelope rejected: %v", err)
	}
}

// ============================================================================
// P0-2: set_mos_policy fet_status bit-level readback reconciliation
// ============================================================================

func TestJiabaidaVerifyMOSPolicyFetReconciliation(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	cases := []struct {
		name   string
		params string
		fet    byte
	}{
		{"charge closed discharge open", `{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`, 0x02},
		{"charge open discharge closed", `{"charge_software_closed":false,"discharge_software_closed":true,"priority":"user"}`, 0x01},
		{"both open", `{"charge_software_closed":false,"discharge_software_closed":false,"priority":"user"}`, 0x03},
		{"both closed", `{"charge_software_closed":true,"discharge_software_closed":true,"priority":"user"}`, 0x00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := jiabaidaMOSEnvelope(t, jiabaidaZeroAck(0xE1), tc.fet)
			data, err := d.VerifyControlAction("set_mos_policy", json.RawMessage(tc.params), envelope)
			if err != nil {
				t.Fatalf("VerifyControlAction error = %v", err)
			}
			if len(data) < 2 || data[0].Name != "mos_ack" || data[0].Value != 1 {
				t.Fatalf("verified result = %+v", data)
			}
			found := false
			for _, s := range data[1:] {
				if s.Name == "fet_status" {
					found = true
					if s.Value != float64(tc.fet) {
						t.Fatalf("fet_status = %v, want %d", s.Value, tc.fet)
					}
				}
			}
			if !found {
				t.Fatalf("readback missing fet_status: %+v", data)
			}
		})
	}
}

func TestJiabaidaVerifyMOSPolicyFetReconciliationMismatch(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`)
	// Charge close requested, but the readback reports both FETs open (0x03):
	// bit0=1 contradicts the requested closed charge MOS.
	envelope := jiabaidaMOSEnvelope(t, jiabaidaZeroAck(0xE1), 0x03)
	_, err := d.VerifyControlAction("set_mos_policy", params, envelope)
	if err == nil {
		t.Fatal("mismatched fet_status accepted")
	}
	if !strings.Contains(err.Error(), "fet") || !strings.Contains(err.Error(), "protection") {
		t.Fatalf("error should mention fet bits and the protection hint: %v", err)
	}
}

func TestJiabaidaVerifyMOSPolicyFetStatusMissing(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	params := json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`)
	// Second step parses fine (0x05 hardware version) but its projection has
	// no fet_status entry, so reconciliation must fail.
	envelope := jiabaidaTestEnvelope(t, jiabaidaZeroAck(0xE1), jiabaidaHardwareVersionReadback())
	if _, err := d.VerifyControlAction("set_mos_policy", params, envelope); err == nil {
		t.Fatal("set_mos_policy accepted readback without fet_status")
	}
}

// ============================================================================
// parse0xAA / parse0x05 short-input defenses
// ============================================================================

func TestParse0xAA_TooShort(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	_, err := d.parse0xAA(make([]byte, 22))
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrDataTooShort {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrDataTooShort)
	}
}

func TestParse0x05_Empty(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	_, err := d.parse0x05([]byte{})
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if parseErr.Code != ErrDataTooShort {
		t.Errorf("error code: got %d, want %d", parseErr.Code, ErrDataTooShort)
	}
}
