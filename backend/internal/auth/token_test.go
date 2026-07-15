package auth

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/golang-jwt/jwt/v5"
)

func TestSessionTokenRoundTripAndDatabaseValidation(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	if err := db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 3, InitializedAt: &now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	token, err := SignSessionToken(user, []byte("test-secret-at-least-32-bytes-long"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseSessionToken(token, []byte("test-secret-at-least-32-bytes-long"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "1" || claims.SessionVersion != 3 || claims.ID == "" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	validated, err := ValidateSessionClaims(db, claims)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ID != user.ID {
		t.Fatalf("validated user=%d", validated.ID)
	}
}

func TestParseSessionTokenRejectsLegacyTokenWithoutSessionVersion(t *testing.T) {
	legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": 1,
		"role":    "admin",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	raw, err := legacy.SignedString([]byte("test-secret-at-least-32-bytes-long"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSessionToken(raw, []byte("test-secret-at-least-32-bytes-long"), time.Now()); err == nil {
		t.Fatal("legacy token without required claims must be rejected")
	}
}

func TestValidateSessionClaimsRejectsStaleVersion(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 2, InitializedAt: &now}
	db.Create(&user)
	claims := &SessionClaims{SessionVersion: 1, RegisteredClaims: jwt.RegisteredClaims{Subject: "1"}}
	if _, err := ValidateSessionClaims(db, claims); err == nil {
		t.Fatal("stale session version must be rejected")
	}
}
