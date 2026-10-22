package alert

// 门禁 #10a（告警侧）：生产求值链路 literal 快路径基准。
//
// 测真实生产调用：Evaluator.evalSelector 的 KindLiteral 分支（provider 快照 +
// 编译缓存命中 + markRuleResolved + legacy matchField+compare）。
// 基线 = 本包 matchField + compare（与 main 基线 d406dc2d 逐字节相同）。
// 判据见 Lead 裁决：相对 main 基线 ≤ +5% 判 PASS/FAIL；新层抽象成本另计（#10b）。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"
)

// newAlertLiteralBenchEvaluator 构造只含 literal 快路径所需状态的最小 Evaluator（不触 DB）。
func newAlertLiteralBenchEvaluator(prov PlanProvider) *Evaluator {
	e := &Evaluator{
		plans:     newPlanCache(),
		ruleStats: map[uint]*ruleStat{},
	}
	e.planProvider = prov
	return e
}

// alertLiteralBenchFrame 返回 16 字段帧、目标字段在首位。
func alertLiteralBenchFrame() []parser.Field {
	f := make([]parser.Field, 0, 16)
	f = append(f, parser.Field{Name: "rsoc", Value: 42, Unit: "%"})
	for i := 0; i < 15; i++ {
		f = append(f, parser.Field{Name: "other_field", Value: float64(i)})
	}
	return f
}

// BenchmarkAlertLiteralPath 是告警生产链路（evalSelector 的 KindLiteral 快路径）基准。
func BenchmarkAlertLiteralPath(b *testing.B) {
	prov := newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindLiteral}
	})
	ev := newAlertLiteralBenchEvaluator(prov)
	rule := models.AlertRule{ID: 1, SensorName: "rsoc", Comparator: "gt", Threshold: 30}
	frame := alertLiteralBenchFrame()
	at := time.Now()
	if ok, _, _ := ev.evalSelector(rule, 1, frame, at); !ok {
		b.Fatal("literal path warmup failed")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, satisfied, _ := ev.evalSelector(rule, 1, frame, at)
		if !ok || !satisfied {
			b.Fatalf("literal path broken: ok=%v satisfied=%v", ok, satisfied)
		}
	}
}

// BenchmarkAlertLiteralPathBaseline 是 main 基线（matchField + compare）。
func BenchmarkAlertLiteralPathBaseline(b *testing.B) {
	frame := alertLiteralBenchFrame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, found := matchField(frame, "rsoc")
		if !found || !compare("gt", v, 30) {
			b.Fatal("baseline broken")
		}
	}
}
