package api

import (
	"net/http"
	"strconv"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"

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
	registerAuthRoutesWithLimiter(r, db, authservice.NewLoginLimiter(5, 15*time.Minute))
}

func registerAuthRoutesWithLimiter(r *gin.Engine, db *gorm.DB, limiter *authservice.LoginLimiter) {
	auth := r.Group("/api/v1/auth")
	{
		auth.GET("/initialization", func(c *gin.Context) {
			state, err := models.LoadAuthState(db)
			if err != nil {
				// 原先回 {"code":"AUTH_UNAVAILABLE","data":{"state":"unavailable"}}：
				// 该 data 在应用内**不可达** —— 503 会被 axios 当错误抛出，前端拦截器
				// 只读 message（client.ts:80），Login.vue 又是 .catch() 兜底。
				// 故机器可读原因改走 envelope 的 error_code 字段，不再伪造 data。
				ErrorWithCode(c, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "auth state unavailable")
				return
			}
			// Fresh database: persist the uninitialized row so the
			// POST /initialize endpoint can find it.
			if state.State == models.AuthStateUninitialized {
				if err := models.InstallAuthState(db); err != nil {
					ErrorWithCode(c, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "auth state unavailable")
					return
				}
			}
			Success(c, gin.H{"state": state.State})
		})
		auth.POST("/initialize", func(c *gin.Context) {
			var request struct {
				Credential string `json:"credential" binding:"required"`
				Username   string `json:"username" binding:"required"`
				Password   string `json:"password" binding:"required"`
				Email      string `json:"email"`
			}
			if err := c.ShouldBindJSON(&request); err != nil {
				Error(c, http.StatusBadRequest, "invalid initialization request")
				return
			}
			user, err := authservice.InitializeSystem(db, authservice.InitializeRequest{Credential: request.Credential, Username: request.Username, Password: request.Password, Email: request.Email})
			if err != nil {
				ErrorWithCode(c, http.StatusConflict, "AUTH_INITIALIZATION_REJECTED", "initialization rejected")
				return
			}
			SuccessWithCodeMsg(c, http.StatusCreated, gin.H{"id": user.ID, "username": user.Username}, "initialized")
		})
		auth.POST("/login", func(c *gin.Context) {
			var req LoginRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				Error(c, http.StatusBadRequest, "username and password required")
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
					Error(c, http.StatusTooManyRequests, "too many login attempts")
					return
				}
				Error(c, http.StatusUnauthorized, "用户名或密码错误")
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
				Error(c, http.StatusInternalServerError, "failed to generate token")
				return
			}

			// Build response
			resp := LoginResponse{
				Token: token,
			}
			resp.User.ID = user.ID
			resp.User.Username = user.Username

			Success(c, resp)
		})
	}
}
