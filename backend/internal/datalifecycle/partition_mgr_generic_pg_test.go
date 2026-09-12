package datalifecycle

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/testutil"
)

// PG-only 集成测试: 泛化后的 MigrateTableToPartitioned 可服务任意时序表。
// 非 PG 方言下 skip (与 partition_mgr_pg_test.go 的 requirePostgres 同款)。
//
// 使用临时表 zz_partition_probe, 测完 DROP; 不触碰 unified_data / device_data。

// dropProbeTables idempotently removes probe relations (and any partitions)
// so the test can rerun against a dirty schema.
func dropProbeTables(t *testing.T, db *gorm.DB, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", n)).Error; err != nil {
			t.Fatalf("drop probe table %s: %v", n, err)
		}
	}
}

func TestMigrateTableToPartitioned_GenericTable(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	const (
		probe       = "zz_partition_probe"
		probeLegacy = "zz_partition_probe_legacy"
	)
	// 幂等清理可能残留的探针关系, 并注册结束时 DROP (CASCADE 连带分区)。
	dropProbeTables(t, db, probe, probe+"_new", probeLegacy)
	t.Cleanup(func() { dropProbeTables(t, db, probe, probe+"_new", probeLegacy) })

	// 平表结构须与迁移读取的列一致 (copyMonthBatched 的列清单)。
	ddl := fmt.Sprintf(
		"CREATE TABLE %s (id BIGSERIAL, device_id BIGINT NOT NULL, sensor_name VARCHAR(32) NOT NULL, value DOUBLE PRECISION NOT NULL, unit VARCHAR(16), timestamp TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ, edge_device_id BIGINT, logical_device_id BIGINT, PRIMARY KEY (id, timestamp))",
		probe,
	)
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create probe flat table: %v", err)
	}

	now := time.Now()
	oldMonth := monthStart(now).AddDate(0, -3, 0) // 超出滚动窗口, 逼出历史分区创建
	seeded := []struct {
		id     int64
		ts     time.Time
		sensor string
	}{
		{id: 11, ts: oldMonth.Add(24 * time.Hour), sensor: "a"},
		{id: 12, ts: oldMonth.Add(48 * time.Hour), sensor: "b"},
		{id: 13, ts: now.Add(-2 * time.Hour), sensor: "c"},
	}
	for _, r := range seeded {
		if err := db.Exec(fmt.Sprintf(
			"INSERT INTO %s (id, device_id, sensor_name, value, timestamp) VALUES (?, 1, ?, 1, ?)",
			probe), r.id, r.sensor, r.ts).Error; err != nil {
			t.Fatalf("seed probe row id=%d: %v", r.id, err)
		}
	}

	// 迁移前: 普通表 (relkind='r'), 尚未分区。
	if tableExistsInSchema(t, db, probe, "r") == 0 {
		t.Fatal("probe flat table must exist before migration")
	}
	if tableExistsInSchema(t, db, probe, "p") != 0 {
		t.Fatal("probe must not be partitioned before migration")
	}

	if err := MigrateTableToPartitioned(db, probe, probeLegacy); err != nil {
		t.Fatalf("MigrateTableToPartitioned: %v", err)
	}

	// 迁移后: 母表 relkind='p' (partitioned table)。
	if tableExistsInSchema(t, db, probe, "p") == 0 {
		t.Error("probe must be a partitioned parent (relkind='p') after migration")
	}
	if tableExistsInSchema(t, db, probe, "r") != 0 {
		t.Error("probe must no longer be a plain table after migration")
	}
	// 当月分区存在 (滚动窗口 -1..+3 覆盖)。
	if tableExistsInSchema(t, db, partitionName(probe, now), "r") == 0 {
		t.Errorf("current-month partition %s must exist", partitionName(probe, now))
	}
	// 历史月份分区存在 (否则 copy 阶段会报 no partition of relation)。
	if tableExistsInSchema(t, db, partitionName(probe, oldMonth), "r") == 0 {
		t.Errorf("history partition %s must exist", partitionName(probe, oldMonth))
	}
	// 行数守恒 (迁移门禁)。
	var count int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&count).Error; err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if count != int64(len(seeded)) {
		t.Errorf("rows after migration = %d, want %d", count, len(seeded))
	}
	// legacy 表保留 (不 DROP) 且数据完整。
	if tableExistsInSchema(t, db, probeLegacy, "r") == 0 {
		t.Error("legacy probe table must be retained after migration")
	}
	var legacyCount int64
	if err := db.Raw("SELECT count(*) FROM " + probeLegacy).Scan(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy rows: %v", err)
	}
	if legacyCount != int64(len(seeded)) {
		t.Errorf("legacy rows = %d, want %d", legacyCount, len(seeded))
	}

	// 幂等: 二次调用看到母表已分区, 直接跳过 (不报错, 不重复搬迁)。
	if err := MigrateTableToPartitioned(db, probe, probeLegacy); err != nil {
		t.Fatalf("second MigrateTableToPartitioned must be idempotent: %v", err)
	}
	var countAfter int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&countAfter).Error; err != nil {
		t.Fatalf("count rows after second migration: %v", err)
	}
	if countAfter != int64(len(seeded)) {
		t.Errorf("rows after second migration = %d, want %d (must not re-copy)", countAfter, len(seeded))
	}
}
