package auth

import (
	"errors"
	"strings"
	"time"

	"ehome/backend/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

func AuthenticateSingleUser(db *gorm.DB, username, password string) (models.User, error) {
	state, err := models.LoadAuthState(db)
	if err != nil || state.State != models.AuthStateInitialized {
		return models.User{}, ErrInvalidCredentials
	}

	var user models.User
	err = db.Where(
		"subject_key = ? AND retired_at IS NULL AND enabled = ?",
		models.SystemAdminSubjectKey,
		true,
	).First(&user).Error
	if err != nil {
		return models.User{}, ErrInvalidCredentials
	}
	if user.Username != strings.TrimSpace(username) {
		return models.User{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return models.User{}, ErrInvalidCredentials
	}

	now := time.Now().UTC()
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).UpdateColumn("last_login_at", now).Error; err != nil {
		return models.User{}, err
	}
	user.LastLoginAt = &now
	return user, nil
}
