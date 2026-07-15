package auth

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestInitializeSystemConsumesCredentialAndCreatesOnlySubject(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := models.InstallAuthState(db); err != nil {
		t.Fatal(err)
	}
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}

	user, err := InitializeSystem(db, InitializeRequest{
		Credential: credential,
		Username:   "admin",
		Password:   "strong-password-123",
		Email:      "admin@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.SubjectKey == nil || *user.SubjectKey != models.SystemAdminSubjectKey || user.SessionVersion != 1 {
		t.Fatalf("unexpected initialized user: %+v", user)
	}
	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateInitialized {
		t.Fatalf("state=%q", state.State)
	}
	var token models.InitializationToken
	if err := db.First(&token).Error; err != nil {
		t.Fatal(err)
	}
	if token.ConsumedAt == nil {
		t.Fatal("credential was not consumed")
	}
	if _, err := InitializeSystem(db, InitializeRequest{Credential: credential, Username: "second", Password: "strong-password-456"}); err == nil {
		t.Fatal("replayed initialization must fail")
	}
}

func TestInitializeSystemFailsClosedWhenAuthStateMissing(t *testing.T) {
	db := testutil.OpenTestDB(t)
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeSystem(db, InitializeRequest{Credential: credential, Username: "admin", Password: "strong-password-123"}); err == nil {
		t.Fatal("missing auth state must not be treated as a fresh install")
	}
}
