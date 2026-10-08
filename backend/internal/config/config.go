package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds all server configuration
type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Database       DatabaseConfig       `yaml:"database"`
	Log            LogConfig            `yaml:"log"`
	Control        ControlConfig        `yaml:"control"`
	Ingest         IngestConfig         `yaml:"ingest"`
	DataRetention  DataRetentionConfig  `yaml:"data_retention"`
	AdminBootstrap AdminBootstrapConfig `yaml:"admin_bootstrap"`
	Device         DeviceConfig         `yaml:"device"`
}

// DeviceConfig configures the 3.0 device-facing TCP+TLS listener.
//
// # This is now the ONLY device transport
//
// MQTT was removed from the backend on 2026-10-08, so this listener is the sole
// way a device can reach the server (uplink AND downlink). The `enabled`
// default of false is therefore no longer "safe because MQTT still works" --
// with it off there is no device path at all.
//
// ⚠ Startup does not refuse Enabled=false: the setting is still useful for
// API-only / test deployments, and refusing would break them. But an operator
// who leaves it off on a real deployment gets a server that no device can talk
// to, so the effective configuration is logged loudly at startup (see
// cmd/server/main.go) rather than assumed.
//
// A half-configured listener is refused at startup rather than silently
// skipped, because "I enabled it and nothing listens" is indistinguishable
// from "the device cannot connect" once you are debugging at 3am.
type DeviceConfig struct {
	// Enabled turns the 3.0 listener on. Default false.
	Enabled bool `yaml:"enabled"`

	// Addr is the listen address, e.g. ":8443" (design §4.1 default port).
	Addr string `yaml:"addr"`

	// CertFile / KeyFile are the server certificate chain and private key.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`

	// ClientCAFile is the CA bundle used to verify DEVICE certificates.
	// Required when Enabled: without it the listener would accept any client,
	// which is the opposite of mTLS.
	ClientCAFile string `yaml:"client_ca_file"`

	// ReadTimeoutSec / WriteTimeoutSec bound a single read/write.
	// 0 means "use the built-in default".
	ReadTimeoutSec  int `yaml:"read_timeout_sec"`
	WriteTimeoutSec int `yaml:"write_timeout_sec"`
}

// Validate refuses a partially configured listener.
//
// Called only when Enabled is true, so an operator who has not opted in is
// never blocked by 3.0 settings they do not have yet.
func (d DeviceConfig) Validate() error {
	if !d.Enabled {
		return nil
	}
	if d.Addr == "" {
		return fmt.Errorf("device.addr is required when device.enabled is true")
	}
	if d.CertFile == "" || d.KeyFile == "" {
		return fmt.Errorf("device.cert_file and device.key_file are required when device.enabled is true")
	}
	if d.ClientCAFile == "" {
		return fmt.Errorf("device.client_ca_file is required when device.enabled is true " +
			"(a device listener without client verification is not mTLS)")
	}
	return nil
}

type ControlConfig struct {
	// DeviceControlV2Enabled defaults to true: implemented control actions
	// are available without an environment-variable allowlist.  Turning it
	// off stops the ChannelCmdV2 dispatcher entirely (emergency kill switch),
	// which also makes every operation unavailable.
	DeviceControlV2Enabled bool `yaml:"device_control_v2_enabled"`
	RawDiagnosticsEnabled  bool `yaml:"raw_diagnostics_enabled"`
}

// ServerConfig holds HTTP server settings
type ServerConfig struct {
	Addr string `yaml:"addr"`
}

// IngestConfig holds data-ingest pipeline parallelism settings.
type IngestConfig struct {
	// ParserShards is the number of independent bus workers the heavy
	// consumers (sensor_parser, db_persist) are fanned out into. Devices are
	// hashed by node ID, so per-device ordering is preserved within a shard.
	// Fixed for the process lifetime; 0/1 means the legacy single consumer.
	ParserShards int `yaml:"parser_shards"`
}

// DefaultParserShards fans the heavy consumers out across 8 workers. The
// ingest pipeline was measured saturating a single serial consumer at
// ~64 evt/s; 8 shards lift the ceiling ~8x while staying well inside the
// default PostgreSQL connection budget (25 conns; each shard holds ≤1).
const DefaultParserShards = 8

// DatabaseConfig holds PostgreSQL settings
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// LogConfig holds logging settings
type LogConfig struct {
	Level string `yaml:"level"`
}

// DefaultDataRetentionDays is the system-level retention applied to newly
// created logical devices (方案 v3.3 §4.1). Overridable via the
// EHOME_DATA_RETENTION_DAYS environment variable (or data_retention.days in
// config.yaml). Existing logical devices keep the value snapshotted at
// creation time; changing this setting never retroacts.
const DefaultDataRetentionDays = 90

// DataRetentionConfig holds the system-level data retention policy.
type DataRetentionConfig struct {
	Days int `yaml:"days"` // EHOME_DATA_RETENTION_DAYS; 新建 logical_device 的保留天数快照
}

// AdminBootstrapConfig is an explicit, first-run-only administrator
// bootstrap configuration. Both username and password must be supplied;
// there is intentionally no default account or password.
type AdminBootstrapConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Email    string `yaml:"email"`
}

// Default configuration
func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "ehome",
			Password: "ehome123",
			DBName:   "ehome",
			SSLMode:  "disable",
		},
		Log: LogConfig{
			Level: "info",
		},
		Control:        ControlConfig{DeviceControlV2Enabled: true},
		Ingest:         IngestConfig{ParserShards: DefaultParserShards},
		DataRetention:  DataRetentionConfig{Days: DefaultDataRetentionDays},
		AdminBootstrap: AdminBootstrapConfig{},
	}
}

// Load loads configuration with priority: env vars > config.yaml > defaults
func Load() *Config {
	cfg := defaultConfig()

	// Try loading config.yaml
	configPath := getEnv("CONFIG_PATH", "config.yaml")
	if data, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", configPath, err)
		}
	}

	// Override with environment variables (highest priority)
	overrideWithEnv(cfg)

	return cfg
}

// overrideWithEnv replaces config values with environment variables if set
func overrideWithEnv(cfg *Config) {
	if v := getEnv("EHOME_SERVER_ADDR", ""); v != "" {
		cfg.Server.Addr = v
	}
	if v := getEnv("EHOME_DB_HOST", ""); v != "" {
		cfg.Database.Host = v
	}
	if v := getEnv("EHOME_DB_PORT", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = n
		}
	}
	if v := getEnv("EHOME_DB_USER", ""); v != "" {
		cfg.Database.User = v
	}
	if v := getEnv("EHOME_DB_PASSWORD", ""); v != "" {
		cfg.Database.Password = v
	}
	if v := getEnv("EHOME_DB_NAME", ""); v != "" {
		cfg.Database.DBName = v
	}
	if v := getEnv("LOG_LEVEL", ""); v != "" {
		cfg.Log.Level = v
	}
	if v := getEnv("EHOME_DEVICE_CONTROL_V2_ENABLED", ""); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.Control.DeviceControlV2Enabled = enabled
		}
	}
	if v := getEnv("EHOME_RAW_DIAGNOSTICS_ENABLED", ""); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.Control.RawDiagnosticsEnabled = enabled
		}
	}
	if v := getEnv("EHOME_ADMIN_USERNAME", ""); v != "" {
		cfg.AdminBootstrap.Username = v
	}
	if v := getEnv("EHOME_ADMIN_PASSWORD", ""); v != "" {
		cfg.AdminBootstrap.Password = v
	}
	if v := getEnv("EHOME_ADMIN_EMAIL", ""); v != "" {
		cfg.AdminBootstrap.Email = v
	}
	if v := getEnv("EHOME_DATA_RETENTION_DAYS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DataRetention.Days = n
		}
	}
	if v := getEnv("EHOME_PARSER_SHARDS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 64 {
			cfg.Ingest.ParserShards = n
		}
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Convenience accessors for backward compatibility
func (c *Config) APIAddr() string { return c.Server.Addr }
func (c *Config) DatabaseURL() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.Database.User, c.Database.Password, c.Database.Host,
		c.Database.Port, c.Database.DBName, c.Database.SSLMode)
}
func (c *Config) LogLevel() string             { return c.Log.Level }
func (c *Config) DBConfig() DatabaseConfig     { return c.Database }
func (c *Config) ControlConfig() ControlConfig { return c.Control }

// DataRetentionDays returns the system-level retention (days) snapshotted
// into newly created logical devices. Never returns <= 0.
func (c *Config) DataRetentionDays() int {
	if c.DataRetention.Days > 0 {
		return c.DataRetention.Days
	}
	return DefaultDataRetentionDays
}

// maxParserShards bounds the shard count from any source (env or YAML):
// each shard is a bus worker with its own mailbox and may hold a DB
// connection, so an absurd YAML value must not create thousands of workers.
const maxParserShards = 64

// ParserShards returns the heavy-consumer shard count, clamped to
// [1, maxParserShards]; 0/negative is normalized to 1 (legacy single
// consumer).
func (c *Config) ParserShards() int {
	if c.Ingest.ParserShards <= 0 {
		return 1
	}
	if c.Ingest.ParserShards > maxParserShards {
		return maxParserShards
	}
	return c.Ingest.ParserShards
}
