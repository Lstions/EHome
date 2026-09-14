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
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
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
	// windows 每规则滑动窗口, key: rule ID。
	windows map[uint][]sample
	// firing 规则当前是否处于 firing 态 (内存态)。
	firing map[uint]bool
	// lastNotifyAt 每规则最近一次通知时间 (SilenceSec 抑制用)。
	lastNotifyAt map[uint]time.Time

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
		firing:       make(map[uint]bool),
		lastNotifyAt: make(map[uint]time.Time),
		stopCh:       make(chan struct{}),
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
		next[r.ID] = r
	}
	e.mu.Lock()
	e.rules = next
	// 清理已删除/禁用规则的窗口与状态
	for id := range e.windows {
		if _, ok := next[id]; !ok {
			delete(e.windows, id)
			delete(e.firing, id)
		}
	}
	e.mu.Unlock()
}

// Invalidate CRUD 写路径直接失效重载缓存 (v0.3 修正: 消除"30s 刷新 vs 即时生效"矛盾)。
func (e *Evaluator) Invalidate() {
	e.LoadRules()
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
	e.mu.RLock()
	target := edgeDeviceID
	var logicalID uint
	rules := make([]models.AlertRule, 0, len(e.rules))
	for _, r := range e.rules {
		switch r.TargetType {
		case models.AlertTargetEdgeDevice:
			if r.TargetID == target {
				rules = append(rules, r)
			}
		case models.AlertTargetLogicalDevice:
			if logicalID == 0 {
				logicalID = e.resolveLogicalID(target)
			}
			if logicalID > 0 && r.TargetID == logicalID {
				rules = append(rules, r)
			}
		}
	}
	e.mu.RUnlock()

	for i := range rules {
		e.evalRule(rules[i], fields, at)
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
func (e *Evaluator) evalRule(rule models.AlertRule, fields []parser.Field, at time.Time) {
	value, ok := matchField(fields, rule.SensorName)
	if !ok {
		return // 本批无该传感器字段, 不影响窗口
	}

	satisfied := compare(rule.Comparator, value, rule.Threshold)

	e.mu.Lock()
	win := append(e.windows[rule.ID], sample{at: at, value: value, satisfied: satisfied})
	win = pruneWindow(win, rule.DurationSec, at)
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
		e.onFire(rule, value, at)
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
func (e *Evaluator) onFire(rule models.AlertRule, value float64, at time.Time) {
	ev := models.AlertEvent{
		RuleID:     rule.ID,
		State:      stateFiring,
		Value:      value,
		FiredAt:    &at,
		NotifiedAt: &at,
	}
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
func pruneWindow(win []sample, durationSec int, now time.Time) []sample {
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
