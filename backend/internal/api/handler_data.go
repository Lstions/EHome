package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// dataScopeCond renders the §六 data-scope condition for a resolved query
// scope: the logical scope condition (query and cleanup share it, §4.3 —
// the OR fallback branch for NULL-logical rows is permanent, independent
// of any backfill marker), or the plain device_id fallback for instances
// without a logical identity yet (兼容 backfill 前旧数据). The column
// names are identical in unified_data and device_data, so one condition
// serves both tables.
func dataScopeCond(qs *datalifecycle.DataQueryScope) (string, []interface{}) {
	if qs.LogicalID > 0 {
		return qs.Scope.Cond()
	}
	return "device_id = ?", []interface{}{qs.FallbackDeviceID}
}

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

		// 查询协议 (§六): 前端始终传 edge_device_id, 后端解析逻辑身份。
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(deviceID))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		if limit <= 0 || limit > 1000 {
			limit = 100
		}

		sinceStr := c.Query("since")
		sensorName := c.Query("sensor")

		cond, args := dataScopeCond(qs)
		query := db.Where(cond, args...)
		if sinceStr != "" {
			if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
				query = query.Where("timestamp >= ?", since)
			}
		}
		if sensorName != "" {
			query = query.Where("sensor_name = ?", sensorName)
		}

		if qs.DedupNeeded {
			query = datalifecycle.ApplyShapeDedup(db.Session(&gorm.Session{}), query)
		}
		var data []models.UnifiedData
		query.Order("timestamp DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, data)
	})

	// Get latest sensor values for a node (all edge devices) — single query via correlated subquery
	// GET /api/v1/nodes/:id/latest
	v1.GET("/nodes/:id/latest", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		// Get all enabled channel IDs for this node
		var channelIDs []uint
		db.Model(&models.Channel{}).Where("node_id = ? AND enabled = ?", node.NodeID, true).Pluck("id", &channelIDs)

		if len(channelIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"node_id": node.NodeID,
				"values":  []map[string]interface{}{},
			})
			return
		}

		// Single query: correlated subquery to get latest UnifiedData per device
		// Compatible with both PostgreSQL and SQLite (no DISTINCT ON).
		// Replaces the previous N+1 query (channels × devices × data).
		type LatestValue struct {
			ChannelID  uint      `json:"channel_id"`
			SensorName string    `json:"sensor_name"`
			Value      float64   `json:"value"`
			Unit       string    `json:"unit"`
			Timestamp  time.Time `json:"timestamp"`
		}

		var results []LatestValue
		err = db.Raw(`
			SELECT ed.channel_id AS channel_id,
				ud.sensor_name,
				ud.value,
				ud.unit,
				ud.timestamp
			FROM unified_data ud
			JOIN edge_devices ed ON ed.id = ud.device_id
			JOIN (
				SELECT device_id, MAX(timestamp) as max_ts
				FROM unified_data
				WHERE device_id IN (SELECT id FROM edge_devices WHERE channel_id IN ?)
				GROUP BY device_id
			) latest ON latest.device_id = ud.device_id AND latest.max_ts = ud.timestamp
		`, channelIDs).Scan(&results).Error

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
			return
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

		// 查询协议 (§六): resolve → scope 条件 (+ 保形去重)。
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(deviceID))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
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

		cond, args := dataScopeCond(qs)
		q := db.Where(cond+" AND timestamp >= ? AND timestamp <= ?", append(args, startTime, endTime)...)
		if sensorName != "" {
			q = q.Where("sensor_name = ?", sensorName)
		}
		if qs.DedupNeeded {
			q = datalifecycle.ApplyShapeDedup(db.Session(&gorm.Session{}), q)
		}

		var data []models.UnifiedData
		q.Order("timestamp ASC").Find(&data)

		// Server-side downsampling: if max_points specified and data exceeds it,
		// uniformly sample to cap response size.
		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))
		data = downsampleUnifiedData(data, maxPoints)

		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": data})
	})

	// Get available measurement categories for one edge device.
	// GET /api/v1/unified-data/categories?device_pk=1
	v1.GET("/unified-data/categories", func(c *gin.Context) {
		devicePK, err := strconv.ParseUint(c.Query("device_pk"), 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device_pk"})
			return
		}

		// 查询协议 (§六): resolve → scope 条件。类别列表本身无 timestamp
		// 维度, 无需保形去重 (GROUP BY sensor_name 已是聚合)。
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(devicePK))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		type category struct {
			Code string `json:"code"`
			Unit string `json:"unit"`
		}
		var categories []category
		cond, args := dataScopeCond(qs)
		if err := db.Model(&models.UnifiedData{}).
			Select("sensor_name AS code, MAX(unit) AS unit").
			Where(cond, args...).
			Group("sensor_name").
			Order("sensor_name ASC").
			Scan(&categories).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "query categories failed"})
			return
		}
		if categories == nil {
			categories = []category{}
		}
		c.JSON(http.StatusOK, categories)
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

		// 查询协议 (§六): resolve → scope 条件 (+ 保形去重)。
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(devicePK))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

		cond, args := dataScopeCond(qs)
		q := db.Where(cond+" AND sensor_name = ? AND timestamp BETWEEN ? AND ?",
			append(args, sensorName, startTime, endTime)...)
		if qs.DedupNeeded {
			q = datalifecycle.ApplyShapeDedup(db.Session(&gorm.Session{}), q)
		}
		var data []models.UnifiedData
		q.Order("timestamp ASC").Find(&data)

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

		// 查询协议 (§六): resolve → scope 条件 (+ 保形去重)。goroutine
		// 并发段之前解析一次, 各 goroutine 只读复用 (Scope 不可变)。
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(devicePK))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

		cond, args := dataScopeCond(qs)

		// Query all categories in parallel
		type catResult struct {
			Category string               `json:"category"`
			Data     []models.UnifiedData `json:"data"`
		}
		results := make([]catResult, len(categories))

		var wg sync.WaitGroup
		for i, cat := range categories {
			wg.Add(1)
			go func(idx int, category string) {
				defer wg.Done()
				// Use a new Session to avoid Statement sharing between concurrent goroutines.
				// GORM v2's *gorm.DB shares Statement state across chained calls; without
				// Session{}, concurrent db.Where() calls race and corrupt each other's conditions,
				// causing some queries to return 0 rows.
				session := db.Session(&gorm.Session{})
				q := session.Where(cond+" AND sensor_name = ? AND timestamp BETWEEN ? AND ?",
					append(append([]interface{}{}, args...), category, startTime, endTime)...)
				if qs.DedupNeeded {
					q = datalifecycle.ApplyShapeDedup(session.Session(&gorm.Session{}), q)
				}
				var data []models.UnifiedData
				q.Order("timestamp ASC").Find(&data)
				data = downsampleUnifiedData(data, maxPoints)
				results[idx] = catResult{Category: category, Data: data}
			}(i, cat)
		}
		wg.Wait()

		c.JSON(http.StatusOK, results)
	})
}
