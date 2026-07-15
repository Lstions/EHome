package auth

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"golang.org/x/crypto/bcrypt"
)

func TestAuthenticateSingleUserRejectsNonSubjectAndDisabledAccounts(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	subjectKey := models.SystemAdminSubjectKey
	active := models.User{Username: "admin", PasswordHash: string(hash), Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1}
	other := models.User{Username: "other", PasswordHash: string(hash), Role: "viewer", Enabled: true, SessionVersion: 1}
	db.Create(&active)
	db.Create(&other)

	if _, err := AuthenticateSingleUser(db, "other", "password123"); err == nil {
		t.Fatal("non-subject historical account must not authenticate")
	}
	db.Model(&models.User{}).Where("id = ?", active.ID).Update("enabled", false)
	if _, err := AuthenticateSingleUser(db, "admin", "password123"); err == nil {
		t.Fatal("disabled subject must not authenticate")
	}
}

func TestAuthenticateSingleUserUpdatesLastLogin(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	hash, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: string(hash), Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1}
	db.Create(&user)

	got, err := AuthenticateSingleUser(db, "admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLoginAt == nil {
		t.Fatal("last login was not recorded")
	}
}
