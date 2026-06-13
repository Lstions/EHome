package nodemgr

import (
	"ehome/backend/pkg/logger"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
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
		if configEpoch > 0 {
			updates["config_epoch"] = configEpoch
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

// sendConfigManifest sends configuration to a device with full nested sub-structures
func (m *Manager) sendConfigManifest(deviceID string) {
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for config", deviceID)
		return
	}

	var templates []models.ConfigTemplate
	m.db.Where("node_id = ?", node.ID).Find(&templates)

	var channels []models.Channel
	m.db.Where("node_id = ?", node.ID).Find(&channels)

	// Build manifest with manifest_id
	manifestID := fmt.Sprintf("v2-%d", time.Now().UnixMilli())

	enc := frame.NewEncoder(frame.MsgConfigMfst)
	enc.EncodeString(1, manifestID)

	// Encode templates (field 2, repeated sub-structure)
	for _, tmpl := range templates {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(tmpl.ID))
		if tmpl.WriteData != "" {
			writeHex := tmpl.WriteData
			if strings.HasPrefix(writeHex, "\\x") || strings.HasPrefix(writeHex, "0x") {
				writeHex = writeHex[2:]
			}
			if writeBytes, err := hex.DecodeString(writeHex); err == nil && len(writeBytes) > 0 {
				subEnc.EncodeBytes(2, writeBytes)
			}
		}
		if tmpl.ReadLength > 0 {
			subEnc.EncodeVarint(3, uint64(tmpl.ReadLength))
		}
		if tmpl.DelayMs > 0 {
			subEnc.EncodeVarint(4, uint64(tmpl.DelayMs))
		}
		enc.EncodeSubFrame(2, subEnc.Bytes())
	}

	// Encode channels (field 3, repeated sub-structure)
	for _, ch := range channels {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(ch.ID))
		subEnc.EncodeString(2, ch.HardwareID)

		// Packed repeated template_ids (field 3)
		if ch.TemplateIDs != "" {
			for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
				if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
					subEnc.EncodeVarint(3, id)
				}
			}
		}

		subEnc.EncodeVarint(4, uint64(ch.IntervalMs))
		subEnc.EncodeBool(5, ch.Enabled)

		// Bus type: map string or numeric to uint8
		busTypeMap := map[string]uint8{
			"UART": 1, "1": 1,
			"I2C": 2, "2": 2,
			"SPI": 3, "3": 3,
			"GPIO": 4, "4": 4,
			"ADC": 5, "5": 5,
		}
		logger.Infof("config: channel %d BusType=%q BusConfig=%q", ch.ID, ch.BusType, ch.BusConfig)
		if bt, ok := busTypeMap[strings.ToUpper(ch.BusType)]; ok {
			subEnc.EncodeVarint(6, uint64(bt))
		} else {
			logger.Warnf("config: unknown bus_type %q for channel %d", ch.BusType, ch.ID)
		}

		// Bus config (field 7, bytes) — support hex (\x...) or JSON string
		busConfigData := ch.BusConfig
		if busConfigData == "" {
			busConfigData = ch.Config
		}
		if busConfigData != "" {
			if strings.HasPrefix(busConfigData, "\\x") || strings.HasPrefix(busConfigData, "\\x") {
				// PostgreSQL bytea hex format: decode to binary
				hexStr := busConfigData[2:]
				if decoded, err := hex.DecodeString(hexStr); err == nil {
					subEnc.EncodeBytes(7, decoded)
				} else {
					subEnc.EncodeString(7, busConfigData)
				}
			} else {
				subEnc.EncodeString(7, busConfigData)
			}
		}

		enc.EncodeSubFrame(3, subEnc.Bytes())
	}

	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		logger.Infof("[%s] Failed to send config: %v", deviceID, err)
	} else {
		logger.Infof("[%s] ConfigManifest sent: id=%s %d templates, %d channels",
			deviceID, manifestID, len(templates), len(channels))
		m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).
			Update("config_version", manifestID)
	}
}
