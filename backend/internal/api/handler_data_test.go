package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	registerEdgeDeviceRoutes(v1, db, mgr, nil)
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

func TestNodeStatusHistory_ReturnsChronologicalStatusEvents(t *testing.T) {
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.NodeEvent{}); err != nil {
		t.Fatalf("migrate node events: %v", err)
	}
	now := time.Now().UTC()
	db.Create(&models.Node{NodeID: "history-node", Name: "History Node", Status: "online"})
	db.Create(&models.NodeEvent{NodeID: "history-node", EventType: "offline", OldStatus: "online", NewStatus: "offline", CreatedAt: now.Add(-time.Hour)})
	db.Create(&models.NodeEvent{NodeID: "history-node", EventType: "online", OldStatus: "offline", NewStatus: "online", CreatedAt: now})

	req := httptest.NewRequest("GET", "/api/v1/nodes/history-node/status-history?limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []models.NodeEvent `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("expected 2 events, got %d", len(body.Data))
	}
	if body.Data[0].NewStatus != "online" || body.Data[1].NewStatus != "offline" {
		t.Fatalf("expected newest-first status events, got %#v", body.Data)
	}
}

func TestGlobalNodeStatusHistory_IncludesNodeNames(t *testing.T) {
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.NodeEvent{}); err != nil {
		t.Fatalf("migrate node events: %v", err)
	}
	db.Create(&models.Node{NodeID: "status-node", Name: "现场节点", Status: "online"})
	db.Create(&models.NodeEvent{NodeID: "status-node", EventType: "online", OldStatus: "offline", NewStatus: "online"})

	req := httptest.NewRequest("GET", "/api/v1/nodes/status-history?limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []struct {
			NodeID    string `json:"node_id"`
			NodeName  string `json:"node_name"`
			NewStatus string `json:"new_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].NodeID != "status-node" || body.Data[0].NodeName != "现场节点" || body.Data[0].NewStatus != "online" {
		t.Fatalf("unexpected status history: %#v", body.Data)
	}
}

func TestUnifiedDataCategories_ReturnsOnlyCategoriesForSelectedDevice(t *testing.T) {
	r, db := setupTestRouter(t)
	now := time.Now().UTC()
	db.Create(&models.UnifiedData{DeviceID: 1, SensorName: "temperature", Value: 20, Unit: "°C", Timestamp: now})
	db.Create(&models.UnifiedData{DeviceID: 1, SensorName: "humidity", Value: 50, Unit: "%", Timestamp: now})
	db.Create(&models.UnifiedData{DeviceID: 2, SensorName: "voltage", Value: 48, Unit: "V", Timestamp: now})

	req := httptest.NewRequest("GET", "/api/v1/unified-data/categories?device_pk=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body []struct {
		Code string `json:"code"`
		Unit string `json:"unit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 2 || body[0].Code != "humidity" || body[1].Code != "temperature" {
		t.Fatalf("unexpected categories: %#v", body)
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
