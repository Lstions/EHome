package firmwarepkg

import "strings"

// Manifest 是包内 fw-manifest.json（方案 §4.3，format_version = 1）。
//
// 只保留服务端**真正消费**的字段：artifact.name / artifact.size / artifact.sha256
// 与 identity.*。build.*（溯源信息）不据此拒绝、也不入库，随 dist/ 归档；
// 未来的 signature 顶层键按未知键忽略（§4.7）。
type Manifest struct {
	Format        string   `json:"format"`
	FormatVersion int      `json:"format_version"`
	Packer        Packer   `json:"packer"`
	Artifact      Artifact `json:"artifact"`
	Identity      Identity `json:"identity"`
}

// Packer 记录是谁打的包。
type Packer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Artifact 描述包内那条 OTA 应用镜像。
type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Identity 是清单里**镜像说不了**的两个字段（方案 §4.3）：
// psram 是构建期事实（镜像头没有这一位），profile 用于与镜像交叉核对（INV-3）。
type Identity struct {
	// PSRAM 用指针：JSON 里缺这个键与显式 false 是两件事。
	PSRAM   *bool  `json:"psram"`
	Profile string `json:"profile"`
}

// PSRAMEnabled 把布尔位翻成可比较的值；nil（缺键）按 false 处理 ——
// 缺键本身已在 Open 的必需字段校验里被拒，这里只服务于通过校验的清单。
func (i Identity) PSRAMEnabled() bool {
	return i.PSRAM != nil && *i.PSRAM
}

// knownManifestKeys 是 format_version=1 认识的顶层键。未知键忽略但记日志（§4.6）——
// 清单是长期格式，多出来的键可能是新客户端写的可选字段，不构成拒绝理由。
var knownManifestKeys = map[string]bool{
	"format":         true,
	"format_version": true,
	"packer":         true,
	"artifact":       true,
	"identity":       true,
	"build":          true, // §4.3 声明的可选块；服务端不读
}

// validate 校验清单的必需字段与取值（方案 §4.3 / §4.4 INV-4）。
func (m *Manifest) validate() error {
	if m.Format != FormatName {
		return newError(CodePkgUnsupported, "manifest format %q, want %q", m.Format, FormatName)
	}
	if m.FormatVersion != FormatVersion {
		return newError(CodePkgUnsupported, "manifest format_version %d, this server supports %d", m.FormatVersion, FormatVersion)
	}
	var bad []string
	if strings.TrimSpace(m.Packer.Name) == "" {
		bad = append(bad, "packer.name")
	}
	if strings.TrimSpace(m.Packer.Version) == "" {
		bad = append(bad, "packer.version")
	}
	if strings.TrimSpace(m.Artifact.Name) == "" {
		bad = append(bad, "artifact.name")
	} else if !strings.HasSuffix(m.Artifact.Name, ".bin") {
		bad = append(bad, "artifact.name (must end with .bin)")
	}
	if m.Artifact.Size <= 0 {
		bad = append(bad, "artifact.size")
	}
	if !isSHA256Hex(m.Artifact.SHA256) {
		bad = append(bad, "artifact.sha256 (must be 64 hex chars)")
	}
	if m.Identity.PSRAM == nil {
		bad = append(bad, "identity.psram")
	}
	if strings.TrimSpace(m.Identity.Profile) == "" {
		bad = append(bad, "identity.profile")
	}
	if len(bad) > 0 {
		return newError(CodePkgInvalid, "%s is missing or invalid: %s", ManifestEntry, strings.Join(bad, ", "))
	}
	return nil
}

// isSHA256Hex 只接受 64 位十六进制（大小写都认：packer 是唯一写方，宽容度只用于
// 比较时统一大小写，不用来放过畸形值）。
func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
