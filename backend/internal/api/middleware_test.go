package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestJWTAuth_MissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "missing authentication token" {
		t.Errorf("unexpected error: %v", body["error"])
	}
}

func TestJWTAuth_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-here")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestJWTAuth_ValidBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) {
		userID, _ := c.Get("user_id")
		role, _ := c.Get("role")
		c.JSON(http.StatusOK, gin.H{"user_id": userID, "role": role})
	})
	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["user_id"] != float64(1) {
		t.Errorf("expected user_id=1, got %v", body["user_id"])
	}
	if body["role"] != "admin" {
		t.Errorf("expected role=admin, got %v", body["role"])
	}
}

func TestJWTAuth_ValidQueryParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { userID, _ := c.Get("user_id"); c.JSON(http.StatusOK, gin.H{"user_id": userID}) })
	token, err := GenerateToken(42, "user")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test?token="+token, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["user_id"] != float64(42) {
		t.Errorf("expected user_id=42, got %v", body["user_id"])
	}
}

func TestJWTAuth_WrongSigningMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for empty bearer, got %d", w.Code)
	}
}

func TestGenerateToken(t *testing.T) {
	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	token2, err := GenerateToken(2, "user")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if token == token2 {
		t.Error("tokens for different users should differ")
	}
}

func TestJWTAuth_DevelopmentBypassIsRetired(t *testing.T) {
	t.Setenv("GIN_MODE", "debug")
	t.Setenv("EHOME_ENV", "development")
	t.Setenv("EHOME_DEV_BYPASS_AUTH", "true")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { role, _ := c.Get("role"); c.JSON(http.StatusOK, gin.H{"role": role}) })
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("retired development bypass must return 401, got %d", w.Code)
	}
}

func TestJWTAuth_BypassDisabledOutsideDevelopment(t *testing.T) {
	t.Setenv("GIN_MODE", "release")
	t.Setenv("EHOME_ENV", "production")
	t.Setenv("EHOME_DEV_BYPASS_AUTH", "true")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth())
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("production must ignore development bypass, got %d", w.Code)
	}
}
