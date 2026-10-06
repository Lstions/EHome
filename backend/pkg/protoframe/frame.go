// Package protoframe implements the 3.0 12-byte delimiter frame header.
//
// # Why a new package instead of extending pkg/frame
//
// pkg/frame is the **2.6 protobuf** format (FrameMagic = 0x0B, varint TLV).
// 3.0 replaces it: no protobuf, fixed 12-byte big-endian header:
//
//	offset field        width
//	0      magic        2     = 0x4548 ("EH")
//	2      ver          1     = 0x30 (3.0)
//	3      type         1     message type
//	4      flags        2     bit0 ACK_REQ / bit1 IS_ACK / bit2 IS_NAK / bit3 CRC32C
//	6      seq          4     per-direction monotonic
//	10     payload_len  2     **this IS the delimiter**
//
// Big-endian was chosen by the implementer (the design never specified it);
// it is host-endianness independent, whereas little-endian would "work" only
// because both ends happen to be little-endian today.
//
// payload_len max = 16368 = MBEDTLS_SSL_IN_CONTENT_LEN(16384) - header(12) - CRC(4),
// so that "one complete message fits in exactly one TLS record" **holds**.
//
// # Contract is verified against the SAME shared vector as the C side
//
// protocol/vectors/frame_header.txt is the single source of truth
// (design S0: "两端各有一套对锚测试"). golden_vectors_test.go reads it
// directly, so drift on either end turns red.
package protoframe

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Wire constants. These MUST match esp32-collector/components/wire/include/wire.h;
// the shared vector test is what actually enforces that.
const (
	Magic      uint16 = 0x4548 // "EH"
	Version    uint8  = 0x30   // 3.0
	HeaderSize int    = 12
	// PayloadMax = 16384 - 12 - 4, so header+payload(+CRC) fits one TLS record.
	PayloadMax uint16 = 16368
	CRCSize    int    = 4
)

// Flags.
const (
	FlagAckReq uint16 = 0x0001
	FlagIsAck  uint16 = 0x0002
	FlagIsNak  uint16 = 0x0004
	FlagCRC32C uint16 = 0x0008
)

// Errors. Each one maps to a distinct caller action, so they are separate
// values rather than one "bad frame" bucket (principle P1).
var (
	// ErrShort means "need more bytes" -- NOT an error condition on a stream.
	ErrShort = errors.New("protoframe: short buffer")
	ErrMagic = errors.New("protoframe: bad magic")
	ErrVer   = errors.New("protoframe: unsupported version")
	ErrRange = errors.New("protoframe: payload_len out of range")
)

// Header is the decoded 12-byte header.
type Header struct {
	Ver        uint8
	Type       uint8
	Flags      uint16
	Seq        uint32
	PayloadLen uint16
}

// HasCRC reports whether the CRC32C flag is set.
func (h Header) HasCRC() bool { return h.Flags&FlagCRC32C != 0 }

// FrameBytes is the total on-wire size of this frame including header,
// payload and (when flagged) the trailing CRC.
func (h Header) FrameBytes() int {
	n := HeaderSize + int(h.PayloadLen)
	if h.HasCRC() {
		n += CRCSize
	}
	return n
}

// EncodeHeader writes the 12-byte header into out (which must be >= HeaderSize).
func EncodeHeader(out []byte, h Header) error {
	if len(out) < HeaderSize {
		return ErrShort
	}
	if h.PayloadLen > PayloadMax {
		return ErrRange
	}
	binary.BigEndian.PutUint16(out[0:2], Magic)
	out[2] = h.Ver
	out[3] = h.Type
	binary.BigEndian.PutUint16(out[4:6], h.Flags)
	binary.BigEndian.PutUint32(out[6:10], h.Seq)
	binary.BigEndian.PutUint16(out[10:12], h.PayloadLen)
	return nil
}

// DecodeHeader parses the 12-byte header at the start of in.
//
// Returns ErrShort when fewer than HeaderSize bytes are available: on a TCP
// stream that means "read more", which is a normal condition, not a failure.
// Keeping it distinct is what lets the caller loop correctly instead of
// treating a partial read as corruption.
func DecodeHeader(in []byte) (Header, error) {
	var h Header
	if len(in) < HeaderSize {
		return h, ErrShort
	}
	if binary.BigEndian.Uint16(in[0:2]) != Magic {
		return h, ErrMagic
	}
	h.Ver = in[2]
	if h.Ver != Version {
		return h, ErrVer
	}
	h.Type = in[3]
	h.Flags = binary.BigEndian.Uint16(in[4:6])
	h.Seq = binary.BigEndian.Uint32(in[6:10])
	h.PayloadLen = binary.BigEndian.Uint16(in[10:12])
	if h.PayloadLen > PayloadMax {
		return h, ErrRange
	}
	return h, nil
}

// CRC32C computes the Castagnoli CRC used when FlagCRC32C is set.
//
// Implemented bitwise (not a table) to mirror the C side exactly and to keep
// the check value CRC32C("123456789") == 0xE3069283 verifiable in one place.
func CRC32C(data []byte) uint32 {
	const poly = 0x82F63B78
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ poly
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFFFFFF
}

// String is for logs/diagnostics.
func (h Header) String() string {
	return fmt.Sprintf("protoframe{ver=0x%02X type=0x%02X flags=0x%04X seq=%d len=%d crc=%v}",
		h.Ver, h.Type, h.Flags, h.Seq, h.PayloadLen, h.HasCRC())
}
