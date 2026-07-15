package auth

import (
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestCreateInitializationCredentialStoresOnlyHash(t *testing.T) {
	db := testutil.OpenTestDB(t)
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(credential, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("credential format = %q", credential)
	}

	var token models.InitializationToken
	if err := db.Where("selector = ?", parts[0]).First(&token).Error; err != nil {
		t.Fatal(err)
	}
	if token.SecretHash == "" || token.SecretHash == parts[1] {
		t.Fatal("database must store a hash, not the secret")
	}
	if token.AttemptCount != 0 || token.ConsumedAt != nil {
		t.Fatalf("unexpected token state: %+v", token)
	}
}

func TestVerifyInitializationCredentialPersistsKnownTokenFailures(t *testing.T) {
	db := testutil.OpenTestDB(t)
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	selector := strings.Split(credential, ".")[0]

	if _, err := VerifyInitializationCredential(db, selector+".wrong-secret", 3); err == nil {
		t.Fatal("wrong secret should fail")
	}
	var token models.InitializationToken
	if err := db.Where("selector = ?", selector).First(&token).Error; err != nil {
		t.Fatal(err)
	}
	if token.AttemptCount != 1 {
		t.Fatalf("attempt count = %d, want 1", token.AttemptCount)
	}
}

func TestVerifyInitializationCredentialAcceptsValidUnexpiredToken(t *testing.T) {
	db := testutil.OpenTestDB(t)
	credential, err := CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	token, err := VerifyInitializationCredential(db, credential, 3)
	if err != nil {
		t.Fatal(err)
	}
	if token.Selector != strings.Split(credential, ".")[0] {
		t.Fatalf("selector = %q", token.Selector)
	}
}

func TestVerifyInitializationCredentialRejectsExpiredToken(t *testing.T) {
	db := testutil.OpenTestDB(t)
	credential, err := CreateInitializationCredential(db, -time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyInitializationCredential(db, credential, 3); err == nil {
		t.Fatal("expired token should fail")
	}
}
