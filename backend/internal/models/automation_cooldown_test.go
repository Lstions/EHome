package models

// 负债 D-5 (cooldown_sec 零值语义矛盾) 的持久化层守护。
//
// 缺陷成因: CooldownSec 若带 gorm:"default:300", GORM 的 Create 会把显式零值
// 当"未设置"省略该列, 由 schema 默认值 300 回填 —— 同一类陷阱本仓已撞过两次
// (models/notification_channel.go 的 max_retries, models/alert.go 的 enabled),
// 见 docs/设计/外发通知通道.md §7.6。带 default 的 int 列无法表达"显式 0"。
//
// 契约: 显式 CooldownSec=0 必须原样读回 0 (0 = 不冷却, 与应用层默认 300 是
// 两个不同语义)。变异自证: 把 tag 改回 gorm:"default:300" 本测试必红。

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openAutomationRuleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&AutomationRule{}); err != nil {
		t.Fatalf("automigrate automation_rules: %v", err)
	}
	return db
}

func TestAutomationRuleCooldownSecZeroRoundTrips(t *testing.T) {
	db := openAutomationRuleDB(t)

	rule := AutomationRule{
		Name:        "零冷却",
		Enabled:     true,
		TriggerType: AutomationTriggerTimeWindow,
		ActionType:  AutomationActionNotification,
		ActionLevel: AlertLevelInfo,
		CooldownSec: 0, // 显式 0 = 不冷却, 必须原样落库
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}

	var got AutomationRule
	if err := db.First(&got, rule.ID).Error; err != nil {
		t.Fatalf("read back rule: %v", err)
	}
	if got.CooldownSec != 0 {
		t.Fatalf("cooldown_sec 显式 0 读回 %d, 期望 0 "+
			"(gorm default tag 把零值当未设置, DB 默认值回填 — 负债 D-5)", got.CooldownSec)
	}

	// 反向守护: 非零值同样原样往返 (排除"列被整个忽略"的误判)。
	rule2 := rule
	rule2.ID = 0
	rule2.Name = "有冷却"
	rule2.CooldownSec = 45
	if err := db.Create(&rule2).Error; err != nil {
		t.Fatalf("create rule2: %v", err)
	}
	var got2 AutomationRule
	if err := db.First(&got2, rule2.ID).Error; err != nil {
		t.Fatalf("read back rule2: %v", err)
	}
	if got2.CooldownSec != 45 {
		t.Fatalf("cooldown_sec=45 读回 %d, 期望 45", got2.CooldownSec)
	}
}
