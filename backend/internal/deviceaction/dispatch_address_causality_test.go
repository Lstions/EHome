package deviceaction

import (
	"strings"
	"testing"
)

// TestDispatchPathRejectsIncidentValueAndAcceptsLegalAddresses is the causal
// regression for the 2026-09-20 incident, driven through the SAME entry point
// dispatch uses (Definition.CompileForAddress -> ParseHardwareAddress), not
// through the parser alone.
func TestDispatchPathRejectsIncidentValueAndAcceptsLegalAddresses(t *testing.T) {
	registry := NewBuiltInRegistry(nil)
	def, ok := registry.Get("sn3001_rain", "read_rainfall")
	if !ok {
		t.Fatal("sn3001_rain/read_rainfall is missing")
	}
	if !def.RequiresTargetAddress() {
		t.Fatal("read_rainfall must be address-bound; otherwise this test proves nothing")
	}

	// The production value is rejected by the dispatch compiler, and the error
	// names the action so the log line matches the incident report.
	if _, err := def.CompileForAddress([]byte("{}"), "UART1"); err == nil {
		t.Fatal("dispatch must reject the incident value UART1")
	} else if !strings.Contains(err.Error(), "read_rainfall") {
		t.Fatalf("dispatch error %q must name the action", err.Error())
	}

	// Legal addresses compile, and the compiled frame embeds the unit id.
	for _, tc := range []struct {
		hardwareID string
		wantUnit   byte
	}{
		{"1", 0x01},
		{"0x01", 0x01},
		{"3", 0x03},
		{"254", 0xFE},
		{"", 0x01},  // legacy default
		{"0", 0x01}, // legacy default
		{" 7 ", 0x07},
	} {
		step, err := def.CompileForAddress([]byte("{}"), tc.hardwareID)
		if err != nil {
			t.Fatalf("CompileForAddress(%q) must succeed, got %v", tc.hardwareID, err)
		}
		if len(step.TXData) < 2 {
			t.Fatalf("CompileForAddress(%q) produced a frame too short to carry a unit id: %x", tc.hardwareID, step.TXData)
		}
		if step.TXData[0] != tc.wantUnit {
			t.Fatalf("CompileForAddress(%q) frame unit id = 0x%02X, want 0x%02X (frame %x)", tc.hardwareID, step.TXData[0], tc.wantUnit, step.TXData)
		}
	}
}

// TestIllegalAddressIsNeverTheDefaultSilently: an illegal value must not be
// quietly normalized into the legacy default address 1. That silent fallback is
// what made the incident confusing (the device would have been addressed as
// unit 1, i.e. possibly a DIFFERENT physical device).
func TestIllegalAddressIsNeverTheDefaultSilently(t *testing.T) {
	if _, err := ParseHardwareAddress("UART1"); err == nil {
		t.Fatal("a bus name must be an error, never a silent default address")
	}
	verdict, err := ParseHardwareAddress("I2C0")
	if err == nil || verdict != 0 {
		t.Fatalf("I2C0 must fail with a zero address, got address=%d err=%v", verdict, err)
	}
}
