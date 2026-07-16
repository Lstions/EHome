package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/terminal"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("error")
}

// --- MockMQTT for sender tests ---

// mockPublishRecord records a single Publish call
type mockPublishRecord struct {
	topic   string
	payload []byte
	qos     int // 1 for Publish, 2 for PublishQoS2
}

// senderMockMQTT implements mqtt.Publisher for sender tests.
// It records every Publish / PublishQoS2 / PublishRetained call.
type senderMockMQTT struct {
	records []mockPublishRecord
}

func (m *senderMockMQTT) Publish(topic string, payload []byte) error {
	m.records = append(m.records, mockPublishRecord{topic: topic, payload: payload, qos: 1})
	return nil
}

func (m *senderMockMQTT) PublishQoS2(topic string, payload []byte) error {
	m.records = append(m.records, mockPublishRecord{topic: topic, payload: payload, qos: 2})
	return nil
}

func (m *senderMockMQTT) PublishRetained(topic string, payload []byte) error {
	m.records = append(m.records, mockPublishRecord{topic: topic, payload: payload, qos: 1})
	return nil
}

func (m *senderMockMQTT) lastRecord() *mockPublishRecord {
	if len(m.records) == 0 {
		return nil
	}
	return &m.records[len(m.records)-1]
}

// setupSenderTestDB creates an in-memory SQLite DB for sender tests.
func setupSenderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{}, &models.ConfigMeta{})
	return db
}

// newSenderManager builds a minimal Manager with a mock MQTT client.
func newSenderManager(t *testing.T) (*Manager, *senderMockMQTT) {
	t.Helper()
	db := setupSenderTestDB(t)
	mock := &senderMockMQTT{}
	mgr := &Manager{
		db:      db,
		mqtt:    mock,
		termMgr: terminal.NewManager(),
	}
	return mgr, mock
}

// --- SendPing tests ---

func TestSender_SendPing(t *testing.T) {
	mgr, mock := newSenderManager(t)

	err := mgr.SendPing("NODE001")
	if err != nil {
		t.Fatalf("SendPing error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	// Topic should be nodes/NODE001/down
	if rec.topic != "nodes/NODE001/down" {
		t.Errorf("topic: got %s, want nodes/NODE001/down", rec.topic)
	}
	// First byte should be MsgPing (0x08)
	if len(rec.payload) < 1 {
		t.Fatal("payload too short")
	}
	if rec.payload[0] != frame.MsgPing {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgPing)
	}
	// Decode the frame and verify field 1 (timestamp varint)
	dec, err := frame.NewDecoder(rec.payload)
	if err != nil {
		t.Fatalf("decoder init: %v", err)
	}
	field, err := dec.NextField()
	if err != nil {
		t.Fatalf("NextField: %v", err)
	}
	if field.FieldNum != 1 {
		t.Errorf("field num: got %d, want 1", field.FieldNum)
	}
	ts := frame.GetUint64(field)
	if ts == 0 {
		t.Error("timestamp should be non-zero")
	}
}

func TestSender_SendPing_MultipleCalls(t *testing.T) {
	mgr, mock := newSenderManager(t)

	for i := 0; i < 3; i++ {
		_ = mgr.SendPing("DEV5")
	}
	if len(mock.records) != 3 {
		t.Errorf("expected 3 publish calls, got %d", len(mock.records))
	}
	for _, r := range mock.records {
		if r.topic != "nodes/DEV5/down" {
			t.Errorf("topic: got %s, want nodes/DEV5/down", r.topic)
		}
	}
}

// --- SendWriteCommand tests ---

func TestSender_SendWriteCommand(t *testing.T) {
	mgr, mock := newSenderManager(t)

	data := []byte{0xAB, 0xCD, 0xEF}
	err := mgr.SendWriteCommand("DEV1", 5, data, 2)
	if err != nil {
		t.Fatalf("SendWriteCommand error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected PublishQoS2 to be called")
	}
	if rec.qos != 2 {
		t.Errorf("expected QoS 2, got %d", rec.qos)
	}
	if rec.topic != "nodes/DEV1/control" {
		t.Errorf("topic: got %s, want nodes/DEV1/control", rec.topic)
	}
	if rec.payload[0] != frame.MsgWriteCmd {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgWriteCmd)
	}

	// Decode frame: field 1=request_id, field 2=channelID, field 3=data, field 4=readSize
	dec, _ := frame.NewDecoder(rec.payload)
	var channelID, readSize uint64
	var cmdData []byte
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 2:
			channelID = frame.GetUint64(field)
		case 3:
			cmdData = frame.GetBytes(field)
		case 4:
			readSize = frame.GetUint64(field)
		}
	}
	if channelID != 5 {
		t.Errorf("channelID: got %d, want 5", channelID)
	}
	if string(cmdData) != string(data) {
		t.Errorf("data: got %x, want %x", cmdData, data)
	}
	if readSize != 2 {
		t.Errorf("readSize: got %d, want 2", readSize)
	}
}

func TestSender_SendWriteCommand_NoReadSize(t *testing.T) {
	mgr, mock := newSenderManager(t)

	data := []byte{0x01}
	err := mgr.SendWriteCommand("DEV2", 3, data, 0)
	if err != nil {
		t.Fatalf("SendWriteCommand error: %v", err)
	}

	rec := mock.lastRecord()
	dec, _ := frame.NewDecoder(rec.payload)
	hasReadSize := false
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 4 {
			hasReadSize = true
		}
	}
	if hasReadSize {
		t.Error("field 4 (readSize) should not be present when readSize=0")
	}
}

// --- SendScanRequest tests ---

func TestSender_SendScanRequest(t *testing.T) {
	mgr, mock := newSenderManager(t)

	err := mgr.SendScanRequest("DEV3", 0x76)
	if err != nil {
		t.Fatalf("SendScanRequest error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV3/down" {
		t.Errorf("topic: got %s, want nodes/DEV3/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgScanReq {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgScanReq)
	}

	// Decode: field 1=request_id (string), field 2=hardwareID (varint)
	dec, _ := frame.NewDecoder(rec.payload)
	var hwID uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 2:
			hwID = frame.GetUint64(field)
		}
	}
	if hwID != 0x76 {
		t.Errorf("hardwareID: got %d, want %d", hwID, 0x76)
	}
}

// --- SendHelloAck tests ---

func TestSender_SendHelloAck(t *testing.T) {
	mgr, mock := newSenderManager(t)

	err := mgr.SendHelloAck("DEV4", 1700000000, 0x03)
	if err != nil {
		t.Fatalf("SendHelloAck error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV4/down" {
		t.Errorf("topic: got %s, want nodes/DEV4/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgHelloAck {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgHelloAck)
	}

	// Decode: field 1=serverTime (varint), field 2=features (varint)
	dec, _ := frame.NewDecoder(rec.payload)
	var serverTime, features uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			serverTime = frame.GetUint64(field)
		case 2:
			features = frame.GetUint64(field)
		}
	}
	if serverTime != 1700000000 {
		t.Errorf("serverTime: got %d, want 1700000000", serverTime)
	}
	if features != 0x03 {
		t.Errorf("features: got %d, want 3", features)
	}
}

// --- SendConfigQuery tests ---

func TestSender_SendConfigQuery(t *testing.T) {
	mgr, mock := newSenderManager(t)

	err := mgr.SendConfigQuery("DEV6")
	if err != nil {
		t.Fatalf("SendConfigQuery error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV6/down" {
		t.Errorf("topic: got %s, want nodes/DEV6/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgConfigQuery {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgConfigQuery)
	}

	// Decode: field 1=request_id (string)
	dec, _ := frame.NewDecoder(rec.payload)
	field, err := dec.NextField()
	if err != nil {
		t.Fatalf("NextField: %v", err)
	}
	if field.FieldNum != 1 {
		t.Errorf("field num: got %d, want 1", field.FieldNum)
	}
	reqID := frame.GetString(field)
	if reqID == "" {
		t.Error("request_id should not be empty")
	}
}

// --- SendQueryResources tests ---

func TestSender_SendQueryResources(t *testing.T) {
	mgr, mock := newSenderManager(t)

	reqID, err := mgr.SendQueryResources("DEV7")
	if err != nil {
		t.Fatalf("SendQueryResources error: %v", err)
	}
	if reqID == "" {
		t.Error("request_id should not be empty")
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV7/down" {
		t.Errorf("topic: got %s, want nodes/DEV7/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgQueryResources {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgQueryResources)
	}

	// Decode: field 1=request_id (string)
	dec, _ := frame.NewDecoder(rec.payload)
	field, err := dec.NextField()
	if err != nil {
		t.Fatalf("NextField: %v", err)
	}
	if field.FieldNum != 1 {
		t.Errorf("field num: got %d, want 1", field.FieldNum)
	}
	decodedReqID := frame.GetString(field)
	if decodedReqID != reqID {
		t.Errorf("request_id: decoded %s, returned %s", decodedReqID, reqID)
	}
}

// --- SendModbusScanRequest tests ---

func TestSender_SendModbusScanRequest(t *testing.T) {
	mgr, mock := newSenderManager(t)

	reqID, err := mgr.SendModbusScanRequest("DEV8", 1, 20, 100)
	if err != nil {
		t.Fatalf("SendModbusScanRequest error: %v", err)
	}
	if reqID == "" {
		t.Error("request_id should not be empty")
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV8/down" {
		t.Errorf("topic: got %s, want nodes/DEV8/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgScanReq {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgScanReq)
	}

	// Decode: field 1=request_id, field 3=scan_type(2=MODBUS), field 4=start, field 5=end, field 6=timeout
	dec, _ := frame.NewDecoder(rec.payload)
	var scanType, startAddr, endAddr, timeoutMs uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 3:
			scanType = frame.GetUint64(field)
		case 4:
			startAddr = frame.GetUint64(field)
		case 5:
			endAddr = frame.GetUint64(field)
		case 6:
			timeoutMs = frame.GetUint64(field)
		}
	}
	if scanType != 2 {
		t.Errorf("scan_type: got %d, want 2 (MODBUS)", scanType)
	}
	if startAddr != 1 {
		t.Errorf("start_addr: got %d, want 1", startAddr)
	}
	if endAddr != 20 {
		t.Errorf("end_addr: got %d, want 20", endAddr)
	}
	if timeoutMs != 100 {
		t.Errorf("timeout_ms: got %d, want 100", timeoutMs)
	}
}

func TestSender_SendModbusScanRequest_Defaults(t *testing.T) {
	mgr, mock := newSenderManager(t)

	_, err := mgr.SendModbusScanRequest("DEV9", 0, 0, 0)
	if err != nil {
		t.Fatalf("SendModbusScanRequest error: %v", err)
	}

	rec := mock.lastRecord()
	dec, _ := frame.NewDecoder(rec.payload)
	var startAddr, endAddr, timeoutMs uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 4:
			startAddr = frame.GetUint64(field)
		case 5:
			endAddr = frame.GetUint64(field)
		case 6:
			timeoutMs = frame.GetUint64(field)
		}
	}
	if startAddr != 1 {
		t.Errorf("default start_addr: got %d, want 1", startAddr)
	}
	if endAddr != 247 {
		t.Errorf("default end_addr: got %d, want 247", endAddr)
	}
	if timeoutMs != 200 {
		t.Errorf("default timeout_ms: got %d, want 200", timeoutMs)
	}
}

// --- SendQueryRequest tests ---

func TestSender_SendQueryRequest(t *testing.T) {
	mgr, mock := newSenderManager(t)

	err := mgr.SendQueryRequest("DEV10", 0x01)
	if err != nil {
		t.Fatalf("SendQueryRequest error: %v", err)
	}

	rec := mock.lastRecord()
	if rec == nil {
		t.Fatal("expected Publish to be called")
	}
	if rec.topic != "nodes/DEV10/down" {
		t.Errorf("topic: got %s, want nodes/DEV10/down", rec.topic)
	}
	if rec.payload[0] != frame.MsgQueryReq {
		t.Errorf("msg type: got 0x%02X, want 0x%02X", rec.payload[0], frame.MsgQueryReq)
	}

	// Decode: field 1=request_id, field 2=query_type
	dec, _ := frame.NewDecoder(rec.payload)
	var queryType uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 2:
			queryType = frame.GetUint64(field)
		}
	}
	if queryType != 0x01 {
		t.Errorf("query_type: got %d, want 1", queryType)
	}
}

// --- parseProtocolVersion table-driven tests ---

func TestParseProtocolVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  float64
	}{
		{"v2.2", "2.2", 2.2},
		{"v2.3", "2.3", 2.3},
		{"v1.0", "1.0", 1.0},
		{"v3.1", "3.1", 3.1},
		{"empty", "", 0},
		{"no_dot single number", "5", 0},
		{"malformed", "abc.def", 0},
		{"v2.2.3 extra parts uses first two", "2.2.3", 2.2},
		{"v10.5", "10.5", 10.5},
		{"v0.0", "0.0", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProtocolVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseProtocolVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- parseHardwareID table-driven tests ---

func TestParseHardwareID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  uint64
	}{
		{"hex 0x76", "0x76", 0x76},
		{"hex 0X76 uppercase prefix", "0X76", 0x76},
		{"hex 0xff", "0xff", 255},
		{"decimal 5", "5", 5},
		{"decimal 123", "123", 123},
		{"empty", "", 0},
		{"whitespace only", "   ", 0},
		{"with leading/trailing spaces", "  0x42  ", 0x42},
		{"invalid string", "abc", 0},
		{"zero", "0", 0},
		{"hex 0x0", "0x0", 0},
		{"large hex", "0xFFFF", 65535},
		{"large decimal", "999999", 999999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHardwareID(tt.input)
			if got != tt.want {
				t.Errorf("parseHardwareID(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
