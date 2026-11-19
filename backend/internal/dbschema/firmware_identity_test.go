package dbschema_test

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// ── 固件身份迁移（方案 §5.6 / AC-7 / AC-17 / IT-MIG-01..03）──
//
// 这组用例覆盖迁移里**最容易变成启动事故**的三件事：
//   1. 重复 elf_sha256 必须在建唯一索引之前被收敛（否则启动直接被 23505 掐死）；
//   2. stable / target_model 必须真的从两库里消失，且 stable 的语义搬到 channel（AC-17）；
//   3. 重跑幂等（每次启动都会跑一遍）。
//
// 两库都跑：testutil.OpenTestDB 已是「SQLite 默认 / EHOME_TEST_DB=postgres 走 PG」的
// 入口，因此这些用例在 make test-integration 下自动覆盖 PG。
//
// ⚠ 夹具里**不做 ADD COLUMN**（除了专门测删列的那条用例）：PG 的 pgx 语句缓存会在
// 表结构变化后拒绝复用同一个 SELECT *（"cached plan must not change result type"），
// 那是「测试夹具在一句话中间改了表结构」，不是迁移缺陷 —— 生产里迁移跑在全新连接池上，
// 且 AutoMigrate 增列发生在任何 firmwares 查询之前。

// buildTestImage 造一份「头部像真固件」的字节串（偏移表见 internal/firmwareid）。
func buildTestImage(t *testing.T, chipID uint16, spiSize uint8, version string) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	buf[0x00] = 0xE9
	buf[0x03] = spiSize << 4
	binary.LittleEndian.PutUint16(buf[0x0C:], chipID)
	binary.LittleEndian.PutUint32(buf[0x20:], 0xABCD5432)
	copy(buf[0x30:0x50], version)
	copy(buf[0x50:0x70], "ehome_collector")
	copy(buf[0x70:0x80], "07:37:54")
	copy(buf[0x80:0x90], "Oct  5 2026")
	copy(buf[0x90:0xB0], "v6.1")
	sum := sha256.Sum256(buf[0x50:0x70])
	copy(buf[0xB0:0xD0], sum[:])
	return buf
}

// TestMigrateFirmwareIdentity_BackfillAndDedupe 覆盖 IT-MIG-02 的核心：
// 先有重复 elf，再跑迁移，必须「回填 → 去重 → 建索引」一次成功。
func TestMigrateFirmwareIdentity_BackfillAndDedupe(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 历史库的一个真实特征：唯一索引还不存在（AutoMigrate 不会按模型 tag 建它）。
	if err := db.Migrator().DropIndex("firmwares", "uq_firmwares_elf"); err != nil {
		t.Fatalf("drop uq_firmwares_elf: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fw.bin"), buildTestImage(t, 0x0009, 4, "2.9.0"), 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}

	// ① 能解析出身份的旧行：库内版本是错的，必须以镜像为准。
	if err := db.Create(&models.Firmware{
		Version: "0.0.0", Checksum: "c", URL: "http://legacy.invalid/fw.bin",
		Filename: "fw.bin", StoragePath: filepath.Join("unused", "fw.bin"),
	}).Error; err != nil {
		t.Fatalf("seed parseable firmware row: %v", err)
	}
	// ② 二进制缺失的旧行：必须保持 legacy（不可选），由迁移报告列出。
	if err := db.Create(&models.Firmware{
		Version: "1.0.0", Checksum: "c", URL: "http://legacy.invalid/missing.bin", Filename: "missing.bin",
	}).Error; err != nil {
		t.Fatalf("seed orphan firmware row: %v", err)
	}
	// ③ 两条 elf_sha256 相同的行（历史重复上传）：必须只留最小 id。
	const sharedElf = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var dupIDs []uint
	for i := 0; i < 2; i++ {
		row := models.Firmware{
			Version: "2.8.0", Checksum: "c", URL: "http://legacy.invalid/dup.bin",
			Filename: "dup.bin", ElfSHA256: sharedElf, IdentityState: dbschema.IdentityVerified,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed duplicate firmware row %d: %v", i, err)
		}
		dupIDs = append(dupIDs, row.ID)
	}

	report, err := dbschema.MigrateFirmwareIdentity(db, dir)
	if err != nil {
		t.Fatalf("MigrateFirmwareIdentity: %v", err)
	}

	// 5) 索引建出来了（AC-7 的「两库都建成」在本函数跑完即可断言）。
	if !db.Migrator().HasIndex("firmwares", "uq_firmwares_elf") {
		t.Fatal("uq_firmwares_elf was not created")
	}
	if !db.Migrator().HasIndex("firmwares", "idx_firmwares_model_version") {
		t.Fatal("idx_firmwares_model_version was not created")
	}
	// 4) 死列必须消失（AC-17）。
	for _, column := range []string{"stable", "target_model"} {
		if db.Migrator().HasColumn("firmwares", column) {
			t.Fatalf("legacy column firmwares.%s still exists after migration", column)
		}
	}

	// 1+2) 身份列回填、版本以镜像为准，但状态**保持 legacy**（R3：未知即不可选）。
	// 镜像头没有 PSRAM 位 ⇒ 回填行拿不到 psram ⇒ 不许出现「verified 但 psram 空」。
	var parsed models.Firmware
	if err := db.First(&parsed, "filename = ?", "fw.bin").Error; err != nil {
		t.Fatalf("reload parseable row: %v", err)
	}
	if parsed.IdentityState != dbschema.IdentityLegacy {
		t.Fatalf("parseable row identity_state = %q, want %q (a row without psram must stay unselectable)",
			parsed.IdentityState, dbschema.IdentityLegacy)
	}
	if parsed.Version != "2.9.0" {
		t.Fatalf("parseable row version = %q, want image version %q (image is authoritative)", parsed.Version, "2.9.0")
	}
	if parsed.Chip != "esp32s3" || parsed.FlashMB != 16 || parsed.ModelKey != "esp32s3-n16" {
		t.Fatalf("parseable row identity = chip %q flash %d model_key %q", parsed.Chip, parsed.FlashMB, parsed.ModelKey)
	}
	if len(parsed.ElfSHA256) != 64 {
		t.Fatalf("parseable row elf_sha256 = %q, want 64 hex chars", parsed.ElfSHA256)
	}
	// 镜像头里没有 psram：回填行只能是 ""，由 sidecar 重传补齐（报告里点明）。
	if parsed.Psram != "" {
		t.Fatalf("backfilled row psram = %q, want empty (the image header carries no PSRAM bit)", parsed.Psram)
	}
	// 缺文件的行走 legacy（RK-1）。
	var orphan models.Firmware
	if err := db.First(&orphan, "filename = ?", "missing.bin").Error; err != nil {
		t.Fatalf("reload orphan row: %v", err)
	}
	if orphan.IdentityState != dbschema.IdentityLegacy {
		t.Fatalf("orphan row identity_state = %q, want %q", orphan.IdentityState, dbschema.IdentityLegacy)
	}

	// 3) 去重：留最小 id，其余置空并标 duplicate。
	var kept, dropped models.Firmware
	if err := db.First(&kept, dupIDs[0]).Error; err != nil {
		t.Fatalf("reload kept duplicate row: %v", err)
	}
	if err := db.First(&dropped, dupIDs[1]).Error; err != nil {
		t.Fatalf("reload dropped duplicate row: %v", err)
	}
	if kept.ElfSHA256 != sharedElf {
		t.Fatalf("kept row elf_sha256 = %q, want the shared digest", kept.ElfSHA256)
	}
	if dropped.IdentityState != dbschema.IdentityDuplicate || dropped.ElfSHA256 != "" {
		t.Fatalf("dropped row = state %q elf %q, want duplicate/empty", dropped.IdentityState, dropped.ElfSHA256)
	}
	// 回填行 + 缺文件行都是 legacy（前者身份已解析、只缺 psram；后者身份未知）。
	if len(report.Legacy) != 2 || len(report.Duplicate) != 1 || report.Deduplicated != 1 {
		t.Fatalf("report = legacy %d duplicate %d deduplicated %d, want 2/1/1",
			len(report.Legacy), len(report.Duplicate), report.Deduplicated)
	}
	if report.Backfilled != 1 {
		t.Fatalf("report.Backfilled = %d, want 1 (only the parseable row gets identified this run)", report.Backfilled)
	}
	// 报告要能区分两种 legacy：身份已解析的行带 chip/elf（只需补 sidecar 重传），
	// 身份未知的行两者皆空（需先找回原始二进制）。
	for _, row := range report.Legacy {
		switch row.Filename {
		case "fw.bin":
			if row.Chip != "esp32s3" || row.FlashMB != 16 || len(row.ElfSHA256) != 64 || row.Psram != "" {
				t.Fatalf("parsed legacy report row = %+v, want chip/flash/elf filled and psram empty", row)
			}
		case "missing.bin":
			if row.Chip != "" || row.ElfSHA256 != "" {
				t.Fatalf("unreadable legacy report row = %+v, want empty chip/elf", row)
			}
		}
	}

	// 唯一性真的生效：同一 elf 再插一行必须被拒。
	if err := db.Create(&models.Firmware{
		Version: "2.9.0", Checksum: "c", URL: "http://x/y.bin", ElfSHA256: sharedElf,
	}).Error; err == nil {
		t.Fatal("duplicate elf_sha256 was accepted; the unique partial index is not in force")
	}
	// 部分是：elf='' 的行可以有多条（legacy / duplicate 都是空 elf）。
	if err := db.Create(&models.Firmware{Version: "1.0.1", Checksum: "c", URL: "http://x/z.bin"}).Error; err != nil {
		t.Fatalf("row with empty elf_sha256 rejected: %v (the partial index must exclude it)", err)
	}
}

// TestDropLegacyFirmwareColumns 覆盖 AC-17 的删列与语义搬迁：
// stable=true → channel=stable，其余 → channel=internal；删列不可逆，重跑必须零副作用。
func TestDropLegacyFirmwareColumns(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// 历史库形态：两个死列还在。注意这一步是**专门测删列**的夹具，
	// 因此表结构变化后再查询也只在本用例内发生（见文件头关于 pgx 语句缓存的说明）。
	for _, stmt := range []string{
		"ALTER TABLE firmwares ADD COLUMN stable boolean DEFAULT false",
		"ALTER TABLE firmwares ADD COLUMN target_model varchar(64) DEFAULT ''",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("seed legacy column (%s): %v", stmt, err)
		}
	}

	stableRow := models.Firmware{Version: "2.8.0", Checksum: "c", URL: "http://legacy.invalid/a.bin", Filename: "a.bin"}
	betaRow := models.Firmware{Version: "2.9.0-rc", Checksum: "c", URL: "http://legacy.invalid/b.bin", Filename: "b.bin"}
	for _, row := range []*models.Firmware{&stableRow, &betaRow} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed firmware row %q: %v", row.Filename, err)
		}
	}
	if err := db.Exec("UPDATE firmwares SET stable = true WHERE id = ?", stableRow.ID).Error; err != nil {
		t.Fatalf("mark legacy stable row: %v", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error { return dbschema.DropLegacyFirmwareColumns(tx) }); err != nil {
		t.Fatalf("DropLegacyFirmwareColumns: %v", err)
	}
	for _, column := range []string{"stable", "target_model"} {
		if db.Migrator().HasColumn("firmwares", column) {
			t.Fatalf("firmwares.%s still exists after drop", column)
		}
	}

	var stableAfter, betaAfter models.Firmware
	if err := db.First(&stableAfter, stableRow.ID).Error; err != nil {
		t.Fatalf("reload stable row: %v", err)
	}
	if err := db.First(&betaAfter, betaRow.ID).Error; err != nil {
		t.Fatalf("reload beta row: %v", err)
	}
	if stableAfter.Channel != models.FirmwareChannelStable {
		t.Fatalf("legacy stable=true row channel = %q, want %q", stableAfter.Channel, models.FirmwareChannelStable)
	}
	if betaAfter.Channel != models.FirmwareChannelInternal {
		t.Fatalf("legacy stable=false row channel = %q, want %q (default not stable!)", betaAfter.Channel, models.FirmwareChannelInternal)
	}

	// 幂等：列已不存在时必须零副作用（每次启动都会跑）。
	if err := db.Transaction(func(tx *gorm.DB) error { return dbschema.DropLegacyFirmwareColumns(tx) }); err != nil {
		t.Fatalf("second DropLegacyFirmwareColumns: %v", err)
	}
}

// TestMigrateFirmwareIdentity_Idempotent 是 IT-MIG-03：每次启动都会跑，
// 第二次必须零副作用（不报错、不重复标记）。
func TestMigrateFirmwareIdentity_Idempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fw.bin"), buildTestImage(t, 0x000D, 3, "2.8.0"), 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	row := models.Firmware{Version: "2.8.0", Checksum: "c", URL: "http://legacy.invalid/fw.bin", Filename: "fw.bin"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	if _, err := dbschema.MigrateFirmwareIdentity(db, dir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := dbschema.MigrateFirmwareIdentity(db, dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Backfilled != 0 || second.VersionOverridden != 0 || second.Deduplicated != 0 {
		t.Fatalf("second run was not a no-op: %+v", second)
	}
	// legacy 行会一直列在报告里，直到补 sidecar 重传 —— 那是待办清单，不是副作用。
	// 幂等的判据是「没有新的写入动作」：Backfilled / VersionOverridden / Deduplicated 都为 0。
	if len(second.Legacy) != 1 || second.Legacy[0].ID != row.ID {
		t.Fatalf("re-run report = %+v, want the same single legacy row still listed", second.Legacy)
	}
	if len(second.Duplicate) != 0 {
		t.Fatalf("re-run created duplicate rows: %+v", second.Duplicate)
	}

	var stored models.Firmware
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("reload firmware: %v", err)
	}
	// 回填行永远是 legacy（缺 psram），身份列照写；重跑不再重复回填（Backfilled=0）。
	if stored.IdentityState != dbschema.IdentityLegacy || stored.Chip != "esp32c6" || stored.FlashMB != 8 {
		t.Fatalf("stored identity = %+v, want legacy esp32c6/8MB", stored)
	}
	if stored.ModelKey != "esp32c6-n8" {
		t.Fatalf("stored model_key = %q, want esp32c6-n8", stored.ModelKey)
	}
}

// TestBackfillNodeHardware 覆盖 RK-7：型号串能解析的字段写进去，解析不出来留空。
func TestBackfillNodeHardware(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seed := []models.Node{
		{NodeID: "NODE-S3P", Name: "s3p", Platform: "esp32s3", Model: "esp32s3p"},
		{NodeID: "NODE-C6", Name: "c6", Platform: "ESP32C6", Model: "esp32c6"},
		{NodeID: "NODE-PROFILE", Name: "profile", Platform: "", Model: "s3p-n16"},
		{NodeID: "NODE-UNKNOWN", Name: "unknown", Platform: "ESP32", Model: "ESP32"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed node %s: %v", seed[i].NodeID, err)
		}
	}

	if _, err := dbschema.MigrateFirmwareIdentity(db, t.TempDir()); err != nil {
		t.Fatalf("MigrateFirmwareIdentity: %v", err)
	}

	load := func(nodeID string) models.Node {
		t.Helper()
		var node models.Node
		if err := db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			t.Fatalf("load node %s: %v", nodeID, err)
		}
		return node
	}

	s3p := load("NODE-S3P")
	if s3p.Platform != "esp32s3" || s3p.HwPsram != "yes" {
		t.Fatalf("s3p node = platform %q hw_psram %q, want esp32s3/yes", s3p.Platform, s3p.HwPsram)
	}
	if s3p.HwFlashMB != 0 {
		t.Fatalf("s3p hw_flash_mb = %d, want 0 (the reported model string carries no capacity; must not be guessed)", s3p.HwFlashMB)
	}
	c6 := load("NODE-C6")
	if c6.Platform != "esp32c6" || c6.HwPsram != "no" {
		t.Fatalf("c6 node = platform %q hw_psram %q, want esp32c6/no (platform is normalized)", c6.Platform, c6.HwPsram)
	}
	profile := load("NODE-PROFILE")
	if profile.Platform != "esp32s3" || profile.HwFlashMB != 16 || profile.HwPsram != "yes" {
		t.Fatalf("profile node = %q/%d/%q, want esp32s3/16/yes", profile.Platform, profile.HwFlashMB, profile.HwPsram)
	}
	unknown := load("NODE-UNKNOWN")
	if unknown.HwFlashMB != 0 || unknown.HwPsram != "" {
		t.Fatalf("unknown node hardware = %d/%q, want 0/\"\" (unknown must stay unknown)", unknown.HwFlashMB, unknown.HwPsram)
	}
}
