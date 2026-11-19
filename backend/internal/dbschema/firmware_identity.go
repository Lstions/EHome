package dbschema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// ── 固件身份 schema（方案《固件与平台更新机制》§5.6 / 附录 B）──
//
// 为什么不能只靠 AutoMigrate：
//  1. GORM 不删列。stable / target_model 必须显式 DROP（v1.4 明确不留死列/双真源）；
//  2. 唯一键是**部分索引**（elf_sha256 WHERE elf_sha256<>'' AND deleted_at IS NULL），
//     AutoMigrate 不会建它；
//  3. 建索引前必须先把历史重复数据去重，否则启动会被 unique violation 掐死
//     （方案 §9.4：不允许把重复数据留给索引去发现）。
//
// 调用点（两处清单必须同步，本仓既有纪律）：
//   - internal/database/gorm.go   的 AutoMigrate 收尾（生产启动）
//   - testutil/db.go              的 AutoMigrate 之后（测试库）
//
// 失败语义：任一错误 → 返回给调用方 → 启动失败（与 abortStartup 口径一致）。
// 整体单事务：PG 的 DDL 可事务、SQLite 逐条 DDL 也在事务内，失败整体回滚不留半态。

// FirmwareDir 是固件落盘目录（相对服务进程 cwd），与 api 上传处同源。
const FirmwareDir = "firmwares"

// 身份状态取值（方案 §5.2）：只有 verified 的行可选。
const (
	IdentityVerified  = "verified"
	IdentityLegacy    = "legacy"
	IdentityDuplicate = "duplicate"
)

// FirmwareIdentityRow 是迁移报告里的一行。
//
// legacy 行有两种：elf_sha256 非空 = 身份已解析、只缺 sidecar 的 psram；
// elf_sha256 为空 = 二进制缺失/不可解析，身份仍未知。报告里把两者的诊断字段都带上，
// 运维才知道「补 sidecar 重传」还是「找回原始二进制再来一次」。
type FirmwareIdentityRow struct {
	ID            uint   `json:"id"`
	Version       string `json:"version"`
	Filename      string `json:"filename"`
	StoragePath   string `json:"storage_path"`
	IdentityState string `json:"identity_state"`
	Chip          string `json:"chip,omitempty"`
	FlashMB       uint16 `json:"flash_mb,omitempty"`
	Psram         string `json:"psram,omitempty"`
	ElfSHA256     string `json:"elf_sha256,omitempty"`
}

// FirmwareIdentityReport 是步骤 7 的产物：legacy 行需重传，duplicate 行是历史重复。
type FirmwareIdentityReport struct {
	Legacy                 []FirmwareIdentityRow `json:"legacy"`
	Duplicate              []FirmwareIdentityRow `json:"duplicate"`
	Backfilled             int                   `json:"backfilled"`
	VersionOverridden      int                   `json:"version_overridden"`
	Deduplicated           int                   `json:"deduplicated"`
	NodeHardwareBackfilled int                   `json:"node_hardware_backfilled"`
}

// MigrateFirmwareIdentity 按方案 §5.6 的顺序跑完 0–7 步，全程单事务。
func MigrateFirmwareIdentity(db *gorm.DB, firmwareDir string) (FirmwareIdentityReport, error) {
	if db == nil {
		return FirmwareIdentityReport{}, fmt.Errorf("firmware identity: database is nil")
	}
	dir := strings.TrimSpace(firmwareDir)
	if dir == "" {
		dir = FirmwareDir
	}

	var report FirmwareIdentityReport
	err := db.Transaction(func(tx *gorm.DB) error {
		// 0) 增列（幂等）。索引/回填/删列都不在这里。
		if err := tx.AutoMigrate(&models.Firmware{}, &models.Node{}, &models.OTATask{}); err != nil {
			return fmt.Errorf("firmware identity: step 0 automigrate: %w", err)
		}
		// 1) 回填身份（按 storage_path → filename 找二进制解析）：写 chip/flash/elf/version
		//    等诊断列，但**保持 legacy**——psram 不可导出，不能让行变成「半可信」（R3）。
		backfilled, err := BackfillFirmwareIdentity(tx, dir)
		if err != nil {
			return err
		}
		// 2) 以镜像内 version 覆盖库内 version（镜像为准，差异写日志）。
		overridden, err := EnforceImageVersion(tx, dir)
		if err != nil {
			return err
		}
		// 3) 按 elf_sha256 去重：保留最小 id，其余置空标 duplicate。
		//    必须在 5) 建唯一索引之前（方案 §9.4）。
		deduped, err := DedupeFirmwareIdentity(tx)
		if err != nil {
			return err
		}
		// 4) DROP stable / target_model（不可逆）。
		if err := DropLegacyFirmwareColumns(tx); err != nil {
			return err
		}
		// 5) 建唯一部分索引 + 普通索引。
		if err := EnsureFirmwareIdentityIndex(tx); err != nil {
			return err
		}
		// 6) 回填 nodes 硬件（解析失败留空 = 未识别）。
		nodeBackfilled, err := BackfillNodeHardware(tx)
		if err != nil {
			return err
		}
		// 7) 输出 legacy / duplicate 清单。
		rep, err := ReportFirmwareIdentity(tx)
		if err != nil {
			return err
		}
		rep.Backfilled = backfilled
		rep.VersionOverridden = overridden
		rep.Deduplicated = deduped
		rep.NodeHardwareBackfilled = nodeBackfilled
		report = rep
		return nil
	})
	if err != nil {
		return FirmwareIdentityReport{}, err
	}

	logger.Infof("[firmware-migrate] done: backfilled=%d version_overridden=%d deduplicated=%d node_hw_backfilled=%d legacy=%d duplicate=%d",
		report.Backfilled, report.VersionOverridden, report.Deduplicated, report.NodeHardwareBackfilled,
		len(report.Legacy), len(report.Duplicate))
	for _, row := range report.Legacy {
		detail := "identity unknown, find the original binary first"
		if strings.TrimSpace(row.ElfSHA256) != "" {
			detail = fmt.Sprintf("identity parsed (chip=%s flash_mb=%d) but psram is only known at build time", row.Chip, row.FlashMB)
		}
		logger.Warnf("[firmware-migrate] legacy firmware id=%d version=%q file=%q: %s — run ehomectl firmware meta-gen --bin <file> --psram yes|no, then re-upload (it completes this row in place)", row.ID, row.Version, row.Filename, detail)
	}
	for _, row := range report.Duplicate {
		logger.Warnf("[firmware-migrate] duplicate firmware id=%d version=%q file=%q: same elf_sha256 as an older row, not selectable", row.ID, row.Version, row.Filename)
	}
	return report, nil
}

// BackfillFirmwareIdentity 解析 identity_state='legacy' 的行的二进制，回填身份列。
//
// **状态保持 legacy，绝不标 verified**（R3「未知即不可选」）：
// 镜像头里没有 PSRAM 位（方案判断 1），回填行拿不到 psram ⇒ 不具备可选资格。
// 标成 verified 会造出「verified 但 psram=”」的半可信状态，而闸门的
// PSRAM 两条判定（要求 yes / 拒绝 no+yes）对 ” 都不命中 ⇒ 等于静默放行，
// 正是 v1.4 要关掉的那条误刷通道。补齐 sidecar 重传是它唯一的出口
// （api 上传的 legacy 就地补齐分支），因此这里只写**诊断与去重**用的列。
//
// version 也按镜像写：RK-8「回填与库内版本不一致 → 镜像为准」的意图不能因为
// 状态从 verified 改成 legacy 而失效（否则列表会给 legacy 行显示一个错版本）。
//
// 已经有 elf_sha256 的行直接跳过：上一轮已经写好了身份，行之所以还是 legacy
// 是因为缺 psram，再解析一次也不会有新信息 ⇒ 保证重跑真幂等（Backfilled=0）。
func BackfillFirmwareIdentity(tx *gorm.DB, firmwareDir string) (int, error) {
	var rows []models.Firmware
	if err := tx.Where("identity_state = ? OR identity_state IS NULL OR identity_state = ''", IdentityLegacy).
		Order("id ASC").Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("firmware identity: step 1 load legacy rows: %w", err)
	}
	done := 0
	for i := range rows {
		row := &rows[i]
		if strings.TrimSpace(row.ElfSHA256) != "" {
			continue // 已识别，缺的只有 sidecar 里的 psram
		}
		path := ResolveFirmwareBinaryPath(firmwareDir, row.StoragePath, row.Filename)
		ident, err := parseImageFile(path)
		if err != nil {
			logger.Warnf("[firmware-migrate] id=%d %q stays legacy (binary unreadable): %v", row.ID, row.Filename, err)
			continue
		}
		updates := map[string]interface{}{
			"version":        ident.Version,
			"chip":           ident.Chip,
			"flash_mb":       ident.FlashMB,
			"model_key":      ident.ModelKey(),
			"elf_sha256":     ident.ElfSHA256,
			"built_at_raw":   ident.BuiltAtRaw,
			"idf_ver":        ident.IDFVersion,
			"identity_state": IdentityLegacy,
		}
		if err := tx.Model(&models.Firmware{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return done, fmt.Errorf("firmware identity: step 1 backfill id=%d: %w", row.ID, err)
		}
		logger.Infof("[firmware-migrate] id=%d %q identified (chip=%s flash_mb=%d version=%s) but stays legacy until a sidecar re-upload supplies psram",
			row.ID, row.Filename, ident.Chip, ident.FlashMB, ident.Version)
		done++
	}
	return done, nil
}

// EnforceImageVersion 用镜像内 version 覆盖库内 version（方案 §5.6 步骤 2 / RK-8）。
//
// 只处理 verified 行：legacy 行的 version 已由步骤 1 按镜像写好（并会随 sidecar
// 重传再校正一次），这里不再重复读盘。它现在的定位是**安全网**——兜住
// 「上一版代码把回填行标成 verified、或历史上传手填版本」这类遗留行。
//
// 不保留「以库内为准」的分支：历史上传手填版本正是 DEF-6 的来源，
// 差异只写日志（含 id 与新旧值），便于事后核对。
func EnforceImageVersion(tx *gorm.DB, firmwareDir string) (int, error) {
	var rows []models.Firmware
	if err := tx.Where("identity_state = ?", IdentityVerified).Order("id ASC").Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("firmware identity: step 2 load verified rows: %w", err)
	}
	overridden := 0
	for i := range rows {
		row := &rows[i]
		path := ResolveFirmwareBinaryPath(firmwareDir, row.StoragePath, row.Filename)
		ident, err := parseImageFile(path)
		if err != nil {
			logger.Warnf("[firmware-migrate] id=%d %q: version not enforced, binary unreadable: %v", row.ID, row.Filename, err)
			continue
		}
		if ident.Version == row.Version {
			continue
		}
		if err := tx.Model(&models.Firmware{}).Where("id = ?", row.ID).Update("version", ident.Version).Error; err != nil {
			return overridden, fmt.Errorf("firmware identity: step 2 override version id=%d: %w", row.ID, err)
		}
		logger.Warnf("[firmware-migrate] id=%d %q: version %q -> %q (image is authoritative)",
			row.ID, row.Filename, row.Version, ident.Version)
		overridden++
	}
	return overridden, nil
}

// DedupeFirmwareIdentity 按 elf_sha256 去重：保留最小 id，其余置空并标 duplicate。
//
// 之所以先于建索引：CREATE UNIQUE INDEX 遇到重复行只会报一句
// 「could not create unique index」并 abort 启动，运维无法定位是哪一份；
// 这里先按组收敛，并把被收敛的行标成 duplicate 让它们在列表里可见但不可选。
func DedupeFirmwareIdentity(tx *gorm.DB) (int, error) {
	type elfGroup struct {
		ElfSHA256 string
		Cnt       int64
	}
	var groups []elfGroup
	if err := tx.Model(&models.Firmware{}).
		Select("elf_sha256, COUNT(*) AS cnt").
		Where("elf_sha256 <> ''").
		Group("elf_sha256").
		Having("COUNT(*) > 1").
		Scan(&groups).Error; err != nil {
		return 0, fmt.Errorf("firmware identity: step 3 group duplicates: %w", err)
	}

	moved := 0
	for _, group := range groups {
		var ids []uint
		if err := tx.Model(&models.Firmware{}).
			Where("elf_sha256 = ?", group.ElfSHA256).
			Order("id ASC").
			Pluck("id", &ids).Error; err != nil {
			return moved, fmt.Errorf("firmware identity: step 3 list elf=%s: %w", shortElf(group.ElfSHA256), err)
		}
		if len(ids) <= 1 {
			continue
		}
		dups := ids[1:]
		if err := tx.Model(&models.Firmware{}).Where("id IN ?", dups).Updates(map[string]interface{}{
			"elf_sha256":     "",
			"identity_state": IdentityDuplicate,
		}).Error; err != nil {
			return moved, fmt.Errorf("firmware identity: step 3 mark duplicates %v: %w", dups, err)
		}
		logger.Warnf("[firmware-migrate] elf=%s… has %d rows: kept id=%d, marked duplicate ids=%v",
			shortElf(group.ElfSHA256), group.Cnt, ids[0], dups)
		moved += len(dups)
	}
	return moved, nil
}

// DropLegacyFirmwareColumns 显式 DROP stable / target_model（AutoMigrate 不删列）。
//
// 不可逆：删列后旧镜像读不到这两列，回滚手段是发布前 pg_dump + 前滚修复（方案 §9.3）。
// 列不存在则跳过，因此重跑零副作用。
func DropLegacyFirmwareColumns(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("firmwares") {
		return nil
	}
	// 删列前先把 stable 映射到 channel（方案 §5.2：stable=true → stable，其余 → internal）。
	// 不做这一步的话，新列的默认值 stable 会把「非稳定版本」悄悄升成稳定渠道——
	// 历史库里 stable=false 是常态，静默升级比丢数据更难发现。
	if tx.Migrator().HasColumn("firmwares", "stable") && tx.Migrator().HasColumn("firmwares", "channel") {
		if err := tx.Exec("UPDATE firmwares SET channel = CASE WHEN stable THEN 'stable' ELSE 'internal' END").Error; err != nil {
			return fmt.Errorf("firmware identity: step 4 map stable→channel: %w", err)
		}
	}
	for _, column := range []string{"stable", "target_model"} {
		if !tx.Migrator().HasColumn("firmwares", column) {
			continue
		}
		// SQLite(≥3.35) 与 PG 都支持原生 DROP COLUMN；SQLite 没有 IF EXISTS
		// 子句，所以「列是否存在」必须先问 Migrator。
		if err := tx.Exec("ALTER TABLE firmwares DROP COLUMN " + column).Error; err != nil {
			return fmt.Errorf("firmware identity: step 4 drop column firmwares.%s: %w", column, err)
		}
		logger.Infof("[firmware-migrate] dropped legacy column firmwares.%s", column)
	}
	return nil
}

// EnsureFirmwareIdentityIndex 建 §5.5 的两条索引。
//
// 唯一键选 elf_sha256 而不是 (model_key, version, channel)：PSRAM 不在镜像头里，
// s3-n16 与 s3p-n16 的 model_key 相同，按档位建唯一索引必然冲突（方案 §5.5）。
func EnsureFirmwareIdentityIndex(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("firmwares") {
		return nil
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_firmwares_elf
			ON firmwares (elf_sha256)
			WHERE elf_sha256 <> '' AND deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_firmwares_model_version
			ON firmwares (model_key, version, channel)`,
	}
	for _, stmt := range statements {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("firmware identity: step 5 create index: %w", err)
		}
	}
	return nil
}

// BackfillNodeHardware 回填 nodes 的硬件档案（方案 §5.6 步骤 6 / RK-7）。
//
// 只填**能解析出来**的字段：'esp32s3'/'esp32s3p'/'esp32c6' 能定芯片与 PSRAM，
// 容量只有串里显式带 -n8/-n16 才有。解析不出来一律留空/0 —— 闸门据此走
// OTA_GATE_HARDWARE_UNKNOWN，由 ehomectl node hw set 更正，而不是猜。
//
// platform 同时被规范化（大小写/别名），因为闸门直接比较 firmware.chip != node.platform。
func BackfillNodeHardware(tx *gorm.DB) (int, error) {
	var nodes []models.Node
	if err := tx.Select("id", "node_id", "platform", "model", "hw_flash_mb", "hw_psram").Find(&nodes).Error; err != nil {
		return 0, fmt.Errorf("firmware identity: step 6 load nodes: %w", err)
	}
	updated := 0
	for i := range nodes {
		node := &nodes[i]
		chip, flashMB, psram := firmwareid.ParseNodeHardware(node.Platform, node.Model)
		if chip == "" {
			continue
		}
		updates := map[string]interface{}{}
		if node.Platform != chip {
			updates["platform"] = chip
		}
		if node.HwFlashMB == 0 && flashMB != 0 {
			updates["hw_flash_mb"] = flashMB
		}
		if node.HwPsram == "" && psram != "" {
			updates["hw_psram"] = psram
		}
		if len(updates) == 0 {
			continue
		}
		if err := tx.Model(&models.Node{}).Where("id = ?", node.ID).Updates(updates).Error; err != nil {
			return updated, fmt.Errorf("firmware identity: step 6 backfill node %s: %w", node.NodeID, err)
		}
		updated++
	}
	return updated, nil
}

// ReportFirmwareIdentity 输出 legacy / duplicate 清单（只读，ehomectl 可复现）。
func ReportFirmwareIdentity(tx *gorm.DB) (FirmwareIdentityReport, error) {
	var report FirmwareIdentityReport
	load := func(state string, out *[]FirmwareIdentityRow) error {
		if err := tx.Model(&models.Firmware{}).
			Select("id", "version", "filename", "storage_path", "identity_state", "chip", "flash_mb", "psram", "elf_sha256").
			Where("identity_state = ?", state).
			Order("id ASC").
			Scan(out).Error; err != nil {
			return fmt.Errorf("firmware identity: step 7 list %s rows: %w", state, err)
		}
		return nil
	}
	if err := load(IdentityLegacy, &report.Legacy); err != nil {
		return FirmwareIdentityReport{}, err
	}
	if err := load(IdentityDuplicate, &report.Duplicate); err != nil {
		return FirmwareIdentityReport{}, err
	}
	return report, nil
}

// ResolveFirmwareBinaryPath 由存储元数据解析磁盘路径。
//
// 顺序：StoragePath（上传时写入）→ firmwares/<base(Filename)>（历史行）。
// 候选值一律先 filepath.Base 再拼，避免被污染的 DB 值逃出 firmwares/ 目录
// ——与 api 侧 firmwareBinaryDiskPath 同一口径。
func ResolveFirmwareBinaryPath(firmwareDir, storagePath, filename string) string {
	dir := strings.TrimSpace(firmwareDir)
	if dir == "" {
		dir = FirmwareDir
	}
	path := strings.TrimSpace(storagePath)
	if path == "" {
		path = strings.TrimSpace(filename)
	}
	if path == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Base(path))
}

func parseImageFile(path string) (*firmwareid.Identity, error) {
	if path == "" {
		return nil, fmt.Errorf("no storage path recorded")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return firmwareid.ReadIdentity(file, info.Size())
}

func shortElf(elf string) string {
	if len(elf) > 12 {
		return elf[:12]
	}
	return elf
}
