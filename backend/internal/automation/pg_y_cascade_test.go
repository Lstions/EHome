package automation

// Y1a/Y2a/Y5a：需要在 **PostgreSQL** 上实跑的收口用例（Lead 2026-10-21 验收清单）。
//
// 为什么单列一个文件：这三条与"内容变更后的级联状态"直接相关，而 SQLite 与 PG 在
// 时间列排序、部分索引、并发条件 UPDATE 上行为不同（本仓既有教训：PG 专属用例必须
// 在 PG 上跑过才算验证）。文件本身**不跳过 SQLite**：EHOME_TEST_DB 未设时走内存
// SQLite，本地仍可快速回归；CI/本地设 EHOME_TEST_DB=postgres 时即真实 PG 语义。
//
// 运行：
//   EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
//   EHOME_DB_USER=... EHOME_DB_PASSWORD=... EHOME_DB_NAME=ehome_test \
//   go test ./internal/automation/ -run 'TestPGY' -count=1 -v

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// pgPlannerEnv 造一个真实 DB（PG 或 SQLite）+ Planner。
func pgPlannerEnv(t *testing.T) (*Planner, func(models.AutomationRule)) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	seed := func(r models.AutomationRule) {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed rule: %v", err)
		}
	}
	return p, seed
}

// Y1a 冷却锚不被内容变更清除（否则绕过 CooldownSec 重复下发真实动作）。
func TestPGY1aContentChangeKeepsCooldownAnchor(t *testing.T) {
	p, seed := pgPlannerEnv(t)
	rule := automationRule(9001, "temperature", "gt", 400, 0)
	rule.CooldownSec = 300
	rule.MaxDailyExec = 0
	seed(rule)

	// 先落一次 executed（等价于"规则刚刚完整触发过"，锚点在同事务写入）。
	at := time.Now().Add(-time.Minute)
	p.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).Update("last_triggered_at", at)
	if id := p.recordExecuted(rule, at, 500, "cmd-1", nil); id == 0 {
		t.Fatal("recordExecuted 应成功写事件 + 锚点")
	}

	// 改触发语义（阈值）——不得移除锚点。
	if err := p.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("trigger_threshold", 100).Error; err != nil {
		t.Fatal(err)
	}
	var reloaded models.AutomationRule
	if err := p.db.First(&reloaded, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.LastTriggeredAt == nil {
		t.Fatal("Y1a: 内容变更不得清空 last_triggered_at（冷却锚，清了会绕过 CooldownSec）")
	}

	// 手动触发路径读的正是该锚点：冷却窗内必须被抑制。
	base, ok := p.cooldownBase(context.Background(), reloaded)
	if !ok || base.IsZero() {
		t.Fatal("Y1a: cooldownBase 应读到锚点")
	}
	if d := at.Add(10 * time.Second).Sub(base); d >= 300*time.Second {
		t.Fatalf("Y1a: 锚点时间异常（距触发 %v）", d)
	}
}

// Y2a 日配额不因内容变更重置（MaxDailyExec 按 rule 计数 executed 行）。
func TestPGY2aContentChangeKeepsDailyQuota(t *testing.T) {
	p, seed := pgPlannerEnv(t)
	rule := automationRule(9002, "temperature", "gt", 400, 0)
	rule.CooldownSec = 0
	rule.MaxDailyExec = 1
	seed(rule)

	at := time.Now()
	if id := p.recordExecuted(rule, at, 500, "cmd-1", nil); id == 0 {
		t.Fatal("recordExecuted 应成功")
	}
	// 改阈值（内容变更）。
	if err := p.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("trigger_threshold", 100).Error; err != nil {
		t.Fatal(err)
	}

	// 用 HandleTrigger 走日熔断口径：当日已有 1 条 executed ⇒ 必须抑制。
	reloaded := rule
	reloaded.TriggerThreshold = 100
	p.HandleTrigger(TriggerEvent{Rule: reloaded, At: at.Add(time.Minute), Value: 500})

	var n int64
	p.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultSuppressedDailyLimit).Count(&n)
	if n != 1 {
		t.Fatalf("Y2a: 内容变更不得重置日配额，期望 1 条 suppressed_daily_limit，got %d", n)
	}
}

// Y5a pending_confirm 的动作快照与当前动作不一致 ⇒ 拒绝确认（ErrConfirmActionChanged）。
func TestPGY5aPendingActionHashRejectsChangedActions(t *testing.T) {
	p, seed := pgPlannerEnv(t)
	rule := automationRule(9003, "temperature", "gt", 400, 0)
	rule.RequireConfirmed = true
	rule.ActionType = models.AutomationActionDeviceAction
	rule.ActionDeviceID = 1
	rule.ActionID = "set_mos_policy"
	rule.ActionParamsJSON = "{}"
	seed(rule)
	if err := p.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("require_confirmed", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.db.Create(&models.AutomationRuleAction{
		RuleID: rule.ID, Seq: 1, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: "set_mos_policy", ActionParamsJSON: "{}", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 触发 ⇒ 落 pending_confirm（带动作集合哈希快照）。
	p.HandleTrigger(TriggerEvent{Rule: rule, At: time.Now(), Value: 500})
	var pending models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultPendingConfirm).
		First(&pending).Error; err != nil {
		t.Fatalf("应有 pending_confirm 事件: %v", err)
	}
	if pending.ActionHash == "" {
		t.Fatal("Y5a: pending_confirm 必须落 action_hash 快照")
	}

	// 用户在"待确认"期间把动作改掉（同 seq 换 action_id）。
	if err := p.db.Model(&models.AutomationRuleAction{}).Where("rule_id = ? AND seq = ?", rule.ID, 1).
		Update("action_id", "set_mos_policy_v2").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := p.ConfirmEvent(nil, pending.ID, 1, "127.0.0.1"); err != ErrConfirmActionChanged {
		t.Fatalf("Y5a: 动作已变更时必须拒绝确认（ErrConfirmActionChanged），got %v", err)
	}
	// 事件必须仍是 pending（拒绝不得把状态改成 executed/failed）。
	var after models.AutomationEvent
	if err := p.db.First(&after, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Result != models.AutomationResultPendingConfirm {
		t.Fatalf("Y5a: 拒绝确认后事件应保持 pending_confirm，got %s", after.Result)
	}
}
