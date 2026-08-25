package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"

	"gorm.io/gorm"
)

// Periph wire constants. They mirror the protocol enums owned by
// api/handler_periph.go (PeriphCmd field 2/4) and must stay in sync with the
// ESP32 implementation; commandexec deliberately does not import the api
// package.
const (
	periphTypeGPIO uint8 = 1
	periphTypePWM  uint8 = 2

	gpioActionSetLow  uint8 = 0
	gpioActionSetHigh uint8 = 1

	pwmActionSetDuty uint8 = 0
)

// periphRequestID is the independent PeriphCmd request-id counter used by
// PeriphTransport (裁决: 独立计数器, 不侵入 nodemgr 的 periphPending 内部
// 状态). Seeded once from time so a restart does not trivially collide with
// ids issued by the previous process.
var periphRequestID = uint32(time.Now().UnixNano())

func nextPeriphRequestID() uint32 {
	return atomic.AddUint32(&periphRequestID, 1)
}

// PeriphTransport is the production transport for node-level peripheral
// actions (gpio_set / pwm_set_duty). It compiles canonical params into a
// PeriphCmd (0x1B) frame and publishes it on the node control topic. The
// PeriphRsp confirmation is correlated by request_id inside nodemgr and
// surfaced as an observation event; this transport intentionally does not
// touch nodemgr's periphPending map.
type PeriphTransport struct {
	db      *gorm.DB
	mqtt    mqtt.Publisher
	actions *deviceaction.Registry
	now     func() time.Time
}

// NewPeriphTransport wires the periph_cmd transport with the same DB and MQTT
// publisher used by the ChannelCmdV2 transport.
func NewPeriphTransport(db *gorm.DB, publisher mqtt.Publisher, actions *deviceaction.Registry) *PeriphTransport {
	return &PeriphTransport{db: db, mqtt: publisher, actions: actions, now: func() time.Time { return time.Now().UTC() }}
}

// Dispatch implements Transport. It reads the GPIO/PWM config through the
// service DB handle; the dispatcher may hand us a transaction handle via
// DispatchInTransaction for read consistency with the state transition.
func (t *PeriphTransport) Dispatch(ctx context.Context, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil || t.db == nil || t.mqtt == nil || t.actions == nil {
		return DispatchResult{}, fmt.Errorf("periph transport is unavailable")
	}
	return t.dispatch(ctx, t.db, execution, attempt)
}

// DispatchInTransaction keeps config reads inside the dispatcher's state
// transition transaction (same semantics as ChannelCmdV2Transport).
func (t *PeriphTransport) DispatchInTransaction(ctx context.Context, tx *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil || tx == nil || t.mqtt == nil || t.actions == nil {
		return DispatchResult{}, fmt.Errorf("periph transport is unavailable")
	}
	return t.dispatch(ctx, tx, execution, attempt)
}

func (t *PeriphTransport) dispatch(ctx context.Context, db *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	definition, ok := t.actions.Get(execution.DeviceType, execution.ActionID)
	if !ok || !definition.Enabled || definition.Version != execution.ActionVersion || definition.Transport != deviceaction.PeriphCmdAdapter {
		return DispatchResult{}, fmt.Errorf("trusted periph action definition is unavailable")
	}
	if !deviceaction.CurrentEngineAllows(definition) {
		return DispatchResult{}, fmt.Errorf("action requires the future high-risk command engine")
	}
	params, err := deviceaction.CanonicalizeParams(definition.InputSchema, json.RawMessage(execution.ParamsJSON))
	if err != nil {
		return DispatchResult{}, fmt.Errorf("persisted action parameters are invalid: %w", err)
	}

	var periphType, resourceID, action uint8
	var value uint32

	switch execution.ActionID {
	case deviceaction.ActionGPIOSet:
		var p struct {
			Pin   int `json:"pin"`
			Level int `json:"level"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return DispatchResult{}, fmt.Errorf("decode gpio params: %w", err)
		}
		var cfg models.GPIOConfig
		if err := db.WithContext(ctx).Where("node_id = ? AND pin = ?", execution.NodeID, p.Pin).First(&cfg).Error; err != nil {
			return DispatchResult{}, fmt.Errorf("gpio config for pin %d: %w", p.Pin, err)
		}
		if !cfg.Enabled {
			return DispatchResult{}, fmt.Errorf("gpio config for pin %d is disabled", p.Pin)
		}
		periphType = periphTypeGPIO
		resourceID = uint8(p.Pin)
		if p.Level == 1 {
			action = gpioActionSetHigh
		} else {
			action = gpioActionSetLow
		}
	case deviceaction.ActionPWMSetDuty:
		var p struct {
			HardwareID string `json:"hardware_id"`
			Duty       int    `json:"duty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return DispatchResult{}, fmt.Errorf("decode pwm params: %w", err)
		}
		var cfg models.PWMConfig
		if err := db.WithContext(ctx).Where("node_id = ? AND hardware_id = ?", execution.NodeID, p.HardwareID).First(&cfg).Error; err != nil {
			return DispatchResult{}, fmt.Errorf("pwm config for hardware_id %q: %w", p.HardwareID, err)
		}
		if !cfg.Enabled {
			return DispatchResult{}, fmt.Errorf("pwm config for hardware_id %q is disabled", p.HardwareID)
		}
		periphType = periphTypePWM
		resourceID = cfg.Channel
		action = pwmActionSetDuty
		value = uint32(p.Duty)
	default:
		return DispatchResult{}, fmt.Errorf("unknown periph action %q", execution.ActionID)
	}

	requestID := nextPeriphRequestID()
	enc := frame.NewEncoder(frame.MsgPeriphCmd)
	enc.EncodeVarint(1, uint64(requestID))  // field 1: request_id
	enc.EncodeVarint(2, uint64(periphType)) // field 2: periph_type
	enc.EncodeVarint(3, uint64(resourceID)) // field 3: resource_id
	enc.EncodeVarint(4, uint64(action))     // field 4: action
	if value > 0 {
		enc.EncodeVarint(5, uint64(value)) // field 5: value (optional)
	}
	payload := enc.Bytes()

	if err := t.mqtt.Publish(mqtt.ControlTopicForNode(execution.NodeID), payload); err != nil {
		return DispatchResult{}, fmt.Errorf("publish PeriphCmd: %w", err)
	}
	return DispatchResult{PublishedAt: t.now(), WireDigest: periphWireDigest(execution, attempt.AttemptNo, payload)}, nil
}

// periphWireDigest binds the wire identity to the logical request plus the
// exact published frame bytes, so an outbox retry re-publishing the identical
// frame keeps the same digest while any param drift is detectable.
func periphWireDigest(execution models.CommandExecution, attemptNo uint32, payload []byte) string {
	material := fmt.Sprintf("ehome.periph-cmd\x00%s\x00%s\x00%d\x00%s\x00%d\x00%x",
		execution.CommandID, execution.ActionID, execution.ActionVersion, execution.RequestHash, attemptNo, payload)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

// MultiTransport routes each dispatch to the transport matching the action
// definition's Transport field (裁决: Dispatcher transport = MultiTransport).
// A nil sub-transport fails closed for its adapter family.
type MultiTransport struct {
	channelCmdV2 *ChannelCmdV2Transport
	periph       *PeriphTransport
}

// NewMultiTransport builds the composition-root router. Either sub-transport
// may be nil; dispatch for that adapter then fails closed.
func NewMultiTransport(channelCmdV2 *ChannelCmdV2Transport, periph *PeriphTransport) *MultiTransport {
	return &MultiTransport{channelCmdV2: channelCmdV2, periph: periph}
}

// DispatchInTransaction routes by action ID inside the dispatcher's
// transaction. Both sub-transports are transaction-aware, so MultiTransport
// must be too — otherwise the dispatcher would fall back to plain Dispatch
// and the config reads would leave the state-transition transaction.
func (t *MultiTransport) DispatchInTransaction(ctx context.Context, tx *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil {
		return DispatchResult{}, fmt.Errorf("multi transport is unavailable")
	}
	if deviceaction.IsPeriphAction(execution.ActionID) {
		if t.periph == nil {
			return DispatchResult{}, fmt.Errorf("periph transport is unavailable")
		}
		return t.periph.DispatchInTransaction(ctx, tx, execution, attempt)
	}
	if t.channelCmdV2 == nil {
		return DispatchResult{}, fmt.Errorf("ChannelCmdV2 transport is unavailable")
	}
	return t.channelCmdV2.DispatchInTransaction(ctx, tx, execution, attempt)
}

// Dispatch routes by action ID using the sub-transports' own DB handles.
func (t *MultiTransport) Dispatch(ctx context.Context, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil {
		return DispatchResult{}, fmt.Errorf("multi transport is unavailable")
	}
	if deviceaction.IsPeriphAction(execution.ActionID) {
		if t.periph == nil {
			return DispatchResult{}, fmt.Errorf("periph transport is unavailable")
		}
		return t.periph.Dispatch(ctx, execution, attempt)
	}
	if t.channelCmdV2 == nil {
		return DispatchResult{}, fmt.Errorf("ChannelCmdV2 transport is unavailable")
	}
	return t.channelCmdV2.Dispatch(ctx, execution, attempt)
}
