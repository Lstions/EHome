package api

// history_frame_budget_test.go — 内存护栏的验收 + 堆峰值实测（P0，2026-11-08）。
//
// 判据是**行数**（帧数 x 字段数），不是帧数 —— 见 history_frame_budget.go 的推导。

import (
	"context"
	"runtime"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestHistoryFrameBudget_BlocksOversizedWindow 守「超行数上限 => 拒绝」。
func TestHistoryFrameBudget_BlocksOversizedWindow(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	// 关键：让 sensor_field_obs 里没有该设备 => 字段数回落到最坏情况 100
	db.Exec("DELETE FROM sensor_field_obs")

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 11
	names := []string{"current", "rsoc"}
	// 字段数未知 => 按 100 算 => 帧数上限 = 3,000,000 / 100 = 30,000
	frameCeiling := maxHistoryRowsPerRequest / defaultSensorCountForBudget
	start := time.Now().UTC().Add(-48 * time.Hour)
	end := start.Add(time.Duration(frameCeiling+1) * time.Second)
	seedNthFrames(t, db, dev, start, time.Second, int(frameCeiling)+1, dict, names)

	verdict := checkHistoryFrameBudget(context.Background(), db, dev, "", start, end)
	if !verdict.Exceeded {
		t.Fatalf("造了 %d 帧（上限 %d），护栏未触发", frameCeiling+1, frameCeiling)
	}
	t.Logf("帧数上限 %d（行上限 %d / 字段 %d），造了 %d 帧 => 拒绝",
		frameCeiling, maxHistoryRowsPerRequest, verdict.Sensors, frameCeiling+1)
}

// TestHistoryFrameBudget_Allows24hOn29FieldDevice 守「24h 在 29 字段设备上必须放行」。
//
// 这条是演练 #3 的前置：24h 是唯一能验证「4 段分段解码」的窗口，
// 阈值把它闸掉就等于把核心覆盖一起闸掉。
func TestHistoryFrameBudget_Allows24hOn29FieldDevice(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM sensor_field_obs")

	const dev = 13
	// 29 个字段写进维度表 => 字段数 29 => 帧数上限 = 3,000,000/29 = 103,448
	for i := 0; i < 29; i++ {
		if err := db.Exec(
			"INSERT INTO sensor_field_obs (device_id, sensor_name, unit, first_seen, last_seen, samples, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, 1, ?)",
			dev, "s"+string(rune(0x61+i)), "u", time.Now().UTC(), time.Now().UTC(), time.Now().UTC()).Error; err != nil {
			t.Skipf("sensor_field_obs 不可用（%v）", err)
		}
	}
	defer db.Exec("DELETE FROM sensor_field_obs WHERE device_id = ?", dev)

	// 24h 在 device14 是 84,866 帧 < 103,448 => 应放行。这里只造 1 帧验证判据本身。
	start := time.Now().UTC().Add(-24 * time.Hour)
	end := time.Now().UTC()
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)
	seedNthFrames(t, db, dev, end.Add(-time.Minute), time.Second, 1, dict, []string{"sa"})

	v := checkHistoryFrameBudget(context.Background(), db, dev, "", start, end)
	if v.Sensors != 29 {
		t.Fatalf("字段数 = %d, want 29（应从 sensor_field_obs 数出来）", v.Sensors)
	}
	if v.Exceeded {
		t.Fatalf("29 字段设备上只造了 1 帧却被拒 —— 24h 会被一刀闸掉，演练 #3 无法验证")
	}
	t.Logf("29 字段 => 帧数上限 %d；1 帧放行（device14 的 24h 是 84,866 帧，也在限内）",
		maxHistoryRowsPerRequest/29)
}

// TestHistoryFrameHeapPerRow 量「每行物化的堆开销」，据此推算生产窗口的峰值。
func TestHistoryFrameHeapPerRow(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
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

	const dev = 12
	names := []string{
		"current", "rsoc", "total_voltage", "grid_voltage", "output_active_power",
		"soc", "soh", "cell_voltage_max", "cell_voltage_min", "temp_max",
	}
	const frames = 2000
	start := time.Now().UTC().Add(-48 * time.Hour)
	seedNthFrames(t, db, dev, start, time.Second, frames, dict, names)
	end := start.Add(time.Duration(frames) * time.Second)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rows, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
	if !ok {
		t.Fatal("帧路径无数据")
	}
	runtime.ReadMemStats(&after)

	rowsN := len(rows)
	bytes := int64(after.TotalAlloc - before.TotalAlloc)
	perRow := float64(bytes) / float64(rowsN)
	// 行数 = 帧数 x 字段数。**不能**拿每行开销直接乘帧数 —— 那会低估 len(names) 倍。
	const prodFields = 29
	rows24h := float64(84866) * prodFields
	rowsCap := float64(maxHistoryRowsPerRequest)
	t.Logf("物化 %d 帧 x %d 字段 = %d 行；分配 %.1f MB；按 %.0f B/行 推算：",
		frames, len(names), rowsN, float64(bytes)/(1024*1024), perRow)
	t.Logf("   24h（84,866 帧 x 29 = %.0f 万行）=> 约 %.0f MB", rows24h/1e4, perRow*rows24h/(1024*1024))
	t.Logf("   行上限 %d => 约 %.0f MB（TotalAlloc 口径；live heap 按 180 B/行 约 %.0f MB）",
		maxHistoryRowsPerRequest, perRow*rowsCap/(1024*1024), rowsCap*180/(1024*1024))
}

// seedNthFrames 造 n 帧（每 step 一帧），每帧 fields 个字段。
func seedNthFrames(t *testing.T, db *gorm.DB, dev uint, start time.Time, step time.Duration, n int,
	dict telemetry.SlotDictionary, fields []string) {
	t.Helper()
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	batch := make([]models.TelemetryFrame, 0, 500)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := db.Session(&gorm.Session{}).CreateInBatches(batch, 500).Error; err != nil {
			t.Fatalf("persist frames: %v", err)
		}
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * step)
		readings := make([]telemetry.Reading, 0, len(fields))
		for j, f := range fields {
			readings = append(readings, telemetry.Reading{Name: f, Value: float64((i*len(fields) + j) % 100)})
		}
		fr, err := enc.EncodeFrame(dev, ts.UTC(), 0, readings)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		batch = append(batch, models.TelemetryFrame{
			DeviceID: fr.DeviceID, TS: fr.TS, Seq: fr.Seq,
			FrameKind: fr.FrameKind, SlotID: fr.SlotID, Payload: fr.Payload,
		})
		if len(batch) >= 500 {
			flush()
		}
	}
	flush()
}
