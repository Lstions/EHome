package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

// setupDeviceTest creates a test router with DB and device-config + channel routes.
func setupDeviceTest(t *testing.T) (*gin.Engine, *gorm.DB) {
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
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	registerDeviceRoutes(v1, db, mgr, nil)
	return r, db
}

// ==================== DeviceConfig CRUD Tests ====================

func TestDeviceConfig_List_Empty(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["total"] != float64(0) {
		t.Errorf("expected total=0, got %v", data["total"])
	}
}

func TestDeviceConfig_List_WithFilter(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Temp1", DeviceType: "temperature", HardwareType: "uart", Status: "active"})
	db.Create(&models.DeviceConfig{Name: "Hum1", DeviceType: "humidity", HardwareType: "i2c", Status: "active"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs?device_type=temperature", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["total"] != float64(1) {
		t.Errorf("expected total=1 for filtered list, got %v", data["total"])
	}
}

func TestDeviceConfig_List_Pagination(t *testing.T) {
	r, db := setupDeviceTest(t)

	for i := 0; i < 25; i++ {
		db.Create(&models.DeviceConfig{Name: "Config" + string(rune('A'+i)), DeviceType: "temperature", HardwareType: "uart", Status: "active"})
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs?page=2&page_size=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Get_Found(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "uart"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["name"] != "Temp Sensor" {
		t.Errorf("expected name 'Temp Sensor', got %v", data["name"])
	}
}

func TestDeviceConfig_Get_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeviceConfig_Create_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "New Config",
		"device_type":   "temperature",
		"hardware_type": "uart",
		"description":   "A test config",
		"status":        "active",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dc models.DeviceConfig
	db.First(&dc, "name = ?", "New Config")
	if dc.Name != "New Config" {
		t.Errorf("expected name 'New Config', got %s", dc.Name)
	}
	if dc.DeviceType != "temperature" {
		t.Errorf("expected device_type 'temperature', got %s", dc.DeviceType)
	}
}

func TestDeviceConfig_Create_MissingName(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"device_type":   "temperature",
		"hardware_type": "uart",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Create_MissingDeviceType(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Config",
		"hardware_type": "uart",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Create_MissingHardwareType(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "Config",
		"device_type": "temperature",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Create_DefaultStatus(t *testing.T) {
	r, db := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Auto Status",
		"device_type":   "temperature",
		"hardware_type": "uart",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dc models.DeviceConfig
	db.First(&dc, "name = ?", "Auto Status")
	if dc.Status != "active" {
		t.Errorf("expected default status 'active', got %s", dc.Status)
	}
}

func TestDeviceConfig_Create_AsDefault(t *testing.T) {
	r, db := setupDeviceTest(t)

	// Create an existing default
	db.Create(&models.DeviceConfig{Name: "Old Default", DeviceType: "temperature", HardwareType: "uart", IsDefault: true, Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "New Default",
		"device_type":   "temperature",
		"hardware_type": "uart",
		"is_default":    true,
		"status":        "active",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Old default should be unset
	var oldDC models.DeviceConfig
	db.First(&oldDC, 1)
	if oldDC.IsDefault {
		t.Error("expected old default to be unset")
	}
}

func TestDeviceConfig_Update_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Old Name", DeviceType: "temperature", HardwareType: "uart", Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "Updated Name",
		"device_type":   "temperature",
		"hardware_type": "uart",
		"description":   "Updated desc",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-configs/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dc models.DeviceConfig
	db.First(&dc, 1)
	if dc.Name != "Updated Name" {
		t.Errorf("expected name 'Updated Name', got %s", dc.Name)
	}
}

func TestDeviceConfig_Update_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":          "X",
		"device_type":   "temperature",
		"hardware_type": "uart",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-configs/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeviceConfig_Update_MissingName(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Old Name", DeviceType: "temperature", HardwareType: "uart", Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"device_type":   "temperature",
		"hardware_type": "uart",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-configs/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Delete_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "To Delete", DeviceType: "temperature", HardwareType: "uart"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/device-configs/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	db.Model(&models.DeviceConfig{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 configs after delete, got %d", count)
	}
}

// ==================== DeviceConfig Default Tests ====================

func TestDeviceConfig_GetDefault_Found(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Default Temp", DeviceType: "temperature", HardwareType: "uart", IsDefault: true, Status: "active"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/default/temperature", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_GetDefault_FallbackActive(t *testing.T) {
	r, db := setupDeviceTest(t)

	// No is_default=true, but an active one exists
	db.Create(&models.DeviceConfig{Name: "Active Temp", DeviceType: "temperature", HardwareType: "uart", IsDefault: false, Status: "active"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/default/temperature", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_GetDefault_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/default/nonexistent_type", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_MarkDefault(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Config1", DeviceType: "temperature", HardwareType: "uart", IsDefault: true, Status: "active"})
	db.Create(&models.DeviceConfig{Name: "Config2", DeviceType: "temperature", HardwareType: "uart", IsDefault: false, Status: "active"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs/2/default", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Config1 should no longer be default
	var dc1 models.DeviceConfig
	db.First(&dc1, 1)
	if dc1.IsDefault {
		t.Error("expected Config1 is_default=false after marking Config2 as default")
	}

	var dc2 models.DeviceConfig
	db.First(&dc2, 2)
	if !dc2.IsDefault {
		t.Error("expected Config2 is_default=true")
	}
}

func TestDeviceConfig_MarkDefault_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs/999/default", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ==================== DeviceConfig InitFlow/Operations Tests ====================

func TestDeviceConfig_InitFlow(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{
		Name:         "With Init",
		DeviceType:   "temperature",
		HardwareType: "uart",
		InitFlow:     json.RawMessage(`[{"step":1,"action":"reset"}]`),
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/1/init-flow", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_InitFlow_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/999/init-flow", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeviceConfig_Operations(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{
		Name:         "With Ops",
		DeviceType:   "temperature",
		HardwareType: "uart",
		Operations:   json.RawMessage(`{"read_data":{"type":"read","command_template":"{{addr_hex}}03..."}}`),
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/1/operations", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Operations_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/999/operations", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ==================== DeviceConfig TestParser Tests ====================

func TestDeviceConfig_TestParser_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"raw_data": "010302006A",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs/999/test-parser", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeviceConfig_TestParser_WithJSONData(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{
		Name:         "Test Parser",
		DeviceType:   "temperature",
		HardwareType: "uart",
	})

	body, _ := json.Marshal(map[string]interface{}{
		"raw_data": `{"temp": 25.5}`,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs/1/test-parser", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_TestParser_WithHexData(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{
		Name:         "Test Parser Hex",
		DeviceType:   "temperature",
		HardwareType: "uart",
	})

	body, _ := json.Marshal(map[string]interface{}{
		"raw_data": "010302006A",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-configs/1/test-parser", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== Channel CRUD Tests ====================

func TestChannel_List_Empty(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/channels", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChannel_List_FilterByNodeID(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE002", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/channels?node_id=NODE001", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 channel for NODE001, got %d", len(data))
	}
}

func TestChannel_Create_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       "NODE001",
		"hardware_type": "I2C",
		"bus_type":      "I2C",
		"enabled":       true,
		"interval_ms":   5000,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var ch models.Channel
	db.First(&ch)
	if ch.NodeID != "NODE001" {
		t.Errorf("expected node_id NODE001, got %s", ch.NodeID)
	}
}

func TestChannel_Get_Found(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/channels/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChannel_Get_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/channels/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestChannel_Update_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true, IntervalMs: 5000})

	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       "NODE001",
		"hardware_type": "UART",
		"bus_type":      "UART",
		"enabled":       true,
		"interval_ms":   10000,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/channels/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChannel_Update_NotFound(t *testing.T) {
	r, _ := setupDeviceTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       "NODE001",
		"hardware_type": "I2C",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/channels/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestChannel_Delete_Success(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/channels/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 channels after delete, got %d", count)
	}
}

// ==================== DeviceConfig Tree Tests ====================

func TestDeviceConfig_Tree_Empty(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/tree", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceConfig_Tree_WithConfigs(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.DeviceConfig{Name: "Temp1", DeviceType: "temperature", HardwareType: "uart", Status: "active"})
	db.Create(&models.DeviceConfig{Name: "Hum1", DeviceType: "humidity", HardwareType: "i2c", Status: "active"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/tree", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== decodeHexString Tests ====================

func TestDecodeHexString(t *testing.T) {
	tests := []struct {
		input    string
		wantErr  bool
		expected int // len of decoded bytes
	}{
		{"0102", false, 2},
		{"0x0102", false, 2},
		{"0X0102", false, 2},
		{"010", true, 0},  // odd length
		{"zz", true, 0},   // invalid hex
		{"", false, 0},    // empty
	}
	for _, tt := range tests {
		got, err := decodeHexString(tt.input)
		if tt.wantErr && err == nil {
			t.Errorf("decodeHexString(%q): expected error, got nil", tt.input)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("decodeHexString(%q): unexpected error: %v", tt.input, err)
		}
		if !tt.wantErr && len(got) != tt.expected {
			t.Errorf("decodeHexString(%q): expected len=%d, got %d", tt.input, tt.expected, len(got))
		}
	}
}

// ==================== parseUintID Tests ====================

func TestParseUintID(t *testing.T) {
	tests := []struct {
		input    string
		expected uint
	}{
		{"1", 1},
		{"42", 42},
		{"0", 0},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseUintID(tt.input)
		if got != tt.expected {
			t.Errorf("parseUintID(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}
