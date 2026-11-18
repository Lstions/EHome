package automation

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// TestWindowEnterDoesNotBackfill 钉死 2026-11-18 与 sun_event 同一条裁决：
// **补发不做**。
//
// time_window 的 enter 边沿旧实现是 `inside && (!exists || !wasInside)`：
// `!exists` 表示"本次求值器还没有这条规则的历史状态"。它把"不知道"当成"刚刚进入"，
// 于是三种情况下会凭空执行一次设备动作：
//   ① 进程重启后第一次 tick 恰好落在窗口内；
//   ② 规则在窗口内才被创建/启用；
//   ③ 规则被编辑（windowStates 被清）时正处窗口内。
// 这三种都不是"跨过窗口边界"，而是补发。与 sun_event 2026-11-17 的裁决一致：
// 要立刻执行请用手动触发。
//
// 变异自证：把 evalWindowRule 里 enter 分支改回 `inside && (!exists || !wasInside)`，
// 本条用例立即变红（首次 tick 在窗口内就执行了动作）。
func TestWindowEnterDoesNotBackfill(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rule := windowRule(t, db, models.AutomationWindowEnter)

	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	// 首次求值就落在窗口内：没有历史状态 ⇒ 不得当作"刚进入"。
	ev.evalTimeWindows(windowAt(8, 30))
	if n := countFor(rec, rule.ID); n != 0 {
		t.Fatalf("重启后首个 tick 落在窗口内时补发了 %d 次 —— 裁决: 不补发, 请手动触发", n)
	}
	// 后续 tick 仍在窗口内：依旧只能等真正的下一段边沿。
	ev.evalTimeWindows(windowAt(9, 30))
	if n := countFor(rec, rule.ID); n != 0 {
		t.Fatalf("持续在窗口内的后续 tick 又补发了 %d 次", n)
	}
}

// TestWindowEnterFiresOnGenuineEdge 是上一条的**反向对照**：真的跨过边界时必须触发，
// 修复不得把 enter 边沿一起废掉。
func TestWindowEnterFiresOnGenuineEdge(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rule := windowRule(t, db, models.AutomationWindowEnter)

	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	ev.evalTimeWindows(windowAt(7, 30)) // 窗口外: 建立 wasInside=false
	if n := countFor(rec, rule.ID); n != 0 {
		t.Fatalf("窗口外不得触发, got %d", n)
	}
	ev.evalTimeWindows(windowAt(8, 30)) // 跨入窗口
	if n := countFor(rec, rule.ID); n != 1 {
		t.Fatalf("真正跨入窗口边沿应触发 1 次, got %d", n)
	}

	// 次日同一边沿仍要正常触发（跳过不是永久失效）。
	ev.evalTimeWindows(windowAt(11, 0))
	ev.evalTimeWindows(windowAt(8, 30).Add(24 * time.Hour))
	if n := countFor(rec, rule.ID); n != 2 {
		t.Fatalf("次日跨入窗口应再触发 1 次（累计 2）, got %d", n)
	}
}

// TestWindowRuleCreatedInsideWindowDoesNotBackfill：规则在窗口内才生效 ⇒ 当天不补发，
// 次日边沿正常触发。
func TestWindowRuleCreatedInsideWindowDoesNotBackfill(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	// 空规则集先求值一次（真实场景里求值器总在跑，规则是后加的）。
	ev.evalTimeWindows(windowAt(7, 30))
	rule := windowRule(t, db, models.AutomationWindowEnter)
	ev.LoadRules() // 规则在"窗口内"这一刻才进入求值器

	ev.evalTimeWindows(windowAt(8, 30))
	if n := countFor(rec, rule.ID); n != 0 {
		t.Fatalf("规则在窗口内新建时补发了 %d 次", n)
	}
	// 次日跨入窗口 → 正常触发。
	ev.evalTimeWindows(windowAt(7, 30).Add(24 * time.Hour))
	ev.evalTimeWindows(windowAt(8, 30).Add(24 * time.Hour))
	if n := countFor(rec, rule.ID); n != 1 {
		t.Fatalf("次日边沿应触发 1 次, got %d —— 修复不得让规则永久失效", n)
	}
}

// TestWindowExitEdgeStillFires：exit 分支本来就要求 exists && wasInside，
// 这条用例防止本次改动顺手把它改坏。
func TestWindowExitEdgeStillFires(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rule := windowRule(t, db, models.AutomationWindowExit)

	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	ev.evalTimeWindows(windowAt(8, 30))  // 窗口内
ev.evalTimeWindows(windowAt(10, 30)) // 跨出
	if n := countFor(rec, rule.ID); n != 1 {
		t.Fatalf("跨出窗口边沿应触发 1 次, got %d", n)
	}
}

// windowRule 造一条 08:00-10:00 的 time_window 规则（纯通知动作，不碰设备）。
func windowRule(t *testing.T, db *gorm.DB, edge string) models.AutomationRule {
	t.Helper()
	// 本文件的时刻用 time.UTC 构造（见 windowAt），所以规则时区必须是 UTC。
	tz := "UTC"
	rule := models.AutomationRule{
		Timezone:           &tz,
		Name:               "早高峰窗口-" + edge,
		Enabled:            true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00",
		TriggerWindowEnd:   "10:00",
		TriggerWindowEdge:  edge,
		ActionType:         models.AutomationActionNotification,
		ActionLevel:        "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return rule
}

// windowAt 返回当天固定时刻（用 UTC 构造；isInWindow 按 now 的 Location 解释时分）。
func windowAt(hour, minute int) time.Time {
	return time.Date(2026, 6, 21, hour, minute, 0, 0, time.UTC)
}
