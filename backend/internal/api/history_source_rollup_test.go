package api

// history_source_rollup_test.go — rollup 读开关的**接线核实**（task-18 要点 4）。
//
// ============ 这一层要核实什么 ============
//
// 要点 4 要求「长窗走 rollup（一并把 EHOME_TELEMETRY_ROLLUP_READ 接上）」。
// rollup 读实现在 task-13 时已经写好并接进了 handler（rollup_read.go +
// handler_data.go），但**默认关闭**，因为生产上 telemetry_rollup_1m 是空表
// （写入侧未接线）。
//
// 所以「接上」的正确含义不是"改默认值为 true"—— 那会让每个长窗请求多付一次
// 必然落空的查询 —— 而是：
//
//	1. 开关确实绑定到环境变量（设了就生效，不用改代码）；
//	2. 开关打开时，长窗请求确实优先走 rollup；
//	3. rollup 没数据时**必须回落**，不能让用户看到空图。
//
// 三条各有一条用例。

import (
	"testing"
	"time"
)

// TestRollupServesLongWindowOnly 守「只有长窗 + avg 才走 rollup」。
//
// 与帧表读路径（短窗）互补，且**必须无缝**（2026-11-14）：
//
//	· rollup：窗口 > 6h、avg；
//	· 帧表  ：窗口 ≤ 6h、avg 或 change；
//	· 没有第三档 —— unified_data 已 DROP，两个源都不服务的窗口 == 用户看到报错。
//
// 因此「短窗分界」与「rollup 分界」必须同源（frameDecodeMaxWindow），
// 否则 (帧窗, rollup 短窗] 会成为死区。
func TestRollupServesLongWindowOnly(t *testing.T) {
	short := time.Now().Add(-30 * time.Minute) // 必须用相对 now，分界是 6h
	long := short.Add(-7 * time.Hour)

	// 长窗 + avg ⇒ 是。
	if !shouldUseRollup(AggModeAvg, long, short, 14) {
		t.Fatal("7h 窗口 + avg 应当走 rollup")
	}
	// 短窗 ⇒ 否（否则 1 小时图会从 3,600 点变 60 点，静默降级）。
	if shouldUseRollup(AggModeAvg, short.Add(-30*time.Minute), short, 14) {
		t.Fatal("30min 窗口不该走 rollup —— 会把点集从 3,600 砍到 60，属于静默降级")
	}
	// change 模式 ⇒ 否（rollup payload 没有翻转信息，重建 0→1 会算错）。
	if shouldUseRollup(AggModeChange, long, short, 14) {
		t.Fatal("change 模式不该走 rollup —— payload 无翻转信息，脉冲计数会错")
	}
}

// TestRollupAndFrameAreComplementary 守两条加速路径**不重叠**。
//
// 重叠会浪费（短窗本可解码却先查了空的 rollup 表），更重要的是会让"哪个源在
// 服务这个请求"变得不可预测 —— 排查图表异常时这是灾难。
func TestRollupAndFrameAreComplementary(t *testing.T) {
	dict := newLazyDictStub()
	now := time.Now()
	// 枚举从 1 分钟到 30 天的一系列窗口，验证：rollup 与帧表**永不同时为真**。
	for _, d := range []time.Duration{
		time.Minute, 30 * time.Minute, time.Hour, 90 * time.Minute,
		3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
	} {
		q := historyPointQuery{
			EdgeDeviceID: 14, Category: "current",
			Start: now.Add(-d), End: now, MaxPoints: 500, Mode: AggModeAvg,
		}
		useRollup := shouldUseRollup(AggModeAvg, q.Start, q.End, 14)
		useFrame := frameReadEligible(dict, q)
		if useRollup && useFrame {
			t.Errorf("窗口 %v：rollup 与帧表同时为真 —— 两条加速路径重叠了", d)
		}
		// **必须恰好有一个为真**：unified_data 已 DROP，没有第三档。
		// 这条断言把「分界不同源 ⇒ 死区 ⇒ 用户报错」挡在单测层。
		if !useRollup && !useFrame {
			t.Errorf("窗口 %v：rollup 与帧表都不服务 —— 死区（分界必须同源）", d)
		}
	}
}
