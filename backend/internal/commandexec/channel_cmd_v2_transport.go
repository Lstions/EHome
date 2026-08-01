package commandexec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	if !deviceaction.CurrentEngineAllows(definition) {
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
	commandID, err := uuidBytes(execution.CommandID)
	if err != nil {
		return DispatchResult{}, err
	}
	deadline := execution.DeadlineAt.UTC()
	if !deadline.After(t.now()) {
		return DispatchResult{}, fmt.Errorf("execution deadline has expired")
	}

	var envelope frame.ChannelCmdV2
	envelope.CommandID = commandID
	envelope.Attempt = attempt.AttemptNo
	envelope.BootID = bootID
	envelope.EdgeDeviceID = uint32(edge.ID)
	envelope.ChannelID = uint32(edge.ChannelID)
	envelope.DeadlineUnixMS = uint64(deadline.UnixMilli())

	var step deviceaction.SingleStep
	var planSteps []frame.ChannelCmdV2Step

	if definition.ExecutionShape == "bounded_sequence" {
		plan, planErr := definition.CompilePlan(params)
		if planErr != nil {
			return DispatchResult{}, planErr
		}
		if len(plan.Steps) > int(capabilities.MaxBatchSteps) {
			return DispatchResult{}, fmt.Errorf("action exceeds node batch-step capability")
		}
		for _, ps := range plan.Steps {
			if !stepFitsCapabilities(ps.SingleStep, capabilities) {
				return DispatchResult{}, fmt.Errorf("action plan step %q exceeds current node capability", ps.ID)
			}
			planSteps = append(planSteps, frame.ChannelCmdV2Step{
				Kind:          planStepKindCode(ps.Kind),
				TXData:        append([]byte(nil), ps.SingleStep.TXData...),
				ReadSize:      ps.SingleStep.ReadSize,
				RXTimeoutMS:   ps.SingleStep.RXTimeoutMS,
				PostTXDelayMS: ps.SingleStep.PostTXDelayMS,
			})
		}
		envelope.Plan = planSteps
		// ESP32 firmware requires all top-level identity fields (8-11) to be
		// present and non-zero even for bounded batches.  Populate them from
		// the first plan step; the firmware executes the Plan steps, not the
		// top-level fields.
		first := plan.Steps[0].SingleStep
		envelope.TXData = append([]byte(nil), first.TXData...)
		envelope.ReadSize = first.ReadSize
		envelope.RXTimeoutMS = first.RXTimeoutMS
		envelope.PostTXDelayMS = first.PostTXDelayMS
		// Bind the wire digest to the actual first plan step so it includes
		// meaningful top-level transaction fields rather than zero values.
		step = first
	} else {
		step, err = definition.CompileForAddress(params, edge.HardwareID)
		if err != nil {
			return DispatchResult{}, err
		}
		if !stepFitsCapabilities(step, capabilities) {
			return DispatchResult{}, fmt.Errorf("action exceeds current node capability")
		}
		envelope.TXData = append([]byte(nil), step.TXData...)
		envelope.ReadSize = step.ReadSize
		envelope.RXTimeoutMS = step.RXTimeoutMS
		envelope.PostTXDelayMS = step.PostTXDelayMS
	}

	wireDigest := channelCmdV2WireDigest(execution, attempt.AttemptNo, bootID, edge.ChannelID, deadline, step, planSteps)
	digest, err := digestBytes(wireDigest)
	if err != nil {
		return DispatchResult{}, err
	}
	envelope.PayloadDigest = digest

	payload, err := frame.EncodeChannelCmdV2(envelope)
	if err != nil {
		return DispatchResult{}, err
	}
	if err := t.mqtt.Publish(mqtt.ControlTopicForNode(execution.NodeID), payload); err != nil {
		return DispatchResult{}, fmt.Errorf("publish ChannelCmdV2: %w", err)
	}
	return DispatchResult{BootID: bootID, PublishedAt: t.now(), WireDigest: wireDigest}, nil
}

// channelCmdV2WireDigest binds the response identity to the final physical
// envelope rather than only to the logical request. This protects against
// stale manifests, changed leases/channels, deadlines and compiled steps
// producing the same logical request hash.
func channelCmdV2WireDigest(execution models.CommandExecution, attemptNo uint32, bootID string, channelID uint, deadline time.Time, step deviceaction.SingleStep, plan []frame.ChannelCmdV2Step) string {
	h := sha256.New()
	writeDigestString(h, "ehome.channel-cmd-v2")
	writeDigestUint32(h, 1)
	writeDigestString(h, execution.CommandID)
	writeDigestString(h, execution.ActionID)
	writeDigestUint32(h, uint32(execution.ActionVersion))
	writeDigestString(h, execution.RequestHash)
	writeDigestUint32(h, attemptNo)
	writeDigestString(h, bootID)
	writeDigestString(h, execution.ManifestID)
	writeDigestUint32(h, execution.CommandEngineRevision)
	writeDigestUint32(h, uint32(execution.EdgeDeviceID))
	writeDigestUint32(h, uint32(channelID))
	writeDigestUint64(h, uint64(deadline.UnixMilli()))
	writeDigestStep(h, 0, step)
	writeDigestUint32(h, uint32(len(plan)))
	for i, item := range plan {
		writeDigestUint32(h, uint32(i+1))
		writeDigestUint32(h, item.Kind)
		writeDigestUint32(h, item.ReadSize)
		writeDigestUint32(h, item.RXTimeoutMS)
		writeDigestUint32(h, item.PostTXDelayMS)
		writeDigestBytes(h, item.TXData)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeDigestStep(h interface{ Write([]byte) (int, error) }, index uint32, step deviceaction.SingleStep) {
	writeDigestUint32(h, index)
	writeDigestUint32(h, step.ReadSize)
	writeDigestUint32(h, step.RXTimeoutMS)
	writeDigestUint32(h, step.PostTXDelayMS)
	writeDigestBytes(h, step.TXData)
}

func writeDigestString(h interface{ Write([]byte) (int, error) }, value string) {
	writeDigestBytes(h, []byte(value))
}

func writeDigestBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func writeDigestUint32(h interface{ Write([]byte) (int, error) }, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = h.Write(encoded[:])
}

func writeDigestUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = h.Write(encoded[:])
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
