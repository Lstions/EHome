package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

func newAdminLogRouter(t *testing.T) (*gin.Engine, *models.Node) {
	t.Helper()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	node := &models.Node{NodeID: "node-log-admin", LogStreamEnabled: true, LogStreamLevel: 2, LogPersistEnabled: true}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), db, nil)
	return r, node
}

func adminRequest(t *testing.T, r *gin.Engine, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLogStreamConfigAndPersistWritesValidateAndRetainZeroValues(t *testing.T) {
	r, node := newAdminLogRouter(t)
	w := adminRequest(t, r, http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/log-config", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get config=%d: %s", w.Code, w.Body.String())
	}
	var config map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config["stream_enabled"] != true || config["level"] != float64(2) || config["persist_enabled"] != true {
		t.Fatalf("unexpected config: %#v", config)
	}

	w = adminRequest(t, r, http.MethodPut, "/api/v1/nodes/"+node.NodeID+"/log-config", []byte(`{"stream_enabled":false,"level":0}`))
	if w.Code != http.StatusOK {
		t.Fatalf("zero-value update=%d: %s", w.Code, w.Body.String())
	}
	w = adminRequest(t, r, http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/log-config", nil)
	json.Unmarshal(w.Body.Bytes(), &config)
	if config["stream_enabled"] != false || config["level"] != float64(0) {
		t.Fatalf("zero values not persisted: %#v", config)
	}

	for _, body := range [][]byte{nil, []byte(`{}`), []byte(`{"level":5}`), []byte(`{"level":-1}`)} {
		w = adminRequest(t, r, http.MethodPut, "/api/v1/nodes/"+node.NodeID+"/log-config", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid config %q=%d want 400", body, w.Code)
		}
	}
	w = adminRequest(t, r, http.MethodPut, "/api/v1/nodes/missing/log-config", []byte(`{"level":1}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing node=%d want 404", w.Code)
	}

	w = adminRequest(t, r, http.MethodPut, "/api/v1/nodes/"+node.NodeID+"/log-persist", []byte(`{"enabled":false}`))
	if w.Code != http.StatusOK {
		t.Fatalf("persist false=%d: %s", w.Code, w.Body.String())
	}
	w = adminRequest(t, r, http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/log-config", nil)
	json.Unmarshal(w.Body.Bytes(), &config)
	if config["persist_enabled"] != false {
		t.Fatalf("persist false not retained: %#v", config)
	}
	for _, body := range [][]byte{nil, []byte(`{}`)} {
		w = adminRequest(t, r, http.MethodPut, "/api/v1/nodes/"+node.NodeID+"/log-persist", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid persist %q=%d want 400", body, w.Code)
		}
	}
}

func TestDeleteNodeLogsReturnsServerErrorWhenDeleteFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	node := models.Node{NodeID: "node-delete-failure"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	db.Callback().Delete().Replace("gorm:delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "node_logs" {
			tx.AddError(fmt.Errorf("forced delete failure"))
			return
		}
		callbacks.Delete(&callbacks.Config{})(tx)
	})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), db, nil)

	w := adminRequest(t, r, http.MethodDelete, "/api/v1/nodes/"+node.NodeID+"/logs", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete failure status=%d body=%s, want 500", w.Code, w.Body.String())
	}
}

func TestGetNodeLogs_FiltersOrdersAndDeleteScopesByCreatedAt(t *testing.T) {
	_, node := newAdminLogRouter(t)
	// Recreate route with the seeded DB to exercise query and delete semantics.
	seedDB := setupTestDB(t)
	if err := seedDB.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	if err := seedDB.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	other := models.Node{NodeID: "other-node"}
	if err := seedDB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	logs := []models.NodeLog{
		{NodeID: node.NodeID, Level: 2, Ts: 999999, Tag: "OLD", Message: "old alpha", CreatedAt: base.Add(-time.Hour)},
		{NodeID: node.NodeID, Level: 1, Ts: 1, Tag: "WARN", Message: "new beta", CreatedAt: base.Add(2 * time.Minute)},
		{NodeID: node.NodeID, Level: 2, Ts: 2, Tag: "INFO", Message: "new alpha", CreatedAt: base.Add(3 * time.Minute)},
		{NodeID: other.NodeID, Level: 2, Ts: 3, Tag: "INFO", Message: "other alpha", CreatedAt: base.Add(-time.Hour)},
	}
	if err := seedDB.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), seedDB, nil)

	from := strconv.FormatInt(base.Add(time.Minute).UnixMilli(), 10)
	to := strconv.FormatInt(base.Add(4*time.Minute).UnixMilli(), 10)
	w := adminRequest(t, r, http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/logs?from="+from+"&to="+to+"&level=2&tag=INFO&q=alpha&page=1&size=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("filtered query=%d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Total int              `json:"total"`
		Logs  []models.NodeLog `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Logs) != 1 || result.Logs[0].Tag != "INFO" {
		t.Fatalf("unexpected filtered result: %+v", result)
	}
	w = adminRequest(t, r, http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/logs?from=invalid", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid from=%d want 400", w.Code)
	}

	before := strconv.FormatInt(base.Add(time.Minute).UnixMilli(), 10)
	w = adminRequest(t, r, http.MethodDelete, "/api/v1/nodes/"+node.NodeID+"/logs?before="+before, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("scoped delete=%d: %s", w.Code, w.Body.String())
	}
	var countNode, countOther int64
	seedDB.Model(&models.NodeLog{}).Where("node_id = ?", node.NodeID).Count(&countNode)
	seedDB.Model(&models.NodeLog{}).Where("node_id = ?", other.NodeID).Count(&countOther)
	if countNode != 2 || countOther != 1 {
		t.Fatalf("delete scope node=%d other=%d want 2/1", countNode, countOther)
	}
	w = adminRequest(t, r, http.MethodDelete, "/api/v1/nodes/"+node.NodeID+"/logs?before=bad", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid before=%d want 400", w.Code)
	}
}
