package sensorfield

import (
	"testing"
)

// BenchmarkLiteralEval 是门禁 #10 的 literal 热路径基准（目标 ≤ 今天 matchField +5%）。
func BenchmarkLiteralEval(b *testing.B) {
	reg := New(enabledConfig())
	reg.RegisterDeclarations("dev", []Field{numField("rsoc")})
	reg.SetDeviceType(1, "dev")
	plan, err := reg.Compile(1, "rsoc")
	if err != nil {
		b.Fatalf("Compile failed: %v", err)
	}
	frame := make([]Reading, 0, 16)
	frame = append(frame, Reading{Name: "rsoc", Value: 42})
	for i := 0; i < 15; i++ {
		frame = append(frame, Reading{Name: "other_field", Value: float64(i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if r := plan.Eval(frame); !r.OK {
			b.Fatalf("unexpected unbound: %s", r.Reason)
		}
	}
}

// BenchmarkFamilyEval16 是 family 热路径基准（方案 §13.5 目标 < 2µs）。
func BenchmarkFamilyEval16(b *testing.B) {
	reg := New(enabledConfig())
	decls := make([]Field, 0, 16)
	frame := make([]Reading, 0, 16)
	for i := 1; i <= 16; i++ {
		name := "temperature_" + itoa(i)
		decls = append(decls, numField(name))
		frame = append(frame, Reading{Name: name, Value: float64(20 + i)})
	}
	reg.RegisterDeclarations("dev", decls)
	reg.SetDeviceType(1, "dev")
	plan, err := reg.Compile(1, "temperature_*:max")
	if err != nil {
		b.Fatalf("Compile failed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := EvalPredicate(plan, frame, CmpGT, 30); !v.OK {
			b.Fatalf("unexpected unbound: %s", v.Reason)
		}
	}
}

// BenchmarkResolve 是控制面解析基准（方案 §13.5 目标 < 50µs）。
func BenchmarkResolve(b *testing.B) {
	reg := New(enabledConfig())
	decls := make([]Field, 0, 16)
	for i := 1; i <= 16; i++ {
		decls = append(decls, numField("temperature_"+itoa(i)))
	}
	reg.RegisterDeclarations("dev", decls)
	reg.SetDeviceType(1, "dev")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Resolve(1, "temperature_*:max"); err != nil {
			b.Fatalf("Resolve failed: %v", err)
		}
	}
}

// itoa 是无分配的小整数转字符串（bench 设置阶段用）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
