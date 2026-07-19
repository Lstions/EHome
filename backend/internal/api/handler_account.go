package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	redisstore "ehome/backend/internal/redis"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type accountResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Enabled  bool   `json:"enabled"`

	LastLoginAt   any `json:"last_login_at,omitempty"`
	InitializedAt any `json:"initialized_at,omitempty"`
}

func registerAccountRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	registerAccountRoutesWithLimiter(v1, db, authservice.NewLoginLimiter(redisstore.Client, 5, 15*time.Minute))
}

func registerAccountRoutesWithLimiter(v1 *gin.RouterGroup, db *gorm.DB, limiter *authservice.LoginLimiter) {
	v1.GET("/account", func(c *gin.Context) {
		user, ok := currentSubject(c, db)
		if !ok {
			return
		}
		Success(c, toAccountResponse(user))
	})

	v1.PATCH("/account", func(c *gin.Context) {
		user, ok := currentSubject(c, db)
		if !ok {
			return
		}
		var request struct {
			Username *string `json:"username"`
			Email    *string `json:"email"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			Error(c, http.StatusBadRequest, "invalid account update")
			return
		}
		updates := map[string]interface{}{}
		if request.Username != nil {
			username := strings.TrimSpace(*request.Username)
			if username == "" {
				Error(c, http.StatusBadRequest, "username is required")
				return
			}
			updates["username"] = username
		}
		if request.Email != nil {
			updates["email"] = strings.TrimSpace(*request.Email)
		}
		if len(updates) > 0 {
			if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
				Error(c, http.StatusConflict, "account update conflict")
				return
			}
			if err := db.First(&user, user.ID).Error; err != nil {
				Error(c, http.StatusInternalServerError, "failed to reload account")
				return
			}
		}
		Success(c, toAccountResponse(user))
	})

	v1.POST("/account/password", func(c *gin.Context) {
		user, ok := currentSubject(c, db)
		if !ok {
			return
		}
		var request struct {
			OldPassword string `json:"old_password" binding:"required"`
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			Error(c, http.StatusBadRequest, "old_password and new_password are required")
			return
		}
		if _, err := authservice.ChangePassword(db, user.ID, request.OldPassword, request.NewPassword); err != nil {
			Error(c, http.StatusForbidden, "password change failed")
			return
		}
		Success(c, gin.H{"reauthenticate": true})
	})

	v1.POST("/account/reauthenticate", func(c *gin.Context) {
		user, ok := currentSubject(c, db)
		if !ok {
			return
		}
		var request struct {
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			ErrorWithCode(c, http.StatusBadRequest, "invalid_reauthentication_request", "password is required")
			return
		}
		authenticated, err := authservice.AuthenticateSingleUser(db, user.Username, request.Password)
		if err != nil {
			allowed, retryAfter, limitErr := limiter.AllowFailure(c.Request.Context(), c.ClientIP(), user.Username)
			if limitErr != nil || !allowed {
				seconds := int(retryAfter.Seconds())
				if seconds < 1 {
					seconds = 1
				}
				c.Header("Retry-After", strconv.Itoa(seconds))
				ErrorWithCode(c, http.StatusTooManyRequests, "reauthentication_rate_limited", "too many reauthentication attempts")
				return
			}
			// A bad password must not revoke the still-valid session. The caller
			// may retry within the bounded limiter window.
			ErrorWithCode(c, http.StatusForbidden, "invalid_reauthentication_credentials", "reauthentication failed")
			return
		}
		limiter.Reset(c.Request.Context(), c.ClientIP(), user.Username)
		Success(c, gin.H{"authenticated_at": authenticated.LastLoginAt})
	})

	revoke := func(c *gin.Context) {
		user, ok := currentSubject(c, db)
		if !ok {
			return
		}
		if _, err := authservice.RevokeAllSessions(db, user.ID, "logout"); err != nil {
			Error(c, http.StatusInternalServerError, "failed to revoke sessions")
			return
		}
		Success(c, gin.H{"reauthenticate": true})
	}
	v1.POST("/account/logout-all", revoke)
	v1.POST("/auth/logout", revoke)
}

func currentSubject(c *gin.Context, db *gorm.DB) (models.User, bool) {
	value, exists := c.Get("subject_id")
	userID, ok := value.(uint)
	if !exists || !ok || userID == 0 {
		Error(c, http.StatusUnauthorized, "invalid authenticated subject")
		return models.User{}, false
	}
	var user models.User
	if err := db.Where("id = ? AND subject_key = ? AND retired_at IS NULL", userID, models.SystemAdminSubjectKey).First(&user).Error; err != nil {
		Error(c, http.StatusUnauthorized, "invalid authenticated subject")
		return models.User{}, false
	}
	return user, true
}

func toAccountResponse(user models.User) accountResponse {
	return accountResponse{
		ID:       user.ID,
		Username: user.Username,
		Email:    user.Email,
		Enabled:  user.Enabled,

		LastLoginAt:   user.LastLoginAt,
		InitializedAt: user.InitializedAt,
	}
}
