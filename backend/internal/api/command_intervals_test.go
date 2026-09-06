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
