package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("error")
}

// ==================== OTA Tests ====================

func setupOTATest(t *testing.T) (*gin.Engine, *gorm.DB, *ota.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.DeviceConfig{}, &models.DeviceData{}, &models.UnifiedData{},
		&models.User{}, &models.OTATask{}, &models.Firmware{},
		&models.Vendor{}, &models.Notification{}, &models.OperationLog{},
		&models.DeviceModel{}, &models.NodeEvent{},
		&models.CalibrationCache{}, &models.ConfigMeta{},
		&models.PendingWriteRecord{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	otaMgr := ota.NewManager(db, nil, nil)
	registerOTARoutes(v1, db, otaMgr, mgr)
	return r, db, otaMgr
}

func TestOTA_ListTasks_Empty(t *testing.T) {
	r, _, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/ota/tasks", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_ListTasks_WithData(t *testing.T) {
	r, db, _ := setupOTATest(t)

	db.Create(&models.OTATask{OtaID: "ota-1", NodeID: "NODE001", FirmwareID: 1, Status: "pending"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/ota/tasks", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_GetTask_Found(t *testing.T) {
	r, db, _ := setupOTATest(t)

	db.Create(&models.OTATask{OtaID: "ota-2", NodeID: "NODE001", FirmwareID: 1, Status: "pending"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/ota/tasks/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_GetTask_NotFound(t *testing.T) {
	r, _, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/ota/tasks/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestOTA_CreateTask_MissingFields(t *testing.T) {
	r, _, _ := setupOTATest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE001",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/ota/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_CancelTask_InvalidID(t *testing.T) {
	r, _, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/ota/tasks/abc/cancel", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_ListFirmwares_Empty(t *testing.T) {
	r, _, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/firmwares", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_ListFirmwares_WithData(t *testing.T) {
	r, db, _ := setupOTATest(t)

	db.Create(&models.Firmware{Version: "1.0.0", Checksum: "abc123", SizeBytes: 1024, URL: "http://test/fw.bin"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/firmwares", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_UpdateFirmware_Success(t *testing.T) {
	r, db, _ := setupOTATest(t)

	db.Create(&models.Firmware{Version: "1.0.0", Checksum: "abc123", SizeBytes: 1024, URL: "http://test/fw.bin"})

	body, _ := json.Marshal(map[string]interface{}{
		"version":   "1.1.0",
		"changelog": "Bug fixes",
		"stable":    true,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/firmwares/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_UpdateFirmware_NotFound(t *testing.T) {
	r, _, _ := setupOTATest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"version": "1.1.0",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/firmwares/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestOTA_DeleteFirmware_Success(t *testing.T) {
	r, db, _ := setupOTATest(t)

	db.Create(&models.Firmware{Version: "1.0.0", Checksum: "abc123", SizeBytes: 1024, URL: "http://test/fw.bin", Filename: "fw.bin"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/firmwares/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOTA_DeleteFirmware_NotFound(t *testing.T) {
	r, _, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/firmwares/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== Vendor CRUD Tests ====================

func setupVendorTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Vendor{}, &models.DeviceModel{}, &models.User{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerVendorRoutes(v1, db)
	return r, db
}

func TestVendor_List_Empty(t *testing.T) {
	r, _ := setupVendorTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/vendors", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVendor_Create_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name": "TestVendor",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/vendors", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var v models.Vendor
	db.First(&v)
	if v.Name != "TestVendor" {
		t.Errorf("expected name 'TestVendor', got %s", v.Name)
	}
}

func TestVendor_Create_MissingName(t *testing.T) {
	r, _ := setupVendorTest(t)

	body, _ := json.Marshal(map[string]interface{}{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/vendors", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVendor_Create_NonAdminForbidden(t *testing.T) {
	r, _ := setupVendorTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name": "TestVendor",
	})
	token, _ := GenerateToken(2, "user")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/vendors", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
}

func TestVendor_Get_Found(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "Vendor1"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/vendors/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVendor_Get_NotFound(t *testing.T) {
	r, _ := setupVendorTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/vendors/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestVendor_Update_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "OldName"})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "NewName",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/vendors/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVendor_Update_NotFound(t *testing.T) {
	r, _ := setupVendorTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name": "X",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/vendors/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestVendor_Delete_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "ToDelete"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/vendors/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== DeviceModel Tests ====================

func TestDeviceModel_List_Empty(t *testing.T) {
	r, _ := setupVendorTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-models", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Create_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "Vendor1"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":       "Model1",
		"type":       "temperature",
		"vendor_id":  1,
		"fields":     `{"sensors":["temp"]}`,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Create_MissingFields(t *testing.T) {
	r, _ := setupVendorTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Model1",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/device-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Get_Found(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "V1"})
	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-models/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Get_NotFound(t *testing.T) {
	r, _ := setupVendorTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-models/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeviceModel_Update_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "V1"})
	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 1})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "UpdatedModel",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-models/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Delete_Success(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.Vendor{Name: "V1"})
	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 1})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/device-models/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Fields_Get(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 0, Fields: `{"sensors":["temp"]}`})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-models/1/fields", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceModel_Fields_Update(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 0, Fields: `{}`})

	body, _ := json.Marshal(map[string]interface{}{
		"fields": `{"sensors":["temp","humidity"]}`,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-models/1/fields", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeviceCategories_List(t *testing.T) {
	r, db := setupVendorTest(t)

	db.Create(&models.DeviceModel{Name: "M1", Type: "temperature", VendorID: 0})
	db.Create(&models.DeviceModel{Name: "M2", Type: "humidity", VendorID: 0})
	db.Create(&models.DeviceModel{Name: "M3", Type: "temperature", VendorID: 0})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-categories", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== User CRUD Tests ====================

func setupUserTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(&models.User{})
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerUserRoutes(v1, db)
	return r, db
}

func TestUser_List_Empty(t *testing.T) {
	r, _ := setupUserTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_Create_Success(t *testing.T) {
	r, db := setupUserTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"username": "testuser",
		"password": "password123",
		"email":    "test@example.com",
		"role":     "user",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var user models.User
	db.First(&user, "username = ?", "testuser")
	if user.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %s", user.Username)
	}
	if user.Role != "user" {
		t.Errorf("expected role 'user', got %s", user.Role)
	}
}

func TestUser_Create_MissingFields(t *testing.T) {
	r, _ := setupUserTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"username": "testuser",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_Create_NonAdminForbidden(t *testing.T) {
	r, _ := setupUserTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"username": "testuser",
		"password": "password123",
	})
	token, _ := GenerateToken(2, "user")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
}

func TestUser_Get_Found(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "admin", PasswordHash: string(hash), Role: "admin"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/users/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_Get_NotFound(t *testing.T) {
	r, _ := setupUserTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/users/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUser_Update_Success(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "admin", PasswordHash: string(hash), Role: "admin"})

	body, _ := json.Marshal(map[string]interface{}{
		"role":    "user",
		"enabled": false,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/users/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_Update_NotFound(t *testing.T) {
	r, _ := setupUserTest(t)

	body, _ := json.Marshal(map[string]interface{}{
		"role": "user",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/users/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUser_Delete_Success(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "todelete", PasswordHash: string(hash), Role: "user"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/users/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_ChangePassword_Success(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "user1", PasswordHash: string(hash), Role: "user"})

	// Generate token for user ID=1
	token, _ := GenerateToken(1, "user")

	body, _ := json.Marshal(map[string]interface{}{
		"old_password": "oldpass",
		"new_password": "newpass123",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users/me/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_ChangePassword_WrongOldPassword(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "user1", PasswordHash: string(hash), Role: "user"})

	token, _ := GenerateToken(1, "user")

	body, _ := json.Marshal(map[string]interface{}{
		"old_password": "wrongpass",
		"new_password": "newpass123",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users/me/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_ResetPassword_Success(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "user1", PasswordHash: string(hash), Role: "user"})

	body, _ := json.Marshal(map[string]interface{}{
		"new_password": "resetpass123",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users/1/reset-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_ResetPassword_MissingPassword(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "user1", PasswordHash: string(hash), Role: "user"})

	body, _ := json.Marshal(map[string]interface{}{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users/1/reset-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUser_List_WithKeyword(t *testing.T) {
	r, db := setupUserTest(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "admin", PasswordHash: string(hash), Role: "admin"})
	db.Create(&models.User{Username: "viewer", PasswordHash: string(hash), Role: "user"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/users?keyword=admin", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== Metrics Tests ====================

func setupMetricsTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.EdgeDevice{}, &models.UnifiedData{},
		&models.OTATask{}, &models.User{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerMetricsRoutes(v1, db)
	return r, db
}

func TestMetrics_BasicEndpoint(t *testing.T) {
	r, _ := setupMetricsTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/metrics", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetrics_Summary_Empty(t *testing.T) {
	r, _ := setupMetricsTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/metrics/summary", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["timestamp"] == nil {
		t.Error("expected timestamp in summary")
	}
}

func TestMetrics_Summary_WithData(t *testing.T) {
	r, db := setupMetricsTest(t)

	db.Create(&models.Node{NodeID: "N1", Name: "Online", Status: "online"})
	db.Create(&models.Node{NodeID: "N2", Name: "Offline", Status: "offline"})
	db.Create(&models.EdgeDevice{Name: "Dev1", Type: "temp", NodeID: "N1", ChannelID: 1, DeviceConfigID: 0, Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Dev2", Type: "hum", NodeID: "N1", ChannelID: 1, DeviceConfigID: 0, Status: "inactive"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/metrics/summary", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== RequireRole Extended Tests ====================

func TestRequireRole_NoRoleInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireRole("admin"))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when no role in context, got %d", w.Code)
	}
}

func TestJWTAuth_BearerPrefixCaseInsensitive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	token, _ := GenerateToken(1, "admin")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "bearer "+token) // lowercase
	r.ServeHTTP(w, req)

	// Should still work because EqualFold is used
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for lowercase bearer, got %d", w.Code)
	}
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	// This test validates the token parsing path;
	// actual expired token would need time manipulation
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// Use a completely invalid token string
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjF9.invalid")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid/expired token, got %d", w.Code)
	}
}

func TestJWTAuth_MalformedAuthHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// No Bearer prefix
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for Basic auth header, got %d", w.Code)
	}
}
