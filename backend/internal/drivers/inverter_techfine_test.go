package drivers

import (
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
	// (120.5 08.0 00960 — 120.5V, 8.0A, 960W
	raw := []byte("(120.5 08.0 00960\r")
	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData error: %v", err)
	}

	assertFloat(t, data, "pv1_voltage", 120.5, 0.01)
	assertFloat(t, data, "pv1_current", 8.0, 0.01)
	assertFloat(t, data, "pv1_power", 960, 0.01)
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
	if d.DeviceName() != "Techfine GB3024 逆变器" {
		t.Errorf("DeviceName: got %q", d.DeviceName())
	}
	if d.OEM() != "Techfine" {
		t.Errorf("OEM: got %q", d.OEM())
	}
	if d.Category() != "inverter" {
		t.Errorf("Category: got %q", d.Category())
	}
	if len(d.HardwareTypes()) != 1 || d.HardwareTypes()[0] != "GB3024" {
		t.Errorf("HardwareTypes: got %v", d.HardwareTypes())
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
// 14. TestCommandTemplates — Verify templates are generated with CRC16
// ============================================================================

func TestTechfine_CommandTemplates(t *testing.T) {
	d := &TechfineInverterDriver{}
	templates := d.GetCommandTemplates()

	if len(templates) < 25 {
		t.Errorf("expected >=25 command templates, got %d", len(templates))
	}

	// Verify query templates
	queryIDs := []string{
		"query_status", "query_grid", "query_output", "query_battery",
		"query_pv1", "query_pv2", "query_temperature", "query_energy",
		"query_bms", "query_eeprom", "query_version", "query_protocol",
		"query_pe", "query_pd",
	}
	tmplMap := make(map[string]CommandTemplate)
	for _, tmpl := range templates {
		tmplMap[tmpl.ID] = tmpl
	}

	for _, id := range queryIDs {
		if tmpl, ok := tmplMap[id]; !ok {
			t.Errorf("missing command template: %s", id)
		} else {
			if !tmpl.Schedulable {
				t.Errorf("query template %s should be schedulable", id)
			}
			if tmpl.WriteData == "" {
				t.Errorf("query template %s has empty WriteData", id)
			}
		}
	}

	// Verify control template
	if tmpl, ok := tmplMap["turn_on"]; !ok {
		t.Errorf("missing command template: turn_on")
	} else {
		if tmpl.Schedulable {
			t.Errorf("control template turn_on should not be schedulable")
		}
	}

	// Verify parameterized setting templates exist
	settingIDs := []string{
		"set_voltage_220", "set_voltage_230", "set_voltage_240",
		"set_frequency_50", "set_frequency_60",
		"set_battery_type_agm", "set_battery_type_fld", "set_battery_type_user",
		"set_grid_range_apl", "set_grid_range_ups",
		"set_work_mode_uti", "set_work_mode_sub", "set_work_mode_sbu",
		"set_bms_off", "set_bms_on",
		"set_lock_voltage", "set_charge_voltage", "set_float_voltage",
		"set_total_charge_current", "set_mains_charge_current", "system_reset",
	}
	for _, id := range settingIDs {
		if _, ok := tmplMap[id]; !ok {
			t.Errorf("missing setting template: %s", id)
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
// 15. TestCRC16 — Verify CRC16-Modbus computation
// ============================================================================

func TestTechfine_CRC16(t *testing.T) {
	// CRC16-Modbus of "V230" should be deterministic
	crc := CRC16Modbus([]byte("V230"))
	if crc == 0 {
		t.Error("CRC16 of 'V230' should not be 0")
	}

	// Same input → same output
	crc2 := CRC16Modbus([]byte("V230"))
	if crc != crc2 {
		t.Errorf("CRC16 not deterministic: %d vs %d", crc, crc2)
	}

	// Different input → different output
	crc3 := CRC16Modbus([]byte("V220"))
	if crc == crc3 {
		t.Error("CRC16 of 'V230' and 'V220' should differ")
	}
}

// ============================================================================
// 16. TestCommandAwareDriver — Verify interface compliance
// ============================================================================

func TestTechfine_CommandAwareDriver(t *testing.T) {
	d := &TechfineInverterDriver{}

	// Verify it implements CommandAwareDriver
	var _ CommandAwareDriver = d

	// Test with empty/invalid commandWriteData falls back to ParseData
	raw := []byte("(120.5 08.0 00960\r")
	data, err := d.ParseDataWithCommand(raw, "")
	if err != nil {
		t.Fatalf("ParseDataWithCommand with empty cmd should fall back: %v", err)
	}
	assertFloat(t, data, "pv1_voltage", 120.5, 0.01)
}
