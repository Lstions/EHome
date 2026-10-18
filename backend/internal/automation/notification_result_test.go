package automation

import (
	"testing"

	"ehome/backend/internal/models"
)

// TestHandleTriggerNotificationOnlyRecordsNotificationResult is the regression
// test for the regression found on real hardware 2026-10-18.
//
// History that makes this test necessary: before the multi-action refactor,
// HandleTrigger had `case models.AutomationActionNotification: notify();
// record(AutomationResultNotification)`. The refactor replaced that switch with
// an action loop followed by an unconditional recordExecuted, so a
// notification-only rule started landing result="executed" with an empty
// command_id. On the CI simulation suite this made 33 of 140 end-to-end
// scenarios fail with a missing notification event.
//
// Reverse verification: delete the `if executed == 0 && notified > 0` branch in
// executeActions and this test goes red (result becomes "executed").
func TestHandleTriggerNotificationOnlyRecordsNotificationResult(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	rule := models.AutomationRule{
		Name:                "notification only rule",
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		ActionType:          models.AutomationActionNotification,
		ActionLevel:         models.AlertLevelInfo,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 42, At: p.nowFn()})

	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatalf("no event recorded: %v", err)
	}
	if ev.Result != models.AutomationResultNotification {
		t.Fatalf("result=%q want %q: a notification-only rule must not claim a device " +
			"action happened, and reporting executed also makes the rule consume " +
			"MaxDailyExec quota and write a cooldown anchor it never used to write",
			ev.Result, models.AutomationResultNotification)
	}
	if ev.CommandID != "" {
		t.Fatalf("command_id=%q want empty: no device command was dispatched", ev.CommandID)
	}

	// The cooldown anchor must stay untouched: old behaviour for notification-only
	// rules was "no anchor", and writing one silently cools the rule down.
	var after models.AutomationRule
	if err := p.db.First(&after, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastTriggeredAt != nil {
		t.Fatalf("last_triggered_at=%v want NULL: a notification-only rule must not " +
			"write the cooldown anchor (that anchor gates the next real trigger)",
			after.LastTriggeredAt)
	}
}
