package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("warn")
}

// setupTestDB creates an in-memory SQLite database with all models migrated.
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	err = db.AutoMigrate(
		&models.Node{},
		&models.Channel{},
		&models.ConfigTemplate{},
		&models.EdgeDevice{},
		&models.DeviceConfig{},
		&models.DeviceData{},
		&models.UnifiedData{},
		&models.DataSource{},
		&models.OTATask{},
		&models.Firmware{},
		&models.Notification{},
		&models.User{},
		&models.AuthState{},
		&models.InitializationToken{},
		&models.SecurityAuditEvent{},
		&models.OperationLog{},
		&models.Vendor{},
		&models.DeviceModel{},
		&models.NodeEvent{},
		&models.CalibrationCache{},
		&models.ConfigMeta{},
		&models.PendingWriteRecord{},
		&models.NodeLog{},
		&models.LogicalDevice{},
	)
	if err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// setupRouter creates a gin.Engine in test mode.
func setupRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// generateAuthToken generates a valid JWT token for testing.
func generateAuthToken(t *testing.T) string {
	t.Helper()
	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return token
}

// authHeader returns an Authorization header value.
func authHeader(t *testing.T) string {
	return "Bearer " + generateAuthToken(t)
}

// ==================== Envelope Tests ====================

func TestEnvelope_Success(t *testing.T) {
	r := setupRouter()
	r.GET("/test", func(c *gin.Context) {
		Success(c, gin.H{"key": "value"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["code"] != float64(200) {
		t.Errorf("expected code 200, got %v", resp["code"])
	}
	if resp["message"] != "ok" {
		t.Errorf("expected message 'ok', got %v", resp["message"])
	}
}

func TestEnvelope_SuccessMsg(t *testing.T) {
	r := setupRouter()
	r.GET("/test", func(c *gin.Context) {
		SuccessMsg(c, gin.H{"id": 1}, "created")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["message"] != "created" {
		t.Errorf("expected message 'created', got %v", resp["message"])
	}
}

func TestEnvelope_SuccessWithCode(t *testing.T) {
	r := setupRouter()
	r.POST("/test", func(c *gin.Context) {
		SuccessWithCode(c, http.StatusCreated, gin.H{"id": 1})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(201) {
		t.Errorf("expected code 201, got %v", resp["code"])
	}
}

func TestEnvelope_SuccessWithCodeMsg(t *testing.T) {
	r := setupRouter()
	r.POST("/test", func(c *gin.Context) {
		SuccessWithCodeMsg(c, http.StatusCreated, gin.H{"id": 1}, "created")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/test", nil)
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(201) {
		t.Errorf("expected code 201, got %v", resp["code"])
	}
	if resp["message"] != "created" {
		t.Errorf("expected message 'created', got %v", resp["message"])
	}
}

func TestEnvelope_Error(t *testing.T) {
	r := setupRouter()
	r.GET("/test", func(c *gin.Context) {
		Error(c, http.StatusNotFound, "not found")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(404) {
		t.Errorf("expected code 404, got %v", resp["code"])
	}
	if resp["message"] != "not found" {
		t.Errorf("expected message 'not found', got %v", resp["message"])
	}
	if resp["data"] != nil {
		t.Errorf("expected data nil for error, got %v", resp["data"])
	}
}

// ==================== Auth Tests ====================

func TestAuth_Login_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	// Seed the unique authenticated subject
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	subjectKey := models.SystemAdminSubjectKey
	db.Create(&models.User{
		Username:       "admin",
		PasswordHash:   string(hash),
		Role:           "admin",
		Enabled:        true,
		SubjectKey:     &subjectKey,
		SessionVersion: 1,
		InitializedAt:  &now,
	})

	registerAuthRoutes(r, db)

	body, _ := json.Marshal(LoginRequest{Username: "admin", Password: "password123"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got %v", resp["data"])
	}
	if data["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestAuthLoginRejectsHistoricalNonSubjectAccount(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "historical", PasswordHash: string(hash), Role: "viewer", Enabled: true, SessionVersion: 1})
	registerAuthRoutes(r, db)
	body, _ := json.Marshal(LoginRequest{Username: "historical", Password: "password123"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuth_Login_WrongPassword(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "admin", PasswordHash: string(hash), Role: "admin"})

	registerAuthRoutes(r, db)

	body, _ := json.Marshal(LoginRequest{Username: "admin", Password: "wrong"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_Login_UserNotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	registerAuthRoutes(r, db)

	body, _ := json.Marshal(LoginRequest{Username: "nonexistent", Password: "pass"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_Login_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	registerAuthRoutes(r, db)

	body, _ := json.Marshal(map[string]string{"username": "admin"}) // missing password
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAuthLogoutIsNotPublic(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	registerAuthRoutes(r, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("public logout route must not exist, got %d", w.Code)
	}
}

func TestServerDoesNotSeedKnownDefaultAdmin(t *testing.T) {
	db := setupTestDB(t)
	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fresh database should not contain an automatic admin, got %d", count)
	}
}

// ==================== Node CRUD Tests ====================

func TestNodeCRUD_List(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	// Create test nodes
	db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "Node 2", Status: "offline"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data array, got %v", resp["data"])
	}
	if len(data) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(data))
	}
}

func TestNodeCRUD_GetByID(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	node := models.Node{NodeID: "NODE001", Name: "Test Node", Status: "online"}
	db.Create(&node)

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNodeCRUD_GetByNodeID(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "F0F5BD02F35C", Name: "Test Node", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/F0F5BD02F35C", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNodeCRUD_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestNodeCRUD_Create(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE003",
		"name":    "New Node",
		"model":   "ESP32-C6",
		"status":  "online",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var node models.Node
	db.First(&node, "node_id = ?", "NODE003")
	if node.Name != "New Node" {
		t.Errorf("expected name 'New Node', got %s", node.Name)
	}
	if node.Model != "" || node.Status != "offline" {
		t.Fatalf("create injected device-owned fields: model=%q status=%q", node.Model, node.Status)
	}
}

func TestNodeCRUD_Create_InvalidBody(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/nodes", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestNodeCRUD_Update(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Old Name", Status: "offline"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body, _ := json.Marshal(map[string]interface{}{
		"name":             "New Name",
		"status":           "online",
		"platform":         "forged",
		"model":            "forged",
		"firmware_version": "forged",
		"protocol_version": "forged",
		"config_version":   "forged",
		"config_status":    "forged",
		"wifi_ssid":        "forged",
		"wifi_rssi":        -1,
		"free_heap_bytes":  -1,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/nodes/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.Node
	if err := db.First(&updated, "node_id = ?", "NODE001").Error; err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New Name" || updated.Status != "offline" || updated.Platform != "" ||
		updated.Model != "" || updated.FirmwareVersion != "" || updated.ProtocolVersion != "2.2" ||
		updated.ConfigVersion != "" || updated.ConfigStatus != "pending" || updated.WiFiSSID != "" ||
		updated.WiFiRSSI != 0 || updated.FreeHeapBytes != 0 {
		t.Fatalf("generic update injected device-owned fields: %+v", updated)
	}
}

func TestNodeCRUD_Update_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body, _ := json.Marshal(map[string]interface{}{"name": "X"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/nodes/999", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestNodeCRUD_Delete(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "To Delete", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/nodes/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	db.Model(&models.Node{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 nodes after delete, got %d", count)
	}
}

func TestNodeCRUD_Delete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/nodes/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ==================== Node Sub-resource Tests ====================

func TestNode_GetChannels(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	node := models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}
	db.Create(&node)
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/channels", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNode_GetData(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.DeviceData{DeviceID: 1, NodeID: "NODE001", DataJSON: `{"temp":25}`, Timestamp: time.Now()})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/data?limit=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNode_Capabilities(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/capabilities", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if caps, ok := data["capabilities"].([]interface{}); ok && len(caps) != 0 {
		t.Fatalf("expected no invented capabilities before ResourceReport, got %#v", caps)
	}
	buses := data["buses"].(map[string]interface{})
	if len(buses) != 0 {
		t.Fatalf("expected empty hardware resources before ResourceReport, got %#v", buses)
	}
}

func TestNode_HardwareConfig_Get(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/hardware/config", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNode_HardwareConfig_PutRejectsReportedBuses(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body, _ := json.Marshal(map[string]interface{}{
		"hardware": map[string]interface{}{
			"buses": map[string]interface{}{
				"i2c": []interface{}{},
			},
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/nodes/1/hardware/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("hardware.buses is reported state and must be rejected: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var node models.Node
	if err := db.First(&node, 1).Error; err != nil {
		t.Fatal(err)
	}
	if node.HardwareInfo != "{}" && node.HardwareInfo != "" {
		t.Fatalf("hardware config API overwrote reported hardware_info: %s", node.HardwareInfo)
	}
}

func TestNode_UpdateCannotInjectReportedResources(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	node := models.Node{
		NodeID: "NODE001", Name: "before", Status: "online",
		Capabilities: `{"buses":{"gpio":[{"pin":6}]}}`,
		HardwareInfo: `{"platform":"ESP32C6","buses":{"gpio":[{"pin":6}]}}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body := bytes.NewBufferString(`{"name":"after","capabilities":"{\"buses\":{\"gpio\":[{\"pin\":99}]}}","hardware_info":"{\"buses\":{\"gpio\":[{\"pin\":99}]}}"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/nodes/NODE001", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got models.Node
	if err := db.First(&got, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "after" {
		t.Fatalf("allowed field was not updated: %+v", got)
	}
	if got.Capabilities != node.Capabilities || got.HardwareInfo != node.HardwareInfo {
		t.Fatalf("generic update injected reported resources: capabilities=%s hardware_info=%s", got.Capabilities, got.HardwareInfo)
	}
}

func TestNode_CreateCannotInjectReportedResourcesOrModelFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	body := bytes.NewBufferString(`{"id":999,"node_id":"NODE001","name":"created","platform":"forged-platform","capabilities":"{\"buses\":{\"gpio\":[{\"pin\":99}]}}","hardware_info":"{\"buses\":{\"gpio\":[{\"pin\":99}]}}"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var got models.Node
	if err := db.Where("node_id = ?", "NODE001").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.ID == 999 {
		t.Fatal("create accepted injected model primary key")
	}
	if got.Capabilities != "{}" || got.HardwareInfo != "{}" || got.Platform != "" {
		t.Fatalf("create injected reported resources: capabilities=%s hardware_info=%s platform=%s", got.Capabilities, got.HardwareInfo, got.Platform)
	}
}

func TestNodeConfigUpdateRejectsPeripheralChannelTypes(t *testing.T) {
	for _, busType := range []string{"GPIO", "PWM"} {
		t.Run(busType, func(t *testing.T) {
			db := setupTestDB(t)
			r := setupRouter()
			node := models.Node{NodeID: "NODE001", Name: "node", Status: "online"}
			db.Create(&node)
			ch := models.Channel{NodeID: node.NodeID, HardwareType: "I2C", BusType: "I2C", Enabled: true}
			db.Create(&ch)
			v1 := r.Group("/api/v1")
			v1.Use(JWTAuth())
			registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))
			body, _ := json.Marshal(map[string]interface{}{"channels": []map[string]interface{}{{"id": ch.ID, "bus_type": busType}}})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			db.First(&ch, ch.ID)
			if ch.BusType != "I2C" {
				t.Fatalf("rejected config update mutated bus_type to %q", ch.BusType)
			}
		})
	}
}

func TestNodeConfigCannotEnableStoredLegacyPeripheralChannel(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	node := models.Node{NodeID: "NODE001", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "GPIO", BusType: "GPIO", Enabled: false}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&ch).UpdateColumn("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	ch.Enabled = false
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))
	body, _ := json.Marshal(map[string]interface{}{"channels": []map[string]interface{}{{"id": ch.ID, "enabled": true}}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if err := db.First(&ch, ch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Enabled {
		t.Fatal("legacy peripheral channel was re-enabled")
	}
}

func TestNodeConfigUpdateEnforcesDeviceConfigBinding(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	node := models.Node{NodeID: "NODE001", Name: "node", Status: "online"}
	db.Create(&node)
	db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Current", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.DeviceConfig{Name: "Inactive", DeviceType: "other", HardwareType: "i2c", Status: "inactive"})
	db.Create(&models.DeviceConfig{Name: "Replacement", DeviceType: "replacement", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device", Type: "bmp280", NodeID: node.NodeID, ChannelID: 1, DeviceConfigID: 1})
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	for _, payload := range []map[string]interface{}{
		{"edge_devices": []map[string]interface{}{{"id": 1, "channel_id": 2}}},
		{"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 2}}},
	} {
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	}

	body, _ := json.Marshal(map[string]interface{}{"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 3}}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.EdgeDevice
	db.First(&updated, 1)
	if updated.DeviceConfigID != 3 || updated.Type != "replacement" {
		t.Fatalf("config update did not derive binding and type: %+v", updated)
	}

	// A batch is transactional: a later invalid item must roll back the earlier
	// valid replacement rather than leaving a partly rebound node configuration.
	db.Create(&models.EdgeDevice{Name: "Second", Type: "replacement", NodeID: node.NodeID, ChannelID: 1, DeviceConfigID: 3})
	body, _ = json.Marshal(map[string]interface{}{"edge_devices": []map[string]interface{}{
		{"id": 1, "device_config_id": 1},
		{"id": 2, "channel_id": 2},
	}})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	db.First(&updated, 1)
	if updated.DeviceConfigID != 3 || updated.Type != "replacement" {
		t.Fatalf("failed batch partially updated first device: %+v", updated)
	}

	// Channel and EdgeDevice mutations share one request transaction: an invalid
	// device candidate must roll back an otherwise valid channel mutation.
	interval := 4321
	body, _ = json.Marshal(map[string]interface{}{
		"channels":     []map[string]interface{}{{"id": 1, "interval_ms": interval}},
		"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 2}},
	})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var unchangedChannel models.Channel
	if err := db.First(&unchangedChannel, 1).Error; err != nil {
		t.Fatal(err)
	}
	if unchangedChannel.IntervalMs == interval {
		t.Fatalf("failed composite request committed channel mutation: %+v", unchangedChannel)
	}
	// Duplicate IDs are rejected before candidate construction; a request cannot
	// silently overwrite an earlier item with a later stale candidate.
	body, _ = json.Marshal(map[string]interface{}{"channels": []map[string]interface{}{
		{"id": 1, "enabled": false},
		{"id": 1, "interval_ms": 2000},
	}})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate channel 400, got %d: %s", w.Code, w.Body.String())
	}

	body, _ = json.Marshal(map[string]interface{}{"edge_devices": []map[string]interface{}{
		{"id": 1, "device_config_id": 1},
		{"id": 1, "device_config_id": 3},
	}})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate device 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNodeConfigUpdateRejectsDisablingBoundChannel(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	node := models.Node{NodeID: "NODE001", Name: "node", Status: "online"}
	db.Create(&node)
	db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "I2C", BusType: "I2C", BusConfig: "0102", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Config", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device", Type: "bmp280", NodeID: node.NodeID, ChannelID: 1, DeviceConfigID: 1})
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	registerNodeRoutes(v1, db, mgr)

	bus := mgr.EventBus()
	events := bus.Subscribe()
	body, _ := json.Marshal(map[string]interface{}{"channels": []map[string]interface{}{{"id": 1, "enabled": false}}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/config", node.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var channel models.Channel
	if err := db.First(&channel, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !channel.Enabled {
		t.Fatal("bound channel was disabled")
	}
	select {
	case event := <-events:
		t.Fatalf("rejected node config request emitted event: %+v", event)
	default:
	}
}

func TestNode_I2CScan(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/nodes/1/bus/i2c/scan", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== findNodeByID Tests ====================

func TestFindNodeByID_Numeric(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	node, err := findNodeByID(db, "1")
	if err != nil {
		t.Fatalf("findNodeByID: %v", err)
	}
	if node.NodeID != "NODE001" {
		t.Errorf("expected NODE001, got %s", node.NodeID)
	}
}

func TestFindNodeByID_StringNodeID(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&models.Node{NodeID: "F0F5BD02F35C", Name: "Test", Status: "online"})

	node, err := findNodeByID(db, "F0F5BD02F35C")
	if err != nil {
		t.Fatalf("findNodeByID: %v", err)
	}
	if node.NodeID != "F0F5BD02F35C" {
		t.Errorf("expected F0F5BD02F35C, got %s", node.NodeID)
	}
}

func TestFindNodeByID_NotFound(t *testing.T) {
	db := setupTestDB(t)

	_, err := findNodeByID(db, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent node")
	}
}

// ==================== Data Routes Tests ====================

func TestDataRoutes_SensorData(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/devices/1/sensor-data?limit=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDataRoutes_SensorData_InvalidDeviceID(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/devices/abc/sensor-data", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDataRoutes_History(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/devices/1/history?sensor=temp&hours=24", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDataRoutes_Historical(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	start := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	end := time.Now().Format(time.RFC3339)
	q := url.Values{}
	q.Set("device_pk", "1")
	q.Set("category", "temp")
	q.Set("start_time", start)
	q.Set("end_time", end)
	path := "/api/v1/unified-data/historical?" + q.Encode()

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDataRoutes_FailoverLogs(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/devices/1/failover-logs?limit=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== Edge Device Tests ====================

func TestEdgeDevice_List(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_GetByID(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/999", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestEdgeDevice_Delete(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/edge-devices/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_LatestData(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/latest-data", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Data(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/data?page=1&page_size=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_OperationsIsNotProvidedByLegacyRoutes(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	body, _ := json.Marshal(map[string]interface{}{
		"operation": "read",
		"params":    map[string]interface{}{"register": "0x00"},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without the Phase 1 operation service, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== RequireRole Tests ====================

// ==================== Empty hardware resource tests ====================

func TestEmptyHardwareResources(t *testing.T) {
	buses := emptyHardwareResources()
	if len(buses) != 0 {
		t.Fatalf("server must not invent node hardware resources: %#v", buses)
	}
}

// ==================== parseHardwareIDUint Tests ====================

func TestParseHardwareIDUint(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
	}{
		{"0x76", 118},
		{"5", 5},
		{"", 0},
		{"0xFF", 255},
		{"invalid", 0},
	}
	for _, tt := range tests {
		got := parseHardwareIDUint(tt.input)
		if got != tt.expected {
			t.Errorf("parseHardwareIDUint(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// ==================== createSingleTemplate Tests ====================

func TestCreateSingleTemplate(t *testing.T) {
	db := setupTestDB(t)
	node := models.Node{NodeID: "test-node-1", Name: "Test Node", Status: "online"}
	db.Create(&node)
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", HardwareID: "UART0"}
	db.Create(&ch)

	t.Run("valid template", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			return createSingleTemplate(tx, &ch, "DDA50300FFFD77", 60, 100)
		})
		if err != nil {
			t.Fatalf("createSingleTemplate failed: %v", err)
		}
		// Verify template was created
		var tmpl models.ConfigTemplate
		if err := db.Where("write_data = ?", "DDA50300FFFD77").First(&tmpl).Error; err != nil {
			t.Fatalf("template not found: %v", err)
		}
		if tmpl.ReadLength != 60 || tmpl.DelayMs != 100 {
			t.Errorf("unexpected template params: read_length=%d delay_ms=%d", tmpl.ReadLength, tmpl.DelayMs)
		}
	})

	t.Run("empty write_data", func(t *testing.T) {
		err := db.Transaction(func(tx *gorm.DB) error {
			return createSingleTemplate(tx, &ch, "", 10, 100)
		})
		if err != nil {
			t.Fatalf("createSingleTemplate with empty write_data should not error: %v", err)
		}
	})
}
