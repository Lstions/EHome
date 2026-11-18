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
	// ErrCapabilityStale 是 ErrActionUnavailable 的**子类**（errors.Is 同时命中两者）:
	// 节点命令能力快照缺失或过期（current_capabilities 门禁的**可自愈**分支）。
	//
	// 覆盖范围刻意收窄：只挂"缺失/过期"，不挂"节点不支持 ChannelCmdV2"或能力解码
	// 失败 —— 后者刷新快照也不会好，挂上去会让将来的重试消费者空转。
	//
	// 消费者现状（如实记录，避免注释比代码活得久）：自动化走的是**执行前主动刷新**
	// （planner.ensureNodeCapabilityFresh），**不读**这个哨兵；它当前只用于可诊断
	// 分类（事件 detail 里能看出"这是刷新一下就好"的那一类失败）。
	ErrCapabilityStale = errors.New("node command capability is absent or stale")
	ErrInvalidParams        = errors.New("action parameters are invalid")
	ErrNotCancellable       = errors.New("execution is no longer cancellable")
	ErrInvalidResolution    = errors.New("manual resolution is invalid")
	ErrNotResolvable        = errors.New("execution is not unknown")
	ErrAlreadyResolved      = errors.New("execution already has a different manual resolution")
	ErrInvalidRequest       = errors.New("invalid command request")
)

// commandDeadline 是新建命令执行的 Deadline（生产恒 2 分钟）。
//
// 仿真需要秒级超时来验证「设备不回 Ack/Final ⇒ Deadline 后以明确终态 + 原因收尾」，
// 且该判据必须真等一次超期（RecoverExpired 每秒实跑，判据是库里的 deadline_at
// vs now，无法用注入时钟替代）。覆盖走 build tag（command_deadline_sim.go），
// **生产二进制里不存在这个变量**。
var commandDeadline = 2 * time.Minute

// CommandDeadline 返回新建命令执行的 Deadline 时长。
func CommandDeadline() time.Duration { return commandDeadline }

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
	// system 路径**不需要**确认令牌: 规则是用户保存时确认过的, 无人值守时不可能
	// 再点一次弹窗; 要不要"先问我"由规则上的 require_confirmed 决定 (planner 分流)。
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
	// 风险动作直接执行 (规则在保存时已由用户确认); 是否等人由 require_confirmed 决定。
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
	// Available 保持旧语义：运行期此刻能否真正执行（全门禁通过）。
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	// Configurable 是配置期可选：只包含驱动启用/引擎支持/设备与通道配置等
	// 非瞬态门禁。设备离线时 Configurable=true、Available=false —— 规则仍可保存。
	Configurable bool `json:"configurable"`
	// RuntimeReason/Code 只在"配置期可选、运行期不可执行"时给出。
	RuntimeReason     string `json:"runtime_reason,omitempty"`
	RuntimeReasonCode string `json:"runtime_reason_code,omitempty"`
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
// actionUnavailableError 把**失败的那一道门禁**带进错误链。
//
// errors.Is(err, ErrActionUnavailable) 仍然成立（错误链首段就是它），因此既有
// isGateError / 指标口径无需改动；而 err.Error() 从一句无信息量的通稿变成
// "action is unavailable for this device: gate=node_status reason=node_offline"，
// planner 已把 err.Error() 原样写进事件 detail，UI 立刻可诊断。
//
// current_capabilities 额外挂 ErrCapabilityStale，供自动化路径识别"可自愈"。
func actionUnavailableError(gate gateResult) error {
	detail := string(gate.name)
	if gate.reasonCode != "" {
		detail += " reason=" + gate.reasonCode
	}
	if gate.name == gateCurrentCapabilities && gate.reasonCode == "capability_stale" {
		// 只有这一种能力失败是**可自愈**的：刷新资源快照即可。
		// "节点不支持 ChannelCmdV2"/"能力解码失败"刷新一万次也没用，不能挂这个哨兵，
		// 否则未来任何"见哨兵就刷新重试"的消费者会在不可自愈场景里空转。
		return fmt.Errorf("%w: gate=%s reason=%s %w", ErrActionUnavailable, gate.name, gate.reasonCode, ErrCapabilityStale)
	}
	return fmt.Errorf("%w: gate=%s", ErrActionUnavailable, detail)
}

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
	appendResult(gateNodeStatus, edge.Node.Status == "online", "node_offline", "")
	appendResult(gateActionChannel, channelErr == nil, "", "")
	appendResult(gateReportedActionChannel, reportedErr == nil, "channel_not_reported", "")
	appendResult(gateAppliedManifest, manifestErr == nil, "manifest_not_applied", "")
	// reason 码与 Catalog 对齐：只有"快照缺失/过期"是 capability_stale（可自愈），
	// 其余（不支持 V2 / 能力解码失败）是 capability_unavailable（刷新也不会好）。
	capReason := ""
	if capabilitiesErr != nil {
		capReason = "capability_unavailable"
		if errors.Is(capabilitiesErr, ErrCapabilityStale) {
			capReason = "capability_stale"
		}
	}
	appendResult(gateCurrentCapabilities, capabilitiesErr == nil, capReason, "")
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
	appendResult(gateDefinitionFitsCapabilities, fitsPassed, "capability_mismatch", fitsReason)
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
	appendResult(gatePeriphConfig, periphErr == nil, "periph_config_unavailable", "")
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
	return s.CatalogFiltered(ctx, edgeDeviceID, "")
}

// CatalogFiltered 返回设备动作目录，可按 Exposure 过滤（automation/diagnostic/
// configuration）。空 purpose = 全部（设备详情页）；规则向导只取 automation。
func (s *Service) CatalogFiltered(ctx context.Context, edgeDeviceID uint, purpose string) ([]CatalogItem, error) {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).Preload("Node").First(&edge, edgeDeviceID).Error; err != nil {
		return nil, err
	}
	items := make([]CatalogItem, 0)
	capabilityStale := false
	for _, definition := range s.actions.List(edge.Type) {
		if purpose != "" && definition.Exposure != purpose {
			continue
		}
		item := CatalogItem{Definition: definition}
		// Evaluate every gate without short-circuiting so a channel failure
		// still annotates capability_stale in the same cases as before.
		gates := evaluateActionGates(s, s.db.WithContext(ctx), edge, definition, nil)
		firstFailed := firstFailedGateForCatalog(gates)
		firstConfig := firstFailedGateByClass(gates, false)
		firstRuntime := firstFailedGateByClass(gates, true)
		item.Available = firstFailed == nil
		item.Configurable = firstConfig == nil
		if firstFailed != nil {
			// Reason/ReasonCode 保持旧契约（first-failure-wins），避免改变既有
			// 客户端与门禁测试的注解语义；Configurable 是新增的独立维度。
			item.Reason = reasonForGate(firstFailed.name)
			item.ReasonCode = firstFailed.reasonCode
			if !s.dispatchEnabled {
				item.Reason = "device control v2 is disabled"
				item.ReasonCode = ""
			} else if !definition.Enabled && definition.AvailabilityCode != "" {
				item.Reason = definition.AvailabilityReason
				item.ReasonCode = definition.AvailabilityCode
			} else if firstFailed.name == gateDefinitionFitsCapabilities && firstFailed.reason != "" {
				item.Reason = firstFailed.reason
			}
		}
		if firstRuntime != nil {
			item.RuntimeReason = reasonForGate(firstRuntime.name)
			item.RuntimeReasonCode = firstRuntime.reasonCode
		}
		if firstFailed != nil && firstFailed.name == gateCurrentCapabilities &&
			(edge.Node.ResourceReportedAt == nil || s.now().Sub(*edge.Node.ResourceReportedAt) > MaxCapabilityAge) {
			// A browser may safely request a fresh ResourceReport and reload the
			// catalog. Keep this machine-readable so the UI never relies on the
			// presentation text or retries unrelated safety failures.
			item.ReasonCode = "capability_stale"
			item.RuntimeReasonCode = "capability_stale"
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

// gateIsRuntime 区分"配置期可选"与"运行期可执行"。运行期门禁只影响此刻能否
// 下发，不应阻止用户在规则向导里选择动作（设备离线仍可保存规则）。
func gateIsRuntime(name gateName) bool {
	switch name {
	case gateNodeStatus, gateReportedActionChannel, gateAppliedManifest,
		gateCurrentCapabilities, gateDefinitionFitsCapabilities:
		return true
	default:
		return false
	}
}

// firstFailedGateByClass 返回指定类别（配置期/运行期）的第一个失败门禁。
// Catalog 不参与 edgeNodeID（Create-only），因此与 firstFailedGateForCatalog 一样跳过。
func firstFailedGateByClass(gates []gateResult, runtime bool) *gateResult {
	for i := range gates {
		if gates[i].name == gateEdgeNodeID {
			continue
		}
		if gates[i].passed || gateIsRuntime(gates[i].name) != runtime {
			continue
		}
		return &gates[i]
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
		//
		// 2026-11-18: 拒绝原因不再被丢弃。旧实现只 return 哨兵, 12 道门禁的
		// 差异在事件表里塌缩成同一句 "action is unavailable for this device" ——
		// 生产上真机问题 (capability_stale) 因此无法从 UI 诊断。
		for _, gate := range evaluateActionGates(s, tx, edge, def, params) {
			if !gate.passed {
				return actionUnavailableError(gate)
			}
		}
		// 确认制门禁: 判定源收敛为**唯一**的 RiskLevel.RequiresConfirmation()
		// (方案 §3.3.3 落点 1 / §6.4)。此处不得再内联任何 risk 字面量比较 ——
		// 那正是 R3/A-9 防漂移门禁要抓的第二份判定。
		//
		// 2026-11-18 简化: 风险等级只决定"界面要不要弹窗提示", 不再承担授权语义。
		// - user 路径 (设备页/手动触发/确认执行): 风险动作必须持一次性令牌;
		// - system 路径 (自动化自动执行): 直接执行 —— 规则是用户保存时确认过的,
		//   无人值守时不可能再有人点一次弹窗。要不要"先问我"由规则上的
		//   require_confirmed 决定 (true 在 planner 侧分流为 pending_confirm)。
		needsConfirmation := def.RiskLevel.RequiresConfirmation() && in.ActorKind != ActorKindSystem
		if needsConfirmation {
			// 2026-10-15 用户裁定：确认弹窗只保留确认/取消，理由不再是必填
			// （可选审计备注）。确认的**实质**仍是 consumeConfirmation 对
			// 一次性 token 的消费 —— 留空理由不会绕过确认门。
			if utf8.RuneCountInString(in.Reason) > 512 {
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
			ParamsJSON: string(params), Status: StatusQueued, DeadlineAt: now.Add(CommandDeadline()), CreatedAt: now,
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
			Metadata: map[string]interface{}{"action_id": def.ID, "action_version": def.Version, "request_hash": hash, "risk_level": string(def.RiskLevel)},
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
	case errors.Is(err, ErrConfirmationRequired), errors.Is(err, ErrConfirmationInvalid), errors.Is(err, ErrConfirmationForbidden):
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
