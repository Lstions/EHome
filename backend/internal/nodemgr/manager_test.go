package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{}, &models.ConfigMeta{},
		&models.Channel{}, &models.ConfigTemplate{}, &models.EdgeDevice{},
		&models.DeviceConfig{}, &models.GPIOConfig{}, &models.PWMConfig{})
	return db
}

// TestConfigHashManager_Deterministic verifies same input → same output.
func TestConfigHashManager_Deterministic(t *testing.T) {
	hm := NewConfigHashManager()
	h1 := hm.CalcConfigHash([]byte("node-X-v1"))
	h2 := hm.CalcConfigHash([]byte("node-X-v1"))
	if h1 != h2 {
		t.Errorf("Expected identical hashes, got %s vs %s", h1, h2)
	}
}

// TestConfigHashManager_OutputFormat verifies hash is a non-empty string.
func TestConfigHashManager_OutputFormat(t *testing.T) {
	hm := NewConfigHashManager()
	hash := hm.CalcConfigHash([]byte("test-input"))
	if len(hash) == 0 {
		t.Error("Expected non-empty hash, got empty string")
	}
}

// TestConfigHashManager_CollisionResistance verifies different input → diff hash.
func TestConfigHashManager_CollisionResistance(t *testing.T) {
	hm := NewConfigHashManager()
	h1 := hm.CalcConfigHash([]byte("version-A"))
	h2 := hm.CalcConfigHash([]byte("version-B"))
	if h1 == h2 {
		t.Error("Collision: different inputs produced identical hash")
	}
}

// TestConfigHashManager_EmptyInput verifies empty input produces non-empty hash.
func TestConfigHashManager_EmptyInput(t *testing.T) {
	hm := NewConfigHashManager()
	h := hm.CalcConfigHash([]byte{})
	if h == "" {
		t.Error("Empty input should produce non-empty hash")
	}
}

// TestBuildHashData_Deterministic via direct Manager method call.
func TestBuildHashData_Deterministic(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	tpl := []models.ConfigTemplate{{ID: 1, WriteData: "0x01", ReadLength: 1, DelayMs: 100}}
	ch := []models.Channel{{ID: 10, HardwareID: "hw1", TemplateIDs: "1", IntervalMs: 1000, Enabled: true, BusConfig: "{}"}}
	ed := []models.EdgeDevice{{ID: 100, DeviceConfigID: 5, HardwareID: "hwX", IntervalMs: 500, Enabled: true, ChannelID: 1, Type: "tA", Name: "d1"}}
	dc := []models.DeviceConfig{{ID: 5, DeviceType: "tA", DeviceModel: "mA", Connection: []byte("uart"), Parser: []byte("bin"), InitFlow: []byte(""), Operations: []byte("[]"), Status: "active"}}
	dma := []models.DmaChannelConfig{{DmaID: 1, Enabled: true, BindTo: "ch1"}}

	r1 := mgr.buildHashData(tpl, ch, ed, dc, dma, nil, nil)
	r2 := mgr.buildHashData(tpl, ch, ed, dc, dma, nil, nil)
	if len(r1) == 0 {
		t.Fatal("empty hash data")
	}
	if string(r1) != string(r2) {
		t.Error("buildHashData not deterministic")
	}
}

// TestBuildHashData_EmptyInput verifies nil slices don't crash.
func TestBuildHashData_EmptyInput(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	r := mgr.buildHashData(nil, nil, nil, nil, nil, nil, nil)
	// nil input returns nil (var buf []byte with no appends = nil)
	// This is expected behavior — just verify no panic
	_ = r
}

// TestBuildHashData_SectionChange verifies adding edgeDevices changes hash.
func TestBuildHashData_SectionChange(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	tpl := []models.ConfigTemplate{{ID: 1, WriteData: "0x01", ReadLength: 1, DelayMs: 100}}
	ch := []models.Channel{{ID: 10, HardwareID: "hw1", TemplateIDs: "1", IntervalMs: 1000, Enabled: true, BusConfig: "{}"}}

	base := mgr.buildHashData(tpl, ch, nil, nil, nil, nil, nil)

	ed := []models.EdgeDevice{{ID: 1, DeviceConfigID: 5, HardwareID: "x", IntervalMs: 100, Enabled: true, ChannelID: 1, Type: "tA", Name: "a"}}
	dc := []models.DeviceConfig{{ID: 5, DeviceType: "tA", DeviceModel: "mA", Connection: []byte("uart"), Parser: []byte("bin"), InitFlow: []byte(""), Operations: []byte("[]"), Status: "active"}}
	changed := mgr.buildHashData(tpl, ch, ed, dc, nil, nil, nil)

	if string(base) == string(changed) {
		t.Error("Adding edgeDevices+deviceConfigs must change hash output")
	}
}

// TestBuildHashData_DedupDeviceConfigID verifies that different edge device entries
// with the same DeviceConfigID produce different hash data (because edge device
// fields like HardwareID/IntervalMs/Enabled are included in the hash).
func TestBuildHashData_DedupDeviceConfigID(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	tpl := []models.ConfigTemplate{{ID: 1, WriteData: "0x01", ReadLength: 1, DelayMs: 100}}
	ch := []models.Channel{{ID: 10, HardwareID: "hw1", TemplateIDs: "1", IntervalMs: 1000, Enabled: true, BusConfig: "{}"}}
	dc := []models.DeviceConfig{{ID: 5, DeviceType: "tA", DeviceModel: "mA", Connection: []byte("uart"), Parser: []byte("bin"), InitFlow: []byte(""), Operations: []byte("[]"), Status: "active"}}

	ed1 := []models.EdgeDevice{
		{ID: 1, DeviceConfigID: 5, HardwareID: "x", IntervalMs: 100, Enabled: true, ChannelID: 1, Type: "tA", Name: "a"},
	}
	ed2 := []models.EdgeDevice{
		{ID: 3, DeviceConfigID: 5, HardwareID: "z", IntervalMs: 300, Enabled: true, ChannelID: 1, Type: "tA", Name: "c"},
	}

	r1 := mgr.buildHashData(tpl, ch, ed1, dc, nil, nil, nil)
	r2 := mgr.buildHashData(tpl, ch, ed2, dc, nil, nil, nil)
	// Different edge device content → different hash data (expected behavior)
	if string(r1) == string(r2) {
		t.Error("Different edge device entries should produce different hash data")
	}
}

// TestStop_ClosesStopChannel verifies Stop closes the manager's stop channel.
func TestStop_ClosesStopChannel(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	mgr.Stop()

	select {
	case <-mgr.stopCh:
		// expected
	default:
		t.Error("Stop did not close stopCh")
	}
}

func TestLoadInitializableEdgeDevicesRequiresMatchingEnabledTransport(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	db.Create(&models.Node{NodeID: "node-1", Status: "online"})
	db.Create(&models.Node{NodeID: "node-2", Status: "online"})
	channels := []models.Channel{
		{NodeID: "node-1", HardwareType: "I2C", BusType: "I2C", Enabled: true},
		{NodeID: "node-1", HardwareType: "UART", BusType: "UART", Enabled: false},
		{NodeID: "node-1", HardwareType: "GPIO", BusType: "4", Enabled: true},
		{NodeID: "node-1", HardwareType: "6", BusType: "PWM", Enabled: true},
		{NodeID: "node-2", HardwareType: "I2C", BusType: "I2C", Enabled: true},
	}
	for i := range channels {
		if err := db.Create(&channels[i]).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := db.Model(&models.Channel{}).Where("id = ?", channels[i].ID).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	edges := []models.EdgeDevice{
		{Name: "valid", Type: "bmp280", NodeID: "node-1", ChannelID: channels[0].ID, Enabled: true},
		{Name: "disabled-edge", Type: "bmp280", NodeID: "node-1", ChannelID: channels[0].ID, Enabled: false},
		{Name: "disabled-channel", Type: "bmp280", NodeID: "node-1", ChannelID: channels[1].ID, Enabled: true},
		{Name: "gpio-alias", Type: "bmp280", NodeID: "node-1", ChannelID: channels[2].ID, Enabled: true},
		{Name: "pwm-alias", Type: "bmp280", NodeID: "node-1", ChannelID: channels[3].ID, Enabled: true},
		{Name: "edge-channel-node-mismatch", Type: "bmp280", NodeID: "node-1", ChannelID: channels[4].ID, Enabled: true},
		{Name: "other-node", Type: "bmp280", NodeID: "node-2", ChannelID: channels[4].ID, Enabled: true},
	}
	for i := range edges {
		if err := db.Create(&edges[i]).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := db.Model(&models.EdgeDevice{}).Where("id = ?", edges[i].ID).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := mgr.loadInitializableEdgeDevices("node-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "valid" {
		t.Fatalf("expected only valid edge device, got %+v", got)
	}
}

// TestCalcConfigHashForDevice_NotFound verifies error handling on missing node.
func TestCalcConfigHashForDevice_NotFound(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	r := mgr.CalcConfigHashForDevice("nonexistent-node")
	if r.Hash != "" || r.ManifestID != "" {
		t.Error("Expected empty result for nonexistent node")
	}
}

// =====================================================================
// GPIO/PWM config hash 测试 (v3.0)
// =====================================================================

// setupTestDBWithPeriph 创建包含 GPIO/PWM 模型的测试 DB
func setupTestDBWithPeriph(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.NodeEvent{}, &models.ConfigMeta{},
		&models.Channel{}, &models.ConfigTemplate{}, &models.EdgeDevice{},
		&models.DeviceConfig{}, &models.GPIOConfig{}, &models.PWMConfig{},
	)
	return db
}

// TestBuildHashData_GPIOConfigs 验证 GPIO 配置参与 hash 计算
func TestBuildHashData_GPIOConfigs(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)

	gpio1 := []models.GPIOConfig{{Pin: 2, Direction: 1, InitialLevel: 0, Enabled: true}}
	gpio2 := []models.GPIOConfig{{Pin: 2, Direction: 1, InitialLevel: 1, Enabled: true}} // initial_level 不同

	r1 := mgr.buildHashData(nil, nil, nil, nil, nil, gpio1, nil)
	r2 := mgr.buildHashData(nil, nil, nil, nil, nil, gpio2, nil)

	if string(r1) == string(r2) {
		t.Error("不同 GPIO initial_level 应产生不同 hash data")
	}
}

// TestBuildHashData_PWMConfigs 验证 PWM 配置参与 hash 计算
func TestBuildHashData_PWMConfigs(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)

	pwm1 := []models.PWMConfig{{HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, Enabled: true}}
	pwm2 := []models.PWMConfig{{HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 2000, Duty: 500, Resolution: 14, Enabled: true}} // frequency 不同

	r1 := mgr.buildHashData(nil, nil, nil, nil, nil, nil, pwm1)
	r2 := mgr.buildHashData(nil, nil, nil, nil, nil, nil, pwm2)

	if string(r1) == string(r2) {
		t.Error("不同 PWM frequency 应产生不同 hash data")
	}
}

func TestBuildHashData_PWMResourceIdentityAndRuntimeConfig(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	base := models.PWMConfig{
		HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000,
		Duty: 500, Resolution: 14, AutoStart: false, Enabled: true,
	}
	tests := []struct {
		name   string
		mutate func(*models.PWMConfig)
	}{
		{"hardware_id", func(p *models.PWMConfig) { p.HardwareID = "PWM1" }},
		{"channel", func(p *models.PWMConfig) { p.Channel = 1 }},
		{"pin", func(p *models.PWMConfig) { p.Pin = 7 }},
		{"frequency", func(p *models.PWMConfig) { p.Frequency++ }},
		{"duty", func(p *models.PWMConfig) { p.Duty++ }},
		{"resolution", func(p *models.PWMConfig) { p.Resolution-- }},
		{"auto_start", func(p *models.PWMConfig) { p.AutoStart = true }},
		{"enabled", func(p *models.PWMConfig) { p.Enabled = false }},
	}
	baseline := string(mgr.buildHashData(nil, nil, nil, nil, nil, nil, []models.PWMConfig{base}))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.mutate(&changed)
			got := string(mgr.buildHashData(nil, nil, nil, nil, nil, nil, []models.PWMConfig{changed}))
			if got == baseline {
				t.Fatalf("changing %s must change PWM hash data", tt.name)
			}
		})
	}
}

// TestBuildHashData_GPIOAndPWM 验证 GPIO 和 PWM 都参与 hash
func TestBuildHashData_GPIOAndPWM(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)

	gpio := []models.GPIOConfig{{Pin: 2, Direction: 1, InitialLevel: 0, Enabled: true}}
	pwm := []models.PWMConfig{{HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, Enabled: true}}

	r1 := mgr.buildHashData(nil, nil, nil, nil, nil, gpio, nil)
	r2 := mgr.buildHashData(nil, nil, nil, nil, nil, gpio, pwm)

	if string(r1) == string(r2) {
		t.Error("添加 PWM 配置应改变 hash data")
	}
}

// TestBuildHashData_GPIOEnabledFlag 验证 GPIO enabled 状态影响 hash
func TestBuildHashData_GPIOEnabledFlag(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)

	gpioEnabled := []models.GPIOConfig{{Pin: 2, Direction: 1, Enabled: true}}
	gpioDisabled := []models.GPIOConfig{{Pin: 2, Direction: 1, Enabled: false}}

	r1 := mgr.buildHashData(nil, nil, nil, nil, nil, gpioEnabled, nil)
	r2 := mgr.buildHashData(nil, nil, nil, nil, nil, gpioDisabled, nil)

	if string(r1) == string(r2) {
		t.Error("GPIO enabled 状态不同应产生不同 hash data")
	}
}

// TestBuildHashData_PWMResolution 验证 PWM resolution 影响 hash
func TestBuildHashData_PWMResolution(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)

	pwm14 := []models.PWMConfig{{HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 14, Enabled: true}}
	pwm12 := []models.PWMConfig{{HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Duty: 500, Resolution: 12, Enabled: true}}

	r1 := mgr.buildHashData(nil, nil, nil, nil, nil, nil, pwm14)
	r2 := mgr.buildHashData(nil, nil, nil, nil, nil, nil, pwm12)

	if string(r1) == string(r2) {
		t.Error("不同 PWM resolution 应产生不同 hash data")
	}
}

// TestCalcConfigHashForDevice_WithGPIO 验证 CalcConfigHashForDevice 包含 GPIO 配置
func TestCalcConfigHashForDevice_WithGPIO(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	db.Create(&models.Node{NodeID: "dev1", Status: "online"})

	// 先计算无 GPIO 的 hash
	r1 := mgr.CalcConfigHashForDevice("dev1")

	// 添加 GPIO 配置
	db.Create(&models.GPIOConfig{NodeID: "dev1", Pin: 2, Direction: 1, Enabled: true})

	r2 := mgr.CalcConfigHashForDevice("dev1")

	if r1.Hash == r2.Hash {
		t.Error("添加 GPIO 配置应改变 config hash")
	}
}

// TestCalcConfigHashForDevice_WithPWM 验证 CalcConfigHashForDevice 包含 PWM 配置
func TestCalcConfigHashForDevice_WithPWM(t *testing.T) {
	db := setupTestDBWithPeriph(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	db.Create(&models.Node{NodeID: "dev1", Status: "online"})

	r1 := mgr.CalcConfigHashForDevice("dev1")

	db.Create(&models.PWMConfig{NodeID: "dev1", HardwareID: "PWM0", Channel: 0, Pin: 6, Frequency: 1000, Resolution: 14, Enabled: true})

	r2 := mgr.CalcConfigHashForDevice("dev1")

	if r1.Hash == r2.Hash {
		t.Error("添加 PWM 配置应改变 config hash")
	}
}
