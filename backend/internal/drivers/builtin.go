package drivers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"

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
func (d *LKTH01Driver) OEM() string             { return "蓝控" }
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
	}, {
		ID: "reset_rainfall", Version: 1, Name: "清零累计雨量",
		Description: "清零后必须读取累计值为零并完成对账；普锐森协议清零帧尚未导入",
		Semantics:   "reset", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
		AtMostOnce: true, MaxSteps: 3, AvailabilityCode: "protocol_unverified",
		AvailabilityReason: "普锐森清零寄存器/CRC 及写入→读回→恢复证据尚未冻结",
	}}
}

// SN3001RainDriver handles the SN-3001-GYL RS485 optical rain gauge.  It is
// intentionally distinct from the legacy PRS-3001 entry: the documented safe
// read is one Modbus register (7-byte response), rather than a guessed
// multi-register layout.  Its development rollout is backed by the dedicated
// C6/UART1 real-device evidence; production risk gates remain enforced by the
// command-execution policy.
type SN3001RainDriver struct {
	configParser *parser.ConfigParser
}

func (d *SN3001RainDriver) DeviceType() string      { return "sn3001_rain" }
func (d *SN3001RainDriver) DeviceName() string      { return "SN-3001 光学雨量计" }
func (d *SN3001RainDriver) OEM() string             { return "威盟士" }
func (d *SN3001RainDriver) Category() string        { return "雨量传感器" }
func (d *SN3001RainDriver) HardwareTypes() []string { return []string{"uart"} }
func (d *SN3001RainDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "rainfall", Unit: "mm"}, {Name: "rain_sensitivity", Unit: "raw"},
		{Name: "device_address", Unit: "address"}, {Name: "baud_rate", Unit: "bit/s"},
	}
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
		Semantics:   "read", Risk: "low", Enabled: true,
		TXData:   []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0a},
		ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100,
	}, {
		ID: "read_rain_sensitivity", Version: 1, Name: "读取雨量灵敏度",
		Description: "SN-3001 寄存器 0x0052",
		Semantics:   "read", Risk: "low", Enabled: true,
		TXData: modbusReadFrame(0x01, 0x0052, 1), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100,
	}, {
		ID: "read_device_address", Version: 1, Name: "读取设备地址",
		Description: "SN-3001 寄存器 0x07D0",
		Semantics:   "read", Risk: "low", Enabled: true,
		TXData: modbusReadFrame(0xff, 0x07d0, 1), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100,
	}, {
		ID: "read_baud_rate", Version: 1, Name: "读取设备波特率",
		Description: "SN-3001 寄存器 0x07D1",
		Semantics:   "read", Risk: "low", Enabled: true,
		TXData: modbusReadFrame(0xff, 0x07d1, 1), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100,
	}, {
		ID: "reset_rainfall", Version: 1, Name: "清零累计雨量",
		Description: "SN-3001 清零寄存器 0x0000=0x005A；执行后必须重新读取确认",
		Semantics:   "reset", Risk: "high", Enabled: false, ExecutionShape: "bounded_sequence", Verification: "readback", AtMostOnce: true, MaxSteps: 3,
		AvailabilityCode: "hardware_evidence_required", AvailabilityReason: "已完成开发实机 ACK/读回及节点 bounded durable replay；生产仍需独立故障注入放行",
	}, {
		ID: "clear_rainfall_write", Version: 1, Name: "发送雨量清零",
		Description: "开发验证用单步清零写入；收到回显后必须立即执行读取确认",
		Semantics:   "reset", Risk: "high", Enabled: false, Verification: "ack", AtMostOnce: true,
		TXData: modbusWriteFrame(0x01, 0x0000, 0x005a), ReadSize: 8, RXTimeoutMS: 1500, PostTXDelayMS: 100,
		AvailabilityCode: "hardware_evidence_required", AvailabilityReason: "仅允许开发实机证据；生产必须使用 bounded 清零工作流",
	}, {
		ID: "set_rain_sensitivity", Version: 1, Name: "设置雨量灵敏度", Description: "写入寄存器 0x0052，默认值 60，修改后需重新读取确认",
		Semantics: "set", Risk: "high", Enabled: false, Verification: "readback", AtMostOnce: true,
		AvailabilityCode: "hardware_evidence_required", AvailabilityReason: "已完成开发实机 ACK/读回及节点 durable replay；生产仍需独立高风险放行",
		Parameters: []ControlParameter{{Name: "value", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)}},
	}, {
		ID: "set_device_address", Version: 1, Name: "设置设备地址", Description: "写入寄存器 0x07D0，范围 1~254",
		Semantics: "set", Risk: "critical", Enabled: false, Verification: "readback", AtMostOnce: true,
		AvailabilityCode: "hardware_evidence_required", AvailabilityReason: "已完成 1→2→1 开发实机恢复和地址配置同步；生产仍需独立高风险放行",
		Parameters: []ControlParameter{
			{Name: "value", Type: "integer", Required: true, Minimum: floatPtr(1), Maximum: floatPtr(254)},
			{Name: "source_address", Type: "integer", Required: false, Minimum: floatPtr(1), Maximum: floatPtr(254)},
		},
	}, {
		ID: "set_baud_rate", Version: 1, Name: "设置设备波特率", Description: "写入寄存器 0x07D1；2400/4800/9600",
		Semantics: "set", Risk: "critical", Enabled: false, Verification: "readback", AtMostOnce: true,
		AvailabilityCode: "hardware_evidence_required", AvailabilityReason: "已完成 4800↔9600 开发实机切换、UART manifest 同步和读回；生产仍需独立高风险放行",
		Parameters: []ControlParameter{
			{Name: "value", Type: "string", Required: true, Enum: []string{"2400", "4800", "9600"}},
			{Name: "source_address", Type: "integer", Required: false, Minimum: floatPtr(1), Maximum: floatPtr(254)},
		},
	}}
}

func floatPtr(value float64) *float64 { return &value }

func modbusReadFrame(address byte, register uint16, count uint16) []byte {
	frame := []byte{address, 0x03, byte(register >> 8), byte(register), byte(count >> 8), byte(count), 0, 0}
	crc := parser.ModbusCRC16(frame[:6])
	frame[6], frame[7] = byte(crc), byte(crc>>8)
	return frame
}

func modbusWriteFrame(address byte, register, value uint16) []byte {
	frame := []byte{address, 0x06, byte(register >> 8), byte(register), byte(value >> 8), byte(value), 0, 0}
	crc := parser.ModbusCRC16(frame[:6])
	frame[6], frame[7] = byte(crc), byte(crc>>8)
	return frame
}

func (d *SN3001RainDriver) CompileControlAction(actionID string, params json.RawMessage) (CompiledControlStep, error) {
	var raw struct {
		Value         json.RawMessage `json:"value"`
		SourceAddress json.RawMessage `json:"source_address"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return CompiledControlStep{}, fmt.Errorf("decode %s params: %w", actionID, err)
	}
	registers := map[string]uint16{"set_rain_sensitivity": 0x0052, "set_device_address": 0x07d0, "set_baud_rate": 0x07d1}
	register, ok := registers[actionID]
	if !ok {
		return CompiledControlStep{}, fmt.Errorf("action %q is not parameterized", actionID)
	}
	var value uint64
	if actionID == "set_baud_rate" {
		var baud string
		if err := json.Unmarshal(raw.Value, &baud); err != nil {
			return CompiledControlStep{}, fmt.Errorf("baud value must be string: %w", err)
		}
		switch baud {
		case "2400":
			value = 0
		case "4800":
			value = 1
		case "9600":
			value = 2
		default:
			return CompiledControlStep{}, fmt.Errorf("unsupported baud rate %q", baud)
		}
	} else {
		var number json.Number
		if err := json.Unmarshal(raw.Value, &number); err != nil {
			return CompiledControlStep{}, fmt.Errorf("value must be integer: %w", err)
		}
		parsed, err := strconv.ParseUint(string(number), 10, 16)
		if err != nil {
			return CompiledControlStep{}, fmt.Errorf("invalid value: %w", err)
		}
		value = parsed
	}
	address := uint64(1)
	if len(raw.SourceAddress) != 0 {
		var number json.Number
		if err := json.Unmarshal(raw.SourceAddress, &number); err != nil {
			return CompiledControlStep{}, fmt.Errorf("source_address must be integer: %w", err)
		}
		parsed, err := strconv.ParseUint(string(number), 10, 8)
		if err != nil || parsed < 1 || parsed > 254 {
			return CompiledControlStep{}, fmt.Errorf("source_address must be between 1 and 254")
		}
		address = parsed
	}
	return CompiledControlStep{TXData: modbusWriteFrame(byte(address), register, uint16(value)), ReadSize: 8, RXTimeoutMS: 1500, PostTXDelayMS: 100}, nil
}

func (d *SN3001RainDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error) {
	if actionID == "reset_rainfall" {
		steps, err := decodeSN3001BatchRaw(raw)
		if err != nil {
			return nil, err
		}
		if len(steps) != 3 {
			return nil, fmt.Errorf("sn3001_rain: reset requires three verified responses")
		}
		if _, err := verifySN3001ReadRainfall(steps[0]); err != nil {
			return nil, fmt.Errorf("sn3001_rain: pre-read: %w", err)
		}
		clear := modbusWriteFrame(0x01, 0x0000, 0x005a)
		if !bytes.Equal(steps[1], clear) {
			return nil, fmt.Errorf("sn3001_rain: clear response does not echo request")
		}
		final, err := verifySN3001ReadRainfall(steps[2])
		if err != nil || final != 0 {
			if err != nil {
				return nil, fmt.Errorf("sn3001_rain: readback: %w", err)
			}
			return nil, fmt.Errorf("sn3001_rain: readback rainfall is %v, want 0", final)
		}
		return []SensorData{{Name: "rainfall", Value: 0, Unit: "mm"}, {Name: "reset_ack", Value: 1, Unit: "ack"}}, nil
	}
	if len(raw) < 5 {
		return nil, fmt.Errorf("sn3001_rain: response too short")
	}
	if got, want := binary.LittleEndian.Uint16(raw[len(raw)-2:]), parser.ModbusCRC16(raw[:len(raw)-2]); got != want {
		return nil, fmt.Errorf("sn3001_rain: response CRC %04x, want %04x", got, want)
	}
	if actionID == "reset_rainfall" || actionID == "clear_rainfall_write" || actionID == "set_rain_sensitivity" || actionID == "set_device_address" || actionID == "set_baud_rate" {
		step, err := CompiledControlStep{}, error(nil)
		if actionID == "reset_rainfall" || actionID == "clear_rainfall_write" {
			step = CompiledControlStep{TXData: modbusWriteFrame(0x01, 0x0000, 0x005a)}
		} else {
			step, err = d.CompileControlAction(actionID, params)
		}
		if err != nil {
			return nil, err
		}
		if string(raw) != string(step.TXData) {
			return nil, fmt.Errorf("sn3001_rain: write response does not echo request")
		}
		return []SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, nil
	}
	if raw[1] != 0x03 {
		return nil, fmt.Errorf("sn3001_rain: unexpected function 0x%02x", raw[1])
	}
	if int(raw[2])+5 != len(raw) {
		return nil, fmt.Errorf("sn3001_rain: invalid response length")
	}
	data := raw[3 : 3+int(raw[2])]
	switch actionID {
	case "read_rainfall":
		if len(data) != 2 {
			return nil, fmt.Errorf("rainfall response data length")
		}
		return []SensorData{{Name: "rainfall", Value: float64(binary.BigEndian.Uint16(data)) / 10, Unit: "mm"}}, nil
	case "read_rain_sensitivity", "read_device_address", "read_baud_rate":
		if len(data) != 2 {
			return nil, fmt.Errorf("configuration response data length")
		}
		value := float64(binary.BigEndian.Uint16(data))
		name, unit := "", "raw"
		switch actionID {
		case "read_rain_sensitivity":
			name = "rain_sensitivity"
		case "read_device_address":
			name, unit = "device_address", "address"
		case "read_baud_rate":
			name, unit = "baud_rate", "bit/s"
			switch uint16(value) {
			case 0:
				value = 2400
			case 1:
				value = 4800
			case 2:
				value = 9600
			}
		}
		return []SensorData{{Name: name, Value: value, Unit: unit}}, nil
	default:
		return nil, fmt.Errorf("unknown SN-3001 action %q", actionID)
	}
}

func decodeSN3001BatchRaw(raw []byte) ([][]byte, error) {
	if len(raw) < 1 || raw[0] < 2 || raw[0] > 8 {
		return nil, fmt.Errorf("sn3001_rain: invalid batch response")
	}
	steps := make([][]byte, 0, raw[0])
	pos := 1
	for i := 0; i < int(raw[0]); i++ {
		if pos+3 > len(raw) {
			return nil, fmt.Errorf("sn3001_rain: truncated batch response")
		}
		length := int(binary.LittleEndian.Uint16(raw[pos+1 : pos+3]))
		pos += 3 // kind byte plus little-endian length
		if length == 0 || pos+length > len(raw) {
			return nil, fmt.Errorf("sn3001_rain: invalid batch step length")
		}
		steps = append(steps, append([]byte(nil), raw[pos:pos+length]...))
		pos += length
	}
	if pos != len(raw) {
		return nil, fmt.Errorf("sn3001_rain: trailing batch response bytes")
	}
	return steps, nil
}

func verifySN3001ReadRainfall(raw []byte) (float64, error) {
	if len(raw) != 7 || raw[0] != 0x01 || raw[1] != 0x03 || raw[2] != 0x02 {
		return 0, fmt.Errorf("invalid rainfall response")
	}
	if got, want := binary.LittleEndian.Uint16(raw[5:]), parser.ModbusCRC16(raw[:5]); got != want {
		return 0, fmt.Errorf("rainfall CRC %04x, want %04x", got, want)
	}
	return float64(binary.BigEndian.Uint16(raw[3:5])) / 10, nil
}

// CompileControlActionPlan compiles the confirmed SN-3001 clear workflow.
// The plan is intentionally not executable by the current single-step node:
// it must be dispatched atomically with durable at-most-once replay state.
func (d *SN3001RainDriver) CompileControlActionPlan(actionID string, params json.RawMessage) (CompiledControlPlan, error) {
	if actionID != "reset_rainfall" {
		return CompiledControlPlan{}, fmt.Errorf("sn3001_rain action %q has no bounded plan", actionID)
	}
	if string(params) != "{}" && len(params) != 0 {
		return CompiledControlPlan{}, fmt.Errorf("sn3001_rain reset does not accept parameters")
	}
	return CompiledControlPlan{
		AtMostOnce: true,
		Steps: []CompiledControlPlanStep{
			{ID: "read_before", Kind: "read", TXData: []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0A}, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			{ID: "clear_accumulated", Kind: "write", TXData: []byte{0x01, 0x06, 0x00, 0x00, 0x00, 0x5A, 0x09, 0xF1}, ReadSize: 8, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			{ID: "readback_zero", Kind: "readback", TXData: []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0A}, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
		},
	}, nil
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
