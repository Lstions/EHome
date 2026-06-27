package api

import (
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

func setupTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	logger.Init("error")
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.DeviceConfig{}, &models.UnifiedData{}, &models.DeviceData{},
		&models.User{}, &models.OTATask{}, &models.Firmware{}, &models.Vendor{},
		&models.Notification{}, &models.OperationLog{})

	r := gin.New()
	v1 := r.Group("/api/v1")
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	registerNodeRoutes(v1, db, mgr)
	registerEdgeDeviceRoutes(v1, db, mgr)
	registerDataRoutes(v1, db)
	return r, db
}

func TestSensorData_EmptyResult(t *testing.T) {
	r, db := setupTestRouter(t)
	_ = db

	req := httptest.NewRequest("GET", "/api/v1/devices/1/sensor-data?limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result []interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	// Should return empty array, not null
	if w.Body.String() == "null" {
		t.Error("Expected empty array, got null")
	}
}

func TestSensorData_InvalidDeviceID(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/devices/abc/sensor-data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for invalid device ID, got %d", w.Code)
	}
}

func TestSensorData_WithSensorFilter(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/devices/1/sensor-data?sensor=temperature&limit=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestSensorData_WithSinceFilter(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/devices/1/sensor-data?since=2024-01-01T00:00:00Z", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestNodeLatest_NotFound(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/nodes/99999/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for nonexistent node, got %d", w.Code)
	}
}

func TestNodeLatest_WithNode(t *testing.T) {
	r, db := setupTestRouter(t)
	db.Create(&models.Node{NodeID: "test-node-1", Name: "Test", Status: "online"})

	req := httptest.NewRequest("GET", "/api/v1/nodes/test-node-1/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should return 200 (even if no data)
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for existing node, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNodeList_Empty(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestNodeList_WithNode(t *testing.T) {
	r, db := setupTestRouter(t)
	db.Create(&models.Node{NodeID: "node-1", Name: "Node A", Status: "online"})
	db.Create(&models.Node{NodeID: "node-2", Name: "Node B", Status: "offline"})

	req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if data, ok := body["data"]; ok {
		if list, ok := data.([]interface{}); ok {
			if len(list) != 2 {
				t.Errorf("Expected 2 nodes, got %d", len(list))
			}
		}
	}
}

func TestNodeDetail_Existing(t *testing.T) {
	r, db := setupTestRouter(t)
	node := models.Node{NodeID: "detail-node", Name: "Detail Test", Status: "online", Model: "ESP32-C6"}
	db.Create(&node)

	req := httptest.NewRequest("GET", "/api/v1/nodes/detail-node", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDeviceList_Empty(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/edge-devices", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestEdgeDeviceList_WithDevice(t *testing.T) {
	r, db := setupTestRouter(t)
	db.Create(&models.EdgeDevice{Name: "Dev1", Type: "sensor", NodeID: "n1", ChannelID: 1, DeviceConfigID: 1, HardwareID: "0x77"})

	req := httptest.NewRequest("GET", "/api/v1/edge-devices", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}
