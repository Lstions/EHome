package commandexec

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"

	"gorm.io/gorm"
)

const MaxCapabilityAge = 5 * time.Minute

// ChannelCmdV2Transport is the only production transport for business
// actions. It compiles server-owned ActionDefinition data into a bounded V2
// envelope and refuses to publish when current node facts cannot prove that
// the firmware instance supports that envelope.
type ChannelCmdV2Transport struct {
	db      *gorm.DB
	mqtt    mqtt.Publisher
	actions *deviceaction.Registry
	now     func() time.Time
}

type commandEngineCapabilities struct {
	SupportsChannelCmdV2 bool   `json:"supports_channel_cmd_v2"`
	SupportsBoundedBatch bool   `json:"supports_bounded_batch"`
	SupportsFinally      bool   `json:"supports_finally"`
	MaxBatchSteps        uint32 `json:"max_batch_steps"`
	MaxTXBytes           uint32 `json:"max_tx_bytes"`
	MaxRXBytes           uint32 `json:"max_rx_bytes"`
	MaxStepTimeoutMS     uint32 `json:"max_step_timeout_ms"`
}

func NewChannelCmdV2Transport(db *gorm.DB, publisher mqtt.Publisher, actions *deviceaction.Registry) *ChannelCmdV2Transport {
	return &ChannelCmdV2Transport{db: db, mqtt: publisher, actions: actions, now: func() time.Time { return time.Now().UTC() }}
}

func (t *ChannelCmdV2Transport) Dispatch(ctx context.Context, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil || t.db == nil || t.mqtt == nil || t.actions == nil {
		return DispatchResult{}, fmt.Errorf("ChannelCmdV2 transport is unavailable")
	}
	return t.dispatch(ctx, t.db, execution, attempt)
}

// DispatchInTransaction keeps admission reads in the dispatcher's state
// transition transaction. This matters for both transaction consistency and
// SQLite test semantics, where a separate in-memory connection has no schema.
func (t *ChannelCmdV2Transport) DispatchInTransaction(ctx context.Context, db *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	if t == nil || db == nil || t.mqtt == nil || t.actions == nil {
		return DispatchResult{}, fmt.Errorf("ChannelCmdV2 transport is unavailable")
	}
	return t.dispatch(ctx, db, execution, attempt)
}

func (t *ChannelCmdV2Transport) dispatch(ctx context.Context, db *gorm.DB, execution models.CommandExecution, attempt models.CommandAttempt) (DispatchResult, error) {
	var edge models.EdgeDevice
	if err := db.WithContext(ctx).Preload("Node").First(&edge, execution.EdgeDeviceID).Error; err != nil {
		return DispatchResult{}, err
	}
	if edge.NodeID != execution.NodeID || edge.Type != execution.DeviceType ||
		edge.DeviceConfigID != execution.DeviceConfigID || edge.ChannelID != execution.ChannelID ||
		!edge.Enabled || edge.Status == "inactive" || edge.ChannelID == 0 || edge.Node.Status != "online" {
		return DispatchResult{}, fmt.Errorf("execution target is no longer available")
	}
	if _, err := loadActionChannel(db.WithContext(ctx), edge); err != nil {
		return DispatchResult{}, err
	}
	if err := requireReportedActionChannel(edge.Node, edge.ChannelID); err != nil {
		return DispatchResult{}, err
	}
	if err := requireAppliedManifest(edge.Node, execution.ManifestID); err != nil {
		return DispatchResult{}, err
	}
	definition, ok := t.actions.Get(execution.DeviceType, execution.ActionID)
	if !ok || !definition.Enabled || definition.Version != execution.ActionVersion || definition.Transport != deviceaction.ChannelCmdV2Adapter {
		return DispatchResult{}, fmt.Errorf("trusted action definition is unavailable")
	}
	if definition.ExecutionShape != "single" && definition.ExecutionShape != "bounded_sequence" {
		return DispatchResult{}, fmt.Errorf("unsupported action execution shape")
	}
	devHighRisk := os.Getenv("EHOME_ENV") == "development" && os.Getenv("EHOME_ENABLE_HIGH_RISK_ACTIONS") == "true" && definition.Verification != "none"
	if (definition.Semantics != "read" || definition.Risk != "low") && !devHighRisk && definition.ExecutionShape != "bounded_sequence" {
		return DispatchResult{}, fmt.Errorf("action requires the future high-risk command engine")
	}
	bootID, capabilities, err := currentCapabilities(edge.Node, t.now)
	if err != nil {
		return DispatchResult{}, err
	}
	if execution.CommandEngineRevision == 0 || execution.CommandEngineRevision != edge.Node.CommandEngineRevision {
		return DispatchResult{}, fmt.Errorf("execution command-engine revision no longer matches node capability")
	}
	params, err := deviceaction.CanonicalizeParams(definition.InputSchema, json.RawMessage(execution.ParamsJSON))
	if err != nil {
		return DispatchResult{}, fmt.Errorf("persisted action parameters are invalid: %w", err)
	}
	var step deviceaction.SingleStep
	var batch []frame.ChannelCmdV2Step
	if definition.ExecutionShape == "bounded_sequence" {
		if !capabilities.SupportsBoundedBatch || capabilities.MaxBatchSteps == 0 {
			return DispatchResult{}, fmt.Errorf("bounded batch capability is unavailable")
		}
		plan, err := definition.CompilePlan(params)
		if err != nil {
			return DispatchResult{}, err
		}
		if len(plan.Steps) > int(capabilities.MaxBatchSteps) {
			return DispatchResult{}, fmt.Errorf("action exceeds node batch-step capability")
		}
		for _, planStep := range plan.Steps {
			if !stepFitsCapabilities(planStep.SingleStep, capabilities) {
				return DispatchResult{}, fmt.Errorf("action exceeds current node capability")
			}
			batch = append(batch, frame.ChannelCmdV2Step{Kind: planStepKindCode(planStep.Kind), TXData: planStep.SingleStep.TXData, ReadSize: planStep.SingleStep.ReadSize, RXTimeoutMS: planStep.SingleStep.RXTimeoutMS, PostTXDelayMS: planStep.SingleStep.PostTXDelayMS})
		}
		step = plan.Steps[0].SingleStep
	} else {
		step, err = definition.Compile(params)
		if err != nil {
			return DispatchResult{}, err
		}
		if !stepFitsCapabilities(step, capabilities) {
			return DispatchResult{}, fmt.Errorf("action exceeds current node capability")
		}
	}
	commandID, err := uuidBytes(execution.CommandID)
	if err != nil {
		return DispatchResult{}, err
	}
	digest, err := digestBytes(attempt.WireDigest)
	if err != nil {
		return DispatchResult{}, err
	}
	deadline := execution.DeadlineAt.UTC()
	if !deadline.After(t.now()) {
		return DispatchResult{}, fmt.Errorf("execution deadline has expired")
	}
	payload, err := frame.EncodeChannelCmdV2(frame.ChannelCmdV2{
		CommandID: commandID, PayloadDigest: digest, Attempt: attempt.AttemptNo,
		BootID: bootID, EdgeDeviceID: uint32(edge.ID), ChannelID: uint32(edge.ChannelID),
		DeadlineUnixMS: uint64(deadline.UnixMilli()), TXData: append([]byte(nil), step.TXData...),
		ReadSize: step.ReadSize, RXTimeoutMS: step.RXTimeoutMS, PostTXDelayMS: step.PostTXDelayMS,
		Plan: batch,
	})
	if err != nil {
		return DispatchResult{}, err
	}
	if err := t.mqtt.Publish(mqtt.ControlTopicForNode(execution.NodeID), payload); err != nil {
		return DispatchResult{}, fmt.Errorf("publish ChannelCmdV2: %w", err)
	}
	return DispatchResult{BootID: bootID, PublishedAt: t.now()}, nil
}

func currentCapabilities(node models.Node, now func() time.Time) (string, commandEngineCapabilities, error) {
	var capabilities commandEngineCapabilities
	if node.BootID == "" || node.ResourceReportedAt == nil || now().Sub(*node.ResourceReportedAt) > MaxCapabilityAge || node.CommandEngineRevision == 0 {
		return "", capabilities, fmt.Errorf("node command capability is absent or stale")
	}
	if err := json.Unmarshal([]byte(node.CommandEngineCapabilities), &capabilities); err != nil {
		return "", capabilities, fmt.Errorf("decode node command capability: %w", err)
	}
	if !capabilities.SupportsChannelCmdV2 || !capabilities.SupportsFinally || capabilities.MaxTXBytes == 0 || capabilities.MaxRXBytes == 0 || capabilities.MaxStepTimeoutMS == 0 {
		return "", capabilities, fmt.Errorf("node does not support ChannelCmdV2")
	}
	return node.BootID, capabilities, nil
}

func planStepKindCode(kind string) uint32 {
	switch kind {
	case "read":
		return 0
	case "write":
		return 1
	case "readback":
		return 2
	case "finally":
		return 3
	default:
		return 0
	}
}

func uuidBytes(value string) ([16]byte, error) {
	var out [16]byte
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(raw) != len(out) {
		return out, fmt.Errorf("invalid command ID")
	}
	copy(out[:], raw)
	return out, nil
}

func digestBytes(value string) ([16]byte, error) {
	var out [16]byte
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("invalid wire digest")
	}
	copy(out[:], raw[:len(out)])
	return out, nil
}
