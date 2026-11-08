package datalifecycle

// rollup_backfill_unified_dropped_pg_test.go — 钉住「unified_data 被 DROP 后回填仍然工作」（task-31 A1）。
//
// ============ 这个用例钉的是什么 ============
//
// 回填的「按日期选源」原来是逐日一条 SQL：
//
//	SELECT count(*) FROM (SELECT 1 FROM unified_data WHERE ... LIMIT 1) t
//
// DROP unified_data 之后这条会报 `relation "unified_data" does not exist`，于是
// backfillUnit 返回 err => 整轮回填失败 => coverage 停止推进 => 长窗读回落 =>
// 回落读的也是不存在的表。**整条链断掉。**
//
// 修法不是「err != nil 就当没有」，那是把两种完全不同的情况混为一谈：
//
//	表不存在 = 正常终态（DROP 正是目标）  => 走帧表源
//	DB 挂了  = 异常                        => 必须失败并报警
//
// 所以判据换成**结构性**的「表是否存在」，探测一次后缓存到 runner 上。
//
// ⚠ 本文件的两条用例**必须成对存在**：
//
//	TestRollupBackfillSurvivesUnifiedDrop          表不存在 => 回填继续（走帧表）
//	TestRollupBackfillUnifiedProbeErrorStillFails  表存在但查不动 => 回填必须失败
//
// 只留前者的后果：把 err 分支改成 `return false, nil` 用例照样绿，
// 而数据库故障会被静默吞掉 —— 然后一路标 missing，而 **missing 是粘性的**，
// 等于把一次连接故障永久写成「这天没有数据」。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// wireTestFrameDecoder 注入帧解码实现并返回 restore。
//
// datalifecycle 不能直接 import telemetry 的解码器（会形成 test-only import cycle），
// 生产由 main.go 注入，用例在这里注入等价实现 —— 与 rollup_coverage_advance_pg_test.go 同一手法。
func wireTestFrameDecoder(t *testing.T, dict telemetry.SlotDictionary) func() {
	t.Helper()
	return SetFrameDecoder(func(ctx context.Context, gdb *gorm.DB, deviceID uint, from, to time.Time) ([]FramePoint, error) {
		window := to.Sub(from)
		pts, _, err := telemetry.DecodeRangeAnchoredBounded(
			ctx, gdb, dict, deviceID, from, to, nil, window*2, telemetry.MaxDecodeFrames)
		if err != nil {
			return nil, err
		}
		out := make([]FramePoint, len(pts))
		for i := range pts {
			out[i] = FramePoint{TS: pts[i].TS, Name: pts[i].Name, Value: pts[i].Value}
		}
		return out, nil
	})
}

func TestRollupBackfillSurvivesUnifiedDrop(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("建 rollup 表: %v", err)
	}
	if err := EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("建 coverage 表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM " + TelemetryRollupTable)
	db.Exec("DELETE FROM " + RollupCoverageTable)

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	defer wireTestFrameDecoder(t, dict)()

	// 造帧（不造 unified_data）—— DROP 之后的真实形态：帧表有数据，源表不存在。
	const dev = 9971
	dayStart := time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour)
	seedSparseFrames(t, db, dev, dayStart, dayStart.Add(30*time.Minute), dict,
		[]string{"current", "rsoc"}, 10*time.Second)

	// 前提：帧真的写进去了（否则后面「回填成功」可能是「没数据所以没报错」）。
	var frameCount int64
	db.Raw("SELECT count(*) FROM telemetry_frame").Scan(&frameCount)
	if frameCount == 0 {
		t.Fatal("帧表为空 —— 本用例的前提不成立")
	}

	// 把 unified_data 整个 DROP。
	if err := db.Exec("DROP TABLE IF EXISTS unified_data CASCADE").Error; err != nil {
		t.Fatalf("drop unified_data: %v", err)
	}
	// 确认真的没了 —— 否则下面的断言是在验一个假前提。
	if db.Migrator().HasTable("unified_data") {
		t.Fatal("unified_data 仍然存在 —— 本用例的前提不成立")
	}

	cfg := RollupBackfillConfig{Enabled: true, BatchSeconds: 1800}
	if err := NewRollupBackfillRunner(db, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("unified_data 被 DROP 后回填失败（应当自动改用帧表源）: %v\n"+
			"这条链断掉的后果：coverage 停止推进 => 长窗读回落 => 回落读的也是不存在的表", err)
	}

	// 真的写出了 rollup 行 —— 不能只看「没报错」。
	// 「没报错但一行没写」比直接失败更危险：coverage 会标 complete 而实际没有数据。
	var buckets int64
	// 列名是 bucket（见 models 的 gorm:"column:bucket"），不是 bucket_ts。
	db.Raw("SELECT count(DISTINCT bucket) FROM " + TelemetryRollupTable).Scan(&buckets)
	if buckets == 0 {
		t.Fatal("回填没有报错但一行 rollup 都没写出 —— " +
			"coverage 会标 complete 而实际没有数据，这比直接失败更危险")
	}

	// 来源必须记为帧表（证明真的走了帧表分支，而不是「碰巧表还在」）。
	var framesMarked int64
	db.Raw("SELECT count(*) FROM "+RollupCoverageTable+" WHERE source = ?",
		RollupSourceFrames).Scan(&framesMarked)
	if framesMarked == 0 {
		t.Fatalf("coverage 没有一行标记 source=%s —— 来源判定没有走到帧表分支", RollupSourceFrames)
	}
	t.Logf("unified_data 已 DROP => 回填仍写出 %d 个桶，%d 个单元标记为帧表源", buckets, framesMarked)
}
