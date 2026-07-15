package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/auth"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
)

func TestLoginRouteReturns429AfterFailureLimit(t *testing.T) {
	db := testutil.OpenTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAuthRoutesWithLimiter(r, db, auth.NewLoginLimiter(nil, 1, time.Minute))
	body, _ := json.Marshal(LoginRequest{Username: "admin", Password: "wrong"})
	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body)))
	second := httptest.NewRecorder()
	r.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body)))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}
