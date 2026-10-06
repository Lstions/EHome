package transport

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"ehome/backend/pkg/protoframe"
)

// reply_test.go -- the server must be able to send DOWNLINK, not only receive.
//
// Until now the transport could read frames and do nothing with them: no
// session was published, so there was no way to address a connected device.
// Nothing that requires a request/response exchange (MsgDeviceOp and its ACK,
// config sync, OTA command) could work.

// TestServerCanReplyToConnectedDevice is the end-to-end proof.
//
// It connects a device, waits for the node to appear in the registry, sends a
// downlink frame, and reads it back **off the device's own socket**. Asserting
// only on counters would repeat the D-30 mistake: counters stayed green while
// the bytes on the wire were wrong.
func TestServerCanReplyToConnectedDevice(t *testing.T) {
	pki := newPKI(t)
	srv, addr, _ := startServer(t, pki, Config{})
	conn := dialDevice(t, pki, addr, "node-reply")

	// Wait until the device is registered (registration happens on the server
	// goroutine, after the TLS handshake).
	waitNodePresent(t, srv, "node-reply")

	payload := []byte("device-op-please-reboot")
	frame := buildFrame(t, 0x22, 1, payload) // 0x22 = MsgDeviceOp

	if r := srv.Send("node-reply", frame); r != SendOK {
		t.Fatalf("Send = %v, want SendOK", r)
	}

	// Read the frame back on the DEVICE side.
	got := readFrame(t, conn)
	if string(got) != string(frame) {
		t.Fatalf("device received %x, want %x", got, frame)
	}
}

// TestDownlinkToUnknownNodeIsNotReady -- P1: the caller must be able to tell
// "that node is not connected" from "I wrote to it and the write failed".
func TestDownlinkToUnknownNodeIsNotReady(t *testing.T) {
	pki := newPKI(t)
	srv, _, _ := startServer(t, pki, Config{})
	if r := srv.Send("nobody", buildFrame(t, 0x22, 1, []byte("x"))); r != SendNotReady {
		t.Fatalf("Send to an unconnected node = %v, want SendNotReady", r)
	}
	if st := srv.Registry().Stats(); st.SendsToMissing != 1 {
		t.Errorf("SendsToMissing = %d, want 1 (an unroutable downlink must be visible)",
			st.SendsToMissing)
	}
}

// TestDisconnectDeregistersNode -- and critically, the disconnect hook must
// carry the REAL node id.
//
// It used to pass "": every disconnect reported an empty node, so cleanup
// keyed on the node could never work. That is invisible in a test that only
// counts connections, which is why this asserts the id itself.
func TestDisconnectDeregistersNode(t *testing.T) {
	pki := newPKI(t)

	type ev struct {
		node string
		err  error
	}
	disc := make(chan ev, 4)
	cfg := Config{OnDisconnect: func(nodeID string, err error) {
		disc <- ev{nodeID, err}
	}}
	srv, addr, _ := startServer(t, pki, cfg)
	conn := dialDevice(t, pki, addr, "node-bye")
	waitNodePresent(t, srv, "node-bye")

	_ = conn.Close()

	select {
	case e := <-disc:
		if e.node == "" {
			t.Fatal("OnDisconnect reported an EMPTY node id -- cleanup keyed on the " +
				"node can never work, and the log names nobody")
		}
		if e.node != "node-bye" {
			t.Fatalf("OnDisconnect node = %q, want %q", e.node, "node-bye")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnDisconnect never fired")
	}

	// And the node must be gone from the directory.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := srv.Registry().Get("node-bye"); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("node still registered after disconnect")
}

// TestReconnectDoesNotLeaveTwoSessions -- device reboots and reconnects before
// the server notices the old socket died. Both connections are live; only one
// may be in the directory, and downlinks must go to the NEW one.
func TestReconnectDoesNotLeaveTwoSessions(t *testing.T) {
	pki := newPKI(t)
	srv, addr, _ := startServer(t, pki, Config{})

	first := dialDevice(t, pki, addr, "node-flap")
	waitNodePresent(t, srv, "node-flap")

	second := dialDevice(t, pki, addr, "node-flap")
	// Wait for the replacement to be installed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := srv.Registry().Get("node-flap"); ok && s.Remote() == second.RemoteAddr().String() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := srv.Registry().Stats(); st.Live != 1 {
		t.Fatalf("Live = %d, want exactly 1 session for one node", st.Live)
	}

	frame := buildFrame(t, 0x22, 2, []byte("to-the-new-conn"))
	if r := srv.Send("node-flap", frame); r != SendOK {
		t.Fatalf("Send = %v", r)
	}
	if got := readFrame(t, second); string(got) != string(frame) {
		t.Fatalf("new connection received %x, want %x", got, frame)
	}

	// The old socket must not receive the downlink. It may be closed by the
	// server; either way nothing should arrive on it.
	_ = first.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 64)
	if n, err := first.Read(buf); err == nil && n > 0 {
		t.Fatalf("the REPLACED connection received %x -- downlink went to a dead session", buf[:n])
	}
}

// --- helpers ---

func waitNodePresent(t *testing.T, srv *Server, node string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := srv.Registry().Get(node); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node %q never appeared in the registry", node)
}

// readFrame reads exactly one frame from the device side of the connection.
//
// Reads until a whole frame is present: a TCP read boundary is not a frame
// boundary, so a single Read is not enough.
func readFrame(t *testing.T, conn *tls.Conn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		if len(buf) >= protoframe.HeaderSize {
			h, err := protoframe.DecodeHeader(buf)
			if err == nil {
				total := h.FrameBytes()
				if len(buf) >= total {
					return buf[:total]
				}
			}
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatalf("connection closed while waiting for a frame (got %d bytes)", len(buf))
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("timed out waiting for a frame (got %d bytes)", len(buf))
			}
			t.Fatalf("read: %v", err)
		}
	}
}
