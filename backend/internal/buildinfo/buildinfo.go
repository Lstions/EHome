// Package buildinfo 是**运行时构建身份的唯一来源**（方案 §4.3 / §6.5 / T2.1）。
//
// 谁在用它（五处出口必须同源，否则"版本号"会变成各说各的）：
//   - GET /api/v1/system/version 的 data（version/build_id/commit/build_time/notes，AC-18）
//   - GET /health 的 commit（2026-10-21 事故后加的来源自证；key 与语义不得变）
//   - GET /ping 的 version（此前两处硬编码 "2.0.0"，早已与实际镜像无关）
//   - 所有 /api/* 响应头 X-EHome-Build（AC-13 的秒级通道，方案 §6.4 通道 1）
//   - 指标 ehome_build_info{version,commit}（方案 §9.5）
//
// 两份输入、一个出口：
//  1. release.json（go:embed，本包内）：version 与 notes 由**人**维护。仓库里的
//     deploy/release.json 是模板；构建镜像时先把它覆盖到本文件再 go build ——
//     让"构建时说的版本/更新内容"与"运行时报告的"是同一份文件，不因挂载或环境
//     差异而漂移（§6.5）。
//  2. ldflags（-X main.buildCommit / -X main.buildTime，由 main.go 转发到本包）：
//     commit 与 build_time。优先级高于 release.json，这样即便嵌入文件被人改过，
//     来源自证仍以构建环境为准。
//
// 为什么不读挂载的配置文件：运行时读文件会把"构建时"和"运行时"分成两份真源，
// 少挂/错挂一次就漂移，而发布对账脚本恰恰靠这个值判断"线上跑的到底是不是这份代码"。
// embed 让二进制自己带着答案。
package buildinfo

import (
	_ "embed" // //go:embed release.json
	"encoding/json"
	"strings"
)

const (
	// UnknownCommit 是"不知道 commit"的稳定取值（与既有 /health 默认值一致，不得改）。
	UnknownCommit = "unknown"
	// DevBuildID 是未注入 commit（干净 clone 直接 go build / go test）时的 build id。
	DevBuildID = "dev"
	// buildIDLength 是 build id 的长度：git 短 SHA 前 7 位（与前端 VITE_BUILD_ID 同源同长）。
	buildIDLength = 7
	// devVersion 是 release.json 缺失/未填 version 时的兜底版本号。
	devVersion = "0.0.0-dev"
)

// 注入值（main.go 从 -ldflags -X main.buildCommit / -X main.buildTime 转发进来）。
// 保持空 = 未注入。
//
// 为什么不直接 -X 本包的变量：-X 指向不存在的符号时链接器**静默忽略**（不报错、
// 不告警），注入断链的表现是字段永久为空却无人察觉。把注入符号集中到 main 的两个
// 既有变量上，排查点唯一，而且启动日志会打印解析后的 build identity。
// 运行期没有任何代码应该"顺手改掉版本号"，所以这两个变量不导出。
var (
	injectedCommit    = ""
	injectedBuildTime = ""
)

// embeddedRelease 是构建时烙进二进制的发布清单。
// **干净 clone 必须能 `go build ./...`**：因此仓库里始终提交一份 dev 默认值，
// 镜像构建时再用 deploy/release.json 覆盖（见 Dockerfile）。
//
//go:embed release.json
var embeddedRelease []byte

// releaseManifest 是 release.json 的 schema（deploy/release.json 同 schema）。
type releaseManifest struct {
	Version   string   `json:"version"`
	Commit    string   `json:"commit"`
	BuildTime string   `json:"build_time"`
	Notes     []string `json:"notes"`
}

var release releaseManifest

func init() {
	// 解析失败**不得**让进程起不来：构建身份读不出来时降级成 dev/unknown，
	// 而不是让「关于」页的一个版本号把整个服务带崩。
	_ = json.Unmarshal(embeddedRelease, &release)
}

// Version 返回发布版本号：release.json 的 version；未填为 devVersion。
//
// 这是「关于」页与侧栏显示的版本，**不是**固件版本，也不是协议版本。
func Version() string {
	if v := strings.TrimSpace(release.Version); v != "" {
		return v
	}
	return devVersion
}

// Commit 返回完整 commit：ldflags 注入 > release.json > "unknown"。
//
// 全量 SHA 是发布对账的唯一凭据（§6.5 只对账 commit），必须逐字可比较。
func Commit() string {
	if c := normalizeCommit(injectedCommit); c != "" {
		return c
	}
	if c := normalizeCommit(release.Commit); c != "" {
		return c
	}
	return UnknownCommit
}

// BuildID 返回前端判定"页面是否过期"的短 id：commit 前 7 位；未注入为 "dev"。
//
// 必须与前端 VITE_BUILD_ID **同一个值**（同一个 GIT_COMMIT 前 7 位），否则每次
// 部署旧页面都会误报"有新版本"，或者真发布时永远不报。
func BuildID() string {
	c := Commit()
	if c == UnknownCommit {
		return DevBuildID
	}
	if len(c) <= buildIDLength {
		return c
	}
	return c[:buildIDLength]
}

// BuildTime 返回构建时间（RFC3339，UTC）：ldflags 注入 > release.json > 空串。
//
// 前端用它区分"发布"与"回滚"：服务端时间不新于本地构建时间 ⇒ 视为回滚，不提示。
// 未知时返回空串（前端对不可解析时间**不判回滚**，见 stores/appUpdate.ts）。
func BuildTime() string {
	if t := strings.TrimSpace(injectedBuildTime); t != "" {
		return t
	}
	return strings.TrimSpace(release.BuildTime)
}

// Notes 返回用户可见的更新内容，**恒非 nil**（无内容返回空切片）。
//
// AC-18：notes 为 [] 时前端两处入口都不渲染，因此绝不能返回 nil（JSON 会变成
// null，前端 `notes.length` 判断分叉）。运行期只做两件事：trim、剔除空串 ——
// 「不超过 10 条、每条不超过 120 字」由 scripts/verify-release.sh 在构建期拒绝，
// 运行期**不截断**（被截断的说明比没有说明更误导人）。
func Notes() []string {
	return normalizeNotes(release.Notes)
}

// SetBuildTime 注入构建时间。main.go 从 `-ldflags -X main.buildTime` 的变量转发。
//
// 为什么与 commit 一样走 main 的变量而不是直接 -X 本包：-X 指向不存在的符号时
// 链接器**静默忽略**（不报错、不告警），线上表现是 build_time 恒为空却无人察觉。
// 统一收口到 main.* 两个现有注入点，至少与 main.buildCommit 的注入链同形、同一处排查。
func SetBuildTime(buildTime string) {
	if t := strings.TrimSpace(buildTime); t != "" {
		injectedBuildTime = t
	}
}

// SetCommit 注入构建来源。main.go 从 `-ldflags -X main.buildCommit` 的变量转发。
//
// 空串、`unknown`、`dev` 一律忽略：未注入时不能把 release.json 里的已知来源擦掉
// （与 api.SetBuildCommit 的空串语义一致）。
func SetCommit(commit string) {
	if c := normalizeCommit(commit); c != "" {
		injectedCommit = c
	}
}

// normalizeCommit 过滤"没有 commit"的三种写法，返回可对账的 SHA；其余返回空串。
func normalizeCommit(commit string) string {
	c := strings.TrimSpace(commit)
	if c == "" || strings.EqualFold(c, UnknownCommit) || strings.EqualFold(c, DevBuildID) {
		return ""
	}
	return c
}

// normalizeNotes 把 release.json 的 notes 规整成"恒数组、无空串"的形状。
func normalizeNotes(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		if v := strings.TrimSpace(note); v != "" {
			out = append(out, v)
		}
	}
	return out
}
