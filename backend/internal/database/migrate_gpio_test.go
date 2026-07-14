package database

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("warn")
}

// setupMigrationDB creates an in-memory SQLite DB with all tables migrated
// and returns the *gorm.DB. It mirrors testutil.OpenTestDB but keeps the
// package-internal reference to DB for functions that use it.
func setupMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	allModels := []interface{}{
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
		&models.NodeLog{},
		&models.GPIOConfig{},
		&models.PWMConfig{},
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// createGPIOChannel is a helper to insert a legacy GPIO channel row.
func createGPIOChannel(db *gorm.DB, nodeID, busConfig, config string, enabled bool) models.Channel {
	ch := models.Channel{
		NodeID:       nodeID,
		HardwareType: "gpio",
		BusType:      "GPIO",
		BusConfig:    busConfig,
		Config:       config,
		Enabled:      enabled,
	}
	db.Create(&ch)
	return ch
}

// =====================================================================
// Success cases
// =====================================================================

func TestMigrateGPIOChannels_Success(t *testing.T) {
	db := setupMigrationDB(t)

	// Seed a node
	node := models.Node{NodeID: "NODE_A", Name: "test"}
	db.Create(&node)

	// Create a legacy GPIO channel: pin=5, direction=1 (OUTPUT)
	// bus_config hex: 05 01 → "0501"
	createGPIOChannel(db, "NODE_A", "0501", `{"label":"LED","initial_level":1}`, true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}

	if result.Scanned != 1 {
		t.Errorf("expected scanned=1, got %d", result.Scanned)
	}
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1, got %d", result.Migrated)
	}
	if result.Skipped != 0 {
		t.Errorf("expected skipped=0, got %d", result.Skipped)
	}

	// Verify the GPIOConfig was created correctly
	var cfg models.GPIOConfig
	if err := db.Where("node_id = ? AND pin = ?", "NODE_A", 5).First(&cfg).Error; err != nil {
		t.Fatalf("gpio_config not found: %v", err)
	}
	if cfg.Direction != 1 {
		t.Errorf("expected direction=1, got %d", cfg.Direction)
	}
	if cfg.InitialLevel != 1 {
		t.Errorf("expected initial_level=1, got %d", cfg.InitialLevel)
	}
	if cfg.Label != "LED" {
		t.Errorf("expected label=LED, got %s", cfg.Label)
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestMigrateGPIOChannels_DefaultsWhenConfigEmpty(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "NODE_B", Name: "test"}
	db.Create(&node)

	// Channel with bus_config (pin=2, direction=0) but no config JSON
	createGPIOChannel(db, "NODE_B", "0200", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 1 {
		t.Fatalf("expected migrated=1, got %d", result.Migrated)
	}

	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "NODE_B", 2).First(&cfg)
	if cfg.Direction != 0 {
		t.Errorf("expected direction=0 (INPUT), got %d", cfg.Direction)
	}
	if cfg.InitialLevel != 0 {
		t.Errorf("expected initial_level=0, got %d", cfg.InitialLevel)
	}
	if cfg.Label != "" {
		t.Errorf("expected empty label, got %s", cfg.Label)
	}
	if !cfg.Enabled {
		t.Error("expected enabled=true from channel")
	}
}

// =====================================================================
// Idempotency
// =====================================================================

func TestMigrateGPIOChannels_Idempotent(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "NODE_C", Name: "test"}
	db.Create(&node)

	createGPIOChannel(db, "NODE_C", "0a01", `{"label":"Relay"}`, true)

	// First run
	r1, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if r1.Migrated != 1 {
		t.Fatalf("first run: expected migrated=1, got %d", r1.Migrated)
	}

	// Second run — should skip, not duplicate
	r2, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if r2.Migrated != 0 {
		t.Errorf("second run: expected migrated=0, got %d", r2.Migrated)
	}
	if r2.Skipped != 1 {
		t.Errorf("second run: expected skipped=1, got %d", r2.Skipped)
	}

	// Verify only one GPIOConfig exists
	var count int64
	db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", "NODE_C", 10).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 gpio_config after two runs, got %d", count)
	}
}

// =====================================================================
// Multiple nodes, same pin
// =====================================================================

func TestMigrateGPIOChannels_MultipleNodesSamePin(t *testing.T) {
	db := setupMigrationDB(t)

	db.Create(&models.Node{NodeID: "N1", Name: "n1"})
	db.Create(&models.Node{NodeID: "N2", Name: "n2"})

	// Both nodes use pin 3, direction=1 (OUTPUT)
	createGPIOChannel(db, "N1", "0301", "", true)
	createGPIOChannel(db, "N2", "0301", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 2 {
		t.Fatalf("expected migrated=2, got %d", result.Migrated)
	}

	// Verify each node has its own GPIOConfig for pin 3
	var cfgs []models.GPIOConfig
	db.Where("pin = ?", 3).Find(&cfgs)
	if len(cfgs) != 2 {
		t.Errorf("expected 2 gpio_configs for pin=3 across nodes, got %d", len(cfgs))
	}
}

// =====================================================================
// Already has GPIOConfig (pre-existing, not from migration)
// =====================================================================

func TestMigrateGPIOChannels_PreExistingGPIOConfig(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N3", Name: "n3"}
	db.Create(&node)

	// Pre-create a GPIOConfig for pin 7
	db.Create(&models.GPIOConfig{NodeID: "N3", Pin: 7, Direction: 0, Enabled: true})

	// Legacy channel for the same pin
	createGPIOChannel(db, "N3", "0701", `{"label":"existing"}`, true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 0 {
		t.Errorf("expected migrated=0 (already exists), got %d", result.Migrated)
	}
	if result.Skipped != 1 {
		t.Errorf("expected skipped=1, got %d", result.Skipped)
	}

	// Verify the original GPIOConfig was not overwritten
	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "N3", 7).First(&cfg)
	if cfg.Direction != 0 {
		t.Errorf("expected direction=0 (original), got %d", cfg.Direction)
	}
	if cfg.Label != "" {
		t.Errorf("expected empty label (original), got %s", cfg.Label)
	}
}

// =====================================================================
// Bad data: invalid JSON config
// =====================================================================

func TestMigrateGPIOChannels_InvalidJSONConfig(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N4", Name: "n4"}
	db.Create(&node)

	// Invalid JSON in config, but valid bus_config
	createGPIOChannel(db, "N4", "0801", `{not valid json}`, true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}

	// Should still migrate (with defaults) — bad JSON is a warning, not fatal
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1 (bad JSON should use defaults), got %d", result.Migrated)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected at least one warning for invalid JSON")
	}

	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "N4", 8).First(&cfg)
	if cfg.Label != "" {
		t.Errorf("expected empty label from bad JSON, got %s", cfg.Label)
	}
}

// =====================================================================
// Bad data: missing bus_config
// =====================================================================

func TestMigrateGPIOChannels_MissingBusConfig(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N5", Name: "n5"}
	db.Create(&node)

	// Empty bus_config — cannot parse pin
	createGPIOChannel(db, "N5", "", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 0 {
		t.Errorf("expected migrated=0, got %d", result.Migrated)
	}
	if result.Skipped != 1 {
		t.Errorf("expected skipped=1, got %d", result.Skipped)
	}
}

// =====================================================================
// Bad data: bus_config too short (only 1 byte)
// =====================================================================

func TestMigrateGPIOChannels_ShortBusConfig(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N6", Name: "n6"}
	db.Create(&node)

	// Only 1 byte — pin is parseable but direction is not
	createGPIOChannel(db, "N6", "0c", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	// Pin can be parsed from 1 byte, so migration should proceed
	// with direction defaulting to 0 (INPUT).
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1 (pin parseable, direction defaults), got %d", result.Migrated)
	}

	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "N6", 12).First(&cfg)
	if cfg.Direction != 0 {
		t.Errorf("expected direction=0 (default), got %d", cfg.Direction)
	}
}

// =====================================================================
// Bad data: invalid hex in bus_config
// =====================================================================

func TestMigrateGPIOChannels_InvalidHexBusConfig(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N7", Name: "n7"}
	db.Create(&node)

	createGPIOChannel(db, "N7", "xyz-not-hex", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 0 {
		t.Errorf("expected migrated=0, got %d", result.Migrated)
	}
	if result.Skipped != 1 {
		t.Errorf("expected skipped=1, got %d", result.Skipped)
	}
}

// =====================================================================
// Bad data: invalid direction (>3) in bus_config
// =====================================================================

func TestMigrateGPIOChannels_InvalidDirection(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N8", Name: "n8"}
	db.Create(&node)

	// pin=1, direction=5 (invalid) → should default to 0 (INPUT)
	createGPIOChannel(db, "N8", "0105", "", true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1 (direction defaults), got %d", result.Migrated)
	}

	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "N8", 1).First(&cfg)
	if cfg.Direction != 0 {
		t.Errorf("expected direction=0 (default for invalid), got %d", cfg.Direction)
	}
}

// =====================================================================
// Non-GPIO channels are ignored
// =====================================================================

func TestMigrateGPIOChannels_NonGPIOChannelsIgnored(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N9", Name: "n9"}
	db.Create(&node)

	// I2C channel — should be ignored
	ch := models.Channel{
		NodeID:       "N9",
		HardwareType: "I2C",
		BusType:      "I2C",
		BusConfig:    "7600",
		Enabled:      true,
	}
	db.Create(&ch)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("expected scanned=0 (no GPIO channels), got %d", result.Scanned)
	}
	if result.Migrated != 0 {
		t.Errorf("expected migrated=0, got %d", result.Migrated)
	}
}

// =====================================================================
// Mixed: GPIO + non-GPIO channels
// =====================================================================

func TestMigrateGPIOChannels_MixedChannels(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N10", Name: "n10"}
	db.Create(&node)

	// GPIO channel
	createGPIOChannel(db, "N10", "0201", `{"label":"pin2"}`, true)
	// I2C channel
	db.Create(&models.Channel{NodeID: "N10", HardwareType: "I2C", BusType: "I2C", BusConfig: "7600", Enabled: true})
	// UART channel
	db.Create(&models.Channel{NodeID: "N10", HardwareType: "UART", BusType: "UART", BusConfig: "00", Enabled: true})

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Scanned != 1 {
		t.Errorf("expected scanned=1 (only GPIO), got %d", result.Scanned)
	}
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1, got %d", result.Migrated)
	}
}

// =====================================================================
// Channel preserved (not deleted)
// =====================================================================

func TestMigrateGPIOChannels_ChannelPreserved(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N11", Name: "n11"}
	db.Create(&node)

	ch := createGPIOChannel(db, "N11", "0501", `{"label":"LED"}`, true)

	_, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}

	// Verify the channel still exists and is unmodified
	var fetched models.Channel
	if err := db.First(&fetched, ch.ID).Error; err != nil {
		t.Fatalf("channel was deleted: %v", err)
	}
	if fetched.HardwareType != "gpio" {
		t.Errorf("channel hardware_type changed: %s", fetched.HardwareType)
	}
	if fetched.Enabled != true {
		t.Error("channel enabled flag changed")
	}
}

// =====================================================================
// Case-insensitive matching (GPIO vs gpio)
// =====================================================================

func TestMigrateGPIOChannels_CaseInsensitiveMatch(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N12", Name: "n12"}
	db.Create(&node)

	// Lowercase hardware_type and bus_type
	ch := models.Channel{
		NodeID:       "N12",
		HardwareType: "gpio",
		BusType:      "gpio",
		BusConfig:    "0301",
		Enabled:      true,
	}
	db.Create(&ch)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Scanned != 1 {
		t.Errorf("expected scanned=1 (case-insensitive match), got %d", result.Scanned)
	}
	if result.Migrated != 1 {
		t.Errorf("expected migrated=1, got %d", result.Migrated)
	}
}

// =====================================================================
// config JSON with enabled=false
// =====================================================================

func TestMigrateGPIOChannels_ConfigEnabledFalse(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N13", Name: "n13"}
	db.Create(&node)

	// Channel enabled=true, but config JSON says enabled=false
	createGPIOChannel(db, "N13", "0601", `{"enabled":false}`, true)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Migrated != 1 {
		t.Fatalf("expected migrated=1, got %d", result.Migrated)
	}

	var cfg models.GPIOConfig
	db.Where("node_id = ? AND pin = ?", "N13", 6).First(&cfg)
	// Config JSON's enabled=false should override channel's enabled=true
	if cfg.Enabled {
		t.Error("expected enabled=false (from config JSON)")
	}
}

// =====================================================================
// No GPIO channels at all
// =====================================================================

func TestMigrateGPIOChannels_NoGPIOChannels(t *testing.T) {
	db := setupMigrationDB(t)

	result, err := MigrateGPIOChannels(db)
	if err != nil {
		t.Fatalf("MigrateGPIOChannels: %v", err)
	}
	if result.Scanned != 0 || result.Migrated != 0 || result.Skipped != 0 {
		t.Errorf("expected all zeros for empty DB, got %+v", result)
	}
}

// =====================================================================
// Helper unit tests
// =====================================================================

func TestParseGPIOPinFromBusConfig(t *testing.T) {
	cases := []struct {
		input    string
		wantPin  int
		wantOk   bool
	}{
		{"0501", 5, true},
		{"0c", 12, true},
		{"ff", 255, true},
		{"", 0, false},
		{"xyz", 0, false},
		{"0", 0, false},  // odd-length hex
	}
	for _, tc := range cases {
		pin, ok := parseGPIOPinFromBusConfig(tc.input)
		if pin != tc.wantPin || ok != tc.wantOk {
			t.Errorf("parseGPIOPinFromBusConfig(%q) = (%d, %v), want (%d, %v)",
				tc.input, pin, ok, tc.wantPin, tc.wantOk)
		}
	}
}

func TestParseGPIODirectionFromBusConfig(t *testing.T) {
	cases := []struct {
		input     string
		wantDir   uint8
		wantOk    bool
	}{
		{"0500", 0, true},   // INPUT
		{"0501", 1, true},   // OUTPUT
		{"0502", 2, true},   // INPUT_PULLUP
		{"0503", 3, true},   // INPUT_PULLDOWN
		{"0504", 0, false},  // invalid direction → not ok
		{"05", 0, false},    // too short
		{"", 0, false},
		{"xyz", 0, false},
	}
	for _, tc := range cases {
		dir, ok := parseGPIODirectionFromBusConfig(tc.input)
		if dir != tc.wantDir || ok != tc.wantOk {
			t.Errorf("parseGPIODirectionFromBusConfig(%q) = (%d, %v), want (%d, %v)",
				tc.input, dir, ok, tc.wantDir, tc.wantOk)
		}
	}
}

// =====================================================================
// Transaction error: DB closed mid-migration
// =====================================================================

func TestMigrateGPIOChannels_DBError(t *testing.T) {
	db := setupMigrationDB(t)

	node := models.Node{NodeID: "N14", Name: "n14"}
	db.Create(&node)

	createGPIOChannel(db, "N14", "0501", "", true)

	// Close the underlying SQL DB to force transaction errors
	sqlDB, _ := db.DB()
	sqlDB.Close()

	_, err := MigrateGPIOChannels(db)
	if err == nil {
		t.Fatal("expected error from closed DB, got nil")
	}
}
