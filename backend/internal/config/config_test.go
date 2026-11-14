package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg == nil {
		t.Fatal("expected non-nil default config")
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default server addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Database.Host != "localhost" {
		t.Errorf("default db host = %q, want localhost", cfg.Database.Host)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("default db port = %d, want 5432", cfg.Database.Port)
	}
	if cfg.Database.User != "ehome" {
		t.Errorf("default db user = %q, want ehome", cfg.Database.User)
	}
	if cfg.Database.DBName != "ehome" {
		t.Errorf("default db name = %q, want ehome", cfg.Database.DBName)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default log level = %q, want info", cfg.Log.Level)
	}
	// 2026-11-14：Control 段已删除（分发器恒开、raw 诊断策略收敛为代码字面量）。
	// 这里改为守「没有证书材料时默认不监听」——正是 EHOME_DEVICE_ENABLED 曾经的角色。
	if cfg.Device.Enabled {
		t.Fatalf("默认不应启动设备监听（无证书材料），got %+v", cfg.Device)
	}
}

func TestLoadDefaults(t *testing.T) {
	// Ensure no config.yaml exists and no env vars set
	os.Unsetenv("CONFIG_PATH")
	os.Unsetenv("EHOME_SERVER_ADDR")
	os.Unsetenv("EHOME_DB_HOST")
	os.Unsetenv("REDIS_ADDR")
	os.Unsetenv("LOG_LEVEL")

	cfg := Load()
	if cfg.Server.Addr != ":8080" {
		t.Errorf("expected default :8080, got %q", cfg.Server.Addr)
	}
}

func TestLoadWithEnvOverride(t *testing.T) {
	os.Setenv("EHOME_SERVER_ADDR", ":9999")
	os.Setenv("EHOME_DB_HOST", "dbhost")
	os.Setenv("EHOME_DB_PORT", "5433")
	os.Setenv("EHOME_DB_USER", "testuser")
	os.Setenv("EHOME_DB_PASSWORD", "testpass")
	os.Setenv("EHOME_DB_NAME", "testdb")
	os.Setenv("LOG_LEVEL", "debug")
	defer func() {
		os.Unsetenv("EHOME_SERVER_ADDR")
		os.Unsetenv("EHOME_DB_HOST")
		os.Unsetenv("EHOME_DB_PORT")
		os.Unsetenv("EHOME_DB_USER")
		os.Unsetenv("EHOME_DB_PASSWORD")
		os.Unsetenv("EHOME_DB_NAME")
		os.Unsetenv("LOG_LEVEL")
	}()

	cfg := Load()
	if cfg.Server.Addr != ":9999" {
		t.Errorf("expected :9999, got %q", cfg.Server.Addr)
	}
	if cfg.Database.Host != "dbhost" {
		t.Errorf("expected dbhost, got %q", cfg.Database.Host)
	}
	if cfg.Database.Port != 5433 {
		t.Errorf("expected 5433, got %d", cfg.Database.Port)
	}
	if cfg.Database.User != "testuser" {
		t.Errorf("expected testuser, got %q", cfg.Database.User)
	}
	if cfg.Database.Password != "testpass" {
		t.Errorf("expected testpass, got %q", cfg.Database.Password)
	}
	if cfg.Database.DBName != "testdb" {
		t.Errorf("expected testdb, got %q", cfg.Database.DBName)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("expected debug, got %q", cfg.Log.Level)
	}
}

func TestConvenienceAccessors(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Addr: ":9090"},
		Database: DatabaseConfig{Host: "h", Port: 5432, User: "u", Password: "p", DBName: "d", SSLMode: "disable"},
		Log:      LogConfig{Level: "warn"},
	}

	if cfg.APIAddr() != ":9090" {
		t.Errorf("APIAddr = %q, want :9090", cfg.APIAddr())
	}
	if cfg.LogLevel() != "warn" {
		t.Errorf("LogLevel = %q, want warn", cfg.LogLevel())
	}
	if cfg.DBConfig().Host != "h" {
		t.Errorf("DBConfig().Host = %q, want h", cfg.DBConfig().Host)
	}
}

func TestDatabaseURL(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "ehome",
			Password: "secret",
			DBName:   "ehome",
			SSLMode:  "disable",
		},
	}
	expected := "postgres://ehome:secret@localhost:5432/ehome?sslmode=disable"
	if cfg.DatabaseURL() != expected {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL(), expected)
	}
}

func TestLoadWithInvalidConfigPath(t *testing.T) {
	os.Setenv("CONFIG_PATH", "/nonexistent/path/config.yaml")
	os.Unsetenv("EHOME_SERVER_ADDR")
	defer os.Unsetenv("CONFIG_PATH")

	cfg := Load()
	// Should fall back to defaults
	if cfg.Server.Addr != ":8080" {
		t.Errorf("expected default :8080 with invalid config path, got %q", cfg.Server.Addr)
	}
}

func TestEnvPartialOverride(t *testing.T) {
	// Only set some env vars, others should remain default
	os.Setenv("EHOME_SERVER_ADDR", ":7777")
	os.Unsetenv("EHOME_DB_HOST")
	defer os.Unsetenv("EHOME_SERVER_ADDR")

	cfg := Load()
	if cfg.Server.Addr != ":7777" {
		t.Errorf("expected :7777, got %q", cfg.Server.Addr)
	}
	if cfg.Database.Host != "localhost" {
		t.Errorf("expected default localhost, got %q", cfg.Database.Host)
	}
}

// TestControlEnvsAreGone 守「控制面不再有环境开关」（2026-11-14）。
//
// EHOME_RAW_DIAGNOSTICS_ENABLED / EHOME_DEVICE_CONTROL_V2_ENABLED 已删除：
// 分发器恒开（急停 = 改代码发版），raw 诊断策略收敛为 api.ControlPolicy 的代码字面量。
// 显式设上旧变量名，断言它们既不生效、也不会让 Load() 失败。
func TestControlEnvsAreGone(t *testing.T) {
	t.Setenv("EHOME_RAW_DIAGNOSTICS_ENABLED", "true")
	t.Setenv("EHOME_DEVICE_CONTROL_V2_ENABLED", "false")
	cfg := Load()
	if cfg.Device.Enabled {
		t.Fatalf("设备监听不应受已删除的开关影响（应由证书推导）：%+v", cfg.Device)
	}
}

func TestDataRetentionDays(t *testing.T) {
	// 默认 90
	t.Setenv("EHOME_DATA_RETENTION_DAYS", "")
	cfg := Load()
	if got := cfg.DataRetentionDays(); got != DefaultDataRetentionDays {
		t.Errorf("expected default %d, got %d", DefaultDataRetentionDays, got)
	}

	// 环境变量覆盖
	t.Setenv("EHOME_DATA_RETENTION_DAYS", "30")
	cfg = Load()
	if got := cfg.DataRetentionDays(); got != 30 {
		t.Errorf("expected 30 from env, got %d", got)
	}

	// 0 = 永久保留（2026-11-07 用户裁决「所有数据要长期保留」）。
	// 改造前 0 被当成非法值回落 90，而 datalifecycle 又把它兜底成 1 天 ——
	// 「设 0 想永久保留」反而删得更狠，是本轮修掉的反向 bug。
	t.Setenv("EHOME_DATA_RETENTION_DAYS", "0")
	cfg = Load()
	if got := cfg.DataRetentionDays(); got != 0 {
		t.Errorf("EHOME_DATA_RETENTION_DAYS=0 应为永久保留（DataRetentionDays()=0）, got %d", got)
	}

	// 非法值 (非数字 / 负数) 被拒绝, 回落默认。
	// 负数单独留在外面: 它**不是**「比永久更永久」，而是配置写错（shell 变量没展开、
	// 减一过头），把笔误变成「再也不清理」的静默事故比回落默认危险得多。
	for _, bad := range []string{"not-a-number", "-5"} {
		t.Setenv("EHOME_DATA_RETENTION_DAYS", bad)
		cfg = Load()
		if got := cfg.DataRetentionDays(); got != DefaultDataRetentionDays {
			t.Errorf("invalid %q accepted as %d, want default %d", bad, got, DefaultDataRetentionDays)
		}
	}

	// 访问器对负值也归一（对方可能绕过 env 直接构造 Config）。
	raw := &Config{DataRetention: DataRetentionConfig{Days: -1}}
	if got := raw.DataRetentionDays(); got != DefaultDataRetentionDays {
		t.Errorf("负值配置 = %d, want default %d", got, DefaultDataRetentionDays)
	}
	// 零值配置则是合法的「永久」（不能因为「没填」就当成永久 —— 但也不能拒绝 0）。
	zero := &Config{DataRetention: DataRetentionConfig{Days: 0}}
	if got := zero.DataRetentionDays(); got != 0 {
		t.Errorf("零值配置 = %d, want 0（0 = 永久保留是合法值）", got)
	}
}

// TestParserShards_Default verifies the sharded-ingest default.
func TestParserShards_Default(t *testing.T) {
	cfg := Load()
	if got := cfg.ParserShards(); got != DefaultParserShards {
		t.Errorf("default ParserShards = %d, want %d", got, DefaultParserShards)
	}
}

// TestParserShards_EnvIsIgnored 守「分片数不可配」（2026-11-14）。
//
// EHOME_PARSER_SHARDS 已删除：DefaultParserShards(8) 是代码常量。分片数是
// 吞吐参数而非环境事实，可调只会让不同部署的并发形状悄悄不同。
func TestParserShards_EnvIsIgnored(t *testing.T) {
	for _, v := range []string{"1", "64", "0", "garbage"} {
		t.Setenv("EHOME_PARSER_SHARDS", v)
		if got := Load().ParserShards(); got != DefaultParserShards {
			t.Errorf("env %q 不应再生效：ParserShards = %d, want %d", v, got, DefaultParserShards)
		}
	}
}

// TestParserShards_ClampUpperBound verifies that absurd values (env and
// YAML-sourced alike) are clamped by ParserShards().
func TestParserShards_ClampUpperBound(t *testing.T) {
	cfg := Load()
	cfg.Ingest.ParserShards = 100000
	if got := cfg.ParserShards(); got != maxParserShards {
		t.Errorf("clamped ParserShards = %d, want %d", got, maxParserShards)
	}
	cfg.Ingest.ParserShards = -1
	if got := cfg.ParserShards(); got != 1 {
		t.Errorf("negative ParserShards = %d, want 1", got)
	}
}

// TestLoadDeviceListenerFromEnv 守 3.0 设备监听的环境变量覆盖。
//
// 为什么需要（2026-10-11，仿真移植）：仿真 harness 刻意让 CONFIG_PATH 指向一个
// **不存在**的文件（保证子进程只用默认值 + 注入的环境变量）。而 device 段原本
// 只能走 YAML ⇒ harness 无法启用设备监听 ⇒ 仿真设备没有可用传输。
// 补上 env 覆盖后，这条测试钉住"五个变量都能生效"，避免将来有人只删掉其中一个。
// TestLoadDeviceListenerFromCerts 守「监听由证书三件套推导，没有 enabled 开关」。
//
// 2026-11-14 删除 EHOME_DEVICE_ENABLED：默认 false + 需要手设 true 的后果是
// 新部署「节点永远离线且无报错」。现在三件套齐全就监听，少一件就不监听
// （半配置的 mTLS 由 Validate 拒绝）。
func TestLoadDeviceListenerFromCerts(t *testing.T) {
	os.Setenv("CONFIG_PATH", "/nonexistent/sim-no-config.yaml")
	os.Setenv("EHOME_DEVICE_ADDR", "127.0.0.1:18443")
	os.Setenv("EHOME_DEVICE_CERT_FILE", "/tmp/x/server.crt")
	os.Setenv("EHOME_DEVICE_KEY_FILE", "/tmp/x/server.key")
	os.Setenv("EHOME_DEVICE_CLIENT_CA_FILE", "/tmp/x/ca.crt")
	defer func() {
		for _, k := range []string{"CONFIG_PATH", "EHOME_DEVICE_ADDR",
			"EHOME_DEVICE_CERT_FILE", "EHOME_DEVICE_KEY_FILE", "EHOME_DEVICE_CLIENT_CA_FILE"} {
			os.Unsetenv(k)
		}
	}()

	cfg := Load()
	if !cfg.Device.Enabled {
		t.Errorf("证书三件套齐全时必须监听（不再有 EHOME_DEVICE_ENABLED）")
	}
	if cfg.Device.Addr != "127.0.0.1:18443" {
		t.Errorf("Addr = %q, want 127.0.0.1:18443", cfg.Device.Addr)
	}
	if cfg.Device.CertFile != "/tmp/x/server.crt" {
		t.Errorf("CertFile = %q", cfg.Device.CertFile)
	}
	if cfg.Device.KeyFile != "/tmp/x/server.key" {
		t.Errorf("KeyFile = %q", cfg.Device.KeyFile)
	}
	if cfg.Device.ClientCAFile != "/tmp/x/ca.crt" {
		t.Errorf("ClientCAFile = %q", cfg.Device.ClientCAFile)
	}
	// 少一件证书 ⇒ 不监听。半配置的 mTLS（有 cert/key 但没 CA）比不监听更危险。
	os.Unsetenv("EHOME_DEVICE_CLIENT_CA_FILE")
	if Load().Device.Enabled {
		t.Errorf("缺少 client CA 时设备监听不应开启")
	}
}

// ==================== 遥测心跳参数配置门禁 ====================
//
// 出处: docs/设计/遥测存储优化方案-2026-11-06.md §3.1 / §7 阶段1；
//       docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §3.1。
//
// 2026-11-07 起 L1 收窄**无条件生效**；2026-11-14 起连参数也不再可配
// （EHOME_TELEMETRY_HEARTBEAT_SECONDS 已删除）。因此这里只剩两条守则:
//  1. 默认 60s，且 Heartbeat() 归一化后与之一致;
//  2. 心跳永远归一化到 (0, 24h] —— 0 / 负数 / 荒谬大值都不得让心跳静默失效。
//
// 「开关/参数确实没了」由两处源码级门禁守：
// internal/config 的 TestProductionEnvSurfaceIsWhitelisted（白名单），
// 与本文件里 TestTelemetryHeartbeatEnvIsIgnored（旧变量不再生效）。

func TestTelemetryDefaultsToHeartbeat60s(t *testing.T) {
	// 刻意清空变量: 环境里残留的覆盖会让「默认 60s」的断言失去意义。
	t.Setenv("EHOME_TELEMETRY_HEARTBEAT_SECONDS", "")
	cfg := Load()
	if got := cfg.Telemetry.HeartbeatSeconds; got != DefaultTelemetryHeartbeatSeconds {
		t.Errorf("默认心跳 = %d, want %d", got, DefaultTelemetryHeartbeatSeconds)
	}
	if got := cfg.Telemetry.Heartbeat(); got != DefaultTelemetryHeartbeatSeconds*time.Second {
		t.Errorf("Heartbeat() = %v, want %ds", got, DefaultTelemetryHeartbeatSeconds)
	}
}

// TestTelemetryHeartbeatEnvIsIgnored 守「心跳不可配」（2026-11-14）。
//
// EHOME_TELEMETRY_HEARTBEAT_SECONDS 已删除、常量 60s。这里显式设上旧变量名并
// 断言它不再有任何作用 —— 参数可调在实践中等于「没人调，但生产值与代码默认值
// 可能分裂」，2026-11-14 的长窗事故就是这种分裂。
func TestTelemetryHeartbeatEnvIsIgnored(t *testing.T) {
	t.Setenv("EHOME_TELEMETRY_HEARTBEAT_SECONDS", "120")
	if got := Load().Telemetry.HeartbeatSeconds; got != DefaultTelemetryHeartbeatSeconds {
		t.Errorf("环境变量不应再生效：心跳 = %d, want %d", got, DefaultTelemetryHeartbeatSeconds)
	}
}

// TestTelemetryHeartbeatClamped 守上界钳制: 心跳不得大于 24h。
func TestTelemetryHeartbeatClamped(t *testing.T) {
	cfg := &Config{Telemetry: TelemetryConfig{HeartbeatSeconds: 99999999}}
	if got := cfg.Telemetry.Heartbeat(); got != maxTelemetryHeartbeatSeconds*time.Second {
		t.Errorf("心跳钳制 = %v, want %v", got, maxTelemetryHeartbeatSeconds*time.Second)
	}
	// YAML / 手工构造的零值同样归一（访问器兜底，不依赖 defaultConfig 是否被调用）。
	raw := &Config{}
	if got := raw.TelemetrySettings().HeartbeatSeconds; got != DefaultTelemetryHeartbeatSeconds {
		t.Errorf("零值配置心跳 = %d, want %d", got, DefaultTelemetryHeartbeatSeconds)
	}
}
