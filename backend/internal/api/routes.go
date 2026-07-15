package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// SetupRoutes configures all API routes by domain
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, nodeMgr *nodemgr.Manager, otaMgr *ota.Manager, driverRegistry *drivers.Registry) {
	// Global HTTP metrics middleware
	r.Use(func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, path).Inc()
		c.Next()
	})

	// Health check (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Auth routes (no JWT required)
	registerAuthRoutes(r, db)

	// Firmware download — no auth (ESP32 fetches without JWT)
	RegisterFirmwareDownload(r)

	// API v1: every route in this group requires the authoritative single
	// subject session. Public endpoints are registered explicitly above.
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuthWithDB(db))
	{
		v1.GET("/metrics/prometheus", gin.WrapH(promhttp.Handler()))
		registerAccountRoutes(v1, db)
		registerDeviceRoutes(v1, db, nodeMgr, driverRegistry)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, nodeMgr)
		registerOTARoutesCompat(v1, db, otaMgr, nodeMgr)
		registerHARoutes(v1)
		registerTerminalRoutes(v1, nodeMgr)
		registerMetricsRoutes(v1, db)

		// v2.2 routes
		registerNodeRoutes(v1, db, nodeMgr)
		registerEdgeDeviceRoutes(v1, db, nodeMgr)
		registerDriverCommandRoutes(v1, db, nodeMgr)

		// v3.0: GPIO/PWM peripheral control routes
		registerPeriphRoutes(v1, db, nodeMgr)

		// Overview + Notification routes
		registerOverviewRoutes(v1, db)
		registerNotificationRoutes(v1, db)

		// Removed multi-user API compatibility surface (authenticated 410).
		registerLegacyUserRoutes(v1)

		// Data reports (placeholder)
		registerDataReportRoutes(v1, db)

		// Driver compatibility routes (reuse device-configs)
		registerDriverCompatRoutes(v1, db)

		// Data source CRUD routes
		registerDataSourceRoutes(v1.Group("/data-sources"), db)

		// Vendor + DeviceModel + DeviceCategory CRUD
		registerVendorRoutes(v1, db)

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
			return nodeMgr.TerminalMgr().GetHistory(channelID, 256), nil
		},
		func(deviceID string, channelID uint32, data []byte, readSize uint32) error {
			return nodeMgr.SendWriteCommand(deviceID, channelID, data, readSize)
		},
	)
	r.GET("/api/v1/ws/terminal", JWTAuthWithDB(db), termWSHandler.HandleTerminalWS)
}
