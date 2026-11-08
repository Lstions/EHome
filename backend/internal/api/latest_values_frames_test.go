package api

// latest_values_frames_test.go — 帧表数据源的验收（task-24 步骤 3）。
//
// 守的核心不变量（Lead 2026-11-07 提前点出的风险）：
//
//	**停写 unified_data 之后，启动预热不能静默失效。**
//
// 失效链条（不报任何错）：
//	WarmupLatestValues -> 源返回 0 行 -> 缓存空 -> 服务正常启动
//	-> 首次请求 Miss 回落 SQL -> SQL 读的也是空表 -> 接口静默返回空
//
// 所以本用例直接模拟终态：**unified_data 一行都没有**，只有帧表。
// 断言：帧表补全后，缓存里每台设备都有完整的物理量集合。

import (
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// TestLatestValuesFromFrames_SurvivesUnifiedRetirement 是步骤 3 的主验收。
func TestLatestValuesFromFrames_SurvivesUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data") // 终态：unified_data 空了
	resetLatestValueCacheForTest()
	ResetDeviceUnitCache()
	defer func() {
		resetLatestValueCacheForTest()
		ResetDeviceUnitCache()
	}()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}

	const dev = 9971
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	seedFramesOnlyFields(t, db, dev, start, start.Add(5*time.Minute), dict,
		[]string{"current", "rsoc", "total_voltage"}, 5*time.Second)

	// 前置断言：缓存此时是空的（否则下面的 n>0 可能来自旧数据，而不是帧表补全）
	//
	// 原实现这里调 WarmupLatestValues() 并断言返回 0 —— 那个函数（unified_data 预热）
	// 已在 2026-11-08 随 DROP 删除，所以「从 unified 预热返回 0」这个前置不再需要：
	// 现在**没有** unified 预热路径了，缓存空是 resetLatestValueCacheForTest() 的直接结果。
	if len(LatestValues(dev)) != 0 {
		t.Fatal("缓存应仍为空")
	}

	// 帧表补全
	n := WarmupLatestValuesFromFrames(db, dict)
	if n == 0 {
		t.Fatal("帧表补全返回 0 行 —— **停写后启动预热会静默失效**，这正是本用例要防的")
	}
	got := LatestValues(dev)
	if len(got) != 3 {
		names := make([]string, 0, len(got))
		for _, r := range got {
			names = append(names, r.SensorName)
		}
		t.Fatalf("缓存里 %d 个物理量, want 3（%v）", len(got), names)
	}
	for _, r := range got {
		if r.DeviceID != dev {
			t.Fatalf("设备号错乱: %d", r.DeviceID)
		}
	}
}

// TestLatestValuesFromFrames_SkipsDevicesAlreadyCached 守 skipCached 语义。
//
// 为什么需要：运行时缓存里是**比帧表更新鲜**的值（帧表是落库的，缓存是内存里的最新）。
// 预热补全不能把它们覆盖回去，否则并发写入的值会回退。
func TestLatestValuesFromFrames_SkipsDevicesAlreadyCached(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	resetLatestValueCacheForTest()
	ResetDeviceUnitCache()
	defer func() {
		resetLatestValueCacheForTest()
		ResetDeviceUnitCache()
	}()

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}

	const dev = 9972
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	seedFramesOnlyFields(t, db, dev, start, start.Add(5*time.Minute), dict, []string{"current"}, 5*time.Second)

	// 先塞一条"运行期"的新鲜值
	SetLatestValue(models.UnifiedData{DeviceID: dev, SensorName: "current", Value: 999, Unit: "A", Timestamp: time.Now().UTC()})

	rows, err := LatestValuesRowsFromFrames(db, dict, true)
	if err != nil {
		t.Fatalf("rows from frames: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("设备已在缓存中却仍从帧表补了 %d 行 —— 会覆盖运行期更新的值", len(rows))
	}
	if v, ok := LatestValueBySensor(dev, "current"); !ok || v.Value != 999 {
		t.Fatalf("运行期值被覆盖: %v %v", v.Value, ok)
	}
}
