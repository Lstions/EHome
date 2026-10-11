package nodemgr

import (
	"errors"
	"time"

	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/google/uuid"
)

// ConfigChangeType identifies the type of configuration entity that changed.
type ConfigChangeType string

const (
	CfgChangeTemplate     ConfigChangeType = "template"
	CfgChangeChannel      ConfigChangeType = "channel"
	CfgChangeEdgeDevice   ConfigChangeType = "edge_device"
	CfgChangeNode         ConfigChangeType = "node"
	CfgChangeDeviceConfig ConfigChangeType = "device_config"
	CfgChangeGPIO         ConfigChangeType = "gpio"
	CfgChangePWM          ConfigChangeType = "pwm"
)

// ConfigChangeAction identifies the CRUD action performed.
type ConfigChangeAction string

const (
	CfgActionCreate ConfigChangeAction = "create"
	CfgActionUpdate ConfigChangeAction = "update"
	CfgActionDelete ConfigChangeAction = "delete"
)

// ConfigChangeEvent represents a single configuration change event on the bus.
type ConfigChangeEvent struct {
	EventID   string           // UUID v4
	Type      ConfigChangeType // template / channel / device / device_config
	Action    ConfigChangeAction
	NodeID    string // affected node (0 = all / unknown)
	EntityID  string // changed entity ID
	Timestamp time.Time
	Actor     string // "api:admin", "init:factory_reset", "system:startup"
}

// ConfigEventBus is a simple channel-based event bus for configuration changes.
// Single subscriber model: only SyncGate subscribes.
type ConfigEventBus struct {
	ch chan ConfigChangeEvent
}

// NewConfigEventBus creates a new bus with the given buffer size.
func NewConfigEventBus(bufferSize int) *ConfigEventBus {
	return &ConfigEventBus{
		ch: make(chan ConfigChangeEvent, bufferSize),
	}
}

// ErrConfigEventBusFull reports that Publish could not enqueue the event
// because the bus buffer was full, and the event was therefore DROPPED.
//
// S5 (2026-10-07): before this sentinel existed, Publish dropped the event and
// still returned nil, so every caller's error branch was dead code — a full
// buffer was indistinguishable from a successful publish. Publish stays
// non-blocking (the hot path must not stall on a slow subscriber); it now
// REPORTS the drop instead of hiding it.
//
// Callers may use errors.Is(err, ErrConfigEventBusFull) to distinguish "the
// event was dropped" from any future failure mode.
var ErrConfigEventBusFull = errors.New("ConfigEventBus buffer full: event dropped")

// Publish sends the event to the bus channel.
//
// Non-blocking by design: a full buffer never stalls the caller. That drop is
// reported as ErrConfigEventBusFull (see above) rather than swallowed.
func (b *ConfigEventBus) Publish(evt ConfigChangeEvent) error {
	if evt.EventID == "" {
		evt.EventID = uuid.New().String()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	select {
	case b.ch <- evt:
		return nil
	default:
		// Log + metric stay: the log carries the event identity for triage, the
		// metric drives alerting. The returned error is what lets callers
		// actually act (retry, degrade, or surface it) instead of assuming
		// success.
		logger.Warnf("ConfigEventBus buffer full, dropping event: type=%s action=%s node=%s entity=%s event_id=%s",
			evt.Type, evt.Action, evt.NodeID, evt.EntityID, evt.EventID)
		metrics.EventBusDroppedTotal.Inc()
		return ErrConfigEventBusFull
	}
}

// Subscribe returns a read-only channel for consuming events.
// Only one subscriber (SyncGate) is expected.
func (b *ConfigEventBus) Subscribe() <-chan ConfigChangeEvent {
	return b.ch
}

// CurrentEpoch returns the current global epoch value.
// Retained for backward compatibility (API handlers); always returns 0 in v2.
func (b *ConfigEventBus) CurrentEpoch() uint64 {
	return 0
}
