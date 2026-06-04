package database

import (
	"fmt"
	"ehome/backend/pkg/logger"
	"ehome/backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

func Connect(cfg Config) error {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	sqlDB, _ := DB.DB()
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)

	logger.Infof("Database connected successfully")
	return nil
}

func AutoMigrate() error {
	return DB.AutoMigrate(
		// v2.1 表 (保留, GORM 会自动加新字段)
		&models.Collector{},
		&models.Channel{},
		&models.ConfigTemplate{},
		&models.Device{},
		&models.DeviceConfig{},
		&models.DeviceData{},
		&models.UnifiedData{},
		&models.DataSource{},
		&models.OTATask{},
		&models.Firmware{},
		&models.Notification{},
		&models.User{},
		&models.OperationLog{},
		&models.Vendor{},
		&models.DeviceModel{},
		&models.CollectorEvent{},
		&models.CalibrationCache{},
		&models.ConfigMeta{}, // v2.1: epoch persistence

		// v2.2 新表 (Phase 2A-2: DB 迁移)
		// 注意: Node 和 EdgeDevice struct 由 T-BE-RENAME-01 并行添加
		// 如果 struct 尚未定义, 注释掉这两行, 等 struct 改名完成后再启用
		// &models.Node{},
		// &models.EdgeDevice{},
	)
}

func GetDB() *gorm.DB {
	return DB
}
