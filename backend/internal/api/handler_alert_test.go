package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// ---- helpers ----

func alertReq(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) (code int, data json.RawMessage) {
	t.Helper()
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, w.Body.String())
	}
	return env.Code, env.Data
}

// ginNew 构造无路由前缀的测试引擎 (告警路由直接注册在根组)。
func ginNew() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// ---- CRUD ----

func TestAlertRuleCRUD(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), db, nil)

	invalid := alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1,
		"sensor_name": "temperature", "comparator": "bogus", // 非法比较符
		"threshold": 50.0, "level": "warning", "name": "r1",
	})
	if invalid.Code != 400 {
		t.Fatalf("invalid comparator must 400, got %d %s", invalid.Code, invalid.Body.String())
	}

	invalidLevel := alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1,
		"sensor_name": "temperature", "comparator": "gt",
		"threshold": 50.0, "level": "fatal", "name": "r1", // 非法级别
	})
	if invalidLevel.Code != 400 {
		t.Fatalf("invalid level must 400, got %d", invalidLevel.Code)
	}

	created := alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1,
		"sensor_name": "temperature", "comparator": "gt",
		"threshold": 50.0, "duration_sec": 0, "level": "critical", "enabled": true, "name": "高温告警",
	})
	if created.Code != 201 {
		t.Fatalf("create must 201, got %d %s", created.Code, created.Body.String())
	}
	var rule models.AlertRule
	code, data := decodeEnvelope(t, created)
	if code != 201 || data == nil {
		t.Fatalf("bad envelope: code=%d", code)
	}
	if err := json.Unmarshal(data, &rule); err != nil {
		t.Fatal(err)
	}
	if rule.ID == 0 || rule.SilenceSec != 300 || rule.Level != "critical" {
		t.Fatalf("unexpected rule: %+v (silence 默认 300)", rule)
	}

	// GET list
	listed := alertReq(t, r, "GET", "/api/v1/alert-rules", nil)
	if listed.Code != 200 {
		t.Fatalf("list must 200, got %d", listed.Code)
	}
	var items []models.AlertRule
	_, data = decodeEnvelope(t, listed)
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(items))
	}

	// PUT update
	updated := alertReq(t, r, "PUT", "/api/v1/alert-rules/1", map[string]any{
		"threshold": 60.0, "level": "warning",
	})
	if updated.Code != 200 {
		t.Fatalf("update must 200, got %d %s", updated.Code, updated.Body.String())
	}
	var after models.AlertRule
	db.First(&after, 1)
	if after.Threshold != 60 || after.Level != "warning" {
		t.Fatalf("unexpected updated rule: %+v", after)
	}

	// PATCH enabled
	patched := alertReq(t, r, "PATCH", "/api/v1/alert-rules/1/enabled", map[string]any{"enabled": false})
	if patched.Code != 200 {
		t.Fatalf("patch enabled must 200, got %d", patched.Code)
	}
	db.First(&after, 1)
	if after.Enabled {
		t.Fatalf("expected disabled, got %+v", after)
	}

	missing := alertReq(t, r, "PATCH", "/api/v1/alert-rules/999/enabled", map[string]any{"enabled": true})
	if missing.Code != 404 {
		t.Fatalf("patch missing must 404, got %d", missing.Code)
	}

	// DELETE
	deleted := alertReq(t, r, "DELETE", "/api/v1/alert-rules/1", nil)
	if deleted.Code != 200 {
		t.Fatalf("delete must 200, got %d", deleted.Code)
	}
	var count int64
	db.Model(&models.AlertRule{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", count)
	}
	gone := alertReq(t, r, "DELETE", "/api/v1/alert-rules/1", nil)
	if gone.Code != 404 {
		t.Fatalf("delete missing must 404, got %d", gone.Code)
	}
}

// ---- events 查询 + 已读 ----

func TestAlertEventsListAndRead(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}, &models.Notification{}); err != nil {
		t.Fatal(err)
	}
	fired := models.AlertEvent{RuleID: 7, State: "firing", Value: 66}
	resolved := models.AlertEvent{RuleID: 7, State: "resolved", Value: 40}
	db.Create(&fired)
	db.Create(&resolved)
	db.Create(&models.Notification{
		Type: "warning", Message: "m", Title: "t", Source: "alert_rule", SourceID: "7",
	})

	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), db, nil)

	// 分页契约 (P1.2): data 由裸数组改为 {items,total,page,page_size}。
	// 此处逐字段断言信封, 不是"是不是数组"的宽松判断 —— 契约回退时会红。
	all := alertReq(t, r, "GET", "/api/v1/alert-events", nil)
	var allPage struct {
		Items    []models.AlertEvent `json:"items"`
		Total    int64               `json:"total"`
		Page     int                 `json:"page"`
		PageSize int                 `json:"page_size"`
	}
	_, data := decodeEnvelope(t, all)
	if err := json.Unmarshal(data, &allPage); err != nil {
		t.Fatal(err)
	}
	if len(allPage.Items) != 2 || allPage.Total != 2 {
		t.Fatalf("expected 2 events, got items=%d total=%d", len(allPage.Items), allPage.Total)
	}
	if allPage.Page != 1 || allPage.PageSize != 20 {
		t.Fatalf("expected page/page_size echo 1/20, got %d/%d", allPage.Page, allPage.PageSize)
	}

	byRule := alertReq(t, r, "GET", "/api/v1/alert-events?rule_id=7&state=firing", nil)
	var filtered struct {
		Items []models.AlertEvent `json:"items"`
		Total int64               `json:"total"`
	}
	_, data = decodeEnvelope(t, byRule)
	if err := json.Unmarshal(data, &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].State != "firing" {
		t.Fatalf("expected firing-only filter, got %+v", filtered.Items)
	}
	if filtered.Total != 1 {
		t.Fatalf("expected firing-only total=1, got %d", filtered.Total)
	}

	read := alertReq(t, r, "POST", "/api/v1/alert-events/read", map[string]any{"ids": []uint{fired.ID}})
	if read.Code != 200 {
		t.Fatalf("read must 200, got %d %s", read.Code, read.Body.String())
	}
	var n models.Notification
	db.Where("source = ?", "alert_rule").First(&n)
	if !n.Read {
		t.Fatalf("notification should be marked read via rule backlink")
	}

	readAll := alertReq(t, r, "POST", "/api/v1/alert-events/read", map[string]any{"all": true})
	if readAll.Code != 200 {
		t.Fatalf("read all must 200, got %d", readAll.Code)
	}

	badTime := alertReq(t, r, "GET", "/api/v1/alert-events?start_time=notatime", nil)
	if badTime.Code != 400 {
		t.Fatalf("bad start_time must 400, got %d", badTime.Code)
	}
}

// ---- evaluator 缓存失效接线 ----

type fakeEvaluator struct{ invalidated int }

func (f *fakeEvaluator) Invalidate() { f.invalidated++ }

func TestAlertRuleCRUDInvalidatesEvaluator(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeEvaluator{}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), db, fake)

	alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1, "sensor_name": "t",
		"comparator": "gt", "threshold": 1.0, "level": "info", "name": "x",
	})
	alertReq(t, r, "PUT", "/api/v1/alert-rules/1", map[string]any{"threshold": 2.0})
	alertReq(t, r, "PATCH", "/api/v1/alert-rules/1/enabled", map[string]any{"enabled": false})
	alertReq(t, r, "DELETE", "/api/v1/alert-rules/1", nil)
	if fake.invalidated != 4 {
		t.Fatalf("expected 4 invalidations, got %d", fake.invalidated)
	}
}
