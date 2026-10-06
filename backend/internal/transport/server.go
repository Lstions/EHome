// Package transport implements the 3.0 device-facing TCP+TLS server.
//
// # What this replaces
//
// 3.0 drops MQTT: the device is a TCP **client** dialling out to
// EHOME_DEVICE_PORT (default 8443) with mTLS, and one long-lived connection
// carries both directions (ESP32-3.0-重构方案 §4.1). Consequently:
//
//   - node identity stops being the MQTT topic and becomes the **client
//     certificate CN/SAN**;
//   - the broker's per-message durability disappears, so application-level
//     ACK takes its place;
//   - topic routing becomes a node_id -> session registry.
//
// # Layout (main design §5.4)
//
//	server.go    ListenAndServeTLS with mTLS
//	session.go   per-node session: seq / ack / heartbeat / send window
//	framing.go   12-byte fixed header (same golden vector as the firmware)
//	registry.go  node_id -> session
//
// # Status: skeleton, deliberately incremental
//
// This file currently implements the **listener and per-connection read
// loop** only. Sending, ACK/heartbeat state and the registry follow; they are
// separate files precisely so each lands as its own reviewable step.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"sync"
	"time"

	"ehome/backend/pkg/protoframe"
)

// Config configures the device-facing listener.
type Config struct {
	// Addr is the listen address, e.g. ":8443".
	Addr string

	// Cert is the server certificate chain.
	Cert tls.Certificate

	// ClientCAs verifies device certificates. Required: without it we would
	// accept any client, which is the opposite of mTLS.
	ClientCAs *x509.CertPool

	// ReadTimeout is applied per read. 0 means no deadline.
	ReadTimeout time.Duration

	// WriteTimeout is applied per write. 0 means no deadline.
	WriteTimeout time.Duration

	// MaxPayload caps a single frame's payload. 0 means protoframe.PayloadMax.
	// Lowering it is a deliberate protection against a peer that announces a
	// huge payload: protoframe.DecodeHeader already rejects anything over the
	// TLS-record-derived maximum, but a per-deployment cap can be tighter.
	MaxPayload uint16

	// HandshakeTimeout bounds the TLS handshake. Without it, a peer that
	// connects and then stalls would hold a slot forever.
	HandshakeTimeout time.Duration

	Logger *slog.Logger

	// OnFrame is called for each complete frame whose payload has been read.
	// Returning an error closes the connection.
	OnFrame func(nodeID string, h protoframe.Header, payload []byte) error

	// OnConnect / OnDisconnect are optional lifecycle hooks.
	OnConnect    func(nodeID string, remote string)
	OnDisconnect func(nodeID string, err error)
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.MaxPayload == 0 {
		out.MaxPayload = protoframe.PayloadMax
	}
	if out.HandshakeTimeout == 0 {
		out.HandshakeTimeout = 10 * time.Second
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// Stats counts what the server rejected, per design §5.3.
//
// The design requires every rejection path to be **counted**. Without counters
// a resynchronisation looks identical to normal operation: bytes are skipped,
// no frame arrives, and nothing says why. That is the same "silently dropped"
// shape as principle P3 (no silent drops).
type Stats struct {
	// BadHeader counts frames refused for bad magic/version. The stream is
	// resynchronised (bytes dropped) rather than the connection closed.
	BadHeader atomic.Uint64

	// ResyncBytes counts bytes dropped while searching for the next magic.
	// Kept separate from BadHeader because one bad header can require many
	// byte drops, and the ratio is what tells you whether it is a stray bit
	// or a peer speaking a different protocol.
	ResyncBytes atomic.Uint64

	// TooLarge counts frames whose declared payload exceeded the cap. These
	// are refused at header-parse time so nothing is buffered for them.
	TooLarge atomic.Uint64

	// BadCRC counts frames whose CRC32C did not match.
	BadCRC atomic.Uint64

	// Overflow counts connections dropped because the buffer grew past one
	// maximum frame without ever completing one.
	Overflow atomic.Uint64

	// Frames counts successfully delivered frames.
	Frames atomic.Uint64
}

// Server accepts device connections.
type Server struct {
	cfg      Config
	stats    Stats
	registry *Registry
	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	closing  bool
	conns    map[net.Conn]struct{}
}

// Registry exposes the live-session directory so callers can send downlinks
// and list connected nodes.
//
// The transport owns it (rather than the caller passing one in) because a
// session is created inside handleConn the moment a device authenticates; if
// the caller also owned the directory there would be a window in which a
// connected node is reachable by the transport but invisible to everyone else.
func (s *Server) Registry() *Registry { return s.registry }

// Send delivers a whole frame to a connected node.
//
// P1: the result tells the caller what to DECIDE. "No such node" and "node
// present but its write failed" are different problems and are not collapsed.
func (s *Server) Send(nodeID string, frame []byte) SendResult {
	return s.registry.Send(nodeID, frame)
}

// New creates a server. It does not listen yet.
func New(cfg Config) (*Server, error) {
	// Fail closed: a device-facing server without client verification is not
	// mTLS at all, and silently accepting it would defeat the entire point of
	// replacing MQTT username/password with certificates.
	if cfg.ClientCAs == nil {
		return nil, errors.New("transport: ClientCAs is required (mTLS without client verification is not mTLS)")
	}
	if len(cfg.Cert.Certificate) == 0 {
		return nil, errors.New("transport: server certificate is required")
	}
	if cfg.Addr == "" {
		return nil, errors.New("transport: Addr is required")
	}
	if cfg.OnFrame == nil {
		return nil, errors.New("transport: OnFrame is required")
	}
	return &Server{
		cfg:      cfg.withDefaults(),
		registry: NewRegistry(),
		conns:    map[net.Conn]struct{}{},
	}, nil
}

// tlsConfig builds the mTLS configuration.
//
// ClientAuth is RequireAndVerifyClientCert: the client certificate is both
// required and verified against ClientCAs, so an unauthenticated peer cannot
// even complete the handshake.
func (s *Server) tlsConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{s.cfg.Cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    s.cfg.ClientCAs,
		MinVersion:   tls.VersionTLS12,
	}
}

// Listen opens the socket. Split from Serve so tests can learn the actual
// address (port 0) before traffic starts.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("transport: listen %s: %w", s.cfg.Addr, err)
	}
	s.ln = ln
	return nil
}

// StatsSnapshot returns the counters as plain numbers.
//
// Returns a snapshot rather than the live struct so callers cannot mutate
// counters, and so the numbers come from one consistent moment.
func (s *Server) StatsSnapshot() (frames, badHeader, resyncBytes, tooLarge, badCRC, overflow uint64) {
	return s.stats.Frames.Load(), s.stats.BadHeader.Load(),
		s.stats.ResyncBytes.Load(), s.stats.TooLarge.Load(),
		s.stats.BadCRC.Load(), s.stats.Overflow.Load()
}

// Addr reports the bound address (useful with ":0").
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("transport: Serve called before Listen")
	}
	tlsLn := tls.NewListener(s.ln, s.tlsConfig())

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := tlsLn.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing || ctx.Err() != nil {
				return nil
			}
			// A single failed accept (e.g. a client that broke off during the
			// handshake) must not kill the listener. Log and continue.
			s.cfg.Logger.Warn("transport: accept failed", "err", err)
			continue
		}
		s.wg.Add(1)
		go s.handleConn(ctx, conn)
	}
}

// Close stops accepting and drops existing connections.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closing = true
	ln := s.ln
	var err error
	if ln != nil {
		err = ln.Close()
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

// nodeIDFromCert derives the node identity from the client certificate.
//
// Design §4.1: "session identity = client cert CN/SAN = node_id". We therefore
// trust ONLY the verified certificate -- never a value sent in a Hello frame,
// which would let any certificate holder impersonate any node.
//
// SAN email is accepted as a fallback because the CN field is deprecated for
// this purpose and many PKI setups now only populate SAN. The first non-empty
// of (CN, SAN DNS, SAN email) wins; we do NOT concatenate, because a compound
// identity would have to match on both sides and neither end knows the rule.
func nodeIDFromCert(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", errors.New("transport: no client certificate")
	}
	if cn := strings.TrimSpace(cert.Subject.CommonName); cn != "" {
		return cn, nil
	}
	if len(cert.DNSNames) > 0 {
		if d := strings.TrimSpace(cert.DNSNames[0]); d != "" {
			return d, nil
		}
	}
	if len(cert.EmailAddresses) > 0 {
		if e := strings.TrimSpace(cert.EmailAddresses[0]); e != "" {
			return e, nil
		}
	}
	return "", errors.New("transport: client certificate has no CN/SAN to use as node_id")
}

// handleConn runs one device connection.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()

	// ⚠ nodeID is declared here, BEFORE the deferred cleanup, so the closure
	// below sees the final value.
	//
	// It used to be declared later (with :=) and the deferred call passed "".
	// That is a real defect, not cosmetic: every disconnect reported an empty
	// node id, so any cleanup keyed on the node — which is exactly what the
	// registry needs (RemoveIfSame) — could never work, and the log line
	// "device disconnected" named nobody.
	var nodeID string
	var closeErr error
	var session *Session

	defer func() {
		// Deregister BEFORE closing the connection: a stale session finishing
		// cleanup must not evict the session that replaced it, which is why
		// removal is by identity rather than by node id.
		if session != nil && s.registry != nil {
			s.registry.RemoveIfSame(session)
		}
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		if session != nil {
			session.Close("connection closed")
		}
		_ = conn.Close()
		if s.cfg.OnDisconnect != nil {
			s.cfg.OnDisconnect(nodeID, closeErr)
		}
	}()

	if s.cfg.HandshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		closeErr = errors.New("transport: accepted connection is not TLS")
		return
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		closeErr = fmt.Errorf("transport: TLS handshake: %w", err)
		s.cfg.Logger.Warn("transport: handshake failed", "remote", conn.RemoteAddr(), "err", err)
		return
	}
	// Past the handshake we manage deadlines per-operation.
	_ = conn.SetDeadline(time.Time{})

	state := tlsConn.ConnectionState()
	var err error
	nodeID, err = nodeIDFromCert(state.PeerCertificates[0])
	if len(state.PeerCertificates) == 0 {
		nodeID, err = "", errors.New("transport: no peer certificate after verified handshake")
	}
	if err != nil {
		closeErr = err
		s.cfg.Logger.Warn("transport: no node identity", "remote", conn.RemoteAddr(), "err", err)
		return
	}

	// Publish a session so the rest of the server can reach this device.
	//
	// Registration happens BEFORE OnConnect: a hook that wants to send
	// something immediately (a config sync, say) would otherwise find no
	// session for the node that just connected.
	session = NewSession(nodeID, conn, s.cfg.WriteTimeout)
	if s.registry != nil {
		s.registry.Register(session) // closes any previous session for this node
	}
	if s.cfg.OnConnect != nil {
		s.cfg.OnConnect(nodeID, conn.RemoteAddr().String())
	}
	s.cfg.Logger.Info("transport: device connected", "node", nodeID, "remote", conn.RemoteAddr())

	closeErr = s.readLoop(conn, nodeID)
	if closeErr != nil {
		s.cfg.Logger.Info("transport: device disconnected",
			"node", nodeID, "err", closeErr)
	}
	_ = conn.Close()
}

// readLoop delimits the stream into frames and dispatches them.
func (s *Server) readLoop(conn net.Conn, nodeID string) error {
	// buf accumulates bytes until a whole frame (header + payload + optional
	// CRC) is present. A TCP read boundary means nothing: one Read may return
	// half a header, and one frame may span many Reads. Treating a Read as a
	// frame is exactly the firmware D-09 defect, mirrored on the server.
	buf := make([]byte, 0, 32*1024)
	tmp := make([]byte, 16*1024)

	for {
		if s.cfg.ReadTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if len(buf) > 0 && !errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("transport: %d trailing bytes unparsed: %w", len(buf), err)
			}
			return err
		}

		// Drain as many complete frames as the buffer holds.
		for {
			h, derr := protoframe.DecodeHeader(buf)
			if derr == protoframe.ErrShort {
				break // need more bytes -- normal on a stream
			}
			if derr == protoframe.ErrMagic || derr == protoframe.ErrVer {
				// ⚠ 设计 §5.3 规定：「头非法（magic/ver）⇒ **拒绝 + 复位重新同步**，计数」。
				//
				// 我上一轮实现的是**直接断开连接** —— 那是**静默偏离设计**。
				// 设计要的是"重新同步"而不是"踢掉设备"：这个端口上连的是
				// 现场设备，为几个坏字节断开一条长连接（还要走退避重连）
				// 代价远大于跳过它们。
				//
				// 如何重新同步：**magic 是两字节的固定标识**，
				// 所以只要向后滑动一格再找 magic 即可。这里实现为
				// "丢掉一个字节，重新尝试解析"（等价于在流里找下一个 magic）。
				// 若滑动到不足一个头就停（切回 NEED_MORE 路径）。
				s.stats.BadHeader.Add(1)
				s.stats.ResyncBytes.Add(1)
				buf = buf[1:]
				s.cfg.Logger.Warn("transport: bad frame header, resynchronising",
					"node", nodeID, "err", derr, "dropped", 1)
				continue
			}
			if derr != nil {
				return fmt.Errorf("transport: header error from %s: %w", nodeID, derr)
			}
			if h.PayloadLen > s.cfg.MaxPayload {
				s.stats.TooLarge.Add(1)
				return fmt.Errorf("transport: payload %d exceeds cap %d from %s",
					h.PayloadLen, s.cfg.MaxPayload, nodeID)
			}
			total := h.FrameBytes()
			if len(buf) < total {
				break // header present, payload still in flight
			}
			payload := buf[protoframe.HeaderSize : protoframe.HeaderSize+int(h.PayloadLen)]
			if h.HasCRC() {
				want := uint32(buf[total-protoframe.CRCSize])<<24 |
					uint32(buf[total-protoframe.CRCSize+1])<<16 |
					uint32(buf[total-protoframe.CRCSize+2])<<8 |
					uint32(buf[total-protoframe.CRCSize+3])
				if got := protoframe.CRC32C(payload); got != want {
					s.stats.BadCRC.Add(1)
					return fmt.Errorf("transport: CRC mismatch from %s (got 0x%08X want 0x%08X)",
						nodeID, got, want)
				}
			}
			s.stats.Frames.Add(1)
			if err := s.cfg.OnFrame(nodeID, h, payload); err != nil {
				return fmt.Errorf("transport: onFrame: %w", err)
			}
			// Advance. Copy the payload is NOT done: OnFrame must consume it
			// before returning (documented on the hook).
			buf = buf[total:]
		}

		// Guard against unbounded growth if a peer never completes a frame.
		if len(buf) > int(s.cfg.MaxPayload)+protoframe.HeaderSize+protoframe.CRCSize {
			return fmt.Errorf("transport: buffer overflow (%d bytes) from %s without a complete frame",
				len(buf), nodeID)
		}
	}
}
