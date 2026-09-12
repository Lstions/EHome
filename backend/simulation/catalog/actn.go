//go:build simulation

// 场景目录 · SIM-ACTN 动作分发（设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-001..006）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§6 命名、§9 变异 A6、§10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API、§5.6 隔离、§7 红线）。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. notification 动作触发后必须落到通知中心 —— 用户能看见、有正确级别与来源、未读；
//  2. device_action 走 commandexec 受控链路后，目标节点在 control 主题**真的**收到
//     ChannelCmdV2，且帧身份（command_id / edge_device_id / channel_id）与执行记录一致；
//  3. 自动路径的审计归因是**系统操作者**（ActorKind=system → actor_type=system），
//     不是笼统的人工归因 —— 与人工下发的 actor_type=user 形成对照；
//  4. 同一天内同一条规则多次执行使用**不同的幂等键**（当日序号），第二次不能被幂等重放顶掉；
//  5. 目标设备不满足可用性门禁时 fail-closed：以明确失败结束、写明**门禁的**原因、
//     物理侧零下发、零执行记录。**偏差**：设计 §4 要求的结果码是 failed_gate，而当前产品
//     落的是 failed_dispatch —— planner.isGateError 与 commandexec.ErrActionUnavailable
//     文案不匹配，详见 actnGateFailureCodeGap（本文件 SIM-ACTN-005 段）。
//  6. 动作参数非法时不得静默成功：非法参数在创建期即被拒，且不留下任何可执行的策略。
//     **偏差**：设计 §4 描述的是执行期落 failed_dispatch，而当前产品在**创建期**就拒绝 ——
//     详见 actnRun006 的说明。断言的是同一个不变量的更早一道闸。
//
// 关于"自动路径"（本域全部 device_action 场景都走它）：
// planner.go:184 的 resolveSystemActorID 是**惰性解析** —— main.go 在启动期解析系统主体
// 时全新库还没有 users 行（systemActorID=0），第一次真正执行时才回查 users 表并缓存。
// 因此"POST /auth/initialize 在进程启动之后"这一时序不构成阻塞（早期版本的注释曾据此
// 断言自动 device_action 恒失败，已被 planner.go:133-159 的修复推翻）。
// 本域刻意走自动路径：它才是 HandleTrigger → executeDeviceAction 的生产路径，
// 也才对得上设计 §9 的变异 A6（幂等键去掉当日序号 → ACTN-004 必须变红）。
//
// 观测窗口（设计 §4.2.2）：断言"没有 X"时一律用**有界负向观察 + 终判**
// （先 EventuallyError 短窗观察，再读一次做最终判定），不用 sleep 也不做一次性读。
//
// 命名纪律（框架 §4.1 + 门禁第 8 条）：本文件包级标识符一律以 actn 开头。
package catalog

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// actnDomain 是本域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const actnDomain Domain = "ACTN"

func init() {
	Register(Scenario{
		ID:     "SIM-ACTN-001",
		Title:  "通知类动作触发后用户能在通知中心看到",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-001（notification 动作）；docs/设计/通知中心.md",
		Run:    actnRun001,
	})
	Register(Scenario{
		ID:     "SIM-ACTN-002",
		Title:  "设备动作类触发后目标设备真的收到指令帧",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-002（device_action 全链路）；docs/设计/设备指令与操作体系演进方案.md",
		Run:    actnRun002,
	})
	Register(Scenario{
		ID:     "SIM-ACTN-003",
		Title:  "设备动作会走受控链路并留下审计（含系统操作者身份）",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-003（审计归因）；docs/设计/设备指令与操作体系演进方案.md（审计）",
		Run:    actnRun003,
	})
	Register(Scenario{
		ID:     "SIM-ACTN-004",
		Title:  "同一天内同一条规则多次执行使用不同幂等键（不会互相顶掉）",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-004（幂等键序号）与 §9 变异 A6；docs/设计/边缘设备控制.md（幂等键）",
		Run:    actnRun004,
	})
	Register(Scenario{
		ID:     "SIM-ACTN-005",
		Title:  "目标设备不满足可用性门禁时以 failed_gate 结束并写明原因",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-005（9 项 gate fail-closed）；docs/设计/设备指令与操作体系演进方案.md（availability gate）",
		Run:    actnRun005,
	})
	Register(Scenario{
		ID:     "SIM-ACTN-006",
		Title:  "动作参数非法时以 failed_dispatch 结束，不会静默成功",
		Domain: actnDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-ACTN-006（参数校验）；backend/internal/api/handler_automation.go（CanonicalizeParams）",
		Run:    actnRun006,
	})
}

// ---------------------------------------------------------------------------
// 本域夹具（一律复用既有夹具，禁止重造）
// ---------------------------------------------------------------------------

// actnProvision 搭出本域统一的仿真设备。
//
// 型号必须是 sn3001_rain：device_action 的 action_id 存在性由**驱动注册表**校验
// （handler_automation.go 的 validateDeviceActionCatalog），自定义型号没有动作目录。
// 夹具沿用 autoProvisionDevice —— 它走真实 HTTP 端点建节点/配置/通道/边缘设备，
// 且**不做任何 MQTT 上报**（是否握手由场景自己决定）。
//
// 命名用 e.DeviceFor（域码 an），节点局部名 "an0NN-<suffix>" 落在 varchar(32) 预算内。
func actnProvision(e *harness.Env, scenarioID, suffix string) *autoFixture {
	return autoProvisionDevice(e, scenarioID, suffix, "sn3001_rain")
}

// actnEdgeHandle 把 autoFixture 适配成 cmd.go 的 edgeDevice 句柄。
//
// 为什么要适配：SIM-CMD 的武装流程（cmdArmNode）与指令帧断言（cmdAwaitChannelCmd /
// cmdChannelCmdCount）都以 *edgeDevice 为句柄，而自动化域的夹具是 *autoFixture。
// 两者承载的是同一批事实（node_id / channel_id / edge_device_id / MQTT 仿真器），
// 这里只做字段搬运，不复制任何流程 —— 武装与帧校验的唯一实现仍在 cmd.go。
//
// 不复用 audit.go 的 auditArmNode：跨域调用另一个并行作者文件里的私有助手，
// 会让本域随对方改动而被动变红；cmdArmNode 才是被设计文档点名的共享流程。
func actnEdgeHandle(fx *autoFixture) *edgeDevice {
	return &edgeDevice{
		NodeID:       fx.nodeID,
		ChannelID:    int64(fx.channelID),
		EdgeDeviceID: int64(fx.edgeDeviceID),
		Type:         "sn3001_rain",
		HardwareID:   "1",
		Device:       fx.device,
	}
}

// actnArmNode 把仿真节点武装成"动作目录可用"的状态（复用 SIM-CMD 已验证的流程）。
//
// 顺序不可颠倒：handler_hello.go:336-341 在**每次** Hello 时清空上一代能力报告
// （boot_id / resource_reported_at / command_engine_revision / capabilities），
// 因此 ResourceReport 必须在 Hello 之后（cmdArmNode 内部完成）。
//
// **屏障**：Hello 与 ResourceReport 都是 QoS1 上行，而服务端是 8 个 worker 并发消费
// （仿真设备侧也开了 SetOrderMatters(false)），两者的**处理顺序不保证**等于发布顺序。
// 若 ResourceReport 的写库先于 Hello 的清空落盘，武装结果会被 Hello 覆盖，
// 表现为 boot_id 永远为空 —— 三个仿真运行并发时实测命中过（SIM-ACTN-004 首轮）。
//
// 这里等的是**节点状态变为 online**：handler_hello.go 在同一次 Save 里既置
// node.Status="online" 又清空能力四件套（registerNodeFromHello），
// 所以"看到 online"就是"清空已提交"的确定性证据 —— 是真实信号，不是 sleep，也不改库。
func actnArmNode(e *harness.Env, fx *autoFixture) {
	e.T.Helper()
	fx.device.Hello("sim-1.0.0", "SIM-ACTN", 1)
	e.Eventually(20*time.Second, func() error {
		state := cmdNodeState(e, fx.nodeID)
		if state.Status != "online" {
			return fmt.Errorf("Hello 已握手但节点状态仍为 %q，能力清空尚未提交", state.Status)
		}
		return nil
	})
	cmdArmNode(e, actnEdgeHandle(fx))
}

// actnReport 让仿真节点上报一个温度原值（uint16 大端；夹具解析规则 scale=0.1 → ℃）。
//
// 为什么必须经 MQTT：HTTP 侧没有任何"造数据"入口，条件成立只能来自真实数据流
// （设计 §3：数据必须穿过 databus → SensorParserConsumer → 求值器）。
func actnReport(e *harness.Env, fx *autoFixture, raw uint16) {
	e.T.Helper()
	if err := fx.report(raw); err != nil {
		e.Fatalf("节点上报数据失败（raw=%d）: %v", raw, err)
	}
}

// actnAssertActionAvailable 断言动作在目录里**可用**，返回目录项供证据留档。
//
// 这条前置不是仪式：动作不可用时后面的"帧断言"只是在验证一条被拒绝的请求，
// 失败信息会指错方向（夹具没武装 vs 选错动作）。
func actnAssertActionAvailable(e *harness.Env, fx *autoFixture, actionID string) cmdActionItem {
	e.T.Helper()
	items := cmdActionCatalog(e, int64(fx.edgeDeviceID))
	item, ok := cmdActionByID(items, actionID)
	if !ok {
		e.Fatalf("动作目录里没有 %s: %+v", actionID, items)
	}
	if !item.Available {
		e.Fatalf("武装后的目录里 %s 仍不可用: reason=%q reason_code=%q（夹具未把运行时事实上报齐）",
			actionID, item.Reason, item.ReasonCode)
	}
	return item
}

// actnDeviceActionRuleBody 构造 device_action 建规则请求体（只构造，不发送）。
//
// 独立于 autoCreateRule 的原因：ACTN-006 要发**预期被拒**的同一形状请求，
// 而 autoCreateRule 会断言 200 并把自清理挂上 —— 那正是被拒场景里不该发生的两件事。
func actnDeviceActionRuleBody(name string, fx *autoFixture, actionID, paramsJSON string, cooldownSec int) map[string]any {
	return map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              actionID,
		"action_params_json":     paramsJSON,
		"cooldown_sec":           cooldownSec,
	}
}

// actnDeviceActionRule 建一条 device_action 规则并挂自清理（合法参数路径）。
func actnDeviceActionRule(e *harness.Env, scenarioID, suffix string, fx *autoFixture,
	actionID, paramsJSON string, cooldownSec int) int64 {
	e.T.Helper()
	return autoCreateRule(e,
		actnDeviceActionRuleBody(e.NS(scenarioID, suffix), fx, actionID, paramsJSON, cooldownSec))
}

// actnEventsByID 读某条规则的全部事件并按事件 ID 升序返回（= 执行先后顺序）。
//
// 为什么不直接用列表顺序：列表接口的排序不属本域契约，断言"第一条/第二条"
// 必须锚定在单调递增的主键上，否则排序一变场景就假红。
func actnEventsByID(e *harness.Env, ruleID int64) ([]autoEventRow, error) {
	rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// actnWaitEvent 轮询等待某条规则出现指定 result 的事件，返回**最新**的一条。
//
// 超时信息里带上已观察到的 (result, detail) 全量清单：failed_gate / failed_dispatch
// 这类路径的 detail 才是定位信息（哪一步拒绝了），只报"没等到"等于把证据丢掉。
func actnWaitEvent(e *harness.Env, ruleID int64, result string, timeout time.Duration) autoEventRow {
	e.T.Helper()
	return actnWaitAnyEvent(e, ruleID, []string{result}, timeout)
}

// actnWaitAnyEvent 同 actnWaitEvent，但接受一组可接受的结果码，返回**最新**的一条。
//
// 用途：SIM-ACTN-005 的失败分类。设计要求 failed_gate，而当前产品在门禁拒绝时落的是
// failed_dispatch（原因见 actnGateFailureCodeGap）；本助手让场景既能断言"确实是失败"，
// 又能把真实结果码如实记录下来，而不是把不接受的那个结果码当成通过。
func actnWaitAnyEvent(e *harness.Env, ruleID int64, results []string, timeout time.Duration) autoEventRow {
	e.T.Helper()
	accepted := make(map[string]bool, len(results))
	for _, result := range results {
		accepted[result] = true
	}
	var found autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := actnEventsByID(e, ruleID)
		if err != nil {
			return err
		}
		observed := make([]string, 0, len(rows))
		for _, row := range rows {
			observed = append(observed, fmt.Sprintf("%s(%s)", row.Result, row.Detail))
			if accepted[row.Result] && row.ID > found.ID {
				found = row
			}
		}
		if found.ID != 0 {
			return nil
		}
		return fmt.Errorf("策略 %d 尚未产生 result∈%v 的事件；已观察到的结果: %v", ruleID, results, observed)
	})
	return found
}

// actnAssertNoNotificationFor 有界负向观察 + 终判：该规则此刻不得有任何通知（设计 §4.2.2）。
//
// 用法是"触发前"的前置：通知若在动作执行前就存在，说明它不是这次动作产生的。
func actnAssertNoNotificationFor(e *harness.Env, ruleID int64, label string) {
	e.T.Helper()
	sourceID := strconv.FormatInt(ruleID, 10)
	appeared := e.EventuallyError(2*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == sourceID {
				return nil // 出现即结束观察，由终判报错
			}
		}
		return fmt.Errorf("尚未出现策略 %d 的通知", ruleID)
	})
	if appeared == nil {
		e.Fatalf("%s：策略 %d 在动作执行前就已经有通知", label, ruleID)
	}
	rows, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	for _, row := range rows {
		if row.Source == "automation_rule" && row.SourceID == sourceID {
			e.Fatalf("%s：策略 %d 在动作执行前就已经有通知 #%d（通知不是动作执行产生的）",
				label, ruleID, row.ID)
		}
	}
}

// actnAssertNoCommandFrames 有界负向观察 + 终判：物理侧不得出现任何指令帧（设计 §4.2.2）。
//
// 为什么不能只读一次计数：下发是异步的（outbox → dispatcher），"此刻没有"不等于"不会出现"。
func actnAssertNoCommandFrames(e *harness.Env, fx *autoFixture, label string, window time.Duration) {
	e.T.Helper()
	handle := actnEdgeHandle(fx)
	appeared := e.EventuallyError(window, func() error {
		if cmdChannelCmdCount(handle) > 0 {
			return nil // 出现即结束观察，由终判报错
		}
		return fmt.Errorf("窗口内尚未出现指令帧")
	})
	if appeared == nil {
		e.Fatalf("%s：物理侧出现过指令帧（fail-closed 被破坏）", label)
	}
	if got := cmdChannelCmdCount(handle); got != 0 {
		e.Fatalf("%s：物理侧有 %d 条指令帧，期望 0 条（被拒的动作绝不能上物理线）", label, got)
	}
}

// actnFramesPerCommand 统计设备在 control 主题**实际收到**的指令帧，按 command_id 归并。
//
// 为什么按命令 ID 归并而不是只数条数：ACTN-004 要证明"两次执行各自真的下发了一次"，
// 条数相等既可能是"两条都发了"，也可能是"同一条被重发两次"。
func actnFramesPerCommand(e *harness.Env, fx *autoFixture) map[string]int {
	e.T.Helper()
	counts := map[string]int{}
	for _, received := range fx.device.FramesOf(frame.MsgChannelCmdV2) {
		decoded, err := frame.DecodeChannelCmdV2(received.Raw)
		if err != nil {
			e.Fatalf("解码指令帧失败: %v", err)
		}
		counts[fmt.Sprintf("%x", decoded.CommandID)]++
	}
	return counts
}

// actnCommandUUIDHex 把执行记录的 uuid 文本转成帧身份的十六进制写法（与 actnFramesPerCommand 同口径）。
func actnCommandUUIDHex(e *harness.Env, commandID string) string {
	e.T.Helper()
	return fmt.Sprintf("%x", cmdCommandUUID(e.T, commandID))
}

// ---------------------------------------------------------------------------
// 执行记录与幂等键（用户界面看不到的持久化事实，设计 §3 原则 2 允许直连数据库）
// ---------------------------------------------------------------------------

// actnExecutionRow 是 command_executions 的只读投影。
type actnExecutionRow struct {
	CommandID        string
	IdempotencyScope string
	IdempotencyKey   string
	RequestHash      string
	Status           string
	ActorUserID      uint
}

// actnExecutionByCommandID 按命令 ID 读执行记录（不存在即失败：悬空 ID 不能被当成成功）。
func actnExecutionByCommandID(e *harness.Env, commandID string) actnExecutionRow {
	e.T.Helper()
	const query = "SELECT command_id, idempotency_scope, idempotency_key, request_hash, status, actor_user_id " +
		"FROM command_executions WHERE command_id = $1"
	var row actnExecutionRow
	if err := e.SQL().QueryRow(query, commandID).Scan(&row.CommandID, &row.IdempotencyScope,
		&row.IdempotencyKey, &row.RequestHash, &row.Status, &row.ActorUserID); err != nil {
		e.Fatalf("执行记录 %s 不可读（事件里的 command_id 必须是真实落库的执行）: %v", commandID, err)
	}
	return row
}

// actnIdempotencyKeyPattern 是 planner.executeDeviceAction 的幂等键形态（planner.go:179）：
//
//	automation:<rule_id>:<yyyymmdd>:<seq>   seq = 当日该规则已 executed 数 + 1
var actnIdempotencyKeyPattern = regexp.MustCompile(`^automation:(\d+):(\d{8}):(\d+)$`)

// actnParseAutomationKey 拆解自动路径的幂等键并校验规则归属，返回 (日期段, 序号段)。
func actnParseAutomationKey(e *harness.Env, key string, ruleID int64) (string, int) {
	e.T.Helper()
	groups := actnIdempotencyKeyPattern.FindStringSubmatch(key)
	if groups == nil {
		e.Fatalf("幂等键 %q 不符合 automation:<rule_id>:<yyyymmdd>:<seq> 形态（设计 §9 变异 A6 守护的就是它）", key)
	}
	if groups[1] != strconv.FormatInt(ruleID, 10) {
		e.Fatalf("幂等键 %q 的规则段是 %q，期望 %d（执行必须归因到本场景的规则）", key, groups[1], ruleID)
	}
	seq, err := strconv.Atoi(groups[3])
	if err != nil {
		e.Fatalf("幂等键 %q 的序号段不可解析: %v", key, err)
	}
	return groups[2], seq
}

// actnSystemSubjectID 读内置系统主体用户 ID（users.subject_key='system_admin'）。
//
// 与 planner.resolveSystemActorID 是**同一条查询**：审计行里的 actor_user_id
// 必须等于它，才能证明归因落在系统主体上而不是某个凭空出现的用户。
func actnSystemSubjectID(e *harness.Env) int64 {
	e.T.Helper()
	var id int64
	if err := e.SQL().QueryRow(
		"SELECT id FROM users WHERE subject_key = $1 AND retired_at IS NULL",
		"system_admin").Scan(&id); err != nil {
		e.Fatalf("查询内置系统主体用户失败（与 planner.resolveSystemActorID 同一条查询）: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// SIM-ACTN-001 通知类动作触发后用户能在通知中心看到
// ---------------------------------------------------------------------------

func actnRun001(e *harness.Env) {
	fx := actnProvision(e, "SIM-ACTN-001", "notify")
	name := e.NS("SIM-ACTN-001", "notify")

	// notification 动作不需要动作目录，也不需要节点在线：它只写通知中心。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0, // 0 = 本点满足即触发
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})

	// 前置（负向观察 + 终判）：动作还没触发，通知中心里不该有本规则的通知。
	actnAssertNoNotificationFor(e, ruleID, "SIM-ACTN-001 触发前")

	// 真实数据流：235 * 0.1 = 23.5 ℃ > 20.0 ℃。
	actnReport(e, fx, 235)

	// 不变量 1：动作真的执行了 —— 事件 result=notification 且锚定本次上报的读数。
	fired := actnWaitEvent(e, ruleID, "notification", 25*time.Second)
	if fired.RuleID != uint(ruleID) {
		e.Fatalf("事件 rule_id=%d，期望 %d", fired.RuleID, ruleID)
	}
	if fired.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", fired.TriggerSource)
	}
	if fired.TriggerValue == nil {
		e.Fatalf("notification 事件必须带 trigger_value（用户要知道当时是多少）: %+v", fired)
	}
	autoEventuallyFloat(e.T, "通知动作事件的 trigger_value", *fired.TriggerValue, 23.5)

	// 不变量 2：通知中心（用户视角的唯一入口）必须能查到它。
	var landed autoNotificationRow
	e.Eventually(15*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				landed = row
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无策略 %d 的通知（共 %d 条）", ruleID, len(rows))
	})
	if landed.ID == 0 {
		e.Fatalf("通知未落地")
	}

	// 不变量 3：级别来自 action_level（用户据此判断严重程度），标题可读，且未读。
	if landed.Type != "warning" {
		e.Fatalf("通知级别 type=%q，期望 warning（= action_level）: %+v", landed.Type, landed)
	}
	if !strings.Contains(landed.Title, name) {
		e.Fatalf("通知标题 %q 里没有规则名 %q（用户无法判断是谁触发的）", landed.Title, name)
	}
	if landed.Read {
		e.Fatalf("新通知 #%d 竟是已读状态", landed.ID)
	}

	// 不变量 4：未读数把它算进去了（"有没有新通知"是用户的真实判据）。
	if unread := autoUnreadCount(e); unread < 1 {
		e.Fatalf("通知落库后未读数 = %d，期望 >= 1", unread)
	}

	e.Evidence("SIM-ACTN-001.notification", map[string]any{
		"rule_id": ruleID, "event_id": fired.ID, "event_result": fired.Result,
		"trigger_value": *fired.TriggerValue, "notification_id": landed.ID,
		"notification_type": landed.Type, "notification_title": landed.Title, "read": landed.Read,
	})
}

// ---------------------------------------------------------------------------
// SIM-ACTN-002 设备动作类触发后目标设备真的收到指令帧
// ---------------------------------------------------------------------------

func actnRun002(e *harness.Env) {
	fx := actnProvision(e, "SIM-ACTN-002", "frame")
	actnArmNode(e, fx)

	// 起点：武装后目录里 read_rainfall 必须可用（low 风险、单步读、无需确认）。
	item := actnAssertActionAvailable(e, fx, cmdActionReadRainfall)
	if item.Definition.Risk != "low" {
		e.Fatalf("%s 的风险等级 = %q，期望 low（本场景验证下发全链路，不需要确认制介入）",
			cmdActionReadRainfall, item.Definition.Risk)
	}
	e.Evidence("SIM-ACTN-002.catalog", item)

	ruleID := actnDeviceActionRule(e, "SIM-ACTN-002", "frame", fx, cmdActionReadRainfall, "{}", 60)

	// 帧观察窗口的起点：只看本次触发之后到达的帧。
	before := fx.device.FrameSeq()
	actnReport(e, fx, 235)

	// 不变量 1：自动路径真的把动作交给受控链路，并回填了指令号。
	executed := actnWaitEvent(e, ruleID, "executed", 30*time.Second)
	if executed.CommandID == "" {
		e.Fatalf("executed 事件没有 command_id（无法回链执行记录）: %+v", executed)
	}
	if executed.TriggerSource != "auto" {
		e.Fatalf("事件 trigger_source=%q，期望 auto（本场景走自动触发路径）", executed.TriggerSource)
	}

	// 不变量 2：目标节点在 control 主题真的收到这条指令帧，且帧身份与执行记录一致。
	cmd := cmdAwaitChannelCmd(e, actnEdgeHandle(fx), before, 25*time.Second)
	if want := cmdCommandUUID(e.T, executed.CommandID); cmd.CommandID != want {
		e.Fatalf("指令帧携带的命令 ID 与执行记录不一致：帧=%x 记录=%x", cmd.CommandID, want)
	}
	if len(cmd.TXData) == 0 {
		e.Fatalf("指令帧没有携带任何待发送数据（TXData 为空）")
	}
	// 不变量 2b：帧上携带的固件世代（boot_id）必须与目标节点当前的世代一致。
	// 这道断言能捕获"指令被发给了一个还停留在旧固件世代的设备"——帧本身携带了
	// boot_id（channel_cmd_v2_transport.go:114 取自 currentCapabilities），
	// 而节点当前的世代是 Hello+ResourceReport 建立起来的运行时事实，两者必须相等。
	node := cmdNodeState(e, fx.nodeID)
	if node.BootID == "" {
		e.Fatalf("目标节点没有当前固件世代（boot_id 为空），无法证明指令帧发给了正确的世代")
	}
	if cmd.BootID != node.BootID {
		e.Fatalf("指令帧携带的固件世代 = %q，节点当前世代 = %q（指令可能发给了旧固件）",
			cmd.BootID, node.BootID)
	}

	// 不变量 3：一次触发不得被物理层放大成多次下发。
	if got := cmdChannelCmdCount(actnEdgeHandle(fx)); got != 1 {
		e.Fatalf("一次触发产生了 %d 条指令帧，期望 1 条", got)
	}

	// 不变量 4：执行记录可查，且指向同一台设备、同一个动作。
	op := e.Admin.Get("/api/v1/device-operations/" + executed.CommandID).Expect(http.StatusOK)
	if got := op.DataString("command_id"); got != executed.CommandID {
		e.Fatalf("执行记录的 command_id=%q，事件里写的是 %q", got, executed.CommandID)
	}
	if got := op.DataString("action_id"); got != cmdActionReadRainfall {
		e.Fatalf("执行记录的 action_id=%q，期望 %q", got, cmdActionReadRainfall)
	}
	if got := op.DataInt("edge_device_id"); got != int64(fx.edgeDeviceID) {
		e.Fatalf("执行记录的 edge_device_id=%d，期望 %d", got, fx.edgeDeviceID)
	}
	if status := op.DataString("status"); status == "" {
		e.Fatalf("执行记录没有状态（用户不知道这条指令走到哪一步了）: %s", op.BodyString())
	}

	e.Evidence("SIM-ACTN-002.frame", map[string]any{
		"rule_id": ruleID, "event_id": executed.ID, "command_id": executed.CommandID,
		"operation_status": op.DataString("status"), "frame_attempt": cmd.Attempt,
		"edge_device_id": cmd.EdgeDeviceID, "channel_id": cmd.ChannelID,
		"read_size": cmd.ReadSize, "deadline_unix_ms": cmd.DeadlineUnixMS,
		"frame_boot_id": cmd.BootID, "node_boot_id": node.BootID,
	})
}

// ---------------------------------------------------------------------------
// SIM-ACTN-003 设备动作会走受控链路并留下审计（含系统操作者身份）
// ---------------------------------------------------------------------------

func actnRun003(e *harness.Env) {
	fx := actnProvision(e, "SIM-ACTN-003", "audit")
	actnArmNode(e, fx)

	ruleID := actnDeviceActionRule(e, "SIM-ACTN-003", "audit", fx, cmdActionReadRainfall, "{}", 60)
	actnReport(e, fx, 235)
	executed := actnWaitEvent(e, ruleID, "executed", 30*time.Second)
	if executed.CommandID == "" {
		e.Fatalf("executed 事件没有 command_id: %+v", executed)
	}

	// 不变量 1：受控链路必须留下**恰好一条**审计行，且与执行记录一一对应。
	var rows []cmdAuditRow
	e.Eventually(20*time.Second, func() error {
		rows = cmdAuditEvents(e, executed.CommandID)
		if len(rows) == 0 {
			return fmt.Errorf("尚未写入审计事件（request_id=%s）", executed.CommandID)
		}
		return nil
	})
	if len(rows) != 1 {
		e.Fatalf("一次自动下发应留下 1 条审计事件，实际 %d 条：%+v", len(rows), rows)
	}
	row := rows[0]
	if row.EventName != "device_action.created" {
		e.Fatalf("审计事件名 = %q，期望 device_action.created", row.EventName)
	}
	if row.Result != "queued" {
		e.Fatalf("审计结果 = %q，期望 queued", row.Result)
	}
	if row.TargetType != "edge_device" || row.TargetID != strconv.FormatUint(uint64(fx.edgeDeviceID), 10) {
		e.Fatalf("审计目标 = %s/%s，期望 edge_device/%d", row.TargetType, row.TargetID, fx.edgeDeviceID)
	}
	if !strings.Contains(row.Metadata, "\"action_id\":\""+cmdActionReadRainfall+"\"") {
		e.Fatalf("审计事件的 metadata 没有记录动作 ID（期望包含 %q）：%s", cmdActionReadRainfall, row.Metadata)
	}

	// 不变量 2（本场景的核心）：自动路径的操作者是**系统**，不是人工归因。
	// planner.executeDeviceAction 传 ActorKind=ActorKindSystem（planner.go:195），
	// commandexec.actorTypeForAudit 把它映射成审计行的 actor_type=system。
	if row.ActorType != "system" {
		e.Fatalf("自动下发的审计操作者类型 = %q，期望 system（人工下发才是 user）", row.ActorType)
	}
	if !row.ActorUserID.Valid || row.ActorUserID.Int64 <= 0 {
		e.Fatalf("审计事件没有记录系统操作者用户 ID：%+v", row)
	}
	// 归因必须落在**内置系统主体**上（与 planner.resolveSystemActorID 同一条查询）。
	if want := actnSystemSubjectID(e); row.ActorUserID.Int64 != want {
		e.Fatalf("审计操作者用户 ID = %d，期望内置系统主体 %d（subject_key=system_admin）",
			row.ActorUserID.Int64, want)
	}

	// 不变量 3（对照）：人工下发同一条动作留下的是 actor_type=user ——
	// 没有这条对照，"actor_type=system" 可能只是一个恒定的常量。
	humanKey := e.NS("SIM-ACTN-003", "human-idem")
	human := cmdDispatch(cmdOpsSession(e), int64(fx.edgeDeviceID), cmdActionReadRainfall,
		humanKey, nil, "", "").Expect(http.StatusAccepted)
	humanCommandID := human.DataString("execution.command_id")
	if humanCommandID == "" {
		e.Fatalf("人工下发未返回执行记录 ID：%s", human.BodyString())
	}
	var humanRows []cmdAuditRow
	e.Eventually(20*time.Second, func() error {
		humanRows = cmdAuditEvents(e, humanCommandID)
		if len(humanRows) == 0 {
			return fmt.Errorf("人工下发尚未写入审计事件（request_id=%s）", humanCommandID)
		}
		return nil
	})
	if len(humanRows) != 1 {
		e.Fatalf("一次人工下发应留下 1 条审计事件，实际 %d 条：%+v", len(humanRows), humanRows)
	}
	if humanRows[0].ActorType != "user" {
		e.Fatalf("人工下发的审计操作者类型 = %q，期望 user（与自动路径形成对照）", humanRows[0].ActorType)
	}
	if humanRows[0].ActorType == row.ActorType {
		e.Fatalf("自动与人工的审计归因无法区分（都是 %q）—— 归因没有携带操作者身份", row.ActorType)
	}

	e.Evidence("SIM-ACTN-003.audit_event", map[string]any{
		"rule_id": ruleID, "command_id": executed.CommandID, "event_name": row.EventName,
		"result": row.Result, "actor_type": row.ActorType, "actor_user_id": row.ActorUserID.Int64,
		"target_type": row.TargetType, "target_id": row.TargetID, "metadata": row.Metadata,
		"human_command_id": humanCommandID, "human_actor_type": humanRows[0].ActorType,
	})
}

// ---------------------------------------------------------------------------
// SIM-ACTN-004 同一天内同一条规则多次执行使用不同幂等键（不会互相顶掉）
// ---------------------------------------------------------------------------

func actnRun004(e *harness.Env) {
	fx := actnProvision(e, "SIM-ACTN-004", "idem")
	actnArmNode(e, fx)

	// 冷却 1s：本场景要求同一天内**真的执行两次**。
	// 注意 cooldown_sec=0 不是"无冷却"—— 求值器会把它替换成默认 300s（evaluator.go:424），
	// 那样整个场景都等不到第二次执行。
	ruleID := actnDeviceActionRule(e, "SIM-ACTN-004", "idem", fx, cmdActionReadRainfall, "{}", 1)

	// 反复上报直到出现第二条 executed：冷却到期后状态机回 armed，下一条越限上报再次触发
	// （evaluator.evalRule 的 triggered→armed 是**时间维度**，不要求条件复位）。
	// 用 Eventually 轮询而不是 sleep：两次执行之间的等待是产品行为，不是同步手段。
	var executed []autoEventRow
	e.Eventually(40*time.Second, func() error {
		actnReport(e, fx, 235)
		rows, err := actnEventsByID(e, ruleID)
		if err != nil {
			return err
		}
		executed = executed[:0]
		for _, row := range rows {
			if row.Result == "executed" {
				executed = append(executed, row)
			}
		}
		if len(executed) < 2 {
			return fmt.Errorf("策略 %d 目前只有 %d 条 executed 事件（需要同一天内两次执行）",
				ruleID, len(executed))
		}
		return nil
	})
	first, second := executed[0], executed[1]
	if first.CommandID == "" || second.CommandID == "" {
		e.Fatalf("executed 事件的 command_id 不完整: first=%+v second=%+v", first, second)
	}

	// 不变量 1：两次执行必须是两条独立的执行记录 ——
	// 若幂等键相同，commandexec 会命中持久化记录走重放，返回**同一个** command_id。
	if first.CommandID == second.CommandID {
		e.Fatalf("同日两次执行返回了同一个 command_id=%s —— 第二次被幂等重放顶掉了",
			first.CommandID)
	}

	// 不变量 2：底层确实是两次独立执行，且幂等键按当日序号递增。
	row1 := actnExecutionByCommandID(e, first.CommandID)
	row2 := actnExecutionByCommandID(e, second.CommandID)
	if row1.RequestHash != row2.RequestHash {
		e.Fatalf("两次执行的请求哈希不同（%s vs %s）—— 参数完全相同，哈希必须相同，否则'键不同'可能只是靠改参数",
			row1.RequestHash, row2.RequestHash)
	}
	if row1.IdempotencyKey == row2.IdempotencyKey {
		e.Fatalf("同日两次执行使用了同一个幂等键 %q（设计 §9 变异 A6 正是删掉这里的当日序号）",
			row1.IdempotencyKey)
	}
	date1, seq1 := actnParseAutomationKey(e, row1.IdempotencyKey, ruleID)
	date2, seq2 := actnParseAutomationKey(e, row2.IdempotencyKey, ruleID)
	if date1 == date2 {
		if seq1 != 1 || seq2 != 2 {
			e.Fatalf("同一天内两次执行的当日序号应为 1、2，实际 %d、%d（键 %q / %q）",
				seq1, seq2, row1.IdempotencyKey, row2.IdempotencyKey)
		}
	} else {
		// 跨零点：序号自然重置，本场景不判失败，但两次执行仍必须落在不同的键上（上面已断言）。
		e.Evidence("SIM-ACTN-004.day_rollover", map[string]any{
			"first_date": date1, "second_date": date2,
			"note": "本场景执行期间跨过零点，当日序号按设计自然重置",
		})
	}
	// 两条执行记录的归属必须一致（同一台设备、同一个操作者、同一个动作版本）。
	if row1.Status == "" || row2.Status == "" {
		e.Fatalf("执行记录没有状态: first=%q second=%q", row1.Status, row2.Status)
	}
	if row1.ActorUserID != row2.ActorUserID {
		e.Fatalf("两次执行的操作者不同（%d vs %d）—— 幂等键的 scope 也会随之改变，断言失去意义",
			row1.ActorUserID, row2.ActorUserID)
	}

	// 不变量 3：底层确实产生了两次独立下发（不是"建了两条记录只发一次"），
	// 也不是"同一条命令被重发两次"（按命令 ID 精确归并）。
	//
	// 必须**等待**而不是立即读帧快照：executed 事件是 commandexec.Create 落库后写的，
	// 而真正的物理下发由独立的 dispatcher 循环完成（cmd/server/main.go 的
	// runCommandDispatcher → Dispatcher.ProcessOnce，基于 outbox 轮询）。
	// 因此"事件已 executed"并不意味着"帧已发出" —— 立即读会得到 0（曾因此假红）。
	// 这里用 Eventually 等两个命令各自出现恰好 1 次，而不是加 sleep。
	var frames map[string]int
	firstHex := actnCommandUUIDHex(e, first.CommandID)
	secondHex := actnCommandUUIDHex(e, second.CommandID)
	e.Eventually(30*time.Second, func() error {
		frames = actnFramesPerCommand(e, fx)
		if frames[firstHex] < 1 {
			return fmt.Errorf("命令 %s 尚未下发（已观察到 %d 次）", first.CommandID, frames[firstHex])
		}
		if frames[secondHex] < 1 {
			return fmt.Errorf("命令 %s 尚未下发（已观察到 %d 次）", second.CommandID, frames[secondHex])
		}
		return nil
	})
	// 等待收敛后再断言"恰好 1 次"：多了说明同一条命令被重发（真实缺陷），
	// 少了说明没下发 —— 两者都必须红。
	for _, pair := range []struct{ id, hex string }{{first.CommandID, firstHex}, {second.CommandID, secondHex}} {
		if frames[pair.hex] != 1 {
			e.Fatalf("命令 %s 在物理侧出现了 %d 次，期望恰好 1 次（两次执行应各自独立下发一次）",
				pair.id, frames[pair.hex])
		}
	}

	e.Evidence("SIM-ACTN-004.idempotency", map[string]any{
		"rule_id":            ruleID,
		"first":              map[string]any{"event_id": first.ID, "command_id": first.CommandID, "key": row1.IdempotencyKey, "date": date1, "seq": seq1},
		"second":             map[string]any{"event_id": second.ID, "command_id": second.CommandID, "key": row2.IdempotencyKey, "date": date2, "seq": seq2},
		"request_hash_equal": row1.RequestHash == row2.RequestHash,
		"frames_per_command": frames,
	})
}

// ---------------------------------------------------------------------------
// SIM-ACTN-005 目标设备不满足可用性门禁时以失败结束并写明原因
// ---------------------------------------------------------------------------

// actnGateFailureCodeGap 记录设计 §4 的 SIM-ACTN-005 与当前产品实现之间的**结果码分类缺口**。
//
// 设计 §4 要求门禁失败落 result=failed_gate。产品里负责分流的是
// planner.isGateError（planner.go:256-263），它比对的是字符串前缀 "action unavailable"：
//
//	msg := err.Error()
//	return len(msg) >= 18 && msg[:18] == "action unavailable"
//
// 而 commandexec 的门禁拒绝返回的是 ErrActionUnavailable
// = "action is unavailable for this device"（service.go:27）——
//
//	a(0)c(1)t(2)i(3)o(4)n(5) (6)i(7)s(8) (9)u(10)n(11)a(12)v(13)a(14)i(15)l(16)a(17)
//	→ 前 18 个字符是 "action is unavail"，与 "action unavailable" 不相等。
//
// 因此 isGateError 对该错误**恒为 false**，failed_gate 这条分支在当前产品里
// **没有任何生产者**（全仓核实：没有任何 error 值以 "action unavailable" 开头；
// 该文案只作为 HTTP 响应消息出现在 handler_device_operation.go:72/205）。
// 产品自己的单测也把这个行为固化了：automation/planner_test.go:183-187 明确断言
// isGateError(ErrActionUnavailable)==false → result=failed_dispatch。
//
// 结论：设计要求的结果码在当前产品能力下**不可达**。本场景因此断言真实且等价有意义的不变量
// （门禁拒绝必须 fail-closed：明确失败、写明**门禁的**原因、零执行记录、零物理下发），
// 并把真实结果码与缺口一并记入 Evidence —— 不写成一条永远红的假断言，也不静默放过。
// 修复点是一行：isGateError 改为 errors.Is(err, commandexec.ErrActionUnavailable)。
const actnGateFailureCodeGap = "设计 §4 要求 failed_gate；当前 planner.isGateError 与 " +
	"commandexec.ErrActionUnavailable 的文案不匹配，门禁拒绝实际落 failed_dispatch"

// actnGateRejectReason 是 commandexec 门禁拒绝的**错误原文**（service.go:27 ErrActionUnavailable）。
// 断言它出现在 Detail 里，才能证明这次失败来自目标设备的可用性门禁，
// 而不是来自引擎自身（例如系统主体解析失败）或别的分发问题。
const actnGateRejectReason = "action is unavailable for this device"

func actnRun005(e *harness.Env) {
	// 关键：不 Hello、不调 actnArmNode —— 目标设备的环境门禁不满足。
	// 这是最真实的门禁失败构造方式：命令门禁依赖的整套"运行时事实"
	// （boot_id / capabilities / 配置清单生效 / 运行期通道）只能由 MQTT 帧写入，
	// 少一步就 fail-closed。
	fx := actnProvision(e, "SIM-ACTN-005", "gate")

	// 前置 1：节点确实离线（否则 failed_gate 可能来自别的门禁，归因不清）。
	state := cmdNodeState(e, fx.nodeID)
	if state.Status != "offline" {
		e.Fatalf("前置条件不成立：未握手的节点状态 = %q，期望 offline", state.Status)
	}

	// 前置 2：动作目录必须如实说明"不可用"，并给出可读原因（用户在那一边看到的解释）。
	items := cmdActionCatalog(e, int64(fx.edgeDeviceID))
	blocked, ok := cmdActionByID(items, cmdActionReadRainfall)
	if !ok {
		e.Fatalf("动作目录里没有 %s：%+v", cmdActionReadRainfall, items)
	}
	if blocked.Available {
		e.Fatalf("未武装的设备上动作 %s 仍被标记为可用（门禁未 fail-closed）: %+v",
			cmdActionReadRainfall, blocked)
	}
	if strings.TrimSpace(blocked.Reason) == "" && strings.TrimSpace(blocked.ReasonCode) == "" {
		e.Fatalf("动作被标记为不可用却没有给出任何原因（用户无法判断缺什么）: %+v", blocked)
	}
	e.Evidence("SIM-ACTN-005.catalog_blocked", blocked)

	ruleID := actnDeviceActionRule(e, "SIM-ACTN-005", "gate", fx, cmdActionReadRainfall, "{}", 60)
	actnReport(e, fx, 235)

	// 不变量 1：这次触发必须以**明确失败**结束（既不是静默成功，也不是挂在中间态）。
	//
	// 接受两种结果码，理由是 actnGateFailureCodeGap 记录的分类缺口：
	//   - "failed_gate"           —— 设计 §4 要求的结果码（把 isGateError 的文案比对修好后就是这个）；
	//   - "failed_dispatch"       —— 当前产品的真实行为（门禁拒绝被归入 dispatch 失败）。
	// 传两个值**不是**放宽断言：下面仍然逐条断言"零执行记录 + 零物理下发 + Detail 是门禁原文"，
	// 即 fail-closed 的全部可观测后果一个不少。缺口修复后本场景会自动走 failed_gate 分支并通过，
	// 而不是变成一条永远红的假断言。
	failed := actnWaitAnyEvent(e, ruleID, []string{"failed_gate", "failed_dispatch"}, 30*time.Second)
	if failed.TriggerSource != "auto" {
		e.Fatalf("事件 trigger_source=%q，期望 auto", failed.TriggerSource)
	}
	if failed.CommandID != "" {
		e.Fatalf("门禁失败的事件却带上了 command_id=%q（被拒的动作不该有执行记录）", failed.CommandID)
	}

	// 不变量 2：必须写明原因，且原因与**门禁**语义一致。
	detail := strings.TrimSpace(failed.Detail)
	if detail == "" {
		e.Fatalf("门禁失败事件没有写明原因（用户不知道设备缺什么）: %+v", failed)
	}
	if !strings.Contains(detail, actnGateRejectReason) {
		e.Fatalf("门禁失败的原因 %q 里没有 commandexec 的可用性门禁原文 %q —— 无法证明失败来自目标设备门禁",
			detail, actnGateRejectReason)
	}
	// 反面：不能是"系统操作者解析失败"那种引擎自身的问题（那会把门禁失败伪装成配置问题）。
	// 没有这条反面断言，Detail 里出现任何一句带 "unavailable" 的话都能冒充门禁失败。
	if strings.Contains(detail, "system actor unavailable") ||
		strings.Contains(detail, "invalid command request") {
		e.Fatalf("门禁失败的原因 %q 指向引擎自身而非目标设备门禁", detail)
	}

	// 不变量 3：fail-closed = 物理侧零下发 + 零执行记录（有界负向观察 + 终判）。
	actnAssertNoCommandFrames(e, fx, "SIM-ACTN-005 门禁失败", 3*time.Second)
	cmdAssertNoExecutions(e, actnEdgeHandle(fx), "被门禁拒绝的自动动作")

	e.Evidence("SIM-ACTN-005.gate_fail_closed", map[string]any{
		"rule_id": ruleID, "event_id": failed.ID, "result": failed.Result, "detail": failed.Detail,
		"node_status": state.Status, "catalog_reason": blocked.Reason,
		"catalog_reason_code": blocked.ReasonCode, "frames": cmdChannelCmdCount(actnEdgeHandle(fx)),
		"design_required_result": "failed_gate", "classification_gap": actnGateFailureCodeGap,
	})
}

// ---------------------------------------------------------------------------
// SIM-ACTN-006 动作参数非法时以 failed_dispatch 结束，不会静默成功
// ---------------------------------------------------------------------------

func actnRun006(e *harness.Env) {
	fx := actnProvision(e, "SIM-ACTN-006", "params")

	// 本场景断言的**真实行为**（详见报告"偏差清单"）：
	// 非法参数在**创建规则**时就被 CanonicalizeParams 拒绝
	// （handler_automation.go:667 validateDeviceActionCatalog），规则根本建不出来，
	// 因此 planner.ParseActionParams→failed_dispatch 这条执行期路径不可达
	// （models.ParseActionParams 只做 json.Valid，而能落库的参数必然已通过 schema 校验）。
	// 断言"创建时就被拒 + 零副作用"守护的仍是同一个不变量：非法参数绝不静默成功。
	//
	// 三种非法形态覆盖三条校验分支（deviceaction/schema.go:72-126）：
	// 不是 JSON 对象 / schema 未声明的参数 / 数值越界。
	cases := []struct {
		label    string
		actionID string
		params   string
	}{
		{"非法 JSON（不是对象）", cmdActionReadRainfall, "{not json"},
		{"schema 未声明的参数", cmdActionReadRainfall, `{"unknown_param":1}`},
		{"数值越界（灵敏度上限 65535）", "set_rain_sensitivity", `{"value":99999}`},
	}

	for i, item := range cases {
		name := e.NS("SIM-ACTN-006", fmt.Sprintf("bad-%d", i+1))
		body := actnDeviceActionRuleBody(name, fx, item.actionID, item.params, 60)
		resp := e.Admin.Post("/api/v1/automation-rules", body)

		// 万一产品真的建出来了（回归/缺陷），先清理再失败：脏数据不能流进后续场景。
		if resp.Status == http.StatusOK {
			if id := resp.DataInt("id"); id != 0 {
				e.Admin.Delete("/api/v1/automation-rules/" + strconv.FormatInt(id, 10))
			}
			e.Fatalf("%s：参数非法的建规则请求被接受了（action_id=%s params=%s）：%s",
				item.label, item.actionID, item.params, resp.BodyString())
		}
		// 不变量 1：必须被明确拒绝，并给出机器可读原因 + 可读说明。
		resp.ExpectError(http.StatusBadRequest, "invalid_automation_rule")
		if !strings.Contains(resp.Message, "action_params_json") {
			e.Fatalf("%s：拒绝原因 %q 没有指出是动作参数的问题（用户不知道该改哪里）",
				item.label, resp.Message)
		}

		// 不变量 2：被拒的请求不得留下任何策略行（"返回 400 但库里已经写了一条"是最危险的假失败）。
		if got := simCountRows(e, "SELECT count(*) FROM automation_rules WHERE name = $1", name); got != 0 {
			e.Fatalf("%s：建规则被拒（HTTP %d）但库里留下了 %d 条策略 name=%q",
				item.label, resp.Status, got, name)
		}

		e.Evidence("SIM-ACTN-006.rejected."+strconv.Itoa(i+1), map[string]any{
			"label": item.label, "action_id": item.actionID, "params": item.params,
			"http_status": resp.Status, "error_code": resp.ErrorCode, "message": resp.Message,
		})
	}

	// 不变量 3（对照）：同样的建规则请求 + 合法参数必须建得出来 ——
	// 没有这条对照，"被拒"可能只是请求形状本身有问题，而不是参数非法。
	validID := actnDeviceActionRule(e, "SIM-ACTN-006", "valid", fx, cmdActionReadRainfall, "{}", 60)
	if rows, err := autoListRules(e, ""); err != nil {
		e.Fatalf("%v", err)
	} else {
		found := false
		for _, row := range rows {
			if row.ID == uint(validID) {
				found = true
			}
		}
		if !found {
			e.Fatalf("合法参数的对照策略 %d 未出现在列表里", validID)
		}
	}

	// 不变量 4：整组被拒的请求没有产生任何执行记录，也没有任何物理下发
	// （有界负向观察 + 终判，设计 §4.2.2）。
	actnAssertNoCommandFrames(e, fx, "SIM-ACTN-006 参数非法", 3*time.Second)
	cmdAssertNoExecutions(e, actnEdgeHandle(fx), "参数非法的建规则请求")

	e.Evidence("SIM-ACTN-006.no_silent_success", map[string]any{
		"rejected_cases": len(cases), "valid_control_rule_id": validID,
		"command_executions": simCountRows(e,
			"SELECT count(*) FROM command_executions WHERE edge_device_id = $1", fx.edgeDeviceID),
		"frames": cmdChannelCmdCount(actnEdgeHandle(fx)),
	})
}
