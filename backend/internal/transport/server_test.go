package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/pkg/protoframe"
)

// server_test.go -- 真实的 mTLS 往返，不 mock TLS。
//
// 为什么不用假的 conn：本轮要证明的正是"证书里的身份确实变成了 nodeID"、
// "半包确实被正确拼起来"、"坏帧确实断开连接"。这些行为全都在真 TLS
// 与真 socket 的交互里，用一个假 conn 会把它们正好绕过去。

// --- 测试用 PKI ---

type testPKI struct {
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPEM   []byte
	srvCert tls.Certificate
	pool    *x509.CertPool
}

func newPKI(t *testing.T) *testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	issue := func(cn string, isServer bool, dns []string) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		eku := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if isServer {
			eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  eku,
			DNSNames:     dns,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("issue %s: %v", cn, err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	return &testPKI{
		caCert:  caCert,
		caKey:   caKey,
		pool:    pool,
		srvCert: issue("ehome-server", true, []string{"localhost"}),
	}
}

// startServer brings up a real listener on an ephemeral port.
func startServer(t *testing.T, pki *testPKI, cfg Config) (*Server, string, *frameSink) {
	t.Helper()
	sink := &frameSink{}
	cfg.Addr = "127.0.0.1:0"
	cfg.Cert = pki.srvCert
	cfg.ClientCAs = pki.pool
	if cfg.OnFrame == nil {
		cfg.OnFrame = sink.onFrame
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = srv.Close() })
	go func() { _ = srv.Serve(ctx) }()
	return srv, srv.Addr().String(), sink
}

type frameSink struct {
	mu     sync.Mutex
	nodes  []string
	frames [][]byte
}

func (s *frameSink) onFrame(nodeID string, h protoframe.Header, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = append(s.nodes, nodeID)
	cp := make([]byte, len(payload))
	copy(cp, payload)
	s.frames = append(s.frames, cp)
	return nil
}

func (s *frameSink) snapshot() ([]string, [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := append([]string(nil), s.nodes...)
	frames := make([][]byte, len(s.frames))
	for i := range s.frames {
		frames[i] = append([]byte(nil), s.frames[i]...)
	}
	return nodes, frames
}

func dialDevice(t *testing.T, pki *testPKI, addr, clientCN string) *tls.Conn {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: clientCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, pki.caCert, &key.PublicKey, pki.caKey)
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		RootCAs:      pki.pool,
		ServerName:   "localhost",
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestNodeIDComesFromClientCertificate -- the node identity MUST be the
// verified certificate subject, never a value the peer asserts in a frame.
// If it came from the peer, any certificate holder could impersonate any node.
func TestNodeIDComesFromClientCertificate(t *testing.T) {
	pki := newPKI(t)
	_, addr, sink := startServer(t, pki, Config{})

	conn := dialDevice(t, pki, addr, "node-1001")
	frame := buildFrame(t, 0x03, 1, []byte{0xAA})
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	nodes, frames := waitFrames(t, sink, 1)
	if nodes[0] != "node-1001" {
		t.Fatalf("nodeID = %q, want %q (must come from the client cert CN)",
			nodes[0], "node-1001")
	}
	if len(frames[0]) != 1 || frames[0][0] != 0xAA {
		t.Fatalf("payload = %x, want aa", frames[0])
	}
}

// TestHandshakeWithUntrustedClientCertFails -- mTLS must actually verify.
// A server that accepts any client is not mTLS, and that failure would be
// invisible until someone noticed unknown nodes in the DB.
func TestHandshakeWithUntrustedClientCertFails(t *testing.T) {
	pki := newPKI(t)
	_, addr, _ := startServer(t, pki, Config{})

	// A second, unrelated CA -- this client is not signed by pki.caCert.
	other := newPKI(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "impostor"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, other.caCert, &key.PublicKey, other.caKey)
	if err != nil {
		t.Fatalf("impostor cert: %v", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		RootCAs:      pki.pool,
		ServerName:   "localhost",
	})
	if err == nil {
		// The handshake may appear to succeed locally and fail on first I/O,
		// so force a round trip before concluding.
		_, werr := conn.Write(buildFrame(t, 0x03, 1, []byte{1}))
		if werr == nil {
			buf := make([]byte, 1)
			_, rerr := conn.Read(buf)
			werr = rerr
		}
		_ = conn.Close()
		if werr == nil {
			t.Fatal("an untrusted client certificate was ACCEPTED -- this is not mTLS")
		}
	}
}

// TestPartialWritesAreReassembled -- a TCP read boundary is not a frame
// boundary. Writing one frame in several Write calls (with delays) must
// still yield exactly one frame, not several garbage ones.
//
// This mirrors firmware defect D-09 (one recv treated as one message).
func TestPartialWritesAreReassembled(t *testing.T) {
	pki := newPKI(t)
	_, addr, sink := startServer(t, pki, Config{})
	conn := dialDevice(t, pki, addr, "node-split")

	payload := []byte("hello-delimiter")
	frame := buildFrame(t, 0x03, 7, payload)

	// Split at deliberately awkward offsets: inside the header, and inside
	// the payload.
	for _, cut := range []int{1, 5, 12, len(frame) - 1} {
		if cut <= 0 || cut >= len(frame) {
			continue
		}
		if _, err := conn.Write(frame[:cut]); err != nil {
			t.Fatalf("write head: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := conn.Write(frame[cut:]); err != nil {
			t.Fatalf("write tail: %v", err)
		}
		nodes, frames := waitFrames(t, sink, 1)
		if len(frames) != 1 {
			t.Fatalf("cut=%d: got %d frames, want exactly 1", cut, len(frames))
		}
		if string(frames[0]) != string(payload) {
			t.Fatalf("cut=%d: payload %q != %q", cut, frames[0], payload)
		}
		if nodes[0] != "node-split" {
			t.Fatalf("cut=%d: nodeID %q", cut, nodes[0])
		}
		// reset for the next cut
		sink.mu.Lock()
		sink.frames = nil
		sink.nodes = nil
		sink.mu.Unlock()
	}
}

// TestTwoFramesInOneWrite -- the opposite direction: two frames arriving in a
// single Read must produce two frames, not one merged blob.
func TestTwoFramesInOneWrite(t *testing.T) {
	pki := newPKI(t)
	_, addr, sink := startServer(t, pki, Config{})
	conn := dialDevice(t, pki, addr, "node-two")

	a := buildFrame(t, 0x03, 1, []byte("first"))
	b := buildFrame(t, 0x04, 2, []byte("second"))
	if _, err := conn.Write(append(append([]byte{}, a...), b...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, frames := waitFrames(t, sink, 2)
	if string(frames[0]) != "first" || string(frames[1]) != "second" {
		t.Fatalf("got %q,%q want first,second", frames[0], frames[1])
	}
}

// TestBadMagicClosesConnection -- a desynchronised stream cannot be
// resynchronised (there is no per-frame length outside the header), so the
// server must drop the connection rather than emit garbage frames.
func TestBadMagicClosesConnection(t *testing.T) {
	pki := newPKI(t)
	_, addr, sink := startServer(t, pki, Config{})
	conn := dialDevice(t, pki, addr, "node-bad")

	bad := make([]byte, 16)
	binary.BigEndian.PutUint16(bad[0:2], 0xDEAD) // wrong magic
	if _, err := conn.Write(bad); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Server should close the connection.
	//
	// ⚠ 这里不能用"Read 返回 error"作为判据 —— 我第一版就是这么写的，
	// 结果是个**真空断言**：我设了读超时，而**连接即使保持打开，
	// 超时也会返回 error** ⇒ 该断言无论连接是否关闭都会通过。
	// （它之所以在 T5 里"看起来"有效，是因为下面那条 frames==0 断言
	//   碰巧也红了；T6 没有第二条断言，于是漏过。）
	//
	// 正确判据：**读到 EOF**（对端真的关了）而不是超时。
	assertConnClosed(t, conn, "malformed header")
	_, frames := sink.snapshot()
	if len(frames) != 0 {
		t.Fatalf("emitted %d frames from a malformed stream, want 0", len(frames))
	}
}

// TestMissingClientCAsIsRejected -- constructing a device-facing server
// without client verification must fail loudly (fail closed).
func TestMissingClientCAsIsRejected(t *testing.T) {
	pki := newPKI(t)
	_, err := New(Config{Addr: "127.0.0.1:0", Cert: pki.srvCert, OnFrame: func(string, protoframe.Header, []byte) error { return nil }})
	if err == nil {
		t.Fatal("New accepted a config with no ClientCAs -- that is not mTLS")
	}
	if !strings.Contains(err.Error(), "ClientCAs") {
		t.Fatalf("error should name ClientCAs, got %v", err)
	}
}

// TestFramesWithCRCAreVerified -- when the CRC flag is set the payload must
// match, otherwise a corrupted frame would be delivered as valid data.
func TestFramesWithCRCAreVerified(t *testing.T) {
	pki := newPKI(t)
	_, addr, sink := startServer(t, pki, Config{})
	conn := dialDevice(t, pki, addr, "node-crc")

	payload := []byte("crc-checked")
	frame := buildFrame(t, 0x03, 9, payload)
	frame = appendFrameCRC(frame, payload)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, frames := waitFrames(t, sink, 1)
	if string(frames[0]) != string(payload) {
		t.Fatalf("payload %q != %q", frames[0], payload)
	}

	// Now corrupt the CRC: the connection must be dropped.
	//
	// ⚠ 注意：上面那个**好**帧已经被合法交付了，所以不能再用
	// "sink 里存在 payload" 作为判据 —— 我第一版就是这么写的，
	// 结果把那个好帧当成了"坏帧被交付"，是自己把测试写错了。
	// 正确做法：记下**坏帧之前**的条数，只断言没有**新增**。
	_, before := sink.snapshot()

	bad := buildFrame(t, 0x03, 10, payload)
	bad = appendFrameCRC(bad, payload)
	bad[len(bad)-1] ^= 0xFF
	if _, err := conn.Write(bad); err != nil {
		t.Fatalf("write bad crc: %v", err)
	}
	assertConnClosed(t, conn, "CRC mismatch")

	_, after := sink.snapshot()
	if len(after) != len(before) {
		t.Fatalf("a frame with a mismatched CRC was delivered: %d -> %d frames",
			len(before), len(after))
	}
}

// assertConnClosed proves the peer closed the connection, distinguishing
// "closed" from "my own read deadline expired".
//
// This helper exists because the obvious assertion is wrong: with a read
// deadline set, a *timeout* also returns an error, so "Read returned error"
// passes whether or not anyone closed anything. A vacuous assertion is worse
// than no assertion -- it reports safety that was never checked.
func assertConnClosed(t *testing.T, conn *tls.Conn, what string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	for {
		n, err := conn.Read(buf)
		if err == nil && n > 0 {
			// A byte we did not expect: still open, and the server is talking.
			continue
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return // peer closed -- this is what we wanted
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("connection was still OPEN after %s: read timed out instead of EOF", what)
		}
		if errors.Is(err, net.ErrClosed) {
			return
		}
		// Any other error (e.g. connection reset) also means "not usable".
		return
	}
}

func buildFrame(t *testing.T, typ uint8, seq uint32, payload []byte) []byte {
	t.Helper()
	out := make([]byte, protoframe.HeaderSize+len(payload))
	if err := protoframe.EncodeHeader(out, protoframe.Header{
		Ver: protoframe.Version, Type: typ, Seq: seq, PayloadLen: uint16(len(payload)),
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	copy(out[protoframe.HeaderSize:], payload)
	return out
}

func appendFrameCRC(frame, payload []byte) []byte {
	// set the CRC flag then append the CRC
	frame[4] |= 0x00
	frame[5] |= 0x08
	crc := protoframe.CRC32C(payload)
	out := make([]byte, 0, len(frame)+4)
	out = append(out, frame...)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], crc)
	return append(out, b[:]...)
}

func waitFrames(t *testing.T, sink *frameSink, want int) ([]string, [][]byte) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		nodes, frames := sink.snapshot()
		if len(frames) >= want {
			return nodes, frames
		}
		time.Sleep(5 * time.Millisecond)
	}
	nodes, frames := sink.snapshot()
	t.Fatalf("timed out waiting for %d frame(s), got %d", want, len(frames))
	return nodes, frames
}

var _ = io.EOF
var _ net.Conn
