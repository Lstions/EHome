package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerNodeRoutes sets up node CRUD routes
func registerNodeRoutes(v1 *gin.RouterGroup, db *gorm.DB, collectorMgr *collector.Manager) {
	eventBus := collectorMgr.EventBus()

	// List nodes (v2.2 path for /collectors)
	v1.GET("/nodes", func(c *gin.Context) {
		var nodes []models.Node
		if err := db.Find(&nodes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, nodes)
	})

	// Get node by node_id (v2.2 path for /collectors/:device_id)
	v1.GET("/nodes/:node_id", func(c *gin.Context) {
		nodeID := c.Param("node_id")
		var node models.Node
		if err := db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusOK, node)
	})

	// Create node (v2.2 path for POST /collectors)
	v1.POST("/nodes", func(c *gin.Context) {
		var node models.Node
		if err := c.ShouldBindJSON(&node); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&node).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		collector.EmitConfigChange(eventBus, collector.CfgChangeNode, collector.CfgActionCreate, node.ID, node.ID)
		c.JSON(http.StatusCreated, node)
	})

	// Update node (v2.2 path for PUT /collectors/:id)
	v1.PUT("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if err := c.ShouldBindJSON(&node); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		db.Save(&node)
		collector.EmitConfigChange(eventBus, collector.CfgChangeNode, collector.CfgActionUpdate, node.ID, node.ID)
		c.JSON(http.StatusOK, node)
	})

	// Delete node (v2.2 path for DELETE /collectors/:id)
	v1.DELETE("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		nodeID := parseUintID(id)
		if err := db.Delete(&models.Node{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		collector.EmitConfigChange(eventBus, collector.CfgChangeNode, collector.CfgActionDelete, nodeID, nodeID)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// Get channels for node (v2.2 path for /collectors/:node_id/channels)
	v1.GET("/nodes/:node_id/channels", func(c *gin.Context) {
		nodeID := c.Param("node_id")
		var node models.Node
		if err := db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var channels []models.Channel
		db.Where("node_id = ?", node.ID).Find(&channels)
		c.JSON(http.StatusOK, channels)
	})

	// Get node data (v2.2 path for /collectors/:node_id/data)
	v1.GET("/nodes/:node_id/data", func(c *gin.Context) {
		nodeID := c.Param("node_id")
		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		var node models.Node
		if err := db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var data []models.DeviceData
		db.Where("collector_id = ?", node.ID).Order("timestamp DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, data)
	})

	// Ping node (v2.2 path for /collectors/:node_id/ping)
	v1.POST("/nodes/:node_id/ping", func(c *gin.Context) {
		nodeID := c.Param("node_id")
		if err := collectorMgr.SendPing(nodeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ping sent"})
	})
}
