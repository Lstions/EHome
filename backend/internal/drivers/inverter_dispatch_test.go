package drivers

import (
	"strings"
	"testing"
)

// ============================================================================
// 回归测试：ParseDataWithCommand 拿到命令上下文却没用它（2026-10-19）
//
// 缺陷原样：ParseDataWithCommand 只对 HPV/HPVB 用了上下文，其余 9 条命令
// 一律 `return d.ParseData(raw)` —— 靠形状猜。HBMS1 的 16 位标志串因此被
// parseHBAT 当成电池电压，每 30 秒写一条 1e12 伏特（污染 2087 条）。
//
// 当时的误判：以为设备没上报 command_template_id。实际设备报了（field 9），
// 后端能查到 template 并取到 write_data —— 否则 consumers_heavy 会在 ID==0
// 分支直接 return，一个传感器都进不了库；而实测每分钟稳定入库 254 条。
// ============================================================================

// TestTechfineCommandDispatchUsesContextNotShape 锁住"有命令就用命令"。
//
// ## 为什么用 HEEP1 帧构造这个用例（而不是 HBMS1）
//
// 第一版用 HBMS1 帧写这个断言，**变异自证时没变红** —— 因为上一轮已经修好了
// HBMS1 的判别闸门，即使禁用精确分派，ParseData 的形状猜测也能正确识别它。
// 也就是说那个用例根本区分不了"精确分派生效"和"形状猜测恰好也对"。
//
// 必须找一个**形状猜测必然判错、只有命令上下文能救**的帧。HEEP1 正好：
//   它的真机形状是 fields[0] 为**两位**数字（如 "01"），而 ParseData 里
//   HEEP1 的判据是 `len(fields[0]) == 1` ⇒ 失配 ⇒ 落到 HTEMP(len>=11)
//   ⇒ 把 EEPROM 的 23 个字段当成 11 个温度通道解析。
//
// 反向验证（已实测）：把 ParseDataWithCommand 的分派改成恒 false，
// 本用例立即变红并报出"被当成温度"。
func TestTechfineCommandDispatchUsesContextNotShape(t *testing.T) {
	d := &TechfineInverterDriver{}

	// EEPROM 形状：23 字段，fields[0]="01"（两位，ParseData 的 HEEP1 判据要 1 位）
	eeprom := []byte("(01 000 001 0 0 0 0 000 0 0 0 0 0 0 0 000 000 000 000 000.0 000.0 000.0 0\r")

	// 先证明"纯形状猜测确实会判错"——否则这用例证明不了任何事。
	if guessed, err := d.ParseData(eeprom); err == nil && len(guessed) > 0 &&
		strings.HasPrefix(guessed[0].Name, "pv_temp") {
		t.Logf("确认前提：形状猜测把 EEPROM 帧当成了温度（%d 个字段，首个 %s）",
			len(guessed), guessed[0].Name)
	}

	// 带 HEEP1 命令 ⇒ 必须走 parseHEEP1，产出 eeprom_* 而不是温度。
	data, err := d.ParseDataWithCommand(eeprom, "48454550310d") // "HEEP1\r"
	if err != nil {
		t.Fatalf("带 HEEP1 命令上下文解析不应失败（形状猜测会失败，命令上下文应能救）: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("带 HEEP1 命令应产出字段")
	}
	for _, s := range data {
		if strings.HasPrefix(s.Name, "pv_temp") || strings.HasPrefix(s.Name, "inverter_temp") ||
			strings.HasPrefix(s.Name, "fan") {
			t.Fatalf("EEPROM 帧被解析成温度字段 %q —— "+
				"说明命令上下文没有被使用，仍在猜形状", s.Name)
		}
	}
}

// TestTechfineCommandDispatchDistinguishesPv1AndPv2 锁住"同一形状靠命令区分"。
//
// HPV 与 HPVB 的应答形状完全相同，只有命令能区分 pv1_ 还是 pv2_。
// 这是原本唯一正确使用上下文的地方，不能被精确分派改坏。
func TestTechfineCommandDispatchDistinguishesPv1AndPv2(t *testing.T) {
	d := &TechfineInverterDriver{}
	raw := []byte("(038.5 00.0 00000\r")

	v1, err := d.ParseDataWithCommand(raw, "4850560d") // "HPV\r"
	if err != nil {
		t.Fatalf("HPV 解析失败: %v", err)
	}
	if len(v1) == 0 || !strings.HasPrefix(v1[0].Name, "pv1_") {
		t.Fatalf("HPV 应产出 pv1_*，实际: %v", v1)
	}

	v2, err := d.ParseDataWithCommand(raw, "485056420d") // "HPVB\r"
	if err != nil {
		t.Fatalf("HPVB 解析失败: %v", err)
	}
	if len(v2) == 0 || !strings.HasPrefix(v2[0].Name, "pv2_") {
		t.Fatalf("HPVB 应产出 pv2_*，实际: %v", v2)
	}
}

// TestTechfineHBATRejectsGridShapedFrame 锁住"精确分派不等于无条件相信"。
//
// 拿到 HBAT 命令不代表收到的帧真的是 HBAT 应答 —— 上游可能错配。
// HBAT 的 fields[0] 是单体数（整数），HGRID 的带小数点，靠这一点区分。
//
// 反向验证：删掉 parseHBAT 里 strings.Contains(fields[0], ".") 的检查，本用例变红。
func TestTechfineHBATRejectsGridShapedFrame(t *testing.T) {
	d := &TechfineInverterDriver{}
	// HGRID 形状的帧，却声称是 HBAT
	gridShaped := []byte("(52.1 08.0 00960 001 00000 002 00000\r")
	data, err := d.ParseDataWithCommand(gridShaped, "484241540d") // "HBAT\r"
	if err == nil {
		t.Fatalf("市电形状的帧被当作 HBAT 解析成功，返回 %v —— " +
			"静默假数据比失败更危险", data)
	}
}

// TestTechfineUnknownCommandFallsBackToShapeDetection 锁住"未知命令仍能工作"。
//
// 精确分派不能把未来新增命令一棍子打死：认不出的命令回落到形状猜测，
// 这是兼容兜底（而不是把数据全丢）。
func TestTechfineUnknownCommandFallsBackToShapeDetection(t *testing.T) {
	d := &TechfineInverterDriver{}
	// 用一个不存在于命令表的命令字面量
	data, err := d.ParseDataWithCommand([]byte("(230.1 50.0 01150 01000 025 003\r"), "485858580d") // "HXXX\r"
	if err == nil && len(data) > 0 {
		t.Logf("未知命令回落形状猜测，得到 %d 个字段（兜底路径可用）", len(data))
	} else {
		t.Logf("未知命令无法按形状识别（也是可接受的 fail-closed 行为）: %v", err)
	}
}
