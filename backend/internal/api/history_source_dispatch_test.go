package api

// history_source_dispatch_test.go — 数据源分派的**决策链**（task-18 要点 4/5）。
//
// ============ 为什么单独测「分派」而不是只测各实现 ============
//
// 三个数据源各自正确，不代表**选择逻辑**正确。可能出现的真实故障：
//
//	· 长窗仍走解码 ⇒ 一次请求解几十万帧（要点 5 明令禁止）；
//	· 超 MaxDecodeWindow 仍走解码 ⇒ DecodeRange 报错，用户看到 500；
//	· 短窗却走 unified_data ⇒ 删了老分区就读到空（长期保留失效）。
//
// 这些都不是"某个实现算错了"，而是"选错了源"。用 checkDispatch 把整条决策链
// 钉住，比在每个实现里各测一遍更直接。

import (
	"testing"
	"time"

	"ehome/backend/internal/telemetry"
)

// dispatchCase 是一条分派决策的期望。
type dispatchCase struct {
	Name       string
	Query      historyPointQuery
	WantSource string // "telemetry_frame" / "unified_data"
	Why        string
}

// baseDispatchQuery 返回一个「短窗 + 有效设备 + avg」的基准查询。
func baseDispatchQuery(start time.Time) historyPointQuery {
	return historyPointQuery{
		EdgeDeviceID: 14, Category: "current",
		Start: start, End: start.Add(time.Hour),
		MaxPoints: 500, Mode: AggModeAvg,
	}
}

// TestDispatchDecisionChain 覆盖全部五个分支（纯判据，不需要 DB）。
//
// 帧表覆盖与否由 frameReadEligible 的**前置条件**决定；真正的覆盖探测要 DB，
// 在 TestFrameCoverageNotCoveredFallsBack 等用例里（PG）。这里只钉「由于参数
// 就不该解码」的那几条 —— 它们是要点 4/5 的硬约束。
func TestDispatchDecisionChain(t *testing.T) {
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	// 1) 短窗（1h）⇒ 允许解码。
	if !frameReadEligible(true, dict, baseDispatchQuery(start)) {
		t.Fatal("1h 窗口应当允许解码 —— 这是帧表读路径的主场景")
	}

	// 2) 中长窗（3h）⇒ **不解码**，回落 unified_data 的 SQL 聚合。
	//    ＜要点 4＞「短窗才解码」。
	mid := baseDispatchQuery(start)
	mid.End = start.Add(3 * time.Hour)
	if frameReadEligible(true, dict, mid) {
		t.Fatal("3h 窗口不该解码（默认分界 1h）—— 否则一次请求要解上万帧")
	}

	// 3) 超 MaxDecodeWindow（25h）⇒ **必须拒绝**，不能静默扫几百万帧。
	//    ＜要点 5＞ 这是硬约束：先判掉，不让它走到 DecodeRange 的报错路径。
	long := baseDispatchQuery(start)
	long.End = start.Add(25 * time.Hour)
	if frameReadEligible(true, dict, long) {
		t.Fatal("25h 窗口超过 MaxDecodeWindow 却仍允许解码 —— 要点 5 明令禁止")
	}
	// 且必须**早于** DecodeRange 被拒：DecodeRange 对超窗返回 RangeTooLargeError，
	// 那条路径会让 handler 返回 500（用户看到加载失败），而不是静默回落。
	if long.End.Sub(long.Start) <= telemetry.MaxDecodeWindow {
		t.Fatalf("用例前提不成立：25h 应大于 MaxDecodeWindow(%v)", telemetry.MaxDecodeWindow)
	}

	// 4) change 模式短窗允许（它要走翻转序列，帧表能表达）。
	change := baseDispatchQuery(start)
	change.Mode = AggModeChange
	if !frameReadEligible(true, dict, change) {
		t.Fatal("短窗 change 模式应当允许解码 —— 帧表解出的原始点能还原翻转序列")
	}

	// 5) raw 模式**永不解码**：它要返回 UnifiedData 行形状（含 id/sensor_name/unit），
	//    帧表给不出。硬走会静默改变响应形状。
	raw := baseDispatchQuery(start)
	raw.Mode = AggModeRaw
	if frameReadEligible(true, dict, raw) {
		t.Fatal("raw 模式不该解码 —— 它必须保持 UnifiedData 行形状（前端契约）")
	}
}

// TestDispatchLongWindowNeverDecodes 守「长窗走 rollup / 回落，不走解码」。
//
// 用 1h 分界与 24h 硬上限之间的三个尺度的窗口各测一次，确认全部拒绝解码。
func TestDispatchLongWindowNeverDecodes(t *testing.T) {
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{
		2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour,
	} {
		q := baseDispatchQuery(start)
		q.End = start.Add(d)
		if frameReadEligible(true, dict, q) {
			t.Errorf("窗口 %v 不该解码（分界 %v）—— 长窗应走 rollup 或 unified_data SQL 聚合",
				d, FrameDecodeMaxWindow())
		}
	}
}

// TestDispatchShortWindowDecodes 是上面的**反向**守卫：
// 「永远不解码」的假实现也能让上面全绿，这条挡住它。
func TestDispatchShortWindowDecodes(t *testing.T) {
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{
		time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour,
	} {
		q := baseDispatchQuery(start)
		q.End = start.Add(d)
		if !frameReadEligible(true, dict, q) {
			t.Errorf("窗口 %v 应当允许解码（分界 %v）—— 否则帧表读路径形同未接线",
				d, FrameDecodeMaxWindow())
		}
	}
}

// TestFrameReadDefaultIsOn 守默认值。
//
// 为什么帧表读默认**开**而 rollup 读默认**关**：
//
//	· 帧表是**主路径**（回填完成后它就是唯一数据源），且回退判据保守
//	  （覆盖不全就回落），开着不会让用户看到空图；
//	· rollup 是**派生加速路径**，生产上 telemetry_rollup_1m 目前是空表
//	  （写入侧未接线），开了只会多一次空查询。
func TestFrameReadDefaultIsOn(t *testing.T) {
	if !FrameReadEnabled() {
		t.Fatal("帧表读路径默认应为 true —— 它是「删老 unified_data 分区」的前提，默认关闭等于没接线")
	}
	if RollupReadEnabled() {
		t.Fatal("rollup 读路径默认应为 false —— telemetry_rollup_1m 在生产上是空表")
	}
}

// TestFrameDecodeMaxWindowDefault 守短窗分界默认 1h 与其可覆盖性。
func TestFrameDecodeMaxWindowDefault(t *testing.T) {
	old := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(old)
	SetFrameDecodeMaxWindow(defaultFrameDecodeMaxWindow)
	if got := FrameDecodeMaxWindow(); got != time.Hour {
		t.Fatalf("默认短窗分界 = %v, want 1h", got)
	}
	// 必须小于 DecodeRange 的硬上限，否则内层会先报错（用户看到 500 而不是回落）。
	if defaultFrameDecodeMaxWindow > telemetry.MaxDecodeWindow {
		t.Fatalf("短窗分界 %v 大于 MaxDecodeWindow %v —— 超窗请求会走到 DecodeRange 报错路径",
			defaultFrameDecodeMaxWindow, telemetry.MaxDecodeWindow)
	}
	// 1h 分界下，一帧 ≤ 1 秒的密度最多 3,600 帧，远小于 MaxDecodeFrames。
	if maxFrames := int64(defaultFrameDecodeMaxWindow / time.Second); maxFrames > telemetry.MaxDecodeFrames {
		t.Fatalf("1h @ 1Hz = %d 帧 > MaxDecodeFrames %d", maxFrames, telemetry.MaxDecodeFrames)
	}
}

// newLazyDictStub 返回一个非 nil 的最小 SlotDictionary（只为判据测试）。
func newLazyDictStub() telemetry.SlotDictionary {
	return telemetry.NewMemorySlots()
}
