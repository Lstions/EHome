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

// MaxCapabilityAge bounds how old the newest ResourceReport may be before the
// server stops trusting the node's ChannelCmdV2 capability facts.
//
// 为什么不是 5 分钟（2026-09-21 缺陷根因）：
// ESP32 固件**没有**周期性 ResourceReport。全固件只有三处触发点：
//   - hello_handshake.c:44  handle_hello_success()      —— 每次 Hello 握手成功后
//   - app_callbacks.c:363   handle_config_applied()     —— 每次 ConfigManifest 提交成功后
//   - handler_config.c:70   QueryResources(0x1A) 处理   —— 服务端主动查询时
//
// 而周期性 Hello 的间隔由周期 sync 决定：
//   - sync_manager.c:269-273  sync_manager_periodic_task() 每 60s 轮询一次
//   - sync_manager.c:295      should_request_sync(PERIODIC) 要求 > 600s
//
// 即稳态下最有把握的上报间隔约 600~660s（10~11 分钟）。
// 5 分钟 < 10 分钟 ⇒ **必然**周期性地把一台健康节点的能力判为过期。
// 这不是"证据不够新鲜"，而是"阈值与真实事件节奏不匹配"。
//
// 口径/来源待补：本条注释原先引用一组"生产实测"数字
// （19 次 ResourceReport / 84 分钟、stale 占 1912s/5029s = 38%、7 个窗口、最长 360s）。
// 仓库内**既无对应 artifact，也无复跑命令**，该组数字不可复核，故已移除。
// 若需重新引入定量结论，请先落盘证据（原始 ResourceReport 时序 + 复跑脚本），
// 并同时给出分母（窗口内事件数与节点数）、口径（stale 判定规则与采样粒度）与采集时刻。
// 结论不依赖那组数字：上面的固件节奏推导已足以证明 5 分钟阈值必然误判健康节点。
//
// resourceReportInterval 的取值依据（上界，不是平均值）：
//
//	稳态周期 sync 间隔 600s + 任务轮询粒度 60s = 660s。
//
// capabilityReportMargin 是裕量：覆盖 HelloAck 往返、MQTT 重连后的 Hello
// 重建、以及设备侧调度抖动。裕量 = 5 分钟，即 900s 里留 240s（约 36%）。
const resourceReportInterval = 10 * time.Minute
const capabilityReportMargin = 5 * time.Minute

// MaxCapabilityAge 必须是"固件上报节奏 + 裕量"，而不是一个凭感觉挑的数字。
// 下面的门禁测试（capability_window_alignment_test.go）把这条不变式钉死：
// 任何把 MaxCapabilityAge 调到 resourceReportInterval 以下的改动都会变红。
//
// 为什么放宽到这里**不会**让危险操作使用过期能力：
//  1. 能力快照只决定"服务端认为固件接受哪种 V2 信封形状"。请求的**物理正确性**
//     来自服务端冻结的 ActionDefinition + 参数，与快照新旧无关。
//  2. 快照"变得更差"的唯一现实途径是固件换代：OTA 后设备重启 ⇒ 新 BootID ⇒
//     handler_hello.go:338-341 清空 BootID/ResourceReportedAt/Revision ⇒
//     gate 立即 fail-closed，与年龄无关（BootID 清空使任何旧快照都不匹配）。
//  3. 快照描述的资源发生任何**服务端可见**的变化（通道增删、设备地址改写、
//     清单下发）都会走 ConfigManifest 提交 ⇒ app_callbacks.c:363 立刻重报。
//     也就是说：只有"什么都没发生"时快照才会变老 —— 旧快照在那种情况下
//     恰恰仍然是对的。
//  4. 写类动作另有更强的、按**动作**分级的证据门禁（不是按时间分级的）：
//     未冻结协议的写操作被 AvailabilityCode（protocol_unverified /
//     hardware_evidence_required）在注册期冻结，生产根本进不来；
//     clear_rainfall_write 走 bounded_sequence + readback 校验。
//     按风险调**时间窗**并不能替代这些门禁，只会再引入一个不匹配真实节奏的常量。
const MaxCapabilityAge = resourceReportInterval + capabilityReportMargin

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
		plan, planErr := definition.CompilePlanForAddress(params, edge.HardwareID)
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
