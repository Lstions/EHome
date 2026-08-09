package main

import (
	"path/filepath"
	"time"

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

// immutableStaticCache 为 /assets/* 与 /favicon.svg 设置长期不可变缓存头。
// 作为组中间件挂在静态资源路由之前（r.Static/r.StaticFile 的处理函数在
// 组中间件之后执行，先写 Header 后写 Body 不会互相覆盖）。
func immutableStaticCache(c *gin.Context) {
	c.Header("Cache-Control", staticImmutableCache)
	c.Next()
}

// setupStaticRoutes 挂载前端静态资源服务与 SPA 回退。
// 与旧实现（main.go 内联的 r.Static/r.StaticFile/r.NoRoute）行为等价，
// 仅补充缓存响应头：
//   - /assets/* 与 /favicon.svg → immutable（Vite hash 产物）
//   - 其余路径（history 路由）→ index.html + no-cache
//
// staticDir 为空时静默跳过（等价未设置 EHOME_STATIC_DIR）。
func setupStaticRoutes(r *gin.Engine, staticDir string) {
	if staticDir == "" {
		return
	}

	// 注意：gin 的 NoRoute 只注册在 engine 上，无法加组中间件，
	// 因此 SPA 回退的 no-cache 头直接在 NoRoute 处理器内设置。
	r.NoRoute(func(c *gin.Context) {
		c.Header("Cache-Control", staticNoCache)
		c.File(filepath.Join(staticDir, "index.html"))
	})

	// /assets 与 /favicon.svg 的缓存头策略不同（immutable），
	// 拆成两个组分别挂相同中间件，避免把 no-cache 误加到静态产物上。
	immutable := r.Group("/", immutableStaticCache)
	{
		immutable.Static("/assets", filepath.Join(staticDir, "assets"))
		immutable.StaticFile("/favicon.svg", filepath.Join(staticDir, "favicon.svg"))
	}
}
