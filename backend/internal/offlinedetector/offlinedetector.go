package offlinedetector

import (
	"ehome/backend/internal/events"
	"ehome/backend/pkg/logger"
	"strconv"
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
}

// NewDetector creates a new offline detector
func NewDetector(db *gorm.DB, wsHub *websocket.Hub) *Detector {
	return &Detector{
		db:    db,
		wsHub: wsHub,
		quit:  make(chan struct{}),
	}
}

// Start begins the offline detection loop
func (d *Detector) Start() {
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
}

// checkRedisHeartbeats checks Redis TTL for all collectors
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
		deviceID := strconv.FormatInt(col.NodeID, 10)
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
		if redis.IsOnline(strconv.FormatInt(col.NodeID, 10)) {
			continue
		}

		// Check if last_seen is older than 90s (L3: DB fallback)
		if col.LastSeen != nil && now.Sub(*col.LastSeen) > 90*time.Second {
			d.markOffline(strconv.FormatInt(col.NodeID, 10), "db_last_seen_timeout")
		}
	}
}

// markOffline marks a collector as offline
func (d *Detector) markOffline(deviceID, reason string) {
	logger.Infof("[Offline] %s: %s", deviceID, reason)

	// Update DB
	d.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"status": "offline",
	})

	// Record event
	var collector models.Node
	if err := d.db.Where("node_id = ?", deviceID).First(&collector).Error; err == nil {
		d.db.Create(&models.NodeEvent{
			NodeID: collector.ID,
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

// UpdateHeartbeat updates the heartbeat for a collector
func (d *Detector) UpdateHeartbeat(deviceID string) {
	if redis.Client != nil {
		redis.SetHeartbeat(deviceID, 15*time.Second)
	}
}
