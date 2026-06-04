package collector

import (
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupEventBusTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	db.AutoMigrate(&models.ConfigMeta{})
	return db
}

func TestConfigEventBus_Publish_IncrementsEpoch(t *testing.T) {
	db := setupEventBusTestDB(t)
	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	bus := NewConfigEventBus(10, epochGen)

	before := bus.CurrentEpoch()
	bus.Publish(ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionUpdate,
		NodeID: 1,
		EntityID:    100,
	})
	after := bus.CurrentEpoch()

	if after <= before {
		t.Fatalf("epoch should increment: before=%d after=%d", before, after)
	}
}

func TestConfigEventBus_Subscribe_ReceiveEvent(t *testing.T) {
	db := setupEventBusTestDB(t)
	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	bus := NewConfigEventBus(10, epochGen)

	ch := bus.Subscribe()

	evt := ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionCreate,
		NodeID: 5,
		EntityID:    42,
	}
	bus.Publish(evt)

	select {
	case received := <-ch:
		if received.Type != evt.Type {
			t.Fatalf("wrong type: got %s want %s", received.Type, evt.Type)
		}
		if received.NodeID != evt.NodeID {
			t.Fatalf("wrong collector: got %d want %d", received.NodeID, evt.NodeID)
		}
		if received.Epoch == 0 {
			t.Fatal("epoch should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestConfigEventBus_Publish_NonBlocking(t *testing.T) {
	db := setupEventBusTestDB(t)
	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	// Buffer size 1 — will fill quickly
	bus := NewConfigEventBus(1, epochGen)

	// Publish more events than buffer can hold
	for i := 0; i < 10; i++ {
		bus.Publish(ConfigChangeEvent{
			Type:        CfgChangeChannel,
			Action:      CfgActionUpdate,
			NodeID: uint(i),
			EntityID:    uint(i),
		})
	}
	// Should not block or panic
}

func TestConfigEventBus_Publish_SetsDefaults(t *testing.T) {
	db := setupEventBusTestDB(t)
	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	bus := NewConfigEventBus(10, epochGen)

	ch := bus.Subscribe()

	// Publish without EventID or Timestamp
	bus.Publish(ConfigChangeEvent{
		Type:        CfgChangeChannel,
		Action:      CfgActionDelete,
		NodeID: 1,
		EntityID:    1,
	})

	select {
	case evt := <-ch:
		if evt.EventID == "" {
			t.Fatal("EventID should be auto-generated")
		}
		if evt.Timestamp.IsZero() {
			t.Fatal("Timestamp should be auto-set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestConfigEventBus_ConcurrentPublish(t *testing.T) {
	db := setupEventBusTestDB(t)
	epochGen := NewEpochGenerator(db)
	epochGen.Restore()
	bus := NewConfigEventBus(100, epochGen)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bus.Publish(ConfigChangeEvent{
				Type:        CfgChangeChannel,
				Action:      CfgActionUpdate,
				NodeID: uint(idx),
				EntityID:    uint(idx),
			})
		}(i)
	}
	wg.Wait()

	// All events should have unique epochs (monotonic increment)
	epochs := make(map[uint64]bool)
	ch := bus.Subscribe()
	for i := 0; i < n; i++ {
		select {
		case evt := <-ch:
			if epochs[evt.Epoch] {
				t.Fatalf("duplicate epoch: %d", evt.Epoch)
			}
			epochs[evt.Epoch] = true
		case <-time.After(time.Second):
			t.Fatalf("timeout at event %d", i)
		}
	}
}
