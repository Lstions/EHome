package parser

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

// --- Modbus CRC Tests ---

func TestModbusCRC16(t *testing.T) {
	// Known Modbus CRC for function code 03 read request
	data := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02}
	crc := ModbusCRC16(data)
	// Expected CRC for this frame: 0x0BC4 (the uint16 value; LE bytes are 0xC4 0x0B)
	if crc != 0x0BC4 {
		t.Errorf("ModbusCRC16 = 0x%04X, want 0x0BC4", crc)
	}
}

// --- StripModbusHeader Tests ---

func TestStripModbusHeader_Valid(t *testing.T) {
	// PRS-3001 response: addr=01, func=03, count=06, data=000000000014, CRC=bae0
	raw := []byte{0x01, 0x03, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x14, 0xE0, 0xBA}
	data, err := StripModbusHeader(raw)
	if err != nil {
		t.Fatalf("StripModbusHeader error: %v", err)
	}
	if len(data) != 6 {
		t.Errorf("data length = %d, want 6", len(data))
	}
}

func TestStripModbusHeader_TooShort(t *testing.T) {
	_, err := StripModbusHeader([]byte{0x01, 0x03})
	if err == nil {
		t.Error("expected error for too-short frame")
	}
}

func TestStripModbusHeader_Exception(t *testing.T) {
	raw := []byte{0x01, 0x83, 0x02, 0xC0, 0xF1} // exception: func=0x83, code=0x02
	_, err := StripModbusHeader(raw)
	if err == nil {
		t.Error("expected error for exception response")
	}
}

// --- ConfigParser Tests ---

func TestNewConfigParser_Empty(t *testing.T) {
	_, err := NewConfigParser(nil)
	if err == nil {
		t.Error("expected error for nil parser")
	}

	_, err = NewConfigParser(json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for empty parser")
	}
}

func TestNewConfigParser_Modbus(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "modbus",
		"fields": [
			{"name": "rainfall", "type": "uint16", "unit": "mm", "scale": 0.1, "offset": 0, "length": 2},
			{"name": "illuminance", "type": "uint32", "unit": "Lux", "scale": 1.0, "offset": 4, "length": 4}
		]
	}`)

	cp, err := NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser error: %v", err)
	}
	if cp.DataFormat != "modbus" {
		t.Errorf("DataFormat = %q, want %q", cp.DataFormat, "modbus")
	}
	if len(cp.Fields) != 2 {
		t.Fatalf("Fields count = %d, want 2", len(cp.Fields))
	}
	if cp.Fields[0].Name != "rainfall" {
		t.Errorf("Field[0].Name = %q, want %q", cp.Fields[0].Name, "rainfall")
	}
	if cp.Fields[0].Scale != 0.1 {
		t.Errorf("Field[0].Scale = %v, want 0.1", cp.Fields[0].Scale)
	}
}

func TestConfigParser_Parse_PRS3001(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "modbus",
		"fields": [
			{"name": "rainfall", "type": "uint16", "unit": "mm", "scale": 0.1, "offset": 0, "length": 2},
			{"name": "illuminance", "type": "uint32", "unit": "Lux", "scale": 1.0, "offset": 4, "length": 4}
		]
	}`)

	cp, err := NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser error: %v", err)
	}

	// PRS-3001 Modbus response: addr=01, func=03, count=06, data=0000 0000 0014, CRC
	// rainfall=0 (0*0.1=0.0mm), illuminance=20 (0*0.1=0.0, 20*1.0=20 Lux)
	// But offset 4 means bytes 4-7 in data after header strip, and count=6 so only 6 bytes
	// Let's use a count=8 (4 registers) response for proper test
	// addr=01, func=03, count=08, data=[00 00] [00 00] [00 00] [00 14], CRC
	raw := make([]byte, 13) // 1+1+1+8+2
	raw[0] = 0x01           // addr
	raw[1] = 0x03           // func
	raw[2] = 0x08           // byte count
	// reg0-1: rainfall = 0
	binary.BigEndian.PutUint16(raw[3:5], 0x0000)
	// reg2-3: padding
	binary.BigEndian.PutUint16(raw[5:7], 0x0000)
	// reg4-5: high 16 of illuminance
	binary.BigEndian.PutUint16(raw[7:9], 0x0000)
	// reg6-7: low 16 of illuminance = 20
	binary.BigEndian.PutUint16(raw[9:11], 0x0014)

	// Compute CRC
	crc := ModbusCRC16(raw[:11])
	raw[11] = byte(crc & 0xFF)
	raw[12] = byte(crc >> 8)

	fields, err := cp.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("fields count = %d, want 2", len(fields))
	}
	if fields[0].Name != "rainfall" {
		t.Errorf("fields[0].Name = %q, want %q", fields[0].Name, "rainfall")
	}
	if fields[0].Value != 0.0 {
		t.Errorf("fields[0].Value = %v, want 0.0", fields[0].Value)
	}
	if fields[0].Unit != "mm" {
		t.Errorf("fields[0].Unit = %q, want %q", fields[0].Unit, "mm")
	}
	if fields[1].Name != "illuminance" {
		t.Errorf("fields[1].Name = %q, want %q", fields[1].Name, "illuminance")
	}
	if fields[1].Value != 20.0 {
		t.Errorf("fields[1].Value = %v, want 20.0", fields[1].Value)
	}
}

func TestConfigParser_Parse_Binary(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "binary",
		"fields": [
			{"name": "temperature", "type": "int16", "unit": "°C", "scale": 0.1, "offset": 0, "length": 2},
			{"name": "humidity", "type": "uint16", "unit": "%RH", "scale": 0.1, "offset": 2, "length": 2}
		]
	}`)

	cp, err := NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser error: %v", err)
	}

	raw := make([]byte, 4)
	binary.BigEndian.PutUint16(raw[0:2], 0x00FB) // 251 → 25.1°C
	binary.BigEndian.PutUint16(raw[2:4], 0x028A) // 650 → 65.0%RH

	fields, err := cp.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("fields count = %d, want 2", len(fields))
	}
	if fields[0].Value != 25.1 {
		t.Errorf("temperature = %v, want 25.1", fields[0].Value)
	}
	if fields[1].Value != 65.0 {
		t.Errorf("humidity = %v, want 65.0", fields[1].Value)
	}
}

func TestConfigParser_ParseSingle(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "modbus",
		"fields": [
			{"name": "wind_direction", "type": "uint16", "unit": "°", "scale": 0.1, "offset": 0, "length": 2}
		]
	}`)

	cp, err := NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser error: %v", err)
	}

	// addr=01, func=03, count=02, data=012C (300→30.0°), CRC
	raw := make([]byte, 7)
	raw[0] = 0x01
	raw[1] = 0x03
	raw[2] = 0x02
	binary.BigEndian.PutUint16(raw[3:5], 0x012C)
	crc := ModbusCRC16(raw[:5])
	raw[5] = byte(crc & 0xFF)
	raw[6] = byte(crc >> 8)

	value, unit, err := cp.ParseSingle(raw)
	if err != nil {
		t.Fatalf("ParseSingle error: %v", err)
	}
	if value != 30.0 {
		t.Errorf("value = %v, want 30.0", value)
	}
	if unit != "°" {
		t.Errorf("unit = %q, want %q", unit, "°")
	}
}

// --- ParserRule Tests ---

func TestParseResponseByRule_Uint16(t *testing.T) {
	rule := `{"type":"modbus_register","byte_offset":3,"byte_length":2,"data_type":"uint16","scale":0.1,"unit":"mm"}`
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64} // offset 3 = 0x0064 = 100 → 10.0mm
	value, unit, err := ParseResponseByRule(rule, raw)
	if err != nil {
		t.Fatalf("ParseResponseByRule error: %v", err)
	}
	if value != 10.0 {
		t.Errorf("value = %v, want 10.0", value)
	}
	if unit != "mm" {
		t.Errorf("unit = %q, want %q", unit, "mm")
	}
}

// --- Named Parser Registry Tests ---

func TestRegistry_ModbusUint16(t *testing.T) {
	p, err := Get("modbus_uint16")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	// Modbus FC03 response with uint16 data
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64, 0x00, 0x00} // addr=01, func=03, count=02, data=0x0064
	fields, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("fields count = %d, want 1", len(fields))
	}
	if fields[0].Value != 100.0 {
		t.Errorf("value = %v, want 100.0", fields[0].Value)
	}
}

func TestRegistry_ModbusUint16Div10(t *testing.T) {
	p, err := Get("modbus_uint16_div10")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64, 0x00, 0x00}
	fields, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if fields[0].Value != 10.0 {
		t.Errorf("value = %v, want 10.0", fields[0].Value)
	}
}

// --- ParseModbusResponse Tests ---

func TestParseModbusResponse_NamedParser(t *testing.T) {
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64, 0x00, 0x00}
	value, err := ParseModbusResponse(raw, "modbus_uint16")
	if err != nil {
		t.Fatalf("ParseModbusResponse error: %v", err)
	}
	if value != 100.0 {
		t.Errorf("value = %v, want 100.0", value)
	}
}

func TestParseModbusResponse_JSONRule(t *testing.T) {
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64, 0x00, 0x00}
	rule := `{"type":"modbus_register","byte_offset":3,"byte_length":2,"data_type":"uint16","scale":0.1,"unit":"mm"}`
	value, err := ParseModbusResponse(raw, rule)
	if err != nil {
		t.Fatalf("ParseModbusResponse error: %v", err)
	}
	if value != 10.0 {
		t.Errorf("value = %v, want 10.0", value)
	}
}

func TestParseModbusResponse_Unknown(t *testing.T) {
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x64}
	_, err := ParseModbusResponse(raw, "nonexistent_parser")
	if err == nil {
		t.Error("expected error for unknown parser")
	}
}

// --- Template Tests ---

func TestRenderCommandTemplate_ModbusCRC(t *testing.T) {
	tmpl := "{addr:02X}03 00 00 00 02 {crc}"
	vars := TemplateVars{Addr: 1}
	data, err := RenderCommandTemplate(tmpl, vars)
	if err != nil {
		t.Fatalf("RenderCommandTemplate error: %v", err)
	}
	// Expected: 01 03 00 00 00 02 C4 0B
	if len(data) != 8 {
		t.Fatalf("data length = %d, want 8", len(data))
	}
	if data[0] != 0x01 || data[1] != 0x03 {
		t.Errorf("data[0:2] = %02X %02X, want 01 03", data[0], data[1])
	}
	// Verify CRC
	crc := ModbusCRC16(data[:6])
	if data[6] != byte(crc&0xFF) || data[7] != byte(crc>>8) {
		t.Errorf("CRC = %02X %02X, want %02X %02X", data[6], data[7], byte(crc&0xFF), byte(crc>>8))
	}
}

func TestRenderCommandTemplate_WithParams(t *testing.T) {
	tmpl := "{addr:02X}06 00 00 {new_addr:04X} {crc}"
	vars := TemplateVars{
		Addr:   1,
		Params: map[string]interface{}{"new_addr": 2},
	}
	data, err := RenderCommandTemplate(tmpl, vars)
	if err != nil {
		t.Fatalf("RenderCommandTemplate error: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("data length = %d, want 8", len(data))
	}
	// new_addr=2 should appear as 00 02 at offset 4
	if data[4] != 0x00 || data[5] != 0x02 {
		t.Errorf("data[4:6] = %02X %02X, want 00 02", data[4], data[5])
	}
}

// --- Endianness Tests ---

func TestConfigParser_LittleEndian(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "binary",
		"fields": [
			{"name": "value", "type": "uint16", "unit": "", "scale": 1.0, "offset": 0, "length": 2, "endian": "little"}
		]
	}`)

	cp, err := NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser error: %v", err)
	}

	raw := []byte{0x64, 0x00} // LE: 0x0064 = 100
	fields, err := cp.Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if fields[0].Value != 100.0 {
		t.Errorf("value = %v, want 100.0", fields[0].Value)
	}
}

// --- Exception Code Tests ---

func TestModbusExceptionMessage(t *testing.T) {
	tests := []struct {
		code byte
		want string
	}{
		{0x01, "illegal function"},
		{0x02, "illegal data address"},
		{0x03, "illegal data value"},
		{0x04, "slave device failure"},
		{0xFF, "unknown exception code 0xFF"},
	}
	for _, tt := range tests {
		got := ModbusExceptionMessage(tt.code)
		if got != tt.want {
			t.Errorf("ModbusExceptionMessage(0x%02X) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
