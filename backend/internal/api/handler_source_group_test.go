package api

// 来源组 API 契约测试（verifier 形状：{items,total,page,page_size} / 嵌套 members /
// active_member_id / composite 禁 active / V-5 400 / I6 409）。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newSourceGroupAPI 构造仅含来源组路由的真实 SQLite 引擎（复用 handler_test.go 鉴权）。
func newSourceGroupAPI(t *testing.T) (*gin.Engine, *gorm.DB, *datasource.GroupService) {
	t.Helper()
	db := setupTestDB(t)
	// setupTestDB 是共享夹具，不塞来源组表（避免影响其它域用例）；本测试自补。
	if err := db.AutoMigrate(&models.SourceGroup{}, &models.SourceGroupMember{},
		&models.SourceGroupFailoverLog{}, &models.SourceGroupHealth{},
		&models.SensorFieldObs{}, &models.EdgeDevice{}, &models.AlertRule{}, &models.AutomationRule{}); err != nil {
		t.Fatalf("migrate source group tables: %v", err)
	}
	gsvc := datasource.NewGroupService(db, datasource.Options{})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerSourceGroupRoutes(v1, db, gsvc)
	return r, db, gsvc
}

// addLightDevice 造一台已上报 illuminance 的边缘设备（V-5 通过）。
func addLightDevice(t *testing.T, db *gorm.DB, name string) models.EdgeDevice {
	t.Helper()
	ed := models.EdgeDevice{Name: name, NodeID: "node-1", ChannelID: 1, IntervalMs: 5000, Enabled: true}
	if err := db.Create(&ed).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	if err := db.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}).Error; err != nil {
		t.Fatalf("create sensor field obs: %v", err)
	}
	return ed
}

type sgEnvelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	ErrorCode string          `json:"error_code"`
	Data      json.RawMessage `json:"data"`
}

func decodeSG(t *testing.T, body []byte) sgEnvelope {
	t.Helper()
	var env sgEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	return env
}

type sgView struct {
	ID             uint   `json:"id"`
	Kind           string `json:"kind"`
	Category       string `json:"category"`
	Revision       int64  `json:"revision"`
	ActiveMemberID uint   `json:"active_member_id"`
	Members        []struct {
		ID           uint   `json:"id"`
		EdgeDeviceID uint   `json:"edge_device_id"`
		Status       string `json:"status"`
	} `json:"members"`
}

// 1) 创建主备组 + 列表形状（items/total/page/page_size）+ 嵌套 members/active_member_id。
func TestSourceGroupAPI_CreateAndListShape(t *testing.T) {
	r, db, _ := newSourceGroupAPI(t)
	d1 := addLightDevice(t, db, "light-1")
	d2 := addLightDevice(t, db, "light-2")

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"name": "光照主备", "kind": "primary_backup", "category": "illuminance",
		"members": []gin.H{
			{"edge_device_id": d1.ID, "priority": 100},
			{"edge_device_id": d2.ID, "priority": 50},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	env := decodeSG(t, w.Body.Bytes())
	var created sgView
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ActiveMemberID == 0 || len(created.Members) != 2 {
		t.Fatalf("主备组创建后应有 active_member_id 与 2 成员: %s", env.Data)
	}
	if created.Members[0].Status != models.SourceMemberStatusActive {
		t.Fatalf("priority 最高成员应为 active，got %s", created.Members[0].Status)
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/source-groups?page=1&page_size=10&kind=primary_backup&enabled=true", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Items    []sgView `json:"items"`
		Total    int64    `json:"total"`
		Page     int      `json:"page"`
		PageSize int      `json:"page_size"`
	}
	env = decodeSG(t, w.Body.Bytes())
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 1 || list.Page != 1 || list.PageSize != 10 || len(list.Items) != 1 {
		t.Fatalf("列表形状错误: %s", env.Data)
	}
}

// 2) composite：缺 combine 400；合法创建后 activate 必须 400（composite 无 active）。
func TestSourceGroupAPI_CompositeRules(t *testing.T) {
	r, db, _ := newSourceGroupAPI(t)
	d1 := addLightDevice(t, db, "light-1")
	d2 := addLightDevice(t, db, "light-2")

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"kind": "composite", "category": "illuminance",
		"members": []gin.H{{"edge_device_id": d1.ID}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("V-1 缺 combine 应 400，got %d body=%s", w.Code, w.Body.String())
	}

	w = doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"kind": "composite", "category": "illuminance", "combine": "avg",
		"members": []gin.H{{"edge_device_id": d1.ID}, {"edge_device_id": d2.ID}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("composite 创建 status=%d body=%s", w.Code, w.Body.String())
	}
	env := decodeSG(t, w.Body.Bytes())
	var created sgView
	_ = json.Unmarshal(env.Data, &created)
	if created.ActiveMemberID != 0 {
		t.Fatalf("composite 的 active_member_id 必须为 0，got %d", created.ActiveMemberID)
	}
	w = doDSRequest(t, r, http.MethodPost,
		"/api/v1/source-groups/"+strconv.FormatUint(uint64(created.ID), 10)+"/activate",
		gin.H{"member_id": created.Members[0].ID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("composite activate 应 400（V-6），got %d body=%s", w.Code, w.Body.String())
	}
}

// 3) V-5：成员设备未上报组类别 ⇒ 400 category_not_observed。
func TestSourceGroupAPI_MemberNotObserved(t *testing.T) {
	r, db, _ := newSourceGroupAPI(t)
	ed := models.EdgeDevice{Name: "no-obs", NodeID: "node-1", ChannelID: 1, Enabled: true}
	if err := db.Create(&ed).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"kind": "primary_backup", "category": "illuminance",
		"members": []gin.H{{"edge_device_id": ed.ID}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("V-5 应 400，got %d body=%s", w.Code, w.Body.String())
	}
	env := decodeSG(t, w.Body.Bytes())
	if env.ErrorCode != "category_not_observed" {
		t.Fatalf("V-5 应带 error_code=category_not_observed，got code=%q message=%q", env.ErrorCode, env.Message)
	}
}

// 4) I6：被规则引用的组删除 ⇒ 409。
func TestSourceGroupAPI_DeleteReferenced(t *testing.T) {
	r, db, _ := newSourceGroupAPI(t)
	d1 := addLightDevice(t, db, "light-1")
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/source-groups", gin.H{
		"kind": "primary_backup", "category": "illuminance",
		"members": []gin.H{{"edge_device_id": d1.ID}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	env := decodeSG(t, w.Body.Bytes())
	var created sgView
	_ = json.Unmarshal(env.Data, &created)
	rule := models.AlertRule{SensorName: "illuminance", SourceGroupID: &created.ID}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	w = doDSRequest(t, r, http.MethodDelete, "/api/v1/source-groups/"+strconv.FormatUint(uint64(created.ID), 10), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("被引用的组删除应 409（I6），got %d body=%s", w.Code, w.Body.String())
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
