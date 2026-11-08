package api

import (
	"net/http"
	"sync"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
		// Pending 是「已创建但尚未收到任何数据」的设备数（models.EdgeDeviceStatusPending）。
		// 2026-10-03 缺陷 5：此前 offline 用的是 status <> active，会把新建的
		// pending 设备算进「离线」——与"没数据却显示在线"是同一类错误，只是方向相反：
		// 刚建好的设备会被监控页报成故障。三态必须分开计数：
		// online（采到数据）/ pending（等第一帧）/ offline（超时未上报）。
		// 消费端（Monitor.vue 的 deviceTotal）用 online+offline 求和，
		// 因此前端需要一并把 pending 计入总数，否则百分比的分母会漏掉一部分设备。
		Pending int64 `json:"pending"`
	} `json:"device"`
	Node struct {
		Online  int64 `json:"online"`
		Offline int64 `json:"offline"`
	} `json:"node"`
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
	Control struct {
		OperationsTotal      int64 `json:"operations_total"`
		Active               int64 `json:"active"`
		Queued               int64 `json:"queued"`
		Succeeded            int64 `json:"succeeded"`
		Failed               int64 `json:"failed"`
		Unknown              int64 `json:"unknown"`
		UnresolvedUnknown    int64 `json:"unresolved_unknown"`
		Cancelled            int64 `json:"cancelled"`
		OutboxPending        int64 `json:"outbox_pending"`
		OutboxLeased         int64 `json:"outbox_leased"`
		CapabilityStaleNodes int64 `json:"capability_stale_nodes"`
		AuditWriteFailures   int64 `json:"audit_write_failures"`
	} `json:"control"`
	Timestamp int64 `json:"timestamp"`
}

// ==================== D3：points_stored 的采样帧数口径 ====================
//
// 背景见 getMetricsSummaryHandler 里 D3 注释块。这里只放实现。
//
// 实现已迁到 metrics_points_stored_frames.go（改从帧表计数，2026-11-08）。
//
// 为什么必须缓存：count(DISTINCT (device_id, timestamp)) 是全表操作，生产
// 72.36M 行实测 19.8 s；而 /metrics/summary 被 Monitor.vue 以 10 s 间隔轮询。
// 不缓存 = 监控页每 10 s 有一条查询跑 20 s ⇒ 连接池被拖垮。
//
// 缓存语义（懒加载，不是后台刷新）：
//   - 命中且未过期 ⇒ 直接返回，零查询；
//   - 过期/为空 ⇒ **同步**重算一次（该请求变慢，但只有一个请求变慢，不会堆积）；
//     重算失败 ⇒ 记 Warn 并返回上一次的旧值（首次则回落行数），绝不让
//     /metrics/summary 返回 5xx。
//
// 为什么 TTL=60 s：比前端轮询间隔（10 s）长 6 倍，稳态下每分钟只付一次全表扫描；
// 这是「已存储总量」这类累计指标，60 s 滞后对判读没有影响。
const pointsStoredCacheTTL = 60 * time.Second

var (
	pointsStoredMu     sync.Mutex
	pointsStoredValue  int64
	pointsStoredAt     time.Time
	pointsStoredPrimed bool
)

// cachedPointsStored 返回缓存的「采样帧数」；缓存过期时同步重算。
//
// db 为 nil 时返回 0（测试/降级路径不应 panic）。
func cachedPointsStored(db *gorm.DB) int64 {
	if db == nil {
		return 0
	}
	pointsStoredMu.Lock()
	defer pointsStoredMu.Unlock()
	if pointsStoredPrimed && time.Since(pointsStoredAt) < pointsStoredCacheTTL {
		return pointsStoredValue
	}
	frames, err := countStoredFrames(db)
	if err != nil {
		logger.Warnf("[metrics] count stored frames failed (serving last known value): %v", err)
		if pointsStoredPrimed {
			return pointsStoredValue
		}
		// 首次就失败：退化为行数（旧口径）。偏小但不会让面板转圈。
		var rows int64
		if e := db.Model(&models.UnifiedData{}).Count(&rows).Error; e == nil {
			return rows
		}
		return 0
	}
	pointsStoredValue = frames
	pointsStoredAt = time.Now()
	pointsStoredPrimed = true
	return frames
}

// countStoredFrames 数「采样帧数」= DISTINCT (device_id, timestamp)。
//
// SQL 用「子查询 + GROUP BY」而不是 PG 的 count(DISTINCT (a,b)) 行构造器：
// 后者是 PG 专有语法；前者 SQLite/PG 都能执行（SQLite 分支由单测覆盖），
// 与 handler_overview.go 的 D2 保持同一形状（两处口径必须一致，改一处要改两处）。

// resetPointsStoredCache 清空缓存（仅测试用：包级状态会跨用例串味）。
func resetPointsStoredCache() {
	pointsStoredMu.Lock()
	defer pointsStoredMu.Unlock()
	pointsStoredValue = 0
	pointsStoredAt = time.Time{}
	pointsStoredPrimed = false
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

		// --- HTTP: read from prometheus counter ---
		resp.HTTP.RequestsTotal = readCounterTotal(metrics.HTTPRequests)
		resp.HTTP.RequestsInFlight = 0 // not tracked yet

		// --- MQTT: read from prometheus counters ---
		resp.MQTT.MessagesReceived = readCounterTotal(metrics.MessagesReceived)
		resp.MQTT.MessagesSent = readCounterTotal(metrics.MessagesSent)
		resp.MQTT.ConnectionErrors = 0 // not tracked yet

		// DB counts
		// 三态分开计数，offline 不再用 `status <> active` 兜底：那样会把新建的
		// pending 设备算成离线（2026-10-03 缺陷 5）。
		// 残余的未知状态值归入 offline（保守：宁可报异常也不要谎报在线）。
		var devOnline, devOffline, devPending int64
		db.Model(&models.EdgeDevice{}).Where("status = ?", models.EdgeDeviceStatusActive).Count(&devOnline)
		db.Model(&models.EdgeDevice{}).Where("status = ?", models.EdgeDeviceStatusPending).Count(&devPending)
		db.Model(&models.EdgeDevice{}).
			Where("status NOT IN ?", []string{models.EdgeDeviceStatusActive, models.EdgeDeviceStatusPending}).
			Count(&devOffline)
		resp.Device.Online = devOnline
		resp.Device.Offline = devOffline
		resp.Device.Pending = devPending

		var nodeOnline, nodeOffline int64
		db.Model(&models.Node{}).Where("status = ?", "online").Count(&nodeOnline)
		db.Model(&models.Node{}).Where("status = ?", "offline").Count(&nodeOffline)
		resp.Node.Online = nodeOnline
		resp.Node.Offline = nodeOffline

		// ==================== D3：points_stored 口径 = 采样帧数（2026-11-07）====================
		//
		// L1（值不变不入库）无条件生效后，unified_data 的行数不再等于「采了多少点」。
		// 旧口径 COUNT(*) 会随 L1 掉到约 1/5（真实数据回归：142,467 帧/天 → 117,192 行/天），
		// 而前端 Monitor.vue 把它标成「已存储」，与旁边的「已采集」（points_collected，
		// 计数器，不受影响）并排显示 ⇒ 看起来像「采了 5000 只存了 900」。
		//
		// 新口径 = 采样帧数 = DISTINCT (device_id, timestamp)。它对 L1 不敏感：
		// L1 只减少「同一帧写几个字段」，不减少「有过几帧」。
		//
		// ⚠ **这个查询是全表的，很贵**（生产实测 72.36M 行：
		//   count(*)                              2.9 s
		//   count(DISTINCT (device_id,timestamp))  19.8 s
		// 前端 Monitor.vue 默认 10s 轮询一次）。因此这里**不直接算**，
		// 而是复用 metricsSnapshotCache 的懒加载缓存（见 getMetricsSummaryHandler 顶部）。
		// 缓存 TTL 60s：比轮询间隔长，因此稳态下每个 TTL 只付一次全表扫描。
		//
		// 若缓存里还没有值（首次请求或上一轮失败），退化为行数并在日志里 Warn 一次 ——
		// **宁可给个偏小的旧口径数字，也不让 /metrics/summary 超时**（监控页不能因为
		// 一个统计卡而整页转圈）。
		pointsStored := cachedPointsStored(db)
		resp.Data.PointsStored = pointsStored
		resp.Data.PointsCollected = readCounterTotal(metrics.DataReportsProcessed)

		var otaTotal int64
		db.Model(&models.OTATask{}).Count(&otaTotal)
		resp.OTA.UpgradesTotal = otaTotal

		// ── 控制域: 5 个【事件累计量】= 存活行 COUNT(*) + 清理基线 ─
		//
		// 清理前置条件 B (docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §2.2):
		// 这些数字原先全是无时间窗 COUNT(*), 命令域一旦启用保留策略清理,
		// 删掉旧的 FAILED 行会让"操作失败 9"变"0"、Monitor.vue:160 的 attention
		// 高亮消失 —— 看起来像故障自愈, 实际是证据被删。
		// 口径 = 存活行 + 已被清理器删除的部分 (基线), 因此【累计语义原样保住】:
		// 前端 JSON 契约字段名不变、面板文案不变、清理未上线时行为与今天逐字一致 (基线为 0)。
		baseline := commandexec.GetMetricsBaseline(db)
		db.Model(&models.CommandExecution{}).Count(&resp.Control.OperationsTotal)
		resp.Control.OperationsTotal += baseline.OperationsTotal
		db.Model(&models.CommandExecution{}).Where("status IN ?", []string{
			commandexec.StatusQueued, commandexec.StatusDispatched, commandexec.StatusDeviceAccepted, commandexec.StatusVerifying,
		}).Count(&resp.Control.Active)
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusQueued).Count(&resp.Control.Queued)
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusSucceeded).Count(&resp.Control.Succeeded)
		resp.Control.Succeeded += baseline.Succeeded
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusFailed).Count(&resp.Control.Failed)
		resp.Control.Failed += baseline.Failed
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusUnknown).Count(&resp.Control.Unknown)
		resp.Control.Unknown += baseline.Unknown
		// ── unresolved_unknown: 【刻意不加基线】──
		//
		// 它是【集合成员数】(当前 UNKNOWN 且尚未被人工处置), 不是事件累计量:
		// 有人处置就减一, 本来就会下降。若给它加基线, 而被删掉的 UNKNOWN 行
		// 【再也无法被处置】(处置入口 commandexec/service.go:594-600 必须先加载该行),
		// 基线里那几条会永久留存 ⇒ 面板"未处置异常"永久虚高且用户怎么处置都降不下来。
		// 变 0 是少报 (可察觉), 虚高是多报且不可纠正 —— 更坏。
		// 正确性由"未处置的 UNKNOWN 永不随时间删"保证 (上游 §2.4), 见 B-INV-1b。
		db.Table("command_executions AS ce").Joins("LEFT JOIN command_manual_resolutions AS cmr ON cmr.command_id = ce.command_id").
			Where("ce.status = ? AND cmr.command_id IS NULL", commandexec.StatusUnknown).Count(&resp.Control.UnresolvedUnknown)
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusCancelled).Count(&resp.Control.Cancelled)
		resp.Control.Cancelled += baseline.Cancelled
		db.Model(&models.CommandOutbox{}).Where("state = ?", "PENDING").Count(&resp.Control.OutboxPending)
		db.Model(&models.CommandOutbox{}).Where("state = ?", "LEASED").Count(&resp.Control.OutboxLeased)
		db.Model(&models.Node{}).Where("status = ? AND (resource_reported_at IS NULL OR resource_reported_at < ?)", "online", time.Now().UTC().Add(-commandexec.MaxCapabilityAge)).Count(&resp.Control.CapabilityStaleNodes)
		resp.Control.AuditWriteFailures = readCounterTotal(metrics.SecurityAuditWriteFailuresTotal)

		resp.Timestamp = time.Now().UnixMilli()
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "ok",
			"data":    resp,
		})
		// 注：此处曾有 `_ = metrics.NodesOnline // suppress unused` —— 它只是拿
		// metrics 包"占位"以维持 import，靠一句空赋值让编译通过，读起来却像
		// "指标已在本 handler 里维护"，实际什么都没写。该 import 仍有
		// readCounterTotal(metrics.DataReportsProcessed) 等真实用途，故直接删掉这行。
	}
}

// readCounterTotal reads the total value from a prometheus Counter or CounterVec (sum over all labels).
// Uses a goroutine to read while Collect sends, avoiding deadlock when label cardinality > buffer size.
func readCounterTotal(c prometheus.Collector) int64 {
	ch := make(chan prometheus.Metric, 10)
	done := make(chan struct{})
	var total float64
	go func() {
		var m dto.Metric
		for metric := range ch {
			if err := metric.Write(&m); err == nil {
				if m.Counter != nil {
					total += m.Counter.GetValue()
				}
			}
		}
		close(done)
	}()
	c.Collect(ch)
	close(ch)
	<-done
	return int64(total)
}
