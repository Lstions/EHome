package drivers

import (
	"encoding/binary"
	"fmt"
	"math"
)

// jiabaidaFieldValue converts a raw field value for the parse projection.
// Scale>=1 multiplies; Scale<1 divides by the exact reciprocal (1/Scale is
// exactly 10/100 in float64), bit-identical to the legacy /10 and /100
// divisions.  Temp fields use the 0.1K → °C conversion.
func jiabaidaFieldValue(raw uint16, f jiabaidaField) float64 {
	if f.Temp {
		return jiabaidaTemperature(raw)
	}
	if f.Scale >= 1 {
		return float64(raw) * f.Scale
	}
	return float64(raw) / (1.0 / f.Scale)
}

// jiabaidaParseBlock projects the declared (Parse=true) fields of a parameter
// block into SensorData in table order (== legacy parse order).
func jiabaidaParseBlock(data []byte, table []jiabaidaField) []SensorData {
	var result []SensorData
	for _, f := range table {
		if !f.Parse {
			continue
		}
		var raw uint16
		if f.Width == 2 {
			raw = binary.BigEndian.Uint16(data[f.Offset : f.Offset+2])
		} else {
			raw = uint16(data[f.Offset])
		}
		result = append(result, SensorData{Name: f.Name, Value: jiabaidaFieldValue(raw, f), Unit: f.Unit})
	}
	return result
}

// ============================================================================
// Temperature conversion helper
// ============================================================================

// jiabaidaTemperature converts raw 0.1K absolute temperature to °C.
func jiabaidaTemperature(raw uint16) float64 {
	return float64(raw)/10.0 - 273.15
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
	case 0x0C:
		return d.parse0x0C(data)
	case 0x0F:
		return d.parse0x0F(data)
	case 0xA2:
		return d.parse0xA2(data)
	case 0xAA:
		return d.parse0xAA(data)
	case 0xF0:
		return d.parse0xF0(data)
	case 0xF2:
		return d.parse0xF2(data)
	case 0xF3:
		return d.parse0xF3(data)
	case 0xF6:
		return d.parse0xF6(data)
	default:
		return nil, &ParseError{
			Code:   ErrUnknownCommand,
			Detail: fmt.Sprintf("cmd 0x%02X (not yet enabled)", cmd),
			Raw:    raw,
		}
	}
}

// parse0x0C — MOS test status readback (协议 §7.5)
// DATA: [放电 MOS 测试状态, 充电 MOS 测试状态]
// 状态值: 0=未测试 1=OK 2=NG(可能损坏) 3=超时未加载电流
func (d *JiabaidaBMSDriver) parse0x0C(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x0C: need 2 bytes"}
	}
	return []SensorData{
		{Name: "discharge_mos_test_status", Value: float64(data[0]), Unit: "state"},
		{Name: "charge_mos_test_status", Value: float64(data[1]), Unit: "state"},
	}, nil
}

// parse0xF0 — custom attributes readback (协议 §7.8)
// DATA: 3 × uint16 自定义字段，大端
func (d *JiabaidaBMSDriver) parse0xF0(data []byte) ([]SensorData, error) {
	if len(data) < 6 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF0: need 6 bytes"}
	}
	return []SensorData{
		{Name: "custom_attr_1", Value: float64(binary.BigEndian.Uint16(data[0:2])), Unit: "raw"},
		{Name: "custom_attr_2", Value: float64(binary.BigEndian.Uint16(data[2:4])), Unit: "raw"},
		{Name: "custom_attr_3", Value: float64(binary.BigEndian.Uint16(data[4:6])), Unit: "raw"},
	}, nil
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
	if len(data) < 24 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xAA: need 24 bytes"}
	}
	names := []string{
		"short_circuit_count", "charge_overcurrent_count", "discharge_overcurrent_count",
		"cell_overvoltage_count", "cell_undervoltage_count",
		"charge_overtemp_count", "charge_undertemp_count",
		"discharge_overtemp_count", "discharge_undertemp_count",
		"pack_overvoltage_count", "pack_undervoltage_count", "restart_count",
	}
	var result []SensorData
	for i, name := range names {
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
	if len(data) < 1 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x05: empty"}
	}
	return []SensorData{{Name: "hardware_version", Value: 0, Unit: "", StringValue: string(data)}}, nil
}

// ============================================================================
// parse0xF2 — Protection parameters (53 bytes, requires factory mode)
// ============================================================================
// The read projection is generated from the jiabaidaF2Fields() table
// (Parse=true fields, table order), so field layout cannot drift from the
// block compiler and schema.

func (d *JiabaidaBMSDriver) parse0xF2(data []byte) ([]SensorData, error) {
	if len(data) < 53 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF2: need 53 bytes"}
	}
	return jiabaidaParseBlock(data, jiabaidaF2Fields()), nil
}

// ============================================================================
// parse0xF3 — System parameters (52 bytes, requires factory mode)
// ============================================================================
// Read projection generated from the jiabaidaF3Fields() table; reserved byte
// ranges (16-19, 32-47) carry no fields and are never projected.

func (d *JiabaidaBMSDriver) parse0xF3(data []byte) ([]SensorData, error) {
	if len(data) < 52 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF3: need 52 bytes"}
	}
	return jiabaidaParseBlock(data, jiabaidaF3Fields()), nil
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
