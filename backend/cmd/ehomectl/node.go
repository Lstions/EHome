package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// runNodeCLI 是节点硬件档案的更正入口（方案 §6.3 data.fix / §5.6 步骤 6）。
func runNodeCLI() {
	if len(os.Args) < 4 || os.Args[2] != "hw" {
		fatal("usage: ehomectl node hw <set|backfill>")
	}
	switch os.Args[3] {
	case "set":
		runNodeHWSet(os.Args[4:])
	case "backfill":
		runNodeHWBackfill(os.Args[4:])
	default:
		fatal("usage: ehomectl node hw <set|backfill>")
	}
}

// runNodeHWSet 人工补录单台节点的硬件档案。
//
// 这是闸门 OTA_GATE_HARDWARE_UNKNOWN 的唯一出口：硬件类不匹配**不可 force**
// （方案 §2.4 R5），只能把数据改对。
func runNodeHWSet(args []string) {
	fs := flag.NewFlagSet("node hw set", flag.ExitOnError)
	nodeID := fs.String("id", "", "node_id, e.g. F0F5BD02F35C (required)")
	flashMB := fs.Int("flash-mb", -1, "hardware flash size in MB: 8 or 16")
	psram := fs.String("psram", "", "yes|no")
	_ = fs.Parse(args)
	if *nodeID == "" || (*flashMB < 0 && *psram == "") {
		fatal("usage: ehomectl node hw set --id <node_id> [--flash-mb 8|16] [--psram yes|no]")
	}

	updates := map[string]interface{}{}
	if *flashMB >= 0 {
		if *flashMB != 8 && *flashMB != 16 {
			fatal("--flash-mb must be 8 or 16 (0 means unknown, which is what we are fixing)")
		}
		updates["hw_flash_mb"] = *flashMB
	}
	if *psram != "" {
		value := strings.ToLower(strings.TrimSpace(*psram))
		if value != firmwareid.PSRAMYes && value != firmwareid.PSRAMNo {
			fatal("--psram must be yes or no")
		}
		updates["hw_psram"] = value
	}

	db := connectDBNoMigrate()
	requireFirmwareIdentitySchema(db, "node hw set")
	result := db.Model(&models.Node{}).Where("node_id = ?", *nodeID).Updates(updates)
	if result.Error != nil {
		fatal(result.Error.Error())
	}
	if result.RowsAffected != 1 {
		fatal(fmt.Sprintf("node %s not found (or nothing changed): rows=%d", *nodeID, result.RowsAffected))
	}
	fmt.Printf("node %s hardware updated: %v\n", *nodeID, updates)
}

// runNodeHWBackfill 批量回填 nodes.hw_flash_mb / hw_psram（与启动迁移同一函数）。
//
// 迁移已在启动时跑过一次；本命令用于固件升级后型号串变规范（或人工修过数据）
// 需要重跑的场景，幂等：已填的字段不会被覆盖。
func runNodeHWBackfill(args []string) {
	fs := flag.NewFlagSet("node hw backfill", flag.ExitOnError)
	_ = fs.Parse(args)

	db := connectDBNoMigrate()
	requireFirmwareIdentitySchema(db, "node hw backfill")
	updated := 0
	if err := db.Transaction(func(tx *gorm.DB) error {
		count, err := dbschema.BackfillNodeHardware(tx)
		updated = count
		return err
	}); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("node hardware backfilled: %d row(s)\n", updated)
}
