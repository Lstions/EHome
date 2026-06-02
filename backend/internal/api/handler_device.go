package api

import (
	"net/http"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceRoutes sets up device + channel CRUD routes
func registerDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// List devices
	v1.GET("/devices", func(c *gin.Context) {
		var devices []models.Device
		db.Find(&devices)
		c.JSON(http.StatusOK, devices)
	})

	// Create device
	v1.POST("/devices", func(c *gin.Context) {
		var dev models.Device
		if err := c.ShouldBindJSON(&dev); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&dev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, dev)
	})

	// Create channel
	v1.POST("/channels", func(c *gin.Context) {
		var ch models.Channel
		if err := c.ShouldBindJSON(&ch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&ch).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, ch)
	})
}