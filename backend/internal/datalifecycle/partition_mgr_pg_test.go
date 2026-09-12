package datalifecycle

import (
	"testing"
	"time"

	"gorm.io/gorm"

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
	// legacy 表保留不删 (降险)。
	if tableExistsInSchema(t, db, legacyTable, "r") == 0 {
		t.Error("legacy table must be retained after migration")
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

func TestEnsureRollupTable_Postgres_CreateOnceIdempotent(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	if err := EnsureRollupTable(db); err != nil {
		t.Fatalf("EnsureRollupTable: %v", err)
	}
	if tableExistsInSchema(t, db, "unified_data_rollup_1m", "r") == 0 {
		t.Fatal("rollup table must exist after EnsureRollupTable")
	}
	// 幂等: 重复调用不报错。
	if err := EnsureRollupTable(db); err != nil {
		t.Fatalf("EnsureRollupTable second call: %v", err)
	}
}

func TestEnsureRollupTable_SQLite_Noop(t *testing.T) {
	db := newSQLiteDB(t)
	if err := EnsureRollupTable(db); err != nil {
		t.Fatalf("EnsureRollupTable on sqlite must be no-op, got %v", err)
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
