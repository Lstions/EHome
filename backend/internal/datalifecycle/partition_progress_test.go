package datalifecycle

// partition_progress_test.go — 迁移/索引维护进度埋点的验收单测（契约 §3.2）。
//
// 守五件事（漏任一条都会让维护页要么空白、要么乱跳、要么把启动搞崩）：
//  1. **nil 回调是纯 no-op**：未注入时所有迁移路径的行为与埋点前逐字一致，
//     且 copyMonthBatched 的零值 monthProgress 不上报（既有调用方零改动）；
//  2. **非 PG 方言 no-op**：SQLite 测试库上四个埋点位置一个回调都不发
//     （这是"埋点不改变现有行为"的最直接证据 —— 整条路径本来就是 no-op）；
//  3. **每批上报**：copyMonthBatched 在 SQLite 上真实跑批（纯 SQL，两方言通用），
//     断言"每批一次"、pct 单调不减、落在 [0,100]、detail 含已搬行数；
//  4. **table→stepID 映射**：unified_data / device_data / telemetry_rollup_1m 各自的
//     stepID 正确，未登记表不上报（空 stepID）；
//  5. **跨包一致性**：本包上报的 stepID 必须与 bootstrap 包常量逐字相同
//     （只在测试里 import bootstrap，生产依赖方向不变）。

import (
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/bootstrap"
)

// recordingProgress 是一个记录全部回调的 MigrationProgress 实现。
type recordingProgress struct {
	mu      sync.Mutex
	steps   []string
	pcts    []float64
	details []string
}

func (r *recordingProgress) StepProgress(stepID string, pct float64, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, stepID)
	r.pcts = append(r.pcts, pct)
	r.details = append(r.details, detail)
}

func (r *recordingProgress) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pcts)
}

// withProgress 注入回调并在用例结束时清空全局状态（防用例间串味）。
func withProgress(t *testing.T, p MigrationProgress) {
	t.Helper()
	prev := migrationProgress
	t.Cleanup(func() {
		migrationProgressMu.Lock()
		migrationProgress = prev
		migrationProgressMu.Unlock()
	})
	SetMigrationProgress(p)
}

// newSQLiteProbeDB 建一对用于 copyMonthBatched 的平表（列清单与 unified_data 对齐）。
func newSQLiteProbeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	const cols = "(id INTEGER, device_id INTEGER, sensor_name TEXT, value REAL, unit TEXT, timestamp DATETIME, created_at DATETIME, edge_device_id INTEGER, PRIMARY KEY (id, timestamp))"
	for _, table := range []string{"zz_prog_src", "zz_prog_dst"} {
		if err := db.Exec("CREATE TABLE " + table + " " + cols).Error; err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
	return db
}

// seedProbeMonth 往 src 灌 n 行（同一月内），返回该月起点。
func seedProbeMonth(t *testing.T, db *gorm.DB, table string, n int) time.Time {
	t.Helper()
	month := monthStart(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	ts := month.Add(2 * time.Hour)
	tx := db.Begin()
	for i := 1; i <= n; i++ {
		if err := tx.Exec(
			"INSERT INTO "+table+" (id, device_id, sensor_name, value, timestamp) VALUES (?, 1, 'a', 1.0, ?)",
			i, ts).Error; err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return month
}

// TestSetMigrationProgressNilIsSafe 守 nil 注入与 typed-nil 归一。
func TestSetMigrationProgressNilIsSafe(t *testing.T) {
	withProgress(t, nil)
	// nil 回调下所有上报点都必须是纯 no-op（不得 panic）。
	reportMigrationProgress(StepIDPartitionUnifiedData, 50, "x")
	reportMigrationProgress("", 50, "x")
	reportMigrationProgress(StepIDParentIndexes, -10, "x")
	reportMigrationProgress(StepIDRetireDeadIndexes, 1000, "x")
	if migrationProgress != nil {
		t.Fatal("SetMigrationProgress(nil) must clear the global")
	}

	// typed nil：接口值非 nil 但底层指针为空 —— 直接调用会 panic，
	// 因此 SetMigrationProgress 必须把它归一成 nil（实现在 partition_mgr.go）。
	var typedNil *recordingProgress
	SetMigrationProgress(typedNil)
	migrationProgressMu.RLock()
	got := migrationProgress
	migrationProgressMu.RUnlock()
	if got != nil {
		t.Fatalf("typed-nil progress must be normalized to nil, got %#v", got)
	}
	reportMigrationProgress(StepIDParentIndexes, 1, "must not panic")
}

// TestReportMigrationProgressClampsPct 守 0..100 钳位（调用方算比例，浮点会越界）。
func TestReportMigrationProgressClampsPct(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)

	reportMigrationProgress(StepIDParentIndexes, -5, "under")
	reportMigrationProgress(StepIDParentIndexes, 250, "over")
	reportMigrationProgress(StepIDParentIndexes, 42.5, "ok")
	reportMigrationProgress("", 50, "empty stepID must be dropped")

	if rec.count() != 3 {
		t.Fatalf("reports = %d, want 3 (empty stepID must not be reported)", rec.count())
	}
	want := []float64{0, 100, 42.5}
	for i, w := range want {
		if rec.pcts[i] != w {
			t.Errorf("report[%d] pct = %v, want %v", i, rec.pcts[i], w)
		}
	}
}

// TestMigrationProgressConcurrentInjection 并发注入/上报不得 race（配合 -race 跑）。
func TestMigrationProgressConcurrentInjection(t *testing.T) {
	withProgress(t, nil)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			reportMigrationProgress(StepIDParentIndexes, float64(i%101), "reader")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			SetMigrationProgress(&recordingProgress{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			SetMigrationProgress(nil)
		}
	}()
	wg.Wait()
	reportMigrationProgress(StepIDParentIndexes, 1, "after")
}

// TestPartitionStepIDMapping 守 table→stepID 映射（契约 §3.2）。
func TestPartitionStepIDMapping(t *testing.T) {
	cases := []struct {
		table string
		want  string
	}{
		{"unified_data", StepIDPartitionUnifiedData},
		{"device_data", StepIDPartitionDeviceData},
		{"telemetry_rollup_1m", StepIDPartitionRollup},
		// 未登记表：不上报（返回空串 ⇒ reportMigrationProgress 早退）。
		{"zz_partition_probe", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := partitionStepIDOf(c.table); got != c.want {
			t.Errorf("partitionStepIDOf(%q) = %q, want %q", c.table, got, c.want)
		}
	}
	// ⚠ 2026-11-08 硬切换：bootstrap.StepPartitionUnifiedData 已删除
	//（main.go 不再为 unified_data 调分区迁移，否则 DROP 后重启会把空表重建）。
	// 但 datalifecycle 的 StepIDPartitionUnifiedData **保留** —— 它描述的是
	// MigrateUnifiedDataToPartitioned 这个泛化入口自己的埋点映射，
	// 该函数仍在（device_data / telemetry_frame 走同一套机制），且仍可能被
	// 非 main 的调用方使用。所以这里只校验**两边都还在的**常量。
	if StepIDPartitionDeviceData != bootstrap.StepPartitionDeviceData ||
		StepIDPartitionRollup != bootstrap.StepPartitionRollup ||
		StepIDParentIndexes != bootstrap.StepParentIndexes ||
		StepIDRetireDeadIndexes != bootstrap.StepRetireDeadIndexes {
		t.Fatal("datalifecycle 的 stepID 常量与 bootstrap 包不一致 —— 维护页会永远收不到进度")
	}
}

// TestNoProgressOnNonPostgres 守"非 PG 方言零回调"：SQLite 上四个埋点位置
// 全部落在既有 isPostgres 分支之内，一条回调都不该发。
func TestNoProgressOnNonPostgres(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)
	db := newSQLiteDB(t) // partition_mgr_test.go 的辅助函数

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("MigrateUnifiedDataToPartitioned on sqlite: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("MigrateTableToPartitioned on sqlite: %v", err)
	}
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryRollup1mToPartitioned on sqlite: %v", err)
	}
	if err := ensurePartitionParentIndexes(db, "unified_data"); err != nil {
		t.Fatalf("ensurePartitionParentIndexes on sqlite: %v", err)
	}
	if err := retireDeadTimeseriesIndexes(db, "unified_data"); err != nil {
		t.Fatalf("retireDeadTimeseriesIndexes on sqlite: %v", err)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("non-postgres dialect must report nothing, got %d reports: %v / %v", got, rec.steps, rec.pcts)
	}
}

// TestCopyMonthBatchedReportsPerBatch 守"每批一次 + 单调不减 + 落在 0..100 + detail 含行数"。
//
// 为什么用 SQLite 跑：copyMonthBatched 是纯 SQL（INSERT ... SELECT ... ON CONFLICT），
// 两方言通用，不需要 PG 就能验证"批数×上报次数"这个契约本身。
// 生产上的真实耗时/行数由 PG 集成测试覆盖。
func TestCopyMonthBatchedReportsPerBatch(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)
	db := newSQLiteProbeDB(t)

	const rows = 25000 // 批大小 10000 ⇒ 3 批
	month := seedProbeMonth(t, db, "zz_prog_src", rows)

	moved, err := copyMonthBatched(db, "zz_prog_dst", "zz_prog_src", month, monthProgress{
		stepID: StepIDPartitionUnifiedData,
		base:   0,
		span:   80,
		label:  "202611 第 1/1 批",
	})
	if err != nil {
		t.Fatalf("copyMonthBatched: %v", err)
	}
	if moved != rows {
		t.Errorf("moved = %d, want %d (全部行都被尝试搬运)", moved, rows)
	}
	var dstCount int64
	if err := db.Raw("SELECT count(*) FROM zz_prog_dst").Scan(&dstCount).Error; err != nil {
		t.Fatalf("count dst: %v", err)
	}
	if dstCount != rows {
		t.Fatalf("dst rows = %d, want %d (搬迁结果必须与埋点前一致)", dstCount, rows)
	}

	wantBatches := (rows + migrateBatchSizePostgres - 1) / migrateBatchSizePostgres
	if rec.count() != wantBatches {
		t.Fatalf("reports = %d, want %d (每批一次)", rec.count(), wantBatches)
	}
	prev := -1.0
	for i := 0; i < rec.count(); i++ {
		if rec.steps[i] != StepIDPartitionUnifiedData {
			t.Errorf("report[%d] stepID = %q, want %q", i, rec.steps[i], StepIDPartitionUnifiedData)
		}
		p := rec.pcts[i]
		if p < 0 || p > 100 {
			t.Errorf("report[%d] pct = %v, want within [0,100]", i, p)
		}
		if p < prev {
			t.Errorf("report[%d] pct = %v went backwards (prev %v) —— 进度条会抖动", i, p, prev)
		}
		prev = p
		if !strings.Contains(rec.details[i], "已搬") || !strings.Contains(rec.details[i], "202611") {
			t.Errorf("report[%d] detail = %q, want 含月份标签与已搬行数", i, rec.details[i])
		}
	}
	// 最后一批必须到达本月比例上界（span 的终点），否则月内看不到"搬完"。
	if last := rec.pcts[rec.count()-1]; last != 80 {
		t.Errorf("last report pct = %v, want 80 (base+span 终点)", last)
	}
}

// TestCopyMonthBatchedZeroProgressIsNoop 守零值 monthProgress 不上报
// （既有调用方/测试不传 progress 时的兼容面）。
func TestCopyMonthBatchedZeroProgressIsNoop(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)
	db := newSQLiteProbeDB(t)
	month := seedProbeMonth(t, db, "zz_prog_src", 2000)

	moved, err := copyMonthBatched(db, "zz_prog_dst", "zz_prog_src", month, monthProgress{})
	if err != nil {
		t.Fatalf("copyMonthBatched: %v", err)
	}
	if moved != 2000 {
		t.Errorf("moved = %d, want 2000", moved)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("zero-value monthProgress must report nothing, got %d", got)
	}
}

// TestCopyMonthBatchedNilCallbackIsNoop 守"nil 回调下 copyMonthBatched 结果不变"。
func TestCopyMonthBatchedNilCallbackIsNoop(t *testing.T) {
	withProgress(t, nil)
	db := newSQLiteProbeDB(t)
	month := seedProbeMonth(t, db, "zz_prog_src", 15000)

	moved, err := copyMonthBatched(db, "zz_prog_dst", "zz_prog_src", month, monthProgress{
		stepID: StepIDPartitionDeviceData, span: 80, label: "202611 第 1/1 批",
	})
	if err != nil {
		t.Fatalf("copyMonthBatched with nil callback: %v", err)
	}
	if moved != 15000 {
		t.Errorf("moved = %d, want 15000", moved)
	}
	var dstCount int64
	db.Raw("SELECT count(*) FROM zz_prog_dst").Scan(&dstCount)
	if dstCount != 15000 {
		t.Errorf("dst rows = %d, want 15000", dstCount)
	}
}

// TestCopyMonthBatchedErrorStillReturned 守"埋点不吞错"：
// 错误路径仍返回非 nil err（进度是有返回值的旁路，不得改变控制流）。
func TestCopyMonthBatchedErrorStillReturned(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)
	db := newSQLiteProbeDB(t)
	month := seedProbeMonth(t, db, "zz_prog_src", 100)

	// 目标表不存在 ⇒ INSERT 失败，必须把 err 透出来。
	_, err := copyMonthBatched(db, "zz_prog_missing_dst", "zz_prog_src", month, monthProgress{
		stepID: StepIDPartitionUnifiedData, span: 80, label: "202611 第 1/1 批",
	})
	if err == nil {
		t.Fatal("copyMonthBatched must return the INSERT error (埋点不得吞错)")
	}
	// 失败发生在第一批的 INSERT 之后 ⇒ 那一批的进度上报已经发过（这是期望行为：
	// 前端看到"已尝试到第 k 批"比看到"什么都没发生"更贴近事实）。
	if rec.count() > 1 {
		t.Errorf("reports = %d, want <= 1 on first-batch failure", rec.count())
	}
}

// TestMonthSpanInclusive 守按月循环的分母（最少 1，避免除零把 pct 打成 NaN）。
func TestMonthSpanInclusive(t *testing.T) {
	nov := monthStart(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	cases := []struct {
		from, to time.Time
		want     int
	}{
		{nov, nov, 1},
		{nov, addMonths(nov, 6), 7},
		{addMonths(nov, -3), nov, 4},
		// 反向区间（理论上 oldest > now 不会发生）：按 1 处理，不返回 0/负数。
		{nov, addMonths(nov, -2), 1},
	}
	for _, c := range cases {
		if got := monthSpanInclusive(c.from, c.to); got != c.want {
			t.Errorf("monthSpanInclusive(%s, %s) = %d, want %d",
				c.from.Format("200601"), c.to.Format("200601"), got, c.want)
		}
	}
}

// TestCommatize 守 detail 里行数的千分位（41000 → 41,000）。
func TestCommatize(t *testing.T) {
	cases := map[int64]string{
		0: "0", 1: "1", 999: "999", 1000: "1,000",
		41000: "41,000", 1000000: "1,000,000", 69600000: "69,600,000",
		-2500: "-2,500",
	}
	for in, want := range cases {
		if got := commatize(in); got != want {
			t.Errorf("commatize(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestMonthProgressReportRange 守 monthProgress 的 [base, base+span] 映射（两级粒度不打架）。
func TestMonthProgressReportRange(t *testing.T) {
	rec := &recordingProgress{}
	withProgress(t, rec)
	mp := monthProgress{stepID: StepIDParentIndexes, base: 25, span: 50, label: "L"}
	mp.report(-1, "clamp low")
	mp.report(0, "start")
	mp.report(0.5, "middle")
	mp.report(2, "clamp high")
	got := []float64{rec.pcts[0], rec.pcts[1], rec.pcts[2], rec.pcts[3]}
	want := []float64{25, 25, 50, 75}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pct[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if rec.steps[0] != StepIDParentIndexes {
		t.Errorf("stepID = %q, want %q", rec.steps[0], StepIDParentIndexes)
	}
	// 零值 monthProgress.report 必须 no-op。
	before := rec.count()
	monthProgress{}.report(0.5, "off")
	if rec.count() != before {
		t.Error("zero-value monthProgress must not report")
	}
}

// TestProgressNeverMarksFailure 守 Lead 裁决 c：埋点侧**绝不**标记失败，
// 只上报进度。失败语义由 main 侧决定（rollup 失败只 Warn ⇒ main 调 Skip）。
func TestProgressNeverMarksFailure(t *testing.T) {
	// 该包对外的进度契约只有 StepProgress 一个方法；没有任何 Fail/Error 通道。
	var p MigrationProgress = &recordingProgress{}
	if _, ok := p.(interface{ Fail(string, error) }); ok {
		t.Fatal("MigrationProgress 不得暴露 Fail —— 失败语义属于 main.go，不属于埋点")
	}
	if _, ok := p.(interface{ StepProgress(string, float64, string) }); !ok {
		t.Fatal("MigrationProgress 必须提供 StepProgress(stepID, pct, detail)")
	}
}
