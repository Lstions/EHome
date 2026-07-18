package seed

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	logger.Init("warn")
}

func setupSeedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.AutoMigrate(
		&models.Node{},
		&models.Channel{},
		&models.ConfigTemplate{},
		&models.EdgeDevice{},
		&models.DeviceConfig{},
		&models.DeviceData{},
		&models.UnifiedData{},
		&models.OTATask{},
		&models.Firmware{},
		&models.Notification{},
		&models.User{},
		&models.OperationLog{},
		&models.Vendor{},
		&models.DeviceModel{},
		&models.NodeEvent{},
		&models.CalibrationCache{},
	)
	return db
}

func TestSeedTestData_EmptyDB(t *testing.T) {
	db := setupSeedDB(t)

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	// Development seed must only add DeviceConfig. Hardware resources are
	// authoritative ResourceReport data and must never be fabricated server-side.
	var nodeCount, channelCount, edgeDeviceCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	db.Model(&models.Channel{}).Count(&channelCount)
	db.Model(&models.EdgeDevice{}).Count(&edgeDeviceCount)
	if nodeCount != 0 || channelCount != 0 || edgeDeviceCount != 0 {
		t.Fatalf("seed fabricated hardware state: nodes=%d channels=%d devices=%d", nodeCount, channelCount, edgeDeviceCount)
	}

	var config models.DeviceConfig
	if err := db.Where("parser_id = ? AND device_type = ? AND hardware_type = ? AND status = ?", "bosch.bmp280", "bmp280", "i2c", "active").First(&config).Error; err != nil {
		t.Fatalf("expected canonical device config: %v", err)
	}
}

func TestSeedTestData_Idempotent(t *testing.T) {
	db := setupSeedDB(t)

	// Seed twice
	if err := SeedTestData(db); err != nil {
		t.Fatalf("first SeedTestData failed: %v", err)
	}
	if err := SeedTestData(db); err != nil {
		t.Fatalf("second SeedTestData failed: %v", err)
	}

	// No node is added, so a second seed remains hardware-resource neutral.
	var nodeCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	if nodeCount != 0 {
		t.Errorf("expected no seeded nodes after double seed, got %d", nodeCount)
	}
}

func TestSeedTestData_AddsDeviceConfigWhenNodesAlreadyExist(t *testing.T) {
	db := setupSeedDB(t)

	// Pre-create a node
	db.Create(&models.Node{NodeID: "EXISTING", Name: "Existing Node"})

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	// It must preserve the existing node instead of replacing it.
	var nodeCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	if nodeCount != 1 {
		t.Errorf("expected 1 node (pre-existing), got %d", nodeCount)
	}

	var node models.Node
	db.First(&node)
	if node.NodeID != "EXISTING" {
		t.Errorf("expected EXISTING node_id, got %s", node.NodeID)
	}

	var config models.DeviceConfig
	if err := db.Where("parser_id = ? AND device_type = ? AND hardware_type = ? AND status = ?", "bosch.bmp280", "bmp280", "i2c", "active").First(&config).Error; err != nil {
		t.Fatalf("expected the default device config to be added: %v", err)
	}
	if config.HardwareType != "i2c" {
		t.Errorf("expected i2c hardware type, got %q", config.HardwareType)
	}
	var flow []struct {
		Name     string `json:"name"`
		Write    string `json:"write"`
		ReadSize uint32 `json:"read_size"`
	}
	if err := json.Unmarshal(config.InitFlow, &flow); err != nil || len(flow) != 5 || flow[0].Write != "E0B6" || flow[2].Name != "read_calib" || flow[2].ReadSize != 24 {
		t.Fatalf("seed init flow is not executable BMP280 flow: %s, err=%v", config.InitFlow, err)
	}
}

func TestSeedTestData_RepairsInactiveOrIncompatibleBMP280Config(t *testing.T) {
	for _, existing := range []models.DeviceConfig{
		{ParserID: "bosch.bmp280", DeviceType: "bmp280", HardwareType: "i2c", Status: "inactive"},
		{ParserID: "bosch.bmp280", DeviceType: "bmp280", HardwareType: "uart", Status: "active"},
	} {
		db := setupSeedDB(t)
		if err := db.Create(&existing).Error; err != nil {
			t.Fatal(err)
		}
		if err := SeedTestData(db); err != nil {
			t.Fatalf("SeedTestData failed: %v", err)
		}
		var config models.DeviceConfig
		if err := db.Where("parser_id = ? AND device_type = ? AND hardware_type = ? AND status = ?", "bosch.bmp280", "bmp280", "i2c", "active").First(&config).Error; err != nil {
			t.Fatalf("expected active I2C canonical config: %v", err)
		}
	}
}

func TestSeedTestData_PreservesOtherDeviceTypeDefault(t *testing.T) {
	db := setupSeedDB(t)
	other := models.DeviceConfig{ParserID: "other.default", DeviceType: "other", HardwareType: "uart", IsDefault: true, Status: "active"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeedTestData(db); err != nil {
		t.Fatal(err)
	}
	var got models.DeviceConfig
	if err := db.First(&got, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.IsDefault {
		t.Fatal("seeding bmp280 must not clear another device_type's default")
	}
}

func TestSeedTestData_LeavesSingleDefaultForBMP280(t *testing.T) {
	db := setupSeedDB(t)
	oldDefault := models.DeviceConfig{
		ParserID: "old.bmp280", DeviceType: "bmp280", HardwareType: "i2c",
		IsDefault: true, Status: "active",
	}
	if err := db.Create(&oldDefault).Error; err != nil {
		t.Fatal(err)
	}

	if err := SeedTestData(db); err != nil {
		t.Fatal(err)
	}

	var defaults []models.DeviceConfig
	if err := db.Where("device_type = ? AND is_default = ?", "bmp280", true).Find(&defaults).Error; err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 1 {
		t.Fatalf("bmp280 defaults = %d, want 1", len(defaults))
	}
	if defaults[0].ParserID != "bosch.bmp280" {
		t.Fatalf("default parser_id = %q, want bosch.bmp280", defaults[0].ParserID)
	}
	var old models.DeviceConfig
	if err := db.First(&old, oldDefault.ID).Error; err != nil {
		t.Fatal(err)
	}
	if old.IsDefault {
		t.Fatal("previous bmp280 default was not cleared")
	}
}

func TestSeedTestData_DeviceConfigFields(t *testing.T) {
	db := setupSeedDB(t)

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	var dc models.DeviceConfig
	db.First(&dc)
	if dc.DeviceType != "bmp280" {
		t.Errorf("expected device_type bmp280, got %s", dc.DeviceType)
	}
	if dc.Name != "BMP280 温湿度传感器" {
		t.Errorf("expected name 'BMP280 温湿度传感器', got %s", dc.Name)
	}
	if !dc.IsDefault {
		t.Error("expected is_default=true")
	}
	if dc.Status != "active" {
		t.Errorf("expected status active, got %s", dc.Status)
	}
	if dc.HardwareType != "i2c" {
		t.Errorf("expected hardware_type i2c, got %s", dc.HardwareType)
	}
}
