// Command ehomectl provides host-local authentication administration.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/models"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "auth" {
		fatal("usage: ehomectl auth <bootstrap-database|create-initialization-token|migration-status|migrate-single-user>")
	}
	cfg := config.Load().DBConfig()
	if err := database.Connect(database.Config{Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: cfg.Password, DBName: cfg.DBName, SSLMode: cfg.SSLMode}); err != nil {
		fatal(err.Error())
	}
	db := database.GetDB()
	if err := database.AutoMigrate(); err != nil {
		fatal(err.Error())
	}

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
		if state.State != models.AuthStateMigrationRequired {
			fatal("bootstrap refused: auth state already exists")
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
	case "migration-status":
		report, err := authservice.InspectSingleUserMigration(db)
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("users=%d requires_keep_user_id=%t\n", report.UserCount, report.RequiresKeepUserID)
		for _, user := range report.Users {
			fmt.Printf("id=%d username=%q role=%q enabled=%t\n", user.ID, user.Username, user.Role, user.Enabled)
		}
	case "migrate-single-user":
		if len(os.Args) < 4 {
			fatal("usage: ehomectl auth migrate-single-user <keep-user-id>")
		}
		value, err := strconv.ParseUint(os.Args[3], 10, 64)
		if err != nil {
			fatal("invalid keep user id")
		}
		report, err := authservice.MigrateSingleUser(db, authservice.MigrateOptions{KeepUserID: uint(value)})
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("kept_user_id=%d retired=%d\n", report.KeptUserID, report.RetiredCount)
	default:
		fatal("unknown auth command")
	}
}

func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
