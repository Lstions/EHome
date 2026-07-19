// Package deviceaction owns the server-side Action Catalog. It deliberately
// exposes a restricted, declarative subset: drivers never receive DB, MQTT or
// authorization lifecycle access through this package.
package deviceaction

import (
	"encoding/json"
	"fmt"
	"os"
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

// PlanStep and BoundedPlan model the future high-risk transport without
// accepting arbitrary scripts. Every step is fixed by trusted Driver code;
// the node may execute at most eight steps and must preserve the finally step
// when the plan declares one. This is deliberately a data model first: the
// current single-step ChannelCmdV2 adapter rejects such plans until the node
// advertises durable replay protection.
type PlanStep struct {
	ID         string
	Kind       string // read, write, readback, finally
	SingleStep SingleStep
}

type BoundedPlan struct {
	Steps           []PlanStep
	AtMostOnce      bool
	RequiresFinally bool
}

type PlanCompiler func(json.RawMessage) (BoundedPlan, error)

type Compiler func(json.RawMessage) (SingleStep, error)
type Verifier func(json.RawMessage, []byte) ([]drivers.SensorData, error)

type Definition struct {
	ID                 string          `json:"id"`
	Version            int             `json:"version"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	DeviceType         string          `json:"device_type"`
	Semantics          string          `json:"semantics"`
	Risk               string          `json:"risk"`
	ExecutionShape     string          `json:"execution_shape"`
	Verification       string          `json:"verification"`
	AtMostOnce         bool            `json:"at_most_once"`
	MaxSteps           uint8           `json:"max_steps"`
	Enabled            bool            `json:"enabled"`
	Transport          string          `json:"transport"`
	InputSchema        ParameterSchema `json:"input_schema"`
	AvailabilityCode   string          `json:"availability_code,omitempty"`
	AvailabilityReason string          `json:"availability_reason,omitempty"`
	SingleStep         SingleStep      `json:"-"`
	Plan               BoundedPlan     `json:"-"`
	compiler           Compiler
	planCompiler       PlanCompiler
	verifier           Verifier
}

type Registry struct {
	byType map[string]map[string]Definition
}

func NewRegistry() *Registry { return &Registry{byType: make(map[string]map[string]Definition)} }

func (r *Registry) Register(def Definition) error {
	if def.ID == "" || def.Version <= 0 || def.DeviceType == "" || !allowedSemantics(def.Semantics) || !allowedRisk(def.Risk) || def.Transport != ChannelCmdV2Adapter {
		return fmt.Errorf("invalid restricted action definition %q", def.ID)
	}
	if def.ExecutionShape == "" {
		def.ExecutionShape = "single"
	}
	if def.Verification == "" {
		def.Verification = "ack"
	}
	if def.MaxSteps == 0 {
		def.MaxSteps = 1
	}
	if def.ExecutionShape != "single" && def.ExecutionShape != "bounded_sequence" {
		return fmt.Errorf("invalid execution shape for %q", def.ID)
	}
	if def.Verification != "none" && def.Verification != "ack" && def.Verification != "readback" && def.Verification != "observation" {
		return fmt.Errorf("invalid verification strategy for %q", def.ID)
	}
	if def.MaxSteps > 8 || def.ExecutionShape == "single" && def.MaxSteps != 1 {
		return fmt.Errorf("invalid max steps for %q", def.ID)
	}
	if def.ExecutionShape == "bounded_sequence" && def.MaxSteps < 2 {
		return fmt.Errorf("bounded action %q needs at least two steps", def.ID)
	}
	if def.AtMostOnce && def.Risk != "high" && def.Risk != "critical" {
		return fmt.Errorf("at-most-once action %q must be high or critical risk", def.ID)
	}
	if err := def.InputSchema.Validate(); err != nil {
		return fmt.Errorf("invalid action schema %q: %w", def.ID, err)
	}
	if (def.Semantics == "set" || def.Semantics == "reset") && def.verifier == nil && def.AvailabilityCode == "" {
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
	} else if def.AvailabilityCode == "" && def.ExecutionShape == "single" {
		if err := validateSingleStep(def.SingleStep); err != nil {
			return fmt.Errorf("invalid static action step %q: %w", def.ID, err)
		}
	} else if def.AvailabilityCode == "" && def.ExecutionShape == "bounded_sequence" {
		if err := validatePlan(def.Plan, def.MaxSteps, def.AtMostOnce); err != nil {
			return fmt.Errorf("invalid action plan %q: %w", def.ID, err)
		}
	} else if def.AvailabilityCode == "" && def.ExecutionShape != "single" {
		return fmt.Errorf("invalid action shape %q", def.ID)
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
	return value == "read" || value == "set" || value == "reset"
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
	if def.AvailabilityCode != "" {
		return SingleStep{}, fmt.Errorf("action %q unavailable: %s", def.ID, def.AvailabilityReason)
	}
	if def.ExecutionShape != "single" {
		return SingleStep{}, fmt.Errorf("action %q requires bounded plan transport", def.ID)
	}
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

// CompilePlan validates a fixed multi-step workflow. The current dispatcher
// intentionally does not flatten it into a single request, which prevents a
// destructive action from silently losing readback/finally semantics.
func (def Definition) CompilePlan(params json.RawMessage) (BoundedPlan, error) {
	if def.AvailabilityCode != "" {
		return BoundedPlan{}, fmt.Errorf("action %q unavailable: %s", def.ID, def.AvailabilityReason)
	}
	if def.ExecutionShape != "bounded_sequence" {
		return BoundedPlan{}, fmt.Errorf("action %q is not a bounded plan", def.ID)
	}
	if def.planCompiler == nil {
		return BoundedPlan{}, fmt.Errorf("action %q has no trusted plan compiler", def.ID)
	}
	plan, err := def.planCompiler(append(json.RawMessage(nil), params...))
	if err != nil {
		return BoundedPlan{}, fmt.Errorf("compile action plan %q: %w", def.ID, err)
	}
	if err := validatePlan(plan, def.MaxSteps, def.AtMostOnce); err != nil {
		return BoundedPlan{}, fmt.Errorf("compiled action plan %q: %w", def.ID, err)
	}
	return clonePlan(plan), nil
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

func validatePlan(plan BoundedPlan, maxSteps uint8, atMostOnce bool) error {
	if len(plan.Steps) < 2 || len(plan.Steps) > int(maxSteps) || len(plan.Steps) > 8 {
		return fmt.Errorf("step count is outside bounded limit")
	}
	if plan.AtMostOnce != atMostOnce {
		return fmt.Errorf("at-most-once policy mismatch")
	}
	if plan.RequiresFinally && plan.Steps[len(plan.Steps)-1].Kind != "finally" {
		return fmt.Errorf("finally step must be last")
	}
	for _, step := range plan.Steps {
		if step.ID == "" || (step.Kind != "read" && step.Kind != "write" && step.Kind != "readback" && step.Kind != "finally") {
			return fmt.Errorf("invalid plan step")
		}
		if err := validateSingleStep(step.SingleStep); err != nil {
			return err
		}
	}
	return nil
}

func clonePlan(plan BoundedPlan) BoundedPlan {
	plan.Steps = append([]PlanStep(nil), plan.Steps...)
	for i := range plan.Steps {
		plan.Steps[i].SingleStep = cloneStep(plan.Steps[i].SingleStep)
	}
	return plan
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
	if enabled {
		// A rollout selector is the explicit composition-root evidence gate. It
		// may clear a Driver's advisory availability reason only in the
		// development high-risk override; production still requires the normal
		// engine capability gate below.
		def.AvailabilityCode = ""
		def.AvailabilityReason = ""
	}
	r.byType[deviceType][actionID] = def
	return nil
}

// currentEngineAllows is deliberately narrower than the complete design. The
// deployed single-step/RAM replay engine cannot safely execute setters or
// medium/high/critical actions until risk encoding, durable at-most-once and
// bounded verification semantics land together.
func currentEngineAllows(def Definition) bool {
	if def.ExecutionShape == "single" && def.Semantics == "read" && def.Risk == "low" && !def.AtMostOnce {
		return true
	}
	if def.ExecutionShape == "bounded_sequence" && def.planCompiler != nil && def.AtMostOnce {
		return true
	}
	return os.Getenv("EHOME_ENV") == "development" && os.Getenv("EHOME_ENABLE_HIGH_RISK_ACTIONS") == "true" && def.ExecutionShape == "single" && def.Verification != "none"
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
			var planCompiler PlanCompiler
			if action.ExecutionShape == "bounded_sequence" {
				driverPlanCompiler, ok := driver.(drivers.ControlActionPlanCompiler)
				if ok {
					actionID := action.ID
					planCompiler = func(params json.RawMessage) (BoundedPlan, error) {
						compiled, err := driverPlanCompiler.CompileControlActionPlan(actionID, params)
						if err != nil {
							return BoundedPlan{}, err
						}
						plan := BoundedPlan{AtMostOnce: compiled.AtMostOnce, RequiresFinally: compiled.RequiresFinally}
						for _, step := range compiled.Steps {
							plan.Steps = append(plan.Steps, PlanStep{ID: step.ID, Kind: step.Kind, SingleStep: SingleStep{
								TXData: step.TXData, ReadSize: step.ReadSize, RXTimeoutMS: step.RXTimeoutMS, PostTXDelayMS: step.PostTXDelayMS,
							}})
						}
						return plan, nil
					}
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
			enabled := action.Enabled && action.Semantics == "read" && action.Risk == "low" && action.ExecutionShape == "single" && !action.AtMostOnce
			if err := r.Register(Definition{ID: action.ID, Version: action.Version, Name: action.Name,
				Description: action.Description, DeviceType: deviceType, Semantics: action.Semantics,
				Risk: action.Risk, ExecutionShape: action.ExecutionShape, Verification: action.Verification,
				AtMostOnce: action.AtMostOnce, MaxSteps: action.MaxSteps, Enabled: enabled, Transport: ChannelCmdV2Adapter,
				AvailabilityCode: action.AvailabilityCode, AvailabilityReason: action.AvailabilityReason,
				InputSchema: schema,
				SingleStep: SingleStep{TXData: action.TXData, ReadSize: action.ReadSize,
					RXTimeoutMS: action.RXTimeoutMS, PostTXDelayMS: action.PostTXDelayMS}, Plan: BoundedPlan{}, compiler: compiler, planCompiler: planCompiler, verifier: verifier}); err != nil {
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
