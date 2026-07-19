package drivers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"

	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"
)

func init() {
	logger.Init("warn")
}

// === BMP280Driver edge cases ===

func TestBMP280Driver_ParseData_TooShort(t *testing.T) {
	d := &BMP280Driver{}
	_, err := d.ParseData([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestBMP280Driver_ParseData_Exact6Bytes(t *testing.T) {
	d := &BMP280Driver{}
	if _, err := d.ParseData([]byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00}); err == nil {
		t.Fatal("uncalibrated BMP280 data must fail closed")
	}
}

func TestBMP280Driver_ParseDataWithCalibration_BoschReferenceVector(t *testing.T) {
	d := &BMP280Driver{}
	calibration := []byte{0x70, 0x6b, 0x43, 0x67, 0x18, 0xfc, 0x7d, 0x8e, 0x43, 0xd6, 0xd0, 0x0b, 0x27, 0x0b, 0x8c, 0x00, 0xf9, 0xff, 0x8c, 0x3c, 0xf8, 0xc6, 0x70, 0x17}
	data, err := d.ParseDataWithCalibration([]byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00}, calibration)
	if err != nil {
		t.Fatalf("ParseDataWithCalibration: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("sensor count: got %d, want 2", len(data))
	}
	if data[0].Name != "temperature" || math.Abs(data[0].Value-25.08) > 0.02 {
		t.Fatalf("temperature: got %#v, want about 25.08 C", data[0])
	}
	if data[1].Name != "pressure" || math.Abs(data[1].Value-1006.53) > 0.05 {
		t.Fatalf("pressure: got %#v, want about 1006.53 hPa", data[1])
	}
}

func TestBMP280Driver_ParseDataWithCalibration_RejectsInvalidCalibration(t *testing.T) {
	d := &BMP280Driver{}
	raw := []byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00}
	for _, calibration := range [][]byte{nil, make([]byte, 23), bytesOf(0xff, 24), make([]byte, 24)} {
		if _, err := d.ParseDataWithCalibration(raw, calibration); err == nil {
			t.Fatalf("invalid calibration %x must fail", calibration)
		}
	}
}

func bytesOf(value byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func TestBMP280Driver_ParseData_7Bytes(t *testing.T) {
	d := &BMP280Driver{}
	if _, err := d.ParseData([]byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32, 0xFF}); err == nil {
		t.Fatal("uncalibrated BMP280 data must fail closed")
	}
}

func TestBMP280Driver_ParseData_Empty(t *testing.T) {
	d := &BMP280Driver{}
	_, err := d.ParseData([]byte{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestBMP280Driver_Metadata(t *testing.T) {
	d := &BMP280Driver{}
	if d.DeviceType() != "bmp280" {
		t.Errorf("DeviceType: got %s", d.DeviceType())
	}
	if d.DeviceName() != "BMP280 温度气压传感器" {
		t.Errorf("DeviceName: got %s", d.DeviceName())
	}
	if d.OEM() != "博世" {
		t.Errorf("OEM: got %s", d.OEM())
	}
	if d.Category() != "温度气压传感器" {
		t.Errorf("Category: got %s", d.Category())
	}
	ht := d.HardwareTypes()
	if len(ht) != 2 || ht[0] != "i2c" || ht[1] != "spi" {
		t.Errorf("HardwareTypes: got %v", ht)
	}
	defs := d.GetSensorDefinitions()
	if len(defs) != 2 {
		t.Errorf("GetSensorDefinitions: got %d, want 2", len(defs))
	}
}

// === LKTH01Driver edge cases ===

func TestLKTH01Driver_ParseData_TooShort(t *testing.T) {
	d := &LKTH01Driver{}
	_, err := d.ParseData([]byte{0x00, 0x01})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestLKTH01Driver_ParseData_NegativeTemp(t *testing.T) {
	d := &LKTH01Driver{}
	// -25.1°C = -251 → 0xFF05 in big-endian int16
	raw := []byte{0xFF, 0x05, 0x02, 0x08}
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	if data[0].Value >= 0 {
		t.Errorf("expected negative temperature, got %f", data[0].Value)
	}
}

func TestLKTH01Driver_ParseData_Exact4Bytes(t *testing.T) {
	d := &LKTH01Driver{}
	raw := []byte{0x00, 0xFB, 0x02, 0x08} // 25.1°C, 52.0%RH
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 sensors, got %d", len(data))
	}
	if data[0].Name != "temperature" {
		t.Errorf("sensor[0] name: got %s", data[0].Name)
	}
	if data[1].Name != "humidity" {
		t.Errorf("sensor[1] name: got %s", data[1].Name)
	}
}

func TestLKTH01Driver_ParseData_Empty(t *testing.T) {
	d := &LKTH01Driver{}
	_, err := d.ParseData([]byte{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestLKTH01Driver_Metadata(t *testing.T) {
	d := &LKTH01Driver{}
	if d.DeviceType() != "lk_th01" {
		t.Errorf("DeviceType: got %s", d.DeviceType())
	}
	if d.OEM() != "蓝控" {
		t.Errorf("OEM: got %s", d.OEM())
	}
	if d.Category() != "温湿度传感器" {
		t.Errorf("Category: got %s", d.Category())
	}
	ht := d.HardwareTypes()
	if len(ht) != 1 || ht[0] != "uart" {
		t.Errorf("HardwareTypes: got %v", ht)
	}
	defs := d.GetSensorDefinitions()
	if len(defs) != 2 {
		t.Errorf("GetSensorDefinitions: got %d, want 2", len(defs))
	}
}

// === SN3000Driver edge cases ===

func TestSN3000Driver_ParseData_TooShort(t *testing.T) {
	d := &SN3000Driver{}
	_, err := d.ParseData([]byte{0x01, 0x03, 0x02})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestSN3000Driver_ParseData_InvalidFuncCode(t *testing.T) {
	d := &SN3000Driver{}
	raw := []byte{0x01, 0x04, 0x02, 0x00, 0x64} // func code 0x04 instead of 0x03
	_, err := d.ParseData(raw)
	if err == nil {
		t.Error("expected error for invalid function code")
	}
}

func TestSN3000Driver_ParseData_ValidLegacy(t *testing.T) {
	d := &SN3000Driver{}
	// Valid Modbus RTU: addr=0x01, func=0x03, then 2 data bytes for direction
	raw := []byte{0x01, 0x03, 0x02, 0x03, 0xE8} // direction = 1000/10 = 100.0°
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 sensor, got %d", len(data))
	}
	if data[0].Name != "wind_direction" {
		t.Errorf("name: got %s, want wind_direction", data[0].Name)
	}
	if data[0].Unit != "°" {
		t.Errorf("unit: got %s, want °", data[0].Unit)
	}
}

func TestSN3000Driver_ParseData_WithConfigParser(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "modbus",
		"fields": [{"name": "wind_direction", "type": "uint16", "scale": 0.1, "offset": 3, "unit": "°"}]
	}`)
	cp, err := parser.NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser: %v", err)
	}
	d := &SN3000Driver{configParser: cp}

	// Build valid Modbus frame: addr=1, func=3, byte_count=2, data=0x03E8, CRC=2bytes
	raw := []byte{0x01, 0x03, 0x02, 0x03, 0xE8, 0x00, 0x00}
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData with ConfigParser: %v", err)
	}
	if len(data) < 1 {
		t.Fatalf("expected at least 1 sensor, got %d", len(data))
	}
}

func TestSN3000Driver_ParseData_ConfigParserFails_Fallback(t *testing.T) {
	// ConfigParser that will fail → fallback to legacy
	badParser := json.RawMessage(`{"data_format": "invalid"}`)
	cp, _ := parser.NewConfigParser(badParser)
	d := &SN3000Driver{configParser: cp}

	raw := []byte{0x01, 0x03, 0x02, 0x03, 0xE8}
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData fallback: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 sensor from fallback, got %d", len(data))
	}
}

func TestSN3000Driver_Metadata(t *testing.T) {
	d := &SN3000Driver{}
	if d.DeviceType() != "sn3000" {
		t.Errorf("DeviceType: got %s", d.DeviceType())
	}
	if d.OEM() != "普锐森社" {
		t.Errorf("OEM: got %s", d.OEM())
	}
	if d.Category() != "风向传感器" {
		t.Errorf("Category: got %s", d.Category())
	}
	defs := d.GetSensorDefinitions()
	if len(defs) != 1 || defs[0].Name != "wind_direction" {
		t.Errorf("GetSensorDefinitions: got %v", defs)
	}
}

// === PRS3001Driver edge cases ===

func TestPRS3001Driver_ParseData_TooShort(t *testing.T) {
	d := &PRS3001Driver{}
	_, err := d.ParseData([]byte{0x01, 0x03, 0x02})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestPRS3001Driver_ParseData_InvalidFuncCode(t *testing.T) {
	d := &PRS3001Driver{}
	raw := []byte{0x01, 0x04, 0x02, 0x00, 0x64}
	_, err := d.ParseData(raw)
	if err == nil {
		t.Error("expected error for invalid function code")
	}
}

func TestPRS3001Driver_ParseData_2ByteCount(t *testing.T) {
	d := &PRS3001Driver{}
	// byte_count=2: rainfall only
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x14, 0x00, 0x00} // rainfall=20/10=2.0mm
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData 2-byte: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 sensor, got %d", len(data))
	}
	if data[0].Name != "rainfall" {
		t.Errorf("name: got %s, want rainfall", data[0].Name)
	}
}

func TestPRS3001Driver_ParseData_6ByteCount(t *testing.T) {
	d := &PRS3001Driver{}
	// byte_count=6: rainfall + illuminance (uint16)
	data := make([]byte, 3+6+2)                 // header(3) + data(6) + crc(2)
	data[0] = 0x01                              // addr
	data[1] = 0x03                              // func
	data[2] = 0x06                              // byte_count
	binary.BigEndian.PutUint16(data[3:5], 50)   // rainfall = 5.0mm
	binary.BigEndian.PutUint16(data[5:7], 0)    // padding
	binary.BigEndian.PutUint16(data[7:9], 1000) // illuminance = 1000 Lux

	result, err := d.ParseData(data)
	if err != nil {
		t.Fatalf("ParseData 6-byte: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 sensors, got %d", len(result))
	}
	if result[0].Name != "rainfall" {
		t.Errorf("sensor[0] name: got %s, want rainfall", result[0].Name)
	}
	if result[1].Name != "illuminance" {
		t.Errorf("sensor[1] name: got %s, want illuminance", result[1].Name)
	}
}

func TestPRS3001Driver_ParseData_8ByteCount(t *testing.T) {
	d := &PRS3001Driver{}
	// byte_count=8: rainfall + illuminance (uint32)
	data := make([]byte, 3+8+2)
	data[0] = 0x01
	data[1] = 0x03
	data[2] = 0x08
	binary.BigEndian.PutUint16(data[3:5], 50)     // rainfall
	binary.BigEndian.PutUint16(data[5:7], 0)      // padding
	binary.BigEndian.PutUint32(data[7:11], 50000) // illuminance uint32

	result, err := d.ParseData(data)
	if err != nil {
		t.Fatalf("ParseData 8-byte: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 sensors, got %d", len(result))
	}
}

func TestPRS3001Driver_ParseData_UnexpectedByteCount(t *testing.T) {
	d := &PRS3001Driver{}
	// byte_count=4: unexpected
	data := make([]byte, 3+4+2)
	data[0] = 0x01
	data[1] = 0x03
	data[2] = 0x04
	binary.BigEndian.PutUint16(data[3:5], 50)
	binary.BigEndian.PutUint16(data[5:7], 100)

	_, err := d.ParseData(data)
	if err == nil {
		t.Error("expected error for unexpected byte count")
	}
}

func TestSN3001RainDriver_ParseData_ValidFC03(t *testing.T) {
	driver := &SN3001RainDriver{}
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x14}
	crc := parser.ModbusCRC16(raw)
	raw = append(raw, byte(crc), byte(crc>>8))
	values, err := driver.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData: %v", err)
	}
	if len(values) != 1 || values[0].Name != "rainfall" || values[0].Value != 2.0 || values[0].Unit != "mm" {
		t.Fatalf("values = %#v", values)
	}
}

func TestSN3001RainDriver_ParseData_RejectsBadCRC(t *testing.T) {
	_, err := (&SN3001RainDriver{}).ParseData([]byte{0x01, 0x03, 0x02, 0x00, 0x14, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected CRC rejection")
	}
}

func TestPRS3001Driver_ParseData_FrameTooShort(t *testing.T) {
	d := &PRS3001Driver{}
	// byte_count=6 but only 5 data bytes
	data := []byte{0x01, 0x03, 0x06, 0x00, 0x14}
	_, err := d.ParseData(data)
	if err == nil {
		t.Error("expected error for frame too short")
	}
}

func TestPRS3001Driver_ParseData_WithConfigParser(t *testing.T) {
	parserJSON := json.RawMessage(`{
		"data_format": "modbus",
		"fields": [{"name": "rainfall", "type": "uint16", "scale": 0.1, "offset": 3, "unit": "mm"}]
	}`)
	cp, err := parser.NewConfigParser(parserJSON)
	if err != nil {
		t.Fatalf("NewConfigParser: %v", err)
	}
	d := &PRS3001Driver{configParser: cp}

	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x14, 0x00, 0x00}
	data, err := d.ParseData(raw)
	if err != nil {
		t.Fatalf("ParseData with ConfigParser: %v", err)
	}
	if len(data) < 1 {
		t.Fatalf("expected at least 1 sensor, got %d", len(data))
	}
}

func TestPRS3001Driver_Metadata(t *testing.T) {
	d := &PRS3001Driver{}
	if d.DeviceType() != "prs3001" {
		t.Errorf("DeviceType: got %s", d.DeviceType())
	}
	if d.OEM() != "普锐森社" {
		t.Errorf("OEM: got %s", d.OEM())
	}
	if d.Category() != "雨量光照传感器" {
		t.Errorf("Category: got %s", d.Category())
	}
	defs := d.GetSensorDefinitions()
	if len(defs) != 2 {
		t.Errorf("GetSensorDefinitions: got %d, want 2", len(defs))
	}
}

func TestSN3001RainResetPlanGoldenVector(t *testing.T) {
	d := &SN3001RainDriver{}
	plan, err := d.CompileControlActionPlan("reset_rainfall", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CompileControlActionPlan error = %v", err)
	}
	if !plan.AtMostOnce || len(plan.Steps) != 3 {
		t.Fatalf("plan metadata = %+v", plan)
	}
	wantClear := []byte{0x01, 0x06, 0x00, 0x00, 0x00, 0x5A, 0x09, 0xF1}
	if got := plan.Steps[1].TXData; string(got) != string(wantClear) {
		t.Fatalf("clear frame = % X, want % X", got, wantClear)
	}
	if plan.Steps[1].ReadSize != 8 || plan.Steps[2].Kind != "readback" {
		t.Fatalf("clear/readback steps = %+v", plan.Steps)
	}
}

func TestSN3001ProtocolActionGoldenVectors(t *testing.T) {
	d := &SN3001RainDriver{}
	actions := d.ControlActions()
	if len(actions) != 9 {
		t.Fatalf("got %d SN-3001 actions, want 9", len(actions))
	}
	step, err := d.CompileControlAction("set_rain_sensitivity", json.RawMessage(`{"value":60}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("% X", step.TXData), "01 06 00 52 00 3C 28 0A"; got != want {
		t.Fatalf("sensitivity frame %s, want %s", got, want)
	}
	step, err = d.CompileControlAction("set_baud_rate", json.RawMessage(`{"value":"9600"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("% X", step.TXData), "01 06 07 D1 00 02 59 46"; got != want {
		t.Fatalf("baud frame %s, want %s", got, want)
	}
	step, err = d.CompileControlAction("set_device_address", json.RawMessage(`{"value":1,"source_address":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("% X", step.TXData), "02 06 07 D0 00 01 48 B4"; got != want {
		t.Fatalf("address restore frame %s, want %s", got, want)
	}
}

// === Registry edge cases ===

func TestRegistry_GetNonExistent(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent driver")
	}
}

func TestRegistry_ListEmpty(t *testing.T) {
	reg := NewRegistry()
	types := reg.List()
	if len(types) != 0 {
		t.Errorf("expected empty list, got %d", len(types))
	}
}

func TestRegistry_RegisterAndList(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&BMP280Driver{})
	reg.Register(&LKTH01Driver{})

	types := reg.List()
	if len(types) != 2 {
		t.Errorf("expected 2 types, got %d", len(types))
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&BMP280Driver{})
	reg.Register(&BMP280Driver{}) // overwrite

	types := reg.List()
	if len(types) != 1 {
		t.Errorf("expected 1 type after overwrite, got %d", len(types))
	}
}

// === Global registry ===

func TestGlobalRegistry(t *testing.T) {
	// The generic path must register the complete built-in set.
	RegisterBuiltInDrivers(GlobalRegistry())

	types := List()
	if len(types) < 6 {
		t.Errorf("expected at least 6 global drivers, got %d", len(types))
	}

	driver, err := Get("bmp280")
	if err != nil {
		t.Fatalf("Get bmp280: %v", err)
	}
	if driver.DeviceType() != "bmp280" {
		t.Errorf("device type: got %s", driver.DeviceType())
	}
}

func TestRegisterBuiltInDriversWithParsers(t *testing.T) {
	reg := NewRegistry()

	// With nil/empty parser configs
	RegisterBuiltInDriversWithParsers(reg, nil)
	types := reg.List()
	if len(types) != 7 {
		t.Errorf("expected 7 drivers, got %d", len(types))
	}
}

func TestRegisterBuiltInDrivers_RegistersSameSetAsParserAwareEntryPoint(t *testing.T) {
	legacy := NewRegistry()
	withParsers := NewRegistry()

	RegisterBuiltInDrivers(legacy)
	RegisterBuiltInDriversWithParsers(withParsers, nil)

	legacyTypes := legacy.List()
	withParserTypes := withParsers.List()
	slices.Sort(legacyTypes)
	slices.Sort(withParserTypes)
	if !slices.Equal(legacyTypes, withParserTypes) {
		t.Fatalf("registration sets differ: legacy=%v parser-aware=%v", legacyTypes, withParserTypes)
	}
	want := []string{"bmp280", "jiabaida_bms", "lk_th01", "prs3001", "sn3000", "sn3001_rain", "techfine_inverter"}
	if !slices.Equal(legacyTypes, want) {
		t.Fatalf("registered types = %v, want %v", legacyTypes, want)
	}
}

func TestRegisterBuiltInDriversWithParsers_ValidParser(t *testing.T) {
	reg := NewRegistry()

	parserConfigs := map[string]json.RawMessage{
		"sn3000":  json.RawMessage(`{"data_format":"modbus","fields":[{"name":"wind_direction","type":"uint16","scale":0.1,"offset":3,"unit":"°"}]}`),
		"prs3001": json.RawMessage(`{"data_format":"modbus","fields":[{"name":"rainfall","type":"uint16","scale":0.1,"offset":3,"unit":"mm"}]}`),
	}
	RegisterBuiltInDriversWithParsers(reg, parserConfigs)

	types := reg.List()
	if len(types) != 7 {
		t.Errorf("expected 7 drivers, got %d", len(types))
	}
}

func TestRegisterBuiltInDriversWithParsers_InvalidParser(t *testing.T) {
	reg := NewRegistry()

	parserConfigs := map[string]json.RawMessage{
		"sn3000": json.RawMessage(`invalid json`),
	}
	RegisterBuiltInDriversWithParsers(reg, parserConfigs)

	// Should still register the driver (just without ConfigParser)
	types := reg.List()
	if len(types) != 7 {
		t.Errorf("expected 7 drivers even with invalid parser, got %d", len(types))
	}
}

func TestRegisterBuiltInDriversWithParsers_EmptyParser(t *testing.T) {
	reg := NewRegistry()

	parserConfigs := map[string]json.RawMessage{
		"sn3000": json.RawMessage(``),
	}
	RegisterBuiltInDriversWithParsers(reg, parserConfigs)

	types := reg.List()
	if len(types) != 7 {
		t.Errorf("expected 7 drivers, got %d", len(types))
	}
}

// === fieldsToSensorData ===

func TestFieldsToSensorData(t *testing.T) {
	fields := []parser.Field{
		{Name: "temperature", Value: 25.3, Unit: "°C"},
		{Name: "humidity", Value: 65.0, Unit: "%RH"},
	}
	result := fieldsToSensorData(fields)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].Name != "temperature" {
		t.Errorf("name: got %s", result[0].Name)
	}
	if result[0].Value != 25.3 {
		t.Errorf("value: got %f", result[0].Value)
	}
	if result[0].Unit != "°C" {
		t.Errorf("unit: got %s", result[0].Unit)
	}
}

func TestFieldsToSensorData_Empty(t *testing.T) {
	result := fieldsToSensorData([]parser.Field{})
	if len(result) != 0 {
		t.Errorf("expected 0, got %d", len(result))
	}
}
