package auth

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestCreateStartupInitializationCredentialInstallsMissingState(t *testing.T) {
	db := testutil.OpenTestDB(t)

	credential, err := CreateStartupInitializationCredential(db)
	if err != nil {
		t.Fatal(err)
	}
	if credential == "" {
		t.Fatal("expected startup initialization credential")
	}

	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateUninitialized {
		t.Fatalf("state=%q, want uninitialized", state.State)
	}
	if _, err := VerifyInitializationCredential(db, credential, 5); err != nil {
		t.Fatalf("generated credential is not valid: %v", err)
	}
}

func TestCreateStartupInitializationCredentialCreatesNewCredentialEachStartup(t *testing.T) {
	db := testutil.OpenTestDB(t)

	first, err := CreateStartupInitializationCredential(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateStartupInitializationCredential(db)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("each startup must receive a fresh credential")
	}

	var count int64
	if err := db.Model(&models.InitializationToken{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("token count=%d, want 2", count)
	}
}

func TestCreateStartupInitializationCredentialSkipsInitializedState(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	if err := db.Create(&models.AuthState{
		Key:             models.SystemAuthStateKey,
		State:           models.AuthStateInitialized,
		SecurityVersion: 1,
		InitializedAt:   &now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	credential, err := CreateStartupInitializationCredential(db)
	if err != nil {
		t.Fatal(err)
	}
	if credential != "" {
		t.Fatalf("initialized state produced credential %q", credential)
	}
}
