package drivers

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// ============================================================================
// GB3024 命令注册表 —— 全驱动唯一真相源
// ============================================================================
//
// ## 为什么要有这张表（2026-10-21 重构）
//
// 重构前，同一张 11 条命令表在同一个文件里被**抄了 4 份**：
//
//	ControlActions()        —— 命令 + 名称 + 描述
//	VerifyControlAction()   —— 内联 map[actionID]command
//	techfineParserFor()     —— switch[command]parser
//	GetCommandTemplates()   —— 命令 + 轮询间隔
//
// 四份拷贝各自演化，已经产生了真实缺陷：
//
//	· read_eeprom 在 ControlActions 里 Enabled=true，但在 techfineActionMinSensors
//	  里被要求 >=15 个传感器，而 parseHEEP1 无论帧多完整都只产出 4 个
//	  ⇒ 该动作**永远不可能成功**，报错还说"帧被截断"（误导排查方向）。
//	· GetCommandTemplates 的注释自称"与 ControlActions 使用同一张命令表，
//	  避免两处各自维护而产生漂移" —— 实测它只复用了 asciiToHex，命令字面量
//	  仍是第 4 份拷贝。注释与事实不符比没有注释更危险。
//
// 现在四个入口全部从下面这一张表派生；新增/修改命令只需要改一处，
// 漏改会在包初始化时 panic（见 init 的下标校验），而不是在运行时静默漂移。
//
// ## 两个设计选择
//
//  1. Parser 存**方法表达式**而不是绑定后的方法值。
//     方法表达式类型是 func(*TechfineInverterDriver, []string) ...，
//     表在 init 期构建一次 ⇒ 解析路径每帧零分配；而旧的 techfineParserFor
//     每次调用都会 &TechfineInverterDriver{} 新建一个接收者。
//
//  2. 每个命令显式声明 MinSensors（完整形状下最少产出多少传感器）。
//     它同时是两个东西：动作校验的下界，以及"解析器与动作表是否一致"的
//     可测断言目标（inverter_techfine_invariants_test.go 用一条用例钉死）。
type techfineParserFunc func(*TechfineInverterDriver, []string) ([]SensorData, error)

type techfineCommand struct {
	// ID 同时是 ControlAction.ID 与 CommandTemplate.ID —— 一个命名空间。
	ID          string
	Name        string
	Description string
	// Wire 是发送到设备的 ASCII 帧，含结尾 CR。
	Wire   string
	Parser techfineParserFunc
	// MinSensors 是**完整形状**应答的最少产出数。小于它会判为截断帧。
	MinSensors int
	// Family 是该命令应答允许出现的传感器名前缀。
	Family []string
	// DefaultIntervalMs 是轮询模板的默认周期；0 = 默认不轮询，由用户按需开启。
	DefaultIntervalMs int
}

// techfineCommands 是 GB3024 全部 11 条只读查询命令的定义。
//
// 顺序即 id 的声明顺序（历史上 command_index 就按这个顺序编号，
// 见 parseHTEMP 上方关于"索引 5 其实是 HPVB"的注释）—— 不要重排。
var techfineCommands = []techfineCommand{
	{
		ID: "read_status", Name: "读取运行状态", Wire: "HSTS\r",
		Description:       "故障代码、运行模式与告警标志",
		Parser:            (*TechfineInverterDriver).parseHSTS,
		MinSensors:        14, // fault_code + work_mode + 12 个告警位
		Family:            []string{"fault_code", "work_mode", "alarm_"},
		DefaultIntervalMs: 1000,
	},
	{
		ID: "read_grid", Name: "读取市电信息", Wire: "HGRID\r",
		Description: "市电电压、频率与丢失阈值",
		Parser:      (*TechfineInverterDriver).parseHGRID,
		MinSensors:  2, // grid_voltage + grid_frequency
		Family:      []string{"grid_"},
	},
	{
		ID: "read_output", Name: "读取输出信息", Wire: "HOP\r",
		Description: "输出电压、频率、功率与负载",
		Parser:      (*TechfineInverterDriver).parseHOP,
		MinSensors:  5, // output_voltage/frequency/apparent/active/load
		Family:      []string{"output_"},
	},
	{
		ID: "read_battery", Name: "读取电池信息", Wire: "HBAT\r",
		Description: "电池电压、容量、充放电电流与 BUS 电压",
		Parser:      (*TechfineInverterDriver).parseHBAT,
		MinSensors:  5, // battery_voltage/capacity/charge/discharge + bus_voltage
		Family:      []string{"battery_", "bus_voltage"},
	},
	{
		ID: "read_pv1", Name: "读取 PV1 信息", Wire: "HPV\r",
		Description: "PV1 电压、电流与功率",
		// HPV 与 HPVB 共用同一应答形状，必须靠命令区分 pv1_/pv2_ 前缀。
		Parser:     func(d *TechfineInverterDriver, f []string) ([]SensorData, error) { return d.parsePV(f, "pv1") },
		MinSensors: 3,
		Family:     []string{"pv1_"},
	},
	{
		ID: "read_pv2", Name: "读取 PV2 信息", Wire: "HPVB\r",
		// PV2 是**可选硬件**：协议 V0.5 把 HPVB 列为独立一节（该文档第 51 行），
		// 但很多机型只有一路 PV 输入。本机（node F0F5BD02DD84 / device 15）即 PV1-only：
		// 2026-10-18 15:29 ~ 10-19 12:54 期间 read_pv2 曾轮询，7,708 条应答**全部为 0**
		// （电压/电流/功率），随后用户把该命令 interval 设为 0。
		// 关键是设备对这些帧**有应答**（不是超时）⇒ HPVB 命令本身有效，只是没有第二路 PV。
		// 因此保留该能力（换一台带 PV2 的机型即可用），但默认不轮询。
		Description: "PV2 电压、电流与功率（可选硬件；PV1-only 机型恒为 0）",
		Parser:      func(d *TechfineInverterDriver, f []string) ([]SensorData, error) { return d.parsePV(f, "pv2") },
		MinSensors:  3,
		Family:      []string{"pv2_"},
	},
	{
		ID: "read_temperature", Name: "读取温度信息", Wire: "HTEMP\r",
		Description: "温度、风扇转速与状态",
		Parser:      (*TechfineInverterDriver).parseHTEMP,
		// 真机 8 字段形状产出 5 温度 + 2 风扇转速 + 2 风扇状态 = 9；
		// 11 字段布局产出 11（多出 PV2 温度与 DC 整流温度）。下界取 9。
		//
		// 注：协议 V0.5 的 HTEMP 摘要只列「PV/逆变/升压/变压器温度、风扇状态」，
		// 并没有 pv2_temp / dc_rectifier_temp 这两个名字 —— 它们是 11 字段布局里
		// 第 9/10 位的**候选语义**，且 PV2 本身是可选硬件（本机 PV1-only）。
		// 本机应答固定 8 字段 ⇒ 这两个传感器在本机永远不会出现。
		MinSensors: 9,
		Family: []string{
			"pv_temp", "inverter_temp", "boost_temp", "transformer_temp",
			"max_temp", "pv2_temp", "dc_rectifier_temp", "fan",
		},
	},
	{
		ID: "read_energy", Name: "读取发电量", Wire: "HGEN\r",
		Description: "日、月、年与总发电量",
		Parser:      (*TechfineInverterDriver).parseHGEN,
		MinSensors:  4,
		Family:      []string{"daily_energy", "monthly_energy", "yearly_energy", "total_energy"},
	},
	{
		ID: "read_bms", Name: "读取 BMS 信息", Wire: "HBMS1\r",
		Description: "BMS 状态、SOC、电流与限制值",
		Parser:      (*TechfineInverterDriver).parseHBMS1,
		MinSensors:  14, // 8 个状态位 + SOC/电流/限制值；bms_temp 为条件产出
		Family:      []string{"bms_"},
	},
	{
		ID: "read_eeprom", Name: "读取 EEPROM 设置", Wire: "HEEP1\r",
		Description: "持久化配置与 BMS SOC 阈值",
		Parser:      (*TechfineInverterDriver).parseHEEP1,
		// 历史缺陷：这里曾写 15，而 parseHEEP1 只产出 4 个 ⇒ 动作永不可达。
		// 4 是 parseHEEP1 的固定产出数（见该函数），MinSensors 必须等于它。
		MinSensors: 4,
		Family:     []string{"eeprom_"},
	},
	{
		ID: "read_version", Name: "读取软件版本", Wire: "HIMSG1\r",
		Description: "软件版本号与发布日期",
		Parser:      (*TechfineInverterDriver).parseHIMSG1,
		MinSensors:  2, // software_version + software_date
		Family:      []string{"software_"},
	},
}

var (
	// techfineCommandByID / ByWireHex / ByWireText 在 init 期建好，解析路径只做查表。
	techfineCommandByID      map[string]*techfineCommand
	techfineCommandByWireHex map[string]*techfineCommand
	// ByWireText 的键是**去掉 CR/空白并大写**的命令字面量（如 "HTEMP"）。
	// 存在的理由：生产库 config_template.write_data 可能存在历史遗留格式
	// （缺 CR、尾随空格）。按 hex 精确相等会把它们判成"未知命令"，从而回落到
	// 形状判定 —— 对 HPV/HPVB 这种同构形状就是直接报错，等于让一条能用的
	// 模板突然失效。宽容一次解码是安全的：它只做"命令名"匹配，不猜形状。
	techfineCommandByWireText map[string]*techfineCommand
)

func init() {
	techfineCommandByID = make(map[string]*techfineCommand, len(techfineCommands))
	techfineCommandByWireHex = make(map[string]*techfineCommand, len(techfineCommands))
	techfineCommandByWireText = make(map[string]*techfineCommand, len(techfineCommands))
	for i := range techfineCommands {
		c := &techfineCommands[i]
		if c.ID == "" || c.Wire == "" || c.Parser == nil || c.MinSensors <= 0 || len(c.Family) == 0 {
			panic("techfine: incomplete command registry entry at index " + fmt.Sprint(i))
		}
		if !strings.HasSuffix(c.Wire, "\r") {
			panic("techfine: command " + c.ID + " wire frame must end with CR")
		}
		if _, dup := techfineCommandByID[c.ID]; dup {
			panic("techfine: duplicate command ID " + c.ID)
		}
		hexWire := hex.EncodeToString([]byte(c.Wire))
		if _, dup := techfineCommandByWireHex[hexWire]; dup {
			panic("techfine: duplicate wire frame for command " + c.ID)
		}
		techfineCommandByID[c.ID] = c
		techfineCommandByWireHex[hexWire] = c
		if cmdText := techfineWireText(c.Wire); cmdText != "" {
			if _, dup := techfineCommandByWireText[cmdText]; dup {
				panic("techfine: duplicate wire text for command " + c.ID)
			}
			techfineCommandByWireText[cmdText] = c
		}
	}
}

// techfineWireText 把 wire 帧归一成"命令名"（去 CR / 去空白 / 大写）。
func techfineWireText(wire string) string {
	return strings.ToUpper(strings.TrimSpace(wire))
}

// techfineCommandByWire 按**发送帧的十六进制**查命令（与 CommandTemplate.WriteData
// / ConfigTemplate.WriteData 同一编码）。
//
// 两级匹配：
//  1. 精确 hex（正常路径，零解码开销）；
//  2. 宽容路径：解成 ASCII 后归一化再匹配命令名 —— 兼容历史上缺 CR/带空格的模板。
func techfineCommandByWire(writeDataHex string) (*techfineCommand, bool) {
	key := strings.ToLower(strings.TrimSpace(writeDataHex))
	if cmd, ok := techfineCommandByWireHex[key]; ok {
		return cmd, true
	}
	// 只有"合法 hex 但不是已知 wire"才继续；非法 hex 由调用方决定语义。
	raw, err := hex.DecodeString(key)
	if err != nil {
		return nil, false
	}
	cmd, ok := techfineCommandByWireText[techfineWireText(string(raw))]
	return cmd, ok
}

// ============================================================================
// 应答形状规则 —— ParseData（无命令上下文）的唯一依据
// ============================================================================
//
// ## 设计原则：只认**能自证**的形状，认不出就失败
//
// 重构前的 ParseData 是一条长长的 if 链，末段 `len(fields) >= 7 → parseHBAT` 是"兜底猜测"。
// 真机 HTEMP 是 8 字段，`len >= 11` 的 HTEMP 分支不命中，于是掉进 HBAT 兜底
// ⇒ **每 30 秒把一个 8 字段温度帧解析成 5 个电池字段**，且 err == nil。
// 生产实测污染 battery_voltage 2501/26847 条（value<45V，正常应 48~58V），
// 窗口 2026-10-18 14:46 → 10-19 11:43 UTC。
//
// 现在的规则：
//
//   - 每条规则自带**内容校验**（不只是字段数），例如 HTEMP8 要求前 7 位数字、
//     第 8 位是二进制串；
//   - 规则**按顺序**求值，命中即用（保持与历史 command_index 一致的分派）；
//   - 走到最后仍没有规则命中 ⇒ 返回错误，**不再有兜底猜测**；
//   - PV(HPV/HPVB) 形状同构、无法自证 ⇒ 明确返回"需要命令上下文"错误。
//
// 这些规则的证据分两层，不要混为一谈：
//
//	· **离线**：在生产 70,115 帧语料上验证过 —— 7 种真实形状每种恰好命中 1 条规则，
//	  且规则→落库传感器族 70,115/70,115 正确（脚本见 .analysis/，非 CI 门禁）；
//	· **CI**：techfine_production_replay_test.go 回放 62 条真实帧夹具，
//	  形状互斥性由 TestTechfineShapeRulesAreMutuallyExclusive 覆盖 10 种形状。
type techfineShapeRule struct {
	Name  string
	Match func([]string) bool
	// Command 是命中后要用的命令 ID。空 = 形状可识别但**无法自证命令**
	// （必须由调用方给出上下文），例如 HPV/HPVB。
	Command string
}

var techfineShapeRules = []techfineShapeRule{
	// HBMS1: (AA <二进制标志串> ...) —— f[1] 是 8+ 位二进制串。
	// 必须排在 HBAT 之前：历史上正是这条没排对，导致 16 位标志串被当成电压。
	{Name: "HBMS1", Command: "read_bms", Match: func(f []string) bool {
		return len(f) >= 10 && len(f[1]) >= 8 && isAllBinary(f[1])
	}},

	// HGEN: (YYYYMM HH:MM ...) —— f[1] 含冒号。
	{Name: "HGEN", Command: "read_energy", Match: func(f []string) bool {
		return len(f) >= 6 && strings.Contains(f[1], ":")
	}},

	// HEEP1: 15+ 字段，f[0] 是单字符机型码 0-2。
	{Name: "HEEP1", Command: "read_eeprom", Match: func(f []string) bool {
		// 机型码位数有两种说法：协议文档写单字符 0-2，驱动里的 dispatch 回归
		// 测试备注称真机是两位（"01"）。生产没有 read_eeprom 帧可证，因此两种
		// 都放行 —— 15+ 字段这个特征本身就是 HEEP1 独有的，不会与其它形状撞车。
		if len(f) < 15 {
			return false
		}
		code := f[0]
		if len(code) < 1 || len(code) > 2 || !isAllDigits(code) {
			return false
		}
		v, err := strconv.Atoi(code)
		return err == nil && v >= 0 && v <= 2
	}},

	// HTEMP(真机 8 字段): (AAA BBB CCC DDD EEE FFF GGG <20 位状态串>)
	//   前 7 位数字（5 温度 + 2 风扇转速），第 8 位二进制串。
	// 必须排在 HBAT 之前，且**不能**用 len>=11 的宽判据 —— 这正是 P0 根因。
	{Name: "HTEMP8", Command: "read_temperature", Match: func(f []string) bool {
		if len(f) != 8 || !isAllBinary(f[7]) {
			return false
		}
		// 前 7 位必须是**纯整数**（5 温度 + 2 风扇转速都是整数）。
		// 不能放宽成 isNumericField：8 字段 HGRID 帧的末位（"+00000"）恰好也是
		// 二进制串，用"数字"判定会把市电帧误判成温度帧 —— 那正是我们要根除的
		// "形状撞车"缺陷，只是换了个方向。
		for i := 0; i < 7; i++ {
			if !isAllDigits(f[i]) {
				return false
			}
		}
		return true
	}},

	// HSTS: (NN M<12 报警位> [尾部数据]) —— f[1] 是 1 个模式字符 + 12 报警位。
	// 生产真机是 3 字段（尾部还有一段），早期夹具是 2 字段，两者都接受。
	{Name: "HSTS", Command: "read_status", Match: func(f []string) bool {
		return len(f) >= 2 && len(f[1]) >= 13 && isWorkModeLetter(f[1][0])
	}},

	// HOP: (NNN.N MM.M AAAAA BBBBB CCC DDD EEEEE FFF.F ...) —— 第 8 位带小数（电感电流）。
	{Name: "HOP", Command: "read_output", Match: techfineLooksLikeHOP},

	// HGRID: (NNN.N MM.M AAA BBB CC DD ...) —— 前两位带小数，其余为整数阈值。
	// 用"损失电压必须是整数"与 HPV 区分：HPV 的 f[3] 形如 "00000.0"（带小数）。
	{Name: "HGRID", Command: "read_grid", Match: func(f []string) bool {
		if len(f) < 6 || !strings.Contains(f[0], ".") || !isNumericField(f[1]) {
			return false
		}
		if !isAllDigits(f[2]) || !isAllDigits(f[3]) || !isAllDigits(f[4]) || !isAllDigits(f[5]) {
			return false
		}
		// 与 HOP 互斥：同一个谓词取反即 HGRID（见 techfineLooksLikeHOP）。
		// 这样"8 字段市电帧被当成 HOP"与"9 字段输出帧被当成 HGRID"
		// 两类撞车都由构造排除，而不是靠 if 链顺序碰运气。
		return !techfineLooksLikeHOP(f)
	}},

	// HBAT: (AA BBB.B CCC DDD EEEEE FFF <标志串>) —— 恰好 7 字段，f[0] 是单体数（整数）。
	// **恰好 7** 是关键防线：8 字段的温度帧即使命令上下文给错也进不了这里。
	{Name: "HBAT", Command: "read_battery", Match: func(f []string) bool {
		return len(f) == 7 && !strings.Contains(f[0], ".") && isNumericField(f[0]) &&
			strings.Contains(f[1], ".")
	}},

	// HTEMP(旧机型 11 字段): 5 温度 + 2 风扇转速 + 2 风扇状态 + 2 额外温度。
	// 收紧为**恰好 11**（历史实现是 >=11，会给畸形长帧开口子）。
	{Name: "HTEMP11", Command: "read_temperature", Match: func(f []string) bool {
		if len(f) != 11 || isAllBinary(f[1]) {
			// isAllBinary(f[1]) 为真说明这是 BMS 状态串，不是温度帧。
			return false
		}
		for i := 0; i < 11; i++ {
			if !isNumericField(f[i]) {
				return false
			}
		}
		return true
	}},

	// HIMSG1: (NNNN.NN YYYYMMDD [DD]) —— f[1] 是 8 位日期。
	{Name: "HIMSG1", Command: "read_version", Match: func(f []string) bool {
		// 恰好 3 字段（版本 + 8 位日期 + 附加位）。不能放宽成 len>=2：
		// HBMS1 的二进制状态串 "10010000" 也是 8 位数字，会被误认成日期。
		return len(f) == 3 && strings.Contains(f[0], ".") &&
			len(f[1]) == 8 && isAllDigits(f[1]) && !isAllBinary(f[1])
	}},

	// HPV/HPVB: (AAA.A BB.B CCCCC ...) —— 形状同构，**无法自证是 PV1 还是 PV2**。
	// Command 留空：ParseData 遇到它会返回"需要命令上下文"，而不是去猜。
	{Name: "PV", Command: "", Match: techfineLooksLikePV},
}

// techfineLooksLikeHOP 判定一帧是否是 HOP（输出）形状。
//
// 提取成谓词的唯一理由是**让 HOP 与 HGRID 互斥**：两者都是"前两位带小数、
// 中间若干整数位"，区别只有 HOP 的第 8 位是电感电流（带小数）且功率位是
// 4+ 定宽数字。历史实现靠 if/else 顺序区分，顺序一改就撞车；现在两处共用
// 同一谓词（HOP 取它、HGRID 取反），互斥性由构造保证，且可被一条用例直接验证。
func techfineLooksLikeHOP(f []string) bool {
	return len(f) >= 8 && strings.Contains(f[0], ".") && strings.Contains(f[7], ".") &&
		isAllDigits(f[2]) && len(f[2]) >= 4 &&
		isAllDigits(f[3]) && len(f[3]) >= 4
}

// techfineLooksLikePV 判定一帧是否是 PV（HPV/HPVB）形状。
//
// 与形状规则表里的 PV 项共用同一判据：3 字段定宽，或 9 字段且第 4 位带小数
// （PV 的电流位）而第 8 位不带小数（那才是 HOP 的电感电流）。
// parser 用它是为了"命令上下文给错也必须失败"——否则 read_pv1 会接受输出帧。
func techfineLooksLikePV(f []string) bool {
	if len(f) < 3 || !strings.Contains(f[0], ".") || !isNumericField(f[1]) {
		return false
	}
	if len(f) == 3 {
		return isAllDigits(f[2]) && len(f[2]) >= 4
	}
	if len(f) == 9 {
		return strings.Contains(f[3], ".") && !strings.Contains(f[7], ".")
	}
	return false
}

// techfineClassifyShape 按顺序返回第一个命中的形状规则。
func techfineClassifyShape(fields []string) (*techfineShapeRule, bool) {
	for i := range techfineShapeRules {
		if techfineShapeRules[i].Match(fields) {
			return &techfineShapeRules[i], true
		}
	}
	return nil, false
}

// techfineMatchedShapeNames 返回**所有**命中的形状规则名（仅供测试断言互斥性）。
func techfineMatchedShapeNames(fields []string) []string {
	var out []string
	for i := range techfineShapeRules {
		if techfineShapeRules[i].Match(fields) {
			out = append(out, techfineShapeRules[i].Name)
		}
	}
	return out
}

func isWorkModeLetter(c byte) bool {
	switch c {
	case 'P', 'S', 'L', 'B', 'F', 'D', 'X':
		return true
	}
	return false
}

// isNumericField 判断一个字段是否是协议里的十进制数（允许前导 + / -、小数点）。
// 它比"能被 ParseFloat 解析"更严：拒绝 "NaN"、"Inf"、十六进制等怪形状。
func isNumericField(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '+' || c == '-' {
			continue
		}
		return false
	}
	return true
}
