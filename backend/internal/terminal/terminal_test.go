package terminal

import (
	"testing"
)

func TestNewChannelTerminal(t *testing.T) {
	ct := NewChannelTerminal()
	if ct == nil {
		t.Fatal("expected non-nil terminal")
	}
	if ct.Count() != 0 {
		t.Fatalf("expected 0 entries, got %d", ct.Count())
	}
}

func TestAppendAndHistory(t *testing.T) {
	ct := NewChannelTerminal()

	// Append 3 entries
	ct.Append(DirectionTX, []byte{0x01, 0x02})
	ct.Append(DirectionRX, []byte{0x03, 0x04, 0x05})
	ct.Append(DirectionTX, []byte{0x06})

	if ct.Count() != 3 {
		t.Fatalf("expected 3 entries, got %d", ct.Count())
	}

	// Get all history
	entries := ct.History(10)
	if len(entries) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(entries))
	}

	// Verify order (oldest first)
	if entries[0].Direction != "tx" {
		t.Errorf("entry[0] direction: expected tx, got %s", entries[0].Direction)
	}
	if entries[1].Direction != "rx" {
		t.Errorf("entry[1] direction: expected rx, got %s", entries[1].Direction)
	}
	if entries[2].Direction != "tx" {
		t.Errorf("entry[2] direction: expected tx, got %s", entries[2].Direction)
	}

	// Verify hex encoding
	if entries[0].DataHex != "0102" {
		t.Errorf("entry[0] hex: expected 0102, got %s", entries[0].DataHex)
	}
	if entries[0].Length != 2 {
		t.Errorf("entry[0] length: expected 2, got %d", entries[0].Length)
	}
}

func TestRingBufferOverflow(t *testing.T) {
	ct := NewChannelTerminal()
	// ringBufferSize = 256, write 300 entries
	for i := 0; i < 300; i++ {
		ct.Append(DirectionTX, []byte{byte(i % 256)})
	}

	// Should cap at 256
	if ct.Count() != 256 {
		t.Fatalf("expected 256 entries after overflow, got %d", ct.Count())
	}

	// Last 10 entries should be most recent
	entries := ct.History(10)
	if len(entries) != 10 {
		t.Fatalf("expected 10 entries, got %d", len(entries))
	}

	// Most recent entry should be byte(299 % 256) = 43
	last := entries[9]
	if last.DataHex != "2b" { // 0x2b = 43
		t.Errorf("last entry hex: expected 2b, got %s", last.DataHex)
	}
}

func TestHistoryLimit(t *testing.T) {
	ct := NewChannelTerminal()
	for i := 0; i < 10; i++ {
		ct.Append(DirectionTX, []byte{byte(i)})
	}

	// Request more than available
	entries := ct.History(100)
	if len(entries) != 10 {
		t.Fatalf("expected 10 entries (capped), got %d", len(entries))
	}

	// Request less than available
	entries = ct.History(3)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestSafeASCII(t *testing.T) {
	tests := []struct {
		input    []byte
		expected string
	}{
		{[]byte{0x41, 0x42, 0x43}, "ABC"},           // printable
		{[]byte{0x01, 0x02, 0x03}, "..."},            // non-printable
		{[]byte{0x48, 0x00, 0x65}, "H.e"},            // mixed
		{[]byte{}, ""},                                // empty
		{[]byte{0x7F}, "."},                           // DEL
		{[]byte{0x20, 0x7E}, " ~"},                    // boundary
	}

	for _, tt := range tests {
		result := safeASCII(tt.input)
		if result != tt.expected {
			t.Errorf("safeASCII(%x): expected %q, got %q", tt.input, tt.expected, result)
		}
	}
}

func TestManagerRecordAndGetHistory(t *testing.T) {
	mgr := NewManager()

	// Record TX/RX for channel 1
	mgr.RecordTX("device1", 1, []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x84, 0x0A})
	mgr.RecordRX("device1", 1, []byte{0x01, 0x03, 0x02, 0x00, 0x5A})

	entries := mgr.GetHistory(1, 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// First should be TX
	if entries[0].Direction != "tx" {
		t.Errorf("entry[0]: expected tx, got %s", entries[0].Direction)
	}
	// Second should be RX
	if entries[1].Direction != "rx" {
		t.Errorf("entry[1]: expected rx, got %s", entries[1].Direction)
	}
}

func TestManagerGetHistoryNonExistent(t *testing.T) {
	mgr := NewManager()
	entries := mgr.GetHistory(999, 10)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for non-existent channel, got %d", len(entries))
	}
}

func TestManagerEvents(t *testing.T) {
	mgr := NewManager()

	// Record an event
	mgr.RecordTX("device1", 1, []byte{0xAA, 0xBB})

	// Read from events channel
	select {
	case evt := <-mgr.Events():
		if evt.DeviceID != "device1" {
			t.Errorf("event device_id: expected device1, got %s", evt.DeviceID)
		}
		if evt.ChannelID != 1 {
			t.Errorf("event channel_id: expected 1, got %d", evt.ChannelID)
		}
		if evt.Direction != "tx" {
			t.Errorf("event direction: expected tx, got %s", evt.Direction)
		}
		if evt.DataHex != "aabb" {
			t.Errorf("event data_hex: expected aabb, got %s", evt.DataHex)
		}
	default:
		t.Fatal("expected event on Events channel")
	}
}

func TestDirectionString(t *testing.T) {
	if directionString(DirectionTX) != "tx" {
		t.Error("DirectionTX should be 'tx'")
	}
	if directionString(DirectionRX) != "rx" {
		t.Error("DirectionRX should be 'rx'")
	}
}
