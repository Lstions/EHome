package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
	// FieldResolver 是"数据字段解析层"配置段（设计/数据字段解析层-方案-2026-10-21.md v0.2 §11）。
	// ⚠ 本仓**没有配置热加载**（Load 仅在启动时调用一次，无 fsnotify/viper）：
	// 所有 field_resolver 配置（含 enabled）变更需**重启进程**生效；灰度用
	// EHOME_FIELD_RESOLVER_ENABLED 环境变量 + 重启（方案 §11 明示，不新增 watcher）。
	FieldResolver FieldResolverConfig `yaml:"field_resolver"`
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


// FieldResolverConfig 是"数据字段解析层"（Field Resolution Layer）的配置段。
//
// 权威设计：docs/设计/数据字段解析层-方案-2026-10-21.md v0.2 §11。
// 生效方式：**改配置必须重启**（本仓无热加载机制，§11 明确不新增 watcher）。
//
// 默认值语义：零值字段一律在 FieldResolver() 访问器里回落到下面这些默认值，
// 因此 YAML 里只写需要偏离默认的键即可。
const (
	// DefaultFieldResolverObserveInterval 观测投影落库间隔（方案 §4.2：每 60s 批量 UPSERT）。
	DefaultFieldResolverObserveInterval = 60 * time.Second
	// DefaultFieldResolverSnapshotTTL 编译缓存兜底 TTL（方案 §4.1 Config.SnapshotTTL 默认 10m）。
	DefaultFieldResolverSnapshotTTL = 10 * time.Minute
	// DefaultFieldResolverMaxSelectorChars 选择器最大字符数（方案 §5.1：64 个 Unicode 字符）。
	DefaultFieldResolverMaxSelectorChars = 64
	// DefaultFieldResolverMaxFamilySize 单族展开上限（方案 §5.1/§11）。
	DefaultFieldResolverMaxFamilySize = 64
	// DefaultFieldResolverMaxVirtualDepth 派生嵌套深度上限（方案 §6.2：默认 4）。
	DefaultFieldResolverMaxVirtualDepth = 4
	// DefaultFieldResolverPreviewMaxSamples 试算取样上限（方案 §8.3：5000 行）。
	DefaultFieldResolverPreviewMaxSamples = 5000
	// DefaultFieldResolverNeverResolvedHours never_resolved 判定窗口（方案 §10：单位**小时**）。
	DefaultFieldResolverNeverResolvedHours = 24
)

// FieldResolverConfig 对应 YAML 的 field_resolver 段。
type FieldResolverConfig struct {
	// Enabled 是 kill switch（默认 false，方案 §11）。关闭并重启后：
	// 特殊 selector 规则不触发 + 落审计；纯字面量行为不变。**不自动降级为字面量**。
	Enabled bool `yaml:"enabled"`
	// AliasesFile 语义别名表路径（方案 §6.1 唯一默认表）。
	AliasesFile string `yaml:"aliases_file"`
	// VirtualFile 派生字段定义路径（方案 §6.2）。
	VirtualFile string `yaml:"virtual_file"`
	// ObserveInterval 观测投影落库间隔，接受 "60s"/"10m" 这类 Go duration 字面量。
	ObserveInterval Duration `yaml:"observe_interval"`
	// SnapshotTTL 编译缓存兜底 TTL。
	SnapshotTTL Duration `yaml:"snapshot_ttl"`
	// MaxSelectorChars / MaxFamilySize / MaxVirtualDepth 是解析层硬上限（方案 §5.1/§6.2）。
	MaxSelectorChars int `yaml:"max_selector_chars"`
	MaxFamilySize    int `yaml:"max_family_size"`
	MaxVirtualDepth  int `yaml:"max_virtual_depth"`
	// PreviewMaxSamples 试算端点（POST /sensor-fields/preview）取数上限（方案 §8.3）。
	PreviewMaxSamples int `yaml:"preview_max_samples"`
	// NeverResolvedHours "配错 selector 24h 内被标记" 的判定窗口，单位小时（方案 §10，v0.2 修正）。
	NeverResolvedHours int `yaml:"never_resolved_hours"`
}

// FieldResolverSettings 返回补齐默认值后的字段解析层配置。
//
// 为什么不把默认值直接写进 defaultConfig：YAML 里显式写 0 与"没写"目前无法区分
// （yaml.v3 没有指针字段的既有先例），把归一化放在访问器里可以保证任何来源
// （YAML / env / 测试手工构造）都不会得到 0 值导致除零或"无上限"。
func (c *Config) FieldResolverSettings() FieldResolverConfig {
	fr := c.FieldResolver
	if fr.ObserveInterval.Duration <= 0 {
		fr.ObserveInterval = Duration{Duration: DefaultFieldResolverObserveInterval}
	}
	if fr.SnapshotTTL.Duration <= 0 {
		fr.SnapshotTTL = Duration{Duration: DefaultFieldResolverSnapshotTTL}
	}
	if fr.MaxSelectorChars <= 0 {
		fr.MaxSelectorChars = DefaultFieldResolverMaxSelectorChars
	}
	if fr.MaxFamilySize <= 0 {
		fr.MaxFamilySize = DefaultFieldResolverMaxFamilySize
	}
	if fr.MaxVirtualDepth <= 0 {
		fr.MaxVirtualDepth = DefaultFieldResolverMaxVirtualDepth
	}
	if fr.PreviewMaxSamples <= 0 {
		fr.PreviewMaxSamples = DefaultFieldResolverPreviewMaxSamples
	}
	if fr.NeverResolvedHours <= 0 {
		fr.NeverResolvedHours = DefaultFieldResolverNeverResolvedHours
	}
	return fr
}

// Duration 是允许 YAML 写 Go duration 字面量（"60s" / "10m"）的配置类型。
//
// 为什么自定义而不是 time.Duration：yaml.v3 把 time.Duration 当整数纳秒解析，
// 设计文档 §11 的样例是 "60s"，直接用 time.Duration 会导致**配置加载失败**
// （cannot unmarshal !!str into time.Duration）。这里同时兼容整数（按秒解释，
// 与 read_timeout_sec 等既有字段的直觉一致），避免运维在两种写法上踩坑。
type Duration struct {
	time.Duration
}

// UnmarshalYAML 实现 yaml.Unmarshaler。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: duration must be a scalar (e.g. \"60s\")")
	}
	raw := strings.TrimSpace(value.Value)
	if raw == "" {
		return nil
	}
	if parsed, err := time.ParseDuration(raw); err == nil {
		d.Duration = parsed
		return nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		d.Duration = time.Duration(seconds) * time.Second
		return nil
	}
	return fmt.Errorf("config: invalid duration %q (want Go duration like \"60s\" or integer seconds)", raw)
}

// MarshalYAML 让打印/回写时保持可读字符串。
func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
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
		// 字段解析层默认**关闭**（kill switch 默认 off，方案 §11）：
		// 关闭时特殊 selector 一律拒绝、纯字面量行为与今天逐字节相同。
		FieldResolver: FieldResolverConfig{
			Enabled:            false,
			AliasesFile:        "config/field_aliases.yaml",
			VirtualFile:        "config/virtual_fields.yaml",
			ObserveInterval:    Duration{Duration: DefaultFieldResolverObserveInterval},
			SnapshotTTL:        Duration{Duration: DefaultFieldResolverSnapshotTTL},
			MaxSelectorChars:   DefaultFieldResolverMaxSelectorChars,
			MaxFamilySize:      DefaultFieldResolverMaxFamilySize,
			MaxVirtualDepth:    DefaultFieldResolverMaxVirtualDepth,
			PreviewMaxSamples:  DefaultFieldResolverPreviewMaxSamples,
			NeverResolvedHours: DefaultFieldResolverNeverResolvedHours,
		},
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
	// 3.0 设备监听（mTLS）的环境变量覆盖。
	//
	// 为什么需要（2026-10-11，仿真移植）：仿真 harness 刻意让 CONFIG_PATH 指向
	// 一个不存在的文件，以保证"子进程只用代码默认值 + 注入的环境变量"，
	// 从而杜绝仓库里的 config.yaml 影响仿真确定性。此前 device 段**只有 YAML**，
	// 于是 harness 无法启用设备监听 ⇒ 仿真设备没有可用传输。
	// 补上 env 覆盖后，harness 依旧不需要写 YAML，确定性前提不被破坏。
	if v := getEnv("EHOME_DEVICE_ENABLED", ""); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.Device.Enabled = enabled
		}
	}
	if v := getEnv("EHOME_DEVICE_ADDR", ""); v != "" {
		cfg.Device.Addr = v
	}
	if v := getEnv("EHOME_DEVICE_CERT_FILE", ""); v != "" {
		cfg.Device.CertFile = v
	}
	if v := getEnv("EHOME_DEVICE_KEY_FILE", ""); v != "" {
		cfg.Device.KeyFile = v
	}
	if v := getEnv("EHOME_DEVICE_CLIENT_CA_FILE", ""); v != "" {
		cfg.Device.ClientCAFile = v
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
	// 数据字段解析层 (§11: 灰度/回滚靠 env + 重启, 本仓无热加载)。
	//
	// 为什么每个标量都留 env 覆盖：kill switch 必须在**不改镜像内 YAML**的前提下
	// 可切换（回滚预案 §14.3 要求"关闭开关并重启"即可生效），而灰度环境往往
	// 只有 env 注入能力（docker/k8s）。布尔与时长解析失败时**保持原值**，
	// 与本文件既有 env 覆盖的静默容错口径一致。
	if v := getEnv("EHOME_FIELD_RESOLVER_ENABLED", ""); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.FieldResolver.Enabled = enabled
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_ALIASES_FILE", ""); v != "" {
		cfg.FieldResolver.AliasesFile = v
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_VIRTUAL_FILE", ""); v != "" {
		cfg.FieldResolver.VirtualFile = v
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_OBSERVE_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
			cfg.FieldResolver.ObserveInterval = Duration{Duration: d}
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_SNAPSHOT_TTL", ""); v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
			cfg.FieldResolver.SnapshotTTL = Duration{Duration: d}
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_MAX_SELECTOR_CHARS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FieldResolver.MaxSelectorChars = n
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_MAX_FAMILY_SIZE", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FieldResolver.MaxFamilySize = n
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_MAX_VIRTUAL_DEPTH", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FieldResolver.MaxVirtualDepth = n
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_PREVIEW_MAX_SAMPLES", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FieldResolver.PreviewMaxSamples = n
		}
	}
	if v := getEnv("EHOME_FIELD_RESOLVER_NEVER_RESOLVED_HOURS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FieldResolver.NeverResolvedHours = n
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
