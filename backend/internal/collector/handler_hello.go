package collector

import (
	"ehome/backend/pkg/logger"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"

	"gorm.io/gorm"
)

// handleHello processes Hello messages (type=0x01)
func (m *Manager) handleHello(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode Hello: %v", deviceID, err)
		return
	}

	var firmwareVersion, model string
	var channelCount uint64

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
		}
	}

	logger.Infof("[%s] Hello: fw=%s model=%s channels=%d", deviceID, firmwareVersion, model, channelCount)

	// Immediately send HelloAck (0x12) to confirm handshake
	serverTime := uint64(time.Now().UnixMilli())
	if err := m.SendHelloAck(deviceID, serverTime, 0); err != nil {
		logger.Infof("[%s] Failed to send HelloAck: %v", deviceID, err)
	} else {
		logger.Infof("[%s] HelloAck sent: server_time=%d features=0", deviceID, serverTime)
	}

	// Upsert collector
	var collector models.Collector
	result := m.db.Where("device_id = ?", deviceID).First(&collector)
	now := time.Now()
	oldStatus := ""
	if result.Error == gorm.ErrRecordNotFound {
		collector = models.Collector{
			DeviceID:        deviceID,
			Model:           model,
			FirmwareVersion: firmwareVersion,
			Status:          "online",
			LastSeen:        &now,
			UptimeSeconds:   0,
		}
		m.db.Create(&collector)
		m.db.Create(&models.CollectorEvent{
			CollectorID: collector.ID,
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
			m.db.Create(&models.CollectorEvent{
				CollectorID: collector.ID,
				EventType:   "online",
				OldStatus:   oldStatus,
				NewStatus:   "online",
			})
		}
	}

	// WebSocket push
	m.wsHub.BroadcastEvent("collector_status", map[string]interface{}{
		"device_id": deviceID,
		"status":    "online",
		"model":     model,
		"firmware":  firmwareVersion,
	})

	// Config hash check + 30s dedup window
	var templates []models.ConfigTemplate
	m.db.Where("collector_id = ?", collector.ID).Find(&templates)
	var channels []models.Channel
	m.db.Where("collector_id = ?", collector.ID).Find(&channels)

	// Calculate hash from templates + channels data
	hashData := m.buildHashData(templates, channels)
	newHash := m.hashMgr.CalcConfigHash(hashData, nil)

	// Check if config should be sent (hash changed or first time)
	if m.hashMgr.ShouldSendConfig(deviceID, newHash) {
		logger.Infof("[%s] Config hash changed or first time, sending ConfigManifest (hash=%s)", deviceID, newHash)
		m.sendConfigManifest(deviceID)
		m.hashMgr.UpdateLastSent(deviceID)
	} else {
		logger.Infof("[%s] Config unchanged (hash=%s), skip ConfigManifest", deviceID, newHash)
	}

	// offline→online detection: trigger device initialization
	if oldStatus == "offline" || oldStatus == "" {
		m.triggerDeviceInit(collector.ID, deviceID)
	}

	// Async ping
	go m.SendPing(deviceID)
}
