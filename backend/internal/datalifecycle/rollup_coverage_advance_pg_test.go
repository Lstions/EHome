package datalifecycle

// rollup_coverage_advance_pg_test.go — P0（2026-11-07）：目标覆盖必须**随日历推进**，
// 不能随 unified_data 的 max(timestamp) 冻结。
//
// ============ 为什么这是停写的硬前置 ============
//
// 生产实测（Lead）：
//
//	telemetry_rollup_coverage  max(day) = 2026-11-06
//	今天(UTC)                           = 2026-11-07
//	rollup 表最晚桶                      = 2026-11-07 14:10Z  <- 数据在写，元数据不动
//
// 链条：
//	[1] live collector 写 rollup 行，但不写 coverage
//	[2] coverage 唯一写入者 = rollup_backfill
//	[3] 回填单元枚举依赖 SELECT min/max(timestamp) FROM unified_data
//	[4] 停写后 maxTS 冻结在 T_stop => T_stop 之后的日子永远不在 units 里
//	[5] RollupWindowCovered=false => 读路径回落
//	[6] >6h 窗口走不了帧表 => 只能读 unified_data => 最新行停在 T_stop
//	[7] **图表画到 T_stop 就断，HTTP 200，无告警**
//
// 而且它**现在就存在**：回填只在进程启动时跑一次，服务连续运行超过 2 天，
// coverage 就已经停止推进；只是满的 unified_data 让回落看起来正常。
//
// ============ 本用例钉什么 ============
//
// 造「unified_data 一行都没有 + 帧表有最近 3 天数据」的终态，跑回填，断言：
//  ① coverage 行出现了（不是"没有单元所以什么都没做"）
//  ② 覆盖到**昨天**（而不是停在某个更早的日期）
//  ③ 7 天窗口 RollupWindowCovered = true（读路径不会回落）
//
// 变异验证：把 discoverSource 改回读 unified_data 的 min/max(timestamp) ⇒ 本用例红。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// seedSparseFrames 造稀疏帧（每 step 一帧），够让回填看到"这些天有数据"。
func seedSparseFrames(t *testing.T, db *gorm.DB, deviceID uint, from, to time.Time,
	dict telemetry.SlotDictionary, names []string, step time.Duration) {
	t.Helper()
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frames []models.TelemetryFrame
	n := 0
	for ts := from; ts.Before(to); ts = ts.Add(step) {
		readings := make([]telemetry.Reading, 0, len(names))
		for i, name := range names {
			readings = append(readings, telemetry.Reading{Name: name, Value: float64(n + i)})
		}
		f, err := enc.EncodeFrame(deviceID, ts.UTC(), 0, readings)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
		n++
		if len(frames) >= 500 {
			if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 500).Error; err != nil {
				t.Fatalf("persist frames: %v", err)
			}
			frames = frames[:0]
		}
	}
	if len(frames) > 0 {
		if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 500).Error; err != nil {
			t.Fatalf("persist frames: %v", err)
		}
	}
}

// TestRollupCoverageAdvancesWithoutUnifiedData 是 P0 的主验收。
func TestRollupCoverageEnumerationDoesNotDependOnUnifiedData(t *testing.T) {
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
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM telemetry_rollup_coverage")
	db.Exec("DELETE FROM telemetry_rollup_1m")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	// 注入帧解码实现：datalifecycle 不认识 telemetry（会形成 test-only import cycle），
	// 生产由 main.go 注入，用例在这里注入等价实现。
	restoreDec := SetFrameDecoder(func(ctx context.Context, gdb *gorm.DB, deviceID uint, from, to time.Time) ([]FramePoint, error) {
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
	defer restoreDec()

	// 终态：unified_data 一行都没有（停写之后），帧表有最近 3 天数据。
	const dev = 9961
	now := time.Now().UTC()
	// 造 **9 天** 稀疏帧：7 天窗口的每一天都必须有真实数据支撑，
	// 否则 RollupWindowCovered 里更早的日子没有 coverage 行（那是诚实结果，不是 bug）。
	from := now.AddDate(0, 0, -9).Truncate(time.Hour)
	seedSparseFrames(t, db, dev, from, now, dict, []string{"current", "rsoc"}, 30*time.Minute)

	var unifiedCount int64
	if err := db.Raw("SELECT count(*) FROM unified_data").Scan(&unifiedCount).Error; err != nil {
		t.Fatalf("count unified: %v", err)
	}
	if unifiedCount != 0 {
		t.Fatalf("unified_data 应为空，实际 %d 行 —— 用例前置不成立", unifiedCount)
	}

	// 跑回填
	cfg := RollupBackfillConfig{Enabled: true, BatchSeconds: 3600}
	if err := RollupBackfillRunOnce(context.Background(), db, cfg); err != nil {
		t.Fatalf("回填失败: %v", err)
	}

	// ① coverage 必须有行
	weekAgo := now.AddDate(0, 0, -7)
	rows, err := ReadRollupCoverage(db, dev,
		weekAgo.Format("2006-01-02"), now.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("读 coverage: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("coverage 一行都没有 —— 回填单元枚举仍依赖 unified_data（本 P0 未修复）")
	}

	// ② 必须覆盖到**昨天**（今天按设计排除）
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	if _, ok := rows[yesterday]; !ok {
		got := make([]string, 0, len(rows))
		for d := range rows {
			got = append(got, d)
		}
		t.Fatalf("coverage 没推进到昨天 %s（现有 %v）—— "+
			"这正是生产现象：元数据停在旧日期而数据在写", yesterday, got)
	}

	// ③ 来源标记必须落库且为 frames。
	//
	// 这一天的 unified_data 是空的，所以来源**必然**是帧表。如果不记来源，将来
	// 排查「这天的 avg 为什么比那天高」时无法区分口径（帧表全采样 vs unified 收窄后），
	// 而这个信息随进程重启就永久丢失。
	for day, row := range rows {
		if row.Source != RollupSourceFrames {
			t.Fatalf("day=%s 的 source = %q, want %q —— 来源标记没写进元数据",
				day, row.Source, RollupSourceFrames)
		}
	}

	// ④ rollup **表里必须真的有数据**。
	//
	// 这条是我第一版漏掉的：用例只断言了"coverage 推进了"，而 coverage 在聚合读到
	// 0 行时会写 missing —— 于是"推进"这个观测**同时兼容**"正常工作"与"聚合根本没读到
	// 数据"。这是典型的假绿：断言覆盖了一个更弱的性质。
	var rollupRows int64
	if err := db.Raw("SELECT count(*) FROM " + TelemetryRollupTable).Scan(&rollupRows).Error; err != nil {
		t.Fatalf("count rollup: %v", err)
	}
	if rollupRows == 0 {
		t.Fatal("coverage 推进了，但 rollup 一行数据都没有 —— " +
			"枚举脱钩了、聚合还在读 unified_data（本 P0 的残留缺口）")
	}

	// ⑤ state 不能是 missing。
	//
	// missing 的语义是「**已验证该日无数据**」。而这一天帧表里有数据、
	// 上面的 rollupRows 也证明写出去了 => 把它标成 missing 就是**元数据在说谎**，
	// 而且 missing 是粘性的（unitAlreadyDone 永不重试）=> 永久错误。
	//
	// 这条比 ③ 更直接：它直接检查"元数据是否诚实"。
	for day, row := range rows {
		if row.State == RollupCoverageMissing {
			t.Fatalf("day=%s 被标成 missing，但帧表该日有数据 —— "+
				"missing 表示「已验证无数据」，标错会让次日自愈永久失效", day)
		}
	}

	// ③ 读路径判据：7 天窗口必须算"已覆盖"，否则长窗会回落
	// 注意末端用 **RollupCoverageCutoff()** 而不是 now：
	// RollupWindowCovered 有实时段护栏（to 晚于 now-liveLag 一律 false），
	// 读路径也是先截断再调用的。传 now 会永远 false —— 那是用法错误，不是覆盖缺失。
	covered, err := RollupWindowCovered(db, dev, weekAgo, RollupCoverageCutoff())
	if err != nil {
		t.Fatalf("RollupWindowCovered: %v", err)
	}
	if !covered {
		t.Fatal("7 天窗口 RollupWindowCovered = false —— 读路径会回落，停写后长窗将静默截断")
	}
}
