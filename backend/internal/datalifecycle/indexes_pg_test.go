//go:build integration

package datalifecycle

// PG 集成测试: Lead 补充契约要求 —— 用 testutil.OpenTestDB + EHOME_TEST_DB=postgres
// 在真实 PostgreSQL 上断言 EnsureLogicalDataIndexes 幂等 (连跑两次都成功且索引
// valid), 并且覆盖 BMS 页慢修复新增的两个复合索引 (契约 §1.4):
//   - 平表 (AutoMigrate 出来的普通表) 走 CONCURRENTLY 分支;
//   - 分区母表 (MigrateTableToPartitioned 之后) 走非并发 CREATE INDEX 分支,
//     索引挂在 relkind='p' 的母表上且 valid, 之后写入不受残留锁影响。
//
// 运行:
//
//	EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
//	EHOME_DB_USER=ehome EHOME_DB_PASSWORD=*** EHOME_DB_NAME=ehome_test \
//	go test -tags=integration -count=1 ./internal/datalifecycle/ -run Postgres -v
//
// 未设 EHOME_TEST_DB=postgres 时 requirePostgresIndexes(t) 显式 skip。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// requirePostgresIndexes skips unless the test DB backend is PostgreSQL.
func requirePostgresIndexes(t *testing.T) {
	t.Helper()
	if !testutil.IsPostgres() {
		t.Skip("requires PostgreSQL: EHOME_TEST_DB=postgres + -tags=integration (make test-integration)")
	}
}

// allLogicalIndexes is the full spec list owned by EnsureLogicalDataIndexes =
// the pre-existing logical-ts pair plus the two BMS page slow-fix indexes
// (契约 §1.4).
var allLogicalIndexes = []string{
	IndexUnifiedLogicalTS, IndexDevDataLogicalTS,
	IndexUnifiedLogicalSensorTS, IndexDevDataLogicalCreated,
}

// indexOwnerInSchema resolves which table (in the current test schema) owns
// index name; "" when the index does not exist.
func indexOwnerInSchema(t *testing.T, db *gorm.DB, index string) string {
	t.Helper()
	var owner string
	err := db.Raw(`SELECT t.relname FROM pg_class i
JOIN pg_index x ON x.indexrelid = i.oid
JOIN pg_class t ON t.oid = x.indrelid
JOIN pg_namespace n ON n.oid = i.relnamespace
WHERE i.relname = ? AND n.nspname = current_schema()`, index).Scan(&owner).Error
	if err != nil {
		t.Fatalf("resolve owner of index %s: %v", index, err)
	}
	return owner
}

// assertAllIndexesValid asserts every canonical index exists and is valid
// (IndexExists on PG also checks indisvalid) and is owned by wantOwner.
func assertAllIndexesValid(t *testing.T, db *gorm.DB, ctx context.Context, wantOwner map[string]string, run int) {
	t.Helper()
	for _, idx := range allLogicalIndexes {
		exists, err := IndexExists(ctx, db, idx)
		if err != nil {
			t.Fatalf("run %d: IndexExists(%s): %v", run, idx, err)
		}
		if !exists {
			t.Fatalf("run %d: index %s missing or INVALID (indisvalid=false)", run, idx)
		}
		if wantOwner != nil {
			if got := indexOwnerInSchema(t, db, idx); got != wantOwner[idx] {
				t.Errorf("run %d: index %s owned by %q, want %q", run, idx, got, wantOwner[idx])
			}
		}
	}
}

// TestEnsureLogicalDataIndexes_PostgresIdempotent — 平表路径 (CONCURRENTLY
// 分支): 连跑两次都成功, 四个索引 valid, 且归属正确。
func TestEnsureLogicalDataIndexes_PostgresIdempotent(t *testing.T) {
	requirePostgresIndexes(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres (SQLite 下本测试必须 skip 而不是静默替换方言)", db.Dialector.Name())
	}
	ctx := context.Background()
	owners := map[string]string{
		IndexUnifiedLogicalTS:       "unified_data",
		IndexUnifiedLogicalSensorTS: "unified_data",
		IndexDevDataLogicalTS:       "device_data",
		IndexDevDataLogicalCreated:  "device_data",
	}
	for run := 1; run <= 2; run++ {
		if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
			t.Fatalf("ensure run %d: %v", run, err)
		}
		assertAllIndexesValid(t, db, ctx, owners, run)
	}
	// 幂等: 已存在的索引不应被重建/失效; 再确认一次 valid。
	if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("ensure run 3: %v", err)
	}
	assertAllIndexesValid(t, db, ctx, owners, 3)
}

// TestEnsureLogicalDataIndexes_PostgresPartitionedParent — 分区母表路径
// (非并发 CREATE INDEX 分支): 迁移 unified_data/device_data 后连跑两次都成功,
// 四个索引都挂在分区母表上且 valid; 之后写入正常 (无残留锁), 新建分区继承父索引。
func TestEnsureLogicalDataIndexes_PostgresPartitionedParent(t *testing.T) {
	requirePostgresIndexes(t)
	db := testutil.OpenTestDB(t)
	ctx := context.Background()

	for _, table := range []string{"unified_data", "device_data"} {
		if err := MigrateTableToPartitioned(db, table, table+"_legacy"); err != nil {
			t.Fatalf("migrate %s to partitioned: %v", table, err)
		}
		if !IsTablePartitioned(db, table) {
			t.Fatalf("%s must be a partitioned parent after migration", table)
		}
	}

	owners := map[string]string{
		IndexUnifiedLogicalTS:       "unified_data",
		IndexUnifiedLogicalSensorTS: "unified_data",
		IndexDevDataLogicalTS:       "device_data",
		IndexDevDataLogicalCreated:  "device_data",
	}
	for run := 1; run <= 2; run++ {
		if err := EnsureLogicalDataIndexes(ctx, db); err != nil {
			t.Fatalf("ensure run %d on partitioned parents: %v", run, err)
		}
		assertAllIndexesValid(t, db, ctx, owners, run)
	}

	// 写入证明: 非并发建索引结束后无残留锁, 且当月分区接受写入。
	one := uint(1)
	now := time.Now()
	unified := models.UnifiedData{
		DeviceID: 1, LogicalDeviceID: &one, SensorName: "cell_voltage_1",
		Value: 3.2, Timestamp: now,
	}
	if err := db.Create(&unified).Error; err != nil {
		t.Fatalf("insert into partitioned unified_data after index creation: %v", err)
	}
	devData := models.DeviceData{
		DeviceID: 1, NodeID: "N-IDX", DataJSON: `{"v":1}`,
		Timestamp: now, LogicalDeviceID: &one,
	}
	if err := db.Create(&devData).Error; err != nil {
		t.Fatalf("insert into partitioned device_data after index creation: %v", err)
	}
	var unifiedCount, devDataCount int64
	if err := db.Model(&models.UnifiedData{}).Count(&unifiedCount).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	if err := db.Model(&models.DeviceData{}).Count(&devDataCount).Error; err != nil {
		t.Fatalf("count device_data: %v", err)
	}
	if unifiedCount != 1 || devDataCount != 1 {
		t.Errorf("partitioned writes after index creation: unified=%d device_data=%d, want 1/1",
			unifiedCount, devDataCount)
	}
}
