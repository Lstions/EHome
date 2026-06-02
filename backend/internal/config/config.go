package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds all server configuration
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	MQTT     MQTTConfig     `yaml:"mqtt"`
	Redis    RedisConfig    `yaml:"redis"`
	Log      LogConfig      `yaml:"log"`
}

// ServerConfig holds HTTP server settings
type ServerConfig struct {
	Addr string `yaml:"addr"`
}

// DatabaseConfig holds PostgreSQL settings
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// MQTTConfig holds MQTT broker settings
type MQTTConfig struct {
	Broker   string `yaml:"broker"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// RedisConfig holds Redis settings
type RedisConfig struct {
	Addr string `yaml:"addr"`
}

// LogConfig holds logging settings
type LogConfig struct {
	Level string `yaml:"level"`
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
		MQTT: MQTTConfig{
			Broker: "tcp://localhost:1883",
		},
		Redis: RedisConfig{
			Addr: "localhost:6379",
		},
		Log: LogConfig{
			Level: "info",
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
	if v := getEnv("MQTT_BROKER", ""); v != "" {
		cfg.MQTT.Broker = v
	}
	if v := getEnv("MQTT_USER", ""); v != "" {
		cfg.MQTT.User = v
	}
	if v := getEnv("MQTT_PASSWORD", ""); v != "" {
		cfg.MQTT.Password = v
	}
	if v := getEnv("REDIS_ADDR", ""); v != "" {
		cfg.Redis.Addr = v
	}
	if v := getEnv("LOG_LEVEL", ""); v != "" {
		cfg.Log.Level = v
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Convenience accessors for backward compatibility
func (c *Config) MQTTBroker() string   { return c.MQTT.Broker }
func (c *Config) MQTTUser() string     { return c.MQTT.User }
func (c *Config) MQTTPassword() string { return c.MQTT.Password }
func (c *Config) APIAddr() string      { return c.Server.Addr }
func (c *Config) RedisAddr() string    { return c.Redis.Addr }
func (c *Config) DatabaseURL() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.Database.User, c.Database.Password, c.Database.Host,
		c.Database.Port, c.Database.DBName, c.Database.SSLMode)
}
func (c *Config) LogLevel() string { return c.Log.Level }
func (c *Config) DBConfig() DatabaseConfig { return c.Database }