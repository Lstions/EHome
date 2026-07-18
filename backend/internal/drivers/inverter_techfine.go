package drivers

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// ============================================================================
// TechfineInverterDriver — Techfine GB3024 逆变器 (ASCII 协议)
// ============================================================================
// Protocol: RS232C, 2400bps, 8N1, ASCII text
// Commands end with \r, responses start with '(' and end with \r
// Fields are space-separated. Trailing "OOO..." data is ignored.
// ============================================================================

// TechfineInverterDriver parses Techfine GB3024 inverter ASCII protocol responses.
type TechfineInverterDriver struct{}

func (d *TechfineInverterDriver) DeviceType() string      { return "techfine_inverter" }
func (d *TechfineInverterDriver) DeviceName() string      { return "Techfine GB3024 逆变器" }
func (d *TechfineInverterDriver) OEM() string             { return "Techfine" }
func (d *TechfineInverterDriver) Category() string        { return "inverter" }
func (d *TechfineInverterDriver) HardwareTypes() []string { return []string{"uart"} }

// GetSensorDefinitions returns all sensor definitions for HA Discovery.
func (d *TechfineInverterDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		// PV
		{Name: "pv1_voltage", Unit: "V"},
		{Name: "pv1_current", Unit: "A"},
		{Name: "pv1_power", Unit: "W"},
		{Name: "pv2_voltage", Unit: "V"},
		{Name: "pv2_current", Unit: "A"},
		{Name: "pv2_power", Unit: "W"},
		// Grid
		{Name: "grid_voltage", Unit: "V"},
		{Name: "grid_frequency", Unit: "Hz"},
		// Output
		{Name: "output_voltage", Unit: "V"},
		{Name: "output_frequency", Unit: "Hz"},
		{Name: "output_apparent_power", Unit: "VA"},
		{Name: "output_active_power", Unit: "W"},
		{Name: "output_load_percent", Unit: "%"},
		// Battery
		{Name: "battery_voltage", Unit: "V"},
		{Name: "battery_capacity", Unit: "%"},
		{Name: "battery_charge_current", Unit: "A"},
		{Name: "battery_discharge_current", Unit: "A"},
		{Name: "bus_voltage", Unit: "V"},
		// Temperature
		{Name: "pv_temp", Unit: "°C"},
		{Name: "inverter_temp", Unit: "°C"},
		{Name: "boost_temp", Unit: "°C"},
		{Name: "transformer_temp", Unit: "°C"},
		{Name: "max_temp", Unit: "°C"},
		{Name: "pv2_temp", Unit: "°C"},
		{Name: "dc_rectifier_temp", Unit: "°C"},
		// Fan
		{Name: "fan1_speed", Unit: "%"},
		{Name: "fan2_speed", Unit: "%"},
		{Name: "fan1_status", Unit: ""},
		{Name: "fan2_status", Unit: ""},
		// Energy
		{Name: "daily_energy", Unit: "kWh"},
		{Name: "monthly_energy", Unit: "kWh"},
		{Name: "yearly_energy", Unit: "kWh"},
		{Name: "total_energy", Unit: "kWh"},
		// Status
		{Name: "fault_code", Unit: ""},
		{Name: "work_mode", Unit: ""},
		// Alarms
		{Name: "alarm_pv_to_load", Unit: ""},
		{Name: "alarm_output", Unit: ""},
		{Name: "alarm_battery_low", Unit: ""},
		{Name: "alarm_battery_missing", Unit: ""},
		{Name: "alarm_overload", Unit: ""},
		{Name: "alarm_overtemp", Unit: ""},
		{Name: "alarm_eeprom_data", Unit: ""},
		{Name: "alarm_eeprom_rw", Unit: ""},
		{Name: "alarm_pv_low", Unit: ""},
		{Name: "alarm_input_overvoltage", Unit: ""},
		{Name: "alarm_battery_overvoltage", Unit: ""},
		{Name: "alarm_fan_error", Unit: ""},
		// BMS
		{Name: "bms_comm_ok", Unit: ""},
		{Name: "bms_charge_allowed", Unit: ""},
		{Name: "bms_discharge_allowed", Unit: ""},
		{Name: "bms_low_alarm", Unit: ""},
		{Name: "bms_low_fault", Unit: ""},
		{Name: "bms_charge_overcurrent", Unit: ""},
		{Name: "bms_discharge_overcurrent", Unit: ""},
		{Name: "bms_temp_low", Unit: ""},
		{Name: "bms_soc", Unit: "%"},
		{Name: "bms_charge_current", Unit: "A"},
		{Name: "bms_discharge_current", Unit: "A"},
		{Name: "bms_charge_voltage_limit", Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Unit: "V"},
		{Name: "bms_charge_current_limit", Unit: "A"},
		{Name: "bms_temp", Unit: "°C"},
		// Version / EEPROM
		{Name: "software_version", Unit: ""},
		{Name: "software_date", Unit: ""},
		// Protocol
		{Name: "protocol_type", Unit: ""},
	}
}

// ============================================================================
// ParseData — main entry point
// ============================================================================
// ParseData parses a raw ASCII response from the Techfine GB3024 inverter.
// The response format: '(' + space-separated fields + '\r'
// Trailing "OOO..." padding fields are ignored.
//
// Command type is inferred from field count and format patterns:
//   - 2 fields, 2nd starts with work-mode letter → HSTS
//   - 3 fields, 2nd is 8-digit date (all digits)  → HIMSG1 (version)
//   - 3 fields (numeric)                          → HPV/HPVB (PV data)
//   - 6+ fields, 2nd contains ':'                 → HGEN
//   - 15+ fields, 1st is single digit 0-2         → HEEP1 (EEPROM)
//   - 6+ fields, 1st has decimal, 8th has decimal → HOP
//   - 6+ fields, 1st has decimal                  → HGRID
//   - 7+ fields, 1st is integer                   → HBAT
//   - 10 fields, 2nd is 8-bit binary              → HBMS1
//   - 11+ fields                                  → HTEMP

func (d *TechfineInverterDriver) ParseData(raw []byte) ([]SensorData, error) {
	s := string(raw)

	// Response must start with '('
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("techfine: response must start with '(', got: %q", s)
	}

	// Strip leading '(' and trailing whitespace/CR
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimRight(s, "\r\n\x00 ")

	// Split by spaces
	fields := strings.Fields(s)

	if len(fields) == 0 {
		return nil, fmt.Errorf("techfine: empty response")
	}

	// --- HSTS: (NN MABCDEFGHIJKL...)
	// Second field starts with a work-mode letter: P/S/L/B/F/D/X
	if len(fields) >= 2 && len(fields[1]) >= 1 {
		c := fields[1][0]
		if c == 'P' || c == 'S' || c == 'L' || c == 'B' || c == 'F' || c == 'D' || c == 'X' {
			return d.parseHSTS(fields)
		}
	}

	// --- HBMS1: (AA b7b6b5b4b3b2b1b0 c7c6c5c4c3c2c1c0 ...)
	// Second and third fields are 8-character binary strings
	if len(fields) >= 10 && len(fields[1]) == 8 && len(fields[2]) == 8 &&
		isAllBinary(fields[1]) && isAllBinary(fields[2]) {
		return d.parseHBMS1(fields)
	}

	// --- HGEN: (AAAAAA BB:BB CC.CCC DDDD.D EEEE.E FFFFFFFFF.F ...)
	// Second field contains ':' (time format HH:MM)
	if len(fields) >= 6 && strings.Contains(fields[1], ":") {
		return d.parseHGEN(fields)
	}

	// --- HEEP1: (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
	// 20+ fields, first field is single digit (0-2)
	if len(fields) >= 15 && len(fields[0]) == 1 && fields[0] >= "0" && fields[0] <= "2" {
		return d.parseHEEP1(fields)
	}

	// --- HTEMP: (AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK ...)
	// 11 meaningful fields.
	// NOTE: fan status fields [7] and [8] are space-separated single chars; needs real device verification.
	if len(fields) >= 11 {
		return d.parseHTEMP(fields)
	}

	// --- HGRID / HOP: first field has decimal point (NNN.N)
	// HGRID: (NNN.N MM.M AAA BBB CC DD ...)       6 meaningful fields
	// HOP:   (NNN.N MM.M AAAAA BBBBB CCC DDD EEEEE FFF.F ...) 8 fields, 8th has decimal
	// Hardening: HOP fields[2] and fields[3] are 4+ digit numeric power values;
	// HGRID trailing data won't match this pattern.
	if len(fields) >= 6 && strings.Contains(fields[0], ".") {
		if len(fields) >= 8 && strings.Contains(fields[7], ".") &&
			isAllDigitsOrEmpty(fields[2]) && len(fields[2]) >= 4 &&
			isAllDigitsOrEmpty(fields[3]) && len(fields[3]) >= 4 {
			return d.parseHOP(fields)
		}
		return d.parseHGRID(fields)
	}

	// --- HBAT: (AA BBB.B CCC DDD EEEEE FFF GHIJK...) 7 meaningful fields, first is integer
	if len(fields) >= 7 {
		return d.parseHBAT(fields)
	}

	// --- HIMSG1: (NNNN.NN AAAABBCC DD — version + date, 2nd field is 8-digit date
	// Must be checked BEFORE PV (both have 3 fields, first has decimal point)
	if len(fields) >= 3 && len(fields[1]) == 8 && isAllDigits(fields[1]) {
		return d.parseHIMSG1(fields)
	}

	// --- PV: (AAA.A BB.B CCCCC ...) 3 meaningful fields
	// HPV and HPVB share the same response shape. Without the originating
	// command there is no safe way to select pv1_* or pv2_*.
	if len(fields) >= 3 {
		return nil, fmt.Errorf("techfine: ambiguous HPV/HPVB response requires command context")
	}

	// --- QPRTL: (MMMMMMMM) — protocol type, single field
	if len(fields) == 1 {
		return []SensorData{
			{Name: "protocol_type", Value: 1, Unit: ""},
		}, nil
	}

	return nil, fmt.Errorf("techfine: unrecognized response format, %d fields: %v", len(fields), fields)
}

// ============================================================================
// ParseDataWithCommand — command-aware parsing for HPV/HPVB disambiguation
// ============================================================================
// commandWriteData is the hex-encoded WriteData of the ConfigTemplate
// (e.g. "4850560d" for "HPV\r", "485056420d" for "HPVB\r").
func (d *TechfineInverterDriver) ParseDataWithCommand(raw []byte, commandWriteData string) ([]SensorData, error) {
	// Decode hex to ASCII
	decoded, err := hex.DecodeString(commandWriteData)
	if err != nil {
		return nil, fmt.Errorf("techfine: invalid command write data: %w", err)
	}
	cmdStr := strings.ToUpper(strings.TrimSpace(string(decoded)))

	if cmdStr == "HPV" || cmdStr == "HPVB" {
		s := string(raw)
		if !strings.HasPrefix(s, "(") {
			return nil, fmt.Errorf("techfine: response must start with '(', got: %q", s)
		}
		s = strings.TrimPrefix(s, "(")
		s = strings.TrimRight(s, "\r\n\x00 ")
		fields := strings.Fields(s)
		if len(fields) < 3 {
			return nil, fmt.Errorf("techfine %s: need >=3 fields, got %d", cmdStr, len(fields))
		}
		prefix := "pv1"
		if cmdStr == "HPVB" {
			prefix = "pv2"
		}
		return d.parsePV(fields, prefix)
	}

	// Other response formats are structurally distinguishable. If the response
	// has the ambiguous PV shape, ParseData still fails closed.
	return d.ParseData(raw)
}

// ============================================================================
// parseHSTS — Status query
// ============================================================================
// Format: (NN MABCDEFGHIJKL...)
//   NN           – fault code (integer, decimal)
//   M            – work mode: P=Power-on, S=Standby, L=Line, B=Battery, F=Fault, D=Shutdown, X=Test
//   A-L          – 12 alarm flags (12 chars), '0'=inactive, other=active
//   A: PV馈能到负载  B: 有输出  C: 电池低电报警  D: 电池未接
//   E: 输出过载    F: 过温    G: EEPROM数据异常  H: EEPROM读写异常
//   I: PV功率过低  J: 输入电压过高  K: 电池电压过高  L: 风扇异常

// workModeMap maps work mode letters to numeric codes for LastData storage.
var workModeMap = map[byte]float64{'P': 0, 'S': 1, 'L': 2, 'B': 3, 'F': 4, 'D': 5, 'X': 6}

func (d *TechfineInverterDriver) parseHSTS(fields []string) ([]SensorData, error) {
	if len(fields) < 2 {
		return nil, fmt.Errorf("techfine HSTS: need >=2 fields, got %d", len(fields))
	}

	faultCode, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil, fmt.Errorf("techfine HSTS: invalid fault code %q", fields[0])
	}

	statusStr := fields[1]
	if len(statusStr) < 1 {
		return nil, fmt.Errorf("techfine HSTS: empty status string")
	}

	workModeByte := statusStr[0]
	workModeVal, ok := workModeMap[workModeByte]
	if !ok {
		workModeVal = -1 // unknown mode
	}

	// Alarm flags are positions 1..12 in the status string
	alarmFlags := ""
	if len(statusStr) >= 13 {
		alarmFlags = statusStr[1:13]
	} else if len(statusStr) > 1 {
		alarmFlags = statusStr[1:]
	}

	// Map alarm flag positions to sensor names
	alarmMap := []string{
		"alarm_pv_to_load",          // A (pos 0)
		"alarm_output",              // B (pos 1)
		"alarm_battery_low",         // C (pos 2)
		"alarm_battery_missing",     // D (pos 3)
		"alarm_overload",            // E (pos 4)
		"alarm_overtemp",            // F (pos 5)
		"alarm_eeprom_data",         // G (pos 6)
		"alarm_eeprom_rw",           // H (pos 7)
		"alarm_pv_low",              // I (pos 8)
		"alarm_input_overvoltage",   // J (pos 9)
		"alarm_battery_overvoltage", // K (pos 10)
		"alarm_fan_error",           // L (pos 11)
	}

	result := []SensorData{
		{Name: "fault_code", Value: float64(faultCode), Unit: ""},
		{Name: "work_mode", Value: workModeVal, Unit: ""},
	}

	for i, name := range alarmMap {
		val := 0.0
		if i < len(alarmFlags) && alarmFlags[i] != '0' {
			val = 1.0
		}
		result = append(result, SensorData{Name: name, Value: val, Unit: ""})
	}

	return result, nil
}

// ============================================================================
// parseHGRID — Grid info
// ============================================================================
// Format: (NNN.N MM.M AAA BBB CC DD ...)
//   NNN.N – grid voltage (V)
//   MM.M  – grid frequency (Hz)
//   AAA   – loss voltage high (V)
//   BBB   – loss voltage low (V)
//   CC    – frequency high (Hz)
//   DD    – frequency low (Hz)

func (d *TechfineInverterDriver) parseHGRID(fields []string) ([]SensorData, error) {
	if len(fields) < 6 {
		return nil, fmt.Errorf("techfine HGRID: need >=6 fields, got %d", len(fields))
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid frequency %q", fields[1])
	}

	return []SensorData{
		{Name: "grid_voltage", Value: voltage, Unit: "V"},
		{Name: "grid_frequency", Value: freq, Unit: "Hz"},
	}, nil
}

// ============================================================================
// parseHOP — Output info
// ============================================================================
// Format: (NNN.N MM.M AAAAA BBBBB CCC DDD EEEEE FFF.F ...)
//   NNN.N  – output voltage (V)
//   MM.M   – output frequency (Hz)
//   AAAAA  – apparent power (VA)
//   BBBBB  – active power (W)
//   CCC    – load percentage (%)
//   DDD    – DC component
//   EEEEE  – internal data
//   FFF.F  – inductor current (A)

func (d *TechfineInverterDriver) parseHOP(fields []string) ([]SensorData, error) {
	if len(fields) < 8 {
		return nil, fmt.Errorf("techfine HOP: need >=8 fields, got %d", len(fields))
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid frequency %q", fields[1])
	}

	apparentPower := parseFloat(fields[2])
	activePower := parseFloat(fields[3])
	loadPercent := parseFloat(fields[4])

	return []SensorData{
		{Name: "output_voltage", Value: voltage, Unit: "V"},
		{Name: "output_frequency", Value: freq, Unit: "Hz"},
		{Name: "output_apparent_power", Value: apparentPower, Unit: "VA"},
		{Name: "output_active_power", Value: activePower, Unit: "W"},
		{Name: "output_load_percent", Value: loadPercent, Unit: "%"},
	}, nil
}

// ============================================================================
// parseHBAT — Battery info
// ============================================================================
// Format: (AA BBB.B CCC DDD EEEEE FFF GHIJK...)
//   AA     – battery cell count
//   BBB.B  – battery voltage (V)
//   CCC    – battery capacity (%)
//   DDD    – charge current (A)
//   EEEEE  – discharge current (A)
//   FFF    – BUS voltage (V)
//   GHIJK  – charge switches (5 packed flags)

func (d *TechfineInverterDriver) parseHBAT(fields []string) ([]SensorData, error) {
	if len(fields) < 7 {
		return nil, fmt.Errorf("techfine HBAT: need >=7 fields, got %d", len(fields))
	}

	voltage, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HBAT: invalid voltage %q", fields[1])
	}

	capacity := parseFloat(fields[2])
	chargeCurrent := parseFloat(fields[3])
	dischargeCurrent := parseFloat(fields[4])
	busVoltage := parseFloat(fields[5])

	return []SensorData{
		{Name: "battery_voltage", Value: voltage, Unit: "V"},
		{Name: "battery_capacity", Value: capacity, Unit: "%"},
		{Name: "battery_charge_current", Value: chargeCurrent, Unit: "A"},
		{Name: "battery_discharge_current", Value: dischargeCurrent, Unit: "A"},
		{Name: "bus_voltage", Value: busVoltage, Unit: "V"},
	}, nil
}

// ============================================================================
// parsePV — PV info (HPV / HPVB)
// ============================================================================
// Format: (AAA.A BB.B CCCCC ...)
//   AAA.A  – PV voltage (V)
//   BB.B   – PV current (A)
//   CCCCC  – PV power (W)
//
// prefix is "pv1" or "pv2" depending on the command sent.

func (d *TechfineInverterDriver) parsePV(fields []string, prefix string) ([]SensorData, error) {
	if len(fields) < 3 {
		return nil, fmt.Errorf("techfine PV: need >=3 fields, got %d", len(fields))
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine PV: invalid voltage %q", fields[0])
	}

	current, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine PV: invalid current %q", fields[1])
	}

	power := parseFloat(fields[2])

	return []SensorData{
		{Name: prefix + "_voltage", Value: voltage, Unit: "V"},
		{Name: prefix + "_current", Value: current, Unit: "A"},
		{Name: prefix + "_power", Value: power, Unit: "W"},
	}, nil
}

// ============================================================================
// parseHTEMP — Temperature info
// ============================================================================
// Format: (AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK ...)
//   AAA – PV temperature (°C)
//   BBB – inverter temperature (°C)
//   CCC – boost temperature (°C)
//   DDD – transformer temperature (°C)
//   EEE – max temperature (°C)
//   FFF – fan1 speed (%)
//   GGG – fan2 speed (%)
//   HI  – fan1 status
//   JJJ – PV2 temperature (°C)  (note: positions shifted, HI is 2 chars)
//   KKK – DC rectifier temperature (°C)
//
// Actually the format is: AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK
// which is 11 space-separated fields (HI is one 2-char field).
// NOTE: fan status fields [7] and [8] are space-separated single chars; needs real device verification.

func (d *TechfineInverterDriver) parseHTEMP(fields []string) ([]SensorData, error) {
	if len(fields) < 11 {
		return nil, fmt.Errorf("techfine HTEMP: need >=11 fields, got %d", len(fields))
	}

	pvTemp := parseFloat(fields[0])
	invTemp := parseFloat(fields[1])
	boostTemp := parseFloat(fields[2])
	transformerTemp := parseFloat(fields[3])
	maxTemp := parseFloat(fields[4])
	fan1Speed := parseFloat(fields[5])
	fan2Speed := parseFloat(fields[6])
	fan1Status := parseFloat(fields[7])
	fan2Status := parseFloat(fields[8])
	pv2Temp := parseFloat(fields[9])
	dcRectifierTemp := parseFloat(fields[10])

	return []SensorData{
		{Name: "pv_temp", Value: pvTemp, Unit: "°C"},
		{Name: "inverter_temp", Value: invTemp, Unit: "°C"},
		{Name: "boost_temp", Value: boostTemp, Unit: "°C"},
		{Name: "transformer_temp", Value: transformerTemp, Unit: "°C"},
		{Name: "max_temp", Value: maxTemp, Unit: "°C"},
		{Name: "fan1_speed", Value: fan1Speed, Unit: "%"},
		{Name: "fan2_speed", Value: fan2Speed, Unit: "%"},
		{Name: "fan1_status", Value: fan1Status, Unit: ""},
		{Name: "fan2_status", Value: fan2Status, Unit: ""},
		{Name: "pv2_temp", Value: pv2Temp, Unit: "°C"},
		{Name: "dc_rectifier_temp", Value: dcRectifierTemp, Unit: "°C"},
	}, nil
}

// ============================================================================
// parseHGEN — Energy generation info
// ============================================================================
// Format: (AAAAAA BB:BB CC.CCC DDDD.D EEEE.E FFFFFFFFF.F ...)
//   AAAAAA      – system date (YYYYMM)
//   BB:BB       – system time (HH:MM)
//   CC.CCC      – daily energy (kWh)
//   DDDD.D      – monthly energy (kWh)
//   EEEE.E      – yearly energy (kWh)
//   FFFFFFFFF.F – total energy (kWh)

func (d *TechfineInverterDriver) parseHGEN(fields []string) ([]SensorData, error) {
	if len(fields) < 6 {
		return nil, fmt.Errorf("techfine HGEN: need >=6 fields, got %d", len(fields))
	}

	daily := parseFloat(fields[2])
	monthly := parseFloat(fields[3])
	yearly := parseFloat(fields[4])
	total := parseFloat(fields[5])

	return []SensorData{
		{Name: "daily_energy", Value: daily, Unit: "kWh"},
		{Name: "monthly_energy", Value: monthly, Unit: "kWh"},
		{Name: "yearly_energy", Value: yearly, Unit: "kWh"},
		{Name: "total_energy", Value: total, Unit: "kWh"},
	}, nil
}

// ============================================================================
// parseHBMS1 — BMS info
// ============================================================================
// Format: (AA b7b6b5b4b3b2b1b0 c7c6c5c4c3c2c1c0 BBB.B CCC.C DDD.D EEE FFFF.F GGGG.G HHHHH ...)
//   AA      – protocol type
//   b7..b0  – BMS status 1 (8 binary digits, MSB first: b7 is first char, b0 is last)
//             b7: BMS通信正常
//             b6: BMS低电报警标志
//             b5: BMS低电故障标志
//             b4: BMS允许充电标志
//             b3: BMS允许放电标志
//             b2: BMS充电过流标志
//             b1: BMS放电过流标志
//             b0: BMS温度过低标志
//   c7..c0  – BMS status 2 (8 binary digits)
//   BBB.B   – discharge voltage limit (V)
//   CCC.C   – charge voltage limit (V)
//   DDD.D   – charge current limit (A)
//   EEE     – SOC (%)
//   FFFF.F  – charge current (A)
//   GGGG.G  – discharge current (A)
//   HHHHH   – average temperature (0.01K, convert: /100 - 273.15)

func (d *TechfineInverterDriver) parseHBMS1(fields []string) ([]SensorData, error) {
	if len(fields) < 10 {
		return nil, fmt.Errorf("techfine HBMS1: need >=10 fields, got %d", len(fields))
	}

	// BMS status flags from fields[1] (8 binary digits, MSB first)
	// status1 = "b7b6b5b4b3b2b1b0" → index 0=b7, index 1=b6, ..., index 7=b0
	status1 := fields[1]

	var bmsCommOK, bmsLowAlarm, bmsLowFault float64
	var bmsChargeAllowed, bmsDischargeAllowed float64
	var bmsChargeOvercurrent, bmsDischargeOvercurrent, bmsTempLow float64

	if len(status1) >= 8 {
		if status1[0] == '1' { // b7: BMS通信正常
			bmsCommOK = 1.0
		}
		if status1[1] == '1' { // b6: BMS低电报警标志
			bmsLowAlarm = 1.0
		}
		if status1[2] == '1' { // b5: BMS低电故障标志
			bmsLowFault = 1.0
		}
		if status1[3] == '1' { // b4: BMS允许充电标志
			bmsChargeAllowed = 1.0
		}
		if status1[4] == '1' { // b3: BMS允许放电标志
			bmsDischargeAllowed = 1.0
		}
		if status1[5] == '1' { // b2: BMS充电过流标志
			bmsChargeOvercurrent = 1.0
		}
		if status1[6] == '1' { // b1: BMS放电过流标志
			bmsDischargeOvercurrent = 1.0
		}
		if status1[7] == '1' { // b0: BMS温度过低标志
			bmsTempLow = 1.0
		}
	}

	dischargeVLimit := parseFloat(fields[3])
	chargeVLimit := parseFloat(fields[4])
	chargeILimit := parseFloat(fields[5])
	soc := parseFloat(fields[6])
	chargeCurrent := parseFloat(fields[7])
	dischargeCurrent := parseFloat(fields[8])
	tempRaw := parseFloat(fields[9])
	// HHHHH is 5 digits in 0.01K → convert to °C
	tempCelsius := tempRaw/100.0 - 273.15

	return []SensorData{
		{Name: "bms_comm_ok", Value: bmsCommOK, Unit: ""},
		{Name: "bms_charge_allowed", Value: bmsChargeAllowed, Unit: ""},
		{Name: "bms_discharge_allowed", Value: bmsDischargeAllowed, Unit: ""},
		{Name: "bms_low_alarm", Value: bmsLowAlarm, Unit: ""},
		{Name: "bms_low_fault", Value: bmsLowFault, Unit: ""},
		{Name: "bms_charge_overcurrent", Value: bmsChargeOvercurrent, Unit: ""},
		{Name: "bms_discharge_overcurrent", Value: bmsDischargeOvercurrent, Unit: ""},
		{Name: "bms_temp_low", Value: bmsTempLow, Unit: ""},
		{Name: "bms_soc", Value: soc, Unit: "%"},
		{Name: "bms_charge_current", Value: chargeCurrent, Unit: "A"},
		{Name: "bms_discharge_current", Value: dischargeCurrent, Unit: "A"},
		{Name: "bms_charge_voltage_limit", Value: chargeVLimit, Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Value: dischargeVLimit, Unit: "V"},
		{Name: "bms_charge_current_limit", Value: chargeILimit, Unit: "A"},
		{Name: "bms_temp", Value: tempCelsius, Unit: "°C"},
	}, nil
}

// ============================================================================
// parseHIMSG1 — Software version info
// ============================================================================
// Format: (NNNN.NN AAAABBCC DD
//   NNNN.NN  – software version number
//   AAAABBCC – software date (YYYYMMDD as 8-digit integer)
//   DD       – (additional data, ignored)

func (d *TechfineInverterDriver) parseHIMSG1(fields []string) ([]SensorData, error) {
	if len(fields) < 2 {
		return nil, fmt.Errorf("techfine HIMSG1: need >=2 fields, got %d", len(fields))
	}

	versionStr := fields[0]
	dateStr := fields[1]

	// Parse version "0000.03" → 0.0003 as float64 for LastData storage.
	// The version format is MMMM.NN where the minor part is treated as
	// a 4-digit fractional value: 0000.03 → 0 + 3/10000 = 0.0003.
	versionParts := strings.SplitN(versionStr, ".", 2)
	versionMajor := parseFloat(versionParts[0])
	versionMinor := 0.0
	if len(versionParts) >= 2 {
		minorInt, _ := strconv.Atoi(versionParts[1])
		versionMinor = float64(minorInt) / 10000.0
	}
	versionVal := versionMajor + versionMinor

	// Parse date "20230220" → 20230220 as float64 for LastData storage
	dateVal := parseFloat(dateStr)

	return []SensorData{
		{Name: "software_version", Value: versionVal, Unit: ""},
		{Name: "software_date", Value: dateVal, Unit: ""},
	}, nil
}

// ============================================================================
// parseHEEP1 — EEPROM settings (basic parse, stores key fields)
// ============================================================================
// Format: (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
//   A     – machine model source (0-2)
//   BBB   – max charge current
//   CCC   – (config data)
//   D     – input voltage range
//   E     – battery type
//   F     – (config)
//   G     – (config)
//   HHH   – output voltage
//   I     – output frequency
//   ... remaining fields are EEPROM settings
//
// This is a basic parser that stores selected fields. Full EEPROM parsing
// can be extended as needed.

func (d *TechfineInverterDriver) parseHEEP1(fields []string) ([]SensorData, error) {
	if len(fields) < 15 {
		return nil, fmt.Errorf("techfine HEEP1: need >=15 fields, got %d", len(fields))
	}

	// fields[8] = output frequency flag (0=50Hz, 1=60Hz)
	freqFlag := parseFloat(fields[8])
	freqValue := 50.0
	if freqFlag == 1 {
		freqValue = 60.0
	}

	result := []SensorData{
		{Name: "eeprom_model_source", Value: parseFloat(fields[0]), Unit: ""},
		{Name: "eeprom_max_charge_current", Value: parseFloat(fields[1]), Unit: "A"},
		{Name: "eeprom_output_voltage", Value: parseFloat(fields[7]), Unit: "V"},
		{Name: "eeprom_output_frequency", Value: freqValue, Unit: "Hz"},
	}

	return result, nil
}

// ============================================================================
// Helpers
// ============================================================================

// parseFloat parses a float string, returning 0 on error.
func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// isAllBinary checks if a string consists only of '0' and '1' characters.
func isAllBinary(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c != '0' && c != '1' {
			return false
		}
	}
	return true
}

// isAllDigits checks if a string consists only of digit characters.
func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isAllDigitsOrEmpty checks if a string is empty or consists only of digits.
func isAllDigitsOrEmpty(s string) bool {
	if len(s) == 0 {
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// asciiToHex encodes an ASCII string as a hex string (for CommandTemplate.WriteData).
func asciiToHex(s string) string {
	return hex.EncodeToString([]byte(s))
}

// crc16ToHex returns the CRC16-Modbus of the given ASCII command as uppercase hex.
// E.g. crc16ToHex("V230") → "A1B2"
func crc16ToHex(s string) string {
	crc := CRC16Modbus([]byte(s))
	return fmt.Sprintf("%04X", crc)
}

// ============================================================================
// GetCommandTemplates — protocol command templates
// ============================================================================

func (d *TechfineInverterDriver) GetCommandTemplates() []CommandTemplate {
	// Query commands (schedulable polling)
	queries := []CommandTemplate{
		{
			ID: "query_status", Name: "查询状态", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HSTS\r"),
			ReadLength: 30, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "故障代码、工作模式、告警标志",
		},
		{
			ID: "query_grid", Name: "查询市电信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HGRID\r"),
			ReadLength: 40, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "市电电压(V)、频率(Hz)",
		},
		{
			ID: "query_output", Name: "查询输出信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HOP\r"),
			ReadLength: 60, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "输出电压、频率、功率、负载百分比",
		},
		{
			ID: "query_battery", Name: "查询电池信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HBAT\r"),
			ReadLength: 50, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "电池电压、容量、充放电电流、BUS电压",
		},
		{
			ID: "query_pv1", Name: "查询PV1信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HPV\r"),
			ReadLength: 30, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "PV1电压(V)、电流(A)、功率(W)",
		},
		{
			ID: "query_pv2", Name: "查询PV2信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HPVB\r"),
			ReadLength: 30, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "PV2电压(V)、电流(A)、功率(W)",
		},
		{
			ID: "query_temperature", Name: "查询温度信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HTEMP\r"),
			ReadLength: 60, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "PV/逆变/升压/变压器温度、风扇转速及状态",
		},
		{
			ID: "query_energy", Name: "查询发电量", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HGEN\r"),
			ReadLength: 70, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "日/月/年/总发电量(kWh)",
		},
		{
			ID: "query_bms", Name: "查询BMS信息", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HBMS1\r"),
			ReadLength: 80, DelayMs: 200, IntervalMs: 5000, Schedulable: true,
			Description: "BMS通信状态、充放电允许、SOC、充放电电流、温度",
		},
		{
			ID: "query_eeprom", Name: "查询EEPROM设置", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HEEP1\r"),
			ReadLength: 100, DelayMs: 200, IntervalMs: 0, Schedulable: true,
			Description: "工作模式、充电电流、电池类型等EEPROM设置",
		},
		{
			ID: "query_version", Name: "查询软件版本", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("HIMSG1\r"),
			ReadLength: 30, DelayMs: 200, IntervalMs: 0, Schedulable: true,
			Description: "软件版本号及日期",
		},
		{
			ID: "query_protocol", Name: "查询协议类型", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("QPRTL\r"),
			ReadLength: 20, DelayMs: 200, IntervalMs: 0, Schedulable: true,
			Description: "协议类型标识",
		},
		{
			ID: "query_pe", Name: "查询PE使能状态", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("PE?\r"),
			ReadLength: 20, DelayMs: 200, IntervalMs: 0, Schedulable: true,
			Description: "PE功能使能状态查询",
		},
		{
			ID: "query_pd", Name: "查询PD使能状态", Type: "read",
			CmdByte: 0, WriteData: asciiToHex("PD?\r"),
			ReadLength: 20, DelayMs: 200, IntervalMs: 0, Schedulable: true,
			Description: "PD功能使能状态查询",
		},
	}

	// Control command (one-shot trigger)
	control := []CommandTemplate{
		{
			ID: "turn_on", Name: "开机", Type: "write",
			CmdByte: 0, WriteData: asciiToHex("SON\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "向逆变器发送开机指令，回复ACK",
		},
	}

	// Setting commands — parameterized with CRC16
	// Setting commands require CRC16-Modbus appended as uppercase ASCII hex before \r
	// Query commands (HSTS, HGRID, etc.) and control command (SON) do NOT require CRC16.

	settings := []CommandTemplate{}

	// V: Output voltage (220/230/240)
	for _, v := range []string{"220", "230", "240"} {
		cmd := "V" + v
		settings = append(settings, CommandTemplate{
			ID:         "set_voltage_" + v,
			Name:       "设置输出电压" + v + "V",
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置输出电压: V220/V230/V240 (带CRC16)",
		})
	}

	// F: Output frequency (50/60)
	for _, f := range []string{"50", "60"} {
		cmd := "F" + f
		settings = append(settings, CommandTemplate{
			ID:         "set_frequency_" + f,
			Name:       "设置频率" + f + "Hz",
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置输出频率: F50/F60 (带CRC16)",
		})
	}

	// PBT: Battery type (00=AGM/01=FLD/02=USER)
	for _, pbt := range []string{"00", "01", "02"} {
		cmd := "PBT" + pbt
		name := map[string]string{"00": "AGM", "01": "FLD", "02": "USER"}[pbt]
		settings = append(settings, CommandTemplate{
			ID:         "set_battery_type_" + strings.ToLower(name),
			Name:       "设置电池类型" + name,
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置电池类型: PBT" + pbt + "=" + name + " (带CRC16)",
		})
	}

	// PGR: Grid input range (00=APL/01=UPS)
	for _, pgr := range []string{"00", "01"} {
		cmd := "PGR" + pgr
		name := map[string]string{"00": "APL", "01": "UPS"}[pgr]
		settings = append(settings, CommandTemplate{
			ID:         "set_grid_range_" + strings.ToLower(name),
			Name:       "设置市电范围" + name,
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置市电输入范围: PGR" + pgr + "=" + name + " (带CRC16)",
		})
	}

	// POP: Work mode (00=UTI/01=SUB/02=SBU)
	for _, pop := range []string{"00", "01", "02"} {
		cmd := "POP" + pop
		name := map[string]string{"00": "UTI", "01": "SUB", "02": "SBU"}[pop]
		settings = append(settings, CommandTemplate{
			ID:         "set_work_mode_" + strings.ToLower(name),
			Name:       "设置工作模式" + name,
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置工作模式: POP" + pop + "=" + name + " (带CRC16)",
		})
	}

	// BMSC: BMS control (00=off/01=on)
	for _, bmsc := range []string{"00", "01"} {
		cmd := "BMSC" + bmsc
		id := map[string]string{"00": "off", "01": "on"}[bmsc]
		name := map[string]string{"00": "关闭", "01": "开启"}[bmsc]
		settings = append(settings, CommandTemplate{
			ID:         "set_bms_" + id,
			Name:       "BMS" + name,
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: "设置BMS控制: BMSC" + bmsc + "=" + name + " (带CRC16)",
		})
	}

	// Non-parameterized settings (keep existing with CRC16 added)
	otherSettings := []struct {
		id, name, cmd, desc string
	}{
		{"set_lock_voltage", "设置锁机电压", "PSDV230", "设置锁机电压(PSDV+数值)"},
		{"set_charge_voltage", "设置恒压充电电压", "PCVV144", "设置恒压充电电压(PCVV+数值)"},
		{"set_float_voltage", "设置浮充电压", "PBFT136", "设置浮充电压(PBFT+数值)"},
		{"set_total_charge_current", "设置总充电电流", "MNCHGC020", "设置总充电电流(MNCHGC+010~080A)"},
		{"set_mains_charge_current", "设置市电充电电流", "MUCHGC020", "设置市电充电电流(MUCHGC+010~060A)"},
		{"system_reset", "系统复位", "PF", "系统复位(PF)"},
	}
	for _, s := range otherSettings {
		cmd := s.cmd
		settings = append(settings, CommandTemplate{
			ID:         s.id,
			Name:       s.name,
			Type:       "write",
			CmdByte:    0,
			WriteData:  asciiToHex(cmd + crc16ToHex(cmd) + "\r"),
			ReadLength: 10, DelayMs: 200, IntervalMs: 0, Schedulable: false,
			Description: s.desc + " (带CRC16)",
		})
	}

	return append(append(queries, control...), settings...)
}
