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

// jwtSecret is the HMAC secret for signing JWT tokens.
// v2.2: configurable via EHOME_JWT_SECRET env var.
var jwtSecret = []byte(func() string {
	if s := os.Getenv("EHOME_JWT_SECRET"); s != "" {
		return s
	}
	logger.Warnf("⚠️  EHOME_JWT_SECRET not set — using insecure default secret. DO NOT use in production!")
	return "ehome-dev-secret-change-me"
}())

// Claims represents the JWT payload
type Claims struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role,omitempty"`
	jwt.RegisteredClaims
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
