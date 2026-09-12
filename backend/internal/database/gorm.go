package database

import (
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"fmt"
	"log"
	"os"
	"time"

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
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold: 200 * time.Millisecond,
			LogLevel:      gormlogger.Warn,
			// record not found 是业务正常路径(如 datalifecycle.identity.go 查询
			// 不存在的 identity_key 后即创建), 业务代码已用 errors.Is 区分。
			// 不忽略则 GORM logger 每次以 Error 级打印 SQL 到 stdout,
			// 不受 LOG_LEVEL 控制——压测/高频上报时成噪声源。
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
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
		// 数据层时序化 (v3.4 §3.2.1): UnifiedData 移出 AutoMigrate — PG 生产库
		// 由 partition_mgr.MigrateUnifiedDataToPartitioned 建分区母表 (复合主键
		// (id,timestamp))，AutoMigrate 无法建分区表且会把母表降级改写。
		// SQLite 测试库由 testutil/db.go 的 AutoMigrate 覆盖。
		&models.DataSource{},
		&models.DataSourceHealth{},
		&models.FailoverLog{},
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
		&models.PendingWriteRecord{}, // P3-4: pending write persistence
		&models.NodeLog{},            // v2.5: remote ESP32 system-log history
		// Phase 1: durable device-action control domain. These are additive
		// tables; no legacy operation history is rewritten during migration.
		&models.CommandExecution{},
		&models.CommandAttempt{},
		&models.CommandOutbox{},
		&models.CommandInbox{},
		&models.CommandConfirmation{},
		&models.CommandManualResolution{},
		&models.ConfigChangeOutbox{},

		// v3.0: GPIO/PWM peripheral control models
		&models.GPIOConfig{},
		&models.PWMConfig{},

		// 数据生命周期 P0: 逻辑设备身份 (方案 v3.3 §1.1)
		&models.LogicalDevice{},
		// 数据生命周期 M 迁移: 大表回填进度水位 (§4.3 断点续跑)
		&models.BackfillJob{},
		// 数据生命周期 P3: 合并搬迁任务进度 (§4.3 任务 3)
		&models.MergeJob{},

		// 阈值告警引擎 (方案 v0.4 §5.1.1 任务C)
		&models.AlertRule{},
		&models.AlertEvent{},

		// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)
		&models.AutomationRule{},
		&models.AutomationEvent{},

		// v2.2 新表 (Phase 2A-2: DB 迁移)
		// 注意: Node 和 EdgeDevice struct 由 T-BE-RENAME-01 并行添加
		// 如果 struct 尚未定义, 注释掉这两行, 等 struct 改名完成后再启用
		// &models.Node{},
		// &models.EdgeDevice{},
	); err != nil {
		return err
	}
	if err := ensureDeviceConfigDefaultConstraint(DB); err != nil {
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
