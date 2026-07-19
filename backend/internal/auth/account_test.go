package auth

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"golang.org/x/crypto/bcrypt"
)

func TestChangePasswordAtomicallyBumpsSessionAndWritesOutbox(t *testing.T) {
	db := testutil.OpenTestDB(t)
	oldHash, _ := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.DefaultCost)
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: string(oldHash), Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 4}
	db.Create(&user)

	updated, err := ChangePassword(db, user.ID, "old-password-123", "new-password-456")
	if err != nil {
		t.Fatal(err)
	}
	if updated.SessionVersion != 5 || updated.PasswordChangedAt == nil {
		t.Fatalf("unexpected updated user: %+v", updated)
	}
	if bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("new-password-456")) != nil {
		t.Fatal("new password hash does not match")
	}
	var outbox models.AuthOutbox
	if err := db.First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.SubjectID != user.ID || outbox.SessionVersion != 5 || outbox.EventType != "session.revoked" {
		t.Fatalf("unexpected outbox: %+v", outbox)
	}
}

func TestRevokeAllSessionsUsesAtomicVersionIncrement(t *testing.T) {
	db := testutil.OpenTestDB(t)
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1}
	db.Create(&user)
	updated, err := RevokeAllSessions(db, user.ID, "logout")
	if err != nil {
		t.Fatal(err)
	}
	if updated.SessionVersion != 2 {
		t.Fatalf("version=%d", updated.SessionVersion)
	}
}

func TestResetPasswordHostLocalRevokesSessionsAndAudits(t *testing.T) {
	db := testutil.OpenTestDB(t)
	oldHash, _ := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.DefaultCost)
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: string(oldHash), Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 8}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := ResetPasswordHostLocal(db, "temporary-dev-password-456")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != user.ID || updated.SessionVersion != 9 || updated.PasswordChangedAt == nil {
		t.Fatalf("unexpected reset user: %+v", updated)
	}
	if bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("temporary-dev-password-456")) != nil {
		t.Fatal("new password was not persisted")
	}
	var outbox models.AuthOutbox
	if err := db.Where("subject_id = ? AND reason = ?", user.ID, "host_password_reset").First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.SecurityAuditEvent{}).Where("event_name = ? AND result = ?", "auth.password.reset", "success").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("reset audit count=%d err=%v", count, err)
	}
}

func TestResetPasswordHostLocalRejectsWeakPassword(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if _, err := ResetPasswordHostLocal(db, "too-short"); err == nil {
		t.Fatal("weak host-local reset password was accepted")
	}
}
