package drivers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// ControlActions moves the documented, side-effect-free V19 queries onto the
// unified Action Catalog. They remain disabled until a real BMS supplies the
// model/firmware/line evidence needed for rollout. In particular, no MOS,
// factory-mode, reset or parameter command is represented here.
func (d *JiabaidaBMSDriver) ControlActions() []ControlAction {
	actions := []ControlAction{
		jiabaidaReadAction("read_basic_info", "读取基本信息", jiabaidaReadFrame(0x03), 60, "总压、电流、容量、SOC、温度与 FET 状态"),
		jiabaidaReadAction("read_cell_voltage", "读取单体电压", jiabaidaReadFrame(0x04), 50, "各串电芯电压"),
		jiabaidaReadAction("read_hardware_version", "读取硬件版本", jiabaidaReadFrame(0x05), 40, "硬件版本字符串"),
		jiabaidaReadAction("read_comprehensive", "读取综合信息", jiabaidaReadFrame(0x0F), 100, "综合状态与单体电压"),
		jiabaidaReadAction("read_protection_count", "读取保护历史次数", jiabaidaReadFrame(0xAA), 40, "保护触发次数统计"),
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
			Description: "一次提交充电/放电两个软件关闭位；bounded 写+读回 fet_status 对账",
			Semantics:   "set", Risk: "high", Enabled: true, ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2,
			Parameters: []ControlParameter{
				{Name: "charge_software_closed", Type: "boolean", Required: true},
				{Name: "discharge_software_closed", Type: "boolean", Required: true},
				{Name: "priority", Type: "string", Required: true, Enum: []string{"user", "operator"}},
			},
		},
		ControlAction{ID: "read_protection_parameters", Version: 1, Name: "读取 BMS 保护参数",
			Description: "嘉佰达 F2；需要受控工厂模式工作流，当前仅登记能力",
			Semantics:   "read", Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 3, AvailabilityCode: "protocol_unverified", AvailabilityReason: "F2 工厂模式步骤与实机响应尚未冻结"},
		ControlAction{ID: "read_system_parameters", Version: 1, Name: "读取 BMS 系统参数",
			Description: "嘉佰达 F3；需要受控工厂模式工作流，当前仅登记能力",
			Semantics:   "read", Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 3, AvailabilityCode: "protocol_unverified", AvailabilityReason: "F3 工厂模式步骤与实机响应尚未冻结"},
		ControlAction{ID: "bms_restart", Version: 1, Name: "重启 BMS",
			Description: "重启后必须观察离线窗口或 restart counter，不能以 ACK 判定成功",
			Semantics:   "reset", Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "observation",
			AtMostOnce: true, MaxSteps: 4, AvailabilityCode: "protocol_unverified", AvailabilityReason: "未冻结 BMS 重启帧及离线/启动观测证据"},
	)
	// -----------------------------------------------------------------------
	// Factory-mode parameter read/write (F2/F3) — V19 §7.11
	// -----------------------------------------------------------------------
	actions = append(actions,
		ControlAction{ID: "write_protection_parameters", Version: 1, Name: "写入 BMS 保护参数",
			Description: "F2 写：进工厂→写 53 字节参数块(含 CRC-16)→读回对账→退出(2828 初始化)",
			Semantics:   "set", Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F2 写帧 CRC 覆盖范围及真机读回黄金向量未冻结",
			Parameters:         jiabaidaF2Parameters()},
		ControlAction{ID: "write_system_parameters", Version: 1, Name: "写入 BMS 系统参数",
			Description: "F3 写：进工厂→写 52 字节参数块(含 CRC-16)→读回对账→退出(2828 初始化)",
			Semantics:   "set", Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F3 写帧 CRC 覆盖范围及真机读回黄金向量未冻结",
			Parameters:         jiabaidaF3Parameters()},
	)
	// -----------------------------------------------------------------------
	// MOS test / balance / buzzer / alarm / EDV / custom / resistance / timing
	// -----------------------------------------------------------------------
	actions = append(actions,
		ControlAction{ID: "test_charge_mos", Version: 1, Name: "测试充电 MOS",
			Description: "0C 写 test_status=01 + 读回 0C 状态对账；测试需注入负载电流",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "0C 测试帧真机响应未冻结"},
		ControlAction{ID: "test_discharge_mos", Version: 1, Name: "测试放电 MOS",
			Description: "0C 写 test_status=02 + 读回 0C 状态对账；测试需注入负载电流",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "0C 测试帧真机响应未冻结"},
		ControlAction{ID: "read_test_mos_status", Version: 1, Name: "读取 MOS 测试状态",
			Description: "0C 读：返回充放电 MOS 测试状态(0 未测试/1 OK/2 NG/3 超时)",
			Semantics:   "read", Risk: "low", Enabled: false,
			TXData: []byte{0xDD, 0xA5, 0x0C, 0x00, 0xFF, 0xF4, 0x77}, ReadSize: 9, RXTimeoutMS: 1000},
		ControlAction{ID: "force_balance", Version: 1, Name: "强制均衡",
			Description: "F5 进入强制均衡模式 + 读回 0x03 均衡状态对账",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F5 强制均衡真机响应未冻结"},
		ControlAction{ID: "find_car", Version: 1, Name: "寻车(蜂鸣器)",
			Description: "F1 蜂鸣器开/关(0x1801/0x1800)，最长 30S 自动关闭",
			Semantics:   "set", Risk: "low", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F1 寻车帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "enabled", Type: "boolean", Required: true},
			}},
		ControlAction{ID: "clear_alarm", Version: 1, Name: "清除告警状态",
			Description: "E6 清除所有告警信息(固定数据 0x1881)",
			Semantics:   "reset", Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "E6 清告警帧真机响应未冻结"},
		ControlAction{ID: "auto_test_edv", Version: 1, Name: "自动测试 EDV",
			Description: "0D 设置静止时间(分钟)启动 EDV 测试；需 SOC>95%",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "ack",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "0D EDV 测试帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "rest_minutes", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "write_custom_attributes", Version: 1, Name: "写入自定义属性",
			Description: "F0 写 3 个 uint16 自定义字段 + 读回对账",
			Semantics:   "set", Risk: "low", ExecutionShape: "bounded_sequence", Verification: "readback",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F0 自定义属性真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "custom_1", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "custom_2", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "custom_3", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "read_custom_attributes", Version: 1, Name: "读取自定义属性",
			Description: "F0 读 3 个 uint16 自定义字段",
			Semantics:   "read", Risk: "low", Enabled: false,
			TXData: []byte{0xDD, 0xA5, 0xF0, 0x00, 0xFF, 0x10, 0x77}, ReadSize: 13, RXTimeoutMS: 1000},
		ControlAction{ID: "write_internal_resistance", Version: 1, Name: "写入电芯内阻",
			Description: "F6 写 30 串内阻(0.1mΩ 有符号) + 读回对账；协议固定 30 串，参数为 30 个标量（catalog schema 仅支持标量参数）",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F6 写内阻帧真机响应未冻结",
			Parameters:         jiabaidaResistanceParameters()},
		ControlAction{ID: "set_static_correction_time", Version: 1, Name: "设置静态修正时间",
			Description: "F7 写入静态修正时间(分钟)",
			Semantics:   "set", Risk: "low", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F7 修正时间帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "minutes", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_report_interval", Version: 1, Name: "设置上报时间间隔",
			Description: "F8 写入静态/充电/放电上报间隔(各 uint16 秒)",
			Semantics:   "set", Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "F8 上报间隔帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "static_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "charge_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "discharge_interval_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_charge_time_window", Version: 1, Name: "设置充电时间窗",
			Description: "FA 写入充电延迟+充电时长(秒)；0=持续允许",
			Semantics:   "set", Risk: "medium", ExecutionShape: "bounded_sequence", Verification: "ack",
			MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "FA 充电时间窗帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "delay_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
				{Name: "duration_s", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "set_discharge_time_limit", Version: 1, Name: "设置放电时限",
			Description: "FB 写入放电使能+时限(天)；到期自动断开放电",
			Semantics:   "set", Risk: "critical", ExecutionShape: "bounded_sequence", Verification: "ack",
			AtMostOnce: true, MaxSteps: 2, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "FB 放电时限帧真机响应未冻结；远程断电语义需产品审批",
			Parameters: []ControlParameter{
				{Name: "enabled", Type: "boolean", Required: true},
				{Name: "days", Type: "integer", Required: true, Minimum: floatPtr(0), Maximum: floatPtr(65535)},
			}},
		ControlAction{ID: "write_sn", Version: 1, Name: "写入 SN 码",
			Description: "进工厂→写 SN(ASCII,≤31 字符)→退出工厂",
			Semantics:   "set", Risk: "high", ExecutionShape: "bounded_sequence", Verification: "readback",
			AtMostOnce: true, MaxSteps: 4, AvailabilityCode: "protocol_unverified",
			AvailabilityReason: "SN 写帧真机响应未冻结",
			Parameters: []ControlParameter{
				{Name: "sn", Type: "string", Required: true, MinLength: uint32Ptr(1), MaxLength: uint32Ptr(31)},
			}},
	)
	return actions
}

func uint32Ptr(v uint32) *uint32 { return &v }

func jiabaidaReadAction(id, name string, tx []byte, readSize uint32, description string) ControlAction {
	return ControlAction{ID: id, Version: 1, Name: name, Description: description, Semantics: "read", Risk: "low", Enabled: false,
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
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_mos", Kind: "write", TXData: writeStep.TXData, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_fet", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_reboot", Kind: "write", TXData: rebootFrame, ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_restart_count", Kind: "readback", TXData: jiabaidaReadFrame(0xAA), ReadSize: 40, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "read_protection_parameters", "read_system_parameters":
		cmd, readSize := byte(0xF2), uint32(60)
		if actionID == "read_system_parameters" {
			cmd, readSize = 0xF3, 59
		}
		return CompiledControlPlan{
			RequiresFinally: true,
			Steps: []CompiledControlPlanStep{
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "read_params", Kind: "readback", TXData: jiabaidaReadFrame(cmd), ReadSize: readSize, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForRead(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "write_protection_parameters", "write_system_parameters":
		cmd, readSize := byte(0xF2), uint32(60)
		block, err := compileF2Block(params)
		if actionID == "write_system_parameters" {
			cmd, readSize = 0xF3, 59
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
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "write_params", Kind: "write", TXData: jiabaidaWriteFrame(cmd, block), ReadSize: 7, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_params", Kind: "readback", TXData: jiabaidaReadFrame(cmd), ReadSize: readSize, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForWrite(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_test", Kind: "write", TXData: jiabaidaWriteFrame(0x0C, []byte{0x00, status}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_test_status", Kind: "readback", TXData: jiabaidaReadFrame(0x0C), ReadSize: 9, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "force_balance":
		return CompiledControlPlan{
			AtMostOnce: true,
			Steps: []CompiledControlPlanStep{
				{ID: "write_force_balance", Kind: "write", TXData: jiabaidaWriteFrame(0xF5, []byte{0x00, 0x01}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_find_car", Kind: "write", TXData: jiabaidaWriteFrame(0xF1, []byte{0x18, onOff}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
			},
		}, nil
	case "clear_alarm":
		return CompiledControlPlan{
			Steps: []CompiledControlPlanStep{
				{ID: "write_clear_alarm", Kind: "write", TXData: jiabaidaWriteFrame(0xE6, []byte{0x18, 0x81}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_edv", Kind: "write", TXData: jiabaidaWriteFrame(0x0D, []byte{byte(minutes >> 8), byte(minutes)}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_custom", Kind: "write", TXData: jiabaidaWriteFrame(0xF0, data), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_custom", Kind: "readback", TXData: jiabaidaReadFrame(0xF0), ReadSize: 13, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_resistance", Kind: "write", TXData: jiabaidaWriteFrame(0xF6, data), ReadSize: 7, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_resistance", Kind: "readback", TXData: jiabaidaReadFrame(0xF6), ReadSize: 67, RXTimeoutMS: 2000, PostTXDelayMS: 100},
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
				{ID: "write_correction", Kind: "write", TXData: jiabaidaWriteFrame(0xF7, []byte{byte(minutes >> 8), byte(minutes)}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_interval", Kind: "write", TXData: jiabaidaWriteFrame(0xF8, data), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_charge_window", Kind: "write", TXData: jiabaidaWriteFrame(0xFA, []byte{byte(delay >> 8), byte(delay), byte(duration >> 8), byte(duration)}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "write_discharge_limit", Kind: "write", TXData: jiabaidaWriteFrame(0xFB, []byte{enable, byte(days >> 8), byte(days)}), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "readback_basic", Kind: "readback", TXData: jiabaidaReadFrame(0x03), ReadSize: 60, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
				{ID: "enter_factory", Kind: "write", TXData: FactoryModeEnterCmd(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
				{ID: "write_sn", Kind: "write", TXData: jiabaidaWriteFrame(0xA2, data), ReadSize: 7, RXTimeoutMS: 2000, PostTXDelayMS: 200},
				{ID: "readback_sn", Kind: "readback", TXData: jiabaidaReadFrame(0xA2), ReadSize: 39, RXTimeoutMS: 2000, PostTXDelayMS: 100},
				{ID: "exit_factory", Kind: "finally", TXData: FactoryModeExitForWrite(), ReadSize: 7, RXTimeoutMS: 1500, PostTXDelayMS: 100},
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
		// The bounded plan reports a step-count envelope: raw[0] = N steps,
		// then per step: kind(1B) + length_le(2B) + response.  Step 1 is the
		// E1 MOS write ACK (DD E1 00 00 CRC 77), step 2 is the 0x03 readback
		// whose fet_status confirms the software-close state.
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			return nil, err
		}
		if len(steps) != 2 {
			return nil, fmt.Errorf("jiabaida set_mos_policy requires two verified responses, got %d", len(steps))
		}
		if err := jiabaidaExpectZeroAck(steps[0], 0xE1); err != nil {
			return nil, fmt.Errorf("jiabaida set_mos_policy ACK: %w", err)
		}
		readback, err := d.ParseData(steps[1])
		if err != nil {
			return nil, fmt.Errorf("jiabaida set_mos_policy readback: %w", err)
		}
		// Bit-level reconciliation against the requested software-close flags.
		// The E1 write byte is inverse-polarity (1 = software close) while
		// fet_status bit0 = charge MOS / bit1 = discharge MOS with 1 = open,
		// so a close request must read back as a cleared FET bit.
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
		var fet *uint8
		for i := range readback {
			if readback[i].Name == "fet_status" {
				v := uint8(readback[i].Value)
				fet = &v
				break
			}
		}
		if fet == nil {
			return nil, fmt.Errorf("jiabaida set_mos_policy readback: fet_status missing")
		}
		gotCharge := *fet & 0x01
		gotDischarge := (*fet >> 1) & 0x01
		if gotCharge != wantChargeOpen || gotDischarge != wantDischargeOpen {
			return nil, fmt.Errorf("jiabaida set_mos_policy readback mismatch: fet_status=0x%02X charge bit=%d want %d discharge bit=%d want %d; 检查 protection_status 是否有硬件保护覆盖", *fet, gotCharge, wantChargeOpen, gotDischarge, wantDischargeOpen)
		}
		return append([]SensorData{{Name: "mos_ack", Value: 1, Unit: "ack"}}, readback...), nil
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
	expected, ok := map[string]byte{
		"read_basic_info": 0x03, "read_cell_voltage": 0x04, "read_hardware_version": 0x05,
		"read_comprehensive": 0x0F, "read_protection_count": 0xAA,
	}[actionID]
	if !ok {
		return nil, fmt.Errorf("unknown jiabaida control action %q", actionID)
	}
	if len(raw) < 2 || raw[0] != 0xDD || raw[1] != expected {
		return nil, fmt.Errorf("jiabaida action %q received unexpected response command", actionID)
	}
	return d.ParseData(raw)
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
func (d *JiabaidaBMSDriver) GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			ID: "read_basic_info", Name: "读取基本信息", Type: "read",
			CmdByte: 0x03, WriteData: jiabaidaReadFrameHex(0x03),
			ReadLength: 60, DelayMs: 100, IntervalMs: 5000, Schedulable: true,
			Description: "总电压、电流、剩余容量、SOC、温度等",
		},
		{
			ID: "read_cell_voltage", Name: "读取单体电压", Type: "read",
			CmdByte: 0x04, WriteData: jiabaidaReadFrameHex(0x04),
			ReadLength: 50, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "每串电芯电压、最高/最低/平均",
		},
		{
			ID: "read_hardware_version", Name: "读取硬件版本", Type: "read",
			CmdByte: 0x05, WriteData: jiabaidaReadFrameHex(0x05),
			ReadLength: 40, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "硬件版本字符串",
		},
		{
			ID: "read_comprehensive", Name: "读取综合信息", Type: "read",
			CmdByte: 0x0F, WriteData: jiabaidaReadFrameHex(0x0F),
			ReadLength: 100, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "0x03超集：含单体电压、均衡状态、运行时间",
		},
		{
			ID: "read_protection_count", Name: "读取保护历史次数", Type: "read",
			CmdByte: 0xAA, WriteData: jiabaidaReadFrameHex(0xAA),
			ReadLength: 40, DelayMs: 100, IntervalMs: 0, Schedulable: true,
			Description: "12种保护触发次数统计",
		},
	}
}
