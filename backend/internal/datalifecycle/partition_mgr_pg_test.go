package datalifecycle

import (
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

// TestRetentionTask_PartitionDrop — 全库分区清扫把整月过期的 telemetry_frame
// 分区 DROP，窗口内的行保留（2026-11-14：unified_data 退役后改为覆盖帧表）。
func TestRetentionTask_PartitionDrop(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("migrate frame: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)

	oldStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	pm := NewPartitionManager(db)
	if err := pm.createPartitionIfNotExists(partitionName(TelemetryFrameTable, oldStart), TelemetryFrameTable, oldStart, addMonths(oldStart, 1)); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	dev := seedDevice(t, db, "rp", "bms_jbd", "rp-hw", false)
	recentMonth := monthStart(now.Add(-48 * time.Hour))
	if err := pm.createPartitionIfNotExists(partitionName(TelemetryFrameTable, recentMonth), TelemetryFrameTable, recentMonth, addMonths(recentMonth, 1)); err != nil {
		t.Fatalf("create recent-month partition: %v", err)
	}

	for _, row := range []models.TelemetryFrame{
		{DeviceID: dev.ID, TS: oldStart.Add(48 * time.Hour), FrameKind: 0, SlotID: 1, Payload: []byte{0}},
		{DeviceID: dev.ID, TS: now.Add(-48 * time.Hour), FrameKind: 0, SlotID: 1, Payload: []byte{1}},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed frame: %v", err)
		}
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if tableExistsInSchema(t, db, partitionName(TelemetryFrameTable, oldStart), "r") != 0 {
		t.Error("expired partition must be dropped")
	}
	var recentCount int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ? AND ts > ?", dev.ID, now.Add(-30*24*time.Hour)).Count(&recentCount)
	if recentCount != 1 {
		t.Errorf("recent rows after retention = %d, want 1", recentCount)
	}
}

// TestRetention_PartitionDropUsesSystemWindow — 分区清扫按系统级窗口执行。
//
// 2026-11-14：改以 telemetry_frame 为载体（unified_data 已退役）。
// 要钉死两条性质：窗口内分区保留；整月过期的分区必须 DROP。
func TestRetention_PartitionDropUsesSystemWindow(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("migrate frame: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	keepStart := monthStart(now)
	dropStart := monthStart(now).AddDate(0, -14, 0)

	pm := NewPartitionManager(db)
	for _, s := range []time.Time{keepStart, dropStart} {
		if err := pm.createPartitionIfNotExists(partitionName(TelemetryFrameTable, s), TelemetryFrameTable, s, addMonths(s, 1)); err != nil {
			t.Fatalf("create partition %s: %v", partitionName(TelemetryFrameTable, s), err)
		}
	}

	dev := seedDevice(t, db, "win", "bms_jbd", "win-hw", false)
	for _, row := range []models.TelemetryFrame{
		{DeviceID: dev.ID, TS: keepStart.Add(24 * time.Hour), FrameKind: 0, SlotID: 1, Payload: []byte{7}},
		{DeviceID: dev.ID, TS: dropStart.Add(24 * time.Hour), FrameKind: 0, SlotID: 1, Payload: []byte{8}},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed frame: %v", err)
		}
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if tableExistsInSchema(t, db, partitionName(TelemetryFrameTable, keepStart), "r") == 0 {
		t.Errorf("partition %s must NOT be dropped: it is inside the 90-day window",
			partitionName(TelemetryFrameTable, keepStart))
	}
	var kept int64
	if err := db.Model(&models.TelemetryFrame{}).
		Where("device_id = ? AND ts >= ? AND ts < ?",
			dev.ID, keepStart, addMonths(keepStart, 1)).
		Count(&kept).Error; err != nil {
		t.Fatalf("count kept rows: %v", err)
	}
	if kept != 1 {
		t.Errorf("kept rows = %d, want 1", kept)
	}
	if tableExistsInSchema(t, db, partitionName(TelemetryFrameTable, dropStart), "r") != 0 {
		t.Errorf("all-expired partition %s must be dropped",
			partitionName(TelemetryFrameTable, dropStart))
	}
}
