package nodemgr

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// mockMQTTPublisher 记录所有发布的消息, 用于验证 SendPeriphCmd 编码正确性
type mockMQTTPublisher struct {
	publishedTopic     string
	publishedPayload   []byte
	publishQoS2Topic   string
	publishQoS2Payload []byte
	publishErr         error
}

func (m *mockMQTTPublisher) Publish(topic string, payload []byte) error {
	m.publishedTopic = topic
	m.publishedPayload = payload
	return m.publishErr
}

func (m *mockMQTTPublisher) PublishQoS2(topic string, payload []byte) error {
	m.publishQoS2Topic = topic
	m.publishQoS2Payload = payload
	return m.publishErr
}

func (m *mockMQTTPublisher) PublishRetained(topic string, payload []byte) error {
	return m.publishErr
}

// =====================================================================
// SendPeriphCmd 编码正确性测试
// =====================================================================

// TestSendPeriphCmd_MessageType 验证消息类型字节为 0x1B
func TestSendPeriphCmd_MessageType(t *testing.T) {
	// Arrange
	db := setupTestDBForPeriph(t)
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)
	db.Create(&models.Node{NodeID: "dev1", Status: "online"})

	// Act
	err := mgr.SendPeriphCmd("dev1", 1, 5, 0, 0, nil)

	// Assert
	if err != nil {
		t.Fatalf("SendPeriphCmd failed: %v", err)
	}
	if len(mock.publishedPayload) < 1 {
		t.Fatal("empty payload")
	}
	if mock.publishedPayload[0] != frame.MsgPeriphCmd {
		t.Errorf("expected msg type 0x%02X, got 0x%02X", frame.MsgPeriphCmd, mock.publishedPayload[0])
	}
}

func TestSendPeriphCmd_NilMQTTFailsClosed(t *testing.T) {
	db := setupTestDBForPeriph(t)
	mgr := newManagerWithMock(db, nil)
	mgr.mqtt = nil
	if err := mgr.SendPeriphCmd("dev1", 1, 0, 1, 0, nil); err == nil {
		t.Fatal("nil MQTT client must return an error instead of panicking")
	}
}

func TestSendPeriphCmd_UsesQoS1Publish(t *testing.T) {
	db := setupTestDBForPeriph(t)
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	if err := mgr.SendPeriphCmd("dev1", 1, 0, 1, 0, nil); err != nil {
		t.Fatalf("SendPeriphCmd failed: %v", err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("QoS1 Publish was not used")
	}
	if len(mock.publishQoS2Payload) != 0 {
		t.Fatal("QoS2 Publish must not be used for ESP-MQTT peripheral commands")
	}
}

// TestSendPeriphCmd_Topic 验证发布到正确的 MQTT topic
func TestSendPeriphCmd_Topic(t *testing.T) {
	db := setupTestDBForPeriph(t)
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	// Act
	mgr.SendPeriphCmd("dev1", 1, 5, 0, 0, nil)

	// Assert
	expectedTopic := "nodes/dev1/control"
	if mock.publishedTopic != expectedTopic {
		t.Errorf("expected topic %s, got %s", expectedTopic, mock.publishedTopic)
	}
}

// TestSendPeriphCmd_Fields 验证各字段编码正确 (表驱动)
func TestSendPeriphCmd_Fields(t *testing.T) {
	cases := []struct {
		name        string
		periphType  uint8
		pin         uint8
		action      uint8
		value       uint32
		config      []byte
		expectValue bool // value 字段是否应编码 (value > 0 时才编码)
		expectCfg   bool // config 字段是否应编码
	}{
		{
			name:       "GPIO SetHigh",
			periphType: 1, pin: 3, action: 1, value: 0, config: nil,
		},
		{
			name:       "GPIO Read",
			periphType: 1, pin: 5, action: 2, value: 0, config: nil,
		},
		{
			name:       "GPIO Config",
			periphType: 1, pin: 7, action: 3, value: 0, config: []byte{1, 0},
			expectCfg: true,
		},
		{
			name:       "PWM SetDuty with value",
			periphType: 2, pin: 6, action: 0, value: 5000, config: nil,
			expectValue: true,
		},
		{
			name:       "PWM Start with config",
			periphType: 2, pin: 6, action: 2, value: 0,
			config:    []byte{0xE8, 0x03, 0x00, 0x00, 0x88, 0x13, 0x0E},
			expectCfg: true,
		},
		{
			name:       "PWM Stop (no value, no config)",
			periphType: 2, pin: 6, action: 3, value: 0, config: nil,
		},
		{
			name:       "GPIO Toggle",
			periphType: 1, pin: 4, action: 5, value: 0, config: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			db := setupTestDBForPeriph(t)
			mock := &mockMQTTPublisher{}
			mgr := newManagerWithMock(db, mock)

			// Act
			err := mgr.SendPeriphCmd("dev1", tc.periphType, tc.pin, tc.action, tc.value, tc.config)
			if err != nil {
				t.Fatalf("SendPeriphCmd: %v", err)
			}

			// Assert: 解码验证
			dec, err := frame.NewDecoder(mock.publishedPayload)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			var gotPeriphType, gotPin, gotAction uint64
			var gotValue uint64
			var gotConfig []byte
			var hasValue, hasConfig bool

			for {
				field, err := dec.NextField()
				if err != nil {
					break
				}
				switch field.FieldNum {
				case 2:
					gotPeriphType = frame.GetUint64(field)
				case 3:
					gotPin = frame.GetUint64(field)
				case 4:
					gotAction = frame.GetUint64(field)
				case 5:
					gotValue = frame.GetUint64(field)
					hasValue = true
				case 6:
					gotConfig = frame.GetBytes(field)
					hasConfig = true
				}
			}

			if gotPeriphType != uint64(tc.periphType) {
				t.Errorf("periph_type: expected %d, got %d", tc.periphType, gotPeriphType)
			}
			if gotPin != uint64(tc.pin) {
				t.Errorf("pin: expected %d, got %d", tc.pin, gotPin)
			}
			if gotAction != uint64(tc.action) {
				t.Errorf("action: expected %d, got %d", tc.action, gotAction)
			}

			if tc.expectValue && !hasValue {
				t.Error("expected value field to be present")
			}
			if !tc.expectValue && hasValue {
				t.Error("value field should not be present for zero value")
			}
			if tc.expectValue && gotValue != uint64(tc.value) {
				t.Errorf("value: expected %d, got %d", tc.value, gotValue)
			}

			if tc.expectCfg && !hasConfig {
				t.Error("expected config field to be present")
			}
			if tc.expectCfg && hasConfig {
				if len(gotConfig) != len(tc.config) {
					t.Errorf("config length: expected %d, got %d", len(tc.config), len(gotConfig))
				}
				for i, b := range tc.config {
					if gotConfig[i] != b {
						t.Errorf("config[%d]: expected %d, got %d", i, b, gotConfig[i])
					}
				}
			}
		})
	}
}

// TestSendPeriphCmd_RequestIDIncrement 验证 request_id 自增
func TestSendPeriphCmd_RequestIDIncrement(t *testing.T) {
	db := setupTestDBForPeriph(t)
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	// Act: 发送两次命令, request_id 应递增
	mgr.SendPeriphCmd("dev1", 1, 1, 0, 0, nil)
	firstPayload := mock.publishedPayload

	mgr.SendPeriphCmd("dev1", 1, 2, 0, 0, nil)
	secondPayload := mock.publishedPayload

	// Assert: 解码 request_id
	dec1, _ := frame.NewDecoder(firstPayload)
	dec2, _ := frame.NewDecoder(secondPayload)

	var reqID1, reqID2 uint64
	for {
		field, err := dec1.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 1 {
			reqID1 = frame.GetUint64(field)
		}
	}
	for {
		field, err := dec2.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 1 {
			reqID2 = frame.GetUint64(field)
		}
	}

	if reqID2 <= reqID1 {
		t.Errorf("request_id should increment: first=%d, second=%d", reqID1, reqID2)
	}
}

// TestSendPeriphCmd_PublishError 验证 MQTT 发布失败时返回错误
func TestSendPeriphCmd_PublishError(t *testing.T) {
	db := setupTestDBForPeriph(t)
	mock := &mockMQTTPublisher{publishErr: errMockPublish}
	mgr := newManagerWithMock(db, mock)

	err := mgr.SendPeriphCmd("dev1", 1, 1, 0, 0, nil)
	if err == nil {
		t.Error("expected error when MQTT publish fails")
	}
}

// =====================================================================
// ConfigManifest field 11/12 (GPIO/PWM) 编码测试
// =====================================================================

// setupTestDBForManifest 创建带 GPIO/PWM 配置的测试 DB
func setupTestDBForManifest(t *testing.T, nodeID string, protocolVersion string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.GPIOConfig{},
		&models.PWMConfig{}, &models.ConfigMeta{},
	)
	capabilities := `{"buses":{"gpio":[{"id":"GPIO1","pin":1},{"id":"GPIO2","pin":2},{"id":"GPIO5","pin":5},{"id":"GPIO6","pin":6},{"id":"GPIO7","pin":7}],"pwm":[{"id":"PWM0","channel":0,"max_resolution_bits":14},{"id":"PWM1","channel":1,"max_resolution_bits":14}]}}`
	db.Create(&models.Node{NodeID: nodeID, Status: "online", ProtocolVersion: protocolVersion, Capabilities: capabilities})
	return db
}

// TestConfigManifest_GPIOConfigs 验证 field 11 正确编码 GPIO 配置
func TestConfigManifest_GPIOConfigs(t *testing.T) {
	// Arrange
	db := setupTestDBForManifest(t, "dev1", "2.5")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	// 创建 GPIO 配置 (全部 enabled=true, 注意 GORM default:true 会覆盖 false)
	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 1, InitialLevel: 0, Enabled: true})
	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 5, Direction: 0, InitialLevel: 1, Enabled: true})

	// Act
	mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID:   "dev1",
		Action:     SyncActionFull,
		SyncID:     "test-sync-1",
		ManifestID: "test-manifest-1",
		Reason:     "test",
	})

	// Assert: 解码 manifest, 查找 field 11
	dec, err := frame.NewDecoder(mock.publishedPayload)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	var gpioSubFrames [][]byte
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 11 {
			gpioSubFrames = append(gpioSubFrames, frame.GetBytes(field))
		}
	}

	// 应有 2 个 GPIO sub-frame (全部 enabled)
	if len(gpioSubFrames) != 2 {
		t.Fatalf("expected 2 GPIO sub-frames, got %d", len(gpioSubFrames))
	}

	// 解码第一个 sub-frame: pin=2, direction=1, initial_level=0
	subDec1, _ := frame.NewSubDecoder(gpioSubFrames[0])
	var pin1, dir1, level1 uint64
	for {
		field, err := subDec1.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			pin1 = frame.GetUint64(field)
		case 2:
			dir1 = frame.GetUint64(field)
		case 3:
			level1 = frame.GetUint64(field)
		}
	}
	if pin1 != 2 || dir1 != 1 || level1 != 0 {
		t.Errorf("GPIO[0]: expected pin=2 dir=1 level=0, got pin=%d dir=%d level=%d", pin1, dir1, level1)
	}
}

// TestConfigManifest_PWMConfigs 验证 field 12 正确编码 PWM 配置
func TestConfigManifest_PWMConfigs(t *testing.T) {
	// Arrange
	db := setupTestDBForManifest(t, "dev1", "2.5")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, AutoStart: false, Enabled: true})
	db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM1", Channel: 1, Pin: 7, Frequency: 2000, Duty: 8000, Resolution: 12, AutoStart: true, Enabled: true})

	// Act
	mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID:   "dev1",
		Action:     SyncActionFull,
		SyncID:     "test-sync-2",
		ManifestID: "test-manifest-2",
		Reason:     "test",
	})

	// Assert: 解码 manifest, 查找 field 12
	dec, err := frame.NewDecoder(mock.publishedPayload)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	var pwmSubFrames [][]byte
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 12 {
			pwmSubFrames = append(pwmSubFrames, frame.GetBytes(field))
		}
	}

	if len(pwmSubFrames) != 2 {
		t.Fatalf("expected 2 PWM sub-frames, got %d", len(pwmSubFrames))
	}

	// 解码第一个 sub-frame: channel=0, pin=6, freq=1000, duty=500, resolution=14, auto_start=false
	subDec, _ := frame.NewSubDecoder(pwmSubFrames[0])
	var channel, pin, freq, duty, resolution uint64
	var autoStart bool
	for {
		field, err := subDec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			channel = frame.GetUint64(field)
		case 2:
			pin = frame.GetUint64(field)
		case 3:
			freq = frame.GetUint64(field)
		case 4:
			duty = frame.GetUint64(field)
		case 5:
			resolution = frame.GetUint64(field)
		case 6:
			autoStart = frame.GetBool(field)
		}
	}
	if channel != 0 || pin != 6 || freq != 1000 || duty != 500 || resolution != 14 {
		t.Errorf("PWM[0]: expected channel=0 pin=6 freq=1000 duty=500 res=14, got channel=%d pin=%d freq=%d duty=%d res=%d",
			channel, pin, freq, duty, resolution)
	}
	if autoStart {
		t.Error("PWM[0]: expected auto_start=false")
	}
}

// TestConfigManifest_PeriphVersionGate 验证 protocol_version < 2.4 时不编码 GPIO/PWM
func TestConfigManifest_PeriphVersionGate(t *testing.T) {
	// Arrange: 使用旧版本固件
	db := setupTestDBForManifest(t, "dev1", "2.3")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 1, Enabled: true})
	db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	// Act
	mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID:   "dev1",
		Action:     SyncActionFull,
		SyncID:     "test-sync-3",
		ManifestID: "test-manifest-3",
		Reason:     "test",
	})

	// Assert: field 11 和 12 不应存在
	dec, _ := frame.NewDecoder(mock.publishedPayload)
	hasField11 := false
	hasField12 := false
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 11 {
			hasField11 = true
		}
		if field.FieldNum == 12 {
			hasField12 = true
		}
	}
	if hasField11 {
		t.Error("field 11 (GPIO) should NOT be present for protocol_version 2.3")
	}
	if hasField12 {
		t.Error("field 12 (PWM) should NOT be present for protocol_version 2.3")
	}
}

// TestConfigManifest_PeriphVersion_2_4 验证 protocol_version 2.4 时编码 GPIO/PWM
func TestConfigManifest_PeriphVersion_2_4(t *testing.T) {
	// Arrange: 2.4 版本应包含 GPIO/PWM
	db := setupTestDBForManifest(t, "dev1", "2.4")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)

	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 1, Direction: 0, Enabled: true})

	// Act
	mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID:   "dev1",
		Action:     SyncActionFull,
		SyncID:     "test-sync-4",
		ManifestID: "test-manifest-4",
		Reason:     "test",
	})

	// Assert: field 11 应存在
	dec, _ := frame.NewDecoder(mock.publishedPayload)
	hasField11 := false
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 11 {
			hasField11 = true
		}
	}
	if !hasField11 {
		t.Error("field 11 (GPIO) should be present for protocol_version 2.4")
	}
}

// TestConfigManifest_PeriphEmpty 验证无 GPIO/PWM 配置时不编码 field 11/12
func TestConfigManifest_PeriphEmpty(t *testing.T) {
	// Arrange
	db := setupTestDBForManifest(t, "dev1", "2.5")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)
	// 不创建任何 GPIO/PWM 配置

	// Act
	mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID:   "dev1",
		Action:     SyncActionFull,
		SyncID:     "test-sync-5",
		ManifestID: "test-manifest-5",
		Reason:     "test",
	})

	// Assert: field 11 和 12 不应存在
	dec, _ := frame.NewDecoder(mock.publishedPayload)
	hasField11 := false
	hasField12 := false
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 11 {
			hasField11 = true
		}
		if field.FieldNum == 12 {
			hasField12 = true
		}
	}
	if hasField11 {
		t.Error("field 11 should not be present when no GPIO configs")
	}
	if hasField12 {
		t.Error("field 12 should not be present when no PWM configs")
	}
}

func TestConfigManifest_RejectsLegacyPeripheralChannelsIncludingNumericAliases(t *testing.T) {
	db := setupTestDBForManifest(t, "dev1", "2.5")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)
	db.Create(&models.Channel{NodeID: "dev1", HardwareType: "GPIO", BusType: "GPIO", Enabled: true})
	db.Create(&models.Channel{NodeID: "dev1", HardwareType: "6", BusType: "6", Enabled: true})
	err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: "dev1", SyncID: "sync", ManifestID: "manifest"})
	if err == nil || !strings.Contains(err.Error(), "legacy peripheral") {
		t.Fatalf("got %v", err)
	}
	if len(mock.publishedPayload) != 0 {
		t.Fatal("invalid legacy peripheral manifest was published")
	}
}

func TestConfigManifestRevalidatesCurrentAuthorityAndRecordsFailure(t *testing.T) {
	tests := []struct {
		name string
		seed func(*gorm.DB)
	}{
		{"stale GPIO membership", func(db *gorm.DB) { db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 99, Direction: 1, Enabled: true}) }},
		{"invalid GPIO scalar", func(db *gorm.DB) { db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 9, Enabled: true}) }},
		{"stale PWM identity", func(db *gorm.DB) {
			db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 1, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})
		}},
		{"PWM resolution limit", func(db *gorm.DB) {
			db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 15, Enabled: true})
		}},
		{"PWM clock scalar", func(db *gorm.DB) {
			db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 3000, Resolution: 14, Enabled: true})
		}},
		{"enabled transport conflict", func(db *gorm.DB) {
			db.Create(&models.Channel{NodeID: "dev1", BusType: "I2C", HardwareType: "I2C", BusConfig: "0207", Enabled: true})
			db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 1, Enabled: true})
		}},
		{"malformed enabled transport", func(db *gorm.DB) {
			db.Create(&models.Channel{NodeID: "dev1", BusType: "I2C", HardwareType: "I2C", BusConfig: "02", Enabled: true})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDBForManifest(t, "dev1", "2.5")
			mock := &mockMQTTPublisher{}
			mgr := newManagerWithMock(db, mock)
			tc.seed(db)
			err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: "dev1", SyncID: "authority", ManifestID: "bad"})
			if err == nil {
				t.Fatal("expected authority validation failure")
			}
			if len(mock.publishedPayload) != 0 {
				t.Fatal("invalid manifest was published")
			}
			var node models.Node
			db.Where("node_id = ?", "dev1").First(&node)
			if node.ConfigSyncState != "failed" {
				t.Fatalf("sync state=%q error=%v", node.ConfigSyncState, err)
			}
		})
	}
}

func TestConfigManifestIgnoresDisabledTransportPinReservation(t *testing.T) {
	db := setupTestDBForManifest(t, "dev1", "2.5")
	mock := &mockMQTTPublisher{}
	mgr := newManagerWithMock(db, mock)
	db.Create(&models.Channel{NodeID: "dev1", BusType: "I2C", HardwareType: "I2C", BusConfig: "0207", Enabled: true})
	db.Model(&models.Channel{}).Where("node_id = ?", "dev1").Update("enabled", false)
	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 1, Enabled: true})
	if err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: "dev1", SyncID: "ok", ManifestID: "ok"}); err != nil {
		t.Fatal(err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("valid manifest not published")
	}
}

// =====================================================================
// 辅助函数
// =====================================================================

var errMockPublish = &mockError{"mock publish error"}

type mockError struct{ msg string }

func (e *mockError) Error() string { return e.msg }

// setupTestDBForPeriph 创建用于外设测试的 DB
func setupTestDBForPeriph(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.ConfigMeta{})
	return db
}

// newManagerWithMock 创建使用 mock MQTT publisher 的 Manager
func newManagerWithMock(db *gorm.DB, mock *mockMQTTPublisher) *Manager {
	return &Manager{
		db:       db,
		mqtt:     mock,
		hashMgr:  NewConfigHashManager(),
		eventBus: NewConfigEventBus(64),
	}
}
