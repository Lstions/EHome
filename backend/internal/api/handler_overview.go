package api

import (
	"sync"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// overviewCache caches the /overview response for 30s (C2 fix)
var (
	overviewCacheMu   sync.RWMutex
	overviewCacheData interface{}
	overviewCacheTime time.Time
	overviewCacheTTL  = 30 * time.Second
)

func registerOverviewRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	v1.GET("/overview", func(c *gin.Context) {
		// Check cache first (C2 fix: 30s TTL)
		overviewCacheMu.RLock()
		if overviewCacheData != nil && time.Since(overviewCacheTime) < overviewCacheTTL {
			cached := overviewCacheData
			overviewCacheMu.RUnlock()
			Success(c, cached)
			return
		}
		overviewCacheMu.RUnlock()

		var nodeTotal int64
		var nodeOnline int64
		var edgeDeviceTotal int64
		var edgeDeviceOnline int64
		db.Model(&models.Node{}).Count(&nodeTotal)
		db.Model(&models.Node{}).Where("status = ?", "online").Count(&nodeOnline)
		db.Model(&models.EdgeDevice{}).Count(&edgeDeviceTotal)
		db.Model(&models.EdgeDevice{}).Where("status = ?", "active").Count(&edgeDeviceOnline)

		// Build latest_data from edge devices + unified_data (C2 fix: batch query)
		type latestEntry struct {
			DeviceID      uint               `json:"device_id"`
			DeviceName    string             `json:"device_name"`
			NodeName      string             `json:"node_name"`
			CollectorName string             `json:"collector_name"`
			Data          map[string]float64 `json:"data"`
			CollectedAt   string             `json:"collected_at"`
			RawData       string             `json:"raw_data,omitempty"`
		}

		// Only load devices with data
		var devices []models.EdgeDevice
		db.Preload("Node").Where("last_data_at IS NOT NULL").Find(&devices)

		// Batch query: get latest sensor values per device using DISTINCT ON
		type sensorVal struct {
			DeviceID   uint    `json:"device_id"`
			SensorName string  `json:"sensor_name"`
			Value      float64 `json:"value"`
		}
		deviceIDs := make([]uint, 0, len(devices))
		for _, dev := range devices {
			deviceIDs = append(deviceIDs, dev.ID)
		}
		var allVals []sensorVal
		if len(deviceIDs) > 0 {
			db.Table("unified_data").
				Select("unified_data.device_id, unified_data.sensor_name, unified_data.value").
				Joins("INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data WHERE device_id IN ? ORDER BY device_id, created_at DESC) latest ON unified_data.device_id = latest.device_id AND unified_data.created_at = latest.created_at", deviceIDs).
				Where("unified_data.device_id IN ?", deviceIDs).
				Find(&allVals)
		}

		// Group by device ID
		dataByDevice := make(map[uint]map[string]float64, len(devices))
		for _, v := range allVals {
			if dataByDevice[v.DeviceID] == nil {
				dataByDevice[v.DeviceID] = make(map[string]float64)
			}
			dataByDevice[v.DeviceID][v.SensorName] = v.Value
		}

		latestData := make([]latestEntry, 0, len(devices))
		for _, dev := range devices {
			entry := latestEntry{
				DeviceID:      dev.ID,
				DeviceName:    dev.Name,
				NodeName:      dev.Node.Name,
				CollectorName: dev.Node.Name, // legacy alias — same as node_name for backward compat
				CollectedAt:   dev.LastDataAt.Format("2006-01-02T15:04:05Z"),
			}
			if dm, ok := dataByDevice[dev.ID]; ok && len(dm) > 0 {
				entry.Data = dm
			}
			latestData = append(latestData, entry)
		}

		result := gin.H{
			"nodes":        gin.H{"total": nodeTotal, "online": nodeOnline, "offline": nodeTotal - nodeOnline},
			"edge_devices": gin.H{"total": edgeDeviceTotal, "online": edgeDeviceOnline, "offline": edgeDeviceTotal - edgeDeviceOnline},
			"latest_data":  latestData,
		}

		// Update cache
		overviewCacheMu.Lock()
		overviewCacheData = result
		overviewCacheTime = time.Now()
		overviewCacheMu.Unlock()

		Success(c, result)
	})
}
