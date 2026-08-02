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
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil, registry)
	registerEdgeDeviceRoutes(v1, db, mgr, registry, ControlPolicy{allowUnsafeLegacyForTests: true})
	return r, db
}

// ==================== EdgeDevice Create Tests ====================

func TestEdgeDevice_Create_Success(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "i2c", Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":             "New Device",
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
	if dev.IntervalMs != 3000 {
		t.Errorf("expected interval_ms 3000, got %d", dev.IntervalMs)
	}
}

func TestEdgeDevice_Create_PreservesExplicitZeroInterval(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Rain", DeviceType: "sn3001_rain", HardwareType: "uart", Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "No schedule", "node_id": "NODE001", "channel_id": 1,
		"device_config_id": 1, "enabled": true, "interval_ms": 0,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "No schedule")
	if dev.IntervalMs != 0 {
		t.Fatalf("explicit interval_ms=0 was replaced with %d", dev.IntervalMs)
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

func TestEdgeDevice_Create_RequiresActiveCompatibleDeviceConfig(t *testing.T) {
	tests := []struct {
		name            string
		configStatus    string
		configHardware  string
		channelHardware string
		channelBusType  string
		includeConfigID bool
		wantStatus      int
	}{
		{name: "missing device config", channelHardware: "I2C", wantStatus: http.StatusBadRequest},
		{name: "inactive device config", configStatus: "inactive", configHardware: "i2c", channelHardware: "I2C", includeConfigID: true, wantStatus: http.StatusBadRequest},
		{name: "hardware mismatch", configStatus: "active", configHardware: "uart", channelHardware: "I2C", includeConfigID: true, wantStatus: http.StatusBadRequest},
		{name: "inconsistent channel metadata", configStatus: "active", configHardware: "i2c", channelHardware: "UART", channelBusType: "I2C", includeConfigID: true, wantStatus: http.StatusBadRequest},
		{name: "compatible config derives type", configStatus: "active", configHardware: "i2c", channelHardware: "I2C", includeConfigID: true, wantStatus: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
			channelBusType := tt.channelBusType
			if channelBusType == "" {
				channelBusType = tt.channelHardware
			}
			db.Create(&models.Channel{NodeID: "NODE001", HardwareType: tt.channelHardware, BusType: channelBusType, Enabled: true})
			if tt.configStatus != "" {
				db.Create(&models.DeviceConfig{Name: "Configured sensor", DeviceType: "configured_type", HardwareType: tt.configHardware, Status: tt.configStatus})
			}

			body := map[string]interface{}{
				"name": "Configured device", "node_id": "NODE001", "channel_id": 1,
			}
			if tt.includeConfigID {
				body["device_config_id"] = 1
			}
			payload, _ := json.Marshal(body)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}
			if tt.wantStatus == http.StatusCreated {
				var created models.EdgeDevice
				if err := db.First(&created, "name = ?", "Configured device").Error; err != nil {
					t.Fatalf("load created device: %v", err)
				}
				if created.DeviceConfigID != 1 || created.Type != "configured_type" {
					t.Fatalf("expected config binding and derived type, got config=%d type=%q", created.DeviceConfigID, created.Type)
				}
			}
		})
	}
}

func TestEdgeDevice_CreateRejectsPeripheralChannelBinding(t *testing.T) {
	for _, busType := range []string{"GPIO", "PWM"} {
		t.Run(busType, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
			db.Create(&models.Channel{NodeID: "NODE001", HardwareType: busType, BusType: busType, Enabled: true})
			body, _ := json.Marshal(map[string]interface{}{
				"name": "bad binding", "type": "temperature", "node_id": "NODE001", "channel_id": 1,
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEdgeDeviceExecuteReadIsRetiredInFavorOfOperationAPI(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	if err := db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	operations := json.RawMessage(`{"read":{"type":"read","command_template":"010300000002C40B","read_size":9}}`)
	if err := db.Create(&models.DeviceConfig{Name: "Readable", DeviceType: "test", HardwareType: "uart", Operations: operations}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{Name: "Device", Type: "test", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/execute", bytes.NewReader([]byte(`{"operation":"read","params":{}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("expected V2 operation migration response, got %d: %s", w.Code, w.Body.String())
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

func TestEdgeDevice_InitRequiresEnabledEdgeAndMatchingEnabledTransportChannel(t *testing.T) {
	tests := []struct {
		name    string
		edge    models.EdgeDevice
		channel models.Channel
	}{
		{"disabled edge", models.EdgeDevice{Name: "sensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, Enabled: false}, models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true}},
		{"disabled channel", models.EdgeDevice{Name: "sensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, Enabled: true}, models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: false}},
		{"node mismatch", models.EdgeDevice{Name: "sensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, Enabled: true}, models.Channel{NodeID: "NODE002", HardwareType: "I2C", BusType: "I2C", Enabled: true}},
		{"peripheral PWM channel", models.EdgeDevice{Name: "sensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, Enabled: true}, models.Channel{NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"})
			if tt.channel.NodeID == "NODE002" {
				db.Create(&models.Node{NodeID: "NODE002", Name: "Node 2", Status: "online"})
			}
			db.Create(&tt.channel)
			db.Create(&tt.edge)
			if tt.name == "disabled channel" {
				db.Model(&models.Channel{}).Where("id = ?", tt.channel.ID).Update("enabled", false)
			}
			if tt.name == "disabled edge" {
				db.Model(&models.EdgeDevice{}).Where("id = ?", tt.edge.ID).Update("enabled", false)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/init", nil)
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			var got models.EdgeDevice
			db.First(&got, tt.edge.ID)
			if got.InitState == "running" {
				t.Fatal("rejected init was marked running")
			}
		})
	}
}

func TestEdgeDevice_Create_WithModbusType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "SN3000", DeviceType: "sn3000", HardwareType: "uart"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":             "Wind Sensor",
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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "temperature", HardwareType: "i2c", Status: "active"})
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

func TestEdgeDevice_Update_RejectsInactiveOrIncompatibleDeviceConfig(t *testing.T) {
	for _, tt := range []struct {
		name           string
		configStatus   string
		configHardware string
	}{
		{name: "inactive", configStatus: "inactive", configHardware: "i2c"},
		{name: "incompatible hardware", configStatus: "active", configHardware: "uart"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
			db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
			db.Create(&models.DeviceConfig{Name: "Current", DeviceType: "current", HardwareType: "i2c", Status: "active"})
			db.Create(&models.DeviceConfig{Name: "Candidate", DeviceType: "candidate", HardwareType: tt.configHardware, Status: tt.configStatus})
			db.Create(&models.EdgeDevice{Name: "Device", Type: "current", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

			payload, _ := json.Marshal(map[string]interface{}{"device_config_id": 2})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			var updated models.EdgeDevice
			db.First(&updated, 1)
			if updated.DeviceConfigID != 1 || updated.Type != "current" {
				t.Fatalf("rejected update changed config=%d type=%q", updated.DeviceConfigID, updated.Type)
			}
		})
	}
}

func TestEdgeDevice_Update_RejectsTypeOverrideForConfiguredDevice(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Configured", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	payload, _ := json.Marshal(map[string]interface{}{"type": "spoofed_type"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.EdgeDevice
	db.First(&updated, 1)
	if updated.Type != "bmp280" {
		t.Fatalf("type override changed configured device type to %q", updated.Type)
	}
}

func TestEdgeDevice_Create_RejectsTypeOverrideForConfiguredDevice(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Configured", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})

	payload, _ := json.Marshal(map[string]interface{}{
		"name": "Spoofed", "type": "spoofed_type", "node_id": "NODE001",
		"channel_id": 1, "device_config_id": 1,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "Spoofed").Count(&count)
	if count != 0 {
		t.Fatalf("type-rejected request should not create a device, found %d rows", count)
	}
}

func TestEdgeDevice_Update_RejectsChannelIncompatibleWithBoundDeviceConfig(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Configured", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	payload, _ := json.Marshal(map[string]interface{}{"channel_id": 2})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.EdgeDevice
	db.First(&updated, 1)
	if updated.ChannelID != 1 {
		t.Fatalf("rejected update changed configured device channel to %d", updated.ChannelID)
	}
}

func TestEdgeDevice_Update_RejectsNodeMoveIncompatibleWithBoundDeviceConfig(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Current", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "Target", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE002", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Configured", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	payload, _ := json.Marshal(map[string]interface{}{"node_id": "NODE002", "channel_id": 2})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.EdgeDevice
	db.First(&updated, 1)
	if updated.NodeID != "NODE001" || updated.ChannelID != 1 {
		t.Fatalf("rejected move changed node=%q channel=%d", updated.NodeID, updated.ChannelID)
	}
}

func TestEdgeDevice_UpdateRejectsPeripheralChannelMassBind(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "temperature", NodeID: "NODE001", ChannelID: 1})
	body, _ := json.Marshal(map[string]interface{}{"channel_id": 2})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/edge-devices/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.ChannelID != 1 {
		t.Fatalf("rejected mass-bind changed channel_id to %d", dev.ChannelID)
	}
}

func TestEdgeDeviceUpdateRejectsChannelFromDifferentNode(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "Node 2", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE002", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "test", NodeID: "NODE001", ChannelID: 1, Enabled: true})
	body, _ := json.Marshal(map[string]interface{}{"channel_id": 2})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
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

func TestEdgeDevice_Operations_PostIsNotProvidedByLegacyRoutes(t *testing.T) {
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

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without the Phase 1 operation service, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_OperationsHistoryIsNotProvidedByLegacyRoutes(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/1/operations/history?limit=20", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without the Phase 1 operation service, got %d: %s", w.Code, w.Body.String())
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

func TestEdgeDeviceExecuteRejectsLegacyPeripheralDisabledAndStaleChannel(t *testing.T) {
	for _, tt := range []struct {
		name     string
		channel  models.Channel
		edgeNode string
	}{
		{name: "GPIO", channel: models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true}, edgeNode: "NODE001"},
		{name: "PWM", channel: models.Channel{NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true}, edgeNode: "NODE001"},
		{name: "disabled UART", channel: models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "force-disabled"}, edgeNode: "NODE001"},
		{name: "stale ownership", channel: models.Channel{NodeID: "NODE002", HardwareType: "UART", BusType: "UART", Enabled: true}, edgeNode: "NODE001"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"})
			db.Create(&models.Node{NodeID: "NODE002", Name: "Node 2", Status: "online"})
			operations := json.RawMessage(`{"set":{"type":"write","command_template":"01"}}`)
			dc := models.DeviceConfig{Name: "Writable", DeviceType: "test", HardwareType: "uart", Operations: operations}
			db.Create(&dc)
			db.Create(&tt.channel)
			if tt.channel.HardwareID == "force-disabled" {
				db.Model(&models.Channel{}).Where("id = ?", tt.channel.ID).UpdateColumn("enabled", false)
			}
			edge := models.EdgeDevice{Name: "Device", Type: "test", NodeID: tt.edgeNode, ChannelID: tt.channel.ID, DeviceConfigID: dc.ID, Enabled: true}
			db.Create(&edge)

			body, _ := json.Marshal(map[string]interface{}{"operation": "set", "params": map[string]interface{}{}})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEdgeDeviceChangeAddressRejectsLegacyPeripheralAndStaleChannel(t *testing.T) {
	for _, tt := range []struct {
		name     string
		channel  models.Channel
		edgeNode string
	}{
		{name: "GPIO", channel: models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true}, edgeNode: "NODE001"},
		{name: "PWM", channel: models.Channel{NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true}, edgeNode: "NODE001"},
		{name: "disabled UART", channel: models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "force-disabled"}, edgeNode: "NODE001"},
		{name: "stale ownership", channel: models.Channel{NodeID: "NODE002", HardwareType: "UART", BusType: "UART", Enabled: true}, edgeNode: "NODE001"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, db := setupEdgeDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Node 1", Status: "online"})
			db.Create(&models.Node{NodeID: "NODE002", Name: "Node 2", Status: "online"})
			db.Create(&tt.channel)
			if tt.channel.HardwareID == "force-disabled" {
				db.Model(&models.Channel{}).Where("id = ?", tt.channel.ID).UpdateColumn("enabled", false)
			}
			db.Create(&models.EdgeDevice{Name: "Device", Type: "test", NodeID: tt.edgeNode, ChannelID: tt.channel.ID, Enabled: true})
			body, _ := json.Marshal(map[string]interface{}{"new_address": 5, "command": "010300000001840A"})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/change-address", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
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

// ==================== DeviceConfig-optional (driver fallback) tests ====================

func TestEdgeDevice_Create_WithoutDeviceConfigID(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "DriverBacked",
		"node_id":     "NODE001",
		"channel_id":  1,
		"type":        "bmp280",
		"hardware_id": "0x76",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "DriverBacked").Error; err != nil {
		t.Fatalf("load created device: %v", err)
	}
	if dev.Type != "bmp280" {
		t.Errorf("expected type bmp280, got %q", dev.Type)
	}
	if dev.DeviceConfigID != 0 {
		t.Errorf("expected DeviceConfigID=0, got %d", dev.DeviceConfigID)
	}
	// ConfigTemplate should be auto-created via the driver's CommandTemplates
	var tmpl models.ConfigTemplate
	if err := db.First(&tmpl, "node_id = ?", "NODE001").Error; err != nil {
		t.Fatalf("expected ConfigTemplate created via driver for bmp280: %v", err)
	}
	if tmpl.WriteData == "" {
		t.Error("expected non-empty write_data for bmp280 driver-backed template")
	}
}

func TestEdgeDevice_Create_WithoutDeviceConfigID_UnknownType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name":       "UnknownDriver",
		"node_id":    "NODE001",
		"channel_id": 1,
		"type":       "not_a_real_driver",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unregistered type, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "UnknownDriver").Count(&count)
	if count != 0 {
		t.Fatalf("device with unregistered type should not be created, found %d rows", count)
	}
}

func TestEdgeDevice_Create_WithoutDeviceConfigID_MissingType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	// No device_config_id and no type — should be rejected with 400.
	body, _ := json.Marshal(map[string]interface{}{
		"name":       "NoTypeNoConfig",
		"node_id":    "NODE001",
		"channel_id": 1,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "NoTypeNoConfig").Count(&count)
	if count != 0 {
		t.Fatalf("device without type or config should not be created, found %d rows", count)
	}
}

func TestEdgeDevice_Create_WithoutDeviceConfigID_AllowsTypeForDriverBacked(t *testing.T) {
	// When device_config_id is 0, supplying type must NOT be rejected (G1 inversion).
	// Sanity: ensure that when device_config_id is set, type is still rejected.
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Cfg", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Spoof", "type": "bmp280", "node_id": "NODE001",
		"channel_id": 1, "device_config_id": 1,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for type+device_config_id combo, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEdgeDevice_Update_SwitchesToDriverBackedWhenDeviceConfigIDSetToZero(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Cfg", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Dev", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

	// Switch to driver-backed by setting device_config_id=0 and providing type.
	payload, _ := json.Marshal(map[string]interface{}{
		"device_config_id": 0,
		"type":             "bmp280",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated models.EdgeDevice
	db.First(&updated, 1)
	if updated.DeviceConfigID != 0 {
		t.Errorf("expected DeviceConfigID=0 after switch, got %d", updated.DeviceConfigID)
	}
	if updated.Type != "bmp280" {
		t.Errorf("expected type bmp280, got %q", updated.Type)
	}
}

// F: inline channel creation — when channel_id is absent and a channel sub-object
// is provided, the backend should create the channel and the edge device inside
// the same transaction, eliminating orphan-channel risk.
func TestEdgeDevice_Create_WithInlineChannel(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	// No pre-existing channel — the inline path should create one

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "InlineChannel",
		"node_id":     "NODE001",
		"type":        "bmp280",
		"hardware_id": "0x76",
		"channel": map[string]interface{}{
			"hardware_type": "i2c",
			"hardware_id":   "0x76",
			"config": map[string]interface{}{
				"interval_ms": 1000,
				"device_type": "bmp280",
			},
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the edge device was created
	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "InlineChannel")
	if dev.ID == 0 {
		t.Fatalf("edge device not created")
	}
	if dev.Type != "bmp280" {
		t.Errorf("expected type bmp280, got %s", dev.Type)
	}

	// Verify the inline channel was created and linked
	var ch models.Channel
	db.First(&ch, dev.ChannelID)
	if ch.ID == 0 {
		t.Fatalf("inline channel not created")
	}
	if ch.HardwareType != "i2c" {
		t.Errorf("expected hardware_type i2c, got %s", ch.HardwareType)
	}
	if ch.NodeID != "NODE001" {
		t.Errorf("expected node_id NODE001, got %s", ch.NodeID)
	}
}

// ==================== Duplicate-device uniqueness guard ====================

// 同一通道(channel_id)+同从机地址(hardware_id)+同型号(type)不可重复;
// 但同通道不同从机地址(SPI 多 CS / I2C 多地址)允许。
func TestEdgeDevice_Create_RejectsDuplicateTypeAtSameAddress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Existing", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Dup", "node_id": "NODE001", "channel_id": 1,
		"type": "bmp280", "hardware_id": "0x76",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate channel+hardware_id+type, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "Dup").Count(&count)
	if count != 0 {
		t.Fatalf("duplicate device should not be created, found %d rows", count)
	}
}

// 多从机总线: 同通道同型号但不同从机地址是合法的(如 SPI 多 CS、I2C 多地址)。
func TestEdgeDevice_Create_AllowsSameTypeAtDifferentAddress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Slave-A", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Slave-B", "node_id": "NODE001", "channel_id": 1,
		"type": "bmp280", "hardware_id": "0x77",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for different address on multi-drop bus, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "Slave-B").Error; err != nil {
		t.Fatalf("expected Slave-B created on multi-drop bus: %v", err)
	}
}

// 无地址设备: hardware_id 为空时按 (channel_id, type) 去重。
func TestEdgeDevice_Create_RejectsDuplicateTypeWhenAddressEmpty(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Existing", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "DupNoAddr", "node_id": "NODE001", "channel_id": 1,
		"type": "sn3001_rain",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate channel+type with empty address, got %d: %s", w.Code, w.Body.String())
	}
}

// 同通道同地址但不同型号是合法的(守卫只按 channel+type+hardware_id 三元组去重,
// 不同 type 不冲突;此处验证守卫不误伤不同 type)。
func TestEdgeDevice_Create_AllowsDifferentTypeAtSameAddress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "TempSensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "HumiditySensor", "node_id": "NODE001", "channel_id": 1,
		"type": "lk_th01", "hardware_id": "0x76",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for different type at same address, got %d: %s", w.Code, w.Body.String())
	}
}

// 纯空格 hardware_id 应走空地址分支(TrimSpace 后为空),与已有空地址同型号设备冲突。
func TestEdgeDevice_Create_TreatsWhitespaceAddressAsEmpty(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Existing", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "", Enabled: true})

	body, _ := json.Marshal(map[string]interface{}{
		"name": "DupWhitespace", "node_id": "NODE001", "channel_id": 1,
		"type": "sn3001_rain", "hardware_id": "   ",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only address colliding with empty-address device, got %d: %s", w.Code, w.Body.String())
	}
}

// UPDATE 路径碰撞: PUT 把设备移到另一设备已占的 (channel, type, hardware_id) 应被拒。
func TestEdgeDevice_Update_RejectsCollisionWithOtherDevice(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "DeviceA", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "DeviceB", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x77", Enabled: true})

	// 把 DeviceB 的地址改成 DeviceA 的 0x76 → 碰撞
	body, _ := json.Marshal(map[string]interface{}{"hardware_id": "0x76"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/2", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for update colliding with another device, got %d: %s", w.Code, w.Body.String())
	}
	// DeviceB 地址不应被改
	var devB models.EdgeDevice
	db.First(&devB, 2)
	if devB.HardwareID != "0x77" {
		t.Errorf("DeviceB hardware_id should remain 0x77, got %q", devB.HardwareID)
	}
}

// UPDATE 自身不碰撞: 重存相同三元组不应误判(excludeID 排除自身)。
func TestEdgeDevice_Update_AllowsSelfResave(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "DeviceA", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})

	// 只改名字,三元组不变 → 不应碰撞自身
	body, _ := json.Marshal(map[string]interface{}{"name": "RenamedA"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for self-resave (no collision with self), got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Name != "RenamedA" {
		t.Errorf("expected name RenamedA, got %q", dev.Name)
	}
}
