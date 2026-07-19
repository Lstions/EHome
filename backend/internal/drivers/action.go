package drivers

import "encoding/json"

// ControlAction is a declarative, driver-owned description of one bounded
// physical read. It contains no database, transport, authorization, or user
// supplied command bytes. deviceaction validates and compiles this narrow
// contract into the shared ChannelCmdV2 envelope.
type ControlAction struct {
	ID          string
	Version     int
	Name        string
	Description string
	Semantics   string
	Risk        string
	// ExecutionShape describes the physical workflow required by the action.
	// single is the currently deployed one-request envelope; bounded_sequence
	// is reserved for a fixed, server-compiled sequence (readback/finally),
	// never for a browser supplied script.
	ExecutionShape string
	// Verification is the evidence required before the action may become
	// SUCCEEDED.  It is metadata as well as a review gate: an ACK alone is not
	// a readback or reboot observation.
	Verification string
	// AtMostOnce means that after a physical dispatch has been accepted, a
	// transport retry is forbidden and an ambiguous result must become UNKNOWN.
	AtMostOnce bool
	MaxSteps   uint8
	// Enabled is an explicit per-action rollout gate. It must remain false
	// until the action has passed its own protocol and hardware evidence gate.
	Enabled       bool
	TXData        []byte
	ReadSize      uint32
	RXTimeoutMS   uint32
	PostTXDelayMS uint32
	// Parameters stays declarative and scalar-only. Driver code cannot attach
	// executable validation or a template; deviceaction turns it into the
	// server-side restricted schema before any request is accepted.
	Parameters []ControlParameter
	// AvailabilityCode/Reason make a known-but-not-yet-safe capability visible
	// in the catalog without manufacturing a command frame.  Examples include
	// protocol_unverified and hardware_evidence_required.
	AvailabilityCode   string
	AvailabilityReason string
}

type ControlParameter struct {
	Name      string
	Type      string
	Required  bool
	Minimum   *float64
	Maximum   *float64
	MinLength *uint32
	MaxLength *uint32
	Enum      []string
}

// ControlActionProvider is deliberately optional so existing parsing-only
// drivers remain source compatible and cannot accidentally expose controls.
type ControlActionProvider interface {
	ControlActions() []ControlAction
}

// CompiledControlStep is returned by trusted in-process Driver code only. It
// is never decoded from a browser request or DeviceConfig JSON.
type CompiledControlStep struct {
	TXData        []byte
	ReadSize      uint32
	RXTimeoutMS   uint32
	PostTXDelayMS uint32
}

// CompiledControlPlan is the driver-owned representation of a bounded
// physical workflow. It is intentionally made of the same bounded steps as
// ChannelCmdV2; no arbitrary script or branching data can enter the plan.
type CompiledControlPlan struct {
	Steps           []CompiledControlPlanStep
	AtMostOnce      bool
	RequiresFinally bool
}

type CompiledControlPlanStep struct {
	ID           string
	Kind         string
	TXData       []byte
	ReadSize     uint32
	RXTimeoutMS  uint32
	PostTXDelayMS uint32
}

// ControlActionCompiler is intentionally separate from ControlActionProvider:
// parsing-only drivers cannot accidentally make a parameterized action
// executable. The server persists canonical params and invokes this compiler
// again for a retry, so implementations must be deterministic.
type ControlActionCompiler interface {
	CompileControlAction(actionID string, params json.RawMessage) (CompiledControlStep, error)
}

// ControlActionPlanCompiler is optional. It is required only by actions whose
// execution_shape is bounded_sequence, such as rain clear (read → write →
// readback). The node must advertise a matching batch/NVS capability before
// the plan can be dispatched.
type ControlActionPlanCompiler interface {
	CompileControlActionPlan(actionID string, params json.RawMessage) (CompiledControlPlan, error)
}

// ControlActionVerifier is the trusted, action-specific interpretation of a
// successful ChannelCmdV2 Final.  It is intentionally separate from
// ParseData: a setter's ACK or readback is not a sensor sample.  Future set
// actions must implement this interface; read actions may use the driver's
// existing ParseData fallback while they remain pure reads.
//
// The returned projection is safe to persist as verified_result.  Raw control
// frames remain confined to the transport boundary.
type ControlActionVerifier interface {
	VerifyControlAction(actionID string, params json.RawMessage, raw []byte) ([]SensorData, error)
}
