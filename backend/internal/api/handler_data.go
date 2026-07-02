package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// downsampleUnifiedData uniformly samples data to at most maxPoints rows.
// If maxPoints <= 0 or data already fits, returns data unchanged.
// Always keeps the first and last points.
func downsampleUnifiedData(data []models.UnifiedData, maxPoints int) []models.UnifiedData {
	if maxPoints <= 0 || len(data) <= maxPoints {
		return data
	}
	step := len(data) / maxPoints
	if step < 2 {
		step = 2
	}
	result := make([]models.UnifiedData, 0, maxPoints+2)
	result = append(result, data[0]) // always keep first
	for i := step; i < len(data)-1; i += step {
		result = append(result, data[i])
	}
	result = append(result, data[len(data)-1]) // always keep last
	return result
}

// registerDataRoutes sets up data query routes
func registerDataRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// Get unified data for a device
	// GET /api/v1/devices/:id/sensor-data?limit=100&since=2024-01-01T00:00:00Z
	v1.GET("/devices/:id/sensor-data", func(c *gin.Context) {
		deviceIDStr := c.Param("id")
		deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
			return
		}

		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		if limit <= 0 || limit > 1000 {
			limit = 100
		}

		sinceStr := c.Query("since")
		sensorName := c.Query("sensor")

		query := db.Where("device_id = ?", deviceID)
		if sinceStr != "" {
			if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
				query = query.Where("timestamp >= ?", since)
			}
		}
		if sensorName != "" {
			query = query.Where("sensor_name = ?", sensorName)
		}

		var data []models.UnifiedData
		query.Order("timestamp DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, data)
	})

	// Get latest sensor values for a node (all edge devices)
	// GET /api/v1/nodes/:id/latest
	v1.GET("/nodes/:id/latest", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		// Get all channels for this node
		var channels []models.Channel
		db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Find(&channels)

		type LatestValue struct {
			ChannelID  uint      `json:"channel_id"`
			SensorName string    `json:"sensor_name"`
			Value      float64   `json:"value"`
			Unit       string    `json:"unit"`
			Timestamp  time.Time `json:"timestamp"`
		}

		var results []LatestValue
		for _, ch := range channels {
			var devices []models.EdgeDevice
			db.Where("channel_id = ?", ch.ID).Find(&devices)
			for _, dev := range devices {
				var ud models.UnifiedData
				if err := db.Where("device_id = ?", dev.ID).
					Order("timestamp DESC").First(&ud).Error; err == nil {
					results = append(results, LatestValue{
						ChannelID:  ch.ID,
						SensorName: ud.SensorName,
						Value:      ud.Value,
						Unit:       ud.Unit,
						Timestamp:  ud.Timestamp,
					})
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"node_id": node.NodeID,
			"values":  results,
		})
	})

	// Get time-series data for charting
	// GET /api/v1/devices/:id/history?sensor=wind_direction&hours=24
	v1.GET("/devices/:id/history", func(c *gin.Context) {
		deviceIDStr := c.Param("id")
		deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid device id"})
			return
		}

		sensorName := c.Query("sensor")

		// Support start_time/end_time from frontend
		startStr := c.Query("start_time")
		endStr := c.Query("end_time")

		var startTime, endTime time.Time
		if startStr != "" && endStr != "" {
			var err1, err2 error
			startTime, err1 = time.Parse(time.RFC3339, startStr)
			endTime, err2 = time.Parse(time.RFC3339, endStr)
			if err1 != nil || err2 != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid time format (RFC3339 expected)"})
				return
			}
		} else {
			// Fallback to hours parameter
			hoursStr := c.DefaultQuery("hours", "24")
			hours, _ := strconv.Atoi(hoursStr)
			if hours <= 0 || hours > 720 {
				hours = 24
			}
			startTime = time.Now().Add(-time.Duration(hours) * time.Hour)
			endTime = time.Now()
		}

		q := db.Where("device_id = ? AND timestamp >= ? AND timestamp <= ?", deviceID, startTime, endTime)
		if sensorName != "" {
			q = q.Where("sensor_name = ?", sensorName)
		}

		var data []models.UnifiedData
		q.Order("timestamp ASC").Find(&data)

		// Server-side downsampling: if max_points specified and data exceeds it,
		// uniformly sample to cap response size.
		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))
		data = downsampleUnifiedData(data, maxPoints)

		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": data})
	})

	// Historical unified data for trend charts (Dashboard)
	// GET /api/v1/unified-data/historical?device_pk=1&category=wind_direction&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z
	v1.GET("/unified-data/historical", func(c *gin.Context) {
		devicePKStr := c.Query("device_pk")
		devicePK, err := strconv.ParseUint(devicePKStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device_pk"})
			return
		}

		sensorName := c.Query("category")
		if sensorName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "category parameter required"})
			return
		}

		startStr := c.Query("start_time")
		endStr := c.Query("end_time")
		if startStr == "" || endStr == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start_time and end_time required"})
			return
		}

		startTime, err1 := time.Parse(time.RFC3339, startStr)
		endTime, err2 := time.Parse(time.RFC3339, endStr)
		if err1 != nil || err2 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid time format (RFC3339 expected)"})
			return
		}

		var data []models.UnifiedData
		db.Where("device_id = ? AND sensor_name = ? AND timestamp BETWEEN ? AND ?",
			devicePK, sensorName, startTime, endTime).
			Order("timestamp ASC").
			Find(&data)

		// Server-side downsampling: if max_points specified and data exceeds it,
		// uniformly sample to cap response size.
		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))
		data = downsampleUnifiedData(data, maxPoints)

		c.JSON(http.StatusOK, data)
	})

	// GET /api/v1/devices/:id/failover-logs
	v1.GET("/devices/:id/failover-logs", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": []gin.H{}, "total": 0, "limit": limit})
	})

	// Batch historical query — eliminates N+1 request pattern
	// GET /api/v1/unified-data/historical-batch?device_pk=8&categories=rsoc,temperature_1,cell_voltage_1&start_time=...&end_time=...&max_points=500
	v1.GET("/unified-data/historical-batch", func(c *gin.Context) {
		devicePKStr := c.Query("device_pk")
		devicePK, err := strconv.ParseUint(devicePKStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device_pk"})
			return
		}

		categoriesStr := c.Query("categories")
		if categoriesStr == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "categories parameter required (comma-separated)"})
			return
		}
		categories := strings.Split(categoriesStr, ",")

		startStr := c.Query("start_time")
		endStr := c.Query("end_time")
		if startStr == "" || endStr == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start_time and end_time required"})
			return
		}

		startTime, err1 := time.Parse(time.RFC3339, startStr)
		endTime, err2 := time.Parse(time.RFC3339, endStr)
		if err1 != nil || err2 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid time format (RFC3339 expected)"})
			return
		}

		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))

		// Query all categories in parallel
		type catResult struct {
			Category string                 `json:"category"`
			Data     []models.UnifiedData   `json:"data"`
		}
		results := make([]catResult, len(categories))

		var wg sync.WaitGroup
		for i, cat := range categories {
			wg.Add(1)
			go func(idx int, category string) {
				defer wg.Done()
				var data []models.UnifiedData
				db.Where("device_id = ? AND sensor_name = ? AND timestamp BETWEEN ? AND ?",
					devicePK, category, startTime, endTime).
					Order("timestamp ASC").
					Find(&data)
				data = downsampleUnifiedData(data, maxPoints)
				results[idx] = catResult{Category: category, Data: data}
			}(i, cat)
		}
		wg.Wait()

		c.JSON(http.StatusOK, results)
	})
}
