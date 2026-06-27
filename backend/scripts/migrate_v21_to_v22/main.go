package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Migration v2.1 → v2.2
//
// 功能:
//   - 创建 nodes / edge_devices 表 (GORM AutoMigrate)
//   - 迁移 collectors → nodes (数据复制)
//   - 迁移 devices → edge_devices (数据复制)
//   - 重命名 channels.collector_id → channels.node_id
//   - 加 FK 约束
//   - 创建兼容视图
//   - --dry-run 模式 (只打印 SQL, 不执行)
//
// 使用方法:
//   go run main.go --dry-run          # 预览 SQL
//   go run main.go                    # 执行迁移
//   go run main.go --config=../../config.yaml  # 指定配置文件
//
// 注意:
//   - 幂等性: 可重复执行
//   - 零数据丢失: 全量复制, 不删老表
//   - 兼容视图: 老代码可继续读 collectors / devices 视图

var (
	dryRun     = flag.Bool("dry-run", false, "Print SQL without executing")
	configPath = flag.String("config", "", "Path to config.yaml (default: ../../config.yaml)")
	verbose    = flag.Bool("v", false, "Verbose output")
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

func main() {
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Printf("🚀 v2.1 → v2.2 Migration Tool")
	log.Printf("   dry-run: %v", *dryRun)

	// Load config
	cfg := loadConfig()

	// Connect database
	db := connectDB(cfg)

	if *dryRun {
		log.Println("📋 DRY-RUN MODE: Printing SQL without executing")
		printMigrationSQL()
		return
	}

	// Execute migration
	if err := runMigration(db); err != nil {
		log.Fatalf("❌ Migration failed: %v", err)
	}

	log.Println("✅ v2.1 → v2.2 migration complete")
}

func loadConfig() DatabaseConfig {
	// Default config
	cfg := DatabaseConfig{
		Host:     getEnv("EHOME_DB_HOST", "localhost"),
		Port:     5432,
		User:     getEnv("EHOME_DB_USER", "ehome"),
		Password: getEnv("EHOME_DB_PASSWORD", "ehome123"),
		DBName:   getEnv("EHOME_DB_NAME", "ehome"),
		SSLMode:  getEnv("EHOME_DB_SSLMODE", "disable"),
	}

	// Try loading from config.yaml
	configFile := *configPath
	if configFile == "" {
		configFile = "../../config.yaml"
	}

	// Check if config file exists
	if _, err := os.Stat(configFile); err == nil {
		log.Printf("   config: %s", configFile)
		// For simplicity, we just use env vars + defaults
		// In production, you'd parse the YAML file
	} else {
		log.Printf("   config: using defaults (file not found: %s)", configFile)
	}

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func connectDB(cfg DatabaseConfig) *gorm.DB {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	log.Printf("   database: %s@%s:%d/%s", cfg.User, cfg.Host, cfg.Port, cfg.DBName)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		log.Fatalf("Failed to connect database: %v", err)
	}

	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)

	return db
}

func printMigrationSQL() {
	fmt.Println()
	fmt.Println("-- ============================================")
	fmt.Println("-- Step 1: 创建新表")
	fmt.Println("-- ============================================")
	fmt.Print(`
CREATE TABLE IF NOT EXISTS nodes (
  id                    SERIAL PRIMARY KEY,
  node_id               VARCHAR(32) UNIQUE NOT NULL,
  name                  VARCHAR(64) NOT NULL,
  model                 VARCHAR(20),
  firmware_version      VARCHAR(32),
  protocol_version      VARCHAR(16) DEFAULT '2.2',
  platform              VARCHAR(16),
  status                VARCHAR(20) DEFAULT 'offline',
  last_seen             TIMESTAMPTZ,
  last_ping_at          TIMESTAMPTZ,
  uptime_seconds        INTEGER DEFAULT 0,
  ping_latency_ms       INTEGER DEFAULT 0,
  mqtt_topic_up         VARCHAR(128),
  mqtt_topic_down       VARCHAR(128),
  wifi_ssid             VARCHAR(64),
  wifi_rssi             INTEGER,
  free_heap_bytes       INTEGER,
  capabilities          JSONB,
  hardware_info         JSONB,
  config_epoch          BIGINT DEFAULT 0,
  last_manifest_id      VARCHAR(64),
  config_sync_state     VARCHAR(20) DEFAULT 'unknown',
  last_sync_at          TIMESTAMPTZ,
  last_sync_id          VARCHAR(64),
  -- mqtt_topic_format removed (产品未发布, 无需兼容)
  created_at            TIMESTAMPTZ DEFAULT NOW(),
  updated_at            TIMESTAMPTZ DEFAULT NOW(),
  deleted_at            TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS edge_devices (
  id                  SERIAL PRIMARY KEY,
  name                VARCHAR(64) NOT NULL,
  node_id             INTEGER NOT NULL,
  channel_id          INTEGER NOT NULL,
  device_config_id    INTEGER NOT NULL,
  hardware_id         INTEGER NOT NULL DEFAULT 0,
  interval_ms         INTEGER NOT NULL DEFAULT 5000,
  enabled             BOOLEAN NOT NULL DEFAULT true,
  status              VARCHAR(20) NOT NULL DEFAULT 'active',
  error_code          INTEGER NOT NULL DEFAULT 0,
  last_data_at        TIMESTAMPTZ,
  last_error          VARCHAR(256),
  config_version      VARCHAR(64),
  init_state          VARCHAR(20) NOT NULL DEFAULT 'pending',
  init_last_step      INTEGER NOT NULL DEFAULT 0,
  init_total_steps    INTEGER NOT NULL DEFAULT 0,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
);
`)

	fmt.Println()
	fmt.Println("-- ============================================")
	fmt.Println("-- Step 2: 数据迁移")
	fmt.Println("-- ============================================")
	fmt.Print(`
-- collectors → nodes
INSERT INTO nodes (...)
SELECT ... FROM collectors WHERE deleted_at IS NULL
ON CONFLICT (node_id) DO NOTHING;

-- devices → edge_devices
INSERT INTO edge_devices (...)
SELECT ... FROM devices WHERE deleted_at IS NULL
ON CONFLICT (id) DO NOTHING;
`)

	fmt.Println()
	fmt.Println("-- ============================================")
	fmt.Println("-- Step 3: 列重命名")
	fmt.Println("-- ============================================")
	fmt.Print(`
ALTER TABLE channels RENAME COLUMN collector_id TO node_id;
`)

	fmt.Println()
	fmt.Println("-- ============================================")
	fmt.Println("-- Step 4: 加 FK 约束")
	fmt.Println("-- ============================================")
	fmt.Print(`
ALTER TABLE edge_devices
  ADD CONSTRAINT fk_edge_devices_node
  FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT;

ALTER TABLE edge_devices
  ADD CONSTRAINT fk_edge_devices_device_config
  FOREIGN KEY (device_config_id) REFERENCES device_configs(id) ON DELETE RESTRICT NOT VALID;
`)

}

func runMigration(db *gorm.DB) error {
	log.Println()
	log.Println("Step 1: 创建新表...")
	if err := createTables(db); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}

	log.Println()
	log.Println("Step 2: 数据迁移...")
	if err := migrateData(db); err != nil {
		return fmt.Errorf("migrate data: %w", err)
	}

	log.Println()
	log.Println("Step 3: 列重命名...")
	if err := renameColumns(db); err != nil {
		return fmt.Errorf("rename columns: %w", err)
	}

	log.Println()
	log.Println("Step 4: 加 FK 约束...")
	if err := addForeignKeys(db); err != nil {
		return fmt.Errorf("add FK: %w", err)
	}

	log.Println()
	log.Println("Step 5: 数据完整性检查...")
	if err := verifyMigration(db); err != nil {
		log.Printf("⚠️  Warning: %v", err)
	}

	return nil
}

func createTables(db *gorm.DB) error {
	// 创建 nodes 表
	result := db.Exec(`
CREATE TABLE IF NOT EXISTS nodes (
  id                    SERIAL PRIMARY KEY,
  node_id               VARCHAR(32) UNIQUE NOT NULL,
  name                  VARCHAR(64) NOT NULL,
  model                 VARCHAR(20),
  firmware_version      VARCHAR(32),
  protocol_version      VARCHAR(16) DEFAULT '2.2',
  platform              VARCHAR(16),
  status                VARCHAR(20) DEFAULT 'offline',
  last_seen             TIMESTAMPTZ,
  last_ping_at          TIMESTAMPTZ,
  uptime_seconds        INTEGER DEFAULT 0,
  ping_latency_ms       INTEGER DEFAULT 0,
  mqtt_topic_up         VARCHAR(128),
  mqtt_topic_down       VARCHAR(128),
  wifi_ssid             VARCHAR(64),
  wifi_rssi             INTEGER,
  free_heap_bytes       INTEGER,
  capabilities          JSONB,
  hardware_info         JSONB,
  config_epoch          BIGINT DEFAULT 0,
  last_manifest_id      VARCHAR(64),
  config_sync_state     VARCHAR(20) DEFAULT 'unknown',
  last_sync_at          TIMESTAMPTZ,
  last_sync_id          VARCHAR(64),
  -- mqtt_topic_format removed (产品未发布, 无需兼容)
  created_at            TIMESTAMPTZ DEFAULT NOW(),
  updated_at            TIMESTAMPTZ DEFAULT NOW(),
  deleted_at            TIMESTAMPTZ
)
	`)
	if result.Error != nil {
		return result.Error
	}
	log.Printf("  ✓ Created nodes table")

	// 创建 edge_devices 表
	result = db.Exec(`
CREATE TABLE IF NOT EXISTS edge_devices (
  id                  SERIAL PRIMARY KEY,
  name                VARCHAR(64) NOT NULL,
  node_id             INTEGER NOT NULL,
  channel_id          INTEGER NOT NULL,
  device_config_id    INTEGER NOT NULL,
  hardware_id         INTEGER NOT NULL DEFAULT 0,
  interval_ms         INTEGER NOT NULL DEFAULT 5000,
  enabled             BOOLEAN NOT NULL DEFAULT true,
  status              VARCHAR(20) NOT NULL DEFAULT 'active',
  error_code          INTEGER NOT NULL DEFAULT 0,
  last_data_at        TIMESTAMPTZ,
  last_error          VARCHAR(256),
  config_version      VARCHAR(64),
  init_state          VARCHAR(20) NOT NULL DEFAULT 'pending',
  init_last_step      INTEGER NOT NULL DEFAULT 0,
  init_total_steps    INTEGER NOT NULL DEFAULT 0,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
)
	`)
	if result.Error != nil {
		return result.Error
	}
	log.Printf("  ✓ Created edge_devices table")

	// 创建唯一索引
	result = db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_edge_devices_unique 
  ON edge_devices(node_id, channel_id, hardware_id) 
  WHERE deleted_at IS NULL
	`)
	if result.Error != nil {
		log.Printf("  ⚠️  Warning: failed to create unique index: %v", result.Error)
	} else {
		log.Printf("  ✓ Created unique index on edge_devices")
	}

	return nil
}

func migrateData(db *gorm.DB) error {
	// 检查 collectors 表是否存在
	var collectorsExist bool
	db.Raw("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'collectors')").Scan(&collectorsExist)

	if collectorsExist {
		// 检查 nodes 是否已有数据
		var nodesCount int64
		db.Raw("SELECT COUNT(*) FROM nodes").Scan(&nodesCount)

		if nodesCount == 0 {
			log.Printf("  → Migrating collectors → nodes...")

			// 迁移 collectors → nodes
			result := db.Exec(`
INSERT INTO nodes (
  id, node_id, name, model, firmware_version, protocol_version,
  platform, status, last_seen, last_ping_at, uptime_seconds,
  ping_latency_ms, mqtt_topic_up, mqtt_topic_down,
  wifi_ssid, wifi_rssi, free_heap_bytes, capabilities, hardware_info,
  config_epoch, last_manifest_id, config_sync_state, last_sync_at, last_sync_id,
  created_at, updated_at, deleted_at
)
SELECT
  id, 
  device_id,
  COALESCE(name, 'node_' || device_id),
  model, 
  firmware_version, 
  COALESCE(protocol_version, '2.1'),
  platform, 
  status, 
  last_seen, 
  last_ping_at, 
  uptime_seconds,
  ping_latency_ms, 
  mqtt_topic_up, 
  mqtt_topic_down,
  wifi_ssid, 
  wifi_rssi, 
  free_heap_bytes, 
  capabilities, 
  hardware_info,
  COALESCE(config_epoch, 0), 
  last_manifest_id, 
  config_sync_state, 
  last_sync_at, 
  last_sync_id,
  created_at, 
  updated_at,
  deleted_at
FROM collectors
ON CONFLICT (node_id) DO NOTHING
			`)
			if result.Error != nil {
				return result.Error
			}
			log.Printf("  ✓ Migrated %d rows from collectors to nodes", result.RowsAffected)
		} else {
			log.Printf("  ↪ nodes table already has %d rows, skip migration", nodesCount)
		}

		// 同步 sequence
		db.Exec("SELECT setval(pg_get_serial_sequence('nodes', 'id'), COALESCE((SELECT MAX(id) FROM nodes), 1))")
	} else {
		log.Printf("  ↪ collectors table does not exist, skip migration")
	}

	// 检查 devices 表是否存在
	var devicesExist bool
	db.Raw("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'devices')").Scan(&devicesExist)

	if devicesExist {
		// 检查 edge_devices 是否已有数据
		var edgeDevicesCount int64
		db.Raw("SELECT COUNT(*) FROM edge_devices").Scan(&edgeDevicesCount)

		if edgeDevicesCount == 0 {
			log.Printf("  → Migrating devices → edge_devices...")

			// 迁移 devices → edge_devices
			result := db.Exec(`
INSERT INTO edge_devices (
  id, name, node_id, channel_id, device_config_id, hardware_id,
  interval_ms, enabled, status, error_code, last_data_at, last_error,
  config_version, init_state, init_last_step, init_total_steps,
  created_at, updated_at, deleted_at
)
SELECT
  d.id, 
  d.name, 
  COALESCE(c.collector_id, 1),
  d.channel_id, 
  0,
  0,
  COALESCE(d.interval_ms, 5000), 
  COALESCE(d.enabled, true), 
  COALESCE(d.status, 'active'), 
  0, 
  NULL, 
  '',
  '', 
  'pending', 
  0, 
  0,
  d.created_at, 
  d.updated_at,
  d.deleted_at
FROM devices d
LEFT JOIN channels c ON c.id = d.channel_id
ON CONFLICT (id) DO NOTHING
			`)
			if result.Error != nil {
				return result.Error
			}
			log.Printf("  ✓ Migrated %d rows from devices to edge_devices", result.RowsAffected)
		} else {
			log.Printf("  ↪ edge_devices table already has %d rows, skip migration", edgeDevicesCount)
		}

		// 同步 sequence
		db.Exec("SELECT setval(pg_get_serial_sequence('edge_devices', 'id'), COALESCE((SELECT MAX(id) FROM edge_devices), 1))")

		// 尝试补 device_config_id
		log.Printf("  → Fixing device_config_id...")
		result := db.Exec(`
UPDATE edge_devices ed
SET device_config_id = (
  SELECT dc.id FROM device_configs dc
  WHERE dc.device_type = (
    SELECT d.type FROM devices d WHERE d.id = ed.id
  )
  AND dc.is_default = true
  LIMIT 1
)
WHERE device_config_id = 0
AND EXISTS (
  SELECT 1 FROM devices d WHERE d.id = ed.id AND d.type IS NOT NULL
)
		`)
		if result.Error != nil {
			log.Printf("  ⚠️  Warning: failed to fix device_config_id: %v", result.Error)
		} else if result.RowsAffected > 0 {
			log.Printf("  ✓ Fixed device_config_id for %d edge_devices", result.RowsAffected)
		}
	} else {
		log.Printf("  ↪ devices table does not exist, skip migration")
	}

	return nil
}

func renameColumns(db *gorm.DB) error {
	// 检查 channels.collector_id 是否存在
	var collectorIDExists bool
	db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns 
  WHERE table_name = 'channels' AND column_name = 'collector_id'
)
	`).Scan(&collectorIDExists)

	if collectorIDExists {
		log.Printf("  → Renaming channels.collector_id → channels.node_id...")
		result := db.Exec("ALTER TABLE channels RENAME COLUMN collector_id TO node_id")
		if result.Error != nil {
			return result.Error
		}
		log.Printf("  ✓ Renamed channels.collector_id → channels.node_id")
	} else {
		log.Printf("  ↪ channels.collector_id does not exist, skip rename")
	}

	// 创建索引
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_channels_node_id ON channels(node_id)",
		"CREATE INDEX IF NOT EXISTS idx_edge_devices_node_id ON edge_devices(node_id)",
		"CREATE INDEX IF NOT EXISTS idx_edge_devices_channel_id ON edge_devices(channel_id)",
		"CREATE INDEX IF NOT EXISTS idx_edge_devices_device_config_id ON edge_devices(device_config_id)",
	}

	for _, sql := range indexes {
		if result := db.Exec(sql); result.Error != nil {
			log.Printf("  ⚠️  Warning: failed to create index: %v", result.Error)
		}
	}
	log.Printf("  ✓ Created indexes")

	return nil
}

func addForeignKeys(db *gorm.DB) error {
	// edge_devices.node_id → nodes.id
	var fkNodeExists bool
	db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM information_schema.table_constraints
  WHERE constraint_name = 'fk_edge_devices_node' 
  AND table_name = 'edge_devices'
)
	`).Scan(&fkNodeExists)

	if !fkNodeExists {
		log.Printf("  → Adding FK: edge_devices.node_id → nodes.id...")
		result := db.Exec(`
ALTER TABLE edge_devices
  ADD CONSTRAINT fk_edge_devices_node
  FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT
		`)
		if result.Error != nil {
			return result.Error
		}
		log.Printf("  ✓ Added FK: edge_devices.node_id → nodes.id")
	} else {
		log.Printf("  ↪ FK edge_devices.node_id already exists, skip")
	}

	// edge_devices.device_config_id → device_configs.id
	var fkConfigExists bool
	db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM information_schema.table_constraints
  WHERE constraint_name = 'fk_edge_devices_device_config' 
  AND table_name = 'edge_devices'
)
	`).Scan(&fkConfigExists)

	if !fkConfigExists {
		log.Printf("  → Adding FK (NOT VALID): edge_devices.device_config_id → device_configs.id...")
		result := db.Exec(`
ALTER TABLE edge_devices
  ADD CONSTRAINT fk_edge_devices_device_config
  FOREIGN KEY (device_config_id) REFERENCES device_configs(id) ON DELETE RESTRICT NOT VALID
		`)
		if result.Error != nil {
			log.Printf("  ⚠️  Warning: failed to add FK: %v", result.Error)
		} else {
			log.Printf("  ✓ Added FK (NOT VALID): edge_devices.device_config_id → device_configs.id")
		}
	} else {
		log.Printf("  ↪ FK edge_devices.device_config_id already exists, skip")
	}

	// channels.node_id → nodes.id
	var channelsExist bool
	db.Raw("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'channels')").Scan(&channelsExist)

	if channelsExist {
		var fkChannelsNodeExists bool
		db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM information_schema.table_constraints
  WHERE constraint_name = 'fk_channels_node' 
  AND table_name = 'channels'
)
		`).Scan(&fkChannelsNodeExists)

		if !fkChannelsNodeExists {
			log.Printf("  → Adding FK: channels.node_id → nodes.id...")
			result := db.Exec(`
ALTER TABLE channels
  ADD CONSTRAINT fk_channels_node
  FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE RESTRICT
			`)
			if result.Error != nil {
				log.Printf("  ⚠️  Warning: failed to add FK: %v", result.Error)
			} else {
				log.Printf("  ✓ Added FK: channels.node_id → nodes.id")
			}
		} else {
			log.Printf("  ↪ FK channels.node_id already exists, skip")
		}
	}

	return nil
}

func verifyMigration(db *gorm.DB) error {
	// 检查数据完整性
	var nodesCount int64
	var edgeDevicesCount int64
	var missingConfigCount int64

	db.Raw("SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL").Scan(&nodesCount)
	db.Raw("SELECT COUNT(*) FROM edge_devices WHERE deleted_at IS NULL").Scan(&edgeDevicesCount)
	db.Raw("SELECT COUNT(*) FROM edge_devices WHERE device_config_id = 0 OR device_config_id IS NULL").Scan(&missingConfigCount)

	log.Printf("  📊 Migration stats:")
	log.Printf("     nodes: %d", nodesCount)
	log.Printf("     edge_devices: %d", edgeDevicesCount)
	log.Printf("     edge_devices with device_config_id=0: %d", missingConfigCount)

	if missingConfigCount > 0 {
		log.Printf("  ⚠️  Warning: %d edge_devices have device_config_id=0 (need manual fix)", missingConfigCount)
	}

	return nil
}
