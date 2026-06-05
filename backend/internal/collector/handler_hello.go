package collector

import (
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// parseDeviceID parses a deviceID string to int64 with logging on failure.
func parseDeviceID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		logger.Warnf("[%s] Device Hello with non-numeric deviceID, parse failed: %v", s, err)
	}
	return n, err
}

// handleHello processes Hello messages (type=0x01)
// v2.1: parses 8 fields (4 new: config_epoch, nvs_has_config, last_manifest, protocol_version)
// and routes through SyncGate for unified sync decision.
func (m *Manager) handleHello(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode Hello: %v", deviceID, err)
		return
	}

	var firmwareVersion, model string
	var channelCount uint64

	// v2.1 new fields (defaults for v2.0 compatibility)
	var configEpoch uint64
	var nvsHasConfig = true // default true for v2.0 firmware (they have config if they're sending Hello)
	var lastManifest string
	var protocolVersion = "2.0"

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 2:
			firmwareVersion = frame.GetString(field)
		case 3:
			model = frame.GetString(field)
		case 4:
			channelCount = frame.GetUint64(field)
		case 5: // v2.1: config_epoch
			configEpoch = frame.GetUint64(field)
		case 6: // v2.1: nvs_has_config
			nvsHasConfig = frame.GetBool(field)
		case 7: // v2.1: last_manifest
			lastManifest = frame.GetString(field)
		case 8: // v2.1: protocol_version
			protocolVersion = frame.GetString(field)
		}
	}

	logger.Infof("[%s] Hello: fw=%s model=%s channels=%d epoch=%d nvs=%v manifest=%s proto=%s",
		deviceID, firmwareVersion, model, channelCount, configEpoch, nvsHasConfig, lastManifest, protocolVersion)

	// Immediately send HelloAck (0x12) to confirm handshake
	serverTime := uint64(time.Now().UnixMilli())
	if err := m.SendHelloAck(deviceID, serverTime, 0); err != nil {
		logger.Infof("[%s] Failed to send HelloAck: %v", deviceID, err)
	} else {
		logger.Infof("[%s] HelloAck sent: server_time=%d features=0", deviceID, serverTime)
	}

	nodeID, err := parseDeviceID(deviceID)
	if err != nil {
		logger.Warnf("[%s] Ignoring Hello: invalid deviceID", deviceID)
		return
	}

	// Upsert collector
	var collector models.Node
	result := m.db.Where("node_id = ?", deviceID).First(&collector)
	now := time.Now()
	oldStatus := ""
	if result.Error == gorm.ErrRecordNotFound {
		collector = models.Node{
			NodeID:          nodeID,
			Model:           model,
			FirmwareVersion: firmwareVersion,
			Status:          "online",
			LastSeen:        &now,
			UptimeSeconds:   0,
		}
		m.db.Create(&collector)
		m.db.Create(&models.NodeEvent{
			NodeID: collector.ID,
			EventType:   "online",
			NewStatus:   "online",
		})
	} else {
		oldStatus = collector.Status
		collector.FirmwareVersion = firmwareVersion
		collector.Model = model
		collector.Status = "online"
		collector.LastSeen = &now
		m.db.Save(&collector)
		if oldStatus != "online" {
			m.db.Create(&models.NodeEvent{
				NodeID: collector.ID,
				EventType:   "online",
				OldStatus:   oldStatus,
				NewStatus:   "online",
			})
		}
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id":  deviceID,
		"status":   "online",
		"model":    model,
		"firmware": firmwareVersion,
	})

	// OTA state reconciliation per docs §6.4.3: if device Hello reports
	// the target firmware version of an in-flight OTA task, mark it success.
	if m.otaMgr != nil {
		m.otaMgr.HandleHelloOTACompletion(collector.ID, deviceID, firmwareVersion)
	}

	// === v2.1: SyncGate decision (replaces ad-hoc hash check) ===
	helloMsg := &HelloMsg{
		NodeID:        deviceID,
		FirmwareVersion: firmwareVersion,
		Model:           model,
		ChannelCount:    channelCount,
		ConfigEpoch:     configEpoch,
		NvsHasConfig:    nvsHasConfig,
		LastManifest:    lastManifest,
		ProtocolVersion: protocolVersion,
	}

	decision := m.syncGate.OnHello(deviceID, helloMsg)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] Hello push: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	} else {
		logger.Infof("[sync_id=%s] Hello skip: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
	}

	// offline→online detection: trigger device initialization
	if oldStatus == "offline" || oldStatus == "" {
		m.triggerDeviceInit(collector.ID, deviceID)
	}

	// HomeAssistant Discovery: publish on first registration or status change
	if result.Error == gorm.ErrRecordNotFound || oldStatus == "offline" || oldStatus == "" {
		m.publishHADiscovery(collector.ID, deviceID)
	}

	// Async ping
	go m.SendPing(deviceID)
}
