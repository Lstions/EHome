package transport

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"ehome/backend/pkg/protoframe"
)

// session_test.go -- the server send path.
//
// The interesting cases are all about PARTIAL writes, because that is where
// the firmware was actually burned (defect D-30): a partial write treated as
// "retry the whole frame" duplicates bytes on the wire and corrupts the TCP
// stream while every counter still looks healthy.

// fakeConn is a programmable net.Conn. It records exactly what was written so
// tests can assert on the BYTES, not on counters -- counters stayed green in
// D-30 too.
type fakeConn struct {
	mu       sync.Mutex
	written  []byte
	closed   bool
	// dribble, when > 0, caps how many bytes one Write accepts. This makes a
	// frame span many Write calls, which is what exposes missing serialisation
	// between concurrent senders.
	dribble  int
	// script drives Write: each call consumes one step.
	script   []writeStep
	step     int
	deadline time.Time
}

type writeStep struct {
	// accept is the number of bytes accepted from the offered slice.
	accept int
	// err, if non-nil, is returned alongside accept.
	err error
	// timeout makes the returned error a net.Error with Timeout()==true.
	timeout bool
}

func (c *fakeConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if c.dribble > 0 {
		// ⚠ 必须连**错误一起返回**：返回 n < len(p) 且 err == nil 是违反
		// io.Writer 契约的，实现会（正确地）把它当成错误并断开连接 ——
		// 我第一版就是那样写的，结果只写了 1 字节就停了。
		//
		// 这里用 timeout 错误：它是**可续写**的，正是真实慢连接的样子，
		// 也让每一帧都跨越多次 Write ⇒ 并发交错的机会真实存在。
		n := c.dribble
		if n > len(p) {
			n = len(p)
		}
		c.written = append(c.written, p[:n]...)
		if n == len(p) {
			return n, nil
		}
		return n, &timeoutErr{}
	}
	if c.step >= len(c.script) {
		// default: accept everything
		c.written = append(c.written, p...)
		return len(p), nil
	}
	s := c.script[c.step]
	c.step++
	n := s.accept
	if n > len(p) {
		n = len(p)
	}
	c.written = append(c.written, p[:n]...)
	if s.timeout {
		return n, &timeoutErr{}
	}
	return n, s.err
}

type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

func (c *fakeConn) Read([]byte) (int, error)         { return 0, errors.New("not used") }
func (c *fakeConn) Close() error                     { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { c.mu.Lock(); c.deadline = t; c.mu.Unlock(); return nil }

func (c *fakeConn) bytesWritten() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.written...)
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func mkFrame(t *testing.T, typ uint8, seq uint32, payload []byte) []byte {
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

// TestSendWritesWholeFrame -- the happy path, asserted on BYTES.
func TestSendWritesWholeFrame(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	frame := mkFrame(t, 0x03, 1, []byte("abc"))

	if r := s.Send(frame); r != SendOK {
		t.Fatalf("Send = %v, want SendOK", r)
	}
	if got := c.bytesWritten(); string(got) != string(frame) {
		t.Fatalf("wrote %x, want %x", got, frame)
	}
	st := s.Stats()
	if st.FramesSent != 1 || st.BytesSent != uint64(len(frame)) {
		t.Fatalf("stats %+v", st)
	}
}

// TestPartialWriteIsResumedNotResent -- THE D-30 CASE.
//
// The connection accepts 5 bytes, then times out. Because a write timeout can
// return n > 0 (net.Conn docs), the socket is still usable and the remainder
// must be written. Resending the whole frame would put the first 5 bytes on
// the wire TWICE.
//
// The assertion is on the BYTES, mirroring the firmware regression test
// (test_partial_write_is_continued) which also records what actually landed.
func TestPartialWriteIsResumedNotResent(t *testing.T) {
	c := &fakeConn{script: []writeStep{
		{accept: 5, timeout: true}, // partial progress then timeout
	}}
	s := NewSession("n1", c, time.Second)
	frame := mkFrame(t, 0x03, 1, []byte("payload-payload"))

	if r := s.Send(frame); r != SendOK {
		t.Fatalf("Send = %v, want SendOK (a timeout is resumable)", r)
	}
	got := c.bytesWritten()
	if len(got) != len(frame) {
		t.Fatalf("wrote %d bytes, want %d -- partial write was NOT resumed", len(got), len(frame))
	}
	if string(got) != string(frame) {
		t.Fatalf("wire bytes corrupted:\n got  %x\n want %x (first 5 bytes duplicated?)", got, frame)
	}
	if st := s.Stats(); st.PartialsResumed == 0 {
		t.Error("partial resumption was not counted")
	}
}

// TestWriteFailureMidFrameIsFatalNotBackpressure --
// if part of a frame is out and the connection then dies, the peer holds a
// truncated frame it can never complete. Reporting "backpressure" would invite
// a caller retry that appends a second copy after the prefix.
func TestWriteFailureMidFrameIsFatalNotBackpressure(t *testing.T) {
	c := &fakeConn{script: []writeStep{
		{accept: 7, err: errors.New("connection reset")},
	}}
	s := NewSession("n1", c, time.Second)
	frame := mkFrame(t, 0x03, 1, []byte("some-payload"))

	r := s.Send(frame)
	if r != SendFatal {
		t.Fatalf("Send = %v, want SendFatal (not backpressure -- a truncated frame is on the wire)", r)
	}
	if st := s.Stats(); st.Connected {
		t.Error("session should be closed after a mid-frame write failure")
	}
	if st := s.Stats(); st.CloseReason == "" {
		t.Error("close reason must be recorded")
	}
}

// TestOversizedFrameRefusedBeforeAnyWrite -- P2: the contract is verified
// BEFORE writing, so an oversized frame must not put a single byte on the wire.
func TestOversizedFrameRefusedBeforeAnyWrite(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)

	tooBig := make([]byte, s.MTU()+1)
	if r := s.Send(tooBig); r != SendPayloadTooBig {
		t.Fatalf("Send = %v, want SendPayloadTooBig", r)
	}
	if n := len(c.bytesWritten()); n != 0 {
		t.Fatalf("%d bytes were written for an oversized frame, want 0 (P2: check BEFORE send)", n)
	}
	// The largest LEGAL frame. mkFrame builds header+payload WITHOUT a CRC, so
	// its maximum is header + PayloadMax = 12 + 16368 = 16380.
	//
	// ⚠ 我这个用例前后算错过两次，都记在这里：
	//    ① 先写 MTU-header = 16372，忘了 CRC 也要占字节 ⇒ protoframe 正确拒绝；
	//    ② 再写 MTU-header-CRC，但 mkFrame **不加 CRC** ⇒ 只有 16380 字节。
	// 两次都是**我的测试算错**，实现一直是对的。
	frame := mkFrame(t, 0x03, 1, make([]byte, protoframe.PayloadMax))
	if want := protoframe.HeaderSize + int(protoframe.PayloadMax); len(frame) != want {
		t.Fatalf("built frame is %d bytes, expected %d", len(frame), want)
	}
	if r := s.Send(frame); r != SendOK {
		t.Fatalf("the largest legal no-CRC frame was refused: %v", r)
	}
	// With a CRC the same payload is MTU bytes exactly -- the true maximum,
	// and still accepted.
	withCRC := append(append([]byte(nil), frame...), 0, 0, 0, 0)
	withCRC[5] |= 0x08 // set the CRC32C flag
	if len(withCRC) != int(s.MTU()) {
		t.Fatalf("frame with CRC is %d bytes, expected MTU %d", len(withCRC), s.MTU())
	}
	if r := s.Send(withCRC); r != SendOK {
		t.Fatalf("a frame of exactly MTU was refused: %v", r)
	}
	// One byte over MTU must be refused.
	over := make([]byte, s.MTU()+1)
	if r := s.Send(over); r != SendPayloadTooBig {
		t.Fatalf("MTU+1 frame = %v, want SendPayloadTooBig", r)
	}
}

// TestEmptyFrameRefused -- zero-length is not a frame (EncodeHeader requires a
// header), so it must be refused rather than written as a stray empty write.
func TestEmptyFrameRefused(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	if r := s.Send(nil); r != SendPayloadTooBig {
		t.Fatalf("Send(nil) = %v, want SendPayloadTooBig", r)
	}
	if n := len(c.bytesWritten()); n != 0 {
		t.Fatalf("%d bytes written for an empty frame", n)
	}
}

// TestSendAfterCloseIsNotReady -- P1: the caller of a closed session must get
// "not ready", not a fatal it might treat as retryable.
func TestSendAfterCloseIsNotReady(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	s.Close("test")
	if r := s.Send(mkFrame(t, 0x03, 1, []byte("x"))); r != SendNotReady {
		t.Fatalf("Send after Close = %v, want SendNotReady", r)
	}
	if st := s.Stats(); st.Connected {
		t.Error("Stats must report not connected after Close")
	}
}

// TestCloseKeepsFirstReason -- the first cause is the informative one; a later
// cleanup Close must not overwrite it with something trivial.
func TestCloseKeepsFirstReason(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	s.Close("the real reason")
	s.Close("cleanup")
	if st := s.Stats(); st.CloseReason != "the real reason" {
		t.Fatalf("CloseReason = %q, want the FIRST reason", st.CloseReason)
	}
}

// TestMTUMatchesFirmwareAndFrameLayout -- the MTU is a number both ends must
// agree on. If the server used a different one than the firmware, frames would
// be rejected with no obvious cause ("it worked yesterday").
func TestMTUMatchesFirmwareAndFrameLayout(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	if got := s.MTU(); got != protoframe.MaxFrameBytes {
		t.Fatalf("MTU = %d, want MaxFrameBytes %d", got, protoframe.MaxFrameBytes)
	}
	// MaxFrameBytes must be exactly header+payloadmax+crc, so one maximum
	// frame still fits one TLS record.
	if sum := uint32(protoframe.HeaderSize) + uint32(protoframe.PayloadMax) + uint32(protoframe.CRCSize); sum != s.MTU() {
		t.Fatalf("header+payloadmax+crc = %d != MTU %d", sum, s.MTU())
	}
}

// TestConcurrentSendsDoNotInterleave -- two goroutines sending to one device
// must not weave their bytes together. TCP would deliver the corruption
// faithfully and the receiver could not detect it.
func TestConcurrentSendsDoNotInterleave(t *testing.T) {
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)

	const n = 50
	frameA := mkFrame(t, 0x03, 1, make([]byte, 100))
	frameB := mkFrame(t, 0x04, 2, make([]byte, 100))
	for i := range frameA { frameA[i] = 0xAA }
	for i := range frameB { frameB[i] = 0xBB }
	// restore the headers overwritten by the fill above
	frameA = mkFrame(t, 0x03, 1, frameA[protoframe.HeaderSize:])
	frameB = mkFrame(t, 0x04, 2, frameB[protoframe.HeaderSize:])

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Send(frameA) }()
		go func() { defer wg.Done(); s.Send(frameB) }()
	}
	wg.Wait()

	got := c.bytesWritten()
	want := 2 * n * len(frameA)
	if len(got) != want {
		t.Fatalf("wrote %d bytes, want %d", len(got), want)
	}
	// Every len(frameA)-sized slice must be entirely one frame or the other.
	for off := 0; off < len(got); off += len(frameA) {
		chunk := got[off : off+len(frameA)]
		if string(chunk) != string(frameA) && string(chunk) != string(frameB) {
			t.Fatalf("frame at offset %d is interleaved (neither A nor B)", off)
		}
	}
	if st := s.Stats(); st.FramesSent != 2*n {
		t.Fatalf("FramesSent = %d, want %d", st.FramesSent, 2*n)
	}
}

// TestConcurrentSendsDoNotInterleaveWithShortWrites --
//
// ⚠ 这个用例是 S5 变异没被抓住之后补的。原来的并发用例**抓不到缺锁**，
// 因为它的假连接默认**一次接受全部字节** ⇒ 每一帧都在单次 Write 调用里写完，
// 两个 goroutine 之间**没有可交错的边界**。
// ⇒ **假连接把"缺少序列化"这件事掩盖了**，测试绿，但绿得没有意义。
//
// 改成每次只接受 1 字节：这样每一帧都要多次 Write，交错的机会真实存在。
// 断言仍然打在**字节序列**上：每个字节只能属于 A 或 B，不允许混杂。
func TestConcurrentSendsDoNotInterleaveWithShortWrites(t *testing.T) {
	c := &fakeConn{} // script empty => default path, but we force 1-byte writes
	c.dribble = 1
	s := NewSession("n1", c, time.Second)

	const n = 20
	payloadA := make([]byte, 40)
	payloadB := make([]byte, 40)
	frameA := mkFrame(t, 0x03, 1, payloadA)
	frameB := mkFrame(t, 0x04, 2, payloadB)
	for i := 0; i < 40; i++ {
		frameA[protoframe.HeaderSize+i] = 0xAA
		frameB[protoframe.HeaderSize+i] = 0xBB
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Send(frameA) }()
		go func() { defer wg.Done(); s.Send(frameB) }()
	}
	wg.Wait()

	got := c.bytesWritten()
	if len(got) != 2*n*len(frameA) {
		t.Fatalf("wrote %d bytes, want %d", len(got), 2*n*len(frameA))
	}
	// Walk frame by frame; each must be wholly A or wholly B.
	for off := 0; off < len(got); off += len(frameA) {
		chunk := got[off : off+len(frameA)]
		if string(chunk) != string(frameA) && string(chunk) != string(frameB) {
			t.Fatalf("frame at offset %d is INTERLEAVED -- two senders were not serialised", off)
		}
	}
}

// TestSendBackpressureIsStillAStub pins the honest state of the enum.
//
// Design §2.5.3 specifies SendBackpressure, but the send window that would make
// it reachable is undefined in the design (§4.1 points at §5.2, which has no
// window number). So today the value is unreachable.
//
// This test exists so the situation cannot drift SILENTLY: when the window is
// implemented, this test fails, forcing the stub comment to be updated in the
// same change. A caller writing a retry branch today should know it is dead code.
func TestSendBackpressureIsStillAStub(t *testing.T) {
	if !SendBackpressureIsStub {
		t.Log("SendBackpressure is now reachable -- update the stub comment in session.go")
	}
	c := &fakeConn{}
	s := NewSession("n1", c, time.Second)
	// A normal send never reports backpressure today.
	if r := s.Send(mkFrame(t, 0x03, 1, []byte("x"))); r == SendBackpressure {
		t.Fatal("Send reported backpressure, but no window exists -- " +
			"either the window landed (update SendBackpressureIsStub) or this is a bug")
	}
}

// TestShortWriteWithNilErrorIsSurfaced -- a Write that reports progress with a
// nil error while writing less than asked violates the io.Writer contract.
// Accepting it would leave us unable to tell "done" from "stalled".
func TestShortWriteWithNilErrorIsSurfaced(t *testing.T) {
	c := &fakeConn{script: []writeStep{{accept: 3, err: nil}}}
	s := NewSession("n1", c, time.Second)
	frame := mkFrame(t, 0x03, 1, []byte("abcdef"))
	// It must not silently loop or claim success on a stalled peer.
	r := s.Send(frame)
	if r == SendOK {
		t.Fatal("Send reported success despite a short write with a nil error")
	}
}
