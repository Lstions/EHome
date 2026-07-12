package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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
	ginMode := os.Getenv("GIN_MODE")
	return ginMode == "" || ginMode == "debug" || os.Getenv("EHOME_ENV") == "development"
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

// Claims represents the JWT payload
type Claims struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// devAuthBypassEnabled permits no-token local development only when explicitly
// enabled. It is intentionally impossible in production mode, even if the env
// variable is accidentally inherited.
func devAuthBypassEnabled() bool {
	return isDevelopmentMode() && strings.EqualFold(os.Getenv("EHOME_DEV_BYPASS_AUTH"), "true")
}

// JWTAuth returns a middleware that validates JWT tokens.
// It checks:
//   - Authorization: Bearer <token> header
//   - ?token=<jwt> query parameter (for WebSocket connections)
//
// On success, sets "user_id" and "role" in gin context.
// On failure, returns 401.
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if devAuthBypassEnabled() {
			// Explicit local-only bypass. Keep role=admin so development E2E can
			// exercise routes protected by RequireRole without minting a JWT.
			c.Set("user_id", uint(0))
			c.Set("role", "admin")
			c.Next()
			return
		}

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

// RequireRole returns a middleware that checks the authenticated user's role.
// It reads "role" from gin context (set by JWTAuth) and returns 403 if it doesn't match.
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if devAuthBypassEnabled() {
			c.Next()
			return
		}
		r, exists := c.Get("role")
		if !exists || r != role {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": fmt.Sprintf("%s role required", role),
			})
			return
		}
		c.Next()
	}
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
