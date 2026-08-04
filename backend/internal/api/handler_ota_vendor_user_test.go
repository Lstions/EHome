package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
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
		&models.CalibrationCache{},
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
		"name":      "Model1",
		"type":      "temperature",
		"vendor_id": 1,
		"fields":    `{"sensors":["temp"]}`,
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
		&models.OTATask{}, &models.User{}, &models.CommandExecution{},
		&models.CommandOutbox{}, &models.CommandManualResolution{},
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

func TestMetricsSummaryIncludesDurableControlHealth(t *testing.T) {
	r, db := setupMetricsTest(t)
	now := time.Now().UTC()
	fresh := now.Add(-time.Minute)
	stale := now.Add(-commandexec.MaxCapabilityAge - time.Second)
	for _, node := range []models.Node{
		{NodeID: "fresh-control", Name: "Fresh", Status: "online", ResourceReportedAt: &fresh},
		{NodeID: "stale-control", Name: "Stale", Status: "online", ResourceReportedAt: &stale},
		{NodeID: "missing-control", Name: "Missing", Status: "online"},
		{NodeID: "offline-control", Name: "Offline", Status: "offline"},
	} {
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
	}
	statuses := []string{
		commandexec.StatusQueued, commandexec.StatusDispatched, commandexec.StatusSucceeded,
		commandexec.StatusFailed, commandexec.StatusUnknown, commandexec.StatusUnknown, commandexec.StatusCancelled,
	}
	for i, status := range statuses {
		commandID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		execution := models.CommandExecution{
			CommandID: commandID, EdgeDeviceID: 1, NodeID: "fresh-control", DeviceType: "test", DeviceConfigID: 1, ChannelID: 1,
			ManifestID: "manifest", ActionID: "read", ActionVersion: 1, CommandEngineRevision: 1, ActorUserID: 1,
			IdempotencyScope: fmt.Sprintf("metrics-scope-%d", i), IdempotencyKey: fmt.Sprintf("metrics-key-%d", i),
			RequestHash: strings.Repeat("a", 64), ParamsJSON: "{}", Status: status, DeadlineAt: now.Add(time.Minute), CreatedAt: now,
		}
		if err := db.Create(&execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.CommandManualResolution{CommandID: "00000000-0000-4000-8000-000000000005", Outcome: commandexec.ResolutionAcknowledgedUnknown, Reason: "reviewed", ResolvedBy: 1, ResolvedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"PENDING", "LEASED", "PROCESSED"} {
		if err := db.Create(&models.CommandOutbox{CommandID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), EventType: "command.dispatch", PayloadJSON: "{}", State: state, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/metrics/summary", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data MetricsResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	control := response.Data.Control
	if control.OperationsTotal != 7 || control.Active != 2 || control.Queued != 1 || control.Succeeded != 1 || control.Failed != 1 || control.Unknown != 2 || control.UnresolvedUnknown != 1 || control.Cancelled != 1 {
		t.Fatalf("control status summary=%+v", control)
	}
	if control.OutboxPending != 1 || control.OutboxLeased != 1 || control.CapabilityStaleNodes != 2 {
		t.Fatalf("control health summary=%+v", control)
	}
}

// ==================== RequireRole Extended Tests ====================

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
	req.Header.Set("Authorization", "Bearer eyJhbG...alid")
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
