package collector

import (
	"ehome/backend/internal/events"
	"ehome/backend/pkg/logger"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
)

// handleStatusReport processes StatusReport (type=0x02)
// v2.1: parses 5 fields (2 new: config_epoch, sync_state)
// and routes through SyncGate for config re-sync on offline→online (fixes G5).
func (m *Manager) handleStatusReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode StatusReport: %v", deviceID, err)
		return
	}

	var uptimeSec uint64
	var status string
	var channelCount uint64

	// v2.1 new fields
	var configEpoch uint64
	var syncState string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			uptimeSec = frame.GetUint64(field)
		case 2:
			status = frame.GetString(field)
		case 3:
			channelCount = frame.GetUint64(field)
		case 4: // v2.1: config_epoch
			configEpoch = frame.GetUint64(field)
		case 5: // v2.1: sync_state
			syncState = frame.GetString(field)
		}
	}

	// Update collector
	var collector models.Collector
	if err := m.db.Where("device_id = ?", deviceID).First(&collector).Error; err != nil {
		logger.Infof("[%s] Collector not found for status update", deviceID)
		return
	}

	oldStatus := collector.Status
	now := time.Now()
	collector.Status = status
	collector.LastSeen = &now
	collector.UptimeSeconds = uint32(uptimeSec)

	// v2.1: update config_sync_state if provided
	if syncState != "" {
		collector.ConfigStatus = syncState
	}

	m.db.Save(&collector)

	// Record event on status change
	if oldStatus != status {
		m.db.Create(&models.CollectorEvent{
			CollectorID: collector.ID,
			EventType:   "status_change",
			OldStatus:   oldStatus,
			NewStatus:   status,
		})
	}

	// === v2.1: SyncGate decision on StatusReport (fixes G5) ===
	rpt := &StatusReportMsg{
		UptimeSec:    uptimeSec,
		Status:       status,
		ChannelCount: channelCount,
		ConfigEpoch:  configEpoch,
		SyncState:    syncState,
	}

	decision := m.syncGate.OnStatusReport(deviceID, rpt)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] StatusReport push: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	}

	// offline→online detection
	if oldStatus == "offline" && status == "online" {
		m.triggerDeviceInit(collector.ID, deviceID)
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.CollectorStatus, map[string]interface{}{
		"device_id":      deviceID,
		"status":         status,
		"uptime_seconds": uptimeSec,
		"channel_count":  channelCount,
	})
}
