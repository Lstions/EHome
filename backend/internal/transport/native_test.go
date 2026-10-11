package transport

import (
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/protoframe"
)

// native_test.go -- the two-method slice the downlink bridge depends on.
//
// These are thin, but they are the contract that decides WHICH transport a
// downlink takes, so they get their own assertions rather than being covered
// incidentally.

func TestHasSessionTracksTheDirectory(t *testing.T) {
	r := NewRegistry()
	if r.HasSession("n1") {
		t.Fatal("HasSession is true before any registration")
	}
	s := NewSession("n1", &fakeConn{}, time.Second)
	r.Register(s)
	if !r.HasSession("n1") {
		t.Fatal("HasSession is false right after registration -- a downlink " +
			"would be misrouted to MQTT while the device is on TCP")
	}
	r.RemoveIfSame(s)
	if r.HasSession("n1") {
		t.Fatal("HasSession is still true after removal")
	}
}

func TestHasSessionAfterReplacement(t *testing.T) {
	r := NewRegistry()
	old := NewSession("n1", &fakeConn{}, time.Second)
	r.Register(old)
	neu := NewSession("n1", &fakeConn{}, time.Second)
	r.Register(neu)

	if !r.HasSession("n1") {
		t.Fatal("HasSession false after a reconnect")
	}
	// A stale cleanup must not make the node look absent.
	r.RemoveIfSame(old)
	if !r.HasSession("n1") {
		t.Fatal("a stale cleanup made a live node look absent -- downlinks " +
			"would silently go to MQTT instead of the live TCP session")
	}
}

func TestSendToNodeReportsWhyItFailed(t *testing.T) {
	r := NewRegistry()
	// No session at all.
	err := r.SendToNode("ghost", mkFrame(t, 0x03, 1, []byte("x")))
	if err == nil {
		t.Fatal("SendToNode to an unknown node returned nil")
	}
	if !strings.Contains(err.Error(), "no live session") {
		t.Fatalf("error %q does not say the node has no session; a log line "+
			"cannot then tell 'node vanished' from 'we built a bad frame'", err)
	}

	// Present session, oversized frame.
	r.Register(NewSession("n1", &fakeConn{}, time.Second))
	err = r.SendToNode("n1", make([]byte, protoframe.MaxFrameBytes+1))
	if err == nil {
		t.Fatal("SendToNode accepted an oversized frame")
	}
	if !strings.Contains(err.Error(), "exceeds the transport MTU") {
		t.Fatalf("error %q does not name the size problem", err)
	}
}

func TestSendToNodeDeliversBytes(t *testing.T) {
	r := NewRegistry()
	c := &fakeConn{}
	r.Register(NewSession("n1", c, time.Second))

	frame := mkFrame(t, 0x22, 1, []byte("reboot-please"))
	if err := r.SendToNode("n1", frame); err != nil {
		t.Fatalf("SendToNode: %v", err)
	}
	if string(c.bytesWritten()) != string(frame) {
		t.Fatal("the frame did not reach the connection")
	}
}
