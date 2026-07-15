package api

import (
	"net/http"
	"os"
	"strings"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

const defaultJWTSecret = "ehome-dev-secret-change-me"

// jwtSecret is the HMAC secret for signing JWT tokens.
// v2.2: configurable via EHOME_JWT_SECRET env var.
var jwtSecret = []byte(func() string {
	if s := os.Getenv("EHOME_JWT_SECRET"); s != "" {
		return s
	}
	return defaultJWTSecret
}())

// isDevelopmentMode returns true if the server is running in development mode.
// Development mode is enabled when GIN_MODE is unset (Gin defaults to debug),
// GIN_MODE=debug, or EHOME_ENV=development.
func isDevelopmentMode() bool {
	return strings.EqualFold(os.Getenv("EHOME_ENV"), "development") && os.Getenv("GIN_MODE") == "debug"
}

// ValidateJWTSecret checks that the JWT secret is not the default value in production.
// Must be called from main.go at startup.
func ValidateJWTSecret() {
	if string(jwtSecret) == defaultJWTSecret && !isDevelopmentMode() {
		logger.Fatalf("JWT secret is set to default value. Set EHOME_JWT_SECRET env var or run in development mode (GIN_MODE=debug or EHOME_ENV=development)")
	}
	if string(jwtSecret) == defaultJWTSecret {
		logger.Warnf("WARNING: using default JWT secret in development mode. Set EHOME_JWT_SECRET for production.")
	}
}

// Claims is retained only for compatibility tests around legacy token parsing.
type Claims struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// JWTAuth validates legacy tokens for compatibility tests only. Production
// routes use JWTAuthWithDB and the authoritative session-version contract.
// It checks:
//   - Authorization: Bearer <token> header
//   - ?token=<jwt> query parameter (for WebSocket connections)
//
// On success, sets "user_id" and "role" in gin context.
// On failure, returns 401.
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {

		tokenStr := ""

		// 1. Try Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenStr = strings.TrimSpace(parts[1])
			}
		}

		// 2. Fallback to query parameter (for WebSocket)
		if tokenStr == "" {
			tokenStr = c.Query("token")
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing authentication token",
			})
			return
		}

		// Parse and validate token
		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
			// Validate signing method
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
			})
			return
		}

		if claims, ok := token.Claims.(*Claims); ok {
			c.Set("user_id", claims.UserID)
			c.Set("role", claims.Role)
		}

		c.Next()
	}
}

// JWTAuthWithDB validates the single authenticated subject against the
// authoritative database on every request.
func JWTAuthWithDB(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractToken(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "missing authentication token"})
			return
		}
		claims, err := authservice.ParseSessionToken(tokenStr, jwtSecret, time.Now())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "invalid or expired token"})
			return
		}
		user, err := authservice.ValidateSessionClaims(db, claims)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "invalid or expired token"})
			return
		}
		c.Set("subject_id", user.ID)
		c.Set("user_id", user.ID)
		c.Set("session_version", user.SessionVersion)
		c.Set("token_expires_at", claims.ExpiresAt.Time)
		c.Set("token_jti", claims.ID)
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token
			}
		}
	}
	return strings.TrimSpace(c.Query("token"))
}

// GenerateToken creates a JWT token for a given user ID and role.
// Token expires in 24 hours.
func GenerateToken(userID uint, role string) (string, error) {
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "ehome",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}
