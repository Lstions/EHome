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

// ==================== R4: 派发失败可观测 (2026-09-20 故障回归) ====================
//
// 故障期间 dispatch **每次都失败**, 但失败只打进了服务端日志:
// execution 行一直停在 QUEUED, 直到 deadline 到期才写成
// "deadline expired before dispatch"。用户看到的是"操作一直排队然后超时",
// 而真正的原因 (地址被拒) 完全不可见。
//
// 这些测试钉死: 派发被拒时 execution.final_reason 立即带上可读原因,
// 且成功投递时必须清空, 状态机 (QUEUED) 与 err 语义保持不变。

func TestProcessOnceTransportErrorRecordsReadableReason(t *testing.T) {
	ft := &dispatchFakeTransport{err: fmt.Errorf("hardware_id %q must be an address from 1 to 254", "UART1")}
	d, exec, _ := setupDispatcher(t, ft)

	if _, err := d.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected the transport error to propagate unchanged")
	}

	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)

	// Status machine unchanged: the execution must still be QUEUED so
	// RecoverExpired keeps applying its deadline semantics.
	if updated.Status != StatusQueued {
		t.Fatalf("execution status=%s, want QUEUED (the reason is informational only)", updated.Status)
	}
	if !strings.HasPrefix(updated.FinalReason, "dispatch rejected: ") {
		t.Fatalf("final_reason=%q, want a 'dispatch rejected: ...' summary", updated.FinalReason)
	}
	if !strings.Contains(updated.FinalReason, "UART1") {
		t.Fatalf("final_reason=%q must carry the underlying cause", updated.FinalReason)
	}
}

// A successful dispatch must not leave a stale rejection behind: after a retry
// the row is on the wire, so an old reason would be actively misleading.
func TestProcessOnceSuccessClearsStaleRejectionReason(t *testing.T) {
	ft := &dispatchFakeTransport{err: fmt.Errorf("device channel is not reported")}
	d, exec, outbox := setupDispatcher(t, ft)

	if _, err := d.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected the first dispatch to fail")
	}
	var afterFailure models.CommandExecution
	d.db.First(&afterFailure, "command_id = ?", exec.CommandID)
	if afterFailure.FinalReason == "" {
		t.Fatal("first failure must record a reason")
	}

	// The failed attempt left the outbox LEASED (its transaction rolled back).
	// Recover the lease the way ProcessOnce's own recovery path does, then let
	// the same command dispatch successfully.
	ft.err = nil
	ft.result = DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}
	past := time.Now().UTC().Add(-time.Minute)
	if err := d.db.Model(&models.CommandOutbox{}).Where("id = ?", outbox.ID).
		Updates(map[string]interface{}{"state": "PENDING", "lease_owner": "", "lease_expires_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	_ = past

	processed, err := d.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("second dispatch must succeed: %v", err)
	}
	if !processed {
		t.Fatal("second dispatch should have processed the outbox row")
	}

	var afterSuccess models.CommandExecution
	d.db.First(&afterSuccess, "command_id = ?", exec.CommandID)
	if afterSuccess.Status != StatusDispatched {
		t.Fatalf("execution status=%s, want DISPATCHED", afterSuccess.Status)
	}
	if afterSuccess.FinalReason != "" {
		t.Fatalf("final_reason=%q must be cleared after a successful dispatch", afterSuccess.FinalReason)
	}
}

// The reason is truncated rather than silently cut by the DB column (size:256).
func TestProcessOnceTransportErrorTruncatesLongReason(t *testing.T) {
	long := strings.Repeat("误", 400)
	ft := &dispatchFakeTransport{err: fmt.Errorf("%s", long)}
	d, exec, _ := setupDispatcher(t, ft)

	if _, err := d.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected the transport error to propagate")
	}
	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)

	if !strings.HasSuffix(updated.FinalReason, "…") {
		t.Fatalf("truncated reason must be visibly truncated, got %q", updated.FinalReason)
	}
	if got := len([]rune(updated.FinalReason)); got > dispatchRejectionMaxRunes {
		t.Fatalf("reason length=%d runes, want <= %d (the final_reason column budget)", got, dispatchRejectionMaxRunes)
	}
	if !strings.HasPrefix(updated.FinalReason, "dispatch rejected: ") {
		t.Fatalf("truncated reason must keep its readable prefix, got %q", updated.FinalReason)
	}
}

// A dispatch that succeeds on the first try must never write a reason.
func TestProcessOnceSuccessLeavesReasonEmpty(t *testing.T) {
	ft := &dispatchFakeTransport{result: DispatchResult{BootID: "boot-1", PublishedAt: time.Now().UTC()}}
	d, exec, _ := setupDispatcher(t, ft)

	if _, err := d.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var updated models.CommandExecution
	d.db.First(&updated, "command_id = ?", exec.CommandID)
	if updated.FinalReason != "" {
		t.Fatalf("final_reason=%q must stay empty on a clean dispatch", updated.FinalReason)
	}
}

// clampRunes is the shared truncation helper; multi-byte text must never be
// sliced mid-character.
func TestClampRunesKeepsValidUTF8(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{"shorter_than_limit", "abc", 10, "abc"},
		{"exactly_at_limit", "abcde", 5, "abcde"},
		// The ellipsis counts against the budget (the caller sizes it after the
		// storage column), so a cut result is always exactly limit runes long.
		{"cut_ascii", "abcdefg", 5, "abcd…"},
		{"cut_multibyte", "雨量计地址非法", 3, "雨量…"},
		{"cut_to_one", "abc", 1, "…"},
		{"zero_budget", "abc", 0, ""},
		{"empty", "", 5, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clampRunes(tc.value, tc.limit)
			if got != tc.want {
				t.Fatalf("clampRunes(%q, %d) = %q, want %q", tc.value, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("clampRunes produced invalid UTF-8: %q", got)
			}
		})
	}
}
