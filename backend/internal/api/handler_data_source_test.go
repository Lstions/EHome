package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 数据源主备 API 契约测试 (设计 §7) ====================

// newDataSourceAPI 用真实 SQLite 库构造包含数据源域全部 10 条路由的引擎。
// setupTestDB/setupRouter/authHeader 来自 handler_test.go。
func newDataSourceAPI(t *testing.T) (*gin.Engine, *gorm.DB, *datasource.Service) {
	t.Helper()
	db := setupTestDB(t)
	svc := datasource.New(db, datasource.Options{})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataSourceRoutes(v1.Group("/data-sources"), svc)
	registerFailoverLogRoutes(v1, svc)
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
	seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 10})
	seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "humidity", EdgeDeviceID: 2, Priority: 5})
	third := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 2, Category: "temperature", EdgeDeviceID: 3, Priority: 1})
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
	if got := count("/api/v1/data-sources?device_id=1"); got != 2 {
		t.Fatalf("device_id=1 items = %d want 2", got)
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

func TestDataSourceList_InvalidDeviceID(t *testing.T) {
	r, _, _ := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/data-sources?device_id=abc", nil)
	dsAssertCode(t, w, http.StatusBadRequest)
}

// ---------- GET /data-sources/:id ----------

func TestDataSourceGet(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})

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
	if dsDataMap(t, resp)["status"] != datasource.StatusStandby {
		t.Fatalf("second source status = %v want standby", dsDataMap(t, resp)["status"])
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
		{"missing device_id", map[string]interface{}{"category": "t", "edge_device_id": 1}},
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
		DeviceID: 1, Category: "temperature", EdgeDeviceID: 1,
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
	if got.DeviceID != 1 || got.Category != "temperature" || got.EdgeDeviceID != 1 {
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

func TestDataSourceUpdate_PointerZeroValuesApplied(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{
		DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 5, IsPrimary: true, MaxFailCount: 7,
	})
	w := doDSRequest(t, r, http.MethodPut, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), map[string]interface{}{
		"priority": 0, "is_primary": false, "max_fail_count": 1,
	})
	dsAssertCode(t, w, http.StatusOK)
	var got models.DataSource
	db.First(&got, item.ID)
	if got.Priority != 0 || got.IsPrimary || got.MaxFailCount != 1 {
		t.Fatalf("pointer zero values not applied: %+v", got)
	}
}

func TestDataSourceUpdate_NotFoundAndValidation(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodPut, "/api/v1/data-sources/9999", map[string]interface{}{"name": "x"})
	dsAssertCode(t, w, http.StatusNotFound)

	item := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	w = doDSRequest(t, r, http.MethodPut, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), map[string]interface{}{"max_fail_count": 21})
	dsAssertCode(t, w, http.StatusBadRequest)

	w = doDSRequest(t, r, http.MethodPut, "/api/v1/data-sources/abc", map[string]interface{}{"name": "x"})
	dsAssertCode(t, w, http.StatusBadRequest)
}

// ---------- DELETE /data-sources/:id ----------

func TestDataSourceDelete_NotFoundAndSuccess(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	w := doDSRequest(t, r, http.MethodDelete, "/api/v1/data-sources/9999", nil)
	dsAssertCode(t, w, http.StatusNotFound)

	item := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	w = doDSRequest(t, r, http.MethodDelete, fmt.Sprintf("/api/v1/data-sources/%d", item.ID), nil)
	dsAssertCode(t, w, http.StatusOK)
	var n int64
	db.Model(&models.DataSource{}).Where("id = ?", item.ID).Count(&n)
	if n != 0 {
		t.Fatalf("source still present after delete")
	}
}

func TestDataSourceDelete_ActivePromotesCandidate(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	active := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 5})
	standby := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 1})

	w := doDSRequest(t, r, http.MethodDelete, fmt.Sprintf("/api/v1/data-sources/%d", active.ID), nil)
	dsAssertCode(t, w, http.StatusOK)

	var got models.DataSource
	if err := db.First(&got, standby.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != datasource.StatusActive {
		t.Fatalf("candidate status = %s want active", got.Status)
	}
	var activeCount int64
	db.Model(&models.DataSource{}).
		Where("device_id = ? AND category = ? AND status = ?", 1, "temperature", datasource.StatusActive).
		Count(&activeCount)
	if activeCount != 1 {
		t.Fatalf("active count = %d want 1", activeCount)
	}
}

// ---------- POST /data-sources/:id/activate ----------

func TestDataSourceActivate(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	a := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 5})
	b := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 1})

	w := doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/activate", b.ID), nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	data := dsDataMap(t, resp)
	if data["status"] != datasource.StatusActive {
		t.Fatalf("activate response status = %v want active", data["status"])
	}
	if int(data["id"].(float64)) != int(b.ID) {
		t.Fatalf("activate returned source %v want %d", data["id"], b.ID)
	}
	var old, nw models.DataSource
	db.First(&old, a.ID)
	db.First(&nw, b.ID)
	if old.Status != datasource.StatusStandby || nw.Status != datasource.StatusActive {
		t.Fatalf("after activate old=%s new=%s", old.Status, nw.Status)
	}

	w = doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources/9999/activate", nil)
	dsAssertCode(t, w, http.StatusNotFound)

	// disabled → 允许 activate（= 重新启用，R7 v1.1 修订）
	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).Update("status", datasource.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	w = doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/activate", a.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if dsDataMap(t, resp)["status"] != datasource.StatusActive {
		t.Fatalf("activate disabled status = %v want active", dsDataMap(t, resp)["status"])
	}
	var reA, reB models.DataSource
	db.First(&reA, a.ID)
	db.First(&reB, b.ID)
	if reA.Status != datasource.StatusActive || reB.Status != datasource.StatusStandby {
		t.Fatalf("after re-enable a=%s b=%s want active/standby", reA.Status, reB.Status)
	}
}

// ---------- POST /data-sources/:id/deactivate ----------

func TestDataSourceDeactivate(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	// active 且无候选 → 409 (R6)
	only := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	w := doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/deactivate", only.ID), nil)
	dsAssertCode(t, w, http.StatusConflict)
	var still models.DataSource
	db.First(&still, only.ID)
	if still.Status != datasource.StatusActive {
		t.Fatalf("409 must not mutate status, got %s", still.Status)
	}

	// active 且有候选 → 候选接替 (R5)
	a := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 2, Category: "temperature", EdgeDeviceID: 1})
	b := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 2, Category: "temperature", EdgeDeviceID: 2})
	w = doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/deactivate", a.ID), nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	if dsDataMap(t, resp)["status"] != datasource.StatusDisabled {
		t.Fatalf("deactivate response status = %v want disabled", dsDataMap(t, resp)["status"])
	}
	var rb models.DataSource
	db.First(&rb, b.ID)
	if rb.Status != datasource.StatusActive {
		t.Fatalf("candidate status = %s want active", rb.Status)
	}

	// standby → disabled (R8)
	_ = seedDataSource(t, svc, datasource.CreateInput{DeviceID: 3, Category: "temperature", EdgeDeviceID: 1})
	d := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 3, Category: "temperature", EdgeDeviceID: 2})
	w = doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/deactivate", d.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if dsDataMap(t, resp)["status"] != datasource.StatusDisabled {
		t.Fatalf("standby deactivate status = %v want disabled", dsDataMap(t, resp)["status"])
	}

	w = doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources/9999/deactivate", nil)
	dsAssertCode(t, w, http.StatusNotFound)
}

// ---------- POST /data-sources/:id/reset ----------

func TestDataSourceReset(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	// 组 1：存在 active 时 error → standby
	a := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2})
	if err := db.Model(&models.DataSource{}).Where("id = ?", b.ID).
		Updates(map[string]interface{}{"status": datasource.StatusError, "fail_count": 2}).Error; err != nil {
		t.Fatal(err)
	}

	// 非 error → 400
	w := doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/reset", a.ID), nil)
	dsAssertCode(t, w, http.StatusBadRequest)

	w = doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/reset", b.ID), nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	data := dsDataMap(t, resp)
	if data["status"] != datasource.StatusStandby {
		t.Fatalf("reset with active present status = %v want standby", data["status"])
	}
	if int(data["fail_count"].(float64)) != 0 {
		t.Fatalf("reset fail_count = %v want 0", data["fail_count"])
	}

	// 组 2：无 active 时 error → active
	only := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 2, Category: "temperature", EdgeDeviceID: 1})
	if err := db.Model(&models.DataSource{}).Where("id = ?", only.ID).
		Updates(map[string]interface{}{"status": datasource.StatusError, "fail_count": 1}).Error; err != nil {
		t.Fatal(err)
	}
	w = doDSRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/data-sources/%d/reset", only.ID), nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if dsDataMap(t, resp)["status"] != datasource.StatusActive {
		t.Fatalf("reset without active status = %v want active", dsDataMap(t, resp)["status"])
	}

	w = doDSRequest(t, r, http.MethodPost, "/api/v1/data-sources/9999/reset", nil)
	dsAssertCode(t, w, http.StatusNotFound)
}

// ---------- GET /data-sources/:id/health ----------

func TestDataSourceHealth_EmptyArrayLimitClamp(t *testing.T) {
	r, db, svc := newDataSourceAPI(t)
	item := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})

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

// ---------- GET /devices/:id/failover-logs ----------

func TestFailoverLogs_ArrayEmptyAndCategoryFilter(t *testing.T) {
	r, _, svc := newDataSourceAPI(t)

	// 空集 → []
	w := doDSRequest(t, r, http.MethodGet, "/api/v1/devices/1/failover-logs", nil)
	resp := dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 0 {
		t.Fatalf("empty failover logs len = %d want 0", len(arr))
	}

	// 用 svc 制造一次手动切换 (activate 第二条)。
	seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 5})
	b := seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 1})
	seedDataSource(t, svc, datasource.CreateInput{DeviceID: 1, Category: "humidity", EdgeDeviceID: 3})
	if _, err := svc.Activate(b.ID); err != nil {
		t.Fatalf("activate to produce failover log: %v", err)
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/devices/1/failover-logs", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	arr := dsDataArray(t, resp)
	if len(arr) != 1 {
		t.Fatalf("failover logs len = %d want 1", len(arr))
	}
	entry := arr[0].(map[string]interface{})
	if entry["category"] != "temperature" || int(entry["to_source_id"].(float64)) != int(b.ID) {
		t.Fatalf("unexpected failover entry: %v", entry)
	}

	// category 过滤生效
	w = doDSRequest(t, r, http.MethodGet, "/api/v1/devices/1/failover-logs?category=humidity", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 0 {
		t.Fatalf("category=humidity logs len = %d want 0", len(arr))
	}

	w = doDSRequest(t, r, http.MethodGet, "/api/v1/devices/1/failover-logs?category=temperature", nil)
	resp = dsAssertCode(t, w, http.StatusOK)
	if arr := dsDataArray(t, resp); len(arr) != 1 {
		t.Fatalf("category=temperature logs len = %d want 1", len(arr))
	}

	// 非法设备 id → 400
	w = doDSRequest(t, r, http.MethodGet, "/api/v1/devices/abc/failover-logs", nil)
	dsAssertCode(t, w, http.StatusBadRequest)
}
