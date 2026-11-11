package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
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
	return setupEdgeDeviceTestWithRegistry(t, nil)
}

// setupEdgeDeviceTestWithRegistry 在标准测试环境上允许调用方追加自定义驱动。
//
// 为什么需要：某些用例要验证"已知但不可调度的命令 id 被拒"，这需要一个真正
// 声明了 Schedulable=false 模板的驱动。用内置驱动当这种 fixture 是脆弱的 ——
// 演进方案 C6 曾让 techfine_inverter 的模板集合清空，2026-10-03 又因现场缺陷
// 恢复为全部 schedulable；两次变动都会让该用例静默地失去被测语义（仍通过，
// 但走的是"未知 id"分支）。显式 stub 让 fixture 的生命周期与被测契约绑定。
//
// customize 为 nil 时行为与 setupEdgeDeviceTest 完全一致。
func setupEdgeDeviceTestWithRegistry(t *testing.T, customize func(*drivers.Registry)) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		// 2026-11-11: models.DeviceData 已删除（device_data 整表退役）。原本紧跟
		// DeviceConfig 的 &models.DeviceData{} 一并移除 —— 不要再从 git history 把
		// 它抄回来，模型都不存在了。
		&models.EdgeDevice{}, &models.DeviceConfig{},
		&models.UnifiedData{}, &models.User{}, &models.OTATask{},
		&models.Firmware{}, &models.Vendor{}, &models.Notification{},
		&models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{},
		&models.PendingWriteRecord{},
		// 2026-10-03：向导内联建 UART 通道现在会按能力补齐 bus_config，
		// 于是 validateChannelPeripheralConflicts 会真的执行（旧实现 bus_config 为空时跳过），
		// 必须像 setupDeviceTest 一样迁移这两张表，否则报 "no such table: gpio_configs"。
		&models.GPIOConfig{}, &models.PWMConfig{},
		// 2026-10-25：合并历史的台账/投影表（sensor_field_obs / data_sources）也要在
		// 测试库里存在，否则 merge-history 的台账迁移分支永远走「跳过」，测试假绿。
		&models.SensorFieldObs{}, &models.DataSource{},
		// 2026-11-11：帧表进测试库。device_data 整表退役后，data-summary 的行数估算
		// 与时间范围、merge-history 的时序迁移、purge 的清理对象**全部只剩帧表**
		// （datalifecycle.TimeseriesTables）。不建它，这些用例的断言会全部退化成
		// "0 行"，看起来像功能坏了。
		//
		// ⚠ 关于「AutoMigrate 帧表会不会加 id 列」：不会。模型主键 tag 已显式声明
		// (device_id, ts, seq) 三列，GORM 建出的是 PRIMARY KEY (device_id, ts, seq)、
		// 没有 id 列（已用 sqlite_master 的 DDL 实测确认）。这与生产分区母表一致。
		&models.TelemetryFrame{}, &models.TelemetrySlot{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	if customize != nil {
		customize(registry)
	}
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, registry)
	registerEdgeDeviceRoutes(v1, db, mgr, registry)
	return r, db
}

// ==================== EdgeDevice Create Tests ====================

func TestEdgeDevice_Create_Success(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})

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
				"name": "bad binding", "type": "bmp280", "node_id": "NODE001", "channel_id": 1,
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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

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
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1})
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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Dev1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})
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
	db.Create(&models.DeviceConfig{Name: "Temp Sensor", DeviceType: "bmp280", HardwareType: "uart"})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, DeviceConfigID: 1})

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

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for delete of nonexistent, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== EdgeDevice Sub-resource Tests ====================

// 2026-11-11: TestEdgeDevice_LatestData_DeletedDeviceStillReadable 与
// TestEdgeDevice_Data_DeletedDeviceStillReadable 已删除。
//
// 它们断言的端点 GET /:id/latest-data 与 GET /:id/data 都随 device_data 整表退役
// 被删除（原始数据退役方案 §1.3 读路径表第 1/2 行）。端点不存在 ⇒ 用例只能得到 404，
// 改写它没有意义。
//
// 这两条用例原本守的**语义**（软删设备的历史仍可读，历史不因设备行软删而消失）依然
// 成立，但已经由别的用例覆盖：/devices/:id/history（帧表）与 /:id/latest-values 都是
// 按 device_id 直查、不看设备行的 deleted_at。若将来要为新端点补回归，请写到对应新
// 端点的用例里，而不是复活这两个端点。

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

	// 409 而非 400：设计文档 §7.2「三元重复 → 409」，§1.1 冲突文案亦是 409
	// （前端按 409 渲染"该位置已有同型号设备"）。2026-10-11 真机验证改正。
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate channel+hardware_id+type, got %d: %s", w.Code, w.Body.String())
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

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate channel+type with empty address, got %d: %s", w.Code, w.Body.String())
	}
}

// 同通道同地址但不同型号是合法的(守卫只按 channel+type+hardware_id 三元组去重,
// 不同 type 不冲突;此处验证守卫不误伤不同 type)。
func TestEdgeDevice_Create_AllowsDifferentTypeAtSameAddress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "TempSensor", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", Enabled: true})

	// 2026-10-25：create 路径补齐「驱动声明的总线类型必须覆盖通道」门禁后，原先
	// 用 lk_th01（只支持 uart）建在 I2C 通道上的 fixture 会被正确拒绝。本用例要
	// 验证的是**唯一性守卫不误伤不同 type**，换成同样支持 I2C 的 generic_i2c。
	body, _ := json.Marshal(map[string]interface{}{
		"name": "HumiditySensor", "node_id": "NODE001", "channel_id": 1,
		"type": "generic_i2c", "hardware_id": "0x76",
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

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for whitespace-only address colliding with empty-address device, got %d: %s", w.Code, w.Body.String())
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

// ==================== 数据生命周期: delete_data / data-summary / restore ====================

// 2026-10-23 语义变更（逻辑设备概念层移除，.contract/logical-device-removal.md）:
//   - 删除时「同时删除数据」的标记从 logical_devices.purge_requested 搬到
//     edge_devices.data_purge_requested（设备自己就是数据的所有者）；
//   - /logical-device-info 被 /data-summary 取代（只需要知道「有多少行、什么时间范围」）；
//   - 新增 /deleted、/:id/restore、/:id/merge-history，实现用户真正要的
//     「换节点后历史不丢」（原地复用 / 恢复 / 历史合并三条路径）。

func TestEdgeDevice_Delete_DefaultKeepsDataNoPurgeFlag(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/edge-devices/1", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 软删行仍在 (Unscoped 可见)，且未请求删数据 ⇒ 标记为 false。
	var inst models.EdgeDevice
	if err := db.Unscoped().First(&inst, 1).Error; err != nil {
		t.Fatalf("soft-deleted instance must still exist: %v", err)
	}
	if !inst.DeletedAt.Valid {
		t.Error("device row must be soft-deleted")
	}
	if inst.DataPurgeRequested {
		t.Error("data_purge_requested must stay false without delete_data")
	}
}

func TestEdgeDevice_Delete_DeleteDataSetsPurgeRequestedOnly(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76"})
	// 该设备名下有数据行 — API 事务内不得删除（只置标记，删数据由后台任务做）。
	// 2026-11-11: 第二行原本是 device_data，随该表退役改成帧表（帧表是 purge
	// 现在唯一会清的时序表，见 telemetry_frame_lifecycle.go 的 TimeseriesTables）。
	db.Create(&models.UnifiedData{DeviceID: 1, SensorName: "voltage", Value: 12})
	seedFrameTable(t, db)
	seedFrameRow(t, db, 1, time.Now())

	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/edge-devices/1?delete_data=true", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var inst models.EdgeDevice
	db.Unscoped().First(&inst, 1)
	if !inst.DataPurgeRequested {
		t.Error("data_purge_requested must be TRUE with delete_data=true")
	}
	// 数据行仍在（异步 purge，不在 API 事务里删）。
	var unified, frames int64
	db.Model(&models.UnifiedData{}).Count(&unified)
	db.Model(&models.TelemetryFrame{}).Count(&frames)
	if unified != 1 || frames != 1 {
		t.Errorf("data must survive the API transaction, got unified=%d telemetry_frame=%d", unified, frames)
	}
}

// rr2Do 发一个带 JWT 的测试请求（query/body 可空）。
func rr2Do(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

// rr2SummaryItem / rr2MergeResp 是 data-summary / merge-history 的响应形状。
type rr2SummaryItem struct {
	SourceID   uint       `json:"source_id"`
	Rows       *int64     `json:"rows"`
	FirstTime  *time.Time `json:"first_time"`
	LastTime   *time.Time `json:"last_time"`
	State      string     `json:"state"`
	DeviceName string     `json:"device_name"`
	DeviceType string     `json:"device_type"`
}

type rr2MergeResp struct {
	RowsMigrated float64 `json:"rows_migrated"`
	Sources      []struct {
		ID         uint   `json:"id"`
		State      string `json:"state"`
		DeviceType string `json:"device_type"`
	} `json:"sources"`
	Warnings []string `json:"warnings"`
}

// ==================== data-summary：目标与源必须分开 ====================

// TestEdgeDevice_DataSummary — /data-summary 回答「这台设备有多少行数据、覆盖
// 什么时间范围」。2026-10-25 起响应拆成 target / sources 两组：删除确认弹窗读
// target，合并弹窗只读 sources，两者绝不混用。
func TestEdgeDevice_DataSummary(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76"})
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	newer := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	// ============ 2026-11-11：三行夹具改为两帧 ============
	//
	// 原夹具：unified_data 两行（old/newer）+ device_data 一行（newer）。
	// 两张原始表先后退役后，data-summary 的「行数」与「时间范围」**都只从帧表读**
	// （handler 的 tsTables 走 datalifecycle.ExistingTimeseriesTables；行数走
	// row_estimate.EstimateDeviceRows）。所以夹具必须换成帧行，否则时间范围会只剩
	// 一份来源、行数恒为 0 —— 那不是"功能坏了"，是夹具指向了已退役的表。
	//
	// 两帧分别落在 old / newer：既保证 target_rows = 2，也保证
	// first_time = old、last_time = newer（时间范围跨两端这条语义原样保留）。
	seedFrameTable(t, db)
	seedFrameAt(t, db, 1, old)
	seedFrameAt(t, db, 1, newer)

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/1/data-summary", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Target     rr2SummaryItem   `json:"target"`
			Sources    []rr2SummaryItem `json:"sources"`
			TargetRows *int64           `json:"target_rows"`
			SourceRows *int64           `json:"source_rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Data.Target.SourceID != 1 {
		t.Errorf("target.source_id = %d, want 1", resp.Data.Target.SourceID)
	}
	// device_type 是合并弹窗做「同型号」校验/展示的依据（2026-10-25 产品重做）。
	if resp.Data.Target.DeviceType != "bms_jbd" {
		t.Errorf("target.device_type = %q, want bms_jbd", resp.Data.Target.DeviceType)
	}
	// 2026-11-11: 行数口径从「unified_data + device_data」改为「只数帧表」
	// （row_estimate.go 的 EstimateDeviceRows）。上面 seed 了两帧 ⇒ want 2。
	if resp.Data.TargetRows == nil || *resp.Data.TargetRows != 2 {
		t.Errorf("target_rows = %v, want 2 (帧表是唯一时序载体，unified_data/device_data 均已退役)", resp.Data.TargetRows)
	}
	if len(resp.Data.Sources) != 0 {
		t.Errorf("sources = %+v, want empty (没有传 source_ids)", resp.Data.Sources)
	}
	if resp.Data.SourceRows != nil {
		t.Errorf("source_rows = %v, want omitted when no source_ids", *resp.Data.SourceRows)
	}
	if resp.Data.Target.FirstTime == nil || !resp.Data.Target.FirstTime.Equal(old) {
		t.Errorf("target.first_time = %v, want %v", resp.Data.Target.FirstTime, old)
	}
	if resp.Data.Target.LastTime == nil || !resp.Data.Target.LastTime.Equal(newer) {
		t.Errorf("target.last_time = %v, want %v", resp.Data.Target.LastTime, newer)
	}
}

// TestEdgeDevice_DataSummary_SeparatesTargetFromSources 钉住旧缺陷：
// total_rows 曾把目标自己也加进去，前端「预计迁移 N 行」因此系统性虚高；
// 明细表里目标设备还被列成一行「源设备」。
func TestEdgeDevice_DataSummary_SeparatesTargetFromSources(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	db.Create(&models.EdgeDevice{Name: "Old", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	// 目标自己有 2 行（**不应**出现在 source_rows 里），源有 1 行。
	// 2026-11-11: 夹具从 unified_data 换成帧表（两张原始表均已退役，行数估算只数帧）。
	seedFrameTable(t, db)
	for i := 0; i < 2; i++ {
		seedFrameAt(t, db, 1, time.Now().Add(time.Duration(i)*time.Second))
	}
	seedFrameAt(t, db, 2, time.Now())

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/1/data-summary?source_ids=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Target     rr2SummaryItem   `json:"target"`
			Sources    []rr2SummaryItem `json:"sources"`
			TargetRows *int64           `json:"target_rows"`
			SourceRows *int64           `json:"source_rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Data.Sources) != 1 || resp.Data.Sources[0].SourceID != 2 {
		t.Fatalf("sources = %+v, want exactly [source_id=2]（目标不得混进 sources）", resp.Data.Sources)
	}
	if resp.Data.Sources[0].State != "live" {
		t.Errorf("source state = %q, want live", resp.Data.Sources[0].State)
	}
	if resp.Data.SourceRows == nil || *resp.Data.SourceRows != 1 {
		t.Errorf("source_rows = %v, want 1（只算源；目标自己的 2 行不得加进来）", resp.Data.SourceRows)
	}
	if resp.Data.TargetRows == nil || *resp.Data.TargetRows != 2 {
		t.Errorf("target_rows = %v, want 2", resp.Data.TargetRows)
	}
}

// TestEdgeDevice_DataSummary_MarksMissingSource — 没有设备行的源（孤儿历史）
// 用 state=missing 标出；这不是错误（合并历史的救援对象），但要可识别。
func TestEdgeDevice_DataSummary_MarksMissingSource(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	// 2026-11-11: 孤儿历史改成帧行（device_id=999 没有设备行 ⇒ 走 CountDeviceRowsCapped）。
	seedFrameTable(t, db)
	seedFrameAt(t, db, 999, time.Now())

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/1/data-summary?source_ids=999", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Sources []rr2SummaryItem `json:"sources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Data.Sources) != 1 {
		t.Fatalf("sources = %+v, want 1", resp.Data.Sources)
	}
	if resp.Data.Sources[0].State != "missing" {
		t.Errorf("state = %q, want missing（没有设备行，但有孤儿数据）", resp.Data.Sources[0].State)
	}
	if resp.Data.Sources[0].Rows == nil || *resp.Data.Sources[0].Rows != 1 {
		t.Errorf("rows = %v, want 1", resp.Data.Sources[0].Rows)
	}
}

func TestEdgeDevice_DataSummary_NotFound(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)
	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/999/data-summary", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== restore：校验 / 状态复位 / 与 purge 互斥 ====================

// TestEdgeDevice_Restore_RebindsNodeAndKeepsHistory — 用户真正需求的直接表达:
// 设备从节点 A 删掉、在节点 B 恢复，历史数据（device_id 不变）必须连续可查。
func TestEdgeDevice_Restore_RebindsNodeAndKeepsHistory(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "B", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE002", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1, HardwareID: ""}
	db.Create(&dev)
	db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "voltage", Value: 12})
	db.Delete(&dev) // 用户先删了设备（历史数据仍在）

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/deleted", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deleted list expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Data struct {
			Items []struct {
				ID uint `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(listResp.Data.Items) != 1 || listResp.Data.Items[0].ID != dev.ID {
		t.Fatalf("deleted items = %+v, want the soft-deleted device %d", listResp.Data.Items, dev.ID)
	}

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE002", "channel_id": 2})
	w = rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("restore expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var restored models.EdgeDevice
	db.First(&restored, dev.ID) // 默认 scope 能查到 ⇒ 已恢复
	if restored.NodeID != "NODE002" || restored.ChannelID != 2 {
		t.Errorf("restored binding = (%s,%d), want (NODE002,2)", restored.NodeID, restored.ChannelID)
	}
	if restored.Status != models.EdgeDeviceStatusPending {
		t.Errorf("restored status = %q, want pending (等待首个数据帧)", restored.Status)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", restored.ID).Count(&rows)
	if rows != 1 {
		t.Errorf("history rows = %d, want 1 (restore must keep history)", rows)
	}
}

// TestEdgeDevice_Restore_RejectsLiveDevice — 「恢复」不能被当成「改绑定」的后门。
func TestEdgeDevice_Restore_RejectsLiveDevice(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 1})
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a live device, got %d: %s", w.Code, w.Body.String())
	}
}

// TestEdgeDevice_Restore_RejectsPeripheralChannel — GPIO/PWM 是外设资源，不是
// 数据传输通道；旧实现只校验「通道属于节点」，可以把设备绑到 GPIO 上并「恢复成功」，
// 但设备永远采不到数据（静默失败）。
func TestEdgeDevice_Restore_RejectsPeripheralChannel(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true})
	dev := models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&dev)
	db.Delete(&dev)

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 2})
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for peripheral channel, got %d: %s", w.Code, w.Body.String())
	}
	var still models.EdgeDevice
	if err := db.Unscoped().First(&still, dev.ID).Error; err != nil || !still.DeletedAt.Valid {
		t.Fatalf("被拒绝的恢复必须保持软删状态（错误=%v）", err)
	}
}

// TestEdgeDevice_Restore_RejectsDisabledChannel — 停用通道上的设备永远不会被
// polling，等于恢复了一台「死」设备。
func TestEdgeDevice_Restore_RejectsDisabledChannel(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	// GORM 的 default:true 会把零值 false 覆盖成 true，必须建完再显式改停用。
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	if err := db.Model(&models.Channel{}).Where("id = ?", 2).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable channel: %v", err)
	}
	dev := models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&dev)
	db.Delete(&dev)

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 2})
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for disabled channel, got %d: %s", w.Code, w.Body.String())
	}
}

// TestEdgeDevice_Restore_RejectsPurgeInProgress — purge 已认领设备时不能恢复：
// 删除已不可撤销，恢复只会在删了一半的数据上制造「看起来恢复了」的假象。
func TestEdgeDevice_Restore_RejectsPurgeInProgress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&dev)
	db.Delete(&dev)
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Updates(map[string]interface{}{
			"data_purge_requested":  true,
			"data_purge_started_at": time.Now(),
		}).Error; err != nil {
		t.Fatalf("seed purge claim: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 1})
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 while purge in progress, got %d: %s", w.Code, w.Body.String())
	}
	var still models.EdgeDevice
	db.Unscoped().First(&still, dev.ID)
	if !still.DeletedAt.Valid || !still.DataPurgeRequested {
		t.Errorf("purge 认领期间的恢复被拒后，设备必须保持软删且标记不变")
	}
}

// TestEdgeDevice_Restore_ResetsRuntimeState — 恢复复用同一个 edge_device id，必须
// 把运行时状态复位：否则 status/last_error 仍是删除前的、init 缓存仍认为已初始化。
func TestEdgeDevice_Restore_ResetsRuntimeState(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	stale := time.Now().Add(-72 * time.Hour)
	dev := models.EdgeDevice{
		Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1,
		Status: models.EdgeDeviceStatusOffline, ErrorCode: 42, LastError: "boom",
		InitState: "completed", InitLastStep: 3, InitTotalSteps: 3,
		Enabled: false, LastDataAt: &stale, ConfigVersion: "v-old",
	}
	db.Create(&dev)
	db.Delete(&dev)
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Update("data_purge_requested", true).Error; err != nil {
		t.Fatalf("seed latch: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 1})
	if w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body)); w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	var got models.EdgeDevice
	db.Unscoped().First(&got, dev.ID)
	if got.Status != models.EdgeDeviceStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.ErrorCode != 0 || got.LastError != "" {
		t.Errorf("error state = (%d,%q), want (0,\"\")", got.ErrorCode, got.LastError)
	}
	if got.InitState != "pending" || got.InitLastStep != 0 || got.InitTotalSteps != 0 {
		t.Errorf("init state = (%q,%d,%d), want (pending,0,0)", got.InitState, got.InitLastStep, got.InitTotalSteps)
	}
	if got.DataPurgeRequested || got.DataPurgeStartedAt != nil {
		t.Errorf("purge flags = (%v,%v), want (false,nil) —— 恢复即撤回删除意图", got.DataPurgeRequested, got.DataPurgeStartedAt)
	}
	// 恢复 = 回到可用状态：删除前被停用的设备必须重新启用，否则永远不会被调度。
	if !got.Enabled {
		t.Errorf("enabled = false, want true（恢复后应回到可用状态）")
	}
	// 旧的 last_data_at 属于上一个位置，不清掉 offlinedetector 会在下个 tick 判离线。
	if got.LastDataAt != nil {
		t.Errorf("last_data_at = %v, want nil（旧位置的事实必须清掉，否则恢复即被误判离线）", *got.LastDataAt)
	}
	if got.ConfigVersion != "" {
		t.Errorf("config_version = %q, want empty", got.ConfigVersion)
	}
}

// TestEdgeDevice_Update_MoveBindingResetsRuntimeState — 换节点（主路径）同样要
// 复位运行时状态，否则新节点上不会重新走 init/校准。
func TestEdgeDevice_Update_MoveBindingResetsRuntimeState(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "A", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "B", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE002", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{
		Name: "Device1", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1,
		Status: models.EdgeDeviceStatusActive, ErrorCode: 7, LastError: "old",
		InitState: "completed", InitLastStep: 2, InitTotalSteps: 2,
	}
	db.Create(&dev)

	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE002", "channel_id": 2})
	if w := rr2Do(t, r, "PUT", "/api/v1/edge-devices/1", string(body)); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var got models.EdgeDevice
	db.First(&got, dev.ID)
	if got.NodeID != "NODE002" || got.ChannelID != 2 {
		t.Fatalf("binding = (%s,%d), want (NODE002,2)", got.NodeID, got.ChannelID)
	}
	if got.Status != models.EdgeDeviceStatusPending {
		t.Errorf("status = %q, want pending（换节点后等待新节点首个数据帧）", got.Status)
	}
	if got.ErrorCode != 0 || got.LastError != "" {
		t.Errorf("error state = (%d,%q), want (0,\"\")", got.ErrorCode, got.LastError)
	}
	if got.InitState != "pending" || got.InitLastStep != 0 || got.InitTotalSteps != 0 {
		t.Errorf("init state = (%q,%d,%d), want (pending,0,0)", got.InitState, got.InitLastStep, got.InitTotalSteps)
	}
}

// ==================== merge-history：源校验 / 退役 / 幂等 / 台账 ====================

func rr2SeedMergeBase(t *testing.T, db *gorm.DB) {
	t.Helper()
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "New", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	db.Create(&models.EdgeDevice{Name: "Old", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
}

func rr2Merge(t *testing.T, r *gin.Engine, body map[string]interface{}) (int, rr2MergeResp) {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/merge-history", string(raw))
	var resp struct {
		Data rr2MergeResp `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, w.Body.String())
	}
	return w.Code, resp.Data
}

// TestEdgeDevice_MergeHistory_MovesRowsAndIsIdempotent — 把旧 device_id 的历史
// 改挂到新设备，重复调用返回 0 行（幂等）。源必须已软删（主用途）。
//
// ============ 2026-11-11：夹具与断言全面改为帧表 ============
//
// 原夹具是 3 行 unified_data + 1 行 device_data，断言 rows_migrated=4。
// 两张原始表先后退役后，merge-history 的时序迁移只覆盖**帧表**：
//
//	· UPDATE device_id 的循环走 existingTimeseriesTables → 只剩帧表，且它 HasID=false
//	  会被 continue 跳过；
//	· 帧表走 MigrateTelemetryFramesToDevice（复制 + ON CONFLICT + 精确删源）。
//
// 所以 rows_migrated 现在等于「迁入的帧数」。继续塞 unified_data 只会得到一个
// 永远不被搬的干扰项 —— 那会让断言看起来"数字不对"，实际是用例在测一条已退役的路径。
func TestEdgeDevice_MergeHistory_MovesRowsAndIsIdempotent(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)
	old := models.EdgeDevice{}
	db.First(&old, 2)
	db.Delete(&old) // 源已删（历史仍在）
	seedFrameTable(t, db)
	// 源设备 3 帧（不同时间戳，避免撞主键被 ON CONFLICT 丢弃）。
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 3; i++ {
		seedFrameAt(t, db, 2, base.Add(time.Duration(i)*time.Minute))
	}

	code, resp := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}
	if resp.RowsMigrated != 3 {
		t.Fatalf("rows_migrated = %v, want 3 (3 telemetry_frame)", resp.RowsMigrated)
	}
	if len(resp.Sources) != 1 || resp.Sources[0].ID != 2 || resp.Sources[0].State != "deleted" {
		t.Errorf("sources = %+v, want [{id:2,state:deleted}]", resp.Sources)
	}
	if len(resp.Sources) == 1 && resp.Sources[0].DeviceType != "bms_jbd" {
		t.Errorf("source device_type = %q, want bms_jbd", resp.Sources[0].DeviceType)
	}
	var targetRows, sourceRows int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = 1").Count(&targetRows)
	db.Model(&models.TelemetryFrame{}).Where("device_id = 2").Count(&sourceRows)
	if targetRows != 3 || sourceRows != 0 {
		t.Errorf("after merge frames target=%d source=%d, want 3/0", targetRows, sourceRows)
	}
	// 目标设备的「最后采集」必须跟着合并进来的历史前移，否则列表与历史图表自相矛盾。
	var target models.EdgeDevice
	db.First(&target, 1)
	if target.LastDataAt == nil {
		t.Errorf("target last_data_at = nil, want set after merge")
	}

	code, resp = rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("second merge expected 200, got %d", code)
	}
	if resp.RowsMigrated != 0 {
		t.Errorf("second merge rows_migrated = %v, want 0 (idempotent)", resp.RowsMigrated)
	}
}

// TestEdgeDevice_MergeHistory_RejectsLiveSourceByDefault — 把在用设备的历史搬走
// 会让它变成空壳（过去的历史归目标、之后的新数据归它自己）。默认必须拒绝。
func TestEdgeDevice_MergeHistory_RejectsLiveSourceByDefault(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db) // 设备 2 是存活设备
	db.Create(&models.UnifiedData{DeviceID: 2, SensorName: "voltage", Value: 1})

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusConflict {
		t.Fatalf("expected 409 for live source, got %d", code)
	}
	var src models.EdgeDevice
	db.First(&src, 2)
	if src.DeletedAt.Valid || !src.Enabled {
		t.Errorf("被拒绝的合并不应改动源设备")
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = 2").Count(&rows)
	if rows != 1 {
		t.Errorf("source rows after rejected merge = %d, want 1 (must not move)", rows)
	}
}

// TestEdgeDevice_MergeHistory_RejectsLiveSourceEvenWithLegacyFlag — 产品重做后
// 「在役设备不能作为源」是硬约束：即使旧客户端仍传 allow_live_source=true 也一样
// 409（该字段已被删除，Gin 会忽略未知字段 —— 不能因此放行）。
func TestEdgeDevice_MergeHistory_RejectsLiveSourceEvenWithLegacyFlag(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db) // 设备 2 是存活设备
	db.Create(&models.UnifiedData{DeviceID: 2, SensorName: "voltage", Value: 1})

	code, _ := rr2Merge(t, r, map[string]interface{}{
		"source_ids": []uint{2}, "allow_live_source": true,
	})
	if code != http.StatusConflict {
		t.Fatalf("live source must be 409 even with legacy allow_live_source, got %d", code)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = 2").Count(&rows)
	if rows != 1 {
		t.Errorf("source rows = %d, want 1 (must not move)", rows)
	}
}

// TestEdgeDevice_MergeHistory_RejectsCrossTypeSource — 产品红线（2026-10-25）：
// 只能合并**同型号**的历史设备。跨型号合并会把 A 型号的物理量挂到 B 型号名下，
// sensor_name/单位/图表/告警语义全被污染 —— 必须 400 且一行都不搬。
func TestEdgeDevice_MergeHistory_RejectsCrossTypeSource(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target-BMS", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	// 源是**不同型号**（逆变器），且已删除、有数据。
	inverter := models.EdgeDevice{Name: "Old-Inverter", Type: "techfine_inverter", NodeID: "NODE001", ChannelID: 1}
	db.Create(&inverter)
	db.Delete(&inverter)
	db.Create(&models.UnifiedData{DeviceID: inverter.ID, SensorName: "temperature", Value: 30})

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{inverter.ID}})
	if code != http.StatusBadRequest {
		t.Fatalf("cross-type merge must be 400, got %d", code)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", inverter.ID).Count(&rows)
	if rows != 1 {
		t.Errorf("source rows = %d, want 1（跨型号合并被拒后一行都不许动）", rows)
	}
	var targetRows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", 1).Count(&targetRows)
	if targetRows != 0 {
		t.Errorf("target rows = %d, want 0（跨型号合并被拒后目标不得有数据）", targetRows)
	}
}

// TestEdgeDevice_MergeHistory_RejectsSelf — 源=目标不能再静默成功。
func TestEdgeDevice_MergeHistory_RejectsSelf(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{1}})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self merge, got %d", code)
	}
}

// TestEdgeDevice_MergeHistory_RejectsNonexistentSource — 源 id 不存在（没有设备
// 记录）必须 400：既不能 200 假装成功，也不能放行（无法验证型号）。
func TestEdgeDevice_MergeHistory_RejectsNonexistentSourceWithoutData(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{9999}})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for nonexistent source, got %d", code)
	}
}

// TestEdgeDevice_DeletedList_DeviceTypeFilter — 合并弹窗只列「与目标同型号」的
// 已删除设备，服务端过滤保证其它型号不会把同型号历史挤出分页。
func TestEdgeDevice_DeletedList_DeviceTypeFilter(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	for _, typ := range []string{"bms_jbd", "techfine_inverter", "bms_jbd"} {
		d := models.EdgeDevice{Name: "D-" + typ, Type: typ, NodeID: "NODE001", ChannelID: 1}
		db.Create(&d)
		db.Delete(&d)
	}

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/deleted?device_type=bms_jbd", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deleted list: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Items []struct {
				ID   uint   `json:"id"`
				Type string `json:"type"`
			} `json:"items"`
			Total int64 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Data.Total != 2 || len(resp.Data.Items) != 2 {
		t.Fatalf("filtered deleted list = total %d items %d, want 2/2", resp.Data.Total, len(resp.Data.Items))
	}
	for _, it := range resp.Data.Items {
		if it.Type != "bms_jbd" {
			t.Errorf("item type = %q, want bms_jbd（服务端过滤失效）", it.Type)
		}
	}
}

// TestEdgeDevice_MergeHistory_RejectsOrphanWithoutDeviceRow — 设备行已不存在时
// 无法验证型号，「同型号」不变量不可证 ⇒ 400（产品重做明确放弃孤儿救援路径；
// 这类历史只受系统级保留期约束）。
func TestEdgeDevice_MergeHistory_RejectsOrphanWithoutDeviceRow(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)
	db.Create(&models.UnifiedData{DeviceID: 999, SensorName: "voltage", Value: 1})

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{999}})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for orphan source (no device row), got %d", code)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", 999).Count(&rows)
	if rows != 1 {
		t.Errorf("orphan rows = %d, want 1（被拒绝后不许动数据）", rows)
	}
}

// TestEdgeDevice_MergeHistory_MigratesLedgerTables — 只搬时序表会让同一台设备
// 两个端点互相矛盾（/unified-data/categories 有、/sensor-fields 没有）。
func TestEdgeDevice_MergeHistory_MigratesLedgerTables(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)
	old := models.EdgeDevice{}
	db.First(&old, 2)
	db.Delete(&old)

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "rsoc", Unit: "%", FirstSeen: t2, LastSeen: t2, Samples: 100})
	db.Create(&models.SensorFieldObs{DeviceID: 2, SensorName: "rsoc", Unit: "%", FirstSeen: t1, LastSeen: t1, Samples: 80})
	db.Create(&models.SensorFieldObs{DeviceID: 2, SensorName: "voltage", Unit: "V", FirstSeen: t1, LastSeen: t1, Samples: 5})
	db.Create(&models.DataSource{EdgeDeviceID: 1, Category: "rsoc", Name: "t-rsoc", Status: "active", ManagedBy: "auto"})
	db.Create(&models.DataSource{EdgeDeviceID: 2, Category: "voltage", Name: "s-voltage", Status: "active", ManagedBy: "auto"})

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}

	var rsoc models.SensorFieldObs
	if err := db.Where("device_id = ? AND sensor_name = ?", 1, "rsoc").First(&rsoc).Error; err != nil {
		t.Fatalf("load merged rsoc: %v", err)
	}
	if rsoc.Samples != 180 {
		t.Errorf("merged rsoc samples = %d, want 180 (100+80)", rsoc.Samples)
	}
	if !rsoc.FirstSeen.Equal(t1) {
		t.Errorf("merged rsoc first_seen = %v, want earlier %v", rsoc.FirstSeen, t1)
	}
	var srcSFO int64
	db.Model(&models.SensorFieldObs{}).Where("device_id = ?", 2).Count(&srcSFO)
	if srcSFO != 0 {
		t.Errorf("source sensor_field_obs rows = %d, want 0", srcSFO)
	}
	var movedDS int64
	db.Model(&models.DataSource{}).Where("edge_device_id = ? AND category = ?", 1, "voltage").Count(&movedDS)
	if movedDS != 1 {
		t.Errorf("moved data_sources rows = %d, want 1", movedDS)
	}
	var srcDS int64
	db.Model(&models.DataSource{}).Where("edge_device_id = ?", 2).Count(&srcDS)
	if srcDS != 0 {
		t.Errorf("source data_sources rows = %d, want 0", srcDS)
	}
}

// TestEdgeDevice_MergeHistory_RequiresLiveTarget — 目标必须存活: 把历史并进
// 一台已删设备等于把数据埋掉。
func TestEdgeDevice_MergeHistory_RequiresLiveTarget(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "Ghost", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&dev)
	db.Delete(&dev)

	body, _ := json.Marshal(map[string]interface{}{"source_ids": []uint{2}})
	w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/merge-history", string(body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for deleted target, got %d: %s", w.Code, w.Body.String())
	}
}

// TestEdgeDevice_DeletedList_PaginationAndPurgeState — 已删除设备列表必须可分页，
// 并提前暴露 purge 状态（正在清理的设备不能恢复）。
func TestEdgeDevice_DeletedList_PaginationAndPurgeState(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	for i := 0; i < 3; i++ {
		d := models.EdgeDevice{Name: "D", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
		db.Create(&d)
		db.Delete(&d)
	}
	// 让其中一台带上 purge 认领。
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", 1).
		Updates(map[string]interface{}{"data_purge_requested": true, "data_purge_started_at": time.Now()})

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/deleted?page=1&page_size=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("deleted list: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Items []struct {
				ID              uint `json:"id"`
				PurgeRequested  bool `json:"data_purge_requested"`
				PurgeInProgress bool `json:"purge_in_progress"`
			} `json:"items"`
			Total int64 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Data.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Data.Total)
	}
	if len(resp.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2 (page_size=2)", len(resp.Data.Items))
	}
	// 同一台设备的 purge 状态可能在第二页（按 deleted_at DESC 排序，不能假设第 0 项是 id=1）。
	var found bool
	for page := 1; page <= 2 && !found; page++ {
		w := rr2Do(t, r, "GET", fmt.Sprintf("/api/v1/edge-devices/deleted?page=%d&page_size=2", page), "")
		if w.Code != http.StatusOK {
			t.Fatalf("deleted list page %d: %d %s", page, w.Code, w.Body.String())
		}
		var pageResp struct {
			Data struct {
				Items []struct {
					ID              uint `json:"id"`
					PurgeRequested  bool `json:"data_purge_requested"`
					PurgeInProgress bool `json:"purge_in_progress"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &pageResp); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		for _, it := range pageResp.Data.Items {
			if it.ID == 1 {
				found = true
				if !it.PurgeRequested || !it.PurgeInProgress {
					t.Errorf("device 1 purge state = (%v,%v), want (true,true)", it.PurgeRequested, it.PurgeInProgress)
				}
			}
		}
	}
	if !found {
		t.Errorf("device 1 not found in the first two pages")
	}
}

// TestEdgeDevice_PurgeLatch_DeleteRestoreDeleteThenKeepData — 2026-10-24 审核 P0 的不变量。
//
// `data_purge_requested` 是**单向闩锁**（只有 purge 任务在「设备已复活」时才清回 false）。
// 若 restore 不清它，这条用户序列会**静默硬删历史**：
//
//	① 删（勾选同时删数据）→ 标记 true
//	② 恢复（用户后悔了，历史应该保住）
//	③ 再删（这次明确不删数据）→ 旧实现因为 delete_data=false 不写该列 ⇒ 标记仍是 true
//	④ purge 任务跑 ⇒ 硬删历史数据，而 API 应答说 data_purge_requested=false
//
// 本用例断言 ③ 之后标记必须是 false，且 purge.RunOnce 一行都不删。
func TestEdgeDevice_PurgeLatch_DeleteRestoreDeleteThenKeepData(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "Latch", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76"}
	db.Create(&dev)
	for i := 0; i < 3; i++ {
		db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "voltage", Value: float64(i)})
	}

	do := func(method, path, body string) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != "" {
			rdr = bytes.NewReader([]byte(body))
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		return w
	}

	// ① 删，并勾选「同时删除数据」。
	if w := do("DELETE", "/api/v1/edge-devices/1?delete_data=true", ""); w.Code != http.StatusOK {
		t.Fatalf("step1 delete: %d %s", w.Code, w.Body.String())
	}
	var inst models.EdgeDevice
	db.Unscoped().First(&inst, dev.ID)
	if !inst.DataPurgeRequested {
		t.Fatalf("step1: data_purge_requested must be true")
	}

	// ② 恢复：语义 = 撤回删除意图 ⇒ 标记必须被清掉。
	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE001", "channel_id": 1})
	if w := do("POST", "/api/v1/edge-devices/1/restore", string(body)); w.Code != http.StatusOK {
		t.Fatalf("step2 restore: %d %s", w.Code, w.Body.String())
	}
	db.Unscoped().First(&inst, dev.ID)
	if inst.DataPurgeRequested {
		t.Fatal("step2: restore 必须清掉 data_purge_requested（否则之后的「不删数据删除」仍会硬删历史）")
	}

	// ②b **独立断言 restore 本身**（不能靠第 ③ 步代替）：
	// 第二次 DELETE 也会把闩锁写成 false，从而**掩盖**「restore 没清标记」的缺陷 ——
	// 本用例初版就是这样假绿的（变异自证：只回滚 restore 修复时它仍 PASS）。
	// 真实危害场景正是「删(删数据) → 恢复 → 用户不再删除」，此时 purge 任务
	// 若看到残留 true 就会硬删刚恢复的历史。这里直接跑一次 purge 来钉住。
	// 注意：**不能**用「跑一次 purge 看它删不删」来断言这件事 —— purge 的复活
	// 守卫（purge_edge_device.go 的 AbandonedRevived）会自己把标记清掉并跳过删除，
	// 于是「restore 忘了清标记」这个缺陷会被守卫掩盖（变异自证确认过：只回滚
	// restore 修复时，purge 路径仍 PASS）。必须**直接读库**断言闩锁状态。
	{
		var raw struct {
			Flag bool `gorm:"column:data_purge_requested"`
		}
		if err := db.Unscoped().Model(&models.EdgeDevice{}).
			Select("data_purge_requested").Where("id = ?", dev.ID).Scan(&raw).Error; err != nil {
			t.Fatalf("step2b read flag: %v", err)
		}
		if raw.Flag {
			t.Fatalf("step2b: 恢复后闩锁仍为 true —— restore 必须直接清掉它，" +
				"不能依赖 purge 的复活守卫兜底（守卫只在任务真的扫到该设备时才生效，" +
				"而每日任务/重启时机不可控）")
		}
		var rows0 int64
		db.Model(&models.UnifiedData{}).Where("device_id = ?", dev.ID).Count(&rows0)
		if rows0 != 3 {
			t.Fatalf("step2b history rows = %d, want 3", rows0)
		}
	}

	// ③ 再删，这次明确不删数据。
	w := do("DELETE", "/api/v1/edge-devices/1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("step3 delete: %d %s", w.Code, w.Body.String())
	}
	db.Unscoped().First(&inst, dev.ID)
	if inst.DataPurgeRequested {
		t.Fatal("step3: delete_data=false 必须显式把闩锁写成 false（不能只写 true）")
	}
	// 应答也必须回读库值，不能回显入参（原 P0 的另一半：应答 false / 库 true）。
	var resp struct {
		Data struct {
			DataPurgeRequested bool `json:"data_purge_requested"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Data.DataPurgeRequested {
		t.Errorf("response data_purge_requested = true, want false (must reflect DB, not request)")
	}

	// ④ 跑 purge：一行都不许删，设备行也必须还在。
	purger := datalifecycle.NewDevicePurger(db)
	purger.SetSchedule(0, 0, 0)
	results, err := purger.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("purge run: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("purge picked up %d device(s), want 0: %+v", len(results), results)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", dev.ID).Count(&rows)
	if rows != 3 {
		t.Errorf("history rows = %d, want 3 (用户明确要求保留，必须一行不少)", rows)
	}
	var devRows int64
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).Count(&devRows)
	if devRows != 1 {
		t.Errorf("device row count = %d, want 1", devRows)
	}
}

// ==================== 容量门禁 / merge 台账多源 / purge 互斥 ====================

// TestEdgeDevice_MoveAndRestore_RejectCommandCapacityOverflow — 2026-10-25 审核 F-03。
//
// 「换节点」（PUT 改 node/channel）与 restore 会把同一份 command_intervals 带到
// 另一个节点上，而节点申报的每设备命令上限可能更小。不校验的话该节点**整份**
// ConfigManifest 会被拒发（nodemgr/sender.go 容量校验失败即整单拒绝），所有设备
// 静默跑旧配置。create 路径一直有这道门禁，PUT/restore 曾缺失。
func TestEdgeDevice_MoveAndRestore_RejectCommandCapacityOverflow(t *testing.T) {
	r, db := setupEdgeDeviceTestWithRegistry(t, func(reg *drivers.Registry) {
		reg.Register(&fakeMultiDriver{})
	})

	db.Create(&models.Node{NodeID: "NODE-A", Name: "A", Status: "online"})
	db.Create(&models.Node{
		NodeID: "NODE-B", Name: "B", Status: "online",
		HardwareInfo: `{"manifest_capacity":{"max_templates":16,"max_channels":4,"max_template_ids":16,"max_commands_per_device":1}}`,
	})
	db.Create(&models.Channel{NodeID: "NODE-A", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE-B", HardwareType: "UART", BusType: "UART", Enabled: true})
	intervals, _ := json.Marshal(map[string]int{"read_a": 1000, "read_b": 1000})
	dev := models.EdgeDevice{
		Name: "cap", Type: "fake_multi", NodeID: "NODE-A", ChannelID: 1,
		CommandIntervals: intervals,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}

	// PUT 换节点到低上限节点 → 400
	body, _ := json.Marshal(map[string]interface{}{"node_id": "NODE-B", "channel_id": 2})
	if w := rr2Do(t, r, "PUT", "/api/v1/edge-devices/1", string(body)); w.Code != http.StatusBadRequest {
		t.Fatalf("PUT move expected 400 (capacity overflow), got %d: %s", w.Code, w.Body.String())
	}
	var afterMove models.EdgeDevice
	db.First(&afterMove, dev.ID)
	if afterMove.NodeID != "NODE-A" {
		t.Errorf("被拒绝的换节点不应改动绑定，got node=%s", afterMove.NodeID)
	}

	// restore 到低上限节点 → 400
	db.Delete(&dev)
	body, _ = json.Marshal(map[string]interface{}{"node_id": "NODE-B", "channel_id": 2})
	if w := rr2Do(t, r, "POST", "/api/v1/edge-devices/1/restore", string(body)); w.Code != http.StatusBadRequest {
		t.Fatalf("restore expected 400 (capacity overflow), got %d: %s", w.Code, w.Body.String())
	}
}

// TestEdgeDevice_MergeHistory_MultiSourceSameSensorNames — 2026-10-25 审核 F-04。
// 「同型号的多台旧设备并进来」是最常见场景，两个源有同名 sensor_name 时旧实现用
// 一条 UPDATE 搬全部源 ⇒ 撞复合主键 (device_id, sensor_name) ⇒ 台账永久不迁移。
func TestEdgeDevice_MergeHistory_MultiSourceSameSensorNames(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	src2 := models.EdgeDevice{Name: "Old2", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	src3 := models.EdgeDevice{Name: "Old3", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&src2)
	db.Create(&src3)
	db.Delete(&src2)
	db.Delete(&src3)

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	// 目标已有 rsoc；两个源都有 rsoc（与目标冲突）和 voltage（源之间同名、目标没有）。
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "rsoc", FirstSeen: t2, LastSeen: t2, Samples: 5})
	db.Create(&models.SensorFieldObs{DeviceID: 2, SensorName: "rsoc", FirstSeen: t1, LastSeen: t1, Samples: 7})
	db.Create(&models.SensorFieldObs{DeviceID: 2, SensorName: "voltage", FirstSeen: t1, LastSeen: t1, Samples: 10})
	db.Create(&models.SensorFieldObs{DeviceID: 3, SensorName: "rsoc", FirstSeen: t1, LastSeen: t1, Samples: 8})
	db.Create(&models.SensorFieldObs{DeviceID: 3, SensorName: "voltage", FirstSeen: t1, LastSeen: t2, Samples: 20})
	// data_sources：两个源都有 voltage（源间同名），目标有 rsoc。
	db.Create(&models.DataSource{EdgeDeviceID: 1, Category: "rsoc", Name: "t-rsoc", Status: "active", ManagedBy: "auto"})
	db.Create(&models.DataSource{EdgeDeviceID: 2, Category: "voltage", Name: "s2-voltage", Status: "active", ManagedBy: "auto"})
	db.Create(&models.DataSource{EdgeDeviceID: 3, Category: "voltage", Name: "s3-voltage", Status: "active", ManagedBy: "auto"})

	code, resp := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2, 3}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("warnings = %v, want none（台账必须真正迁移成功）", resp.Warnings)
	}

	var rsoc models.SensorFieldObs
	if err := db.Where("device_id = ? AND sensor_name = ?", 1, "rsoc").First(&rsoc).Error; err != nil {
		t.Fatalf("load rsoc: %v", err)
	}
	if rsoc.Samples != 20 {
		t.Errorf("rsoc samples = %d, want 20 (5+7+8)", rsoc.Samples)
	}
	var voltage models.SensorFieldObs
	if err := db.Where("device_id = ? AND sensor_name = ?", 1, "voltage").First(&voltage).Error; err != nil {
		t.Fatalf("load voltage: %v", err)
	}
	if voltage.Samples != 30 {
		t.Errorf("voltage samples = %d, want 30 (10+20)", voltage.Samples)
	}
	if !voltage.FirstSeen.Equal(t1) {
		t.Errorf("voltage first_seen = %v, want %v", voltage.FirstSeen, t1)
	}
	var srcSFO, srcDS int64
	db.Model(&models.SensorFieldObs{}).Where("device_id IN ?", []uint{2, 3}).Count(&srcSFO)
	db.Model(&models.DataSource{}).Where("edge_device_id IN ?", []uint{2, 3}).Count(&srcDS)
	if srcSFO != 0 || srcDS != 0 {
		t.Errorf("source ledger rows left: sfo=%d data_sources=%d, want 0/0", srcSFO, srcDS)
	}
	var volDS int64
	db.Model(&models.DataSource{}).Where("edge_device_id = ? AND category = ?", 1, "voltage").Count(&volDS)
	if volDS != 1 {
		t.Errorf("target voltage data_sources rows = %d, want 1", volDS)
	}

	// 重试幂等：不该再产生 warning 或行。
	code, resp = rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2, 3}})
	if code != http.StatusOK || resp.RowsMigrated != 0 || len(resp.Warnings) != 0 {
		t.Errorf("retry = (%d, %+v), want (200, 0 rows, no warnings)", code, resp)
	}
}

// TestEdgeDevice_MergeHistory_PreservesManualDataSourceOnConflict — 审核 F-08：
// data_sources 的 manual 行是人工配置（投影器永不覆盖），与目标同 category 冲突时
// 必须保留并 warning，不能静默删除。
func TestEdgeDevice_MergeHistory_PreservesManualDataSourceOnConflict(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	old := models.EdgeDevice{Name: "Old", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&old)
	db.Delete(&old)
	db.Create(&models.DataSource{EdgeDeviceID: 1, Category: "rsoc", Name: "t-rsoc", Status: "active", ManagedBy: "auto"})
	db.Create(&models.DataSource{EdgeDeviceID: 2, Category: "rsoc", Name: "manual-rsoc", Status: "standby", ManagedBy: "manual"})

	code, resp := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}
	if len(resp.Warnings) == 0 {
		t.Errorf("warnings empty, want a manual-row conflict warning")
	}
	var manualLeft int64
	db.Model(&models.DataSource{}).Where("edge_device_id = ? AND managed_by = ?", 2, "manual").Count(&manualLeft)
	if manualLeft != 1 {
		t.Errorf("manual data_sources row on source = %d, want 1（人工配置不得静默删除）", manualLeft)
	}
}

// TestEdgeDevice_MergeHistory_RejectsPurgeClaimedSource — 审核 F-05：purge 已认领的
// 源不能再合并（否则 purge 边删、merge 边搬，用户只救回一部分历史却拿到 200）。
func TestEdgeDevice_MergeHistory_RejectsPurgeClaimedSource(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	old := models.EdgeDevice{Name: "Old", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&old)
	db.Create(&models.UnifiedData{DeviceID: old.ID, SensorName: "voltage", Value: 1})
	db.Delete(&old)
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", old.ID).
		Updates(map[string]interface{}{
			"data_purge_requested":  true,
			"data_purge_started_at": time.Now(),
		}).Error; err != nil {
		t.Fatalf("seed purge claim: %v", err)
	}

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusConflict {
		t.Fatalf("merge with purge-claimed source expected 409, got %d", code)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", 2).Count(&rows)
	if rows != 1 {
		t.Errorf("source rows = %d, want 1（被拒绝的合并不许动数据）", rows)
	}
}

// TestEdgeDevice_MergeHistory_RevokesPurgeRequestOfSource — 源只是「请求删数据」但
// 尚未被 purge 认领时，合并要撤回删除意图（与 restore 同语义），否则救回的历史
// 仍会被随后的 purge 任务清掉。
func TestEdgeDevice_MergeHistory_RevokesPurgeRequestOfSource(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Target", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1})
	old := models.EdgeDevice{Name: "Old", Type: "bms_jbd", NodeID: "NODE001", ChannelID: 1}
	db.Create(&old)
	db.Create(&models.UnifiedData{DeviceID: old.ID, SensorName: "voltage", Value: 1})
	db.Delete(&old)
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", old.ID).
		Update("data_purge_requested", true).Error; err != nil {
		t.Fatalf("seed latch: %v", err)
	}

	code, _ := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}
	var flag bool
	db.Unscoped().Model(&models.EdgeDevice{}).Select("data_purge_requested").
		Where("id = ?", old.ID).Scan(&flag)
	if flag {
		t.Errorf("data_purge_requested = true after successful merge, want false（合并即撤回删除意图）")
	}
}
