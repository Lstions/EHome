package telemetry

import (
	"time"
)

// 本文件是给**写路径接线方**（internal/databus）的最小注入面。
//
// ============ 为什么需要这一层而不是让 databus 直接持有 *DualWriter ============
//
// 数据面零依赖红线：internal/databus 不得 import internal/telemetry 的实现
// 细节（更不能 import gorm 模型）。它只需要一个 func 值 —— 本文件的类型
// 就是那个 func 值的形状。
//
// ============ 为什么读数是 []Reading 而不是 []parser.Field / []ingest.Field ============
//
// telemetry 不得 import pkg/parser 或 internal/ingest（那是接入层/公开契约）。
// 接线方做一次逐字段转换（Name/Value 两个字段），成本可忽略（29 个元素）。

// Sink 是写路径旁调用的一次观测。
//
// 参数语义（与 databus.SensorParserConsumer.Handle 里的局部变量一一对应）：
//
//	deviceID          = device.ID                     // EdgeDevice.ID = unified_data.device_id 口径
//	at                = now                           // **与 unified_data.timestamp 同源的那个 now**
//	commandTemplateID = int64(evt.CommandTemplateID)  // device15 的 7 种帧类型唯一可靠判据（§4.4）
//	readings          = 解析出的物理量                 // ingest.Sample.Fields → Reading
//
// 为什么 commandTemplateID 不能省：方案 §4.4 的 ⚠ —— device15 的 7 种帧类型
// 来自 7 条不同指令，硬编码/省略它会把不同指令的值写进同一 slot（写串）。
//
// ==================== 为什么加 raw（2026-11-11，原始数据退役 §4.4 方案 A）====================
//
// device_data 退役后，「**解析成功但数值错误**」（量化规格配错/寄存器偏移错）这类
// 问题会彻底失去追溯能力：库里只剩解析值，没有原始字节可比对。缓解方案 A 是
// 保留 telemetry_frame.raw 列，**只在关键帧填充**：
//
//	· 关键帧实测 10,270 帧/24h（7.1%），原始帧 78~256 B ⇒ 约 +0.13 MB/天；
//	· 比「关键帧 raw 打进日志」省 10 倍（hex 会膨胀），且**可 SQL 按时间/设备查**；
//	· 增量帧的 raw 留 NULL：它们只有「与上一帧的差」，脱离增量链没有独立追溯价值，
//	  而「回放某一时刻」只需要最近的那个关键帧（关键帧自带全字段）。
//
// 注意 raw 与 readings 必须来自**同一次重组结果**（databus.Handle 里的 merged）：
// 拿单帧片段去配关键帧的解析值，事后核对会得出错误结论。
type Sink func(deviceID uint, at time.Time, commandTemplateID int64, readings []Reading, raw []byte)

// SinkOrNil 把 *DualWriter 变成可安全注入的 Sink（w 为 nil 时返回 nil）。
//
// 接线方的用法（默认关闭 ⇒ 得到 nil ⇒ 零成本）：
//
//	if s := telemetry.SinkOrNil(dual); s != nil { consumer.SetTelemetrySink(s) }
//
// 或直接 consumer.SetTelemetrySink(telemetry.SinkOrNil(dual))（setter 接受 nil）。
func SinkOrNil(w *DualWriter) Sink {
	if w == nil {
		return nil
	}
	return w.Observe
}

// ReadingsFrom 把任意"名字+值"的并行切片组装成 []Reading（接线便捷函数）。
//
// 存在的意义：databus 侧的解析产物是 []parser.Field / []ingest.Field，
// 本包不能 import 它们。用两个切片让调用方写一行循环即可，避免为每个来源
// 类型各写一份转换（那会漏掉新增来源）。
func ReadingsFrom(names []string, values []float64) []Reading {
	n := len(names)
	if len(values) < n {
		n = len(values)
	}
	out := make([]Reading, n)
	for i := 0; i < n; i++ {
		out[i] = Reading{Name: names[i], Value: values[i]}
	}
	return out
}
