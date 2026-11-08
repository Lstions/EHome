package api

// metrics_points_stored_frames_test.go — points_stored 改从帧表计数的验收（2026-11-08）。
//
// 守三件事：
//  ① 帧表可用时**精确计数**且**完全不碰 unified_data**（这条查询正是挡住 DDL 的元凶）；
//  ② 帧表为空时回落旧口径（不能把指标直接变成 0）；
//  ③ 语义等价：帧表 count(*) == unified_data 的 count(DISTINCT (device_id,timestamp))。

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// countUnifiedHits2 统计打到 unified_data 的 SQL 条数（与 node-latest 用例同款手法）。
func countUnifiedHits2(t *testing.T, db *gorm.DB) *int64 {
	t.Helper()
	var hits int64
	name := "test:ps_count_unified_" + t.Name()
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Contains(tx.Statement.SQL.String(), "unified_data") {
			atomic.AddInt64(&hits, 1)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	return &hits
}

// TestPointsStored_UsesFrameTableWithoutTouchingUnifiedData 是核心断言。
func TestPointsStored_UsesFrameTableWithoutTouchingUnifiedData(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")

	const dev = 9991
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	// 10 帧（每帧 3 个字段）=> 帧表 10 行；unified_data 里若存在则是 30 行、DISTINCT 10
	seedFramesOnlyFields(t, db, dev, start, start.Add(50*time.Second), dict, []string{"current", "rsoc", "total_voltage"}, 5*time.Second)

	hits := countUnifiedHits2(t, db)
	n, err := countStoredFrames(db)
	if err != nil {
		t.Fatalf("countStoredFrames: %v", err)
	}
	if n != 10 {
		t.Fatalf("帧表有 10 帧，countStoredFrames = %d", n)
	}
	if got := atomic.LoadInt64(hits); got != 0 {
		t.Fatalf("打了 %d 条 unified_data SQL —— 必须为 0（这条查询正是挡住 DETACH 的元凶）", got)
	}
}

// TestPointsStored_FallsBackWhenFrameTableEmpty 守回落。
func TestPointsStored_FallsBackWhenFrameTableEmpty(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM unified_data")

	const dev = 9992
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	// 只写 unified_data：3 个字段 x 5 时刻 = 15 行，DISTINCT (device,timestamp) = 5
	var rows []models.UnifiedData
	for i := 0; i < 5; i++ {
		ts := start.Add(time.Duration(i) * 5 * time.Second)
		for _, name := range []string{"current", "rsoc", "total_voltage"} {
			rows = append(rows, models.UnifiedData{
				DeviceID: dev, SensorName: name, Value: float64(i), Unit: "u", Timestamp: ts,
			})
		}
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 100).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}

	n, err := countStoredFrames(db)
	if err != nil {
		t.Fatalf("countStoredFrames: %v", err)
	}
	if n != 5 {
		t.Fatalf("回落口径 = %d, want 5（15 行 / 3 字段 = 5 个采样帧）—— "+
			"帧表为空时必须回落 unified_data，不能让指标变成 0", n)
	}
}
