package auth

import (
	"errors"
	"fmt"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

var ErrKeepUserRequired = errors.New("keep user id is required when multiple users exist")

type MigrationUser struct {
	ID           uint
	Username     string
	Role         string
	Enabled      bool
	PasswordHash string
}

type MigrationReport struct {
	UserCount          int64
	Users              []MigrationUser
	RequiresKeepUserID bool
	KeptUserID         uint
	RetiredCount       int64
}

type MigrateOptions struct {
	KeepUserID     uint
	EnableKeptUser bool
}

func InspectSingleUserMigration(db *gorm.DB) (MigrationReport, error) {
	var users []models.User
	if err := db.Order("id").Find(&users).Error; err != nil {
		return MigrationReport{}, err
	}
	report := MigrationReport{
		UserCount:          int64(len(users)),
		RequiresKeepUserID: len(users) > 1,
		Users:              make([]MigrationUser, 0, len(users)),
	}
	for _, user := range users {
		report.Users = append(report.Users, MigrationUser{
			ID:       user.ID,
			Username: user.Username,
			Role:     user.Role,
			Enabled:  user.Enabled,
		})
	}
	return report, nil
}

func MigrateSingleUser(db *gorm.DB, options MigrateOptions) (MigrationReport, error) {
	report, err := InspectSingleUserMigration(db)
	if err != nil {
		return MigrationReport{}, err
	}
	if report.UserCount > 1 && options.KeepUserID == 0 {
		return MigrationReport{}, ErrKeepUserRequired
	}
	if report.UserCount == 0 {
		return report, nil
	}

	keepID := options.KeepUserID
	if keepID == 0 && len(report.Users) == 1 {
		keepID = report.Users[0].ID
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		var kept models.User
		if err := tx.First(&kept, keepID).Error; err != nil {
			return fmt.Errorf("keep user %d: %w", keepID, err)
		}
		if kept.RetiredAt != nil {
			return fmt.Errorf("keep user %d is retired", keepID)
		}
		if kept.PasswordHash == "" {
			return fmt.Errorf("keep user %d has invalid password hash", keepID)
		}
		if !kept.Enabled && !options.EnableKeptUser {
			return fmt.Errorf("keep user %d is disabled; explicit enable is required", keepID)
		}

		subjectKey := models.SystemAdminSubjectKey
		updates := map[string]interface{}{
			"subject_key":     subjectKey,
			"session_version": int64(1),
			"role":            "admin",
		}
		if options.EnableKeptUser {
			updates["enabled"] = true
		}
		if err := tx.Model(&models.User{}).Where("id = ?", keepID).Updates(updates).Error; err != nil {
			return err
		}

		now := time.Now().UTC()
		result := tx.Model(&models.User{}).
			Where("id <> ? AND retired_at IS NULL", keepID).
			Updates(map[string]interface{}{
				"subject_key": nil,
				"retired_at":  now,
				"enabled":     false,
			})
		if result.Error != nil {
			return result.Error
		}
		report.RetiredCount = result.RowsAffected

		state := models.AuthState{
			Key:             models.SystemAuthStateKey,
			State:           models.AuthStateInitialized,
			SecurityVersion: 1,
			InitializedAt:   &now,
		}
		return tx.Where("key = ?", models.SystemAuthStateKey).Assign(state).FirstOrCreate(&state).Error
	})
	if err != nil {
		return MigrationReport{}, err
	}
	report.KeptUserID = keepID
	return report, nil
}
