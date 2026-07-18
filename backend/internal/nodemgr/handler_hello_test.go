package nodemgr

import (
	"math"
	"strings"
	"testing"

	"ehome/backend/pkg/frame"
)

func encodedHelloWithNonce(nonce uint64) []byte {
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, "wire-node-id-is-not-authentication")
	enc.EncodeString(2, "2.6.0")
	enc.EncodeString(3, "ESP32-C6")
	enc.EncodeVarint(4, 2)
	enc.EncodeVarint(5, 7)
	enc.EncodeBool(6, true)
	enc.EncodeString(7, "manifest")
	enc.EncodeString(8, "2.6")
	enc.EncodeVarint(frame.HelloFieldHandshakeNonce, nonce)
	return enc.Bytes()
}

func TestParseHelloHandshakeNonceCompatibility(t *testing.T) {
	t.Run("nonzero exact", func(t *testing.T) {
		got, err := parseHello(encodedHelloWithNonce(math.MaxUint32))
		if err != nil {
			t.Fatalf("parseHello: %v", err)
		}
		if got.HandshakeNonce != math.MaxUint32 {
			t.Fatalf("nonce: got %d, want %d", got.HandshakeNonce, uint32(math.MaxUint32))
		}
		if got.ProtocolVersion != "2.6" || got.FirmwareVersion != "2.6.0" {
			t.Fatalf("known fields lost: %#v", got)
		}
	})

	t.Run("absent is legacy", func(t *testing.T) {
		enc := frame.NewEncoder(frame.MsgHello)
		enc.EncodeString(8, "2.5")
		got, err := parseHello(enc.Bytes())
		if err != nil || got.HandshakeNonce != 0 {
			t.Fatalf("legacy parse: got %#v, err=%v", got, err)
		}
	})

	t.Run("explicit zero is legacy", func(t *testing.T) {
		got, err := parseHello(encodedHelloWithNonce(0))
		if err != nil || got.HandshakeNonce != 0 {
			t.Fatalf("zero parse: got %#v, err=%v", got, err)
		}
	})

	t.Run("unknown fields remain compatible", func(t *testing.T) {
		enc := frame.NewEncoder(frame.MsgHello)
		enc.EncodeString(8, "2.6")
		enc.EncodeString(10, "future")
		enc.EncodeVarint(11, 99)
		enc.EncodeVarint(frame.HelloFieldHandshakeNonce, 42)
		got, err := parseHello(enc.Bytes())
		if err != nil || got.HandshakeNonce != 42 {
			t.Fatalf("unknown-field parse: got %#v, err=%v", got, err)
		}
	})
}

func invalidNonceHelloFrames() map[string][]byte {
	duplicate := frame.NewEncoder(frame.MsgHello)
	duplicate.EncodeVarint(frame.HelloFieldHandshakeNonce, 1)
	duplicate.EncodeVarint(frame.HelloFieldHandshakeNonce, 2)

	wrongWire := frame.NewEncoder(frame.MsgHello)
	wrongWire.EncodeString(frame.HelloFieldHandshakeNonce, "1")

	overflow := frame.NewEncoder(frame.MsgHello)
	overflow.EncodeVarint(frame.HelloFieldHandshakeNonce, uint64(math.MaxUint32)+1)

	malformed := append([]byte(nil), frame.NewEncoder(frame.MsgHello).Bytes()...)
	malformed = append(malformed, byte(frame.HelloFieldHandshakeNonce<<3), 0x80)
	nonCanonicalZero := []byte{
		frame.MsgHello, byte(frame.HelloFieldHandshakeNonce << 3), 0x80, 0x00,
	}
	malformedOverflow := []byte{frame.MsgHello, byte(frame.HelloFieldHandshakeNonce << 3)}
	for range 9 {
		malformedOverflow = append(malformedOverflow, 0x80)
	}
	malformedOverflow = append(malformedOverflow, 0x02)

	wrongMessage := frame.NewEncoder(frame.MsgStatusRpt)
	wrongMessage.EncodeVarint(frame.HelloFieldHandshakeNonce, 1)

	return map[string][]byte{
		"duplicate":         duplicate.Bytes(),
		"wrong wire":        wrongWire.Bytes(),
		"uint32 overflow":   overflow.Bytes(),
		"malformed":         malformed,
		"noncanonical zero": nonCanonicalZero,
		"malformed uint64":  malformedOverflow,
		"wrong message":     wrongMessage.Bytes(),
	}
}

func TestParseHelloRejectsInvalidHandshakeNonce(t *testing.T) {
	for name, payload := range invalidNonceHelloFrames() {
		t.Run(name, func(t *testing.T) {
			_, err := parseHello(payload)
			if err == nil {
				t.Fatal("parseHello accepted invalid Hello")
			}
			if name != "wrong message" && !strings.Contains(err.Error(), "Hello") {
				t.Fatalf("error lacks Hello context: %v", err)
			}
		})
	}
}

func TestHandleHelloInvalidNonceDoesNotSendAck(t *testing.T) {
	for name, payload := range invalidNonceHelloFrames() {
		t.Run(name, func(t *testing.T) {
			mock := &senderMockMQTT{}
			mgr := &Manager{mqtt: mock}
			mgr.handleHello("topic-node", payload)
			if len(mock.records) != 0 {
				t.Fatalf("invalid Hello published %d frame(s), want none", len(mock.records))
			}
		})
	}
}
