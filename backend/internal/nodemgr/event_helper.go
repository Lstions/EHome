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
		// S5 (2026-10-07): this branch used to be unreachable — Publish dropped
		// the event and returned nil, so a full buffer looked like success.
		//
		// Why this caller cannot REPORT the drop upwards: it is called by ~20 CRUD
		// handlers AFTER their DB write has already committed, and it is a
		// fire-and-forget helper with no return value. Failing the HTTP response
		// would be wrong (the write did succeed) and changing the signature would
		// ripple through every handler for no gain. So it is kept, but is no
		// longer silent: the drop is logged with its consequence.
		//
		// The consequences are bounded and self-healing: the entity is already
		// persisted, and SyncGate re-evaluates config on the next Hello /
		// StatusReport of the affected node. The worst case is a delayed push,
		// not a lost write.
		logger.Warnf("EmitConfigChange DROPPED config event (bus full): %v (type=%s action=%s node=%s entity=%s event_id=%s); device will pick it up on its next Hello/StatusReport",
			err, t, a, evt.NodeID, evt.EntityID, evt.EventID)
	}
}
