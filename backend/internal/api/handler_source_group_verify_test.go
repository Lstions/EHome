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

	// 观察项 D-3：handler 注释声明 composite deactivate 也应 400；实测记录行为。
	w = doDSRequest(t, r, http.MethodPost,
		fmt.Sprintf("/api/v1/source-groups/%d/deactivate", created.ID), gin.H{"member_id": created.Members[0].ID})
	switch w.Code {
	case http.StatusBadRequest:
		t.Logf("composite deactivate 400（与 handler 注释一致）")
	case http.StatusOK:
		t.Logf("DEVIATION D-3: composite deactivate 返回 200（handler 注释声明应 400，领域层按 R8 直接 disabled）body=%s", w.Body.String())
	default:
		t.Logf("DEVIATION D-3: composite deactivate 返回 %d body=%s", w.Code, w.Body.String())
	}
}
