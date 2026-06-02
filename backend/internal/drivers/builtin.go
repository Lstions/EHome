package drivers

import (
	"encoding/binary"
	"fmt"
)

// BMP280Driver parses BMP280 sensor data
// BMP280: [7 bytes] → {"temperature": 25.3, "pressure": 1013.2}
type BMP280Driver struct{}

func (d *BMP280Driver) DeviceType() string { return "bmp280" }
func (d *BMP280Driver) DeviceName() string { return "BMP280 Temperature/Pressure Sensor" }

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

func (d *LKTH01Driver) DeviceType() string { return "lk_th01" }
func (d *LKTH01Driver) DeviceName() string { return "LK-TH01 Temperature/Humidity Sensor" }

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

func (d *SN3000Driver) DeviceType() string { return "sn3000" }
func (d *SN3000Driver) DeviceName() string { return "SN-3000 Wind Direction Sensor" }

func (d *SN3000Driver) ParseData(raw []byte) ([]SensorData, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("sn3000: need at least 5 bytes (addr+func+count+2data), got %d", len(raw))
	}

	// Verify function code
	if raw[1] != 0x03 {
		return nil, fmt.Errorf("sn3000: unexpected function code 0x%02X, expected 0x03", raw[1])
	}

	// Wind direction: data[3:4], high byte first, divide by 10
	direction := float64(binary.BigEndian.Uint16(raw[3:5])) / 10.0
	result := []SensorData{
		{Name: "wind_direction", Value: direction, Unit: "°"},
	}

	// Wind speed: data[5:6], if available
	if len(raw) >= 7 {
		speed := float64(binary.BigEndian.Uint16(raw[5:7])) / 10.0
		result = append(result, SensorData{Name: "wind_speed", Value: speed, Unit: "m/s"})
	}

	return result, nil
}

// RegisterBuiltInDrivers registers all built-in drivers
func RegisterBuiltInDrivers(registry *Registry) {
	registry.Register(&BMP280Driver{})
	registry.Register(&LKTH01Driver{})
	registry.Register(&SN3000Driver{})
}
