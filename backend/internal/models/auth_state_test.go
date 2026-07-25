package models

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuthStateMissingReturnsUninitialized(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&AuthState{}); err != nil {
		t.Fatal(err)
	}

	state, err := LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != AuthStateUninitialized {
		t.Fatalf("state = %q, want %q", state.State, AuthStateUninitialized)
	}
	var count int64
	if err := db.Model(&AuthState{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("runtime lookup must not recreate auth state, count=%d", count)
	}
}

func TestAuthStateInstalledRowLoads(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&AuthState{}); err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthState(db); err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthState(db); err != nil {
		t.Fatalf("install must be idempotent: %v", err)
	}

	state, err := LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.Key != SystemAuthStateKey || state.State != AuthStateUninitialized {
		t.Fatalf("unexpected state: %+v", state)
	}
}
