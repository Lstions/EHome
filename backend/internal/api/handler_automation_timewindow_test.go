package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

// D7：time_window 必须带时区（否则执行侧 fail-closed 跳过，规则静默不执行）。
func TestCreateTimeWindowRequiresTimezone(t *testing.T) {
	r, _ := newSunRouter(t)
	base := map[string]interface{}{
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionNotification,
		"action_level":         models.AlertLevelInfo,
	}
	withName := func(extra map[string]interface{}) []byte {
		body := map[string]interface{}{"name": "窗口规则"}
		for k, v := range base {
			body[k] = v
		}
		for k, v := range extra {
			body[k] = v
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	code, resp := postRule(t, r, withName(nil))
	if code != http.StatusBadRequest {
		t.Fatalf("缺时区必须 400, got %d body=%v", code, resp)
	}

	code, resp = postRule(t, r, withName(map[string]interface{}{"timezone": "Not/AZone"}))
	if code != http.StatusBadRequest {
		t.Fatalf("非法时区必须 400, got %d body=%v", code, resp)
	}

	code, resp = postRule(t, r, withName(map[string]interface{}{"timezone": "Asia/Shanghai"}))
	if code != http.StatusOK {
		t.Fatalf("合法时区应 200, got %d body=%v", code, resp)
	}
}

// D16：列表接口有硬上限，避免 O(N) 计算与无界响应体。
func TestListAutomationRulesCapped(t *testing.T) {
	r, db := newSunRouter(t)
	for i := 0; i < automationRulesMaxPage+5; i++ {
		rule := models.AutomationRule{
			Name: fmt.Sprintf("r-%d", i), Enabled: true,
			TriggerType:        models.AutomationTriggerTimeWindow,
			TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
			TriggerWindowEdge: models.AutomationWindowEnter,
			ActionType:        models.AutomationActionNotification, ActionLevel: "info",
		}
		if err := db.Create(&rule).Error; err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/automation-rules", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var decoded struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(decoded.Data) != automationRulesMaxPage {
		t.Fatalf("返回 %d 条, want %d（列表必须有硬上限）", len(decoded.Data), automationRulesMaxPage)
	}
}
