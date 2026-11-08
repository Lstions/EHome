package api

// history_source_share_test.go — 解码共享（BatchHint / 单飞）的**机制**守卫。
//
// ============ 为什么需要这一层 ============
//
// 判定「批请求更快」的 perf 用例（history_source_perf_test.go）直接调
// `frameSource.Points` 并手动设了 `BatchHint`。但**生产走的是 handler**：
// 如果 handler 忘了设这个提示，perf 用例照样绿，生产却退化 4 倍。
//
// 本次开发就真的出过这个 bug：`BatchHint: true` 被写在了**上面的 rollup 调用**
// 里（rollupHistoryPoints 根本不读这个字段），而真正的帧表分派调用没设。
// 是 review `git diff handler_data.go` 时发现的 —— 用例当时全绿。
//
// 所以这里补两层：
//
//	1. **机制**：计数解码次数，验证 BatchHint 真的让 N 个并发 category 只解 1 次；
//	2. **接线**：源码级检查 handler_data.go 里 BatchHint 挂在**帧表分派**调用上，
//	   不在 rollup 调用上（那是 1 处，改错位置就红）。

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestBatchHintSharesOneDecode 守「批请求只解码一次」。
//
// 用 29 个并发 category（生产 BMS 页的真实形状）验证：
//
//	· BatchHint=true  ⇒ 解码 **1** 次；
//	· BatchHint=false ⇒ 解码 29 次（每字段独立 scope）。
//
// 这直接对应 0.61× vs 2.75× 的性能差。
func TestBatchHintSharesOneDecode(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up: %v", err)
	}

	// 装上解码次数计数器（生产恒 nil，这里必须显式赋值否则 StoreInt64 空指针）。
	decodeHookForTest = new(int64)
	defer func() { decodeHookForTest = nil }()

	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	const frames = 240
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frs []models.TelemetryFrame
	for i := 0; i < frames; i++ {
		ts := start.Add(time.Duration(i) * 15 * time.Second)
		var readings []telemetry.Reading
		for fi, f := range bms29 {
			readings = append(readings, telemetry.Reading{Name: f, Value: float64((i + fi) % 40)})
		}
		fr, err := enc.EncodeFrame(14, ts, 0, readings)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		frs = append(frs, models.TelemetryFrame{
			DeviceID: fr.DeviceID, TS: fr.TS, Seq: fr.Seq,
			FrameKind: fr.FrameKind, SlotID: fr.SlotID, Payload: fr.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frs, 200).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}

	q := historyPointQuery{
		EdgeDeviceID: 14, Category: "current",
		Start: start, End: start.Add(frames * 15 * time.Second),
		MaxPoints: 500, Mode: AggModeAvg,
	}

	runBatch := func(hint bool) int64 {
		ResetFrameDecodeCache()
		atomic.StoreInt64(decodeHookForTest, 0)
		var wg sync.WaitGroup
		for _, c := range bms29 {
			wg.Add(1)
			go func(cat string) {
				defer wg.Done()
				qq := q
				qq.Category = cat
				qq.BatchHint = hint
				frameSource{db: db.Session(&gorm.Session{}), dict: dict}.Points(context.Background(), qq)
			}(c)
		}
		wg.Wait()
		return atomic.LoadInt64(decodeHookForTest)
	}

	withHint := runBatch(true)
	if withHint != 1 {
		t.Errorf("BatchHint=true 时解码 %d 次，want 1 —— 批请求该共享一次解码（否则 29 倍浪费）", withHint)
	}
	withoutHint := runBatch(false)
	if withoutHint != int64(len(bms29)) {
		t.Errorf("BatchHint=false 时解码 %d 次，want %d（每个 category 独立单字段 scope）",
			withoutHint, len(bms29))
	}
	t.Logf("解码次数：BatchHint=true ⇒ %d；BatchHint=false ⇒ %d（%d 个 category）",
		withHint, withoutHint, len(bms29))
}

// TestHandlerBatchSetsBatchHintOnFrameDispatch 是**源码级**接线检查。
//
// 为什么要查到源码：handler 是 gin 闭包，起 HTTP 服务器断言「解码次数」代价太大；
// 而**挂错位置**恰恰是本次真实发生过的 bug（挂到 rollup 调用上了）。
// 用正则检查「BatchHint 出现在帧表分派调用的实参里，且不在 rollup 调用的实参里」。
func TestHandlerBatchSetsBatchHintOnFrameDispatch(t *testing.T) {
	raw, err := os.ReadFile("handler_data.go")
	if err != nil {
		t.Fatalf("读 handler_data.go: %v", err)
	}
	src := string(raw)

	// 帧表分派调用：queryHistoryPointsWithFallback(... historyPointQuery{ ... })
	frameCall := regexp.MustCompile(`(?s)queryHistoryPointsWithFallback\(.*?historyPointQuery\{.*?\}\)`)
	frameMatches := frameCall.FindAllString(src, -1)
	if len(frameMatches) == 0 {
		t.Fatal("没找到 queryHistoryPointsWithFallback 调用 —— 帧表分派没接线")
	}
	var batchCall string
	for _, m := range frameMatches {
		if strings.Contains(m, "BatchHint") {
			batchCall = m
		}
	}
	if batchCall == "" {
		t.Fatal("帧表分派调用里没有 BatchHint —— 批端点会退化成 29 次单字段解码（实测 2.75× vs 0.61×）")
	}

	// 反向：rollup 调用里**不该**有 BatchHint（rollupHistoryPoints 不读它，写了是误导）。
	rollupCall := regexp.MustCompile(`(?s)rollupHistoryPoints\(.*?historyPointQuery\{.*?\}\)`)
	for _, m := range rollupCall.FindAllString(src, -1) {
		if strings.Contains(m, "BatchHint") {
			t.Error("rollupHistoryPoints 调用里出现了 BatchHint —— rollup 不读这个字段，" +
				"写在这里恰恰会让真正的帧表分派漏掉它（本次开发真实踩过）")
		}
	}
}
