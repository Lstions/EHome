package nodemgr

import (
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

func TestConfigEventBus_Publish_ReceiveEvent(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	ch := bus.Subscribe()

	evt := ConfigChangeEvent{
		Type:     CfgChangeChannel,
		Action:   CfgActionUpdate,
		NodeID:   "node-5",
		EntityID: "42",
	}
	bus.Publish(evt)

	select {
	case received := <-ch:
		if received.Type != evt.Type {
			t.Fatalf("wrong type: got %s want %s", received.Type, evt.Type)
		}
		if received.NodeID != evt.NodeID {
			t.Fatalf("wrong node: got %s want %s", received.NodeID, evt.NodeID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestConfigEventBus_Publish_NonBlocking(t *testing.T) {
	_ = setupEventBusTestDB(t)
	// Buffer size 1 — will fill quickly
	bus := NewConfigEventBus(1)

	// Publish more events than buffer can hold
	for i := 0; i < 10; i++ {
		bus.Publish(ConfigChangeEvent{
			Type:     CfgChangeChannel,
			Action:   CfgActionUpdate,
			NodeID:   "node",
			EntityID: "item",
		})
	}
	// Should not block or panic
}

func TestConfigEventBus_Publish_SetsDefaults(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	ch := bus.Subscribe()

	// Publish without EventID or Timestamp
	bus.Publish(ConfigChangeEvent{
		Type:     CfgChangeChannel,
		Action:   CfgActionDelete,
		NodeID:   "1",
		EntityID: "1",
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

func TestConfigEventBus_CurrentEpoch_ReturnsZero(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	if bus.CurrentEpoch() != 0 {
		t.Fatalf("CurrentEpoch should always return 0 in v2, got %d", bus.CurrentEpoch())
	}

	bus.Publish(ConfigChangeEvent{
		Type:   CfgChangeChannel,
		Action: CfgActionUpdate,
		NodeID: "1",
	})

	if bus.CurrentEpoch() != 0 {
		t.Fatalf("CurrentEpoch should still return 0 after publish, got %d", bus.CurrentEpoch())
	}
}
