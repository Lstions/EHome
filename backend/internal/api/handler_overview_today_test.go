package api

// F14-b: 「今日数据」统计卡读的 /overview 字段在前后端**双重不存在**。
//
// 实测（2026-09-15）：
//   后端 handler_overview.go 的 gin.H 只有 nodes / edge_devices / latest_data，
//   全仓 `grep -rn data_count_today backend/` = 0 命中；
//   前端 EdgeDeviceList.vue 读 response.data_count_today，且 response 是 **envelope**
//   （拦截器返回 response.data，见 api/data.ts 的注释），所以正确层级是 response.data.data_count_today。
//   两处叠加的结果：该卡片永远显示 0 —— 不是"缺范围词"的展示问题，而是**数字是假的**。
//
// 本文件先钉住后端契约：字段必须存在，且必须是**今日**的真实行数（不是恒 0、不是全表）。
// 前端侧的层级错误由 frontend-shared 的同名门禁覆盖。
//
// See docs/分析/后续工作计划与方案-2026-09-15.md §1.7 F14.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// newOverviewTestRouter 只装 /overview，避免把整个 routes.go 拖进来。
// 缓存是包级变量，必须清空，否则上一个用例的结果会串到下一个（30s TTL）。
func newOverviewTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.Node{}, &models.EdgeDevice{}, &models.UnifiedData{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	overviewCacheMu.Lock()
	overviewCacheData = nil
	overviewCacheTime = time.Time{}
	overviewCacheMu.Unlock()
	r := gin.New()
	registerOverviewRoutes(r.Group("/api/v1"), db)
	return r
}

type overviewPayload struct {
	Code int `json:"code"`
	Data struct {
		Nodes       map[string]any `json:"nodes"`
		EdgeDevices map[string]any `json:"edge_devices"`
	} `json:"data"`
}

// overviewDataProbe 把 data 段当**原始 map** 读，这样才能断言"字段存在与否"，
// 而不是被 struct 的零值兜底骗过去（零值永远存在，字段名不存在也照样解析成 0）。
func overviewDataProbe(t *testing.T, r *gin.Engine) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("envelope code: expected 200, got %d", resp.Code)
	}
	return resp.Data
}

// TestOverview_ExposesDataCountToday 是本次缺陷的核心断言：字段必须存在。
func TestOverview_ExposesDataCountToday(t *testing.T) {
	r := newOverviewTestRouter(t)
	data := overviewDataProbe(t, r)
	if _, ok := data["data_count_today"]; !ok {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		t.Fatalf("「今日数据」卡读的 data_count_today 在 /overview 响应里不存在；实际字段=%v。"+
			"该卡片因此恒显示 0（前端 || 0 兜底）", keys)
	}
}

// TestOverview_DataCountToday_CountsOnlyToday 钉住"今日"语义：
// 昨天/更早的行不得进分母，今天的行必须进。
func TestOverview_DataCountToday_CountsOnlyToday(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.Node{}, &models.EdgeDevice{}, &models.UnifiedData{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	overviewCacheMu.Lock()
	overviewCacheData = nil
	overviewCacheTime = time.Time{}
	overviewCacheMu.Unlock()

	// 数据以**本地零点**为基准播种，而不是以 time.Now() 为基准：
	// 若用 now 播种，测试在 23:59:59.9 启动、handler 在 00:00:00.1 取 now 时，
	// 那 3 条「今天」会落到 handler 眼中的「昨天」，结果变成 0 而非 3 ——
	// 每天有几毫秒的假红窗口。以零点为基准后不存在这个竞态。
	now := time.Now()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	today := startOfToday.Add(time.Minute)      // 恒在「今天」（即使测试刚过零点）
	yesterday := startOfToday.Add(-time.Minute) // 恒在「昨天」
	rows := []models.UnifiedData{
		{DeviceID: 1, SensorName: "t", Value: 1, Timestamp: today},
		{DeviceID: 1, SensorName: "t", Value: 2, Timestamp: today},
		{DeviceID: 2, SensorName: "t", Value: 3, Timestamp: today},
		// 恰好等于零点的那一行必须计入（判据是 >=，不是 >）
		{DeviceID: 1, SensorName: "t", Value: 5, Timestamp: startOfToday},
		// 零点前 1ns：不得计入（边界另一侧）
		{DeviceID: 1, SensorName: "t", Value: 6, Timestamp: startOfToday.Add(-time.Nanosecond)},
		// 昨天：不得计入
		{DeviceID: 1, SensorName: "t", Value: 4, Timestamp: yesterday},
		// 更早：不得计入
		{DeviceID: 1, SensorName: "t", Value: 7, Timestamp: startOfToday.AddDate(0, 0, -3)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}

	w := httptest.NewRecorder()
	r := gin.New()
	registerOverviewRoutes(r.Group("/api/v1"), db)
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	raw, ok := resp.Data["data_count_today"]
	if !ok {
		t.Fatalf("缺少 data_count_today 字段")
	}
	n, ok := raw.(float64)
	if !ok {
		t.Fatalf("data_count_today 应是数字，实际 %T = %v", raw, raw)
	}
	// 计入：today 三条 + 恰好零点一条 = 4；不计入：零点前 1ns、昨天、3 天前
	if int(n) != 4 {
		t.Fatalf("今日行数应为 4（3 条今天 + 1 条恰好零点；零点前 1ns / 昨天 / 3 天前均不计），实际 %s", fmt.Sprint(n))
	}
}
