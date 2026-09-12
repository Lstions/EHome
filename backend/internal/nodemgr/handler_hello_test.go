package nodemgr

import (
	"errors"
	"math"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func encodedHelloWithNonce(nonce uint64) []byte {
	return encodedHello("2.6", nonce)
}

func encodedHello(protocol string, nonce uint64) []byte {
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, "wire-node-id-is-not-authentication")
	enc.EncodeString(2, "2.6.0")
	enc.EncodeString(3, "ESP32-C6")
	enc.EncodeVarint(4, 2)
	enc.EncodeVarint(5, 7)
	enc.EncodeBool(6, true)
	enc.EncodeString(7, "manifest")
	enc.EncodeString(8, protocol)
	enc.EncodeVarint(frame.HelloFieldHandshakeNonce, nonce)
	return enc.Bytes()
}

func TestParseHelloRequiresV26Nonce(t *testing.T) {
	t.Run("nonzero exact", func(t *testing.T) {
		got, err := parseHello(encodedHelloWithNonce(math.MaxUint32))
		if err != nil {
			t.Fatalf("parseHello: %v", err)
		}
		if got.HandshakeNonce != math.MaxUint32 {
			t.Fatalf("nonce: got %d, want %d", got.HandshakeNonce, uint32(math.MaxUint32))
		}
		if got.ProtocolVersion != "2.6" || got.FirmwareVersion != "2.6.0" {
			t.Fatalf("known fields lost: %#v", got)
		}
	})

	t.Run("absent nonce is rejected", func(t *testing.T) {
		enc := frame.NewEncoder(frame.MsgHello)
		enc.EncodeString(8, "2.5")
		if _, err := parseHello(enc.Bytes()); err == nil {
			t.Fatal("Hello without nonce was accepted")
		}
	})

	t.Run("explicit zero is rejected", func(t *testing.T) {
		if _, err := parseHello(encodedHelloWithNonce(0)); err == nil {
			t.Fatal("Hello with zero nonce was accepted")
		}
	})

	t.Run("wrong protocol version is rejected", func(t *testing.T) {
		if _, err := parseHello(encodedHello("2.5", 42)); err == nil {
			t.Fatal("Hello with protocol 2.5 was accepted")
		}
	})
}

func TestHandleHelloRejectsWireNodeMismatch(t *testing.T) {
	mock := &senderMockMQTT{}
	mgr := &Manager{mqtt: mock}
	mgr.handleHello("topic-node", encodedHelloWithNonce(42))
	if len(mock.records) != 0 {
		t.Fatalf("mismatched wire node published %d frame(s), want none", len(mock.records))
	}
}

func invalidNonceHelloFrames() map[string][]byte {
	duplicate := frame.NewEncoder(frame.MsgHello)
	duplicate.EncodeVarint(frame.HelloFieldHandshakeNonce, 1)
	duplicate.EncodeVarint(frame.HelloFieldHandshakeNonce, 2)

	wrongWire := frame.NewEncoder(frame.MsgHello)
	wrongWire.EncodeString(frame.HelloFieldHandshakeNonce, "1")

	overflow := frame.NewEncoder(frame.MsgHello)
	overflow.EncodeVarint(frame.HelloFieldHandshakeNonce, uint64(math.MaxUint32)+1)

	malformed := append([]byte(nil), frame.NewEncoder(frame.MsgHello).Bytes()...)
	malformed = append(malformed, byte(frame.HelloFieldHandshakeNonce<<3), 0x80)
	nonCanonicalZero := []byte{
		frame.MsgHello, byte(frame.HelloFieldHandshakeNonce << 3), 0x80, 0x00,
	}
	malformedOverflow := []byte{frame.MsgHello, byte(frame.HelloFieldHandshakeNonce << 3)}
	for range 9 {
		malformedOverflow = append(malformedOverflow, 0x80)
	}
	malformedOverflow = append(malformedOverflow, 0x02)

	wrongMessage := frame.NewEncoder(frame.MsgStatusRpt)
	wrongMessage.EncodeVarint(frame.HelloFieldHandshakeNonce, 1)

	return map[string][]byte{
		"duplicate":         duplicate.Bytes(),
		"wrong wire":        wrongWire.Bytes(),
		"uint32 overflow":   overflow.Bytes(),
		"malformed":         malformed,
		"noncanonical zero": nonCanonicalZero,
		"malformed uint64":  malformedOverflow,
		"wrong message":     wrongMessage.Bytes(),
	}
}

func TestParseHelloRejectsInvalidHandshakeNonce(t *testing.T) {
	for name, payload := range invalidNonceHelloFrames() {
		t.Run(name, func(t *testing.T) {
			_, err := parseHello(payload)
			if err == nil {
				t.Fatal("parseHello accepted invalid Hello")
			}
			if name != "wrong message" && !strings.Contains(err.Error(), "Hello") {
				t.Fatalf("error lacks Hello context: %v", err)
			}
		})
	}
}

func TestHandleHelloInvalidNonceDoesNotSendAck(t *testing.T) {
	for name, payload := range invalidNonceHelloFrames() {
		t.Run(name, func(t *testing.T) {
			mock := &senderMockMQTT{}
			mgr := &Manager{mqtt: mock}
			mgr.handleHello("topic-node", payload)
			if len(mock.records) != 0 {
				t.Fatalf("invalid Hello published %d frame(s), want none", len(mock.records))
			}
		})
	}
}

// =====================================================================
// P1 regression: a soft-deleted node could never re-register.
//
// Root cause fixed in handler_hello.go:
//   1. the dedup lookup ignored soft-deleted rows (scoped First);
//   2. the Create error was discarded (unique-key collision swallowed);
//   3. node.ID == 0 was cached, so later DataReports were processed as node 0.
// =====================================================================

// newHelloTestManager builds the minimum Manager needed to drive handleHello
// end to end. Only nodes + node_events exist, so the config-hash pipeline
// (which needs config_templates/edge_devices) degrades to SyncActionNone
// instead of pushing a manifest — these tests stay focused on registration.
func newHelloTestManager(t *testing.T) (*Manager, *gorm.DB, *senderMockMQTT) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.NodeEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mock := &senderMockMQTT{}
	mgr := &Manager{db: db, mqtt: mock, wsHub: websocket.NewHub()}
	mgr.syncGate = NewSyncGate(mgr, nil)
	return mgr, db, mock
}

// encodedHelloFor builds a valid v2.6 Hello whose wire node_id matches the
// MQTT-topic-derived identity (parseHello requires exact equality).
func encodedHelloFor(deviceID string, nonce uint64) []byte {
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, deviceID)
	enc.EncodeString(2, "2.6.0")
	enc.EncodeString(3, "ESP32-C6")
	enc.EncodeVarint(4, 2)
	enc.EncodeVarint(5, 7)
	enc.EncodeBool(6, true)
	enc.EncodeString(7, "manifest")
	enc.EncodeString(8, "2.6")
	enc.EncodeVarint(frame.HelloFieldHandshakeNonce, nonce)
	return enc.Bytes()
}

func countHelloAcks(mock *senderMockMQTT) int {
	acks := 0
	for _, rec := range mock.records {
		if len(rec.payload) > 0 && rec.payload[0] == frame.MsgHelloAck {
			acks++
		}
	}
	return acks
}

func countLiveNodes(t *testing.T, db *gorm.DB, deviceID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.Node{}).Where("node_id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("count live nodes: %v", err)
	}
	return count
}

// TestHandleHelloRevivesSoftDeletedNode: register → soft delete → re-handshake
// must reuse the same row (same primary key, deleted_at NULL) instead of
// vanishing behind the table-wide unique index.
func TestHandleHelloRevivesSoftDeletedNode(t *testing.T) {
	mgr, db, mock := newHelloTestManager(t)
	const deviceID = "sim-soft-delete-reregister"
	nodeIDCache.Delete(deviceID)
	defer nodeIDCache.Delete(deviceID)

	// 1) first handshake registers the node.
	mgr.handleHello(deviceID, encodedHelloFor(deviceID, 101))
	mgr.wg.Wait()

	var first models.Node
	if err := db.Where("node_id = ?", deviceID).First(&first).Error; err != nil {
		t.Fatalf("node missing after first handshake: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("first handshake stored a zero primary key")
	}
	if acks := countHelloAcks(mock); acks != 1 {
		t.Fatalf("first handshake published %d HelloAck frame(s), want 1", acks)
	}
	if entry, ok := nodeIDCache.Load(deviceID); !ok {
		t.Fatal("node ID cache was not populated on first registration")
	} else if entry.(nodeIDCacheEntry).nodeID != first.ID {
		t.Fatalf("cache holds ID %d, want %d", entry.(nodeIDCacheEntry).nodeID, first.ID)
	}

	// 2) ops soft-deletes the node (DELETE /api/v1/nodes/:id).
	if err := db.Delete(&models.Node{}, first.ID).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if got := countLiveNodes(t, db, deviceID); got != 0 {
		t.Fatalf("live rows after delete = %d, want 0", got)
	}

	// 3) the same physical device powers up again and re-handshakes.
	mgr.handleHello(deviceID, encodedHelloFor(deviceID, 102))
	mgr.wg.Wait()

	if acks := countHelloAcks(mock); acks != 2 {
		t.Fatalf("re-handshake produced %d HelloAck frame(s) in total, want 2", acks)
	}
	if got := countLiveNodes(t, db, deviceID); got != 1 {
		t.Fatalf("live rows after re-handshake = %d, want 1", got)
	}
	var revived models.Node
	if err := db.Where("node_id = ?", deviceID).First(&revived).Error; err != nil {
		t.Fatalf("node still invisible after re-handshake: %v", err)
	}
	if revived.ID != first.ID {
		t.Fatalf("re-handshake used row id %d, want the original %d", revived.ID, first.ID)
	}
	if revived.DeletedAt.Valid {
		t.Fatalf("deleted_at still set after re-handshake: %v", revived.DeletedAt.Time)
	}
	if revived.Status != "online" {
		t.Fatalf("revived node status = %q, want online", revived.Status)
	}
	var rows int64
	if err := db.Unscoped().Model(&models.Node{}).Where("node_id = ?", deviceID).Count(&rows).Error; err != nil {
		t.Fatalf("count all rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("nodes rows for %s = %d, want exactly 1 (reuse, not duplicate)", deviceID, rows)
	}
	if entry, ok := nodeIDCache.Load(deviceID); !ok {
		t.Fatal("node ID cache lost the revived mapping")
	} else if entry.(nodeIDCacheEntry).nodeID != first.ID {
		t.Fatalf("cache holds ID %d after revival, want %d", entry.(nodeIDCacheEntry).nodeID, first.ID)
	}

	// 4) a second delete → re-register cycle must behave identically: revival is
	//    repeatable, not a one-shot.
	if err := db.Delete(&models.Node{}, first.ID).Error; err != nil {
		t.Fatalf("second soft delete: %v", err)
	}
	mgr.handleHello(deviceID, encodedHelloFor(deviceID, 104))
	mgr.wg.Wait()
	if got := countLiveNodes(t, db, deviceID); got != 1 {
		t.Fatalf("live rows after second re-handshake = %d, want 1", got)
	}
	var again models.Node
	if err := db.Where("node_id = ?", deviceID).First(&again).Error; err != nil {
		t.Fatalf("node invisible after second re-handshake: %v", err)
	}
	if again.ID != first.ID || again.DeletedAt.Valid {
		t.Fatalf("second revival = id %d deleted_at %v, want id %d and NULL", again.ID, again.DeletedAt, first.ID)
	}
	if acks := countHelloAcks(mock); acks != 3 {
		t.Fatalf("total HelloAck frames = %d, want 3", acks)
	}
}

// TestHandleHelloDoesNotAckWhenNodePersistFails: a failing INSERT must be
// logged at Error level and must abort the handshake before HelloAck, so the
// "device believes it is registered, center has no row" state cannot occur.
func TestHandleHelloDoesNotAckWhenNodePersistFails(t *testing.T) {
	mgr, db, mock := newHelloTestManager(t)
	const deviceID = "sim-create-conflict"
	nodeIDCache.Delete(deviceID)
	defer nodeIDCache.Delete(deviceID)

	// Force the nodes INSERT to fail — this is the unique-key collision the defect
	// used to swallow on the create path.
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_nodes_create", func(tx *gorm.DB) {
		if tx.Statement.Table == "nodes" {
			tx.AddError(errors.New("injected nodes insert failure"))
		}
	}); err != nil {
		t.Fatalf("register create-failure callback: %v", err)
	}

	core, observed := observer.New(zapcore.ErrorLevel)
	previous := logger.L
	logger.L = zap.New(core).Sugar()
	defer func() { logger.L = previous }()

	mgr.handleHello(deviceID, encodedHelloFor(deviceID, 103))
	mgr.wg.Wait()

	if acks := countHelloAcks(mock); acks != 0 {
		t.Fatalf("published %d HelloAck frame(s) although the node row was never persisted", acks)
	}
	var rows int64
	if err := db.Unscoped().Model(&models.Node{}).Where("node_id = ?", deviceID).Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("nodes rows for %s = %d, want 0", deviceID, rows)
	}
	if v, ok := nodeIDCache.Load(deviceID); ok {
		t.Fatalf("node ID cache was written on the failure path: %#v", v)
	}
	found := false
	for _, entry := range observed.All() {
		if entry.Level >= zapcore.ErrorLevel && strings.Contains(entry.Message, deviceID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("persistence failure was not logged at Error level; observed: %#v", observed.All())
	}
}

// TestStoreNodeIDCacheNeverCachesZero: a zero primary key must never enter the
// node ID cache (it would route every later DataReport to node ID 0).
func TestStoreNodeIDCacheNeverCachesZero(t *testing.T) {
	const deviceID = "sim-zero-node-id"
	nodeIDCache.Delete(deviceID)
	defer nodeIDCache.Delete(deviceID)

	core, observed := observer.New(zapcore.WarnLevel)
	previous := logger.L
	logger.L = zap.New(core).Sugar()
	defer func() { logger.L = previous }()

	storeNodeIDCache(deviceID, 0)
	if v, ok := nodeIDCache.Load(deviceID); ok {
		t.Fatalf("node ID cache accepted zero ID: %#v", v)
	}
	if entries := observed.FilterLevelExact(zapcore.WarnLevel).All(); len(entries) == 0 {
		t.Fatal("refusing to cache ID 0 was not logged at Warn level")
	}

	storeNodeIDCache(deviceID, 4242)
	v, ok := nodeIDCache.Load(deviceID)
	if !ok {
		t.Fatal("non-zero node ID was not cached")
	}
	if entry, ok := v.(nodeIDCacheEntry); !ok || entry.nodeID != 4242 {
		t.Fatalf("cache entry = %#v, want nodeID=4242", v)
	}
}
