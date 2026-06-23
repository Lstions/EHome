package drivers

import (
	"encoding/binary"
	"fmt"
)

// BMP280Driver parses BMP280 sensor data
// BMP280: [7 bytes] → {"temperature": 25.3, "pressure": 1013.2}
type BMP280Driver struct{}

func (d *BMP280Driver) DeviceType() string     { return "bmp280" }
func (d *BMP280Driver) DeviceName() string     { return "BMP280 温度气压传感器" }
func (d *BMP280Driver) OEM() string            { return "博世" }
func (d *BMP280Driver) Category() string       { return "温度气压传感器" }
func (d *BMP280Driver) HardwareTypes() []string { return []string{"i2c", "spi"} }
func (d *BMP280Driver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "temperature", Unit: "°C"},
		{Name: "pressure", Unit: "hPa"},
	}
}

func (d *BMP280Driver) ParseData(raw []byte) ([]SensorData, error) {
	if len(raw) < 6 {
		return nil, fmt.Errorf("bmp280: need at least 6 bytes, got %d", len(raw))
	}

	// Simplified parsing - actual BMP280 requires calibration
	// raw[0:3] = temperature (20-bit, signed)
	// raw[3:6] = pressure (20-bit, unsigned)
	tempRaw := int32((uint32(raw[0]) << 12) | (uint32(raw[1]) << 4) | (uint32(raw[2]) >> 4))
	pressRaw := uint32((uint32(raw[3]) << 12) | (uint32(raw[4]) << 4) | (uint32(raw[5]) >> 4))

	// Simplified conversion (without calibration)
	temperature := float64(tempRaw) * 0.01 // Rough approximation
	pressure := float64(pressRaw) * 0.01   // Rough approximation

	return []SensorData{
		{Name: "temperature", Value: temperature, Unit: "°C"},
		{Name: "pressure", Value: pressure, Unit: "hPa"},
	}, nil
}

// LKTH01Driver parses LK-TH01 sensor data
// LK-TH01: [8 bytes] → {"temperature": 25.1, "humidity": 65.0}
type LKTH01Driver struct{}

func (d *LKTH01Driver) DeviceType() string     { return "lk_th01" }
func (d *LKTH01Driver) DeviceName() string     { return "LK-TH01 温湿度传感器" }
func (d *LKTH01Driver) OEM() string            { return "路科" }
func (d *LKTH01Driver) Category() string       { return "温湿度传感器" }
func (d *LKTH01Driver) HardwareTypes() []string { return []string{"uart"} }
func (d *LKTH01Driver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "temperature", Unit: "°C"},
		{Name: "humidity", Unit: "%RH"},
	}
}

func (d *LKTH01Driver) ParseData(raw []byte) ([]SensorData, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("lk_th01: need at least 4 bytes, got %d", len(raw))
	}

	// LK-TH01 protocol: [temp_high, temp_low, hum_high, hum_low]
	temp := float64(int16(binary.BigEndian.Uint16(raw[0:2]))) / 10.0
	humidity := float64(int16(binary.BigEndian.Uint16(raw[2:4]))) / 10.0

	return []SensorData{
		{Name: "temperature", Value: temp, Unit: "°C"},
		{Name: "humidity", Value: humidity, Unit: "%RH"},
	}, nil
}

// SN3000Driver parses SN-3000 wind direction sensor data (Modbus RTU)
// Modbus RTU response: [addr][func=0x03][byte_count][dir_hi][dir_lo][spd_hi][spd_lo][crc_lo][crc_hi]
// Wind direction = (dir_hi<<8 | dir_lo) / 10.0, unit: degrees
// Wind speed = (spd_hi<<8 | spd_lo) / 10.0, unit: m/s
type SN3000Driver struct{}

func (d *SN3000Driver) DeviceType() string     { return "sn3000" }
func (d *SN3000Driver) DeviceName() string     { return "SN-3000 风向传感器" }
func (d *SN3000Driver) OEM() string            { return "普锐森社" }
func (d *SN3000Driver) Category() string       { return "风向传感器" }
func (d *SN3000Driver) HardwareTypes() []string { return []string{"uart"} }
func (d *SN3000Driver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "wind_direction", Unit: "°"},
	}
}

func (d *SN3000Driver) ParseData(raw []byte) ([]SensorData, error) {
	// SN-3000-FXJT-N01-360 is a wind DIRECTION sensor only (no wind speed).
	// Modbus response format: [addr][func][byte_count][reg0_hi][reg0_lo][reg1_hi][reg1_lo][crc_lo][crc_hi]
	// Register 0x0000: wind direction × 10 (0-3599) → 0.0° ~ 359.9°
	// Register 0x0001: integer wind direction (0-359) — redundant, skip
	if len(raw) < 5 {
		return nil, fmt.Errorf("sn3000: need at least 5 bytes (addr+func+count+2data), got %d", len(raw))
	}

	if raw[1] != 0x03 {
		return nil, fmt.Errorf("sn3000: unexpected function code 0x%02X, expected 0x03", raw[1])
	}

	direction := float64(binary.BigEndian.Uint16(raw[3:5])) / 10.0
	return []SensorData{
		{Name: "wind_direction", Value: direction, Unit: "°"},
	}, nil
}

// PRS3001Driver parses PRS-3001 optical rainfall & illuminance sensor data (Modbus RTU)
// Brand: 普锐森社, Model: 2628100523 (shell 3001, optical rainfall+illuminance 01, 485 output)
// Default baud: 4800, address: 0x01, 8N1
//
// Registers:
//   0x0000: rainfall × 10 (0.1mm resolution) — func 03/06
//   0x0002-0x0003: illuminance (uint32, 0-200000 Lux) — func 03
//
// Two collection modes:
//  1. Rainfall only: query reg 0x0000 (1 reg), response 2 data bytes → value/10.0 mm
//  2. Rainfall + Illuminance: query reg 0x0000-0x0003 (4 regs), response 8 data bytes
//     bytes[0:2] = rainfall × 10, bytes[4:8] = illuminance uint32 Lux
//
// Modbus RTU response (func=0x03):
//
//	[addr][0x03][byte_count][data...][crc_lo][crc_hi]
type PRS3001Driver struct{}

func (d *PRS3001Driver) DeviceType() string     { return "prs3001" }
func (d *PRS3001Driver) DeviceName() string     { return "PRS-3001 光学雨量光照变送器" }
func (d *PRS3001Driver) OEM() string            { return "普锐森社" }
func (d *PRS3001Driver) Category() string       { return "雨量光照传感器" }
func (d *PRS3001Driver) HardwareTypes() []string { return []string{"uart"} }
func (d *PRS3001Driver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "rainfall", Unit: "mm"},
		{Name: "illuminance", Unit: "Lux"},
	}
}

func (d *PRS3001Driver) ParseData(raw []byte) ([]SensorData, error) {
	// Minimum Modbus RTU frame: [addr][func][byte_count][2 data bytes][crc_lo][crc_hi] = 7 bytes
	if len(raw) < 5 {
		return nil, fmt.Errorf("prs3001: need at least 5 bytes (addr+func+count+2data), got %d", len(raw))
	}

	if raw[1] != 0x03 {
		return nil, fmt.Errorf("prs3001: unexpected function code 0x%02X, expected 0x03", raw[1])
	}

	byteCount := int(raw[2])
	if len(raw) < 3+byteCount+2 {
		return nil, fmt.Errorf("prs3001: frame too short, expected %d data bytes, got %d", byteCount, len(raw)-5)
	}

	data := raw[3 : 3+byteCount]

	switch byteCount {
	case 2:
		// Rainfall only: 1 register (0x0000)
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
		}, nil

	case 6:
		// 3 registers (0x0000-0x0002): rainfall + illuminance
		// reg0 = rainfall × 10, reg1 = unused, reg2 = illuminance (0-200000 Lux, fits in uint16)
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		illuminance := float64(binary.BigEndian.Uint16(data[4:6]))
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
			{Name: "illuminance", Value: illuminance, Unit: "Lux"},
		}, nil

	case 8:
		// 4 registers (0x0000-0x0003): rainfall + full illuminance
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		// Registers 0x0002-0x0003 form a 32-bit unsigned illuminance value
		illuminance := float64(binary.BigEndian.Uint32(data[4:8]))
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
			{Name: "illuminance", Value: illuminance, Unit: "Lux"},
		}, nil

	default:
		return nil, fmt.Errorf("prs3001: unexpected byte count %d, expected 2/6/8", byteCount)
	}
}

// RegisterBuiltInDrivers registers all built-in drivers
func RegisterBuiltInDrivers(registry *Registry) {
	registry.Register(&BMP280Driver{})
	registry.Register(&LKTH01Driver{})
	registry.Register(&SN3000Driver{})
	registry.Register(&PRS3001Driver{})
}
