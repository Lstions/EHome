package nodemgr

import (
	"errors"

	"ehome/backend/pkg/frame"
)

// device_op_glue.go -- the small pieces that connect handler_device_op.go to
// the rest of the manager.

var (
	// errDeviceOpUnsupported is returned when the manager was built without a
	// tracker (older construction paths). The API layer turns this into
	// "this server cannot do that" rather than a generic failure.
	errDeviceOpUnsupported = errors.New("this server build cannot send device operations")

	errEmptyNodeID = errors.New("device operation requires a node id")
)

// deviceOpLabel turns an op into a bounded metric label.
//
// A free-form label would let a bad value create unbounded metric series. The
// label is derived from the enum, and anything unrecognised is reported AS
// its number so a new op cannot silently appear as a zero-value label.
func deviceOpLabel(op frame.DeviceOp) string {
	switch op {
	case frame.DeviceOpReboot:
		return "reboot"
	case frame.DeviceOpFactoryResetKeepConn:
		return "factory_reset"
	default:
		return "unknown"
	}
}
