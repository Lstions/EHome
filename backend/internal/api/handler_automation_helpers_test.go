package api

// 自动化规则写路径测试的共享装配（2026-11-18 删除规则级授权后仍需要）：
// 真实 HTTP 路由 + 真实 Catalog 判定，只保留写路径断言需要的部分。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// riskCatalog 是 automationCatalogQuerier 的测试替身：只回答某动作的风险等级。
type riskCatalog struct {
	levels map[string]deviceaction.RiskLevel
	err    error
}

func (c riskCatalog) Catalog(_ context.Context, _ uint) ([]commandexec.CatalogItem, error) {
	if c.err != nil {
		return nil, c.err
	}
	items := make([]commandexec.CatalogItem, 0, len(c.levels))
	for id, level := range c.levels {
		items = append(items, commandexec.CatalogItem{
			Definition: deviceaction.Definition{ID: id, RiskLevel: level},
			Available:  true,
		})
	}
	return items, nil
}

func newRiskRouter(t *testing.T, catalog automationCatalogQuerier) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{}, &models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate automation: %v", err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, nil, nil, nil, catalog)
	return r, db
}

func boolPtr(v bool) *bool { return &v }

// reloadRule 从库里重新读一行到全新的结构体（避免 GORM 复用指针带来的陈旧值）。
func reloadRule(t *testing.T, db *gorm.DB, id uint) models.AutomationRule {
	t.Helper()
	var rule models.AutomationRule
	if err := db.First(&rule, id).Error; err != nil {
		t.Fatalf("重读规则 %d 失败: %v", id, err)
	}
	return rule
}

// riskCatalogFixture: set_mos_policy 是风险动作, read_rainfall 是普通动作。
func riskCatalogFixture() riskCatalog {
	return riskCatalog{levels: map[string]deviceaction.RiskLevel{
		"set_mos_policy":    deviceaction.RiskElevated,
		"set_mos_policy_v2": deviceaction.RiskElevated,
		"read_rainfall":     deviceaction.RiskNormal,
	}}
}

func postRule(t *testing.T, r *gin.Engine, body []byte) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var decoded map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("解析响应失败: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, decoded
}
