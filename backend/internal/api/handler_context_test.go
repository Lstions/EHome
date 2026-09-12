package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// ===========================================================================
// P2.2 取消传播 (WithContext) + 读路径错误暴露测试
//
// 4 个重查询均已接入 db.WithContext(c.Request.Context()) 并且都检查查询
// 错误: 请求上下文被取消时 GORM 返回 error, handler 统一走 500 分支 (不再
// 吞错返回 200 + 空数组)。正常 context 下仍必须 200, 且空结果集仍必须序列化
// 为 [] (不能把"没有数据"和"查询失败"混为一谈)。
//
// 除 HTTP 状态码外, 这里还用 GORM 的 query callback 捕获每条查询执行时的
// Statement.Context, 证明取消信号确实下传到了数据库执行链的最末端 (包括
// ApplyShapeDedup 的保形去重外层链)。
// ===========================================================================

// canceledGET builds a GET request whose context is already canceled, mirroring
// a client that disconnected or an already-exhausted request deadline.
func canceledGET(target string) *http.Request {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
}

// queryContextRecorder records the Statement.Context of every GORM query
// executed after register was called.
type queryContextRecorder struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (r *queryContextRecorder) register(db *gorm.DB) {
	db.Callback().Query().Before("gorm:query").Register("ehome:p22_capture_ctx", func(tx *gorm.DB) {
		// GORM 在渲染子查询 (如 ApplyShapeDedup 的 Table("(?)")) 时会以
		// DryRun 方式复用 query 回调构建 SQL; 那不是真正打到 DB 的执行。
		// 只记录真正执行的查询, 才能证明"最终执行链"拿到了取消信号。
		if tx.DryRun {
			return
		}
		r.mu.Lock()
		r.ctxs = append(r.ctxs, tx.Statement.Context)
		r.mu.Unlock()
	})
}

func (r *queryContextRecorder) reset() {
	r.mu.Lock()
	r.ctxs = nil
	r.mu.Unlock()
}

func (r *queryContextRecorder) sawCanceled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ctx := range r.ctxs {
		if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
			return true
		}
	}
	return false
}

// TestNodesList_CanceledRequest_ReturnsNon200 — GET /nodes 的列表查询检查查询
// 错误, GORM 在 context 取消时返回 error, handler 应走 500 分支; 正常 context
// 仍必须 200 (防误伤)。
func TestNodesList_CanceledRequest_ReturnsNon200(t *testing.T) {
	r, _ := setupTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("normal request: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, canceledGET("/api/v1/nodes"))
	if w.Code == http.StatusOK {
		t.Fatalf("canceled request: expected non-200 (GORM error -> 500), got %d: %s", w.Code, w.Body.String())
	}
}

// TestHeavyQueries_CanceledRequest_Returns500AndReachesDB — 对每个重查询:
//   - 正常 context → 200 (防误伤);
//   - 已取消 context → 500 (查询错误必须暴露给客户端, 不再 200 + 空数组);
//   - 取消后的执行链必须观察到 canceled 的请求上下文 (取消真的传播到 DB 层)。
//
// 覆盖: edge-devices 全量列表 / unified-data historical (含 dedup-session 变体) /
// devices-history / edge-device-data 分页。上一批 3 个忽略 .Error 的端点现已全部
// 可通过 HTTP 状态码直接观察取消, 不再依赖 DB callback 间接证明。
func TestHeavyQueries_CanceledRequest_Returns500AndReachesDB(t *testing.T) {
	cases := []struct {
		name   string
		target string
		seed   func(*gorm.DB)
	}{
		{
			name:   "edge-devices-list",
			target: "/api/v1/edge-devices",
		},
		{
			name:   "unified-data-historical",
			target: "/api/v1/unified-data/historical?device_pk=1&category=voltage&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z",
		},
		{
			// 保形去重 (ApplyShapeDedup / db.Session) 分支: 目标逻辑设备有一个
			// 已完成的入边合并 → qs.DedupNeeded=true, 外层去重查询的执行链也
			// 必须携带请求上下文。
			name:   "unified-data-historical-dedup-session",
			target: "/api/v1/unified-data/historical?device_pk=1&category=voltage&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z",
			seed: func(db *gorm.DB) {
				done := models.MergeStatusDone
				target := models.LogicalDevice{IdentityKey: "type:dedup-target", Name: "target", DeviceType: "sensor"}
				db.Create(&target)
				src := models.LogicalDevice{IdentityKey: "type:dedup-src", Name: "src", DeviceType: "sensor", MergedInto: &target.ID, MergeStatus: &done}
				db.Create(&src)
				db.Create(&models.EdgeDevice{Name: "probe-dedup", Type: "sensor", NodeID: "n1", ChannelID: 1, LogicalDeviceID: &target.ID})
			},
		},
		{
			name:   "devices-history",
			target: "/api/v1/devices/1/history?hours=24",
		},
		{
			name:   "edge-device-data-pagination",
			target: "/api/v1/edge-devices/1/data?page=1&page_size=50",
			seed: func(db *gorm.DB) {
				db.Create(&models.EdgeDevice{Name: "probe", Type: "sensor", NodeID: "n1", ChannelID: 1})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, db := setupTestRouter(t)
			if tc.seed != nil {
				tc.seed(db)
			}
			rec := &queryContextRecorder{}
			rec.register(db)

			// 防误伤: 正常 context 必须 200。
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("normal request: expected 200, got %d: %s", w.Code, w.Body.String())
			}

			// 读路径错误暴露: 取消 → 500 (而不是 200 + 空数组)。
			rec.reset()
			w = httptest.NewRecorder()
			r.ServeHTTP(w, canceledGET(tc.target))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("canceled request: expected 500 (query error surfaced), got %d: %s", w.Code, w.Body.String())
			}
			// 取消传播: 重查询必须拿到已取消的请求 context。
			if !rec.sawCanceled() {
				t.Fatalf("canceled request: no DB query observed the canceled request context (status %d)", w.Code)
			}
		})
	}
}

// TestHeavyQueries_NormalContext_EmptyIsArray — 正常 context 且无数据时,
// 空结果集必须仍是 200 + [] (补错误检查后不得把空集误判为失败)。
func TestHeavyQueries_NormalContext_EmptyIsArray(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"edge-devices-list", "/api/v1/edge-devices"},
		{"devices-history", "/api/v1/devices/1/history?hours=24"},
		{"unified-data-historical", "/api/v1/unified-data/historical?device_pk=1&category=voltage&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := setupTestRouter(t)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var env struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("response is not valid JSON: %v (%s)", err, w.Body.String())
			}
			if string(env.Data) != "[]" {
				t.Fatalf("empty result must serialize as [], got %s", string(env.Data))
			}
		})
	}
}
