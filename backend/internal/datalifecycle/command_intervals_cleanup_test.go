package datalifecycle

import (
	"encoding/json"
	"testing"

	"gorm.io/gorm"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// cleanupRegistry mirrors the production CLI wiring: all built-in drivers.
func cleanupRegistry() *drivers.Registry {
	r := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(r)
	return r
}

func createCleanupDevice(t *testing.T, db *gorm.DB, devType string, stored any) *models.EdgeDevice {
	t.Helper()
	dev := models.EdgeDevice{Name: "cleanup-" + devType, NodeID: "NODE001", ChannelID: 1, Type: devType}
	if stored != nil {
		b, err := json.Marshal(stored)
		if err != nil {
			t.Fatalf("marshal stored intervals: %v", err)
		}
		dev.CommandIntervals = b
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	return &dev
}

// rawIntervalsColumn reads the physical command_intervals column so the test
// can distinguish NULL from an empty map.
func rawIntervalsColumn(t *testing.T, db *gorm.DB, devID uint) *string {
	t.Helper()
	var raw *string
	err := db.Raw("SELECT command_intervals FROM edge_devices WHERE id = ?", devID).Scan(&raw).Error
	if err != nil {
		t.Fatalf("read raw command_intervals for device %d: %v", devID, err)
	}
	return raw
}

func storedIntervals(t *testing.T, db *gorm.DB, devID uint) map[string]int {
	t.Helper()
	var dev models.EdgeDevice
	if err := db.First(&dev, devID).Error; err != nil {
		t.Fatalf("reload device %d: %v", devID, err)
	}
	var m map[string]int
	if len(dev.CommandIntervals) > 0 {
		if err := json.Unmarshal(dev.CommandIntervals, &m); err != nil {
			t.Fatalf("unmarshal intervals for device %d: %v (%s)", devID, err, dev.CommandIntervals)
		}
	}
	return m
}

func TestCleanup_NullStaysNull(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := createCleanupDevice(t, db, "jiabaida_bms", nil)
	// The column default ('{}') fills nil on INSERT — force the legacy NULL
	// shape the cleanup must leave untouched.
	if err := db.Exec("UPDATE edge_devices SET command_intervals = NULL WHERE id = ?", dev.ID).Error; err != nil {
		t.Fatalf("force NULL: %v", err)
	}

	report, err := CleanupCommandIntervals(db, cleanupRegistry())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if report.DevicesCleaned != 0 || report.KeysRemoved != 0 {
		t.Fatalf("NULL device must not be cleaned: %+v", report)
	}
	if raw := rawIntervalsColumn(t, db, dev.ID); raw != nil {
		t.Fatalf("expected command_intervals to stay NULL, got %q", *raw)
	}
}

func TestCleanup_RemovesNonSchedulableKeepsSchedulable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// jiabaida_bms: read_basic_info is schedulable; close_discharge_mos is a
	// legacy dirty key absent from the driver's (schedulable-only) templates.
	dev := createCleanupDevice(t, db, "jiabaida_bms",
		map[string]int{"read_basic_info": 3000, "close_discharge_mos": 100})

	report, err := CleanupCommandIntervals(db, cleanupRegistry())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if report.DevicesCleaned != 1 || report.KeysRemoved != 1 {
		t.Fatalf("expected 1 device cleaned / 1 key removed, got %+v", report)
	}
	got := storedIntervals(t, db, dev.ID)
	if len(got) != 1 || got["read_basic_info"] != 3000 {
		t.Fatalf("expected only read_basic_info=3000, got %v", got)
	}
}

func TestCleanup_EmptyAfterCleanBecomesNull(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := createCleanupDevice(t, db, "jiabaida_bms",
		map[string]int{"close_discharge_mos": 100})

	if _, err := CleanupCommandIntervals(db, cleanupRegistry()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if raw := rawIntervalsColumn(t, db, dev.ID); raw != nil {
		t.Fatalf("expected NULL after full cleanup, got %q", *raw)
	}
}

func TestCleanup_DriverlessTypeRemovesAll(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := createCleanupDevice(t, db, "ghost", map[string]int{"a": 1})

	report, err := CleanupCommandIntervals(db, cleanupRegistry())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if report.KeysRemoved != 1 {
		t.Fatalf("expected driverless device key removed: %+v", report)
	}
	if raw := rawIntervalsColumn(t, db, dev.ID); raw != nil {
		t.Fatalf("expected NULL for driverless type, got %q", *raw)
	}
}

func TestCleanup_Idempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	devA := createCleanupDevice(t, db, "jiabaida_bms",
		map[string]int{"read_basic_info": 3000, "close_discharge_mos": 100})
	devB := createCleanupDevice(t, db, "jiabaida_bms",
		map[string]int{"close_discharge_mos": 100})
	devC := createCleanupDevice(t, db, "jiabaida_bms",
		map[string]int{"read_basic_info": 3000})

	if _, err := CleanupCommandIntervals(db, cleanupRegistry()); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	snapshotA := storedIntervals(t, db, devA.ID)
	snapshotB := rawIntervalsColumn(t, db, devB.ID)
	snapshotC := storedIntervals(t, db, devC.ID)

	report2, err := CleanupCommandIntervals(db, cleanupRegistry())
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if report2.DevicesCleaned != 0 || report2.KeysRemoved != 0 {
		t.Fatalf("second pass must be a no-op, got %+v", report2)
	}
	if b := rawIntervalsColumn(t, db, devB.ID); b != nil || snapshotB != nil {
		t.Fatalf("NULL state not stable: before=%v after=%v", snapshotB, b)
	}
	if diffIntervals(snapshotA, storedIntervals(t, db, devA.ID)) {
		t.Fatal("device A intervals changed between passes")
	}
	if diffIntervals(snapshotC, storedIntervals(t, db, devC.ID)) {
		t.Fatal("device C intervals changed between passes")
	}
}

func diffIntervals(a, b map[string]int) bool {
	if len(a) != len(b) {
		return true
	}
	for k, v := range a {
		if b[k] != v {
			return true
		}
	}
	return false
}
