package api

import (
	"net/http"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerNotificationRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	v1.GET("/notifications/unread-count", func(c *gin.Context) {
		var count int64
		db.Model(&models.Notification{}).Where("read = ?", false).Count(&count)
		c.JSON(http.StatusOK, gin.H{"count": count})
	})
}
