package drivers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ControlActions moves the documented, side-effect-free V19 queries onto the
// unified Action Catalog. They remain disabled until a real BMS supplies the
// model/firmware/line evidence needed for rollout. In particular, no MOS,
// factory-mode, reset or parameter command is represented here.
func (d *JiabaidaBMSDriver) ControlActions() []ControlAction {
	actions := []ControlAction{
		jiabaidaReadAction("read_basic_info", "读取基本信息", jiabaidaReadFrame(0x03), 256, "总压、电流、容量、SOC、温度与 FET 状态"),
		jiabaidaReadAction("read_cell_voltage", "读取单体电压", jiabaidaReadFrame(0x04), 256, "各串电芯电压"),
		jiabaidaReadAction("read_hardware_version", "读取硬件版本", jiabaidaReadFrame(0x05), 256, "硬件版本字符串"),
		jiabaidaReadAction("read_comprehensive", "读取综合信息", jiabaidaReadFrame(0x0F), 256, "综合状态与单体电压"),
		jiabaidaReadAction("read_protection_count", "读取保护历史次数", jiabaidaReadFrame(0xAA), 256, "保护触发次数统计"),
	}
	// These capabilities are deliberately visible but unavailable. Their
	// schemas and evidence requirements are frozen now, while physical
	// execution remains blocked until a real BMS proves ACK + readback and the
	// node has durable bounded-plan replay protection.
	//
	// set_mos_policy shipped default-enabled once the bounded compiler,
	// verifier and readback reconciliation were implemented (default-enablement
	// principle, 2026-08-14); the other entries keep their gates because their
	// protocol steps are not yet frozen against physical responses.
	actions = append(actions,
		ControlAction{
			ID: "set_mos_policy", Version: 1, Name: "设置充放电 MOS 软件策略",
			Description: "一次提交充电/放电两个软件关闭位；bounded 基线读+写+两帧 0x0F 回读对账（FET 位或 protection_status 锁存位）",
			Semantics:   "set", Risk: "high", Enabled: true, ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4,
			Parameters: []ControlParameter{
				{Name: "charge_software_closed", Type: "boolean", Required: true},
				{Name: "discharge_software_closed", Type: "boolean", Required: true},
				{Name: "priority", Type: "string", Required: true, Enum: []string{"user", "operator"}},
			},
		},
		// 注意(2026-09-23)：这两条是 semantics="read"，而 deviceaction/definition.go
		// 的默认启用重算**只覆盖 set/reset**（其分支写作：若 Semantics 为 "set" 或
		// "reset"，则按 verifier/Verification/AvailabilityCode 重算 Enabled）。
		// read 直接沿用驱动字面量的 Enabled，因此**仅仅清空 AvailabilityCode 并不会
		// 让它们启用**，必须显式写 Enabled: true。
		// 判据由 TestJiabaidaFormerlyGuardedActionsAreEnabledAtDriverLayer 长期钉死：
		// 删掉这里的 Enabled: true 会让该测试变红（已用变异自证）。
		ControlAction{ID: "read_protection_parameters", Version: 1, Name: "读取 BMS 保护参数",
			Description: "嘉佰达 F2；受控工厂模式工作流（进厂→读→出厂）",
			Semantics:   "read", Risk: "medium", Enabled: true, ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 3},
		ControlAction{ID: "read_system_parameters", Version: 1, Name: "读取 BMS 系统参数",
			Description: "嘉佰达 F3；受控工厂模式工作流（进厂→读→出厂）",
			Semantics:   "read", Risk: "medium", Enabled: true, ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 3},
		ControlAction{ID: "bms_restart", Version: 1, Name: "重启 BMS",
			Description: "重启后必须观察离线窗口或 restart counter，不能以 ACK 判定成功",
			Semantics:   "reset", Enabled: true, Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "observation",
			AtMostOnce: true, MaxSteps: 4},
	)
	// -----------------------------------------------------------------------
	// Factory-mode parameter read/write (F2/F3) — V19 §7.11
	// -----------------------------------------------------------------------
	actions = append(actions,
		ControlAction{ID: "write_protection_parameters", Version: 1, Name: "写入 BMS 保护参数",
			Description: "F2 写：进工厂→写 53 字节参数块(含 CRC-16)→读回对账→退出(2828 初始化)",
			Semantics:   "set", Enabled: true, Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4,
			Parameters: jiabaidaF2Parameters()},
		ControlAction{ID: "write_system_parameters", Version: 1, Name: "写入 BMS 系统参数",
			Description: "F3 写：进工厂→写 52 字节参数块(含 CRC-16)→读回对账→退出(2828 初始化)",
			Semantics:   "set", Enabled: true, Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4,
			Parameters: jiabaidaF3Parameters()},
	)
	// -----------------------------------------------------------------------
	// MOS test / balance / buzzer / alarm / EDV / custom / resistance / timing
	// -----------------------------------------------------------------------
	actions = append(actions,
		// 0x0C 是 BMS 的产测/自检命令，不是可反复设置的状态开关：写 test_status
		// 触发一次测试，结果依赖外部负载，读回只用于判定 OK/NG/超时。规则编排要的是
		// 幂等的开关方法，因此这两个动作仅保留在设备详情/手动控制（diagnostic）。
		ControlAction{ID: "test_charge_mos", Version: 1, Name: "测试充电 MOS",
			Description: "0C 写 test_status=01 + 读回 0C 状态对账；测试需注入负载电流",
			Semantics:   "set", Exposure: "diagnostic", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2},
		ControlAction{ID: "test_discharge_mos", Version: 1, Name: "测试放电 MOS",
			Description: "0C 写 test_status=02 + 读回 0C 状态对账；测试需注入负载电流",
			Semantics:   "set", Exposure: "diagnostic", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2},
		ControlAction{ID: "read_test_mos_status", Version: 1, Name: "读取 MOS 测试状态",
			Description: "0C 读：返回充放电 MOS 测试状态(0 未测试/1 OK/2 NG/3 超时)",
			Semantics:   "read", Risk: "low", Enabled: true,
			TXData: []byte{0xDD, 0xA5, 0x0C, 0x00, 0xFF, 0xF4, 0x77}, ReadSize: 256, RXTimeoutMS: 1000},
		ControlAction{ID: "force_balance", Version: 1, Name: "强制均衡",
			Description: "F5 进入强制均衡模式 + 读回 0x03 均衡状态对账",
			Semantics:   "set", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2},
		ControlAction{ID: "find_car", Version: 1, Name: "寻车(蜂鸣器)",
			Description: "F1 蜂鸣器开/关(0x1801/0x1800)，最长 30S 自动关闭",
			Semantics:   "set", Enabled: true, Risk: "low", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "enabled", Type: "boolean", Required: true},
			}},
		ControlAction{ID: "clear_alarm", Version: 1, Name: "清除告警状态",
			Description: "E6 清除所有告警信息(固定数据 0x1881)",
			Semantics:   "reset", Enabled: true, Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2},
		ControlAction{ID: "auto_test_edv", Version: 1, Name: "自动测试 EDV",
			Description: "0D 设置静止时间(分钟)启动 EDV 测试；需 SOC>95%",
			Semantics:   "set", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "ack",
			AtMostOnce: true, MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "rest_minutes", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "write_custom_attributes", Version: 1, Name: "写入自定义属性",
			Description: "F0 写 3 个 uint16 自定义字段 + 读回对账",
			Semantics:   "set", Enabled: true, Risk: "low", ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "custom_1", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "custom_2", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "custom_3", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "read_custom_attributes", Version: 1, Name: "读取自定义属性",
			Description: "F0 读 3 个 uint16 自定义字段",
			Semantics:   "read", Risk: "low", Enabled: true,
			TXData: []byte{0xDD, 0xA5, 0xF0, 0x00, 0xFF, 0x10, 0x77}, ReadSize: 256, RXTimeoutMS: 1000},
		ControlAction{ID: "write_internal_resistance", Version: 1, Name: "写入电芯内阻",
			Description: "F6 写 30 串内阻(0.1mΩ 有符号) + 读回对账；协议固定 30 串，参数为 30 个标量（catalog schema 仅支持标量参数）",
			Semantics:   "set", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2,
			Parameters: jiabaidaResistanceParameters()},
		ControlAction{ID: "set_static_correction_time", Version: 1, Name: "设置静态修正时间",
			Description: "F7 写入静态修正时间(分钟)",
			Semantics:   "set", Enabled: true, Risk: "low", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "minutes", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_report_interval", Version: 1, Name: "设置上报时间间隔",
			Description: "F8 写入静态/充电/放电上报间隔(各 uint16 秒)",
			Semantics:   "set", Enabled: true, Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "static_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "charge_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "discharge_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_charge_time_window", Version: 1, Name: "设置充电时间窗",
			Description: "FA 写入充电延迟+充电时长(秒)；0=持续允许",
			Semantics:   "set", Enabled: true, Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "delay_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "duration_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_discharge_time_limit", Version: 1, Name: "设置放电时限",
			Description: "FB 写入放电使能+时限(天)；到期自动断开放电",
			Semantics:   "set", Enabled: true, Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "ack",
			AtMostOnce: true, MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "enabled", Type: "boolean", Required: true},
				{Name: "days", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "read_sn", Version: 1, Name: "读取 SN 码",
			Description: "嘉佰达 0xA2；受控工厂模式工作流（进厂→读→出厂）",
			Semantics:   "read", Risk: "medium", Enabled: true, ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 3},
		ControlAction{ID: "read_internal_resistance", Version: 1, Name: "读取电芯内阻",
			Description: "F6 读 30 串内阻(0.1mΩ 有符号)",
			Semantics:   "read", Risk: "low", Enabled: true, Verification: "ack", ExecutionShape: "single",
			TXData: jiabaidaReadFrame(0xF6), ReadSize: 256, RXTimeoutMS: 2000},
		ControlAction{ID: "write_sn", Version: 1, Name: "写入 SN 码",
			Description: "进工厂→写 SN(ASCII,≤31 字符)→退出工厂",
			Semantics:   "set", Enabled: true, Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4,
			Parameters: []ControlParameter{
				{Name: "sn", Type: "string", Required: true, MinLength: uint32Ptr(1), MaxLength: uint32Ptr(31)},
			}},
	)
	// RiskReason 是规则创建/风险确认处展示的可读原因。风险等级仍由 Risk 决定，
	// 这里只补"为什么危险"，避免前端只能显示光秃秃的"风险"。
	reasons := map[string]string{
		"set_mos_policy":              "直接控制充放电 MOS；协议一次写两位，误操作会断电或过流",
		"test_charge_mos":             "强制切换充电 MOS，需负载配合；可能短暂断开回路",
		"test_discharge_mos":          "强制切换放电 MOS，需负载配合；可能短暂断开回路",
		"bms_restart":                 "设备会重启并短暂离线，重启期间无法采集/控制",
		"force_balance":               "强制均衡会改变电芯工作状态",
		"clear_alarm":                 "会清除告警历史状态，可能掩盖真实故障",
		"auto_test_edv":               "EDV 测试要求高 SOC，会改变放电策略",
		"set_discharge_time_limit":    "到期会自动断开放电",
		"set_charge_time_window":      "会改变充电允许时间窗",
		"set_report_interval":         "会改变设备上报频率，影响数据时效与功耗",
		"write_protection_parameters": "会改写 BMS 保护参数；错误值可能导致过充/过放保护失效",
		"write_system_parameters":     "会改写 BMS 系统参数；错误值会影响容量与保护策略",
		"write_internal_resistance":   "会改写电芯内阻标定；错误值会污染均衡判断",
		"write_sn":                    "会改写设备 SN，影响资产识别",
		"read_protection_parameters":  "读取需进入工厂模式，采集可能短暂中断",
		"read_system_parameters":      "读取需进入工厂模式，采集可能短暂中断",
		"read_sn":                     "读取需进入工厂模式，采集可能短暂中断",
	}
	for i := range actions {
		if actions[i].RiskReason == "" {
			actions[i].RiskReason = reasons[actions[i].ID]
		}
		if actions[i].ID == "set_mos_policy" {
			// 规则创建只展示这四个一键预设；底层双位参数仍保留给高级/手动入口。
			// Label 明确写出"两位一起写"，避免用户误以为只改一个 MOS。
			actions[i].Presets = []map[string]any{
				{"id": "charge_off", "label": "关闭充电 MOS", "description": "同时将放电 MOS 置为导通（协议一次写两位）", "params": map[string]any{"charge_software_closed": true, "discharge_software_closed": false, "priority": "user"}},
				{"id": "charge_on", "label": "打开充电 MOS", "description": "同时将放电 MOS 置为导通（协议一次写两位）", "params": map[string]any{"charge_software_closed": false, "discharge_software_closed": false, "priority": "user"}},
				{"id": "discharge_off", "label": "关闭放电 MOS", "description": "同时将充电 MOS 置为导通（协议一次写两位）", "params": map[string]any{"charge_software_closed": false, "discharge_software_closed": true, "priority": "user"}},
				{"id": "discharge_on", "label": "打开放电 MOS", "description": "同时将充电 MOS 置为导通（协议一次写两位）", "params": map[string]any{"charge_software_closed": false, "discharge_software_closed": false, "priority": "user"}},
			}
		}
	}
	return actions
}

func uint32Ptr(v uint32) *uint32 { return &v }

// ReadSize 的两种语义（2026-11-15 实机缺陷修复，勿再混用）：
//
//   - **单步 V2（本文件的 jiabaidaReadAction / 显式 ReadSize 的单步动作）**：
//     固件把 read_size 当"**接收窗口**"而不是帧长承诺 —— bus_worker.c 对
//     channel_cmd_v2 豁免了短读丢弃规则，但 read_size==0 会被当作"不期待回包"，
//     在 bus_dma_write 之后立即 complete_control(success, 空载荷)。所以单步读
//     **必须 > 0**，取协议上界 256（与 Techfine 的做法一致），由服务端 verifier
//     裁决内容。
//
//   - **有界计划（CompileControlActionPlan 的每一步）**：固件用
//     uart_collect_response() 收集，2 处判据都是 `len >= expected` 的**下界**：
//     真实回包比声明短就永远收不满，静默窗口一到即判步骤失败（error_code
//     0x1400+step）。因此**每一步都必须声明 0**（= 行空闲定帧），由 verifier 裁决。
//     2026-11-15 实机：「MOS 开关无法控制」的根因就是读回步声明了 60，而真实 0x03
//     帧长 = 30 + 2×NTC（典型 38 B）；详见
//     docs/分析/BMS-MOS实机无法控制-代码缺陷定位-2026-11-15.md
//
// jiabaidaReadAction builds a low-risk single-step read.
//
// Enabled is true (2026-09-23): these frames are already exercised continuously
// by the telemetry poller through the very same ChannelCmdV2 single-step path,
// so exposing them as manual operations adds no new protocol risk.  A read
// performs no write, and a peripheral that does not answer yields a timeout
// (surfaced as a failed execution) rather than a corrupted BMS state.
//
// This does NOT loosen the fail-closed write gate: set/reset actions are gated
// independently in deviceaction (they require a verifier + a declared
// Verification and no AvailabilityCode).  The two factory-mode reads
// (read_protection_parameters / read_system_parameters) were enabled on
// 2026-09-23 together with the 16 writes.  They are NOT covered by that
// recomputation (semantics=="read" passes the literal Enabled straight through),
// which is exactly why each of them now carries an explicit Enabled: true.
func jiabaidaReadAction(id, name string, tx []byte, readSize uint32, description string) ControlAction {
	return ControlAction{ID: id, Version: 1, Name: name, Description: description, Semantics: "read", Risk: "low", Enabled: true,
		TXData: append([]byte(nil), tx...), ReadSize: readSize, RXTimeoutMS: 1000}
}

// CompileControlAction compiles the documented E1 MOS policy frame. The
// action is still unavailable in the catalog: this compiler exists so the
// golden vector and future bounded-plan transport can be tested without ever
// guessing bytes at the HTTP boundary.
func (d *JiabaidaBMSDriver) CompileControlAction(actionID string, params json.RawMessage) (CompiledControlStep, error) {
	if actionID != "set_mos_policy" {
		return CompiledControlStep{}, fmt.Errorf("jiabaida action %q is not parameterized", actionID)
	}
	step, err := d.compileMOSFrame(params)
	if err != nil {
		return CompiledControlStep{}, err
	}
	return step, nil
}

// compileMOSFrame builds the E1 MOS policy write step from canonical params.
func (d *JiabaidaBMSDriver) compileMOSFrame(params json.RawMessage) (CompiledControlStep, error) {
	var input struct {
		ChargeClosed    bool   `json:"charge_software_closed"`
		DischargeClosed bool   `json:"discharge_software_closed"`
		Priority        string `json:"priority"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return CompiledControlStep{}, fmt.Errorf("decode MOS policy: %w", err)
	}
	var priority byte
	switch input.Priority {
	case "user":
		priority = 0x00
	case "operator":
		priority = 0xAA
	default:
		return CompiledControlStep{}, fmt.Errorf("priority must be user or operator")
	}
	var mos byte
	if input.ChargeClosed {
		mos |= 0x01
	}
	if input.DischargeClosed {
		mos |= 0x02
	}
	frame := []byte{0xDD, 0x5A, 0xE1, 0x02, priority, mos, 0, 0, 0x77}
	checksum := jiabaidaChecksum(frame[2:6])
	binary.BigEndian.PutUint16(frame[6:8], checksum)
	return CompiledControlStep{TXData: frame, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100}, nil
}

// jiabaidaWriteFrame builds a V19 write frame: DD 5A CMD LEN DATA... CHK_H CHK_L 77.
// The checksum covers CMD + LEN + DATA (frame[2 : 4+len]).
func jiabaidaWriteFrame(cmd byte, data []byte) []byte {
	frame := make([]byte, 0, 7+len(data))
	frame = append(frame, 0xDD, 0x5A, cmd, byte(len(data)))
	frame = append(frame, data...)
	frame = append(frame, 0, 0, 0x77)
	checksum := jiabaidaChecksum(frame[2 : 4+len(data)])
	binary.BigEndian.PutUint16(frame[4+len(data):6+len(data)], checksum)
	return frame
}

// jiabaidaReadFrame builds a V19 read frame: DD A5 CMD 00 CHK_H CHK_L 77.
func jiabaidaReadFrame(cmd byte) []byte {
	frame := []byte{0xDD, 0xA5, cmd, 0x00, 0, 0, 0x77}
	checksum := jiabaidaChecksum(frame[2:4])
	binary.BigEndian.PutUint16(frame[4:6], checksum)
	return frame
}

// jiabaidaUint16Param decodes one required uint16 JSON parameter by name.
func jiabaidaUint16Param(fields map[string]json.RawMessage, name string) (uint16, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("jiabaida: missing required parameter %q", name)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q must be integer: %w", name, err)
	}
	parsed, err := strconv.ParseUint(string(number), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q out of uint16 range: %w", name, err)
	}
	return uint16(parsed), nil
}

// jiabaidaInt16Param decodes one required signed int16 JSON parameter by name
// (内阻值为 0.1mΩ 有符号, 协议允许负值).
func jiabaidaInt16Param(fields map[string]json.RawMessage, name string) (int16, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("jiabaida: missing required parameter %q", name)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q must be integer: %w", name, err)
	}
	parsed, err := strconv.ParseInt(string(number), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q out of int16 range: %w", name, err)
	}
	return int16(parsed), nil
}

// jiabaidaUint8Param decodes one required uint8 JSON parameter by name.
func jiabaidaUint8Param(fields map[string]json.RawMessage, name string) (byte, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("jiabaida: missing required parameter %q", name)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q must be integer: %w", name, err)
	}
	parsed, err := strconv.ParseUint(string(number), 10, 8)
	if err != nil {
		return 0, fmt.Errorf("jiabaida: parameter %q out of uint8 range: %w", name, err)
	}
	return byte(parsed), nil
}

// jiabaidaBoolParam decodes one required boolean JSON parameter by name.
func jiabaidaBoolParam(fields map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, fmt.Errorf("jiabaida: missing required parameter %q", name)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("jiabaida: parameter %q must be boolean: %w", name, err)
	}
	return value, nil
}

// jiabaidaCompileBlock writes every declared field of the table into block at
// its offset (Width=2 → uint16 big-endian, Width=1 → uint8).  Bytes not
// covered by the table (F3 reserved ranges, CRC tail) stay zero.
func jiabaidaCompileBlock(block []byte, fields map[string]json.RawMessage, table []jiabaidaField) error {
	for _, f := range table {
		switch f.Width {
		case 2:
			v, err := jiabaidaUint16Param(fields, f.Name)
			if err != nil {
				return err
			}
			binary.BigEndian.PutUint16(block[f.Offset:f.Offset+2], v)
		case 1:
			v, err := jiabaidaUint8Param(fields, f.Name)
			if err != nil {
				return err
			}
			block[f.Offset] = v
		default:
			return fmt.Errorf("jiabaida: field %q has invalid width %d", f.Name, f.Width)
		}
	}
	return nil
}

// compileF2Block assembles the 53-byte F2 protection-parameter block (51 field
// bytes + CRC-16).  Field layout comes from the jiabaidaF2Fields() table.
func compileF2Block(params json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, fmt.Errorf("decode F2 params: %w", err)
	}
	block := make([]byte, 53)
	if err := jiabaidaCompileBlock(block, fields, jiabaidaF2Fields()); err != nil {
		return nil, err
	}
	// CRC-16 over the 51 field bytes, appended big-endian in the last 2 bytes.
	crc := CRC16Modbus(block[:51])
	binary.BigEndian.PutUint16(block[51:53], crc)
	return block, nil
}

// compileF3Block assembles the 52-byte F3 system-parameter block (50 field
// bytes + CRC-16).  Reserved byte ranges are left zero.  Field layout comes
// from the jiabaidaF3Fields() table.
func compileF3Block(params json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, fmt.Errorf("decode F3 params: %w", err)
	}
	block := make([]byte, 52)
	if err := jiabaidaCompileBlock(block, fields, jiabaidaF3Fields()); err != nil {
		return nil, err
	}
	crc := CRC16Modbus(block[:50])
	binary.BigEndian.PutUint16(block[50:52], crc)
	return block, nil
}

// CompileControlActionPlan compiles the E1 MOS set workflow as a bounded
// plan: write the dual-bit MOS policy, then read back basic info (0x03) whose
// fet_status confirms the software-close state.  The readback step reuses the
// golden 0x03 read request; the verifier (VerifyControlAction) binds the
// final response to the originating action.
//
// **每一步的 ReadSize 必须是 0**（2026-11-15）：固件 uart_collect_response 把
// read_size 当"收满下界"（`len >= expected`），真实帧比声明短就永不满足，
// 静默窗口一到即判该步失败（error_code 0x1400+step），整条计划 FAILED。
// 嘉佰达所有 0x03/0x04/0x05/0x0C/0x0F/0xAA/0xF0/0xF6/0xA2 回包都是变长的
// （0x03 为 23 + 2×NTC），任何固定值都会在某个 NTC/串数/固件版本上失败。
// 0 = 行空闲定帧（10ms 静默），与 GetCommandTemplates 的 ReadLength=0 同一策略。
//
// bms_restart compiles the V19 §7.7 reset frame (0x0E, fixed code 0x8118)
// followed by a readback of protection history (0xAA) whose restart_count
// observes the reboot side effect.  The latter is a "readback" step so the
// batch verifier can surface restart_count to the operator; the action's
// observation semantics (offline window / uptime change) remain an operator
// concern in the UI timeline.
func (d *JiabaidaBMSDriver) CompileControlActionPlan(actionID string, params json.RawMessage) (CompiledControlPlan, error) {
	switch actionID {
	case "set_mos_policy":
		writeStep, err := d.compileMOSFrame(params)
		if err != nil {
			return CompiledControlPlan{}, err
		}
		// 四步计划（2026-11-25 生产取证重写，勿再退回两步）：
		//
		//	[0] 0x0F 基线读   —— 拿到写入前的 fet_status / protection_status
		//	[1] E1 写          —— 策略下发（一次写两位）
		//	[2] 0x0F 回读      —— 稳定窗 1200ms 后第一帧
		//	[3] 0x0F 再回读    —— 再晚 600ms，兜住"FET 位慢一拍"的设备
		//
		// 为什么回读必须是 0x0F 而不是 0x03：这台 BMS（软件版本 0x81）把 E1 当
		// **策略锁存** —— 关断立刻置 protection_status bit12(0x1000)，而 fet_status
		// 是物理 FET 状态，带载放电时可能十几个小时都不切换。0x03 帧里**没有**
		// protection_status，只读它必然把"策略已生效"判成"设备回退"，于是自动化
		// 审计与通知天天报假失败（2026-11-21~24 连续 5 次，见
		// docs/分析/自动规则误报失败-取证与修复方案-2026-11-25.md）。
		//
		// 稳定窗 1200ms 仍然保留：0x03 时代它是为了躲开"写入后 ~1s 自动恢复"的
		// 假象；现在虽然判据换成锁存位，仍然要在设备完成一次状态刷新之后再读。
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "read_baseline", Kind: "read", TXData: jiabaidaReadFrame(0x0F), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "write_mos", Kind: "write", TXData: writeStep.TXData, ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 1200},
				{ID: "readback_mos", Kind: "readback", TXData: jiabaidaReadFrame(0x0F), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 600},
				{ID: "readback_mos_confirm", Kind: "readback", TXData: jiabaidaReadFrame(0x0F), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "bms_restart":
		if string(params) != "{}" && len(params) != 0 {
			return CompiledControlPlan{}, fmt.Errorf("jiabaida bms_restart does not accept parameters")
		}
		// 协议固定向量，禁止重构: V19 §7.7 重启帧 (固定码 0x8118), checksum over [2:6].
		rebootFrame := []byte{0xDD, 0x5A, 0x0E, 0x00, 0x81, 0x18, 0, 0, 0x77}
		checksum := jiabaidaChecksum(rebootFrame[2:6])
		binary.BigEndian.PutUint16(rebootFrame[6:8], checksum)
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_reboot", Kind: "write", TXData: rebootFrame, ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_restart_count", Kind: "readback", TXData: jiabaidaReadFrame(0xAA), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "read_protection_parameters", "read_system_parameters":
		// ReadSize 一律 0（见文件顶部 plan ReadSize 说明）：0x03/0xF2/0xF3 的
		// 响应长度随 NTC/串数/固件版本变化，任何固定值都会被固件当成"收满下界"。
		cmd := byte(0xF2)
		if actionID == "read_system_parameters" {
			cmd = 0xF3
		}
		return CompiledControlPlan{
			RequiresFinally: true,
			Steps: []CompiledControlPlanStep{
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "read_params", Kind: "readback", TXData: jiabaidaReadFrame(cmd), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForRead(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "write_protection_parameters", "write_system_parameters":
		cmd := byte(0xF2)
		block, err := compileF2Block(params)
		if actionID == "write_system_parameters" {
			cmd = 0xF3
			block, err = compileF3Block(params)
		}
		if err != nil {
			return CompiledControlPlan{}, err
		}
		// Readback happens BEFORE the 0x2828 exit: the exit re-initializes
		// (applies) the parameters, so the read proves the block was stored
		// and the finally step always releases factory mode.
		return CompiledControlPlan{
			AtMostOnce: true, RequiresFinally: true,
			Steps: []CompiledControlPlanStep{
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "write_params", Kind: "write", TXData: jiabaidaWriteFrame(cmd, block), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_params", Kind: "readback", TXData: jiabaidaReadFrame(cmd), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForWrite(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "test_charge_mos", "test_discharge_mos":
		status := byte(0x01)
		if actionID == "test_discharge_mos" {
			status = 0x02
		}
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_test", Kind: "write", TXData: jiabaidaWriteFrame(0x0C, []byte{0x00, status}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_test_status", Kind: "readback", TXData: jiabaidaReadFrame(0x0C), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "force_balance":
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_force_balance", Kind: "write", TXData: jiabaidaWriteFrame(0xF5, []byte{0x00, 0x01}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "find_car":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode find_car params: %w", err)
		}
		enabled, err := jiabaidaBoolParam(fields, "enabled")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		onOff := byte(0x00)
		if enabled {
			onOff = 0x01
		}
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_find_car", Kind: "write", TXData: jiabaidaWriteFrame(0xF1, []byte{0x18, onOff}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "clear_alarm":
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_clear_alarm", Kind: "write", TXData: jiabaidaWriteFrame(0xE6, []byte{0x18, 0x81}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "auto_test_edv":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode auto_test_edv params: %w", err)
		}
		minutes, err := jiabaidaUint16Param(fields, "rest_minutes")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_edv", Kind: "write", TXData: jiabaidaWriteFrame(0x0D, []byte{byte(minutes >> 8), byte(minutes)}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "write_custom_attributes":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode write_custom_attributes params: %w", err)
		}
		data := make([]byte, 0, 6)
		for _, name := range []string{"custom_1", "custom_2", "custom_3"} {
			value, err := jiabaidaUint16Param(fields, name)
			if err != nil {
				return CompiledControlPlan{}, err
			}
			data = append(data, byte(value>>8), byte(value))
		}
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_custom", Kind: "write", TXData: jiabaidaWriteFrame(0xF0, data), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_custom", Kind: "readback", TXData: jiabaidaReadFrame(0xF0), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "write_internal_resistance":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode write_internal_resistance params: %w", err)
		}
		data := make([]byte, 60)
		for i := 0; i < 30; i++ {
			value, err := jiabaidaInt16Param(fields, fmt.Sprintf("resistance_%d", i+1))
			if err != nil {
				return CompiledControlPlan{}, err
			}
			binary.BigEndian.PutUint16(data[i*2:i*2+2], uint16(value))
		}
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_resistance", Kind: "write", TXData: jiabaidaWriteFrame(0xF6, data), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_resistance", Kind: "readback", TXData: jiabaidaReadFrame(0xF6), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 100},
			},
		}, nil
	case "set_static_correction_time":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode set_static_correction_time params: %w", err)
		}
		minutes, err := jiabaidaUint16Param(fields, "minutes")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_correction", Kind: "write", TXData: jiabaidaWriteFrame(0xF7, []byte{byte(minutes >> 8), byte(minutes)}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "set_report_interval":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode set_report_interval params: %w", err)
		}
		data := make([]byte, 0, 6)
		for _, name := range []string{"static_interval_s", "charge_interval_s", "discharge_interval_s"} {
			value, err := jiabaidaUint16Param(fields, name)
			if err != nil {
				return CompiledControlPlan{}, err
			}
			data = append(data, byte(value>>8), byte(value))
		}
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_interval", Kind: "write", TXData: jiabaidaWriteFrame(0xF8, data), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "set_charge_time_window":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode set_charge_time_window params: %w", err)
		}
		delay, err := jiabaidaUint16Param(fields, "delay_s")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		duration, err := jiabaidaUint16Param(fields, "duration_s")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_charge_window", Kind: "write", TXData: jiabaidaWriteFrame(0xFA, []byte{byte(delay >> 8), byte(delay), byte(duration >> 8), byte(duration)}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "set_discharge_time_limit":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode set_discharge_time_limit params: %w", err)
		}
		enabled, err := jiabaidaBoolParam(fields, "enabled")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		days, err := jiabaidaUint16Param(fields, "days")
		if err != nil {
			return CompiledControlPlan{}, err
		}
		enable := byte(0x00)
		if enabled {
			enable = 0x01
		}
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_discharge_limit", Kind: "write", TXData: jiabaidaWriteFrame(0xFB, []byte{enable, byte(days >> 8), byte(days)}), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "read_sn":
		return CompiledControlPlan{
			RequiresFinally: true,
			Steps: []CompiledControlPlanStep{
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "read_sn", Kind: "readback", TXData: jiabaidaReadFrame(0xA2), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForRead(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "write_sn":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("decode write_sn params: %w", err)
		}
		rawSN, ok := fields["sn"]
		if !ok {
			return CompiledControlPlan{}, fmt.Errorf("jiabaida: missing required parameter \"sn\"")
		}
		var sn string
		if err := json.Unmarshal(rawSN, &sn); err != nil {
			return CompiledControlPlan{}, fmt.Errorf("jiabaida: sn must be string: %w", err)
		}
		if len(sn) < 1 || len(sn) > 31 {
			return CompiledControlPlan{}, fmt.Errorf("jiabaida: sn length must be 1..31, got %d", len(sn))
		}
		for _, c := range []byte(sn) {
			if c < 0x20 || c > 0x7E {
				return CompiledControlPlan{}, fmt.Errorf("jiabaida: sn must be printable ASCII")
			}
		}
		// SN write uses the 0xA2 command byte by read/write symmetry with
		// parse0xA2; the OCR original does not state the write command
		// explicitly, so this stays gated on real-device evidence.
		data := append([]byte{byte(len(sn))}, []byte(sn)...)
		return CompiledControlPlan{
			AtMostOnce: true, RequiresFinally: true,
			Steps: []CompiledControlPlanStep{
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "write_sn", Kind: "write", TXData: jiabaidaWriteFrame(0xA2, data), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_sn", Kind: "readback", TXData: jiabaidaReadFrame(0xA2), ReadSize: 0, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForWrite(), ReadSize: 0, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	default:
		return CompiledControlPlan{}, fmt.Errorf("jiabaida action %q has no bounded plan", actionID)
	}
}

// VerifyControlAction binds a successful response to its originating query.
// ParseData alone accepts multiple V19 read commands, so without this check a
// wrong-device or stale response could be stored under the wrong Action.
func (d *JiabaidaBMSDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error) {
	if actionID == "set_mos_policy" {
		// 有界计划固定四步（2026-11-25 生产取证修复）：
		//   [0] 0x0F 基线读   [1] E1 写 ACK   [2] 0x0F 回读   [3] 0x0F 再回读
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, err
		}
		if len(steps) != 4 {
			return nil, fmt.Errorf("jiabaida set_mos_policy requires four verified responses (baseline, ack, two readbacks), got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[1], 0xE1); err != nil {
			return nil, fmt.Errorf("jiabaida set_mos_policy ACK: %w", err)
		}
		var input struct {
			ChargeClosed    bool `json:"charge_software_closed"`
			DischargeClosed bool `json:"discharge_software_closed"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("jiabaida set_mos_policy decode MOS policy: %w", err)
		}
		wantChargeOpen := uint8(0)
		if !input.ChargeClosed {
			wantChargeOpen = 1
		}
		wantDischargeOpen := uint8(0)
		if !input.DischargeClosed {
			wantDischargeOpen = 1
		}
		// 基线只进诊断文案，不参与判据：幂等重复关断时锁存位本来就在，仍应判成功。
		var baseline jiabaidaMOSState
		if samples, parseErr := d.ParseData(steps[0]); parseErr == nil {
			baseline = jiabaidaMOSStateFrom(samples)
		}
		var mismatches []string
		for _, index := range []int{2, 3} {
			samples, parseErr := d.ParseData(steps[index])
			if parseErr != nil {
				mismatches = append(mismatches, fmt.Sprintf("readback#%d 解析失败: %v", index-1, parseErr))
				continue
			}
			state := jiabaidaMOSStateFrom(samples)
			if ok, _ := state.matches(wantChargeOpen, wantDischargeOpen); ok {
				return append([]SensorData{{Name: "mos_ack", Value: 1, Unit: "ack"}}, jiabaidaMOSEvidence(state)...), nil
			}
			mismatches = append(mismatches, fmt.Sprintf("readback#%d %s", index-1, state.describe()))
		}
		return nil, fmt.Errorf("jiabaida set_mos_policy readback mismatch: want charge_open=%d discharge_open=%d (protection_status bit12=0x1000 = 软件关断已锁存); baseline %s; %s",
			wantChargeOpen, wantDischargeOpen, baseline.describe(), strings.Join(mismatches, "; "))
	}
	if actionID == "bms_restart" {
		// Step-count envelope: step 1 = 0x0E reset ACK (DD 0E 00 00 CRC 77),
		// step 2 = 0xAA protection history readback whose restart_count is
		// surfaced for offline-window/uptime reconciliation.
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, err
		}
		if len(steps) != 2 {
			return nil, fmt.Errorf("jiabaida bms_restart requires two verified responses, got %d", len(steps))
		}
		ack := steps[0]
		if err := jiabaidaExpectZeroAck(ack, 0x0E); err != nil {
			return nil, fmt.Errorf("jiabaida bms_restart ACK: %w", err)
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, fmt.Errorf("jiabaida bms_restart readback: %w", err)
		}
		return append([]SensorData{{Name: "reboot_ack", Value: 1, Unit: "ack"}}, readback...), nil
	}
	if result, handled, err := d.verifyExtendedBatchActions(actionID, params, raw); handled {
		return result, err
	}
	if string(params) != "{}" {
		return nil, fmt.Errorf("jiabaida action %q does not accept parameters", actionID)
	}
	// Single-step reads: every enabled manual read must appear here or the
	// command reaches the device and is answered, yet verification reports
	// "unknown control action" and the execution fails after a real round-trip.
	// Keep this table in lockstep with jiabaidaReadAction/ControlActions.
	expected, ok := map[string]byte{
		"read_basic_info": 0x03, "read_cell_voltage": 0x04, "read_hardware_version": 0x05,
		"read_comprehensive": 0x0F, "read_protection_count": 0xAA,
		"read_test_mos_status": 0x0C, "read_custom_attributes": 0xF0, "read_internal_resistance": 0xF6,
	}[actionID]
	if !ok {
		return nil, fmt.Errorf("unknown jiabaida control action %q", actionID)
	}
	if len(raw) < 2 || raw[0] != 0xDD || raw[1] != expected {
		return nil, fmt.Errorf("jiabaida action %q received unexpected response command", actionID)
	}
	return d.ParseData(raw)
}

// jiabaidaMOSState 是一次 0x0F 回读里与 MOS 策略判定有关的两个位域。
//
// 为什么分开记录 known：0x0F 一定同时带 fet_status 与 protection_status，但历史
// 0x03 帧只有 fet_status。缺字段时必须能说"没读到"，而不是把缺失当成读到 0 位。
type jiabaidaMOSState struct {
	fet             uint8
	fetKnown        bool
	protection      uint16
	protectionKnown bool
}

// jiabaidaMOSStateFrom projects a parsed 0x0F/0x03 frame onto the two bit fields.
func jiabaidaMOSStateFrom(samples []SensorData) jiabaidaMOSState {
	var state jiabaidaMOSState
	for i := range samples {
		switch samples[i].Name {
		case "fet_status":
			state.fet = uint8(samples[i].Value)
			state.fetKnown = true
		case "protection_status":
			state.protection = uint16(samples[i].Value)
			state.protectionKnown = true
		}
	}
	return state
}

// matches 判定一次回读是否构成"策略已生效"的证据；第二个返回值是判定理由
// （成功时说明靠哪条证据过的，失败时说明缺什么）。
//
//   - 关断（wantChargeOpen=0）：充电 FET 位已清零 **或** 保护锁存位
//     protection_status bit12(0x1000) 已置位，任一即可。前者是物理断开，后者是
//     BMS 接受了软件关断 —— 这台固件在带载放电时只锁存、不立刻切换物理 FET
//     （2026-11-21~24 生产实测 fet_status 保持 0x03 超过 12 小时，而充电确实被
//     阻断），只认前者就会把"已生效"判成"回退"。
//   - 打开（wantChargeOpen=1）：两条都要满足 —— 锁存位已清零，且充电 FET 位为 1。
//     只满足一条不足以证明"真的能充上电"。
//
// discharge 位在两种请求下都必须等于请求值：协议一次写两位，放电位不匹配说明
// 写入的字节或设备状态有问题。
func (s jiabaidaMOSState) matches(wantChargeOpen, wantDischargeOpen uint8) (bool, string) {
	if !s.fetKnown {
		return false, "回包里没有 fet_status"
	}
	if got := (s.fet >> 1) & 0x01; got != wantDischargeOpen {
		return false, fmt.Sprintf("discharge=%d(want %d)", got, wantDischargeOpen)
	}
	charge := s.fet & 0x01
	latch := s.protectionKnown && s.protection&0x1000 != 0
	if wantChargeOpen == 0 {
		if charge == 0 {
			return true, "充电 FET 位已清零（物理关断）"
		}
		if latch {
			return true, "protection_status bit12(0x1000) 已锁存（软件关断已生效，物理 FET 随负载延后切换）"
		}
		return false, fmt.Sprintf("charge=%d(want 0)，且保护锁存位未置位", charge)
	}
	if charge != 1 {
		return false, fmt.Sprintf("charge=%d(want 1)", charge)
	}
	if latch {
		return false, "充电 FET 位已为 1，但 protection_status bit12(0x1000) 仍锁存（软件关断未解除）"
	}
	return true, "保护锁存位已清零且充电 FET 位为 1"
}

// describe 是错误文案里的单行投影：一行就能看出设备到底报了什么。
func (s jiabaidaMOSState) describe() string {
	if !s.fetKnown {
		return "fet_status 缺失"
	}
	out := fmt.Sprintf("fet_status=0x%02X charge=%d discharge=%d", s.fet, s.fet&0x01, (s.fet>>1)&0x01)
	if s.protectionKnown {
		out += fmt.Sprintf(" protection_status=0x%04X", s.protection)
	} else {
		out += " protection_status=缺失"
	}
	return out
}

// jiabaidaMOSEvidence 只保留判定用到的两个字段。整帧 0x0F 含 16 串电压与温度，
// 全塞进 verified_result_json 会把一条策略执行记录变成几十行遥测噪音。
func jiabaidaMOSEvidence(state jiabaidaMOSState) []SensorData {
	evidence := make([]SensorData, 0, 2)
	if state.fetKnown {
		evidence = append(evidence, SensorData{Name: "fet_status", Value: float64(state.fet), Unit: "bitmask"})
	}
	if state.protectionKnown {
		evidence = append(evidence, SensorData{Name: "protection_status", Value: float64(state.protection), Unit: "bitmask"})
	}
	return evidence
}

// jiabaidaExpectZeroAck asserts that a bounded-plan step response is the
// zero-length write ACK for cmd: DD CMD 00 00 CHK_H CHK_L 77.  Factory enter
// (cmd 0x00), factory exit (cmd 0x01) and every documented setter share this
// shape, so one helper binds all of them.
func jiabaidaExpectZeroAck(step []byte, cmd byte) error {
	if len(step) < 7 || step[0] != 0xDD || step[1] != cmd || step[2] != 0x00 || step[3] != 0x00 || step[6] != 0x77 {
		return fmt.Errorf("expected ACK for cmd 0x%02X, got %X", cmd, step)
	}
	if !verifyJiabaidaChecksum(step) {
		return fmt.Errorf("expected ACK for cmd 0x%02X with valid checksum, got %X", cmd, step)
	}
	return nil
}

// jiabaidaResponseData validates a response frame for cmd and returns its
// DATA section (after 0xDD CMD STATUS LEN, before checksum/0x77).  Used for
// byte-level readback reconciliation where ParseData's field projection is
// not exhaustive enough to prove the written block was stored.
func jiabaidaResponseData(raw []byte, cmd byte) ([]byte, error) {
	if len(raw) < 7 || raw[0] != 0xDD || raw[1] != cmd {
		return nil, fmt.Errorf("expected response for cmd 0x%02X, got %X", cmd, raw)
	}
	length := int(raw[3])
	if len(raw) < 4+length+3 || raw[4+length+2] != 0x77 {
		return nil, fmt.Errorf("response frame for cmd 0x%02X malformed", cmd)
	}
	if !verifyJiabaidaChecksum(raw) {
		return nil, fmt.Errorf("response checksum mismatch for cmd 0x%02X", cmd)
	}
	return raw[4 : 4+length], nil
}

// verifyExtendedBatchActions reconciles the bounded batch responses of the
// V19 write/factory-mode workflows added on top of the original MOS/restart
// actions.  It returns handled=false for action IDs it does not own so the
// legacy read-action verifier keeps its behavior.
func (d *JiabaidaBMSDriver) verifyExtendedBatchActions(actionID string, params json.RawMessage, raw []byte) ([]SensorData, bool, error) {
	switch actionID {
	case "read_protection_parameters", "read_system_parameters":
		cmd := byte(0xF2)
		if actionID == "read_system_parameters" {
			cmd = 0xF3
		}
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 3 {
			return nil, true, fmt.Errorf("jiabaida %s requires three verified responses, got %d", actionID, len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0x00); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s enter factory: %w", actionID, err)
		}
		if _, err := jiabaidaResponseData(steps[1], cmd); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %w", actionID, err)
		}
		fields, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %w", actionID, err)
		}
		if err := jiabaidaExpectZeroAck(steps[2], 0x01); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s exit factory: %w", actionID, err)
		}
		return fields, true, nil
	case "write_protection_parameters", "write_system_parameters":
		cmd := byte(0xF2)
		wantBlock, err := compileF2Block(params)
		if actionID == "write_system_parameters" {
			cmd = 0xF3
			wantBlock, err = compileF3Block(params)
		}
		if err != nil {
			return nil, true, err
		}
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 4 {
			return nil, true, fmt.Errorf("jiabaida %s requires four verified responses, got %d", actionID, len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0x00); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s enter factory: %w", actionID, err)
		}
		if err := jiabaidaExpectZeroAck(steps[1], cmd); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s write: %w", actionID, err)
		}
		gotData, err := jiabaidaResponseData(steps[2], cmd)
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %w", actionID, err)
		}
		// Reconcile only the declared field bytes: F3 has reserved ranges
		// (16-19, 32-47) that a real BMS may legitimately return nonzero, and
		// CRC bytes are excluded because the BMS may recompute them.  A
		// mismatch on any declared field still fails hard.
		spans := jiabaidaF2FieldSpans()
		if actionID == "write_system_parameters" {
			spans = jiabaidaF3FieldSpans()
		}
		if !jiabaidaEqualOnSpans(gotData, wantBlock, spans) {
			return nil, true, fmt.Errorf("jiabaida %s readback does not match written block (declared-field mismatch)", actionID)
		}
		if err := jiabaidaExpectZeroAck(steps[3], 0x01); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s exit factory: %w", actionID, err)
		}
		readback, err := d.ParseData(steps[2])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback projection: %w", actionID, err)
		}
		return append([]SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	case "test_charge_mos", "test_discharge_mos":
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 2 {
			return nil, true, fmt.Errorf("jiabaida %s requires two verified responses, got %d", actionID, len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0x0C); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s write: %w", actionID, err)
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %w", actionID, err)
		}
		// The readback must be RECONCILED, not merely parsed.  parse0x0C yields
		// the two MOS test states (0=untested, 1=OK, 2=NG, 3=timeout); a
		// parse-only check accepted a device reporting NG or timeout as success,
		// so a failed MOS test looked like a passing one.  The MOS under test
		// must report exactly 1.
		wantName := "charge_mos_test_status"
		if actionID == "test_discharge_mos" {
			wantName = "discharge_mos_test_status"
		}
		var got *float64
		for i := range readback {
			if readback[i].Name == wantName {
				v := readback[i].Value
				got = &v
				break
			}
		}
		if got == nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %s missing from response", actionID, wantName)
		}
		if *got != 1 {
			return nil, true, fmt.Errorf("jiabaida %s readback mismatch: %s=%.0f want 1 (0=untested 1=OK 2=NG 3=timeout)", actionID, wantName, *got)
		}
		return append([]SensorData{{Name: "test_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	case "force_balance", "find_car", "clear_alarm", "auto_test_edv",
		"set_static_correction_time", "set_report_interval", "set_charge_time_window", "set_discharge_time_limit":
		ackCmd := map[string]byte{
			"force_balance": 0xF5, "find_car": 0xF1, "clear_alarm": 0xE6, "auto_test_edv": 0x0D,
			"set_static_correction_time": 0xF7, "set_report_interval": 0xF8,
			"set_charge_time_window": 0xFA, "set_discharge_time_limit": 0xFB,
		}[actionID]
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 2 {
			return nil, true, fmt.Errorf("jiabaida %s requires two verified responses, got %d", actionID, len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], ackCmd); err != nil {
			return nil, true, fmt.Errorf("jiabaida %s write: %w", actionID, err)
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida %s readback: %w", actionID, err)
		}
		return append([]SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	case "read_internal_resistance":
		// 读回的名字必须与 write_internal_resistance 的表单字段一致（resistance_N），
		// 否则前端「读取」回填对不上（parse0xF6 原始名是 cell_resistance_N）。
		sensors, err := d.ParseData(raw)
		if err != nil {
			return nil, true, err
		}
		for i := range sensors {
			sensors[i].Name = strings.Replace(sensors[i].Name, "cell_resistance_", "resistance_", 1)
		}
		return sensors, true, nil
	case "read_sn":
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 3 {
			return nil, true, fmt.Errorf("jiabaida read_sn requires three verified responses, got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0x00); err != nil {
			return nil, true, fmt.Errorf("jiabaida read_sn enter factory: %w", err)
		}
		if _, err := jiabaidaResponseData(steps[1], 0xA2); err != nil {
			return nil, true, fmt.Errorf("jiabaida read_sn readback: %w", err)
		}
		sensors, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida read_sn readback: %w", err)
		}
		// 字段名对齐 write_sn 的 "sn"（parse0xA2 原始名是 serial_number）。
		for i := range sensors {
			if sensors[i].Name == "serial_number" {
				sensors[i].Name = "sn"
			}
		}
		return sensors, true, nil
	case "write_custom_attributes":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, true, fmt.Errorf("decode write_custom_attributes params: %w", err)
		}
		want := make([]byte, 0, 6)
		for _, name := range []string{"custom_1", "custom_2", "custom_3"} {
			value, err := jiabaidaUint16Param(fields, name)
			if err != nil {
				return nil, true, err
			}
			want = append(want, byte(value>>8), byte(value))
		}
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 2 {
			return nil, true, fmt.Errorf("jiabaida write_custom_attributes requires two verified responses, got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0xF0); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_custom_attributes write: %w", err)
		}
		gotData, err := jiabaidaResponseData(steps[1], 0xF0)
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida write_custom_attributes readback: %w", err)
		}
		if !bytes.Equal(gotData, want) {
			return nil, true, fmt.Errorf("jiabaida write_custom_attributes readback does not match written values")
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, err
		}
		return append([]SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	case "write_internal_resistance":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, true, fmt.Errorf("decode write_internal_resistance params: %w", err)
		}
		want := make([]byte, 60)
		for i := 0; i < 30; i++ {
			value, err := jiabaidaInt16Param(fields, fmt.Sprintf("resistance_%d", i+1))
			if err != nil {
				return nil, true, err
			}
			binary.BigEndian.PutUint16(want[i*2:i*2+2], uint16(value))
		}
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 2 {
			return nil, true, fmt.Errorf("jiabaida write_internal_resistance requires two verified responses, got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0xF6); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_internal_resistance write: %w", err)
		}
		gotData, err := jiabaidaResponseData(steps[1], 0xF6)
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida write_internal_resistance readback: %w", err)
		}
		if !bytes.Equal(gotData, want) {
			return nil, true, fmt.Errorf("jiabaida write_internal_resistance readback does not match written values")
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, true, err
		}
		return append([]SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	case "write_sn":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, true, fmt.Errorf("decode write_sn params: %w", err)
		}
		var sn string
		if err := json.Unmarshal(fields["sn"], &sn); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_sn: sn must be string: %w", err)
		}
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, true, err
		}
		if len(steps) != 4 {
			return nil, true, fmt.Errorf("jiabaida write_sn requires four verified responses, got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0x00); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_sn enter factory: %w", err)
		}
		if err := jiabaidaExpectZeroAck(steps[1], 0xA2); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_sn write: %w", err)
		}
		readback, err := d.ParseData(steps[2])
		if err != nil {
			return nil, true, fmt.Errorf("jiabaida write_sn readback: %w", err)
		}
		gotSN := ""
		for _, f := range readback {
			if f.Name == "serial_number" {
				gotSN = f.StringValue
			}
		}
		if gotSN != sn {
			return nil, true, fmt.Errorf("jiabaida write_sn readback %q does not match written %q", gotSN, sn)
		}
		if err := jiabaidaExpectZeroAck(steps[3], 0x01); err != nil {
			return nil, true, fmt.Errorf("jiabaida write_sn exit factory: %w", err)
		}
		return append([]SensorData{{Name: "write_ack", Value: 1, Unit: "ack"}}, readback...), true, nil
	}
	return nil, false, nil
}

// The bounded-plan step-count envelope is decoded by the package-level
// decodeBatchPlanEnvelope (see batch_envelope.go — shared with every driver,
// not Jiabaida-specific).

// ============================================================================
// Factory mode command helpers
// ============================================================================

// FactoryModeEnterCmd returns the command to enter factory mode.
// DD 5A 00 02 56 78 FF 30 77
// 协议固定向量，禁止重构 (含特殊 0x5678 进入码).
func FactoryModeEnterCmd() []byte {
	return []byte{0xDD, 0x5A, 0x00, 0x02, 0x56, 0x78, 0xFF, 0x30, 0x77}
}

// FactoryModeExitForRead returns the command to exit factory mode (for read, no init).
// DD 5A 01 02 00 00 FF FD 77
// 协议固定向量，禁止重构.
func FactoryModeExitForRead() []byte {
	return []byte{0xDD, 0x5A, 0x01, 0x02, 0x00, 0x00, 0xFF, 0xFD, 0x77}
}

// FactoryModeExitForWrite returns the command to exit factory mode with parameter init.
// DD 5A 01 02 28 28 FF AD 77
// 协议固定向量，禁止重构 (含特殊 0x2828 初始化码).
func FactoryModeExitForWrite() []byte {
	return []byte{0xDD, 0x5A, 0x01, 0x02, 0x28, 0x28, 0xFF, 0xAD, 0x77}
}

// CRC16Modbus computes CRC-16 (Modbus) with polynomial 0xA001, used for 0xF2/0xF3 parameter blocks.
func CRC16Modbus(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// jiabaidaReadFrameHex is the hex-encoded form of jiabaidaReadFrame(cmd),
// used by GetCommandTemplates so the golden read vectors live in exactly one
// place (the frame builder) instead of hand-written hex literals.
func jiabaidaReadFrameHex(cmd byte) string {
	return hex.EncodeToString(jiabaidaReadFrame(cmd))
}

// GetCommandTemplates exposes only schedulable, side-effect-free polling
// commands.  Legacy 0xE1 MOS frames used to be returned here as one-shot
// templates, which let the old driver-command API advertise an unaudited
// physical-write path.  BMS writes must be represented by a verified Action
// Catalog definition instead, and none is enabled until real-device evidence
// covers the two MOS bits, priority, ACK and readback.
//
// ReadLength MUST stay 0 for every command below (2026-10-02 real-device fix).
//
// A Jiabaida response is VARIABLE length: the frame is
// DD CMD STATUS LEN DATA... CHK_H CHK_L 77 with LEN = 23 + 2*NTC for 0x03, so
// the same command yields a different byte count per pack (NTC count varies).
// ReadLength is not a "suggested size" on the device side -- the firmware treats
// it as a FLOOR: bus_worker.c emit_ready_stream_chunks() withholds the response
// until read_size bytes have accumulated, and complete_idle_response() then
// discards the whole reply as a short read, reporting error_code=3 with an empty
// raw payload.  Every real 0x03/0x04/0x05/0x0F/0xAA reply is shorter than the
// values that used to sit here, so the field silently discarded 100% of the
// data while the device logged "events=N completions=N" -- the sensor WAS
// answering and the firmware was throwing the answer away.
//
// 0 is the documented opt-out: sender_snapshot.go omits field 3 when
// ReadLength == 0, and bus_rx_boundary.h then falls back to the
// protocol-neutral 10ms idle gap (UART_IDLE_THRESHOLD_US) to frame each reply.
// At 9600 8N1 the intra-frame byte gap (~1ms) is far below that threshold while
// the inter-frame gap is far above it, so idle framing recovers exactly the
// bytes the BMS sent, at the length it actually sent them.
//
// Do not "restore" a concrete length here: any non-zero value reintroduces the
// silent-discard bug for every pack whose NTC/cell count differs.
func (d *JiabaidaBMSDriver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_basic_info", Name: "读取基本信息", Type: "read",
			CmdByte: 0x03, WriteData: jiabaidaReadFrameHex(0x03),
			ReadLength: 0, DelayMs: 100, IntervalMs: 5000, Schedulable: true,
			Description: "总电压、电流、剩余容量、SOC、温度等",
		},
		{
			ID: "read_cell_voltage", Name: "读取单体电压", Type: "read",
			CmdByte: 0x04, WriteData: jiabaidaReadFrameHex(0x04),
			ReadLength: 0, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "每串电芯电压、最高/最低/平均",
		},
		{
			ID: "read_hardware_version", Name: "读取硬件版本", Type: "read",
			CmdByte: 0x05, WriteData: jiabaidaReadFrameHex(0x05),
			ReadLength: 0, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "硬件版本字符串",
		},
		{
			ID: "read_comprehensive", Name: "读取综合信息", Type: "read",
			CmdByte: 0x0F, WriteData: jiabaidaReadFrameHex(0x0F),
			ReadLength: 0, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "0x03超集：含单体电压、均衡状态、运行时间",
		},
		{
			ID: "read_protection_count", Name: "读取保护历史次数", Type: "read",
			CmdByte: 0xAA, WriteData: jiabaidaReadFrameHex(0xAA),
			ReadLength: 0, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "12种保护触发次数统计",
		},
	}
}
