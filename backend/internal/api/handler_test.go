package api

import (
	"bytes"
	"encoding/json"
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
		&models.OperationLog{},
		&models.Vendor{},
		&models.DeviceModel{},
		&models.NodeEvent{},
		&models.CalibrationCache{},
		&models.ConfigMeta{},
		&models.PendingWriteRecord{},
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

	// Seed a user
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	db.Create(&models.User{
		Username:     "admin",
		PasswordHash: string(hash),
		Role:         "admin",
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

func TestAuth_Logout(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	registerAuthRoutes(r, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSeedAdminUser(t *testing.T) {
	db := setupTestDB(t)

	// First seed should create admin
	err := SeedAdminUser(db)
	if err != nil {
		t.Fatalf("SeedAdminUser: %v", err)
	}

	var count int64
	db.Model(&models.User{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 user, got %d", count)
	}

	// Second seed should be idempotent
	err = SeedAdminUser(db)
	if err != nil {
		t.Fatalf("SeedAdminUser second call: %v", err)
	}
	db.Model(&models.User{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected still 1 user after second seed, got %d", count)
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
		"status":  "offline",
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
		"name":   "New Name",
		"status": "online",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/nodes/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
	caps := data["capabilities"].([]interface{})
	if len(caps) == 0 {
		t.Error("expected some capabilities for default ESP32-C6")
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

func TestNode_HardwareConfig_Put(t *testing.T) {
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

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/data?page=1&page_size=10", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Operations(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()

	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerEdgeDeviceRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

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

// ==================== RequireRole Tests ====================

func TestRequireRole_Allowed(t *testing.T) {
	r := setupRouter()
	r.Use(JWTAuth())
	r.Use(RequireRole("admin"))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	token, _ := GenerateToken(1, "admin")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRequireRole_Denied(t *testing.T) {
	r := setupRouter()
	r.Use(JWTAuth())
	r.Use(RequireRole("admin"))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	token, _ := GenerateToken(1, "user")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ==================== getDefaultESP32C6Buses Tests ====================

func TestGetDefaultESP32C6Buses(t *testing.T) {
	buses := getDefaultESP32C6Buses()
	expectedBuses := []string{"uart", "i2c", "spi", "gpio", "adc"}
	for _, name := range expectedBuses {
		if _, ok := buses[name]; !ok {
			t.Errorf("expected bus %q in default ESP32-C6 buses", name)
		}
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
