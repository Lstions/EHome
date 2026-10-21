package sensorfield

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNewDefaultsAndEmptyState 验证 New 的默认值与空状态。
func TestNewDefaultsAndEmptyState(t *testing.T) {
	reg := New(Config{})
	cfg := reg.Config()
	if cfg.MaxSelectorChars != 64 || cfg.MaxFamilySize != 64 || cfg.MaxVirtualDepth != 4 {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.SnapshotTTL != 10*time.Minute {
		t.Fatalf("SnapshotTTL = %v", cfg.SnapshotTTL)
	}
	if reg.Enabled() {
		t.Fatal("zero Config must be disabled (fail-closed)")
	}
	if reg.Epoch() == 0 {
		t.Fatal("initial epoch must be > 0")
	}
	if fields := reg.KnownFields(42); len(fields) != 0 {
		t.Fatalf("empty registry KnownFields = %v", fields)
	}
	if res, err := reg.Resolve(42, "rsoc"); err != nil {
		t.Fatalf("literal Resolve on empty registry: %v", err)
	} else if res.Status != StatusUnbound {
		t.Fatalf("literal Resolve on unknown device = %q", res.Status)
	}
}

// TestEpochIncrementsOnSourceChanges 验证任一来源变更 epoch+1；SetDeviceType 同值不 bump。
func TestEpochIncrementsOnSourceChanges(t *testing.T) {
	reg := New(enabledConfig())
	step := func(name string) uint64 {
		t.Helper()
		before := reg.Epoch()
		return before
	}
	e0 := step("init")
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	e1 := reg.Epoch()
	if e1 <= e0 {
		t.Fatalf("RegisterDeclarations did not bump epoch: %d -> %d", e0, e1)
	}
	reg.SetDeviceType(1, "bms")
	e2 := reg.Epoch()
	if e2 <= e1 {
		t.Fatalf("SetDeviceType did not bump epoch")
	}
	reg.SetDeviceType(1, "bms") // 同值
	if reg.Epoch() != e2 {
		t.Fatalf("SetDeviceType with same value bumped epoch")
	}
	reg.SetObservations(1, []Field{numField("rsoc")})
	e3 := reg.Epoch()
	if e3 <= e2 {
		t.Fatalf("SetObservations did not bump epoch")
	}
	reg.SetFieldMeta([]Field{{Name: "rsoc", Unit: "%", ValueType: TypeNumber}})
	e4 := reg.Epoch()
	if e4 <= e3 {
		t.Fatalf("SetFieldMeta did not bump epoch")
	}
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})
	e5 := reg.Epoch()
	if e5 <= e4 {
		t.Fatalf("SetAliases did not bump epoch")
	}
	mustSetVirtuals(t, reg, VirtualDef{Name: "double_soc", Fn: "scale", Args: []string{"@soc"}, Scale: 2})
	e6 := reg.Epoch()
	if e6 <= e5 {
		t.Fatalf("SetVirtuals did not bump epoch")
	}
	reg.SetEnabled(false)
	e7 := reg.Epoch()
	if e7 <= e6 {
		t.Fatalf("SetEnabled did not bump epoch")
	}
	reg.SetEnabled(false) // 同值
	if reg.Epoch() != e7 {
		t.Fatalf("SetEnabled same value bumped epoch")
	}
}

// TestKnownFieldsUnionAndFlags 验证 声明∪观测 合并与 Declared/Observed 标志。
func TestKnownFieldsUnionAndFlags(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{
		{Name: "rsoc", Unit: "%"},
		{Name: "total_voltage", Unit: "V"},
	})
	reg.SetDeviceType(1, "bms")
	seen := fixedTime(0)
	reg.SetObservations(1, []Field{
		{Name: "rsoc", Unit: "%", LastSeen: seen, Samples: 7},
		{Name: "battery_capacity", Unit: "%", LastSeen: seen, Samples: 3},
	})
	fields := reg.KnownFields(1)
	byName := map[string]Field{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	rsoc, ok := byName["rsoc"]
	if !ok || !rsoc.Declared || !rsoc.Observed || rsoc.Samples != 7 {
		t.Fatalf("rsoc merged = %+v", rsoc)
	}
	voltage, ok := byName["total_voltage"]
	if !ok || !voltage.Declared || voltage.Observed {
		t.Fatalf("decl-only = %+v", voltage)
	}
	cap, ok := byName["battery_capacity"]
	if !ok || cap.Declared || !cap.Observed || cap.Samples != 3 {
		t.Fatalf("obs-only = %+v", cap)
	}
	// 声明顺序在前，观测增量按名排序。
	if fields[0].Name != "rsoc" || fields[1].Name != "total_voltage" || fields[2].Name != "battery_capacity" {
		t.Fatalf("order = %v", []string{fields[0].Name, fields[1].Name, fields[2].Name})
	}
	// 返回副本：外部修改不影响内部。
	fields[0].Unit = "mutated"
	if again := reg.KnownFields(1); again[0].Unit == "mutated" {
		t.Fatal("KnownFields returns shared state")
	}
}

// TestFieldMetaPrecedence 验证 meta > obs > decl 三源合并优先级。
func TestFieldMetaPrecedence(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{{Name: "rsoc", Unit: ""}})
	reg.SetDeviceType(1, "bms")
	reg.SetObservations(1, []Field{{Name: "rsoc", Unit: ""}})
	// meta 提供单位与数值类型。
	reg.SetFieldMeta([]Field{{Name: "rsoc", Unit: "%", ValueType: TypeNumber}})
	plan := mustCompile(t, reg, 1, "rsoc")
	if r := plan.Eval(readings("rsoc", 12.0)); !r.OK {
		t.Fatalf("meta number literal = %+v", r)
	}
	// meta 改为 string ⇒ 非数值。
	reg.SetFieldMeta([]Field{{Name: "rsoc", ValueType: TypeString}})
	plan = mustCompile(t, reg, 1, "rsoc")
	if r := plan.Eval(readings("rsoc", 12.0)); r.OK || r.Reason != reasonNonNumeric {
		t.Fatalf("meta string literal = %+v", r)
	}
	// 非法 value_type 被丢弃（保留空 meta，字段回退 decl/obs ⇒ 数值）。
	reg.SetFieldMeta([]Field{{Name: "rsoc", ValueType: ValueType("weird")}})
	plan = mustCompile(t, reg, 1, "rsoc")
	if r := plan.Eval(readings("rsoc", 12.0)); !r.OK {
		t.Fatalf("invalid meta should be ignored: %+v", r)
	}
}

// TestValidateOnlyHardErrors 验证 Validate 只报“配置内部矛盾”（Lead 裁决 A 级）。
func TestValidateOnlyHardErrors(t *testing.T) {
	// 合法配置 + 死候选 ⇒ Validate 必须 nil（不因部署环境数据缺失拒绝启动）。
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{
		Semantic:   "soc",
		Candidates: []string{"rsoc", "battery_capacity"}, // battery_capacity 当前无人产出
	})
	mustSetVirtuals(t, reg, VirtualDef{Name: "soc_scaled", Fn: "scale", Args: []string{"@soc"}, Scale: 2})
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate must not fail on data-dependent dead candidate: %v", err)
	}

	// 跨表硬错误：virtual 引用不存在的语义（先装 virtuals 再装 aliases 可绕过 SetVirtuals 即时校验）。
	bad := New(enabledConfig())
	bad.RegisterDeclarations("bms", []Field{numField("rsoc")})
	bad.SetDeviceType(1, "bms")
	mustSetVirtuals(t, bad, VirtualDef{Name: "v", Fn: "scale", Args: []string{"@missing"}, Scale: 2})
	mustSetAliases(t, bad, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})
	err := bad.Validate()
	if !errors.Is(err, ErrUnknownSemantic) {
		t.Fatalf("Validate cross-ref err = %v want ErrUnknownSemantic", err)
	}

	// 空 registry 的 Validate 不应失败。
	if err := New(enabledConfig()).Validate(); err != nil {
		t.Fatalf("empty Validate = %v", err)
	}
}

// TestWarningsBLevel 验证 B 级告警（死候选 / 无语义可用候选 / 冷启动）。
func TestWarningsBLevel(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{
		Semantic:   "soc",
		Candidates: []string{"rsoc", "battery_capacity"},
	}, AliasRule{
		Semantic:   "ghost",
		Candidates: []string{"no_such_field_a", "no_such_field_b"},
	})
	warns := reg.Warnings()
	joined := strings.Join(warns, "\\n")
	if !strings.Contains(joined, "battery_capacity") {
		t.Fatalf("dead candidate warning missing: %s", joined)
	}
	if !strings.Contains(joined, "ghost") {
		t.Fatalf("no-candidate warning missing: %s", joined)
	}
	if !strings.Contains(joined, "no observations loaded") {
		t.Fatalf("cold start warning missing: %s", joined)
	}
	// 观测载入后冷启动告警消失。
	reg.SetObservations(1, []Field{numField("rsoc")})
	warns = reg.Warnings()
	if strings.Contains(strings.Join(warns, "\n"), "no observations loaded") {
		t.Fatalf("cold start warning should disappear after observations: %v", warns)
	}
	// Warnings 不影响 epoch/可用性。
	if res, err := reg.Resolve(1, "@soc"); err != nil || res.Status != StatusResolved {
		t.Fatalf("Warnings must not disturb registry: %+v err=%v", res, err)
	}
}

// TestAliasesVirtualsConfigGetters 验证 /schema 只读 getter 返回副本。
func TestAliasesVirtualsConfigGetters(t *testing.T) {
	reg := New(Config{Enabled: true, MaxSelectorChars: 32, MaxFamilySize: 8, MaxVirtualDepth: 3})
	rule := AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}}
	def := VirtualDef{Name: "x", Fn: "scale", Args: []string{"rsoc"}, Scale: 2}
	mustSetAliases(t, reg, rule)
	mustSetVirtuals(t, reg, def)

	aliases := reg.Aliases()
	if len(aliases) != 1 || aliases[0].Semantic != "soc" {
		t.Fatalf("Aliases = %+v", aliases)
	}
	aliases[0].Candidates[0] = "mutated"
	if again := reg.Aliases(); again[0].Candidates[0] != "rsoc" {
		t.Fatal("Aliases returns shared state")
	}
	virtuals := reg.Virtuals()
	if len(virtuals) != 1 || virtuals[0].Name != "x" {
		t.Fatalf("Virtuals = %+v", virtuals)
	}
	virtuals[0].Args[0] = "mutated"
	if again := reg.Virtuals(); again[0].Args[0] != "rsoc" {
		t.Fatal("Virtuals returns shared state")
	}
	cfg := reg.Config()
	if cfg.MaxSelectorChars != 32 || cfg.MaxFamilySize != 8 || cfg.MaxVirtualDepth != 3 {
		t.Fatalf("Config limits = %+v", cfg)
	}
}

// TestSnapshotAge 验证快照年龄可读且随时间增长。
func TestSnapshotAge(t *testing.T) {
	reg := New(enabledConfig())
	age := reg.SnapshotAge()
	if age < 0 {
		t.Fatalf("negative snapshot age: %v", age)
	}
	time.Sleep(2 * time.Millisecond)
	if reg.SnapshotAge() <= age {
		t.Fatalf("snapshot age did not grow: %v -> %v", age, reg.SnapshotAge())
	}
}

// TestRegisterDeclarationFiltering 验证非法声明名被过滤而不是打断启动。
func TestRegisterDeclarationFiltering(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("", []Field{numField("rsoc")}) // 空 device_type 忽略
	reg.RegisterDeclarations("bms", []Field{
		numField("rsoc"),
		{Name: "RSOC"}, // 大写非法
		{Name: "温度"},   // 非 ASCII 非法
		{Name: "rsoc"}, // 重复
	})
	fields := reg.KnownFields(0)
	if len(fields) != 0 {
		t.Fatalf("empty device_type must be ignored: %v", fields)
	}
	reg.SetDeviceType(1, "bms")
	got := reg.KnownFields(1)
	if len(got) != 1 || got[0].Name != "rsoc" {
		t.Fatalf("filtered declarations = %+v", got)
	}
}

// TestSetObservationsReplace 验证投影刷新是整体替换。
func TestSetObservationsReplace(t *testing.T) {
	reg := New(enabledConfig())
	reg.SetDeviceType(1, "bms")
	reg.SetObservations(1, []Field{numField("a"), numField("b")})
	if got := reg.KnownFields(1); len(got) != 2 {
		t.Fatalf("obs = %v", got)
	}
	reg.SetObservations(1, []Field{numField("c")})
	got := reg.KnownFields(1)
	if len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("replace obs = %v", got)
	}
	reg.SetObservations(1, nil)
	if got := reg.KnownFields(1); len(got) != 0 {
		t.Fatalf("clear obs = %v", got)
	}
}
