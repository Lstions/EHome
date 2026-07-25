// Command ehomectl provides host-local authentication administration.
package main

import (
	"fmt"
	"os"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/models"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "auth" {
		fatal("usage: ehomectl auth <bootstrap-database|create-initialization-token>")
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
	default:
		fatal("unknown auth command")
	}
}

func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
