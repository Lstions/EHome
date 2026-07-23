package api

import (
	"net/http"
	"strconv"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	redisstore "ehome/backend/internal/redis"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LoginRequest represents the login request body
type LoginRequest struct {
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
	RememberMe bool   `json:"rememberMe"`
}

// LoginResponse represents the login response
type LoginResponse struct {
	Token string `json:"token"`
	User  struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// registerAuthRoutes sets up authentication routes (no JWT required)
func registerAuthRoutes(r *gin.Engine, db *gorm.DB) {
	registerAuthRoutesWithLimiter(r, db, authservice.NewLoginLimiter(redisstore.Client, 5, 15*time.Minute))
}

func registerAuthRoutesWithLimiter(r *gin.Engine, db *gorm.DB, limiter *authservice.LoginLimiter) {
	auth := r.Group("/api/v1/auth")
	{
		auth.GET("/initialization", func(c *gin.Context) {
			state, err := models.LoadAuthState(db)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"code": "AUTH_MIGRATION_REQUIRED", "data": gin.H{"state": models.AuthStateMigrationRequired}})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": gin.H{"state": state.State}})
		})
		auth.POST("/initialize", func(c *gin.Context) {
			var request struct {
				Credential string `json:"credential" binding:"required"`
				Username   string `json:"username" binding:"required"`
				Password   string `json:"password" binding:"required"`
				Email      string `json:"email"`
			}
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid initialization request"})
				return
			}
			user, err := authservice.InitializeSystem(db, authservice.InitializeRequest{Credential: request.Credential, Username: request.Username, Password: request.Password, Email: request.Email})
			if err != nil {
				c.JSON(http.StatusConflict, gin.H{"code": "AUTH_INITIALIZATION_REJECTED", "message": "initialization rejected"})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"code": 201, "message": "initialized", "data": gin.H{"id": user.ID, "username": user.Username}})
		})
		auth.POST("/login", func(c *gin.Context) {
			var req LoginRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "username and password required"})
				return
			}

			user, err := authservice.AuthenticateSingleUser(db, req.Username, req.Password)
			if err != nil {
				allowed, retryAfter, limitErr := limiter.AllowFailure(c.Request.Context(), c.ClientIP(), req.Username)
				if limitErr != nil || !allowed {
					seconds := int(retryAfter.Seconds())
					if seconds < 1 {
						seconds = 1
					}
					c.Header("Retry-After", strconv.Itoa(seconds))
					c.JSON(http.StatusTooManyRequests, gin.H{"code": 429, "message": "too many login attempts"})
					return
				}
				c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "用户名或密码错误"})
				return
			}
			limiter.Reset(c.Request.Context(), c.ClientIP(), req.Username)

			// "记住我"勾选时签发 7 天 token，否则 24 小时。
			tokenTTL := 24 * time.Hour
			if req.RememberMe {
				tokenTTL = 7 * 24 * time.Hour
			}
			token, err := authservice.SignSessionToken(user, jwtSecret, tokenTTL)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to generate token"})
				return
			}

			// Build response
			resp := LoginResponse{
				Token: token,
			}
			resp.User.ID = user.ID
			resp.User.Username = user.Username

			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": resp})
		})
	}
}
