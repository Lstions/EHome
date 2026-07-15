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
