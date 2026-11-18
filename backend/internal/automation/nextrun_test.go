package automation

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
	"ehome/backend/testutil"
)

// ─────────────────────────────────────────────────────────────────────────
// 「下次执行时间」门禁 (2026-11-17)
//
// 设计原则: 每条用例都必须能通过"改坏实现"变红 —— 否则它只是打印。
// 最硬的一条是 TestNextRunSunMatchesEvaluatorFireAt: 预测时刻必须与求值器
// **实际触发**的时刻逐秒相等, 这是防止"预测与执行漂移"的唯一结构保证。
// ─────────────────────────────────────────────────────────────────────────

// sunRuleFixture 造一条坐标/时区在规则行上的 sun_event 规则。
func sunRuleFixture(t *testing.T, kind string, offsetMin int, cooldownSec int) (*Evaluator, models.AutomationRule, *time.Location) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	tzName := "Asia/Shanghai"
	lat, lon := 39.9042, 116.4074 // 北京
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		t.Fatal(err)
	}
	rule := models.AutomationRule{
		Name: "next-run sun", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerSunKind:      kind,
		TriggerSunOffsetMin: offsetMin,
		ActionType:          models.AutomationActionNotification,
		ActionLevel:         "info",
		CooldownSec:         cooldownSec,
		Latitude:            &lat, Longitude: &lon, Timezone: &tzName,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()
	return ev, rule, tz
}

// ── 1. 事件前: 下一次就是今天的 fireAt ────────────────────────────────────
func TestNextRunSunEventBeforeEvent(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunrise, 0, 0)

	now := time.Date(2026, 6, 21, 2, 0, 0, 0, tz) // 日出前的凌晨
	eventAt, status, err := sun.SunEvent(39.9042, 116.4074, tz, now, sun.Sunrise)
	if err != nil || status != sun.StatusOK {
		t.Fatalf("前置失败: %s %v", status, err)
	}

	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunScheduled {
		t.Fatalf("status = %q, want scheduled (detail=%q)", got.Status, got.Detail)
	}
	if got.At == nil || !got.At.Equal(eventAt) {
		t.Fatalf("at = %v, want %v (今日日出)", got.At, eventAt)
	}
	if got.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, 必须是规则时区", got.Timezone)
	}
	if got.Basis != models.NextRunBasisSun {
		t.Fatalf("basis = %q, want sun", got.Basis)
	}
}

// ── 2. 事件后 (求值器已落本次日状态): 下一次是明天 ────────────────────────
func TestNextRunSunEventAfterFiredGoesTomorrow(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunrise, 0, 0)
	rec := ev.handler.(*sunRecorder)

	eventAt, _, _ := sun.SunEvent(39.9042, 116.4074, tz, time.Date(2026, 6, 21, 12, 0, 0, 0, tz), sun.Sunrise)
	// 先把首 tick 走掉, 再跨过事件时刻, 让求值器真的触发一次。
	ev.evalTimeWindows(eventAt.Add(-30 * time.Minute))
	ev.evalTimeWindows(eventAt.Add(time.Second))
	if rec.count() != 1 {
		t.Fatalf("前置失败: 事件应触发 1 次, got %d", rec.count())
	}

	now := eventAt.Add(2 * time.Hour)
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	tomorrowAt, _, _ := sun.SunEvent(39.9042, 116.4074, tz, now.AddDate(0, 0, 1), sun.Sunrise)
	if got.At == nil || !got.At.Equal(tomorrowAt) {
		t.Fatalf("at = %v, want 次日日出 %v (本日已触发)", got.At, tomorrowAt)
	}
}

// ── 3. 事件早已过去且本日未触发 = 首 tick 保守跳过 ⇒ 明天 ─────────────────
//
// 反向验证: 去掉"首 tick 保守"这条, 实现会报"今天还会触发", 与求值器行为分叉。
func TestNextRunSunEventPastWithoutFireIsTomorrow(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunrise, 0, 0)

	eventAt, _, _ := sun.SunEvent(39.9042, 116.4074, tz, time.Date(2026, 6, 21, 12, 0, 0, 0, tz), sun.Sunrise)
	now := eventAt.Add(3 * time.Hour) // 早已过, 但本日没触发过 (进程才起来)

	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	tomorrowAt, _, _ := sun.SunEvent(39.9042, 116.4074, tz, now.AddDate(0, 0, 1), sun.Sunrise)
	if got.At == nil || !got.At.Equal(tomorrowAt) {
		t.Fatalf("at = %v, want 次日 %v (首 tick 保守跳过, 本日不再触发)", got.At, tomorrowAt)
	}
}

// ── 4. 预测时刻必须与求值器**实际触发**时刻逐秒相等 ────────────────────────
//
// 这是防漂移的核心门禁: 预测与执行是两份代码, 只要有一方改了边界语义
// (偏移符号 / 时区 / 归日口径), 本用例立刻变红。
func TestNextRunSunMatchesEvaluatorFireAt(t *testing.T) {
	for _, offset := range []int{0, -30, 45} {
		ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunset, offset, 0)
		rec := ev.handler.(*sunRecorder)
		now := time.Date(2026, 6, 21, 8, 0, 0, 0, tz)

		predicted := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
		if predicted.At == nil {
			t.Fatalf("offset %d: 预测无时刻 (status=%s)", offset, predicted.Status)
		}

		// 走到预测时刻之后 1 秒, 求值器必须恰好触发一次, 且事件时刻 == 预测值。
		ev.evalTimeWindows(now)
		ev.evalTimeWindows(predicted.At.Add(time.Second))
		if rec.count() != 1 {
			t.Fatalf("offset %d: 预测 %v 之后未触发 (got %d)", offset, predicted.At, rec.count())
		}
		if rec.events[0].EventAt == nil || !rec.events[0].EventAt.Equal(*predicted.At) {
			t.Fatalf("offset %d: 实际事件时刻 %v != 预测 %v", offset, rec.events[0].EventAt, predicted.At)
		}
	}
}

// ── 5. 极昼/极夜: no_event, 且绝不返回零值时间 ────────────────────────────
func TestNextRunSunEventPolarNoEvent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	tzName := "Asia/Shanghai"
	lat, lon := 69.6492, 18.9553 // 特罗姆瑟
	tz, _ := time.LoadLocation(tzName)
	rule := models.AutomationRule{
		Name: "polar", Enabled: true,
		TriggerType:    models.AutomationTriggerSunEvent,
		TriggerSunKind: models.AutomationSunKindSunrise,
		ActionType:     models.AutomationActionNotification, ActionLevel: "info",
		Latitude: &lat, Longitude: &lon, Timezone: &tzName,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()

	now := time.Date(2026, 12, 21, 12, 0, 0, 0, tz) // 极夜
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunNoEvent {
		t.Fatalf("status = %q, want no_event (detail=%q)", got.Status, got.Detail)
	}
	if got.At != nil {
		t.Fatalf("极夜不得返回时刻, got %v (零值时间不得冒充结果)", got.At)
	}
}

// ── 6. 停用 / 未实现类型 ─────────────────────────────────────────────────
func TestNextRunDisabledAndUnsupported(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunrise, 0, 0)
	now := time.Date(2026, 6, 21, 2, 0, 0, 0, tz)

	disabled := rule
	disabled.Enabled = false
	if got := ev.NextRuns([]models.AutomationRule{disabled}, now)[disabled.ID]; got.Status != models.NextRunDisabled || got.At != nil {
		t.Fatalf("停用规则 status=%q at=%v, want disabled/nil", got.Status, got.At)
	}

	evt := rule
	evt.ID = rule.ID + 1000
	evt.TriggerType = models.AutomationTriggerEvent
	if got := ev.NextRuns([]models.AutomationRule{evt}, now)[evt.ID]; got.Status != models.NextRunUnsupported {
		t.Fatalf("event 类型 status=%q, want unsupported", got.Status)
	}
}

// ── 7. sensor_threshold: 不给时间 (只有冷却期给"最早可再次触发") ──────────
func TestNextRunSensorWaitingData(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.Local)
	rule := models.AutomationRule{
		Name: "sensor", Enabled: true,
		TriggerType:       models.AutomationTriggerSensorThreshold,
		TriggerSensorName: "temperature", TriggerComparator: "gt", TriggerThreshold: 30,
		ActionType: models.AutomationActionNotification, ActionLevel: "info",
		CooldownSec: 300,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()

	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunWaitingData {
		t.Fatalf("status = %q, want waiting_data", got.Status)
	}
	if got.At != nil {
		t.Fatalf("数据驱动规则在未冷却时不得给时间, got %v", got.At)
	}

	// 冷却中: 给出冷却到期时刻 (这是唯一合法的"最早可再次触发")。
	anchor := now.Add(-100 * time.Second)
	ev.mu.Lock()
	ev.triggered[rule.ID] = anchor
	ev.mu.Unlock()
	got = ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunWaitingData || got.Basis != models.NextRunBasisCooldown {
		t.Fatalf("冷却中 status=%q basis=%q", got.Status, got.Basis)
	}
	if got.At == nil || !got.At.Equal(anchor.Add(300*time.Second)) {
		t.Fatalf("at = %v, want %v", got.At, anchor.Add(300*time.Second))
	}
}

// ── 8. time_window: enter 的三个分支 ────────────────────────────────────
func TestNextRunWindowEnter(t *testing.T) {
	db := testutil.OpenTestDB(t)
	tz := testLocalZone()
	rule := models.AutomationRule{
		Timezone: &tz,
		Name:     "window", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()

	// (a) 窗口之前的钟点 ⇒ 今天的 08:00。
	now := time.Date(2026, 6, 21, 6, 30, 0, 0, time.Local)
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	// time_window 的展示时区必须能交给前端 Intl 用：要么是合法 IANA 名，
	// 要么是空串（前端退回浏览器时区）。**"Local" 不是合法 IANA 名** ——
	// 这是 2026-11-17 真机联调在 list 接口里实测到的缺陷（当时原样下发了
	// "timezone":"Local"，前端 Intl 抛 RangeError 只能走兜底）。
	if got.Timezone == "Local" {
		t.Fatalf("timezone 不得是字面量 \"Local\"（不是 IANA 名），got %q", got.Timezone)
	}
	if got.Timezone != "" {
		if _, err := time.LoadLocation(got.Timezone); err != nil {
			t.Fatalf("timezone %q 不是合法 IANA 名: %v", got.Timezone, err)
		}
	}
	want := time.Date(2026, 6, 21, 8, 0, 0, 0, time.Local)
	if got.At == nil || !got.At.Equal(want) {
		t.Fatalf("(a) at = %v, want %v", got.At, want)
	}

	// (b) 窗口之后的钟点 ⇒ 次日 08:00。
	now = time.Date(2026, 6, 21, 20, 0, 0, 0, time.Local)
	got = ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	want = time.Date(2026, 6, 22, 8, 0, 0, 0, time.Local)
	if got.At == nil || !got.At.Equal(want) {
		t.Fatalf("(b) at = %v, want %v", got.At, want)
	}

	// (c) 当前在窗内但**没有窗口历史**（刚建规则 / 进程刚重启）：
	//     求值器判据是 inside && exists && !wasInside ⇒ 不会触发。
	//     所以这里绝不能报"即将触发"（2026-11-18 D3：那正是"页面说会执行、
	//     实际不会"的反向漂移），必须报下一个窗口开始边界。
	now = time.Date(2026, 6, 21, 12, 0, 0, 0, time.Local)
	got = ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	want = time.Date(2026, 6, 22, 8, 0, 0, 0, time.Local)
	if got.At == nil || !got.At.Equal(want) {
		t.Fatalf("(c) at = %v, want 次日 08:00 %v（无窗口历史不得报即将触发）", got.At, want)
	}

	// (d) 已有窗口历史：上次在窗外、当前在窗内 ⇒ 真边沿，一个 tick 内触发。
	ev.mu.Lock()
	ev.windowStates[rule.ID] = false
	ev.mu.Unlock()
	got = ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.At == nil || got.At.Sub(now) > 2*time.Minute {
		t.Fatalf("(d) at = %v, 应在一个 tick 内（真进入边沿）", got.At)
	}
}

// ── 9. 冷却顺延: 计划时刻早于冷却到期 ⇒ 取冷却到期 ────────────────────────
func TestNextRunCooldownDefers(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 6, 21, 7, 30, 0, 0, time.Local)
	tz := testLocalZone()
	rule := models.AutomationRule{
		Timezone: &tz,
		Name:     "window-cooldown", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
		CooldownSec:       3600, // 1 小时
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()
	// 锚点 07:00 ⇒ 冷却到 08:00 正好与窗口开始重合; 用 07:10 让它更晚。
	anchor := time.Date(2026, 6, 21, 7, 10, 0, 0, time.Local)
	ev.mu.Lock()
	ev.triggered[rule.ID] = anchor
	ev.mu.Unlock()

	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Basis != models.NextRunBasisCooldown {
		t.Fatalf("basis = %q, want cooldown (detail=%q)", got.Basis, got.Detail)
	}
	want := anchor.Add(time.Hour) // 08:10, 晚于窗口开始 08:00
	if got.At == nil || !got.At.Equal(want) {
		t.Fatalf("at = %v, want %v (冷却顺延)", got.At, want)
	}
}

// ── 10. 日熔断顺延: 今日已达上限 ⇒ 次日 00:00 (进程时区) ──────────────────
func TestNextRunDailyLimitDefers(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 6, 21, 7, 30, 0, 0, time.Local)
	tz := testLocalZone()
	rule := models.AutomationRule{
		Timezone: &tz,
		Name:     "daily", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
		MaxDailyExec:      2,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ev := models.AutomationEvent{
			RuleID: rule.ID, TriggeredAt: now.Add(-time.Duration(i+1) * time.Minute),
			Result: models.AutomationResultExecuted, CreatedAt: now,
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()

	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Basis != models.NextRunBasisDailyLimit {
		t.Fatalf("basis = %q, want daily_limit (detail=%q)", got.Basis, got.Detail)
	}
	want := time.Date(2026, 6, 22, 0, 0, 0, 0, time.Local)
	if got.At == nil || !got.At.Equal(want) {
		t.Fatalf("at = %v, want %v (次日 00:00)", got.At, want)
	}
}

// ── 11. 大批规则不做 N+1: 单次调用即可拿到全部结果 ────────────────────────
func TestNextRunsBatch(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunset, 0, 0)
	now := time.Date(2026, 6, 21, 8, 0, 0, 0, tz)

	rules := make([]models.AutomationRule, 0, 50)
	for i := 0; i < 50; i++ {
		r := rule
		r.ID = rule.ID + uint(i)
		rules = append(rules, r)
	}
	got := ev.NextRuns(rules, now)
	if len(got) != len(rules) {
		t.Fatalf("结果数 = %d, want %d", len(got), len(rules))
	}
}
