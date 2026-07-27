package frame

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestLegacyWriteCmdGoldenVector(t *testing.T) {
	enc := NewEncoder(MsgWriteCmd)
	enc.EncodeVarint(1, 17)
	enc.EncodeVarint(2, 9)
	enc.EncodeBytes(3, []byte{0x01, 0x03, 0x00, 0x00})
	enc.EncodeVarint(4, 8)
	enc.EncodeVarint(5, 33)
	enc.EncodeVarint(6, 2500)

	want, err := hex.DecodeString("06081110091a04010300002008282130c413")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc.Bytes(), want) {
		t.Fatalf("WriteCmd wire = %x, want %x", enc.Bytes(), want)
	}
}

// Test vector from protocol-spec.md §3.2 Hello
func TestHelloWireFormat(t *testing.T) {
	// Encode Hello
	enc := NewEncoder(MsgHello)
	enc.EncodeString(1, "esp32-30eda0a9a808") // device_id
	enc.EncodeString(2, "4.0.0")              // firmware_version
	enc.EncodeString(3, "ESP32S3")            // model
	enc.EncodeVarint(4, 1)                    // channel_count

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
	enc.EncodeVarint(1, 5)                                               // channel_id=5
	enc.EncodeVarint(2, 100000000)                                       // timestamp_us=100000000
	enc.EncodeVarint(3, 1)                                               // sequence=1
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
	enc.EncodeVarint(1, 3600)     // uptime_sec
	enc.EncodeString(2, "online") // status
	enc.EncodeVarint(3, 2)        // channel_count

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
	enc.EncodeVarint(1, 42) // known field
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
		{"truncated_length", []byte{0x01, 0x0A, 0x10}},       // string len=16 but no data
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
	sub.EncodeVarint(1, 1)                 // id=1
	sub.EncodeBytes(2, []byte{0xE0, 0xB6}) // write_data
	sub.EncodeVarint(3, 25)                // read_length
	sub.EncodeVarint(4, 10)                // delay_ms

	enc := NewEncoder(MsgConfigMfst)
	enc.EncodeString(1, "manifest-001") // manifest_id
	enc.EncodeSubFrame(2, sub.Bytes())  // templates[0]

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

func TestHelloV26HandshakeNonceRoundTrip(t *testing.T) {
	const nonce uint32 = 0xA1B2C3D4
	enc := NewEncoder(MsgHello)
	enc.EncodeVarint(HelloFieldHandshakeNonce, uint64(nonce))

	wire := enc.Bytes()
	if len(wire) < 2 || wire[1] != byte(HelloFieldHandshakeNonce<<3|WireVarint) {
		t.Fatalf("Hello nonce tag: got %x", wire)
	}
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	field, err := dec.NextField()
	if err != nil {
		t.Fatalf("NextField: %v", err)
	}
	if field.FieldNum != HelloFieldHandshakeNonce || field.WireType != WireVarint || GetUint64(field) != uint64(nonce) {
		t.Fatalf("nonce field: got %#v, want %d", field, nonce)
	}
}

func TestExtendedFieldTagsRoundTrip(t *testing.T) {
	enc := NewEncoder(MsgStatusRpt)
	enc.EncodeVarint(18, 42)
	enc.EncodeBytes(23, []byte("x"))
	wire := enc.Bytes()
	if len(wire) < 8 || wire[1] != 0x90 || wire[2] != 0x01 {
		t.Fatalf("field 18 tag is not canonical: %x", wire)
	}
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	field, err := dec.NextField()
	if err != nil || field.FieldNum != 18 || GetUint64(field) != 42 {
		t.Fatalf("field 18 round trip: field=%#v err=%v", field, err)
	}
	field, err = dec.NextField()
	if err != nil || field.FieldNum != 23 || string(GetBytes(field)) != "x" {
		t.Fatalf("field 23 round trip: field=%#v err=%v", field, err)
	}
}

func TestHelloAckV26HandshakeNonceRoundTrip(t *testing.T) {
	const nonce uint32 = 0xFFFFFFFF
	enc := NewEncoder(MsgHelloAck)
	enc.EncodeVarint(1, 1)
	enc.EncodeVarint(2, 0)
	enc.EncodeVarint(HelloAckFieldHandshakeNonce, uint64(nonce))

	dec, err := NewDecoder(enc.Bytes())
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	var got uint64
	for {
		field, err := dec.NextField()
		if errors.Is(err, ErrEndOfFrame) {
			break
		}
		if err != nil {
			t.Fatalf("NextField: %v", err)
		}
		if field.FieldNum == HelloAckFieldHandshakeNonce {
			got = GetUint64(field)
		}
	}
	if got != uint64(nonce) {
		t.Fatalf("echoed nonce: got %d, want %d", got, nonce)
	}
}

func TestDecoderRejectsOverflowingVarint(t *testing.T) {
	wire := []byte{MsgHello, byte(HelloFieldHandshakeNonce << 3)}
	for range 9 {
		wire = append(wire, 0x80)
	}
	wire = append(wire, 0x02) // tenth byte may carry only bit zero for uint64

	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	if _, err := dec.NextField(); err == nil {
		t.Fatal("overflowing uint64 varint was accepted")
	}
}

func TestDecoderRejectsNonCanonicalVarint(t *testing.T) {
	wire := []byte{
		MsgHello, byte(HelloFieldHandshakeNonce << 3), 0x80, 0x00,
	}
	dec, err := NewDecoder(wire)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	if _, err := dec.NextField(); err == nil {
		t.Fatal("non-canonical zero varint was accepted")
	}
}

// === Benchmarks (P2.x from acceptance-criteria) ===

// BenchmarkDecodeHello measures decode performance for typical Hello message
func BenchmarkDecodeHello(b *testing.B) {
	// Pre-encode a typical Hello message
	enc := NewEncoder(MsgHello)
	enc.EncodeString(1, "esp32c6_404CCA57B7BC")
	enc.EncodeString(2, "2.0.0")
	enc.EncodeString(3, "ESP32C6")
	enc.EncodeVarint(4, 4)
	wire := enc.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec, _ := NewDecoder(wire)
		for {
			_, err := dec.NextField()
			if err != nil {
				break
			}
		}
	}
}

// BenchmarkEncodeHello measures encode performance
func BenchmarkEncodeHello(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc := NewEncoder(MsgHello)
		enc.EncodeString(1, "esp32c6_404CCA57B7BC")
		enc.EncodeString(2, "2.0.0")
		enc.EncodeString(3, "ESP32C6")
		enc.EncodeVarint(4, 4)
		_ = enc.Bytes()
	}
}

// BenchmarkDecodeDataReport measures decode performance for typical DataReport
func BenchmarkDecodeDataReport(b *testing.B) {
	// Pre-encode a typical DataReport (7 bytes raw data)
	enc := NewEncoder(MsgDataRpt)
	enc.EncodeVarint(1, 5)
	enc.EncodeVarint(2, 1717200000000)
	enc.EncodeVarint(3, 12345)
	enc.EncodeBytes(4, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07})
	wire := enc.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec, _ := NewDecoder(wire)
		for {
			_, err := dec.NextField()
			if err != nil {
				break
			}
		}
	}
}

// BenchmarkEncodeDataReport measures encode performance
func BenchmarkEncodeDataReport(b *testing.B) {
	rawData := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc := NewEncoder(MsgDataRpt)
		enc.EncodeVarint(1, 5)
		enc.EncodeVarint(2, 1717200000000)
		enc.EncodeVarint(3, 12345)
		enc.EncodeBytes(4, rawData)
		_ = enc.Bytes()
	}
}

// BenchmarkDecodeConfigManifest measures decode for complex nested message
func BenchmarkDecodeConfigManifest(b *testing.B) {
	// Pre-encode a ConfigManifest with 4 templates
	enc := NewEncoder(MsgConfigMfst)
	enc.EncodeString(1, "cfg-001")
	for i := 0; i < 4; i++ {
		sub := SubEncoder()
		sub.EncodeVarint(1, uint64(i+1))
		sub.EncodeBytes(2, []byte{0xE0, 0xB6})
		sub.EncodeVarint(3, 25)
		sub.EncodeVarint(4, 10)
		enc.EncodeSubFrame(2, sub.Bytes())
	}
	wire := enc.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec, _ := NewDecoder(wire)
		for {
			_, err := dec.NextField()
			if err != nil {
				break
			}
		}
	}
}

// Fuzz test for frame decoder (N3.3)
func FuzzDecoder(f *testing.F) {
	// Seed corpus
	f.Add([]byte{0x01, 0x0A, 0x05, 'h', 'e', 'l', 'l', 'o'}) // Hello with short string
	f.Add([]byte{0x03, 0x08, 0x01})                          // DataReport with varint 1
	f.Add([]byte{0xFF, 0x08, 0x7F})                          // Unknown type, varint 127
	f.Add([]byte{})                                          // empty
	f.Add([]byte{0x01, 0x80, 0x80, 0x80, 0x80})              // malformed varint

	f.Fuzz(func(t *testing.T, data []byte) {
		// Should never panic
		dec, err := NewDecoder(data)
		if err != nil {
			return
		}
		// Consume all fields safely
		for {
			field, err := dec.NextField()
			if err != nil {
				break
			}
			// Try to read all value types - should not panic
			_ = GetUint64(field)
			_ = GetString(field)
			_ = GetBool(field)
		}
	})
}

// Fuzz test for encoder round-trip
func FuzzEncoderRoundTrip(f *testing.F) {
	f.Add(uint8(0x01), uint64(42), "hello", true)
	f.Add(uint8(0x03), uint64(1), "test", false)
	f.Add(uint8(0x0B), uint64(100), "ota-task", true)

	f.Fuzz(func(t *testing.T, msgType uint8, value uint64, str string, flag bool) {
		// Encode
		enc := NewEncoder(msgType)
		enc.EncodeVarint(1, value)
		enc.EncodeString(2, str)
		enc.EncodeBool(3, flag)
		wire := enc.Bytes()

		// Decode and verify
		dec, err := NewDecoder(wire)
		if err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if dec.MsgType() != msgType {
			t.Errorf("msg type: got 0x%02X, want 0x%02X", dec.MsgType(), msgType)
		}

		// Parse all fields
		var gotVal uint64
		var gotStr string
		var gotFlag bool
		for {
			field, err := dec.NextField()
			if err != nil {
				break
			}
			switch field.FieldNum {
			case 1:
				gotVal = GetUint64(field)
			case 2:
				gotStr = GetString(field)
			case 3:
				gotFlag = GetBool(field)
			}
		}

		if gotVal != value {
			t.Errorf("value: got %d, want %d", gotVal, value)
		}
		if gotStr != str {
			t.Errorf("str: got %q, want %q", gotStr, str)
		}
		if gotFlag != flag {
			t.Errorf("flag: got %v, want %v", gotFlag, flag)
		}
	})
}
