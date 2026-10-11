package downlink

// downlink_test.go -- rewritten 2026-10-08 when MQTT was removed.
//
// # What changed and why most of the old tests are gone
//
// The old suite was mostly about the MQTT FALLBACK: does a node without a
// session go to MQTT, does a native failure fall back, is the QoS-2 downgrade
// counted. All of those describe a choice that no longer exists. Keeping them
// (even renamed) would assert the behaviour of a deleted transport.
//
// The tests below cover what replaced them, plus the two properties that were
// easiest to get wrong while removing the fallback:
//
//  1. A frame is still built EXACTLY as before: the payload's leading type
//     byte must equal the header type, and the size bound must still REJECT
//     rather than truncate (the R1 class of bug).
//  2. A node with no session is now an ERROR, not a silent hand-off. This is
//     the most important behavioural change: with no broker consumer left,
//     the old fallback would have become a black hole -- and a black hole
//     that reports success is the failure this repo keeps re-learning.

import (
	"errors"
	"testing"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"
)

// fakeSender records what was sent and can be made to fail.
type fakeSender struct {
	sent    []sentFrame
	hasNode map[string]bool
	sendErr error
}

type sentFrame struct {
	nodeID  string
	payload []byte
}

func newFakeSender(nodes ...string) *fakeSender {
	f := &fakeSender{hasNode: map[string]bool{}}
	for _, n := range nodes {
		f.hasNode[n] = true
	}
	return f
}

func (f *fakeSender) HasSession(nodeID string) bool { return f.hasNode[nodeID] }

func (f *fakeSender) SendToNode(nodeID string, payload []byte) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	if !f.hasNode[nodeID] {
		return errors.New("no session for " + nodeID)
	}
	// Copy: a test that aliased the bridge's buffer could pass while a real
	// receiver saw different bytes.
	cp := make([]byte, len(payload))
	copy(cp, payload)
	f.sent = append(f.sent, sentFrame{nodeID: nodeID, payload: cp})
	return nil
}

// newBridge fails the test rather than panicking, so a wiring mistake reads as
// a test failure with a line number instead of a stack trace.
func newBridge(t *testing.T, s sessionSender) *Bridge {
	t.Helper()
	if s == nil {
		t.Fatal("test would exercise the nil-sender panic; pass a fake")
	}
	return New(s)
}

// ── the frame contract (unchanged by the MQTT removal) ───────────────────

func TestPublishFramesPayloadForTheNode(t *testing.T) {
	sender := newFakeSender("node-a")
	b := newBridge(t, sender)

	payload := []byte{frame.MsgWriteCmd, 0x01, 0x02, 0x03}
	if err := b.Publish("node-a", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(sender.sent))
	}
	got := sender.sent[0]
	if got.nodeID != "node-a" {
		t.Errorf("sent to %q, want node-a", got.nodeID)
	}

	h, err := protoframe.DecodeHeader(got.payload)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if h.Ver != protoframe.Version {
		t.Errorf("version = 0x%02X, want 0x%02X", h.Ver, protoframe.Version)
	}
	// The type is carried TWICE: in the header and as payload[0]. The receiver
	// rejects a disagreement (nodemgr.HandleFrame), so a mismatch here would
	// make every message undeliverable.
	if h.Type != payload[0] {
		t.Errorf("header type = 0x%02X, payload[0] = 0x%02X -- they must agree",
			h.Type, payload[0])
	}
	if int(h.PayloadLen) != len(payload) {
		t.Errorf("PayloadLen = %d, want %d", h.PayloadLen, len(payload))
	}
	body := got.payload[protoframe.HeaderSize:]
	if string(body) != string(payload) {
		t.Errorf("body = % x, want % x", body, payload)
	}
}

func TestPublishWithNoSessionIsAnErrorNotASilentDrop(t *testing.T) {
	sender := newFakeSender() // nobody connected
	b := newBridge(t, sender)

	err := b.Publish("node-a", []byte{frame.MsgWriteCmd, 0x01})
	if err == nil {
		t.Fatal("no session but Publish returned nil: the caller would report " +
			"success for a message nobody received")
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent %d frames despite having no session", len(sender.sent))
	}
}

func TestPublishPropagatesSendFailure(t *testing.T) {
	sender := newFakeSender("node-a")
	sender.sendErr = errors.New("socket closed")
	b := newBridge(t, sender)

	err := b.Publish("node-a", []byte{frame.MsgWriteCmd, 0x01})
	if err == nil {
		t.Fatal("send failed but Publish returned nil")
	}
	if !errors.Is(err, sender.sendErr) {
		t.Errorf("error %v does not wrap the transport error %v", err, sender.sendErr)
	}
}

func TestPublishRejectsEmptyNodeID(t *testing.T) {
	sender := newFakeSender("node-a")
	b := newBridge(t, sender)

	// An empty node id used to mean "not node-scoped -> MQTT" (Home Assistant).
	// That path is gone, so it must be refused rather than guessed at.
	if err := b.Publish("", []byte{frame.MsgWriteCmd, 0x01}); err == nil {
		t.Fatal("empty node id accepted; there is no transport that could carry it")
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent %d frames for an empty node id", len(sender.sent))
	}
}

// ── the size bound (R1): must reject, never truncate ─────────────────────

func TestOversizedPayloadIsRejectedNotTruncated(t *testing.T) {
	sender := newFakeSender("node-a")
	b := newBridge(t, sender)

	payload := make([]byte, MaxDownlinkPayload()+1)
	payload[0] = frame.MsgWriteCmd

	err := b.Publish("node-a", payload)
	if err == nil {
		t.Fatal("oversized payload accepted; the length field cannot hold it")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("oversized payload was sent anyway (%d frames)", len(sender.sent))
	}
}

func TestLargestLegalPayloadSurvivesTheLengthField(t *testing.T) {
	sender := newFakeSender("node-a")
	b := newBridge(t, sender)

	payload := make([]byte, MaxDownlinkPayload())
	payload[0] = frame.MsgWriteCmd
	if err := b.Publish("node-a", payload); err != nil {
		t.Fatalf("largest legal payload rejected: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(sender.sent))
	}
	h, err := protoframe.DecodeHeader(sender.sent[0].payload)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if int(h.PayloadLen) != MaxDownlinkPayload() {
		t.Errorf("PayloadLen = %d, want %d (the length field wrapped)",
			h.PayloadLen, MaxDownlinkPayload())
	}
}

func TestEmptyPayloadIsRejected(t *testing.T) {
	sender := newFakeSender("node-a")
	b := newBridge(t, sender)

	// Without a type byte there is nothing to put in the header.
	if err := b.Publish("node-a", nil); err == nil {
		t.Fatal("empty payload accepted; header type would be garbage")
	}
	if len(sender.sent) != 0 {
		t.Errorf("sent %d frames for an empty payload", len(sender.sent))
	}
}

func TestNewRejectsNilSender(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) did not panic: a bridge with no sender can only " +
				"ever fail every downlink, and must be refused at construction")
		}
	}()
	New(nil)
}
