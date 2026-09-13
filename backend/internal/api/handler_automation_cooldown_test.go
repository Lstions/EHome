package api

// 负债 D-5 契约层守护: cooldown_sec 的 API 契约、应用层默认值与持久化结果
// 必须一致 —— 0 = 不冷却 (显式零值原样入库), 未传 = 应用层默认 300。
//
// 缺陷成因: 模型层 gorm:"default:300" 让 GORM 把显式 0 当"未设置"省略该列,
// 由 schema 默认值回填 300; 应用层默认值也被 gorm tag 隐式承担, 导致
// "0=不冷却"这一前端 (:min="0") 已承诺的语义根本存不进库。
//
// 变异自证: 把 models.AutomationRule.CooldownSec 的 tag 改回 gorm:"default:300"
// TestCreateAutomationRuleCooldownSecZeroPersists 必红 (读回 300)。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newAutomationCooldownRouter 装配仅含 automation-rules 的路由 (evaluator/planner/
// trigger/catalog 全 nil: 本文件只测 CRUD 契约, 不触碰求值器/执行链)。
func newAutomationCooldownRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{}); err != nil {
		t.Fatalf("auto migrate automation: %v", err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, nil, nil, nil, nil)
	return r, db
}

// automationRuleBody 造 time_window + notification 的合法创建体
// (与 cooldown 无关的字段全部走最简合法值, 保证红灯只可能来自 cooldown 语义)。
func automationRuleBody(t *testing.T, cooldown *int) []byte {
	t.Helper()
	body := map[string]interface{}{
		"name":                 "冷却语义契约",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionNotification,
		"action_level":         models.AlertLevelInfo,
	}
	if cooldown != nil {
		body["cooldown_sec"] = *cooldown
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return raw
}

func postAutomationRule(t *testing.T, r *gin.Engine, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// mockAutomationRule 是响应/读回用的最小结构 (只取本任务关心的列)。
type mockAutomationRule struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	CooldownSec int    `json:"cooldown_sec"`
}

func decodeAutomationRule(t *testing.T, raw []byte) mockAutomationRule {
	t.Helper()
	var resp struct {
		Code int                `json:"code"`
		Data mockAutomationRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("解析响应失败: %v (body=%s)", err, string(raw))
	}
	return resp.Data
}

// rawCooldownColumn 绕过 GORM 模型默认值, 直接读 DB 列 (证明写入结果而非内存值)。
func rawCooldownColumn(t *testing.T, db *gorm.DB, id uint) int {
	t.Helper()
	var row struct{ CooldownSec int }
	if err := db.Model(&models.AutomationRule{}).Select("cooldown_sec").
		Where("id = ?", id).Scan(&row).Error; err != nil {
		t.Fatalf("读 cooldown_sec 列失败: %v", err)
	}
	return row.CooldownSec
}

// 契约测试 (负债 D-5 核心断言): 显式 cooldown_sec=0 → 读回必须是 0, 不是 300。
// 旧行为下 POST 响应为 300 且 GET 读回 300, 本测试两条断言同时变红。
func TestCreateAutomationRuleCooldownSecZeroPersists(t *testing.T) {
	r, db := newAutomationCooldownRouter(t)

	code, body := postAutomationRule(t, r, automationRuleBody(t, intp(0)))
	if code != http.StatusOK {
		t.Fatalf("create with cooldown_sec=0 期望 200, got %d: %s", code, string(body))
	}
	created := decodeAutomationRule(t, body)
	if created.ID == 0 {
		t.Fatalf("响应缺少 id: %s", string(body))
	}
	if created.CooldownSec != 0 {
		t.Fatalf("POST 响应 cooldown_sec=%d, 期望 0 (显式零值被 gorm default 回填 — 负债 D-5)",
			created.CooldownSec)
	}
	if got := rawCooldownColumn(t, db, created.ID); got != 0 {
		t.Fatalf("DB 列 cooldown_sec=%d, 期望 0 (显式 0 根本没写进库)", got)
	}

	// GET 读回路径: 与 POST 响应同断言, 防止"响应正确但库里是 300"。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/automation-rules/1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET rule 期望 200, got %d: %s", rec.Code, rec.Body.String())
	}
	read := decodeAutomationRule(t, rec.Body.Bytes())
	if read.CooldownSec != 0 {
		t.Fatalf("GET 读回 cooldown_sec=%d, 期望 0", read.CooldownSec)
	}
}

// 回归保护: 未传 cooldown_sec → 应用层默认 300 (handler_automation.go:236),
// 不依赖 DB 默认值也能成立。
func TestCreateAutomationRuleCooldownSecDefaultsTo300(t *testing.T) {
	r, db := newAutomationCooldownRouter(t)

	code, body := postAutomationRule(t, r, automationRuleBody(t, nil))
	if code != http.StatusOK {
		t.Fatalf("create without cooldown_sec 期望 200, got %d: %s", code, string(body))
	}
	created := decodeAutomationRule(t, body)
	if created.CooldownSec != 300 {
		t.Fatalf("未传 cooldown_sec 时响应=%d, 期望应用层默认 300", created.CooldownSec)
	}
	if got := rawCooldownColumn(t, db, created.ID); got != 300 {
		t.Fatalf("未传 cooldown_sec 时 DB 列=%d, 期望 300", got)
	}
}

// update 路径 (updates map + Updates): 300 → 0 必须真正落库 0。
// map 更新不走 gorm 零值省略逻辑, 这条守护它不被改回 struct 赋值。
func TestUpdateAutomationRuleCooldownSecZeroPersists(t *testing.T) {
	r, db := newAutomationCooldownRouter(t)

	code, body := postAutomationRule(t, r, automationRuleBody(t, intp(300)))
	if code != http.StatusOK {
		t.Fatalf("create 期望 200, got %d: %s", code, string(body))
	}
	id := decodeAutomationRule(t, body).ID

	req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/1",
		bytes.NewReader([]byte(`{"cooldown_sec": 0}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update cooldown_sec=0 期望 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated := decodeAutomationRule(t, rec.Body.Bytes())
	if updated.CooldownSec != 0 {
		t.Fatalf("PUT 响应 cooldown_sec=%d, 期望 0", updated.CooldownSec)
	}
	if got := rawCooldownColumn(t, db, id); got != 0 {
		t.Fatalf("PUT 后 DB 列=%d, 期望 0 (update 路径被 gorm 默认值影响)", got)
	}
}
