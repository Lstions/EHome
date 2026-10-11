package protoframe

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// golden_vectors_test.go -- S0: the Go side consumes the **shared** vector.
//
// protocol/vectors/frame_header.txt is the single source of truth
// (design S0: "两端各有一套对锚测试"). The C side reads the same file via
// host_tests/frame_header_tests.c and tools/check_frame_header.py.
//
// Reading the shared file -- rather than duplicating expected bytes here --
// is the whole point: a copy would drift silently, and each copy would only
// prove itself right.

const headerVectorRel = "../../../protocol/vectors/frame_header.txt"

type hdrCase struct {
	name       string
	ver        uint8
	typ        uint8
	flags      uint16
	seq        uint32
	payloadLen uint16
	payload    []byte
	wire       []byte
	expect     string // "", "expect_magic", "expect_version", "expect_range"
	line       int
}

func findHeaderVector(t *testing.T) string {
	t.Helper()
	cand := filepath.Join(headerVectorRel)
	if _, err := os.Stat(cand); err != nil {
		t.Skipf("shared vector not found at %s (skipping)", cand)
	}
	return cand
}

func parseHex(t *testing.T, s string, line int) []byte {
	t.Helper()
	if s == "-" {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("line %d: bad hex %q: %v", line, s, err)
	}
	return b
}

func loadHeaderCases(t *testing.T) []hdrCase {
	t.Helper()
	f, err := os.Open(findHeaderVector(t))
	if err != nil {
		t.Fatalf("open vector: %v", err)
	}
	defer f.Close()

	var out []hdrCase
	var cur hdrCase
	inCase := false
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "case":
			cur = hdrCase{name: fields[1], line: lineNo}
			inCase = true
		case "end":
			if inCase {
				out = append(out, cur)
			}
			inCase = false
		default:
			if !inCase {
				continue
			}
			v := ""
			if len(fields) > 1 {
				v = fields[1]
			}
			switch fields[0] {
			case "ver":
				n, _ := strconv.ParseUint(v, 16, 8)
				cur.ver = uint8(n)
			case "type":
				n, _ := strconv.ParseUint(v, 16, 8)
				cur.typ = uint8(n)
			case "flags":
				n, _ := strconv.ParseUint(v, 16, 16)
				cur.flags = uint16(n)
			case "seq":
				n, _ := strconv.ParseUint(v, 16, 32)
				cur.seq = uint32(n)
			case "payload_len":
				n, _ := strconv.ParseUint(v, 16, 16)
				cur.payloadLen = uint16(n)
			case "payload":
				cur.payload = parseHex(t, v, lineNo)
			case "wire":
				cur.wire = parseHex(t, v, lineNo)
			case "expect_magic", "expect_version", "expect_range":
				cur.expect = fields[0]
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan vector: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no cases parsed from shared vector")
	}
	return out
}

// TestHeaderVectorsEncodeReachesExpectedWire checks that encoding the
// declared fields yields **byte-for-byte** the vector's wire value.
func TestHeaderVectorsEncodeReachesExpectedWire(t *testing.T) {
	cases := loadHeaderCases(t)
	positives := 0
	for _, c := range cases {
		if c.expect != "" {
			continue // negative cases are about decode
		}
		positives++
		out := make([]byte, HeaderSize)
		h := Header{Ver: c.ver, Type: c.typ, Flags: c.flags, Seq: c.seq,
			PayloadLen: c.payloadLen}
		if err := EncodeHeader(out, h); err != nil {
			t.Errorf("%s: encode: %v", c.name, err)
			continue
		}
		got := hex.EncodeToString(out)
		want := hex.EncodeToString(c.wire[:HeaderSize])
		if got != want {
			t.Errorf("%s: header mismatch\n got  %s\n want %s", c.name, got, want)
		}
		// Wire length = 12 (header) + payload + 4 (CRC, only when flagged).
		// 我第一版漏了 CRC 那 4 字节，把正确向量判成了错 —— 是**测试的错**。
		wantLen := HeaderSize + int(c.payloadLen)
		if h.Flags&FlagCRC32C != 0 {
			wantLen += CRCSize
		}
		if len(c.wire) != wantLen {
			t.Errorf("%s: wire length %d != 12+%d+%s",
				c.name, len(c.wire), c.payloadLen,
				map[bool]string{true: "4(crc)", false: "0"}[h.Flags&FlagCRC32C != 0])
		}
	}
	if positives == 0 {
		t.Fatal("no positive cases in shared vector")
	}
	t.Logf("verified %d positive header vectors against shared file", positives)
}

// TestHeaderVectorsDecodeMatches declared fields on the vector's own wire.
func TestHeaderVectorsDecodeMatches(t *testing.T) {
	for _, c := range loadHeaderCases(t) {
		if c.expect != "" {
			continue
		}
		h, err := DecodeHeader(c.wire)
		if err != nil {
			t.Errorf("%s: decode: %v", c.name, err)
			continue
		}
		if h.Ver != c.ver || h.Type != c.typ || h.Flags != c.flags ||
			h.Seq != c.seq || h.PayloadLen != c.payloadLen {
			t.Errorf("%s: decoded %+v != declared ver=%02X type=%02X flags=%04X seq=%d len=%d",
				c.name, h, c.ver, c.typ, c.flags, c.seq, c.payloadLen)
		}
		// FrameBytes must equal the vector's wire length exactly
		// （我第一版又加了一次 CRC ⇒ 重复计数，是**测试的错**。）
		if got := h.FrameBytes(); got != len(c.wire) {
			t.Errorf("%s: FrameBytes %d != wire length %d", c.name, got, len(c.wire))
		}
	}
}

// TestHeaderNegativeVectors -- a malformed header must be REJECTED with the
// specific error the vector names, not silently accepted.
func TestHeaderNegativeVectors(t *testing.T) {
	want := map[string]error{
		"expect_magic":   ErrMagic,
		"expect_version": ErrVer,
		"expect_range":   ErrRange,
	}
	seen := 0
	for _, c := range loadHeaderCases(t) {
		if c.expect == "" {
			continue
		}
		seen++
		exp, ok := want[c.expect]
		if !ok {
			t.Errorf("%s: unknown expectation %q", c.name, c.expect)
			continue
		}
		_, err := DecodeHeader(c.wire)
		if err != want[c.expect] {
			t.Errorf("%s (%s): got err %v, want %v", c.name, c.expect, err, exp)
		}
	}
	if seen == 0 {
		t.Fatal("no negative cases found -- the vector's negative coverage is gone")
	}
	t.Logf("verified %d negative header vectors", seen)
}

// TestShortBufferIsNeedMoreNotCorruption -- on a TCP stream a partial header
// is normal ("read more"), so it must be distinguishable from a bad frame.
func TestShortBufferIsNeedMoreNotCorruption(t *testing.T) {
	for n := 0; n < HeaderSize; n++ {
		_, err := DecodeHeader(make([]byte, n))
		if err != ErrShort {
			t.Errorf("len=%d: got %v, want ErrShort", n, err)
		}
	}
}

// TestPayloadMaxFitsOneTLSRecord pins the "one message == one TLS record"
// property. It checks TWO distinct things, and conflating them was a defect.
//
// ⚠ 2026-10-08（§195）：这里原先写死 `const tlsInContentLen = 16384`。
//   那让本测试同时承担了两件事，而其中一件不该由它承担：
//     ① **算术自洽**：header + payloadMax + CRC == MaxFrameBytes —— 应当守；
//     ② **"记录大小是 16384"** —— 不该守。那是**设备侧的编译期选择**
//        （CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN），2026-10-08 已因 s3p 内存实测
//        降到 8192（largeat +8192、配置事务 1/9 → 9/9）。
//        写死它 ⇒ 一次**有意的**三端联动改动会被误报成回归，
//        而真正该抓的"改一处漏两处"反而抓不到。
//   ⇒ 断言 ① 用 MaxFrameBytes（本包唯一权威常量）；
//      再用**跨端来源**核对它与固件是否一致（见下面 firmwareTLSRecordLen）。
func TestPayloadMaxFitsOneTLSRecord(t *testing.T) {
	// ① 算术自洽（与具体记录大小无关）
	if got := HeaderSize + int(PayloadMax) + CRCSize; got != int(MaxFrameBytes) {
		t.Errorf("header(%d)+payloadmax(%d)+crc(%d) = %d, want MaxFrameBytes=%d",
			HeaderSize, PayloadMax, CRCSize, got, MaxFrameBytes)
	}
	// ② 跨端一致：MaxFrameBytes 必须等于固件 sdkconfig.defaults 里的
	//    CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN。三者是一个约束：
	//      后端 MaxFrameBytes == 固件 LINK_TCP_MTU_BYTES == 固件 IN_CONTENT_LEN
	//    改一处漏另一处 ⇒ 大帧静默跨 TLS 记录分片 ⇒ 在这里必红。
	if want, ok := firmwareTLSRecordLen(t); ok {
		if int(MaxFrameBytes) != want {
			t.Errorf("MaxFrameBytes=%d 与固件 CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN=%d 不一致："+
				"一处改了、另一处没改 ⇒ 大帧会静默跨 TLS 记录分片",
				MaxFrameBytes, want)
		}
	}
	// Encoding a payload one byte over the max must be refused, not truncated.
	buf := make([]byte, HeaderSize)
	err := EncodeHeader(buf, Header{Ver: Version, PayloadLen: PayloadMax + 1})
	if err != ErrRange {
		t.Errorf("PayloadMax+1: got %v, want ErrRange", err)
	}
	if err := EncodeHeader(buf, Header{Ver: Version, PayloadLen: PayloadMax}); err != nil {
		t.Errorf("PayloadMax: unexpected %v", err)
	}
}

// TestCRCInVectorsMatchesGoImplementation closes the gap my first probe found:
// the encode test only compared wire[0:12], so a corrupted **payload or CRC**
// byte in the shared vector went unnoticed by the Go side.
//
// This matters because the CRC is the one value both ends must compute
// independently and agree on bit-for-bit. If they disagree, every real frame
// with FlagCRC32C set is rejected -- and the symptom looks like "network
// corruption", not a bug.
func TestCRCInVectorsMatchesGoImplementation(t *testing.T) {
	checked := 0
	for _, c := range loadHeaderCases(t) {
		if c.expect != "" || c.flags&FlagCRC32C == 0 {
			continue
		}
		checked++
		if len(c.wire) != HeaderSize+int(c.payloadLen)+CRCSize {
			t.Errorf("%s: crc-flagged wire length %d != 12+%d+4",
				c.name, len(c.wire), c.payloadLen)
			continue
		}
		// Payload bytes must sit where the header says they do.
		gotPayload := c.wire[HeaderSize : HeaderSize+int(c.payloadLen)]
		if hex.EncodeToString(gotPayload) != hex.EncodeToString(c.payload) {
			t.Errorf("%s: wire payload %s != declared %s",
				c.name, hex.EncodeToString(gotPayload), hex.EncodeToString(c.payload))
		}
		// The trailing 4 bytes must be our CRC32C of the payload.
		wantCRC := c.wire[HeaderSize+int(c.payloadLen):]
		gotCRC := CRC32C(gotPayload)
		if gotCRC != binary.BigEndian.Uint32(wantCRC) {
			t.Errorf("%s: CRC32C(payload) = 0x%08X, vector says 0x%08X",
				c.name, gotCRC, binary.BigEndian.Uint32(wantCRC))
		}
	}
	if checked == 0 {
		t.Fatal("no CRC-flagged vectors found -- CRC cross-end coverage is gone")
	}
	t.Logf("verified CRC32C against %d vector(s)", checked)
}

// TestCRC32CCheckValue pins the Castagnoli check value: a wrong polynomial or
// bit order silently produces plausible-looking CRCs that reject every real
// frame, and the failure looks like "network corruption" instead of a bug.
func TestCRC32CCheckValue(t *testing.T) {
	if got := CRC32C([]byte("123456789")); got != 0xE3069283 {
		t.Fatalf("CRC32C(\"123456789\") = 0x%08X, want 0xE3069283", got)
	}
	if CRC32C(nil) != 0 {
		t.Errorf("CRC32C(nil) = 0x%08X, want 0", CRC32C(nil))
	}
}

// firmwareTLSRecordLen 读**固件仓库的 sdkconfig.defaults**，取出
// CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN。返回 ok=false 表示读不到（例如在只 checkout
// 了 backend/ 的 CI 里）—— 那种情况**跳过**而不是失败：本测试的主职责是可自洽的
// 算术，跨端核对是"有源可查时必须成立"的加强项。
//
// 为什么不把它做成"读运行期值"：设备侧的 IN_CONTENT_LEN 是**编译期**常量，
// 运行期进程里根本不存在它；而 sdkconfig.defaults 正是三端联动的那个**源**。
func firmwareTLSRecordLen(t *testing.T) (int, bool) {
	t.Helper()
	// backend/pkg/protoframe → ../../../esp32-collector/sdkconfig.defaults
	rel := filepath.Join("..", "..", "..", "esp32-collector", "sdkconfig.defaults")
	f, err := os.Open(rel)
	if err != nil {
		t.Logf("跳过跨端核对：读不到 %s（%v）", rel, err)
		return 0, false
	}
	defer f.Close()
	const key = "CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN="
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, key) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, key)))
		if err != nil {
			t.Errorf("固件 %s 的值无法解析：%q", key, line)
			return 0, false
		}
		return n, true
	}
	t.Logf("跳过跨端核对：固件 sdkconfig.defaults 里没有 %s（可能用 IDF 默认值）", key)
	return 0, false
}