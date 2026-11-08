package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 数据源主备 API 契约测试 (设计 §7) ====================

// newDataSourceAPI 用真实 SQLite 库构造数据源域路由的引擎。
//
// 2026-10-24（方案 A）：4 个手工运维端点（DELETE / activate / deactivate / reset）
// 已删除；GET 列表/详情/health、POST 创建、PUT 更新仍保留
// （见 handler_data_source.go:registerDataSourceRoutes 的说明）。
// setupTestDB/setupRouter/authHeader 来自 handler_test.go。
func newDataSourceAPI(t *testing.T) (*gin.Engine, *gorm.DB, *datasource.Service) {
	t.Helper()
	db := setupTestDB(t)
	svc := datasource.New(db, datasource.Options{})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataSourceRoutes(v1.Group("/data-sources"), svc, db)
	// failover-logs 路由 2026-10-23 迁到 /api/v1/edge-devices/:id（:id = 边缘设备 id）。
	registerFailoverLogRoutes(v1.Group("/edge-devices"), svc)
	return r, db, svc
}

// doDSRequest 发起带鉴权的请求；body 为 nil 时不带请求体。
func doDSRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// dsAssertCode 断言 HTTP 状态码与 envelope code 一致（契约不变量）。
func dsAssertCode(t *testing.T, w *httptest.ResponseRecorder, want int) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if w.Code != want {
		t.Fatalf("http status = %d want %d (body=%s)", w.Code, want, w.Body.String())
	}
	got, ok := resp["code"].(float64)
	if !ok || int(got) != want {
		t.Fatalf("envelope code = %v want %d (body=%s)", resp["code"], want, w.Body.String())
	}
	return resp
}

func dsDataMap(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data is not object: %T (%v)", resp["data"], resp["data"])
	}
	return data
}

func dsDataArray(t *testing.T, resp map[string]interface{}) []interface{} {
	t.Helper()
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("data is not array: %T (%v)", resp["data"], resp["data"])
	}
	return data
}

func seedDataSource(t *testing.T, svc *datasource.Service, in datasource.CreateInput) *models.DataSource {
	t.Helper()
	item, err := svc.Create(in)
	if err != nil {
		t.Fatalf("seed create %+v: %v", in, err)
	}
	return item
}

// ---------- GET /data-sources ----------

func TestDataSourceList_EmptyItemsIsArray(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources", nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	data := dsDataMap(t, resp)
	items, ok := data["items"].([]interface{})
	if !ok {
		t.Fatalf("empty items must be a JSON array, got %T (%v)", data["items"], data["items"])
	}
	if len(items) != 0 {
		t.Fatalf("empty items len = %d want 0", len(items))
	}
	if total, _ := data["total"].(float64); total != 0 {
		t.Fatalf("total = %v want 0", data["total"])
	}
}

func TestDataSourceList_PaginationAndFilters(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	// 组键 = (edge_device_id, category) 且有唯一索引 ⇒ 三条来源必须两两不同键。
	seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1, Priority: 10})
	seedDataSource(t, svc, datasource.CreateInput{Category: "humidity", EdgeDeviceID: 2, Priority: 5})
	third := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 3, Priority: 1})
	if err := db.Model(&models.DataSource{}).Where("id = ?", third.ID).Update("status", datasource.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}

	count := func(path string) int {
		t.Helper()
		w := doDSRequest(t, r, http.MethodGet, path, nil)
		resp := dsAssertCode(t, w, http.StatusOK)
		return len(dsDataMap(t, resp)["items"].([]interface{}))
	}

	if got := count("/api/v1/data-sources"); got != 3 {
		t.Fatalf("all items = %d want 3", got)
	}
	if got := count("/api/v1/data-sources?edge_device_id=1"); got != 1 {
		t.Fatalf("edge_device_id=1 items = %d want 1（组键含设备, 每设备每类别至多一行）", got)
	}
	if got := count("/api/v1/data-sources?category=humidity"); got != 1 {
		t.Fatalf("category=humidity items = %d want 1", got)
	}
	if got := count("/api/v1/data-sources?status=disabled"); got != 1 {
		t.Fatalf("status=disabled items = %d want 1", got)
	}

	// 分页：page_size=1 → 1 条、total 不变。
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources?page=1&page_size=1", nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	data := dsDataMap(t, resp)
	if got := len(data["items"].([]interface{})); got != 1 {
		t.Fatalf("page_size=1 items = %d want 1", got)
	}
	if total, _ := data["total"].(float64); total != 3 {
		t.Fatalf("paged total = %v want 3", data["total"])
	}

	// page/page_size 越界 clamp 不报错。
	w = doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources?page=0&page_size=100000", nil)
	dsAssertCode(t, w, http.StatusOK)
}

func TestDataSourceList_InvalidEdgeDeviceID(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources?edge_device_id=abc", nil)
	dsAssertCode(t, w, http.StatusBadRequest)
}

// ---------- GET /data-sources/:id ----------

func TestDataSourceGet(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1})

	w := doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	if int(dsDataMap(t, resp)["id"].(float64)) != int(item.ID) {
		t.Fatalf("get returned wrong id: %v", dsDataMap(t, resp)["id"])
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources/9999", nil)
	dsAssertCode(t, w, http.StatusNotFound)

	for _, p := range []string{"abc", "0", "-1"} {
		w = doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources/"+p, nil)
		dsAssertCode(t, w, http.StatusBadRequest)
	}
}

// ---------- POST /data-sources ----------

func TestDataSourceCreate_FirstIsActiveAndDuplicateConflict(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)

	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 1, "category": "temperature", "edge_device_id": 7,
	})
	resp := dsAssertCode(t, w, http.StatusCreated)
	data := dsDataMap(t, resp)
	if data["status"] != datasource.StatusActive {
		t.Fatalf("first source status = %v want active", data["status"])
	}
	if int(data["max_fail_count"].(float64)) != 3 {
		t.Fatalf("default max_fail_count = %v want 3", data["max_fail_count"])
	}

	w = doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 1, "category": "temperature", "edge_device_id": 8,
	})
	resp = dsAssertCode(t, w, http.StatusCreated)
	// 组键含设备 ⇒ 不同 edge_device 是**各自组的首行**，因此也是 active。
	// 「第二行是 standby」的前提（同组多成员）在唯一索引下已不可构造。
	if dsDataMap(t, resp)["status"] != datasource.StatusActive {
		t.Fatalf("second source status = %v want active（不同 edge_device 是独立组键的首行）", dsDataMap(t, resp)["status"])
	}

	// 同组同 edge_device 重复 → 409
	w = doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 1, "category": "temperature", "edge_device_id": 7,
	})
	dsAssertCode(t, w, http.StatusConflict)
}

func TestDataSourceCreate_RequiredAndEnumValidation(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)
	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing category", map[string]interface{}{"device_id": 1, "edge_device_id": 1}},
		{"missing edge_device_id", map[string]interface{}{"device_id": 1, "category": "t"}},
		{"invalid source_type", map[string]interface{}{"device_id": 1, "category": "t", "edge_device_id": 1, "source_type": "mqtt"}},
		{"max_fail_count too high", map[string]interface{}{"device_id": 1, "category": "t", "edge_device_id": 1, "max_fail_count": 21}},
		{"max_fail_count too low", map[string]interface{}{"device_id": 1, "category": "t", "edge_device_id": 1, "max_fail_count": -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", tc.body)
			dsAssertCode(t, w, http.StatusBadRequest)
		})
	}
}

func TestDataSourceCreate_IgnoresInjectedRuntimeFields(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"device_id": 1, "category": "temperature", "edge_device_id": 1,
		"id": 12345, "status": datasource.StatusDisabled, "fail_count": 99,
	})
	resp := dsAssertCode(t, w, http.StatusCreated)
	data := dsDataMap(t, resp)
	if data["status"] != datasource.StatusActive {
		t.Fatalf("status was injected: %v", data["status"])
	}
	if int(data["fail_count"].(float64)) != 0 {
		t.Fatalf("fail_count was injected: %v", data["fail_count"])
	}
	if int(data["id"].(float64)) == 12345 {
		t.Fatalf("id was injected: %v", data["id"])
	}
}

// ---------- PUT /data-sources/:id ----------

func TestDataSourceUpdate_WhitelistAndGroupPreserved(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{
		Category: "temperature", EdgeDeviceID: 1,
		Name: "old", Description: "old", Priority: 1, MaxFailCount: 3, Config: "{}",
	})

	body := map[string]interface{}{
		"name":           "new",
		"description":    "new",
		"priority":       9,
		"is_primary":     true,
		"max_fail_count": 5,
		"config":         `{"k":1}`,
		// 白名单外：必须被忽略
		"device_id":      999,
		"category":       "hacked",
		"edge_device_id": 999,
		"source_type":    "external",
		"status":         datasource.StatusDisabled,
		"fail_count":     77,
	}
	w := doDSRequest(t, r, http.MethodPut, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), body)
	resp := dsAssertCode(t, w, http.StatusOK)
	data := dsDataMap(t, resp)
	if data["name"] != "new" || data["priority"].(float64) != 9 || data["is_primary"] != true {
		t.Fatalf("whitelist fields not applied: %v", data)
	}

	var got models.DataSource
	if err := db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Category != "temperature" || got.EdgeDeviceID != 1 {
		t.Fatalf("group key was injected: %+v", got)
	}
	if got.SourceType != datasource.SourceTypeEdgeDevice || got.Status != datasource.StatusActive || got.FailCount != 0 {
		t.Fatalf("runtime fields were injected: %+v", got)
	}
	if got.Name != "new" || got.Description != "new" || got.Priority != 9 || !got.IsPrimary ||
		got.MaxFailCount != 5 || got.Config != `{"k":1}` {
		t.Fatalf("whitelist fields not persisted: %+v", got)
	}
}

// TestDataSourceUpdate_PointerZeroValuesApplied 锁「指针字段显式传 0 必须生效」。
//
// 2026-10-24 起 **is_primary 不再属于可写字段**：它是 status 的派生量
// （恒等于 status==active），本用例因此改为断言「传 false 被忽略」。
// 保留在自己的用例里而不是删掉，因为「is_primary 传 false 不生效」这件事
// 本身就是不变量的一部分，需要有门禁。
func TestDataSourceUpdate_PointerZeroValuesApplied(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{
		Category: "temperature", EdgeDeviceID: 1, Priority: 5, IsPrimary: true, MaxFailCount: 7,
	})
	w := doDSRequest(t, r, http.MethodPut, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), map[string]interface{}{
		"priority": 0, "max_fail_count": 1,
		// 显式传 false：必须被忽略（status 仍是 active ⇒ is_primary 恒 true）。
		"is_primary": false,
	})
	dsAssertCode(t, w, http.StatusOK)
	var got models.DataSource
	db.First(&got, item.ID)
	if got.Priority != 0 || got.MaxFailCount != 1 {
		t.Fatalf("pointer zero values not applied: %+v", got)
	}
	if got.Status != datasource.StatusActive || !got.IsPrimary {
		t.Fatalf("is_primary 必须派生自 status：status=%s is_primary=%v（期望 active/true）",
			got.Status, got.IsPrimary)
	}
}

func TestDataSourceUpdate_NotFoundAndValidation(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodPut, "/api/v1/data-sources/9999", map[string]interface{}{"name": "x"})
	dsAssertCode(t, w, http.StatusNotFound)

	item := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1})
	w = doDSRequest(t, r, http.MethodPut, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), map[string]interface{}{"max_fail_count": 21})
	dsAssertCode(t, w, http.StatusBadRequest)

	w = doDSRequest(t, r, http.MethodPut, "/api/v1/data-sources/abc", map[string]interface{}{"name": "x"})
	dsAssertCode(t, w, http.StatusBadRequest)
}

// ---------- 已删除的写端点（2026-10-24 方案 A 的回归门禁） ----------

// TestDataSourceWriteEndpointsRemoved 钉住：四个手工运维端点确实**不再存在**。
//
// 为什么需要这条门禁（而不是只删测试）：删掉 handler 后，如果哪天有人「顺手」
// 把路由加回来，不会有人注意到 —— 因为这些端点在生产上是恒失败或恒被投影器覆盖的，
// 加回来也不会立刻报错，只会重新变成「看起来能用、永远失败」的陷阱。
//
// 期望 404（不是 405）：路由整条不存在，gin 直接走 NoRoute。
func TestDataSourceWriteEndpointsRemoved(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1})

	cases := []struct{ method, path string }{
		{http.MethodDelete, fmt.Sprintf("/api/v1/data-sources/%d", item.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/activate", item.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/deactivate", item.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/reset", item.ID)},
	}
	for _, c := range cases {
		w := doDSRequest(t, r, c.method, c.path, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404（该端点已按方案 A 删除；恢复它须先解决「组键二元化后无 standby 候选」的语义问题）",
				c.method, c.path, w.Code)
		}
	}

	// 保留的端点必须仍然可用（别把有效的也一起删了）。
	// POST/PUT 的完整行为由上面的 Create/Update 用例覆盖，这里只确认路由在位。
	if w := doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources", nil); w.Code != http.StatusOK {
		t.Fatalf("GET /data-sources = %d, want 200（只读端点必须保留）", w.Code)
	}
	if w := doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources", map[string]interface{}{
		"edge_device_id": 7, "category": "voltage",
	}); w.Code != http.StatusCreated {
		t.Fatalf("POST /data-sources = %d, want 201（创建端点保留：供数据修正）", w.Code)
	}
}

// ---------- GET /data-sources/:id/health ----------

func TestDataSourceHealth_EmptyArrayLimitClamp(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1})

	// 空集 → []
	w := doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d/health", item.ID), nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 0 {
		t.Fatalf("empty health len = %d want 0", len(arr))
	}

	rows := make([]models.DataSourceHealth, 205)
	for i := range rows {
		rows[i] = models.DataSourceHealth{
			SourceID: item.ID, DeviceID: 1, Category: "temperature",
			Status: "failure", Message: fmt.Sprintf("event-%d", i),
		}
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed health rows: %v", err)
	}

	// 默认 limit = 50
	w = doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d/health", item.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if got := len(dsDataArray(t, resp)); got != 50 {
		t.Fatalf("default health limit len = %d want 50", got)
	}

	// limit=7 → 7 且 id DESC
	w = doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d/health?limit=7", item.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	arr := dsDataArray(t, resp)
	if len(arr) != 7 {
		t.Fatalf("health limit=7 len = %d want 7", len(arr))
	}
	if firstID := int(arr[0].(map[string]interface{})["id"].(float64)); firstID != 205 {
		t.Fatalf("health not id DESC: first id = %d want 205", firstID)
	}

	// limit 上限 clamp 到 200
	w = doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d/health?limit=100000", item.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if got := len(dsDataArray(t, resp)); got != 200 {
		t.Fatalf("health clamp len = %d want 200", got)
	}

	// 非法 limit → 默认 50，不报错
	w = doDSRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/data-sources/%d/health?limit=abc", item.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if got := len(dsDataArray(t, resp)); got != 50 {
		t.Fatalf("invalid limit len = %d want 50", got)
	}
}

// ---------- GET /edge-devices/:id/failover-logs ----------

func TestFailoverLogs_ArrayEmptyAndCategoryFilter(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)

	// 空集 → []
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/edge-devices/1/failover-logs", nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 0 {
		t.Fatalf("empty failover logs len = %d want 0", len(arr))
	}

	// 造一条切换日志。
	//
	// 2026-10-24（方案 A）：原先这里调 svc.Activate 让它顺手写日志，但 Activate 已删
	// （组域通知/日志现在由 GroupService 产生）。本用例的对象是 **GET 只读端点**，
	// 因此直接插入日志行即可 —— 反而更无依赖、更能独立验证 DTO 映射。
	// 日志里的 device_id 列值 = 该行的 edge_device_id。
	ds := seedDataSource(t, svc, datasource.CreateInput{Category: "temperature", EdgeDeviceID: 1, Priority: 5})
	if err := db.Create(&models.FailoverLog{
		DeviceID:     ds.EdgeDeviceID,
		Category:     ds.Category,
		FromSourceID: 0, // 0 = 无前任（组内首条）
		ToSourceID:   ds.ID,
		Reason:       datasource.ReasonManual,
		CreatedAt:    time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed failover log: %v", err)
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/edge-devices/1/failover-logs", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	arr := dsDataArray(t, resp)
	if len(arr) != 1 {
		t.Fatalf("failover logs len = %d want 1", len(arr))
	}
	entry := arr[0].(map[string]interface{})
	if entry["category"] != "temperature" || int(entry["to_source_id"].(float64)) != int(ds.ID) {
		t.Fatalf("unexpected failover entry: %v", entry)
	}
	// API 层 DTO 映射: 模型的 device_id 列对外暴露为 edge_device_id (契约 §6)。
	if got, ok := entry["edge_device_id"].(float64); !ok || uint(got) != ds.EdgeDeviceID {
		t.Fatalf("edge_device_id = %v want %d (device_id 列名是历史命名, 取值是边缘设备 id)",
			entry["edge_device_id"], ds.EdgeDeviceID)
	}
	if _, legacy := entry["device_id"]; legacy {
		t.Fatalf("响应不得再出现 device_id 字段（逻辑设备 id 时代的命名）: %v", entry)
	}

	// category 过滤生效
	w = doDSRequest(t, r, http.MethodGet, "/api/v1/edge-devices/1/failover-logs?category=humidity", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 0 {
		t.Fatalf("category=humidity logs len = %d want 0", len(arr))
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/edge-devices/1/failover-logs?category=temperature", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 1 {
		t.Fatalf("category=temperature logs len = %d want 1", len(arr))
	}

	// 非法设备 id → 400
	w = doDSRequest(t, r, http.MethodGet, "/api/v1/edge-devices/abc/failover-logs", nil)
	dsAssertCode(t, w, http.StatusBadRequest)
}
