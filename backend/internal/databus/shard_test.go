package databus

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type shardRecordingConsumer struct {
	name  string
	mu    sync.Mutex
	seen  []string
	block chan struct{}
}

func (c *shardRecordingConsumer) Name() string { return c.name }
func (c *shardRecordingConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldParse() || evt.ShouldPersist()
}
func (c *shardRecordingConsumer) Handle(evt DataEvent) {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	c.seen = append(c.seen, evt.DeviceID)
	c.mu.Unlock()
}

func (c *shardRecordingConsumer) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func TestShardConsumer_RoutesDeviceToExactlyOneShard(t *testing.T) {
	const shards = 8
	inner := &shardRecordingConsumer{name: "parser"}
	bus := NewDataEventBus()
	for i := 0; i < shards; i++ {
		bus.Register(NewShardConsumer(inner, i, shards))
	}

	// Publish in digestible waves: the bus ingress (256) and each shard
	// mailbox (64) are bounded, and Publish drops oldest under pressure, so a
	// fire-hose burst would legitimately lose events. Waves smaller than
	// capacity make this a pure routing/ordering test, not a backpressure one.
	const devices = 100
	const repeats = 3
	published := 0
	for i := 0; i < devices; i++ {
		device := fmt.Sprintf("node-%03d", i)
		for r := 0; r < repeats; r++ {
			bus.Publish(DataEvent{
				DeviceID:     device,
				EdgeDeviceID: uint64(i + 1),
				RawData:      []byte{0x01},
				ReceivedAt:   time.Now(),
			})
			published++
		}
		waitForCount(t, inner, published, 3*time.Second)
	}
	got := inner.snapshot()
	if len(got) != published {
		t.Fatalf("processed %d of %d events", len(got), published)
	}

	// Routing: each device must have been accepted by exactly one shard.
	perDevice := map[string]int{}
	for _, d := range got {
		perDevice[d]++
	}
	for device, n := range perDevice {
		if n != 3 {
			t.Fatalf("device %s processed %d times, want 3 (one shard, in order)", device, n)
		}
	}

	// Distribution: with 200 devices across 8 shards every shard should get
	// work (FNV-1a spreads far better than 1/8 minimum).
	bus.mu.RLock()
	registered := len(bus.consumers)
	bus.mu.RUnlock()
	if registered != shards {
		t.Fatalf("registered %d consumers, want %d", registered, shards)
	}
	bus.Stop()
}

func TestShardConsumer_PerDeviceOrdering(t *testing.T) {
	const shards = 4
	var mu sync.Mutex
	handled := map[string][]uint64{}
	block := make(chan struct{})
	inner := &orderedConsumer{mu: &mu, handled: handled, block: block}

	bus := NewDataEventBus()
	for i := 0; i < shards; i++ {
		bus.Register(NewShardConsumer(inner, i, shards))
	}

	const device = "ordering-node"
	for seq := uint64(1); seq <= 50; seq++ {
		bus.Publish(DataEvent{
			DeviceID:     device,
			EdgeDeviceID: 1,
			RawData:      []byte{0x01},
			Sequence:     seq,
			ReceivedAt:   time.Now(),
		})
	}
	// Let every event reach its (single) shard mailbox before unblocking.
	time.Sleep(100 * time.Millisecond)
	close(block)

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(handled[device])
		mu.Unlock()
		if n == 50 {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("handled %d/50 events for device: %+v", n, handled)
			mu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, seq := range handled[device] {
		if seq != uint64(i+1) {
			t.Fatalf("device order broken at index %d: got seq %d, want %d (full: %v)", i, seq, i+1, handled[device])
		}
	}
	bus.Stop()
}

type orderedConsumer struct {
	mu      *sync.Mutex
	handled map[string][]uint64
	block   <-chan struct{}
}

func (c *orderedConsumer) Name() string { return "ordered" }
func (c *orderedConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldParse()
}
func (c *orderedConsumer) Handle(evt DataEvent) {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	c.handled[evt.DeviceID] = append(c.handled[evt.DeviceID], evt.Sequence)
	c.mu.Unlock()
}

func TestShardConsumer_FullShardDoesNotBlockOthers(t *testing.T) {
	const shards = 2
	// Shard layout is deterministic; find one device per shard.
	var devA, devB string
	for i := 0; ; i++ {
		cand := fmt.Sprintf("node-%d", i)
		switch shardForDevice(cand, shards) {
		case 0:
			if devA == "" {
				devA = cand
			}
		case 1:
			if devB == "" {
				devB = cand
			}
		}
		if devA != "" && devB != "" {
			break
		}
	}

	var mu sync.Mutex
	handledBy := map[string]int{}
	blocking := &blockingConsumer{mu: &mu, handledBy: handledBy, entered: make(chan struct{}, 64)}

	bus := NewDataEventBus()
	for i := 0; i < shards; i++ {
		bus.Register(NewShardConsumer(blocking, i, shards))
	}

	// First event occupies shard worker indefinitely.
	bus.Publish(DataEvent{DeviceID: devA, EdgeDeviceID: 1, RawData: []byte{0x01}, ReceivedAt: time.Now()})
	<-blocking.entered

	// Overflow shard 0's mailbox with more devA events (device must stay on shard 0).
	for i := 0; i < consumerMailboxSize+10; i++ {
		bus.Publish(DataEvent{DeviceID: devA, EdgeDeviceID: 1, RawData: []byte{0x01}, ReceivedAt: time.Now()})
	}

	// devB lives on shard 1 and must still be processed while shard 0 is stuck.
	deadline := time.Now().Add(2 * time.Second)
	processed := false
	for i := 0; i < 5 && !processed; i++ {
		bus.Publish(DataEvent{DeviceID: devB, EdgeDeviceID: 2, RawData: []byte{0x01}, ReceivedAt: time.Now()})
		for time.Now().Before(deadline) {
			mu.Lock()
			n := handledBy[devB]
			mu.Unlock()
			if n > 0 {
				processed = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !processed {
		t.Fatal("shard 1 made no progress while shard 0 was blocked — shards are not independent")
	}

	blocking.releaseOnce()
	bus.Stop()
}

type blockingConsumer struct {
	mu        *sync.Mutex
	handledBy map[string]int
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (c *blockingConsumer) Name() string { return "blocking" }
func (c *blockingConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldParse()
}
func (c *blockingConsumer) Handle(evt DataEvent) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	if c.release != nil {
		<-c.release
	}
	c.mu.Lock()
	c.handledBy[evt.DeviceID]++
	c.mu.Unlock()
}

func (c *blockingConsumer) releaseOnce() {
	c.once.Do(func() {
		c.release = make(chan struct{})
		close(c.release)
	})
}

func TestShardForDevice_StableAcrossShardCounts(t *testing.T) {
	// Different N may reassign devices (expected), but the same N must be stable.
	for _, shards := range []int{1, 2, 4, 8, 16} {
		for _, device := range []string{"", "a", "SIM0001", "esp32-ࠁ", "node-42"} {
			first := shardForDevice(device, shards)
			for i := 0; i < 100; i++ {
				if got := shardForDevice(device, shards); got != first || got < 0 || got >= shards {
					t.Fatalf("unstable shard for %q n=%d: %d then %d", device, shards, first, got)
				}
			}
		}
	}
}

func waitForCount(t *testing.T, c *shardRecordingConsumer, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for len(c.snapshot()) < want && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
}
