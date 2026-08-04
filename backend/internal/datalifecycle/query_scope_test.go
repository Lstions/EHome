package datalifecycle

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestResolveDataQueryScope_NoLogicalIdentityFallsBackToDeviceID(t *testing.T) {
	db := testutil.OpenTestDB(t)

	dev := models.EdgeDevice{Name: "legacy", Type: "t", NodeID: "N1", ChannelID: 1}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}

	qs, err := ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qs.LogicalID != 0 {
		t.Errorf("expected fallback scope, got LogicalID=%d", qs.LogicalID)
	}
	if qs.FallbackDeviceID != dev.ID {
		t.Errorf("FallbackDeviceID = %d, want %d", qs.FallbackDeviceID, dev.ID)
	}
	if qs.InstanceDeleted {
		t.Error("living instance must not be flagged deleted")
	}
	if qs.DedupNeeded {
		t.Error("fallback scope never enables dedup")
	}
}

func TestResolveDataQueryScope_LogicalIdentityResolvesToFinalTarget(t *testing.T) {
	db := testutil.OpenTestDB(t)

	final := models.LogicalDevice{IdentityKey: "k:final", Name: "final", DeviceType: "t"}
	if err := db.Create(&final).Error; err != nil {
		t.Fatal(err)
	}
	mid := models.LogicalDevice{IdentityKey: "k:mid", Name: "mid", DeviceType: "t", MergedInto: &final.ID}
	if err := db.Create(&mid).Error; err != nil {
		t.Fatal(err)
	}
	// Instance still points at the mid-chain node (stale pointer / merge
	// bookkeeping bypass) — resolve must still land on the final target
	// (v3.2-F1 纵深防御).
	dev := models.EdgeDevice{Name: "d", Type: "t", NodeID: "N1", ChannelID: 1, LogicalDeviceID: &mid.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}

	qs, err := ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qs.LogicalID != final.ID {
		t.Errorf("LogicalID = %d, want final target %d", qs.LogicalID, final.ID)
	}
	if qs.Scope == nil {
		t.Fatal("logical scope must be resolved")
	}
	if len(qs.Scope.LogicalIDs) == 0 || qs.Scope.LogicalIDs[0] != final.ID {
		t.Errorf("scope.LogicalIDs = %v, want [%d, ...]", qs.Scope.LogicalIDs, final.ID)
	}
}

func TestResolveDataQueryScope_DeletedInstanceFlaggedButStillResolves(t *testing.T) {
	db := testutil.OpenTestDB(t)

	ld := models.LogicalDevice{IdentityKey: "k:del", Name: "del", DeviceType: "t"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{Name: "doomed", Type: "t", NodeID: "N1", ChannelID: 1, LogicalDeviceID: &ld.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&dev).Error; err != nil {
		t.Fatal(err)
	}

	qs, err := ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !qs.InstanceDeleted {
		t.Error("soft-deleted instance must be flagged InstanceDeleted")
	}
	// Scope still resolves (chart endpoints keep returning full history).
	if qs.LogicalID != ld.ID || qs.Scope == nil {
		t.Errorf("deleted instance scope = (%d, %v), want (%d, non-nil)", qs.LogicalID, qs.Scope, ld.ID)
	}
}

func TestResolveDataQueryScope_MissingInstance(t *testing.T) {
	db := testutil.OpenTestDB(t)

	qs, err := ResolveDataQueryScope(db, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !qs.InstanceDeleted {
		t.Error("missing instance must be flagged InstanceDeleted")
	}
	if qs.LogicalID != 0 || qs.FallbackDeviceID != 42 {
		t.Errorf("scope = (%d, %d), want fallback (0, 42)", qs.LogicalID, qs.FallbackDeviceID)
	}
}

func TestResolveDataQueryScope_DedupOnlyWithDoneIncomingMerge(t *testing.T) {
	db := testutil.OpenTestDB(t)

	target := models.LogicalDevice{IdentityKey: "k:target", Name: "target", DeviceType: "t"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{Name: "d", Type: "t", NodeID: "N1", ChannelID: 1, LogicalDeviceID: &target.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}

	qs, err := ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qs.DedupNeeded {
		t.Error("no merge history: dedup must stay off (前端零改动)")
	}

	// Pending source: migration running → still no dedup.
	pending := models.LogicalDevice{IdentityKey: "k:psrc", Name: "p", DeviceType: "t",
		MergedInto: &target.ID, MergeStatus: strPtr(models.MergeStatusPending)}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	qs, err = ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qs.DedupNeeded {
		t.Error("pending-only merge: dedup must stay off")
	}
	// But the pending source's scope must be UNIONed in (§4.3 v3.2-终审 B1).
	found := false
	for _, id := range qs.Scope.LogicalIDs {
		if id == pending.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("scope.LogicalIDs = %v must include pending source %d", qs.Scope.LogicalIDs, pending.ID)
	}

	// Done source → dedup on.
	done := models.LogicalDevice{IdentityKey: "k:dsrc", Name: "d", DeviceType: "t",
		MergedInto: &target.ID, MergeStatus: strPtr(models.MergeStatusDone)}
	if err := db.Create(&done).Error; err != nil {
		t.Fatal(err)
	}
	qs, err = ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !qs.DedupNeeded {
		t.Error("done incoming merge: dedup must be enabled")
	}
}

func TestApplyShapeDedup_KeepsNewestRowPerSensorTimestamp(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ts := time.Now().UTC().Truncate(time.Second)

	// Same (sensor_name, timestamp) from two sources → keep MAX(id) row only.
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(10), SensorName: "voltage", Value: 11.1, Timestamp: ts})
	db.Create(&models.UnifiedData{DeviceID: 2, LogicalDeviceID: uptr(10), SensorName: "voltage", Value: 12.2, Timestamp: ts})
	// Distinct timestamp stays.
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(10), SensorName: "voltage", Value: 13.3, Timestamp: ts.Add(time.Hour)})
	// Distinct sensor_name at the duplicate timestamp stays (partition key).
	db.Create(&models.UnifiedData{DeviceID: 2, LogicalDeviceID: uptr(10), SensorName: "current", Value: 1.5, Timestamp: ts})

	base := db.Model(&models.UnifiedData{}).Where("logical_device_id = ?", 10)
	var rows []models.UnifiedData
	if err := ApplyShapeDedup(db.Session(&gorm.Session{}), base).Order("timestamp ASC, sensor_name ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("dedup kept %d rows, want 3 (2 voltage + 1 current)", len(rows))
	}
	// The surviving duplicate-timestamp voltage row must be the newest (12.2,
	// written second → higher id).
	if rows[0].SensorName != "current" || rows[1].Value != 12.2 {
		t.Errorf("unexpected dedup survivors: %#v", rows)
	}
	if rows[2].Value != 13.3 {
		t.Errorf("distinct timestamp row lost or wrong: %#v", rows[2])
	}
}

func TestApplyShapeDedup_NoMergeHistoryPathKeepsOriginalRows(t *testing.T) {
	// Without dedup applied the identical dataset returns all 4 rows — the
	// 前端零改动 guarantee: endpoints must NOT wrap queries when there is no
	// done merge. This test pins the raw query side of that contract.
	db := testutil.OpenTestDB(t)
	ts := time.Now().UTC().Truncate(time.Second)
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(11), SensorName: "v", Value: 1, Timestamp: ts})
	db.Create(&models.UnifiedData{DeviceID: 2, LogicalDeviceID: uptr(11), SensorName: "v", Value: 2, Timestamp: ts})

	var rows []models.UnifiedData
	if err := db.Model(&models.UnifiedData{}).Where("logical_device_id = ?", 11).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("raw query returned %d rows, want both kept", len(rows))
	}
}

func TestApplyShapeDedup_BaseWithoutModel(t *testing.T) {
	// The endpoints build the dedup base via db.Where(cond, args...) with NO
	// Model() call; the subquery must still resolve unified_data as its FROM
	// table (GORM's destination-derived table resolution only runs at Find
	// time, which never happens for a subquery). Pins the Model-pin guard in
	// ApplyShapeDedup.
	db := testutil.OpenTestDB(t)
	ts := time.Now().UTC().Truncate(time.Second)
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(20), SensorName: "v", Value: 1, Timestamp: ts})
	db.Create(&models.UnifiedData{DeviceID: 2, LogicalDeviceID: uptr(20), SensorName: "v", Value: 2, Timestamp: ts})

	scope := Scope{LogicalIDs: []uint{20}, InstanceIDs: []uint{1, 2}}
	cond, args := scope.Cond()
	base := db.Where(cond, args...) // NO Model() — matches handler usage
	var rows []models.UnifiedData
	if err := ApplyShapeDedup(db.Session(&gorm.Session{}), base).
		Order("timestamp ASC").Find(&rows).Error; err != nil {
		t.Fatalf("dedup without Model base failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("dedup kept %d rows, want 1", len(rows))
	}
	if rows[0].Value != 2 {
		t.Errorf("survivor value = %v, want newest (2)", rows[0].Value)
	}
}

// TestScopeCond_ComposesWithTimeFilter covers the §六 operator-precedence
// contract: the full OR condition must stay a single unit when a handler
// concatenates " AND timestamp ..." — otherwise the time filter applies only
// to the NULL-logical branch and rows of the logical branch outside the
// window leak into the result (history/historical/historical-batch).
func TestScopeCond_ComposesWithTimeFilter(t *testing.T) {
	db := testutil.OpenTestDB(t)
	inside := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	outside := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// Backfilled row (logical branch) outside the window — must NOT leak.
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(30), SensorName: "v", Value: 1, Timestamp: outside})
	// Pre-backfill NULL row (fallback branch) outside the window — must NOT leak.
	db.Create(&models.UnifiedData{DeviceID: 1, SensorName: "v", Value: 2, Timestamp: outside})
	// Rows inside the window on both branches — must be visible.
	db.Create(&models.UnifiedData{DeviceID: 1, LogicalDeviceID: uptr(30), SensorName: "v", Value: 3, Timestamp: inside})
	db.Create(&models.UnifiedData{DeviceID: 1, SensorName: "v", Value: 4, Timestamp: inside})

	scope := Scope{LogicalIDs: []uint{30}, InstanceIDs: []uint{1}}
	cond, args := scope.Cond()
	lo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	hi := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	var rows []models.UnifiedData
	err := db.Where(cond+" AND timestamp BETWEEN ? AND ?", append(args, lo, hi)...).
		Order("id").Find(&rows).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("composed condition returned %d rows, want exactly the 2 in-window rows: %#v", len(rows), rows)
	}
	if rows[0].Value != 3 || rows[1].Value != 4 {
		t.Errorf("unexpected survivors: %#v", rows)
	}
}

func uptr(v uint) *uint { return &v }
