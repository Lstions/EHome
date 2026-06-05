package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/pkg/frame"
)

// MockMQTT implements a simple mock for testing
type MockMQTT struct {
	published []struct {
		topic   string
		payload []byte
	}
}

func (m *MockMQTT) Publish(topic string, payload []byte) error {
	m.published = append(m.published, struct {
		topic   string
		payload []byte
	}{topic, payload})
	return nil
}

func TestHandleHello(t *testing.T) {
	// Build a Hello frame: device_id="TEST001", firmware="2.0.0", model="ESP32S3", channel_count=2
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, "TEST001")
	enc.EncodeString(2, "2.0.0")
	enc.EncodeString(3, "ESP32S3")
	enc.EncodeVarint(4, 2)
	frameBytes := enc.Bytes()

	// Verify frame decodes correctly
	dec, err := frame.NewDecoder(frameBytes)
	if err != nil {
		t.Fatalf("decoder init: %v", err)
	}

	if dec.MsgType() != frame.MsgHello {
		t.Fatalf("msg type: got %d, want %d", dec.MsgType(), frame.MsgHello)
	}

	var deviceID, firmware, model string
	var channelCount uint64

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			deviceID = frame.GetString(field)
		case 2:
			firmware = frame.GetString(field)
		case 3:
			model = frame.GetString(field)
		case 4:
			channelCount = frame.GetUint64(field)
		}
	}

	if deviceID != "TEST001" {
		t.Errorf("device_id: got %s, want TEST001", deviceID)
	}
	if firmware != "2.0.0" {
		t.Errorf("firmware: got %s, want 2.0.0", firmware)
	}
	if model != "ESP32S3" {
		t.Errorf("model: got %s, want ESP32S3", model)
	}
	if channelCount != 2 {
		t.Errorf("channel_count: got %d, want 2", channelCount)
	}
}

func TestHandleDataReport(t *testing.T) {
	raw := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	enc := frame.NewEncoder(frame.MsgDataRpt)
	enc.EncodeVarint(1, 1)        // channel_id
	enc.EncodeVarint(2, 12345678) // timestamp_us
	enc.EncodeVarint(3, 42)       // sequence
	enc.EncodeBytes(4, raw)       // raw_data

	dec, err := frame.NewDecoder(enc.Bytes())
	if err != nil {
		t.Fatalf("decoder init: %v", err)
	}

	var channelID, timestamp, sequence uint64
	var rawData []byte

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			channelID = frame.GetUint64(field)
		case 2:
			timestamp = frame.GetUint64(field)
		case 3:
			sequence = frame.GetUint64(field)
		case 4:
			rawData = frame.GetBytes(field)
		}
	}

	if channelID != 1 {
		t.Errorf("channel_id: got %d, want 1", channelID)
	}
	if timestamp != 12345678 {
		t.Errorf("timestamp: got %d, want 12345678", timestamp)
	}
	if sequence != 42 {
		t.Errorf("sequence: got %d, want 42", sequence)
	}
	if string(rawData) != string(raw) {
		t.Errorf("raw_data mismatch")
	}
}

func TestPingPongRoundTrip(t *testing.T) {
	// Server sends Ping with timestamp
	ts := uint64(time.Now().UnixMicro())
	pingEnc := frame.NewEncoder(frame.MsgPing)
	pingEnc.EncodeVarint(1, ts)
	pingFrame := pingEnc.Bytes()

	// ESP32 receives and sends Pong with same timestamp
	pongEnc := frame.NewEncoder(frame.MsgPong)
	pongEnc.EncodeVarint(1, ts)
	pongFrame := pongEnc.Bytes()

	// Verify Pong decodes correctly
	dec, _ := frame.NewDecoder(pongFrame)
	if dec.MsgType() != frame.MsgPong {
		t.Fatalf("msg type: got %d, want %d", dec.MsgType(), frame.MsgPong)
	}

	field, _ := dec.NextField()
	if field.FieldNum != 1 || frame.GetUint64(field) != ts {
		t.Errorf("pong timestamp mismatch: got %d, want %d", frame.GetUint64(field), ts)
	}

	_ = pingFrame
}
