package commandexec

// automation_backfill_test.go —— 命令终态回填自动化事件的回归锁（2026-11-20）。
//
// 缺陷形态（生产实测）：规则「关闭BMS充电」的命令实际 FAILED（ChannelCmdV2 final
// verification failed），automation_events.result 却永远停在 'executed'，
// 「自动化策略」页把"已提交"渲染成"已执行"，用户以为策略生效了。
//
// 本文件钉三件事：
//   ① SUCCEEDED **不得**被回填成失败结果（成功仍然是 executed，只补 detail）；
//   ② FAILED / UNKNOWN / CANCELLED 回填成 models.AutomationResultFailedExecution，
//      且 detail 里必须有 command_executions.final_reason 原文；
//   ③ 三条终态收口（设备终态 / Deadline 收尾 / 操作者取消）都回填，且都只在
//      命令确实关联过自动化事件时才动 —— 人工下发、告警下发的命令不受影响。

import (
	"context"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// seedAutomationEventForCommand 落一行"已提交"的自动化事件（= recordExecuted 的产物）。
func seedAutomationEventForCommand(t *testing.T, db *gorm.DB, commandID string) models.AutomationEvent {
	t.Helper()
	at := time.Now().UTC().Add(-time.Minute)
	ev := models.AutomationEvent{
		RuleID:      1,
		TriggeredAt: at,
		Result:      models.AutomationResultExecuted,
		CommandID:   commandID,
		CreatedAt:   at,
	}
	if err := db.Create(&ev).Error; err != nil {
		t.Fatalf("seed automation event: %v", err)
	}
	return ev
}

// collectAutomationUpdates 装上观察者并收集回调（生产接线 = Planner.OnCommandTerminal）。
func collectAutomationUpdates(s *Service) *[]AutomationEventUpdate {
	updates := &[]AutomationEventUpdate{}
	s.SetAutomationEventObserver(func(update AutomationEventUpdate) {
		*updates = append(*updates, update)
	})
	return updates
}

func reloadAutomationEvent(t *testing.T, db *gorm.DB, id uint) models.AutomationEvent {
	t.Helper()
	var ev models.AutomationEvent
	if err := db.First(&ev, id).Error; err != nil {
		t.Fatalf("reload automation event %d: %v", id, err)
	}
	return ev
}

// TestRecordInboxSucceededKeepsAutomationEventExecuted 是"成功不能被误回填"的守卫。
//
// 反向验证：把 automationTerminalOutcome 的 SUCCEEDED 分支改成返回失败码 ⇒ 本用例红
// （用户会看到"执行失败"，而设备其实确认成功了）。
func TestRecordInboxSucceededKeepsAutomationEventExecuted(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)
	updates := collectAutomationUpdates(s)

	updated, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "final_succeeded"))
	if err != nil || !applied {
		t.Fatalf("RecordInbox(final_succeeded) err=%v applied=%v", err, applied)
	}
	if updated.Status != StatusSucceeded {
		t.Fatalf("execution status=%q want %q", updated.Status, StatusSucceeded)
	}

	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	if got.Result != models.AutomationResultExecuted {
		t.Fatalf("automation result=%q want %q: 成功的命令不得被回填成失败（设备确实执行了）",
			got.Result, models.AutomationResultExecuted)
	}
	if !strings.Contains(got.Detail, "设备已确认执行成功") {
		t.Fatalf("detail=%q 必须写明设备已确认执行成功（否则 executed 仍无法与「已提交」区分）", got.Detail)
	}
	if len(*updates) != 1 {
		t.Fatalf("observer called %d times, want 1", len(*updates))
	}
	if (*updates)[0].Result != models.AutomationResultExecuted || (*updates)[0].Status != StatusSucceeded {
		t.Fatalf("observer update=%+v want result=%q status=%q",
			(*updates)[0], models.AutomationResultExecuted, StatusSucceeded)
	}
}

// TestRecordInboxFailedBackfillsAutomationEventAsFailed 是本次缺陷的靶点：
// 命令终态 FAILED ⇒ automation_events.result 必须离开 'executed'，且 detail 带
// command_executions.final_reason 原文（现场据此与执行记录对齐）。
//
// 反向验证：删掉 recordInbox 里的 backfillAutomationEventTx 调用 ⇒ 本用例红
// （result 仍是 executed，正是生产上"失败却显示已执行"的形态）。
func TestRecordInboxFailedBackfillsAutomationEventAsFailed(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)
	updates := collectAutomationUpdates(s)

	updated, applied, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "final_failed"))
	if err != nil || !applied {
		t.Fatalf("RecordInbox(final_failed) err=%v applied=%v", err, applied)
	}
	if updated.Status != StatusFailed {
		t.Fatalf("execution status=%q want %q", updated.Status, StatusFailed)
	}

	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	if got.Result != models.AutomationResultFailedExecution {
		t.Fatalf("automation result=%q want %q: 命令失败后事件必须变成可区分的失败结果"+
			"（生产缺陷正是这里停在 executed，页面显示「已执行」）",
			got.Result, models.AutomationResultFailedExecution)
	}
	if !strings.Contains(got.Detail, "final_reason=final_failed") {
		t.Fatalf("detail=%q 必须含 final_reason 原文(final_failed)，否则用户无法与执行记录对齐", got.Detail)
	}
	if !strings.Contains(got.Detail, "设备未确认执行成功") {
		t.Fatalf("detail=%q 必须有人类可读的失败说明", got.Detail)
	}
	if len(*updates) != 1 || (*updates)[0].Result != models.AutomationResultFailedExecution {
		t.Fatalf("observer updates=%+v want exactly one failed_execution update", *updates)
	}
}

// TestRecordInboxVerifierCauseLandsInAutomationDetail 钉死 2026-11-25 的第二个缺陷：
// 服务端把设备的成功回传改判为失败时，判定原因必须进审计 detail 与通知，
// 否则用户只看到"设备未确认执行成功"，无法判断是设备没动还是回读判据不认。
func TestRecordInboxVerifierCauseLandsInAutomationDetail(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)
	event := inboxEvent(exec.CommandID, "final_failed")
	event.FailureDetail = "jiabaida set_mos_policy readback mismatch: want charge_open=0; readback#1 fet_status=0x03 protection_status=0x1000"
	if _, applied, err := s.RecordInbox(context.Background(), event); err != nil || !applied {
		t.Fatalf("RecordInbox err=%v applied=%v", err, applied)
	}
	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	for _, want := range []string{"服务端回读校验未通过", "protection_status=0x1000", "final_reason=final_failed"} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("detail=%q 缺少 %q（verifier 原因必须落库，否则只剩日志）", got.Detail, want)
		}
	}
}

// TestRecordInboxUnknownBackfillsAutomationEventAsFailed：设备上报"未知"同样不是"已执行"。
func TestRecordInboxUnknownBackfillsAutomationEventAsFailed(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusVerifying)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)

	if _, _, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "unknown")); err != nil {
		t.Fatalf("RecordInbox(unknown): %v", err)
	}
	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	if got.Result != models.AutomationResultFailedExecution {
		t.Fatalf("automation result=%q want %q (UNKNOWN 时设备可能已被改动，不能显示「已执行」)",
			got.Result, models.AutomationResultFailedExecution)
	}
	if !strings.Contains(got.Detail, "命令终态 UNKNOWN") {
		t.Fatalf("detail=%q 必须标明命令终态是 UNKNOWN", got.Detail)
	}
}

// TestRecordInboxIgnoresCommandsWithoutAutomationEvent：人工/告警下发的命令不产生
// 自动化审计行，回填必须"匹配不到就安静"（不得报错、不得广播）。
func TestRecordInboxIgnoresCommandsWithoutAutomationEvent(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	updates := collectAutomationUpdates(s)

	if _, _, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "final_failed")); err != nil {
		t.Fatalf("RecordInbox(final_failed) 无关联事件时必须成功: %v", err)
	}
	if len(*updates) != 0 {
		t.Fatalf("observer should not fire for a command with no automation event, got %+v", *updates)
	}
}

// TestBackfillAutomationEventOnlyTouchesExecutedRows：已回填/其它结果码的行不得被覆盖。
//
// 反向验证：把 WHERE 里的 result = executed 去掉 ⇒ partial_failure 行会被改写成
// failed_execution（多动作部分失败的危险语义被抹掉）。
func TestBackfillAutomationEventOnlyTouchesExecutedRows(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	at := time.Now().UTC().Add(-time.Minute)
	keep := models.AutomationEvent{RuleID: 2, TriggeredAt: at, Result: models.AutomationResultPartialFailure,
		CommandID: exec.CommandID, Detail: "第 1/2 个动作失败", CreatedAt: at}
	if err := s.Database().Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordInbox(context.Background(), inboxEvent(exec.CommandID, "final_failed")); err != nil {
		t.Fatalf("RecordInbox: %v", err)
	}
	got := reloadAutomationEvent(t, s.Database(), keep.ID)
	if got.Result != models.AutomationResultPartialFailure {
		t.Fatalf("result=%q want %q: 回填只允许改仍处于 executed 的行",
			got.Result, models.AutomationResultPartialFailure)
	}
}

// TestRecoverExpiredBackfillsAutomationEvent：Deadline 收尾（QUEUED→FAILED）也必须回填。
//
// 现场形态：规则动作被 gate 拒绝/下发始终没成功 ⇒ 2 分钟后命令落 FAILED，
// 事件行却仍是"已执行"。
func TestRecoverExpiredBackfillsAutomationEvent(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusQueued)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)
	updates := collectAutomationUpdates(s)
	if err := s.Database().Model(&models.CommandExecution{}).
		Where("command_id = ?", exec.CommandID).
		Update("deadline_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverExpired(context.Background()); err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	if got.Result != models.AutomationResultFailedExecution {
		t.Fatalf("automation result=%q want %q (超期未下发的策略动作没有真的执行)",
			got.Result, models.AutomationResultFailedExecution)
	}
	if !strings.Contains(got.Detail, "deadline expired") {
		t.Fatalf("detail=%q 必须含 final_reason 原文(deadline expired ...)", got.Detail)
	}
	if len(*updates) != 1 {
		t.Fatalf("observer called %d times, want 1", len(*updates))
	}
}

// TestCancelBackfillsAutomationEvent：操作者取消（QUEUED→CANCELLED）同样回填。
func TestCancelBackfillsAutomationEvent(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusQueued)
	ev := seedAutomationEventForCommand(t, s.Database(), exec.CommandID)
	updates := collectAutomationUpdates(s)
	if _, err := s.Cancel(context.Background(), exec.CommandID, exec.ActorUserID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got := reloadAutomationEvent(t, s.Database(), ev.ID)
	if got.Result != models.AutomationResultFailedExecution {
		t.Fatalf("automation result=%q want %q (被取消的动作没有执行)",
			got.Result, models.AutomationResultFailedExecution)
	}
	if !strings.Contains(got.Detail, "cancelled by actor") {
		t.Fatalf("detail=%q 必须含 final_reason 原文(cancelled by actor)", got.Detail)
	}
	if len(*updates) != 1 {
		t.Fatalf("observer called %d times, want 1", len(*updates))
	}
}

// TestAutomationTerminalOutcomeMapping 钉住"终态→(结果码, detail)"的纯映射。
//
// 纯函数单测的价值：结果码与文案只允许有一处定义，调用点不得各自拼字符串。
func TestAutomationTerminalOutcomeMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		reason     string
		cause      string
		wantResult string
		wantParts  []string
	}{
		{name: "succeeded", status: StatusSucceeded, reason: "final_succeeded",
			wantResult: models.AutomationResultExecuted, wantParts: []string{"设备已确认执行成功"}},
		{name: "failed verification", status: StatusFailed, reason: "final_failed",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"设备未确认执行成功", "命令终态 FAILED", "final_reason=final_failed"}},
		{name: "unknown", status: StatusUnknown, reason: "deadline expired without final evidence",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"执行结果未知", "deadline expired without final evidence"}},
		{name: "cancelled", status: StatusCancelled, reason: "cancelled by actor",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"已被取消", "cancelled by actor"}},
		{name: "dispatch rejected", status: StatusFailed, reason: "dispatch rejected: deadline exceeded",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"下发被拒绝", "deadline exceeded"}},
		{name: "empty reason", status: StatusFailed, reason: "",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"没有 final_reason", "final_reason=(空)"}},
		{name: "verifier cause", status: StatusFailed, reason: "final_failed",
			cause:      "jiabaida set_mos_policy readback mismatch: readback#1 protection_status=0x1000",
			wantResult: models.AutomationResultFailedExecution,
			wantParts:  []string{"服务端回读校验未通过", "readback mismatch", "final_reason=final_failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, detail := automationTerminalOutcome(tc.status, tc.reason, tc.cause)
			if result != tc.wantResult {
				t.Fatalf("result=%q want %q", result, tc.wantResult)
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(detail, part) {
					t.Fatalf("detail=%q 缺少片段 %q", detail, part)
				}
			}
			if n := len([]rune(detail)); n > automationEventDetailColumnRunes {
				t.Fatalf("detail 长度 %d 超出列预算 %d", n, automationEventDetailColumnRunes)
			}
		})
	}
}

// TestAutomationTerminalOutcomeClampsLongReason：超长原因必须被截断到列预算内
// （否则 MySQL 静默截断 / PostgreSQL 直接报错，两种方言表现不同）。
func TestAutomationTerminalOutcomeClampsLongReason(t *testing.T) {
	_, detail := automationTerminalOutcome(StatusFailed, strings.Repeat("超长原因", 400), "")
	if n := len([]rune(detail)); n > automationEventDetailColumnRunes {
		t.Fatalf("detail runes=%d must be <= %d", n, automationEventDetailColumnRunes)
	}
	if !strings.HasPrefix(detail, "设备未确认执行成功") {
		t.Fatalf("截断必须从尾部发生（前缀保留），实际 detail=%q", detail)
	}
}
