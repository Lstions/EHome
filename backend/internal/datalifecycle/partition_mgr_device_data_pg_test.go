package datalifecycle

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// ==================== device_data 分区迁移 (PG-only) ====================

// seedDeviceDataDevice creates one edge device (no logical identity — 该概念层
// 已整体移除, 2026-10-23).
func seedDeviceDataDevice(t *testing.T, db *gorm.DB, key string) *models.EdgeDevice {
	t.Helper()
	return seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
}

// seedDeviceDataRow inserts one device_data row routed by its timestamp.
func seedDeviceDataRow(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time, dataJSON string) {
	t.Helper()
	row := models.DeviceData{
		DeviceID:  deviceID,
		NodeID:    "NODE001",
		DataJSON:  dataJSON,
		Timestamp: ts,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed device_data row: %v", err)
	}
}

// TestMigrateDeviceDataToPartitioned covers the legacy-data path: flat
// device_data + rows spanning months → partitioned parent (relkind='p') with
// month partitions, row/column conservation, legacy snapshot DROPped after the
// row-count gate and idempotent re-run.
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
	// legacy 平表在校验通过后被 DROP (不再保留回滚副本)。
	if tableExistsInSchema(t, db, "device_data_legacy", "r") != 0 {
		t.Error("device_data_legacy must be dropped after successful migration")
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

// TestDeviceDataPartitionParentIndexes — 分区迁移后父表索引必须齐备、有效、归属正确。
//
// 2026-10-23 语义变更: 原用例 TestDeviceDataLogicalIndexSurvivesPartitionMigration
// 钉的是 indexes.go 的 (logical_device_id, timestamp DESC) 复合索引在分区迁移后
// 仍归 device_data 所有。逻辑设备概念层移除后，indexes.go 与
// EnsureLogicalDataIndexes / IndexDevDataLogicalTS / IndexExists 一并删除 ——
// 该列已不存在，索引不可能存在。
//
// 但**同类性质仍然成立且必须守着**: 分区迁移会把平表的 b-tree 索引在分区母表上
// 按 partitionParentIndexes 重建；若重建失败或索引挂到 legacy 快照上，查询会退化成
// 全分区扫描。因此改为钉住「迁移后 device_data 上确实存在覆盖各列的合法索引」。
//
// 为什么按**列**断言而不是按索引名断言（实测教训）:
// 旧库路径的母表先建成 <table>_new，索引也就命名为 device_data_new_<col>_idx；
// PG 的 ALTER TABLE ... RENAME TO 不会重命名索引，所以迁移完成后索引名仍是
// device_data_new_ 前缀。按名字断言会对着正确的行为报红（写成名字断言后本用例
// 在 PG 上确实红了）。索引名不是契约，**能服务这些查询**才是。
func TestDeviceDataPartitionParentIndexes(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate device_data: %v", err)
	}
	if !IsTablePartitioned(db, "device_data") {
		t.Fatal("device_data must be a partitioned parent after migration")
	}

	// 期望的索引列集由生产模板推导 (模板形如 "CREATE INDEX %s_<col>_idx ON %s (<cols>)")，
	// 避免测试与生产各抄一份清单后静默漂移。
	var wantCols []string
	for _, tmpl := range partitionParentIndexes["device_data"] {
		open := strings.LastIndex(tmpl, "(")
		closeIdx := strings.LastIndex(tmpl, ")")
		if open < 0 || closeIdx <= open {
			t.Fatalf("无法解析索引模板 %q (期望 ... ON <table> (<cols>))", tmpl)
		}
		cols := strings.TrimSpace(tmpl[open+1 : closeIdx])
		if cols == "" {
			t.Fatalf("索引模板 %q 的列清单为空", tmpl)
		}
		wantCols = append(wantCols, "("+cols+")")
	}
	if len(wantCols) == 0 {
		t.Fatal("partitionParentIndexes[device_data] 为空 —— 迁移不再重建任何父表索引")
	}

	// indexesOn 返回当前 schema 下挂在 table 上且 valid 的索引定义。
	indexesOn := func(t *testing.T) []string {
		t.Helper()
		var defs []string
		if err := db.Raw(`SELECT pg_get_indexdef(x.indexrelid)
FROM pg_index x
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE t.relname = ? AND n.nspname = current_schema() AND x.indisvalid`, "device_data").
			Scan(&defs).Error; err != nil {
			t.Fatalf("list indexes on device_data: %v", err)
		}
		return defs
	}
	assertColsCovered := func(t *testing.T, stage string) {
		t.Helper()
		defs := indexesOn(t)
		if len(defs) == 0 {
			t.Fatalf("%s: device_data 上没有任何有效索引 —— 查询会退化成全分区扫描", stage)
		}
		for _, cols := range wantCols {
			found := false
			for _, def := range defs {
				// pg_get_indexdef 会给保留字加引号 ("timestamp")，比对前统一去掉。
				norm := func(s string) string { return strings.ReplaceAll(s, "\"", "") }
				if strings.HasSuffix(norm(strings.TrimSpace(def)), norm(cols)) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: device_data 上缺少覆盖 %s 的索引；现有索引=%v", stage, cols, defs)
			}
		}
	}

	assertColsCovered(t, "迁移后")

	// 幂等: 二次迁移不得报错, 也不得丢掉索引。
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("second migration must be idempotent, got: %v", err)
	}
	assertColsCovered(t, "幂等二次迁移后")
}

// TestDeviceDataPartitionDropUsesSystemWindow — device_data 侧的分区清扫测试。
//
// 2026-10-23 语义变更: cutoff 不再是「所有逻辑设备中最长 retention」，而是
// 一条系统级窗口（SystemRetentionDays）。因此场景简化为: 窗口内的分区必须保留，
// 整月都已早于 cutoff 的分区必须 DROP。
//
// 场景: retention=90 天, now=2026-08-01 → cutoff=2026-05-03。
//   - 分区 2026-08（窗口内）→ 必须保留；
//   - 分区 2025-06（整月都早于 cutoff）→ 必须 DROP。
func TestDeviceDataPartitionDropUsesSystemWindow(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS device_data CASCADE").Error; err != nil {
		t.Fatalf("drop flat device_data: %v", err)
	}
	if err := MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	keepStart := monthStart(now)                    // 2026-08: 窗口内
	dropStart := monthStart(now).AddDate(0, -14, 0) // 2025-06: 整月都早于 cutoff

	pm := NewPartitionManager(db)
	for _, s := range []time.Time{keepStart, dropStart} {
		if err := pm.createPartitionIfNotExists(partitionName("device_data", s), "device_data", s, addMonths(s, 1)); err != nil {
			t.Fatalf("create partition %s: %v", partitionName("device_data", s), err)
		}
	}

	dev := seedDeviceDataDevice(t, db, "ddA")
	seedDeviceDataRow(t, db, dev.ID, keepStart.Add(24*time.Hour), `{"a":1}`)
	seedDeviceDataRow(t, db, dev.ID, dropStart.Add(24*time.Hour), `{"a":2}`)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// (1) 窗口内分区必须保留，行仍在。
	if tableExistsInSchema(t, db, partitionName("device_data", keepStart), "r") == 0 {
		t.Errorf("partition %s must NOT be dropped: it is inside the 90-day window",
			partitionName("device_data", keepStart))
	}
	var kept int64
	if err := db.Model(&models.DeviceData{}).
		Where("device_id = ? AND timestamp >= ? AND timestamp < ?",
			dev.ID, keepStart, addMonths(keepStart, 1)).
		Count(&kept).Error; err != nil {
		t.Fatalf("count kept rows: %v", err)
	}
	if kept != 1 {
		t.Errorf("rows in surviving partition = %d, want 1", kept)
	}
	// (2) 正向断言: 整月都已过期的分区必须 DROP (防「假修复」: 关掉清扫)。
	if tableExistsInSchema(t, db, partitionName("device_data", dropStart), "r") != 0 {
		t.Errorf("all-expired partition %s must be dropped",
			partitionName("device_data", dropStart))
	}
}
