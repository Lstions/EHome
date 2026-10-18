package database

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// TestNodeIDLegacyFromBaseMAC pins the derivation rule that the migration relies
// on. If ESP-IDF ever changes how the USB-JTAG MAC is synthesised from the base
// MAC, this test is where that shows up -- before the migration silently stops
// matching anything (a no-op migration looks exactly like "nothing to do").
func TestNodeIDLegacyFromBaseMAC(t *testing.T) {
	cases := []struct {
		base, want string
	}{
		// The real board measured on 2026-10-18:
		// base MAC f0:f5:bd:02:f3:5c, old firmware reported F0F5BDFFFE02.
		{"F0F5BD02F35C", "F0F5BDFFFE02"},
		// Second real board (S3P), same rule applied:
		// base[0:6]="30EDA0" + "FFFE" + base[6:8]="A9" -> "30EDA0FFFEA9".
		// (This board never ran the old firmware, so no such row exists -- the
		// case exists to pin the arithmetic, not to match real data.)
		{"30EDA0A9A808", "30EDA0FFFEA9"},
		// Non-MAC ids must be refused rather than mangled.
		{"sim-20261004-74c3", ""},
		{"", ""},
		{"f0f5bd02f35c", ""},   // lowercase is not the stored form
		{"SHORT", ""},
	}
	for _, c := range cases {
		if got := nodeIDLegacyFromBaseMAC(c.base); got != c.want {
			t.Errorf("nodeIDLegacyFromBaseMAC(%q)=%q want %q", c.base, got, c.want)
		}
	}
}

// TestMigrateNodeIDFromJTAGMACRePointsConfig is the regression test for the
// silent "device online but no data" failure measured on real hardware
// 2026-10-18: the live node had zero channels while the retired legacy id still
// owned them, so the server pushed an empty manifest.
//
// Reverse verification: delete the "channels"/"edge_devices" loop in
// migrateOneNodeID and this test goes red (channel stays on the orphan id).
func TestMigrateNodeIDFromJTAGMACRePointsConfig(t *testing.T) {
	db := testutil.OpenTestDB(t)

	orphan := models.Node{NodeID: "F0F5BDFFFE02", Name: "old", Status: "offline"}
	live := models.Node{NodeID: "F0F5BD02F35C", Name: "new", Status: "online"}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: orphan.NodeID, HardwareType: "UART", HardwareID: "UART1", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}

	res, err := MigrateNodeIDFromJTAGMAC(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Migrated != 1 {
		t.Fatalf("migrated=%d want 1 (scanned=%d skipped=%d errors=%d)",
			res.Migrated, res.Scanned, res.Skipped, res.Errors)
	}

	// The channel must now belong to the live node, or the server keeps
	// building an empty manifest.
	var stored models.Channel
	if err := db.First(&stored, ch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.NodeID != live.NodeID {
		t.Fatalf("channel node_id=%q want %q: without this the device receives a " +
			"0-channel manifest and never reports data", stored.NodeID, live.NodeID)
	}

	// The orphan must be retired, not deleted: history stays queryable.
	var retired models.Node
	if err := db.Unscoped().Where("node_id = ?", orphan.NodeID).First(&retired).Error; err != nil {
		t.Fatalf("orphan row must survive unscoped (soft delete): %v", err)
	}
	if !retired.DeletedAt.Valid {
		t.Fatal("orphan node must be soft-deleted so a re-run cannot match it again")
	}
}

// TestMigrateNodeIDFromJTAGMACIsIdempotent pins the second-run no-op: an
// AutoMigrate step runs on EVERY boot, so a migration that keeps matching would
// rewrite rows forever.
func TestMigrateNodeIDFromJTAGMACIsIdempotent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	orphan := models.Node{NodeID: "F0F5BDFFFE02", Name: "old", Status: "offline"}
	live := models.Node{NodeID: "F0F5BD02F35C", Name: "new", Status: "online"}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}

	first, err := MigrateNodeIDFromJTAGMAC(db)
	if err != nil {
		t.Fatal(err)
	}
	if first.Migrated != 1 {
		t.Fatalf("first run migrated=%d want 1", first.Migrated)
	}
	second, err := MigrateNodeIDFromJTAGMAC(db)
	if err != nil {
		t.Fatal(err)
	}
	if second.Migrated != 0 {
		t.Fatalf("second run migrated=%d want 0 (must be idempotent; every boot runs this)", second.Migrated)
	}
}

// TestMigrateNodeIDRefusesTwoLiveNodes pins the safety valve: two ONLINE boards
// whose ids differ by the JTAG substitution are two real devices, not a rename.
// Collapsing them would silently merge two homes.
func TestMigrateNodeIDRefusesTwoLiveNodes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	orphan := models.Node{NodeID: "F0F5BDFFFE02", Name: "old", Status: "online"}
	live := models.Node{NodeID: "F0F5BD02F35C", Name: "new", Status: "online"}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}

	res, err := MigrateNodeIDFromJTAGMAC(db)
	if err != nil {
		t.Fatal(err)
	}
	if res.Migrated != 0 {
		t.Fatalf("migrated=%d want 0 when both nodes are online (would merge two real devices)", res.Migrated)
	}
	var still models.Node
	if err := db.Unscoped().Where("node_id = ?", orphan.NodeID).First(&still).Error; err != nil {
		t.Fatalf("orphan must be untouched: %v", err)
	}
	if still.DeletedAt.Valid {
		t.Fatal("refused migration must not soft-delete anything")
	}
}
