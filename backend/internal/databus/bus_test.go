package databus

import (
	"sync"
	"testing"
	"time"
)

type mockDataConsumer struct {
	name     string
	shouldH  bool
	events   []DataEvent
	mu       sync.Mutex
}

func (m *mockDataConsumer) Name() string { return m.name }
func (m *mockDataConsumer) ShouldHandle(evt DataEvent) bool { return m.shouldH }
func (m *mockDataConsumer) Handle(evt DataEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, evt)
}

func (m *mockDataConsumer) getEvents() []DataEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]DataEvent{}, m.events...)
}

type panicDataConsumer struct{ name string }

func (p *panicDataConsumer) Name() string                    { return p.name }
func (p *panicDataConsumer) ShouldHandle(evt DataEvent) bool { return true }
func (p *panicDataConsumer) Handle(evt DataEvent)            { panic("test panic") }

func TestDataEventBus_Publish_DeliversToActiveConsumers(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	active := &mockDataConsumer{name: "active", shouldH: true}
	inactive := &mockDataConsumer{name: "inactive", shouldH: false}

	bus.Register(active)
	bus.Register(inactive)

	evt := DataEvent{DeviceID: "test", ChannelID: 1, RequestID: 100, RawData: []byte{0x01, 0x02}}
	bus.Publish(evt)
	time.Sleep(100 * time.Millisecond)

	if len(active.getEvents()) != 1 {
		t.Fatalf("active consumer should receive 1 event, got %d", len(active.getEvents()))
	}
	if len(inactive.getEvents()) != 0 {
		t.Errorf("inactive consumer should receive 0 events, got %d", len(inactive.getEvents()))
	}
}

func TestDataEventBus_PassiveDataOnlyGoesToLightConsumers(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	terminal := &mockDataConsumer{name: "terminal", shouldH: true}
	wsPush := &mockDataConsumer{name: "ws_push", shouldH: true}
	// Heavy consumers return false for passive data
	pendingWrite := &mockDataConsumer{name: "pending_write", shouldH: false}
	dbPersist := &mockDataConsumer{name: "db_persist", shouldH: false}
	sensorParser := &mockDataConsumer{name: "sensor_parser", shouldH: false}

	bus.Register(terminal)
	bus.Register(wsPush)
	bus.Register(pendingWrite)
	bus.Register(dbPersist)
	bus.Register(sensorParser)

	// Passive data: request_id=0
	evt := DataEvent{DeviceID: "test", ChannelID: 1, RequestID: 0, RawData: []byte{0xDD, 0xA5}}
	bus.Publish(evt)
	time.Sleep(100 * time.Millisecond)

	if len(terminal.getEvents()) != 1 {
		t.Errorf("terminal consumer should receive passive data, got %d", len(terminal.getEvents()))
	}
	if len(wsPush.getEvents()) != 1 {
		t.Errorf("ws_push consumer should receive passive data, got %d", len(wsPush.getEvents()))
	}
	if len(pendingWrite.getEvents()) != 0 {
		t.Errorf("pending_write consumer should NOT receive passive data")
	}
	if len(dbPersist.getEvents()) != 0 {
		t.Errorf("db_persist consumer should NOT receive passive data")
	}
	if len(sensorParser.getEvents()) != 0 {
		t.Errorf("sensor_parser consumer should NOT receive passive data")
	}
}

func TestDataEventBus_ConsumerPanic_Isolated(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	panicC := &panicDataConsumer{name: "panicker"}
	goodC := &mockDataConsumer{name: "good", shouldH: true}

	bus.Register(panicC)
	bus.Register(goodC)

	bus.Publish(DataEvent{DeviceID: "test"})
	time.Sleep(100 * time.Millisecond)

	if len(goodC.getEvents()) != 1 {
		t.Errorf("good consumer should receive event even if another panics")
	}
}

func TestDataEventBus_RegisterDuplicate(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	c1 := &mockDataConsumer{name: "dup", shouldH: true}
	c2 := &mockDataConsumer{name: "dup", shouldH: true}

	bus.Register(c1)
	bus.Register(c2) // should be ignored

	bus.Publish(DataEvent{DeviceID: "n1"})
	time.Sleep(50 * time.Millisecond)

	if len(c1.getEvents()) != 1 {
		t.Errorf("first consumer should receive event")
	}
}

func TestDataEventBus_Unregister(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	c := &mockDataConsumer{name: "removable", shouldH: true}
	bus.Register(c)

	bus.Publish(DataEvent{DeviceID: "n1"})
	time.Sleep(50 * time.Millisecond)

	bus.Unregister("removable")

	bus.Publish(DataEvent{DeviceID: "n2"})
	time.Sleep(50 * time.Millisecond)

	if len(c.getEvents()) != 1 {
		t.Errorf("consumer should only have 1 event after unregister, got %d", len(c.getEvents()))
	}
}

func TestDataEvent_IsPassive(t *testing.T) {
	passive := DataEvent{RequestID: 0, EdgeDeviceID: 0}
	if !passive.IsPassive() {
		t.Error("RequestID=0, EdgeDeviceID=0 should be passive")
	}

	command := DataEvent{RequestID: 100}
	if command.IsPassive() {
		t.Error("RequestID≠0 should not be passive")
	}
}
