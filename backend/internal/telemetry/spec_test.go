package telemetry

import (
	"math"
	"testing"
)

// TestSpecEnergyPrecisionMatchesRealData 钉住 §4.3 的**唯一修正**：
//
//	daily_energy  实测最小差分 0.001 kWh ⇒ 步长必须是 0.001（19 位）
//	其余 *_energy 实测最小差分 0.1 kWh  ⇒ 步长 0.1（24 位）
//
// 为什么必须钉住：这两组数字都是**实测结论**，不是拍脑袋。改回统一 0.1 会让
// daily_energy 出现 0.05 kWh 的台阶（等于把"每日发电量"从 3 位有效数字砍到 2 位），
// 而改成统一 0.001 会让月/年电量多占 5 位/帧却毫无精度收益。
func TestSpecEnergyPrecisionMatchesRealData(t *testing.T) {
	cases := []struct {
		name string
		step float64
		bits uint8
	}{
		{"daily_energy", 0.001, 19},
		{"monthly_energy", 0.1, 24},
		{"total_energy", 0.1, 24},
		{"yearly_energy", 0.1, 24},
	}
	for _, c := range cases {
		sp, known := SpecFor(c.name)
		if !known {
			t.Fatalf("%s 未显式登记（会落到 pattern 表的 0.1 kWh 规则上）", c.name)
		}
		if math.Abs(sp.Step-c.step) > 1e-12 {
			t.Errorf("%s 步长 = %v, want %v（实测最小差分）", c.name, sp.Step, c.step)
		}
		if sp.Bits != c.bits {
			t.Errorf("%s 位宽 = %d, want %d", c.name, sp.Bits, c.bits)
		}
		if !sp.Valid() {
			t.Errorf("%s 规格非法: %+v", c.name, sp)
		}
	}
	// daily_energy 必须能无损表达实测值域（0–11.004 kWh，留足余量到 524 kWh）
	sp, _ := SpecFor("daily_energy")
	if sp.Max() < 100 {
		t.Errorf("daily_energy 量程上界 = %v，对户用日发电量余量不足", sp.Max())
	}
	// 且必须真的无损：0.001 步长的最小差分能精确还原
	for _, v := range []float64{0, 0.001, 1.55, 11.004} {
		code, clamped := sp.Quantize(v)
		if clamped {
			t.Fatalf("%v 被判定越界（量程上界 %v）", v, sp.Max())
		}
		if got := sp.Dequantize(code); math.Abs(got-v) > 1e-9 {
			t.Errorf("%v 往返得到 %v（daily_energy 必须无损）", v, got)
		}
	}
}

// TestSpecTableExcludesPatternFamilies 钉住 SpecTable 只导出显式登记项。
//
// 为什么要这条：SpecTable 是给文档/接线方看的"精度真值清单"。若它把 pattern
// 族也展开进来，清单会长出一堆带通配符的伪字段名（cell_voltage_1..16 谁展开？）。
func TestSpecTableExcludesPatternFamilies(t *testing.T) {
	rows := SpecTable()
	if len(rows) < 20 {
		t.Fatalf("SpecTable 只有 %d 行（判据被改坏了？）", len(rows))
	}
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		if seen[r.Name] {
			t.Errorf("重复行: %s", r.Name)
		}
		seen[r.Name] = true
		if r.Bits < 1 || r.Bits > 62 {
			t.Errorf("%s 位宽异常: %d", r.Name, r.Bits)
		}
		if r.Max <= r.Min {
			t.Errorf("%s 量程异常: min=%v max=%v", r.Name, r.Min, r.Max)
		}
		if math.Abs(r.MaxError-r.Step/2) > 1e-12 {
			t.Errorf("%s MaxError 与 Step/2 不一致", r.Name)
		}
		// 通配符/族不应出现在显式表里
		if r.Name == "" || r.Name[0] == '*' {
			t.Errorf("SpecTable 出现了族名 %q（应只含显式字段）", r.Name)
		}
	}
}

// TestCheckSpecAgainstMeta 钉住精度同源校验的判据。
func TestCheckSpecAgainstMeta(t *testing.T) {
	p3 := 3
	p1 := 1
	p15 := 15
	metas := []MetaPrecision{
		{SensorName: "cell_voltage_1", Precision: &p3}, // spec 0.001 ⇒ 一致
		{SensorName: "rsoc", Precision: &p3},           // spec 1 ⇒ 漂移（展示 3 位小数是假位）
		{SensorName: "daily_energy", Precision: &p3},   // spec 0.001 ⇒ 一致（修正后）
		{SensorName: "total_energy", Precision: &p3},   // spec 0.1 ⇒ 漂移
		{SensorName: "temperature_1", Precision: &p1},  // spec 0.01 ⇒ 一致
		{SensorName: "temperature_2", Precision: &p15}, // 元数据异常（原始 float64 噪声派生）⇒ 一致，不报
		{SensorName: "no_such_field", Precision: &p3},  // 未登记 ⇒ 不参与
		{SensorName: "cell_voltage_2"},                 // precision nil ⇒ 不参与
	}
	drifts := CheckSpecAgainstMeta(metas)
	got := make(map[string]bool, len(drifts))
	for _, d := range drifts {
		got[d.SensorName] = true
	}
	if !got["rsoc"] {
		t.Error("rsoc: 步长 1 而展示 3 位小数，必须报漂移")
	}
	if !got["total_energy"] {
		t.Error("total_energy: 步长 0.1 而展示 3 位小数，必须报漂移")
	}
	if got["cell_voltage_1"] || got["daily_energy"] || got["temperature_1"] {
		t.Errorf("误报漂移: %v（量化比展示更细时不该报）", drifts)
	}
	if got["no_such_field"] {
		t.Error("未登记字段不该报漂移（那是 UnknownFields 指标的职责）")
	}
}
