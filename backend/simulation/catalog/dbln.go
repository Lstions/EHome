//go:build simulation

// 场景目录 · SIM-DBLN 防抖与限流（设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-001..006）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面 / §4 场景清单 / §6 命名 / §10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API / §5.6 隔离命名）。
// 设计依据：docs/设计/自动化策略引擎方案.md（冷却 CooldownSec、日熔断 MaxDailyExec）。
//
// 本域守护的不变量（每条断言都有源码出处）：
//  1. 冷却窗内重复越限不重复执行（evaluator.go:428-437：triggered 命中即 return，
//     既不更新窗口也不再次提交 Planner）；
//  2. 冷却窗内抑制必须落审计，且**同一冷却窗只落首条**
//     （evaluator.go:465-488 recordSuppressed 的"同窗节流"，防高频上报刷量）；
//  3. 冷却到期回 armed，条件仍满足可再次执行（evaluator.go:436 "冷却到期, 回 armed"）；
//  4. 日熔断只统计 result=executed（planner.go:77-87），因此 notification /
//     pending_confirm 都不占当日额度 —— 这正是 SIM-DBLN-006 要守护的分支；
//  5. 日熔断提醒同日同规则只发一次（planner.go:250-258 notifyDailyLimitOnce 幂等闸）。
//
// 关于夹具：本域全部基于 autoProvisionDevice（auto.go）搭真实数据链路。该夹具**不会**
// 发任何自检数据帧，所以不存在"夹具哨兵值把场景带偏"的问题；即便如此，
// 所有"值"断言都锚定本场景自己上报的 23.5 ℃（而不是"收到了一条事件"）。
package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-DBLN-001",
		Title:  "触发一次后在冷却期内重复越限只记一条抑制记录，不重复执行",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-001；docs/设计/自动化策略引擎方案.md（CooldownSec）",
		Run:    dblnRun001,
	})
	Register(Scenario{
		ID:     "SIM-DBLN-002",
		Title:  "冷却期结束后再次越限会正常再执行一次",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-002；docs/设计/自动化策略引擎方案.md（冷却到期回 armed）",
		Run:    dblnRun002,
	})
	Register(Scenario{
		ID:     "SIM-DBLN-003",
		Title:  "冷却期内的抑制在同一窗口只落一条审计（高频上报不刷量）",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-003；docs/设计/自动化策略引擎方案.md（同窗节流）",
		Run:    dblnRun003,
	})
	Register(Scenario{
		ID:     "SIM-DBLN-004",
		Title:  "设置每日上限后，超出部分被熔断并记录原因",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-004；docs/设计/自动化策略引擎方案.md（MaxDailyExec）",
		Run:    dblnRun004,
	})
	Register(Scenario{
		ID:     "SIM-DBLN-005",
		Title:  "达到每日上限时只通知一次，不会每次越限都打扰用户",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-005；docs/设计/通知中心.md",
		Run:    dblnRun005,
	})
	Register(Scenario{
		ID:     "SIM-DBLN-006",
		Title:  "待确认的事件不占用每日执行额度",
		Domain: DomainDBLN,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-DBLN-006；docs/设计/自动化确认制闭环实现方案.md",
		Run:    dblnRun006,
	})
}

// ---------------------------------------------------------------------------
// 领域夹具
// ---------------------------------------------------------------------------

// dblnEvents 读某条规则下的事件（result 为空表示不过滤）。
func dblnEvents(e *harness.Env, ruleID int64, result string) ([]autoEventRow, error) {
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	return autoListEvents(e, query)
}

// dblnCount 统计某条规则下指定 result 的事件条数。
func dblnCount(e *harness.Env, ruleID int64, result string) int {
	e.T.Helper()
	rows, err := dblnEvents(e, ruleID, result)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return len(rows)
}

// dblnWaitResult 等到规则出现至少 want 条指定 result 的事件，返回最早那条。
func dblnWaitResult(e *harness.Env, ruleID int64, result string, want int) autoEventRow {
	e.T.Helper()
	var first autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := dblnEvents(e, ruleID, result)
		if err != nil {
			return err
		}
		if len(rows) < want {
			return fmt.Errorf("策略 %d 的 %s 事件只有 %d 条（期望 >= %d）", ruleID, result, len(rows), want)
		}
		// 列表按 id DESC：末条即最早一条。
		first = rows[len(rows)-1]
		return nil
	})
	return first
}

// dblnPump 以固定节奏重复上报越限，直到 cond 满足或超时。
//
// 为什么需要它：冷却/日熔断的验证要求"越过一个时间窗之后再越限一次"，而窗口长度
// 由产品决定、到达时刻不可预知。这里用**有界轮询 + 模拟真实上报节奏**替代 sleep，
// 与 autoRun005 在 Eventually 内部回执 ConfigResult 是同一手法（框架 §3 原则 3）。
// 上报间隔 500ms 属于"仿真器模拟上报节奏"，不作为断言同步手段：断言同步完全由
// cond 轮询承担。
func dblnPump(e *harness.Env, fx *autoFixture, raw uint16, timeout time.Duration, what string, cond func() error) {
	e.T.Helper()
	var lastReport time.Time
	err := e.EventuallyEveryError(timeout, 200*time.Millisecond, func() error {
		if time.Since(lastReport) >= 500*time.Millisecond {
			lastReport = time.Now()
			if err := fx.report(raw); err != nil {
				return err
			}
		}
		return cond()
	})
	if err != nil {
		e.Fatalf("重复越限未能在 %s 内使「%s」成立: %v", timeout, what, err)
	}
}

// dblnSampleCount 统计本设备已落库的统一数据条数（值等于 want 的条数）。
//
// 用途：证明"高频上报真的被链路逐帧处理了"。没有这一步，"抑制审计只有一条"
// 既可能是节流生效，也可能是那些上报压根没进管线 —— 两者无法区分。
func dblnSampleCount(e *harness.Env, edgeDeviceID uint, want float64) (int, error) {
	r := e.Admin.Get("/api/v1/devices/" + strconv.FormatUint(uint64(edgeDeviceID), 10) + "/sensor-data?limit=200")
	if r.Status != http.StatusOK {
		return 0, fmt.Errorf("GET /devices/%d/sensor-data 返回 %d: %s", edgeDeviceID, r.Status, r.BodyString())
	}
	var samples []autoSensorSample
	if err := json.Unmarshal(r.Data, &samples); err != nil {
		return 0, err
	}
	count := 0
	for _, sample := range samples {
		if sample.SensorName == "temperature" && sample.Value > want-1e-3 && sample.Value < want+1e-3 {
			count++
		}
	}
	return count, nil
}

// dblnDailyLimitNotifications 统计某条策略的日熔断提醒条数。
// 标题与来源由 planner.go:250-272 固定：title="策略日熔断: <规则名>"、
// source=automation_rule、source_id=<规则 ID>。
func dblnDailyLimitNotifications(e *harness.Env, ruleID int64, name string) int {
	e.T.Helper()
	rows, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	count := 0
	for _, row := range rows {
		if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) &&
			row.Title == "策略日熔断: "+name {
			count++
		}
	}
	return count
}

// dblnActionCount 统计某台设备上某个动作的受控指令条数。
// 复用 cnfm.go 的 cnfmOperations（同包跨文件调用，§4.1 只约束声明名不约束调用写法）。
func dblnActionCount(e *harness.Env, edgeDeviceID uint, actionID string) int {
	e.T.Helper()
	return cnfmCountAction(e, edgeDeviceID, actionID)
}

// dblnNotificationActionRule 生成一条纯通知规则体（不触碰 commandexec，无需固件能力事实）。
func dblnNotificationActionRule(name string, fx *autoFixture, cooldownSec int) map[string]any {
	return map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           cooldownSec,
		"max_daily_exec":         0,
	}
}

// dblnDeviceActionRule 生成一条"确实会产生 executed 结果"的规则体。
//
// 为什么日熔断类场景必须用 device_action：日熔断的计数口径是 result=executed
// （planner.go:79-81），notification 动作落的是 result=notification，
// 永远不计入日额度 —— 用通知动作写"达到上限被熔断"只会得到一条永不熔断的规则。
//
// ⚠ 为什么额度基线用**手动触发**而不是自动触发来占：
// 手动触发（TriggerRule → executeManualDeviceAction）用 JWT 里的操作者 ID，
// 是最确定的人工入口，**不依赖自动路径的健康状况**，因此"当日 executed = 1"
// 这个前置状态在任何时候都稳定可构造。它顺带证明了
// **手动与自动共用同一个日额度计数器**（设计 §3：手动触发仍计日熔断）。
//
// 台账注记：此处用手动触发填额度，是因为当时自动路径受 systemActorID 缺陷影响
// （全新部署下未确认的自动设备动作静默失败，台账 2026-09-12 第六轮）；
// 该缺陷修复后纯自动路径同样可用 —— 本场景验证的是"额度是否被正确计数与熔断"，
// 与额度由哪条入口消耗无关，因此不随该缺陷的修复状态而失效。
func dblnDeviceActionRule(name string, fx *autoFixture, actionID string, cooldownSec, maxDailyExec int, requireConfirmed bool) map[string]any {
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
		"action_params_json":     "{}",
		"require_confirmed":      requireConfirmed,
		"cooldown_sec":           cooldownSec,
		"max_daily_exec":         maxDailyExec,
	}
}

// ---------------------------------------------------------------------------
// SIM-DBLN-001 触发一次后在冷却期内重复越限只记一条抑制记录，不重复执行
// ---------------------------------------------------------------------------

func dblnRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-001", "cool", "sim_dbln_001_sensor")
	name := e.NS("SIM-DBLN-001", "cool")

	// cooldown_sec=60：保证下面 4 次越限全部落在同一个冷却窗内。
	ruleID := autoCreateRule(e, dblnNotificationActionRule(name, fx, 60))

	// 第一次越限：正常执行一次通知动作。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	first := dblnWaitResult(e, ruleID, "notification", 1)
	autoEventuallyFloat(e.T, "首次执行的 trigger_value", *first.TriggerValue, 23.5)
	if first.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", first.TriggerSource)
	}

	// 冷却期内再越限 3 次：必须都被抑制，且不产生第二次执行。
	dblnPump(e, fx, 235, 30*time.Second, "冷却窗内出现抑制审计", func() error {
		if got := dblnCount(e, ruleID, "suppressed_cooldown"); got < 1 {
			return fmt.Errorf("冷却期内尚未出现 suppressed_cooldown 审计（当前 %d 条）", got)
		}
		return nil
	})

	// 不变式 1：冷却期内绝不重复执行 —— 执行类事件只有第一条。
	notifications := dblnCount(e, ruleID, "notification")
	if notifications != 1 {
		e.Fatalf("冷却期内的重复越限产生了 %d 条通知执行事件，期望 1 条（冷却失效）", notifications)
	}

	// 不变式 2：被抑制的触发必须留下审计（可观测），且同窗只有首条。
	suppressed := dblnCount(e, ruleID, "suppressed_cooldown")
	if suppressed != 1 {
		e.Fatalf("同一冷却窗内的抑制审计有 %d 条，期望 1 条（同窗节流失效）", suppressed)
	}

	// 不变式 3：抑制是"策略层"的事实，不能变成对用户的重复打扰：
	// 通知中心里这条策略只应有一条通知（与执行次数一一对应）。
	var landed int
	e.Eventually(15*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		landed = 0
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				landed++
			}
		}
		if landed != 1 {
			return fmt.Errorf("通知中心里策略 %d 的通知有 %d 条，期望 1 条", ruleID, landed)
		}
		return nil
	})

	e.Evidence("SIM-DBLN-001.cooldown", map[string]any{
		"rule_id": ruleID, "cooldown_sec": 60, "reports": 4,
		"notification_events": notifications, "suppressed_cooldown_events": suppressed,
		"notifications_landed": landed,
	})
}

// ---------------------------------------------------------------------------
// SIM-DBLN-002 冷却期结束后再次越限会正常再执行一次
// ---------------------------------------------------------------------------

func dblnRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-002", "rearm", "sim_dbln_002_sensor")

	// cooldown_sec=3：短到可以在场景内等到窗结束，又长到足以稳定观测"窗内被抑制"。
	ruleID := autoCreateRule(e, dblnNotificationActionRule(e.NS("SIM-DBLN-002", "rearm"), fx, 3))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	dblnWaitResult(e, ruleID, "notification", 1)

	// 冷窗内立刻补 3 帧：必须被抑制（证明抑制确实发生了，而不是"压根没求值"）。
	for i := 0; i < 3; i++ {
		if err := fx.report(235); err != nil {
			e.Fatalf("冷却期内第 %d 次上报失败: %v", i+1, err)
		}
	}
	e.Eventually(20*time.Second, func() error {
		if got := dblnCount(e, ruleID, "suppressed_cooldown"); got < 1 {
			return fmt.Errorf("冷却期内尚未出现抑制审计（当前 %d 条）", got)
		}
		return nil
	})
	if got := dblnCount(e, ruleID, "notification"); got != 1 {
		e.Fatalf("冷却期内发生了第 %d 次执行，期望仍为 1 次", got)
	}

	// 不变式：冷却到期后条件仍满足（住宅温度不会因为冷却结束而变），必须能再次执行。
	// 用有界轮询 + 500ms 上报节奏推进时间窗，不用 sleep 断言。
	dblnPump(e, fx, 235, 45*time.Second, "冷却到期后第二次执行", func() error {
		if got := dblnCount(e, ruleID, "notification"); got < 2 {
			return fmt.Errorf("冷却到期后仍未再次执行（当前 %d 条通知事件）", got)
		}
		return nil
	})

	rows, err := dblnEvents(e, ruleID, "notification")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 2 {
		e.Fatalf("策略 %d 的通知执行事件有 %d 条，期望 2 条（冷却到期恰好再执行一次）", ruleID, len(rows))
	}
	for _, row := range rows {
		if row.TriggerValue == nil {
			e.Fatalf("执行事件必须带 trigger_value: %+v", row)
		}
		autoEventuallyFloat(e.T, "执行事件的 trigger_value", *row.TriggerValue, 23.5)
	}

	e.Evidence("SIM-DBLN-002.rearmed", map[string]any{
		"rule_id": ruleID, "cooldown_sec": 3,
		"notification_events": len(rows),
		"suppressed_events":   dblnCount(e, ruleID, "suppressed_cooldown"),
		"event_ids":           []uint{rows[0].ID, rows[1].ID},
	})
}

// ---------------------------------------------------------------------------
// SIM-DBLN-003 冷却期内的抑制在同一窗口只落一条审计（高频上报不刷量）
// ---------------------------------------------------------------------------

func dblnRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-003", "thr", "sim_dbln_003_sensor")

	// cooldown_sec=60：所有上报都落在同一个冷却窗内（节流的判定基准就是这个窗起点）。
	ruleID := autoCreateRule(e, dblnNotificationActionRule(e.NS("SIM-DBLN-003", "thr"), fx, 60))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	dblnWaitResult(e, ruleID, "notification", 1)

	// 高频连发：模拟"1 秒一帧"甚至更密的真实上报（12 帧）。
	const bursts = 12
	for i := 0; i < bursts; i++ {
		if err := fx.report(235); err != nil {
			e.Fatalf("第 %d 帧高频上报失败: %v", i+1, err)
		}
	}

	// 收敛条件同时要求：节流审计已出现 + 12 帧真的都被链路处理并落库。
	samples := 0
	e.Eventually(30*time.Second, func() error {
		var err error
		samples, err = dblnSampleCount(e, fx.edgeDeviceID, 23.5)
		if err != nil {
			return err
		}
		suppressed := dblnCount(e, ruleID, "suppressed_cooldown")
		if suppressed < 1 {
			return fmt.Errorf("尚未出现抑制审计")
		}
		if samples < bursts {
			return fmt.Errorf("高频上报只落库 %d 条（期望 %d），无法证明每一帧都被处理", samples, bursts)
		}
		return nil
	})

	// 不变式：12 帧越限 + 冷却窗内，抑制审计**恰好 1 条**（不是 0，也不是每帧一条）。
	suppressed := dblnCount(e, ruleID, "suppressed_cooldown")
	if suppressed != 1 {
		e.Fatalf("同一冷却窗内 %d 帧越限产生了 %d 条抑制审计，期望恰好 1 条（同窗节流）",
			bursts+1, suppressed)
	}
	if got := dblnCount(e, ruleID, "notification"); got != 1 {
		e.Fatalf("高频越限产生了 %d 次执行，期望 1 次", got)
	}
	if samples < bursts {
		e.Fatalf("本设备落库的 23.5℃ 样本只有 %d 条，少于上报的 %d 帧", samples, bursts)
	}

	e.Evidence("SIM-DBLN-003.throttle", map[string]any{
		"rule_id": ruleID, "cooldown_sec": 60, "high_freq_reports": bursts + 1,
		"persisted_samples": samples, "suppressed_cooldown_events": suppressed,
	})
}

// ---------------------------------------------------------------------------
// SIM-DBLN-004 设置每日上限后，超出部分被熔断并记录原因
// ---------------------------------------------------------------------------

func dblnRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-004", "cap", "sn3001_rain")
	// 需要"真的会 executed"的动作，因此必须让节点具备固件能力事实
	// （夹具在 cnfm.go，本域三个文件共用）。
	cnfmPrepareDispatchable(e, fx)
	name := e.NS("SIM-DBLN-004", "cap")

	// max_daily_exec=1：当日只允许执行 1 次；cooldown_sec=1 便于在场景内跨过冷却窗。
	ruleID := autoCreateRule(e, dblnDeviceActionRule(name, fx, "read_rainfall", 1, 1, false))

	// 第一步：管理员点一次"立即触发"，占掉当日唯一的名额（executed，计入日额度）。
	first := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var occupied autoEventRow
	first.Decode(&occupied)
	if occupied.Result != "executed" || occupied.CommandID == "" {
		e.Fatalf("占额度的手动触发未执行: result=%q command_id=%q detail=%q",
			occupied.Result, occupied.CommandID, occupied.Detail)
	}

	// 第二步：条件自然越限（自动路径）——当日已执行 1 >= 上限 1 → 必须被熔断并记录原因。
	dblnPump(e, fx, 235, 45*time.Second, "日熔断事件", func() error {
		if got := dblnCount(e, ruleID, "suppressed_daily_limit"); got < 1 {
			return fmt.Errorf("尚未出现 suppressed_daily_limit（当前 %d 条）", got)
		}
		return nil
	})

	// 不变式 1：上限之内恰好执行 1 次，超出部分一次都没执行。
	if got := dblnCount(e, ruleID, "executed"); got != 1 {
		e.Fatalf("策略 %d 当日执行了 %d 次，期望 1 次（日上限=1）", ruleID, got)
	}
	// 实施层证据：受控链路上也只有 1 条指令（熔断不是"只在策略表里记一笔"）。
	if got := dblnActionCount(e, fx.edgeDeviceID, "read_rainfall"); got != 1 {
		e.Fatalf("设备上的 read_rainfall 指令有 %d 条，期望 1 条（超出上限的部分不应下发）", got)
	}

	// 不变式 2：熔断必须写明原因，用户能知道"为什么没执行"。
	limitRows, err := dblnEvents(e, ruleID, "suppressed_daily_limit")
	if err != nil {
		e.Fatalf("%v", err)
	}
	blocked := limitRows[len(limitRows)-1]
	if blocked.Detail == "" {
		e.Fatalf("熔断事件没有写明原因（detail 为空）: %+v", blocked)
	}
	if blocked.Detail != fmt.Sprintf("daily limit %d reached", 1) {
		e.Fatalf("熔断原因 detail=%q，期望写明当日上限 1（planner.go:83 的固定文案）", blocked.Detail)
	}
	if blocked.TriggerSource != "auto" {
		e.Fatalf("熔断事件的 trigger_source=%q，期望 auto", blocked.TriggerSource)
	}

	e.Evidence("SIM-DBLN-004.daily_limit", map[string]any{
		"rule_id": ruleID, "max_daily_exec": 1,
		"executed_events": 1, "suppressed_daily_limit_events": len(limitRows),
		"detail": blocked.Detail, "command_id": occupied.CommandID,
		"quota_consumed_by": occupied.TriggerSource,
	})
}

// ---------------------------------------------------------------------------
// SIM-DBLN-005 达到每日上限时只通知一次，不会每次越限都打扰用户
// ---------------------------------------------------------------------------

func dblnRun005(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-005", "once", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)
	name := e.NS("SIM-DBLN-005", "once")

	ruleID := autoCreateRule(e, dblnDeviceActionRule(name, fx, "read_rainfall", 1, 1, false))

	// 先用管理员的一次"立即触发"占满当日唯一名额（理由见 dblnDeviceActionRule 注释：
	// 自动路径的 device_action 在全新部署上拿不到系统主体，无法产生 executed）。
	occupied := manuTrigger(e, ruleID).Expect(http.StatusOK)
	var first autoEventRow
	occupied.Decode(&first)
	if first.Result != "executed" || first.CommandID == "" {
		e.Fatalf("占额度的手动触发未执行: result=%q command_id=%q detail=%q",
			first.Result, first.CommandID, first.Detail)
	}

	// 反复越限，至少要撞到 2 次日熔断 —— 否则"只通知一次"可能只是"只熔断过一次"。
	dblnPump(e, fx, 235, 60*time.Second, "至少两次日熔断", func() error {
		if got := dblnCount(e, ruleID, "suppressed_daily_limit"); got < 2 {
			return fmt.Errorf("日熔断只发生了 %d 次（期望 >= 2）", got)
		}
		return nil
	})

	// 收敛：日熔断提醒至少出现 1 条（它在同一次触发里同步落库，见 planner.go:82-86）。
	e.Eventually(20*time.Second, func() error {
		if got := dblnDailyLimitNotifications(e, ruleID, name); got < 1 {
			return fmt.Errorf("尚未出现日熔断提醒")
		}
		return nil
	})

	// 不变式：多次熔断只打扰用户一次（notifyDailyLimitOnce 的当日幂等闸）。
	if got := dblnDailyLimitNotifications(e, ruleID, name); got != 1 {
		e.Fatalf("日熔断提醒有 %d 条，期望 1 条（每次越限都通知会打扰用户）", got)
	}

	// 提醒必须是对用户可读的：source 可回链策略、未读、级别为 warning。
	rows, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	var landed autoNotificationRow
	found := false
	for _, row := range rows {
		if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) &&
			row.Title == "策略日熔断: "+name {
			landed, found = row, true
		}
	}
	if !found {
		e.Fatalf("通知中心里找不到日熔断提醒（策略 %d）", ruleID)
	}
	if landed.Type != "warning" {
		e.Fatalf("日熔断提醒的 type=%q，期望 warning", landed.Type)
	}
	if landed.Read {
		e.Fatalf("新日熔断提醒不应是已读状态")
	}

	e.Evidence("SIM-DBLN-005.notify_once", map[string]any{
		"rule_id": ruleID, "max_daily_exec": 1,
		"suppressed_daily_limit_events": dblnCount(e, ruleID, "suppressed_daily_limit"),
		"daily_limit_notifications":     1, "notification_id": landed.ID,
		"quota_consumed_by": first.TriggerSource,
	})
}

// ---------------------------------------------------------------------------
// SIM-DBLN-006 待确认的事件不占用每日执行额度
// ---------------------------------------------------------------------------

func dblnRun006(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-DBLN-006", "quota", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)
	name := e.NS("SIM-DBLN-006", "quota")

	// 高风险动作 + 确认制 + 日上限 1：
	//   pending_confirm 不计入 executed（planner.go:79-81），所以确认之前不会被熔断；
	//   一旦确认成 executed，下一次越限就必须被熔断。
	ruleID := autoCreateRule(e, dblnDeviceActionRule(name, fx, "reset_rainfall", 1, 1, true))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	dblnWaitResult(e, ruleID, "pending_confirm", 1)

	// 冷却到期后继续越限：日上限=1，但待确认不占额度 → 必须能继续产生待确认事件。
	dblnPump(e, fx, 235, 45*time.Second, "第二条待确认事件", func() error {
		if got := dblnCount(e, ruleID, "pending_confirm"); got < 2 {
			return fmt.Errorf("待确认事件只有 %d 条（日上限=1 不应拦下它们）", got)
		}
		return nil
	})

	// 不变式 1：确认之前一条都没执行，且没有任何一条待确认被日熔断拦下。
	if got := dblnCount(e, ruleID, "executed"); got != 0 {
		e.Fatalf("未经确认就出现了 %d 条 executed 事件", got)
	}
	if got := dblnCount(e, ruleID, "suppressed_daily_limit"); got != 0 {
		e.Fatalf("待确认事件被日熔断拦下了 %d 条 —— 待确认不应占用当日额度", got)
	}
	pendingRows, err := dblnEvents(e, ruleID, "pending_confirm")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(pendingRows) < 2 {
		e.Fatalf("待确认事件只有 %d 条，期望 >= 2（max_daily_exec=1 下仍可继续触发）", len(pendingRows))
	}
	for _, row := range pendingRows {
		if row.CommandID != "" {
			e.Fatalf("待确认事件不该有 command_id: %+v", row)
		}
	}

	// 确认最新那条待确认 → 变成 executed，此时当日额度才被占用。
	target := pendingRows[0]
	cnfmReauthenticate(e)
	confirmed := cnfmConfirm(e, target.ID).Expect(http.StatusOK)
	var closed autoEventRow
	confirmed.Decode(&closed)
	if closed.Result != "executed" || closed.CommandID == "" {
		e.Fatalf("确认后事件未闭环: result=%q command_id=%q detail=%q",
			closed.Result, closed.CommandID, closed.Detail)
	}

	// 不变式 2：此时当日已执行 1 >= 上限 1，下一次越限必须被熔断。
	dblnPump(e, fx, 235, 60*time.Second, "确认后触发的日熔断", func() error {
		if got := dblnCount(e, ruleID, "suppressed_daily_limit"); got < 1 {
			return fmt.Errorf("确认后仍未出现日熔断（当前 %d 条）", got)
		}
		return nil
	})

	if got := dblnCount(e, ruleID, "executed"); got != 1 {
		e.Fatalf("当日 executed 事件有 %d 条，期望 1 条（上限=1，待确认那条确认后才占额度）", got)
	}
	// 被确认的那条事件是**就地翻转**：pending_confirm 少一条、executed 多一条。
	remaining, err := dblnEvents(e, ruleID, "pending_confirm")
	if err != nil {
		e.Fatalf("%v", err)
	}
	for _, row := range remaining {
		if row.ID == target.ID {
			e.Fatalf("被确认的事件 %d 仍在 pending_confirm 列表里", target.ID)
		}
	}

	e.Evidence("SIM-DBLN-006.quota", map[string]any{
		"rule_id": ruleID, "max_daily_exec": 1,
		"pending_before_confirm": len(pendingRows), "confirmed_event_id": target.ID,
		"command_id": closed.CommandID, "executed_events": 1,
		"suppressed_daily_limit_events": dblnCount(e, ruleID, "suppressed_daily_limit"),
	})
}
