package api

// handler_system.go — 公开的版本端点 GET /api/v1/system/version（方案 §4.3 / T2.2 / T2.11）。
//
// 三个非显然的约束（每一条都有验收条目钉着）：
//  1. **公开**：根路由注册，不进 /api/v1 的 JWT 组。未登录也要能读（AC-12）——
//     登录页要显示版本、维护页要显示"重启前后是不是同一个构建"；挂到 JWT 组
//     会让"没登录就不知道自己跑在哪个版本"。维护引擎也注册了同一个 handler，
//     因此启动窗口内同样返回 200。
//  2. **Cache-Control: no-store**（AC-18）：version/notes 随发布变化，任何缓存
//     （浏览器启发式缓存、反代默认缓存）都会让「关于」页在发布后继续显示旧内容。
//     这里显式写头而不是靠中间件：维护引擎是另一条引擎，没有全量引擎的中间件链。
//  3. **notes 恒为数组**（AC-18）：空就是 []，不是 null、也不省略字段。前端据此
//     决定是否渲染「查看更新内容」入口（§6.4），`[]` 与 `undefined` 必须同义。

import (
	"ehome/backend/internal/buildinfo"

	"github.com/gin-gonic/gin"
)

// SystemVersion 是版本端点的 data 载荷（字段名即契约，前端 system.ts 按此解析）。
type SystemVersion struct {
	Version   string   `json:"version"`
	BuildID   string   `json:"build_id"`
	Commit    string   `json:"commit"`
	BuildTime string   `json:"build_time"`
	Notes     []string `json:"notes"`
}

// systemVersionInfo 是 handler 的唯一数据源。包级变量而非直连 buildinfo：
// 测试要能构造"有 notes / 无 notes / 未注入 commit"三种形态，而不必改全局构建变量。
var systemVersionInfo = func() SystemVersion {
	return SystemVersion{
		Version:   buildinfo.Version(),
		BuildID:   buildinfo.BuildID(),
		Commit:    buildinfo.Commit(),
		BuildTime: buildinfo.BuildTime(),
		Notes:     buildinfo.Notes(),
	}
}

// systemVersionHandler 处理 GET /api/v1/system/version。
//
// 统一 envelope：{code:200, data:{...}, message:"ok"}（§4.3）。
func systemVersionHandler(c *gin.Context) {
	info := systemVersionInfo()
	// 兜底：无论数据源怎么实现，notes 到达 JSON 时都必须是数组而不是 null。
	// （buildinfo.Notes() 已保证非 nil，这里防的是未来有人换掉数据源。）
	if info.Notes == nil {
		info.Notes = []string{}
	}
	c.Header("Cache-Control", "no-store")
	Success(c, info)
}
