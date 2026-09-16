//go:build simulation

// 场景目录 · SIM-WIND 时间窗口（设计 §4 SIM-WIND-001..005）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单）。
// 设计依据：docs/设计/自动化策略引擎方案.md（时间窗口触发器）。
//
// 本域守护的不变量（每条断言都对应对应其中之一）：
//  1. enter 是「进入窗口」的边沿：窗口内只触发一次，不是每个 tick 都刷
//     （evaluator.go:274-278 shouldTrigger = inside && (!exists || !wasInside)）；
//  2. exit 是「离开窗口」的边沿：只有「上一次在窗口内、这一次不在」才触发
//     （evaluator.go:279-283）；
//  3. inside 在窗口内每个 tick 都参与求值，但受 CooldownSec 抑制 ——
//     抑制时不落事件（与 sensor_threshold 的审计节流不同，planner 不参与此路径）；
//  4. 窗口判定按本地时钟的 [start, end) 半开区间，窗口外的规则绝不触发；
//  5. start > end 表示跨零点窗口（[start, 24:00) ∪ [00:00, end)），
//     即 22:00–06:00 这种真实配置必须判定正确。
//
// 时间源与被测对象：窗口求值只由 backend 的 StartWindowTicker 驱动（60s 周期，
// evaluator.go:207），因此本域每个场景都必须等真实 tick，超时按「至少跨过一个
// tick」留足（95s+）。所有等待都走 Eventualy，不用 sleep 做断言同步。
//
// 复用：autoCreateRule / autoListEvents / autoListRules，以及 trig.go 的运行时
// 工具与 cond.go 的写法（同包跨文件调用；框架 §4.1 只约束声明名前缀）。
package catalog

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-WIND-001",
		Title:  "进入设定时段时策略执行一次（进入边沿）",
		Domain: DomainWIND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-WIND-001；docs/设计/自动化策略引擎方案.md（enter 边沿）",
		Run:    windRun001,
	})
	Register(Scenario{
		ID:     "SIM-WIND-002",
		Title:  "离开设定时段时策略执行一次（离开边沿）",
		Domain: DomainWIND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-WIND-002；docs/设计/自动化策略引擎方案.md（exit 边沿）",
		Run:    windRun002,
	})
	Register(Scenario{
		ID:     "SIM-WIND-003",
		Title:  "时段内按冷却节奏执行，不会每个周期都刷一次",
		Domain: DomainWIND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-WIND-003；docs/设计/自动化策略引擎方案.md（inside + CooldownSec）",
		Run:    windRun003,
	})
	Register(Scenario{
		ID:     "SIM-WIND-004",
		Title:  "时段外的策略不会触发（窗口边界正确）",
		Domain: DomainWIND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-WIND-004；docs/设计/自动化策略引擎方案.md（窗口边界）",
		Run:    windRun004,
	})
	Register(Scenario{
		ID:     "SIM-WIND-005",
		Title:  "跨零点时段（如 22:00–06:00）的判定正确",
		Domain: DomainWIND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-WIND-005；docs/设计/自动化策略引擎方案.md（跨零点窗口）",
		Run:    windRun005,
	})
}

// ---------------------------------------------------------------------------
// 时间窗工具（wind 前缀）
// ---------------------------------------------------------------------------

// windMinutes 返回某时刻在当日的分钟数（本地时钟，与后端求值用同一个 time.Now 语义）。
func windMinutes(t time.Time) int { return t.Hour()*60 + t.Minute() }

// windHHMM 把「当日分钟数」格式化为产品要求的 "HH:MM"（自动按 1440 取模）。
func windHHMM(minutes int) string {
	normalized := ((minutes % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", normalized/60, normalized%60)
}

// windWindowAround 生成一个以当前时刻为锚的窗口（"HH:MM"），
// startDelta/endDelta 是相对当前时刻的分钟偏移。
func windWindowAround(now time.Time, startDelta, endDelta int) (string, string) {
	base := windMinutes(now)
	return windHHMM(base + startDelta), windHHMM(base + endDelta)
}

// windContains 按设计冻结的语义独立判断「当前时刻是否在窗口内」：
// start == end 视为全天；start < end 为 [start, end)；start > end 为跨零点
// [start, 24:00) ∪ [00:00, end)。刻意不调用产品内部函数（不可导出），
// 而是按文档语义独立写出，供**构造前提自检**使用 —— 断言的对象始终是引擎行为。
func windContains(start, end string, now time.Time) (bool, error) {
	startAt, err := time.Parse("15:04", start)
	if err != nil {
		return false, fmt.Errorf("解析窗口起点 %q 失败: %w", start, err)
	}
	endAt, err := time.Parse("15:04", end)
	if err != nil {
		return false, fmt.Errorf("解析窗口终点 %q 失败: %w", end, err)
	}
	startMin := startAt.Hour()*60 + startAt.Minute()
	endMin := endAt.Hour()*60 + endAt.Minute()
	nowMin := windMinutes(now)
	switch {
	case startMin == endMin:
		return true, nil // 全天窗口
	case startMin < endMin:
		return nowMin >= startMin && nowMin < endMin, nil
	default:
		return nowMin >= startMin || nowMin < endMin, nil // 跨零点
	}
}

// windAssertContains 把「构造出来的窗口到底含不含当前时刻」先钉死。
// 这是**前提自检**：构造错了会让后面的「没触发」退化成同义反复。
func windAssertContains(e *harness.Env, label, start, end string, now time.Time, want bool) {
	e.T.Helper()
	got, err := windContains(start, end, now)
	if err != nil {
		e.Fatalf("%s: %v", label, err)
	}
	if got != want {
		e.Fatalf("%s: 构造的窗口 %s–%s 对当前时刻 %s 的判定为 %v，期望 %v",
			label, start, end, now.Format("15:04"), got, want)
	}
}

// windAssertCrossMidnight 前提自检：窗口必须是跨零点窗口（起点晚于终点）。
func windAssertCrossMidnight(e *harness.Env, label, start, end string) {
	e.T.Helper()
	startAt, err := time.Parse("15:04", start)
	if err != nil {
		e.Fatalf("%s: 解析窗口起点 %q 失败: %v", label, start, err)
	}
	endAt, err := time.Parse("15:04", end)
	if err != nil {
		e.Fatalf("%s: 解析窗口终点 %q 失败: %v", label, end, err)
	}
	if !startAt.After(endAt) {
		e.Fatalf("%s: 期望跨零点窗口（起点晚于终点），实际 %s–%s", label, start, end)
	}
}

// 跨零点窗口（起点 > 终点）的判定语义与产品一致（automation 的 isInWindow；
// 本文件 windContains 是它的独立复写）：
//
//	窗口 = [start, 24:00) ∪ [00:00, end)
//	now 在窗口内   ⇔ nowMin >= start || nowMin < end
//	now 不在窗口内 ⇔ end <= nowMin < start            （补集是空档 [end, start)）
//
// windFindCrossWindow 直接**构造**满足请求的窗口（不再搜索）：inside=true 返回
// 「当前时刻在其中」的那个，inside=false 返回「当前时刻不在其中」的那个。
//
// 为什么放弃整点搜索：旧实现把 startOffset 与 width 都以 60 分钟步进，搜索空间
// 覆盖不到全部 1440 个分钟点 ——
//   - now ∈ 11:00–11:59：「含 now」无解。傍晚段要求宽度 >= 1440-now >= 13h00m；
//     清晨段要求 end > now 且 start = end+1440-宽度 <= 1439，即宽度 >= end+1 >= 12h01m。
//     两条路都超出旧搜索 12h 的宽度上限。
//   - now ∈ 23:00–23:59：「不含 now」无解。要求 now < start，而整点起点最大 23:00。
//
// ⇒ 每天这两个小时 SIM-WIND-005 必然变红（11:00–11:59 与 23:00–23:59），与分页缺陷无关。
//
// 构造分支的完备性与语义由 wind_satisfiability_test.go 穷举 1440 分钟长期守护。
func windFindCrossWindow(now time.Time, inside bool) (string, string, bool) {
	nowMin := windMinutes(now)
	if inside {
		start, end := windCrossWindowInside(nowMin)
		return windHHMM(start), windHHMM(end), true
	}
	if windCrossWindowUnsolvable(nowMin) {
		return "", "", false
	}
	start, end := windCrossWindowOutside(nowMin)
	return windHHMM(start), windHHMM(end), true
}

// windCrossWindowUnsolvable 判定「不含当前时刻的跨零点窗口」是否**数学上无解**。
//
// 判据来源是上面的等价式，不是经验：不含 now ⇔ end <= nowMin < start，
// 而 start <= 1439（当天最后一分钟），故必须 nowMin < start <= 1439，即 nowMin <= 1438。
// 唯一无解的分钟是 nowMin == 1439（23:59）—— 那一分钟落在**任何**跨零点窗口内
// （start <= 1439 <= nowMin 使 nowMin >= start 成立，或 end >= 1 > ... 的晨间段判定，
// 两条析取必有一条为真）。
//
// 场景只允许在这一分钟跳过 out 分支，且必须把依据写进证据（见 windRun005）。
func windCrossWindowUnsolvable(nowMin int) bool { return nowMin == 1439 }

// windCrossWindowInside 构造一个含 nowMin 的跨零点窗口：14 小时宽、整点对齐。
//
//	H >= 11：now 落在傍晚段 —— start = H:00 <= now，窗口一直延到 24:00；
//	H <= 10：now 落在清晨段 —— 固定 23:00–13:00（now < 13:00 必在该段内）。
//
// 为什么 H >= 11 取 14h（840 分钟）而不是 12h：傍晚段要 start <= now 且
// start + 宽度 >= 1440。H == 11 时 start = 11:00 需要宽度 >= 780（13h），
// 取 840 后 end = start-600 = 01:00 > 0，晨间段非空、窗口不退化。
//
// 稳定性（场景创建规则后要等真实 60s ticker 才求值）：两个分支都保证 now 之后
// 至少还含 60 分钟 —— 傍晚段跨零点后由晨间段接续（end >= 01:00）。这条不变量的
// 完整覆盖见 wind_satisfiability_test.go 的 now+60 断言。
func windCrossWindowInside(nowMin int) (start, end int) {
	if h := nowMin / 60; h >= 11 {
		return 60 * h, 60*h - 600
	}
	return 23 * 60, 13 * 60
}

// windCrossWindowOutside 构造一个不含 nowMin 的跨零点窗口：12 小时宽、整点对齐。
// 补集空档 [end, start) 必须含 nowMin，且 start 要明显晚于 now（空档不能在下一个
// ticker tick 就失效），因此：
//
//	H <= 9       ：end = H:00、start = H+12:00（空档左边界只能落在当天，
//	               否则 start < end，窗口就不再跨零点）。H == 0 时 end = 00:00，
//	               晨间段为空（当天已无更早的整点），窗口仍是 start > end 的跨零点分支。
//	10 <= H <= 21：start = H+2:00、end = start-12h（空档上边界距 now 至少 61 分钟）。
//	H >= 22      ：当天已无更晚的整点，退化为 23:59–11:59（空档 [11:59, 23:59)，
//	               仍覆盖 22:00–23:58）。23:59 由 windCrossWindowUnsolvable 提前挡掉。
//
// 调用前提：!windCrossWindowUnsolvable(nowMin)。
func windCrossWindowOutside(nowMin int) (start, end int) {
	h := nowMin / 60
	switch {
	case h <= 9:
		end = 60 * h
		return end + 720, end
	case h <= 21:
		start = 60 * (h + 2)
		return start, start - 720
	default:
		return 23*60 + 59, 11*60 + 59
	}
}

// windNightWindowContains 用显式小时算术判断当前时刻是否落在字面的
// 22:00–06:00 跨零点窗口内（场景 005 的独立判据）。
func windNightWindowContains(now time.Time) bool {
	nowMin := windMinutes(now)
	return nowMin >= 22*60 || nowMin < 6*60
}

// windCreateWindowRule 创建一条时间窗口策略（time_window + notification）。
// 窗口策略不需要传感器，也不带 trigger_edge_device_id —— 它由时钟驱动。
func windCreateWindowRule(e *harness.Env, name, start, end, edge string, cooldownSec int) int64 {
	e.T.Helper()
	return autoCreateRule(e, map[string]any{
		"name":                 name,
		"enabled":              true,
		"trigger_type":         "time_window",
		"trigger_window_start": start,
		"trigger_window_end":   end,
		"trigger_window_edge":  edge,
		"action_type":          "notification",
		"action_level":         "info",
		"cooldown_sec":         cooldownSec,
	})
}

// windWindowEvents 读某条窗口策略的全部事件（窗口事件不该带 trigger_value）。
func windWindowEvents(e *harness.Env, ruleID int64) []autoEventRow {
	e.T.Helper()
	rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
	if err != nil {
		e.Fatalf("%v", err)
	}
	return rows
}

// windTickTimeout 是「至少跨过一个 60s tick」的等待上限（ticker 相位不可知，
// 首次 tick 最坏要等满一个周期，再留 35s 给事件落库与轮询间隔）。
const windTickTimeout = 95 * time.Second

// ---------------------------------------------------------------------------
// SIM-WIND-001 进入时间窗口时执行一次（enter 边沿）
// ---------------------------------------------------------------------------

func windRun001(e *harness.Env) {
	now := time.Now()
	start, end := windWindowAround(now, -1, 20)
	windAssertContains(e, "SIM-WIND-001", start, end, now, true)

	ruleID := windCreateWindowRule(e, e.NS("SIM-WIND-001", "enter"), start, end, "enter", 300)

	rows := trigWaitResult(e, ruleID, "notification", 1, windTickTimeout)
	// 时间窗口触发不是阈值触发：事件不该带 trigger_value（带了说明触发源被张冠李戴）。
	if rows[0].TriggerValue != nil {
		e.Fatalf("时间窗口事件不应带 trigger_value，实际 %v：%+v", *rows[0].TriggerValue, rows[0])
	}
	if rows[0].TriggerSource != "auto" {
		e.Fatalf("窗口触发的事件 trigger_source=%q，期望 auto", rows[0].TriggerSource)
	}

	e.Evidence("SIM-WIND-001.enter", map[string]any{
		"rule_id": ruleID, "window": start + "–" + end, "edge": "enter",
		"event_id": rows[0].ID, "triggered_at": rows[0].TriggeredAt,
	})
}

// ---------------------------------------------------------------------------
// SIM-WIND-002 离开时间窗口时执行一次（exit 边沿）
// ---------------------------------------------------------------------------

func windRun002(e *harness.Env) {
	now := time.Now()
	start, end := windWindowAround(now, -1, 20)
	windAssertContains(e, "SIM-WIND-002 进入窗口", start, end, now, true)

	// 两条策略起手都在同一个「包含当前时刻」的窗口里：
	//   enter 策略负责证明「第一次 tick 已经发生、且当时两条规则都在窗口内」；
	//   exit 策略此刻不触发（它要的是「上一次在窗口内、这一次不在」）。
	enterID := windCreateWindowRule(e, e.NS("SIM-WIND-002", "enter"), start, end, "enter", 300)
	exitID := windCreateWindowRule(e, e.NS("SIM-WIND-002", "exit"), start, end, "exit", 300)

	trigWaitResult(e, enterID, "notification", 1, windTickTimeout)
	// 仍在窗口内：exit 边沿不能触发。
	trigAssertNoResult(e, "仍在窗口内时的离开边沿策略", exitID, "notification", 2*time.Second)

	// 把 exit 策略的窗口挪到一个**一定不含当前时刻**的区间 —— 这就是「离开窗口」。
	// 过去时段的窗口在任何解释下都不含当前时刻（非跨零点时 end < now；跨零点时
	// 两段分别是 [start,24:00) 与 [00:00,end)，而现在既不 ≥ start 也不 < end）。
	leaveAt := time.Now()
	outStart, outEnd := windWindowAround(leaveAt, -420, -300)
	windAssertContains(e, "SIM-WIND-002 离开窗口", outStart, outEnd, leaveAt, false)
	exitText := strconv.FormatInt(exitID, 10)
	e.Admin.Put("/api/v1/automation-rules/"+exitText, map[string]any{
		"trigger_window_start": outStart,
		"trigger_window_end":   outEnd,
	}).Expect(http.StatusOK)

	// 下一次 tick：上一次在窗口内、这一次不在 → exit 触发一次。
	rows := trigWaitResult(e, exitID, "notification", 1, windTickTimeout)
	exitAt := trigEventTime(e, rows[0])
	// 触发时刻必须晚于「窗口被挪走」的那一刻：否则这条事件可能只是窗口仍在
	// 原始区间时的残留，根本证明不了离开边沿。这是本场景唯一的时序断言。
	if !exitAt.After(leaveAt) {
		e.Fatalf("离开边沿的触发时刻 %s 早于窗口被挪走的时刻 %s，无法归因到「离开窗口」",
			exitAt.Format(time.RFC3339Nano), leaveAt.Format(time.RFC3339Nano))
	}

	// enter 策略仍在窗口内：它只有第一次那一条事件（边沿不是每 tick 刷新）。
	finalEnter := windWindowEvents(e, enterID)
	if got := trigCountResult(finalEnter, "notification"); got != 1 {
		e.Fatalf("仍在窗口内的 enter 策略产生了 %d 条事件，期望恰好 1 条（边沿触发）：%+v", got, finalEnter)
	}

	e.Evidence("SIM-WIND-002.exit", map[string]any{
		"enter_rule_id": enterID, "exit_rule_id": exitID,
		"initial_window":  start + "–" + end,
		"moved_window":    outStart + "–" + outEnd,
		"window_moved_at": leaveAt.Format(time.RFC3339Nano),
		"exit_event_at":   exitAt.Format(time.RFC3339Nano),
		"enter_events":    trigCountResult(finalEnter, "notification"),
	})
}

// ---------------------------------------------------------------------------
// SIM-WIND-003 窗口内按冷却节奏执行，不会每 tick 刷一次（inside + 冷却）
// ---------------------------------------------------------------------------

func windRun003(e *harness.Env) {
	now := time.Now()
	start, end := windWindowAround(now, -1, 30)
	windAssertContains(e, "SIM-WIND-003", start, end, now, true)

	// 主体：inside 边沿 + 90s 冷却。tick 周期是 60s，因此两次执行之间必然跨过
	// 至少一个「按 tick 求值但被冷却压住」的时刻。
	subjectID := windCreateWindowRule(e, e.NS("SIM-WIND-003", "subject"), start, end, "inside", 90)
	// 探针：inside 边沿 + 1s 冷却 —— 每个 tick 必然触发一次，用它当**tick 计数器**。
	// 这样「到底发生过几个 tick」有确凿证据，而不是靠 sleep 猜（设计 §3 原则 3）。
	probeID := windCreateWindowRule(e, e.NS("SIM-WIND-003", "tick-probe"), start, end, "inside", 1)

	// 至少执行过一次：否则下面的「间隔」断言是空断言（什么也没证明）。
	trigWaitResult(e, subjectID, "notification", 1, windTickTimeout)
	// 观察 3 个 tick（跨度约 120s）：两次执行之间至少要跨过 90s 冷却。
	// 探针不参与主体断言，只作为「tick 确实发生过几次」的独立时钟。
	probeRows := trigWaitResult(e, probeID, "notification", 3, 3*windTickTimeout)

	// 不变式（与 tick 相位无关，因此不会假红）：**任意两次相邻执行的间隔 ≥ 冷却窗**。
	// 若 inside 变成「每个 tick 都刷」，间隔会退化成 tick 周期 60s（< 90s）；
	// 正确实现下两次执行至少相距一个冷却窗（实测约 120s = 2 个 tick）。
	// 用间隔而不是「条数恰好 1」：探针与主体规则的首次 tick 可能不在同一个 tick
	// 边界上（两条规则相隔几十毫秒创建），条数在相位偏移下会合法地变成 2。
	finalSubject := windWindowEvents(e, subjectID)
	executions := make([]time.Time, 0, len(finalSubject))
	for _, row := range finalSubject {
		if row.Result == "notification" {
			executions = append(executions, trigEventTime(e, row))
		}
	}
	if len(executions) == 0 {
		e.Fatalf("窗口内的 inside 策略在 %d 个 tick 内一次都没执行：%+v",
			trigCountResult(probeRows, "notification"), finalSubject)
	}

	// 必须按时间**升序**排列后再取相邻差。
	//
	// 服务端 listAutomationEvents 是 `Order("id DESC")`（handler_automation.go），
	// 即返回**最新在前**；直接相减会得到**负间隔**，于是"间隔 < 冷却窗"恒真 ——
	// 这条断言曾经因此假红（实测报出 `-2m0s`）。
	//
	// 为什么不改用绝对值：abs() 会把"时钟倒退/事件时间戳乱序"这类**真实异常**
	// 一并掩盖成通过。按升序排序后，任何负间隔都会以"排序后仍倒序"的形式响亮失败。
	const cooldownSec = 90
	sort.Slice(executions, func(i, j int) bool { return executions[i].Before(executions[j]) })
	for i := 1; i < len(executions); i++ {
		gap := executions[i].Sub(executions[i-1])
		if gap < 0 {
			e.Fatalf("事件时间戳在升序排序后仍出现负间隔 %s（第 %d/%d 次）：时间戳可能被篡改或时钟倒退",
				gap.Round(time.Millisecond), i, i+1)
		}
		if gap < (cooldownSec-5)*time.Second {
			e.Fatalf("inside 策略在第 %d/%d 次执行之间只隔了 %s，短于冷却窗 %ds（每个 tick 都刷）：%+v",
				i, i+1, gap.Round(time.Millisecond), cooldownSec, finalSubject)
		}
	}

	e.Evidence("SIM-WIND-003.inside_cooldown", map[string]any{
		"subject_rule_id": subjectID, "probe_rule_id": probeID,
		"window": start + "–" + end, "cooldown_sec": cooldownSec,
		"ticks_observed":      trigCountResult(probeRows, "notification"),
		"subject_executions":  len(executions),
		"first_execution_at":  executions[0].Format(time.RFC3339Nano),
		"last_execution_at":   executions[len(executions)-1].Format(time.RFC3339Nano),
		"min_gap_requirement": (cooldownSec - 5),
	})
}

// ---------------------------------------------------------------------------
// SIM-WIND-004 窗口内的规则在窗口外不触发（窗口边界正确）
// ---------------------------------------------------------------------------

func windRun004(e *harness.Env) {
	now := time.Now()
	inStart, inEnd := windWindowAround(now, -1, 20)
	outStart, outEnd := windWindowAround(now, -420, -300)
	// 前提自检：含 / 不含当前时刻，必须是我们以为的那样（构造错会让「不触发」变空断言）。
	windAssertContains(e, "SIM-WIND-004 窗口内", inStart, inEnd, now, true)
	windAssertContains(e, "SIM-WIND-004 窗口外", outStart, outEnd, now, false)

	// 对照策略：窗口含当前时刻 —— 它触发即证明 ticker 与窗口求值链路是活的。
	controlID := windCreateWindowRule(e, e.NS("SIM-WIND-004", "in-window"), inStart, inEnd, "inside", 300)
	// 被观察策略：窗口不含当前时刻 —— 一次都不能触发。
	guardedID := windCreateWindowRule(e, e.NS("SIM-WIND-004", "out-window"), outStart, outEnd, "inside", 300)

	controlRows := trigWaitResult(e, controlID, "notification", 1, windTickTimeout)
	trigAssertNoResult(e, "窗口外的策略", guardedID, "notification", 2*time.Second)

	e.Evidence("SIM-WIND-004.boundary", map[string]any{
		"control_rule_id": controlID, "in_window": inStart + "–" + inEnd,
		"guarded_rule_id": guardedID, "out_window": outStart + "–" + outEnd,
		"control_fired_at": controlRows[0].TriggeredAt, "guarded_events": 0,
	})
}

// ---------------------------------------------------------------------------
// SIM-WIND-005 跨零点的时间窗口（如 22:00–06:00）判定正确
// ---------------------------------------------------------------------------

func windRun005(e *harness.Env) {
	now := time.Now()
	nowMin := windMinutes(now)

	// 含当前时刻的跨零点窗口：**恒有解**（推导见 windFindCrossWindow 的注释）。
	// 因此这里不给「跳过」留任何余地 —— 构造不出来就是构造器的真实缺陷，必须红。
	inStart, inEnd, okIn := windFindCrossWindow(now, true)
	if !okIn {
		e.Fatalf("未能构造出含当前时刻的跨零点窗口（now=%s）—— 该分支对任意时刻都有解",
			now.Format(time.RFC3339))
	}
	// 不含当前时刻的跨零点窗口：仅 now == 23:59 数学上无解（见 windCrossWindowUnsolvable）。
	// 关键：这里是**显式裁决的不可满足边界**，不是笼统的「构造失败就跳过」——
	// 只有「构造结果」与「无解判据」不一致时才失败，两者必须严格互为否定。
	outStart, outEnd, okOut := windFindCrossWindow(now, false)
	unsolvable := windCrossWindowUnsolvable(nowMin)
	if okOut == unsolvable {
		e.Fatalf("不含当前时刻的跨零点窗口与无解判据不一致（now=%s nowMin=%d 构造成功=%v 判据无解=%v）："+
			"构造成功而判据说无解 ⇒ 判据错了；构造失败而判据说有解 ⇒ 构造器漏了分支",
			now.Format(time.RFC3339), nowMin, okOut, unsolvable)
	}

	// 前提自检：窗口必须真的是跨零点窗口（起点晚于终点），且含/不含当前时刻与请求一致。
	windAssertCrossMidnight(e, "SIM-WIND-005 含当前时刻的窗口", inStart, inEnd)
	windAssertContains(e, "SIM-WIND-005 含当前时刻的窗口", inStart, inEnd, now, true)
	if okOut {
		windAssertCrossMidnight(e, "SIM-WIND-005 不含当前时刻的窗口", outStart, outEnd)
		windAssertContains(e, "SIM-WIND-005 不含当前时刻的窗口", outStart, outEnd, now, false)
	}

	inID := windCreateWindowRule(e, e.NS("SIM-WIND-005", "cross-in"), inStart, inEnd, "enter", 300)
	// out 规则只在真的有窗口时才创建：23:59 那一分钟不存在不含 now 的跨零点窗口，
	// 造一条不可能满足语义的规则只会污染后续计数（且它必然为「在窗口内」）。
	var outID int64
	if okOut {
		outID = windCreateWindowRule(e, e.NS("SIM-WIND-005", "cross-out"), outStart, outEnd, "enter", 300)
	}
	// 字面窗口 22:00–06:00：真实用户会这么配，用它做一次独立交叉验证。
	literalID := windCreateWindowRule(e, e.NS("SIM-WIND-005", "night"), "22:00", "06:00", "enter", 300)

	nightBefore := windNightWindowContains(now)

	// ① 跨零点且此刻在其中 → 必须触发（证明 22:00–06:00 这类窗口的「在窗口内」分支）。
	inRows := trigWaitResult(e, inID, "notification", 1, windTickTimeout)
	// ② 跨零点且此刻不在其中 → 一次都不能触发（证明另一半分支不会被误判成在窗口内）。
	if okOut {
		trigAssertNoResult(e, "跨零点但此刻不在其中的策略", outID, "notification", 2*time.Second)
	} else {
		// now == 23:59：该分钟落在**任何**跨零点窗口内，因此「不含 now 的窗口」数学上不存在，
		// 断言「不触发」在这里不可能成立。显式跳过并把不可满足的依据写进证据 ——
		// 这不是「构造失败就算了」：判据本身由 wind_satisfiability_test.go 穷举验证。
		e.Evidence("SIM-WIND-005.out_window_unsolvable", map[string]any{
			"now":     now.Format("15:04"),
			"now_min": nowMin,
			"reason": "跨零点窗口不含 now ⇔ end <= now < start <= 1439 ⇒ now <= 1438；" +
				"now == 23:59 数学上不存在这样的窗口，故跳过 out 分支（不可满足边界，非搜索缺陷）",
			"unsolvable_at": "23:59",
			"skipped_rule":  "cross-out（未创建）",
		})
	}

	// ③ 字面 22:00–06:00 的独立交叉验证。
	nightAfter := windNightWindowContains(time.Now())
	switch {
	case nightBefore != nightAfter:
		// 观察期恰好横跨 22:00/06:00：窗口在观察期内翻转，两种结果都自洽。
		// 这是真实时钟的边界竞争，不是被测缺陷（边界翻转本身留给将来的专项场景）。
		e.Evidence("SIM-WIND-005.boundary_race", map[string]any{
			"night_before": nightBefore, "night_after": nightAfter,
			"note": "观察期横跨 22:00/06:00，字面窗口的判定在期内翻转，不作严格断言",
		})
	case nightBefore:
		literalRows := trigWaitResult(e, literalID, "notification", 1, windTickTimeout)
		e.Evidence("SIM-WIND-005.literal_night_inside", map[string]any{
			"literal_rule_id": literalID, "event_id": literalRows[0].ID,
			"triggered_at": literalRows[0].TriggeredAt,
		})
	default:
		trigAssertNoResult(e, "22:00–06:00 窗口外的字面策略", literalID, "notification", 2*time.Second)
		e.Evidence("SIM-WIND-005.literal_night_outside", map[string]any{
			"literal_rule_id": literalID, "events": 0,
		})
	}

	// 注意：时间窗口事件没有 trigger_value（不是阈值触发），证据里只记条数与时刻。
	//
	// cross_out_* 只在真的构造出 out 窗口时记录：23:59 那一分钟不存在这样的窗口，
	// 硬写一个窗口串或 "events: 0" 会让证据看起来比事实更完整。
	crossMidnight := map[string]any{
		"cross_in_window":   inStart + "–" + inEnd,
		"now":               now.Format("15:04"),
		"cross_in_events":   trigCountResult(inRows, "notification"),
		"cross_in_fired_at": inRows[0].TriggeredAt,
		"literal_window":    "22:00–06:00",
		"night_inside":      nightBefore,
		"cross_out_skipped": !okOut,
	}
	if okOut {
		crossMidnight["cross_out_window"] = outStart + "–" + outEnd
		crossMidnight["cross_out_events"] = 0
	}
	e.Evidence("SIM-WIND-005.cross_midnight", crossMidnight)
}
