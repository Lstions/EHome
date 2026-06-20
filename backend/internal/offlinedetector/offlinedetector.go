package offlinedetector

import (
	"ehome/backend/internal/events"
	"ehome/backend/pkg/logger"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/redis"
	"ehome/backend/internal/websocket"

	"gorm.io/gorm"
)

// Detector implements three-layer offline detection
type Detector struct {
	db     *gorm.DB
	wsHub  *websocket.Hub
	ticker *time.Ticker
	quit   chan struct{}

	// M5 fix: Cache active edge device IDs to avoid full table scan every 5s
	mu             sync.RWMutex
	activeDevices  map[uint]time.Time // device PK → last_data_at (only active devices)
	cacheReady     bool
}

// NewDetector creates a new offline detector
func NewDetector(db *gorm.DB, wsHub *websocket.Hub) *Detector {
	return &Detector{
		db:            db,
		wsHub:         wsHub,
		quit:          make(chan struct{}),
		activeDevices: make(map[uint]time.Time),
	}
}

// Start begins the offline detection loop
func (d *Detector) Start() {
	// M5 fix: Initial load of active devices from DB
	d.loadActiveDevices()

	d.ticker = time.NewTicker(5 * time.Second)
	go d.loop()
	logger.Infof("Offline detector started (3-layer)")
}

// Stop stops the offline detection loop
func (d *Detector) Stop() {
	close(d.quit)
	d.ticker.Stop()
}

func (d *Detector) loop() {
	for {
		select {
		case <-d.ticker.C:
			d.checkOffline()
		case <-d.quit:
			return
		}
	}
}

// checkOffline performs three-layer offline detection
func (d *Detector) checkOffline() {
	// Layer 1: Check Redis heartbeats (fast, in-memory)
	d.checkRedisHeartbeats()

	// Layer 2: Check DB last_seen (SQL fallback)
	d.checkDBLastSeen()

	// Layer 3: Check edge devices with stale data (no data for 60s → offline)
	// M5 fix: Use cached device list instead of full DB scan
	d.checkEdgeDevicesOffline()
}

// checkRedisHeartbeats checks Redis TTL for all nodes
// BUG-05 fix: Instead of scanning Redis keys (which won't find expired ones),
// query DB for online nodes and verify their heartbeat key still exists.
func (d *Detector) checkRedisHeartbeats() {
	if redis.Client == nil {
		return // Redis not connected
	}

	// Get all nodes currently marked as online in DB
	var onlineNodes []models.Node
	if err := d.db.Where("status = ?", "online").Find(&onlineNodes).Error; err != nil {
		return
	}

	for _, col := range onlineNodes {
		deviceID := col.NodeID
		if !redis.IsOnline(deviceID) {
			// Heartbeat key missing/expired — mark offline
			d.markOffline(deviceID, "redis_ttl_expired")
		}
	}
}

// checkDBLastSeen checks DB last_seen for collectors without Redis heartbeat
func (d *Detector) checkDBLastSeen() {
	var collectors []models.Node
	if err := d.db.Where("status = ?", "online").Find(&collectors).Error; err != nil {
		return
	}

	now := time.Now()
	for _, col := range collectors {
		// Skip if still has Redis heartbeat
		if redis.IsOnline(col.NodeID) {
			continue
		}

		// Check if last_seen is older than 90s (L3: DB fallback)
		if col.LastSeen != nil && now.Sub(*col.LastSeen) > 90*time.Second {
			d.markOffline(col.NodeID, "db_last_seen_timeout")
		}
	}
}

// markOffline marks a node as offline
func (d *Detector) markOffline(deviceID, reason string) {
	logger.Infof("[Offline] %s: %s", deviceID, reason)

	// Update DB
	d.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"status": "offline",
	})

	// Record event
	var nodeRecord models.Node
	if err := d.db.Where("node_id = ?", deviceID).First(&nodeRecord).Error; err == nil {
		d.db.Create(&models.NodeEvent{
			NodeID: nodeRecord.NodeID,
			EventType:   "offline",
			OldStatus:   "online",
			NewStatus:   "offline",
		})
	}

	// WebSocket push
	d.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id": deviceID,
		"status":  "offline",
		"reason":  reason,
	})
}

// loadActiveDevices loads all active edge devices from DB into the cache.
// Called once at startup.
func (d *Detector) loadActiveDevices() {
	var devices []models.EdgeDevice
	if err := d.db.Where("status = ?", "active").Find(&devices).Error; err != nil {
		logger.Warnf("[OfflineDetector] Failed to load active devices: %v", err)
		return
	}

	d.mu.Lock()
	for _, dev := range devices {
		lastData := time.Time{}
		if dev.LastDataAt != nil {
			lastData = *dev.LastDataAt
		}
		d.activeDevices[dev.ID] = lastData
	}
	d.cacheReady = true
	d.mu.Unlock()

	logger.Infof("[OfflineDetector] Loaded %d active edge devices into cache", len(devices))
}

// OnEdgeDeviceData is called when an edge device reports data (status=active).
// M5 fix: Update the in-memory cache instead of relying on DB scan.
func (d *Detector) OnEdgeDeviceData(deviceID uint) {
	d.mu.Lock()
	d.activeDevices[deviceID] = time.Now()
	d.mu.Unlock()
}

// OnEdgeDeviceOffline is called when an edge device is marked offline.
// M5 fix: Remove from the in-memory cache.
func (d *Detector) OnEdgeDeviceOffline(deviceID uint) {
	d.mu.Lock()
	delete(d.activeDevices, deviceID)
	d.mu.Unlock()
}

// OnEdgeDeviceCreated is called when a new edge device is created with active status.
func (d *Detector) OnEdgeDeviceCreated(deviceID uint) {
	d.mu.Lock()
	d.activeDevices[deviceID] = time.Time{} // no data yet
	d.mu.Unlock()
}

// checkEdgeDevicesOffline finds edge devices still marked "active" whose
// last_data_at is older than 60 seconds and marks them "offline".
// M5 fix: Uses in-memory cache of active device IDs instead of full DB scan.
func (d *Detector) checkEdgeDevicesOffline() {
	threshold := time.Now().Add(-60 * time.Second)

	// Collect stale device IDs from cache (fast, no DB query)
	var staleIDs []uint
	d.mu.RLock()
	for id, lastData := range d.activeDevices {
		if !lastData.IsZero() && lastData.Before(threshold) {
			staleIDs = append(staleIDs, id)
		}
	}
	d.mu.RUnlock()

	if len(staleIDs) == 0 {
		return
	}

	// Fetch only stale devices from DB (targeted query, not full scan)
	var staleDevices []models.EdgeDevice
	if err := d.db.Where("id IN ? AND status = ?", staleIDs, "active").Find(&staleDevices).Error; err != nil {
		return
	}

	for _, dev := range staleDevices {
		d.markEdgeDeviceOffline(dev)
	}
}

// markEdgeDeviceOffline marks an edge device as offline and broadcasts the change.
func (d *Detector) markEdgeDeviceOffline(dev models.EdgeDevice) {
	logger.Infof("[EdgeDevice Offline] id=%d name=%s node_id=%s — no data for >60s", dev.ID, dev.Name, dev.NodeID)

	d.db.Model(&dev).Updates(map[string]interface{}{
		"status": "offline",
	})

	// M5 fix: Remove from cache
	d.OnEdgeDeviceOffline(dev.ID)

	// WebSocket push
	if d.wsHub != nil {
		d.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": dev.ID,
			"device_id":      dev.ID,
			"device_name":    dev.Name,
			"node_id":        dev.NodeID,
			"channel_id":     dev.ChannelID,
			"status":         "offline",
			"reason":         "data_timeout",
		})
	}
}

// UpdateHeartbeat updates the heartbeat for a node
func (d *Detector) UpdateHeartbeat(deviceID string) {
	if redis.Client != nil {
		redis.SetHeartbeat(deviceID, 15*time.Second)
	}
}
