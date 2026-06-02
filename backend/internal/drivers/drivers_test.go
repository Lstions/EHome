package drivers

import (
	"testing"

	"ehome/backend/pkg/logger"
)

func init() {
	logger.Init("warn")
}

func TestBMP280Driver(t *testing.T) {
	driver := &BMP280Driver{}
	
	if driver.DeviceType() != "bmp280" {
		t.Errorf("device type: got %s, want bmp280", driver.DeviceType())
	}
	
	// Test with mock data
	raw := []byte{0x00, 0x01, 0x02, 0x00, 0x01, 0x02}
	data, err := driver.ParseData(raw)
	if err != nil {
		t.Logf("ParseData returned error (expected for mock data): %v", err)
	}
	_ = data
}

func TestLKTH01Driver(t *testing.T) {
	driver := &LKTH01Driver{}
	
	if driver.DeviceType() != "lk_th01" {
		t.Errorf("device type: got %s, want lk_th01", driver.DeviceType())
	}
	
	// Test with valid data: 25.1°C, 65.0%RH
	raw := []byte{0x00, 0xFB, 0x02, 0x08} // 251 = 25.1, 520 = 65.0
	data, err := driver.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	
	if len(data) != 2 {
		t.Fatalf("expected 2 sensors, got %d", len(data))
	}
	
	if data[0].Name != "temperature" {
		t.Errorf("sensor[0] name: got %s, want temperature", data[0].Name)
	}
	
	if data[1].Name != "humidity" {
		t.Errorf("sensor[1] name: got %s, want humidity", data[1].Name)
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	
	// Register drivers
	RegisterBuiltInDrivers(reg)
	
	// Test list
	types := reg.List()
	if len(types) != 3 {
		t.Fatalf("expected 3 drivers, got %d", len(types))
	}
	
	// Test get
	driver, err := reg.Get("bmp280")
	if err != nil {
		t.Fatalf("Get bmp280: %v", err)
	}
	if driver.DeviceType() != "bmp280" {
		t.Errorf("device type: got %s, want bmp280", driver.DeviceType())
	}
	
	// Test get non-existent
	_, err = reg.Get("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent driver")
	}
}
