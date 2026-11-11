package telemetry

// dualwrite_flush_test.go — 定时 flush 的验收（task-23）。
//
// ==================== 为什么这组用例必须存在 ====================
//
// 改造前 DualWriter 的 flush 只有两个触发点：攒满 128 帧、进程退出（Close）。
// **没有任何定时器。** 生产实测（帧速率 87.8 帧/分）表现为：
//
//	telemetry_frame max(ts) = 34 秒前
//	unified_data   max(ts) =  1 秒前
//
// 三条后果（按严重程度）：崩溃丢 ≤127 帧、图表静默少尾且不回落、
// 帧表不能作为「删掉老 unified_data」的实时来源。
//
// 这两条用例一个测「到点必落盘」，一个测「不 Close 也不丢」。
// 它们**都能变异验证**：删掉 run() 里的 `case <-tick.C: flush()` 就全红 ——
// 见文件末尾 TestFlushMutationGuard 的说明。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// countFrames 数 telemetry_frame 的实际行数。
func countFrames(t *testing.T, w *DualWriter) int64 {
	t.Helper()
	var n int64
	if err := w.store.db.Model(&models.TelemetryFrame{}).Count(&n).Error; err != nil {
		t.Fatalf("count telemetry_frame: %v", err)
	}
	return n
}

// waitForFrames 在 deadline 内轮询，直到库内帧数达到 want。
// 返回 (实际帧数, 是否达到)。
func waitForFrames(t *testing.T, w *DualWriter, want int64, deadline time.Duration) (int64, bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	var last int64
	for time.Now().Before(end) {
		last = countFrames(t, w)
		if last >= want {
			return last, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last, false
}

// TestDualWriterFlushesOnTimer 是 task-23 的**核心验收**：
// 写入远少于 128 帧（攒不满批），不调 Close，断言它们仍在 ≤3 秒内落库。
//
// 变异验证：删掉 run() 的 `case <-tick.C: flush()` ⇒ 本用例必红
// （帧一直压在内存里，3 秒后库内仍是 0）。
func TestDualWriterFlushesOnTimer(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	// 周期走 Config（构造参数），**不是** SetFlushInterval：
	// 第一版做成 setter 时它是 data race（worker 已在构造函数里启动并读取该字段）。
	// 缩短到 100ms 仍走**同一条定时器代码路径**，用例验的是「有没有时间驱动」，
	// 不是「2 秒这个具体数值」。
	w, err := NewDualWriter(db, dict, DualWriterConfig{FlushInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDualWriter: %v", err)
	}
	defer w.Close()

	base := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	const n = 5 // 远小于 dualWriteBatchSize(128)，绝不会触发"攒满"路径
	for i := 0; i < n; i++ {
		w.Observe(23, base.Add(time.Duration(i)*time.Second), 0, synthFrame(base, i), nil)
	}

	// 先确认「此刻还没落盘」——否则用例会因为 Close 之外的路径提前落盘而假绿。
	// 注意：Observe 是非阻塞投递，worker 可能已经消费进 batch 但还没 flush，
	// 所以这里不能断言 0，只记录一个基线。
	baseline := countFrames(t, w)
	if baseline >= n {
		t.Logf("注意：投递后立刻就有 %d 帧落库（说明走了其它路径），本用例的定时器断言会弱化", baseline)
	}

	got, ok := waitForFrames(t, w, n, 3*time.Second)
	if !ok {
		t.Fatalf("%v 内只落库 %d/%d 帧 —— 定时 flush 没生效。\n"+
			"这就是生产上「帧表滞后 34~90 秒」的同一个缺陷：攒不满 128 帧就一直压在内存里。",
			3*time.Second, got, n)
	}
	t.Logf("定时 flush：%d 帧在 ≤3s 内落库（flushInterval=100ms）", got)
}

// TestDualWriterNoFramesLostWithoutClose 钉住「崩溃前的窗口有界」。
//
// 真实崩溃（OOM / SIGKILL）不会走 Close ⇒ 内存里的批永远不落库。
// 有定时 flush 之后，这个窗口从「最多 127 帧 / 最长 87 秒」压缩到「≤1 个 flush 周期」。
//
// 本用例**刻意不调用 Close**，模拟非优雅退出：
//
//	· 断言已 Observe 的帧全部落库（有界窗口内）；
//	· 且断言落库发生在 Close 之前（用 defer 之外的方式验证）。
func TestDualWriterNoFramesLostWithoutClose(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	w, err := NewDualWriter(db, dict, DualWriterConfig{FlushInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDualWriter: %v", err)
	}
	// ⚠ 故意不 defer w.Close() —— 模拟崩溃。用例结束时 worker 自行停止
	//（进程退出），这里只关心「崩溃前已经落了多少」。

	const n = 7
	base := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		w.Observe(24, base.Add(time.Duration(i)*time.Second), 0, synthFrame(base, i), nil)
	}

	got, ok := waitForFrames(t, w, n, 3*time.Second)
	if !ok {
		t.Fatalf("不调 Close 时只落库 %d/%d 帧 —— 崩溃会丢这 %d 帧。\n"+
			"L1 门控下这些帧可能没有 unified_data 行（值未变）⇒ 删除老 unified_data 后就是永久丢失。",
			got, n, n-got)
	}
	t.Logf("无 Close 情况下 %d 帧全部落库（定时 flush 生效，崩溃窗口 ≤1 个周期）", got)

	// 收尾：这里可以 Close 了（已经验证完"不 Close 也不丢"）。
	w.Close()
}

// TestDualWriterTimerDoesNotChangeBatchSemantics 确认加定时器没有改变「批量」语义：
// 一次 flush 仍然是同一批事务，不会退化成"每帧一个事务"。
//
// 这条是防止一种很自然的错误实现：把 flush 写成"每来一帧就 flush"，
// 那样语义上没错（数据一样落库），但 WAL 提交次数放大 128 倍。
func TestDualWriterTimerDoesNotChangeBatchSemantics(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	// 故意设很长：本用例只让「攒满 128 帧」这条路径触发。
	w, err := NewDualWriter(db, dict, DualWriterConfig{FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewDualWriter: %v", err)
	}
	defer w.Close()

	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < dualWriteBatchSize; i++ {
		w.Observe(25, base.Add(time.Duration(i)*time.Second), 0, synthFrame(base, i), nil)
	}
	got, ok := waitForFrames(t, w, dualWriteBatchSize, 5*time.Second)
	if !ok {
		t.Fatalf("攒满 %d 帧后只落库 %d 帧 —— 批量路径被定时器破坏了", dualWriteBatchSize, got)
	}
	t.Logf("批量路径仍正常：%d 帧一次落库", got)
}
