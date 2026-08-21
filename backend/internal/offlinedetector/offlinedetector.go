package offlinedetector

import (
	"ehome/backend/internal/events"
	"ehome/backend/pkg/logger"
	"sync"
	"time"

	"ehome/backend/internal/models"
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
	mu            sync.RWMutex
	activeDevices map[uint]time.Time // device PK → last_data_at (only active devices)
	cacheReady    bool
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

// checkOffline performs two-layer offline detection (parallel).
// Each layer uses a session-isolated DB handle (db.Session) to avoid
// Statement races between concurrent GORM calls.
// Redis 退役 (方案 v3.4 §4 任务B): 原 checkRedisHeartbeats (L1) 已删除，
// checkDBLastSeen (L3) 是采集器离线判定的唯一路径。
func (d *Detector) checkOffline() {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.checkDBLastSeen(d.db.Session(&gorm.Session{}))
	}()

	go func() {
		defer wg.Done()
		d.checkEdgeDevicesOffline(d.db.Session(&gorm.Session{}))
	}()

	wg.Wait()
}

// checkDBLastSeen checks DB last_seen for online collectors — the single
// offline-detection path since Redis retirement (方案 v3.4 §4 任务B).
// Nodes with LastSeen==nil are skipped (unchanged semantics).
func (d *Detector) checkDBLastSeen(db *gorm.DB) {
	var collectors []models.Node
	if err := db.Where("status = ?", "online").Find(&collectors).Error; err != nil {
		return
	}

	now := time.Now()
	for _, col := range collectors {
		// Check if last_seen is older than 90s (18 个 5s 心跳周期)
		if col.LastSeen != nil && now.Sub(*col.LastSeen) > 90*time.Second {
			d.markOffline(db, col.NodeID, "db_last_seen_timeout")
		}
	}
}

// markOffline marks a node as offline using the provided session.
func (d *Detector) markOffline(db *gorm.DB, deviceID, reason string) {
	logger.Infof("[Offline] %s: %s", deviceID, reason)

	// Update DB
	db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"status": "offline",
	})

	// Record event
	var nodeRecord models.Node
	if err := db.Where("node_id = ?", deviceID).First(&nodeRecord).Error; err == nil {
		db.Create(&models.NodeEvent{
			NodeID:    nodeRecord.NodeID,
			EventType: "offline",
			OldStatus: "online",
			NewStatus: "offline",
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
func (d *Detector) checkEdgeDevicesOffline(db *gorm.DB) {
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
	if err := db.Where("id IN ? AND status = ?", staleIDs, "active").Find(&staleDevices).Error; err != nil {
		return
	}

	for _, dev := range staleDevices {
		d.markEdgeDeviceOffline(db, dev)
	}
}

// markEdgeDeviceOffline marks an edge device as offline and broadcasts the change.
func (d *Detector) markEdgeDeviceOffline(db *gorm.DB, dev models.EdgeDevice) {
	logger.Infof("[EdgeDevice Offline] id=%d name=%s node_id=%s — no data for >60s", dev.ID, dev.Name, dev.NodeID)

	db.Model(&dev).Updates(map[string]interface{}{
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

// UpdateHeartbeat updates the heartbeat for a node.
// Redis 退役 (方案 v3.4 §4 任务B): 原 redis.SetHeartbeat TTL 刷新已删除。
// 方法保留为 no-op 以维持 Detector 接口稳定——未来多实例部署时
// 在此接入分布式心跳实现即可（方案 §4.2 升级路径）。
func (d *Detector) UpdateHeartbeat(deviceID string) {
	_ = deviceID // no-op: offline detection now relies solely on DB last_seen
}
