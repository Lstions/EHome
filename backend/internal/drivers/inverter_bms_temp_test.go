package drivers

import "testing"

// ============================================================================
// HBMS1 温度哨兵（2026-10-19 真机发现）
//
// 缺陷：BMS 未接入时 HHHHH 字段为 "00000"，代码直接 /100-273.15 换算，
// 得到 **-273.15°C（绝对零度）** 并写进库。这不是"温度偏低"，是"没有读数"。
// 绝对零度会让温度曲线的 Y 轴被拉平，且数值本身物理上不可能。
// ============================================================================

// realHBMSS1NoBms 是真机帧：BMS 未接（末字段全 0）。
const realHBMSS1NoBms = "(00 0001000000000000 000.0 000.0 002.0 000 0000.0 0000.0 00000 000000\r"

// TestTechfineHBMS1OmitsTempWhenBmsAbsent 锁住"0 是哨兵不是绝对零度"。
//
// 反向验证：把 hasBmsTemp 改成恒 true，本用例变红（会产出 -273.15）。
func TestTechfineHBMS1OmitsTempWhenBmsAbsent(t *testing.T) {
	data, err := (&TechfineInverterDriver{}).ParseDataWithCommand(
		[]byte(realHBMSS1NoBms), "48424d53310d") // "HBMS1\r"
	if err != nil {
		t.Fatalf("真机 HBMS1 帧必须能解析: %v", err)
	}

	if s := findSensor(data, "bms_temp"); s != nil {
		t.Fatalf("BMS 未接入时不应产出 bms_temp，实际 = %.2f°C —— "+
			"设备给 0 表示\"无数据\"，换算成绝对零度并写库是假数据", s.Value)
	}

	// 其余 BMS 字段仍应正常产出（不能因为温度缺失就整帧丢弃）。
	if findSensor(data, "bms_soc") == nil {
		t.Fatalf("bms_soc 应正常产出，实际字段: %v", sensorNames(data))
	}
}

// TestTechfineHBMS1EmitsTempWhenBmsPresent 防"一刀切把温度也砍了"。
//
// 反向验证：把 hasBmsTemp 改成恒 false，本用例变红。
func TestTechfineHBMS1EmitsTempWhenBmsPresent(t *testing.T) {
	// 29815 → 29815/100 - 273.15 = 25.00°C
	raw := []byte("(01 1001000000000000 048.0 054.0 020.0 080 010.0 005.0 29815 000000\r")
	data, err := (&TechfineInverterDriver{}).ParseDataWithCommand(raw, "48424d53310d")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	s := findSensor(data, "bms_temp")
	if s == nil {
		t.Fatalf("BMS 在线时应产出 bms_temp，实际字段: %v", sensorNames(data))
	}
	if s.Value < 24.9 || s.Value > 25.1 {
		t.Fatalf("bms_temp 应为 25.00°C，实际 %.2f", s.Value)
	}
}
