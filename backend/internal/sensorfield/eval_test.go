package sensorfield

import (
	"errors"
	"math"
	"testing"
)

// cmpOracle 是测试侧的独立比较符实现（规格 oracle，与本包实现无一共享代码）。
func cmpOracle(op CmpOp, value, threshold float64) bool {
	switch op {
	case CmpGT:
		return value > threshold
	case CmpGTE:
		return value >= threshold
	case CmpLT:
		return value < threshold
	case CmpLTE:
		return value <= threshold
	case CmpEQ:
		return value == threshold
	case CmpNEQ:
		return value != threshold
	}
	panic("unknown comparator")
}

// reduceOracle 是测试侧的独立归约实现。
func reduceOracle(agg Aggregate, valid []float64) float64 {
	switch agg {
	case AggMin:
		out := valid[0]
		for _, v := range valid[1:] {
			if v < out {
				out = v
			}
		}
		return out
	case AggMax:
		out := valid[0]
		for _, v := range valid[1:] {
			if v > out {
				out = v
			}
		}
		return out
	case AggAvg:
		sum := 0.0
		for _, v := range valid {
			sum += v
		}
		return sum / float64(len(valid))
	case AggSum:
		sum := 0.0
		for _, v := range valid {
			sum += v
		}
		return sum
	}
	panic("not a reducer")
}

// cellExpect 是 216 格真值表的逐格手写期望。
type cellExpect struct {
	bound  bool
	reason string // bound 时=降级原因（空=未降级）；unbound 时=unbound 原因
}

// TestEvalPredicateTruthTable216 覆盖 6 聚合 × 6 比较符 × 6 场景 = 216 格
// （方案 §5.3 / 数据语义规范 §5.3 门禁）。
//
// 场景与期望全部手写（独立 oracle），不从实现反推：
//
//	S1 空集:     已知 ∅，本帧 ∅                        ⇒ 六个聚合全 unbound
//	S2 单元素:   temperature_1=50                      ⇒ 全 bound
//	S3 多元素齐全: 10,20,30                            ⇒ 全 bound
//	S4 缺员:     已知 3，本帧 10,20                    ⇒ any bound；all/avg/sum unbound；
//	                                                     min/max 降级求值（partial_frame）
//	S5 含 NaN:   10,NaN,30                             ⇒ any bound；all/avg/sum unbound；
//	                                                     min/max 降级（non_finite）
//	S6 非数值:   已知 t2=bitmask（本帧有值也不参与）    ⇒ any bound；all/avg/sum unbound；
//	                                                     min/max 降级（partial_frame）
func TestEvalPredicateTruthTable216(t *testing.T) {
	aggs := []Aggregate{AggAny, AggAll, AggMin, AggMax, AggAvg, AggSum}
	ops := []CmpOp{CmpGT, CmpGTE, CmpLT, CmpLTE, CmpEQ, CmpNEQ}

	partial := func(reason string) map[Aggregate]cellExpect {
		return map[Aggregate]cellExpect{
			AggAny: {bound: true},
			AggAll: {bound: false, reason: reason},
			AggMin: {bound: true, reason: reason}, // 降级求值
			AggMax: {bound: true, reason: reason}, // 降级求值
			AggAvg: {bound: false, reason: reason},
			AggSum: {bound: false, reason: reason},
		}
	}
	allBound := func() map[Aggregate]cellExpect {
		m := map[Aggregate]cellExpect{}
		for _, a := range aggs {
			m[a] = cellExpect{bound: true}
		}
		return m
	}
	allUnbound := func(reason string) map[Aggregate]cellExpect {
		m := map[Aggregate]cellExpect{}
		for _, a := range aggs {
			m[a] = cellExpect{bound: false, reason: reason}
		}
		return m
	}

	type scenario struct {
		name      string
		known     []Field
		frame     []Reading
		valid     []float64
		threshold float64
		expect    map[Aggregate]cellExpect
	}
	scenarios := []scenario{
		{
			name:      "S1_empty",
			known:     nil,
			frame:     nil,
			valid:     nil,
			threshold: 15,
			expect:    allUnbound(reasonEmptyFrame),
		},
		{
			name:      "S2_single",
			known:     []Field{numField("temperature_1")},
			frame:     readings("temperature_1", 50.0),
			valid:     []float64{50},
			threshold: 50,
			expect:    allBound(),
		},
		{
			name:      "S3_complete",
			known:     []Field{numField("temperature_1"), numField("temperature_2"), numField("temperature_3")},
			frame:     readings("temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0),
			valid:     []float64{10, 20, 30},
			threshold: 15,
			expect:    allBound(),
		},
		{
			name:      "S4_partial",
			known:     []Field{numField("temperature_1"), numField("temperature_2"), numField("temperature_3")},
			frame:     readings("temperature_1", 10.0, "temperature_2", 20.0),
			valid:     []float64{10, 20},
			threshold: 15,
			expect:    partial(reasonPartialFrame),
		},
		{
			name:      "S5_nan",
			known:     []Field{numField("temperature_1"), numField("temperature_2"), numField("temperature_3")},
			frame:     readings("temperature_1", 10.0, "temperature_2", math.NaN(), "temperature_3", 30.0),
			valid:     []float64{10, 30},
			threshold: 15,
			expect:    partial(reasonNonFinite),
		},
		{
			name: "S6_non_numeric",
			known: []Field{
				numField("temperature_1"),
				typedField("temperature_2", TypeBitmask, ""),
				numField("temperature_3"),
			},
			// 本帧甚至带了 temperature_2=20，但已知类型 bitmask ⇒ 必须被排除。
			frame:     readings("temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0),
			valid:     []float64{10, 30},
			threshold: 15,
			expect:    partial(reasonPartialFrame),
		},
	}

	cells := 0
	for _, sc := range scenarios {
		reg := New(enabledConfig())
		reg.RegisterDeclarations("truth_dev", sc.known)
		reg.SetDeviceType(7, "truth_dev")
		if got := reg.KnownFields(7); len(got) != len(sc.known) {
			t.Fatalf("%s: known fields = %d want %d", sc.name, len(got), len(sc.known))
		}
		for _, agg := range aggs {
			selector := "temperature_*:" + string(agg)
			plan := mustCompile(t, reg, 7, selector)
			exp := sc.expect[agg]
			for _, op := range ops {
				cells++
				name := sc.name + "/" + string(agg) + "/" + string(op)
				t.Run(name, func(t *testing.T) {
					v := EvalPredicate(plan, sc.frame, op, sc.threshold)
					if !exp.bound {
						if v.OK {
							t.Fatalf("want unbound, got OK value=%v", v.Value)
						}
						if v.Reason != exp.reason {
							t.Fatalf("unbound reason = %q want %q", v.Reason, exp.reason)
						}
						if v.Degraded {
							t.Fatalf("unbound cell must not be degraded")
						}
						return
					}
					if !v.OK {
						t.Fatalf("want bound, got unbound reason=%q", v.Reason)
					}
					// 期望的参与比较值
					var wantValue float64
					var wantSatisfied bool
					switch agg {
					case AggAny:
						wantValue = sc.valid[0]
						wantSatisfied = false
						for _, val := range sc.valid {
							if cmpOracle(op, val, sc.threshold) {
								wantSatisfied = true
								break
							}
						}
					case AggAll:
						wantValue = sc.valid[0]
						wantSatisfied = true
						for _, val := range sc.valid {
							if !cmpOracle(op, val, sc.threshold) {
								wantSatisfied = false
								break
							}
						}
					default:
						wantValue = reduceOracle(agg, sc.valid)
						wantSatisfied = cmpOracle(op, wantValue, sc.threshold)
					}
					if v.Satisfied != wantSatisfied {
						t.Fatalf("Satisfied = %v want %v (value=%v threshold=%v)", v.Satisfied, wantSatisfied, v.Value, sc.threshold)
					}
					if !nearlyEqual(v.Value, wantValue) {
						t.Fatalf("Value = %v want %v", v.Value, wantValue)
					}
					if exp.reason == "" {
						if v.Degraded {
							t.Fatalf("unexpected degraded: reason=%q", v.Reason)
						}
						if v.Reason != "" {
							t.Fatalf("bound non-degraded cell has reason %q", v.Reason)
						}
					} else {
						if !v.Degraded {
							t.Fatalf("want degraded=true (reason=%q)", exp.reason)
						}
						if v.Reason != exp.reason {
							t.Fatalf("degraded reason = %q want %q", v.Reason, exp.reason)
						}
					}
				})
			}
		}
	}
	if cells != 216 {
		t.Fatalf("truth table cells = %d want 216", cells)
	}
}

// TestFamilyPlanEvalContract 验证 Plan.Eval 的契约（reducer 取值；any/all fail-closed）。
func TestFamilyPlanEvalContract(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("temperature_1"), numField("temperature_2"), numField("temperature_3")})
	reg.SetDeviceType(1, "dev")
	full := readings("temperature_1", 10.0, "temperature_2", 20.0, "temperature_3", 30.0)
	partial := readings("temperature_1", 10.0, "temperature_2", 20.0)

	// reducer：Plan.Eval 正常返回归约值。
	sumPlan := mustCompile(t, reg, 1, "temperature_*:sum")
	if r := sumPlan.Eval(full); !r.OK || !nearlyEqual(r.Value, 60) {
		t.Fatalf("sum Eval = %+v want 60", r)
	}
	if r := sumPlan.Eval(partial); r.OK || r.Reason != reasonPartialFrame {
		t.Fatalf("sum partial Eval = %+v want unbound partial_frame", r)
	}
	// min/max 缺员降级：Plan.Eval 仍返回值（Degraded 只在 Verdict 表达）。
	maxPlan := mustCompile(t, reg, 1, "temperature_*:max")
	if r := maxPlan.Eval(partial); !r.OK || !nearlyEqual(r.Value, 20) {
		t.Fatalf("max partial Eval = %+v want 20", r)
	}
	// any/all 无法脱离比较符：Plan.Eval 必须 fail-closed 返回 unbound。
	anyPlan := mustCompile(t, reg, 1, "temperature_*:any")
	if r := anyPlan.Eval(full); r.OK || r.Reason != reasonAggregateNeedsComparator {
		t.Fatalf("any Eval = %+v want unbound aggregate_requires_comparator", r)
	}
	allPlan := mustCompile(t, reg, 1, "temperature_*:all")
	if r := allPlan.Eval(full); r.OK || r.Reason != reasonAggregateNeedsComparator {
		t.Fatalf("all Eval = %+v want unbound aggregate_requires_comparator", r)
	}
	// 空集：六个聚合一律 unbound（含 max）。
	emptyReg := New(enabledConfig())
	emptyReg.SetDeviceType(2, "dev")
	emptyMax := mustCompile(t, emptyReg, 2, "temperature_*:max")
	if r := emptyMax.Eval(nil); r.OK || r.Reason != reasonEmptyFrame {
		t.Fatalf("empty max Eval = %+v want unbound empty_frame", r)
	}
}

// TestEvalPredicateReasonDistinction 显式验证三种 unbound 原因互不相同（评审 B8 要求）：
// 空集（已知集合为空）/ 本帧缺已知成员 / 字段值非有限 必须可由审计区分。
func TestEvalPredicateReasonDistinction(t *testing.T) {
	// 空集：已知集合为空、本帧为空。
	emptyReg := New(enabledConfig())
	emptyReg.SetDeviceType(1, "dev")
	emptyPlan := mustCompile(t, emptyReg, 1, "temperature_*:avg")
	empty := EvalPredicate(emptyPlan, nil, CmpGT, 0)
	if empty.OK || empty.Reason != reasonEmptyFrame {
		t.Fatalf("empty set reason = %q want %q", empty.Reason, reasonEmptyFrame)
	}

	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("temperature_1"), numField("temperature_2")})
	reg.SetDeviceType(1, "dev")
	plan := mustCompile(t, reg, 1, "temperature_*:avg")

	absent := EvalPredicate(plan, readings("temperature_1", 1.0), CmpGT, 0)
	if absent.OK || absent.Reason != reasonPartialFrame {
		t.Fatalf("partial frame reason = %q want %q", absent.Reason, reasonPartialFrame)
	}
	nonFinite := EvalPredicate(plan, readings("temperature_1", 1.0, "temperature_2", math.Inf(1)), CmpGT, 0)
	if nonFinite.OK || nonFinite.Reason != reasonNonFinite {
		t.Fatalf("non-finite reason = %q want %q", nonFinite.Reason, reasonNonFinite)
	}
	if empty.Reason == absent.Reason || absent.Reason == nonFinite.Reason || empty.Reason == nonFinite.Reason {
		t.Fatal("unbound reasons must be distinguishable")
	}
}

// TestLiteralEvalEquivalence 验证 literal 路径与历史 matchField 语义等价（取第一个同名）。
func TestLiteralEvalEquivalence(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	plan := mustCompile(t, reg, 1, "rsoc")

	// 取第一个同名（与 matchField 一致，不取最后一个也不聚合）。
	frame := readings("rsoc", 33.0, "rsoc", 99.0)
	if r := plan.Eval(frame); !r.OK || !nearlyEqual(r.Value, 33) {
		t.Fatalf("literal first-match = %+v want 33", r)
	}
	if v := EvalPredicate(plan, frame, CmpGT, 30); !v.OK || !v.Satisfied {
		t.Fatalf("literal predicate = %+v want satisfied", v)
	}
	if r := plan.Eval(readings("other", 1.0)); r.OK || r.Reason != reasonMissing {
		t.Fatalf("missing literal = %+v", r)
	}
	if r := plan.Eval(readings("rsoc", math.NaN())); r.OK || r.Reason != reasonNonFinite {
		t.Fatalf("nan literal = %+v", r)
	}
	if v := EvalPredicate(plan, frame, CmpOp("bad"), 0); v.OK || v.Reason != reasonInvalidComparator {
		t.Fatalf("invalid comparator = %+v", v)
	}
}

// TestLiteralZeroAllocation 是门禁 #10 的底座：literal Eval 零分配。
func TestLiteralZeroAllocation(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	plan := mustCompile(t, reg, 1, "rsoc")
	frame := readings("rsoc", 42.0, "total_voltage", 48.0)
	allocs := testing.AllocsPerRun(1000, func() {
		_ = plan.Eval(frame)
	})
	if allocs != 0 {
		t.Fatalf("literal Eval allocations = %v/op, want 0", allocs)
	}
}

// TestNonNumericTypeAndUnitFallback 验证 value_type 与伪单位兜底都视为缺失。
func TestNonNumericTypeAndUnitFallback(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{
		typedField("protection_status", TypeBitmask, ""),
		{Name: "fet_status", Unit: "bitmask"}, // 迁移期兜底：unit 伪单位
		numField("rsoc"),
	})
	reg.SetDeviceType(1, "dev")
	frame := readings("protection_status", 1.0, "fet_status", 1.0, "rsoc", 50.0)

	bitmaskPlan := mustCompile(t, reg, 1, "protection_status")
	if r := bitmaskPlan.Eval(frame); r.OK || r.Reason != reasonNonNumeric {
		t.Fatalf("bitmask literal = %+v want non_numeric", r)
	}
	unitPlan := mustCompile(t, reg, 1, "fet_status")
	if r := unitPlan.Eval(frame); r.OK || r.Reason != reasonNonNumeric {
		t.Fatalf("bitmask-unit literal = %+v want non_numeric", r)
	}
	// 未知类型且无伪单位 ⇒ 按 number 处理（兼容存量）。
	unknownPlan := mustCompile(t, reg, 1, "rsoc")
	if r := unknownPlan.Eval(frame); !r.OK {
		t.Fatalf("unknown type should be numeric: %+v", r)
	}
	// meta 覆盖：把 rsoc 标成 string ⇒ 非数值。
	reg.SetFieldMeta([]Field{{Name: "rsoc", ValueType: TypeString}})
	metaPlan := mustCompile(t, reg, 1, "rsoc")
	if r := metaPlan.Eval(frame); r.OK || r.Reason != reasonNonNumeric {
		t.Fatalf("meta string literal = %+v want non_numeric", r)
	}
}

// TestFamilyDerivedAggregatesExcluded 验证 family 只匹配 ^prefix_[0-9]+$（不含 max/min/avg）。
func TestFamilyDerivedAggregatesExcluded(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{
		numField("cell_voltage_max"), numField("cell_voltage_min"), numField("cell_voltage_avg"),
		numField("cell_voltage_1"), numField("cell_voltage_2"), numField("cell_voltage_10"),
	})
	reg.SetDeviceType(1, "dev")
	res, err := reg.Resolve(1, "cell_voltage_*")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if res.Status != StatusResolved {
		t.Fatalf("status = %q", res.Status)
	}
	want := []string{"cell_voltage_1", "cell_voltage_2", "cell_voltage_10"} // 数值序
	if len(res.Matched) != len(want) {
		t.Fatalf("matched = %v want %v", res.Matched, want)
	}
	for i := range want {
		if res.Matched[i] != want[i] {
			t.Fatalf("matched = %v want %v", res.Matched, want)
		}
	}
	// sum 只归约数字后缀成员。
	plan := mustCompile(t, reg, 1, "cell_voltage_*:sum")
	frame := readings(
		"cell_voltage_max", 100.0, "cell_voltage_min", 1.0, "cell_voltage_avg", 50.0,
		"cell_voltage_1", 2.0, "cell_voltage_2", 3.0, "cell_voltage_10", 5.0,
	)
	if r := plan.Eval(frame); !r.OK || !nearlyEqual(r.Value, 10) {
		t.Fatalf("family sum = %+v want 10 (derived aggregates excluded)", r)
	}
}

// TestFamilyTruncation 验证 max_family_size 截断策略（数值序保留前 N）。
func TestFamilyTruncation(t *testing.T) {
	reg := New(Config{Enabled: true, MaxFamilySize: 2})
	reg.RegisterDeclarations("dev", []Field{
		numField("temperature_1"), numField("temperature_2"), numField("temperature_3"),
	})
	reg.SetDeviceType(1, "dev")
	res, err := reg.Resolve(1, "temperature_*")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if res.Status != StatusResolved || len(res.Matched) != 2 || res.Reason != reasonFamilyTruncated {
		t.Fatalf("truncation result = %+v", res)
	}
	if res.Matched[0] != "temperature_1" || res.Matched[1] != "temperature_2" {
		t.Fatalf("truncated members = %v", res.Matched)
	}
}

// TestKillSwitch 验证 kill switch：关闭后特殊 selector 不触发且可审计，字面量不变。
func TestKillSwitch(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("temperature_1"), numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	specialPlan := mustCompile(t, reg, 1, "temperature_*:max")
	literalPlan := mustCompile(t, reg, 1, "rsoc")
	frame := readings("temperature_1", 50.0, "rsoc", 40.0)
	if v := EvalPredicate(specialPlan, frame, CmpGT, 10); !v.OK {
		t.Fatalf("enabled special plan should be bound: %+v", v)
	}

	reg.SetEnabled(false)
	if reg.Enabled() {
		t.Fatal("Enabled() still true")
	}
	// 已编译计划：求值读原子开关 ⇒ unbound + 可审计 reason。
	if v := EvalPredicate(specialPlan, frame, CmpGT, 10); v.OK || v.Reason != reasonDisabled {
		t.Fatalf("disabled special plan = %+v want unbound disabled", v)
	}
	// 关闭后特殊 selector 不能再编译。
	if _, err := reg.Compile(1, "temperature_*:max"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Compile special err = %v want ErrDisabled", err)
	}
	if _, err := reg.Resolve(1, "@nope"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Resolve special err = %v want ErrDisabled (even unknown semantic)", err)
	}
	// 字面量行为不变。
	litPlan := mustCompile(t, reg, 1, "rsoc")
	if v := EvalPredicate(litPlan, frame, CmpGT, 30); !v.OK || !v.Satisfied {
		t.Fatalf("literal predicate under kill switch = %+v", v)
	}
	if v := EvalPredicate(literalPlan, frame, CmpGT, 30); !v.OK || !v.Satisfied {
		t.Fatalf("pre-disabled literal plan = %+v", v)
	}
	if res, err := reg.Resolve(1, "rsoc"); err != nil || res.Status != StatusResolved {
		t.Fatalf("literal Resolve under kill switch = %+v err=%v", res, err)
	}
	// 关闭态读接口仍可用（/sensor-fields 与 /schema 要求）。
	if fields := reg.KnownFields(1); len(fields) != 2 {
		t.Fatalf("KnownFields under kill switch = %v", fields)
	}
	if cfg := reg.Config(); !cfg.Enabled && cfg.MaxSelectorChars != 0 {
		_ = cfg // Config 返回构造时的 Enabled=false 值；仅验证可读
	}
}

// TestCompileCacheEpochInvalidation 验证编译缓存随 epoch 失效（新成员进入 plan）。
func TestCompileCacheEpochInvalidation(t *testing.T) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("temperature_1")})
	reg.SetDeviceType(1, "dev")
	p1 := mustCompile(t, reg, 1, "temperature_*:max")
	e1 := PlanEpoch(p1)
	if e1 == 0 {
		t.Fatal("PlanEpoch = 0, plan must carry compile-time epoch")
	}
	// 同 epoch 命中缓存：返回同一 plan。
	p1b := mustCompile(t, reg, 1, "temperature_*:max")
	if p1b != p1 {
		t.Fatal("same epoch should hit compile cache (same plan pointer)")
	}
	// 新观测出现 ⇒ epoch+1，缓存失效，新 plan 能看到新成员。
	reg.SetObservations(1, []Field{numField("temperature_1"), numField("temperature_2")})
	if reg.Epoch() <= e1 {
		t.Fatalf("epoch did not advance: %d <= %d", reg.Epoch(), e1)
	}
	p2 := mustCompile(t, reg, 1, "temperature_*:max")
	if p2 == p1 {
		t.Fatal("cache must miss after epoch change")
	}
	if PlanEpoch(p2) <= e1 {
		t.Fatalf("new plan epoch = %d want > %d", PlanEpoch(p2), e1)
	}
	frame := readings("temperature_1", 10.0, "temperature_2", 20.0)
	if v := EvalPredicate(p2, frame, CmpGT, 15); !v.OK || !v.Satisfied || !nearlyEqual(v.Value, 20) {
		t.Fatalf("new member not visible: %+v", v)
	}
	// 旧 plan 仍只知道 temperature_1（不可变）：F_now ⊆ F_known ⇒ 忽略未知帧成员，max=10 不降级。
	oldV := EvalPredicate(p1, frame, CmpGT, 15)
	if !oldV.OK || !nearlyEqual(oldV.Value, 10) || oldV.Degraded {
		t.Fatalf("old plan should stay on old snapshot (unknown frame member ignored): %+v", oldV)
	}
}
