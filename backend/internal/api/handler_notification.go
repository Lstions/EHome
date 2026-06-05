package api

import (
	"net/http"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerNotificationRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /notifications/unread-count
	v1.GET("/notifications/unread-count", func(c *gin.Context) {
		var count int64
		db.Model(&models.Notification{}).Where("read = ?", false).Count(&count)
		c.JSON(http.StatusOK, gin.H{"count": count})
	})

	// PUT /notifications/:id/read
	v1.PUT("/notifications/:id/read", func(c *gin.Context) {
		id := c.Param("id")
		db.Model(&models.Notification{}).Where("id = ?", id).Update("read", true)
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})

	// POST /notifications/read-all
	v1.POST("/notifications/read-all", func(c *gin.Context) {
		db.Model(&models.Notification{}).Where("read = ?", false).Update("read", true)
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
}
