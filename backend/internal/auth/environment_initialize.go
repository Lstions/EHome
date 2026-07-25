package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// EnvironmentInitializationRequest is used only by the server startup path.
// It allows an operator who explicitly supplied both environment variables to
// initialize a fresh deployment without copying a one-time CLI credential.
type EnvironmentInitializationRequest struct {
	Username string
	Password string
	Email    string
}

var ErrIncompleteEnvironmentInitialization = errors.New("EHOME_ADMIN_USERNAME and EHOME_ADMIN_PASSWORD must be provided together")

// InitializeSystemFromEnvironment initializes the system once when explicit
// administrator credentials are configured. It is intentionally a no-op for
// an already initialized database, so retaining the variables does not reset
// or replace the administrator on later restarts.
func InitializeSystemFromEnvironment(db *gorm.DB, request EnvironmentInitializationRequest) (bool, error) {
	username := strings.TrimSpace(request.Username)
	if username == "" && request.Password == "" {
		return false, nil
	}
	if username == "" || request.Password == "" {
		return false, ErrIncompleteEnvironmentInitialization
	}

	state, err := models.LoadAuthState(db)
	if err != nil {
		return false, err
	}
	if state.State == models.AuthStateInitialized {
		return false, nil
	}
	if state.State == models.AuthStateDisabled {
		return false, ErrSystemNotUninitialized
	}
	if state.State != models.AuthStateUninitialized {
		return false, ErrSystemNotUninitialized
	}
	if err := validateInitializationInput(username, request.Password); err != nil {
		return false, err
	}

	// A missing state row is normally uninitialized and must be persisted
	// before the initialization flow can proceed. The explicit administrator
	// environment is the operator's opt-in to a fresh-install bootstrap,
	// but only while the database is truly empty.
	var users int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		return false, err
	}
	if users != 0 {
		return false, fmt.Errorf("environment bootstrap refused: %d user(s) already exist", users)
	}
	if err := models.InstallAuthState(db); err != nil {
		return false, err
	}

	// Reuse the audited, transactional initialization flow. The generated
	// credential never leaves this process and is consumed immediately.
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "environment")
	if err != nil {
		return false, err
	}
	if _, err := InitializeSystem(db, InitializeRequest{
		Credential: credential,
		Username:   username,
		Password:   request.Password,
		Email:      request.Email,
	}); err != nil {
		return false, err
	}
	return true, nil
}
