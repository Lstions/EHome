package database

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// 清理前置条件迁移验证: 新增列/新表必须由生产 AutoMigrate 路径正确迁移。
// models.AutomationRule 与 models.CommandMetricsBaseline 都在 database/gorm.go 的
// AutoMigrate 列表内; 本测试用 testutil 的"同一份模型清单"验证迁移结果可查询。
func TestPreconditionSchemaMigrates(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if !db.Migrator().HasColumn(&models.AutomationRule{}, "last_triggered_at") {
		t.Fatal("automation_rules.last_triggered_at 未被迁移")
	}
	if !db.Migrator().HasTable(&models.CommandMetricsBaseline{}) {
		t.Fatal("command_metrics_baselines 表未被迁移")
	}
	var rule models.AutomationRule
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("新列可写性: %v", err)
	}
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("last_triggered_at", "2026-01-02 03:04:05").Error; err != nil {
		t.Fatalf("新列可更新性: %v", err)
	}
}
