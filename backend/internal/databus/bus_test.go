package databus

import (
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type mockDataConsumer struct {
	name    string
	shouldH bool
	events  []DataEvent
	mu      sync.Mutex
}

func (m *mockDataConsumer) Name() string                    { return m.name }
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

type blockingDataConsumer struct {
	name    string
	started chan struct{}
	release <-chan struct{}
}

type commandAwareTestDriver struct {
	calledWith chan string
}

func (d *commandAwareTestDriver) DeviceType() string { return "command_aware_test" }
func (d *commandAwareTestDriver) DeviceName() string { return "command aware test" }
func (d *commandAwareTestDriver) OEM() string        { return "test" }
func (d *commandAwareTestDriver) Category() string   { return "test" }
func (d *commandAwareTestDriver) HardwareTypes() []string {
	return []string{"UART"}
}
func (d *commandAwareTestDriver) ParseData(raw []byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "fallback_value", Value: 1}}, nil
}
func (d *commandAwareTestDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *commandAwareTestDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return nil
}
func (d *commandAwareTestDriver) ParseDataWithCommand(raw []byte, writeData string) ([]drivers.SensorData, error) {
	d.calledWith <- writeData
	return []drivers.SensorData{{Name: "command_value", Value: 1}}, nil
}

type passthroughReassembler struct{}

func (passthroughReassembler) Append(requestID uint32, data []byte) []byte { return data }
func (passthroughReassembler) Consume(requestID uint32)                    {}

func newConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{}, &models.ConfigTemplate{}, &models.UnifiedData{}, &models.DeviceData{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func (c *blockingDataConsumer) Name() string                    { return c.name }
func (c *blockingDataConsumer) ShouldHandle(evt DataEvent) bool { return true }
func (c *blockingDataConsumer) Handle(evt DataEvent) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.release
}

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

func TestDataEventBus_ScheduledSampleIsNotPassive(t *testing.T) {
	// ESP32 CMD_SAMPLE reports have no request ID, but do include the target
	// edge_device_id. They must take the parse/store path rather than the
	// passive terminal-only path.
	scheduled := DataEvent{RequestID: 0, EdgeDeviceID: 42, RawData: []byte{0x01}}
	if scheduled.IsPassive() {
		t.Fatal("scheduled sample with edge_device_id must not be passive")
	}
	if !NewDBPersistConsumer(nil).ShouldHandle(scheduled) {
		t.Fatal("scheduled sample must be persisted")
	}
	if !NewSensorParserConsumer(nil, nil, nil, nil).ShouldHandle(scheduled) {
		t.Fatal("scheduled sample must be parsed")
	}
}

func TestDataEventBus_BoundsConsumerConcurrency(t *testing.T) {
	bus := NewDataEventBus()
	release := make(chan struct{})
	consumer := &blockingDataConsumer{
		name:    "blocking",
		started: make(chan struct{}, 32),
		release: release,
	}
	bus.Register(consumer)

	for i := 0; i < 16; i++ {
		bus.Publish(DataEvent{Sequence: uint64(i)})
	}

	// Each consumer has one serial worker, so no more than one blocked handler
	// can run for this consumer regardless of event volume.
	time.Sleep(100 * time.Millisecond)
	if got := len(consumer.started); got > 1 {
		close(release)
		bus.Stop()
		t.Fatalf("started %d handlers concurrently; per-consumer bound is 1", got)
	}

	// A separate lightweight consumer must run even while the parser-style
	// consumer is blocked; this protects the terminal/WS fast path.
	fast := &mockDataConsumer{name: "fast", shouldH: true}
	bus.Register(fast)
	bus.Publish(DataEvent{Sequence: 99})
	deadline := time.Now().Add(time.Second)
	for len(fast.getEvents()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(fast.getEvents()) != 1 {
		close(release)
		bus.Stop()
		t.Fatal("slow consumer blocked independent fast consumer")
	}

	close(release)
	bus.Stop()
}

func TestDataEventBus_DropsWhenConsumerMailboxIsFull(t *testing.T) {
	bus := NewDataEventBus()
	release := make(chan struct{})
	consumer := &blockingDataConsumer{name: "blocked-drop", started: make(chan struct{}, 1), release: release}
	bus.Register(consumer)
	bus.Publish(DataEvent{Sequence: 0})
	select {
	case <-consumer.started:
	case <-time.After(time.Second):
		close(release)
		bus.Stop()
		t.Fatal("blocking consumer did not start")
	}
	for i := 1; i < consumerMailboxSize+8; i++ {
		bus.Publish(DataEvent{Sequence: uint64(i)})
	}
	deadline := time.Now().Add(time.Second)
	for bus.DroppedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bus.DroppedCount() == 0 {
		close(release)
		bus.Stop()
		t.Fatal("full consumer mailbox must increment dropped count")
	}
	close(release)
	bus.Stop()
}

func TestDataEventBus_StopIsIdempotentAndPublishSafe(t *testing.T) {
	bus := NewDataEventBus()
	bus.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop/Publish after Stop must not panic: %v", r)
		}
	}()
	bus.Publish(DataEvent{})
	bus.Stop()
}

func TestDataEventBus_IsPassiveAndRoutesWebPush(t *testing.T) {
	passive := DataEvent{RequestID: 0, EdgeDeviceID: 0}
	scheduled := DataEvent{RequestID: 0, EdgeDeviceID: 42}
	command := DataEvent{RequestID: 7}

	wsPush := NewWSPushConsumer(nil)
	for _, evt := range []DataEvent{passive} {
		if !wsPush.ShouldHandle(evt) {
			t.Fatalf("passive event %+v must reach ws push", evt)
		}
	}
	for _, evt := range []DataEvent{scheduled, command} {
		if wsPush.ShouldHandle(evt) {
			t.Fatalf("non-passive event %+v must not duplicate ws push", evt)
		}
	}
}

func TestSensorParserConsumer_UsesCommandWriteData(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-command"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	device := models.EdgeDevice{Name: "command-device", NodeID: node.NodeID, ChannelID: channel.ID, Type: "command_aware_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}
	template := models.ConfigTemplate{NodeID: node.NodeID, WriteData: "485056420d"}
	if err := db.Create(&template).Error; err != nil {
		t.Fatalf("create template: %v", err)
	}

	driver := &commandAwareTestDriver{calledWith: make(chan string, 1)}
	drivers.Register(driver)
	consumer := NewSensorParserConsumer(db, nil, nil, passthroughReassembler{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(channel.ID), RequestID: 9,
		CommandIndex:      uint64(template.ID % 256),
		CommandTemplateID: uint64(template.ID),
		RawData:           []byte("response"),
	})

	select {
	case got := <-driver.calledWith:
		if got != template.WriteData {
			t.Fatalf("command context = %q, want %q", got, template.WriteData)
		}
	case <-time.After(time.Second):
		t.Fatal("CommandAwareDriver was not called")
	}

	var count int64
	if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", device.ID).Count(&count).Error; err != nil {
		t.Fatalf("count unified data: %v", err)
	}
	if count != 1 {
		t.Fatalf("unified data count = %d, want 1", count)
	}
}

func TestSensorParserConsumer_ResolvesExplicitEdgeDeviceID(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-explicit"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	device := models.EdgeDevice{Name: "explicit-device", NodeID: node.NodeID, ChannelID: channel.ID, Type: "command_aware_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}

	driver := &commandAwareTestDriver{calledWith: make(chan string, 1)}
	drivers.Register(driver)
	consumer := NewSensorParserConsumer(db, nil, nil, passthroughReassembler{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, ChannelID: 999, EdgeDeviceID: uint64(device.ID),
		RequestID: 11, RawData: []byte("response"),
	})

	var count int64
	if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", device.ID).Count(&count).Error; err != nil {
		t.Fatalf("count unified data: %v", err)
	}
	if count != 1 {
		t.Fatalf("explicit edge device id did not route data; count = %d", count)
	}
}
