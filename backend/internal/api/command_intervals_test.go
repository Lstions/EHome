package api

import (
	"testing"

	"ehome/backend/internal/drivers"
)

// fakeMultiDriver 供 C1-C4 测试使用：两个 schedulable + 一个 one-shot 模板。
// 注意：本计划所有 api 测试禁止用 techfine 当非 schedulable fixture（C6 会删它）。
type fakeMultiDriver struct{}

func (d *fakeMultiDriver) DeviceType() string                             { return "fake_multi" }
func (d *fakeMultiDriver) DeviceName() string                             { return "契约测试假驱动" }
func (d *fakeMultiDriver) OEM() string                                    { return "test" }
func (d *fakeMultiDriver) Category() string                               { return "test" }
func (d *fakeMultiDriver) HardwareTypes() []string                        { return []string{"uart"} }
func (d *fakeMultiDriver) GetSensorDefinitions() []drivers.SensorData     { return nil }
func (d *fakeMultiDriver) ParseData([]byte) ([]drivers.SensorData, error) { return nil, nil }
func (d *fakeMultiDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return []drivers.CommandTemplate{
		{ID: "read_a", Name: "读A", Type: "read", WriteData: "AA01", ReadLength: 4, IntervalMs: 5000, Schedulable: true},
		{ID: "read_b", Name: "读B", Type: "read", WriteData: "AA02", ReadLength: 4, IntervalMs: 0, Schedulable: true},
		{ID: "one_shot", Name: "一次性", Type: "write", WriteData: "AA03", ReadLength: 4, IntervalMs: 0, Schedulable: false},
	}
}

func newFakeRegistry() *drivers.Registry {
	r := drivers.NewRegistry()
	r.Register(&fakeMultiDriver{})
	return r
}

func assertStringSet(t *testing.T, label string, got map[string]struct{}, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d ids %v, want %d %v", label, len(got), got, len(want), want)
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("%s: missing id %q in %v", label, id, got)
		}
	}
}

func TestSchedulableCommandIDs_ReturnsSchedulableOnly(t *testing.T) {
	got, err := SchedulableCommandIDs(newFakeRegistry(), "fake_multi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertStringSet(t, "SchedulableCommandIDs", got, "read_a", "read_b")
	if _, ok := got["one_shot"]; ok {
		t.Fatal("one_shot must not be in the schedulable set")
	}
}

func TestSchedulableCommandIDs_UnknownTypeErrors(t *testing.T) {
	if _, err := SchedulableCommandIDs(newFakeRegistry(), "nope"); err == nil {
		t.Fatal("expected error for unregistered driver type")
	}
}

func TestValidateCommandIntervals_RejectsUnknownId(t *testing.T) {
	if err := ValidateCommandIntervals(newFakeRegistry(), "fake_multi", map[string]int{"nope": 1}); err == nil {
		t.Fatal("expected error for unknown command id")
	}
}

func TestValidateCommandIntervals_RejectsNonSchedulableId(t *testing.T) {
	if err := ValidateCommandIntervals(newFakeRegistry(), "fake_multi", map[string]int{"one_shot": 1}); err == nil {
		t.Fatal("expected error for non-schedulable command id")
	}
}

func TestValidateCommandIntervals_AcceptsSchedulableIds(t *testing.T) {
	if err := ValidateCommandIntervals(newFakeRegistry(), "fake_multi", map[string]int{"read_a": 1, "read_b": 0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeCommandIntervals_ClampsNegatives(t *testing.T) {
	in := map[string]int{"read_a": -5, "read_b": 3}
	got := NormalizeCommandIntervals(in)
	if got["read_a"] != 0 {
		t.Errorf("read_a: got %d, want 0", got["read_a"])
	}
	if got["read_b"] != 3 {
		t.Errorf("read_b: got %d, want 3", got["read_b"])
	}
	if in["read_a"] != -5 {
		t.Errorf("input map was mutated: read_a=%d", in["read_a"])
	}
}
// ==================== manifest command capacity gate ====================
//
// 生产事故（2026-09-17，节点 F0F5BDFFFE02）：BMS 的 5 条 schedulable 指令全部
// 配了 interval>0，写入侧放行（HTTP 200），但编码器上限是 3 条，于是每次推送
// 都被拒。写入侧本该满足"能保存 = 能下发"，这里把该不变量钉住。

func TestCountManifestCandidates_CountsOnlyEnabledSchedulable(t *testing.T) {
	registry := newFakeRegistry()
	// read_a/read_b 是唯二的 schedulable；one_shot 不算。
	cases := []struct {
		name      string
		intervals map[string]int
		fallback  int
		want      int
	}{
		{"all enabled via stored intervals", map[string]int{"read_a": 5000, "read_b": 5000}, 0, 2},
		{"zero disables a command", map[string]int{"read_a": 5000, "read_b": 0}, 0, 1},
		{"all zero means no polling", map[string]int{"read_a": 0, "read_b": 0}, 0, 0},
		{"empty map falls back to template defaults", nil, 0, 1},
		{"empty map with device fallback enables both", nil, 3000, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CountManifestCandidates(registry, "fake_multi", tc.intervals, tc.fallback)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("count=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestValidateManifestCommandCapacity_RejectsOverCollectorLimit(t *testing.T) {
	registry := drivers.NewRegistry()
	registry.Register(&fourSchedulableDriver{})
	if err := ValidateManifestCommandCapacity(registry, "four-sched", map[string]int{
		"c1": 5000, "c2": 5000, "c3": 5000, "c4": 5000,
	}, 0); err == nil {
		t.Fatal("4 enabled polling commands must exceed the collector limit of 3")
	}
	// Exactly at the limit is allowed.
	if err := ValidateManifestCommandCapacity(registry, "four-sched", map[string]int{
		"c1": 5000, "c2": 5000, "c3": 5000, "c4": 0,
	}, 0); err != nil {
		t.Fatalf("3 enabled polling commands must be accepted: %v", err)
	}
}

func TestValidateManifestCommandCapacity_UnknownDriverErrors(t *testing.T) {
	if err := ValidateManifestCommandCapacity(newFakeRegistry(), "nope", nil, 0); err == nil {
		t.Fatal("unknown driver type must error, not silently pass the gate")
	}
}

// fourSchedulableDriver declares 4 schedulable commands — one more than the
// collector supports — so the capacity gate can be exercised at the boundary.
type fourSchedulableDriver struct{ fakeMultiDriver }

func (*fourSchedulableDriver) DeviceType() string { return "four-sched" }
func (*fourSchedulableDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return []drivers.CommandTemplate{
		{ID: "c1", Type: "read", WriteData: "B1", ReadLength: 1, IntervalMs: 5000, Schedulable: true},
		{ID: "c2", Type: "read", WriteData: "B2", ReadLength: 1, IntervalMs: 5000, Schedulable: true},
		{ID: "c3", Type: "read", WriteData: "B3", ReadLength: 1, IntervalMs: 5000, Schedulable: true},
		{ID: "c4", Type: "read", WriteData: "B4", ReadLength: 1, IntervalMs: 5000, Schedulable: true},
	}
}
