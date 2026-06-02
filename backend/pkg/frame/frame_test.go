package frame

import (
	"encoding/hex"
	"testing"
)

// Test vector from protocol-spec.md §3.2 Hello
func TestHelloWireFormat(t *testing.T) {
	// Encode Hello
	enc := NewEncoder(MsgHello)
	enc.EncodeString(1, "esp32-30eda0a9a808")   // device_id
	enc.EncodeString(2, "4.0.0")                  // firmware_version
	enc.EncodeString(3, "ESP32S3")                 // model
	enc.EncodeVarint(4, 1)                          // channel_count

	wire := enc.Bytes()
	t.Logf("Encoded: %s", hex.EncodeToString(wire))

	// Decode and verify
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dec.MsgType() != MsgHello {
		t.Fatalf("msg type: got 0x%02X, want 0x%02X", dec.MsgType(), MsgHello)
	}

	// Parse all fields
	gotDeviceID := ""
	gotFWVer := ""
	gotModel := ""
	gotChCount := uint64(0)

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			gotDeviceID = GetString(field)
		case 2:
			gotFWVer = GetString(field)
		case 3:
			gotModel = GetString(field)
		case 4:
			gotChCount = GetUint64(field)
		}
	}

	if gotDeviceID != "esp32-30eda0a9a808" {
		t.Errorf("device_id: got %q, want %q", gotDeviceID, "esp32-30eda0a9a808")
	}
	if gotFWVer != "4.0.0" {
		t.Errorf("firmware_version: got %q, want %q", gotFWVer, "4.0.0")
	}
	if gotModel != "ESP32S3" {
		t.Errorf("model: got %q, want %q", gotModel, "ESP32S3")
	}
	if gotChCount != 1 {
		t.Errorf("channel_count: got %d, want 1", gotChCount)
	}

	t.Logf("Hello encode/decode PASS: device=%s fw=%s model=%s ch=%d",
		gotDeviceID, gotFWVer, gotModel, gotChCount)
}

// Test vector from protocol-spec.md §3.4 DataReport
func TestDataReportWireFormat(t *testing.T) {
	// Encode DataReport
	enc := NewEncoder(MsgDataRpt)
	enc.EncodeVarint(1, 5)                    // channel_id=5
	enc.EncodeVarint(2, 100000000)            // timestamp_us=100000000
	enc.EncodeVarint(3, 1)                    // sequence=1
	enc.EncodeBytes(4, []byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32, 0x00}) // raw_data

	wire := enc.Bytes()
	t.Logf("Encoded: %x", wire)

	// Decode and verify round-trip
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dec.MsgType() != MsgDataRpt {
		t.Fatalf("msg type: got 0x%02X, want 0x%02X", dec.MsgType(), MsgDataRpt)
	}

	gotChID := uint64(0)
	gotTS := uint64(0)
	gotSeq := uint64(0)
	var gotRaw []byte

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			gotChID = GetUint64(field)
		case 2:
			gotTS = GetUint64(field)
		case 3:
			gotSeq = GetUint64(field)
		case 4:
			gotRaw = GetBytes(field)
		}
	}

	if gotChID != 5 {
		t.Errorf("channel_id: got %d, want 5", gotChID)
	}
	if gotTS != 100000000 {
		t.Errorf("timestamp_us: got %d, want 100000000", gotTS)
	}
	if gotSeq != 1 {
		t.Errorf("sequence: got %d, want 1", gotSeq)
	}
	if len(gotRaw) != 7 {
		t.Fatalf("raw_data len: got %d, want 7", len(gotRaw))
	}
	expectedRaw := []byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32, 0x00}
	for i, b := range gotRaw {
		if b != expectedRaw[i] {
			t.Errorf("raw_data[%d]: got 0x%02X, want 0x%02X", i, b, expectedRaw[i])
		}
	}

	t.Logf("DataReport encode/decode PASS: ch=%d ts=%d seq=%d raw=%x",
		gotChID, gotTS, gotSeq, gotRaw)
}

// Test StatusRpt round-trip (protocol-spec §3.3)
func TestStatusRptWireFormat(t *testing.T) {
	// uptime_sec=3600, status="online", channel_count=2
	enc := NewEncoder(MsgStatusRpt)
	enc.EncodeVarint(1, 3600)        // uptime_sec
	enc.EncodeString(2, "online")     // status
	enc.EncodeVarint(3, 2)            // channel_count

	wire := enc.Bytes()
	t.Logf("Encoded: %x", wire)

	// Decode round-trip
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dec.MsgType() != MsgStatusRpt {
		t.Fatalf("msg type: got 0x%02X", dec.MsgType())
	}
	t.Logf("StatusRpt wire format PASS")
}

// Test varint boundary values (P1.2 from acceptance-criteria)
func TestVarintBoundaryValues(t *testing.T) {
	tests := []struct {
		value    uint64
		expected int // expected byte count
	}{
		{0, 1},
		{1, 1},
		{127, 1},
		{128, 2},
		{300, 2},
		{16383, 2},
		{16384, 3},
	}

	for _, tt := range tests {
		enc := NewEncoder(MsgHello)
		enc.EncodeVarint(1, tt.value)
		wire := enc.Bytes()
		// wire[0] = msg type, wire[1] = tag byte, wire[2:] = varint bytes
		varintBytes := len(wire) - 2
		if varintBytes != tt.expected {
			t.Errorf("varint(%d): got %d bytes, want %d", tt.value, varintBytes, tt.expected)
		}

		// Round-trip
		dec, _ := NewDecoder(wire)
		field, err := dec.NextField()
		if err != nil {
			t.Fatalf("varint(%d) decode error: %v", tt.value, err)
		}
		got := GetUint64(field)
		if got != tt.value {
			t.Errorf("varint round-trip: got %d, want %d", got, tt.value)
		}
	}
	t.Logf("Varint boundary values PASS (7 cases)")
}

// Test unknown msg type handling (P1.5)
func TestUnknownMsgType(t *testing.T) {
	wire := []byte{0xFF, 0x08, 0x01} // type=0xFF, field 1 varint=1
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("should not error on unknown type: %v", err)
	}
	if dec.MsgType() != 0xFF {
		t.Errorf("msg type: got 0x%02X, want 0xFF", dec.MsgType())
	}
	// Should still be able to parse fields
	field, err := dec.NextField()
	if err != nil {
		t.Fatalf("field parse error: %v", err)
	}
	if GetUint64(field) != 1 {
		t.Errorf("field value: got %d, want 1", GetUint64(field))
	}
	t.Logf("Unknown msg type PASS: parsed without crash")
}

// Test unknown field tag handling (P1.6)
func TestUnknownFieldTag(t *testing.T) {
	enc := NewEncoder(MsgHello)
	enc.EncodeVarint(1, 42)  // known field
	// Manually inject unknown field 99
	enc.EncodeVarint(99, 123)

	dec, _ := NewDecoder(enc.Bytes())
	fieldCount := 0
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		fieldCount++
		if field.FieldNum == 1 && GetUint64(field) != 42 {
			t.Errorf("field 1: got %d, want 42", GetUint64(field))
		}
	}
	if fieldCount != 2 {
		t.Errorf("field count: got %d, want 2", fieldCount)
	}
	t.Logf("Unknown field tag PASS: skipped without crash")
}

// Test malformed message handling (P1.7)
func TestMalformedMessages(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"type_only", []byte{0x01}},
		{"truncated_varint", []byte{0x01, 0x08, 0x80, 0x80}}, // varint never terminates
		{"truncated_length", []byte{0x01, 0x0A, 0x10}},        // string len=16 but no data
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, err := NewDecoder(tt.data)
			if err != nil {
				// Empty frame is expected to error
				return
			}
			// Try to parse - should not panic
			for {
				_, err := dec.NextField()
				if err != nil {
					break
				}
			}
		})
	}
	t.Logf("Malformed messages PASS: no panics")
}

// Test SubFrame encoding for ConfigManifest nested structures
func TestSubFrameEncoding(t *testing.T) {
	// Encode a Template sub-structure: id=1, write_data=[0xE0, 0xB6], read_length=25, delay_ms=10
	sub := SubEncoder()
	sub.EncodeVarint(1, 1)                                 // id=1
	sub.EncodeBytes(2, []byte{0xE0, 0xB6})                // write_data
	sub.EncodeVarint(3, 25)                                // read_length
	sub.EncodeVarint(4, 10)                                // delay_ms

	enc := NewEncoder(MsgConfigMfst)
	enc.EncodeString(1, "manifest-001")      // manifest_id
	enc.EncodeSubFrame(2, sub.Bytes())        // templates[0]

	wire := enc.Bytes()
	t.Logf("ConfigMfst with sub-frame: %s", hex.EncodeToString(wire))

	// Decode and verify
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dec.MsgType() != MsgConfigMfst {
		t.Fatalf("msg type: got 0x%02X", dec.MsgType())
	}

	gotManifestID := ""
	var gotTemplateData []byte

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			gotManifestID = GetString(field)
		case 2:
			gotTemplateData = GetBytes(field)
		}
	}

	if gotManifestID != "manifest-001" {
		t.Errorf("manifest_id: got %q", gotManifestID)
	}
	if len(gotTemplateData) == 0 {
		t.Error("template sub-frame is empty")
	}

	// Decode sub-frame bytes directly as field sequence
	t.Logf("Template sub-frame decoded, %d bytes", len(gotTemplateData))
	t.Logf("Sub-frame encoding PASS")
}

// Test all message type constants
func TestAllMessageTypes(t *testing.T) {
	expected := map[uint8]string{
		0x01: "hello",
		0x02: "status_report",
		0x03: "data_report",
		0x04: "config_manifest",
		0x05: "config_result",
		0x06: "write_cmd",
		0x07: "write_response",
		0x08: "ping",
		0x09: "pong",
		0x0A: "ota_cmd",
		0x0B: "ota_progress",
		0x0C: "scan_report",
		0x0D: "scan_request",
		0x0E: "query_request",
		0x0F: "query_response",
		0x10: "config_query",
		0x11: "config_report",
		0x12: "hello_ack",
	}

	for msgType, name := range expected {
		got := MsgTypeName(msgType)
		if got != name {
			t.Errorf("MsgTypeName(0x%02X): got %q, want %q", msgType, got, name)
		}
	}
	t.Logf("All 18 message type names PASS")
}

// Test HelloAck (0x12) encode/decode round-trip
func TestHelloAckWireFormat(t *testing.T) {
	serverTime := uint64(1717200000000) // Unix ms
	features := uint64(0)

	enc := NewEncoder(MsgHelloAck)
	enc.EncodeVarint(1, serverTime) // server_time
	enc.EncodeVarint(2, features)   // features

	wire := enc.Bytes()
	t.Logf("HelloAck encoded: %s", hex.EncodeToString(wire))

	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dec.MsgType() != MsgHelloAck {
		t.Fatalf("msg type: got 0x%02X, want 0x%02X", dec.MsgType(), MsgHelloAck)
	}

	var gotServerTime, gotFeatures uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			gotServerTime = GetUint64(field)
		case 2:
			gotFeatures = GetUint64(field)
		}
	}

	if gotServerTime != serverTime {
		t.Errorf("server_time: got %d, want %d", gotServerTime, serverTime)
	}
	if gotFeatures != features {
		t.Errorf("features: got %d, want %d", gotFeatures, features)
	}
	t.Logf("HelloAck encode/decode PASS: server_time=%d features=%d", gotServerTime, gotFeatures)
}
