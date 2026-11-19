package api

// no_route.go — 「未匹配路径」的命名空间判定与统一兜底（两个引擎共用）。
//
// 为什么单独成文件而不是留在 maintenance.go：这条规则现在被**两个引擎**使用 ——
// 维护引擎（启动期 503 兜底）与全量引擎（cmd/server/static.go 的 SPA 兜底）。
// 留在维护引擎文件里，既让"全量引擎为什么 import 一个 maintenance 符号"变成疑问，
// 也更容易被复制一份到 cmd/server —— 那正是本次要消灭的规则漂移。
//
// 历史缺陷（2026-11-18 QA 实测）：全量引擎的 NoRoute 对所有未匹配路径都回
// index.html，于是 GET /api/v1/nope 返回 200 + HTML；而维护引擎对同一路径返回
// 503 JSON。两个引擎行为不一致，且 JSON 客户端拿到"成功但解析失败"，比 404 更难查。

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// IsAPIPath 判断路径是否属于 API/非页面命名空间（这些路径绝不回退到 index.html）。
//
// 为什么必须显式判：SPA 回退只应作用于「前端路由」；把 /api/* 也回退成
// index.html 会让调用方拿到 200 + HTML —— 对 JSON 客户端来说这是"成功但解析失败"，
// 比 503/404 都更难排查。
//
// 导出的理由：全量引擎（cmd/server/static.go）必须复用同一条规则。复制一份到
// cmd/server 会让两个引擎的边界再次漂移（反向变更就会变成"维护期 503 JSON、
// 就绪后 200 HTML"）。
func IsAPIPath(path string) bool {
	return hasPathPrefix(path, "/api/") ||
		path == "/api" ||
		hasPathPrefix(path, "/metrics") ||
		path == "/health" ||
		path == "/readyz" ||
		path == "/ping"
}

// hasPathPrefix 是 prefix 匹配但不误伤同前缀的其他词（/api/ 不会匹配 /apix）。
func hasPathPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

// NoRouteHandler 构造全量引擎「未匹配路径」的兜底，API 与页面分流：
//   - API 命名空间 → 404 + 统一 envelope（与其它错误同形状，前端照常按 code 处理）；
//   - 页面路由   → serveSPA(c)：index.html 与缓存策略由 cmd/server 注入，
//     因为静态文件与缓存头不是 api 包的职责（api 包不碰文件系统）。
//
// 注意：维护引擎**不**用这个 handler —— 它在启动期对未匹配路径返回 503 +
// Retry-After（契约 §3.3），只是共用 IsAPIPath 这一条"是不是 API"的判定。
func NoRouteHandler(serveSPA gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if IsAPIPath(c.Request.URL.Path) {
			Error(c, http.StatusNotFound, "not found")
			return
		}
		serveSPA(c)
	}
}
