package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// TestValidateRiskActionAuthorizationFailClosed 钉住 2026-11-18 的 V7 收口。
//
// 旧实现在"设备能力目录查不到"时直接 continue（跳过该动作）—— 等于把"无法判定
// 风险等级"当成"不是风险动作"放行，存下一条永远过不了确认门的规则，而那正是 V7
// 要拦的东西。用户裁定：能保存成功就一定是完成了授权，这条不变式不能靠"上游也会查
// 目录"兜着。
func TestValidateRiskActionAuthorizationFailClosed(t *testing.T) {
	actions := []models.AutomationRuleAction{{
		ActionType:     models.AutomationActionDeviceAction,
		ActionDeviceID: 1,
		ActionID:       "set_mos_policy",
		Enabled:        true,
	}}

	t.Run("目录查询失败", func(t *testing.T) {
		msg, code := validateRiskActionAuthorization(context.Background(),
			riskCatalog{err: errors.New("db down")}, actions, false, false)
		if msg == "" {
			t.Fatal("目录查询失败必须 fail-closed 拒绝保存，而不是跳过风险校验")
		}
		if code != "risk_catalog_unavailable" {
			t.Fatalf("code=%q, 依赖故障需要一个可区分的错误码", code)
		}
	})

	t.Run("目录中找不到该动作", func(t *testing.T) {
		msg, code := validateRiskActionAuthorization(context.Background(),
			riskCatalog{levels: map[string]deviceaction.RiskLevel{}}, actions, false, false)
		if msg == "" {
			t.Fatal("动作不在目录中同样不得跳过（跳过等于未校验就放行）")
		}
		if code != "risk_catalog_unavailable" {
			t.Fatalf("code=%q", code)
		}
	})

	t.Run("正常拒绝仍是配置类错误码", func(t *testing.T) {
		msg, code := validateRiskActionAuthorization(context.Background(),
			riskCatalog{levels: map[string]deviceaction.RiskLevel{"set_mos_policy": deviceaction.RiskElevated}},
			actions, false, false)
		if msg == "" || code != "risk_action_requires_confirmation" {
			t.Fatalf("msg=%q code=%q", msg, code)
		}
	})
}

// TestWriteRiskAuthorizationErrorStatus 钉住两类拒绝的状态码分列：
// 规则配置问题 => 400（调用方改请求）；依赖故障 => 503 + Retry-After（稍后重试）。
// 混用 400 会让监控把服务端故障计成客户端错误，前端也不会提示重试。
func TestWriteRiskAuthorizationErrorStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeRiskAuthorizationError(c, "risk_catalog_unavailable", "catalog down")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("依赖故障 status=%d, want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After=%q, want 5", got)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	writeRiskAuthorizationError(c2, "risk_action_requires_confirmation", "need ack")
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("配置问题 status=%d, want 400", w2.Code)
	}
}
