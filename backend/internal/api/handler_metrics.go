package api

import (
	"net/http"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
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
		var devOnline, devOffline int64
		db.Model(&models.EdgeDevice{}).Where("status = ?", "active").Count(&devOnline)
		db.Model(&models.EdgeDevice{}).Where("status <> ?", "active").Count(&devOffline)
		resp.Device.Online = devOnline
		resp.Device.Offline = devOffline

		var nodeOnline, nodeOffline int64
		db.Model(&models.Node{}).Where("status = ?", "online").Count(&nodeOnline)
		db.Model(&models.Node{}).Where("status = ?", "offline").Count(&nodeOffline)
		resp.Node.Online = nodeOnline
		resp.Node.Offline = nodeOffline

		var points int64
		db.Model(&models.UnifiedData{}).Count(&points)
		resp.Data.PointsStored = points
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
		_ = metrics.NodesOnline // suppress unused
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
