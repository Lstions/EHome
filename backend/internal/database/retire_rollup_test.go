package database

import (
	"testing"

	"ehome/backend/testutil"
)

// legacyRollupDDL 复刻已退役表的建表语句 (字段同被删的 models.UnifiedDataRollup1m)。
// 只用于测试: 证明"存量库里残留的旧表"确实会被 RetireLegacyRollup1m 幂等清掉。
// 两种方言 (SQLite / PG) 都接受这份 DDL 子集。
const legacyRollupDDL = `CREATE TABLE unified_data_rollup_1m (
	device_id   BIGINT NOT NULL,
	sensor_name VARCHAR(32) NOT NULL,
	bucket      TIMESTAMP NOT NULL,
	min_v       DOUBLE PRECISION,
	max_v       DOUBLE PRECISION,
	avg_v       DOUBLE PRECISION,
	last_v      DOUBLE PRECISION,
	last_id     BIGINT,
	cnt         INTEGER,
	PRIMARY KEY (device_id, sensor_name, bucket)
)`

// TestRetireLegacyRollup1mDropsLegacyTable 覆盖退役的核心行为:
// 存量库里真的存在这张旧表时, 退役步骤把它删掉, 且**重复调用零副作用** (幂等)。
//
// 方言: testutil.OpenTestDB 在 EHOME_TEST_DB=sqlite/空 下走内存 SQLite,
// 在 EHOME_TEST_DB=postgres 下走 PG 的隔离 schema (test_<pid>_<nano>)。
func TestRetireLegacyRollup1mDropsLegacyTable(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 1. 造出"存量旧表" (含一行数据, 确认 DROP 不因数据而失败)。
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表 unified_data_rollup_1m: %v", err)
	}
	if err := db.Exec("INSERT INTO unified_data_rollup_1m (device_id, sensor_name, bucket, min_v, max_v, avg_v, last_v, last_id, cnt) VALUES (7, 'temp', ?, 10, 20, 15, 20, 2, 2)",
		"2026-08-21 10:00:00").Error; err != nil {
		t.Fatalf("写入模拟遗留行: %v", err)
	}
	if !db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// 2. 首次退役: 删除, 并返回删除前行数 (留痕值)。
	rows, err := RetireLegacyRollup1m(db)
	if err != nil {
		t.Fatalf("首次退役: %v", err)
	}
	if rows != 1 {
		t.Errorf("退役返回的行数 = %d, want 1 (删除前该表有 1 行)", rows)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("退役后 unified_data_rollup_1m 仍然存在")
	}

	// 3. 二次退役: 幂等 —— 无表时零动作、零错误、行数 0。
	rows, err = RetireLegacyRollup1m(db)
	if err != nil {
		t.Fatalf("二次退役 (幂等性): %v", err)
	}
	if rows != 0 {
		t.Errorf("二次退役返回的行数 = %d, want 0 (表已不存在)", rows)
	}
}

// TestAutoMigrateRetiresRollupOnProductionPath 走**生产入口本身**
// (database.AutoMigrate()) 验证退役: 存量库残留的旧表在同一次启动内被清掉,
// 且随后的启动**不再创建**它 (EnsureRollupTable 已随退役删除, main.go 调用点已摘除)。
// 这是最贴近生产的一条证据 —— 调用链与 cmd/server/main.go 完全一致。
func TestAutoMigrateRetiresRollupOnProductionPath(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 模拟"升级前就已存在该表"的存量库。
	if err := db.Exec(legacyRollupDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表: %v", err)
	}
	if !db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// AutoMigrate() 用包级 DB; 包内测试不并行 (无 t.Parallel), 用后恢复。
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })

	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径: %v", err)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("生产 AutoMigrate 路径结束后 unified_data_rollup_1m 仍然存在 —— 退役 DDL 未被调用")
	}
	// 再跑一次: 幂等 (表已不存在, 不得报错, 不得重建)。这一条同时覆盖
	// "启动后不再创建 rollup 表" (原 EnsureRollupTable 的建表路径已删)。
	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径二次执行 (幂等性): %v", err)
	}
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("二次执行后 unified_data_rollup_1m 被重建")
	}
}

// TestAutoMigrateDoesNotCreateRollup 断言: 走测试库的全量模型清单
// (testutil.allModels, 与生产 AutoMigrate 同源) 迁移后, 该表**不会被创建**。
//
// 这是"新表不再被创建"的库级证据 (源码级由
// datalifecycle/rollup_retirement_gate_test.go 覆盖); 在 EHOME_TEST_DB=postgres
// 下即 PG 生产路径的证据。
func TestAutoMigrateDoesNotCreateRollup(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if db.Migrator().HasTable("unified_data_rollup_1m") {
		t.Fatal("AutoMigrate 迁移后仍创建了 unified_data_rollup_1m —— 死表复活")
	}
}
