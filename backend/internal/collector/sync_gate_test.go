package collector

import (
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// testManagerHelper creates a minimal Manager with test DB for SyncGate tests.
func newTestManagerAndGate(t *testing.T) (*Manager, *SyncGate, *ConfigEventBus) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	db.AutoMigrate(&models.ConfigMeta{}, &models.Collector{}, &models.Channel{}, &models.ConfigTemplate{})

	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	bus := NewConfigEventBus(64, epochGen)

	// Create a Manager with the test DB (no MQTT, no WS)
	mgr := &Manager{
		db:      db,
		hashMgr: NewConfigHashManager(),
	}
	gate := NewSyncGate(mgr, bus)
	return mgr, gate, bus
}

// === OnHello tests ===

func TestOnHello_NvsEmpty_ForceSync(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	hello := &HelloMsg{
		DeviceID:     "dev1",
		NvsHasConfig: false, // NVS empty — should force full sync
	}
	d := gate.OnHello("dev1", hello)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull, got %d", d.Action)
	}
	if d.Reason != "nvs_empty_force_sync" {
		t.Fatalf("expected nvs_empty_force_sync, got %s", d.Reason)
	}
	if d.SyncID == "" {
		t.Fatal("SyncID should be set")
	}
}

func TestOnHello_EpochLag_ForceSync(t *testing.T) {
	_, gate, bus := newTestManagerAndGate(t)

	// Increment server epoch to be ahead of device
	bus.Publish(ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionUpdate, CollectorID: 1, EntityID: 1})
	time.Sleep(10 * time.Millisecond) // let async persist

	hello := &HelloMsg{
		DeviceID:     "dev1",
		NvsHasConfig: true,
		ConfigEpoch:  0, // device epoch = 0, server epoch > 0
	}
	d := gate.OnHello("dev1", hello)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for epoch lag, got %d", d.Action)
	}
	_ = bus // silence unused variable warning
}

func TestOnHello_ManifestMismatch_ForceSync(t *testing.T) {
	_, gate, bus := newTestManagerAndGate(t)

	// Make server epoch == device epoch (0 == 0 or match)
	hello := &HelloMsg{
		DeviceID:     "dev1",
		NvsHasConfig: true,
		ConfigEpoch:  bus.CurrentEpoch(),
		LastManifest: "old-manifest-id",
	}
	d := gate.OnHello("dev1", hello)
	// Should be Full due to manifest mismatch (unless hash happens to match)
	if d.Action == SyncActionNone {
		t.Fatal("expected non-None action when manifest mismatches")
	}
}

func TestOnHello_InSync_NoAction(t *testing.T) {
	mgr, gate, bus := newTestManagerAndGate(t)

	// Create a collector with known config
	collector := models.Collector{DeviceID: "dev1", Status: "online"}
	mgr.db.Create(&collector)

	// Compute the hash the device would report
	serverHash := mgr.CalcConfigHashForDevice("dev1")

	hello := &HelloMsg{
		DeviceID:     "dev1",
		NvsHasConfig: true,
		ConfigEpoch:  bus.CurrentEpoch(),
		LastManifest: serverHash.ManifestID,
	}

	// Pre-populate hashMgr so dedup skips
	mgr.hashMgr.ShouldSendConfig("dev1", serverHash.Hash)
	mgr.hashMgr.UpdateLastSent("dev1")

	d := gate.OnHello("dev1", hello)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for in-sync device, got %d (reason=%s)", d.Action, d.Reason)
	}
}

func TestOnHello_V2Firmware_LegacyPath(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	// v2.0 firmware: no epoch/nvs_has fields → defaults (epoch=0, nvs_has=true)
	hello := &HelloMsg{
		DeviceID:        "dev1",
		NvsHasConfig:    true,  // default for v2.0
		ConfigEpoch:     0,     // default for v2.0
		ProtocolVersion: "2.0",
	}
	d := gate.OnHello("dev1", hello)
	// With epoch=0 and server epoch > 0, should trigger epoch lag
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for legacy v2.0 device (epoch=0 < server), got %d", d.Action)
	}
}

// === OnStatusReport tests ===

func TestOnStatusReport_EpochLag_Push(t *testing.T) {
	_, gate, bus := newTestManagerAndGate(t)

	// Increment server epoch
	bus.Publish(ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionUpdate, CollectorID: 1, EntityID: 1})
	time.Sleep(10 * time.Millisecond)

	rpt := &StatusReportMsg{
		Status:      "online",
		ConfigEpoch: 0, // device epoch = 0, server > 0
	}
	d := gate.OnStatusReport("dev1", rpt)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for status epoch lag, got %d", d.Action)
	}
}

func TestOnStatusReport_InSync(t *testing.T) {
	_, gate, bus := newTestManagerAndGate(t)

	rpt := &StatusReportMsg{
		Status:      "online",
		ConfigEpoch: bus.CurrentEpoch(),
	}
	d := gate.OnStatusReport("dev1", rpt)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for in-sync status, got %d", d.Action)
	}
}

func TestOnStatusReport_OnlineWithEpochZero(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	// Device comes online with epoch 0, server has epoch > 0
	rpt := &StatusReportMsg{
		Status:      "online",
		ConfigEpoch: 0,
	}
	d := gate.OnStatusReport("dev1", rpt)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for epoch=0 status, got %d", d.Action)
	}
}

// === OnConfigChange tests ===

func TestOnConfigChange_PushToAffectedCollector(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a collector
	collector := models.Collector{DeviceID: "dev1", Status: "online"}
	mgr.db.Create(&collector)

	evt := ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionUpdate,
		CollectorID: collector.ID,
		EntityID:    100,
	}
	decisions := gate.OnConfigChange(evt)
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull, got %d", decisions[0].Action)
	}
	if decisions[0].DeviceID != "dev1" {
		t.Fatalf("expected deviceID=dev1, got %s", decisions[0].DeviceID)
	}
}

func TestOnConfigChange_NoDevice_Skip(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	evt := ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionUpdate,
		CollectorID: 9999, // non-existent
		EntityID:    100,
	}
	decisions := gate.OnConfigChange(evt)
	if len(decisions) != 0 {
		t.Fatalf("expected 0 decisions for missing collector, got %d", len(decisions))
	}
}

func TestOnConfigChange_EpochIncremented(t *testing.T) {
	mgr, gate, bus := newTestManagerAndGate(t)

	collector := models.Collector{DeviceID: "dev1", Status: "online"}
	mgr.db.Create(&collector)

	_ = bus.CurrentEpoch() // use bus
	before := bus.CurrentEpoch()
	evt := ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionUpdate,
		CollectorID: collector.ID,
		EntityID:    100,
	}
	// Publish increments epoch
	bus.Publish(evt)
	time.Sleep(10 * time.Millisecond)

	decisions := gate.OnConfigChange(evt)
	if len(decisions) > 0 && decisions[0].Epoch <= before {
		t.Fatalf("decision epoch should be > before: epoch=%d before=%d", decisions[0].Epoch, before)
	}
}

// === OnServerStartup tests ===

func TestOnServerStartup_PushAllOnline(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create online collectors
	mgr.db.Create(&models.Collector{DeviceID: "dev1", Status: "online"})
	mgr.db.Create(&models.Collector{DeviceID: "dev2", Status: "online"})
	mgr.db.Create(&models.Collector{DeviceID: "dev3", Status: "offline"})

	decisions := gate.OnServerStartup()
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions for online collectors, got %d", len(decisions))
	}
	for _, d := range decisions {
		if d.Action != SyncActionFull {
			t.Fatalf("expected SyncActionFull, got %d", d.Action)
		}
		if d.Reason != "server_startup_push" {
			t.Fatalf("expected server_startup_push, got %s", d.Reason)
		}
	}
}

func TestOnServerStartup_NoOnline(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	decisions := gate.OnServerStartup()
	if len(decisions) != 0 {
		t.Fatalf("expected 0 decisions with no online collectors, got %d", len(decisions))
	}
}

// === OnConfigQuery tests ===

func TestOnConfigQuery_Mismatch(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	q := &ConfigQueryMsg{
		Reason:           "periodic",
		CurrentEpoch:     0,
		CurrentManifestID: "old-manifest",
	}
	d := gate.OnConfigQuery("dev1", q)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for query mismatch, got %d", d.Action)
	}
}

func TestOnConfigQuery_InSync(t *testing.T) {
	mgr, gate, bus := newTestManagerAndGate(t)

	// Create collector
	mgr.db.Create(&models.Collector{DeviceID: "dev1", Status: "online"})

	serverHash := mgr.CalcConfigHashForDevice("dev1")

	q := &ConfigQueryMsg{
		Reason:            "periodic",
		CurrentEpoch:      bus.CurrentEpoch(),
		CurrentManifestID: serverHash.ManifestID,
	}
	d := gate.OnConfigQuery("dev1", q)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for in-sync query, got %d (reason=%s)", d.Action, d.Reason)
	}
}

// === OnOfflineReconnect tests ===

func TestOnOfflineReconnect_ForcePush(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	d := gate.OnOfflineReconnect("dev1")
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for offline reconnect, got %d", d.Action)
	}
	if d.Reason != "offline_reconnect_push" {
		t.Fatalf("expected offline_reconnect_push, got %s", d.Reason)
	}
}

// === OnFactoryReset tests ===

func TestOnFactoryReset_ForcePush(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	d := gate.OnFactoryReset("dev1")
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for factory reset, got %d", d.Action)
	}
	if d.Reason != "factory_reset_force_sync" {
		t.Fatalf("expected factory_reset_force_sync, got %s", d.Reason)
	}
}

// === Decision observability ===

func TestSyncDecision_AlwaysHasSyncID(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	decisions := []SyncDecision{
		gate.OnHello("dev1", &HelloMsg{NvsHasConfig: false}),
		gate.OnStatusReport("dev1", &StatusReportMsg{ConfigEpoch: 0}),
		gate.OnOfflineReconnect("dev1"),
		gate.OnFactoryReset("dev1"),
		gate.OnConfigQuery("dev1", &ConfigQueryMsg{CurrentEpoch: 0}),
	}

	for i, d := range decisions {
		if d.SyncID == "" {
			t.Fatalf("decision %d missing SyncID", i)
		}
		if d.Epoch == 0 {
			t.Fatalf("decision %d missing Epoch", i)
		}
	}
}
