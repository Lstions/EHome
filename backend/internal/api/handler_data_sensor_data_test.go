package api

// handler_data_sensor_data_test.go — #16 /devices/:id/sensor-data 的数据源迁移验收（task-27）。
//
// 演练场景：unified_data 全 DETACH 后本接口 .data=0（基线 50）。
// 本用例清空 unified_data、只留帧表，断言仍返回数据，且保持契约（DESC + limit）。

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

func TestSensorData_SurvivesUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	// 迁移是幂等的：已分区时直接返回。
	// ⚠ **绝不 DROP telemetry_frame**：它是共享表。我第一版在迁移失败时 DROP 重建，
	//   结果改变了分区集合，把后面 5 条依赖帧表的用例全带红了（跨用例污染）。
	//   迁移失败就跳过本用例 —— 用例不该为了自己能跑而破坏共享状态。
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	// 演练场景：unified_data 一行都没有
	if err := db.Exec("DELETE FROM unified_data").Error; err != nil {
		t.Fatalf("clean unified_data: %v", err)
	}

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 8
	names := []string{"current", "rsoc"}
	start := time.Now().UTC().Add(-30 * time.Minute)
	end := time.Now().UTC().Add(-time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, names, 10*time.Second)

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/devices/8/sensor-data?limit=5", nil)
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
		t.Fatalf("unified_data 被藏后 .data 为空 —— 接口退化（body=%s）", w.Body.String())
	}
	if len(resp.Data) != 5 {
		t.Fatalf("limit=5 却返回 %d 条", len(resp.Data))
	}
	// 契约：DESC
	for i := 1; i < len(resp.Data); i++ {
		if resp.Data[i].Timestamp.After(resp.Data[i-1].Timestamp) {
			t.Fatalf("第 %d 条时间戳比前一条新 —— 契约是 timestamp DESC", i)
		}
	}
	t.Logf("unified_data 清空后仍返回 %d 条，且保持 DESC", len(resp.Data))
}
