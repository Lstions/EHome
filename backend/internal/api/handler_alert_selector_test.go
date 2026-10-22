package api

// 告警规则保存期 selector 校验（设计 §9.2#7 / §8.5）。
//
// 规则：保存端点只返回硬错误（语法/未知语义/开关关闭下的特殊 selector ⇒ 400 +
// error_code）；unbound（语法合法但当前解析不到）**不阻塞**（软告警属 precheck 的
// warnings，告警保存端点不返回 warnings）。

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
)

func alertRuleBody(sensorName string) map[string]any {
	return map[string]any{
		"target_type": models.AlertTargetEdgeDevice,
		"target_id":   1,
		"sensor_name": sensorName,
		"comparator":  "gt",
		"threshold":   50.0,
		"level":       models.AlertLevelWarning,
		"enabled":     true,
		"name":        "选择器校验",
	}
}

// 语法错误 ⇒ 400 + error_code（invalid_selector_syntax），不得静默保存。
func TestAlertSaveRejectsInvalidSelector(t *testing.T) {
	dbr := setupTestDB(t)
	if err := dbr.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), dbr, nil)
	withPrecheckResolver(t, &precheckResolver{errs: map[string]error{
		"Temperature": sensorfield.ErrInvalidSyntax,
	}})

	resp := alertReq(t, r, "POST", "/api/v1/alert-rules", alertRuleBody("Temperature"))
	if resp.Code != 400 {
		t.Fatalf("非法 selector 应 400，got %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "invalid_selector_syntax") {
		t.Fatalf("400 响应应带 error_code invalid_selector_syntax，body=%s", resp.Body.String())
	}
	var count int64
	dbr.Model(&models.AlertRule{}).Count(&count)
	if count != 0 {
		t.Fatalf("非法 selector 不得落库，got %d", count)
	}
}

// unbound ⇒ 允许保存（符号可能尚未上报；不阻塞）。
func TestAlertSaveAllowsUnboundSelector(t *testing.T) {
	dbr := setupTestDB(t)
	if err := dbr.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), dbr, nil)
	withPrecheckResolver(t, &precheckResolver{unbound: map[string]bool{"@soc": true}})

	resp := alertReq(t, r, "POST", "/api/v1/alert-rules", alertRuleBody("@soc"))
	if resp.Code != 201 {
		t.Fatalf("unbound 不应阻塞保存，got %d body=%s", resp.Code, resp.Body.String())
	}
	var count int64
	dbr.Model(&models.AlertRule{}).Count(&count)
	if count != 1 {
		t.Fatalf("应落库 1 条规则，got %d", count)
	}
}

// 未知语义 ⇒ 400 + unknown_semantic。
func TestAlertSaveRejectsUnknownSemantic(t *testing.T) {
	dbr := setupTestDB(t)
	if err := dbr.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), dbr, nil)
	withPrecheckResolver(t, &precheckResolver{errs: map[string]error{
		"@nope": sensorfield.ErrUnknownSemantic,
	}})

	resp := alertReq(t, r, "POST", "/api/v1/alert-rules", alertRuleBody("@nope"))
	if resp.Code != 400 || !strings.Contains(resp.Body.String(), "unknown_semantic") {
		t.Fatalf("未知语义应 400 + unknown_semantic，got %d body=%s", resp.Code, resp.Body.String())
	}
}

// 未接线 resolver ⇒ 旧行为（不校验，照常保存）。
func TestAlertSaveWithoutResolverKeepsLegacy(t *testing.T) {
	dbr := setupTestDB(t)
	if err := dbr.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), dbr, nil)

	resp := alertReq(t, r, "POST", "/api/v1/alert-rules", alertRuleBody("Temperature"))
	if resp.Code != 201 {
		t.Fatalf("未接线时保持旧行为（允许保存），got %d body=%s", resp.Code, resp.Body.String())
	}
}
