package seed

import (
	"encoding/json"
	"fmt"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

const developmentSeedLockID int64 = 73522901

// SeedTestData inserts development sample data without overwriting existing rows.
// Called at server startup when SEED_TEST_DATA=true; safe to run repeatedly.
func SeedTestData(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// PostgreSQL advisory locks serialize concurrent development-server starts.
		// SQLite is used by unit tests and already serializes this in-memory connection.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", developmentSeedLockID).Error; err != nil {
				return err
			}
		}

		deviceConfig, createdConfig, err := ensureDefaultDeviceConfig(tx)
		if err != nil {
			return err
		}
		if createdConfig {
			logger.Infof("[seed] added default device_config id=%d", deviceConfig.ID)
		}
		// Hardware resources are authoritative only after a real ESP32 ResourceReport.
		// Do not fabricate a node, transport channel, pins, or EdgeDevice in seed data.
		return nil
	})
}

func ensureDefaultDeviceConfig(db *gorm.DB) (*models.DeviceConfig, bool, error) {
	connection, err := json.Marshal(map[string]interface{}{
		"protocol": "i2c",
		"default_params": map[string]interface{}{
			"address": "0x76", "read_register": "F7",
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("marshal BMP280 connection: %w", err)
	}
	// The runtime owns BMP280's 20-bit 6-byte decoding; keep Parser empty so
	// SensorParserConsumer correctly falls back to the registered driver.
	parser := json.RawMessage(`{}`)
	initFlow, err := json.Marshal([]map[string]interface{}{
		{"name": "reset", "write": "E0B6", "read_size": 0},
		{"name": "read_chip_id", "write": "D0", "read_size": 1},
		{"name": "read_calib", "write": "88", "read_size": 24},
		{"name": "set_ctrl", "write": "F427", "read_size": 0},
		{"name": "set_config", "write": "F5A0", "read_size": 0},
	})
	if err != nil {
		return nil, false, fmt.Errorf("marshal BMP280 init flow: %w", err)
	}

	config := &models.DeviceConfig{
		Name:         "BMP280 温湿度传感器",
		Description:  "Bosch BMP280 气压温度传感器",
		DeviceType:   "bmp280",
		Protocol:     "i2c",
		HardwareType: "i2c",
		ParserID:     "bosch.bmp280",
		Connection:   json.RawMessage(connection),
		Parser:       parser,
		InitFlow:     json.RawMessage(initFlow),
		IsDefault:    true,
		Status:       "active",
	}
	var existing models.DeviceConfig
	if err := db.Where("parser_id = ?", config.ParserID).First(&existing).Error; err == nil {
		if err := db.Model(&models.DeviceConfig{}).Where("is_default = ? AND device_type = ? AND id <> ?", true, config.DeviceType, existing.ID).Update("is_default", false).Error; err != nil {
			return nil, false, err
		}
		updates := map[string]interface{}{
			"name": config.Name, "description": config.Description,
			"device_type": config.DeviceType, "protocol": config.Protocol,
			"hardware_type": config.HardwareType, "connection": config.Connection,
			"parser": config.Parser, "init_flow": config.InitFlow,
			"is_default": true, "status": "active",
		}
		if err := db.Model(&existing).Updates(updates).Error; err != nil {
			return nil, false, err
		}
		if err := db.First(&existing, existing.ID).Error; err != nil {
			return nil, false, err
		}
		return &existing, false, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, false, err
	}
	if err := db.Model(&models.DeviceConfig{}).Where("is_default = ? AND device_type = ?", true, config.DeviceType).Update("is_default", false).Error; err != nil {
		return nil, false, err
	}
	if err := db.Create(config).Error; err != nil {
		return nil, false, err
	}
	logger.Infof("[seed] created default device_config: id=%d device_type=%s", config.ID, config.DeviceType)
	return config, true, nil
}
