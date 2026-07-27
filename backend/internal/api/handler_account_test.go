package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func setupAccountRouter(t *testing.T) (*gin.Engine, *models.User, string) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	hash, _ := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.DefaultCost)
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", Email: "admin@example.test", PasswordHash: string(hash), Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, InitializedAt: &now}
	db.Create(&user)
	token, err := authservice.SignSessionToken(user, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuthWithDB(db))
	registerAccountRoutesWithLimiter(v1, db, authservice.NewLoginLimiter(nil, 2, time.Minute))
	return r, &user, token
}

func TestAccountReauthenticateRefreshesRecentAuthentication(t *testing.T) {
	r, _, token := setupAccountRouter(t)
	body, _ := json.Marshal(map[string]string{"password": "old-password-123"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/reauthenticate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			AuthenticatedAt *time.Time `json:"authenticated_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.AuthenticatedAt == nil {
		t.Fatalf("missing authenticated_at: %s", w.Body.String())
	}
}

func TestAccountReauthenticateFailureKeepsSessionAndRateLimits(t *testing.T) {
	r, _, token := setupAccountRouter(t)
	for attempt, want := range []int{http.StatusForbidden, http.StatusForbidden, http.StatusTooManyRequests} {
		body, _ := json.Marshal(map[string]string{"password": "wrong-password"})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/account/reauthenticate", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("attempt=%d status=%d want=%d body=%s", attempt+1, w.Code, want, w.Body.String())
		}
		if attempt == 0 && !bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"invalid_reauthentication_credentials"`)) {
			t.Fatalf("missing machine error code: %s", w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/account", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("failed reauthentication revoked session: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAccountGetReturnsCurrentSubject(t *testing.T) {
	r, user, token := setupAccountRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/account", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data models.User `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	if response.Data.ID != user.ID || response.Data.Username != "admin" {
		t.Fatalf("unexpected account: %+v", response.Data)
	}
}

func TestAccountPasswordChangeRevokesPresentedToken(t *testing.T) {
	r, _, token := setupAccountRouter(t)
	body, _ := json.Marshal(map[string]string{"old_password": "old-password-123", "new_password": "new-password-456"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	stale := httptest.NewRecorder()
	staleReq := httptest.NewRequest(http.MethodGet, "/api/v1/account", nil)
	staleReq.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(stale, staleReq)
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("old token status=%d", stale.Code)
	}
}
