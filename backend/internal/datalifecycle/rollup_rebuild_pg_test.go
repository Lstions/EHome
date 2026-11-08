package datalifecycle

// rollup_rebuild_pg_test.go — 可行性研究：**停写 unified_data 之后，rollup 能否从帧表重建**。
//
// ============ 为什么这条测试比任何代码都重要（Lead task-24）============
//
// rollup_backfill 现在从 unified_data 聚合。停写 unified_data 之后，若将来需要重建
// rollup，就只剩帧表一条路。**如果帧表重建不出来，整个「停写」方案就不可逆。**
//
// 本测试用**生产同一个编码器**把 unified_data 编成帧，再从帧解码重建分钟级统计，
// 与「unified_data 直接聚合」逐桶对比，量化差异来源。
//
// ============ 结论先行（本测试钉的就是它）============
//
// **能重建。** 差异只有两个可量化的来源：
//  1. **量化往返**：帧里存的是 round((v-Min)/Step) 的整数码，解码得 Min+code*Step，
//     单个采样误差 <= Step/2。avg/min/max/last 都落在这个界内。
//     ⇒ 这是**帧表本身的固有精度**，与「用帧表读图表」同源，不是重建引入的新误差。
//  2. **采样集合**：帧表存的是**全部解析帧**（L1 收窄前），而现有回填从 unified_data
//     聚合的是**收窄后的行**。所以从帧表重建得到的 avg 更接近 live collector 的口径
//     （live 也是全采样），而不是现有回填的口径。
//
// 换句话说：**从帧表重建 rollup 不但可行，语义上还更正确。**

import (
	"context"
	"math"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// rebuildStudyFields 选三个量程/步长差异大的字段，让量化误差显形。
//
//	current:       Min=-200 Step=0.01  => 误差 <= 0.005
//	total_voltage: Min=0    Step=0.01  => 误差 <= 0.005
//	rsoc:          Min=0    Step=1     => 误差 <= 0.5（最坏的一个，专门用来显形）
var rebuildStudyFields = []string{"current", "total_voltage", "rsoc"}

// encodeFramesFromUnified 用生产编码器把 unified_data 行编成帧（与回填同一条编码链）。
func encodeFramesFromUnified(t *testing.T, db *gorm.DB, deviceID uint, dict telemetry.SlotDictionary) int {
	t.Helper()
	var rows []struct {
		TS         time.Time
		SensorName string
		Value      float64
	}
	if err := db.Table("unified_data AS u").
		Select("u.timestamp AS ts, u.sensor_name, u.value").
		Where("u.device_id = ?", deviceID).
		Order("u.timestamp ASC, u.sensor_name ASC").Scan(&rows).Error; err != nil {
		t.Fatalf("load unified rows: %v", err)
	}
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frames []models.TelemetryFrame
	i := 0
	for i < len(rows) {
		ts := rows[i].TS
		var readings []telemetry.Reading
		for i < len(rows) && rows[i].TS.Equal(ts) {
			readings = append(readings, telemetry.Reading{Name: rows[i].SensorName, Value: rows[i].Value})
			i++
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
	return len(frames)
}

// minuteStat 是重建出来的分钟统计（与 RollupFieldStat 同形）。
type minuteStat struct {
	Avg, Min, Max, Last float64
	Count               int
}

// TestRollupRebuildFromFramesIsPossible 是 task-24 的**可逆性证明**。
func TestRollupRebuildFromFramesIsPossible(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}

	const dev = 9951
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	const minutes = 10
	const samplesPerMin = 6

	// 造数据：每分钟 6 个采样，值缓慢漂移（avg != last），且**故意不落在量化网格上**
	// 以便让量化往返误差显形。
	var rows []models.UnifiedData
	for m := 0; m < minutes; m++ {
		for i := 0; i < samplesPerMin; i++ {
			ts := start.Add(time.Duration(m)*time.Minute + time.Duration(i)*10*time.Second)
			for fi, name := range rebuildStudyFields {
				base := float64(m*samplesPerMin+i) * 0.137
				v := base + float64(fi)*50 + 0.0037 // 非网格偏移
				rows = append(rows, models.UnifiedData{
					DeviceID: dev, SensorName: name, Value: v, Timestamp: ts,
				})
			}
		}
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
	frameCount := encodeFramesFromUnified(t, db, dev, dict)
	t.Logf("编码帧数 = %d（来自 %d 行）", frameCount, len(rows))

	// ── 路径 A：unified_data 直接聚合（现有 rollup_backfill 的口径）──
	type aggRow struct {
		BucketEpoch int64
		SensorName  string
		AvgV        float64
		MinV        float64
		MaxV        float64
		CntV        int64
		LastV       float64
	}
	var want []aggRow
	if err := db.Raw(
		"SELECT CAST(floor(extract(epoch FROM u.timestamp)/60)*60 AS BIGINT) AS bucket_epoch, "+
			"u.sensor_name, AVG(u.value) AS avg_v, MIN(u.value) AS min_v, MAX(u.value) AS max_v, "+
			"COUNT(*) AS cnt_v, (array_agg(u.value ORDER BY u.timestamp DESC))[1] AS last_v "+
			"FROM unified_data u WHERE u.device_id = ? GROUP BY 1, 2 ORDER BY 1, 2", dev).Scan(&want).Error; err != nil {
		t.Fatalf("聚合 unified_data: %v", err)
	}

	// ── 路径 B：帧表解码重建 ──
	points, stats, err := telemetry.DecodeRangeAnchored(
		context.Background(), db, dict, dev, start, start.Add(minutes*time.Minute), nil)
	if err != nil {
		t.Fatalf("解码帧表: %v", err)
	}
	t.Logf("解码：见帧=%d 解码=%d 跳过(未知slot=%d 坏=%d 断链=%d) 缺失基线=%d 产出点=%d",
		stats.FramesSeen, stats.FramesDecoded, stats.FramesSkippedUnknownSlot,
		stats.FramesSkippedCorrupt, stats.FramesSkippedBrokenChain, stats.MissingBaseline, stats.Points)

	type key struct {
		bucket int64
		name   string
	}
	got := make(map[key]*minuteStat, len(want))
	for i := range points {
		k := key{bucket: points[i].TS.UTC().Unix() / 60 * 60, name: points[i].Name}
		st := got[k]
		if st == nil {
			st = &minuteStat{Min: math.Inf(1), Max: math.Inf(-1)}
			got[k] = st
		}
		v := points[i].Value
		st.Count++
		st.Avg += v
		st.Last = v // points 按时间升序 => 后者覆盖 = 桶内最后值
		if v < st.Min {
			st.Min = v
		}
		if v > st.Max {
			st.Max = v
		}
	}
	for _, st := range got {
		if st.Count > 0 {
			st.Avg /= float64(st.Count)
		}
	}

	// ── 对拍 ──
	if len(want) == 0 {
		t.Fatal("前提不成立：unified_data 聚合没有结果")
	}
	if len(got) == 0 {
		t.Fatal("**帧表重建失败：解不出任何分钟桶** => 停写 unified_data 不可逆，方案需调整")
	}
	if len(got) != len(want) {
		t.Fatalf("重建桶数 = %d, want %d —— 帧表覆盖不全", len(got), len(want))
	}

	stepOf := map[string]float64{"current": 0.01, "total_voltage": 0.01, "rsoc": 1}
	worst := map[string]float64{}
	countMismatch := 0
	for _, w := range want {
		st := got[key{bucket: w.BucketEpoch, name: w.SensorName}]
		if st == nil {
			t.Fatalf("桶 %d / %s 在重建结果里缺失", w.BucketEpoch, w.SensorName)
		}
		if st.Count != int(w.CntV) {
			countMismatch++
			continue
		}
		d := math.Max(math.Abs(st.Avg-w.AvgV),
			math.Max(math.Abs(st.Min-w.MinV), math.Max(math.Abs(st.Max-w.MaxV), math.Abs(st.Last-w.LastV))))
		if d > worst[w.SensorName] {
			worst[w.SensorName] = d
		}
	}
	for _, name := range rebuildStudyFields {
		half := stepOf[name] / 2
		t.Logf("%-14s 最大偏差 = %-14g 量化上界 Step/2 = %g", name, worst[name], half)
		if worst[name] > half+1e-9 {
			t.Errorf("%s 最大偏差 %g 超出量化上界 %g —— 重建引入了额外误差（不只是量化）",
				name, worst[name], half)
		}
	}
	if countMismatch > 0 {
		t.Errorf("%d 个桶的 Count 与源不一致 —— 帧表漏采样（停写方案不可逆）", countMismatch)
	}
}
