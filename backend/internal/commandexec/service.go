package commandexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrIdempotencyCollision = errors.New("idempotency key was already used with different request")
	ErrActionUnavailable    = errors.New("action is unavailable for this device")
	ErrInvalidParams        = errors.New("action parameters are invalid")
	ErrNotCancellable       = errors.New("execution is no longer cancellable")
	ErrInvalidResolution    = errors.New("manual resolution is invalid")
	ErrNotResolvable        = errors.New("execution is not unknown")
	ErrAlreadyResolved      = errors.New("execution already has a different manual resolution")
	ErrInvalidRequest       = errors.New("invalid command request")
)

const (
	ResolutionConfirmedSucceeded  = "CONFIRMED_SUCCEEDED"
	ResolutionConfirmedFailed     = "CONFIRMED_FAILED"
	ResolutionAcknowledgedUnknown = "ACKNOWLEDGED_UNKNOWN"
)

type Service struct {
	db              *gorm.DB
	actions         *deviceaction.Registry
	now             func() time.Time
	dispatchEnabled bool
}

func NewService(db *gorm.DB, actions *deviceaction.Registry) *Service {
	return &Service{db: db, actions: actions, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetDispatchEnabled(enabled bool) { s.dispatchEnabled = enabled }

func (s *Service) Database() *gorm.DB { return s.db }

type CreateInput struct {
	EdgeDeviceID uint
	ActorUserID  uint
	// ActorKind: "user" (默认, 人工操作) / "system" (自动化引擎/系统任务)。
	// system 路径跳过 medium/low risk 的 confirmation (高风险 high/critical 仍强制)。
	// 仅 Planner/内部系统调用方使用, 不接受 API 请求体直接指定。
	ActorKind         string
	ActionID          string
	Params            json.RawMessage
	IdempotencyKey    string
	SourceIP          string
	ConfirmationToken string
	Reason            string
}

const (
	// ActorKindUser 默认值, 人工操作 (走完整 confirmation 流程)。
	ActorKindUser = "user"
	// ActorKindSystem 系统内部调用 (automation planner/告警引擎)。
	// 跳过 low/medium 风险 confirmation, 但 high/critical 仍须 confirmation。
	ActorKindSystem = "system"
)

// actorTypeForAudit 把 CreateInput.ActorKind 映射到 SecurityAuditEvent.ActorType 取值。
// 空 / 未识别值统一回落 "user" (向后兼容: 现有 API 调用方均未传 ActorKind)。
func actorTypeForAudit(kind string) string {
	if kind == ActorKindSystem {
		return ActorKindSystem
	}
	return ActorKindUser
}

type ResolveUnknownInput struct {
	CommandID   string
	ActorUserID uint
	Outcome     string
	Reason      string
	SourceIP    string
}

type CatalogItem struct {
	Definition deviceaction.Definition `json:"definition"`
	Available  bool                    `json:"available"`
	Reason     string                  `json:"reason,omitempty"`
	ReasonCode string                  `json:"reason_code,omitempty"`
}

// gateName identifies one availability predicate shared by Catalog and Create.
// Keep the set in sync in both consumers — this single source of truth is the
// F4 deduplication. Non-symmetric predicates are marked with onlyCreate/onlyCatalog.
type gateName string

const (
	gateDispatchEnabled            gateName = "dispatch_enabled"
	gateActionEnabled              gateName = "action_enabled"
	gateCurrentEngine              gateName = "current_engine"
	gateEdgeEnabled                gateName = "edge_enabled"
	gateEdgeStatus                 gateName = "edge_status"
	gateEdgeNodeID                 gateName = "edge_node_id" // Create-only: Catalog has no such check
	gateNodeStatus                 gateName = "node_status"
	gateActionChannel              gateName = "action_channel"
	gateReportedActionChannel      gateName = "reported_action_channel"
	gateAppliedManifest            gateName = "applied_manifest"
	gateCurrentCapabilities        gateName = "current_capabilities"
	gateDefinitionFitsCapabilities gateName = "definition_fits_capabilities"
	// gatePeriphConfig is the periph_cmd-only gate: the GPIO/PWM config row
	// for the target node+resource must exist and be enabled. Peripherals
	// have no channel/manifest/capability facts, so the four channel-domain
	// gates above never apply to them (no fabricated channel data).
	gatePeriphConfig gateName = "periph_config"
)

// gateResult is one evaluated availability predicate. The whole predicate set
// is evaluated per action (no early return) so both consumers share one
// decision tree; Catalog folds results into the original first-failure-wins
// annotation, Create folds them into a short-circuit reject. This satisfies
// the F4 v2 constraint that the signature is a per-gate result set, not a
// single bool, while preserving the pre-refactor else-if chain semantics
// (e.g. a failing channel gate masks capability_stale, exactly as before).
type gateResult struct {
	name       gateName
	passed     bool
	reasonCode string
	reason     string
}

// evaluateActionGates runs the shared per-action predicate set and returns
// one gateResult per gate. The gates capture the *exact* predicate set of the
// old Catalog/Create availability checks:
//
//   - Catalog annotations follow the original decision chain:
//     dispatchEnabled → def.Enabled → CurrentEngineAllows → edge availability
//     → channel → capabilities → definition fits capabilities.
//   - gateActionEnabled reports definition.Enabled alone; a disabled-but-engine
//     allowed action is annotated "not enabled", an engine-rejected action is
//     annotated command_engine_gate — both as before.
//   - gateEdgeNodeID is Create-only (Create checks edge.NodeID == ""); Catalog
//     has no such check and it does not affect Catalog availability. The gate
//     is still evaluated so both consumers share one predicate list.
//   - capability_stale: the old Catalog surfaced it only when the *first*
//     failing gate was currentCapabilities; a channel failure masked it. This
//     shared version preserves that short-circuit annotation: an item whose
//     first failure is gateCurrentCapabilities and whose resource report is
//     stale gets the machine-readable code (never for definition-fits failures,
//     which are capability *value* mismatches, not staleness).
//   - definitionFitsCapabilities in Catalog is evaluated with canonical nil
//     params ({}), as before; Create passes the real canonical params.
//   - PeriphCmdAdapter definitions (GPIO/PWM) take an independent gate list:
//     the shared node-state gates plus gatePeriphConfig. GPIO/PWM have no
//     channel, manifest or capability facts, so the four channel-domain gates
//     are never evaluated for them (no fabricated channel data).
func evaluateActionGates(s *Service, tx *gorm.DB, edge models.EdgeDevice, definition deviceaction.Definition, params json.RawMessage) []gateResult {
	if definition.Transport == deviceaction.PeriphCmdAdapter {
		return evaluatePeriphActionGates(s, tx, edge, definition, params)
	}
	results := make([]gateResult, 0, 12)
	appendResult := func(name gateName, passed bool, reasonCode, reason string) {
		results = append(results, gateResult{name: name, passed: passed, reasonCode: reasonCode, reason: reason})
	}
	channelErr := loadActionChannelError(tx, edge)
	reportedErr := requireReportedActionChannelError(edge.Node, edge.ChannelID)
	manifestErr := requireAppliedManifestError(edge.Node, edge.Node.ConfigVersion)
	_, capabilities, capabilitiesErr := currentCapabilitiesError(edge.Node, s.now)

	appendResult(gateDispatchEnabled, s.dispatchEnabled, "", "")
	appendResult(gateActionEnabled, definition.Enabled, "", "")
	appendResult(gateCurrentEngine, deviceaction.CurrentEngineAllows(definition), "command_engine_gate", "action requires the future high-risk command engine")
	appendResult(gateEdgeEnabled, edge.Enabled, "", "")
	appendResult(gateEdgeStatus, edge.Status != "inactive", "", "")
	// gateEdgeNodeID is Create-only (Catalog has no equivalent check): the
	// edge must reference a node to have a transport.
	appendResult(gateEdgeNodeID, edge.NodeID != "", "", "")
	appendResult(gateNodeStatus, edge.Node.Status == "online", "", "")
	appendResult(gateActionChannel, channelErr == nil, "", "")
	appendResult(gateReportedActionChannel, reportedErr == nil, "", "")
	appendResult(gateAppliedManifest, manifestErr == nil, "", "")
	appendResult(gateCurrentCapabilities, capabilitiesErr == nil, "", "")
	fitsReason := ""
	fitsPassed := false
	if capabilitiesErr == nil {
		// Reflect the original Catalog behavior: compilation runs against the
		// canonicalized params, and a params set that cannot canonicalize
		// (e.g. a required-parameter action with no values) skips the fits
		// check entirely rather than failing it.
		fitsParams, canonErr := deviceaction.CanonicalizeParams(definition.InputSchema, params)
		if canonErr != nil {
			fitsPassed = true
		} else {
			fitsErr := definitionFitsCapabilities(definition, fitsParams, capabilities)
			fitsPassed = fitsErr == nil
			if fitsErr != nil {
				fitsReason = fitsErr.Error()
			}
		}
	}
	appendResult(gateDefinitionFitsCapabilities, fitsPassed, "", fitsReason)
	return results
}

// evaluatePeriphActionGates is the periph_cmd gate list (§A2 分流裁决):
// dispatch_enabled / action_enabled / current_engine / edge_enabled /
// edge_status / edge_node_id / node_status + gatePeriphConfig. The `edge`
// argument is a synthetic projection built by resolveActionTarget (ID carries
// the node DB id, NodeID the physical node id, Node the preloaded row);
// only node-level fields are read here.
func evaluatePeriphActionGates(s *Service, tx *gorm.DB, edge models.EdgeDevice, definition deviceaction.Definition, params json.RawMessage) []gateResult {
	results := make([]gateResult, 0, 8)
	appendResult := func(name gateName, passed bool, reasonCode, reason string) {
		results = append(results, gateResult{name: name, passed: passed, reasonCode: reasonCode, reason: reason})
	}
	appendResult(gateDispatchEnabled, s.dispatchEnabled, "", "")
	appendResult(gateActionEnabled, definition.Enabled, "", "")
	appendResult(gateCurrentEngine, deviceaction.CurrentEngineAllows(definition), "command_engine_gate", "action requires the future high-risk command engine")
	appendResult(gateEdgeEnabled, edge.Enabled, "", "")
	appendResult(gateEdgeStatus, edge.Status != "inactive", "", "")
	appendResult(gateEdgeNodeID, edge.NodeID != "", "", "")
	appendResult(gateNodeStatus, edge.Node.Status == "online", "", "")
	periphErr := requirePeriphConfig(tx, edge.NodeID, definition.ID, params)
	appendResult(gatePeriphConfig, periphErr == nil, "", "")
	return results
}

// requirePeriphConfig enforces the periph_cmd-specific gate: the GPIO/PWM
// config row for (node, resource) must exist and be enabled. params are the
// canonical action params produced by CanonicalizeParams (Create); Catalog
// passes nil, in which case an absent/invalid parameter set fails closed.
func requirePeriphConfig(tx *gorm.DB, nodeID, actionID string, params json.RawMessage) error {
	if strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("periph action has no target node")
	}
	switch actionID {
	case deviceaction.ActionGPIOSet:
		var p struct {
			Pin int `json:"pin"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("gpio params unavailable: %w", err)
		}
		var cfg models.GPIOConfig
		if err := tx.Where("node_id = ? AND pin = ?", nodeID, p.Pin).First(&cfg).Error; err != nil {
			return fmt.Errorf("gpio config for pin %d: %w", p.Pin, err)
		}
		if !cfg.Enabled {
			return fmt.Errorf("gpio config for pin %d is disabled", p.Pin)
		}
		return nil
	case deviceaction.ActionPWMSetDuty:
		var p struct {
			HardwareID string `json:"hardware_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("pwm params unavailable: %w", err)
		}
		var cfg models.PWMConfig
		if err := tx.Where("node_id = ? AND hardware_id = ?", nodeID, p.HardwareID).First(&cfg).Error; err != nil {
			return fmt.Errorf("pwm config for hardware_id %q: %w", p.HardwareID, err)
		}
		if !cfg.Enabled {
			return fmt.Errorf("pwm config for hardware_id %q is disabled", p.HardwareID)
		}
		return nil
	default:
		return fmt.Errorf("unknown periph action %q", actionID)
	}
}

// resolveActionTarget loads the admission target for in.ActionID.
// ChannelCmdV2 actions keep the edge_devices row semantics; periph_cmd
// actions interpret in.EdgeDeviceID as the nodes table DB id (裁决:
// action_device_id 对 GPIO 动作 = 节点 ID) and return a synthetic EdgeDevice
// projection whose ID is that node id — no edge_devices row is fabricated.
func resolveActionTarget(tx *gorm.DB, in CreateInput) (models.EdgeDevice, error) {
	if deviceaction.IsPeriphAction(in.ActionID) {
		var node models.Node
		if err := tx.First(&node, in.EdgeDeviceID).Error; err != nil {
			return models.EdgeDevice{}, err
		}
		return models.EdgeDevice{
			ID:      node.ID,
			NodeID:  node.NodeID,
			Type:    periphDeviceTypeFor(in.ActionID),
			Enabled: true,
			Status:  "active",
			Node:    node,
		}, nil
	}
	var edge models.EdgeDevice
	if err := tx.Preload("Node").First(&edge, in.EdgeDeviceID).Error; err != nil {
		return models.EdgeDevice{}, err
	}
	return edge, nil
}

// periphDeviceTypeFor maps a periph action to its catalog device type.
func periphDeviceTypeFor(actionID string) string {
	switch actionID {
	case deviceaction.ActionGPIOSet:
		return deviceaction.DeviceTypeGPIO
	case deviceaction.ActionPWMSetDuty:
		return deviceaction.DeviceTypePWM
	default:
		return ""
	}
}

// loadActionChannelError is the predicate wrapper for loadActionChannel.
func loadActionChannelError(tx *gorm.DB, edge models.EdgeDevice) error {
	_, err := loadActionChannel(tx, edge)
	return err
}

// requireReportedActionChannelError is the predicate wrapper for requireReportedActionChannel.
func requireReportedActionChannelError(node models.Node, channelID uint) error {
	return requireReportedActionChannel(node, channelID)
}

// requireAppliedManifestError is the predicate wrapper for requireAppliedManifest.
func requireAppliedManifestError(node models.Node, expected string) error {
	return requireAppliedManifest(node, expected)
}

// currentCapabilitiesError is the predicate wrapper for currentCapabilities.
func currentCapabilitiesError(node models.Node, now func() time.Time) (string, commandEngineCapabilities, error) {
	return currentCapabilities(node, now)
}

func (s *Service) Catalog(ctx context.Context, edgeDeviceID uint) ([]CatalogItem, error) {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).Preload("Node").First(&edge, edgeDeviceID).Error; err != nil {
		return nil, err
	}
	items := make([]CatalogItem, 0)
	capabilityStale := false
	for _, definition := range s.actions.List(edge.Type) {
		item := CatalogItem{Definition: definition}
		// Evaluate every gate without short-circuiting so a channel failure
		// still annotates capability_stale in the same cases as before.
		gates := evaluateActionGates(s, s.db.WithContext(ctx), edge, definition, nil)
		firstFailed := firstFailedGateForCatalog(gates)
		item.Available = firstFailed == nil
		if firstFailed != nil {
			item.Reason = reasonForGate(firstFailed.name)
			item.ReasonCode = firstFailed.reasonCode
			if !s.dispatchEnabled {
				// The dispatch gate is load-bearing and reported distinctly.
				item.Reason = "device control v2 is disabled"
				item.ReasonCode = ""
			} else if !definition.Enabled && definition.AvailabilityCode != "" {
				item.Reason = definition.AvailabilityReason
				item.ReasonCode = definition.AvailabilityCode
			} else if firstFailed.name == gateDefinitionFitsCapabilities && firstFailed.reason != "" {
				// Preserve the old fits-gate reason string (compile error).
				item.Reason = firstFailed.reason
			}
		}
		if firstFailed != nil && firstFailed.name == gateCurrentCapabilities &&
			(edge.Node.ResourceReportedAt == nil || s.now().Sub(*edge.Node.ResourceReportedAt) > MaxCapabilityAge) {
			// A browser may safely request a fresh ResourceReport and reload the
			// catalog. Keep this machine-readable so the UI never relies on the
			// presentation text or retries unrelated safety failures.
			item.ReasonCode = "capability_stale"
			capabilityStale = true
		}
		items = append(items, item)
	}
	if capabilityStale {
		metrics.DeviceActionCapabilityStaleTotal.Inc()
	}
	return items, nil
}

// firstFailedGate returns the first failing gate in predicate order, or nil.
func firstFailedGate(gates []gateResult) *gateResult {
	for i := range gates {
		if !gates[i].passed {
			return &gates[i]
		}
	}
	return nil
}

// firstFailedGateForCatalog is firstFailedGate for the Catalog consumer: the
// Catalog has no edge-node-id predicate (Create-only), so that gate must never
// influence Catalog availability.  This keeps the shared predicate set intact
// while preserving the original Catalog behavior for edge devices with an
// empty node reference.
func firstFailedGateForCatalog(gates []gateResult) *gateResult {
	for i := range gates {
		if gates[i].name == gateEdgeNodeID {
			continue
		}
		if !gates[i].passed {
			return &gates[i]
		}
	}
	return nil
}

// reasonForGate produces the Catalog annotation reason for a failed gate.
// These strings are the UI contract and must not change.
func reasonForGate(name gateName) string {
	switch name {
	case gateDispatchEnabled:
		return "device control v2 is disabled"
	case gateActionEnabled:
		return "action is not enabled for rollout"
	case gateCurrentEngine:
		return "action requires the future high-risk command engine"
	case gateEdgeEnabled, gateEdgeStatus, gateEdgeNodeID, gateNodeStatus:
		return "edge device or node is unavailable"
	case gateActionChannel, gateReportedActionChannel, gateAppliedManifest:
		return "action channel is unavailable"
	case gateCurrentCapabilities:
		return "ChannelCmdV2 capability is unavailable or stale"
	case gateDefinitionFitsCapabilities:
		return "action exceeds current node capability"
	case gatePeriphConfig:
		return "peripheral config is unavailable or disabled"
	default:
		return "action is unavailable"
	}
}

// Create persists execution, audit event and outbox in one transaction. No
// transport is touched here; the dispatcher is the only publication path.
func (s *Service) Create(ctx context.Context, in CreateInput) (*models.CommandExecution, bool, error) {
	if in.EdgeDeviceID == 0 || in.ActorUserID == 0 || strings.TrimSpace(in.ActionID) == "" || !validIdempotencyKey(in.IdempotencyKey) {
		metrics.DeviceActionAdmissionTotal.WithLabelValues("invalid").Inc()
		return nil, false, ErrInvalidRequest
	}
	var result models.CommandExecution
	replayed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		edge, err := resolveActionTarget(tx, in)
		if err != nil {
			return err
		}
		// Resolve the action and canonical request before checking mutable runtime
		// gates.  A retry with an existing idempotency key must return the durable
		// execution even when the node has since gone offline or its capability
		// report has become stale; it must never emit a second physical command.
		def, ok := s.actions.Get(edge.Type, in.ActionID)
		if !ok {
			return ErrActionUnavailable
		}
		params, err := deviceaction.CanonicalizeParams(def.InputSchema, in.Params)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidParams, err)
		}
		digest := sha256.Sum256(params)
		hash := hex.EncodeToString(digest[:])
		scope := fmt.Sprintf("user:%d:edge:%d:action:%s:v:%d", in.ActorUserID, edge.ID, def.ID, def.Version)
		var existing models.CommandExecution
		err = tx.Preload("ManualResolution").Where("idempotency_scope = ? AND idempotency_key = ?", scope, in.IdempotencyKey).First(&existing).Error
		if err == nil {
			if existing.RequestHash != hash {
				return ErrIdempotencyCollision
			}
			result, replayed = existing, true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// Availability gates. The first failing gate rejects the request.
		// This folds the exact shared predicate set from evaluateActionGates
		// (F4): the same single source of truth drives Catalog annotations.
		// The gate order deliberately mirrors the original Create checks so a
		// new gate failure still yields ErrActionUnavailable.
		for _, gate := range evaluateActionGates(s, tx, edge, def, params) {
			if !gate.passed {
				return ErrActionUnavailable
			}
		}
		// 确认制门禁: high/critical 一律须 confirmation; medium 仅人工路径须 confirmation
		// (system 路径放行 — 自动化引擎 IOC 场景, 已在 planner 层独立审计)。
		// low 本就不需要 confirmation (confirmationRequired 返回 false)。
		needsConfirmation := confirmationRequired(def.Risk)
		if in.ActorKind == ActorKindSystem && (def.Risk == "low" || def.Risk == "medium") {
			needsConfirmation = false
		}
		if needsConfirmation {
			if strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 512 {
				return ErrConfirmationRequired
			}
			if err := s.consumeConfirmation(tx, in.ConfirmationToken, in.ActorUserID, edge.ID, def, hash); err != nil {
				return err
			}
		}
		now := s.now()
		commandID, err := newCommandID()
		if err != nil {
			return err
		}
		result = models.CommandExecution{
			CommandID: commandID, EdgeDeviceID: edge.ID, NodeID: edge.NodeID,
			DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID, ManifestID: edge.Node.ConfigVersion,
			ActionID: def.ID, ActionVersion: def.Version, CommandEngineRevision: edge.Node.CommandEngineRevision, ActorUserID: in.ActorUserID,
			IdempotencyScope: scope, IdempotencyKey: in.IdempotencyKey, RequestHash: hash,
			ParamsJSON: string(params), Status: StatusQueued, DeadlineAt: now.Add(2 * time.Minute), CreatedAt: now,
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		outbox := models.CommandOutbox{CommandID: commandID, EventType: "command.dispatch", PayloadJSON: string(params), State: "PENDING", CreatedAt: now}
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		return audit.NewWriter(tx).Write(audit.Event{
			ActorType: actorTypeForAudit(in.ActorKind), ActorUserID: &in.ActorUserID, EventName: "device_action.created", Result: "queued",
			RequestID: commandID, SourceIP: in.SourceIP, TargetType: "edge_device", TargetID: fmt.Sprint(edge.ID),
			Metadata: map[string]interface{}{"action_id": def.ID, "action_version": def.Version, "request_hash": hash},
		})
	})
	if err == nil {
		resultLabel := "queued"
		if replayed {
			resultLabel = "replayed"
		}
		metrics.DeviceActionCreatedTotal.WithLabelValues(resultLabel).Inc()
	}
	metrics.DeviceActionAdmissionTotal.WithLabelValues(admissionMetricResult(err, replayed)).Inc()
	return &result, replayed, err
}

func (s *Service) Get(ctx context.Context, commandID string) (*models.CommandExecution, error) {
	var execution models.CommandExecution
	if err := s.db.WithContext(ctx).Preload("ManualResolution").First(&execution, "command_id = ?", commandID).Error; err != nil {
		return nil, err
	}
	return &execution, nil
}

// VerifyFinal applies the frozen action version and its trusted Driver
// verifier to a successful device Final.  It is intentionally kept inside the
// control domain so nodemgr cannot accidentally fall back to treating a setter
// ACK as generic sensor data.
func (s *Service) VerifyFinal(ctx context.Context, execution models.CommandExecution, raw []byte) ([]drivers.SensorData, error) {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).First(&edge, execution.EdgeDeviceID).Error; err != nil {
		return nil, err
	}
	if edge.NodeID != execution.NodeID || edge.Type != execution.DeviceType ||
		edge.DeviceConfigID != execution.DeviceConfigID || edge.ChannelID != execution.ChannelID {
		return nil, fmt.Errorf("edge device identity changed")
	}
	definition, ok := s.actions.Get(execution.DeviceType, execution.ActionID)
	if !ok || definition.Version != execution.ActionVersion {
		return nil, fmt.Errorf("action definition %s version %d is unavailable", execution.ActionID, execution.ActionVersion)
	}
	params, err := deviceaction.CanonicalizeParams(definition.InputSchema, json.RawMessage(execution.ParamsJSON))
	if err != nil || string(params) != execution.ParamsJSON {
		return nil, fmt.Errorf("persisted action parameters are invalid")
	}
	return definition.VerifyForAddress(params, raw, edge.HardwareID)
}

func (s *Service) List(ctx context.Context, edgeDeviceID uint, limit int) ([]models.CommandExecution, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var items []models.CommandExecution
	err := s.db.WithContext(ctx).Preload("ManualResolution").Where("edge_device_id = ?", edgeDeviceID).Order("created_at DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (s *Service) ResolveUnknown(ctx context.Context, in ResolveUnknownInput) (*models.CommandExecution, bool, error) {
	in.CommandID = strings.TrimSpace(in.CommandID)
	in.Outcome = strings.TrimSpace(in.Outcome)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.CommandID == "" || in.ActorUserID == 0 || !validResolutionOutcome(in.Outcome) || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 512 {
		return nil, false, ErrInvalidResolution
	}

	var execution models.CommandExecution
	replayed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "command_id = ?", in.CommandID).Error; err != nil {
			return err
		}
		if execution.Status != StatusUnknown {
			return ErrNotResolvable
		}
		var existing models.CommandManualResolution
		err := tx.First(&existing, "command_id = ?", in.CommandID).Error
		if err == nil {
			if existing.ResolvedBy == in.ActorUserID && existing.Outcome == in.Outcome && existing.Reason == in.Reason {
				execution.ManualResolution = &existing
				replayed = true
				return nil
			}
			return ErrAlreadyResolved
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		resolution := models.CommandManualResolution{
			CommandID: in.CommandID, Outcome: in.Outcome, Reason: in.Reason,
			ResolvedBy: in.ActorUserID, ResolvedAt: s.now(),
		}
		if err := tx.Create(&resolution).Error; err != nil {
			return err
		}
		if err := audit.NewWriter(tx).Write(audit.Event{
			ActorType: "user", ActorUserID: &in.ActorUserID, EventName: "device_action.unknown_resolved", Result: strings.ToLower(in.Outcome),
			RequestID: in.CommandID, SourceIP: in.SourceIP, TargetType: "edge_device", TargetID: fmt.Sprint(execution.EdgeDeviceID),
			Metadata: map[string]interface{}{"action_id": execution.ActionID, "outcome": in.Outcome, "reason": in.Reason},
		}); err != nil {
			return err
		}
		execution.ManualResolution = &resolution
		return nil
	})
	if err == nil {
		result := "resolved"
		if replayed {
			result = "replayed"
		}
		metrics.DeviceActionManualResolutionTotal.WithLabelValues(result, in.Outcome).Inc()
	}
	return &execution, replayed, err
}

func (s *Service) Cancel(ctx context.Context, commandID string, actor uint) (*models.CommandExecution, error) {
	var execution models.CommandExecution
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "command_id = ?", commandID).Error; err != nil {
			return err
		}
		if execution.ActorUserID != actor || execution.Status != StatusQueued {
			return ErrNotCancellable
		}
		now := s.now()
		updated := tx.Model(&models.CommandExecution{}).Where("command_id = ? AND status = ?", commandID, StatusQueued).Updates(map[string]interface{}{"status": StatusCancelled, "completed_at": now, "final_reason": "cancelled by actor"})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrNotCancellable
		}
		if err := tx.Model(&models.CommandOutbox{}).Where("command_id = ? AND state = ?", commandID, "PENDING").Update("state", "CANCELLED").Error; err != nil {
			return err
		}
		execution.Status, execution.CompletedAt, execution.FinalReason = StatusCancelled, &now, "cancelled by actor"
		return audit.NewWriter(tx).Write(audit.Event{ActorType: "user", ActorUserID: &actor, EventName: "device_action.cancelled", Result: "cancelled", RequestID: commandID, TargetType: "edge_device", TargetID: fmt.Sprint(execution.EdgeDeviceID), Metadata: map[string]interface{}{"action_id": execution.ActionID}})
	})
	return &execution, err
}

func validIdempotencyKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && strings.TrimSpace(key) == key
}

func validResolutionOutcome(outcome string) bool {
	switch outcome {
	case ResolutionConfirmedSucceeded, ResolutionConfirmedFailed, ResolutionAcknowledgedUnknown:
		return true
	default:
		return false
	}
}

func admissionMetricResult(err error, replayed bool) string {
	if err == nil {
		if replayed {
			return "replayed"
		}
		return "queued"
	}
	switch {
	case errors.Is(err, ErrIdempotencyCollision):
		return "collision"
	case errors.Is(err, ErrInvalidParams):
		return "invalid"
	case errors.Is(err, ErrActionUnavailable):
		return "unavailable"
	case errors.Is(err, ErrConfirmationRequired), errors.Is(err, ErrConfirmationInvalid), errors.Is(err, ErrRecentAuthRequired):
		return "confirmation"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "not_found"
	default:
		return "error"
	}
}

func stepFitsCapabilities(step deviceaction.SingleStep, capabilities commandEngineCapabilities) bool {
	return len(step.TXData) > 0 && uint32(len(step.TXData)) <= capabilities.MaxTXBytes &&
		step.ReadSize <= capabilities.MaxRXBytes && step.RXTimeoutMS > 0 &&
		step.RXTimeoutMS <= capabilities.MaxStepTimeoutMS &&
		step.PostTXDelayMS <= capabilities.MaxStepTimeoutMS
}

func definitionFitsCapabilities(def deviceaction.Definition, params json.RawMessage, capabilities commandEngineCapabilities) error {
	if def.ExecutionShape == "bounded_sequence" {
		if !capabilities.SupportsBoundedBatch || capabilities.MaxBatchSteps == 0 {
			return fmt.Errorf("bounded batch capability is unavailable")
		}
		plan, err := def.CompilePlanForAddress(params, "1") // fit check is address-agnostic; compile errors surface in dispatch
		if err != nil {
			return err
		}
		if len(plan.Steps) > int(capabilities.MaxBatchSteps) {
			return fmt.Errorf("action exceeds node batch-step capability")
		}
		for _, planStep := range plan.Steps {
			if !stepFitsCapabilities(planStep.SingleStep, capabilities) {
				return fmt.Errorf("action exceeds current node capability")
			}
		}
		return nil
	}
	step, err := def.Compile(params)
	if err != nil || !stepFitsCapabilities(step, capabilities) {
		if err != nil {
			return err
		}
		return fmt.Errorf("action exceeds current node capability")
	}
	return nil
}

func newCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// UUIDv4 textual form keeps APIs/database operators familiar without a new dependency.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
