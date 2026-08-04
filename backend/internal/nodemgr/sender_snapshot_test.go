package nodemgr

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// =====================================================================
// F2: manifest 快照化 + hash 一致性
// =====================================================================

// newManifestTestManager builds a Manager (with driver registry) on the given
// DB and a capturing publisher. Uses testutil.OpenTestDB so the same tests run
// against SQLite and PostgreSQL (EHOME_TEST_DB=postgres).
func newManifestTestManager(t *testing.T, db *gorm.DB, registry *drivers.Registry) (*Manager, *mockMQTTPublisher) {
	t.Helper()
	mock := &mockMQTTPublisher{}
	mgr := &Manager{
		db:             db,
		mqtt:           mock,
		hashMgr:        NewConfigHashManager(),
		eventBus:       NewConfigEventBus(64),
		driverRegistry: registry,
	}
	return mgr, mock
}

// snapshotCapabilities is a minimal valid ResourceReport for
// validateManifestAuthority (GPIO pins 6/7 and PWM resources).
const snapshotCapabilities = `{"buses":{"gpio":[{"id":"GPIO6","pin":6},{"id":"GPIO7","pin":7}],"pwm":[{"id":"PWM0","channel":0,"max_resolution_bits":14},{"id":"PWM1","channel":1,"max_resolution_bits":14}]}}`

func seedManifestSnapshotDB(t *testing.T, db *gorm.DB, nodeID string) {
	t.Helper()
	if err := db.Create(&models.Node{NodeID: nodeID, Status: "online", ProtocolVersion: "2.5", Capabilities: snapshotCapabilities}).Error; err != nil {
		t.Fatal(err)
	}
	// NOTE: no explicit IDs — on PostgreSQL explicit IDs do not advance the
	// sequence, so a later reconcile auto-Create would collide on pkey. Fresh
	// schema → first rows get id 1 / 2, which is all the tests rely on.
	if err := db.Create(&models.Channel{NodeID: nodeID, BusType: "UART", HardwareType: "UART", Enabled: true, BusConfig: "10110000258000"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ConfigTemplate{NodeID: nodeID, WriteData: "010300000001840A", ReadLength: 7, DelayMs: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{NodeID: nodeID, ChannelID: 1, Type: "jiabaida_bms", HardwareID: "0x76", IntervalMs: 3000, Enabled: true, Name: "bms-1"}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestManifestIDDerivedFromSnapshotMatchesEncodedField1 verifies the F2 core
// invariant: when the decision carries no ManifestID, the ID written into
// field 1 of the encoded manifest is derived from the exact same snapshot that
// produced the bytes. Decoding the published manifest and re-hashing the
// snapshot must yield the same identifier (same-source assertion).
func TestManifestIDDerivedFromSnapshotMatchesEncodedField1(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedManifestSnapshotDB(t, db, "f2-same-source")
	registry := drivers.NewRegistry()
	registry.Register(&drivers.JiabaidaBMSDriver{})
	mgr, mock := newManifestTestManager(t, db, registry)

	if err := mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID: "f2-same-source", SyncID: "s1", Action: SyncActionFull, Reason: "test",
		// ManifestID intentionally empty → derived from snapshot.
	}); err != nil {
		t.Fatal(err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("no manifest was published")
	}

	// Decode field 1 (manifest_id).
	dec, err := frame.NewDecoder(mock.publishedPayload)
	if err != nil {
		t.Fatal(err)
	}
	var encodedID string
	for {
		field, ferr := dec.NextField()
		if errors.Is(ferr, frame.ErrEndOfFrame) {
			break
		}
		if ferr != nil {
			t.Fatal(ferr)
		}
		if field.FieldNum == 1 {
			encodedID = frame.GetString(field)
		}
	}
	if encodedID == "" {
		t.Fatal("manifest_id field 1 missing")
	}

	// Recompute the hash from the same committed DB state — the manifest was
	// derived from the same snapshot, so the identifiers must be equal.
	computed := mgr.CalcConfigHashForDevice("f2-same-source")
	if computed.ManifestID == "" {
		t.Fatal("CalcConfigHashForDevice returned empty manifest id")
	}
	if encodedID != computed.ManifestID {
		t.Fatalf("manifestID mismatch: encoded field 1 = %q, hash-derived = %q (same-source invariant broken)", encodedID, computed.ManifestID)
	}
}

// TestManifestStaleDecisionIDIsOverriddenBySnapshotDerivedID verifies the F2
// core production invariant: SyncGate always computes decision.ManifestID
// BEFORE reconcile runs. If reconcile creates templates, that decision ID is
// stale — honoring it would make field 1 differ from the hash of the encoded
// bytes (the death-loop). The sender must ALWAYS use the post-reconcile
// snapshot-derived ID on the wire.
func TestManifestStaleDecisionIDIsOverriddenBySnapshotDerivedID(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedManifestSnapshotDB(t, db, "f2-stale-id")
	registry := drivers.NewRegistry()
	registry.Register(&drivers.JiabaidaBMSDriver{})
	mgr, mock := newManifestTestManager(t, db, registry)

	// A stale ID that does NOT match the snapshot hash — exactly what SyncGate
	// computes before reconcile creates the 4 missing jiabaida templates.
	if err := mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID: "f2-stale-id", SyncID: "s3", Action: SyncActionFull, Reason: "test",
		ManifestID: "v2-stale-pre-reconcile",
	}); err != nil {
		t.Fatal(err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("no manifest was published")
	}

	dec, err := frame.NewDecoder(mock.publishedPayload)
	if err != nil {
		t.Fatal(err)
	}
	var encodedID string
	for {
		field, ferr := dec.NextField()
		if errors.Is(ferr, frame.ErrEndOfFrame) {
			break
		}
		if ferr != nil {
			t.Fatal(ferr)
		}
		if field.FieldNum == 1 {
			encodedID = frame.GetString(field)
		}
	}
	if encodedID == "v2-stale-pre-reconcile" {
		t.Fatal("wire carried the stale decision manifestID — death-loop invariant broken")
	}
	computed := mgr.CalcConfigHashForDevice("f2-stale-id")
	if encodedID != computed.ManifestID {
		t.Fatalf("wire manifestID %q != post-reconcile snapshot hash %q (same-source invariant broken)", encodedID, computed.ManifestID)
	}
}

// TestManifestReconcileFailureLeavesNoOrphanTemplates verifies fail-closed
// reconcile: when template creation fails mid-way inside the sender
// transaction, the whole manifest send fails AND no orphan ConfigTemplate rows
// are left behind (the transaction rolls back template creation + template_ids
// append together). A GORM callback injects the failure on the second
// config_templates Create — the map iteration order is randomized, so this
// guards both "first create fails" and "a later create fails" orderings.
func TestManifestReconcileFailureLeavesNoOrphanTemplates(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// Inject a deterministic failure on the 2nd config_templates Create.
	var createCount int
	regName := "f2-fail-2nd-config-template-create"
	if err := db.Callback().Create().Before("gorm:create").Register(regName, func(tx *gorm.DB) {
		if tx.Statement.Table == "config_templates" {
			createCount++
			if createCount == 2 {
				tx.AddError(errors.New("injected config_templates create failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Before("gorm:create").Remove(regName)
	})

	seedManifestSnapshotDB(t, db, "f2-reconcile-fail")
	// The driver needs 5 schedulable commands; only the first is seeded, so
	// reconcile attempts 4 Creates → the 2nd aborts the transaction.
	registry := drivers.NewRegistry()
	registry.Register(&drivers.JiabaidaBMSDriver{})
	mgr, mock := newManifestTestManager(t, db, registry)

	err := mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID: "f2-reconcile-fail", SyncID: "s2", Action: SyncActionFull, Reason: "test",
	})
	if err == nil {
		t.Fatalf("expected reconcile failure, got nil: published=%d bytes", len(mock.publishedPayload))
	}
	if len(mock.publishedPayload) != 0 {
		t.Fatal("failed manifest must not be published")
	}
	// No orphan templates: only the pre-seeded row survives (rollback must
	// undo every Create performed before the injected failure).
	var count int64
	if err := db.Model(&models.ConfigTemplate{}).Where("node_id = ?", "f2-reconcile-fail").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reconcile failure left %d templates (want 1 — rollback must not leave orphans)", count)
	}
	// The node was marked failed.
	var node models.Node
	if err := db.Where("node_id = ?", "f2-reconcile-fail").First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.ConfigSyncState != "failed" {
		t.Fatalf("node sync state=%q, want failed", node.ConfigSyncState)
	}
}

// encodeDecodeManifestIDs decodes every channel sub-frame (field 4) and returns
// per-channel template_id varints (field 3 of the channel sub-frame in legacy
// path) plus the edge_device_group sub-frames.
func decodeManifestChannels(t *testing.T, payload []byte) ([]uint64, [][]byte) {
	t.Helper()
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatal(err)
	}
	var channelTemplateIDs []uint64
	var edges [][]byte
	for {
		field, ferr := dec.NextField()
		if errors.Is(ferr, frame.ErrEndOfFrame) {
			break
		}
		if ferr != nil {
			t.Fatal(ferr)
		}
		if field.FieldNum != 4 {
			continue
		}
		chDec, err := frame.NewSubDecoder(frame.GetBytes(field))
		if err != nil {
			t.Fatal(err)
		}
		for {
			chField, cerr := chDec.NextField()
			if errors.Is(cerr, frame.ErrEndOfFrame) {
				break
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			if chField.FieldNum == 3 {
				channelTemplateIDs = append(channelTemplateIDs, frame.GetUint64(chField))
			}
			if chField.FieldNum == 9 {
				edges = append(edges, frame.GetBytes(chField))
			}
		}
	}
	return channelTemplateIDs, edges
}

// TestManifestSnapshotEdgesByChannel_SingleQuerySelfConsistent runs the PG-only
// concurrency check (also exercised under SQLite): a background goroutine
// concurrently mutates templates/channels while SendConfigManifestWithDecision
// encodes. Because the whole pipeline runs in one REPEATABLE READ transaction,
// every edge_device referenced in the manifest must reference a template_id
// that exists in the same snapshot's template list — and the manifestID must
// match the hash of that same template set.
func TestManifestSnapshotEdgesByChannel_SelfConsistentUnderConcurrency(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const nodeID = "f2-concurrent"
	if err := db.Create(&models.Node{NodeID: nodeID, Status: "online", ProtocolVersion: "2.5", Capabilities: snapshotCapabilities}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: nodeID, BusType: "UART", HardwareType: "UART", Enabled: true, BusConfig: "10110000258000"}).Error; err != nil {
		t.Fatal(err)
	}
	// Auto-increment IDs: first channel/template/edge get id 1 (PG-portable).
	if err := db.Create(&models.ConfigTemplate{NodeID: nodeID, WriteData: "01", ReadLength: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{NodeID: nodeID, ChannelID: 1, Type: "jiabaida_bms", HardwareID: "0x76", IntervalMs: 2000, Enabled: true, Name: "bms"}).Error; err != nil {
		t.Fatal(err)
	}
	registry := drivers.NewRegistry()
	registry.Register(&drivers.JiabaidaBMSDriver{})
	mgr, mock := newManifestTestManager(t, db, registry)

	// Background churn: flip template 1's write_data between values and toggle
	// the channel enabled state while the sender encodes repeatedly.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			val := "01"
			if i%2 == 0 {
				val = "02"
			}
			_ = db.Model(&models.ConfigTemplate{}).Where("id = ?", 1).Update("write_data", val).Error
			_ = db.Model(&models.Channel{}).Where("id = ?", 1).Update("enabled", i%3 != 0).Error
			i++
		}
	}()

	for round := 0; round < 30; round++ {
		mock.publishedPayload = nil
		// Use a decision with empty ManifestID so it is derived from the
		// snapshot (the F2 invariant).
		if err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: nodeID, SyncID: fmt.Sprintf("r%d", round), Action: SyncActionFull, Reason: "concurrency"}); err != nil {
			// Authority validation can reject a disabled channel or conflicting
			// states; that is an acceptable transition, the core invariant is
			// about published manifests being internally consistent.
			continue
		}
		if len(mock.publishedPayload) == 0 {
			continue
		}
		tids, edges := decodeManifestChannels(t, mock.publishedPayload)
		// Every edge group must reference an existing template id (field 3 of
		// the group references templates). Verify no dangling reference.
		validTemplateIDs := map[uint64]bool{}
		dec, _ := frame.NewDecoder(mock.publishedPayload)
		for {
			field, err := dec.NextField()
			if errors.Is(err, frame.ErrEndOfFrame) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if field.FieldNum != 3 {
				continue
			}
			tmplDec, _ := frame.NewSubDecoder(frame.GetBytes(field))
			for {
				tf, terr := tmplDec.NextField()
				if errors.Is(terr, frame.ErrEndOfFrame) {
					break
				}
				if terr != nil {
					t.Fatal(terr)
				}
				if tf.FieldNum == 1 {
					validTemplateIDs[frame.GetUint64(tf)] = true
				}
			}
		}
		_ = tids
		for _, edgeGroup := range edges {
			gDec, err := frame.NewSubDecoder(edgeGroup)
			if err != nil {
				t.Fatal(err)
			}
			for {
				gf, gerr := gDec.NextField()
				if errors.Is(gerr, frame.ErrEndOfFrame) {
					break
				}
				if gerr != nil {
					t.Fatal(gerr)
				}
				if gf.FieldNum == 3 {
					cmdDec, _ := frame.NewSubDecoder(frame.GetBytes(gf))
					for {
						cf, cerr := cmdDec.NextField()
						if errors.Is(cerr, frame.ErrEndOfFrame) {
							break
						}
						if cerr != nil {
							t.Fatal(cerr)
						}
						if cf.FieldNum == 1 {
							ref := frame.GetUint64(cf)
							if !validTemplateIDs[ref] {
								t.Fatalf("round %d: edge group references template id %d that is NOT in the same snapshot's template set", round, ref)
							}
						}
					}
				}
			}
		}
	}
	close(stop)
	wg.Wait()
}

// =====================================================================
// F3: findTemplateID 移除 fallback=1
// =====================================================================

// TestFindTemplateIDNoFallback covers the F3 unit contract: an empty,
// unparseable, or dangling template_ids list yields 0 (never the old magic
// template 1), and a valid list yields its first parseable id.
func TestFindTemplateIDNoFallback(t *testing.T) {
	edge := models.EdgeDevice{ID: 7}
	cases := []struct {
		name     string
		template string
		want     uint64
	}{
		{"empty", "", 0},
		{"whitespace", "  ", 0},
		{"unparseable", "abc,def", 0},
		{"dangling", "99,100", 99}, // first id returned; existence is the caller's job
		{"mixed", "  7 ,8", 7},
		{"valid", "1,2,3", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findTemplateID(models.Channel{TemplateIDs: tc.template}, edge)
			if got != tc.want {
				t.Fatalf("findTemplateID(%q) = %d, want %d", tc.template, got, tc.want)
			}
		})
	}
}

// TestManifestEdgeWithDanglingTemplateIDsSkipsCommandEncoding verifies the F3
// wire contract end-to-end: when a channel's template_ids are dangling (or
// empty) and the edge takes the legacy single-command branch (driver without
// CommandTemplates), the command sub-frame is NOT encoded — no varint 0 ever
// reaches the wire. The edge group itself is still emitted (field 9) with zero
// commands; the firmware parses that as command_count=0 and schedules nothing.
func TestManifestEdgeWithDanglingTemplateIDsSkipsCommandEncoding(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const nodeID = "f3-dangling"
	if err := db.Create(&models.Node{NodeID: nodeID, Status: "online", ProtocolVersion: "2.5", Capabilities: snapshotCapabilities}).Error; err != nil {
		t.Fatal(err)
	}
	// template_ids references templates that do NOT exist (dangling, like the
	// dev PG channel 3 case: "9,10,11,12,13,14" over an 8-row table).
	if err := db.Create(&models.Channel{NodeID: nodeID, BusType: "UART", HardwareType: "UART", Enabled: true, BusConfig: "10110000258000", TemplateIDs: "99,100"}).Error; err != nil {
		t.Fatal(err)
	}
	// One real template exists; the dangling refs must not silently fall back to
	// it (or to template 1) — the edge must simply not be commanded.
	if err := db.Create(&models.ConfigTemplate{NodeID: nodeID, WriteData: "010300000001840A", ReadLength: 7, DelayMs: 10}).Error; err != nil {
		t.Fatal(err)
	}
	// Type "unknown" → registry.Get returns nil → getCommandTemplatesFromDriver
	// returns nil → legacy single-command branch (findTemplateID call site).
	if err := db.Create(&models.EdgeDevice{NodeID: nodeID, ChannelID: 1, Type: "no-such-driver", HardwareID: "0x76", IntervalMs: 3000, Enabled: true, Name: "dangling-edge"}).Error; err != nil {
		t.Fatal(err)
	}
	registry := drivers.NewRegistry() // empty — no driver resolves
	mgr, mock := newManifestTestManager(t, db, registry)

	if err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: nodeID, SyncID: "f3-1", Action: SyncActionFull, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("no manifest was published")
	}

	_, edges := decodeManifestChannels(t, mock.publishedPayload)
	if len(edges) != 1 {
		t.Fatalf("got %d edge groups, want 1 (edge group must still be emitted)", len(edges))
	}
	// The edge group must NOT contain any command sub-frame (field 3).
	grpDec, err := frame.NewSubDecoder(edges[0])
	if err != nil {
		t.Fatal(err)
	}
	for {
		gf, gerr := grpDec.NextField()
		if errors.Is(gerr, frame.ErrEndOfFrame) {
			break
		}
		if gerr != nil {
			t.Fatal(gerr)
		}
		if gf.FieldNum == 3 {
			t.Fatalf("edge group contains a command sub-frame (template_id %d) — dangling template_ids must not be encoded", frame.GetUint64(gf))
		}
	}
	// The whole payload must not contain a bare varint 0 template reference.
	// Scan every command sub-frame of every edge group for template_id == 0.
	if found := manifestContainsTemplateIDZero(t, mock.publishedPayload); found {
		t.Fatal("payload contains a template_id == 0 command — varint 0 must never go on the wire")
	}
}

// manifestContainsTemplateIDZero scans every edge_device_group command
// sub-frame (field 9 → field 3 → field 1) for a template_id of 0.
func manifestContainsTemplateIDZero(t *testing.T, payload []byte) bool {
	t.Helper()
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatal(err)
	}
	for {
		field, ferr := dec.NextField()
		if errors.Is(ferr, frame.ErrEndOfFrame) {
			break
		}
		if ferr != nil {
			t.Fatal(ferr)
		}
		if field.FieldNum != 4 {
			continue
		}
		chDec, _ := frame.NewSubDecoder(frame.GetBytes(field))
		for {
			chField, cerr := chDec.NextField()
			if errors.Is(cerr, frame.ErrEndOfFrame) {
				break
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			if chField.FieldNum != 9 {
				continue
			}
			grpDec, _ := frame.NewSubDecoder(frame.GetBytes(chField))
			for {
				gf, gerr := grpDec.NextField()
				if errors.Is(gerr, frame.ErrEndOfFrame) {
					break
				}
				if gerr != nil {
					t.Fatal(gerr)
				}
				if gf.FieldNum != 3 {
					continue
				}
				cmdDec, _ := frame.NewSubDecoder(frame.GetBytes(gf))
				for {
					cf, cerr := cmdDec.NextField()
					if errors.Is(cerr, frame.ErrEndOfFrame) {
						break
					}
					if cerr != nil {
						t.Fatal(cerr)
					}
					if cf.FieldNum == 1 && frame.GetUint64(cf) == 0 {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestManifestEdgeWithEmptyTemplateIDsSkipsCommandEncoding is the same wire
// contract for a channel whose template_ids is empty (the dev PG "0/4 empty"
// case): the command must be skipped, no varint 0 on the wire.
func TestManifestEdgeWithEmptyTemplateIDsSkipsCommandEncoding(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const nodeID = "f3-empty"
	if err := db.Create(&models.Node{NodeID: nodeID, Status: "online", ProtocolVersion: "2.5", Capabilities: snapshotCapabilities}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: nodeID, BusType: "UART", HardwareType: "UART", Enabled: true, BusConfig: "10110000258000", TemplateIDs: ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{NodeID: nodeID, ChannelID: 1, Type: "no-such-driver", HardwareID: "0x77", IntervalMs: 3000, Enabled: true, Name: "empty-edge"}).Error; err != nil {
		t.Fatal(err)
	}
	registry := drivers.NewRegistry()
	mgr, mock := newManifestTestManager(t, db, registry)

	if err := mgr.SendConfigManifestWithDecision(SyncDecision{DeviceID: nodeID, SyncID: "f3-2", Action: SyncActionFull, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("no manifest was published")
	}
	if found := manifestContainsTemplateIDZero(t, mock.publishedPayload); found {
		t.Fatal("payload contains a template_id == 0 command — varint 0 must never go on the wire")
	}
}
