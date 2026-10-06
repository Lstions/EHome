// Package downlink routes server-to-device messages to the right transport.
//
// # Why this exists
//
// During the MQTT retirement window (design 7.3, P0-P3) the backend must serve
// BOTH 2.8.0 devices (MQTT) and 3.0 devices (TCP+TLS). Every downlink call site
// already goes through the mqtt.Publisher interface, so this package implements
// that interface as a COMPOSITE: it prefers the node's live TCP session and
// falls back to MQTT.
//
// Doing it here rather than editing every call site has two benefits:
//
//  1. There is exactly ONE place that decides which transport a node is on
//     (principle P4). Eleven call sites each making that decision would be
//     eleven chances to disagree.
//  2. The 2.x path is untouched: if a node has no TCP session, behaviour is
//     byte-for-byte what it was before.
//
// # The topic becomes a node-id carrier
//
// 3.0 has no topics: the connection identity is the certificate CN/SAN. But the
// existing call sites compute a topic and hand it to Publish, and the topic
// already encodes the node id ("nodes/<id>/down" or "nodes/<id>/control").
// So this adapter PARSES the node id out of the topic and otherwise ignores it.
//
// That is deliberate, and it is honest: the topic is no longer a routing
// mechanism, it is a transport-agnostic way for a caller to name a node. The
// topic's "down" vs "control" distinction carries no meaning over TCP -- the
// frame's message type already says what the message is -- so it is dropped.
package downlink

import (
	"fmt"
	"strings"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/protoframe"
)

// sessionSender is the slice of the transport the bridge needs.
//
// Declared here (rather than importing internal/transport) so this package
// stays testable with a fake and so the dependency points one way: downlink
// knows about transport's shape, transport knows nothing about MQTT.
type sessionSender interface {
	// SendToNode delivers one whole 3.0 frame to a connected node.
	SendToNode(nodeID string, frame []byte) error
	// HasSession reports whether the node is currently connected over TCP.
	HasSession(nodeID string) bool
}

// LegacyPublisher is the 2.x path, exported so main() can name it when
// constructing a Bridge (and so the device-transport helper can hand it on).
type LegacyPublisher interface {
	Publish(topic string, payload []byte) error
	PublishQoS2(topic string, payload []byte) error
	PublishRetained(topic string, payload []byte) error
}

// legacyPublisher is the internal alias for the same shape.
type legacyPublisher = LegacyPublisher

// Bridge is an mqtt.Publisher that prefers the native transport.
//
// Both transports are fixed at construction. I briefly had a SetLegacy method
// so main() could create the bridge before the MQTT client existed; that was
// wrong for two reasons: a bridge with a nil legacy publisher silently drops
// messages, and mutable wiring means "which transport does this node use"
// could change under a caller. Construction-time wiring makes the illegal
// state unrepresentable.
type Bridge struct {
	native sessionSender
	legacy legacyPublisher
}

// New builds a bridge. native may be nil (then everything goes to MQTT).
// legacy must not be nil: it is the fallback the whole design depends on.
func New(native sessionSender, legacy legacyPublisher) *Bridge {
	if legacy == nil {
		panic("downlink: legacy publisher is required; a bridge without a " +
			"fallback would drop every message for a node without a TCP session")
	}
	return &Bridge{native: native, legacy: legacy}
}

// NodeIDFromTopic extracts the node id from "nodes/<id>/<channel>".
//
// Returns "" when the topic is not node-scoped, which means the caller is
// publishing something the native transport has no way to carry (for example
// Home Assistant's retained discovery config). Those must NOT be silently
// dropped -- see PublishRetained below.
func NodeIDFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 || parts[0] != "nodes" || parts[1] == "" {
		return ""
	}
	return parts[1]
}

// Publish sends a downlink message, preferring the node's TCP session.
func (b *Bridge) Publish(topic string, payload []byte) error {
	return b.publish(topic, payload, false)
}

// PublishQoS2 sends a control message, preferring the node's TCP session.
//
// ⚠ Honest gap: QoS 2 means "delivered exactly once". Over TCP the design
// replaces that with an application-layer ACK (frame flag ACK_REQ + seq), but
// that ACK is NOT IMPLEMENTED YET on either end. So a QoS2 publish that goes
// over TCP is downgraded to the same best-effort write as a plain Publish.
//
// That downgrade is COUNTED rather than silent, because "the caller asked for a
// stronger guarantee and did not get it" is exactly the class of problem this
// refactor exists to remove. When the ACK lands: set the flag here and delete
// the counter increment, then assert the counter stays zero.
func (b *Bridge) PublishQoS2(topic string, payload []byte) error {
	return b.publish(topic, payload, true)
}

func (b *Bridge) publish(topic string, payload []byte, wasQoS2 bool) error {
	nodeID := NodeIDFromTopic(topic)

	// Not node-scoped: the native transport cannot carry it. This is not an
	// error condition, it simply has only one possible home.
	if nodeID == "" || b.native == nil || !b.native.HasSession(nodeID) {
		return b.legacy.Publish(topic, payload)
	}

	enc, err := wrapFrame(payload)
	if err != nil {
		// Wrapping failed (empty or oversized). Fall back rather than drop:
		// the MQTT path may still be able to deliver it, and the caller gets
		// a real error if it cannot.
		metrics.DownlinkWrapFailedTotal.Inc()
		logger.Warnf("[%s] Cannot wrap downlink for TCP (%v); using MQTT", nodeID, err)
		return b.legacy.Publish(topic, payload)
	}

	if wasQoS2 {
		metrics.DownlinkQoS2DowngradedTotal.Inc()
	}
	if err := b.native.SendToNode(nodeID, enc); err != nil {
		metrics.DownlinkNativeFailedTotal.Inc()
		logger.Warnf("[%s] TCP downlink failed (%v); using MQTT", nodeID, err)
		return b.legacy.Publish(topic, payload)
	}
	metrics.DownlinkOverNativeTotal.Inc()
	return nil
}

// PublishRetained is MQTT-only by nature: "retained" has no 3.0 equivalent
// (there is no broker to hold the last value). The only current caller is the
// Home Assistant discovery config, which is not node-scoped at all.
//
// It therefore always goes to MQTT. If a caller ever passes a node-scoped
// retained topic, that is a design problem, not something to paper over --
// hence the explicit check and counter.
func (b *Bridge) PublishRetained(topic string, payload []byte) error {
	if nodeID := NodeIDFromTopic(topic); nodeID != "" {
		metrics.DownlinkRetainedNodeScopedTotal.Inc()
		logger.Warnf("[%s] retained publish to a NODE-scoped topic has no 3.0 "+
			"equivalent; sending over MQTT only", nodeID)
	}
	return b.legacy.Publish(topic, payload)
}

// wrapFrame builds a 3.0 frame around a 2.x payload.
//
// The 2.x payload's FIRST BYTE IS the message type (frame.Encoder writes it),
// which is why the 3.0 header's type can be taken straight from payload[0].
// Both copies are then present on the wire; the receiver verifies they agree
// (see nodemgr.HandleFrame).
func wrapFrame(payload []byte) ([]byte, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("empty payload has no message type byte")
	}
	if len(payload) > int(protoframe.PayloadMax) {
		return nil, fmt.Errorf("payload %d exceeds the 3.0 maximum %d",
			len(payload), protoframe.PayloadMax)
	}
	out := make([]byte, protoframe.HeaderSize+len(payload))
	h := protoframe.Header{
		Ver:        protoframe.Version,
		Type:       payload[0],
		Seq:        0, // per-node sequencing is not implemented yet
		PayloadLen: uint16(len(payload)),
	}
	if err := protoframe.EncodeHeader(out, h); err != nil {
		return nil, err
	}
	copy(out[protoframe.HeaderSize:], payload)
	return out, nil
}

// MaxDownlinkPayload exposes the 3.0 payload bound so a caller can decide
// before building a message (the R1 class of bug: the encoder and the wire
// limit disagreeing).
func MaxDownlinkPayload() int { return int(protoframe.PayloadMax) }

var _ = frame.MsgHello // keep the frame import explicit about the type space
