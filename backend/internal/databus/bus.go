package databus

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DataEvent represents a single data report from an ESP32 collector.
// Created by the MQTT handler after parsing a DataReport (0x03) frame.
type DataEvent struct {
	DeviceID     string    // collector node_id
	ChannelID    uint64    // channel ID
	Timestamp    uint64    // ESP32 microsecond timestamp
	Sequence     uint64    // sequence number
	RawData      []byte    // raw data bytes
	ErrorCode    uint64    // 0=ok, 0x01=RX timeout
	RequestID    uint64    // 0=passive/terminal data, ≠0=command response
	EdgeDeviceID uint64    // 0=unknown
	CommandIndex uint64    // command template index
	ReceivedAt   time.Time // backend receive time
}

// IsPassive returns true for passive/terminal data (no pending command).
func (e *DataEvent) IsPassive() bool {
	return e.RequestID == 0 && e.EdgeDeviceID == 0
}

// IsCommandResponse returns true for scheduled command responses.
func (e *DataEvent) IsCommandResponse() bool {
	return e.RequestID != 0
}

// IsError returns true for error reports (e.g. RX timeout).
func (e *DataEvent) IsError() bool {
	return e.ErrorCode != 0
}

// DataConsumer is the interface for all data event consumers.
// Each consumer runs in its own goroutine per event (isolated, panic-safe).
type DataConsumer interface {
	Name() string
	ShouldHandle(evt DataEvent) bool
	Handle(evt DataEvent)
}

// DataEventBus decouples the MQTT handler (producer) from data consumers.
// Same architecture as LogEventBus: bounded channel + dispatch goroutine +
// per-consumer goroutine fanout with panic recovery.
type DataEventBus struct {
	mu        sync.RWMutex
	consumers []DataConsumer
	dataChan  chan DataEvent
	wg        sync.WaitGroup
	dropped   atomic.Uint64
	stopCh    chan struct{}
}

const (
	dataChanBufferSize = 256
)

// NewDataEventBus creates and starts a new data event bus.
func NewDataEventBus() *DataEventBus {
	bus := &DataEventBus{
		dataChan: make(chan DataEvent, dataChanBufferSize),
		stopCh:   make(chan struct{}),
	}
	go bus.dispatch()
	return bus
}

// Publish sends a data event to all active consumers.
// Non-blocking: if the channel is full, the oldest event is dropped.
func (bus *DataEventBus) Publish(evt DataEvent) {
	select {
	case bus.dataChan <- evt:
	default:
		select {
		case <-bus.dataChan:
		default:
		}
		bus.dropped.Add(1)
		bus.dataChan <- evt
	}
}

// Register adds a consumer to the bus.
func (bus *DataEventBus) Register(c DataConsumer) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for _, existing := range bus.consumers {
		if existing.Name() == c.Name() {
			return
		}
	}
	bus.consumers = append(bus.consumers, c)
	slog.Info("databus: consumer registered", "name", c.Name())
}

// Unregister removes a consumer by name.
func (bus *DataEventBus) Unregister(name string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for i, c := range bus.consumers {
		if c.Name() == name {
			bus.consumers = append(bus.consumers[:i], bus.consumers[i+1:]...)
			slog.Info("databus: consumer unregistered", "name", name)
			return
		}
	}
}

// DroppedCount returns the number of events dropped due to backpressure.
func (bus *DataEventBus) DroppedCount() uint64 {
	return bus.dropped.Load()
}

// Stop shuts down the dispatch goroutine and waits for in-flight consumers.
func (bus *DataEventBus) Stop() {
	close(bus.stopCh)
	close(bus.dataChan)
	bus.wg.Wait()
}

// dispatch is the core loop that fans out events to consumers.
func (bus *DataEventBus) dispatch() {
	for {
		select {
		case evt, ok := <-bus.dataChan:
			if !ok {
				return
			}
			bus.fanout(evt)
		case <-bus.stopCh:
			return
		}
	}
}

// fanout sends an event to all consumers whose ShouldHandle returns true.
// Each consumer runs in its own goroutine with panic recovery.
func (bus *DataEventBus) fanout(evt DataEvent) {
	bus.mu.RLock()
	consumers := make([]DataConsumer, len(bus.consumers))
	copy(consumers, bus.consumers)
	bus.mu.RUnlock()

	for _, c := range consumers {
		if !c.ShouldHandle(evt) {
			continue
		}
		bus.wg.Add(1)
		go func(consumer DataConsumer, e DataEvent) {
			defer bus.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("databus: consumer panic",
						"consumer", consumer.Name(), "panic", r)
				}
			}()
			consumer.Handle(e)
		}(c, evt)
	}
}
