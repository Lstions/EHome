// Package deviceaction owns the server-side Action Catalog. It deliberately
// exposes a restricted, declarative subset: drivers never receive DB, MQTT or
// authorization lifecycle access through this package.
package deviceaction

import (
	"encoding/json"
	"fmt"
	"sort"

	"ehome/backend/internal/drivers"
)

const ChannelCmdV2Adapter = "channel_cmd_v2"

// SingleStep is the only physical transport shape currently permitted. It is
// compiled by trusted server code, never sourced from request parameters.
// Keeping it next to the action definition makes the Action Catalog the sole
// place that can introduce a new physical command.
type SingleStep struct {
	TXData        []byte
	ReadSize      uint32
	RXTimeoutMS   uint32
	PostTXDelayMS uint32
}

type Compiler func(json.RawMessage) (SingleStep, error)
type Verifier func(json.RawMessage, []byte) ([]drivers.SensorData, error)

type Definition struct {
	ID          string          `json:"id"`
	Version     int             `json:"version"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	DeviceType  string          `json:"device_type"`
	Semantics   string          `json:"semantics"`
	Risk        string          `json:"risk"`
	Enabled     bool            `json:"enabled"`
	Transport   string          `json:"transport"`
	InputSchema ParameterSchema `json:"input_schema"`
	SingleStep  SingleStep      `json:"-"`
	compiler    Compiler
	verifier    Verifier
}

type Registry struct {
	byType map[string]map[string]Definition
}

func NewRegistry() *Registry { return &Registry{byType: make(map[string]map[string]Definition)} }

func (r *Registry) Register(def Definition) error {
	if def.ID == "" || def.Version <= 0 || def.DeviceType == "" || !allowedSemantics(def.Semantics) || !allowedRisk(def.Risk) || def.Transport != ChannelCmdV2Adapter {
		return fmt.Errorf("invalid restricted action definition %q", def.ID)
	}
	if err := def.InputSchema.Validate(); err != nil {
		return fmt.Errorf("invalid action schema %q: %w", def.ID, err)
	}
	if def.Semantics == "set" && def.verifier == nil {
		// A transport ACK is not evidence that a setting took effect.  Every
		// setter must supply a trusted ACK/readback verifier before it can enter
		// the catalog, even while its rollout flag remains disabled.
		return fmt.Errorf("set action %q requires a trusted verifier", def.ID)
	}
	if len(def.InputSchema.Properties) != 0 || len(def.InputSchema.Required) != 0 {
		// Do not let a parameterized definition look executable until a trusted
		// compiler is attached. The compiler is server code, never data from a
		// browser or DeviceConfig record, and is re-run from persisted params.
		if def.compiler == nil {
			return fmt.Errorf("parameterized action %q requires a trusted compiler", def.ID)
		}
	} else if err := validateSingleStep(def.SingleStep); err != nil {
		return fmt.Errorf("invalid static action step %q: %w", def.ID, err)
	}
	def.SingleStep.TXData = append([]byte(nil), def.SingleStep.TXData...)
	if r.byType[def.DeviceType] == nil {
		r.byType[def.DeviceType] = make(map[string]Definition)
	}
	if _, exists := r.byType[def.DeviceType][def.ID]; exists {
		return fmt.Errorf("duplicate action %s for %s", def.ID, def.DeviceType)
	}
	r.byType[def.DeviceType][def.ID] = def
	return nil
}

func allowedSemantics(value string) bool {
	return value == "read" || value == "set"
}

func allowedRisk(value string) bool {
	switch value {
	case "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func (def Definition) Compile(params json.RawMessage) (SingleStep, error) {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if def.compiler == nil {
		if string(params) != "{}" {
			return SingleStep{}, fmt.Errorf("static action received parameters")
		}
		return cloneStep(def.SingleStep), nil
	}
	step, err := def.compiler(append(json.RawMessage(nil), params...))
	if err != nil {
		return SingleStep{}, fmt.Errorf("compile action %q: %w", def.ID, err)
	}
	if err := validateSingleStep(step); err != nil {
		return SingleStep{}, fmt.Errorf("compiled action %q: %w", def.ID, err)
	}
	return cloneStep(step), nil
}

// Verify interprets a successful Final using only trusted Driver code.  This
// function deliberately has no generic success fallback: that would let a
// future setter become SUCCEEDED merely because the ESP32 completed a byte
// transaction.
func (def Definition) Verify(params json.RawMessage, raw []byte) ([]drivers.SensorData, error) {
	if def.verifier == nil {
		return nil, fmt.Errorf("action %q has no final verifier", def.ID)
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return def.verifier(append(json.RawMessage(nil), params...), append([]byte(nil), raw...))
}

func cloneStep(step SingleStep) SingleStep {
	step.TXData = append([]byte(nil), step.TXData...)
	return step
}

func validateSingleStep(step SingleStep) error {
	if len(step.TXData) == 0 || len(step.TXData) > 128 || step.ReadSize > 256 || step.RXTimeoutMS == 0 || step.RXTimeoutMS > 30000 || step.PostTXDelayMS > 30000 {
		return fmt.Errorf("bounded transaction fields invalid")
	}
	return nil
}

func (r *Registry) Get(deviceType, actionID string) (Definition, bool) {
	def, ok := r.byType[deviceType][actionID]
	return def, ok
}

// SetEnabled is intentionally a composition-root/test rollout primitive, not
// an HTTP capability. It lets deployment configuration enable one action only
// after the corresponding hardware evidence has been recorded.
func (r *Registry) SetEnabled(deviceType, actionID string, enabled bool) error {
	def, ok := r.Get(deviceType, actionID)
	if !ok {
		return fmt.Errorf("action %s for %s not found", actionID, deviceType)
	}
	if enabled && !currentEngineAllows(def) {
		return fmt.Errorf("action %s for %s requires the future high-risk command engine", actionID, deviceType)
	}
	def.Enabled = enabled
	r.byType[deviceType][actionID] = def
	return nil
}

// currentEngineAllows is deliberately narrower than the complete design. The
// deployed single-step/RAM replay engine cannot safely execute setters or
// medium/high/critical actions until risk encoding, durable at-most-once and
// bounded verification semantics land together.
func currentEngineAllows(def Definition) bool {
	return def.Semantics == "read" && def.Risk == "low"
}

func (r *Registry) List(deviceType string) []Definition {
	defs := make([]Definition, 0, len(r.byType[deviceType]))
	for _, def := range r.byType[deviceType] {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

// NewBuiltInRegistry starts intentionally small. PRS-3001 rainfall is a
// low-risk read with a known fixed request/response shape; all legacy writes
// remain absent from the catalog.
func NewBuiltInRegistry(driverRegistry *drivers.Registry) *Registry {
	registry := driverRegistry
	if registry == nil {
		registry = drivers.NewRegistry()
		drivers.RegisterBuiltInDrivers(registry)
	}
	r := NewRegistry()
	for _, deviceType := range registry.List() {
		driver, err := registry.Get(deviceType)
		if err != nil {
			panic(err)
		}
		provider, ok := driver.(drivers.ControlActionProvider)
		if !ok {
			continue
		}
		for _, action := range provider.ControlActions() {
			schema, err := schemaFromDriver(action.Parameters)
			if err != nil {
				panic(fmt.Errorf("invalid driver action %s/%s: %w", deviceType, action.ID, err))
			}
			var compiler Compiler
			if len(action.Parameters) != 0 {
				driverCompiler, ok := driver.(drivers.ControlActionCompiler)
				if !ok {
					panic(fmt.Errorf("parameterized driver action %s/%s has no compiler", deviceType, action.ID))
				}
				actionID := action.ID
				compiler = func(params json.RawMessage) (SingleStep, error) {
					step, err := driverCompiler.CompileControlAction(actionID, params)
					return SingleStep{TXData: step.TXData, ReadSize: step.ReadSize, RXTimeoutMS: step.RXTimeoutMS, PostTXDelayMS: step.PostTXDelayMS}, err
				}
			}
			var verifier Verifier
			if actionVerifier, ok := driver.(drivers.ControlActionVerifier); ok {
				actionID := action.ID
				verifier = func(params json.RawMessage, raw []byte) ([]drivers.SensorData, error) {
					return actionVerifier.VerifyControlAction(actionID, params, raw)
				}
			} else if action.Semantics == "read" {
				// Pure reads retain the existing driver parser as their explicit
				// verifier.  Setters do not receive this fallback.
				verifier = func(_ json.RawMessage, raw []byte) ([]drivers.SensorData, error) {
					return driver.ParseData(raw)
				}
			}
			enabled := action.Enabled && action.Semantics == "read" && action.Risk == "low"
			if err := r.Register(Definition{ID: action.ID, Version: action.Version, Name: action.Name,
				Description: action.Description, DeviceType: deviceType, Semantics: action.Semantics,
				Risk: action.Risk, Enabled: enabled, Transport: ChannelCmdV2Adapter,
				InputSchema: schema,
				SingleStep: SingleStep{TXData: action.TXData, ReadSize: action.ReadSize,
					RXTimeoutMS: action.RXTimeoutMS, PostTXDelayMS: action.PostTXDelayMS}, compiler: compiler, verifier: verifier}); err != nil {
				panic(err)
			}
		}
	}
	return r
}

func schemaFromDriver(parameters []drivers.ControlParameter) (ParameterSchema, error) {
	schema := ParameterSchema{Properties: make(map[string]Parameter)}
	for _, parameter := range parameters {
		if _, exists := schema.Properties[parameter.Name]; exists {
			return ParameterSchema{}, fmt.Errorf("duplicate parameter %q", parameter.Name)
		}
		schema.Properties[parameter.Name] = Parameter{Type: parameter.Type, Minimum: parameter.Minimum, Maximum: parameter.Maximum,
			MinLength: parameter.MinLength, MaxLength: parameter.MaxLength, Enum: append([]string(nil), parameter.Enum...)}
		if parameter.Required {
			schema.Required = append(schema.Required, parameter.Name)
		}
	}
	if err := schema.Validate(); err != nil {
		return ParameterSchema{}, err
	}
	return schema, nil
}
