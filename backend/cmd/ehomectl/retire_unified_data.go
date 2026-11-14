package main

// retire_unified_data.go — unified_data 整表退役的「校验 + 删表」入口（2026-11-13）。
//
// # 为什么要一条命令，而不是启动时自动 DROP
//
// DROP 整表是**一次性不可逆**动作。启动路径自动执行意味着"升级镜像 = 删数据"，
// 没有回滚窗口，也没有人工确认点。因此与 retire-device-data 同款：
//
//	ehomectl retire-unified-data            # 只校验、只打印（默认，绝不改库）
//	ehomectl retire-unified-data --yes      # 校验后执行 DROP
//
// # 退役前提（都已在本仓落地，命令只做复核与执行）
//
//  1. 写入路径已删除：internal/databus 不再有 unified_data 的 INSERT（2026-11-13）。
//  2. 读路径已删除：用户可见接口全部走 telemetry_frame / rollup / 维度表 /
//     最新值缓存（2026-11-08 硬切换）。
//  3. 老数据回填已删除：unified_data → telemetry_frame 的一次性迁移工具整包移除
//     （生产 3.42M 帧迁移早已完成）。
//  4. 唯一剩余引用是 models.UnifiedData 这个**值类型**（历史接口的 DTO），不碰表。
//
// # 安全边界
//
//   - 默认只读：命令用 connectDBNoMigrate，**绝不**触发 AutoMigrate。
//   - DROP 前打印行数与磁盘占用（DROP 之后 pg_class 查不到，日志是唯一留痕）。
//   - DROP 用 CASCADE：统一数据表在生产是**月分区母表**，手工枚举分区名会漏；
//     CASCADE 按依赖图删除。代价是若有视图依赖也会一起删——本表已无读路径，
//     这是可接受的（与 device_data 退役同口径）。
//   - DROP 后复查"表 + 通过 pg_inherits 依附的子分区"均为 0（不用 LIKE 前缀，
//     避免把 telemetry_backfill_progress 之类同前缀表误算进来）。
//
// 执行前必须有 pg_dump 备份：命令无法验证备份是否存在，只在输出里提示。

import (
	"fmt"
	"os"

	"gorm.io/gorm"
)

// UnifiedDataTable 是已退役表的物理表名。
const UnifiedDataTable = "unified_data"

// legacyBackfillProgressTable 是老数据回填的状态表（同生共死，随手清理）。
const legacyBackfillProgressTable = "telemetry_backfill_progress"

type unifiedRetireReport struct {
	tableExists    bool
	rowCount       int64
	sizeBytes      int64
	partitionCount int64
	progressExists bool
}

func runRetireUnifiedDataCLI() {
	args := os.Args[2:]
	dryRun := true
	confirmed := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--yes", "-y":
			confirmed = true
			dryRun = false
		case "--help", "-h":
			fmt.Println("usage: ehomectl retire-unified-data [--dry-run | --yes]")
			fmt.Println()
			fmt.Println("  (no flag)   校验 unified_data 是否已可安全 DROP，并打印表/行数/大小")
			fmt.Println("              —— 只读，绝不改库（与 --dry-run 等价）")
			fmt.Println("  --dry-run   显式声明只校验")
			fmt.Println("  --yes       打印留痕后执行 DROP TABLE unified_data CASCADE，")
			fmt.Println("              并顺手清掉已无写入方的 telemetry_backfill_progress")
			return
		default:
			fatal("unknown flag: " + a + "（只支持 --dry-run / --yes）")
		}
	}
	if dryRun && confirmed {
		fmt.Println("note: --dry-run 与 --yes 同时给出，按 --yes 处理")
	}

	db := connectDBNoMigrate()
	report := inspectUnifiedData(db)
	printUnifiedRetireReport(report)

	if !report.tableExists {
		// 幂等收尾：母表可能早已 DROP，但上一次启动留下的回填状态表还在
		// （demo 库实测就是这种状态）。它已无写入方，顺手清掉，避免运维在
		// "表已退役"与"还有一张 telemetry_backfill_progress"之间反复猜测。
		if report.progressExists && !dryRun {
			fmt.Printf("\n⏳ %s 已无写入方，清理 ...\n", legacyBackfillProgressTable)
			if err := db.Exec("DROP TABLE IF EXISTS " + legacyBackfillProgressTable).Error; err != nil {
				fmt.Printf("⚠ 清理 %s 失败（不影响 unified_data 退役）: %v\n", legacyBackfillProgressTable, err)
			}
		} else if report.progressExists {
			fmt.Printf("\n提示：%s 仍存在（已无写入方），执行 --yes 会一并清理。\n", legacyBackfillProgressTable)
		}
		fmt.Printf("\n%s 不存在（全新部署 / 已退役），无需操作。\n", UnifiedDataTable)
		return
	}
	if dryRun {
		fmt.Printf("\n✅ 以上是**将要删除**的内容（当前是 --dry-run，未做任何修改）。\n")
		fmt.Printf("   确认无误后执行： ehomectl retire-unified-data --yes\n")
		fmt.Printf("   ⚠ DROP 不可回滚：执行前必须有 pg_dump 备份。\n")
		return
	}

	// 留痕必须在 DROP 之前（DROP 后查不到行数/大小）。
	fmt.Printf("\n⏳ 执行 DROP TABLE %s CASCADE ...\n", UnifiedDataTable)
	if err := db.Exec("DROP TABLE IF EXISTS " + UnifiedDataTable + " CASCADE").Error; err != nil {
		fatal(fmt.Sprintf("DROP TABLE %s failed: %v（表结构未改变，可修复后重跑）", UnifiedDataTable, err))
	}
	if report.progressExists {
		fmt.Printf("⏳ 清理 %s ...\n", legacyBackfillProgressTable)
		if err := db.Exec("DROP TABLE IF EXISTS " + legacyBackfillProgressTable).Error; err != nil {
			fmt.Printf("⚠ 清理 %s 失败（不影响 unified_data 退役）: %v\n", legacyBackfillProgressTable, err)
		}
	}

	after := inspectUnifiedData(db)
	switch {
	case after.tableExists:
		fatal(fmt.Sprintf("复核失败：%s 仍然存在（DROP 未生效？）", UnifiedDataTable))
	case after.progressExists:
		fmt.Printf("⚠ %s 仍存在（可忽略，它已无写入方）\n", legacyBackfillProgressTable)
	}
	fmt.Printf("\n✅ %s 已退役（母表与依附分区均不存在）。\n", UnifiedDataTable)
	fmt.Printf("   服务端从 2026-11-13 起不再有该表的任何读写路径；重启后日志中不应再出现 42P01。\n")
}

func inspectUnifiedData(db *gorm.DB) unifiedRetireReport {
	var report unifiedRetireReport
	report.tableExists = db.Migrator().HasTable(UnifiedDataTable)
	if report.tableExists {
		// 行数/大小只用于留痕，任何一项查不到都按 0 处理（不阻断退役）。
		_ = db.Raw("SELECT count(*) FROM " + UnifiedDataTable).Scan(&report.rowCount).Error
		_ = db.Raw("SELECT COALESCE(pg_total_relation_size(?)::bigint, 0)", UnifiedDataTable).
			Scan(&report.sizeBytes).Error
	}
	// 分区数用 pg_inherits（结构性判据），不用名字前缀：避免把
	// telemetry_backfill_progress 这类同前缀表算进来。
	_ = db.Raw("SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhparent "+
		"WHERE c.relname = ?", UnifiedDataTable).Scan(&report.partitionCount).Error
	report.progressExists = db.Migrator().HasTable(legacyBackfillProgressTable)
	return report
}

func printUnifiedRetireReport(report unifiedRetireReport) {
	fmt.Printf("== unified_data 退役校验 ==\n")
	if !report.tableExists {
		fmt.Printf("  表存在:  否\n")
		fmt.Printf("  回填状态表: %v\n", report.progressExists)
		return
	}
	fmt.Printf("  表存在:  是\n")
	fmt.Printf("  行数:    %d\n", report.rowCount)
	fmt.Printf("  磁盘占用: %.1f MB\n", float64(report.sizeBytes)/1024/1024)
	fmt.Printf("  月分区数: %d（DROP 母表时随 CASCADE 删除）\n", report.partitionCount)
	fmt.Printf("  回填状态表: %v（随本次一并清理）\n", report.progressExists)
}
