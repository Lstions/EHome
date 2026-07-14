package logstream

import (
	"sync"
	"testing"
	"time"
)

func logBusEventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal(message)
		}
	}
}

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

	logBusEventually(t, func() bool { return len(active.getBatches()) == 1 }, "active consumer did not receive batch")

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
	logBusEventually(t, func() bool { return len(c1.getBatches()) == 1 }, "first duplicate-named consumer did not receive batch")

	// Only c1 should receive (c2 was rejected as duplicate)
	if len(c1.getBatches()) != 1 {
		t.Errorf("first consumer should receive batch")
	}
}

func TestLogEventBus_Unregister(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	c := &mockConsumer{name: "removable", active: true}
	observer := &mockConsumer{name: "observer", active: true}
	bus.Register(c)
	bus.Register(observer)

	bus.Publish(LogBatch{NodeID: "n1"})
	logBusEventually(t, func() bool { return len(c.getBatches()) == 1 }, "consumer did not receive batch before unregister")

	bus.Unregister("removable")

	bus.Publish(LogBatch{NodeID: "n2"})
	logBusEventually(t, func() bool { return len(observer.getBatches()) == 2 }, "post-unregister batch was not dispatched")

	batches := c.getBatches()
	if len(batches) != 1 {
		t.Errorf("consumer should only have 1 batch after unregister, got %d", len(batches))
	}
}

func TestLogEventBus_StoppedSnapshotWorkerDoesNotAbortFanout(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()
	worker := &consumerWorker{
		consumer: &mockConsumer{name: "stopped", active: true},
		mailbox:  make(chan LogBatch, consumerMailboxSize),
		done:     make(chan struct{}),
	}
	worker.doneOnce.Do(func() { close(worker.done) })

	if keepGoing := bus.fanoutWorker(worker, LogBatch{NodeID: "snapshot"}); !keepGoing {
		t.Fatal("a stopped snapshot worker must be skipped without aborting fanout")
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
	logBusEventually(t, func() bool { return len(goodConsumer.getBatches()) == 1 }, "good consumer did not receive batch after peer panic")

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

func TestLogEventBus_StopIsIdempotentAndPublishIsSafeAfterStop(t *testing.T) {
	bus := NewLogEventBus()
	bus.Stop()
	bus.Stop()
	bus.Publish(LogBatch{NodeID: "after-stop"})
}

func TestLogEventBus_BoundedConsumerWorker(t *testing.T) {
	bus := NewLogEventBus()
	defer bus.Stop()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	blocked := &blockingConsumer{name: "blocked", started: started, release: release}
	bus.Register(blocked)

	bus.Publish(LogBatch{NodeID: "first"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("consumer did not start")
	}
	for i := 0; i < consumerMailboxSize+4; i++ {
		bus.fanout(LogBatch{NodeID: "queued"})
	}
	if got := bus.DroppedCount(); got == 0 {
		t.Fatal("expected a bounded mailbox overflow to increment dropped count")
	}
	// release is deferred so a failed assertion cannot deadlock bus.Stop().
}

type blockingConsumer struct {
	name    string
	started chan<- struct{}
	release <-chan struct{}
}

func (c *blockingConsumer) Name() string   { return c.name }
func (c *blockingConsumer) IsActive() bool { return true }
func (c *blockingConsumer) Consume(LogBatch) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.release
}

func TestDBConsumer_AlwaysRegistered(t *testing.T) {
	consumer := &DBConsumer{}
	if !consumer.IsActive() {
		t.Error("DBConsumer must stay registered; it evaluates persistence per node in Consume")
	}
}

func TestWSConsumer_BroadcastEventToAdminRole(t *testing.T) {
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
	if mockHub.events[0].role != "admin" {
		t.Errorf("role = %s, want admin", mockHub.events[0].role)
	}
}

type mockWSHub struct {
	events []struct {
		eventType string
		payload   interface{}
		role      string
	}
}

func (m *mockWSHub) BroadcastEventToRole(eventType string, payload interface{}, role string) {
	m.events = append(m.events, struct {
		eventType string
		payload   interface{}
		role      string
	}{eventType, payload, role})
}
