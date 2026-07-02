package offlinedetector

import (
	"testing"
	"time"

	ehomeRedis "ehome/backend/internal/redis"
	ehomeModels "ehome/backend/internal/models"
	"ehome/backend/internal/websocket"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupExtraTestDB creates a fresh SQLite in-memory DB for the extra tests.
// Uses a different function name from offlinedetector_test.go's setupTestDB.
func setupExtraTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open extra test db: %v", err)
	}
	db.AutoMigrate(&ehomeModels.Node{}, &ehomeModels.NodeEvent{}, &ehomeModels.EdgeDevice{})
	return db
}

// setupExtraDetector creates a Detector with a fresh DB and a running wsHub.
func setupExtraDetector(t *testing.T) (*Detector, *gorm.DB) {
	t.Helper()
	db := setupExtraTestDB(t)
	wsHub := websocket.NewHub()
	go wsHub.Run()
	d := NewDetector(db, wsHub)
	return d, db
}

// ensureRedisNil makes sure redis.Client is nil so that redis.IsOnline
// short-circuits in checkRedisHeartbeats (which checks `if redis.Client == nil`).
// For checkDBLastSeen, which calls redis.IsOnline unconditionally, we use
// a non-nil but disconnected client instead (see setupDisconnectedRedis).
func ensureRedisNil() {
	ehomeRedis.Client = nil
}

// setupDisconnectedRedis sets redis.Client to a non-nil but unconnected client.
// This prevents nil-pointer panics in redis.IsOnline — TTL() returns an error
// (connection refused), and .Val() returns 0, so IsOnline returns false.
func setupDisconnectedRedis() {
	ehomeRedis.Client = redis.NewClient(&redis.Options{
		Addr: "localhost:0", // invalid address — connection will fail
	})
}

func restoreRedis() {
	ehomeRedis.Client = nil
}

// TestCheckDBLastSeen_NoNodes verifies that checkDBLastSeen does not error
// when there are no nodes in the database.
func TestCheckDBLastSeen_NoNodes(t *testing.T) {
	ensureRedisNil()
	defer restoreRedis()

	d, db := setupExtraDetector(t)
	// No nodes in DB — should be a no-op
	d.checkDBLastSeen()

	var count int64
	db.Model(&ehomeModels.Node{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 nodes, got %d", count)
	}
}

// TestCheckDBLastSeen_OfflineNode verifies that a node whose last_seen is
// older than 90s (and no Redis heartbeat) gets marked offline.
func TestCheckDBLastSeen_OfflineNode(t *testing.T) {
	// Use a disconnected redis client so IsOnline returns false without panic
	setupDisconnectedRedis()
	defer restoreRedis()

	d, db := setupExtraDetector(t)

	oldTime := time.Now().Add(-120 * time.Second)
	node := ehomeModels.Node{
		NodeID:   "offline-test-001",
		Status:   "online",
		LastSeen: &oldTime,
	}
	db.Create(&node)

	// Call checkDBLastSeen — should mark node offline
	d.checkDBLastSeen()

	var updated ehomeModels.Node
	db.Where("node_id = ?", "offline-test-001").First(&updated)
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}

	// Verify a NodeEvent was created
	var event ehomeModels.NodeEvent
	db.Where("node_id = ?", "offline-test-001").First(&event)
	if event.EventType != "offline" {
		t.Errorf("expected event type 'offline', got %s", event.EventType)
	}
}

// TestCheckEdgeDevicesOffline_NoDevices verifies that checkEdgeDevicesOffline
// does not error when there are no active edge devices.
func TestCheckEdgeDevicesOffline_NoDevices(t *testing.T) {
	d, _ := setupExtraDetector(t)

	// No devices in cache — should be a no-op
	d.checkEdgeDevicesOffline()

	if d.pending_deviceCount() != 0 {
		t.Error("expected 0 active devices in cache")
	}
}

// TestUpdateHeartbeat verifies that UpdateHeartbeat does not panic when
// redis is nil (it checks `if redis.Client != nil`).
func TestUpdateHeartbeat(t *testing.T) {
	ensureRedisNil()
	defer restoreRedis()

	d, _ := setupExtraDetector(t)

	// Should be a no-op when redis is nil
	d.UpdateHeartbeat("test-node-001")

	// With disconnected redis — should not panic
	setupDisconnectedRedis()
	d.UpdateHeartbeat("test-node-002")
}

// TestOnEdgeDeviceCreated verifies that OnEdgeDeviceCreated adds the device
// to the activeDevices cache.
func TestOnEdgeDeviceCreated(t *testing.T) {
	d, _ := setupExtraDetector(t)

	d.OnEdgeDeviceCreated(1001)

	count := d.pending_deviceCount()
	if count != 1 {
		t.Errorf("expected 1 active device after OnEdgeDeviceCreated, got %d", count)
	}

	// Verify the device is in the cache with zero time (no data yet)
	d.mu.RLock()
	lastData, ok := d.activeDevices[1001]
	d.mu.RUnlock()
	if !ok {
		t.Error("expected device 1001 in activeDevices cache")
	}
	if !lastData.IsZero() {
		t.Error("expected zero time for newly created device (no data yet)")
	}
}

// TestOnEdgeDeviceData verifies that OnEdgeDeviceData updates the
// activeDevices cache timestamp.
func TestOnEdgeDeviceData(t *testing.T) {
	d, _ := setupExtraDetector(t)

	// Create device first
	d.OnEdgeDeviceCreated(2002)

	// Before data — time should be zero
	d.mu.RLock()
	before := d.activeDevices[2002]
	d.mu.RUnlock()
	if !before.IsZero() {
		t.Error("expected zero time before data report")
	}

	// Report data
	d.OnEdgeDeviceData(2002)

	// After data — time should be non-zero (now)
	d.mu.RLock()
	after := d.activeDevices[2002]
	d.mu.RUnlock()
	if after.IsZero() {
		t.Error("expected non-zero time after data report")
	}
	if time.Since(after) > 1*time.Second {
		t.Error("expected recent timestamp after data report")
	}
}

// TestOnEdgeDeviceOffline verifies that OnEdgeDeviceOffline removes the
// device from the activeDevices cache.
func TestOnEdgeDeviceOffline(t *testing.T) {
	d, _ := setupExtraDetector(t)

	// Create device
	d.OnEdgeDeviceCreated(3003)
	if d.pending_deviceCount() != 1 {
		t.Fatalf("expected 1 device after create, got %d", d.pending_deviceCount())
	}

	// Mark offline
	d.OnEdgeDeviceOffline(3003)
	if d.pending_deviceCount() != 0 {
		t.Errorf("expected 0 devices after offline, got %d", d.pending_deviceCount())
	}

	// Verify it's actually removed from the map
	d.mu.RLock()
	_, ok := d.activeDevices[3003]
	d.mu.RUnlock()
	if ok {
		t.Error("expected device 3003 to be removed from activeDevices")
	}
}

// TestCheckEdgeDevicesOffline_StaleDevice verifies that a device with stale
// data (>60s) is marked offline in the DB.
func TestCheckEdgeDevicesOffline_StaleDevice(t *testing.T) {
	d, db := setupExtraDetector(t)

	// Create an edge device in DB
	dev := ehomeModels.EdgeDevice{
		Name:   "stale-device",
		NodeID: "stale-node-001",
		Status: "active",
	}
	db.Create(&dev)

	// Simulate stale data — set lastData to 120s ago
	staleTime := time.Now().Add(-120 * time.Second)
	d.mu.Lock()
	d.activeDevices[dev.ID] = staleTime
	d.cacheReady = true
	d.mu.Unlock()

	// Run checkEdgeDevicesOffline
	d.checkEdgeDevicesOffline()

	// Verify device is now offline in DB
	var updated ehomeModels.EdgeDevice
	db.First(&updated, dev.ID)
	if updated.Status != "offline" {
		t.Errorf("expected device status 'offline', got %s", updated.Status)
	}

	// Verify device was removed from cache
	d.mu.RLock()
	_, ok := d.activeDevices[dev.ID]
	d.mu.RUnlock()
	if ok {
		t.Error("expected device removed from cache after offline")
	}
}

// TestCheckEdgeDevicesOffline_RecentDevice verifies that a device with recent
// data (<60s) is NOT marked offline.
func TestCheckEdgeDevicesOffline_RecentDevice(t *testing.T) {
	d, db := setupExtraDetector(t)

	dev := ehomeModels.EdgeDevice{
		Name:   "recent-device",
		NodeID: "recent-node-001",
		Status: "active",
	}
	db.Create(&dev)

	// Set recent data — 10s ago
	recentTime := time.Now().Add(-10 * time.Second)
	d.mu.Lock()
	d.activeDevices[dev.ID] = recentTime
	d.cacheReady = true
	d.mu.Unlock()

	d.checkEdgeDevicesOffline()

	var updated ehomeModels.EdgeDevice
	db.First(&updated, dev.ID)
	if updated.Status != "active" {
		t.Errorf("expected device to remain 'active', got %s", updated.Status)
	}
}

// TestLoadActiveDevices verifies that loadActiveDevices populates the cache
// from the database.
func TestLoadActiveDevices(t *testing.T) {
	d, db := setupExtraDetector(t)

	// Create some active and inactive devices
	now := time.Now()
	dev1 := ehomeModels.EdgeDevice{Name: "dev1", Status: "active", LastDataAt: &now}
	dev2 := ehomeModels.EdgeDevice{Name: "dev2", Status: "active"}
	dev3 := ehomeModels.EdgeDevice{Name: "dev3", Status: "offline"}
	db.Create(&dev1)
	db.Create(&dev2)
	db.Create(&dev3)

	d.loadActiveDevices()

	d.mu.RLock()
	count := len(d.activeDevices)
	_, hasDev1 := d.activeDevices[dev1.ID]
	_, hasDev2 := d.activeDevices[dev2.ID]
	_, hasDev3 := d.activeDevices[dev3.ID]
	cacheReady := d.cacheReady
	d.mu.RUnlock()

	if count != 2 {
		t.Errorf("expected 2 active devices in cache, got %d", count)
	}
	if !hasDev1 {
		t.Error("expected dev1 in cache")
	}
	if !hasDev2 {
		t.Error("expected dev2 in cache")
	}
	if hasDev3 {
		t.Error("dev3 (offline) should not be in cache")
	}
	if !cacheReady {
		t.Error("expected cacheReady=true after loadActiveDevices")
	}
}

// pending_deviceCount is a test helper that returns the number of active
// devices in the cache. Uses a different name to avoid collision with
// offlinedetector_test.go helpers.
func (d *Detector) pending_deviceCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.activeDevices)
}
