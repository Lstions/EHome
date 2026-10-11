package transport

import (
	"errors"
	"sort"
	"sync"
)

// registry.go -- node_id -> Session.
//
// This replaces MQTT topic routing. In 2.x the broker was the directory: a
// topic string identified the device and the broker handled fan-out. In 3.0 a
// node is identified by its client certificate and the server must keep its
// own directory of live sessions.
//
// # The hard part is not the map. It is replacement.
//
// A device that reboots (or whose network flaps) opens a NEW TCP connection
// while the server may not yet have noticed the OLD one is dead. That leaves
// two live sessions for one node_id, and three things can go wrong:
//
//  1. If we simply overwrite the map entry, the old session keeps its socket
//     and may still write -- two connections to one device, both "valid".
//  2. If the old session cleanup removes by node_id, it evicts the NEW
//     session, and the device becomes unreachable while its socket is healthy.
//  3. If a caller captured the old Session before replacement, it must not be
//     able to deliver anything through it.
//
// So removal is by IDENTITY (RemoveIfSame), never by node_id alone, and
// replacement explicitly closes the previous session. Case 2 is the subtle
// one -- it is the same "act on the thing I looked up, not the thing that is
// there now" mistake as firmware D-30, one layer up.

// ErrNoSession is returned when a node has no live session.
var ErrNoSession = errors.New("transport: no live session for node")

// Registry is a concurrent node_id -> *Session directory.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	registered, removed, replaced, sendsToMissing uint64
}

// NewRegistry creates an empty directory.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Register installs a session for its node, closing any previous one.
//
// Returns the session that was displaced, or nil. It is already closed.
func (r *Registry) Register(s *Session) *Session {
	if s == nil {
		return nil
	}
	r.mu.Lock()
	prev := r.sessions[s.ID()]
	r.sessions[s.ID()] = s
	r.registered++
	if prev != nil {
		r.replaced++
	}
	r.mu.Unlock()

	// Close OUTSIDE the lock: Close touches the socket, and holding the
	// registry lock through it would stall every other node.
	if prev != nil && prev != s {
		prev.Close("replaced by a newer connection from the same node")
	}
	return prev
}

// RemoveIfSame removes s only if it is still the registered session for its
// node, and reports whether it removed anything.
//
// This is the important one. A stale session finishing its cleanup must NOT
// evict the session that replaced it: doing so makes a healthy device
// unreachable, and the symptom (device online, server says no session) points
// nowhere near the cause.
func (r *Registry) RemoveIfSame(s *Session) bool {
	if s == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.sessions[s.ID()]
	if !ok || cur != s {
		return false // superseded (or already gone): leave the replacement alone
	}
	delete(r.sessions, s.ID())
	r.removed++
	return true
}

// Get returns the live session for a node.
func (r *Registry) Get(nodeID string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[nodeID]
	return s, ok
}

// Send delivers a whole frame to a node.
//
// P1: the caller gets a decision-ready result, not a bool. "No such node" and
// "node present but its write failed" are different operational problems and
// must not collapse into one "send failed".
func (r *Registry) Send(nodeID string, frame []byte) SendResult {
	r.mu.RLock()
	s, ok := r.sessions[nodeID]
	r.mu.RUnlock()
	if !ok {
		r.mu.Lock()
		r.sendsToMissing++
		r.mu.Unlock()
		return SendNotReady
	}
	return s.Send(frame)
}

// Nodes lists connected node ids, sorted (stable output for logs and tests).
func (r *Registry) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RegistryStats is a snapshot of the directory counters.
type RegistryStats struct {
	// Live is the number of nodes currently connected.
	Live int
	// Registered counts sessions installed.
	Registered uint64
	// Replaced counts sessions displaced by a newer connection from the same
	// node. NOT an error by itself (reboots are normal), but a rising rate is
	// the signal that something is flapping.
	Replaced uint64
	// Removed counts sessions removed by RemoveIfSame.
	Removed uint64
	// SendsToMissing counts sends addressed to a node with no live session.
	SendsToMissing uint64
}

// Stats returns a consistent snapshot.
func (r *Registry) Stats() RegistryStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RegistryStats{
		Live:           len(r.sessions),
		Registered:     r.registered,
		Replaced:       r.replaced,
		Removed:        r.removed,
		SendsToMissing: r.sendsToMissing,
	}
}

// CloseAll closes every session (server shutdown) and empties the directory.
func (r *Registry) CloseAll(reason string) {
	r.mu.Lock()
	all := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		all = append(all, s)
	}
	r.sessions = make(map[string]*Session)
	r.mu.Unlock()
	for _, s := range all {
		s.Close(reason)
	}
}
