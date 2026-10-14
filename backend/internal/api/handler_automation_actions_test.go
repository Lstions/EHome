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

// ─── P2-F: 镜像列口径两路必须一致 (create 与 update 同取 seq 最小) ─────────
//
// 反向验证: 把 create 路径的 mirrorAction(actions) 退回 actions[0] →
// TestMirrorColumnIsSeqMinOnCreateAndUpdate 红 (数组顺序与 seq 顺序不一致时,
// create 落库的镜像列与 update 重写后的镜像列不同)。
func TestMirrorColumnIsSeqMinOnCreateAndUpdate(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	// 请求体的数组顺序刻意与 seq 顺序**相反**: 数组第 0 项是 seq=2。
	// 若 create 取数组下标 0, 镜像列会被写成 seq=2 的动作 (read_rainfall);
	// 正确口径 (seq 最小) 应写成 seq=1 的动作 (set_mos_policy)。
	body := multiRiskBody(true, boolPtr(true), []map[string]interface{}{
		actionSpec(2, "read_rainfall", `{}`),
		actionSpec(1, "set_mos_policy", `{}`),
	})
	code, resp := postRule(t, r, body)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%v)", code, resp)
	}
	created := reloadRule(t, db, 1)
	if created.ActionID != "set_mos_policy" {
		t.Fatalf("create 后镜像列 action_id=%q want set_mos_policy (seq 最小的动作, P2-F)",
			created.ActionID)
	}

	// 同一集合再经 update 重放一次 (动作集合逐字段不变): 镜像列必须保持一致。
	replay, _ := json.Marshal(map[string]interface{}{
		"actions": []map[string]interface{}{
			actionSpec(2, "read_rainfall", `{}`),
			actionSpec(1, "set_mos_policy", `{}`),
		},
	})
	if code, resp := putRule(t, r, "1", replay); code != http.StatusOK {
		t.Fatalf("重放更新失败: status=%d body=%v", code, resp)
	}
	updated := reloadRule(t, db, 1)
	if updated.ActionID != created.ActionID ||
		updated.ActionDeviceID != created.ActionDeviceID ||
		updated.ActionParamsJSON != created.ActionParamsJSON {
		t.Fatalf("同集合 create/update 后镜像列必须一致: create=%q/%v, update=%q/%v",
			created.ActionID, created.ActionDeviceID, updated.ActionID, updated.ActionDeviceID)
	}
}

// P2-F 补充: 老前端"只改 action_id"的标量请求必须改到 seq 最小的那个动作,
// 而不是请求体数组里的第一个。
func TestScalarUpdateTargetsSeqMinAction(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiRiskBody(true, boolPtr(true), []map[string]interface{}{
		actionSpec(2, "read_rainfall", `{}`),
		actionSpec(1, "set_mos_policy", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	// 标量改 action_id (不带 actions 数组)。
	raw, _ := json.Marshal(map[string]interface{}{"action_id": "set_mos_policy_v2"})
	if code, resp := putRule(t, r, "1", raw); code != http.StatusOK {
		t.Fatalf("标量更新失败: status=%d body=%v", code, resp)
	}
	var actions []models.AutomationRuleAction
	if err := db.Order("seq ASC").Find(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 {
		t.Fatalf("子表行数=%d want 2 (标量更新不得丢掉其它动作)", len(actions))
	}
	if actions[0].Seq != 1 || actions[0].ActionID != "set_mos_policy_v2" {
		t.Fatalf("标量更新必须落在 seq 最小的动作上: %+v", actions[0])
	}
	if actions[1].Seq != 2 || actions[1].ActionID != "read_rainfall" {
		t.Fatalf("其余动作必须原样保留: %+v", actions[1])
	}
}

// ─── P1-D: 同请求"改动作集合 + risk_acknowledged=true" 必须显式 400 ───────
//
// 反向验证: 删掉 update 路径里的 changed && risk_acknowledged 判定 → 本用例红
// (HTTP 200, 且落库为"风险动作 + require_confirmed=false + 无授权")。
func TestUpdateActionChangeWithReAcknowledgementIsRejected(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	// 前置: require_confirmed=true 的风险动作规则 (走人工确认, 无需授权)。
	body := multiRiskBody(true, nil, []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}

	// 同一次请求: 改动作集合 + 声明已授权 + 免确认。
	conflict, _ := json.Marshal(map[string]interface{}{
		"actions": []map[string]interface{}{
			actionSpec(1, "set_mos_policy_v2", `{}`),
		},
		"risk_acknowledged": true,
		"require_confirmed": false,
	})
	code, resp := putRule(t, r, "1", conflict)
	if code != http.StatusBadRequest {
		t.Fatalf("改动作 + 同请求重新授权必须被显式拒绝 (P1-D), status=%d body=%v", code, resp)
	}
	if resp["error_code"] != "action_changed_requires_reconfirmation" {
		t.Fatalf("error_code=%v want action_changed_requires_reconfirmation", resp["error_code"])
	}
	// 被拒的请求整体不得落库: 动作原样保留, 且不得出现"免确认 + 无授权"的规则。
	after := reloadRule(t, db, 1)
	if after.ActionID != "set_mos_policy" {
		t.Fatalf("被拒的更新不得改动动作: action_id=%q", after.ActionID)
	}
	if !after.RequireConfirmed {
		t.Fatal("被拒的更新不得改动 require_confirmed")
	}
	if after.RiskAcknowledgedAt != nil {
		t.Fatal("被拒的更新不得写入授权列")
	}
}

// P1-D 的正向对照 1: 动作不变时携带 risk_acknowledged=true 仍须成功。
func TestUpdateUnchangedActionWithReAcknowledgementSucceeds(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiRiskBody(false, boolPtr(true), []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	// 显式撤回再重新授权 (动作集合逐字段不变) → 必须成功。
	raw, _ := json.Marshal(map[string]interface{}{"risk_acknowledged": true})
	if code, resp := putRule(t, r, "1", raw); code != http.StatusOK {
		t.Fatalf("动作未变时重新授权必须成功, status=%d body=%v", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("重新授权必须写入 risk_acknowledged_at")
	}
}

// P1-D 的正向对照 2: 只改名称时携带 risk_acknowledged=true 仍须成功。
func TestUpdateNameOnlyWithReAcknowledgementSucceeds(t *testing.T) {
	r, db := newRiskRouter(t, riskCatalogFixture())
	body := multiRiskBody(false, boolPtr(true), []map[string]interface{}{
		actionSpec(1, "set_mos_policy", `{}`),
	})
	if code, resp := postRule(t, r, body); code != http.StatusOK {
		t.Fatalf("前置规则创建失败: status=%d body=%v", code, resp)
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"name":              "改个名字",
		"risk_acknowledged": true,
	})
	if code, resp := putRule(t, r, "1", raw); code != http.StatusOK {
		t.Fatalf("仅改名称时重新授权必须成功, status=%d body=%v", code, resp)
	}
	if reloadRule(t, db, 1).RiskAcknowledgedAt == nil {
		t.Fatal("仅改名称不得清空授权, 且重新授权须写入")
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
