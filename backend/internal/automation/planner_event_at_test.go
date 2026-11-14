package automation

// planner_event_at_test.go — automation_events.event_at 的落库判据（2026-11-13）。
//
// 需求：触发历史要能回答"这次触发对应哪一次日出/日落"。event_at 与 triggered_at
// 是两个不同口径：前者是 fireAt（日出/日落 + 偏移），后者是求值 tick 的 now。
// 本用例直接打 recordRet（事件落库的唯一收口），断言两条：
//   1. 带 EventAt 的时钟事件 ⇒ 列被写入且值一致；
//   2. 不带 EventAt 的普通事件 ⇒ 列保持 NULL（不得用 triggered_at 冒充）。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestPlannerPersistsEventAt(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rule := models.AutomationRule{Name: "sun", Enabled: true,
		TriggerType: models.AutomationTriggerSunEvent,
		TriggerSunKind: models.AutomationSunKindSunset}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	p := &Planner{db: db}

	now := time.Date(2026, 11, 13, 23, 8, 13, 0, time.UTC)
	fireAt := now.Add(-1500 * time.Millisecond) // 事件时刻略早于 tick

	id := p.recordRet(rule, now, 0, models.AutomationResultExecuted, "cmd-1", "", &TriggerEvent{EventAt: &fireAt})
	if id == 0 {
		t.Fatal("recordRet 未落库")
	}
	var ev models.AutomationEvent
	if err := db.First(&ev, id).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if ev.EventAt == nil {
		t.Fatal("时钟事件的 event_at 未落库 —— UI 的「事件时刻」列会永远是 -")
	}
	if !ev.EventAt.Equal(fireAt) {
		t.Fatalf("event_at = %v, want %v（必须是事件时刻，不是 triggered_at）", ev.EventAt, fireAt)
	}

	// 非时钟事件：event_at 必须保持 NULL，不得用 triggered_at 顶替。
	id2 := p.recordRet(rule, now, 12.5, models.AutomationResultExecuted, "cmd-2", "", nil)
	var ev2 models.AutomationEvent
	if err := db.First(&ev2, id2).Error; err != nil {
		t.Fatalf("load event 2: %v", err)
	}
	if ev2.EventAt != nil {
		t.Fatalf("非时钟事件的 event_at 应为 NULL，got %v", ev2.EventAt)
	}
}
