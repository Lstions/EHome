package nodemgr

import (
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// handler_device_op.go -- server side of MsgDeviceOp (0x22) / MsgDeviceOpAck (0x23).
//
// # What this is for
//
// The operator wants two node-level actions from the UI: reboot, and
// factory-reset that KEEPS the WiFi provisioning (so the device comes back on
// the network instead of needing a physical re-provision). The device side
// already implements both policies; this is the server half.
//
// # Why it is node-level and not an edge-device action
//
// The existing commandexec/deviceaction pipeline is built around EDGE devices
// (channel commands, manifests, periph registers). A collector reboot has no
// channel, no manifest and no edge-device id -- forcing it through that pipeline
// would mean inventing values for all three just to satisfy signatures. The
// device_op message exists precisely because this is a different kind of
// operation, so it gets its own small, explicit path.

// SendDeviceOp asks a node to perform an operation and waits for its ACK.
//
// Returns Acked=false (with a non-nil Err) on timeout or on a send failure: in
// both cases the operation may still have HAPPENED, so the caller must not
// report failure as if the device refused. See DeviceOpOutcome.
func (m *Manager) SendDeviceOp(nodeID string, op frame.DeviceOp, timeout time.Duration) (DeviceOpOutcome, error) {
	if m.deviceOps == nil {
		return DeviceOpOutcome{}, errDeviceOpUnsupported
	}
	if nodeID == "" {
		return DeviceOpOutcome{}, errEmptyNodeID
	}
	if timeout <= 0 {
		timeout = m.deviceOpTimeoutOrDefault()
	}
	p, err := m.deviceOps.Begin(nodeID, op, time.Now(), timeout)
	if err != nil {
		// Already in flight: refuse rather than queue. Told apart from a send
		// failure because the operator's next action differs (wait vs retry now).
		metrics.DeviceOpRefusedTotal.WithLabelValues(deviceOpLabel(op)).Inc()
		return DeviceOpOutcome{}, err
	}

	payload, err := frame.EncodeDeviceOp(op, p.RequestID())
	if err != nil {
		m.deviceOps.Fail(nodeID, p.RequestID(), err)
		return DeviceOpOutcome{}, err
	}

	// The downlink goes through the same publisher every other server->device
	// message uses, so a 3.0 node takes TCP and a 2.x node takes MQTT without
	// this code choosing (and therefore without it being able to choose wrong).
	if err := m.mqtt.Publish(mqttDownlinkTopic(nodeID), payload); err != nil {
		m.deviceOps.Fail(nodeID, p.RequestID(), err)
		metrics.DeviceOpSendFailedTotal.WithLabelValues(deviceOpLabel(op)).Inc()
		// A send failure on a topic/transport that CANNOT carry 0x22 to a 2.x
		// device is expected during the migration window; the operator is told
		// the request was not delivered.
		return DeviceOpOutcome{}, err
	}
	metrics.DeviceOpSentTotal.WithLabelValues(deviceOpLabel(op)).Inc()

	// Enforce the deadline HERE.
	//
	// I originally wrote this as a bare `<-p.Done()` and left expiry to
	// "a ticker in production" -- but no ticker was ever wired, so a device that
	// never ACKs (crashed, or a 2.x device that drops 0x22 as an unknown type)
	// blocked the caller FOREVER. Behind an HTTP handler that is one leaked
	// goroutine per click, and the operator never learns anything.
	//
	// The request path now owns its own deadline. ExpireDeviceOps remains for
	// housekeeping and for outcomes nobody is waiting on.
	effective := effectiveDeviceOpTimeout(timeout)
	timer := time.NewTimer(effective)
	defer timer.Stop()

	var out DeviceOpOutcome
	select {
	case out = <-p.Done():
		m.reportDeviceOpOutcome(out)
		return out, nil
	case <-timer.C:
		// Fail() resolves the waiter and frees the single-flight slot, so the
		// operator can retry immediately rather than waiting for housekeeping.
		m.deviceOps.Fail(nodeID, p.RequestID(), fmt.Errorf(
			"device did not acknowledge within %s", effective))
		out = <-p.Done()
		m.reportDeviceOpOutcome(out)
		return out, nil
	}
}

// effectiveDeviceOpTimeout supplies the default when a caller passes <= 0, so
// "no timeout" is not expressible and cannot become an unbounded wait.
func effectiveDeviceOpTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultDeviceOpTimeout
	}
	return d
}

// SetDeviceOpTimeout changes how long a device operation waits for its ACK.
//
// Exists so the wait is tunable rather than hard-coded -- the right value
// depends on the deployment (a slow link wants longer, a UI wants shorter) --
// and so tests do not have to sit through the 15s default.
func (m *Manager) SetDeviceOpTimeout(d time.Duration) {
	m.deviceOpTimeout = d
}

func (m *Manager) deviceOpTimeoutOrDefault() time.Duration {
	if m.deviceOpTimeout <= 0 {
		return DefaultDeviceOpTimeout
	}
	return m.deviceOpTimeout
}

// reportDeviceOpOutcome records and publishes the result.
//
// The three outcomes are deliberately distinct in BOTH the metric and the
// event: "the device said OK", "the device said no (and why)", and "we never
// heard back". Collapsing the third into failure would tell the operator a
// reboot did not happen when the device may be up and running already.
func (m *Manager) reportDeviceOpOutcome(out DeviceOpOutcome) {
	switch {
	case out.Acked && out.Result == frame.DeviceOpOK:
		metrics.DeviceOpAckedTotal.WithLabelValues(deviceOpLabel(out.Op), "ok").Inc()
	case out.Acked:
		metrics.DeviceOpAckedTotal.WithLabelValues(
			deviceOpLabel(out.Op), frame.DeviceOpResultName(out.Result)).Inc()
	default:
		metrics.DeviceOpUnackedTotal.WithLabelValues(deviceOpLabel(out.Op)).Inc()
	}
	if m.wsHub != nil {
		m.wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, map[string]any{
			"node_id":    out.NodeID,
			"op":         deviceOpLabel(out.Op),
			"request_id": out.RequestID,
			"acked":      out.Acked,
			"result":     frame.DeviceOpResultName(out.Result),
			"detail":     out.Detail,
		})
	}
}

// handleDeviceOpAck processes an inbound 0x23.
//
// Returns nothing and never panics: a hostile or buggy device must not be able
// to take the server down by sending a malformed ACK.
func (m *Manager) handleDeviceOpAck(deviceID string, payload []byte) {
	ack, err := frame.DecodeDeviceOpAck(payload)
	if err != nil {
		metrics.DeviceOpBadAckTotal.Inc()
		logger.Warnf("[%s] Malformed device op ACK dropped: %v", deviceID, err)
		return
	}
	if m.deviceOps == nil {
		metrics.DeviceOpBadAckTotal.Inc()
		return
	}
	if !m.deviceOps.Complete(deviceID, ack) {
		// Not an error the operator should see: the request this ACK answers is
		// long gone (expired, or replaced). Counted so a mismatch storm is
		// visible, because it means either a clock/retry bug on the device or a
		// correlation bug here.
		metrics.DeviceOpStaleAckTotal.Inc()
		logger.Warnf("[%s] Device op ACK for unknown/expired request %q (result %s) -- ignored",
			deviceID, ack.RequestID, frame.DeviceOpResultName(ack.Result))
		return
	}
	logger.Infof("[%s] Device op ACK: request=%s result=%s detail=%q",
		deviceID, ack.RequestID, frame.DeviceOpResultName(ack.Result), ack.Detail)
}

// ExpireDeviceOps resolves requests whose ACK never arrived.
//
// Called from a ticker in production (and directly from tests). Kept public so
// the caller owns the cadence; this type deliberately has no goroutine of its
// own, because a background goroutine here would need lifetime management that
// would duplicate the Manager's existing Stop machinery.
func (m *Manager) ExpireDeviceOps(now time.Time) []DeviceOpOutcome {
	if m.deviceOps == nil {
		return nil
	}
	expired := m.deviceOps.Expire(now)
	for _, out := range expired {
		m.reportDeviceOpOutcome(out)
	}
	return expired
}

// PendingDeviceOp reports whether the node already has an operation in flight.
func (m *Manager) PendingDeviceOp(nodeID string) bool {
	if m.deviceOps == nil {
		return false
	}
	return m.deviceOps.PendingFor(nodeID)
}

// DeviceOpSupported reports whether this build can reach a device at all.
//
// False means the whole feature is unavailable, which the API layer must
// surface as such instead of accepting a request it can never deliver.
func (m *Manager) DeviceOpSupported() bool { return m != nil && m.deviceOps != nil }
