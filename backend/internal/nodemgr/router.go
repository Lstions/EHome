package nodemgr

import "ehome/backend/pkg/protoframe"

// router.go -- bridges the 3.0 TCP+TLS transport to the message dispatcher.
//
// The transport knows nothing about message semantics: it delimits frames and
// hands over (nodeID, header, payload). This adapter is the seam that turns
// that into a dispatch call.
//
// Why an adapter rather than making the transport import nodemgr: the
// transport is deliberately unaware of what messages mean, so it can be
// tested with synthetic frames (see internal/transport tests). Wiring the
// dependency upward would drag ten packages' worth of behaviour into every
// framing test.

// FrameHandler returns a function suitable for transport.Config.OnFrame.
//
// Keeping it here (rather than inlining a closure at the call site) means the
// routing decision -- node id from the certificate, type from the header -- is
// written once and testable without a socket.
func (m *Manager) FrameHandler() func(nodeID string, h protoframe.Header, payload []byte) error {
	return func(nodeID string, h protoframe.Header, payload []byte) error {
		// The header type is authoritative for ROUTING; HandleFrame also
		// verifies the payload's own type byte agrees, and drops the frame
		// (with a metric) if it does not.
		m.HandleFrame(nodeID, h.Type, payload)
		// A per-frame error would tear down the connection. The dispatcher
		// already records and drops individually bad frames, so a rejected
		// message must not kill a healthy device's session.
		return nil
	}
}
