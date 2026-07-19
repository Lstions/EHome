package config

import (
	"os"
	"testing"
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
	if cfg.MQTT.Broker != "tcp://localhost:1883" {
		t.Errorf("default mqtt broker = %q, want tcp://localhost:1883", cfg.MQTT.Broker)
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("default redis addr = %q, want localhost:6379", cfg.Redis.Addr)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default log level = %q, want info", cfg.Log.Level)
	}
	if cfg.Control.LegacyDeviceWriteMode != "disabled" || cfg.Control.RawDiagnosticsEnabled {
		t.Fatalf("unsafe control defaults: %+v", cfg.Control)
	}
}

func TestLoadDefaults(t *testing.T) {
	// Ensure no config.yaml exists and no env vars set
	os.Unsetenv("CONFIG_PATH")
	os.Unsetenv("EHOME_SERVER_ADDR")
	os.Unsetenv("EHOME_DB_HOST")
	os.Unsetenv("MQTT_BROKER")
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
	os.Setenv("MQTT_BROKER", "tcp://mqtt:1883")
	os.Setenv("MQTT_USER", "mqttuser")
	os.Setenv("MQTT_PASSWORD", "mqttpass")
	os.Setenv("REDIS_ADDR", "redis:6380")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("EHOME_ADMIN_USERNAME", "compose-admin")
	os.Setenv("EHOME_ADMIN_PASSWORD", "compose-password-123")
	os.Setenv("EHOME_ADMIN_EMAIL", "admin@example.test")
	defer func() {
		os.Unsetenv("EHOME_SERVER_ADDR")
		os.Unsetenv("EHOME_DB_HOST")
		os.Unsetenv("EHOME_DB_PORT")
		os.Unsetenv("EHOME_DB_USER")
		os.Unsetenv("EHOME_DB_PASSWORD")
		os.Unsetenv("EHOME_DB_NAME")
		os.Unsetenv("MQTT_BROKER")
		os.Unsetenv("MQTT_USER")
		os.Unsetenv("MQTT_PASSWORD")
		os.Unsetenv("REDIS_ADDR")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("EHOME_ADMIN_USERNAME")
		os.Unsetenv("EHOME_ADMIN_PASSWORD")
		os.Unsetenv("EHOME_ADMIN_EMAIL")
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
	if cfg.MQTT.Broker != "tcp://mqtt:1883" {
		t.Errorf("expected tcp://mqtt:1883, got %q", cfg.MQTT.Broker)
	}
	if cfg.MQTT.User != "mqttuser" {
		t.Errorf("expected mqttuser, got %q", cfg.MQTT.User)
	}
	if cfg.MQTT.Password != "mqttpass" {
		t.Errorf("expected mqttpass, got %q", cfg.MQTT.Password)
	}
	if cfg.Redis.Addr != "redis:6380" {
		t.Errorf("expected redis:6380, got %q", cfg.Redis.Addr)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("expected debug, got %q", cfg.Log.Level)
	}
	if cfg.AdminBootstrap.Username != "compose-admin" || cfg.AdminBootstrap.Password != "compose-password-123" || cfg.AdminBootstrap.Email != "admin@example.test" {
		t.Errorf("unexpected admin bootstrap config: %+v", cfg.AdminBootstrap)
	}
}

func TestConvenienceAccessors(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Addr: ":9090"},
		Database: DatabaseConfig{Host: "h", Port: 5432, User: "u", Password: "p", DBName: "d", SSLMode: "disable"},
		MQTT:     MQTTConfig{Broker: "tcp://m:1883", User: "mu", Password: "mp"},
		Redis:    RedisConfig{Addr: "r:6379"},
		Log:      LogConfig{Level: "warn"},
	}

	if cfg.APIAddr() != ":9090" {
		t.Errorf("APIAddr = %q, want :9090", cfg.APIAddr())
	}
	if cfg.MQTTBroker() != "tcp://m:1883" {
		t.Errorf("MQTTBroker = %q", cfg.MQTTBroker())
	}
	if cfg.MQTTUser() != "mu" {
		t.Errorf("MQTTUser = %q", cfg.MQTTUser())
	}
	if cfg.MQTTPassword() != "mp" {
		t.Errorf("MQTTPassword = %q", cfg.MQTTPassword())
	}
	if cfg.RedisAddr() != "r:6379" {
		t.Errorf("RedisAddr = %q", cfg.RedisAddr())
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
	os.Unsetenv("MQTT_BROKER")
	defer os.Unsetenv("EHOME_SERVER_ADDR")

	cfg := Load()
	if cfg.Server.Addr != ":7777" {
		t.Errorf("expected :7777, got %q", cfg.Server.Addr)
	}
	if cfg.Database.Host != "localhost" {
		t.Errorf("expected default localhost, got %q", cfg.Database.Host)
	}
}

func TestControlEnvOverridesAreBounded(t *testing.T) {
	t.Setenv("EHOME_LEGACY_DEVICE_WRITE_MODE", "bridge")
	t.Setenv("EHOME_RAW_DIAGNOSTICS_ENABLED", "true")
	t.Setenv("EHOME_DEVICE_CONTROL_V2_ENABLED", "true")
	t.Setenv("EHOME_ENABLED_DEVICE_ACTIONS", " prs3001/read_rainfall , techfine_inverter/set_mode ")
	cfg := Load()
	if cfg.Control.LegacyDeviceWriteMode != "bridge" || !cfg.Control.RawDiagnosticsEnabled || !cfg.Control.DeviceControlV2Enabled || len(cfg.Control.EnabledDeviceActions) != 2 || cfg.Control.EnabledDeviceActions[0] != "prs3001/read_rainfall" {
		t.Fatalf("control env override = %+v", cfg.Control)
	}

	t.Setenv("EHOME_LEGACY_DEVICE_WRITE_MODE", "direct")
	t.Setenv("EHOME_RAW_DIAGNOSTICS_ENABLED", "not-a-bool")
	t.Setenv("EHOME_DEVICE_CONTROL_V2_ENABLED", "not-a-bool")
	cfg = Load()
	if cfg.Control.LegacyDeviceWriteMode != "disabled" || cfg.Control.RawDiagnosticsEnabled || cfg.Control.DeviceControlV2Enabled {
		t.Fatalf("invalid control env was accepted: %+v", cfg.Control)
	}
}
