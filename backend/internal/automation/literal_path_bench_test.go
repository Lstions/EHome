package automation

// 门禁 #10a：生产求值链路 literal 快路径基准（Lead 2026-10-21 裁决拆分口径）。
//
// 测的是**真实生产调用**：Evaluator.evalSelector 的 KindLiteral 分支
// （含 plan provider 快照、编译缓存命中判定、指令：markRuleResolved、legacy matchField+compare）。
// 基线 = 本包 matchField + compare（与 main 基线 d406dc2d internal/alert/evaluator.go
// 的 matchField/compare 逐字节相同；2026-10-21 由集成批次原样迁入，本文件不改其代码）。
//
// 判据（Lead 裁决）：#10a 相对 main matchField 基线 ≤ +5% 才判 PASS/FAIL；
// #10b（新层抽象成本）只做观测，不阻断。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"
)

// newLiteralBenchEvaluator 构造只含 literal 快路径所需状态的最小 Evaluator
// （不触 DB、不 LoadRules；literal 成功路径不需要这些）。
func newLiteralBenchEvaluator(prov PlanProvider) *Evaluator {
	e := &Evaluator{
		plans:     newPlanCache(),
		ruleStats: map[uint]*ruleStat{},
	}
	e.planProvider = prov
	return e
}

// literalBenchFrame 返回 16 字段帧、目标字段在首位（与基线 bench 同形）。
func literalBenchFrame() []parser.Field {
	f := make([]parser.Field, 0, 16)
	f = append(f, parser.Field{Name: "rsoc", Value: 42, Unit: "%"})
	for i := 0; i < 15; i++ {
		f = append(f, parser.Field{Name: "other_field", Value: float64(i)})
	}
	return f
}

// literalBenchRule 返回一条纯字面量阈值规则。
func literalBenchRule() models.AutomationRule {
	return models.AutomationRule{
		ID:                  1,
		TriggerSensorName:   "rsoc",
		TriggerComparator:   "gt",
		TriggerThreshold:    30,
		TriggerEdgeDeviceID: 1,
	}
}

// BenchmarkAutomationLiteralPath 是生产链路（evalSelector 的 KindLiteral 快路径）基准。
func BenchmarkAutomationLiteralPath(b *testing.B) {
	prov := newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindLiteral}
	})
	ev := newLiteralBenchEvaluator(prov)
	rule := literalBenchRule()
	frame := literalBenchFrame()
	at := time.Now()
	// warmup：编译并写入 plan 缓存，基准只测稳态生产路径。
	if ok, _, _ := ev.evalSelector(rule.ID, 0, 1, "rsoc", "gt", 30, frame, at); !ok {
		b.Fatal("literal path warmup failed")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, satisfied, _ := ev.evalSelector(rule.ID, 0, 1, "rsoc", "gt", 30, frame, at)
		if !ok || !satisfied {
			b.Fatalf("literal path broken: ok=%v satisfied=%v", ok, satisfied)
		}
	}
}

// BenchmarkAutomationLiteralPathBaseline 是 main 基线（matchField + compare，纯字面量路径）。
func BenchmarkAutomationLiteralPathBaseline(b *testing.B) {
	frame := literalBenchFrame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, found := matchField(frame, "rsoc")
		if !found || !compare("gt", v, 30) {
			b.Fatal("baseline broken")
		}
	}
}

// benchmarkLiteralFastPathShortCircuit 是**优化原型**（仅基准用，不在生产路径）：
// 若把 literal/legacy（非特殊形态）在 provider/编译缓存之前直接短路到历史
// matchField+compare（与 G-6 逐字节等价），生产开销可回到基线附近。
// 该原型用于量化"建议给 auto-analyst 的 6 行短路改动"的预期收益。
func benchmarkLiteralFastPathShortCircuit(fields []parser.Field, selector, comparator string, threshold float64) (bool, bool, float64) {
	if !isSpecialSelector(selector) {
		value, found := matchField(fields, selector)
		if !found {
			return false, false, 0
		}
		return true, compare(comparator, value, threshold), value
	}
	return false, false, 0
}

// BenchmarkAutomationLiteralPathAfterShortCircuit 是短路优化后的预期生产路径成本。
func BenchmarkAutomationLiteralPathAfterShortCircuit(b *testing.B) {
	frame := literalBenchFrame()
	selector, comparator, threshold := "rsoc", "gt", 30.0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, satisfied, _ := benchmarkLiteralFastPathShortCircuit(frame, selector, comparator, threshold)
		if !ok || !satisfied {
			b.Fatal("short-circuit path broken")
		}
	}
}

// ── 分解微基准：定位 evalSelector 生产路径相对基线的开销来源 ─────────────────
// （生产路径 = isSpecialSelector + provider 快照 + 编译缓存命中 + markRuleResolved
//   + legacy matchField/compare；下列微基准逐项量化。）

// BenchmarkIsSpecialSelectorOnly 测 selector 形态判定成本（每次调用都会执行）。
func BenchmarkIsSpecialSelectorOnly(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if isSpecialSelector("rsoc") {
			b.Fatal("rsoc must not be special")
		}
	}
}

// BenchmarkPlanProviderSnapshotOnly 测 planProvider 快照（RLock）成本。
func BenchmarkPlanProviderSnapshotOnly(b *testing.B) {
	prov := newFakeProvider(nil)
	ev := newLiteralBenchEvaluator(prov)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ev.planProviderSnapshot() == nil {
			b.Fatal("provider missing")
		}
	}
}

// BenchmarkCompileSelectorCacheHit 测编译缓存命中成本（FNV hash + 双检查 + PlanEpoch）。
func BenchmarkCompileSelectorCacheHit(b *testing.B) {
	prov := newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindLiteral}
	})
	ev := newLiteralBenchEvaluator(prov)
	if _, err := ev.compileSelector(1, 0, 1, "rsoc"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ev.compileSelector(1, 0, 1, "rsoc"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMarkRuleResolved 测 literal 快路径每次都会执行的统计置位（statsMu）。
func BenchmarkMarkRuleResolved(b *testing.B) {
	ev := newLiteralBenchEvaluator(newFakeProvider(nil))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.markRuleResolved(1)
	}
}

// panicProvider 是"调用即测试失败"的 PlanProvider：用于**证明** literal 快路径
// 完全不触碰 provider（编译/缓存/开关）。若 evalSelector 的 literal 分支状态泄漏到
// provider 分支，本基准会直接 panic。（单一职责：只做证据，不参与生产。）
type panicProvider struct{}

func (panicProvider) Compile(uint, string) (sensorfield.Plan, error) {
	panic("literal fast path must not call Compile")
}
func (panicProvider) Enabled() bool { return true }
func (panicProvider) Epoch() uint64 { return 1 }

// BenchmarkAutomationLiteralPathNoProviderTouch 证明 literal 路径不触碰 provider：
// 注入 panicProvider 仍必须正常返回（否则 panic）。
func BenchmarkAutomationLiteralPathNoProviderTouch(b *testing.B) {
	ev := newLiteralBenchEvaluator(panicProvider{})
	frame := literalBenchFrame()
	rule := literalBenchRule()
	at := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, satisfied, _ := ev.evalSelector(rule.ID, 0, 1, "rsoc", "gt", 30, frame, at)
		if !ok || !satisfied {
			b.Fatalf("literal broken: ok=%v satisfied=%v", ok, satisfied)
		}
	}
}

// BenchmarkAutomationLiteralPathNoProviderNoRuleStruct 是"参数最小化"对照：
// 与生产函数不同，只传 selector/comparator/threshold/frame，用于量化
// (ruleID, slot, deviceID, at) 与函数返回三元组的调用约定开销。
func BenchmarkAutomationLiteralPathNoProviderNoRuleStruct(b *testing.B) {
	frame := literalBenchFrame()
	selector, comparator, threshold := "rsoc", "gt", 30.0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, found := matchField(frame, selector)
		if !found || !compare(comparator, v, threshold) {
			b.Fatal("broken")
		}
	}
}

// BenchmarkEvaluateFrameBaseline 是 #10a-2 的对照基线：同一 Evaluate 入口、
// 同一帧/规则集合，但**不注入解析层**（planProvider=nil）——即 main 基线行为
// （纯字面量走历史 matchField/compare）。判据：当前/基线 ≤ 1.05。
//
// 为什么这是公平对照：短路后 literal 路径与注入与否无关，两者的差异仅来自
// "是否多一层 isSpecialSelector 形态判定"（每 selector 一次扫描），这正是
// 集成层对生产求值链路的**全部**新增成本。
func BenchmarkEvaluateFrameBaseline(b *testing.B) {
	ev := evaluateFrameBenchEvaluator()
	ev.SetPlanProvider(nil) // main 行为：无解析层
	frame := evaluateFrameBenchFrame()
	at := time.Now()
	ev.Evaluate(1, frame, at)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.Evaluate(1, frame, at)
	}
}
