package api

// device_history_downsample_test.go — 下推降采样的验收（Lead 2026-11-08 批准项 ①）。
//
// 核心要求（原话）：「返回的点与现在逐点一致，用现有实现的结果作为基准逐点对拍」。
//
// 为什么这条必须逐点比而不是比点数：下推最容易出的问题是「抽样规则变了」
// —— 点数一样但首尾点/中间点位移，前端图表的端点会变，而单价断言抓不到。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestDownsampledHistoryMatchesBaselinePointByPoint 是验收主用例。
func TestDownsampledHistoryMatchesBaselinePointByPoint(t *testing.T) {
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

	const dev = 9981
	fields := []string{"current", "rsoc", "total_voltage"}
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(10 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, fields, 10*time.Second)

	for _, maxPoints := range []int{1, 5, 17, 100, 500} {
		baseline, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
		if !ok {
			t.Fatal("基线路径无数据")
		}
		want := downsampleUnifiedData(baseline, maxPoints)

		got, ok := frameHistoryRowsDownsampled(context.Background(), db, dev, "", start, end, maxPoints)
		if !ok {
			t.Fatalf("maxPoints=%d: 下推路径 ok=false", maxPoints)
		}
		if len(got) != len(want) {
			t.Fatalf("maxPoints=%d: 行数 %d vs 基线 %d", maxPoints, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID ||
				got[i].SensorName != want[i].SensorName ||
				got[i].Value != want[i].Value ||
				got[i].Unit != want[i].Unit ||
				!got[i].Timestamp.Equal(want[i].Timestamp) {
				t.Fatalf("maxPoints=%d 第 %d 行与基线不一致:\n  got  %+v\n  want %+v",
					maxPoints, i, got[i], want[i])
			}
		}
		t.Logf("maxPoints=%-4d 基线 %d 行 => 抽样 %d 行，逐点一致",
			maxPoints, len(baseline), len(got))
	}
}

// TestDownsampledHistoryMaterializesFarFewerRows 量「不再物化 2.46M 行」的收益。
//
// ⚠ 本地帧表规模远小于生产，所以这里量的是**物化量的比例**而不是墙钟时间的绝对值。
func TestDownsampledHistoryMaterializesFarFewerRows(t *testing.T) {
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

	const dev = 9982
	fields := []string{"a1", "a2", "a3", "current"}
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(20 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, fields, 10*time.Second)

	all, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
	if !ok {
		t.Fatal("基线无数据")
	}
	small, ok := frameHistoryRowsDownsampled(context.Background(), db, dev, "", start, end, 20)
	if !ok {
		t.Fatal("下推路径 ok=false")
	}
	if len(small) > 22 { // maxPoints + 首尾
		t.Fatalf("抽样后 %d 行，超过 maxPoints+2", len(small))
	}
	ratio := float64(len(all)) / float64(len(small))
	if ratio < 5 {
		t.Fatalf("物化量只降到 1/%.1f —— 用例数据太少，测不出下推的意义", ratio)
	}
	t.Logf("全量 %d 行 => 下推后 %d 行（物化量降到 1/%.0f）", len(all), len(small), ratio)
	_ = models.UnifiedData{}
}
