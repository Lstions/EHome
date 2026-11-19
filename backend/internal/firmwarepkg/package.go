package firmwarepkg

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"ehome/backend/pkg/logger"
)

// entryNamePattern 是 INV-4：扁平命名空间（无目录、全小写）。
//
// zip slip 的前提是「条目名被当路径用」；本包永远把条目名当**键**，
// 再加上这条正则，`../evil.bin`、`A/b.bin`、`..` 都进不来（方案 §4.2 / §4.4）。
var entryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Package 是一个已通过结构校验的固件包。
type Package struct {
	manifest Manifest
	artifact *zip.File
}

// Open 从 r 解析并校验固件包的结构（方案 §5.2 步骤 3）。
//
// size 是整包字节数（调用方已落盘/已知长度）。Open **不**解压 artifact：
// 条目数、解压总量、单条目压缩比三项全部取自中央目录的声明值（§4.5），
// 因此一个 1KB→100MB 的 zip bomb 在解压任何一个字节之前就被拒。
func Open(r io.ReaderAt, size int64, limits Limits) (*Package, error) {
	if r == nil || size <= 0 {
		return nil, newError(CodePkgInvalid, "firmware package is empty")
	}
	lim := limits.normalized()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, newError(CodePkgInvalid, "not a ZIP container: %v", err)
	}
	if err := checkEntries(zr.File, lim); err != nil {
		return nil, err
	}

	manifestFile := entryByName(zr.File, ManifestEntry)
	if manifestFile == nil {
		return nil, newError(CodePkgInvalid, "package has no %s entry (entries: %s)", ManifestEntry, entryNames(zr.File))
	}
	raw, err := readLimited(manifestFile, lim.MaxManifest)
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, newError(CodePkgInvalid, "%s is not valid JSON: %v", ManifestEntry, err)
	}
	logUnknownManifestKeys(raw)
	if err := manifest.validate(); err != nil {
		return nil, err
	}

	// INV-1 收口：包内恰好两条 —— 清单 + 清单声明的 artifact，无未声明条目。
	// 重名条目会让 len(entries) 小于 len(zr.File)，这一条同时拦住重名。
	entries := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	artifactFile := entries[manifest.Artifact.Name]
	if artifactFile == nil || len(entries) != 2 {
		return nil, newError(CodePkgInvalid, "package entries are %s, want exactly %q and %q",
			entryNames(zr.File), ManifestEntry, manifest.Artifact.Name)
	}

	return &Package{manifest: manifest, artifact: artifactFile}, nil
}

// Manifest 返回清单的只读副本。
//
// 返回副本而不是指针：调用方（handler / CLI）不该能改到包内状态，
// 而 PSRAM 是 *bool，浅拷贝会把指针一起共享出去。
func (p *Package) Manifest() *Manifest {
	if p == nil {
		return nil
	}
	cp := p.manifest
	if cp.Identity.PSRAM != nil {
		value := *cp.Identity.PSRAM
		cp.Identity.PSRAM = &value
	}
	return &cp
}

// ExtractArtifactTo 流式写出 artifact 并返回 (字节数, sha256 hex)。
//
// **只解出、不比较**：INV-2 的比较与错误出口（FW_PKG_DIGEST_MISMATCH）留在 handler，
// 否则同一件事会在两处各判一遍、两处各有一份错误文案（评审 B-2）。
func (p *Package) ExtractArtifactTo(w io.Writer) (int64, string, error) {
	if p == nil || p.artifact == nil {
		return 0, "", newError(CodePkgInvalid, "package has no artifact to extract")
	}
	src, err := p.artifact.Open()
	if err != nil {
		return 0, "", newError(CodePkgInvalid, "open artifact %q: %v", p.artifact.Name, err)
	}
	defer func() { _ = src.Close() }()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(w, hasher), src)
	if err != nil {
		// zip 读端在 CRC 不符时于 EOF 返回 ErrChecksum：落盘字节必须与清单一致，
		// 所以这里一律按结构不合法处理（handler 不会拿到一份「半份」artifact 继续用）。
		return size, "", newError(CodePkgInvalid, "read artifact %q: %v", p.artifact.Name, err)
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

// checkEntries 只用中央目录（不解压）就完成 INV-4 / INV-5 的全部判定。
func checkEntries(files []*zip.File, lim Limits) error {
	if len(files) > lim.MaxEntries {
		return newError(CodePkgTooLarge, "package has %d entries, limit is %d", len(files), lim.MaxEntries)
	}
	if len(files) != 2 {
		return newError(CodePkgInvalid, "package has %d entries, want exactly 2 (%s + artifact)", len(files), ManifestEntry)
	}
	seen := make(map[string]bool, len(files))
	var total int64
	for _, f := range files {
		if !entryNamePattern.MatchString(f.Name) {
			return newError(CodePkgInvalid, "entry name %q is not a flat lowercase key (^[a-z0-9][a-z0-9._-]{0,63}$)", f.Name)
		}
		if seen[f.Name] {
			return newError(CodePkgInvalid, "duplicate entry name %q", f.Name)
		}
		seen[f.Name] = true
		// 压缩方法白名单：0=STORE、8=DEFLATE；bzip2/lzma/aes 一律拒绝（§4.5）。
		// AES 加密同时置 flag bit 0，方法号会是 99，两条判定取其一即可拦住。
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return newError(CodePkgInvalid, "entry %q uses compression method %d, only 0 (STORE) and 8 (DEFLATE) are allowed", f.Name, f.Method)
		}
		if f.Flags&0x1 != 0 {
			return newError(CodePkgInvalid, "entry %q is encrypted", f.Name)
		}
		if f.UncompressedSize64 > uint64(lim.MaxUncompressed) {
			return newError(CodePkgTooLarge, "entry %q declares %d uncompressed bytes, limit is %d", f.Name, f.UncompressedSize64, lim.MaxUncompressed)
		}
		total += int64(f.UncompressedSize64)
		if f.UncompressedSize64 > 0 {
			if f.CompressedSize64 == 0 || f.UncompressedSize64 > uint64(lim.MaxRatio)*f.CompressedSize64 {
				return newError(CodePkgTooLarge, "entry %q compression ratio %d:%d exceeds %d:1",
					f.Name, f.UncompressedSize64, f.CompressedSize64, lim.MaxRatio)
			}
		}
	}
	if total > lim.MaxUncompressed {
		return newError(CodePkgTooLarge, "package declares %d uncompressed bytes, limit is %d", total, lim.MaxUncompressed)
	}
	return nil
}

// readLimited 读出条目内容，超过 limit（清单上限 64 KiB）即拒绝。
//
// 这一项不是 INV-5 的「解压总量/压缩比」，所以给 FW_PKG_INVALID 而不是 TOO_LARGE：
// 清单超标说明这不是一个合规的包，而不是「包异常膨胀」。
func readLimited(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, newError(CodePkgInvalid, "%s declares %d bytes, limit is %d", f.Name, f.UncompressedSize64, limit)
	}
	src, err := f.Open()
	if err != nil {
		return nil, newError(CodePkgInvalid, "open %s: %v", f.Name, err)
	}
	defer func() { _ = src.Close() }()
	raw, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, newError(CodePkgInvalid, "read %s: %v", f.Name, err)
	}
	if int64(len(raw)) > limit {
		return nil, newError(CodePkgInvalid, "%s is larger than %d bytes", f.Name, limit)
	}
	return raw, nil
}

// logUnknownManifestKeys 记下未知顶层键后**照常通过**（方案 §4.6）：
// 清单是长期格式，新增可选字段不升 format_version；结构性变更才升版本。
func logUnknownManifestKeys(raw []byte) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return // 已经通过同一份字节的 Manifest 解析，这里不会失败
	}
	var unknown []string
	for key := range top {
		if !knownManifestKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		logger.Warnf("[firmwarepkg] ignoring unknown %s key(s): %s", ManifestEntry, strings.Join(unknown, ", "))
	}
}

func entryByName(files []*zip.File, name string) *zip.File {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func entryNames(files []*zip.File) string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
