package databus

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/pkg/metrics"
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

// IsPassive returns true only for uncorrelated terminal/RX data. Scheduler
// samples use request_id=0 too, but carry edge_device_id and must be parsed.
func (e *DataEvent) IsPassive() bool {
	return e.RequestID == 0 && e.EdgeDeviceID == 0
}

// IsCommandResponse returns true for pending write/read command responses.
func (e *DataEvent) IsCommandResponse() bool {
	return e.RequestID != 0
}

// IsScheduledSample returns true for scheduler reports addressed to a known
// edge device. ESP32 emits these with request_id=0.
func (e *DataEvent) IsScheduledSample() bool {
	return e.RequestID == 0 && e.EdgeDeviceID != 0
}

// ShouldPersist returns true for reports that belong in device history.
func (e *DataEvent) ShouldPersist() bool {
	return !e.IsPassive()
}

// ShouldParse returns true for successful reports associated with an edge
// device or a correlated command response.
func (e *DataEvent) ShouldParse() bool {
	return e.ShouldPersist() && e.ErrorCode == 0 && len(e.RawData) > 0
}

// IsError returns true for error reports (e.g. RX timeout).
func (e *DataEvent) IsError() bool {
	return e.ErrorCode != 0
}

// DataConsumer is the interface for all data event consumers.
// Consumers execute in a fixed, panic-isolated worker pool.
type DataConsumer interface {
	Name() string
	ShouldHandle(evt DataEvent) bool
	Handle(evt DataEvent)
}

// DataEventBus decouples the MQTT handler (producer) from data consumers.
// It uses bounded ingress and worker queues, a fixed worker pool, and panic
// recovery so slow consumers cannot create unbounded goroutines.
type DataEventBus struct {
	mu         sync.RWMutex
	consumers  []DataConsumer
	dataChan   chan DataEvent
	workChan   chan consumerWork
	wg         sync.WaitGroup
	dispatchWG sync.WaitGroup
	dropped    atomic.Uint64
	stopCh     chan struct{}
	stopOnce   sync.Once
}

type consumerWork struct {
	consumer DataConsumer
	event    DataEvent
}

const (
	dataChanBufferSize      = 256
	dataConsumerWorkerCount = 8
	dataConsumerQueueSize   = 256
)

// NewDataEventBus creates and starts a new data event bus.
func NewDataEventBus() *DataEventBus {
	bus := &DataEventBus{
		dataChan: make(chan DataEvent, dataChanBufferSize),
		workChan: make(chan consumerWork, dataConsumerQueueSize),
		stopCh:   make(chan struct{}),
	}
	bus.dispatchWG.Add(1)
	go bus.dispatch()
	for i := 0; i < dataConsumerWorkerCount; i++ {
		bus.wg.Add(1)
		go bus.consume()
	}
	return bus
}

// Publish sends a data event to all active consumers.
// It is non-blocking. During overload, the oldest queued event is dropped; after
// shutdown it is a no-op so MQTT callbacks cannot panic during teardown.
func (bus *DataEventBus) Publish(evt DataEvent) {
	select {
	case <-bus.stopCh:
		return
	default:
	}

	select {
	case bus.dataChan <- evt:
		return
	default:
	}

	// Drop the oldest event while retaining the newest report. A concurrent
	// dispatcher may empty the channel between these selects, so retry the send
	// non-blockingly instead of risking a blocked MQTT callback.
	select {
	case <-bus.dataChan:
		bus.dropped.Add(1)
		metrics.DataEventBusDroppedTotal.Inc()
	default:
	}
	select {
	case bus.dataChan <- evt:
	default:
		bus.dropped.Add(1)
		metrics.DataEventBusDroppedTotal.Inc()
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

// Stop shuts down dispatch and consumer workers after draining queued work.
// It is safe to call repeatedly.
func (bus *DataEventBus) Stop() {
	bus.stopOnce.Do(func() {
		close(bus.stopCh)
		bus.dispatchWG.Wait()
		bus.wg.Wait()
	})
}

// dispatch fans out events to matching consumers via a bounded work queue.
func (bus *DataEventBus) dispatch() {
	defer bus.dispatchWG.Done()
	defer close(bus.workChan)

	for {
		select {
		case evt := <-bus.dataChan:
			bus.fanout(evt)
		case <-bus.stopCh:
			for {
				select {
				case evt := <-bus.dataChan:
					bus.fanout(evt)
				default:
					return
				}
			}
		}
	}
}

// fanout submits matching consumers to a bounded worker queue. If the queue is
// full, dispatcher backpressure preserves ordering and prevents unbounded
// goroutine creation.
func (bus *DataEventBus) fanout(evt DataEvent) {
	bus.mu.RLock()
	consumers := make([]DataConsumer, len(bus.consumers))
	copy(consumers, bus.consumers)
	bus.mu.RUnlock()

	for _, c := range consumers {
		if !c.ShouldHandle(evt) {
			continue
		}
		bus.workChan <- consumerWork{consumer: c, event: evt}
	}
}

func (bus *DataEventBus) consume() {
	defer bus.wg.Done()
	for work := range bus.workChan {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("databus: consumer panic", "consumer", work.consumer.Name(), "panic", r)
				}
			}()
			work.consumer.Handle(work.event)
		}()
	}
}
