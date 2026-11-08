package api

// rollup_tail_parity_pg_test.go — **end=now** 的「rollup 主体 + 帧表尾巴」对拍（task-22）。
//
// ============ 为什么这条用例是「删 12 GB」的最后一道闸 ============
//
// 前端的 preset（1h/24h/7d）和后端 hours=N 形态，**End 都是 time.Now()**
// （handler_data.go 的 hours 分支 / HistoryChartSection.vue 的 getTimeRange）。
// 而 liveLag 会挡掉「末端在 now-liveLag 之后」的窗口。
//
// 如果按「整窗回落」处理，长窗 rollup **永远不生效** ⇒ 删掉 unified_data 之后
// 7d/30d 图表会变成空页。所以读路径必须**截断 + 拼接**：
//
//	rollup  服务 [Start, now-liveLag)   ← 主体
//	帧表解码服务 [now-liveLag, now)     ← 尾巴（≤2 分钟）
//
// 本用例断言：这条拼接路径与「单源 unified_data 查询整个窗口」**逐点一致**。
//
// 尾巴必须用帧表（不能用 unified_data）—— 那正是要删的表。

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestRollupLiveTailParity 用 end=now 的 7d 窗口对拍「主体+尾巴」与 unified_data。
func TestRollupLiveTailParity(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("migrate rollup: %v", err)
	}
	if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("ensure coverage: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM " + datalifecycle.TelemetryRollupTable)
	db.Exec("DELETE FROM " + datalifecycle.RollupCoverageTable)
	// ============ 前置：本用例的基准是 unified_data（2026-11-08）============
	//
	// 硬切换后 unified_data 已 DROP，本用例的基准（内联的同口径 SQL 聚合，
	// 对 unified_data 执行）**没有可查的对象**。
	//
	// 明确 skip 而不是让它红：红会诱导人去「修」一个不存在的东西。
	// DROP 之后的「rollup 主体 + 帧表尾巴拼接」由 rollup_tail_*_test.go 的
	// 其余用例 + rollup_oracle_fixture_test.go 覆盖。
	//
	// ⚠ 与 rollup_parity_pg_test.go 同一个前置，理由相同。
	if !db.Migrator().HasTable("unified_data") {
		t.Skip("unified_data 已 DROP（2026-11-08 硬切换）—— 本用例的对拍基准是 unified_data 的分桶聚合；" +
			"帧→rollup 由 rollup_oracle_fixture_test.go 覆盖")
	}
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("migrate frame: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")

	// 数据截止到「现在」（正是前端 hours=N 的形态）。
	now := time.Now().UTC()
	end := now
	const days = 8
	start := end.AddDate(0, 0, -days)
	const dev = 22

	ensureRollupMonthPartitions(t, db, start, end)
	seedParityUnified(t, db, dev, start, end)

	// 尾巴要能解码 ⇒ 必须有帧。用生产同一个编码器把 unified_data 编码成帧。
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)
	seedFrameFromUnifiedData(t, db, dev, dict)

	// 主体：回填所有**已结束**的日子（今天被排除，由 live collector 负责）。
	cfg := datalifecycle.RollupBackfillConfig{Enabled: true, BatchSeconds: 21600}
	runner := datalifecycle.NewRollupBackfillRunner(db, cfg)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// 模拟「live collector 已经写了今天到目前为止的桶」：
	// 直接对今天调用一次单元回填（生产里这段由 RollupCollector 每分钟 flush 完成）。
	today := datalifecycle.RollupCoverageDay(now)
	if _, err := datalifecycle.RollupBackfillUnitForTest(context.Background(), db, cfg, dev, today); err != nil {
		t.Fatalf("seed today rollup: %v", err)
	}

	q := historyPointQuery{
		EdgeDeviceID: dev,
		Category:     "rsoc",
		Start:        end.AddDate(0, 0, -7),
		End:          end,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	}
	got, handled, err := rollupHistoryPoints(context.Background(), db, q)
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if !handled {
		t.Fatal("end=now 的 7d 窗口必须走「rollup 主体 + 帧表尾巴」—— " +
			"若这里回落，删掉 unified_data 之后 7d/30d 图表就是空页")
	}
	// ============ 基准改为「直接对 unified_data 做分桶聚合」（2026-11-08）============
	//
	// 原来调 queryHistoryPoints，那个函数随 unified_data 一起删掉了。
	// 这里内联同一口径（D1 桶内最后值）的 SQL —— 与本用例造数据的过程**独立**，
	// 所以仍能抓住「主体+尾巴拼接错位」。
	widthSeconds := bucketWidthSeconds(historyWindowSeconds(q.Start, q.End), q.MaxPoints)
	wantSQL := fmt.Sprintf(`SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / %d) * %d) AS "timestamp",`+
		`(array_agg("value" ORDER BY "timestamp" DESC))[1] AS "value"`+
		`FROM unified_data WHERE device_id = ? AND sensor_name = ? AND "timestamp" BETWEEN ? AND ? `+
		`GROUP BY 1 ORDER BY 1`, widthSeconds, widthSeconds)
	var want []HistoryPoint
	if err := db.WithContext(context.Background()).Raw(wantSQL,
		q.EdgeDeviceID, q.Category, q.Start, q.End).Scan(&want).Error; err != nil {
		t.Fatalf("基准 SQL 聚合失败: %v", err)
	}
	cut := 0
	for cut < len(want) && want[cut].Timestamp.Before(q.Start) {
		cut++
	}
	want = want[cut:]

	if len(got) != len(want) {
		t.Fatalf("点数 = %d, want %d —— 主体+尾巴拼接后必须与单源查询点数一致", len(got), len(want))
	}
	var worst float64
	for i := range got {
		if !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Fatalf("第 %d 点时间 = %v, want %v", i, got[i].Timestamp, want[i].Timestamp)
		}
		if d := math.Abs(got[i].Value - want[i].Value); d > worst {
			worst = d
		}
	}
	t.Logf("end=now 7d：%d 点，最大偏差 %g", len(got), worst)
	// 容差 = 帧表量化上界 Step/2。尾巴走帧表解码，而 rsoc 的 spec Step=1
	// ⇒ 解码值最多与原始值差 0.5。主体走 rollup（无量化），所以整条链路的
	// 误差上界就是尾巴那一段的 0.5。
	//
	// ⚠ 这个容差**只覆盖量化**，不覆盖口径错误：若拼接把主体和尾巴的桶边界
	// 对错了，偏差会是"整条曲线的量级"（实测第一版就是 4135），远超 0.5。
	const tailQuantTolerance = 0.5 + 1e-9
	if worst > tailQuantTolerance {
		t.Errorf("最大偏差 %g > %g（帧表量化上界）—— 拼接口径与单源不一致",
			worst, tailQuantTolerance)
	}
}

// TestRollupLiveTailMissingFramesStillServes 守**降级路径**，而且它才是当前生产的真实形态。
//
// ============ 为什么这条比「尾巴有帧」那条更重要 ============
//
// telemetry/dualwrite.go 的 dualWriteBatchSize = 128，且 run() 里**没有任何 ticker**
// （实测 proc 数为 0）—— 落盘只在「攒满 128 帧」或「进程退出」时发生。
// 按生产历史帧速率（36 天 3,454,432 帧 ≈ 66.6 帧/分）折算，128 帧 ≈ 115 秒，
// 设备越慢越久且**没有上界**。
//
// ⇒ 最近 liveLag(120s) 的帧**大概率还在内存 batch 里**，帧表尾巴是空的。
//
// 本用例钉住：**尾表取不到数据 ≠ 故障**。必须照常服务（右边缘停在 cutoff），
// 而不是整窗回落 —— 整窗回落会让 end=now 的长窗永远走不到 rollup，
// 删掉 unified_data 之后 7d/30d 就是空页。
//
// 区别对待两种情况（这是本条最容易写错的地方）：
//
//	· 帧表存在但最近没有帧（滞后）     ⇒ 服务，尾巴为空        ← 本用例
//	· 帧表不存在 / 字典未注入 / 解码错 ⇒ 整窗回落（保守）      ← 上面 TestRollupLiveTailParity 的反面
func TestRollupLiveTailMissingFramesStillServes(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("migrate rollup: %v", err)
	}
	if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("ensure coverage: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM " + datalifecycle.TelemetryRollupTable)
	db.Exec("DELETE FROM " + datalifecycle.RollupCoverageTable)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("migrate frame: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")

	// 帧表连字典都没有：解码肯定拿不到东西。用空字典模拟「帧表可用但没数据」。
	// （frameDictionaryFor 在 SQLite/空库上会退化，这里显式注入一个空字典以便走通。）
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	now := time.Now().UTC()
	end := now
	const days = 8
	start := end.AddDate(0, 0, -days)
	const dev = 23

	ensureRollupMonthPartitions(t, db, start, end)
	seedParityUnified(t, db, dev, start, end)

	cfg := datalifecycle.RollupBackfillConfig{Enabled: true, BatchSeconds: 21600}
	if err := datalifecycle.NewRollupBackfillRunner(db, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	today := datalifecycle.RollupCoverageDay(now)
	if _, err := datalifecycle.RollupBackfillUnitForTest(context.Background(), db, cfg, dev, today); err != nil {
		t.Fatalf("seed today rollup: %v", err)
	}

	q := historyPointQuery{
		EdgeDeviceID: dev,
		Category:     "rsoc",
		Start:        end.AddDate(0, 0, -7),
		End:          end,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	}
	points, handled, err := rollupHistoryPoints(context.Background(), db, q)
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if !handled {
		t.Fatal("帧表里没有最近的帧（写路径 128 帧批量滞后）**不是故障** —— " +
			"必须照常服务、右边缘停在 cutoff。整窗回落会让 end=now 的长窗永远走不到 rollup")
	}
	if len(points) == 0 {
		t.Fatal("主体已完整回填，点集不该为空")
	}
	// 右边缘：因为尾巴拿不到，最后一点必须 <= cutoff 所在桶。
	cutoff := datalifecycle.RollupCoverageCutoff()
	if points[len(points)-1].Timestamp.After(cutoff) {
		t.Fatalf("末点 %v 晚于 cutoff %v —— 没有尾巴帧却返回了 cutoff 之后的点",
			points[len(points)-1].Timestamp, cutoff)
	}
	t.Logf("降级路径：%d 点，末点 %v（cutoff %v）—— 尾巴为空但服务正常",
		len(points), points[len(points)-1].Timestamp.Format(time.RFC3339), cutoff.Format(time.RFC3339))
}
