package ingest

import (
	"ehome/backend/pkg/parser"
)

// AdapterConfigParser 是 DeviceConfig.parser（配置化解析）的接入适配。
//
// # 在解析链里的位置
//
// 注册在 AdapterDriver **之前**：配置解析优先于驱动解析（既有语义）。
//
// # 为什么标定驱动必须绕过它
//
// 标定（calibration）是驱动解码不变量的一部分 —— 用通用配置解析去解标定设备的
// 帧，得到的是**未标定的原始值**，会污染历史数据且事后无法区分。
// 因此 Supports 显式排除 DriverKindCalibration（与改造前 consumer 的
// 「calibrationAware 时不走 ConfigParser」完全等价）。
//
// 命名带 Adapter 前缀是为了在 main.go 的注册行里一眼看出这是适配器而非实现。
type AdapterConfigParser struct{}

// configParserName 是 adapter 名（有界枚举，进指标标签）。
const configParserName = "config_parser"

// NewAdapterConfigParser 构造配置解析适配器。
func NewAdapterConfigParser() *AdapterConfigParser { return &AdapterConfigParser{} }

// Name 实现 Adapter。
func (*AdapterConfigParser) Name() string { return configParserName }

// Supports 实现 Adapter。
//
// 条件（与改造前 consumers_heavy.go:270-274 逐条对齐）：
//   - 不是标定驱动（标定必须走驱动自己的解码）
//   - 有 DeviceConfig 且 parser JSON 非空
//
// 注意：这里**不判断 parser JSON 是否合法** —— 那是 Decode 的职责。
// Supports 必须是廉价的纯判断（每帧调用），JSON 解析放 Decode。
func (*AdapterConfigParser) Supports(ctx SupportContext) bool {
	return ctx.DriverKind != DriverKindCalibration && ctx.HasDeviceConfig
}

// Decode 实现 Adapter。
func (*AdapterConfigParser) Decode(ctx DecodeContext) (Sample, error) {
	if len(ctx.ConfigParserJSON) == 0 {
		return Sample{}, NewDecodeError(configParserName, ReasonDecodeFailed, nil)
	}
	cp, err := parser.NewConfigParser(ctx.ConfigParserJSON)
	if err != nil {
		return Sample{}, NewDecodeError(configParserName, ReasonDecodeFailed, err)
	}
	fields, err := cp.Parse(ctx.Raw)
	if err != nil {
		return Sample{}, NewDecodeError(configParserName, ReasonDecodeFailed, err)
	}
	if len(fields) == 0 {
		return Sample{}, NewDecodeError(configParserName, ReasonEmptyResult, nil)
	}

	// Method 字符串保持改造前格式：fmt.Sprintf("ConfigParser(%s)", dc.Name)
	// 它已进生产日志与审计，改格式会造成历史不可比（M1 等价性判据之一）。
	return Sample{
		EdgeDeviceID:    ctx.EdgeDevice.ID,
		LogicalDeviceID: ctx.LogicalDeviceID,
		At:              ctx.At,
		Fields:          fromParserFields(fields),
		Method:          "ConfigParser(" + ctx.ConfigName + ")",
		Quality:         QualityOk,
	}, nil
}

// fromParserFields 把 pkg/parser.Field 转换为 ingest.Field（逐字段等价）。
func fromParserFields(in []parser.Field) []Field {
	out := make([]Field, len(in))
	for i, f := range in {
		out[i] = Field{Name: f.Name, Value: f.Value, Unit: f.Unit, StringValue: f.StringValue}
	}
	return out
}
