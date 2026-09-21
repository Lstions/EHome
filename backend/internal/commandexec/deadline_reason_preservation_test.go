package commandexec

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ehome/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== G5: deadline 不得覆盖派发器写下的真实原因 (2026-09-21) ====================
//
// R4 让派发被拒时立刻把 "dispatch rejected: <cause>" 写到 execution.final_reason
// (dispatcher.go recordDispatchRejection), 当时状态仍是 QUEUED。但 RecoverExpired
// 在 deadline 到期时把同一行**整句覆盖**成 "deadline expired before dispatch":
//
//	审计实测: 同一 command_id, t+20s 看到真实原因, t+135s 只剩通用文案。
//
// 于是用户点进历史看到的和事故前一模一样 —— 可观测性在最后一步被自己抹掉。
//
// 修复口径: deadline 是**一个**原因, 但"已知的具体原因"更具体。两者都保留,
// 用 "; " 连接; 没有已知原因时仍然是原样那一句。状态机语义 (FAILED /
// completed_at / outbox CANCELLED) 完全不变。

// seedQueuedWithRejection 造一个 QUEUED 且已带派发拒绝原因的执行行,
// 复刻"派发被拒 -> 尚未到 deadline"这一刻的真实 DB 状态。
func seedQueuedWithRejection(t *testing.T, rejection string) (*Service, *models.CommandExecution) {
	t.Helper()
	s, exec, _ := setupInboxService(t, StatusQueued)
	if rejection != "" {
		if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).
			Update("final_reason", rejection).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&models.CommandOutbox{CommandID: exec.CommandID, EventType: "dispatch", PayloadJSON: "{}", State: "PENDING", CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	return s, exec
}

// 契约点名的对照实验 (时间跨度): 同一个 command_id 在"20s"与"135s"两个时刻观察
// final_reason。20s 由派发器写, 135s 由 RecoverExpired 走 deadline 结算。
// 断言两个时刻**都能看到真实原因** —— 这正是修复前失败、修复后通过的那条。
func TestDeadlineNeverOverwritesDispatchRejectionReason(t *testing.T) {
	const cause = "dispatch rejected: action read_rainfall target address: hardware_id \"UART1\" must be an address from 1 to 254"
	s, exec := seedQueuedWithRejection(t, cause)
	ctx := context.Background()

	// t+20s: 尚未到期, 行上就是派发器写下的真实原因。
	early, err := s.Get(ctx, exec.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if early.Status != StatusQueued {
		t.Fatalf("t+20s status=%s, want QUEUED", early.Status)
	}
	if early.FinalReason != cause {
		t.Fatalf("t+20s reason=%q, want the dispatcher's cause verbatim", early.FinalReason)
	}

	// 时间前进到 deadline 之后 (等价于 t+135s 的那次 RecoverExpired 扫描)。
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}

	// t+135s: 结算后真实原因必须仍然可见。
	late, err := s.Get(ctx, exec.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if late.Status != StatusFailed {
		t.Fatalf("t+135s status=%s, want FAILED (state machine unchanged)", late.Status)
	}
	if late.CompletedAt == nil {
		t.Fatal("t+135s completed_at must be stamped")
	}
	if !strings.Contains(late.FinalReason, "must be an address from 1 to 254") {
		t.Fatalf("t+135s reason=%q lost the underlying cause; the operator sees the same generic text as before the fix", late.FinalReason)
	}
	if !strings.Contains(late.FinalReason, "deadline expired before dispatch") {
		t.Fatalf("t+135s reason=%q must still state that the deadline expired", late.FinalReason)
	}
	if late.FinalReason == "deadline expired before dispatch" {
		t.Fatal("t+135s reason is the generic text alone: the dispatcher's cause was overwritten")
	}
	// 返回给组合根的结构体必须携带同一份文案 (它会推给已连接的客户端)。
	if expired[0].FinalReason != late.FinalReason {
		t.Fatalf("returned reason=%q differs from stored reason=%q", expired[0].FinalReason, late.FinalReason)
	}
}

// 回归保护: 没有已知原因时, 文案必须**逐字不变** —— 既有测试与运维习惯都依赖它。
func TestDeadlineKeepsGenericReasonWhenNoRejectionRecorded(t *testing.T) {
	s, exec := seedQueuedWithRejection(t, "")
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	if expired[0].FinalReason != "deadline expired before dispatch" {
		t.Fatalf("reason=%q, want the exact legacy text when no cause was recorded", expired[0].FinalReason)
	}
	if expired[0].Status != StatusFailed || expired[0].CompletedAt == nil {
		t.Fatalf("status=%s completed=%v, want FAILED with completed_at", expired[0].Status, expired[0].CompletedAt)
	}
}

// 混合批次: 同一批里一行有原因、一行没有, 两行各自的文案都不能串。
// (修复前是一次 IN (...) 批量 UPDATE, 现在必须逐行, 这条钉住逐行没有写错对象。)
func TestDeadlineReasonsDoNotLeakAcrossRows(t *testing.T) {
	s, withCause, _ := setupInboxService(t, StatusQueued)
	// 第二个执行行必须落在**同一个** DB 里 (setupInboxService 每次开新库),
	// 否则这条测不到"批量逐行"的串写风险。
	plain := &models.CommandExecution{
		CommandID: uuid.NewString(), EdgeDeviceID: withCause.EdgeDeviceID, NodeID: withCause.NodeID,
		DeviceType: withCause.DeviceType, DeviceConfigID: withCause.DeviceConfigID, ChannelID: withCause.ChannelID,
		ManifestID: withCause.ManifestID, ActionID: withCause.ActionID, ActionVersion: withCause.ActionVersion,
		ActorUserID: withCause.ActorUserID, IdempotencyScope: "deadline-leak", IdempotencyKey: uuid.NewString(),
		RequestHash: "hash-leak", ParamsJSON: "{}", Status: StatusQueued,
		DeadlineAt: withCause.DeadlineAt, CreatedAt: time.Now().UTC(),
	}
	if err := s.db.Create(plain).Error; err != nil {
		t.Fatal(err)
	}
	const cause = "dispatch rejected: device channel is not reported"
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", withCause.CommandID).Update("final_reason", cause).Error; err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id IN ?", []string{withCause.CommandID, plain.CommandID}).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 2 {
		t.Fatalf("expired count=%d, want 2", len(expired))
	}
	got := map[string]string{}
	for _, e := range expired {
		got[e.CommandID] = e.FinalReason
	}
	if !strings.Contains(got[withCause.CommandID], cause) {
		t.Fatalf("row with a cause got %q", got[withCause.CommandID])
	}
	if got[plain.CommandID] != "deadline expired before dispatch" {
		t.Fatalf("row without a cause got %q, want the generic text only", got[plain.CommandID])
	}
	// DB 侧同样核对, 不能只有内存副本正确。
	var reloaded models.CommandExecution
	if err := s.db.First(&reloaded, "command_id = ?", plain.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.FinalReason != "deadline expired before dispatch" {
		t.Fatalf("stored reason for the cause-less row is %q", reloaded.FinalReason)
	}
}

// DISPATCHED 超期那条路径 (没有最终证据 -> UNKNOWN) 的文案不能被本次改动影响。
func TestDeadlineWithoutFinalEvidenceTextUnchanged(t *testing.T) {
	s, exec, _ := setupInboxService(t, StatusDispatched)
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Status != StatusUnknown {
		t.Fatalf("expired=%+v", expired)
	}
	if expired[0].FinalReason != "deadline expired without final evidence" {
		t.Fatalf("reason=%q, want the unchanged without-evidence text", expired[0].FinalReason)
	}
}

// composedReasonColumnChars 是 command_executions.final_reason 的列宽口径
// (size:256 字符)。写成字面量而**不是**引用生产常量: 生产常量只是"代码认为的
// 预算", 这里要的是"存储真正允许的宽度"。把生产里的 clamp 上限调大时, 下面的断言
// 必须因此变红 —— 两边共用一个常量的话, 这种变异会被静默放过。
const composedReasonColumnChars = 256

// 终局列宽断言 (执行级路径): 组合文案不得突破 final_reason 列预算 (size:256),
// 且必须多字节安全、保留 "deadline expired before dispatch" 这句可判别文案。
//
// 与旧版本的关键差别: 故意喂一条**未预截断**的超长原因, 证明列宽是在组装边界上被
// 保证的, 而不是靠"上游恰好已经截到 200"这一巧合。
func TestComposedDeadlineReasonFitsColumnBudget(t *testing.T) {
	long := "dispatch rejected: " + strings.Repeat("误", 400)
	s, exec := seedQueuedWithRejection(t, long)
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.db.Model(&models.CommandExecution{}).Where("command_id = ?", exec.CommandID).Update("deadline_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	var reloaded models.CommandExecution
	if err := s.db.First(&reloaded, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	assertComposedReasonFitsColumn(t, reloaded.FinalReason)
	if expired[0].FinalReason != reloaded.FinalReason {
		t.Fatalf("returned reason=%q differs from stored reason=%q", expired[0].FinalReason, reloaded.FinalReason)
	}
}

// 同一条终局断言走 **attempt 级** 组合路径 (M-3): 原因写在被拒的 attempt 行上,
// 执行级副本已被成功路径清空。两条独立路径都要满足列宽与多字节安全。
func TestComposedDeadlineReasonFromAttemptLevelFitsColumnBudget(t *testing.T) {
	long := "dispatch rejected: " + strings.Repeat("误", 400)
	s, exec := seedQueuedWithAttemptCause(t, 1, long)

	expired, err := s.RecoverExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired count=%d, want 1", len(expired))
	}
	var reloaded models.CommandExecution
	if err := s.db.First(&reloaded, "command_id = ?", exec.CommandID).Error; err != nil {
		t.Fatal(err)
	}
	assertComposedReasonFitsColumn(t, reloaded.FinalReason)
	// attempt 行本身保留**未截断**的原始原因 (列宽同为 256, 由写入方负责) ——
	// 终态组合是展示层, 不得反过来改写证据行。
	var attempt models.CommandAttempt
	if err := s.db.First(&attempt, "command_id = ? AND attempt_no = ?", exec.CommandID, 1).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.FinalReason != long {
		t.Fatalf("attempt reason was rewritten by the deadline settlement: got %d runes, want %d", len([]rune(attempt.FinalReason)), len([]rune(long)))
	}
	if expired[0].FinalReason != reloaded.FinalReason {
		t.Fatalf("returned reason=%q differs from stored reason=%q", expired[0].FinalReason, reloaded.FinalReason)
	}
}

// assertComposedReasonFitsColumn 是一条口径化断言: 字符数按列宽 (256 chars),
// 字节数另设上界 (256*4) —— 后者独立地证明截断发生在 rune 边界而不是字节边界。
func assertComposedReasonFitsColumn(t *testing.T, reason string) {
	t.Helper()
	if !utf8.ValidString(reason) {
		t.Fatalf("composed reason is not valid UTF-8: %q", reason)
	}
	if got := utf8.RuneCountInString(reason); got > composedReasonColumnChars {
		t.Fatalf("composed reason length=%d chars exceeds the final_reason column width of %d", got, composedReasonColumnChars)
	}
	if got := len(reason); got > composedReasonColumnChars*4 {
		t.Fatalf("composed reason byte length=%d exceeds %d — truncation cut inside a multi-byte character", got, composedReasonColumnChars*4)
	}
	if !strings.HasSuffix(reason, "deadline expired before dispatch") {
		t.Fatalf("composed reason must end with the deadline text (it is what distinguishes 被拒 from 从未派发), got %q", reason)
	}
	if !strings.HasSuffix(strings.TrimSuffix(reason, "deadline expired before dispatch"), "…; ") {
		t.Fatalf("an over-long cause must be visibly truncated before the deadline text, got %q", reason)
	}
}
