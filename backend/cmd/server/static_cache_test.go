package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
