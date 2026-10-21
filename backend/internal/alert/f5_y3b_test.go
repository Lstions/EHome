package alert

// Y3b（F5 清单）：firing 中的告警规则被修改内容后，历史不得被"改写谎报"。
//
// 模型约束（重要，与 F5 建议文本的差异）：models.AlertEvent **没有 detail 列**，
// 且既有语义是**单行事件**（onResolve 就地 update 同一行：state→resolved +
// resolved_at + value，见 evaluator.go onResolve）。因此本实现采用"就地关闭陈旧
// firing"，而不是"新增一条 resolved 行（detail=rule_updated）"——新增行会与
// 「一行=一次告警生命周期」的既有契约冲突，且无法写入 detail。
//
// 本用例钉住三件事：
//   1. 陈旧 firing 被置 resolved + resolved_at（不永久挂在 firing）；
//   2. 历史 notifications 行字段不变（历史只增不改）；
//   3. 若新条件下仍满足，会重新 firing（可见、可接受，F3 裁决）。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

func TestY3bContentChangeClosesStaleFiringKeepsHistory(t *testing.T) {
	r := rule(1, "temperature", "gt", 40, 0)
	ev, db := newAlertEval(t, r, nil)

	at := time.Now()
	ev.Evaluate(1, fields("temperature", 50), at)

	var firing models.AlertEvent
	if err := db.Where("rule_id = ? AND state = ?", 1, stateFiring).First(&firing).Error; err != nil {
		t.Fatalf("应先产生 firing 事件: %v", err)
	}
	var notifBefore models.Notification
	if err := db.Where("source = ? AND source_id = ?", "alert_rule", "1").First(&notifBefore).Error; err != nil {
		t.Fatalf("应先产生告警通知: %v", err)
	}

	// 改阈值（新条件下 50>100 不满足 ⇒ 若不清会永久 firing）。
	if err := db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("threshold", 100).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	var after models.AlertEvent
	if err := db.First(&after, firing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != stateResolved {
		t.Fatalf("Y3b: 内容变更后陈旧 firing 必须置 resolved，got %s", after.State)
	}
	if after.ResolvedAt == nil {
		t.Fatal("Y3b: resolved_at 必须被写入（否则前端无法判断何时结束）")
	}
	if !after.FiredAt.Equal(*firing.FiredAt) {
		t.Fatalf("Y3b: 历史 fired_at 不得被改写（before=%v after=%v）", firing.FiredAt, after.FiredAt)
	}

	// 历史通知逐字段不变 + 行数不减少。
	var notifAfter models.Notification
	if err := db.First(&notifAfter, notifBefore.ID).Error; err != nil {
		t.Fatalf("Y3b: 历史通知不得被删除: %v", err)
	}
	if notifAfter.Title != notifBefore.Title || notifAfter.Message != notifBefore.Message ||
		notifAfter.Source != notifBefore.Source || notifAfter.SourceID != notifBefore.SourceID ||
		notifAfter.Read != notifBefore.Read || !notifAfter.CreatedAt.Equal(notifBefore.CreatedAt) {
		t.Fatalf("Y3b: 历史通知必须逐字段不变\nbefore=%+v\nafter =%+v", notifBefore, notifAfter)
	}

	// 条件仍满足（把阈值改回 40）⇒ 允许重新 firing（F3 已接受的代价，必须可见）。
	if err := db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("threshold", 40).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, fields("temperature", 50), at.Add(time.Second))
	var firingCount int64
	db.Model(&models.AlertEvent{}).Where("rule_id = ? AND state = ?", 1, stateFiring).Count(&firingCount)
	if firingCount == 0 {
		t.Fatal("Y3b: 条件仍满足时重新 firing 必须是可见的新状态（不得被静默吞掉）")
	}
}
