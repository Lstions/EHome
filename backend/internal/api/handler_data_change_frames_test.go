package api

// handler_data_change_frames_test.go — #5 agg=change 与 #18 agg=raw 的关系（task-27）。
//
// 演练同时红了 #5(agg=change) 和 #18(agg=raw)。**它们很可能是同一个根因**：
//
//\t调用链：rollup 分派 -> (aggMode != raw && shouldAggregateHistory) -> 裸查询
//\t而 shouldAggregateHistory 需要 maxPoints > 0：
//\t  bucketWidthSeconds(window, 0) = 0  =>  不大于 0  =>  eligible=false
//
// 所以**不传 max_points 的 agg=change 请求会落到 raw 分支**，与 agg=raw 同一条路。
// 本用例钉住这个前提 —— 如果将来有人改了 shouldAggregateHistory 的判据，
// #5 的修复会静默失效，而它的症状（.data=0）看起来和 #18 一模一样。

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

// TestHistoricalChange_NoMaxPointsFallsToFrameRawPath 守「change 无 max_points 时也能读到数据」。
func TestHistoricalChange_NoMaxPointsFallsToFrameRawPath(t *testing.T) {
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

	const dev = 22
	start := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Minute)
	end := start.Add(5 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, []string{"protection_status"}, 30*time.Second)

	// 前置：确认 shouldAggregateHistory 在 maxPoints=0 时为 false —— 这就是「落到 raw 分支」的判据。
	if shouldAggregateHistory(db, 0, start, end) {
		t.Fatalf("maxPoints=0 时 shouldAggregateHistory 竟为 true —— 与本用例前提不符，" +
			"请检查 bucketWidthSeconds 的语义变化")
	}

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	// 不传 max_points（与演练一致）
	url := "/api/v1/unified-data/historical?device_pk=22&category=protection_status&agg=change" +
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
			Value float64 `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data) == 0 {
		t.Fatalf("agg=change 且无 max_points 时 .data 为空 —— 会落到 raw 分支，"+
			"而 raw 分支已接帧表，说明没走到（body=%s）", w.Body.String())
	}
	t.Logf("agg=change 无 max_points => 走 raw 帧表路径，返回 %d 条", len(resp.Data))
}
