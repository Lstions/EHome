package collector

import (
	"time"

	"ehome/backend/pkg/logger"

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
	NodeID    uint   // affected node (0 = all / unknown)
	EntityID  uint   // changed entity ID
	Epoch     uint64 // global epoch after increment
	Timestamp time.Time
	Actor     string // "api:admin", "init:factory_reset", "system:startup"
}

// ConfigEventBus is a simple channel-based event bus for configuration changes.
// Single subscriber model: only SyncGate subscribes.
type ConfigEventBus struct {
	ch       chan ConfigChangeEvent
	epochGen *EpochGenerator
}

// NewConfigEventBus creates a new bus with the given buffer size.
func NewConfigEventBus(bufferSize int, epochGen *EpochGenerator) *ConfigEventBus {
	return &ConfigEventBus{
		ch:       make(chan ConfigChangeEvent, bufferSize),
		epochGen: epochGen,
	}
}

// Publish increments the epoch and sends the event to the bus channel.
// If the channel is full, the event is dropped with a warning (non-blocking).
func (b *ConfigEventBus) Publish(evt ConfigChangeEvent) error {
	// Increment epoch on every publish
	evt.Epoch = b.epochGen.Next()

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
		logger.Warnf("ConfigEventBus buffer full, dropping event: type=%s action=%s node=%d entity=%d",
			evt.Type, evt.Action, evt.NodeID, evt.EntityID)
		return nil // drop silently per design — alarm via metrics in production
	}
}

// Subscribe returns a read-only channel for consuming events.
// Only one subscriber (SyncGate) is expected.
func (b *ConfigEventBus) Subscribe() <-chan ConfigChangeEvent {
	return b.ch
}

// CurrentEpoch returns the current global epoch value.
func (b *ConfigEventBus) CurrentEpoch() uint64 {
	return b.epochGen.Current()
}
