package commandexec

import "fmt"

const (
	StatusQueued         = "QUEUED"
	StatusDispatched     = "DISPATCHED"
	StatusDeviceAccepted = "DEVICE_ACCEPTED"
	StatusVerifying      = "VERIFYING"
	StatusSucceeded      = "SUCCEEDED"
	StatusFailed         = "FAILED"
	StatusUnknown        = "UNKNOWN"
	StatusCancelled      = "CANCELLED"
)

func IsTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusUnknown, StatusCancelled:
		return true
	}
	return false
}

func validateTransition(from, to string) error {
	if IsTerminal(from) {
		return fmt.Errorf("terminal execution cannot transition from %s", from)
	}
	allowed := map[string]map[string]bool{
		StatusQueued:         {StatusDispatched: true, StatusCancelled: true, StatusUnknown: true, StatusFailed: true},
		StatusDispatched:     {StatusDeviceAccepted: true, StatusVerifying: true, StatusSucceeded: true, StatusFailed: true, StatusUnknown: true},
		StatusDeviceAccepted: {StatusVerifying: true, StatusSucceeded: true, StatusFailed: true, StatusUnknown: true},
		StatusVerifying:      {StatusSucceeded: true, StatusFailed: true, StatusUnknown: true},
	}
	if !allowed[from][to] {
		return fmt.Errorf("invalid execution transition %s -> %s", from, to)
	}
	return nil
}
