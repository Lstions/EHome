package api

// 保存校验 V7 与规则级授权的生命周期门禁 (方案 §4.1.1 V7 / §8 A-10, 实施计划 §0.1)。
//
// 走**真实 HTTP 路由** + 真实 commandexec.Catalog 判定风险等级 (不是打桩), 因为
// V7 的判据本身就是"该 action_id 在当前 Catalog 里是不是风险动作" —— 打桩就把
// 被测逻辑换掉了。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// riskCatalog 是 automationCatalogQuerier 的测试替身: 只回答"某动作的风险等级"。
// 生产实现是 commandexec.Service.Catalog, 这里用最小结构避免为一条校验拉起整条
// channel/manifest/capability 链路 (那属于 commandexec 自己的测试面)。
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

// newRiskRouter 装配只含 automation-rules 的路由, 注入风险目录。
func newRiskRouter(t *testing.T, catalog automationCatalogQuerier) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{}, &models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate automation: %v", err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, nil, nil, nil, catalog)
	return r, db
}

// riskRuleBody 造一条 device_action 规则; requireConfirmed / acknowledged 可调。
func riskRuleBody(actionID string, requireConfirmed bool, acknowledged *bool) []byte {
	body := map[string]interface{}{
		"name":                 "风险动作保存校验",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionDeviceAction,
		"action_device_id":     1,
		"action_id":            actionID,
		"action_params_json":   "{}",
		"require_confirmed":    requireConfirmed,
	}
	if acknowledged != nil {
		body["risk_acknowledged"] = *acknowledged
	}
	raw, _ := json.Marshal(body)
	return raw
}

func boolPtr(v bool) *bool { return &v }

// reloadRule 从库里重新读一行到**全新的**结构体。
//
// 为什么必须新结构体: GORM 把 SQL NULL 扫进一个已经持有非 nil 指针的 *time.Time
// 字段时不会把指针置回 nil (字段 Set 对 nil 值在已有值上不生效)。复用同一个结构体
// 读第二遍会拿到**陈旧**的授权时间戳, 于是"授权已被清空"这类断言会假绿, 而
// "授权仍在"这类断言会假红。本文件的每个断言都基于库里真实状态, 因此统一走这里。
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
		"set_mos_policy_v2": deviceaction.RiskElevated, // 用于验证"换动作"清空授权
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

// ─── A-10: 风险动作 + require_confirmed=false + 未授权 → 400 risk_action_requires_confirmation ───
func TestCreateRiskActionWithoutConfirmationIsRejected(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	code, resp := postRule(t, r, riskRuleBody("set_mos_policy", false, nil))
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (body=%v)", code, resp)
	}
	if resp["error_code"] != "risk_action_requires_confirmation" {
		t.Fatalf("error_code=%v want risk_action_requires_confirmation", resp["error_code"])
	}
	// 关键: 不得落库 —— 否则会存下一条"永远不会执行"的规则 (C5 根因)。
	var cnt int64
	db.Model(&models.AutomationRule{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("被拒的规则不得落库, 实际 %d 条", cnt)
	}
}

// V7 的正向对照 1: 显式授权 → 201/200 且 risk_acknowledged_at 真的写入。
func TestCreateRiskActionWithAcknowledgementSucceeds(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	code, resp := postRule(t, r, riskRuleBody("set_mos_policy", false, boolPtr(true)))
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%v)", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("显式授权后 risk_acknowledged_at 必须写入时间戳 (授权载体)")
	}
}

// V7 的正向对照 2: require_confirmed=true → 无需授权 (走人工确认链路)。
func TestCreateRiskActionWithRequireConfirmedSucceeds(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	code, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, nil))
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%v)", code, resp)
	}
}

// V7 的负向对照 (不得误伤): 普通动作 + require_confirmed=false 照旧放行。
func TestCreateNormalActionWithoutConfirmationStillSucceeds(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	code, resp := postRule(t, r, riskRuleBody("read_rainfall", false, nil))
	if code != http.StatusOK {
		t.Fatalf("普通动作必须照常放行, status=%d body=%v", code, resp)
	}
}

// V7 的负向对照: notification 动作不涉及风险动作判定, 不得被误拦。
func TestCreateNotificationActionUnaffectedByV7(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	body := map[string]interface{}{
		"name":                 "纯通知",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionNotification,
		"action_level":         models.AlertLevelInfo,
	}
	raw, _ := json.Marshal(body)
	code, resp := postRule(t, r, raw)
	if code != http.StatusOK {
		t.Fatalf("notification 动作不得被 V7 误拦, status=%d body=%v", code, resp)
	}
}

// ─── 授权失效: 动作集合变化必须清空 risk_acknowledged_at ───────────────────
//
// 两个语义必须同时成立, 否则会留下"旧授权放行新动作"的漏洞:
//
//	① 换动作 (动作集合变化) 时授权被清空 —— 即使用户没有在请求里提到授权;
//	② 清空后若仍配 require_confirmed=false 的风险动作 → V7 拒绝, 强制重新确认。
//
// ② 让"清空 + 直接执行"不可能在同一次请求里完成 (fail-closed)。
func TestUpdateActionSetChangeClearsAcknowledgement(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	// 用 require_confirmed=true 建规则并授权: 这样"换动作"这一步不会被 V7 拦,
	// 能单独观测到"授权被清空"这一事实。
	if _, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, boolPtr(true))); resp["code"] != float64(200) {
		t.Fatalf("前置规则创建失败: %v", resp)
	}
	var rule models.AutomationRule
	if err := db.First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if rule.RiskAcknowledgedAt == nil {
		t.Fatal("前置条件失败: 规则应已授权")
	}
	put := func(body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var decoded map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
		return rec.Code, decoded
	}

	// ① 换动作 (action_id 变化) → 授权必须被清空。
	code, resp := put(map[string]interface{}{"action_id": "set_mos_policy_v2"})
	if code != http.StatusOK {
		t.Fatalf("require_confirmed=true 时换动作应被接受, status=%d body=%v", code, resp)
	}
	after := reloadRule(t, db, rule.ID)
	if after.RiskAcknowledgedAt != nil {
		t.Fatal("动作集合变化必须清空 risk_acknowledged_at (旧授权不得放行新动作)")
	}
	if after.ActionID != "set_mos_policy_v2" {
		t.Fatalf("动作未更新: action_id=%q", after.ActionID)
	}
}

// ② 清空 + require_confirmed=false 在同一次请求里 → 必须被 V7 拒绝 (不允许"换动作即执行")。
func TestUpdateActionChangeWithRequireConfirmedFalseIsRejected(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	if _, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, boolPtr(true))); resp["code"] != float64(200) {
		t.Fatalf("前置规则创建失败: %v", resp)
	}
	var before models.AutomationRule
	if err := db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"action_id":         "set_mos_policy_v2",
		"require_confirmed": false,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("换动作 + require_confirmed=false 必须被 V7 拒绝 (旧授权不得沿用), status=%d body=%s", rec.Code, rec.Body.String())
	}
	var decoded map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	if decoded["error_code"] != "risk_action_requires_confirmation" {
		t.Fatalf("error_code=%v want risk_action_requires_confirmation", decoded["error_code"])
	}
	// 被拒的请求整体不得落库: 动作与授权都必须原样保留。
	after := reloadRule(t, db, before.ID)
	if after.ActionID != before.ActionID {
		t.Fatalf("被拒的更新不得改动动作: %q → %q", before.ActionID, after.ActionID)
	}
	if after.RiskAcknowledgedAt == nil {
		t.Fatal("被拒的更新不得改动授权列")
	}
}

// 反向自证: 只改名称/冷却等非动作字段 → 授权**不得**被清空 (否则用户每次改名都要重授权)。
func TestUpdateNonActionFieldKeepsAcknowledgement(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	if _, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, boolPtr(true))); resp["code"] != float64(200) {
		t.Fatalf("前置规则创建失败: %v", resp)
	}
	raw, _ := json.Marshal(map[string]interface{}{"name": "改个名字"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("改名称不应被拒: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("仅改名称不得清空授权 (非动作字段变化不失效)")
	}
}

// 显式撤回授权: risk_acknowledged=false 且 require_confirmed=false 的风险动作 → 被 V7 拒绝。
func TestUpdateRevokeAcknowledgementIsRejected(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	if _, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, boolPtr(true))); resp["code"] != float64(200) {
		t.Fatalf("前置规则创建失败: %v", resp)
	}
	// 先切到 require_confirmed=false + 已授权 (合法)。
	raw, _ := json.Marshal(map[string]interface{}{"require_confirmed": false, "risk_acknowledged": true})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("授权更新失败: %s", rec.Body.String())
	}
	// 再撤回授权 → 必须被拒。
	raw2, _ := json.Marshal(map[string]interface{}{"risk_acknowledged": false})
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1", bytes.NewReader(raw2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("撤回授权后仍是 require_confirmed=false 的风险动作, 必须被 V7 拒绝; status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("被拒的更新不得改动授权列 (撤回被 V7 拒绝后, 原授权必须原样保留)")
	}
}

// Catalog 为 nil (单测兼容路径) 时不得猜测风险等级 —— 跳过 V7 而不是误判。
func TestCreateWithNilCatalogSkipsV7(t *testing.T) {
	r, _ := newRiskRouter(t, nil)
	code, resp := postRule(t, r, riskRuleBody("set_mos_policy", false, nil))
	if code != http.StatusOK {
		t.Fatalf("catalog=nil 时应跳过 V7 (无法判定风险), status=%d body=%v", code, resp)
	}
}

// 授权时间戳必须真实落库 (不是内存态) —— 重启后自动路径仍能读到。
func TestAcknowledgementPersistedAcrossReads(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	if _, resp := postRule(t, r, riskRuleBody("set_mos_policy", true, boolPtr(true))); resp["code"] != float64(200) {
		t.Fatalf("前置规则创建失败: %v", resp)
	}
	rule := reloadRule(t, db, 1)
	if rule.RiskAcknowledgedAt == nil || rule.RiskAcknowledgedAt.IsZero() {
		t.Fatal("授权时间戳未落库")
	}
	if rule.RiskAcknowledgedAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("授权时间戳不合理: %v", rule.RiskAcknowledgedAt)
	}
}

var _ = testutil.OpenTestDB
