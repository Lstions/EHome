package nodemgr

import (
	"bytes"
	"testing"
)

// TestStreamReassembler_EmptyInput verifies that empty data input returns
// empty bytes and does not create a buffer.
func TestStreamReassembler_EmptyInput(t *testing.T) {
	sr := newStreamReassembler()

	// requestID=0 returns data unchanged (no buffering path)
	got := sr.Append("devA", 0, []byte{})
	if len(got) != 0 {
		t.Errorf("requestID=0 empty: expected empty, got %d bytes", len(got))
	}

	// requestID>0 with empty data also returns empty, no buffer created
	got = sr.Append("devA", 42, []byte{})
	if len(got) != 0 {
		t.Errorf("reqID=42 empty: expected empty, got %d bytes", len(got))
	}
	if sr.pending() != 0 {
		t.Errorf("expected 0 pending buffers, got %d", sr.pending())
	}
}

// TestStreamReassembler_SingleFrame verifies that a single complete frame
// can be appended and then consumed.
func TestStreamReassembler_SingleFrame(t *testing.T) {
	sr := newStreamReassembler()
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}

	got := sr.Append("devA", 100, data)
	if !bytes.Equal(got, data) {
		t.Errorf("expected %v, got %v", data, got)
	}
	if sr.pending() != 1 {
		t.Errorf("expected 1 pending buffer, got %d", sr.pending())
	}

	// consume removes the buffer
	sr.Consume("devA", 100)
	if sr.pending() != 0 {
		t.Errorf("after consume, expected 0 pending, got %d", sr.pending())
	}
}

// TestStreamReassembler_PartialFrame verifies that data appended in two
// chunks accumulates correctly — only after the second chunk is the full
// data available.
func TestStreamReassembler_PartialFrame(t *testing.T) {
	sr := newStreamReassembler()
	half1 := []byte{0xAA, 0xBB}
	half2 := []byte{0xCC, 0xDD}
	full := append(half1, half2...)

	// First half
	got := sr.Append("devA", 200, half1)
	if !bytes.Equal(got, half1) {
		t.Errorf("first half: expected %v, got %v", half1, got)
	}
	if sr.pending() != 1 {
		t.Errorf("expected 1 pending after first half, got %d", sr.pending())
	}

	// Second half — should return full accumulated data
	got = sr.Append("devA", 200, half2)
	if !bytes.Equal(got, full) {
		t.Errorf("second half: expected %v, got %v", full, got)
	}

	// Still 1 buffer (not consumed yet)
	if sr.pending() != 1 {
		t.Errorf("expected 1 pending after second half, got %d", sr.pending())
	}

	sr.Consume("devA", 200)
	if sr.pending() != 0 {
		t.Errorf("after consume, expected 0 pending, got %d", sr.pending())
	}
}

// TestStreamReassembler_MultipleFrames verifies that multiple independent
// requestIDs each get their own buffer.
func TestStreamReassembler_MultipleFrames(t *testing.T) {
	sr := newStreamReassembler()

	data1 := []byte{0x01}
	data2 := []byte{0x02}
	data3 := []byte{0x03}

	sr.Append("devA", 301, data1)
	sr.Append("devA", 302, data2)
	sr.Append("devA", 303, data3)

	if sr.pending() != 3 {
		t.Errorf("expected 3 pending buffers, got %d", sr.pending())
	}

	// Consume one at a time
	sr.Consume("devA", 302)
	if sr.pending() != 2 {
		t.Errorf("after consuming 302, expected 2 pending, got %d", sr.pending())
	}

	sr.Consume("devA", 301)
	if sr.pending() != 1 {
		t.Errorf("after consuming 301, expected 1 pending, got %d", sr.pending())
	}

	sr.Consume("devA", 303)
	if sr.pending() != 0 {
		t.Errorf("after consuming 303, expected 0 pending, got %d", sr.pending())
	}
}

// TestStreamReassembler_Pending verifies that pending() returns the correct
// count of active reassembly buffers.
func TestStreamReassembler_Pending(t *testing.T) {
	sr := newStreamReassembler()

	if sr.pending() != 0 {
		t.Errorf("initial: expected 0 pending, got %d", sr.pending())
	}

	sr.Append("devA", 401, []byte{0x01})
	if sr.pending() != 1 {
		t.Errorf("after 1 append: expected 1, got %d", sr.pending())
	}

	sr.Append("devA", 402, []byte{0x02})
	sr.Append("devA", 403, []byte{0x03})
	if sr.pending() != 3 {
		t.Errorf("after 3 appends: expected 3, got %d", sr.pending())
	}

	// requestID=0 does not create a buffer
	sr.Append("devA", 0, []byte{0xFF})
	if sr.pending() != 3 {
		t.Errorf("reqID=0 append should not add buffer: expected 3, got %d", sr.pending())
	}
}

// TestStreamReassembler_Discard verifies that discard() clears the buffer
// for a given requestID.
func TestStreamReassembler_Discard(t *testing.T) {
	sr := newStreamReassembler()

	sr.Append("devA", 501, []byte{0x01, 0x02})
	sr.Append("devA", 502, []byte{0x03, 0x04})

	if sr.pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", sr.pending())
	}

	// Discard one buffer
	sr.discard("devA", 501)
	if sr.pending() != 1 {
		t.Errorf("after discard(501): expected 1 pending, got %d", sr.pending())
	}

	// The other buffer should still be intact — appending more should show accumulated data
	got := sr.Append("devA", 502, []byte{0x05})
	expected := []byte{0x03, 0x04, 0x05}
	if !bytes.Equal(got, expected) {
		t.Errorf("after discard(501), append(502): expected %v, got %v", expected, got)
	}

	// Discard the remaining
	sr.discard("devA", 502)
	if sr.pending() != 0 {
		t.Errorf("after discard(502): expected 0 pending, got %d", sr.pending())
	}
}

// TestStreamReassembler_Interleaved verifies that multiple requestIDs can be
// interleaved (appended in alternating order) and each maintains its own
// independent buffer.
func TestStreamReassembler_Interleaved(t *testing.T) {
	sr := newStreamReassembler()

	// Interleave data for requestID 601 and 602
	got1a := sr.Append("devA", 601, []byte{0xA1})
	got2a := sr.Append("devA", 602, []byte{0xB1})
	got1b := sr.Append("devA", 601, []byte{0xA2})
	got2b := sr.Append("devA", 602, []byte{0xB2})

	// Each requestID should have accumulated its own data
	expected1 := []byte{0xA1, 0xA2}
	expected2 := []byte{0xB1, 0xB2}

	if !bytes.Equal(got1a, []byte{0xA1}) {
		t.Errorf("601 first: expected [0xA1], got %v", got1a)
	}
	if !bytes.Equal(got2a, []byte{0xB1}) {
		t.Errorf("602 first: expected [0xB1], got %v", got2a)
	}
	if !bytes.Equal(got1b, expected1) {
		t.Errorf("601 second: expected %v, got %v", expected1, got1b)
	}
	if !bytes.Equal(got2b, expected2) {
		t.Errorf("602 second: expected %v, got %v", expected2, got2b)
	}

	if sr.pending() != 2 {
		t.Errorf("expected 2 pending buffers, got %d", sr.pending())
	}

	// Consume both
	sr.Consume("devA", 601)
	sr.Consume("devA", 602)
	if sr.pending() != 0 {
		t.Errorf("after consuming both: expected 0 pending, got %d", sr.pending())
	}
}

// TestStreamReassembler_RequestIDZero_NoBuffering verifies that requestID=0
// (CMD_SAMPLE, no correlation) bypasses buffering entirely.
func TestStreamReassembler_RequestIDZero_NoBuffering(t *testing.T) {
	sr := newStreamReassembler()

	data := []byte{0x01, 0x02, 0x03}
	got := sr.Append("devA", 0, data)
	if !bytes.Equal(got, data) {
		t.Errorf("reqID=0: expected data unchanged %v, got %v", data, got)
	}
	if sr.pending() != 0 {
		t.Errorf("reqID=0: expected 0 pending buffers, got %d", sr.pending())
	}

	// consume and discard on requestID=0 should be no-ops (no panic)
	sr.Consume("devA", 0)
	sr.discard("devA", 0)
}

// TestStreamReassembler_HardCap verifies that the buffer is capped at maxBytes
// and the tail is kept.
func TestStreamReassembler_HardCap(t *testing.T) {
	sr := newStreamReassembler()
	// Temporarily lower maxBytes for predictable testing
	sr.maxBytes = 4

	// Append 3 bytes
	sr.Append("devA", 701, []byte{0x01, 0x02, 0x03})

	// Append 3 more → total 6, capped to 4 (tail)
	got := sr.Append("devA", 701, []byte{0x04, 0x05, 0x06})

	expected := []byte{0x03, 0x04, 0x05, 0x06}
	if !bytes.Equal(got, expected) {
		t.Errorf("hard cap: expected %v, got %v", expected, got)
	}
}

// TestStreamReassembler_SameRequestIDAcrossDevices verifies the composite-key
// invariant required for concurrent parser shards: two devices using the same
// requestID must never share a reassembly buffer.
func TestStreamReassembler_SameRequestIDAcrossDevices(t *testing.T) {
	sr := newStreamReassembler()

	// deviceA and deviceB both send fragments under requestID 801.
	gotA1 := sr.Append("deviceA", 801, []byte{0xA1, 0xA2})
	gotB1 := sr.Append("deviceB", 801, []byte{0xB1, 0xB2})
	gotA2 := sr.Append("deviceA", 801, []byte{0xA3})
	gotB2 := sr.Append("deviceB", 801, []byte{0xB3})

	if want := []byte{0xA1, 0xA2}; !bytes.Equal(gotA1, want) {
		t.Errorf("deviceA first: expected %v, got %v", want, gotA1)
	}
	if want := []byte{0xB1, 0xB2}; !bytes.Equal(gotB1, want) {
		t.Errorf("deviceB first: expected %v, got %v", want, gotB1)
	}
	if want := []byte{0xA1, 0xA2, 0xA3}; !bytes.Equal(gotA2, want) {
		t.Errorf("deviceA second: expected %v, got %v — deviceB bytes leaked in", want, gotA2)
	}
	if want := []byte{0xB1, 0xB2, 0xB3}; !bytes.Equal(gotB2, want) {
		t.Errorf("deviceB second: expected %v, got %v — deviceA bytes leaked in", want, gotB2)
	}

	if sr.pending() != 2 {
		t.Errorf("expected 2 independent buffers, got %d", sr.pending())
	}

	// Consuming deviceA must not touch deviceB's buffer.
	sr.Consume("deviceA", 801)
	if sr.pending() != 1 {
		t.Errorf("after consuming deviceA only: expected 1 pending, got %d", sr.pending())
	}
	gotB3 := sr.Append("deviceB", 801, []byte{0xB4})
	if want := []byte{0xB1, 0xB2, 0xB3, 0xB4}; !bytes.Equal(gotB3, want) {
		t.Errorf("deviceB after deviceA consume: expected %v, got %v", want, gotB3)
	}
}
