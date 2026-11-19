// Package firmwareid derives a firmware's identity from the ESP image itself.
//
// 方案: docs/计划/固件与平台更新机制-设计与实施方案-2026-11-18.md §4.1 / §4.2。
//
// 本包是**纯函数 + 无 IO**：调用方负责把前 HeaderSize 字节读进来（firmwareid.ReadIdentity
// 只是把 io.ReaderAt 读成那一段，不打开文件）。这样上传、迁移回填、ehomectl
// 三条路径共用同一份偏移表，偏移只在 identity.go 里出现一次。
package firmwareid

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 上传/解析错误码（附录 A 的唯一真源是方案文档；这里与之逐字对齐）。
//
// 它们是**机器码**而不是文案：前端与 e2e 按码断言，改文案不算破坏契约。
const (
	CodeNotImage         = "FW_NOT_IMAGE"
	CodeTruncated        = "FW_TRUNCATED"
	CodeProjectMismatch  = "FW_PROJECT_MISMATCH"
	CodeChipUnsupported  = "FW_CHIP_UNSUPPORTED"
	CodeFlashUnsupported = "FW_FLASH_UNSUPPORTED"
)

// 支持的芯片。chip_id 取自镜像头 0x0C 的 u16 LE。
const (
	ChipS3 = "esp32s3"
	ChipC6 = "esp32c6"
)

var chipByID = map[uint16]string{
	0x0009: ChipS3,
	0x000D: ChipC6,
}

// chipAlias 把平台上报/人工填写的芯片名规范化。旧固件上报的是 CONFIG_IDF_TARGET
// （"esp32s3"/"esp32c6"），构建档位串则是 "s3"/"c3" 之类，两者都要能认。
var chipAlias = map[string]string{
	"esp32s3": ChipS3,
	"s3":      ChipS3,
	"esp32c6": ChipC6,
	"c6":      ChipC6,
}

// CanonicalChip 规范化芯片名；未知返回 ""（不猜，方案 §2.4 R5）。
func CanonicalChip(name string) string {
	return chipAlias[strings.ToLower(strings.TrimSpace(name))]
}

// flashMBBySpiSize 是 esp_image_header_t.spi_size 的取值表。
// 方案只接受 3 与 4（8MB / 16MB）：1/2/4MB 档位在本项目不存在，
// 放进来只会让误刷多一条通道。
var flashMBBySpiSize = map[uint8]uint16{
	3: 8,
	4: 16,
}

// 镜像内偏移表（方案 §4.1，已用 4 份真实产物逐字节核对）。
const (
	offMagic     = 0x00
	offSpiSize   = 0x03
	offChipID    = 0x0C
	offDescMagic = 0x20
	offVersion   = 0x30
	offProject   = 0x50
	offTime      = 0x70
	offDate      = 0x80
	offIDFVer    = 0x90
	offElfSHA256 = 0xB0
	// ImageMagic 是 ESP 应用镜像的首字节 0xE9。导出它是为了让上传层在解包之前
	// 区分「打包前的裸镜像」（FW_PKG_REQUIRED）与「别的什么文件」（FW_NOT_IMAGE），
	// 而不是在 api 里再抄一份魔数。
	ImageMagic    = 0xE9
	descMagicWord = 0xABCD5432
	// HeaderSize 是 Parse 需要的字节数：esp_app_desc_t 的最后一个字段
	// app_elf_sha256[32] 结束于 0xB0+0x20 = 0xD0。
	HeaderSize = 0xD0
	// ProjectCollector 是本平台唯一接受的固件 project_name。
	ProjectCollector = "ehome_collector"
)

// Error 是带机器码的解析/校验错误。上传层用 Code(err) 取码写进 envelope.error_code。
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Code 返回错误的机器码；不是本包的 *Error 时返回 ""。
func Code(err error) string {
	var fwErr *Error
	if errors.As(err, &fwErr) {
		return fwErr.Code
	}
	return ""
}

// Identity 是固件自证的身份。PSRAM 不在这里——镜像头里没有这一位，
// 它只能来自构建期元数据（单文件包之后是包内清单的 identity.psram，方案判断 1）。
type Identity struct {
	ChipID     uint16 // 0x0009=S3, 0x000D=C6
	Chip       string // "esp32c6" / "esp32s3"
	FlashMB    uint16 // 3→8、4→16
	Version    string // 镜像内 version[32]
	Project    string // "ehome_collector"
	ElfSHA256  string // 镜像内 app_elf_sha256（hex）
	BuiltAtRaw string // 镜像内 date+time 原文（不做时间解析）
	IDFVersion string // 镜像内 idf_ver[32]
	SizeBytes  int64  // 完整文件长度
}

// ModelKey 是「芯片-容量」档位键，如 esp32c6-n8。
// **不含 p**：PSRAM 不在镜像头里，带 p 会让 s3 与 s3p 撞成同一个键（方案 §5.5）。
func (id *Identity) ModelKey() string {
	if id == nil {
		return ""
	}
	return ModelKey(id.Chip, id.FlashMB)
}

// ModelKey 拼「芯片-容量」档位键；chip 或 flashMB 缺失时返回 ""。
func ModelKey(chip string, flashMB uint16) string {
	if chip == "" || flashMB == 0 {
		return ""
	}
	return fmt.Sprintf("%s-n%d", chip, flashMB)
}

// ReadIdentity 从 r 读取前 HeaderSize 字节并解析。sizeBytes 是完整文件长度。
func ReadIdentity(r io.ReaderAt, sizeBytes int64) (*Identity, error) {
	if sizeBytes < HeaderSize {
		return nil, newError(CodeTruncated, "image is %d bytes, need at least %d", sizeBytes, HeaderSize)
	}
	header := make([]byte, HeaderSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return nil, newError(CodeTruncated, "read image header: %v", err)
	}
	return Parse(header, sizeBytes)
}

// Parse 解析镜像头。header 至少要有 HeaderSize 字节。
//
// 判定顺序即错误码优先级：长度 → magic → 描述符 magic（截断）→ project → chip → 容量。
// 这样「随便传个文件」得到 FW_NOT_IMAGE，而不是某个更具体的码。
func Parse(header []byte, sizeBytes int64) (*Identity, error) {
	if len(header) < HeaderSize || sizeBytes < HeaderSize {
		return nil, newError(CodeTruncated, "image is %d bytes, need at least %d", minInt64(int64(len(header)), sizeBytes), HeaderSize)
	}
	if header[offMagic] != ImageMagic {
		return nil, newError(CodeNotImage, "magic 0x%02x, want 0x%02x", header[offMagic], ImageMagic)
	}
	if got := binary.LittleEndian.Uint32(header[offDescMagic:]); got != descMagicWord {
		return nil, newError(CodeTruncated, "app descriptor magic 0x%08x, want 0x%08x", got, uint32(descMagicWord))
	}
	version := cString(header[offVersion : offVersion+32])
	project := cString(header[offProject : offProject+32])
	if project != ProjectCollector {
		return nil, newError(CodeProjectMismatch, "project %q, want %q", project, ProjectCollector)
	}
	chipID := binary.LittleEndian.Uint16(header[offChipID:])
	chip, ok := chipByID[chipID]
	if !ok {
		return nil, newError(CodeChipUnsupported, "chip_id 0x%04x is not supported", chipID)
	}
	flashMB, ok := flashMBBySpiSize[header[offSpiSize]>>4]
	if !ok {
		return nil, newError(CodeFlashUnsupported, "spi_size 0x%x is not 3 (8MB) or 4 (16MB)", header[offSpiSize]>>4)
	}
	builtAt := cString(header[offDate : offDate+16])
	if clock := cString(header[offTime : offTime+16]); builtAt != "" && clock != "" {
		builtAt += " " + clock
	}
	return &Identity{
		ChipID:     chipID,
		Chip:       chip,
		FlashMB:    flashMB,
		Version:    version,
		Project:    project,
		ElfSHA256:  hex.EncodeToString(header[offElfSHA256 : offElfSHA256+32]),
		BuiltAtRaw: builtAt,
		IDFVersion: cString(header[offIDFVer : offIDFVer+32]),
		SizeBytes:  sizeBytes,
	}, nil
}

// cString 取 NUL 截断的 C 字符串并 trim（version/project 字段是按 32 字节截断的）。
func cString(buf []byte) string {
	if idx := strings.IndexByte(string(buf), 0); idx >= 0 {
		buf = buf[:idx]
	}
	return strings.TrimSpace(string(buf))
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
