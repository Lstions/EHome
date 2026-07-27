package auth

import (
	"errors"
	"fmt"
	"math"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	ErrWrongPassword          = errors.New("wrong password")
	ErrSessionVersionOverflow = errors.New("session version overflow")
)

func ChangePassword(db *gorm.DB, userID uint, oldPassword, newPassword string) (models.User, error) {
	if len(newPassword) < minimumPasswordLength || newPassword == oldPassword {
		return models.User{}, errors.New("new password does not satisfy policy")
	}
	var updated models.User
	err := db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Where("id = ? AND subject_key = ? AND retired_at IS NULL", userID, models.SystemAdminSubjectKey).First(&user).Error; err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)) != nil {
			return ErrWrongPassword
		}
		if user.SessionVersion >= math.MaxInt64 {
			return ErrSessionVersionOverflow
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		result := tx.Model(&models.User{}).Where("id = ? AND session_version = ?", user.ID, user.SessionVersion).
			Updates(map[string]interface{}{
				"password_hash":       string(hash),
				"password_changed_at": now,
				"session_version":     gorm.Expr("session_version + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrInvalidTransaction
		}
		if err := tx.First(&updated, user.ID).Error; err != nil {
			return err
		}
		if err := writeRevocationOutbox(tx, updated, "password_changed"); err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "admin", ActorUserID: &updated.ID, ActorSnapshot: updated.Username, EventName: "auth.password.changed", Result: "success", TargetType: "account"})
	})
	return updated, err
}

// ResetPasswordHostLocal is reserved for the host-local ehomectl recovery
// path. Possession of database administration access is the authorization
// boundary; the password is accepted only through process environment, never
// an HTTP endpoint or command-line argument. The reset atomically revokes all
// existing sessions and writes the same durable revocation/audit evidence as
// an authenticated password change.
func ResetPasswordHostLocal(db *gorm.DB, newPassword string) (models.User, error) {
	if len(newPassword) < 12 {
		return models.User{}, errors.New("new password does not satisfy policy")
	}
	var updated models.User
	err := db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Where("subject_key = ? AND retired_at IS NULL AND enabled = ?", models.SystemAdminSubjectKey, true).First(&user).Error; err != nil {
			return err
		}
		if user.SessionVersion >= math.MaxInt64 {
			return ErrSessionVersionOverflow
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		result := tx.Model(&models.User{}).Where("id = ? AND session_version = ?", user.ID, user.SessionVersion).
			Updates(map[string]interface{}{
				"password_hash":       string(hash),
				"password_changed_at": now,
				"session_version":     gorm.Expr("session_version + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrInvalidTransaction
		}
		if err := tx.First(&updated, user.ID).Error; err != nil {
			return err
		}
		if err := writeRevocationOutbox(tx, updated, "host_password_reset"); err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "system", ActorSnapshot: "ehomectl", EventName: "auth.password.reset", Result: "success", TargetType: "account", TargetID: fmt.Sprint(updated.ID)})
	})
	return updated, err
}

func RevokeAllSessions(db *gorm.DB, userID uint, reason string) (models.User, error) {
	var updated models.User
	err := db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Where("id = ? AND subject_key = ? AND retired_at IS NULL", userID, models.SystemAdminSubjectKey).First(&user).Error; err != nil {
			return err
		}
		if user.SessionVersion >= math.MaxInt64 {
			return ErrSessionVersionOverflow
		}
		result := tx.Model(&models.User{}).Where("id = ? AND session_version = ?", user.ID, user.SessionVersion).
			UpdateColumn("session_version", gorm.Expr("session_version + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrInvalidTransaction
		}
		if err := tx.First(&updated, user.ID).Error; err != nil {
			return err
		}
		if err := writeRevocationOutbox(tx, updated, reason); err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "admin", ActorUserID: &updated.ID, ActorSnapshot: updated.Username, EventName: "auth.sessions.revoked", Result: "success", TargetType: "account", Metadata: map[string]interface{}{"reason": reason}})
	})
	return updated, err
}

func writeRevocationOutbox(tx *gorm.DB, user models.User, reason string) error {
	return tx.Create(&models.AuthOutbox{
		EventType:      "session.revoked",
		SubjectID:      user.ID,
		SessionVersion: user.SessionVersion,
		Reason:         reason,
		CreatedAt:      time.Now().UTC(),
	}).Error
}
