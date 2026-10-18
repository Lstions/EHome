package nodemgr

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// buildMemReport builds a MSG_MEM_RPT payload the way mem_guard.c does:
// five unconditional varints, fields 1..5.
func buildMemReport(free, largest, minEver, minStack, floor uint64) []byte {
	enc := frame.NewEncoder(frame.MsgMemRpt)
	enc.EncodeVarint(1, free)
	enc.EncodeVarint(2, largest)
	enc.EncodeVarint(3, minEver)
	enc.EncodeVarint(4, minStack)
	enc.EncodeVarint(5, floor)
	return enc.Bytes()
}

// TestMemReportPersistsGateUnit is the regression test for the defect found on
// real hardware 2026-10-18: the firmware sent 0x21 once a minute and the
// backend answered "Unknown msg type: 0x21" because the dispatch switch had no
// case, so nodes.largest_free_internal_bytes -- the unit the firmware memory
// gate judges on -- was never populated from this message.
//
// Reverse verification: delete the `case frame.MsgMemRpt` arm in manager.go and
// this test goes red (the column stays 0).
func TestMemReportPersistsGateUnit(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:       "node-mem",
		Name:         "node",
		Status:       "offline",
		HardwareInfo: `{"channels":[]}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	// Drive it through the production dispatch path, not by calling the handler
	// directly: the defect was a missing dispatch arm, so a test that bypasses
	// the switch could never have caught it.
	manager.HandleFrame(node.NodeID, frame.MsgMemRpt, buildMemReport(120000, 40960, 90000, 2048, 16384))

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LargestFreeInternalBytes != 40960 {
		t.Fatalf("largest_free_internal_bytes=%d want 40960 (the gate unit); " +
			"0 means the 0x21 frame was dropped by the dispatch default branch",
			stored.LargestFreeInternalBytes)
	}
	if stored.FreeHeapBytes != 120000 {
		t.Fatalf("free_heap_bytes=%d want 120000", stored.FreeHeapBytes)
	}

	// The dimensions without columns must still be observable.
	var info struct {
		RuntimeMemory struct {
			MinEverBytes         uint64 `json:"min_ever_bytes"`
			MinStackHighWater    uint64 `json:"min_stack_high_water_bytes"`
			FloorBytes           uint64 `json:"floor_bytes"`
		} `json:"runtime_memory"`
	}
	if err := json.Unmarshal([]byte(stored.HardwareInfo), &info); err != nil {
		t.Fatalf("hardware_info is not valid JSON: %v", err)
	}
	if info.RuntimeMemory.MinEverBytes != 90000 {
		t.Fatalf("min_ever_bytes=%d want 90000", info.RuntimeMemory.MinEverBytes)
	}
	if info.RuntimeMemory.MinStackHighWater != 2048 {
		t.Fatalf("min_stack_high_water_bytes=%d want 2048", info.RuntimeMemory.MinStackHighWater)
	}
	if info.RuntimeMemory.FloorBytes != 16384 {
		t.Fatalf("floor_bytes=%d want 16384", info.RuntimeMemory.FloorBytes)
	}
}

// TestMemReportDoesNotClobberKnownValues pins the "only write what was sent"
// rule: a payload carrying only free_bytes must not zero the gate unit that an
// earlier complete report established.
//
// Reverse verification: drop the `report.haveLargest &&` guard from
// persistMemReport and this test goes red.
func TestMemReportDoesNotClobberKnownValues(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:                   "node-mem-partial",
		Name:                     "node",
		Status:                   "offline",
		LargestFreeInternalBytes: 40960,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	partial := frame.NewEncoder(frame.MsgMemRpt)
	partial.EncodeVarint(1, 99999) // free_bytes only, no field 2
	manager.HandleFrame(node.NodeID, frame.MsgMemRpt, partial.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LargestFreeInternalBytes != 40960 {
		t.Fatalf("largest_free_internal_bytes=%d want 40960 (must not be zeroed by a " +
			"partial report); a zero here would silently erase the gate evidence",
			stored.LargestFreeInternalBytes)
	}
	if stored.FreeHeapBytes != 99999 {
		t.Fatalf("free_heap_bytes=%d want 99999", stored.FreeHeapBytes)
	}
}

// TestMemReportRejectsWrongWireType pins fail-closed decoding: a known field
// with the wrong wire type is a protocol violation, not something to guess at.
//
// Reverse verification: replace the wire-type check in memReportVarint with an
// unchecked frame.GetUint64 and this test goes red (it would write 0).
func TestMemReportRejectsWrongWireType(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:                   "node-mem-bad",
		Name:                     "node",
		Status:                   "offline",
		LargestFreeInternalBytes: 50000,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	bad := frame.NewEncoder(frame.MsgMemRpt)
	bad.EncodeVarint(1, 11111)
	bad.EncodeString(2, "not-a-varint") // field 2 as length-delimited
	manager.HandleFrame(node.NodeID, frame.MsgMemRpt, bad.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LargestFreeInternalBytes != 50000 {
		t.Fatalf("malformed frame must be dropped whole (got largest=%d want 50000); " +
			"partial application of a rejected frame is how a wrong number lands in the DB",
			stored.LargestFreeInternalBytes)
	}
}
