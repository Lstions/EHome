package automation

// 门禁 #10a-②：端到端求值基准（Lead 2026-10-21 二次裁决）。
//
// 为什么需要它（而不是只看单条 literal 路径）：用户可感知的是「一帧数据到达后，
// 所有规则被求值一遍」的总成本；单条路径 ≤10ns 的绝对预算抓不住"每规则多一层
// 包装"的累积效应。本基准构造**一帧 8 字段 + 3 条 literal 规则**的真实 Evaluate 入口，
// 用相对参考基线 ≤+5% 作为硬判据。
//
// 口径与噪声：
//   - 只测稳态（先 warmup 一轮，让 selector 计划缓存命中、窗口状态稳定）；
//   - 三条规则的阈值都设为**不触发**（rsoc=42，阈值 100），因此本基准不含
//     handler/DB/审计写入 —— 它测的是"求值"而不是"触发后的动作"；
//   - Evaluate 内部会 RLock 快照 rules 并做设备匹配（TriggerEdgeDeviceID=1 精确命中），
//     这两步也计入（它们是生产路径的真实组成）。
//
// 基线：testdata/bench_baseline_e2e.txt（参考基线，非 main 基线；main d406dc2d
// 无本集成层）。判据：当前/参考 ≤ 1.05。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"
)

// evaluateFrameBenchEvaluator 构造一个只含求值状态的最小 Evaluator（不触 DB）。
func evaluateFrameBenchEvaluator() *Evaluator {
	e := &Evaluator{
		rules:     map[uint]models.AutomationRule{},
		windows:   map[uint][]sample{},
		triggered: map[uint]time.Time{},
		plans:     newPlanCache(),
		ruleStats: map[uint]*ruleStat{},
	}
	e.planProvider = newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindLiteral}
	})
	// 3 条 literal 规则，阈值都高于实测值 ⇒ 不触发（纯求值路径）。
	rules := []models.AutomationRule{
		{ID: 1, TriggerSensorName: "rsoc", TriggerComparator: "gt", TriggerThreshold: 100, TriggerEdgeDeviceID: 1},
		{ID: 2, TriggerSensorName: "temperature_1", TriggerComparator: "gt", TriggerThreshold: 100, TriggerEdgeDeviceID: 1},
		{ID: 3, TriggerSensorName: "cell_voltage_1", TriggerComparator: "gt", TriggerThreshold: 100, TriggerEdgeDeviceID: 1},
	}
	for _, r := range rules {
		e.rules[r.ID] = r
	}
	// 规则快照（#10a-2 性能修复后 Evaluate 读 atomic.Pointer；测试构造不经 LoadRules，
	// 这里显式发布一次，保证基准测的是真实 Evaluate 热路径而不是空快照早退）。
	e.rulesSnap.Store(makeRulesSnapshot(e.rules))
	return e
}

// evaluateFrameBenchFrame 返回一帧 8 字段（3 个被规则引用 + 5 个干扰字段）。
func evaluateFrameBenchFrame() []parser.Field {
	return []parser.Field{
		{Name: "rsoc", Value: 42, Unit: "%"},
		{Name: "temperature_1", Value: 25, Unit: "C"},
		{Name: "cell_voltage_1", Value: 3.2, Unit: "V"},
		{Name: "current", Value: 10, Unit: "A"},
		{Name: "total_voltage", Value: 51.2, Unit: "V"},
		{Name: "cell_voltage_2", Value: 3.19, Unit: "V"},
		{Name: "temperature_2", Value: 26, Unit: "C"},
		{Name: "cycle_count", Value: 120, Unit: ""},
	}
}

// BenchmarkEvaluateFrame 是端到端求值基准：一帧 8 字段 × 3 条 literal 规则。
func BenchmarkEvaluateFrame(b *testing.B) {
	ev := evaluateFrameBenchEvaluator()
	frame := evaluateFrameBenchFrame()
	at := time.Now()
	// warmup：让计划缓存命中、窗口状态稳定（首帧与稳态的差异不计入判据）。
	ev.Evaluate(1, frame, at)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.Evaluate(1, frame, at)
	}
}
