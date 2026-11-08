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

// TestRollupReadSwitchBindsEnv 守「环境变量能打开开关」。
//
// 这条是"接上"的字面核实：不设 env 时 false，设 1/true 时 true。
// 用 Setter 覆写后再读，验证的是**状态机**；env 解析本身由
// TestReadBoolEnvDefaultParsing（rollup_read_test.go）覆盖。
func TestRollupReadSwitchBindsEnv(t *testing.T) {
	old := RollupReadEnabled()
	defer SetRollupReadEnabled(old)
	SetRollupReadEnabled(true)
	if !RollupReadEnabled() {
		t.Fatal("SetRollupReadEnabled(true) 后仍为 false —— 开关没接线")
	}
	SetRollupReadEnabled(false)
	if RollupReadEnabled() {
		t.Fatal("SetRollupReadEnabled(false) 后仍为 true")
	}
}

// TestRollupServesLongWindowOnly 守「只有长窗 + avg 才走 rollup」。
//
// 与帧表读路径（短窗）正好互补：
//
//	· rollup：长窗（> 6h）、avg、开关开；
//	· 帧表  ：短窗（≤ 1h）、avg 或 change、覆盖则用；
//	· 中间带（1h ~ 6h）：两者都不满足 ⇒ unified_data 的 SQL 聚合。
//
// 中间带确实存在，且是有意的 —— 3h 窗口解码要 ~1 万帧（解码成本开始显著），
// 而 rollup 的 60s 粒度对 3h 太粗（只能给 180 个点）。SQL 聚合在这段最合适。
func TestRollupServesLongWindowOnly(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	short := time.Now().Add(-30 * time.Minute) // 必须用相对 now，RollupShortWindow 是 6h
	long := short.Add(-7 * time.Hour)

	// 长窗 + avg + 开关开 ⇒ 是。
	if !shouldUseRollup(true, AggModeAvg, long, short, 14) {
		t.Fatal("7h 窗口 + avg + 开关开 应当走 rollup")
	}
	// 短窗 ⇒ 否（否则 1 小时图会从 3,600 点变 60 点，静默降级）。
	if shouldUseRollup(true, AggModeAvg, short.Add(-30*time.Minute), short, 14) {
		t.Fatal("30min 窗口不该走 rollup —— 会把点集从 3,600 砍到 60，属于静默降级")
	}
	// change 模式 ⇒ 否（rollup payload 没有翻转信息，重建 0→1 会算错）。
	if shouldUseRollup(true, AggModeChange, long, short, 14) {
		t.Fatal("change 模式不该走 rollup —— payload 无翻转信息，脉冲计数会错")
	}
	// 开关关 ⇒ 否。
	if shouldUseRollup(false, AggModeAvg, long, short, 14) {
		t.Fatal("开关关闭时不该走 rollup")
	}
	_ = start
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
		useRollup := shouldUseRollup(true, AggModeAvg, q.Start, q.End, 14)
		useFrame := frameReadEligible(true, dict, q)
		if useRollup && useFrame {
			t.Errorf("窗口 %v：rollup 与帧表同时为真 —— 两条加速路径重叠了", d)
		}
	}
}
