package databus

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/pkg/metrics"
)

// DataEvent represents a single data report from an ESP32 collector.
type DataEvent struct {
	DeviceID          string
	ChannelID         uint64
	Timestamp         uint64
	Sequence          uint64
	RawData           []byte
	ErrorCode         uint64
	RequestID         uint64
	EdgeDeviceID      uint64
	CommandIndex      uint64 // per-edge-device command array index
	CommandTemplateID uint64 // ConfigTemplate.ID carried by firmware field 9
	ReceivedAt        time.Time
}

// IsPassive identifies uncorrelated terminal data. Scheduler samples use
// request_id=0 too, but carry edge_device_id and must be parsed/stored.
func (e *DataEvent) IsPassive() bool         { return e.RequestID == 0 && e.EdgeDeviceID == 0 }
func (e *DataEvent) IsCommandResponse() bool { return e.RequestID != 0 }
func (e *DataEvent) IsScheduledSample() bool { return e.RequestID == 0 && e.EdgeDeviceID != 0 }
func (e *DataEvent) ShouldPersist() bool     { return !e.IsPassive() }
func (e *DataEvent) ShouldParse() bool {
	return e.ShouldPersist() && e.ErrorCode == 0 && len(e.RawData) > 0
}
func (e *DataEvent) IsError() bool { return e.ErrorCode != 0 }

// DataConsumer handles one data-event category.
type DataConsumer interface {
	Name() string
	ShouldHandle(evt DataEvent) bool
	Handle(evt DataEvent)
}

const (
	dataChanBufferSize  = 256
	consumerMailboxSize = 64
)

type consumerWorker struct {
	consumer DataConsumer
	mailbox  chan DataEvent
}

// DataEventBus has bounded ingress and a dedicated bounded mailbox per
// consumer. Slow parser/DB consumers can therefore never delay terminal/WS
// delivery or create unbounded goroutines.
type DataEventBus struct {
	mu         sync.RWMutex
	consumers  map[string]*consumerWorker
	dataChan   chan DataEvent
	dispatchWG sync.WaitGroup
	workerWG   sync.WaitGroup
	dropped    atomic.Uint64
	stopped    atomic.Bool
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func NewDataEventBus() *DataEventBus {
	bus := &DataEventBus{
		consumers: make(map[string]*consumerWorker),
		dataChan:  make(chan DataEvent, dataChanBufferSize),
		stopCh:    make(chan struct{}),
	}
	bus.dispatchWG.Add(1)
	go bus.dispatch()
	return bus
}

// Publish is non-blocking and safe after Stop. Under ingress pressure it drops
// an oldest event to retain the latest state.
func (bus *DataEventBus) Publish(evt DataEvent) {
	if bus.stopped.Load() {
		return
	}
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
	select {
	case <-bus.dataChan:
		bus.recordDrop()
	default:
	}
	select {
	case bus.dataChan <- evt:
	case <-bus.stopCh:
	default:
		bus.recordDrop()
	}
}

func (bus *DataEventBus) recordDrop() {
	bus.dropped.Add(1)
	metrics.DataEventBusDroppedTotal.Inc()
}

func (bus *DataEventBus) Register(c DataConsumer) {
	if c == nil || bus.stopped.Load() {
		return
	}
	worker := &consumerWorker{consumer: c, mailbox: make(chan DataEvent, consumerMailboxSize)}
	bus.mu.Lock()
	if _, exists := bus.consumers[c.Name()]; exists || bus.stopped.Load() {
		bus.mu.Unlock()
		return
	}
	bus.consumers[c.Name()] = worker
	bus.workerWG.Add(1)
	bus.mu.Unlock()
	go bus.runConsumer(worker)
	slog.Info("databus: consumer registered", "name", c.Name())
}

// Unregister detaches a consumer. Its worker exits on bus shutdown; mailbox is
// intentionally not closed because fanout can hold a read-only snapshot.
func (bus *DataEventBus) Unregister(name string) {
	bus.mu.Lock()
	if _, ok := bus.consumers[name]; ok {
		delete(bus.consumers, name)
		slog.Info("databus: consumer unregistered", "name", name)
	}
	bus.mu.Unlock()
}

func (bus *DataEventBus) DroppedCount() uint64 { return bus.dropped.Load() }

// Stop is idempotent. MQTT producers observe stopped and cannot send to a
// closed channel; workers terminate without close/send races.
func (bus *DataEventBus) Stop() {
	bus.stopOnce.Do(func() {
		bus.stopped.Store(true)
		close(bus.stopCh)
	})
	bus.dispatchWG.Wait()
	bus.workerWG.Wait()
}

func (bus *DataEventBus) dispatch() {
	defer bus.dispatchWG.Done()
	for {
		select {
		case <-bus.stopCh:
			return
		case evt := <-bus.dataChan:
			bus.fanout(evt)
		}
	}
}

func (bus *DataEventBus) fanout(evt DataEvent) {
	bus.mu.RLock()
	workers := make([]*consumerWorker, 0, len(bus.consumers))
	for _, worker := range bus.consumers {
		workers = append(workers, worker)
	}
	bus.mu.RUnlock()
	for _, worker := range workers {
		if !worker.consumer.ShouldHandle(evt) {
			continue
		}
		select {
		case worker.mailbox <- evt:
			continue
		default:
		}
		select {
		case <-worker.mailbox:
			bus.recordDrop()
		default:
		}
		select {
		case worker.mailbox <- evt:
		case <-bus.stopCh:
			return
		default:
			bus.recordDrop()
		}
	}
}

func (bus *DataEventBus) runConsumer(worker *consumerWorker) {
	defer bus.workerWG.Done()
	for {
		select {
		case <-bus.stopCh:
			return
		case evt := <-worker.mailbox:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("databus: consumer panic", "consumer", worker.consumer.Name(), "panic", r)
					}
				}()
				worker.consumer.Handle(evt)
			}()
		}
	}
}
