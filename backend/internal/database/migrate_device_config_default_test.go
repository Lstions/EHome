package database

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openDeviceConfigDefaultDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DeviceConfig{}); err != nil {
		t.Fatalf("migrate device configs: %v", err)
	}
	return db
}

func TestEnsureDeviceConfigDefaultConstraintEnforcesOneLiveDefault(t *testing.T) {
	db := openDeviceConfigDefaultDB(t)
	if err := ensureDeviceConfigDefaultConstraint(db); err != nil {
		t.Fatalf("install constraint: %v", err)
	}
	first := models.DeviceConfig{Name: "first", DeviceType: "bmp280", IsDefault: true}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first default: %v", err)
	}
	second := models.DeviceConfig{Name: "second", DeviceType: "bmp280", IsDefault: true}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected duplicate live default to be rejected")
	}
	otherType := models.DeviceConfig{Name: "other", DeviceType: "sht30", IsDefault: true}
	if err := db.Create(&otherType).Error; err != nil {
		t.Fatalf("default for another device type must be allowed: %v", err)
	}
}

func TestEnsureDeviceConfigDefaultConstraintAllowsReplacementAfterSoftDelete(t *testing.T) {
	db := openDeviceConfigDefaultDB(t)
	if err := ensureDeviceConfigDefaultConstraint(db); err != nil {
		t.Fatalf("install constraint: %v", err)
	}
	first := models.DeviceConfig{Name: "first", DeviceType: "bmp280", IsDefault: true}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first default: %v", err)
	}
	if err := db.Delete(&first).Error; err != nil {
		t.Fatalf("soft-delete default: %v", err)
	}
	second := models.DeviceConfig{Name: "second", DeviceType: "bmp280", IsDefault: true}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("replacement default after soft-delete: %v", err)
	}
}

func TestEnsureDeviceConfigDefaultConstraintFailsClosedOnHistoricalDuplicates(t *testing.T) {
	db := openDeviceConfigDefaultDB(t)
	if err := db.Create(&models.DeviceConfig{Name: "first", DeviceType: "bmp280", IsDefault: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DeviceConfig{Name: "second", DeviceType: "bmp280", IsDefault: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureDeviceConfigDefaultConstraint(db); err == nil || !strings.Contains(err.Error(), "migration_required") || !strings.Contains(err.Error(), "bmp280") {
		t.Fatalf("expected actionable duplicate migration error, got %v", err)
	}
}
