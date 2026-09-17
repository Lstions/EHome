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
		// 数据层时序化 (v3.4 §3.2.1): DeviceData 移出 AutoMigrate — PG 生产库
		// 由 partition_mgr.MigrateTableToPartitioned 建分区母表 (复合主键
		// (id,timestamp))，AutoMigrate 无法建分区表且会把母表降级改写。
		// SQLite 测试库由 testutil/db.go 的 AutoMigrate 覆盖。
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
		// 外发通知通道 (设计/外发通知通道.md §3)
		&models.NotificationChannel{},
		&models.NotificationDelivery{},
		&models.User{},
		&models.AuthState{},
		&models.AuthOutbox{},
		&models.InitializationToken{},
		&models.SecurityAuditEvent{},
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
		// 清理前置条件 B: 命令域监控计数基线 (单行表)
		// 见 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §2.2
		&models.CommandMetricsBaseline{},

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
	if _, err = RetireLegacyPWMChannels(DB); err != nil {
		return err
	}
	// 死 schema 退役 (2026-09): operation_logs 0 写入者/0 读取者/0 行, 已被
	// models.SecurityAuditEvent 取代 (裁决: docs/分析/运行期无界增长表-保留策略
	// 设计-2026-09-13.md §2.7)。模型已从上面的 AutoMigrate 列表移除, 表本身
	// 在此幂等 DROP (DROP TABLE IF EXISTS, 重跑零副作用)。
	// 放在 AutoMigrate 之后: 即便某次显式重加注册建出空表, 也在同一次启动内被清掉。
	_, err = RetireLegacyOperationLogs(DB)
	if err != nil {
		return err
	}
	// 死表退役 (2026-09-15): unified_data_rollup_1m 是"写了没人读, 且读了也不划算"
	// 的冻结件 —— EXPLAIN 实测 30 天窗口查询仅毫秒级 (Index Scan + 分区裁剪),
	// 且该表无 logical_device_id 列 ⇒ 本仓查询协议下接线不可达。建表路径
	// EnsureRollupTable 与其 main.go 调用点已随退役删除; 存量库 (ehome/ehome_test)
	// 里残留的表在此幂等 DROP (裁决: docs/分析/rollup-退役裁决-2026-09-15.md)。
	// 放在 AutoMigrate 之后: 即便某次有人把建表加回来, 也在同一次启动内被清掉。
	if _, err = RetireLegacyRollup1m(DB); err != nil {
		return err
	}
	// 列级 schema 漂移修复 (2026-09-17 生产实测): v2.3 把 OTATask.CollectorID 改名为
	// NodeID 后，代码里已无 collector_id 的读写者，但**老 PG 库上该列仍是 NOT NULL**，
	// 而 AutoMigrate 不会删列 ⇒ 每次建 OTA 任务都报
	//   null value in column "collector_id" ... violates not-null constraint
	// 表现为「OTA 升级」必 500、OTA 功能整体不可用。
	// 单测跑 SQLite 内存库（按模型现建表，无此列）所以从未暴露 —— 属"旧库 schema
	// 漂移"而非模型/逻辑缺陷。在此幂等 DROP（列不存在则零 DDL 副作用）。
	if _, err = MigrateOTATaskDropLegacyCollectorID(DB); err != nil {
		return err
	}
	return err
}

func GetDB() *gorm.DB {
	return DB
}
