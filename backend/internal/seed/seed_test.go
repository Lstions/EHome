package seed

import (
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

	// Verify node was created
	var nodeCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	if nodeCount != 1 {
		t.Errorf("expected 1 node, got %d", nodeCount)
	}

	// Verify node fields
	var node models.Node
	db.First(&node)
	if node.NodeID != "F0F5BD02F35C" {
		t.Errorf("expected node_id F0F5BD02F35C, got %s", node.NodeID)
	}
	if node.Model != "esp32c6" {
		t.Errorf("expected model esp32c6, got %s", node.Model)
	}
	if node.Platform != "ESP32C6" {
		t.Errorf("expected platform ESP32C6, got %s", node.Platform)
	}

	// Verify device_config was created
	var dcCount int64
	db.Model(&models.DeviceConfig{}).Count(&dcCount)
	if dcCount != 1 {
		t.Errorf("expected 1 device_config, got %d", dcCount)
	}

	// Verify channel was created
	var chCount int64
	db.Model(&models.Channel{}).Count(&chCount)
	if chCount != 1 {
		t.Errorf("expected 1 channel, got %d", chCount)
	}

	// Verify edge_device was created
	var edCount int64
	db.Model(&models.EdgeDevice{}).Count(&edCount)
	if edCount != 1 {
		t.Errorf("expected 1 edge_device, got %d", edCount)
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

	// Should still have only 1 node (idempotent)
	var nodeCount int64
	db.Model(&models.Node{}).Count(&nodeCount)
	if nodeCount != 1 {
		t.Errorf("expected 1 node after double seed, got %d", nodeCount)
	}
}

func TestSeedTestData_SkipsWhenNodesExist(t *testing.T) {
	db := setupSeedDB(t)

	// Pre-create a node
	db.Create(&models.Node{NodeID: "EXISTING", Name: "Existing Node"})

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	// Should still have only the pre-existing node
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
}

func TestSeedTestData_EdgeDeviceFields(t *testing.T) {
	db := setupSeedDB(t)

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	var ed models.EdgeDevice
	db.First(&ed)
	if ed.Name != "BMP280 现场 A" {
		t.Errorf("expected edge device name 'BMP280 现场 A', got %s", ed.Name)
	}
	if ed.Status != "active" {
		t.Errorf("expected active status, got %s", ed.Status)
	}
	if ed.InitState != "pending" {
		t.Errorf("expected pending init_state, got %s", ed.InitState)
	}
	if !ed.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestSeedTestData_DeviceConfigFields(t *testing.T) {
	db := setupSeedDB(t)

	if err := SeedTestData(db); err != nil {
		t.Fatalf("SeedTestData failed: %v", err)
	}

	var dc models.DeviceConfig
	db.First(&dc)
	if dc.DeviceType != "BMP280" {
		t.Errorf("expected device_type BMP280, got %s", dc.DeviceType)
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
}
