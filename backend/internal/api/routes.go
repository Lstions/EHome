package api

import (
	"fmt"
	"net/http"
	"time"

	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
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
func SetupRoutes(r *gin.Engine, db *gorm.DB, wsHub *websocket.Hub, nodeMgr *nodemgr.Manager, otaMgr *ota.Manager, driverRegistry *drivers.Registry, options ...interface{}) {
	var controlPolicy ControlPolicy
	var commandService *commandexec.Service
	// alertEvaluatorOpt 阈值告警求值器 (方案 v0.4 §5.1.3): 经 *alert.Evaluator
	// option 注入 (main.go), CRUD 写路径调用 Invalidate 即时失效规则缓存。
	var alertEvaluatorOpt alertEvaluator
	// automationEvaluatorOpt 自动化策略求值器 (设计/自动化策略引擎方案.md v0.1):
	// 经 *automation.Evaluator option 注入 (main.go), 同上失效缓存。
	var automationEvaluatorOpt automationEvaluator
	// automationPlannerOpt 自动化编排器 (裁决 4 确认制闭环): 经 *automation.Planner
	// option 注入 (main.go), POST /automation-events/:id/confirm 人工确认执行。
	var automationPlannerOpt automationPlanner
	// automationManualTriggerOpt 手动触发器: 与 planner 同实例 (*automation.Planner),
	// 单独变量承接避免接口类型不含 TriggerRule 方法。
	var automationManualTriggerOpt automationManualTrigger
	// datasourceSvc 数据源主备领域服务 (设计/数据源主备与故障转移.md v1.0 §7):
	// 经 *datasource.Service option 注入 (main.go); 测试未注入时为 nil。
	var datasourceSvc *datasource.Service
	for _, option := range options {
		switch value := option.(type) {
		case ControlPolicy:
			controlPolicy = value
		case *commandexec.Service:
			commandService = value
		// 具体类型判断 (非接口): *automation.Evaluator 与 *alert.Evaluator 都实现
		// Invalidate() 接口, 若用接口 case 会按序首个匹配, automation 永不到达。
		case *automation.Evaluator:
			automationEvaluatorOpt = value
		// 具体类型: *automation.Planner 有 ConfirmEvent 方法, 与上述无方法集交集。
		case *automation.Planner:
			automationPlannerOpt = value
			automationManualTriggerOpt = value
		case *datasource.Service:
			datasourceSvc = value
		case alertEvaluator:
			alertEvaluatorOpt = value
		}
	}
	controlPolicy = resolveControlPolicy(controlPolicy)
	// Global HTTP metrics middleware
	r.Use(func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, path).Inc()
		c.Next()
	})

	// Prometheus metrics endpoint — the canonical unauthenticated scrape
	// target. Prometheus/Alertmanager scrape from outside the API; requiring a
	// session token would break scraping. Exposed format only carries counts
	// over labels (consumer/table) — no row payloads, no PII.
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Health check (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Login and in-session reauthentication share one limiter (in-process
	// sliding window; Redis retired in 方案 v3.4 §4 任务B).
	authLimiter := authservice.NewLoginLimiter(5, 15*time.Minute)
	registerAuthRoutesWithLimiter(r, db, authLimiter)

	// Firmware download — no auth (ESP32 fetches without JWT)
	RegisterFirmwareDownload(r)

	// API v1: every route in this group requires the authoritative single
	// subject session. Public endpoints are registered explicitly above.
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuthWithDB(db))
	{
		// Phase 1 records actions durably but intentionally has no live
		// transport. The reviewed ChannelCmdV2 dispatcher is a Phase 2 gate.
		if commandService == nil {
			commandService = commandexec.NewService(db, deviceaction.NewBuiltInRegistry(driverRegistry))
		}
		v1.GET("/metrics/prometheus", gin.WrapH(promhttp.Handler()))
		registerAccountRoutesWithLimiter(v1, db, authLimiter)
		registerDeviceRoutes(v1, db, nodeMgr, driverRegistry, controlPolicy)
		registerDataRoutes(v1, db)
		registerOTARoutes(v1, db, otaMgr, nodeMgr)
		registerOTARoutesCompat(v1, db, otaMgr, nodeMgr)
		registerHARoutes(v1)
		registerTerminalRoutes(v1, db, nodeMgr, controlPolicy)
		registerMetricsRoutes(v1, db)

		// v2.2 routes
		registerNodeRoutes(v1, db, nodeMgr)
		registerEdgeDeviceRoutes(v1, db, nodeMgr, driverRegistry, controlPolicy)
		registerDeviceOperationRoutes(v1, commandService, wsHub)
		registerDriverCommandRoutes(v1, db, nodeMgr, driverRegistry)

		// v3.0: GPIO/PWM peripheral control routes
		registerPeriphRoutes(v1, db, nodeMgr)

		// Overview + Notification routes
		registerOverviewRoutes(v1, db)
		registerNotificationRoutes(v1, db)

		// 阈值告警引擎 (方案 v0.4 §5.1.3 任务C): 规则 CRUD + 事件查询。
		// evaluator 经 options 注入 (main.go), 单测可传 nil。
		registerAlertRoutes(v1, db, alertEvaluatorOpt)

		// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 规则 CRUD + 事件查询 + 手动触发。
		// planner 供裁决 4 确认制闭环 (POST /automation-events/:id/confirm)。
		// trigger 供手动触发端点 (POST /automation-rules/:id/trigger), 与 planner 同实例。
		// commandService 供 §5.3 校验补强 (action_id Catalog 存在性 + params 规范化)。
		registerAutomationRoutes(v1, db, automationEvaluatorOpt, automationPlannerOpt, automationManualTriggerOpt, commandService)

		// 数据生命周期 P3: 逻辑设备管理 + 多源合并 (§3.4/§九)
		registerLogicalDeviceRoutes(v1, db)

		// Removed multi-user API compatibility surface (authenticated 410).
		registerLegacyUserRoutes(v1)

		// Data reports (placeholder)
		registerDataReportRoutes(v1, db)

		// Driver compatibility routes (reuse device-configs)
		registerDriverCompatRoutes(v1, db)

		// Data source CRUD routes + /devices/:id/failover-logs (v1.0 §7)。
		// 未注入领域服务时注册显式 503 占位，避免 nil 解引用 panic，并保持路由存在。
		if datasourceSvc == nil {
			registerDataSourceUnavailable(v1.Group("/data-sources"))
			registerFailoverLogUnavailable(v1)
		} else {
			registerDataSourceRoutes(v1.Group("/data-sources"), datasourceSvc)
			registerFailoverLogRoutes(v1, datasourceSvc)
		}

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
			if !controlPolicy.rawWritesEnabled() {
				return nil, fmt.Errorf("raw terminal diagnostics are disabled")
			}
			return nodeMgr.TerminalMgr().GetHistory(channelID, 256), nil
		},
		validatedTerminalWriteSender(db, func(deviceID string, channelID uint32, data []byte, readSize uint32) error {
			if !controlPolicy.rawWritesEnabled() {
				return fmt.Errorf("raw terminal writes are disabled; use an audited diagnostics service")
			}
			return nodeMgr.SendWriteCommand(deviceID, channelID, data, readSize)
		}),
	)
	r.GET("/api/v1/ws/terminal", JWTAuthWithDB(db), termWSHandler.HandleTerminalWS)
}
