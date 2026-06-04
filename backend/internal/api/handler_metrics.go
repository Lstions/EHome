package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// MetricsResponse is the unified envelope for metrics endpoints
type MetricsResponse struct {
	HTTP struct {
		RequestsTotal    int64 `json:"requests_total"`
		RequestsInFlight int64 `json:"requests_in_flight"`
	} `json:"http"`
	MQTT struct {
		MessagesReceived int64 `json:"messages_received"`
		MessagesSent     int64 `json:"messages_sent"`
		ConnectionErrors int64 `json:"connection_errors"`
	} `json:"mqtt"`
	Device struct {
		Online  int64 `json:"online"`
		Offline int64 `json:"offline"`
	} `json:"device"`
	Collector struct {
		Online  int64 `json:"online"`
		Offline int64 `json:"offline"`
	} `json:"collector"`
	Data struct {
		PointsCollected int64 `json:"points_collected"`
		PointsStored    int64 `json:"points_stored"`
	} `json:"data"`
	OTA struct {
		UpgradesTotal int64 `json:"upgrades_total"`
	} `json:"ota"`
	WebSocket struct {
		ConnectionsActive int64 `json:"connections_active"`
		MessagesTotal     int64 `json:"messages_total"`
	} `json:"websocket"`
	Timestamp int64 `json:"timestamp"`
}

// registerMetricsRoutes wires /api/v1/metrics endpoints
func registerMetricsRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	v1.GET("/metrics", getMetricsHandler(db))
	v1.GET("/metrics/summary", getMetricsSummaryHandler(db))
}

func getMetricsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Prometheus exposition format
		c.String(http.StatusOK, "# metrics endpoint - use /metrics/summary for JSON\n")
	}
}

func getMetricsSummaryHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp := MetricsResponse{}

		// DB counts
		var devOnline, devOffline int64
		db.Model(&models.Device{}).Where("status = ?", "active").Count(&devOnline)
		db.Model(&models.Device{}).Where("status <> ?", "active").Count(&devOffline)
		resp.Device.Online = devOnline
		resp.Device.Offline = devOffline

		var colOnline, colOffline int64
		db.Model(&models.Collector{}).Where("status = ?", "online").Count(&colOnline)
		db.Model(&models.Collector{}).Where("status = ?", "offline").Count(&colOffline)
		resp.Collector.Online = colOnline
		resp.Collector.Offline = colOffline

		var points int64
		db.Model(&models.UnifiedData{}).Count(&points)
		resp.Data.PointsStored = points

		var otaTotal int64
		db.Model(&models.OTATask{}).Count(&otaTotal)
		resp.OTA.UpgradesTotal = otaTotal

		resp.Timestamp = time.Now().UnixMilli()
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "ok",
			"data":    resp,
		})
		_ = metrics.CollectorsOnline // suppress unused
	}
}
