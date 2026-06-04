package api

import (
	"errors"
	"net/http"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerEdgeDeviceRoutes sets up v2.2 edge-device CRUD routes (/edge-devices = /devices alias)
// Same handlers, same logic, different path. 6-month compat, then delete v2.1 paths.
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, collectorMgr *collector.Manager) {
	eventBus := collectorMgr.EventBus()

	// List edge devices (v2.2 path for /devices)
	v1.GET("/edge-devices", func(c *gin.Context) {
		var devices []models.EdgeDevice
		db.Find(&devices)
		c.JSON(http.StatusOK, devices)
	})

	// Get single edge device by id (v2.2 path for /devices/:id)
	v1.GET("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Create edge device (v2.2 path for POST /devices)
	v1.POST("/edge-devices", func(c *gin.Context) {
		var dev models.EdgeDevice
		if err := c.ShouldBindJSON(&dev); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&dev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, dev.ChannelID).Error == nil {
			collector.EmitConfigChange(eventBus, collector.CfgChangeDevice, collector.CfgActionCreate, ch.NodeID, dev.ID)
		}
		c.JSON(http.StatusCreated, dev)
	})

	// Update edge device (v2.2 path for PUT /devices/:id)
	v1.PUT("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
			return
		}
		if err := c.ShouldBindJSON(&d); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		d.ID = parseUintID(id)
		if err := db.Save(&d).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			collector.EmitConfigChange(eventBus, collector.CfgChangeDevice, collector.CfgActionUpdate, ch.NodeID, d.ID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Delete edge device (v2.2 path for DELETE /devices/:id)
	v1.DELETE("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Find node before deletion for event emission
		var ch models.Channel
		hasNode := db.First(&ch, d.ChannelID).Error == nil
		if err := db.Delete(&models.EdgeDevice{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// EmitConfigChange
		if hasNode {
			collector.EmitConfigChange(eventBus, collector.CfgChangeDevice, collector.CfgActionDelete, ch.NodeID, d.ID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})
}
