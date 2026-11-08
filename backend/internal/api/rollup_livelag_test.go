package api

// rollup_livelag_test.go — 「今天」的实时段护栏（Lead 2026-11-07 裁决）。
//
// ============ 这个洞是什么 ============
//
// RollupCollector 写的是**当前分钟**；历史回填只能从 unified_data 聚合**已落库**的行。
// 如果今天这一天被回填跑过并置了 complete，那么**今天尚未发生的分钟也被算作覆盖**：
//
//	过去日期：置 complete 时该天所有分钟都已发生 ⇒ 判据正确
//	今天    ：置 complete 时今天只过了 N 分钟 ⇒ 剩余 1440-N 分钟被判"覆盖"但其实是空
//
// 表现：**最近一小时的图表偶发空洞** —— 而那恰恰是用户最容易看到的一段时间。
//
// 处理：窗口末端落在「当前时间 - liveLag(120s)」之后的，一律回落。
// **不追求"今天也走 rollup"：先正确，再快。**

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
)

// TestRollupRecentWindowFallsBack 守「窗口末端太靠近现在 ⇒ 回落」。
func TestRollupRecentWindowFallsBack(t *testing.T) {
	db := newRollupReadDB(t)
	// 用固定时钟排除"跑用例时恰好跨分钟"的抖动：现在 = 2026-11-05T12:00:00Z。
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	restore := datalifecycle.SetRollupCoverageClock(func() time.Time { return now })
	defer restore()

	// 造一整天的完整覆盖（模拟"今天已被回填跑过并置 complete"）。
	dayStart := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, dayStart, 720) // 12 小时 = 到"现在"为止

	// 窗口末端 = 现在（实时段）：必须回落。
	_, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        dayStart,
		End:          now,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if handled {
		t.Fatal("窗口末端 = 现在 却 handled=true —— 今天尚未发生的分钟会被当成有数据，" +
			"最近一小时的图表会偶发空洞（用户最容易看到的那段）")
	}
}

// TestRollupWindowJustBeforeLiveLagServes 是**反向守卫**：
// 「永远返回 false」的假实现也能让上面那条绿，这条挡住它。
func TestRollupWindowJustBeforeLiveLagServes(t *testing.T) {
	db := newRollupReadDB(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	restore := datalifecycle.SetRollupCoverageClock(func() time.Time { return now })
	defer restore()

	dayStart := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, dayStart, 720)

	// 窗口末端 = 现在 - liveLag - 1s：**刚好在护栏外**，应当正常服务。
	end := now.Add(-datalifecycle.RollupCoverageLiveLag - time.Second)
	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        dayStart,
		End:          end,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if !handled {
		t.Fatal("窗口末端已在 liveLag 之外却 handled=false —— 护栏把正常窗口也挡掉了")
	}
	if len(points) == 0 {
		t.Fatal("应当返回点集")
	}
}

// TestRollupWindowDaysHalfOpen 守日期枚举用**半开区间**（to-1ns）。
//
// 为什么这条重要：若用闭区间，`end` 恰好对齐午夜（最常见的"最近 N 天"形状）时
// 会多算一天，而那一天在窗口内**一秒都没有** ⇒ 要求它有覆盖元数据 ⇒ 所有对齐
// 到午夜的正常窗口一律回落。这不是保守，是错判。
func TestRollupWindowDaysHalfOpen(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// 恰好 3 天：应只有 11-01/02/03 三天，不含 11-04。
	days := datalifecycle.RollupWindowDays(start, start.AddDate(0, 0, 3))
	want := []string{"2026-11-01", "2026-11-02", "2026-11-03"}
	if len(days) != len(want) {
		t.Fatalf("days = %v, want %v（end 对齐午夜时不能多算一天）", days, want)
	}
	for i := range want {
		if days[i] != want[i] {
			t.Fatalf("days = %v, want %v", days, want)
		}
	}

	// 末端多 1 秒 ⇒ 跨入 11-04，必须包含。
	days = datalifecycle.RollupWindowDays(start, start.AddDate(0, 0, 3).Add(time.Second))
	if len(days) != 4 || days[3] != "2026-11-04" {
		t.Fatalf("days = %v, want 四天且末位 2026-11-04", days)
	}

	// 跨日但不跨午夜的短窗口：同一天。
	days = datalifecycle.RollupWindowDays(
		time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC))
	if len(days) != 1 || days[0] != "2026-11-01" {
		t.Fatalf("days = %v, want [2026-11-01]", days)
	}

	// 空窗口（to == from）⇒ nil。
	if days := datalifecycle.RollupWindowDays(start, start); days != nil {
		t.Fatalf("空窗口应返回 nil, got %v", days)
	}
}
