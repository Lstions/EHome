package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func timeNow() time.Time { return time.Now() }

// seedLogicalWithInstances creates a logical device plus n soft-deleted
// instances (each with data rows) and returns the logical device.
func seedLogicalWithInstances(t *testing.T, db *gorm.DB, key string, instances int, rowsPerInstance int) *models.LogicalDevice {
	t.Helper()
	ld := &models.LogicalDevice{IdentityKey: key, Name: key, DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	for i := 0; i < instances; i++ {
		dev := seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
		db.Model(dev).Update("logical_device_id", ld.ID)
		for r := 0; r < rowsPerInstance; r++ {
			if err := db.Create(&models.UnifiedData{
				DeviceID:        dev.ID,
				SensorName:      "voltage",
				Value:           float64(r),
				Timestamp:       timeNow(),
				LogicalDeviceID: nil, // pre-backfill rows: logical_device_id NULL
			}).Error; err != nil {
				t.Fatalf("seed unified_data: %v", err)
			}
			if err := db.Create(&models.DeviceData{
				DeviceID:  dev.ID,
				NodeID:    "NODE001",
				DataJSON:  `{"v":1}`,
				Timestamp: timeNow(),
			}).Error; err != nil {
				t.Fatalf("seed device_data: %v", err)
			}
		}
		db.Delete(dev) // soft delete the instance
	}
	return ld
}

func TestScopeCond_Variants(t *testing.T) {
	cases := []struct {
		name     string
		scope    Scope
		wantCond string
		wantArgs int
	}{
		{"empty", Scope{}, "1 = 0", 0},
		{"logical only", Scope{LogicalIDs: []uint{1}}, "logical_device_id IN ?", 1},
		{"instance only", Scope{InstanceIDs: []uint{5}}, "logical_device_id IS NULL AND device_id IN ?", 1},
		{"full", Scope{LogicalIDs: []uint{1}, InstanceIDs: []uint{5}},
			"logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?)", 2},
	}
	for _, tc := range cases {
		cond, args := tc.scope.Cond()
		if cond != tc.wantCond {
			t.Errorf("%s: cond = %q, want %q", tc.name, cond, tc.wantCond)
		}
		if len(args) != tc.wantArgs {
			t.Errorf("%s: %d args, want %d", tc.name, len(args), tc.wantArgs)
		}
	}
}

func TestResolveScope_IncludesPendingSourcesAndSoftDeletedInstances(t *testing.T) {
	db := testutil.OpenTestDB(t)
	target := &models.LogicalDevice{IdentityKey: "t:target", Name: "target", DeviceType: "t", RetentionDays: 365}
	if err := db.Create(target).Error; err != nil {
		t.Fatal(err)
	}
	pending := models.MergeStatusPending
	source := &models.LogicalDevice{IdentityKey: "t:source", Name: "source", DeviceType: "t", RetentionDays: 365,
		MergedInto: &target.ID, MergeStatus: &pending}
	if err := db.Create(source).Error; err != nil {
		t.Fatal(err)
	}
	done := models.MergeStatusDone
	doneSource := &models.LogicalDevice{IdentityKey: "t:done", Name: "done-src", DeviceType: "t", RetentionDays: 365,
		MergedInto: &target.ID, MergeStatus: &done}
	if err := db.Create(doneSource).Error; err != nil {
		t.Fatal(err)
	}

	instTarget := seedDevice(t, db, "i1", "t", "a", true)
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", instTarget.ID).Update("logical_device_id", target.ID)
	instSource := seedDevice(t, db, "i2", "t", "b", true)
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", instSource.ID).Update("logical_device_id", source.ID)

	scope, err := ResolveScope(db, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	// pending source included, done source excluded
	hasLogical := func(id uint) bool {
		for _, v := range scope.LogicalIDs {
			if v == id {
				return true
			}
		}
		return false
	}
	if !hasLogical(target.ID) || !hasLogical(source.ID) {
		t.Errorf("expected target+pending source in LogicalIDs, got %v", scope.LogicalIDs)
	}
	if hasLogical(doneSource.ID) {
		t.Errorf("done source must not be included, got %v", scope.LogicalIDs)
	}
	if len(scope.InstanceIDs) != 2 {
		t.Errorf("expected 2 instances (both soft-deleted, Unscoped), got %v", scope.InstanceIDs)
	}
}

// ==================== purge 守卫 ====================

func TestPurge_GuardAbandonsWhenLivingInstanceReappears(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 1, 3)
	ld.PurgeRequested = true
	db.Save(ld)

	// User re-created a device inheriting this identity (living instance).
	living := seedDevice(t, db, "recreated", "bms_jbd", "0x76", false)
	db.Model(living).Update("logical_device_id", ld.ID)

	p := NewPurger(db)
	p.SetSchedule(0, 0, 0)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != AbandonedLivingInstance {
		t.Fatalf("expected abandoned_living_instance, got %+v", results)
	}
	// Flag cleared, data intact.
	var reloaded models.LogicalDevice
	db.First(&reloaded, ld.ID)
	if reloaded.PurgeRequested {
		t.Errorf("purge_requested must be cleared after abandon")
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 3 {
		t.Errorf("data must be intact, got %d rows", rows)
	}
}

func TestPurge_GuardDefersWhenPendingMerge(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 1, 3)
	ld.PurgeRequested = true
	db.Save(ld)

	// The logical device is the TARGET of a pending merge → defer.
	pending := models.MergeStatusPending
	src := &models.LogicalDevice{IdentityKey: "bms_jbd:0x77", Name: "src", DeviceType: "bms_jbd",
		RetentionDays: 365, MergedInto: &ld.ID, MergeStatus: &pending}
	if err := db.Create(src).Error; err != nil {
		t.Fatal(err)
	}

	p := NewPurger(db)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != DeferredPendingMerge {
		t.Fatalf("expected deferred_pending_merge, got %+v", results)
	}
	// Flag kept (retry later), data intact, logical device row kept.
	var reloaded models.LogicalDevice
	db.First(&reloaded, ld.ID)
	if !reloaded.PurgeRequested {
		t.Errorf("purge_requested must be kept while deferred")
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 3 {
		t.Errorf("data must be intact while deferred, got %d rows", rows)
	}
}

func TestPurge_GuardDefersWhenPendingSource(t *testing.T) {
	db := testutil.OpenTestDB(t)
	target := &models.LogicalDevice{IdentityKey: "bms_jbd:0x99", Name: "target", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(target).Error; err != nil {
		t.Fatal(err)
	}
	// The logical device is itself a PENDING SOURCE → defer.
	ld := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 1, 2)
	pending := models.MergeStatusPending
	ld.MergedInto = &target.ID
	ld.MergeStatus = &pending
	ld.PurgeRequested = true
	db.Save(ld)

	p := NewPurger(db)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != DeferredPendingMerge {
		t.Fatalf("expected deferred_pending_merge for pending source, got %+v", results)
	}
}

// ==================== purge 完整执行 + FK 解除顺序 ====================

func TestPurge_FullLifecycleDeletesDataAndDissolvesReferences(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 2, 5)
	ld.PurgeRequested = true
	db.Save(ld)

	// calibration_cache orphan rows for the instances (§2.5).
	var insts []models.EdgeDevice
	db.Unscoped().Where("logical_device_id = ?", ld.ID).Find(&insts)
	for _, inst := range insts {
		db.Create(&models.CalibrationCache{
			NodeID: "NODE001", EdgeDeviceID: inst.ID, DeviceType: "bms_jbd", Data: "{}",
		})
	}
	// A done-merge source pointing at this logical device (B4 inbound edge).
	done := models.MergeStatusDone
	srcDone := &models.LogicalDevice{IdentityKey: "bms_jbd:0x01", Name: "done-src", DeviceType: "bms_jbd",
		RetentionDays: 365, MergedInto: &ld.ID, MergeStatus: &done}
	if err := db.Create(srcDone).Error; err != nil {
		t.Fatal(err)
	}

	p := NewPurger(db)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != Purged {
		t.Fatalf("expected purged, got %+v", results)
	}
	// 2 instances × 5 rows × 2 tables = 20 rows hard-deleted.
	if results[0].RowsDeleted != 20 {
		t.Errorf("expected 20 rows deleted, got %d", results[0].RowsDeleted)
	}

	// Data gone from both tables.
	var unifiedRows, deviceRows int64
	db.Model(&models.UnifiedData{}).Count(&unifiedRows)
	db.Model(&models.DeviceData{}).Count(&deviceRows)
	if unifiedRows != 0 || deviceRows != 0 {
		t.Errorf("expected 0/0 rows after purge, got unified=%d device=%d", unifiedRows, deviceRows)
	}
	// calibration_cache orphans cleaned.
	var calRows int64
	db.Model(&models.CalibrationCache{}).Count(&calRows)
	if calRows != 0 {
		t.Errorf("expected calibration_cache cleaned, got %d rows", calRows)
	}
	// F6 outbound: soft-deleted instances detached (Unscoped).
	var attached int64
	db.Unscoped().Model(&models.EdgeDevice{}).
		Where("logical_device_id = ?", ld.ID).Count(&attached)
	if attached != 0 {
		t.Errorf("expected edge_devices detached from logical device, got %d", attached)
	}
	// Soft-deleted instance rows themselves still exist (only FK nulled).
	var instRows int64
	db.Unscoped().Model(&models.EdgeDevice{}).Count(&instRows)
	if instRows != 2 {
		t.Errorf("expected 2 soft-deleted instance rows kept, got %d", instRows)
	}
	// B4 inbound: done source's merged_into/merge_status dissolved.
	var reloadedSrc models.LogicalDevice
	db.First(&reloadedSrc, srcDone.ID)
	if reloadedSrc.MergedInto != nil || reloadedSrc.MergeStatus != nil {
		t.Errorf("expected inbound merge edge dissolved, got merged_into=%v merge_status=%v",
			reloadedSrc.MergedInto, reloadedSrc.MergeStatus)
	}
	// Finally: logical_device row deleted.
	var ldCount int64
	db.Model(&models.LogicalDevice{}).Count(&ldCount)
	if ldCount != 1 { // only srcDone remains
		t.Errorf("expected 1 logical device left (done source), got %d", ldCount)
	}
	var gone models.LogicalDevice
	if err := db.First(&gone, ld.ID).Error; err == nil {
		t.Errorf("purged logical device row must be deleted")
	}
}

func TestPurge_BatchedDelete(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 1, 0)
	// 15 rows under the instance's device_id with NULL logical_device_id.
	var inst models.EdgeDevice
	db.Unscoped().Where("logical_device_id = ?", ld.ID).First(&inst)
	for i := 0; i < 15; i++ {
		db.Create(&models.UnifiedData{DeviceID: inst.ID, SensorName: "v", Timestamp: timeNow()})
	}
	ld.PurgeRequested = true
	db.Save(ld)

	p := NewPurger(db)
	p.SetSchedule(0, 0, 0)
	// Force tiny batches to exercise the multi-batch loop (15 rows / 4 = 4 batches).
	p.SetBatchSize(4)

	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != Purged || results[0].RowsDeleted != 15 {
		t.Fatalf("expected purged 15 rows, got %+v", results)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 0 {
		t.Errorf("expected 0 rows, got %d", rows)
	}
}

func TestPurge_OnlyTouchesRequestedLogicalDevices(t *testing.T) {
	db := testutil.OpenTestDB(t)
	victim := seedLogicalWithInstances(t, db, "bms_jbd:0x76", 1, 4)
	victim.PurgeRequested = true
	db.Save(victim)
	bystander := seedLogicalWithInstances(t, db, "bms_jbd:0x77", 1, 4)

	p := NewPurger(db)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].LogicalID != victim.ID {
		t.Fatalf("expected only victim processed, got %+v", results)
	}
	// Bystander data untouched.
	scope, err := ResolveScope(db, bystander.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := ApplyScope(db.Model(&models.UnifiedData{}), scope).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Errorf("bystander rows must survive, got %d", rows)
	}
	var ld models.LogicalDevice
	if err := db.First(&ld, bystander.ID).Error; err != nil {
		t.Errorf("bystander logical device must survive: %v", err)
	}
}
