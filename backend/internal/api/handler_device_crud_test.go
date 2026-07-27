package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// setupDeviceTest creates a test router with DB and device-config + channel routes.
func setupDeviceTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	return setupDeviceTestWithRegistry(t, nil)
}

func setupDeviceTestWithRegistry(t *testing.T, driverRegistry *drivers.Registry) (*gin.Engine, *gorm.DB) {
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
		&models.GPIOConfig{}, &models.PWMConfig{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	registerDeviceRoutes(v1, db, mgr, driverRegistry, ControlPolicy{allowUnsafeRawForTests: true})
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

func TestDeviceConfig_CreateCanonicalizesAndRejectsStatus(t *testing.T) {
	r, db := setupDeviceTest(t)
	for _, status := range []string{"ACTIVE", "unsupported"} {
		body, _ := json.Marshal(map[string]interface{}{
			"name": "Status " + status, "device_type": "temperature", "hardware_type": "uart", "status": status,
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/device-configs", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if status == "ACTIVE" {
			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201 for canonicalized status, got %d: %s", w.Code, w.Body.String())
			}
			var config models.DeviceConfig
			if err := db.Where("name = ?", "Status ACTIVE").First(&config).Error; err != nil {
				t.Fatal(err)
			}
			if config.Status != "active" {
				t.Fatalf("status was not canonicalized: %q", config.Status)
			}
		} else if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for unsupported status, got %d: %s", w.Code, w.Body.String())
		}
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

func TestDeviceConfig_RejectsDestructiveChangesWhileReferenced(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Node", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Bound", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Bound device", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	for _, body := range []map[string]interface{}{
		{"name": "Bound", "hardware_type": "uart", "status": "active"},
		{"name": "Bound", "hardware_type": "i2c", "status": "inactive"},
		{"name": "Bound", "hardware_type": "i2c", "status": "unsupported"},
	} {
		payload, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/device-configs/1", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
		var unchanged models.DeviceConfig
		if err := db.First(&unchanged, 1).Error; err != nil {
			t.Fatal(err)
		}
		if unchanged.Status != "active" {
			t.Fatalf("rejected mutation changed status: %q", unchanged.Status)
		}
	}
	// Case variants normalize to the one stored canonical spelling instead of
	// leaving a binding that the exact active-status query would reject.
	payload, _ := json.Marshal(map[string]interface{}{"name": "Bound", "hardware_type": "i2c", "status": "ACTIVE"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/device-configs/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected canonicalized update 200, got %d: %s", w.Code, w.Body.String())
	}
	var canonical models.DeviceConfig
	if err := db.First(&canonical, 1).Error; err != nil {
		t.Fatal(err)
	}
	if canonical.Status != "active" {
		t.Fatalf("status was not canonicalized: %q", canonical.Status)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/device-configs/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
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

func TestDeviceConfig_DefaultMustBeActive(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.DeviceConfig{Name: "Active default", DeviceType: "temperature", HardwareType: "uart", IsDefault: true, Status: "active"})
	db.Create(&models.DeviceConfig{Name: "Inactive", DeviceType: "temperature", HardwareType: "uart", Status: "inactive"})

	for _, request := range []struct {
		method string
		path   string
		body   map[string]interface{}
	}{
		{method: http.MethodPost, path: "/api/v1/device-configs", body: map[string]interface{}{
			"name": "Inactive create", "device_type": "temperature", "hardware_type": "uart", "status": "inactive", "is_default": true,
		}},
		{method: http.MethodPut, path: "/api/v1/device-configs/2", body: map[string]interface{}{
			"name": "Inactive", "status": "inactive", "is_default": true,
		}},
		{method: http.MethodPost, path: "/api/v1/device-configs/2/default"},
	} {
		payload, err := json.Marshal(request.body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: expected 400, got %d: %s", request.method, request.path, w.Code, w.Body.String())
		}
		var active models.DeviceConfig
		if err := db.First(&active, 1).Error; err != nil {
			t.Fatal(err)
		}
		if !active.IsDefault {
			t.Fatalf("%s %s cleared the active default", request.method, request.path)
		}
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
		"bus_config":    "0102",
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

func TestChannel_Create_PreservesExplicitZeroInterval(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE001", "hardware_type": "UART", "bus_type": "UART",
		"bus_config": "1415000012C0", "enabled": true, "interval_ms": 0,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ch models.Channel
	db.First(&ch)
	if ch.IntervalMs != 0 {
		t.Fatalf("explicit interval_ms=0 was replaced with %d", ch.IntervalMs)
	}
}

func TestChannel_CreateRejectsMalformedTransportRoute(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE001", "hardware_type": "UART", "bus_type": "UART", "bus_config": "zz", "enabled": true,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code < 400 {
		t.Fatalf("malformed route accepted: %d %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 0 {
		t.Fatalf("malformed channel persisted")
	}
}

func TestChannel_CreateRejectsPeripheralTypes(t *testing.T) {
	for _, peripheralType := range []string{"GPIO", "gpio", "PWM", "pwm"} {
		t.Run(peripheralType, func(t *testing.T) {
			r, db := setupDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
			body, _ := json.Marshal(map[string]interface{}{
				"node_id": "NODE001", "hardware_type": peripheralType, "bus_type": peripheralType,
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/channels", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for peripheral channel type, got %d: %s", w.Code, w.Body.String())
			}
			var count int64
			db.Model(&models.Channel{}).Count(&count)
			if count != 0 {
				t.Fatalf("rejected peripheral channel was persisted")
			}
		})
	}
}

func TestChannel_CreateAllowsExplicitDisabledTransport(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE001", "hardware_type": "UART", "bus_type": "UART", "enabled": false,
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
	if err := db.First(&ch, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Enabled {
		t.Fatal("explicit disabled state was not preserved")
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

	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", BusConfig: "0102", Enabled: true, IntervalMs: 5000})

	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       "NODE001",
		"hardware_type": "UART",
		"bus_type":      "UART",
		"bus_config":    "0304",
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

func TestChannel_UpdateRejectsInvalidatingBoundEdgeDevice(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Node", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", BusConfig: "0102", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "I2C config", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Bound", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	for _, body := range []map[string]interface{}{
		{"enabled": false},
		{"hardware_type": "UART", "bus_type": "UART", "bus_config": "0304"},
	} {
		payload, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/channels/1", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("invalidating bound channel update succeeded: %s", w.Body.String())
		}
	}
	var channel models.Channel
	if err := db.First(&channel, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !channel.Enabled || channel.HardwareType != "I2C" || channel.BusType != "I2C" {
		t.Fatalf("rejected update mutated channel: %+v", channel)
	}
}

func TestChannel_UpdateDisabledTransportCanBeReenabled(t *testing.T) {
	r, db := setupDeviceTest(t)
	ch := models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", BusConfig: "0304", Enabled: false}
	db.Create(&ch)
	db.Model(&ch).UpdateColumn("enabled", false)
	if err := db.First(&ch, ch.ID).Error; err != nil || ch.Enabled {
		t.Fatalf("failed to establish disabled precondition: err=%v enabled=%v", err, ch.Enabled)
	}
	body, _ := json.Marshal(map[string]interface{}{"enabled": true})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/channels/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := db.First(&ch, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !ch.Enabled {
		t.Fatal("channel remained disabled")
	}
}

func TestChannel_UpdateRejectsPeripheralTypes(t *testing.T) {
	for _, field := range []string{"hardware_type", "bus_type"} {
		for _, peripheralType := range []string{"GPIO", "PWM"} {
			t.Run(field+"_"+peripheralType, func(t *testing.T) {
				r, db := setupDeviceTest(t)
				db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
				body, _ := json.Marshal(map[string]interface{}{field: peripheralType})
				w := httptest.NewRecorder()
				req := httptest.NewRequest("PUT", "/api/v1/channels/1", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", authHeader(t))
				r.ServeHTTP(w, req)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
				}
				var ch models.Channel
				db.First(&ch, 1)
				if ch.HardwareType != "I2C" || ch.BusType != "I2C" {
					t.Fatalf("rejected update mutated channel: %+v", ch)
				}
			})
		}
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

func TestChannel_DeleteRejectsBoundEdgeDevice(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Node", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", BusConfig: "0102", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "I2C config", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Bound", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/channels/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var channel models.Channel
	if err := db.First(&channel, 1).Error; err != nil {
		t.Fatal(err)
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

func TestChannelWriteRejectsLegacyPeripheralOrDisabledChannel(t *testing.T) {
	for _, tt := range []struct {
		name    string
		channel models.Channel
	}{
		{name: "GPIO", channel: models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true}},
		{name: "PWM", channel: models.Channel{NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true}},
		{name: "disabled UART", channel: models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "force-disabled"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupDeviceTest(t)
			if err := db.Create(&tt.channel).Error; err != nil {
				t.Fatalf("create channel: %v", err)
			}
			if tt.channel.HardwareID == "force-disabled" {
				db.Model(&models.Channel{}).Where("id = ?", tt.channel.ID).UpdateColumn("enabled", false)
			}

			body, _ := json.Marshal(map[string]interface{}{"data": "01", "hex_mode": true})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/1/write", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestChannelScanRejectsLegacyPeripheralOrDisabledChannel(t *testing.T) {
	for _, tt := range []struct {
		name    string
		channel models.Channel
	}{
		{name: "GPIO", channel: models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true}},
		{name: "PWM", channel: models.Channel{NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true}},
		{name: "disabled UART", channel: models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "force-disabled"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
			if err := db.Create(&tt.channel).Error; err != nil {
				t.Fatalf("create channel: %v", err)
			}
			if tt.channel.HardwareID == "force-disabled" {
				db.Model(&models.Channel{}).Where("id = ?", tt.channel.ID).UpdateColumn("enabled", false)
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/1/scan", bytes.NewBufferString(`{"scan_type":"i2c"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
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

func TestDeviceConfig_Tree_UsesInjectedDriverRegistry(t *testing.T) {
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	r, _ := setupDeviceTestWithRegistry(t, registry)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/tree", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"name":"蓝控"`) || !strings.Contains(body, `"type":"lk_th01"`) {
		t.Fatalf("injected LK-TH01 metadata is missing: %s", body)
	}
	if got := strings.Count(body, `"type":`); got != 7 {
		t.Fatalf("injected registry driver count=%d, want 7: %s", got, body)
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
		{"010", true, 0}, // odd length
		{"zz", true, 0},  // invalid hex
		{"", false, 0},   // empty
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
