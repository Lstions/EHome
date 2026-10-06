package transport

import (
	"sync"
	"testing"
	"time"
)

// registry_test.go -- node directory.
//
// The interesting case is REPLACEMENT: a device reconnects before the server
// notices the old connection died. The obvious implementation (delete by
// node_id on disconnect) then evicts the healthy NEW session, and the device
// looks online while the server insists it has no session.

func regConn(id string) *Session {
	return NewSession(id, &fakeConn{}, time.Second)
}

// TestRegisterReplacesAndClosesPrevious -- one node must never have two live
// sessions: the old one keeps its socket and could still write.
func TestRegisterReplacesAndClosesPrevious(t *testing.T) {
	r := NewRegistry()
	old := regConn("n1")
	if prev := r.Register(old); prev != nil {
		t.Fatalf("first Register returned a previous session: %v", prev)
	}
	neu := regConn("n1")
	prev := r.Register(neu)
	if prev != old {
		t.Fatalf("Register returned %v, want the displaced old session", prev)
	}
	if old.Stats().Connected {
		t.Error("the displaced session is still marked connected -- two live sessions for one node")
	}
	if !neu.Stats().Connected {
		t.Error("the new session must stay connected")
	}
	if s, ok := r.Get("n1"); !ok || s != neu {
		t.Fatal("Get must return the NEW session")
	}
	if st := r.Stats(); st.Replaced != 1 {
		t.Errorf("Replaced = %d, want 1", st.Replaced)
	}
}

// TestStaleCleanupDoesNotEvictReplacement -- THE subtle one.
//
// Sequence: old session is replaced by a new one, THEN the old connection's
// cleanup runs. If cleanup deletes by node_id, it removes the healthy new
// session and the device becomes unreachable while its socket is fine.
func TestStaleCleanupDoesNotEvictReplacement(t *testing.T) {
	r := NewRegistry()
	old := regConn("n1")
	r.Register(old)
	neu := regConn("n1")
	r.Register(neu)

	// The old connection now finishes its cleanup.
	if removed := r.RemoveIfSame(old); removed {
		t.Fatal("RemoveIfSame(stale) reported a removal -- it must be a no-op")
	}
	s, ok := r.Get("n1")
	if !ok {
		t.Fatal("the replacement session was EVICTED by the stale cleanup: " +
			"device is online but the server has no session")
	}
	if s != neu {
		t.Fatal("Get returned the wrong session")
	}
	if !neu.Stats().Connected {
		t.Error("the replacement session must still be connected")
	}
}

// TestRemoveIfSameRemovesTheCurrentSession -- the normal disconnect path.
func TestRemoveIfSameRemovesTheCurrentSession(t *testing.T) {
	r := NewRegistry()
	s := regConn("n1")
	r.Register(s)
	if !r.RemoveIfSame(s) {
		t.Fatal("RemoveIfSame(current) must remove")
	}
	if _, ok := r.Get("n1"); ok {
		t.Fatal("session still present after removal")
	}
	if st := r.Stats(); st.Live != 0 || st.Removed != 1 {
		t.Fatalf("stats %+v", st)
	}
	// Removing again is a no-op, not a double count.
	if r.RemoveIfSame(s) {
		t.Error("second RemoveIfSame must report false")
	}
}

// TestSendToMissingNodeIsNotReadyAndCounted -- P1: the caller must be able to
// tell "no such node" from "node present, write failed".
func TestSendToMissingNodeIsNotReadyAndCounted(t *testing.T) {
	r := NewRegistry()
	if got := r.Send("ghost", mkFrame(t, 0x03, 1, []byte("x"))); got != SendNotReady {
		t.Fatalf("Send to unknown node = %v, want SendNotReady", got)
	}
	if st := r.Stats(); st.SendsToMissing != 1 {
		t.Errorf("SendsToMissing = %d, want 1 (an unroutable downlink must be visible)",
			st.SendsToMissing)
	}
}

// TestSendReachesTheLiveSession -- the happy path, asserted on BYTES so the
// frame really lands on the connection.
func TestSendReachesTheLiveSession(t *testing.T) {
	r := NewRegistry()
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	r.Register(s)

	frame := mkFrame(t, 0x03, 7, []byte("hello"))
	if got := r.Send("n1", frame); got != SendOK {
		t.Fatalf("Send = %v, want SendOK", got)
	}
	if string(c.bytesWritten()) != string(frame) {
		t.Fatal("frame did not reach the session's connection")
	}
}

// TestSendToReplacedSessionDoesNotReachOldConn -- after replacement, a downlink
// must go to the NEW connection only.
func TestSendToReplacedSessionDoesNotReachOldConn(t *testing.T) {
	r := NewRegistry()
	oldConn := &fakeConn{}
	newConn := &fakeConn{}
	r.Register(NewSession("n1", oldConn, time.Second))
	r.Register(NewSession("n1", newConn, time.Second))

	frame := mkFrame(t, 0x03, 1, []byte("routed"))
	if got := r.Send("n1", frame); got != SendOK {
		t.Fatalf("Send = %v", got)
	}
	if len(oldConn.bytesWritten()) != 0 {
		t.Error("frame was written to the REPLACED connection")
	}
	if string(newConn.bytesWritten()) != string(frame) {
		t.Error("frame did not reach the live connection")
	}
}

// TestNodesIsSorted -- stable output; logs and tests should not depend on map
// iteration order (Go randomises it deliberately).
func TestNodesIsSorted(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"n3", "n1", "n2"} {
		r.Register(regConn(id))
	}
	got := r.Nodes()
	want := []string{"n1", "n2", "n3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Nodes() = %v, want %v", got, want)
		}
	}
}

// TestRegistryConcurrentRegisterAndSend exercises the lock under -race.
// Two goroutines reconnecting while another sends is the real-world pattern.
func TestRegistryConcurrentRegisterAndSend(t *testing.T) {
	r := NewRegistry()
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Register(NewSession("n1", &fakeConn{}, time.Second))
		}()
		go func() {
			defer wg.Done()
			r.Send("n1", mkFrame(t, 0x03, 1, []byte("x")))
		}()
	}
	wg.Wait()
	if st := r.Stats(); st.Live != 1 {
		t.Fatalf("Live = %d, want exactly 1 session for one node", st.Live)
	}
}

// TestCloseAllEmptiesDirectory -- shutdown must not leave sessions behind.
func TestCloseAllEmptiesDirectory(t *testing.T) {
	r := NewRegistry()
	s := regConn("n1")
	r.Register(s)
	r.CloseAll("shutdown")
	if len(r.Nodes()) != 0 {
		t.Fatal("sessions remain after CloseAll")
	}
	if s.Stats().Connected {
		t.Error("CloseAll must close the sessions")
	}
}
