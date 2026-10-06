package downlink

import (
	"errors"
	"testing"

	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/protoframe"
)

// downlink_test.go -- the transport-choice bridge.
//
// The failure this guards against: during the MQTT retirement window a node may
// be reachable over TCP or over MQTT, and a downlink that goes to the wrong one
// is SILENTLY lost. Every test here therefore asserts where the bytes actually
// went, not just that Publish returned nil.

type fakeNative struct {
	sent   map[string][][]byte
	has    map[string]bool
	failOn map[string]error
}

func newFakeNative() *fakeNative {
	return &fakeNative{
		sent:   map[string][][]byte{},
		has:    map[string]bool{},
		failOn: map[string]error{},
	}
}

func (f *fakeNative) SendToNode(nodeID string, frame []byte) error {
	if err := f.failOn[nodeID]; err != nil {
		return err
	}
	f.sent[nodeID] = append(f.sent[nodeID], frame)
	return nil
}

func (f *fakeNative) HasSession(nodeID string) bool { return f.has[nodeID] }

type fakeLegacy struct {
	published []string
	qos2      int
	err       error
}

func (f *fakeLegacy) Publish(topic string, payload []byte) error {
	f.published = append(f.published, topic)
	return f.err
}

// PublishQoS2 / PublishRetained exist because LegacyPublisher mirrors the
// mqtt.Publisher interface. Making the fake implement the FULL interface (not
// just the one method a test happens to call) means a future change to the
// interface surfaces here as a compile error instead of a nil-method panic.
func (f *fakeLegacy) PublishQoS2(topic string, payload []byte) error {
	f.published = append(f.published, topic)
	f.qos2++
	return f.err
}

func (f *fakeLegacy) PublishRetained(topic string, payload []byte) error {
	f.published = append(f.published, topic)
	return f.err
}

func TestNodeIDFromTopic(t *testing.T) {
	cases := []struct {
		topic string
		want  string
	}{
		{"nodes/abc123/down", "abc123"},
		{"nodes/abc123/control", "abc123"},
		{"homeassistant/sensor/x/config", ""}, // not node-scoped
		{"nodes//down", ""},                   // empty id
		{"nodes/abc", ""},                     // wrong arity
		{"", ""},
	}
	for _, c := range cases {
		if got := NodeIDFromTopic(c.topic); got != c.want {
			t.Errorf("NodeIDFromTopic(%q) = %q, want %q", c.topic, got, c.want)
		}
	}
}

// TestPrefersNativeWhenSessionExists -- a 3.0 node must get TCP, not MQTT.
func TestPrefersNativeWhenSessionExists(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	leg := &fakeLegacy{}
	b := New(nat, leg)

	payload := []byte{0x06, 0x08, 0x01} // WriteCmd with one field
	if err := b.Publish("nodes/n1/down", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(leg.published) != 0 {
		t.Fatalf("payload ALSO went to MQTT (%v) -- a node on TCP must not get "+
			"a duplicate on the other transport", leg.published)
	}
	if got := len(nat.sent["n1"]); got != 1 {
		t.Fatalf("native got %d frames, want 1", got)
	}
}

// TestFrameCarriesThePayloadTypeInTheHeader -- the 2.x payload's first byte IS
// the message type, so the 3.0 header must take its type from there.
//
// If this were wrong the receiver's agreement check would reject every
// downlink, and the device would simply never receive commands.
func TestFrameCarriesThePayloadTypeInTheHeader(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	b := New(nat, &fakeLegacy{})

	payload := []byte{0x06, 0x08, 0x01}
	if err := b.Publish("nodes/n1/down", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	frame := nat.sent["n1"][0]
	h, err := protoframe.DecodeHeader(frame)
	if err != nil {
		t.Fatalf("the frame we built does not decode: %v", err)
	}
	if h.Type != payload[0] {
		t.Errorf("header type = 0x%02X, want the payload type 0x%02X", h.Type, payload[0])
	}
	if int(h.PayloadLen) != len(payload) {
		t.Errorf("header payload_len = %d, want %d", h.PayloadLen, len(payload))
	}
	// The receiver verifies header.type == payload[0]; assert that holds.
	if frame[protoframe.HeaderSize] != h.Type {
		t.Errorf("header type 0x%02X disagrees with the payload byte 0x%02X -- "+
			"the receiver would drop this frame", h.Type, frame[protoframe.HeaderSize])
	}
}

// TestFallsBackToMQTTWhenNoSession -- a 2.8.0 device must keep working exactly
// as before. This is the whole point of the dual-stack window.
func TestFallsBackToMQTTWhenNoSession(t *testing.T) {
	nat := newFakeNative() // has no session for n1
	leg := &fakeLegacy{}
	b := New(nat, leg)

	if err := b.Publish("nodes/n1/down", []byte{0x06}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(leg.published) != 1 || leg.published[0] != "nodes/n1/down" {
		t.Fatalf("legacy published %v, want [nodes/n1/down]", leg.published)
	}
	if len(nat.sent) != 0 {
		t.Fatal("nothing should have gone to the native transport")
	}
}

// TestNonNodeTopicAlwaysGoesToMQTT -- Home Assistant discovery config is not
// node-scoped, so the native transport has no way to carry it.
func TestNonNodeTopicAlwaysGoesToMQTT(t *testing.T) {
	nat := newFakeNative()
	leg := &fakeLegacy{}
	b := New(nat, leg)

	if err := b.Publish("homeassistant/sensor/x/config", []byte("{...}")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(leg.published) != 1 {
		t.Fatalf("legacy published %v, want 1", leg.published)
	}
}

// TestNativeFailureFallsBackNotDrops -- if the TCP write fails we must not lose
// the message while the node is still reachable over MQTT.
func TestNativeFailureFallsBackNotDrops(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	nat.failOn["n1"] = errors.New("write failed")
	leg := &fakeLegacy{}
	b := New(nat, leg)

	if err := b.Publish("nodes/n1/down", []byte{0x06}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(leg.published) != 1 {
		t.Fatal("a failed native send must fall back to MQTT, not drop the message")
	}
}

// TestOversizedPayloadFallsBack -- a payload above the 3.0 maximum cannot be
// framed; it must fall back rather than be silently dropped.
func TestOversizedPayloadFallsBack(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	leg := &fakeLegacy{}
	b := New(nat, leg)

	tooBig := make([]byte, MaxDownlinkPayload()+1)
	tooBig[0] = 0x06
	if err := b.Publish("nodes/n1/down", tooBig); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(nat.sent) != 0 {
		t.Error("an oversized payload must not be sent over TCP")
	}
	if len(leg.published) != 1 {
		t.Fatal("an un-frameable payload must fall back, not vanish")
	}
}

// TestHugePayloadDoesNotTruncateTheLengthField is the case a mutation run
// showed my first version missed.
//
// I originally tested PayloadMax+1 only, and the mutant that removed the bound
// check was NOT caught -- because protoframe.EncodeHeader range-checks too, so
// that input is caught twice (an equivalent mutant for that input).
//
// But the bound check is load-bearing for payloads ABOVE 65535:
//
//	PayloadLen: uint16(len(payload))
//
// silently TRUNCATES. A 70000-byte payload becomes a header claiming 4464,
// which passes EncodeHeader's own check, and the result is a frame whose length
// field disagrees with its content -- stream corruption on a live connection.
//
// So this asserts on a payload in the truncation zone, where only the explicit
// bound check protects us.
func TestHugePayloadDoesNotTruncateTheLengthField(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	leg := &fakeLegacy{}
	b := New(nat, leg)

	// Above 65535 so uint16() cannot represent the real length.
	huge := make([]byte, 70000)
	huge[0] = 0x06 // a message type, so only the SIZE is wrong
	if err := b.Publish("nodes/n1/down", huge); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(nat.sent) != 0 {
		t.Fatalf("a %d-byte payload was sent over TCP -- its 16-bit length field "+
			"would have wrapped, producing a frame whose header disagrees with "+
			"its content", len(huge))
	}
	if len(leg.published) != 1 {
		t.Fatal("an un-frameable payload must fall back to MQTT, not vanish")
	}
}

// TestEmptyPayloadFallsBack -- an empty payload has no message type byte, so it
// cannot be framed. Same rule: fall back, never drop.
func TestEmptyPayloadFallsBack(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	leg := &fakeLegacy{}
	b := New(nat, leg)

	if err := b.Publish("nodes/n1/down", nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(nat.sent) != 0 {
		t.Error("an empty payload must not be sent over TCP")
	}
	if len(leg.published) != 1 {
		t.Fatal("an empty payload must still reach MQTT, where it may be legal")
	}
}

// TestNilNativeIsPurePassthrough -- before the transport is wired in (or on a
// server configuration without it), behaviour must be exactly 2.x.
func TestNilNativeIsPurePassthrough(t *testing.T) {
	leg := &fakeLegacy{}
	b := New(nil, leg)
	if err := b.Publish("nodes/n1/down", []byte{0x06}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := b.PublishQoS2("nodes/n1/control", []byte{0x06}); err != nil {
		t.Fatalf("PublishQoS2: %v", err)
	}
	if len(leg.published) != 2 {
		t.Fatalf("legacy published %v, want 2", leg.published)
	}
}

// TestRetainedGoesToMQTTEvenWhenNodeHasSession -- "retained" is broker state
// with no 3.0 equivalent, so it cannot silently move to TCP.
func TestRetainedGoesToMQTTEvenWhenNodeHasSession(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	leg := &fakeLegacy{}
	b := New(nat, leg)

	if err := b.PublishRetained("nodes/n1/down", []byte{0x06}); err != nil {
		t.Fatalf("PublishRetained: %v", err)
	}
	if len(nat.sent) != 0 {
		t.Error("retained must not be sent over TCP: there is no broker to retain it")
	}
	if len(leg.published) != 1 {
		t.Fatal("retained must go to MQTT")
	}
}

// TestQoS2OverTCPIsCountedAsDowngraded -- the ACK that should replace QoS 2 is
// not implemented, so the downgrade must be VISIBLE rather than silent.
func TestQoS2OverTCPIsCountedAsDowngraded(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	b := New(nat, &fakeLegacy{})

	before := counterValue(t, metrics.DownlinkQoS2DowngradedTotal)
	if err := b.PublishQoS2("nodes/n1/control", []byte{0x06}); err != nil {
		t.Fatalf("PublishQoS2: %v", err)
	}
	after := counterValue(t, metrics.DownlinkQoS2DowngradedTotal)
	if after != before+1 {
		t.Fatalf("QoS2 downgraded count %v -> %v, want +1 (an unhonoured "+
			"delivery guarantee must be visible)", before, after)
	}
}

// TestPlainPublishIsNotCountedAsDowngraded -- only QoS2 carries the stronger
// promise; a plain publish is not a downgrade and must not inflate the metric.
func TestPlainPublishIsNotCountedAsDowngraded(t *testing.T) {
	nat := newFakeNative()
	nat.has["n1"] = true
	b := New(nat, &fakeLegacy{})

	before := counterValue(t, metrics.DownlinkQoS2DowngradedTotal)
	if err := b.Publish("nodes/n1/down", []byte{0x06}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if after := counterValue(t, metrics.DownlinkQoS2DowngradedTotal); after != before {
		t.Fatalf("a plain publish was counted as a QoS2 downgrade (%v -> %v)", before, after)
	}
}
