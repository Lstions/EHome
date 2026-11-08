package backfill

// percent_test.go — 进度百分比的权重口径（2026-11-07 生产 percent 偏低 bug 的回归）。
//
// ============ 生产现象 ============
//
//	GET /api/v1/system/backfill → percent=13.1, frames_done=1,546,000
//	实际：1,546,000 / 3,419,991 = 45.2% 的帧已回填
//
// **进度条显示 13%，实际完成 45%。**
//
// ============ 两个叠加的缺陷 ============
//
// 1) **权重口径错了**：原实现按分区**时间跨度**等权。生产 6 个月分区里 4 个是
//    空分区（0 行），只有 202610/202611 有数据。按时间跨度 ⇒ 分母恒为 6 个月、
//    分子最多 2 个月 ⇒ **percent 天花板 33%**。空分区的时间跨度不是工作量。
//
// 2) **内存不同步**：markPartitionRunning/Done/Failed 只写数据库，不改
//    r.progress.Partitions。空分区不经过 advanceWatermark（那里才有内存同步），
//    所以它们在内存里永远是 pending ⇒ recomputePercent 的 switch 不匹配任何
//    分支 ⇒ 计 0 分。
//
// 两条独立成因，**必须分别守**：只修 (1) 会让空分区因为 (2) 仍算 0 分；
// 只修 (2) 会让 4 个空分区在启动瞬间各贡献 1/6 ⇒ 进度条一开局跳到 66.7%。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
)

// syntheticPartitions 造一组分区（4 个空 + 2 个有数据），模拟生产形状。
func syntheticPartitions() []partition {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := make([]partition, 0, 6)
	// 生产实测的行数分布（契约 §3）：202610 约 5,200 万行、202611 约 2,000 万行，
	// 其余月份 0 行。这里按比例缩小，但**空分区的存在**是关键。
	rows := []int64{0, 52_000_000, 20_000_000, 0, 0, 0}
	for i := 0; i < 6; i++ {
		lo := base.AddDate(0, i, 0)
		out = append(out, partition{
			name: r6Name(lo),
			lo:   lo,
			hi:   lo.AddDate(0, 1, 0),
			rows: rows[i],
		})
	}
	return out
}

func r6Name(lo time.Time) string { return "unified_data_" + lo.Format("200601") }

// TestPercentWeightsByRowsNotSpan 守权重口径：空分区不该稀释进度。
//
// 断言方式：把两个有数据的分区都标 done，percent 必须是 **100**。
// 旧实现（时间跨度等权）会得到 2/6 ≈ 33.3%。
func TestPercentWeightsByRowsNotSpan(t *testing.T) {
	r := &Runner{parts: syntheticPartitions()}
	r.progress.Partitions = []PartitionProgress{
		{Name: r6Name(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillDone},
		{Name: r6Name(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillDone},
		{Name: r6Name(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillDone},
	}
	r.recomputePercent()
	if got := r.progress.Percent; got != 100 {
		t.Fatalf("percent = %v, want 100（两个有数据的分区都 done ⇒ 100%%；"+
			"得到 33.3 说明还在按时间跨度加权，空分区稀释了进度）", got)
	}
}

// TestPercentIgnoresEmptyPartitionsRunning 守空分区在 running/done 混合下也不稀释。
//
// 场景：202610 已 done（52M 行），202611 正在跑且消费了 10M 行（共 20M）。
// 期望 percent = (52 + 10) / 72 ≈ 86.1%，**不是** 33.3%。
func TestPercentIgnoresEmptyPartitionsRunning(t *testing.T) {
	r := &Runner{parts: syntheticPartitions()}
	r.progress.Partitions = []PartitionProgress{
		{Name: r6Name(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillDone},
		{Name: r6Name(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillRunning, RowsDone: 10_000_000},
	}
	r.recomputePercent()
	want := 86.1 // (52M + 10M) / 72M = 86.11%
	if got := r.progress.Percent; got < want-0.2 || got > want+0.2 {
		t.Fatalf("percent = %v, want ≈%v（(52M+10M)/72M）", got, want)
	}
}

// TestPercentEmptyPartitionDoneDoesNotJump 守反方向：空分区被标 done 不能让进度虚高。
//
// 这条是「另一种修法」的守卫：如果为了让空分区计入进度而给它们分权重，
// 进度条会在启动瞬间跳到 4/6 ≈ 66.7%。空分区必须**零权重**。
func TestPercentEmptyPartitionDoneDoesNotJump(t *testing.T) {
	r := &Runner{parts: syntheticPartitions()}
	// 只把 4 个空分区标 done，有数据的一个都没动。
	var items []PartitionProgress
	for _, p := range r.parts {
		st := datalifecycle.BackfillPending
		if p.rows == 0 {
			st = datalifecycle.BackfillDone
		}
		items = append(items, PartitionProgress{Name: p.name, State: st})
	}
	r.progress.Partitions = items
	r.recomputePercent()
	if got := r.progress.Percent; got != 0 {
		t.Fatalf("percent = %v, want 0（4 个空分区 done 但**零行**回填 ⇒ 真实进度就是 0%%；"+
			"非 0 说明空分区被分了权重，进度条会谎报）", got)
	}
}

// TestPercentFallsBackToSpanWhenCountingFails 守降级路径。
//
// count(*) 全失败时 rows 全为 0 ⇒ 退回时间跨度加权。此时不能显示 0%（那会让
// 运维以为回填卡住了），要按时间推进给出合理估算。
func TestPercentFallsBackToSpanWhenCountingFails(t *testing.T) {
	parts := syntheticPartitions()
	for i := range parts {
		parts[i].rows = 0 // 模拟 count(*) 全失败
	}
	r := &Runner{parts: parts}
	// 第 2 个分区（10 月）running 且 watermark 走到月中。
	mid := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	r.progress.Partitions = []PartitionProgress{
		{Name: r6Name(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), State: datalifecycle.BackfillRunning, Watermark: &mid},
	}
	r.recomputePercent()
	// 时间跨度口径：6 个月，只有 10 月走了一半 ⇒ ≈ (1 + 0.5) / 6 ≈ 25%。
	if got := r.progress.Percent; got <= 0 || got >= 34 {
		t.Fatalf("percent = %v, want 0 < p < 34（降级路径必须给出非零估算）", got)
	}
}

// TestMarkPartitionDoneSyncsMemory 守**成因 (2)**：标 done 必须同步内存状态。
//
// 这条守的是「空分区不经过 advanceWatermark，只在 markPartitionDone 里被标 done」
// 的路径 —— 不同步的话，内存里永远是 pending。
//
// ⚠ 这是变异验证的目标：把 markPartitionDone 里的 setPartitionState 注释掉 ⇒ 红。
func TestMarkPartitionDoneSyncsMemory(t *testing.T) {
	db := openBackfillTestDB(t)
	dict := newPGDict(t, db)
	_ = dict
	r := NewRunner(db, nil, Config{}, false, nil)
	parts := syntheticPartitions()
	r.parts = parts
	// 内存快照：全 pending（模拟 reloadProgressRows 刚跑完）。
	r.progress.Partitions = make([]PartitionProgress, 0, len(parts))
	for _, p := range parts {
		r.progress.Partitions = append(r.progress.Partitions, PartitionProgress{
			Name: p.name, State: datalifecycle.BackfillPending,
		})
	}
	// 建出状态行（markPartitionDone 要 UPDATE 它们）。
	for _, p := range parts {
		if err := db.Create(&datalifecycle.BackfillProgressRow{
			SrcTable: r.srcTable(), PartName: p.name, State: datalifecycle.BackfillPending,
		}).Error; err != nil {
			t.Fatalf("seed progress row %s: %v", p.name, err)
		}
	}

	empty := parts[0] // 空分区（0 行）—— 正是生产上被漏掉的那 4 个
	if err := r.markPartitionDone(context.Background(), empty); err != nil {
		t.Fatalf("markPartitionDone: %v", err)
	}

	snap := r.Progress()
	var got *PartitionProgress
	for i := range snap.Partitions {
		if snap.Partitions[i].Name == empty.name {
			got = &snap.Partitions[i]
		}
	}
	if got == nil {
		t.Fatalf("Progress().Partitions 里没有分区 %s", empty.name)
	}
	if got.State != datalifecycle.BackfillDone {
		t.Fatalf("内存快照 state = %q, want %q —— markPartitionDone 只写了库、没同步内存。\n"+
			"后果：空分区在 recomputePercent 里永远是 pending ⇒ 计 0 分 ⇒ 进度条严重偏低。",
			got.State, datalifecycle.BackfillDone)
	}
	if got.Watermark == nil || !got.Watermark.Equal(empty.hi) {
		t.Errorf("done 分区的 watermark = %v, want 分区上界 %v（接口看到的水位应走满）", got.Watermark, empty.hi)
	}
}
