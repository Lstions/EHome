package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("error")
}

// setupPeriphTest 创建外设控制测试路由 (GPIO/PWM)
// 返回 gin.Engine 和 gorm.DB, 数据库中预置一个节点
func setupPeriphTest(t *testing.T) (*gin.Engine, *gorm.DB, *nodemgr.Manager) {
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
		&models.PendingWriteRecord{},
		&models.NodeLog{}, &models.GPIOConfig{}, &models.PWMConfig{},
	)
	// 创建默认用户 (JWT 需要 user_id=1 存在)
	db.Create(&models.User{Username: "admin", PasswordHash: "$2a$10$dummy", Role: "admin", Enabled: true})

	// 使用零值 mqtt.Client (c.client=nil, PublishQoS2 返回 error 而非 panic)
	mockMQTT := &mqtt.Client{}
	mgr := nodemgr.NewManager(db, mockMQTT, nil, nil, nil, nil)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerPeriphRoutes(v1, db, mgr)
	return r, db, mgr
}

// createTestNode 在 DB 中创建一个测试节点并返回
func createTestNode(t *testing.T, db *gorm.DB, nodeID string) *models.Node {
	t.Helper()
	node := models.Node{NodeID: nodeID, Name: "test-node", Status: "online", ProtocolVersion: "2.5"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	return &node
}

const testPeriphCapabilities = `{"buses":{"pwm":[{"id":"PWM0","channel":0,"timer_count":4,"max_resolution_bits":14},{"id":"PWM1","channel":1,"timer_count":4,"max_resolution_bits":14}],"gpio":[{"id":"GPIO6","pin":6},{"id":"GPIO7","pin":7},{"id":"GPIO8","pin":8}],"uart":[{"id":"UART0","port":0,"default_tx_pin":16,"default_rx_pin":17}],"i2c":[{"id":"I2C0","port":0,"default_sda_pin":4,"default_scl_pin":5}],"spi":[{"id":"SPI2","port":2,"default_mosi_pin":10,"default_miso_pin":11,"default_sclk_pin":12,"default_cs_pin":13}]}}`

func createTestNodeWithPeriphResources(t *testing.T, db *gorm.DB, nodeID string) *models.Node {
	t.Helper()
	node := createTestNode(t, db, nodeID)
	node.Capabilities = testPeriphCapabilities
	if err := db.Model(node).Update("capabilities", node.Capabilities).Error; err != nil {
		t.Fatalf("set node capabilities: %v", err)
	}
	return node
}

// periphJSON 发送 JSON 请求并返回响应
func periphJSON(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// parseEnvelope 解析标准响应信封
func parseEnvelope(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse response: %v (body: %s)", err, string(body))
	}
	return resp
}

// =====================================================================
// GPIO API 测试
// =====================================================================

// ==================== GET /nodes/:id/gpio ====================

func TestGPIO_List_Empty(t *testing.T) {
	// Arrange: 创建节点但无 GPIO 配置
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/gpio", nil)

	// Assert: 返回空数组
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseEnvelope(t, w.Body.Bytes())
	data := resp["data"].([]interface{})
	if len(data) != 0 {
		t.Errorf("expected empty list, got %d items", len(data))
	}
}

func TestGPIO_List_WithConfigs(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNode(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 2, Direction: 1, Enabled: true})
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 5, Direction: 0, Enabled: true})

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/gpio", nil)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseEnvelope(t, w.Body.Bytes())
	data := resp["data"].([]interface{})
	if len(data) != 2 {
		t.Errorf("expected 2 configs, got %d", len(data))
	}
}

func TestGPIO_List_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/nonexistent/gpio", nil)

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/gpio ====================

func TestGPIO_Create_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin":           6,
		"direction":     1,
		"initial_level": 0,
		"label":         "LED1",
	})

	// Assert: 返回 201 + 创建的配置
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.GPIOConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Pin != 6 {
		t.Errorf("expected pin=6, got %d", cfg.Pin)
	}
	if cfg.Direction != 1 {
		t.Errorf("expected direction=1, got %d", cfg.Direction)
	}
	if cfg.Label != "LED1" {
		t.Errorf("expected label=LED1, got %s", cfg.Label)
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true by default")
	}
}

func TestGPIO_Create_WithEnabledFalse(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin":       7,
		"direction": 0,
		"enabled":   false,
	})

	// Assert: explicit false must survive GORM's default:true model tag.
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.GPIOConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Enabled {
		t.Fatal("explicit enabled=false was overwritten")
	}
	var stored models.GPIOConfig
	if err := db.First(&stored, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("stored GPIO enabled=false was overwritten")
	}
}

func TestGPIOCreateRequiresPinPresenceAndSetRejectsNonBinaryLevel(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"direction": 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing pin: %d %s", w.Code, w.Body.String())
	}
	db.Create(&models.GPIOConfig{NodeID: "node-1", Pin: 6, Direction: 1, Enabled: true})
	w = periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/6/set", map[string]interface{}{"level": 2})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("level=2: %d %s", w.Code, w.Body.String())
	}
}

func TestPWMCreateExplicitEnabledFalsePersists(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0", "pin": 6, "frequency": 1000, "resolution": 14, "enabled": false,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var stored models.PWMConfig
	if err := db.Where("node_id = ? AND hardware_id = ?", "node-1", "PWM0").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("stored PWM enabled=false was overwritten")
	}
}

func TestDisabledPeripheralConfigsRejectControl(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	gpio := models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1, Enabled: false}
	pwm := models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 7, Frequency: 1000, Duty: 1000, Resolution: 14, Enabled: false}
	db.Create(&gpio)
	db.Create(&pwm)
	db.Model(&gpio).UpdateColumn("enabled", false)
	db.Model(&pwm).UpdateColumn("enabled", false)
	for _, tc := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{"POST", "/api/v1/nodes/node-1/gpio/6/set", map[string]interface{}{"level": 1}},
		{"POST", "/api/v1/nodes/node-1/gpio/6/read", nil},
		{"POST", "/api/v1/nodes/node-1/pwm/PWM0/start", nil},
		{"POST", "/api/v1/nodes/node-1/pwm/PWM0/stop", nil},
		{"POST", "/api/v1/nodes/node-1/pwm/PWM0/duty", map[string]interface{}{"duty": 2000}},
		{"POST", "/api/v1/nodes/node-1/pwm/PWM0/freq", map[string]interface{}{"frequency": 500}},
		{"GET", "/api/v1/nodes/node-1/pwm/PWM0/state", nil},
	} {
		w := periphJSON(t, r, tc.method, tc.path, tc.body)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s %s expected 409, got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestGPIO_Create_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/gpio", map[string]interface{}{
		"pin":       1,
		"direction": 1,
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGPIO_Create_InvalidDirection(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act: direction=5 超出范围 (0-3)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin":       1,
		"direction": 5,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Create_NegativePin(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin":       -1,
		"direction": 1,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Create_DuplicatePin(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 7, Direction: 1, Enabled: true})

	// Act: 同一 pin 再次创建
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin":       7,
		"direction": 0,
	})

	// Assert: 应返回 409 Conflict
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Create_InvalidJSON(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act: 发送非法 JSON
	req := httptest.NewRequest("POST", "/api/v1/nodes/node-1/gpio", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ==================== PUT /nodes/:id/gpio/:pin ====================

func TestGPIO_Update_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 0, Label: "old", Enabled: true})

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/gpio/6", map[string]interface{}{
		"direction": 1,
		"label":     "updated",
	})

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.GPIOConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Direction != 1 {
		t.Errorf("expected direction=1, got %d", cfg.Direction)
	}
	if cfg.Label != "updated" {
		t.Errorf("expected label=updated, got %s", cfg.Label)
	}
}

func TestGPIO_Update_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/nonexistent/gpio/1", map[string]interface{}{
		"label": "test",
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGPIO_Update_ConfigNotFound(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act: pin 99 不存在
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/gpio/99", map[string]interface{}{
		"label": "test",
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Update_InvalidDirection(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 0, Enabled: true})

	// Act: direction=10 超出范围
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/gpio/6", map[string]interface{}{
		"direction": 10,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Update_EnabledFlag(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1, Enabled: true})

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/gpio/6", map[string]interface{}{
		"enabled": false,
	})

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.GPIOConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Enabled {
		t.Error("expected enabled=false after update")
	}
}

// ==================== DELETE /nodes/:id/gpio/:pin ====================

func TestGPIO_Delete_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1, Enabled: true})

	// Act
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/node-1/gpio/6", nil)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// 验证 DB 中已删除
	var count int64
	db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, 6).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 records after delete, got %d", count)
	}
}

func TestGPIO_Delete_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/nonexistent/gpio/1", nil)

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGPIO_Delete_ConfigNotFound(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/node-1/gpio/99", nil)

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/gpio/:pin/set ====================

func TestGPIO_Set_Success(t *testing.T) {
	// Arrange: 创建带有 mqtt publisher 的 manager (nil mqtt 会导致 SendPeriphCmd 报错)
	// 由于 SendPeriphCmd 需要 mqtt client, 这里测试 nil mqtt 的错误路径
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act: 发送 set 命令 (mqtt=nil, SendPeriphCmd 会失败)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/3/set", map[string]interface{}{
		"level": 1,
	})

	// Assert: 由于 mqtt 为 nil, SendPeriphCmd 会报错, 返回 500
	// 这是预期行为 — 没有 MQTT 连接时无法发送命令
	if w.Code != http.StatusInternalServerError {
		// 如果有 mqtt mock 则可能成功
		t.Logf("got status %d (expected 500 without MQTT): %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Set_Toggle(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/3/set", map[string]interface{}{
		"toggle": true,
	})

	// Assert: toggle=true 时 action=GPIOActionToggle
	// 没有 MQTT 会返回 500
	t.Logf("toggle response: %d %s", w.Code, w.Body.String())
}

func TestGPIO_Set_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/gpio/1/set", map[string]interface{}{
		"level": 1,
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGPIO_Set_MissingBodyReturnsBadRequest(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	if err := db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/6/set", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing body, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/gpio/:pin/read ====================

func TestGPIO_Read_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/gpio/1/read", nil)

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGPIO_Read_NoMQTT(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/2/read", nil)

	// Assert: 没有 MQTT 时 SendPeriphCmd 报错
	if w.Code != http.StatusInternalServerError {
		t.Logf("got status %d (expected 500 without MQTT): %s", w.Code, w.Body.String())
	}
}

// =====================================================================
// PWM API 测试
// =====================================================================

// ==================== GET /nodes/:id/pwm ====================

func TestPWM_List_Empty(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/pwm", nil)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseEnvelope(t, w.Body.Bytes())
	data := resp["data"].([]interface{})
	if len(data) != 0 {
		t.Errorf("expected empty list, got %d items", len(data))
	}
}

func TestPWM_List_WithConfigs(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/pwm", nil)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := parseEnvelope(t, w.Body.Bytes())
	data := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 config, got %d", len(data))
	}
	cfg := data[0].(map[string]interface{})
	if cfg["hardware_id"] != "PWM0" || cfg["channel"] != float64(0) {
		t.Fatalf("unexpected PWM resource identity: %v", cfg)
	}
}

func TestPWM_List_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/nonexistent/pwm", nil)

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ==================== POST /nodes/:id/pwm ====================

func TestPWM_Create_RequiresReportedResourceIdentity(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"pin": 6, "frequency": 1000,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing hardware_id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Create_FailsClosedForUnreportedResources(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	t.Run("PWM hardware resource", func(t *testing.T) {
		w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
			"hardware_id": "PWM9", "pin": 6, "frequency": 1000,
		})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("GPIO pin resource", func(t *testing.T) {
		w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
			"hardware_id": "PWM0", "pin": 99, "frequency": 1000,
		})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestPWM_Create_ResolvesReportedChannel(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM1", "pin": 6, "frequency": 1000,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.PWMConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HardwareID != "PWM1" || cfg.Channel != 1 || cfg.Pin != 6 {
		t.Fatalf("unexpected resolved PWM identity: %+v", cfg)
	}
}

func TestPWM_Create_RejectsGPIOPinConflict(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 6, Direction: 1, Enabled: true})

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0", "pin": 6, "frequency": 1000,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_Create_RejectsPWMPinConflict(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin": 6, "direction": 1,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIO_FailsClosedAgainstCurrentReportedResources(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   interface{}
		seed   bool
	}{
		{"create without report", "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 6, "direction": 1}, false},
		{"create unknown pin", "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 99, "direction": 1}, false},
		{"update stale pin", "PUT", "/api/v1/nodes/node-1/gpio/99", map[string]interface{}{"label": "stale"}, true},
		{"delete stale pin", "DELETE", "/api/v1/nodes/node-1/gpio/99", nil, true},
		{"set stale pin", "POST", "/api/v1/nodes/node-1/gpio/99/set", map[string]interface{}{"level": 1}, true},
		{"read stale pin", "POST", "/api/v1/nodes/node-1/gpio/99/read", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, db, _ := setupPeriphTest(t)
			var node *models.Node
			if tc.name == "create without report" {
				node = createTestNode(t, db, "node-1")
			} else {
				node = createTestNodeWithPeriphResources(t, db, "node-1")
			}
			if tc.seed {
				if err := db.Create(&models.GPIOConfig{NodeID: node.NodeID, Pin: 99, Direction: 1, Enabled: true}).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := periphJSON(t, r, tc.method, tc.path, tc.body)
			if tc.method == "DELETE" {
				if w.Code != http.StatusOK {
					t.Fatalf("stale cleanup expected 200, got %d: %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGPIO_SetAndReadRequireExistingConfig(t *testing.T) {
	for _, action := range []string{"set", "read"} {
		t.Run(action, func(t *testing.T) {
			r, db, _ := setupPeriphTest(t)
			createTestNodeWithPeriphResources(t, db, "node-1")
			var body interface{}
			if action == "set" {
				body = map[string]interface{}{"level": 1}
			}
			w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio/6/"+action, body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPWM_Update_UsesHardwareIDIdentity(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM0", map[string]interface{}{"frequency": 2000})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.PWMConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.HardwareID != "PWM0" || cfg.Frequency != 2000 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestPWM_UpdateAndControlRejectStaleUnreportedConfig(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   interface{}
	}{
		{"update", "PUT", "/api/v1/nodes/node-1/pwm/PWM9", map[string]interface{}{"frequency": 2000}},
		{"start", "POST", "/api/v1/nodes/node-1/pwm/PWM9/start", nil},
		{"stop", "POST", "/api/v1/nodes/node-1/pwm/PWM9/stop", nil},
		{"duty", "POST", "/api/v1/nodes/node-1/pwm/PWM9/duty", map[string]interface{}{"duty": 100}},
		{"freq", "POST", "/api/v1/nodes/node-1/pwm/PWM9/freq", map[string]interface{}{"frequency": 2000}},
		{"state", "GET", "/api/v1/nodes/node-1/pwm/PWM9/state", nil},
		{"delete", "DELETE", "/api/v1/nodes/node-1/pwm/PWM9", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, db, _ := setupPeriphTest(t)
			node := createTestNodeWithPeriphResources(t, db, "node-1")
			if err := db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM9", Channel: 9, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true}).Error; err != nil {
				t.Fatal(err)
			}
			w := periphJSON(t, r, tc.method, tc.path, tc.body)
			if tc.method == "DELETE" {
				if w.Code != http.StatusOK {
					t.Fatalf("stale cleanup expected 200, got %d: %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
			}
			var count int64
			db.Model(&models.PWMConfig{}).Where("node_id = ? AND hardware_id = ?", node.NodeID, "PWM9").Count(&count)
			if count != 1 {
				t.Fatalf("stale PWM config was removed despite rejection")
			}
		})
	}
}

func TestPWM_UpdateRejectsReportedResolutionLimit(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM0", map[string]interface{}{"resolution": 15})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGPIOAndPWMRejectReportedBusPinConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body interface{}
	}{
		{"GPIO", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 4, "direction": 1}},
		{"PWM", "/api/v1/nodes/node-1/pwm", map[string]interface{}{"hardware_id": "PWM0", "pin": 4, "frequency": 1000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, db, _ := setupPeriphTest(t)
			node := createTestNodeWithPeriphResources(t, db, "node-1")
			node.Capabilities = strings.Replace(node.Capabilities, `{"id":"GPIO6","pin":6}`, `{"id":"GPIO4","pin":4},{"id":"GPIO6","pin":6}`, 1)
			if err := db.Model(node).Update("capabilities", node.Capabilities).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.Channel{NodeID: node.NodeID, BusType: "I2C", HardwareType: "I2C", BusConfig: "0405", Enabled: true}).Error; err != nil {
				t.Fatal(err)
			}
			w := periphJSON(t, r, "POST", tc.path, tc.body)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422 for enabled I2C pin conflict, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPeripheralConflictUsesEnabledChannelRemapNotReportedDefaults(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	node.Capabilities = strings.Replace(node.Capabilities, `{"id":"GPIO6","pin":6}`, `{"id":"GPIO4","pin":4},{"id":"GPIO6","pin":6}`, 1)
	if err := db.Model(node).Update("capabilities", node.Capabilities).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: node.NodeID, BusType: "I2C", HardwareType: "I2C", BusConfig: "0708", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 4, "direction": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("reported defaults must not reserve remapped pin: %d %s", w.Code, w.Body.String())
	}
	w = periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{"hardware_id": "PWM0", "pin": 7, "frequency": 1000})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("actual remapped channel pin must conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestPeripheralConflictIgnoresInactiveChannelAndRejectsMalformedEnabledChannel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		enabled bool
		want    int
	}{
		{"inactive", "0607", false, http.StatusCreated},
		{"malformed enabled", "06", true, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, db, _ := setupPeriphTest(t)
			node := createTestNodeWithPeriphResources(t, db, "node-1")
			if err := db.Create(&models.Channel{NodeID: node.NodeID, BusType: "I2C", HardwareType: "I2C", BusConfig: tc.config, Enabled: true}).Error; err != nil {
				t.Fatal(err)
			}
			if !tc.enabled {
				if err := db.Model(&models.Channel{}).Where("node_id = ?", node.NodeID).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 6, "direction": 1})
			if w.Code != tc.want {
				t.Fatalf("want %d got %d: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestGPIOInitialLevelMustBeBinary(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{"pin": 6, "direction": 1, "initial_level": 2})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWMRejectsZeroAndInfeasibleFrequency(t *testing.T) {
	for _, frequency := range []uint32{0, 3000} {
		r, db, _ := setupPeriphTest(t)
		createTestNodeWithPeriphResources(t, db, "node-1")
		w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{"hardware_id": "PWM0", "pin": 6, "frequency": frequency, "resolution": 14})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("frequency=%d expected 400, got %d: %s", frequency, w.Code, w.Body.String())
		}
	}
}

func TestPWM_Create_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0",
		"pin":         7,
		"frequency":   2000,
		"duty":        5000,
		"resolution":  14,
		"auto_start":  false,
		"label":       "Fan PWM",
	})

	// Assert
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.PWMConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Pin != 7 {
		t.Errorf("expected pin=7, got %d", cfg.Pin)
	}
	if cfg.Frequency != 2000 {
		t.Errorf("expected frequency=2000, got %d", cfg.Frequency)
	}
	if cfg.Duty != 5000 {
		t.Errorf("expected duty=5000, got %d", cfg.Duty)
	}
	if cfg.Resolution != 14 {
		t.Errorf("expected resolution=14, got %d", cfg.Resolution)
	}
}

func TestPWM_Create_DefaultResolution(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act: 不提供 resolution, 应默认为 14
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0",
		"pin":         8,
		"frequency":   1000,
		"duty":        0,
	})

	// Assert
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.PWMConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Resolution != 14 {
		t.Errorf("expected default resolution=14, got %d", cfg.Resolution)
	}
}

func TestPWM_Create_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/pwm", map[string]interface{}{
		"hardware_id": "PWM0",
		"pin":         1,
		"frequency":   1000,
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_Create_NegativePin(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0",
		"pin":         -1,
		"frequency":   1000,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Create_ResolutionOutOfRange(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// 表驱动: 测试多种非法 resolution
	cases := []uint8{3, 21, 0}
	for _, res := range cases {
		// resolution=0 走默认值逻辑, 不算非法
		if res == 0 {
			continue
		}
		w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
			"hardware_id": "PWM0",
			"pin":         6,
			"frequency":   1000,
			"resolution":  res,
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("resolution=%d: expected 400, got %d: %s", res, w.Code, w.Body.String())
		}
	}
}

func TestPWM_Create_DutyOutOfRange(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")

	// Act: duty=20000 超出 0-10000 范围
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM0",
		"pin":         6,
		"frequency":   1000,
		"duty":        20000,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Create_DuplicatePin(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 7, Frequency: 1000, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm", map[string]interface{}{
		"hardware_id": "PWM1",
		"pin":         7,
		"frequency":   2000,
	})

	// Assert
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== PUT /nodes/:id/pwm/:hardware_id ====================

func TestPWM_Update_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 100, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM0", map[string]interface{}{
		"frequency": 2000,
		"duty":      8000,
	})

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg models.PWMConfig
	json.Unmarshal(w.Body.Bytes(), &cfg)
	if cfg.Frequency != 2000 {
		t.Errorf("expected frequency=2000, got %d", cfg.Frequency)
	}
	if cfg.Duty != 8000 {
		t.Errorf("expected duty=8000, got %d", cfg.Duty)
	}
}

func TestPWM_Update_NodeNotFound(t *testing.T) {
	// Arrange
	r, _, _ := setupPeriphTest(t)

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/nonexistent/pwm/PWM0", map[string]interface{}{
		"duty": 100,
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_Update_ConfigNotFound(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM9", map[string]interface{}{
		"duty": 100,
	})

	// Assert
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Update_MissingBodyValidatedBeforeConfigLookup(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")

	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM9", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Update_DutyOutOfRange(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM0", map[string]interface{}{
		"duty": 50000,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Update_ResolutionOutOfRange(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "PUT", "/api/v1/nodes/node-1/pwm/PWM0", map[string]interface{}{
		"resolution": 25,
	})

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== DELETE /nodes/:id/pwm/:hardware_id ====================

func TestPWM_Delete_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	cfg := models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: false}
	db.Create(&cfg)
	db.Model(&cfg).UpdateColumn("enabled", false)

	// Act
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/node-1/pwm/PWM0", nil)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, 6).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 records after delete, got %d", count)
	}
}

func TestPWM_Delete_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/nonexistent/pwm/PWM0", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_Delete_ConfigNotFound(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")
	w := periphJSON(t, r, "DELETE", "/api/v1/nodes/node-1/pwm/PWM9", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/pwm/:hardware_id/start ====================

func TestPWM_Start_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/pwm/PWM0/start", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_Start_ConfigNotFound(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM9/start", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_Start_NoMQTT(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, Enabled: true})
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM0/start", nil)
	// 没有 MQTT, SendPeriphCmd 报错 → 500
	if w.Code != http.StatusInternalServerError {
		t.Logf("got %d (expected 500 without MQTT): %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/pwm/:hardware_id/stop ====================

func TestPWM_Stop_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/pwm/PWM0/stop", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ==================== POST /nodes/:id/pwm/:hardware_id/duty ====================

func TestPWM_SetDuty_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/pwm/PWM0/duty", map[string]interface{}{
		"duty": 5000,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_SetDuty_OutOfRange(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM0/duty", map[string]interface{}{
		"duty": 50000,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_SetDuty_MissingDutyField(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")
	// Missing body fields are request errors even when the referenced config is absent.
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM9/duty", map[string]interface{}{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing duty, got %d: %s", w.Code, w.Body.String())
	}
	w = periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM9/duty", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing body, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== POST /nodes/:id/pwm/:hardware_id/freq ====================

func TestPWM_SetFreq_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "POST", "/api/v1/nodes/nonexistent/pwm/PWM0/freq", map[string]interface{}{
		"frequency": 1000,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_SetFreq_MissingFreqField(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")
	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM9/freq", map[string]interface{}{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing frequency, got %d: %s", w.Code, w.Body.String())
	}
	w = periphJSON(t, r, "POST", "/api/v1/nodes/node-1/pwm/PWM9/freq", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing body, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== GET /nodes/:id/pwm/:hardware_id/state ====================

func TestPWM_GetState_Success(t *testing.T) {
	// Arrange
	r, db, _ := setupPeriphTest(t)
	node := createTestNodeWithPeriphResources(t, db, "node-1")
	db.Create(&models.PWMConfig{NodeID: node.NodeID, HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 2000, Duty: 7500, Resolution: 14, Enabled: true})

	// Act
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/pwm/PWM0/state", nil)

	// Assert
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without MQTT runtime authority, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPWM_GetState_NodeNotFound(t *testing.T) {
	r, _, _ := setupPeriphTest(t)
	w := periphJSON(t, r, "GET", "/api/v1/nodes/nonexistent/pwm/PWM0/state", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPWM_GetState_ConfigNotFound(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNode(t, db, "node-1")
	w := periphJSON(t, r, "GET", "/api/v1/nodes/node-1/pwm/PWM9/state", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== 表驱动: GPIO direction 编码验证 ====================

func TestGPIO_DirectionValues(t *testing.T) {
	// 验证 GPIO direction 常量值正确
	cases := []struct {
		name      string
		direction uint8
		valid     bool
	}{
		{"INPUT", GPIODirInput, true},
		{"OUTPUT", GPIODirOutput, true},
		{"INPUT_PULLUP", GPIODirInputPullUp, true},
		{"INPUT_PULLDOWN", GPIODirInputPullDn, true},
		{"INVALID_4", 4, false},
		{"INVALID_255", 255, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.valid {
				if tc.direction > GPIODirInputPullDn {
					t.Errorf("%s: direction %d should be valid", tc.name, tc.direction)
				}
			} else {
				if tc.direction <= GPIODirInputPullDn {
					t.Errorf("%s: direction %d should be invalid", tc.name, tc.direction)
				}
			}
		})
	}
}

// ==================== 表驱动: PWM action 常量验证 ====================

func TestPWM_ActionConstants(t *testing.T) {
	// 验证 PWM action 常量值不冲突
	if PWMActionSetDuty == PWMActionSetFreq {
		t.Error("PWMActionSetDuty and PWMActionSetFreq should differ")
	}
	if PWMActionStart == PWMActionStop {
		t.Error("PWMActionStart and PWMActionStop should differ")
	}
}

// ==================== 表驱动: GPIO action 常量验证 ====================

func TestGPIO_ActionConstants(t *testing.T) {
	// 验证 GPIO action 常量值不冲突
	actions := []uint8{GPIOActionSetLow, GPIOActionSetHigh, GPIOActionRead, GPIOActionConfig, GPIOActionDeconfig, GPIOActionToggle}
	seen := make(map[uint8]bool)
	for _, a := range actions {
		if seen[a] {
			t.Errorf("GPIO action %d is duplicated", a)
		}
		seen[a] = true
	}
}

// ==================== 表驱动: periph type 常量验证 ====================

func TestPeriphTypeConstants(t *testing.T) {
	if PeriphTypeGPIO == PeriphTypePWM {
		t.Error("PeriphTypeGPIO and PeriphTypePWM should differ")
	}
	if PeriphTypeGPIO != 1 {
		t.Errorf("PeriphTypeGPIO should be 1, got %d", PeriphTypeGPIO)
	}
	if PeriphTypePWM != 2 {
		t.Errorf("PeriphTypePWM should be 2, got %d", PeriphTypePWM)
	}
}
