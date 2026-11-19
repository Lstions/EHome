package firmwarepkg

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehome/backend/internal/firmwareid"
)

// ── golden vectors（方案 §8.2）──
//
// 打包在 Python（构建机），解析在 Go（服务端）。两份实现必然存在，风险是「各自
// 自洽却互相不通」；本仓在帧协议上吃过这个亏，所以照 protocol/vectors 的做法让
// **两侧消费同一批向量**：Python 侧 test_pack_firmware.py，Go 侧就是本文件。
//
// 这些用例**刻意不 skip**：向量缺失/改名就让用例红 —— 那是「集成断了」的信号，
// 跳过会把断链伪装成绿灯。
const vectorsDir = "../../../protocol/firmware-package/vectors"

// vectorPath 拼出向量文件路径，并在整个目录缺失时给出可执行的提示。
//
// 缺失就红（不 skip）：这不是「本机没装向量」的可选依赖，而是 §8.2 的集成契约。
//
// 注意 protocol/ 在 Go 模块（backend/）之外，所以跑测试必须把**仓库根**挂进容器
// （-v <repo>:/repo -w /repo/backend）；只挂 backend/ 时 ../../../protocol 会落到
// /protocol，下面这句报错就是给那种跑法看的。
func vectorPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(vectorsDir, name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("golden vectors not reachable at %s: %v\n"+
			"  protocol/ lives outside the Go module (backend/): mount the repository root, e.g.\n"+
			"  docker run --rm -v <repo>:/repo -w /repo/backend ... go test ./internal/firmwarepkg/...",
			path, err)
	}
	return path
}

// vectorCase 对应 cases.json 的一条（schema 见该文件；两侧同名）。
type vectorCase struct {
	File         string `json:"file"`
	OpenError    string `json:"open_error"`
	HandlerError string `json:"handler_error"`
	ExpectedFile string `json:"expected_file"`
	Note         string `json:"note"`
}

type vectorCases struct {
	Generator     string       `json:"generator"`
	Format        string       `json:"format"`
	FormatVersion int          `json:"format_version"`
	Cases         []vectorCase `json:"cases"`
}

// vectorExpected 是 ok_*.expected.json 的形状。
type vectorExpected struct {
	Manifest Manifest `json:"manifest"`
	Image    struct {
		Chip       string `json:"chip"`
		ChipID     uint16 `json:"chip_id"`
		FlashMB    uint16 `json:"flash_mb"`
		Version    string `json:"version"`
		Project    string `json:"project"`
		IDFVersion string `json:"idf_version"`
		ElfSHA256  string `json:"elf_sha256"`
		SizeBytes  int64  `json:"size_bytes"`
		SHA256     string `json:"sha256"`
	} `json:"image"`
}

func loadVectorCases(t *testing.T) vectorCases {
	t.Helper()
	raw, err := os.ReadFile(vectorPath(t, "cases.json"))
	if err != nil {
		t.Fatalf("read golden vectors cases.json: %v (run protocol/firmware-package/vectors/gen_vectors.py)", err)
	}
	var cases vectorCases
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse cases.json: %v", err)
	}
	if len(cases.Cases) == 0 {
		t.Fatal("cases.json lists no cases; the vector set is empty")
	}
	return cases
}

// openVector 打开并解析一个向量包，返回包与读句柄（调用方负责 Close）。
func openVector(t *testing.T, name string, limits Limits) (*Package, *os.File, error) {
	t.Helper()
	path := vectorPath(t, name)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open vector %s: %v", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("stat vector %s: %v", name, err)
	}
	pkg, openErr := Open(file, info.Size(), limits)
	return pkg, file, openErr
}

// TestVectors_Cases 逐条消费 cases.json：
//
//	open_error    → firmwarepkg.Open 的错误码（空串 = 必须成功）
//	handler_error → 摘要比对那一步（§5.2 步骤 5）的错误码（空串 = 摘要必须一致）
//
// 断言同时覆盖「错误码相等」与「字段级相等」：只比码会让「码对了但内容错了」漏网。
func TestVectors_Cases(t *testing.T) {
	cases := loadVectorCases(t)
	if cases.Format != FormatName || cases.FormatVersion != FormatVersion {
		t.Fatalf("cases.json format = %q/%d, want %q/%d", cases.Format, cases.FormatVersion, FormatName, FormatVersion)
	}
	for _, tc := range cases.Cases {
		t.Run(tc.File, func(t *testing.T) {
			pkg, file, err := openVector(t, tc.File, DefaultLimits())
			defer func() { _ = file.Close() }()

			if tc.OpenError != "" {
				if err == nil {
					t.Fatalf("Open succeeded, want error %s (%s)", tc.OpenError, tc.Note)
				}
				if got := firmwareid.Code(err); got != tc.OpenError {
					t.Fatalf("Open error code = %q, want %q (err: %v)", got, tc.OpenError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open failed, want success: %v (%s)", err, tc.Note)
			}

			manifest := pkg.Manifest()
			if manifest == nil {
				t.Fatal("Manifest() = nil")
			}
			size, digest, err := pkg.ExtractArtifactTo(io.Discard)
			if err != nil {
				t.Fatalf("ExtractArtifactTo: %v", err)
			}
			digestMatches := size == manifest.Artifact.Size && strings.EqualFold(digest, manifest.Artifact.SHA256)
			switch tc.HandlerError {
			case "":
				if !digestMatches {
					t.Fatalf("digest mismatch: measured %d/%s, manifest %d/%s (%s)",
						size, digest, manifest.Artifact.Size, manifest.Artifact.SHA256, tc.Note)
				}
			case CodePkgDigestMismatch:
				if digestMatches {
					t.Fatalf("digest matched, want %s (%s)", CodePkgDigestMismatch, tc.Note)
				}
			default:
				t.Fatalf("unsupported handler_error %q in cases.json", tc.HandlerError)
			}
		})
	}
}

// TestVectors_OKExpectedFields 是正例的**字段级**对照：清单全文 + 镜像自证身份。
//
// 只看「Open 没报错」不够 —— 清单字段解析错位（例如 psram 与 profile 换了位置）
// 一样能过结构校验，却会让服务端把档位写错。
func TestVectors_OKExpectedFields(t *testing.T) {
	const okFile = "ok_c6-n8.ehfw"
	raw, err := os.ReadFile(vectorPath(t, "ok_c6-n8.expected.json"))
	if err != nil {
		t.Fatalf("read ok_c6-n8.expected.json: %v", err)
	}
	var want vectorExpected
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse ok_c6-n8.expected.json: %v", err)
	}

	pkg, file, err := openVector(t, okFile, DefaultLimits())
	defer func() { _ = file.Close() }()
	if err != nil {
		t.Fatalf("Open(%s): %v", okFile, err)
	}

	// 1) 清单逐字段相等。
	got := pkg.Manifest()
	switch {
	case got.Format != want.Manifest.Format:
		t.Fatalf("format = %q, want %q", got.Format, want.Manifest.Format)
	case got.FormatVersion != want.Manifest.FormatVersion:
		t.Fatalf("format_version = %d, want %d", got.FormatVersion, want.Manifest.FormatVersion)
	case got.Packer != want.Manifest.Packer:
		t.Fatalf("packer = %+v, want %+v", got.Packer, want.Manifest.Packer)
	case got.Artifact != want.Manifest.Artifact:
		t.Fatalf("artifact = %+v, want %+v", got.Artifact, want.Manifest.Artifact)
	case got.Identity.Profile != want.Manifest.Identity.Profile:
		t.Fatalf("identity.profile = %q, want %q", got.Identity.Profile, want.Manifest.Identity.Profile)
	case got.Identity.PSRAM == nil:
		t.Fatal("identity.psram is missing from the parsed manifest")
	case got.Identity.PSRAMEnabled() != want.Manifest.Identity.PSRAMEnabled():
		t.Fatalf("identity.psram = %t, want %t", got.Identity.PSRAMEnabled(), want.Manifest.Identity.PSRAMEnabled())
	}

	// 2) artifact 解出来逐字节等于清单声明的大小与摘要。
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.bin")
	out, err := os.Create(artifactPath)
	if err != nil {
		t.Fatalf("create artifact file: %v", err)
	}
	size, digest, err := pkg.ExtractArtifactTo(out)
	closeErr := out.Close()
	if err != nil {
		t.Fatalf("ExtractArtifactTo: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close artifact file: %v", closeErr)
	}
	if size != want.Image.SizeBytes || size != want.Manifest.Artifact.Size {
		t.Fatalf("artifact size = %d, want %d/%d", size, want.Image.SizeBytes, want.Manifest.Artifact.Size)
	}
	if !strings.EqualFold(digest, want.Image.SHA256) || !strings.EqualFold(digest, want.Manifest.Artifact.SHA256) {
		t.Fatalf("artifact sha256 = %q, want %q", digest, want.Image.SHA256)
	}

	// 3) 镜像自证身份（服务端 §5.2 步骤 6 的同一条路径）。
	artifact, err := os.Open(artifactPath)
	if err != nil {
		t.Fatalf("reopen artifact: %v", err)
	}
	defer func() { _ = artifact.Close() }()
	ident, err := firmwareid.ReadIdentity(artifact, size)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if ident.Chip != want.Image.Chip || ident.ChipID != want.Image.ChipID || ident.FlashMB != want.Image.FlashMB {
		t.Fatalf("image chip = %q/%#x/flash %d, want %q/%#x/%d",
			ident.Chip, ident.ChipID, ident.FlashMB, want.Image.Chip, want.Image.ChipID, want.Image.FlashMB)
	}
	if ident.Version != want.Image.Version || ident.Project != want.Image.Project || ident.IDFVersion != want.Image.IDFVersion {
		t.Fatalf("image version/project/idf = %q/%q/%q, want %q/%q/%q",
			ident.Version, ident.Project, ident.IDFVersion, want.Image.Version, want.Image.Project, want.Image.IDFVersion)
	}
	if ident.ElfSHA256 != want.Image.ElfSHA256 {
		t.Fatalf("image elf_sha256 = %q, want %q", ident.ElfSHA256, want.Image.ElfSHA256)
	}

	// 4) INV-3：清单的 profile 必须能被镜像的 (chip, flash) + 清单的 psram 还原。
	if derived := firmwareid.BuildProfile(ident.Chip, ident.FlashMB, got.Identity.PSRAMEnabled()); derived != got.Identity.Profile {
		t.Fatalf("BuildProfile(...) = %q, manifest profile = %q", derived, got.Identity.Profile)
	}
}

// TestOpen_ZipBomb：200 KiB 全零 → Deflate 约 200 B，压缩比 > 100:1（INV-5）。
//
// 「解压前拒绝」在实现上是结构性的：三项上限全部取自中央目录的声明值，
// checkEntries 跑在任何条目被 Open() 之前。这里再断言错误文案确实来自压缩比分支，
// 而不是被别的上限（例如解压总量）顺手拦下 —— 那样两条规则的 coverage 会混在一起。
func TestOpen_ZipBomb(t *testing.T) {
	const bomb = "bad_compression_ratio.ehfw"
	_, file, err := openVector(t, bomb, DefaultLimits())
	defer func() { _ = file.Close() }()
	if err == nil {
		t.Fatal("compression-ratio bomb was accepted")
	}
	if got := firmwareid.Code(err); got != CodePkgTooLarge {
		t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgTooLarge, err)
	}
	if !strings.Contains(err.Error(), "compression ratio") {
		t.Fatalf("error %v did not come from the ratio check", err)
	}
}
