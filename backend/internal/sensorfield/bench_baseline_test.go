package sensorfield

import "testing"

// 门禁 #10（literal 性能 ≤ 今天 +5%）的“今天”基线。
//
// 基线代码逐字节复制自 main 基线 d406dc2d：
//   - baselineMatchField ← backend/internal/alert/evaluator.go::matchField
//   - 帧类型 baselineParserField ← backend/pkg/parser.Field（四字段）
//
// 说明：本文件只用于对比基准，不参与生产路径；不 import 任何业务包（红线 2）。

// baselineParserField 是 main 基线 pkg/parser.Field 的逐字段拷贝。
type baselineParserField struct {
	Name        string
	Value       float64
	Unit        string
	StringValue string
}

// baselineMatchField 与 main 基线 d406dc2d internal/alert/evaluator.go::matchField 逐字节相同。
func baselineMatchField(fields []baselineParserField, name string) (float64, bool) {
	for i := range fields {
		if fields[i].Name == name {
			return fields[i].Value, true
		}
	}
	return 0, false
}

// benchmarkBaselineFrame 构造与 BenchmarkLiteralEval 同形的 16 字段帧（目标字段在首位）。
func benchmarkBaselineFrame() []baselineParserField {
	frame := make([]baselineParserField, 0, 16)
	frame = append(frame, baselineParserField{Name: "rsoc", Value: 42})
	for i := 0; i < 15; i++ {
		frame = append(frame, baselineParserField{Name: "other_field", Value: float64(i)})
	}
	return frame
}

// benchmarkReadingFrame 构造同形的 []Reading 帧。
func benchmarkReadingFrame() []Reading {
	frame := make([]Reading, 0, 16)
	frame = append(frame, Reading{Name: "rsoc", Value: 42})
	for i := 0; i < 15; i++ {
		frame = append(frame, Reading{Name: "other_field", Value: float64(i)})
	}
	return frame
}

// BenchmarkLiteralEvalBaselineMatchField 是“今天”的 literal 字段查找成本（基线）。
func BenchmarkLiteralEvalBaselineMatchField(b *testing.B) {
	frame := benchmarkBaselineFrame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := baselineMatchField(frame, "rsoc"); !ok {
			b.Fatal("baseline lookup missed")
		}
	}
}

// BenchmarkLiteralEvalPlanEval 是解析层的 literal 取值路径（Plan.Eval，接口分派）。
func BenchmarkLiteralEvalPlanEval(b *testing.B) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	plan, err := reg.Compile(1, "rsoc")
	if err != nil {
		b.Fatalf("Compile failed: %v", err)
	}
	frame := benchmarkReadingFrame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if r := plan.Eval(frame); !r.OK {
			b.Fatalf("unexpected unbound: %s", r.Reason)
		}
	}
}

// BenchmarkLiteralEvalPredicate 是 literal 的完整谓词路径（含比较符，接口分派）。
func BenchmarkLiteralEvalPredicate(b *testing.B) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	plan, err := reg.Compile(1, "rsoc")
	if err != nil {
		b.Fatalf("Compile failed: %v", err)
	}
	frame := benchmarkReadingFrame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := EvalPredicate(plan, frame, CmpGT, 30); !v.OK || !v.Satisfied {
			b.Fatalf("unexpected verdict: %+v", v)
		}
	}
}
