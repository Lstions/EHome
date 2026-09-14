package datalifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedAutomationEvent 插入一条自动化事件行。triggered_at 与 created_at 都显式指定
// (生产写入点两者同值; 清理按 triggered_at 判定, 与全部 7 处业务判定读同一列)。
func seedAutomationEvent(t *testing.T, db *gorm.DB, ruleID uint, result string, at time.Time) *models.AutomationEvent {
	t.Helper()
	row := &models.AutomationEvent{
		RuleID:      ruleID,
		TriggeredAt: at,
		Result:      result,
		CreatedAt:   at,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed automation event result=%s: %v", result, err)
	}
	return row
}

// resultsOfEvents 返回全部事件行的 result (按 id 升序), 供精确条数/取值断言。
func resultsOfEvents(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var results []string
	if err := db.Model(&models.AutomationEvent{}).Order("id").Pluck("result", &results).Error; err != nil {
		t.Fatalf("pluck automation event results: %v", err)
	}
	return results
}

func countEvents(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.AutomationEvent{}).Count(&n).Error; err != nil {
		t.Fatalf("count automation events: %v", err)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────
// A. 白名单 / 分层口径的【静态】约束 (不依赖 DB)
// ─────────────────────────────────────────────────────────────────────

// TestAutomationEventCleanup_PendingConfirmIsNeverPrunable — 待办语义守卫。
//
// pending_confirm 是【待人工确认的待办】(models/automation.go:40), 消费方是
// planner.ConfirmEvent (行不存在 → ErrConfirmEventNotFound) —— 删掉它 = 抹掉一个
// 待办, 与命令域"未处置的 UNKNOWN"同型。
//
// 本测试断言三件事, 任一条被破坏即变红:
//  1. pending_confirm 不在任何可删白名单里;
//  2. 它在受保护集合里;
//  3. 若有人把它塞进白名单, assertResultsExcludeProtected 必须报错
//     (RunOnce 据此 fail-closed 地完全不删, 而不是静默过滤)。
func TestAutomationEventCleanup_PendingConfirmIsNeverPrunable(t *testing.T) {
	prunable := AutomationEventPrunableResults()
	for _, r := range prunable {
		if r == models.AutomationResultPendingConfirm {
			t.Fatalf("pending_confirm 出现在清理白名单 %v 里 —— 它是待人工确认的待办, "+
				"删除它等于用清理器代替人把悬案结案 (ConfirmEvent 将返回 ErrConfirmEventNotFound)", prunable)
		}
	}

	var protected bool
	for _, r := range automationEventProtectedResults() {
		if r == models.AutomationResultPendingConfirm {
			protected = true
		}
	}
	if !protected {
		t.Fatalf("automationEventProtectedResults 未包含 pending_confirm, 守卫失去对象")
	}

	if err := assertResultsExcludeProtected([]string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}); err == nil {
		t.Fatalf("白名单含 pending_confirm 时守卫未报错 —— 这会让删除静默发生")
	}
	if err := assertResultsExcludeProtected(prunable); err != nil {
		t.Fatalf("合法白名单被守卫误拒: %v", err)
	}

	// 守卫必须【独立】存在于删除路径上 (deleteBefore), 而不是只存在于 RunOnce 的
	// 预先校验: 两者是两道闸, 绕过/删掉任一道都要能被发现 (本仓 §6.5 "首次漏网" 教训)。
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)
	row := seedAutomationEvent(t, db, 1, models.AutomationResultPendingConfirm, now.AddDate(0, 0, -800))

	c := NewAutomationEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	if _, err := c.deleteBefore(context.Background(), []string{models.AutomationResultPendingConfirm}, now); err == nil {
		t.Fatalf("deleteBefore 接受了含 pending_confirm 的白名单 —— 第二道闸失效")
	}
	var left int64
	db.Model(&models.AutomationEvent{}).Where("id = ?", row.ID).Count(&left)
	if left != 1 {
		t.Fatalf("deleteBefore 在守卫报错的同时仍删除了待办行 (left=%d)", left)
	}
}

// TestAutomationEventCleanup_TierWindowsAreLayered — 分层裁决锁定。
//
// 设计裁决 (§2.1): 非执行档 30 天 / 执行档 400 天。**不是"一刀切 N 天"** ——
// 两档等长意味着要么丢掉设备动作归因 (取 30), 要么让诊断噪声跟着留 400 天。
// 本测试同时锁定【白名单的确切内容】: 新增 result 必须经过一次显式裁决
// (改这个集合) 才能进入删除范围, 否则 fail-closed 地保留。
func TestAutomationEventCleanup_TierWindowsAreLayered(t *testing.T) {
	classes := automationEventRetentionClasses()
	if len(classes) != 2 {
		t.Fatalf("档位数 = %d, want 2 (非执行档 + 执行档)", len(classes))
	}

	byLabel := map[string]automationEventRetentionClass{}
	for _, c := range classes {
		byLabel[c.label] = c
	}
	telemetry, ok := byLabel["telemetry"]
	if !ok {
		t.Fatalf("缺少非执行档 (telemetry)")
	}
	execution, ok := byLabel["execution"]
	if !ok {
		t.Fatalf("缺少执行档 (execution)")
	}

	if telemetry.windowDays != 30 {
		t.Errorf("非执行档窗口 = %d, want 30 (§2.1 档位 A)", telemetry.windowDays)
	}
	if execution.windowDays != 400 {
		t.Errorf("执行档窗口 = %d, want 400 (§2.1 档位 B)", execution.windowDays)
	}
	if execution.windowDays <= telemetry.windowDays {
		t.Errorf("执行档 %d 未长于非执行档 %d —— 分层裁决被拉平", execution.windowDays, telemetry.windowDays)
	}
	if len(execution.results) != 1 || execution.results[0] != models.AutomationResultExecuted {
		t.Errorf("执行档白名单 = %v, want 仅 [executed]", execution.results)
	}

	// 白名单确切内容 (设计文档 §2.1 档位 A 逐项枚举)。
	wantTelemetry := []string{
		models.AutomationResultSuppressedCooldown,
		models.AutomationResultSuppressedDailyLimit,
		models.AutomationResultConditionChanged,
		models.AutomationResultFailedGate,
		models.AutomationResultFailedDispatch,
		models.AutomationResultNotification,
		models.AutomationResultExpired,
	}
	if len(telemetry.results) != len(wantTelemetry) {
		t.Fatalf("非执行档白名单 = %v, want %v", telemetry.results, wantTelemetry)
	}
	for i, want := range wantTelemetry {
		if telemetry.results[i] != want {
			t.Errorf("非执行档白名单[%d] = %q, want %q", i, telemetry.results[i], want)
		}
	}

	// fail-closed: 白名单之外 (含将来新增取值) 不得被任何档覆盖。
	prunable := AutomationEventPrunableResults()
	if len(prunable) != len(wantTelemetry)+1 {
		t.Errorf("可删白名单 = %v, want %d 项", prunable, len(wantTelemetry)+1)
	}
	if err := assertResultsExcludeProtected(prunable); err != nil {
		t.Errorf("默认可删白名单未过守卫: %v", err)
	}
}

// TestAutomationEventCleanup_CutoffNeverReachesToday — 当日判定区间与删除区间不相交。
//
// 日熔断 (planner.go:93/583) 与幂等序号 (planner.go:175/703) 读的是
// [本地时区当日 00:00, now] 的 executed 计数, 跨度 < 1 天。本测试对**所有档位**
// (含把窗口压到最小的显式覆盖) 断言 cutoff 仍在"今日 00:00"之前 ——
// 即被删的行永远是昨天的, 当日计数不可能被清理影响。
// 这条断言不依赖具体数字: 只要有人把 automationEventMinRetentionDays 降到 1 天
// (cutoff = now-24h 可能落在今日 00:00 之后), 它就变红。
func TestAutomationEventCleanup_CutoffNeverReachesToday(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	c := NewAutomationEventCleaner(db)
	c.SetRetention(1, 1) // 故意压到最小 (含非法短值) → 应被下界钳住

	for _, class := range automationEventRetentionClasses() {
		days := c.retentionWindowDays(class)
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		// 断言的是【下界给出的余量】, 不只是"不相交": cutoff 必须至少比今日 00:00
		// 再早 24 小时 (即窗口 >= 2 天)。1 天窗口在 now 靠近午夜时会把 cutoff 推进
		// 今日区间 (时钟回拨/补写场景), 恰好是本仓最怕的"少删一行没事、多删一行是行为改变"。
		if cutoff.Add(24 * time.Hour).After(dayStart) {
			t.Errorf("档位 %s: cutoff %s (+24h 余量) 晚于今日 00:00 (%s) —— "+
				"删除区间与当日熔断/幂等序号计数区间存在相交风险 (窗口下界被削)",
				class.label, cutoff, dayStart)
		}
	}
}

// TestAutomationEventCleanup_WindowFloorAndDirtyValues — 脏值防御。
func TestAutomationEventCleanup_WindowFloorAndDirtyValues(t *testing.T) {
	db := testutil.OpenTestDB(t)
	c := NewAutomationEventCleaner(db)

	// 非正值 → 回落各档默认窗口 (负保留期会算出未来的 cutoff)。
	c.SetRetention(0, -5)
	for _, class := range automationEventRetentionClasses() {
		if got := c.retentionWindowDays(class); got != class.windowDays {
			t.Errorf("档位 %s: SetRetention(0,-5) 后窗口 = %d, want 默认 %d", class.label, got, class.windowDays)
		}
	}

	// 显式短值 → 钳到下界 (不得低于 2 天)。
	c.SetRetention(1, 1)
	for _, class := range automationEventRetentionClasses() {
		if got := c.retentionWindowDays(class); got != automationEventMinRetentionDays {
			t.Errorf("档位 %s: SetRetention(1,1) 后窗口 = %d, want 下界 %d",
				class.label, got, automationEventMinRetentionDays)
		}
	}

	// 显式合法值原样生效 (测试可注入)。
	c.SetRetention(10, 1000)
	if got := c.retentionWindowDays(automationEventRetentionClasses()[0]); got != 10 {
		t.Errorf("显式覆盖未生效: %d, want 10", got)
	}
	if got := c.retentionWindowDays(automationEventRetentionClasses()[1]); got != 1000 {
		t.Errorf("显式覆盖未生效: %d, want 1000", got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// B. 行为测试 (分层窗口 / 白名单 / 悬案 / 分批 / 幂等)
// ─────────────────────────────────────────────────────────────────────

// TestAutomationEventCleaner_DeletesOnlyPastTierWindow — 核心条数断言:
// 两档各自按自己的窗口删; 窗内一律保留; 白名单外永不删; pending_confirm 永不删。
func TestAutomationEventCleaner_DeletesOnlyPastTierWindow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)
	ago := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	// 非执行档 (30 天)
	seedAutomationEvent(t, db, 1, models.AutomationResultSuppressedCooldown, ago(31))   // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultSuppressedDailyLimit, ago(31)) // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultConditionChanged, ago(31))     // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultFailedGate, ago(31))           // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultFailedDispatch, ago(31))       // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultNotification, ago(31))         // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultExpired, ago(500))             // 窗外 → 删
	seedAutomationEvent(t, db, 1, models.AutomationResultSuppressedCooldown, ago(29))   // 窗内 → 留
	seedAutomationEvent(t, db, 1, models.AutomationResultExpired, ago(1))               // 窗内 → 留

	// 执行档 (400 天)
	seedAutomationEvent(t, db, 2, models.AutomationResultExecuted, ago(401)) // 窗外 → 删
	seedAutomationEvent(t, db, 2, models.AutomationResultExecuted, ago(399)) // 窗内 → 留
	// ★ 分层的关键反例: 100 天前 —— 远超非执行档窗 (30 天), 但在执行档窗内。
	//   若有人把两档拉平成"统一 30 天", 这一行会被删, 本测试变红。
	seedAutomationEvent(t, db, 2, models.AutomationResultExecuted, ago(100)) // 分层 → 留

	// 悬案 (sweep 从未跑到的场景)
	seedAutomationEvent(t, db, 3, models.AutomationResultPendingConfirm, ago(800)) // 待办 → 永不删
	seedAutomationEvent(t, db, 3, models.AutomationResultPendingConfirm, ago(1))   // 待办 → 永不删

	// 白名单外 / 未来新增取值 → 永不删 (fail-closed)
	seedAutomationEvent(t, db, 4, "future_result_not_in_whitelist", ago(9999))
	seedAutomationEvent(t, db, 4, "", ago(9999))

	if got := countEvents(t, db); got != 16 {
		t.Fatalf("seeded events = %d, want 16", got)
	}

	c := NewAutomationEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("automation event cleanup run: %v", err)
	}
	if report.TelemetryDeleted != 7 {
		t.Errorf("TelemetryDeleted = %d, want 7", report.TelemetryDeleted)
	}
	if report.ExecutionDeleted != 1 {
		t.Errorf("ExecutionDeleted = %d, want 1", report.ExecutionDeleted)
	}
	if report.Total() != 8 {
		t.Errorf("Total = %d, want 8", report.Total())
	}

	remaining := resultsOfEvents(t, db)
	want := []string{
		models.AutomationResultSuppressedCooldown,
		models.AutomationResultExpired,
		models.AutomationResultExecuted,
		models.AutomationResultExecuted,
		models.AutomationResultPendingConfirm,
		models.AutomationResultPendingConfirm,
		"future_result_not_in_whitelist",
		"",
	}
	if len(remaining) != len(want) {
		t.Fatalf("remaining results = %v, want %v", remaining, want)
	}
	for i := range want {
		if remaining[i] != want[i] {
			t.Fatalf("remaining results = %v, want %v", remaining, want)
		}
	}

	// 幂等: 再跑一轮不再删任何行。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Total() != 0 {
		t.Errorf("second run deleted %+v, want no-op (idempotent)", report)
	}
	if got := countEvents(t, db); got != int64(len(want)) {
		t.Errorf("after rerun events = %d, want %d", got, len(want))
	}
}

// TestAutomationEventCleaner_DeletesByTriggeredAtNotCreatedAt — 时间列的裁决锁定。
//
// 全部 7 处业务判定读的都是 **triggered_at** (日熔断/幂等序号/手动冷却/超时清扫),
// 清理器必须用【同一列】才是同一把尺子 —— 若改成 created_at, 在补写/维护脚本
// 造成两列不同步时会与读侧错位。
//
// 本测试刻意让两列【背离】, 因此"把 DELETE 的时间列换成 created_at"会立刻变红:
//   - 行 A: triggered_at 老 (60 天前) / created_at 新 (1 小时前) → 必须删 (业务判定口径)
//   - 行 B: triggered_at 新 (1 小时前) / created_at 老 (60 天前) → 必须留
func TestAutomationEventCleaner_DeletesByTriggeredAtNotCreatedAt(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)

	seedAt := func(result string, triggeredAt, createdAt time.Time) *models.AutomationEvent {
		t.Helper()
		row := &models.AutomationEvent{
			RuleID: 11, TriggeredAt: triggeredAt, Result: result, CreatedAt: createdAt,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed event %s: %v", result, err)
		}
		return row
	}
	old := now.AddDate(0, 0, -60)
	fresh := now.Add(-time.Hour)
	a := seedAt(models.AutomationResultSuppressedCooldown, old, fresh) // triggered_at 老 → 删
	b := seedAt(models.AutomationResultSuppressedCooldown, fresh, old) // triggered_at 新 → 留

	c := NewAutomationEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if report.TelemetryDeleted != 1 {
		t.Errorf("TelemetryDeleted = %d, want 1 (按 triggered_at 判定)", report.TelemetryDeleted)
	}
	var aLeft, bLeft int64
	db.Model(&models.AutomationEvent{}).Where("id = ?", a.ID).Count(&aLeft)
	db.Model(&models.AutomationEvent{}).Where("id = ?", b.ID).Count(&bLeft)
	if aLeft != 0 {
		t.Errorf("triggered_at 60 天前的行未被删除 —— 清理未按 triggered_at 判定")
	}
	if bLeft != 1 {
		t.Errorf("created_at 60 天前但 triggered_at 1 小时前的行被删除 —— " +
			"清理错误地按 created_at 判定, 与全部 7 处业务判定的读列不一致")
	}
}

// TestAutomationEventCleaner_PendingConfirmSurvivesWhenSweepNeverRan —
// **守卫必要性**测试 (任务书 + 主控复核要求)。
//
// 场景: 一条 800 天前的 pending_confirm —— 模拟 planner.StartCleanup
// (main.go:294 的独立 goroutine, 每 5 分钟把 24h 前的 pending_confirm 改成 expired)
// **从未跑到** (panic / 未启动 / 上下文提前取消 / 未来有人调大 TTL)。
// 若清理器依赖"它已经被别人改成 expired", 这行就会被当成非执行档删掉 ——
// 用户的待办静默消失, ConfirmEvent 返回 ErrConfirmEventNotFound。
//
// 正对照: 同年龄的 expired 行**必须**被删 —— 证明清理器确实跑了、
// 且删的是同一年龄段的其它 result; 因此本测试不是"什么都没删所以绿"。
//
// 变红条件: 有人把 pending_confirm 加进任一档白名单, 或把白名单换成黑名单
// (result <> 'executed')。
func TestAutomationEventCleaner_PendingConfirmSurvivesWhenSweepNeverRan(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -800)

	pending := seedAutomationEvent(t, db, 7, models.AutomationResultPendingConfirm, old)
	control := seedAutomationEvent(t, db, 7, models.AutomationResultExpired, old)

	c := NewAutomationEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}

	// 正对照: 同年龄的 expired 被删 ⇒ 清理确实执行了 (非空洞绿灯)。
	var controlLeft int64
	db.Model(&models.AutomationEvent{}).Where("id = ?", control.ID).Count(&controlLeft)
	if controlLeft != 0 {
		t.Fatalf("正对照失败: 800 天前的 expired 行未被删除 (deleted=%+v) —— 本测试失去意义", report)
	}

	// 主断言: 待办必须还在, 且仍是 pending_confirm (ConfirmEvent 的入口依赖它)。
	var ev models.AutomationEvent
	if err := db.First(&ev, pending.ID).Error; err != nil {
		t.Fatalf("800 天前的 pending_confirm 被清理器删除 —— "+
			"待人工确认的悬案被抹掉, ConfirmEvent 将返回 ErrConfirmEventNotFound: %v", err)
	}
	if ev.Result != models.AutomationResultPendingConfirm {
		t.Fatalf("pending_confirm 行的 result 被改写为 %q (清理器不得改写 result)", ev.Result)
	}
}

// TestAutomationEventCleaner_DoesNotTouchCooldownAnchor — 前置条件 A 的解耦自证:
// 清理事件行 (含 executed) 绝不触碰 automation_rules.last_triggered_at,
// 也绝不触碰规则行本身。
func TestAutomationEventCleaner_DoesNotTouchCooldownAnchor(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)

	anchor := now.AddDate(0, 0, -500)
	rule := models.AutomationRule{
		Name: "anchor-rule", Enabled: true,
		TriggerType:     models.AutomationTriggerSensorThreshold,
		ActionType:      models.AutomationActionNotification,
		CooldownSec:     3600,
		LastTriggeredAt: &anchor,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	seedAutomationEvent(t, db, rule.ID, models.AutomationResultExecuted, anchor)
	seedAutomationEvent(t, db, rule.ID, models.AutomationResultSuppressedCooldown, anchor)

	c := NewAutomationEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	if _, err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if got := countEvents(t, db); got != 0 {
		t.Fatalf("events after run = %d, want 0 (both rows past their windows)", got)
	}

	var after models.AutomationRule
	if err := db.First(&after, rule.ID).Error; err != nil {
		t.Fatalf("rule row disappeared: %v", err)
	}
	if after.LastTriggeredAt == nil {
		t.Fatalf("清理器把冷却锚点置空了 —— 冷却窗会提前解除 (A-INV 被破坏)")
	}
	if !after.LastTriggeredAt.Equal(anchor) {
		t.Fatalf("冷却锚点被改写: got %s, want %s", after.LastTriggeredAt, anchor)
	}
}

// TestAutomationEventCleaner_Batches — 分批删除 (与 purge 同范式):
// 每批独立事务, 多轮才能删尽; 断言总条数精确。
func TestAutomationEventCleaner_Batches(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)

	const expired = 5
	for i := 0; i < expired; i++ {
		seedAutomationEvent(t, db, 5, models.AutomationResultSuppressedCooldown, now.AddDate(0, 0, -60))
	}
	seedAutomationEvent(t, db, 5, models.AutomationResultSuppressedCooldown, now.AddDate(0, 0, -1))

	c := NewAutomationEventCleaner(db)
	c.SetBatchSize(1) // 强制每批 1 行
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if report.TelemetryDeleted != expired {
		t.Errorf("TelemetryDeleted = %d, want %d", report.TelemetryDeleted, expired)
	}
	if got := countEvents(t, db); got != 1 {
		t.Fatalf("remaining = %d, want 1 (fresh row kept)", got)
	}
}

// TestAutomationEventCleaner_NilDB — 参数守卫 (与其它清理器同约定)。
func TestAutomationEventCleaner_NilDB(t *testing.T) {
	c := NewAutomationEventCleaner(nil)
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatalf("nil db 未报错")
	} else if !strings.Contains(err.Error(), "requires a db") {
		t.Fatalf("nil db 错误信息异常: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// C. 接线自证
// ─────────────────────────────────────────────────────────────────────

// TestRetentionTask_RunsAutomationEventCleanup — 接线自证: automation_events 清理
// 挂在既有每日 retention 任务上 (无新增 goroutine / 不动 main.go), 与
// notifications / deliveries 清理同批执行, 且不干扰逐设备保留期处理。
func TestRetentionTask_RunsAutomationEventCleanup(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 9, 14, 13, 30, 0, 0, time.UTC)
	ago := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	seedAutomationEvent(t, db, 9, models.AutomationResultSuppressedCooldown, ago(60)) // 非执行档窗外 → 删
	seedAutomationEvent(t, db, 9, models.AutomationResultExecuted, ago(500))          // 执行档窗外 → 删
	seedAutomationEvent(t, db, 9, models.AutomationResultExecuted, ago(100))          // 执行档窗内 → 留
	seedAutomationEvent(t, db, 9, models.AutomationResultPendingConfirm, ago(800))    // 待办 → 留
	seedNotification(t, db, "ancient-unread", false, ago(1000))                       // 同批的 notifications 清理
	// key 会拼成 edge_devices 的 device_id/name (varchar(16)) —— PG 下超长直接
	// SQLSTATE 22001 (SQLite 不校验长度, 第一次只跑 SQLite 时此处静默通过)。
	// "d1-autoev" + "-inst" = 14 字符。
	ld := seedRetentionDevice(t, db, "d1-autoev", 365, ago(400))
	_ = ld

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	r.notifier.SetRetention(90, 180)
	r.notifier.SetBatchSleep(0)
	r.notifier.now = func() time.Time { return now }
	r.deliveries.SetBatchSleep(0)
	r.deliveries.now = func() time.Time { return now }
	r.events.SetBatchSleep(0)
	r.events.now = func() time.Time { return now }

	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("retention run: %v", err)
	}
	var deleted int64
	for _, res := range results {
		deleted += res.RowsDeleted
	}
	if deleted != 1 {
		t.Errorf("retention RowsDeleted = %d, want 1 (per-device path unaffected)", deleted)
	}

	remaining := resultsOfEvents(t, db)
	want := []string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}
	if len(remaining) != len(want) {
		t.Fatalf("events after retention run = %v, want %v", remaining, want)
	}
	for i := range want {
		if remaining[i] != want[i] {
			t.Fatalf("events after retention run = %v, want %v", remaining, want)
		}
	}
	if got := countNotifications(t, db); got != 0 {
		t.Errorf("notifications after retention run = %d, want 0", got)
	}
}
