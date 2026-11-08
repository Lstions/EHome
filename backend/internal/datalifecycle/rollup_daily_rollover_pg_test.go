package datalifecycle

// rollup_daily_rollover_pg_test.go — A-改（每日 ticker）的验收（Lead 2026-11-07 要求③）。
//
// ============ 为什么必须用 fake clock ============
//
// 要验的是「服务**连续运行 3 天**，coverage 每天推进一格」。
// 真实等待不可能进 CI，所以用已有的 rollupCoverageNow seam 推进时钟。
//
// ============ 为什么这个 seam 是复用而不是新造 ============
//
// 回填原来用 time.Now()，coverage 用 rollupCoverageNow()。两个时钟**必须一致**：
// buildRollupBackfillUnits 用 one 算「今天」来排除，RollupWindowCovered 用另一个算
// 「今天」来过筛 —— 不一致会让单元范围与判据错位。
// 所以本次把回填的三处 time.Now() 统一到 rollupCoverageNow()，一个子系统只有一个时钟。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// TestRollupCoverageAdvancesAcrossDays 模拟服务连续运行 3 天。
func TestRollupCoverageAdvancesAcrossDays(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("建 rollup 表: %v", err)
	}
	if err := EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("建 coverage 表: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM " + TelemetryRollupTable)
	db.Exec("DELETE FROM " + RollupCoverageTable)

	const dev = 9962
	// 基准日：5 天前的 UTC 午夜。数据覆盖 base .. base+5d。
	base := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -5)
	var rows []models.UnifiedData
	for ts := base; ts.Before(base.AddDate(0, 0, 5)); ts = ts.Add(30 * time.Minute) {
		rows = append(rows, models.UnifiedData{
			DeviceID: dev, SensorName: "current",
			Value: float64(ts.Unix() % 100), Unit: "A", Timestamp: ts, CreatedAt: ts,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}

	covDays := func() int64 {
		var n int64
		db.Raw("SELECT count(*) FROM "+RollupCoverageTable+" WHERE device_id = ?", dev).Scan(&n)
		return n
	}

	cfg := RollupBackfillConfig{Enabled: true, BatchSeconds: 21600}
	var prev int64
	for day := 1; day <= 3; day++ {
		// 模拟第 day 天 00:30 触发每日 rollover
		fakeNow := base.AddDate(0, 0, day).Add(30 * time.Minute)
		restore := SetRollupCoverageClock(func() time.Time { return fakeNow })

		if err := RollupBackfillRunOnce(context.Background(), db, cfg); err != nil {
			restore()
			t.Fatalf("第 %d 天回填失败: %v", day, err)
		}
		restore()

		got := covDays()
		t.Logf("第 %d 天（fake now=%s）：coverage 共 %d 天（上一轮 %d）",
			day, fakeNow.Format("2006-01-02T15:04Z"), got, prev)
		if got <= prev {
			t.Fatalf("第 %d 天 coverage 没有推进（%d -> %d）—— "+
				"这正是「服务连续运行多日，coverage 停止推进」的形态", day, prev, got)
		}
		// 必须推进到「昨天」：fakeNow 当天不该有覆盖行（今天由 live collector 负责）
		yesterday := fakeNow.AddDate(0, 0, -1).Format("2006-01-02")
		var has int64
		db.Raw("SELECT count(*) FROM "+RollupCoverageTable+
			" WHERE device_id = ? AND day = ?", dev, yesterday).Scan(&has)
		if has == 0 {
			t.Fatalf("第 %d 天：没有推进到昨天 %s", day, yesterday)
		}
		prev = got
	}
}

// TestNextRollupRolloverAt 钉住 rollover 时刻计算（纯函数，不需要 DB）。
func TestNextRollupRolloverAt(t *testing.T) {
	utc := func(h, m int) time.Time {
		return time.Date(2026, 11, 7, h, m, 0, 0, time.UTC)
	}
	cases := []struct {
		name      string
		now, want time.Time
	}{
		{"午夜前", utc(0, 0), utc(0, 5)},
		{"已过 00:05", utc(0, 6), utc(0, 5).AddDate(0, 0, 1)},
		{"中午", utc(12, 0), utc(0, 5).AddDate(0, 0, 1)},
		{"恰好 00:05", utc(0, 5), utc(0, 5).AddDate(0, 0, 1)},
		{"23:59", utc(23, 59), utc(0, 5).AddDate(0, 0, 1)},
	}
	for _, c := range cases {
		if got := NextRollupRolloverAt(c.now); !got.Equal(c.want) {
			t.Errorf("%s: now=%s got=%s want=%s", c.name,
				c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}
