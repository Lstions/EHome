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
	db.AutoMigrate(&models.Node{}, &models.NodeEvent{}, &models.ConfigMeta{})
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

	r1 := mgr.buildHashData(tpl, ch, ed, dc, dma)
	r2 := mgr.buildHashData(tpl, ch, ed, dc, dma)
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
	r := mgr.buildHashData(nil, nil, nil, nil, nil)
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

	base := mgr.buildHashData(tpl, ch, nil, nil, nil)

	ed := []models.EdgeDevice{{ID: 1, DeviceConfigID: 5, HardwareID: "x", IntervalMs: 100, Enabled: true, ChannelID: 1, Type: "tA", Name: "a"}}
	dc := []models.DeviceConfig{{ID: 5, DeviceType: "tA", DeviceModel: "mA", Connection: []byte("uart"), Parser: []byte("bin"), InitFlow: []byte(""), Operations: []byte("[]"), Status: "active"}}
	changed := mgr.buildHashData(tpl, ch, ed, dc, nil)

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

	r1 := mgr.buildHashData(tpl, ch, ed1, dc, nil)
	r2 := mgr.buildHashData(tpl, ch, ed2, dc, nil)
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

// TestCalcConfigHashForDevice_NotFound verifies error handling on missing node.
func TestCalcConfigHashForDevice_NotFound(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewManager(db, nil, nil, nil, nil, nil)
	r := mgr.CalcConfigHashForDevice("nonexistent-node")
	if r.Hash != "" || r.ManifestID != "" {
		t.Error("Expected empty result for nonexistent node")
	}
}
