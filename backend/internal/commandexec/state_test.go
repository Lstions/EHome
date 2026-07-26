package commandexec

import "testing"

func TestIsTerminal(t *testing.T) {
	terminal := []string{StatusSucceeded, StatusFailed, StatusUnknown, StatusCancelled}
	nonTerminal := []string{StatusQueued, StatusDispatched, StatusDeviceAccepted, StatusVerifying, "", "BOGUS"}
	for _, s := range terminal {
		if !IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = false, want true", s)
		}
	}
	for _, s := range nonTerminal {
		if IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = true, want false", s)
		}
	}
}

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		from, to string
		wantErr  bool
	}{
		// QUEUED transitions
		{StatusQueued, StatusDispatched, false},
		{StatusQueued, StatusCancelled, false},
		{StatusQueued, StatusUnknown, false},
		{StatusQueued, StatusFailed, false},
		{StatusQueued, StatusSucceeded, true},
		{StatusQueued, StatusDeviceAccepted, true},
		{StatusQueued, StatusVerifying, true},
		{StatusQueued, StatusQueued, true},
		// DISPATCHED transitions
		{StatusDispatched, StatusDeviceAccepted, false},
		{StatusDispatched, StatusVerifying, false},
		{StatusDispatched, StatusSucceeded, false},
		{StatusDispatched, StatusFailed, false},
		{StatusDispatched, StatusUnknown, false},
		{StatusDispatched, StatusQueued, true},
		{StatusDispatched, StatusCancelled, true},
		{StatusDispatched, StatusDispatched, true},
		// DEVICE_ACCEPTED transitions
		{StatusDeviceAccepted, StatusVerifying, false},
		{StatusDeviceAccepted, StatusSucceeded, false},
		{StatusDeviceAccepted, StatusFailed, false},
		{StatusDeviceAccepted, StatusUnknown, false},
		{StatusDeviceAccepted, StatusQueued, true},
		{StatusDeviceAccepted, StatusDispatched, true},
		{StatusDeviceAccepted, StatusCancelled, true},
		// VERIFYING transitions
		{StatusVerifying, StatusSucceeded, false},
		{StatusVerifying, StatusFailed, false},
		{StatusVerifying, StatusUnknown, false},
		{StatusVerifying, StatusQueued, true},
		{StatusVerifying, StatusDispatched, true},
		{StatusVerifying, StatusDeviceAccepted, true},
		{StatusVerifying, StatusCancelled, true},
		// Terminal states cannot transition
		{StatusSucceeded, StatusFailed, true},
		{StatusFailed, StatusSucceeded, true},
		{StatusUnknown, StatusSucceeded, true},
		{StatusCancelled, StatusQueued, true},
		// Unknown source state
		{"BOGUS", StatusSucceeded, true},
		{"", StatusDispatched, true},
	}
	for _, tt := range tests {
		err := validateTransition(tt.from, tt.to)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateTransition(%q, %q) err=%v, wantErr=%v", tt.from, tt.to, err, tt.wantErr)
		}
	}
}
