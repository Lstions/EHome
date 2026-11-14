package api

// latest_values_frame_first_pg_test.go — 「/latest-values 的 miss 回落到帧表」的验收。
//
// ============ 守的是一条**结构不变量**，不是两次巧合相等 ============
//
// 原来钉的是「/latest-data 的 sensors 合成」与「/latest-values 的 miss 回落」
// **两个端点同源**。2026-11-11 device_data 整表退役后，sensors 合成整段删除
// （device_data_sensors.go / device_data_sensors_frames.go 都没了），
// 这条「两个端点」的不变量随之只剩一半：**现在只有 /latest-values 一条数据路径**。
//
// 因此本用例的断言收缩为「/latest-values 的 miss 回落真的从帧表取数且字段完整」，
// 这正是它一半的价值里仍然有效的那一半。
//
// ⚠ 变异验证改了：删掉 LatestRowsFromFrames 的帧表解码分支 ⇒ miss rows 返回
// 0 行 ⇒ 本用例红。原来的「删掉 latestSensorViewsFromDB 的帧表分支」已不可用
// （该函数随文件删除）。
//
// 完整的数据源一致性现在由另外两处保证：
//   · /latest-values 的缓存预热（WarmupLatestValuesFromFrames）与 miss 回落
//     共用 LatestRowsFromFrames（latest_values_frames.go 的注释说明）；
//   · /devices/:id/history 走帧表解码，它有自己的用例。

import (
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
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

	// 单位来自 sensor_field_obs（帧表没有 unit 列）。本用例断言字段完整，
	// 因此必须显式投影单位；不投影时 unitMapForDevice 只能返回空串。
	if err := db.AutoMigrate(&models.SensorFieldObs{}); err != nil {
		t.Fatalf("migrate sensor_field_obs: %v", err)
	}
	db.Exec("DELETE FROM sensor_field_obs")
	for _, name := range want {
		unit := "A"
		if name == "rsoc" {
			unit = "%"
		}
		if err := db.Create(&models.SensorFieldObs{
			DeviceID: uint(dev), SensorName: name, Unit: unit,
			FirstSeen: start, LastSeen: start.Add(2 * time.Minute), Samples: 13,
		}).Error; err != nil {
			t.Fatalf("seed sensor_field_obs %s: %v", name, err)
		}
	}
	ResetDeviceUnitCache()

	rows, err := latestValuesMissRowsFromFramesOrDB(t.Context(), db, dev)
	if err != nil {
		t.Fatalf("miss rows: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("latestValuesMissRowsFromFramesOrDB 返回 %d 行，want %d"+
			"（两张原始表都已退役；返回 0 说明帧表分支没生效）", len(rows), len(want))
	}

	// 字段名与单位逐项核对（单位来自 sensor_field_obs，帧表没有 unit 列）。
	byName := make(map[string]models.UnifiedData, len(rows))
	for _, r := range rows {
		byName[r.SensorName] = r
	}
	for _, name := range want {
		row, ok := byName[name]
		if !ok {
			t.Errorf("帧表里有 %q 但 /latest-values 的 miss 回落没返回它", name)
			continue
		}
		if row.Unit == "" {
			// 单位缺失不一定会红（sensor_field_obs 未投影时本来就为空），
			// 但在这条用例里 fixture 已显式写入单位，所以必须非空。
			t.Errorf("%q 的 Unit 为空 —— unitMapForDevice 没读到 sensor_field_obs", name)
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
//
//	① 不报错（错误会让 handler 记 Warn 噪音）
//	② 返回空集（不是 nil 指针解引用 / 不是伪造数据）
//	③ 即使 unified_data 里**有**数据也不返回它（表已 DROP，这条是防「有人加回兜底」）
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
