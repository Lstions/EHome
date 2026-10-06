package nodemgr

import (
	"testing"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/protoframe"
)

// router_test.go -- the 3.0 routing seam.
//
// 3.0 carries the message type TWICE: in the 12-byte header (offset 3) and as
// the first byte of the payload (the 2.x convention that every decoder still
// validates). Nothing could disagree before, because 2.x had no header. A
// divergence would be silent: routing would follow the header while decoding
// followed the payload.
//
// These tests use a Manager built the same way the 2.x path builds one, so the
// check is exercised on the real dispatch entry point rather than a copy.

// TestHandleFrameAcceptsMatchingType -- the normal case: header and payload
// agree, so dispatch proceeds past the check.
//
// Uses an UNKNOWN type on purpose. The assertion is about the routing gate,
// not about any handler, and the dispatch switch's default branch only logs --
// whereas a real type (Pong, say) reaches a handler that needs a database this
// test deliberately does not build.
func TestHandleFrameAcceptsMatchingType(t *testing.T) {
	m := &Manager{}
	before := counterValue(t, metrics.FrameTypeMismatchTotal)

	m.HandleFrame("node-1", 0xFE, []byte{0xFE}) // must not panic

	if got := counterValue(t, metrics.FrameTypeMismatchTotal); got != before {
		t.Fatalf("a matching frame was counted as a mismatch")
	}
}

// TestHandleFrameDropsTypeMismatch is the point of the check.
//
// Header says one type, payload says another. Rejecting is the only safe
// choice: whichever value we honoured, the other half of the pipeline would
// use the other.
func TestHandleFrameDropsTypeMismatch(t *testing.T) {
	m := &Manager{}
	before := counterValue(t, metrics.FrameTypeMismatchTotal)

	// "Dropped" must mean dropped, so observe the CONSEQUENCE too, not just the
	// counter. MessagesReceived is incremented immediately after the check, on
	// the way to the dispatch switch, so it distinguishes:
	//   - rejected (correct): neither type is counted as received
	//   - counted but let through: the header type gets counted
	//   - "follow the payload": the payload type gets counted
	//
	// My first version asserted only the mismatch counter, which stayed correct
	// for all three behaviours -- the mutation run caught that the test was
	// weaker than it looked.
	hdrType := frame.MsgTypeName(frame.MsgDataBatch)
	payType := frame.MsgTypeName(frame.MsgHello)
	hdrBefore := counterValue(t, metrics.MessagesReceived.WithLabelValues(hdrType))
	payBefore := counterValue(t, metrics.MessagesReceived.WithLabelValues(payType))

	// header claims DataBatch (0x20), payload claims Hello (0x01)
	m.HandleFrame("node-1", frame.MsgDataBatch, []byte{frame.MsgHello})

	after := counterValue(t, metrics.FrameTypeMismatchTotal)
	if after != before+1 {
		t.Fatalf("type mismatch was not counted: %v -> %v (a dropped frame that is "+
			"not counted is indistinguishable from one that never arrived)", before, after)
	}
	if got := counterValue(t, metrics.MessagesReceived.WithLabelValues(hdrType)); got != hdrBefore {
		t.Errorf("the mismatched frame was DISPATCHED as the header type %s "+
			"(%v -> %v) -- it must be dropped, not routed", hdrType, hdrBefore, got)
	}
	if got := counterValue(t, metrics.MessagesReceived.WithLabelValues(payType)); got != payBefore {
		t.Errorf("the mismatched frame was DISPATCHED as the payload type %s "+
			"(%v -> %v) -- a frame whose two type bytes disagree has no safe "+
			"interpretation", payType, payBefore, got)
	}
}

// TestHandleFrameWithEmptyPayloadIsIgnored -- not a crash, and not a mismatch:
// there is no payload byte to compare.
func TestHandleFrameWithEmptyPayloadIsIgnored(t *testing.T) {
	m := &Manager{}
	before := counterValue(t, metrics.FrameTypeMismatchTotal)
	m.HandleFrame("node-1", frame.MsgPong, nil)
	m.HandleFrame("node-1", frame.MsgPong, []byte{})
	after := counterValue(t, metrics.FrameTypeMismatchTotal)
	if after != before {
		t.Fatalf("an empty payload was counted as a type mismatch (%v -> %v); "+
			"empty and disagreeing are different problems", before, after)
	}
}

// TestFrameHandlerRoutesHeaderTypeToDispatch -- the adapter must pass the
// HEADER type (authoritative for routing), not payload[0].
func TestFrameHandlerRoutesHeaderTypeToDispatch(t *testing.T) {
	m := &Manager{}
	h := m.FrameHandler()
	if h == nil {
		t.Fatal("FrameHandler returned nil")
	}
	before := counterValue(t, metrics.FrameTypeMismatchTotal)

	// Header and payload agree -> no mismatch, routed normally.
	// (Unknown type: only the routing gate is under test here.)
	err := h("node-1", protoframe.Header{Type: 0xFE, PayloadLen: 1}, []byte{0xFE})
	if err != nil {
		t.Fatalf("FrameHandler returned %v; a per-frame problem must not tear "+
			"down a healthy session", err)
	}
	if got := counterValue(t, metrics.FrameTypeMismatchTotal); got != before {
		t.Fatalf("a matching frame was counted as a mismatch")
	}
}

// TestFrameHandlerNeverReturnsError -- an error from OnFrame closes the
// connection. One malformed message from an otherwise healthy device must not
// cost the device its session: the dispatcher already drops and counts bad
// frames individually.
func TestFrameHandlerNeverReturnsError(t *testing.T) {
	m := &Manager{}
	h := m.FrameHandler()

	cases := []struct {
		name    string
		header  uint8
		payload []byte
	}{
		{"mismatched type", frame.MsgDataBatch, []byte{frame.MsgHello}},
		{"empty payload", frame.MsgPong, nil},
		{"unknown type", 0xFE, []byte{0xFE}},
		{"truncated fields", frame.MsgHello, []byte{frame.MsgHello, 0x08}},
	}
	for _, c := range cases {
		if err := h("node-1", protoframe.Header{Type: c.header,
			PayloadLen: uint16(len(c.payload))}, c.payload); err != nil {
			t.Errorf("%s: FrameHandler returned %v, want nil (an error would close "+
				"the device's connection)", c.name, err)
		}
	}
}
