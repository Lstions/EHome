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

func (d *BMP280Driver) DeviceType() string      { return "bmp280" }
func (d *BMP280Driver) DeviceName() string      { return "BMP280 温度气压传感器" }
func (d *BMP280Driver) OEM() string             { return "博世" }
func (d *BMP280Driver) Category() string        { return "温度气压传感器" }
func (d *BMP280Driver) HardwareTypes() []string { return []string{"i2c", "spi"} }
func (d *BMP280Driver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "temperature", Unit: "°C"},
		{Name: "pressure", Unit: "hPa"},
	}
}

// ParseData intentionally fails closed: a BMP280 sample has no physical meaning
// without the calibration registers read from that exact sensor.
func (d *BMP280Driver) ParseData(raw []byte) ([]SensorData, error) {
	if len(raw) < 6 {
		return nil, fmt.Errorf("bmp280: need at least 6 bytes, got %d", len(raw))
	}
	return nil, fmt.Errorf("bmp280: calibration missing")
}

// ParseDataWithCalibration decodes F7..FC (pressure first, then temperature)
// using the Bosch BMP280 reference compensation equations.
func (d *BMP280Driver) ParseDataWithCalibration(raw, calibration []byte) ([]SensorData, error) {
	if len(raw) < 6 {
		return nil, fmt.Errorf("bmp280: need at least 6 bytes, got %d", len(raw))
	}
	if len(calibration) != 24 {
		return nil, fmt.Errorf("bmp280: calibration must be 24 bytes, got %d", len(calibration))
	}
	allFF := true
	for _, b := range calibration {
		if b != 0xff {
			allFF = false
			break
		}
	}
	if allFF {
		return nil, fmt.Errorf("bmp280: invalid all-ff calibration")
	}

	leU16 := func(offset int) uint16 { return binary.LittleEndian.Uint16(calibration[offset : offset+2]) }
	leI16 := func(offset int) int16 { return int16(leU16(offset)) }
	digT1, digT2, digT3 := leU16(0), leI16(2), leI16(4)
	digP1, digP2, digP3 := leU16(6), leI16(8), leI16(10)
	digP4, digP5, digP6 := leI16(12), leI16(14), leI16(16)
	digP7, digP8, digP9 := leI16(18), leI16(20), leI16(22)
	if digT1 == 0 || digP1 == 0 {
		return nil, fmt.Errorf("bmp280: invalid calibration")
	}

	adcP := int32(uint32(raw[0])<<12 | uint32(raw[1])<<4 | uint32(raw[2])>>4)
	adcT := int32(uint32(raw[3])<<12 | uint32(raw[4])<<4 | uint32(raw[5])>>4)
	var1 := (float64(adcT)/16384.0 - float64(digT1)/1024.0) * float64(digT2)
	var2 := ((float64(adcT)/131072.0 - float64(digT1)/8192.0) * (float64(adcT)/131072.0 - float64(digT1)/8192.0)) * float64(digT3)
	tFine := var1 + var2
	temperature := tFine / 5120.0

	p1 := tFine/2.0 - 64000.0
	p2 := p1 * p1 * float64(digP6) / 32768.0
	p2 += p1 * float64(digP5) * 2.0
	p2 = p2/4.0 + float64(digP4)*65536.0
	p1 = (float64(digP3)*p1*p1/524288.0 + float64(digP2)*p1) / 524288.0
	p1 = (1.0 + p1/32768.0) * float64(digP1)
	if p1 == 0 {
		return nil, fmt.Errorf("bmp280: invalid pressure calibration")
	}
	pressurePa := 1048576.0 - float64(adcP)
	pressurePa = (pressurePa - p2/4096.0) * 6250.0 / p1
	p1 = float64(digP9) * pressurePa * pressurePa / 2147483648.0
	p2 = pressurePa * float64(digP8) / 32768.0
	pressurePa += (p1 + p2 + float64(digP7)) / 16.0
	if temperature < -40 || temperature > 85 || pressurePa < 30000 || pressurePa > 110000 {
		return nil, fmt.Errorf("bmp280: compensated values out of range")
	}
	return []SensorData{
		{Name: "temperature", Value: temperature, Unit: "°C"},
		{Name: "pressure", Value: pressurePa / 100.0, Unit: "hPa"},
	}, nil
}

func (d *BMP280Driver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_temp_pressure", Name: "读取温度气压", Type: "read",
			CmdByte: 0xF7, WriteData: "F7",
			ReadLength: 6, DelayMs: 50, IntervalMs: 5000, Schedulable: true,
			Description: "BMP280 温度 (°C) 和气压 (hPa)",
		},
	}
}

// LKTH01Driver parses LK-TH01 sensor data
// LK-TH01: [8 bytes] → {"temperature": 25.1, "humidity": 65.0}
type LKTH01Driver struct{}

func (d *LKTH01Driver) DeviceType() string      { return "lk_th01" }
func (d *LKTH01Driver) DeviceName() string      { return "LK-TH01 温湿度传感器" }
func (d *LKTH01Driver) OEM() string             { return "路科" }
func (d *LKTH01Driver) Category() string        { return "温湿度传感器" }
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

func (d *SN3000Driver) DeviceType() string      { return "sn3000" }
func (d *SN3000Driver) DeviceName() string      { return "SN-3000 风向传感器" }
func (d *SN3000Driver) OEM() string             { return "普锐森社" }
func (d *SN3000Driver) Category() string        { return "风向传感器" }
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

func (d *PRS3001Driver) DeviceType() string      { return "prs3001" }
func (d *PRS3001Driver) DeviceName() string      { return "PRS-3001 光学雨量光照变送器" }
func (d *PRS3001Driver) OEM() string             { return "普锐森社" }
func (d *PRS3001Driver) Category() string        { return "雨量光照传感器" }
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

func (d *PRS3001Driver) ControlActions() []ControlAction {
	return []ControlAction{{
		ID: "read_rainfall", Version: 1, Name: "读取雨量",
		Description: "PRS-3001 雨量 (mm) 和光照度 (Lux)，Modbus RTU FC03",
		Semantics:   "read", Risk: "low", Enabled: false,
		TXData:   []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xc4, 0x0b},
		ReadSize: 9, RXTimeoutMS: 1000, PostTXDelayMS: 100,
	}}
}

// SN3001RainDriver handles the SN-3001-GYL RS485 optical rain gauge.  It is
// intentionally distinct from the legacy PRS-3001 entry: the documented safe
// read is one Modbus register (7-byte response), rather than a guessed
// multi-register layout.  The action remains disabled until a real device
// produces a CRC-valid response in the target deployment.
type SN3001RainDriver struct {
	configParser *parser.ConfigParser
}

func (d *SN3001RainDriver) DeviceType() string      { return "sn3001_rain" }
func (d *SN3001RainDriver) DeviceName() string      { return "SN-3001 光学雨量计" }
func (d *SN3001RainDriver) OEM() string             { return "威盟士" }
func (d *SN3001RainDriver) Category() string        { return "雨量传感器" }
func (d *SN3001RainDriver) HardwareTypes() []string { return []string{"uart"} }
func (d *SN3001RainDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{{Name: "rainfall", Unit: "mm"}}
}
func (d *SN3001RainDriver) ParseData(raw []byte) ([]SensorData, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("sn3001_rain: need at least 5 bytes, got %d", len(raw))
	}
	if raw[0] != 0x01 || raw[1] != 0x03 {
		return nil, fmt.Errorf("sn3001_rain: unexpected address/function %02x/%02x", raw[0], raw[1])
	}
	dataLen := int(raw[2])
	if len(raw) != 3+dataLen+2 {
		return nil, fmt.Errorf("sn3001_rain: frame length %d does not match byte count %d", len(raw), dataLen)
	}
	if got, want := binary.LittleEndian.Uint16(raw[len(raw)-2:]), parser.ModbusCRC16(raw[:len(raw)-2]); got != want {
		return nil, fmt.Errorf("sn3001_rain: CRC %04x, want %04x", got, want)
	}
	if dataLen != 2 {
		return nil, fmt.Errorf("sn3001_rain: rainfall read requires 2 data bytes, got %d", dataLen)
	}
	if d.configParser != nil {
		if fields, err := d.configParser.Parse(raw); err == nil && len(fields) > 0 {
			return fieldsToSensorData(fields), nil
		}
	}
	return (&PRS3001Driver{}).parseLegacy(raw)
}
func (d *SN3001RainDriver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{{
		ID: "read_rainfall", Name: "读取累计雨量", Type: "read", CmdByte: 0x03,
		WriteData: "010300000001840A", ReadLength: 7, DelayMs: 100, IntervalMs: 0,
		Schedulable: false, Description: "SN-3001 雨量寄存器 0x0000，Modbus RTU FC03",
	}}
}
func (d *SN3001RainDriver) ControlActions() []ControlAction {
	return []ControlAction{{
		ID: "read_rainfall", Version: 1, Name: "读取累计雨量",
		Description: "SN-3001 雨量寄存器 0x0000，Modbus RTU FC03",
		Semantics: "read", Risk: "low", Enabled: false,
		TXData: []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0a},
		ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100,
	}}
}

// RegisterBuiltInDrivers registers all built-in drivers.
// It delegates to the parser-aware entry point so both public registration
// paths always expose the same driver set.
func RegisterBuiltInDrivers(registry *Registry) {
	RegisterBuiltInDriversWithParsers(registry, nil)
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

	// SN-3001 rain gauge with optional ConfigParser.
	sn3001Rain := &SN3001RainDriver{}
	if pc, ok := parserConfigs["sn3001_rain"]; ok && len(pc) > 0 {
		if cp, err := parser.NewConfigParser(pc); err == nil {
			sn3001Rain.configParser = cp
		}
	}
	registry.Register(sn3001Rain)

	// Jiabaida BMS — no ConfigParser (binary protocol, handled in ParseData)
	registry.Register(&JiabaidaBMSDriver{})

	// Techfine GB3024 inverter — ASCII protocol, no ConfigParser
	registry.Register(&TechfineInverterDriver{})
}
