package firmwareid

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Sidecar 是构建期产出的 <bin>.meta.json（方案 §4.2）。
//
// 它是**唯一**能表达 PSRAM 的地方：镜像头里没有这一位（方案判断 1）。
// 缺 sidecar 的固件不入库——存量二进制由 ehomectl firmware meta-gen 补齐后重传，
// 而不是在运行期用三态兜住（方案 §2.4 R3）。
type Sidecar struct {
	Profile string `json:"profile"`
	Chip    string `json:"chip"`
	FlashMB uint16 `json:"flash_mb"`
	// PSRAM 用指针：JSON 里缺这个键与显式 false 是两件事，
	// 前者是 sidecar 不合格（FW_META_REQUIRED），后者是「无 PSRAM」。
	PSRAM   *bool  `json:"psram"`
	Version string `json:"version"`
	IDFVer  string `json:"idf_ver"`
	SHA256  string `json:"sha256"`
}

// PSRAMValue 把布尔位翻成固件列的取值：只有 "yes" / "no"（方案 §5.2）。
func (s *Sidecar) PSRAMValue() string {
	if s != nil && s.PSRAM != nil && *s.PSRAM {
		return "yes"
	}
	return "no"
}

// ParseSidecar 解析并校验 sidecar 的**结构**（与镜像的交叉校验在 CheckSidecar）。
//
// 结构不合格一律 FW_META_REQUIRED：契约上「不合格的 sidecar」与「没有 sidecar」
// 等价——两者都拿不到 psram，而 psram 是必填项。
func ParseSidecar(raw []byte) (*Sidecar, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, newError(CodeMetaRequired, "sidecar is empty")
	}
	var side Sidecar
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&side); err != nil {
		return nil, newError(CodeMetaRequired, "sidecar is not a valid .meta.json: %v", err)
	}
	side.Profile = strings.TrimSpace(side.Profile)
	side.Chip = strings.TrimSpace(side.Chip)
	side.Version = strings.TrimSpace(side.Version)
	side.IDFVer = strings.TrimSpace(side.IDFVer)
	side.SHA256 = strings.TrimSpace(side.SHA256)

	var missing []string
	if side.Profile == "" {
		missing = append(missing, "profile")
	}
	if side.Chip == "" {
		missing = append(missing, "chip")
	}
	if side.FlashMB == 0 {
		missing = append(missing, "flash_mb")
	}
	if side.PSRAM == nil {
		missing = append(missing, "psram")
	}
	if side.Version == "" {
		missing = append(missing, "version")
	}
	if side.IDFVer == "" {
		missing = append(missing, "idf_ver")
	}
	if side.SHA256 == "" {
		missing = append(missing, "sha256")
	}
	if len(missing) > 0 {
		return nil, newError(CodeMetaRequired, "sidecar is missing %s", strings.Join(missing, ", "))
	}
	return &side, nil
}

// CheckSidecar 交叉校验 sidecar 与镜像 / 上传内容（方案 §4.2）。
//
// chip / flash_mb / version 三者必须与镜像一致；sha256 必须与上传内容一致。
// 任一不符即 FW_META_CONFLICT——这挡的是「构建产物与 sidecar 不是同一份」，
// 也正是 DEF-6（上传不解析镜像、版本手填）的关闭条件。
func CheckSidecar(side *Sidecar, id *Identity, fileSHA256 string) error {
	if side == nil {
		return newError(CodeMetaRequired, "sidecar is required")
	}
	if id == nil {
		return newError(CodeMetaRequired, "image identity is required")
	}
	if got := CanonicalChip(side.Chip); got != id.Chip {
		return newError(CodeMetaConflict, "sidecar chip %q != image chip %q", side.Chip, id.Chip)
	}
	if side.FlashMB != id.FlashMB {
		return newError(CodeMetaConflict, "sidecar flash_mb %d != image flash_mb %d", side.FlashMB, id.FlashMB)
	}
	if side.Version != id.Version {
		return newError(CodeMetaConflict, "sidecar version %q != image version %q", side.Version, id.Version)
	}
	if !strings.EqualFold(side.SHA256, fileSHA256) {
		return newError(CodeMetaConflict, "sidecar sha256 %q != uploaded content %q", side.SHA256, fileSHA256)
	}
	return nil
}

// BuildProfile 由芯片/容量/PSRAM 反推构建档位名（如 s3p-n16 / c6-n8）。
//
// 只用于 ehomectl firmware meta-gen：镜像本身不带档位名，而 psram 是人工给的，
// 两者合起来就是构建脚本的 <profile>。缺容量或未知芯片时返回 ""。
func BuildProfile(chip string, flashMB uint16, psram bool) string {
	var short string
	switch CanonicalChip(chip) {
	case ChipS3:
		short = "s3"
	case ChipC6:
		short = "c6"
	default:
		return ""
	}
	if flashMB == 0 {
		return ""
	}
	if psram {
		short += "p"
	}
	return fmt.Sprintf("%s-n%d", short, flashMB)
}
