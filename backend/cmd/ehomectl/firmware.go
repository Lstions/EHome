package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/firmwarepkg"
)

// firmwareUsage 是本子命令唯一的用法串：main.go 的 usage 直接引用它。
//
// 从前 ehomectl 里有两份 usage（main.go:71 与 firmware.go:22/30），
// 改一处忘一处必然腐坏（§5.8 #7），所以收敛成一个常量。
const firmwareUsage = "usage: ehomectl firmware verify --pkg <file.ehfw>\n" +
	"       ehomectl firmware inspect --pkg <file.ehfw>"

// runFirmwareCLI 是固件包的离线运维入口（方案 §5.2 / §9.2）。
//
// Go 侧**只做 verify / inspect**：打包的唯一实现是
// esp32-collector/tools/pack_firmware.py，再写一份 Go 打包器只会多一个漂移源（评审 B-7）。
func runFirmwareCLI() {
	if len(os.Args) < 3 {
		fatal(firmwareUsage)
	}
	switch os.Args[2] {
	case "verify":
		runFirmwareVerify(os.Args[3:])
	case "inspect":
		runFirmwareInspect(os.Args[3:])
	default:
		fatal(firmwareUsage)
	}
}

// runFirmwareVerify 用与服务端**同一套规则**离线校验一个包，不通过即非零退出。
func runFirmwareVerify(args []string) {
	pkgPath := firmwarePkgFlag("firmware verify", args)
	info, err := loadFirmwarePackage(pkgPath)
	if err != nil {
		fatal(fmt.Sprintf("FAIL %s: %v", pkgPath, err))
	}
	if err := info.checkAgainstManifest(); err != nil {
		fatal(fmt.Sprintf("FAIL %s: %v", pkgPath, err))
	}
	manifest := info.Manifest
	fmt.Printf("OK %s\n", pkgPath)
	fmt.Printf("  artifact: %s (%d bytes, sha256 %s)\n", manifest.Artifact.Name, info.ArtifactSize, info.ArtifactSHA)
	fmt.Printf("  image:    version=%s chip=%s flash_mb=%d psram=%t profile=%s\n",
		info.Image.Version, info.Image.Chip, info.Image.FlashMB, manifest.Identity.PSRAMEnabled(),
		firmwareid.BuildProfile(info.Image.Chip, info.Image.FlashMB, manifest.Identity.PSRAMEnabled()))
	fmt.Printf("  manifest: format=%s format_version=%d packer=%s %s\n",
		manifest.Format, manifest.FormatVersion, manifest.Packer.Name, manifest.Packer.Version)
}

// runFirmwareInspect 打印包的清单 + 从 artifact 现读的镜像身份（不做通过判定）。
//
// 与 verify 的分工：verify 回答「能不能上传」，inspect 回答「这个包里到底是什么」。
// 摘要不符时 inspect 照样打印，只把 digest_ok 标成 false —— 排障正需要看到这份证据。
func runFirmwareInspect(args []string) {
	pkgPath := firmwarePkgFlag("firmware inspect", args)
	info, err := loadFirmwarePackage(pkgPath)
	if err != nil {
		fatal(fmt.Sprintf("inspect %s: %v", pkgPath, err))
	}
	result := struct {
		Package  string                `json:"package"`
		Manifest *firmwarepkg.Manifest `json:"manifest"`
		Artifact firmwareArtifactDump  `json:"artifact"`
		Image    firmwareImageDump     `json:"image"`
	}{
		Package:  pkgPath,
		Manifest: info.Manifest,
		Artifact: firmwareArtifactDump{
			Name:       info.Manifest.Artifact.Name,
			SizeBytes:  info.ArtifactSize,
			SHA256:     info.ArtifactSHA,
			DigestOkay: info.digestMatches(),
		},
		Image: firmwareImageDump{
			Chip:         info.Image.Chip,
			FlashMB:      info.Image.FlashMB,
			Version:      info.Image.Version,
			Project:      info.Image.Project,
			ElfSHA256:    info.Image.ElfSHA256,
			IDFVersion:   info.Image.IDFVersion,
			BuiltAtRaw:   info.Image.BuiltAtRaw,
			SizeBytes:    info.Image.SizeBytes,
			BuildProfile: firmwareid.BuildProfile(info.Image.Chip, info.Image.FlashMB, info.Manifest.Identity.PSRAMEnabled()),
		},
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(fmt.Sprintf("encode inspect result: %v", err))
	}
	fmt.Println(string(encoded))
}

// firmwarePackageInfo 是「打开并解出过一个包」的结果。
type firmwarePackageInfo struct {
	Manifest     *firmwarepkg.Manifest
	Image        *firmwareid.Identity
	ArtifactSize int64
	ArtifactSHA  string
}

// firmwareArtifactDump / firmwareImageDump 是 inspect 的 JSON 形状。
type firmwareArtifactDump struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	DigestOkay bool   `json:"digest_ok"`
}

type firmwareImageDump struct {
	Chip         string `json:"chip"`
	FlashMB      uint16 `json:"flash_mb"`
	Version      string `json:"version"`
	Project      string `json:"project"`
	ElfSHA256    string `json:"elf_sha256"`
	IDFVersion   string `json:"idf_ver"`
	BuiltAtRaw   string `json:"built_at_raw"`
	SizeBytes    int64  `json:"size_bytes"`
	BuildProfile string `json:"build_profile"`
}

func firmwarePkgFlag(command string, args []string) string {
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	pkgPath := fs.String("pkg", "", "path to the .ehfw firmware package (required)")
	_ = fs.Parse(args)
	if *pkgPath == "" {
		fatal("usage: ehomectl " + command + " --pkg <file.ehfw>")
	}
	return *pkgPath
}

// loadFirmwarePackage 打开包、解出 artifact 到临时文件、解析镜像身份，然后清理临时文件。
//
// 解压落临时文件而不是内存：与服务端同一条路径，也顺便验证「这个包真的能解出
// 一个可解析的 ESP 镜像」。临时文件用 defer 删，任何失败路径都不留残余。
func loadFirmwarePackage(pkgPath string) (*firmwarePackageInfo, error) {
	container, err := os.Open(pkgPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = container.Close() }()
	info, err := container.Stat()
	if err != nil {
		return nil, err
	}
	pkg, err := firmwarepkg.Open(container, info.Size(), firmwarepkg.DefaultLimits())
	if err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "ehomectl-fwverify-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	artifactPath := filepath.Join(tempDir, "artifact")
	artifact, err := os.Create(artifactPath)
	if err != nil {
		return nil, err
	}
	size, digest, extractErr := pkg.ExtractArtifactTo(artifact)
	closeErr := artifact.Close()
	if extractErr != nil {
		return nil, extractErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	image, err := os.Open(artifactPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = image.Close() }()
	ident, err := firmwareid.ReadIdentity(image, size)
	if err != nil {
		return nil, err
	}
	return &firmwarePackageInfo{
		Manifest:     pkg.Manifest(),
		Image:        ident,
		ArtifactSize: size,
		ArtifactSHA:  digest,
	}, nil
}

// digestMatches 是 INV-2 的判据（大小与 sha256 都要对）。
func (i *firmwarePackageInfo) digestMatches() bool {
	return i.ArtifactSize == i.Manifest.Artifact.Size &&
		strings.EqualFold(i.ArtifactSHA, i.Manifest.Artifact.SHA256)
}

// checkAgainstManifest 复现服务端的 INV-2 + INV-3，让离线结论与线上一致（G6）。
func (i *firmwarePackageInfo) checkAgainstManifest() error {
	if !i.digestMatches() {
		return &firmwareid.Error{
			Code: firmwarepkg.CodePkgDigestMismatch,
			Detail: fmt.Sprintf("artifact is %d bytes / sha256 %s, manifest declares %d / %s",
				i.ArtifactSize, i.ArtifactSHA, i.Manifest.Artifact.Size, i.Manifest.Artifact.SHA256),
		}
	}
	if want := firmwareid.BuildProfile(i.Image.Chip, i.Image.FlashMB, i.Manifest.Identity.PSRAMEnabled()); want != i.Manifest.Identity.Profile {
		return &firmwareid.Error{
			Code: firmwarepkg.CodePkgConflict,
			Detail: fmt.Sprintf("manifest profile %q does not match image (%s, %dMB, psram=%t → %q)",
				i.Manifest.Identity.Profile, i.Image.Chip, i.Image.FlashMB, i.Manifest.Identity.PSRAMEnabled(), want),
		}
	}
	return nil
}
