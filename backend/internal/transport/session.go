package transport

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/pkg/protoframe"
)

// session.go -- per-node session: the server half of one device connection.
//
// Design 2.5.3 defines the interface, deliberately symmetric with the
// firmware link_result_t so the same defect classes cannot reappear on only
// one side.
//
// Size contract: Send takes a WHOLE frame (header+payload+CRC), and MTU is the
// maximum WHOLE-frame size (16384 = MBEDTLS_SSL_IN_CONTENT_LEN), matching
// firmware LINK_TCP_MTU_BYTES (link_tcp.h:42) and firmware link_send, which
// compares the whole frame length against drv->mtu(). Payload max is therefore
// MTU - header(12) - CRC(4) = 16368 = protoframe.PayloadMax.

// SendResult mirrors design 2.5.3.
//
// Note on the deliberate asymmetry with firmware link_result_t: the firmware
// has SIX values (it distinguishes SENT_FULL from SENT_PARTIAL, per defect
// D-30), while the design specifies FIVE here. That is NOT an oversight to be
// "fixed" by adding a sixth: the io.Writer contract says a Write returning
// n < len(p) MUST also return a non-nil error, so a partial write is never
// reported as success. The asymmetry is justified -- but only because this
// file honours that contract (see writeOnce).
type SendResult int

const (
	SendOK         SendResult = iota
	SendNotReady              // not connected / not handshaken
	// ⚠ SendBackpressure is currently a STUB: nothing in this file returns it.
	//
	// It exists because design §2.5.3 specifies it, and because the send window
	// that would make it reachable is **itself undefined in the design**: §4.1
	// says "backpressure -> application-layer send window (see 5.2)", but §5.2
	// is titled "maximum size and memory bounds" and contains no window number.
	//
	// So the honest state is: the enum value is specified, the mechanism is not.
	// A caller writing "case SendBackpressure: retry later" is writing a branch
	// that cannot be taken yet. I am deliberately NOT inventing a window size
	// (this repo has three prior incidents of a skeleton inventing a contract:
	// msgcodec's tag format, wire's frame format, D-30).
	//
	// Until the window lands, a slow peer is handled by the write deadline:
	// a timeout resumes the frame, and a hard failure is SendFatal.
	SendBackpressure  // P3: peer slow; retry later, NOT an error
	SendPayloadTooBig // P2: exceeds negotiated MTU
	SendFatal
)

// SendBackpressureIsStub documents, in code, that the above value is currently
// unreachable. Tests assert this so the state cannot drift silently: if someone
// later makes it reachable, that test fails and forces the doc comment above to
// be updated in the same change.
const SendBackpressureIsStub = true

func (r SendResult) String() string {
	switch r {
	case SendOK:
		return "ok"
	case SendNotReady:
		return "not_ready"
	case SendBackpressure:
		return "backpressure"
	case SendPayloadTooBig:
		return "payload_too_big"
	case SendFatal:
		return "fatal"
	default:
		return fmt.Sprintf("unrecognized(%d)", int(r))
	}
}

// SessionStats is a consistent snapshot of one session counters.
type SessionStats struct {
	ID              string
	FramesSent      uint64
	BytesSent       uint64
	SendErrors      uint64
	Backpressure    uint64
	PayloadTooBig   uint64
	PartialsResumed uint64
	Writes          uint64
	Connected       bool
	Remote          string
	CloseReason     string
}

// Session owns one device connection write side.
type Session struct {
	id     string
	conn   net.Conn
	remote string

	// writeMu serialises whole frames. WITHOUT this, two goroutines sending to
	// the same device interleave their bytes and corrupt the stream -- and TCP
	// will faithfully deliver the corruption. The receiver cannot even detect
	// it: each frame length field says how long that frame is, and the bytes
	// simply do not match.
	writeMu sync.Mutex

	writeTimeout time.Duration
	closed       atomic.Bool
	closeReason  atomic.Value // string

	framesSent      atomic.Uint64
	bytesSent       atomic.Uint64
	sendErrors      atomic.Uint64
	backpressure    atomic.Uint64
	payloadTooBig   atomic.Uint64
	partialsResumed atomic.Uint64
	writes          atomic.Uint64
}

// NewSession wraps a connection for one node.
func NewSession(id string, conn net.Conn, writeTimeout time.Duration) *Session {
	return &Session{
		id:           id,
		conn:         conn,
		remote:       conn.RemoteAddr().String(),
		writeTimeout: writeTimeout,
	}
}

func (s *Session) ID() string { return s.id }

// Remote is the peer address, for logs.
func (s *Session) Remote() string { return s.remote }

// MTU returns the maximum whole-frame size. 16384 is the TLS record size, and
// it matches firmware LINK_TCP_MTU_BYTES so both ends refuse the same frames.
func (s *Session) MTU() uint32 { return protoframe.MaxFrameBytes }

// Stats returns a consistent snapshot.
func (s *Session) Stats() SessionStats {
	reason, _ := s.closeReason.Load().(string)
	return SessionStats{
		ID:              s.id,
		FramesSent:      s.framesSent.Load(),
		BytesSent:       s.bytesSent.Load(),
		SendErrors:      s.sendErrors.Load(),
		Backpressure:    s.backpressure.Load(),
		PayloadTooBig:   s.payloadTooBig.Load(),
		PartialsResumed: s.partialsResumed.Load(),
		Writes:          s.writes.Load(),
		Connected:       !s.closed.Load(),
		Remote:          s.remote,
		CloseReason:     reason,
	}
}

// Close marks the session closed and drops the connection.
//
// Keeps the FIRST reason: the first cause is the informative one, and a later
// Close (e.g. from cleanup) would otherwise overwrite it with something trivial.
func (s *Session) Close(reason string) {
	if s.closed.Swap(true) {
		return
	}
	s.closeReason.Store(reason)
	_ = s.conn.Close()
}

// Send writes one whole frame (header+payload[+CRC]).
//
// P1: this does NOT do a "check then send". It reports what the CALLER must
// decide: not ready / too big / backpressure / fatal. Callers must branch.
func (s *Session) Send(frame []byte) SendResult {
	if s.closed.Load() {
		return SendNotReady
	}
	// P2: verify the end-to-end contract BEFORE writing anything. Checking
	// after a partial write would leave half a frame on the wire.
	if len(frame) == 0 || len(frame) > int(s.MTU()) {
		s.payloadTooBig.Add(1)
		return SendPayloadTooBig
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.closed.Load() {
		return SendNotReady
	}

	written := 0
	for written < len(frame) {
		n, err := s.writeOnce(frame[written:])
		if n > 0 {
			written += n
		}
		s.writes.Add(1)

		if err == nil {
			continue
		}

		// A write deadline expiry is NOT a dead connection. net.Conn docs for
		// SetWriteDeadline say explicitly:
		//
		//   "Even if write times out, it may return n > 0, indicating that
		//    some of the data was successfully written."
		//
		// So a timeout means "this attempt made partial progress"; the socket
		// is still usable and we must RESUME from the bytes already accepted,
		// never resend the whole frame. Resending duplicates the first n bytes
		// on the wire -- exactly firmware defect D-30 (partial write treated as
		// "retry the whole frame"), which corrupts the TCP stream while every
		// counter still looks healthy.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			if written > 0 {
				s.partialsResumed.Add(1)
			}
			continue
		}

		// Any other error: the connection is unusable for further sends.
		//
		// If part of the frame already went out, the peer holds a truncated
		// frame it can never complete (the rest will not arrive), so the stream
		// is desynchronised. We must NOT report "backpressure" -- that would
		// invite a caller retry that appends a SECOND copy of the frame after
		// the truncated prefix.
		s.sendErrors.Add(1)
		if written > 0 && written < len(frame) {
			s.Close(fmt.Sprintf("write failed mid-frame after %d/%d bytes: %v",
				written, len(frame), err))
		} else {
			s.Close("write failed: " + err.Error())
		}
		return SendFatal
	}

	s.framesSent.Add(1)
	s.bytesSent.Add(uint64(written))
	return SendOK
}

// writeOnce performs a single Write with a deadline applied.
//
// It never hides a partial write: n and err are returned exactly as the
// connection reported them, so the caller can distinguish "resumable
// progress" from "dead connection".
func (s *Session) writeOnce(p []byte) (int, error) {
	if s.writeTimeout > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}
	n, err := s.conn.Write(p)
	// A Write that reports progress with a nil error while writing less than
	// asked would violate the io.Writer contract, leaving us unable to tell
	// "done" from "stalled". Surface it rather than looping forever on a peer
	// that never accepts more.
	if err == nil && n < len(p) {
		return n, errors.New("transport: short write with nil error (violates io.Writer)")
	}
	return n, err
}
