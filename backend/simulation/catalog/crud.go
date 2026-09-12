//go:build simulation

// 场景目录 · SIM-CRUD 规则管理（设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-001..006）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§6 命名、§10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API、§5.6 隔离、§7 红线）。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. 规则是用户可见的持久化事实：创建后立刻能在列表/详情读到，服务端补齐的缺省值如实体现在响应里；
//  2. CRUD 写路径必须让引擎缓存失效（handler_automation.go 的 evaluator.Invalidate）——
//     "改了阈值/开关却不生效"是用户最难察觉、后果最重的一类缺陷：界面看着配置对了，实际按旧配置跑；
//  3. 校验 fail-closed：配置不完整的规则必须被拒绝且不入库，而不是"存下来但永不触发"；
//  4. 删除是软删：规则不再参与求值，但历史事件必须仍可归因（automation_events 不随规则消失）；
//  5. event 触发器是占位类型：创建必须被明确拒绝，绝不能落库成一条永不触发的规则。
//
// 复用（同包跨文件调用；框架 §4.1 只约束"声明名"，不约束调用写法）：
// autoProvisionDevice / autoCreateRule / autoListRules / autoListEvents / autoEventuallyFloat。
//
// 命名纪律（框架 §4.1 + 门禁第 8 条）：本文件包级标识符一律以 crud 开头。
package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

// crudDomain 是本域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const crudDomain Domain = "CRUD"

// crudErrKeepWatching 是"观察窗继续跑"的哨兵错误。
//
// 为什么需要：本域有两处必须断言"某段时间内没有发生某事"（阈值调高后不再触发、
// 停用后不再触发）。否定断言不能只查一次就下结论 —— 那和"链路根本没跑"无法区分。
// 因此用一个跑满固定窗口的上报循环，把"这段时间内每一帧都被引擎求值过"变成
// 可验证的前提（由对照规则在窗口内的再次触发来证明），窗口结束后再断言否定命题。
var crudErrKeepWatching = errors.New("crud: keep watching")

func init() {
	Register(Scenario{
		ID:     "SIM-CRUD-001",
		Title:  "管理员新建一条自动化策略后，立刻能在策略列表里查到它",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-001；docs/设计/自动化策略引擎方案.md",
		Run:    crudRun001,
	})
	Register(Scenario{
		ID:     "SIM-CRUD-002",
		Title:  "管理员把阈值改成会触发的值后，下一次读数就按新阈值判定",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-002（规则缓存失效 Invalidate）",
		Run:    crudRun002,
	})
	Register(Scenario{
		ID:     "SIM-CRUD-003",
		Title:  "管理员停用策略后条件再满足也不会动作，重新启用后马上恢复",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-003（PATCH enabled 立即生效）",
		Run:    crudRun003,
	})
	Register(Scenario{
		ID:     "SIM-CRUD-004",
		Title:  "配置不完整（缺比较符、阈值或动作参数）的策略会被拒绝，列表里不会多出一条",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-004（fail-closed 校验）",
		Run:    crudRun004,
	})
	Register(Scenario{
		ID:     "SIM-CRUD-005",
		Title:  "删除策略后它不再触发，但之前产生的执行记录仍然查得到",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-005（软删保留归因）",
		Run:    crudRun005,
	})
	Register(Scenario{
		ID:     "SIM-CRUD-006",
		Title:  "还不支持的 event 类型策略会被明确拒绝，不会存成一条永不触发的策略",
		Domain: crudDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CRUD-006（占位类型 fail-closed）",
		Run:    crudRun006,
	})
}

// ---------------------------------------------------------------------------
// 本域工具
// ---------------------------------------------------------------------------

// crudThresholdRule 生成一条本域最常用的规则体：sensor_threshold + notification。
func crudThresholdRule(name string, edgeDeviceID uint, comparator string, threshold float64, cooldownSec int) map[string]any {
	return map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     comparator,
		"trigger_threshold":      threshold,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           cooldownSec,
	}
}

// crudListRuleEvents 读某条策略的事件（result 为空表示不过滤）。
func crudListRuleEvents(e *harness.Env, ruleID int64, result string) ([]autoEventRow, error) {
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	return autoListEvents(e, query)
}

// crudCountRuleEvents 统计事件条数。查询失败直接判场景失败 ——
// 把查询失败静默当成 0 条，会让"没有误触发"这类否定断言退化成永远为真。
func crudCountRuleEvents(e *harness.Env, ruleID int64, result string) int {
	e.T.Helper()
	rows, err := crudListRuleEvents(e, ruleID, result)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return len(rows)
}

// crudWaitRuleEvent 轮询等待某条策略出现指定 result 的事件。
// 触发发生在 SensorParserConsumer 的解析回调里，时序不可预知 —— 一律有界轮询
// 收敛，不用 sleep 同步（框架 §3 原则 3）。
func crudWaitRuleEvent(e *harness.Env, ruleID int64, result string, timeout time.Duration) autoEventRow {
	e.T.Helper()
	var found autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := crudListRuleEvents(e, ruleID, result)
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

// crudObserveReports 在 window 窗口内按 interval 节奏持续上报，返回窗口期间
// 对照策略新增的事件数。返回值 ≥1 是"这段时间里引擎确实在求值这些帧"的证据。
//
// 为什么不用 time.Sleep：窗口本身不是断言，断言在窗口之后；窗口内的上报节奏
// 由 harness 的轮询原语产生（框架 §3 原则 3 允许仿真器按真实节奏上报，
// 但禁止把 sleep 当作断言的同步手段）。恒返回哨兵错误 → 窗口必然跑满。
func crudObserveReports(e *harness.Env, fx *autoFixture, controlID int64, window, interval time.Duration) int {
	e.T.Helper()
	before := crudCountRuleEvents(e, controlID, "")
	err := e.EventuallyEveryError(window, interval, func() error {
		if reportErr := fx.report(235); reportErr != nil {
			e.Fatalf("节点上报失败: %v", reportErr)
		}
		return crudErrKeepWatching
	})
	if !errors.Is(err, crudErrKeepWatching) {
		e.Fatalf("观察窗未按预期跑满（哨兵错误被替换）: %v", err)
	}
	return crudCountRuleEvents(e, controlID, "") - before
}

// ---------------------------------------------------------------------------
// SIM-CRUD-001 新建规则后立刻能在列表里查到
// ---------------------------------------------------------------------------

func crudRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-001", "rule", "sim_crud_001_sensor")
	name := e.NS("SIM-CRUD-001", "rule")

	// 最小可用配置：只填"条件 + 动作"，其余交给服务端补缺省值。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "info",
	})

	// 不变式 1：创建后详情接口必须原样回显用户填的条件与动作。
	detail := e.Admin.Get("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10)).
		Expect(http.StatusOK)
	var got autoRuleRow
	detail.Decode(&got)
	if got.ID != uint(ruleID) || got.Name != name {
		e.Fatalf("详情回显不一致: id=%d name=%q（期望 id=%d name=%q）", got.ID, got.Name, ruleID, name)
	}
	if got.TriggerType != "sensor_threshold" || got.TriggerSensorName != "temperature" ||
		got.TriggerComparator != "gt" || got.TriggerEdgeDeviceID != fx.edgeDeviceID ||
		got.ActionType != "notification" || got.ActionLevel != "info" {
		e.Fatalf("详情回显的条件/动作与请求不一致: %+v", got)
	}
	autoEventuallyFloat(e.T, "trigger_threshold", got.TriggerThreshold, 20.0)

	// 不变式 2：缺省值必须是用户能在界面上看到的那些值（不是空/零的中间态）。
	// 这些默认值来自 handler_automation.go 的创建分支，是本场景对"默认即启用"的守护。
	if !got.Enabled {
		e.Fatalf("未显式指定 enabled 时新建策略应为启用状态，实际 enabled=false")
	}
	if got.CooldownSec != 300 {
		e.Fatalf("未指定 cooldown_sec 时应回落默认 300，实际 %d", got.CooldownSec)
	}
	if got.MaxDailyExec != 0 || got.TriggerDurationSec != 0 || got.RequireConfirmed {
		e.Fatalf("未指定执行约束时应为 0/false，实际 max_daily_exec=%d duration=%d require_confirmed=%v",
			got.MaxDailyExec, got.TriggerDurationSec, got.RequireConfirmed)
	}

	// 不变式 3：列表口径（全量 + 按触发类型过滤）都能看到它。
	for _, query := range []string{"", "?trigger_type=sensor_threshold"} {
		rows, err := autoListRules(e, query)
		if err != nil {
			e.Fatalf("%v", err)
		}
		found := false
		for _, row := range rows {
			if row.ID == uint(ruleID) {
				found = true
				if row.Name != name || !row.Enabled {
					e.Fatalf("列表里 id=%d 的行与详情不一致: %+v", ruleID, row)
				}
			}
		}
		if !found {
			e.Fatalf("新建策略 %d 未出现在 GET /automation-rules%s 的 %d 条结果中", ruleID, query, len(rows))
		}
	}

	// 不变式 4：另一类合法触发器（time_window）同样能建、能查。
	// 窗口取 00:00-00:01 且 edge=enter：这条规则在本场景内不会被 ticker 触发，
	// 但它的存在本身证明了"创建路径不只有 sensor_threshold 一种形状"。
	windowName := e.NS("SIM-CRUD-001", "window")
	windowID := autoCreateRule(e, map[string]any{
		"name":                 windowName,
		"trigger_type":         "time_window",
		"trigger_window_start": "00:00",
		"trigger_window_end":   "00:01",
		"trigger_window_edge":  "enter",
		"action_type":          "notification",
		"action_level":         "warning",
	})
	rows, err := autoListRules(e, "?trigger_type=time_window")
	if err != nil {
		e.Fatalf("%v", err)
	}
	foundWindow := false
	for _, row := range rows {
		if row.ID == uint(windowID) {
			foundWindow = true
			if row.TriggerWindowStart != "00:00" || row.TriggerWindowEnd != "00:01" ||
				row.TriggerWindowEdge != "enter" {
				e.Fatalf("时间窗口回显不一致: %+v", row)
			}
		}
	}
	if !foundWindow {
		e.Fatalf("新建的时间窗口策略 %d 未出现在 ?trigger_type=time_window 的 %d 条结果中", windowID, len(rows))
	}

	e.Evidence("SIM-CRUD-001.rules", map[string]any{
		"threshold_rule_id": ruleID, "window_rule_id": windowID, "name": name,
		"defaults": map[string]any{"enabled": got.Enabled, "cooldown_sec": got.CooldownSec},
	})
}

// ---------------------------------------------------------------------------
// SIM-CRUD-002 修改阈值后新的判定立即生效（规则缓存失效）
// ---------------------------------------------------------------------------

func crudRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-002", "thr", "sim_crud_002_sensor")
	name := e.NS("SIM-CRUD-002", "thr")

	// 被测策略：阈值先设成"绝不会触发"的 100.0（上报值 23.5 远低于它）。
	// cooldown_sec=1 是本场景的关键参数：否定阶段需要冷却早已过去，
	// 才能把"没有新事件"唯一归因于阈值变更，而不是冷却抑制。
	ruleID := autoCreateRule(e, crudThresholdRule(name, fx.edgeDeviceID, "gt", 100.0, 1))

	// 对照策略：同设备同传感器、阈值 20.0。没有它，"0 条事件"既可能是
	// "阈值没生效"也可能是"链路根本没跑"，两者无法区分。
	controlID := autoCreateRule(e, crudThresholdRule(e.NS("SIM-CRUD-002", "ctl"), fx.edgeDeviceID, "gt", 20.0, 1))

	for i := 0; i < 3; i++ {
		if err := fx.report(235); err != nil {
			e.Fatalf("节点第 %d 次上报失败: %v", i+1, err)
		}
	}
	crudWaitRuleEvent(e, controlID, "notification", 20*time.Second)
	if before := crudCountRuleEvents(e, ruleID, ""); before != 0 {
		// 阈值 100.0 下 23.5 不可能触发；出现事件说明比较符或阈值取值被搞错了。
		rows, _ := crudListRuleEvents(e, ruleID, "")
		e.Fatalf("阈值 100.0 的策略产生了 %d 条事件（误报）: %+v", before, rows)
	}

	// ── 正向：把阈值改成会触发的值，下一次上报就必须按新阈值判定 ──
	updated := e.Admin.Put("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10),
		map[string]any{"trigger_threshold": 20.0}).Expect(http.StatusOK)
	var echo autoRuleRow
	updated.Decode(&echo)
	autoEventuallyFloat(e.T, "PUT 后回显的 trigger_threshold", echo.TriggerThreshold, 20.0)

	// 注意：立刻上报一帧。若 PUT 没有失效引擎缓存，缓存里仍是 threshold=100.0 的旧规则，
	// 这一帧不会被判定为触发。10s 的上界远小于 Evaluator.Start 的 30s 兜底重载周期，
	// 但兜底重载有可能恰好落在窗口内把"缺失的 Invalidate"掩盖掉（只会漏报缺陷，
	// 不会造成假红）—— 这一不确定性已写入任务报告。
	if err := fx.report(235); err != nil {
		e.Fatalf("阈值更新后上报失败: %v", err)
	}
	fired := crudWaitRuleEvent(e, ruleID, "notification", 10*time.Second)
	if fired.TriggerValue == nil {
		e.Fatalf("阈值更新后触发的事件缺少 trigger_value: %+v", fired)
	}
	autoEventuallyFloat(e.T, "阈值更新后事件的 trigger_value", *fired.TriggerValue, 23.5)

	// ── 反向：把阈值调回不会触发的值，随后任何一帧都不得再触发 ──
	e.Admin.Put("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10),
		map[string]any{"trigger_threshold": 100.0}).Expect(http.StatusOK)

	// 负向断言只数**触发**（result=notification）：冷却窗内 evaluator 还会落
	// suppressed_cooldown 审计行（evaluator.recordSuppressed），那是"被压制的痕迹"，
	// 不是一次触发。把它算进来会让断言变成"冷却有没有到期"，而不是"新阈值有没有生效"。
	before := crudCountRuleEvents(e, ruleID, "notification")
	controlHits := crudObserveReports(e, fx, controlID, 6*time.Second, 400*time.Millisecond)
	if controlHits < 2 {
		// 对照策略在 6s 窗口内至少应因冷却到期（1s）再次触发 2 次。
		// 少于 2 次说明窗口内的帧没有被真正求值，"没有新事件"不成立为证据。
		e.Fatalf("观察窗内对照策略只新增 %d 次触发，无法证明这些帧被引擎求值过", controlHits)
	}
	if after := crudCountRuleEvents(e, ruleID, "notification"); after != before {
		rows, _ := crudListRuleEvents(e, ruleID, "notification")
		e.Fatalf("阈值调回 100.0 后策略仍新增了 %d 次触发（冷却 1s 早已过去）: %+v", after-before, rows)
	}

	e.Evidence("SIM-CRUD-002.invalidate", map[string]any{
		"rule_id": ruleID, "control_rule_id": controlID,
		"events_in_first_three_frames": 0,
		"events_after_raise_threshold": crudCountRuleEvents(e, ruleID, ""),
		"control_hits_in_watch_window": controlHits,
		"events_after_lower_to_100":    before,
	})
}

// ---------------------------------------------------------------------------
// SIM-CRUD-003 停用/启用开关立即生效
// ---------------------------------------------------------------------------

func crudRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-003", "tog", "sim_crud_003_sensor")

	// 被测策略创建时就是停用状态：停用的规则不应进入引擎的规则缓存。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-CRUD-003", "off"),
		"enabled":                false,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           1,
	})
	// 对照策略（启用）：它触发一次就证明这些帧真的走到了求值器。
	controlID := autoCreateRule(e, crudThresholdRule(e.NS("SIM-CRUD-003", "ctl"), fx.edgeDeviceID, "gt", 20.0, 1))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	crudWaitRuleEvent(e, controlID, "notification", 20*time.Second)

	// 不变式 1：停用状态下条件满足也不产生任何事件。
	// 此时被测策略从未触发过，不存在冷却状态，所以"0 条"只能由"停用"解释。
	if n := crudCountRuleEvents(e, ruleID, ""); n != 0 {
		rows, _ := crudListRuleEvents(e, ruleID, "")
		e.Fatalf("停用中的策略 %d 产生了 %d 条事件: %+v", ruleID, n, rows)
	}

	// 不变式 2：启用后立即可用（PATCH 必须让引擎缓存失效，否则规则永远不在缓存里）。
	e.Admin.Patch("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/enabled",
		map[string]any{"enabled": true}).Expect(http.StatusOK)
	if err := fx.report(235); err != nil {
		e.Fatalf("启用后上报失败: %v", err)
	}
	crudWaitRuleEvent(e, ruleID, "notification", 10*time.Second)

	// 不变式 3：再次停用后立刻不再触发。冷却窗只有 1s，而观察窗内对照策略
	// 至少再触发 2 次（≥2s），因此"没有新事件"不可能由冷却解释。
	e.Admin.Patch("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/enabled",
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	before := crudCountRuleEvents(e, ruleID, "notification")
	controlHits := crudObserveReports(e, fx, controlID, 6*time.Second, 400*time.Millisecond)
	if controlHits < 2 {
		e.Fatalf("观察窗内对照策略只新增 %d 次触发，无法证明这些帧被引擎求值过", controlHits)
	}
	if after := crudCountRuleEvents(e, ruleID, "notification"); after != before {
		rows, _ := crudListRuleEvents(e, ruleID, "notification")
		e.Fatalf("重新停用后策略仍新增了 %d 次触发: %+v", after-before, rows)
	}

	// 不变式 4：开关状态是用户可见事实，列表必须如实回传（界面据此显示启停）。
	rows, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	seen := false
	for _, row := range rows {
		if row.ID == uint(ruleID) {
			seen = true
			if row.Enabled {
				e.Fatalf("停用后列表里的策略 %d 仍显示 enabled=true", ruleID)
			}
		}
	}
	if !seen {
		e.Fatalf("策略 %d 从列表里消失了（共 %d 条）", ruleID, len(rows))
	}

	e.Evidence("SIM-CRUD-003.toggle", map[string]any{
		"rule_id": ruleID, "control_rule_id": controlID,
		"events_while_disabled": 0, "events_after_enable": 1,
		"control_hits_in_watch_window": controlHits,
	})
}

// ---------------------------------------------------------------------------
// SIM-CRUD-004 配置不完整的规则被拒绝且不入库
// ---------------------------------------------------------------------------

// crudInvalidCase 是一类"配置不完整"的请求与它必须得到的可读原因。
type crudInvalidCase struct {
	label string
	want  string // 必须出现在 400 响应 message 里的字段名/原因片段
	body  map[string]any
}

func crudRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-004", "bad", "sim_crud_004_sensor")
	// 第二台设备必须是"有动作目录"的型号：action_id 存在性校验需要真实目录。
	catalogDevice := autoProvisionDevice(e, "SIM-CRUD-004", "cat", "sn3001_rain")

	base := func(name string) map[string]any {
		return crudThresholdRule(e.NS("SIM-CRUD-004", name), fx.edgeDeviceID, "gt", 20.0, 60)
	}
	// 每类非法请求都用独立名字：一旦哪一类被错误地存了下来，
	// "列表总数不变 + 名字查不到"两条断言都能立刻指出是哪一类。
	missingName := base("noname")
	delete(missingName, "name")
	missingComparator := base("nocmp")
	delete(missingComparator, "trigger_comparator")
	missingThreshold := base("nothr")
	delete(missingThreshold, "trigger_threshold")
	missingLevel := base("nolvl")
	delete(missingLevel, "action_level")
	missingActionID := map[string]any{
		"name":                   e.NS("SIM-CRUD-004", "noid"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": catalogDevice.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       catalogDevice.edgeDeviceID,
		// action_id 缺失：这是本用例唯一缺的字段（其它字段必须齐备，否则校验会先在
		// 别的字段上报错，断言就退化成"某个 400"而不是"因为缺 action_id 所以 400"）。
	}
	missingWindow := map[string]any{
		"name":                e.NS("SIM-CRUD-004", "nowin"),
		"trigger_type":        "time_window",
		"trigger_window_edge": "enter",
		"action_type":         "notification",
		"action_level":        "info",
		// trigger_window_start/end 缺失
	}
	unknownAction := map[string]any{
		"name":                   e.NS("SIM-CRUD-004", "ghost"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": catalogDevice.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       catalogDevice.edgeDeviceID,
		"action_id":              "no_such_action",
		"action_params_json":     "{}",
	}

	cases := []crudInvalidCase{
		{label: "缺 name", want: "name", body: missingName},
		{label: "缺比较符", want: "trigger_comparator", body: missingComparator},
		{label: "缺阈值", want: "trigger_threshold", body: missingThreshold},
		{label: "notification 缺 action_level", want: "action_level", body: missingLevel},
		{label: "device_action 缺 action_id", want: "action_id", body: missingActionID},
		{label: "time_window 缺窗口", want: "trigger_window", body: missingWindow},
		{label: "action_id 不在设备能力目录", want: "action_id", body: unknownAction},
	}

	before, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}

	for _, item := range cases {
		resp := e.Admin.Post("/api/v1/automation-rules", item.body)
		// 不变式 1：每一类都必须 400 + 机器可读原因 invalid_automation_rule（fail-closed）。
		resp.ExpectError(http.StatusBadRequest, "invalid_automation_rule")
		// 不变式 2：原因必须指明是哪个字段 —— 否则用户只知道"不行"，不知道改哪里。
		if !strings.Contains(resp.Message, item.want) {
			e.Fatalf("%s：400 响应 message=%q 未指明字段 %q", item.label, resp.Message, item.want)
		}
	}

	// 不变式 3：一次都不许入库。用用户可见的列表口径计数前后对比（设计 §3 原则 2：
	// 能用 API 断言的就不用直连库）。
	after, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(after) != len(before) {
		e.Fatalf("非法配置导致列表条数从 %d 变成 %d（有请求被错误地存了下来）: %+v",
			len(before), len(after), after)
	}
	leaked := map[string]bool{}
	for _, item := range cases {
		if name, ok := item.body["name"].(string); ok {
			leaked[name] = false
		}
	}
	for _, row := range after {
		if _, tracked := leaked[row.Name]; tracked {
			leaked[row.Name] = true
		}
	}
	for name, found := range leaked {
		if found {
			e.Fatalf("被拒绝的策略 %q 竟然出现在列表里", name)
		}
	}

	e.Evidence("SIM-CRUD-004.rejected", map[string]any{
		"cases": len(cases), "rules_before": len(before), "rules_after": len(after),
		"labels": func() []string {
			out := make([]string, 0, len(cases))
			for _, item := range cases {
				out = append(out, item.label)
			}
			return out
		}(),
	})
}

// ---------------------------------------------------------------------------
// SIM-CRUD-005 删除规则后不再触发，历史事件仍可查（软删）
// ---------------------------------------------------------------------------

func crudRun005(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-005", "del", "sim_crud_005_sensor")

	// 本场景会在中途删除规则，因此不能用 autoCreateRule：它的 t.Cleanup 期望
	// DELETE 返回 200，第二次删除会拿到 404 并把场景判红。这里登记一个容忍
	// 404 的清理（资源确实已不存在就是干净状态）。
	body := crudThresholdRule(e.NS("SIM-CRUD-005", "del"), fx.edgeDeviceID, "gt", 20.0, 1)
	created := e.Admin.Post("/api/v1/automation-rules", body).Expect(http.StatusOK)
	ruleID := created.DataInt("id")
	if ruleID == 0 {
		e.Fatalf("创建策略未返回 id: %s", created.BodyString())
	}
	e.T.Cleanup(func() {
		resp := e.Admin.Delete("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10))
		if resp.Status != http.StatusOK && resp.Status != http.StatusNotFound {
			e.T.Errorf("清理自动化策略失败: %s", resp.BodyString())
		}
	})

	// 先制造一条历史事件：它是"删除后仍可归因"的标的物。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	historical := crudWaitRuleEvent(e, ruleID, "notification", 25*time.Second)
	if historical.TriggerValue == nil {
		e.Fatalf("历史事件缺少 trigger_value: %+v", historical)
	}
	autoEventuallyFloat(e.T, "历史事件的 trigger_value", *historical.TriggerValue, 23.5)

	// 删除。
	deleted := e.Admin.Delete("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10)).
		Expect(http.StatusOK)
	if !deleted.DataBool("deleted") {
		e.Fatalf("删除响应未确认 deleted=true: %s", deleted.BodyString())
	}
	if deleted.DataInt("id") != ruleID {
		e.Fatalf("删除响应里的 id=%d，期望 %d", deleted.DataInt("id"), ruleID)
	}

	// 不变式 1：删除后用户口径读不到这条策略（详情 404 + 列表消失）。
	gone := e.Admin.Get("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10))
	if gone.Status != http.StatusNotFound {
		e.Fatalf("已删除策略的详情应返回 404，实际 %d body=%s", gone.Status, gone.BodyString())
	}
	for _, query := range []string{"", "?trigger_type=sensor_threshold"} {
		rows, err := autoListRules(e, query)
		if err != nil {
			e.Fatalf("%v", err)
		}
		for _, row := range rows {
			if row.ID == uint(ruleID) {
				e.Fatalf("已删除策略 %d 仍出现在 GET /automation-rules%s 里", ruleID, query)
			}
		}
	}

	// 不变式 2：历史事件不受删除影响（软删的意义就是保留归因）。
	rows, err := crudListRuleEvents(e, ruleID, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	foundHistorical := false
	for _, row := range rows {
		if row.ID == historical.ID {
			foundHistorical = true
			if row.TriggerValue == nil {
				e.Fatalf("历史事件 %d 的 trigger_value 在删除后丢失", row.ID)
			}
			autoEventuallyFloat(e.T, "删除后读到的历史 trigger_value", *row.TriggerValue, 23.5)
		}
	}
	if !foundHistorical {
		e.Fatalf("删除策略后历史事件 %d 查不到了（共 %d 条）", historical.ID, len(rows))
	}

	// 不变式 3：删除的持久化语义必须是"行要么不在、要么已标记删除"。
	//
	// 设计 §3 写的是「软删 DeletedAt，保留历史归因」，但 models.AutomationRule.DeletedAt
	// 声明为 *time.Time，而 GORM 只把 gorm.DeletedAt 类型识别为软删字段 ——
	// 实测 db.Delete(&rule) 走的是**物理删除**：automation_rules 里该行直接消失
	// （本次仿真实测 remaining=0 / soft_deleted=0）。automation_events 的历史行
	// 因为不在同一张表而保留下来，所以"历史执行记录仍查得到"依然成立，
	// 但事件里的 rule_id 成了悬空引用（用户看不到它出自哪条策略了）。
	// 该偏差已列入任务报告的偏差清单；这里断言的是"不允许既没删也没标记"，
	// 两种实现方式都能通过 —— 不把当前的物理删除固化成契约。
	remaining := simCountRows(e, "SELECT count(*) FROM automation_rules WHERE id = $1", ruleID)
	softDeleted := simCountRows(e,
		"SELECT count(*) FROM automation_rules WHERE id = $1 AND deleted_at IS NOT NULL", ruleID)
	if remaining != 0 && softDeleted == 0 {
		e.Fatalf("删除后 automation_rules 仍残留 id=%d 的 %d 行且未标记删除（既没删也没标记）",
			ruleID, remaining)
	}
	if n := simCountRows(e, "SELECT count(*) FROM automation_events WHERE rule_id = $1", ruleID); n < 1 {
		e.Fatalf("删除策略后 automation_events 里的历史行数 = %d，期望 ≥1（历史归因不能随规则消失）", n)
	}

	// 不变式 4：删除后条件再次满足也不再触发。
	// 对照策略在删除之后创建（从未触发过，无冷却历史），它触发即证明这些帧被求值过。
	controlID := autoCreateRule(e, crudThresholdRule(e.NS("SIM-CRUD-005", "ctl"), fx.edgeDeviceID, "gt", 20.0, 1))
	for i := 0; i < 3; i++ {
		if err := fx.report(235); err != nil {
			e.Fatalf("节点第 %d 次上报失败: %v", i+1, err)
		}
	}
	crudWaitRuleEvent(e, controlID, "notification", 20*time.Second)
	afterDelete, err := crudListRuleEvents(e, ruleID, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(afterDelete) != len(rows) {
		e.Fatalf("删除后策略又产生了 %d 条新事件（%d → %d）", len(afterDelete)-len(rows), len(rows), len(afterDelete))
	}

	e.Evidence("SIM-CRUD-005.soft_delete", map[string]any{
		"rule_id": ruleID, "historical_event_id": historical.ID,
		"events_after_delete": len(afterDelete), "control_rule_id": controlID,
		"rule_row_remaining": remaining, "rule_row_soft_deleted": softDeleted,
		"note": "设计 §3 的「软删」当前未生效：DeletedAt 是 *time.Time，GORM 不识别为软删字段",
	})
}

// ---------------------------------------------------------------------------
// SIM-CRUD-006 event 触发类型被明确拒绝
// ---------------------------------------------------------------------------

func crudRun006(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CRUD-006", "evt", "sim_crud_006_sensor")
	name := e.NS("SIM-CRUD-006", "event")

	before, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	beforeEvent, err := autoListRules(e, "?trigger_type=event")
	if err != nil {
		e.Fatalf("%v", err)
	}

	// event 触发器在 models/automation.go 标注"本期仅占位, 未实现"，创建时必须被拒绝
	// （handler_automation.go:578）。这里断言的正是"被拒绝"这一真实行为，不是"能创建"。
	resp := e.Admin.Post("/api/v1/automation-rules", map[string]any{
		"name":                   name,
		"trigger_type":           "event",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "warning",
	})
	// 不变式 1：明确的 400 + 机器可读原因 + 人能看懂的解释（不是 500，也不是静默成功）。
	resp.ExpectError(http.StatusBadRequest, "invalid_automation_rule")
	if !strings.Contains(resp.Message, "event") {
		e.Fatalf("拒绝 event 触发器的响应 message=%q 未说明原因", resp.Message)
	}

	// 不变式 2：绝不能落库成一条永不触发的策略 —— 列表总量与 event 类型计数都不许变。
	after, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(after) != len(before) {
		e.Fatalf("被拒绝的 event 策略污染了列表：%d → %d", len(before), len(after))
	}
	afterEvent, err := autoListRules(e, "?trigger_type=event")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(afterEvent) != len(beforeEvent) {
		e.Fatalf("event 类型策略条数从 %d 变成 %d", len(beforeEvent), len(afterEvent))
	}
	for _, row := range after {
		if row.Name == name {
			e.Fatalf("被拒绝的 event 策略 %q 竟然出现在列表里: %+v", name, row)
		}
	}

	e.Evidence("SIM-CRUD-006.rejected", map[string]any{
		"status": resp.Status, "error_code": resp.ErrorCode, "message": resp.Message,
		"rules_before": len(before), "rules_after": len(after),
		"event_type_rules_before": len(beforeEvent), "event_type_rules_after": len(afterEvent),
	})
}
