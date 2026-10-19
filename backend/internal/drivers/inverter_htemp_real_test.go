package drivers

import (
	"strings"
	"testing"
)

// ============================================================================
// HTEMP 真机形状（2026-10-19 实测 40 帧定稿）
//
// 之前一直解析失败，根因是**两个误判叠加**：
//   ① 我把 command_index=5 的应答当成 HTEMP，实际那是 HPVB
//      （索引按**驱动声明顺序**：HSTS/HGRID/HOP/HBAT/HPV/HPVB/HTEMP/HGEN/HBMS1，
//       不是按 channel.template_ids 的数字顺序）；
//   ② 真机只给 8 字段（7 温度 + 1 状态位串），而解析器按 11 字段写的。
// ============================================================================

// 真机实测帧（40 帧中字段数恒为 8，温度位恒定或有 1°C 抖动）。
const realHTEMPFrame = "(027 037 031 033 037 030 030 11000000000000000000\r"

// TestTechfineHTEMPRealFrameIsParsed 锁住"真机 8 字段形状必须能解析"。
//
// 反向验证：把 parseHTEMP 的 8 字段分支去掉，本用例立即变红
// （会走 >=11 的分支并报 need 8 or >=11）。
func TestTechfineHTEMPRealFrameIsParsed(t *testing.T) {
	data, err := (&TechfineInverterDriver{}).ParseDataWithCommand(
		[]byte(realHTEMPFrame), "4854454d500d") // "HTEMP\r"
	if err != nil {
		t.Fatalf("真机 HTEMP 帧必须能解析，实际报错: %v", err)
	}

	// 7 个温度值必须全部落在物理可行区间（实测 27~38°C）。
	temps := []string{"pv_temp", "inverter_temp", "boost_temp", "transformer_temp",
		"max_temp", "pv2_temp", "dc_rectifier_temp"}
	for _, name := range temps {
		s := findSensor(data, name);
		if s == nil {
			t.Fatalf("缺少温度字段 %s，实际字段: %v", name, sensorNames(data))
		}
		if s.Value < -40 || s.Value > 150 {
			t.Fatalf("%s = %.1f°C 超出物理可行区间", name, s.Value)
		}
	}

	// 位串 "11..." 的前两位 = 两个风扇都在转。
	f1 := findSensor(data, "fan1_status")
	f2 := findSensor(data, "fan2_status")
	if f1 == nil || f2 == nil {
		t.Fatalf("应从状态位串解析出 fan1_status/fan2_status，实际: %v", sensorNames(data))
	}
	if f1.Value != 1 || f2.Value != 1 {
		t.Fatalf("位串以 \"11\" 开头，两个风扇状态都应为 1，实际 f1=%.0f f2=%.0f", f1.Value, f2.Value)
	}
}

// TestTechfineHTEMPRejectsWrongFieldCount 锁住"既不是 8 也不是 >=11 必须拒绝"。
//
// 这是 fail-closed 的边界：任何"差不多能用"的宽松都会让畸形帧产出
// 看起来合理的温度（例如把电池帧的 51.5V 当温度 51.5°C）。
func TestTechfineHTEMPRejectsWrongFieldCount(t *testing.T) {
	bad := []struct {
		frame, why string
	}{
		{"(027 037 031 033\r", "4 字段（被截断）"},
		{"(027 037 031 033 037 030\r", "6 字段（截断，正是我最初误判的那个）"},
	}
	for _, c := range bad {
		fields := strings.Fields(strings.Trim(strings.TrimSuffix(c.frame, "\r"), "( "))
		data, err := (&TechfineInverterDriver{}).parseHTEMP(fields)
		if err == nil {
			t.Errorf("%s 必须被拒绝，却返回 %d 个字段: %v", c.why, len(data), sensorNames(data))
		}
	}
}

// TestTechfineHTEMPRejectsImplausibleTemperature 锁住温度物理闸门。
//
// 与 parseHBAT 的电压闸门同一条理由：宁可丢这一帧，也不写一个看起来
// 像真读数的垃圾值 —— 后者会让人去查散热，而真相是解析错位。
//
// 反向验证：删掉 parseHTEMPCompact 里的 [-40,150] 检查，本用例变红。
func TestTechfineHTEMPRejectsImplausibleTemperature(t *testing.T) {
	// 把电压值塞进温度位：51.5°C 尚在区间内，但 515°C 不可能
	fields := []string{"515", "037", "031", "033", "037", "030", "030", "11000000000000000000"}
	data, err := (&TechfineInverterDriver{}).parseHTEMP(fields)
	if err == nil {
		t.Fatalf("515°C 必须被拒绝，却返回: %v", sensorNames(data))
	}
	if !strings.Contains(err.Error(), "implausible") {
		t.Fatalf("错误信息应指出温度不合理，实际: %v", err)
	}
}

// TestTechfineHTEMPStillSupportsLegacyElevenFieldShape 锁住旧机型兼容。
//
// 11 字段形状（7 温度 + 2 风扇转速 + 2 风扇状态 + 2 额外温度）来自早期机型/
// 固件。改成 8 字段解析时**不能**把这条路径弄坏 —— 否则老设备会突然没数据。
func TestTechfineHTEMPStillSupportsLegacyElevenFieldShape(t *testing.T) {
	fields := []string{"035", "045", "040", "050", "050", "060", "050", "01", "01", "038", "042"}
	data, err := (&TechfineInverterDriver{}).parseHTEMP(fields)
	if err != nil {
		t.Fatalf("旧的 11 字段形状必须继续支持: %v", err)
	}
	// 旧形状有风扇转速（真机 8 字段形状没有）。
	if findSensor(data, "fan1_speed") == nil {
		t.Fatalf("11 字段形状应产出 fan1_speed，实际: %v", sensorNames(data))
	}
}

// sensorNames 提取字段名列表，用于失败信息。
func sensorNames(d []SensorData) []string {
	out := make([]string, len(d))
	for i := range d {
		out[i] = d[i].Name
	}
	return out
}
