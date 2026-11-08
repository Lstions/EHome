package api

// maintenance.go — 启动期维护引擎 + 原子切换根 handler（契约 §2/§3.3/§3.4）。
//
// 为什么需要它（2026-11-06 契约 §1）：当前启动序列是同步阻塞的，首次升级时
// ensurePartitionParentIndexes 要补建分钟级 DDL、retireDeadTimeseriesIndexes 要
// DROP（ACCESS EXCLUSIVE）—— 这段时间端口根本没监听，浏览器只看到连接失败。
// 本文件把「监听」提前到 database.Connect 之前：先对外提供一个小引擎（维护页 +
// 健康探针 + 静态资源），等全量路由装配完成后由 RootHandler 原子切换到全量引擎。
//
// 为什么是 atomic.Pointer[gin.Engine] 而不是「边跑边 AddRoute」：gin 的
// Serve 期间 AddRoute 是数据竞争（内部 trees 无锁读写）。外层 handler 持有指针，
// nil ⇒ 维护引擎，非 nil ⇒ 全量引擎，切换是一次原子写入，Serve 侧只读。

import (
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// MaintenanceErrorCode 是维护期业务请求的稳定机器码（前端据此识别维护态，
	// 不依赖人读文案 —— 文案随时可能改）。
	MaintenanceErrorCode = "MAINTENANCE_IN_PROGRESS"
	// MaintenanceMessage 是维护期业务请求的默认文案（契约 §3.3 逐字一致）。
	MaintenanceMessage = "服务正在执行启动期数据库优化，请稍候"
	// MaintenanceRetryAfter 是维护期响应建议的重试间隔（秒，契约 §3.3）。
	MaintenanceRetryAfter = "5"
)

// 维护引擎的静态资源缓存策略：与 cmd/server/static.go 保持同一语义
// （/assets 与 /favicon.svg 是带内容 hash 的构建产物 ⇒ immutable；
// SPA 入口 index.html 必须每次回源校验 ⇒ no-cache）。两处不能共用常量：
// static.go 在 package main，本文件在 package api，且 api 不能反向依赖 main。
const (
	maintenanceImmutableCache = "public, max-age=31536000, immutable"
	maintenanceNoCache        = "no-cache"
)

// MaintenanceStep 是单个启动步骤的快照行。
//
// ⚠ 本类型是 internal/bootstrap.Step 的**镜像**：字段名与 JSON tag 必须与契约
// §3.1 逐字一致。api 包刻意不 import bootstrap —— api 是路由/协议层，bootstrap 是
// 启动编排层，让前者依赖后者会把「HTTP 契约」和「谁负责跑迁移」焊死；镜像类型
// 让两边可以独立演进，代价是必须有一条测试锁住 JSON key 集合（见
// TestMaintenanceSnapshotJSONShapeMatchesContract），否则镜像会静默漂移。
type MaintenanceStep struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Weight     int        `json:"weight"`
	State      string     `json:"state"`
	Progress   float64    `json:"progress"`
	Detail     string     `json:"detail,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// MaintenanceSnapshot 是 GET /api/v1/system/maintenance 的 data 载荷（契约 §3.1 镜像）。
type MaintenanceSnapshot struct {
	Ready        bool              `json:"ready"`
	Failed       bool              `json:"failed"`
	Percent      float64           `json:"percent"`
	CurrentStep  string            `json:"current_step"`
	CurrentLabel string            `json:"current_label"`
	StartedAt    time.Time         `json:"started_at"`
	ElapsedMS    int64             `json:"elapsed_ms"`
	Steps        []MaintenanceStep `json:"steps"`
	Version      string            `json:"version"`
	BuildCommit  string            `json:"build_commit,omitempty"`
}

// MaintenanceSource 是维护引擎读取启动进度的窄接口。
//
// 只要实现它就能被维护引擎驱动（生产实现由 main.go 用 *bootstrap.Tracker 适配，
// 测试用 fake）。nil 安全：未注入时引擎按「未就绪」渲染，绝不 panic。
type MaintenanceSource interface {
	MaintenanceSnapshot() MaintenanceSnapshot
}

// MaintenanceSourceFunc 让普通函数直接作为 MaintenanceSource（便于 main.go 适配器）。
type MaintenanceSourceFunc func() MaintenanceSnapshot

// MaintenanceSnapshot 实现 MaintenanceSource。
func (f MaintenanceSourceFunc) MaintenanceSnapshot() MaintenanceSnapshot {
	if f == nil {
		return MaintenanceSnapshot{}
	}
	return f()
}

// snapshotOf 读取进度快照；nil source 返回零值（未就绪），保证 handler 永不 panic。
func snapshotOf(src MaintenanceSource) MaintenanceSnapshot {
	if src == nil {
		return MaintenanceSnapshot{}
	}
	return src.MaintenanceSnapshot()
}

// maintenancePhase 把快照压成一个阶段字符串，供 /health 与 /readyz 的 phase 字段。
//
// 取值优先级: failed > ready > 当前 step ID > "starting"。
// 用 step ID（稳定标识）而不是中文 Label 作为机器可读 phase；Label 单独在
// current_label 里给前端展示。
func maintenancePhase(s MaintenanceSnapshot) string {
	switch {
	case s.Failed:
		return "failed"
	case s.Ready:
		return "ready"
	case s.CurrentStep != "":
		return s.CurrentStep
	default:
		return "starting"
	}
}

// NewMaintenanceEngine 构造维护期 gin 引擎。
//
// 契约 §3.3 的免鉴权白名单全部在这里放行：/health、/readyz、
// /api/v1/system/maintenance、/ping、/assets/*、/favicon.svg、/metrics。
// 其余一切请求（含所有 /api/* 业务路由）返回 503 + Retry-After。
//
// staticDir 为空（未设置 EHOME_STATIC_DIR）时跳过静态与 SPA 回退 —— 与
// cmd/server/static.go 的语义一致：此时访问非白名单路径仍是 503。
func NewMaintenanceEngine(src MaintenanceSource, staticDir string) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	// /health 向后兼容（Lead 2026-11-06 裁决）：status / commit 两个既有 key 的
	// 名字与取值都不得改变（运维脚本与探针依赖它们），只能**新增**
	// ready / phase / percent。
	r.GET("/health", func(c *gin.Context) {
		snap := snapshotOf(src)
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"commit":  apiBuildCommit,
			"ready":   snap.Ready,
			"phase":   maintenancePhase(snap),
			"percent": snap.Percent,
		})
	})

	// /readyz: ready=200 {"ready":true}; 否则 503 {"ready":false, phase, percent}。
	r.GET("/readyz", func(c *gin.Context) {
		snap := snapshotOf(src)
		if snap.Ready {
			c.JSON(http.StatusOK, gin.H{"ready": true})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"ready":   false,
			"phase":   maintenancePhase(snap),
			"percent": snap.Percent,
		})
	})

	// /api/v1/system/maintenance: **免鉴权**（前端未登录也要能看到维护页）。
	// 只暴露阶段/进度/耗时，不含任何业务数据。返回与全仓一致的 envelope
	// （Lead 裁决：code=200，而不是契约初稿里的 0）。
	r.GET("/api/v1/system/maintenance", func(c *gin.Context) {
		Success(c, snapshotOf(src))
	})

	// /ping 与 /metrics 保持维护期可用（白名单，契约 §3.3）。
	// /ping 在维护期没有 driver registry，只回最小字段，不假装全量信息。
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": "2.0.0"})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	if staticDir != "" {
		immutable := r.Group("/", func(c *gin.Context) {
			c.Header("Cache-Control", maintenanceImmutableCache)
			c.Next()
		})
		immutable.Static("/assets", filepath.Join(staticDir, "assets"))
		immutable.StaticFile("/favicon.svg", filepath.Join(staticDir, "favicon.svg"))
	}

	// 兜底：GET/HEAD 到非 API 路径 → SPA 入口 index.html（history 路由要能打开
	// /maintenance 这类页面）；其余一律维护态 503。
	r.NoRoute(func(c *gin.Context) {
		// NoRoute 是**已匹配失败**的兜底，不是路由器内置路由 —— gin 不会给它
		// 追加尾部斜杠或重定向，所以这里的分支就是最终结果。
		if staticDir != "" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) &&
			!isAPIPath(c.Request.URL.Path) {
			c.Header("Cache-Control", maintenanceNoCache)
			c.File(filepath.Join(staticDir, "index.html"))
			return
		}
		c.Header("Retry-After", MaintenanceRetryAfter)
		c.JSON(http.StatusServiceUnavailable, envelope{
			Code:      http.StatusServiceUnavailable,
			Data:      nil,
			Message:   MaintenanceMessage,
			ErrorCode: MaintenanceErrorCode,
		})
	})
	return r
}

// isAPIPath 判断路径是否属于 API/非页面命名空间（这些路径绝不回退到 index.html）。
//
// 为什么必须显式判：SPA 回退只应作用于「前端路由」，把 /api/* 也回退成
// index.html 会让调用方拿到 200 + HTML，比 503 更难排查。
func isAPIPath(path string) bool {
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

// RootHandler 是全量引擎与维护引擎之间的原子开关（契约 §2）。
//
// nil 全量引擎 ⇒ 维护引擎；非 nil ⇒ 全量引擎。SetEngine 只在启动装配完成时调用
// **一次**，之后 Serve 侧全部是 atomic 读 —— 不持有锁、不做路由表修改。
//
// ⚠ 切换后**仍然**由维护引擎接管三个启动期端点（见 isStartupStatusPath）。为什么：
// 前端维护页每 2s 轮询 /api/v1/system/maintenance，就绪那一刻如果这个接口随引擎
// 一起消失，请求会落进全量引擎的 NoRoute（SPA 回退 ⇒ 200 + HTML），前端 JSON 解析
// 失败 ⇒ 永远留在维护页；同一时刻 /readyz 也会变成 404/HTML，让 K8s 就绪探针判红
// 而把刚就绪的实例摘掉。契约 §3.3 也没说这三个接口"仅维护期可用"。
// 代价（**运维排查时必须知道的盲区**，Lead 2026-11-06 裁决接受）：
//
//	/health、/readyz、/api/v1/system/maintenance 三条路径**不计入 HTTP 指标**。
//	全量引擎的 metrics 中间件挂在 r.Use(...)（routes.go 的 SetupRoutes 开头），
//	而这三条请求在 RootHandler 层就被转给维护引擎、根本没进入 r 的中间件链。
//	因此 dashboards 上这三条路径的 QPS/延迟/状态码分布永远是空的 —— 那不是
//	"没人访问"，是**没有被采集**。排查"探针是否真的在打"时请直接看容器
//	healthcheck 状态或抓包，不要看 metrics.HTTPRequests 的 path 维度。
//	其余代价：这三条路径也不经过 gzip/CORS（都是小 JSON 探针，无实际影响）。
type RootHandler struct {
	maintenance *gin.Engine
	full        atomic.Pointer[gin.Engine]
}

// NewRootHandler 用维护引擎构造根 handler（全量引擎初始为 nil）。
func NewRootHandler(maintenance *gin.Engine) *RootHandler {
	if maintenance == nil {
		// 兜底：即便调用方给 nil 也不要在 ServeHTTP 里 panic ——
		// 退化为一个"永远 503"的最小引擎。
		maintenance = NewMaintenanceEngine(nil, "")
	}
	return &RootHandler{maintenance: maintenance}
}

// SetEngine 原子切换到全量引擎（启动装配完成后调用一次）。
// 传 nil 是 no-op：切换是单向的，不允许把已就绪的服务打回维护态。
func (h *RootHandler) SetEngine(engine *gin.Engine) {
	if h == nil || engine == nil {
		return
	}
	h.full.Store(engine)
}

// Engine 返回当前生效的全量引擎（未切换时为 nil），供测试与诊断使用。
func (h *RootHandler) Engine() *gin.Engine {
	if h == nil {
		return nil
	}
	return h.full.Load()
}

// Switched 报告是否已切换到全量引擎。
func (h *RootHandler) Switched() bool { return h.Engine() != nil }

// ServeHTTP 实现 http.Handler（挂到 http.Server.Handler）。
func (h *RootHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, "server not initialized", http.StatusServiceUnavailable)
		return
	}
	engine := h.full.Load()
	if engine == nil {
		// 维护期：所有请求由维护引擎应答（含 503 业务兜底）。
		h.maintenance.ServeHTTP(w, r)
		return
	}
	// 就绪后：启动期三端点仍由维护引擎接管（见类型注释里的事故场景）。
	if isStartupStatusPath(r) {
		h.maintenance.ServeHTTP(w, r)
		return
	}
	engine.ServeHTTP(w, r)
}

// isStartupStatusPath 判断请求是否属于「两个引擎都要服务」的启动期端点。
//
// 只放 GET/HEAD：这些是只读探针，其他方法交给全量引擎按正常路由/405 处理，
// 避免在维护引擎里意外吞掉 POST（例如将来有人把某条写接口放到同一路径下）。
func isStartupStatusPath(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case "/health", "/readyz", "/api/v1/system/maintenance":
		return true
	default:
		return false
	}
}
