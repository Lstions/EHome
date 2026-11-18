package database

import (
	"testing"

	"ehome/backend/testutil"
)

// legacyAutomationRulesDDL 复刻 2026-11-18 之前 automation_rules 的最小 schema:
// 含已废弃的 risk_acknowledged_at。方言中立 (SQLite / PostgreSQL 都接受
// BIGINT / VARCHAR / TIMESTAMP), 因为本测试同时跑在两种方言上。
// 只建迁移需要的列 —— 本测试只关心"该死列能否被幂等删掉且不丢业务行"。
const legacyAutomationRulesDDL = "CREATE TABLE automation_rules (" +
	"id BIGINT, " +
	"name VARCHAR(128), " +
	"risk_acknowledged_at TIMESTAMP NULL)"

// TestMigrateAutomationRuleDropRiskAcknowledged 是"规则级授权删除"的收尾门禁:
// 老库里残留的列必须被删掉, 且删除不能影响规则行本身。
func TestMigrateAutomationRuleDropRiskAcknowledged(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 0. OpenTestDB 可能已按当前模型建过 automation_rules（不含死列）—— 先删掉,
	// 才能造出"带 risk_acknowledged_at 的旧表"这一被测前置状态。
	if err := db.Migrator().DropTable("automation_rules"); err != nil {
		t.Fatalf("清理已存在的 automation_rules: %v", err)
	}
	if err := db.Exec(legacyAutomationRulesDDL).Error; err != nil {
		t.Fatalf("创建模拟旧 automation_rules 表: %v", err)
	}
	if !db.Migrator().HasColumn("automation_rules", "risk_acknowledged_at") {
		t.Fatal("前置条件失败: 模拟旧表未带上 risk_acknowledged_at 列")
	}
	if err := db.Exec("INSERT INTO automation_rules (id, name, risk_acknowledged_at) VALUES (1, '旧规则', '2026-11-18 00:00:00')").Error; err != nil {
		t.Fatalf("插入前置业务行: %v", err)
	}

	dropped, err := MigrateAutomationRuleDropRiskAcknowledged(db)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if !dropped {
		t.Fatal("迁移应报告 dropped=true（列确实存在过）")
	}
	if db.Migrator().HasColumn("automation_rules", "risk_acknowledged_at") {
		t.Fatal("迁移后 risk_acknowledged_at 仍然存在")
	}

	// 关键效果层断言: 删的是死列, 不是表 —— 业务行必须原样保留。
	var count int64
	if err := db.Table("automation_rules").Count(&count).Error; err != nil {
		t.Fatalf("统计规则行失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("删列后规则行数 = %d, want 1（不得丢业务数据）", count)
	}
	var name string
	if err := db.Table("automation_rules").Select("name").Where("id = ?", 1).Scan(&name).Error; err != nil {
		t.Fatalf("读取规则名失败: %v", err)
	}
	if name != "旧规则" {
		t.Fatalf("删列后规则名 = %q, want 旧规则", name)
	}

	// 幂等: 重复调用不再改任何东西。
	droppedAgain, err := MigrateAutomationRuleDropRiskAcknowledged(db)
	if err != nil {
		t.Fatalf("重复调用报错: %v", err)
	}
	if droppedAgain {
		t.Fatal("重复调用不应再报告 dropped=true")
	}
}

// TestMigrateAutomationRuleDropRiskAcknowledgedNoTable 覆盖表不存在时不产生副作用
// （全新库第一次启动时 AutoMigrate 已建表; 但迁移顺序被调整/单测直连时仍须安全）。
func TestMigrateAutomationRuleDropRiskAcknowledgedNoTable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Migrator().DropTable("automation_rules"); err != nil {
		t.Fatalf("清理 automation_rules: %v", err)
	}
	dropped, err := MigrateAutomationRuleDropRiskAcknowledged(db)
	if err != nil {
		t.Fatalf("表不存在时应静默返回: %v", err)
	}
	if dropped {
		t.Fatal("表不存在时不应报告 dropped=true")
	}
}
