// Package firmwarepkg 解析 EHome 固件包（.ehfw = ZIP，format_version = 1）。
//
// 方案: docs/计划/固件单文件包与构建打包规范-2026-11-19.md §4 / §5.1。
//
// 职责边界（方案 §5.1）：
//   - Open 只做**包结构**校验（INV-1 / INV-4 / INV-5 + 清单必需字段），
//     一个字节都不解压 artifact；
//   - ExtractArtifactTo 只解出 artifact 并算 sha256，**不比较**；
//   - INV-2（清单摘要 vs 实测）与 INV-3（profile vs 镜像）留在 handler ——
//     两处都校验会让「谁说了算」变成第二个真源。
//
// 错误类型复用 *firmwareid.Error：上传层的 firmwareid.Code(err) 与指标 reason
// 维度原样可用；新造一个 Error 类型会让 error_code 变成空串、指标全落
// FW_UNCLASSIFIED，排障断链（方案 §5.1 / 评审 A-3）。
package firmwarepkg

import (
	"fmt"

	"ehome/backend/internal/firmwareid"
)

// 包格式常量（方案 §4.1）。清单名刻意**不是** manifest.json：本仓 manifest 已是
// 设备配置下发事务（ConfigManifest）的专名，同名会让全仓 grep 失去意义。
const (
	FormatName    = "ehome-firmware"
	FormatVersion = 1
	ManifestEntry = "fw-manifest.json"
)

// 错误码（附录 A：包格式增量）。文案可以改，机器码不能改。
const (
	CodePkgRequired       = "FW_PKG_REQUIRED"
	CodePkgInvalid        = "FW_PKG_INVALID"
	CodePkgUnsupported    = "FW_PKG_UNSUPPORTED"
	CodePkgDigestMismatch = "FW_PKG_DIGEST_MISMATCH"
	CodePkgConflict       = "FW_PKG_CONFLICT"
	CodePkgTooLarge       = "FW_PKG_TOO_LARGE"
)

// newError 构造上传层认识的那种错误：*firmwareid.Error。
func newError(code, format string, args ...any) *firmwareid.Error {
	return &firmwareid.Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
