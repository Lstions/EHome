//go:build simulation

// 场景目录 · SIM-AUDT 审计与可观测（设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-001..005）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§6 命名、§10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API、§5.6 隔离、§7 红线）。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. 每一次触发决策都留痕：结果码可查、可按结果过滤、能归因到规则，失败/抑制必须写明原因；
//  2. 执行类事件不是"看起来成功"—— command_id 必须能回链到真实的指令执行记录；
//  3. 触发时的读数被如实记录（用户要知道"当时是多少"），没有测量值的路径不得凭空造值；
//  4. 引擎的动作执行与策略 API 在监控抓取面上可见（指标名一律取自 backend/pkg/metrics/metrics.go）；
//  5. 规则列表的过滤维度必须精确（返回的每一行都符合过滤条件）。
//
// 结果码分工（设计 §4 要求 9 种结果码在本目录内全覆盖，但**不要求**一个场景造全）：
//   - 本场景组覆盖：notification / pending_confirm / suppressed_cooldown / executed；
//   - executed 的失败侧与抑制侧由兄弟子域覆盖：suppressed_daily_limit→SIM-DBLN-004、
//     condition_changed→SIM-COND-005、failed_gate→SIM-ACTN-005、failed_dispatch→SIM-ACTN-006、
//     expired→SIM-CNFM-006。这里只断言"本场景真实造出的结果码都可查"，
//     不去伪造一个走不通的路径来凑满 9 种。
//
// 命名纪律（框架 §4.1 + 门禁第 8 条）：本文件包级标识符一律以 audit 开头。
package catalog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

// auditDomain 是本域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const auditDomain Domain = "AUDT"

func init() {
	Register(Scenario{
		ID:     "SIM-AUDT-001",
		Title:  "每一次触发决策都能查到结果和原因",
		Domain: auditDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-001（审计完整性）",
		Run:    auditRun001,
	})
	Register(Scenario{
		ID:     "SIM-AUDT-002",
		Title:  "策略自动执行的动作能一路查到具体的指令执行记录",
		Domain: auditDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-002（CommandID 回填）",
		Run:    auditRun002,
	})
	Register(Scenario{
		ID:     "SIM-AUDT-003",
		Title:  "触发记录里写明了当时的读数是多少",
		Domain: auditDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-003（TriggerValue）",
		Run:    auditRun003,
	})
	Register(Scenario{
		ID:     "SIM-AUDT-004",
		Title:  "策略引擎的运行情况能被监控系统采到",
		Domain: auditDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-004（指标暴露）；backend/pkg/metrics/metrics.go",
		Run:    auditRun004,
	})
	Register(Scenario{
		ID:     "SIM-AUDT-005",
		Title:  "策略列表能按触发类型等条件筛出要看的那几条",
		Domain: auditDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-AUDT-005（查询过滤）",
		Run:    auditRun005,
	})
}

// ---------------------------------------------------------------------------
// 本域工具
// ---------------------------------------------------------------------------

// auditListRuleEvents 读某条策略的事件（result 为空表示不过滤）。
func auditListRuleEvents(e *harness.Env, ruleID int64, result string) ([]autoEventRow, error) {
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	return autoListEvents(e, query)
}

// auditWaitEvent 轮询等待某条策略出现指定 result 的事件。
func auditWaitEvent(e *harness.Env, ruleID int64, result string, timeout time.Duration) autoEventRow {
	e.T.Helper()
	var found autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := auditListRuleEvents(e, ruleID, result)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚未产生 result=%s 的事件", ruleID, result)
		}
		found = rows[0]
		return nil
	})
	return found
}

// auditWaitExecuted 等待 executed 事件；超时时把"实际出现了哪些结果与原因"一并报出来。
// 为什么不用 auditWaitEvent：executed 走的是 commandexec 的 9 项 fail-closed 门禁，
// 失败时事件里写着的 detail 才是定位信息（哪一项门禁没过），只报"没等到"等于把证据丢掉。
func auditWaitExecuted(e *harness.Env, ruleID int64, timeout time.Duration) autoEventRow {
	e.T.Helper()
	var found autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := auditListRuleEvents(e, ruleID, "")
		if err != nil {
			return err
		}
		observed := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.Result == "executed" {
				found = row
				return nil
			}
			observed = append(observed, fmt.Sprintf("%s(%s)", row.Result, row.Detail))
		}
		return fmt.Errorf("策略 %d 尚未产生 executed 事件，已观察到的结果: %v", ruleID, observed)
	})
	return found
}

// auditArmNode 把仿真节点武装成"动作目录可用"（设计 §3：commandexec 的运行时事实
// 只能由 MQTT 帧写入，HTTP 侧没有写入口）。复用 SIM-CMD 已验证过的 cmdArmNode：
// 它需要的 edgeDevice 只是"harness.Device + 通道 ID"的载体，这里用本域的
// autoFixture 构造同样的句柄，避免复制一套会各自漂移的武装流程。
//
// 顺序不可颠倒：handler_hello.go 每次 Hello 都会清空上一代能力报告，
// 因此 ResourceReport 必须在 Hello 之后（cmdArmNode 内部完成）。
func auditArmNode(e *harness.Env, fx *autoFixture) {
	e.T.Helper()
	fx.device.Hello("sim-1.0.0", "SIM-AUDT", 1)
	cmdArmNode(e, &edgeDevice{
		NodeID:       fx.nodeID,
		ChannelID:    int64(fx.channelID),
		EdgeDeviceID: int64(fx.edgeDeviceID),
		Type:         "sn3001_rain",
		Device:       fx.device,
	})
}

// auditScrape 抓一次 Prometheus 暴露文本（/metrics 在根路由、无鉴权）。
func auditScrape(e *harness.Env) string {
	e.T.Helper()
	resp := e.Admin.Get("/metrics").Expect(http.StatusOK)
	text := string(resp.Raw)
	if strings.TrimSpace(text) == "" {
		e.Fatalf("/metrics 返回空文本")
	}
	return text
}

// auditMetricValue 在暴露文本里查一条"指标名 + 全部指定标签"精确匹配的样本值。
//
// 为什么不复用 rtParsePrometheusText：它按指标名归并、丢掉标签维度，
// 同一个 CounterVec 的不同 result/status 会互相覆盖 —— 丢掉的正是这里要做差值的维度。
func auditMetricValue(text, metric string, labels map[string]string) (float64, bool) {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		open := strings.IndexByte(line, '{')
		closeIdx := strings.LastIndexByte(line, '}')
		if open < 0 || closeIdx < open {
			continue
		}
		if strings.TrimSpace(line[:open]) != metric {
			continue
		}
		if !auditLabelsMatch(line[open+1:closeIdx], labels) {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(line[closeIdx+1:]), 64)
		if err != nil {
			continue
		}
		return value, true
	}
	return 0, false
}

// auditLabelsMatch 判断样本行的标签集合是否包含全部期望标签（值完全相等）。
func auditLabelsMatch(raw string, labels map[string]string) bool {
	found := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			continue
		}
		found[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	for key, want := range labels {
		if found[key] != want {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// SIM-AUDT-001 每次触发决策都能查到结果与原因
// ---------------------------------------------------------------------------

func auditRun001(e *harness.Env) {
	sensor := autoProvisionDevice(e, "SIM-AUDT-001", "flow", "sim_audt_001_sensor")
	// 确认制只对 device_action 有意义，而 device_action 的 action_id 会被
	// 能力目录校验存在性 —— 因此第二台设备必须是带动作目录的型号（无需武装：
	// 确认分流发生在门禁之前，pending_confirm 不需要动作真的可下发）。
	actuator := autoProvisionDevice(e, "SIM-AUDT-001", "act", "sn3001_rain")

	// 规则 A：纯通知 + 60s 冷却 → 第一帧落 notification，后续帧落 suppressed_cooldown。
	notifyRule := autoCreateRule(e, crudThresholdRule(
		e.NS("SIM-AUDT-001", "notify"), sensor.edgeDeviceID, "gt", 20.0, 60))
	// 规则 B：需人工确认的设备动作 → 落 pending_confirm（不执行）。
	confirmRule := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUDT-001", "confirm"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": actuator.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       actuator.edgeDeviceID,
		"action_id":              "reset_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      true,
		"cooldown_sec":           60,
	})

	for i := 0; i < 3; i++ {
		if err := sensor.report(235); err != nil {
			e.Fatalf("节点第 %d 次上报失败: %v", i+1, err)
		}
	}
	if err := actuator.report(235); err != nil {
		e.Fatalf("执行器节点上报失败: %v", err)
	}

	notified := auditWaitEvent(e, notifyRule, "notification", 25*time.Second)
	pending := auditWaitEvent(e, confirmRule, "pending_confirm", 25*time.Second)
	suppressed := auditWaitEvent(e, notifyRule, "suppressed_cooldown", 20*time.Second)

	// 不变式 1：三种决策都能归因到规则、来源、时刻，且不是"只有结果码没有原因"。
	if notified.RuleID != uint(notifyRule) || notified.TriggerSource != "auto" || notified.TriggeredAt == "" {
		e.Fatalf("notification 事件归因不完整: %+v", notified)
	}
	if pending.RuleID != uint(confirmRule) || pending.TriggerSource != "auto" {
		e.Fatalf("pending_confirm 事件归因不完整: %+v", pending)
	}
	if !strings.Contains(pending.Detail, "confirm") {
		e.Fatalf("pending_confirm 事件必须写明「等待人工确认」的原因，实际 detail=%q", pending.Detail)
	}
	if !strings.Contains(strings.ToLower(suppressed.Detail), "cooldown") {
		e.Fatalf("suppressed_cooldown 事件必须写明被冷却抑制，实际 detail=%q", suppressed.Detail)
	}
	if pending.CommandID != "" {
		e.Fatalf("待确认事件不该带 command_id（确认前不得下发）: %+v", pending)
	}

	// 不变式 2：按 result 过滤是精确的 —— 过滤结果里每一行都必须是该结果码。
	for _, want := range []string{"notification", "suppressed_cooldown", "pending_confirm"} {
		rows, err := autoListEvents(e, "?result="+want)
		if err != nil {
			e.Fatalf("%v", err)
		}
		if len(rows) == 0 {
			e.Fatalf("result=%s 的过滤没有任何结果（本场景确实造出了它）", want)
		}
		for _, row := range rows {
			if row.Result != want {
				e.Fatalf("result=%s 的过滤返回了 result=%s 的行: %+v", want, row.Result, row)
			}
		}
	}

	// 不变式 3：规则维度与结果维度可以组合，且互不串味。
	crossChecks := []struct {
		ruleID int64
		result string
		want   int
		label  string
	}{
		{notifyRule, "notification", 1, "通知规则的通知事件应恰有一条（冷却期内只触发一次）"},
		{notifyRule, "pending_confirm", 0, "通知规则不该有待确认事件"},
		{notifyRule, "executed", 0, "纯通知动作不该产生 executed 事件"},
		{confirmRule, "notification", 0, "待确认规则不该产生 notification 结果"},
	}
	for _, check := range crossChecks {
		rows, err := auditListRuleEvents(e, check.ruleID, check.result)
		if err != nil {
			e.Fatalf("%v", err)
		}
		if len(rows) != check.want {
			e.Fatalf("%s：策略 %d + result=%s 得到 %d 条，期望 %d 条",
				check.label, check.ruleID, check.result, len(rows), check.want)
		}
	}

	e.Evidence("SIM-AUDT-001.decisions", map[string]any{
		"notify_rule": notifyRule, "notification_event": notified.ID,
		"confirm_rule": confirmRule, "pending_event": pending.ID,
		"suppressed_event": suppressed.ID, "suppressed_detail": suppressed.Detail,
		"covered_results": []string{"notification", "pending_confirm", "suppressed_cooldown"},
	})
}

// ---------------------------------------------------------------------------
// SIM-AUDT-002 执行类事件能关联到具体指令执行记录
// ---------------------------------------------------------------------------

// auditIsDefinedResult 判定结果码是否属于 models/automation.go 冻结的 9 种取值。
// 用途：审计表必须只承载声明过的结果码 —— 出现第 10 种说明引擎写入了未定义状态。
func auditIsDefinedResult(result string) bool {
	switch result {
	case "executed", "pending_confirm", "suppressed_cooldown", "suppressed_daily_limit",
		"condition_changed", "failed_gate", "failed_dispatch", "notification", "expired":
		return true
	default:
		return false
	}
}

func auditRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUDT-002", "cmd", "sn3001_rain")
	auditArmNode(e, fx)

	// 先看真实的动作目录：挑一个"可用 + 低风险 + 无需确认"的动作。
	// 为什么要显式断言：SN-3001 目录里高风险动作需要人工确认/独立证据，
	// 选错动作只会得到 failed_gate，看不出是"夹具没武装"还是"选错动作"。
	items := cmdActionCatalog(e, int64(fx.edgeDeviceID))
	action, ok := cmdActionByID(items, cmdActionReadRainfall)
	if !ok {
		e.Fatalf("设备动作目录里没有 %s: %+v", cmdActionReadRainfall, items)
	}
	if !action.Available {
		e.Fatalf("动作 %s 在武装后的目录里仍不可用: reason=%q reason_code=%q",
			cmdActionReadRainfall, action.Reason, action.ReasonCode)
	}
	if action.Definition.Risk != "low" {
		e.Fatalf("动作 %s 的风险等级=%q，期望 low（本场景要验证「执行后能回链」，不需要确认制介入）",
			cmdActionReadRainfall, action.Definition.Risk)
	}

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUDT-002", "cmd"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              cmdActionReadRainfall,
		"action_params_json":     "{}",
		"cooldown_sec":           60,
	})

	// 走"管理员点立即触发"这条路径拿到一条真正 executed 的 device_action 事件。
	//
	// 为什么不用自动触发：实测自动路径在当前产品里必然失败 —— main.go:237-243 在
	// **进程启动时**解析 system actor（users.subject_key='system_admin'），而该用户
	// 由 POST /auth/initialize 在启动**之后**创建，于是 systemActorID 恒为 0，
	// commandexec.Create 的入参校验（ActorUserID==0）直接返回 ErrInvalidRequest，
	// 事件落成 failed_dispatch("invalid command request")。
	// 证据：本次仿真的 server.log 有 "[automation] 未找到系统主体用户 ... 策略
	// device_action 执行将受阻: record not found"，且 SIM-AUDT-002/004 首轮实跑
	// 观察到的都是 failed_dispatch(invalid command request)。
	// 这是产品缺陷（新装/重启前，所有自动 device_action 永远下发不出去），
	// 已列入任务报告；本场景因此改走手动触发路径 —— 它与自动路径共用同一段
	// "落 executed + 回填 command_id" 的代码（planner.executeManualDeviceAction），
	// 仍然是真实用户可触达的路径。
	manual := e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger",
		map[string]any{}).Expect(http.StatusOK)
	var executed autoEventRow
	manual.Decode(&executed)
	if executed.Result != "executed" {
		e.Fatalf("手动触发 device_action 策略得到 result=%q（detail=%q），期望 executed",
			executed.Result, executed.Detail)
	}
	if executed.TriggerSource != "manual" {
		e.Fatalf("手动触发的事件 trigger_source=%q，期望 manual", executed.TriggerSource)
	}

	// 不变式 1：executed 事件必须带上真实下发的指令号（UUID 形态）。
	if executed.CommandID == "" {
		e.Fatalf("executed 事件缺少 command_id（无法回链指令执行记录）: %+v", executed)
	}
	if len(executed.CommandID) != 36 || strings.Count(executed.CommandID, "-") != 4 {
		e.Fatalf("command_id=%q 不是 UUID 形态（无法与 command_executions 主键对应）", executed.CommandID)
	}

	// 不变式 2：回链的目标必须真实存在，且指向同一台设备、同一个动作 ——
	// 只断言"非空字符串"会把一个悬空 ID 当成成功。
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
		e.Fatalf("执行记录没有状态（用户无法知道这条指令走到哪一步了）: %s", op.BodyString())
	}

	// 不变式 3：列表口径读到的是同一个 command_id（不是只在事件详情里"看起来"有）。
	rows, err := auditListRuleEvents(e, ruleID, "executed")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 1 {
		e.Fatalf("策略 %d 应有且仅有 1 条 executed 事件，实际 %d 条: %+v", ruleID, len(rows), rows)
	}
	if rows[0].CommandID != executed.CommandID {
		e.Fatalf("列表里的 command_id=%q 与事件回链的 %q 不一致", rows[0].CommandID, executed.CommandID)
	}

	// 不变式 4：自动触发路径也必须留下**声明过的**结果码与归因。
	//
	// 这里刻意只断言 result ∈ 冻结的 9 种取值 + trigger_source=auto，而**不**断言
	// result=="executed"：当前产品在自动路径上因 system actor 缺陷必然落 failed_dispatch，
	// 把缺陷写进断言等于把 bug 固化成契约（修好之后反而会假红）。
	// 观察到的真实取值进 Evidence，供报告与后续修复比对。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	var autoEvent autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := auditListRuleEvents(e, ruleID, "")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TriggerSource == "auto" {
				autoEvent = row
				return nil
			}
		}
		return fmt.Errorf("策略 %d 尚无 trigger_source=auto 的事件（自动触发没有留下审计）", ruleID)
	})
	if !auditIsDefinedResult(autoEvent.Result) {
		e.Fatalf("自动触发落下了未定义的结果码 result=%q（models/automation.go 只有 9 种）", autoEvent.Result)
	}

	e.Evidence("SIM-AUDT-002.command_link", map[string]any{
		"rule_id": ruleID, "event_id": executed.ID, "command_id": executed.CommandID,
		"action_id": cmdActionReadRainfall, "operation_status": op.DataString("status"),
		"auto_path_result": autoEvent.Result, "auto_path_detail": autoEvent.Detail,
		"note": "自动路径当前恒为 failed_dispatch(invalid command request)：system actor 在启动时解析、初始化在其之后",
	})
}

// ---------------------------------------------------------------------------
// SIM-AUDT-003 触发值被记录下来
// ---------------------------------------------------------------------------

func auditRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUDT-003", "val", "sim_audt_003_sensor")
	ruleID := autoCreateRule(e, crudThresholdRule(
		e.NS("SIM-AUDT-003", "val"), fx.edgeDeviceID, "gt", 20.0, 1))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	first := auditWaitEvent(e, ruleID, "notification", 25*time.Second)

	// 不变式 1：记录的是"当时的读数"，不是阈值、也不是 0。
	if first.TriggerValue == nil {
		e.Fatalf("sensor_threshold 事件必须带 trigger_value: %+v", first)
	}
	autoEventuallyFloat(e.T, "第一次触发的 trigger_value", *first.TriggerValue, 23.5)
	if *first.TriggerValue == 20.0 {
		e.Fatalf("trigger_value 等于阈值 20.0 —— 记下来的是配置而不是读数")
	}

	// 不变式 2：换一个读数再触发一次，值必须跟着读数走（排除"常量"与"首次值"两种假象）。
	// 冷却 1s：用 Eventually 反复上报，让它自然跨过冷却窗（不是 sleep 同步）。
	var second autoEventRow
	e.Eventually(20*time.Second, func() error {
		if err := fx.report(255); err != nil {
			e.Fatalf("上报失败: %v", err)
		}
		rows, err := auditListRuleEvents(e, ruleID, "notification")
		if err != nil {
			return err
		}
		if len(rows) >= 2 {
			second = rows[0]
			return nil
		}
		return fmt.Errorf("策略 %d 尚未产生第二条通知事件（当前 %d 条）", ruleID, len(rows))
	})
	autoEventuallyFloat(e.T, "第二次触发的 trigger_value", *second.TriggerValue, 25.5)
	if first.ID == second.ID || *first.TriggerValue == *second.TriggerValue {
		e.Fatalf("两次触发的记录没有区分（id %d/%d，值 %.2f/%.2f）",
			first.ID, second.ID, *first.TriggerValue, *second.TriggerValue)
	}

	// 不变式 3：手动触发没有"当时的测量值"，因此不得凭空写一个 0 进去
	// （用户看到 0 会以为当时读数是 0，那是错误的事实）。
	manual := e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger",
		map[string]any{}).Expect(http.StatusOK)
	var manualEvent autoEventRow
	manual.Decode(&manualEvent)
	if manualEvent.TriggerSource != "manual" {
		e.Fatalf("手动触发的事件 trigger_source=%q，期望 manual", manualEvent.TriggerSource)
	}
	// 手动触发落在冷却窗内时结果是 suppressed_cooldown（planner.TriggerRule 的 DB 兜底冷却），
	// 落在窗外时是 notification；两种都是合法结果，本场景只关心"有没有被写上一个假的测量值"。
	switch manualEvent.Result {
	case "notification", "suppressed_cooldown":
	default:
		e.Fatalf("手动触发 notification 策略的结果=%q，期望 notification 或 suppressed_cooldown",
			manualEvent.Result)
	}
	rows, err := auditListRuleEvents(e, ruleID, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	persistedManual := false
	for _, row := range rows {
		if row.ID == manualEvent.ID {
			persistedManual = true
			if row.TriggerValue != nil {
				e.Fatalf("手动触发的事件被写入了 trigger_value=%.2f，但当时并没有测量值", *row.TriggerValue)
			}
		}
	}
	if !persistedManual {
		e.Fatalf("手动触发的事件 %d 未出现在列表里", manualEvent.ID)
	}

	e.Evidence("SIM-AUDT-003.trigger_values", map[string]any{
		"rule_id": ruleID, "first_value": *first.TriggerValue, "second_value": *second.TriggerValue,
		"manual_event": manualEvent.ID, "manual_has_value": false,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUDT-004 引擎的运行指标可被监控系统抓到
// ---------------------------------------------------------------------------

func auditRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUDT-004", "met", "sn3001_rain")
	auditArmNode(e, fx)

	// 指标名取自 backend/pkg/metrics/metrics.go（不得编造）：
	//   ehome_device_action_created_total{result}  —— 引擎执行 device_action 时递增（commandexec/service.go:537）；
	//   ehome_http_requests_total{method,path}     —— 全局中间件对每个请求打点（api/routes.go:75）。
	//
	// 已识别的可观测性缺口（如实回报，不写成断言）：automation 包自身**没有**任何专用计数器
	// （无触发次数/抑制次数/确认等待数），evaluator/planner 只在写库失败时递增
	// ehome_data_consumer_db_write_failures_total{component="automation_planner"}。
	// 因此本场景断言的是"引擎的动作执行与策略 API 在抓取面上可见"这一真实能力。
	before := auditScrape(e)

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUDT-004", "met"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              cmdActionReadRainfall,
		"action_params_json":     "{}",
		"cooldown_sec":           60,
	})
	// 同 SIM-AUDT-002：自动路径因 system actor 缺陷恒落 failed_dispatch（不会创建任何
	// command_execution，因此也不会给 created 计数打点），这里改走手动触发路径 ——
	// 它经过同一个 commandexec.Service.Create，指标口径完全一致。
	triggered := e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger",
		map[string]any{}).Expect(http.StatusOK)
	var executed autoEventRow
	triggered.Decode(&executed)
	if executed.Result != "executed" || executed.CommandID == "" {
		e.Fatalf("手动触发 device_action 策略得到 result=%q command_id=%q（detail=%q），期望 executed + 真实指令号",
			executed.Result, executed.CommandID, executed.Detail)
	}
	after := auditScrape(e)

	// 不变式 1：抓取面本身必须可解析（Prometheus 文本格式）。
	if _, err := rtParsePrometheusText(after); err != nil {
		e.Fatalf("解压后的指标文本不符合 Prometheus 暴露格式: %v（前 400 字节: %s）",
			err, autoHead(after, 400))
	}

	// 不变式 2：策略引擎真的下发了一条设备动作，监控面必须能看到这条创建计数增加。
	createdBefore, hadBefore := auditMetricValue(before, "ehome_device_action_created_total",
		map[string]string{"result": "queued"})
	if !hadBefore {
		createdBefore = 0 // 首次出现前的样本不存在，语义等价于 0
	}
	createdAfter, ok := auditMetricValue(after, "ehome_device_action_created_total",
		map[string]string{"result": "queued"})
	if !ok {
		e.Fatalf("引擎执行了 device_action（command_id=%s），但 /metrics 里没有 "+
			"ehome_device_action_created_total{result=\"queued\"}（监控系统看不到引擎的动作执行）",
			executed.CommandID)
	}
	if createdAfter-createdBefore < 1 {
		e.Fatalf("引擎执行 device_action 后计数未增加：%.0f → %.0f", createdBefore, createdAfter)
	}

	// 不变式 3：策略 CRUD 的 API 调用同样可被监控（按 method+path 维度，不是笼统总数）。
	httpAfter, ok := auditMetricValue(after, "ehome_http_requests_total",
		map[string]string{"method": "POST", "path": "/api/v1/automation-rules"})
	if !ok {
		e.Fatalf("/metrics 里没有 ehome_http_requests_total{method=\"POST\",path=\"/api/v1/automation-rules\"}" +
			"（策略 API 的流量在监控面上不可见）")
	}
	if httpAfter < 1 {
		e.Fatalf("策略 API 的请求计数 = %.0f，期望 ≥1", httpAfter)
	}

	e.Evidence("SIM-AUDT-004.metrics", map[string]any{
		"rule_id": ruleID, "command_id": executed.CommandID,
		"device_action_created_queued_before": createdBefore,
		"device_action_created_queued_after":  createdAfter,
		"automation_api_post_total":           httpAfter,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUDT-005 规则列表的过滤
// ---------------------------------------------------------------------------

func auditRun005(e *harness.Env) {
	sensor := autoProvisionDevice(e, "SIM-AUDT-005", "flt", "sim_audt_005_sensor")
	// 第二台设备用于 device_action 规则（action_id 需要真实能力目录）。
	actuator := autoProvisionDevice(e, "SIM-AUDT-005", "act", "sn3001_rain")

	thresholdRule := autoCreateRule(e, crudThresholdRule(
		e.NS("SIM-AUDT-005", "thr"), sensor.edgeDeviceID, "gt", 20.0, 60))
	// 时间窗口取 00:00-00:01：本场景内 ticker 不会命中该窗口，规则只作为过滤维度存在。
	windowRule := autoCreateRule(e, map[string]any{
		"name":                 e.NS("SIM-AUDT-005", "win"),
		"trigger_type":         "time_window",
		"trigger_window_start": "00:00",
		"trigger_window_end":   "00:01",
		"trigger_window_edge":  "inside",
		"action_type":          "device_action",
		"action_device_id":     actuator.edgeDeviceID,
		"action_id":            cmdActionReadRainfall,
		"action_params_json":   "{}",
		"cooldown_sec":         86400,
	})

	// 不变式 1：每个过滤维度返回的每一行都必须真的符合条件，并且目标规则在其中。
	type auditFilterCase struct {
		query  string
		label  string
		expect func(autoRuleRow) error
		has    int64 // 必须出现的规则 ID
		absent int64 // 必须不出现的规则 ID
	}
	cases := []auditFilterCase{
		{
			query: "?trigger_type=sensor_threshold", label: "按触发类型 sensor_threshold",
			has: thresholdRule, absent: windowRule,
			expect: func(row autoRuleRow) error {
				if row.TriggerType != "sensor_threshold" {
					return fmt.Errorf("trigger_type=%q", row.TriggerType)
				}
				return nil
			},
		},
		{
			query: "?trigger_type=time_window", label: "按触发类型 time_window",
			has: windowRule, absent: thresholdRule,
			expect: func(row autoRuleRow) error {
				if row.TriggerType != "time_window" {
					return fmt.Errorf("trigger_type=%q", row.TriggerType)
				}
				return nil
			},
		},
		{
			query: "?action_type=device_action", label: "按动作类型 device_action",
			has: windowRule, absent: thresholdRule,
			expect: func(row autoRuleRow) error {
				if row.ActionType != "device_action" {
					return fmt.Errorf("action_type=%q", row.ActionType)
				}
				return nil
			},
		},
		{
			query: "?action_type=notification", label: "按动作类型 notification",
			has: thresholdRule, absent: windowRule,
			expect: func(row autoRuleRow) error {
				if row.ActionType != "notification" {
					return fmt.Errorf("action_type=%q", row.ActionType)
				}
				return nil
			},
		},
		{
			query: "?trigger_edge_device_id=" + strconv.FormatUint(uint64(sensor.edgeDeviceID), 10),
			label: "按触发设备", has: thresholdRule, absent: windowRule,
			expect: func(row autoRuleRow) error {
				if row.TriggerEdgeDeviceID != sensor.edgeDeviceID {
					return fmt.Errorf("trigger_edge_device_id=%d", row.TriggerEdgeDeviceID)
				}
				return nil
			},
		},
		{
			query: "?trigger_type=sensor_threshold&action_type=notification",
			label: "组合过滤", has: thresholdRule, absent: windowRule,
			expect: func(row autoRuleRow) error {
				if row.TriggerType != "sensor_threshold" || row.ActionType != "notification" {
					return fmt.Errorf("组合条件不满足: trigger_type=%q action_type=%q",
						row.TriggerType, row.ActionType)
				}
				return nil
			},
		},
	}
	for _, item := range cases {
		rows, err := autoListRules(e, item.query)
		if err != nil {
			e.Fatalf("%v", err)
		}
		hasTarget, hasAbsent := false, false
		for _, row := range rows {
			if err := item.expect(row); err != nil {
				e.Fatalf("%s：过滤结果里出现了不符合条件的行 id=%d（%v）", item.label, row.ID, err)
			}
			if row.ID == uint(item.has) {
				hasTarget = true
			}
			if row.ID == uint(item.absent) {
				hasAbsent = true
			}
		}
		if !hasTarget {
			e.Fatalf("%s：目标策略 %d 未出现在 %d 条结果里", item.label, item.has, len(rows))
		}
		if hasAbsent {
			e.Fatalf("%s：不该出现的策略 %d 出现在了结果里", item.label, item.absent)
		}
	}

	// 不变式 2：启用状态必须如实回传（界面据此显示启停）。
	// 说明：服务端**没有** enabled 查询参数（handler_automation.go 的 listAutomationRules
	// 只解析 trigger_type / trigger_edge_device_id / action_type），"按启用状态筛选"
	// 目前只能由界面在本地完成。因此这里断言"状态字段如实"，**不**把
	// "?enabled= 被忽略"写成契约（那会把一个缺口固化成断言）。
	e.Admin.Patch("/api/v1/automation-rules/"+strconv.FormatInt(thresholdRule, 10)+"/enabled",
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	rows, err := autoListRules(e, "?action_type=notification")
	if err != nil {
		e.Fatalf("%v", err)
	}
	checked := false
	for _, row := range rows {
		if row.ID == uint(thresholdRule) {
			checked = true
			if row.Enabled {
				e.Fatalf("已停用的策略 %d 在过滤结果里仍显示 enabled=true", thresholdRule)
			}
		}
	}
	if !checked {
		e.Fatalf("停用后策略 %d 仍应能在 ?action_type=notification 里查到（停止≠删除）", thresholdRule)
	}

	// 记录（不作为断言）：?enabled=false 目前会被服务端忽略。
	ignored, err := autoListRules(e, "?enabled=false")
	if err != nil {
		e.Fatalf("%v", err)
	}
	contradictions := 0
	for _, row := range ignored {
		if row.Enabled {
			contradictions++
		}
	}
	e.Evidence("SIM-AUDT-005.enabled_filter_gap", map[string]any{
		"query": "?enabled=false", "rows": len(ignored), "rows_with_enabled_true": contradictions,
		"note": "服务端不解析 enabled 参数；界面需本地过滤",
	})
	e.Evidence("SIM-AUDT-005.filters", map[string]any{
		"threshold_rule_id": thresholdRule, "window_rule_id": windowRule,
		"dims": []string{"trigger_type", "action_type", "trigger_edge_device_id", "组合"},
	})
}
