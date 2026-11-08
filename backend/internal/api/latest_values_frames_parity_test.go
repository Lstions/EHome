package api

// latest_values_frames_parity_test.go — 步骤 3 的两条专项验收（Lead 2026-11-07 要求）。
//
// ============ ① 过渡期对拍（比终态更重要）============
//
// 只在"停写之后"测是不够的 —— 过渡期（unified_data 还在写、帧表也在写）才是这一层
// **长期运行的真实状态**。必须证明：两趟补进缓存的值**逐设备逐字段相等**，
// 否则过渡期里「谁先跑」会决定用户看到什么，属于偶发不一致。
//
// ============ ② 解码开销必须量出来 ============
//
// WarmupLatestValuesFromFrames 每台设备解码一个 1 分钟窗口。若有人把窗口从 1min 改成
// 1h，10 台设备的启动开销会放大 60 倍而没人发现。本用例钉住耗时上限，
// 并把实测值打进日志。

import (
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// cacheSnapshot 取某设备缓存里 {字段: 值} 的快照（排序后便于逐项比较）。
func cacheSnapshot(dev uint) map[string]float64 {
	out := map[string]float64{}
	for _, r := range LatestValues(dev) {
		out[r.SensorName] = r.Value
	}
	return out
}

// TestLatestValues_UnifiedAndFramePathsAgree 已删除（2026-11-08）。
//
// 它是**过渡期对拍**：unified_data 预热路径 vs 帧表预热路径逐字段相等。
// unified_data 那条路径（LatestValuesWarmupRows + WarmupLatestValuesFrom）已删，
// 对拍没有第二条实现可比。
//
// 帧表那一趟的自洽保证仍在 TestLatestValuesFromFrames_CostIsBounded（下面的用例）
// 与 latest_values_frames_test.go 里。

// TestLatestValuesFromFrames_CostIsBounded 钉住解码开销。
//
// 数值依据：每台设备解码 1 分钟窗口。生产活跃设备 p50 帧间隔 1s => 约 60 帧/设备。
// 30 台设备约 1800 帧，实测远快于 1s（见日志）。上限取 2s 是给慢机器留的余量，
// 不是实测值 —— 真正的意义是「窗口被改大时会立刻红」。
func TestLatestValuesFromFrames_CostIsBounded(t *testing.T) {
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

	const devices = 10
	fields := []string{"current", "rsoc", "total_voltage", "grid_voltage"}
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	for i := 0; i < devices; i++ {
		seedFramesOnlyFields(t, db, uint(9990+i), start, start.Add(2*time.Minute), dict, fields, 5*time.Second)
	}

	began := time.Now()
	n := WarmupLatestValuesFromFrames(db, dict)
	elapsed := time.Since(began)
	t.Logf("%d 台设备 / 每台 1 分钟解码窗口：耗时 %v，补入 %d 行", devices, elapsed, n)
	if n == 0 {
		t.Fatal("没有补入任何行")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("耗时 %v 超过上限 2s —— 解码窗口或设备数被放大时会出现，先看 latestValuesFrameSlack", elapsed)
	}
}
