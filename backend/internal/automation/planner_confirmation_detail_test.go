package automation

import (
	"errors"
	"strings"
	"testing"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
)

// TestDescribeDeviceActionError 钉住确认门两类失败的用户可见文案。
//
// 2026-11-18 简化后 system 路径不再走到确认门, 这里只剩"令牌无效/铸造限流"
// 两类 (人工路径的铸造失败) 以及非确认门错误的原样透传。
func TestDescribeDeviceActionError(t *testing.T) {
	t.Run("令牌无效: 指向内部错误", func(t *testing.T) {
		got := describeDeviceActionError(commandexec.ErrConfirmationInvalid)
		if !strings.Contains(got, "确认令牌无效") {
			t.Fatalf("detail=%q", got)
		}
	})

	t.Run("铸造限流: 提示稍后重试", func(t *testing.T) {
		got := describeDeviceActionError(commandexec.ErrConfirmationRateLimited)
		if !strings.Contains(got, "过于频繁") {
			t.Fatalf("detail=%q", got)
		}
	})

	t.Run("非确认门错误原样透传", func(t *testing.T) {
		if got := describeDeviceActionError(errors.New("boom")); got != "boom" {
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
