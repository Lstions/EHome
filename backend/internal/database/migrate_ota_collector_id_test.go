package database

import (
	"testing"

	"ehome/backend/testutil"
)

// legacyOTATasksDDL 复刻 v2.3 之前 ota_tasks 的 schema：含 NOT NULL 的 collector_id
// 死列（改名后由 node_id 取代）。只用于测试，证明"旧库里残留的死列"确实会被
// MigrateOTATaskDropLegacyCollectorID 幂等清掉。
//
// ⚠️ 必须是**方言中立**的 DDL：本测试同时跑在 SQLite（默认）与 PostgreSQL
// （EHOME_TEST_DB=postgres，CI 的集成 job）上。
// 初版写了 SQLite 专有的 `INTEGER PRIMARY KEY AUTOINCREMENT`，在 PG 上直接
// 语法错误（SQLSTATE 42601）—— 而"只跑 SQLite"时全绿，正是典型的**只跑一半门禁**。
// 这里用最保守的 `id BIGINT`（不加自增/主键）：本测试只关心 collector_id 列的存在
// 与可删性，不插入指定 id，两方言都接受。
const legacyOTATasksDDL = "CREATE TABLE ota_tasks (" +
	"id BIGINT, " +
	"ota_id VARCHAR(64) NOT NULL, " +
	"collector_id VARCHAR(64) NOT NULL, " +
	"node_id VARCHAR(32) NOT NULL, " +
	"status VARCHAR(20) DEFAULT 'pending', " +
	"progress INTEGER DEFAULT 0)"

// TestMigrateOTATaskDropLegacyCollectorIDRemovesDeadColumn 是 2026-09-17 生产事故的
// 回归测试：老 PG 库上 ota_tasks.collector_id 仍是 NOT NULL，而代码只写 node_id
// ⇒ 建 OTA 任务必报 SQLSTATE 23502，OTA 功能整体 500。
//
// 本测试先造出"带死列的旧表"，再断言迁移把列删掉、且重复调用零副作用（幂等）。
func TestMigrateOTATaskDropLegacyCollectorIDRemovesDeadColumn(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 0. OpenTestDB 可能已按当前模型建过 ota_tasks（不含死列）—— 先删掉，
	// 才能造出"带 collector_id NOT NULL 的旧表"这一被测前置状态。
	if err := db.Migrator().DropTable("ota_tasks"); err != nil {
		t.Fatalf("清理已存在的 ota_tasks: %v", err)
	}

	// 1. 造出"存量旧表"（带 collector_id NOT NULL）。
	if err := db.Exec(legacyOTATasksDDL).Error; err != nil {
		t.Fatalf("创建模拟旧 ota_tasks 表: %v", err)
	}
	if !db.Migrator().HasColumn("ota_tasks", "collector_id") {
		t.Fatal("前置条件失败: 模拟旧表未带上 collector_id 列")
	}

	// 1.1 前置证伪：不删列时，只插 node_id 的 INSERT 必须失败 ——
	// 这正是生产里 500 的那条语句。
	err := db.Exec("INSERT INTO ota_tasks (ota_id, node_id, status, progress) VALUES ('ota-pre', 'NODE1', 'pending', 0)").Error
	if err == nil {
		t.Fatal("前置条件失败: 带 collector_id NOT NULL 的旧表本应拒绝缺少该列的 INSERT（说明模拟表没造对）")
	}

	// 2. 迁移：删除死列。
	dropped, err := MigrateOTATaskDropLegacyCollectorID(db)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if !dropped {
		t.Fatal("迁移应报告 dropped=true（列确实存在过）")
	}
	if db.Migrator().HasColumn("ota_tasks", "collector_id") {
		t.Fatal("迁移后 collector_id 仍然存在")
	}

	// 3. 关键效果层断言：删列后，建 OTA 任务的那条 INSERT 必须成功。
	if err := db.Exec("INSERT INTO ota_tasks (ota_id, node_id, status, progress) VALUES ('ota-post', 'NODE1', 'pending', 0)").Error; err != nil {
		t.Fatalf("删列后仍无法插入 OTA 任务（修复无效）: %v", err)
	}

	// 4. 幂等：重复调用不再改任何东西。
	droppedAgain, err := MigrateOTATaskDropLegacyCollectorID(db)
	if err != nil {
		t.Fatalf("重复调用报错: %v", err)
	}
	if droppedAgain {
		t.Fatal("重复调用不应再报告 dropped=true")
	}
}

// TestMigrateOTATaskDropLegacyCollectorIDNoTable 覆盖表不存在时不产生副作用。
func TestMigrateOTATaskDropLegacyCollectorIDNoTable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dropped, err := MigrateOTATaskDropLegacyCollectorID(db)
	if err != nil {
		t.Fatalf("表不存在时应静默返回: %v", err)
	}
	if dropped {
		t.Fatal("表不存在时不应报告 dropped=true")
	}
}
