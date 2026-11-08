package api

import (
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

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
		var edgeDevicePending int64
		db.Model(&models.Node{}).Count(&nodeTotal)
		db.Model(&models.Node{}).Where("status = ?", "online").Count(&nodeOnline)
		db.Model(&models.EdgeDevice{}).Count(&edgeDeviceTotal)
		db.Model(&models.EdgeDevice{}).Where("status = ?", models.EdgeDeviceStatusActive).Count(&edgeDeviceOnline)
		// pending 必须单独计数（2026-10-03 缺陷 5）：offline 若继续用
		// total - online 推导，新建的、还没采到数据的设备会被算成「离线」，
		// 与"没数据却显示在线"是同一类错误的反方向版本。
		db.Model(&models.EdgeDevice{}).Where("status = ?", models.EdgeDeviceStatusPending).Count(&edgeDevicePending)

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
			// ============ 缓存 miss 不再回落 unified_data（2026-11-08，DROP 之后）============
			//
			// 原实现：对 missed 设备发 DISTINCT ON 查询去 unified_data 捞该时刻全部行。
			// DROP 后那条查询报 42P01（生产实测：启动/首次请求 1 条），且 `Find` 的错误没被检查。
			//
			// 与 handler_edge_device.go 的同类回落一起删。理由相同：
			//	· 缓存由每帧 SetLatestValue 持续更新，正常运行时是热的；
			//	· 启动预热有帧表那一趟；
			//	· miss 只在「刚启动 + 预热未完成」或「设备确实没数据」时发生 —— 返回空是诚实的。
			//
			// ⚠ overview 的 latest_data 汇总在预热未完成窗口里会少几个设备，这是**既定行为**，
			//   不是故障：它随时间自愈（缓存被后续帧填满）。
			_ = missed
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
		// 注意不能用 time.Now().Truncate(24*time.Hour)：Truncate 按 UTC 纪元取整，
		// 在 UTC+8 会得到本地 08:00，把当天前 8 小时的数据算到"昨天"。
		//
		// ==================== D2：口径从「行数」改成「采样帧数」（2026-11-07）====================
		//
		// L1（值不变不入库）自 2026-11-07 起**无条件生效**：同一 (设备, 字段) 的值没变
		// 且未到心跳就不写行。于是 unified_data 的行数不再是「采了多少点」，再数行数，
		// 这张卡会直接掉到 17.7%（真实数据回归实测），用户看到的是「数据丢了」，
		// 而实际上一条都没丢 —— 只是不再重复记录没有变化的同一行。
		//
		// 新口径 = **采样帧数**：一帧 = 同一设备同一 timestamp 的一批物理量
		// （生产端一帧内所有字段用同一个 now 写入，见 consumers_heavy.go）。
		// 它对 L1 天然不敏感：L1 只减少「同一帧里写了几个字段」，不减少「有过几帧」。
		//
		// 代价（生产实测：全表 72.36M 行 / 今日 213 万行）：
		//   count(*)                               今日窗口  235 ms
		//   count(DISTINCT device_id,timestamp)    今日窗口  516 ms
		// /overview 有 30 s 缓存（overviewCacheTTL），轮询不会放大这个开销。
		// 因此这里**只对今日窗口去重**，不做全表去重（全表 20–33 s，见 handler_metrics.go 的 D3 注释）。
		//
		// SQL 用「子查询 + DISTINCT」而不是 PG 的 count(DISTINCT (a,b)) 行构造器：
		// 后者是 PG 专有语法；前者 SQLite/PG 都能执行（SQLite 分支由单测覆盖）。
		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		var dataCountToday int64
		// ============ 数据源改为 telemetry_frame（2026-11-08，unified_data DROP）============
		//
		// 原来数 unified_data 的 (device_id, timestamp) 去重对。DROP 之后那张表不存在，
		// 查询报 42P01 => todayFrames 恒 0 => 卡片显示「今日 0 条」+ 一条 WARN。
		//
		// 换到 telemetry_frame **口径等价**，而且是更原始的定义：
		//	· 一帧 = 同一设备同一 timestamp 的一批物理量（与上面注释的「采样帧数」定义一致）；
		//	· unified_data 的每一帧本来就从帧派生（consumers_heavy 的写入路径）；
		//	· telemetry_frame 是 L2 主写入路径，**不受 L1 收窄影响**（它记录的是帧本身）。
		//
		// ⚠ 数的是**原始帧数**，不是分钟桶数 —— 正好对应卡片要的「采了多少点」。
		if err := db.Model(&models.TelemetryFrame{}).
			Where("ts >= ?", startOfToday).
			Count(&dataCountToday).Error; err != nil {
			logger.Warnf("[overview] count today frames: %v", err)
		}

		result := gin.H{
			"nodes":            gin.H{"total": nodeTotal, "online": nodeOnline, "offline": nodeTotal - nodeOnline},
			"edge_devices":     gin.H{"total": edgeDeviceTotal, "online": edgeDeviceOnline, "offline": edgeDeviceTotal - edgeDeviceOnline - edgeDevicePending, "pending": edgeDevicePending},
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
