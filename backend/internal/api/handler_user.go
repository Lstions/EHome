package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func registerUserRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	u := v1.Group("/users")
	// GET /users
	u.GET("", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		keyword := c.Query("keyword")
		var users []models.User
		var total int64
		q := db.Model(&models.User{})
		if keyword != "" {
			q = q.Where("username ILIKE ? OR email ILIKE ?", "%"+keyword+"%", "%"+keyword+"%")
		}
		q.Count(&total)
		q.Offset((page - 1) * pageSize).Limit(pageSize).Find(&users)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"items": users, "total": total, "page": page, "page_size": pageSize}})
	})
	// GET /users/:id
	u.GET("/:id", func(c *gin.Context) {
		var user models.User
		if err := db.First(&user, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": user})
	})
	// POST /users
	u.POST("", RequireRole("admin"), func(c *gin.Context) {
		var req struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
			Email    string `json:"email"`
			Role     string `json:"role"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		user := models.User{Username: req.Username, PasswordHash: string(hash), Email: req.Email, Role: req.Role}
		if err := db.Create(&user).Error; err != nil {
			c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "username exists"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"code": 201, "data": user})
	})
	// PUT /users/:id
	u.PUT("/:id", RequireRole("admin"), func(c *gin.Context) {
		var user models.User
		if err := db.First(&user, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		var req struct {
			Username *string `json:"username"`
			Email    *string `json:"email"`
			Role     *string `json:"role"`
			Enabled  *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400})
			return
		}
		updates := map[string]interface{}{}
		if req.Username != nil {
			updates["username"] = *req.Username
		}
		if req.Email != nil {
			updates["email"] = *req.Email
		}
		if req.Role != nil {
			updates["role"] = *req.Role
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		db.Model(&user).Updates(updates)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": user})
	})
	// DELETE /users/:id
	u.DELETE("/:id", RequireRole("admin"), func(c *gin.Context) {
		db.Delete(&models.User{}, c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
	// POST /users/me/change-password
	u.POST("/me/change-password", func(c *gin.Context) {
		userID, _ := c.Get("user_id")
		var req struct {
			OldPassword string `json:"old_password" binding:"required"`
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400})
			return
		}
		var user models.User
		if err := db.First(&user, userID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "wrong password"})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		db.Model(&user).Update("password_hash", string(hash))
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
	// POST /users/:id/reset-password
	u.POST("/:id/reset-password", RequireRole("admin"), func(c *gin.Context) {
		var req struct {
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		db.Model(&models.User{}).Where("id = ?", c.Param("id")).Update("password_hash", string(hash))
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
}
