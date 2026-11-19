package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// setupTestStaticDir 创建含 index.html/favicon.svg/assets/app.js 的临时静态目录。
func setupTestStaticDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	files := map[string]string{
		"index.html":       "<!doctype html><title>ehome</title>",
		"favicon.svg":      "<svg/>",
		"assets/app.js":    "console.log('app')",
		"assets/index.css": "body{}",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// TestStaticAssetsCacheHeaders 验证前端静态资源的缓存响应头策略：
//
//	/assets/*        → Cache-Control: public, max-age=31536000, immutable
//	/favicon.svg     → Cache-Control: public, max-age=31536000, immutable
//	SPA 回退 index.html → Cache-Control: no-cache（每次回源校验，避免陈旧入口）
func TestStaticAssetsCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := setupTestStaticDir(t)

	r := gin.New()
	setupStaticRoutes(r, dir)

	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, tc := range []struct {
		path   string
		status int
		cache  string
	}{
		{"/assets/app.js", http.StatusOK, "public, max-age=31536000, immutable"},
		{"/assets/index.css", http.StatusOK, "public, max-age=31536000, immutable"},
		{"/favicon.svg", http.StatusOK, "public, max-age=31536000, immutable"},
		// SPA 入口必须可回源：no-cache 而非 no-store（仍可缓存命中，但每次需校验）
		{"/", http.StatusOK, "no-cache"},
		{"/login", http.StatusOK, "no-cache"}, // history 路由回退
	} {
		resp, err := srv.Client().Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("GET %s: status = %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		if got := resp.Header.Get("Cache-Control"); got != tc.cache {
			t.Fatalf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.cache)
		}
	}
}

// TestNoRouteAPI404AndSPAFallback 锁定「全量引擎未匹配路径」的三条边界
// （2026-11-18 QA 实测缺陷：/api/v1/nope 曾返回 200 + index.html）：
//
//	/api/v1/nope                 → 404 + JSON envelope（API 路径绝不落 SPA）
//	/dashboard/history           → 200 + index.html + no-cache（页面路由照旧回退）
//	/assets/does-not-exist.js    → 404（静态资源缺失就是缺失，不能回退成页面）
//
// X-EHome-Build 的覆盖不在这里断言：它由 api.SetupRoutes 注册的全局中间件提供，
// 由 api 包的 TestNoRouteHandlerSeparatesAPIAndSPA / TestBuildHeaderCoversAPIErrors
// 用**真实**中间件 + 真实兜底钉住（cmd/server 的测试够不到未导出的中间件）。
func TestNoRouteAPI404AndSPAFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := setupTestStaticDir(t)

	r := gin.New()
	setupStaticRoutes(r, dir)

	// 1) 未知 API 路径 → 404 JSON，且不带 SPA 入口的 no-cache 标（否则等于承诺可缓存页面）
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nope: status = %d, want 404（API 路径不得 SPA 回退）", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /api/v1/nope: Content-Type = %q, want application/json", ct)
	}
	if cache := w.Header().Get("Cache-Control"); cache != "" {
		t.Errorf("GET /api/v1/nope: Cache-Control = %q, want 空（404 不得继承 SPA 的 no-cache）", cache)
	}
	body := w.Body.String()
	if strings.Contains(strings.ToLower(body), "<!doctype html") {
		t.Fatalf("GET /api/v1/nope: 返回了 index.html（SPA 回退未修）: %s", body)
	}
	var envelope struct {
		Code    int    `json:"code"`
		Data    any    `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("GET /api/v1/nope: 不是统一 JSON envelope: %v body=%s", err, body)
	}
	if envelope.Code != http.StatusNotFound || envelope.Data != nil {
		t.Errorf("GET /api/v1/nope: envelope = %+v, want code=404 data=null", envelope)
	}

	// 2) 页面路由 → 仍回退 index.html + no-cache
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/history", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/history: status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<title>ehome</title>") {
		t.Errorf("GET /dashboard/history: 未回退到 index.html: %s", w.Body.String())
	}
	if cache := w.Header().Get("Cache-Control"); cache != "no-cache" {
		t.Errorf("GET /dashboard/history: Cache-Control = %q, want no-cache", cache)
	}

	// 3) 缺失静态资源 → 404，不能回退成页面（否则旧 hash 的 chunk 会静默变 200 HTML）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/does-not-exist.js: status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<title>ehome</title>") {
		t.Errorf("GET /assets/does-not-exist.js: 回退成了 index.html: %s", w.Body.String())
	}
	// 404 不得继承 /assets 组的 immutable：否则部署竞态的 404 会被浏览器缓存一年。
	if cache := w.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("GET /assets/does-not-exist.js: Cache-Control = %q, want no-store", cache)
	}
}

// TestNoStaticRoutesWithoutDir 确保静态服务未配置时（EHOME_STATIC_DIR 未设置）
// 路由照常工作且不 panic（缓存头中间件不注册、NoRoute 不挂载）。
func TestNoStaticRoutesWithoutDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	setupStaticRoutes(r, "") // 等价 EHOME_STATIC_DIR=""
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /ping: status = %d, want 200", w.Code)
	}
}
