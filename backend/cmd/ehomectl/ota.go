package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"

	"gorm.io/gorm"
)

// runOTACLI 是 OTA 运维入口。
//
// 退出码约定（脚本依赖它，不要与 fatal() 的 1 混用）：
//
//	0 = 全部兼容；1 = 至少一对命中闸门；2 = 参数错 / 连库错 / schema 未迁移。
func runOTACLI() {
	if len(os.Args) < 3 {
		gateCheckUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[2] {
	case "gate-check":
		runOTAGateCheck(os.Args[3:])
	default:
		gateCheckUsage(os.Stderr)
		os.Exit(2)
	}
}

func gateCheckUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: ehomectl ota gate-check --all | --node <node_id>")
	fmt.Fprintln(w, "  Read-only: evaluates every (node, firmware) pair with the same firmwareid.CheckCompat")
	fmt.Fprintln(w, "  used by POST /api/v1/ota/tasks. Exit 0 = all compatible, 1 = blocked pairs, 2 = usage/DB error.")
}

// runOTAGateCheck 是闸门的**只读预检**（方案 §6.3 / T1a.6）。
//
// 设计意图：上线前把「每个 (节点, 固件) 会命中哪条分支」打印出来，先补数据再放开
// 建任务。它与 CreateTaskWithOptions 复用同一个 firmwareid.CheckCompat 纯函数
// （经 ota.NewCompatRequest 映射），因此「预检通过」与「建任务通过」判定同源
// （v1.4 删除 gateMode 双模式的前提）。
func runOTAGateCheck(args []string) {
	nodeID, err := parseGateCheckFlags(args, os.Stderr)
	if err != nil {
		gateCheckUsage(os.Stderr)
		os.Exit(2)
	}
	db := openGateCheckDB()
	blocked, err := runGateCheck(db, os.Stdout, nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ota gate-check: %v\n", err)
		os.Exit(2)
	}
	if blocked > 0 {
		os.Exit(1)
	}
}

// parseGateCheckFlags 只接受 --all 与 --node <id> 二选一；返回 "" 表示 --all。
func parseGateCheckFlags(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("ota gate-check", flag.ContinueOnError)
	fs.SetOutput(out)
	all := fs.Bool("all", false, "check every (node, firmware) pair")
	nodeID := fs.String("node", "", "check one node (node_id)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	switch {
	case *all && strings.TrimSpace(*nodeID) != "":
		return "", errors.New("--all and --node are mutually exclusive")
	case !*all && strings.TrimSpace(*nodeID) == "":
		return "", errors.New("either --all or --node <node_id> is required")
	}
	return strings.TrimSpace(*nodeID), nil
}

// openGateCheckDB 只连库（**不跑迁移**）+ 检查新列是否存在。
//
// 自称只读的运维命令绝不能偷偷改 schema（本仓既有纪律，见 main.go 里
// connectDBNoMigrate 的长注释）；列缺失时明确提示「先让新版本服务完成迁移」，
// 并以退出码 2 结束，而不是抛一句 no such column。
func openGateCheckDB() *gorm.DB {
	cfg := config.Load().DBConfig()
	if err := database.Connect(database.Config{
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		DBName:   cfg.DBName,
		SSLMode:  cfg.SSLMode,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ota gate-check: connect database: %v\n", err)
		os.Exit(2)
	}
	db := database.GetDB()
	if err := gateCheckSchemaError(db); err != nil {
		fmt.Fprintf(os.Stderr, "ota gate-check: %v\n", err)
		os.Exit(2)
	}
	return db
}

// gateCheckSchemaError 把「库还没迁移」翻译成可执行的提示（而不是 SQL 错误原文）。
func gateCheckSchemaError(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not connected")
	}
	for _, table := range []string{"nodes", "firmwares"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("%s table does not exist yet; start the new backend once so migrations run", table)
		}
	}
	for _, column := range []string{"platform", "hw_flash_mb", "hw_psram"} {
		if !db.Migrator().HasColumn("nodes", column) {
			return fmt.Errorf("nodes.%s is missing; start the new backend once so migrations run", column)
		}
	}
	for _, column := range []string{"chip", "flash_mb", "psram", "identity_state", "withdrawn_at"} {
		if !db.Migrator().HasColumn("firmwares", column) {
			return fmt.Errorf("firmwares.%s is missing; start the new backend once so migrations run", column)
		}
	}
	return nil
}

// runGateCheck 遍历节点 × 固件，打印每对命中的分支；返回被拒对数（调用方映射退出码）。
//
// 只做 SELECT：不写任务、不改数据。固件集合包含已撤回行（第 1 条也要能看见）：
// 预检报告的是「这一对提交上去会怎样」，而不是「界面里能选什么」；软删行由 GORM
// 默认排除（deleted_at IS NULL）。
func runGateCheck(db *gorm.DB, out io.Writer, nodeID string) (int, error) {
	nodeQuery := db.Order("node_id ASC")
	if nodeID != "" {
		nodeQuery = nodeQuery.Where("node_id = ?", nodeID)
	}
	var nodes []models.Node
	if err := nodeQuery.Find(&nodes).Error; err != nil {
		return 0, fmt.Errorf("load nodes: %w", err)
	}
	if nodeID != "" && len(nodes) == 0 {
		return 0, fmt.Errorf("node %s not found", nodeID)
	}

	var firmwares []models.Firmware
	if err := db.Order("id ASC").Find(&firmwares).Error; err != nil {
		return 0, fmt.Errorf("load firmwares: %w", err)
	}

	total := len(nodes) * len(firmwares)
	fmt.Fprintf(out, "ota gate-check: %d node(s) x %d firmware(s) = %d pair(s), source=user, read-only\n",
		len(nodes), len(firmwares), total)

	blocked := 0
	for i := range nodes {
		node := &nodes[i]
		for j := range firmwares {
			firmware := &firmwares[j]
			result := firmwareid.CheckCompat(ota.NewCompatRequest(node, firmware, firmwareid.SourceUser))
			if !result.Compatible {
				blocked++
			}
			fmt.Fprintln(out, formatGateCheckPair(node, firmware, result))
		}
	}
	fmt.Fprintf(out, "ota gate-check: %d compatible, %d blocked\n", total-blocked, blocked)
	return blocked, nil
}

// formatGateCheckPair 输出一行：节点硬件档案 + 固件身份 + 判定结果（含 fix 命令）。
func formatGateCheckPair(node *models.Node, firmware *models.Firmware, result firmwareid.CompatResult) string {
	status := "OK"
	if !result.Compatible {
		status = fmt.Sprintf("BLOCKED branch=%d code=%s reasons=%s forceable=%v",
			result.Branch, result.Code, strings.Join(result.Reasons, ","), result.Forceable)
	}
	line := fmt.Sprintf("node=%s hw=(chip=%s flash_mb=%d psram=%s) fw=#%d model_key=%s version=%s size_bytes=%d -> %s",
		node.NodeID, node.Platform, node.HwFlashMB, node.HwPsram,
		firmware.ID, firmware.ModelKey, firmware.Version, firmware.SizeBytes, status)
	if result.Fix != "" {
		line += fmt.Sprintf(" fix=%q", result.Fix)
	}
	return line
}
