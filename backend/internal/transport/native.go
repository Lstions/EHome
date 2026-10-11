package transport

import "fmt"

// native.go -- the slice of the transport that the downlink bridge consumes.
//
// The bridge (internal/downlink) is written against a two-method interface
// declared in its own package, so this file exists to adapt *Registry to it.
// Keeping the adapter here means the bridge never imports transport internals,
// and the compile-time assertion below keeps the two in step: if either side
// changes shape, the build fails instead of the downlink silently stopping.

var _ interface {
	SendToNode(nodeID string, frame []byte) error
	HasSession(nodeID string) bool
} = (*Registry)(nil)

// HasSession reports whether the node currently has a live session.
//
// P1: the caller uses this to CHOOSE a transport, so it must answer about the
// present, not about "last time we looked".
func (r *Registry) HasSession(nodeID string) bool {
	_, ok := r.Get(nodeID)
	return ok
}

// SendToNode delivers one whole 3.0 frame to a connected node.
//
// Translates SendResult into an error because the bridge's contract is the
// MQTT Publisher one (error or nil). The interesting part is that NOT EVERY
// non-OK result is an error worth falling back on:
//
//   - SendNotReady: no session. The caller checked HasSession first, so this
//     is a race (the node dropped in between). Falling back to MQTT is right.
//   - SendPayloadTooBig: the frame is malformed for this transport. Falling
//     back is right, and the bridge also counts it.
//   - SendFatal: the connection is broken. Falling back is right.
//
// All of them become errors, but the message says which, so a log line is
// enough to tell "the node vanished" from "we built a bad frame".
func (r *Registry) SendToNode(nodeID string, frame []byte) error {
	switch res := r.Send(nodeID, frame); res {
	case SendOK:
		return nil
	case SendNotReady:
		return fmt.Errorf("no live session for node %s", nodeID)
	case SendPayloadTooBig:
		return fmt.Errorf("frame of %d bytes exceeds the transport MTU", len(frame))
	case SendBackpressure:
		return fmt.Errorf("node %s is applying backpressure", nodeID)
	default:
		return fmt.Errorf("send to %s failed: %s", nodeID, res)
	}
}
