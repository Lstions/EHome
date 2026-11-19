package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"ehome/backend/internal/api"

	"github.com/gin-gonic/gin"
)

// 前端静态资源缓存策略（P2）：
//
// Vite 构建产物（/assets/*）文件名带内容 hash，内容一变文件名即变，
// 因此可以安全地长期不可变缓存（immutable），浏览器二次访问零请求。
// favicon 同样为带版本构建的静态文件，按不可变缓存处理。
// SPA 入口 index.html（NoRoute 回退）必须每次回源校验（no-cache），
// 否则发布新版本后浏览器可能命中陈旧的入口 HTML，导致加载旧 hash 产物。
const (
	staticImmutableCache = "public, max-age=31536000, immutable"
	staticNoCache        = "no-cache"
	staticCacheMaxAge    = 365 * 24 * time.Hour
)

// isStaticNamespace 判断路径是否属于本文件服务的静态资源命名空间。
//
// 只认 /assets 前缀与 /favicon.svg —— 这两个是 setupStaticRoutes 注册的产物路径，
// 其余路径都是页面（history）路由，必须继续回退 index.html。
func isStaticNamespace(path string) bool {
	return path == "/assets" || strings.HasPrefix(path, "/assets/") || path == "/favicon.svg"
}

// immutableStaticCache 为 /assets/* 与 /favicon.svg 设置长期不可变缓存头。
// 作为组中间件挂在静态资源路由之前（r.Static/r.StaticFile 的处理函数在
// 组中间件之后执行，先写 Header 后写 Body 不会互相覆盖）。
func immutableStaticCache(c *gin.Context) {
	c.Header("Cache-Control", staticImmutableCache)
	c.Next()
}

// setupStaticRoutes 挂载前端静态资源服务与 SPA 回退。
// 与旧实现（main.go 内联的 r.Static/r.StaticFile/r.NoRoute）的差别只有两处：
//   - /assets/* 与 /favicon.svg → immutable（Vite hash 产物）
//   - 其余页面路径（history 路由）→ index.html + no-cache
//   - **API 命名空间例外**：未匹配的 /api/*（及 /metrics、/health、/readyz、/ping）
//     返回 404 + 统一 JSON envelope，绝不落 SPA（判定见 api.NoRouteHandler）。
//
// 静态资源命名空间里**缺失**的文件同样是 404（gin 会把未命中的静态请求交给
// NoRoute，这里必须显式拒绝，不能回退成 index.html）。
//
// staticDir 为空时静默跳过（等价未设置 EHOME_STATIC_DIR）。
func setupStaticRoutes(r *gin.Engine, staticDir string) {
	if staticDir == "" {
		return
	}

	// 注意：gin 的 NoRoute 只注册在 engine 上，无法加组中间件，
	// 因此 SPA 回退的 no-cache 头直接在 NoRoute 处理器内设置。
	//
	// API vs 页面的分流与 404 形状统一由 api.NoRouteHandler 提供：它复用维护引擎
	// 同一条 IsAPIPath 规则（两个引擎的边界必须只有一份），这里只负责页面侧的
	// index.html + no-cache —— 静态资源与缓存策略是 cmd/server 的职责，不下沉到 api。
	r.NoRoute(api.NoRouteHandler(func(c *gin.Context) {
		// 静态资源命名空间缺失 → 404。gin 的 Static/StaticFile 在文件不存在时会
		// 把请求交回 NoRoute（便于应用自定义 404 页）；若这里回 index.html，缺失的
		// chunk 会变成 200 + HTML —— 旧 hash 的懒加载分片永远拿不到 404，前端
		// 「chunk 加载失败 ⇒ 强制刷新」通道（AC-16）就被静默吞掉了。
		if isStaticNamespace(c.Request.URL.Path) {
			// 404 **不能**继承 /assets 组的 immutable：那是给带内容 hash 的**成功产物**
			// 的长期缓存承诺。把缺失文件标成"一年不可变"，会让一次部署竞态（旧 chunk
			// 已删、新 chunk 未就绪）产生的 404 在浏览器里钉死，刷新也拿不到新 chunk。
			c.Header("Cache-Control", "no-store")
			http.NotFound(c.Writer, c.Request)
			return
		}
		c.Header("Cache-Control", staticNoCache)
		c.File(filepath.Join(staticDir, "index.html"))
	}))

	// /assets 与 /favicon.svg 的缓存头策略不同（immutable），
	// 拆成两个组分别挂相同中间件，避免把 no-cache 误加到静态产物上。
	immutable := r.Group("/", immutableStaticCache)
	{
		immutable.Static("/assets", filepath.Join(staticDir, "assets"))
		immutable.StaticFile("/favicon.svg", filepath.Join(staticDir, "favicon.svg"))
	}
}
