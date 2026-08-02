package drivers

import (
	"fmt"
)

// GenericI2CDriver is a protocol-level driver for I2C devices that don't have
// a dedicated driver implementation. It provides a standard I2C read command
// template targeting register 0xF7 (a common temperature/pressure data
// register, e.g. BMP280) with a 6-byte response.
//
// Per-device customization (register address, read length) is not supported
// through the CommandTemplateProvider interface; devices needing non-default
// parameters should register a dedicated driver (e.g. BMP280Driver).
//
// This driver replaces the legacy getTemplateParamsFromDeviceConfig I2C
// fallback (including the former bmp280 special case) in the API layer
// (handler_edge_device.go), unifying template creation onto the driver
// registry as the single source of truth.
type GenericI2CDriver struct{}

func (d *GenericI2CDriver) DeviceType() string      { return "generic_i2c" }
func (d *GenericI2CDriver) DeviceName() string      { return "通用 I2C 设备" }
func (d *GenericI2CDriver) OEM() string             { return "通用" }
func (d *GenericI2CDriver) Category() string        { return "通用 I2C 设备" }
func (d *GenericI2CDriver) HardwareTypes() []string { return []string{"i2c"} }

// GetSensorDefinitions returns nil — parsing is delegated to per-device
// parser config (DeviceConfig.Parser JSONB) handled by nodemgr.
func (d *GenericI2CDriver) GetSensorDefinitions() []SensorData {
	return nil
}

// ParseData cannot interpret raw I2C data without device-specific parser
// configuration. It fails closed; the caller (nodemgr) should use the
// ConfigParser or a dedicated driver for actual data interpretation.
func (d *GenericI2CDriver) ParseData(raw []byte) ([]SensorData, error) {
	return nil, fmt.Errorf("generic_i2c: ParseData requires device-specific parser config")
}

// GetCommandTemplates returns a standard I2C read command targeting register
// 0xF7 with a 6-byte expected response (matches the BMP280 data register
// layout, which is the most common I2C sensor covered by this generic driver).
func (d *GenericI2CDriver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID:          "read_i2c_register",
			Name:        "读取 I2C 寄存器",
			Type:        "read",
			CmdByte:     0xF7,
			WriteData:   "F7",
			ReadLength:  6,
			DelayMs:     100,
			IntervalMs:  5000,
			Schedulable: true,
			Description: "通用 I2C 读取 (寄存器地址 0xF7, 6 字节响应)",
		},
	}
}
