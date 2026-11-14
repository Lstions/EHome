package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestFieldResolverSettings_Defaults 覆盖 §11 默认值归一（零值字段回落，不产生 0 上限）。
func TestFieldResolverSettings_Defaults(t *testing.T) {
	cfg := defaultConfig()
	fr := cfg.FieldResolverSettings()
	if fr.Enabled {
		t.Fatalf("field_resolver.enabled must default to false (kill switch off, §11)")
	}
	if fr.ObserveInterval.Duration != DefaultFieldResolverObserveInterval {
		t.Fatalf("observe_interval = %v want %v", fr.ObserveInterval.Duration, DefaultFieldResolverObserveInterval)
	}
	if fr.SnapshotTTL.Duration != DefaultFieldResolverSnapshotTTL {
		t.Fatalf("snapshot_ttl = %v want %v", fr.SnapshotTTL.Duration, DefaultFieldResolverSnapshotTTL)
	}
	if fr.MaxSelectorChars != 64 || fr.MaxFamilySize != 64 || fr.MaxVirtualDepth != 4 {
		t.Fatalf("limits = %d/%d/%d want 64/64/4", fr.MaxSelectorChars, fr.MaxFamilySize, fr.MaxVirtualDepth)
	}
	if fr.PreviewMaxSamples != 5000 {
		t.Fatalf("preview_max_samples = %d want 5000", fr.PreviewMaxSamples)
	}
	if fr.NeverResolvedHours != 24 {
		t.Fatalf("never_resolved_hours = %d want 24", fr.NeverResolvedHours)
	}
}

// TestFieldResolverSettings_ZeroValueFallsBack 手工零值结构也必须回落默认（防除零/无上限）。
func TestFieldResolverSettings_ZeroValueFallsBack(t *testing.T) {
	cfg := &Config{}
	fr := cfg.FieldResolverSettings()
	if fr.ObserveInterval.Duration != 60*time.Second || fr.MaxSelectorChars != 64 || fr.PreviewMaxSamples != 5000 {
		t.Fatalf("zero-value config not normalized: %+v", fr)
	}
}

// TestFieldResolverConfig_EnvIsIgnored 守「字段解析层不再有环境变量」（2026-11-14）。
//
// 原 11 个 EHOME_FIELD_RESOLVER_* 已删除：配置只来自 config.yaml / 代码默认值。
// 灰度回滚改为改配置或改代码 —— 一个「灰度时能用、平时没人看」的 env 面，
// 正是 2026-11-14 长窗事故的同款风险。
func TestFieldResolverConfig_EnvIsIgnored(t *testing.T) {
	t.Setenv("EHOME_FIELD_RESOLVER_ENABLED", "true")
	t.Setenv("EHOME_FIELD_RESOLVER_ALIASES_FILE", "/etc/ehome/aliases.yaml")
	t.Setenv("EHOME_FIELD_RESOLVER_OBSERVE_INTERVAL", "15s")
	t.Setenv("EHOME_FIELD_RESOLVER_MAX_SELECTOR_CHARS", "48")
	cfg := Load()
	fr := cfg.FieldResolverSettings()
	if fr.Enabled {
		t.Fatalf("env 不应再打开字段解析层（默认关闭）")
	}
	if fr.AliasesFile == "/etc/ehome/aliases.yaml" {
		t.Fatalf("env 不应再覆盖 aliases_file: %q", fr.AliasesFile)
	}
	if fr.ObserveInterval.Duration != DefaultFieldResolverObserveInterval {
		t.Fatalf("env 不应再覆盖 observe_interval: %v", fr.ObserveInterval.Duration)
	}
}

// TestDuration_YAMLStringAndInteger 设计 §11 的 YAML 样例是 "60s" 字符串；
// 同时兼容整数（按秒），否则一份自洽的配置文件会导致启动失败。
func TestDuration_YAMLStringAndInteger(t *testing.T) {
	var fromString struct {
		Interval Duration `yaml:"interval"`
	}
	if err := yaml.Unmarshal([]byte("interval: 90s"), &fromString); err != nil {
		t.Fatalf("unmarshal 90s: %v", err)
	}
	if fromString.Interval.Duration != 90*time.Second {
		t.Fatalf("90s parsed as %v", fromString.Interval.Duration)
	}

	var fromInt struct {
		Interval Duration `yaml:"interval"`
	}
	if err := yaml.Unmarshal([]byte("interval: 30"), &fromInt); err != nil {
		t.Fatalf("unmarshal 30: %v", err)
	}
	if fromInt.Interval.Duration != 30*time.Second {
		t.Fatalf("integer seconds parsed as %v", fromInt.Interval.Duration)
	}

	var bad struct {
		Interval Duration `yaml:"interval"`
	}
	if err := yaml.Unmarshal([]byte("interval: soon"), &bad); err == nil {
		t.Fatalf("invalid duration must fail to unmarshal")
	}
}
