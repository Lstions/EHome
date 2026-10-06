package api

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// L-09: the collector's bus_config slot is 64 bytes and its manifest decoder is
// fail-closed for the WHOLE manifest when a field overflows (config_mgr.c:342-346).
// So an over-long USB bus_config does not just drop one channel - it makes every
// template/pin/command in that manifest undeliverable while the backend reports
// success. These tests pin the bound at the only place that produces the value.
func TestUSBChannelBusConfigRejectsOverLongValue(t *testing.T) {
	// 65 bytes == 130 hex chars -> must be rejected (firmware slot is 64).
	over := strings.Repeat("ab", 65)
	ch := models.Channel{BusType: "USB", HardwareType: "USB", BusConfig: over}

	_, err := channelRoutePins(ch)
	if err == nil {
		t.Fatalf("65-byte USB bus_config must be rejected: the firmware slot is 64 bytes "+
			"and its decoder rejects the entire manifest on overflow (got %d bytes accepted)", len(over)/2)
	}
	if !strings.Contains(err.Error(), "64") {
		t.Fatalf("error should name the 64-byte limit so the operator can act on it, got: %v", err)
	}
}

func TestUSBChannelBusConfigAcceptsAtLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
	}{
		{"empty", 0},
		{"typical 2-byte route", 2},
		{"exactly 64 (the firmware slot size)", 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := models.Channel{
				BusType: "USB", HardwareType: "USB",
				BusConfig: strings.Repeat("ab", tc.bytes),
			}
			if _, err := channelRoutePins(ch); err != nil {
				t.Fatalf("%d-byte USB bus_config must be accepted (firmware accepts <=64), got: %v", tc.bytes, err)
			}
		})
	}
}

// A UART/SPI value that exceeds the firmware slot must keep being handled by its
// own branch - the USB bound must not leak into other bus types.
func TestUSBBoundDoesNotLeakToOtherBusTypes(t *testing.T) {
	ch := models.Channel{BusType: "SPI", HardwareType: "SPI", BusConfig: strings.Repeat("ab", 65)}
	if _, err := channelRoutePins(ch); err == nil {
		t.Fatal("SPI with 65 bytes must still be rejected by the SPI branch (needs exactly 9)")
	} else if !strings.Contains(err.Error(), "SPI") {
		t.Fatalf("SPI rejection must come from the SPI branch, not the USB bound, got: %v", err)
	}
}
