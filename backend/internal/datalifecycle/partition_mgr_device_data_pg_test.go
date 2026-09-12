package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// ==================== device_data 分区迁移 (PG-only) ====================

// seedDeviceDataLogical creates a logical device with retentionDays plus one
// attached edge-device instance (mounting logical_device_id before any write).
func seedDeviceDataLogical(t *testing.T, db *gorm.DB, key string, retentionDays int) (*models.LogicalDevice, *models.EdgeDevice) {
	t.Helper()
	ld := &models.LogicalDevice{IdentityKey: key, Name: key, DeviceType: "bms_jbd", RetentionDays: retentionDays}
	if err := db.Create(ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	dev := seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
	if err := db.Model(dev).Update("logical_device_id", ld.ID).Error; err != nil {
		t.Fatalf("mount logical_device_id: %v", err)
	}
	return ld, dev
}

// seedDeviceDataRow inserts one device_data row routed by its timestamp.
func seedDeviceDataRow(t *testing.T, db *gorm.DB, deviceID, logicalID uint, ts time.Time, dataJSON string) {
	t.Helper()
	row := models.DeviceData{
		DeviceID:        deviceID,
		NodeID:          "NODE001",
		DataJSON:        dataJSON,
		Timestamp:       ts,
		LogicalDeviceID: &logicalID,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed device_data row: %v", err)
	}
}

// TestMigrateDeviceDataToPartitioned covers the legacy-data path: flat
// device_data + rows spanning months → partitioned parent (relkind='p') with
// month partitions, row/column conservation, retained legacy snapshot and
// idempotent re-run.
func TestMigrateDeviceDataToPartitioned(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	now := time.Now()
	oldMonth := monthStart(now).AddDate(0, -3, 0)
	seed := []models.DeviceData{
		{ID: 100, DeviceID: 1, NodeID: "n1", DataJSON: `{"v":1}`, Timestamp: oldMonth.Add(48 * time.Hour)},
		{ID: 150, DeviceID: 1, NodeID: "n1", DataJSON: `{"v":2}`, Timestamp: oldMonth.Add(72 * time.Hour)},
		{ID: 200, DeviceID: 2, NodeID: "n2", DataJSON: `{"v":3}`, Timestamp: monthStart(now).Add(24 * time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", seed[i].ID, err)
		}
	}

	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}
	if !IsTablePartitioned(db, "device_data") {
		t.Fatal("device_data must be a partitioned parent after migration")
	}
	if got := tableExistsInSchema(t, db, "device_data", "p"); got != 1 {
		t.Errorf("device_data relkind='p' count = %d, want 1", got)
	}
	// 历史月份与当月分区均已创建 (无分区则 INSERT 报 no partition of relation)。
	for _, m := range []time.Time{oldMonth, monthStart(now)} {
		if tableExistsInSchema(t, db, partitionName("device_data", m), "r") == 0 {
			t.Errorf("partition %s must exist", partitionName("device_data", m))
		}
	}
	// 行数守恒 (迁移门禁)。
	var count int64
	if err := db.Model(&models.DeviceData{}).Count(&count).Error; err != nil {
		t.Fatalf("count after migration: %v", err)
	}
	if count != int64(len(seed)) {
		t.Errorf("rows after migration = %d, want %d", count, len(seed))
	}
	// 列值 (node_id / data_json) 搬迁无损。
	var got models.DeviceData
	if err := db.Where("id = ?", 100).First(&got).Error; err != nil {
		t.Fatalf("load migrated row: %v", err)
	}
	if got.NodeID != "n1" || got.DataJSON != `{"v":1}` || got.DeviceID != 1 {
		t.Errorf("migrated row = %+v, want device_id=1 node_id=n1 data_json=%s", got, `{"v":1}`)
	}
	// legacy 平表保留不删 (降险)。
	if tableExistsInSchema(t, db, "device_data_legacy", "r") == 0 {
		t.Error("device_data_legacy must be retained after migration")
	}
	// 幂等: 二次迁移直接跳过, 不报错、不重复搬。
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("second migration must be idempotent, got: %v", err)
	}
	if err := db.Model(&models.DeviceData{}).Count(&count).Error; err != nil {
		t.Fatalf("count after second migration: %v", err)
	}
	if count != int64(len(seed)) {
		t.Errorf("rows after second migration = %d, want %d", count, len(seed))
	}
}

// TestMigrateDeviceData_FreshDeploy_PartitionedParentAtFinalName mirrors the
// unified_data fresh path: no flat table → parent at the final name (not _new)
// plus the rolling partitions, and the parent accepts writes.
func TestMigrateDeviceData_FreshDeploy_PartitionedParentAtFinalName(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop flat device_data: %v", err)
	}

	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate (fresh): %v", err)
	}
	if !IsTablePartitioned(db, "device_data") {
		t.Fatal("device_data must be a partitioned parent after fresh migration")
	}
	if tableExistsInSchema(t, db, "device_data_new", "r") != 0 {
		t.Error("device_data_new must be renamed away after fresh migration")
	}
	now := time.Now()
	for i := -1; i <= partitionRollaheadMonths; i++ {
		name := partitionName("device_data", addMonths(now, i))
		if tableExistsInSchema(t, db, name, "r") == 0 {
			t.Errorf("expected partition %s to exist after fresh migration", name)
		}
	}
	row := models.DeviceData{DeviceID: 1, NodeID: "n", DataJSON: `{}`, Timestamp: now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert into partitioned parent: %v", err)
	}
}

// TestDeviceDataLogicalIndexSurvivesPartitionMigration pins the indexes.go
// partitioned branch: PG names indexes schema-globally, so after the swap the
// canonical composite index name is still held by device_data_legacy. The
// migration-time partition sweep only happens while releaseIndexNameFromNonTarget
// returns the canonical name to the partitioned parent — otherwise the parent
// silently loses the (logical_device_id, timestamp DESC) index.
func TestDeviceDataLogicalIndexSurvivesPartitionMigration(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	ctx := context.Background()

	// 迁移前先在平表上建好既有复合索引 (生产迁移时的真实前置状态)。
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("pre-migration ensure: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("post-migration ensure: %v", err)
	}

	var owner string
	if err := db.Raw(`SELECT t.relname FROM pg_class i
JOIN pg_index x ON x.indexrelid = i.oid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = i.relnamespace
WHERE i.relname = ? AND n.nspname = current_schema()`, IndexDevDataLogicalTS).
		Scan(&owner).Error; err != nil {
		t.Fatalf("resolve owner of %s: %v", IndexDevDataLogicalTS, err)
	}
	if owner != "device_data" {
		t.Fatalf("%s attached to %q, want device_data partitioned parent", IndexDevDataLogicalTS, owner)
	}
	exists, err := IndexExists(ctx, db, IndexDevDataLogicalTS)
	if err != nil || !exists {
		t.Fatalf("IndexExists(%s) = %v, %v; want true (valid)", IndexDevDataLogicalTS, exists, err)
	}
	// 幂等: 再次 ensure 不得报错或遗失索引。
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("third ensure: %v", err)
	}
}

// TestDeviceDataPartitionDropRespectsMaxRetention — device_data 侧的 P0 防回归。
// 与 unified_data 的 TestRetention_PartitionDropUsesMaxRetention 同构: cutoff 由
// 所有逻辑设备中最长 retention 决定, 一个分区只有对每一个数据所有者都已到期
// 才可整月 DROP。
//
// 场景: A=30 天, B=365 天; now=2026-08-01。
//   - 生存分区 2026-02 (≈180 天): 对 A 到期、对 B 未到期 → 必须保留, B 行仍在。
//   - 全到期分区 2025-06 (≈426 天): 对 A/B 均到期 → 必须 DROP。
func TestDeviceDataPartitionDropRespectsMaxRetention(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop flat device_data: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	surviveStart := monthStart(now).AddDate(0, -6, 0) // 2026-02
	dropStart := monthStart(now).AddDate(0, -14, 0)   // 2025-06

	pm := NewPartitionManager(db)
	for _, s := range []time.Time{surviveStart, dropStart} {
		if err := pm.createPartitionIfNotExists(partitionName("device_data", s), "device_data", s, addMonths(s, 1)); err != nil {
			t.Fatalf("create partition %s: %v", partitionName("device_data", s), err)
		}
	}

	// A: 30 天保留; B: 365 天保留。
	ldA, devA := seedDeviceDataLogical(t, db, "ddA", 30)
	ldB, devB := seedDeviceDataLogical(t, db, "ddB", 365)

	// 生存分区内 A/B 各一行；全到期分区内 A/B 各一行。
	seedDeviceDataRow(t, db, devA.ID, ldA.ID, surviveStart.Add(24*time.Hour), `{"a":1}`)
	seedDeviceDataRow(t, db, devB.ID, ldB.ID, surviveStart.Add(48*time.Hour), `{"b":1}`)
	seedDeviceDataRow(t, db, devA.ID, ldA.ID, dropStart.Add(24*time.Hour), `{"a":2}`)
	seedDeviceDataRow(t, db, devB.ID, ldB.ID, dropStart.Add(48*time.Hour), `{"b":2}`)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// (1) 生存分区未被 DROP: B 的 365 天尚未到期。
	if tableExistsInSchema(t, db, partitionName("device_data", surviveStart), "r") == 0 {
		t.Errorf("partition %s must NOT be dropped: device B(retention=365d) still needs it",
			partitionName("device_data", surviveStart))
	}
	// (2) B 在生存分区内的行仍在 (未被 A 的 30 天保留期连带 DROP)。
	var bRows int64
	if err := db.Model(&models.DeviceData{}).
		Where("logical_device_id = ? AND timestamp >= ? AND timestamp < ?",
			ldB.ID, surviveStart, addMonths(surviveStart, 1)).
		Count(&bRows).Error; err != nil {
		t.Fatalf("count B rows: %v", err)
	}
	if bRows != 1 {
		t.Errorf("device B rows in surviving partition = %d, want 1 (A's 30d retention must not touch B's 365d data)", bRows)
	}
	// (3) 正向断言: 对所有设备都已到期的分区必须 DROP (防"假修复": 关掉清扫)。
	if tableExistsInSchema(t, db, partitionName("device_data", dropStart), "r") != 0 {
		t.Errorf("all-expired partition %s must be dropped (sweep must keep reaping fully expired partitions)",
			partitionName("device_data", dropStart))
	}
}
