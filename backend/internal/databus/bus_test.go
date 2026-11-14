package databus

import (
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newSensorParserTestConsumer(db *gorm.DB, reassembler Reassembler, testDrivers ...drivers.Driver) *SensorParserConsumer {
	registry := drivers.NewRegistry()
	for _, driver := range testDrivers {
		registry.Register(driver)
	}
	// 2026-11-13：unified_data 整表退役，L1 写入收窄门禁随之删除；
	// 本包的用例断言改为「解析成功 ⇒ sink/latestSink 被调用」。
	return NewSensorParserConsumerWithRegistry(db, nil, reassembler, registry)
}

// captureParsed 用 latestSink 记录解析结果。
//
// 2026-11-13：unified_data 整表退役后，"解析成功"不能再靠数 DB 行来证明
// （没有写入路径了）。latestSink 是同一批解析值的唯一去处之一，用它做判据
// 既保留了断言语义，也不再依赖一张已删除的表。
func captureParsed(consumer *SensorParserConsumer) *[]models.UnifiedData {
	out := make([]models.UnifiedData, 0, 8)
	consumer.SetLatestSink(func(rec models.UnifiedData) { out = append(out, rec) })
	return &out
}

func counterValue(t *testing.T, collector prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	collector.Collect(ch)
	metric := <-ch
	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatal(err)
	}
	return out.GetCounter().GetValue()
}

func eventually(t *testing.T, condition func() bool, message string) {
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
	calledWith  chan string
	plainCalled chan struct{}
	commandErr  error
}

type plainTestDriver struct{}

func (d *plainTestDriver) DeviceType() string      { return "plain_test" }
func (d *plainTestDriver) DeviceName() string      { return "plain test" }
func (d *plainTestDriver) OEM() string             { return "test" }
func (d *plainTestDriver) Category() string        { return "test" }
func (d *plainTestDriver) HardwareTypes() []string { return []string{"UART"} }
func (d *plainTestDriver) ParseData(raw []byte) ([]drivers.SensorData, error) {
	return []drivers.SensorData{{Name: "plain_value", Value: 1}}, nil
}
func (d *plainTestDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *plainTestDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return nil
}

func (d *commandAwareTestDriver) DeviceType() string { return "command_aware_test" }
func (d *commandAwareTestDriver) DeviceName() string { return "command aware test" }
func (d *commandAwareTestDriver) OEM() string        { return "test" }
func (d *commandAwareTestDriver) Category() string   { return "test" }
func (d *commandAwareTestDriver) HardwareTypes() []string {
	return []string{"UART"}
}
func (d *commandAwareTestDriver) ParseData(raw []byte) ([]drivers.SensorData, error) {
	if d.plainCalled != nil {
		d.plainCalled <- struct{}{}
	}
	return []drivers.SensorData{{Name: "fallback_value", Value: 1}}, nil
}
func (d *commandAwareTestDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *commandAwareTestDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return nil
}
func (d *commandAwareTestDriver) ParseDataWithCommand(raw []byte, writeData string) ([]drivers.SensorData, error) {
	d.calledWith <- writeData
	if d.commandErr != nil {
		return nil, d.commandErr
	}
	return []drivers.SensorData{{Name: "command_value", Value: 1}}, nil
}

type passthroughReassembler struct{}

func (passthroughReassembler) Append(deviceID string, requestID uint32, data []byte) []byte {
	return data
}
func (passthroughReassembler) Consume(deviceID string, requestID uint32) {}

type recordingReassembler struct {
	mu       sync.Mutex
	consumed []uint32
}

func (r *recordingReassembler) Append(deviceID string, requestID uint32, data []byte) []byte {
	return data
}
func (r *recordingReassembler) Consume(deviceID string, requestID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consumed = append(r.consumed, requestID)
}
func (r *recordingReassembler) consumedRequest(requestID uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, consumed := range r.consumed {
		if consumed == requestID {
			return true
		}
	}
	return false
}

func newConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 2026-11-11: &models.DeviceData{} 已从清单移除（device_data 整表退役）。
	// 保留下方 UnifiedData 是因为它仍是本包部分用例的记录载体（停写开关语义）。
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{}, &models.ConfigTemplate{}, &models.UnifiedData{}, &models.CalibrationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSensorParserConsumerUsesPersistedBMP280CalibrationAfterRestart(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-bmp280-restart"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "bmp280", NodeID: node.NodeID, ChannelID: channel.ID, Type: "bmp280", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	calibration := []byte{0x70, 0x6b, 0x43, 0x67, 0x18, 0xfc, 0x7d, 0x8e, 0x43, 0xd6, 0xd0, 0x0b, 0x27, 0x0b, 0x8c, 0x00, 0xf9, 0xff, 0x8c, 0x3c, 0xf8, 0xc6, 0x70, 0x17}
	if err := db.Create(&models.CalibrationCache{
		NodeID: node.NodeID, EdgeDeviceID: device.ID, DeviceType: device.Type,
		Data: hex.EncodeToString(calibration),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// A fresh consumer has no initialization-process memory. Successful parsing
	// therefore proves calibration survives and is reloaded from persistence.
	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &drivers.BMP280Driver{})
	captured := captureParsed(consumer)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 1,
		RawData: []byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00},
	})

	if len(*captured) != 2 {
		t.Fatalf("persisted calibration produced %d sensor values, want 2", len(*captured))
	}
	for _, rec := range *captured {
		if rec.DeviceID != device.ID {
			t.Errorf("解析值 device_id = %d, want %d", rec.DeviceID, device.ID)
		}
	}
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
	eventually(t, func() bool { return len(active.getEvents()) == 1 }, "active consumer did not receive event")

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
	eventually(t, func() bool {
		return len(terminal.getEvents()) == 1 && len(wsPush.getEvents()) == 1
	}, "light consumers did not receive passive event")

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
	eventually(t, func() bool { return len(goodC.getEvents()) == 1 }, "good consumer did not receive event after peer panic")

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
	eventually(t, func() bool { return len(c1.getEvents()) == 1 }, "first duplicate-named consumer did not receive event")

	if len(c1.getEvents()) != 1 {
		t.Errorf("first consumer should receive event")
	}
}

func TestDataEventBus_Unregister(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()

	c := &mockDataConsumer{name: "removable", shouldH: true}
	observer := &mockDataConsumer{name: "unregister-observer", shouldH: true}
	bus.Register(c)
	bus.Register(observer)

	bus.Publish(DataEvent{DeviceID: "n1"})
	eventually(t, func() bool { return len(c.getEvents()) == 1 }, "consumer did not receive event before unregister")

	bus.Unregister("removable")

	bus.Publish(DataEvent{DeviceID: "n2"})
	eventually(t, func() bool { return len(observer.getEvents()) == 2 }, "post-unregister event was not dispatched")
	if got := len(c.getEvents()); got != 1 {
		t.Fatalf("consumer received %d events, want 1 after unregister", got)
	}
}

func TestDataEventBus_StoppedSnapshotWorkerDoesNotSkipRemainingConsumers(t *testing.T) {
	for i := 0; i < 50; i++ {
		bus := NewDataEventBus()
		live := &mockDataConsumer{name: "live", shouldH: true}
		bus.Register(live)
		dead := &consumerWorker{
			consumer: &mockDataConsumer{name: "dead", shouldH: true},
			mailbox:  make(chan DataEvent, consumerMailboxSize),
			done:     make(chan struct{}),
		}
		dead.doneOnce.Do(func() { close(dead.done) })
		bus.mu.Lock()
		bus.consumers[dead.consumer.Name()] = dead
		bus.mu.Unlock()

		bus.fanout(DataEvent{Sequence: uint64(i)})
		eventually(t, func() bool { return len(live.getEvents()) == 1 }, "stopped snapshot worker skipped a remaining consumer")
		bus.Stop()
	}
}

func TestDataEventBus_UnregisterTerminatesWorker(t *testing.T) {
	bus := NewDataEventBus()
	defer bus.Stop()
	consumer := &mockDataConsumer{name: "worker-exit", shouldH: true}
	bus.Register(consumer)

	bus.Unregister(consumer.Name())
	exited := make(chan struct{})
	go func() {
		bus.workerWG.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("consumer worker did not exit after Unregister")
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
	// 2026-11-11: 这里原有一条 NewDBPersistConsumer(nil).ShouldHandle(scheduled)
	// 断言（"scheduled sample must be persisted"）。该消费者随 device_data 退役
	// 删除，而"它必须被持久化"这个语义现在由 sensor_parser 承担：
	// ShouldParse = ShouldPersist && ErrorCode==0 && len(RawData)>0。
	// 所以只保留下面那条解析断言 —— 它同时覆盖了"非被动"这个前提。
	if !NewSensorParserConsumer(nil, nil, nil).ShouldHandle(scheduled) {
		t.Fatal("scheduled sample must be parsed (ShouldParse 蕴含 ShouldPersist；" +
			"db_persist 消费者已随 device_data 退役)")
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
	select {
	case <-consumer.started:
	case <-time.After(time.Second):
		close(release)
		bus.Stop()
		t.Fatal("blocking consumer did not start")
	}
	if got := len(consumer.started); got > 1 {
		close(release)
		bus.Stop()
		t.Fatalf("started %d handlers concurrently; per-consumer bound is 1", got)
	}

	// Wait until the dispatcher has moved every pre-registration event into the
	// blocking consumer's mailbox. Otherwise a newly registered fast consumer
	// could legitimately receive some of those earlier ingress events too.
	bus.mu.RLock()
	blockingWorker := bus.consumers[consumer.Name()]
	bus.mu.RUnlock()
	eventually(t, func() bool { return len(blockingWorker.mailbox) == 15 }, "dispatcher did not drain the initial ingress events")

	// A separate lightweight consumer must run even while the parser-style
	// consumer is blocked; this protects the terminal/WS fast path.
	fast := &mockDataConsumer{name: "fast", shouldH: true}
	bus.Register(fast)
	bus.Publish(DataEvent{Sequence: 99})
	eventually(t, func() bool {
		events := fast.getEvents()
		return len(events) == 1 && events[0].Sequence == 99
	}, "slow consumer blocked independent fast consumer")
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
	eventually(t, func() bool { return bus.DroppedCount() > 0 }, "full consumer mailbox did not increment dropped count")
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

func TestDataMetricsConsumerCountsSuccessfulAndFailedReports(t *testing.T) {
	processedBefore := counterValue(t, metrics.DataReportsProcessed)
	errorsBefore := counterValue(t, metrics.DataReportErrors)
	consumer := NewDataMetricsConsumer()

	consumer.Handle(DataEvent{RawData: []byte{0x01}})
	consumer.Handle(DataEvent{ErrorCode: 1})

	if got := counterValue(t, metrics.DataReportsProcessed) - processedBefore; got != 1 {
		t.Fatalf("processed metric delta = %v, want 1", got)
	}
	if got := counterValue(t, metrics.DataReportErrors) - errorsBefore; got != 1 {
		t.Fatalf("error metric delta = %v, want 1", got)
	}
}

// TestDBPersistConsumerCountsWriteFailure / TestSensorParserConsumerCountsUnifiedDataWriteFailure
// / TestDataConsumerDBWriteFailuresPrometheusExposition 均已于 2026-11-13 删除：
//
// 前两条的被测对象（device_data 消费者、unified_data 写入）已随整表退役消失；
// 第三条靠"制造一次 unified_data 写失败"来驱动指标，同样失去可触发路径。
// 指标 ehome_data_consumer_db_write_failures_total 仍保留给其它消费者（如
// pending_write / logstream），不再由 sensor_parser 产出。

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
	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, driver)
	captured := captureParsed(consumer)
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

	if len(*captured) != 1 {
		t.Fatalf("解析值数量 = %d, want 1", len(*captured))
	}
	if (*captured)[0].DeviceID != device.ID {
		t.Fatalf("解析值 device_id = %d, want %d", (*captured)[0].DeviceID, device.ID)
	}
}

func TestSensorParserConsumer_CommandAwareDriverFailsClosedWithoutCommandContext(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-command-missing"}
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

	driver := &commandAwareTestDriver{
		calledWith:  make(chan string, 1),
		plainCalled: make(chan struct{}, 1),
	}
	reassembler := &recordingReassembler{}
	consumer := newSensorParserTestConsumer(db, reassembler, driver)
	captured := captureParsed(consumer)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(channel.ID), RequestID: 10,
		RawData: []byte("ambiguous-response"),
	})

	select {
	case <-driver.plainCalled:
		t.Fatal("command-aware driver fell back to plain ParseData without command context")
	default:
	}
	if len(*captured) != 0 {
		t.Fatalf("解析值数量 = %d, want 0（缺少命令上下文必须 fail-closed）", len(*captured))
	}
	if !reassembler.consumedRequest(10) {
		t.Fatal("failed command-aware sample left its reassembly buffer behind")
	}
}

func TestSensorParserConsumer_CommandAwareParseErrorDoesNotFallback(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-command-error"}
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
	template := models.ConfigTemplate{NodeID: node.NodeID, WriteData: "4850560d"}
	if err := db.Create(&template).Error; err != nil {
		t.Fatalf("create template: %v", err)
	}

	driver := &commandAwareTestDriver{
		calledWith:  make(chan string, 1),
		plainCalled: make(chan struct{}, 1),
		commandErr:  errors.New("ambiguous command response"),
	}
	reassembler := &recordingReassembler{}
	consumer := newSensorParserTestConsumer(db, reassembler, driver)
	captured := captureParsed(consumer)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(channel.ID), RequestID: 12,
		CommandTemplateID: uint64(template.ID), RawData: []byte("ambiguous-response"),
	})

	select {
	case <-driver.calledWith:
	default:
		t.Fatal("command-aware parser was not called")
	}
	select {
	case <-driver.plainCalled:
		t.Fatal("command-aware parse error fell back to plain ParseData")
	default:
	}
	if len(*captured) != 0 {
		t.Fatalf("解析值数量 = %d, want 0（解析失败不得产生样本）", len(*captured))
	}
	if !reassembler.consumedRequest(12) {
		t.Fatal("command-aware parse error left its reassembly buffer behind")
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
	device := models.EdgeDevice{Name: "explicit-device", NodeID: node.NodeID, ChannelID: channel.ID, Type: "plain_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	captured := captureParsed(consumer)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, ChannelID: 999, EdgeDeviceID: uint64(device.ID),
		RequestID: 11, RawData: []byte("response"),
	})

	if len(*captured) != 1 || (*captured)[0].DeviceID != device.ID {
		t.Fatalf("explicit edge device id did not route data; captured=%+v", *captured)
	}
}
