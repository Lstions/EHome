package datalifecycle

import (
	"fmt"
	"strings"
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
	// legacy 旧表在校验通过后被 DROP (不再保留回滚副本)。
	if tableExistsInSchema(t, db, probeLegacy, "r") != 0 {
		t.Error("legacy probe table must be dropped after successful migration")
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

// TestMigrateTableToPartitioned_RowCountMismatchKeepsLegacy is the safety-gate
// regression for "DROP legacy after successful verification".
//
// A row with a NULL timestamp is invisible to copyMonthBatched (its WHERE
// timestamp >= ? / < ? window excludes NULL) while count(*) still counts it on
// the flat source, so srcCount != dstCount fires and the migration aborts before
// the RENAME swap. A pre-existing <table>_legacy table (as an interrupted prior
// attempt could leave behind) must survive untouched: no DROP may run on the
// failure path.
//
// Coverage note: this exercises the real row-count gate, i.e. the exact branch
// that guards the DROP. It cannot make the gate pass and then fail afterwards
// (that would require fault injection into the rename/setval step), so the
// "crossed the gate but DROP failed" case is not covered here.
func TestMigrateTableToPartitioned_RowCountMismatchKeepsLegacy(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	const (
		probe       = "zz_partition_gate_probe"
		probeLegacy = "zz_partition_gate_probe_legacy"
	)
	dropProbeTables(t, db, probe, probe+"_new", probeLegacy)
	t.Cleanup(func() { dropProbeTables(t, db, probe, probe+"_new", probeLegacy) })

	// 平表: timestamp 为可空非 PK 列, 才能注入一条 NULL 行制造行数不一致。
	ddl := fmt.Sprintf(
		"CREATE TABLE %s (id BIGSERIAL PRIMARY KEY, device_id BIGINT NOT NULL, sensor_name VARCHAR(32) NOT NULL, value DOUBLE PRECISION NOT NULL, unit VARCHAR(16), timestamp TIMESTAMPTZ, created_at TIMESTAMPTZ, edge_device_id BIGINT, logical_device_id BIGINT)",
		probe,
	)
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create probe flat table: %v", err)
	}
	now := time.Now()
	oldMonth := monthStart(now).AddDate(0, -1, 0)
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, device_id, sensor_name, value, timestamp) VALUES (1,1,'a',1,?), (2,1,'b',1,?), (3,1,'null_ts',1,NULL)",
		probe), oldMonth.Add(24*time.Hour), now.Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("seed probe rows: %v", err)
	}

	// 模拟历史遗留的 legacy 回滚副本 (含哨兵行)。
	if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY, marker TEXT)", probeLegacy)).Error; err != nil {
		t.Fatalf("create pre-existing legacy table: %v", err)
	}
	if err := db.Exec(fmt.Sprintf("INSERT INTO %s (id, marker) VALUES (999, 'sentinel')", probeLegacy)).Error; err != nil {
		t.Fatalf("seed legacy sentinel: %v", err)
	}

	err := MigrateTableToPartitioned(db, probe, probeLegacy)
	if err == nil {
		t.Fatal("migration must fail on row-count mismatch")
	}
	if !strings.Contains(err.Error(), "row count mismatch") {
		t.Fatalf("error = %v, want row count mismatch gate (safety gate did not fire)", err)
	}
	// 门禁失败: legacy 必须原样保留 (含哨兵行), 绝不允许被 DROP。
	if got := tableExistsInSchema(t, db, probeLegacy, "r"); got != 1 {
		t.Errorf("legacy table relkind='r' count = %d, want 1 (must not be dropped on failed verification)", got)
	}
	var markerCount int64
	if err := db.Raw("SELECT count(*) FROM " + probeLegacy + " WHERE id = 999 AND marker = 'sentinel'").Scan(&markerCount).Error; err != nil {
		t.Fatalf("read legacy sentinel: %v", err)
	}
	if markerCount != 1 {
		t.Errorf("legacy sentinel rows = %d, want 1 (legacy must be intact)", markerCount)
	}
	// 原平表也原样保留 (未 swap)。
	if got := tableExistsInSchema(t, db, probe, "r"); got != 1 {
		t.Errorf("flat probe table relkind='r' count = %d, want 1 (no swap on failure)", got)
	}
	var srcCount int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&srcCount).Error; err != nil {
		t.Fatalf("count flat rows: %v", err)
	}
	if srcCount != 3 {
		t.Errorf("flat rows after failed migration = %d, want 3 (data untouched)", srcCount)
	}
}

// TestMigrateTableToPartitioned_IdempotentDropsLeftoverLegacy pins the
// "already partitioned" early-return cleanup: a historical <table>_legacy
// leftover is DROPped on the next call (after the parent is confirmed
// partitioned), while the partitioned parent itself is left untouched.
func TestMigrateTableToPartitioned_IdempotentDropsLeftoverLegacy(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	const (
		probe       = "zz_partition_idem_probe"
		probeLegacy = "zz_partition_idem_probe_legacy"
	)
	dropProbeTables(t, db, probe, probe+"_new", probeLegacy)
	t.Cleanup(func() { dropProbeTables(t, db, probe, probe+"_new", probeLegacy) })

	ddl := fmt.Sprintf(
		"CREATE TABLE %s (id BIGSERIAL, device_id BIGINT NOT NULL, sensor_name VARCHAR(32) NOT NULL, value DOUBLE PRECISION NOT NULL, unit VARCHAR(16), timestamp TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ, edge_device_id BIGINT, logical_device_id BIGINT, PRIMARY KEY (id, timestamp))",
		probe,
	)
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create probe flat table: %v", err)
	}
	now := time.Now()
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, device_id, sensor_name, value, timestamp) VALUES (1,1,'a',1,?)",
		probe), now.Add(-time.Hour)).Error; err != nil {
		t.Fatalf("seed probe row: %v", err)
	}
	if err := MigrateTableToPartitioned(db, probe, probeLegacy); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if tableExistsInSchema(t, db, probeLegacy, "r") != 0 {
		t.Fatal("legacy must be dropped by the first successful migration")
	}

	// 伪造历史遗留: 母表已分区, 但 <table>_legacy 又被残留下来。
	if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY)", probeLegacy)).Error; err != nil {
		t.Fatalf("recreate leftover legacy: %v", err)
	}
	if err := db.Exec(fmt.Sprintf("INSERT INTO %s (id) VALUES (1)", probeLegacy)).Error; err != nil {
		t.Fatalf("seed leftover legacy: %v", err)
	}

	if err := MigrateTableToPartitioned(db, probe, probeLegacy); err != nil {
		t.Fatalf("idempotent migration must clean leftover legacy, got: %v", err)
	}
	if tableExistsInSchema(t, db, probeLegacy, "r") != 0 {
		t.Error("leftover legacy table must be dropped on the already-partitioned path")
	}
	if !IsTablePartitioned(db, probe) {
		t.Error("probe must remain a partitioned parent")
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&count).Error; err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if count != 1 {
		t.Errorf("rows after idempotent cleanup = %d, want 1 (must not re-copy)", count)
	}
}
