package nodemgr

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// EmitConfigChange is a helper for CRUD handlers to emit a ConfigChangeEvent
// via the ConfigEventBus. It increments the epoch and publishes the event.
//
// Usage in API handlers:
//
//	defer EmitConfigChange(ctx, bus, CfgChangeNode, CfgActionUpdate, nodeID, channelID)
//
// The actor field is derived from gin.Context when available, otherwise defaults to "system".
func EmitConfigChange(ctx *gin.Context, bus *ConfigEventBus, t ConfigChangeType, a ConfigChangeAction, nodeID string, entityID string) {
	if bus == nil {
		logger.Warnf("EmitConfigChange: bus is nil, skipping event (type=%s action=%s node=%d entity=%d)",
			t, a, nodeID, entityID)
		return
	}

	actor := "system"
	if ctx != nil {
		if uid, exists := ctx.Get("user_id"); exists {
			actor = fmt.Sprintf("api:%v", uid)
		}
	}

	evt := ConfigChangeEvent{
		EventID:  uuid.New().String(),
		Type:     t,
		Action:   a,
		NodeID:   fmt.Sprint(nodeID),
		EntityID: fmt.Sprint(entityID),
		Actor:    actor,
	}

	if err := bus.Publish(evt); err != nil {
		logger.Warnf("EmitConfigChange publish failed: %v (type=%s action=%s)", err, t, a)
	}
}
