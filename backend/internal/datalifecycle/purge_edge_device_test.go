package datalifecycle

// purge_edge_device_test.go — 「删除设备时同时删除数据」后台任务的测试。
//
// 2026-10-23: 该任务由原 Purger（按逻辑设备 purge_requested）改写为按设备
// （edge_devices.data_purge_requested）。语义不变：软删设备行 → 后台分批硬删
// 它的历史数据 → 清理校准行 → 物理删除设备行。
//
// 三条不变量（每条都能红）：
//  1. 到期设备的数据被真正删掉，**设备行也被物理删除**（否则列表里留幽灵）；
//  2. 设备在删除后被恢复（deleted_at 清空）⇒ 撤回删除意图、一行数据都不删
//     （否则会静默清空用户刚选择保留的历史 —— 不可恢复）；
//  3. 重复运行幂等：第二次没有目标行，零动作。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedPurgeCandidate 造一台「已软删且要求删数据」的设备，带 unified_data 与
// device_data 各一行、一行 sensor_field_obs、一行 data_sources、一行校准缓存。
//
// 为什么台账两表也要 seed（2026-10-25 F-01 审核）: DevicePurger 曾只删两张时序表
// 与 calibration，把 sensor_field_obs / data_sources 留在库里 —— 设备行已经物理
// 删除，GET /sensor-fields、GET /data-sources 却还能看到它（幽灵台账）。
// purge 的语义是「这台设备的数据全部清掉」，四表行数必须一起归零。
func seedPurgeCandidate(t *testing.T, db *gorm.DB, name string) *models.EdgeDevice {
	t.Helper()
	dev := seedDevice(t, db, name, "bms_jbd", name+"-hw", false)
	if err := db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "voltage", Value: 1}).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
	if err := db.Create(&models.DeviceData{DeviceID: dev.ID, NodeID: "NODE001", DataJSON: "{}"}).Error; err != nil {
		t.Fatalf("seed device_data: %v", err)
	}
	if err := db.Create(&models.CalibrationCache{
		NodeID: "NODE001", EdgeDeviceID: dev.ID, DeviceType: "bms_jbd", Data: "{}",
	}).Error; err != nil {
		t.Fatalf("seed calibration: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&models.SensorFieldObs{
		DeviceID: dev.ID, SensorName: "voltage", Unit: "V", FirstSeen: now, LastSeen: now, Samples: 1,
	}).Error; err != nil {
		t.Fatalf("seed sensor_field_obs: %v", err)
	}
	if err := db.Create(&models.DataSource{
		EdgeDeviceID: dev.ID, Category: "voltage", SourceType: "edge_device",
		Name: "e" + name + "·voltage", Status: "active", ManagedBy: "auto",
	}).Error; err != nil {
		t.Fatalf("seed data_sources: %v", err)
	}
	// 软删 + 置删除数据标记（与 DELETE /edge-devices/:id?delete_data=true 同形状）。
	if err := db.Delete(dev).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Update("data_purge_requested", true).Error; err != nil {
		t.Fatalf("set data_purge_requested: %v", err)
	}
	return dev
}

func TestDevicePurger_HardDeletesDataAndDeviceRow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := seedPurgeCandidate(t, db, "purge-me")

	p := NewDevicePurger(db)
	p.SetBatchSize(1) // 强制分批
	p.SetSchedule(0, 0, 0)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want exactly 1 (only the flagged device)", results)
	}
	if results[0].Outcome != Purged {
		t.Fatalf("outcome = %s, want %s (err=%s)", results[0].Outcome, Purged, results[0].Error)
	}
	if results[0].RowsDeleted != 2 {
		t.Errorf("RowsDeleted = %d, want 2 (1 unified_data + 1 device_data)", results[0].RowsDeleted)
	}
	// 数据清了。
	var unified, devdata int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", dev.ID).Count(&unified)
	db.Model(&models.DeviceData{}).Where("device_id = ?", dev.ID).Count(&devdata)
	if unified != 0 || devdata != 0 {
		t.Errorf("rows left: unified_data=%d device_data=%d, want 0/0", unified, devdata)
	}
	// 校准行清了（数据没了，工厂校准值不再有意义）。
	var cals int64
	db.Model(&models.CalibrationCache{}).Where("edge_device_id = ?", dev.ID).Count(&cals)
	if cals != 0 {
		t.Errorf("calibration rows = %d, want 0", cals)
	}
	// 台账/投影表也清了：设备行已物理删除，留着这些行就是「幽灵台账」
	// （GET /sensor-fields、GET /data-sources 还能看到一个不存在的设备）。
	var sfo, ds int64
	db.Model(&models.SensorFieldObs{}).Where("device_id = ?", dev.ID).Count(&sfo)
	db.Model(&models.DataSource{}).Where("edge_device_id = ?", dev.ID).Count(&ds)
	if sfo != 0 || ds != 0 {
		t.Errorf("ledger rows left: sensor_field_obs=%d data_sources=%d, want 0/0", sfo, ds)
	}
	// 设备行也被物理删除（不是软删残留）。
	var rows int64
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).Count(&rows)
	if rows != 0 {
		t.Errorf("edge_devices row still present (Unscoped count = %d), want 0 (幽灵行)", rows)
	}
}

// TestDevicePurger_AbandonsWhenDeviceRevived 是**不可恢复数据丢失**的守卫。
func TestDevicePurger_AbandonsWhenDeviceRevived(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := seedPurgeCandidate(t, db, "revived")

	// 用户点了恢复（或重建了同一条设备行）：deleted_at 清空。
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Update("deleted_at", nil).Error; err != nil {
		t.Fatalf("revive device: %v", err)
	}

	p := NewDevicePurger(db)
	p.SetSchedule(0, 0, 0)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != AbandonedRevived {
		t.Fatalf("results = %+v, want one AbandonedRevived", results)
	}
	// 一行都没删。
	var unified, devdata int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", dev.ID).Count(&unified)
	db.Model(&models.DeviceData{}).Where("device_id = ?", dev.ID).Count(&devdata)
	if unified != 1 || devdata != 1 {
		t.Errorf("rows after abandon: unified=%d device_data=%d, want 1/1 (history must survive)", unified, devdata)
	}
	// 标记被撤回，否则下一轮又会想删。
	var flag bool
	db.Unscoped().Model(&models.EdgeDevice{}).Select("data_purge_requested").
		Where("id = ?", dev.ID).Scan(&flag)
	if flag {
		t.Error("data_purge_requested must be cleared after abandoning")
	}
}

// TestDevicePurger_ClaimIsAtomic — claim() 的互斥语义（F-01 的协议核心）。
//
// 三个方向都要红：
//  1. 首次 claim=true（并把 data_purge_started_at 写上）；
//  2. 同一台设备第二次 claim=false（另一个 purger 不能拿到同一台设备）；
//  3. 设备被恢复（deleted_at 清空）后 claim=false（restore 与 purge 的互斥入口）。
func TestDevicePurger_ClaimIsAtomic(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := seedPurgeCandidate(t, db, "claim-atomic")
	p := NewDevicePurger(db)

	claimed, err := p.claim(context.Background(), dev.ID)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim = false, want true")
	}
	var claimAt *time.Time
	db.Unscoped().Model(&models.EdgeDevice{}).Select("data_purge_started_at").
		Where("id = ?", dev.ID).Scan(&claimAt)
	if claimAt == nil {
		t.Fatal("data_purge_started_at must be written by claim()")
	}

	claimed, err = p.claim(context.Background(), dev.ID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("second claim = true, want false (device already claimed by another run)")
	}

	// 复用同一行 + 恢复（restore 的写集）：deleted_at 清空后 claim 必须失败。
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Updates(map[string]interface{}{"deleted_at": nil, "data_purge_started_at": nil}).Error; err != nil {
		t.Fatalf("revive: %v", err)
	}
	claimed, err = p.claim(context.Background(), dev.ID)
	if err != nil {
		t.Fatalf("claim after revive: %v", err)
	}
	if claimed {
		t.Fatal("claim on a revived (live) device = true, want false")
	}
}

// TestDevicePurger_StaleClaimThreshold — 只清「陈旧」认领，不清在途认领（F-01 残余）。
//
// 场景：两个 purger 进程/两种时刻的认领：
//   - fresh: data_purge_started_at = now-1min（另一进程正在删）⇒ RunOnce 不得清它、
//     不得处理该设备；
//   - stale: data_purge_started_at = now-2h（上一进程崩溃残留）⇒ 必须清掉并正常 purge。
//
// 若实现退回「开头无条件清零」，fresh 设备会被本进程抢走并硬删 —— 这正是要防的窗口。
func TestDevicePurger_StaleClaimThreshold(t *testing.T) {
	db := testutil.OpenTestDB(t)
	fresh := seedPurgeCandidate(t, db, "claim-fresh")
	stale := seedPurgeCandidate(t, db, "claim-stale")
	now := time.Now()
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", fresh.ID).
		Update("data_purge_started_at", now.Add(-time.Minute)).Error; err != nil {
		t.Fatalf("set fresh claim: %v", err)
	}
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", stale.ID).
		Update("data_purge_started_at", now.Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("set stale claim: %v", err)
	}

	p := NewDevicePurger(db)
	p.SetBatchSize(1)
	p.SetSchedule(0, 0, 0)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// stale 设备被正常清理（设备行物理删除）。
	var staleRows int64
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", stale.ID).Count(&staleRows)
	if staleRows != 0 {
		t.Errorf("stale-claim device row still present, want purged (count=0)")
	}
	// fresh 设备的认领必须原样保留、数据一行不少、也不出现在本轮结果里。
	var claimAt *time.Time
	db.Unscoped().Model(&models.EdgeDevice{}).Select("data_purge_started_at").
		Where("id = ?", fresh.ID).Scan(&claimAt)
	if claimAt == nil {
		t.Fatal("fresh claim must NOT be cleared by another purger run (这会重新打开 F-01 的窗口)")
	}
	var unified int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", fresh.ID).Count(&unified)
	if unified != 1 {
		t.Errorf("fresh-claim device data rows = %d, want 1 (must not be touched)", unified)
	}
	for _, r := range results {
		if r.EdgeDeviceID == fresh.ID {
			t.Errorf("fresh-claim device %d must not be processed by this run, got result %+v", fresh.ID, r)
		}
	}
}

func TestDevicePurger_Idempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedPurgeCandidate(t, db, "once")

	p := NewDevicePurger(db)
	p.SetSchedule(0, 0, 0)
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("second run processed %d devices, want 0 (flag+garbage row both gone)", len(results))
	}
}
