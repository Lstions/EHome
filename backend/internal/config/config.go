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
	Ingest         IngestConfig         `yaml:"ingest"`
	DataRetention  DataRetentionConfig  `yaml:"data_retention"`
	AdminBootstrap AdminBootstrapConfig `yaml:"admin_bootstrap"`
	Device         DeviceConfig         `yaml:"device"`
	// FieldResolver 是"数据字段解析层"配置段（设计/数据字段解析层-方案-2026-10-21.md v0.2 §11）。
	// ⚠ 本仓**没有配置热加载**（Load 仅在启动时调用一次，无 fsnotify/viper）：
	// 配置变更需**重启进程**生效。2026-11-14 起**不再有 EHOME_FIELD_RESOLVER_* 环境变量**
	// （11 个），一律走本段的代码默认值 / config.yaml。
	FieldResolver FieldResolverConfig `yaml:"field_resolver"`
	// Telemetry 是遥测写入策略（设计/遥测存储优化方案-2026-11-06.md §3 L1）。
	// ⚠ 与 field_resolver 同理：本仓**没有配置热加载**，改配置必须重启。
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// TelemetryConfig 是 L1「值不变不入库」写入收窄策略（方案 §3.1；2026-11-07 起无条件生效）。
//
// 【没有开关】2026-11-07 用户裁决「完成并打开 L1、L2，同时删除开关」：
// write_on_change 与 rollup_write 两个布尔字段连同它们的环境变量一起**删除**，
// 不是「默认改成 true」。本结构体剩下的都是**参数**，不是开关：
//
//   - 某 (设备, 字段) 的值与「上次成功落库的值」相同、且距上次成功落库
//     不足 HeartbeatSeconds ⇒ 跳过 INSERT（内存缓存/告警/自动化/WS 照常更新）；
//   - 值变化 ⇒ 必写；距上次落库 ≥ HeartbeatSeconds ⇒ 强制写一帧（心跳，
//     保证时间轴连续、图表不空档）。
//
// 唯一可调的是心跳粒度。要临时停用收窄，只能回滚镜像 —— 这是有意的：
// 留一个 kill-switch 就等于留一条「线上行为随环境变量漂移」的路，
// 而读侧口径（avg 聚合 / 行计数）已经按 L1 生效改过（见 internal/api/history_points.go
// 的 D1、handler_overview.go 的 D2、handler_metrics.go 的 D3），开关一关读侧就先错了。
type TelemetryConfig struct {
	// HeartbeatSeconds 心跳间隔（秒）；<=0 回落默认 60，超过 24h 被钳制。
	// 它同时被 L2 复用为「关键帧间隔」（见 internal/telemetry 的 EnvKeyFrameSeconds）。
	HeartbeatSeconds int `yaml:"heartbeat_seconds"`
}

// DefaultTelemetryHeartbeatSeconds 是心跳默认值（方案 §3.2 实测口径：60s）。
const DefaultTelemetryHeartbeatSeconds = 60

// maxTelemetryHeartbeatSeconds 钳制心跳上界（24h）：心跳语义是「保证时间轴
// 不空档」，写死一个比保留期还大的值等价于关掉心跳，宁可钳制也不静默失效。
const maxTelemetryHeartbeatSeconds = 24 * 60 * 60

// Heartbeat 返回归一化后的心跳间隔：<=0 → 默认 60s；> 24h → 钳制到 24h。
func (t TelemetryConfig) Heartbeat() time.Duration {
	sec := t.HeartbeatSeconds
	if sec <= 0 {
		sec = DefaultTelemetryHeartbeatSeconds
	}
	if sec > maxTelemetryHeartbeatSeconds {
		sec = maxTelemetryHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// TelemetrySettings 返回补齐默认值后的遥测写入策略。
//
// 与 FieldResolverSettings 同一范式：归一化放在访问器里，保证任何来源
// （YAML / env / 测试手工构造）都不会得到 0 值心跳。
func (c *Config) TelemetrySettings() TelemetryConfig {
	t := c.Telemetry
	if t.HeartbeatSeconds <= 0 {
		t.HeartbeatSeconds = DefaultTelemetryHeartbeatSeconds
	}
	if t.HeartbeatSeconds > maxTelemetryHeartbeatSeconds {
		t.HeartbeatSeconds = maxTelemetryHeartbeatSeconds
	}
	return t
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
	// Enabled 表示「本次进程是否启动 3.0 mTLS 监听」。
	//
	// **它不是配置项**（yaml:"-"）：2026-11-14 起由「证书三件套是否齐全」推导
	// （见 Load）。删除了 EHOME_DEVICE_ENABLED 与 device.enabled：
	// 默认 false + 需要手设 true 的后果是「新部署节点永远离线且无报错」，
	// 而这恰恰是 11-13 复盘里真实发生过的静默失败。
	Enabled bool `yaml:"-"`

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
// 2026-11-14：不再由 enabled 开关决定是否校验 —— 开关已删，监听与否由「证书三件套
// 是否齐全」推导。校验规则变成：**完全没配 ⇒ 合法（不监听）；配了一部分 ⇒ 拒绝**。
// 后者是关键：有 cert/key 但没 CA 的「半配置 mTLS」会接受任意客户端，比不监听更危险，
// 而它恰好在「开关默认 false」时代会被静默跳过。
func (d DeviceConfig) Validate() error {
	if !d.Enabled && d.CertFile == "" && d.KeyFile == "" && d.ClientCAFile == "" {
		return nil // 完全没配：不监听（合法）
	}
	if d.Addr == "" {
		return fmt.Errorf("device.addr is required when device mTLS material is configured")
	}
	if d.CertFile == "" || d.KeyFile == "" {
		return fmt.Errorf("device.cert_file and device.key_file are required when device mTLS material is configured")
	}
	if d.ClientCAFile == "" {
		return fmt.Errorf("device.client_ca_file is required when device mTLS material is configured " +
			"(a device listener without client verification is not mTLS)")
	}
	return nil
}

// ============ ControlConfig 已删除（2026-11-14）============
//
// 原来的两个字段都是「急停阀」：DeviceControlV2Enabled（关掉分发器）与
// RawDiagnosticsEnabled（关掉 raw 诊断读写）。它们在生产的取值从未被切换过
// （默认 true / 生产 true），却让「功能是否可用」取决于环境变量。
//
// 现在：分发器**恒开**；raw 诊断的开关收敛成 api.ControlPolicy 的代码字面量
// （见 cmd/server/main.go）。要急停时改代码发版，而不是指望有人记得设变量。

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
	// Days 是系统级保留期（全库统一窗口）。**0 = 永久保留**（2026-11-07），
	// 负值在访问器里归一成默认 90（见 DataRetentionDays）。
	Days int `yaml:"days"` // EHOME_DATA_RETENTION_DAYS; 0 = 永久保留
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
		// 遥测写入收窄（L1）2026-11-07 起**无条件生效**，没有开关；
		// 这里只剩心跳参数（默认 60s，方案 §3.2 实测口径）。
		Telemetry: TelemetryConfig{
			HeartbeatSeconds: DefaultTelemetryHeartbeatSeconds,
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

	// 设备监听 = 「证书三件套齐全」。没有 enabled 开关，也就没有「忘了开」：
	// 部分配置（例如只给 cert/key 没给 CA）会被 Validate 拒绝 —— 半配置的 mTLS
	// 比不监听更危险。
	cfg.Device.Enabled = cfg.Device.CertFile != "" && cfg.Device.KeyFile != "" && cfg.Device.ClientCAFile != ""

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
	// 3.0 设备监听：这里只注入**连接材料**，启不启用由材料是否齐全推导（见 Load 末尾）。
	//
	// 2026-11-14 删除的三个开关：
	//   EHOME_DEVICE_ENABLED           默认 false ⇒ 新部署「节点永远离线且无报错」
	//   EHOME_DEVICE_CONTROL_V2_ENABLED 生产恒 true，从未被切换过
	//   EHOME_RAW_DIAGNOSTICS_ENABLED   生产恒 true，从未被切换过
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
	// EHOME_DATA_RETENTION_DAYS: 0 = **永久保留**（不自动删除数据），2026-11-07 用户裁决。
	// 负值仍然拒绝（更可能是写错，而不是「比永久更永久」）。
	if v := getEnv("EHOME_DATA_RETENTION_DAYS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.DataRetention.Days = n
		}
	}
	// ============ 2026-11-14 收敛：以下行为/调参不再接受环境变量覆盖 ============
	//
	// 一律写死为代码常量或 config.yaml 默认值。它们曾经唯一的实际作用，是让
	// 「生产值」与「代码默认值」悄悄分裂 —— 2026-11-14 的长窗事故就是生产 6h、
	// 代码 1h，重部署丢了变量即全站报错。
	//
	//	EHOME_PARSER_SHARDS                → DefaultParserShards (8)
	//	EHOME_TELEMETRY_HEARTBEAT_SECONDS  → DefaultTelemetryHeartbeatSeconds (60)
	//	EHOME_FIELD_RESOLVER_*（11 个）    → FieldResolverConfig 默认值 / config.yaml
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
func (c *Config) LogLevel() string         { return c.Log.Level }
func (c *Config) DBConfig() DatabaseConfig { return c.Database }

// DataRetentionDays returns the system-level retention window in days.
//
// **0 = 永久保留**（不自动删除数据），是合法配置值，不是「未配置」。
// 2026-11-07 用户裁决「所有数据要长期保留」（见
// docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §5）。
//
// 负值仍然归一成默认 90：零值有明确语义（永久），负值没有 —— 它只可能来自
// 配置写错（shell 变量没展开、减一过头），把它当「永久」会让一个笔误变成
// 「再也不清理」的静默事故，因此 fail-safe 到默认窗口。
//
// ⚠ 调用方必须区分 0 与「小窗口」：0 不能拿去算 cutoff（cutoff 会等于 now，
// 把所有数据判成过期）。用 datalifecycle.IsRetentionPermanent() 判断，
// 或直接依赖 datalifecycle.RunOnce 内部的永久判断。
func (c *Config) DataRetentionDays() int {
	if c.DataRetention.Days >= 0 {
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
