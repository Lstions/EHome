package telemetry

import (
	"fmt"
	"math"
	"sort"
)

// 量化规格与 sensor_field_meta（展示元数据）的**同源校验**桥。
//
// ============ 为什么要这个桥 ============
//
// 方案 §4.3 尾注要求「每字段的 (量程, 步长, 位宽) 与 sensor_field_meta 同源维护」。
// 但两张表管的是不同的东西：
//
//	spec.go            → 精度真值（量化步长、位宽、量程）
//	sensor_field_meta  → 展示元数据（中文名、单位、语义类型、**展示小数位**）
//
// 它们唯一的交叉事实是"这个字段需要几位小数"，而 `precision` 正是
// 用户/驱动声明的小数位。若 spec 的步长比 precision 粗（例如 precision=3
// 但步长 0.1），那么"展示 3 位小数"就是骗人的 —— 后两位永远是 0。
//
// ============ 为什么不在本包 import sensorfield / models ============
//
// 数据面零依赖红线：internal/telemetry 只允许依赖 models（GORM 模型）与标准库。
// 因此这里只接受**调用方投影过的窄结构**（MetaPrecision），由接线方
// （cmd/server 或测试）把 sensor_field_meta 的行投影成它。本包不需要知道
// 那张表长什么样，更不需要 import 它的包。

// MetaPrecision 是 sensor_field_meta 里参与精度校验的一列投影。
type MetaPrecision struct {
	// SensorName 物理名字（= unified_data.sensor_name = 本包 Reading.Name）。
	SensorName string
	// Precision 展示小数位；nil = 未设置（未设置不参与校验，见 models.SensorFieldMeta.Precision
	// 的注释：NULL 与 0 有区别）。
	Precision *int
}

// SpecDrift 是一条「spec 与 meta 不一致」的证据。
type SpecDrift struct {
	// SensorName 字段名。
	SensorName string
	// Precision 声明的小数位（nil 表示未声明，此时 Drift 不会被产出）。
	Precision int
	// Step 本包使用的量化步长。
	Step float64
	// ImpliedStep 是"要支持 Precision 位小数"所需的最大步长（= 10^-Precision）。
	ImpliedStep float64
	// Reason 人类可读的原因。
	Reason string
}

func (d SpecDrift) String() string {
	return fmt.Sprintf("%s: precision=%d ⇒ 需要步长 <= %.0e，实际 %.0e（%s）",
		d.SensorName, d.Precision, d.ImpliedStep, d.Step, d.Reason)
}

// CheckSpecAgainstMeta 对比量化规格与展示元数据，返回漂移清单（空 = 一致）。
//
// 判据（只有一条，但要正确理解它）：
//
//	步长 <= 10^-precision   ⇒ 一致（量化比展示更细，展示不丢信息）
//	步长 >  10^-precision   ⇒ 漂移（展示的小数位里有假位）
//
// 只对**已登记在 spec.go 的字段**报漂移：走 FallbackSpec 的字段精度是猜的，
// 报出来只会淹没真问题（它们由 SlotDictionary 的 UnknownFields 指标负责）。
//
// ⚠ 已知的**故意宽容**：某些字段的展示位比量化步长细是合理的 —— 例如
// temperature_1 的原始值是 25.450000000000045（float64 表示误差），
// sensor_field_meta 若按原始值派生出 precision=15，并不代表"用户需要 15 位"。
// 因此本函数只用于**人工巡检与告警**，不做 fail-closed 拒绝启动：
// 拒绝启动会让一次元数据误填把整个服务停掉，代价远大于收益。
func CheckSpecAgainstMeta(metas []MetaPrecision) []SpecDrift {
	out := make([]SpecDrift, 0, 4)
	for _, m := range metas {
		if m.Precision == nil {
			continue
		}
		if m.SensorName == "" {
			continue
		}
		sp, known := SpecFor(m.SensorName)
		if !known {
			continue // 未登记字段的精度的兜底问题由 UnknownFields 指标暴露
		}
		p := *m.Precision
		if p < 0 || p > 15 {
			// 越界的展示位不是本函数能判断的（多半是脏元数据），跳过。
			continue
		}
		implied := math.Pow(10, -float64(p))
		if sp.Step > implied*(1+1e-9) {
			out = append(out, SpecDrift{
				SensorName:  m.SensorName,
				Precision:   p,
				Step:        sp.Step,
				ImpliedStep: implied,
				Reason:      "展示位比量化精度更细：展示出来的低位是假位",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SensorName < out[j].SensorName })
	return out
}

// SpecTable 是当前量化规格的可序列化视图（接线方/文档生成用）。
//
// 为什么不直接导出 exactSpecs：map 是可变的包级状态，导出会让任何调用方
// 悄悄改掉精度真值（那是"整段历史数据变成垃圾"级别的后果）。只导出副本。
type SpecTableRow struct {
	Name string
	Min  float64
	Step float64
	Bits uint8
	// Max 是量程上界（Min + (2^Bits-1)*Step）。
	Max float64
	// MaxError 是量化引入的最大绝对误差（Step/2）。
	MaxError float64
}

// SpecTable 返回**已显式登记**的字段规格表（不含 pattern 族与 Fallback）。
//
// pattern 族不进这张表：它们是按名字前缀/后缀匹配的规则，展开成字段清单
// 需要知道设备的完整字段集合，那属于调用方（且各设备型号不同）。
func SpecTable() []SpecTableRow {
	out := make([]SpecTableRow, 0, len(exactSpecs))
	for name, sp := range exactSpecs {
		out = append(out, SpecTableRow{
			Name: name, Min: sp.Min, Step: sp.Step, Bits: sp.Bits,
			Max: sp.Max(), MaxError: sp.MaxError(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
