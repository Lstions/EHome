package sensorfield

import (
	"errors"
	"testing"
)

// TestResolveLiteral 验证 literal 控制面解析（已知/未知）。
func TestResolveLiteral(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	res, err := reg.Resolve(1, "rsoc")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if res.Status != StatusResolved || res.Kind != KindLiteral || len(res.Matched) != 1 || res.Matched[0] != "rsoc" {
		t.Fatalf("resolved = %+v", res)
	}
	if res.Aggregate != "" || res.Ambiguous {
		t.Fatalf("literal fields = %+v", res)
	}
	unbound, err := reg.Resolve(2, "rsoc")
	if err != nil {
		t.Fatalf("unbound literal must not error: %v", err)
	}
	if unbound.Status != StatusUnbound {
		t.Fatalf("status = %q", unbound.Status)
	}
}

// TestResolveFamily 验证族展开（数字序/默认聚合/无已知成员）。
func TestResolveFamily(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{
		numField("temperature_10"), numField("temperature_2"), numField("temperature_1"),
		numField("temperature_max"), // 派生归约量不得入族
	})
	reg.SetDeviceType(1, "bms")
	res, err := reg.Resolve(1, "temperature_*")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if res.Kind != KindFamily || res.Aggregate != AggAny {
		t.Fatalf("family shape = %+v", res)
	}
	want := []string{"temperature_1", "temperature_2", "temperature_10"}
	if len(res.Matched) != 3 {
		t.Fatalf("matched = %v", res.Matched)
	}
	for i := range want {
		if res.Matched[i] != want[i] {
			t.Fatalf("matched = %v want %v", res.Matched, want)
		}
	}
	explicit, err := reg.Resolve(1, "temperature_*:sum")
	if err != nil {
		t.Fatalf("Resolve explicit agg failed: %v", err)
	}
	if explicit.Aggregate != AggSum {
		t.Fatalf("aggregate = %q", explicit.Aggregate)
	}
	empty, err := reg.Resolve(1, "pressure_*")
	if err != nil {
		t.Fatalf("empty family must not error: %v", err)
	}
	if empty.Status != StatusUnbound || empty.Reason != reasonNoKnownMember {
		t.Fatalf("empty family = %+v", empty)
	}
}

// TestResolveSyntaxAndLookupErrors 验证控制面 400 类错误（语法/未知语义/未知派生）。
func TestResolveSyntaxAndLookupErrors(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})

	tests := []struct {
		selector string
		want     error
	}{
		{"", ErrEmptySelector},
		{"   ", ErrEmptySelector},
		{"温度_*", ErrInvalidSyntax},
		{"temperature_*:median", ErrUnsupportedAggregate},
		{"temperature_*:MAX", ErrUnsupportedAggregate},
		{"@unknown", ErrUnknownSemantic},
		{"virtual:unknown", ErrUnknownVirtual},
	}
	for _, tc := range tests {
		res, err := reg.Resolve(1, tc.selector)
		if !errors.Is(err, tc.want) {
			t.Fatalf("Resolve(%q) err = %v want %v", tc.selector, err, tc.want)
		}
		// 错误响应无 status（方案 §8.2：错误走 400 + error_code）。
		if res.Status == StatusResolved || res.Status == StatusUnbound {
			t.Fatalf("Resolve(%q) status = %q on error", tc.selector, res.Status)
		}
	}
}

// TestResolveNeverReturnsThirdStatus 验证 Status 只有 resolved/unbound 两个取值。
func TestResolveNeverReturnsThirdStatus(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc"), numField("temperature_1")})
	reg.SetDeviceType(1, "bms")
	for _, sel := range []string{"rsoc", "temperature_*", "temperature_*:max"} {
		res, err := reg.Resolve(1, sel)
		if err != nil {
			t.Fatalf("Resolve(%q) failed: %v", sel, err)
		}
		if res.Status != StatusResolved && res.Status != StatusUnbound {
			t.Fatalf("Resolve(%q) status = %q", sel, res.Status)
		}
	}
}

// TestResolveMetricsDoNotPanic 验证解析指标路径（只跑不崩）。
func TestResolveMetricsDoNotPanic(t *testing.T) {
	reg := New(enabledConfig())
	reg.SetDeviceType(1, "bms")
	for i := 0; i < 5; i++ {
		_, _ = reg.Resolve(1, "rsoc")
		_, _ = reg.Resolve(1, "@nope")
		_, _ = reg.Resolve(1, "温度_*")
	}
}
