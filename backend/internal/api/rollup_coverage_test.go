package api

// rollup_coverage_test.go — L3 rollup 的**覆盖率判据**（task-22 最容易出事的地方）。
//
// ============ 这个判据为什么必须有 ============
//
// 旧实现：`len(buckets) > 0` ⇒ handled=true。回填进行到一半时，窗口里一半分钟有
// rollup、一半没有 ⇒ 返回**半截数据** ⇒ 图表中间凭空少一段。
// 这比空图更难发现：横轴还在，只是那几天的线没了。
//
// ============ 判据为什么不能是「应有桶数 vs 实际桶数」============
//
// 两条致命缺陷（详见 datalifecycle/rollup_coverage.go 文件头）：
//
//	缺陷一：设备天然有停机间隙。按窗口长度硬算"应有桶数"会在**正确的系统上**
//	  判错 —— 设备 10:00 上线、10:30 下线，真实桶数 30 而硬算 60 ⇒ 永远判
//	  "覆盖不足" ⇒ 永远回落 ⇒ 回填白做。
//	  ⇒ 本文件用 TestRollupMissingDayStillServes 专门钉住"停机不会误伤"。
//
//	缺陷二：算"应有桶数"必须查源数据实际有哪些分钟，而那个源就是 unified_data
//	  —— 正是回填完成后要删掉的表。用即将删除的表当判据 = 自相矛盾。
//
// 所以判据读的是**回填时写下的元数据**（telemetry_rollup_coverage），运行时不碰
// unified_data。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
)

// TestRollupPartialCoverageFallsBack 是 task-22 的**核心守卫**。
//
// 造一个「窗口跨两天，只有第一天回填过」的形状 —— 这正是回填进行中的真实形态。
// 断言必须回落，否则用户会看到图表中间凭空少一天。
//
// **变异验证目标**：注释掉 rollupHistoryPoints 里的 `if !covered { return ... }`，
// 本用例必须变红（handled 会变成 true 并返回半截数据）。
func TestRollupPartialCoverageFallsBack(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// 两天各有 60 分钟数据（22:00~24:00 + 00:00~01:00 的跨日形状不重要，
	// 关键是"两天都有行，但只有一天有覆盖元数据"）。
	seedRollupMinutes(t, db, 14, start.Add(22*time.Hour), 60)     // 11-01 有数据 + 有覆盖
	seedRollupMinutesOnly(t, db, 14, start.Add(48*time.Hour), 60) // 11-03 有数据但**没有**覆盖

	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start.Add(22 * time.Hour),
		End:          start.Add(49 * time.Hour), // 跨 11-01 / 11-02 / 11-03 三天
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if handled {
		t.Fatalf("窗口跨过未回填的日期却 handled=true（返回 %d 个点）—— "+
			"用户会看到图表中间凭空少一段，比空图更难发现", len(points))
	}
	if points != nil {
		t.Fatalf("回落时必须返回 nil 点集（由调用方走 unified_data），got %d 点", len(points))
	}
}

// TestRollupNoCoverageMetadataFallsBack 守「有数据行但完全没有覆盖元数据」。
//
// 这是**升级/迁移**的真实形态：表里已经有 live 写入的行，但历史回填还没跑过、
// 覆盖表是空的。此时绝不能用 rollup —— 因为我们不知道它覆盖了哪一段。
func TestRollupNoCoverageMetadataFallsBack(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRollupMinutesOnly(t, db, 14, start, 120) // 只有行，没有覆盖元数据

	_, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(2 * time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if handled {
		t.Fatal("没有覆盖元数据时不能 handled=true —— 我们不知道它覆盖了哪一段")
	}
}

// TestRollupPartialStateFallsBack 守 state=partial（回填开始但没跑完）。
func TestRollupPartialStateFallsBack(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, start, 60)
	// 把这一天的状态改成 partial（模拟"回填被中断，这一天没跑完"）。
	if err := datalifecycle.MarkRollupCoverage(db, 14, datalifecycle.RollupCoverageDay(start), 60,
		datalifecycle.RollupCoveragePartial); err != nil {
		t.Fatalf("mark partial: %v", err)
	}

	_, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if handled {
		t.Fatal("state=partial 必须回落 —— 这一天没跑完，数据可能不全")
	}
}

// TestRollupMissingDayStillServes 守**停机设备不会被误伤**（陷阱一的回归测试）。
//
// 形态：设备 11-02 全天离线 ⇒ 这一天**本来就没有分钟桶**。回填把这一天记为
// missing（已验证、buckets=0）。读路径必须**照常服务**（返回 11-01 和 11-03 的点），
// 而不是因为"11-02 没数据"就回落。
//
// 这条如果失败，说明判据退化成了"按窗口长度硬算应有桶数"——那会让所有有停机
// 间隙的设备永远回落，回填彻底白做。
func TestRollupMissingDayStillServes(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// 11-01 有数据；11-02 整天没有（设备离线）；11-03 有数据。
	seedRollupMinutes(t, db, 14, start, 60)
	seedRollupMinutes(t, db, 14, start.AddDate(0, 0, 2), 60)
	// 11-02 记为 missing（已验证"这一天本来就没数据"）。
	if err := datalifecycle.MarkRollupCoverage(db, 14,
		datalifecycle.RollupCoverageDay(start.AddDate(0, 0, 1)), 0,
		datalifecycle.RollupCoverageMissing); err != nil {
		t.Fatalf("mark missing: %v", err)
	}

	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.AddDate(0, 0, 3),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if !handled {
		t.Fatal("设备中途离线一天不该让整个窗口回落 —— 那会让所有有停机间隙的设备永远回落，" +
			"历史回填彻底白做（这正是「按窗口长度硬算桶数」的致命缺陷）")
	}
	if len(points) < 2 {
		t.Fatalf("点集 = %d, want ≥2（11-01 与 11-03 各有数据）", len(points))
	}
}

// seedRollupMinutesOnly 只写 rollup 行、**不写**覆盖元数据。
//
// 用来构造「表里有数据但覆盖状态未知」的形态（升级中途 / 迁移未跑）。
func seedRollupMinutesOnly(t *testing.T, db *gorm.DB, deviceID uint, start time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		payload, err := datalifecycle.EncodeRollupPayloadV1(map[string]*datalifecycle.RollupFieldStat{
			"rsoc": {Avg: float64(50 + i), Min: float64(50 + i), Max: float64(50 + i), Last: float64(50 + i), Count: 60},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := db.Exec("INSERT INTO telemetry_rollup_1m (device_id, bucket, payload) VALUES (?, ?, ?)",
			deviceID, start.Add(time.Duration(i)*time.Minute), payload).Error; err != nil {
			t.Fatalf("seed rollup row %d: %v", i, err)
		}
	}
}
