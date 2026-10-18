package database

import (
	"fmt"
	"time"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// nodeIDRow is the minimal node projection this migration needs.
type nodeIDRow struct {
	ID     uint
	NodeID string
	Status string
}

// MigrateNodeIDFromJTAGMACResult records one node-id reconciliation run.
type MigrateNodeIDFromJTAGMACResult struct {
	Scanned  int // distinct current node_ids examined
	Migrated int // legacy ids whose rows were re-pointed to the current id
	Skipped  int // current ids left alone (no orphan / ambiguity)
	Errors   int
}

// nodeIDLegacyFromBaseMAC derives the node_id that the pre-2026-10-06 firmware
// would have produced for a given base-MAC node_id.
//
// The two firmware derivations, side by side (ESP32-C6, base MAC
// f0:f5:bd:02:f3:5c):
//
//	OLD esp_efuse_mac_get_default() -> f0:f5:bd:ff:fe:02:f3:5c (USB-JTAG MAC)
//	NEW esp_read_mac(WIFI_STA)      -> f0:f5:bd:02:f3:5c       (base MAC)
//
// ESP-IDF does not store the JTAG MAC separately: it synthesises it from the
// base MAC by inserting the fixed OUI-extension bytes FF FE after the first
// three bytes (mac_addr.c: the USB-Serial-JTAG interface MAC is derived, and
// FF FE is the locally-administered marker IDF inserts). Both observed ids are
// consistent with that one rule:
//
//	base F0 F5 BD | 02 F3 5C
//	JTAG F0 F5 BD | FF FE | 02 F3 5C
//
// The old node_id took the first six bytes of the JTAG MAC, i.e.
// base[0:3] + FF FE, which in the 12-hex-char node_id renders as
// base[0:6] + "FFFE". That is exact, not a heuristic: verified against the
// real device on 2026-10-18 (base F0F5BD02F35C -> legacy F0F5BDFFFE02, which is
// precisely the orphan row in the database).
//
// Returns "" when the input cannot be a base-MAC id, so callers never rewrite
// names like "sim-20261004-xxx" or hand-picked ids.
func nodeIDLegacyFromBaseMAC(nodeID string) string {
	if len(nodeID) != 12 {
		return ""
	}
	for _, r := range nodeID {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			return ""
		}
	}
	// Byte layout, verified with esptool on the real board 2026-10-18:
	//
	//	BASE MAC (6 B): f0 f5 bd | 02 f3 5c
	//	USB-JTAG MAC (8 B): f0 f5 bd | ff fe | 02 f3 5c
	//	  i.e. JTAG = base[0:3] || FF FE || base[3:6]
	//
	// The legacy node_id took the first SIX bytes of the JTAG MAC, which is
	// base[0:3] || FF FE || base[3]  ->  in the 12-char hex id that is
	// base[0:6] + "FFFE" + base[6:8].
	//
	// The trailing base[6:8] is essential: dropping it yields a 10-char id that
	// can never match. TestNodeIDLegacyFromBaseMAC pins the exact 12-char result.
	return nodeID[0:6] + "FFFE" + nodeID[6:8]
}

// MigrateNodeIDFromJTAGMAC reconciles node rows created by the old node-id
// derivation with the current one.
//
// # The failure this fixes (measured on real hardware 2026-10-18)
//
// commit 54dae005 changed how node_id is derived, because two physically
// different C6 boards both registered as F0F5BDFFFE02 (the old derivation used
// the USB-JTAG MAC, which identifies the chip family rather than the unit).
// The firmware fix is right, but it RENAMES the device, and nothing migrated the
// rows that were already in the database. The observable end state:
//
//	F0F5BD02F35C  online   channels=0   <- the live board, config-less
//	F0F5BDFFFE02  offline  channels=2   <- the orphan, still owns the config
//
// The server builds the manifest from the ONLINE node, finds no channels, and
// pushes a 0-channel manifest. The device applies it, polls nothing, and reports
// no data -- while the UI shows the node as online. This is the exact shape of a
// silent failure: nothing errors, the link is healthy, and the data is simply
// absent.
//
// # What "migrate" means here
//
// Re-point the rows that hang off the legacy id (channels, edge_devices,
// unified_data/device_data ownership is NOT rewritten -- historical samples keep
// their original attribution) onto the current id, then retire the orphan node
// row. Nothing is deleted: the orphan is soft-deleted via deleted_at so its
// history stays queryable and the operation is reversible.
//
// # Safety
//
//   - Idempotent: once the legacy row is soft-deleted there is nothing left to
//     match, so a second run is a no-op.
//   - Conservative: acts only when the legacy id is EXACTLY what the old
//     derivation produces for a currently-registered node, and only when the
//     current node exists and is not itself an orphan.
//   - Never merges two live devices: if both ids are currently online the run is
//     skipped and logged, because that means two real boards, not a rename.
func MigrateNodeIDFromJTAGMAC(db *gorm.DB) (*MigrateNodeIDFromJTAGMACResult, error) {
	res := &MigrateNodeIDFromJTAGMACResult{}
	if db == nil {
		return nil, fmt.Errorf("migrate node_id: database is nil")
	}
	if !db.Migrator().HasTable("nodes") {
		return res, nil
	}

	var currents []nodeIDRow
	if err := db.Table("nodes").
		Select("id, node_id, status").
		Where("deleted_at IS NULL").
		Order("id ASC").
		Scan(&currents).Error; err != nil {
		return nil, fmt.Errorf("migrate node_id: list nodes: %w", err)
	}
	res.Scanned = len(currents)
	if len(currents) == 0 {
		return res, nil
	}

	// Index the live ids so "does the orphan still have a home?" is one lookup.
	live := make(map[string]nodeIDRow, len(currents))
	for _, n := range currents {
		live[n.NodeID] = n
	}

	for _, cur := range currents {
		legacy := nodeIDLegacyFromBaseMAC(cur.NodeID)
		if legacy == "" {
			res.Skipped++
			continue
		}
		var orphan nodeIDRow
		err := db.Table("nodes").
			Select("id, node_id, status").
			Where("node_id = ? AND deleted_at IS NULL", legacy).
			Take(&orphan).Error
		if err == gorm.ErrRecordNotFound {
			res.Skipped++
			continue
		}
		if err != nil {
			res.Errors++
			logger.Warnf("[migrate] node_id: lookup %s failed: %v", legacy, err)
			continue
		}

		// Two live boards would be a genuine collision, not a rename.
		if orphan.Status == "online" && cur.Status == "online" {
			res.Skipped++
			logger.Warnf("[migrate] node_id: both %s and %s are online -- treating as " +
				"two devices, NOT rewriting (manual reconciliation required)",
				orphan.NodeID, cur.NodeID)
			continue
		}

		if err := migrateOneNodeID(db, orphan, cur.NodeID); err != nil {
			res.Errors++
			logger.Warnf("[migrate] node_id: %s -> %s failed: %v", orphan.NodeID, cur.NodeID, err)
			continue
		}
		res.Migrated++
		logger.Infof("[migrate] node_id: re-pointed %s -> %s (orphan was %s)",
			orphan.NodeID, cur.NodeID, orphan.Status)
	}
	return res, nil
}

// migrateOneNodeID moves one orphan node's dependants onto targetNodeID and
// soft-deletes the orphan, in a single transaction.
//
// The tables are enumerated explicitly rather than discovered, because a table
// that gains a node_id column later must be a conscious decision here -- a
// wildcard sweep could silently rewrite audit data that should stay attributed.
func migrateOneNodeID(db *gorm.DB, orphan nodeIDRow, targetNodeID string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// Channels and edge devices are the config carrier: they MUST move, or the
		// live node keeps receiving an empty manifest.
		for _, table := range []string{"channels", "edge_devices"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			if err := tx.Table(table).
				Where("node_id = ?", orphan.NodeID).
				Update("node_id", targetNodeID).Error; err != nil {
				return fmt.Errorf("re-point %s: %w", table, err)
			}
		}

		// Retire the orphan without deleting it: soft-delete keeps every historical
		// sample queryable and makes this step reversible.
		// deleted_at is written from Go, not SQL: NOW() exists on PostgreSQL but
		// not on SQLite, and this migration runs on both (unit tests use SQLite).
		if err := tx.Table("nodes").
			Where("id = ?", orphan.ID).
			Updates(map[string]interface{}{
				"deleted_at": time.Now().UTC(),
				"status":     "offline",
			}).Error; err != nil {
			return fmt.Errorf("retire orphan %s: %w", orphan.NodeID, err)
		}
		return nil
	})
}
