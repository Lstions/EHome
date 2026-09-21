package commandexec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ehome/backend/internal/models"
)

// ==================== M-3 (2026-09-21): 拒绝原因下沉到 attempt 级 ====================
//
// 背景: R4 让"派发被拒原因"只存在于 command_executions.final_reason, 而该列在
// 同一行的生命周期里同时承载"当代 attempt 的拒绝原因"与"终态原因"。成功路径必须
// 清空它以免疫串味 —— 一旦引入第 2 次 attempt, 那次清空就会把首次 attempt 的原因
// 一起抹掉。这些测试钉死下沉后的三条性质:
//   A. 被拒的 attempt 行自己留下原因 (attempt 级不丢);
//   B. 组装终态原因时 attempt 级优先, 执行级为其兜底, 两者都空则逐字节沿用旧文案;
//   C. 任何组合路径的最终字符串都 ≤ final_reason 列宽 (256 runes) 且多字节安全。
//
// 反例 (修复前必红的一条): A 里执行级副本被清空 (成功投递的正常后果) 而 attempt 级
// 仍在 —— 旧实现只剩通用文案, 原因被"清空"抹掉。见
// TestDeadlineKeepsAttemptLevelCauseAfterExecutionCopyCleared。

// finalReasonColumnChars 是 command_executions.final_reason (也是
// command_attempts.final_reason) 的列宽口径: size:256 字符。
//
// 它是**独立于生产常量**的裁判: 生产里的 FinalReasonColumnRunes 只是"代码认为的
// 预算", 这里写死的是"存储真正允许的宽度"。把生产常量调大 (clamp 上限放宽) 时,
// 下面的长度断言必须因此变红 —— 两边都读同一个常量的话, 这种变异会被静默放过。
const finalReasonColumnChars = 256

// seedQueuedWithAttemptCause 造出"某次 attempt 被拒并已记下原因, 随后执行级副本
// 被成功路径清空, 且尚未到 deadline"这一刻的 DB 状态 —— 也就是旧实现下原因已经
// 丢失的那个状态。
func seedQueuedWithAttemptCause(t *testing.T, attemptNo uint32, attemptCause string) (*Service, *models.CommandExecution) {
	t.Helper()
	s, exec, _ := setupInboxService(t, StatusQueued)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).
		Update("final_reason", "").Error; err != nil {
		t.Fatal(err)
	}
	if attemptNo == 1 {
		if err := s.db.Model(&models.CommandAttempt{}).Where("command_id = ? AND attempt_no = ?", exec.CommandID, attemptNo).
			Update("final_reason", attemptCause).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		attempt := models.CommandAttempt{
			CommandID: exec.CommandID, AttemptNo: attemptNo, Status: StatusDispatched,
			EnvelopeID: fmt.Sprintf("%s:%d", exec.CommandID, attemptNo), WireDigest: "digest-attempt",
			FencingToken: uint64(attemptNo), CreatedAt: time.Now().UTC(), FinalReason: attemptCause,
		}
		if err := s.db.Create(&attempt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&models.CommandOutbox{CommandID: exec.CommandID, EventType: "dispatch", PayloadJSON: "{}", State: "PENDING", CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	return s, exec
}

// A. 被拒的 attempt 行必须自己携带原因。走的是生产函数 recordDispatchRejection
// (ProcessOnce 在派发失败时调用的就是它), 而不是测试自己拼 SQL。
func TestDispatchRejectionIsRecordedOnTheRejectedAttempt(t *testing.T) {
	ctx := context.Background()
	d, exec, _ := setupDispatcher(t, &dispatchFakeTransport{err: fmt.Errorf("ignored")})
	attempt := models.CommandAttempt{
		CommandID: exec.CommandID, AttemptNo: 1, Status: StatusDispatched,
		EnvelopeID: exec.CommandID + ":1", WireDigest: "digest-attempt-1",
		BootID: "boot-1", FencingToken: 3, CreatedAt: time.Now().UTC(),
	}
	if err := d.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}

	d.recordDispatchRejection(ctx, exec.CommandID, fmt.Errorf("hardware_id %q must be an address from 1 to 254", "UART1"))

	var stored models.CommandAttempt
	if err := d.db.First(&stored, "command_id = ? AND attempt_no = ?", exec.CommandID, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.FinalReason, "dispatch rejected: ") {
		t.Fatalf("attempt final_reason=%q, want the 'dispatch rejected: ...' summary on the rejected attempt", stored.FinalReason)
	}
	if !strings.Contains(stored.FinalReason, "UART1") {
		t.Fatalf("attempt final_reason=%q must carry the underlying cause", stored.FinalReason)
	}
	// 生命周期列不得被这次"记账式"写入改动。
	if stored.Status != StatusDispatched || stored.BootID != "boot-1" || stored.WireDigest != "digest-attempt-1" {
		t.Fatalf("rejection write mutated attempt lifecycle columns: %+v", stored)
	}
	// 执行级副本仍然保留 (R4 的可观测性要求: 命令还在 QUEUED 时 UI 看的就是它)。
	var execution models.CommandExecution
	if err := d.db.First(&execution, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	if execution.Status != StatusQueued {
		t.Fatalf("execution status=%s, want QUEUED", execution.Status)
	}
	if execution.FinalReason != stored.FinalReason {
		t.Fatalf("execution reason=%q differs from attempt reason=%q", execution.FinalReason, stored.FinalReason)
	}
}

// 今日单 attempt 流程里, 被拒的 attempt 行随 d.dispatch 的事务一起回滚, 因此这里
// 没有行可写 —— 不得为了"写进 attempt"而伪造一行: command_attempts 的
// (command_id, attempt_no)/envelope_id 唯一键会被重试自己的 Create 撞上,
// 且它与 PROCESSED outbox 的一对一关系是保留期不变式 (INV-9) 的前提。
func TestRejectedDispatchDoesNotFabricateAttemptRow(t *testing.T) {
	ft := &dispatchFakeTransport{err: fmt.Errorf("device channel is not reported")}
	d, exec, _ := setupDispatcher(t, ft)

	if _, err := d.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected the transport error to propagate")
	}

	var attempts int64
	if err := d.db.Model(&models.CommandAttempt{}).Where("command_id = ?", exec.CommandID).Count(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("attempt rows=%d, want 0: a dispatch that never reached the wire must not leave attempt evidence behind", attempts)
	}
	var execution models.CommandExecution
	if err := d.db.First(&execution, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	if execution.Status != StatusQueued {
		t.Fatalf("execution status=%s, want QUEUED (deadline semantics unchanged)", execution.Status)
	}
	if !strings.HasPrefix(execution.FinalReason, "dispatch rejected: ") {
		t.Fatalf("execution final_reason=%q must still carry the reason while QUEUED (R4)", execution.FinalReason)
	}
}

// 核心回归: attempt 级原因在"执行级副本被成功路径清空"之后仍然存活, 并进入终态文案。
// 修复前 (原因只存在于 execution.final_reason) 这条必红: 终态只剩通用文案。
func TestDeadlineKeepsAttemptLevelCauseAfterExecutionCopyCleared(t *testing.T) {
	const cause = "dispatch rejected: hardware_id \"UART1\" must be an address from 1 to 254"
	s, exec := seedQueuedWithAttemptCause(t, 1, cause)

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}

	var stored models.CommandExecution
	if err := s.db.First(&stored, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusFailed || stored.CompletedAt == nil || expired[0].CompletedAt == nil {
		t.Fatalf("state machine changed: status=%s completed=%v/%v", stored.Status, stored.CompletedAt, expired[0].CompletedAt)
	}
	if !strings.Contains(stored.FinalReason, "must be an address from 1 to 254") {
		t.Fatalf("stored terminal reason=%q lost the attempt-level cause after the execution copy was cleared", stored.FinalReason)
	}
	if !strings.Contains(stored.FinalReason, "deadline expired before dispatch") {
		t.Fatalf("stored terminal reason=%q must still say the deadline expired", stored.FinalReason)
	}
	if stored.FinalReason == "deadline expired before dispatch" {
		t.Fatal("stored terminal reason is the generic text alone: the attempt-level cause was lost")
	}
	// 组装根广播用的返回值必须与库里逐字节一致 (不能出现"DB 已写但返回值没带")。
	if expired[0].FinalReason != stored.FinalReason {
		t.Fatalf("returned reason=%q differs from stored reason=%q", expired[0].FinalReason, stored.FinalReason)
	}
	// 被拒的 attempt 行本身也不得被终态结算改写。
	var attempt models.CommandAttempt
	if err := s.db.First(&attempt, "command_id = ? AND attempt_no = ?", exec.CommandID, 1).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.FinalReason != cause {
		t.Fatalf("attempt final_reason=%q, want the cause preserved verbatim", attempt.FinalReason)
	}
}

// B. attempt 级优先于执行级: 两份副本不同时, 采用更具体的那一份。
func TestAttemptLevelCauseOutranksExecutionCause(t *testing.T) {
	const (
		attemptCause   = "dispatch rejected: attempt-scoped cause (boot id mismatch)"
		executionCause = "dispatch rejected: execution-scoped cause (stale summary)"
	)
	s, exec := seedQueuedWithAttemptCause(t, 1, attemptCause)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).
		Update("final_reason", executionCause).Error; err != nil {
		t.Fatal(err)
	}

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	got := expired[0].FinalReason
	if !strings.HasPrefix(got, attemptCause) {
		t.Fatalf("composed reason=%q, want it to start with the attempt-level cause %q", got, attemptCause)
	}
	if strings.Contains(got, executionCause) {
		t.Fatalf("composed reason=%q leaked the execution-level copy %q", got, executionCause)
	}
}

// B2. 多次 attempt 并存时取"最早记录了原因的那一次": 它才是"为什么这次命令没出去"
// 的答案; attempt_no 顺序不得反过来。
func TestFirstAttemptCauseWinsOverLaterAttempts(t *testing.T) {
	const (
		firstCause  = "dispatch rejected: first attempt was refused (address 0 is illegal)"
		laterCause  = "dispatch rejected: later attempt was refused (channel busy)"
		genericText = "deadline expired before dispatch"
	)
	s, exec := seedQueuedWithAttemptCause(t, 3, laterCause)
	if err := s.db.Model(&models.CommandAttempt{}).Where("command_id = ? AND attempt_no = ?", exec.CommandID, 1).
		Update("final_reason", firstCause).Error; err != nil {
		t.Fatal(err)
	}

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	got := expired[0].FinalReason
	if !strings.HasPrefix(got, firstCause) || !strings.HasSuffix(got, genericText) {
		t.Fatalf("composed reason=%q, want %q + %q", got, firstCause, genericText)
	}
	if strings.Contains(got, laterCause) {
		t.Fatalf("composed reason=%q used a later attempt's cause", got)
	}
}

// B3. 兜底链: attempt 级缺失 -> 执行级 (G5 逐字节行为); 两者都缺失 -> 旧文案原文。
func TestComposeDeadlineReasonFallbackLadder(t *testing.T) {
	const (
		deadlineText = "deadline expired before dispatch"
		attemptCause = "dispatch rejected: attempt-level"
		execCause    = "dispatch rejected: execution-level"
	)
	cases := []struct {
		name      string
		attempt   string
		execution string
		want      string
	}{
		{"attempt_only", attemptCause, "", attemptCause + "; " + deadlineText},
		{"execution_only", "", execCause, execCause + "; " + deadlineText},
		{"attempt_wins", attemptCause, execCause, attemptCause + "; " + deadlineText},
		{"blank_attempt_falls_back", "   ", execCause, execCause + "; " + deadlineText},
		{"none_is_legacy_text", "", "", deadlineText},
		{"blank_execution_is_legacy_text", "", "  ", deadlineText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDeadlineReason(tc.attempt, tc.execution); got != tc.want {
				t.Fatalf("composeDeadlineReason(%q, %q) = %q, want %q", tc.attempt, tc.execution, got, tc.want)
			}
		})
	}
}

// C. 终局列宽断言 (纯函数路径): 组合结果必须 ≤ 列宽, 且在多字节原因下仍旧是合法
// UTF-8 —— 列宽以 command_executions.final_reason 的 size:256 为准。
func TestComposeDeadlineReasonHonorsColumnBudget(t *testing.T) {
	const deadlineText = "deadline expired before dispatch"
	long := "dispatch rejected: " + strings.Repeat("误", 400)

	// 走 composeDeadlineReason 本身 (而不是只测 clampRunes): 预算必须真的接在组装
	// 路径上, 否则"clamp 上限调大"这类变异不会体现在终局文案里。
	composed := composeDeadlineReason(long, "")
	if !utf8.ValidString(composed) {
		t.Fatalf("composed reason is not valid UTF-8: %q", composed)
	}
	if got := len([]rune(composed)); got > finalReasonColumnChars {
		t.Fatalf("composed reason length=%d runes exceeds the final_reason column width of %d (size:256)", got, finalReasonColumnChars)
	}
	if !strings.HasSuffix(composed, deadlineText) {
		t.Fatalf("composed reason=%q must keep the deadline statement intact", composed)
	}
	if !strings.Contains(composed, "…") {
		t.Fatalf("an over-long cause must be visibly truncated, got %q", composed)
	}
	// 生产预算必须与列宽口径一致 —— 只改常量不改断言, 或只改断言不改常量, 都要红。
	if FinalReasonColumnRunes != finalReasonColumnChars {
		t.Fatalf("FinalReasonColumnRunes=%d, want %d (the final_reason column width)", FinalReasonColumnRunes, finalReasonColumnChars)
	}
}
