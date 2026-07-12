package nodemgr

import (
	"testing"

	"ehome/backend/pkg/frame"
)

func TestParseLogEntry_ESP32SubFrameWithoutMessageType(t *testing.T) {
	// ESP32 nested entries are raw field sequences; they must not contain a
	// top-level message-type byte. This verifies the exact decoder contract.
	sub := frame.SubEncoder()
	sub.EncodeVarint(1, 2)
	sub.EncodeVarint(2, 1234567)
	sub.EncodeString(3, "CALLBACK")
	sub.EncodeString(4, "remote log stream enabled, level=2")

	got := parseLogEntry(sub.Bytes())
	if got == nil {
		t.Fatal("parseLogEntry returned nil")
	}
	if got.Level != 2 || got.Ts != 1234567 || got.Tag != "CALLBACK" || got.Message != "remote log stream enabled, level=2" {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestParseLogEntry_RejectsEmptySubFrame(t *testing.T) {
	if got := parseLogEntry(nil); got != nil {
		t.Fatalf("parseLogEntry(nil) = %+v, want nil", got)
	}
}
