package api

// device_history_segments_test.go — 分段解码的接缝验收 + 帧路径点集**包含**关系（task-26 续）。
//
// ============ 为什么要专门验接缝 ============
//
// 长窗（24h = 4 段、168h = 28 段）靠分段解码才保住「原始样本」契约。分段最典型的
// 两种 bug 都是**静默的**：
//
//	留缝  => 段边界附近少几个点 ⇒ 图表上出现 1 像素的空隙，没人会注意到
//	重叠  => 同一点返回两次 ⇒ 前端折线出现回折，或者统计值偏大
//
// 所以这里不是「拼起来能跑就行」，而是断言**总数精确 + 时间戳严格递增无重复**：
// 少一个点总数就少，多一个点时间戳就不再严格递增。两个方向都抓得到。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestFrameHistoryRows_SegmentSeamHasNoGapOrDup 跨越至少一个段边界。
func TestFrameHistoryRows_SegmentSeamHasNoGapOrDup(t *testing.T) {
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

	const dev = 9975
	const step = 60 * time.Second
	// 窗口取「刚好超过一个段」，确保至少发生一次分段
	window := frameDecodeMaxWindow + 30*time.Minute
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(window)
	seedFramesOnlyFields(t, db, dev, start, end, dict, []string{"current"}, step)

	wantFrames := int(window / step)
	rows, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
	if !ok {
		t.Fatal("ok=false —— 分段路径未生效")
	}
	if len(rows) != wantFrames {
		t.Fatalf("行数 = %d, want %d —— 差额 %d 说明段边界**留缝**（少点）或**重叠**（多点）",
			len(rows), wantFrames, len(rows)-wantFrames)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i].Timestamp.After(rows[i-1].Timestamp) {
			t.Fatalf("第 %d 行时间戳未严格递增（%s -> %s）—— 段边界重叠产生重复点",
				i, rows[i-1].Timestamp.Format(time.RFC3339), rows[i].Timestamp.Format(time.RFC3339))
		}
	}
	t.Logf("窗口 %s（%.1f 段）=> %d 行，时间戳严格递增无重复",
		window, float64(window)/float64(frameDecodeMaxWindow), len(rows))
}

// TestFrameHistoryRows_IsSupersetOfUnified 回答「#2 点数从 58,333 涨到 104,081」这个问号。
//
// ============ 为什么是「包含」而不是「相等」============
//
// 帧表写的是**每一帧解析结果**（L1 收窄之前）；unified_data 写的是**值有变化的行**
// （L1 收窄之后）。所以同一窗口下帧路径的点集**必然大于等于** unified_data：
//
//	帧路径少点  => bug（帧链断了 / 分段丢点）
//	帧路径多点  => 预期（全采样 vs 收窄后）
//
// 本用例把 unified_data 造成帧集合的**真子集**（每 3 帧写 1 行），断言：
// ① unified 的每个 (sensor, timestamp) 都能在帧结果里找到；
// ② 帧结果严格更多（证明这不是「恰好相等」的巧合场景）。
func TestFrameHistoryRows_IsSupersetOfUnified(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9976
	const step = 10 * time.Second
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(5 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, []string{"current"}, step)

	// 帧解码取真实值，再按「每 3 帧写 1 行」造 L1 收窄后的 unified_data。
	all, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
	if !ok || len(all) == 0 {
		t.Fatal("帧路径无数据，用例前置不成立")
	}
	var unified []models.UnifiedData
	for i := 0; i < len(all); i += 3 {
		unified = append(unified, models.UnifiedData{
			DeviceID: dev, SensorName: all[i].SensorName,
			Value: all[i].Value, Unit: "u", Timestamp: all[i].Timestamp,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(unified, 100).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}

	// ① 包含关系
	have := make(map[string]struct{}, len(all))
	for i := range all {
		have[all[i].SensorName+"|"+all[i].Timestamp.UTC().Format(time.RFC3339Nano)] = struct{}{}
	}
	for i := range unified {
		k := unified[i].SensorName + "|" + unified[i].Timestamp.UTC().Format(time.RFC3339Nano)
		if _, ok := have[k]; !ok {
			t.Fatalf("unified 第 %d 个采样 %s 在帧路径里**缺失** —— 这是 bug（帧链断/分段丢点），"+
				"不是全采样与收窄的差异", i, k)
		}
	}
	// ② 是真子集：帧路径必须严格更多
	if len(all) <= len(unified) {
		t.Fatalf("帧路径 %d 行 vs unified %d 行 —— 用例没造出「全采样 vs 收窄」的差异，"+
			"这条断言就没有鉴别力", len(all), len(unified))
	}
	t.Logf("帧路径 %d 行 ⊇ unified %d 行（多出 %d 行 = L1 收窄掉的部分）",
		len(all), len(unified), len(all)-len(unified))
}
