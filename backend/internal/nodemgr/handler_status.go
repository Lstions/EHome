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
	var configHash string // v2.2: config_hash from device

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
		case 6: // v2.2: config_hash
			configHash = frame.GetString(field)
		}
	}

	// Map ESP32 sync_state varint to config_sync_state string.
	// idle (0) = device finished applying config → mark in_sync (self-healing).
	// This is the primary recovery path when ConfigResult (0x05) is lost.
	var syncState string
	switch syncStateVarint {
	case 0:
		syncState = "in_sync"
	case 1:
		syncState = "syncing"
	case 2:
		syncState = "error"
	}

	// Update node status
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for status update", deviceID)
		return
	}

	// Populate node_id → node.ID cache for worker pool lookups
	nodeIDCache.Store(deviceID, node.ID)

	oldStatus := node.Status
	now := time.Now()
	node.Status = status
	node.LastSeen = &now
	node.UptimeSeconds = uint32(uptimeSec)
	node.OnlineDuration = uint64(uptimeSec)

	// last_online_time = 本次上线的起始时刻，只在 offline→online 转换时设置。
	// 在线期间不覆盖，这样用户看到的是"什么时候上线的"而非"几秒前"。
	if status == "online" && oldStatus != "online" {
		node.LastOnlineTime = &now
	}

	// v2.1: update config_sync_state and config_epoch from device report
	if syncState != "" {
		node.ConfigSyncState = syncState
	}
	node.ConfigEpoch = configEpoch

	logger.Infof("[%s] StatusReport: status=%s uptime=%d ch=%d epoch=%d sync_state=%d(%s) hash=%s",
		deviceID, status, uptimeSec, channelCount, configEpoch, syncStateVarint, syncState, configHash)

	m.db.Save(&node)

	// Record event on status change
	if oldStatus != status {
		m.db.Create(&models.NodeEvent{
			NodeID: node.NodeID,
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
		ConfigHash:   configHash,
	}

	decision := m.syncGate.OnStatusReport(deviceID, rpt)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] StatusReport push: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	}

	// offline→online detection
	if oldStatus == "offline" && status == "online" {
		m.triggerDeviceInit(node.NodeID, deviceID)
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id":        deviceID,
		"status":         status,
		"uptime_seconds": uptimeSec,
		"channel_count":  channelCount,
	})
}
