package logstream

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// LogEntry represents a single system log line from an ESP32 collector.
type LogEntry struct {
	NodeID  string
	Level   int    // 0=ERROR 1=WARN 2=INFO 3=DEBUG 4=VERBOSE
	Ts      int64  // ESP32 microsecond timestamp
	Tag     string // log tag (e.g. "MQTT", "RX_TASK")
	Message string // log message (no trailing newline)
}

// LogBatch represents a batch of log entries from one MsgLogStream frame.
type LogBatch struct {
	NodeID string
	Seq    int
	Logs   []LogEntry
}

// LogConsumer is the interface that all log consumers must implement.
// Consumers are registered with the LogEventBus and receive log batches.
// Each consumer runs in its own goroutine with panic recovery.
type LogConsumer interface {
	Name() string       // unique identifier for management
	IsActive() bool     // whether this consumer should receive batches
	Consume(batch LogBatch) // process a batch of logs
}

// LogEventBus is the middleware between the MQTT handler (producer) and
// downstream consumers (WS push, DB persist, future webhook/API, etc.).
//
// The bus decouples producers from consumers:
//   - Producers call Publish() without knowing how many consumers exist
//   - Consumers register/unregister dynamically
//   - Each consumer gets its own goroutine per batch (isolated, panic-safe)
//   - Backpressure: bounded channel, drops oldest when full
type LogEventBus struct {
	mu        sync.RWMutex
	consumers []LogConsumer
	logChan   chan LogBatch
	wg        sync.WaitGroup
	dropped   atomic.Uint64
	stopCh    chan struct{}
}

const (
	logChanBufferSize = 64
)

// NewLogEventBus creates and starts a new event bus.
func NewLogEventBus() *LogEventBus {
	bus := &LogEventBus{
		logChan: make(chan LogBatch, logChanBufferSize),
		stopCh:  make(chan struct{}),
	}
	go bus.dispatch()
	return bus
}

// Publish sends a log batch to all active consumers.
// Non-blocking: if the channel is full, the oldest batch is dropped.
func (bus *LogEventBus) Publish(batch LogBatch) {
	select {
	case bus.logChan <- batch:
	default:
		// Channel full — drop oldest, then push
		select {
		case <-bus.logChan:
		default:
		}
		bus.dropped.Add(1)
		bus.logChan <- batch
	}
}

// Register adds a consumer to the bus.
func (bus *LogEventBus) Register(c LogConsumer) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	// Check for duplicate by name
	for _, existing := range bus.consumers {
		if existing.Name() == c.Name() {
			return
		}
	}
	bus.consumers = append(bus.consumers, c)
	slog.Info("logstream: consumer registered", "name", c.Name())
}

// Unregister removes a consumer by name.
func (bus *LogEventBus) Unregister(name string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for i, c := range bus.consumers {
		if c.Name() == name {
			bus.consumers = append(bus.consumers[:i], bus.consumers[i+1:]...)
			slog.Info("logstream: consumer unregistered", "name", name)
			return
		}
	}
}

// DroppedCount returns the number of batches dropped due to backpressure.
func (bus *LogEventBus) DroppedCount() uint64 {
	return bus.dropped.Load()
}

// Stop shuts down the dispatch goroutine and waits for in-flight consumers.
func (bus *LogEventBus) Stop() {
	close(bus.stopCh)
	close(bus.logChan)
	bus.wg.Wait()
}

// dispatch is the core loop that fans out batches to consumers.
func (bus *LogEventBus) dispatch() {
	for {
		select {
		case batch, ok := <-bus.logChan:
			if !ok {
				return
			}
			bus.fanout(batch)
		case <-bus.stopCh:
			return
		}
	}
}

// fanout sends a batch to all active consumers in isolated goroutines.
func (bus *LogEventBus) fanout(batch LogBatch) {
	bus.mu.RLock()
	consumers := make([]LogConsumer, len(bus.consumers))
	copy(consumers, bus.consumers)
	bus.mu.RUnlock()

	for _, c := range consumers {
		if !c.IsActive() {
			continue
		}
		bus.wg.Add(1)
		go func(consumer LogConsumer, b LogBatch) {
			defer bus.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("logstream: consumer panic",
						"consumer", consumer.Name(), "panic", r)
				}
			}()
			consumer.Consume(b)
		}(c, batch)
	}
}
