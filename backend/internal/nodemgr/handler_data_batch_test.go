package nodemgr

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/databus"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// =============================================================================
// V3-2a DataBatch (0x20) tests.
//
// Two layers are pinned here:
//   1. parseDataBatch — the strict wire decoder (contract §2.1 invariants).
//   2. handleDataBatch — the fan-out, asserted on the SAME DataEvent type the
//      0x03 path produces, because "前端零改动" is only true if the two paths
//      are indistinguishable downstream.
//
// The anchor tests use hard-coded byte strings that were derived from the frozen
// §2 layout, not from backend/pkg/frame, so an encoder/decoder that agree only
// with themselves cannot pass them. They were subsequently confirmed to be
// BYTE-EXACT against the real firmware encoder
// (esp32-collector/components/msg_handler/data_batch_codec.c), compiled and run
// out of tree — see TestParseDataBatchFirmwareFullSizeFrame for the full-size
// vector and the provenance note.
// =============================================================================

type batchSample struct {
	deltaUS uint64
	raw     []byte
}

// dataBatchWire is a configurable 0x20 encoder used to build both valid frames
// and one-invariant-broken frames. Nothing here validates anything — that is the
// parser's job.
type dataBatchWire struct {
	count        uint64
	baseTS       uint64
	firstSeq     uint64
	channelID    uint64
	samples      []batchSample
	edgeDeviceID *uint64
	templateID   *uint64
	commandIndex *uint64

	// omission switches (each must independently make the frame invalid)
	omitCount    bool
	omitBase     bool
	omitFirstSeq bool
	omitChannel  bool

	// rawHead is injected between the header fields and the samples;
	// rawTail is appended after everything. Both are raw wire bytes.
	rawHead []byte
	rawTail []byte

	// sampleOverride, when non-nil, replaces the encoded field 5 payloads
	// verbatim (used for malformed/duplicate/missing sub-fields).
	sampleOverride [][]byte
}

func (w dataBatchWire) bytes() []byte {
	enc := frame.NewEncoder(frame.MsgDataBatch)
	if !w.omitCount {
		enc.EncodeVarint(dataBatchFieldCount, w.count)
	}
	if !w.omitBase {
		enc.EncodeVarint(dataBatchFieldBaseTimestampUS, w.baseTS)
	}
	if !w.omitFirstSeq {
		enc.EncodeVarint(dataBatchFieldFirstSequence, w.firstSeq)
	}
	if !w.omitChannel {
		enc.EncodeVarint(dataBatchFieldChannelID, w.channelID)
	}
	// rawHead is injected between the header fields and the samples.
	out := enc.Bytes()
	if w.rawHead != nil {
		out = append(out, w.rawHead...)
	}
	if w.sampleOverride != nil {
		for _, payload := range w.sampleOverride {
			out = append(out, encodeField5(payload)...)
		}
	} else {
		for _, s := range w.samples {
			sub := frame.SubEncoder()
			sub.EncodeVarint(dataBatchSampleFieldDeltaUS, s.deltaUS)
			sub.EncodeBytes(dataBatchSampleFieldRawData, s.raw)
			out = append(out, encodeField5(sub.Bytes())...)
		}
	}
	// Batch-level scalar fields are appended as RAW wire bytes via a throwaway
	// sub-encoder. frame.NewEncoder(0) would prepend a 0x00 message-type byte,
	// which is a field-0 tag (illegal) and would corrupt the frame.
	if w.edgeDeviceID != nil {
		out = append(out, encodeField(dataBatchFieldEdgeDeviceID, *w.edgeDeviceID)...)
	}
	if w.templateID != nil {
		out = append(out, encodeField(dataBatchFieldCommandTemplateID, *w.templateID)...)
	}
	if w.commandIndex != nil {
		out = append(out, encodeField(dataBatchFieldCommandIndex, *w.commandIndex)...)
	}
	if w.rawTail != nil {
		out = append(out, w.rawTail...)
	}
	return out
}

// encodeField5 wraps a sub-message payload as the length-delimited field 5.
func encodeField5(payload []byte) []byte {
	enc := frame.SubEncoder()
	enc.EncodeSubFrame(dataBatchFieldSample, payload)
	return enc.Bytes()
}

// encodeField encodes one varint field as raw wire bytes.
func encodeField(fieldNum uint8, value uint64) []byte {
	enc := frame.SubEncoder()
	enc.EncodeVarint(fieldNum, value)
	return enc.Bytes()
}

func u64ptr(v uint64) *uint64 { return &v }

// --- fan-out capture -------------------------------------------------------

type batchCaptureConsumer struct {
	mu     sync.Mutex
	events []databus.DataEvent
}

func (c *batchCaptureConsumer) Name() string { return "data_batch_test_capture" }
func (c *batchCaptureConsumer) ShouldHandle(databus.DataEvent) bool {
	return true
}
func (c *batchCaptureConsumer) Handle(evt databus.DataEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
}

func (c *batchCaptureConsumer) snapshot() []databus.DataEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]databus.DataEvent, len(c.events))
	copy(out, c.events)
	return out
}

// newBatchTestManager wires a Manager whose worker pool drains into a real
// DataEventBus, mirroring production (handleDataBatch → enqueue → worker →
// processDataReportJob → dataBus.Publish → consumer).
func newBatchTestManager(t *testing.T) (*Manager, *batchCaptureConsumer) {
	t.Helper()
	bus := databus.NewDataEventBus()
	capture := &batchCaptureConsumer{}
	bus.Register(capture)
	mgr := &Manager{
		dataCh:  make(chan dataReportJob, defaultJobBuffer),
		dataBus: bus,
	}
	for i := 0; i < defaultWorkerCount; i++ {
		mgr.wg.Add(1)
		go mgr.dataWorker(i)
	}
	t.Cleanup(func() {
		close(mgr.dataCh)
		mgr.wg.Wait()
		bus.Stop()
	})
	return mgr, capture
}

// waitForEvents blocks until the capture consumer has seen want events.
func waitForEvents(t *testing.T, c *batchCaptureConsumer, want int) []databus.DataEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.snapshot(); len(got) >= want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("captured %d event(s), want %d", len(c.snapshot()), want)
	return nil
}

// indexBySequence keys the captured events by their wire sequence, so a test can
// assert the exact SET of fanned-out samples without depending on the order in
// which the shared worker pool happened to finish them.
func indexBySequence(t *testing.T, events []databus.DataEvent) map[uint64]databus.DataEvent {
	t.Helper()
	out := make(map[uint64]databus.DataEvent, len(events))
	for _, evt := range events {
		if _, dup := out[evt.Sequence]; dup {
			t.Fatalf("duplicate sequence %d in fan-out: %v", evt.Sequence, sequenceList(events))
		}
		out[evt.Sequence] = evt
	}
	return out
}

func sequenceList(events []databus.DataEvent) []uint64 {
	out := make([]uint64, 0, len(events))
	for _, evt := range events {
		out = append(out, evt.Sequence)
	}
	return out
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var out dto.Metric
	if err := (<-ch).Write(&out); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return out.GetCounter().GetValue()
}

// =============================================================================
// 1. Byte anchor — hard-coded hex derived from contract §2, decoded by the parser.
// =============================================================================

// TestParseDataBatchByteAnchor decodes a frame whose bytes were produced
// independently of backend/pkg/frame.
//
// Shape (contract §2):
//
//	count=2, base_timestamp_us=1700000000000, first_sequence=4096, channel_id=3
//	sample[0]: delta_us=0,     raw=0103020000b844
//	sample[1]: delta_us=10000, raw=0204aabbccdd
//	edge_device_id=42, command_template_id=7, command_index=1
//
// A regression in varint/tag/sub-frame handling (for example encoding a
// sub-message without its length prefix) changes these bytes and turns this
// test red even though encoder and decoder would still agree with each other.
func TestParseDataBatchByteAnchor(t *testing.T) {
	const anchorHex = "2008021080d095ffbc3118802020032a0b080012070103020000b8442a0b08904e12060204aabbccdd302a38074001"

	payload, err := hex.DecodeString(anchorHex)
	if err != nil {
		t.Fatalf("decode anchor hex: %v", err)
	}
	if payload[0] != frame.MsgDataBatch {
		t.Fatalf("anchor msg type = 0x%02X, want 0x%02X", payload[0], frame.MsgDataBatch)
	}

	batch, err := parseDataBatch(payload)
	if err != nil {
		t.Fatalf("parseDataBatch(anchor): %v", err)
	}
	if batch.count != 2 {
		t.Errorf("count = %d, want 2", batch.count)
	}
	if batch.baseTimestampUS != 1700000000000 {
		t.Errorf("base_timestamp_us = %d, want 1700000000000", batch.baseTimestampUS)
	}
	if batch.firstSequence != 4096 {
		t.Errorf("first_sequence = %d, want 4096", batch.firstSequence)
	}
	if batch.channelID != 3 {
		t.Errorf("channel_id = %d, want 3", batch.channelID)
	}
	if batch.edgeDeviceID != 42 || batch.commandTemplateID != 7 || batch.commandIndex != 1 {
		t.Errorf("extras = edge %d template %d index %d, want 42/7/1",
			batch.edgeDeviceID, batch.commandTemplateID, batch.commandIndex)
	}
	if len(batch.samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(batch.samples))
	}
	if batch.samples[0].deltaUS != 0 || hex.EncodeToString(batch.samples[0].rawData) != "0103020000b844" {
		t.Errorf("sample[0] = {%d, %x}, want {0, 0103020000b844}", batch.samples[0].deltaUS, batch.samples[0].rawData)
	}
	if batch.samples[1].deltaUS != 10000 || hex.EncodeToString(batch.samples[1].rawData) != "0204aabbccdd" {
		t.Errorf("sample[1] = {%d, %x}, want {10000, 0204aabbccdd}", batch.samples[1].deltaUS, batch.samples[1].rawData)
	}
}

// TestParseDataBatchSingleSampleAnchor is the n=1 shape the firmware emits when
// the queue holds a single non-critical sample (no batching possible).
func TestParseDataBatchSingleSampleAnchor(t *testing.T) {
	const anchorHex = "2008011080d095ffbc3118802020032a0b080012070103020000b844"

	payload, err := hex.DecodeString(anchorHex)
	if err != nil {
		t.Fatalf("decode anchor hex: %v", err)
	}
	batch, err := parseDataBatch(payload)
	if err != nil {
		t.Fatalf("parseDataBatch(n=1 anchor): %v", err)
	}
	if batch.count != 1 || len(batch.samples) != 1 {
		t.Fatalf("count=%d samples=%d, want 1/1", batch.count, len(batch.samples))
	}
	if batch.samples[0].deltaUS != 0 {
		t.Errorf("first delta = %d, want 0", batch.samples[0].deltaUS)
	}
	if batch.edgeDeviceID != 0 || batch.commandTemplateID != 0 || batch.commandIndex != 0 {
		t.Errorf("absent optional fields must decode as 0, got %d/%d/%d",
			batch.edgeDeviceID, batch.commandTemplateID, batch.commandIndex)
	}
}

// TestParseDataBatchFirmwareFullSizeFrame decodes the frame the REAL firmware
// encoder (components/msg_handler/data_batch_codec.c) produces for the §2.2
// budget shape: n=4 samples of 300 B each.
//
// Provenance: the hex below is the literal stdout of data_batch_encode() compiled
// and run out of tree (gcc -std=c11, frame_codec.c + data_batch_codec.c), not bytes
// produced by backend/pkg/frame. It pins three things at once: the multi-byte
// varint paths (300 B length prefixes, deltas >= 128), the 1,258-byte encoded size
// staying inside the 1,400 B firmware TX budget (contract §2.2), and the fact that
// a full-size batch survives the backend parser intact.
func TestParseDataBatchFirmwareFullSizeFrame(t *testing.T) {
	const firmwareHex = "2008041080d095ffbc3118802020032ab102080012ac02000102030405060708" +
		"090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728" +
		"292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f404142434445464748" +
		"494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f606162636465666768" +
		"696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f808182838485868788" +
		"898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fa0a1a2a3a4a5a6a7a8" +
		"a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0c1c2c3c4c5c6c7c8" +
		"c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7e8" +
		"e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeff000102030405060708" +
		"090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728" +
		"292a2b2ab20208904e12ac02000102030405060708090a0b0c0d0e0f10111213" +
		"1415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f30313233" +
		"3435363738393a3b3c3d3e3f404142434445464748494a4b4c4d4e4f50515253" +
		"5455565758595a5b5c5d5e5f606162636465666768696a6b6c6d6e6f70717273" +
		"7475767778797a7b7c7d7e7f808182838485868788898a8b8c8d8e8f90919293" +
		"9495969798999a9b9c9d9e9fa0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3" +
		"b4b5b6b7b8b9babbbcbdbebfc0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3" +
		"d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3" +
		"f4f5f6f7f8f9fafbfcfdfeff000102030405060708090a0b0c0d0e0f10111213" +
		"1415161718191a1b1c1d1e1f202122232425262728292a2b2ab30208a09c0112" +
		"ac02000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d" +
		"1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d" +
		"3e3f404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d" +
		"5e5f606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d" +
		"7e7f808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d" +
		"9e9fa0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbd" +
		"bebfc0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdd" +
		"dedfe0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfd" +
		"feff000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d" +
		"1e1f202122232425262728292a2b2ab30208b0ea0112ac020001020304050607" +
		"08090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021222324252627" +
		"28292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f4041424344454647" +
		"48494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f6061626364656667" +
		"68696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f8081828384858687" +
		"88898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fa0a1a2a3a4a5a6a7" +
		"a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0c1c2c3c4c5c6c7" +
		"c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7" +
		"e8e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeff0001020304050607" +
		"08090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021222324252627" +
		"28292a2b302a38074001"

	payload, err := hex.DecodeString(firmwareHex)
	if err != nil {
		t.Fatalf("decode firmware hex: %v", err)
	}
	if len(payload) != 1258 {
		t.Fatalf("firmware frame length = %d, want 1258 (contract §2.2 budget is 1400)", len(payload))
	}

	batch, err := parseDataBatch(payload)
	if err != nil {
		t.Fatalf("parseDataBatch(firmware n=4 frame): %v", err)
	}
	if batch.count != 4 || len(batch.samples) != 4 {
		t.Fatalf("count=%d samples=%d, want 4/4", batch.count, len(batch.samples))
	}
	if batch.channelID != 3 || batch.baseTimestampUS != 1700000000000 || batch.firstSequence != 4096 {
		t.Fatalf("header = ch %d base %d seq %d, want 3/1700000000000/4096",
			batch.channelID, batch.baseTimestampUS, batch.firstSequence)
	}
	if batch.edgeDeviceID != 42 || batch.commandTemplateID != 7 || batch.commandIndex != 1 {
		t.Fatalf("addressing = %d/%d/%d, want 42/7/1",
			batch.edgeDeviceID, batch.commandTemplateID, batch.commandIndex)
	}
	for i, sample := range batch.samples {
		if sample.deltaUS != uint64(i)*10000 {
			t.Errorf("sample[%d] delta = %d, want %d", i, sample.deltaUS, i*10000)
		}
		if len(sample.rawData) != 300 {
			t.Fatalf("sample[%d] raw len = %d, want 300", i, len(sample.rawData))
		}
		for b, v := range sample.rawData {
			if v != byte(b&0xff) {
				t.Fatalf("sample[%d] raw[%d] = 0x%02x, want 0x%02x", i, b, v, byte(b&0xff))
			}
		}
	}
}

// TestParseDataBatchFirmwareCompactAnchor decodes the compact 44-byte n=4 frame
// produced by the production firmware encoder (v3-firmware, 2026-10-06).
//
// Input: ch=3, base=1000, first_seq=7, edge/template/index all 0,
//
//	deltas 0/10/20/30, raw deadbeef / 0102 / aa / 556677.
//
// Because every optional field is omitted this is the SMALLEST possible n=4
// frame, so it pins the field order and the minimal-length varint paths that a
// larger frame could mask.
func TestParseDataBatchFirmwareCompactAnchor(t *testing.T) {
	const firmwareHex = "20080410e807180720032a0808001204deadbeef2a06080a120201022a0508141201aa2a07081e1203556677"

	payload, err := hex.DecodeString(firmwareHex)
	if err != nil {
		t.Fatalf("decode firmware compact hex: %v", err)
	}
	if len(payload) != 44 {
		t.Fatalf("compact frame length = %d, want 44", len(payload))
	}

	batch, err := parseDataBatch(payload)
	if err != nil {
		t.Fatalf("parseDataBatch(firmware compact n=4): %v", err)
	}
	if batch.count != 4 || len(batch.samples) != 4 {
		t.Fatalf("count=%d samples=%d, want 4/4", batch.count, len(batch.samples))
	}
	if batch.channelID != 3 || batch.baseTimestampUS != 1000 || batch.firstSequence != 7 {
		t.Fatalf("header = ch %d base %d seq %d, want 3/1000/7",
			batch.channelID, batch.baseTimestampUS, batch.firstSequence)
	}
	if batch.edgeDeviceID != 0 || batch.commandTemplateID != 0 || batch.commandIndex != 0 {
		t.Fatalf("omitted optionals decoded as %d/%d/%d, want 0/0/0",
			batch.edgeDeviceID, batch.commandTemplateID, batch.commandIndex)
	}
	wantDeltas := []uint64{0, 10, 20, 30}
	wantRaw := []string{"deadbeef", "0102", "aa", "556677"}
	for i, sample := range batch.samples {
		if sample.deltaUS != wantDeltas[i] {
			t.Errorf("sample[%d] delta = %d, want %d", i, sample.deltaUS, wantDeltas[i])
		}
		if got := hex.EncodeToString(sample.rawData); got != wantRaw[i] {
			t.Errorf("sample[%d] raw = %s, want %s", i, got, wantRaw[i])
		}
	}
}

// TestHandleDataBatchFirmwareCompactFanOut fans the 44-byte firmware frame out
// and asserts the exact timestamps/sequences the device intended.
//
// This vector is the PASSIVE case on purpose: edge_device_id=0 and request_id=0
// make each fanned-out event a passive report (IsPassive), exactly as a 0x03
// frame with edge=0 would be. Pinning that here prevents a future refactor from
// silently "upgrading" batched samples into persisted ones — which would start
// writing rows the 0x03 path never wrote.
func TestHandleDataBatchFirmwareCompactFanOut(t *testing.T) {
	const firmwareHex = "20080410e807180720032a0808001204deadbeef2a06080a120201022a0508141201aa2a07081e1203556677"
	payload, err := hex.DecodeString(firmwareHex)
	if err != nil {
		t.Fatalf("decode firmware compact hex: %v", err)
	}

	mgr, capture := newBatchTestManager(t)
	mgr.handleDataBatch("DEV-COMPACT", payload)

	events := waitForEvents(t, capture, 4)
	if len(events) != 4 {
		t.Fatalf("fanned out %d event(s), want 4", len(events))
	}

	bySequence := indexBySequence(t, events)
	wantRaw := []string{"deadbeef", "0102", "aa", "556677"}
	for i := 0; i < 4; i++ {
		wantSeq := uint64(7 + i)
		wantTS := uint64(1000 + i*10)
		evt, ok := bySequence[wantSeq]
		if !ok {
			t.Fatalf("no event with sequence %d; got %v", wantSeq, sequenceList(events))
		}
		if evt.Timestamp != wantTS {
			t.Errorf("seq %d timestamp = %d, want %d", wantSeq, evt.Timestamp, wantTS)
		}
		if evt.ChannelID != 3 {
			t.Errorf("seq %d channel = %d, want 3", wantSeq, evt.ChannelID)
		}
		if got := hex.EncodeToString(evt.RawData); got != wantRaw[i] {
			t.Errorf("seq %d raw = %s, want %s", wantSeq, got, wantRaw[i])
		}
		if evt.EdgeDeviceID != 0 || evt.CommandTemplateID != 0 || evt.CommandIndex != 0 {
			t.Errorf("seq %d addressing = %d/%d/%d, want 0/0/0",
				wantSeq, evt.EdgeDeviceID, evt.CommandTemplateID, evt.CommandIndex)
		}
		if !evt.IsPassive() || evt.ShouldPersist() || evt.ShouldParse() {
			t.Errorf("seq %d classification = passive:%v persist:%v parse:%v, want passive-only (matches 0x03 with edge=0)",
				wantSeq, evt.IsPassive(), evt.ShouldPersist(), evt.ShouldParse())
		}
	}
}

// =============================================================================
// 2. Fan-out — N samples must become N DataEvents with 0x03 semantics.
// =============================================================================

func TestHandleDataBatchFansOutFourSamples(t *testing.T) {
	mgr, capture := newBatchTestManager(t)

	samples := []batchSample{
		{deltaUS: 0, raw: []byte{0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44}},
		{deltaUS: 10000, raw: []byte{0x01, 0x03, 0x02, 0x00, 0x01, 0x11, 0x22}},
		{deltaUS: 20000, raw: []byte{0x01, 0x03, 0x02, 0x00, 0x02, 0x33, 0x44}},
		{deltaUS: 30000, raw: []byte{0x01, 0x03, 0x02, 0x00, 0x03, 0x55, 0x66}},
	}
	wire := dataBatchWire{
		count: 4, baseTS: 1700000000000, firstSeq: 4096, channelID: 3,
		samples: samples, edgeDeviceID: u64ptr(42), templateID: u64ptr(7), commandIndex: u64ptr(1),
	}.bytes()

	framesBefore := counterValue(t, metrics.DataBatchFramesTotal)
	samplesBefore := counterValue(t, metrics.DataBatchSamplesTotal)

	mgr.handleDataBatch("DEV-BATCH", wire)

	events := waitForEvents(t, capture, 4)
	if len(events) != 4 {
		t.Fatalf("fanned out %d event(s), want 4", len(events))
	}

	// Delivery order is deliberately NOT asserted: the fan-out shares the 8-worker
	// pool with 0x03, so four batched samples are processed concurrently exactly as
	// four consecutive 0x03 frames would be. Each event is self-describing
	// (timestamp + sequence), which is the contract downstream consumers rely on.
	bySequence := indexBySequence(t, events)

	for i, sample := range samples {
		wantTS := uint64(1700000000000) + sample.deltaUS
		wantSeq := uint64(4096) + uint64(i)
		evt, ok := bySequence[wantSeq]
		if !ok {
			t.Fatalf("no event with sequence %d; got %v", wantSeq, sequenceList(events))
		}
		if evt.DeviceID != "DEV-BATCH" {
			t.Errorf("seq %d device = %q", wantSeq, evt.DeviceID)
		}
		if evt.Timestamp != wantTS {
			t.Errorf("seq %d timestamp = %d, want %d", wantSeq, evt.Timestamp, wantTS)
		}
		if evt.ChannelID != 3 {
			t.Errorf("seq %d channel = %d, want 3", wantSeq, evt.ChannelID)
		}
		if hex.EncodeToString(evt.RawData) != hex.EncodeToString(sample.raw) {
			t.Errorf("seq %d raw = %x, want %x", wantSeq, evt.RawData, sample.raw)
		}
		if evt.EdgeDeviceID != 42 || evt.CommandTemplateID != 7 || evt.CommandIndex != 1 {
			t.Errorf("seq %d addressing = %d/%d/%d, want 42/7/1",
				wantSeq, evt.EdgeDeviceID, evt.CommandTemplateID, evt.CommandIndex)
		}
		// §2.1-6: a batched sample is by definition non-critical.
		if evt.ErrorCode != 0 || evt.RequestID != 0 {
			t.Errorf("seq %d err=%d req=%d, want 0/0", wantSeq, evt.ErrorCode, evt.RequestID)
		}
		// The downstream classifier must treat these exactly like 0x03
		// scheduled samples, otherwise data silently stops being parsed.
		if !evt.IsScheduledSample() || !evt.ShouldPersist() || !evt.ShouldParse() {
			t.Errorf("seq %d classification = scheduled:%v persist:%v parse:%v, want all true",
				wantSeq, evt.IsScheduledSample(), evt.ShouldPersist(), evt.ShouldParse())
		}
	}

	if got := counterValue(t, metrics.DataBatchFramesTotal) - framesBefore; got != 1 {
		t.Errorf("data_batch_frames_total delta = %v, want 1", got)
	}
	if got := counterValue(t, metrics.DataBatchSamplesTotal) - samplesBefore; got != 4 {
		t.Errorf("data_batch_samples_total delta = %v, want 4", got)
	}
}

// TestDataBatchEventMatchesDataReportPath is the core "前端零改动" proof: the
// same logical sample delivered as 0x03 and as a 0x20 batch must produce
// identical DataEvents. If this ever diverges, every downstream consumer would
// need to learn about batching.
func TestDataBatchEventMatchesDataReportPath(t *testing.T) {
	raw := []byte{0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44}

	// 0x03 path.
	mgrA, captureA := newBatchTestManager(t)
	enc := frame.NewEncoder(frame.MsgDataRpt)
	enc.EncodeVarint(1, 3)             // channel_id
	enc.EncodeVarint(2, 1700000000000) // timestamp
	enc.EncodeVarint(3, 4096)          // sequence
	enc.EncodeBytes(4, raw)            // raw_data
	enc.EncodeVarint(5, 0)             // error_code
	enc.EncodeVarint(6, 0)             // request_id
	enc.EncodeVarint(7, 42)            // edge_device_id
	enc.EncodeVarint(8, 1)             // command_index
	enc.EncodeVarint(9, 7)             // command_template_id
	mgrA.handleDataReport("DEV-SAME", enc.Bytes())
	evtA := waitForEvents(t, captureA, 1)[0]

	// 0x20 path, one sample carrying the identical values.
	mgrB, captureB := newBatchTestManager(t)
	wire := dataBatchWire{
		count: 1, baseTS: 1700000000000, firstSeq: 4096, channelID: 3,
		samples:      []batchSample{{deltaUS: 0, raw: raw}},
		edgeDeviceID: u64ptr(42), templateID: u64ptr(7), commandIndex: u64ptr(1),
	}.bytes()
	mgrB.handleDataBatch("DEV-SAME", wire)
	evtB := waitForEvents(t, captureB, 1)[0]

	// ReceivedAt is server wall-clock, not wire data, so it is excluded by design.
	if evtA.DeviceID != evtB.DeviceID || evtA.ChannelID != evtB.ChannelID ||
		evtA.Timestamp != evtB.Timestamp || evtA.Sequence != evtB.Sequence ||
		evtA.ErrorCode != evtB.ErrorCode || evtA.RequestID != evtB.RequestID ||
		evtA.EdgeDeviceID != evtB.EdgeDeviceID || evtA.CommandIndex != evtB.CommandIndex ||
		evtA.CommandTemplateID != evtB.CommandTemplateID ||
		hex.EncodeToString(evtA.RawData) != hex.EncodeToString(evtB.RawData) {
		t.Fatalf("0x03 event != 0x20 event: 0x03=%+v 0x20=%+v", evtA, evtB)
	}
}

// TestParseDataBatchFirmwareWideVarints decodes a firmware-produced frame whose
// varint widths the other anchor vectors never reach.
//
// Why this exists: the earlier anchors leave the 2-byte delta class [128, 16383]
// untested, and never push the batch header to its widest varints. This frame was
// produced by the production firmware encoder with:
//
//	ch=9, base_timestamp_us=4294967295 (5-byte varint), first_sequence=65535 (3-byte),
//	edge=254, template=65535 (3-byte), index=255,
//	deltas 0 / 127 (1-byte max) / 128 (2-byte min) / 16383 (2-byte max),
//	raw_len=300 (2-byte length prefix) for every sample.
//
// The firmware encoder's size prediction matched the emitted length exactly
// (pred=actual=1259) and the firmware decoder reads it back. This test proves the
// BACKEND parser handles the same byte classes, so the two sides are anchored on a
// vector that exercises the varint width matrix rather than one corner of it.
func TestParseDataBatchFirmwareWideVarints(t *testing.T) {
	const firmwareHex = "20080410ffffffff0f18ffff0320092ab102080012ac02a0a1a2a3a4a5a6a7a8" +
		"a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf808182838485868788" +
		"898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fe0e1e2e3e4e5e6e7e8" +
		"e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeffc0c1c2c3c4c5c6c7c8" +
		"c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf202122232425262728" +
		"292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f000102030405060708" +
		"090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f606162636465666768" +
		"696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f404142434445464748" +
		"494a4b4c4d4e4f505152535455565758595a5b5c5d5e5fa0a1a2a3a4a5a6a7a8" +
		"a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf808182838485868788" +
		"898a8b2ab102087f12ac02a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4" +
		"b5b6b7b8b9babbbcbdbebf808182838485868788898a8b8c8d8e8f9091929394" +
		"95969798999a9b9c9d9e9fe0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3f4" +
		"f5f6f7f8f9fafbfcfdfeffc0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4" +
		"d5d6d7d8d9dadbdcdddedf202122232425262728292a2b2c2d2e2f3031323334" +
		"35363738393a3b3c3d3e3f000102030405060708090a0b0c0d0e0f1011121314" +
		"15161718191a1b1c1d1e1f606162636465666768696a6b6c6d6e6f7071727374" +
		"75767778797a7b7c7d7e7f404142434445464748494a4b4c4d4e4f5051525354" +
		"55565758595a5b5c5d5e5fa0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4" +
		"b5b6b7b8b9babbbcbdbebf808182838485868788898a8b2ab20208800112ac02" +
		"a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf" +
		"808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f" +
		"e0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeff" +
		"c0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf" +
		"202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f" +
		"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" +
		"606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f" +
		"404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f" +
		"a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf" +
		"808182838485868788898a8b2ab20208ff7f12ac02a0a1a2a3a4a5a6a7a8a9aa" +
		"abacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf808182838485868788898a" +
		"8b8c8d8e8f909192939495969798999a9b9c9d9e9fe0e1e2e3e4e5e6e7e8e9ea" +
		"ebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeffc0c1c2c3c4c5c6c7c8c9ca" +
		"cbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf202122232425262728292a" +
		"2b2c2d2e2f303132333435363738393a3b3c3d3e3f000102030405060708090a" +
		"0b0c0d0e0f101112131415161718191a1b1c1d1e1f606162636465666768696a" +
		"6b6c6d6e6f707172737475767778797a7b7c7d7e7f404142434445464748494a" +
		"4b4c4d4e4f505152535455565758595a5b5c5d5e5fa0a1a2a3a4a5a6a7a8a9aa" +
		"abacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf808182838485868788898a" +
		"8b30fe0138ffff0340ff01"

	payload, err := hex.DecodeString(firmwareHex)
	if err != nil {
		t.Fatalf("decode firmware wide-varint hex: %v", err)
	}
	if len(payload) != 1259 {
		t.Fatalf("frame length = %d, want 1259 (firmware pred==actual)", len(payload))
	}

	batch, err := parseDataBatch(payload)
	if err != nil {
		t.Fatalf("parseDataBatch(firmware wide-varint frame): %v", err)
	}
	if batch.count != 4 || len(batch.samples) != 4 {
		t.Fatalf("count=%d samples=%d, want 4/4", batch.count, len(batch.samples))
	}
	if batch.channelID != 9 {
		t.Errorf("channel_id = %d, want 9", batch.channelID)
	}
	if batch.baseTimestampUS != 4294967295 {
		t.Errorf("base_timestamp_us = %d, want 4294967295 (5-byte varint)", batch.baseTimestampUS)
	}
	if batch.firstSequence != 65535 {
		t.Errorf("first_sequence = %d, want 65535 (3-byte varint)", batch.firstSequence)
	}
	if batch.edgeDeviceID != 254 || batch.commandTemplateID != 65535 || batch.commandIndex != 255 {
		t.Errorf("addressing = %d/%d/%d, want 254/65535/255",
			batch.edgeDeviceID, batch.commandTemplateID, batch.commandIndex)
	}
	wantDeltas := []uint64{0, 127, 128, 16383}
	for i, sample := range batch.samples {
		if sample.deltaUS != wantDeltas[i] {
			t.Errorf("sample[%d] delta = %d, want %d", i, sample.deltaUS, wantDeltas[i])
		}
		if len(sample.rawData) != 300 {
			t.Fatalf("sample[%d] raw len = %d, want 300", i, len(sample.rawData))
		}
		// raw[i] = (uint8_t)(0xA0 ^ i); the last byte is (0xA0 ^ 299) & 0xFF = 0x8B.
		if sample.rawData[0] != 0xA0 || sample.rawData[299] != 0x8B {
			t.Errorf("sample[%d] raw boundaries = 0x%02x/0x%02x", i, sample.rawData[0], sample.rawData[299])
		}
	}
}

// =============================================================================
// 3. Strictness — every invariant rejects the WHOLE frame (fail-closed).
// =============================================================================

func validBatchWire() dataBatchWire {
	return dataBatchWire{
		count: 2, baseTS: 1000000, firstSeq: 7, channelID: 1,
		samples: []batchSample{
			{deltaUS: 0, raw: []byte{0xaa}},
			{deltaUS: 10000, raw: []byte{0xbb}},
		},
	}
}

// TestParseDataBatchRejectsInvalidFrames pins each §2.1 invariant with the
// specific error text, so a failure says which rule broke instead of only
// "some error occurred".
func TestParseDataBatchRejectsInvalidFrames(t *testing.T) {
	cases := []struct {
		name    string
		wire    dataBatchWire
		wantErr string
	}{
		{
			name:    "count greater than samples",
			wire:    func() dataBatchWire { w := validBatchWire(); w.count = 4; return w }(),
			wantErr: "does not match",
		},
		{
			name:    "count less than samples",
			wire:    func() dataBatchWire { w := validBatchWire(); w.count = 1; return w }(),
			wantErr: "does not match",
		},
		{
			name:    "count zero",
			wire:    dataBatchWire{count: 0, baseTS: 1, firstSeq: 1, channelID: 1},
			wantErr: "out of range",
		},
		{
			name: "count five",
			wire: dataBatchWire{
				count: 5, baseTS: 1, firstSeq: 1, channelID: 1,
				samples: []batchSample{{0, []byte{1}}, {1000, []byte{2}}, {2000, []byte{3}}, {3000, []byte{4}}, {4000, []byte{5}}},
			},
			wantErr: "out of range",
		},
		{
			name: "first sample delta non-zero",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.samples[0].deltaUS = 1
				return w
			}(),
			wantErr: "first sample delta_us",
		},
		{
			name: "later sample delta zero",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.samples[1].deltaUS = 0
				return w
			}(),
			wantErr: "delta_us is zero",
		},
		{
			name: "delta non-monotonic",
			wire: dataBatchWire{
				count: 3, baseTS: 1, firstSeq: 1, channelID: 1,
				samples: []batchSample{{0, []byte{1}}, {5000, []byte{2}}, {4000, []byte{3}}},
			},
			wantErr: "not monotonic",
		},
		{
			name: "empty raw_data",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.samples[1].raw = []byte{}
				return w
			}(),
			wantErr: "empty raw_data",
		},
		{
			name: "raw_data over 1024 bytes",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.samples[1].raw = make([]byte, dataBatchMaxRawBytes+1)
				return w
			}(),
			wantErr: "exceeds 1024",
		},
		{
			name:    "missing count",
			wire:    func() dataBatchWire { w := validBatchWire(); w.omitCount = true; return w }(),
			wantErr: "missing required field",
		},
		{
			name:    "missing base timestamp",
			wire:    func() dataBatchWire { w := validBatchWire(); w.omitBase = true; return w }(),
			wantErr: "missing required field",
		},
		{
			name:    "missing first sequence",
			wire:    func() dataBatchWire { w := validBatchWire(); w.omitFirstSeq = true; return w }(),
			wantErr: "missing required field",
		},
		{
			name:    "missing channel",
			wire:    func() dataBatchWire { w := validBatchWire(); w.omitChannel = true; return w }(),
			wantErr: "missing required field",
		},
		{
			// 0x08 = field 1 (varint) again: the count appears twice.
			name:    "duplicate count field",
			wire:    func() dataBatchWire { w := validBatchWire(); w.rawTail = []byte{0x08, 0x02}; return w }(),
			wantErr: "duplicate",
		},
		{
			// 0x30 = field 6 (varint) twice: edge_device_id duplicated.
			name: "duplicate edge_device_id field",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.edgeDeviceID = u64ptr(9)
				w.rawTail = []byte{0x30, 0x0a}
				return w
			}(),
			wantErr: "duplicate",
		},
		{
			name: "sample missing raw_data",
			wire: func() dataBatchWire {
				w := validBatchWire()
				sub := frame.SubEncoder()
				sub.EncodeVarint(dataBatchSampleFieldDeltaUS, 0)
				w.count = 1
				w.samples = nil
				w.sampleOverride = [][]byte{sub.Bytes()}
				return w
			}(),
			wantErr: "requires delta_us and raw_data",
		},
		{
			name: "sample missing delta_us",
			wire: func() dataBatchWire {
				w := validBatchWire()
				sub := frame.SubEncoder()
				sub.EncodeBytes(dataBatchSampleFieldRawData, []byte{0xaa})
				w.count = 1
				w.samples = nil
				w.sampleOverride = [][]byte{sub.Bytes()}
				return w
			}(),
			wantErr: "requires delta_us and raw_data",
		},
		{
			name: "sample duplicates raw_data",
			wire: func() dataBatchWire {
				w := validBatchWire()
				sub := frame.SubEncoder()
				sub.EncodeVarint(dataBatchSampleFieldDeltaUS, 0)
				sub.EncodeBytes(dataBatchSampleFieldRawData, []byte{0xaa})
				sub.EncodeBytes(dataBatchSampleFieldRawData, []byte{0xbb})
				w.count = 1
				w.samples = nil
				w.sampleOverride = [][]byte{sub.Bytes()}
				return w
			}(),
			wantErr: "duplicate sample field",
		},
		{
			name: "empty sample sub-message",
			wire: func() dataBatchWire {
				w := validBatchWire()
				w.count = 1
				w.samples = nil
				w.sampleOverride = [][]byte{{}}
				return w
			}(),
			wantErr: "sample",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDataBatch(tc.wire.bytes())
			if err == nil {
				t.Fatalf("parseDataBatch accepted an invalid frame (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestParseDataBatchRejectsWrongWireType: the field must be rejected when its
// wire type contradicts the frozen layout, not silently coerced.
func TestParseDataBatchRejectsWrongWireType(t *testing.T) {
	t.Run("count as length-delimited", func(t *testing.T) {
		enc := frame.NewEncoder(frame.MsgDataBatch)
		enc.EncodeBytes(dataBatchFieldCount, []byte{0x02})
		enc.EncodeVarint(dataBatchFieldBaseTimestampUS, 1)
		enc.EncodeVarint(dataBatchFieldFirstSequence, 1)
		enc.EncodeVarint(dataBatchFieldChannelID, 1)
		if _, err := parseDataBatch(enc.Bytes()); err == nil || !strings.Contains(err.Error(), "wire type") {
			t.Fatalf("err = %v, want a wire-type rejection", err)
		}
	})

	t.Run("sample as varint", func(t *testing.T) {
		enc := frame.NewEncoder(frame.MsgDataBatch)
		enc.EncodeVarint(dataBatchFieldCount, 1)
		enc.EncodeVarint(dataBatchFieldBaseTimestampUS, 1)
		enc.EncodeVarint(dataBatchFieldFirstSequence, 1)
		enc.EncodeVarint(dataBatchFieldChannelID, 1)
		enc.EncodeVarint(dataBatchFieldSample, 1)
		if _, err := parseDataBatch(enc.Bytes()); err == nil || !strings.Contains(err.Error(), "wire type") {
			t.Fatalf("err = %v, want a wire-type rejection", err)
		}
	})

	t.Run("wrong message type", func(t *testing.T) {
		wire := validBatchWire()
		payload := wire.bytes()
		payload[0] = frame.MsgDataRpt
		if _, err := parseDataBatch(payload); err == nil || !strings.Contains(err.Error(), "message type") {
			t.Fatalf("err = %v, want a message-type rejection", err)
		}
	})
}

// TestParseDataBatchSkipsUnknownFields is the forward-compatibility contract:
// a future firmware may add field 9 without a coordinated backend release.
func TestParseDataBatchSkipsUnknownFields(t *testing.T) {
	// field 90, varint 42 → tag 0xD0 0x05. Injected between the header and the
	// samples so the skip is proven position-independent.
	unknownField := []byte{0xd0, 0x05, 0x2a}
	// An unknown sub-field inside a sample must be skipped too.
	sub := frame.SubEncoder()
	sub.EncodeVarint(dataBatchSampleFieldDeltaUS, 0)
	sub.EncodeBytes(dataBatchSampleFieldRawData, []byte{0xaa})
	sub.EncodeVarint(99, 7)

	wire := dataBatchWire{
		count: 1, baseTS: 555, firstSeq: 3, channelID: 2,
		sampleOverride: [][]byte{sub.Bytes()},
		rawHead:        unknownField,
		rawTail:        unknownField,
	}
	batch, err := parseDataBatch(wire.bytes())
	if err != nil {
		t.Fatalf("unknown fields must be skipped, got: %v", err)
	}
	if batch.count != 1 || batch.baseTimestampUS != 555 || batch.firstSequence != 3 || batch.channelID != 2 {
		t.Fatalf("known fields lost around unknown fields: %+v", batch)
	}
	if len(batch.samples) != 1 || hex.EncodeToString(batch.samples[0].rawData) != "aa" {
		t.Fatalf("sample lost around unknown sub-field: %+v", batch.samples)
	}
}

// TestHandleDataBatchRejectsAndCountsWholeFrame: a rejected frame must produce
// ZERO events (no partial fan-out) and must move the rejection counter.
func TestHandleDataBatchRejectsAndCountsWholeFrame(t *testing.T) {
	mgr, capture := newBatchTestManager(t)

	// count=3 but only 2 samples: the first two samples are individually valid,
	// which is exactly the case where a partial fan-out would go unnoticed.
	wire := dataBatchWire{
		count: 3, baseTS: 1000, firstSeq: 10, channelID: 4,
		samples: []batchSample{{0, []byte{0x01}}, {10000, []byte{0x02}}},
	}.bytes()

	before := counterValue(t, metrics.DataBatchRejectedTotal)
	framesBefore := counterValue(t, metrics.DataBatchFramesTotal)

	mgr.handleDataBatch("DEV-BAD", wire)

	if got := counterValue(t, metrics.DataBatchRejectedTotal) - before; got != 1 {
		t.Errorf("data_batch_rejected_total delta = %v, want 1", got)
	}
	if got := counterValue(t, metrics.DataBatchFramesTotal) - framesBefore; got != 0 {
		t.Errorf("rejected frame incremented data_batch_frames_total by %v, want 0", got)
	}
	// Give the worker pool a chance to (wrongly) deliver something.
	time.Sleep(50 * time.Millisecond)
	if events := capture.snapshot(); len(events) != 0 {
		t.Fatalf("rejected frame produced %d event(s), want 0 (fail-closed)", len(events))
	}
}

// TestHandleMessageDispatchesDataBatch pins the production dispatch switch:
// 0x20 must reach handleDataBatch through HandleMessage, not fall into the
// "Unknown msg type" default branch.
func TestHandleMessageDispatchesDataBatch(t *testing.T) {
	mgr, capture := newBatchTestManager(t)

	wire := dataBatchWire{
		count: 2, baseTS: 2000, firstSeq: 100, channelID: 5,
		samples:      []batchSample{{0, []byte{0x11}}, {10000, []byte{0x22}}},
		edgeDeviceID: u64ptr(8), templateID: u64ptr(3), commandIndex: u64ptr(0),
	}.bytes()

	mgr.HandleMessage("nodes/DEV-DISPATCH/up", wire)

	events := waitForEvents(t, capture, 2)
	if len(events) != 2 {
		t.Fatalf("HandleMessage fanned out %d event(s), want 2", len(events))
	}
	bySequence := indexBySequence(t, events)
	for wantSeq, wantTS := range map[uint64]uint64{100: 2000, 101: 12000} {
		evt, ok := bySequence[wantSeq]
		if !ok {
			t.Fatalf("no event with sequence %d; got %v", wantSeq, sequenceList(events))
		}
		if evt.DeviceID != "DEV-DISPATCH" {
			t.Errorf("seq %d device = %q, want DEV-DISPATCH", wantSeq, evt.DeviceID)
		}
		if evt.Timestamp != wantTS {
			t.Errorf("seq %d timestamp = %d, want %d", wantSeq, evt.Timestamp, wantTS)
		}
	}
}

// TestDataBatchOneFrameOneChannelIdentity documents §2.1-1 as an observed
// property of the fan-out: every sample of a frame shares the batch-level
// routing metadata (channel/edge/template/index), because the wire carries it
// only once.
func TestDataBatchOneFrameOneChannelIdentity(t *testing.T) {
	mgr, capture := newBatchTestManager(t)

	wire := dataBatchWire{
		count: 3, baseTS: 900, firstSeq: 1, channelID: 77,
		samples:      []batchSample{{0, []byte{0x01}}, {1000, []byte{0x02}}, {2000, []byte{0x03}}},
		edgeDeviceID: u64ptr(5), templateID: u64ptr(6), commandIndex: u64ptr(2),
	}.bytes()

	mgr.handleDataBatch("DEV-ROUTE", wire)
	for _, evt := range waitForEvents(t, capture, 3) {
		if evt.ChannelID != 77 || evt.EdgeDeviceID != 5 || evt.CommandTemplateID != 6 || evt.CommandIndex != 2 {
			t.Fatalf("sample lost batch routing metadata: %+v", evt)
		}
	}
}
