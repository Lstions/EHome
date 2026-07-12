package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func setupLogStreamTestRoutes(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), db, nil)
	return r, ""
}

func TestLogStreamAPI_RequiresAdminRole(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Node{NodeID: "node-log-viewer"}).Error; err != nil {
		t.Fatal(err)
	}

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), db, nil)

	token, err := GenerateToken(2, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	for _, methodPath := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/nodes/node-log-viewer/log-config"},
		{http.MethodGet, "/api/v1/nodes/node-log-viewer/logs"},
		{http.MethodPut, "/api/v1/nodes/node-log-viewer/log-config"},
		{http.MethodPut, "/api/v1/nodes/node-log-viewer/log-persist"},
		{http.MethodDelete, "/api/v1/nodes/node-log-viewer/logs"},
	} {
		req := httptest.NewRequest(methodPath.method, methodPath.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want %d: %s", methodPath.method, methodPath.path, w.Code, http.StatusForbidden, w.Body.String())
		}
	}
}

func TestGetNodeLogs_FiltersAndOrdersByCreatedAt(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Node{NodeID: "node-log-history"}).Error; err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	logs := []models.NodeLog{
		{NodeID: "node-log-history", Level: 2, Ts: 9_999_999, Tag: "OLD", Message: "old", CreatedAt: base.Add(-time.Hour)},
		{NodeID: "node-log-history", Level: 2, Ts: 1, Tag: "NEWER", Message: "newer", CreatedAt: base.Add(2 * time.Minute)},
		{NodeID: "node-log-history", Level: 2, Ts: 2, Tag: "NEWEST", Message: "newest", CreatedAt: base.Add(3 * time.Minute)},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	v1.GET("/nodes/:id/logs", RequireRole("admin"), getNodeLogs(db))
	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	from := strconv.FormatInt(base.Add(time.Minute).UnixMilli(), 10)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-log-history/logs?from="+from, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Total int              `json:"total"`
		Logs  []models.NodeLog `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 2 || len(response.Logs) != 2 {
		t.Fatalf("filtered logs = total %d, rows %d; want 2", response.Total, len(response.Logs))
	}
	if response.Logs[0].Tag != "NEWEST" || response.Logs[1].Tag != "NEWER" {
		t.Fatalf("history order = [%s, %s], want [NEWEST, NEWER]", response.Logs[0].Tag, response.Logs[1].Tag)
	}
}
