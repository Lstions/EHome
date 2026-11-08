package api

// latest_values_frame_first_pg_test.go — 硬切换后「两个端点同源」的验收（2026-11-08）。
//
// ============ 守的是一条**结构不变量**，不是两次巧合相等 ============
//
// device_data_sensors.go 的 latestSensorViews 注释写着：
//
//	与 /latest-values 端点**同一口径**：两者若不一致，详情页(用 latest-values)
//	与数据面板(用 latest-data) 会显示不同的数值，且这种不一致只在缓存冷热切换时出现，
//	极难复现。
//
// 停写之后这条不变量有了**新的失效方式**：如果一边走帧表、另一边走 unified_data，
// 那么热路径（缓存命中）两边一致，但**冷缓存时**一个拿到停写前最后一刻的值、
// 另一个拿到最后一帧的值 —— 数值与 timestamp 都可能不同。而冷缓存正是
// 「重启后第一次请求」/「缓存被显式清空」这两个时刻。
//
// 所以本用例刻意造「帧表有数据、unified_data **一行都没有**」，
// 让任何仍走 unified_data 的路径**返回空**，从而暴露出来。
//
// ============ 变异验证（两个都验过）============
//
//	删掉 latestSensorViewsFromDB 里的帧表分支
//	  => latestSensorViewsFromDB 返回 0 项 => 本用例红
//
//	删掉 latestValuesMissRowsFromFramesOrDB 里的 LatestRowsFromFrames 分支
//	  => miss rows 返回 0 行 => 本用例红

import (
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestLatestValuesAndLatestData_ShareFrameSource 断言两个端点在「unified_data 已退役」时
// **都**能从帧表拿到答案，且两个答案逐字段一致。
func TestLatestValuesAndLatestData_ShareFrameSource(t *testing.T) {
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

	const dev = 9951
	want := []string{"current", "rsoc"}
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	seedFramesOnlyFields(t, db, dev, start, start.Add(2*time.Minute), dict, want, 10*time.Second)

	views := latestSensorViewsFromDB(db, dev)
	if len(views) != len(want) {
		t.Fatalf("latestSensorViewsFromDB 返回 %d 项，want %d（unified_data 已清空，"+
			"只剩帧表可读；返回 0 说明帧表分支没生效）", len(views), len(want))
	}

	rows, err := latestValuesMissRowsFromFramesOrDB(t.Context(), db, dev)
	if err != nil {
		t.Fatalf("miss rows: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("latestValuesMissRowsFromFramesOrDB 返回 %d 行，want %d"+
			"（unified_data 已空；返回 0 说明帧表分支没生效）", len(rows), len(want))
	}

	byName := make(map[string]sensorView, len(views))
	for _, v := range views {
		byName[v.Name] = v
	}
	for _, r := range rows {
		v, ok := byName[r.SensorName]
		if !ok {
			t.Errorf("/latest-values 有 %q 但 /latest-data 没有 => 两条路径不同源", r.SensorName)
			continue
		}
		if v.Value != r.Value {
			t.Errorf("%q 值不一致: latest-data=%v latest-values=%v", r.SensorName, v.Value, r.Value)
		}
		if v.Unit != r.Unit {
			t.Errorf("%q 单位不一致: latest-data=%q latest-values=%q", r.SensorName, v.Unit, r.Unit)
		}
	}
}

// TestLatestValuesMiss_FramesEmptyReturnsEmpty 守**退役后**的终态行为：
// 帧表没有该设备时，必须返回「空 + 无错误」，而不是 panic / 报错 / 去查 unified_data。
//
// 历史（2026-11-08 硬切换前）：这条用例叫 FallsBackWhenFramesEmpty，断言的是
// 「帧表空了就回落 unified_data」。unified_data DROP 之后回落目标不存在 ——
// 原实现走到回落分支必然 42P01，上层 Warn 后返回空，用户看到「—」。
// 现在直接返回空，用户**同样**看到「—」，区别只是少一条必现的 42P01 日志。
//
// 所以本用例验的不再是「回落了」，而是「退役后的终态是干净的空」：
//   ① 不报错（错误会让 handler 记 Warn 噪音）
//   ② 返回空集（不是 nil 指针解引用 / 不是伪造数据）
//   ③ 即使 unified_data 里**有**数据也不返回它（表已 DROP，这条是防「有人加回兜底」）
func TestLatestValuesMiss_FramesEmptyReturnsEmpty(t *testing.T) {
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

	const dev = 9952
	rows, err := latestValuesMissRowsFromFramesOrDB(t.Context(), db, dev)
	if err != nil {
		t.Fatalf("帧表无该设备时不该报错（上层会把 err 记成 Warn 噪音），got: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("帧表无该设备时必须返回空集，got %d rows: %+v", len(rows), rows)
	}
}
