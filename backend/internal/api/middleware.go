package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// JWTSecret is the shared secret for token validation.
// In production, load from config/env. For now, use a default.
var JWTSecret = []byte(getJWTSecret())

func getJWTSecret() string {
	// Allow override via env
	if v := getEnvDefault("EHOME_JWT_SECRET", ""); v != "" {
		return v
	}
	// Default secret for development
	return "ehome-dev-jwt-secret-2024"
}

func getEnvDefault(key, fallback string) string {
	if v := strings.TrimSpace(getEnvOrEmpty(key)); v != "" {
		return v
	}
	return fallback
}

func getEnvOrEmpty(key string) string {
	if v, ok := getEnv2(key); ok {
		return v
	}
	return ""
}

// Simple env lookup without importing os in this helper
var envLookup = func(key string) (string, bool) {
	return "", false
}

func getEnv2(key string) (string, bool) {
	return envLookup(key)
}

// AuthMiddleware validates JWT tokens in the Authorization header or query param.
// Supports: Authorization: Bearer <token>  OR  ?token=<token>
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""

		// 1. Check Authorization header
		auth := c.GetHeader("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}

		// 2. Check query parameter (useful for WebSocket connections)
		if token == "" {
			token = c.Query("token")
		}

		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing authentication token",
			})
			return
		}

		// Validate JWT token
		claims, err := ValidateJWT(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid token: " + err.Error(),
			})
			return
		}

		// Store user info in context
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}
