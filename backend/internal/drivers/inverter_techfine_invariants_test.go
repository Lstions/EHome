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
	// 复核变异 M9 发现：此前每个命令只破坏**一个**下标，而 read_output 破坏的是
	// idx0、read_battery 破坏的是 idx1 —— 两者都走 strconv 直连，不经过
	// parseRequiredFloat，于是"把 parseHOP 的 apparent_power 改成 0 兜底"能存活。
	// 现在按"每个真正被解析的数值位"逐一破坏（共 43 个组合）。
	parsedIndices := map[string][]int{
		"read_status":      {0},             // fault_code（Atoi）
		"read_grid":        {0, 1},          // 电压、频率
		"read_output":      {0, 1, 2, 3, 4}, // 电压/频率/视在功率/有功功率/负载
		"read_battery":     {1, 2, 3, 4, 5}, // 电压/容量/充电流/放电流/BUS 电压
		"read_pv1":         {0, 1, 2},       // 电压/电流/功率
		"read_pv2":         {0, 1, 2},
		"read_temperature": {0, 1, 2, 3, 4, 5, 6}, // 5 温度 + 2 风扇转速
		"read_energy":      {2, 3, 4, 5},          // 日/月/年/总
		"read_bms":         {3, 4, 5, 6, 7, 8, 9}, // 限制值/SOC/电流/温度原始值
		"read_eeprom":      {0, 1, 7, 8},          // 机型码/充电限流/输出电压/频率标志
		"read_version":     {0, 1},                // 版本号/日期
	}
	d := &TechfineInverterDriver{}
	checked := 0
	for cmdID, indices := range parsedIndices {
		cmd, ok := techfineCommandByID[cmdID]
		if !ok {
			t.Fatalf("未知命令 %q", cmdID)
		}
		frame := techfineCanonicalFrames[cmdID]
		base := strings.Fields(strings.TrimRight(strings.TrimPrefix(frame, "("), "\r"))
		for _, idx := range indices {
			if idx >= len(base) {
				t.Fatalf("%s: 夹具只有 %d 个字段，无法破坏第 %d 位", cmdID, len(base), idx)
			}
			f := append([]string(nil), base...)
			f[idx] = "XX"
			corrupted := []byte("(" + strings.Join(f, " ") + "\r")
			wire := hex.EncodeToString([]byte(cmd.Wire))
			checked++
			if data, err := d.ParseDataWithCommand(corrupted, wire); err == nil {
				t.Errorf("%s: 第 %d 位改成 \"XX\" 后必须报错，却返回 %d 个字段 —— "+
					"静默 0 兜底会把垃圾帧变成 0 读数（假数据比缺失更难排查）: %v",
					cmdID, idx, len(data), data)
			}
		}
	}
	if checked < 40 {
		t.Fatalf("只检查了 %d 个数值位，覆盖不足", checked)
	}
}

// ============================================================================
// 交叉命令矩阵 —— 替代此前只覆盖 6 对的"过度承诺"用例
// ============================================================================
//
// 2026-10-21 复核发现：之前的 TestTechfineCrossCommandFramesAreRejected 只挑了
// 6 个组合，但实测存在**系统性 fail-open**：
//
//	read_energy 接受全部 70,115 帧（温度帧当能量 → daily=28/monthly=31 kWh）
//	read_output 接受全部市电帧（280V 线电压当输出电压、损失电压当功率）
//	read_pv1/2  接受全部输出帧（输出电流 49.9A 当 pv1_current）
//
// 根因是 parser 只校验"字段数够不够"，不校验"这是不是我的形状"。
// 现在 parseHGEN 要求 HH:MM、parseHOP 要求 HOP 谓词、parsePV 要求 PV 谓词。
// 本用例把"命令 × 形状"的全部组合（11 × 11 = 121）都钉住：
// **只有该命令本该接受的形状能通过，其余一律必须拒绝。**

// techfineShapeFixtures 是每种真实形状的一条代表帧（生产帧或协议夹具）。
var techfineShapeFixtures = map[string]string{
	"HSTS":    "(00 B010000000000 102110021000000000000",
	"HGRID":   "(213.0 49.9 280 170 65 40 +00000 0000000000000000",
	"HOP":     "(217.5 50.0 00652 00612 010 030 06200 004.9 00790",
	"HBAT":    "(04 051.1 069 000 00010 356 110007100000000000000",
	"PV":      "(230.5 12.4 02577 00000.0 00000 2 205.3 0 0000000",
	"HTEMP8":  "(041 037 032 033 041 100 100 11000000000000000000",
	"HTEMP11": "(035 045 040 050 050 060 050 01 01 038 042",
	"HGEN":    "(261020 11:21 05.766 0177.3 2200.0 000004601.6 000000000000",
	"HBMS1":   "(01 10010000 00000000 048.0 054.0 020 080 010.0 005.0 29815",
	"HEEP1":   "(1 060 020 0 0 0 0 230 1 0 0 0 0 0 0 000 000 000 000 230.0 50.0 000.0 O",
	"HIMSG1":  "(0000.03 20230220 00",
}

// techfineAllowedShapes 声明每个命令**允许**接受的应答形状。
var techfineAllowedShapes = map[string][]string{
	"read_status":      {"HSTS"},
	"read_grid":        {"HGRID"},
	"read_output":      {"HOP"},
	"read_battery":     {"HBAT"},
	"read_pv1":         {"PV"},
	"read_pv2":         {"PV"},
	"read_temperature": {"HTEMP8", "HTEMP11"},
	"read_energy":      {"HGEN"},
	"read_bms":         {"HBMS1"},
	"read_eeprom":      {"HEEP1"},
	"read_version":     {"HIMSG1"},
}

func TestTechfineCrossCommandMatrix(t *testing.T) {
	d := &TechfineInverterDriver{}
	allowed := make([]string, 0, len(techfineAllowedShapes))
	for id := range techfineAllowedShapes {
		allowed = append(allowed, id)
	}
	// 注册表里的每个命令都必须在这张矩阵里声明允许形状，否则矩阵会静默漏测。
	if len(allowed) != len(techfineCommands) {
		t.Fatalf("矩阵覆盖 %d 个命令，注册表有 %d 个（新增命令必须同时声明允许形状）",
			len(allowed), len(techfineCommands))
	}

	checked := 0
	for cmdID, okShapes := range techfineAllowedShapes {
		if _, exists := techfineCommandByID[cmdID]; !exists {
			t.Fatalf("矩阵引用了未登记的命令 %q", cmdID)
		}
		for shape, frame := range techfineShapeFixtures {
			raw := []byte(frame + "\r")
			data, err := d.VerifyControlAction(cmdID, json.RawMessage("{}"), raw)
			wantAccept := false
			for _, s := range okShapes {
				if s == shape {
					wantAccept = true
					break
				}
			}
			checked++
			switch {
			case wantAccept && err != nil:
				t.Errorf("%s 应接受 %s 形状，却被拒绝: %v\n  raw=%s", cmdID, shape, err, frame)
			case !wantAccept && err == nil:
				names := make([]string, 0, len(data))
				for _, sd := range data {
					names = append(names, fmt.Sprintf("%s=%.1f", sd.Name, sd.Value))
				}
				t.Errorf("%s 接受了 %s 形状的帧 —— 命令上下文给错时必须拒绝，否则会把该帧读成假数据: %v\n  raw=%s",
					cmdID, shape, names, frame)
			}
		}
	}
	if checked != len(techfineAllowedShapes)*len(techfineShapeFixtures) {
		t.Fatalf("矩阵只跑了 %d 个组合", checked)
	}
}

// ============================================================================
// fail-closed 守卫的行为用例（复核变异 M7/M8 暴露的缺口）
// ============================================================================

// TestTechfineHBMS1ShortStatusIsRejected 钉住"状态串不足 8 位必须拒绝"。
//
// 复核变异 M7 发现：把 parseHBMS1 的 len(status1)<8 守卫删掉后，**没有用例变红**，
// 而且真实行为不是"静默降级"而是 **panic: index out of range**（status1[4]）。
// 这条用例同时证明：拒绝发生在 panic 之前。
func TestTechfineHBMS1ShortStatusIsRejected(t *testing.T) {
	short := []string{"01", "1001000", "00000000", "048.0", "054.0", "020", "080", "010.0", "005.0", "29815"}
	data, err := (&TechfineInverterDriver{}).parseHBMS1(short)
	if err == nil {
		t.Fatalf("7 位状态串必须拒绝（不足 8 位无法判定告警），实际返回 %d 个字段", len(data))
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("错误信息应指出状态串过短，实际: %v", err)
	}

	// 反向对照：8 位状态串必须正常解析（防止"一刀切拒绝"）。
	ok := []string{"01", "10010000", "00000000", "048.0", "054.0", "020", "080", "010.0", "005.0", "29815"}
	data, err = (&TechfineInverterDriver{}).parseHBMS1(ok)
	if err != nil {
		t.Fatalf("8 位状态串应正常解析: %v", err)
	}
	if findSensor(data, "bms_comm_ok") == nil {
		t.Fatalf("应产出 bms_comm_ok，实际 %v", sensorNames(data))
	}
}

// TestTechfineHTEMPShortStatusIsRejected 钉住"状态位串不足 2 位必须拒绝"。
//
// 复核变异 M8 发现：删掉该守卫后没有用例变红，行为是 fan2_status 静默变成 0
// —— 即"缺失"被读成"风扇停转"，又一种静默假数据。
func TestTechfineHTEMPShortStatusIsRejected(t *testing.T) {
	// 只有 1 位状态串：无法判定 fan2。
	short := []string{"041", "037", "032", "033", "041", "030", "030", "1"}
	data, err := (&TechfineInverterDriver{}).parseHTEMP(short)
	if err == nil {
		t.Fatalf("1 位状态串必须拒绝（缺失位会变成「风扇停转」），实际返回 %v", sensorNames(data))
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("错误信息应指出状态位串过短，实际: %v", err)
	}

	// 反向对照：2 位状态串必须可解析（"10" → fan1 转、fan2 停）。
	ok := []string{"041", "037", "032", "033", "041", "030", "030", "10"}
	data, err = (&TechfineInverterDriver{}).parseHTEMP(ok)
	if err != nil {
		t.Fatalf("2 位状态串应可解析: %v", err)
	}
	if s := findSensor(data, "fan1_status"); s == nil || s.Value != 1 {
		t.Fatalf("fan1_status 应为 1，实际 %+v", s)
	}
	if s := findSensor(data, "fan2_status"); s == nil || s.Value != 0 {
		t.Fatalf("fan2_status 应为 0，实际 %+v", s)
	}
}
