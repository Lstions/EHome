// Package frame implements the binary frame protocol encoder/decoder.
// Compatible with the ESP32 C implementation.
package frame

import (
	"encoding/binary"
	"fmt"
)

// Wire types (protobuf compatible)

// Note: 设计文档 v2.0 最初提议 0xEB，实际实现选用 0x0B 保持兼容性
const (
	FrameMagic = 0x0B

	WireVarint          = 0
	WireFixed64         = 1
	WireLengthDelimited = 2
	WireStartGroup      = 3
	WireEndGroup        = 4
	WireFixed32         = 5
)

// Message types
const (
	MsgHello      = 0x01
	MsgStatusRpt  = 0x02
	MsgDataRpt    = 0x03
	MsgConfigMfst = 0x04
	MsgConfigRslt = 0x05
	MsgWriteCmd   = 0x06
	MsgWriteRsp   = 0x07
	MsgPing       = 0x08
	MsgPong       = 0x09
	MsgOtaCmd     = 0x0A
	MsgOtaProg    = 0x0B
	MsgScanRpt    = 0x0C
	MsgScanReq    = 0x0D
	MsgQueryReq   = 0x0E
	MsgQueryRsp   = 0x0F
	MsgConfigQuery  = 0x10
	MsgConfigReport = 0x11
	MsgHelloAck     = 0x12
	MsgConfigSyncReq  = 0x13 // v2.1: ConfigSyncRequest (ESP→SVR)
	MsgConfigSyncRsp  = 0x14 // v2.1: ConfigSyncResponse (SVR→ESP)
	MsgPongAck        = 0x18 // v3: PongAck (SVR→ESP, response to MsgPing from device)
)

// Field represents a decoded field
type Field struct {
	FieldNum uint8
	WireType uint8
	Value    interface{} // uint64 for varint, []byte for length-delimited
}

// Encoder builds binary frames
type Encoder struct {
	buf []byte
}

// NewEncoder creates a new encoder for the given message type
func NewEncoder(msgType uint8) *Encoder {
	return &Encoder{buf: []byte{msgType}}
}

// Bytes returns the encoded frame
func (e *Encoder) Bytes() []byte {
	return e.buf
}

// Size returns the current size of the encoded frame
func (e *Encoder) Size() int {
	return len(e.buf)
}

// EncodeVarint adds a varint field
func (e *Encoder) EncodeVarint(fieldNum uint8, value uint64) {
	e.buf = append(e.buf, makeTag(fieldNum, WireVarint))
	e.buf = appendVarint(e.buf, value)
}

// EncodeString adds a string field
func (e *Encoder) EncodeString(fieldNum uint8, value string) {
	e.buf = append(e.buf, makeTag(fieldNum, WireLengthDelimited))
	data := []byte(value)
	e.buf = appendVarint(e.buf, uint64(len(data)))
	e.buf = append(e.buf, data...)
}

// EncodeBytes adds a bytes field
func (e *Encoder) EncodeBytes(fieldNum uint8, value []byte) {
	e.buf = append(e.buf, makeTag(fieldNum, WireLengthDelimited))
	e.buf = appendVarint(e.buf, uint64(len(value)))
	e.buf = append(e.buf, value...)
}

// EncodeBool adds a bool field (encoded as varint 0/1)
func (e *Encoder) EncodeBool(fieldNum uint8, value bool) {
	var v uint64
	if value {
		v = 1
	}
	e.EncodeVarint(fieldNum, v)
}

// EncodeSubFrame encodes a nested sub-structure as a length-delimited bytes field.
// The caller encodes sub-fields into a sub-encoder, then wraps with this method.
// Equivalent to protobuf's length-delimited sub-message encoding.
func (e *Encoder) EncodeSubFrame(fieldNum uint8, subBuf []byte) {
	e.EncodeBytes(fieldNum, subBuf)
}

// SubEncoder creates a new encoder for encoding a sub-structure.
// The returned encoder does NOT include a message type byte (field 0),
// as sub-structures are pure field sequences wrapped in a length-delimited field.
func SubEncoder() *Encoder {
	return &Encoder{buf: nil}
}

// === Decoder ===

// Decoder parses binary frames
type Decoder struct {
	buf []byte
	pos int
}

// NewDecoder creates a decoder from raw frame bytes
func NewDecoder(buf []byte) (*Decoder, error) {
	if len(buf) < 1 {
		return nil, fmt.Errorf("empty frame")
	}
	return &Decoder{buf: buf, pos: 1}, nil
}

// MsgType returns the message type byte
func (d *Decoder) MsgType() uint8 {
	return d.buf[0]
}

// NextField reads the next field from the decoder
func (d *Decoder) NextField() (*Field, error) {
	if d.pos >= len(d.buf) {
		return nil, fmt.Errorf("end of frame")
	}

	tag, newPos, err := d.readVarint(d.pos)
	if err != nil {
		return nil, err
	}
	d.pos = newPos

	fieldNum := uint8(tag >> 3)
	wireType := uint8(tag & 0x07)

	field := &Field{FieldNum: fieldNum, WireType: wireType}

	switch wireType {
	case WireVarint:
		val, newPos, err := d.readVarint(d.pos)
		if err != nil {
			return nil, err
		}
		d.pos = newPos
		field.Value = val

	case WireLengthDelimited:
		length, newPos, err := d.readVarint(d.pos)
		if err != nil {
			return nil, err
		}
		d.pos = newPos
		// Fuzz fix: guard against negative or overflow length
		if length < 0 || length > uint64(len(d.buf)) {
			return nil, fmt.Errorf("length-delimited field exceeds frame")
		}
		if d.pos+int(length) > len(d.buf) {
			return nil, fmt.Errorf("length-delimited field exceeds frame")
		}
		field.Value = d.buf[d.pos : d.pos+int(length)]
		d.pos += int(length)

	default:
		return nil, fmt.Errorf("unsupported wire type: %d", wireType)
	}

	return field, nil
}

// === Helpers ===

func makeTag(fieldNum uint8, wireType uint8) uint8 {
	return (fieldNum << 3) | (wireType & 0x07)
}

func appendVarint(buf []byte, value uint64) []byte {
	for value > 0x7F {
		buf = append(buf, byte((value&0x7F)|0x80))
		value >>= 7
	}
	buf = append(buf, byte(value&0x7F))
	return buf
}

func (d *Decoder) readVarint(pos int) (uint64, int, error) {
	var result uint64
	var shift uint
	for {
		if pos >= len(d.buf) {
			return 0, pos, fmt.Errorf("incomplete varint")
		}
		b := d.buf[pos]
		result |= uint64(b&0x7F) << shift
		pos++
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 64 {
			return 0, pos, fmt.Errorf("varint too long")
		}
	}
	return result, pos, nil
}

// GetString extracts a string value from a field
func GetString(field *Field) string {
	if b, ok := field.Value.([]byte); ok {
		return string(b)
	}
	return ""
}

// GetUint64 extracts a uint64 value from a field
func GetUint64(field *Field) uint64 {
	if v, ok := field.Value.(uint64); ok {
		return v
	}
	return 0
}

// GetBytes extracts a byte slice from a field
func GetBytes(field *Field) []byte {
	if b, ok := field.Value.([]byte); ok {
		return b
	}
	return nil
}

// GetBool extracts a bool value from a field
func GetBool(field *Field) bool {
	if v, ok := field.Value.(uint64); ok {
		return v != 0
	}
	return false
}

// Uint64ToBytes converts uint64 to big-endian bytes
func Uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// Uint32ToBytes converts uint32 to big-endian bytes
func Uint32ToBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// MsgTypeName returns a human-readable name for a message type byte
func MsgTypeName(msgType uint8) string {
	names := map[uint8]string{
		MsgHello:       "hello",
		MsgStatusRpt:   "status_report",
		MsgDataRpt:     "data_report",
		MsgConfigMfst:  "config_manifest",
		MsgConfigRslt:  "config_result",
		MsgWriteCmd:    "write_cmd",
		MsgWriteRsp:    "write_response",
		MsgPing:        "ping",
		MsgPong:        "pong",
		MsgOtaCmd:      "ota_cmd",
		MsgOtaProg:     "ota_progress",
		MsgScanRpt:     "scan_report",
		MsgScanReq:     "scan_request",
		MsgQueryReq:    "query_request",
		MsgQueryRsp:    "query_response",
		MsgConfigQuery: "config_query",
		MsgConfigReport: "config_report",
		MsgHelloAck:    "hello_ack",
		MsgConfigSyncReq:  "config_sync_request",
		MsgConfigSyncRsp:  "config_sync_response",
		MsgPongAck:        "pong_ack",
	}
	if name, ok := names[msgType]; ok {
		return name
	}
	return "unknown"
}
