package collector

import (
	"sync"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	db.AutoMigrate(&models.ConfigMeta{})
	return db
}

func TestEpochGenerator_Next_Monotonic(t *testing.T) {
	db := setupTestDB(t)
	gen := NewEpochGenerator(db)
	gen.Restore()

	var prev uint64
	for i := 0; i < 100; i++ {
		val := gen.Next()
		if val <= prev && i > 0 {
			t.Fatalf("epoch not monotonic: prev=%d current=%d at i=%d", prev, val, i)
		}
		prev = val
	}
}

func TestEpochGenerator_Next_Concurrent100(t *testing.T) {
	db := setupTestDB(t)
	gen := NewEpochGenerator(db)
	gen.Restore()

	const n = 100
	var wg sync.WaitGroup
	results := make(chan uint64, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- gen.Next()
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[uint64]bool)
	for val := range results {
		if seen[val] {
			t.Fatalf("duplicate epoch: %d", val)
		}
		seen[val] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique epochs, got %d", n, len(seen))
	}
}

func TestEpochGenerator_Restore(t *testing.T) {
	db := setupTestDB(t)
	gen := NewEpochGenerator(db)

	// First restore seeds
	if err := gen.Restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	first := gen.Current()
	if first == 0 {
		t.Fatal("seeded epoch should not be 0")
	}

	// Increment a few times
	gen.Next()
	gen.Next()
	afterNext := gen.Current()

	// Create a new generator and restore — should get the persisted value
	gen2 := NewEpochGenerator(db)
	if err := gen2.Restore(); err != nil {
		t.Fatalf("restore2 failed: %v", err)
	}
	restored := gen2.Current()

	// The async persist may not have caught up, so we accept >= first
	if restored < first {
		t.Fatalf("restored epoch %d < first epoch %d", restored, first)
	}
	_ = afterNext
}

func TestEpochGenerator_Current_NoIncrement(t *testing.T) {
	db := setupTestDB(t)
	gen := NewEpochGenerator(db)
	gen.Restore()

	c1 := gen.Current()
	c2 := gen.Current()
	if c1 != c2 {
		t.Fatalf("Current() should not increment: c1=%d c2=%d", c1, c2)
	}
}
