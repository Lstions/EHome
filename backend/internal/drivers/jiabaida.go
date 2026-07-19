package drivers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// ============================================================================
// JiabaidaBMS Driver — 嘉佰达 BMS 电池管理系统 (V19 协议)
// ============================================================================
// Protocol: Binary, RS485/UART, 9600bps, big-endian
// Frame: 0xDD | CMD | STATUS/LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID 4B]
// ============================================================================

// JiabaidaBMSDriver parses Jiabaida BMS binary protocol frames.
type JiabaidaBMSDriver struct{}

func (d *JiabaidaBMSDriver) DeviceType() string      { return "jiabaida_bms" }
func (d *JiabaidaBMSDriver) DeviceName() string      { return "嘉佰达 BMS 电池管理系统" }
func (d *JiabaidaBMSDriver) OEM() string             { return "嘉佰达" }
func (d *JiabaidaBMSDriver) Category() string        { return "BMS" }
func (d *JiabaidaBMSDriver) HardwareTypes() []string { return []string{"uart"} }

// ControlActions moves the documented, side-effect-free V19 queries onto the
// unified Action Catalog. They remain disabled until a real BMS supplies the
// model/firmware/line evidence needed for rollout. In particular, no MOS,
// factory-mode, reset or parameter command is represented here.
func (d *JiabaidaBMSDriver) ControlActions() []ControlAction {
	return []ControlAction{
		jiabaidaReadAction("read_basic_info", "读取基本信息", []byte{0xDD, 0xA5, 0x03, 0x00, 0xFF, 0xFD, 0x77}, 60, "总压、电流、容量、SOC、温度与 FET 状态"),
		jiabaidaReadAction("read_cell_voltage", "读取单体电压", []byte{0xDD, 0xA5, 0x04, 0x00, 0xFF, 0xFC, 0x77}, 50, "各串电芯电压"),
		jiabaidaReadAction("read_hardware_version", "读取硬件版本", []byte{0xDD, 0xA5, 0x05, 0x00, 0xFF, 0xFB, 0x77}, 40, "硬件版本字符串"),
		jiabaidaReadAction("read_comprehensive", "读取综合信息", []byte{0xDD, 0xA5, 0x0F, 0x00, 0xFF, 0xF1, 0x77}, 100, "综合状态与单体电压"),
		jiabaidaReadAction("read_protection_count", "读取保护历史次数", []byte{0xDD, 0xA5, 0xAA, 0x00, 0xFF, 0x56, 0x77}, 40, "保护触发次数统计"),
	}
}

func jiabaidaReadAction(id, name string, tx []byte, readSize uint32, description string) ControlAction {
	return ControlAction{ID: id, Version: 1, Name: name, Description: description, Semantics: "read", Risk: "low", Enabled: false,
		TXData: append([]byte(nil), tx...), ReadSize: readSize, RXTimeoutMS: 1000}
}

// VerifyControlAction binds a successful response to its originating query.
// ParseData alone accepts multiple V19 read commands, so without this check a
// wrong-device or stale response could be stored under the wrong Action.
func (d *JiabaidaBMSDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error) {
	if string(params) != "{}" {
		return nil, fmt.Errorf("jiabaida action %q does not accept parameters", actionID)
	}
	expected, ok := map[string]byte{
		"read_basic_info": 0x03, "read_cell_voltage": 0x04, "read_hardware_version": 0x05,
		"read_comprehensive": 0x0F, "read_protection_count": 0xAA,
	}[actionID]
	if !ok {
		return nil, fmt.Errorf("unknown jiabaida control action %q", actionID)
	}
	if len(raw) < 2 || raw[0] != 0xDD || raw[1] != expected {
		return nil, fmt.Errorf("jiabaida action %q received unexpected response command", actionID)
	}
	return d.ParseData(raw)
}

func (d *JiabaidaBMSDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "total_voltage", Unit: "V"},
		{Name: "current", Unit: "A"},
		{Name: "remaining_capacity", Unit: "Ah"},
		{Name: "nominal_capacity", Unit: "Ah"},
		{Name: "cycle_count", Unit: "次"},
		{Name: "rsoc", Unit: "%"},
		{Name: "protection_status", Unit: "bitmask"},
		{Name: "fet_status", Unit: "bitmask"},
		{Name: "cell_count", Unit: "串"},
		{Name: "cell_voltage_max", Unit: "V"},
		{Name: "cell_voltage_min", Unit: "V"},
		{Name: "cell_voltage_avg", Unit: "V"},
	}
}

// ============================================================================
// Error codes
// ============================================================================

// ErrorCode represents a Jiabaida protocol error category.
type ErrorCode int

const (
	ErrInvalidFrameStart ErrorCode = iota + 1
	ErrInvalidStopByte
	ErrChecksumMismatch
	ErrIncompleteFrame
	ErrUnknownCommand
	ErrBMSStatusError
	ErrDataTooShort
)

// ParseError carries structured error information for Jiabaida protocol errors.
type ParseError struct {
	Code   ErrorCode
	Detail string
	Raw    []byte
}

func (e *ParseError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("jiabaida: [%d] %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("jiabaida: [%d]", e.Code)
}

// ============================================================================
// Checksum algorithm
// ============================================================================
// Algorithm: uint16 sum of bytes → bitwise NOT + 1 → big-endian uint16.
// Verified against 8 known frames from the protocol docs.
// Send frame checksum range: CMD + LEN + DATA (excludes 0xDD and 0xA5/0x5A).
// Response frame checksum range: LEN + DATA (excludes 0xDD, CMD, STATUS).

// jiabaidaChecksum computes the checksum over the given bytes.
func jiabaidaChecksum(data []byte) uint16 {
	var sum uint16
	for _, b := range data {
		sum += uint16(b)
	}
	return ^sum + 1
}

// verifyJiabaidaChecksum verifies the checksum of a complete raw frame.
// raw must contain the full frame from 0xDD to at least the checksum bytes.
func verifyJiabaidaChecksum(raw []byte) bool {
	if len(raw) < 7 {
		return false
	}
	cmd := raw[1]
	isSend := (cmd == 0xA5 || cmd == 0x5A)
	length := int(raw[3])
	payloadEnd := 4 + length
	if len(raw) < payloadEnd+2 {
		return false
	}
	if isSend {
		// Send frame: checksum over CMD + LEN + DATA (skip 0xDD and 0xA5/0x5A)
		return jiabaidaChecksum(raw[2:payloadEnd]) == binary.BigEndian.Uint16(raw[payloadEnd:])
	}
	// Response frame: checksum over LEN + DATA
	return jiabaidaChecksum(raw[3:payloadEnd]) == binary.BigEndian.Uint16(raw[payloadEnd:])
}

// ============================================================================
// ParseData — main entry point
// ============================================================================

// ParseData parses a raw Jiabaida BMS frame and returns sensor data.
// The frame structure:
//
//	Success response: 0xDD | CMD | STATUS(0x00) | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//	Error response:   0xDD | STATUS(0x80/0x81/0x82) | LEN(0x00) | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//	Send frame:       0xDD | 0xA5/0x5A | CMD | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//
// After the stop byte 0x77 there may be up to 4 extra CALLBACKID bytes.
// We locate the stop byte via the LEN field and ignore trailing bytes.
func (d *JiabaidaBMSDriver) ParseData(raw []byte) ([]SensorData, error) {
	// A normal frame needs at least seven bytes, but a documented zero-length
	// error frame is exactly six bytes before an optional callback ID.
	if len(raw) < 6 || raw[0] != 0xDD {
		return nil, &ParseError{Code: ErrInvalidFrameStart, Raw: raw}
	}

	// Detect error response frames before interpreting byte 1 as a command.
	// V19 permits an optional four-byte callback after the delimiter, so the
	// delimiter is at its length-derived position (5), not necessarily the last
	// byte.  For LEN=0, the response checksum is the two's-complement checksum
	// of zero, i.e. 0x0000.
	if raw[1] == 0x80 || raw[1] == 0x81 || raw[1] == 0x82 {
		status := raw[1]
		if len(raw) < 6 {
			return nil, &ParseError{Code: ErrIncompleteFrame, Detail: "truncated BMS error response", Raw: raw}
		}
		if raw[2] != 0x00 {
			return nil, &ParseError{Code: ErrIncompleteFrame, Detail: "BMS error response has nonzero length", Raw: raw}
		}
		if raw[5] != 0x77 {
			return nil, &ParseError{Code: ErrInvalidStopByte, Raw: raw}
		}
		if raw[3] != 0x00 || raw[4] != 0x00 {
			return nil, &ParseError{Code: ErrChecksumMismatch, Raw: raw}
		}
		return nil, &ParseError{
			Code:   ErrBMSStatusError,
			Detail: fmt.Sprintf("BMS status 0x%02X", status),
			Raw:    raw,
		}
	}

	// Determine if this is a send frame (write command/read request) or response frame.
	cmdOrRW := raw[1]
	var cmd, status byte
	var length int
	isSend := false

	if cmdOrRW == 0xA5 || cmdOrRW == 0x5A {
		// Send frame: 0xDD | 0xA5/0x5A | CMD | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77
		isSend = true
		cmd = raw[2]
		length = int(raw[3])
	} else {
		// Response frame: 0xDD | CMD | STATUS | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77
		cmd = cmdOrRW
		status = raw[2]
		length = int(raw[3])
	}

	// Calculate expected minimum frame length: header(4) + data(length) + checksum(2) + stop(1) = 4+LEN+2+1
	expectedLen := 4 + length + 2 + 1
	if len(raw) < expectedLen {
		return nil, &ParseError{
			Code:   ErrIncompleteFrame,
			Detail: fmt.Sprintf("expected >=%d bytes, got %d", expectedLen, len(raw)),
			Raw:    raw,
		}
	}

	// Verify stop byte 0x77
	stopBytePos := 4 + length + 2
	if raw[stopBytePos] != 0x77 {
		return nil, &ParseError{Code: ErrInvalidStopByte, Raw: raw}
	}

	// Verify checksum
	if !verifyJiabaidaChecksum(raw) {
		return nil, &ParseError{Code: ErrChecksumMismatch, Raw: raw}
	}

	// For response frames, check BMS status
	if !isSend {
		if status == 0x80 || status == 0x81 || status == 0x82 {
			return nil, &ParseError{
				Code:   ErrBMSStatusError,
				Detail: fmt.Sprintf("BMS status 0x%02X", status),
				Raw:    raw,
			}
		}
	}

	// Extract data payload
	data := raw[4 : 4+length]

	// Route only documented read commands.  0xE1 is not a factory-mode command
	// in the V19 source, but it is still a destructive dual-bit control and is
	// deliberately unavailable until its Action Catalog/readback gate is met.
	// F2/F3/F6 stay unavailable as well; their physical workflows require
	// additional factory-mode and CRC evidence.
	switch cmd {
	case 0x03:
		return d.parse0x03(data)
	case 0x04:
		return d.parse0x04(data)
	case 0x05:
		return d.parse0x05(data)
	case 0x0F:
		return d.parse0x0F(data)
	case 0xAA:
		return d.parse0xAA(data)
	default:
		return nil, &ParseError{
			Code:   ErrUnknownCommand,
			Detail: fmt.Sprintf("cmd 0x%02X (not yet enabled)", cmd),
			Raw:    raw,
		}
	}
}

// ============================================================================
// Temperature conversion helper
// ============================================================================

// jiabaidaTemperature converts raw 0.1K absolute temperature to °C.
func jiabaidaTemperature(raw uint16) float64 {
	return float64(raw)/10.0 - 273.15
}

// ============================================================================
// parse0x03 — Basic information (协议 Page 5-6)
// ============================================================================
// DATA layout (byte offsets):
//
//	[0:2]   Total voltage (uint16, 10mV) → /100 → V
//	[2:4]   Current (int16, 10mA, signed) → /100 → A
//	[4:6]   Remaining capacity (uint16, 10mAh) → /100 → Ah
//	[6:8]   Nominal capacity (uint16, 10mAh) → /100 → Ah
//	[8:10]  Cycle count (uint16)
//	[10:12] Manufacture date (uint16) — skipped
//	[12:14] Balance status low 16 cells (uint16) — skipped
//	[14:16] Balance status high 16 cells (uint16) — skipped
//	[16:18] Protection status (uint16, bitmask)
//	[18]     Software version (uint8)
//	[19]     RSOC (uint8, %)
//	[20]     FET control status (uint8)
//	[21]     Cell count (uint8)
//	[22]     NTC count (uint8)
//	[23:]    N×Temperature (uint16, 0.1K) → /10 - 273.15 → °C

func (d *JiabaidaBMSDriver) parse0x03(data []byte) ([]SensorData, error) {
	if len(data) < 23 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: fmt.Sprintf("0x03: need >=23 bytes, got %d", len(data))}
	}
	totalVoltage := float64(binary.BigEndian.Uint16(data[0:2])) / 100.0
	currentRaw := int16(binary.BigEndian.Uint16(data[2:4]))
	current := float64(currentRaw) / 100.0
	remainingCap := float64(binary.BigEndian.Uint16(data[4:6])) / 100.0
	nominalCap := float64(binary.BigEndian.Uint16(data[6:8])) / 100.0
	cycleCount := float64(binary.BigEndian.Uint16(data[8:10]))
	protectStatus := float64(binary.BigEndian.Uint16(data[16:18]))
	version := float64(data[18])
	rsoc := float64(data[19])
	fetStatus := float64(data[20])
	cellCount := int(data[21])
	ntcCount := int(data[22])

	result := []SensorData{
		{Name: "total_voltage", Value: totalVoltage, Unit: "V"},
		{Name: "current", Value: current, Unit: "A"},
		{Name: "remaining_capacity", Value: remainingCap, Unit: "Ah"},
		{Name: "nominal_capacity", Value: nominalCap, Unit: "Ah"},
		{Name: "cycle_count", Value: cycleCount, Unit: "次"},
		{Name: "rsoc", Value: rsoc, Unit: "%"},
		{Name: "protection_status", Value: protectStatus, Unit: "bitmask"},
		{Name: "fet_status", Value: fetStatus, Unit: "bitmask"},
		{Name: "cell_count", Value: float64(cellCount), Unit: "串"},
		{Name: "software_version", Value: version, Unit: ""},
	}

	tempOffset := 23
	for i := 0; i < ntcCount && tempOffset+1 < len(data); i++ {
		rawTemp := binary.BigEndian.Uint16(data[tempOffset : tempOffset+2])
		result = append(result, SensorData{
			Name:  fmt.Sprintf("temperature_%d", i+1),
			Value: jiabaidaTemperature(rawTemp),
			Unit:  "°C",
		})
		tempOffset += 2
	}
	return result, nil
}

// ============================================================================
// parse0x04 — Cell voltages (协议 Page 7-8)
// ============================================================================
// DATA layout: N×2B, each 2 bytes = cell voltage in mV → /1000 → V.
// Also computes max/min/avg.

func (d *JiabaidaBMSDriver) parse0x04(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x04: empty"}
	}
	var total, maxV, minV float64
	minV = math.MaxFloat64
	cellCount := len(data) / 2
	var result []SensorData

	for i := 0; i+1 < len(data); i += 2 {
		voltage := float64(binary.BigEndian.Uint16(data[i:i+2])) / 1000.0
		total += voltage
		if voltage > maxV {
			maxV = voltage
		}
		if voltage < minV {
			minV = voltage
		}
		result = append(result, SensorData{
			Name:  fmt.Sprintf("cell_voltage_%d", i/2+1),
			Value: voltage,
			Unit:  "V",
		})
	}
	if cellCount > 0 {
		result = append(result,
			SensorData{Name: "cell_voltage_max", Value: maxV, Unit: "V"},
			SensorData{Name: "cell_voltage_min", Value: minV, Unit: "V"},
			SensorData{Name: "cell_voltage_avg", Value: total / float64(cellCount), Unit: "V"},
		)
	}
	return result, nil
}

// ============================================================================
// parse0x0F — Comprehensive information (协议 Page 12-13)
// ============================================================================
// 0x0F is a superset of 0x03, adding cell voltages + balance + runtime.
// BYTE layout (0-indexed):
//
//	[0]     Reserved
//	[1:3]   Total voltage (uint16, 10mV)
//	[3:5]   Current (int16, 10mA)
//	[5]     SOC (uint8, %)
//	[6:8]   Remaining capacity (uint16, 10mAh)
//	[8:10]  Full capacity (uint16, 10mAh)
//	[10:12] Protection status (uint16, bitmask)
//	[12:14] Max cell voltage (uint16, mV)
//	[14:16] Min cell voltage (uint16, mV)
//	[16:18] Balance low (uint16)
//	[18:20] Balance high (uint16)
//	[20:22] Cycle count (uint16)
//	[22]    FET status (uint8)
//	[23]    NTC count (uint8)
//	[24:24+2N] Temperature array (uint16, 0.1K × N)
//	[24+2N] Cell count M (uint8)
//	[25+2N:] M×Cell voltage (uint16 × M, mV)
//	trailer: current_state(1B) + charge_capacity(2B) + runtime(2B) + sequence(2B) + humidity(1B)

func (d *JiabaidaBMSDriver) parse0x0F(data []byte) ([]SensorData, error) {
	if len(data) < 24 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x0F: too short"}
	}
	totalVoltage := float64(binary.BigEndian.Uint16(data[1:3])) / 100.0
	currentRaw := int16(binary.BigEndian.Uint16(data[3:5]))
	current := float64(currentRaw) / 100.0
	rsoc := float64(data[5])
	remainingCap := float64(binary.BigEndian.Uint16(data[6:8])) / 100.0
	fullCap := float64(binary.BigEndian.Uint16(data[8:10])) / 100.0
	protectStatus := float64(binary.BigEndian.Uint16(data[10:12]))
	maxCellMV := float64(binary.BigEndian.Uint16(data[12:14]))
	minCellMV := float64(binary.BigEndian.Uint16(data[14:16]))
	cycleCount := float64(binary.BigEndian.Uint16(data[20:22]))
	fetStatus := float64(data[22])
	ntcCount := int(data[23])

	result := []SensorData{
		{Name: "total_voltage", Value: totalVoltage, Unit: "V"},
		{Name: "current", Value: current, Unit: "A"},
		{Name: "rsoc", Value: rsoc, Unit: "%"},
		{Name: "remaining_capacity", Value: remainingCap, Unit: "Ah"},
		{Name: "nominal_capacity", Value: fullCap, Unit: "Ah"},
		{Name: "cycle_count", Value: cycleCount, Unit: "次"},
		{Name: "protection_status", Value: protectStatus, Unit: "bitmask"},
		{Name: "fet_status", Value: fetStatus, Unit: "bitmask"},
		{Name: "cell_voltage_max", Value: maxCellMV / 1000.0, Unit: "V"},
		{Name: "cell_voltage_min", Value: minCellMV / 1000.0, Unit: "V"},
	}

	// NTC temperatures
	tempOffset := 24
	for i := 0; i < ntcCount && tempOffset+1 < len(data); i++ {
		rawTemp := binary.BigEndian.Uint16(data[tempOffset : tempOffset+2])
		result = append(result, SensorData{
			Name:  fmt.Sprintf("temperature_%d", i+1),
			Value: jiabaidaTemperature(rawTemp),
			Unit:  "°C",
		})
		tempOffset += 2
	}

	// Cell voltages (after temperatures + cell count byte)
	if tempOffset < len(data) {
		cellCount := int(data[tempOffset])
		tempOffset++
		for i := 0; i < cellCount && tempOffset+1 < len(data); i++ {
			voltage := float64(binary.BigEndian.Uint16(data[tempOffset:tempOffset+2])) / 1000.0
			result = append(result, SensorData{
				Name:  fmt.Sprintf("cell_voltage_%d", i+1),
				Value: voltage,
				Unit:  "V",
			})
			tempOffset += 2
		}
	}

	return result, nil
}

// ============================================================================
// parse0xAA — Protection history counts (协议 Page 19)
// ============================================================================
// DATA layout: 12 × uint16, each = count of a protection type triggered.

func (d *JiabaidaBMSDriver) parse0xAA(data []byte) ([]SensorData, error) {
	names := []string{
		"short_circuit_count", "charge_overcurrent_count", "discharge_overcurrent_count",
		"cell_overvoltage_count", "cell_undervoltage_count",
		"charge_overtemp_count", "charge_undertemp_count",
		"discharge_overtemp_count", "discharge_undertemp_count",
		"pack_overvoltage_count", "pack_undervoltage_count", "restart_count",
	}
	var result []SensorData
	for i, name := range names {
		if i*2+1 >= len(data) {
			break
		}
		result = append(result, SensorData{
			Name:  name,
			Value: float64(binary.BigEndian.Uint16(data[i*2 : i*2+2])),
			Unit:  "次",
		})
	}
	return result, nil
}

// ============================================================================
// parse0x05 — Hardware version string (ASCII)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0x05(data []byte) ([]SensorData, error) {
	return []SensorData{{Name: "hardware_version", Value: 0, Unit: "", StringValue: string(data)}}, nil
}

// ============================================================================
// parse0xF2 — Protection parameters (53 bytes, requires factory mode)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xF2(data []byte) ([]SensorData, error) {
	if len(data) < 53 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF2: need 53 bytes"}
	}
	return []SensorData{
		{Name: "cell_ov_protect", Value: float64(binary.BigEndian.Uint16(data[0:2])), Unit: "mV"},
		{Name: "cell_ov_release", Value: float64(binary.BigEndian.Uint16(data[2:4])), Unit: "mV"},
		{Name: "cell_uv_protect", Value: float64(binary.BigEndian.Uint16(data[4:6])), Unit: "mV"},
		{Name: "cell_uv_release", Value: float64(binary.BigEndian.Uint16(data[6:8])), Unit: "mV"},
		{Name: "pack_ov_protect", Value: float64(binary.BigEndian.Uint16(data[8:10])) * 10, Unit: "mV"},
		{Name: "pack_ov_release", Value: float64(binary.BigEndian.Uint16(data[10:12])) * 10, Unit: "mV"},
		{Name: "pack_uv_protect", Value: float64(binary.BigEndian.Uint16(data[12:14])) * 10, Unit: "mV"},
		{Name: "pack_uv_release", Value: float64(binary.BigEndian.Uint16(data[14:16])) * 10, Unit: "mV"},
		{Name: "cell_ov_delay", Value: float64(data[16]), Unit: "S"},
		{Name: "cell_uv_delay", Value: float64(data[17]), Unit: "S"},
		{Name: "pack_ov_delay", Value: float64(data[18]), Unit: "S"},
		{Name: "pack_uv_delay", Value: float64(data[19]), Unit: "S"},
		{Name: "chg_ot_protect", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[20:22])), Unit: "°C"},
		{Name: "chg_ot_release", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[22:24])), Unit: "°C"},
		{Name: "chg_ut_protect", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[24:26])), Unit: "°C"},
		{Name: "chg_ut_release", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[26:28])), Unit: "°C"},
		{Name: "dis_ot_protect", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[28:30])), Unit: "°C"},
		{Name: "dis_ot_release", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[30:32])), Unit: "°C"},
		{Name: "dis_ut_protect", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[32:34])), Unit: "°C"},
		{Name: "dis_ut_release", Value: jiabaidaTemperature(binary.BigEndian.Uint16(data[34:36])), Unit: "°C"},
		{Name: "chg_oc_protect", Value: float64(binary.BigEndian.Uint16(data[40:42])) / 100, Unit: "A"},
		{Name: "dis_oc_protect", Value: float64(binary.BigEndian.Uint16(data[44:46])) / 100, Unit: "A"},
		{Name: "short_circuit_release", Value: float64(data[50]), Unit: "S"},
	}, nil
}

// ============================================================================
// parse0xF3 — System parameters (52 bytes, requires factory mode)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xF3(data []byte) ([]SensorData, error) {
	if len(data) < 52 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF3: need 52 bytes"}
	}
	return []SensorData{
		{Name: "function_config", Value: float64(binary.BigEndian.Uint16(data[0:2])), Unit: "bitmask"},
		{Name: "ntc_config", Value: float64(binary.BigEndian.Uint16(data[2:4])), Unit: "bitmask"},
		{Name: "cell_count_config", Value: float64(binary.BigEndian.Uint16(data[4:6])), Unit: "串"},
		{Name: "shunt_resistance", Value: float64(binary.BigEndian.Uint16(data[6:8])) / 10, Unit: "mΩ"},
		{Name: "balance_start_voltage", Value: float64(binary.BigEndian.Uint16(data[8:10])), Unit: "mV"},
		{Name: "balance_diff", Value: float64(binary.BigEndian.Uint16(data[10:12])), Unit: "mV"},
		{Name: "gps_shutdown_voltage", Value: float64(binary.BigEndian.Uint16(data[12:14])), Unit: "mV"},
		{Name: "gps_shutdown_delay", Value: float64(binary.BigEndian.Uint16(data[14:16])), Unit: "S"},
		{Name: "nominal_capacity_cfg", Value: float64(binary.BigEndian.Uint16(data[20:22])) / 100, Unit: "Ah"},
		{Name: "cycle_capacity_cfg", Value: float64(binary.BigEndian.Uint16(data[22:24])) / 100, Unit: "Ah"},
		{Name: "cell_full_voltage", Value: float64(binary.BigEndian.Uint16(data[24:26])), Unit: "mV"},
		{Name: "cell_empty_voltage", Value: float64(binary.BigEndian.Uint16(data[26:28])), Unit: "mV"},
		{Name: "self_discharge_rate", Value: float64(binary.BigEndian.Uint16(data[28:30])) / 10, Unit: "%"},
		{Name: "soc100_voltage", Value: float64(binary.BigEndian.Uint16(data[30:32])), Unit: "mV"},
		{Name: "soc0_voltage", Value: float64(binary.BigEndian.Uint16(data[48:50])), Unit: "mV"},
	}, nil
}

// ============================================================================
// parse0xF6 — Cell internal resistance (N×2B, signed int16, 0.1mΩ)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xF6(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF6: empty"}
	}
	var result []SensorData
	for i := 0; i+1 < len(data); i += 2 {
		raw := int16(binary.BigEndian.Uint16(data[i : i+2]))
		result = append(result, SensorData{
			Name:  fmt.Sprintf("cell_resistance_%d", i/2+1),
			Value: float64(raw) / 10.0,
			Unit:  "mΩ",
		})
	}
	return result, nil
}

// ============================================================================
// parse0xA2 — Serial number (ASCII, length-prefixed)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xA2(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xA2: empty"}
	}
	snLen := int(data[0])
	if 1+snLen > len(data) {
		snLen = len(data) - 1
	}
	return []SensorData{{Name: "serial_number", Value: 0, Unit: "", StringValue: string(data[1 : 1+snLen])}}, nil
}

// ============================================================================
// Factory mode command helpers
// ============================================================================

// FactoryModeEnterCmd returns the command to enter factory mode.
// DD 5A 00 02 56 78 FF 30 77
func FactoryModeEnterCmd() []byte {
	return []byte{0xDD, 0x5A, 0x00, 0x02, 0x56, 0x78, 0xFF, 0x30, 0x77}
}

// FactoryModeExitForRead returns the command to exit factory mode (for read, no init).
// DD 5A 01 02 00 00 FF FD 77
func FactoryModeExitForRead() []byte {
	return []byte{0xDD, 0x5A, 0x01, 0x02, 0x00, 0x00, 0xFF, 0xFD, 0x77}
}

// FactoryModeExitForWrite returns the command to exit factory mode with parameter init.
// DD 5A 01 02 28 28 FF AD 77
func FactoryModeExitForWrite() []byte {
	return []byte{0xDD, 0x5A, 0x01, 0x02, 0x28, 0x28, 0xFF, 0xAD, 0x77}
}

// CRC16Modbus computes CRC-16 (Modbus) with polynomial 0xA001, used for 0xF2/0xF3 parameter blocks.
func CRC16Modbus(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// GetCommandTemplates exposes only schedulable, side-effect-free polling
// commands.  Legacy 0xE1 MOS frames used to be returned here as one-shot
// templates, which let the old driver-command API advertise an unaudited
// physical-write path.  BMS writes must be represented by a verified Action
// Catalog definition instead, and none is enabled until real-device evidence
// covers the two MOS bits, priority, ACK and readback.
func (d *JiabaidaBMSDriver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_basic_info", Name: "读取基本信息", Type: "read",
			CmdByte: 0x03, WriteData: "DDA50300FFFD77",
			ReadLength: 60, DelayMs: 100, IntervalMs: 5000, Schedulable: true,
			Description: "总电压、电流、剩余容量、SOC、温度等",
		},
		{
			ID: "read_cell_voltage", Name: "读取单体电压", Type: "read",
			CmdByte: 0x04, WriteData: "DDA50400FFFC77",
			ReadLength: 50, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "每串电芯电压、最高/最低/平均",
		},
		{
			ID: "read_hardware_version", Name: "读取硬件版本", Type: "read",
			CmdByte: 0x05, WriteData: "DDA50500FFFB77",
			ReadLength: 40, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "硬件版本字符串",
		},
		{
			ID: "read_comprehensive", Name: "读取综合信息", Type: "read",
			CmdByte: 0x0F, WriteData: "DDA50F00FFF177",
			ReadLength: 100, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "0x03超集：含单体电压、均衡状态、运行时间",
		},
		{
			ID: "read_protection_count", Name: "读取保护历史次数", Type: "read",
			CmdByte: 0xAA, WriteData: "DDA5AA00FF5677",
			ReadLength: 40, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "12种保护触发次数统计",
		},
	}
}
