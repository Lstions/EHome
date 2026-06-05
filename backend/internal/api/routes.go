package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/drivers"
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
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, collectorMgr *collector.Manager, otaMgr *ota.Manager, driverRegistry *drivers.Registry) {
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
		registerDeviceRoutes(v1, db, collectorMgr, driverRegistry)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, collectorMgr)
		registerOTARoutesCompat(v1, db, otaMgr, collectorMgr)
		registerHARoutes(v1)
		registerTerminalRoutes(v1, collectorMgr)
		registerMetricsRoutes(v1, db)

		// v2.2 routes
		registerNodeRoutes(v1, db, collectorMgr)
		registerEdgeDeviceRoutes(v1, db, collectorMgr)

		// Overview + Notification routes
		registerOverviewRoutes(v1, db)
		registerNotificationRoutes(v1, db)

		// User CRUD routes
		registerUserRoutes(v1, db)

		// Data reports (placeholder)
		registerDataReportRoutes(v1, db)

		// Driver compatibility routes (reuse device-configs)
		registerDriverCompatRoutes(v1, db)

		// Data source CRUD routes
		registerDataSourceRoutes(v1.Group("/data-sources"), db)

		// WebSocket endpoint (general)
		v1.GET("/ws", wsHub.HandleWebSocket)
		// WebSocket status endpoint (alias)
		v1.GET("/ws/status", wsHub.HandleWebSocket)

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
