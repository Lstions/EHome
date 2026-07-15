package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
)

func TestLegacyUsersRoutesReturnGoneAfterAuthentication(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1}
	db.Create(&user)
	token, _ := authservice.SignSessionToken(user, jwtSecret, time.Hour)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuthWithDB(db))
	registerLegacyUserRoutes(v1)

	for _, path := range []string{"/api/v1/users", "/api/v1/users/1", "/api/v1/users/1/reset-password"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusGone {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Deprecation") == "" {
			t.Fatalf("%s missing deprecation header", path)
		}
	}
}

func TestLegacyUsersRoutesRequireAuthenticationBeforeGone(t *testing.T) {
	db := testutil.OpenTestDB(t)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuthWithDB(db))
	registerLegacyUserRoutes(v1)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
