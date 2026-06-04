package collector

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
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
		}
	}

	logger.Infof("[%s] DataReport: ch=%d ts=%d seq=%d req=%d err=%d raw=%x",
		deviceID, channelID, timestamp, sequence, requestID, errorCode, rawData)

	// Dispatch to worker pool (non-blocking)
	// Look up collector numeric ID (may be 0 if not found, but that's fine for fanout)
	var collectorID uint
	var collector models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&collector).Error; err == nil {
		collectorID = collector.ID
	}
	job := dataReportJob{
		deviceID:    deviceID,
		collectorID: collectorID,
		channelID:   channelID,
		timestamp:   timestamp,
		sequence:    sequence,
		rawData:     rawData,
		errorCode:   errorCode,
		requestID:   requestID,
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

// parseAndStoreData parses raw data using device drivers and stores in unified_data
func (m *Manager) parseAndStoreData(collectorID uint, deviceID string, channelID uint64, rawData []byte) {
	// Get device type from database
	var device models.EdgeDevice
	if err := m.db.Where("channel_id = ?", channelID).First(&device).Error; err != nil {
		return
	}

	// Get driver and parse
	driver, err := drivers.Get(device.Type)
	if err != nil {
		logger.Infof("[%s] No driver for type %s: %v", deviceID, device.Type, err)
		return
	}

	sensorData, err := driver.ParseData(rawData)
	if err != nil {
		logger.Infof("[%s] Failed to parse data: %v", deviceID, err)
		return
	}

	// Store parsed data
	for _, sd := range sensorData {
		m.db.Create(&models.UnifiedData{
			DeviceID:   device.ID,
			SensorName: sd.Name,
			Value:      sd.Value,
			Unit:       sd.Unit,
			Timestamp:  time.Now(),
		})
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
			CollectorID: collectorID,
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
			"node_id":        collectorID, // v2.2 新增 (同一值)
			"channel_id":     channelID,
			"data":           dataMap,
			"collected_at":   time.Now().Format(time.RFC3339),
		}
		m.wsHub.BroadcastEvent(events.DataUpdate, payload)
	}

	logger.Infof("[%s] Parsed %d sensors using driver %s", deviceID, len(sensorData), device.Type)
}
