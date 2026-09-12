//go:build simulation

// 场景目录 · SIM-MANU 手动触发（设计/自动化引擎场景仿真验证.md §4 SIM-MANU-001..004）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面 / §4 场景清单 / §6 命名 / §10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API / §5.6 隔离命名）。
//
// 本域守护的不变量（每条断言都有源码出处）：
//  1. "立即触发"跳过条件评估：planner.go:359-368 TriggerRule 的注释与实现都不查 F4 conditions，
//     因此条件不满足时点击也必须执行；
//  2. 手动触发**仍然**受冷却约束：planner.go:406-428 用 DB 里最近一条
//     executed/pending_confirm 的 triggered_at 做冷却兜底；
//  3. 手动触发**仍然**受日熔断约束：planner.go:383-402 与自动触发同口径
//     （只统计 result=executed）；
//  4. 审计可区分来源：TriggerSource=manual（models/automation.go:52-56）。
//
// 关于夹具：全部复用 autoProvisionDevice / autoCreateRule / autoListEvents（auto.go）。
// device_action 类场景还需要 cnfmPrepareDispatchable（cnfm.go，本域共用）把节点
// 备成"可被 commandexec 接纳"的状态。
package catalog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-MANU-001",
		Title:  "管理员点「立即触发」后动作马上执行，不需要等条件满足",
		Domain: DomainMANU,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-MANU-001；docs/设计/自动化策略引擎方案.md（TriggerRule）",
		Run:    manuRun001,
	})
	Register(Scenario{
		ID:     "SIM-MANU-002",
		Title:  "手动触发也会计入冷却，防止误连点刷爆",
		Domain: DomainMANU,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-MANU-002；docs/设计/自动化策略引擎方案.md（手动触发的冷却兜底）",
		Run:    manuRun002,
	})
	Register(Scenario{
		ID:     "SIM-MANU-003",
		Title:  "手动触发也会计入每日上限",
		Domain: DomainMANU,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-MANU-003；docs/设计/自动化策略引擎方案.md（MaxDailyExec）",
		Run:    manuRun003,
	})
	Register(Scenario{
		ID:     "SIM-MANU-004",
		Title:  "手动触发在审计里可区分出来（来源为 manual）",
		Domain: DomainMANU,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-MANU-004；docs/设计/自动化策略引擎方案.md（TriggerSource）",
		Run:    manuRun004,
	})
}

// ---------------------------------------------------------------------------
// 领域夹具
// ---------------------------------------------------------------------------

// manuTrigger 点一次"立即触发"（POST /automation-rules/:id/trigger）。
// 端点不接收 body，返回落库的 AutomationEvent 本身（handler_automation.go:472-502）。
func manuTrigger(e *harness.Env, ruleID int64) *harness.Response {
	e.T.Helper()
	return e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger",
		map[string]any{})
}

// manuMissingSensorConditions 是一段引用了"本场景永远不会上报的传感器"的附加条件。
//
// 用途：证明手动触发确实跳过了条件评估 —— 同一条上报数据下自动路径永不满足
// （evaluator.go:415-420：任一条件缺字段即不触发），而点击"立即触发"必须照样执行。
func manuMissingSensorConditions() string {
	return "[{\"sensor_name\":\"humidity\",\"comparator\":\"gt\",\"threshold\":999}]"
}

// manuEvents 读某条规则下的全部事件。
func manuEvents(e *harness.Env, ruleID int64) ([]autoEventRow, error) {
	return autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
}

// manuWaitManual 等到规则出现一条 trigger_source=manual 的事件并返回它。
func manuWaitManual(e *harness.Env, ruleID int64) autoEventRow {
	e.T.Helper()
	var manual autoEventRow
	e.Eventually(20*time.Second, func() error {
		rows, err := manuEvents(e, ruleID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TriggerSource == "manual" {
				manual = row
				return nil
			}
		}
		return fmt.Errorf("策略 %d 尚无 trigger_source=manual 的事件（当前 %d 条）", ruleID, len(rows))
	})
	return manual
}

// ---------------------------------------------------------------------------
// SIM-MANU-001 管理员点「立即触发」后动作马上执行，不需要等条件满足
// ---------------------------------------------------------------------------

func manuRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-MANU-001", "now", "sim_manu_001_sensor")
	name := e.NS("SIM-MANU-001", "now")

	// 规则带一条**永远不满足**的附加条件（humidity 从未上报）：
	// 自动路径必然沉默，手动路径必须照常执行。
	body := dblnNotificationActionRule(name, fx, 300)
	body["conditions_json"] = manuMissingSensorConditions()
	ruleID := autoCreateRule(e, body)

	// 先让真实数据流跑起来：235 * 0.1 = 23.5 ℃，触发阈值 gt 20 本身是满足的，
	// 但附加条件缺字段 → 自动路径必须不触发（这就是"等条件满足"的那部分）。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	// 证明数据链路确实是活的：统一数据里必须能看到这次上报。
	samples := 0
	e.Eventually(25*time.Second, func() error {
		count, err := dblnSampleCount(e, fx.edgeDeviceID, 23.5)
		if err != nil {
			return err
		}
		samples = count
		if samples < 1 {
			return fmt.Errorf("本设备尚无 temperature=23.5 的统一数据，链路可能没通")
		}
		return nil
	})

	// 自动路径静默：条件不满足就不产生事件。
	autoRows, err := manuEvents(e, ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(autoRows) != 0 {
		e.Fatalf("附加条件不满足时自动路径产生了 %d 条事件（误触发）: %+v", len(autoRows), autoRows[0])
	}

	// 不变式：点击"立即触发"后立刻执行，不看条件。
	triggered := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var manual autoEventRow
	triggered.Decode(&manual)
	if manual.RuleID != uint(ruleID) || manual.ID == 0 {
		e.Fatalf("手动触发返回的事件不完整: %+v", manual)
	}
	if manual.Result != "notification" {
		e.Fatalf("手动触发通知类策略的 result=%q，期望 notification（detail=%q）", manual.Result, manual.Detail)
	}
	if manual.TriggerSource != "manual" {
		e.Fatalf("手动触发的事件 trigger_source=%q，期望 manual", manual.TriggerSource)
	}

	// 列表口径读到同一条事实，且只有它一条（自动路径自始至终没产生事件）。
	rows, err := manuEvents(e, ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 1 {
		e.Fatalf("手动触发后策略 %d 应有且仅有 1 条事件，实际 %d 条: %+v", ruleID, len(rows), rows)
	}
	if rows[0].ID != manual.ID {
		e.Fatalf("列表里的事件 %d 与触发响应 %d 不一致", rows[0].ID, manual.ID)
	}

	e.Evidence("SIM-MANU-001.immediate", map[string]any{
		"rule_id": ruleID, "event_id": manual.ID, "result": manual.Result,
		"trigger_source": manual.TriggerSource, "persisted_samples": samples,
		"auto_events_before_click": 0,
	})
}

// ---------------------------------------------------------------------------
// SIM-MANU-002 手动触发也会计入冷却，防止误连点刷爆
// ---------------------------------------------------------------------------

func manuRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-MANU-002", "cool", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)

	// 必须用 device_action：手动触发的冷却兜底只查 result IN (executed, pending_confirm)
	// 的最近一条事件（planner.go:408-411），notification 动作落的是 result=notification，
	// 根本不进冷却判定 —— 用通知动作验证"手动也受冷却"会得到一条永不冷却的规则。
	name := e.NS("SIM-MANU-002", "cool")
	ruleID := autoCreateRule(e, dblnDeviceActionRule(name, fx, "read_rainfall", 60, 0, false))

	// 第 1 次点击：正常执行。
	first := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var executed autoEventRow
	first.Decode(&executed)
	if executed.Result != "executed" || executed.CommandID == "" {
		e.Fatalf("首次手动触发未执行: result=%q command_id=%q detail=%q",
			executed.Result, executed.CommandID, executed.Detail)
	}
	if executed.TriggerSource != "manual" {
		e.Fatalf("首次手动触发的事件 trigger_source=%q，期望 manual", executed.TriggerSource)
	}

	// 第 2 次点击（冷却 60s 内）：必须被抑制，且写明还剩多久。
	second := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var blocked autoEventRow
	second.Decode(&blocked)
	if blocked.Result != "suppressed_cooldown" {
		e.Fatalf("冷却期内的第二次手动触发 result=%q，期望 suppressed_cooldown（防误连点失效）",
			blocked.Result)
	}
	if blocked.TriggerSource != "manual" {
		e.Fatalf("被抑制的手动触发事件 trigger_source=%q，期望 manual", blocked.TriggerSource)
	}
	if !strings.Contains(blocked.Detail, "cooldown active") {
		e.Fatalf("抑制原因 detail=%q，期望写明冷却仍在生效（planner.go:421 的固定文案）", blocked.Detail)
	}

	// 不变式：连点不会真的下发第二条指令。
	if got := dblnActionCount(e, fx.edgeDeviceID, "read_rainfall"); got != 1 {
		e.Fatalf("连点两次后设备上的 read_rainfall 指令有 %d 条，期望 1 条", got)
	}
	rows, err := manuEvents(e, ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	executedCount, suppressedCount := 0, 0
	for _, row := range rows {
		switch row.Result {
		case "executed":
			executedCount++
		case "suppressed_cooldown":
			suppressedCount++
		}
	}
	if executedCount != 1 || suppressedCount != 1 {
		e.Fatalf("事件分布异常：executed=%d suppressed_cooldown=%d，期望各 1 条（共 %d 条）",
			executedCount, suppressedCount, len(rows))
	}

	e.Evidence("SIM-MANU-002.cooldown", map[string]any{
		"rule_id": ruleID, "cooldown_sec": 60,
		"executed_events": executedCount, "suppressed_events": suppressedCount,
		"command_id": executed.CommandID, "detail": blocked.Detail,
		"operations": 1,
	})
}

// ---------------------------------------------------------------------------
// SIM-MANU-003 手动触发也会计入每日上限
// ---------------------------------------------------------------------------

func manuRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-MANU-003", "cap", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)

	// cooldown_sec 传 0：本场景只验证日熔断，冷却不参与判定 ——
	// TriggerRule 的日熔断检查排在冷却检查**之前**（planner.go:383-402 先于 406-428），
	// 第二次点击必然先撞日上限，因此冷却取什么值都不影响本场景的结论。
	//
	// ⚠ 顺带记一条实测缺陷（已单独上报，这里不写成断言）：CooldownSec 带
	// gorm:"default:300"，GORM 的零值规则会把显式传入的 0 静默写成 DB 默认 300 ——
	// 传 0 的真实语义是"默认 300"，不是"关闭冷却"。与 models/automation.go:105-109
	// 对 Enabled 记载的是同一类缺陷。
	name := e.NS("SIM-MANU-003", "cap")
	ruleID := autoCreateRule(e, dblnDeviceActionRule(name, fx, "read_rainfall", 0, 1, false))

	first := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var executed autoEventRow
	first.Decode(&executed)
	if executed.Result != "executed" || executed.CommandID == "" {
		e.Fatalf("首次手动触发未执行: result=%q command_id=%q detail=%q",
			executed.Result, executed.CommandID, executed.Detail)
	}

	// 不变式：第二次点击撞上当日上限（口径与自动触发一致：只数 executed）。
	second := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var blocked autoEventRow
	second.Decode(&blocked)
	if blocked.Result != "suppressed_daily_limit" {
		e.Fatalf("超过当日上限后的手动触发 result=%q，期望 suppressed_daily_limit", blocked.Result)
	}
	if blocked.TriggerSource != "manual" {
		e.Fatalf("被熔断的手动触发事件 trigger_source=%q，期望 manual", blocked.TriggerSource)
	}
	if blocked.Detail != fmt.Sprintf("daily limit %d reached", 1) {
		e.Fatalf("熔断原因 detail=%q，期望写明当日上限 1（planner.go:395 的固定文案）", blocked.Detail)
	}

	// 实施层证据：超限那次真的没有下发第二条指令。
	if got := dblnActionCount(e, fx.edgeDeviceID, "read_rainfall"); got != 1 {
		e.Fatalf("超过当日上限后设备上的 read_rainfall 指令有 %d 条，期望 1 条", got)
	}
	rows, err := manuEvents(e, ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	executedCount, limitCount := 0, 0
	for _, row := range rows {
		switch row.Result {
		case "executed":
			executedCount++
		case "suppressed_daily_limit":
			limitCount++
		}
	}
	if executedCount != 1 || limitCount != 1 {
		e.Fatalf("事件分布异常：executed=%d suppressed_daily_limit=%d，期望各 1 条（共 %d 条）",
			executedCount, limitCount, len(rows))
	}

	e.Evidence("SIM-MANU-003.daily_limit", map[string]any{
		"rule_id": ruleID, "max_daily_exec": 1,
		"executed_events": executedCount, "suppressed_daily_limit_events": limitCount,
		"command_id": executed.CommandID, "detail": blocked.Detail, "operations": 1,
	})
}

// ---------------------------------------------------------------------------
// SIM-MANU-004 手动触发在审计里可区分出来（来源为 manual）
// ---------------------------------------------------------------------------

func manuRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-MANU-004", "src", "sim_manu_004_sensor")
	name := e.NS("SIM-MANU-004", "src")

	// cooldown_sec=0：自动路径沿用默认冷却 300s，手动路径直接跳过冷却兜底
	// （planner.go:406），于是同一条规则上可以同时观察到两种来源。
	ruleID := autoCreateRule(e, dblnNotificationActionRule(name, fx, 0))

	// 自动路径：真实上报越限 → trigger_source=auto。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	auto := dblnWaitResult(e, ruleID, "notification", 1)
	if auto.TriggerSource != "auto" {
		e.Fatalf("上报触发的事件 trigger_source=%q，期望 auto", auto.TriggerSource)
	}
	if auto.TriggerValue == nil {
		e.Fatalf("上报触发的事件必须带 trigger_value（本次上报 23.5℃）: %+v", auto)
	}
	autoEventuallyFloat(e.T, "自动事件的 trigger_value", *auto.TriggerValue, 23.5)

	// 手动路径：管理员点"立即触发" → trigger_source=manual。
	triggered := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var manual autoEventRow
	triggered.Decode(&manual)
	if manual.Result != "notification" {
		e.Fatalf("手动触发 notification 策略的 result=%q，期望 notification", manual.Result)
	}

	// 不变式 1：同一事件绝不可能是两种来源，两条事件的来源必须能区分开。
	if manual.ID == auto.ID {
		e.Fatalf("手动触发返回的事件与自动事件是同一条（id=%d），来源无法区分", auto.ID)
	}
	if manual.TriggerSource != "manual" {
		e.Fatalf("手动触发的事件 trigger_source=%q，期望 manual", manual.TriggerSource)
	}
	manual = manuWaitManual(e, ruleID)

	// 不变式 2：列表口径里两种来源同时存在，且各自可归因。
	rows, err := manuEvents(e, ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	sources := map[string]int{}
	for _, row := range rows {
		sources[row.TriggerSource]++
		if row.TriggerSource == "manual" && row.ID == manual.ID {
			if row.Result != "notification" {
				e.Fatalf("列表里手动事件的 result=%q，期望 notification", row.Result)
			}
		}
	}
	if sources["auto"] != 1 || sources["manual"] != 1 {
		e.Fatalf("事件来源分布为 %v，期望 auto/manual 各 1 条（共 %d 条）", sources, len(rows))
	}

	e.Evidence("SIM-MANU-004.source", map[string]any{
		"rule_id": ruleID, "auto_event_id": auto.ID, "manual_event_id": manual.ID,
		"sources": sources, "auto_trigger_value": *auto.TriggerValue,
	})
}
