package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("error")
}

// setupEdgeDeviceTest creates a test router with DB and edge-device routes.
func setupEdgeDeviceTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.DeviceData{},
		&models.UnifiedData{}, &models.User{}, &models.OTATask{},
		&models.Firmware{}, &models.Vendor{}, &models.Notification{},
		&models.OperationLog{}, &models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{},
		&models.ConfigMeta{}, &models.PendingWriteRecord{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	// Register drivers so createTemplatesFromDriver works in tests
	drivers.RegisterBuiltInDrivers(drivers.GlobalRegistry())
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	registerEdgeDeviceRoutes(v1, db, mgr)
	return r, db
}

// ==================== EdgeDevice Create Tests ====================

func TestEdgeDevice_Create_Success(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":             "New Device",
		"type":             "temperature",
		"node_id":          "NODE001",
		"channel_id":       1,
		"device_config_id": 1,
		"hardware_id":      "0x77",
		"enabled":          true,
		"interval_ms":      3000,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "New Device")
	if dev.Name != "New Device" {
		t.Errorf("expected name 'New Device', got %s", dev.Name)
	}
	if dev.HardwareID != "0x77" {
		t.Errorf("expected hardware_id '0x77', got %s", dev.HardwareID)
	}
}

func TestEdgeDevice_Create_MissingRequiredFields(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Incomplete Device",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Create_InvalidJSON(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestEdgeDevice_Create_WithModbusType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "SN3000", DeviceType: "sn3000", HardwareType: "uart"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":             "Wind Sensor",
		"type":             "sn3000",
		"node_id":          "NODE001",
		"channel_id":       1,
		"device_config_id": 1,
		"hardware_id":      "3",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Check that a ConfigTemplate was auto-created for Modbus device
	var tmpl models.ConfigTemplate
	db.First(&tmpl, "node_id = ?", "NODE001")
	if tmpl.WriteData == "" {
		t.Error("expected ConfigTemplate with write_data for sn3000 device type")
	}
}

// ==================== EdgeDevice Update Tests ====================

func TestEdgeDevice_Update_Success(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "Updated Name",
		"enabled":     false,
		"interval_ms": 10000,
		"hardware_id": "0x78",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/edge-devices/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Name != "Updated Name" {
		t.Errorf("expected name 'Updated Name', got %s", dev.Name)
	}
	if dev.Enabled {
		t.Error("expected enabled=false")
	}
}

func TestEdgeDevice_Update_NotFound(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{"name": "X"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/edge-devices/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestEdgeDevice_Update_InvalidJSON(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/edge-devices/1", bytes.NewReader([]byte("bad json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ==================== EdgeDevice Get Tests ====================

func TestEdgeDevice_Get_Found(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(200) {
		t.Errorf("expected code 200, got %v", resp["code"])
	}
}

// ==================== EdgeDevice List Tests ====================

func TestEdgeDevice_List_WithData(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Dev1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})
	db.Create(&models.EdgeDevice{Name: "Dev2", Type: "humidity", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== EdgeDevice Delete Tests ====================

func TestEdgeDevice_Delete_Success(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/edge-devices/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	db.Model(&models.EdgeDevice{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 devices after delete, got %d", count)
	}
}

func TestEdgeDevice_Delete_NonExistent(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/edge-devices/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for delete of nonexistent, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== EdgeDevice Sub-resource Tests ====================

func TestEdgeDevice_LatestData_Empty(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/latest-data", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Data_WithPagination(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/data?page=2&page_size=5", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["total"] != float64(0) {
		t.Errorf("expected total=0 for empty data, got %v", data["total"])
	}
}

func TestEdgeDevice_Data_WithTimeRange(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/data?start_time=2024-01-01T00:00:00Z&end_time=2025-12-31T23:59:59Z", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Operations_Post(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"operation": "read",
		"params":    map[string]interface{}{"register": "0x00"},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Operations_History(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/operations/history?limit=20", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Execute_InvalidOperationName(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"operation": "bad operation!@#",
		"params":    map[string]interface{}{},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid operation name, got %d", w.Code)
	}
}

func TestEdgeDevice_Execute_MissingOperation(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"params": map[string]interface{}{},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing operation, got %d", w.Code)
	}
}

func TestEdgeDevice_Execute_DeviceNotFound(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"operation": "read_data",
		"params":    map[string]interface{}{},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/999/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent device, got %d", w.Code)
	}
}

func TestEdgeDevice_ChangeAddress_InvalidAddress(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"new_address": 0,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/change-address", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid address, got %d", w.Code)
	}
}

func TestEdgeDevice_ChangeAddress_AddressOutOfRange(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"new_address": 300,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/change-address", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for address out of range, got %d", w.Code)
	}
}

func TestEdgeDevice_ChangeAddress_MissingNewAddress(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/change-address", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing new_address, got %d", w.Code)
	}
}

func TestEdgeDevice_ChangeAddress_DeviceNotFound(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"new_address": 5,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/999/change-address", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent device, got %d", w.Code)
	}
}

// ==================== getTemplateParamsFromDeviceConfig Tests ====================

func TestGetTemplateParamsFromDeviceConfig_Modbus(t *testing.T) {
	dc := models.DeviceConfig{
		Connection: json.RawMessage(`{"protocol":"modbus","default_params":{"start_register":0,"register_count":2}}`),
	}
	writeData, readLength, _ := getTemplateParamsFromDeviceConfig(dc, "3")
	if writeData == "" {
		t.Error("expected non-empty writeData for Modbus device config")
	}
	if readLength == 0 {
		t.Error("expected non-zero readLength")
	}
}

func TestGetTemplateParamsFromDeviceConfig_I2C(t *testing.T) {
	dc := models.DeviceConfig{
		DeviceType: "bmp280",
		Connection: json.RawMessage(`{"protocol":"i2c","default_params":{"read_register":"F7"}}`),
	}
	writeData, readLength, _ := getTemplateParamsFromDeviceConfig(dc, "")
	if writeData == "" {
		t.Error("expected non-empty writeData for I2C device config")
	}
	if readLength == 0 {
		t.Error("expected non-zero readLength for I2C")
	}
}

func TestGetTemplateParamsFromDeviceConfig_NilConnection(t *testing.T) {
	dc := models.DeviceConfig{}
	writeData, readLength, delayMs := getTemplateParamsFromDeviceConfig(dc, "")
	if writeData != "" {
		t.Error("expected empty writeData for nil connection")
	}
	if readLength != 0 || delayMs != 0 {
		t.Error("expected zero for nil connection")
	}
}

func TestGetTemplateParamsFromDeviceConfig_UnknownProtocol(t *testing.T) {
	dc := models.DeviceConfig{
		Connection: json.RawMessage(`{"protocol":"unknown"}`),
	}
	writeData, _, _ := getTemplateParamsFromDeviceConfig(dc, "")
	if writeData != "" {
		t.Error("expected empty writeData for unknown protocol")
	}
}

func TestGetTemplateParamsFromDeviceConfig_InvalidJSON(t *testing.T) {
	dc := models.DeviceConfig{
		Connection: json.RawMessage(`not json`),
	}
	writeData, _, _ := getTemplateParamsFromDeviceConfig(dc, "")
	if writeData != "" {
		t.Error("expected empty writeData for invalid JSON")
	}
}
