package automation

// F5 残留清单 Y1b/Y1c/Y2b/Y2c/Y3a/Y4a/Y4b/Y5b/Y5c 的回归锁
// （.analysis/narrow-cache.md §F5-verdict；Y1a/Y2a/Y5a 见 pg_y_cascade_test.go，
//  Y3b 见 alert 包，Y5d 由既有 cooldown_anchor_test.go 覆盖）。
//
// 这些状态是**有意不清**的：清它们会绕过冷却/配额/审计或制造重复动作。
// 因此判据不是"重置"，而是"行为被钉住、可观测、有文档"。

import (
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// ── Y1b 换设备不重置冷却（安全方向：宁可少执行，不可重复动作）──────────────

func TestY1bDeviceRebindKeepsCooldown(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.CooldownSec = 300
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	if len(h.events) != 1 {
		t.Fatalf("首次应触发，got %d", len(h.events))
	}
	// 换绑设备（PUT 等价：改列 + LoadRules）。
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", 1).
		Update("trigger_edge_device_id", 2).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(2, frame("temperature", 500), t0.Add(time.Second))
	if len(h.events) != 1 {
		t.Fatalf("Y1b: 换设备后仍在冷却窗内 ⇒ 必须 suppressed_cooldown（不得重置冷却），got %d 次触发", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", 1, models.AutomationResultSuppressedCooldown).Count(&n)
	if n == 0 {
		t.Fatal("Y1b: 必须落 suppressed_cooldown 审计（可观测）")
	}
}

// ── Y1c 重启后冷却锚恢复（rebuildCooldowns）────────────────────────────────

func TestY1cRestartRestoresCooldownFromAnchor(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.CooldownSec = 300
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	if len(h.events) != 1 {
		t.Fatalf("首次应触发，got %d", len(h.events))
	}
	// 真实触发路径由 Planner.recordExecuted 写锚点；本用例的 handler 是桩，
	// 故直接落锚点，模拟"触发后进程崩溃"的状态（这正是 rebuildCooldowns 的场景）。
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", 1).
		Update("last_triggered_at", t0).Error; err != nil {
		t.Fatal(err)
	}

	// "重启"：新 Evaluator（内存 triggered 清空，从 last_triggered_at 重建）。
	h2 := &captureHandler{}
	ev2 := NewEvaluator(db, h2)
	ev2.Evaluate(1, frame("temperature", 500), t0.Add(time.Second))
	if len(h2.events) != 0 {
		t.Fatalf("Y1c: 重启后冷却锚应恢复（冷却窗内不得重触发），got %d", len(h2.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", 1, models.AutomationResultSuppressedCooldown).Count(&n)
	if n == 0 {
		t.Fatal("Y1c: 重启后抑制必须落 suppressed_cooldown 审计")
	}
}

// ── Y2b 日切恢复（配额按日切重置，与内容变化无关）──────────────────────────

func TestY2bDailyQuotaResetsAtDayBoundary(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.CooldownSec = 0
	r.MaxDailyExec = 1
	r.ActionType = models.AutomationActionDeviceAction // 占位：本用例只看熔断计数口径
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	if id := p.recordExecuted(r, day1, 500, "cmd-1"); id == 0 {
		t.Fatal("recordExecuted 应成功")
	}
	// 同日再触发 ⇒ 熔断（通知动作不需要 cmdSvc，能走到 executed/notification 分支）。
	noteRule := r
	noteRule.ActionType = models.AutomationActionNotification
	noteRule.ActionLevel = models.AlertLevelInfo
	p.HandleTrigger(TriggerEvent{Rule: noteRule, At: day1.Add(time.Hour), Value: 500})
	var suppressed int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", 1, models.AutomationResultSuppressedDailyLimit).Count(&suppressed)
	if suppressed != 1 {
		t.Fatalf("Y2b: 同日应触发日熔断，got %d", suppressed)
	}

	// 次日：配额按日切恢复（不得被前一日的 executed 行压制）。
	day2 := day1.Add(24 * time.Hour)
	p.HandleTrigger(TriggerEvent{Rule: noteRule, At: day2, Value: 500})
	var suppressed2 int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ? AND triggered_at >= ?", 1,
			models.AutomationResultSuppressedDailyLimit, day2.Add(-time.Minute)).Count(&suppressed2)
	if suppressed2 != 0 {
		t.Fatalf("Y2b: 次日不得再被日熔断压制（配额按日切），got %d", suppressed2)
	}
}

// ── Y2c pending_confirm 不占日配额（F6 口径）───────────────────────────────

func TestY2cPendingConfirmDoesNotConsumeQuota(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.CooldownSec = 0
	r.MaxDailyExec = 1
	r.RequireConfirmed = true
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	p.HandleTrigger(TriggerEvent{Rule: r, At: at, Value: 500})
	p.HandleTrigger(TriggerEvent{Rule: r, At: at.Add(time.Second), Value: 500})

	var pending, suppressed int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", 1, models.AutomationResultPendingConfirm).Count(&pending)
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", 1, models.AutomationResultSuppressedDailyLimit).Count(&suppressed)
	if pending != 2 {
		t.Fatalf("Y2c: 两条 pending_confirm 都应落库（pending 不占配额），got %d", pending)
	}
	if suppressed != 0 {
		t.Fatalf("Y2c: pending_confirm 不得消耗 MaxDailyExec，got %d 条 suppressed_daily_limit", suppressed)
	}
}

// ── Y3a 已发送通知不可撤回（历史只增不改）──────────────────────────────────

func TestY3aHistoricalNotificationsImmutable(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.ActionType = models.AutomationActionNotification
	r.ActionLevel = models.AlertLevelWarning
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	p.HandleTrigger(TriggerEvent{Rule: r, At: at, Value: 500})

	var before models.Notification
	if err := db.Where("source = ? AND source_id = ?", "automation_rule", "1").First(&before).Error; err != nil {
		t.Fatalf("应先产生一条通知: %v", err)
	}

	// 改规则（触发语义 + 名称）并重载 —— 历史通知必须逐字段不变。
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"trigger_threshold": 100, "name": "改名后",
	}).Error; err != nil {
		t.Fatal(err)
	}
	var after models.Notification
	if err := db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Title != before.Title || after.Message != before.Message || after.Description != before.Description ||
		after.Source != before.Source || after.SourceID != before.SourceID || after.Read != before.Read ||
		!after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("Y3a: 历史通知必须逐字段不变\nbefore=%+v\nafter =%+v", before, after)
	}
	var count int64
	db.Model(&models.Notification{}).Where("source = ? AND source_id = ?", "automation_rule", "1").Count(&count)
	if count != 1 {
		t.Fatalf("Y3a: 通知行数不得减少/增加（改规则不产生新通知），got %d", count)
	}
}

// ── Y4a 在途 TriggerEvent 用触发时 rule 拷贝（不回溯改写）───────────────────

func TestY4aInFlightEventUsesTriggerTimeSnapshot(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	// DB 里的规则阈值 = 400（新内容）；事件拷贝里的阈值 = 600（触发时内容）。
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.ActionType = models.AutomationActionDeviceAction
	r.ActionDeviceID = 1
	r.ActionID = "set_mos_policy"
	r.ActionParamsJSON = "{}"
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.AutomationRuleAction{
		RuleID: 1, Seq: 1, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: "set_mos_policy", ActionParamsJSON: "{}", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 最新值 500：对新阈值 400 满足、对旧拷贝阈值 600 不满足。
	p.SetLatestValueBySensorFn(func(uint, string) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "temperature", Value: 500}, true
	})

	stale := r
	stale.TriggerThreshold = 600 // 触发时刻的内容
	p.HandleTrigger(TriggerEvent{Rule: stale, At: time.Now(), Value: 500})

	var ev models.AutomationEvent
	if err := db.Where("rule_id = ?", 1).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Result != models.AutomationResultConditionChanged {
		t.Fatalf("Y4a: 必须按触发时拷贝（阈值 600）复核 ⇒ condition_changed，got %s (detail=%s)", ev.Result, ev.Detail)
	}
	if !strings.Contains(ev.Detail, "600.00") {
		t.Fatalf("Y4a: 复核用的阈值必须是拷贝里的 600.00（不得读 DB 的 400），detail=%s", ev.Detail)
	}
	var cmds int64
	db.Model(&models.CommandExecution{}).Count(&cmds)
	if cmds != 0 {
		t.Fatalf("Y4a: 条件复核失败不得下发命令，got %d", cmds)
	}
}

// ── Y4b 规则在处理中被删除：按旧拷贝执行、不 panic ─────────────────────────

func TestY4bRuleDeletedMidFlightUsesSnapshot(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.ActionType = models.AutomationActionNotification
	r.ActionLevel = models.AlertLevelInfo
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	// 事件已进入 planner 后删除规则。
	if err := db.Delete(&models.AutomationRule{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: time.Now(), Value: 500}) // 不得 panic

	var ev models.AutomationEvent
	if err := db.Where("rule_id = ?", 1).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatalf("Y4b: 必须留下事件记录（不得静默）: %v", err)
	}
	if ev.Result != models.AutomationResultNotification {
		t.Fatalf("Y4b: 纯通知动作按旧拷贝执行 ⇒ notification，got %s (detail=%s)", ev.Result, ev.Detail)
	}
}

// ── Y5b 规则删除后确认 ⇒ ErrConfirmRuleMissing、零命令 ─────────────────────

func TestY5bConfirmRuleDeleted(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.RequireConfirmed = true
	r.ActionType = models.AutomationActionDeviceAction
	r.ActionDeviceID = 1
	r.ActionID = "set_mos_policy"
	r.ActionParamsJSON = "{}"
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.AutomationRuleAction{
		RuleID: 1, Seq: 1, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: "set_mos_policy", ActionParamsJSON: "{}", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: time.Now(), Value: 500})
	var pending models.AutomationEvent
	if err := db.Where("rule_id = ? AND result = ?", 1, models.AutomationResultPendingConfirm).
		First(&pending).Error; err != nil {
		t.Fatalf("应有 pending_confirm: %v", err)
	}
	if err := db.Delete(&models.AutomationRule{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := p.ConfirmEvent(nil, pending.ID, 1, "127.0.0.1"); err != ErrConfirmRuleMissing {
		t.Fatalf("Y5b: 规则已删除必须 ErrConfirmRuleMissing，got %v", err)
	}
	var cmds int64
	db.Model(&models.CommandExecution{}).Count(&cmds)
	if cmds != 0 {
		t.Fatalf("Y5b: 不得下发任何命令，got %d", cmds)
	}
	var after models.AutomationEvent
	if err := db.First(&after, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Result != models.AutomationResultPendingConfirm {
		t.Fatalf("Y5b: 失败的确认不得改写事件状态，got %s", after.Result)
	}
}

// ── Y5c 24h 超时清扫 ⇒ expired；confirm ⇒ ErrConfirmEventExpired ───────────

func TestY5cExpiredSweepThenConfirmRejected(t *testing.T) {
	db := newTestDB(t)
	p := NewPlanner(db, nil, nil, 0)
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.RequireConfirmed = true
	r.ActionType = models.AutomationActionDeviceAction
	r.ActionDeviceID = 1
	r.ActionID = "set_mos_policy"
	r.ActionParamsJSON = "{}"
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.AutomationRuleAction{
		RuleID: 1, Seq: 1, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: "set_mos_policy", ActionParamsJSON: "{}", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: r, At: time.Now().Add(-25 * time.Hour), Value: 500})
	var pending models.AutomationEvent
	if err := db.Where("rule_id = ? AND result = ?", 1, models.AutomationResultPendingConfirm).
		First(&pending).Error; err != nil {
		t.Fatalf("应有 pending_confirm: %v", err)
	}

	p.sweepExpiredPending()
	var after models.AutomationEvent
	if err := db.First(&after, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Result != models.AutomationResultExpired {
		t.Fatalf("Y5c: 25h 未确认应被清扫为 expired，got %s", after.Result)
	}
	if _, err := p.ConfirmEvent(nil, pending.ID, 1, "127.0.0.1"); err != ErrConfirmEventExpired {
		t.Fatalf("Y5c: 过期事件确认必须 ErrConfirmEventExpired，got %v", err)
	}
	var cmds int64
	db.Model(&models.CommandExecution{}).Count(&cmds)
	if cmds != 0 {
		t.Fatalf("Y5c: 过期确认不得下发命令，got %d", cmds)
	}
}
