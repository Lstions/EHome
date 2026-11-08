package api

// handler_data_categories_test.go — #15 /unified-data/categories 的数据源迁移验收（task-27）。
//
// 演练场景：unified_data 全 DETACH 后本接口返回 .data=0（基线 29）。
// 本用例复现该场景：**清空 unified_data**，只留 sensor_field_obs，断言接口仍返回类别。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestCategories_SurvivesUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	// 维度表在生产由 projector 建；测试库需要显式迁移。
	if err := db.AutoMigrate(&models.SensorFieldObs{}); err != nil {
		t.Fatalf("migrate sensor_field_obs: %v", err)
	}
	db.Exec("DELETE FROM sensor_field_obs")
	// 演练场景：unified_data 一行都没有
	if err := db.Exec("DELETE FROM unified_data").Error; err != nil {
		t.Fatalf("clean unified_data: %v", err)
	}

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	const dev = uint(7)
	names := []string{"current", "rsoc", "total_voltage"}
	for _, n := range names {
		if err := db.Exec(
			"INSERT INTO sensor_field_obs (device_id, sensor_name, unit, first_seen, last_seen, samples, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, 1, ?)",
			dev, n, "U-"+n, time.Now().UTC(), time.Now().UTC(), time.Now().UTC()).Error; err != nil {
			t.Fatalf("seed sensor_field_obs: %v", err)
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/unified-data/categories?device_pk=7", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Code string `json:"code"`
			Unit string `json:"unit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data) != len(names) {
		t.Fatalf("类别数 = %d, want %d —— unified_data 被藏后必须仍能返回（body=%s）",
			len(resp.Data), len(names), w.Body.String())
	}
	for _, c := range resp.Data {
		if c.Unit == "" {
			t.Fatalf("类别 %q 没有单位 —— 维度表的 unit 没被读出来", c.Code)
		}
	}
	t.Logf("unified_data 清空后仍返回 %d 个类别（含单位）", len(resp.Data))
}
