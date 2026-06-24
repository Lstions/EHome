package api

import (
	"math"
	"testing"
)

func TestModbusCRC16(t *testing.T) {
	// Standard Modbus CRC16 test vectors
	// CRC of [0x01, 0x03, 0x00, 0x00, 0x00, 0x02] = 0x0BC4
	data := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02}
	crc := ModbusCRC16(data)
	if crc != 0x0BC4 {
		t.Errorf("ModbusCRC16([01 03 00 00 00 02]) = 0x%04X, want 0x0BC4", crc)
	}

	// CRC of empty data
	crcEmpty := ModbusCRC16([]byte{})
	if crcEmpty != 0xFFFF {
		t.Errorf("ModbusCRC16([]) = 0x%04X, want 0xFFFF", crcEmpty)
	}

	// CRC of [0x01, 0x03, 0x00, 0x00, 0x00, 0x01] = 0x0A84
	data2 := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01}
	crc2 := ModbusCRC16(data2)
	if crc2 != 0x0A84 {
		t.Errorf("ModbusCRC16([01 03 00 00 00 01]) = 0x%04X, want 0x0A84", crc2)
	}
}

func TestRenderCommandTemplate_Simple(t *testing.T) {
	// Simple template without CRC
	vars := TemplateVars{
		Addr:   1,
		Params: nil,
	}
	result, err := RenderCommandTemplate("{addr:02X}0300000001", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01}
	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d", len(result), len(expected))
	}
	for i, b := range result {
		if b != expected[i] {
			t.Errorf("result[%d] = 0x%02X, want 0x%02X", i, b, expected[i])
		}
	}
}

func TestRenderCommandTemplate_WithCRC(t *testing.T) {
	// Template with CRC placeholder
	vars := TemplateVars{
		Addr:   1,
		Params: nil,
	}
	result, err := RenderCommandTemplate("{addr:02X}0300000002{crc}", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: [0x01, 0x03, 0x00, 0x00, 0x00, 0x02, CRC_LO, CRC_HI]
	// CRC of [0x01, 0x03, 0x00, 0x00, 0x00, 0x02] = 0x0BC4
	// Little-endian: [0xC4, 0x0B]
	expected := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xC4, 0x0B}
	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d (got %x)", len(result), len(expected), result)
	}
	for i, b := range result {
		if b != expected[i] {
			t.Errorf("result[%d] = 0x%02X, want 0x%02X", i, b, expected[i])
		}
	}
}

func TestRenderCommandTemplate_WithParams(t *testing.T) {
	// Template with params (e.g., change address)
	vars := TemplateVars{
		Addr: 1,
		Params: map[string]interface{}{
			"new_addr": 5,
		},
	}
	result, err := RenderCommandTemplate("{addr:02X}06 00 00 {new_addr:02X} {crc}", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: [0x01, 0x06, 0x00, 0x00, 0x05, CRC_LO, CRC_HI]
	// CRC of [0x01, 0x06, 0x00, 0x00, 0x05] = ?
	data := []byte{0x01, 0x06, 0x00, 0x00, 0x05}
	crc := ModbusCRC16(data)
	expected := append(data, byte(crc&0xFF), byte(crc>>8))

	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d (got %x)", len(result), len(expected), result)
	}
	for i, b := range result {
		if b != expected[i] {
			t.Errorf("result[%d] = 0x%02X, want 0x%02X", i, b, expected[i])
		}
	}
}

func TestRenderCommandTemplate_WithBaudCode(t *testing.T) {
	// Template with baud_code param
	vars := TemplateVars{
		Addr: 3,
		Params: map[string]interface{}{
			"baud_code": 0x0004,
		},
	}
	result, err := RenderCommandTemplate("{addr:02X}030020{baud_code:04X}{crc}", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: [0x03, 0x03, 0x00, 0x20, 0x00, 0x04, CRC_LO, CRC_HI]
	data := []byte{0x03, 0x03, 0x00, 0x20, 0x00, 0x04}
	crc := ModbusCRC16(data)
	expected := append(data, byte(crc&0xFF), byte(crc>>8))

	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d (got %x)", len(result), len(expected), result)
	}
	for i, b := range result {
		if b != expected[i] {
			t.Errorf("result[%d] = 0x%02X, want 0x%02X", i, b, expected[i])
		}
	}
}

func TestRenderCommandTemplate_Spaces(t *testing.T) {
	// Template with spaces (should be stripped)
	vars := TemplateVars{
		Addr:   1,
		Params: nil,
	}
	result, err := RenderCommandTemplate("{addr:02X} 03 00 00 00 02 {crc}", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xC4, 0x0B}
	if len(result) != len(expected) {
		t.Fatalf("result length = %d, want %d (got %x)", len(result), len(expected), result)
	}
	for i, b := range result {
		if b != expected[i] {
			t.Errorf("result[%d] = 0x%02X, want 0x%02X", i, b, expected[i])
		}
	}
}

func TestRenderCommandTemplate_UnknownVar(t *testing.T) {
	vars := TemplateVars{
		Addr:   1,
		Params: nil,
	}
	_, err := RenderCommandTemplate("{unknown:02X}03{crc}", vars)
	if err == nil {
		t.Error("expected error for unknown variable, got nil")
	}
}

func TestParseModbusResponse_Uint16(t *testing.T) {
	// Modbus FC03 response: [addr=01, func=03, byte_count=04, data_hi=00, data_lo=0A, ...]
	// Actually just need bytes 3-5 for the parser
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x0A}

	val, err := ParseModbusResponse(rawData, "modbus_uint16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseModbusResponse modbus_uint16 = %v, want 10.0", val)
	}
}

func TestParseModbusResponse_Uint16Div10(t *testing.T) {
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x64} // data = 100

	val, err := ParseModbusResponse(rawData, "modbus_uint16_div10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseModbusResponse modbus_uint16_div10 = %v, want 10.0", val)
	}
}

func TestParseModbusResponse_TooShort(t *testing.T) {
	rawData := []byte{0x01, 0x03}
	_, err := ParseModbusResponse(rawData, "modbus_uint16")
	if err == nil {
		t.Error("expected error for too-short response, got nil")
	}
}

func TestParseModbusResponse_UnknownParser(t *testing.T) {
	_, err := ParseModbusResponse([]byte{0x01, 0x03, 0x04, 0x00, 0x0A}, "unknown")
	if err == nil {
		t.Error("expected error for unknown parser, got nil")
	}
}

// --- P3-3: ParseResponseByRule tests ---

func TestParseResponseByRule_Uint16(t *testing.T) {
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x0A}
	ruleJSON := `{"byte_offset":3,"byte_length":2,"data_type":"uint16","scale":1,"unit":"mm"}`
	val, unit, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseResponseByRule uint16 = %v, want 10.0", val)
	}
	if unit != "mm" {
		t.Errorf("unit = %q, want %q", unit, "mm")
	}
}

func TestParseResponseByRule_Int16(t *testing.T) {
	// 0xFFFF as int16 = -1
	rawData := []byte{0xFF, 0xFF}
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"int16"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != -1.0 {
		t.Errorf("ParseResponseByRule int16 = %v, want -1.0", val)
	}
}

func TestParseResponseByRule_Uint16Div10(t *testing.T) {
	// Equivalent to modbus_uint16_div10: data=100, scale=0.1
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x64}
	ruleJSON := `{"byte_offset":3,"byte_length":2,"data_type":"uint16","scale":0.1,"unit":"°C"}`
	val, unit, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseResponseByRule uint16_div10 = %v, want 10.0", val)
	}
	if unit != "°C" {
		t.Errorf("unit = %q, want %q", unit, "°C")
	}
}

func TestParseResponseByRule_Uint32(t *testing.T) {
	// 0x00010000 = 65536
	rawData := []byte{0x00, 0x01, 0x00, 0x00}
	ruleJSON := `{"byte_offset":0,"byte_length":4,"data_type":"uint32"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 65536.0 {
		t.Errorf("ParseResponseByRule uint32 = %v, want 65536.0", val)
	}
}

func TestParseResponseByRule_Int32(t *testing.T) {
	// 0xFFFFFFFF as int32 = -1
	rawData := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	ruleJSON := `{"byte_offset":0,"byte_length":4,"data_type":"int32"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != -1.0 {
		t.Errorf("ParseResponseByRule int32 = %v, want -1.0", val)
	}
}

func TestParseResponseByRule_Float32(t *testing.T) {
	// IEEE 754 float32 for 3.14 ≈ 0x4048F5C3
	rawData := []byte{0x40, 0x48, 0xF5, 0xC3}
	ruleJSON := `{"byte_offset":0,"byte_length":4,"data_type":"float32"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// float32 precision: 3.14 may not be exact
	if math.Abs(val-3.14) > 0.001 {
		t.Errorf("ParseResponseByRule float32 = %v, want ~3.14", val)
	}
}

func TestParseResponseByRule_LittleEndian(t *testing.T) {
	// Little-endian uint16: 0x0A 0x00 = 10
	rawData := []byte{0x0A, 0x00}
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"uint16","endian":"little"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseResponseByRule little-endian uint16 = %v, want 10.0", val)
	}
}

func TestParseResponseByRule_Ascii(t *testing.T) {
	rawData := []byte{0x01, 0x03, 'O', 'K'}
	ruleJSON := `{"byte_offset":2,"byte_length":2,"data_type":"ascii"}`
	val, unit, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 0 {
		t.Errorf("ascii value should be 0, got %v", val)
	}
	if unit != "OK" {
		t.Errorf("ascii unit = %q, want %q", unit, "OK")
	}
}

func TestParseResponseByRule_ScaleAndOffset(t *testing.T) {
	// value=100, scale=0.1, offset=-5 → 100*0.1 + (-5) = 5.0
	rawData := []byte{0x00, 0x64}
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"uint16","scale":0.1,"offset":-5}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 5.0 {
		t.Errorf("ParseResponseByRule scale+offset = %v, want 5.0", val)
	}
}

func TestParseResponseByRule_DefaultScale(t *testing.T) {
	// No scale specified → defaults to 1.0 (not 0)
	rawData := []byte{0x00, 0x0A}
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"uint16"}`
	val, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseResponseByRule default scale = %v, want 10.0", val)
	}
}

func TestParseResponseByRule_DataTooShort(t *testing.T) {
	rawData := []byte{0x01}
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"uint16"}`
	_, _, err := ParseResponseByRule(ruleJSON, rawData)
	if err == nil {
		t.Error("expected error for data too short, got nil")
	}
}

func TestParseResponseByRule_InvalidRule(t *testing.T) {
	_, _, err := ParseResponseByRule("not json", []byte{0x01})
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseResponseByRule_UnsupportedDataType(t *testing.T) {
	ruleJSON := `{"byte_offset":0,"byte_length":2,"data_type":"bcd"}`
	_, _, err := ParseResponseByRule(ruleJSON, []byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for unsupported data_type, got nil")
	}
}

func TestParseModbusResponse_JSONRule(t *testing.T) {
	// Test that ParseModbusResponse delegates to ParseResponseByRule for JSON input
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x0A}
	ruleJSON := `{"byte_offset":3,"byte_length":2,"data_type":"uint16","scale":0.1,"unit":"°C"}`
	val, err := ParseModbusResponse(rawData, ruleJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 1.0 {
		t.Errorf("ParseModbusResponse JSON rule = %v, want 1.0", val)
	}
}

func TestParseModbusResponse_JSONRuleWithSpaces(t *testing.T) {
	// Test JSON detection works even with leading spaces
	rawData := []byte{0x00, 0x0A}
	ruleJSON := `  {"byte_offset":0,"byte_length":2,"data_type":"uint16"}`
	val, err := ParseModbusResponse(rawData, ruleJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseModbusResponse JSON rule (leading spaces) = %v, want 10.0", val)
	}
}

func TestParseModbusResponse_LegacyStillWorks(t *testing.T) {
	// Verify legacy named parsers still work after P3-3 changes
	rawData := []byte{0x01, 0x03, 0x04, 0x00, 0x0A}
	val, err := ParseModbusResponse(rawData, "modbus_uint16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 10.0 {
		t.Errorf("ParseModbusResponse legacy modbus_uint16 = %v, want 10.0", val)
	}
}

func TestToUint64(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected uint64
		hasErr   bool
	}{
		{float64(5), 5, false},
		{int(10), 10, false},
		{int64(20), 20, false},
		{uint64(30), 30, false},
		{"42", 42, false},
		{"0x10", 16, false},
		{"0xFF", 255, false},
		{"abc", 0, true},
		{nil, 0, true},
	}

	for _, tt := range tests {
		got, err := toUint64(tt.input)
		if tt.hasErr {
			if err == nil {
				t.Errorf("toUint64(%v) expected error, got nil", tt.input)
			}
		} else {
			if err != nil {
				t.Errorf("toUint64(%v) unexpected error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("toUint64(%v) = %d, want %d", tt.input, got, tt.expected)
			}
		}
	}
}
