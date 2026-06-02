package collector

import (
	"time"

	"ehome/backend/internal/drivers"
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
	job := dataReportJob{
		deviceID:  deviceID,
		channelID: channelID,
		timestamp: timestamp,
		sequence:  sequence,
		rawData:   rawData,
		errorCode: errorCode,
		requestID: requestID,
	}

	select {
	case m.dataCh <- job:
		// submitted
	default:
		logger.Warnf("[%s] Worker pool full, dropping DataReport", deviceID)
	}
}

// parseAndStoreData parses raw data using device drivers and stores in unified_data
func (m *Manager) parseAndStoreData(collectorID uint, deviceID string, channelID uint64, rawData []byte) {
	// Get device type from database
	var device models.Device
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

	// Publish to HomeAssistant
	if m.ha != nil {
		m.ha.PublishState(deviceID, sensorData)
	}

	logger.Infof("[%s] Parsed %d sensors using driver %s", deviceID, len(sensorData), device.Type)
}
