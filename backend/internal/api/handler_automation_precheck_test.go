package api

// POST /automation-rules/precheck 契约测试（修 AU-06）。
//
// 契约 = frontend-shared/src/api/automation.ts PrecheckResponse:
//   { risk_actions?: PrecheckRiskAction[], blocking_errors?: string[], warnings?: string[] }
//
// 三条必须被钉死的语义：
//   1. risk_actions 与"今天"的风险清单完全一致（Catalog 的 RequiresConfirmation 单一事实源）；
//   2. 风险动作 + require_confirmed=false + 未授权 **不进 blocking_errors**（否则前端永远存不进去；
//      V7 由保存端点负责，precheck 只负责列清单让用户确认）；
//   3. selector 语法/未知语义 ⇒ blocking；unbound ⇒ warnings；且端点无副作用。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type precheckResolver struct {
	errs    map[string]error
	unbound map[string]bool
	calls   int
}

func (r *precheckResolver) Resolve(_ uint, selector string) (sensorfield.Resolved, error) {
	r.calls++
	if err, ok := r.errs[selector]; ok {
		return sensorfield.Resolved{Selector: selector}, err
	}
	if r.unbound[selector] {
		return sensorfield.Resolved{Selector: selector, Status: sensorfield.StatusUnbound}, nil
	}
	return sensorfield.Resolved{Selector: selector, Status: sensorfield.StatusResolved}, nil
}

func (r *precheckResolver) Enabled() bool { return true }

func withPrecheckResolver(t *testing.T, r *precheckResolver) {
	t.Helper()
	SetFieldSelectorResolver(r)
	t.Cleanup(func() { SetFieldSelectorResolver(nil) })
}

func postPrecheck(t *testing.T, engine *gin.Engine, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-rules/precheck", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	var data map[string]interface{}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode data: %v data=%s", err, string(env.Data))
		}
	}
	return rec.Code, data
}

func precheckRiskBody(actionID string) map[string]interface{} {
	return map[string]interface{}{
		"name":                 "预检风险动作",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionDeviceAction,
		"action_device_id":     1,
		"action_id":            actionID,
		"action_params_json":   "{}",
		"require_confirmed":    false,
	}
}

func precheckSensorBody(sensorName string) map[string]interface{} {
	return map[string]interface{}{
		"name":                    "预检 selector",
		"trigger_type":            models.AutomationTriggerSensorThreshold,
		"trigger_edge_device_id":  1,
		"trigger_sensor_name":     sensorName,
		"trigger_comparator":      "gt",
		"trigger_threshold":       50.0,
		"action_type":             models.AutomationActionNotification,
		"action_level":            models.AlertLevelWarning,
		"cooldown_sec":            300,
		"max_daily_exec":          0,
	}
}

func stringList(t *testing.T, data map[string]interface{}, key string) []string {
	t.Helper()
	raw, ok := data[key].([]interface{})
	if !ok {
		t.Fatalf("响应缺少 %s 数组: %v", key, data)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// 1) risk_actions 语义与 Catalog 单一事实源一致；V7 不阻塞；无副作用。
func TestPrecheckRiskActionsContract(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	code, data := postPrecheck(t, r, precheckRiskBody("set_mos_policy"))
	if code != 200 {
		t.Fatalf("precheck 应 200，got %d", code)
	}
	risks, ok := data["risk_actions"].([]interface{})
	if !ok || len(risks) != 1 {
		t.Fatalf("应返回 1 个风险动作，got %v", data["risk_actions"])
	}
	entry, _ := risks[0].(map[string]interface{})
	if entry["action_id"] != "set_mos_policy" {
		t.Fatalf("风险动作 action_id 不符: %v", entry)
	}
	if entry["risk_level"] != "elevated" {
		t.Fatalf("风险动作应下发 2 级契约 elevated，got %v", entry["risk_level"])
	}
	if entry["risk_label"] != "风险" {
		t.Fatalf("risk_label 应为 风险，got %v", entry["risk_label"])
	}
	if got := stringList(t, data, "blocking_errors"); len(got) != 0 {
		t.Fatalf("precheck 不得因 V7 授权把风险动作判成 blocking（否则永远存不进去），got %v", got)
	}
	// 普通动作不进风险清单。
	normalCode, normalData := postPrecheck(t, r, precheckRiskBody("read_rainfall"))
	if normalCode != 200 {
		t.Fatalf("普通动作 precheck 应 200，got %d", normalCode)
	}
	if risks, _ := normalData["risk_actions"].([]interface{}); len(risks) != 0 {
		t.Fatalf("普通动作不应出现在 risk_actions: %v", normalData["risk_actions"])
	}
	// 无副作用：不写入任何规则。
	var ruleCount int64
	db.Model(&models.AutomationRule{}).Count(&ruleCount)
	if ruleCount != 0 {
		t.Fatalf("precheck 不得落库，got %d 条规则", ruleCount)
	}
}

// 2) selector 语法错误 ⇒ blocking_errors（不阻塞在 apply 之前，前端据此阻止保存）。
func TestPrecheckSelectorSyntaxBlocks(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	withPrecheckResolver(t, &precheckResolver{errs: map[string]error{
		"Temperature": sensorfield.ErrInvalidSyntax,
	}})
	code, data := postPrecheck(t, r, precheckSensorBody("Temperature"))
	if code != 200 {
		t.Fatalf("precheck 本身应 200（错误进 blocking_errors），got %d", code)
	}
	blocking := stringList(t, data, "blocking_errors")
	if len(blocking) == 0 || !strings.Contains(strings.Join(blocking, " | "), "invalid_selector_syntax") {
		t.Fatalf("语法错误应进 blocking_errors 且带 error_code，got %v", blocking)
	}
	if w := stringList(t, data, "warnings"); len(w) != 0 {
		t.Fatalf("语法错误不应只给 warning，got %v", w)
	}
}

// 3) unbound（语法合法但解析不到）⇒ warnings，不阻塞保存。
func TestPrecheckSelectorUnboundWarns(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	withPrecheckResolver(t, &precheckResolver{unbound: map[string]bool{"@soc": true}})
	code, data := postPrecheck(t, r, precheckSensorBody("@soc"))
	if code != 200 {
		t.Fatalf("precheck 应 200，got %d", code)
	}
	if blocking := stringList(t, data, "blocking_errors"); len(blocking) != 0 {
		t.Fatalf("unbound 不得阻塞保存，got %v", blocking)
	}
	warnings := stringList(t, data, "warnings")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "resolution_unbound") {
		t.Fatalf("unbound 应进 warnings 且带 resolution_unbound，got %v", warnings)
	}
}

// 4) conditions_json 里的 selector 同样校验（评审 B5 的接口面）。
func TestPrecheckConditionsSelectorBlocks(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	withPrecheckResolver(t, &precheckResolver{errs: map[string]error{
		"@bad": sensorfield.ErrUnknownSemantic,
	}})
	body := precheckSensorBody("temperature")
	body["conditions_json"] = "[{\"sensor_name\":\"@bad\",\"comparator\":\"lt\",\"threshold\":30}]"
	_, data := postPrecheck(t, r, body)
	blocking := strings.Join(stringList(t, data, "blocking_errors"), " | ")
	if !strings.Contains(blocking, "conditions_json[0]") || !strings.Contains(blocking, "unknown_semantic") {
		t.Fatalf("conditions 的 selector 错误应带下标进 blocking_errors，got %s", blocking)
	}
}

// 5) 非法 conditions_json（非 JSON）⇒ blocking。
func TestPrecheckConditionsInvalidJSON(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	body := precheckSensorBody("temperature")
	body["conditions_json"] = "not-json"
	_, data := postPrecheck(t, r, body)
	blocking := strings.Join(stringList(t, data, "blocking_errors"), " | ")
	if !strings.Contains(blocking, "conditions_json") {
		t.Fatalf("非法 conditions_json 应进 blocking_errors，got %s", blocking)
	}
}

// 6) 未接线 resolver：保持旧行为（不校验 selector，不 panic）。
func TestPrecheckWithoutResolverKeepsLegacy(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	code, data := postPrecheck(t, r, precheckSensorBody("@soc"))
	if code != 200 {
		t.Fatalf("未接线时应 200，got %d", code)
	}
	if len(stringList(t, data, "blocking_errors")) != 0 || len(stringList(t, data, "warnings")) != 0 {
		t.Fatalf("未接线时不应产生 selector 诊断，got %v / %v", data["blocking_errors"], data["warnings"])
	}
}

var _ = gorm.ErrRecordNotFound // 保持 gorm 依赖显式（与同包其它测试一致的 import 形态）
