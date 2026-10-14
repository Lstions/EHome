package database

// A-19 回填迁移幂等门禁 (方案 §5.2 / §8 A-19, 实施计划 §2 B2)。
//
// 反向验证: 去掉 backfillOneRuleAction 里的 NOT EXISTS 判据 →
// TestMigrateAutomationRuleActionsIsIdempotent 第二次运行会因唯一索引
// (rule_id, seq) 报错 (或行数翻倍) → 本门禁红。

import (
	"os"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// seedLegacyRule 造一条"子表上线前"的规则: 只有单动作镜像列, 无子行。
func seedLegacyRule(t *testing.T, db *gorm.DB, name, actionID string) models.AutomationRule {
	t.Helper()
	rule := models.AutomationRule{
		Name:               name,
		Enabled:            true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00",
		TriggerWindowEnd:   "18:00",
		TriggerWindowEdge:  models.AutomationWindowEnter,
		CooldownSec:        0,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     7,
		ActionID:           actionID,
		ActionParamsJSON:   `{"pin":5}`,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return rule
}

func countRuleActions(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.AutomationRuleAction{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// A-19 主判据: 连跑两次, 行数不变, 且内容与镜像列一致。
func TestMigrateAutomationRuleActionsIsIdempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	r1 := seedLegacyRule(t, db, "历史规则1", "set_mos_policy")
	r2 := seedLegacyRule(t, db, "历史规则2", "read_rainfall")

	first, err := MigrateAutomationRuleActions(db)
	if err != nil {
		t.Fatalf("首次回填失败: %v", err)
	}
	if first.Backfilled != 2 {
		t.Fatalf("首次回填数=%d want 2 (两条历史规则都该补齐 seq=1)", first.Backfilled)
	}
	if got := countRuleActions(t, db); got != 2 {
		t.Fatalf("首次回填后子表行数=%d want 2", got)
	}

	// 第二次运行: 幂等 —— 一条都不插, 行数不变。
	second, err := MigrateAutomationRuleActions(db)
	if err != nil {
		t.Fatalf("第二次回填失败 (幂等被破坏): %v", err)
	}
	if second.Backfilled != 0 {
		t.Fatalf("第二次回填数=%d want 0 (A-19: 连跑两次行数必须不变)", second.Backfilled)
	}
	if got := countRuleActions(t, db); got != 2 {
		t.Fatalf("第二次回填后子表行数=%d want 2 (行数不得翻倍)", got)
	}

	// 内容正确性: seq=1 且镜像列逐字段搬进子行。
	var act models.AutomationRuleAction
	if err := db.Where("rule_id = ?", r1.ID).First(&act).Error; err != nil {
		t.Fatal(err)
	}
	if act.Seq != 1 || act.ActionType != r1.ActionType || act.ActionID != r1.ActionID ||
		act.ActionDeviceID != r1.ActionDeviceID || act.ActionParamsJSON != r1.ActionParamsJSON {
		t.Fatalf("回填内容与镜像列不一致: %+v vs rule %+v", act, r1)
	}
	if !act.Enabled {
		t.Fatal("回填的子动作必须 enabled=true (否则规则静默失效)")
	}
	_ = r2
}

// 反向对照: 子表非空的规则绝不被回填覆盖 (只补空的)。
func TestMigrateAutomationRuleActionsDoesNotOverwriteExisting(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rule := seedLegacyRule(t, db, "已有多动作", "set_mos_policy")
	// 模拟"用户已改成两个动作": 子表已有 seq=1/2。
	for _, seq := range []int{1, 2} {
		if err := db.Create(&models.AutomationRuleAction{
			RuleID: rule.ID, Seq: seq, ActionType: models.AutomationActionDeviceAction,
			ActionDeviceID: 7, ActionID: "gpio_set", ActionParamsJSON: `{"pin":` + itoaSmall(seq) + `}`, Enabled: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	res, err := MigrateAutomationRuleActions(db)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backfilled != 0 {
		t.Fatalf("已有子行的规则被回填覆盖: backfilled=%d want 0", res.Backfilled)
	}
	if got := countRuleActions(t, db); got != 2 {
		t.Fatalf("子表行数=%d want 2 (回填不得覆盖/追加)", got)
	}
	// 原动作仍是用户设置的动作, 未被镜像列覆盖。
	var acts []models.AutomationRuleAction
	db.Where("rule_id = ?", rule.ID).Order("seq ASC").Find(&acts)
	if acts[0].ActionID != "gpio_set" || acts[1].ActionID != "gpio_set" {
		t.Fatalf("既有动作被改写: %+v", acts)
	}
}

// 反向验证的第二只眼 (真正的"会咬"判据): 把唯一索引摘掉再注入"没有 NOT EXISTS"。
//
// 为什么需要它: 唯一索引 (rule_id, seq) 本身就挡住了重复插入 —— 只靠"行数不变"
// 或"第二次不报错"判幂等时, 去掉 NOT EXISTS 判据依然绿 (索引把错误吞成了跳过),
// 门禁是假绿。本用例先把索引摘掉, 让重复插入成为**可以真的发生**的事, 再断言
// "行数仍为 1" —— 此时幂等只能由 SQL 里的 NOT EXISTS 判据自己保证。
//
// 注入验证: 去掉 NOT EXISTS 判据 → 本用例行数变 2 → 红。
//
// 摘索引只作用于本用例自己创建的临时索引名, 不改模型与生产 DDL; t.Cleanup 还原。
func TestBackfillIdempotencyJudgementActuallyBites(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedLegacyRule(t, db, "咬合规则", "set_mos_policy")

	// 摘掉 (rule_id, seq) 唯一索引: 这样"重复插入 seq=1"不再被 DB 拒绝,
	// 幂等必须由 SQL 里的 NOT EXISTS 判据自己保证。
	if err := db.Exec("DROP INDEX idx_automation_rule_actions_rule_seq").Error; err != nil {
		t.Fatalf("摘除唯一索引失败 (测试环境与预期 schema 不符): %v", err)
	}
	t.Cleanup(func() {
		db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_rule_actions_rule_seq ON automation_rule_actions(rule_id, seq)")
	})

	if _, err := MigrateAutomationRuleActions(db); err != nil {
		t.Fatalf("首次回填失败: %v", err)
	}
	if got := countRuleActions(t, db); got != 1 {
		t.Fatalf("首次回填后行数=%d want 1", got)
	}

	if _, err := MigrateAutomationRuleActions(db); err != nil {
		t.Fatalf("第二次回填失败: %v", err)
	}
	if got := countRuleActions(t, db); got != 1 {
		t.Fatalf("行数=%d want 1 —— 无唯一索引兜底时, 幂等必须由 NOT EXISTS 判据本身保证", got)
	}
}

// 边界: 空库 / 无规则时不得报错, 也不得产生行。
func TestMigrateAutomationRuleActionsOnEmptyDatabase(t *testing.T) {
	db := testutil.OpenTestDB(t)
	res, err := MigrateAutomationRuleActions(db)
	if err != nil {
		t.Fatalf("空库回填必须零副作用: %v", err)
	}
	if res.Scanned != 0 || res.Backfilled != 0 {
		t.Fatalf("空库结果=%+v want 全 0", res)
	}
	if got := countRuleActions(t, db); got != 0 {
		t.Fatalf("空库回填后行数=%d want 0", got)
	}
}

// 生产路径覆盖性: 回填必须在 database.AutoMigrate() 内被调用 ——
// 只在测试里调用等于生产库永不回填 (静默迁移缺失)。
func TestAutomationRuleActionsBackfillRunsOnProductionPath(t *testing.T) {
	raw, err := os.ReadFile("gorm.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func AutoMigrate() error {")
	if start < 0 {
		t.Fatal("gorm.go 找不到 func AutoMigrate")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "MigrateAutomationRuleActions(DB)") {
		t.Fatal("AutoMigrate 未调用 MigrateAutomationRuleActions —— 存量库的动作永远不会回填到子表")
	}
}

// itoaSmall 是测试内的最小整数格式化 (避免为一个数字引入 strconv)。
func itoaSmall(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
