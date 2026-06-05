package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/collector"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/offlinedetector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/redis"
	"ehome/backend/internal/seed"
	"ehome/backend/internal/websocket"
	"github.com/gin-contrib/cors"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// Load configuration (env > config.yaml > defaults)
	cfg := config.Load()

	// Initialize structured logger
	if err := logger.Init(cfg.LogLevel()); err != nil {
		panic("failed to init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.Infof("EHomeSystem Server v2.0 starting...")
	logger.Infof("Config: MQTT=%s, DB=%s:%d/%s, API=%s",
		cfg.MQTTBroker(), cfg.DBConfig().Host, cfg.DBConfig().Port, cfg.DBConfig().DBName, cfg.APIAddr())

	// Initialize database from config
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

	db := database.GetDB()

	// Seed test data only when explicitly requested via environment variable
	if os.Getenv("SEED_TEST_DATA") == "true" {
		if err := seed.SeedTestData(db); err != nil {
			logger.Warnf("Failed to seed test data: %v", err)
		} else {
			logger.Infof("Test data seeded")
		}
	}

	// Seed admin user if not exists
	if err := api.SeedAdminUser(db); err != nil {
		logger.Warnf("Failed to seed admin user: %v", err)
	} else {
		logger.Infof("Admin user seeded (if not existed)")
	}

	// Initialize Redis
	if err := redis.Connect(cfg.RedisAddr()); err != nil {
		logger.Infof("Redis connection failed (non-fatal): %v", err)
	} else {
		logger.Infof("Redis connected")
	}

	// Initialize MQTT client
	mqttClient, err := mqtt.Initialize(cfg.MQTTBroker(), cfg.MQTTUser(), cfg.MQTTPassword())
	if err != nil {
		logger.Fatalf("Failed to initialize MQTT: %v", err)
	}
	defer mqttClient.Close()
	logger.Infof("MQTT connected")

	// Register built-in device drivers
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(driverRegistry)
	logger.Infof("Registered %d device drivers", len(driverRegistry.List()))

	// Initialize WebSocket hub
	wsHub := websocket.NewHub()
	go wsHub.Run()

	// Initialize HomeAssistant integration
	haIntegration := homeassistant.NewIntegration(mqttClient)

	// Initialize OTA manager
	otaMgr := ota.NewManager(db, mqttClient, wsHub)

	// Initialize offline detector
	offlineDetector := offlinedetector.NewDetector(db, wsHub)

	// Initialize collector manager
	collectorMgr := collector.NewManager(db, mqttClient, wsHub, haIntegration, offlineDetector)
	go collectorMgr.Start()

	// v2.1: Server startup push — push config to all online collectors (fixes G2)
	go func() {
		time.Sleep(5 * time.Second) // Wait for MQTT/Redis/DB to be ready
		decisions := collectorMgr.SyncGate().OnServerStartup()
		for _, d := range decisions {
			if d.Action != collector.SyncActionNone {
				collectorMgr.SendConfigManifestWithDecision(d)
				logger.Infof("[sync_id=%s] Server-startup push: device=%s reason=%s",
					d.SyncID, d.DeviceID, d.Reason)
			}
		}
		if len(decisions) > 0 {
			logger.Infof("Server-startup push complete: %d collectors notified", len(decisions))
		}
	}()

	// Start offline detection loop (after collector manager is ready)
	offlineDetector.Start()

	// Setup MQTT message handlers
	mqttClient.SetHandler(collectorMgr.HandleMessage)

	// Setup HTTP API
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// CORS: allow frontend dev servers (Vite) + production origins
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173", "http://localhost:5174", "http://localhost:5175", "http://127.0.0.1:5173", "http://127.0.0.1:5174", "http://127.0.0.1:5175"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	api.SetupRoutes(r, db, wsHub, collectorMgr, otaMgr)

	// Health check
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": "2.0.0",
			"drivers": driverRegistry.List(),
		})
	})

	// Prometheus metrics endpoint
	r.GET("/metrics", func(c *gin.Context) {
		promhttp.Handler().ServeHTTP(c.Writer, c.Request)
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

	// === Graceful shutdown: wait for SIGTERM/SIGINT ===
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit

	logger.Infof("Received signal %v, shutting down gracefully...", sig)

	// 1. Stop offline detector
	offlineDetector.Stop()
	logger.Infof("Offline detector stopped")

	// 2. Stop collector manager (drains in-flight message processing)
	collectorMgr.Stop()
	logger.Infof("Collector manager stopped")

	// 3. Shutdown HTTP server with 10s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("HTTP server shutdown error: %v", err)
	} else {
		logger.Infof("HTTP server stopped")
	}

	// 4. Close MQTT connection
	mqttClient.Close()
	logger.Infof("MQTT disconnected")

	// 5. Flush logger
	logger.Infof("EHomeSystem Server stopped")
}
