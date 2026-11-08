package datalifecycle

// rollup_backfill_unified_probe_error_pg_test.go — 上面那条用例的**反面**（task-31 A1）。
//
// ============ 为什么必须成对存在 ============
//
// 修 A1 时最省事的写法是：
//
//	hasUnified, err := r.dayHasUnifiedRows(...)
//	if err != nil { hasUnified = false }   // ← 错！
//
// 那样 TestRollupBackfillSurvivesUnifiedDrop 照样绿（表不存在时确实该走帧表），
// **但数据库故障会被静默吞掉**：
//
//	表不存在 = 正常终态（DROP 正是目标） => 走帧表源    ← 应该这样
//	DB 挂了   = 异常                      => 必须失败报警 ← 不能这样
//
// 而「静默吞掉」的后果不是少几条数据，是：回填继续跑 → 每天标 missing →
// **missing 是粘性的**（RollupCoverageVerified 认 complete 和 missing，两者都永不重试）
// ⇒ 一次连接故障被永久写成「这天没有数据」，而且 coverage 看起来是完整的。
//
// ============ 这个仓库里「失败并报警」的具体形态 ============
//
// RunOnce **不会**因为单个单元失败就中止（这是刻意的：一台设备的一天出错不该让
// 整轮回填停摆）。它的报警机制是两条，都必须满足：
//
//	① progress.Errors 里有这条错误（对外通过 GET /api/v1/system/rollup-backfill 暴露）
//	② 该单元 coverage 标 partial —— **不是** complete/missing
//
// ②是关键：partial 不被 RollupCoverageVerified 认可 ⇒ 下次续跑会重试。
// 如果错误被吞掉后走「0 行 ⇒ 标 missing」这条路，就永久钉死成「没数据」了。

import (
	"context"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

func TestRollupBackfillUnifiedProbeErrorStillFails(t *testing.T) {
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

	const dev = 9972
	dayStart := time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour)
	seedSparseFrames(t, db, dev, dayStart, dayStart.Add(10*time.Minute), dict,
		[]string{"current"}, 10*time.Second)

	// 构造「unified_data 存在（HasTable 为真）但 SELECT 报错」。
	//
	// 为什么要真的做这个形态、而不是 mock：修法是用 Migrator().HasTable 判存在性，
	// 只有真实 PG 对象才能同时满足「HasTable=true」和「查询报错」两个条件。
	//
	// 第一版用「视图指向不存在的表」，PG 在 CREATE VIEW 时就拒绝了
	// （ERROR: relation "__no_such_table__" does not exist）—— PG 建视图会校验依赖。
	// 改成「建一张缺列的同名表」：relation 存在 => HasTable true；
	// 而探测 SQL 用到 device_id / timestamp 两列 => column does not exist。
	// 这与「这条表在 DB 里结构不对」是同一类故障，语义上也更贴切。
	if err := db.Exec("DROP TABLE IF EXISTS unified_data CASCADE").Error; err != nil {
		t.Fatalf("drop unified_data: %v", err)
	}
	if err := db.Exec("CREATE TABLE unified_data (x int)").Error; err != nil {
		t.Fatalf("create malformed unified_data: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS unified_data CASCADE") })

	// 前提自检：HasTable 必须是 true，否则本用例验的是「表不存在」那条分支。
	if !db.Migrator().HasTable("unified_data") {
		t.Fatal("HasTable(unified_data) = false —— 本用例要验的是「表存在但查不动」，前提不成立")
	}

	cfg := RollupBackfillConfig{Enabled: true, BatchSeconds: 1800}
	runner := NewRollupBackfillRunner(db, cfg)
	// RunOnce 对单单元失败不中止（刻意的），所以这里不断言返回 err，
	// 而是断言「报警机制真的响了」—— 见文件头的 ①②。
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("本用例期望单单元失败被吸收（不中止整轮），实际整轮返回: %v", err)
	}

	prog := runner.Progress()
	// ① 错误必须出现在 progress.Errors 里（对外暴露给运维）。
	found := false
	for _, e := range prog.Errors {
		if strings.Contains(e, "day source probe") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("unified_data 探测报错没有进入 progress.Errors（运维无从知晓）\n"+
			"errors = %v", prog.Errors)
	}

	// ② 该单元必须标 partial，**不能**是 complete 或 missing。
	// 这是本用例真正的价值：missing 是粘性的，一旦标错就永久「这天没数据」。
	var states []string
	db.Raw("SELECT DISTINCT state FROM " + RollupCoverageTable).Scan(&states)
	for _, st := range states {
		if st == RollupCoverageComplete || st == RollupCoverageMissing {
			t.Fatalf("探测报错时 coverage 被标成 %q —— "+
				"RollupCoverageVerified 认这两种，会永久钉死；"+
				"探测失败必须标 partial 让下一轮重试", st)
		}
	}
	t.Logf("探测报错 => progress.Errors 有记录（%d 条），coverage 状态 %v（不会被当作已验证）",
		len(prog.Errors), states)
}
