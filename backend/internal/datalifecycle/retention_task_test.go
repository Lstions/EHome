package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedRetentionDevice 建一个带单行数据的逻辑设备, 数据时间戳 = oldest。
// logical_device_id 必须在软删前挂载 (GORM 软删 scope 静默吞 Update)。
func seedRetentionDevice(t *testing.T, db *gorm.DB, key string, retentionDays int, oldest time.Time) *models.LogicalDevice {
	t.Helper()
	ld := &models.LogicalDevice{IdentityKey: key, Name: key, DeviceType: "bms_jbd", RetentionDays: retentionDays}
	if err := db.Create(ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	dev := seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
	db.Model(dev).Update("logical_device_id", ld.ID)
	if err := db.Create(&models.UnifiedData{
		DeviceID:        dev.ID,
		SensorName:      "voltage",
		Value:           1,
		Timestamp:       oldest,
		LogicalDeviceID: &ld.ID,
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	db.Delete(dev) // 软删实例 — 数据保留
	return ld
}

func TestRetentionTask_Tier30Notice(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 最早数据 now-340d, retention 365 → 剩余 25d → 30 天档。
	ld := seedRetentionDevice(t, db, "r30", 365, now.AddDate(0, 0, -340))

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].NotifiedTier != 30 {
		t.Fatalf("results = %+v, want NotifiedTier=30", results)
	}
	var n models.Notification
	if err := db.Where("source = ?", NotificationSourceRetentionExpiring).First(&n).Error; err != nil {
		t.Fatalf("notification not created: %v", err)
	}
	if n.Type != "warning" || n.Title != retentionNoticeTitle30 {
		t.Errorf("notification = type %s title %q", n.Type, n.Title)
	}
	// 文案含延长入口 (§4.2)。
	if n.Description == "" {
		t.Error("notification description empty, want retention-extension hint")
	}

	// 同窗口重跑: 去重不再发。
	results, err = r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if results[0].NotifiedTier != 0 {
		t.Errorf("rerun NotifiedTier = %d, want 0 (deduped)", results[0].NotifiedTier)
	}
	var count int64
	db.Model(&models.Notification{}).Where("source = ?", NotificationSourceRetentionExpiring).Count(&count)
	if count != 1 {
		t.Errorf("notifications = %d, want 1 after dedup", count)
	}
	_ = ld
}

func TestRetentionTask_Tier7Notice(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 最早数据 now-360d, retention 365 → 剩余 5d → 7 天档。
	seedRetentionDevice(t, db, "r7", 365, now.AddDate(0, 0, -360))

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].NotifiedTier != 7 {
		t.Fatalf("results = %+v, want NotifiedTier=7", results)
	}
	var n models.Notification
	db.Where("source = ?", NotificationSourceRetentionExpiring).First(&n)
	if n.Title != retentionNoticeTitle7 {
		t.Errorf("title = %q, want 7-day tier", n.Title)
	}
}

func TestRetentionTask_ExpiredDeletesRows(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 最早数据 now-366d → 已到期 → 分批硬删。保留新数据 (now-10d) 不动。
	ld := seedRetentionDevice(t, db, "expired", 365, now.AddDate(0, 0, -366))
	dev := seedDevice(t, db, "expired-fresh", "bms_jbd", "expired", false)
	db.Model(dev).Update("logical_device_id", ld.ID)
	db.Delete(dev)
	fresh := now.AddDate(0, 0, -10)
	db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "voltage", Value: 2, Timestamp: fresh, LogicalDeviceID: &ld.ID})

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSize(1) // 强制分批
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].RowsDeleted != 1 {
		t.Fatalf("RowsDeleted = %d, want 1 (only expired row)", results[0].RowsDeleted)
	}
	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("remaining rows = %d, want 1 (fresh row kept)", remaining)
	}
	var kept models.UnifiedData
	db.First(&kept)
	if !kept.Timestamp.Equal(fresh) {
		t.Errorf("kept row timestamp = %v, want fresh %v", kept.Timestamp, fresh)
	}
}

func TestRetentionTask_NotYetExpiring(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 最早数据 now-100d, retention 365 → 剩余 265d → 无动作。
	seedRetentionDevice(t, db, "safe", 365, now.AddDate(0, 0, -100))

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].NotifiedTier != 0 || results[0].RowsDeleted != 0 {
		t.Errorf("results = %+v, want no action", results)
	}
	var count int64
	db.Model(&models.Notification{}).Count(&count)
	if count != 0 {
		t.Errorf("notifications = %d, want 0", count)
	}
}

func TestRetentionTask_DefersPendingMerge(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 已到期的源, 但处于 pending 合并 → 顺延 (不通知不删除)。
	src1 := seedRetentionDevice(t, db, "msrc1", 1, now.AddDate(0, 0, -400))
	src2 := seedRetentionDevice(t, db, "msrc2", 1, now.AddDate(0, 0, -400))
	// 目标继承系统级快照: 置 1 天使搬迁完成后的合并数据同样到期。
	SetSystemRetentionDays(1)
	defer SetSystemRetentionDays(365)
	startMerge(t, db, src1, src2) // 两源均 pending

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 源顺延; 新建的合并目标无数据也静默跳过。
	for _, res := range results {
		if res.RowsDeleted != 0 || res.NotifiedTier != 0 || res.Err != "" {
			t.Errorf("pending-merge participant acted: %+v", res)
		}
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 2 {
		t.Errorf("rows = %d, want 2 (deferred, nothing deleted)", rows)
	}

	// 合并完成 (搬迁 done) 后, retention 恢复处理。
	m := NewMigrator(db)
	m.SetBatchSleep(0)
	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	results, err = r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run after merge: %v", err)
	}
	var totalDeleted int64
	for _, res := range results {
		totalDeleted += res.RowsDeleted
	}
	if totalDeleted != 2 {
		t.Errorf("deleted after merge done = %d, want 2", totalDeleted)
	}
}

func TestRetentionTask_SkipsPurgeRequested(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ld := seedRetentionDevice(t, db, "purged", 1, now.AddDate(0, 0, -400))
	db.Model(&models.LogicalDevice{}).Where("id = ?", ld.ID).Update("purge_requested", true)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("purge_requested device processed: %+v", results)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want 1 (purge task owns this device)", rows)
	}
}

// TestRetentionTask_ScopeUsesInstanceFallback — retention 删除与查询同一
// scope 解析 (v3.1-Q2): backfill 前 logical_device_id NULL 的旧行经
// 实例兜底也被清理, retention 不形同虚设。
func TestRetentionTask_ScopeUsesInstanceFallback(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// retention 365: 种子行 (now-10d, 带 logical_id) 未到期保留;
	// NULL-logical 旧行 (now-400d) 到期, 只能经实例兜底命中。
	ld := seedRetentionDevice(t, db, "nullrow", 365, now.AddDate(0, 0, -10))
	var dev models.EdgeDevice
	db.Unscoped().Where("logical_device_id = ?", ld.ID).First(&dev)
	db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "current", Value: 3,
		Timestamp: now.AddDate(0, 0, -400), LogicalDeviceID: nil})

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].RowsDeleted != 1 {
		t.Fatalf("RowsDeleted = %d, want 1 (NULL-logical row via fallback)", results[0].RowsDeleted)
	}
	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1", remaining)
	}
}
