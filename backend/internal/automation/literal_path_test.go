package automation

// #10a-1a 判决性单测：deterministic proof that the literal fast path does not touch
// the field resolver (provider snapshot / Compile / Enabled / Epoch / stats / plan cache).
//
// 为什么不用绝对 ns 判据：并行 teammate 负载下 baseline 在 1.19~6.4ns 间跳动（4–5×方差），
// 绝对阈值不可复现。本测试用**计数型 provider** 直接证明红线 9 的实质（不为统一入口付税），
// 机器负载无关。

import (
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"
)

// countingProvider 统计解析层被触碰的次数（每个方法各自计数）。
type countingProvider struct {
	snapshotForbidden bool // 冗余保险：literal 期间若被读取，记录后续断言

	snapshots atomic.Int64
	compiles  atomic.Int64
	enableds  atomic.Int64
	epochs    atomic.Int64
}

func (p *countingProvider) Compile(_ uint, selector string) (sensorfield.Plan, error) {
	p.compiles.Add(1)
	return &fakePlan{
		selector: selector,
		kind:     sensorfield.KindFamily,
		epoch:    1,
		verdict: func([]sensorfield.Reading, sensorfield.CmpOp, float64) sensorfield.Verdict {
			return sensorfield.Verdict{OK: true, Satisfied: true, Value: 55}
		},
	}, nil
}
func (p *countingProvider) Enabled() bool { p.enableds.Add(1); return true }
func (p *countingProvider) Epoch() uint64 { p.epochs.Add(1); return 1 }

// countingSnapshotProvider 是 countingProvider + evaluator（通过 hook 计 snapshot 次数）。
type countingSnapshotProvider struct{ countingProvider }

// TestLiteralPathSkipsProviderAndCompile 是 #10a-1a 硬门禁（确定性）。
//
// 断言：
//  1. 纯字面量 selector 调 evalSelector N 次 ⇒ snapshot/Compile/Enabled/Epoch 全为 0，
//     且 plan 缓存与 ruleStats 均为空（统计写入 0）；
//  2. 再调一次特殊 selector ⇒ 上述计数 >0（证明计数器真的在工作，不是恒 0 的假绿）；
//  3. literal 语义不变：取第一个同名、返回原始上报值。
func TestLiteralPathSkipsProviderAndCompile(t *testing.T) {
	prov := &countingProvider{}
	ev := newLiteralBenchEvaluator(prov)

	// 通过 hook 统计 planProviderSnapshot 调用次数（生产路径恒 nil，测试注入后生效）。
	var snapshots atomic.Int64
	planProviderSnapshotHook.Store(&snapshots)
	t.Cleanup(func() { planProviderSnapshotHook.Store(nil) })

	rule := literalBenchRule() // TriggerSensorName="rsoc", gt 30
	// 帧里故意放两个同名 rsoc（第一个 42 满足 gt30，第二个 99）：验证"取第一个同名"。
	frame := append([]parser.Field{{Name: "rsoc", Value: 42, Unit: "%"}},
		literalBenchFrame()...)
	frame = append(frame, parser.Field{Name: "rsoc", Value: 99, Unit: "%"})
	at := time.Now()

	const n = 1000
	for i := 0; i < n; i++ {
		ok, satisfied, value := ev.evalSelector(rule.ID, 0, 1, "rsoc", "gt", 30, frame, at)
		if !ok || !satisfied {
			t.Fatalf("literal path broken: ok=%v satisfied=%v", ok, satisfied)
		}
		if value != 42 {
			t.Fatalf("literal must return the FIRST same-named value (42), got %v", value)
		}
	}

	// ① 解析层零触碰（确定性断言，与机器负载无关）。
	if got := snapshots.Load(); got != 0 {
		t.Fatalf("literal path called planProviderSnapshot %d times, want 0", got)
	}
	if got := prov.compiles.Load(); got != 0 {
		t.Fatalf("literal path called Compile %d times, want 0", got)
	}
	if got := prov.enableds.Load(); got != 0 {
		t.Fatalf("literal path called Enabled %d times, want 0", got)
	}
	if got := prov.epochs.Load(); got != 0 {
		t.Fatalf("literal path called Epoch %d times, want 0", got)
	}
	if got := len(ev.plans.plans); got != 0 {
		t.Fatalf("literal path wrote %d plan-cache entries, want 0", got)
	}
	if got := len(ev.ruleStats); got != 0 {
		t.Fatalf("literal path wrote %d ruleStats entries, want 0", got)
	}

	// ② 反证：特殊 selector 必须触发这些路径（防"计数器恒 0"的假绿）。
	ok, satisfied, _ := ev.evalSelector(rule.ID, 0, 1, "temperature_*:max", "gt", 50, frame, at)
	if !ok || !satisfied {
		t.Fatalf("special selector path broken: ok=%v satisfied=%v", ok, satisfied)
	}
	if snapshots.Load() == 0 {
		t.Fatal("counter is dead: planProviderSnapshot never counted even on special path")
	}
	if prov.compiles.Load() == 0 {
		t.Fatal("counter is dead: Compile never counted even on special path")
	}
	if prov.enableds.Load() == 0 {
		t.Fatal("counter is dead: Enabled never counted even on special path")
	}
	if got := len(ev.plans.plans); got == 0 {
		t.Fatal("special path must populate the plan cache")
	}
}

// TestLiteralPathMatchesLegacySemantics 是 #10a-1a 的语义附带断言：
// 短路后的 literal 行为与"历史 matchField+compare"逐字节一致（多算例矩阵）。
func TestLiteralPathMatchesLegacySemantics(t *testing.T) {
	prov := &countingProvider{}
	ev := newLiteralBenchEvaluator(prov)
	rule := literalBenchRule()
	at := time.Now()

	frames := [][]parser.Field{
		{{Name: "rsoc", Value: 42}},
		{{Name: "rsoc", Value: 42}, {Name: "rsoc", Value: 99}}, // 第一个同名
		{{Name: "other", Value: 1}, {Name: "rsoc", Value: 31}}, // 非首位
		{{Name: "other", Value: 1}},                            // 缺失
		{{Name: "rsoc", Value: 30}},                            // 等于阈值（gt 不满足）
	}
	comparators := []string{"gt", "gte", "lt", "lte", "eq", "neq"}
	for _, frame := range frames {
		for _, op := range comparators {
			legacyValue, legacyFound := matchField(frame, "rsoc")
			legacySatisfied := false
			if legacyFound {
				legacySatisfied = compare(op, legacyValue, 30)
			}
			ok, satisfied, value := ev.evalSelector(rule.ID, 0, 1, "rsoc", op, 30, frame, at)
			if ok != legacyFound {
				t.Fatalf("op=%s frame=%v: ok=%v want %v", op, frame, ok, legacyFound)
			}
			if legacyFound && (satisfied != legacySatisfied || value != legacyValue) {
				t.Fatalf("op=%s frame=%v: got (satisfied=%v,value=%v) want (%v,%v)",
					op, frame, satisfied, value, legacySatisfied, legacyValue)
			}
		}
	}
	if prov.compiles.Load() != 0 || prov.enableds.Load() != 0 {
		t.Fatalf("literal semantics matrix touched provider: compiles=%d enableds=%d",
			prov.compiles.Load(), prov.enableds.Load())
	}
}
