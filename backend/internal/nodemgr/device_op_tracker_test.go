package nodemgr

import (
	"testing"
	"time"

	"ehome/backend/pkg/frame"
)

// device_op_tracker_test.go -- pairing ACKs with requests.
//
// The whole reason this type exists is the LATE ACK: an ACK that arrives after
// the server has moved on must not be attributed to the current request. A test
// that only checks "an ACK resolves the wait" would pass with no correlation ID
// at all, so these assertions name request ids explicitly.

func TestBeginReservesAndReturnsID(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	p, err := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if p.RequestID() == "" {
		t.Fatal("Begin returned an empty request id; the device would echo back " +
			"nothing and no ACK could ever be matched")
	}
	if !tr.PendingFor("n1") {
		t.Fatal("the request was not recorded as pending")
	}
}

// TestSecondRequestIsRefused -- two concurrent reboots have no coherent meaning.
func TestSecondRequestIsRefused(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	if _, err := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	_, err := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)
	if err == nil {
		t.Fatal("a second concurrent request was accepted; the operator would see " +
			"'accepted' twice and the second reboot could execute much later")
	}
}

// TestDifferentNodesAreIndependent -- single-flight is PER NODE.
func TestDifferentNodesAreIndependent(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	if _, err := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second); err != nil {
		t.Fatalf("n1: %v", err)
	}
	if _, err := tr.Begin("n2", frame.DeviceOpReboot, now, time.Second); err != nil {
		t.Fatalf("n2 must not be blocked by n1: %v", err)
	}
	if tr.Pending() != 2 {
		t.Fatalf("Pending = %d, want 2", tr.Pending())
	}
}

// TestMatchingAckResolves -- the happy path, with the id echoed back.
func TestMatchingAckResolves(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	p, _ := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)

	ok := tr.Complete("n1", frame.DeviceOpAck{
		RequestID: p.RequestID(), Result: frame.DeviceOpOK, Detail: "rebooting",
	})
	if !ok {
		t.Fatal("a matching ACK was not accepted")
	}
	select {
	case out := <-p.Done():
		if !out.Acked {
			t.Fatal("outcome is not marked Acked")
		}
		if out.Result != frame.DeviceOpOK {
			t.Fatalf("result = %v, want DeviceOpOK", out.Result)
		}
		if out.Op != frame.DeviceOpReboot {
			t.Fatalf("op = %v, want reboot", out.Op)
		}
	case <-time.After(time.Second):
		t.Fatal("Done() never fired for a matching ACK")
	}
	if tr.PendingFor("n1") {
		t.Fatal("the slot was not freed after a matching ACK")
	}
}

// TestStaleAckIsRejected is THE test for the defect this type exists to prevent.
//
// A late ACK from an earlier request must not resolve the current one. Without
// the request-id check this passes (it resolves) and the operator is told an
// operation succeeded when its ACK was never seen.
func TestStaleAckIsRejected(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()

	first, _ := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)
	firstID := first.RequestID()
	// The first request times out (ACK lost).
	tr.Fail("n1", firstID, nil)

	second, _ := tr.Begin("n1", frame.DeviceOpReboot, now.Add(time.Second), time.Second)

	// The OLD ack finally arrives.
	if tr.Complete("n1", frame.DeviceOpAck{
		RequestID: firstID, Result: frame.DeviceOpOK,
	}) {
		t.Fatal("a STALE ACK (from the previous request) resolved the current one; " +
			"the operator would be told the reboot succeeded based on an ACK that " +
			"belonged to an operation that already timed out")
	}
	if !tr.PendingFor("n1") {
		t.Fatal("the current request was consumed by a stale ACK")
	}
	// The real ACK still works.
	if !tr.Complete("n1", frame.DeviceOpAck{
		RequestID: second.RequestID(), Result: frame.DeviceOpOK,
	}) {
		t.Fatal("the correct ACK was rejected")
	}
}

// TestBogusAckIsRejected -- an ACK for a request that never existed.
func TestBogusAckIsRejected(t *testing.T) {
	tr := NewDeviceOpTracker()
	tr.Begin("n1", frame.DeviceOpReboot, time.Now(), time.Second)
	if tr.Complete("n1", frame.DeviceOpAck{RequestID: "not-a-real-id", Result: frame.DeviceOpOK}) {
		t.Fatal("an ACK with an unknown request id was accepted")
	}
}

// TestFailReportsLocalError -- a send failure must resolve the waiter, not hang.
func TestFailReportsLocalError(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	p, _ := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)
	tr.Fail("n1", p.RequestID(), errBoom{})

	select {
	case out := <-p.Done():
		if out.Acked {
			t.Fatal("a local failure was reported as a device ACK")
		}
		if out.Err == nil {
			t.Fatal("a local failure carried no error")
		}
	case <-time.After(time.Second):
		t.Fatal("Fail did not resolve the waiter -- the operator's request would hang forever")
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "send failed" }

// TestExpireResolvesTheWaiter -- the bug I introduced and this test would have caught.
//
// My first Expire deleted the entry and returned WITHOUT writing to p.done, so
// whoever waited on Done() blocked forever. Expiry is also a different fact
// from a device-reported failure, so it must be Acked=false.
func TestExpireResolvesTheWaiter(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	p, _ := tr.Begin("n1", frame.DeviceOpReboot, now, 10*time.Millisecond)

	expired := tr.Expire(now.Add(time.Second))
	if len(expired) != 1 {
		t.Fatalf("Expire returned %d outcomes, want 1", len(expired))
	}
	select {
	case out := <-p.Done():
		if out.Acked {
			t.Fatal("an expiry was reported as an ACK; 'we never heard back' and " +
				"'the device said it failed' are different facts")
		}
		if out.Err == nil {
			t.Fatal("an expiry carried no error")
		}
	case <-time.After(time.Second):
		t.Fatal("Expire did NOT resolve the waiter -- it would block forever")
	}
	if tr.PendingFor("n1") {
		t.Fatal("the expired request still holds the node's single-flight slot; " +
			"the operator could never retry")
	}
}

// TestExpireLeavesLiveRequestsAlone.
func TestExpireLeavesLiveRequestsAlone(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	tr.Begin("n1", frame.DeviceOpReboot, now, time.Hour)
	if got := tr.Expire(now.Add(time.Second)); len(got) != 0 {
		t.Fatalf("Expire killed a live request: %v", got)
	}
	if !tr.PendingFor("n1") {
		t.Fatal("a live request lost its slot")
	}
}

// TestRequestIDsAreUnique -- two nodes at the same instant must not collide.
func TestRequestIDsAreUnique(t *testing.T) {
	tr := NewDeviceOpTracker()
	now := time.Now()
	a, _ := tr.Begin("n1", frame.DeviceOpReboot, now, time.Second)
	b, _ := tr.Begin("n2", frame.DeviceOpReboot, now, time.Second)
	if a.RequestID() == b.RequestID() {
		t.Fatalf("two requests share the id %q; an ACK could be matched to the "+
			"wrong node's request", a.RequestID())
	}
}
