package collector

import (
	"strconv"
	_ "ehome/backend/pkg/logger"
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
		NodeID:          3001,
		Model:           "ESP32S3",
		FirmwareVersion: "1.0.0",
		Status:          "online",
	}
	db.Create(&col)

	ch := models.Channel{
		NodeID: col.ID,
		HardwareID:  1,
		IntervalMs:  5000,
		Enabled:     true,
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

	mgr.parseAndStoreData(col.ID, strconv.FormatInt(col.NodeID, 10), uint64(ch.ID), rawData)

	// Verify unified_data was written
	var unified []models.UnifiedData
	db.Where("device_id = ?", dev.ID).Find(&unified)
	if len(unified) != 2 {
		// BMP280 produces 2 sensors (temp + pressure)
		t.Errorf("expected 2 unified_data rows, got %d", len(unified))
	}

	// Verify device_data was written (F4.1 R9)
	var deviceData []models.DeviceData
	db.Where("device_id = ?", dev.ID).Find(&deviceData)
	if len(deviceData) == 0 {
		t.Error("expected device_data row, got 0")
	}
	if len(deviceData) > 0 && deviceData[0].DataJSON == "" {
		t.Error("expected device_data.data_json to be populated")
	}
}

// TestDataPipeline_UnknownDevice: raw data for a channel with no Device
// should silently skip without crashing.
func TestDataPipeline_UnknownDevice(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{})

	col := models.Node{NodeID: 3002, Status: "online"}
	db.Create(&col)
	ch := models.Channel{NodeID: col.ID, HardwareID: 1, IntervalMs: 5000, Enabled: true}
	db.Create(&ch)
	// No device created

	mgr := &Manager{db: db}
	mgr.parseAndStoreData(col.ID, "3002", uint64(ch.ID), []byte{1, 2, 3, 4, 5, 6})

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

	col := models.Node{NodeID: 3003, Status: "online"}
	db.Create(&col)
	ch := models.Channel{NodeID: col.ID, HardwareID: 1, IntervalMs: 5000, Enabled: true}
	db.Create(&ch)
	dev := models.EdgeDevice{Name: "sn3000", ChannelID: ch.ID, Type: "sn3000"}
	db.Create(&dev)

	mgr := &Manager{db: db}
	mgr.parseAndStoreData(col.ID, "3003", uint64(ch.ID), []byte{})

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

// TestBMP280Driver_Parse: ensure BMP280 driver works
func TestBMP280Driver_Parse(t *testing.T) {
	d := &drivers.BMP280Driver{}
	data, err := d.ParseData([]byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32})
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 sensors, got %d", len(data))
	}
	sensorNames := map[string]bool{}
	for _, s := range data {
		sensorNames[s.Name] = true
	}
	for _, expected := range []string{"temperature", "pressure"} {
		if !sensorNames[expected] {
			t.Errorf("expected sensor %s not in output", expected)
		}
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
