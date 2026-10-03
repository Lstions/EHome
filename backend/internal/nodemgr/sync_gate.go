package nodemgr

import (
	"fmt"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/google/uuid"
)

// SyncAction represents the decision outcome for a sync event.
type SyncAction int

const (
	SyncActionNone  SyncAction = iota // No action needed — device is in sync
	SyncActionFull                    // Send full ConfigManifest
	SyncActionDefer                   // Defer — within dedup window
)

// SyncDecision is the output of a SyncGate decision for a single device.
type SyncDecision struct {
	Action     SyncAction
	Reason     string // Human-readable reason for logging
	SyncID     string // UUID for observability
	ManifestID string // Manifest ID to send (if action is Full)
	DeviceID   string // Target device
}

// HelloMsg carries the parsed v2.1 Hello fields for SyncGate decision.
type HelloMsg struct {
	NodeID          string
	FirmwareVersion string
	Model           string
	ChannelCount    uint64
	ConfigEpoch     uint64
	NvsHasConfig    bool
	LastManifest    string
	ProtocolVersion string
}

// StatusReportMsg carries the parsed v2.2 StatusReport fields for SyncGate decision.
type StatusReportMsg struct {
	UptimeSec    uint64
	Status       string
	ChannelCount uint64
	ConfigEpoch  uint64
	SyncState    string
	ConfigHash   string // v2.2: config_hash from device
}

// ConfigQueryMsg carries the parsed ConfigSyncRequest fields.
type ConfigQueryMsg struct {
	Reason            string
	CurrentEpoch      uint64
	CurrentManifestID string
}

// SyncGate is the unified synchronization decision center.
// All sync entry points route through SyncGate for consistent decision-making.
type SyncGate struct {
	mgr      *Manager
	eventBus *ConfigEventBus

	// dedupMu / lastFullPushAt 实现 SyncActionDefer 的**去重窗口**：
	// 同一设备在窗口内重复触发的「全量 manifest 推送」被降级为 Defer。
	//
	// 2026-10-03 审查发现（P1）：SyncActionDefer 一直只有声明（见上方常量）
	// 与 metric 分支，**没有任何代码产生它** —— 也就是说去重窗口从未实现。
	// 此前无人在意，因为 hash_mismatch 分支每次 StatusReport 都推送，而心跳是 5s；
	// 心跳收到 1s 后，一个持续失配的节点（配置一直应用失败）会让全量 manifest
	// 推送频率 ×5。故在此把窗口真正实现出来，而不是新造第二套机制。
	dedupMu        sync.Mutex
	lastFullPushAt map[string]time.Time

	// now 可注入（测试用）；nil 时用 time.Now。
	now func() time.Time
}

// fullPushDedupWindow 是同一设备两次「全量 manifest 推送」之间的最小间隔。
//
// 取 5s = **收紧前的心跳周期**：这样把心跳从 5s 收到 1s 之后，一个持续失配的
// 节点收到的 manifest 推送频率**不会高于改动之前**。既保留原有的自愈速度，
// 又消除 ×5 的放大。只作用于周期心跳（OnStatusReport）触发的推送；
// 显式配置变更（OnConfigChange / OnHello / nvs_empty）必须立即推送。
const fullPushDedupWindow = 5 * time.Second

// NewSyncGate creates a new SyncGate.
func NewSyncGate(mgr *Manager, eventBus *ConfigEventBus) *SyncGate {
	return &SyncGate{
		mgr:            mgr,
		eventBus:       eventBus,
		lastFullPushAt: make(map[string]time.Time),
	}
}

// gateClock 返回当前时间（可注入）。
func (g *SyncGate) gateClock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// allowPeriodicFullPush 判断该设备是否已脱离去重窗口；若是则记录本次推送时间。
//
// 只应在**周期心跳**路径调用。返回 false 表示应降级为 SyncActionDefer。
func (g *SyncGate) allowPeriodicFullPush(deviceID string) bool {
	g.dedupMu.Lock()
	defer g.dedupMu.Unlock()
	now := g.gateClock()
	if last, ok := g.lastFullPushAt[deviceID]; ok && now.Sub(last) < fullPushDedupWindow {
		return false
	}
	g.lastFullPushAt[deviceID] = now
	return true
}

// recordDecision records a sync decision metric.
func recordDecision(d SyncDecision) {
	actionStr := "none"
	switch d.Action {
	case SyncActionFull:
		actionStr = "full"
	case SyncActionDefer:
		actionStr = "defer"
	}
	metrics.SyncDecisionsTotal.WithLabelValues(d.Reason, actionStr).Inc()
}

// decide is the single decision point for all sync logic.
// deviceHash: the hash reported by the device (from Hello last_manifest or StatusReport config_hash)
// nvsEmpty: true if device reports NVS has no config
// deviceChannelCount: number of channels the device reports
func (g *SyncGate) decide(deviceID string, deviceHash string, nvsEmpty bool,
	deviceChannelCount uint64) SyncDecision {

	syncID := uuid.New().String()

	if nvsEmpty {
		serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "nvs_empty",
			SyncID:     syncID,
			ManifestID: serverHash.ManifestID,
			DeviceID:   deviceID,
		}
		recordDecision(d)
		return d
	}

	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	if serverHash.Hash == "" {
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "no_server_config",
			SyncID:   syncID,
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}

	if deviceHash == serverHash.ManifestID {
		// hash 匹配 = 设备已持有正确配置。
		// 但 channel_count=0 且 nvs_has=1 表示设备 in-memory 配置为空
		// （重启后 NVS 有旧 manifest_id 但 config_mgr 未加载），
		// 必须强制推送让设备重建配置。
		if deviceChannelCount == 0 && !nvsEmpty && serverHash.ChannelCount > 0 {
			d := SyncDecision{
				Action:     SyncActionFull,
				Reason:     "force_push:hash_match_but_zero_channels",
				SyncID:     syncID,
				ManifestID: serverHash.ManifestID,
				DeviceID:   deviceID,
			}
			recordDecision(d)
			return d
		}
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "hash_match",
			SyncID:   syncID,
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}

	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "hash_mismatch",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// OnHello makes a sync decision when a device sends Hello.
func (g *SyncGate) OnHello(deviceID string, hello *HelloMsg) SyncDecision {
	return g.decide(deviceID, hello.LastManifest, !hello.NvsHasConfig, hello.ChannelCount)
}

// OnStatusReport makes a sync decision when a device sends StatusReport.
// CRITICAL: old firmware does not send config_hash — must short-circuit to avoid
// pushing config every 5 seconds (empty string != serverHash is always true).
func (g *SyncGate) OnStatusReport(deviceID string, rpt *StatusReportMsg) SyncDecision {
	if rpt.ConfigHash == "" {
		d := SyncDecision{
			Action:   SyncActionNone,
			Reason:   "no_config_hash_wait_for_hello",
			SyncID:   uuid.New().String(),
			DeviceID: deviceID,
		}
		recordDecision(d)
		return d
	}
	d := g.decide(deviceID, rpt.ConfigHash, false, rpt.ChannelCount)
	// 周期心跳触发的全量推送走去重窗口（2026-10-03）：心跳 5s→1s 后，
	// 一个持续 hash_mismatch 的节点会每 1s 收到一次全量 manifest。
	// 窗口内降级为 SyncActionDefer —— 这正是该常量声明的语义
	//（"Defer — within dedup window"），此前无代码产生它。
	//
	// 只对周期路径生效：OnConfigChange / OnHello / nvs_empty 走 decide 的其他分支，
	// 那些是**事件驱动**的显式变更，必须立即推送，不能被冷却期拖延。
	if d.Action == SyncActionFull && d.Reason == "hash_mismatch" && !g.allowPeriodicFullPush(deviceID) {
		deferred := SyncDecision{
			Action:   SyncActionDefer,
			Reason:   "hash_mismatch_within_dedup_window",
			SyncID:   d.SyncID,
			DeviceID: deviceID,
		}
		recordDecision(deferred)
		return deferred
	}
	return d
}

// OnConfigChange handles a ConfigChangeEvent from the bus.
// Global/empty node IDs have no broadcast semantics: callers must fan out to
// concrete affected nodes after their database transaction commits.
func (g *SyncGate) OnConfigChange(evt ConfigChangeEvent) []SyncDecision {
	if evt.NodeID == "" || evt.NodeID == "0" {
		logger.Warnf("rejecting config change with non-concrete node_id=%q", evt.NodeID)
		return nil
	}
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(evt.NodeID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     fmt.Sprintf("config_changed: type=%s action=%s", evt.Type, evt.Action),
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   evt.NodeID,
	}
	recordDecision(d)
	return []SyncDecision{d}
}

// OnServerStartup returns decisions for all online nodes.
// Pushes full config to every online device — ensures devices get
// any config changes that happened while the server was down.
func (g *SyncGate) OnServerStartup() []SyncDecision {
	deviceIDs := g.mgr.GetOnlineDeviceIDs()
	decisions := make([]SyncDecision, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		syncID := uuid.New().String()
		serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "server_startup",
			SyncID:     syncID,
			ManifestID: serverHash.ManifestID,
			DeviceID:   deviceID,
		}
		recordDecision(d)
		decisions = append(decisions, d)
	}
	return decisions
}

// OnConfigQuery handles an explicit config query from a device (0x13).
func (g *SyncGate) OnConfigQuery(deviceID string, q *ConfigQueryMsg) SyncDecision {
	return g.decide(deviceID, q.CurrentManifestID, false, 0)
}

// OnOfflineReconnect handles an offline→online transition for a device.
func (g *SyncGate) OnOfflineReconnect(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "offline_reconnect",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// OnFactoryReset handles a factory reset event for a device.
func (g *SyncGate) OnFactoryReset(deviceID string) SyncDecision {
	syncID := uuid.New().String()
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     "factory_reset",
		SyncID:     syncID,
		ManifestID: serverHash.ManifestID,
		DeviceID:   deviceID,
	}
	recordDecision(d)
	return d
}

// Start begins consuming events from the ConfigEventBus and processing them.
func (g *SyncGate) Start() {
	go func() {
		ch := g.eventBus.Subscribe()
		// Replay committed control-side-effect notifications before consuming
		// new in-memory events. This closes the event-bus-full/process-restart
		// gap without making the command Inbox depend on MQTT availability.
		g.replayConfigChangeOutbox()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					logger.Infof("SyncGate event consumer stopped")
					return
				}
				g.processConfigChange(evt)
			case <-ticker.C:
				g.replayConfigChangeOutbox()
			}
		}
	}()
}

func (g *SyncGate) processConfigChange(evt ConfigChangeEvent) bool {
	decisions := g.OnConfigChange(evt)
	processed := true
	for _, d := range decisions {
		if d.Action != SyncActionFull {
			continue
		}
		logger.Infof("[sync_id=%s] ConfigChange push: device=%s reason=%s",
			d.SyncID, d.DeviceID, d.Reason)
		if err := g.mgr.SendConfigManifestWithDecision(d); err != nil {
			processed = false
		}
	}
	if processed && evt.EventID != "" {
		now := time.Now().UTC()
		result := g.mgr.db.Model(&models.ConfigChangeOutbox{}).
			Where("event_id = ? AND state = ?", evt.EventID, "PENDING").
			Updates(map[string]interface{}{"state": "PROCESSED", "processed_at": now})
		if result.Error != nil {
			logger.Warnf("[event_id=%s] durable config event acknowledgement failed: %v", evt.EventID, result.Error)
			return false
		}
	}
	return processed
}

func (g *SyncGate) replayConfigChangeOutbox() {
	var pending []models.ConfigChangeOutbox
	if err := g.mgr.db.Where("state = ?", "PENDING").Order("created_at ASC").Limit(100).Find(&pending).Error; err != nil {
		logger.Warnf("SyncGate durable config event query failed: %v", err)
		return
	}
	for _, row := range pending {
		g.processConfigChange(ConfigChangeEvent{
			EventID: row.EventID, Type: ConfigChangeType(row.Type), Action: ConfigChangeAction(row.Action),
			NodeID: row.NodeID, EntityID: row.EntityID, Actor: row.Actor,
		})
	}
}
