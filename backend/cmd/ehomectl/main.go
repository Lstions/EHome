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
	case "command-intervals":
		runCommandIntervalsCleanupCLI()
	default:
		usage()
	}
}

func usage() {
	fatal("usage: ehomectl auth <bootstrap-database|create-initialization-token|reset-password>\n" +
		"       ehomectl retire-logical-device\n" +
		"       ehomectl command-intervals cleanup")
}

func connectDB() *gorm.DB {
	cfg := config.Load().DBConfig()
	if err := database.Connect(database.Config{Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: cfg.Password, DBName: cfg.DBName, SSLMode: cfg.SSLMode}); err != nil {
		fatal(err.Error())
	}
	db := database.GetDB()
	if err := database.AutoMigrate(); err != nil {
		fatal(err.Error())
	}
	return db
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
