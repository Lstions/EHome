package seed

import (
	"encoding/json"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// SeedTestData inserts minimal test data if core tables are empty.
// Called at server startup; safe to run multiple times (idempotent via FirstOrCreate).
func SeedTestData(db *gorm.DB) error {
	// 1. Seed Node (if empty)
	var nodeCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	if nodeCount > 0 {
		logger.Infof("[seed] nodes table already has %d rows, skip seeding", nodeCount)
		return nil
	}

	logger.Infof("[seed] Core tables empty, inserting seed data...")

	// --- Node ---
	node := models.Node{
		NodeID:          1001,
		Name:            "E2E测试节点",
		Model:           "esp32c6",
		FirmwareVersion: "2.2.0",
		ProtocolVersion: "2.2",
		Platform:        "ESP32C6",
		Status:          "offline",
		Capabilities:    `{}`,
		HardwareInfo:    `{}`,
		Config:          `{}`,
		ConnectionType:  "mqtt",
		ConnectionQuality: 100,
	}
	if err := db.Create(&node).Error; err != nil {
		return err
	}
	logger.Infof("[seed] Created node: id=%d, node_id=%d", node.ID, node.NodeID)

	// --- DeviceConfig ---
	connJSON, _ := json.Marshal(map[string]interface{}{
		"bus_type": "I2C",
		"address":  "0x76",
	})
	parserJSON, _ := json.Marshal(map[string]interface{}{
		"data_format": "binary",
		"fields": []map[string]interface{}{
			{"name": "temperature", "offset": 0, "length": 2, "type": "int16", "scale": 0.01},
			{"name": "pressure", "offset": 2, "length": 4, "type": "uint32", "scale": 1},
		},
	})
	initFlowJSON, _ := json.Marshal([]map[string]interface{}{
		{"action": "write", "data": "0xB6", "description": "reset"},
	})

	deviceConfig := models.DeviceConfig{
		Name:         "BMP280 温湿度传感器",
		Description:  "Bosch BMP280 气压温度传感器",
		DeviceType:   "BMP280",
		Protocol:     "I2C",
		HardwareType: "sensor",
		Connection:   json.RawMessage(connJSON),
		Parser:       json.RawMessage(parserJSON),
		InitFlow:     json.RawMessage(initFlowJSON),
		IsDefault:    true,
		Status:       "active",
	}
	if err := db.Create(&deviceConfig).Error; err != nil {
		return err
	}
	logger.Infof("[seed] Created device_config: id=%d, device_type=%s", deviceConfig.ID, deviceConfig.DeviceType)

	// --- Channel (I2C) ---
	channel := models.Channel{
		NodeID:       node.ID,
		HardwareType: "I2C",
		HardwareID:   "0x68",
		BusType:      "I2C",
		BusConfig:    `{"sda_pin":21,"scl_pin":22,"clock_hz":400000}`,
		IntervalMs:   1000,
		Enabled:      true,
	}
	if err := db.Create(&channel).Error; err != nil {
		return err
	}
	logger.Infof("[seed] Created channel: id=%d, bus_type=%s", channel.ID, channel.BusType)

	// --- EdgeDevice ---
	edgeDevice := models.EdgeDevice{
		Name:           "BMP280 现场 A",
		Type:           "sensor",
		NodeID:         node.ID,
		ChannelID:      channel.ID,
		DeviceConfigID: deviceConfig.ID,
		HardwareID:     "0x68",
		IntervalMs:     1000,
		Enabled:        true,
		Status:         "active",
		InitState:      "pending",
	}
	if err := db.Create(&edgeDevice).Error; err != nil {
		return err
	}
	logger.Infof("[seed] Created edge_device: id=%d, name=%s", edgeDevice.ID, edgeDevice.Name)

	logger.Infof("[seed] Seed data complete: 1 node + 1 device_config + 1 channel + 1 edge_device")
	return nil
}
