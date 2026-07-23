package models

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	SystemAuthStateKey         = "system_auth"
	AuthStateUninitialized     = "uninitialized"
	AuthStateInitialized       = "initialized"
	AuthStateMigrationRequired = "migration_required"
	AuthStateDisabled          = "disabled"
)

// AuthState is the fixed, system-level authentication state row. Application
// runtime code must never infer an uninitialized installation from a missing row.
type AuthState struct {
	Key             string     `gorm:"primaryKey;size:32" json:"key"`
	State           string     `gorm:"size:32;not null" json:"state"`
	SecurityVersion int64      `gorm:"not null;default:1;check:security_version > 0" json:"-"`
	InitializedAt   *time.Time `json:"initialized_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// LoadAuthState fails closed when the fixed state row is missing. It does not
// persist the synthetic migration_required value.
func LoadAuthState(db *gorm.DB) (AuthState, error) {
	var state AuthState
	err := db.Where("key = ?", SystemAuthStateKey).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AuthState{Key: SystemAuthStateKey, State: AuthStateMigrationRequired}, nil
	}
	return state, err
}

// InstallAuthState is for the versioned installation migration and the
// explicit server-startup bootstrap path. Runtime request handling must not
// call it to repair a missing row.
func InstallAuthState(db *gorm.DB) error {
	state := AuthState{
		Key:             SystemAuthStateKey,
		State:           AuthStateUninitialized,
		SecurityVersion: 1,
	}
	return db.Where("key = ?", SystemAuthStateKey).FirstOrCreate(&state).Error
}
