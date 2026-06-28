package nodemgr

import (
	"testing"

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
	db.AutoMigrate(&models.ConfigMeta{}, &models.Node{}, &models.Channel{}, &models.ConfigTemplate{})

	bus := NewConfigEventBus(64)

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
		NodeID:      "dev1",
		NvsHasConfig: false, // NVS empty — should force full sync
	}
	d := gate.OnHello("dev1", hello)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull, got %d", d.Action)
	}
	if d.Reason != "nvs_empty" {
		t.Fatalf("expected nvs_empty, got %s", d.Reason)
	}
	if d.SyncID == "" {
		t.Fatal("SyncID should be set")
	}
}

func TestOnHello_HashMismatch_Push(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node so CalcConfigHashForDevice returns a non-empty hash
	mgr.db.Create(&models.Node{NodeID: "dev1", Status: "online"})

	hello := &HelloMsg{
		NodeID:       "dev1",
		NvsHasConfig: true,
		LastManifest: "old-manifest-id",
	}
	d := gate.OnHello("dev1", hello)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for hash mismatch, got %d (reason=%s)", d.Action, d.Reason)
	}
}

func TestOnHello_InSync_NoAction(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node with known config
	nodeRecord := models.Node{NodeID: "4001", Status: "online"}
	mgr.db.Create(&nodeRecord)

	// Compute the hash the device would report
	deviceID := "4001"
	serverHash := mgr.CalcConfigHashForDevice(deviceID)

	hello := &HelloMsg{
		NodeID:      deviceID,
		NvsHasConfig: true,
		LastManifest: serverHash.ManifestID, // device reports same ManifestID as server
	}

	d := gate.OnHello(deviceID, hello)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for in-sync device, got %d (reason=%s)", d.Action, d.Reason)
	}
}

func TestOnHello_V2Firmware_LegacyPath(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	// v2.0 firmware: no epoch/nvs_has fields → defaults (epoch=0, nvs_has=true)
	hello := &HelloMsg{
		NodeID:          "dev1",
		NvsHasConfig:    true,  // default for v2.0
		ConfigEpoch:     0,     // default for v2.0
		ProtocolVersion: "2.0",
	}
	d := gate.OnHello("dev1", hello)
	// With empty LastManifest and no server config, should be no_server_config or hash_mismatch
	if d.Reason != "no_server_config" && d.Reason != "hash_mismatch" {
		// Either is acceptable depending on whether node exists in DB
		t.Fatalf("expected no_server_config or hash_mismatch, got %s", d.Reason)
	}
}

// === OnStatusReport tests ===

func TestOnStatusReport_EmptyConfigHash_Skip(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	// Old firmware: no config_hash field → short-circuit skip
	rpt := &StatusReportMsg{
		Status:     "online",
		ConfigHash: "", // empty → skip
	}
	d := gate.OnStatusReport("dev1", rpt)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for empty config_hash, got %d", d.Action)
	}
	if d.Reason != "no_config_hash_wait_for_hello" {
		t.Fatalf("expected no_config_hash_wait_for_hello, got %s", d.Reason)
	}
}

func TestOnStatusReport_ConfigHashMatch(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node
	nodeRecord := models.Node{NodeID: "dev2", Status: "online"}
	mgr.db.Create(&nodeRecord)

	serverHash := mgr.CalcConfigHashForDevice("dev2")
	rpt := &StatusReportMsg{
		Status:       "online",
		ConfigHash:   serverHash.ManifestID, // device reports same ManifestID as server
		ChannelCount: uint64(serverHash.ChannelCount),
	}
	d := gate.OnStatusReport("dev2", rpt)
	if d.Action != SyncActionNone {
		t.Fatalf("expected SyncActionNone for hash match, got %d (reason=%s)", d.Action, d.Reason)
	}
}

func TestOnStatusReport_ConfigHashMismatch(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node
	nodeRecord := models.Node{NodeID: "dev3", Status: "online"}
	mgr.db.Create(&nodeRecord)

	rpt := &StatusReportMsg{
		Status:     "online",
		ConfigHash: "wrong_hash",
	}
	d := gate.OnStatusReport("dev3", rpt)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for hash mismatch, got %d", d.Action)
	}
}

// === OnConfigChange tests ===

func TestOnConfigChange_PushToAffectedCollector(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node
	nodeRecord := models.Node{NodeID: "4002", Status: "online"}
	mgr.db.Create(&nodeRecord)

	evt := ConfigChangeEvent{
		Type:     CfgChangeChannel,
		Action:   CfgActionUpdate,
		NodeID:   "4002",
		EntityID: "100",
	}
	decisions := gate.OnConfigChange(evt)
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull, got %d", decisions[0].Action)
	}
	if decisions[0].DeviceID != "4002" {
		t.Fatalf("expected deviceID=4002, got %s", decisions[0].DeviceID)
	}
}

// === OnServerStartup tests ===

func TestOnServerStartup_PushesToOnlineNodes(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create online nodes
	mgr.db.Create(&models.Node{NodeID: "5001", Status: "online"})
	mgr.db.Create(&models.Node{NodeID: "5002", Status: "online"})
	// Also create an offline node — should NOT get a decision
	mgr.db.Create(&models.Node{NodeID: "5003", Status: "offline"})

	decisions := gate.OnServerStartup()
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions for 2 online nodes, got %d", len(decisions))
	}
	deviceIDs := make(map[string]bool)
	for _, d := range decisions {
		if d.Action != SyncActionFull {
			t.Fatalf("expected SyncActionFull for %s, got %d", d.DeviceID, d.Action)
		}
		if d.Reason != "server_startup" {
			t.Fatalf("expected server_startup reason for %s, got %s", d.DeviceID, d.Reason)
		}
		deviceIDs[d.DeviceID] = true
	}
	if !deviceIDs["5001"] || !deviceIDs["5002"] {
		t.Fatalf("missing expected device in decisions, got: %v", deviceIDs)
	}
}

func TestOnServerStartup_NoOnlineNodes(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Only offline nodes
	mgr.db.Create(&models.Node{NodeID: "5003", Status: "offline"})

	decisions := gate.OnServerStartup()
	if len(decisions) != 0 {
		t.Fatalf("expected 0 decisions for 0 online nodes, got %d", len(decisions))
	}
}

// === OnConfigQuery tests ===

func TestOnConfigQuery_Mismatch(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	// Create a node so CalcConfigHashForDevice returns a non-empty hash
	mgr.db.Create(&models.Node{NodeID: "dev1", Status: "online"})

	q := &ConfigQueryMsg{
		Reason:            "periodic",
		CurrentManifestID: "old-manifest",
	}
	d := gate.OnConfigQuery("dev1", q)
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for query mismatch, got %d (reason=%s)", d.Action, d.Reason)
	}
}

func TestOnConfigQuery_InSync(t *testing.T) {
	mgr, gate, _ := newTestManagerAndGate(t)

	deviceID := "6001"
	mgr.db.Create(&models.Node{NodeID: "6001", Status: "online"})

	serverHash := mgr.CalcConfigHashForDevice(deviceID)

	q := &ConfigQueryMsg{
		Reason:            "periodic",
		CurrentManifestID: serverHash.ManifestID, // device reports matching ManifestID
	}
	d := gate.OnConfigQuery(deviceID, q)
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
	if d.Reason != "offline_reconnect" {
		t.Fatalf("expected offline_reconnect, got %s", d.Reason)
	}
}

// === OnFactoryReset tests ===

func TestOnFactoryReset_ForcePush(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	d := gate.OnFactoryReset("dev1")
	if d.Action != SyncActionFull {
		t.Fatalf("expected SyncActionFull for factory reset, got %d", d.Action)
	}
	if d.Reason != "factory_reset" {
		t.Fatalf("expected factory_reset, got %s", d.Reason)
	}
}

// === Decision observability ===

func TestSyncDecision_AlwaysHasSyncID(t *testing.T) {
	_, gate, _ := newTestManagerAndGate(t)

	decisions := []SyncDecision{
		gate.OnHello("dev1", &HelloMsg{NvsHasConfig: false}),
		gate.OnStatusReport("dev1", &StatusReportMsg{ConfigHash: ""}), // empty hash → skip
		gate.OnOfflineReconnect("dev1"),
		gate.OnFactoryReset("dev1"),
		gate.OnConfigQuery("dev1", &ConfigQueryMsg{CurrentManifestID: ""}),
	}

	for i, d := range decisions {
		if d.SyncID == "" {
			t.Fatalf("decision %d missing SyncID", i)
		}
	}
}
