package auth

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestInitializeSystemFromEnvironmentBootstrapsFreshDatabase(t *testing.T) {
	db := testutil.OpenTestDB(t)

	initialized, err := InitializeSystemFromEnvironment(db, EnvironmentInitializationRequest{
		Username: "compose-admin",
		Password: "p8pass!!",
		Email:    "admin@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !initialized {
		t.Fatal("fresh database should be initialized")
	}

	user, err := AuthenticateSingleUser(db, "compose-admin", "p8pass!!")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "admin@example.test" || user.Role != "admin" {
		t.Fatalf("unexpected bootstrapped user: %+v", user)
	}
	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateInitialized {
		t.Fatalf("state=%q", state.State)
	}

	var tokenCount int64
	if err := db.Model(&models.InitializationToken{}).Count(&tokenCount).Error; err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 {
		t.Fatalf("internal initialization token count=%d, want 1", tokenCount)
	}
}

func TestInitializeSystemFromEnvironmentIsIdempotentAfterInitialization(t *testing.T) {
	db := testutil.OpenTestDB(t)

	first, err := InitializeSystemFromEnvironment(db, EnvironmentInitializationRequest{
		Username: "original-admin",
		Password: "original-password-123",
	})
	if err != nil || !first {
		t.Fatalf("first initialization: initialized=%t err=%v", first, err)
	}

	second, err := InitializeSystemFromEnvironment(db, EnvironmentInitializationRequest{
		Username: "replacement-admin",
		Password: "replacement-password-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("initialized database must not be initialized again")
	}
	if _, err := AuthenticateSingleUser(db, "original-admin", "original-password-123"); err != nil {
		t.Fatalf("original administrator was changed: %v", err)
	}
}

func TestInitializeSystemFromEnvironmentRequiresCompleteCredentials(t *testing.T) {
	db := testutil.OpenTestDB(t)

	initialized, err := InitializeSystemFromEnvironment(db, EnvironmentInitializationRequest{
		Username: "compose-admin",
	})
	if err != ErrIncompleteEnvironmentInitialization {
		t.Fatalf("err=%v, want incomplete environment error", err)
	}
	if initialized {
		t.Fatal("incomplete environment must not initialize the system")
	}

	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateMigrationRequired {
		t.Fatalf("state=%q, want migration_required", state.State)
	}
}

func TestInitializeSystemFromEnvironmentRefusesExistingUsers(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.User{Username: "legacy", PasswordHash: "hash", Role: "admin", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	initialized, err := InitializeSystemFromEnvironment(db, EnvironmentInitializationRequest{
		Username: "compose-admin",
		Password: "compose-password-123",
	})
	if err == nil || initialized {
		t.Fatalf("existing users must refuse bootstrap: initialized=%t err=%v", initialized, err)
	}
	state, err := models.LoadAuthState(db)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != models.AuthStateMigrationRequired {
		t.Fatalf("state=%q, want migration_required", state.State)
	}
}
