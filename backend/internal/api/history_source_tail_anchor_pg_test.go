package api

// history_source_tail_anchor_pg_test.go — probeDeviceLastDataAt 的 **DB 集成**验证。
//
// ============ 本文件现在只剩一件事（2026-11-08）============
//
// 原来这里有三条：一条验 probeDeviceLastDataAt 真的把列读出来，两条验
// probeFrameCoverage 在真 PG 上的 count/min/max + 尾部判据。
//
// 后两条已随覆盖判据整体删除（见 history_source.go 的墓碑）。
// 🌱 **第一条必须留**：probeDeviceLastDataAt 没删 —— 它现在服务 rollup 读路径的
//    尾巴空帧诊断（rollup_read.go 的 tailEmptyLooksLikeLag）。
//
// ============ 为什么当时要补这条（历史动机，仍然有效）============
//
// 纯函数用例直接传 lastDataAt，**证明了算法逻辑**，但没有证明
// 「真的把 edge_devices.last_data_at 读出来了」。
//
// 实测确认过这个缺口是真的：ehome_test 里 edge_devices 有 last_data_at 列，
// 但 14 条设备行 **0 条**有值 ⇒ 全部走「未知 ⇒ 返回零值」分支 ⇒
// 那段逻辑在集成层**一次都没被执行**，用例绿得毫无意义。
//
// 本文件补上这条：造真实的设备行（带 last_data_at），走完整查询。
import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedEdgeDeviceWithLastData 造一条带 last_data_at 的设备行（测试专用 ID，跑完删）。
func seedEdgeDeviceWithLastData(t *testing.T, db *gorm.DB, id uint, lastDataAt time.Time) {
	t.Helper()
	db.Exec("DELETE FROM edge_devices WHERE id = ?", id)
	if err := db.Exec(
		"INSERT INTO edge_devices (id, type, name, node_id, channel_id, enabled, status, last_data_at) "+
			"VALUES (?, ?, ?, ?, ?, true, ?, ?)",
		id, "test", "tail-anchor-device", "node-test", 1, models.EdgeDeviceStatusActive, lastDataAt,
	).Error; err != nil {
		t.Fatalf("seed edge_devices: %v", err)
	}
}

// seedFramesInRange 在 [from, to) 内按 step 铺帧（只用于 count/min/max 探测，payload 无意义）。
func seedFramesInRange(t *testing.T, db *gorm.DB, deviceID uint, from, to time.Time, step time.Duration) time.Time {
	t.Helper()
	var frames []models.TelemetryFrame
	var last time.Time
	for ts := from; ts.Before(to); ts = ts.Add(step) {
		frames = append(frames, models.TelemetryFrame{
			DeviceID: deviceID, TS: ts, Seq: 0,
			FrameKind: models.TelemetryFrameKindKey, SlotID: 1,
			Payload: []byte{0x01, 0x00, 0x7F},
		})
		last = ts
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 1000).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}
	return last
}

// TestProbeDeviceLastDataAt_ReadsColumn 是最直接的守卫：
// 证明 `probeDeviceLastDataAt` 真的把列读出来了，而不是永远返回零值。
//
// **这条如果红，上面所有集成用例都是假绿**（会走"未知 ⇒ 退回 end"分支）。
func TestProbeDeviceLastDataAt_ReadsColumn(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	const dev = 9917
	want := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	seedEdgeDeviceWithLastData(t, db, dev, want)
	defer db.Exec("DELETE FROM edge_devices WHERE id = ?", dev)

	got := probeDeviceLastDataAt(context.Background(), db, dev)
	if got.IsZero() {
		t.Fatal("probeDeviceLastDataAt 返回零值 —— 说明它没读到列（或走了错误分支）。" +
			"这会让所有集成用例静默退回 end 锚点，测的仍是旧逻辑")
	}
	if !got.Equal(want) {
		t.Fatalf("probeDeviceLastDataAt = %v, want %v", got, want)
	}
}

// ============ 下面两条 probeFrameCoverage 用例已删（2026-11-08）============
//
//	TestProbeFrameCoverage_QuietDeviceIsCoveredViaDB
//	TestProbeFrameCoverage_StuckFlushFallsBackViaDB
//
// 它们测的是「覆盖探测在真 PG 上的 count/min/max + 尾部判据」，
// 而被测函数 probeFrameCoverage 已随覆盖判据整体删除（见 history_source.go 的墓碑）。
//
// ⚠ 本文件**保留** TestProbeDeviceLastDataAt_ReadsColumn 与它的两个造数 helper：
//   那个函数没删 —— 它现在服务 rollup 读路径的尾巴空帧诊断（tailEmptyLooksLikeLag）。
