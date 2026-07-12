package logstream

import (
	"sync"
	"testing"
	"time"
)

// mockConsumer is a test consumer that records all batches it receives
type mockConsumer struct {
	name    string
	active  bool
	batches []LogBatch
	mu      sync.Mutex
}

func (m *mockConsumer) Name() string   { return m.name }
func (m *mockConsumer) IsActive() bool { return m.active }
func (m *mockConsumer) Consume(batch LogBatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches = append(m.batches, batch)
}

func (m *mockConsumer) getBatches() []LogBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]LogBatch{}, m.batches...)
}

func TestLogEventBus_Publish_DeliversToActiveConsumers(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	active := &mockConsumer{name: "active", active: true}
	inactive := &mockConsumer{name: "inactive", active: false}

	bus.Register(active)
	bus.Register(inactive)

	batch := LogBatch{
		NodeID: "test-node",
		Seq:    1,
		Logs: []LogEntry{
			{NodeID: "test-node", Level: 2, Ts: 1000, Tag: "MQTT", Message: "connected"},
		},
	}

	bus.Publish(batch)

	// Wait for async dispatch
	time.Sleep(100 * time.Millisecond)

	activeBatches := active.getBatches()
	if len(activeBatches) != 1 {
		t.Fatalf("active consumer should receive 1 batch, got %d", len(activeBatches))
	}
	if activeBatches[0].NodeID != "test-node" {
		t.Errorf("batch NodeID = %s, want test-node", activeBatches[0].NodeID)
	}

	inactiveBatches := inactive.getBatches()
	if len(inactiveBatches) != 0 {
		t.Errorf("inactive consumer should receive 0 batches, got %d", len(inactiveBatches))
	}
}

func TestLogEventBus_Register_DuplicateName(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	c1 := &mockConsumer{name: "dup", active: true}
	c2 := &mockConsumer{name: "dup", active: true}

	bus.Register(c1)
	bus.Register(c2) // should be ignored

	bus.Publish(LogBatch{NodeID: "n1"})
	time.Sleep(50 * time.Millisecond)

	// Only c1 should receive (c2 was rejected as duplicate)
	if len(c1.getBatches()) != 1 {
		t.Errorf("first consumer should receive batch")
	}
}

func TestLogEventBus_Unregister(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	c := &mockConsumer{name: "removable", active: true}
	bus.Register(c)

	bus.Publish(LogBatch{NodeID: "n1"})
	time.Sleep(50 * time.Millisecond)

	bus.Unregister("removable")

	bus.Publish(LogBatch{NodeID: "n2"})
	time.Sleep(50 * time.Millisecond)

	batches := c.getBatches()
	if len(batches) != 1 {
		t.Errorf("consumer should only have 1 batch after unregister, got %d", len(batches))
	}
}

func TestLogEventBus_ConsumerPanic_Isolated(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	panicConsumer := &panicConsumer{name: "panicker"}
	goodConsumer := &mockConsumer{name: "good", active: true}

	bus.Register(panicConsumer)
	bus.Register(goodConsumer)

	bus.Publish(LogBatch{NodeID: "n1"})
	time.Sleep(100 * time.Millisecond)

	// good consumer should still receive despite panicker crashing
	if len(goodConsumer.getBatches()) != 1 {
		t.Errorf("good consumer should receive batch even if another panics")
	}
}

type panicConsumer struct{ name string }

func (p *panicConsumer) Name() string   { return p.name }
func (p *panicConsumer) IsActive() bool { return true }
func (p *panicConsumer) Consume(batch LogBatch) {
	panic("intentional test panic")
}

func TestDBConsumer_AlwaysRegistered(t *testing.T) {
	consumer := &DBConsumer{}
	if !consumer.IsActive() {
		t.Error("DBConsumer must stay registered; it evaluates persistence per node in Consume")
	}
}

func TestWSConsumer_BroadcastEvent(t *testing.T) {
 mockHub := &mockWSHub{}
 consumer := NewWSConsumer(mockHub)

 batch := LogBatch{
  NodeID: "test-node",
  Logs: []LogEntry{
   {Level: 0, Ts: 1000, Tag: "ERR", Message: "error msg"},
   {Level: 2, Ts: 2000, Tag: "INFO", Message: "info msg"},
  },
 }

 consumer.Consume(batch)

 if len(mockHub.events) != 1 {
  t.Fatalf("expected 1 broadcast event, got %d", len(mockHub.events))
 }

 if mockHub.events[0].eventType != "node_log" {
  t.Errorf("event type = %s, want node_log", mockHub.events[0].eventType)
 }
}

type mockWSHub struct {
 events []struct {
  eventType string
  payload   interface{}
 }
}

func (m *mockWSHub) BroadcastEvent(eventType string, payload interface{}) {
 m.events = append(m.events, struct {
  eventType string
  payload   interface{}
 }{eventType, payload})
}
