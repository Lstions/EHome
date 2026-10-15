package api

import (
	"fmt"
	"strings"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/nodemgr"
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

// TestValidateManifestCommandCapacity_RejectsOverCollectorLimit 守住容量门的边界：
// 「恰好等于上限」放行、「超出一条」拒绝，且拒绝文案必须点名真实上限。
//
// 2026-10-15：生产常量由 3 提到 8（固件改为动态分配，见 nodemgr/sender.go）。
// 本用例改为**从常量推导**条数，而不是硬编码 4 —— 硬编码会在常量变大后
// 落在限内，断言随之变成假绿。推导后无论常量怎么变都继续验证机制本身。
func TestValidateManifestCommandCapacity_RejectsOverCollectorLimit(t *testing.T) {
	registry := drivers.NewRegistry()
	registry.Register(&fourSchedulableDriver{})
	limit := nodemgr.MaxCommandsPerEdgeDevice

	// ⚠ 映射必须覆盖**驱动声明的每一条**：CountManifestCandidates 对"不在映射里的
	// 指令"会回退到模板默认 interval_ms（本假驱动是 5000 ⇒ 默认即启用）。
	// 只写 limit 条会让第 limit+1 条落到回退分支而被计入，边界断言随即失真。
	atLimit := map[string]int{}
	for i := 0; i < limit+1; i++ {
		if i < limit {
			atLimit[fmt.Sprintf("c%d", i)] = 5000
		} else {
			atLimit[fmt.Sprintf("c%d", i)] = 0 // 显式禁用，避免走默认值回退
		}
	}
	if err := ValidateManifestCommandCapacity(registry, "four-sched", atLimit, 0, limit); err != nil {
		t.Fatalf("%d enabled polling commands (== collector limit) must be accepted: %v", limit, err)
	}

	over := map[string]int{}
	for i := 0; i < limit+1; i++ {
		over[fmt.Sprintf("c%d", i)] = 5000
	}
	err := ValidateManifestCommandCapacity(registry, "four-sched", over, 0, limit)
	if err == nil {
		t.Fatalf("%d enabled polling commands must exceed the collector limit of %d", limit+1, limit)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("at most %d", limit)) {
		t.Fatalf("rejection must name the real limit %d, got: %v", limit, err)
	}
}

func TestValidateManifestCommandCapacity_UnknownDriverErrors(t *testing.T) {
	if err := ValidateManifestCommandCapacity(newFakeRegistry(), "nope", nil, 0, 0); err == nil {
		t.Fatal("unknown driver type must error, not silently pass the gate")
	}
}

// fourSchedulableDriver 声明 **collector 上限 + 1** 条可调度指令，使容量门**永远**
// 能在边界上被真实触发。
//
// 2026-10-15：改前硬编码 4 条（当时上限 3）。上限提到 8 后 4 条落在限内，
// 用例会"通过但没测到门"。现在按常量推导：无论上限多少，都恰好比它多一条。
type fourSchedulableDriver struct{ fakeMultiDriver }

func (*fourSchedulableDriver) DeviceType() string { return "four-sched" }
func (*fourSchedulableDriver) GetCommandTemplates() []drivers.CommandTemplate {
	templates := make([]drivers.CommandTemplate, 0, nodemgr.MaxCommandsPerEdgeDevice+1)
	for i := 0; i < nodemgr.MaxCommandsPerEdgeDevice+1; i++ {
		templates = append(templates, drivers.CommandTemplate{
			ID: fmt.Sprintf("c%d", i), Type: "read",
			WriteData: fmt.Sprintf("B%02d", i), ReadLength: 1,
			IntervalMs: 5000, Schedulable: true,
		})
	}
	return templates
}
