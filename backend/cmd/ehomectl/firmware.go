package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/firmwareid"
	"gorm.io/gorm"
)

// runFirmwareCLI 是固件身份相关的运维入口（方案 T0.2 / §5.6 步骤 7）。
func runFirmwareCLI() {
	if len(os.Args) < 3 {
		fatal("usage: ehomectl firmware <meta-gen|migrate-report>")
	}
	switch os.Args[2] {
	case "meta-gen":
		runFirmwareMetaGen(os.Args[3:])
	case "migrate-report":
		runFirmwareMigrateReport(os.Args[3:])
	default:
		fatal("usage: ehomectl firmware <meta-gen|migrate-report>")
	}
}

// runFirmwareMetaGen 给存量 .bin 补出同名 .meta.json（方案 §4.2）。
//
// 镜像头能自证 chip / flash_mb / version / idf_ver / sha256，**只有 psram 不能**
// ——它是构建期信息，必须人工给定。补出 sidecar 后走正常上传即可。
func runFirmwareMetaGen(args []string) {
	fs := flag.NewFlagSet("firmware meta-gen", flag.ExitOnError)
	binPath := fs.String("bin", "", "path to the firmware .bin (required)")
	psram := fs.String("psram", "", "yes|no: the one bit the image header cannot prove (required)")
	_ = fs.Parse(args)
	if *binPath == "" || *psram == "" {
		fatal("usage: ehomectl firmware meta-gen --bin <path> --psram yes|no")
	}
	psramEnabled, err := parsePSRAMFlag(*psram)
	if err != nil {
		fatal(err.Error())
	}

	file, err := os.Open(*binPath)
	if err != nil {
		fatal(fmt.Sprintf("open %s: %v", *binPath, err))
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		fatal(fmt.Sprintf("stat %s: %v", *binPath, err))
	}
	ident, err := firmwareid.ReadIdentity(file, info.Size())
	if err != nil {
		fatal(fmt.Sprintf("parse %s: %v (not a supported ehome_collector image)", *binPath, err))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		fatal(fmt.Sprintf("seek %s: %v", *binPath, err))
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		fatal(fmt.Sprintf("hash %s: %v", *binPath, err))
	}

	profile := firmwareid.BuildProfile(ident.Chip, ident.FlashMB, psramEnabled)
	sidecar := firmwareid.Sidecar{
		Profile: profile,
		Chip:    ident.Chip,
		FlashMB: ident.FlashMB,
		PSRAM:   &psramEnabled,
		Version: ident.Version,
		IDFVer:  ident.IDFVersion,
		SHA256:  hex.EncodeToString(hasher.Sum(nil)),
	}
	outPath := strings.TrimSuffix(*binPath, filepath.Ext(*binPath)) + ".meta.json"
	encoded, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		fatal(fmt.Sprintf("encode sidecar: %v", err))
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		fatal(fmt.Sprintf("write %s: %v", outPath, err))
	}
	fmt.Printf("wrote %s\n  chip=%s flash_mb=%d psram=%s version=%s idf_ver=%s profile=%s\n",
		outPath, sidecar.Chip, sidecar.FlashMB, sidecar.PSRAMValue(), sidecar.Version, sidecar.IDFVer, sidecar.Profile)
}

// runFirmwareMigrateReport 复现启动迁移报告（只读，方案 §5.6 步骤 7）。
//
// 刻意用 connectDBNoMigrate：本命令承诺不改库。它要求库已由新版本服务迁移过
// （schema 检查会明确提示），因此在**维护窗口里先起一次新容器**再跑它。
func runFirmwareMigrateReport(args []string) {
	fs := flag.NewFlagSet("firmware migrate-report", flag.ExitOnError)
	_ = fs.Parse(args)

	db := connectDBNoMigrate()
	requireFirmwareIdentitySchema(db, "firmware migrate-report")
	report, err := dbschema.ReportFirmwareIdentity(db)
	if err != nil {
		fatal(err.Error())
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(fmt.Sprintf("encode report: %v", err))
	}
	fmt.Println(string(encoded))
	fmt.Fprintf(os.Stderr, "legacy=%d duplicate=%d (legacy rows must be re-uploaded with a sidecar)\n",
		len(report.Legacy), len(report.Duplicate))
}

// requireFirmwareIdentitySchema 给只读命令一个可执行的失败信息。
//
// 只读命令不跑迁移（本仓既有纪律：自称只读的工具绝不偷偷改 schema），
// 所以列不存在时必须说清「先让新版本服务完成迁移」，而不是抛一句
// "no such column: identity_state"。
func requireFirmwareIdentitySchema(db *gorm.DB, command string) {
	if !db.Migrator().HasTable("firmwares") {
		fatal(fmt.Sprintf("%s: firmwares table does not exist yet; start the new backend once so migrations run", command))
	}
	for _, column := range []string{"identity_state", "elf_sha256", "channel"} {
		if !db.Migrator().HasColumn("firmwares", column) {
			fatal(fmt.Sprintf("%s: firmwares.%s is missing; start the new backend once so firmware identity migrations run, then retry", command, column))
		}
	}
}

// parsePSRAMFlag 只接受 yes/no（方案 §5.2：固件列只有两值）。
func parsePSRAMFlag(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("--psram must be yes or no (got %q): the image header has no PSRAM bit", value)
	}
}
