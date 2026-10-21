package drivers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ============================================================================
// TechfineInverterDriver — Techfine GB3024 逆变器 (ASCII 协议)
// ============================================================================
// Protocol: RS232C, 2400bps, 8N1, ASCII text
// Commands end with \r, responses start with '(' and end with \r
// Fields are space-separated. Trailing "OOO..." data is ignored.
// ============================================================================

// TechfineInverterDriver parses Techfine GB3024 inverter ASCII protocol responses.
type TechfineInverterDriver struct{}

func (d *TechfineInverterDriver) DeviceType() string      { return "techfine_inverter" }
func (d *TechfineInverterDriver) DeviceName() string      { return "泰琪丰 GB3024 逆变器" }
func (d *TechfineInverterDriver) OEM() string             { return "泰琪丰" }
func (d *TechfineInverterDriver) Category() string        { return "inverter" }
func (d *TechfineInverterDriver) HardwareTypes() []string { return []string{"uart"} }

// ControlActions exposes only documented, side-effect-free ASCII queries.
//
// All eleven are ENABLED (2026-10-03).  They were hard-coded Enabled:false as a
// development rollout gate ("until GB3024 hardware evidence is recorded"), but
// that gate had no configuration entry point -- SetEnabled is a
// composition-root/test primitive, docs/设计/设备指令与操作体系演进方案.md
// §4.1 says so explicitly -- so the effect on a real deployment was simply
// "eleven operations are visible and unusable": the edge-device UI listed
// every one of them as "action is not enabled for rollout".
// A gate nobody can open is not a gate, it is a permanent disablement.
//
// Re-enabling them is safe for the reason that gate was about *writes*, not
// reads: these are pure ASCII queries with no side effect, so a wrong frame or
// a silent inverter produces a timeout (a failed execution) and can never
// corrupt inverter state.  The fail-closed write gate is untouched and stays
// enforced elsewhere: no SON, no CRC-bearing setting and no parameter write is
// represented here at all, because the V0.5 document does not define the CRC
// algorithm and provides no verifiable write/readback capture.
//
// Process: the same document (§4.2 item 3) requires an unfreeze to flip the
// guarded assertion in the same change and to register the action in the gate
// ledger.  Both are done:
//
//	· backend/internal/deviceaction/definition_test.go -- the GB3024 read
//	  assertions flipped to "enabled", plus a new guard proving the
//	  AvailabilityCode actions (reset_rainfall / clear_rainfall_write) still
//	  fail closed.
//	· docs/分析/动作门禁台账.md -- ledger row for these eleven actions.
//
// Firmware dependency (this is the part that was actually broken, and it is
// why enabling the flag alone would not have been enough): the driver used to
// declare ReadSize 256 while a GB3024 reply is a single ASCII line of roughly
// 15-40 bytes.  A single-step ChannelCmdV2 command reaches the node as
// CMD_WRITE (bus_manager.c:1064), where a reply shorter than read_size was
// converted into error 0x03 by complete_idle_response(), and read_size == 0
// instead completed the control with no payload at all (:985).  Every
// variable-length read was therefore broken either way.  The node now treats
// the line-idle gap as the frame boundary for V2 (bus_worker.c, "short-read
// rule"), which keeps the legacy write-response rule intact.  Without that
// firmware change these eleven actions stay unusable no matter what Enabled
// says, which is precisely why the flag was never the real gate.
//
// ReadSize is the node's *upper* read window here, deliberately set to the
// protocol maximum (256) rather than the real line length.
//
// Why not the exact length: a Techfine reply is one ASCII line whose byte
// count varies by command and firmware revision, and the node uses read_size
// as a hard boundary, not a hint.  bus_rx_boundary.h:18 buffers until that
// many bytes arrived (so any value larger than the real line simply never
// completes), while a value smaller than the real line makes
// emit_ready_stream_chunks() cut the line at that length
// (bus_worker.c:1197-1213) and hand a truncated frame to the parser.  There is
// no single "correct" exact length to guess.
//
// 256 is reachable in the other direction: a line shorter than the window is
// delivered when the line goes idle (10 ms gap).  That delivery requires the
// node's ChannelCmdV2 short-response rule -- see
// docs/分析/六项缺陷诊断与修复方案-2026-10-03.md 缺陷 1 -- where a V2 response
// that is shorter than read_size is passed to the server instead of being
// turned into error_code=3 locally.  That split is deliberate: for V2 the node
// cannot know a vendor frame's true length, and the server already owns the
// authoritative per-action verifier (VerifyControlAction), which rejects
// anything that is not a well-formed '(' ... '\r' response.  On a node without
// that rule these actions still fail closed (they error, they do not fake a
// success), which is why enabling them cannot silently corrupt anything.
//
// RXTimeoutMS remains the hard bound so a silent inverter still fails.
func (d *TechfineInverterDriver) ControlActions() []ControlAction {
	return techfineControlActions()
}

// techfineControlActions 从命令注册表派生动作目录 —— 不再手抄第二份命令表。
//
// 顺序沿用注册表顺序（= 历史 command_index 的声明顺序），不要重排。
func techfineControlActions() []ControlAction {
	out := make([]ControlAction, 0, len(techfineCommands))
	for i := range techfineCommands {
		out = append(out, techfineReadAction(&techfineCommands[i]))
	}
	return out
}

func techfineReadAction(c *techfineCommand) ControlAction {
	return ControlAction{
		ID: c.ID, Version: 1, Name: c.Name, Description: c.Description,
		Semantics: "read", Risk: "low", Enabled: true,
		// UART worker returns the entire line-idle-delimited response; the window
		// is the protocol max because the exact line length is not knowable.  See
		// the ControlActions comment above.
		TXData: []byte(c.Wire), ReadSize: 256, RXTimeoutMS: 1000,
	}
}

// VerifyControlAction makes final-result parsing command-aware.  HPV and HPVB
// share the same wire shape, so using generic ParseData here would lose PV1/PV2
// identity and could persist data under the wrong sensor names.
func (d *TechfineInverterDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error) {
	if string(params) != "{}" {
		return nil, fmt.Errorf("techfine action %q does not accept parameters", actionID)
	}
	cmd, ok := techfineCommandByID[actionID]
	if !ok {
		return nil, fmt.Errorf("unknown techfine control action %q", actionID)
	}
	// 帧完整性：GB3024 的每一条响应都以 CR 结尾（查询帧本身也是 "<CMD>\r"，
	// 见上面的命令表）。校验结尾不是"锦上添花"，而是**必需的**：
	//
	// 2026-10-03 主控自查发现（P0）：节点侧对 ChannelCmdV2 改为「行空闲即帧边界」后，
	// 短于 read_size 的响应不再在节点本地被丢成 error 0x03，而是原样投递上来。
	// 若这里不校验结尾，一个被截断的 ASCII 行会**通过校验并产出错误数值**：
	// 例如 "(220.5 08.0 0" 会被 ParseData 解析成 pv2_power=0（本该是 960），
	// 于是"读取失败"退化成"静默的假数据"——比失败更糟，用户无从察觉。
	//
	// 判据放在 verifier 而不是 ParseData：ParseData 也被轮询路径（CMD_SAMPLE）调用，
	// 那条路径的定帧由节点侧负责，收紧它会影响既有轮询行为；
	// 而 V2 控制动作的权威校验点就是这里。
	if len(raw) == 0 || raw[len(raw)-1] != '\r' {
		return nil, fmt.Errorf("techfine %s: response is not CR-terminated (len=%d, tail=%q) — 截断或畸形帧拒绝采信",
			actionID, len(raw), tailForError(raw))
	}
	data, err := d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(cmd.Wire)))
	if err != nil {
		return nil, err
	}
	// 形状绑定（2026-10-03 固件审查发现，P0）：**仅校验 CR 结尾是不够的**。
	//
	// ParseData 是按「字段数/格式」嗅探分支的，它不绑定所请求的动作：
	// 一个 CR 结尾但形状错误的帧会被解析成**别的传感器**并当成成功。实测：
	//
	//	请求 read_battery，喂 HGRID 形状 → 返回 grid_voltage/grid_frequency
	//	请求 read_status，喂被截短的 HSTS → 12 个告警位全 0（**fail-open**：
	//	  截短的状态帧被读成"一切正常"，这是最危险的一种）
	//	请求 read_temperature，喂 HBAT 形状 → 返回 battery_* 字段
	//
	// 这是本批改动放行短读后新增的暴露面（改动前 <256 字节会被节点本地 0x03 拒绝）。
	// 因此每个动作必须绑定**它期望的传感器族**：返回的字段必须全部属于该族，
	// 否则判为「响应与请求不符」并失败。
	if err := expectActionSensors(cmd, data); err != nil {
		return nil, err
	}
	return data, nil
}

// expectActionSensors 校验解析结果确实属于该动作期望的传感器族，且形状完整。
//
// 两道判据缺一不可：
//  1. **每一个**返回字段都必须属于期望族（拦"喂错形状"）；
//  2. 字段数不得少于该动作的完整形状（拦"同族但被截短"）。
//
// 只做 1 会放过截短的 HSTS（12 个告警位全 0 = fail-open）；
// 只做 2 会放过"字段数够但根本不是这个动作"的错配帧。
func expectActionSensors(cmd *techfineCommand, data []SensorData) error {
	if cmd == nil {
		return fmt.Errorf("techfine: internal error — nil command passed to shape binding")
	}
	if len(data) == 0 {
		return fmt.Errorf("techfine %s: response produced no sensor values", cmd.ID)
	}
	for _, sd := range data {
		if !sensorNameMatchesFamily(sd.Name, cmd.Family) {
			return fmt.Errorf("techfine %s: response contained %q which does not belong to the expected sensor family %v — 响应与请求的动作不符（错配或截断帧）",
				cmd.ID, sd.Name, cmd.Family)
		}
	}
	if min := cmd.MinSensors; min > 0 && len(data) < min {
		return fmt.Errorf("techfine %s: response yielded only %d sensor values, expected at least %d — 帧形状不完整（很可能是被截断的响应，拒绝采信以免产生静默假数据）",
			cmd.ID, len(data), min)
	}
	return nil
}

func sensorNameMatchesFamily(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// tailForError 截取用于报错信息的尾部片段（最多 16 字节），避免把整帧塞进日志。
func tailForError(raw []byte) string {
	const maxTail = 16
	if len(raw) > maxTail {
		raw = raw[len(raw)-maxTail:]
	}
	return string(raw)
}

// GetSensorDefinitions returns all sensor definitions for HA Discovery.
func (d *TechfineInverterDriver) GetSensorDefinitions() []SensorData {
	return []SensorData{
		// PV
		{Name: "pv1_voltage", Unit: "V"},
		{Name: "pv1_current", Unit: "A"},
		{Name: "pv1_power", Unit: "W"},
		// PV2 是可选硬件（协议 V0.5 的 HPVB 独立一节）。这里按**能力级**声明：
		// 换一台带两路 PV 的机型即可产出；PV1-only 机型（如本机 node
		// F0F5BD02DD84）永远不会出现这些传感器，前端按「字段不存在」处理即可。
		{Name: "pv2_voltage", Unit: "V"},
		{Name: "pv2_current", Unit: "A"},
		{Name: "pv2_power", Unit: "W"},
		// Grid
		{Name: "grid_voltage", Unit: "V"},
		{Name: "grid_frequency", Unit: "Hz"},
		// Output
		{Name: "output_voltage", Unit: "V"},
		{Name: "output_frequency", Unit: "Hz"},
		{Name: "output_apparent_power", Unit: "VA"},
		{Name: "output_active_power", Unit: "W"},
		{Name: "output_load_percent", Unit: "%"},
		// Battery
		{Name: "battery_voltage", Unit: "V"},
		{Name: "battery_capacity", Unit: "%"},
		{Name: "battery_charge_current", Unit: "A"},
		{Name: "battery_discharge_current", Unit: "A"},
		{Name: "bus_voltage", Unit: "V"},
		// Temperature
		{Name: "pv_temp", Unit: "°C"},
		{Name: "inverter_temp", Unit: "°C"},
		{Name: "boost_temp", Unit: "°C"},
		{Name: "transformer_temp", Unit: "°C"},
		{Name: "max_temp", Unit: "°C"},
		{Name: "pv2_temp", Unit: "°C"},
		{Name: "dc_rectifier_temp", Unit: "°C"},
		// Fan
		{Name: "fan1_speed", Unit: "%"},
		{Name: "fan2_speed", Unit: "%"},
		{Name: "fan1_status", Unit: ""},
		{Name: "fan2_status", Unit: ""},
		// Energy
		{Name: "daily_energy", Unit: "kWh"},
		{Name: "monthly_energy", Unit: "kWh"},
		{Name: "yearly_energy", Unit: "kWh"},
		{Name: "total_energy", Unit: "kWh"},
		// Status
		{Name: "fault_code", Unit: ""},
		{Name: "work_mode", Unit: ""},
		// Alarms
		{Name: "alarm_pv_to_load", Unit: ""},
		{Name: "alarm_output", Unit: ""},
		{Name: "alarm_battery_low", Unit: ""},
		{Name: "alarm_battery_missing", Unit: ""},
		{Name: "alarm_overload", Unit: ""},
		{Name: "alarm_overtemp", Unit: ""},
		{Name: "alarm_eeprom_data", Unit: ""},
		{Name: "alarm_eeprom_rw", Unit: ""},
		{Name: "alarm_pv_low", Unit: ""},
		{Name: "alarm_input_overvoltage", Unit: ""},
		{Name: "alarm_battery_overvoltage", Unit: ""},
		{Name: "alarm_fan_error", Unit: ""},
		// BMS
		{Name: "bms_comm_ok", Unit: ""},
		{Name: "bms_charge_allowed", Unit: ""},
		{Name: "bms_discharge_allowed", Unit: ""},
		{Name: "bms_low_alarm", Unit: ""},
		{Name: "bms_low_fault", Unit: ""},
		{Name: "bms_charge_overcurrent", Unit: ""},
		{Name: "bms_discharge_overcurrent", Unit: ""},
		{Name: "bms_temp_low", Unit: ""},
		{Name: "bms_temp_high", Unit: ""},
		{Name: "bms_soc", Unit: "%"},
		{Name: "bms_charge_current", Unit: "A"},
		{Name: "bms_discharge_current", Unit: "A"},
		{Name: "bms_charge_voltage_limit", Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Unit: "V"},
		{Name: "bms_charge_current_limit", Unit: "A"},
		{Name: "bms_temp", Unit: "°C"},
		// EEPROM（read_eeprom 动作产出。2026-10-21 补齐：此前 4 个字段
		// 一直被解析并写库，却从未在这里声明 —— 前端拿不到单位，
		// HA Discovery 也发现不了它们）
		{Name: "eeprom_work_mode", Unit: ""},
		{Name: "eeprom_max_charge_current", Unit: "A"},
		{Name: "eeprom_output_voltage", Unit: "V"},
		{Name: "eeprom_output_frequency", Unit: "Hz"},
		// Version
		{Name: "software_version", Unit: ""},
		{Name: "software_date", Unit: ""},
		// Protocol
		{Name: "protocol_type", Unit: ""},
	}
}

// ============================================================================
// ParseData — 无命令上下文时的形状分派（fail-closed）
// ============================================================================
//
// ParseData 只做一件事：**拿不到命令上下文时，如果应答形状能唯一确定命令，
// 就用该命令的解析器；否则报错。**
//
// 它不是主路径 —— CommandAwareDriver.ParseDataWithCommand 才是（摄入链路
// consumers_heavy.go 在 evt.CommandTemplateID == 0 时直接丢弃样本，不会走到这里）。
// ParseData 只服务于「未知命令的兼容兜底」与「直接调用驱动的诊断/测试代码」。
//
// ## 为什么必须 fail-closed
//
// 旧实现末端有一条 `len(fields) >= 7 => parseHBAT` 的兜底猜测。真机 HTEMP 是 8 字段，
// `len >= 11` 的 HTEMP 分支不命中，于是掉进 HBAT：
//
//	(027 039 033 035 039 030 030 11000000000000000000
//	 => battery_voltage=39 battery_capacity=33 battery_charge_current=35
//	    battery_discharge_current=39 bus_voltage=30   err=<nil>
//
// 生产实测：battery_voltage 被写入 2501 条 30~45V 的假值（正常应 48~58V），
// 窗口 2026-10-18 14:46 -> 10-19 11:43 UTC。**「看起来合理」的假数据比报错危险得多。**
//
// 现在认不出形状就报错，绝不按位置猜。
func (d *TechfineInverterDriver) ParseData(raw []byte) ([]SensorData, error) {
	fields, err := techfineSplitFields(raw)
	if err != nil {
		return nil, err
	}
	// QPRTL: 单字段应答 = 协议类型查询。它没有对应命令模板（不在注册表里），
	// 也不产出物理量，只把「对端自称是 GB3024」记下来。
	if len(fields) == 1 {
		return []SensorData{{Name: "protocol_type", Value: 1, Unit: ""}}, nil
	}
	rule, ok := techfineClassifyShape(fields)
	if !ok {
		return nil, fmt.Errorf("techfine: unrecognized response shape (%d fields: %v) — "+
			"拒绝按字段位置猜测解析：错帧会被读成「看起来合理」的假数据，必须由命令上下文指定解析器",
			len(fields), fields)
	}
	if rule.Command == "" {
		return nil, fmt.Errorf("techfine: response shape %q cannot identify its command — "+
			"该形状被多条命令共用（如 HPV/HPVB），必须用 ParseDataWithCommand 提供命令上下文", rule.Name)
	}
	cmd, ok := techfineCommandByID[rule.Command]
	if !ok {
		return nil, fmt.Errorf("techfine: shape %q maps to unregistered command %q", rule.Name, rule.Command)
	}
	return cmd.Parser(d, fields)
}

// techfineSplitFields 把一个 ASCII 应答行切成字段。
// 定帧（'(' 开头、CR 结尾）由发送侧的 verifier 负责；这里只做形状校验与切分。
func techfineSplitFields(raw []byte) ([]string, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("techfine: response must start with '(', got: %q", s)
	}
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimRight(s, "\r\n\x00 ")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("techfine: empty response")
	}
	return fields, nil
}

// ============================================================================
// ParseDataWithCommand — 命令感知解析（主路径）
// ============================================================================
//
// commandWriteData 是 ConfigTemplate / CommandTemplate.WriteData 的十六进制编码
// （例如 "4854454d500d" = "HTEMP\r"）。查表命中即用该命令自己的解析器 ——
// **有上下文就绝不猜形状**。
//
// 未登记的命令（未来新增字面量、命令字节被改）才回落到 ParseData 的形状判定；
// 而 ParseData 本身也 fail-closed，不会产出「猜出来的」假数据。
func (d *TechfineInverterDriver) ParseDataWithCommand(raw []byte, commandWriteData string) ([]SensorData, error) {
	wire := strings.TrimSpace(commandWriteData)
	if wire == "" {
		// 没有上下文（诊断/测试路径）：交给形状判定，认不出会报错。
		return d.ParseData(raw)
	}
	cmd, ok := techfineCommandByWire(wire)
	if !ok {
		// 非法 hex：命令上下文本身是坏的，不能当成"没有上下文"静默继续 ——
		// 那会让一个传错参数的调用方得到"解析成功"的假象。
		if _, decErr := hex.DecodeString(wire); decErr != nil {
			return nil, fmt.Errorf("techfine: invalid command write data %q: %w", commandWriteData, decErr)
		}
		// 合法 hex 但未登记的命令（未来新增/字面量被改）：回落形状判定。
		// 形状判定 fail-closed，不会产出猜出来的数据。
		return d.ParseData(raw)
	}
	fields, err := techfineSplitFields(raw)
	if err != nil {
		return nil, fmt.Errorf("techfine %s: %w", cmd.ID, err)
	}
	return cmd.Parser(d, fields)
}

// ============================================================================
// parseHSTS — Status query
// ============================================================================
// Format: (NN MABCDEFGHIJKL...)
//   NN           – fault code (integer, decimal)
//   M            – work mode: P=Power-on, S=Standby, L=Line, B=Battery, F=Fault, D=Shutdown, X=Test
//   A-L          – 12 alarm flags (12 chars), '0'=inactive, other=active
//   A: PV馈能到负载  B: 有输出  C: 电池低电报警  D: 电池未接
//   E: 输出过载    F: 过温    G: EEPROM数据异常  H: EEPROM读写异常
//   I: PV功率过低  J: 输入电压过高  K: 电池电压过高  L: 风扇异常

// workModeMap maps work mode letters to numeric codes for LastData storage.
var workModeMap = map[byte]float64{'P': 0, 'S': 1, 'L': 2, 'B': 3, 'F': 4, 'D': 5, 'X': 6}

// plausible 是一组统一的物理边界，供各解析器做"这个值不可能是真的"判断。
//
// 为什么需要统一（2026-10-19）：解析器靠命令字面量精确分派之后，仍可能收到
// **错配帧**（上游把别的命令的应答投递过来）。此时 parseXXX 会按自己的位置
// 硬取值，产出一个"看起来合理"的数字 —— 这类假数据比报错危险得多：
// 运维会去查硬件，而真相是帧错了。
//
// 边界取**宽于任何真实工况、窄于任何明显错位**的值：
//
//	· 家用储能市电 220V±20% ⇒ [80,300] 足够了，但留到 [50,600] 容纳宽压机型
//	· 输出同样 [50,600]；频率 50/60Hz ⇒ [40,70] 已很宽
//	· 功率上限按 20kW 机型留到 50000W/VA
//	· 百分比 [0,120]（允许短暂过载显示 100+）
//
// 判据的目的不是精确描述设备，而是**拦住错位**：把 380V 母线电压当成
// 220V 市电是拦不住的（都在区间内），但把 000.0 或 1e12 当市电一定拦得住。
var (
	minVoltAC  = 50.0
	maxVoltAC  = 600.0
	minFreq    = 40.0
	maxFreq    = 70.0
	maxPower   = 50000.0
	maxPercent = 120.0
)

// checkRange 校验 v 是否落在 [min,max] 内，越界返回带上下文的错误。
func checkRange(v, min, max float64, field string) error {
	if v < min || v > max {
		return fmt.Errorf("techfine: %s=%.3f out of plausible range [%.1f,%.1f] — "+
			"该帧与该动作不符，拒绝采信以免写入假数据（错位的帧比缺失更难排查）",
			field, v, min, max)
	}
	return nil
}

// hbatMaxPlausibleVoltage 是 HBAT 电压字段的物理上界（V）。
//
// 为什么需要：ParseData 靠形状挑解析器，任何一次形状放宽都可能让**别的命令**
// 的应答落到 parseHBAT。若不设界，"0001000000000000" 会被读成 1e12 伏特并
// 写库（2026-10-19 实测发生过，污染 359 条）。
//
// 取 1000V 而不是精确的 60V：家用储能 48V/96V 体系远低于此，而 1000V 仍能
// 拦住所有"把长整数字符串当电压"的误判。界要能拦住**错误类型**，不是要
// 精确描述电池。
const hbatMaxPlausibleVoltage = 1000.0

func (d *TechfineInverterDriver) parseHSTS(fields []string) ([]SensorData, error) {
	if len(fields) < 2 {
		return nil, fmt.Errorf("techfine HSTS: need >=2 fields, got %d", len(fields))
	}

	faultCode, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil, fmt.Errorf("techfine HSTS: invalid fault code %q", fields[0])
	}

	statusStr := fields[1]
	if len(statusStr) < 1 {
		return nil, fmt.Errorf("techfine HSTS: empty status string")
	}

	workModeByte := statusStr[0]
	// 2026-10-21 协议核对时发现（fail-open）：旧实现把未知模式字符映射成 -1 并继续，
	// 于是 HBMS1 的 16 位状态串（首字符 '0'）会被 read_status 上下文当成状态应答解析成功，
	// 产出 work_mode=-1 + 12 个"告警位"（纯属假数据）。协议只定义 P/S/L/B/F/D/X。
	workModeVal, ok := workModeMap[workModeByte]
	if !ok {
		return nil, fmt.Errorf("techfine HSTS: unknown work mode %q (协议 V0.5 §5.2 只定义 P/S/L/B/F/D/X) — "+
			"该帧不是状态应答，拒绝按告警位解读", string(workModeByte))
	}

	// Alarm flags are positions 1..12 in the status string
	// 状态串必须是 1 个模式字符 + 12 个告警位（共 13 字符）。
	//
	// 2026-10-03 固件审查发现（P0，fail-open）：原实现在串短于 13 时
	// 只取到多少算多少，缺失的告警位在下面被默认成 0 —— 于是
	// "(00 P0000\r"（告警串被截到 5 字符）会返回 12 个 alarm_* 且**全为 0**，
	// 即"一切正常"。这是最危险的一类静默假数据：设备可能正在报警，
	// 界面显示无告警。放行短读后这种帧能到达解析器，故必须在此 fail-closed。
	if len(statusStr) < 13 {
		return nil, fmt.Errorf("techfine HSTS: status string truncated (%d chars, need 13: 1 mode + 12 alarm flags) — 拒绝采信以免把截断帧读成\"无告警\"", len(statusStr))
	}
	alarmFlags := statusStr[1:13]

	// Map alarm flag positions to sensor names
	alarmMap := []string{
		"alarm_pv_to_load",          // A (pos 0)
		"alarm_output",              // B (pos 1)
		"alarm_battery_low",         // C (pos 2)
		"alarm_battery_missing",     // D (pos 3)
		"alarm_overload",            // E (pos 4)
		"alarm_overtemp",            // F (pos 5)
		"alarm_eeprom_data",         // G (pos 6)
		"alarm_eeprom_rw",           // H (pos 7)
		"alarm_pv_low",              // I (pos 8)
		"alarm_input_overvoltage",   // J (pos 9)
		"alarm_battery_overvoltage", // K (pos 10)
		"alarm_fan_error",           // L (pos 11)
	}

	result := []SensorData{
		{Name: "fault_code", Value: float64(faultCode), Unit: ""},
		{Name: "work_mode", Value: workModeVal, Unit: ""},
	}

	for i, name := range alarmMap {
		val := 0.0
		if i < len(alarmFlags) && alarmFlags[i] != '0' {
			val = 1.0
		}
		result = append(result, SensorData{Name: name, Value: val, Unit: ""})
	}

	return result, nil
}

// ============================================================================
// parseHGRID — Grid info
// ============================================================================
// Format: (NNN.N MM.M AAA BBB CC DD ...)
//   NNN.N – grid voltage (V)
//   MM.M  – grid frequency (Hz)
//   AAA   – loss voltage high (V)
//   BBB   – loss voltage low (V)
//   CC    – frequency high (Hz)
//   DD    – frequency low (Hz)

func (d *TechfineInverterDriver) parseHGRID(fields []string) ([]SensorData, error) {
	if len(fields) < 6 {
		return nil, fmt.Errorf("techfine HGRID: need >=6 fields, got %d", len(fields))
	}
	// 形状绑定（2026-10-21）：HOP 形状的帧不得按 HGRID 解读。
	// 纵深防御：即使上游把 read_output 的应答交给 read_grid，也不能产出市电读数。
	if techfineLooksLikeHOP(fields) {
		return nil, fmt.Errorf("techfine HGRID: field 7 %q has a decimal point — "+
			"该帧是 HOP（输出）形状，不是市电应答；拒绝按 HGRID 位置解读", fields[7])
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid frequency %q", fields[1])
	}

	// 物理闸门：市电电压/频率错位时数值往往仍"看起来正常"，
	// 但 000.0V 或 380V+ 这类一定会被下面拦住。
	if err := checkRange(voltage, minVoltAC, maxVoltAC, "grid_voltage"); err != nil {
		return nil, err
	}
	if err := checkRange(freq, minFreq, maxFreq, "grid_frequency"); err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "grid_voltage", Value: voltage, Unit: "V"},
		{Name: "grid_frequency", Value: freq, Unit: "Hz"},
	}, nil
}

// ============================================================================
// parseHOP — Output info
// ============================================================================
// Format: (NNN.N MM.M AAAAA BBBBB CCC DDD EEEEE FFF.F ...)
//   NNN.N  – output voltage (V)
//   MM.M   – output frequency (Hz)
//   AAAAA  – apparent power (VA)
//   BBBBB  – active power (W)
//   CCC    – load percentage (%)
//   DDD    – DC component
//   EEEEE  – internal data
//   FFF.F  – inductor current (A)

func (d *TechfineInverterDriver) parseHOP(fields []string) ([]SensorData, error) {
	if len(fields) < 8 {
		return nil, fmt.Errorf("techfine HOP: need >=8 fields, got %d", len(fields))
	}
	// 形状绑定（2026-10-21 复核发现）：HOP 的第 8 位是电感电流（带小数）、
	// 功率位是 4+ 定宽数字。不校验的话 read_output 会接受全部市电帧
	// （实测 10,787/10,787），把 280V 线电压读成输出电压、把损失电压读成功率。
	if !techfineLooksLikeHOP(fields) {
		return nil, fmt.Errorf("techfine HOP: frame does not match the output-response shape "+
			"(fields[7]=%q, fields[2]=%q) — 该帧不是输出应答，拒绝按 HOP 位置解读",
			safeField(fields, 7), safeField(fields, 2))
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid frequency %q", fields[1])
	}

	apparentPower, err := parseRequiredFloat(fields, 2, "output_apparent_power")
	if err != nil {
		return nil, err
	}
	activePower, err := parseRequiredFloat(fields, 3, "output_active_power")
	if err != nil {
		return nil, err
	}
	loadPercent, err := parseRequiredFloat(fields, 4, "output_load_percent")
	if err != nil {
		return nil, err
	}

	// 物理闸门（同 HGRID 的理由）
	if err := checkRange(voltage, minVoltAC, maxVoltAC, "output_voltage"); err != nil {
		return nil, err
	}
	if err := checkRange(freq, minFreq, maxFreq, "output_frequency"); err != nil {
		return nil, err
	}
	if err := checkRange(apparentPower, 0, maxPower, "output_apparent_power"); err != nil {
		return nil, err
	}
	if err := checkRange(activePower, 0, maxPower, "output_active_power"); err != nil {
		return nil, err
	}
	if err := checkRange(loadPercent, 0, maxPercent, "output_load_percent"); err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "output_voltage", Value: voltage, Unit: "V"},
		{Name: "output_frequency", Value: freq, Unit: "Hz"},
		{Name: "output_apparent_power", Value: apparentPower, Unit: "VA"},
		{Name: "output_active_power", Value: activePower, Unit: "W"},
		{Name: "output_load_percent", Value: loadPercent, Unit: "%"},
	}, nil
}

// ============================================================================
// parseHBAT — Battery info
// ============================================================================
// Format: (AA BBB.B CCC DDD EEEEE FFF GHIJK...)
//   AA     – battery cell count
//   BBB.B  – battery voltage (V)
//   CCC    – battery capacity (%)
//   DDD    – charge current (A)
//   EEEEE  – discharge current (A)
//   FFF    – BUS voltage (V)
//   GHIJK  – charge switches (5 packed flags)

func (d *TechfineInverterDriver) parseHBAT(fields []string) ([]SensorData, error) {
	// **恰好 7 字段**（2026-10-21 收紧）。
	//
	// 历史实现是 len>=7，于是 8 字段的 HTEMP 帧在命令上下文缺失时会掉进来，
	// 被读成 battery_voltage=37 / battery_capacity=31 ... 并静默入库
	// （生产实测污染 2501 条）。真机 HBAT 在 30 小时 10786 帧里全部是 7 字段，
	// 所以这里没有理由接受第 8 个字段。
	if len(fields) != 7 {
		return nil, fmt.Errorf("techfine HBAT: need exactly 7 fields, got %d (fields=%v) — "+
			"真机电池应答是 7 字段；其它字段数是错配帧，拒绝按 HBAT 位置解读", len(fields), fields)
	}

	// 形状绑定：HBAT 的 fields[0] 是**单体数**（整数，如 "04"）；
	// 而 HGRID 的 fields[0] 是电压（带小数点，如 "52.1"）。两者字段数都是 7，
	// 单看数量区分不了 —— 这是精确分派之后仍然必须有的第二道闸门。
	//
	// 为什么在精确分派之后还要它：解析器也可能被**非控制路径**直接调用
	// （轮询 CMD_SAMPLE），而 VerifyControlAction 的族校验只覆盖控制动作。
	// 一旦上游把错配帧喂进来，"强行按 HBAT 位置取值"就会产出
	// battery_voltage=8.0 这种看起来合理的假数据 —— 比报错危险得多。
	if strings.Contains(fields[0], ".") {
		return nil, fmt.Errorf("techfine HBAT: first field %q is not an integer cell count — "+
			"该帧不是电池应答（很可能是 HGRID），拒绝按 HBAT 位置解读", fields[0])
	}

	voltage, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HBAT: invalid voltage %q", fields[1])
	}

	// 合理性闸门（2026-10-19 真机缺陷后加）。
	//
	// ParseData 靠**形状**猜命令类型。HBMS1 的标志串是 16 位二进制，而闸门
	// 曾写死 len==8 ⇒ 不匹配 ⇒ 落到本函数 ⇒ 把 "0001000000000000" 读成
	// 1e12 V 写进数据库（实测污染 359 条）。闸门已修，但**不能只修根因**：
	// 任何一次判别顺序调整都可能让同类形状再次落到这里。
	//
	// 判据取物理边界而非魔法数：48V 电池系统最高约 60V，96V 系统约 120V；
	// 取 0..1000V 覆盖所有家用场景，超出即"这不是电压字段"，fail-closed。
	// **宁可丢弃这一帧，也不写一个看起来像真读数的垃圾值** —— 后者会让
	// 运维去查电池，而真相是解析器认错了帧。
	if voltage < 0 || voltage > hbatMaxPlausibleVoltage {
		return nil, fmt.Errorf("techfine HBAT: implausible battery voltage %.1f V (field %q) — "+
			"响应形状与该动作不符，拒绝采信以免写入垃圾值", voltage, fields[1])
	}

	capacity, err := parseRequiredFloat(fields, 2, "battery_capacity")
	if err != nil {
		return nil, err
	}
	chargeCurrent, err := parseRequiredFloat(fields, 3, "battery_charge_current")
	if err != nil {
		return nil, err
	}
	dischargeCurrent, err := parseRequiredFloat(fields, 4, "battery_discharge_current")
	if err != nil {
		return nil, err
	}
	busVoltage, err := parseRequiredFloat(fields, 5, "bus_voltage")
	if err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "battery_voltage", Value: voltage, Unit: "V"},
		{Name: "battery_capacity", Value: capacity, Unit: "%"},
		{Name: "battery_charge_current", Value: chargeCurrent, Unit: "A"},
		{Name: "battery_discharge_current", Value: dischargeCurrent, Unit: "A"},
		{Name: "bus_voltage", Value: busVoltage, Unit: "V"},
	}, nil
}

// ============================================================================
// parsePV — PV info (HPV / HPVB)
// ============================================================================
// Format: (AAA.A BB.B CCCCC ...)
//   AAA.A  – PV voltage (V)
//   BB.B   – PV current (A)
//   CCCCC  – PV power (W)
//
// prefix is "pv1" or "pv2" depending on the command sent.

func (d *TechfineInverterDriver) parsePV(fields []string, prefix string) ([]SensorData, error) {
	if len(fields) < 3 {
		return nil, fmt.Errorf("techfine PV: need >=3 fields, got %d", len(fields))
	}
	// 形状绑定（2026-10-21 复核发现）：不加校验时 read_pv1/read_pv2 会接受
	// 全部输出帧（实测 10,787/10,787），把输出电流 49.9A 读成 pv1_current。
	if !techfineLooksLikePV(fields) {
		return nil, fmt.Errorf("techfine PV: frame does not match the PV-response shape "+
			"(%d fields, fields[3]=%q) — 该帧不是 PV 应答，拒绝按 pv1/pv2 位置解读",
			len(fields), safeField(fields, 3))
	}

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine PV: invalid voltage %q", fields[0])
	}

	current, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine PV: invalid current %q", fields[1])
	}

	// 功率字段必须可解析，不能用 parseFloat 的"解析失败返回 0"兜底。
	//
	// 2026-10-03 固件审查发现（P0）：截断帧 "(220.5 08.0 0\r" 的第三字段是 "0"，
	// parseFloat 把它读成 0，于是返回 pv2_power=0 —— 一个看起来正常但错误的功率读数。
	// 真实的 "00960" 表示 960W；把截断读成 0W 会让用户以为逆变器没在发电。
	// 判据：字段必须是非空且可被 ParseFloat 解析；"0" 本身是合法值，但
	// 被截断成更短的形状时字段会缺失或非数字，故必须显式解析并报错。
	powerText := strings.TrimSpace(fields[2])
	if powerText == "" {
		return nil, fmt.Errorf("techfine PV: power field is empty — 帧被截断")
	}
	// 功率字段是协议规定的定宽零填充 "CCCCC"（见上方 Format 注释与
	// TestTechfine_HPV_WithCommand 的 "00960"、"00400" 夹具）。
	//
	// 2026-10-03 固件审查发现（P0）：仅"可解析"不够 —— 截断帧
	// "(220.5 08.0 0\r" 的功率字段是单个 "0"，ParseFloat 读成 0，
	// 于是返回 pv2_power=0W。而完整帧的 "00960" 是 960W。
	// 把"被截断"读成"0 瓦"会让用户以为逆变器停止发电 —— 静默假数据。
	// 因此要求宽度至少 4（协议 5，留 1 位容差以兼容不补零的固件变体）；
	// 单字符 "0" 这类畸形形状被拒。
	if len(powerText) < 4 {
		return nil, fmt.Errorf("techfine PV: power field %q too short (%d chars, expected the fixed-width %q form) — 帧形状不完整，拒绝采信",
			powerText, len(powerText), "CCCCC")
	}
	power, err := strconv.ParseFloat(powerText, 64)
	if err != nil {
		return nil, fmt.Errorf("techfine PV: invalid power %q: %w", powerText, err)
	}

	return []SensorData{
		{Name: prefix + "_voltage", Value: voltage, Unit: "V"},
		{Name: prefix + "_current", Value: current, Unit: "A"},
		{Name: prefix + "_power", Value: power, Unit: "W"},
	}, nil
}

// ============================================================================
// parseHTEMP — 温度与风扇状态
// ============================================================================
//
// ## 真机格式（2026-10-19 实测 40 帧；2026-10-21 生产 5545 帧复核）
//
//	(041 037 032 033 041 100 100 11000000000000000000
//	 └─── 5 个温度 ───┘ └2 风扇转速┘ └── 20 位状态串 ──┘
//
// ## 位序（协议 V0.5 §HTEMP + 实测双重确认）
//
//	[0] pv_temp  [1] inverter_temp  [2] boost_temp  [3] transformer_temp  [4] max_temp
//	[5] fan1_speed (%)   [6] fan2_speed (%)      ← 实测只有 30/50/100 三档
//	[7] 状态位串（前两位 = 风扇 1/2 启停，实测与温升同步切换）
//
// ## 2026-10-21 修正：第 5/6 位曾被误标为 pv2_temp / dc_rectifier_temp
//
// 旧实现把 [0..6] 全部当温度，于是风扇转速被写成 30/50/100 ℃。三条独立证据：
//
//	· 协议文档（本文件旧格式注释、V0.5 §HTEMP）：FFF/GGG = fan1/fan2 speed (%)；
//	· 实测 5545 帧：[5]/[6] 只有 {30,50,100} 三档，且 [5]==[6] 100%（双风扇同步）；
//	· 实测：[5] 取 50/100 只出现在状态位串前两位为 "11"（风扇运转）时。
//
// 修正后 8 字段形状产出 5 温度 + 2 风扇转速 + 2 风扇状态 = 9 个传感器；
// pv2_temp/dc_rectifier_temp 只由 11 字段布局产出（协议 V0.5 的完整形状是
// AAA..EEE 5 温度 + FFF/GGG 2 风扇转速 + 状态位 + PV2 温度 + DC 整流温度）。
//
// 本机是 PV1-only 机型，固定返回 8 字段 —— 即「5 温度 + 2 转速 + 状态串」，
// 后两位（PV2 温度 / DC 整流温度）根本没出现，与硬件事实一致（机器只有一路 PV）。
// 旧实现把 FFF/GGG 当成 pv2_temp/dc_rectifier_temp，等于凭空造出两个不存在的温度位。
//
// ## 为什么仍然保留 11 字段分支
//
// 老固件/老机型可能真的发 11 字段（5 温度 + 2 风扇转速 + 2 风扇状态 + 2 额外温度）。
// 两种形状靠**字段数**区分（恰好 8 vs 恰好 11），都能给出合理结果，互不干扰。
func (d *TechfineInverterDriver) parseHTEMP(fields []string) ([]SensorData, error) {
	// 8 字段真机形状：5 温度 + 2 风扇转速 + 1 状态位串
	if len(fields) == 8 {
		return d.parseHTEMPCompact(fields)
	}

	// 协议 V0.5 §5.8 的完整形状是 **10 个空格分隔字段**：
	//
	//	(AAA BBB CCC DDD EEE FFF GGG HI JJJ KKKOOOOOOOOOO
	//
	// 有两处**没有空格**，必须按位置切：
	//   · HI  是一个字段（风扇1状态 + 风扇2状态两位连写）；
	//   · KKK 与该命令的 O 标志位直接相连（协议原文写作 KKKOOOOOOOOOO）。
	//
	// 2026-10-21 按协议原件修正：旧实现按 **11 个字段**、把 HI 拆成两个字段解析，
	// 与协议不符（其注释还写着"HI 是一个 2 字符字段"，自相矛盾）。
	if len(fields) != 10 {
		return nil, fmt.Errorf("techfine HTEMP: need exactly 8 (真机截断形状) or exactly 10 (协议 V0.5 §5.8 完整形状) fields, got %d (fields=%v) — 拒绝采信", len(fields), fields)
	}

	// [0..8] 是纯数字：5 温度 + 2 风扇转速 + PV2 温度
	var vals [9]float64
	for i := 0; i < 9; i++ {
		v, err := parseRequiredFloat(fields, i, "HTEMP")
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	if err := checkTemperatures(vals[:5], "HTEMP"); err != nil {
		return nil, err
	}
	if err := checkFanSpeeds(vals[5:7], "HTEMP"); err != nil {
		return nil, err
	}
	if err := checkTemperatures(vals[8:9], "HTEMP"); err != nil {
		return nil, err
	}

	// [7] = "HI"：风扇 1/2 状态，两位各 0/1。
	fanStatus := fields[7]
	if len(fanStatus) < 2 || !isAllDigits(fanStatus) {
		return nil, fmt.Errorf("techfine HTEMP: fan status field %q is not two digits (fan1/fan2) — "+
			"拒绝采信以免把缺失位读成「风扇停转」", fanStatus)
	}
	fan1Status, err := parseRequiredFloat([]string{fanStatus[0:1]}, 0, "HTEMP fan1_status")
	if err != nil {
		return nil, err
	}
	fan2Status, err := parseRequiredFloat([]string{fanStatus[1:2]}, 0, "HTEMP fan2_status")
	if err != nil {
		return nil, err
	}
	if err := checkFanStatuses([]float64{fan1Status, fan2Status}, "HTEMP"); err != nil {
		return nil, err
	}

	// [9] = "KKK" + O 标志位（无空格）：按协议定宽取前 3 位。
	dcDigits, ok := leadingFixedDigits(fields[9], 3)
	if !ok {
		return nil, fmt.Errorf("techfine HTEMP: DC rectifier temperature field %q does not start with a 3-digit value — "+
			"该帧与协议 §5.8 的 KKKOOOOOOOOOO 形状不符", fields[9])
	}
	dcTemp, err := parseRequiredFloat([]string{dcDigits}, 0, "HTEMP dc_rectifier_temp")
	if err != nil {
		return nil, err
	}
	if err := checkTemperatures([]float64{dcTemp}, "HTEMP"); err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "pv_temp", Value: vals[0], Unit: "°C"},
		{Name: "inverter_temp", Value: vals[1], Unit: "°C"},
		{Name: "boost_temp", Value: vals[2], Unit: "°C"},
		{Name: "transformer_temp", Value: vals[3], Unit: "°C"},
		{Name: "max_temp", Value: vals[4], Unit: "°C"},
		{Name: "fan1_speed", Value: vals[5], Unit: "%"},
		{Name: "fan2_speed", Value: vals[6], Unit: "%"},
		{Name: "fan1_status", Value: fan1Status, Unit: ""},
		{Name: "fan2_status", Value: fan2Status, Unit: ""},
		{Name: "pv2_temp", Value: vals[8], Unit: "°C"},
		{Name: "dc_rectifier_temp", Value: dcTemp, Unit: "°C"},
	}, nil
}

// leadingFixedDigits 取固定宽度的前导十进制数字（协议里 KKK 与 O 标志位无空格相连）。
func leadingFixedDigits(s string, width int) (string, bool) {
	if len(s) < width {
		return "", false
	}
	for i := 0; i < width; i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	return s[:width], true
}

// parseHTEMPCompact 解析 8 字段真机形状：5 温度 + 2 风扇转速 + 1 个 20 位状态串。
//
// 位序依据见 parseHTEMP 的注释（协议文档 + 5545 帧实测双重确认）。
// max_temp 由设备自己给出（实测恒等于前 4 路最大值 5545/5545），这里不重算 ——
// 若将来某台设备的 max_temp 与前 4 路不一致，那是设备侧语义问题，应如实呈现。
func (d *TechfineInverterDriver) parseHTEMPCompact(fields []string) ([]SensorData, error) {
	// 用栈数组而不是 make([]float64, 7)：这条路径是每 30 秒一次的稳态热路径。
	// 错误信息里的字段序号由 parseRequiredFloat 统一拼（只在**出错时**格式化），
	// 不在循环里预先 fmt.Sprintf —— 那会让成功路径也付出分配代价。
	var vals [7]float64
	for i := 0; i < 7; i++ {
		v, err := parseRequiredFloat(fields, i, "HTEMP")
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	// 物理闸门：温度不可能低于 -40°C 或高于 150°C；风扇转速是百分比。
	// 宁可丢这一帧，也不写一个看起来像真读数的垃圾值（那会让人去查散热，
	// 而真相是解析错位）。
	if err := checkTemperatures(vals[:5], "HTEMP"); err != nil {
		return nil, err
	}
	if err := checkFanSpeeds(vals[5:7], "HTEMP"); err != nil {
		return nil, err
	}

	statusBits := strings.TrimSpace(fields[7])
	if !isAllBinary(statusBits) {
		return nil, fmt.Errorf("techfine HTEMP: status field %q is not a binary string — "+
			"第 8 字段应为风扇/状态位串", statusBits)
	}
	if len(statusBits) < 2 {
		return nil, fmt.Errorf("techfine HTEMP: status field %q too short (%d bits, need >=2: fan1/fan2) — "+
			"拒绝采信以免把缺失位读成「风扇停转」", statusBits, len(statusBits))
	}

	return []SensorData{
		{Name: "pv_temp", Value: vals[0], Unit: "°C"},
		{Name: "inverter_temp", Value: vals[1], Unit: "°C"},
		{Name: "boost_temp", Value: vals[2], Unit: "°C"},
		{Name: "transformer_temp", Value: vals[3], Unit: "°C"},
		{Name: "max_temp", Value: vals[4], Unit: "°C"},
		{Name: "fan1_speed", Value: vals[5], Unit: "%"},
		{Name: "fan2_speed", Value: vals[6], Unit: "%"},
		{Name: "fan1_status", Value: bitAt(statusBits, 0), Unit: ""},
		{Name: "fan2_status", Value: bitAt(statusBits, 1), Unit: ""},
	}, nil
}

// bitAt 返回位串第 i 位的数值（0 或 1）；越界返回 0。
func bitAt(bits string, i int) float64 {
	if i < 0 || i >= len(bits) {
		return 0
	}
	if bits[i] == '1' {
		return 1
	}
	return 0
}

// ============================================================================
// parseHGEN — Energy generation info
// ============================================================================
// Format: (AAAAAA BB:BB CC.CCC DDDD.D EEEE.E FFFFFFFFF.F ...)
//   AAAAAA      – system date (YYYYMM)
//   BB:BB       – system time (HH:MM)
//   CC.CCC      – daily energy (kWh)
//   DDDD.D      – monthly energy (kWh)
//   EEEE.E      – yearly energy (kWh)
//   FFFFFFFFF.F – total energy (kWh)

func (d *TechfineInverterDriver) parseHGEN(fields []string) ([]SensorData, error) {
	if len(fields) < 6 {
		return nil, fmt.Errorf("techfine HGEN: need >=6 fields, got %d", len(fields))
	}
	// 形状绑定（2026-10-21 复核发现）：HGEN 的第 2 位是 HH:MM。
	//
	// 不加这条，parseHGEN 会成为**万能接收器**：任何 >=6 字段、且 [2..5] 能解析
	// 成数字的帧都会被读成"日/月/年/总发电量"。交叉命令矩阵实测：read_energy
	// 接受全部 70,115 帧 —— 温度帧当能量会得到 daily=28/monthly=31/... kWh，
	// 全是"看起来合理"的假数据，与 2026-10-19 的电池污染同型。
	if !strings.Contains(fields[1], ":") {
		return nil, fmt.Errorf("techfine HGEN: field 1 %q is not an HH:MM timestamp — "+
			"该帧不是发电量应答（错配帧比缺失更难排查）", fields[1])
	}

	daily, err := parseRequiredFloat(fields, 2, "daily_energy")
	if err != nil {
		return nil, err
	}
	monthly, err := parseRequiredFloat(fields, 3, "monthly_energy")
	if err != nil {
		return nil, err
	}
	yearly, err := parseRequiredFloat(fields, 4, "yearly_energy")
	if err != nil {
		return nil, err
	}
	total, err := parseRequiredFloat(fields, 5, "total_energy")
	if err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "daily_energy", Value: daily, Unit: "kWh"},
		{Name: "monthly_energy", Value: monthly, Unit: "kWh"},
		{Name: "yearly_energy", Value: yearly, Unit: "kWh"},
		{Name: "total_energy", Value: total, Unit: "kWh"},
	}, nil
}

// ============================================================================
// parseHBMS1 — BMS info
// ============================================================================
// Format: (AA b7b6b5b4b3b2b1b0 c7c6c5c4c3c2c1c0 BBB.B CCC.C DDD.D EEE FFFF.F GGGG.G HHHHH ...)
//   AA      – protocol type
//   b7..b0  – BMS status 1 (8 binary digits, MSB first: b7 is first char, b0 is last)
//             b7: BMS通信正常
//             b6: BMS低电报警标志
//             b5: BMS低电故障标志
//             b4: BMS允许充电标志
//             b3: BMS允许放电标志
//             b2: BMS充电过流标志
//             b1: BMS放电过流标志
//             b0: BMS温度过低标志
//   c7..c0  – BMS status 2 (8 binary digits)
//   BBB.B   – discharge voltage limit (V)
//   CCC.C   – charge voltage limit (V)
//   DDD.D   – charge current limit (A)
//   EEE     – SOC (%)
//   FFFF.F  – charge current (A)
//   GGGG.G  – discharge current (A)
//   HHHHH   – average temperature (0.01K, convert: /100 - 273.15)

func (d *TechfineInverterDriver) parseHBMS1(fields []string) ([]SensorData, error) {
	if len(fields) < 10 {
		return nil, fmt.Errorf("techfine HBMS1: need >=10 fields, got %d", len(fields))
	}

	// 协议 V0.5 §5.10：状态字段是**一个 16 位连写字符串** b7..b0 c7..c0，
	// 不是两个 8 位字段。index 0=b7 ... index 7=b0，index 8=c7（BMS 温度过高）。
	status1 := fields[1]

	// 2026-10-21：状态串不足 8 位必须**拒绝**，而不是把缺失位默认成 0。
	//
	// 旧实现是 if len(status1) >= 8 { ... } —— 长度不足时 8 个告警标志
	// 全部保持 0，等于告诉上层「BMS 一切正常」。这正是 parseHSTS 早就明确
	// 拒绝的 fail-open（见该函数里「最危险的一类静默假数据」），HBMS1 却没跟上。
	if len(status1) < 8 {
		return nil, fmt.Errorf("techfine HBMS1: status field %q too short (%d bits, need >=8) — "+
			"不足 8 位时无法判定告警，拒绝采信以免把缺失位读成「无告警」", status1, len(status1))
	}
	for i := 0; i < len(status1); i++ {
		if status1[i] != '0' && status1[i] != '1' {
			return nil, fmt.Errorf("techfine HBMS1: status field %q has non-binary char at %d — "+
				"标志位串必须全为 0/1，否则无法判定告警（拒绝把垃圾位读成「无告警」）", status1, i)
		}
	}
	// status1 = "b7b6b5b4b3b2b1b0"：index 0=b7 ... index 7=b0
	var bmsCommOK, bmsLowAlarm, bmsLowFault float64
	var bmsChargeAllowed, bmsDischargeAllowed float64
	var bmsChargeOvercurrent, bmsDischargeOvercurrent, bmsTempLow float64
	if status1[0] == '1' { // b7: BMS通信正常
		bmsCommOK = 1.0
	}
	if status1[1] == '1' { // b6: BMS低电报警标志
		bmsLowAlarm = 1.0
	}
	if status1[2] == '1' { // b5: BMS低电故障标志
		bmsLowFault = 1.0
	}
	if status1[3] == '1' { // b4: BMS允许充电标志
		bmsChargeAllowed = 1.0
	}
	if status1[4] == '1' { // b3: BMS允许放电标志
		bmsDischargeAllowed = 1.0
	}
	if status1[5] == '1' { // b2: BMS充电过流标志
		bmsChargeOvercurrent = 1.0
	}
	if status1[6] == '1' { // b1: BMS放电过流标志
		bmsDischargeOvercurrent = 1.0
	}
	if status1[7] == '1' { // b0: BMS温度过低标志
		bmsTempLow = 1.0
	}
	// c7 = BMS 温度过高标志。c6..c0 是协议标注的"内部数据"，不对外暴露。
	// 只有状态串确实有 16 位时才解析；8~15 位时**不发**该字段（不补 0 —— 缺失好过假"正常"）。
	var bmsTempHigh float64
	hasTempHigh := len(status1) >= 16
	if hasTempHigh && status1[8] == '1' {
		bmsTempHigh = 1.0
	}

	// 字段下标按协议 §5.10（旧实现整体错开一位，把 O 标志串当成了平均温度）：
	//   f[0]=AA  f[1]=状态16位  f[2]=BBB.B 放电限压  f[3]=CCC.C 充电限压
	//   f[4]=DDD.D 充电限流  f[5]=EEE SOC  f[6]=FFFF.F 充电电流
	//   f[7]=GGGG.G 放电电流  f[8]=HHHHH 平均温度(0.01K)  f[9]=O 标志
	dischargeVLimit, err := parseRequiredFloat(fields, 2, "bms_discharge_voltage_limit")
	if err != nil {
		return nil, err
	}
	chargeVLimit, err := parseRequiredFloat(fields, 3, "bms_charge_voltage_limit")
	if err != nil {
		return nil, err
	}
	chargeILimit, err := parseRequiredFloat(fields, 4, "bms_charge_current_limit")
	if err != nil {
		return nil, err
	}
	soc, err := parseRequiredFloat(fields, 5, "bms_soc")
	if err != nil {
		return nil, err
	}
	chargeCurrent, err := parseRequiredFloat(fields, 6, "bms_charge_current")
	if err != nil {
		return nil, err
	}
	dischargeCurrent, err := parseRequiredFloat(fields, 7, "bms_discharge_current")
	if err != nil {
		return nil, err
	}
	tempRaw, err := parseRequiredFloat(fields, 8, "bms_temp_raw")
	if err != nil {
		return nil, err
	}
	// HHHHH 是 0.01K 单位的 5 位定宽值 ⇒ /100.0 - 273.15 得 °C。
	//
	// ⚠ 2026-10-19 真机发现：BMS **未接入**时该字段为 "00000"，直接换算得到
	// **-273.15°C（绝对零度）** 并写进库。这不是"读数偏低"，而是"没有读数"。
	// 绝对零度物理上不可能出现，画进曲线会把 Y 轴整个拉平。
	//
	// 判据：tempRaw == 0 是设备表达"未接 BMS"的哨兵（0 K 无法表示任何工况），
	// 此时**不产出** bms_temp 字段 —— 缺失好过荒谬值，前端会显示"—"。
	hasBmsTemp := tempRaw > 0
	tempCelsius := tempRaw/100.0 - 273.15
	// 0.01K 换算后必须落在物理区间内；否则说明该位不是平均温度（形状错配），整帧拒绝。
	if hasBmsTemp {
		if err := checkTemperatures([]float64{tempCelsius}, "HBMS1"); err != nil {
			return nil, err
		}
	}

	out := []SensorData{
		{Name: "bms_comm_ok", Value: bmsCommOK, Unit: ""},
		{Name: "bms_charge_allowed", Value: bmsChargeAllowed, Unit: ""},
		{Name: "bms_discharge_allowed", Value: bmsDischargeAllowed, Unit: ""},
		{Name: "bms_low_alarm", Value: bmsLowAlarm, Unit: ""},
		{Name: "bms_low_fault", Value: bmsLowFault, Unit: ""},
		{Name: "bms_charge_overcurrent", Value: bmsChargeOvercurrent, Unit: ""},
		{Name: "bms_discharge_overcurrent", Value: bmsDischargeOvercurrent, Unit: ""},
		{Name: "bms_temp_low", Value: bmsTempLow, Unit: ""},
		// c7 = BMS 温度过高（协议 §5.10）。状态串只有 8 位时该位不存在，
		// 此时不产出该字段（宁可缺失，也不报一个假的"未过热"）。
		{Name: "bms_temp_high", Value: bmsTempHigh, Unit: ""},
		{Name: "bms_soc", Value: soc, Unit: "%"},
		{Name: "bms_charge_current", Value: chargeCurrent, Unit: "A"},
		{Name: "bms_discharge_current", Value: dischargeCurrent, Unit: "A"},
		{Name: "bms_charge_voltage_limit", Value: chargeVLimit, Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Value: dischargeVLimit, Unit: "V"},
		{Name: "bms_charge_current_limit", Value: chargeILimit, Unit: "A"},
	}

	// bms_temp 只在设备给了有效值时才产出（见上方 hasBmsTemp 的说明）。
	// 不产出而不是给 0/-273.15：前者前端显示"—"，后者会被当成真实读数。
	if hasBmsTemp {
		out = append(out, SensorData{Name: "bms_temp", Value: tempCelsius, Unit: "°C"})
	}
	if !hasTempHigh {
		// 状态串不足 16 位：c7 不存在，剔除该字段（缺失好过假的"未过热"）。
		filtered := out[:0]
		for _, sd := range out {
			if sd.Name != "bms_temp_high" {
				filtered = append(filtered, sd)
			}
		}
		out = filtered
	}
	return out, nil
}

// ============================================================================
// parseHIMSG1 — Software version info
// ============================================================================
// Format: (NNNN.NN AAAABBCC DD
//   NNNN.NN  – software version number
//   AAAABBCC – software date (YYYYMMDD as 8-digit integer)
//   DD       – (additional data, ignored)

func (d *TechfineInverterDriver) parseHIMSG1(fields []string) ([]SensorData, error) {
	if len(fields) < 2 {
		return nil, fmt.Errorf("techfine HIMSG1: need >=2 fields, got %d", len(fields))
	}
	// 形状绑定（2026-10-21）：第 2 位必须是 8 位整数日期（YYYYMMDD）。
	// 不加这条，市电帧（f[1]="49.9"）也会被当成版本应答。
	// 判据与本注册表里 HIMSG1 的形状规则**必须一致**：8 位纯数字、且不能是全 0/1
	// （HBMS1 的二进制状态串 "10010000" 也是 8 位数字，会被误读成日期）。
	if len(fields[1]) != 8 || !isAllDigits(fields[1]) || isAllBinary(fields[1]) {
		return nil, fmt.Errorf("techfine HIMSG1: date field %q is not an 8-digit YYYYMMDD — "+
			"该帧不是版本应答（错配帧比缺失更难排查）", fields[1])
	}

	// 版本号 "0000.03" 是十进制记法（MMMM.NN），直接按小数解析 = 0.03。
	//
	// 旧实现手工拆成 major + minor/10000 得到 0.0003 —— 那不是协议语义；
	// 而且 minorInt 的 Atoi 错误被 "_" 吞掉，非法版本串会静默变成 0.0。
	versionVal, err := parseRequiredFloat(fields, 0, "software_version")
	if err != nil {
		return nil, err
	}
	// 日期 "20230220" 是 8 位整数，float64 可精确表示（< 2^53）。
	dateVal, err := parseRequiredFloat(fields, 1, "software_date")
	if err != nil {
		return nil, err
	}

	return []SensorData{
		{Name: "software_version", Value: versionVal, Unit: ""},
		{Name: "software_date", Value: dateVal, Unit: ""},
	}, nil
}

// ============================================================================
// parseHEEP1 — EEPROM settings (basic parse, stores key fields)
// ============================================================================
// Format: (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
//   A     – 工作模式：0=UTI（市电优先）、1=SUB（太阳能优先）、2=SBU（电池优先）
//           协议 V0.5「HEEP1 响应与写后核对字段」表：A | 工作模式：0 UTI、1 SUB、2 SBU
//           旧代码把它叫 eeprom_model_source（机型来源），名称与语义都不对
//   BBB   – max charge current
//   CCC   – (config data)
//   D     – input voltage range
//   E     – battery type
//   F     – (config)
//   G     – (config)
//   HHH   – output voltage
//   I     – output frequency
//   ... remaining fields are EEPROM settings
//
// This is a basic parser that stores selected fields. Full EEPROM parsing
// can be extended as needed.

func (d *TechfineInverterDriver) parseHEEP1(fields []string) ([]SensorData, error) {
	if len(fields) < 15 {
		return nil, fmt.Errorf("techfine HEEP1: need >=15 fields, got %d", len(fields))
	}

	workMode, err := parseRequiredFloat(fields, 0, "eeprom_work_mode")
	if err != nil {
		return nil, err
	}
	maxChargeCurrent, err := parseRequiredFloat(fields, 1, "eeprom_max_charge_current")
	if err != nil {
		return nil, err
	}
	outputVoltage, err := parseRequiredFloat(fields, 7, "eeprom_output_voltage")
	if err != nil {
		return nil, err
	}
	freqFlag, err := parseRequiredFloat(fields, 8, "eeprom_output_frequency_flag")
	if err != nil {
		return nil, err
	}

	// fields[8] = output frequency flag (0=50Hz, 1=60Hz)
	freqValue := 50.0
	if freqFlag == 1 {
		freqValue = 60.0
	}

	// 注意：本 parser 固定产出 4 个传感器，注册表里的 MinSensors 必须等于 4。
	// 2026-10-21 前注册表写的是 15，导致 read_eeprom 动作永远不可能成功。
	result := []SensorData{
		{Name: "eeprom_work_mode", Value: workMode, Unit: ""},
		{Name: "eeprom_max_charge_current", Value: maxChargeCurrent, Unit: "A"},
		{Name: "eeprom_output_voltage", Value: outputVoltage, Unit: "V"},
		{Name: "eeprom_output_frequency", Value: freqValue, Unit: "Hz"},
	}

	return result, nil
}

// ============================================================================
// Helpers
// ============================================================================

// safeField 安全取第 i 个字段，越界返回空串（只用于构造错误信息）。
func safeField(fields []string, i int) string {
	if i < 0 || i >= len(fields) {
		return ""
	}
	return fields[i]
}

// parseRequiredFloat 解析 fields[i] 为 float64，失败**报错而不是返回 0**。
//
// 2026-10-21 重构：旧的 parseFloat 解析失败返回 0，被 6 个 parser 共约 25 个字段
// 使用。0 是一个完全合法的物理读数，于是「字段是垃圾串」会变成「读数为 0」——
// 静默假数据。同一个文件里的 parsePV 早就用三十行论证过「截断帧不能读成 0W」，
// 但那条纪律只覆盖了 PV 功率一个字段。
func parseRequiredFloat(fields []string, i int, what string) (float64, error) {
	if i < 0 || i >= len(fields) {
		return 0, fmt.Errorf("techfine %s: missing field %d (frame has %d fields)", what, i, len(fields))
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
	if err != nil {
		return 0, fmt.Errorf("techfine %s: field %d (%q) is not numeric: %w", what, i, fields[i], err)
	}
	return v, nil
}

// checkTemperatures 是温度位的统一物理闸门（-40..150°C）。
func checkTemperatures(vs []float64, what string) error {
	for i, v := range vs {
		if v < -40 || v > 150 {
			return fmt.Errorf("techfine %s: temperature field %d = %.1f°C is implausible — "+
				"该帧形状与本机型不符，拒绝采信", what, i, v)
		}
	}
	return nil
}

// checkFanSpeeds 是风扇转速位的闸门（0..120%）。
func checkFanSpeeds(vs []float64, what string) error {
	for i, v := range vs {
		if v < 0 || v > 120 {
			return fmt.Errorf("techfine %s: fan speed field %d = %.1f%% is implausible — "+
				"该位应为风扇转速百分比，实际超出 [0,120]%%；拒绝采信", what, i, v)
		}
	}
	return nil
}

// checkFanStatuses 是风扇状态位的闸门（只接受 0/1）。
func checkFanStatuses(vs []float64, what string) error {
	for i, v := range vs {
		if v != 0 && v != 1 {
			return fmt.Errorf("techfine %s: fan status field %d = %.1f is implausible — "+
				"风扇状态只应是 0/1；拒绝采信", what, i, v)
		}
	}
	return nil
}

// isAllBinary checks if a string consists only of '0' and '1' characters.
func isAllBinary(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c != '0' && c != '1' {
			return false
		}
	}
	return true
}

// isAllDigits checks if a string consists only of digit characters.
func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isAllDigitsOrEmpty checks if a string is empty or consists only of digits.
func isAllDigitsOrEmpty(s string) bool {
	if len(s) == 0 {
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// asciiToHex encodes an ASCII string as a hex string (for CommandTemplate.WriteData).
func asciiToHex(s string) string {
	return hex.EncodeToString([]byte(s))
}

// ============================================================================
// GetCommandTemplates — 可调度轮询模板（2026-10-03 恢复）
// ============================================================================
//
// 为什么恢复（这是现场缺陷的根因，不是"设计偏好"）
// ----------------------------------------------
// 2026-10-03 现场：S3 节点 UART1 上的泰琪丰逆变器**从未收到任何指令**。
// 用户的物理证据是决定性的 —— TTL↔RS232 转接板的 Tx/Rx 指示灯从未亮过，
// 说明固件根本没有在 UART1 上发送过任何字节（若只是电平不匹配，TX 灯仍会亮，
// 只是对端收不到）。
//
// 代码层因果链完全吻合：
//
//	GetCommandTemplates() 返回 nil
//	  -> CommandIsManifestCandidate 对任何命令都为 false，该边设备编码 0 条命令
//	     （生产日志原文：edge_device 4 (channel 4): no valid template_id in
//	       channel.template_ids "" (resolved 0), skipping command encoding）
//	  -> 固件 scheduler_add_channel 登记 command_count = 0
//	  -> 调度器从不投递 CMD_SAMPLE
//	  -> UART1 TX 引脚从头到尾没有电平变化 -> 转接板指示灯不亮
//
// 演进方案 C6 (2026-09-06) 删除这 11 条模板时的理由本身没错 —— 它反对的是
// "Schedulable=false 的第三态"（一次性触发命令混进轮询域）。但 C6 把**轮询**
// 一并删掉，等于让该驱动在架构上失去周期性采集能力：ControlActions 是受控
// 动作目录，只有服务端主动下发 ChannelCmdV2 才会执行，**没有任何后台路径**
// 会周期性调用它。于是"逆变器像 BMS 一样可配置定期采集"无法实现。
//
// 因此这里恢复的是**纯轮询模板**，全部 Schedulable=true（遵守 P2：第三态
// 仍然废止），与 JBD BMS 驱动的形态完全一致。受控写操作仍只存在于
// ControlActions，不进模板域。
//
// ReadLength = 0 是刻意的
// -----------------------
// 泰琪丰响应是 ASCII 行，长度不定（HSTS/HGRID/HOP/HBAT/HPV/HPVB/HTEMP/HGEN/
// HBMS1/HEEP1/HIMSG1 各不相同）。固件里 read_length 的语义分两种：
//
//	read_length > 0 -> 期望**恰好**该长度，短读判 error 0x03
//	read_length = 0 -> 走 CMD_SAMPLE 的"行空闲即帧边界"，整行原样上报
//
// 逆变器必须用后者，否则每条响应都会因长度不符被判读失败。
// 这与 JBD BMS 的模板一致（同为 ReadLength: 0）。
//
// IntervalMs 是**默认值**：实际轮询周期由 edge_devices.command_intervals
// 覆盖（与 BMS 相同的可配置机制，经 api.ValidateCommandIntervals 校验）。
// 默认只给 read_status 一个非零值（1s），其余默认 0 = 不轮询；用户可在界面
// 上按需开启，最多 3 条同时启用（MaxCommandsPerEdgeDevice）。这样既满足
// "像 BMS 一样可配置参数定期采集"，又不会在默认状态下把 UART1 塞满。
func (d *TechfineInverterDriver) GetCommandTemplates() []CommandTemplate {
	// 从注册表派生 —— 2026-10-21 前这里手抄了第 4 份命令表，注释却自称
	// 「与 ControlActions()/VerifyControlAction() 使用同一张命令表，避免漂移」。
	// 实测漂移已经发生（read_eeprom 的 MinSensors 写 15，parser 只产出 4）。
	out := make([]CommandTemplate, 0, len(techfineCommands))
	for i := range techfineCommands {
		c := &techfineCommands[i]
		out = append(out, CommandTemplate{
			ID:   c.ID,
			Name: c.Name,
			Type: "read",
			// CmdByte 不适用于 ASCII 协议行（保留 0 表示「见 WriteData」）。
			CmdByte:     0,
			WriteData:   asciiToHex(c.Wire),
			ReadLength:  0, // 行空闲定帧，见上方 ReadLength 说明
			DelayMs:     0,
			IntervalMs:  c.DefaultIntervalMs,
			Schedulable: true, // P2: 第三态废止，模板域恒为 true
			Description: c.Description,
		})
	}
	return out
}
