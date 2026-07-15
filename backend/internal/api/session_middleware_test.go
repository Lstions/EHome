package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
)

func TestJWTAuthWithDBValidatesAuthoritativeSessionVersion(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	db.Create(&models.AuthState{Key: models.SystemAuthStateKey, State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now})
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "admin", PasswordHash: "hash", Role: "admin", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, InitializedAt: &now}
	db.Create(&user)
	token, err := authservice.SignSessionToken(user, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuthWithDB(db))
	r.GET("/test", func(c *gin.Context) {
		subjectID, _ := c.Get("subject_id")
		c.JSON(http.StatusOK, gin.H{"subject_id": subjectID})
	})

	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)
		return w
	}
	first := request()
	if first.Code != http.StatusOK {
		t.Fatalf("valid session status=%d body=%s", first.Code, first.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(first.Body.Bytes(), &body)
	if body["subject_id"] != float64(user.ID) {
		t.Fatalf("subject=%v", body["subject_id"])
	}

	db.Model(&models.User{}).Where("id = ?", user.ID).UpdateColumn("session_version", 2)
	stale := request()
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("stale token status=%d", stale.Code)
	}
}
