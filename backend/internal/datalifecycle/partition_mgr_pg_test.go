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
		name := partitionName(addMonths(now, i))
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
	if tableExistsInSchema(t, db, partitionName(oldMonth), "r") == 0 {
		t.Errorf("history partition %s must exist", partitionName(oldMonth))
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
	curName := partitionName(now)
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
