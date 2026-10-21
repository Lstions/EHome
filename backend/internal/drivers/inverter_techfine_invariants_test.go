package drivers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// 不变式用例 —— 用「可证伪的断言」锁住这次重构的每一个设计承诺
// ============================================================================
//
// 2026-10-21 重构前，这套不变式**一条都不成立**：
//
//	· 命令表 4 份拷贝，改一处漏三处（read_eeprom 的 MinSensors 漂移成 15 ⇒ 动作永不可达）；
//	· GetSensorDefinitions 声明了 fan1_speed/fan2_speed，但没有任何代码路径能产出它们（幽灵传感器）；
//	· parseHEEP1 产出 4 个 eeprom_* 传感器，但 GetSensorDefinitions 一个都没声明；
//	· ParseData 的末期兜底会把 8 字段温度帧读成 5 个电池字段（生产污染 2501 条）。
//
// 下面每条用例都对应其中一条。它们全部只依赖注册表，不复制任何命令字面量，
// 因此将来新增命令时**不需要改测试**（除非确实要测新命令自身的语义）。

// techfineCanonicalFrames 是每个命令一条**生产实测**应答帧（不含结尾 CR）。
// 来源：生产 device_data（device_id=15），2026-10-19 ~ 10-21。
var techfineCanonicalFrames = map[string]string{
	"read_status":      "(00 B010000000000 102110021000000000000",
	"read_grid":        "(213.0 49.9 280 170 65 40 +00000 0000000000000000",
	"read_output":      "(217.5 50.0 00652 00612 010 030 06200 004.9 00790",
	"read_battery":     "(04 051.1 069 000 00010 356 110007100000000000000",
	"read_pv1":         "(230.5 12.4 02577 00000.0 00000 2 205.3 0 0000000",
	"read_pv2":         "(000.0 00.0 00000 00000.0 00000 0 070.7 0 0000000",
	"read_temperature": "(025 034 028 031 034 030 030 11000000000000000000",
	"read_energy":      "(261020 11:21 05.766 0177.3 2200.0 000004601.6 000000000000",
	"read_bms":         "(01 10010000 00000000 048.0 054.0 020 080 010.0 005.0 29815",
	"read_eeprom":      "(1 060 020 0 0 0 0 230 1 0 0 0 0 0 0 000 000 000 000 230.0 50.0 000.0 O",
	"read_version":     "(0000.03 20230220 00",
}

// techfineExtraFrames 是不属于「每个命令一条」主夹具、但必须被覆盖的形状。
var techfineExtraFrames = []struct {
	why   string
	cmd   string // 空 = 走 ParseData（无上下文）路径
	frame string
}{
	{"真机 8 字段温度帧：风扇转速 50%", "read_temperature", "(041 037 032 033 041 050 050 11000000000000000000"},
	{"真机 8 字段温度帧：风扇停转（状态位 00）", "read_temperature", "(034 035 028 031 035 030 030 00000000000000000000"},
	{"旧机型 11 字段温度帧（唯一能产出 pv2_temp/dc_rectifier_temp 的形状）", "read_temperature", "(035 045 040 050 050 060 050 01 01 038 042"},
	{"QPRTL 协议类型查询（无命令模板）", "", "(MMMMMMMM"},
}

// techfineParseFixture 用给定命令解析一条夹具；cmd 为空时走 ParseData。
func techfineParseFixture(t *testing.T, cmd, frame string) ([]SensorData, error) {
	t.Helper()
	d := &TechfineInverterDriver{}
	raw := []byte(frame + "\r")
	if cmd == "" {
		return d.ParseData(raw)
	}
	c, ok := techfineCommandByID[cmd]
	if !ok {
		t.Fatalf("fixture refers to unknown command %q", cmd)
	}
	return d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(c.Wire)))
}

// TestTechfineRegistryIsSingleSourceOfTruth 钉住「四个入口只有一个真相源」。
//
// 它凭什么会失败：把 ControlActions()/GetCommandTemplates() 改回手抄一份命令表，
// 任何一处漏改（少一条、顺序不同、wire 编码不一致、IntervalMs 不一致）都会变红。
func TestTechfineRegistryIsSingleSourceOfTruth(t *testing.T) {
	if len(techfineCommands) == 0 {
		t.Fatal("命令注册表为空")
	}
	d := &TechfineInverterDriver{}

	// --- 注册表自身完整性 ---
	seenID := map[string]bool{}
	seenWire := map[string]bool{}
	for i := range techfineCommands {
		c := &techfineCommands[i]
		if c.ID == "" || c.Name == "" || c.Description == "" {
			t.Errorf("命令 #%d 缺少 ID/Name/Description", i)
		}
		if c.Parser == nil {
			t.Errorf("%s: Parser 为 nil", c.ID)
		}
		if c.MinSensors <= 0 {
			t.Errorf("%s: MinSensors=%d，必须为正（否则动作校验失效）", c.ID, c.MinSensors)
		}
		if len(c.Family) == 0 {
			t.Errorf("%s: Family 为空（形状绑定失效）", c.ID)
		}
		if !strings.HasSuffix(c.Wire, "\r") {
			t.Errorf("%s: Wire=%q 必须以 CR 结尾", c.ID, c.Wire)
		}
		if seenID[c.ID] {
			t.Errorf("重复的命令 ID: %s", c.ID)
		}
		seenID[c.ID] = true
		if seenWire[c.Wire] {
			t.Errorf("重复的 wire 帧: %q", c.Wire)
		}
		seenWire[c.Wire] = true
	}

	// --- ControlActions 必须与注册表逐条一致 ---
	actions := d.ControlActions()
	if len(actions) != len(techfineCommands) {
		t.Fatalf("ControlActions 返回 %d 条，注册表 %d 条", len(actions), len(techfineCommands))
	}
	for i, act := range actions {
		c := &techfineCommands[i]
		if act.ID != c.ID {
			t.Errorf("ControlActions[%d].ID=%q，注册表=%q（顺序/内容漂移）", i, act.ID, c.ID)
			continue
		}
		if string(act.TXData) != c.Wire {
			t.Errorf("%s: TXData=%q，注册表 Wire=%q", c.ID, string(act.TXData), c.Wire)
		}
		if !act.Enabled {
			t.Errorf("%s: 只读动作必须 Enabled（门禁不该成为永久禁用）", c.ID)
		}
		if act.ReadSize == 0 || act.RXTimeoutMS == 0 {
			t.Errorf("%s: ReadSize/RXTimeoutMS 必须非零", c.ID)
		}
	}

	// --- 轮询模板必须与注册表逐条一致 ---
	templates := d.GetCommandTemplates()
	if len(templates) != len(techfineCommands) {
		t.Fatalf("GetCommandTemplates 返回 %d 条，注册表 %d 条", len(templates), len(techfineCommands))
	}
	for i, tmpl := range templates {
		c := &techfineCommands[i]
		if tmpl.ID != c.ID {
			t.Errorf("模板[%d].ID=%q，注册表=%q", i, tmpl.ID, c.ID)
			continue
		}
		if tmpl.WriteData != hex.EncodeToString([]byte(c.Wire)) {
			t.Errorf("%s: WriteData=%q，期望 %q（hex(Wire)）",
				c.ID, tmpl.WriteData, hex.EncodeToString([]byte(c.Wire)))
		}
		if tmpl.ReadLength != 0 {
			t.Errorf("%s: ReadLength=%d，ASCII 行协议必须是 0（行空闲定帧）", c.ID, tmpl.ReadLength)
		}
		if !tmpl.Schedulable {
			t.Errorf("%s: 必须是可调度轮询模板（第三态已废止）", c.ID)
		}
		if tmpl.IntervalMs != c.DefaultIntervalMs {
			t.Errorf("%s: IntervalMs=%d，注册表=%d", c.ID, tmpl.IntervalMs, c.DefaultIntervalMs)
		}
		if _, ok := techfineCommandByWire(tmpl.WriteData); !ok {
			t.Errorf("%s: 模板 WriteData 反查不到命令（发送帧与解析帧不一致）", c.ID)
		}
	}
}

// TestTechfineEveryCommandIsReachable 钉住「每个 Enabled 动作都真的能成功」。
//
// 这条用例直接对应历史缺陷：read_eeprom 在动作目录里 Enabled=true，
// 但 MinSensors 写 15 而 parseHEEP1 只产出 4 ⇒ 用户点它必然失败，
// 且报错说"帧被截断"（把人引向错误的排查方向）。
//
// 它凭什么会失败：任何一个命令的 MinSensors 大于其 parser 在规范帧上的产出数。
func TestTechfineEveryCommandIsReachable(t *testing.T) {
	for i := range techfineCommands {
		c := &techfineCommands[i]
		frame, ok := techfineCanonicalFrames[c.ID]
		if !ok {
			t.Errorf("%s: 缺少规范夹具（新增命令必须同时提供一条生产实测帧）", c.ID)
			continue
		}
		data, err := techfineParseFixture(t, c.ID, frame)
		if err != nil {
			t.Errorf("%s: 规范帧解析失败，该动作不可达: %v", c.ID, err)
			continue
		}
		if len(data) < c.MinSensors {
			t.Errorf("%s: 规范帧产出 %d 个传感器，小于 MinSensors=%d —— 该动作永远不可能通过校验",
				c.ID, len(data), c.MinSensors)
		}
		// 动作级校验必须对规范帧放行。
		if err := expectActionSensors(c, data); err != nil {
			t.Errorf("%s: 规范帧未通过动作形状绑定: %v", c.ID, err)
		}
		// 规范帧同样必须能走通 verifier（含 CR 结尾 + 形状绑定）。
		raw := []byte(frame + "\r")
		if _, err := (&TechfineInverterDriver{}).VerifyControlAction(
			c.ID, json.RawMessage("{}"), raw); err != nil {
			t.Errorf("%s: verifier 拒绝了规范帧: %v", c.ID, err)
		}
	}
}

// TestTechfineSensorDefinitionsMatchParserOutput 钉住「声明面 == 产出面」。
//
// 两个方向都要成立：
//  1. 每个 parser 产出的传感器名都必须在 GetSensorDefinitions 里声明过
//     （否则前端/HA Discovery 看不到它 —— 历史缺陷：4 个 eeprom_* 从未声明）；
//  2. 每个声明的传感器都必须至少有一条 parser 路径能产出
//     （否则就是幽灵传感器 —— 历史缺陷：fan1_speed/fan2_speed 声明了但永远为 0 行）。
func TestTechfineSensorDefinitionsMatchParserOutput(t *testing.T) {
	d := &TechfineInverterDriver{}
	declared := map[string]bool{}
	for _, def := range d.GetSensorDefinitions() {
		if declared[def.Name] {
			t.Errorf("GetSensorDefinitions 重复声明: %s", def.Name)
		}
		declared[def.Name] = true
	}

	produced := map[string]bool{}
	record := func(cmd, frame string) {
		data, err := techfineParseFixture(t, cmd, frame)
		if err != nil {
			t.Fatalf("夹具解析失败（cmd=%q frame=%q）: %v", cmd, frame, err)
		}
		for _, s := range data {
			produced[s.Name] = true
		}
	}
	for i := range techfineCommands {
		c := &techfineCommands[i]
		record(c.ID, techfineCanonicalFrames[c.ID])
	}
	for _, extra := range techfineExtraFrames {
		record(extra.cmd, extra.frame)
	}

	// 方向 1：产出 ⊆ 声明
	var undeclared []string
	for name := range produced {
		if !declared[name] {
			undeclared = append(undeclared, name)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("以下传感器会被解析产出，但 GetSensorDefinitions 没有声明: %v", undeclared)
	}

	// 方向 2：声明 ⊆ 产出
	var ghosts []string
	for name := range declared {
		if !produced[name] {
			ghosts = append(ghosts, name)
		}
	}
	sort.Strings(ghosts)
	if len(ghosts) > 0 {
		t.Errorf("以下传感器被声明但没有任何解析路径能产出（幽灵传感器）: %v", ghosts)
	}
}

// TestTechfineShapeRulesAreMutuallyExclusive 钉住「每种真实形状只命中一条规则」。
//
// 形状分派是 ParseData 的唯一依据；一旦两条规则同时命中，分派就退化成
// "按 if 链顺序碰运气"，而历史上正是这个顺序错误把温度帧送进了 HBAT。
func TestTechfineShapeRulesAreMutuallyExclusive(t *testing.T) {
	type shapeCase struct {
		why      string
		frame    string
		wantRule string
	}
	cases := []shapeCase{
		{"真机 8 字段温度帧（P0 元凶：曾命中 HBAT）", "(025 034 028 031 034 030 030 11000000000000000000", "HTEMP8"},
		{"真机 7 字段电池帧", "(04 051.1 069 000 00010 356 110007100000000000000", "HBAT"},
		{"真机 8 字段市电帧", "(213.0 49.9 280 170 65 40 +00000 0000000000000000", "HGRID"},
		{"真机 9 字段输出帧", "(217.5 50.0 00652 00612 010 030 06200 004.9 00790", "HOP"},
		{"真机 9 字段 PV 帧（结构上与 HOP 同字段数）", "(230.5 12.4 02577 00000.0 00000 2 205.3 0 0000000", "PV"},
		{"真机 3 字段状态帧", "(00 B010000000000 102110021000000000000", "HSTS"},
		{"真机 7 字段发电量帧", "(261020 11:21 05.766 0177.3 2200.0 000004601.6 000000000000", "HGEN"},
		{"旧机型 11 字段温度帧", "(035 045 040 050 050 060 050 01 01 038 042", "HTEMP11"},
		{"HBMS1 10 字段帧", "(01 10010000 00000000 048.0 054.0 020 080 010.0 005.0 29815", "HBMS1"},
		{"HIMSG1 版本帧", "(0000.03 20230220 00", "HIMSG1"},
	}
	for _, c := range cases {
		fields := strings.Fields(strings.TrimRight(strings.TrimPrefix(c.frame, "("), "\r"))
		got := techfineMatchedShapeNames(fields)
		if len(got) != 1 || got[0] != c.wantRule {
			t.Errorf("%s\n  frame=%q\n  命中规则=%v，期望恰好 [%s]", c.why, c.frame, got, c.wantRule)
		}
	}
}

// TestTechfineParseDataFailsClosedOnAmbiguousAndUnknown 钉住「认不出就失败」。
//
// 这是本次修复的**行为级**回归点：修复前，第一条输入会返回 5 个电池字段且 err=nil。
func TestTechfineParseDataFailsClosedOnAmbiguousAndUnknown(t *testing.T) {
	d := &TechfineInverterDriver{}
	// 8 字段温度帧在**形状唯一**时应当被正确解析（收紧 HBAT 到恰好 7 字段后，
	// 它不再与任何其它形状撞车）。这正是 P0 的修复方式：不是"一律拒绝"，
	// 而是"要么正确解析、要么明确报错，绝不猜成另一个命令"。
	//
	// 这里钉住的是**结果**而不是报错：解析成功时绝不能出现电池字段。
	{
		data, err := d.ParseData([]byte("(027 039 033 035 039 030 030 11000000000000000000\r"))
		if err != nil {
			t.Fatalf("8 字段温度帧形状唯一，应解析成功，实际: %v", err)
		}
		for _, sd := range data {
			if strings.HasPrefix(sd.Name, "battery_") || sd.Name == "bus_voltage" {
				t.Fatalf("温度帧被解析出电池字段 %s=%.1f —— 这就是 2026-10-19 污染 2501 条的缺陷",
					sd.Name, sd.Value)
			}
		}
		if findSensor(data, "inverter_temp") == nil {
			t.Fatalf("温度帧解析结果缺少 inverter_temp: %v", data)
		}
	}

	cases := []struct {
		why   string
		frame string
	}{
		{"HPV/HPVB 同构形状（无法自证是 PV1 还是 PV2）", "(120.5 08.0 00960"},
		{"9 字段 PV 形状（与 HOP 同字段数，必须靠命令区分）", "(000.0 00.0 00000 00000.0 00000 0 070.7 0 0000000"},
		{"字段数不足的畸形帧", "(230.0 50"},
		{"完全无法识别的形状", "(hello world this is not a techfine frame"},
	}
	for _, c := range cases {
		data, err := d.ParseData([]byte(c.frame + "\r"))
		if err == nil {
			names := make([]string, 0, len(data))
			for _, s := range data {
				names = append(names, fmt.Sprintf("%s=%.1f", s.Name, s.Value))
			}
			t.Errorf("%s\n  ParseData 必须报错，却返回 %v", c.why, names)
		}
	}

	// 反向对照：有命令上下文时，同一条 8 字段帧必须成功（否则就是"一刀切拒绝"式假修复）。
	cmd, ok := techfineCommandByID["read_temperature"]
	if !ok {
		t.Fatal("注册表缺少 read_temperature")
	}
	raw := []byte("(027 039 033 035 039 030 030 11000000000000000000\r")
	data, err := d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(cmd.Wire)))
	if err != nil {
		t.Fatalf("有命令上下文时该帧必须成功: %v", err)
	}
	if len(data) < cmd.MinSensors {
		t.Errorf("产出 %d 个传感器 < MinSensors=%d", len(data), cmd.MinSensors)
	}
	for _, s := range data {
		if strings.HasPrefix(s.Name, "battery_") || s.Name == "bus_voltage" {
			t.Fatalf("温度帧在命令上下文下仍产出了电池字段 %s=%.1f", s.Name, s.Value)
		}
	}
}

// TestTechfineCrossCommandFramesAreRejected 钉住「命令上下文给错也必须失败」。
//
// 纵深防御：即使上游把命令 ID 弄错，字段数/形状校验也要拦住——
// 这样"命令上下文丢失或错误"不会重新变成 2026-10-19 那类污染。
func TestTechfineCrossCommandFramesAreRejected(t *testing.T) {
	cases := []struct {
		cmd   string
		frame string
		why   string
	}{
		{"read_battery", "(027 039 033 035 039 030 030 11000000000000000000", "8 字段温度帧被当成电池应答"},
		{"read_temperature", "(04 051.1 069 000 00010 356 110007100000000000000", "7 字段电池帧被当成温度应答"},
		{"read_grid", "(217.5 50.0 00652 00612 010 030 06200 004.9 00790", "9 字段输出帧被当成市电应答"},
		{"read_status", "(025 034 028 031 034 030 030 11000000000000000000", "温度帧被当成状态应答"},
		{"read_version", "(213.0 49.9 280 170 65 40 +00000 0000000000000000", "市电帧被当成版本应答"},
		{"read_temperature", "(213.0 49.9 280 170 65 40 +00000 0000000000000000", "8 字段市电帧被当成温度应答（末位恰好也是二进制串）"},
	}
	for _, c := range cases {
		raw := []byte(c.frame + "\r")
		data, err := (&TechfineInverterDriver{}).VerifyControlAction(c.cmd, json.RawMessage("{}"), raw)
		if err == nil {
			names := make([]string, 0, len(data))
			for _, s := range data {
				names = append(names, s.Name)
			}
			t.Errorf("%s: 必须拒绝，却返回 %v", c.why, names)
		}
	}
}

// TestTechfineMalformedNumericFieldIsRejected 钉住「数值字段是垃圾串必须报错」。
//
// 2026-10-21 变异测试发现的缺口：把 parseRequiredFloat 改回「解析失败返回 0」
// 后，此前全部用例**仍然全绿**。原因是所有夹具都是合法帧，没有任何用例
// 检查"字段内容坏掉"这一维度。
//
// 这个缺口很危险：0 是完全合法的物理读数，静默 0 会把"垃圾帧"变成"读数为 0"，
// 正是 parsePV 在注释里用三十行论证过、却只覆盖了 PV 功率一个字段的那类假数据。
//
// 它凭什么会失败：任何一个 parser 对某个数值位退化成 0 兜底（或干脆不校验），
// 对应命令那一行就会变红。
func TestTechfineMalformedNumericFieldIsRejected(t *testing.T) {
	cases := []struct {
		cmd string
		idx int // 要破坏的字段下标（必须是该命令真正会被解析的数值位）
	}{
		{"read_status", 0},      // fault_code（Atoi）
		{"read_grid", 0},        // grid_voltage
		{"read_output", 0},      // output_voltage
		{"read_battery", 1},     // battery_voltage
		{"read_pv1", 0},         // pv1_voltage
		{"read_pv2", 0},         // pv2_voltage
		{"read_temperature", 0}, // pv_temp
		{"read_energy", 2},      // daily_energy
		{"read_bms", 3},         // discharge voltage limit
		{"read_eeprom", 1},      // max charge current
		{"read_version", 0},     // software_version
	}
	d := &TechfineInverterDriver{}
	for _, c := range cases {
		cmd, ok := techfineCommandByID[c.cmd]
		if !ok {
			t.Fatalf("未知命令 %q", c.cmd)
		}
		frame, ok := techfineCanonicalFrames[c.cmd]
		if !ok {
			t.Fatalf("%s: 缺少规范夹具", c.cmd)
		}
		f := strings.Fields(strings.TrimRight(strings.TrimPrefix(frame, "("), "\r"))
		if c.idx >= len(f) {
			t.Fatalf("%s: 夹具只有 %d 个字段，无法破坏第 %d 位", c.cmd, len(f), c.idx)
		}
		f[c.idx] = "XX"
		corrupted := []byte("(" + strings.Join(f, " ") + "\r")
		wire := hex.EncodeToString([]byte(cmd.Wire))

		if data, err := d.ParseDataWithCommand(corrupted, wire); err == nil {
			t.Errorf("%s: 第 %d 位改成 \"XX\" 后必须报错，却返回 %d 个字段 —— "+
				"静默 0 兜底会把垃圾帧变成 0 读数（假数据比缺失更难排查）: %v",
				c.cmd, c.idx, len(data), data)
		}
	}
}
