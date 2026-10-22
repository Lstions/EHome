package api

// 独立验证（impl-core）：composite 禁 active 的 API 契约（V-6/I5），以及
// 「API 拒绝 vs 领域层行为」一致性观察。与 ds-analyst 的 handler 测试相互独立。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestVerifySourceGroupAPI_CompositeActiveForbidden(t *testing.T) {
	r, db, _ := newSourceGroupAPI(t)
	d1 := addLightDevice(t, db, "verify-light-1")
	d2 := addLightDevice(t, db, "verify-light-2")

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"name": "verify-composite", "kind": "composite", "category": "illuminance", "combine": "avg",
		"members": []gin.H{
			{"edge_device_id": d1.ID, "priority": 100},
			{"edge_device_id": d2.ID, "priority": 90},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("composite 创建 status=%d body=%s", w.Code, w.Body.String())
	}
	env := decodeSG(t, w.Body.Bytes())
	var created sgView
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ActiveMemberID != 0 {
		t.Fatalf("composite active_member_id 必须 0，got %d", created.ActiveMemberID)
	}
	if len(created.Members) != 2 {
		t.Fatalf("members = %d want 2", len(created.Members))
	}
	for _, m := range created.Members {
		if m.Status == "active" {
			t.Fatalf("composite 成员不得为 active（I5）: %+v", m)
		}
	}

	// V-6：composite 组 activate 必须 400。
	w = doDSRequest(t, r, http.MethodPost,
		fmt.Sprintf("/api/v1/source-groups/%d/activate", created.ID), gin.H{"member_id": created.Members[0].ID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("composite activate 必须 400（V-6），got %d body=%s", w.Code, w.Body.String())
	}

	// F-6 裁决（2026-10-22）：composite 成员显式 deactivate 允许（R8 → disabled），
	// 只有 activate 一律 400；handler 注释已同步更正。
	w = doDSRequest(t, r, http.MethodPost,
		fmt.Sprintf("/api/v1/source-groups/%d/deactivate", created.ID), gin.H{"member_id": created.Members[0].ID})
	if w.Code != http.StatusOK {
		t.Fatalf("composite deactivate 应 200（F-6 裁决），got %d body=%s", w.Code, w.Body.String())
	}
	env = decodeSG(t, w.Body.Bytes())
	var after sgView
	if err := json.Unmarshal(env.Data, &after); err != nil {
		t.Fatalf("decode after deactivate: %v", err)
	}
	if after.ActiveMemberID != 0 {
		t.Fatalf("composite active_member_id 恒 0，got %d", after.ActiveMemberID)
	}
	foundDisabled := false
	for _, m := range after.Members {
		if m.ID == created.Members[0].ID && m.Status == "disabled" {
			foundDisabled = true
		}
		if m.Status == "active" {
			t.Fatalf("composite 成员不得为 active: %+v", m)
		}
	}
	if !foundDisabled {
		t.Fatalf("deactivate 目标成员应为 disabled: %s", env.Data)
	}
}
