package telemetry

// stream_decode_test.go — 分页/流式解码（去 32768 帧墙）的回归用例。
//
// 背景：生产 dev14 在 2026-11-08T15:57:58Z ~ 2026-11-10T05:21:16Z 的存量帧没有
// 周期关键帧（旧 encoder bug 的历史损伤）。旧读路径一次 queryFrames(maxFrames+1)
// 后「保留最新、丢弃最老」⇒ 唯一的关键帧锚点被丢 ⇒ 窗口内 0 点 / FramesTruncated
// 报错 ⇒ API 500 / 前端空图。这里用「1 个关键帧 + 50000 增量帧」把修复钉死。

import (
	"context"
	"math"
	"reflect"
	"runtime"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// persistSyntheticChain 落库一条「1 个关键帧 + n-1 个增量帧」的长链。
//
// KeyFrameInterval 设成 24h ⇒ 编码器只在第一帧发关键帧，精确模拟生产 dev14
// 2026-11-08~11-10 的历史损伤形态（连续 37h 没有周期关键帧）。
func persistSyntheticChain(t *testing.T, db *gorm.DB, dict SlotDictionary, deviceID uint, n int) (base time.Time) {
	t.Helper()
	enc := NewEncoder(EncoderConfig{KeyFrameInterval: 24 * time.Hour}, dict)
	store := NewStore(db, dict, nil)
	base = time.Date(2026, 11, 8, 15, 57, 58, 0, time.UTC)
	batch := make([]EncodedFrame, 0, 4096)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := store.PersistFrames(batch); err != nil {
			t.Fatalf("persist frames: %v", err)
		}
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		f, err := enc.EncodeFrame(deviceID, at, 0, synthFrame(at, i))
		if err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}
		batch = append(batch, f)
		if len(batch) == cap(batch) {
			flush()
		}
	}
	flush()
	return base
}

// TestStreamDecodeTailWindowAfterSingleKeyFrame 是 task-4 的验收用例。
//
// 1 个关键帧 + 50000 个增量帧（≈13.9h，远超 MaxDecodeFrames=32768），请求窗口取
// 链尾 60s：旧实现会把唯一的关键帧锚点丢掉（0 点/截断报错）；分页解码必须解出
// 非空且数值正确的窗口数据，且 FramesTruncated 必须为 0。
func TestStreamDecodeTailWindowAfterSingleKeyFrame(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	const deviceID = 41
	const totalFrames = 50001 // 1 关键帧 + 50000 增量帧

	base := persistSyntheticChain(t, db, dict, deviceID, totalFrames)

	const tailOffset = 40000 // 窗口起点落在链中间（距锚点 40000 帧）
	from := base.Add(tailOffset * time.Second)
	to := from.Add(60 * time.Second) // 1 分钟窗口 = 60 帧

	// 前置条件：锚点→to 的帧数必须 > MaxDecodeFrames，否则本用例证明不了「去墙」。
	scanFrames := tailOffset + 60 // 锚点帧 + 窗口 60 帧
	if scanFrames <= MaxDecodeFrames {
		t.Fatalf("前置条件不成立：扫描帧数 %d 必须 > MaxDecodeFrames %d", scanFrames, MaxDecodeFrames)
	}

	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	points, stats, err := DecodeRangeAnchored(context.Background(), db, dict, deviceID, from, to, nil)
	dur := time.Since(start)
	runtime.ReadMemStats(&ms1)
	if err != nil {
		t.Fatalf("DecodeRangeAnchored: %v", err)
	}
	t.Logf("分页解码：扫描=%d 帧，窗口产出=%d 点，耗时=%s，TotalAlloc 增量=%.2f MB，HeapAlloc 增量=%.2f MB",
		stats.FramesSeen, stats.Points, dur.Round(time.Millisecond),
		float64(ms1.TotalAlloc-ms0.TotalAlloc)/1024/1024,
		float64(int64(ms1.HeapAlloc)-int64(ms0.HeapAlloc))/1024/1024)

	if len(points) == 0 {
		t.Fatal("链尾窗口解出 0 点 —— 锚点又被丢了吗？（旧实现就是在这里返回 500）")
	}
	// 扫描量必须超过旧墙，且真正被解码（不是靠截断）
	if stats.FramesSeen != int64(scanFrames) {
		t.Errorf("FramesSeen = %d, want %d（锚点起全量扫描）", stats.FramesSeen, scanFrames)
	}
	if stats.FramesSeen <= MaxDecodeFrames {
		t.Errorf("FramesSeen = %d 必须 > MaxDecodeFrames %d（否则旧实现也能过，用例没意义）",
			stats.FramesSeen, MaxDecodeFrames)
	}
	if stats.FramesTruncated != 0 {
		t.Errorf("FramesTruncated = %d, want 0（正常窗口不该触发扫描预算）", stats.FramesTruncated)
	}
	if stats.KeyFrames != 1 {
		t.Errorf("KeyFrames = %d, want 1（这条链只有一个关键帧）", stats.KeyFrames)
	}
	if stats.FramesSkippedBrokenChain != 0 || stats.FramesSkippedCorrupt != 0 {
		t.Errorf("链应完整：broken_chain=%d corrupt=%d，want 0/0",
			stats.FramesSkippedBrokenChain, stats.FramesSkippedCorrupt)
	}
	if stats.MissingBaseline != 0 {
		t.Errorf("MissingBaseline = %d, want 0（锚点参与解码，不应有无基线字段）", stats.MissingBaseline)
	}
	// 只收集窗口内的点：60 帧 × 29 字段（synthFrame 每帧全字段有效）
	wantPoints := int64(60 * 29)
	if int64(len(points)) != wantPoints || stats.Points != wantPoints {
		t.Fatalf("窗口点数 = %d（stats.Points=%d），want %d（锚点预热段的点必须丢弃）",
			len(points), stats.Points, wantPoints)
	}
	for _, p := range points {
		if p.TS.Before(from) || !p.TS.Before(to) {
			t.Fatalf("点 %s 落在窗口 [%s,%s) 之外", p.TS.Format(time.RFC3339), from, to)
		}
	}
	// 值必须正确：窗口末帧（to-1s，即第 40059 帧）的所有字段逐值对拍原始读数
	snap := make(map[string]float64, 32)
	for _, p := range points {
		snap[p.Name] = p.Value
	}
	lastAt := to.Add(-time.Second)
	for _, rd := range synthFrame(lastAt, tailOffset+59) {
		sp, ok := SpecFor(rd.Name)
		if !ok {
			t.Fatalf("spec for %q 不存在", rd.Name)
		}
		got, ok := snap[rd.Name]
		if !ok {
			t.Errorf("窗口末帧缺字段 %q", rd.Name)
			continue
		}
		if d := math.Abs(got - rd.Value); d > sp.MaxError()+1e-9 {
			t.Errorf("字段 %q 解码=%v 原值=%v 误差=%v > 步长/2=%v", rd.Name, got, rd.Value, d, sp.MaxError())
		}
	}
}

// TestDecodeRangeAnchoredBoundedScanBudgetTruncation 钉住「真正的极限不静默」：
// 扫描预算耗尽但仍有帧时，FramesTruncated 必须 = 剩余未解码帧数（不是 0，
// 也不是把锚点丢掉后假装解完）。
func TestDecodeRangeAnchoredBoundedScanBudgetTruncation(t *testing.T) {
	db := openTelemetryTestDB(t)
	dict := NewDBSlots(db)
	const deviceID = 42
	const totalFrames = 500
	base := persistSyntheticChain(t, db, dict, deviceID, totalFrames)

	from, to := base, base.Add(totalFrames*time.Second)
	const budget = 200
	points, stats, err := DecodeRangeAnchoredBounded(
		context.Background(), db, dict, deviceID, from, to, nil, 24*time.Hour, budget)
	if err != nil {
		t.Fatalf("DecodeRangeAnchoredBounded: %v", err)
	}
	if stats.FramesSeen != budget {
		t.Errorf("FramesSeen = %d, want %d（只扫了预算内帧）", stats.FramesSeen, budget)
	}
	if want := int64(totalFrames - budget); stats.FramesTruncated != want {
		t.Errorf("FramesTruncated = %d, want %d（剩余未解码帧数必须如实上报）",
			stats.FramesTruncated, want)
	}
	if got, want := int64(len(points)), int64(budget*29); got != want {
		t.Errorf("已解码点数 = %d, want %d", got, want)
	}
}

// TestDecoderFeedMatchesDecodeFrames 钉住「抽状态机是等价变换」：
// 同一批帧分页 Feed 与一次性 DecodeFrames 的点序列、统计必须逐字段一致 ——
// 包括坏 payload 断链、未知 slot、列值/payload 关键帧标志不一致这些分支。
func TestDecoderFeedMatchesDecodeFrames(t *testing.T) {
	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	const n = 130
	frames := make([]models.TelemetryFrame, 0, n)
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		vals := baseReadings(3.2, 10+float64(i%7)*0.1, 60+float64(i%20))
		f, err := enc.EncodeFrame(1, at, 0, readingsOf(vals, order3))
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: f.TS, Seq: f.Seq,
			FrameKind: f.FrameKind, SlotID: f.SlotID, Payload: f.Payload,
		})
	}
	// 分支覆盖：
	frames[55].FrameKind = models.TelemetryFrameKindKey // 列值说关键帧、payload 是增量 ⇒ MissingBaseline
	frames[100].Payload = []byte{0x01}                  // 截断坏帧 ⇒ 断链（到 120s 的关键帧恢复）
	frames[110].SlotID = 9999                           // 未知 slot

	cases := []struct {
		name  string
		opts  DecodeOptions
		chunk int
	}{
		{name: "全部字段_分页7", opts: DecodeOptions{}, chunk: 7},
		{name: "字段过滤_分页13", opts: DecodeOptions{Fields: []string{"rsoc", "current"}}, chunk: 13},
		{name: "全部字段_单批", opts: DecodeOptions{}, chunk: n},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantPoints, wantStats, err := DecodeFrames(frames, dict, tc.opts)
			if err != nil {
				t.Fatalf("DecodeFrames: %v", err)
			}
			dec, err := NewDecoder(dict, tc.opts)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			var gotPoints []Point
			for i := 0; i < len(frames); i += tc.chunk {
				end := i + tc.chunk
				if end > len(frames) {
					end = len(frames)
				}
				pts, _ := dec.Feed(frames[i:end])
				gotPoints = append(gotPoints, pts...)
			}
			if !reflect.DeepEqual(gotPoints, wantPoints) {
				t.Fatalf("分页 Feed 的点序列与 DecodeFrames 不一致（got %d 点, want %d 点）",
					len(gotPoints), len(wantPoints))
			}
			if got := dec.Stats(); !reflect.DeepEqual(got, wantStats) {
				t.Fatalf("累计 stats 与 DecodeFrames 不一致：\n got=%+v\nwant=%+v", got, wantStats)
			}
		})
	}
}

// TestQueryFramesPageKeysetNoLoss 钉住 (ts, seq) keyset 分页在「同一 ts 多帧」
// 的页边界上不丢帧、不重帧（seq 必须参与游标比较）。
func TestQueryFramesPageKeysetNoLoss(t *testing.T) {
	db := openTelemetryTestDB(t)
	store := NewStore(db, nil, nil)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

	type pageKey struct {
		off int64
		seq int16
	}
	var want []pageKey
	rows := make([]models.TelemetryFrame, 0, 20)
	for ti := 0; ti < 5; ti++ {
		at := base.Add(time.Duration(ti) * time.Second)
		for seq := int16(0); seq < 4; seq++ {
			rows = append(rows, models.TelemetryFrame{
				DeviceID: 7, TS: at, Seq: seq,
				FrameKind: models.TelemetryFrameKindDelta, SlotID: 1, Payload: []byte{0x00},
			})
			want = append(want, pageKey{off: int64(ti), seq: seq})
		}
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed frames: %v", err)
	}

	// 页大小 3：5 个 ts × 4 seq = 20 帧，必然切在同一 ts 的中间。
	var got []pageKey
	var cursor *frameCursor
	for {
		page, err := store.queryFramesPage(7, base, base.Add(5*time.Second), cursor, 3)
		if err != nil {
			t.Fatalf("queryFramesPage: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, fr := range page {
			got = append(got, pageKey{off: int64(fr.TS.Sub(base) / time.Second), seq: fr.Seq})
		}
		last := page[len(page)-1]
		cursor = &frameCursor{ts: last.TS, seq: last.Seq}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("分页结果与插入顺序不一致（丢帧/重帧/乱序）：\n got=%v\nwant=%v", got, want)
	}
}
