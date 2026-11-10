package api

// change_longwindow_test.go — change 模式长窗（rollup 精确预筛 + 帧表精解）的验收。
//
// ============ 为什么必须对拍「全量解码」而不是只断言点数 ============
//
// 预筛路径跳过 97%+ 的分钟，最危险的错误是**静默少算/多算翻转点**：
//
//	· 漏解一个含翻转的分钟  => 保护触发次数变少（用户以为设备没触发过保护）
//	· 接缝多输出一个「首点」 => 触发次数变多（误报设备故障）
//	· 跨分钟变化的时刻错位   => 持续时长算错（最多偏 60s）
//
// 这三种都「看起来正常」，所以验收必须是**逐点对拍**：预筛结果 == 单次全量解码结果
// （同一批帧、同一个 change 归约），点的时间戳与值都要相等。
//
// 四类用例：
//  1. 纯函数：候选分钟判据（含 min!=max / 跨桶变化 / 首分钟 / 间隙 / NaN）；
//  2. PG 对拍：预筛 == 全量解码（数据里刻意埋了「分钟内翻转」「分钟边界翻转」
//     「分段边界翻转」三类接缝）；
//  3. PG 对拍：rollup 覆盖不可用 ⇒ 回落纯帧分段解码，结果仍与全量解码逐点相等；
//  4. PG 对拍：实时尾巴（rollup 尚未落定的最近几分钟）不能丢。
//
// 另外守两条「诚实失败」：>90 天窗口 / 预筛不可用且 >8 天 ⇒ 必须报错（不能返回 []）。

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// prepareChangeLongWindowDB 建帧表 + rollup 表 + 覆盖表，返回 db 与注入的 slot 字典。
func prepareChangeLongWindowDB(t *testing.T) (*gorm.DB, telemetry.SlotDictionary) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")

	if err := datalifecycle.MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("建 rollup 表: %v", err)
	}
	if err := datalifecycle.EnsureTelemetryRollup1mPartitions(db, 3); err != nil {
		t.Fatalf("建 rollup 分区: %v", err)
	}
	db.Exec("DELETE FROM " + datalifecycle.TelemetryRollupTable)
	if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("建覆盖表: %v", err)
	}
	db.Exec("DELETE FROM " + datalifecycle.RollupCoverageTable)

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	t.Cleanup(func() { SetHistoryFrameDictionary(nil) })
	ResetFrameDecodeCache()
	t.Cleanup(ResetFrameDecodeCache)
	return db, dict
}

// seedChangeFrames 按步长造帧：每帧两个字段（保护状态 + 对照组 current）。
//
// 对照组字段的存在让 payload 形状更接近生产（宽行多字段），同时验证字段过滤
// 没有把别的字段混进 change 序列。
func seedChangeFrames(
	t *testing.T, db *gorm.DB, dict telemetry.SlotDictionary, dev uint,
	start, end time.Time, step time.Duration, valueAt func(time.Time) float64,
) {
	t.Helper()
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	var frames []models.TelemetryFrame
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		readings := []telemetry.Reading{
			{Name: "protection_status", Value: valueAt(ts)},
			{Name: "current", Value: 1},
		}
		f, err := enc.EncodeFrame(dev, ts.UTC(), 0, readings)
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

// rollupMinutesFromValues 用与 RollupCollector.Observe 相同的口径从帧采样值算出分钟统计。
func rollupMinutesFromValues(start, end time.Time, step time.Duration, valueAt func(time.Time) float64) []changeRollupMinute {
	type acc struct {
		min, max, last float64
		count          uint32
	}
	buckets := make(map[int64]*acc)
	order := make([]int64, 0, 64)
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		b := ts.Truncate(time.Minute).Unix()
		v := valueAt(ts)
		a := buckets[b]
		if a == nil {
			a = &acc{min: v, max: v, last: v}
			buckets[b] = a
			order = append(order, b)
		}
		if v < a.min {
			a.min = v
		}
		if v > a.max {
			a.max = v
		}
		a.last = v
		a.count++
	}
	out := make([]changeRollupMinute, 0, len(order))
	for _, b := range order {
		a := buckets[b]
		out = append(out, changeRollupMinute{Start: time.Unix(b, 0).UTC(), Min: a.min, Max: a.max, Last: a.last, Count: a.count})
	}
	return out
}

// seedChangeRollup 写入分钟桶 payload（只含被查询的字段；真实 payload 含全部字段，
// 但读侧只按字段名取，单字段足够验证预筛）+ 覆盖元数据。
func seedChangeRollup(
	t *testing.T, db *gorm.DB, dev uint,
	start, end time.Time, step time.Duration, valueAt func(time.Time) float64,
) []changeRollupMinute {
	t.Helper()
	minutes := rollupMinutesFromValues(start, end, step, valueAt)
	dayBuckets := make(map[string]int)
	for _, m := range minutes {
		payload, err := datalifecycle.EncodeRollupPayloadV1(map[string]*datalifecycle.RollupFieldStat{
			"protection_status": {Avg: m.Last, Min: m.Min, Max: m.Max, Last: m.Last, Count: m.Count},
		})
		if err != nil {
			t.Fatalf("encode rollup payload: %v", err)
		}
		if err := db.Exec(
			"INSERT INTO "+datalifecycle.TelemetryRollupTable+" (device_id, bucket, payload) VALUES (?, ?, ?)",
			dev, m.Start, payload,
		).Error; err != nil {
			t.Fatalf("insert rollup row %s: %v", m.Start.Format(time.RFC3339), err)
		}
		dayBuckets[m.Start.Format("2006-01-02")]++
	}
	for day, n := range dayBuckets {
		if err := datalifecycle.MarkRollupCoverageWithSource(
			db, dev, day, n, datalifecycle.RollupCoverageComplete, datalifecycle.RollupSourceFrames,
		); err != nil {
			t.Fatalf("mark coverage %s: %v", day, err)
		}
	}
	return minutes
}

// dropChangeRollupCoverage 删掉覆盖元数据，模拟「回填还没覆盖该窗口」。
func dropChangeRollupCoverage(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DELETE FROM " + datalifecycle.RollupCoverageTable).Error; err != nil {
		t.Fatalf("delete coverage: %v", err)
	}
}

// expectedChangePoints 直接从**全部帧采样值**算出 change 点集（首点 + 每次值变化）。
func expectedChangePoints(start, end time.Time, step time.Duration, valueAt func(time.Time) float64) []HistoryPoint {
	var out []HistoryPoint
	var prev float64
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		v := valueAt(ts)
		if len(out) == 0 || v != prev {
			out = append(out, HistoryPoint{Timestamp: ts.UTC(), Value: v})
		}
		prev = v
	}
	return out
}

// assertPointsEqual 逐点断言（点数 + 时间戳 + 值）。
func assertPointsEqual(t *testing.T, got, want []HistoryPoint, why string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s：点数 = %d, want %d\ngot=%v\nwant=%v", why, len(got), len(want), got, want)
	}
	for i := range want {
		if !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Fatalf("%s：第 %d 点时间戳 = %s, want %s", why, i,
				got[i].Timestamp.Format(time.RFC3339Nano), want[i].Timestamp.Format(time.RFC3339Nano))
		}
		if got[i].Value != want[i].Value {
			t.Fatalf("%s：第 %d 点值 = %v, want %v", why, i, got[i].Value, want[i].Value)
		}
	}
}

// changeQuery 构造一个 change 长窗查询。
func changeQuery(dev uint, start, end time.Time) historyPointQuery {
	return historyPointQuery{
		EdgeDeviceID: dev, Category: "protection_status",
		Start: start, End: end, MaxPoints: 500, Mode: AggModeChange,
	}
}

// ============ ① 纯函数：候选分钟判据 ============

func TestChangeCandidateIndexes(t *testing.T) {
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mk := func(i int, min, max, last float64) changeRollupMinute {
		return changeRollupMinute{
			Start: base.Add(time.Duration(i) * time.Minute),
			Min:   min, Max: max, Last: last, Count: 1,
		}
	}
	cases := []struct {
		name    string
		minutes []changeRollupMinute
		want    []int
	}{
		{
			// 常量段：只有「窗口内第一个有数据的分钟」是候选（要输出首点）。
			name:    "常量段只解首分钟",
			minutes: []changeRollupMinute{mk(0, 0, 0, 0), mk(1, 0, 0, 0), mk(2, 0, 0, 0)},
			want:    []int{0},
		},
		{
			// 分钟内翻转：min != max ⇒ 该分钟必解；后续常量分钟（值相同）跳过。
			name:    "分钟内翻转只标记该分钟",
			minutes: []changeRollupMinute{mk(0, 0, 0, 0), mk(1, 0, 1, 1), mk(2, 1, 1, 1)},
			want:    []int{0, 1},
		},
		{
			// 跨分钟变化：分钟 0 常量 0、分钟 1 常量 1 ⇒ 变化发生在分钟 1 首个采样点，
			// 必须精解分钟 1 拿时间戳（只比较值会让触发时刻错位 60s）。
			name:    "跨分钟变化标记落点的分钟",
			minutes: []changeRollupMinute{mk(0, 0, 0, 0), mk(1, 1, 1, 1), mk(2, 1, 1, 1)},
			want:    []int{0, 1},
		},
		{
			// 间隙（设备离线几分钟）后值不同 ⇒ 仍要解（与上一个已知值比较）。
			name:    "间隙后值不同仍标记",
			minutes: []changeRollupMinute{mk(0, 0, 0, 0), mk(5, 1, 1, 1)},
			want:    []int{0, 1},
		},
		{
			// 间隙后值相同 ⇒ 无翻转点，跳过。
			name:    "间隙后值相同可跳过",
			minutes: []changeRollupMinute{mk(0, 1, 1, 1), mk(5, 1, 1, 1)},
			want:    []int{0},
		},
		{
			// NaN：帧表量化表达不了，但 rollup 回填自 unified_data 理论可能有；
			// 保守：含 NaN 必解，并清空「上一个已知值」⇒ 下一分钟也必解。
			name:    "NaN 保守标记",
			minutes: []changeRollupMinute{mk(0, 0, 0, 0), mk(1, 0, 0, math.NaN()), mk(2, 1, 1, 1)},
			want:    []int{0, 1, 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := changeCandidateIndexes(c.minutes)
			if len(got) != len(c.want) {
				t.Fatalf("候选下标 = %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("候选下标 = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// ============ ② 门槛/错误信息：90 天上限与 fallback 上限必须诚实 ============

func TestChangeLongWindowEligibilityAndErrors(t *testing.T) {
	dict := telemetry.NewMemorySlots()
	start := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	q := historyPointQuery{EdgeDeviceID: 14, Category: "protection_status", Start: start, End: start.Add(30 * 24 * time.Hour), MaxPoints: 500, Mode: AggModeChange}
	if !frameReadEligible(true, dict, q) {
		t.Fatal("30 天 change 窗口必须合格（rollup 精确预筛路径，task-5）")
	}
	q90 := q
	q90.End = start.Add(90 * 24 * time.Hour)
	if !frameReadEligible(true, dict, q90) {
		t.Fatal("90 天 change 窗口必须合格（诊断卡最大档位）")
	}
	q91 := q
	q91.End = start.Add(91 * 24 * time.Hour)
	if frameReadEligible(true, dict, q91) {
		t.Fatal(">90 天 change 窗口必须拒绝（没有业务场景，且 rollup 保留期外读不到）")
	}
	err := frameReadUnavailableError(dict, q91)
	if err == nil || !strings.Contains(err.Error(), "change") || !strings.Contains(err.Error(), "90 天") {
		t.Fatalf(">90 天 change 的错误信息必须说清 change 专属上限，got: %v", err)
	}

	// avg 的短窗分界不受 change 改造影响（>24h 仍然必须拒绝）。
	avg := q
	avg.Mode = AggModeAvg
	if frameReadEligible(true, dict, avg) {
		t.Fatal("avg 30 天窗口不该在帧表短窗解码路径上（avg 走 rollup）")
	}
}

// ============ ③ PG 对拍：预筛 == 全量解码 ============

func TestChangeLongWindowPrefilterMatchesFullDecode(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)

	const dev = 9941
	// 固定在过去（避免实时尾巴；rollup 覆盖元数据也按「过去的天」校验）。
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	const step = 30 * time.Second
	const window = 30 * time.Minute
	end := start.Add(window)

	// 数据里刻意埋三类接缝：
	//   · 分钟内翻转（第 3 分钟 0→1、第 15 分钟 0→1）
	//   · 跨分钟变化（第 10 分钟首个采样点 1→0）
	//   · 分段边界变化（第 5/10/15 分钟边界 = 5 分钟分界下的段边界）
	valueAt := func(ts time.Time) float64 {
		mi := int(ts.Sub(start) / time.Minute)
		sec := int(ts.Sub(start)/time.Second) % 60
		switch {
		case mi < 3:
			return 0
		case mi == 3:
			if sec < 30 {
				return 0
			}
			return 1
		case mi < 10:
			return 1
		case mi == 10:
			return 0 // 跨分钟变化：上一分钟末值 1，本分钟首个采样点 0
		case mi < 15:
			return 0
		case mi == 15:
			if sec < 30 {
				return 0
			}
			return 1
		case mi < 25:
			return 1
		default:
			return 0 // 第 25 分钟边界 1→0
		}
	}

	seedChangeFrames(t, db, dict, dev, start, end, step, valueAt)
	rollupMinutes := seedChangeRollup(t, db, dev, start, end, step, valueAt)

	want := expectedChangePoints(start, end, step, valueAt)
	ctx := context.Background()
	src := frameSource{db: db, dict: dict}

	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)

	// ── 参考：单次整窗解码（改变分界到 1h ⇒ 走单段路径，无预筛无分段）──
	SetFrameDecodeMaxWindow(time.Hour)
	ResetFrameDecodeCache()
	ref, err := src.Points(ctx, changeQuery(dev, start, end))
	if err != nil {
		t.Fatalf("参考全量解码失败: %v", err)
	}
	assertPointsEqual(t, ref, want, "参考全量解码 vs 期望值")

	// ── 预筛路径：分界 5 分钟 ⇒ 30 分钟窗口走长窗分支 ──
	SetFrameDecodeMaxWindow(5 * time.Minute)
	ResetFrameDecodeCache()
	var prefilterDecodes int64
	changePrefilterDecodeHookForTest = &prefilterDecodes
	t.Cleanup(func() { changePrefilterDecodeHookForTest = nil })
	got, err := src.Points(ctx, changeQuery(dev, start, end))
	if err != nil {
		t.Fatalf("预筛路径失败: %v", err)
	}
	changePrefilterDecodeHookForTest = nil
	assertPointsEqual(t, got, want, "预筛路径 vs 期望值")
	assertPointsEqual(t, got, ref, "预筛路径 vs 全量解码")

	// 候选分钟数（纯函数算出）必须等于实际解码次数：
	// 正常关键帧（60s）下锚点距离 ≤60s < 2min 阈值 ⇒ 每个候选一次单分钟解码。
	candidates := changeCandidateIndexes(rollupMinutes)
	if int64(len(candidates)) != prefilterDecodes {
		t.Fatalf("预筛实际解码 %d 次, want %d 次（候选分钟数）", prefilterDecodes, len(candidates))
	}
	// 核心收益：30 分钟只解了极少数分钟（其余靠 min==max 精确跳过）。
	if len(candidates) > 10 {
		t.Fatalf("候选分钟 %d 个（共 %d 个桶）—— 预筛没有起到跳过作用", len(candidates), len(rollupMinutes))
	}
	t.Logf("预筛：%d 个分钟桶 -> %d 个候选分钟（%d 次解码），输出 %d 个 change 点，与全量解码逐点相等",
		len(rollupMinutes), len(candidates), prefilterDecodes, len(got))

	// ── 回落路径：删掉覆盖元数据 ⇒ 预筛不可用 ⇒ 纯帧分段解码 ──
	dropChangeRollupCoverage(t, db)
	ResetFrameDecodeCache()
	var segDecodes int64
	decodeHookForTest = &segDecodes
	t.Cleanup(func() { decodeHookForTest = nil })
	gotFallback, err := src.Points(ctx, changeQuery(dev, start, end))
	if err != nil {
		t.Fatalf("纯帧分段解码回落失败: %v", err)
	}
	decodeHookForTest = nil
	assertPointsEqual(t, gotFallback, want, "纯帧分段解码回落 vs 期望值")
	assertPointsEqual(t, gotFallback, ref, "纯帧分段解码回落 vs 全量解码")
	if wantSegments := int((window + 5*time.Minute - 1) / (5 * time.Minute)); segDecodes != int64(wantSegments) {
		t.Fatalf("分段回落实际解码 %d 段, want %d 段", segDecodes, wantSegments)
	}
	t.Logf("分段回落：%d 段解码，输出与全量解码逐点相等", segDecodes)
}

// ============ ④ PG 对拍：实时尾巴不能丢 ============

func TestChangeLongWindowIncludesLiveTail(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)

	// 固定「现在」，让 cutoff / bodyEnd / 尾巴都可复现。
	fixedNow := time.Now().UTC().Truncate(time.Hour).Add(-time.Minute)
	restoreClock := datalifecycle.SetRollupCoverageClock(func() time.Time { return fixedNow })
	defer restoreClock()

	const dev = 9942
	const step = 30 * time.Second
	end := fixedNow.Add(-5 * time.Second)
	start := end.Add(-20 * time.Minute)
	cutoff := fixedNow.Add(-datalifecycle.RollupCoverageLiveLag)
	bodyEnd := cutoff.Truncate(time.Minute)

	// 变化 1：主体窗口内（分钟边界）；变化 2：尾巴窗口内（rollup 还没有它）。
	tailChangeAt := end.Add(-70 * time.Second)
	valueAt := func(ts time.Time) float64 {
		if ts.Before(start.Add(7 * time.Minute)) {
			return 0
		}
		if ts.Before(tailChangeAt) {
			return 1
		}
		return 0
	}

	seedChangeFrames(t, db, dict, dev, start, end, step, valueAt)
	// rollup 只覆盖「已落定的完整分钟」；尾巴必须由帧表解码提供。
	// ⚠ 采样网格必须与 seedChangeFrames **完全一致**（同一个 start 起点、同一个 step）：
	// 否则按分钟聚合出的 min/max 与真实帧不一致（测试夹具自己骗自己）。
	seedChangeRollup(t, db, dev, start, bodyEnd, step, valueAt)

	want := expectedChangePoints(start, end, step, valueAt)

	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)
	SetFrameDecodeMaxWindow(5 * time.Minute)
	ResetFrameDecodeCache()

	got, err := (frameSource{db: db, dict: dict}).Points(context.Background(), changeQuery(dev, start, end))
	if err != nil {
		t.Fatalf("预筛+尾巴解码失败: %v", err)
	}
	assertPointsEqual(t, got, want, "预筛 + 实时尾巴 vs 期望值")
	t.Logf("主体 %s..%s + 尾巴 %s..%s => %d 个 change 点（含尾巴里的翻转）",
		start.Format(time.RFC3339), bodyEnd.Format(time.RFC3339),
		bodyEnd.Format(time.RFC3339), end.Format(time.RFC3339), len(got))
}

// ============ ⑤ 诚实失败：预筛不可用且窗口 > 纯帧分段上限 ============

func TestChangeLongWindowBeyondPureFrameLimitReturnsHonestError(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)

	// 9 天 > maxChangeDecodeWindow(8 天)，且没有 rollup 覆盖 ⇒ 预筛不可用。
	start := time.Now().UTC().Add(-15 * 24 * time.Hour)
	end := start.Add(9 * 24 * time.Hour)

	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)
	SetFrameDecodeMaxWindow(6 * time.Hour)

	_, err := (frameSource{db: db, dict: dict}).Points(context.Background(), changeQuery(9943, start, end))
	if err == nil {
		t.Fatal("9 天窗口 + 无 rollup 覆盖 ⇒ 必须返回诚实错误，不能静默返回空点集")
	}
	if !strings.Contains(err.Error(), "超过纯帧分段解码上限") {
		t.Fatalf("错误信息必须说清 fallback 上限，got: %v", err)
	}
	t.Logf("诚实错误（符合预期）: %v", err)
}

// ============ ③b PG 对拍：长链（旧 encoder 存量帧没有周期关键帧）= 合并解码 ============

func TestChangeLongWindowLongChainGrouping(t *testing.T) {
	requirePostgresAPI(t)
	db, dict := prepareChangeLongWindowDB(t)

	const dev = 9951
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	const step = 10 * time.Second
	const window = 10 * time.Minute
	end := start.Add(window)

	// 只埋**一个**关键帧（窗口首帧）⇒ 后面每个候选分钟的锚点距离都 > 2min，
	// 触发「长链合并解码」分支（2026-11-08~11-10 的存量帧就是这种形态）。
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 10 * time.Minute}, dict)
	var frames []models.TelemetryFrame
	valueAt := func(ts time.Time) float64 {
		mi := int(ts.Sub(start) / time.Minute)
		sec := int(ts.Sub(start)/time.Second) % 60
		switch {
		case mi < 1:
			return 0
		case mi < 4:
			return 1
		case mi == 4:
			if sec >= 20 && sec < 50 {
				return 2
			}
			return 1
		case mi < 7:
			return 1
		case mi < 9:
			return 0
		default: // 第 9 分钟：分钟内的脉冲
			if sec >= 20 && sec < 40 {
				return 2
			}
			return 0
		}
	}
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		fr, err := enc.EncodeFrame(dev, ts.UTC(), 0, []telemetry.Reading{
			{Name: "protection_status", Value: valueAt(ts)},
		})
		if err != nil {
			t.Fatalf("encode %s: %v", ts.Format(time.RFC3339), err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: fr.DeviceID, TS: fr.TS, Seq: fr.Seq,
			FrameKind: fr.FrameKind, SlotID: fr.SlotID, Payload: fr.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 500).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}
	seedChangeRollup(t, db, dev, start, end, step, valueAt)
	want := expectedChangePoints(start, end, step, valueAt)

	oldMax := FrameDecodeMaxWindow()
	defer SetFrameDecodeMaxWindow(oldMax)
	src := frameSource{db: db, dict: dict}
	ctx := context.Background()

	// 参考：单次整窗解码（分界 1h，窗口 10min ⇒ 单段；解码从唯一锚点 start 起）。
	SetFrameDecodeMaxWindow(time.Hour)
	ResetFrameDecodeCache()
	ref, err := src.Points(ctx, changeQuery(dev, start, end))
	if err != nil {
		t.Fatalf("参考全量解码失败: %v", err)
	}
	assertPointsEqual(t, ref, want, "长链参考全量解码 vs 期望值")

	// 预筛路径：分界 2min ⇒ 长窗分支 + 长链合并。
	SetFrameDecodeMaxWindow(2 * time.Minute)
	ResetFrameDecodeCache()
	var decodes int64
	changePrefilterDecodeHookForTest = &decodes
	t.Cleanup(func() { changePrefilterDecodeHookForTest = nil })
	got, err := src.Points(ctx, changeQuery(dev, start, end))
	changePrefilterDecodeHookForTest = nil
	if err != nil {
		t.Fatalf("长链预筛路径失败: %v", err)
	}
	assertPointsEqual(t, got, want, "长链预筛 vs 期望值")
	assertPointsEqual(t, got, ref, "长链预筛 vs 全量解码")

	// 候选分钟：m0（首点）、m1（跨分钟变化，锚点 60s ≤2min ⇒ 单独解）、
	// m4/m7/m9（锚点 4min+ ⇒ 长链，合并成 1 段）⇒ 3 次解码。
	if decodes != 3 {
		t.Fatalf("长链合并后解码次数 = %d, want 3（前两个候选单解 + 后三个合并成 1 段）", decodes)
	}
	t.Logf("长链：5 个候选分钟 -> %d 次解码（合并省了 2 次从首锚点的重复解码），输出与全量解码逐点相等", decodes)
}
