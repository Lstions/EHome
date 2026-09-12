// EHomeSystem main.go
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ehome/backend/internal/alert"
	"ehome/backend/internal/api"
	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/offlinedetector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/seed"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"encoding/json"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()

	if err := logger.Init(cfg.LogLevel()); err != nil {
		panic("failed to init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.Infof("EHomeSystem Server v2.0 starting...")

	// Validate JWT secret is not default in production
	api.ValidateJWTSecret()
	logger.Infof("Config: MQTT=%s, DB=%s:%d/%s, API=%s",
		cfg.MQTTBroker(), cfg.DBConfig().Host, cfg.DBConfig().Port, cfg.DBConfig().DBName, cfg.APIAddr())

	dbCfg := cfg.DBConfig()
	if err := database.Connect(database.Config{
		Host:     dbCfg.Host,
		Port:     dbCfg.Port,
		User:     dbCfg.User,
		Password: dbCfg.Password,
		DBName:   dbCfg.DBName,
		SSLMode:  dbCfg.SSLMode,
	}); err != nil {
		logger.Fatalf("Failed to connect database: %v", err)
	}
	if err := database.AutoMigrate(); err != nil {
		logger.Fatalf("Failed to migrate database: %v", err)
	}
	logger.Infof("Database connected and migrated")

	// 数据层时序化 (方案 v3.4 §3.2.1): unified_data / device_data 分区迁移 +
	// 滚动分区保障。失败降级为 Error 不 Fatal——分区功能异常不阻塞服务启动
	// （表仍以普通表形态可用）。
	db := database.GetDB()
	if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil {
		logger.Errorf("unified_data partition migration failed (continuing with flat table): %v", err)
	}
	if err := datalifecycle.MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		logger.Errorf("device_data partition migration failed (continuing with flat table): %v", err)
	}
	// 滚动分区保障: 已分区的时序表各自确保 [上月, 未来 3 月] 分区存在。
	// 未分区表 (迁移失败/非 PG) 跳过, 避免对普通表执行 PARTITION OF 报错。
	pm := datalifecycle.NewPartitionManager(db)
	ensurePartitions := func() {
		for _, table := range []string{"unified_data", "device_data"} {
			if !datalifecycle.IsTablePartitioned(db, table) {
				continue
			}
			if err := pm.EnsurePartitionsFor(table, 3); err != nil {
				logger.Errorf("ensure %s partitions failed: %v", table, err)
			}
		}
	}
	ensurePartitions()
	// 每日滚动检查: 创建下月分区（retention 到期分区由 retention_task 触发 DROP）。
	partitionStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-partitionStop:
				return
			case <-ticker.C:
				ensurePartitions()
			}
		}
	}()
	// 与其他后台任务同序收尾。
	defer close(partitionStop)

	// 数据层时序化 (方案 v3.4 §3.2.2): rollup 分钟聚合表建表 (幂等)。
	// 与分区迁移相互独立, 失败同样降级不阻塞启动 (rollup fail-open 语义)。
	if err := datalifecycle.EnsureRollupTable(db); err != nil {
		logger.Errorf("ensure rollup table failed (rollup aggregation disabled until fixed): %v", err)
	}

	// v3.0: One-time idempotent migration of old GPIO channels → gpio_configs
	if migrateResult, err := database.MigrateGPIOChannels(database.GetDB()); err != nil {
		logger.Warnf("GPIO channel migration failed (non-fatal): %v", err)
	} else if migrateResult.Migrated > 0 {
		logger.Infof("GPIO channel migration: %d migrated, %d skipped, %d errors",
			migrateResult.Migrated, migrateResult.Skipped, migrateResult.Errors)
		for _, w := range migrateResult.Warnings {
			logger.Warnf("GPIO migration: %s", w)
		}
	}

	// db 已在上方分区迁移处获取 (database.GetDB())。

	// 数据生命周期 P0 (方案 v3.3 §2.3.1 路径 1 + §4.1): 注册系统级保留期
	// 快照源, 并对全量 edge_devices (含软删) 幂等补建 logical_device。
	datalifecycle.SetSystemRetentionDays(cfg.DataRetentionDays())
	if backfilled, err := datalifecycle.BackfillLogicalDevices(db, cfg.DataRetentionDays()); err != nil {
		logger.Errorf("Logical device backfill failed: %v", err)
	} else if backfilled > 0 {
		logger.Infof("Logical device backfill: %d instance(s) attached", backfilled)
	} else {
		logger.Infof("Logical device backfill: all instances already attached")
	}

	// purge 后台任务 (§4.3 任务 2): 每日分批硬删 purge_requested 的逻辑设备数据。
	purger := datalifecycle.NewPurger(db)
	purger.Start()

	// 数据生命周期 M 迁移步骤 (方案 v3.3 §七 + §1.1): 复合索引
	// CONCURRENTLY 创建 + 大表 logical_device_id 分批回填, 全部在后台
	// goroutine 内执行——墙钟随存量数据量增长, 不能阻塞启动; 回填进度
	// 持久化在 backfill_jobs 水位表, 中断重启自动续跑 (§4.3); 两者均幂等,
	// 失败下次启动重试。运维可用 `ehomectl datalifecycle backfill`
	// 同步执行并以退出码判定校验结果。
	// 前置: P0 身份补建 (上方 BackfillLogicalDevices) 必须先完成,
	// 回填依赖实例已挂载 logical_device_id。
	backfiller := datalifecycle.NewBackfiller(db)
	backfiller.Start()

	// 数据生命周期 P3 (方案 v3.3 §4.3 任务 3): 合并搬迁 worker — 处理
	// merge_status='pending' 源的数据搬迁, 水位断点续跑 + 失败通知/重试。
	migrator := datalifecycle.NewMigrator(db)
	migrator.Start()

	// 数据生命周期 P3 (方案 v3.3 §4.1/§4.2/§4.3 任务 1): retention 每日
	// 任务 — 到期前 30/7 天通知 + 到期分批硬删。
	retentionTask := datalifecycle.NewRetentionTask(db)
	retentionTask.Start()

	if credential, err := authservice.CreateStartupInitializationCredential(db); err != nil {
		logger.Fatalf("Failed to create initialization credential: %v", err)
	} else if credential != "" {
		logger.Infof("Initialization credential (valid for 10 minutes): %s", credential)
	}

	if os.Getenv("SEED_TEST_DATA") == "true" {
		if err := seed.SeedTestData(db); err != nil {
			logger.Warnf("Failed to seed test data: %v", err)
		} else {
			logger.Infof("Test data seeded")
		}
	}

	mqttClient := mqtt.New(cfg.MQTTBroker(), cfg.MQTTUser(), cfg.MQTTPassword())
	defer mqttClient.Close()

	parserConfigs := loadDeviceConfigParsers(db)
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
	logger.Infof("Registered %d device drivers with %d parser overrides", len(driverRegistry.List()), len(parserConfigs))

	// 数据生命周期 P4 收尾 (方案 v3.3 §2.4-2/§七-3): 尽力回填
	// config_templates.edge_device_id 归属。依赖 driverRegistry 已就绪
	// (WriteData 匹配需 driver CommandTemplates); 幂等, 失败仅告警不阻断
	// 启动 (归属匹配不上留 NULL, 宁留勿删)。
	if backfilled, err := datalifecycle.BackfillConfigTemplateOwnership(db, driverRegistry); err != nil {
		logger.Warnf("ConfigTemplate ownership backfill failed: %v", err)
	} else if backfilled > 0 {
		logger.Infof("ConfigTemplate ownership backfill: %d template(s) attributed", backfilled)
	}

	wsHub := websocket.NewHub()
	wsHub.SetSessionValidator(func(subjectID uint, version int64) bool {
		var user models.User
		if err := db.Where("id = ? AND subject_key = ? AND retired_at IS NULL AND enabled = ?", subjectID, models.SystemAdminSubjectKey, true).First(&user).Error; err != nil {
			return false
		}
		state, err := models.LoadAuthState(db)
		return err == nil && state.State == models.AuthStateInitialized && user.SessionVersion == version
	})
	go wsHub.Run()
	outboxContext, stopOutbox := context.WithCancel(context.Background())
	defer stopOutbox()
	outboxProcessor := authservice.NewOutboxProcessor(db, func(subjectID uint, _ int64, _ string) {
		wsHub.DisconnectSubject(subjectID)
	})
	go outboxProcessor.Run(outboxContext, time.Second)

	haIntegration := homeassistant.NewIntegration(mqttClient)
	otaMgr := ota.NewManager(db, mqttClient, wsHub)
	offlineDetector := offlinedetector.NewDetector(db, wsHub)
	nodeMgr := nodemgr.NewManager(db, mqttClient, wsHub, haIntegration, offlineDetector, otaMgr, driverRegistry)
	// 数据层时序化 (v3.4 §3.2.4): 最新值缓存回调接线 (api 包函数, 避免包依赖环)。
	nodeMgr.SetLatestSinkFn(api.SetLatestValue)
	// 阈值告警引擎 (方案 v0.4 §5 任务C): 求值器构造 + 解析后回调接线。
	alertEvaluator := alert.NewEvaluator(db, wsHub.BroadcastEvent)
	nodeMgr.SetAlertEvaluator(alertEvaluator)
	go alertEvaluator.Start()
	defer alertEvaluator.Stop()

	// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 求值器+执行器构造接线。
	// 与 alert 并列挂同一批解析后物理量; 动作执行一律走 commandexec (9 gate+幂等+审计)。
	// 系统 actor = 单主体管理员 (subject_key=system_admin), 策略执行归因到该主体。
	var systemActorID uint
	{
		var adminUser models.User
		if err := db.Where("subject_key = ? AND retired_at IS NULL", models.SystemAdminSubjectKey).First(&adminUser).Error; err == nil {
			systemActorID = adminUser.ID
		} else {
			logger.Warnf("[automation] 未找到系统主体用户 (subject_key=system_admin), 策略 device_action 执行将受阻: %v", err)
		}
	}
	actionRegistry := deviceaction.NewBuiltInRegistry(driverRegistry)
	commandService := commandexec.NewService(db, actionRegistry)
	// 数据源主备领域服务 (B1 已实现)。显式 Options{} 即 §4 默认语义:
	// Cooldown 5m / MinResidency 2m / Staleness 5m / ScanInterval 60s。
	datasourceSvc := datasource.New(db, datasource.Options{})
	// 数据源主备引擎接线 (设计/数据源主备与故障转移.md §4/§6):
	//   解析成功 → MarkSuccess; 边缘设备离线 → MarkFailure; 停滞扫描 → Start。
	// 硬约束: sink 注入必须在 nodemgr.NewManager 之后 (nodeMgr 已构建);
	// SetSourceHealthSink 会把回调推送到所有已构建 parserConsumers, 否则
	// consumer 持有的 sink 永远为 nil, 引擎"从未触发"。
	datasourceSvc.SetNotifier(func(n models.Notification) {
		if err := db.Create(&n).Error; err != nil {
			logger.Warn("datasource: failed to write notification", "source_id", n.SourceID, "error", err)
		}
	})
	nodeMgr.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		datasourceSvc.MarkSuccess(edgeDeviceID, names, at)
	})
	offlineDetector.SetDeviceOfflineHook(func(edgeDeviceID uint) {
		datasourceSvc.MarkFailure(edgeDeviceID, datasource.TriggerDeviceOffline)
	})
	dataSourceCtx, dataSourceStop := context.WithCancel(context.Background())
	defer dataSourceStop()
	go datasourceSvc.Start(dataSourceCtx)

	automationPlanner := automation.NewPlanner(db, commandService, wsHub.BroadcastEvent, systemActorID)
	// F4 条件复核接线: 注入最新值缓存查询, 触发到执行间条件失效则落 condition_changed 不执行。
	// 用函数注入避免 automation→api 编译期反向依赖 (与 databus latestSink 同模式)。
	automationPlanner.SetLatestValueFn(api.LatestValue)
	automationEvaluator := automation.NewEvaluator(db, automationPlanner)
	nodeMgr.SetAutomationEvaluator(automationEvaluator)
	go automationEvaluator.Start()
	defer automationEvaluator.Stop()
	// B1 接线: time_window 触发器独立 1min ticker (不挂传感器解析回调)。
	automationWindowCtx, automationWindowStop := context.WithCancel(context.Background())
	automationEvaluator.StartWindowTicker(automationWindowCtx)
	defer automationWindowStop()
	// 裁决 4 确认制闭环: pending_confirm 事件 24h 超时清扫 goroutine (设计/自动化确认制闭环实现方案.md §3.4)。
	// 独立挂 Planner (非 evaluator ticker): evaluator 只缓存 sensor_threshold 规则会漏扫其它触发类型。
	automationCleanupContext, automationPlannerStopCleanup := context.WithCancel(context.Background())
	go automationPlanner.StartCleanup(automationCleanupContext)
	defer automationPlannerStopCleanup()
	commandService.SetDispatchEnabled(cfg.ControlConfig().DeviceControlV2Enabled)
	nodeMgr.SetCommandExecutionService(commandService)
	go nodeMgr.Start()

	wsHub.SetOnMessage(func(client *websocket.Client, evt websocket.Event) {
		if evt.Type == "send" {
			// Generic WebSocket events must never be a raw WriteCmd transport.
			// The former handler bypassed REST diagnostics gating, audit and the
			// CommandExecution control domain. Raw diagnostics have no audited
			// implementation yet, so reject rather than preserving a second
			// physical-TX path.
			logger.Warnf("[WS] Rejecting retired raw send event for subject=%d", client.SubjectID)
		}
	})

	mqttClient.SetHandler(nodeMgr.HandleMessage)
	mqttContext, stopMQTT := context.WithCancel(context.Background())
	defer stopMQTT()
	go func() {
		if err := mqttClient.Run(mqttContext); err != nil {
			logger.Errorf("MQTT supervisor stopped: %v", err)
		}
	}()
	if cfg.ControlConfig().DeviceControlV2Enabled {
		dispatcherOwner := commandexec.NewDispatcherOwner("server")
		// MultiTransport 按 action Transport 路由: channel_cmd_v2 → 通道指令,
		// periph_cmd → GPIO/PWM 外设帧 (PeriphCmd 0x1B)。
		transport := commandexec.NewMultiTransport(
			commandexec.NewChannelCmdV2Transport(db, mqttClient, actionRegistry),
			commandexec.NewPeriphTransport(db, mqttClient, actionRegistry))
		dispatcher := commandexec.NewDispatcher(db, transport, dispatcherOwner)
		go runCommandDispatcher(outboxContext, dispatcher, commandService, wsHub)
		logger.Infof("ChannelCmdV2 dispatcher enabled owner=%s", dispatcherOwner)
	} else {
		logger.Infof("ChannelCmdV2 dispatcher disabled by configuration")
	}

	// v2.1: push only after a real CONNECT+SUBACK, never after an arbitrary sleep.
	go func() {
		select {
		case <-mqttClient.Ready():
		case <-mqttContext.Done():
			return
		}
		decisions := nodeMgr.SyncGate().OnServerStartup()
		for _, d := range decisions {
			if d.Action != nodemgr.SyncActionNone {
				nodeMgr.SendConfigManifestWithDecision(d)
				logger.Infof("[sync_id=%s] Server-startup push: device=%s reason=%s",
					d.SyncID, d.DeviceID, d.Reason)
			}
		}
		if len(decisions) > 0 {
			logger.Infof("Server-startup push complete: %d nodes notified", len(decisions))
		}
	}()

	otaMgr.Start()
	offlineDetector.Start()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// Gzip compression for API responses (20MB JSON → ~3MB)
	r.Use(gzip.Gzip(gzip.DefaultCompression))
	allowedOrigins := []string{}
	for _, origin := range strings.Split(os.Getenv("EHOME_ALLOWED_ORIGINS"), ",") {
		if value := strings.TrimSpace(origin); value != "" {
			allowedOrigins = append(allowedOrigins, value)
		}
	}
	// CORS is only needed when AllowCredentials=true and the frontend runs on a
	// different origin. When EHOME_ALLOWED_ORIGINS is unset (empty), the
	// production deployment serves the frontend from the same origin, so CORS
	// headers are unnecessary and an empty AllowOrigins slice would panic in
	// gin-contrib/cors v1.7.7 when AllowCredentials=true.
	// 支持通配符 "*"（放开任意来源）：gin-contrib/cors 在 AllowOrigins 含 "*"
	// 且 AllowCredentials=true 时会自动回显请求 Origin 而非字面 "*"，因此
	// 既允许任何来源的浏览器访问，也保留凭据（Cookie/Authorization）传递。
	if len(allowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins:     allowedOrigins,
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Requested-With"},
			ExposeHeaders:    []string{"Content-Length", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           12 * time.Hour,
		}))
	}
	controlCfg := cfg.ControlConfig()
	api.SetupRoutes(r, db, wsHub, nodeMgr, otaMgr, driverRegistry, commandService, alertEvaluator, automationEvaluator, automationPlanner, datasourceSvc, api.ControlPolicy{
		RawDiagnosticsEnabled: controlCfg.RawDiagnosticsEnabled,
	})

	staticDir := os.Getenv("EHOME_STATIC_DIR")
	// P2：静态资源缓存头（/assets immutable、SPA 入口 no-cache）封装在 static.go
	setupStaticRoutes(r, staticDir)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": "2.0.0",
			"drivers": driverRegistry.List(),
		})
	})

	// Start HTTP server with graceful shutdown support
	srv := &http.Server{
		Addr:    cfg.APIAddr(),
		Handler: r,
	}

	go func() {
		logger.Infof("API server listening on %s", cfg.APIAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("API server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit

	logger.Infof("Received signal %v, shutting down gracefully...", sig)

	purger.Stop()
	logger.Infof("Data lifecycle purger stopped")

	backfiller.Stop()
	logger.Infof("Data lifecycle backfiller stopped")

	migrator.Stop()
	logger.Infof("Data lifecycle merge migrator stopped")

	retentionTask.Stop()
	logger.Infof("Data lifecycle retention task stopped")

	offlineDetector.Stop()
	logger.Infof("Offline detector stopped")

	otaMgr.Close()
	logger.Infof("OTA manager stopped")

	nodeMgr.Stop()
	logger.Infof("Collector manager stopped")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("HTTP server shutdown error: %v", err)
	} else {
		logger.Infof("HTTP server stopped")
	}

	mqttClient.Close()
	logger.Infof("MQTT disconnected")

	logger.Infof("EHomeSystem Server stopped")
}

func loadDeviceConfigParsers(db *gorm.DB) map[string]json.RawMessage {
	configs := make(map[string]json.RawMessage)
	var deviceConfigs []models.DeviceConfig
	if err := db.Where("status = ?", "active").Order("is_default DESC, id DESC").Find(&deviceConfigs).Error; err != nil {
		logger.Warnf("Failed to load DeviceConfig parsers: %v", err)
		return configs
	}
	for _, cfg := range deviceConfigs {
		if cfg.DeviceType == "" || len(cfg.Parser) == 0 || string(cfg.Parser) == "{}" || string(cfg.Parser) == "null" {
			continue
		}
		if _, exists := configs[cfg.DeviceType]; !exists {
			configs[cfg.DeviceType] = cfg.Parser
		}
	}
	return configs
}

func runCommandDispatcher(ctx context.Context, dispatcher *commandexec.Dispatcher, service *commandexec.Service, wsHub *websocket.Hub) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := dispatcher.ProcessOnce(ctx); err != nil {
				logger.Errorf("ChannelCmdV2 dispatch failed: %v", err)
			}
			expired, err := service.RecoverExpired(ctx)
			if err != nil {
				logger.Errorf("ChannelCmdV2 recovery failed: %v", err)
			} else if wsHub != nil {
				for _, execution := range expired {
					wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, execution)
				}
			}
		}
	}
}
