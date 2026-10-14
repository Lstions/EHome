package api

// 多动作写路径门禁: 双写兼容期 + 逐 seq 授权失效 (B1 显式债务的收口) + V7 逐动作。
//
// 反向验证 (注入 → 必须红):
//
//	① 把 actionSetChanged 退回"只比较第一个动作"(B1 原实现) →
//	   TestUpdateSecondActionChangeClearsAcknowledgement 红 (旧授权被保留);
//	② 把 validateRiskActionsAuthorization 退回"只看第一个动作" →
//	   TestCreateSecondRiskActionWithoutAuthorizationIsRejected 红 (200 而非 400);
//	③ 去掉 create 的子表双写 → TestCreatePersistsActionsSubtable 红。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

// multiRiskBody 造一条**多动作**规则请求体 (方案 §4.1.1)。
func multiRiskBody(requireConfirmed bool, acknowledged *bool, actions []map[string]interface{}) []byte {
	body := map[string]interface{}{
		"name":                 "多动作规则",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"actions":              actions,
		"require_confirmed":    requireConfirmed,
	}
	if acknowledged != nil {
		body["risk_acknowledged"] = *acknowledged
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
	body := multiRiskBody(true, boolPtr(true), []map[string]interface{}{
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
	// 双写兼容期: 镜像列 = seq 最小的动作 (老读路径行为不变)。
	rule := reloadRule(t, db, 1)
	if rule.ActionID != "set_mos_policy" {
		t.Fatalf("镜像列必须等于 seq 最小动作: action_id=%q params=%q", rule.ActionID, rule.ActionParamsJSON)
	}
	// 响应里带完整 actions 数组 (方案 §3.4 D4)。
	if _, ok := resp["data"]; !ok {
		t.Fatalf("响应缺少 data: %v", resp)
	}
}

// ② 逐 seq 授权失效 (B1 债务收口): 改**第 2 个**动作也必须清空 risk_acknowledged_at。
//
// B1 的实现只比较单动作四列 (第一个动作), 因此这条在改造前会绿 —— 即旧授权
// 会继续放行用户从未确认过的第 2 个动作 (安全漏洞)。
func TestUpdateSecondActionChangeClearsAcknowledgement(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	// require_confirmed=true 建规则并授权: "换动作"这一步不会被 V7 拦,
	// 能单独观测到"授权被清空"这一事实。
	body := multiRiskBody(true, boolPtr(true), []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
		actionSpec(2, "set_mos_policy_v2", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("前置条件失败: 规则应已授权")
	}

	// 只改第 2 个动作 (第 1 个逐字段不变) → 授权必须被清空。
	changeSecond, _ := json.Marshal(map[string]interface{}{
		"actions": []map[string]interface{}{
			actionSpec(1, "set_mos_policy", `{}`),
			actionSpec(2, "read_rainfall", `{}`),
		},
	})
	code, resp := putRule(t, r, "1", changeSecond)
	if code != http.StatusOK {
		t.Fatalf("require_confirmed=true 时换动作应被接受, status=%d body=%v", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt != nil {
		t.Fatal("第 2 个动作变化必须清空 risk_acknowledged_at —— 只比较第一个动作会保留旧授权 (安全漏洞)")
	}
}

// ③ 反向对照: 只改名称等非动作字段 → 授权不得被清空。
func TestUpdateNonActionFieldKeepsAcknowledgementWithActions(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiRiskBody(true, boolPtr(true), []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
		actionSpec(2, "read_rainfall", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	raw, _ := json.Marshal(map[string]interface{}{"name": "改个名字"})
	code, resp := putRule(t, r, "1", raw)
	if code != http.StatusOK {
		t.Fatalf("改名称不应被拒: status=%d body=%v", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("仅改名称不得清空授权")
	}
}

// ④ V7 必须逐动作: 第 1 个普通、第 2 个风险 + 未授权 → 拒绝保存。
// 只检查第一个动作的实现会让这条规则落库 (永远不会执行)。
func TestCreateSecondRiskActionWithoutAuthorizationIsRejected(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiRiskBody(false, nil, []map[string]interface{}{
		actionSpec(1, "read_rainfall", `{}`),
		actionSpec(2, "set_mos_policy", `{}`),
	})
	code, resp := postRule(t, r, body)
	if code != http.StatusBadRequest {
		t.Fatalf("第 2 个动作是风险动作且未授权, 必须 400; status=%d body=%v", code, resp)
	}
	if resp["error_code"] != "risk_action_requires_confirmation" {
		t.Fatalf("error_code=%v want risk_action_requires_confirmation", resp["error_code"])
	}
	var cnt int64
	db.Model(&models.AutomationRule{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("被拒的规则不得落库, 实际 %d 条", cnt)
	}
}

// ⑤ V1: seq 必须 1..N 连续无重复 (fail-closed)。
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
		code, resp := postRule(t, r, multiRiskBody(true, nil, tc.actions))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want 400 (body=%v)", tc.name, code, resp)
		}
	}
}
