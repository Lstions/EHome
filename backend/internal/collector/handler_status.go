package collector

import (
	"ehome/backend/pkg/logger"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
)

// handleStatusReport processes StatusReport (type=0x02)
func (m *Manager) handleStatusReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode StatusReport: %v", deviceID, err)
		return
	}

	var uptimeSec uint64
	var status string
	var channelCount uint64

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

	// offline→online detection
	if oldStatus == "offline" && status == "online" {
		m.triggerDeviceInit(collector.ID, deviceID)
	}

	// WebSocket push
	m.wsHub.BroadcastEvent("collector_status", map[string]interface{}{
		"device_id":      deviceID,
		"status":         status,
		"uptime_seconds": uptimeSec,
		"channel_count":  channelCount,
	})
}
