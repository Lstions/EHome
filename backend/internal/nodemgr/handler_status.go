package nodemgr

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
	var syncStateVarint uint64

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
		case 5: // v2.1: sync_state (varint enum from ESP32: 0=idle,1=syncing,2=error)
			syncStateVarint = frame.GetUint64(field)
		}
	}

	// Map ESP32 sync_state varint to config_sync_state string
	// Only update on explicit syncing/error states; idle (0) does not overwrite.
	var syncState string
	switch syncStateVarint {
	case 1:
		syncState = "syncing"
	case 2:
		syncState = "error"
	default:
		// varint 0 = idle — leave current state unchanged
	}

	// Update node
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for status update", deviceID)
		return
	}

	oldStatus := node.Status
	now := time.Now()
	node.Status = status
	node.LastSeen = &now
	node.UptimeSeconds = uint32(uptimeSec)
	node.OnlineDuration = uint64(uptimeSec)

	// Set last_online_time on every status report while online
	if status == "online" {
		node.LastOnlineTime = &now
	}

	// v2.1: update config_sync_state if provided
	if syncState != "" {
		node.ConfigSyncState = syncState
	}

	m.db.Save(&node)

	// Record event on status change
	if oldStatus != status {
		m.db.Create(&models.NodeEvent{
			NodeID: node.ID,
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
		m.triggerDeviceInit(node.ID, deviceID)
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id":        deviceID,
		"status":         status,
		"uptime_seconds": uptimeSec,
		"channel_count":  channelCount,
	})
}
