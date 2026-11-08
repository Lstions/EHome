package backfill

// pg_percent_test.go — 进度百分比的**真 PG 端到端**验收（2026-11-07 percent 偏低 bug）。
//
// ============ 为什么这条必须跑在 PG 上、且必须是分区表 ============
//
// 生产的现象只在**多分区 + 部分分区为空**时才出现：6 个月分区里 4 个 0 行。
// testutil 的 SQLite/PG 平表里只有 1 个分区 ⇒ discoverPartitions 走
// discoverSingleRange ⇒ 永远 1 个分区 ⇒ **这个 bug 在平表上根本复现不出来**。
//
// 所以本用例先把 unified_data 迁移成月分区表（与生产同形），再只在其中两个月
// 灌数据 ⇒ 其余月份天然是空分区。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// pgPercentSample 是中途采样到的一次进度快照。
type pgPercentSample struct {
	Percent    float64
	FramesDone int64
	RowsDone   int64
	Batch      int
}

// TestPGPercentTracksFrameRatio 守契约级语义：**percent ≈ 实际帧完成比例**。
//
// 做法：把 unified_data 变成月分区表，只在「两个月」里灌数据（其余月为空分区），
// 跑回填，在每个批次边界采样 (percent, frames_done)，断言两者偏差 < 20%。
//
// 修复前：percent 天花板 = 有数据分区数 / 全部分区数 ≈ 2/8 = 25%，而 frames 已
// 接近 100% ⇒ 偏差远超 20%，用例红。
func TestPGPercentTracksFrameRatio(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	resetBackfillState(t, db)

	// 先在**平表**上灌数据，再迁移成分区表。
	//
	// 顺序很重要：MigrateTableToPartitioned 的 oldestRowMonthFor 决定它建哪些历史
	// 月分区。空表迁移 ⇒ oldest=now ⇒ 只建当月+未来分区 ⇒ 往过去的月份插数据会
	// 报 "no partition of relation found"。必须先有数据。
	//
	// 只在两个「过去的月份」灌数据，且两个月数据量不同（模拟生产分布）。
	//
	// 用相对当前时间的偏移，避免写死日期后随着时间推移分区名对不上。
	now := time.Now().UTC()
	// 选 **上月** 作为唯一有数据的月份：
	//
	// telemetry_frame 的滚动分区窗口是 [当月-1, 当月+3]（partitionRollaheadMonths=3 加
	// 上月兜底），回填要往那张分区表里写帧。用更早的月份会报
	// "no partition of relation telemetry_frame found"。上月在窗口内，
	// 而当月+1..+3 是**空分区** —— 正是复现生产的形状。
	monthA := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	monthB := monthA.AddDate(0, 1, 0)
	const (
		framesA = 60
		framesB = 30
	)

	seed := func(base time.Time, n int) {
		for i := 0; i < n; i++ {
			ts := base.Add(time.Duration(i) * time.Minute)
			seedPGProjectedRows(t, db, 14, ts, bmsFrame(float64(i)*0.001))
		}
	}
	seed(monthA.Add(2*time.Hour), framesA)
	seed(monthB.Add(2*time.Hour), framesB)
	const totalFrames = framesA + framesB

	// 现在把平表迁成月分区表：会自动为 monthA/monthB 建历史分区，
	// 当月与未来月份也会建（这些就是**空分区** —— 复现生产形状的关键）。
	if err := datalifecycle.MigrateTableToPartitioned(db, "unified_data", "unified_data_legacy"); err != nil {
		t.Fatalf("把 unified_data 迁移成分区表: %v", err)
	}

	var parts int
	db.Raw("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace " +
		"WHERE c.relkind = 'r' AND n.nspname = current_schema() AND c.relname LIKE 'unified_data_%'").Scan(&parts)
	if parts < 3 {
		t.Fatalf("前置条件不符：只建出 %d 个月分区，本用例需要至少 3 个（含空分区）才复现得 LEAD 报告的 bug", parts)
	}
	t.Logf("分区数 = %d（其中只有 2 个有数据），总帧数 = %d", parts, totalFrames)

	r := newPGRunner(t, db, Config{BatchFrames: 5})
	var samples []pgPercentSample
	r.testHookAfterBatch = func(batch int) error {
		p := r.Progress()
		samples = append(samples, pgPercentSample{
			Percent:    p.Percent,
			FramesDone: p.FramesDone,
			RowsDone:   p.RowsDone,
			Batch:      batch,
		})
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	prog, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("回填失败: %v（进度=%+v）", err, prog)
	}
	if prog.State != StateDone {
		t.Fatalf("回填未完成 state=%q errors=%v", prog.State, prog.Errors)
	}
	if prog.FramesDone != totalFrames {
		t.Fatalf("frames_done = %d, want %d", prog.FramesDone, totalFrames)
	}

	// ── 核心断言：中途每一刻 percent 都必须贴近真实帧比例 ──
	if len(samples) < 3 {
		t.Fatalf("只采到 %d 个样本，不足以验证中途进度（调小批大小）", len(samples))
	}
	var worst float64
	var worstAt pgPercentSample
	for _, s := range samples {
		if s.FramesDone == 0 {
			continue // 第一批之前没有分母意义
		}
		want := float64(s.FramesDone) / float64(totalFrames) * 100
		dev := s.Percent - want
		if dev < 0 {
			dev = -dev
		}
		if dev > worst {
			worst = dev
			worstAt = s
		}
	}
	t.Logf("采样 %d 次；最大偏差 = %.1f 个百分点（batch=%d: percent=%.1f, frames_done=%d/%d = %.1f%%）",
		len(samples), worst, worstAt.Batch, worstAt.Percent, worstAt.FramesDone, totalFrames,
		float64(worstAt.FramesDone)/float64(totalFrames)*100)
	if worst > 20 {
		t.Errorf("percent 与真实帧比例最大偏差 = %.1f 个百分点 > 20%% 上限。\n"+
			"batch=%d: percent=%.1f 但 frames_done=%d/%d=%.1f%%。\n"+
			"这正是 2026-11-07 生产 bug：空分区按时间跨度分权重，稀释了真实进度。",
			worst, worstAt.Batch, worstAt.Percent, worstAt.FramesDone, totalFrames,
			float64(worstAt.FramesDone)/float64(totalFrames)*100)
	}

	// ── percent 必须单调不减（进度条倒退会让人以为出了事）──
	prev := -1.0
	for _, s := range samples {
		if s.Percent < prev {
			t.Errorf("batch=%d percent 回退: %.1f → %.1f", s.Batch, prev, s.Percent)
		}
		prev = s.Percent
	}

	// ── 跑完后每个分区在内存里都必须是 done（含空分区）──
	snap := r.Progress()
	var notDone []string
	for _, pp := range snap.Partitions {
		if pp.State != datalifecycle.BackfillDone {
			notDone = append(notDone, fmt.Sprintf("%s=%s", pp.Name, pp.State))
		}
	}
	if len(notDone) > 0 {
		t.Errorf("回填完成后仍有分区不是 done: %v（空分区也必须被标 done 并同步到内存）", notDone)
	}
	if snap.Percent != 100 {
		t.Errorf("最终 percent = %v, want 100", snap.Percent)
	}
}

// TestPGProgressPartitionsCoverAllDiscovered 守契约要求 2：
// r.progress.Partitions 必须为 r.parts 里的**每个**分区都建一条（含空分区）。
func TestPGProgressPartitionsCoverAllDiscovered(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	resetBackfillState(t, db)

	now := time.Now().UTC()
	// 选 **上月** 作为唯一有数据的月份：
	//
	// telemetry_frame 的滚动分区窗口是 [当月-1, 当月+3]（partitionRollaheadMonths=3 加
	// 上月兜底），回填要往那张分区表里写帧。用更早的月份会报
	// "no partition of relation telemetry_frame found"。上月在窗口内，
	// 而当月+1..+3 是**空分区** —— 正是复现生产的形状。
	monthA := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	// 先灌数据（平表），再迁移 —— 否则过去的月份没有分区可写。
	for i := 0; i < 12; i++ {
		seedPGProjectedRows(t, db, 14, monthA.Add(2*time.Hour+time.Duration(i)*time.Minute), bmsFrame(0))
	}
	if err := datalifecycle.MigrateTableToPartitioned(db, "unified_data", "unified_data_legacy"); err != nil {
		t.Fatalf("迁移分区表: %v", err)
	}

	r := newPGRunner(t, db, Config{BatchFrames: 4})
	parts, err := r.discoverPartitions(context.Background())
	if err != nil {
		t.Fatalf("discoverPartitions: %v", err)
	}
	if len(parts) < 3 {
		t.Fatalf("发现 %d 个分区，want >= 3（需要空分区才复现 bug）", len(parts))
	}
	if err := r.reloadProgressRows(context.Background(), parts); err != nil {
		t.Fatalf("reloadProgressRows: %v", err)
	}
	snap := r.Progress()
	if len(snap.Partitions) != len(parts) {
		t.Fatalf("Progress().Partitions 有 %d 条，discoverPartitions 发现 %d 个 —— "+
			"空分区没有进进度列表，运维看不到它们，percent 也会漏算", len(snap.Partitions), len(parts))
	}
	seen := make(map[string]bool, len(snap.Partitions))
	for _, p := range snap.Partitions {
		seen[p.Name] = true
	}
	for _, p := range parts {
		if !seen[p.name] {
			t.Errorf("分区 %s 不在 Progress().Partitions 里", p.name)
		}
	}
}

// 编译期断言：确保 telemetry 包被本文件使用（bmsFrame 之外的依赖）。
var _ = telemetry.SpecVersion
