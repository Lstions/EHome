package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// SetupRoutes configures all API routes by domain
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, collectorMgr *collector.Manager, otaMgr *ota.Manager) {
	// Health check (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus metrics endpoint (no auth required)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Auth routes (no JWT required)
	registerAuthRoutes(r, db)

	// API v1 with JWT auth
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	{
		registerDeviceRoutes(v1, db, collectorMgr)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, collectorMgr)
		registerTerminalRoutes(v1, collectorMgr)
		registerMetricsRoutes(v1, db)

		// v2.2 routes
		registerNodeRoutes(v1, db, collectorMgr)
		registerEdgeDeviceRoutes(v1, db, collectorMgr)

		// Overview + Notification routes
		registerOverviewRoutes(v1, db)
		registerNotificationRoutes(v1, db)

		// Data reports (placeholder)
		registerDataReportRoutes(v1, db)

		// Driver compatibility routes (reuse device-configs)
		registerDriverCompatRoutes(v1, db)

		// WebSocket endpoint (general)
		v1.GET("/ws", wsHub.HandleWebSocket)
		// WebSocket status endpoint (alias)
		v1.GET("/ws/status", wsHub.HandleWebSocket)

		// v2.1 兼容路由 (6 个月后删除)
		// /collectors → /nodes
		v1.GET("/collectors", func(c *gin.Context) {
			var nodes []models.Node
			if err := db.Find(&nodes).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, nodes)
		})
		v1.GET("/collectors/:id", func(c *gin.Context) {
			id := c.Param("id")
			var node models.Node
			if err := db.First(&node, id).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
				return
			}
			c.JSON(http.StatusOK, node)
		})
		v1.POST("/collectors", func(c *gin.Context) {
			var node models.Node
			if err := c.ShouldBindJSON(&node); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if err := db.Create(&node).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeNode, collector.CfgActionCreate, node.ID, node.ID)
			c.JSON(http.StatusCreated, node)
		})
		v1.PUT("/collectors/:id", func(c *gin.Context) {
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
			collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeNode, collector.CfgActionUpdate, node.ID, node.ID)
			c.JSON(http.StatusOK, node)
		})
		v1.DELETE("/collectors/:id", func(c *gin.Context) {
			id := c.Param("id")
			nodeID := parseUintID(id)
			if err := db.Delete(&models.Node{}, id).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeNode, collector.CfgActionDelete, nodeID, nodeID)
			c.JSON(http.StatusOK, gin.H{"message": "deleted"})
		})

		// /devices → /edge-devices
		v1.GET("/devices", func(c *gin.Context) {
			var devices []models.EdgeDevice
			db.Find(&devices)
			c.JSON(http.StatusOK, devices)
		})
		v1.GET("/devices/:id", func(c *gin.Context) {
			id := c.Param("id")
			var d models.EdgeDevice
			if err := db.First(&d, id).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
		})
		v1.POST("/devices", func(c *gin.Context) {
			var dev models.EdgeDevice
			if err := c.ShouldBindJSON(&dev); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if err := db.Create(&dev).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			var ch models.Channel
			if db.First(&ch, dev.ChannelID).Error == nil {
				collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeEdgeDevice, collector.CfgActionCreate, ch.NodeID, dev.ID)
			}
			c.JSON(http.StatusCreated, dev)
		})
		v1.PUT("/devices/:id", func(c *gin.Context) {
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
			var ch models.Channel
			if db.First(&ch, d.ChannelID).Error == nil {
				collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeEdgeDevice, collector.CfgActionUpdate, ch.NodeID, d.ID)
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
		})
		v1.DELETE("/devices/:id", func(c *gin.Context) {
			id := c.Param("id")
			var d models.EdgeDevice
			if err := db.First(&d, id).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
				return
			}
			var ch models.Channel
			hasNode := db.First(&ch, d.ChannelID).Error == nil
			if err := db.Delete(&models.EdgeDevice{}, id).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
				return
			}
			if hasNode {
				collector.EmitConfigChange(collectorMgr.EventBus(), collector.CfgChangeEdgeDevice, collector.CfgActionDelete, ch.NodeID, d.ID)
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
		})
	}

	// Terminal WebSocket endpoint (separate handler with callbacks)
	// This endpoint also requires JWT auth via query param
	termWSHandler := terminal.NewWSHandler(
		wsHub,
		func(channelID uint) ([]terminal.Entry, error) {
			return collectorMgr.TerminalMgr().GetHistory(channelID, 256), nil
		},
		func(deviceID string, channelID uint32, data []byte, readSize uint32) error {
			return collectorMgr.SendWriteCommand(deviceID, channelID, data, readSize)
		},
	)
	r.GET("/api/v1/ws/terminal", JWTAuth(), termWSHandler.HandleTerminalWS)
}
