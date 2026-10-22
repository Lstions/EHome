package sensorfield

import (
	"errors"
	"strings"
	"testing"
)

// TestSelectorBoundaryTable 覆盖方案 §5.1 的全部 15 条边界（门禁 #8 的测试入口）。
func TestSelectorBoundaryTable(t *testing.T) {
	cfg := enabledConfig()
	long64 := "a" + strings.Repeat("b", 63)
	long65 := "a" + strings.Repeat("b", 64)
	familyLong := "temperature_" + strings.Repeat("x", 60) + "_*" // > 64
	tests := []struct {
		name     string
		in       string
		wantErr  error
		wantKind Kind
		check    func(t *testing.T, ps parsedSelector)
	}{
		// --- §5.1 边界 1-15 ---
		{name: "01_star_only", in: "*", wantErr: ErrInvalidSyntax},
		{name: "02_underscore_colon", in: "_:x", wantErr: ErrInvalidSyntax},
		{name: "03_empty_aggregate", in: "temperature_*:", wantErr: ErrInvalidSyntax},
		{name: "04_at_empty", in: "@", wantErr: ErrInvalidSyntax},
		{name: "05_virtual_empty", in: "virtual:", wantErr: ErrInvalidSyntax},
		{name: "06_multi_aggregate", in: "a_*:any:max", wantErr: ErrInvalidSyntax},
		{name: "07_aggregate_upper", in: "temperature_*:MAX", wantErr: ErrUnsupportedAggregate},
		{name: "08_non_ascii", in: "温度_*", wantErr: ErrInvalidSyntax},
		{name: "09a_empty", in: "", wantErr: ErrEmptySelector},
		{name: "09b_blank", in: "   ", wantErr: ErrEmptySelector},
		{name: "10_literal64_ok", in: long64, wantKind: KindLiteral},
		{name: "11_literal65_too_long", in: long65, wantErr: ErrSelectorTooLong},
		{name: "12_family_trailing_space", in: "temperature_*:max ", wantKind: KindFamily, check: func(t *testing.T, ps parsedSelector) {
			if ps.aggregate != AggMax {
				t.Fatalf("aggregate=%q want max", ps.aggregate)
			}
		}},
		{name: "13_virtual_is_literal", in: "virtual", wantKind: KindLiteral, check: func(t *testing.T, ps parsedSelector) {
			if ps.literal != "virtual" {
				t.Fatalf("literal=%q", ps.literal)
			}
		}},
		{name: "14_indexed_voltage_is_literal", in: "cell_voltage_10", wantKind: KindLiteral},
		{name: "15_unknown_semantic_is_syntactically_valid", in: "@unknown", wantKind: KindSemantic, check: func(t *testing.T, ps parsedSelector) {
			if ps.semantic != "unknown" {
				t.Fatalf("semantic=%q", ps.semantic)
			}
		}},

		// --- 补充：大小写敏感 / 字面量 / 族 / 派生 / 非 ASCII / 前缀规则 ---
		{name: "literal_ok", in: "rsoc", wantKind: KindLiteral},
		{name: "family_default_any", in: "temperature_*", wantKind: KindFamily, check: func(t *testing.T, ps parsedSelector) {
			if ps.aggregate != AggAny || ps.aggExplicit {
				t.Fatalf("aggregate=%q explicit=%v", ps.aggregate, ps.aggExplicit)
			}
		}},
		{name: "family_explicit_sum", in: "temperature_*:sum", wantKind: KindFamily, check: func(t *testing.T, ps parsedSelector) {
			if ps.aggregate != AggSum || !ps.aggExplicit {
				t.Fatalf("aggregate=%q explicit=%v", ps.aggregate, ps.aggExplicit)
			}
		}},
		{name: "virtual_ok", in: "virtual:dc_power", wantKind: KindVirtual, check: func(t *testing.T, ps parsedSelector) {
			if ps.virtual != "dc_power" {
				t.Fatalf("virtual=%q", ps.virtual)
			}
		}},
		{name: "literal_upper_invalid", in: "RSOC", wantErr: ErrInvalidSyntax},
		{name: "semantic_upper_invalid", in: "@SOC", wantErr: ErrInvalidSyntax},
		{name: "virtual_prefix_case_sensitive", in: "Virtual:dc_power", wantErr: ErrInvalidSyntax},
		{name: "family_median_unsupported", in: "temperature_*:median", wantErr: ErrUnsupportedAggregate},
		{name: "family_mixed_case_unsupported", in: "temperature_*:Max", wantErr: ErrUnsupportedAggregate},
		{name: "family_space_in_aggregate", in: "temperature_*: max", wantErr: ErrInvalidSyntax},
		{name: "family_prefix_trailing_underscore_invalid", in: "a__*", wantErr: ErrInvalidSyntax},
		{name: "family_prefix_leading_underscore_invalid", in: "_a_*", wantErr: ErrInvalidSyntax},
		{name: "family_invalid_suffix", in: "a_*x", wantErr: ErrInvalidSyntax},
		{name: "virtual_suffix_invalid", in: "virtual:dc_power:max", wantErr: ErrInvalidSyntax},
		{name: "semantic_with_suffix_invalid", in: "@soc:max", wantErr: ErrInvalidSyntax},
		{name: "semantic_trailing_space_ok", in: "@soc ", wantKind: KindSemantic},
		{name: "family_too_long", in: familyLong, wantErr: ErrSelectorTooLong},
		{name: "non_ascii_literal", in: "température", wantErr: ErrInvalidSyntax},
		{name: "tab_only", in: "\t\n ", wantErr: ErrEmptySelector},
		{name: "digits_start_literal_invalid", in: "1abc", wantErr: ErrInvalidSyntax},
		{name: "family_digit_start_invalid", in: "1temp_*", wantErr: ErrInvalidSyntax},
		{name: "aggregate_on_literal_invalid", in: "rsoc:max", wantErr: ErrInvalidSyntax},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ps, err := parseSelector(tc.in, cfg)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("parseSelector(%q) = nil error, want %v", tc.in, tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("parseSelector(%q) error = %v, want errors.Is %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelector(%q) unexpected error: %v", tc.in, err)
			}
			if tc.wantKind != "" && ps.kind != tc.wantKind {
				t.Fatalf("parseSelector(%q) kind=%q want %q", tc.in, ps.kind, tc.wantKind)
			}
			if tc.check != nil {
				tc.check(t, ps)
			}
		})
	}
}

// TestSelectorMaxCharsConfigurable 验证 MaxSelectorChars 可配置（按 Unicode 字符计）。
func TestSelectorMaxCharsConfigurable(t *testing.T) {
	cfg := Config{Enabled: true, MaxSelectorChars: 8}
	if _, err := parseSelector("abcdefgh", cfg); err != nil {
		t.Fatalf("8 chars should be ok with max=8: %v", err)
	}
	if _, err := parseSelector("abcdefghi", cfg); !errors.Is(err, ErrSelectorTooLong) {
		t.Fatalf("9 chars should be too long: %v", err)
	}
	// 非 ASCII 先被长度判（trim 后 9 个字符）——按字符计而非字节。
	if _, err := parseSelector("温度温度温度温度温", cfg); !errors.Is(err, ErrSelectorTooLong) {
		t.Fatalf("non-ascii 9 chars should be too long: %v", err)
	}
}

// TestMatchFamilyIndex 验证严格 ^prefix_[0-9]+$ 匹配（不是纯前缀）。
func TestMatchFamilyIndex(t *testing.T) {
	tests := []struct {
		prefix string
		name   string
		wantOK bool
		want   uint64
	}{
		{"temperature", "temperature_1", true, 1},
		{"temperature", "temperature_10", true, 10},
		{"temperature", "temperature_01", true, 1},
		{"temperature", "temperature_max", false, 0},
		{"temperature", "temperature_min", false, 0},
		{"temperature", "temperature_avg", false, 0},
		{"temperature", "temperature", false, 0},
		{"temperature", "temperature_", false, 0},
		{"temperature", "temperature_1x", false, 0},
		{"temperature", "xtemperature_1", false, 0},
		{"cell_voltage", "cell_voltage_1", true, 1},
		{"cell_voltage", "cell_voltage_max", false, 0},
		{"a", "a_0", true, 0},
		{"temperature", "temperature_999999999999999999999999999", true, ^uint64(0)},
	}
	for _, tc := range tests {
		got, ok := matchFamilyIndex(tc.prefix, tc.name)
		if ok != tc.wantOK {
			t.Fatalf("matchFamilyIndex(%q,%q) ok=%v want %v", tc.prefix, tc.name, ok, tc.wantOK)
		}
		if ok && got != tc.want {
			t.Fatalf("matchFamilyIndex(%q,%q) idx=%d want %d", tc.prefix, tc.name, got, tc.want)
		}
	}
}

// TestPhysicalNameGrammar 验证物理名/前缀文法。
func TestPhysicalNameGrammar(t *testing.T) {
	valid := []string{"a", "rsoc", "cell_voltage_1", "a_", "a1_b2"}
	invalid := []string{"", "A", "1a", "_a", "a-b", "a.b", "a b", "@a", "virtual:a", "a:b", "温度"}
	for _, s := range valid {
		if !isPhysicalName(s) {
			t.Fatalf("isPhysicalName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isPhysicalName(s) {
			t.Fatalf("isPhysicalName(%q) = true, want false", s)
		}
	}
	if isFamilyPrefix("a_") {
		t.Fatal("family prefix must not end with underscore")
	}
	if !isFamilyPrefix("a") || !isFamilyPrefix("temperature") {
		t.Fatal("valid family prefixes rejected")
	}
}

// TestErrorCodeMapping 验证 §8.6 错误码映射。
func TestErrorCodeMapping(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{ErrEmptySelector, "empty_selector"},
		{ErrSelectorTooLong, "selector_too_long"},
		{ErrInvalidSyntax, "invalid_selector_syntax"},
		{ErrUnknownSemantic, "unknown_semantic"},
		{ErrUnknownVirtual, "unknown_virtual"},
		{ErrUnsupportedAggregate, "unsupported_aggregate"},
		{ErrDisabled, "field_resolver_disabled"},
		{ErrAliasConflict, ""},
		{ErrVirtualCycle, ""},
		{nil, ""},
	}
	for _, tc := range tests {
		if got := ErrorCode(tc.err); got != tc.code {
			t.Fatalf("ErrorCode(%v) = %q want %q", tc.err, got, tc.code)
		}
	}
	// 包装后的哨兵仍需可映射。
	wrapped := syntaxErr(ErrInvalidSyntax, "x", "detail")
	if got := ErrorCode(wrapped); got != "invalid_selector_syntax" {
		t.Fatalf("wrapped error code = %q", got)
	}
}
