package deviceaction

import "fmt"

// Built-in node-level peripheral actions (GPIO/PWM) transported by the
// PeriphCmd (0x1B) protocol frame instead of ChannelCmdV2.
//
// Peripherals are node-level resources: they have no EdgeDevice row, no
// channel, no manifest and no capability report, so these definitions carry
// no compiler/verifier — the frame is constructed by commandexec's
// PeriphTransport from canonical params, and confirmation is the PeriphRsp
// observation event (Verification="observation").

// Action IDs.
const (
	ActionGPIOSet    = "gpio_set"
	ActionPWMSetDuty = "pwm_set_duty"
)

// Peripheral device types (Registry keys).
const (
	DeviceTypeGPIO = "gpio"
	DeviceTypePWM  = "pwm"
)

// periphDefinition returns the built-in Definition for id, or false.
// Exported metadata (ID/Version/DeviceType/Semantics/Risk/ExecutionShape/
// Verification/Transport/InputSchema) is declarative data; execution fields
// stay empty because PeriphTransport owns frame construction.
func periphDefinition(id string) (Definition, bool) {
	switch id {
	case ActionGPIOSet:
		minPin, maxPin := 0.0, 255.0
		minLevel, maxLevel := 0.0, 1.0
		return Definition{
			ID:             ActionGPIOSet,
			Version:        1,
			Name:           "GPIO 电平设置",
			Description:    "设置节点 GPIO 引脚输出电平 (PeriphCmd 0x1B, action=SetLow/SetHigh)",
			DeviceType:     DeviceTypeGPIO,
			Semantics:      "set",
			Risk:           "medium",
			ExecutionShape: "single",
			Verification:   "observation",
			Transport:      PeriphCmdAdapter,
			Enabled:        true,
			InputSchema: ParameterSchema{
				Properties: map[string]Parameter{
					"pin":   {Type: "integer", Minimum: &minPin, Maximum: &maxPin},
					"level": {Type: "integer", Minimum: &minLevel, Maximum: &maxLevel},
				},
				Required: []string{"pin", "level"},
			},
		}, true
	case ActionPWMSetDuty:
		minDuty, maxDuty := 0.0, 10000.0
		minHwLen, maxHwLen := uint32(1), uint32(16)
		return Definition{
			ID:             ActionPWMSetDuty,
			Version:        1,
			Name:           "PWM 占空比设置",
			Description:    "设置节点 PWM 通道占空比 (PeriphCmd 0x1B, action=SetDuty, duty 0-10000 = 0.00%-100.00%)",
			DeviceType:     DeviceTypePWM,
			Semantics:      "set",
			Risk:           "medium",
			ExecutionShape: "single",
			Verification:   "observation",
			Transport:      PeriphCmdAdapter,
			Enabled:        true,
			InputSchema: ParameterSchema{
				Properties: map[string]Parameter{
					"hardware_id": {Type: "string", MinLength: &minHwLen, MaxLength: &maxHwLen},
					"duty":        {Type: "integer", Minimum: &minDuty, Maximum: &maxDuty},
				},
				Required: []string{"hardware_id", "duty"},
			},
		}, true
	default:
		return Definition{}, false
	}
}

// RegisterPeriphActions registers the built-in GPIO/PWM actions on r.
// It is called at the end of NewBuiltInRegistry; registering twice (or after
// a manual duplicate) returns an error so composition-root wiring stays
// fail-closed.
func RegisterPeriphActions(r *Registry) error {
	for _, id := range []string{ActionGPIOSet, ActionPWMSetDuty} {
		def, ok := periphDefinition(id)
		if !ok {
			return fmt.Errorf("unknown built-in periph action %q", id)
		}
		if err := r.Register(def); err != nil {
			return err
		}
	}
	return nil
}

// IsPeriphAction reports whether actionID is a built-in periph_cmd action.
// commandexec uses it to route Create/gates onto the node-id address space
// without a Registry lookup (the execution target is a node, not an edge).
func IsPeriphAction(actionID string) bool {
	_, ok := periphDefinition(actionID)
	return ok
}
