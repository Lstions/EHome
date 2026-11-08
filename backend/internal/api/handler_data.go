package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

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

// registerDataRoutes sets up data query routes.
//
// 数据读取的最终形态 (.contract/logical-device-removal.md §5):
//
//	所有数据读取一律 `WHERE device_id = ?` —— 边缘设备维度, 无 scope 解析、
//	无 OR 兜底、无 UNION 拼装、无保形去重。
//
// 为什么可以这么简单: 逻辑设备概念层已整体移除, `unified_data.device_id` /
// `device_data.device_id` 的语义回归单一 —— **就是 EdgeDevice.ID**。曾经那套
// `ResolveDataQueryScope → (logical_device_id IN (...) OR (logical_device_id IS
// NULL AND device_id IN (...)))` 的解析链, 存在的唯一理由是「一次查询要跨
// 多台物理设备取数」(历史合并后的逻辑身份)。这个需求本身被产品负责人否掉了,
// 于是解析链没有对象; 而它带来的代价是实打实的: OR 条件把索引前导列降级成
// Filter, 边缘设备数据端点曾因它退化到秒级 (docs/分析/逻辑设备-业务逻辑与需求
// 偏差评估-2026-10-23.md 的运行时证据)。
//
// 软删设备的可见性 (契约 §5): 数据端点**不**校验设备存活 —— 已软删设备的
// 历史数据仍然可查 (设备行用 Unscoped 语义, 只有配置端点才 404)。这也是
// 「换节点后历史不丢」的一部分: 设备换绑/软删都不该让数据变得查不到。
func registerDataRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// Get unified data for a device
	// GET /api/v1/devices/:id/sensor-data?limit=100&since=2024-01-01T00:00:00Z
	v1.GET("/devices/:id/sensor-data", func(c *gin.Context) {
		deviceIDStr := c.Param("id")
		deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid device id")
			return
		}

		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		if limit <= 0 || limit > 1000 {
			limit = 100
		}

		sinceStr := c.Query("since")
		sensorName := c.Query("sensor")

		query := db.Where("device_id = ?", uint(deviceID))
		if sinceStr != "" {
			if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
				query = query.Where("timestamp >= ?", since)
			}
		}
		if sensorName != "" {
			query = query.Where("sensor_name = ?", sensorName)
		}

		// ============ 数据源 1：帧表（task-27 #16，2026-11-08）============
		//
		// 演练实测：unified_data 全 DETACH 后本接口 .data=0（基线 50）。
		//
		// 这条接口的语义是「**最近 limit 条采样**」，没有显式时间窗 —— 而帧表解码
		// 必须给窗口。所以用**自适应窗口**：从短到长依次尝试，够 limit 条就停。
		//
		// 为什么从 1h 起：limit 上限 1000，生产活跃设备 1Hz ⇒ 1000 条约 17 分钟，
		// 1h 足够；慢设备（5 分钟一报）则需要扩到 24h。逐级放大**只在需要时付代价**。
		//
		// since 参数给定时直接用它作为起点（尊重调用方的意图）；窗口超过帧表上限
		// （8 天）时 frameHistoryRows 会返回 ok=false，自动回落下面的原查询。
		end := time.Now().UTC()
		var startCandidates []time.Time
		if sinceStr != "" {
			if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
				startCandidates = []time.Time{since}
			}
		}
		if startCandidates == nil {
			startCandidates = []time.Time{
				end.Add(-1 * time.Hour),
				end.Add(-6 * time.Hour),
				end.Add(-24 * time.Hour),
			}
		}
		// ── 内存护栏跟着**自适应窗口**走，而不是固定探测最长窗口 ──
		//
		// 第一版在循环外固定探测 24h：结果是 35+ 字段的设备上，连 ?limit=50 都会被 413 拒，
		// 而它实际只需要解码 1h（1h 内就有 50 条）。**护栏的范围必须与实际解码范围一致。**
		//
		// 语义（Lead 2026-11-08 裁决 A）：
		//	· 某个窗口超限 => **跳过它**，用上一个更小窗口已有/能拿到的数据；
		//	· 只有**最小的窗口也超限**才 413。
		//
		// 理由：本接口契约是「最近 limit 条」，不是「必须覆盖 24h」。
		// 拿不到 50 条就返回 30 条，比 413 有用得多。
		// 只有**最小窗口**就超限时才有 blockVerdict（更大窗口必然也超限，预算随窗口单调）。
		var blockVerdict historyBudgetVerdict
		for i, start := range startCandidates {
			if v := checkHistoryFrameBudget(c.Request.Context(), db, uint(deviceID), sensorName, start, end); v.Exceeded {
				if i == 0 {
					// 最小窗口就超限：没有更小的可退，只能 413。
					blockVerdict = v
				}
				continue // 该窗口太大，跳过（不是整个请求失败）
			}
			rows, ok := frameHistoryRows(c.Request.Context(), db, uint(deviceID), sensorName, start, end)
			if !ok {
				continue
			}
			if len(rows) == 0 {
				continue // 该窗口没数据：扩窗口再试
			}
			// rows 是 timestamp 升序；本接口要 DESC + 只取前 limit 条。
			if len(rows) > limit {
				rows = rows[len(rows)-limit:]
			}
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
			Success(c, rows)
			return
		}

		if blockVerdict.Exceeded {
			Error(c, http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"连最小窗口（%s）都超过单次上限：%d 帧 x %d 字段 = %d 行，超过行上限 %d；请传 since 缩小时间窗",
				startCandidates[0].Format(time.RFC3339), blockVerdict.Frames, blockVerdict.Sensors,
				blockVerdict.Frames*int64(blockVerdict.Sensors), maxHistoryRowsPerRequest))
			return
		}

		// ============ 数据源 2：原裸 SQL（unified_data）============
		var data []models.UnifiedData
		query.Order("timestamp DESC").Limit(limit).Find(&data)
		if data == nil {
			data = []models.UnifiedData{}
		}
		Success(c, data)
	})

	// Get latest sensor values for a node (all edge devices) — single query via correlated subquery
	// GET /api/v1/nodes/:id/latest
	v1.GET("/nodes/:id/latest", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		// Get all enabled channel IDs for this node
		var channelIDs []uint
		db.Model(&models.Channel{}).Where("node_id = ? AND enabled = ?", node.NodeID, true).Pluck("id", &channelIDs)

		if len(channelIDs) == 0 {
			Success(c, gin.H{
				"node_id": node.NodeID,
				"values":  []map[string]interface{}{},
			})
			return
		}

		// Single query: correlated subquery to get latest UnifiedData per device
		// Compatible with both PostgreSQL and SQLite (no DISTINCT ON).
		// Replaces the previous N+1 query (channels × devices × data).
		type LatestValue = nodeLatestValue

		// 首选：读每设备最新值缓存（步骤 1，task-24）。实现与理由见 handler_node_latest_cache.go，
		// 其中包含一个刻意的行为变更（软删设备不再出现）的说明。
		if values, ok := nodeLatestValuesFromCache(db, channelIDs); ok {
			Success(c, gin.H{"node_id": node.NodeID, "values": values})
			return
		}

		// ============ 契约变更（2026-11-08，unified_data 硬切换）============
		//
		// **原语义**：缓存 miss 时回落查 unified_data，所以「有行就有值」。
		// **新语义**：**只读缓存**，缓存没有就返回空数组。
		//
		// 为什么删回落：停写后 unified_data 冻结在 T_stop，回落会返回
		// 「看起来正常但停在那一刻」的过期值 —— 空用户立刻发现，过期值用户以为正常。
		//
		// **影响窗口**：刚启动 + 预热未完成时返回 []。预热完成后与旧行为一致。
		// （正常运行时缓存由 SensorParserConsumer 每帧 SetLatestValue 持续更新；
		//   启动预热由 main.go 的 WarmupLatestValuesFromFrames 从帧表回填。）
		//
		// 对外契约记录见 docs/规范/unified_data硬切换-读路径契约变更-2026-11-08.md。
		// **形状保证**：始终 []，不是 null（见 handler_envelope_contract_test.go 的
		// TestEnvelopeContract_NodeLatestEmptyDataSerializesAsArray）。
		//
		// 原回落 SQL（已删）：
		//
		// 原来的回落是这条裸 SQL：
		//
		//	SELECT ... FROM unified_data ud JOIN edge_devices ed ...
		//	JOIN (SELECT device_id, MAX(timestamp) FROM unified_data GROUP BY device_id) latest ...
		//
		// 它现在是一条**活着的过期数据源**，不是死代码：停写 = 不再 INSERT，不是 TRUNCATE，
		// unified_data 里还留着冻结时刻之前的 45 天数据。于是缓存 miss 时会返回
		// **「看起来正常但停在停写那一刻」的值** —— 这比返回空更危险：
		// 空用户立刻发现，过期值用户以为正常。
		//
		// 为什么可以直接返回空而不会挂：
		//	· 正常运行时缓存由 SensorParserConsumer 每帧 SetLatestValue 持续更新，miss 只在
		//	  「刚启动且帧表预热还没跑完」或「该设备确实没有数据」两种情况下发生；
		//	· 启动预热有**帧表那一趟**兜底（main.go 的 WarmupLatestValuesFromFrames），
		//	  它覆盖所有在帧表里有帧的设备；
		//	· 形状与「无启用通道」那条分支一致：始终返回 `[]` 而不是 `null`
		//	  （nil 切片会被序列化成 null，前端按 data.values.length 取值会抛 TypeError）。
		Success(c, gin.H{
			"node_id": node.NodeID,
			"values":  []LatestValue{},
		})
	})

	// Get time-series data for charting
	// GET /api/v1/devices/:id/history?sensor=wind_direction&hours=24
	v1.GET("/devices/:id/history", func(c *gin.Context) {
		deviceIDStr := c.Param("id")
		deviceID, err := strconv.ParseUint(deviceIDStr, 10, 32)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid device id")
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
				Error(c, http.StatusBadRequest, "invalid time format (RFC3339 expected)")
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

		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))

		// ── 内存护栏（P0）：帧数超限直接 413，不解码 ──
		// 降采样发生在解码之后，所以 max_points **不能**免除这个检查。
		if v := checkHistoryFrameBudget(c.Request.Context(), db, uint(deviceID), sensorName, startTime, endTime); v.Exceeded {
			Error(c, http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"窗口内数据量超过单次上限：%d 帧 x %d 字段 = %d 行，超过行上限 %d；请缩小时间窗或改用分钟粒度路径",
				v.Frames, v.Sensors, v.Frames*int64(v.Sensors), maxHistoryRowsPerRequest))
			return
		}

		// ============ 数据源 1：帧表原始解码（task-26，2026-11-08）============
		//
		// 演练实测：unified_data 6 分区全 DETACH 后，本接口 1h/24h/168h 全部返回 0 行。
		// 帧表覆盖 10-02 至今且是长期存储，所以先在帧表里找。
		//
		// 这是**数据源选择**，不是「要不要聚合」的闸门 —— 两者正交：
		//	· 现有闸门 shouldAggregateHistory 判的是「聚合 or 原始」；
		//	· 这里判的是「从帧表读 or 从 unified_data 读」。
		// 混在一起是错误（我第一次就是这么改的，3 个既有用例红）。
		//
		// 窗口 > frameDecodeMaxWindow 时 ok=false ⇒ 回落下面的裸 SQL。
		//
		// ⚠ frameDecodeMaxWindow 有三个值，不要只记一个：
		//     代码默认 1h / 生产 6h（部署脚本设 env）/ 测试 1h。
		//   旧注释写「默认 6h」是把生产值当成了默认值 —— 本行原样是「（默认 6h）」，已删。
		//   三个值的完整对照见 device_history_frames.go 的分段解码注释。
		//
		// 所以 hours=168 目前仍走裸 SQL，单独处理（见 task-26 的后续项）。
		// max_points 的抽样**下推到组装阶段**（2026-11-08）：同样一次解码，
		// 但只物化被抽中的那 ~maxPoints 行，而不是先把 2.46M 行造出来再扔掉 99.996%。
		// 输出与 downsampleUnifiedData 逐点一致（同一套下标规则，见 device_history_downsample.go）。
		if rows, ok := frameHistoryRowsDownsampled(c.Request.Context(), db, uint(deviceID), sensorName, startTime, endTime, maxPoints); ok {
			Success(c, rows)
			return
		}

		// ============ 帧表路径不可用：返回空，**不再回落 unified_data**（2026-11-08）============
		//
		// 原回落是这条裸 SQL：
		//
		//	q := db.Where(`device_id = ? AND timestamp >= ? AND timestamp <= ?`, ...).Find(&data)
		//
		// 它现在是一条**活着的过期数据源**：停写 ≠ TRUNCATE，unified_data 里仍有冻结时刻
		// 之前的 45 天数据。回落会返回「看起来正常但停在停写那一刻」的历史 ——
		// 对**历史图表**尤其危险：用户以为看到的是最新的，实际是一段被冻结的时间序列。
		//
		// 什么情况下会走到这里（帧表路径 ok=false）：
		//	· 窗口超过 8 天（maxFrameHistoryWindow）
		//	· 字典未就绪（极早期启动窗口）
		//	· 该窗口一帧都没有（设备当时未上线）
		//
		// 这三种情况下返回空数组是**诚实**的：我们确实没有这个窗口的数据。
		// 而回落 unified_data 会让「没有数据」与「有过期数据」看起来一样。
		//
		// ============ 但当「空」的原因是**请求被取消**时，不能返回 200 ============
		//
		// 上一轮删回落时我漏了这条：取消与「确实没有数据」必须区分开。
		//	· 「窗口内没数据」  => 200 + []    （诚实的结果）
		//	· 「客户端已断开/超时」 => 500        （这是错误，不是空结果）
		//
		// 混为一谈的后果：调用方看到 200 + 空数组，会以为「这段时间确实没数据」，
		// 从而把一次失败当成正常结果继续用它 —— 这类错误最难发现，因为它没有症状。
		//
		// 由 TestHeavyQueries_CanceledRequest_Returns500AndReachesDB 钉住
		// （它同时要求取消真的传播到 DB 层，这一点由上面的 budget 探测查询满足）。
		if ctxErr := c.Request.Context().Err(); ctxErr != nil {
			logger.Warnf("[devices-history] request canceled before data source returned: device_id=%d err=%v", deviceID, ctxErr)
			Error(c, http.StatusInternalServerError, "failed to query device history")
			return
		}

		// 形状保证：返回 `[]` 而不是 `null`（前端按 .length 消费，null 会抛 TypeError）。
		Success(c, []models.UnifiedData{})
	})

	// Get available measurement categories for one edge device.
	// GET /api/v1/unified-data/categories?device_pk=1
	v1.GET("/unified-data/categories", func(c *gin.Context) {
		devicePK, err := strconv.ParseUint(c.Query("device_pk"), 10, 32)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid device_pk")
			return
		}

		// 类别列表本身无 timestamp 维度 (GROUP BY sensor_name 已是聚合),
		// 因此无需降采样。
		type category struct {
			Code string `json:"code"`
			Unit string `json:"unit"`
		}
		// ============ 数据源 1：sensor_field_obs（task-27 #15，2026-11-08）============
		//
		// 演练实测：unified_data 6 分区全 DETACH 后本接口返回 .data=0（基线 29）。
		// 而「有哪些类别（字段）+ 各自单位」本来就是**维度信息**，不是时序数据 ——
		// 它应该来自维度表，而不是扫一张时序表。sensor_field_obs 正是这样的表
		// （生产 71 行，主键 (device_id, sensor_name)）。
		//
		// 每 (device, sensor) 只有一行，所以不需要 GROUP BY；仍保留 MAX(unit) 是为了
		// 防御性处理「同名字段多行」的脏数据（与原 SQL 的聚合口径一致）。
		var categories []category
		if err := db.Raw(
			"SELECT sensor_name AS code, MAX(unit) AS unit FROM sensor_field_obs "+
				"WHERE device_id = ? GROUP BY sensor_name ORDER BY sensor_name ASC",
			uint(devicePK)).Scan(&categories).Error; err != nil {
			// 投影表可能不存在（精简部署 / 未迁移）：不是错误，走下面的回落。
			categories = nil
		}

		// ============ 数据源 2：unified_data（回落）============
		//
		// 何时会走到：投影未开启 / 该设备尚无投影行（新设备刚上线，投影每 60s 才刷新）。
		// 此时沿用原 SQL，行为与改造前完全一致。
		if len(categories) == 0 {
			if err := db.Model(&models.UnifiedData{}).
				Select("sensor_name AS code, MAX(unit) AS unit").
				Where("device_id = ?", uint(devicePK)).
				Group("sensor_name").
				Order("sensor_name ASC").
				Scan(&categories).Error; err != nil {
				Error(c, http.StatusInternalServerError, "query categories failed")
				return
			}
		}
		if categories == nil {
			categories = []category{}
		}
		Success(c, categories)
	})

	// Historical unified data for trend charts (Dashboard)
	// GET /api/v1/unified-data/historical?device_pk=1&category=wind_direction&start_time=2024-01-01T00:00:00Z&end_time=2024-01-02T00:00:00Z
	v1.GET("/unified-data/historical", func(c *gin.Context) {
		devicePKStr := c.Query("device_pk")
		devicePK, err := strconv.ParseUint(devicePKStr, 10, 32)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid device_pk")
			return
		}

		sensorName := c.Query("category")
		if sensorName == "" {
			Error(c, http.StatusBadRequest, "category parameter required")
			return
		}

		startStr := c.Query("start_time")
		endStr := c.Query("end_time")
		if startStr == "" || endStr == "" {
			Error(c, http.StatusBadRequest, "start_time and end_time required")
			return
		}

		startTime, err1 := time.Parse(time.RFC3339, startStr)
		endTime, err2 := time.Parse(time.RFC3339, endStr)
		if err1 != nil || err2 != nil {
			Error(c, http.StatusBadRequest, "invalid time format (RFC3339 expected)")
			return
		}

		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))

		// BMS 页慢修复 P0-1 (契约 §1.2): PG 下把 max_points 降采样下推到 SQL,
		// 只返回轻量点; 失败按旧语义返回 500 (旧路径 DB 错误也返回 500)。
		// SQLite / maxPoints<=0 / 非法时间窗 / agg=raw 完全走下面的旧路径。
		aggMode := parseAggMode(c.Query("agg"))
		// L3 读路径分派（方案 §5.3）：长窗 avg 先试分钟级 rollup（预聚合，写时算好），
		// 命中则直接返回；未启用/无数据/坏行都回落下面的既有路径（fail-open，不是错误）。
		// 位置必须在降采样聚合之前：降采样仍要扫原始行，rollup 不用。
		resolvedMode := resolveAggMode(aggMode, sensorName)
		useRollup := shouldUseRollup(RollupReadEnabled(), resolvedMode, startTime, endTime, uint(devicePK))
		if useRollup {
			if points, handled, rerr := rollupHistoryPoints(c.Request.Context(), db, historyPointQuery{
				EdgeDeviceID: uint(devicePK),
				Category:     sensorName,
				Start:        startTime,
				End:          endTime,
				MaxPoints:    maxPoints,
				Mode:         resolvedMode,
			}); rerr == nil && handled {
				Success(c, points)
				return
			}
		}
		if aggMode != AggModeRaw && shouldAggregateHistory(db, maxPoints, startTime, endTime) {
			// task-18：数据源分派（帧表覆盖 ⇒ 解码；否则 ⇒ unified_data）。
			// 两个源产出的点集必须逐值相等（history_source.go 的 aggregateFramePoints
			// 与 historyAvgSQLFor/historyChangeSQL 同口径），接线前后图表形状不变。
			points, err := queryHistoryPointsWithFallback(c.Request.Context(), db, historyPointQuery{
				EdgeDeviceID: uint(devicePK),
				Category:     sensorName,
				Start:        startTime,
				End:          endTime,
				MaxPoints:    maxPoints,
				Mode:         resolveAggMode(aggMode, sensorName),
			})
			if err != nil {
				logger.Warnf("[unified-data-historical] aggregate failed device_pk=%d category=%s: %v", devicePK, sensorName, err)
				Error(c, http.StatusInternalServerError, "failed to query historical data")
				return
			}
			Success(c, points)
			return
		}

		// ============ 数据源 1：帧表原始解码（task-27 #18，2026-11-08）============
		//
		// 演练实测：unified_data 全 DETACH 后本分支 .data=0（基线 11,126）。
		//
		// **为什么 raw 分支不能走 rollup**：rollup 存的是分钟级聚合（avg/min/max/last），
		// 而 agg=raw 的契约是「原始样本」。用聚合点冒充原始样本会改变数据语义
		// （点数、值都不同），且调用方无从分辨。所以这里走**帧表原始解码**，
		// 与 /devices/:id/history 同一条路。
		//
		// 也正因为它全量物化，**必须接内存护栏**（与 history 同一判据）。
		if v := checkHistoryFrameBudget(c.Request.Context(), db, uint(devicePK), sensorName, startTime, endTime); v.Exceeded {
			Error(c, http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"窗口内数据量超过单次上限：%d 帧 x %d 字段 = %d 行，超过行上限 %d；请缩小时间窗",
				v.Frames, v.Sensors, v.Frames*int64(v.Sensors), maxHistoryRowsPerRequest))
			return
		}
		if rows, ok := frameHistoryRows(c.Request.Context(), db, uint(devicePK), sensorName, startTime, endTime); ok {
			Success(c, downsampleUnifiedData(rows, maxPoints))
			return
		}

		// ============ 数据源 2：原裸 SQL（unified_data）============
		// P2.2 取消传播: 全量拉取绑定请求上下文。
		q := db.WithContext(c.Request.Context()).
			Where(`device_id = ? AND sensor_name = ? AND timestamp BETWEEN ? AND ?`,
				uint(devicePK), sensorName, startTime, endTime)
		var data []models.UnifiedData
		if err := q.Order("timestamp ASC").Find(&data).Error; err != nil {
			logger.Warnf("[unified-data-historical] query failed device_pk=%d category=%s: %v", devicePK, sensorName, err)
			Error(c, http.StatusInternalServerError, "failed to query historical data")
			return
		}

		// Server-side downsampling: if max_points specified and data exceeds it,
		// uniformly sample to cap response size.
		data = downsampleUnifiedData(data, maxPoints)
		if data == nil {
			data = []models.UnifiedData{}
		}

		Success(c, data)
	})

	// Batch historical query — eliminates N+1 request pattern
	// GET /api/v1/unified-data/historical-batch?device_pk=8&categories=rsoc,temperature_1,cell_voltage_1&start_time=...&end_time=...&max_points=500
	v1.GET("/unified-data/historical-batch", func(c *gin.Context) {
		devicePKStr := c.Query("device_pk")
		devicePK, err := strconv.ParseUint(devicePKStr, 10, 32)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid device_pk")
			return
		}

		categoriesStr := c.Query("categories")
		if categoriesStr == "" {
			Error(c, http.StatusBadRequest, "categories parameter required (comma-separated)")
			return
		}
		categories := strings.Split(categoriesStr, ",")

		startStr := c.Query("start_time")
		endStr := c.Query("end_time")
		if startStr == "" || endStr == "" {
			Error(c, http.StatusBadRequest, "start_time and end_time required")
			return
		}

		startTime, err1 := time.Parse(time.RFC3339, startStr)
		endTime, err2 := time.Parse(time.RFC3339, endStr)
		if err1 != nil || err2 != nil {
			Error(c, http.StatusBadRequest, "invalid time format (RFC3339 expected)")
			return
		}

		maxPoints, _ := strconv.Atoi(c.DefaultQuery("max_points", "0"))

		// BMS 页慢修复 P0-1 (契约 §1.2): PG 下每个 category 用 SQL 分桶聚合,
		// 只返回轻量点; SQLite / maxPoints<=0 / 非法时间窗 / agg=raw 完全走
		// 下面的旧路径 (全量拉取 + Go 侧 downsample, 既有测试是契约)。
		aggMode := parseAggMode(c.Query("agg"))
		useAgg := aggMode != AggModeRaw && shouldAggregateHistory(db, maxPoints, startTime, endTime)

		// Query all categories in parallel
		type catResult struct {
			Category string      `json:"category"`
			Data     interface{} `json:"data"`
		}
		results := make([]catResult, len(categories))

		var wg sync.WaitGroup
		for i, cat := range categories {
			wg.Add(1)
			go func(idx int, category string) {
				defer wg.Done()
				resolved := resolveAggMode(aggMode, category)
				// L3 读路径分派（方案 §5.3，historical 单条同款）：长窗 avg 先试 rollup。
				// 注意本次是并发 goroutine，用 db.Session(&gorm.Session{}) 隔离 Statement
				// 状态（下面的既有注释解释了不隔离会串条件）。
				if shouldUseRollup(RollupReadEnabled(), resolved, startTime, endTime, uint(devicePK)) {
					if points, handled, rerr := rollupHistoryPoints(c.Request.Context(), db.Session(&gorm.Session{}), historyPointQuery{
						EdgeDeviceID: uint(devicePK),
						Category:     category,
						Start:        startTime,
						End:          endTime,
						MaxPoints:    maxPoints,
						Mode:         resolved,
					}); rerr == nil && handled {
						results[idx] = catResult{Category: category, Data: points}
						return
					}
				}
				if useAgg {
					// task-18：同一处分派（并发 goroutine 内，用 Session 隔离 Statement）。
					points, err := queryHistoryPointsWithFallback(c.Request.Context(), db.Session(&gorm.Session{}), historyPointQuery{
						EdgeDeviceID: uint(devicePK),
						Category:     category,
						Start:        startTime,
						End:          endTime,
						MaxPoints:    maxPoints,
						Mode:         resolved,
						// task-18: **必须**告诉数据层这是批请求 —— 帧表一次解码能服务
						// 全部字段，29 个 goroutine 共享一次解码。漏了它每个 category
						// 各自单字段解码（实测 2.75× vs 0.61×）。
						// 注意：**不能**加在上面的 rollup 调用上 —— rollupHistoryPoints
						// 不读这个字段，加了只是误导读者。
						BatchHint: true,
					})
					if err != nil {
						// 单 category 查询错误: 保持现状 (忽略 error 返回空 data,
						// 不改 500), 仅记录 warn。
						logger.Warnf("[unified-data-historical-batch] aggregate failed device_pk=%d category=%s: %v",
							devicePK, category, err)
						results[idx] = catResult{Category: category, Data: []HistoryPoint{}}
						return
					}
					results[idx] = catResult{Category: category, Data: points}
					return
				}
				// Use a new Session to avoid Statement sharing between concurrent goroutines.
				// GORM v2's *gorm.DB shares Statement state across chained calls; without
				// Session{}, concurrent db.Where() calls race and corrupt each other's conditions,
				// causing some queries to return 0 rows.
				session := db.Session(&gorm.Session{})
				q := session.Where(`device_id = ? AND sensor_name = ? AND timestamp BETWEEN ? AND ?`,
					uint(devicePK), category, startTime, endTime)
				var data []models.UnifiedData
				q.Order("timestamp ASC").Find(&data)
				data = downsampleUnifiedData(data, maxPoints)
				results[idx] = catResult{Category: category, Data: data}
			}(i, cat)
		}
		wg.Wait()
		if results == nil {
			results = []catResult{}
		}

		Success(c, results)
	})
}
