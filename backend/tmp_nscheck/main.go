package main

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dsn := "host=127.0.0.1 port=5432 user=ehome password=ehome123 dbname=ehome_sim_pg sslmode=disable"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	var n int64
	if err := db.Raw("SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'test_%'").Scan(&n).Error; err != nil {
		fmt.Println("query:", err)
		os.Exit(1)
	}
	fmt.Printf("leftover_test_schemas=%d\n", n)
	var names []string
	_ = db.Raw("SELECT nspname FROM pg_namespace WHERE nspname LIKE 'test_%'").Scan(&names).Error
	fmt.Println("names:", names)
}
