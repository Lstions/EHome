package ingest

import (
	"ehome/backend/internal/drivers"
)

// AdapterDriver 是驱动注册表（ParseData 家族）的接入适配。
//
// # 覆盖三种驱动分支
//
// 同一台设备的驱动可能是普通驱动，也可能实现可选接口：
//
//	drivers.Driver                  → ParseData(raw)
//	drivers.CalibrationAwareDriver  → ParseDataWithCalibration(raw, calibration)
//	drivers.CommandAwareDriver      → ParseDataWithCommand(raw, writeData)
//
// 三种分支的选择**由调用方算好后经 SupportContext.DriverKind 传入**
// （而不是 adapter 自己类型断言）—— 这样分支选择逻辑集中在一处，
// 且 adapter 保持纯判断、可穷举单测。
type AdapterDriver struct {
	registry *drivers.Registry
}

// driverName 是 adapter 名（有界枚举，进指标标签）。
const driverName = "driver"

// NewAdapterDriver 构造驱动解析适配器。
//
// registry 为 nil 时 Supports 恒 false（安全降级：宁可报 no_adapter，
// 也不要 panic 在每帧路径上）。
func NewAdapterDriver(registry *drivers.Registry) *AdapterDriver {
	return &AdapterDriver{registry: registry}
}

// Name 实现 Adapter。
func (*AdapterDriver) Name() string { return driverName }

// Supports 实现 Adapter。
//
// 条件：驱动注册表里存在该设备 type 的驱动。
//
// 注意：**不排除标定/命令分支** —— 它们都是本 adapter 的职责范围，
// 只是 Decode 里走不同方法。这与 ConfigParser 的排除逻辑不矛盾：
// 配置解析器无法处理标定设备，而本 adapter 本来就负责标定。
func (a *AdapterDriver) Supports(ctx SupportContext) bool {
	return a != nil && a.registry != nil && ctx.DriverExists
}

// Decode 实现 Adapter。
//
// 三个分支的失败原因必须区分（ReasonCalibrationMiss / ReasonCommandCtxMiss）：
// 这两种失败是**配置缺失**，与帧格式错误（decode_failed）的处置完全不同 ——
// 前者要补标定/补模板，后者要查协议。
func (a *AdapterDriver) Decode(ctx DecodeContext) (Sample, error) {
	drv, err := a.registry.Get(ctx.EdgeDevice.Type)
	if err != nil {
		return Sample{}, NewDecodeError(driverName, ReasonDecodeFailed, err)
	}

	// 分支上下文**惰性**获取：只有标定/命令分支才触发对应查询，
	// 普通驱动分支零额外查询（与改造前行为一致）。
	var raw []drivers.SensorData
	switch kindOfDriver(drv) {
	case DriverKindCalibration:
		dctx := ctx.DriverCtx()
		if dctx == nil || len(dctx.Calibration) == 0 {
			return Sample{}, NewDecodeError(driverName, ReasonCalibrationMiss, nil)
		}
		cal, ok := drv.(drivers.CalibrationAwareDriver)
		if !ok {
			return Sample{}, NewDecodeError(driverName, ReasonDecodeFailed, nil)
		}
		raw, err = cal.ParseDataWithCalibration(ctx.Raw, dctx.Calibration)
	case DriverKindCommand:
		dctx := ctx.DriverCtx()
		if dctx == nil || dctx.CommandWriteData == "" {
			return Sample{}, NewDecodeError(driverName, ReasonCommandCtxMiss, nil)
		}
		cmd, ok := drv.(drivers.CommandAwareDriver)
		if !ok {
			return Sample{}, NewDecodeError(driverName, ReasonDecodeFailed, nil)
		}
		raw, err = cmd.ParseDataWithCommand(ctx.Raw, dctx.CommandWriteData)
	default:
		raw, err = drv.ParseData(ctx.Raw)
	}
	if err != nil {
		return Sample{}, NewDecodeError(driverName, ReasonDecodeFailed, err)
	}
	if len(raw) == 0 {
		return Sample{}, NewDecodeError(driverName, ReasonEmptyResult, nil)
	}

	// Method 字符串保持改造前格式：fmt.Sprintf("Driver(%s)", device.Type)
	return Sample{
		EdgeDeviceID:    ctx.EdgeDevice.ID,
		LogicalDeviceID: ctx.LogicalDeviceID,
		At:              ctx.At,
		Fields:          fromDriverData(raw),
		Method:          "Driver(" + ctx.EdgeDevice.Type + ")",
		Quality:         QualityOk,
	}, nil
}

// kindOfDriver 判断驱动走哪个解析分支。
//
// 顺序有语义：先判标定（标定优先于命令 —— 若一个驱动同时实现两者，
// 标定是更强的解码不变量），再判命令。
func kindOfDriver(drv drivers.Driver) DriverKind {
	if _, ok := drv.(drivers.CalibrationAwareDriver); ok {
		return DriverKindCalibration
	}
	if _, ok := drv.(drivers.CommandAwareDriver); ok {
		return DriverKindCommand
	}
	return DriverKindPlain
}

// fromDriverData 把 drivers.SensorData 转换为 ingest.Field（逐字段等价）。
func fromDriverData(in []drivers.SensorData) []Field {
	out := make([]Field, len(in))
	for i, d := range in {
		out[i] = Field{Name: d.Name, Value: d.Value, Unit: d.Unit, StringValue: d.StringValue}
	}
	return out
}

// ResolveDriverKind 供调用方在构造 SupportContext 前算出驱动分支。
//
// 为什么导出：分支判定必须与 Decode 内部使用的完全一致，否则会出现
// 「Supports 认为普通、Decode 走标定」的分裂。导出同一个函数是唯一可靠做法。
func ResolveDriverKind(registry *drivers.Registry, deviceType string) (kind DriverKind, exists bool) {
	if registry == nil {
		return DriverKindPlain, false
	}
	drv, err := registry.Get(deviceType)
	if err != nil || drv == nil {
		return DriverKindPlain, false
	}
	return kindOfDriver(drv), true
}
