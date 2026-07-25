package auth

import (
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

const startupInitializationCredentialTTL = 10 * time.Minute

// CreateStartupInitializationCredential creates a fresh one-time credential
// for every server startup while authentication is still uninitialized.
// The full credential is returned to the caller so the host-local startup
// path can print it once; it is never stored in plaintext in the database.
func CreateStartupInitializationCredential(db *gorm.DB) (string, error) {
	state, err := models.LoadAuthState(db)
	if err != nil {
		return "", err
	}
	if state.State == models.AuthStateInitialized || state.State == models.AuthStateDisabled {
		return "", nil
	}
	if state.State != models.AuthStateUninitialized {
		return "", ErrSystemNotUninitialized
	}
	if err := models.InstallAuthState(db); err != nil {
		return "", err
	}
	return CreateInitializationCredential(db, startupInitializationCredentialTTL, "server-startup")
}
