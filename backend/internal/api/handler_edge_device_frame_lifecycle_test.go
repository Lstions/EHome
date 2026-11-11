package api

// handler_edge_device_frame_lifecycle_test.go — 契约 §4 里属于 api 层的三处
// 生命周期改动（data-summary 时间范围 / merge-history 迁移 / latestDeviceTimestamp）
// 在 **HTTP 层**的验收（评审 B-3）。
//
// 为什么必须有用例：这三处都是"静默降级"型缺陷 —— 漏掉 telemetry_frame 不会
// 报错，只会让：
//   · 删除确认弹窗显示「这台设备没有历史」（其实有帧数据）；
//   · 合并历史把帧留在源设备上（源设备行已删 ⇒ 帧成了永久孤儿，且不再有任何
//     生命周期路径能清它，因为 purge 只按 device_id 找已存在的设备行）；
//   · 列表里的「最后采集」停在旧值，而图表能看到新数据。
//
// 这些都不会让任何既有测试变红，所以必须显式钉住。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// seedFrameTable 在 api 测试库里建出帧表（testutil 的 AutoMigrate 清单不含它）。
func seedFrameTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&models.TelemetryFrame{}, &models.TelemetrySlot{}); err != nil {
		t.Fatalf("automigrate telemetry tables: %v", err)
	}
}

// seedFrameRow 往帧表写一行（关键帧，时间戳 = now）。
func seedFrameRow(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time) {
	seedFrameAt(t, db, deviceID, ts)
}

// seedFrameAt 往帧表写一行关键帧（指定时间戳）。
//
// 与 seedFrameRow 的差别只有时间戳是否显式：保留两个名字是因为调用点的可读性
// 不同（"随便什么时候"vs"必须是这个时刻"）。实现合一，避免两份 fixture 漂移。
func seedFrameAt(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time) {
	t.Helper()
	f := models.TelemetryFrame{
		DeviceID:  deviceID,
		TS:        ts,
		Seq:       0,
		FrameKind: models.TelemetryFrameKindKey,
		SlotID:    1,
		Payload:   []byte{0x01, 0x00, 0x7F},
	}
	if err := db.Create(&f).Error; err != nil {
		t.Fatalf("seed frame device=%d ts=%s: %v", deviceID, ts.Format(time.RFC3339), err)
	}
}

// TestMergeHistoryMigratesTelemetryFrames 守 merge-history 真的把帧搬到目标设备。
//
// 关键断言：
//  1. 源设备的帧归到目标设备（否则帧成了孤儿）；
//  2. rows_migrated 计入迁入的帧数（用户看到的"N 行"要和实际一致）；
//  3. 目标设备已有的同 (ts, seq) 帧不被覆盖，也不让合并失败（ON CONFLICT 语义）；
//  4. 重跑幂等（第二次 rows_migrated = 0 或仅剩已搬不动的部分）。
func TestMergeHistoryMigratesTelemetryFrames(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db) // 设备 1 = 目标（存活），设备 2 = 源
	seedFrameTable(t, db)

	old := models.EdgeDevice{}
	db.First(&old, 2)
	if err := db.Delete(&old).Error; err != nil { // 源已软删（合并的主用途）
		t.Fatalf("soft delete source: %v", err)
	}

	base := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
	// 源设备 3 帧；目标设备在源第一帧的同一 (ts, seq) 上已有一帧（撞主键）。
	seedFrameRow(t, db, 2, base.Add(-3*time.Minute))
	seedFrameRow(t, db, 2, base.Add(-2*time.Minute))
	seedFrameRow(t, db, 2, base.Add(-1*time.Minute))
	seedFrameRow(t, db, 1, base.Add(-3*time.Minute))
	// 再加一条 unified_data 行，确认两种表的计数都被计入。
	if err := db.Create(&models.UnifiedData{DeviceID: 2, SensorName: "voltage", Value: 1}).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}

	code, resp := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("merge expected 200, got %d", code)
	}
	if len(resp.Warnings) > 0 {
		t.Errorf("warnings = %v, want none（帧表迁移不该产生警告）", resp.Warnings)
	}

	// 源设备的帧必须一帧不剩（源设备行已删，留着就是永久孤儿）。
	var srcFrames int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", 2).Count(&srcFrames)
	if srcFrames != 0 {
		t.Errorf("源设备仍有 %d 帧 —— 合并后成了没有任何生命周期路径能清掉的孤儿", srcFrames)
	}
	// 目标设备：原有 1 帧 + 迁入 2 帧（第 3 帧撞主键被丢弃）。
	var tgtFrames int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", 1).Count(&tgtFrames)
	if tgtFrames != 3 {
		t.Errorf("目标设备帧数 = %d, want 3（1 原有 + 2 迁入，1 帧因同 (ts,seq) 丢弃）", tgtFrames)
	}
	// rows_migrated 必须把帧算进去。
	//
	// 2026-11-11: 期望从 3 降到 2 —— 原夹具里那 1 行 unified_data 已不会迁移
	// （unified_data 已停写、退役，且 merge-history 的 UPDATE device_id 循环现在
	// 走动态表清单 ExistingTimeseriesTables，里面只剩帧表）。
	// 所以本用例现在真实覆盖的是「帧走 MigrateTelemetryFramesToDevice 迁移并计数」。
	if resp.RowsMigrated != 2 {
		t.Errorf("rows_migrated = %v, want 2（2 telemetry_frame；unified_data 已退役不参与迁移）", resp.RowsMigrated)
	}

	// 幂等：源已空，重跑不应再搬任何东西。
	code, resp2 := rr2Merge(t, r, map[string]interface{}{"source_ids": []uint{2}})
	if code != http.StatusOK {
		t.Fatalf("second merge expected 200, got %d", code)
	}
	if resp2.RowsMigrated != 0 {
		t.Errorf("second merge rows_migrated = %v, want 0 (idempotent)", resp2.RowsMigrated)
	}
}

// TestDataSummaryCountsFrameTimeRange 守 data-summary 的时间范围涵盖帧表。
//
// 场景：帧表里有该设备的数据，data-summary 必须读到它。
//
// 2026-11-11: 夹具从「unified_data 一条更早的行 + 帧表一条更新的行」改为「两帧」。
// 原来的场景（帧表比 unified_data 新）已经不存在 —— 两张原始表都退役了，
// data-summary 的 tsTables 只剩帧表。所以本用例现在钉的是「first/last 两端都真的
// 从帧表读出来了」，仍然是**有信息量的断言**：漏读帧表的 ts 列（写成 timestamp）、
// 或把两端时间取反，都会立刻红。
func TestDataSummaryCountsFrameTimeRange(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db) // 设备 1、2
	seedFrameTable(t, db)

	oldTS := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	frameTS := time.Date(2026, 11, 7, 5, 0, 0, 0, time.UTC)
	seedFrameRow(t, db, 1, oldTS)
	seedFrameRow(t, db, 1, frameTS)

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/1/data-summary", "")
	if w.Code != http.StatusOK {
		t.Fatalf("data-summary expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Target struct {
				FirstTime *time.Time `json:"first_time"`
				LastTime  *time.Time `json:"last_time"`
			} `json:"target"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, w.Body.String())
	}
	tgt := resp.Data.Target
	if tgt.LastTime == nil {
		t.Fatalf("target last_time = nil，want %s（帧表里有数据）", frameTS.Format(time.RFC3339))
	}
	if !tgt.LastTime.Equal(frameTS) {
		t.Errorf("target last_time = %s, want %s —— 漏读帧表的 (device_id, ts) 时间列",
			tgt.LastTime.Format(time.RFC3339), frameTS.Format(time.RFC3339))
	}
	if tgt.FirstTime == nil || !tgt.FirstTime.Equal(oldTS) {
		t.Errorf("target first_time = %v, want %s", tgt.FirstTime, oldTS.Format(time.RFC3339))
	}
}

// TestLatestDeviceTimestampCoversFrames 直接钉住 latestDeviceTimestamp 覆盖帧表。
func TestLatestDeviceTimestampCoversFrames(t *testing.T) {
	_, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db)
	seedFrameTable(t, db)

	oldTS := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	frameTS := time.Date(2026, 11, 7, 6, 30, 0, 0, time.UTC)
	if err := db.Create(&models.UnifiedData{
		DeviceID: 1, SensorName: "voltage", Value: 1, Timestamp: oldTS,
	}).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}
	seedFrameRow(t, db, 1, frameTS)

	latest, err := latestDeviceTimestamp(db, 1)
	if err != nil {
		t.Fatalf("latestDeviceTimestamp: %v", err)
	}
	if latest == nil {
		t.Fatal("latest = nil, want 帧表里的最新时间")
	}
	if !latest.Equal(frameTS) {
		t.Errorf("latest = %s, want %s（必须取三张表的最大值，帧表列名是 ts）",
			latest.Format(time.RFC3339), frameTS.Format(time.RFC3339))
	}
}

// TestDataSummaryWorksWithoutFrameTable 守**降级边界**：帧表不存在时端点照常工作。
//
// 精简部署/测试库没有 telemetry_frame。data-summary 不能被它拖成 500 ——
// 一个新增的可选表把既有端点打成故障是不可接受的回归。
func TestDataSummaryWorksWithoutFrameTable(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	rr2SeedMergeBase(t, db) // 注意：这里**不**建帧表

	ts := time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC)
	if err := db.Create(&models.UnifiedData{
		DeviceID: 1, SensorName: "voltage", Value: 1, Timestamp: ts,
	}).Error; err != nil {
		t.Fatalf("seed unified: %v", err)
	}

	w := rr2Do(t, r, "GET", "/api/v1/edge-devices/1/data-summary", "")
	if w.Code != http.StatusOK {
		t.Fatalf("帧表不存在时 data-summary 必须照常工作，got %d body=%s", w.Code, w.Body.String())
	}
}
