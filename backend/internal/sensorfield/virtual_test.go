package sensorfield

import (
	"errors"
	"math"
	"testing"
)

// standardVirtuals 返回覆盖全部 9 个派生函数的定义集（含 2 层嵌套）。
func standardVirtuals() []VirtualDef {
	return []VirtualDef{
		{Name: "prod_ab", Fn: "product", Args: []string{"@a", "@b"}},
		{Name: "sum_abc", Fn: "sum", Args: []string{"@a", "@b", "@c"}},
		{Name: "diff_abc", Fn: "diff", Args: []string{"@a", "@b", "@c"}},
		{Name: "absdiff_ab", Fn: "abs_diff", Args: []string{"@a", "@b"}},
		{Name: "ratio_ab", Fn: "ratio", Args: []string{"@a", "@b"}},
		{Name: "scaled_a", Fn: "scale", Args: []string{"@a"}, Scale: 2.5},
		{Name: "max_t", Fn: "max_of", Args: []string{"temperature_*"}},
		{Name: "min_t", Fn: "min_of", Args: []string{"temperature_*"}},
		{Name: "sum_t", Fn: "sum_of", Args: []string{"temperature_*"}},
		{Name: "sum_prod_c", Fn: "sum", Args: []string{"virtual:prod_ab", "@c"}}, // 嵌套深度 2
	}
}

// newVirtualTestRegistry 构造带别名/派生的设备夹具（设备 1）。
func newVirtualTestRegistry(t *testing.T, cfg Config) *Registry {
	t.Helper()
	reg := New(cfg)
	reg.RegisterDeclarations("dev", []Field{
		numField("va"), numField("vb"), numField("vc"),
		numField("temperature_1"), numField("temperature_2"), numField("temperature_3"),
	})
	reg.SetDeviceType(1, "dev")
	mustSetAliases(t, reg,
		AliasRule{Semantic: "a", Candidates: []string{"va"}},
		AliasRule{Semantic: "b", Candidates: []string{"vb"}},
		AliasRule{Semantic: "c", Candidates: []string{"vc"}},
	)
	mustSetVirtuals(t, reg, standardVirtuals()...)
	return reg
}

// virtualFrame 返回标准帧：va=3, vb=4, vc=5, temperatures=10/20/30。
func virtualFrame() []Reading {
	return readings(
		"va", 3.0, "vb", 4.0, "vc", 5.0,
		"temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0,
	)
}

// TestVirtualFunctions 覆盖 9 个派生函数的正常求值（含嵌套）。
func TestVirtualFunctions(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	frame := virtualFrame()
	tests := []struct {
		selector string
		want     float64
	}{
		{"virtual:prod_ab", 12},    // 3*4
		{"virtual:sum_abc", 12},    // 3+4+5
		{"virtual:diff_abc", -6},   // 3-4-5
		{"virtual:absdiff_ab", 1},  // |3-4|
		{"virtual:ratio_ab", 0.75}, // 3/4
		{"virtual:scaled_a", 7.5},  // 3*2.5
		{"virtual:max_t", 30},      // max(10,20,30)
		{"virtual:min_t", 10},      // min(10,20,30)
		{"virtual:sum_t", 60},      // sum
		{"virtual:sum_prod_c", 17}, // virtual:prod_ab(12) + 5
	}
	for _, tc := range tests {
		t.Run(tc.selector, func(t *testing.T) {
			plan := mustCompile(t, reg, 1, tc.selector)
			r := plan.Eval(frame)
			if !r.OK {
				t.Fatalf("%s Eval unbound: %s", tc.selector, r.Reason)
			}
			if !nearlyEqual(r.Value, tc.want) {
				t.Fatalf("%s = %v want %v", tc.selector, r.Value, tc.want)
			}
			v := EvalPredicate(plan, frame, CmpGT, tc.want-0.001)
			if !v.OK || !v.Satisfied {
				t.Fatalf("%s predicate = %+v want satisfied", tc.selector, v)
			}
		})
	}
}

// TestVirtualMissingInput 验证“任何输入缺失 ⇒ 整体 unbound”（方案 §6.2 不变量 1）。
func TestVirtualMissingInput(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	// 缺 vb。
	frame := readings("va", 3.0, "vc", 5.0, "temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0)
	for _, sel := range []string{"virtual:prod_ab", "virtual:sum_abc", "virtual:ratio_ab"} {
		plan := mustCompile(t, reg, 1, sel)
		if r := plan.Eval(frame); r.OK || r.Reason != reasonMissing {
			t.Fatalf("%s with missing input = %+v want unbound missing", sel, r)
		}
	}
}

// TestVirtualRatioGuards 验证 ratio 分母 0/负数 ⇒ unbound（§6.2 不变量 3）。
func TestVirtualRatioGuards(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	plan := mustCompile(t, reg, 1, "virtual:ratio_ab")
	if r := plan.Eval(readings("va", 1.0, "vb", 0.0)); r.OK || r.Reason != reasonDiv0 {
		t.Fatalf("zero denominator = %+v want div0", r)
	}
	if r := plan.Eval(readings("va", 1.0, "vb", -2.0)); r.OK || r.Reason != reasonNegativeDenominator {
		t.Fatalf("negative denominator = %+v want negative_denominator", r)
	}
}

// TestVirtualNonFinite 验证 NaN/Inf 输入与输出 ⇒ unbound（§6.2 不变量 2）。
func TestVirtualNonFinite(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	prod := mustCompile(t, reg, 1, "virtual:prod_ab")
	if r := prod.Eval(readings("va", math.NaN(), "vb", 2.0)); r.OK || r.Reason != reasonNonFinite {
		t.Fatalf("NaN input = %+v want non_finite", r)
	}
	if r := prod.Eval(readings("va", 1e308, "vb", 10.0)); r.OK || r.Reason != reasonInf {
		t.Fatalf("overflow output = %+v want inf", r)
	}
	// NaN 输出：Inf * 0 = NaN。
	if r := prod.Eval(readings("va", math.Inf(1), "vb", 0.0)); r.OK || r.Reason != reasonNonFinite {
		t.Fatalf("inf input = %+v want non_finite", r)
	}
}

// TestVirtualFamilyReduction 验证 *_of 的族归约与缺员规则（v0.3 裁决）：
//   - max_of/min_of 缺员 ⇒ 降级求值（Degraded=true，与 family min/max 一致）
//   - sum_of 缺员 ⇒ 严格 unbound（统计量失真无安全解释）
//   - 空集 ⇒ 六个聚合一律 unbound（空集规则优先于降级）
func TestVirtualFamilyReduction(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	maxPlan := mustCompile(t, reg, 1, "virtual:max_t")
	minPlan := mustCompile(t, reg, 1, "virtual:min_t")
	sumPlan := mustCompile(t, reg, 1, "virtual:sum_t")

	// 缺 temperature_3（已知 3 个）⇒ max_of/min_of 降级，sum_of unbound。
	partial := readings("temperature_1", 10.0, "temperature_2", 20.0)
	maxVerdict := EvalPredicate(maxPlan, partial, CmpGT, 15)
	if !maxVerdict.OK || !maxVerdict.Degraded || maxVerdict.Reason != reasonPartialFrame {
		t.Fatalf("max_of partial = %+v want OK+degraded partial_frame", maxVerdict)
	}
	if !nearlyEqual(maxVerdict.Value, 20) || !maxVerdict.Satisfied {
		t.Fatalf("max_of partial value/satisfied = %v/%v want 20/true", maxVerdict.Value, maxVerdict.Satisfied)
	}
	minVerdict := EvalPredicate(minPlan, partial, CmpGT, 15)
	if !minVerdict.OK || !minVerdict.Degraded || minVerdict.Reason != reasonPartialFrame ||
		!nearlyEqual(minVerdict.Value, 10) || minVerdict.Satisfied {
		t.Fatalf("min_of partial = %+v want degraded value 10 satisfied false", minVerdict)
	}
	if v := EvalPredicate(sumPlan, partial, CmpGT, 0); v.OK || v.Reason != reasonPartialFrame {
		t.Fatalf("sum_of partial = %+v want unbound partial_frame", v)
	}
	// Plan.Eval：降级仍返回值（Degraded 只在 Verdict 表达）；sum_of 保持 unbound。
	if r := maxPlan.Eval(partial); !r.OK || !nearlyEqual(r.Value, 20) {
		t.Fatalf("max_of partial Eval = %+v want 20", r)
	}
	if r := sumPlan.Eval(partial); r.OK || r.Reason != reasonPartialFrame {
		t.Fatalf("sum_of partial Eval = %+v want unbound", r)
	}

	// 非有限成员视同缺失：max_of/min_of 降级原因 non_finite；sum_of unbound。
	nonFinite := readings("temperature_1", 10.0, "temperature_2", math.NaN(), "temperature_3", 30.0)
	nanVerdict := EvalPredicate(maxPlan, nonFinite, CmpGT, 15)
	if !nanVerdict.OK || !nanVerdict.Degraded || nanVerdict.Reason != reasonNonFinite || !nearlyEqual(nanVerdict.Value, 30) {
		t.Fatalf("max_of non-finite = %+v want degraded non_finite value 30", nanVerdict)
	}
	if v := EvalPredicate(sumPlan, nonFinite, CmpGT, 0); v.OK || v.Reason != reasonNonFinite {
		t.Fatalf("sum_of non-finite = %+v want unbound non_finite", v)
	}

	// 已知成员全部缺失（本帧为空）⇒ 空集/缺员规则优先：max_of/min_of 也 unbound。
	if r := maxPlan.Eval(nil); r.OK || r.Reason != reasonPartialFrame {
		t.Fatalf("empty frame with known members = %+v want unbound partial_frame", r)
	}

	// 齐全时正常求值、不降级。
	full := readings("temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0)
	fullVerdict := EvalPredicate(maxPlan, full, CmpGTE, 30)
	if !fullVerdict.OK || fullVerdict.Degraded || fullVerdict.Reason != "" || !nearlyEqual(fullVerdict.Value, 30) {
		t.Fatalf("max_of full = %+v want clean bound 30", fullVerdict)
	}
}

// TestVirtualDegradationPropagates 验证嵌套派生的降级原因向顶层传播，unbound 传染。
func TestVirtualDegradationPropagates(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{
		numField("va"), numField("temperature_1"), numField("temperature_2"), numField("temperature_3"),
	})
	reg.SetDeviceType(1, "dev")
	mustSetAliases(t, reg, AliasRule{Semantic: "a", Candidates: []string{"va"}})
	mustSetVirtuals(t, reg,
		VirtualDef{Name: "max_t", Fn: "max_of", Args: []string{"temperature_*"}},
		VirtualDef{Name: "margin", Fn: "diff", Args: []string{"virtual:max_t", "@a"}},
		VirtualDef{Name: "sum_t", Fn: "sum_of", Args: []string{"temperature_*"}},
		VirtualDef{Name: "sum_margin", Fn: "diff", Args: []string{"virtual:sum_t", "@a"}},
	)
	partial := readings("va", 5.0, "temperature_1", 10.0, "temperature_2", 20.0)
	// 内层 max_t 降级 ⇒ 外层 margin 也标 degraded，但值仍正确（20-5=15）。
	v := EvalPredicate(mustCompile(t, reg, 1, "virtual:margin"), partial, CmpGT, 10)
	if !v.OK || !v.Degraded || v.Reason != reasonPartialFrame || !nearlyEqual(v.Value, 15) || !v.Satisfied {
		t.Fatalf("nested degradation = %+v want degraded value 15 satisfied", v)
	}
	// 内层 sum_t unbound ⇒ 外层整体 unbound（缺失传染）。
	if v := EvalPredicate(mustCompile(t, reg, 1, "virtual:sum_margin"), partial, CmpGT, 0); v.OK || v.Reason != reasonPartialFrame {
		t.Fatalf("nested unbound = %+v want unbound partial_frame", v)
	}
}

// TestVirtualEmptyFamilyKnown 验证族在已知集合里为空 ⇒ 空集 unbound。
func TestVirtualEmptyFamilyKnown(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("va")})
	reg.SetDeviceType(1, "dev")
	mustSetAliases(t, reg, AliasRule{Semantic: "a", Candidates: []string{"va"}})
	mustSetVirtuals(t, reg, VirtualDef{Name: "max_t", Fn: "max_of", Args: []string{"temperature_*"}})
	plan := mustCompile(t, reg, 1, "virtual:max_t")
	if r := plan.Eval(readings("va", 1.0)); r.OK || r.Reason != reasonEmptyFrame {
		t.Fatalf("no known family = %+v want unbound empty_frame", r)
	}
}

// TestVirtualDefinitionValidation 覆盖参数个数/类型/环/深度等配置错误（fail-closed）。
func TestVirtualDefinitionValidation(t *testing.T) {
	newReg := func(cfg Config) *Registry {
		reg := New(cfg)
		reg.RegisterDeclarations("dev", []Field{numField("va"), numField("vb")})
		return reg
	}
	tests := []struct {
		name string
		cfg  Config
		defs []VirtualDef
		want error
	}{
		{
			name: "unknown_fn",
			defs: []VirtualDef{{Name: "v", Fn: "frobnicate", Args: []string{"va"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "product_one_arg",
			defs: []VirtualDef{{Name: "v", Fn: "product", Args: []string{"va"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "abs_diff_three_args",
			defs: []VirtualDef{{Name: "v", Fn: "abs_diff", Args: []string{"va", "vb", "va"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "scale_without_factor",
			defs: []VirtualDef{{Name: "v", Fn: "scale", Args: []string{"va"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "scale_nan_factor",
			defs: []VirtualDef{{Name: "v", Fn: "scale", Args: []string{"va"}, Scale: math.NaN()}},
			want: ErrVirtualInvalid,
		},
		{
			name: "product_family_arg",
			defs: []VirtualDef{{Name: "v", Fn: "product", Args: []string{"va", "temperature_*"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "max_of_literal_arg",
			defs: []VirtualDef{{Name: "v", Fn: "max_of", Args: []string{"va"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "max_of_explicit_aggregate",
			defs: []VirtualDef{{Name: "v", Fn: "max_of", Args: []string{"temperature_*:max"}}},
			want: ErrVirtualInvalid,
		},
		{
			name: "unknown_virtual_ref",
			defs: []VirtualDef{{Name: "v", Fn: "scale", Args: []string{"virtual:nope"}, Scale: 2}},
			want: ErrVirtualInvalid,
		},
		{
			name: "self_cycle",
			defs: []VirtualDef{{Name: "v", Fn: "scale", Args: []string{"virtual:v"}, Scale: 2}},
			want: ErrVirtualCycle,
		},
		{
			name: "mutual_cycle",
			defs: []VirtualDef{
				{Name: "a", Fn: "scale", Args: []string{"virtual:b"}, Scale: 2},
				{Name: "b", Fn: "scale", Args: []string{"virtual:a"}, Scale: 2},
			},
			want: ErrVirtualCycle,
		},
		{
			name: "depth_exceeded",
			cfg:  Config{Enabled: true, MaxVirtualDepth: 2},
			defs: []VirtualDef{
				{Name: "a", Fn: "scale", Args: []string{"va"}, Scale: 2},
				{Name: "b", Fn: "scale", Args: []string{"virtual:a"}, Scale: 2},
				{Name: "c", Fn: "scale", Args: []string{"virtual:b"}, Scale: 2},
			},
			want: ErrVirtualInvalid,
		},
		{
			name: "duplicate_name",
			defs: []VirtualDef{
				{Name: "v", Fn: "scale", Args: []string{"va"}, Scale: 2},
				{Name: "v", Fn: "scale", Args: []string{"va"}, Scale: 3},
			},
			want: ErrVirtualInvalid,
		},
		{
			name: "invalid_name",
			defs: []VirtualDef{{Name: "V", Fn: "scale", Args: []string{"va"}, Scale: 2}},
			want: ErrVirtualInvalid,
		},
		{
			name: "bad_arg_selector",
			defs: []VirtualDef{{Name: "v", Fn: "scale", Args: []string{"bad selector"}, Scale: 2}},
			want: ErrVirtualInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if cfg == (Config{}) {
				cfg = enabledConfig()
			}
			reg := newReg(cfg)
			if err := reg.SetVirtuals(tc.defs); !errors.Is(err, tc.want) {
				t.Fatalf("SetVirtuals err = %v want %v", err, tc.want)
			}
		})
	}
}

// TestVirtualUnknownSemanticWithAliases 验证派生引用未定义语义 ⇒ ErrUnknownSemantic。
func TestVirtualUnknownSemanticWithAliases(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("va")})
	mustSetAliases(t, reg, AliasRule{Semantic: "a", Candidates: []string{"va"}})
	err := reg.SetVirtuals([]VirtualDef{{Name: "v", Fn: "scale", Args: []string{"@nope"}, Scale: 2}})
	if !errors.Is(err, ErrUnknownSemantic) {
		t.Fatalf("err = %v want ErrUnknownSemantic", err)
	}
}

// TestVirtualLoadOrderIndependence 验证 先派生后别名 也能通过 Validate 交叉校验。
func TestVirtualLoadOrderIndependence(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("va"), numField("vc")})
	reg.SetDeviceType(1, "dev")
	// 故意先装 virtuals（此时别名为空，语义引用暂不校验）。
	mustSetVirtuals(t, reg, VirtualDef{Name: "sum_ac", Fn: "sum", Args: []string{"@a", "@c"}})
	mustSetAliases(t, reg,
		AliasRule{Semantic: "a", Candidates: []string{"va"}},
		AliasRule{Semantic: "c", Candidates: []string{"vc"}},
	)
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate after out-of-order load = %v", err)
	}
	plan := mustCompile(t, reg, 1, "virtual:sum_ac")
	r := plan.Eval(readings("va", 2.0, "vc", 3.0))
	if !r.OK || !nearlyEqual(r.Value, 5) {
		t.Fatalf("sum_ac = %+v want 5", r)
	}
}

// TestVirtualSetFailureKeepsOld 验证 SetVirtuals 失败不改动当前定义。
func TestVirtualSetFailureKeepsOld(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	if err := reg.SetVirtuals([]VirtualDef{{Name: "broken", Fn: "product", Args: []string{"va"}}}); err == nil {
		t.Fatal("expected invalid virtual error")
	}
	plan := mustCompile(t, reg, 1, "virtual:prod_ab")
	if r := plan.Eval(virtualFrame()); !r.OK || !nearlyEqual(r.Value, 12) {
		t.Fatalf("old virtual lost: %+v", r)
	}
	if defs := reg.Virtuals(); len(defs) != len(standardVirtuals()) {
		t.Fatalf("Virtuals after failed set = %d", len(defs))
	}
}

// TestVirtualFnsWhitelist 验证唯一函数表（9 个）与 /schema 描述。
func TestVirtualFnsWhitelist(t *testing.T) {
	fns := VirtualFns()
	if len(fns) != 9 {
		t.Fatalf("VirtualFns = %d want 9", len(fns))
	}
	want := []string{"product", "sum", "diff", "abs_diff", "ratio", "scale", "sum_of", "max_of", "min_of"}
	for _, name := range want {
		found := false
		for _, fn := range fns {
			if fn.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing virtual fn %q", name)
		}
	}
}

// TestVirtualResolve 验证控制面派生解析（依赖集合 / unbound）。
func TestVirtualResolve(t *testing.T) {
	reg := newVirtualTestRegistry(t, enabledConfig())
	res, err := reg.Resolve(1, "virtual:sum_abc")
	if err != nil {
		t.Fatalf("Resolve virtual failed: %v", err)
	}
	if res.Status != StatusResolved || res.Kind != KindVirtual {
		t.Fatalf("resolved = %+v", res)
	}
	want := []string{"va", "vb", "vc"}
	if len(res.Matched) != len(want) {
		t.Fatalf("deps = %v want %v", res.Matched, want)
	}
	for i := range want {
		if res.Matched[i] != want[i] {
			t.Fatalf("deps = %v want %v", res.Matched, want)
		}
	}
	// 自定义 literal 依赖缺失 ⇒ unbound（不是错误）。
	reg2 := New(enabledConfig())
	reg2.RegisterDeclarations("dev", []Field{numField("va")})
	reg2.SetDeviceType(1, "dev")
	mustSetVirtuals(t, reg2, VirtualDef{Name: "v", Fn: "scale", Args: []string{"rsoc_missing"}, Scale: 2})
	res, err = reg2.Resolve(1, "virtual:v")
	if err != nil {
		t.Fatalf("Resolve unbound virtual must not error: %v", err)
	}
	if res.Status != StatusUnbound || res.Reason != reasonMissing {
		t.Fatalf("unbound virtual = %+v", res)
	}
	if _, err := reg.Resolve(1, "virtual:nope"); !errors.Is(err, ErrUnknownVirtual) {
		t.Fatalf("unknown virtual err = %v", err)
	}
}
