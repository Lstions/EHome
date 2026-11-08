package api

// device_history_frames_test.go — task-26 帧表原始历史读路径的验收。
//
// 守四件事：
//  ① unified_data **空**时仍能返回数据（演练场景：6 分区全 DETACH）；
//  ② 形状与契约一致：返回 models.UnifiedData，键齐全，id/sensor_name/value/timestamp 有值；
//  ③ id **跨调用稳定**（同一 (device,sensor,ts) 两次调用必须相同，否则前端列表 key 抖动）；
//  ④ 行序按 timestamp 升序、同刻按 sensor_name —— 跨请求稳定。

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestFrameHistoryRows_ServesWhenUnifiedIsEmpty 是 task-26 的主验收。
func TestFrameHistoryRows_ServesWhenUnifiedIsEmpty(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data") // 演练场景：unified_data 全 DETACH
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9971
	fields := []string{"current", "rsoc"}
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(2 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, fields, 10*time.Second)

	rows, ok := frameHistoryRows(context.Background(), db, dev, "", start, end)
	if !ok {
		t.Fatal("帧表有数据却 ok=false —— 演练场景下这个接口仍会返回 0 行")
	}
	if len(rows) == 0 {
		t.Fatal("帧表路径返回 0 行")
	}
	// 12 帧 x 2 字段 = 24 行（10s 间隔，2 分钟窗口）
	if len(rows) != 24 {
		t.Fatalf("rows = %d, want 24（12 帧 x 2 字段）", len(rows))
	}
	seen := map[string]bool{}
	for i := range rows {
		r := rows[i]
		if r.ID == 0 {
			t.Fatalf("第 %d 行 id = 0 —— 前端拿 id 当 key 会全冲突", i)
		}
		if r.DeviceID != dev || r.SensorName == "" {
			t.Fatalf("第 %d 行键不全: %+v", i, r)
		}
		if r.CreatedAt.IsZero() || !r.CreatedAt.Equal(r.Timestamp) {
			t.Fatalf("第 %d 行 created_at 必须等于 timestamp（帧没有入库时间，用采样时间是最诚实的近似）", i)
		}
		seen[r.SensorName] = true
	}
	for _, f := range fields {
		if !seen[f] {
			t.Fatalf("缺字段 %q —— 多字段历史必须一次返回全部字段", f)
		}
	}
	// 行序：timestamp 升序
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp.Before(rows[i-1].Timestamp) {
			t.Fatalf("第 %d 行时间倒序", i)
		}
	}
}

// TestFrameHistoryRows_IDIsStableAcrossCalls 守 id 跨调用稳定。
func TestFrameHistoryRows_IDIsStableAcrossCalls(t *testing.T) {
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

	const dev = 9972
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	seedFramesOnlyFields(t, db, dev, start, start.Add(time.Minute), dict, []string{"current"}, 10*time.Second)

	first, ok := frameHistoryRows(context.Background(), db, dev, "", start, start.Add(time.Minute))
	if !ok {
		t.Fatal("ok=false")
	}
	second, _ := frameHistoryRows(context.Background(), db, dev, "", start, start.Add(time.Minute))
	if len(first) != len(second) {
		t.Fatalf("两次行数不同 %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("第 %d 行 id 跨调用不稳定: %d vs %d —— 前端列表 key 会抖动",
				i, first[i].ID, second[i].ID)
		}
	}
}

// TestFrameHistoryRows_NoFramesFallsBack 守「该窗口一帧都没有 ⇒ 交回裸 SQL」。
//
// ⚠ 这条用例原来叫 TestFrameHistoryRows_RejectsLongWindow，意思是「超 6h 就回落」。
// 引入分段解码后那个含义**已经不存在**了（168h = 28 段 ≤ maxFrameHistorySegments ⇒ 正常处理）。
// 它当时仍然绿，但绿的原因变成了「这个设备没有帧」—— **名字与行为不符的假绿**。
// 改名并把它该验的东西验准。
func TestFrameHistoryRows_NoFramesFallsBack(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	end := time.Now().UTC()
	start := end.Add(-168 * time.Hour) // 7 天 = 28 段，仍在段数上限内
	if _, ok := frameHistoryRows(context.Background(), db, 9973, "", start, end); ok {
		t.Fatal("该设备一帧都没有却 ok=true —— 会让接口返回 0 行却不回落裸 SQL")
	}
}
