package nodemgr

import (
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"time"
)

// handleConfigResult processes ConfigResult (type=0x05)
// v2.1: also updates config_sync_state
func (m *Manager) handleConfigResult(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ConfigResult: %v", deviceID, err)
		return
	}

	var manifestID string
	var success bool

	// v2.1 optional fields
	var configEpoch uint64
	var syncID string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			manifestID = frame.GetString(field)
		case 2:
			success = frame.GetBool(field)
		case 3: // v2.1: config_epoch
			configEpoch = frame.GetUint64(field)
		case 4: // v2.1: sync_id
			syncID = frame.GetString(field)
		}
	}

	logger.Infof("[%s] ConfigResult: manifest=%s success=%v epoch=%d sync_id=%s",
		deviceID, manifestID, success, configEpoch, syncID)

	// Update node config version
	if success {
		now := time.Now()
		updates := map[string]interface{}{
			"config_version":    manifestID,
			"config_status":     "applied",
			"config_sync_state": "in_sync",
			"last_sync_at":      now,
		}
		if syncID != "" {
			updates["last_sync_id"] = syncID
		}
		m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(updates)
	} else {
		m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).
			Update("config_status", "failed")
	}
}

// handleConfigReport processes ConfigReport (type=0x11)
func (m *Manager) handleConfigReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ConfigReport: %v", deviceID, err)
		return
	}

	var requestID, manifestID string
	var templateCount, channelCount, uptimeSec uint64

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			requestID = frame.GetString(field)
		case 2:
			manifestID = frame.GetString(field)
		case 3:
			templateCount = frame.GetUint64(field)
		case 4:
			channelCount = frame.GetUint64(field)
		case 5:
			uptimeSec = frame.GetUint64(field)
		}
	}

	logger.Infof("[%s] ConfigReport: request=%s manifest=%s templates=%d channels=%d uptime=%d",
		deviceID, requestID, manifestID, templateCount, channelCount, uptimeSec)

	// Verify config sync: compare reported manifest with DB
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err == nil {
		if node.ConfigVersion != manifestID {
			logger.Infof("[%s] Config mismatch! DB=%s device=%s", deviceID, node.ConfigVersion, manifestID)
		}
	}
}


