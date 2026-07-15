package auth

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

func TestCriticalAuthenticationTransactionsWriteSecurityAudit(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := models.InstallAuthState(db); err != nil {
		t.Fatal(err)
	}
	credential, _ := CreateInitializationCredential(db, time.Minute, "test")
	user, err := InitializeSystem(db, InitializeRequest{Credential: credential, Username: "admin", Password: "old-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	if countAudit(t, db, "auth.initialized") != 1 {
		t.Fatal("missing initialization audit")
	}
	if _, err := ChangePassword(db, user.ID, "old-password-123", "new-password-456"); err != nil {
		t.Fatal(err)
	}
	if countAudit(t, db, "auth.password.changed") != 1 {
		t.Fatal("missing password audit")
	}
	if _, err := RevokeAllSessions(db, user.ID, "logout"); err != nil {
		t.Fatal(err)
	}
	if countAudit(t, db, "auth.sessions.revoked") != 1 {
		t.Fatal("missing revoke audit")
	}
}

func countAudit(t *testing.T, db *gorm.DB, name string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.SecurityAuditEvent{}).Where("event_name = ?", name).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}
