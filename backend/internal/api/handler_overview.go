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
			DeviceID    uint               `json:"device_id"`
			DeviceName  string             `json:"device_name"`
			NodeName    string             `json:"node_name"`
			Data        map[string]float64 `json:"data"`
			CollectedAt string             `json:"collected_at"`
			RawData     string             `json:"raw_data,omitempty"`
			ErrorCode   int                `json:"error_code"`
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
			// 数据层时序化 (v3.4 §3.2.4): 缓存优先, miss 的设备回落原 DISTINCT ON SQL。
			var missed []uint
			cacheByDevice := make(map[uint][]sensorVal)
			for _, did := range deviceIDs {
				// 用 LatestValues（该设备**全部**物理量）而不是 LatestValue（单条）：
				// 后者只返回最后写入的那一条，会让缓存命中时某设备只报 1 个物理量，
				// 而缓存 miss 的回落 SQL 返回该时刻全部行 ⇒ 同一份数据两种形状
				// （实测：设备 7053 回落路径 14 个 vs 缓存路径 1 个）。
				recs := LatestValues(did)
				if len(recs) > 0 {
					for _, rec := range recs {
						cacheByDevice[did] = append(cacheByDevice[did], sensorVal{DeviceID: rec.DeviceID, SensorName: rec.SensorName, Value: rec.Value})
					}
				} else {
					missed = append(missed, did)
				}
			}
			if len(missed) > 0 {
				var fallback []sensorVal
				db.Table("unified_data").
					Select("unified_data.device_id, unified_data.sensor_name, unified_data.value").
					Joins("INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data WHERE device_id IN ? ORDER BY device_id, created_at DESC) latest ON unified_data.device_id = latest.device_id AND unified_data.created_at = latest.created_at", missed).
					Where("unified_data.device_id IN ?", missed).
					Find(&fallback)
				for _, v := range fallback {
					cacheByDevice[v.DeviceID] = append(cacheByDevice[v.DeviceID], v)
				}
			}
			for _, vals := range cacheByDevice {
				allVals = append(allVals, vals...)
			}
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
				DeviceID:    dev.ID,
				DeviceName:  dev.Name,
				NodeName:    dev.Node.Name,
				CollectedAt: dev.LastDataAt.Format("2006-01-02T15:04:05Z"),
				ErrorCode:   dev.ErrorCode,
			}
			if dm, ok := dataByDevice[dev.ID]; ok && len(dm) > 0 {
				entry.Data = dm
			}
			latestData = append(latestData, entry)
		}

		// F14-b: 「今日数据」统计卡读的字段。此前该字段在前后端都不存在，
		// 前端 `|| 0` 兜底把「字段缺失」伪装成一个合法的 0，卡片永远显示 0。
		// 口径 = unified_data 里**今天本地 00:00 起**写入的行数，与卡片文案一致。
		// 注意不能用 time.Now().Truncate(24*time.Hour)：Truncate 按 UTC 纪元取整，
		// 在 UTC+8 会得到本地 08:00，把当天前 8 小时的数据算到"昨天"。
		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		var dataCountToday int64
		db.Model(&models.UnifiedData{}).Where("timestamp >= ?", startOfToday).Count(&dataCountToday)

		result := gin.H{
			"nodes":            gin.H{"total": nodeTotal, "online": nodeOnline, "offline": nodeTotal - nodeOnline},
			"edge_devices":     gin.H{"total": edgeDeviceTotal, "online": edgeDeviceOnline, "offline": edgeDeviceTotal - edgeDeviceOnline},
			"latest_data":      latestData,
			"data_count_today": dataCountToday,
		}

		// Update cache
		overviewCacheMu.Lock()
		overviewCacheData = result
		overviewCacheTime = time.Now()
		overviewCacheMu.Unlock()

		Success(c, result)
	})
}
