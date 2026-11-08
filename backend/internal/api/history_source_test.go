package api

// history_source_test.go — 数据源抽象的**纯函数**契约（不需要 DB）。
//
// 端到端逐值对拍在 history_source_pg_test.go（PG + 真实回填数据）。这里守的是
// 不依赖数据库就能钉住的部分：分桶数学、覆盖判据前提、变更点语义。

import (
	"testing"
	"time"

	"ehome/backend/internal/telemetry"
)

// TestFloorDiv 守向下取整除法 —— Go 的 / 向零截断，负数会错。
func TestFloorDiv(t *testing.T) {
	cases := []struct{ a, b, want int64 }{
		{10, 3, 3},
		{-10, 3, -4}, // 向零截断会得 -3，floor 是 -4
		{-9, 3, -3},
		{0, 3, 0},
		{5, 5, 1},
	}
	for _, c := range cases {
		if got := floorDiv(c.a, c.b); got != c.want {
			t.Errorf("floorDiv(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestAggregateFramePointsAvgMatchesSQLShape 守 avg 模式产出**桶起点**而非数据时间戳。
//
// 这是最容易写错的一处：SQL 路径的 SELECT 出 `to_timestamp(floor(epoch/W)*W)`，
// 即**桶起点**。若 Go 侧返回桶内最后一条数据的时间戳，两条路径的点集在时间轴上
// 就错开了最多 W 秒 —— 值一样、时间不一样，对拍必然失败但原因很难看出来。
func TestAggregateFramePointsAvgMatchesSQLShape(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	q := historyPointQuery{Start: start, End: end, MaxPoints: 500, Mode: AggModeAvg}
	// 1 小时 / 500 点 ⇒ W = ceil(3600/500) = 8 秒。
	width := bucketWidthSeconds(historyWindowSeconds(start, end), 500)
	if width != 8 {
		t.Fatalf("前置条件不符：W = %d, want 8", width)
	}

	base := start.Add(3 * time.Second) // 桶 [0,8) 内
	raw := []HistoryPoint{
		{base, 1},
		{base.Add(2 * time.Second), 2}, // 同桶的最后一条 ⇒ 值 2
		{start.Add(9 * time.Second), 3},
	}
	got := aggregateFramePoints(raw, q)
	if len(got) != 2 {
		t.Fatalf("点数 = %d, want 2（两个桶）", len(got))
	}
	if !got[0].Timestamp.Equal(start) {
		t.Errorf("首点时间 = %v, want 桶起点 %v（不是数据时间戳 %v）",
			got[0].Timestamp, start, base)
	}
	if got[0].Value != 2 {
		t.Errorf("首桶值 = %v, want 2（桶内最后值，不是平均 1.5）", got[0].Value)
	}
	if !got[1].Timestamp.Equal(start.Add(8 * time.Second)) {
		t.Errorf("次点时间 = %v, want %v", got[1].Timestamp, start.Add(8*time.Second))
	}
	if got[1].Value != 3 {
		t.Errorf("次桶值 = %v, want 3", got[1].Value)
	}
}

// TestAggregateFramePointsChangeMatchesSQL 守变更点语义（含 1→0 结束点）。
func TestAggregateFramePointsChangeMatchesSQL(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	q := historyPointQuery{Start: start, End: start.Add(time.Hour), MaxPoints: 500, Mode: AggModeChange}
	raw := []HistoryPoint{
		{start, 0},
		{start.Add(time.Second), 0},     // 重复值：不是变更点
		{start.Add(2 * time.Second), 1}, // 0->1 变更
		{start.Add(3 * time.Second), 1},
		{start.Add(4 * time.Second), 0}, // 1->0 变更（脉冲结束点，绝不能丢）
	}
	got := aggregateFramePoints(raw, q)
	if len(got) != 3 {
		t.Fatalf("变更点数 = %d, want 3（首点 + 0→1 + 1→0），得到 %+v", len(got), got)
	}
	if got[2].Value != 0 || !got[2].Timestamp.Equal(start.Add(4*time.Second)) {
		t.Errorf("结束点 = %v@%v, want 0@%v（1→0 的结束点必须保留，丢了脉冲宽度就错了）",
			got[2].Value, got[2].Timestamp, start.Add(4*time.Second))
	}
}

// TestAggregateFramePointsEmpty 守空输入返回**空切片而非 nil**（JSON 序列化成 [] 不是 null）。
func TestAggregateFramePointsEmpty(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	got := aggregateFramePoints(nil, historyPointQuery{Start: start, End: start.Add(time.Hour), MaxPoints: 500})
	if got == nil {
		t.Fatal("空输入必须返回空切片（nil 会序列化成 null，前端画图会报错）")
	}
	if len(got) != 0 {
		t.Errorf("点数 = %d, want 0", len(got))
	}
}

// TestFrameReadEligible 守前提判据：五条必须同时成立。
func TestFrameReadEligible(t *testing.T) {
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	base := historyPointQuery{
		EdgeDeviceID: 14, Category: "rsoc",
		Start: start, End: start.Add(time.Hour), MaxPoints: 500, Mode: AggModeAvg,
	}
	if !frameReadEligible(true, dict, base) {
		t.Fatal("1 小时 / avg / 有字典 ⇒ 应该合格")
	}
	if frameReadEligible(false, dict, base) {
		t.Error("开关关闭时必须不合格")
	}
	if frameReadEligible(true, nil, base) {
		t.Error("没有 slot 字典时必须不合格（解不了码）")
	}

	noDevice := base
	noDevice.EdgeDeviceID = 0
	if frameReadEligible(true, dict, noDevice) {
		t.Error("device=0 时必须不合格")
	}

	rawMode := base
	rawMode.Mode = AggModeRaw
	if frameReadEligible(true, dict, rawMode) {
		t.Error("raw 模式必须不合格（帧表给不出原始行形状）")
	}

	longWindow := base
	longWindow.End = start.Add(25 * time.Hour)
	if frameReadEligible(true, dict, longWindow) {
		t.Error("窗口 > MaxDecodeWindow(24h) 必须不合格 —— 不能静默扫几百万帧")
	}

	badWindow := base
	badWindow.End = start
	if frameReadEligible(true, dict, badWindow) {
		t.Error("end<=start 必须不合格")
	}
}

// TestFrameReadEligibleRespectsDecodeMaxWindow 守短窗分界可被环境/Setter 影响。
func TestFrameReadEligibleRespectsDecodeMaxWindow(t *testing.T) {
	old := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(old)
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	q := historyPointQuery{
		EdgeDeviceID: 14, Category: "rsoc",
		Start: start, End: start.Add(2 * time.Hour), MaxPoints: 500, Mode: AggModeAvg,
	}
	SetFrameDecodeMaxWindow(time.Hour)
	if frameReadEligible(true, dict, q) {
		t.Error("2 小时窗口在 1 小时分界下必须不合格")
	}
	SetFrameDecodeMaxWindow(3 * time.Hour)
	if !frameReadEligible(true, dict, q) {
		t.Error("2 小时窗口在 3 小时分界下应该合格")
	}
}
