package api

// 多动作写路径门禁（2026-11-18 清理：规则级授权已删除，这里只保留动作集合语义）。
//
// 反向验证（注入 → 必须红）:
//   - 去掉 create 的子表双写 → TestCreatePersistsActionsSubtable 红;
//   - 让 V1 seq 校验放行 → TestCreateRejectsNonContiguousActionSeq 红;
//   - 把风险动作判成"不能保存" → TestCreateRiskyActionWithoutAuthorizationSucceeds 红。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

func multiActionBody(requireConfirmed bool, actions []map[string]interface{}) []byte {
	body := map[string]interface{}{
		"name":                 "多动作规则",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"timezone":             "UTC",
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"actions":              actions,
		"require_confirmed":    requireConfirmed,
	}
	raw, _ := json.Marshal(body)
	return raw
}

func actionSpec(seq int, actionID string, params string) map[string]interface{} {
	return map[string]interface{}{
		"seq":                seq,
		"action_type":        models.AutomationActionDeviceAction,
		"action_device_id":   1,
		"action_id":          actionID,
		"action_params_json": params,
	}
}

func putRule(t *testing.T, r interface {
	ServeHTTP(w http.ResponseWriter, req *http.Request)
}, id string, body []byte) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/automation-rules/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var decoded map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec.Code, decoded
}

// ① 双写: 请求里的 actions 必须落子表, 且镜像列 = seq 最小的动作。
func TestCreatePersistsActionsSubtable(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiActionBody(false, []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
		actionSpec(2, "read_rainfall", `{}`),
	})
	code, resp := postRule(t, r, body)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%v)", code, resp)
	}
	var actions []models.AutomationRuleAction
	if err := db.Order("seq ASC").Find(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 {
		t.Fatalf("子表行数=%d want 2", len(actions))
	}
	if actions[0].Seq != 1 || actions[0].ActionID != "set_mos_policy" ||
		actions[1].Seq != 2 || actions[1].ActionID != "read_rainfall" {
		t.Fatalf("子表内容错误: %+v", actions)
	}
	if rule := reloadRule(t, db, 1); rule.ActionID != "set_mos_policy" {
		t.Fatalf("镜像列必须等于 seq 最小动作: action_id=%q params=%q", rule.ActionID, rule.ActionParamsJSON)
	}
	if _, ok := resp["data"]; !ok {
		t.Fatalf("响应缺少 data: %v", resp)
	}
}

// ② 2026-11-18: 风险动作不再需要任何"保存时授权" —— 直接可保存。
func TestCreateRiskyActionWithoutAuthorizationSucceeds(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiActionBody(false, []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
	})
	code, resp := postRule(t, r, body)
	if code != http.StatusOK {
		t.Fatalf("风险动作应当可以直接保存, status=%d body=%v", code, resp)
	}
	if got := reloadRule(t, db, 1); got.RequireConfirmed {
		t.Fatal("require_confirmed 应保持请求值 false")
	}
}

// ③ 改第 2 个动作为另一个风险动作: 直接保存（不再有授权失效/400 冲突）。
func TestUpdateSecondRiskActionSucceeds(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiActionBody(false, []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
		actionSpec(2, "read_rainfall", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	changeSecond, _ := json.Marshal(map[string]interface{}{
		"actions": []map[string]interface{}{
			actionSpec(1, "set_mos_policy", `{}`),
			actionSpec(2, "set_mos_policy_v2", `{}`),
		},
	})
	code, resp := putRule(t, r, "1", changeSecond)
	if code != http.StatusOK {
		t.Fatalf("改第 2 个动作应被接受, status=%d body=%v", code, resp)
	}
	var actions []models.AutomationRuleAction
	if err := db.Order("seq ASC").Find(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[1].ActionID != "set_mos_policy_v2" {
		t.Fatalf("第 2 个动作必须被替换: %+v", actions)
	}
}

// ④ V1: seq 必须 1..N 连续无重复 (fail-closed)。
func TestCreateRejectsNonContiguousActionSeq(t *testing.T) {
	r, _ := newRiskRouter(t, riskCatalogFixture())
	for _, tc := range []struct {
		name    string
		actions []map[string]interface{}
	}{
		{"seq 跳号", []map[string]interface{}{actionSpec(1, "read_rainfall", `{}`), actionSpec(3, "read_rainfall", `{}`)}},
		{"seq 重复", []map[string]interface{}{actionSpec(1, "read_rainfall", `{}`), actionSpec(1, "read_rainfall", `{}`)}},
		{"seq 从 0 起", []map[string]interface{}{actionSpec(0, "read_rainfall", `{}`)}},
		{"空数组", []map[string]interface{}{}},
	} {
		code, resp := postRule(t, r, multiActionBody(true, tc.actions))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want 400 (body=%v)", tc.name, code, resp)
		}
	}
}
