package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var ErrSystemNotUninitialized = errors.New("system is not available for initialization")

type InitializeRequest struct {
	Credential string
	Username   string
	Password   string
	Email      string
}

func InitializeSystem(db *gorm.DB, request InitializeRequest) (models.User, error) {
	if strings.TrimSpace(request.Username) == "" || len(request.Password) < 12 {
		return models.User{}, errors.New("username and a password of at least 12 characters are required")
	}
	token, err := VerifyInitializationCredential(db, request.Credential, 5)
	if err != nil {
		return models.User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		return models.User{}, err
	}

	var created models.User
	err = db.Transaction(func(tx *gorm.DB) error {
		var state models.AuthState
		if err := tx.Where("key = ?", models.SystemAuthStateKey).First(&state).Error; err != nil {
			return ErrSystemNotUninitialized
		}
		if state.State != models.AuthStateUninitialized {
			return ErrSystemNotUninitialized
		}

		var lockedToken models.InitializationToken
		if err := tx.Where("id = ? AND consumed_at IS NULL", token.ID).First(&lockedToken).Error; err != nil {
			return ErrInvalidInitializationCredential
		}
		if time.Now().UTC().After(lockedToken.ExpiresAt) {
			return ErrInvalidInitializationCredential
		}

		var activeCount int64
		if err := tx.Model(&models.User{}).Where("subject_key = ? AND retired_at IS NULL", models.SystemAdminSubjectKey).Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount != 0 {
			return fmt.Errorf("active subject already exists")
		}

		now := time.Now().UTC()
		subjectKey := models.SystemAdminSubjectKey
		created = models.User{
			Username:          strings.TrimSpace(request.Username),
			PasswordHash:      string(hash),
			Email:             strings.TrimSpace(request.Email),
			Role:              "admin",
			Enabled:           true,
			SubjectKey:        &subjectKey,
			SessionVersion:    1,
			PasswordChangedAt: &now,
			InitializedAt:     &now,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if result := tx.Model(&models.InitializationToken{}).
			Where("id = ? AND consumed_at IS NULL", lockedToken.ID).
			Update("consumed_at", now); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return ErrInvalidInitializationCredential
		}
		if err := audit.NewWriter(tx).Write(audit.Event{ActorType: "system", ActorUserID: &created.ID, ActorSnapshot: created.Username, EventName: "auth.initialized", Result: "success", TargetType: "account", TargetID: fmt.Sprint(created.ID)}); err != nil {
			return err
		}
		return tx.Model(&models.AuthState{}).
			Where("key = ? AND state = ?", models.SystemAuthStateKey, models.AuthStateUninitialized).
			Updates(map[string]interface{}{
				"state":          models.AuthStateInitialized,
				"initialized_at": now,
				"updated_at":     now,
			}).Error
	})
	return created, err
}
