package api

// maintenance_test.go — 维护引擎与 RootHandler 的契约测试（契约 §2/§3.3/§6.2）。
//
// 覆盖范围刻意只有"协议面"：状态码、白名单、免鉴权、JSON 形状、原子切换。
// 运行时真实迁移进度由 bootstrap 包单测 + 集成测试覆盖，这里不重复。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fakeMaintenanceSource 是可变的 MaintenanceSource，用来驱动 ready/running/failed 三态。
type fakeMaintenanceSource struct {
	snap MaintenanceSnapshot
}

func (f *fakeMaintenanceSource) MaintenanceSnapshot() MaintenanceSnapshot { return f.snap }

func setupMaintenanceTestStaticDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	for name, content := range map[string]string{
		"index.html":    "<!doctype html><title>ehome-maintenance</title>",
		"favicon.svg":   "<svg/>",
		"assets/app.js": "console.log('app')",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func serveMaintenance(t *testing.T, src MaintenanceSource, staticDir, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := NewMaintenanceEngine(src, staticDir)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// TestMaintenanceEngineNotReady 维护期（未就绪）的核心协议面：
//   - /readyz              → 503 {"ready":false,...}
//   - /health              → 200 且保留 status/commit，新增 ready/phase/percent
//   - /api/v1/system/maintenance → 200 envelope（免鉴权，未登录也要能读）
//   - 业务路径             → 503 + Retry-After + MAINTENANCE_IN_PROGRESS
func TestMaintenanceEngineNotReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := apiBuildCommit
	SetBuildCommit("cafebabe")
	defer SetBuildCommit(prev)

	src := &fakeMaintenanceSource{snap: MaintenanceSnapshot{
		Ready: false, Percent: 33.5, CurrentStep: "parent_indexes", CurrentLabel: "补齐父表索引",
		Version: "2.0.0", Steps: []MaintenanceStep{
			{ID: "db_connect", Label: "连接数据库", Weight: 3, State: "done", Progress: 100},
			{ID: "parent_indexes", Label: "补齐父表索引", Weight: 25, State: "running", Progress: 34},
		},
	}}

	// 1) /readyz 未就绪 → 503
	w := serveMaintenance(t, src, "", http.MethodGet, "/readyz")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz 未就绪 status=%d want 503", w.Code)
	}
	var readyz map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &readyz); err != nil {
		t.Fatalf("/readyz unmarshal: %v", err)
	}
	if readyz["ready"] != false {
		t.Errorf("/readyz ready=%v want false", readyz["ready"])
	}
	if readyz["phase"] != "parent_indexes" {
		t.Errorf("/readyz phase=%v want parent_indexes", readyz["phase"])
	}
	if readyz["percent"] != 33.5 {
		t.Errorf("/readyz percent=%v want 33.5", readyz["percent"])
	}

	// 2) /health 向后兼容 + 新增字段
	w = serveMaintenance(t, src, "", http.MethodGet, "/health")
	if w.Code != http.StatusOK {
		t.Fatalf("/health status=%d want 200", w.Code)
	}
	var health map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("/health unmarshal: %v", err)
	}
	if health["status"] != "ok" {
		t.Errorf("/health status=%v want ok（向后兼容字段不得改）", health["status"])
	}
	if health["commit"] != "cafebabe" {
		t.Errorf("/health commit=%v want cafebabe（构建来源必须可查询）", health["commit"])
	}
	for _, key := range []string{"ready", "phase", "percent"} {
		if _, ok := health[key]; !ok {
			t.Errorf("/health 缺少新增字段 %q（契约 §3.3）", key)
		}
	}
	if health["ready"] != false {
		t.Errorf("/health ready=%v want false（维护期未就绪）", health["ready"])
	}

	// 3) /api/v1/system/maintenance 免鉴权 + envelope
	w = serveMaintenance(t, src, "", http.MethodGet, "/api/v1/system/maintenance")
	if w.Code != http.StatusOK {
		t.Fatalf("/api/v1/system/maintenance status=%d want 200（免鉴权）", w.Code)
	}
	var env struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Data    MaintenanceSnapshot `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("maintenance unmarshal: %v", err)
	}
	if env.Code != http.StatusOK {
		t.Errorf("envelope code=%d want 200（全仓 envelope 习惯）", env.Code)
	}
	if env.Message != "ok" {
		t.Errorf("envelope message=%q want ok", env.Message)
	}
	if env.Data.CurrentStep != "parent_indexes" || env.Data.Percent != 33.5 {
		t.Errorf("data 快照未透传: %+v", env.Data)
	}
	if len(env.Data.Steps) != 2 || env.Data.Steps[1].State != "running" {
		t.Errorf("data.steps 未透传: %+v", env.Data.Steps)
	}

	// 4) 业务路径 → 503 + Retry-After + 稳定 error_code
	for _, path := range []string{"/api/v1/edge-devices", "/api/v1/auth/initialize", "/api/v1/nodes/1"} {
		w = serveMaintenance(t, src, "", http.MethodGet, path)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d want 503", path, w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != MaintenanceRetryAfter {
			t.Errorf("%s Retry-After=%q want %q", path, got, MaintenanceRetryAfter)
		}
		var body struct {
			Code      int    `json:"code"`
			ErrorCode string `json:"error_code"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s unmarshal: %v", path, err)
		}
		if body.Code != 503 || body.ErrorCode != MaintenanceErrorCode {
			t.Errorf("%s body=%+v want code=503 error_code=%s", path, body, MaintenanceErrorCode)
		}
		if body.Message != MaintenanceMessage {
			t.Errorf("%s message=%q want %q", path, body.Message, MaintenanceMessage)
		}
	}
}

// TestMaintenanceEngineReady 就绪后 /readyz 必须 200 {"ready":true}。
func TestMaintenanceEngineReady(t *testing.T) {
	src := &fakeMaintenanceSource{snap: MaintenanceSnapshot{Ready: true, Percent: 100}}
	w := serveMaintenance(t, src, "", http.MethodGet, "/readyz")
	if w.Code != http.StatusOK {
		t.Fatalf("/readyz ready status=%d want 200", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["ready"] != true {
		t.Errorf("ready=%v want true", body["ready"])
	}

	w = serveMaintenance(t, src, "", http.MethodGet, "/health")
	if w.Code != http.StatusOK {
		t.Fatalf("/health status=%d want 200", w.Code)
	}
	var health map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("health unmarshal: %v", err)
	}
	if health["phase"] != "ready" {
		t.Errorf("/health phase=%v want ready", health["phase"])
	}
	if health["status"] != "ok" || health["commit"] == nil {
		t.Errorf("/health 向后兼容字段缺失: %+v", health)
	}
}

// TestMaintenanceEngineStaticAndSPA 维护期必须能打开维护页：
//   - /assets/* 与 /favicon.svg → 200 + immutable
//   - 前端路由（/maintenance、/login）→ index.html + no-cache
//   - 未知 API 路径 → 仍是 503（绝不回退成 HTML）
func TestMaintenanceEngineStaticAndSPA(t *testing.T) {
	dir := setupMaintenanceTestStaticDir(t)
	src := &fakeMaintenanceSource{snap: MaintenanceSnapshot{Percent: 10}}

	for _, tc := range []struct {
		path  string
		cache string
	}{
		{"/assets/app.js", maintenanceImmutableCache},
		{"/favicon.svg", maintenanceImmutableCache},
		{"/maintenance", maintenanceNoCache},
		{"/login", maintenanceNoCache},
		{"/", maintenanceNoCache},
	} {
		w := serveMaintenance(t, src, dir, http.MethodGet, tc.path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d want 200", tc.path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != tc.cache {
			t.Errorf("GET %s Cache-Control=%q want %q", tc.path, got, tc.cache)
		}
	}

	// API 路径：即便静态目录存在也**不得**回退成 index.html。
	for _, path := range []string{"/api/v1/edge-devices", "/api/v1/system/unknown", "/metrics/unknown"} {
		w := serveMaintenance(t, src, dir, http.MethodGet, path)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s status=%d want 503（API 路径不得 SPA 回退）", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "" && ct[:9] == "text/html" {
			t.Errorf("GET %s 返回了 HTML（API 路径 SPA 回退是错的）", path)
		}
	}

	// 非 GET 的未知路径同样 503（POST 不能被 SPA 回退吞掉）。
	w := serveMaintenance(t, src, dir, http.MethodPost, "/maintenance")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /maintenance status=%d want 503", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != MaintenanceRetryAfter {
		t.Errorf("POST /maintenance Retry-After=%q want %q", got, MaintenanceRetryAfter)
	}
}

// TestMaintenanceEnginePingAndMetrics 白名单里的 /ping 与 /metrics 在维护期也必须放行。
func TestMaintenanceEnginePingAndMetrics(t *testing.T) {
	src := &fakeMaintenanceSource{}
	if w := serveMaintenance(t, src, "", http.MethodGet, "/ping"); w.Code != http.StatusOK {
		t.Errorf("/ping status=%d want 200（白名单）", w.Code)
	}
	if w := serveMaintenance(t, src, "", http.MethodGet, "/metrics"); w.Code != http.StatusOK {
		t.Errorf("/metrics status=%d want 200（白名单）", w.Code)
	}
}

// TestMaintenanceEngineNilSource 没有进度源时不得 panic：按未就绪渲染。
func TestMaintenanceEngineNilSource(t *testing.T) {
	if w := serveMaintenance(t, nil, "", http.MethodGet, "/readyz"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil source /readyz status=%d want 503", w.Code)
	}
	if w := serveMaintenance(t, nil, "", http.MethodGet, "/health"); w.Code != http.StatusOK {
		t.Errorf("nil source /health status=%d want 200", w.Code)
	}
	if w := serveMaintenance(t, nil, "", http.MethodGet, "/api/v1/system/maintenance"); w.Code != http.StatusOK {
		t.Errorf("nil source maintenance status=%d want 200", w.Code)
	}
}

// TestMaintenanceSnapshotJSONShapeMatchesContract 锁住镜像类型的 JSON key 集合
// 与契约 §3.1 逐字一致（Lead 要求：防两边漂移）。
func TestMaintenanceSnapshotJSONShapeMatchesContract(t *testing.T) {
	now := time.Now()
	started := now.Add(-time.Minute)
	snap := MaintenanceSnapshot{
		Ready: true, Failed: false, Percent: 12.3,
		CurrentStep: "db_connect", CurrentLabel: "连接数据库",
		StartedAt: now, ElapsedMS: 1000,
		Steps: []MaintenanceStep{{
			ID: "db_connect", Label: "连接数据库", Weight: 3, State: "done",
			Progress: 100, Detail: "ok", Error: "", StartedAt: &started, FinishedAt: &now,
		}},
		Version: "2.0.0", BuildCommit: "abc123",
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	for _, key := range []string{
		"ready", "failed", "percent", "current_step", "current_label",
		"started_at", "elapsed_ms", "steps", "version", "build_commit",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("Snapshot 缺少契约 §3.1 要求的 key %q（镜像类型已漂移）", key)
		}
	}

	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(got["steps"], &steps); err != nil {
		t.Fatalf("unmarshal steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps len=%d want 1", len(steps))
	}
	for _, key := range []string{
		"id", "label", "weight", "state", "progress",
		"detail", "started_at", "finished_at",
	} {
		if _, ok := steps[0][key]; !ok {
			t.Errorf("Step 缺少契约 §3.1 要求的 key %q（含 omitempty 的字段必须有值才出现）", key)
		}
	}

	// 反向断言：不得出现契约外的多余 key（多了意味着字段改名但忘了同步契约）。
	allowed := map[string]bool{
		"ready": true, "failed": true, "percent": true, "current_step": true,
		"current_label": true, "started_at": true, "elapsed_ms": true,
		"steps": true, "version": true, "build_commit": true,
	}
	for key := range got {
		if !allowed[key] {
			t.Errorf("Snapshot 出现契约外的 key %q", key)
		}
	}
}

// TestRootHandlerSwitchesAtomically 验证 RootHandler 的原子切换语义：
// 切换前走维护引擎（503），切换后走全量引擎（业务路由可达），且切换不影响已建立的请求。
func TestRootHandlerSwitchesAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	src := &fakeMaintenanceSource{snap: MaintenanceSnapshot{Percent: 50}}

	maintenance := NewMaintenanceEngine(src, "")
	root := NewRootHandler(maintenance)
	if root.Switched() {
		t.Fatal("新构造的 RootHandler 不应已切换")
	}

	// 切换前：维护引擎
	w := httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("切换前业务路径 status=%d want 503", w.Code)
	}
	w = httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("切换前 /readyz status=%d want 503", w.Code)
	}

	// 全量引擎：只有一条业务路由，用来证明请求真的被转发过去。
	full := gin.New()
	full.GET("/api/v1/edge-devices", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": []string{}})
	})
	root.SetEngine(full)

	if !root.Switched() || root.Engine() != full {
		t.Fatal("SetEngine 后 Switched/Engine 应反映切换结果")
	}
	w = httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("切换后业务路径 status=%d want 200", w.Code)
	}

	// 切换是单向的：SetEngine(nil) 不得把服务打回维护态。
	root.SetEngine(nil)
	w = httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("SetEngine(nil) 后 status=%d want 200（切换单向）", w.Code)
	}
}

// TestRootHandlerStartupEndpointsSurviveSwitch 是关键回归门禁：
// 切换成全量引擎之后，三个启动期端点**仍然**必须由维护引擎应答。
//
// 事故场景（不这么做就会发生）：前端维护页 2s 轮询 /api/v1/system/maintenance，
// 就绪那一刻如果该接口随维护引擎一起消失，请求会落进全量引擎的 NoRoute ⇒
// SPA 回退 200 + HTML ⇒ 前端 JSON 解析失败 ⇒ 永远停在维护页；
// 同一时刻 /readyz 变成 404/HTML ⇒ K8s 就绪探针判红 ⇒ 把刚就绪的实例摘掉。
func TestRootHandlerStartupEndpointsSurviveSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	src := &fakeMaintenanceSource{snap: MaintenanceSnapshot{Ready: true, Percent: 100}}

	maintenance := NewMaintenanceEngine(src, "")
	root := NewRootHandler(maintenance)

	// 全量引擎：刻意**不**注册 /health、/readyz、/api/v1/system/maintenance，
	// 并带一个 SPA 风格 NoRoute —— 完全复刻 main.go 里全量引擎的形状
	// （那三条路由是维护引擎独有的，SetupRoutes 只注册业务路由 + /metrics + /health 静态版）。
	full := gin.New()
	full.GET("/api/v1/edge-devices", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
	full.NoRoute(func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.String(http.StatusOK, "<!doctype html><title>spa</title>")
	})
	root.SetEngine(full)

	for _, tc := range []struct {
		path   string
		status int
		key    string
	}{
		{"/readyz", http.StatusOK, "ready"},
		{"/health", http.StatusOK, "status"},
		{"/api/v1/system/maintenance", http.StatusOK, "code"},
	} {
		w := httptest.NewRecorder()
		root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("切换后 %s status=%d want %d（启动期端点必须仍由维护引擎服务）", tc.path, w.Code, tc.status)
		}
		if ct := w.Header().Get("Content-Type"); ct == "" || !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("切换后 %s Content-Type=%q want application/json（落进了全量引擎的 SPA 回退）", tc.path, ct)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("切换后 %s 不是 JSON（落进了 SPA 回退）: %v", tc.path, err)
		}
		if _, ok := body[tc.key]; !ok {
			t.Errorf("切换后 %s 缺少 key %q: %s", tc.path, tc.key, w.Body.String())
		}
	}

	// 业务路由仍然走全量引擎（没有被维护引擎截胡）。
	w := httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("切换后业务路径 status=%d want 200", w.Code)
	}
	var biz struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &biz); err != nil {
		t.Fatalf("切换后业务路径返回的不是 JSON: %v", err)
	}
	if biz.Code != 200 {
		t.Fatalf("切换后业务路径被维护引擎截胡: %s", w.Body.String())
	}

	// 非 GET 的同路径请求必须交回全量引擎（维护引擎只接管只读探针）。
	// 判据用"响应体来自全量引擎的 NoRoute"而不是具体状态码：状态码取决于全量引擎
	// 自己的 404/405 策略，不是本文件的契约。
	w = httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if w.Header().Get("Retry-After") == MaintenanceRetryAfter {
		t.Fatalf("POST /readyz 被维护引擎接管（返回了维护态 503）: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<title>spa</title>") {
		t.Fatalf("POST /readyz 未交回全量引擎: status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestNewRootHandlerNilMaintenance panic-free: nil 维护引擎时退化为「永远 503」。
func TestNewRootHandlerNilMaintenance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := NewRootHandler(nil)
	w := httptest.NewRecorder()
	root.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil maintenance engine status=%d want 503", w.Code)
	}
}

// TestMaintenancePhase 阶段推导的优先级：failed > ready > current_step > starting。
func TestMaintenancePhase(t *testing.T) {
	for _, tc := range []struct {
		name string
		snap MaintenanceSnapshot
		want string
	}{
		{"failed", MaintenanceSnapshot{Failed: true, CurrentStep: "x"}, "failed"},
		{"ready", MaintenanceSnapshot{Ready: true, CurrentStep: "x"}, "ready"},
		{"running step", MaintenanceSnapshot{CurrentStep: "db_connect"}, "db_connect"},
		{"empty", MaintenanceSnapshot{}, "starting"},
	} {
		if got := maintenancePhase(tc.snap); got != tc.want {
			t.Errorf("%s: phase=%q want %q", tc.name, got, tc.want)
		}
	}
}
