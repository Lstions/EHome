package backfill

// realdata_roundtrip_test.go — 契约 §6.1 的**真实数据无损对拍**（本次验收的核心）。
//
// ============ 对拍口径 ============
//
// 用生产导出的 unified_data CSV（.tmp-telemetry/realdata/unified_data_202611.csv.gz，
// device 14/15，2026-11-01~11-06）跑完整条回填链路，然后**用生产同一个解码器**
// （telemetry.DecodeRange → DecodeFrames）把帧解回来，与原值逐值比对：
//
//	误差 ≤ Step/2   （量化理论误差上界，spec.go 的 MaxError）
//	缺失 = 0        （原表里有的字段，解码后必须都有）
//	超差 = 0        （battery_voltage 的 1e12 走 raw 列，不参与数值比对）
//
// ============ 为什么必须用真实数据 ============
//
// 合成数据（bmsFrame 那种）永远在 spec 量程内、字段集合固定、时间间隔均匀 ——
// 它测不出这三件事：
//
//	1. device15 的 7 种帧类型（字段集合各不相同，slot 派生是否真的分开）；
//	2. L1 之后的**稀疏帧**（一帧只有 3 个字段，增量链如何跨 slot 累积）；
//	3. 真实量化边界（cell_voltage 两位小数 vs spec 1mV）。
//
// 这三件恰恰是回填在真实数据上最容易出错的地方。
//
// 夹具缺席时 skip（CI 上没有那份 114 MB 的生产导出）。

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

const realdataRelPath = ".tmp-telemetry/realdata/unified_data_202611.csv.gz"

// realdataWindow 是验收窗口：2026-11-04 全天。
//
// 选这一天与方案 §1.1/§4.5 的全部实测数字同源（86,342 + 56,125 = 142,467 帧），
// 便于与既有报告逐项对账。
var (
	realFrom = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	realTo   = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
)

// locateRealdata 找到真实数据夹具；找不到返回 ""（调用方 skip）。
func locateRealdata(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("EHOME_TELEMETRY_REALDATA"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Fatalf("EHOME_TELEMETRY_REALDATA 指向不存在的文件 %q", p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, realdataRelPath)
		if _, serr := os.Stat(cand); serr == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if _, serr := os.Stat("/ws/" + realdataRelPath); serr == nil {
		return "/ws/" + realdataRelPath
	}
	return ""
}

// realFrame 是一帧真实数据。
type realFrame struct {
	DeviceID uint
	TS       time.Time
	Fields   map[string]float64
}

// loadRealFrames 从 CSV 载入某时间窗内的全部帧（按 (ts, device) 升序）。
//
// 分帧口径与方案 §1.1 一致：**同一设备同一 timestamp 的全部行 = 一帧**
// （写路径每帧用一个 time.Now() 写 N 行，时间戳逐位相同）。
func loadRealFrames(t *testing.T, path string, from, to time.Time) []realFrame {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open realdata: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer zr.Close()

	type acc struct {
		ts     time.Time
		device uint
		fields map[string]float64
	}
	byKey := make(map[string]*acc, 1<<18)

	layouts := []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07",
	}
	parseTS := func(raw string) (time.Time, error) {
		var lastErr error
		for _, layout := range layouts {
			if ts, perr := time.Parse(layout, raw); perr == nil {
				return ts.UTC(), nil
			} else {
				lastErr = perr
			}
		}
		return time.Time{}, lastErr
	}

	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if line == "" {
			continue
		}
		cols := strings.Split(line, ",")
		if len(cols) < 7 {
			t.Fatalf("line %d: 列数 %d < 7: %q", lineNo, len(cols), line)
		}
		dev, derr := strconv.ParseUint(strings.TrimSpace(cols[1]), 10, 64)
		if derr != nil {
			t.Fatalf("line %d: 坏 device_id %q: %v", lineNo, cols[1], derr)
		}
		ts, terr := parseTS(strings.TrimSpace(cols[5]))
		if terr != nil {
			t.Fatalf("line %d: 坏 timestamp %q: %v", lineNo, cols[5], terr)
		}
		if ts.Before(from) || !ts.Before(to) {
			continue
		}
		name := strings.TrimSpace(cols[2])
		val, verr := strconv.ParseFloat(strings.TrimSpace(cols[3]), 64)
		if verr != nil {
			t.Fatalf("line %d: 坏 value %q: %v", lineNo, cols[3], verr)
		}
		key := fmt.Sprintf("%d|%d", dev, ts.UnixNano())
		a := byKey[key]
		if a == nil {
			a = &acc{ts: ts, device: uint(dev), fields: make(map[string]float64, 32)}
			byKey[key] = a
		}
		a.fields[name] = val
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan realdata: %v", err)
	}

	out := make([]realFrame, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, realFrame{DeviceID: a.device, TS: a.ts, Fields: a.fields})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TS.Equal(out[j].TS) {
			return out[i].TS.Before(out[j].TS)
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

// TestRealDataBackfillRoundTrip 是契约 §6.1 的核心验收。
//
// 流程：真实 CSV → 装进 unified_data（模拟生产老数据）→ 回填 → 用生产解码器解回
// → 与原值逐值比对。
func TestRealDataBackfillRoundTrip(t *testing.T) {
	path := locateRealdata(t)
	if path == "" {
		t.Skip("真实数据夹具缺席（.tmp-telemetry/realdata/unified_data_202611.csv.gz）；" +
			"可用 EHOME_TELEMETRY_REALDATA 指定路径")
	}
	frames := loadRealFrames(t, path, realFrom, realTo)
	if len(frames) == 0 {
		t.Fatalf("验收窗口 %s~%s 内没有帧", realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339))
	}
	t.Logf("真实数据：%d 帧（%s ~ %s）", len(frames), realFrom.Format("2006-01-02"), realTo.Format("2006-01-02"))

	db := openRealdataDB(t)

	// 灌进 unified_data（一行一个字段，与生产老数据同形状）。
	var totalRows int64
	const seedBatch = 2000
	buf := make([]models.UnifiedData, 0, seedBatch)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := db.CreateInBatches(buf, seedBatch).Error; err != nil {
			t.Fatalf("seed unified_data: %v", err)
		}
		totalRows += int64(len(buf))
		buf = buf[:0]
	}
	for _, fr := range frames {
		names := make([]string, 0, len(fr.Fields))
		for n := range fr.Fields {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			buf = append(buf, models.UnifiedData{
				DeviceID:   fr.DeviceID,
				SensorName: n,
				Value:      fr.Fields[n],
				Timestamp:  fr.TS,
			})
			if len(buf) >= seedBatch {
				flush()
			}
		}
	}
	flush()
	t.Logf("已灌入 unified_data：%d 行（%d 帧）", totalRows, len(frames))

	// 回填（用落 DB 的 slot 字典 —— 与生产同一条 slot 派生路径）。
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up slots: %v", err)
	}
	if err := datalifecycle.EnsureBackfillProgressTable(db); err != nil {
		t.Fatalf("ensure progress table: %v", err)
	}
	r := NewRunner(db, dict, Config{BatchFrames: 5000}, false, nil)
	prog, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	if prog.State != StateDone {
		t.Fatalf("回填状态 = %q, want done（errors=%v）", prog.State, prog.Errors)
	}

	// ── 完整性：帧数必须等于真实帧数 ──
	var written int64
	db.Model(&models.TelemetryFrame{}).Count(&written)
	if written != int64(len(frames)) {
		t.Fatalf("telemetry_frame 帧数 = %d, want %d", written, len(frames))
	}
	// ── slot 规模（契约 §6.6：< 10,000 行）──
	var slotCount int64
	db.Model(&models.TelemetrySlot{}).Count(&slotCount)
	t.Logf("slot 规模：%d 行（契约要求 < 10,000）", slotCount)
	if slotCount >= 10000 {
		t.Errorf("slot 行数 = %d, 超出契约上界 10,000", slotCount)
	}

	// ── 无损对拍：用生产解码器解回来，逐值比对 ──
	verifyRoundTrip(t, db, dict, frames)
}

// verifyRoundTrip 解码每一帧并与原值逐值比对（误差 ≤ Step/2、缺失 0、超差 0）。
func verifyRoundTrip(t *testing.T, db *gorm.DB, dict telemetry.SlotDictionary, frames []realFrame) {
	t.Helper()
	byDevice := make(map[uint][]realFrame, 4)
	for _, fr := range frames {
		byDevice[fr.DeviceID] = append(byDevice[fr.DeviceID], fr)
	}
	devices := make([]uint, 0, len(byDevice))
	for d := range byDevice {
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i] < devices[j] })

	var checked, missing, overTol, clampedFields int64
	var worstErr float64
	var worstDesc string

	for _, dev := range devices {
		devFrames := byDevice[dev]
		var rows []models.TelemetryFrame
		if err := db.Where("device_id = ?", dev).Order("ts, seq").Find(&rows).Error; err != nil {
			t.Fatalf("读 device %d 的帧: %v", dev, err)
		}
		if len(rows) == 0 {
			t.Fatalf("device %d 没有回填出任何帧", dev)
		}
		points, stats, err := telemetry.DecodeFrames(rows, dict, telemetry.DecodeOptions{})
		if err != nil {
			t.Fatalf("解码 device %d: %v", dev, err)
		}
		t.Logf("device %d：帧=%d 解码点=%d 坏帧=%d 断链=%d 缺基线=%d",
			dev, len(rows), stats.Points, stats.FramesSkippedCorrupt,
			stats.FramesSkippedBrokenChain, stats.MissingBaseline)
		// 回填的数据不该有任何坏帧/断链/缺基线（网络噪声那些问题在回填场景不存在）。
		if stats.FramesSkippedCorrupt != 0 {
			t.Errorf("device %d：坏帧 %d 个（回填的数据不该有坏 payload）", dev, stats.FramesSkippedCorrupt)
		}
		if stats.FramesSkippedBrokenChain != 0 {
			t.Errorf("device %d：断链跳过 %d 帧（关键帧策略失效）", dev, stats.FramesSkippedBrokenChain)
		}
		if stats.MissingBaseline != 0 {
			t.Errorf("device %d：缺基线 %d 次（增量帧引用了不存在的基线）", dev, stats.MissingBaseline)
		}

		// 建立 (ts, seq) → 该帧解码值 的索引。
		type frameVals struct {
			values map[string]float64
		}
		decoded := make(map[string]*frameVals, len(rows))
		keyOf := func(ts time.Time, seq int16) string {
			return fmt.Sprintf("%d|%d", ts.UnixNano(), seq)
		}
		for _, p := range points {
			k := keyOf(p.TS, p.Seq)
			fv := decoded[k]
			if fv == nil {
				fv = &frameVals{values: make(map[string]float64, 32)}
				decoded[k] = fv
			}
			fv.values[p.Name] = p.Value
		}

		// 回填时每帧 seq 都是 0（一个 ts 一帧）；用 (ts,0) 对齐。
		for _, orig := range devFrames {
			fv := decoded[keyOf(orig.TS, 0)]
			if fv == nil {
				t.Fatalf("device %d ts=%s 的帧没有解码结果", dev, orig.TS.Format(time.RFC3339Nano))
			}
			for name, want := range orig.Fields {
				spec, _ := telemetry.SpecFor(name)
				// 超量程字段：编码时被 clamp，解码值必然偏离原值 —— 不算超差，
				// 它由 raw 列保原文（另一个用例专测）。这里只确认它**存在**。
				if _, wasClamped := spec.Quantize(want); wasClamped {
					clampedFields++
					if _, ok := fv.values[name]; !ok {
						t.Errorf("device %d ts=%s：超量程字段 %s 在解码结果里缺失", dev, orig.TS.Format(time.RFC3339Nano), name)
					}
					continue
				}
				got, ok := fv.values[name]
				if !ok {
					missing++
					if missing <= 5 {
						t.Errorf("device %d ts=%s：字段 %s 在解码结果里缺失（原值 %v）",
							dev, orig.TS.Format(time.RFC3339Nano), name, want)
					}
					continue
				}
				checked++
				diff := math.Abs(got - want)
				tol := spec.MaxError()
				// 浮点比较留一点余量（Dequantize 是一次乘加，误差在 1e-12 量级）。
				if diff > tol+1e-9 {
					overTol++
					if overTol <= 5 {
						t.Errorf("device %d ts=%s 字段 %s：原值=%v 解码=%v 误差=%v > Step/2=%v",
							dev, orig.TS.Format(time.RFC3339Nano), name, want, got, diff, tol)
					}
				}
				if diff > worstErr {
					worstErr = diff
					worstDesc = fmt.Sprintf("%s @ %s 原值=%v 解码=%v 误差=%v (Step/2=%v)",
						name, orig.TS.Format(time.RFC3339Nano), want, got, diff, tol)
				}
			}
		}
	}

	t.Logf("对拍结果：已比对 %d 个值 | 缺失 %d | 超差 %d | 超量程(走 raw) %d",
		checked, missing, overTol, clampedFields)
	t.Logf("最大误差：%v  (%s)", worstErr, worstDesc)
	if missing != 0 {
		t.Errorf("缺失 %d 个值（契约 §6.1 要求 0）", missing)
	}
	if overTol != 0 {
		t.Errorf("超差 %d 个值（契约 §6.1 允许 0）", overTol)
	}
}

// openRealdataDB 建库（真实数据量大，用文件级 SQLite 提高写入吞吐）。
func openRealdataDB(t *testing.T) *gorm.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "backfill_realdata.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite %s: %v", dsn, err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&models.UnifiedData{},
		&models.TelemetryFrame{},
		&models.TelemetrySlot{},
		&datalifecycle.BackfillProgressRow{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}
