package api

// handler_data_raw_frames_test.go — #18 /unified-data/historical?agg=raw 的迁移验收（task-27）。
//
// 演练场景：unified_data 全 DETACH 后本分支 .data=0（基线 11,126）。
// 本用例清空 unified_data、只留帧表，断言 agg=raw 仍返回原始样本。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

func TestHistoricalRaw_SurvivesUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 21
	start := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	end := start.Add(10 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, []string{"rsoc"}, 30*time.Second)

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	url := "/api/v1/unified-data/historical?device_pk=21&category=rsoc&agg=raw" +
		"&start_time=" + start.Format(time.RFC3339) + "&end_time=" + end.Format(time.RFC3339)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			SensorName string    `json:"sensor_name"`
			Value      float64   `json:"value"`
			Timestamp  time.Time `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data) == 0 {
		t.Fatalf("agg=raw 在 unified_data 被藏后 .data 为空 —— 接口退化（body=%s）", w.Body.String())
	}
	// raw 契约：返回的是**原始样本**，不是聚合点 —— 30s 间隔 10 分钟应约 20 条
	if len(resp.Data) < 15 {
		t.Fatalf("raw 只返回 %d 条，不像原始样本（30s 间隔 10 分钟应约 20 条）", len(resp.Data))
	}
	for i := 1; i < len(resp.Data); i++ {
		if resp.Data[i].Timestamp.Before(resp.Data[i-1].Timestamp) {
			t.Fatalf("第 %d 条时间倒序 —— raw 契约是 timestamp ASC", i)
		}
	}
	t.Logf("unified_data 清空后 agg=raw 仍返回 %d 条原始样本，时间升序", len(resp.Data))
}
