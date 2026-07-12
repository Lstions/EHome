// Package testutil provides test helpers for EHomeSystem backend tests.
//
// It abstracts database creation so tests can run against either SQLite (fast,
// local, default) or PostgreSQL (integration, requires running PG instance).
//
// Switch via environment variable:
//
//	EHOME_TEST_DB=postgres   → connect to PG (uses EHOME_DB_HOST/PORT/USER/PASSWORD/NAME)
//	EHOME_TEST_DB=sqlite     → in-memory SQLite (default)
//
// Usage in test files:
//
//	db := testutil.OpenTestDB(t)
//	// ... use db as *gorm.DB ...
package testutil

import (
	"fmt"
	"os"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// allModels is the complete list of models for AutoMigrate.
// Centralised here so every test gets the same set.
var allModels = []interface{}{
	&models.Node{},
	&models.Channel{},
	&models.ConfigTemplate{},
	&models.EdgeDevice{},
	&models.DeviceConfig{},
	&models.DeviceData{},
	&models.UnifiedData{},
	&models.DataSource{},
	&models.OTATask{},
	&models.Firmware{},
	&models.Notification{},
	&models.User{},
	&models.OperationLog{},
	&models.Vendor{},
	&models.DeviceModel{},
	&models.NodeEvent{},
	&models.CalibrationCache{},
	&models.ConfigMeta{},
	&models.PendingWriteRecord{},
	&models.NodeLog{},
}

// OpenTestDB opens a test database based on EHOME_TEST_DB env var.
//   - "sqlite" or empty → in-memory SQLite (default, fast)
//   - "postgres"        → PostgreSQL using EHOME_DB_* env vars
//
// The database is migrated with all models automatically.
// For PostgreSQL, each test gets an isolated schema that is cleaned up via t.Cleanup.
func OpenTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	driver := os.Getenv("EHOME_TEST_DB")
	switch driver {
	case "", "sqlite":
		return openSQLite(t)
	case "postgres":
		return openPostgres(t)
	default:
		t.Fatalf("unknown EHOME_TEST_DB value %q (use 'sqlite' or 'postgres')", driver)
		return nil
	}
}

// IsPostgres returns true if the test DB backend is PostgreSQL.
func IsPostgres() bool {
	return os.Getenv("EHOME_TEST_DB") == "postgres"
}

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("sqlite automigrate: %v", err)
	}
	return db
}

func openPostgres(t *testing.T) *gorm.DB {
	t.Helper()

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open postgres: %v (dsn: host=%s port=%s user=%s dbname=%s)", err, host, port, user, dbname)
	}

	// Create an isolated schema per test for parallel safety
	schemaName := fmt.Sprintf("test_%s", randomSuffix())
	db.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName))
	db.Exec(fmt.Sprintf("SET search_path TO %s, public", schemaName))

	if err := db.AutoMigrate(allModels...); err != nil {
		t.Fatalf("postgres automigrate: %v", err)
	}

	// Cleanup: drop the test schema when test finishes
	t.Cleanup(func() {
		d, _ := db.DB()
		if d != nil {
			d.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
			d.Close()
		}
	})

	return db
}

// SetTransactionIsolation is a cross-DB helper for setting transaction
// isolation level. On PostgreSQL it executes SET TRANSACTION ISOLATION LEVEL
// REPEATABLE READ. On SQLite it is a no-op (SQLite doesn't support the
// syntax, but its transactions are already SERIALIZABLE which is stricter).
//
// Usage:
//
//	db.Transaction(func(tx *gorm.DB) error {
//	    testutil.SetTransactionIsolation(tx)
//	    // ... queries see a consistent snapshot ...
//	})
func SetTransactionIsolation(tx *gorm.DB) {
	if IsPostgres() {
		tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	}
	// SQLite: no-op. SQLite transactions are SERIALIZABLE by default,
	// which is strictly stronger than REPEATABLE READ.
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomSuffix() string {
	// Use PID + timestamp for uniqueness without importing crypto/rand
	// Fix: previously used os.Getpid() twice (identical values → schema collision)
	return fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
}
