// Package downlink delivers server-to-device messages over the node's 3.0
// TCP+TLS session.
//
// # MQTT is gone (2026-10-08)
//
// This package used to be a COMPOSITE: it preferred the node's live TCP
// session and fell back to MQTT (design 7.3 P0-P3, the dual-stack window).
// MQTT has now been removed from the backend entirely, so there is exactly
// ONE transport left and the fallback is impossible by construction rather
// than by configuration.
//
// ⚠ What that means for callers: a message for a node with no live session
// is now an ERROR, not a silent hand-off to a broker. That is deliberate --
// the old fallback would have become a black hole once no broker consumer
// existed, which is exactly the silent-loss failure this repo keeps
// re-learning. Callers that care can retry when the node reconnects.
//
// # There are no topics any more
//
// Callers used to compute an MQTT topic (`nodes/<id>/down` or
// `nodes/<id>/control`) and hand it to Publish. That scheme is gone: the
// 3.0 connection identity is the certificate CN/SAN, so this package now
// takes the node id DIRECTLY (principle P4 -- one definition of "which node
// is this for", instead of a string that had to be parsed back apart).
//
// The old `down` vs `control` split never carried meaning over TCP (the
// frame's message type already says what the message is), and neither did
// MQTT QoS 2 (it was already downgraded to a best-effort write, and counted
// rather than hidden -- see the removed DownlinkQoS2DowngradedTotal).
package downlink

import (
	"fmt"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/protoframe"
)

// sessionSender is the slice of the transport the bridge needs.
//
// Declared here (rather than importing internal/transport) so this package
// stays testable with a fake and so the dependency points one way: downlink
// knows about transport's shape, transport knows nothing about this package.
type sessionSender interface {
	// SendToNode delivers one whole 3.0 frame to a connected node.
	SendToNode(nodeID string, frame []byte) error
	// HasSession reports whether the node is currently connected over TCP.
	HasSession(nodeID string) bool
}

// Publisher is the downlink surface the rest of the backend consumes.
//
// It is an interface (not *Bridge) so tests can substitute a recorder, and
// so the packages that send messages do not depend on the transport's shape.
type Publisher interface {
	// Publish delivers one 2.x payload to a node, framed as a 3.0 message.
	// Returns an error when the node has no live session: with MQTT gone
	// there is no second path, and reporting success would be a lie.
	Publish(nodeID string, payload []byte) error
}

// Bridge is the only Publisher implementation: it frames a payload and sends
// it down the node's live session.
type Bridge struct {
	native sessionSender
}

// New builds a bridge over the device registry.
//
// native must not be nil. There is no fallback to construct around any more,
// so a nil sender could only ever produce "every downlink fails" -- a
// configuration mistake that must be loud at startup, not silent per message.
func New(native sessionSender) *Bridge {
	if native == nil {
		panic("downlink: a session sender is required; without it no downlink " +
			"can be delivered and every send would fail silently")
	}
	return &Bridge{native: native}
}

// Publish frames one payload and delivers it to nodeID.
func (b *Bridge) Publish(nodeID string, payload []byte) error {
	if nodeID == "" {
		// Without a node there is no session to look up. Refuse rather than
		// guess: the old code treated "no node id" as "not node-scoped, send
		// to MQTT", and that path no longer exists.
		return fmt.Errorf("downlink: empty node id; cannot deliver %d-byte payload", len(payload))
	}

	enc, err := wrapFrame(payload)
	if err != nil {
		metrics.DownlinkWrapFailedTotal.Inc()
		return fmt.Errorf("downlink: cannot frame payload for %s: %w", nodeID, err)
	}

	if err := b.native.SendToNode(nodeID, enc); err != nil {
		metrics.DownlinkNativeFailedTotal.Inc()
		// No fallback: report the failure to the caller. Losing it here would
		// be indistinguishable from a delivered message.
		return fmt.Errorf("downlink: no live session for %s: %w", nodeID, err)
	}
	metrics.DownlinkOverNativeTotal.Inc()
	return nil
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

// keep the frame import explicit about the type space
var _ = frame.MsgHello
