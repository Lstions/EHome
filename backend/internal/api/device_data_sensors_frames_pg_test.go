package api

// device_data_sensors_frames_pg_test.go — 帧表合成 sensors 的验收（P0 回归修复，2026-11-07）。
//
// 守四件事：
//  1. **回归已修**：unified_data 没有该时段的行时，历史行仍然有 sensors；
//  2. **对拍相等**：帧表路径与 unified_data 路径产出逐字段相同；
//  3. **回落有效**：帧表不可用时走回原逻辑且仍然正确；
//  4. **性能**：50 行/页 < 50 ms（改造前是每行一次 SQL / 或一条 651 ms 的基线查询）。

import (
	"encoding/json"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

var frameSensorFields = []string{"current", "total_voltage", "rsoc"}

// seedListRows 造 n 条**不带 sensors** 的 device_data 行（task-19 之后的线上形状）。
func seedListRows(t *testing.T, db *gorm.DB, deviceID uint, start time.Time, n int, step time.Duration) []models.DeviceData {
	t.Helper()
	rows := make([]models.DeviceData, 0, n)
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * step)
		raw, err := json.Marshal(map[string]any{
			"raw_hex":    "AABB",
			"channel_id": 1,
			"timestamp":  ts.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rows = append(rows, models.DeviceData{
			DeviceID:  deviceID,
			NodeID:    "n1",
			DataJSON:  string(raw),
			Timestamp: ts,
		})
	}
	return rows
}

// sensorsOf 解析一行 data_json 里的 sensors 数组。
func sensorsOf(t *testing.T, raw string) []sensorView {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	rawSensors, ok := obj["sensors"]
	if !ok {
		return nil
	}
	var views []sensorView
	if err := json.Unmarshal(rawSensors, &views); err != nil {
		t.Fatalf("unmarshal sensors: %v", err)
	}
	return views
}

// TestDeviceDataSensors_FromFramesWhenUnifiedRetired 是**回归主用例**。
//
// 造「unified_data 里没有这段历史」（等价于 10 月分区被删），断言：
//   - 每一行都有 sensors（不是 0 项）；
//   - 字段集合与帧表一致；
//   - 50 行页耗时 < 50 ms。
func TestDeviceDataSensors_FromFramesWhenUnifiedRetired(t *testing.T) {
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

	const dev = 9941
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(10 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, frameSensorFields, 5*time.Second)

	// **关键**：故意不写 unified_data —— 模拟 10 月分区已退役。
	rows := seedListRows(t, db, dev, start.Add(time.Minute), 50, 5*time.Second)

	began := time.Now()
	deviceDataJSONListWithSensors(db, dev, rows)
	elapsed := time.Since(began)

	for i := range rows {
		views := sensorsOf(t, rows[i].DataJSON)
		if len(views) == 0 {
			t.Fatalf("第 %d 行 sensors = 0 —— 这正是要修的回归（unified_data 退役后历史行丢 sensors）", i)
		}
		if len(views) != len(frameSensorFields) {
			t.Fatalf("第 %d 行 sensors = %d 项, want %d", i, len(views), len(frameSensorFields))
		}
		for _, want := range frameSensorFields {
			found := false
			for _, v := range views {
				if v.Name == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("第 %d 行缺字段 %q", i, want)
			}
		}
	}
	t.Logf("50 行页耗时 = %v", elapsed)
	if elapsed > 50*time.Millisecond {
		t.Errorf("50 行页耗时 %v > 50ms（目标见 Lead 2026-11-07 要求）", elapsed)
	}
}

// TestDeviceDataSensors_FramesMatchUnifiedPath 是**对拍**：两条路径逐字段相等。
// TestDeviceDataSensors_FromFramesProducesExpectedFields 锁帧表合成的产出内容。
//
// ============ 这条用例的前身与它为什么必须改（2026-11-08）============
//
// 原名 TestDeviceDataSensors_FramesMatchUnifiedPath，做的是**对拍**：
// 帧表路径 vs unified_data 回落路径，逐字段比 Name/Unit。
//
// 硬切换删掉 unified_data 回落后，那个「基准」不存在了 —— 删掉帧表之后，
// 回落侧现在返回 0 项（本用例正是这样变红的：「帧表 3 项 vs unified 0 项」）。
//
// **这和对拍被删是同一件事的两面**：删回落既删掉了生产路径，也删掉了这条 Oracle。
// 所以本用例改成「断言帧表路径产出这些字段」，而不是「与另一条路径相等」。
//
// ⚠ 代价如实记录：以后**没有第二条独立实现**能发现帧表解码的偏差了。
// 新的独立基准是 rollup 路径（TestRollupMatchesUnifiedForLongWindows 仍在用
// queryHistoryPoints 做 Oracle），但那验证的是聚合口径，不是列表 sensors 的形状。
func TestDeviceDataSensors_FromFramesProducesExpectedFields(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9942
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	end := start.Add(10 * time.Minute)
	seedFramesOnlyFields(t, db, dev, start, end, dict, frameSensorFields, 5*time.Second)

	// unified_data：同样的字段（值取整到量化网格之外也无所谓，这里只比 Name/Value 一致性与单位）。
	var ud []models.UnifiedData
	for i := 0; i < 120; i++ {
		ts := start.Add(time.Duration(i) * 5 * time.Second)
		for fi, name := range frameSensorFields {
			ud = append(ud, models.UnifiedData{
				DeviceID: dev, SensorName: name, Value: float64(10 + fi),
				Unit: []string{"A", "V", "%"}[fi], Timestamp: ts,
			})
		}
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(ud, 200).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}

	base := seedListRows(t, db, dev, start.Add(time.Minute), 10, 30*time.Second)

	rows := append([]models.DeviceData(nil), base...)
	deviceDataJSONListWithSensors(db, dev, rows)

	for i := range rows {
		views := sensorsOf(t, rows[i].DataJSON)
		if len(views) != len(frameSensorFields) {
			t.Fatalf("第 %d 行 sensors = %d 项, want %d: %s",
				i, len(views), len(frameSensorFields), rows[i].DataJSON)
		}
		names := make([]string, 0, len(views))
		for _, v := range views {
			names = append(names, v.Name)
		}
		for _, want := range frameSensorFields {
			found := false
			for _, got := range names {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("第 %d 行缺字段 %q（实际 %v）", i, want, names)
			}
		}
	}
}

// TestDeviceDataSensors_NoFallbackWhenFramesUnavailable 锁「帧表不可用 => sensors 不出现」。
//
// 原名 TestDeviceDataSensors_FallsBackWhenFramesUnavailable，断言「帧表不可用时回落
// unified_data，每行仍有 sensors」。2026-11-08 硬切换删掉那条回落后，**被测语义整体反转**：
// 现在必须断言「没有回落」。
//
// 这条用例的价值恰在于它**变红过**：删回落之后它立刻红了，证明它真的在测那条路径。
// 如果当初写成「sensors 非空即可」的弱断言，它会一直是绿的，而我以为覆盖还在。
func TestDeviceDataSensors_NoFallbackWhenFramesUnavailable(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Skipf("帧表不可用（%v），跳过", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM unified_data")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9943
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	// 只有 unified_data、没有帧：旧实现会回落到它，新实现必须完全无视。
	var ud []models.UnifiedData
	for i := 0; i < 40; i++ {
		ts := start.Add(time.Duration(i) * 5 * time.Second)
		ud = append(ud, models.UnifiedData{
			DeviceID: dev, SensorName: "current", Value: float64(i), Unit: "A", Timestamp: ts,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(ud, 100).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}

	rows := seedListRows(t, db, dev, start.Add(30*time.Second), 5, 5*time.Second)
	deviceDataJSONListWithSensors(db, dev, rows)
	for i := range rows {
		if views := sensorsOf(t, rows[i].DataJSON); len(views) != 0 {
			t.Fatalf("第 %d 行仍有 %d 个 sensors —— unified_data 回落还在（硬切换没生效）：%s",
				i, len(views), rows[i].DataJSON)
		}
	}
}

// TestDeviceDataSensors_LongPageSpanStillUsesFrames 守「页内跨度超 24h」的潜伏 bug。
//
// ============ 这个用例钉的是什么 ============
//
// 原实现用 DecodeRangeAnchored，它内部套生产护栏 MaxDecodeWindow = 24h。
// 而本路径的解码窗口 = 「页内跨度 + 10 分钟 lookback」，由 page_size 与设备速率决定：
//
//	页内跨度 > 23.83h ⇒ RangeTooLargeError ⇒ ok=false ⇒ 回落 ⇒ 10 月行仍丢 sensors
//
// 生产 device14/15 太快，看不出问题；一台 5 分钟一报的温湿度传感器，200 行就跨 16 小时、
// 4.2 行/小时就跨 47 小时 —— **数学必然踩到**。
//
// 改用 DecodeRangeAnchoredBounded（放宽 maxWindow，maxFrames 仍用生产值）后，本用例必须绿。
//
// **变异验证**：把它改回 DecodeRangeAnchored，本用例必须红。
func TestDeviceDataSensors_LongPageSpanStillUsesFrames(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data") // 关键：没有任何回落来源
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9944
	// 40 行 x 45 分钟 = 30 小时跨度（> 24h 护栏）。帧间隔 45min ⇒ 每帧都是关键帧
	// （encoder 的 KeyFrameInterval = 60s），所以稀疏数据也能解出全字段。
	start := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	span := 30 * time.Hour
	step := 45 * time.Minute
	seedFramesOnlyFields(t, db, dev, start, start.Add(span+time.Hour), dict, frameSensorFields, step)

	rows := seedListRows(t, db, dev, start, 40, step)

	deviceDataJSONListWithSensors(db, dev, rows)
	for i := range rows {
		views := sensorsOf(t, rows[i].DataJSON)
		if len(views) == 0 {
			t.Fatalf("第 %d 行（跨 %.1fh 的页）sensors 为空 —— 解码窗口超了 24h 护栏而回落，"+
				"且没有 unified_data 可回 ⇒ 历史行仍丢 sensors（慢设备必踩）", i, span.Hours())
		}
		if len(views) != len(frameSensorFields) {
			t.Fatalf("第 %d 行 sensors = %d 项, want %d", i, len(views), len(frameSensorFields))
		}
	}
}

// TestDeviceDataSensors_UnitsSurviveUnifiedRetirement 守「单位不随 unified_data 退役」。
//
// ============ 这条钉的是什么 ============
//
// telemetry_frame **没有 Unit 列**，所以帧表合成出的 sensors 单位只能从别处补。
// 第一版我从 unified_data 取 —— 那又犯了同一个错：**借了正在退役的表**。
//
// 正解是维度表 sensor_field_obs（boot-api 2026-11-07 指出）：
// 它的数据通路是 field_resolver 的 projector，不经过 unified_data，且 upsert 带
// 「空串不覆盖已存单位」的保护。
//
// 本用例：unified_data 为空（模拟全退役）+ sensor_field_obs 有单位
// ⇒ 合成结果必须**带单位**。
//
// 变异验证：把 unitMapForDevice 里读 sensor_field_obs 的那段删掉，本用例必须红。
func TestDeviceDataSensors_UnitsSurviveUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data") // 全退役：没有任何 unified_data 可借
	db.Exec("DELETE FROM sensor_field_obs")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	defer SetHistoryFrameDictionary(nil)

	const dev = 9945
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	seedFramesOnlyFields(t, db, dev, start, start.Add(10*time.Minute), dict, frameSensorFields, 5*time.Second)

	// 维度表：单位是静态属性，一次观测到就长期有效。
	wantUnits := map[string]string{"current": "A", "total_voltage": "V", "rsoc": "%"}
	for name, unit := range wantUnits {
		if err := db.Exec(
			"INSERT INTO sensor_field_obs (device_id, sensor_name, unit, first_seen, last_seen, samples, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, 1, ?)",
			dev, name, unit, start, start, start).Error; err != nil {
			t.Fatalf("seed sensor_field_obs(%s): %v", name, err)
		}
	}
	defer db.Exec("DELETE FROM sensor_field_obs WHERE device_id = ?", dev)

	rows := seedListRows(t, db, dev, start.Add(time.Minute), 5, 5*time.Second)
	deviceDataJSONListWithSensors(db, dev, rows)
	for i := range rows {
		views := sensorsOf(t, rows[i].DataJSON)
		if len(views) == 0 {
			t.Fatalf("第 %d 行 sensors 为空", i)
		}
		for _, v := range views {
			if want := wantUnits[v.Name]; v.Unit != want {
				t.Fatalf("第 %d 行 %q 的 Unit = %q, want %q —— "+
					"单位必须来自维度表 sensor_field_obs，不能依赖即将退役的 unified_data",
					i, v.Name, v.Unit, want)
			}
		}
	}
}
