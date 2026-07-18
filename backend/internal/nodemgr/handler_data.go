package nodemgr

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"
)

// P1-4: Backpressure — overflow goroutine limit for when worker pool is full
var overflowGoroutines int64

const maxOverflowGoroutines = 50

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
	var edgeDeviceID, commandIndex, commandTemplateID uint64

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
		case 9:
			commandTemplateID = frame.GetUint64(field)
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
	// P1-4: collectorID resolved in worker, not in MQTT callback
	job := dataReportJob{
		deviceID:          deviceID,
		channelID:         channelID,
		timestamp:         timestamp,
		sequence:          sequence,
		rawData:           rawData,
		errorCode:         errorCode,
		requestID:         requestID,
		edgeDeviceID:      edgeDeviceID,
		commandIndex:      commandIndex,
		commandTemplateID: commandTemplateID,
	}

	select {
	case m.dataCh <- job:
		// submitted to worker pool
	default:
		// P1-4: Backpressure — limit overflow goroutines via atomic CAS loop
		metrics.WorkerPoolOverflowTotal.Inc()
		for {
			current := atomic.LoadInt64(&overflowGoroutines)
			if current >= maxOverflowGoroutines {
				// Critical overload — block MQTT callback (better than data loss)
				metrics.WorkerPoolBackpressureBlockTotal.Inc()
				logger.Warnf("[%s] CRITICAL: overflow limit reached (%d), blocking MQTT callback", deviceID, current)
				m.processDataReportJob(job)
				break
			}
			if atomic.CompareAndSwapInt64(&overflowGoroutines, current, current+1) {
				go func() {
					defer atomic.AddInt64(&overflowGoroutines, -1)
					m.processDataReportJob(job)
				}()
				break
			}
			// CAS failed — another goroutine modified the counter, retry
		}
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

// parseAndStoreData parses raw data using DeviceConfig.Parser JSONB (primary)
// with Driver fallback, and stores in unified_data.
// commandIndex is the ConfigTemplate ID that was sent (0 if unknown).
// Returns the parsed sensor (for WS broadcast) or nil on failure.
func (m *Manager) parseAndStoreData(collectorID uint, deviceID string, channelID uint64, edgeDeviceID uint64, commandIndex uint64, rawData []byte) map[string]interface{} {
	// Get device type from database — Preload DeviceConfig for Parser JSONB access
	device, found := m.findEdgeDeviceByChannelID(deviceID, channelID, edgeDeviceID)
	if !found {
		return nil
	}

	// Primary path: try DeviceConfig.Parser JSONB (unified ConfigParser)
	var sensorData []parser.Field
	var parseMethod string

	if device.DeviceConfigID > 0 {
		var dc models.DeviceConfig
		if err := m.db.First(&dc, device.DeviceConfigID).Error; err == nil {
			if len(dc.Parser) > 0 && string(dc.Parser) != "{}" && string(dc.Parser) != "null" {
				cp, err := parser.NewConfigParser(dc.Parser)
				if err == nil {
					fields, err := cp.Parse(rawData)
					if err == nil && len(fields) > 0 {
						sensorData = fields
						parseMethod = fmt.Sprintf("ConfigParser(%s)", dc.Name)
					} else if err != nil {
						logger.Debugf("[%s] ConfigParser failed, falling back to driver: %v", deviceID, err)
					}
				}
			}
		}
	}

	// Fallback: try Driver registry (legacy, for HA Discovery compatibility)
	if sensorData == nil {
		driver, err := drivers.Get(device.Type)
		if err != nil {
			logger.Infof("[%s] No ConfigParser and no driver for type %s", deviceID, device.Type)
			return nil
		}

		var drvData []drivers.SensorData
		if caDriver, ok := driver.(drivers.CommandAwareDriver); ok {
			if commandIndex == 0 {
				logger.Infof("[%s] Command-aware driver requires command context", deviceID)
				return nil
			}
			var tmpl models.ConfigTemplate
			if findErr := m.db.First(&tmpl, commandIndex).Error; findErr != nil {
				logger.Infof("[%s] Failed to resolve command template %d: %v", deviceID, commandIndex, findErr)
				return nil
			}
			if tmpl.WriteData == "" {
				logger.Infof("[%s] Command template %d has no write data", deviceID, commandIndex)
				return nil
			}
			drvData, err = caDriver.ParseDataWithCommand(rawData, tmpl.WriteData)
			if err != nil {
				logger.Infof("[%s] Failed to parse command-aware data: %v", deviceID, err)
				return nil
			}
		} else {
			drvData, err = driver.ParseData(rawData)
			if err != nil {
				logger.Infof("[%s] Failed to parse data: %v", deviceID, err)
				return nil
			}
		}
		// Convert drivers.SensorData to parser.Field
		sensorData = make([]parser.Field, len(drvData))
		for i, sd := range drvData {
			sensorData[i] = parser.Field{Name: sd.Name, Value: sd.Value, Unit: sd.Unit, StringValue: sd.StringValue}
		}
		parseMethod = fmt.Sprintf("Driver(%s)", device.Type)
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
			DeviceID:  device.ID,
			NodeID:    deviceID,
			DataJSON:  string(dataJSON),
			Timestamp: time.Now(),
		})
	}

	// Publish to HomeAssistant — convert parser.Field back to drivers.SensorData
	if m.ha != nil {
		haData := make([]drivers.SensorData, len(sensorData))
		for i, f := range sensorData {
			haData[i] = drivers.SensorData{Name: f.Name, Value: f.Value, Unit: f.Unit}
		}
		m.ha.PublishState(deviceID, haData)
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
			"collector_id":   collectorID,      // v2.1 字段名
			"collector_name": device.Node.Name, // v2.2 新增
			"node_id":        collectorID,      // v2.2 新增 (同一值)
			"channel_id":     channelID,
			"data":           dataMap,
			"collected_at":   time.Now().Format(time.RFC3339),
		}
		m.wsHub.BroadcastEvent(events.DataUpdate, payload)
		logger.Debugf("[%s] Broadcast data_update: %v sensors", deviceID, len(sensorData))
	} else if m.wsHub == nil {
		logger.Warnf("[%s] wsHub is nil, skipping data_update broadcast", deviceID)
	}

	logger.Infof("[%s] Parsed %d sensors using %s", deviceID, len(sensorData), parseMethod)

	// Return parsed data map for caller to include in channel_data event
	dataMap := make(map[string]interface{}, len(sensorData))
	for _, sd := range sensorData {
		dataMap[sd.Name] = sd.Value
	}
	return dataMap
}
