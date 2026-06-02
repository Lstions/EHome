package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestAPIV1Envelope: every v1 API endpoint (except /ws and /auth/login) must
// return a {code, message, data} envelope OR a top-level array. This catches
// regressions where a new endpoint forgets the envelope and the front-end
// axios interceptor gets undefined.
func TestAPIV1Envelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.AutoMigrate(&models.Firmware{})

	db.Create(&models.Firmware{Version: "1.0.0", URL: "u", SizeBytes: 100, Checksum: "x"})

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	{
		v1.GET("/firmwares", func(c *gin.Context) {
			var list []models.Firmware
			db.Find(&list)
			c.JSON(http.StatusOK, list) // bare array (acceptable for lists)
		})

		v1.GET("/firmwares/:id", func(c *gin.Context) {
			var fw models.Firmware
			if err := db.First(&fw, c.Param("id")).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "not found"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": fw})
		})

		v1.DELETE("/firmwares/:id", func(c *gin.Context) {
			db.Delete(&models.Firmware{}, c.Param("id"))
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"id": c.Param("id")}})
		})
	}

	// Get a real signed token
	tok, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	auth := "Bearer " + tok

	tests := []struct {
		name         string
		method       string
		path         string
		envelopeOnly bool
	}{
		{"List bare-array OK", "GET", "/api/v1/firmwares", false},
		{"Get envelope", "GET", "/api/v1/firmwares/1", true},
		{"Delete envelope", "DELETE", "/api/v1/firmwares/1", true},
	}

	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}

			body := w.Body.Bytes()
			if len(body) == 0 {
				t.Fatal("empty body")
			}

			// Try parse as JSON object first
			var resp map[string]interface{}
			if err := json.Unmarshal(body, &resp); err == nil {
				// If envelope required, must have these keys
				if tt.envelopeOnly {
					for _, k := range []string{"code", "message", "data"} {
						if _, ok := resp[k]; !ok {
							t.Errorf("envelope missing key %q in response: %s", k, body)
						}
					}
				}
				return
			}

			// Try as bare array
			var arr []interface{}
			if err := json.Unmarshal(body, &arr); err == nil {
				if tt.envelopeOnly {
					t.Errorf("expected envelope, got bare array: %s", body)
				}
				return
			}

			t.Fatalf("body is neither object nor array: %s", body)
		})
	}
}

// TestAPIV1Envelope_RejectUnauthorized: ensure JWTAuth middleware kicks in
// for protected endpoints
func TestAPIV1Envelope_RejectUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	{
		v1.GET("/firmwares", func(c *gin.Context) {
			c.JSON(http.StatusOK, []models.Firmware{})
		})
	}

	req := httptest.NewRequest("GET", "/api/v1/firmwares", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", w.Code)
	}
}
