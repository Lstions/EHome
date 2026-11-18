package automation

// 2026-11-18 D22/P1 回归门禁：
//   - 偏移把 fireAt 推过本地午夜时，规则仍必须触发（旧实现 0 次）；
//   - 进程启动跨过事件时，10 分钟窗口内补发一次、超窗落 missed 审计；
//   - 库侧已有该事件记录时不得重复补发（重复设备动作是安全相关）。

import (
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
	"ehome/backend/testutil"
)

// ── D22: 偏移跨本地午夜仍必须触发 ─────────────────────────────────────────
//
// 旧实现只取「now 当日那一场」事件：日出 -600 的 fireAt 落在前一日 18:45，
// 到点时算出的却是再前一天的事件 ⇒ now < fireAt 恒成立，永远不触发。
// 反向验证：把候选集退回单日 → 两个子用例都变 0 次。
func TestSunEventOffsetCrossingLocalMidnight(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		offset int
	}{
		{"日出前 600 分钟（落到前一日）", models.AutomationSunKindSunrise, -600},
		{"日落后 300 分钟（落到次日）", models.AutomationSunKindSunset, 300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := buildSunFixture(t, c.kind, c.offset)
			rec := &sunRecorder{}
			ev := NewEvaluator(fx.db, rec)
			ev.LoadRules()

			fireAt := fx.eventAt.Add(time.Duration(c.offset) * time.Minute)
			if fireAt.In(fx.tz).Format("2006-01-02") == fx.eventAt.In(fx.tz).Format("2006-01-02") {
				t.Fatalf("夹具前提失败：偏移 %d 没有把 fireAt 推出事件日（%s）", c.offset, fireAt)
			}

			// 上一轮 tick 放在 fireAt 前 2 分钟：走正常边沿路径，不是补发窗口。
			ev.evalTimeWindows(fireAt.Add(-2 * time.Minute))
			if got := rec.count(); got != 0 {
				t.Fatalf("到点前触发了 %d 次", got)
			}
			ev.evalTimeWindows(fireAt.Add(1 * time.Second))
			if got := rec.count(); got != 1 {
				t.Fatalf("偏移跨本地午夜必须触发 1 次，got %d —— 仍然只算了 now 当日那一场？", got)
			}
			if rec.events[0].EventAt == nil || !rec.events[0].EventAt.Equal(fireAt) {
				t.Fatalf("EventAt = %v，want fireAt %v（事件时刻不是 tick 时刻）", rec.events[0].EventAt, fireAt)
			}
			// 同一场事件后续 tick 不得重复。
			for i := 0; i < 3; i++ {
				ev.evalTimeWindows(fireAt.Add(time.Duration(i+1) * time.Minute))
			}
			if got := rec.count(); got != 1 {
				t.Fatalf("同一场事件重复触发 %d 次", got)
			}
		})
	}
}

// ── P1: 启动补发窗口 ─────────────────────────────────────────────────────
//
// 规则在事件之前就已生效（UpdatedAt < fireAt），进程在事件后 5 分钟启动：
// 补发一次，EventAt 必须是事件时刻；库侧已有记录时第二次重启不得重复。
func TestSunEventStartupCatchUpWindow(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunrise, 0)
	// 把规则的 UpdatedAt 拨到事件前 1 小时：表达「当前配置先于事件生效」。
	if err := fx.db.Exec("UPDATE automation_rules SET updated_at = ? WHERE id = ?",
		fx.eventAt.Add(-time.Hour), fx.rule.ID).Error; err != nil {
		t.Fatal(err)
	}

	rec := &sunRecorder{}
	ev := NewEvaluator(fx.db, rec)
	ev.LoadRules()
	ev.evalTimeWindows(fx.eventAt.Add(5 * time.Minute)) // 窗口内（10min）
	if got := rec.count(); got != 1 {
		t.Fatalf("启动后 5 分钟内的补发必须触发 1 次，got %d", got)
	}
	if rec.events[0].EventAt == nil || !rec.events[0].EventAt.Equal(fx.eventAt) {
		t.Fatalf("补发的 EventAt 必须是事件时刻 %v，got %v", fx.eventAt, rec.events[0].EventAt)
	}

	// 模拟 Planner 落库（recorder 本身不写库），再重启 ⇒ 库侧已有记录，不得重复。
	fireAt := fx.eventAt
	if err := fx.db.Create(&models.AutomationEvent{
		RuleID: fx.rule.ID, TriggeredAt: fx.eventAt.Add(5 * time.Minute),
		Result: models.AutomationResultExecuted, EventAt: &fireAt,
		CreatedAt: fx.eventAt.Add(5 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	rec2 := &sunRecorder{}
	ev2 := NewEvaluator(fx.db, rec2)
	ev2.LoadRules()
	ev2.evalTimeWindows(fx.eventAt.Add(6 * time.Minute))
	if got := rec2.count(); got != 0 {
		t.Fatalf("库侧已有该事件记录时重启补发了 %d 次 —— 会重复下发设备动作", got)
	}
}

// ── P1: 超窗落 missed_solar_event 审计（且不重复写）──────────────────────
func TestSunEventStartupMissedAudit(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
	if err := fx.db.Exec("UPDATE automation_rules SET updated_at = ? WHERE id = ?",
		fx.eventAt.Add(-time.Hour), fx.rule.ID).Error; err != nil {
		t.Fatal(err)
	}

	rec := &sunRecorder{}
	ev := NewEvaluator(fx.db, rec)
	ev.LoadRules()
	ev.evalTimeWindows(fx.eventAt.Add(11 * time.Minute)) // 超出 10min 窗口
	if got := rec.count(); got != 0 {
		t.Fatalf("超出补发窗口不得执行，got %d 次", got)
	}

	var evs []models.AutomationEvent
	if err := fx.db.Where("rule_id = ? AND result = ?", fx.rule.ID,
		models.AutomationResultMissedSolarEvent).Find(&evs).Error; err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("应落恰好 1 条 missed_solar_event，got %d —— 静默跳过正是要消除的形态", len(evs))
	}
	if evs[0].EventAt == nil || !evs[0].EventAt.Equal(fx.eventAt) {
		t.Fatalf("missed 审计的 EventAt 必须是事件时刻 %v，got %v", fx.eventAt, evs[0].EventAt)
	}
	if evs[0].Detail == "" {
		t.Fatal("missed 审计必须说明原因（前端/运维据此判断停机窗口）")
	}

	// 再次重启：missed 行本身带 event_at ⇒ 判为已处理，不得重复写。
	ev2 := NewEvaluator(fx.db, &sunRecorder{})
	ev2.LoadRules()
	ev2.evalTimeWindows(fx.eventAt.Add(12 * time.Minute))
	var cnt int64
	if err := fx.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", fx.rule.ID, models.AutomationResultMissedSolarEvent).
		Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("missed 审计重复写入：%d 条", cnt)
	}
}

// ── P1: 配置晚于事件时仍不补发（2026-11-17 裁决保持）──────────────────────
func TestSunEventStartupNoCatchUpWhenConfigNewer(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
	// UpdatedAt 在事件之后：模拟「事件后才保存/启用」。
	if err := fx.db.Exec("UPDATE automation_rules SET updated_at = ? WHERE id = ?",
		fx.eventAt.Add(time.Minute), fx.rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	rec := &sunRecorder{}
	ev := NewEvaluator(fx.db, rec)
	ev.LoadRules()
	ev.evalTimeWindows(fx.eventAt.Add(2 * time.Minute))
	if got := rec.count(); got != 0 {
		t.Fatalf("配置晚于事件的规则补发了 %d 次 —— 2026-11-17 裁决：不补发", got)
	}
	var cnt int64
	fx.db.Model(&models.AutomationEvent{}).Where("rule_id = ?", fx.rule.ID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("配置晚于事件不应落任何事件，got %d 条", cnt)
	}
}

// ── D22 + next_run: 跨午夜时「下一次」必须报 fireAt，而不是事件当日时刻 ──
func TestNextRunSunOffsetCrossMidnight(t *testing.T) {
	ev, rule, tz := sunRuleFixture(t, models.AutomationSunKindSunset, 300, 0)
	eventDay := time.Date(2026, 6, 21, 12, 0, 0, 0, tz)
	eventAt, status, err := sun.SunEvent(39.9042, 116.4074, tz, eventDay, sun.Sunset)
	if err != nil || status != sun.StatusOK {
		t.Fatalf("夹具前提失败：%s %v", status, err)
	}
	fireAt := eventAt.Add(300 * time.Minute)
	if fireAt.In(tz).Format("2006-01-02") == eventAt.In(tz).Format("2006-01-02") {
		t.Fatalf("夹具前提失败：+300 分钟没有把 fireAt 推出事件日（%s）", fireAt)
	}

	// ① 事件日 22:00（fireAt 前）：下一次必须是次日 00:46。
	now := time.Date(2026, 6, 21, 22, 0, 0, 0, tz)
	got := ev.NextRuns([]models.AutomationRule{rule}, now)[rule.ID]
	if got.Status != models.NextRunScheduled || got.At == nil || !got.At.Equal(fireAt) {
		t.Fatalf("at = %v, want %v（跨午夜 fireAt）", got.At, fireAt)
	}
	if !strings.Contains(got.Detail, "06-22") {
		t.Fatalf("detail = %q，跨午夜时必须点明触发日（否则用户以为规则配错）", got.Detail)
	}

	// ② 真的跨过 fireAt 后：下一次是后一日落 + 偏移。
	ev.evalTimeWindows(fireAt.Add(-time.Minute))
	ev.evalTimeWindows(fireAt.Add(time.Second))
	nextAt, status, err := sun.SunEvent(39.9042, 116.4074, tz, eventDay.AddDate(0, 0, 1), sun.Sunset)
	if err != nil || status != sun.StatusOK {
		t.Fatalf("次日日落求解失败：%s %v", status, err)
	}
	got2 := ev.NextRuns([]models.AutomationRule{rule}, fireAt.Add(time.Hour))[rule.ID]
	if got2.At == nil || !got2.At.Equal(nextAt.Add(300*time.Minute)) {
		t.Fatalf("after fire at = %v, want %v", got2.At, nextAt.Add(300*time.Minute))
	}
}

// ── P2: 精确唤醒（最近 fireAt 提前唤醒，1 分钟 tick 兜底）──────────────────
func TestNextSunWakeAt(t *testing.T) {
	ev, _, tz := sunRuleFixture(t, models.AutomationSunKindSunset, 0, 0)
	eventDay := time.Date(2026, 6, 21, 12, 0, 0, 0, tz)
	eventAt, _, _ := sun.SunEvent(39.9042, 116.4074, tz, eventDay, sun.Sunset)

	// 事件前：唤醒时刻 = 今日 fireAt。
	before := time.Date(2026, 6, 21, 8, 0, 0, 0, tz)
	if got := ev.nextSunWakeAt(before); !got.Equal(eventAt) {
		t.Fatalf("wakeAt = %v, want %v", got, eventAt)
	}
	// 事件后：下一次是次日 fireAt（不是零值、不是已过去的今天）。
	after := eventAt.Add(time.Minute)
	nextDay, _, _ := sun.SunEvent(39.9042, 116.4074, tz, eventDay.AddDate(0, 0, 1), sun.Sunset)
	if got := ev.nextSunWakeAt(after); !got.Equal(nextDay) {
		t.Fatalf("wakeAt(after) = %v, want %v", got, nextDay)
	}

	// 跨午夜偏移：唤醒时刻必须是 fireAt（次日 00:46），不是事件时刻。
	ev2, _, tz2 := sunRuleFixture(t, models.AutomationSunKindSunset, 300, 0)
	day2 := time.Date(2026, 6, 21, 12, 0, 0, 0, tz2)
	event2, _, _ := sun.SunEvent(39.9042, 116.4074, tz2, day2, sun.Sunset)
	wake := ev2.nextSunWakeAt(time.Date(2026, 6, 21, 22, 0, 0, 0, tz2))
	if !wake.Equal(event2.Add(300 * time.Minute)) {
		t.Fatalf("跨午夜 wakeAt = %v, want %v", wake, event2.Add(300*time.Minute))
	}
}

// 没有 sun 规则时返回零值（循环按 1 分钟 tick 兜底）。
func TestNextSunWakeAtNoSunRules(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ev := NewEvaluator(db, &sunRecorder{})
	if got := ev.nextSunWakeAt(time.Now()); !got.IsZero() {
		t.Fatalf("无 sun 规则时 wakeAt = %v, want 零值", got)
	}
}

// ── P2: 跨进程认领（滚动发布/多副本只执行一次）────────────────────────────
func TestSunEventClaimSingleWinner(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunrise, 0)
	fireAt := fx.eventAt
	rec1 := &sunRecorder{}
	rec2 := &sunRecorder{}
	ev1 := NewEvaluator(fx.db, rec1)
	ev2 := NewEvaluator(fx.db, rec2)
	ev1.LoadRules()
	ev2.LoadRules()

	// 两边都先 tick 到事件前 2 分钟（建立 prevTick，避开首 tick 补发分支）。
	ev1.evalTimeWindows(fireAt.Add(-2 * time.Minute))
	ev2.evalTimeWindows(fireAt.Add(-2 * time.Minute))
	// 先后跨过同一场事件：唯一键只让一个 INSERT 成功。
	ev1.evalTimeWindows(fireAt.Add(time.Second))
	ev2.evalTimeWindows(fireAt.Add(time.Second))

	if got := rec1.count() + rec2.count(); got != 1 {
		t.Fatalf("同一场事件应只执行 1 次，got %d（rec1=%d rec2=%d）", got, rec1.count(), rec2.count())
	}
	var claims int64
	if err := fx.db.Model(&models.AutomationSunFire{}).Where("rule_id = ?", fx.rule.ID).
		Count(&claims).Error; err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("认领行 = %d, want 1", claims)
	}
}

// 认领表每日清理：14 天前的行删除，近期行保留。
func TestSunClaimPrune(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ev := NewEvaluator(db, &sunRecorder{})

	oldDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	recentDate := time.Now().Format("2006-01-02")
	for _, date := range []string{oldDate, recentDate} {
		if err := db.Create(&models.AutomationSunFire{
			RuleID: 1, EventDate: date, Kind: "sunrise", FireAt: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ev.lastSunClaimPrune = time.Time{} // 强制本次执行
	ev.pruneSunClaims()

	var cnt int64
	if err := db.Model(&models.AutomationSunFire{}).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("清理后认领行 = %d, want 1（只删 14 天前）", cnt)
	}
}
