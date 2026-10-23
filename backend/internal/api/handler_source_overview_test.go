package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

// 本文件是来源总览端点的契约测试（数据流架构 v1 §4.1）。
//
// 最重要的一条在末尾：**/overview 不可写**（POST/PUT/DELETE ⇒ 405）。
// 只读语义必须由路由层保证，不能只靠前端不调用。

func overviewDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DataSource{}, &models.EdgeDevice{},
		&models.LogicalDevice{}, &models.SourceGroup{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func overviewRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSourceOverviewRoute(r.Group("/api/v1/data-sources"), db)
	return r
}

// TestSourceOverview_EmptyIsValid 验证空表时返回合法空结构（不是 null）。
//
// 为什么重要：前端拿到 null 会崩；空数组 + total=0 是「还没数据」的正确表达。
func TestSourceOverview_EmptyIsValid(t *testing.T) {
	r := overviewRouter(overviewDB(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Items []json.RawMessage `json:"items"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Data.Items == nil {
		t.Error("items 是 null，应为空数组（前端会崩）")
	}
	if body.Data.Total != 0 {
		t.Errorf("total = %d, want 0", body.Data.Total)
	}
}

// TestSourceOverview_GroupsByLogicalDeviceAndCategory 验证聚合语义。
//
// 三条同 (device, category) 的记录应聚合成**一个**来源，含 3 个成员。
func TestSourceOverview_GroupsByLogicalDeviceAndCategory(t *testing.T) {
	db := overviewDB(t)
	ld := models.LogicalDevice{ID: 14, IdentityKey: "k14", Name: "bms"}
	db.Create(&ld)

	// 三个成员设备供同一个 (逻辑设备 14, 类别 rsoc)
	for _, id := range []uint{1, 2, 3} {
		db.Create(&models.EdgeDevice{ID: id, Name: "d" + string(rune('0'+id)), Type: "t",
			NodeID: "n", Enabled: true, LogicalDeviceID: &ld.ID})
		db.Create(&models.DataSource{
			DeviceID: 14, Category: "rsoc", EdgeDeviceID: id,
			SourceType: "edge_device", Name: "x",
			Status:    datasource.StatusActive,
			ManagedBy: models.DataSourceManagedAuto,
		})
	}

	r := overviewRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))

	var body struct {
		Data struct {
			Items []struct {
				LogicalDeviceID   uint   `json:"logical_device_id"`
				LogicalDeviceName string `json:"logical_device_name"`
				Category          string `json:"category"`
				Kind              string `json:"kind"`
				MemberCount       int    `json:"member_count"`
				Members           []struct {
					EdgeDeviceID   uint   `json:"edge_device_id"`
					EdgeDeviceName string `json:"edge_device_name"`
					Active         bool   `json:"active"`
				} `json:"members"`
				Status string `json:"status"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Data.Total != 1 {
		t.Fatalf("total = %d, want 1（三条同 key 应聚合为一条来源）", body.Data.Total)
	}
	it := body.Data.Items[0]
	if it.LogicalDeviceID != 14 || it.Category != "rsoc" {
		t.Errorf("key = (%d,%s), want (14,rsoc)", it.LogicalDeviceID, it.Category)
	}
	if it.LogicalDeviceName != "bms" {
		t.Errorf("逻辑设备名 = %q, want bms", it.LogicalDeviceName)
	}
	if it.MemberCount != 3 {
		t.Errorf("member_count = %d, want 3", it.MemberCount)
	}
	if it.Kind != "direct" {
		t.Errorf("kind = %q, want direct（无来源组时为直连）", it.Kind)
	}
	if it.Status != "ok" {
		t.Errorf("status = %q, want ok（有 active 成员）", it.Status)
	}
	if it.Members[0].EdgeDeviceName == "" {
		t.Error("成员设备名为空 —— 名称批量查询失效")
	}
}

// TestSourceOverview_ReflectsSourceGroup 验证有来源组时 kind/group 反映组。
func TestSourceOverview_ReflectsSourceGroup(t *testing.T) {
	db := overviewDB(t)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d1", Type: "t", NodeID: "n", Enabled: true})
	db.Create(&models.DataSource{
		DeviceID: 10, Category: "v", EdgeDeviceID: 1, SourceType: "edge_device",
		Name: "x", Status: datasource.StatusActive, ManagedBy: models.DataSourceManagedAuto,
	})
	db.Create(&models.SourceGroup{
		ID: 5, Name: "我的主备组", Kind: "primary_backup", Category: "v", Enabled: true,
	})

	r := overviewRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))

	var body struct {
		Data struct {
			Items []struct {
				GroupID   uint    `json:"group_id"`
				GroupName *string `json:"group_name"`
				Kind      string  `json:"kind"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	it := body.Data.Items[0]
	if it.GroupID != 5 {
		t.Errorf("group_id = %d, want 5", it.GroupID)
	}
	if it.GroupName == nil || *it.GroupName != "我的主备组" {
		t.Errorf("group_name = %v, want 我的主备组", it.GroupName)
	}
	if it.Kind != "primary_backup" {
		t.Errorf("kind = %q, want primary_backup", it.Kind)
	}
}

// TestSourceOverview_ExcludesDisabled 验证 disabled 行不出现在总览里。
func TestSourceOverview_ExcludesDisabled(t *testing.T) {
	db := overviewDB(t)
	db.Create(&models.DataSource{
		DeviceID: 10, Category: "a", EdgeDeviceID: 1, SourceType: "edge_device",
		Name: "x", Status: datasource.StatusActive, ManagedBy: models.DataSourceManagedAuto,
	})
	db.Create(&models.DataSource{
		DeviceID: 10, Category: "b", EdgeDeviceID: 1, SourceType: "edge_device",
		Name: "y", Status: datasource.StatusDisabled, ManagedBy: models.DataSourceManagedAuto,
	})

	r := overviewRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/overview", nil))

	var body struct {
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Data.Total != 1 {
		t.Errorf("total = %d, want 1（disabled 行应被排除）", body.Data.Total)
	}
}

// TestSourceOverview_IsReadOnly 是**协议层只读断言**。
//
// 只注册 GET ⇒ 同路径的 POST/PUT/DELETE 必须是 405（Method Not Allowed）。
// 若有人后来加了写路由，本测试立刻红 —— 这是「数据源只读」需求的技术保证，
// 不依赖前端不调用、也不依赖运维自觉。
func TestSourceOverview_IsReadOnly(t *testing.T) {
	r := overviewRouter(overviewDB(t))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/data-sources/overview", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /overview = %d, want 405 —— 该端点必须只读", method, w.Code)
		}
	}
}

// TestSourceOverview_Filters 验证过滤参数。
func TestSourceOverview_Filters(t *testing.T) {
	db := overviewDB(t)
	db.Create(&models.DataSource{DeviceID: 10, Category: "a", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "x", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})
	db.Create(&models.DataSource{DeviceID: 20, Category: "b", EdgeDeviceID: 2,
		SourceType: "edge_device", Name: "y", Status: datasource.StatusActive,
		ManagedBy: models.DataSourceManagedAuto})

	r := overviewRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/data-sources/overview?category=a", nil))

	var body struct {
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Data.Total != 1 {
		t.Errorf("category 过滤后 total = %d, want 1", body.Data.Total)
	}
}
