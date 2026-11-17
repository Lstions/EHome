package api

import (
	"net/http"
	"strings"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"

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
	registerAccountRoutesWithLimiter(v1, db, authservice.NewLoginLimiter(5, 15*time.Minute))
}

// limiter 保留在签名里只为不改动调用点：2026-11-15 删除 /account/reauthenticate 后，
// 账户路由已不需要限流器（登录限流在 registerAuthRoutesWithLimiter 里）。
func registerAccountRoutesWithLimiter(v1 *gin.RouterGroup, db *gorm.DB, _ *authservice.LoginLimiter) {
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

	// 2026-11-15 用户裁定：危险动作只保留「二次点击确认」，整程序不再要求用户
	// 重新输入密码。原 POST /account/reauthenticate 是"近认证门"的唯一刷新入口，
	// 已随该门一并删除；保留它只会让客户端以为"还有密码环节"。回归判据见
	// handler_account_test.go 的 TestAccountReauthenticateEndpointIsGone。

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
