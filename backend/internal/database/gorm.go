package database

import (
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"fmt"

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
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true, // GORM AutoMigrate creates wrong-direction FKs; real FKs managed via SQL migration
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
	legacyPWM, err := CheckLegacyPWMRows(DB)
	if err != nil {
		return err
	}
	if legacyPWM.MigrationRequired {
		return fmt.Errorf("pwm_configs migration_required: %d legacy row(s) lack hardware_id/channel; reconcile rows against a fresh ResourceReport: %+v", len(legacyPWM.Rows), legacyPWM.Rows)
	}
	if err := DB.AutoMigrate(
		// v2.1 表 (保留, GORM 会自动加新字段)
		&models.Node{},
		&models.Channel{},
		&models.ConfigTemplate{},
		&models.EdgeDevice{},
		&models.DeviceConfig{},
		&models.DeviceData{},
		&models.UnifiedData{},
		&models.DataSource{},
		&models.OTATask{},
		&models.Firmware{},
		&models.Notification{},
		&models.User{},
		&models.AuthState{},
		&models.AuthOutbox{},
		&models.InitializationToken{},
		&models.SecurityAuditEvent{},
		&models.OperationLog{},
		&models.Vendor{},
		&models.DeviceModel{},
		&models.NodeEvent{},
		&models.CalibrationCache{},
		&models.ConfigMeta{},         // v2.1: epoch persistence
		&models.PendingWriteRecord{}, // P3-4: pending write persistence
		&models.NodeLog{},            // v2.5: remote ESP32 system-log history

		// v3.0: GPIO/PWM peripheral control models
		&models.GPIOConfig{},
		&models.PWMConfig{},

		// v2.2 新表 (Phase 2A-2: DB 迁移)
		// 注意: Node 和 EdgeDevice struct 由 T-BE-RENAME-01 并行添加
		// 如果 struct 尚未定义, 注释掉这两行, 等 struct 改名完成后再启用
		// &models.Node{},
		// &models.EdgeDevice{},
	); err != nil {
		return err
	}
	if _, err = MigrateGPIOChannels(DB); err != nil {
		return err
	}
	_, err = RetireLegacyPWMChannels(DB)
	return err
}

func GetDB() *gorm.DB {
	return DB
}
