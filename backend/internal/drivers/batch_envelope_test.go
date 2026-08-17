package drivers

import (
	"bytes"
	"testing"
)

// TestDecodeBatchPlanEnvelope locks the ChannelCmdV2 bounded-plan envelope
// decoder (shared package-level function, see batch_envelope.go) against its
// boundary contract: 1..8 steps, little-endian per-step lengths, no trailing
// bytes, no zero-length or truncated steps.
func TestDecodeBatchPlanEnvelope(t *testing.T) {
	// envelope encodes steps as the firmware bus_worker.c does:
	// [count][kind(1B)][len_le(2B)][response...] per step.
	envelope := func(steps ...[]byte) []byte {
		raw := []byte{byte(len(steps))}
		for _, step := range steps {
			raw = append(raw, 0x00)
			raw = append(raw, byte(len(step)), byte(len(step)>>8))
			raw = append(raw, step...)
		}
		return raw
	}
	step1 := []byte{0xDD, 0xE1, 0x00, 0x00, 0x00, 0x00, 0x77}
	step2 := []byte{0xDD, 0x03, 0x00, 0x03, 0x01, 0x02, 0x03, 0xFF, 0xF7, 0x77}

	t.Run("empty input rejected", func(t *testing.T) {
		if _, err := decodeBatchPlanEnvelope(nil); err == nil {
			t.Fatal("nil input accepted")
		}
		if _, err := decodeBatchPlanEnvelope([]byte{}); err == nil {
			t.Fatal("empty input accepted")
		}
	})

	t.Run("step count zero rejected", func(t *testing.T) {
		if _, err := decodeBatchPlanEnvelope([]byte{0x00}); err == nil {
			t.Fatal("N=0 accepted")
		}
	})

	t.Run("step count nine rejected", func(t *testing.T) {
		// Nine zero-length headers: first fails the count check outright.
		if _, err := decodeBatchPlanEnvelope([]byte{0x09, 0x00, 0x00, 0x00}); err == nil {
			t.Fatal("N=9 accepted")
		}
	})

	t.Run("truncated step header rejected", func(t *testing.T) {
		raw := []byte{0x02, 0x00, 0x07, 0x00} // count says 2, only one header
		if _, err := decodeBatchPlanEnvelope(raw); err == nil {
			t.Fatal("truncated step header accepted")
		}
	})

	t.Run("truncated step body rejected", func(t *testing.T) {
		// count=1, kind=0, len_le=0x007F but only one payload byte present.
		raw := []byte{0x01, 0x00, 0x7F, 0x00, 0x01}
		if _, err := decodeBatchPlanEnvelope(raw); err == nil {
			t.Fatal("truncated step body accepted")
		}
	})

	t.Run("zero-length step rejected", func(t *testing.T) {
		raw := []byte{0x01, 0x00, 0x00, 0x00}
		if _, err := decodeBatchPlanEnvelope(raw); err == nil {
			t.Fatal("zero-length step accepted")
		}
	})

	t.Run("trailing bytes rejected", func(t *testing.T) {
		raw := append(envelope(step1), 0xAA)
		if _, err := decodeBatchPlanEnvelope(raw); err == nil {
			t.Fatal("trailing bytes accepted")
		}
	})

	t.Run("two-step envelope decodes in order", func(t *testing.T) {
		raw := envelope(step1, step2)
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			t.Fatalf("decode error = %v", err)
		}
		if len(steps) != 2 {
			t.Fatalf("got %d steps, want 2", len(steps))
		}
		if !bytes.Equal(steps[0], step1) || !bytes.Equal(steps[1], step2) {
			t.Fatalf("steps = % X / % X", steps[0], steps[1])
		}
	})

	t.Run("kind byte ignored, length little-endian", func(t *testing.T) {
		// A 300-byte step exercises the little-endian length encoding and
		// arbitrary kind bytes.
		big := bytes.Repeat([]byte{0x5A}, 300)
		raw := []byte{0x01, 0x42, 0x2C, 0x01} // kind=0x42, len=0x012C=300 LE
		raw = append(raw, big...)
		steps, err := decodeBatchPlanEnvelope(raw)
		if err != nil {
			t.Fatalf("decode error = %v", err)
		}
		if len(steps) != 1 || !bytes.Equal(steps[0], big) {
			t.Fatalf("big step = %d bytes, want 300", len(steps[0]))
		}
	})
}
