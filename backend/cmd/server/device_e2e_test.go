package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"ehome/backend/internal/downlink"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/transport"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"
	"ehome/backend/testutil"
)

// device_e2e_test.go -- the whole 3.0 chain on loopback, for the first time.
//
// Everything before this was tested a layer at a time: the listener, the
// registry, the frame routing, the downlink bridge. This test runs them
// TOGETHER against real TLS and a real (in-memory) database, because the
// defects worth finding now live in the seams:
//
//	device --mTLS--> transport --> routing --> real handler --> DB
//	       <--downlink bridge-- session <-------- HelloAck
//
// A layer-at-a-time test cannot see a frame that is framed correctly, routed
// correctly, and answered on the WRONG transport.

// e2ePKI mints a CA, a server cert and a device cert.
type e2ePKI struct {
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caPEM    []byte
	srvCert  tls.Certificate
	devCert  tls.Certificate
	devNode  string
	pool     *x509.CertPool
}

func newE2EPKI(t *testing.T, deviceCN string) *e2ePKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2e-ca"},
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
	caPEM := pemEncodeCert(caDER)

	issue := func(cn string, server bool) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		eku := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if server {
			eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  eku,
			DNSNames:     []string{"localhost"},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("issue %s: %v", cn, err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &e2ePKI{
		caCert: caCert, caKey: caKey, caPEM: caPEM, pool: pool,
		srvCert: issue("ehome-server", true),
		devCert: issue(deviceCN, false),
		devNode: deviceCN,
	}
}

func pemEncodeCert(der []byte) []byte {
	out := []byte("-----BEGIN CERTIFICATE-----\n")
	// base64 with 64-char lines, matching PEM.
	const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var s []byte
	for i := 0; i < len(der); i += 3 {
		var n uint32
		rem := len(der) - i
		n = uint32(der[i]) << 16
		if rem > 1 {
			n |= uint32(der[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(der[i+2])
		}
		s = append(s, b64[(n>>18)&0x3F], b64[(n>>12)&0x3F])
		if rem > 1 {
			s = append(s, b64[(n>>6)&0x3F])
		} else {
			s = append(s, '=')
		}
		if rem > 2 {
			s = append(s, b64[n&0x3F])
		} else {
			s = append(s, '=')
		}
	}
	for i := 0; i < len(s); i += 64 {
		end := i + 64
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end]...)
		out = append(out, '\n')
	}
	out = append(out, []byte("-----END CERTIFICATE-----\n")...)
	return out
}

// startE2EServer brings up the real listener with the real manager behind it.
func startE2EServer(t *testing.T, pki *e2ePKI) (*transport.Server, *nodemgr.Manager, string) {
	t.Helper()
	db := testutil.OpenTestDB(t)

	// The same object serves both roles: transport populates it, the bridge
	// reads it. That is the property the startup wiring establishes, and this
	// test therefore exercises the REAL composition, not a simplified one.
	reg := transport.NewRegistry()
	legacy := &recordingLegacy{}
	bridge := downlink.New(reg, legacy)

	// A REAL websocket hub, not nil. Registering a node publishes an event, and
	// handleHello dereferences m.wsHub unconditionally -- passing nil panicked
	// inside the server goroutine, which killed the whole test process. In
	// production the hub is always present, so this is a test-setup matter, not
	// a production defect; but it does mean a nil hub cannot be assumed away.
	hub := websocket.NewHub()
	go hub.Run()
	mgr := nodemgr.NewManager(db, bridge, hub, nil, nil, nil)
	_ = legacy

	cfg := transport.Config{
		Addr:             "127.0.0.1:0",
		Cert:             pki.srvCert,
		ClientCAs:        pki.pool,
		HandshakeTimeout: 5 * time.Second,
		OnFrame:          mgr.FrameHandler(),
		// ⚠ Registry MUST be the same object the bridge holds.
		//
		// I first omitted this line, so the transport built its own private
		// registry while the bridge watched an empty one. The result: the
		// device authenticated, sent Hello, and got NOTHING back -- because
		// the bridge saw "no session" and sent HelloAck to MQTT instead.
		//
		// That is precisely the seam defect this test exists to catch, and it
		// was silent (no error, no log, MQTT "succeeded"). It is also why the
		// production wiring makes the Registry caller-supplied rather than
		// transport-owned: one object, or the two halves disagree.
		Registry: reg,
	}
	srv, err := transport.New(cfg)
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = srv.Close() })
	go func() { _ = srv.Serve(ctx) }()
	return srv, mgr, srv.Addr().String()
}

// recordingLegacy stands in for MQTT and records what would have gone there.
//
// This is how the test detects the seam defect: if HelloAck is answered over
// MQTT instead of the device's TCP session, the device never receives it AND
// this slice is non-empty.
type recordingLegacy struct {
	topics [][]byte
}

func (r *recordingLegacy) Publish(topic string, payload []byte) error {
	r.topics = append(r.topics, []byte(topic))
	return nil
}
func (r *recordingLegacy) PublishQoS2(topic string, payload []byte) error {
	r.topics = append(r.topics, []byte(topic))
	return nil
}
func (r *recordingLegacy) PublishRetained(topic string, payload []byte) error {
	r.topics = append(r.topics, []byte(topic))
	return nil
}

func dialE2EDevice(t *testing.T, pki *e2ePKI, addr string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{pki.devCert},
		RootCAs:      pki.pool,
		ServerName:   "localhost",
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// buildHelloFrame builds the 2.x-registered Hello payload plus its 3.0 header.
//
// Mirrors what the firmware sends: the payload's first byte IS the message
// type, and the 12-byte header repeats it.
func buildHelloFrame(t *testing.T, nodeID string, nonce uint32) []byte {
	t.Helper()
	// Field numbers come from parseHello (handler_hello.go). I first wrote this
	// from memory and got it wrong -- field 3 is "model" (a string), not
	// channel_count -- so the server rejected every Hello while this test only
	// saw "nothing came back". The per-line comment IS the contract.
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, nodeID)        // node_id (required, must match cert CN)
	enc.EncodeString(2, "3.0.0")       // firmware_version (required, non-empty)
	enc.EncodeString(3, "e2e-model")   // model (required, non-empty)
	enc.EncodeVarint(4, 1)             // channel_count (required)
	enc.EncodeVarint(5, 0)             // config_epoch (required)
	enc.EncodeVarint(6, 0)             // nvs_has_config (required; 0 = false)
	enc.EncodeString(7, "")            // last_manifest (optional)
	enc.EncodeString(8, "3.0")         // protocol_version (required; in [2.6, 3.0])
	enc.EncodeVarint(9, uint64(nonce)) // handshake_nonce (required, non-zero)
	payload := enc.Bytes()

	out := make([]byte, protoframe.HeaderSize+len(payload))
	h := protoframe.Header{
		Ver: protoframe.Version, Type: frame.MsgHello,
		PayloadLen: uint16(len(payload)),
	}
	if err := protoframe.EncodeHeader(out, h); err != nil {
		t.Fatalf("encode header: %v", err)
	}
	copy(out[protoframe.HeaderSize:], payload)
	return out
}

func readOneFrame(t *testing.T, conn *tls.Conn, timeout time.Duration) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 256)
	for {
		if len(buf) >= protoframe.HeaderSize {
			h, err := protoframe.DecodeHeader(buf)
			if err == nil && len(buf) >= h.FrameBytes() {
				return buf[:h.FrameBytes()]
			}
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			continue
		}
		if err != nil {
			return nil
		}
	}
}

// TestEndToEndHelloGetsHelloAckOverTCP is the first full-chain proof.
//
// A device connects with mTLS, sends Hello over TCP, the server registers it
// and answers HelloAck -- and the answer must arrive on the DEVICE'S OWN
// SOCKET, not on MQTT.
func TestEndToEndHelloGetsHelloAckOverTCP(t *testing.T) {
	pki := newE2EPKI(t, "e2e-node-1")
	srv, _, addr := startE2EServer(t, pki)
	conn := dialE2EDevice(t, pki, addr)

	// Wait for the server to publish the session (registration happens on the
	// server goroutine after the handshake).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Registry().HasSession("e2e-node-1") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !srv.Registry().HasSession("e2e-node-1") {
		t.Fatal("the device never appeared in the registry")
	}

	if _, err := conn.Write(buildHelloFrame(t, "e2e-node-1", 4242)); err != nil {
		t.Fatalf("write Hello: %v", err)
	}

	got := readOneFrame(t, conn, 5*time.Second)
	if got == nil {
		t.Fatal("the device received NOTHING after sending Hello -- the chain " +
			"breaks somewhere between routing and the downlink")
	}
	h, err := protoframe.DecodeHeader(got)
	if err != nil {
		t.Fatalf("reply does not decode: %v", err)
	}
	if h.Type != frame.MsgHelloAck {
		t.Fatalf("reply type = 0x%02X, want HelloAck 0x%02X", h.Type, frame.MsgHelloAck)
	}
	// The receiver's agreement check requires the payload's own type byte to
	// match the header, so assert it holds on the wire.
	if got[protoframe.HeaderSize] != frame.MsgHelloAck {
		t.Fatalf("HelloAck header says 0x%02X but its payload says 0x%02X; the "+
			"receiver would DROP this frame", h.Type, got[protoframe.HeaderSize])
	}
}

// TestHelloAckDoesNotAlsoGoToMQTT -- a node on TCP must not get its answer
// twice, on two transports.
func TestHelloAckDoesNotAlsoGoToMQTT(t *testing.T) {
	pki := newE2EPKI(t, "e2e-node-2")
	db := testutil.OpenTestDB(t)
	reg := transport.NewRegistry()
	legacy := &recordingLegacy{}
	bridge := downlink.New(reg, legacy)
	hub := websocket.NewHub()
	go hub.Run()
	mgr := nodemgr.NewManager(db, bridge, hub, nil, nil, nil)

	cfg := transport.Config{
		Addr: "127.0.0.1:0", Cert: pki.srvCert, ClientCAs: pki.pool,
		HandshakeTimeout: 5 * time.Second, OnFrame: mgr.FrameHandler(),
		Registry:         reg, // same object as the bridge -- see the note above
	}
	srv, err := transport.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = srv.Close() })
	go func() { _ = srv.Serve(ctx) }()

	conn := dialE2EDevice(t, pki, srv.Addr().String())
	for i := 0; i < 600 && !srv.Registry().HasSession("e2e-node-2"); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := conn.Write(buildHelloFrame(t, "e2e-node-2", 7)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readOneFrame(t, conn, 5*time.Second); got == nil {
		t.Fatal("no HelloAck on the device socket")
	}
	if len(legacy.topics) != 0 {
		t.Fatalf("HelloAck was ALSO published to MQTT (%d topics: %v) -- a node "+
			"on TCP must not be answered on both transports", len(legacy.topics), legacy.topics)
	}
}

// guard: keep io/errors/net referenced if assertions change
var _ = io.EOF
var _ = errors.Is
var _ net.Conn
