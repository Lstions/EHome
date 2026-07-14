// EHomeSystem main.go
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/drivers"
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
	"encoding/hex"
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

	if err := api.SeedAdminUser(db); err != nil {
		logger.Warnf("Failed to seed admin user: %v", err)
	} else {
		logger.Infof("Admin user seeded (if not existed)")
	}

	if err := redis.Connect(cfg.RedisAddr()); err != nil {
		logger.Infof("Redis connection failed (non-fatal): %v", err)
	} else {
		logger.Infof("Redis connected")
	}

	mqttClient, err := mqtt.Initialize(cfg.MQTTBroker(), cfg.MQTTUser(), cfg.MQTTPassword())
	if err != nil {
		logger.Fatalf("Failed to initialize MQTT: %v", err)
	}
	defer mqttClient.Close()
	logger.Infof("MQTT connected")

	parserConfigs := loadDeviceConfigParsers(db)
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
	drivers.RegisterBuiltInDriversWithParsers(drivers.GlobalRegistry(), parserConfigs)
	logger.Infof("Registered %d device drivers with %d parser overrides", len(driverRegistry.List()), len(parserConfigs))

	wsHub := websocket.NewHub()
	go wsHub.Run()

	haIntegration := homeassistant.NewIntegration(mqttClient)
	otaMgr := ota.NewManager(db, mqttClient, wsHub)
	offlineDetector := offlinedetector.NewDetector(db, wsHub)
	nodeMgr := nodemgr.NewManager(db, mqttClient, wsHub, haIntegration, offlineDetector, otaMgr)
	go nodeMgr.Start()

	wsHub.SetOnMessage(func(client *websocket.Client, evt websocket.Event) {
		if evt.Type == "send" {
			if client.UserID == "" {
				logger.Warnf("[WS] Rejecting send command: unauthenticated client")
				return
			}
			if client.Role != "admin" {
				logger.Warnf("[WS] Rejecting send command: user %s role=%s (admin required)", client.UserID, client.Role)
				return
			}
			payload, err := json.Marshal(evt.Payload)
			if err != nil {
				logger.Warnf("[WS] Failed to marshal send payload: %v", err)
				return
			}
			var p struct {
				DeviceID  string `json:"device_id"`
				ChannelID uint32 `json:"channel_id"`
				DataHex   string `json:"data_hex"`
				ReadSize  uint32 `json:"read_size"`
			}
			if err := json.Unmarshal(payload, &p); err == nil && p.DeviceID != "" && p.DataHex != "" {
				data, err := hex.DecodeString(p.DataHex)
				if err != nil {
					logger.Warnf("[WS] Invalid hex in terminal send: %v", err)
					return
				}
				if err := nodeMgr.SendWriteCommand(p.DeviceID, p.ChannelID, data, p.ReadSize); err != nil {
					logger.Warnf("[WS] SendWriteCommand failed: %v", err)
				} else {
					logger.Infof("[WS] Terminal send via WS: device=%s ch=%d data=%s read=%d", p.DeviceID, p.ChannelID, p.DataHex, p.ReadSize)
				}
			}
		}
	})

	// v2.1: Server startup push — push config to all online nodes (fixes G2)
	go func() {
		time.Sleep(5 * time.Second)
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

	mqttClient.SetHandler(nodeMgr.HandleMessage)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// Gzip compression for API responses (20MB JSON → ~3MB)
	r.Use(gzip.Gzip(gzip.DefaultCompression))
	// CORS: allow all origins — services listen on 0.0.0.0, LAN/external devices need access
	r.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			return true
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	api.SetupRoutes(r, db, wsHub, nodeMgr, otaMgr, driverRegistry)

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
