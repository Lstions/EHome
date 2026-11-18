package automation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
)

// TestDescribeDeviceActionError 钉住 2026-11-18 的观测性修复。
//
// 三类确认门失败**处置方向完全不同**，必须按错误本身分支（审查指出：早期版本只看
// RiskAcknowledgedAt，会把"已授权但没带令牌"误诊成"令牌无效"——恰好误诊它自己要修
// 的那次生产事故）。
func TestDescribeDeviceActionError(t *testing.T) {
	ackAt := time.Now().UTC()
	unacked := models.AutomationRule{Name: "legacy"}
	acked := models.AutomationRule{Name: "acked", RiskAcknowledgedAt: &ackAt}

	t.Run("未授权规则被拦: 给原因与处置", func(t *testing.T) {
		got := describeDeviceActionError(unacked, commandexec.ErrConfirmationRequired)
		if !strings.Contains(got, "缺少规则级风险授权") || !strings.Contains(got, "勾选风险确认") {
			t.Fatalf("detail=%q", got)
		}
	})

	t.Run("已授权但没带令牌: 内部错误, 不要让用户去重新授权", func(t *testing.T) {
		got := describeDeviceActionError(acked, commandexec.ErrConfirmationRequired)
		if !strings.Contains(got, "未携带确认令牌") {
			t.Fatalf("detail=%q, 应判为未携带令牌", got)
		}
		if strings.Contains(got, "勾选风险确认") {
			t.Fatalf("detail=%q, 已授权规则不该让用户去重新授权", got)
		}
	})

	t.Run("令牌无效: 指向内部错误", func(t *testing.T) {
		got := describeDeviceActionError(acked, commandexec.ErrConfirmationInvalid)
		if !strings.Contains(got, "确认令牌无效") {
			t.Fatalf("detail=%q", got)
		}
	})

	t.Run("铸造限流: 提示稍后重试", func(t *testing.T) {
		got := describeDeviceActionError(acked, commandexec.ErrConfirmationRateLimited)
		if !strings.Contains(got, "过于频繁") {
			t.Fatalf("detail=%q", got)
		}
	})

	t.Run("非确认门错误原样透传", func(t *testing.T) {
		if got := describeDeviceActionError(unacked, errors.New("boom")); got != "boom" {
			t.Fatalf("detail=%q", got)
		}
	})
}

// TestClassifyDeviceActionError 把"哪个错误落哪个结果码"钉成表：
// 审查指出限流原先落 failed_dispatch（用户看到"下发失败"，与传输故障混淆）。
func TestClassifyDeviceActionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"确认门-缺令牌", commandexec.ErrConfirmationRequired, models.AutomationResultConfirmationRequired},
		{"确认门-令牌无效", commandexec.ErrConfirmationInvalid, models.AutomationResultConfirmationRequired},
		{"确认门-铸造限流", commandexec.ErrConfirmationRateLimited, models.AutomationResultFailedGate},
		{"确认门-无权铸造", commandexec.ErrConfirmationForbidden, models.AutomationResultFailedGate},
		{"可用性门禁", commandexec.ErrActionUnavailable, models.AutomationResultFailedGate},
		{"其它", errors.New("boom"), models.AutomationResultFailedDispatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyDeviceActionError(c.err); got != c.want {
				t.Fatalf("classify=%q want %q", got, c.want)
			}
		})
	}
}
