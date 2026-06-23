package nodemgr

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// handleDataReport processes DataReport (type=0x03)
// Fast path: decode frame fields, then dispatch to worker pool
func (m *Manager) handleDataReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode DataReport: %v", deviceID, err)
		return
	}

	var channelID, timestamp, sequence uint64
	var rawData []byte
	var errorCode, requestID uint64
	var edgeDeviceID, commandIndex uint64

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			channelID = frame.GetUint64(field)
		case 2:
			timestamp = frame.GetUint64(field)
		case 3:
			sequence = frame.GetUint64(field)
		case 4:
			rawData = frame.GetBytes(field)
		case 5:
			errorCode = frame.GetUint64(field)
		case 6:
			requestID = frame.GetUint64(field)
		case 7:
			edgeDeviceID = frame.GetUint64(field)
		case 8:
			commandIndex = frame.GetUint64(field)
		}
	}

	logger.Infof("[%s] DataReport: ch=%d ts=%d seq=%d req=%d err=%d raw=%x",
		deviceID, channelID, timestamp, sequence, requestID, errorCode, rawData)

	// G10: Record data received metric
	status := "ok"
	if errorCode != 0 {
		status = "error"
	}
	metrics.DataReceivedTotal.WithLabelValues(deviceID, status).Inc()

	// Dispatch to worker pool (non-blocking)
	// Look up node numeric ID (may be 0 if not found, but that's fine for fanout)
	var collectorID uint
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err == nil {
		collectorID = node.ID
	}
	job := dataReportJob{
		deviceID:     deviceID,
		collectorID:  collectorID,
		channelID:    channelID,
		timestamp:    timestamp,
		sequence:     sequence,
		rawData:      rawData,
		errorCode:    errorCode,
		requestID:    requestID,
		edgeDeviceID: edgeDeviceID,
		commandIndex: commandIndex,
	}

	select {
	case m.dataCh <- job:
		// submitted to worker pool
	default:
		// Worker pool full, fallback to sync processing to avoid data loss
		logger.Warnf("[%s] Worker pool full, fallback to sync", deviceID)
		m.processDataReportJob(job)
	}
}

// findEdgeDeviceByChannelID resolves an edge_device from the channel_id in a DataReport.
//
// C6 firmware encodes channel_id as the config_mgr internal index (0-based), NOT the
// channels.id DB primary key.  When the direct lookup `WHERE channel_id = ?` succeeds
// (new firmware that sends the real DB id), we use that.  Otherwise we fall back to
// finding the channel by its position among the node's channels (ordered by id) and
// then looking up the edge_device on that channel.
//
// If edgeDeviceID > 0 (new firmware provides it directly), we look up by primary key
// first — this is the preferred fast path.
func (m *Manager) findEdgeDeviceByChannelID(deviceID string, channelID uint64, edgeDeviceID uint64) (models.EdgeDevice, bool) {
	var device models.EdgeDevice

	// Fast path: new firmware provides edge_device_id directly
	if edgeDeviceID > 0 {
		if err := m.db.Preload("Node").Where("id = ?", edgeDeviceID).First(&device).Error; err == nil {
			return device, true
		}
		// Fall through to channel-based lookup if edge_device_id lookup fails
	}

	// Try direct channel_id lookup (works when C6 sends channels.id as channel_id)
	if err := m.db.Preload("Node").Where("channel_id = ?", channelID).First(&device).Error; err == nil {
		return device, true
	}

	// Fallback: C6 sends channel_id as 0-based index into its config_mgr channel list.
	// Find the node's channels ordered by id, pick the one at index channelID,
	// then find the edge_device on that channel.
	var channels []models.Channel
	if err := m.db.Where("node_id = ?", deviceID).Order("id ASC").Find(&channels).Error; err != nil {
		return device, false
	}

	idx := int(channelID)
	if idx < 0 || idx >= len(channels) {
		logger.Infof("[%s] Channel index %d out of range (node has %d channels)", deviceID, channelID, len(channels))
		return device, false
	}

	realChannelID := channels[idx].ID
	if err := m.db.Preload("Node").Where("channel_id = ?", realChannelID).First(&device).Error; err != nil {
		// Last resort: if multiple edge_devices on this channel, try node_id + channel_id
		if err2 := m.db.Preload("Node").Where("channel_id = ? AND node_id = ?", realChannelID, deviceID).First(&device).Error; err2 != nil {
			return device, false
		}
	}

	logger.Infof("[%s] Resolved channel_id %d (C6 index) -> channels.id %d -> edge_device.id %d",
		deviceID, channelID, realChannelID, device.ID)
	return device, true
}

// parseAndStoreData parses raw data using device drivers and stores in unified_data
// Returns the parsed sensor (for WS broadcast) or nil on failure
func (m *Manager) parseAndStoreData(collectorID uint, deviceID string, channelID uint64, edgeDeviceID uint64, rawData []byte) map[string]interface{} {
	// Get device type from database
	device, found := m.findEdgeDeviceByChannelID(deviceID, channelID, edgeDeviceID)
	if !found {
		return nil
	}

	// Get driver and parse
	driver, err := drivers.Get(device.Type)
	if err != nil {
		logger.Infof("[%s] No driver for type %s: %v", deviceID, device.Type, err)
		return nil
	}

	sensorData, err := driver.ParseData(rawData)
	if err != nil {
		logger.Infof("[%s] Failed to parse data: %v", deviceID, err)
		return nil
	}

	// Store parsed data (C3 fix: batch INSERT instead of per-sensor db.Create)
	now := time.Now()
	records := make([]models.UnifiedData, 0, len(sensorData))
	for _, sd := range sensorData {
		records = append(records, models.UnifiedData{
			DeviceID:   device.ID,
			SensorName: sd.Name,
			Value:      sd.Value,
			Unit:       sd.Unit,
			Timestamp:  now,
		})
	}
	if len(records) > 0 {
		m.db.Create(&records)
	}

	// Update last_data_at and status on the edge device
	// M4 fix: Use atomic CAS to prevent multiple workers broadcasting the same transition
	oldStatus := device.Status
	result := m.db.Model(&device).Where("status = ?", "offline").Updates(map[string]interface{}{
		"last_data_at": now,
		"status":       "active",
	})
	// Only broadcast if this worker actually performed the transition (rows affected > 0)
	if result.RowsAffected > 0 && m.wsHub != nil {
		// M5 fix: Notify offline detector that device is active
		if m.offlineDetector != nil {
			m.offlineDetector.OnEdgeDeviceData(device.ID)
		}
		m.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": device.ID,
			"device_id":      device.ID,
			"device_name":    device.Name,
			"node_id":        device.NodeID,
			"channel_id":     device.ChannelID,
			"status":         "active",
			"reason":         "data_received",
		})
	} else if oldStatus != "offline" {
		// Device was already active, just update last_data_at
		m.db.Model(&device).Updates(map[string]interface{}{
			"last_data_at": now,
		})
		// M5 fix: Update cache timestamp
		if m.offlineDetector != nil {
			m.offlineDetector.OnEdgeDeviceData(device.ID)
		}
	}

	// F4.1: Also store raw data in device_data table for historical record
	if collectorID > 0 {
		dataJSON, _ := json.Marshal(map[string]interface{}{
			"raw_hex":    fmt.Sprintf("%x", rawData),
			"sensors":    sensorData,
			"channel_id": channelID,
			"timestamp":  time.Now().UnixMilli(),
		})
		m.db.Create(&models.DeviceData{
			DeviceID:    device.ID,
			NodeID: deviceID,
			DataJSON:    string(dataJSON),
			Timestamp:   time.Now(),
		})
	}

	// Publish to HomeAssistant
	if m.ha != nil {
		m.ha.PublishState(deviceID, sensorData)
	}

	// Broadcast parsed data via WebSocket for Dashboard real-time display
	if m.wsHub != nil && len(sensorData) > 0 {
		dataMap := make(map[string]interface{}, len(sensorData))
		for _, sd := range sensorData {
			dataMap[sd.Name] = sd.Value
		}
		payload := map[string]interface{}{
			"device_id":      device.ID, // v2.1 字段名 (保留兼容前端)
			"edge_device_id": device.ID, // v2.2 新增 (同一值)
			"device_name":    device.Name,
			"collector_id":   collectorID, // v2.1 字段名
			"collector_name": device.Node.Name, // v2.2 新增
			"node_id":        collectorID, // v2.2 新增 (同一值)
			"channel_id":     channelID,
			"data":           dataMap,
			"collected_at":   time.Now().Format(time.RFC3339),
		}
		m.wsHub.BroadcastEvent(events.DataUpdate, payload)
		logger.Debugf("[%s] Broadcast data_update: %v sensors", deviceID, len(sensorData))
	} else if m.wsHub == nil {
		logger.Warnf("[%s] wsHub is nil, skipping data_update broadcast", deviceID)
	}

	logger.Infof("[%s] Parsed %d sensors using driver %s", deviceID, len(sensorData), device.Type)

	// Return parsed data map for caller to include in channel_data event
	dataMap := make(map[string]interface{}, len(sensorData))
	for _, sd := range sensorData {
		dataMap[sd.Name] = sd.Value
	}
	return dataMap
}
