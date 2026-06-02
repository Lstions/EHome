package api

import (
	"net/http"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupRoutes configures all API routes by domain
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, collectorMgr *collector.Manager, otaMgr *ota.Manager) {
	// Health check (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Auth routes (no JWT required)
	registerAuthRoutes(r, db)

	// API v1 with JWT auth
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	{
		registerCollectorRoutes(v1, db, collectorMgr)
		registerDeviceRoutes(v1, db)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, collectorMgr)
		registerTerminalRoutes(v1, collectorMgr)

		// WebSocket endpoint (general)
		v1.GET("/ws", wsHub.HandleWebSocket)
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
