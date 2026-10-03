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
//   · backend/internal/deviceaction/definition_test.go -- the GB3024 read
//     assertions flipped to "enabled", plus a new guard proving the
//     AvailabilityCode actions (reset_rainfall / clear_rainfall_write) still
//     fail closed.
//   · docs/分析/动作门禁台账.md -- ledger row for these eleven actions.
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
	return []ControlAction{
		techfineReadAction("read_status", "读取运行状态", "HSTS\r", "故障代码、运行模式与告警标志"),
		techfineReadAction("read_grid", "读取市电信息", "HGRID\r", "市电电压、频率与丢失阈值"),
		techfineReadAction("read_output", "读取输出信息", "HOP\r", "输出电压、频率、功率与负载"),
		techfineReadAction("read_battery", "读取电池信息", "HBAT\r", "电池电压、容量、充放电电流与 BUS 电压"),
		techfineReadAction("read_pv1", "读取 PV1 信息", "HPV\r", "PV1 电压、电流与功率"),
		techfineReadAction("read_pv2", "读取 PV2 信息", "HPVB\r", "PV2 电压、电流与功率"),
		techfineReadAction("read_temperature", "读取温度信息", "HTEMP\r", "温度、风扇转速与状态"),
		techfineReadAction("read_energy", "读取发电量", "HGEN\r", "日、月、年与总发电量"),
		techfineReadAction("read_bms", "读取 BMS 信息", "HBMS1\r", "BMS 状态、SOC、电流与限制值"),
		techfineReadAction("read_eeprom", "读取 EEPROM 设置", "HEEP1\r", "持久化配置与 BMS SOC 阈值"),
		techfineReadAction("read_version", "读取软件版本", "HIMSG1\r", "软件版本号与发布日期"),
	}
}

func techfineReadAction(id, name, command, description string) ControlAction {
	return ControlAction{
		ID: id, Version: 1, Name: name, Description: description,
		Semantics: "read", Risk: "low", Enabled: true,
		// UART worker returns the entire line-idle-delimited response; the window
		// is the protocol max because the exact line length is not knowable.  See
		// the ControlActions comment above.
		TXData: []byte(command), ReadSize: 256, RXTimeoutMS: 1000,
	}
}

// VerifyControlAction makes final-result parsing command-aware.  HPV and HPVB
// share the same wire shape, so using generic ParseData here would lose PV1/PV2
// identity and could persist data under the wrong sensor names.
func (d *TechfineInverterDriver) VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error) {
	if string(params) != "{}" {
		return nil, fmt.Errorf("techfine action %q does not accept parameters", actionID)
	}
	command, ok := map[string]string{
		"read_status": "HSTS\r", "read_grid": "HGRID\r", "read_output": "HOP\r",
		"read_battery": "HBAT\r", "read_pv1": "HPV\r", "read_pv2": "HPVB\r",
		"read_temperature": "HTEMP\r", "read_energy": "HGEN\r", "read_bms": "HBMS1\r",
		"read_eeprom": "HEEP1\r", "read_version": "HIMSG1\r",
	}[actionID]
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
	data, err := d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(command)))
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
	if err := expectActionSensors(actionID, data); err != nil {
		return nil, err
	}
	return data, nil
}

// techfineActionSensorFamily 把每个动作绑定到它期望的传感器名前缀。
//
// 为什么用「前缀族」而不是「精确字段集合」：同一动作在不同固件版本上返回的
// 字段数可能不同（例如 HSTS 的告警位数量、HEEP1 的尾部填充），精确集合会因
// 版本差异误拒合法响应；而族前缀（grid_/output_/battery_/...）足以区分
// 「这个响应根本不是我要的那类数据」。
var techfineActionSensorFamily = map[string][]string{
	"read_status":      {"fault_code", "work_mode", "alarm_"},
	"read_grid":        {"grid_"},
	"read_output":      {"output_"},
	"read_battery":     {"battery_", "bus_voltage"},
	"read_pv1":         {"pv1_"},
	"read_pv2":         {"pv2_"},
	"read_temperature": {"pv_temp", "inverter_temp", "boost_temp", "transformer_temp", "max_temp", "pv2_temp", "dc_rectifier_temp", "fan"},
	"read_energy":      {"daily_energy", "monthly_energy", "yearly_energy", "total_energy"},
	"read_bms":         {"bms_"},
	"read_eeprom":      {"eeprom_"},
	"read_version":     {"software_"},
}

// techfineActionMinSensors 是每个动作期望的**最少**字段数。
//
// 为什么还需要它（族前缀检查不够）：一个"同族但被截短"的帧产出的字段是**子集**，
// 族检查会放行。实测两个 fail-open 案例：
//
//	read_status 喂 "(00 P0000\r"（HSTS 的告警串被截到 5 字符）→
//	  仍返回 12 个 alarm_* 且**全为 0**，即"一切正常"。
//	  这是最危险的一类：设备明明可能正在报警，界面显示无告警。
//	read_pv2 喂 "(220.5 08.0 0\r"（功率字段被截）→ 返回 pv2_power=0。
//
// 因此除了"字段必须同族"，还要求"字段数不少于该动作的完整形状"。
// 取的是各 parser 的字段数下界（与 parse* 内部的 need >=N 校验一致）。
var techfineActionMinSensors = map[string]int{
	"read_status":      14, // fault_code + work_mode + 12 个告警位
	"read_grid":        2,  // grid_voltage + grid_frequency
	"read_output":      5,  // output_voltage/frequency/apparent/active/load
	"read_battery":     5,  // battery_voltage/capacity/charge/discharge + bus_voltage
	"read_pv1":         3,  // pv1_voltage/current/power
	"read_pv2":         3,  // pv2_voltage/current/power
	"read_temperature": 5,  // 至少 5 个温度通道
	"read_energy":      4,  // daily/monthly/yearly/total
	"read_bms":         10,
	"read_eeprom":      15,
	"read_version":     2,  // software_version + software_date
}

// expectActionSensors 校验解析结果确实属于该动作期望的传感器族，且形状完整。
//
// 两道判据缺一不可：
//   1. **每一个**返回字段都必须属于期望族（拦"喂错形状"）；
//   2. 字段数不得少于该动作的完整形状（拦"同族但被截短"）。
// 只做 1 会放过截短的 HSTS（12 个告警位全 0 = fail-open）；
// 只做 2 会放过"字段数够但根本不是这个动作"的错配帧。
func expectActionSensors(actionID string, data []SensorData) error {
	allowed, ok := techfineActionSensorFamily[actionID]
	if !ok {
		// 未登记的动作不静默放行：新增动作时必须显式声明期望形状，
		// 否则这条防线的覆盖面会随动作增加而静默缩水。
		return fmt.Errorf("techfine %s: no expected sensor family registered — 新增动作必须同时声明形状绑定", actionID)
	}
	if len(data) == 0 {
		return fmt.Errorf("techfine %s: response produced no sensor values", actionID)
	}
	for _, sd := range data {
		if !sensorNameMatchesFamily(sd.Name, allowed) {
			return fmt.Errorf("techfine %s: response contained %q which does not belong to the expected sensor family %v — 响应与请求的动作不符（错配或截断帧）",
				actionID, sd.Name, allowed)
		}
	}
	if min := techfineActionMinSensors[actionID]; min > 0 && len(data) < min {
		return fmt.Errorf("techfine %s: response yielded only %d sensor values, expected at least %d — 帧形状不完整（很可能是被截断的响应，拒绝采信以免产生静默假数据）",
			actionID, len(data), min)
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
		{Name: "bms_soc", Unit: "%"},
		{Name: "bms_charge_current", Unit: "A"},
		{Name: "bms_discharge_current", Unit: "A"},
		{Name: "bms_charge_voltage_limit", Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Unit: "V"},
		{Name: "bms_charge_current_limit", Unit: "A"},
		{Name: "bms_temp", Unit: "°C"},
		// Version / EEPROM
		{Name: "software_version", Unit: ""},
		{Name: "software_date", Unit: ""},
		// Protocol
		{Name: "protocol_type", Unit: ""},
	}
}

// ============================================================================
// ParseData — main entry point
// ============================================================================
// ParseData parses a raw ASCII response from the Techfine GB3024 inverter.
// The response format: '(' + space-separated fields + '\r'
// Trailing "OOO..." padding fields are ignored.
//
// Command type is inferred from field count and format patterns:
//   - 2 fields, 2nd starts with work-mode letter → HSTS
//   - 3 fields, 2nd is 8-digit date (all digits)  → HIMSG1 (version)
//   - 3 fields (numeric)                          → HPV/HPVB (PV data)
//   - 6+ fields, 2nd contains ':'                 → HGEN
//   - 15+ fields, 1st is single digit 0-2         → HEEP1 (EEPROM)
//   - 6+ fields, 1st has decimal, 8th has decimal → HOP
//   - 6+ fields, 1st has decimal                  → HGRID
//   - 7+ fields, 1st is integer                   → HBAT
//   - 10 fields, 2nd is 8-bit binary              → HBMS1
//   - 11+ fields                                  → HTEMP

func (d *TechfineInverterDriver) ParseData(raw []byte) ([]SensorData, error) {
	s := string(raw)

	// Response must start with '('
	if !strings.HasPrefix(s, "(") {
		return nil, fmt.Errorf("techfine: response must start with '(', got: %q", s)
	}

	// Strip leading '(' and trailing whitespace/CR
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimRight(s, "\r\n\x00 ")

	// Split by spaces
	fields := strings.Fields(s)

	if len(fields) == 0 {
		return nil, fmt.Errorf("techfine: empty response")
	}

	// --- HSTS: (NN MABCDEFGHIJKL...)
	// Second field starts with a work-mode letter: P/S/L/B/F/D/X
	if len(fields) >= 2 && len(fields[1]) >= 1 {
		c := fields[1][0]
		if c == 'P' || c == 'S' || c == 'L' || c == 'B' || c == 'F' || c == 'D' || c == 'X' {
			return d.parseHSTS(fields)
		}
	}

	// --- HBMS1: (AA b7b6b5b4b3b2b1b0 c7c6c5c4c3c2c1c0 ...)
	// Second and third fields are 8-character binary strings
	if len(fields) >= 10 && len(fields[1]) == 8 && len(fields[2]) == 8 &&
		isAllBinary(fields[1]) && isAllBinary(fields[2]) {
		return d.parseHBMS1(fields)
	}

	// --- HGEN: (AAAAAA BB:BB CC.CCC DDDD.D EEEE.E FFFFFFFFF.F ...)
	// Second field contains ':' (time format HH:MM)
	if len(fields) >= 6 && strings.Contains(fields[1], ":") {
		return d.parseHGEN(fields)
	}

	// --- HEEP1: (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
	// 20+ fields, first field is single digit (0-2)
	if len(fields) >= 15 && len(fields[0]) == 1 && fields[0] >= "0" && fields[0] <= "2" {
		return d.parseHEEP1(fields)
	}

	// --- HTEMP: (AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK ...)
	// 11 meaningful fields.
	// NOTE: fan status fields [7] and [8] are space-separated single chars; needs real device verification.
	if len(fields) >= 11 {
		return d.parseHTEMP(fields)
	}

	// --- HGRID / HOP: first field has decimal point (NNN.N)
	// HGRID: (NNN.N MM.M AAA BBB CC DD ...)       6 meaningful fields
	// HOP:   (NNN.N MM.M AAAAA BBBBB CCC DDD EEEEE FFF.F ...) 8 fields, 8th has decimal
	// Hardening: HOP fields[2] and fields[3] are 4+ digit numeric power values;
	// HGRID trailing data won't match this pattern.
	if len(fields) >= 6 && strings.Contains(fields[0], ".") {
		if len(fields) >= 8 && strings.Contains(fields[7], ".") &&
			isAllDigitsOrEmpty(fields[2]) && len(fields[2]) >= 4 &&
			isAllDigitsOrEmpty(fields[3]) && len(fields[3]) >= 4 {
			return d.parseHOP(fields)
		}
		return d.parseHGRID(fields)
	}

	// --- HBAT: (AA BBB.B CCC DDD EEEEE FFF GHIJK...) 7 meaningful fields, first is integer
	if len(fields) >= 7 {
		return d.parseHBAT(fields)
	}

	// --- HIMSG1: (NNNN.NN AAAABBCC DD — version + date, 2nd field is 8-digit date
	// Must be checked BEFORE PV (both have 3 fields, first has decimal point)
	if len(fields) >= 3 && len(fields[1]) == 8 && isAllDigits(fields[1]) {
		return d.parseHIMSG1(fields)
	}

	// --- PV: (AAA.A BB.B CCCCC ...) 3 meaningful fields
	// HPV and HPVB share the same response shape. Without the originating
	// command there is no safe way to select pv1_* or pv2_*.
	if len(fields) >= 3 {
		return nil, fmt.Errorf("techfine: ambiguous HPV/HPVB response requires command context")
	}

	// --- QPRTL: (MMMMMMMM) — protocol type, single field
	if len(fields) == 1 {
		return []SensorData{
			{Name: "protocol_type", Value: 1, Unit: ""},
		}, nil
	}

	return nil, fmt.Errorf("techfine: unrecognized response format, %d fields: %v", len(fields), fields)
}

// ============================================================================
// ParseDataWithCommand — command-aware parsing for HPV/HPVB disambiguation
// ============================================================================
// commandWriteData is the hex-encoded WriteData of the ConfigTemplate
// (e.g. "4850560d" for "HPV\r", "485056420d" for "HPVB\r").
func (d *TechfineInverterDriver) ParseDataWithCommand(raw []byte, commandWriteData string) ([]SensorData, error) {
	// Decode hex to ASCII
	decoded, err := hex.DecodeString(commandWriteData)
	if err != nil {
		return nil, fmt.Errorf("techfine: invalid command write data: %w", err)
	}
	cmdStr := strings.ToUpper(strings.TrimSpace(string(decoded)))

	if cmdStr == "HPV" || cmdStr == "HPVB" {
		s := string(raw)
		if !strings.HasPrefix(s, "(") {
			return nil, fmt.Errorf("techfine: response must start with '(', got: %q", s)
		}
		s = strings.TrimPrefix(s, "(")
		s = strings.TrimRight(s, "\r\n\x00 ")
		fields := strings.Fields(s)
		if len(fields) < 3 {
			return nil, fmt.Errorf("techfine %s: need >=3 fields, got %d", cmdStr, len(fields))
		}
		prefix := "pv1"
		if cmdStr == "HPVB" {
			prefix = "pv2"
		}
		return d.parsePV(fields, prefix)
	}

	// Other response formats are structurally distinguishable. If the response
	// has the ambiguous PV shape, ParseData still fails closed.
	return d.ParseData(raw)
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
	workModeVal, ok := workModeMap[workModeByte]
	if !ok {
		workModeVal = -1 // unknown mode
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

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HGRID: invalid frequency %q", fields[1])
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

	voltage, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid voltage %q", fields[0])
	}

	freq, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HOP: invalid frequency %q", fields[1])
	}

	apparentPower := parseFloat(fields[2])
	activePower := parseFloat(fields[3])
	loadPercent := parseFloat(fields[4])

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
	if len(fields) < 7 {
		return nil, fmt.Errorf("techfine HBAT: need >=7 fields, got %d", len(fields))
	}

	voltage, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, fmt.Errorf("techfine HBAT: invalid voltage %q", fields[1])
	}

	capacity := parseFloat(fields[2])
	chargeCurrent := parseFloat(fields[3])
	dischargeCurrent := parseFloat(fields[4])
	busVoltage := parseFloat(fields[5])

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
// parseHTEMP — Temperature info
// ============================================================================
// Format: (AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK ...)
//   AAA – PV temperature (°C)
//   BBB – inverter temperature (°C)
//   CCC – boost temperature (°C)
//   DDD – transformer temperature (°C)
//   EEE – max temperature (°C)
//   FFF – fan1 speed (%)
//   GGG – fan2 speed (%)
//   HI  – fan1 status
//   JJJ – PV2 temperature (°C)  (note: positions shifted, HI is 2 chars)
//   KKK – DC rectifier temperature (°C)
//
// Actually the format is: AAA BBB CCC DDD EEE FFF GGG HI JJJ KKK
// which is 11 space-separated fields (HI is one 2-char field).
// NOTE: fan status fields [7] and [8] are space-separated single chars; needs real device verification.

func (d *TechfineInverterDriver) parseHTEMP(fields []string) ([]SensorData, error) {
	if len(fields) < 11 {
		return nil, fmt.Errorf("techfine HTEMP: need >=11 fields, got %d", len(fields))
	}

	pvTemp := parseFloat(fields[0])
	invTemp := parseFloat(fields[1])
	boostTemp := parseFloat(fields[2])
	transformerTemp := parseFloat(fields[3])
	maxTemp := parseFloat(fields[4])
	fan1Speed := parseFloat(fields[5])
	fan2Speed := parseFloat(fields[6])
	fan1Status := parseFloat(fields[7])
	fan2Status := parseFloat(fields[8])
	pv2Temp := parseFloat(fields[9])
	dcRectifierTemp := parseFloat(fields[10])

	return []SensorData{
		{Name: "pv_temp", Value: pvTemp, Unit: "°C"},
		{Name: "inverter_temp", Value: invTemp, Unit: "°C"},
		{Name: "boost_temp", Value: boostTemp, Unit: "°C"},
		{Name: "transformer_temp", Value: transformerTemp, Unit: "°C"},
		{Name: "max_temp", Value: maxTemp, Unit: "°C"},
		{Name: "fan1_speed", Value: fan1Speed, Unit: "%"},
		{Name: "fan2_speed", Value: fan2Speed, Unit: "%"},
		{Name: "fan1_status", Value: fan1Status, Unit: ""},
		{Name: "fan2_status", Value: fan2Status, Unit: ""},
		{Name: "pv2_temp", Value: pv2Temp, Unit: "°C"},
		{Name: "dc_rectifier_temp", Value: dcRectifierTemp, Unit: "°C"},
	}, nil
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

	daily := parseFloat(fields[2])
	monthly := parseFloat(fields[3])
	yearly := parseFloat(fields[4])
	total := parseFloat(fields[5])

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

	// BMS status flags from fields[1] (8 binary digits, MSB first)
	// status1 = "b7b6b5b4b3b2b1b0" → index 0=b7, index 1=b6, ..., index 7=b0
	status1 := fields[1]

	var bmsCommOK, bmsLowAlarm, bmsLowFault float64
	var bmsChargeAllowed, bmsDischargeAllowed float64
	var bmsChargeOvercurrent, bmsDischargeOvercurrent, bmsTempLow float64

	if len(status1) >= 8 {
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
	}

	dischargeVLimit := parseFloat(fields[3])
	chargeVLimit := parseFloat(fields[4])
	chargeILimit := parseFloat(fields[5])
	soc := parseFloat(fields[6])
	chargeCurrent := parseFloat(fields[7])
	dischargeCurrent := parseFloat(fields[8])
	tempRaw := parseFloat(fields[9])
	// HHHHH is 5 digits in 0.01K → convert to °C
	tempCelsius := tempRaw/100.0 - 273.15

	return []SensorData{
		{Name: "bms_comm_ok", Value: bmsCommOK, Unit: ""},
		{Name: "bms_charge_allowed", Value: bmsChargeAllowed, Unit: ""},
		{Name: "bms_discharge_allowed", Value: bmsDischargeAllowed, Unit: ""},
		{Name: "bms_low_alarm", Value: bmsLowAlarm, Unit: ""},
		{Name: "bms_low_fault", Value: bmsLowFault, Unit: ""},
		{Name: "bms_charge_overcurrent", Value: bmsChargeOvercurrent, Unit: ""},
		{Name: "bms_discharge_overcurrent", Value: bmsDischargeOvercurrent, Unit: ""},
		{Name: "bms_temp_low", Value: bmsTempLow, Unit: ""},
		{Name: "bms_soc", Value: soc, Unit: "%"},
		{Name: "bms_charge_current", Value: chargeCurrent, Unit: "A"},
		{Name: "bms_discharge_current", Value: dischargeCurrent, Unit: "A"},
		{Name: "bms_charge_voltage_limit", Value: chargeVLimit, Unit: "V"},
		{Name: "bms_discharge_voltage_limit", Value: dischargeVLimit, Unit: "V"},
		{Name: "bms_charge_current_limit", Value: chargeILimit, Unit: "A"},
		{Name: "bms_temp", Value: tempCelsius, Unit: "°C"},
	}, nil
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

	versionStr := fields[0]
	dateStr := fields[1]

	// Parse version "0000.03" → 0.0003 as float64 for LastData storage.
	// The version format is MMMM.NN where the minor part is treated as
	// a 4-digit fractional value: 0000.03 → 0 + 3/10000 = 0.0003.
	versionParts := strings.SplitN(versionStr, ".", 2)
	versionMajor := parseFloat(versionParts[0])
	versionMinor := 0.0
	if len(versionParts) >= 2 {
		minorInt, _ := strconv.Atoi(versionParts[1])
		versionMinor = float64(minorInt) / 10000.0
	}
	versionVal := versionMajor + versionMinor

	// Parse date "20230220" → 20230220 as float64 for LastData storage
	dateVal := parseFloat(dateStr)

	return []SensorData{
		{Name: "software_version", Value: versionVal, Unit: ""},
		{Name: "software_date", Value: dateVal, Unit: ""},
	}, nil
}

// ============================================================================
// parseHEEP1 — EEPROM settings (basic parse, stores key fields)
// ============================================================================
// Format: (A BBB CCC D E F G HHH I J K L M N P QQQ RRR SSS TTT UUU.U VVV.V XXX.X O
//   A     – machine model source (0-2)
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

	// fields[8] = output frequency flag (0=50Hz, 1=60Hz)
	freqFlag := parseFloat(fields[8])
	freqValue := 50.0
	if freqFlag == 1 {
		freqValue = 60.0
	}

	result := []SensorData{
		{Name: "eeprom_model_source", Value: parseFloat(fields[0]), Unit: ""},
		{Name: "eeprom_max_charge_current", Value: parseFloat(fields[1]), Unit: "A"},
		{Name: "eeprom_output_voltage", Value: parseFloat(fields[7]), Unit: "V"},
		{Name: "eeprom_output_frequency", Value: freqValue, Unit: "Hz"},
	}

	return result, nil
}

// ============================================================================
// Helpers
// ============================================================================

// parseFloat parses a float string, returning 0 on error.
func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
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
	// 与 ControlActions()/VerifyControlAction() 使用同一张命令表，
	// 避免两处各自维护一份 ASCII 命令而产生漂移。
	type pollCmd struct {
		id       string
		name     string
		command  string
		interval int
		desc     string
	}
	cmds := []pollCmd{
		{"read_status", "读取运行状态", "HSTS\r", 1000, "故障代码、运行模式与告警标志"},
		{"read_grid", "读取市电信息", "HGRID\r", 0, "市电电压、频率与丢失阈值"},
		{"read_output", "读取输出信息", "HOP\r", 0, "输出电压、频率、功率与负载"},
		{"read_battery", "读取电池信息", "HBAT\r", 0, "电池电压、容量、充放电电流与 BUS 电压"},
		{"read_pv1", "读取 PV1 信息", "HPV\r", 0, "PV1 电压、电流与功率"},
		{"read_pv2", "读取 PV2 信息", "HPVB\r", 0, "PV2 电压、电流与功率"},
		{"read_temperature", "读取温度信息", "HTEMP\r", 0, "温度、风扇转速与状态"},
		{"read_energy", "读取发电量", "HGEN\r", 0, "日、月、年与总发电量"},
		{"read_bms", "读取 BMS 信息", "HBMS1\r", 0, "BMS 状态、SOC、电流与限制值"},
		{"read_eeprom", "读取 EEPROM 设置", "HEEP1\r", 0, "持久化配置与 BMS SOC 阈值"},
		{"read_version", "读取软件版本", "HIMSG1\r", 0, "软件版本号与发布日期"},
	}
	out := make([]CommandTemplate, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, CommandTemplate{
			ID: c.id, Name: c.name, Type: "read",
			// CmdByte 不适用于 ASCII 协议行（保留 0 表示"见 WriteData"）。
			CmdByte:     0,
			WriteData:   asciiToHex(c.command),
			ReadLength:  0, // 行空闲定帧，见上面 ReadLength 说明
			DelayMs:     0,
			IntervalMs:  c.interval,
			Schedulable: true, // P2: 第三态废止，模板域恒为 true
			Description: c.desc,
		})
	}
	return out
}
