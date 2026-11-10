package api

// handler_data_batch_error_test.go — batch 端点**逐 category 错误不再被吞**的验收（task-5）。
//
// ============ 为什么这条契约是 P0 ============
//
// 本轮 bug（BMS 诊断卡「保护触发历史卡」永远没数据）被藏了数天的直接原因就是：
//
//	后端单 category 查询失败（窗口超限 / 帧解码失败） ⇒ HTTP 200 + data: []
//	前端拿到与「这个字段确实没有数据」**完全一样**的形状 ⇒ 渲染「暂无数据」
//
// 用户不会报障、运维看不到错误。修法有两半，本文件测后端那一半：
//
//	catResult 增加**可选** error 字段（omitempty，纯增量）：
//	  成功 ⇒ 响应里根本不出现该字段（既有契约零变化）
//	  失败 ⇒ {"category": "...", "data": [], "error": "可读原因"}，HTTP 仍 200
//	         （其它 category 的结果不能因为一个坏 category 整批 500）
//
// 前端那一半在 frontend-shared/src/composables/useHistoryData.ts（收集 error；
// throwOnError=true 且**全部** category 都带 error ⇒ 抛错，诊断卡立刻「加载失败」）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

type batchEntryLite struct {
	Category string          `json:"category"`
	Data     json.RawMessage `json:"data"`
	Error    string          `json:"error"`
}

// doBatchGet 发一次带鉴权的 batch/单条请求，返回状态码、解析后的 entries 与原始 body。
func doBatchGet(t *testing.T, db *gorm.DB, url string) (int, []batchEntryLite, string) {
	t.Helper()
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataRoutes(v1, db)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	body := w.Body.String()
	var entries []batchEntryLite
	if w.Code == http.StatusOK && strings.Contains(url, "historical-batch") {
		// 响应外壳：{"code":0,"message":"ok","data":[...]}
		var resp struct {
			Data []batchEntryLite `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("unmarshal batch body: %v (body=%s)", err, body)
		}
		entries = resp.Data
	}
	return w.Code, entries, body
}

// batchURL 拼一个 historical-batch URL（category 逗号分隔）。
func batchURL(dev uint, categories string, start, end time.Time, maxPoints int) string {
	return fmt.Sprintf("/api/v1/unified-data/historical-batch?device_pk=%d&categories=%s&start_time=%s&end_time=%s&max_points=%d",
		dev, categories, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), maxPoints)
}

// singleURL 拼一个 historical URL（单 category）。
func singleURL(dev uint, category string, start, end time.Time, agg string, maxPoints int) string {
	return fmt.Sprintf("/api/v1/unified-data/historical?device_pk=%d&category=%s&start_time=%s&end_time=%s&agg=%s&max_points=%d",
		dev, category, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), agg, maxPoints)
}

// TestHistoricalBatch_PerCategoryErrorIsReported 是本次修复的**核心守卫**：
// 每个失败的 category 都必须带 error；HTTP 仍 200（其它 category 不受影响）。
func TestHistoricalBatch_PerCategoryErrorIsReported(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	// 帧表读路径要求有 slot 字典（生产由 main.go 启动时注入）；这里注入一个空字典，
	// 让判据走到「窗口超限」分支而不是「字典未就绪」分支（后者与业务无关）。
	SetHistoryFrameDictionary(telemetry.NewMemorySlots())
	t.Cleanup(func() { SetHistoryFrameDictionary(nil) })

	// 91 天窗口：change 超过 90 天上限、avg 超过帧表单次解码上限 —— 两个都会在
	// 选源阶段失败，不需要任何帧数据（这也正是生产「窗口超限 ⇒ 空 data」的形态）。
	end := time.Now().UTC()
	start := end.Add(-91 * 24 * time.Hour)

	code, entries, body := doBatchGet(t, db, batchURL(9944, "protection_status,current", start, end, 500))
	if code != http.StatusOK {
		t.Fatalf("batch 必须保持 HTTP 200（其它 category 不能被一个坏 category 拖成 500），got %d: %s", code, body)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 个 category 结果，got %d（body=%s）", len(entries), body)
	}
	for _, e := range entries {
		if strings.TrimSpace(e.Error) == "" {
			t.Fatalf("category=%s 查询失败却没有 error 字段 —— 前端会把故障渲染成「暂无数据」"+
				"（这正是本轮 bug 被藏数天的原因）。body=%s", e.Category, body)
		}
		if got := strings.TrimSpace(string(e.Data)); got != "[]" {
			t.Fatalf("category=%s 失败时 data 必须是 []（不是 null、不是半截），got %s", e.Category, got)
		}
		t.Logf("category=%s error=%q", e.Category, e.Error)
	}
	// 契约要点：error 是可读原因而不是笼统占位。
	if !strings.Contains(body, "change") || !strings.Contains(body, "90") {
		t.Fatalf("change 窗口超限的 error 必须说清专属上限（含 change 与 90）；body=%s", body)
	}
}

// TestHistoricalBatch_SuccessOmitsErrorField 守**纯增量**：成功响应里不出现 error。
func TestHistoricalBatch_SuccessOmitsErrorField(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)
	const dev = 9945
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	end := start.Add(20 * time.Minute)
	valueAt := func(time.Time) float64 { return 3.3 }
	seedChangeFrames(t, db, dict, dev, start, end, 30*time.Second, valueAt)

	code, entries, body := doBatchGet(t, db, batchURL(dev, "current", start, end, 500))
	if code != http.StatusOK {
		t.Fatalf("batch 应 200，got %d: %s", code, body)
	}
	if len(entries) != 1 {
		t.Fatalf("期望 1 个 category 结果，got %d", len(entries))
	}
	if entries[0].Error != "" {
		t.Fatalf("成功结果不该带 error，got %q", entries[0].Error)
	}
	if strings.Contains(body, "\"error\"") {
		t.Fatalf("成功响应里出现 error 键 —— omitempty 失效会污染既有契约：%s", body)
	}
	if got := strings.TrimSpace(string(entries[0].Data)); got == "" || got == "[]" || got == "null" {
		t.Fatalf("成功结果 data 应非空，got %s（body=%s）", got, body)
	}
	t.Logf("成功响应（无 error 键）data=%s", string(entries[0].Data))
}

// TestHistorical_ChangeLongWindowServedThroughHTTP 是问题 1 的**端到端**验收：
// 30 分钟 change 窗口（分界 5 分钟 ⇒ 走 rollup 预筛长窗路径）经 HTTP 返回非空、
// 且与全量解码的点集完全一致 —— 改造前这里只会返回 200 + []。
func TestHistorical_ChangeLongWindowServedThroughHTTP(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)

	const dev = 9946
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	const step = 30 * time.Second
	end := start.Add(30 * time.Minute)
	// 三次翻转，含一次跨分钟变化。
	valueAt := func(ts time.Time) float64 {
		switch {
		case ts.Before(start.Add(10 * time.Minute)):
			return 0
		case ts.Before(start.Add(11 * time.Minute)):
			return 1
		case ts.Before(start.Add(20 * time.Minute)):
			return 0
		default:
			return 1
		}
	}
	seedChangeFrames(t, db, dict, dev, start, end, step, valueAt)
	seedChangeRollup(t, db, dev, start, end, step, valueAt)
	want := expectedChangePoints(start, end, step, valueAt)

	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)
	SetFrameDecodeMaxWindow(5 * time.Minute)
	ResetFrameDecodeCache()

	code, entries, body := doBatchGet(t, db, batchURL(dev, "protection_status", start, end, 500))
	if code != http.StatusOK {
		t.Fatalf("batch 应 200，got %d: %s", code, body)
	}
	if len(entries) != 1 || entries[0].Error != "" {
		t.Fatalf("change 长窗应成功且无 error，got entries=%+v body=%s", entries, body)
	}
	var got []HistoryPoint
	if err := json.Unmarshal(entries[0].Data, &got); err != nil {
		t.Fatalf("unmarshal data: %v (data=%s)", err, string(entries[0].Data))
	}
	assertPointsEqual(t, got, want, "HTTP batch 30 分钟 change 窗口 vs 期望值")
	t.Logf("HTTP batch 30 分钟 change 窗口 => %d 个点（改造前是 200 + []）", len(got))

	// 单条端点同口径。
	ResetFrameDecodeCache()
	code1, _, body1 := doBatchGet(t, db, singleURL(dev, "protection_status", start, end, "change", 500))
	if code1 != http.StatusOK {
		t.Fatalf("单条端点应 200，got %d: %s", code1, body1)
	}
	var resp1 struct {
		Data []HistoryPoint `json:"data"`
	}
	if err := json.Unmarshal([]byte(body1), &resp1); err != nil {
		t.Fatalf("unmarshal single: %v (body=%s)", err, body1)
	}
	assertPointsEqual(t, resp1.Data, want, "HTTP 单条 30 分钟 change 窗口 vs 期望值")
}

// TestHistorical_ChangeOver90DaysReturns500 —— 超过 change 上限的窗口必须**失败**，
// 不能 200 + []（前端会显示「加载失败 + 重试」，这是诚实的行为）。
func TestHistorical_ChangeOver90DaysReturns500(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)

	end := time.Now().UTC()
	start := end.Add(-91 * 24 * time.Hour)
	code, _, body := doBatchGet(t, db, singleURL(9947, "protection_status", start, end, "change", 500))
	if code != http.StatusInternalServerError {
		t.Fatalf(">90 天 change 窗口必须 500（诚实失败），got %d: %s", code, body)
	}
	t.Logf(">90 天 change ⇒ %d（诚实失败）", code)
}
