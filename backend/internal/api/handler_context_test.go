package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// ===========================================================================
// P2.2 取消传播 (WithContext) 测试
//
// 目标: 证明 4 个重查询接入 db.WithContext(c.Request.Context()) 后, 请求的
// 取消信号确实被带到了数据库执行链的最末端。
//
// 现状说明 (测试策略): 这 4 个重查询中只有 GET /nodes 会检查查询错误并走 500
// 分支; 其余 3 个 (edge-devices 全量列表 / unified-data historical 全量 /
// device_data 分页) 沿用既有 "尽力而为" 语义忽略 .Error, 因此仅靠 HTTP 状态码
// 无法观察取消 (取消时它们依旧返回 200)。为在不改动业务逻辑的前提下证明取消
// 确实下传, 这里通过 GORM 的 query callback 捕获每条查询执行时的
// Statement.Context, 断言重查询拿到的是已取消的请求上下文。
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

// TestHeavyQueries_CanceledRequest_ReachesDB — 对每个重查询: 正常 context 下
// 仍 200 (防误伤); 已取消的 context 下, 该重查询的执行链必须观察到 canceled
// 的请求上下文 (取消真的传播到了 DB 层)。
func TestHeavyQueries_CanceledRequest_ReachesDB(t *testing.T) {
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

			// 取消传播: 重查询必须拿到已取消的请求 context。
			rec.reset()
			w = httptest.NewRecorder()
			r.ServeHTTP(w, canceledGET(tc.target))
			if !rec.sawCanceled() {
				t.Fatalf("canceled request: no DB query observed the canceled request context (status %d)", w.Code)
			}
			// 记录实际状态: 这 3 个端点忽略查询错误, 因此取消后仍可能是 200,
			// 这正是本测试用 callback 而非状态码来证明传播的原因。
			t.Logf("canceled status=%d (handler error-swallowing is pre-existing)", w.Code)
		})
	}
}
