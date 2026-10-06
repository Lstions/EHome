package nodemgr

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"ehome/backend/pkg/frame"
)

// device_op_tracker.go -- pairing a device-op ACK with the request it answers.
//
// # The defect this exists to prevent
//
// A device acknowledges a reboot and then REBOOTS. If the ACK is lost in
// transit, the device's retry logic may deliver it after the server has already
// moved on -- or the operator may press the button twice. Without a correlation
// id, an ACK that arrives late is indistinguishable from the current attempt,
// so the server reports "reboot succeeded" for an operation whose ACK it never
// actually received. That is a silent false success on the one operation where
// the operator's next action (walk to the device, or not) depends on the answer.
//
// # Why not just match on node id
//
// Because a node is single-flighted only by convention here, not by
// construction; the tracker enforces it per node and makes a second concurrent
// request an explicit refusal rather than a race.

// ErrDeviceOpInFlight means the node already has an operation outstanding.
//
// Exported so callers can tell "wait and retry" apart from "we could not even
// ask", which need different answers and different operator behaviour.
var ErrDeviceOpInFlight = errors.New("device operation already in flight")

// DefaultDeviceOpTimeout bounds how long the server waits for an ACK.
//
// Reboot and factory-reset both END the device's current session, so waiting
// too long is pointless: the ACK arrives before the operation (design §44.3
// requires ACK-before-restart), or it never will.
const DefaultDeviceOpTimeout = 15 * time.Second

// DeviceOpOutcome is what happened to one request.
type DeviceOpOutcome struct {
	RequestID string
	Op        frame.DeviceOp
	NodeID    string
	Result    frame.DeviceOpResult
	Detail    string
	// Acked is false when the wait timed out or the request was replaced: the
	// operation may well have HAPPENED on the device, we simply do not know.
	// Callers must not read Result unless Acked is true.
	Acked bool
	// Err is non-nil for local failures (already pending, send failed,
	// timeout). It is NOT the device's result code.
	Err error
}

type pendingDeviceOp struct {
	op        frame.DeviceOp
	requestID string
	done      chan DeviceOpOutcome
	deadline  time.Time
}

// DeviceOpTracker enforces one outstanding device op per node and pairs ACKs
// with requests.
type DeviceOpTracker struct {
	mu      sync.Mutex
	pending map[string]*pendingDeviceOp // node id -> outstanding request
	seq     uint64
}

// NewDeviceOpTracker builds an empty tracker.
func NewDeviceOpTracker() *DeviceOpTracker {
	return &DeviceOpTracker{pending: map[string]*pendingDeviceOp{}}
}

// Begin reserves the node's single slot and returns the request id to send.
//
// Refuses when the node already has an outstanding op: two concurrent reboots
// have no coherent meaning, and silently queueing them would mean the operator
// sees "accepted" twice and the second one may execute minutes later.
func (t *DeviceOpTracker) Begin(nodeID string, op frame.DeviceOp, now time.Time, timeout time.Duration) (*pendingDeviceOp, error) {
	if timeout <= 0 {
		timeout = DefaultDeviceOpTimeout
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.pending[nodeID]; ok {
		// Wrapped in a sentinel so the API layer can answer 409 Conflict
		// ("one is already in flight, wait") instead of a generic failure. The
		// operator's next action differs between those two cases.
		return nil, fmt.Errorf("%w: node %s already has a device op (%d) in flight "+
			"(request %s, %s left)", ErrDeviceOpInFlight, nodeID, uint8(existing.op),
			existing.requestID, existing.deadline.Sub(now).Round(time.Second))
	}
	t.seq++
	p := &pendingDeviceOp{
		op:        op,
		requestID: fmt.Sprintf("op-%s-%d-%d", nodeID, now.UnixNano(), t.seq),
		done:      make(chan DeviceOpOutcome, 1),
		deadline:  now.Add(timeout),
	}
	t.pending[nodeID] = p
	return p, nil
}

// RequestID returns the correlation id the device must echo back.
func (p *pendingDeviceOp) RequestID() string { return p.requestID }

func (p *pendingDeviceOp) Op() frame.DeviceOp { return p.op }

func (p *pendingDeviceOp) Done() <-chan DeviceOpOutcome { return p.done }

// Fail resolves the request with a local error and frees the slot.
func (t *DeviceOpTracker) Fail(nodeID, requestID string, err error) {
	t.mu.Lock()
	p, ok := t.pending[nodeID]
	if ok && p.requestID == requestID {
		delete(t.pending, nodeID)
	}
	t.mu.Unlock()
	if ok && p.requestID == requestID {
		p.done <- DeviceOpOutcome{
			RequestID: requestID, Op: p.op, NodeID: nodeID, Err: err,
		}
	}
}

// Complete resolves a request from a device ACK.
//
// Returns false when the ACK does not belong to the outstanding request --
// either the request id does not match, or there is no outstanding request.
// A mismatched ACK is NOT an error to report to the operator (the request it
// belongs to is already gone); it is a fact to count, which is why the caller
// increments a metric on false rather than logging a failure.
func (t *DeviceOpTracker) Complete(nodeID string, ack frame.DeviceOpAck) bool {
	t.mu.Lock()
	p, ok := t.pending[nodeID]
	if !ok || p.requestID != ack.RequestID {
		t.mu.Unlock()
		return false
	}
	delete(t.pending, nodeID)
	t.mu.Unlock()
	p.done <- DeviceOpOutcome{
		RequestID: ack.RequestID, Op: p.op, NodeID: nodeID,
		Result: ack.Result, Detail: ack.Detail, Acked: true,
	}
	return true
}

// Expire resolves and frees any request whose deadline has passed.
//
// Returns the expired outcomes so the caller can report them. Expiry is
// reported as Acked=false with Err set, because "we never heard back" is a
// genuinely different fact from "the device said it failed": for a reboot the
// device may well be up and running the new firmware.
func (t *DeviceOpTracker) Expire(now time.Time) []DeviceOpOutcome {
	t.mu.Lock()
	var expired []DeviceOpOutcome
	type waiter struct {
		ch  chan DeviceOpOutcome
		out DeviceOpOutcome
	}
	var waiters []waiter
	for nodeID, p := range t.pending {
		if now.Before(p.deadline) {
			continue
		}
		delete(t.pending, nodeID)
		out := DeviceOpOutcome{
			RequestID: p.requestID, Op: p.op, NodeID: nodeID,
			Err: fmt.Errorf("device did not acknowledge within %s",
				p.deadline.Sub(now).Round(time.Second)),
		}
		expired = append(expired, out)
		waiters = append(waiters, waiter{ch: p.done, out: out})
	}
	t.mu.Unlock()

	// Resolve the waiters AFTER releasing the lock.
	//
	// My first version deleted the entry and returned without writing to
	// p.done, which left whoever was blocked on Done() hanging FOREVER: a
	// goroutine leak on the operator's request path, and the operation would
	// never be reported at all. The channel is buffered (cap 1) so this cannot
	// block here.
	for _, w := range waiters {
		w.ch <- w.out
	}
	return expired
}

// Pending reports how many nodes have an outstanding request.
func (t *DeviceOpTracker) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// PendingFor reports whether a specific node has an outstanding request.
func (t *DeviceOpTracker) PendingFor(nodeID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.pending[nodeID]
	return ok
}
