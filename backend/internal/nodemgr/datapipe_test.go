package nodemgr

import (
	_ "ehome/backend/pkg/logger"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("warn")
	// Register built-in drivers in the global registry
	drivers.Register(&drivers.BMP280Driver{})
	drivers.Register(&drivers.LKTH01Driver{})
	drivers.Register(&drivers.SN3000Driver{})
}

// TestDataPipeline_EndToEnd: simulate a DataReport coming in, going through
// the worker pool, and being stored as UnifiedData + DeviceData.
func TestDataPipeline_EndToEnd(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{})

	// Set up a channel + device
	col := models.Node{
		NodeID:          "3001",
		Model:           "ESP32S3",
		FirmwareVersion: "1.0.0",
		Status:          "online",
	}
	db.Create(&col)

	ch := models.Channel{
		NodeID:     fmt.Sprintf("%d", col.ID),
		HardwareID: "1",
		IntervalMs: 5000,
		Enabled:    true,
	}
	db.Create(&ch)

	dev := models.EdgeDevice{
		Name:      "BMP280-Test",
		ChannelID: ch.ID,
		Type:      "bmp280",
	}
	db.Create(&dev)

	// Manually call parseAndStoreData (without going through MQTT)
	// BMP280 raw data: 6 bytes [temp_xl, temp_l, temp_h, press_xl, press_l, press_h]
	rawData := []byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32}

	// Build a minimal manager
	mgr := &Manager{db: db}

	mgr.parseAndStoreData(col.ID, col.NodeID, uint64(ch.ID), 0, 0, rawData)

	// BMP280 samples without a calibration record must fail closed: no plausible
	// but physically invalid readings may be persisted.
	var unified []models.UnifiedData
	db.Where("device_id = ?", dev.ID).Find(&unified)
	if len(unified) != 0 {
		t.Errorf("uncalibrated BMP280 wrote %d unified_data rows", len(unified))
	}

	// The parsed-device history must likewise remain empty.
	var deviceData []models.DeviceData
	db.Where("device_id = ?", dev.ID).Find(&deviceData)
	if len(deviceData) != 0 {
		t.Errorf("uncalibrated BMP280 wrote %d device_data rows", len(deviceData))
	}
}

// TestDataPipeline_UnknownDevice: raw data for a channel with no Device
// should silently skip without crashing.
func TestDataPipeline_UnknownDevice(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{})

	col := models.Node{NodeID: "3002", Status: "online"}
	db.Create(&col)
	ch := models.Channel{NodeID: fmt.Sprintf("%d", col.ID), HardwareID: "1", IntervalMs: 5000, Enabled: true}
	db.Create(&ch)
	// No device created

	mgr := &Manager{db: db}
	mgr.parseAndStoreData(col.ID, "3002", uint64(ch.ID), 0, 0, []byte{1, 2, 3, 4, 5, 6})

	var count int64
	db.Model(&models.UnifiedData{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 unified_data for unknown device, got %d", count)
	}
}

// TestDataPipeline_EmptyRaw: empty raw data should be skipped gracefully
func TestDataPipeline_EmptyRaw(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{})

	col := models.Node{NodeID: "3003", Status: "online"}
	db.Create(&col)
	ch := models.Channel{NodeID: fmt.Sprintf("%d", col.ID), HardwareID: "1", IntervalMs: 5000, Enabled: true}
	db.Create(&ch)
	dev := models.EdgeDevice{Name: "sn3000", ChannelID: ch.ID, Type: "sn3000"}
	db.Create(&dev)

	mgr := &Manager{db: db}
	mgr.parseAndStoreData(col.ID, "3003", uint64(ch.ID), 0, 0, []byte{})

	var count int64
	db.Model(&models.UnifiedData{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 unified_data for empty raw, got %d", count)
	}
}

// TestDriverRegistry: ensure built-in drivers are registered
func TestDriverRegistry(t *testing.T) {
	types := drivers.List()
	expected := map[string]bool{
		"bmp280":  false,
		"lk_th01": false,
		"sn3000":  false,
	}
	for _, t := range types {
		if _, ok := expected[t]; ok {
			expected[t] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("driver %s not registered", name)
		}
	}
}

// TestBMP280Driver_Parse: uncalibrated samples must be rejected.
func TestBMP280Driver_Parse(t *testing.T) {
	d := &drivers.BMP280Driver{}
	if _, err := d.ParseData([]byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32}); err == nil {
		t.Fatal("uncalibrated BMP280 sample must fail closed")
	}
}

// TestBMP280Driver_GetSensorDefinitions: for HA Discovery (R8 T6)
func TestBMP280Driver_GetSensorDefinitions(t *testing.T) {
	d := &drivers.BMP280Driver{}
	sensors := d.GetSensorDefinitions()
	if len(sensors) != 2 {
		t.Errorf("expected 2 sensor defs, got %d", len(sensors))
	}
}

// TestBMP280Driver_TooShort: malformed data should error not panic
func TestBMP280Driver_TooShort(t *testing.T) {
	d := &drivers.BMP280Driver{}
	_, err := d.ParseData([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short data")
	}
}

// TestBackpressure_Fallback: when worker pool is full, processDataReportJob
// is called synchronously (F4.5 from R8)
func TestBackpressure_Fallback(t *testing.T) {
	// Simulate by setting dataCh to nil and calling processDataReportJob
	// directly - should not panic
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	mgr := &Manager{db: db}

	job := dataReportJob{
		deviceID:  "test",
		channelID: 1,
		rawData:   []byte{},
	}
	// Should not panic even with no channel/device
	mgr.processDataReportJob(job)
	// Give it a moment in case of async
	time.Sleep(50 * time.Millisecond)
}

func TestDriverRegistry_Global(t *testing.T) {
	// The init() in this file registers them
	types := drivers.List()
	t.Logf("registered: %v", types)
}

// TestFindEdgeDeviceByChannelID_C6IndexFallback: when C6 sends channel_id=0 (its
// internal config_mgr index) instead of the DB channels.id, the fallback path
// should resolve the correct edge_device by looking up the node's channels
// ordered by id and treating the C6 channel_id as a 0-based index.
func TestFindEdgeDeviceByChannelID_C6IndexFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{})

	// Create a node
	node := models.Node{
		NodeID:          "AABBCCDDEE01",
		Model:           "ESP32C6",
		FirmwareVersion: "1.0.0",
		Status:          "online",
	}
	db.Create(&node)

	// Create two channels with explicit DB IDs far from 0/1 so C6 legacy
	// 0-based indexes cannot collide with real channels.id values.
	ch1 := models.Channel{
		ID:         100,
		NodeID:     node.NodeID,
		HardwareID: "1",
		BusType:    "I2C",
		IntervalMs: 5000,
		Enabled:    true,
	}
	db.Create(&ch1)

	ch2 := models.Channel{
		ID:         101,
		NodeID:     node.NodeID,
		HardwareID: "0x76",
		BusType:    "I2C",
		IntervalMs: 1000,
		Enabled:    true,
	}
	db.Create(&ch2)

	// Create edge devices on both channels
	ed1 := models.EdgeDevice{
		Name:      "BMP280-Ch1",
		NodeID:    node.NodeID,
		ChannelID: ch1.ID,
		Type:      "bmp280",
	}
	db.Create(&ed1)

	ed2 := models.EdgeDevice{
		Name:      "LK_TH01-Ch2",
		NodeID:    node.NodeID,
		ChannelID: ch2.ID,
		Type:      "lk_th01",
	}
	db.Create(&ed2)

	mgr := &Manager{db: db}

	// Test 1: Direct lookup by channels.id should work (new firmware path)
	device, found := mgr.findEdgeDeviceByChannelID(node.NodeID, uint64(ch1.ID), 0)
	if !found {
		t.Error("expected to find edge_device by direct channel_id lookup")
	}
	if device.ID != ed1.ID {
		t.Errorf("expected device ID %d, got %d", ed1.ID, device.ID)
	}

	// Test 2: C6 index 0 should resolve to the first channel (ch1)
	// This simulates C6 sending channel_id=0 when ch1.ID is actually a larger number
	device, found = mgr.findEdgeDeviceByChannelID(node.NodeID, 0, 0)
	if !found {
		t.Error("expected to find edge_device by C6 index 0 fallback")
	}
	if device.ID != ed1.ID {
		t.Errorf("expected device ID %d (ch1), got %d", ed1.ID, device.ID)
	}

	// Test 3: C6 index 1 should resolve to the second channel (ch2)
	device, found = mgr.findEdgeDeviceByChannelID(node.NodeID, 1, 0)
	if !found {
		t.Error("expected to find edge_device by C6 index 1 fallback")
	}
	if device.ID != ed2.ID {
		t.Errorf("expected device ID %d (ch2), got %d", ed2.ID, device.ID)
	}

	// Test 4: edge_device_id > 0 should use direct primary key lookup
	device, found = mgr.findEdgeDeviceByChannelID(node.NodeID, 0, uint64(ed2.ID))
	if !found {
		t.Error("expected to find edge_device by edge_device_id")
	}
	if device.ID != ed2.ID {
		t.Errorf("expected device ID %d, got %d", ed2.ID, device.ID)
	}

	// Test 5: Out-of-range index should return false
	_, found = mgr.findEdgeDeviceByChannelID(node.NodeID, 99, 0)
	if found {
		t.Error("expected NOT to find edge_device for out-of-range index 99")
	}
}
