// Command ehomectl provides host-local administration.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"gorm.io/gorm"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/datalifecycle"
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
	case "datalifecycle":
		if len(os.Args) < 3 {
			usage()
		}
		runDatalifecycle()
	default:
		usage()
	}
}

func usage() {
	fatal("usage: ehomectl auth <bootstrap-database|create-initialization-token|reset-password>\n" +
		"       ehomectl datalifecycle backfill")
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

// runDatalifecycle 数据生命周期 M 迁移步骤的同步执行入口 (方案 v3.3 §七)。
// 与服务端启动钩子不同, 本命令同步执行并以退出码报告校验结果——
// 校验自检 (§七-3) 非 0 或执行失败时 exit 非零, 供运维脚本判定。
func runDatalifecycle() {
	switch os.Args[2] {
	case "backfill":
		db := connectDB()
		ctx := context.Background()

		if err := datalifecycle.EnsureLogicalDataIndexes(ctx, db); err != nil {
			fatal(fmt.Sprintf("index creation failed: %v", err))
		}
		fmt.Println("composite indexes ensured")

		results, err := datalifecycle.NewBackfiller(db).RunOnce(ctx)
		if err != nil {
			fatal(err.Error())
		}
		failed := false
		for _, r := range results {
			if r.Err != "" {
				fmt.Fprintf(os.Stderr, "backfill %s FAILED: %s\n", r.Table, r.Err)
				failed = true
				continue
			}
			fmt.Printf("backfill %s: rows_updated=%d batches=%d resumed_from=%d\n",
				r.Table, r.RowsUpdated, r.Batches, r.ResumedFrom)
			if !r.VerifyPassed {
				fmt.Fprintf(os.Stderr, "verify %s FAILED: %d row(s) still NULL while instance has logical_device_id\n",
					r.Table, r.RowsMissing)
				failed = true
				continue
			}
			fmt.Printf("verify %s: PASSED (0 rows missing)\n", r.Table)
		}
		if failed {
			os.Exit(1)
		}
	default:
		fatal("unknown datalifecycle command")
	}
}

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
