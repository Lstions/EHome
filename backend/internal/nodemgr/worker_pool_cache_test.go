package nodemgr

import (
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupNodeIDCacheTestDB returns an in-memory DB with a single node
// created and its mapping stored in the cache.
func setupNodeIDCacheTestDB(t *testing.T, deviceID string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}); err != nil {
		t.Fatalf("migrate node: %v", err)
	}
	node := models.Node{NodeID: deviceID, Name: "node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	nodeIDCache.Store(deviceID, nodeIDCacheEntry{nodeID: node.ID, writtenAt: time.Now()})
	return db
}

// TestLookupCollectorID_HitWithinTTL verifies a fresh cache entry returns
// the cached ID and never touches the DB.
func TestLookupCollectorID_HitWithinTTL(t *testing.T) {
	// A plain DB without the node row: if the cache were bypassed, the
	// lookup would fail. A hit proves the cache is used.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = db.AutoMigrate(&models.Node{})

	const deviceID = "NODE-TTL-HIT"
	nodeIDCache.Store(deviceID, nodeIDCacheEntry{nodeID: 4242, writtenAt: time.Now()})
	defer nodeIDCache.Delete(deviceID)

	id, ok := lookupCollectorID(db, deviceID)
	if !ok {
		t.Fatal("expected cache hit within TTL")
	}
	if id != 4242 {
		t.Fatalf("expected cached id 4242, got %d", id)
	}
}

// TestLookupCollectorID_RequeryAfterTTL verifies an entry older than the TTL
// is discarded and the mapping is re-fetched from the DB (and refreshed).
func TestLookupCollectorID_RequeryAfterTTL(t *testing.T) {
	db := setupNodeIDCacheTestDB(t, "NODE-TTL-EXPIRED")

	// Age the entry past the TTL.
	key := "NODE-TTL-EXPIRED"
	if v, ok := nodeIDCache.Load(key); ok {
		entry := v.(nodeIDCacheEntry)
		entry.writtenAt = time.Now().Add(-nodeIDCacheTTL - time.Minute)
		nodeIDCache.Store(key, entry)
	}
	defer nodeIDCache.Delete(key)

	// DB lookup must still succeed and refresh the entry timestamp.
	id, ok := lookupCollectorID(db, key)
	if !ok {
		t.Fatal("expected re-query after TTL to succeed")
	}
	if v, ok := nodeIDCache.Load(key); !ok {
		t.Fatal("expected refreshed entry after TTL re-query")
	} else if entry := v.(nodeIDCacheEntry); entry.nodeID != id {
		t.Fatalf("refreshed entry id %d != returned id %d", entry.nodeID, id)
	} else if time.Since(entry.writtenAt) > nodeIDCacheTTL {
		t.Fatal("refreshed entry still marked expired")
	}
}

// TestLookupCollectorID_RequeryAfterTTL_DeletedNode verifies that when the
// node has been removed from the DB, an expired cache entry does not mask
// the deletion: the re-query fails and the miss is reported.
func TestLookupCollectorID_RequeryAfterTTL_DeletedNode(t *testing.T) {
	db := setupNodeIDCacheTestDB(t, "NODE-TTL-DELETED")

	const deviceID = "NODE-TTL-DELETED"
	if v, ok := nodeIDCache.Load(deviceID); ok {
		entry := v.(nodeIDCacheEntry)
		entry.writtenAt = time.Now().Add(-nodeIDCacheTTL - time.Minute)
		nodeIDCache.Store(deviceID, entry)
	}
	defer nodeIDCache.Delete(deviceID)

	// Simulate the node being deleted behind the cache's back.
	if err := db.Where("node_id = ?", deviceID).Delete(&models.Node{}).Error; err != nil {
		t.Fatalf("delete node: %v", err)
	}

	if _, ok := lookupCollectorID(db, deviceID); ok {
		t.Fatal("expected miss after node deletion and TTL expiry")
	}
}

// TestLookupCollectorID_ExplicitInvalidation verifies InvalidateNodeIDCache
// takes effect immediately even within the TTL window.
func TestLookupCollectorID_ExplicitInvalidation(t *testing.T) {
	db := setupNodeIDCacheTestDB(t, "NODE-INVAL")

	const deviceID = "NODE-INVAL"
	defer nodeIDCache.Delete(deviceID)

	// Warm the cache, then invalidate.
	if _, ok := lookupCollectorID(db, deviceID); !ok {
		t.Fatal("expected warm lookup to succeed")
	}
	InvalidateNodeIDCache(deviceID)
	if _, ok := nodeIDCache.Load(deviceID); ok {
		t.Fatal("expected entry removed after explicit invalidation")
	}

	// Lookup must now re-query the DB (node still present) and repopulate.
	id, ok := lookupCollectorID(db, deviceID)
	if !ok {
		t.Fatal("expected re-query after explicit invalidation to succeed")
	}
	if v, ok := nodeIDCache.Load(deviceID); !ok {
		t.Fatal("expected cache repopulated after explicit invalidation")
	} else if entry := v.(nodeIDCacheEntry); entry.nodeID != id {
		t.Fatalf("repopulated id %d != returned id %d", entry.nodeID, id)
	}
}

// TestLookupCollectorID_Miss_NoNode verifies a cache miss with no matching
// node returns (0, false) and does not panic.
func TestLookupCollectorID_Miss_NoNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = db.AutoMigrate(&models.Node{})

	if id, ok := lookupCollectorID(db, "NODE-NOPE"); ok {
		t.Fatalf("expected miss for unknown node, got id=%d", id)
	}
	if _, ok := nodeIDCache.Load("NODE-NOPE"); ok {
		t.Fatal("expected no cache entry stored on miss")
	}
}

// TestLookupCollectorID_Concurrent ensures the TTL check and lazy delete do
// not race under parallel lookups of the same device.
func TestLookupCollectorID_Concurrent(t *testing.T) {
	db := setupNodeIDCacheTestDB(t, "NODE-CONC")

	const deviceID = "NODE-CONC"
	defer nodeIDCache.Delete(deviceID)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := lookupCollectorID(db, deviceID); !ok {
				t.Error("concurrent lookup failed")
			}
		}()
	}
	wg.Wait()
}
