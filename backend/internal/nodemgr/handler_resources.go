package nodemgr

import (
	"encoding/json"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm/clause"
)

// handleResourceReport processes ResourceReport (type=0x19)
// Field 1: resource_count (varint)
// Field 2: resources_json (string, JSON containing buses, channels, etc.)
// Field 3: platform (string, e.g. "ESP32-C6")
func (m *Manager) handleResourceReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ResourceReport: %v", deviceID, err)
		return
	}

	var resourceCount uint64
	var resourcesJSON string
	var platform string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			resourceCount = frame.GetUint64(field)
		case 2:
			resourcesJSON = frame.GetString(field)
		case 3:
			platform = frame.GetString(field)
		}
	}

	logger.Infof("[%s] ResourceReport: count=%d platform=%s json_len=%d",
		deviceID, resourceCount, platform, len(resourcesJSON))

	// Find node
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Warnf("[%s] Node not found for ResourceReport", deviceID)
		return
	}

	// Parse JSON to extract buses and channels
	var resourceData struct {
		Buses    map[string]interface{} `json:"buses"`
		Channels []struct {
			ID           uint   `json:"id"`
			HardwareType string `json:"hardware_type"`
			HardwareID   string `json:"hardware_id"`
			BusType      string `json:"bus_type"`
			BusConfig    string `json:"bus_config"`
			Enabled      bool   `json:"enabled"`
		} `json:"channels"`
	}

	if resourcesJSON != "" {
		if err := json.Unmarshal([]byte(resourcesJSON), &resourceData); err != nil {
			logger.Warnf("[%s] Failed to parse resources JSON: %v", deviceID, err)
		}
	}

	// Extract buses part for capabilities
	busesJSON := ""
	if resourceData.Buses != nil {
		if busesBytes, err := json.Marshal(resourceData.Buses); err == nil {
			busesJSON = string(busesBytes)
		}
	}

	// Update node: capabilities (buses only), hardware_info (full JSON), platform
	updates := map[string]interface{}{
		"hardware_info": resourcesJSON,
		"platform":      platform,
	}
	if busesJSON != "" {
		updates["capabilities"] = busesJSON
	}
	m.db.Model(&node).Updates(updates)

	// Upsert channels
	for _, ch := range resourceData.Channels {
		channel := models.Channel{
			NodeID:       node.ID,
			HardwareType: ch.HardwareType,
			HardwareID:   ch.HardwareID,
			BusType:      ch.BusType,
			BusConfig:    ch.BusConfig,
			Enabled:      ch.Enabled,
		}

		// Upsert: find by node_id + hardware_id, or create new
		result := m.db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_id"}, {Name: "hardware_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"hardware_type", "bus_type", "bus_config", "enabled", "updated_at"}),
		}).Create(&channel)
		if result.Error != nil {
			// Fallback: try find-by-params then create or update
			var existing models.Channel
			if err := m.db.Where("node_id = ? AND hardware_id = ?", node.ID, ch.HardwareID).First(&existing).Error; err == nil {
				m.db.Model(&existing).Updates(map[string]interface{}{
					"hardware_type": ch.HardwareType,
					"bus_type":      ch.BusType,
					"bus_config":    ch.BusConfig,
					"enabled":       ch.Enabled,
				})
			} else {
				channel.NodeID = node.ID
				m.db.Create(&channel)
			}
		}
		logger.Infof("[%s] Upserted channel: hw_id=%s type=%s", deviceID, ch.HardwareID, ch.BusType)
	}

	// WebSocket push: node_resources_updated
	m.wsHub.BroadcastEvent(events.NodeResourcesUpdated, map[string]interface{}{
		"node_id":        deviceID,
		"resource_count": resourceCount,
		"platform":       platform,
		"buses":          resourceData.Buses,
	})
}
