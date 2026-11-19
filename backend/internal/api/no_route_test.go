package api

// no_route_test.go — 未匹配路径的命名空间判定与兜底（本轮修复 QA 实测缺陷）。
//
// 缺陷现场：GET /api/v1/nope 在**全量引擎**返回 200 + index.html（SPA 兜底），
// 而维护引擎对同一路径返回 503 JSON。JSON 客户端拿到"成功但解析失败"，比 404 更难查。
// 本文件的断言分两层：
//   - IsAPIPath：唯一判定表（两个引擎共用，这里把边界逐条钉住）；
//   - NoRouteHandler：API → 404 统一 envelope 且带 X-EHome-Build；页面 → 交回 SPA。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/buildinfo"

	"github.com/gin-gonic/gin"
)

// TestIsAPIPath 钉住"是不是 API 命名空间"的边界。
//
// 这张表是维护引擎 503 兜底与全量引擎 404 兜底的**共同**依据，两个引擎行为
// 不一致的根因就是这份判定曾经只存在于维护引擎里。
func TestIsAPIPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		// API / 探针命名空间：绝不回退到 index.html
		{"/api", true},
		{"/api/", true},
		{"/api/v1/nope", true},
		{"/api/v1/system/version", true},
		{"/metrics", true},
		{"/metrics/unknown", true},
		{"/health", true},
		{"/readyz", true},
		{"/ping", true},
		// 页面（history）路由：必须继续回退 index.html
		{"/", false},
		{"/login", false},
		{"/dashboard", false},
		{"/dashboard/history", false},
		// 前缀边界：/apix、/api-docs 不是 /api/ 命名空间的成员
		{"/apix", false},
		{"/apix/foo", false},
		{"/api-docs", false},
	} {
		if got := IsAPIPath(tc.path); got != tc.want {
			t.Errorf("IsAPIPath(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}

// TestNoRouteHandlerSeparatesAPIAndSPA 用**真实**的构建头中间件 + 真实兜底 handler
// 断言本次修复的三件事：
//  1. 未知 API 路径 → 404 + 统一 envelope（不再是 200 + HTML）；
//  2. 该 404 响应带 X-EHome-Build（前端响应拦截器靠它判断旧页面）；
//  3. 页面路由仍交给注入的 serveSPA（index.html + no-cache 由 cmd/server 负责）。
func TestNoRouteHandlerSeparatesAPIAndSPA(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var spaCalls int
	spa := func(c *gin.Context) {
		spaCalls++
		c.Header("Cache-Control", "no-cache")
		c.String(http.StatusOK, "<!doctype html><title>spa</title>")
	}
	r := gin.New()
	r.Use(buildHeaderMiddleware())
	r.NoRoute(NoRouteHandler(spa))

	// 1) 未知 API 路径：404 JSON envelope
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nope status=%d want 404（API 路径不得 SPA 回退）", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q want application/json（统一 envelope）", ct)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "<!doctype html") {
		t.Fatalf("API 404 返回了 HTML（SPA 回退）：%s", w.Body.String())
	}
	var env struct {
		Code    int    `json:"code"`
		Data    any    `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("404 响应不是合法 JSON envelope: %v body=%s", err, w.Body.String())
	}
	if env.Code != http.StatusNotFound || env.Message != "not found" || env.Data != nil {
		t.Errorf("envelope=%+v want code=404 message=\"not found\" data=null", env)
	}
	if got := w.Header().Get(BuildHeaderName); got != buildinfo.BuildID() {
		t.Errorf("API 404 的 %s=%q want %q", BuildHeaderName, got, buildinfo.BuildID())
	}

	// 2) 页面路由：仍交给 serveSPA（200 + index.html + no-cache）
	before := spaCalls
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/history", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/history status=%d want 200", w.Code)
	}
	if spaCalls != before+1 {
		t.Errorf("页面路由没有交给 serveSPA（调用次数 %d → %d）", before, spaCalls)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("/dashboard/history Cache-Control=%q want no-cache（SPA 入口必须回源校验）", got)
	}

	// 3) 前缀边界：/apix 不是 API，仍然走 SPA（防止前缀比较退化成 strings.Contains）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/apix/foo", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html") {
		t.Fatalf("/apix/foo 应走 SPA 回退: status=%d body=%s", w.Code, w.Body.String())
	}
}
