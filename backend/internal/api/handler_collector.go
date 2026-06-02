package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerCollectorRoutes sets up collector CRUD + data + ping routes
func registerCollectorRoutes(v1 *gin.RouterGroup, db *gorm.DB, collectorMgr *collector.Manager) {
	// List collectors
	v1.GET("/collectors", func(c *gin.Context) {
		var collectors []models.Collector
		if err := db.Find(&collectors).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, collectors)
	})

	// Get collector by device_id
	v1.GET("/collectors/:device_id", func(c *gin.Context) {
		deviceID := c.Param("device_id")
		var col models.Collector
		if err := db.Where("device_id = ?", deviceID).First(&col).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "collector not found"})
			return
		}
		c.JSON(http.StatusOK, col)
	})

	// Create collector
	v1.POST("/collectors", func(c *gin.Context) {
		var col models.Collector
		if err := c.ShouldBindJSON(&col); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&col).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, col)
	})

	// Update collector
	v1.PUT("/collectors/:id", func(c *gin.Context) {
		id := c.Param("id")
		var col models.Collector
		if err := db.First(&col, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "collector not found"})
			return
		}
		if err := c.ShouldBindJSON(&col); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		db.Save(&col)
		c.JSON(http.StatusOK, col)
	})

	// Delete collector
	v1.DELETE("/collectors/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.Collector{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// Get channels for collector
	v1.GET("/collectors/:device_id/channels", func(c *gin.Context) {
		deviceID := c.Param("device_id")
		var col models.Collector
		if err := db.Where("device_id = ?", deviceID).First(&col).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "collector not found"})
			return
		}
		var channels []models.Channel
		db.Where("collector_id = ?", col.ID).Find(&channels)
		c.JSON(http.StatusOK, channels)
	})

	// Get device data
	v1.GET("/collectors/:device_id/data", func(c *gin.Context) {
		deviceID := c.Param("device_id")
		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		var col models.Collector
		if err := db.Where("device_id = ?", deviceID).First(&col).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "collector not found"})
			return
		}
		var data []models.DeviceData
		db.Where("collector_id = ?", col.ID).Order("timestamp DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, data)
	})

	// Ping collector
	v1.POST("/collectors/:device_id/ping", func(c *gin.Context) {
		deviceID := c.Param("device_id")
		if err := collectorMgr.SendPing(deviceID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ping sent"})
	})
}
