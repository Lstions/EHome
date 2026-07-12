package logstream

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"ehome/backend/pkg/metrics"
)

// LogEntry represents a single structured system log line from an ESP32 collector.
// Ts is ESP monotonic uptime in microseconds, not a wall-clock timestamp.
type LogEntry struct {
	NodeID  string
	Level   int    // 0=ERROR 1=WARN 2=INFO 3=DEBUG 4=VERBOSE
	Ts      int64  // ESP32 microsecond uptime
	Tag     string // component tag (for example MQTT or RX_TASK)
	Message string // message without a trailing newline
}

// LogBatch represents entries received in one MsgLogStream frame.
type LogBatch struct {
	NodeID string
	Seq    int
	Logs   []LogEntry
}

// LogConsumer receives batches serially through its own bounded mailbox.
// Consume must return promptly; a blocked consumer never blocks the dispatcher
// or other consumers, but can have its own oldest batches dropped.
type LogConsumer interface {
	Name() string
	IsActive() bool
	Consume(batch LogBatch)
}

const (
	logChanBufferSize   = 64
	consumerMailboxSize = 16
)

type consumerWorker struct {
	consumer LogConsumer
	mailbox  chan LogBatch
	done     chan struct{}
	doneOnce sync.Once
}

// LogEventBus owns one bounded ingress queue and one bounded serial worker per
// registered consumer. This bounds goroutine and queued-batch growth even when a
// downstream DB/websocket consumer is slow. Publish is safe during and after Stop.
type LogEventBus struct {
	mu        sync.RWMutex
	consumers map[string]*consumerWorker
	logChan   chan LogBatch
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
	dropped   atomic.Uint64
	stopped   atomic.Bool
}

func NewLogEventBus() *LogEventBus {
	bus := &LogEventBus{
		consumers: make(map[string]*consumerWorker),
		logChan:   make(chan LogBatch, logChanBufferSize),
		stopCh:    make(chan struct{}),
	}
	bus.wg.Add(1)
	go bus.dispatch()
	return bus
}

// Publish drops the oldest ingress batch under pressure. It is intentionally
// non-blocking because it runs on the MQTT receive path.
func (bus *LogEventBus) Publish(batch LogBatch) {
	if bus.stopped.Load() {
		return
	}
	select {
	case <-bus.stopCh:
		return
	default:
	}

	select {
	case bus.logChan <- batch:
		return
	case <-bus.stopCh:
		return
	default:
	}

	select {
	case <-bus.logChan:
		bus.recordDrop("ingress", "")
	default:
	}
	select {
	case bus.logChan <- batch:
	case <-bus.stopCh:
	default:
		// A racing dispatcher filled the channel again; preserve non-blocking behavior.
		bus.recordDrop("ingress", "")
	}
}

// Register adds a consumer and starts exactly one bounded mailbox worker.
func (bus *LogEventBus) Register(consumer LogConsumer) {
	if consumer == nil || bus.stopped.Load() {
		return
	}

	worker := &consumerWorker{
		consumer: consumer,
		mailbox:  make(chan LogBatch, consumerMailboxSize),
		done:     make(chan struct{}),
	}
	bus.mu.Lock()
	if _, exists := bus.consumers[consumer.Name()]; exists || bus.stopped.Load() {
		bus.mu.Unlock()
		return
	}
	bus.consumers[consumer.Name()] = worker
	bus.wg.Add(1)
	bus.mu.Unlock()

	go bus.runConsumer(worker)
	slog.Info("logstream: consumer registered", "name", consumer.Name())
}

// Unregister detaches and terminates a consumer worker. The mailbox is not
// closed because fanout may hold a snapshot; done is safe to select on.
func (bus *LogEventBus) Unregister(name string) {
	bus.mu.Lock()
	worker, exists := bus.consumers[name]
	if exists {
		delete(bus.consumers, name)
	}
	bus.mu.Unlock()
	if exists {
		worker.doneOnce.Do(func() { close(worker.done) })
		slog.Info("logstream: consumer unregistered", "name", name)
	}
}

func (bus *LogEventBus) DroppedCount() uint64 { return bus.dropped.Load() }

func (bus *LogEventBus) recordDrop(stage, consumer string) {
	bus.dropped.Add(1)
	metrics.LogEventBusDroppedTotal.WithLabelValues(stage, consumer).Inc()
}

// Stop is idempotent and waits for the dispatcher plus every consumer worker.
// It does not close ingress/mailbox channels, eliminating concurrent send/close
// panics from MQTT delivery or a dispatcher fan-out race.
func (bus *LogEventBus) Stop() {
	bus.stopOnce.Do(func() {
		bus.stopped.Store(true)
		close(bus.stopCh)
		bus.mu.RLock()
		workers := make([]*consumerWorker, 0, len(bus.consumers))
		for _, worker := range bus.consumers {
			workers = append(workers, worker)
		}
		bus.mu.RUnlock()
		for _, worker := range workers {
			worker.doneOnce.Do(func() { close(worker.done) })
		}
	})
	bus.wg.Wait()
}

func (bus *LogEventBus) dispatch() {
	defer bus.wg.Done()
	for {
		select {
		case <-bus.stopCh:
			return
		case batch := <-bus.logChan:
			bus.fanout(batch)
		}
	}
}

func (bus *LogEventBus) fanout(batch LogBatch) {
	bus.mu.RLock()
	workers := make([]*consumerWorker, 0, len(bus.consumers))
	for _, worker := range bus.consumers {
		workers = append(workers, worker)
	}
	bus.mu.RUnlock()

	for _, worker := range workers {
		if !worker.consumer.IsActive() {
			continue
		}
		select {
		case worker.mailbox <- batch:
			continue
		case <-worker.done:
			continue
		default:
		}
		select {
		case <-worker.mailbox:
			bus.recordDrop("consumer", worker.consumer.Name())
		default:
		}
		select {
		case worker.mailbox <- batch:
		case <-worker.done:
			return
		case <-bus.stopCh:
			return
		default:
			bus.recordDrop("consumer", worker.consumer.Name())
		}
	}
}

func (bus *LogEventBus) runConsumer(worker *consumerWorker) {
	defer bus.wg.Done()
	for {
		select {
		case <-worker.done:
			return
		case batch := <-worker.mailbox:
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						slog.Error("logstream: consumer panic", "consumer", worker.consumer.Name(), "panic", recovered)
					}
				}()
				worker.consumer.Consume(batch)
			}()
		}
	}
}
