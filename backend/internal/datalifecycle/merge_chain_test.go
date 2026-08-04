package datalifecycle

import (
	"testing"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// strPtr is a tiny helper for merge_status literals.
func strPtr(s string) *string { return &s }

func TestMergeGraph_Resolve_FollowsChainToFinalTarget(t *testing.T) {
	db := testutil.OpenTestDB(t)

	final := models.LogicalDevice{IdentityKey: "k:final", Name: "final", DeviceType: "t"}
	if err := db.Create(&final).Error; err != nil {
		t.Fatal(err)
	}
	mid := models.LogicalDevice{IdentityKey: "k:mid", Name: "mid", DeviceType: "t", MergedInto: &final.ID}
	if err := db.Create(&mid).Error; err != nil {
		t.Fatal(err)
	}
	leaf := models.LogicalDevice{IdentityKey: "k:leaf", Name: "leaf", DeviceType: "t", MergedInto: &mid.ID}
	if err := db.Create(&leaf).Error; err != nil {
		t.Fatal(err)
	}

	g, err := LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Resolve(leaf.ID); got != final.ID {
		t.Errorf("Resolve(leaf) = %d, want final target %d", got, final.ID)
	}
	if got := g.Resolve(final.ID); got != final.ID {
		t.Errorf("Resolve(final) = %d, want itself %d", got, final.ID)
	}
}

func TestMergeGraph_Resolve_CycleTerminatesWithin8Hops(t *testing.T) {
	db := testutil.OpenTestDB(t)

	a := models.LogicalDevice{IdentityKey: "k:a", Name: "a", DeviceType: "t"}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	b := models.LogicalDevice{IdentityKey: "k:b", Name: "b", DeviceType: "t", MergedInto: &a.ID}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	// Close the cycle: a → b → a (malformed data must not hang the walk).
	if err := db.Model(&models.LogicalDevice{}).Where("id = ?", a.ID).
		Update("merged_into", b.ID).Error; err != nil {
		t.Fatal(err)
	}

	g, err := LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	got := g.Resolve(a.ID) // terminates only if the hop bound works
	if got != a.ID && got != b.ID {
		t.Errorf("Resolve escaped the cycle: got %d (a=%d b=%d)", got, a.ID, b.ID)
	}
}

func TestMergeGraph_Resolve_StopsAt8HopsOnLongChain(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// Chain 1 → 2 → ... → 10 (10 nodes, 9 edges > the 8-hop bound).
	nodes := make([]models.LogicalDevice, 10)
	for i := range nodes {
		nodes[i] = models.LogicalDevice{
			IdentityKey: string(rune('a'+i)) + ":x",
			Name:        "n", DeviceType: "t",
		}
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < len(nodes)-1; i++ {
		next := nodes[i+1].ID
		if err := db.Model(&models.LogicalDevice{}).Where("id = ?", nodes[i].ID).
			Update("merged_into", next).Error; err != nil {
			t.Fatal(err)
		}
	}

	g, err := LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	// 8 hops from node[0] lands on node[8] (index 8 = 9th node), not node[9].
	if got, want := g.Resolve(nodes[0].ID), nodes[8].ID; got != want {
		t.Errorf("Resolve stopped at %d, want %d after exactly %d hops", got, want, mergeChainMaxHops)
	}
}

func TestLoadMergeGraph_SingleQuery(t *testing.T) {
	db := testutil.OpenTestDB(t)

	final := models.LogicalDevice{IdentityKey: "k:f", Name: "f", DeviceType: "t"}
	if err := db.Create(&final).Error; err != nil {
		t.Fatal(err)
	}
	src := models.LogicalDevice{IdentityKey: "k:s", Name: "s", DeviceType: "t",
		MergedInto: &final.ID, MergeStatus: strPtr(models.MergeStatusDone)}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}

	// v3.3-N3: the graph load must be ONE query — never per-hop round trips.
	// Count on both processors: GORM's Find() executes the Query processor
	// (gorm:query) while Scan() executes the Row processor (gorm:row); the
	// contract is "one logical_devices query" regardless of which finisher
	// LoadMergeGraph uses. Hooks are registered After the main callback so
	// Statement.Table is already resolved by BuildQuerySQL.
	queries := 0
	count := func(tx *gorm.DB) {
		if tx.Statement.Table == "logical_devices" {
			queries++
		}
	}
	if err := db.Callback().Query().After("gorm:query").
		Register("test:count_logical_device_queries", count); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().After("gorm:row").
		Register("test:count_logical_device_queries_row", count); err != nil {
		t.Fatal(err)
	}

	g, err := LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Errorf("LoadMergeGraph issued %d logical_devices queries, want exactly 1", queries)
	}
	// The single load must carry enough to walk AND to decide dedup (§六:
	// 与 scope 解析同往返).
	if got := g.Resolve(src.ID); got != final.ID {
		t.Errorf("Resolve(src) = %d, want %d", got, final.ID)
	}
	if !g.HasDoneIncomingMerge(final.ID) {
		t.Error("HasDoneIncomingMerge(final) = false, want true (done source exists)")
	}
}

func TestMergeGraph_HasDoneIncomingMerge_StatusSensitive(t *testing.T) {
	db := testutil.OpenTestDB(t)

	target := models.LogicalDevice{IdentityKey: "k:t", Name: "t", DeviceType: "x"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	other := models.LogicalDevice{IdentityKey: "k:o", Name: "o", DeviceType: "x"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}

	g, err := LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	if g.HasDoneIncomingMerge(target.ID) {
		t.Error("no sources at all: must be false")
	}

	// Pending-only source → dedup must NOT enable (migration still running).
	pending := models.LogicalDevice{IdentityKey: "k:p", Name: "p", DeviceType: "x",
		MergedInto: &target.ID, MergeStatus: strPtr(models.MergeStatusPending)}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	g, err = LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	if g.HasDoneIncomingMerge(target.ID) {
		t.Error("pending-only source: must be false")
	}

	// Done source → enable.
	done := models.LogicalDevice{IdentityKey: "k:d", Name: "d", DeviceType: "x",
		MergedInto: &target.ID, MergeStatus: strPtr(models.MergeStatusDone)}
	if err := db.Create(&done).Error; err != nil {
		t.Fatal(err)
	}
	g, err = LoadMergeGraph(db)
	if err != nil {
		t.Fatal(err)
	}
	if !g.HasDoneIncomingMerge(target.ID) {
		t.Error("done source present: must be true")
	}
	// Direction matters: target has no outgoing done merge into other.
	if g.HasDoneIncomingMerge(other.ID) {
		t.Error("other has no incoming merges: must be false")
	}
}

func TestResolveMergeTarget_ZeroAndChain(t *testing.T) {
	db := testutil.OpenTestDB(t)

	if got, err := ResolveMergeTarget(db, 0); err != nil || got != 0 {
		t.Errorf("ResolveMergeTarget(0) = (%d, %v), want (0, nil)", got, err)
	}

	final := models.LogicalDevice{IdentityKey: "k:rf", Name: "rf", DeviceType: "t"}
	if err := db.Create(&final).Error; err != nil {
		t.Fatal(err)
	}
	src := models.LogicalDevice{IdentityKey: "k:rs", Name: "rs", DeviceType: "t", MergedInto: &final.ID}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}

	got, err := ResolveMergeTarget(db, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != final.ID {
		t.Errorf("ResolveMergeTarget(src) = %d, want %d", got, final.ID)
	}
	// Unmerged device resolves to itself.
	got, err = ResolveMergeTarget(db, final.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != final.ID {
		t.Errorf("ResolveMergeTarget(final) = %d, want itself", got)
	}
}
