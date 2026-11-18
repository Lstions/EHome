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

	// NodeConfigFailed 是**服务端权威状态**：该节点当前 config_status/config_sync_state
	// 为 failed（由 ConfigResult(success=false) 或服务端拒绝 manifest 写入）。
	//
	// 为什么 SyncGate 需要它：hash 相等 / 无 hash 都只说明"设备当前持有的配置"，
	// 不说明"设备成功应用了它"。设备一旦应用失败就会丢弃暂存的新 manifest
	// （固件 config_mgr_discard_staged_manifest），active 仍是旧值，于是它
	// 继续上报**旧** hash —— 服务端据此判定 hash_match 而停止下发，双方互等。
	// failed 是唯一能打破这个对称的服务端证据。详见 OnStatusReport。
	NodeConfigFailed bool
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

	// stuckMu / stuckSince / stuckRepushedAt 实现「设备自报 sync_state 长期非 idle」
	// 的**卡死自愈**（2026-11-18）。
	//
	// 现场：C6 节点 F0F5BD02DD84（fw 2.8.0）连续 11 小时每条 StatusReport 都是
	// sync_state=1(syncing)、epoch=0，12 小时内只有 1 次 Hello。设备在等一次同步
	// 事务完成，而服务端因为 manifest hash 相等一直在 skip（Hello skip: reason=hash_match）
	// —— 双方都没做错动作，所以日志里既没有 sent 也没有 rejected。这与 2026-10-05
	// config_failed 那次是**同一形状**的死锁，只是打破对称的证据从「服务端判 failed」
	// 换成了「设备自报卡死」。
	//
	// 生命周期（2026-11-18 D22）：这两个 map 只增不减，但与既有 lastFullPushAt
	// 同款 —— 键是设备 ID，上界 = 设备总数，单条 ~几十字节。当前没有"删除节点"
	// 的 API 路径（边缘设备删除走 purge，节点行保留），所以不存在可观察的泄漏；
	// 若将来新增节点删除，**必须**在这里同步清理（否则同一 ID 复用时残留
	// stuckSince 会立刻触发一次重推）。
	stuckMu         sync.Mutex
	stuckSince      map[string]time.Time // 首次观察到非 idle 的时刻
	stuckRepushedAt map[string]time.Time // 最近一次为卡死重下发的时刻

	// now 可注入（测试用）；nil 时用 time.Now。
	now func() time.Time
}

// 卡死自愈的时间参数。
//
// 正常一次同步是秒级（Hello → manifest → ConfigResult），3 分钟仍非 idle 就已
// 不可能是「正在同步中」；重下发节流取 5 分钟：卡死是**稳态**，喷 manifest 没有
// 额外收益，只会把「配置卡死」放大成「总线过载」（与 fullPushDedupWindow 同源
// 取舍，只是周期更长 —— 这里本来就不指望每推一次都能修好）。
const (
	stuckSyncGrace          = 3 * time.Minute
	stuckSyncRepushInterval = 5 * time.Minute
)

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
		mgr:             mgr,
		eventBus:        eventBus,
		lastFullPushAt:  make(map[string]time.Time),
		stuckSince:      make(map[string]time.Time),
		stuckRepushedAt: make(map[string]time.Time),
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
	// === 服务端已判 failed 的节点：hash 无法证明它应用成功，必须重新下发 ===
	//
	// 这是 2026-10-05 S3(30EDA0A9A808) "config_status=failed 且服务端停止下发"
	// 的根因修复。设备侧证据（固件 app_callbacks.c）：
	//   事务失败 → config_mgr_discard_staged_manifest() → active 仍是**旧**配置
	//   → ConfigResult(success=false) → 此后每次 StatusReport 都上报**旧** manifest_id。
	// 服务端侧证据（本文件）：
	//   旧逻辑先判 hash 相等就返回 hash_match(none)，于是**永远**不再下发；
	//   设备在等一份新 manifest，服务端在等设备改变 hash —— 双向死锁，
	//   双方都没做错任何"动作"，所以日志里既没有 sent 也没有 rejected。
	//
	// failed 是打破这个对称的**服务端权威**信号。这里刻意放在 hash 比较**之前**，
	// 因为 hash 相等恰恰就是死锁的那个条件（放在之后等于没修）。
	//
	// 为什么不会打爆节点：本分支与周期失配推送**共用**同一个 5s 去重窗口，
	// 频率不高于改动之前；且设备在应用失败时会明确回 ConfigResult(false)，
	// 不会出现"假装成功"的抖动。
	if rpt.NodeConfigFailed {
		// 服务端无配置可发时不要制造空 manifest：此时设备拿到的只会是空配置，
		// 把它标成 syncing 反而会掩盖"服务端侧缺配置"这个真实问题。
		// 走与 decide() 一致的 no_server_config 语义。
		serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
		if serverHash.Hash == "" {
			d := SyncDecision{
				Action:   SyncActionNone,
				Reason:   "no_server_config",
				SyncID:   uuid.New().String(),
				DeviceID: deviceID,
			}
			recordDecision(d)
			return d
		}
		d := SyncDecision{
			Action:     SyncActionFull,
			Reason:     "hash_match_but_config_failed",
			SyncID:     uuid.New().String(),
			DeviceID:   deviceID,
			ManifestID: serverHash.ManifestID,
		}
		if !g.allowPeriodicFullPush(deviceID) {
			deferred := SyncDecision{
				Action:   SyncActionDefer,
				Reason:   "config_failed_within_dedup_window",
				SyncID:   d.SyncID,
				DeviceID: deviceID,
			}
			recordDecision(deferred)
			return deferred
		}
		recordDecision(d)
		return d
	}

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

	// === 设备自报「同步卡死」：hash 相等同样不能证明同步已完成 ===
	//
	// 必须放在 hash 比较**之前**（hash 相等恰恰就是死锁的那个条件，放在之后等于
	// 没修）；但必须放在**老固件守卫之后** —— 不报 config_hash 的固件原先"永不重推"，
	// 让它每 5 分钟收一份全量 manifest 是行为变更，不在本次修复范围内。
	if d, ok := g.reconcileStuckSync(deviceID, rpt); ok {
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

// reconcileStuckSync 处理「设备自报长期非 idle」的同步卡死自愈。
//
// 返回 ok=true 表示本函数已经给出决策，调用方必须直接采用（不得再走 hash 比较）。
// ok=false 表示「要么健康、要么还在宽限期、要么刚推过」，交给原有逻辑。
//
// 为什么这个信号可信：sync_state 是**设备状态机**的自述，不是内容比对结果。
// hash 相等只说明「设备持有的配置与我一致」，不说明「设备那次同步事务走完了」。
// 而设备的卡死形态是：置 SYNCING → 发 Hello → 收到 hash_match 的 skip（无 manifest）
// → 永远等不到让状态回 IDLE 的那条下行帧 ⇒ 双方互等。
func (g *SyncGate) reconcileStuckSync(deviceID string, rpt *StatusReportMsg) (SyncDecision, bool) {
	now := g.gateClock()
	if rpt.SyncState == "" || rpt.SyncState == "idle" {
		g.clearStuckSync(deviceID)
		return SyncDecision{}, false
	}
	// 先用**廉价判据**筛一遍（宽限期 / 节流），再算 manifest hash。
	//
	// 为什么顺序重要（2026-11-18 复审指出）：CalcConfigHashForDevice 是 1 个事务 +
	// 约 7 张表查询 + 全量 manifest SHA256；而生产节点恰恰长期处于非 idle，心跳 1Hz。
	// 先算 hash 再判节流，等于每个非 idle 心跳都白算一次（本函数一次 + decide() 一次）。
	// 廉价判据不成立就直接返回，常态心跳退回基线的成本。
	g.stuckMu.Lock()
	since, seen := g.stuckSince[deviceID]
	if !seen {
		g.stuckSince[deviceID] = now
		since = now
	}
	lastPush, pushed := g.stuckRepushedAt[deviceID]
	mayPush := now.Sub(since) >= stuckSyncGrace &&
		(!pushed || now.Sub(lastPush) >= stuckSyncRepushInterval)
	g.stuckMu.Unlock()
	if !mayPush {
		return SyncDecision{}, false
	}
	serverHash := g.mgr.CalcConfigHashForDevice(deviceID)
	if serverHash.Hash == "" {
		// 服务端自己没配置可发：制造空 manifest 只会掩盖真实原因（与 decide() 的
		// no_server_config 语义保持一致），交给原有路径如实返回。
		// 同时清掉卡死观测：否则残留的 stuckSince 会让"后来真的配上配置"的那一刻立刻重推。
		g.clearStuckSync(deviceID)
		return SyncDecision{}, false
	}
	hashMatch := rpt.ConfigHash == serverHash.ManifestID

	// 复判 + 置位必须在**同一个临界区**里。卡死态本身就是每秒一条 StatusReport，
	// 旧写法"先读 lastPush → 决定 → 回写"会让两条并发上报同时看到"该推"，
	// 各推一份全量 manifest（重复下发不致命，但会把卡死放大成总线过载）。
	// 上面那次是**廉价预判**（可能因为算 hash 花了几毫秒而过期），这里才是判定点。
	//
	// 锁序固定 stuckMu → dedupMu（allowPeriodicFullPush 内部取 dedupMu）；
	// 全文件没有反向获取路径，故不会死锁。
	g.stuckMu.Lock()
	since, seen = g.stuckSince[deviceID]
	if !seen {
		g.stuckSince[deviceID] = now
		since = now
	}
	lastPush, pushed = g.stuckRepushedAt[deviceID]
	eligible := now.Sub(since) >= stuckSyncGrace &&
		(!pushed || now.Sub(lastPush) >= stuckSyncRepushInterval)
	if eligible && !g.allowPeriodicFullPush(deviceID) {
		// 与周期失配推送共用 5s 去重窗口：两条路径不能在同一秒各推一份全量 manifest。
		eligible = false
	}
	if eligible {
		g.stuckRepushedAt[deviceID] = now
	}
	g.stuckMu.Unlock()
	if !eligible {
		return SyncDecision{}, false
	}

	// reason 必须如实描述判据：早期版本无条件写 hash_match_but_sync_stuck，
	// 而代码根本没比较 hash —— 日志/metric 会给出假的"hash 相等"结论。
	reason := "sync_stuck_hash_mismatch"
	switch {
	case rpt.SyncState == "error":
		reason = "sync_error_repush" // error 与 syncing 语义不同（前者是请求失败）
	case hashMatch:
		reason = "hash_match_but_sync_stuck"
	}
	d := SyncDecision{
		Action:     SyncActionFull,
		Reason:     reason,
		SyncID:     uuid.New().String(),
		DeviceID:   deviceID,
		ManifestID: serverHash.ManifestID,
	}
	recordDecision(d)
	logger.Warnf("[%s] 设备自报 sync_state=%s 已超过 %s 未回 idle（hash_match=%v）：疑似同步卡死，强制重下发 manifest",
		deviceID, rpt.SyncState, stuckSyncGrace, hashMatch)
	return d, true
}

// clearStuckSync 清掉卡死观测（设备回到 idle 即视为已恢复）。
func (g *SyncGate) clearStuckSync(deviceID string) {
	g.stuckMu.Lock()
	defer g.stuckMu.Unlock()
	delete(g.stuckSince, deviceID)
	delete(g.stuckRepushedAt, deviceID)
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
