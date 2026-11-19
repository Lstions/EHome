package api

// handler_system_test.go — 版本端点的协议面（AC-12 / AC-18 / 方案 §4.3 / §6.4）。
//
// 覆盖的是"外部可观察的契约"，不是实现细节：
//   - 未登录 200（公开）与维护态 200（白名单）—— AC-12
//   - notes 空时 JSON 是 [] 而不是 null，且有内容时逐条透传 —— AC-18
//   - Cache-Control: no-store —— AC-18
//   - X-EHome-Build 覆盖 /api/* 的成功与**错误**响应，且与 data.build_id 同源 —— AC-13

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/buildinfo"

	"github.com/gin-gonic/gin"
)

// withSystemVersionSource 临时替换数据源，返回复原函数。
//
// 直接改包级变量而不是加测试专用 setter：生产代码不该为了测试多一个"可写版本"入口。
func withSystemVersionSource(t *testing.T, src func() SystemVersion) {
	t.Helper()
	prev := systemVersionInfo
	systemVersionInfo = src
	t.Cleanup(func() { systemVersionInfo = prev })
}

// newSystemVersionRouter 复刻 routes.go 的注册形状：全局构建头中间件 + 根路由 handler。
func newSystemVersionRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(buildHeaderMiddleware())
	r.GET("/api/v1/system/version", systemVersionHandler)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "commit": apiBuildCommit})
	})
	return r
}

func getJSON(t *testing.T, r http.Handler, path string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s 不是 JSON: %v body=%s", path, err, w.Body.String())
	}
	return w, body
}

// TestSystemVersionPublicEnvelopeAndNoStore 未登录可读（AC-12）+ envelope + no-store。
func TestSystemVersionPublicEnvelopeAndNoStore(t *testing.T) {
	r := newSystemVersionRouter()
	w, body := getJSON(t, r, "/api/v1/system/version")

	if w.Code != http.StatusOK {
		t.Fatalf("未登录 GET /api/v1/system/version status=%d want 200（AC-12：公开）", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q want no-store（AC-18：版本/说明随发布变化，不得缓存）", got)
	}

	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Code != http.StatusOK || env.Message != "ok" {
		t.Errorf("envelope=%+v want code=200 message=ok（统一 envelope）", env)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(body["data"], &data); err != nil {
		t.Fatalf("data 不是对象: %v", err)
	}
	for _, key := range []string{"version", "build_id", "commit", "build_time", "notes"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data 缺少契约字段 %q（§4.3）", key)
		}
	}
	if got := data["version"]; string(got) == `""` || len(got) == 0 {
		t.Errorf("version=%s want 非空字符串", got)
	}
}

// TestSystemVersionNotesAlwaysArray AC-18 的核心：空 notes 必须是 []，不是 null。
func TestSystemVersionNotesAlwaysArray(t *testing.T) {
	r := newSystemVersionRouter()

	// 1) 数据源返回 nil notes（最坏情况）→ 响应体仍必须是 []
	withSystemVersionSource(t, func() SystemVersion {
		return SystemVersion{Version: "2.9.0", BuildID: "a1b2c3d", Commit: "a1b2c3d9e8f7", Notes: nil}
	})
	_, body := getJSON(t, r, "/api/v1/system/version")
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body["data"], &data); err != nil {
		t.Fatalf("data 不是对象: %v", err)
	}
	if got := string(data["notes"]); got != "[]" {
		t.Fatalf("空 notes 序列化=%s want []（null 会让前端 notes.length 判断分叉）", got)
	}

	// 2) 有内容时逐条透传，顺序不变
	withSystemVersionSource(t, func() SystemVersion {
		return SystemVersion{Version: "2.9.0", BuildID: "a1b2c3d", Commit: "a1b2c3d9e8f7",
			Notes: []string{"第一条", "第二条"}}
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))
	var withNotes struct {
		Data SystemVersion `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &withNotes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(withNotes.Data.Notes) != 2 || withNotes.Data.Notes[0] != "第一条" {
		t.Errorf("notes 透传错误: %v", withNotes.Data.Notes)
	}
}

// TestSystemVersionExactJSONShape 钉住 data 的**逐字段 JSON** 形状（契约 §4.3）。
//
// 断言整串而不是逐 key 比较：字段名拼错（buildId vs build_id）、notes 变 null、
// 多包一层 data 之类的问题只有整串对比才会暴露。样例同时是接口文档的运行时版本。
func TestSystemVersionExactJSONShape(t *testing.T) {
	withSystemVersionSource(t, func() SystemVersion {
		return SystemVersion{
			Version:   "2.9.0",
			BuildID:   "a1b2c3d",
			Commit:    "a1b2c3d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3",
			BuildTime: "2026-11-18T06:12:00Z",
			Notes:     []string{"第一条", "第二条"},
		}
	})
	r := newSystemVersionRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))

	const want = `{"code":200,"data":{"version":"2.9.0","build_id":"a1b2c3d","commit":"a1b2c3d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3","build_time":"2026-11-18T06:12:00Z","notes":["第一条","第二条"]},"message":"ok"}`
	if got := w.Body.String(); got != want {
		t.Fatalf("响应体与契约 §4.3 不一致\n got: %s\nwant: %s", got, want)
	}
	t.Logf("端点响应样例: %s", w.Body.String())
	t.Logf("响应头 %s: %s / Cache-Control: %s", BuildHeaderName, w.Header().Get(BuildHeaderName), w.Header().Get("Cache-Control"))
}

// TestSystemVersionMatchesBuildinfo 端点与 buildinfo 同源（/health、响应头、指标都读它）。
func TestSystemVersionMatchesBuildinfo(t *testing.T) {
	r := newSystemVersionRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))

	var env struct {
		Data SystemVersion `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Commit != buildinfo.Commit() {
		t.Errorf("data.commit=%q want %q（与 /health 同源）", env.Data.Commit, buildinfo.Commit())
	}
	if env.Data.BuildID != buildinfo.BuildID() {
		t.Errorf("data.build_id=%q want %q（与响应头同源）", env.Data.BuildID, buildinfo.BuildID())
	}
	if got := w.Header().Get(BuildHeaderName); got != buildinfo.BuildID() {
		t.Errorf("%s=%q want %q", BuildHeaderName, got, buildinfo.BuildID())
	}
}

// TestSystemVersionAvailableDuringMaintenance AC-12：维护窗口内本端点仍 200。
//
// 维护引擎是**独立的一条 gin 引擎**，不会自动继承全量引擎的路由；漏注册的表现是
// 维护页/登录页拿不到版本（503 或 SPA HTML），而 AC-12 明确要求 200。
func TestSystemVersionAvailableDuringMaintenance(t *testing.T) {
	engine := NewMaintenanceEngine(nil, "")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("维护态 GET /api/v1/system/version status=%d want 200（AC-12 白名单）", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("维护态 Cache-Control=%q want no-store", got)
	}
	var env struct {
		Code int                        `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("维护态响应不是 JSON envelope: %v body=%s", err, w.Body.String())
	}
	if env.Code != http.StatusOK {
		t.Errorf("维护态 envelope code=%d want 200", env.Code)
	}
	if got := string(env.Data["notes"]); got != "[]" {
		t.Errorf("维护态 notes=%s want []", got)
	}
}

// TestBuildHeaderCoversAPIErrors AC-13 的响应头通道：错误响应也必须带 build id。
//
// 前端拦截器正是在 401/409/500 这些"不是 data"的分支里也不该丢通道 —— 否则
// 会话过期（401 反复出现）恰好是最需要提示"页面已过期"的时刻。
func TestBuildHeaderCoversAPIErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(buildHeaderMiddleware())
	r.GET("/api/v1/boom", func(c *gin.Context) { AbortError(c, http.StatusUnauthorized, "missing authentication token") })
	r.NoRoute(func(c *gin.Context) { c.String(http.StatusNotFound, "spa") })

	want := buildinfo.BuildID()

	// 1) 处理器内部产生的错误（JWT 401 同构）
	w, _ := getJSONRaw(t, r, "/api/v1/boom")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("401 分支 status=%d want 401", w.Code)
	}
	if got := w.Header().Get(BuildHeaderName); got != want {
		t.Errorf("401 响应 %s=%q want %q（错误响应也必须带 build id）", BuildHeaderName, got, want)
	}

	// 2) NoRoute（未知 /api/* 路径）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if got := w.Header().Get(BuildHeaderName); got != want {
		t.Errorf("NoRoute %s=%q want %q", BuildHeaderName, got, want)
	}

	// 3) 范围守卫：不在 /api/ 下的响应不加头（避免误以为静态/探针通道也参与）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if got := w.Header().Get(BuildHeaderName); got != "" {
		t.Errorf("/health 带了 %s=%q：该通道只承诺覆盖 /api/*", BuildHeaderName, got)
	}
}

// getJSONRaw 与 getJSON 相同，但不解析 body（NoRoute 返回的不是 envelope）。
func getJSONRaw(t *testing.T, r http.Handler, path string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w, w.Body.Bytes()
}
