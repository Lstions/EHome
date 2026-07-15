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
)

func TestInitializationStatusAndInitializeRoutes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := models.InstallAuthState(db); err != nil {
		t.Fatal(err)
	}
	credential, err := authservice.CreateInitializationCredential(db, 10*time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAuthRoutes(r, db)

	status := httptest.NewRecorder()
	r.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/auth/initialization", nil))
	if status.Code != http.StatusOK || !bytes.Contains(status.Body.Bytes(), []byte("uninitialized")) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}

	body, _ := json.Marshal(map[string]string{"credential": credential, "username": "admin", "password": "strong-password-123", "email": "admin@example.test"})
	initialized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/initialize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(initialized, req)
	if initialized.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", initialized.Code, initialized.Body.String())
	}

	replay := httptest.NewRecorder()
	replayReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/initialize", bytes.NewReader(body))
	replayReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(replay, replayReq)
	if replay.Code == http.StatusCreated {
		t.Fatal("initialization credential replay succeeded")
	}
}
