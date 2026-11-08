package datalifecycle

// rollup_backfill_has_table_gate_pg_test.go — HasTable 守卫的**两条分支都要验**。
//
// ============ 守的是什么（2026-11-08）============
//
// discoverDevices / discoverStart 里有这样一段：
//
//	for _, q := range []struct{ table, sql string }{
//	    {"unified_data", "SELECT DISTINCT device_id FROM unified_data"},
//	    {TelemetryFrameTable, ...},
//	    {TelemetryRollupTable, ...},
//	} {
//	    if !r.db.Migrator().HasTable(q.table) { continue }   // <- 守卫
//	    ...执行 SQL...
//	}
//
// DROP 之后已在**生产**验过分支 A（表不存在 => continue => 不再刷 42P01）。
// 而分支 B（表存在 => 仍然查）**没验**。
//
// 分支 A 的绿**不能推出** B 正确：若 HasTable 恒返回 false（判据写错 / 对分区表
// 返回 false），A 也是绿的，而 B 永远不执行 —— 回填会静默少枚举一批设备。
//
// ============ ⚠ 本文件钉住的是 gorm 的实现细节，注释必须留住 ============
//
// gorm.io/driver/postgres 的 HasTable 查的是：
//
//	SELECT count(*) FROM information_schema.tables
//	WHERE table_schema = ? AND table_name = ? AND table_type = 'BASE TABLE'
//
// 而 PG 的分区母表（pg_class.relkind = 'p'）在 information_schema 里报为
// BASE TABLE（实测：生产上 device_data / telemetry_frame / telemetry_rollup_1m
// 三个分区母表全部为 BASE TABLE）。两者叠加 => HasTable(分区表) == true。
//
// 若将来 gorm 改成查 pg_class + relkind='r'（只认普通表），本文件会红 ——
// **而那正是我们想知道的**：那时 HasTable 对分区表返回 false，
// 迁移期/历史库场景下回填会静默少一批设备（「静默降级」第 6 级）。
//
// ============ 为什么必须用真分区表造数 ============
//
// 生产上这三张表全是分区母表。若用普通表造数，验的是「HasTable 对 relkind='r'
// 的行为」—— 而那正是**不会出问题**的那种，用例会绿得毫无意义。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/testutil"
)

// TestHasTableOnPartitionedParent 先单独钉住底层事实：
// **gorm 的 HasTable 对分区母表必须返回 true**。
//
// 为什么单独一条：它是上面那个守卫的地基，且**独立于业务代码**。
// 它若为 false，守卫会把「表存在」当成「不存在」=> 分支 B 永不执行。
func TestHasTableOnPartitionedParent(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表分区母表: %v", err)
	}

	// 前置：确认它**真的是**分区母表（relkind = p），否则本用例测错对象。
	var relkind string
	if err := db.Raw(
		`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = ? AND n.nspname = current_schema()`, TelemetryFrameTable).
		Scan(&relkind).Error; err != nil {
		t.Fatalf("查 relkind: %v", err)
	}
	if relkind != "p" {
		t.Fatalf("%s 的 relkind = %q, want \"p\"（分区母表）—— 本用例的前提是"+
			"「对象确实是分区表」，否则验不到 HasTable 对 relkind=p 的行为",
			TelemetryFrameTable, relkind)
	}

	// ============ 两级假设分开断言（归因成本，2026-11-08）============
	//
	// 下面那条 HasTable 断言其实依赖两个可独立变化的实现：
	//
	//	① PG：分区母表（relkind='p'）在 information_schema 里报为 BASE TABLE
	//	② gorm：HasTable 查的正是 information_schema + table_type='BASE TABLE'
	//
	// 只断言结果的话，红时只能看到「HasTable = false」，得先查 gorm、
	// 查完发现没变、再查 PG —— 多绕一圈。所以这里把 ① 单独钉住；
	// 下面那条断言的是 ①+② 的结果。两级分开 => 红时立刻知道是哪一级变了。
	var tableType string
	infoSchemaSQL := `SELECT table_type FROM information_schema.tables ` +
		`WHERE table_schema = current_schema() AND table_name = ?`
	if err := db.Raw(infoSchemaSQL, TelemetryFrameTable).Scan(&tableType).Error; err != nil {
		t.Fatalf("查 information_schema.tables: %v", err)
	}
	if tableType != "BASE TABLE" {
		t.Fatalf("PG 把分区母表 %s 在 information_schema 里报成 %q，而非 BASE TABLE —— "+
			"gorm 的 HasTable 会因此返回 false，而错误信息看起来像 gorm 的问题。"+
			"请分别确认：PG 的 information_schema 语义 / gorm 的 HasTable 实现",
			TelemetryFrameTable, tableType)
	}

	if !db.Migrator().HasTable(TelemetryFrameTable) {
		t.Fatalf("HasTable(%s) = false，而它是**存在的分区母表** —— "+
			"守卫 `if !HasTable { continue }` 会把「表存在」当成「不存在」，"+
			"迁移期回填会静默少一批设备。gorm 的 HasTable 实现或 PG 的 "+
			"information_schema 语义可能已变，请重新确认后修守卫的判据（不是改本断言）",
			TelemetryFrameTable)
	}

	// 负向对照：真的不存在的表必须是 false（否则守卫形同虚设）。
	if db.Migrator().HasTable("__no_such_table__") {
		t.Fatal("HasTable(不存在) = true —— 守卫恒放行，42P01 会回来")
	}
	t.Logf("%s（relkind=%s）：HasTable=true；不存在的表：HasTable=false",
		TelemetryFrameTable, relkind)
}

// TestRollupBackfill_UnifiedTablePresentStillQueries 是**分支 B 的正向见证**。
//
// 断言：unified_data **存在**（且是分区母表）时，discoverDevices 必须真的
// 把它里面的 device_id 枚举出来，而不是被守卫整批跳过。
//
// 没有这条，那行 `if !HasTable { continue }` 被改成恒跳过时不会有任何东西变红 ——
// 而症状是「迁移期回填少一批设备」，一个只在日志里安静体现的降级。
func TestRollupBackfill_UnifiedTablePresentStillQueries(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	// 三张表都要在（discoverDevices 会遍历全部三项）。
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("建 rollup 表: %v", err)
	}
	db.Exec("DELETE FROM " + TelemetryFrameTable)
	db.Exec("DELETE FROM " + TelemetryRollupTable)

	// unified_data 造成**分区母表**（与生产同形态）。
	if err := MigrateTableToPartitioned(db, "unified_data", "unified_data_legacy"); err != nil {
		t.Fatalf("建 unified_data 分区母表: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS unified_data CASCADE") })

	// 前置 1：它必须是分区母表。本用例的全部价值在「对象是 relkind=p」，
	// 若实际建成了普通表，下面的正向断言就验错了对象。
	var relkind string
	if err := db.Raw(
		`SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = 'unified_data' AND n.nspname = current_schema()`).Scan(&relkind).Error; err != nil {
		t.Fatalf("查 unified_data 的 relkind: %v", err)
	}
	if relkind != "p" {
		t.Fatalf("unified_data 的 relkind = %q, want \"p\"（分区母表）—— 前置不成立", relkind)
	}

	// 造一条**只**在 unified_data 里存在的设备。
	// 时间取 now：MigrateTableToPartitioned 会为 [上月, 未来 N 月] 建分区，
	// 当前时间必然落在其中（写不进去会立刻在下面报错，不会静默）。
	const dev = 7701
	now := time.Now().UTC()
	if err := db.Exec(
		`INSERT INTO unified_data (device_id, sensor_name, value, unit, "timestamp", created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		dev, "rsoc", 42.0, "%", now, now).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}

	// 前置 2：行真的写进去了。否则下面「枚举到了」可能来自 edge_devices 那条路，
	// 而不是 unified_data —— 用例会验错对象。
	var rows int64
	if err := db.Raw("SELECT count(*) FROM unified_data WHERE device_id = ?", dev).Scan(&rows).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	if rows == 0 {
		t.Fatal("unified_data 里没写进去 —— 用例前置不成立")
	}

	// 前置 3：另外两张源表必须是空的。
	// 这样「枚举出 dev」只可能来自 unified_data 那一路（可归因性）。
	var others int64
	if err := db.Raw("SELECT (SELECT count(*) FROM " + TelemetryFrameTable + ") + " +
		"(SELECT count(*) FROM " + TelemetryRollupTable + ")").Scan(&others).Error; err != nil {
		t.Fatalf("count 其它源表: %v", err)
	}
	if others != 0 {
		t.Fatalf("帧表 / rollup 表非空（共 %d 行）—— 无归因性，本用例验不到 unified 那一路", others)
	}

	// ============ 断言：设备必须被枚举出来 ============
	//
	// 注意 edge_devices 表里可能已有别的设备行（OpenTestDB 的 schema 是共享的，
	// 但每用例独立 schema —— 这里仍显式确认 dev 不来自 edge_devices）。
	var inEdge int64
	if err := db.Raw("SELECT count(*) FROM edge_devices WHERE id = ?", dev).Scan(&inEdge).Error; err != nil {
		t.Fatalf("查 edge_devices: %v", err)
	}
	if inEdge != 0 {
		t.Fatalf("edge_devices 里已有 id=%d —— 无法归因到 unified_data", dev)
	}

	runner := NewRollupBackfillRunner(db, RollupBackfillConfig{Enabled: true, BatchSeconds: 1800})
	got, err := runner.discoverDevices(context.Background())
	if err != nil {
		t.Fatalf("discoverDevices: %v", err)
	}
	found := false
	for _, id := range got {
		if id == dev {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("discoverDevices 没枚举出 device_id=%d（结果 %v）。"+
			"unified_data 存在、是分区母表、且有 1 行，却被 HasTable 守卫跳过 => "+
			"迁移期 / 历史库场景下回填会**静默少一批设备**", dev, got)
	}
	t.Logf("unified_data（relkind=p，1 行）=> 枚举出 %d 台设备，含 %d", len(got), dev)
}
