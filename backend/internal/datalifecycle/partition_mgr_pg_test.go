package datalifecycle

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/database"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// PG-only 集成测试 (make test-integration)。
// SQLite 单测下这些路径全部 no-op, 由 partition_mgr_test.go 覆盖。

// requirePostgres skips the test unless running against PostgreSQL.
func requirePostgres(t *testing.T) {
	t.Helper()
	if !testutil.IsPostgres() {
		t.Skip("requires PostgreSQL (make test-integration)")
	}
}

// dropUnifiedDataFlat removes the AutoMigrate'd flat unified_data so the
// partition migration paths start from a clean slate within the test schema.
func dropUnifiedDataFlat(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DROP TABLE IF EXISTS unified_data CASCADE").Error; err != nil {
		t.Fatalf("drop flat unified_data: %v", err)
	}
}

// tableExistsInSchema counts relname/relkind in the current schema.
func tableExistsInSchema(t *testing.T, db *gorm.DB, name, kind string) int64 {
	t.Helper()
	var count int64
	if err := db.Raw(tableExistsSQL, name, kind).Scan(&count).Error; err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return count
}

// unifiedDataRelkinds 列出**全库所有 schema** 下名为 unified_data 的关系及其
// relkind, 形如 "public:p,test_123_456:p"。当前 schema 之外的同名关系是
// "PARTITION OF 打到了别的对象上" 这类问题的唯一直接证据 (search_path 回退)。
func unifiedDataRelkinds(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var s string
	if err := db.Raw(`SELECT COALESCE(string_agg(n.nspname || ':' || c.relkind::text, ',' ORDER BY n.nspname), '<none>')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ?`, partitionedTable).Scan(&s).Error; err != nil {
		return "<query-error: " + err.Error() + ">"
	}
	if s == "" {
		return "<none>"
	}
	return s
}

// flakeProbe 在关键步骤前后打印定位偶发 42P17 所需的状态。
//
// 背景 (2026-09-14 首轮 PG 全包偶发一次, 其后 5+4 次未复现):
//
//	partition_mgr_pg_test.go:155  EnsurePartitions: ensure partition unified_data_202608:
//	ERROR: "unified_data" is not partitioned (SQLSTATE 42P17)
//
// 该错误由 PG 在 "CREATE TABLE <分区> PARTITION OF <母表>" 且母表 relkind != 'p' 时
// 抛出。PG 服务端日志 (docker logs ehome-postgres) 留有原始记录 (2026-09-14
// 18:53:26–18:55:32, 4 条同形态记录): parent 是 unified_data(**不带 _new**),
// 月份是 **202608** —— 该 DDL 只可能来自 EnsurePartitionsFor("unified_data", n),
// 与失败点逐字吻合 (迁移内部建分区时 parent 恒为 unified_data_new)。
//
// 注意: PG 默认 log_statement=none, **不记录成功语句**, 因此"当时哪些月份分区已
// 存在"无从得知, 不要据此推断失败时序。4 条记录也可能只是同一次失败的重试。
// 详见 .logs/partition-flake-report.md。
//
// 下次偶发时这张日志必须能直接回答四个问题: 当前 schema 是谁、unified_data 在当前
// schema 下的 relkind 是什么、全库同名关系分布 (跨 schema 回退)、以及包级全局
// database.DB 此刻指向谁 (本用例第 3 步会临时改写它)。
func flakeProbe(t *testing.T, db *gorm.DB, stage string) {
	t.Helper()
	var schema, relkind, searchPath, dbName string
	_ = db.Raw("SELECT current_schema()").Scan(&schema).Error
	_ = db.Raw(`SELECT COALESCE((
		SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()), '<absent>')`,
		partitionedTable).Scan(&relkind).Error
	_ = db.Raw("SHOW search_path").Scan(&searchPath).Error
	_ = db.Raw("SELECT current_database()").Scan(&dbName).Error
	globalDB := "other"
	switch {
	case database.DB == nil:
		globalDB = "nil"
	case database.DB == db:
		globalDB = "same-as-db"
	}
	t.Logf("[flake-probe] stage=%q db=%s schema=%s relkind(unified_data)=%s "+
		"IsTablePartitioned=%v search_path=%q database.DB=%s all_schemas_unified_data=[%s]",
		stage, dbName, schema, relkind, IsTablePartitioned(db, partitionedTable),
		searchPath, globalDB, unifiedDataRelkinds(t, db))
}

func TestMigrateUnifiedData_FreshDeploy_PartitionedParentAtFinalName(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate (fresh): %v", err)
	}
	if !IsUnifiedDataPartitioned(db) {
		t.Fatal("unified_data must be a partitioned parent after fresh migration")
	}
	// 修复回归: fresh 路径母表必须落在 unified_data (非 unified_data_new),
	// 且滚动分区已就位 (-1..+3 月)。
	if tableExistsInSchema(t, db, "unified_data_new", "r") != 0 {
		t.Error("unified_data_new must be renamed away after fresh migration")
	}
	now := time.Now()
	for i := -1; i <= partitionRollaheadMonths; i++ {
		name := partitionName(partitionedTable, addMonths(now, i))
		if tableExistsInSchema(t, db, name, "r") == 0 {
			t.Errorf("expected partition %s to exist after fresh migration", name)
		}
	}
	// 新数据可写入分区母表。
	row := models.UnifiedData{DeviceID: 1, SensorName: "v", Value: 1, Timestamp: now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert into partitioned parent: %v", err)
	}
}

func TestMigrateUnifiedData_LegacyData_PreservesRowsAndAdvancesSequence(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	now := time.Now()
	oldMonth := monthStart(now).AddDate(0, -3, 0) // 3 个月前 (超出 EnsurePartitions 窗口)
	seed := []models.UnifiedData{
		{ID: 100, DeviceID: 1, SensorName: "a", Value: 1, Timestamp: oldMonth.Add(48 * time.Hour)},
		{ID: 150, DeviceID: 1, SensorName: "b", Value: 2, Timestamp: oldMonth.Add(72 * time.Hour)},
		{ID: 200, DeviceID: 2, SensorName: "c", Value: 3, Timestamp: now.Add(-24 * time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", seed[i].ID, err)
		}
	}

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate (legacy): %v", err)
	}
	if !IsUnifiedDataPartitioned(db) {
		t.Fatal("unified_data must be partitioned after legacy migration")
	}

	// 行数守恒 (迁移门禁)。
	var count int64
	db.Raw("SELECT count(*) FROM unified_data").Scan(&count)
	if count != int64(len(seed)) {
		t.Errorf("rows after migration = %d, want %d", count, len(seed))
	}
	// 迁移校验通过后 legacy 旧表被 DROP (不再保留回滚副本)。
	if tableExistsInSchema(t, db, legacyTable, "r") != 0 {
		t.Error("legacy table must be dropped after successful migration")
	}
	// 历史月份分区已创建 (修复回归: 无分区则 INSERT 报 no partition of relation)。
	if tableExistsInSchema(t, db, partitionName(partitionedTable, oldMonth), "r") == 0 {
		t.Errorf("history partition %s must exist", partitionName(partitionedTable, oldMonth))
	}

	// id 序列推进 (修复回归: 不推进则新行 id 回到 1)。
	var newID int64
	if err := db.Raw(
		"INSERT INTO unified_data (device_id, sensor_name, value, timestamp) VALUES (9, 'seq', 9, ?) RETURNING id",
		now.Add(time.Hour),
	).Scan(&newID).Error; err != nil {
		t.Fatalf("insert post-migration row: %v", err)
	}
	if newID <= 200 {
		t.Errorf("post-migration id = %d, want > 200 (sequence must advance past MAX(id))", newID)
	}

	// 幂等: 二次迁移直接跳过。
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("second migration must be idempotent, got: %v", err)
	}
}

// TestRollupTableNotCreated_Postgres 取代退役前的
// TestEnsureRollupTable_Postgres_CreateOnceIdempotent:
// 走**启动路径** (分区分支 + AutoMigrate 尾部退役) 之后, 隔离 schema 里
// 不得出现 unified_data_rollup_1m —— 表由 database.AutoMigrate() 尾部幂等 DROP,
// 且建表路径 EnsureRollupTable 已随退役删除。
//
// 这里是 PG 侧的"表在运行库中确实不存在"证据 (方言相关);
// SQLite/源码级分别由 database/retire_rollup_test.go 与
// rollup_retirement_gate_test.go 覆盖。
func TestRollupTableNotCreated_Postgres(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	// 1. 模拟"存量库里残留的旧表"。
	if err := db.Exec("CREATE TABLE unified_data_rollup_1m (device_id BIGINT, sensor_name VARCHAR(32), bucket TIMESTAMP, PRIMARY KEY (device_id, sensor_name, bucket))").Error; err != nil {
		t.Fatalf("创建模拟遗留 rollup 表: %v", err)
	}
	if tableExistsInSchema(t, db, "unified_data_rollup_1m", "r") == 0 {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// 2. 走启动期的分区分支 (退役的建表路径曾在这里的下游被调用)。
	// 前置: unified_data 需先分区化 (同 TestRetentionTask_PartitionDrop 的既有范式)。
	dropUnifiedDataFlat(t, db)
	// 可观测性锚点: 失败点 (原 155 行) 的上下游状态。详见 flakeProbe 注释。
	flakeProbe(t, db, "after-drop-flat")
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	flakeProbe(t, db, "after-migrate")
	if err := NewPartitionManager(db).EnsurePartitions(2); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	flakeProbe(t, db, "after-ensure-partitions")

	// 3. 走**生产入口**: AutoMigrate() 尾部会幂等 DROP 该表。
	prev := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = prev })
	flakeProbe(t, db, "before-automigrate")
	if err := database.AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径: %v", err)
	}
	// AutoMigrate 之后母表必须仍分区: 若这里 relkind 变了, 就是这个用例自己
	// 把母表降级 (生产 AutoMigrate 列表已不含 UnifiedData, 见 gorm.go 注释)。
	flakeProbe(t, db, "after-automigrate")
	if tableExistsInSchema(t, db, "unified_data_rollup_1m", "r") != 0 {
		t.Fatal("退役后 unified_data_rollup_1m 仍存在 —— 死表复活 (INV: 死表不得复活)")
	}

	// 4. 二次执行: 幂等, 不得报错, 不得重建。
	if err := database.AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 二次执行 (幂等性): %v", err)
	}
	flakeProbe(t, db, "after-automigrate-2")
	if tableExistsInSchema(t, db, "unified_data_rollup_1m", "r") != 0 {
		t.Fatal("二次执行后 unified_data_rollup_1m 被重建")
	}
}

// TestRetentionTask_PartitionDrop covers 11370650 的 PG-only 分支:
// unified_data 已分区时, retention 到期先整分区 DROP, 未整月到期的行
// 仍走 DELETE 批次。SQLite/未分区 PG 均跳过。
func TestRetentionTask_PartitionDrop(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 手工建一个远超 EnsurePartitions 窗口的历史分区并灌入到期数据。
	oldStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	pm := NewPartitionManager(db)
	if err := pm.createPartitionIfNotExists("unified_data_202501", partitionedTable, oldStart, addMonths(oldStart, 1)); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	ld := seedRetentionDevice(t, db, "rp", 30, oldStart.Add(48*time.Hour))
	// seedRetentionDevice 的 row 落在 unified_data (分区母表路由到 202501)。
	// 另加一条未到期行 (retention 30 天, now-48h 仍在保留窗口内)。
	dev := seedDevice(t, db, "rp-recent", "bms_jbd", "rp-recent-hw", false)
	db.Model(dev).Update("logical_device_id", ld.ID)
	// 分区滚动窗口按真实时钟创建 (EnsurePartitions -1..+3 月); 本用例固定
	// now=2026-08-01, recent 行 (now-48h) 的月份可能已漂出该窗口, 此处显式
	// 补建其月份分区, 否则 INSERT 报 "no partition of relation found"。
	// (纯 setup 修复, 不改动任何断言; 原先在 2026-08 运行时依赖窗口恰好覆盖。)
	recentMonth := monthStart(now.Add(-48 * time.Hour))
	if err := pm.createPartitionIfNotExists(partitionName(partitionedTable, recentMonth), partitionedTable, recentMonth, addMonths(recentMonth, 1)); err != nil {
		t.Fatalf("create recent-month partition: %v", err)
	}
	recent := models.UnifiedData{
		DeviceID: dev.ID, SensorName: "voltage", Value: 2,
		Timestamp: now.Add(-48 * time.Hour), LogicalDeviceID: &ld.ID,
	}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatalf("seed recent row: %v", err)
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	var found bool
	for _, res := range results {
		if res.LogicalID == ld.ID {
			found = true
			if res.Err != "" {
				t.Fatalf("retention result error: %s", res.Err)
			}
		}
	}
	if !found {
		t.Fatalf("logical device %d missing from results: %+v", ld.ID, results)
	}

	// 整月到期分区被 DROP (O(1) 路径)。
	if tableExistsInSchema(t, db, "unified_data_202501", "r") != 0 {
		t.Error("expired partition unified_data_202501 must be dropped")
	}
	// 未到期的行保留。
	var recentCount int64
	db.Model(&models.UnifiedData{}).Where("logical_device_id = ? AND timestamp > ?", ld.ID, now.Add(-30*24*time.Hour)).Count(&recentCount)
	if recentCount != 1 {
		t.Errorf("recent rows after retention = %d, want 1", recentCount)
	}
}

// TestRetention_PartitionDropUsesMaxRetention — P0 防回归 (跨设备数据丢失)。
//
// 分区 DROP 是全局整月粒度操作 (DropPartitionsBeforeFor 扫描 unified_data_%,
// 不区分数据归属), 因此 cutoff 必须由**所有**逻辑设备共同决定: 一个分区只有
// 对**每一个**数据所有者都已到期时才可删除。设分区年龄为 age, 则该条件为
//
//	age > retention_i 对所有 i 成立  ⟺  age > max(retention_i),
//
// 即安全语义要求取所有设备中**最长**的 retention_days。
//
// 取 max 而非 min: min 会让 cutoff 最靠近现在、删得最多, 与旧实现逐设备各算
// 一次 (等价于取 min) 行为一致, 无法防止长保留期设备的数据被短保留期设备连带
// 删除。本用例正是钉死这一点。
//
// 场景: A=30 天, B=365 天。
//   - 生存分区 = now-6 个月 (≈180 天): 对 A 已到期、对 B 未到期 → 必须保留。
//   - 全到期分区 = now-14 个月 (≈426 天, 远超 365): 对 A/B 均到期 → 必须 DROP。
//
// 两条断言缺一不可: 既证明该留的留住 (旧实现按单设备 A 的 30 天会整表 DROP 生存
// 分区, B 数据丢失), 也证明该删的仍会删 (若修复只是把分区 DROP 关掉, 则是"假修复")。
func TestRetention_PartitionDropUsesMaxRetention(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	surviveStart := monthStart(now).AddDate(0, -6, 0) // 2026-02: 对 A 到期, 对 B 未到期
	dropStart := monthStart(now).AddDate(0, -14, 0)   // 2025-06: 对 A/B 均到期

	pm := NewPartitionManager(db)
	for _, s := range []time.Time{surviveStart, dropStart} {
		if err := pm.createPartitionIfNotExists(partitionName(partitionedTable, s), partitionedTable, s, addMonths(s, 1)); err != nil {
			t.Fatalf("create partition %s: %v", partitionName(partitionedTable, s), err)
		}
	}

	// A: 30 天保留; B: 365 天保留。两行都落在「生存分区」内 (手工建的分区)。
	ldA := seedRetentionDevice(t, db, "maxA", 30, surviveStart.Add(24*time.Hour))
	ldB := seedRetentionDevice(t, db, "maxB", 365, surviveStart.Add(48*time.Hour))

	var devA, devB models.EdgeDevice
	if err := db.Unscoped().Where("logical_device_id = ?", ldA.ID).First(&devA).Error; err != nil {
		t.Fatalf("load A edge device: %v", err)
	}
	if err := db.Unscoped().Where("logical_device_id = ?", ldB.ID).First(&devB).Error; err != nil {
		t.Fatalf("load B edge device: %v", err)
	}
	// 全到期分区内同时含 A 与 B 的行, 证明该分区被整表 DROP。
	for _, row := range []models.UnifiedData{
		{DeviceID: devA.ID, SensorName: "voltage", Value: 7, Timestamp: dropStart.Add(24 * time.Hour), LogicalDeviceID: &ldA.ID},
		{DeviceID: devB.ID, SensorName: "voltage", Value: 8, Timestamp: dropStart.Add(48 * time.Hour), LogicalDeviceID: &ldB.ID},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed all-expired row: %v", err)
		}
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// (1) 生存分区未被 DROP: B 的 365 天尚未到期。
	if tableExistsInSchema(t, db, partitionName(partitionedTable, surviveStart), "r") == 0 {
		t.Errorf("partition %s must NOT be dropped: device B(retention=365d) still needs it",
			partitionName(partitionedTable, surviveStart))
	}
	// (2) B 在生存分区内的行仍在 (未被 A 的 30 天保留期连带删除/连带 DROP)。
	var bRows int64
	if err := db.Model(&models.UnifiedData{}).
		Where("logical_device_id = ? AND timestamp >= ? AND timestamp < ?",
			ldB.ID, surviveStart, addMonths(surviveStart, 1)).
		Count(&bRows).Error; err != nil {
		t.Fatalf("count B rows: %v", err)
	}
	if bRows != 1 {
		t.Errorf("device B rows in surviving partition = %d, want 1 (A's 30d retention must not touch B's 365d data)", bRows)
	}
	// (3) 正向断言: 对**所有**设备都已到期 (≈426 天 > 365 天) 的分区必须被 DROP。
	// 这条防"假修复": 若把全局分区清扫整个关掉, 该断言会失败 —— 证明修复是
	// "按最长保留期清扫", 而非"不再清扫"。
	if tableExistsInSchema(t, db, partitionName(partitionedTable, dropStart), "r") != 0 {
		t.Errorf("all-expired partition %s must be dropped (fix must still reap fully expired partitions)",
			partitionName(partitionedTable, dropStart))
	}
}

// TestRollupRetentionUnaffected verifies partition DROP 的 cutoff 计算:
// cutoff 月当月分区不删 (整月未到期)。
func TestDropPartitionsBefore_CurrentMonthKept(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropUnifiedDataFlat(t, db)
	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now()
	curName := partitionName(partitionedTable, now)
	if tableExistsInSchema(t, db, curName, "r") == 0 {
		t.Skipf("partition %s unexpectedly missing", curName)
	}
	pm := NewPartitionManager(db)
	dropped, err := pm.DropPartitionsBefore(now) // cutoff=当月 → 当月分区起点不 < cutoff 月起点
	if err != nil {
		t.Fatalf("DropPartitionsBefore: %v", err)
	}
	for _, d := range dropped {
		if d == curName {
			t.Errorf("current-month partition %s must not be dropped", curName)
		}
	}
	if tableExistsInSchema(t, db, curName, "r") == 0 {
		t.Error("current-month partition must survive")
	}
}

// ==================== P0: 单月 id 稀疏多批搬迁 ====================

// seedUnifiedFlatMonth inserts n rows into the flat unified_data, all inside
// month m, with deliberately sparse ids (stride 101) to mirror the production
// table's id holes. idBase gives each seeded month a disjoint id range.
func seedUnifiedFlatMonth(t *testing.T, db *gorm.DB, m time.Time, idBase, n int) {
	t.Helper()
	if err := db.Exec(fmt.Sprintf(
		`INSERT INTO unified_data (id, device_id, sensor_name, value, timestamp)
		 SELECT gs*101 + ?, 1, 'v', 1.0, ?::timestamptz + (gs * interval '1 minute')
		 FROM generate_series(1, %d) AS gs`, n),
		idBase, m.Add(12*time.Hour)).Error; err != nil {
		t.Fatalf("seed %d rows for month %s: %v", n, m.Format("2006-01"), err)
	}
}

// countUnifiedMonth counts the rows of the flat/partitioned unified_data parent
// that fall in [m, m+1).
func countUnifiedMonth(t *testing.T, db *gorm.DB, m time.Time) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(
		"SELECT count(*) FROM unified_data WHERE timestamp >= ? AND timestamp < ?",
		m, addMonths(m, 1),
	).Scan(&n).Error; err != nil {
		t.Fatalf("count unified_data month %s: %v", m.Format("2006-01"), err)
	}
	return n
}

// TestMigrateUnifiedData_MultiBatchCopiesAllRows — P0 防回归 (单月 id 稀疏丢数据)。
//
// 复刻真实开发库形态: 平表 87487 行全部落在同一个 2026-08, 且 id 稀疏
// (1..2094379, 大量空洞)。旧实现搬走第一批 10000 行后, 水位被错误地推进到
// "整月剩余行的 max(id)" (== 整月最大 id), 第二批 `id > watermark` 无行 →
// 提前退出, 恰好只搬了 migrateBatchSizePostgres (10000) 行, 直到行数门禁报
// src=87487 dst=10000 才中止。
//
// 本用例造单月 25000 行 (> 2 个批次) 且 id 稀疏: 正确实现必须精确搬完全部
// 25000 行。变异自证: 把水位改回"整月 max(id)"后, 本用例失败于 dst=10000。
func TestMigrateUnifiedData_MultiBatchCopiesAllRows(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	const n = 25000
	month := monthStart(time.Now()).AddDate(0, -2, 0)
	seedUnifiedFlatMonth(t, db, month, 7, n)

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migration must copy every row in a sparse-id single month: %v", err)
	}
	if !IsUnifiedDataPartitioned(db) {
		t.Fatal("unified_data must be partitioned after migration")
	}
	var total int64
	if err := db.Raw("SELECT count(*) FROM unified_data").Scan(&total).Error; err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if total != n {
		t.Fatalf("dst rows = %d, want %d (exact conservation; old code stops at %d)",
			total, n, migrateBatchSizePostgres)
	}
	if got := countUnifiedMonth(t, db, month); got != n {
		t.Errorf("month %s rows = %d, want %d", month.Format("2006-01"), got, n)
	}
	if tableExistsInSchema(t, db, legacyTable, "r") != 0 {
		t.Error("legacy flat table must be dropped after successful migration")
	}
}

// TestMigrateUnifiedData_MultiMonthCopiesAllRows — 多个月份的守恒 (至少一个月
// 超过一个批次): 旧实现在每个 >1 批的月份都会提前退出, 正确实现在任意月份数 /
// 月内行数分布下都精确守恒。
func TestMigrateUnifiedData_MultiMonthCopiesAllRows(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	now := time.Now()
	months := []time.Time{
		monthStart(now).AddDate(0, -3, 0),
		monthStart(now).AddDate(0, -2, 0),
		monthStart(now).AddDate(0, -1, 0),
	}
	counts := []int{12000, 11000, 300}
	const idStride = 5000000
	total := 0
	for i, m := range months {
		seedUnifiedFlatMonth(t, db, m, 7+i*idStride, counts[i])
		total += counts[i]
	}

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migration must copy every row across months: %v", err)
	}
	var got int64
	if err := db.Raw("SELECT count(*) FROM unified_data").Scan(&got).Error; err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if got != int64(total) {
		t.Fatalf("dst rows = %d, want %d across %d months", got, total, len(months))
	}
	for i, m := range months {
		if c := countUnifiedMonth(t, db, m); c != int64(counts[i]) {
			t.Errorf("month %s rows = %d, want %d", m.Format("2006-01"), c, counts[i])
		}
	}
}

// TestMigrateUnifiedData_ResumesAfterStalePartitions — M3 (平表 + 遗留分区).
//
// 复刻开发库的混合态: 上次失败迁移留下
//
//	(a) detached 的 <table>_YYYYMM 残留表 (含部分源行副本);
//	(b) 残留的 <table>_new 母表 + 挂在它下面的 <table>_YYYYMM 分区 (含部分源行副本)。
//
// 旧实现会因 (a) 使 createPartitionIfNotExists 误判"分区已存在"而跳过在 new
// 母表下建分区, 随后 INSERT 报 "no partition of relation found", 重跑永不收敛。
// 修复后必须清理残留并精确守恒 (残留副本既不泄漏也不挡路)。
func TestMigrateUnifiedData_ResumesAfterStalePartitions(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	now := time.Now()
	oldM := monthStart(now).AddDate(0, -3, 0)      // 挂在残留 _new 下的月份
	detachedM := monthStart(now).AddDate(0, -2, 0) // detached 残留月份
	seedUnifiedFlatMonth(t, db, oldM, 100000, 30)
	seedUnifiedFlatMonth(t, db, detachedM, 7, 50)

	// (a) detached 残留: 独立的 <table>_YYYYMM 普通表, 含 10 行源数据副本。
	detachedName := partitionName(partitionedTable, detachedM)
	if err := db.Exec(fmt.Sprintf(
		"CREATE TABLE %s (id BIGINT, device_id BIGINT, sensor_name VARCHAR(32), value DOUBLE PRECISION, unit VARCHAR(16), timestamp TIMESTAMPTZ, created_at TIMESTAMPTZ, edge_device_id BIGINT, logical_device_id BIGINT)",
		detachedName)).Error; err != nil {
		t.Fatalf("create detached stale partition: %v", err)
	}
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, device_id, sensor_name, value, timestamp) SELECT id, device_id, sensor_name, value, timestamp FROM unified_data WHERE timestamp >= ? AND timestamp < ? ORDER BY id LIMIT 10",
		detachedName), detachedM, addMonths(detachedM, 1)).Error; err != nil {
		t.Fatalf("seed detached stale partition: %v", err)
	}

	// (b) 残留 _new 母表 + 其下的月份分区 (含 5 行源数据副本)。
	if err := createPartitionedParent(db, partitionedTable, partitionedTable+"_new", false); err != nil {
		t.Fatalf("create stale new parent: %v", err)
	}
	pm := NewPartitionManager(db)
	stalePart := partitionName(partitionedTable, oldM)
	if err := pm.createPartitionIfNotExists(stalePart, partitionedTable+"_new", oldM, addMonths(oldM, 1)); err != nil {
		t.Fatalf("create stale new-parent partition: %v", err)
	}
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, device_id, sensor_name, value, timestamp) SELECT id, device_id, sensor_name, value, timestamp FROM unified_data WHERE timestamp >= ? AND timestamp < ? ORDER BY id LIMIT 5",
		stalePart), oldM, addMonths(oldM, 1)).Error; err != nil {
		t.Fatalf("seed stale new-parent partition: %v", err)
	}

	if err := MigrateUnifiedDataToPartitioned(db); err != nil {
		t.Fatalf("migration must converge after stale partitions (M3): %v", err)
	}
	var total int64
	if err := db.Raw("SELECT count(*) FROM unified_data").Scan(&total).Error; err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if total != 80 {
		t.Fatalf("dst rows = %d, want 80 (30 + 50; stale copies must not leak nor block)", total)
	}
	// detached 残留被清理后重建为真正的分区, 且恰好等于源行数 (50): 残留的 10
	// 行副本既未泄漏也没挡住新数据。
	var detachedRows int64
	if err := db.Raw("SELECT count(*) FROM " + detachedName).Scan(&detachedRows).Error; err != nil {
		t.Fatalf("count detached month partition: %v", err)
	}
	if detachedRows != 50 {
		t.Errorf("%s rows = %d, want 50 (stale 10-row copy must be cleaned, all 50 source rows copied)",
			detachedName, detachedRows)
	}
	if !IsUnifiedDataPartitioned(db) {
		t.Error("unified_data must be a partitioned parent after convergent re-run")
	}
	if tableExistsInSchema(t, db, legacyTable, "r") != 0 {
		t.Error("legacy flat table must be dropped after successful migration")
	}
}

// TestCopyMonthBatched_ConflictRowsDoNotStopBatches — M1 退出条件与
// RowsAffected 解耦的直接证明。
//
// 先在 dst 预置本批 10000 行中的 5000 行 (制造 5000 次 ON CONFLICT DO
// NOTHING), 旧实现会因 RowsAffected=5000 < batchSize 立即返回, 只搬走 15000
// 行中的 10000 行。修复后必须搬完全部 15000 行。
func TestCopyMonthBatched_ConflictRowsDoNotStopBatches(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	const n = 15000
	month := monthStart(time.Now()).AddDate(0, -3, 0)
	seedUnifiedFlatMonth(t, db, month, 7, n)

	dst := "unified_data_new"
	if err := db.Exec("DROP TABLE IF EXISTS " + dst + " CASCADE").Error; err != nil {
		t.Fatalf("drop dst: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + dst + " CASCADE") })
	if err := createPartitionedParent(db, partitionedTable, dst, false); err != nil {
		t.Fatalf("create dst parent: %v", err)
	}
	pm := NewPartitionManager(db)
	if err := pm.createPartitionIfNotExists(partitionName(partitionedTable, month), dst, month, addMonths(month, 1)); err != nil {
		t.Fatalf("create dst partition: %v", err)
	}
	// 预置前 5000 行 (与源同 id/timestamp) → 本批必然发生 5000 次冲突。
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id, logical_device_id) SELECT id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id, logical_device_id FROM unified_data WHERE timestamp >= ? AND timestamp < ? ORDER BY id LIMIT 5000",
		dst), month, addMonths(month, 1)).Error; err != nil {
		t.Fatalf("pre-seed dst conflicts: %v", err)
	}

	if err := copyMonthBatched(db, dst, partitionedTable, month); err != nil {
		t.Fatalf("copyMonthBatched: %v", err)
	}
	var got int64
	if err := db.Raw("SELECT count(*) FROM "+dst+" WHERE timestamp >= ? AND timestamp < ?",
		month, addMonths(month, 1)).Scan(&got).Error; err != nil {
		t.Fatalf("count dst rows: %v", err)
	}
	if got != n {
		t.Fatalf("dst rows = %d, want %d (conflicts must not terminate the batch loop)", got, n)
	}
}
