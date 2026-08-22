package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/pendingwrite"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("warn")
}

// noopPublisher implements mqtt.Publisher for tests — all methods return nil.
type noopPublisher struct{}

func (n *noopPublisher) Publish(topic string, payload []byte) error         { return nil }
func (n *noopPublisher) PublishQoS2(topic string, payload []byte) error     { return nil }
func (n *noopPublisher) PublishRetained(topic string, payload []byte) error { return nil }

// setupHandlerTestDB creates a fresh SQLite in-memory DB for handler response tests.
// Uses a different function name to avoid collision with manager_test.go's setupTestDB.
func setupHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open handler test db: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{})
	return db
}

// setupHandlerManager creates a Manager suitable for handler_response tests.
// It has a real DB, a real pendingWrite manager, a websocket hub, and a no-op MQTT publisher.
func setupHandlerManager(t *testing.T) *Manager {
	t.Helper()
	db := setupHandlerTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()

	mgr := &Manager{
		db:           db,
		wsHub:        wsHub,
		pendingWrite: pendingwrite.NewManager(nil, db),
		pingTracker:  NewPingTracker(),
		mqtt:         &noopPublisher{},
	}
	return mgr
}

// --- handleWriteResponse tests ---

// TestHandleWriteResponse_Success verifies that a well-formed success
// WriteResponse frame is processed without panic.
func TestHandleWriteResponse_Success(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Build a WriteResponse frame: field1=requestID, field2=success, field3=errorCode, field4=errorMsg
	enc := frame.NewEncoder(frame.MsgWriteRsp)
	enc.EncodeVarint(1, 42)   // requestID
	enc.EncodeBool(2, true)   // success
	enc.EncodeVarint(3, 0)    // errorCode
	enc.EncodeString(4, "ok") // errorMsg

	// Should not panic
	mgr.handleWriteResponse("test-device-001", enc.Bytes())
}

// TestHandleWriteResponse_Error verifies that a well-formed failure
// WriteResponse frame is processed without panic.
func TestHandleWriteResponse_Error(t *testing.T) {
	mgr := setupHandlerManager(t)

	enc := frame.NewEncoder(frame.MsgWriteRsp)
	enc.EncodeVarint(1, 99)            // requestID
	enc.EncodeBool(2, false)           // success = false
	enc.EncodeVarint(3, 500)           // errorCode
	enc.EncodeString(4, "device busy") // errorMsg

	// Should not panic
	mgr.handleWriteResponse("test-device-002", enc.Bytes())
}

// TestHandleWriteResponse_InvalidPayload verifies that invalid/corrupt
// payload does not cause a panic.
func TestHandleWriteResponse_InvalidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Empty payload — NewDecoder returns error
	mgr.handleWriteResponse("test-device-003", []byte{})

	// Random garbage bytes
	mgr.handleWriteResponse("test-device-003", []byte{0xFF, 0xFE, 0xFD})

	// Valid first byte but truncated field
	mgr.handleWriteResponse("test-device-003", []byte{frame.MsgWriteRsp, 0x08, 0x01})
}

// --- handlePong tests ---

// TestHandlePong_ValidPayload verifies that a valid Pong frame is processed
// without panic. The anti-forgery check rejects it (no matching Ping tracked),
// which is the expected rejection path for an untracked Pong.
func TestHandlePong_ValidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Create a node in DB so the Updates query has something to update
	node := models.Node{
		NodeID: "pong-device-001",
		Status: "online",
	}
	mgr.db.Create(&node)

	// Build Pong frame: field1=timestamp (microseconds)
	enc := frame.NewEncoder(frame.MsgPong)
	enc.EncodeVarint(1, uint64(1234567890))

	// Should not panic — no tracked Ping, so the Pong is rejected by
	// anti-forgery before any DB/WS side effects.
	mgr.handlePong("pong-device-001", enc.Bytes())

	var n models.Node
	mgr.db.First(&n, "node_id = ?", "pong-device-001")
	if n.PingLatencyMs != 0 || n.LastPingAt != nil {
		t.Errorf("rejected Pong must not update node RTT, got latency=%d last_ping=%v",
			n.PingLatencyMs, n.LastPingAt)
	}
}

// TestHandlePong_InvalidPayload verifies that invalid payload does not panic.
func TestHandlePong_InvalidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Empty payload
	mgr.handlePong("pong-device-002", []byte{})

	// Garbage
	mgr.handlePong("pong-device-002", []byte{0xFF, 0x00, 0x01})

	// Just the msg type byte, no fields
	mgr.handlePong("pong-device-002", []byte{frame.MsgPong})
}

// TestHandlePong_MatchedPing_AcceptsAndConsumes verifies the positive
// anti-forgery path (Redis 退役步骤 1/4 缺口3): a Pong whose timestamp
// matches a tracked Ping is accepted — callback fires, node RTT is stored,
// and the record is consumed exactly once (Complete == former Redis Del).
func TestHandlePong_MatchedPing_AcceptsAndConsumes(t *testing.T) {
	mgr := setupHandlerManager(t)
	mgr.db.Create(&models.Node{NodeID: "pong-device-010", Status: "online"})

	ts := uint64(1750000000123456) // microseconds
	var cbLatency int64 = -2
	var cbSuccess bool
	mgr.pingTracker.Track("pong-device-010", int64(ts), func(latencyMs int64, success bool) {
		cbLatency = latencyMs
		cbSuccess = success
	})

	enc := frame.NewEncoder(frame.MsgPong)
	enc.EncodeVarint(1, ts)
	mgr.handlePong("pong-device-010", enc.Bytes())

	if !cbSuccess {
		t.Fatal("callback must report success for a matched Pong")
	}
	if cbLatency < 0 {
		t.Errorf("callback latency = %d, want non-negative RTT", cbLatency)
	}
	if mgr.pingTracker.PendingCount() != 0 {
		t.Errorf("pending count = %d, want 0 (record consumed once)", mgr.pingTracker.PendingCount())
	}
	var n models.Node
	mgr.db.First(&n, "node_id = ?", "pong-device-010")
	if n.LastPingAt == nil {
		t.Error("node last_ping_at must be set after verified Pong")
	}
}

// TestHandlePong_TimestampMismatch_Rejected verifies that a Pong with a
// tracked device but a wrong timestamp is rejected without consuming the
// pending record (防伪造: forged Pong cannot close a real in-flight Ping).
func TestHandlePong_TimestampMismatch_Rejected(t *testing.T) {
	mgr := setupHandlerManager(t)
	mgr.db.Create(&models.Node{NodeID: "pong-device-011", Status: "online"})

	called := false
	mgr.pingTracker.Track("pong-device-011", 1750000000000000, func(int64, bool) { called = true })

	enc := frame.NewEncoder(frame.MsgPong)
	enc.EncodeVarint(1, uint64(1750000000999999)) // mismatched timestamp
	mgr.handlePong("pong-device-011", enc.Bytes())

	if called {
		t.Error("callback must not fire for a mismatched Pong")
	}
	if mgr.pingTracker.PendingCount() != 1 {
		t.Errorf("pending count = %d, want 1 (record must survive rejection)", mgr.pingTracker.PendingCount())
	}
	var n models.Node
	mgr.db.First(&n, "node_id = ?", "pong-device-011")
	if n.LastPingAt != nil || n.PingLatencyMs != 0 {
		t.Error("mismatched Pong must not update node RTT")
	}
}

// TestHandlePong_ReplayRejected verifies one-time consumption: a second
// Pong carrying the same (already consumed) timestamp is rejected.
func TestHandlePong_ReplayRejected(t *testing.T) {
	mgr := setupHandlerManager(t)
	mgr.db.Create(&models.Node{NodeID: "pong-device-012", Status: "online"})

	ts := uint64(1750000000555555)
	callbacks := 0
	mgr.pingTracker.Track("pong-device-012", int64(ts), func(int64, bool) { callbacks++ })

	enc := frame.NewEncoder(frame.MsgPong)
	enc.EncodeVarint(1, ts)
	mgr.handlePong("pong-device-012", enc.Bytes())
	mgr.handlePong("pong-device-012", enc.Bytes()) // replay

	if callbacks != 1 {
		t.Errorf("callback fired %d times, want exactly 1 (replay must be rejected)", callbacks)
	}
}

// --- handlePing tests ---

// TestHandlePing_ValidPayload verifies that a valid Ping frame is processed
// and a PongAck is sent (via no-op publisher) without panic.
func TestHandlePing_ValidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Build Ping frame: field1=timestamp
	enc := frame.NewEncoder(frame.MsgPing)
	enc.EncodeVarint(1, uint64(9876543210))

	// Should not panic — mqtt is a no-op publisher
	mgr.handlePing("ping-device-001", enc.Bytes())
}

// TestHandlePing_InvalidPayload verifies that invalid payload does not panic.
func TestHandlePing_InvalidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Empty payload
	mgr.handlePing("ping-device-002", []byte{})

	// Garbage bytes
	mgr.handlePing("ping-device-002", []byte{0xFF, 0xFF})
}

// --- handleScanReport tests ---

// TestHandleScanReport_InvalidPayload verifies that invalid payload does not panic.
func TestHandleScanReport_InvalidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Empty payload
	mgr.handleScanReport("scan-device-001", []byte{})

	// Garbage bytes
	mgr.handleScanReport("scan-device-001", []byte{0xFF, 0x00, 0x01, 0x02})

	// Just msg type byte
	mgr.handleScanReport("scan-device-001", []byte{frame.MsgScanRpt})
}

// TestHandleScanReport_ValidPayload verifies that a valid ScanReport frame
// is processed without panic.
func TestHandleScanReport_ValidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	// Build ScanReport: field1=requestID(string), field2=hardwareID, field3=success, field4=addresses(bytes)
	enc := frame.NewEncoder(frame.MsgScanRpt)
	enc.EncodeString(1, "scan-req-001")
	enc.EncodeVarint(2, 1) // hardwareID
	enc.EncodeBool(3, true)
	enc.EncodeBytes(4, []byte{0x01, 0x02, 0x03}) // Modbus addresses

	// Should not panic
	mgr.handleScanReport("scan-device-002", enc.Bytes())
}

// --- handleQueryResponse test (bonus) ---

// TestHandleQueryResponse_ValidPayload verifies that a valid QueryRsp frame
// is processed without panic.
func TestHandleQueryResponse_ValidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	enc := frame.NewEncoder(frame.MsgQueryRsp)
	enc.EncodeString(1, "query-req-001")
	enc.EncodeBool(2, true)
	enc.EncodeString(3, "")

	mgr.handleQueryResponse("query-device-001", enc.Bytes())
}

// TestHandleQueryResponse_InvalidPayload verifies invalid payload doesn't panic.
func TestHandleQueryResponse_InvalidPayload(t *testing.T) {
	mgr := setupHandlerManager(t)

	mgr.handleQueryResponse("query-device-002", []byte{})
	mgr.handleQueryResponse("query-device-002", []byte{0xFF, 0xFF})
}

// --- handleConfigSyncRequest test (bonus) ---
// Note: This requires syncGate to be initialized. We skip it here as
// syncGate needs more setup (eventBus, etc.) and the task doesn't require it.
