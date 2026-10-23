package drivers

import (
	"encoding/binary"
	"fmt"
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

// GetSensorDefinitions 返回该驱动的字段声明（含中文描述与语义类型）。
//
// LabelZh/ValueType 是**静态知识**（物理名 → 含义），不随帧变化；
// 它们由 jiaabaida 协议文档的字段表直接映射，改字段名时中文名就在旁边，
// 不会出现"改了名忘了改描述"的漂移。
func (d *JiabaidaBMSDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "total_voltage", Unit: "V", LabelZh: "电池组总电压", ValueType: "number"},
		{Name: "current", Unit: "A", LabelZh: "电池电流", ValueType: "number"},
		{Name: "remaining_capacity", Unit: "Ah", LabelZh: "剩余容量", ValueType: "number"},
		{Name: "nominal_capacity", Unit: "Ah", LabelZh: "标称容量", ValueType: "number"},
		{Name: "cycle_count", Unit: "次", LabelZh: "循环次数", ValueType: "number"},
		{Name: "rsoc", Unit: "%", LabelZh: "剩余电量百分比", ValueType: "number"},
		{Name: "protection_status", Unit: "bitmask", LabelZh: "保护状态位", ValueType: "bitmask"},
		{Name: "fet_status", Unit: "bitmask", LabelZh: "MOS 管开关状态位", ValueType: "bitmask"},
		{Name: "cell_count", Unit: "串", LabelZh: "电芯串数", ValueType: "number"},
		{Name: "cell_voltage_max", Unit: "V", LabelZh: "单体最高电压", ValueType: "number"},
		{Name: "cell_voltage_min", Unit: "V", LabelZh: "单体最低电压", ValueType: "number"},
		{Name: "cell_voltage_avg", Unit: "V", LabelZh: "单体平均电压", ValueType: "number"},
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
