package datalifecycle

// L3 rollup 的 PostgreSQL 集成测试（EHOME_TEST_DB=postgres 才跑，其余 skip）。
//
// 为什么必须有 PG 这一层：分区表只存在于 PG。SQLite 侧的用例（telemetry_rollup_test.go）
// 只能证明「登记齐全」「读写语义对」，证明不了：
//   - createPartitionedParent 能用 partitionParentDDL 里的 DDL 真的建出分区母表；
//   - ensure 建出的分区名符合 <table>_<YYYYMM>，且 reaper（DropPartitionsBeforeFor）
//     能**按同一套命名**识别并删除它。
//
// 后者是 Lead 明确点名的风险：ensure 与 reaper 命名不一致会互相打架（ensure 反复建、
// reaper 永远删不到），或者更糟 —— reaper 删错东西。这里用一个真实用例把两者串起来。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/testutil"
)

func TestTelemetryRollupEnsureThenReap_Postgres(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	// 幂等清理：AutoMigrate 不建本表（分区表由 partition_mgr 裸 DDL 建），
	// 但上一轮用例可能残留。
	drop := func() {
		var names []string
		if err := db.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE (c.relname = ? OR c.relname LIKE ?) AND n.nspname = current_schema()`,
			TelemetryRollupTable, TelemetryRollupTable+"\\_%").Scan(&names).Error; err != nil {
			t.Fatalf("list probe relations: %v", err)
		}
		for _, n := range names {
			if err := db.Exec("DROP TABLE IF EXISTS " + n + " CASCADE").Error; err != nil {
				t.Fatalf("drop probe relation %s: %v", n, err)
			}
		}
	}
	drop()
	t.Cleanup(drop)

	// ① 建分区母表（全新库路径：表不存在 ⇒ 直接以最终名建 + 建滚动分区）。
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryRollup1mToPartitioned: %v", err)
	}
	if !IsTablePartitioned(db, TelemetryRollupTable) {
		t.Fatalf("%s 必须是分区母表 (relkind='p')", TelemetryRollupTable)
	}
	// 只应有主键索引（宽行表不配二级索引）。
	var idxCount int64
	if err := db.Raw(`SELECT count(*) FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = ? AND n.nspname = current_schema()`, TelemetryRollupTable).Scan(&idxCount).Error; err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("分区母表索引数 = %d, want 1（只有主键；宽行表加二级索引只放大写入成本）", idxCount)
	}

	// ② ensure 建出的分区名必须符合 <table>_<YYYYMM>（与 unified_data 同款约定）。
	now := time.Now()
	lastMonth := addMonths(now, -1)
	wantLastMonthPartition := partitionName(TelemetryRollupTable, lastMonth)
	if tableExistsInSchema(t, db, wantLastMonthPartition, "r") == 0 {
		t.Fatalf("ensure 未建出上月分区 %s（reaper 按同名约定识别，缺了就没法回收）", wantLastMonthPartition)
	}
	// 分区名不是 YYYYMM 后缀的关系必须存在性为 0（防止命名漂移）。
	if tableExistsInSchema(t, db, TelemetryRollupTable+"_legacy", "r") != 0 {
		t.Error("不应残留 _legacy 平表")
	}

	// ③ 写入上月分区的一行，证明 reaper DROP 的是真数据。
	rowBucket := lastMonth.Add(24 * time.Hour)
	if err := db.Exec("INSERT INTO "+TelemetryRollupTable+" (device_id, bucket, payload) VALUES (?, ?, ?)",
		14, rowBucket, []byte{1, 0, 1, 1, 'a'}).Error; err != nil {
		t.Fatalf("insert row into %s: %v", wantLastMonthPartition, err)
	}
	var before int64
	db.Raw("SELECT count(*) FROM " + TelemetryRollupTable).Scan(&before)
	if before != 1 {
		t.Fatalf("写入后行数 = %d, want 1", before)
	}

	// ④ reaper：cutoff = 本月 1 日 ⇒ 上月整分区到期，应被识别并 DROP。
	cutoff := monthStart(now)
	dropped, err := DropTelemetryRollup1mPartitionsBefore(db, cutoff)
	if err != nil {
		t.Fatalf("DropTelemetryRollup1mPartitionsBefore: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != wantLastMonthPartition {
		t.Fatalf("被删分区 = %v, want [%s]（ensure 与 reaper 的命名必须一致）", dropped, wantLastMonthPartition)
	}
	if tableExistsInSchema(t, db, wantLastMonthPartition, "r") != 0 {
		t.Fatalf("上月分区 %s 应已被 DROP", wantLastMonthPartition)
	}
	// 母表本身必须还在（reaper 只删分区，绝不删母表 —— 否则下次写入报 no partition）。
	if !IsTablePartitioned(db, TelemetryRollupTable) {
		t.Fatal("reaper 误删了分区母表 —— 后续写入将全部失败")
	}
	// 本月分区必须还在（cutoff 是本月 1 日，本月分区不该被删）。
	thisMonth := partitionName(TelemetryRollupTable, now)
	if tableExistsInSchema(t, db, thisMonth, "r") == 0 {
		t.Errorf("本月分区 %s 被误删（cutoff 语义是「整分区早于 cutoff 才删」）", thisMonth)
	}

	// ⑤ 幂等：再跑一次不报错、不再删任何东西。
	again, err := DropTelemetryRollup1mPartitionsBefore(db, cutoff)
	if err != nil {
		t.Fatalf("二次 reaper: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("二次 reaper 不应再删: %v", again)
	}

	// ⑥ ensure 幂等：再跑一次不报错、不重复建已被 DROP 的旧分区之外的意外关系。
	if err := EnsureTelemetryRollup1mPartitions(db, 3); err != nil {
		t.Fatalf("EnsureTelemetryRollup1mPartitions: %v", err)
	}
	if tableExistsInSchema(t, db, thisMonth, "r") == 0 {
		t.Errorf("ensure 幂等后本月分区 %s 仍应在", thisMonth)
	}
}

// TestTelemetryRollupRetentionNeverTouchesLegacyTable_Postgres 钉死「reaper 不会误伤
// 旧退役表/其它表」：同库里放一张名字同构的旧表，reaper 只应处理自己的分区。
func TestTelemetryRollupRetentionNeverTouchesLegacyTable_Postgres(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	legacy := "unified_data_rollup_1m"
	db.Exec("DROP TABLE IF EXISTS " + legacy + " CASCADE")
	db.Exec("DROP TABLE IF EXISTS " + TelemetryRollupTable + " CASCADE")
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS " + legacy + " CASCADE")
		db.Exec("DROP TABLE IF EXISTS " + TelemetryRollupTable + " CASCADE")
	})

	// 旧的退役表（平表，且名字以 _rollup_1m 结尾 —— 与新区分表同构）。
	if err := db.Exec("CREATE TABLE " + legacy + " (device_id BIGINT, sensor_name VARCHAR(32), bucket TIMESTAMP, PRIMARY KEY (device_id, sensor_name, bucket))").Error; err != nil {
		t.Fatalf("create legacy rollup table: %v", err)
	}
	if err := db.Exec("INSERT INTO "+legacy+" (device_id, sensor_name, bucket) VALUES (7, 'temp', ?)", "2026-08-21 10:00:00").Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("migrate telemetry_rollup_1m: %v", err)
	}
	// 用一个远在未来的 cutoff，确保所有 rollup 分区都被判到期。
	if _, err := DropTelemetryRollup1mPartitionsBefore(db, time.Now().AddDate(2, 0, 0)); err != nil {
		t.Fatalf("reaper: %v", err)
	}

	// 旧表必须原样在（它归 retire_rollup.go 的幂等 DROP 管，不归 reaper）。
	var count int64
	if err := db.Raw("SELECT count(*) FROM " + legacy).Scan(&count).Error; err != nil {
		t.Fatalf("旧表 %s 被 reaper 误删: %v", legacy, err)
	}
	if count != 1 {
		t.Errorf("旧表行数 = %d, want 1（reaper 不得碰它）", count)
	}
}

// TestPGRollupUpsertAgainstBareDDL 是 2026-11-07 生产事故的**端到端回归**：
// 走生产同一条建表路径（partition_mgr 的裸 DDL）建出 telemetry_rollup_1m，
// 再调生产同一个写入口 upsertRollupRow，断言不报错且行数=1。
//
// ============ 为什么这条用例是本次事故的核心防线 ============
//
// 事故形态：
//
//	· 生产 PG 的分区母表由 partitionParentDDL 建（裸 DDL，当时**只有 3 列**）；
//	· upsertRollupRow 的 INSERT 语句带 updated_at（第 4 列）；
//	· ⇒ 每分钟报 column "updated_at" does not exist，rollup 表 0 行。
//
// 而全部 SQLite 单测是绿的，因为 newRollupTestDB 走 AutoMigrate(&models.TelemetryRollup1m{})
// —— 模型字段一定被建出来。**两条建表路径各自自洽，交叉点上没人验**。
//
// 本用例补的就是那个交叉点：建表**只走裸 DDL**（不 AutoMigrate），写入走真实生产函数。
// 任何一列在 DDL 与写入 SQL 之间漂移，这里立刻红。
//
// ⚠ 变异验证（改 DDL 列时请照做）：把 partitionParentDDL[telemetry_rollup_1m] 里的
// updated_at 删掉再跑本用例，必须红在 upsert 的 column does not exist 上。
// 若删了列用例还绿，说明它又退回了「建表路径与写入路径不交叉」的假绿状态。
func TestPGRollupUpsertAgainstBareDDL(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	drop := func() {
		// 只匹配表/分区（relkind r/p）：主键索引 telemetry_rollup_1m_pkey 的名字也撞前缀，
		// 不过滤会让 DROP TABLE 打到索引上报 is not a table（2026-11-07 实测踩过）。
		var names []string
		if err := db.Raw("SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE c.relkind IN ('r', 'p') AND (c.relname = ? OR c.relname LIKE ?) "+
			"AND n.nspname = current_schema() ORDER BY (c.relkind = 'r') DESC",
			TelemetryRollupTable, TelemetryRollupTable+"\\_%").Scan(&names).Error; err != nil {
			t.Fatalf("list rollup relations: %v", err)
		}
		for _, n := range names {
			if err := db.Exec("DROP TABLE IF EXISTS " + n + " CASCADE").Error; err != nil {
				t.Fatalf("drop %s: %v", n, err)
			}
		}
	}
	drop()
	t.Cleanup(drop)

	// ① 只走生产建表路径（裸 DDL + 滚动分区），**不** AutoMigrate 模型。
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryRollup1mToPartitioned: %v", err)
	}

	// ② 调生产写入口（这就是事故里失败的那条语句）。
	bucket := RollupBucket(time.Now().UTC())
	if err := upsertRollupRow(context.Background(), db, 14, bucket, []byte{1, 0, 1, 1, 97}); err != nil {
		t.Fatalf("upsertRollupRow against bare-DDL partitioned table: %v\n"+
			"这正是 2026-11-07 生产事故的形状：partitionParentDDL 与 upsertRollupRow 的列清单漂移，\n"+
			"SQLite 用例（AutoMigrate）永远绿，PG（裸 DDL）必炸。", err)
	}

	// ③ 行数=1，且 updated_at 真的写进去了。
	var cnt int64
	if err := db.Raw("SELECT count(*) FROM " + TelemetryRollupTable).Scan(&cnt).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("行数 = %d, want 1", cnt)
	}
	var updatedAt time.Time
	if err := db.Raw("SELECT updated_at FROM " + TelemetryRollupTable + " LIMIT 1").Scan(&updatedAt).Error; err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAt.IsZero() {
		t.Error("updated_at 是零值 —— 列存在但没写进去（运维就看不到「这桶最后更新时间」了）")
	}

	// ④ 幂等覆盖：同 (device, bucket) 再写一次仍是 1 行（ON CONFLICT DO UPDATE）。
	if err := upsertRollupRow(context.Background(), db, 14, bucket, []byte{1, 0, 1, 1, 98}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := db.Raw("SELECT count(*) FROM " + TelemetryRollupTable).Scan(&cnt).Error; err != nil {
		t.Fatalf("count after second upsert: %v", err)
	}
	if cnt != 1 {
		t.Errorf("覆盖写后行数 = %d, want 1（宽行语义：一设备一分钟一行）", cnt)
	}
}
