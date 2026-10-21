package drivers

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// ============================================================================
// 解析热路径基准 —— 证明重构既没有牺牲性能，也消除了每帧分配
// ============================================================================
//
// 重构前的性能问题（全是每帧一次的浪费）：
//   · VerifyControlAction 每次都构造一个 11 条目的 map[string]string 字面量；
//   · techfineParserFor 每次都 &TechfineInverterDriver{} 新建接收者 + 走 switch；
//   · parseHTEMPCompact 每次 make([]float64, 7) 分配一个小切片；
//   · ParseData 每帧重新 strings.TrimPrefix + 多次重复判定。
//
// 现在的做法：注册表在 init 期建好 map（按 ID / 按 wire hex），解析只做一次查表；
// HTEMP 的温度位用 [7]float64 栈数组。下面的 Benchmark 同时给出 ns/op 与 allocs/op。

var (
	benchHTEMPRaw  = []byte("(041 037 032 033 041 100 100 11000000000000000000\r")
	benchHTEMPWire = hex.EncodeToString([]byte("HTEMP\r"))
	benchHGRIDRaw  = []byte("(213.0 49.9 280 170 65 40 +00000 0000000000000000\r")
	benchHGRIDWire = hex.EncodeToString([]byte("HGRID\r"))
)

// BenchmarkTechfineHTEMPCommandAware 测生产主路径：命令上下文 + 8 字段温度帧。
func BenchmarkTechfineHTEMPCommandAware(b *testing.B) {
	d := &TechfineInverterDriver{}
	b.ReportAllocs()
	b.SetBytes(int64(len(benchHTEMPRaw)))
	for i := 0; i < b.N; i++ {
		if _, err := d.ParseDataWithCommand(benchHTEMPRaw, benchHTEMPWire); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTechfineHGRIDCommandAware 测另一条常见路径（市电帧）。
func BenchmarkTechfineHGRIDCommandAware(b *testing.B) {
	d := &TechfineInverterDriver{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := d.ParseDataWithCommand(benchHGRIDRaw, benchHGRIDWire); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTechfineParseDataShapeDispatch 测无上下文的形状分派路径。
func BenchmarkTechfineParseDataShapeDispatch(b *testing.B) {
	d := &TechfineInverterDriver{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := d.ParseData(benchHTEMPRaw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTechfineVerifyControlAction 测受控动作的完整校验路径
// （CR 结尾检查 + 解析 + 形状绑定 + 最小字段数）。
func BenchmarkTechfineVerifyControlAction(b *testing.B) {
	d := &TechfineInverterDriver{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := d.VerifyControlAction("read_temperature", json.RawMessage("{}"), benchHTEMPRaw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTechfineRegistryLookup 单测注册表查表本身的开销
// （重构前这里是 map 字面量构造 + switch）。
func BenchmarkTechfineRegistryLookup(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok := techfineCommandByWire(benchHTEMPWire); !ok {
			b.Fatal("lookup failed")
		}
	}
}
