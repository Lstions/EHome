package api

// rollup_tail_empty_field_pg_test.go — 尾巴窗口里「目标字段没有采样」不许当成解码故障（P0）。
//
// ============ 这个 bug 是什么（Lead 2026-11-07 DETACH 10 月分区时实测）============
//
// `decodeFrameWindowNow` 里有一条判据：
//
//	if len(points) == 0 && stats.FramesSeen > 0 { return nil, error }
//
// 它在**整窗**帧表读路径上是必需的（「有帧但一个点都解不出」= 解码坏了，必须回落）。
// 但 `rollupLiveTailPoints` 复用了它，而尾巴窗口是 **[now-120s, now) 的 2 分钟**：
//
//	· 告警类字段（alarm_*）只在状态翻转时上报 ⇒ 这 2 分钟很可能一个采样都没有；
//	· 低频字段几小时才报一次；
//	· 值未变 + 增量帧不产点，也能让某字段在这段为空。
//
// 于是：len(points)==0（但别的字段有帧）⇒ error ⇒ ok=false ⇒
// 「尾巴取不到 ⇒ 整窗回落」⇒ 长窗整段回落 unified_data。
//
// 删掉 unified_data 之后的表现**不是空页，是静默截断**：
//
//	收藏夹里的 30 天图：grid_voltage 正常显示 30 天，
//	                    pv1_voltage 只剩最近 7 天，没有任何报错。
//
// 生产实测（dev15，10 月分区 DETACH）：
//	grid_voltage        7d=200  30d=133  ← rollup 生效
//	pv1_voltage         7d=186  30d= 44  ← 回落（只剩 11 月）
//	把 end 从 now 改成 now-2min ⇒ 全部 133 —— 证明只在触发末端截断时出现。

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

// seedFramesOnlyFields 只把指定字段编码成帧（用来构造「某字段在帧里不存在」）。
func seedFramesOnlyFields(t *testing.T, db *gorm.DB, deviceID uint, start, end time.Time, dict telemetry.SlotDictionary, names []string, step time.Duration) {
	t.Helper()
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frames []models.TelemetryFrame
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		readings := make([]telemetry.Reading, 0, len(names))
		for i, n := range names {
			readings = append(readings, telemetry.Reading{Name: n, Value: float64(10+i) + float64(ts.Unix()%7)})
		}
		f, err := enc.EncodeFrame(deviceID, ts.UTC(), 0, readings)
		if err != nil {
			t.Fatalf("encode %s: %v", ts.Format(time.RFC3339), err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 500).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}
}

// TestRollupLiveTail_FieldAbsentFromTailIsNotFailure 是本次 P0 的**核心守卫**。
//
// 场景：帧表在尾巴窗口里有帧，但**查询的那个字段**在这 2 分钟没有采样。
// 期望：`rollupLiveTailPoints` 返回 (空, true) —— 空尾巴是合法状态，不是故障。
//
// 变异验证：把 allowEmpty 改回 false，本用例必须红。
func TestRollupLiveTail_FieldAbsentFromTailIsNotFailure(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9931
	now := time.Now().UTC()
	cutoff := now.Add(-datalifecycle.RollupCoverageLiveLag)
	// 尾巴窗口里**只**编码 "current" 这一帧字段 —— 模拟"告警字段这 2 分钟没上报"。
	seedFramesOnlyFields(t, db, dev, cutoff, now, dict, []string{"current"}, 10*time.Second)

	q := historyPointQuery{
		EdgeDeviceID: dev,
		Category:     "alarm_fan_error", // 帧里根本没有这个字段
		Start:        cutoff.Add(-7 * 24 * time.Hour),
		End:          now,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	}
	tail, ok := rollupLiveTailPoints(context.Background(), db, q, cutoff)
	if !ok {
		t.Fatal("尾巴窗口里帧存在、但目标字段没有采样 —— 这是**正常状态**，不是解码故障。" +
			"返回 ok=false 会让整窗回落 ⇒ 删掉 unified_data 之后长窗静默截断（比空页更难发现）")
	}
	if len(tail) != 0 {
		t.Fatalf("该字段在尾巴里本就没有采样，点集应为空，got %d 点", len(tail))
	}
}

// TestRollupLiveTail_NoFramesButDeviceQuietIsStillServed —— 空尾巴**不是失败**。
//
// 我第一版把这条写成了「一帧都没有 ⇒ ok=false」，那是错的。想清楚之后的语义分界：
//
//	ok=false 只留给**硬失败**（字典未注入 / 解码报错 / 表不可用）；
//	空结果（设备这 2 分钟没上报）⇒ ok=true，照常服务主体。
//
// 为什么不能对空结果回落：**删掉 unified_data 之后，「回落」等于读一张不存在的表
// ⇒ 整窗空页**；而「少 2 分钟尾巴」至少把 30 天里的 29.998 天正确显示出来。
// 两者都错，但后者错得小得多，且不会让用户失去整段历史。
//
// 真缺口（设备刚报过、帧表却没帧）由 Warn 暴露（见 rollupLiveTailPoints 的注释），
// 不靠回落地雷式处理。
func TestRollupLiveTail_NoFramesButDeviceQuietIsStillServed(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	now := time.Now().UTC()
	cutoff := now.Add(-datalifecycle.RollupCoverageLiveLag)
	q := historyPointQuery{
		EdgeDeviceID: 9932,
		Category:     "current",
		Start:        cutoff.Add(-7 * 24 * time.Hour),
		End:          now,
		MaxPoints:    500,
		Mode:         AggModeAvg,
	}
	// 尾巴窗口没有帧、设备也没报过（没有 edge_devices 行）⇒ 空尾巴是可信的。
	tail, ok := rollupLiveTailPoints(context.Background(), db, q, cutoff)
	if !ok {
		t.Fatal("空尾巴被当成硬失败 —— 删掉 unified_data 之后这会变成整窗空页，" +
			"而它可能只是设备这 2 分钟没上报")
	}
	if len(tail) != 0 {
		t.Fatalf("尾巴点集应为空，got %d", len(tail))
	}
}

// ============ 一条实测发现（写在这里免得后人重踩）============
//
// 我原本想加一条「帧表不存在 ⇒ 尾巴必须 ok=false」的反向守卫。**实测发现做不到**：
//
//	DROP TABLE telemetry_frame CASCADE 之后（pg_class 确认为 0），
//	telemetry.DecodeRangeAnchored(...) 返回 **err=nil + 全零 stats**。
//
// 也就是说：**帧表缺失被解码器当成「没有帧」，而不是错误。**
//
// 后果分两处看：
//	· 整窗路径（frameSource）：返回「空点集 + 无错误」—— 与「设备真的没数据」不可区分。
//	  这是**硬切换后唯一可能的行为**（没有第二份数据源可以对照），
//	  也是把「表缺失」判定为部署事故而不是数据事实的理由：生产上有表存在性门禁。
//	· 尾巴路径：返回「空尾巴 + ok=true」，图表少最后 2 分钟。
//
// 这个降级我判定为**可接受**：删掉 unified_data 之后，「回落」等于整窗空页，
// 而「少 2 分钟」至少保住整段历史。真要收紧需要另一层探测，本次不做。
