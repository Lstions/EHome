// Package alert 实现阈值告警引擎 (方案 v0.4 §5 任务C)。
//
// 挂接点裁决 (§5.1.2): DataEvent 只携带 RawData 字节, 物理量在
// SensorParserConsumer 内部解析为 []parser.Field。Evaluator 通过解析后回调
// (alertSink) 接入, 复用 latestSink 先例 (原与 rollupSink 同点), 避免独立 consumer 的
// 重复解析开销; 分片模式下回调随分片并发, 天然并行求值。
package alert

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	stateFiring   = "firing"
	stateResolved = "resolved"

	// maxWindowSamples 超载保护: 单规则滑动窗口样本上限,
	// 超出丢最旧并计数 (对齐 databus fail-open 风格, §5.1.2)。
	maxWindowSamples = 1000

	defaultSilenceSec = 300
)

// sample 滑动窗口单点。
type sample struct {
	at        time.Time
	value     float64
	satisfied bool
	// identity 是窗口来源身份（S1/S2：窗口不得跨来源拼接；见 source.go）。
	identity sourceIdentity
}

// Evaluator 阈值求值器: 规则缓存 + 每规则滑动窗口 + 状态机。
type Evaluator struct {
	db *gorm.DB
	// broadcast WS 广播回调 (websocket.Hub.BroadcastEvent), main.go 注入,
	// 避免 alert→websocket 编译期依赖。nil 时跳过广播。
	broadcast func(eventType string, payload any)
	// notifier 通知写入+投递入口 (D-1 步骤 3, 设计/外发通知通道.md §2.1 取向 A):
	// main.go 经 SetNotifier 注入 notify.Dispatcher。
	// **nil 是显式支持的合法状态** (而非"碰巧没 nil"): 既有测试与任何直接构造
	// Evaluator 的代码不注入时, 回落为直接写 notifications 表 —— 行为与改造前
	// 逐字节等价 (见 createNotification 注释)。
	notifier notify.Notifier

	mu sync.RWMutex
	// rules 规则缓存 (仅 enabled 规则), key: rule ID。
	rules map[uint]models.AlertRule
	// rulesSnap 是 rules 的不可变快照（LoadRules/Invalidate 时整体重建）。
	// 与 automation 同因（硬性能门禁 #10a-2）：Evaluate 每帧按值拷贝规则数组会让
	// 帧开销随规则数增长（AlertRule 也含来源指针列）。快照不可变 ⇒ 无锁零拷贝读取。
	rulesSnap atomic.Pointer[[]models.AlertRule]
	// windows 每规则滑动窗口, key: rule ID。
	windows map[uint][]sample
	// firing 规则当前是否处于 firing 态 (内存态)。
	firing map[uint]bool
	// lastNotifyAt 每规则最近一次通知时间 (SilenceSec 抑制用)。
	lastNotifyAt map[uint]time.Time

	// planProvider 数据字段解析层控制面（设计 §7.2）；nil=未接线，字面量走历史
	// matchField（逐字节等价），特殊形态 fail-closed。
	planProvider PlanProvider
	// plans 编译产物旁路缓存，key=(ruleID,slot,deviceID)。
	plans *planCache
	// auditMu/unresolvedLog 解析失败日志的 5 分钟节流表（key: ruleID+selectorHash）。
	auditMu       sync.Mutex
	unresolvedLog map[unresolvedKey]time.Time
	// statsMu/ruleStats 是 ehome_rule_never_resolved/_never_fired 的 kind="alert" 口径。
	statsMu   sync.Mutex
	ruleStats map[uint]*ruleStat

	// sourceResolver 来源组解析器（nil=未接线 ⇒ 组规则 fail-closed 并 Warn）。
	sourceResolver SourceResolver
	// latestValueBySensorFn 复合组逐成员取值注入点（生产=api.LatestValueBySensor）。
	latestValueBySensorFn func(deviceID uint, sensorName string) (models.UnifiedData, bool)
	// deviceIntervalFn 设备采集间隔（S8 gap 上限=2×间隔；nil/0 ⇒ 不启用）。
	deviceIntervalFn func(edgeDeviceID uint) time.Duration
	// rulesByGroup groupID → ruleID 反向索引（切换钩子按组清窗/关卡）。
	rulesByGroup map[uint][]uint
	// sourceHealthFn 是「来源降级可见性」回调（写 source_group_health）。
	// 与 automation 同签名同语义；nil=未接线（只落日志，不写健康表）。
	sourceHealthFn func(groupID uint, reason string)

	stopCh chan struct{}
	once   sync.Once
}

// NewEvaluator 构造求值器并全量加载 enabled 规则缓存。
func NewEvaluator(db *gorm.DB, broadcast func(eventType string, payload any)) *Evaluator {
	e := &Evaluator{
		db:           db,
		broadcast:    broadcast,
		rules:        make(map[uint]models.AlertRule),
		windows:      make(map[uint][]sample),
		firing:        make(map[uint]bool),
		lastNotifyAt:  make(map[uint]time.Time),
		plans:         newPlanCache(),
		unresolvedLog: make(map[unresolvedKey]time.Time),
		ruleStats:     make(map[uint]*ruleStat),
		rulesByGroup:  make(map[uint][]uint),
		stopCh:        make(chan struct{}),
	}
	e.LoadRules()
	return e
}

// SetNotifier 注入"通知写入 + 投递"入口 (D-1 步骤 3, main.go 接线)。
//
// 契约: 注入后通知经它落库并顺带外发投递; 传 nil 显式回落为直接写 notifications 表。
// 采用窄接口 (notify.Notifier) 而非 *notify.Dispatcher, 与本包已有的 broadcast 函数
// 注入同思路 —— 避免 alert→(外发 HTTP 引擎) 的编译期依赖。
func (e *Evaluator) SetNotifier(n notify.Notifier) {
	e.notifier = n
}

// LoadRules 全量加载 enabled 规则到缓存 (CRUD 写路径经 Invalidate 即时调用;
// Start 兜底刷新仅作保险, §5.1.2 v0.3 修正)。
func (e *Evaluator) LoadRules() {
	var rules []models.AlertRule
	if err := e.db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		logger.Warn("alert: failed to load rules", "error", err)
		return
	}
	next := make(map[uint]models.AlertRule, len(rules))
	for _, r := range rules {
		// H-3：非法 source_kind ⇒ Warn + 计数，不跳过、不 fail-closed，保持老路求值。
		if models.InvalidSourceKind(r.SourceKind) {
			logger.Warn("alert: rule has invalid source_kind, falling back to edge_device",
				"rule_id", r.ID, "source_kind", *r.SourceKind)
			metrics.RuleInvalidSourceKindTotal.WithLabelValues("alert").Inc()
		}
		next[r.ID] = r
	}
	e.mu.Lock()
	// 必须先保留 prev 快照：内容变化清窗口需要 old vs new 比较
	// （窄复核 R2-followup S2；旧实现只 swap+删，无法检测"规则仍在但语义变了"）。
	prev := e.rules
	e.rules = next
	var updatedWhileFiring []models.AlertRule
	for id, old := range prev {
		nr, still := next[id]
		if !still {
			// 移除/禁用：清窗口与状态（既有语义）。
			delete(e.windows, id)
			delete(e.firing, id)
			delete(e.lastNotifyAt, id)
			continue
		}
		if sameAlertTriggerState(old, nr) {
			continue // 仅 UpdatedAt/Name/SilenceSec 等簿记字段漂移（含 last_triggered_at 簿记触发 UpdatedAt）⇒ 不清
		}
		if alertTriggerNeedsWindowReset(old, nr) {
			// 绑定（sensor_name/target）或 DurationSec 变化：样本语义不再成立，必须清。
			// 绑定变化尤其危险：窗口按 ruleID 存、不按目标设备分区，A 的旧样本会参与
			// B 的 windowSatisfied 判定（窄复核 F1#⑤，真误触发）。
			delete(e.windows, id)
		} else {
			// 只改阈值/比较符：sample 持有原始 value ⇒ 可**重算** satisfied，保留
			// DurationSec 连续性（窄复核 F2#3；automation 的 sample 无原始值，只能清）。
			if win, ok := e.windows[id]; ok {
				for i := range win {
					win[i].satisfied = compare(nr.Comparator, win[i].value, nr.Threshold)
				}
				e.windows[id] = win
			}
		}
		if e.firing[id] {
			// 级联裁决（F3）：内容变化时若 firing=true 则关闭陈旧 firing，否则改阈值后
			// "新条件永不满足 + 设备离线"会让告警永久停在 firing（陈旧假警）。
			updatedWhileFiring = append(updatedWhileFiring, nr)
			delete(e.firing, id)
			delete(e.lastNotifyAt, id)
		}
	}
	// 来源组反向索引（切换钩子按组清窗/关闭 firing）。
	byGroup := make(map[uint][]uint)
	for id, r := range next {
		if ref := r.SourceRef(); ref.IsGroup() {
			byGroup[ref.GroupID] = append(byGroup[ref.GroupID], id)
		}
	}
	e.rulesByGroup = byGroup
	// 发布热路径规则快照（锁内构建，避免读者看到半更新状态）。
	e.rulesSnap.Store(makeAlertRulesSnapshot(next))
	e.mu.Unlock()
	// DB 写放在锁外（失败只告警，不影响规则加载）。
	for _, rule := range updatedWhileFiring {
		e.resolveUpdatedFiring(rule)
	}
	// 设计 §7.2：LoadRules 后清空编译产物（selector/epoch 变化惰性重编译）。
	e.plans.reset()
	// 指标口径随规则集合重建（保留仍存在规则的进度）。
	e.rebuildRuleStats(next)
	// 清理已删除规则的日志节流条目，避免 map 无界增长。
	e.auditMu.Lock()
	for key := range e.unresolvedLog {
		if _, ok := next[key.ruleID]; !ok {
			delete(e.unresolvedLog, key)
		}
	}
	e.auditMu.Unlock()
}

// makeAlertRulesSnapshot 构建不可变告警规则快照（语义同原每帧浅拷贝）。
func makeAlertRulesSnapshot(m map[uint]models.AlertRule) *[]models.AlertRule {
	snap := make([]models.AlertRule, 0, len(m))
	for _, r := range m {
		snap = append(snap, r)
	}
	return &snap
}

// sameAlertTriggerState 是告警求值状态的**内容白名单**（窄复核 R2-followup S1）。
//
// 只含决定 satisfied 布尔是否有意义的字段；**排除簿记字段**：UpdatedAt（触发/手动/
// 回填都会 bump）、Name、SilenceSec、Enabled、last_triggered_at。否则 30s 兜底
// LoadRules 会周期性清窗口 ⇒ DurationSec 永不满足（A-17 教训的镜像，漏触发方向）。
func sameAlertTriggerState(prev, next models.AlertRule) bool {
	return prev.TargetType == next.TargetType &&
		prev.TargetID == next.TargetID &&
		prev.SensorName == next.SensorName &&
		prev.Comparator == next.Comparator &&
		prev.Threshold == next.Threshold &&
		prev.DurationSec == next.DurationSec &&
		// 来源绑定变化（M0）：换组/换锚目标都改变窗口语义 ⇒ 必须清窗。
		sourceKindEq(prev.SourceKind, next.SourceKind) &&
		uintPtrEq(prev.SourceGroupID, next.SourceGroupID)
}

// alertTriggerNeedsWindowReset 判定变化是否需要**丢弃**窗口样本：
// 绑定目标/物理量变化、DurationSec 变化 ⇒ 丢弃；仅阈值/比较符变化 ⇒ 可重算。
func alertTriggerNeedsWindowReset(prev, next models.AlertRule) bool {
	return prev.TargetType != next.TargetType ||
		prev.TargetID != next.TargetID ||
		prev.SensorName != next.SensorName ||
		prev.DurationSec != next.DurationSec
}

// resolveUpdatedFiring 关闭"因规则内容变更而陈旧的 firing 事件"（F3 裁决）。
//
// 语义：规则内容变化 ⇒ 之前 firing 的判定依据已不存在，事件行置 resolved
// （resolved_at=now，value 保留最后触发值），并清掉内存 firing/lastNotifyAt。
// **不发送"已恢复"通知**：条件并未真的恢复，发"已恢复"是误导性文案；若条件仍满足，
// 下一次求值会重新 firing 并正常通知（F3 已接受的代价）。
//
// 注：AlertEvent 模型没有 detail 列（state/value/fired_at/resolved_at/notified_at），
// 因此"detail=rule_updated"只能落日志，无法写进事件行（模型不在本任务范围）。
func (e *Evaluator) resolveUpdatedFiring(rule models.AlertRule) {
	res := e.db.Model(&models.AlertEvent{}).
		Where("rule_id = ? AND state = ?", rule.ID, stateFiring).
		Updates(map[string]interface{}{"state": stateResolved, "resolved_at": time.Now()})
	if res.Error != nil {
		logger.Warn("alert: failed to resolve stale firing event after rule update",
			"rule_id", rule.ID, "error", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		logger.Info("alert: firing event closed because rule content changed",
			"rule_id", rule.ID, "rule_name", rule.Name, "detail", "rule_updated")
	}
}

// Invalidate CRUD 写路径直接失效重载缓存 (v0.3 修正: 消除"30s 刷新 vs 即时生效"矛盾)。
func (e *Evaluator) Invalidate() {
	e.LoadRules()
}

// SetPlanProvider 注入数据字段解析层控制面（设计 §7.2 / §9.2#3）。
// nil = 未接线：字面量继续走历史 matchField（G-6），特殊形态 fail-closed（只记日志+指标）。
func (e *Evaluator) SetPlanProvider(p PlanProvider) {
	e.mu.Lock()
	e.planProvider = p
	e.mu.Unlock()
	e.plans.reset()
}

func (e *Evaluator) planProviderSnapshot() PlanProvider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planProvider
}

// Start 启动兜底刷新 goroutine (30s 周期, 仅作保险)。
func (e *Evaluator) Start() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.LoadRules()
			}
		}
	}()
}

// Stop 停止兜底刷新。
func (e *Evaluator) Stop() {
	e.once.Do(func() { close(e.stopCh) })
}

// Evaluate 解析后回调入口: 对 edgeDeviceID 的物理量 fields 逐规则求值。
// at 为采样时间 (SensorParserConsumer records 构建处的 now)。
func (e *Evaluator) Evaluate(edgeDeviceID uint, fields []parser.Field, at time.Time) {
	if len(fields) == 0 || edgeDeviceID == 0 {
		return
	}
	// 热路径零拷贝：规则快照由 LoadRules/Invalidate 一次性构建并以 atomic.Pointer
	// 发布；快照不可变 ⇒ 无需 RLock、无需每帧重建（硬性能门禁 #10a-2）。
	// resolveLogicalID 的 SQL 往返仍在锁外（原 H2 修复语义不变）。
	snapPtr := e.rulesSnap.Load()
	if snapPtr == nil {
		return
	}
	snapshot := *snapPtr

	target := edgeDeviceID
	var logicalID uint
	logicalResolved := false
	for i := range snapshot {
		r := snapshot[i]
		// 来源组规则：匹配/过滤由 sourceref 快照决定（按 kind 分流，裁决 1）。
		if ref := r.SourceRef(); ref.IsGroup() {
			m, ok := e.matchSourceRule(r, ref, target, at)
			if !ok {
				continue
			}
			if m.Kind == sourceref.KindComposite {
				okc, satisfied, value, src := e.evalComposite(r, m, at)
				if !okc {
					continue
				}
				e.applyTransition(r, at, value, satisfied, identityForFrame(m, target), src, e.groupGap(m.Group))
				continue
			}
			e.evalRuleWithSource(r, target, fields, at, identityForFrame(m, target),
				&alertSourceEvidence{kind: sourceref.GroupKindSourceGroup, groupID: m.GroupID,
					edgeDeviceID: target})
			continue
		}
		// 存量单设备/逻辑设备匹配（逐字节保持原逻辑；logical 解析移到锁外且惰性）。
		switch r.TargetType {
		case models.AlertTargetEdgeDevice:
			if r.TargetID != target {
				continue
			}
		case models.AlertTargetLogicalDevice:
			if !logicalResolved {
				logicalID = e.resolveLogicalID(target)
				logicalResolved = true
			}
			if logicalID == 0 || r.TargetID != logicalID {
				continue
			}
		default:
			continue
		}
		e.evalRule(r, target, fields, at)
	}
}

// resolveLogicalID 解析边缘设备的最终逻辑身份 (合并链跟随, 与写入侧同链)。
func (e *Evaluator) resolveLogicalID(edgeDeviceID uint) uint {
	var dev models.EdgeDevice
	if err := e.db.Select("id", "logical_device_id").First(&dev, edgeDeviceID).Error; err != nil {
		return 0
	}
	if dev.LogicalDeviceID == nil || *dev.LogicalDeviceID == 0 {
		return 0
	}
	target, err := datalifecycle.ResolveMergeTarget(e.db, *dev.LogicalDeviceID)
	if err != nil {
		logger.Warn("alert: resolve merge target failed", "edge_device_id", edgeDeviceID, "error", err)
		return 0
	}
	return target
}

// evalRule 单规则状态机: normal → firing (连续满足 DurationSec) → resolved (任一不满足)。
//
// deviceID 是本次上报的**边缘设备 ID**（Evaluate 入参）：解析层 plan 按
// (device, selector) 编译，不能用 rule.TargetID（可能是逻辑设备 ID，会恒 unbound）。
func (e *Evaluator) evalRule(rule models.AlertRule, deviceID uint, fields []parser.Field, at time.Time) {
	// literal 路径内部与历史 matchField 逐字节等价；特殊 selector 走 EvalPredicate。
	ok, satisfied, value := e.evalSelector(rule, deviceID, fields, at)
	if !ok {
		return // 本批无该字段 / 特殊 selector unbound / kill switch，不影响窗口
	}
	e.applyTransition(rule, at, value, satisfied, legacyIdentity(deviceID), nil, e.sourceGap(deviceID))
}

// evalRuleWithSource 是 primary_backup 规则的单帧路径（active 成员帧）：
// window identity 带组信息，其余状态机与存量完全一致。
func (e *Evaluator) evalRuleWithSource(rule models.AlertRule, deviceID uint, fields []parser.Field,
	at time.Time, ident sourceIdentity, src *alertSourceEvidence) {
	ok, satisfied, value := e.evalSelector(rule, deviceID, fields, at)
	if !ok {
		return
	}
	e.applyTransition(rule, at, value, satisfied, ident, src, e.sourceGap(deviceID))
}

// applyTransition 是 normal → firing → resolved 状态机的唯一实现（三路复用：存量帧、
// 主备组成员帧、复合整组结果）。
//
// S1：写样本前 identity 与窗口最后一条不一致 ⇒ 丢弃旧段（不依赖切换钩子，S3）。
// S8：pruneWindow 增加跨长断流判据（maxGap=2×采集间隔；0=不启用）。
func (e *Evaluator) applyTransition(rule models.AlertRule, at time.Time, value float64,
	satisfied bool, ident sourceIdentity, src *alertSourceEvidence, maxGap time.Duration) {
	e.mu.Lock()
	win := e.windows[rule.ID]
	if len(win) > 0 && !win[len(win)-1].identity.equal(ident) {
		win = nil
	}
	win = append(win, sample{at: at, value: value, satisfied: satisfied, identity: ident})
	win = pruneWindow(win, rule.DurationSec, at, maxGap)
	e.windows[rule.ID] = win

	isFiring := e.firing[rule.ID]
	// 连续满足判定: DurationSec=0 直通 (本点满足即触发);
	// 否则要求窗口内所有样本都满足且窗口跨度 ≥ DurationSec。
	continuous := satisfied && windowSatisfied(win, rule.DurationSec, at)

	switch {
	case continuous && !isFiring:
		e.firing[rule.ID] = true
		e.lastNotifyAt[rule.ID] = at
		e.mu.Unlock()
		e.markRuleFired(rule.ID)
		e.onFire(rule, value, at, src)
	case !satisfied && isFiring:
		delete(e.firing, rule.ID)
		e.mu.Unlock()
		e.onResolve(rule, value, at)
	case isFiring:
		// firing 未 resolved: 不重复发事件; SilenceSec 内不重复通知但更新 AlertEvent。
		last := e.lastNotifyAt[rule.ID]
		e.mu.Unlock()
		if at.Sub(last) >= time.Duration(rule.SilenceSec)*time.Second {
			e.mu.Lock()
			e.lastNotifyAt[rule.ID] = at
			e.mu.Unlock()
			e.renotify(rule, value, at)
		}
	default:
		e.mu.Unlock()
	}
}

// onFire normal→firing: 创建 AlertEvent(firing) + Notification + WS 广播。
func (e *Evaluator) onFire(rule models.AlertRule, value float64, at time.Time, src *alertSourceEvidence) {
	ev := models.AlertEvent{
		RuleID:     rule.ID,
		State:      stateFiring,
		Value:      value,
		FiredAt:    &at,
		NotifiedAt: &at,
	}
	// 来源归因（方案 §4.4）：告警事件必须能回答"谁供的数"。
	src.apply(&ev)
	if err := e.db.Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("alert_evaluator", "alert_events").Inc()
		logger.Warn("alert: failed to create firing event", "rule_id", rule.ID, "error", err)
		return
	}
	e.notify(rule, ev, value, at)
}

// renotify firing 持续期静默窗口到期后的重复通知: 更新最近一条 firing 事件的
// Value/NotifiedAt, 不新建事件、不改 State (抑制语义, §5.1.2)。
func (e *Evaluator) renotify(rule models.AlertRule, value float64, at time.Time) {
	var ev models.AlertEvent
	if err := e.db.Where("rule_id = ? AND state = ?", rule.ID, stateFiring).
		Order("id DESC").First(&ev).Error; err != nil {
		return
	}
	if err := e.db.Model(&models.AlertEvent{}).Where("id = ?", ev.ID).
		Updates(map[string]interface{}{"value": value, "notified_at": at}).Error; err != nil {
		logger.Warn("alert: failed to update firing event", "rule_id", rule.ID, "error", err)
		return
	}
	ev.Value = value
	ev.NotifiedAt = &at
	e.notify(rule, ev, value, at)
}

// onResolve firing→resolved: 任一不满足值出现。单行事件模型 (§5.1.1
// "AlertEvent 告警事件(含恢复)": ResolvedAt/Value 同行) — 就地关闭最近一条
// firing 事件 (state→resolved, 记录恢复时值); 无在案 firing 事件 (如重启丢失)
// 时补记一条完整 resolved 事件。
func (e *Evaluator) onResolve(rule models.AlertRule, value float64, at time.Time) {
	var ev models.AlertEvent
	if err := e.db.Where("rule_id = ? AND state = ?", rule.ID, stateFiring).
		Order("id DESC").First(&ev).Error; err != nil {
		ev = models.AlertEvent{
			RuleID:     rule.ID,
			State:      stateResolved,
			Value:      value,
			FiredAt:    &at,
			ResolvedAt: &at,
		}
		if err := e.db.Create(&ev).Error; err != nil {
			metrics.DataConsumerDBWriteFailures.WithLabelValues("alert_evaluator", "alert_events").Inc()
			logger.Warn("alert: failed to backfill resolved event", "rule_id", rule.ID, "error", err)
			return
		}
	} else {
		if err := e.db.Model(&models.AlertEvent{}).Where("id = ?", ev.ID).
			Updates(map[string]interface{}{
				"state":       stateResolved,
				"value":       value,
				"resolved_at": at,
			}).Error; err != nil {
			logger.Warn("alert: failed to resolve event", "rule_id", rule.ID, "error", err)
			return
		}
		ev.State = stateResolved
		ev.Value = value
		ev.ResolvedAt = &at
	}
	e.notify(rule, ev, value, at)
}

// createNotification 落库一条通知 (D-1 步骤 3: 经注入的 Notifier 或直接落库)。
//
// nil 回落是**显式设计**而非"碰巧没 nil":
//   - 注入时: 交给 notify.Dispatcher —— 它负责写 notifications 行 + 按通道配置外发投递
//     (设计 §2.1 取向 A 的单一入口); Dispatcher 本身 fail-open, 不返回错误。
//   - 未注入时 (既有测试直接 NewEvaluator(db, ...) / 任何未接线的构造): 回落为
//     改造前逐字节相同的直接 Create + 同一条指标与日志。既有行为与断言不受影响。
//
// 返回 true 表示通知已落库 (自增 ID 可用, 广播前提)。广播契约 (负债 D-3) 要求
// 拿到 ID 才广播, 因此两条路径都必须**同步**完成落库后才返回 —— 这也是写入点
// 一律用 Dispatcher.Create 而非 CreateAsync 的原因 (见 notify/dispatcher.go)。
func (e *Evaluator) createNotification(ctx context.Context, n *models.Notification, ruleID uint) bool {
	if e.notifier != nil {
		e.notifier.Create(ctx, n)
		// Dispatcher 不返回错误 (fail-open); 落库失败时 n.ID 保持 0, 由下面的
		// 广播前提 (ID != 0) 统一兜住, 无需在此重复判定。
		return n.ID != 0
	}
	if err := e.db.Create(n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("alert_evaluator", "notifications").Inc()
		logger.Warn("alert: failed to create notification", "rule_id", ruleID, "error", err)
		return false
	}
	return true
}

// notify 写 Notification 行 + WS BroadcastEvent (events.Notification 类型)。
//
// 载荷契约 (负债 D-3): 广播的必须是**通知实体本身** —— 与前端
// api/notification.ts 的 Notification 接口、GET /notifications 列表项同形
// (id/type/title/description/message/source/source_id/read/created_at),
// 前端才能"收到即插入列表头部"。触发它的告警领域详情 (rule_id/sensor_name/
// threshold/comparator...) 收进嵌套 detail 字段, 不与通知字段平铺混淆。
//
// 铁律: 只有 Create 成功 (拿到自增 ID) 才广播 —— 否则前端会插入一条库里
// 不存在的通知, 且该条永远无法标记已读 (PUT /notifications/:id/read 无行可改)。
func (e *Evaluator) notify(rule models.AlertRule, ev models.AlertEvent, value float64, at time.Time) {
	desc := sensorDesc(rule, value, ev.State)
	n := models.Notification{
		Type:        models.NotificationType(rule.Level),
		Message:     desc,
		Title:       "告警: " + rule.Name,
		Description: desc,
		Source:      "alert_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if !e.createNotification(context.Background(), &n, rule.ID) {
		return
	}
	if e.broadcast != nil {
		e.broadcast(events.Notification, gin.H{
			// ── 通知实体 (与列表项同形) ──
			"id":          n.ID,
			"type":        n.Type,
			"title":       n.Title,
			"description": n.Description,
			"message":     n.Message,
			"source":      n.Source,
			"source_id":   n.SourceID,
			"read":        n.Read,
			"created_at":  n.CreatedAt,
			// ── 告警领域详情 (嵌套, 不平铺) ──
			"detail": gin.H{
				"rule_id":     rule.ID,
				"rule_name":   rule.Name,
				"event_id":    ev.ID,
				"state":       ev.State,
				"value":       value,
				"level":       rule.Level,
				"sensor_name": rule.SensorName,
				"threshold":   rule.Threshold,
				"comparator":  rule.Comparator,
				"fired_at":    ev.FiredAt,
				"resolved_at": ev.ResolvedAt,
			},
		})
	}
}

func sensorDesc(rule models.AlertRule, value float64, state string) string {
	action := "超过阈值"
	if state == stateResolved {
		action = "已恢复"
	}
	return fmt.Sprintf("%s %s %s %.2f (当前 %.2f)", rule.Name, rule.SensorName, action, rule.Threshold, value)
}

// matchField 在 fields 中找 SensorName 同名字段。
func matchField(fields []parser.Field, name string) (float64, bool) {
	for i := range fields {
		if fields[i].Name == name {
			return fields[i].Value, true
		}
	}
	return 0, false
}

// compare 求值比较符。
func compare(op string, value, threshold float64) bool {
	switch op {
	case models.AlertComparatorGT:
		return value > threshold
	case models.AlertComparatorGTE:
		return value >= threshold
	case models.AlertComparatorLT:
		return value < threshold
	case models.AlertComparatorLTE:
		return value <= threshold
	case models.AlertComparatorEQ:
		return value == threshold
	case models.AlertComparatorNEQ:
		return value != threshold
	default:
		return false
	}
}

// pruneWindow 仅保留 [now-DurationSec, now] 内的样本并限制上限。
func pruneWindow(win []sample, durationSec int, now time.Time, maxGap time.Duration) []sample {
	if len(win) == 0 {
		return win
	}
	cutoff := now.Add(-time.Duration(durationSec) * time.Second)
	i := 0
	for i < len(win) && win[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		win = append(win[:0], win[i:]...)
	}
	// S8（方案 F-7 写死算法）：跨长断流不得拼接 —— 相邻样本间隔 > maxGap
	// （2×采集间隔）时，更早样本不能作为窗口起点。maxGap<=0 ⇒ 不启用。
	if maxGap > 0 {
		start := 0
		for i := len(win) - 1; i > 0; i-- {
			if win[i].at.Sub(win[i-1].at) > maxGap {
				start = i
				break
			}
		}
		win = win[start:]
	}
	// ring buffer 上限保护: 超出丢最旧。
	if len(win) > maxWindowSamples {
		win = win[len(win)-maxWindowSamples:]
	}
	return win
}

// windowSatisfied 判定连续满足: DurationSec=0 直通 (当前点满足即可);
// 否则窗口非空、全部样本满足、且最早样本距今 ≥ DurationSec。
func windowSatisfied(win []sample, durationSec int, now time.Time) bool {
	if durationSec <= 0 {
		return len(win) > 0
	}
	if len(win) == 0 {
		return false
	}
	for i := range win {
		if !win[i].satisfied {
			return false
		}
	}
	return now.Sub(win[0].at) >= time.Duration(durationSec)*time.Second
}
