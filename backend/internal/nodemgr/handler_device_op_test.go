package nodemgr

import (
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/metrics"
)

// handler_device_op_test.go -- the server half of reboot / factory-reset.
//
// The four outcomes must stay distinguishable end to end:
//   sent+acked(ok) / sent+acked(no) / sent+unacked / refused-locally
// Collapsing "unacked" into "failed" is the specific error worth guarding,
// because a reboot that was never acknowledged may well have happened.

type fakeOpPublisher struct {
	mu       sync.Mutex
	nodeIDs  []string
	payloads [][]byte
	err      error
	// onPublish runs while the "publish" is in flight, so a test can deliver
	// the ACK before SendDeviceOp starts waiting.
	onPublish func(payload []byte)
}

func (f *fakeOpPublisher) Publish(nodeID string, payload []byte) error {
	f.mu.Lock()
	f.nodeIDs = append(f.nodeIDs, nodeID)
	f.payloads = append(f.payloads, append([]byte(nil), payload...))
	err := f.err
	hook := f.onPublish
	f.mu.Unlock()
	if hook != nil {
		hook(payload)
	}
	return err
}

func newOpManager(pub *fakeOpPublisher) *Manager {
	m := &Manager{downlink: pub}
	m.deviceOps = NewDeviceOpTracker()
	return m
}

// TestSendDeviceOpDeliversEncodableRequest -- what goes on the wire must decode
// back to the operation the operator asked for.
func TestSendDeviceOpDeliversEncodableRequest(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)

	// Answer as soon as the request is published, from another goroutine.
	pub.onPublish = func(payload []byte) {
		req, err := frame.DecodeDeviceOp(payload)
		if err != nil {
			t.Errorf("published payload does not decode: %v", err)
			return
		}
		if req.Op != frame.DeviceOpReboot {
			t.Errorf("op = %d, want reboot", req.Op)
		}
		go func() {
			// Give SendDeviceOp a moment to start waiting.
			time.Sleep(10 * time.Millisecond)
			m.handleDeviceOpAck("n1", mustAck(t, req.RequestID, frame.DeviceOpOK))
		}()
	}

	out, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, 2*time.Second)
	if err != nil {
		t.Fatalf("SendDeviceOp: %v", err)
	}
	if !out.Acked || out.Result != frame.DeviceOpOK {
		t.Fatalf("outcome = %+v, want acked OK", out)
	}
	if len(pub.nodeIDs) != 1 {
		t.Fatalf("published %d times, want 1", len(pub.nodeIDs))
	}
}

func mustAck(t *testing.T, requestID string, result frame.DeviceOpResult) []byte {
	t.Helper()
	b, err := frame.EncodeDeviceOpAck(requestID, result, "")
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	return b
}

// TestSendDeviceOpRefusesWhileInFlight -- the second request must be refused,
// and must NOT be delivered to the device.
func TestSendDeviceOpRefusesWhileInFlight(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)
	// First request never gets an ACK; keep it pending.
	if _, err := m.deviceOps.Begin("n1", frame.DeviceOpReboot, time.Now(), time.Hour); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, time.Second); err == nil {
		t.Fatal("a second request was accepted while one was in flight")
	}
	if len(pub.nodeIDs) != 0 {
		t.Fatalf("a refused request was still published (%v); the device would "+
			"receive two reboots", pub.nodeIDs)
	}
}

// TestSendDeviceOpReportsSendFailure -- and frees the slot so the operator can retry.
func TestSendDeviceOpReportsSendFailure(t *testing.T) {
	pub := &fakeOpPublisher{err: errors.New("transport down")}
	m := newOpManager(pub)

	_, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, time.Second)
	if err == nil {
		t.Fatal("a failed publish was reported as success")
	}
	if m.PendingDeviceOp("n1") {
		t.Fatal("a failed send left the node's slot reserved; the operator could " +
			"never retry")
	}
}

// TestUnackedIsNotReportedAsFailure -- the outcome must say "we do not know".
//
// The device may have rebooted successfully and simply lost the ACK. Telling
// the operator "the reboot failed" would be wrong, and they might drive to site.
func TestUnackedIsNotReportedAsFailure(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)

	p, err := m.deviceOps.Begin("n1", frame.DeviceOpReboot, time.Now(), time.Millisecond)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	expired := m.ExpireDeviceOps(time.Now().Add(time.Second))
	if len(expired) != 1 {
		t.Fatalf("expired %d, want 1", len(expired))
	}
	select {
	case out := <-p.Done():
		if out.Acked {
			t.Fatal("an expiry was reported as an ACK")
		}
		if out.Err == nil {
			t.Fatal("an expiry carried no error; the caller cannot distinguish it")
		}
	case <-time.After(time.Second):
		t.Fatal("Expire did not resolve the waiter")
	}
}

// TestMalformedAckIsDroppedNotFatal -- a hostile/buggy device must not be able
// to crash the server or resolve a pending request.
//
// ⚠ My first version only asserted "the request is still pending", and MUTATION
// S4 (decode failure ignored, empty ack passed to Complete) did NOT fail it --
// because an empty ack fails the request-id check anyway, so pending stayed
// true for the WRONG reason. The assertion was riding a side effect instead of
// the behaviour.
//
// The behaviour is "decode failure is DROPPED and COUNTED", so that is what is
// asserted now: the counter must rise by exactly one per bad payload.
func TestMalformedAckIsDroppedNotFatal(t *testing.T) {
	m := newOpManager(&fakeOpPublisher{})
	m.deviceOps.Begin("n1", frame.DeviceOpReboot, time.Now(), time.Hour)

	before := counterValue(t, metrics.DeviceOpBadAckTotal)

	// Empty, wrong type byte, and truncated payloads.
	m.handleDeviceOpAck("n1", nil)
	m.handleDeviceOpAck("n1", []byte{frame.MsgHello})
	m.handleDeviceOpAck("n1", []byte{frame.MsgDeviceOpAck})

	after := counterValue(t, metrics.DeviceOpBadAckTotal)
	if after != before+3 {
		t.Fatalf("bad-ACK counter %v -> %v, want +3: a malformed 0x23 must be "+
			"DROPPED AND COUNTED, not silently ignored", before, after)
	}
	if !m.PendingDeviceOp("n1") {
		t.Fatal("a malformed ACK resolved a pending request")
	}
}

// TestUnackedThroughSendDeviceOpIsNotAnAck closes the gap mutation S3 found.
//
// S3 forced out.Acked = true on the path WHERE SendDeviceOp itself observes the
// timeout, and nothing failed: my only unacked test called ExpireDeviceOps
// directly, so the mutated line was never executed. Coverage, not equivalence.
//
// This drives the timeout THROUGH SendDeviceOp: the goroutine blocks in the
// real wait, and expiry is what releases it.
func TestUnackedThroughSendDeviceOpIsNotAnAck(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)

	type result struct {
		out DeviceOpOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, time.Millisecond)
		done <- result{out, err}
	}()

	// Wait for the publish, then let the deadline pass and expire it.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pub.mu.Lock()
		published := len(pub.nodeIDs) > 0
		pub.mu.Unlock()
		if published {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	m.ExpireDeviceOps(time.Now().Add(time.Second))

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("SendDeviceOp returned a local error: %v", r.err)
		}
		if r.out.Acked {
			t.Fatal("SendDeviceOp reported an ACK for a request that was never " +
				"acknowledged -- the operator would be told the reboot succeeded " +
				"on the strength of an answer that never arrived")
		}
		if r.out.Err == nil {
			t.Fatal("an unacked outcome carried no error, so the caller cannot " +
				"tell it apart from an ACKed one")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendDeviceOp never returned after the request expired")
	}
}

// TestAckWithoutRequestIdIsRejected -- the id is what makes correlation work.
func TestAckWithoutRequestIdIsRejected(t *testing.T) {
	m := newOpManager(&fakeOpPublisher{})
	m.deviceOps.Begin("n1", frame.DeviceOpReboot, time.Now(), time.Hour)

	enc := frame.NewEncoder(frame.MsgDeviceOpAck)
	enc.EncodeVarint(1, uint64(frame.DeviceOpOK)) // result only, no request_id
	m.handleDeviceOpAck("n1", enc.Bytes())

	if !m.PendingDeviceOp("n1") {
		t.Fatal("a 0x23 with no request_id resolved a pending request; correlation " +
			"would be meaningless")
	}
}

// TestDeviceOpLabelIsBounded -- an unknown op must not create a new metric series.
func TestDeviceOpLabelIsBounded(t *testing.T) {
	if got := deviceOpLabel(frame.DeviceOpReboot); got != "reboot" {
		t.Errorf("reboot label = %q", got)
	}
	if got := deviceOpLabel(frame.DeviceOpFactoryResetKeepConn); got != "factory_reset" {
		t.Errorf("factory label = %q", got)
	}
	if got := deviceOpLabel(frame.DeviceOp(99)); got != "unknown" {
		t.Errorf("unknown label = %q, want a bounded value", got)
	}
}

// TestUnsupportedManagerRefuses -- a manager without a tracker must say so,
// rather than accept a request it cannot deliver.
func TestUnsupportedManagerRefuses(t *testing.T) {
	m := &Manager{}
	if m.DeviceOpSupported() {
		t.Fatal("a manager with no tracker claims to support device operations")
	}
	if _, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, time.Second); err == nil {
		t.Fatal("an unsupported manager accepted a device operation")
	}
}

// TestFactoryResetUsesItsOwnOp -- the two operations must not be interchangeable.
func TestFactoryResetUsesItsOwnOp(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)
	var seen frame.DeviceOp
	pub.onPublish = func(payload []byte) {
		req, _ := frame.DecodeDeviceOp(payload)
		seen = req.Op
		go func() {
			time.Sleep(10 * time.Millisecond)
			m.handleDeviceOpAck("n1", mustAck(t, req.RequestID, frame.DeviceOpOK))
		}()
	}
	if _, err := m.SendDeviceOp("n1", frame.DeviceOpFactoryResetKeepConn, 2*time.Second); err != nil {
		t.Fatalf("SendDeviceOp: %v", err)
	}
	if seen != frame.DeviceOpFactoryResetKeepConn {
		t.Fatalf("wire op = %d, want factory reset (2) -- the operator asked to wipe "+
			"the device and a reboot would silently do nothing", seen)
	}
}

// TestSendDeviceOpHonoursItsOwnTimeout -- THE deadline must be enforced by the
// request path itself, not by someone else remembering to call Expire.
//
// I wrote SendDeviceOp as "out := <-p.Done()" and left ExpireDeviceOps to be
// called by "a ticker in production" -- but no such ticker was ever wired. A
// device that never ACKs (crashed, or 0x22 unsupported) therefore blocked the
// caller FOREVER, and through an HTTP handler that is one leaked goroutine per
// click. This test is written BEFORE the fix: it must fail (hang) on the old
// code.
func TestSendDeviceOpHonoursItsOwnTimeout(t *testing.T) {
	pub := &fakeOpPublisher{}
	m := newOpManager(pub)

	start := time.Now()
	out, err := m.SendDeviceOp("n1", frame.DeviceOpReboot, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a timeout should be reported through the outcome, not as a local "+
			"error the caller cannot inspect: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("SendDeviceOp blocked for %s with a 100ms timeout; the deadline "+
			"must be enforced by the request path", elapsed)
	}
	if out.Acked {
		t.Fatal("a request nobody acknowledged was reported as acknowledged")
	}
	if out.Err == nil {
		t.Fatal("the timeout outcome carried no error")
	}
	if m.PendingDeviceOp("n1") {
		t.Fatal("after a timeout the node's single-flight slot is still held; the " +
			"operator could never retry the reboot")
	}
}
