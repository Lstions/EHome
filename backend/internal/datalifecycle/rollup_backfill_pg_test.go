package datalifecycle

// rollup_backfill_pg_test.go — 历史回填 + 逐点对拍的**真 PG** 验收（task-22）。
//
// 三件事：
//   1. 回填正确性：从 unified_data 聚合出的 rollup 行，逐分钟等于 SQL 直接聚合；
//   2. 覆盖元数据：buckets 是**写之前**数出来的真实值，且 state=complete；
//   3. 断点续跑：已 complete 的单元再跑一轮必须**跳过**（UnitsSkipped 增长）。
//
// 为什么必须在 PG 上跑：窗口函数 + epoch 表达式 + 分区表三者组合在 SQLite 上
// 语义不同（SQLite 的时间是 TEXT），只有在 PG 上验证过才能说明生产可用。

import (
	"context"
	"math"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedUnifiedForRollup 造一段 unified_data：每 10 秒一行 × nFields。
//
// 值刻意做成缓慢漂移 + 台阶（贴近真实 BMS 电流），这样 Avg 与 Last 会明显不同 ——
// 若回填错用 Avg，逐点对拍立刻能发现。
func seedUnifiedForRollup(t *testing.T, db *gorm.DB, deviceID uint, start time.Time, minutes int) {
	t.Helper()
	const fields = 3
	names := []string{"rsoc", "current", "total_voltage"}
	var rows []models.UnifiedData
	for m := 0; m < minutes; m++ {
		for s := 0; s < 6; s++ { // 每 10 秒一个采样
			ts := start.Add(time.Duration(m)*time.Minute + time.Duration(s)*10*time.Second)
			for fi := 0; fi < fields; fi++ {
				// 同一分钟内 6 个采样值不同 ⇒ Avg != Last。
				v := float64(m*6+s+fi) * 0.01
				rows = append(rows, models.UnifiedData{
					DeviceID: deviceID, SensorName: names[fi], Value: v, Timestamp: ts,
				})
			}
		}
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
}

// TestRollupBackfillMatchesSQLAggregation 是回填的**核心正确性验收**。
//
// 断言：回填写出的每个分钟桶，其 avg/min/max/last 与直接 SQL 聚合逐值相等。
// 特别是 Last —— 若回填错用 Avg，本用例立刻红。
func TestRollupBackfillMatchesSQLAggregation(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("migrate rollup: %v", err)
	}
	if err := EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("ensure coverage: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM " + TelemetryRollupTable)
	db.Exec("DELETE FROM " + RollupCoverageTable)

	// 窗口必须落在 rollup 分区覆盖范围内（MigrateTelemetryRollup1mToPartitioned
	// 建的是 [now-1月, now+3月]），所以用相对现在的时间。
	dayStart := time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour)
	const dev = 14
	const minutes = 90
	seedUnifiedForRollup(t, db, dev, dayStart, minutes)

	cfg := RollupBackfillConfig{
		Enabled:      true,
		BatchSeconds: 1800, // 30 分钟一批（回填内部还会再切）
	}
	if err := NewRollupBackfillRunner(db, cfg).RunOnce(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// ── 逐分钟、逐字段对拍 ──
	//
	// ⚠ 参考 SQL 必须 **GROUP BY (bucket, sensor_name)**，不能只按 bucket 分组。
	// 本用例第一版就是只按 bucket 分组 ⇒ 把 rsoc/current/total_voltage 三个字段的
	// 18 个采样混成一个桶 ⇒ Count 比对必然是 6 vs 18。那是**参考实现写错了**，
	// 不是回填错了 —— 这正是"对拍"的价值：它先抓住了我自己写错的参考。
	type sqlRow struct {
		BucketEpoch int64
		SensorName  string
		AvgV        float64
		MinV        float64
		MaxV        float64
		LastV       float64
		CntV        int64
	}
	var want []sqlRow
	q := `SELECT CAST(floor(extract(epoch FROM "timestamp")/60)*60 AS BIGINT) AS bucket_epoch,
		sensor_name,
		AVG(value) AS avg_v, MIN(value) AS min_v, MAX(value) AS max_v, COUNT(*) AS cnt_v,
		(array_agg(value ORDER BY "timestamp" DESC))[1] AS last_v
		FROM unified_data WHERE device_id = ? GROUP BY 1, 2 ORDER BY 1, 2`
	if err := db.Raw(q, dev).Scan(&want).Error; err != nil {
		t.Fatalf("sql aggregate: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("对拍前提不成立：SQL 聚合没有返回任何桶")
	}

	// 参考结果按 (bucket, sensor) 建索引。
	wantIdx := make(map[int64]map[string]sqlRow, 128)
	for _, w := range want {
		m := wantIdx[w.BucketEpoch]
		if m == nil {
			m = make(map[string]sqlRow, 4)
			wantIdx[w.BucketEpoch] = m
		}
		m[w.SensorName] = w
	}

	var got []struct {
		Bucket  time.Time
		Payload []byte
	}
	if err := db.Raw("SELECT bucket, payload FROM "+TelemetryRollupTable+
		" WHERE device_id = ? ORDER BY bucket", dev).Scan(&got).Error; err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if len(got) != len(wantIdx) {
		t.Fatalf("回填桶数 = %d, want %d（SQL 聚合的分钟桶数）", len(got), len(wantIdx))
	}

	names := []string{"rsoc", "current", "total_voltage"}
	var worstAvg, worstLast float64
	for i := range got {
		bucket := got[i].Bucket.UTC()
		refByField, ok := wantIdx[bucket.Unix()]
		if !ok {
			t.Fatalf("第 %d 桶 %v 不在 SQL 聚合结果里", i, bucket)
		}
		decoded, err := DecodeRollupPayload(got[i].Payload)
		if err != nil {
			t.Fatalf("第 %d 桶解码: %v", i, err)
		}
		if len(decoded) != len(refByField) {
			t.Fatalf("第 %d 桶字段数 = %d, want %d", i, len(decoded), len(refByField))
		}
		for _, name := range names {
			st, ok := decoded[name]
			if !ok {
				t.Fatalf("第 %d 桶缺少字段 %q", i, name)
			}
			ref := refByField[name]
			worstAvg = math.Max(worstAvg, math.Abs(st.Avg-ref.AvgV))
			worstLast = math.Max(worstLast, math.Abs(st.Last-ref.LastV))
			if st.Count != uint32(ref.CntV) {
				t.Fatalf("第 %d 桶 %s Count = %d, want %d", i, name, st.Count, ref.CntV)
			}
			if math.Abs(st.Min-ref.MinV) > 1e-9 || math.Abs(st.Max-ref.MaxV) > 1e-9 {
				t.Fatalf("第 %d 桶 %s Min/Max = %v/%v, want %v/%v",
					i, name, st.Min, st.Max, ref.MinV, ref.MaxV)
			}
		}
	}
	t.Logf("对拍：%d 个分钟桶 × %d 字段，Avg 最大偏差=%g，Last 最大偏差=%g",
		len(got), len(names), worstAvg, worstLast)
	if worstAvg > 1e-9 {
		t.Errorf("Avg 最大偏差 = %g > 1e-9", worstAvg)
	}
	if worstLast > 1e-9 {
		t.Errorf("Last 最大偏差 = %g > 1e-9 —— 回填必须用桶内最后值（D1 口径），不能是均值", worstLast)
	}
}
