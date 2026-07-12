package databus

import (
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/deviceinit"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/models"
	"ehome/backend/internal/pendingwrite"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// Reassembler is the interface for frame reassembly (implemented by nodemgr.streamReassembler).
type Reassembler interface {
	Append(requestID uint32, data []byte) []byte
	Consume(requestID uint32)
}

// PendingWriteConsumer routes command responses to the pendingWrite manager
// for WriteCmd acknowledgment and device initialization tracking.
type PendingWriteConsumer struct {
	pendingWrite *pendingwrite.Manager
	deviceInit   *deviceinit.Orchestrator
	db           *gorm.DB
}

func NewPendingWriteConsumer(pw *pendingwrite.Manager, di *deviceinit.Orchestrator, db *gorm.DB) *PendingWriteConsumer {
	return &PendingWriteConsumer{pendingWrite: pw, deviceInit: di, db: db}
}

func (c *PendingWriteConsumer) Name() string { return "pending_write" }
func (c *PendingWriteConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.RequestID != 0
}
func (c *PendingWriteConsumer) Handle(evt DataEvent) {
	// RX timeout
	if evt.ErrorCode == 0x01 && c.pendingWrite != nil {
		c.pendingWrite.HandleResponse(uint32(evt.RequestID), false, 0x01, "sensor RX timeout")
		return
	}
	// Normal command response
	if c.pendingWrite != nil {
		c.pendingWrite.HandleDataReportAck(uint32(evt.RequestID), evt.RawData)
	}
	// Device init notification
	if c.deviceInit != nil && evt.EdgeDeviceID > 0 {
		var device models.EdgeDevice
		if err := c.db.Where("id = ?", evt.EdgeDeviceID).First(&device).Error; err == nil {
			if c.deviceInit.HasActiveInit(device.Type) {
				logger.Infof("[%s] DataReport ack for device init, type=%s", evt.DeviceID, device.Type)
			}
		}
	}
}

// DBPersistConsumer writes raw data to device_data table for audit/history.
// Only handles command responses (passive data is not persisted).
type DBPersistConsumer struct {
	db *gorm.DB
}

func NewDBPersistConsumer(db *gorm.DB) *DBPersistConsumer {
	return &DBPersistConsumer{db: db}
}

func (c *DBPersistConsumer) Name() string { return "db_persist" }
func (c *DBPersistConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.RequestID != 0
}
func (c *DBPersistConsumer) Handle(evt DataEvent) {
	dataJSON, _ := json.Marshal(map[string]interface{}{
		"raw":            fmt.Sprintf("%x", evt.RawData),
		"channel":        evt.ChannelID,
		"sequence":       evt.Sequence,
		"error_code":     evt.ErrorCode,
		"request_id":     evt.RequestID,
		"edge_device_id": evt.EdgeDeviceID,
		"command_index":  evt.CommandIndex,
	})
	c.db.Session(&gorm.Session{}).Create(&models.DeviceData{
		NodeID:    evt.DeviceID,
		DataJSON:  string(dataJSON),
		Timestamp: evt.ReceivedAt,
	})
}

// SensorParserConsumer parses sensor data, stores to unified_data,
// updates edge_device status, publishes to HomeAssistant, and broadcasts
// data_update WebSocket events. This is the heaviest consumer.
// Only handles command responses with valid data (not passive, not error).
type SensorParserConsumer struct {
	db          *gorm.DB
	wsHub       *websocket.Hub
	ha          *homeassistant.Integration
	reassembler Reassembler
}

func NewSensorParserConsumer(db *gorm.DB, wsHub *websocket.Hub, ha *homeassistant.Integration, reassembler Reassembler) *SensorParserConsumer {
	return &SensorParserConsumer{db: db, wsHub: wsHub, ha: ha, reassembler: reassembler}
}

func (c *SensorParserConsumer) Name() string { return "sensor_parser" }
func (c *SensorParserConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.RequestID != 0 && evt.ErrorCode == 0 && len(evt.RawData) > 0
}
func (c *SensorParserConsumer) Handle(evt DataEvent) {
	// Frame reassembly for multi-frame protocols
	merged := c.reassembler.Append(uint32(evt.RequestID), evt.RawData)

	// Find edge device
	var channels []models.Channel
	if err := c.db.Where("node_id = ?", evt.DeviceID).Order("id ASC").Find(&channels).Error; err != nil {
		return
	}
	idx := int(evt.ChannelID)
	if idx < 0 || idx >= len(channels) {
		return
	}
	realChannelID := channels[idx].ID

	var device models.EdgeDevice
	if err := c.db.Preload("Node").Where("channel_id = ?", realChannelID).First(&device).Error; err != nil {
		return
	}

	// Parse sensor data
	var sensorData []parser.Field
	var parseMethod string

	// Primary: DeviceConfig.Parser
	if device.DeviceConfigID > 0 {
		var dc models.DeviceConfig
		if err := c.db.First(&dc, device.DeviceConfigID).Error; err == nil {
			if len(dc.Parser) > 0 && string(dc.Parser) != "{}" && string(dc.Parser) != "null" {
				cp, err := parser.NewConfigParser(dc.Parser)
				if err == nil {
					fields, err := cp.Parse(merged)
					if err == nil && len(fields) > 0 {
						sensorData = fields
						parseMethod = fmt.Sprintf("ConfigParser(%s)", dc.Name)
					}
				}
			}
		}
	}

	// Fallback: Driver registry
	if sensorData == nil {
		drv, err := drivers.Get(device.Type)
		if err != nil {
			c.reassembler.Consume(uint32(evt.RequestID))
			return
		}
		drvData, err := drv.ParseData(merged)
		if err != nil {
			logger.Infof("[%s] Failed to parse data: %v", evt.DeviceID, err)
			return
		}
		sensorData = make([]parser.Field, len(drvData))
		for i, sd := range drvData {
			sensorData[i] = parser.Field{Name: sd.Name, Value: sd.Value, Unit: sd.Unit, StringValue: sd.StringValue}
		}
		parseMethod = fmt.Sprintf("Driver(%s)", device.Type)
	}

	// Parse succeeded — consume reassembly buffer
	c.reassembler.Consume(uint32(evt.RequestID))

	// Store parsed data
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
		c.db.Create(&records)
	}

	// Update edge device status
	result := c.db.Model(&device).Where("status = ?", "offline").Updates(map[string]interface{}{
		"last_data_at": now,
		"status":       "active",
	})
	if result.RowsAffected > 0 && c.wsHub != nil {
		c.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": device.ID,
			"device_id":      device.ID,
			"device_name":    device.Name,
			"node_id":        device.NodeID,
			"channel_id":     device.ChannelID,
			"status":         "active",
			"reason":         "data_received",
		})
	}

	// Store raw data for this edge device
	dataJSON, _ := json.Marshal(map[string]interface{}{
		"raw_hex":    fmt.Sprintf("%x", merged),
		"sensors":    sensorData,
		"channel_id": evt.ChannelID,
		"timestamp":  now.UnixMilli(),
	})
	c.db.Create(&models.DeviceData{
		DeviceID:  device.ID,
		NodeID:    evt.DeviceID,
		DataJSON:  string(dataJSON),
		Timestamp: now,
	})

	// HomeAssistant publish
	if c.ha != nil {
		haData := make([]drivers.SensorData, len(sensorData))
		for i, f := range sensorData {
			haData[i] = drivers.SensorData{Name: f.Name, Value: f.Value, Unit: f.Unit}
		}
		c.ha.PublishState(evt.DeviceID, haData)
	}

	// Broadcast data_update event
	if c.wsHub != nil && len(sensorData) > 0 {
		dataMap := make(map[string]interface{}, len(sensorData))
		for _, sd := range sensorData {
			dataMap[sd.Name] = sd.Value
		}
		c.wsHub.BroadcastEvent(events.DataUpdate, map[string]interface{}{
			"device_id":      device.ID,
			"edge_device_id": device.ID,
			"device_name":    device.Name,
			"collector_id":   device.NodeID,
			"node_id":        device.NodeID,
			"channel_id":     evt.ChannelID,
			"data":           dataMap,
			"collected_at":   now.Format(time.RFC3339),
		})
	}

	logger.Infof("[%s] Parsed %d sensors using %s", evt.DeviceID, len(sensorData), parseMethod)
}
