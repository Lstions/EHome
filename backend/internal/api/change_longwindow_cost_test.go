package api

// change_longwindow_cost_test.go — change 长窗预筛的**本地合成实测**（默认跳过）。
//
// ============ 这个用例回答什么问题 ============
//
// 「30/90 天的保护触发历史卡到底要跑多久」不能只靠帧数外推：预筛路径的耗时由
// 三部分组成，比例完全不同 ——
//
//	① 读 + 解析 rollup 分钟桶（与**窗口天数**线性相关，每桶 ~1.45 KB payload）；
//	② 候选分钟精解（与**翻转次数**相关，7 天生产只读量测 = 228 个候选 ≈ 2.3%）；
//	③ 输出点集本身（change 点，30 天约 2 万点）。
//
// 本用例在本地 PG 上按**生产量级与形状**合成数据（29 字段 payload、全天 1440 桶/
// 天、每天 33 个 15s 保护脉冲 ⇒ 候选分钟比例与生产同量级），然后端到端计时
// frameSource.Points（change 长窗）。断言结果与「全量值序列直接归约」逐点相等。
//
// 运行（默认 Skip，避免日常测试变慢/造大量数据）：
//
//	EHOME_COST_TEST=1 EHOME_COST_DAYS=30 GOW_WORKDIR=backend \
//	  .scratch_fix/gow-api.sh test ./internal/api/ -run TestChangeLongWindowCostSynthetic -count=1 -v
//
// 实测结果写在 .scratch_fix/CHANGE_LONGWINDOW_REPORT.md。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

// ensureCostPartitions 给测试 schema 补建 [from, to) 覆盖月份的分区（幂等）。
//
// 生产上这些月分区本来就在；测试 schema 每次新建，只会建「上月~未来」几个，
// 90 天窗口要往前 4 个月，所以这里按生产同款裸 DDL 补建。
func ensureCostPartitions(t *testing.T, db *gorm.DB, table string, from, to time.Time) {
	t.Helper()
	cur := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for cur.Before(to) {
		next := cur.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%04d%02d", table, cur.Year(), int(cur.Month()))
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
			name, table, cur.Format("2006-01-02 15:04:05"), next.Format("2006-01-02 15:04:05"))
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("补建分区 %s: %v", name, err)
		}
		cur = next
	}
}

// TestChangeLongWindowCostSynthetic 合成 30/90 天量级数据并端到端计时。
func TestChangeLongWindowCostSynthetic(t *testing.T) {
	if os.Getenv("EHOME_COST_TEST") != "1" {
		t.Skip("合成成本实测默认跳过：设 EHOME_COST_TEST=1 运行（会造 4 万~13 万行 rollup 与 10 万+ 帧）")
	}
	requirePostgresAPI(t)
	days := 30
	if raw := os.Getenv("EHOME_COST_DAYS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			days = n
		}
	}
	db, dict := prepareChangeLongWindowDB(t)
	const dev = 9950

	end := time.Now().UTC().Add(-48 * time.Hour).Truncate(24 * time.Hour)
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	// 90 天窗口跨 4 个月；测试 schema 里只建了「上月~未来」的分区，历史月份要补
	// （生产上这些分区一直存在，只是测试 schema 是新建的）。
	ensureCostPartitions(t, db, datalifecycle.TelemetryRollupTable, start, end)
	ensureCostPartitions(t, db, datalifecycle.TelemetryFrameTable, start, end)
	totalSeconds := int(end.Sub(start) / time.Second)
	const daySeconds = 86400

	// ── 值序列：每秒一个采样（1 Hz）。每天 33 个 15s 脉冲，位置伪随机：
	//    多数落在分钟内部（rollup min!=max ⇒ 候选），少数跨分钟边界。──
	values := make([]float64, totalSeconds)
	for d := 0; d < days; d++ {
		for k := 0; k < 33; k++ {
			minute := (d*137 + k*43 + 61) % 1440 // 43 与 1440 互素 ⇒ 同日不重复
			pulseStart := d*daySeconds + minute*60 + (k*7)%50
			for s := 0; s < 15; s++ {
				if pulseStart+s < totalSeconds {
					values[pulseStart+s] = 1
				}
			}
		}
	}
	// 期望结果：直接从每秒值序列做 change 归约（首点 + 每次值变化）。
	var want []HistoryPoint
	for i := 0; i < totalSeconds; i++ {
		if len(want) == 0 || values[i] != values[i-1] {
			want = append(want, HistoryPoint{
				Timestamp: start.Add(time.Duration(i) * time.Second),
				Value:     values[i],
			})
		}
	}

	// ── ① 造 rollup：每天 1440 个桶，每桶 29 字段（≈1.45 KB，生产 dev14 宽度）──
	const rollupFields = 29
	makePayload := func(min, max, last float64) []byte {
		fields := make(map[string]*datalifecycle.RollupFieldStat, rollupFields)
		fields["protection_status"] = &datalifecycle.RollupFieldStat{Avg: last, Min: min, Max: max, Last: last, Count: 60}
		for i := 1; i < rollupFields; i++ {
			v := float64(i) * 1.5
			fields[fmt.Sprintf("field_%02d", i)] = &datalifecycle.RollupFieldStat{Avg: v, Min: v, Max: v, Last: v, Count: 60}
		}
		b, err := datalifecycle.EncodeRollupPayloadV1(fields)
		if err != nil {
			t.Fatalf("encode rollup payload: %v", err)
		}
		return b
	}
	tSeed := time.Now()
	totalBuckets := totalSeconds / 60
	rows := make([]models.TelemetryRollup1m, 0, totalBuckets)
	minutes := make([]changeRollupMinute, 0, totalBuckets)
	for s := 0; s < totalSeconds; s += 60 {
		seg := values[s : s+60]
		mn, mx, last := seg[0], seg[0], seg[0]
		for _, v := range seg {
			if v < mn {
				mn = v
			}
			if v > mx {
				mx = v
			}
			last = v
		}
		bucket := start.Add(time.Duration(s) * time.Second)
		rows = append(rows, models.TelemetryRollup1m{DeviceID: dev, Bucket: bucket, Payload: makePayload(mn, mx, last)})
		minutes = append(minutes, changeRollupMinute{Start: bucket, Min: mn, Max: mx, Last: last, Count: 60})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("insert rollup rows: %v", err)
	}
	for d := 0; d < days; d++ {
		day := start.Add(time.Duration(d) * 24 * time.Hour).Format("2006-01-02")
		if err := datalifecycle.MarkRollupCoverageWithSource(
			db, dev, day, 1440, datalifecycle.RollupCoverageComplete, datalifecycle.RollupSourceFrames,
		); err != nil {
			t.Fatalf("mark coverage %s: %v", day, err)
		}
	}
	seedRollup := time.Since(tSeed)

	// ── ② 造帧：只给候选分钟造（这正是预筛的收益：其余分钟不读帧表）──
	candidates := changeCandidateIndexes(minutes)
	tSeed = time.Now()
	needed := make(map[int64]bool, len(candidates)*121)
	for _, idx := range candidates {
		base := int(minutes[idx].Start.Sub(start) / time.Second)
		for d := -60; d < 60; d++ {
			off := base + d
			if off >= 0 && off < totalSeconds {
				needed[int64(off)] = true
			}
		}
	}
	offsets := make([]int64, 0, len(needed))
	for off := range needed {
		offsets = append(offsets, off)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	frames := make([]models.TelemetryFrame, 0, len(offsets))
	for _, off := range offsets {
		f, err := enc.EncodeFrame(dev, start.Add(time.Duration(off)*time.Second).UTC(), 0,
			[]telemetry.Reading{{Name: "protection_status", Value: values[int(off)]}})
		if err != nil {
			t.Fatalf("encode frame: %v", err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 1000).Error; err != nil {
		t.Fatalf("insert frames: %v", err)
	}
	seedFrames := time.Since(tSeed)

	// ── ③ 端到端计时：生产同款分界（6h），change 长窗预筛路径 ──
	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)
	SetFrameDecodeMaxWindow(6 * time.Hour)
	ResetFrameDecodeCache()

	began := time.Now()
	got, err := (frameSource{db: db, dict: dict}).Points(context.Background(), changeQuery(dev, start, end))
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("change 长窗读取失败: %v", err)
	}
	assertPointsEqual(t, got, want, fmt.Sprintf("%d 天合成数据端到端", days))

	t.Logf("合成 %d 天：rollup 桶=%d（插入 %s）| 候选分钟=%d/%d（%.2f%%）| 帧=%d（插入 %s）",
		days, totalBuckets, seedRollup.Round(time.Millisecond),
		len(candidates), totalBuckets, 100*float64(len(candidates))/float64(totalBuckets),
		len(frames), seedFrames.Round(time.Millisecond))
	t.Logf("端到端 change 长窗读取 = %s，输出 %d 个 change 点（与全量值序列逐点相等）",
		elapsed.Round(time.Millisecond), len(got))
}
