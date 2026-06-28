package drivers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"ehome/backend/pkg/parser"
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

func (d *BMP280Driver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_temp_pressure", Name: "读取温度气压", Type: "read",
			CmdByte: 0x00, WriteData: "",
			ReadLength: 6, DelayMs: 50, IntervalMs: 5000, Schedulable: true,
			Description: "BMP280 温度 (°C) 和气压 (hPa)",
		},
	}
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

func (d *LKTH01Driver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_temp_humidity", Name: "读取温湿度", Type: "read",
			CmdByte: 0x00, WriteData: "",
			ReadLength: 4, DelayMs: 50, IntervalMs: 5000, Schedulable: true,
			Description: "LK-TH01 温度 (°C) 和湿度 (%RH)",
		},
	}
}

// SN3000Driver parses SN-3000 wind direction sensor data (Modbus RTU)
// Delegates to pkg/parser.ConfigParser for actual parsing.
type SN3000Driver struct {
	configParser *parser.ConfigParser
}

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
	// Try ConfigParser first (from DeviceConfig.Parser JSONB)
	if d.configParser != nil {
		fields, err := d.configParser.Parse(raw)
		if err == nil && len(fields) > 0 {
			return fieldsToSensorData(fields), nil
		}
	}
	// Fallback: legacy hardcoded parsing
	return d.parseLegacy(raw)
}

func (d *SN3000Driver) parseLegacy(raw []byte) ([]SensorData, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("sn3000: need at least 5 bytes, got %d", len(raw))
	}
	if raw[1] != 0x03 {
		return nil, fmt.Errorf("sn3000: unexpected function code 0x%02X", raw[1])
	}
	direction := float64(binary.BigEndian.Uint16(raw[3:5])) / 10.0
	return []SensorData{
		{Name: "wind_direction", Value: direction, Unit: "°"},
	}, nil
}

// PRS3001Driver parses PRS-3001 optical rainfall & illuminance sensor data (Modbus RTU)
// Delegates to pkg/parser.ConfigParser for actual parsing.
type PRS3001Driver struct {
	configParser *parser.ConfigParser
}

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
	// Try ConfigParser first (from DeviceConfig.Parser JSONB)
	if d.configParser != nil {
		fields, err := d.configParser.Parse(raw)
		if err == nil && len(fields) > 0 {
			return fieldsToSensorData(fields), nil
		}
	}
	// Fallback: legacy hardcoded parsing
	return d.parseLegacy(raw)
}

func (d *PRS3001Driver) parseLegacy(raw []byte) ([]SensorData, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("prs3001: need at least 5 bytes, got %d", len(raw))
	}
	if raw[1] != 0x03 {
		return nil, fmt.Errorf("prs3001: unexpected function code 0x%02X", raw[1])
	}

	byteCount := int(raw[2])
	if len(raw) < 3+byteCount+2 {
		return nil, fmt.Errorf("prs3001: frame too short, expected %d data bytes, got %d", byteCount, len(raw)-5)
	}

	data := raw[3 : 3+byteCount]

	switch byteCount {
	case 2:
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
		}, nil
	case 6:
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		illuminance := float64(binary.BigEndian.Uint16(data[4:6]))
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
			{Name: "illuminance", Value: illuminance, Unit: "Lux"},
		}, nil
	case 8:
		rainfall := float64(binary.BigEndian.Uint16(data[0:2])) / 10.0
		illuminance := float64(binary.BigEndian.Uint32(data[4:8]))
		return []SensorData{
			{Name: "rainfall", Value: rainfall, Unit: "mm"},
			{Name: "illuminance", Value: illuminance, Unit: "Lux"},
		}, nil
	default:
		return nil, fmt.Errorf("prs3001: unexpected byte count %d, expected 2/6/8", byteCount)
	}
}

// fieldsToSensorData converts parser.Field slice to drivers.SensorData slice.
func fieldsToSensorData(fields []parser.Field) []SensorData {
	result := make([]SensorData, len(fields))
	for i, f := range fields {
		result[i] = SensorData{Name: f.Name, Value: f.Value, Unit: f.Unit, StringValue: f.StringValue}
	}
	return result
}

func (d *SN3000Driver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_wind_direction", Name: "读取风向", Type: "read",
			CmdByte: 0x03, WriteData: "0103000000018458",
			ReadLength: 7, DelayMs: 50, IntervalMs: 5000, Schedulable: true,
			Description: "SN-3000 风向角度 (°)，Modbus RTU FC03",
		},
	}
}

func (d *PRS3001Driver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_rainfall", Name: "读取雨量", Type: "read",
			CmdByte: 0x03, WriteData: "010300000002C40B",
			ReadLength: 9, DelayMs: 100, IntervalMs: 5000, Schedulable: true,
			Description: "PRS-3001 雨量 (mm) 和光照度 (Lux)，Modbus RTU FC03",
		},
	}
}

// RegisterBuiltInDrivers registers all built-in drivers.
// If parserJSON is provided for a device type, the driver will use ConfigParser
// as the primary parsing path, falling back to legacy hardcoded logic.
func RegisterBuiltInDrivers(registry *Registry) {
	registry.Register(&BMP280Driver{})
	registry.Register(&LKTH01Driver{})
	registry.Register(&SN3000Driver{})
	registry.Register(&PRS3001Driver{})
	// JiabaidaBMSDriver registered in RegisterBuiltInDriversWithParsers
}

// RegisterBuiltInDriversWithParsers registers built-in drivers with ConfigParser overrides.
// parserConfigs maps device_type → DeviceConfig.Parser JSONB.
// When a ConfigParser is available, the driver delegates to it first.
func RegisterBuiltInDriversWithParsers(registry *Registry, parserConfigs map[string]json.RawMessage) {
	registry.Register(&BMP280Driver{})
	registry.Register(&LKTH01Driver{})

	// SN3000 with optional ConfigParser
	sn3000 := &SN3000Driver{}
	if pc, ok := parserConfigs["sn3000"]; ok && len(pc) > 0 {
		if cp, err := parser.NewConfigParser(pc); err == nil {
			sn3000.configParser = cp
		}
	}
	registry.Register(sn3000)

	// PRS3001 with optional ConfigParser
	prs3001 := &PRS3001Driver{}
	if pc, ok := parserConfigs["prs3001"]; ok && len(pc) > 0 {
		if cp, err := parser.NewConfigParser(pc); err == nil {
			prs3001.configParser = cp
		}
	}
	registry.Register(prs3001)

	// Jiabaida BMS — no ConfigParser (binary protocol, handled in ParseData)
	registry.Register(&JiabaidaBMSDriver{})
}
