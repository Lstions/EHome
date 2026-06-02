package api

import (
	"net/http"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupRoutes configures all API routes by domain
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, collectorMgr *collector.Manager, otaMgr *ota.Manager) {
	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// API v1
	v1 := r.Group("/api/v1")
	{
		registerCollectorRoutes(v1, db, collectorMgr)
		registerDeviceRoutes(v1, db)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, collectorMgr)
		registerTerminalRoutes(v1, collectorMgr)

		// WebSocket endpoint
		v1.GET("/ws", wsHub.HandleWebSocket)
	}
}
