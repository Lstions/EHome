package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// LoginRequest represents the login request body
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse represents the login response
type LoginResponse struct {
	Token string `json:"token"`
	User  struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

// registerAuthRoutes sets up authentication routes (no JWT required)
func registerAuthRoutes(r *gin.Engine, db *gorm.DB) {
	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/login", func(c *gin.Context) {
			var req LoginRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
				return
			}

			// Find user
			var user models.User
			if err := db.Where("username = ?", req.Username).First(&user).Error; err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
				return
			}

			// Verify password
			if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
				return
			}

			// Generate JWT token
			token, err := GenerateToken(user.ID, user.Role)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
				return
			}

			// Build response
			resp := LoginResponse{
				Token: token,
			}
			resp.User.ID = user.ID
			resp.User.Username = user.Username
			resp.User.Role = user.Role

			c.JSON(http.StatusOK, resp)
		})
		auth.POST("/logout", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "logged out"})
		})
	}
}

// SeedAdminUser creates the default admin user if not exists
func SeedAdminUser(db *gorm.DB) error {
	var count int64
	db.Model(&models.User{}).Count(&count)
	if count > 0 {
		return nil // Users already exist
	}

	// Hash the default password
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	admin := models.User{
		Username:     "admin",
		PasswordHash: string(passwordHash),
		Role:         "admin",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := db.Create(&admin).Error; err != nil {
		return err
	}

	return nil
}
