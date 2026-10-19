package drivers

import (
	"strings"
	"testing"
)

// ============================================================================
// 回归测试：真机形状与解析器假设不符导致的数据污染（2026-10-19）
//
// 背景：ParseData 靠**形状**挑解析器，而命令上下文（command_template_id）
// 在真机上恒为 0（固件未透传），所以判别顺序的每一次偏差都会直接变成
// 写进库里的错误读数。下面两条都是实测发现的污染路径。
// ============================================================================

// TestTechfineHBMS1AcceptsSixteenBitFlags 锁住"HBMS1 标志串是 16 位"。
//
// 缺陷原样：判别闸门写死 len(fields[1]) == 8，而真机是 16 位 ⇒ 永不匹配 ⇒
// 落到 parseHBAT（len>=7 且首字段是整数）⇒ 把 "0001000000000000" 当电压
// ⇒ **每 3 分钟写一条 1e12 伏特**（实测污染 359 条，占该字段 20%）。
//
// 反向验证：把闸门改回 ==8，本用例立即变红。
func TestTechfineHBMS1AcceptsSixteenBitFlags(t *testing.T) {
	// 真机原始应答（F0F5BD02DD84 / ch11 / command_index=8）
	raw := []byte("(00 0001000000000000 000.0 000.0 002.0 000 0000.0 0000.0 00000 0000000\r")

	data, err := (&TechfineInverterDriver{}).ParseData(raw)
	if err != nil {
		t.Fatalf("HBMS1 应答应被识别并解析，却报错: %v", err)
	}

	// 必须产出 bms_* 字段，而不是 battery_voltage；
	// 后者出现即说明落到了 HBAT 分支。
	if s := findSensor(data, "battery_voltage"); s != nil {
		t.Fatalf("HBMS1 应答被误解析成 battery_voltage=%.1f V —— " +
			"说明判别闸门没匹配上，落到了 parseHBAT", s.Value)
	}
	found := false
	for _, s := range data {
		if strings.HasPrefix(s.Name, "bms_") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("HBMS1 应答应产出 bms_* 传感器，实际得到 %d 个字段", len(data))
	}
}

// TestTechfineHBATRejectsImplausibleVoltage 锁住"电压字段的物理闸门"。
//
// 即使判别顺序将来又被改错，也不能让一个形如 1e12 的值进库 ——
// 假数据比缺失数据更难排查：运维会去查电池，而真相是解析器认错了帧。
//
// 反向验证：删掉 parseHBAT 里的 hbatMaxPlausibleVoltage 判断，本用例变红。
func TestTechfineHBATRejectsImplausibleVoltage(t *testing.T) {
	// 这正是被误判的 HBMS1 应答：fields[1] 是 16 位二进制串。
	raw := []byte("(00 0001000000000000 000.0 000.0 002.0 000 0000.0 0000.0 00000 0000000\r")

	d := &TechfineInverterDriver{}
	fields := []string{"00", "0001000000000000", "000.0", "000.0", "002.0", "000", "0000.0", "0000.0", "00000", "0000000"}
	data, err := d.parseHBAT(fields)
	if err == nil {
		t.Fatalf("HBAT 收到 %q 必须拒绝（那是 HBMS1 的形状），实际返回 %d 个字段", fields[1], len(data))
	}
	if !strings.Contains(err.Error(), "implausible") {
		t.Fatalf("错误信息应指出电压不合理，实际: %v", err)
	}
	_ = raw // 保留原始帧便于对照
}

// TestTechfineHBATAcceptsRealVoltage 防止闸门过严把正常数据也拒掉。
//
// 真机正常帧：battery_voltage = 51.1 V。若闸门写错（比如界限取成 50），
// 会把一半的真数据拒掉 —— 这类"修复引入的新故障"必须由用例挡住。
func TestTechfineHBATAcceptsRealVoltage(t *testing.T) {
	fields := []string{"04", "051.1", "069", "000", "00000", "379", "110007200000000000000"}
	data, err := (&TechfineInverterDriver{}).parseHBAT(fields)
	if err != nil {
		t.Fatalf("真机正常 HBAT 应答不应被拒: %v", err)
	}
	volt := findSensor(data, "battery_voltage")
	if volt == nil || volt.Value < 50 || volt.Value > 52 {
		t.Fatalf("battery_voltage 应为 51.1 V，实际 %+v", volt)
	}
	cap := findSensor(data, "battery_capacity")
	if cap == nil || cap.Value != 69 {
		t.Fatalf("battery_capacity 应为 69%%，实际 %+v", cap)
	}
}

// TestTechfineHTEMPRejectsSixFieldFrame 锁住"6 字段既不是真机 8 字段形状、
// 也不是旧机型 11 字段形状 ⇒ 必须拒绝"。
//
// ## 这个用例的来历与一次误判（2026-10-19 更正）
//
// 最初我以为这 6 字段帧就是真机的 HTEMP 应答，据此写下"温度帧形状不符、
// 待厂商文档确认"，并保留了拒绝逻辑。**那个前提是错的**：
//
//	(000.0 00.0 00000 0 380.0 00000000000000000000000
//
// 这是 command_index=5 的应答，而 command_index 按**驱动声明顺序**
// （HSTS/HGRID/HOP/HBAT/HPV/HPVB/HTEMP/HGEN/HBMS1）排 ⇒ 索引 5 是 **HPVB**
// （PV2），不是 HTEMP。PV2 未接线，所以电流/功率为 0、电压字段是残留值。
// 真正的 HTEMP 在索引 6，形状是 8 字段（见 inverter_htemp_real_test.go）。
//
// 用例本身仍然**有效且必要**：6 字段确实是畸形帧（既非 8 也非 >=11），
// 拒绝是对的 —— 只是理由要写对。留着它防的是"将来有人放宽门槛到 >=6"。
func TestTechfineHTEMPRejectsSixFieldFrame(t *testing.T) {
	// PV2 未接线时的应答（曾被误认为是温度帧）
	fields := []string{"000.0", "00.0", "00000", "0", "380.0", "00000000000000000000000"}
	data, err := (&TechfineInverterDriver{}).parseHTEMP(fields)
	if err == nil {
		t.Fatalf("6 字段帧必须拒绝（既非真机 8 字段形状，也非旧机型 11 字段形状），"+
			"实际返回 %d 个字段: %v", len(data), data)
	}
	// 错误信息要能让人一眼看出"该给几种形状"，而不是只说"格式错误"。
	if !strings.Contains(err.Error(), "8 or >=11") {
		t.Logf("提示：错误信息宜写明可接受的字段数范围，当前: %v", err)
	}
}
