package sensorfield

import (
	"errors"
	"testing"
)

// TestAliasResolutionOrder 验证按候选配置顺序取第一个“该设备已知”的物理名。
func TestAliasResolutionOrder(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{
		Semantic:     "soc",
		Candidates:   []string{"rsoc", "battery_capacity", "bms_soc"},
		ExpectedUnit: "%",
	})

	res, err := reg.Resolve(1, "@soc")
	if err != nil {
		t.Fatalf("Resolve @soc failed: %v", err)
	}
	if res.Status != StatusResolved || len(res.Matched) != 1 || res.Matched[0] != "rsoc" {
		t.Fatalf("resolved = %+v", res)
	}
	if res.Ambiguous || res.Reason != "" {
		t.Fatalf("first candidate should be unambiguous: %+v", res)
	}
	plan := mustCompile(t, reg, 1, "@soc")
	if r := plan.Eval(readings("rsoc", 88.0)); !r.OK || !nearlyEqual(r.Value, 88) {
		t.Fatalf("semantic Eval = %+v", r)
	}
	// 冻结后其它候选出现也不改变结果（首解析冻结）。
	reg.SetObservations(1, []Field{numField("battery_capacity")})
	res2, err := reg.Resolve(1, "@soc")
	if err != nil {
		t.Fatalf("Resolve after obs failed: %v", err)
	}
	if res2.Matched[0] != "rsoc" {
		t.Fatalf("frozen candidate changed: %+v", res2)
	}
	if !res2.Ambiguous {
		t.Fatalf("multi-candidate should be reported ambiguous: %+v", res2)
	}
}

// TestAliasNonFirstCandidate 验证非首候选命中的可观测标记。
func TestAliasNonFirstCandidate(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("inverter", []Field{numField("battery_capacity")})
	reg.SetDeviceType(2, "inverter")
	mustSetAliases(t, reg, AliasRule{
		Semantic:     "soc",
		Candidates:   []string{"rsoc", "battery_capacity"},
		ExpectedUnit: "%",
	})
	res, err := reg.Resolve(2, "@soc")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if res.Status != StatusResolved || res.Matched[0] != "battery_capacity" {
		t.Fatalf("resolved = %+v", res)
	}
	if res.Reason != "non_first_candidate" {
		t.Fatalf("reason = %q want non_first_candidate", res.Reason)
	}
	if res.Ambiguous {
		t.Fatalf("single known candidate should not be ambiguous: %+v", res)
	}
}

// TestAliasAmbiguousMultipleKnown 验证多候选同时存在 ⇒ Ambiguous。
func TestAliasAmbiguousMultipleKnown(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("mixed", []Field{numField("rsoc"), numField("battery_capacity")})
	reg.SetDeviceType(3, "mixed")
	mustSetAliases(t, reg, AliasRule{
		Semantic:     "soc",
		Candidates:   []string{"rsoc", "battery_capacity"},
		ExpectedUnit: "%",
	})
	res, err := reg.Resolve(3, "@soc")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !res.Ambiguous || res.Matched[0] != "rsoc" || res.Reason != "" {
		t.Fatalf("ambiguous result = %+v", res)
	}
}

// TestAliasUnboundAndUnknown 验证“无候选已知 ⇒ unbound（非错误）”与“未定义语义 ⇒ 400 类错误”。
func TestAliasUnboundAndUnknown(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})

	// 设备 9 没有任何已知字段 ⇒ semantic 解析 unbound 且 err=nil。
	res, err := reg.Resolve(9, "@soc")
	if err != nil {
		t.Fatalf("unbound semantic must not error: %v", err)
	}
	if res.Status != StatusUnbound || res.Reason != reasonNoKnownCandidate {
		t.Fatalf("unbound semantic = %+v", res)
	}
	plan, err := reg.Compile(9, "@soc")
	if err != nil {
		t.Fatalf("Compile of unbound semantic must succeed (runtime unbound): %v", err)
	}
	if r := plan.Eval(readings("rsoc", 1.0)); r.OK || r.Reason != reasonNoKnownCandidate {
		t.Fatalf("unbound semantic plan Eval = %+v", r)
	}

	// 未定义语义 ⇒ ErrUnknownSemantic（控制面 400）。
	if _, err := reg.Resolve(1, "@nope"); !errors.Is(err, ErrUnknownSemantic) {
		t.Fatalf("Resolve @nope err = %v want ErrUnknownSemantic", err)
	}
	if _, err := reg.Compile(1, "@nope"); !errors.Is(err, ErrUnknownSemantic) {
		t.Fatalf("Compile @nope err = %v want ErrUnknownSemantic", err)
	}
	if got := ErrorCode(errOf(reg.Resolve, 1, "@nope")); got != "unknown_semantic" {
		t.Fatalf("error code = %q", got)
	}
}

// errOf 调用返回 error 的函数（测试辅助）。
func errOf(fn func(uint, string) (Resolved, error), deviceID uint, selector string) error {
	_, err := fn(deviceID, selector)
	return err
}

// TestAliasConflictDetection 验证关系冲突检测（方案 §6.1 规则 2）。
func TestAliasConflictDetection(t *testing.T) {
	soc := AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}}
	pct := AliasRule{Semantic: "battery_pct", Candidates: []string{"rsoc"}}

	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	if err := reg.SetAliases([]AliasRule{soc, pct}); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("overlap without allow_overlap err = %v want ErrAliasConflict", err)
	}

	// 两项都 allow_overlap 才放行。
	socOverlap := soc
	socOverlap.AllowOverlap = true
	pctOverlap := pct
	pctOverlap.AllowOverlap = true
	mustSetAliases(t, reg, socOverlap, pctOverlap)
	if got := reg.SemanticsOf("rsoc"); len(got) != 2 || got[0] != "soc" || got[1] != "battery_pct" {
		t.Fatalf("SemanticsOf = %v", got)
	}

	// 只有一项 allow_overlap ⇒ 仍冲突（所有相关项必须显式同意）。
	one := New(enabledConfig())
	one.RegisterDeclarations("bms", []Field{numField("rsoc")})
	socAllow := soc
	socAllow.AllowOverlap = true
	if err := one.SetAliases([]AliasRule{socAllow, pct}); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("one-sided allow_overlap err = %v want ErrAliasConflict", err)
	}
}

// TestAliasStructuralErrors 验证别名表结构错误。
func TestAliasStructuralErrors(t *testing.T) {
	reg := New(enabledConfig())
	tests := []struct {
		name string
		rule []AliasRule
		want error
	}{
		{"empty_semantic", []AliasRule{{Semantic: "", Candidates: []string{"rsoc"}}}, ErrAliasConflict},
		{"invalid_semantic", []AliasRule{{Semantic: "SOC", Candidates: []string{"rsoc"}}}, ErrInvalidSyntax},
		{"no_candidates", []AliasRule{{Semantic: "soc"}}, ErrAliasConflict},
		{"empty_candidate", []AliasRule{{Semantic: "soc", Candidates: []string{""}}}, ErrAliasConflict},
		{"invalid_candidate", []AliasRule{{Semantic: "soc", Candidates: []string{"RSOC"}}}, ErrAliasConflict},
		{"duplicate_candidate", []AliasRule{{Semantic: "soc", Candidates: []string{"rsoc", "rsoc"}}}, ErrAliasConflict},
		{"duplicate_semantic", []AliasRule{
			{Semantic: "soc", Candidates: []string{"rsoc"}},
			{Semantic: "soc", Candidates: []string{"battery_capacity"}},
		}, ErrAliasConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := reg.SetAliases(tc.rule); !errors.Is(err, tc.want) {
				t.Fatalf("SetAliases err = %v want %v", err, tc.want)
			}
		})
	}
}

// TestAliasUnitClassValidation 验证候选单位类与 ExpectedUnit 不符 ⇒ 拒绝加载。
func TestAliasUnitClassValidation(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("inv", []Field{
		typedField("total_voltage", TypeNumber, "V"),
		typedField("battery_capacity", TypeNumber, "%"),
		numField("rsoc"),
	})
	// 单位类不符：期望电流 A，候选却是电压 V。
	if err := reg.SetAliases([]AliasRule{{Semantic: "current", Candidates: []string{"total_voltage"}, ExpectedUnit: "A"}}); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("unit class mismatch err = %v want ErrAliasConflict", err)
	}
	// 单位类相符（% 与 percent 同类）。
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"battery_capacity"}, ExpectedUnit: "%"})
	// 期望单位写成类名亦可。
	mustSetAliases(t, reg, AliasRule{Semantic: "soc2", Candidates: []string{"battery_capacity"}, ExpectedUnit: "percent"})
	// 候选没有任何已知单位 ⇒ 跳过校验（不猜测）。
	mustSetAliases(t, reg, AliasRule{Semantic: "voltage", Candidates: []string{"undeclared_field"}, ExpectedUnit: "V"})
}

// TestAliasSetFailureKeepsOldTable 验证 SetAliases 失败不改动当前表（fail-closed）。
func TestAliasSetFailureKeepsOldTable(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("bms", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "bms")
	mustSetAliases(t, reg, AliasRule{Semantic: "soc", Candidates: []string{"rsoc"}})

	if err := reg.SetAliases([]AliasRule{
		{Semantic: "soc", Candidates: []string{"rsoc"}},
		{Semantic: "battery_pct", Candidates: []string{"rsoc"}},
	}); err == nil {
		t.Fatal("expected conflict error")
	}
	res, err := reg.Resolve(1, "@soc")
	if err != nil || res.Status != StatusResolved {
		t.Fatalf("old alias table lost: %+v err=%v", res, err)
	}
}

// TestUnitClasses 验证单位类归一表（符号 + 类名 + 未知）。
func TestUnitClasses(t *testing.T) {
	tests := []struct {
		unit string
		want unitClass
	}{
		{"V", unitClassVoltage},
		{"mV", unitClassVoltage},
		{"A", unitClassCurrent},
		{"mA", unitClassCurrent},
		{"%", unitClassPercent},
		{"%RH", unitClassPercent},
		{"°C", unitClassTemperature},
		{"℃", unitClassTemperature},
		{"W", unitClassPower},
		{"kWh", unitClassEnergy},
		{"Ah", unitClassCharge},
		{"Hz", unitClassFrequency},
		{"Ω", unitClassResistance},
		{"次", unitClassCount},
		{"串", unitClassCount},
		{"s", unitClassTime},
		{"dB", unitClassSignal},
		{"lx", unitClassIlluminance},
		{"mm", unitClassLength},
		{"hPa", unitClassPressure},
		{"ppm", unitClassConcentration},
		{"m/s", unitClassSpeed},
		{"°", unitClassAngle},
		{"bit/s", unitClassBitrate},
		{"", unitClassUnknown},
		{"ack", unitClassUnknown},
		{"bitmask", unitClassUnknown},
		{"mystery", unitClassUnknown},
	}
	for _, tc := range tests {
		if got := classOfUnit(tc.unit); got != tc.want {
			t.Fatalf("classOfUnit(%q) = %q want %q", tc.unit, got, tc.want)
		}
	}
	// ExpectedUnit 接受类名。
	if got := expectedClass("voltage"); got != unitClassVoltage {
		t.Fatalf("expectedClass(voltage) = %q", got)
	}
	if got := expectedClass("percent"); got != unitClassPercent {
		t.Fatalf("expectedClass(percent) = %q", got)
	}
	if got := expectedClass("mystery"); got != unitClassUnknown {
		t.Fatalf("expectedClass(mystery) = %q", got)
	}
}

// TestNonNumericUnitFallback 验证伪单位兜底判定。
func TestNonNumericUnitFallback(t *testing.T) {
	numeric := []string{"v", "a", "%", "°c", "", "kwh", "次"}
	nonNumeric := []string{"ack", "raw", "state", "bitmask", "ACK", " bitmask "}
	for _, u := range numeric {
		if nonNumericUnit(u) {
			t.Fatalf("unit %q should be numeric-capable", u)
		}
	}
	for _, u := range nonNumeric {
		if !nonNumericUnit(u) {
			t.Fatalf("unit %q should be non-numeric", u)
		}
	}
}
