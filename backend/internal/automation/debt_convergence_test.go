package automation

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// =====================================================================
// 2026-11-18 债务收敛的回归锁（每一条对应清单里的一个编号）
// =====================================================================

// ── D3: exit 边沿在“没有窗口历史”时不得报一个精确时刻 ──
func TestNextRunWindowExitWithoutHistoryIsUnavailable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	tz := testLocalZone()
	rule := models.AutomationRule{
		Name: "exit", Enabled: true, Timezone: &tz,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowExit,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()

	// 进程刚重启：windowStates 为空（windowKnown=false）。此时求值器的 exit 判据
	// `!inside && exists && wasInside` 永远不成立，报“18:00 会执行”就是骗人。
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.Local)
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunUnavailable {
		t.Fatalf("status = %q, want %q（无窗口历史时 exit 不会触发）", got.Status, models.NextRunUnavailable)
	}
	if got.At != nil {
		t.Fatalf("at = %v, 无历史时不得给时刻", got.At)
	}

	// 建立历史后再问：必须恢复成精确边界。
	ev.evalTimeWindows(now) // 窗内，写入 wasInside=true
	got = ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunScheduled || got.At == nil {
		t.Fatalf("有历史后 status=%q at=%v, want scheduled + 18:00", got.Status, got.At)
	}
	want := time.Date(2026, 6, 21, 18, 0, 0, 0, time.Local)
	if !got.At.Equal(want) {
		t.Fatalf("at = %v, want %v", got.At, want)
	}
}

// ── D15: “预测 == 执行” 对拍（这才是防止 D3 类漂移再活一次的门禁） ──
func TestNextRunWindowMatchesEvaluator(t *testing.T) {
	h := &captureHandler{}
	db := testutil.OpenTestDB(t)
	tz := testLocalZone()
	rule := models.AutomationRule{
		Name: "parity", Enabled: true, Timezone: &tz,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		CooldownSec:       0,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)
	ev.LoadRules()

	// (a) 先建立“窗外”历史，再问预测：必须精确等于 08:00。
	outside := time.Date(2026, 6, 21, 7, 0, 0, 0, time.Local)
	ev.evalTimeWindows(outside)
	if len(h.events) != 0 {
		t.Fatalf("窗外不得触发, got %d", len(h.events))
	}
	before := time.Date(2026, 6, 21, 7, 30, 0, 0, time.Local)
	predicted := ev.NextRuns([]models.AutomationRule{rule}, before)[rule.ID]
	want := time.Date(2026, 6, 21, 8, 0, 0, 0, time.Local)
	if predicted.At == nil || !predicted.At.Equal(want) {
		t.Fatalf("预测 = %v, want %v", predicted.At, want)
	}
	// 真跑那一刻：必须恰好触发一次，且时刻与预测一致（预测是边界，执行也是）。
	ev.evalTimeWindows(want)
	if len(h.events) != 1 {
		t.Fatalf("08:00 应触发 1 次, got %d", len(h.events))
	}
	if got := h.events[0].At; !got.Equal(*predicted.At) {
		t.Fatalf("执行时刻 %v != 预测 %v（预测/执行已漂移）", got, *predicted.At)
	}

	// (b) 反向：无窗口历史 + 已在窗内 ⇒ 预测“不立刻执行”，实际也不执行。
	h2 := &captureHandler{}
	ev2 := NewEvaluator(db, h2)
	ev2.LoadRules()
	inside := time.Date(2026, 6, 21, 12, 0, 0, 0, time.Local)
	p2 := ev2.NextRuns([]models.AutomationRule{rule}, inside)[rule.ID]
	if p2.At == nil || p2.At.Sub(inside) < 10*time.Minute {
		t.Fatalf("无历史预测 = %v, 不应报即将触发", p2.At)
	}
	ev2.evalTimeWindows(inside)
	if len(h2.events) != 0 {
		t.Fatalf("无历史首个 tick 不得触发, got %d", len(h2.events))
	}
}

// ── D7: 执行侧必须按规则时区解释窗口（不是进程时区） ──
func TestTimeWindowUsesRuleTimezone(t *testing.T) {
	db := testutil.OpenTestDB(t)
	h := &captureHandler{}
	zone := "Asia/Shanghai"
	rule := models.AutomationRule{
		Name: "tz", Enabled: true, Timezone: &zone,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		CooldownSec:       0,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)
	ev.LoadRules()

	// 用 UTC 构造同一物理时刻：上海 07:00 = UTC 23:00（前一日），上海 10:00 = UTC 02:00。
	shOutside := time.Date(2026, 6, 20, 23, 0, 0, 0, time.UTC)
	shInside := time.Date(2026, 6, 21, 2, 0, 0, 0, time.UTC)
	ev.evalTimeWindows(shOutside)
	ev.evalTimeWindows(shInside)
	if len(h.events) != 1 {
		t.Fatalf("按规则时区应跨入窗口 1 次, got %d（若为 0 说明仍在按进程 UTC 解释）", len(h.events))
	}
}

// ── D4: MarkTriggered 必须重启冷却窗（手动/确认执行后自动路径不得提前触发） ──
func TestMarkTriggeredRestartsCooldown(t *testing.T) {
	db := testutil.OpenTestDB(t)
	h := &captureHandler{}
	tz := testLocalZone()
	rule := models.AutomationRule{
		Name: "mark", Enabled: true, Timezone: &tz, ID: 1,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowInside,
		CooldownSec:       300,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)
	ev.LoadRules()

	t0 := time.Date(2026, 6, 21, 12, 0, 0, 0, time.Local)
	ev.evalTimeWindows(t0)
	if len(h.events) != 1 {
		t.Fatalf("首次窗口内 tick 应触发, got %d", len(h.events))
	}
	// 冷却窗内：不触发。
	ev.evalTimeWindows(t0.Add(100 * time.Second))
	if len(h.events) != 1 {
		t.Fatalf("冷却中不得触发, got %d", len(h.events))
	}
	// 模拟一次手动执行（回灌内存锚点）：冷却应从此刻重新计 300s。
	ev.MarkTriggered(rule.ID, t0.Add(200*time.Second))
	ev.evalTimeWindows(t0.Add(250 * time.Second))
	if len(h.events) != 1 {
		t.Fatalf("手动触发后 50s 仍在冷却窗内, 不得触发, got %d", len(h.events))
	}
	ev.evalTimeWindows(t0.Add(501 * time.Second))
	if len(h.events) != 2 {
		t.Fatalf("冷却应从现在起算并在 300s 后到期, got %d", len(h.events))
	}
}

// cooldownRecorder 记录 Planner 是否把“已下发”回灌给求值器。
type cooldownRecorder struct {
	ids []uint
	at  []time.Time
}

func (r *cooldownRecorder) MarkTriggered(id uint, at time.Time) {
	r.ids = append(r.ids, id)
	r.at = append(r.at, at)
}

// ── D4 接线: 手动触发成功后必须回灌（删掉 markCooldown 调用本用例即红） ──
func TestPlannerMarksCooldownOnManualTrigger(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rec := &cooldownRecorder{}
	p.SetCooldownMarker(rec)
	rule := models.AutomationRule{
		Name: "manual-cooldown", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowInside,
		CooldownSec:       0,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1"); err != nil {
		t.Fatalf("手动触发: %v", err)
	}
	if len(rec.ids) != 1 || rec.ids[0] != rule.ID {
		t.Fatalf("手动触发未回灌冷却锚点, recorder=%v", rec.ids)
	}
}

// ── D5: partial_failure 必须有具名 WS 生产者（前端早已订阅） ──
func TestPartialFailureBroadcastProducer(t *testing.T) {
	db := testutil.OpenTestDB(t)
	var names []string
	p := NewPlanner(db, nil, func(name string, payload any) { names = append(names, name) }, 0)
	rule := models.AutomationRule{ID: 1, Name: "multi"}
	ev := models.AutomationEvent{
		ID: 9, RuleID: 1, Result: models.AutomationResultPartialFailure,
	}
	p.broadcastAutomationEvent(rule, ev, 0, "")
	var hasGeneric, hasPartial bool
	for _, n := range names {
		if n == "automation_event" {
			hasGeneric = true
		}
		if n == "automation_partial_failure" {
			hasPartial = true
		}
	}
	if !hasGeneric || !hasPartial {
		t.Fatalf("广播名 = %v, 需要 automation_event + automation_partial_failure", names)
	}
}

// ── D6: 事件落库必须递增 ehome_automation_events_total{result} ──
func automationEventCounter(t *testing.T, result string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "ehome_automation_events_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestAutomationEventCounterIncrements(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const result = models.AutomationResultFailedGate
	before := automationEventCounter(t, result)
	if err := db.Create(&models.AutomationEvent{
		RuleID: 1, TriggeredAt: time.Now(), Result: result,
		TriggerSource: models.AutomationTriggerSourceAuto, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	after := automationEventCounter(t, result)
	if after <= before {
		t.Fatalf("count %v -> %v, 事件落库必须递增计数器", before, after)
	}
}

// ── D14: 日熔断计数查询失败时不得 fail-open 承诺一个会被抑制的时刻 ──
func TestNextRunDailyLimitQueryFailureIsUnavailable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 6, 21, 7, 30, 0, 0, time.Local)
	tz := testLocalZone()
	anchor := now.Add(-time.Hour)
	rule := models.AutomationRule{
		Name: "fail-open", Enabled: true, Timezone: &tz, MaxDailyExec: 2,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		CooldownSec:       0,
		LastTriggeredAt:   &anchor,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, &sunRecorder{})
	ev.LoadRules()
	// 制造一次真实的查询失败：表被删掉（比 mock 更接近“库坏了”）。
	if err := db.Migrator().DropTable(&models.AutomationEvent{}); err != nil {
		t.Fatal(err)
	}
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunUnavailable {
		t.Fatalf("status = %q, want unavailable（计数查不出时不得承诺时刻）", got.Status)
	}
	if got.At != nil {
		t.Fatalf("at = %v, 计数未知时不得给时刻", got.At)
	}
}

// 2026-11-18：手动触发**通知**动作也必须写冷却锚点（事件+锚点同事务）。
// 旧实现只写事件，而手动路径的冷却判定读 last_triggered_at ⇒ 连点永不冷却；
// next_run 的冷却顺延也会在重启后丢失。
func TestManualNotificationWritesCooldownAnchor(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rule := models.AutomationRule{
		Name: "notify-cooldown", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:   "x", TriggerComparator: models.AlertComparatorGT,
		TriggerThreshold: 1, CooldownSec: 300,
		ActionType: models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	first, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if first.Result != models.AutomationResultNotification {
		t.Fatalf("first result = %q, want notification", first.Result)
	}
	var stored models.AutomationRule
	if err := p.db.First(&stored, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastTriggeredAt == nil {
		t.Fatal("手动通知动作未写 last_triggered_at —— 冷却锚点缺失（事件与锚点必须同事务）")
	}
	second, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("second trigger: %v", err)
	}
	if second.Result != models.AutomationResultSuppressedCooldown {
		t.Fatalf("second result = %q, want suppressed_cooldown（冷却必须对通知动作生效）", second.Result)
	}
}
