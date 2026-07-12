package nodemgr

import (
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/logstream"
	"ehome/backend/pkg/frame"
)

type recordingLogConsumer struct {
	mu      sync.Mutex
	batches []logstream.LogBatch
}

func (c *recordingLogConsumer) Name() string   { return "recording" }
func (c *recordingLogConsumer) IsActive() bool { return true }
func (c *recordingLogConsumer) Consume(batch logstream.LogBatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, batch)
}
func (c *recordingLogConsumer) snapshot() []logstream.LogBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]logstream.LogBatch(nil), c.batches...)
}

func TestParseLogEntry_ESP32SubFrameWithoutMessageType(t *testing.T) {
	// ESP32 nested entries are raw field sequences; they must not contain a
	// top-level message-type byte. This verifies the exact decoder contract.
	sub := frame.SubEncoder()
	sub.EncodeVarint(1, 2)
	sub.EncodeVarint(2, 1234567)
	sub.EncodeString(3, "CALLBACK")
	sub.EncodeString(4, "remote log stream enabled, level=2")

	got := parseLogEntry(sub.Bytes())
	if got == nil {
		t.Fatal("parseLogEntry returned nil")
	}
	if got.Level != 2 || got.Ts != 1234567 || got.Tag != "CALLBACK" || got.Message != "remote log stream enabled, level=2" {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestParseLogEntry_RejectsEmptySubFrame(t *testing.T) {
	if got := parseLogEntry(nil); got != nil {
		t.Fatalf("parseLogEntry(nil) = %+v, want nil", got)
	}
}

func TestHandleLogStream_DecodesAndPublishesBatch(t *testing.T) {
	bus := logstream.NewLogEventBus()
	defer bus.Stop()
	recorder := &recordingLogConsumer{}
	bus.Register(recorder)
	manager := &Manager{logBus: bus}

	entry1 := frame.SubEncoder()
	entry1.EncodeVarint(1, 2)
	entry1.EncodeVarint(2, 123)
	entry1.EncodeString(3, "MQTT")
	entry1.EncodeString(4, "connected")
	entry2 := frame.SubEncoder()
	entry2.EncodeVarint(1, 1)
	entry2.EncodeVarint(2, 456)
	entry2.EncodeString(3, "RX")
	entry2.EncodeString(4, "timeout")
	enc := frame.NewEncoder(frame.MsgLogStream)
	enc.EncodeVarint(1, 2)
	enc.EncodeVarint(2, 9)
	enc.EncodeSubFrame(3, entry1.Bytes())
	enc.EncodeSubFrame(3, entry2.Bytes())

	manager.handleLogStream("node-log", enc.Bytes())
	deadline := time.Now().Add(time.Second)
	for len(recorder.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	batches := recorder.snapshot()
	if len(batches) != 1 {
		t.Fatalf("published batches=%d want 1", len(batches))
	}
	batch := batches[0]
	if batch.NodeID != "node-log" || batch.Seq != 9 || len(batch.Logs) != 2 {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	if batch.Logs[0].NodeID != "node-log" || batch.Logs[0].Tag != "MQTT" || batch.Logs[1].Message != "timeout" {
		t.Fatalf("unexpected entries: %+v", batch.Logs)
	}
}

func TestHandleLogStream_EmptyPayloadDoesNotPublish(t *testing.T) {
	bus := logstream.NewLogEventBus()
	defer bus.Stop()
	recorder := &recordingLogConsumer{}
	bus.Register(recorder)
	(&Manager{logBus: bus}).handleLogStream("node-log", []byte{frame.MsgLogStream})
	time.Sleep(20 * time.Millisecond)
	if len(recorder.snapshot()) != 0 {
		t.Fatal("empty payload must not publish a batch")
	}
}
