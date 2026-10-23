package databus

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/ingest"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/parser"
)

// 本文件是 M1 接入层改造的**等价性差分测试**。
//
// # 为什么需要它
//
// M1 把消费者里 83 行内联解析逻辑（ConfigParser 优先 / Driver 兜底 / 标定 /
// 命令四个分支）替换成了 ingest.Resolver。这类改造最危险的失败模式是
// 「大部分设备正常，个别分支悄悄变了行为」—— 全量测试可能全绿，因为既有
// 测试只覆盖少量设备类型。
//
// 因此这里保留一份**改造前逻辑的忠实重实现**（legacyParse），对同一批输入
// 逐字段比对新旧两版输出。任何差异立即红。
//
// # 覆盖的四个分支
//
//	1. ConfigParser 命中（普通驱动 + 有配置）
//	2. ConfigParser 跳过、Driver 普通分支
//	3. CalibrationAware 驱动（标定字节）
//	4. CommandAware 驱动（命令 write_data）
//
// 以及关键的反向断言：标定驱动**不得**被 ConfigParser 绕过（这是既有硬约束）。

// legacyParseResult 是改造前解析逻辑的产物（用于比对）。
type legacyParseResult struct {
	Fields []parser.Field
	Method string
	OK     bool
}

// legacyParse 是**改造前消费者解析逻辑的忠实重实现**。
//
// 逐行对照改造前的 consumers_heavy.go（commit 39d8617a 之前的版本），
// 只剥离了 reassembler.Consume 与日志（它们不参与解析结果）。
func legacyParse(t *testing.T, db *gorm.DB, reg *drivers.Registry,
	device models.EdgeDevice, merged []byte, commandTemplateID uint64) legacyParseResult {
	t.Helper()

	var sensorData []parser.Field
	var parseMethod string

	// Primary: calibration-aware drivers must never be bypassed by a generic parser.
	drv, _ := reg.Get(device.Type)
	_, calibrationAware := drv.(drivers.CalibrationAwareDriver)
	if !calibrationAware && device.DeviceConfigID > 0 {
		var dc models.DeviceConfig
		if err := db.First(&dc, device.DeviceConfigID).Error; err == nil {
			if len(dc.Parser) > 0 && string(dc.Parser) != "{}" && string(dc.Parser) != "null" {
				cp, err := parser.NewConfigParser(dc.Parser)
				if err == nil {
					fields, err := cp.Parse(merged)
					if err == nil && len(fields) > 0 {
						sensorData = fields
						parseMethod = fmt.Sprintf("ConfigParser(%s)", dc.Name)
					}
				}
			}
		}
	}

	// Fallback: Driver registry.
	if sensorData == nil {
		d, err := reg.Get(device.Type)
		if err != nil {
			return legacyParseResult{OK: false}
		}
		var drvData []drivers.SensorData
		if calibrationDriver, ok := d.(drivers.CalibrationAwareDriver); ok {
			var calibration models.CalibrationCache
			if err := db.Where("edge_device_id = ? AND device_type = ?", device.ID, device.Type).
				First(&calibration).Error; err != nil {
				return legacyParseResult{OK: false}
			}
			calBytes, decErr := hexDecode(calibration.Data)
			if decErr != nil {
				return legacyParseResult{OK: false}
			}
			drvData, err = calibrationDriver.ParseDataWithCalibration(merged, calBytes)
		} else if commandAware, ok := d.(drivers.CommandAwareDriver); ok {
			if commandTemplateID == 0 {
				return legacyParseResult{OK: false}
			}
			var template models.ConfigTemplate
			if lookupErr := db.Where("id = ? AND node_id = ?", commandTemplateID, device.NodeID).
				First(&template).Error; lookupErr != nil {
				return legacyParseResult{OK: false}
			}
			if template.WriteData == "" {
				return legacyParseResult{OK: false}
			}
			drvData, err = commandAware.ParseDataWithCommand(merged, template.WriteData)
		} else {
			drvData, err = d.ParseData(merged)
		}
		if err != nil {
			return legacyParseResult{OK: false}
		}
		sensorData = make([]parser.Field, len(drvData))
		for i, sd := range drvData {
			sensorData[i] = parser.Field{Name: sd.Name, Value: sd.Value, Unit: sd.Unit, StringValue: sd.StringValue}
		}
		parseMethod = fmt.Sprintf("Driver(%s)", device.Type)
	}
	return legacyParseResult{Fields: sensorData, Method: parseMethod, OK: len(sensorData) > 0}
}

// hexDecode 是 encoding/hex.DecodeString 的薄封装（避免与本文件其他 import 冲突）。
func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var b byte
		if _, err := fmt.Sscanf(s[i*2:i*2+2], "%02x", &b); err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// ---------- 差分测试 ----------

// diffCase 是一个差分用例。
type diffCase struct {
	name              string
	device            models.EdgeDevice
	deviceConfig      *models.DeviceConfig
	calibration       *models.CalibrationCache
	commandTemplate   *models.ConfigTemplate
	commandTemplateID uint64
	raw               []byte
}

// TestIngestEquivalence_Differential 逐分支比对新旧解析结果。
//
// 断言：**Fields 逐字段相等、Method 字符串相等、成功/失败判定相等**。
func TestIngestEquivalence_Differential(t *testing.T) {
	cases := []diffCase{
		{
			name:   "Driver普通分支",
			device: models.EdgeDevice{ID: 1, Type: "plain_test", Name: "p", NodeID: "n1"},
			raw:    []byte{0x2A},
		},
		{
			name:   "ConfigParser命中",
			device: models.EdgeDevice{ID: 2, Type: "plain_test", Name: "c", NodeID: "n1", DeviceConfigID: 1},
			deviceConfig: &models.DeviceConfig{
				ID: 1, Name: "cfg-a",
				Parser: []byte(`{"data_format":"binary","fields":[{"name":"x","offset":0,"type":"uint16","length":2}]}`),
			},
			raw: []byte{0x00, 0x2A},
		},
		{
			name:   "Calibration分支",
			device: models.EdgeDevice{ID: 3, Type: "calib_test", Name: "k", NodeID: "n1"},
			calibration: &models.CalibrationCache{
				EdgeDeviceID: 3, DeviceType: "calib_test", Data: "0a0b0c0d",
			},
			raw: []byte{0x01},
		},
		{
			name:              "Command分支",
			device:            models.EdgeDevice{ID: 4, Type: "cmd_test", Name: "m", NodeID: "n1"},
			commandTemplate:   &models.ConfigTemplate{ID: 10, NodeID: "n1", WriteData: "4850560d"},
			commandTemplateID: 10,
			raw:               []byte{0x01},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newEquivalenceDB(t)
			reg := drivers.NewRegistry()
			reg.Register(&equivPlainDriver{})
			reg.Register(&equivCalibDriver{})
			reg.Register(&equivCmdDriver{})

			// 准备数据
			if err := db.Create(&models.Node{ID: 1, NodeID: tc.device.NodeID, Name: "n"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&tc.device).Error; err != nil {
				t.Fatal(err)
			}
			if tc.deviceConfig != nil {
				if err := db.Create(tc.deviceConfig).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.calibration != nil {
				if err := db.Create(tc.calibration).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.commandTemplate != nil {
				if err := db.Create(tc.commandTemplate).Error; err != nil {
					t.Fatal(err)
				}
			}

			// 旧逻辑
			legacy := legacyParse(t, db, reg, tc.device, tc.raw, tc.commandTemplateID)

			// 新逻辑（走 consumer 的 decodeFrame）
			c := NewSensorParserConsumerWithRegistry(db, nil, nil, reg)
			outcome := c.decodeFrame(tc.device, tc.raw, nil, nowFixed(), tc.commandTemplateID)

			if legacy.OK != outcome.ok() {
				t.Fatalf("成功判定不一致: legacy.OK=%v new.ok()=%v (reason=%s err=%v)",
					legacy.OK, outcome.ok(), outcome.FailReason, outcome.Err)
			}
			if !legacy.OK {
				return // 两边都失败，等价
			}

			if legacy.Method != outcome.Sample.Method {
				t.Errorf("Method 不一致: legacy=%q new=%q", legacy.Method, outcome.Sample.Method)
			}
			if len(legacy.Fields) != len(outcome.Sample.Fields) {
				t.Fatalf("字段数不一致: legacy=%d new=%d", len(legacy.Fields), len(outcome.Sample.Fields))
			}
			for i := range legacy.Fields {
				lf, nf := legacy.Fields[i], outcome.Sample.Fields[i]
				if lf.Name != nf.Name || lf.Value != nf.Value || lf.Unit != nf.Unit || lf.StringValue != nf.StringValue {
					t.Errorf("字段[%d] 不一致: legacy=%+v new=%+v", i, lf, nf)
				}
			}
		})
	}
}

// TestIngestEquivalence_CalibrationNotBypassedByConfigParser 是**反向断言**。
//
// 若标定驱动被 ConfigParser 绕过，会得到未标定的原始值 —— 这是既有代码里
// 写死的硬约束，也是本次改造最容易弄坏的地方（配置解析器排在前面）。
func TestIngestEquivalence_CalibrationNotBypassedByConfigParser(t *testing.T) {
	db := newEquivalenceDB(t)
	reg := drivers.NewRegistry()
	reg.Register(&equivCalibDriver{})
	reg.Register(&equivPlainDriver{})

	if err := db.Create(&models.Node{ID: 1, NodeID: "n1", Name: "n"}).Error; err != nil {
		t.Fatal(err)
	}
	// 标定设备**同时**配了 DeviceConfig —— 这正是"可能被绕过"的场景
	dc := models.DeviceConfig{
		ID: 1, Name: "trap",
		Parser: []byte(`{"data_format":"binary","fields":[{"name":"from_config_parser","offset":0,"type":"uint16","length":2}]}`),
	}
	if err := db.Create(&dc).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{ID: 9, Type: "calib_test", Name: "k", NodeID: "n1", DeviceConfigID: 1}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CalibrationCache{
		EdgeDeviceID: 9, DeviceType: "calib_test", Data: "0a0b",
	}).Error; err != nil {
		t.Fatal(err)
	}

	c := NewSensorParserConsumerWithRegistry(db, nil, nil, reg)
	outcome := c.decodeFrame(dev, []byte{0x00, 0x2A}, nil, nowFixed(), 0)
	if !outcome.ok() {
		t.Fatalf("标定设备解析失败: reason=%s err=%v", outcome.FailReason, outcome.Err)
	}
	// 必须走 Driver，绝不能是 ConfigParser
	if want := "Driver(calib_test)"; outcome.Sample.Method != want {
		t.Errorf("Method = %q, want %q —— 标定驱动被 ConfigParser 绕过了！",
			outcome.Sample.Method, want)
	}
	for _, f := range outcome.Sample.Fields {
		if f.Name == "from_config_parser" {
			t.Fatal("出现 ConfigParser 的字段 ⇒ 标定驱动被绕过（会产生未标定读数污染历史）")
		}
	}
}

// ---------- 测试辅助 ----------

func newEquivalenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.EdgeDevice{},
		&models.DeviceConfig{}, &models.CalibrationCache{}, &models.ConfigTemplate{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func nowFixed() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }

// ---------- 测试驱动 ----------

type equivPlainDriver struct{}

func (*equivPlainDriver) ParseData([]byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "plain_v", Value: 7, Unit: "V"}}, nil
}
func (*equivPlainDriver) DeviceType() string                         { return "plain_test" }
func (*equivPlainDriver) DeviceName() string                         { return "Plain" }
func (*equivPlainDriver) OEM() string                                { return "t" }
func (*equivPlainDriver) Category() string                           { return "t" }
func (*equivPlainDriver) HardwareTypes() []string                    { return nil }
func (*equivPlainDriver) GetSensorDefinitions() []drivers.SensorData { return nil }

type equivCalibDriver struct{ equivPlainDriver }

func (*equivCalibDriver) DeviceType() string { return "calib_test" }
func (*equivCalibDriver) ParseDataWithCalibration(raw, cal []byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "calib_v", Value: float64(len(cal)), Unit: "C"}}, nil
}

type equivCmdDriver struct{ equivPlainDriver }

func (*equivCmdDriver) DeviceType() string { return "cmd_test" }
func (*equivCmdDriver) ParseDataWithCommand(raw []byte, writeData string) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "cmd_v", Value: float64(len(writeData)), Unit: ""}}, nil
}

// 编译期断言：确保本文件用的 ingest 包被真实引用（防止改动后静默未使用）。
var _ = ingest.QualityOk
