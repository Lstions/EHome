package api

// device_history_longwindow_test.go — 长窗分段解码的段数上限 + 代价实测（task-26 续）。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestFrameHistoryRows_TooManySegmentsFallsBack 守段数上限。
//
// 8 天以上（> maxFrameHistoryWindow x 6h）必须交回裸 SQL —— 否则一个
// start=1970 的请求会让服务解几万段。这是防滥用，不是性能取舍。
func TestFrameHistoryRows_TooManySegmentsFallsBack(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	// 上限 32 段 x 6h = 8 天；取 15 天确保超限
	end := time.Now().UTC()
	start := end.Add(-15 * 24 * time.Hour)
	if _, ok := frameHistoryRows(context.Background(), db, 9977, "", start, end); ok {
		t.Fatalf("15 天窗口超过 %s 上限却仍走帧表", maxFrameHistoryWindow)
	}
}

// TestFrameHistoryRows_LongWindowCost 量 24h / 168h 分段解码的本地代价。
//
// ⚠ **这是本地合成数据，不是生产实测。** 生产量级由 Lead 决定要不要跑；
// 这里只回答"分段本身有没有不可接受的固定开销"（段数 x 每段固定成本）。
func TestFrameHistoryRows_LongWindowCost(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9978
	// 5 分钟 @10s = 30 帧，只铺在窗口末端：目的是量「28 次空段查询」的固定开销，
	// 而不是量「数据量 x 解码」—— 后者本地造不出来（要 60 万帧）。
	end := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	start := end.Add(-5 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, []string{"current"}, 10*time.Second)

	for _, hours := range []int{24, 168} {
		w := time.Duration(hours) * time.Hour
		ws, we := end.Add(-w), end
		began := time.Now()
		rows, ok := frameHistoryRows(context.Background(), db, dev, "", ws, we)
		elapsed := time.Since(began)
		segments := int((w + frameDecodeMaxWindow - 1) / frameDecodeMaxWindow)
		t.Logf("%3dh（%2d 段）：耗时 %v，返回 %d 行，ok=%v", hours, segments, elapsed, len(rows), ok)
		if !ok {
			t.Fatalf("%dh 分段解码失败", hours)
		}
	}
}
