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

	"ehome/backend/internal/api"
	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/offlinedetector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/redis"
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

	db := database.GetDB()

	if os.Getenv("SEED_TEST_DATA") == "true" {
		if err := seed.SeedTestData(db); err != nil {
			logger.Warnf("Failed to seed test data: %v", err)
		} else {
			logger.Infof("Test data seeded")
		}
	}

	if err := redis.Connect(cfg.RedisAddr()); err != nil {
		logger.Infof("Redis connection failed (non-fatal): %v", err)
	} else {
		logger.Infof("Redis connected")
	}

	mqttClient := mqtt.New(cfg.MQTTBroker(), cfg.MQTTUser(), cfg.MQTTPassword())
	defer mqttClient.Close()

	parserConfigs := loadDeviceConfigParsers(db)
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
	logger.Infof("Registered %d device drivers with %d parser overrides", len(driverRegistry.List()), len(parserConfigs))

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
	actionRegistry := deviceaction.NewBuiltInRegistry(driverRegistry)
	for _, selector := range cfg.ControlConfig().EnabledDeviceActions {
		parts := strings.SplitN(selector, "/", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			logger.Warnf("Ignoring invalid device action rollout selector %q; expected device_type/action_id", selector)
			continue
		}
		if err := actionRegistry.SetEnabled(parts[0], parts[1], true); err != nil {
			logger.Warnf("Ignoring unavailable device action rollout selector %q: %v", selector, err)
			continue
		}
		logger.Infof("Enabled device action rollout: %s", selector)
	}
	commandService := commandexec.NewService(db, actionRegistry)
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
		dispatcher := commandexec.NewDispatcher(db,
			commandexec.NewChannelCmdV2Transport(db, mqttClient, actionRegistry), "server")
		go runCommandDispatcher(outboxContext, dispatcher, commandService, wsHub)
		logger.Infof("ChannelCmdV2 dispatcher enabled")
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
	if len(allowedOrigins) > 0 {
		for _, origin := range allowedOrigins {
			if strings.TrimSpace(origin) == "*" {
				logger.Fatalf("EHOME_ALLOWED_ORIGINS must not contain '*' when AllowCredentials=true; use explicit origins")
			}
		}
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
	api.SetupRoutes(r, db, wsHub, nodeMgr, otaMgr, driverRegistry, commandService, api.ControlPolicy{
		LegacyDeviceWriteMode: controlCfg.LegacyDeviceWriteMode,
		RawDiagnosticsEnabled: controlCfg.RawDiagnosticsEnabled,
	})

	staticDir := os.Getenv("EHOME_STATIC_DIR")
	if staticDir != "" {
		r.Static("/assets", staticDir+"/assets")
		r.StaticFile("/favicon.svg", staticDir+"/favicon.svg")
		r.NoRoute(func(c *gin.Context) {
			c.File(staticDir + "/index.html")
		})
	}

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
