// Package deviceaction owns the server-side Action Catalog. It deliberately
// exposes a restricted, declarative subset: drivers never receive DB, MQTT or
// authorization lifecycle access through this package.
package deviceaction

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
type PlanAddressCompiler func(json.RawMessage, uint8) (BoundedPlan, error)

type Compiler func(json.RawMessage) (SingleStep, error)
type Verifier func(json.RawMessage, []byte) ([]drivers.SensorData, error)
type AddressCompiler func(json.RawMessage, uint8) (SingleStep, error)
type AddressVerifier func(json.RawMessage, []byte, uint8) ([]drivers.SensorData, error)

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
	addressCompiler    AddressCompiler
	planCompiler       PlanCompiler
	planAddrCompiler   PlanAddressCompiler
	verifier           Verifier
	addressVerifier    AddressVerifier
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
		// Bounded plans are either statically declared (def.Plan) or compiled
		// dynamically at dispatch time (planCompiler/planAddrCompiler).  A
		// dynamic compiler re-validates the plan against MaxSteps on every
		// CompilePlan call, so an empty static Plan is legitimate here.
		if len(def.Plan.Steps) != 0 {
			if err := validatePlan(def.Plan, def.MaxSteps, def.AtMostOnce); err != nil {
				return fmt.Errorf("invalid action plan %q: %w", def.ID, err)
			}
		} else if def.planCompiler == nil && def.planAddrCompiler == nil {
			return fmt.Errorf("bounded action %q has no plan source", def.ID)
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

// CompileForAddress applies the EdgeDevice-owned physical address when the
// trusted driver declares that its protocol embeds one.  Non-addressed
// actions retain the ordinary compiler path.
func (def Definition) CompileForAddress(params json.RawMessage, hardwareID string) (SingleStep, error) {
	if def.addressCompiler == nil {
		return def.Compile(params)
	}
	address, err := ParseHardwareAddress(hardwareID)
	if err != nil {
		return SingleStep{}, fmt.Errorf("action %q target address: %w", def.ID, err)
	}
	if def.AvailabilityCode != "" {
		return SingleStep{}, fmt.Errorf("action %q unavailable: %s", def.ID, def.AvailabilityReason)
	}
	step, err := def.addressCompiler(append(json.RawMessage(nil), params...), address)
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

// CompilePlanForAddress applies the EdgeDevice-owned physical address to a
// bounded plan when the trusted driver declares that its protocol embeds one
// (e.g. Modbus unit address in every plan step).  Non-addressed plans retain
// the ordinary CompilePlan path.
func (def Definition) CompilePlanForAddress(params json.RawMessage, hardwareID string) (BoundedPlan, error) {
	if def.planAddrCompiler == nil {
		return def.CompilePlan(params)
	}
	address, err := ParseHardwareAddress(hardwareID)
	if err != nil {
		return BoundedPlan{}, fmt.Errorf("action %q target address: %w", def.ID, err)
	}
	if def.AvailabilityCode != "" {
		return BoundedPlan{}, fmt.Errorf("action %q unavailable: %s", def.ID, def.AvailabilityReason)
	}
	if def.ExecutionShape != "bounded_sequence" {
		return BoundedPlan{}, fmt.Errorf("action %q is not a bounded plan", def.ID)
	}
	if def.planAddrCompiler == nil {
		return BoundedPlan{}, fmt.Errorf("action %q has no trusted plan compiler", def.ID)
	}
	plan, err := def.planAddrCompiler(append(json.RawMessage(nil), params...), address)
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

// VerifyForAddress binds a final response to the physical EdgeDevice target
// when the trusted driver supports addressed verification.
func (def Definition) VerifyForAddress(params json.RawMessage, raw []byte, hardwareID string) ([]drivers.SensorData, error) {
	if def.addressVerifier == nil {
		return def.Verify(params, raw)
	}
	address, err := ParseHardwareAddress(hardwareID)
	if err != nil {
		return nil, fmt.Errorf("action %q target address: %w", def.ID, err)
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return def.addressVerifier(append(json.RawMessage(nil), params...), append([]byte(nil), raw...), address)
}

// ParseHardwareAddress accepts the decimal and 0x-prefixed forms used by
// EdgeDevice.hardware_id.  Zero/empty means the legacy default address 1 for
// addressed built-in actions; other values must be valid Modbus unit IDs.
func ParseHardwareAddress(value string) (uint8, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return 1, nil
	}
	base := 10
	text := value
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		base = 16
		text = text[2:]
	}
	parsed, err := strconv.ParseUint(text, base, 8)
	if err != nil || parsed < 1 || parsed > 254 {
		return 0, fmt.Errorf("hardware_id %q must be an address from 1 to 254", value)
	}
	return uint8(parsed), nil
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
	if enabled && !CurrentEngineAllows(def) {
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

// CurrentEngineAllows gates which actions the deployed engine may execute.
// Low-risk reads are always allowed. Reset/set actions are permitted only
// when the execution shape is bounded_sequence — the ESP32 command engine
// supports bounded durable replay with channel_cmd_v2 and readback/finally
// verification. Single-step reset/set is rejected because a transport ACK is
// not evidence that a setting took effect, and the future high-risk engine
// gate has not yet shipped. The rollout selector in configuration provides
// the explicit per-action evidence gate on top of this engine gate.
func CurrentEngineAllows(def Definition) bool {
	if def.ExecutionShape == "single" && def.Semantics == "read" && def.Risk == "low" && !def.AtMostOnce {
		return true
	}
	if def.Semantics == "reset" || def.Semantics == "set" {
		// bounded_sequence: the multi-step workflow itself performs the
		// write + readback reconciliation inside one durable batch.
		if def.ExecutionShape == "bounded_sequence" {
			return true
		}
		// single + ack-echo + readback verification: the driver's write
		// response is an explicit echo of its own requested frame (transport
		// ACK is not evidence), and the confirming readback is performed by
		// the command lifecycle after the write (e.g. SN-3001 address/baud
		// changes reconcile hardware_id/bus_config and re-push the manifest,
		// then a subsequent read verifies the new parameter domain).  This is
		// still a write + readback reconciliation, just split across the
		// command lifecycle instead of one physical batch.
		if def.ExecutionShape == "single" && def.Verification == "readback" && def.verifier != nil {
			return true
		}
	}
	return false
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
			var addressCompiler AddressCompiler
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
			if targetCompiler, ok := driver.(drivers.ControlActionAddressCompiler); ok {
				actionID := action.ID
				addressCompiler = func(params json.RawMessage, address uint8) (SingleStep, error) {
					step, err := targetCompiler.CompileControlActionForAddress(actionID, params, address)
					return SingleStep{TXData: step.TXData, ReadSize: step.ReadSize, RXTimeoutMS: step.RXTimeoutMS, PostTXDelayMS: step.PostTXDelayMS}, err
				}
			}
			var planCompiler PlanCompiler
			var planAddrCompiler PlanAddressCompiler
			if action.ExecutionShape == "bounded_sequence" {
				if driverPlanCompiler, ok := driver.(drivers.ControlActionPlanCompiler); ok {
					actionID := action.ID
					planCompiler = func(params json.RawMessage) (BoundedPlan, error) {
						compiled, err := driverPlanCompiler.CompileControlActionPlan(actionID, params)
						if err != nil {
							return BoundedPlan{}, err
						}
						return planFromDriver(compiled), nil
					}
				}
				if driverPlanAddrCompiler, ok := driver.(drivers.ControlActionPlanCompilerForAddress); ok {
					actionID := action.ID
					planAddrCompiler = func(params json.RawMessage, address uint8) (BoundedPlan, error) {
						compiled, err := driverPlanAddrCompiler.CompileControlActionPlanForAddress(actionID, params, address)
						if err != nil {
							return BoundedPlan{}, err
						}
						return planFromDriver(compiled), nil
					}
				}
			}
			var verifier Verifier
			var addressVerifier AddressVerifier
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
			if targetVerifier, ok := driver.(drivers.ControlActionAddressVerifier); ok {
				actionID := action.ID
				addressVerifier = func(params json.RawMessage, raw []byte, address uint8) ([]drivers.SensorData, error) {
					return targetVerifier.VerifyControlActionForAddress(actionID, params, raw, address)
				}
			}
			// 默认启用原则：功能实现出来就应该默认可用。不需要环境变量白名单
			// 来"批准"一个已实现的受控操作——真正的门禁是运行时 gate
			// (dispatchEnabled / node capability / edge/node 状态 / manifest /
			// 通道上报)。但 fail-closed 仍保留：只有具备完整执行链(compiler/
			// planCompiler/verifier)且声明了对账语义(Verification)的操作才默认启用；
			// 尚未具备完整执行链或协议未验证的操作保持 disabled。
			//
			// set/reset: 写操作必须声明对账语义(readback = 写入后读回验证；
			// observation = 写后观察确认)且具备可信 verifier，否则强制 disabled。
			// 具备完整执行链的写操作忽略驱动出厂 Enabled:false（如 BMS
			// set_mos_policy 有 bounded compiler + verifier + readback 声明，
			// 无需白名单即可启用）；仅带 AvailabilityCode 的仍保持禁用（协议
			// 未冻结等显式门禁）。
			enabled := action.Enabled
			if action.Semantics == "set" || action.Semantics == "reset" {
				if verifier != nil && action.Verification != "" && action.AvailabilityCode == "" {
					enabled = true
				} else {
					enabled = false
				}
			}
			if err := r.Register(Definition{ID: action.ID, Version: action.Version, Name: action.Name,
				Description: action.Description, DeviceType: deviceType, Semantics: action.Semantics,
				Risk: action.Risk, ExecutionShape: action.ExecutionShape, Verification: action.Verification,
				AtMostOnce: action.AtMostOnce, MaxSteps: action.MaxSteps, Enabled: enabled, Transport: ChannelCmdV2Adapter,
				AvailabilityCode: action.AvailabilityCode, AvailabilityReason: action.AvailabilityReason,
				InputSchema: schema, addressCompiler: addressCompiler, addressVerifier: addressVerifier,
				SingleStep: SingleStep{TXData: action.TXData, ReadSize: action.ReadSize,
					RXTimeoutMS: action.RXTimeoutMS, PostTXDelayMS: action.PostTXDelayMS}, Plan: BoundedPlan{}, compiler: compiler, planCompiler: planCompiler, planAddrCompiler: planAddrCompiler, verifier: verifier}); err != nil {
				panic(err)
			}
		}
	}
	return r
}

// planFromDriver converts a driver-owned CompiledControlPlan into the
// deviceaction.BoundedPlan representation shared by the dispatcher.
func planFromDriver(compiled drivers.CompiledControlPlan) BoundedPlan {
	plan := BoundedPlan{AtMostOnce: compiled.AtMostOnce, RequiresFinally: compiled.RequiresFinally}
	for _, step := range compiled.Steps {
		plan.Steps = append(plan.Steps, PlanStep{ID: step.ID, Kind: step.Kind, SingleStep: SingleStep{
			TXData: step.TXData, ReadSize: step.ReadSize, RXTimeoutMS: step.RXTimeoutMS, PostTXDelayMS: step.PostTXDelayMS,
		}})
	}
	return plan
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
