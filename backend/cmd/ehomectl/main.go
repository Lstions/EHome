// Command ehomectl provides host-local administration.
package main

import (
	"fmt"
	"os"
	"time"

	"gorm.io/gorm"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/models"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "auth":
		if len(os.Args) < 3 {
			usage()
		}
		runAuth()
	case "retire-logical-device":
		// 逻辑设备概念层的数据层拆除 (2026-10-23, .contract/logical-device-removal.md)。
		// 服务端启动时 database.AutoMigrate() 也会自动幂等执行同一动作；本命令
		// 供部署维护窗口**先行**执行（在停旧容器之后、起新容器之前），这样
		// 大表 DROP COLUMN 的锁窗口与启动流程解耦，可控且可重试。
		db := connectDB()
		rows, err := database.RetireLogicalDeviceLayer(db)
		if err != nil {
			fatal(fmt.Sprintf("retire logical device layer failed: %v", err))
		}
		fmt.Printf("logical device layer retired (rows touched=%d)\n", rows)
	case "retire-device-data":
		// device_data 整表退役的「校验 + 删表」入口（2026-11-11）。
		//
		// 与 retire-logical-device 的区别：那一条在**启动时**也会幂等重跑（表结构退役，
		// 重跑零成本）；本命令是**一次性不可逆**动作（DROP 整表），所以刻意
		// **不**接到启动流程里 —— 它必须由人在维护窗口显式执行，且默认只校验。
		// 语义见 retire_device_data.go 的文件头。
		runRetireDeviceDataCLI()
	case "retire-unified-data":
		// unified_data 整表退役（2026-11-13）：写入/读取/回填路径均已删除，
		// 本命令只做「只读复核 + 显式 DROP」，默认不执行。
		runRetireUnifiedDataCLI()
	case "command-intervals":
		runCommandIntervalsCleanupCLI()
	default:
		usage()
	}
}

func usage() {
	fatal("usage: ehomectl auth <bootstrap-database|create-initialization-token|reset-password>\n" +
		"       ehomectl retire-logical-device\n" +
		retireUsageLine() +
		"       ehomectl command-intervals cleanup")
}

// autoMigrateFn 是 database.AutoMigrate 的间接引用。
//
// 为什么做成变量：**只读子命令绝不能跑 schema 迁移**，而"有没有跑"在单元测试里
// 无法从外部观察（它执行的是 DDL，副作用是 schema 变化，很难在 sqlite 上注入
// 一个可靠的探针）。做成可替换的变量后，测试可以把它换成一个计数器，
// 直接断言「retire-device-data 路径没有调用它」。
//
// 生产里它恒为 database.AutoMigrate，没有任何调用方会改写它 —— 替换只发生在测试中。
var autoMigrateFn = database.AutoMigrate

func connectDB() *gorm.DB {
	db := connectDBNoMigrate()
	if err := autoMigrateFn(); err != nil {
		fatal(err.Error())
	}
	return db
}

// connectDBNoMigrate 只连库（`Connect` + `GetDB`），**不执行 AutoMigrate**。
//
// ==================== 为什么必须有它（2026-11-11 生产事故）====================
//
// `ehomectl retire-device-data` 的自我声明是「默认/--dry-run **只校验只打印，
// 绝不改库**」。但它原先走的是 `connectDB()`，而那一行里藏着
// `database.AutoMigrate()` —— 也就是说**它在连库的瞬间就对生产库执行了
// schema 迁移**。生产实测确实触发了（review 时发现的）。
//
// 为什么这是严重问题（不是洁癖）：
//
//  1. **违反部署顺序的安全前提**。方案 §3.3 的顺序是
//     「停容器 → **备份 pg_dump** → 校验 → DROP」。校验跑在备份**之前**，
//     若这次 AutoMigrate 改了 schema 而随后 DROP 出问题需要回滚，
//     **备份里没有那次 schema 改动** —— 回滚后 schema 与代码不一致。
//  2. **违反工具自己的声明**。一个自称只读的运维工具偷偷改 schema，是最坏的一类
//     违反预期：运维会据此认为"dry-run 是安全的、可以随时跑"。
//  3. 这个命令**根本不需要 schema 迁移**：它只读 device_data / telemetry_frame /
//     edge_devices / telemetry_rollup_coverage 四张表并做 DROP。任何一张表的结构
//     都不是它关心的（列不存在会在校验 SQL 里直接报错，那正是我们想要的信号）。
//
// 其余子命令（auth / retire-logical-device / command-intervals）**继续用
// connectDB()**（带 AutoMigrate）：那三个是"要么改 schema、要么依赖最新 schema"的
// 维护动作，AutoMigrate 是它们的前置条件。区别写在各自的调用点。
func connectDBNoMigrate() *gorm.DB {
	cfg := config.Load().DBConfig()
	if err := database.Connect(database.Config{Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: cfg.Password, DBName: cfg.DBName, SSLMode: cfg.SSLMode}); err != nil {
		fatal(err.Error())
	}
	return database.GetDB()
}

// runMaintenance 是运维维护窗口的同步入口。
//
// 历史: 这里曾有 runDatalifecycle（ensure-indexes / backfill），用于给
// 「逻辑设备身份」建复合索引并回填大表 logical_device_id。逻辑设备概念层
// 已整体移除 (2026-10-23, .contract/logical-device-removal.md)，索引与回填
// 都不再有对象，命令随之删除；保留的是同形态的退役入口。
func runAuth() {
	db := connectDB()

	switch os.Args[2] {
	case "bootstrap-database":
		var users int64
		if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
			fatal(err.Error())
		}
		if users != 0 {
			fatal("bootstrap refused: users already exist")
		}
		state, err := models.LoadAuthState(db)
		if err != nil {
			fatal(err.Error())
		}
		if state.State != models.AuthStateUninitialized {
			fatal("bootstrap refused: auth state is not uninitialized")
		}
		if err := models.InstallAuthState(db); err != nil {
			fatal(err.Error())
		}
		fmt.Println("authentication database bootstrapped")
	case "create-initialization-token":
		state, err := models.LoadAuthState(db)
		if err != nil {
			fatal(err.Error())
		}
		if state.State != models.AuthStateUninitialized {
			fatal("initialization token refused: auth state is not uninitialized")
		}
		credential, err := authservice.CreateInitializationCredential(db, 10*time.Minute, "ehomectl")
		if err != nil {
			fatal(err.Error())
		}
		fmt.Println(credential)
	case "reset-password":
		password := os.Getenv("EHOME_NEW_ADMIN_PASSWORD")
		_ = os.Unsetenv("EHOME_NEW_ADMIN_PASSWORD")
		if password == "" {
			fatal("EHOME_NEW_ADMIN_PASSWORD is required and must contain at least 12 characters")
		}
		updated, err := authservice.ResetPasswordHostLocal(db, password)
		password = ""
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("password reset for system administrator id=%d username=%q; existing sessions revoked\n", updated.ID, updated.Username)
	default:
		fatal("unknown auth command")
	}
}

func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
