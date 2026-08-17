package drivers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// ============================================================================
// JiabaidaBMS Driver — 嘉佰达 BMS 电池管理系统 (V19 协议)
// ============================================================================
// Protocol: Binary, RS485/UART, 9600bps, big-endian
// Frame: 0xDD | CMD | STATUS/LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID 4B]
// ============================================================================

// JiabaidaBMSDriver parses Jiabaida BMS binary protocol frames.
type JiabaidaBMSDriver struct{}

func (d *JiabaidaBMSDriver) DeviceType() string      { return "jiabaida_bms" }
func (d *JiabaidaBMSDriver) DeviceName() string      { return "嘉佰达 BMS 电池管理系统" }
func (d *JiabaidaBMSDriver) OEM() string             { return "嘉佰达" }
func (d *JiabaidaBMSDriver) Category() string        { return "BMS" }
func (d *JiabaidaBMSDriver) HardwareTypes() []string { return []string{"uart"} }

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

// jiabaidaResistanceParameters declares the 30 per-cell internal resistance
// values as scalar integer parameters.  The deviceaction catalog schema is
// deliberately scalar-only (string/boolean/integer/number), so a fixed-30
// F6 resistance block is expressed as resistance_1..resistance_30 rather
// than an array.  Values are signed 0.1mΩ (协议 §八 F6, int16 范围).
func jiabaidaResistanceParameters() []ControlParameter {
	params := make([]ControlParameter, 0, 30)
	for i := 1; i <= 30; i++ {
		params = append(params, ControlParameter{
			Name:     fmt.Sprintf("resistance_%d", i),
			Type:     "integer",
			Required: true,
			Minimum:  floatPtr(-32768),
			Maximum:  floatPtr(32767),
		})
	}
	return params
}

// ============================================================================
// F2/F3 parameter-block field tables — single source of truth
// ============================================================================
// Every F2/F3 field is declared exactly once in jiabaidaF2Fields() /
// jiabaidaF3Fields(); the block compilers, read projections (parse0xF2/F3),
// writable schemas (jiabaidaF2Parameters/F3Parameters) and readback
// reconciliation spans (jiabaidaF2FieldSpans/F3FieldSpans) all derive from
// the same table, so a field can no longer drift between the three copies.

// jiabaidaField describes one declared byte range of an F2/F3 parameter
// block.  Width must be 1 (uint8) or 2 (uint16 big-endian).  Scale is the
// parse output multiplier; for Scale<1 the parse divides by the exact
// reciprocal (1/Scale is exactly 10/100 in float64), which is bit-identical
// to the legacy explicit /10 and /100 divisions.  Temp fields override Scale
// with the 0.1K → °C conversion.  Parse=false fields are writable but not
// projected by the read parsers (F2 delay/protection counters).
type jiabaidaField struct {
	Name   string
	Offset int
	Width  int
	Scale  float64
	Unit   string
	Min    float64
	Max    float64
	Temp   bool
	Parse  bool
}

// jiabaidaF2Fields declares all 33 writable fields of the 53-byte F2 block
// (51 field bytes 0..50 + CRC at 51-52).  Order is the schema order (== byte
// order) so compile, parse and schema outputs match the legacy hand-written
// versions exactly.  chg_oc_protect keeps its protocol cap 32676; every
// other u16 is 0..65535, every u8 0..255.
func jiabaidaF2Fields() []jiabaidaField {
	return []jiabaidaField{
		{Name: "cell_ov_protect", Offset: 0, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_ov_release", Offset: 2, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_uv_protect", Offset: 4, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_uv_release", Offset: 6, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_ov_protect", Offset: 8, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_ov_release", Offset: 10, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_uv_protect", Offset: 12, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "pack_uv_release", Offset: 14, Width: 2, Scale: 10, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_ov_delay", Offset: 16, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "cell_uv_delay", Offset: 17, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "pack_ov_delay", Offset: 18, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "pack_uv_delay", Offset: 19, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
		{Name: "chg_ot_protect", Offset: 20, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ot_release", Offset: 22, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ut_protect", Offset: 24, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ut_release", Offset: 26, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ot_protect", Offset: 28, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ot_release", Offset: 30, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ut_protect", Offset: 32, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "dis_ut_release", Offset: 34, Width: 2, Scale: 1, Unit: "°C", Min: 0, Max: 65535, Temp: true, Parse: true},
		{Name: "chg_ot_delay", Offset: 36, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_ut_delay", Offset: 37, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_ot_delay", Offset: 38, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_ut_delay", Offset: 39, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_oc_protect", Offset: 40, Width: 2, Scale: 0.01, Unit: "A", Min: 0, Max: 32676, Parse: true},
		{Name: "chg_oc_delay", Offset: 42, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "chg_oc_release_delay", Offset: 43, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_oc_protect", Offset: 44, Width: 2, Scale: 0.01, Unit: "A", Min: 0, Max: 65535, Parse: true},
		{Name: "dis_oc_delay", Offset: 46, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "dis_oc_release_delay", Offset: 47, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "short_circuit_protect", Offset: 48, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "hardware_oc_protect", Offset: 49, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255},
		{Name: "short_circuit_release", Offset: 50, Width: 1, Scale: 1, Unit: "S", Min: 0, Max: 255, Parse: true},
	}
}

// jiabaidaF3Fields declares all 15 writable fields of the 52-byte F3 block
// (50 field bytes + CRC at 50-51).  Reserved byte ranges (16-19, 32-47) are
// NOT declared and therefore never compiled, parsed or reconciled.
// cell_count_config keeps its protocol range 1..32.
func jiabaidaF3Fields() []jiabaidaField {
	return []jiabaidaField{
		{Name: "function_config", Offset: 0, Width: 2, Scale: 1, Unit: "bitmask", Min: 0, Max: 65535, Parse: true},
		{Name: "ntc_config", Offset: 2, Width: 2, Scale: 1, Unit: "bitmask", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_count_config", Offset: 4, Width: 2, Scale: 1, Unit: "串", Min: 1, Max: 32, Parse: true},
		{Name: "shunt_resistance", Offset: 6, Width: 2, Scale: 0.1, Unit: "mΩ", Min: 0, Max: 65535, Parse: true},
		{Name: "balance_start_voltage", Offset: 8, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "balance_diff", Offset: 10, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "gps_shutdown_voltage", Offset: 12, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "gps_shutdown_delay", Offset: 14, Width: 2, Scale: 1, Unit: "S", Min: 0, Max: 65535, Parse: true},
		{Name: "nominal_capacity_cfg", Offset: 20, Width: 2, Scale: 0.01, Unit: "Ah", Min: 0, Max: 65535, Parse: true},
		{Name: "cycle_capacity_cfg", Offset: 22, Width: 2, Scale: 0.01, Unit: "Ah", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_full_voltage", Offset: 24, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "cell_empty_voltage", Offset: 26, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "self_discharge_rate", Offset: 28, Width: 2, Scale: 0.1, Unit: "%", Min: 0, Max: 65535, Parse: true},
		{Name: "soc100_voltage", Offset: 30, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
		{Name: "soc0_voltage", Offset: 48, Width: 2, Scale: 1, Unit: "mV", Min: 0, Max: 65535, Parse: true},
	}
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

// jiabaidaFieldValue converts a raw field value for the parse projection.
// Scale>=1 multiplies; Scale<1 divides by the exact reciprocal (1/Scale is
// exactly 10/100 in float64), bit-identical to the legacy /10 and /100
// divisions.  Temp fields use the 0.1K → °C conversion.
func jiabaidaFieldValue(raw uint16, f jiabaidaField) float64 {
	if f.Temp {
		return jiabaidaTemperature(raw)
	}
	if f.Scale >= 1 {
		return float64(raw) * f.Scale
	}
	return float64(raw) / (1.0 / f.Scale)
}

// jiabaidaParseBlock projects the declared (Parse=true) fields of a parameter
// block into SensorData in table order (== legacy parse order).
func jiabaidaParseBlock(data []byte, table []jiabaidaField) []SensorData {
	var result []SensorData
	for _, f := range table {
		if !f.Parse {
			continue
		}
		var raw uint16
		if f.Width == 2 {
			raw = binary.BigEndian.Uint16(data[f.Offset : f.Offset+2])
		} else {
			raw = uint16(data[f.Offset])
		}
		result = append(result, SensorData{Name: f.Name, Value: jiabaidaFieldValue(raw, f), Unit: f.Unit})
	}
	return result
}

// jiabaidaFieldParameters renders the writable ControlParameter schema from
// the field table: every field is an integer parameter required=true with the
// table's Min/Max (u8 → 0..255, u16 → 0..65535 except the declared
// overrides cell_count_config 1..32 and chg_oc_protect 0..32676).
func jiabaidaFieldParameters(fields []jiabaidaField) []ControlParameter {
	params := make([]ControlParameter, 0, len(fields))
	for _, f := range fields {
		params = append(params, ControlParameter{
			Name: f.Name, Type: "integer", Required: true,
			Minimum: floatPtr(f.Min), Maximum: floatPtr(f.Max),
		})
	}
	return params
}

// jiabaidaF2Parameters declares the writable protection-parameter fields of
// the 53-byte F2 block.  Names match parse0xF2 so the readback verifier can
// reconcile write→read 1:1.
func jiabaidaF2Parameters() []ControlParameter {
	return jiabaidaFieldParameters(jiabaidaF2Fields())
}

// jiabaidaF3Parameters declares the writable system-parameter fields of the
// 52-byte F3 block.  Reserved byte ranges (16-19, 32-47) are zero-filled by
// the compiler; names match parse0xF3.
func jiabaidaF3Parameters() []ControlParameter {
	return jiabaidaFieldParameters(jiabaidaF3Fields())
}

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

// jiabaidaF2FieldSpans returns the inclusive [start,end] byte ranges of the
// F2 block that carry DECLARED protocol fields, derived from the field table
// (0..50 continuous — no reserved area).  The CRC-16 bytes (51-52) are
// deliberately excluded: a real BMS may recompute the CRC on readback, and
// the CRC protects the field bytes themselves — if every declared field
// matches, the CRC must be correct.
func jiabaidaF2FieldSpans() [][2]int {
	return jiabaidaFieldSpans(jiabaidaF2Fields())
}

// jiabaidaF3FieldSpans returns the inclusive [start,end] byte ranges of the
// F3 block that carry DECLARED protocol fields ({0-15, 20-31, 48-49}),
// derived from the field table.  Reserved byte ranges (16-19, 32-47) and the
// CRC bytes (50-51) are NOT compared: a real BMS may hold nonzero reserved
// bytes or recompute the CRC.
func jiabaidaF3FieldSpans() [][2]int {
	return jiabaidaFieldSpans(jiabaidaF3Fields())
}

// jiabaidaFieldSpans derives inclusive declared-field byte spans from a field
// table by merging contiguous ranges (fields sorted by offset).
func jiabaidaFieldSpans(fields []jiabaidaField) [][2]int {
	sorted := append([]jiabaidaField(nil), fields...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	var spans [][2]int
	for _, f := range sorted {
		start, end := f.Offset, f.Offset+f.Width-1
		if n := len(spans); n > 0 && start <= spans[n-1][1]+1 {
			if end > spans[n-1][1] {
				spans[n-1][1] = end
			}
			continue
		}
		spans = append(spans, [2]int{start, end})
	}
	return spans
}

// jiabaidaEqualOnSpans reports whether got and want are byte-identical on
// every declared-field span.  Length mismatch (including a truncated block
// that cannot cover a span) is a mismatch.
func jiabaidaEqualOnSpans(got, want []byte, spans [][2]int) bool {
	if len(got) != len(want) {
		return false
	}
	for _, span := range spans {
		start, end := span[0], span[1]
		if start < 0 || end < start || end >= len(got) {
			return false
		}
		for i := start; i <= end; i++ {
			if got[i] != want[i] {
				return false
			}
		}
	}
	return true
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

func (d *JiabaidaBMSDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		{Name: "total_voltage", Unit: "V"},
		{Name: "current", Unit: "A"},
		{Name: "remaining_capacity", Unit: "Ah"},
		{Name: "nominal_capacity", Unit: "Ah"},
		{Name: "cycle_count", Unit: "次"},
		{Name: "rsoc", Unit: "%"},
		{Name: "protection_status", Unit: "bitmask"},
		{Name: "fet_status", Unit: "bitmask"},
		{Name: "cell_count", Unit: "串"},
		{Name: "cell_voltage_max", Unit: "V"},
		{Name: "cell_voltage_min", Unit: "V"},
		{Name: "cell_voltage_avg", Unit: "V"},
	}
}

// ============================================================================
// Error codes
// ============================================================================

// ErrorCode represents a Jiabaida protocol error category.
type ErrorCode int

const (
	ErrInvalidFrameStart ErrorCode = iota + 1
	ErrInvalidStopByte
	ErrChecksumMismatch
	ErrIncompleteFrame
	ErrUnknownCommand
	ErrBMSStatusError
	ErrDataTooShort
)

// ParseError carries structured error information for Jiabaida protocol errors.
type ParseError struct {
	Code   ErrorCode
	Detail string
	Raw    []byte
}

func (e *ParseError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("jiabaida: [%d] %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("jiabaida: [%d]", e.Code)
}

// ============================================================================
// Checksum algorithm
// ============================================================================
// Algorithm: uint16 sum of bytes → bitwise NOT + 1 → big-endian uint16.
// Verified against 8 known frames from the protocol docs.
// Send frame checksum range: CMD + LEN + DATA (excludes 0xDD and 0xA5/0x5A).
// Response frame checksum range: LEN + DATA (excludes 0xDD, CMD, STATUS).

// jiabaidaChecksum computes the checksum over the given bytes.
func jiabaidaChecksum(data []byte) uint16 {
	var sum uint16
	for _, b := range data {
		sum += uint16(b)
	}
	return ^sum + 1
}

// verifyJiabaidaChecksum verifies the checksum of a complete raw frame.
// raw must contain the full frame from 0xDD to at least the checksum bytes.
func verifyJiabaidaChecksum(raw []byte) bool {
	if len(raw) < 7 {
		return false
	}
	cmd := raw[1]
	isSend := (cmd == 0xA5 || cmd == 0x5A)
	length := int(raw[3])
	payloadEnd := 4 + length
	if len(raw) < payloadEnd+2 {
		return false
	}
	if isSend {
		// Send frame: checksum over CMD + LEN + DATA (skip 0xDD and 0xA5/0x5A)
		return jiabaidaChecksum(raw[2:payloadEnd]) == binary.BigEndian.Uint16(raw[payloadEnd:])
	}
	// Response frame: checksum over LEN + DATA
	return jiabaidaChecksum(raw[3:payloadEnd]) == binary.BigEndian.Uint16(raw[payloadEnd:])
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

// ============================================================================
// ParseData — main entry point
// ============================================================================

// ParseData parses a raw Jiabaida BMS frame and returns sensor data.
// The frame structure:
//
//	Success response: 0xDD | CMD | STATUS(0x00) | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//	Error response:   0xDD | STATUS(0x80/0x81/0x82) | LEN(0x00) | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//	Send frame:       0xDD | 0xA5/0x5A | CMD | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77 | [CALLBACKID]
//
// After the stop byte 0x77 there may be up to 4 extra CALLBACKID bytes.
// We locate the stop byte via the LEN field and ignore trailing bytes.
func (d *JiabaidaBMSDriver) ParseData(raw []byte) ([]SensorData, error) {
	// A normal frame needs at least seven bytes, but a documented zero-length
	// error frame is exactly six bytes before an optional callback ID.
	if len(raw) < 6 || raw[0] != 0xDD {
		return nil, &ParseError{Code: ErrInvalidFrameStart, Raw: raw}
	}

	// Detect error response frames before interpreting byte 1 as a command.
	// V19 permits an optional four-byte callback after the delimiter, so the
	// delimiter is at its length-derived position (5), not necessarily the last
	// byte.  For LEN=0, the response checksum is the two's-complement checksum
	// of zero, i.e. 0x0000.
	if raw[1] == 0x80 || raw[1] == 0x81 || raw[1] == 0x82 {
		status := raw[1]
		if len(raw) < 6 {
			return nil, &ParseError{Code: ErrIncompleteFrame, Detail: "truncated BMS error response", Raw: raw}
		}
		if raw[2] != 0x00 {
			return nil, &ParseError{Code: ErrIncompleteFrame, Detail: "BMS error response has nonzero length", Raw: raw}
		}
		if raw[5] != 0x77 {
			return nil, &ParseError{Code: ErrInvalidStopByte, Raw: raw}
		}
		if raw[3] != 0x00 || raw[4] != 0x00 {
			return nil, &ParseError{Code: ErrChecksumMismatch, Raw: raw}
		}
		return nil, &ParseError{
			Code:   ErrBMSStatusError,
			Detail: fmt.Sprintf("BMS status 0x%02X", status),
			Raw:    raw,
		}
	}

	// Determine if this is a send frame (write command/read request) or response frame.
	cmdOrRW := raw[1]
	var cmd, status byte
	var length int
	isSend := false

	if cmdOrRW == 0xA5 || cmdOrRW == 0x5A {
		// Send frame: 0xDD | 0xA5/0x5A | CMD | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77
		isSend = true
		cmd = raw[2]
		length = int(raw[3])
	} else {
		// Response frame: 0xDD | CMD | STATUS | LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77
		cmd = cmdOrRW
		status = raw[2]
		length = int(raw[3])
	}

	// Calculate expected minimum frame length: header(4) + data(length) + checksum(2) + stop(1) = 4+LEN+2+1
	expectedLen := 4 + length + 2 + 1
	if len(raw) < expectedLen {
		return nil, &ParseError{
			Code:   ErrIncompleteFrame,
			Detail: fmt.Sprintf("expected >=%d bytes, got %d", expectedLen, len(raw)),
			Raw:    raw,
		}
	}

	// Verify stop byte 0x77
	stopBytePos := 4 + length + 2
	if raw[stopBytePos] != 0x77 {
		return nil, &ParseError{Code: ErrInvalidStopByte, Raw: raw}
	}

	// Verify checksum
	if !verifyJiabaidaChecksum(raw) {
		return nil, &ParseError{Code: ErrChecksumMismatch, Raw: raw}
	}

	// For response frames, check BMS status
	if !isSend {
		if status == 0x80 || status == 0x81 || status == 0x82 {
			return nil, &ParseError{
				Code:   ErrBMSStatusError,
				Detail: fmt.Sprintf("BMS status 0x%02X", status),
				Raw:    raw,
			}
		}
	}

	// Extract data payload
	data := raw[4 : 4+length]

	// Route only documented read commands.  0xE1 is not a factory-mode command
	// in the V19 source, but it is still a destructive dual-bit control and is
	// deliberately unavailable until its Action Catalog/readback gate is met.
	// F2/F3/F6 stay unavailable as well; their physical workflows require
	// additional factory-mode and CRC evidence.
	switch cmd {
	case 0x03:
		return d.parse0x03(data)
	case 0x04:
		return d.parse0x04(data)
	case 0x05:
		return d.parse0x05(data)
	case 0x0C:
		return d.parse0x0C(data)
	case 0x0F:
		return d.parse0x0F(data)
	case 0xA2:
		return d.parse0xA2(data)
	case 0xAA:
		return d.parse0xAA(data)
	case 0xF0:
		return d.parse0xF0(data)
	case 0xF2:
		return d.parse0xF2(data)
	case 0xF3:
		return d.parse0xF3(data)
	case 0xF6:
		return d.parse0xF6(data)
	default:
		return nil, &ParseError{
			Code:   ErrUnknownCommand,
			Detail: fmt.Sprintf("cmd 0x%02X (not yet enabled)", cmd),
			Raw:    raw,
		}
	}
}

// parse0x0C — MOS test status readback (协议 §7.5)
// DATA: [放电 MOS 测试状态, 充电 MOS 测试状态]
// 状态值: 0=未测试 1=OK 2=NG(可能损坏) 3=超时未加载电流
func (d *JiabaidaBMSDriver) parse0x0C(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x0C: need 2 bytes"}
	}
	return []SensorData{
		{Name: "discharge_mos_test_status", Value: float64(data[0]), Unit: "state"},
		{Name: "charge_mos_test_status", Value: float64(data[1]), Unit: "state"},
	}, nil
}

// parse0xF0 — custom attributes readback (协议 §7.8)
// DATA: 3 × uint16 自定义字段，大端
func (d *JiabaidaBMSDriver) parse0xF0(data []byte) ([]SensorData, error) {
	if len(data) < 6 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF0: need 6 bytes"}
	}
	return []SensorData{
		{Name: "custom_attr_1", Value: float64(binary.BigEndian.Uint16(data[0:2])), Unit: "raw"},
		{Name: "custom_attr_2", Value: float64(binary.BigEndian.Uint16(data[2:4])), Unit: "raw"},
		{Name: "custom_attr_3", Value: float64(binary.BigEndian.Uint16(data[4:6])), Unit: "raw"},
	}, nil
}

// ============================================================================
// Temperature conversion helper
// ============================================================================

// jiabaidaTemperature converts raw 0.1K absolute temperature to °C.
func jiabaidaTemperature(raw uint16) float64 {
	return float64(raw)/10.0 - 273.15
}

// ============================================================================
// parse0x03 — Basic information (协议 Page 5-6)
// ============================================================================
// DATA layout (byte offsets):
//
//	[0:2]   Total voltage (uint16, 10mV) → /100 → V
//	[2:4]   Current (int16, 10mA, signed) → /100 → A
//	[4:6]   Remaining capacity (uint16, 10mAh) → /100 → Ah
//	[6:8]   Nominal capacity (uint16, 10mAh) → /100 → Ah
//	[8:10]  Cycle count (uint16)
//	[10:12] Manufacture date (uint16) — skipped
//	[12:14] Balance status low 16 cells (uint16) — skipped
//	[14:16] Balance status high 16 cells (uint16) — skipped
//	[16:18] Protection status (uint16, bitmask)
//	[18]     Software version (uint8)
//	[19]     RSOC (uint8, %)
//	[20]     FET control status (uint8)
//	[21]     Cell count (uint8)
//	[22]     NTC count (uint8)
//	[23:]    N×Temperature (uint16, 0.1K) → /10 - 273.15 → °C

func (d *JiabaidaBMSDriver) parse0x03(data []byte) ([]SensorData, error) {
	if len(data) < 23 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: fmt.Sprintf("0x03: need >=23 bytes, got %d", len(data))}
	}
	totalVoltage := float64(binary.BigEndian.Uint16(data[0:2])) / 100.0
	currentRaw := int16(binary.BigEndian.Uint16(data[2:4]))
	current := float64(currentRaw) / 100.0
	remainingCap := float64(binary.BigEndian.Uint16(data[4:6])) / 100.0
	nominalCap := float64(binary.BigEndian.Uint16(data[6:8])) / 100.0
	cycleCount := float64(binary.BigEndian.Uint16(data[8:10]))
	protectStatus := float64(binary.BigEndian.Uint16(data[16:18]))
	version := float64(data[18])
	rsoc := float64(data[19])
	fetStatus := float64(data[20])
	cellCount := int(data[21])
	ntcCount := int(data[22])

	result := []SensorData{
		{Name: "total_voltage", Value: totalVoltage, Unit: "V"},
		{Name: "current", Value: current, Unit: "A"},
		{Name: "remaining_capacity", Value: remainingCap, Unit: "Ah"},
		{Name: "nominal_capacity", Value: nominalCap, Unit: "Ah"},
		{Name: "cycle_count", Value: cycleCount, Unit: "次"},
		{Name: "rsoc", Value: rsoc, Unit: "%"},
		{Name: "protection_status", Value: protectStatus, Unit: "bitmask"},
		{Name: "fet_status", Value: fetStatus, Unit: "bitmask"},
		{Name: "cell_count", Value: float64(cellCount), Unit: "串"},
		{Name: "software_version", Value: version, Unit: ""},
	}

	tempOffset := 23
	for i := 0; i < ntcCount && tempOffset+1 < len(data); i++ {
		rawTemp := binary.BigEndian.Uint16(data[tempOffset : tempOffset+2])
		result = append(result, SensorData{
			Name:  fmt.Sprintf("temperature_%d", i+1),
			Value: jiabaidaTemperature(rawTemp),
			Unit:  "°C",
		})
		tempOffset += 2
	}
	return result, nil
}

// ============================================================================
// parse0x04 — Cell voltages (协议 Page 7-8)
// ============================================================================
// DATA layout: N×2B, each 2 bytes = cell voltage in mV → /1000 → V.
// Also computes max/min/avg.

func (d *JiabaidaBMSDriver) parse0x04(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x04: empty"}
	}
	var total, maxV, minV float64
	minV = math.MaxFloat64
	cellCount := len(data) / 2
	var result []SensorData

	for i := 0; i+1 < len(data); i += 2 {
		voltage := float64(binary.BigEndian.Uint16(data[i:i+2])) / 1000.0
		total += voltage
		if voltage > maxV {
			maxV = voltage
		}
		if voltage < minV {
			minV = voltage
		}
		result = append(result, SensorData{
			Name:  fmt.Sprintf("cell_voltage_%d", i/2+1),
			Value: voltage,
			Unit:  "V",
		})
	}
	if cellCount > 0 {
		result = append(result,
			SensorData{Name: "cell_voltage_max", Value: maxV, Unit: "V"},
			SensorData{Name: "cell_voltage_min", Value: minV, Unit: "V"},
			SensorData{Name: "cell_voltage_avg", Value: total / float64(cellCount), Unit: "V"},
		)
	}
	return result, nil
}

// ============================================================================
// parse0x0F — Comprehensive information (协议 Page 12-13)
// ============================================================================
// 0x0F is a superset of 0x03, adding cell voltages + balance + runtime.
// BYTE layout (0-indexed):
//
//	[0]     Reserved
//	[1:3]   Total voltage (uint16, 10mV)
//	[3:5]   Current (int16, 10mA)
//	[5]     SOC (uint8, %)
//	[6:8]   Remaining capacity (uint16, 10mAh)
//	[8:10]  Full capacity (uint16, 10mAh)
//	[10:12] Protection status (uint16, bitmask)
//	[12:14] Max cell voltage (uint16, mV)
//	[14:16] Min cell voltage (uint16, mV)
//	[16:18] Balance low (uint16)
//	[18:20] Balance high (uint16)
//	[20:22] Cycle count (uint16)
//	[22]    FET status (uint8)
//	[23]    NTC count (uint8)
//	[24:24+2N] Temperature array (uint16, 0.1K × N)
//	[24+2N] Cell count M (uint8)
//	[25+2N:] M×Cell voltage (uint16 × M, mV)
//	trailer: current_state(1B) + charge_capacity(2B) + runtime(2B) + sequence(2B) + humidity(1B)

func (d *JiabaidaBMSDriver) parse0x0F(data []byte) ([]SensorData, error) {
	if len(data) < 24 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x0F: too short"}
	}
	totalVoltage := float64(binary.BigEndian.Uint16(data[1:3])) / 100.0
	currentRaw := int16(binary.BigEndian.Uint16(data[3:5]))
	current := float64(currentRaw) / 100.0
	rsoc := float64(data[5])
	remainingCap := float64(binary.BigEndian.Uint16(data[6:8])) / 100.0
	fullCap := float64(binary.BigEndian.Uint16(data[8:10])) / 100.0
	protectStatus := float64(binary.BigEndian.Uint16(data[10:12]))
	maxCellMV := float64(binary.BigEndian.Uint16(data[12:14]))
	minCellMV := float64(binary.BigEndian.Uint16(data[14:16]))
	cycleCount := float64(binary.BigEndian.Uint16(data[20:22]))
	fetStatus := float64(data[22])
	ntcCount := int(data[23])

	result := []SensorData{
		{Name: "total_voltage", Value: totalVoltage, Unit: "V"},
		{Name: "current", Value: current, Unit: "A"},
		{Name: "rsoc", Value: rsoc, Unit: "%"},
		{Name: "remaining_capacity", Value: remainingCap, Unit: "Ah"},
		{Name: "nominal_capacity", Value: fullCap, Unit: "Ah"},
		{Name: "cycle_count", Value: cycleCount, Unit: "次"},
		{Name: "protection_status", Value: protectStatus, Unit: "bitmask"},
		{Name: "fet_status", Value: fetStatus, Unit: "bitmask"},
		{Name: "cell_voltage_max", Value: maxCellMV / 1000.0, Unit: "V"},
		{Name: "cell_voltage_min", Value: minCellMV / 1000.0, Unit: "V"},
	}

	// NTC temperatures
	tempOffset := 24
	for i := 0; i < ntcCount && tempOffset+1 < len(data); i++ {
		rawTemp := binary.BigEndian.Uint16(data[tempOffset : tempOffset+2])
		result = append(result, SensorData{
			Name:  fmt.Sprintf("temperature_%d", i+1),
			Value: jiabaidaTemperature(rawTemp),
			Unit:  "°C",
		})
		tempOffset += 2
	}

	// Cell voltages (after temperatures + cell count byte)
	if tempOffset < len(data) {
		cellCount := int(data[tempOffset])
		tempOffset++
		for i := 0; i < cellCount && tempOffset+1 < len(data); i++ {
			voltage := float64(binary.BigEndian.Uint16(data[tempOffset:tempOffset+2])) / 1000.0
			result = append(result, SensorData{
				Name:  fmt.Sprintf("cell_voltage_%d", i+1),
				Value: voltage,
				Unit:  "V",
			})
			tempOffset += 2
		}
	}

	return result, nil
}

// ============================================================================
// parse0xAA — Protection history counts (协议 Page 19)
// ============================================================================
// DATA layout: 12 × uint16, each = count of a protection type triggered.

func (d *JiabaidaBMSDriver) parse0xAA(data []byte) ([]SensorData, error) {
	if len(data) < 24 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xAA: need 24 bytes"}
	}
	names := []string{
		"short_circuit_count", "charge_overcurrent_count", "discharge_overcurrent_count",
		"cell_overvoltage_count", "cell_undervoltage_count",
		"charge_overtemp_count", "charge_undertemp_count",
		"discharge_overtemp_count", "discharge_undertemp_count",
		"pack_overvoltage_count", "pack_undervoltage_count", "restart_count",
	}
	var result []SensorData
	for i, name := range names {
		result = append(result, SensorData{
			Name:  name,
			Value: float64(binary.BigEndian.Uint16(data[i*2 : i*2+2])),
			Unit:  "次",
		})
	}
	return result, nil
}

// ============================================================================
// parse0x05 — Hardware version string (ASCII)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0x05(data []byte) ([]SensorData, error) {
	if len(data) < 1 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0x05: empty"}
	}
	return []SensorData{{Name: "hardware_version", Value: 0, Unit: "", StringValue: string(data)}}, nil
}

// ============================================================================
// parse0xF2 — Protection parameters (53 bytes, requires factory mode)
// ============================================================================
// The read projection is generated from the jiabaidaF2Fields() table
// (Parse=true fields, table order), so field layout cannot drift from the
// block compiler and schema.

func (d *JiabaidaBMSDriver) parse0xF2(data []byte) ([]SensorData, error) {
	if len(data) < 53 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF2: need 53 bytes"}
	}
	return jiabaidaParseBlock(data, jiabaidaF2Fields()), nil
}

// ============================================================================
// parse0xF3 — System parameters (52 bytes, requires factory mode)
// ============================================================================
// Read projection generated from the jiabaidaF3Fields() table; reserved byte
// ranges (16-19, 32-47) carry no fields and are never projected.

func (d *JiabaidaBMSDriver) parse0xF3(data []byte) ([]SensorData, error) {
	if len(data) < 52 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF3: need 52 bytes"}
	}
	return jiabaidaParseBlock(data, jiabaidaF3Fields()), nil
}

// ============================================================================
// parse0xF6 — Cell internal resistance (N×2B, signed int16, 0.1mΩ)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xF6(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xF6: empty"}
	}
	var result []SensorData
	for i := 0; i+1 < len(data); i += 2 {
		raw := int16(binary.BigEndian.Uint16(data[i : i+2]))
		result = append(result, SensorData{
			Name:  fmt.Sprintf("cell_resistance_%d", i/2+1),
			Value: float64(raw) / 10.0,
			Unit:  "mΩ",
		})
	}
	return result, nil
}

// ============================================================================
// parse0xA2 — Serial number (ASCII, length-prefixed)
// ============================================================================

func (d *JiabaidaBMSDriver) parse0xA2(data []byte) ([]SensorData, error) {
	if len(data) < 2 {
		return nil, &ParseError{Code: ErrDataTooShort, Detail: "0xA2: empty"}
	}
	snLen := int(data[0])
	if 1+snLen > len(data) {
		snLen = len(data) - 1
	}
	return []SensorData{{Name: "serial_number", Value: 0, Unit: "", StringValue: string(data[1 : 1+snLen])}}, nil
}

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

// The bounded-plan step-count envelope is decoded by the package-level
// decodeBatchPlanEnvelope (see batch_envelope.go — shared with every driver,
// not Jiabaida-specific).
