//go:build simulation

// 场景目录 · SIM-SCNE 真实业务剧本（设计 §4 SIM-SCNE-001..008）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§6 命名、§10 门禁）。
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API、§5.6 隔离与命名、§7 红线）。
// 领域依据：docs/设计/自动化策略引擎方案.md、docs/设计/自动化确认制闭环实现方案.md。
//
// 为什么这一组是验收重心（设计 §4 SIM-SCNE 前言）：它直接对应用户点名的使用场景
// ——「根据光伏发电情况控制 BMS、光照控制开关灯、雨量到中雨/大雨给用户发通知」。
// 因此每条场景都必须做到「真实传感器数据 → 真实引擎求值 → 真实动作/通知」，
// 而不是把规则建出来就算数。
//
// 本文件守护的不变量（每条断言都对应其中之一）：
//  1. 业务语义建立在真实物理量上：场景私有 DeviceConfig 用 rainfall / illuminance /
//     pv_power / battery_temp / soc 这些**用户可读**的字段名与单位（设计文档建议的做法 (a)），
//     而不是把所有业务都塞进一个 temperature 字段；
//  2. 分级策略（开灯/关灯、中雨/大雨）必须是两条独立规则各自求值：低级别不误触发、
//     升级后高级别单独触发、且冷却期内不重复打扰用户；
//  3. 高风险动作（BMS 充放电 MOS 策略）绝不自动落地：先落 pending_confirm + 待确认通知，
//     人工确认后才真正下发指令帧，且**指令帧里的字节必须是期望的那一帧**；
//  4. 「只通知不动作」是可观测事实，不只是规则字段：指令帧计数为 0
//     且执行记录列表为空，两者都要断言。
//
// 哨兵上报说明（跨域隐患预警）：本文件的 sceneProvision **不做**任何自检上报
// （不像 harness.ProvisionSimpleDevice 那样先报一帧 temperature=21.37 探针），
// 因此不存在"夹具哨兵帧在规则建立之后才被求值 → 幽灵触发"的窗口；
// 并且所有"必须触发"的断言都锚定了本场景实际上报的 trigger_value，
// 所有"不该触发"的断言都先把该帧证明到统一数据里再断言事件数为 0。
//
// 命名纪律（设计 §4.1 / 门禁第 8 条）：本文件所有包级标识符一律以 scene 开头。
package catalog

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// sceneDomain 是本子域的域名段（设计 §4 冻结的 11 个自动化子域之一）。
const sceneDomain Domain = "SCNE"

// sceneMosAction 是嘉佰达 BMS 的「充放电 MOS 软件策略」动作 ID
// （drivers/jiabaida_control.go 的 set_mos_policy：一次提交充电/放电两个软件关闭位，
// bounded_sequence + readback 对账 + at_most_once，风险 high）。
//
// 为什么用它：用户场景「根据光伏发电情况控制 BMS 充电」的真实执行腿就是它。
// 设计文档里提到的 set_mos_policy 在 Catalog 里**确实存在且已放行**
// （Enabled=true、无 AvailabilityCode、bounded_sequence），因此本域不需要用别的动作顶替。
const sceneMosAction = "set_mos_policy"

// sceneMosPriorityUser / sceneMosPriorityOperator 是 set_mos_policy 的 priority 枚举值
// （写帧里 user=0x00、operator=0xAA，见 compileMOSFrame）。
const (
	sceneMosPriorityUser     = "user"
	sceneMosPriorityOperator = "operator"
)

// sceneMosFramePriorityUser / Operator 是 E1 写帧里 priority 字节的实际取值。
const (
	sceneMosFramePriorityUser     byte = 0x00
	sceneMosFramePriorityOperator byte = 0xAA
)

func init() {
	Register(Scenario{
		ID:     "SIM-SCNE-001",
		Title:  "光照暗下来自动开灯，天亮后自动关灯",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-001；docs/设计/自动化策略引擎方案.md",
		Run:    sceneRun001,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-002",
		Title:  "夜间光伏不发电时自动关闭 BMS 充电，避免无谓损耗",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-002；docs/设计/自动化确认制闭环实现方案.md",
		Run:    sceneRun002,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-003",
		Title:  "电池 SOC 过低时断开负载并同时通知用户",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-003；docs/设计/自动化确认制闭环实现方案.md",
		Run:    sceneRun003,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-004",
		Title:  "温度过高时停止充电并发出告警",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-004；docs/设计/自动化确认制闭环实现方案.md",
		Run:    sceneRun004,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-005",
		Title:  "雨量达到中雨时给用户发一条提醒通知",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-005；docs/设计/自动化策略引擎方案.md",
		Run:    sceneRun005,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-006",
		Title:  "雨量升级到大雨时发出更高等级通知，且不与中雨重复打扰",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-006；docs/设计/自动化策略引擎方案.md（冷却与同窗节流）",
		Run:    sceneRun006,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-007",
		Title:  "无人值守时段出现异常只通知、不自动动作",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-007；docs/设计/自动化确认制闭环实现方案.md",
		Run:    sceneRun007,
	})
	Register(Scenario{
		ID:     "SIM-SCNE-008",
		Title:  "光伏发电恢复后自动恢复 BMS 充电（对称策略）",
		Domain: sceneDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-SCNE-008；docs/设计/自动化确认制闭环实现方案.md",
		Run:    sceneRun008,
	})
}

// ---------------------------------------------------------------------------
// 夹具：带业务字段名的仿真设备（sceneProvision）
// ---------------------------------------------------------------------------

// sceneField 描述一个业务物理量在二进制上报帧里的位置，直接对应
// DeviceConfig.Parser.fields（backend/pkg/parser/parser.go 的 FieldRule）。
//
// 为什么不用现成夹具（设计文档 §4 明确建议的做法 (a)，本报告已说明取舍）：
//   - autoProvisionDevice 的解析器只有一个字段 temperature（scale 0.1）；
//   - edgeProvision 支持自定义字段名，但类型固定 uint16、scale 固定 0.1、单位固定 °C。
//
// 本域需要 illuminance(Lux) / pv_power(W) / soc(%) / battery_temp(℃) / rainfall(mm)
// 这五个带正确单位与量程的字段名（用户可读）。因此这里自建**多字段**解析器；
// 除此之外的一切（命名空间、自清理、武装、指令帧断言）都复用既有夹具，不重复实现。
type sceneField struct {
	Name   string  // 物理量名（与 parser.Field.Name / 规则 trigger_sensor_name 同域）
	Type   string  // uint16 | int16 | uint32 | int32 | float32
	Scale  float64 // 解析后乘的系数（0 视为 1）
	Offset int     // 帧内字节偏移
	Length int     // 字节长度（0 = 按类型推断）
	Unit   string  // 单位（写进 unified_data，用户界面可见）
}

// sceneSpec 描述一台要被搭出来的仿真设备。
type sceneSpec struct {
	Scenario string // 场景 ID，用于命名空间
	// Suffix 是节点局部名后缀。紧凑场景码由 harness 统一维护（设计 §6 由 T1 负责），
	// 本域一律使用 ≤3 字符后缀：无论 SCNE 的紧凑码是否已注册（"sn"→"sn001-led"，
	// 未注册时回退 slug "scene-001"），node_id 都不会超过 varchar(32)。
	Suffix     string
	Type       string // 边缘设备型号：驱动型号（有动作目录）或场景私有型号
	Fields     []sceneField
	IntervalMs int
	HardwareID string
	Handshake  bool // 是否完成 MQTT Hello 握手（需要节点在线/清单下发/动作目录时为 true）
}

// sceneDevice 是「节点 + 通道 + 设备配置 + 边缘设备 + MQTT 仿真设备」的句柄。
//
// edge 字段直接复用 catalog/edge.go 的 edgeDevice 句柄，从而可以原样使用
// cmd.go 的 cmdArmNode（把节点武装到动作目录可用）与 cmdAwaitChannelCmd
// （断言真实指令帧），不必重写这两套夹具。
type sceneDevice struct {
	edge   *edgeDevice
	fields []sceneField
}

// sceneFieldLength 返回字段的字节长度（0 时按类型推断，与 parser.typeLength 同表）。
func sceneFieldLength(f sceneField) int {
	if f.Length > 0 {
		return f.Length
	}
	switch f.Type {
	case "uint16", "int16":
		return 2
	case "uint32", "int32", "float32":
		return 4
	default:
		return 0
	}
}

// sceneScale 返回解析系数（0 视为 1，与 parser.parseField 同语义）。
func sceneScale(f sceneField) float64 {
	if f.Scale == 0 {
		return 1
	}
	return f.Scale
}

// sceneProvision 用真实产品端点搭出边缘设备全链路：
//
//	POST /api/v1/nodes          建节点（node_id 与 MQTT 仿真设备同名）
//	POST /api/v1/channels       建一条启用的 UART 通道
//	POST /api/v1/device-configs 建带多字段 binary 解析规则的设备配置（业务字段名 + 单位）
//	POST /api/v1/edge-devices   把设备绑到节点 + 通道（自动附带逻辑身份）
//	harness.Device              真实 MQTT 连接（+ 可选 Hello 握手）
//
// 清理顺序与创建顺序相反（边缘设备 → 通道 → 设备配置 → 节点，设计 §5.6 自清理）。
//
// 注意：本夹具**不做**任何自检哨兵上报（不像 harness.ProvisionSimpleDevice），
// 因此不存在"哨兵帧在规则建立之后才被求值"的幽灵触发窗口。
func sceneProvision(e *harness.Env, spec sceneSpec) *sceneDevice {
	t := e.T
	t.Helper()

	if spec.IntervalMs == 0 {
		spec.IntervalMs = 60000
	}
	if spec.HardwareID == "" {
		spec.HardwareID = "1"
	}
	if len(spec.Fields) == 0 {
		e.Fatalf("sceneProvision(%s): 必须至少声明一个解析字段", spec.Scenario)
	}

	label := e.NS(spec.Scenario, spec.Suffix)
	// e.DeviceFor 生成「运行 + 场景」双重命名空间（框架 §5.6 v1.3）：
	// 跨场景唯一性由紧凑场景码保证，本场景内只需后缀唯一。
	dev := e.DeviceFor(spec.Scenario, spec.Suffix)

	node := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": dev.NodeID,
		"name":    label + "-节点",
	}).Expect(http.StatusCreated)
	nodeDBID := node.DataInt("id")

	channel := e.Admin.Post("/api/v1/channels", map[string]any{
		"node_id":       dev.NodeID,
		"hardware_type": "UART",
		"bus_type":      "UART",
		"hardware_id":   spec.HardwareID,
		// bus_config 是**必填**：nodemgr 的 validateManifestAuthority 会拒收
		// "启用但 bus_config 不可解码"的通道（manifest_codec.go:60），一旦被拒收，
		// 该节点的配置清单永远推不下去（config_status 卡在 failed），
		// 后续所有下发类断言都会连锁失败。01/02 是不与其它外设抢引脚的 UART 路由。
		"bus_config":  "0102",
		"interval_ms": spec.IntervalMs,
		"enabled":     true,
	}).Expect(http.StatusCreated)
	channelID := channel.DataInt("id")

	fields := make([]map[string]any, 0, len(spec.Fields))
	for _, f := range spec.Fields {
		length := sceneFieldLength(f)
		if length == 0 {
			e.Fatalf("sceneProvision(%s): 字段 %s 的类型 %q 无法推断字节长度", spec.Scenario, f.Name, f.Type)
		}
		fields = append(fields, map[string]any{
			"name": f.Name, "type": f.Type, "scale": sceneScale(f),
			"offset": f.Offset, "length": length, "unit": f.Unit,
		})
	}

	cfg := e.Admin.Post("/api/v1/device-configs", map[string]any{
		"name":          label + "-配置",
		"device_type":   spec.Type,
		"hardware_type": "uart",
		"status":        "active",
		"parser":        map[string]any{"data_format": "binary", "fields": fields},
	}).Expect(http.StatusCreated)
	cfgID := cfg.DataInt("id")

	edge := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":             label + "-设备",
		"node_id":          dev.NodeID,
		"channel_id":       channelID,
		"device_config_id": cfgID,
		"hardware_id":      spec.HardwareID,
		"interval_ms":      spec.IntervalMs,
		"enabled":          true,
	}).Expect(http.StatusCreated)
	edgeID := edge.DataInt("id")
	logicalID := edge.DataInt("logical_device_id")
	if edgeID == 0 || channelID == 0 || cfgID == 0 {
		e.Fatalf("边缘设备创建返回不完整: %s", edge.BodyString())
	}
	// 型号由 device_config_id 派生（handler_edge_device.go:401-404）——
	// 它是"这台设备有没有动作目录"的唯一决定因素，必须回读确认。
	if got := edge.DataString("type"); got != spec.Type {
		e.Fatalf("边缘设备的型号 = %q，期望 %q（型号决定动作目录）", got, spec.Type)
	}

	t.Cleanup(func() {
		edgeCleanupOK(t, "边缘设备", e.Admin.Delete("/api/v1/edge-devices/"+strconv.FormatInt(edgeID, 10)))
		edgeCleanupOK(t, "通道", e.Admin.Delete("/api/v1/channels/"+strconv.FormatInt(channelID, 10)))
		edgeCleanupOK(t, "设备配置", e.Admin.Delete("/api/v1/device-configs/"+strconv.FormatInt(cfgID, 10)))
		edgeCleanupOK(t, "节点", e.Admin.Delete("/api/v1/nodes/"+strconv.FormatInt(nodeDBID, 10)))
	})

	if err := dev.Connect(); err != nil {
		e.Fatalf("MQTT 仿真设备连接失败（%s）: %v", dev.NodeID, err)
	}
	t.Cleanup(dev.Close)

	// 调度采样语义：帧必须携带 edge_device_id，否则 databus 判为无关联透传数据而不落库
	// （backend/internal/databus/bus.go 的 IsPassive）。
	dev.EdgeDeviceID = uint32(edgeID)
	dev.ChannelID = uint32(channelID)

	fixture := &edgeDevice{
		NodeID:          dev.NodeID,
		NodeDBID:        nodeDBID,
		ChannelID:       channelID,
		DeviceConfigID:  cfgID,
		EdgeDeviceID:    edgeID,
		LogicalDeviceID: logicalID,
		Type:            spec.Type,
		HardwareID:      spec.HardwareID,
		Device:          dev,
	}
	if spec.Handshake {
		// Hello 必须在 ResourceReport 之前（handler_hello.go 每次 Hello 会清空
		// boot_id / 能力四件套，"Hello 开启新的固件世代"）。
		dev.Hello("sim-1.0.0", "SIM-SCNE", 1)
	}
	return &sceneDevice{edge: fixture, fields: spec.Fields}
}

// sceneEncode 按字段规则把业务值编码成二进制上报载荷。
// values 里没有给出的字段保持 0（解析器仍会产出该字段，值 0）。
func sceneEncode(fields []sceneField, values map[string]float64) ([]byte, error) {
	size := 0
	for _, f := range fields {
		if end := f.Offset + sceneFieldLength(f); end > size {
			size = end
		}
	}
	buffer := make([]byte, size)
	for _, f := range fields {
		value := values[f.Name]
		offset := f.Offset
		// 反算原始整数：解析时会乘回 scale（scale=0.1 时 23.5 → 235）。
		raw := math.Round(value / sceneScale(f))
		switch f.Type {
		case "uint16":
			binary.BigEndian.PutUint16(buffer[offset:offset+2], uint16(raw))
		case "int16":
			binary.BigEndian.PutUint16(buffer[offset:offset+2], uint16(int16(raw)))
		case "uint32":
			binary.BigEndian.PutUint32(buffer[offset:offset+4], uint32(raw))
		case "int32":
			binary.BigEndian.PutUint32(buffer[offset:offset+4], uint32(int32(raw)))
		case "float32":
			binary.BigEndian.PutUint32(buffer[offset:offset+4], math.Float32bits(float32(value)))
		default:
			return nil, fmt.Errorf("sceneEncode: 不支持的字段类型 %q（字段 %s）", f.Type, f.Name)
		}
	}
	return buffer, nil
}

// report 让仿真设备按字段规则上报一帧真实数据（MQTT nodes/<id>/up）。
func (d *sceneDevice) report(values map[string]float64) error {
	payload, err := sceneEncode(d.fields, values)
	if err != nil {
		return err
	}
	return d.edge.Device.DataReport(uint32(d.edge.ChannelID), uint64(time.Now().UnixMilli()), payload)
}

// ---------------------------------------------------------------------------
// 通用断言小工具（全部以 scene 前缀，避免与同包其它域文件撞名）
// ---------------------------------------------------------------------------

// sceneEventRows 读某条策略的事件（可带 result 过滤），失败即终止场景。
func sceneEventRows(e *harness.Env, ruleID int64, result string) []autoEventRow {
	e.T.Helper()
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	rows, err := autoListEvents(e, query)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return rows
}

// sceneWaitEvent 等到某条策略出现指定 result 的事件（轮询收敛，不用 sleep 同步）。
func sceneWaitEvent(e *harness.Env, ruleID int64, result string) autoEventRow {
	e.T.Helper()
	var hit autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&result="+result)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚未产生 result=%s 的事件", ruleID, result)
		}
		hit = rows[0]
		return nil
	})
	return hit
}

// sceneWaitEventWithin 是 sceneWaitEvent 的显式超时版本。
//
// 为什么需要：time_window 规则只由 StartWindowTicker 每 1 分钟求值一次
// （evaluator.go:204-222），而 ticker 从**服务进程启动那一刻**起算。
// 套件跑到某个时段场景时，距离下一个 tick 最坏还有将近 60s，
// 因此这类断言必须留满一个 tick 周期以上（这里由调用方给 100s）。
func sceneWaitEventWithin(e *harness.Env, ruleID int64, result string, timeout time.Duration) autoEventRow {
	e.T.Helper()
	var hit autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&result="+result)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚未产生 result=%s 的事件", ruleID, result)
		}
		hit = rows[0]
		return nil
	})
	return hit
}

// sceneAssertNeverFires 断言某条策略在 settle 窗口内**始终不产生事件**。
//
// 为什么用有界等待而不是立刻断言：立刻读到 0 条只能证明"此刻还没有"，
// 排不掉迟到的误触发；反过来一旦真的出现事件就立刻失败（不白等）。
func sceneAssertNeverFires(e *harness.Env, ruleID int64, settle time.Duration, what string) {
	e.T.Helper()
	err := e.EventuallyError(settle, func() error {
		if rows := sceneEventRows(e, ruleID, ""); len(rows) > 0 {
			return nil // 出现了事件 → 误报，会让 EventuallyError 提前返回 nil
		}
		return fmt.Errorf("策略 %d 尚无事件", ruleID)
	})
	if err == nil {
		rows := sceneEventRows(e, ruleID, "")
		e.Fatalf("%s：策略 %d 不应产生事件，实际 %d 条（首条 result=%s trigger_value=%v）",
			what, ruleID, len(rows), rows[0].Result, rows[0].TriggerValue)
	}
}

// sceneNotificationsFor 在通知中心里按 source=automation_rule + source_id=策略 ID
// 取出该策略的通知（用户能看见的落地行）。
func sceneNotificationsFor(e *harness.Env, ruleID int64) []autoNotificationRow {
	e.T.Helper()
	rows, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	id := strconv.FormatInt(ruleID, 10)
	var out []autoNotificationRow
	for _, row := range rows {
		if row.Source == "automation_rule" && row.SourceID == id {
			out = append(out, row)
		}
	}
	return out
}

// sceneWaitNotification 等到通知中心出现该策略的通知。
func sceneWaitNotification(e *harness.Env, ruleID int64) autoNotificationRow {
	e.T.Helper()
	var hit autoNotificationRow
	e.Eventually(20*time.Second, func() error {
		rows := sceneNotificationsFor(e, ruleID)
		if len(rows) == 0 {
			return fmt.Errorf("通知中心尚无策略 %d 的通知", ruleID)
		}
		hit = rows[0]
		return nil
	})
	return hit
}

// sceneWaitSensor 证明"这一帧真的走完了 上报 → 解析 → 落库"。
//
// 为什么必须先证明链路是活的：只断言"没有事件"时，"没触发"与"链路根本没跑"
// 无法区分（这正是本仓历史 P0 的形态）。SIM-AUTO-003 用同款对照思路。
func sceneWaitSensor(e *harness.Env, edgeDeviceID int64, name string, value float64) {
	e.T.Helper()
	e.Eventually(25*time.Second, func() error {
		r := e.Admin.Get("/api/v1/devices/" + strconv.FormatInt(edgeDeviceID, 10) + "/sensor-data?limit=50")
		if r.Status != http.StatusOK {
			return fmt.Errorf("GET /devices/%d/sensor-data 返回 %d: %s", edgeDeviceID, r.Status, r.BodyString())
		}
		var samples []autoSensorSample
		if err := json.Unmarshal(r.Data, &samples); err != nil {
			return fmt.Errorf("解析统一数据失败: %w", err)
		}
		for _, sample := range samples {
			if sample.SensorName == name && math.Abs(sample.Value-value) <= 1e-3 {
				return nil
			}
		}
		return fmt.Errorf("本设备尚无 %s=%.4g 的统一数据（当前 %d 条）", name, value, len(samples))
	})
}

// sceneNotifyRow 是通知中心行的富投影：比 auto.go 的 autoNotificationRow 多出
// message / description —— 待确认通知要断言的那句"为什么要人工确认"正写在里面。
type sceneNotifyRow struct {
	ID          uint   `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SourceID    string `json:"source_id"`
	Read        bool   `json:"read"`
}

// sceneWaitNotificationDetail 等到通知中心出现该策略的通知，返回带正文的富投影。
func sceneWaitNotificationDetail(e *harness.Env, ruleID int64) sceneNotifyRow {
	e.T.Helper()
	var hit sceneNotifyRow
	e.Eventually(20*time.Second, func() error {
		r := e.Admin.Get("/api/v1/notifications?limit=100")
		if r.Status != http.StatusOK {
			return fmt.Errorf("GET /api/v1/notifications 返回 %d: %s", r.Status, r.BodyString())
		}
		var rows []sceneNotifyRow
		if err := json.Unmarshal(r.Data, &rows); err != nil {
			return fmt.Errorf("解析通知列表失败: %w", err)
		}
		id := strconv.FormatInt(ruleID, 10)
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == id {
				hit = row
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无策略 %d 的通知", ruleID)
	})
	return hit
}

// sceneAssertNoDispatch 断言"没有任何物理动作发生"：既没有执行记录，也没有指令帧出网。
// 执行记录断言复用 cmd.go 的 cmdAssertNoExecutions。
func sceneAssertNoDispatch(e *harness.Env, d *sceneDevice, label string) {
	e.T.Helper()
	cmdAssertNoExecutions(e, d.edge, label)
	if got := len(d.edge.Device.FramesOf(frame.MsgChannelCmdV2)); got != 0 {
		e.T.Fatalf("%s：设备不应收到指令帧，实际收到 %d 条", label, got)
	}
}

// sceneConfirm 走真实的人工确认端点（POST /automation-events/:id/confirm）。
//
// 近认证窗口是 10 分钟（commandexec/confirmation.go）。长套件跑到这里时
// 管理员登录时刻可能已过期，因此先 RefreshAdmin 刷新 LastLoginAt ——
// 这正是产品里"需先完成手动确认（刷新近认证）"的真实动作，不是绕过。
func sceneConfirm(e *harness.Env, eventID uint) autoEventRow {
	e.T.Helper()
	if err := e.RefreshAdmin(); err != nil {
		e.Fatalf("刷新管理员会话（近认证）失败: %v", err)
	}
	resp := e.Admin.Post(
		"/api/v1/automation-events/"+strconv.FormatUint(uint64(eventID), 10)+"/confirm",
		map[string]any{}).Expect(http.StatusOK)
	var closed autoEventRow
	resp.Decode(&closed)
	return closed
}

// sceneAssertMosFrame 断言下发到 BMS 的指令帧里携带的正是期望的 E1 MOS 写帧。
//
// 帧结构与字节语义来自 drivers/jiabaida_control.go 的 compileMOSFrame：
//
//	DD 5A E1 02 <priority> <mos> <chk_hi> <chk_lo> 77
//	mos bit0 = 充电软件关闭位, bit1 = 放电软件关闭位
//	priority: user = 0x00, operator = 0xAA
//
// 为什么断言到字节：result=executed 只证明"指令进了队列"，而这条链路对用户的价值
// 恰恰在于"真正发到 BMS 的字节是对的"（关的是充电还是放电，差一位就是两回事）。
func sceneAssertMosFrame(e *harness.Env, cmd frame.ChannelCmdV2, wantPriority, wantMos byte, label string) {
	e.T.Helper()
	if len(cmd.Plan) != 2 {
		e.Fatalf("%s：set_mos_policy 是 bounded_sequence（写 + 读回对账），"+
			"指令帧应携带 2 个步骤，实际 %d 个", label, len(cmd.Plan))
	}
	tx := cmd.Plan[0].TXData
	if len(tx) != 9 {
		e.Fatalf("%s：E1 写帧长度 %d 不合法（期望 9 字节）: %x", label, len(tx), tx)
	}
	if tx[0] != 0xDD || tx[1] != 0x5A || tx[2] != 0xE1 || tx[3] != 0x02 || tx[8] != 0x77 {
		e.Fatalf("%s：E1 MOS 写帧头尾不对: %x（期望 DD 5A E1 02 … 77）", label, tx)
	}
	if tx[4] != wantPriority {
		e.Fatalf("%s：E1 写帧的 priority 字节 = 0x%02X，期望 0x%02X", label, tx[4], wantPriority)
	}
	if tx[5] != wantMos {
		e.Fatalf("%s：E1 写帧的 MOS 位 = 0x%02X，期望 0x%02X（bit0=充电关闭 bit1=放电关闭）",
			label, tx[5], wantMos)
	}
	e.Evidence("SIM-SCNE.mos_frame", map[string]any{
		"label": label, "tx_hex": fmt.Sprintf("%x", tx), "priority": tx[4], "mos": tx[5],
		"plan_steps": len(cmd.Plan),
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-001 光照暗下来自动开灯，天亮后自动关灯
// ---------------------------------------------------------------------------

// sceneRun001 的**诚实降级说明（务必保留）**：
//
// 用户场景里"开灯/关灯"的真实执行腿是 GPIO 动作（gpio_set，PeriphCmd 0x1B）。
// 但该动作在当前仿真夹具下**不可达**，证据链如下（全部已逐行核实）：
//
//	gatePeriphConfig 要求 gpio_configs 表里存在 (node_id, pin) 且 enabled=true
//	  （commandexec/service.go:243-262）；
//	该行的唯一写入口是 POST /api/v1/nodes/:id/gpio，它先过 validateReportedGPIO
//	  （api/handler_periph.go:158），要求 nodes.capabilities.buses.gpio 里含该引脚；
//	nodes.capabilities 的唯一写入点是 ResourceReport(0x19) 的 buses_blob
//	  （nodemgr/handler_resources.go:872），HTTP 侧没有任何写入口；
//	而 harness 的 Device.ResourceReport 只编码一条 UART 总线
//	  （harness/device.go:588-596 的 buses 子消息），无法上报 GPIO。
//
// 结论：**降级为 notification 动作**（设计文档也允许并给了这个退路），并且不假装
// 真的开了灯 —— 本场景只断言"决策腿"：真实 Lux 数据 → 真实引擎求值 →
// 两条独立规则各自触发 → 用户收到通知。该降级已写入交付报告的「未实现/降级」清单。
func sceneRun001(e *harness.Env) {
	const scenarioID = "SIM-SCNE-001"
	d := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "led", Type: "sim_scene_001_light",
		Fields: []sceneField{
			{Name: "illuminance", Type: "uint32", Scale: 1, Offset: 0, Length: 4, Unit: "Lux"},
		},
	})

	// 黄昏开灯：光照 < 50 Lux。cooldown 60s 防抖，duration 0（本点满足即判定）。
	onID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "天黑开灯"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": d.edge.EdgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "lt",
		"trigger_threshold":      50.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           60,
	})
	// 天亮关灯：光照 > 200 Lux。两条规则必须各自独立求值（互不吞噬）。
	offID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "天亮关灯"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": d.edge.EdgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           60,
	})

	// —— 黄昏：30 Lux ——
	if err := d.report(map[string]float64{"illuminance": 30}); err != nil {
		e.Fatalf("上报光照数据失败: %v", err)
	}
	on := sceneWaitEvent(e, onID, "notification")
	if on.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", on.TriggerSource)
	}
	if on.TriggerValue == nil {
		e.Fatalf("sensor_threshold 事件必须带 trigger_value（用户要知道当时是多少）: %+v", on)
	}
	autoEventuallyFloat(e.T, "开灯事件的 trigger_value", *on.TriggerValue, 30)
	// 同一批数据下"天亮关灯"绝不该触发（比较方向正确）。
	sceneAssertNeverFires(e, offID, 3*time.Second, "光照 30 Lux（天黑）")

	// —— 天亮：800 Lux ——
	if err := d.report(map[string]float64{"illuminance": 800}); err != nil {
		e.Fatalf("上报光照数据失败: %v", err)
	}
	off := sceneWaitEvent(e, offID, "notification")
	if off.TriggerValue == nil {
		e.Fatalf("sensor_threshold 事件必须带 trigger_value: %+v", off)
	}
	autoEventuallyFloat(e.T, "关灯事件的 trigger_value", *off.TriggerValue, 800)

	// 不变式：一整天的两个边沿各自只产生一条**决策**（不重复、不互相顶掉）。
	//
	// 注意引擎的真实语义（evaluator.go:430-434）：冷却判定优先于满足性判定，
	// 冷却期内只要这一帧**带了这个字段**就落一条 suppressed_cooldown 审计，
	// 与当前是否满足条件无关。因此"天亮那一帧(800 Lux)"会给开灯规则也留下
	// 一条抑制审计 —— 那是防抖可观测性，不是误触发。本场景据此断言
	// "决策只有一条"，并显式记录抑制审计的存在（不掩饰）。
	if rows := sceneEventRows(e, onID, "notification"); len(rows) != 1 {
		e.Fatalf("开灯策略应有且仅有 1 条 notification 决策，实际 %d 条: %+v", len(rows), rows)
	}
	if rows := sceneEventRows(e, offID, "notification"); len(rows) != 1 {
		e.Fatalf("关灯策略应有且仅有 1 条 notification 决策，实际 %d 条: %+v", len(rows), rows)
	}
	onSuppressed := sceneEventRows(e, onID, "suppressed_cooldown")
	if len(onSuppressed) > 1 {
		e.Fatalf("开灯策略同一冷却窗内落了 %d 条抑制审计，期望 ≤1（同窗节流）", len(onSuppressed))
	}
	for _, row := range onSuppressed {
		if !strings.Contains(row.Detail, "cooldown") {
			e.Fatalf("抑制审计没有写明原因: detail=%q", row.Detail)
		}
	}
	// 开会灯的开销只有一条通知：抑制审计绝不产生用户可见的打扰。
	if notes := sceneNotificationsFor(e, onID); len(notes) != 1 {
		e.Fatalf("开灯策略应只产生 1 条通知，实际 %d 条", len(notes))
	}

	// 不变式：两条规则的触发都必须落到通知中心，且标题里带策略名
	// （否则用户分不清是"该开灯"还是"该关灯"）。
	onNote := sceneWaitNotification(e, onID)
	offNote := sceneWaitNotification(e, offID)
	if onNote.Title == offNote.Title {
		e.Fatalf("开灯与关灯的通知标题相同（%q），用户无法区分", onNote.Title)
	}
	if !strings.Contains(onNote.Title, e.NS(scenarioID, "天黑开灯")) ||
		!strings.Contains(offNote.Title, e.NS(scenarioID, "天亮关灯")) {
		e.Fatalf("通知标题未包含策略名: 开灯=%q 关灯=%q", onNote.Title, offNote.Title)
	}
	if onNote.Read || offNote.Read {
		e.Fatalf("新通知不应是已读状态: 开灯=%v 关灯=%v", onNote.Read, offNote.Read)
	}

	e.Evidence("SIM-SCNE-001.light", map[string]any{
		"edge_device_id": d.edge.EdgeDeviceID,
		"on_rule":        onID, "off_rule": offID,
		"dusk_lux": 30, "dawn_lux": 800,
		"on_event": on.ID, "off_event": off.ID,
		"on_title": onNote.Title, "off_title": offNote.Title,
		"degraded_action": "gpio_set 不可达（见 sceneRun001 注释），改用 notification 动作",
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-002 夜间光伏不发电时自动关闭 BMS 充电，避免无谓损耗
// ---------------------------------------------------------------------------

// sceneRun002 是确认制最真实的用例：光伏不发电 → 策略触发 → **BMS 不被立即操作**。
//
// 断言链刻意做成"排他"的：
//   - 先把 BMS 节点武装到动作目录**可用**（命令门禁全绿），证明"没动手"不是环境噪声；
//   - 再断言事件停在 pending_confirm、command_id 为空、执行记录为空、指令帧为 0。
func sceneRun002(e *harness.Env) {
	const scenarioID = "SIM-SCNE-002"
	// 光伏侧：pv_power（W）。夜间 0 W。
	pv := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "pv", Type: "sim_scene_002_pv",
		Fields: []sceneField{{Name: "pv_power", Type: "uint16", Scale: 1, Offset: 0, Length: 2, Unit: "W"}},
	})
	// BMS 侧：soc（%），型号必须是驱动注册表里的 jiabaida_bms —— 动作目录按型号注册。
	bms := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "bms", Type: "jiabaida_bms",
		Fields:    []sceneField{{Name: "soc", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "%"}},
		Handshake: true,
	})
	// 武装：Hello → ResourceReport（能力四件套 + 运行期通道）→ 配置清单回执。
	// 复现真实固件顺序，复用 cmd.go 的夹具（含 20s 心跳保活）。
	cmdArmNode(e, bms.edge)

	// 前置事实：这台 BMS 现在**真的**可以被下发 set_mos_policy。
	item, ok := cmdActionByID(cmdActionCatalog(e, bms.edge.EdgeDeviceID), sceneMosAction)
	if !ok {
		e.Fatalf("BMS 动作目录里没有 %s（型号 %s）", sceneMosAction, bms.edge.Type)
	}
	if !item.Available {
		e.Fatalf("武装后 %s 仍不可用: reason=%q reason_code=%q（夹具未把运行时事实上报齐）",
			sceneMosAction, item.Reason, item.ReasonCode)
	}
	if item.Definition.Risk != "high" {
		e.Fatalf("%s 的风险等级 = %q，期望 high（确认制的前提）", sceneMosAction, item.Definition.Risk)
	}
	e.Evidence("SIM-SCNE-002.catalog", item)

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "夜间停充"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": pv.edge.EdgeDeviceID,
		"trigger_sensor_name":    "pv_power",
		"trigger_comparator":     "lt",
		"trigger_threshold":      50.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       bms.edge.EdgeDeviceID,
		"action_id":              sceneMosAction,
		"action_params_json":     `{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`,
		"require_confirmed":      true,
		"cooldown_sec":           300,
	})

	// —— 入夜：光伏 0 W ——
	if err := pv.report(map[string]float64{"pv_power": 0}); err != nil {
		e.Fatalf("上报光伏功率失败: %v", err)
	}
	pending := sceneWaitEvent(e, ruleID, "pending_confirm")

	// 不变式 1：动作必须停在"待确认"，绝不能已经下发。
	if pending.CommandID != "" {
		e.Fatalf("待确认事件的 command_id=%q —— 高风险动作未经人工确认就下发了", pending.CommandID)
	}
	if pending.TriggerValue == nil || math.Abs(*pending.TriggerValue) > 1e-3 {
		e.Fatalf("夜间光伏功率应记录为 0 W，实际 %v", pending.TriggerValue)
	}
	if pending.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", pending.TriggerSource)
	}

	// 不变式 2：必须给用户一条待确认通知（否则用户不知道要确认什么）。
	pendingNote := sceneWaitNotificationDetail(e, ruleID)
	if !strings.Contains(pendingNote.Title, "策略待确认") {
		e.Fatalf("待确认通知的标题没有点明待确认语义: %q", pendingNote.Title)
	}
	if !strings.Contains(pendingNote.Description, "待人工确认") {
		e.Fatalf("待确认通知没有说明需要人工确认: title=%q description=%q",
			pendingNote.Title, pendingNote.Description)
	}
	if pendingNote.Read {
		e.Fatalf("新待确认通知不应是已读状态（notification=%d）", pendingNote.ID)
	}

	// 不变式 3（本场景核心）：BMS 一根手指都没动 —— 无执行记录、无指令帧。
	sceneAssertNoDispatch(e, bms, "夜间光伏不发电（未人工确认）")

	// 保护必须仍然存在：指令没有下发，待确认事件仍然可被人工确认。
	stillPending := sceneEventRows(e, ruleID, "pending_confirm")
	if len(stillPending) != 1 || stillPending[0].ID != pending.ID {
		e.Fatalf("待确认事件被意外推进: %+v", stillPending)
	}

	e.Evidence("SIM-SCNE-002.night", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID, "result": pending.Result,
		"pv_power": *pending.TriggerValue, "pv_edge_device_id": pv.edge.EdgeDeviceID,
		"bms_edge_device_id": bms.edge.EdgeDeviceID, "notification_id": pendingNote.ID,
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-003 电池 SOC 过低时断开负载并同时通知用户
// ---------------------------------------------------------------------------

// sceneRun003 验证"一次上报驱动两条策略"这一真实用法：
// 通知类策略立刻告诉用户，动作类策略停在待确认；管理员确认后负载真的被断开
// （放电 MOS 软件关闭位 = 1），并且指令帧真的出网。
func sceneRun003(e *harness.Env) {
	const scenarioID = "SIM-SCNE-003"
	bms := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "bms", Type: "jiabaida_bms",
		Fields:    []sceneField{{Name: "soc", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "%"}},
		Handshake: true,
	})
	cmdArmNode(e, bms.edge)

	// 策略 A：纯通知（critical → 通知中心 type=error），用户第一时间知道。
	notifyID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "电量过低提醒"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": bms.edge.EdgeDeviceID,
		"trigger_sensor_name":    "soc",
		"trigger_comparator":     "lt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "critical",
		"cooldown_sec":           300,
	})
	// 策略 B：断开负载 = 关闭放电 MOS（高风险 → 必须人工确认）。
	cutID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "断开负载"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": bms.edge.EdgeDeviceID,
		"trigger_sensor_name":    "soc",
		"trigger_comparator":     "lt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       bms.edge.EdgeDeviceID,
		"action_id":              sceneMosAction,
		"action_params_json":     `{"charge_software_closed":false,"discharge_software_closed":true,"priority":"operator"}`,
		"require_confirmed":      true,
		"cooldown_sec":           300,
	})

	// —— 真实 SOC 上报：15.0% ——
	if err := bms.report(map[string]float64{"soc": 15.0}); err != nil {
		e.Fatalf("上报 SOC 失败: %v", err)
	}

	// 不变式 1：两条策略都由同一次真实上报驱动，且都归因到 15.0%。
	noteEvent := sceneWaitEvent(e, notifyID, "notification")
	if noteEvent.TriggerValue == nil {
		e.Fatalf("通知事件缺少 trigger_value: %+v", noteEvent)
	}
	autoEventuallyFloat(e.T, "通知事件的 trigger_value", *noteEvent.TriggerValue, 15)
	cutEvent := sceneWaitEvent(e, cutID, "pending_confirm")
	if cutEvent.TriggerValue == nil {
		e.Fatalf("断开负载事件缺少 trigger_value: %+v", cutEvent)
	}
	autoEventuallyFloat(e.T, "断开负载事件的 trigger_value", *cutEvent.TriggerValue, 15)
	if cutEvent.CommandID != "" {
		e.Fatalf("断开负载未经确认就已下发: command_id=%q", cutEvent.CommandID)
	}

	// 不变式 2：用户真的收到了告警级通知（critical → error），且不是已读。
	note := sceneWaitNotification(e, notifyID)
	if note.Type != "error" {
		e.Fatalf("critical 策略的通知 type=%q，期望 error（重度告警）", note.Type)
	}
	if note.Read {
		e.Fatalf("新告警通知不应是已读状态（notification=%d）", note.ID)
	}
	if !strings.Contains(note.Title, e.NS(scenarioID, "电量过低提醒")) {
		e.Fatalf("通知标题未包含策略名，用户无法辨认来源: %q", note.Title)
	}

	// 不变式 3：确认之前，负载一刻也没有被断开。
	sceneAssertNoDispatch(e, bms, "SOC 过低但尚未人工确认")

	// —— 管理员确认：断开负载真正下发 ——
	before := bms.edge.Device.FrameSeq()
	closed := sceneConfirm(e, cutEvent.ID)
	if closed.Result != "executed" {
		e.Fatalf("人工确认后事件 result=%q，期望 executed（detail=%q）", closed.Result, closed.Detail)
	}
	if closed.CommandID == "" {
		e.Fatalf("executed 事件必须带 command_id（回链 command_executions）")
	}

	// 不变式 4：指令帧真的出网，且字节语义就是"关闭放电 MOS"。
	cmd := cmdAwaitChannelCmd(e, bms.edge, before, 20*time.Second)
	sceneAssertMosFrame(e, cmd, sceneMosFramePriorityOperator, 0x02, "SIM-SCNE-003 断开负载")

	// 不变式 5：执行记录可查（用户/运维能追溯这次"断开负载"）。
	list := e.Admin.Get("/api/v1/edge-devices/" + strconv.FormatInt(bms.edge.EdgeDeviceID, 10) + "/operations").
		Expect(http.StatusOK)
	var executions []struct {
		CommandID string `json:"command_id"`
		ActionID  string `json:"action_id"`
	}
	list.Decode(&executions)
	found := false
	for _, item := range executions {
		if item.CommandID == closed.CommandID && item.ActionID == sceneMosAction {
			found = true
		}
	}
	if !found {
		e.Fatalf("执行记录里没有这次断开负载（command_id=%s）：%s", closed.CommandID, list.BodyString())
	}

	e.Evidence("SIM-SCNE-003.low_soc", map[string]any{
		"notify_rule": notifyID, "cut_rule": cutID,
		"soc": 15.0, "notification_type": note.Type,
		"pending_event": cutEvent.ID, "closed_event": closed.ID, "command_id": closed.CommandID,
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-004 温度过高时停止充电并发出告警
// ---------------------------------------------------------------------------

// sceneRun004 与 003 的区别在于**被守护的物理语义不同**：
// 003 关的是放电 MOS（断负载），004 关的是充电 MOS（停充电），并且温度与 SOC
// 来自同一帧（多字段解析器，证明引擎是按字段取值判定而不是"有数据就报"）。
func sceneRun004(e *harness.Env) {
	const scenarioID = "SIM-SCNE-004"
	bms := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "bms", Type: "jiabaida_bms",
		Fields: []sceneField{
			{Name: "battery_temp", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "℃"},
			{Name: "soc", Type: "uint16", Scale: 0.1, Offset: 2, Length: 2, Unit: "%"},
		},
		Handshake: true,
	})
	cmdArmNode(e, bms.edge)

	stopID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "高温停充"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": bms.edge.EdgeDeviceID,
		"trigger_sensor_name":    "battery_temp",
		"trigger_comparator":     "gt",
		"trigger_threshold":      45.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       bms.edge.EdgeDeviceID,
		"action_id":              sceneMosAction,
		"action_params_json":     `{"charge_software_closed":true,"discharge_software_closed":false,"priority":"operator"}`,
		"require_confirmed":      true,
		"cooldown_sec":           300,
	})
	// 对照策略：SOC 正常，绝不该触发。
	guardID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "电量正常对照"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": bms.edge.EdgeDeviceID,
		"trigger_sensor_name":    "soc",
		"trigger_comparator":     "lt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           300,
	})

	// —— 同一帧里：电池温度 52.0℃、SOC 60.0% ——
	if err := bms.report(map[string]float64{"battery_temp": 52.0, "soc": 60.0}); err != nil {
		e.Fatalf("上报电池数据失败: %v", err)
	}
	sceneWaitSensor(e, bms.edge.EdgeDeviceID, "battery_temp", 52.0)

	event := sceneWaitEvent(e, stopID, "pending_confirm")
	if event.TriggerValue == nil {
		e.Fatalf("高温事件缺少 trigger_value: %+v", event)
	}
	autoEventuallyFloat(e.T, "高温事件的 trigger_value", *event.TriggerValue, 52)
	if event.CommandID != "" {
		e.Fatalf("停止充电未经确认就已下发: command_id=%q", event.CommandID)
	}

	// 不变式 1：告警必须到达用户（确认制产生的就是 warning 级"待确认"通知）。
	note := sceneWaitNotification(e, stopID)
	if note.Type != "warning" {
		e.Fatalf("%s 的告警通知 type=%q，期望 warning", scenarioID, note.Type)
	}
	if !strings.Contains(note.Title, e.NS(scenarioID, "高温停充")) {
		e.Fatalf("告警通知标题未包含策略名: %q", note.Title)
	}
	if note.Read {
		e.Fatalf("新告警通知不应是已读状态")
	}

	// 不变式 2：同帧里的正常 SOC 不产生任何事件（无误报）。
	if rows := sceneEventRows(e, guardID, ""); len(rows) != 0 {
		e.Fatalf("SOC=60%% 的对照策略不应产生事件，实际 %d 条: %+v", len(rows), rows[0])
	}
	// 不变式 3：确认之前充电一刻也没停。
	sceneAssertNoDispatch(e, bms, "温度过高但尚未人工确认")

	// —— 管理员确认：停止充电真正下发 ——
	before := bms.edge.Device.FrameSeq()
	closed := sceneConfirm(e, event.ID)
	if closed.Result != "executed" || closed.CommandID == "" {
		e.Fatalf("确认后事件 result=%q command_id=%q，期望 executed + 指令号", closed.Result, closed.CommandID)
	}
	cmd := cmdAwaitChannelCmd(e, bms.edge, before, 20*time.Second)
	// 停止充电 = 关闭**充电** MOS（bit0=1、bit1=0）。
	sceneAssertMosFrame(e, cmd, sceneMosFramePriorityOperator, 0x01, "SIM-SCNE-004 停止充电")

	e.Evidence("SIM-SCNE-004.over_temp", map[string]any{
		"rule_id": stopID, "guard_rule_id": guardID,
		"battery_temp": 52.0, "soc": 60.0,
		"event_id": event.ID, "command_id": closed.CommandID, "notification_type": note.Type,
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-005 雨量达到中雨时给用户发一条提醒通知
// ---------------------------------------------------------------------------

// sceneRun005 用真实雨量数据证明"中雨阈值"这一分级语义：
// 2.0mm（小雨）不打扰用户，15.0mm（中雨）立刻提醒。
func sceneRun005(e *harness.Env) {
	const scenarioID = "SIM-SCNE-005"
	// 雨量计：rainfall（mm，0.1mm 分辨率）—— 型号用真实驱动 sn3001_rain，
	// 但解析走场景自建的多字段解析器（ConfigParser 分支优先于驱动的 ParseData）。
	rain := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "rain", Type: "sn3001_rain",
		Fields: []sceneField{{Name: "rainfall", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "mm"}},
	})

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "中雨提醒"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": rain.edge.EdgeDeviceID,
		"trigger_sensor_name":    "rainfall",
		"trigger_comparator":     "gte",
		"trigger_threshold":      10.0, // 中雨下限（12 小时降水量 10mm）
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           1800,
	})

	// —— 先下小雨：2.0mm ——
	if err := rain.report(map[string]float64{"rainfall": 2.0}); err != nil {
		e.Fatalf("上报雨量失败: %v", err)
	}
	// 先证明这一帧真的走完了上报→解析→落库，再断言"没有误报"。
	sceneWaitSensor(e, rain.edge.EdgeDeviceID, "rainfall", 2.0)
	sceneAssertNeverFires(e, ruleID, 3*time.Second, "雨量 2.0mm（小雨）")

	// —— 雨量到中雨：15.0mm ——
	if err := rain.report(map[string]float64{"rainfall": 15.0}); err != nil {
		e.Fatalf("上报雨量失败: %v", err)
	}
	event := sceneWaitEvent(e, ruleID, "notification")
	if event.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", event.TriggerSource)
	}
	if event.TriggerValue == nil {
		e.Fatalf("中雨事件必须带 trigger_value: %+v", event)
	}
	autoEventuallyFloat(e.T, "中雨事件的 trigger_value", *event.TriggerValue, 15)

	// 不变式：用户真的收到一条提醒，能看出是哪条策略、什么级别。
	note := sceneWaitNotification(e, ruleID)
	if note.Type != "warning" {
		e.Fatalf("中雨提醒的通知 type=%q，期望 warning", note.Type)
	}
	if !strings.Contains(note.Title, e.NS(scenarioID, "中雨提醒")) {
		e.Fatalf("通知标题未包含策略名: %q", note.Title)
	}
	// 不改写引擎的既有语义：notification 动作的标题前缀固定为"策略通知: "。
	if !strings.Contains(note.Title, "策略通知") {
		e.Fatalf("通知标题与引擎既有语义不符（期望 策略通知: <策略名>）: %q", note.Title)
	}
	if note.Read {
		e.Fatalf("新通知不应是已读状态")
	}
	// 只有一条同级别提醒：中雨只打扰一次。
	if notes := sceneNotificationsFor(e, ruleID); len(notes) != 1 {
		e.Fatalf("中雨策略应只产生 1 条通知，实际 %d 条", len(notes))
	}

	e.Evidence("SIM-SCNE-005.moderate_rain", map[string]any{
		"rule_id": ruleID, "edge_device_id": rain.edge.EdgeDeviceID,
		"light_rain_mm": 2.0, "moderate_rain_mm": 15.0,
		"event_id": event.ID, "notification_id": note.ID, "notification_type": note.Type,
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-006 雨量升级到大雨时发出更高等级通知，且不与中雨重复打扰
// ---------------------------------------------------------------------------

// sceneRun006 守护两条语义：分级升级（warning → error）与冷却抑制
// （中雨规则在同一冷却窗内只落一条 suppressed_cooldown 审计，绝不重复轰炸）。
func sceneRun006(e *harness.Env) {
	const scenarioID = "SIM-SCNE-006"
	rain := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "rain", Type: "sn3001_rain",
		Fields: []sceneField{{Name: "rainfall", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "mm"}},
	})

	moderateID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "中雨提醒"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": rain.edge.EdgeDeviceID,
		"trigger_sensor_name":    "rainfall",
		"trigger_comparator":     "gte",
		"trigger_threshold":      10.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           3600,
	})
	heavyID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "大雨提醒"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": rain.edge.EdgeDeviceID,
		"trigger_sensor_name":    "rainfall",
		"trigger_comparator":     "gte",
		"trigger_threshold":      25.0, // 大雨下限（12 小时降水量 25mm）
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "critical",
		"cooldown_sec":           3600,
	})
	// 前置：雨量升级之前，大雨策略一条事件都不能有。
	if rows := sceneEventRows(e, heavyID, ""); len(rows) != 0 {
		e.Fatalf("前置不成立：大雨策略 %d 已有 %d 条事件", heavyID, len(rows))
	}

	// —— 第一步：中雨 15.0mm ——
	if err := rain.report(map[string]float64{"rainfall": 15.0}); err != nil {
		e.Fatalf("上报雨量失败: %v", err)
	}
	moderate := sceneWaitEvent(e, moderateID, "notification")
	autoEventuallyFloat(e.T, "中雨事件的 trigger_value", *moderate.TriggerValue, 15)
	moderateNote := sceneWaitNotification(e, moderateID)
	if moderateNote.Type != "warning" {
		e.Fatalf("中雨提醒的通知 type=%q，期望 warning", moderateNote.Type)
	}

	// —— 第二步：升级到大雨 30.0mm，连报三帧（模拟真实高频上报） ——
	for i := 0; i < 3; i++ {
		if err := rain.report(map[string]float64{"rainfall": 30.0}); err != nil {
			e.Fatalf("第 %d 次上报大雨失败: %v", i+1, err)
		}
	}
	heavy := sceneWaitEvent(e, heavyID, "notification")
	autoEventuallyFloat(e.T, "大雨事件的 trigger_value", *heavy.TriggerValue, 30)
	heavyNote := sceneWaitNotification(e, heavyID)

	// 不变式 1：大雨通知的等级必须**高于**中雨（critical → error vs warning）。
	if heavyNote.Type != "error" {
		e.Fatalf("大雨通知 type=%q，期望 error（高于中雨的 warning）", heavyNote.Type)
	}
	if moderateNote.Type == heavyNote.Type {
		e.Fatalf("中雨与大雨通知级别相同（%q），分级失效", heavyNote.Type)
	}

	// 不变式 2：不与中雨重复打扰 —— 中雨策略只有 1 条事件、1 条通知。
	if rows := sceneEventRows(e, moderateID, "notification"); len(rows) != 1 {
		e.Fatalf("中雨策略应只执行 1 次，实际 %d 条 notification 事件: %+v", len(rows), rows)
	}
	e.Eventually(15*time.Second, func() error {
		if notes := sceneNotificationsFor(e, moderateID); len(notes) != 1 {
			return fmt.Errorf("中雨策略的通知条数 = %d，期望 1（冷却期内不得重复打扰）", len(notes))
		}
		return nil
	})

	// 不变式 3：冷却期内的重复越限必须留下**且只留下一条** suppressed_cooldown 审计
	// （同窗节流：高频上报不刷量）。三帧大雨同时命中了中雨规则的冷却窗。
	e.Eventually(15*time.Second, func() error {
		rows := sceneEventRows(e, moderateID, "suppressed_cooldown")
		if len(rows) != 1 {
			return fmt.Errorf("中雨策略的冷却抑制审计 = %d 条，期望 1（同一冷却窗只落首条）", len(rows))
		}
		if !strings.Contains(rows[0].Detail, "cooldown") {
			return fmt.Errorf("冷却抑制审计没有写明原因: detail=%q", rows[0].Detail)
		}
		return nil
	})
	// 大雨规则自己也在冷却中：后两帧同样只落一条审计。
	e.Eventually(15*time.Second, func() error {
		rows := sceneEventRows(e, heavyID, "suppressed_cooldown")
		if len(rows) != 1 {
			return fmt.Errorf("大雨策略的冷却抑制审计 = %d 条，期望 1", len(rows))
		}
		return nil
	})
	// 通知中心里两条策略各只有一条 —— 用户不被重复轰炸。
	if notes := sceneNotificationsFor(e, heavyID); len(notes) != 1 {
		e.Fatalf("大雨策略应只产生 1 条通知，实际 %d 条", len(notes))
	}

	e.Evidence("SIM-SCNE-006.rain_escalation", map[string]any{
		"moderate_rule": moderateID, "heavy_rule": heavyID,
		"moderate_mm": 15.0, "heavy_mm": 30.0, "heavy_frames": 3,
		"moderate_type": moderateNote.Type, "heavy_type": heavyNote.Type,
		"moderate_notifications": len(sceneNotificationsFor(e, moderateID)),
		"heavy_notifications":    len(sceneNotificationsFor(e, heavyID)),
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-007 无人值守时段出现异常只通知、不自动动作
// ---------------------------------------------------------------------------

// sceneRun007 用 time_window 触发器（时钟驱动，独立 1min ticker）表达"无人值守时段"，
// 并在同一时段内制造一次真实异常，断言"只通知、不自动动作"。
//
// 窗口刻意设成 now-1h .. now+1h（而不是写死 22:00-06:00）：
// 仿真可以在任何时刻运行，写死深夜窗口会让场景在白天永远不触发 ——
// 这是为了让场景**可重复**而做的等价表达（真实部署里这就是运维配置的无人值守时段）。
// isInWindow 对跨零点窗口的处理是 [start,24:00) ∪ [00:00,end)，
// 因此 now±1h 跨零点时判定依然正确。
func sceneRun007(e *harness.Env) {
	const scenarioID = "SIM-SCNE-007"
	bms := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "unm", Type: "jiabaida_bms",
		Fields:    []sceneField{{Name: "battery_temp", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "℃"}},
		Handshake: true,
	})
	cmdArmNode(e, bms.edge)

	now := time.Now()
	windowStart := now.Add(-time.Hour).Format("15:04")
	windowEnd := now.Add(time.Hour).Format("15:04")

	// 无人值守巡检：时段内只发通知（enter/exit/inside 三选一，
	// inside = 窗口内每个 tick 参与求值，受 cooldown 抑制）。
	watchID := autoCreateRule(e, map[string]any{
		"name":                 e.NS(scenarioID, "无人值守巡检"),
		"enabled":              true,
		"trigger_type":         "time_window",
		"trigger_window_start": windowStart,
		"trigger_window_end":   windowEnd,
		"trigger_window_edge":  "inside",
		"action_type":          "notification",
		"action_level":         "warning",
		"cooldown_sec":         3600,
	})
	// 时段内的异常：电池温度过高 → 高风险动作（必须人工确认）。
	anomalyID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "异常停充"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": bms.edge.EdgeDeviceID,
		"trigger_sensor_name":    "battery_temp",
		"trigger_comparator":     "gt",
		"trigger_threshold":      45.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       bms.edge.EdgeDeviceID,
		"action_id":              sceneMosAction,
		"action_params_json":     `{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`,
		"require_confirmed":      true,
		"cooldown_sec":           300,
	})

	// —— 时段内出现异常：55.0℃ ——
	if err := bms.report(map[string]float64{"battery_temp": 55.0}); err != nil {
		e.Fatalf("上报电池温度失败: %v", err)
	}

	// 不变式 1：时段规则必须由真实 ticker 求值并触发。
	// 超时给满一个 tick 周期（60s）+ 余量：ticker 起点是服务进程启动时刻，
	// 与本场景进入时刻无关，最坏情况要等将近一整个周期。
	watchEvent := sceneWaitEventWithin(e, watchID, "notification", 100*time.Second)
	// time_window 事件没有传感器触发值（planner 只为 sensor_threshold 记录 trigger_value）。
	if watchEvent.TriggerValue != nil {
		e.Fatalf("time_window 事件不应带 trigger_value，实际 %v", *watchEvent.TriggerValue)
	}
	if watchEvent.TriggerSource != "auto" {
		e.Fatalf("时段事件的 trigger_source=%q，期望 auto", watchEvent.TriggerSource)
	}
	watchNote := sceneWaitNotification(e, watchID)
	if !strings.Contains(watchNote.Title, e.NS(scenarioID, "无人值守巡检")) {
		e.Fatalf("时段提醒的标题未包含策略名: %q", watchNote.Title)
	}

	// 不变式 2：异常只产生"待确认"，没有任何自动动作。
	anomaly := sceneWaitEvent(e, anomalyID, "pending_confirm")
	if anomaly.CommandID != "" {
		e.Fatalf("无人值守时段内高风险动作自动下发了: command_id=%q", anomaly.CommandID)
	}
	if anomaly.TriggerValue == nil {
		e.Fatalf("异常事件缺少 trigger_value: %+v", anomaly)
	}
	autoEventuallyFloat(e.T, "异常事件的 trigger_value", *anomaly.TriggerValue, 55)
	anomalyNote := sceneWaitNotification(e, anomalyID)
	if anomalyNote.Type != "warning" {
		e.Fatalf("异常待确认通知 type=%q，期望 warning", anomalyNote.Type)
	}

	// 不变式 3（本场景核心）：整段时间里没有任何物理动作发生。
	sceneAssertNoDispatch(e, bms, "无人值守时段内的异常")
	e.Eventually(10*time.Second, func() error {
		if rows := sceneEventRows(e, anomalyID, "executed"); len(rows) != 0 {
			return fmt.Errorf("异常策略不应出现 executed 事件: %+v", rows)
		}
		return nil
	})

	e.Evidence("SIM-SCNE-007.unattended", map[string]any{
		"watch_rule": watchID, "anomaly_rule": anomalyID,
		"window_start": windowStart, "window_end": windowEnd,
		"battery_temp": 55.0, "watch_event": watchEvent.ID, "anomaly_event": anomaly.ID,
	})
}

// ---------------------------------------------------------------------------
// SIM-SCNE-008 光伏发电恢复后自动恢复 BMS 充电（对称策略）
// ---------------------------------------------------------------------------

// sceneRun008 是 002 的恢复侧：白天光伏恢复 → 策略触发 → 人工确认 →
// 充电 MOS 重新打开（E1 帧 mos 位 = 0x00），并且真的下发到节点。
func sceneRun008(e *harness.Env) {
	const scenarioID = "SIM-SCNE-008"
	pv := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "pv", Type: "sim_scene_008_pv",
		Fields: []sceneField{{Name: "pv_power", Type: "uint16", Scale: 1, Offset: 0, Length: 2, Unit: "W"}},
	})
	bms := sceneProvision(e, sceneSpec{
		Scenario: scenarioID, Suffix: "bms", Type: "jiabaida_bms",
		Fields:    []sceneField{{Name: "soc", Type: "uint16", Scale: 0.1, Offset: 0, Length: 2, Unit: "%"}},
		Handshake: true,
	})
	cmdArmNode(e, bms.edge)

	item, ok := cmdActionByID(cmdActionCatalog(e, bms.edge.EdgeDeviceID), sceneMosAction)
	if !ok || !item.Available {
		e.Fatalf("%s 在武装后的目录里不可用: ok=%v item=%+v", sceneMosAction, ok, item)
	}

	// 恢复侧：光伏功率恢复到 200W 以上 → 恢复充电（充电软件关闭位 = false）。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS(scenarioID, "光伏恢复充电"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": pv.edge.EdgeDeviceID,
		"trigger_sensor_name":    "pv_power",
		"trigger_comparator":     "gte",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       bms.edge.EdgeDeviceID,
		"action_id":              sceneMosAction,
		"action_params_json":     `{"charge_software_closed":false,"discharge_software_closed":false,"priority":"user"}`,
		"require_confirmed":      true,
		"cooldown_sec":           300,
	})

	// —— 前置对照：夜间 0W，恢复侧策略绝不该触发 ——
	if err := pv.report(map[string]float64{"pv_power": 0}); err != nil {
		e.Fatalf("上报光伏功率失败: %v", err)
	}
	sceneWaitSensor(e, pv.edge.EdgeDeviceID, "pv_power", 0)
	sceneAssertNeverFires(e, ruleID, 3*time.Second, "夜间光伏 0W")

	// —— 天亮：光伏恢复到 650W ——
	if err := pv.report(map[string]float64{"pv_power": 650}); err != nil {
		e.Fatalf("上报光伏功率失败: %v", err)
	}
	pending := sceneWaitEvent(e, ruleID, "pending_confirm")
	if pending.TriggerValue == nil {
		e.Fatalf("恢复充电事件缺少 trigger_value: %+v", pending)
	}
	autoEventuallyFloat(e.T, "恢复充电事件的 trigger_value", *pending.TriggerValue, 650)
	// 确认之前不得动手（与 002 同一条不变量，方向相反）。
	if pending.CommandID != "" {
		e.Fatalf("恢复充电未经确认就已下发: command_id=%q", pending.CommandID)
	}
	sceneWaitNotification(e, ruleID)
	sceneAssertNoDispatch(e, bms, "光伏恢复但尚未人工确认")

	// —— 管理员确认：恢复充电真正下发 ——
	before := bms.edge.Device.FrameSeq()
	closed := sceneConfirm(e, pending.ID)
	if closed.Result != "executed" {
		e.Fatalf("确认后事件 result=%q，期望 executed（detail=%q）", closed.Result, closed.Detail)
	}
	if closed.CommandID == "" {
		e.Fatalf("executed 事件必须带 command_id")
	}
	cmd := cmdAwaitChannelCmd(e, bms.edge, before, 20*time.Second)
	// 恢复充电 = 充电与放电 MOS 都不关闭（mos = 0x00）。
	sceneAssertMosFrame(e, cmd, sceneMosFramePriorityUser, 0x00, "SIM-SCNE-008 恢复充电")

	// 闭环：事件与执行记录互相可追溯。
	e.Eventually(10*time.Second, func() error {
		rows := sceneEventRows(e, ruleID, "executed")
		for _, row := range rows {
			if row.ID == pending.ID && row.CommandID == closed.CommandID {
				return nil
			}
		}
		return fmt.Errorf("执行事件尚未收敛到 command_id=%s", closed.CommandID)
	})

	e.Evidence("SIM-SCNE-008.pv_recovery", map[string]any{
		"rule_id": ruleID, "pv_edge_device_id": pv.edge.EdgeDeviceID,
		"bms_edge_device_id": bms.edge.EdgeDeviceID,
		"night_w":            0, "recovered_w": 650,
		"pending_event": pending.ID, "command_id": closed.CommandID,
	})
}
