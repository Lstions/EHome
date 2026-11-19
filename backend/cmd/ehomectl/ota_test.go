package main

// ota_test.go — `ehomectl ota gate-check` 的判据、过滤与**只读性**门禁。
//
// 只读性用行为断言（GORM 写回调 + 行数不变）而不是「看代码觉得没写」：
// 一个自称预检的运维命令如果偷偷建任务/改节点档案，运维会把它当成安全的，
// 这正是 main.go 里 connectDBNoMigrate 那段长注释要防的一类事故。

import (
	"bytes"
	"strings"
	"testing"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openGateCheckTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Firmware{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func createGateCheckNode(t *testing.T, db *gorm.DB, nodeID, platform string, flashMB uint16, psram, version string) *models.Node {
	t.Helper()
	node := &models.Node{
		NodeID:          nodeID,
		Name:            nodeID,
		Platform:        platform,
		HwFlashMB:       flashMB,
		HwPsram:         psram,
		FirmwareVersion: version,
		Status:          "online",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create node %s: %v", nodeID, err)
	}
	return node
}

func createGateCheckFirmware(t *testing.T, db *gorm.DB, version, chip string, flashMB uint16, psram, elf string) *models.Firmware {
	t.Helper()
	fw := &models.Firmware{
		Version:       version,
		Checksum:      "checksum",
		URL:           "http://example/fw.bin",
		SizeBytes:     1024 * 1024,
		Chip:          chip,
		FlashMB:       flashMB,
		Psram:         psram,
		ModelKey:      firmwareid.ModelKey(chip, flashMB),
		ElfSHA256:     elf,
		IdentityState: firmwareid.IdentityVerified,
	}
	if err := db.Create(fw).Error; err != nil {
		t.Fatalf("create firmware %s: %v", version, err)
	}
	return fw
}

// TestParseGateCheckFlags 守三种参数形态与两种错法（互斥 / 都缺）。
func TestParseGateCheckFlags(t *testing.T) {
	var stderr bytes.Buffer
	nodeID, err := parseGateCheckFlags([]string{"--all"}, &stderr)
	if err != nil {
		t.Fatalf("--all: %v", err)
	}
	if nodeID != "" {
		t.Errorf("--all: nodeID = %q, want empty", nodeID)
	}

	nodeID, err = parseGateCheckFlags([]string{"--node", "F0F5BD02F35C"}, &stderr)
	if err != nil {
		t.Fatalf("--node: %v", err)
	}
	if nodeID != "F0F5BD02F35C" {
		t.Errorf("--node: nodeID = %q", nodeID)
	}

	for _, args := range [][]string{
		{"--all", "--node", "X"}, // 互斥
		{},                       // 都缺
		{"--node"},               // flag 缺值
		{"--all", "leftover"},    // 多余位置参数
	} {
		if _, err := parseGateCheckFlags(args, &stderr); err == nil {
			t.Errorf("args %v must be rejected", args)
		}
	}
}

// TestRunGateCheckCompatibleExitsZero: 全兼容时返回 0 个 blocked（→ exit 0）。
func TestRunGateCheckCompatibleExitsZero(t *testing.T) {
	db := openGateCheckTestDB(t)
	createGateCheckNode(t, db, "N-OK", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, "1.0.0")
	createGateCheckFirmware(t, db, "2.0.0", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, strings.Repeat("a", 64))

	var out bytes.Buffer
	blocked, err := runGateCheck(db, &out, "")
	if err != nil {
		t.Fatalf("runGateCheck: %v", err)
	}
	if blocked != 0 {
		t.Errorf("blocked = %d, want 0", blocked)
	}
	if !strings.Contains(out.String(), " -> OK") {
		t.Errorf("compatible pair must be printed as OK:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0 blocked") {
		t.Errorf("summary must report 0 blocked:\n%s", out.String())
	}
}

// TestRunGateCheckReportsBlockedBranch: 命中时打印分支号 + 机器码 + 原因 + fix。
func TestRunGateCheckReportsBlockedBranch(t *testing.T) {
	db := openGateCheckTestDB(t)
	// 节点档案不完整 → 第 3 条；fix 命令必须出现。
	createGateCheckNode(t, db, "N-UNKNOWN", firmwareid.ChipC6, 0, "", "1.0.0")
	createGateCheckFirmware(t, db, "2.0.0", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, strings.Repeat("b", 64))

	var out bytes.Buffer
	blocked, err := runGateCheck(db, &out, "")
	if err != nil {
		t.Fatalf("runGateCheck: %v", err)
	}
	if blocked != 1 {
		t.Errorf("blocked = %d, want 1", blocked)
	}
	text := out.String()
	for _, want := range []string{
		"BLOCKED branch=3",
		firmwareid.GateHardwareUnknown,
		"HARDWARE_UNKNOWN",
		"ehomectl node hw set --id N-UNKNOWN",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "1 blocked") {
		t.Errorf("summary must report 1 blocked:\n%s", text)
	}
}

// TestRunGateCheckNodeFilter: --node 只看该节点，不打印其它节点。
func TestRunGateCheckNodeFilter(t *testing.T) {
	db := openGateCheckTestDB(t)
	createGateCheckNode(t, db, "N-A", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, "1.0.0")
	createGateCheckNode(t, db, "N-B", firmwareid.ChipS3, 16, firmwareid.PSRAMYes, "1.0.0")
	createGateCheckFirmware(t, db, "2.0.0", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, strings.Repeat("c", 64))

	var out bytes.Buffer
	blocked, err := runGateCheck(db, &out, "N-A")
	if err != nil {
		t.Fatalf("runGateCheck: %v", err)
	}
	if blocked != 0 {
		t.Errorf("blocked = %d, want 0 for N-A", blocked)
	}
	text := out.String()
	if !strings.Contains(text, "node=N-A") {
		t.Errorf("output must include N-A:\n%s", text)
	}
	if strings.Contains(text, "node=N-B") {
		t.Errorf("--node must not evaluate other nodes:\n%s", text)
	}
	if !strings.Contains(text, "1 node(s) x 1 firmware(s)") {
		t.Errorf("pair count must reflect the filter:\n%s", text)
	}
}

// TestRunGateCheckUnknownNode: --node 指向不存在的节点 → error（调用方 exit 2）。
func TestRunGateCheckUnknownNode(t *testing.T) {
	db := openGateCheckTestDB(t)
	createGateCheckFirmware(t, db, "2.0.0", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, strings.Repeat("d", 64))

	var out bytes.Buffer
	if _, err := runGateCheck(db, &out, "NOPE"); err == nil {
		t.Fatal("unknown node must be an error")
	}
}

// TestRunGateCheckIsReadOnly 是本命令的核心承诺：预检绝不写库。
func TestRunGateCheckIsReadOnly(t *testing.T) {
	db := openGateCheckTestDB(t)
	createGateCheckNode(t, db, "N-RO", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, "1.0.0")
	createGateCheckFirmware(t, db, "1.0.0", firmwareid.ChipC6, 8, firmwareid.PSRAMNo, strings.Repeat("e", 64))

	var writes []string
	_ = db.Callback().Create().Before("gorm:create").Register("test:guard_create", func(tx *gorm.DB) {
		writes = append(writes, "create:"+tx.Statement.Table)
	})
	_ = db.Callback().Update().Before("gorm:update").Register("test:guard_update", func(tx *gorm.DB) {
		writes = append(writes, "update:"+tx.Statement.Table)
	})
	_ = db.Callback().Delete().Before("gorm:delete").Register("test:guard_delete", func(tx *gorm.DB) {
		writes = append(writes, "delete:"+tx.Statement.Table)
	})

	var nodesBefore, firmwaresBefore int64
	db.Model(&models.Node{}).Count(&nodesBefore)
	db.Model(&models.Firmware{}).Count(&firmwaresBefore)

	var out bytes.Buffer
	if _, err := runGateCheck(db, &out, ""); err != nil {
		t.Fatalf("runGateCheck: %v", err)
	}

	if len(writes) != 0 {
		t.Errorf("gate-check must be read-only, but issued write(s): %v", writes)
	}
	var nodesAfter, firmwaresAfter int64
	db.Model(&models.Node{}).Count(&nodesAfter)
	db.Model(&models.Firmware{}).Count(&firmwaresAfter)
	if nodesAfter != nodesBefore || firmwaresAfter != firmwaresBefore {
		t.Errorf("row counts changed: nodes %d→%d, firmwares %d→%d",
			nodesBefore, nodesAfter, firmwaresBefore, firmwaresAfter)
	}
}
