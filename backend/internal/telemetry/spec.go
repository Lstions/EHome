package telemetry

import (
	"math"
	"regexp"
)

// SpecVersion 是当前量化规格表的版本号。
//
// # 为什么规格必须版本化（方案 §4.3 尾注）
//
// 量化是**有损**的：同一串码点在不同 (量程, 步长) 下解出不同的物理量。
// 一旦调整某个字段的步长（例如电芯电压 1 mV → 0.5 mV），旧帧里已落库的码点
// 就会按新规格解错 —— 这不是"精度略降"，是**整段历史数据静默变成垃圾**。
// 因此 slot 描述里固化 spec_version + 当时每字段的 (min, step, bits) 快照，
// 解码按快照走，与当前 spec.go 的取值解耦。
//
// 增加/修改字段规格时必须同时 +1：v2 的帧只能出现在 v2 的 slot 里，
// v1 的 slot 永远按 v1 快照解。
const SpecVersion uint16 = 1

// FieldSpec 是一个字段的量化规格：(量程下界, 步长, 位宽)。
//
// 量化规则（与方案 §4.3 一一对应）：
//
//	code  = round((v - Min) / Step)      ∈ [0, 2^Bits-1]
//	value = Min + code * Step
//
// 最大绝对误差 = Step/2（四舍五入的一半），这正是验收标准 §9.1 的阈值。
type FieldSpec struct {
	// Min 是量程下界（物理量单位，与 unified_data.value 同单位）。
	Min float64
	// Step 是量化步长，必须 > 0。
	Step float64
	// Bits 是位宽（1..62；上限由 bit packing 的 uint64 累加器决定）。
	Bits uint8
}

// Valid 报告规格是否可用。
func (s FieldSpec) Valid() bool {
	return s.Step > 0 && s.Bits >= 1 && s.Bits <= 62 && !math.IsNaN(s.Min) && !math.IsInf(s.Min, 0)
}

// MaxCode 是位宽能表达的最大码点。
func (s FieldSpec) MaxCode() uint64 {
	return (uint64(1) << s.Bits) - 1
}

// Max 是量程上界（码点满量程对应的物理量）。
func (s FieldSpec) Max() float64 {
	return s.Min + float64(s.MaxCode())*s.Step
}

// MaxError 是量化引入的最大绝对误差（步长的一半）。
func (s FieldSpec) MaxError() float64 { return s.Step / 2 }

// Quantize 把物理量量化成码点。
//
// 返回值 clamped 表示该值超出量程、已被夹到边界 —— 这是**真实精度损失**
// （误差 > Step/2），编码器必须计数并在验收中要求为 0；工程上宁可少一位精度
// 也不能让它静默发生（§8 风险表「量化有损」的对策是"实测 0 超差"）。
// NaN 由调用方按"本帧缺失"处理，不进这里（NaN 会被 Round 传播成实现相关的码点）。
func (s FieldSpec) Quantize(v float64) (code uint64, clamped bool) {
	scaled := math.Round((v - s.Min) / s.Step)
	if scaled < 0 {
		return 0, true
	}
	if scaled > float64(s.MaxCode()) {
		return s.MaxCode(), true
	}
	return uint64(scaled), false
}

// Dequantize 把码点还原成物理量。
func (s FieldSpec) Dequantize(code uint64) float64 {
	if code > s.MaxCode() {
		code = s.MaxCode()
	}
	return s.Min + float64(code)*s.Step
}

func spec(min, step float64, bits uint8) FieldSpec {
	return FieldSpec{Min: min, Step: step, Bits: bits}
}

// FallbackSpec 是未登记字段的兜底规格。
//
// 为什么需要兜底而不是拒绝：写路径上出现新字段（固件加了一个量、或解析模板
// 新增一条）是**正常演进**，双写旁路不能因此把整帧丢掉（那等于静默丢数据）。
// 但兜底规格是**猜的**，精度不保证，所以编码器把它计入 UnknownFields 指标，
// 验收要求真实数据上 UnknownFields == 0。
//
// 量程 ±1000 覆盖绝大多数物理量（电压/电流/功率/温度/容量），步长 1e-6 是
// 微秒级工程量的常见最小分辨率，31 位 = 2.1e9 个码点。
var FallbackSpec = FieldSpec{Min: -1000, Step: 0.000001, Bits: 31}

// exactSpecs 是**精确字段名 → 规格**表，直接来自方案 §4.3 的实测精度表。
//
// ⚠ 与 sensor_field_meta 同源维护（方案 §4.3 尾注）：本表是"精度"这一维的
// 权威来源，sensor_field_meta 是"展示元数据"（中文名/单位/语义类型/小数位）。
// 两者按**物理名**对齐 —— 新增字段时先在本表登记 (量程, 步长, 位宽)，
// 再在 sensor_field_meta 登记展示信息；MetaSpecOverlay 提供反向校验入口
// （见 meta_bridge.go：用 meta.precision 反推"应该用多细的步长"，
// 与本表不一致时打点告警，避免两处各自漂移）。
var exactSpecs = map[string]FieldSpec{
	// ── BMS（device14：29 字段固定帧）────────────────────────────────
	"current":            spec(-200, 0.01, 16),
	"total_voltage":      spec(0, 0.01, 15),
	"remaining_capacity": spec(0, 0.01, 15),
	"nominal_capacity":   spec(0, 0.1, 13),
	"rsoc":               spec(0, 1, 7),
	"cycle_count":        spec(0, 1, 16),
	"fet_status":         spec(0, 1, 16),
	"protection_status":  spec(0, 1, 16),

	// ── 逆变器（device15：7 种帧类型）─────────────────────────────────
	"grid_voltage":    spec(0, 0.1, 13),
	"output_voltage":  spec(0, 0.1, 13),
	"bus_voltage":     spec(0, 0.1, 14),
	"battery_voltage": spec(0, 0.1, 11),
	"fault_code":      spec(0, 1, 16),
	// work_mode 实测 2/3（待机/并网），8 位留足枚举扩展。
	"work_mode": spec(0, 1, 8),
	// output_load_percent 实测 3–36(%)。
	"output_load_percent": spec(0, 1, 7),
	// 逆变器功率量实测 154–2267 W / 222–2267 VA。
	"output_active_power":   spec(-10000, 1, 15),
	"output_apparent_power": spec(0, 1, 14),
	"pv1_power":             spec(0, 1, 14),
	"pv1_voltage":           spec(0, 0.1, 13),
	"pv1_current":           spec(0, 0.01, 14),
	// 电池充放电电流实测 0–54 A，0.01 A 步长给到 327 A 上限。
	"battery_charge_current":    spec(0, 0.01, 15),
	"battery_discharge_current": spec(0, 0.01, 15),
	// battery_capacity 是 SoC 百分比（实测 38–100），**不是** Ah 容量；
	// 与 BMS 的 remaining_capacity（Ah）是两个量，别合并。
	"battery_capacity": spec(0, 1, 7),

	// ── 对方案 §4.3 的**唯一修正**（有实测依据，见下）─────────────────
	//
	// 方案 §4.3 把 *_energy 统一标为「0–1e6 kWh / 0.1 kWh / 24 位」。
	// 但生产实测（2026-11-04 全天 device15）：
	//
	//	daily_energy    0–11.004 kWh，值间最小差 **0.001 kWh**
	//	monthly_energy  0.2–35.6 kWh，最小差 0.1 kWh
	//	total_energy    4737.7–4773.1 kWh，最小差 0.1 kWh
	//	yearly_energy   2336.1–2371.5 kWh，最小差 0.1 kWh
	//
	// 即**只有 daily_energy 的真实分辨率比 0.1 细一个数量级**。按 0.1 量化
	// 会把 1.55 读成 1.6（误差 0.05 = 该字段真值的 3%），在"每日发电量"这种
	// 用户直接看的数字上是可见的台阶。
	//
	// 修正为 0–524.287 kWh / 0.001 kWh / **19 位**，同时满足两个条件：
	//   · 无损：与真实分辨率 0.001 逐位对齐（0.5 mWh 半步误差）；
	//   · 更省：19 位 < 方案给的 24 位，该字段每帧少 5 位。
	// 月/年/总电量保持方案的 0.1 kWh/24 位 —— 它们的真实分辨率就是 0.1，
	// 再细化只是浪费位宽（且总量可能到 1e6 量级，需要 24 位量程）。
	"daily_energy": spec(0, 0.001, 19),
}

// patternSpecs 是**字段族 → 规格**表（按物理名后缀/前缀归族）。
//
// 顺序有意义：更具体的族必须排在更宽泛的族之前。
// 当前各族互不重叠，但新增族时务必按"具体→宽泛"插入。
var patternSpecs = []struct {
	name string
	re   *regexp.Regexp
	spec FieldSpec
}{
	// cell_voltage_1..16 / cell_voltage_max / cell_voltage_min：2.0–4.5 V，1 mV。
	// 12 位满量程 = 2.0 + 4095×0.001 = 6.095 V，覆盖 4.5 V 上限且留过压余量。
	{"cell_voltage", regexp.MustCompile(`^cell_voltage(_[0-9]+|_max|_min)$`), spec(2.0, 0.001, 12)},
	// temperature_1..N：探头温度 −40–125 ℃，0.01 ℃（BMS 实测 25.45 这类两位小数）。
	{"temperature", regexp.MustCompile(`^temperature_[0-9]+$`), spec(-40, 0.01, 15)},
	// *_temp：逆变器整度温度（boost/inverter/max/pv/transformer），−40–125 ℃，0.1 ℃。
	{"*_temp", regexp.MustCompile(`_temp$`), spec(-40, 0.1, 11)},
	// alarm_*：布尔告警位，1 位。
	{"alarm_*", regexp.MustCompile(`^alarm_`), spec(0, 1, 1)},
	// fan1_status / fan2_status：布尔状态，1 位。
	{"fan*_status", regexp.MustCompile(`^fan[0-9]+_status$`), spec(0, 1, 1)},
	// fan1_speed / fan2_speed：实测 30/50/100（%），7 位。
	{"fan*_speed", regexp.MustCompile(`^fan[0-9]+_speed$`), spec(0, 1, 7)},
	// *_frequency：40–70 Hz，0.01 Hz（电网/输出频率实测 49.6–50.3）。
	{"*_frequency", regexp.MustCompile(`_frequency$`), spec(40, 0.01, 13)},
	// *_energy：累计电量 0–1e6 kWh，0.1 kWh（实测 4737.7 kWh）。
	// ⚠ daily_energy 是例外（真实分辨率 0.001 kWh），见 exactSpecs 里的单独登记 ——
	// exactSpecs 优先于本表，因此它不会落到这条规则上。
	{"*_energy", regexp.MustCompile(`_energy$`), spec(0, 0.1, 24)},
}

// SpecFor 返回字段的量化规格；known=false 表示未登记（用的是 FallbackSpec）。
//
// 这是**精度真值的唯一入口**：codec / slot / encoder 一律经它取规格，
// 不允许在别处硬编码 (min, step, bits)。
func SpecFor(name string) (FieldSpec, bool) {
	if s, ok := exactSpecs[name]; ok {
		return s, true
	}
	for _, p := range patternSpecs {
		if p.re.MatchString(name) {
			return p.spec, true
		}
	}
	return FallbackSpec, false
}

// SpecForVersion 按规格版本取字段规格。
//
// 当前只有 v1（SpecVersion）。保留版本参数是为了让解码器在读到 v2 slot 时
// **明确失败**（而不是拿 v1 规格硬解出一堆错的物理量）。
func SpecForVersion(version uint16, name string) (FieldSpec, bool, error) {
	if version != SpecVersion {
		return FieldSpec{}, false, &UnknownSpecVersionError{Version: version}
	}
	s, known := SpecFor(name)
	return s, known, nil
}
