package drivers

import (
	"fmt"

	"ehome/backend/pkg/parser"
)

// GenericModbusDriver is a protocol-level driver for Modbus RTU devices that
// don't have a dedicated driver implementation. It provides a standard
// "read holding registers" (FC03) command template with slave address 1,
// start register 0, and register count 2 as defaults.
//
// Per-device customization (slave address, register range) is not supported
// through the CommandTemplateProvider interface; devices needing non-default
// parameters should register a dedicated driver.
//
// This driver replaces the legacy getTemplateParamsFromDeviceConfig fallback
// in the API layer (handler_edge_device.go), unifying template creation onto
// the driver registry as the single source of truth.
type GenericModbusDriver struct{}

func (d *GenericModbusDriver) DeviceType() string      { return "generic_modbus" }
func (d *GenericModbusDriver) DeviceName() string      { return "通用 Modbus RTU 设备" }
func (d *GenericModbusDriver) OEM() string             { return "通用" }
func (d *GenericModbusDriver) Category() string        { return "通用 Modbus 设备" }
func (d *GenericModbusDriver) HardwareTypes() []string { return []string{"uart"} }

// GetSensorDefinitions returns nil — parsing is delegated to per-device
// parser config (DeviceConfig.Parser JSONB) handled by nodemgr.
func (d *GenericModbusDriver) GetSensorDefinitions() []SensorData {
	return nil
}

// ParseData cannot interpret raw Modbus data without device-specific parser
// configuration. It fails closed; the caller (nodemgr) should use the
// ConfigParser or a dedicated driver for actual data interpretation.
func (d *GenericModbusDriver) ParseData(raw []byte) ([]SensorData, error) {
	return nil, fmt.Errorf("generic_modbus: ParseData requires device-specific parser config")
}

// GetCommandTemplates returns a standard Modbus RTU read-holding-registers
// command (FC03) with slave address 1, start register 0, register count 2.
// The CRC is computed at call time using parser.ModbusCRC16.
func (d *GenericModbusDriver) GetCommandTemplates() []CommandTemplate {
	// Frame: [slave=0x01][func=0x03][startReg=0x0000][regCount=0x0002]
	frame := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02}
	crc := parser.ModbusCRC16(frame)
	// Modbus RTU CRC is little-endian: low byte first, then high byte.
	writeData := fmt.Sprintf("%02X%02X%04X%04X%02X%02X",
		0x01, 0x03, uint16(0), uint16(2), crc&0xFF, crc>>8)
	// Response: [addr=1][func=1][byte_count=1][data=regCount*2][crc=2]
	readLength := uint32(3 + 2*2 + 2)
	return []CommandTemplate{
		{
			ID:          "read_holding_registers",
			Name:        "读取保持寄存器",
			Type:        "read",
			CmdByte:     0x03,
			WriteData:   writeData,
			ReadLength:  readLength,
			DelayMs:     100,
			IntervalMs:  5000,
			Schedulable: true,
			Description: "通用 Modbus RTU 读保持寄存器 (FC03) — 从机地址1, 起始寄存器0, 数量2",
		},
	}
}
