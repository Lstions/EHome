package database

import (
	"testing"

	"ehome/backend/testutil"
)

// legacyOperationLogsDDL 复刻已退役表的建表语句 (字段同被删的 models.OperationLog)。
// 只用于测试: 证明"存量库里残留的旧表"确实会被 RetireLegacyOperationLogs 幂等清掉。
// 两种方言 (SQLite / PG) 都接受这份 DDL 子集。
const legacyOperationLogsDDL = `CREATE TABLE operation_logs (
	id BIGINT,
	user_id BIGINT,
	action VARCHAR(32),
	target VARCHAR(64),
	created_at TIMESTAMP
)`

// TestRetireLegacyOperationLogsDropsLegacyTable 覆盖退役的核心行为:
// 存量库里真的存在这张旧表时, 退役步骤把它删掉, 且**重复调用零副作用** (幂等)。
//
// 方言: testutil.OpenTestDB 在 EHOME_TEST_DB=sqlite/空 下走内存 SQLite,
// 在 EHOME_TEST_DB=postgres 下走 PG 的隔离 schema (test_<pid>_<nano>) ——
// 后者同时验证了生产 DDL 路径 (DROP TABLE IF EXISTS ... CASCADE) 在 PG 上成立。
func TestRetireLegacyOperationLogsDropsLegacyTable(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 1. 造出"存量旧表" (含一行数据, 确认 DROP 不因数据而失败)。
	if err := db.Exec(legacyOperationLogsDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表 operation_logs: %v", err)
	}
	if err := db.Exec("INSERT INTO operation_logs (id, user_id, action, target) VALUES (1, 1, 'login', 'system')").Error; err != nil {
		t.Fatalf("写入模拟遗留行: %v", err)
	}
	if !db.Migrator().HasTable("operation_logs") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// 2. 首次退役: 删除, 并返回删除前行数 (留痕值)。
	rows, err := RetireLegacyOperationLogs(db)
	if err != nil {
		t.Fatalf("首次退役: %v", err)
	}
	if rows != 1 {
		t.Errorf("退役返回的行数 = %d, want 1 (删除前该表有 1 行)", rows)
	}
	if db.Migrator().HasTable("operation_logs") {
		t.Fatal("退役后 operation_logs 仍然存在")
	}

	// 3. 二次退役: 幂等 —— 无表时零动作、零错误、行数 0。
	rows, err = RetireLegacyOperationLogs(db)
	if err != nil {
		t.Fatalf("二次退役 (幂等性): %v", err)
	}
	if rows != 0 {
		t.Errorf("二次退役返回的行数 = %d, want 0 (表已不存在)", rows)
	}
}

// TestAutoMigrateRetiresOperationLogsOnProductionPath 走**生产入口本身**
// (database.AutoMigrate()) 验证退役: 存量库残留的旧表在同一次启动内被清掉。
// 这是最贴近生产的一条证据 —— 调用链与 cmd/server/main.go 完全一致。
func TestAutoMigrateRetiresOperationLogsOnProductionPath(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 模拟"升级前就已存在该表"的存量库。
	if err := db.Exec(legacyOperationLogsDDL).Error; err != nil {
		t.Fatalf("创建模拟遗留表: %v", err)
	}
	if !db.Migrator().HasTable("operation_logs") {
		t.Fatal("前置条件失败: 模拟遗留表未被创建")
	}

	// AutoMigrate() 用包级 DB; 包内测试不并行 (无 t.Parallel), 用后恢复。
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })

	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径: %v", err)
	}
	if db.Migrator().HasTable("operation_logs") {
		t.Fatal("生产 AutoMigrate 路径结束后 operation_logs 仍然存在 —— 退役 DDL 未被调用")
	}
	// 再跑一次: 幂等 (表已不存在, 不得报错, 不得重建)。
	if err := AutoMigrate(); err != nil {
		t.Fatalf("生产 AutoMigrate 路径二次执行 (幂等性): %v", err)
	}
	if db.Migrator().HasTable("operation_logs") {
		t.Fatal("二次执行后 operation_logs 被重建")
	}
}

// TestAutoMigrateDoesNotCreateOperationLogs 断言: 走测试库的全量模型清单
// (testutil.allModels, 与生产 AutoMigrate 同源) 迁移后, 该表**不会被创建**。
//
// 这是"新表不再被创建"的库级证据 (源码级由 operation_logs_gate_test.go 覆盖);
// 在 EHOME_TEST_DB=postgres 下即 PG 生产路径的证据。
func TestAutoMigrateDoesNotCreateOperationLogs(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if db.Migrator().HasTable("operation_logs") {
		t.Fatal("AutoMigrate 迁移后仍创建了 operation_logs —— INV-8 违反")
	}
}
