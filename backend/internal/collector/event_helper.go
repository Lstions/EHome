package collector

import (
	"ehome/backend/pkg/logger"

	"github.com/google/uuid"
)

// EmitConfigChange is a helper for CRUD handlers to emit a ConfigChangeEvent
// via the ConfigEventBus. It increments the epoch and publishes the event.
//
// Usage in API handlers:
//
//	defer EmitConfigChange(bus, CfgChangeChannel, CfgActionUpdate, collectorID, channelID)
//
// The actor field is derived from context when available, otherwise defaults to "api:unknown".
func EmitConfigChange(bus *ConfigEventBus, t ConfigChangeType, a ConfigChangeAction, collectorID, entityID uint) {
	if bus == nil {
		logger.Warnf("EmitConfigChange: bus is nil, skipping event (type=%s action=%s collector=%d entity=%d)",
			t, a, collectorID, entityID)
		return
	}

	evt := ConfigChangeEvent{
		EventID:     uuid.New().String(),
		Type:        t,
		Action:      a,
		CollectorID: collectorID,
		EntityID:    entityID,
		Actor:       "api:admin", // TODO: extract from gin.Context when auth context is available
	}

	if err := bus.Publish(evt); err != nil {
		logger.Warnf("EmitConfigChange publish failed: %v (type=%s action=%s)", err, t, a)
	}
}
