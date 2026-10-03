package drivers

import (
	"encoding/json"
	"testing"
)

// ============================================================================
// Techfine GB3024 Inverter Driver Tests
// ============================================================================

// findSensor returns the SensorData with the given name, or nil.
func findSensor(data []SensorData, name string) *SensorData {
	for i := range data {
		if data[i].Name == name {
			return &data[i]
		}
	}
	return nil
}

// assertFloat checks that a sensor's value matches expected within tolerance.
func assertFloat(t *testing.T, data []SensorData, name string, want float64, tolerance float64) {
	t.Helper()
	s := findSensor(data, name)
	if s == nil {
		t.Errorf("sensor %q not found in results", name)
		return
	}
	diff := s.Value - want
	if diff < -tolerance || diff > tolerance {
		t.Errorf("sensor %q: got %f, want %f (±%f)", name, s.Value, want, tolerance)
	}
}

// assertString checks that a sensor's StringValue matches expected.
func assertString(t *testing.T, data []SensorData, name string, want string) {
	t.Helper()
	s := findSensor(data, name)
	if s == nil {
		t.Errorf("sensor %q not found in results", name)
		return
	}
	if s.StringValue != want {
		t.Errorf("sensor %q: got %q, want %q", name, s.StringValue, want)
	}
}

// ============================================================================
// 1. TestHSTS — Status query parsing
// ============================================================================

func TestTechfine_HSTS_Normal(t *testing.T) {
	// (00 P000000000000 — no faults, power-on mode, no alarms
	raw := []byte("(00 P000000000000\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "fault_code", 0, 0.01)
	// work_mode is now numeric: P=0
	assertFloat(t, data, "work_mode", 0, 0.01)

	// All alarms should be 0
	for _, name := range []string{
		"alarm_pv_to_load", "alarm_output", "alarm_battery_low", "alarm_battery_missing",
		"alarm_overload", "alarm_overtemp", "alarm_eeprom_data", "alarm_eeprom_rw",
		"alarm_pv_low", "alarm_input_overvoltage", "alarm_battery_overvoltage", "alarm_fan_error",
	} {
		assertFloat(t, data, name, 0, 0.01)
	}
}

func TestTechfine_HSTS_WithAlarms(t *testing.T) {
	// (10 L100010000000 — fault code 10 (decimal), line mode, alarms: A(pv_to_load) and E(overload) active
	raw := []byte("(10 L100010000000\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "fault_code", 10, 0.01) // decimal 10
	// work_mode L=2
	assertFloat(t, data, "work_mode", 2, 0.01)

	// A (position 0) = '1' → active
	assertFloat(t, data, "alarm_pv_to_load", 1, 0.01)
	// B (position 1) = '0' → inactive
	assertFloat(t, data, "alarm_output", 0, 0.01)
	// C (position 2) = '0' → inactive
	assertFloat(t, data, "alarm_battery_low", 0, 0.01)
	// D (position 3) = '0' → inactive
	assertFloat(t, data, "alarm_battery_missing", 0, 0.01)
	// E (position 4) = '1' → active
	assertFloat(t, data, "alarm_overload", 1, 0.01)
	// F (position 5) = '0' → inactive
	assertFloat(t, data, "alarm_overtemp", 0, 0.01)
}

func TestTechfine_HSTS_BatteryMode(t *testing.T) {
	// (00 B000000000001 — battery mode, fan error (L flag active)
	raw := []byte("(00 B000000000001\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	// work_mode B=3
	assertFloat(t, data, "work_mode", 3, 0.01)
	assertFloat(t, data, "alarm_fan_error", 1, 0.01)
}

// ============================================================================
// 2. TestHGRID — Grid info parsing
// ============================================================================

func TestTechfine_HGRID(t *testing.T) {
	// (230.0 50.0 184 253 47 53 — 230V, 50Hz, loss voltage 184-253, freq 47-53
	raw := []byte("(230.0 50.0 184 253 47 53\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "grid_voltage", 230.0, 0.01)
	assertFloat(t, data, "grid_frequency", 50.0, 0.01)
}

func TestTechfine_HGRID_WithTrailingData(t *testing.T) {
	// Trailing OOO data should be ignored
	raw := []byte("(220.0 60.0 190 264 57 63 EXTRA123\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "grid_voltage", 220.0, 0.01)
	assertFloat(t, data, "grid_frequency", 60.0, 0.01)
}

// ============================================================================
// 3. TestHOP — Output info parsing
// ============================================================================

func TestTechfine_HOP(t *testing.T) {
	// (230.0 50.0 01000 00800 050 000 12345 005.0
	raw := []byte("(230.0 50.0 01000 00800 050 000 12345 005.0\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "output_voltage", 230.0, 0.01)
	assertFloat(t, data, "output_frequency", 50.0, 0.01)
	assertFloat(t, data, "output_apparent_power", 1000, 0.01)
	assertFloat(t, data, "output_active_power", 800, 0.01)
	assertFloat(t, data, "output_load_percent", 50, 0.01)
}

// ============================================================================
// 4. TestHBAT — Battery info parsing
// ============================================================================

func TestTechfine_HBAT(t *testing.T) {
	// (12 053.2 080 010 00005 380 00000 — 12 cells, 53.2V, 80%, 10A charge, 5A discharge, 380V bus
	raw := []byte("(12 053.2 080 010 00005 380 00000\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "battery_voltage", 53.2, 0.01)
	assertFloat(t, data, "battery_capacity", 80, 0.01)
	assertFloat(t, data, "battery_charge_current", 10, 0.01)
	assertFloat(t, data, "battery_discharge_current", 5, 0.01)
	assertFloat(t, data, "bus_voltage", 380, 0.01)
}

// ============================================================================
// 5. TestHPV — PV1/PV2 info parsing (with CommandAwareDriver)
// ============================================================================

func TestTechfine_HPV(t *testing.T) {
	// HPV and HPVB have the same wire layout, so parsing without the command
	// context must not silently label an HPVB response as PV1.
	raw := []byte("(120.5 08.0 00960\r")
	if _, err := (&TechfineInverterDriver{}).ParseData(raw); err == nil {
		t.Fatal("ambiguous PV response parsed without command context")
	}
}

func TestTechfine_HPVB_WithCommand(t *testing.T) {
	// When command is HPVB, response should return pv2_* fields
	raw := []byte("(080.0 05.0 00400\r")
	d := &TechfineInverterDriver{}
	// HPVB\r hex-encoded
	hpvbHex := asciiToHex("HPVB\r")
	data, err := d.ParseDataWithCommand(raw, hpvbHex)
	if err != nil {
		t.Fatalf("ParseDataWithCommand error: %v", err)
	}

	assertFloat(t, data, "pv2_voltage", 80.0, 0.01)
	assertFloat(t, data, "pv2_current", 5.0, 0.01)
	assertFloat(t, data, "pv2_power", 400, 0.01)
}

func TestTechfine_HPV_WithCommand(t *testing.T) {
	// When command is HPV (not HPVB), response should return pv1_* fields
	raw := []byte("(120.5 08.0 00960\r")
	d := &TechfineInverterDriver{}
	hpvHex := asciiToHex("HPV\r")
	data, err := d.ParseDataWithCommand(raw, hpvHex)
	if err != nil {
		t.Fatalf("ParseDataWithCommand error: %v", err)
	}

	assertFloat(t, data, "pv1_voltage", 120.5, 0.01)
	assertFloat(t, data, "pv1_current", 8.0, 0.01)
	assertFloat(t, data, "pv1_power", 960, 0.01)
}

// ============================================================================
// 6. TestHTEMP — Temperature info parsing
// ============================================================================

func TestTechfine_HTEMP(t *testing.T) {
	// (035 045 040 050 050 060 050 01 01 038 042
	raw := []byte("(035 045 040 050 050 060 050 01 01 038 042\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "pv_temp", 35, 0.01)
	assertFloat(t, data, "inverter_temp", 45, 0.01)
	assertFloat(t, data, "boost_temp", 40, 0.01)
	assertFloat(t, data, "transformer_temp", 50, 0.01)
	assertFloat(t, data, "max_temp", 50, 0.01)
	assertFloat(t, data, "fan1_speed", 60, 0.01)
	assertFloat(t, data, "fan2_speed", 50, 0.01)
	assertFloat(t, data, "fan1_status", 1, 0.01)
	assertFloat(t, data, "fan2_status", 1, 0.01)
	assertFloat(t, data, "pv2_temp", 38, 0.01)
	assertFloat(t, data, "dc_rectifier_temp", 42, 0.01)
}

// ============================================================================
// 7. TestHGEN — Energy generation info parsing
// ============================================================================

func TestTechfine_HGEN(t *testing.T) {
	// (202401 12:00 1.500 45.000 500.000 5000.000
	raw := []byte("(202401 12:00 1.500 45.000 500.000 5000.000\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "daily_energy", 1.500, 0.001)
	assertFloat(t, data, "monthly_energy", 45.000, 0.001)
	assertFloat(t, data, "yearly_energy", 500.000, 0.001)
	assertFloat(t, data, "total_energy", 5000.000, 0.001)
}

// ============================================================================
// 8. TestHBMS1 — BMS info parsing (MSB-first bit mapping)
// ============================================================================

func TestTechfine_HBMS1(t *testing.T) {
	// status1 = "10010000" → b7=1 (comm OK), b4=1 (charge allowed), b3=0 (no discharge)
	// temp = 29815 → 29815/100 - 273.15 = 25.00°C
	raw := []byte("(01 10010000 00000000 048.0 054.0 020 080 010.0 005.0 29815\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "bms_comm_ok", 1, 0.01)
	assertFloat(t, data, "bms_charge_allowed", 1, 0.01)
	assertFloat(t, data, "bms_discharge_allowed", 0, 0.01)
	assertFloat(t, data, "bms_low_alarm", 0, 0.01)
	assertFloat(t, data, "bms_low_fault", 0, 0.01)
	assertFloat(t, data, "bms_charge_overcurrent", 0, 0.01)
	assertFloat(t, data, "bms_discharge_overcurrent", 0, 0.01)
	assertFloat(t, data, "bms_temp_low", 0, 0.01)
	assertFloat(t, data, "bms_soc", 80, 0.01)
	assertFloat(t, data, "bms_charge_current", 10.0, 0.01)
	assertFloat(t, data, "bms_discharge_current", 5.0, 0.01)
	assertFloat(t, data, "bms_charge_voltage_limit", 54.0, 0.01)
	assertFloat(t, data, "bms_discharge_voltage_limit", 48.0, 0.01)
	assertFloat(t, data, "bms_charge_current_limit", 20, 0.01)
	assertFloat(t, data, "bms_temp", 25.0, 0.01)
}

func TestTechfine_HBMS1_AllAllowed(t *testing.T) {
	// status1 = "10011000" → b7=1 (comm), b4=1 (charge), b3=1 (discharge)
	raw := []byte("(01 10011000 00000000 048.0 054.0 020 100 015.0 008.0 29815\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "bms_comm_ok", 1, 0.01)
	assertFloat(t, data, "bms_charge_allowed", 1, 0.01)
	assertFloat(t, data, "bms_discharge_allowed", 1, 0.01)
	assertFloat(t, data, "bms_soc", 100, 0.01)
}

func TestTechfine_HBMS1_AllFlagsActive(t *testing.T) {
	// status1 = "11111111" → all flags active
	raw := []byte("(01 11111111 00000000 048.0 054.0 020 100 015.0 008.0 29815\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "bms_comm_ok", 1, 0.01)
	assertFloat(t, data, "bms_low_alarm", 1, 0.01)
	assertFloat(t, data, "bms_low_fault", 1, 0.01)
	assertFloat(t, data, "bms_charge_allowed", 1, 0.01)
	assertFloat(t, data, "bms_discharge_allowed", 1, 0.01)
	assertFloat(t, data, "bms_charge_overcurrent", 1, 0.01)
	assertFloat(t, data, "bms_discharge_overcurrent", 1, 0.01)
	assertFloat(t, data, "bms_temp_low", 1, 0.01)
}

// ============================================================================
// 9. TestHIMSG1 — Software version parsing (must not be mis-parsed as PV)
// ============================================================================

func TestTechfine_HIMSG1(t *testing.T) {
	// (0000.03 20230220 00 — version 0000.03, date 20230220
	raw := []byte("(0000.03 20230220 00\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	// Should NOT be parsed as PV
	if s := findSensor(data, "pv1_voltage"); s != nil {
		t.Errorf("HIMSG1 was mis-parsed as PV: pv1_voltage=%f", s.Value)
	}

	// version and date are now stored as float64 Value (not StringValue)
	// "0000.03" → 0.0003, "20230220" → 20230220
	assertFloat(t, data, "software_version", 0.0003, 0.0001)
	assertFloat(t, data, "software_date", 20230220, 0.01)
}

// ============================================================================
// 10. TestHEEP1 — EEPROM settings parsing (must not be mis-parsed as HTEMP)
// ============================================================================

func TestTechfine_HEEP1(t *testing.T) {
	// Minimal HEEP1 response with 15+ fields, first field is single digit 0-2
	// (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
	// fields[8] = "1" → 60Hz
	raw := []byte("(1 060 020 0 0 0 0 230 1 0 0 0 0 0 0 000 000 000 000 230.0 50.0 000.0 O\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	// Should NOT be parsed as HTEMP
	if s := findSensor(data, "pv_temp"); s != nil {
		t.Errorf("HEEP1 was mis-parsed as HTEMP: pv_temp=%f", s.Value)
	}

	// Verify basic EEPROM fields
	assertFloat(t, data, "eeprom_model_source", 1, 0.01)
	assertFloat(t, data, "eeprom_max_charge_current", 60, 0.01)
	assertFloat(t, data, "eeprom_output_voltage", 230, 0.01)
	// fields[8] = "1" → 60Hz
	assertFloat(t, data, "eeprom_output_frequency", 60, 0.01)
}

func TestTechfine_HEEP1_50Hz(t *testing.T) {
	// Same HEEP1 but fields[8] = "0" → 50Hz
	raw := []byte("(1 060 020 0 0 0 0 230 0 0 0 0 0 0 0 000 000 000 000 230.0 50.0 000.0 O\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "eeprom_output_frequency", 50, 0.01)
}

// ============================================================================
// 11. TestEdgeCases — Error handling
// ============================================================================

func TestTechfine_NoOpenParen(t *testing.T) {
	raw := []byte("00 P000000000000\r")
	_, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err == nil {
		t.Fatal("expected error for response without '('")
	}
}

func TestTechfine_EmptyResponse(t *testing.T) {
	raw := []byte("(\r")
	_, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err == nil {
		t.Fatal("expected error for empty response")
	}
}

func TestTechfine_TooFewFields(t *testing.T) {
	// 2 fields that don't match any parser (not HSTS, not HIMSG1, not enough for others)
	raw := []byte("(230.0 50\r")
	_, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err == nil {
		t.Fatal("expected error for response with too few fields")
	}
}

// ============================================================================
// 12. TestDriverMetadata — Interface compliance
// ============================================================================

func TestTechfine_DriverMetadata(t *testing.T) {
	d := &TechfineInverterDriver{}

	if d.DeviceType() != "techfine_inverter" {
		t.Errorf("DeviceType: got %q, want %q", d.DeviceType(), "techfine_inverter")
	}
	if d.DeviceName() != "泰琪丰 GB3024 逆变器" {
		t.Errorf("DeviceName: got %q", d.DeviceName())
	}
	if d.OEM() != "泰琪丰" {
		t.Errorf("OEM: got %q", d.OEM())
	}
	if d.Category() != "inverter" {
		t.Errorf("Category: got %q", d.Category())
	}
	if len(d.HardwareTypes()) != 1 || d.HardwareTypes()[0] != "uart" {
		t.Errorf("HardwareTypes: got %v", d.HardwareTypes())
	}
}

// TestTechfine_ControlActionsAreEnabledReads pins the eleven Techfine queries as
// ENABLED low-risk reads (2026-10-03).
//
// What this test is really guarding is NOT the Enabled literal — it is that only
// side-effect-free ASCII queries are reachable.  Until 2026-10-03 the assertions
// here required Enabled==false, which meant the test was locking a development
// rollout flag rather than a safety property: the field effect was eleven
// operations listed in the UI as unusable ("action is not enabled for
// rollout"), with no configuration entry point to open them.  The gate that
// actually matters is the one below it: every entry must be a read, must be
// low risk, must send exactly the frozen command, and no write may appear.
//
// The frozen command set is asserted byte-for-byte on purpose: these are the
// frames the vendor document defines, and a "harmless refactor" that changes
// one would silently address the wrong register.
func TestTechfine_ControlActionsAreEnabledReads(t *testing.T) {
	d := &TechfineInverterDriver{}
	var _ ControlActionProvider = d
	var _ ControlActionVerifier = d
	actions := d.ControlActions()
	if len(actions) != 11 {
		t.Fatalf("got %d actions, want 11", len(actions))
	}
	wantCommands := map[string]string{
		"read_status": "HSTS\r", "read_grid": "HGRID\r", "read_output": "HOP\r",
		"read_battery": "HBAT\r", "read_pv1": "HPV\r", "read_pv2": "HPVB\r",
		"read_temperature": "HTEMP\r", "read_energy": "HGEN\r", "read_bms": "HBMS1\r",
		"read_eeprom": "HEEP1\r", "read_version": "HIMSG1\r",
	}
	for _, action := range actions {
		command, ok := wantCommands[action.ID]
		if !ok {
			t.Fatalf("unexpected action %q", action.ID)
		}
		if !action.Enabled || action.Semantics != "read" || action.Risk != "low" || action.AvailabilityCode != "" || string(action.TXData) != command || action.ReadSize != 256 || action.RXTimeoutMS != 1000 {
			t.Fatalf("unsafe or malformed action %+v", action)
		}
		delete(wantCommands, action.ID)
	}
	if len(wantCommands) != 0 {
		t.Fatalf("missing actions for %v", wantCommands)
	}
}

func TestTechfine_VerifyControlActionUsesCommandContext(t *testing.T) {
	d := &TechfineInverterDriver{}
	pv2, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), []byte("(120.5 08.0 00960\r"))
	if err != nil {
		t.Fatalf("verify PV2: %v", err)
	}
	assertFloat(t, pv2, "pv2_voltage", 120.5, 0.01)
	if findSensor(pv2, "pv1_voltage") != nil {
		t.Fatalf("PV2 response was projected as PV1: %+v", pv2)
	}
	if _, err := d.VerifyControlAction("read_status", json.RawMessage(`{"unexpected":true}`), []byte("(00 P000000000000\r")); err == nil {
		t.Fatal("parameterized read was accepted")
	}
	if _, err := d.VerifyControlAction("set_voltage", json.RawMessage(`{}`), []byte("ACK\r")); err == nil {
		t.Fatal("unregistered write action was accepted")
	}
}

// ============================================================================
// 13. TestSensorDefinitions — All required sensors present
// ============================================================================

func TestTechfine_SensorDefinitions(t *testing.T) {
	d := &TechfineInverterDriver{}
	defs := d.GetSensorDefinitions()

	required := []string{
		// PV
		"pv1_voltage", "pv1_current", "pv1_power",
		"pv2_voltage", "pv2_current", "pv2_power",
		// Grid
		"grid_voltage", "grid_frequency",
		// Output
		"output_voltage", "output_frequency", "output_apparent_power",
		"output_active_power", "output_load_percent",
		// Battery
		"battery_voltage", "battery_capacity", "battery_charge_current",
		"battery_discharge_current", "bus_voltage",
		// Temperature
		"pv_temp", "inverter_temp", "boost_temp", "transformer_temp",
		"max_temp", "pv2_temp", "dc_rectifier_temp",
		// Fan
		"fan1_speed", "fan2_speed", "fan1_status", "fan2_status",
		// Energy
		"daily_energy", "monthly_energy", "yearly_energy", "total_energy",
		// Status
		"fault_code", "work_mode",
		// Alarms
		"alarm_pv_to_load", "alarm_output", "alarm_battery_low",
		"alarm_battery_missing", "alarm_overload", "alarm_overtemp",
		"alarm_eeprom_data", "alarm_eeprom_rw", "alarm_pv_low",
		"alarm_input_overvoltage", "alarm_battery_overvoltage", "alarm_fan_error",
		// BMS
		"bms_comm_ok", "bms_charge_allowed", "bms_discharge_allowed",
		"bms_low_alarm", "bms_low_fault", "bms_charge_overcurrent",
		"bms_discharge_overcurrent", "bms_temp_low",
		"bms_soc", "bms_charge_current", "bms_discharge_current",
		"bms_charge_voltage_limit", "bms_discharge_voltage_limit",
		"bms_charge_current_limit", "bms_temp",
		// Version
		"software_version", "software_date",
		// Protocol
		"protocol_type",
	}

	defMap := make(map[string]bool)
	for _, d := range defs {
		defMap[d.Name] = true
	}

	for _, name := range required {
		if !defMap[name] {
			t.Errorf("missing sensor definition: %s", name)
		}
	}
}

// ============================================================================
// 14. TestCommandTemplates — verify only non-schedulable read compatibility
// metadata remains public.  Settings require an unverified CRC and must not
// reach ConfigManifest or an Action Catalog.
// ============================================================================

func TestTechfine_CommandTemplates(t *testing.T) {
	// 2026-10-03: 轮询模板恢复。C6 删除它们时把"轮询"一并删掉了，导致该驱动
	// 在架构上失去周期性采集能力 —— 现场表现为 UART1 从未发送任何字节
	// （用户观察到 RS232 转接板 Tx/Rx 灯从不亮），因为 manifest 为该边设备
	// 编码了 0 条命令，调度器于是从不投递 CMD_SAMPLE。
	//
	// 断言三件事：
	//   1. 模板非空（否则命令永远不会被调度）
	//   2. 全部 Schedulable（遵守 P2：第三态废止）
	//   3. ReadLength 全为 0（ASCII 行协议必须走行空闲定帧；非 0 会让每条
	//      响应因长度不符被判 error 0x03）
	templates := (&TechfineInverterDriver{}).GetCommandTemplates()
	if len(templates) == 0 {
		t.Fatal("GetCommandTemplates returned no templates — the inverter can never be polled")
	}
	for _, tmpl := range templates {
		if !tmpl.Schedulable {
			t.Errorf("template %q has Schedulable=false — the third state is abolished (演进方案 P2)", tmpl.ID)
		}
		if tmpl.ReadLength != 0 {
			t.Errorf("template %q has ReadLength=%d; ASCII line protocol requires 0 (line-idle framing), "+
				"otherwise every response is rejected as a short read (error 0x03)", tmpl.ID, tmpl.ReadLength)
		}
		if tmpl.WriteData == "" {
			t.Errorf("template %q has empty WriteData — no frame would be transmitted", tmpl.ID)
		}
	}
}

// ============================================================================
// 14b. TestQPRTL — Protocol type query (single field)
// ============================================================================

func TestTechfine_QPRTL(t *testing.T) {
	// (MMMMMMMM) — single field response
	raw := []byte("(MMMMMMMM\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "protocol_type", 1, 0.01)
}

// ============================================================================
// 14c. TestHGRID_HOP_Dispatch — HGRID trailing data must not trigger HOP
// ============================================================================

func TestTechfine_HGRID_HOP_DispatchHardening(t *testing.T) {
	// HGRID with 8+ trailing fields where fields[7] contains a decimal.
	// fields[2] and fields[3] are NOT 4+ digit numerics (HGRID loss voltage values).
	// This should parse as HGRID, not HOP.
	raw := []byte("(230.0 50.0 184 253 47 53 99 1.5\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	// Should be parsed as HGRID (grid_voltage), not HOP (output_voltage)
	if s := findSensor(data, "output_voltage"); s != nil {
		t.Errorf("HGRID trailing data was mis-parsed as HOP: output_voltage=%f", s.Value)
	}
	assertFloat(t, data, "grid_voltage", 230.0, 0.01)
	assertFloat(t, data, "grid_frequency", 50.0, 0.01)
}

// ============================================================================
// 16. TestCommandAwareDriver — Verify interface compliance
// ============================================================================

func TestTechfine_CommandAwareDriver(t *testing.T) {
	d := &TechfineInverterDriver{}

	// Verify it implements CommandAwareDriver
	var _ CommandAwareDriver = d

	// Empty command context must fail closed for the ambiguous HPV/HPVB shape.
	raw := []byte("(120.5 08.0 00960\r")
	if _, err := d.ParseDataWithCommand(raw, ""); err == nil {
		t.Fatal("empty command context accepted an ambiguous PV response")
	}
	if _, err := d.ParseDataWithCommand(raw, "not-hex"); err == nil {
		t.Fatal("invalid command context accepted an ambiguous PV response")
	}
}

// ============================================================================
// 截断帧不得被采信（2026-10-03 主控自查发现的 P0 回归）
//
// 背景：节点侧对 ChannelCmdV2 改为「行空闲即帧边界」后，短于 read_size 的响应
// 不再在节点本地被丢成 error 0x03，而是原样投递到服务端，由本驱动的 verifier
// 做权威校验（这是该固件改动的安全前提）。
//
// 但当时 verifier **没有校验帧结尾**：ParseData 用 TrimRight + strings.Fields，
// 一个被截断的 ASCII 行照样能解析出数值。实测 "(220.5 08.0 0" 会产出
// pv2_power=0（真实值 960）—— 于是"读取失败"退化成"静默的假数据"，
// 比失败更糟：用户看到的是一个看起来正常但错误的功率读数。
//
// 本用例把"截断即拒绝"钉死：GB3024 每条响应都以 CR 结尾，缺 CR 即畸形。
// 它凭什么会失败：把 VerifyControlAction 里的 CR 结尾校验删掉，
// 下面 18 个前缀里会有 5 个被接受，本用例立刻红。
// ============================================================================
func TestTechfine_VerifyControlActionRejectsTruncatedFrames(t *testing.T) {
	d := &TechfineInverterDriver{}
	full := []byte("(220.5 08.0 00960\r")

	// 前置：完整帧必须通过，否则下面的"拒绝"可能只是因为解析器根本不通。
	if _, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), full); err != nil {
		t.Fatalf("完整帧应通过，却失败: %v", err)
	}

	// 逐字节截断：任何缺少 CR 结尾的前缀都不得被采信。
	for n := 0; n < len(full); n++ {
		if _, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), full[:n]); err == nil {
			t.Errorf("截断帧被采信了（前 %d 字节: %q）：缺失 CR 结尾说明线路截断或设备异常，不得产出数值",
				n, string(full[:n]))
		}
	}

	// 反向：完整帧（含 CR）仍然必须通过 —— 防止"一刀切拒绝所有帧"式的假修复。
	if _, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), full); err != nil {
		t.Fatalf("完整帧被误拒: %v", err)
	}

	// 空帧同样不得采信。
	if _, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), nil); err == nil {
		t.Error("空响应被采信")
	}
}

// TestTechfine_VerifyControlActionRejectsTruncatedNumericField 用**具体数值后果**
// 说明为什么必须拒绝：截断会把一个非零功率读成 0。
// 只断言"返回 error"不足以表达危害，这里直接钉住"不得产出 0 这个错误值"。
func TestTechfine_VerifyControlActionRejectsTruncatedNumericField(t *testing.T) {
	d := &TechfineInverterDriver{}
	// 完整帧的第三个字段 00960 会被解析为 pv2_power。
	result, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), []byte("(220.5 08.0 00960\r"))
	if err != nil {
		t.Fatalf("完整帧解析失败: %v", err)
	}
	var power float64
	found := false
	for _, sd := range result {
		if sd.Name == "pv2_power" {
			power = sd.Value
			found = true
		}
	}
	if !found {
		t.Fatal("完整帧未产出 pv2_power，夹具失效")
	}
	if power == 0 {
		t.Fatalf("完整帧的 pv2_power 解析为 0，夹具与实际解析不符")
	}

	// 截断到最后一个字符之前：不得产出任何值（尤其不得产出 0）。
	if got, err := d.VerifyControlAction("read_pv2", json.RawMessage(`{}`), []byte("(220.5 08.0 0")); err == nil {
		for _, sd := range got {
			if sd.Name == "pv2_power" {
				t.Fatalf("截断帧产出了 pv2_power=%v（真实值 %v）—— 静默假数据比失败更危险", sd.Value, power)
			}
		}
		t.Error("截断帧未报错")
	}
}

// ============================================================================
// 形状绑定：CR 结尾不足以防错配/截断帧（2026-10-03 固件审查发现的 P0）
//
// 背景：节点侧放行 V2 短读后，verifier 成为唯一防线。但**仅校验 CR 结尾不够**：
// ParseData 是按字段数/格式嗅探分支的，不绑定所请求的动作。审查者实测出 4 个洞，
// 我自查时又发现第 5 个。下面逐个钉死（全部为实测复现过的输入）：
//
//  1. read_battery 喂 HGRID 形状 → 曾返回 grid_voltage/grid_frequency
//  2. read_output  喂市电形状   → 曾返回 grid_voltage/grid_frequency
//  3. read_status  喂截短 HSTS  → 曾返回 12 个 alarm_* 全为 0（**fail-open**：
//     设备可能正在报警，界面显示"无告警"）
//  4. read_temperature 喂 HBAT 形状 → 曾返回 battery_* 字段
//  5. read_pv2 喂截断功率字段 → 曾返回 pv2_power=0（真实 960W）
//
// 修法：verifier 做两道校验 —— 字段必须同族（拦错配）+ 字段数与形状必须完整（拦截断）；
// 并在 parseHSTS / parsePV 里把"缺字段默认成 0"的 fail-open 改为显式报错。
//
// 它凭什么会失败：删掉 expectActionSensors 的调用，或用回 parseFloat 的 0 兜底，
// 下面 5 条会全部变红。
// ============================================================================
func TestTechfine_VerifyControlActionRejectsWrongShapeAndTruncatedFrames(t *testing.T) {
	d := &TechfineInverterDriver{}
	// 每一条都是"CR 结尾但形状不对/被截断"的帧 —— 旧的 CR 校验放行它们。
	bad := []struct {
		action string
		raw    string
		reason string
	}{
		{"read_battery", "(52.1 08.0 00960 001 00000 002 00000\r", "市电形状的帧被当成电池数据"},
		{"read_output", "(230.1 50.0 01150 01000 025 003\r", "市电形状的帧被当成输出数据"},
		{"read_status", "(00 P0000\r", "截短的状态帧被读成\"无告警\"（fail-open）"},
		{"read_temperature", "(25 30 40 45 50 55 60 70\r", "电池形状的帧被当成温度数据"},
		{"read_pv2", "(220.5 08.0 0\r", "截断的功率字段被读成 0W"},
	}
	for _, tc := range bad {
		got, err := d.VerifyControlAction(tc.action, json.RawMessage(`{}`), []byte(tc.raw))
		if err == nil {
			names := make([]string, 0, len(got))
			for _, sd := range got {
				names = append(names, sd.Name)
			}
			t.Errorf("%s: %s —— 却返回了 %v（静默假数据比失败更危险）", tc.action, tc.reason, names)
		}
	}
}

// TestTechfine_VerifyControlActionAcceptsWellFormedResponses 是上一条的**反向对照**：
// 形状绑定不得把合法响应一起拒掉（防"一刀切拒绝"式假修复）。
// 只说"能拒绝坏帧"无法排除"把好帧也拒了"—— 两条合起来才钉住判据的边界。
func TestTechfine_VerifyControlActionAcceptsWellFormedResponses(t *testing.T) {
	d := &TechfineInverterDriver{}
	good := []struct {
		action string
		raw    string
	}{
		{"read_status", "(00 P000000000000\r"},
		{"read_battery", "(12 053.2 080 010 00005 380 00000\r"},
		{"read_pv1", "(120.5 08.0 00960\r"},
		{"read_pv2", "(080.0 05.0 00400\r"},
	}
	for _, tc := range good {
		data, err := d.VerifyControlAction(tc.action, json.RawMessage(`{}`), []byte(tc.raw))
		if err != nil {
			t.Errorf("%s: 合法响应被误拒: %v", tc.action, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s: 合法响应未产出任何字段", tc.action)
		}
	}
}

// TestTechfine_HSTS_TruncatedStatusIsNotAllClear 直接钉住 fail-open 的**后果**：
// 截短的状态帧绝不能产出"12 个告警全 0"这种"一切正常"的结论。
func TestTechfine_HSTS_TruncatedStatusIsNotAllClear(t *testing.T) {
	// "(00 P0000" 是告警串被截到 5 字符的 HSTS；完整帧是 13 字符（1 模式 + 12 告警位）。
	if _, err := (&TechfineInverterDriver{}).ParseData([]byte("(00 P0000\r")); err == nil {
		t.Fatal("截短的状态帧被解析成功：缺失的告警位被默认成 0，会把\"正在报警\"显示成\"无告警\"")
	}
	// 完整帧必须仍然可解析（否则上面那条可能只是因为解析器整体坏了）。
	if _, err := (&TechfineInverterDriver{}).ParseData([]byte("(00 P000000000000\r")); err != nil {
		t.Fatalf("完整状态帧被误拒: %v", err)
	}
}
