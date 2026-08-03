package datalifecycle

import (
	"testing"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedDevice inserts a minimal node+channel+edge_device row and returns it.
func seedDevice(t *testing.T, db *gorm.DB, name, devType, hardwareID string, softDeleted bool) *models.EdgeDevice {
	t.Helper()
	if err := db.FirstOrCreate(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	var ch models.Channel
	if err := db.Where("node_id = ? AND hardware_type = ?", "NODE001", "I2C").FirstOrCreate(&models.Channel{
		NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	dev := &models.EdgeDevice{
		Name:       name,
		Type:       devType,
		NodeID:     "NODE001",
		ChannelID:  ch.ID,
		HardwareID: hardwareID,
	}
	if err := db.Create(dev).Error; err != nil {
		t.Fatalf("seed edge device: %v", err)
	}
	if softDeleted {
		if err := db.Delete(dev).Error; err != nil {
			t.Fatalf("soft delete: %v", err)
		}
	}
	return dev
}

// ==================== IdentityKey ====================

func TestIdentityKey(t *testing.T) {
	if got := IdentityKey("bms_jbd", "0x76"); got != "bms_jbd:0x76" {
		t.Errorf("expected bms_jbd:0x76, got %q", got)
	}
	// whitespace trimmed
	if got := IdentityKey(" bms_jbd ", " 0x76 "); got != "bms_jbd:0x76" {
		t.Errorf("expected trimmed key, got %q", got)
	}
	// empty hardware_id → uuid suffix (type:{uuid}, 36 chars + type + colon ≤ 64)
	got := IdentityKey("bms_jbd", "")
	if len(got) <= len("bms_jbd:") || got[:8] != "bms_jbd:" {
		t.Errorf("expected uuid-suffixed key with type prefix, got %q", got)
	}
	got2 := IdentityKey("bms_jbd", "")
	if got == got2 {
		t.Errorf("expected distinct uuid keys for empty hardware_id, got %q twice", got)
	}
}

func TestSequenceKeyTruncation(t *testing.T) {
	base := IdentityKey("some_very_long_device_type_name_x", "0xffffffff")
	got := sequenceKey(base, 2)
	if len(got) > identityKeyMaxLen {
		t.Errorf("sequence key exceeds %d chars: %d", identityKeyMaxLen, len(got))
	}
	if got[len(got)-2:] != "#2" {
		t.Errorf("expected #2 suffix, got %q", got)
	}
}

// ==================== 启动补建幂等 ====================

func TestBackfill_Idempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedDevice(t, db, "Dev A", "bms_jbd", "0x76", false)
	seedDevice(t, db, "Dev B", "sn3001_rain", "", false)
	seedDevice(t, db, "Dev C (soft-deleted)", "bms_jbd", "0x77", true)

	n, err := BackfillLogicalDevices(db, 365)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 backfilled, got %d", n)
	}

	var lds []models.LogicalDevice
	if err := db.Find(&lds).Error; err != nil {
		t.Fatal(err)
	}
	if len(lds) != 3 {
		t.Fatalf("expected 3 logical devices, got %d", len(lds))
	}
	// Every row back-written (Unscoped includes the soft-deleted one).
	var nullRows int64
	if err := db.Unscoped().Model(&models.EdgeDevice{}).
		Where("logical_device_id IS NULL").Count(&nullRows).Error; err != nil {
		t.Fatal(err)
	}
	if nullRows != 0 {
		t.Errorf("expected 0 unattached instances, got %d", nullRows)
	}

	// Second run: nothing new.
	n, err = BackfillLogicalDevices(db, 365)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 on rerun, got %d", n)
	}
	var count int64
	db.Model(&models.LogicalDevice{}).Count(&count)
	if count != 3 {
		t.Errorf("expected still 3 logical devices after rerun, got %d", count)
	}
}

func TestBackfill_RetentionSnapshot(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedDevice(t, db, "Dev", "bms_jbd", "0x10", false)
	if _, err := BackfillLogicalDevices(db, 30); err != nil {
		t.Fatal(err)
	}
	var ld models.LogicalDevice
	if err := db.First(&ld).Error; err != nil {
		t.Fatal(err)
	}
	if ld.RetentionDays != 30 {
		t.Errorf("expected retention snapshot 30, got %d", ld.RetentionDays)
	}
}

// ==================== 三路径撞车分流 ====================

func TestStartupPath_ReusesWhenNoLivingInstance(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// Existing logical device with no living instances (its instance is soft-deleted).
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "old", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	seedDevice(t, db, "ghost", "bms_jbd", "0x76", true)
	db.Unscoped().Model(&models.EdgeDevice{}).Where("name = ?", "ghost").Update("logical_device_id", ld.ID)

	// New instance with the same identity: startup path must reuse (no living instance).
	dev := seedDevice(t, db, "fresh", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathStartup, 365)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ld.ID {
		t.Errorf("expected reuse of logical device %d, got %d", ld.ID, got.ID)
	}
}

func TestStartupPath_SequenceKeyWhenLivingInstanceExists(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "old", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	// A living instance attached to the existing logical device.
	living := seedDevice(t, db, "living", "bms_jbd", "0x76", false)
	db.Model(living).Update("logical_device_id", ld.ID)

	// Second instance with same type+hardware_id (different channel is legal):
	// startup path must NOT reuse → creates key#2.
	dev := seedDevice(t, db, "second", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathStartup, 365)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == ld.ID {
		t.Fatalf("must not reuse: living instance exists")
	}
	if got.IdentityKey != "bms_jbd:0x76#2" {
		t.Errorf("expected bms_jbd:0x76#2, got %q", got.IdentityKey)
	}
}

func TestCreateNewPath_NeverReuses(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "old", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	dev := seedDevice(t, db, "new-device", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathCreateNew, 365)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == ld.ID {
		t.Fatalf("PathCreateNew must never reuse")
	}
	if got.IdentityKey != "bms_jbd:0x76#2" {
		t.Errorf("expected sequence key #2, got %q", got.IdentityKey)
	}
}

func TestDeletePath_ReusesExistingKey(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "old", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	dev := seedDevice(t, db, "doomed", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathDelete, 365)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ld.ID {
		t.Errorf("PathDelete should reuse existing key, got logical device %d", got.ID)
	}
}

func TestDeletePath_FollowsMergeChainToFinalTarget(t *testing.T) {
	db := testutil.OpenTestDB(t)
	final := models.LogicalDevice{IdentityKey: "bms_jbd:0x99", Name: "final", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&final).Error; err != nil {
		t.Fatal(err)
	}
	mid := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "mid", DeviceType: "bms_jbd", RetentionDays: 365, MergedInto: &final.ID}
	if err := db.Create(&mid).Error; err != nil {
		t.Fatal(err)
	}
	dev := seedDevice(t, db, "doomed", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathDelete, 365)
	if err != nil {
		t.Fatal(err)
	}
	// v3.2-终审 B5: must attach to the FINAL target, not the mid-chain node.
	if got.ID != final.ID {
		t.Errorf("expected attach to final target %d, got %d (mid=%d)", final.ID, got.ID, mid.ID)
	}
}

func TestDeletePath_RejectsReuseWhenPurgeRequested(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "old", DeviceType: "bms_jbd", RetentionDays: 365, PurgeRequested: true}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	dev := seedDevice(t, db, "doomed", "bms_jbd", "0x76", false)
	got, err := EnsureLogicalDevice(db, dev, PathDelete, 365)
	if err != nil {
		t.Fatal(err)
	}
	// v3.3-N1: purge_requested target must not be reused → sequence key.
	if got.ID == ld.ID {
		t.Fatalf("must not reuse purge_requested logical device")
	}
	if got.IdentityKey != "bms_jbd:0x76#2" {
		t.Errorf("expected bms_jbd:0x76#2, got %q", got.IdentityKey)
	}
}

func TestFollowMergeChain_CycleSafe(t *testing.T) {
	db := testutil.OpenTestDB(t)
	a := models.LogicalDevice{IdentityKey: "t:a", Name: "a", DeviceType: "t", RetentionDays: 365}
	b := models.LogicalDevice{IdentityKey: "t:b", Name: "b", DeviceType: "t", RetentionDays: 365}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	a.MergedInto = &b.ID
	b.MergedInto = &a.ID
	db.Save(&a)
	db.Save(&b)

	got, err := FollowMergeChain(db, &a)
	if err != nil {
		t.Fatalf("cycle must terminate, got error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a result even on malformed cycle")
	}
}

func TestCountInstances_IncludesSoftDeleted(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "bms_jbd:0x76", Name: "x", DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	living := seedDevice(t, db, "living", "bms_jbd", "0x76", false)
	db.Model(living).Update("logical_device_id", ld.ID)
	ghost := seedDevice(t, db, "ghost", "bms_jbd", "0x76", true)
	db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", ghost.ID).Update("logical_device_id", ld.ID)

	n, err := CountInstances(db, ld.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 instances (1 living + 1 soft-deleted), got %d", n)
	}
}
