// Package automation 实现自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)。
//
// 挂接点裁决 (同 alert §5.1.2): 复用 SensorParserConsumer 解析后回调,
// 与 alert.Evaluator 并列不合并 — 告警=人感知只通知 (firing/resolved),
// 策略=系统处置 (armed→triggered→cooldown 三态)。求值器只判定"是否触发",
// 触发后的冷却/熔断/确认分流/动作执行全部委托 Planner, 保持单向数据流。
package automation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

const (
	maxWindowSamples  = 1000
	defaultCooldown   = 300
)

// sample 滑动窗口单点 (同 alert 先例)。
type sample struct {
	at        time.Time
	satisfied bool
}

// TriggerEvent 触发事件: 求值器产出, Planner 消费。
type TriggerEvent struct {
	Rule         models.AutomationRule
	Value        float64 // 触发时值 (sensor_threshold)
	At           time.Time
	WindowEdge   string  // time_window: enter|exit (sensor_threshold 为 "")
}

// TriggerHandler Planner 消费接口 (避免 automation→service 编译期反向依赖)。
type TriggerHandler interface {
	HandleTrigger(ev TriggerEvent)
}

// Evaluator 策略求值器: 规则缓存 + 每规则滑动窗口 + armed/triggered 状态机。
//
// 状态机 (裁决 5, 与告警不同):
//   armed → triggered: 连续满足 DurationSec (DurationSec=0 直通)
//   triggered → armed: CooldownSec 到期 (时间维度, 不是条件复位!)
//
// ⚠️ armed 恢复条件是冷却到期, 不是条件复位 — "光照弱"持续整夜, 触发一次
// 开灯后进入冷却, 冷却到期后若条件仍满足可再次触发; 若等条件复位(天亮),
// 则次日黄昏无法再触发。这是与告警 firing→resolved 最本质的区别。
type Evaluator struct {
	db      *gorm.DB
	handler TriggerHandler

	mu sync.RWMutex
	// rules 规则缓存 (仅 enabled 且 trigger_type IN (sensor_threshold, time_window)), key: rule ID。
	rules map[uint]models.AutomationRule
	// windows 每规则滑动窗口, key: rule ID (sensor_threshold 专用)。
	windows map[uint][]sample
	// triggered 规则当前是否处于 triggered 态 (冷却中)。
	triggered map[uint]time.Time // rule ID → 触发时刻
	// windowStates time_window 规则上次 tick 是否在窗口内 (rule ID → wasInside)。
	// 重启后清空 — 首次 tick 以当前是否在窗口内为基准 (保守: enter 规则首次 tick
	// 若在窗口内则触发, exit 规则首次 tick 若在窗口内则不触发)。
	windowStates map[uint]bool

	stopCh chan struct{}
	once   sync.Once
}

// NewEvaluator 构造求值器并全量加载 enabled 规则缓存。
func NewEvaluator(db *gorm.DB, handler TriggerHandler) *Evaluator {
	e := &Evaluator{
		db:           db,
		handler:      handler,
		rules:        make(map[uint]models.AutomationRule),
		windows:      make(map[uint][]sample),
		triggered:    make(map[uint]time.Time),
		windowStates: make(map[uint]bool),
		stopCh:       make(chan struct{}),
	}
	e.LoadRules()
	e.rebuildCooldowns()
	return e
}

// rebuildCooldowns 启动重建冷却状态 (H1 修复, 方案 §9 R2): triggered map 纯内存,
// 重启清空后冷却期规则被误判 armed, 条件仍满足时立即重触发。启动时对每条 enabled
// 规则查最近一条 result=executed 的触发时刻回填 triggered[ruleID]。
func (e *Evaluator) rebuildCooldowns() {
	rows := []struct {
		RuleID uint
		MaxAt  *string // SQLite MAX(timestamp) 返回字符串, 需手动解析
	}{}
	if err := e.db.Model(&models.AutomationEvent{}).
		Select("rule_id, MAX(triggered_at) AS max_at").
		Where("result = ?", models.AutomationResultExecuted).
		Group("rule_id").Scan(&rows).Error; err != nil {
		logger.Warn("automation: failed to rebuild cooldowns", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	e.mu.Lock()
	for _, r := range rows {
		if r.MaxAt == nil {
			continue
		}
		at, err := parseDBTime(*r.MaxAt)
		if err != nil {
			logger.Warn("automation: rebuild cooldowns parse time failed",
				"rule_id", r.RuleID, "raw", *r.MaxAt, "error", err)
			continue
		}
		e.triggered[r.RuleID] = at
	}
	e.mu.Unlock()
}

// parseDBTime 解析 SQLite 返回的时间字符串 (gorm sqlite driver 写 time.Time
// 的默认格式为 RFC3339Nano, 但 MAX() 聚合可能返回空格分隔格式, 逐一尝试)。
func parseDBTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var err error
	var t time.Time
	for _, l := range layouts {
		if t, err = time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

// LoadRules 全量加载 enabled 的 sensor_threshold + time_window 规则 (CRUD 写路径经 Invalidate
// 即时调用; Start 兜底刷新仅作保险)。time_window 规则不挂传感器回调, 由独立 ticker 求值。
func (e *Evaluator) LoadRules() {
	var rules []models.AutomationRule
	if err := e.db.Where("enabled = ? AND trigger_type IN ?",
		true, []string{models.AutomationTriggerSensorThreshold, models.AutomationTriggerTimeWindow}).
		Find(&rules).Error; err != nil {
		logger.Warn("automation: failed to load rules", "error", err)
		return
	}
	next := make(map[uint]models.AutomationRule, len(rules))
	for _, r := range rules {
		next[r.ID] = r
	}
	e.mu.Lock()
	e.rules = next
	for id := range e.windows {
		if _, ok := next[id]; !ok {
			delete(e.windows, id)
			delete(e.triggered, id)
		}
	}
	// time_window 规则清理: 从缓存移除的规则同时清理窗口状态。
	for id := range e.windowStates {
		if _, ok := next[id]; !ok {
			delete(e.windowStates, id)
		}
	}
	e.mu.Unlock()
}

// Invalidate CRUD 写路径直接失效重载缓存。
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

// Stop 停止兜底刷新 + time_window ticker。
func (e *Evaluator) Stop() {
	e.once.Do(func() { close(e.stopCh) })
}

// StartWindowTicker 启动 time_window 触发器独立求值 ticker (1min 周期)。
// 与 sensor_threshold 的 Evaluate 路径完全独立 — 时钟驱动不挂传感器解析回调。
// ctx 取消时优雅退出 (main.go 接线用)。
func (e *Evaluator) StartWindowTicker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.evalTimeWindows(time.Now())
			}
		}
	}()
}

// evalTimeWindows 对所有 time_window 规则执行一次 tick 求值。
// 由 StartWindowTicker 每 1min 调用; 时间源一律用后端本地时间 (time.Now())。
func (e *Evaluator) evalTimeWindows(now time.Time) {
	e.mu.RLock()
	var windowRules []models.AutomationRule
	for _, r := range e.rules {
		if r.TriggerType == models.AutomationTriggerTimeWindow {
			windowRules = append(windowRules, r)
		}
	}
	e.mu.RUnlock()

	for _, rule := range windowRules {
		e.evalWindowRule(rule, now)
	}
}

// evalWindowRule 单条 time_window 规则状态机: 判定当前时刻是否在窗口内,
// 根据 edge 语义 (enter/exit/inside) 决定是否触发, 受 CooldownSec 抑制。
func (e *Evaluator) evalWindowRule(rule models.AutomationRule, now time.Time) {
	inside, err := isInWindow(rule.TriggerWindowStart, rule.TriggerWindowEnd, now)
	if err != nil {
		logger.Warn("automation: invalid time window format",
			"rule_id", rule.ID, "start", rule.TriggerWindowStart, "end", rule.TriggerWindowEnd, "error", err)
		return
	}

	e.mu.Lock()
	wasInside, exists := e.windowStates[rule.ID]
	// 更新窗口状态 (无论是否触发都要记录本次状态)。
	e.windowStates[rule.ID] = inside

	// 冷却判定 (与 sensor_threshold 共用 triggered map)。
	cooldown := time.Duration(rule.CooldownSec) * time.Second
	if rule.CooldownSec <= 0 {
		cooldown = defaultCooldown * time.Second
	}
	if firedAt, isTriggered := e.triggered[rule.ID]; isTriggered {
		if now.Sub(firedAt) < cooldown {
			e.mu.Unlock()
			return // 冷却期内, 不重复触发
		}
		delete(e.triggered, rule.ID) // 冷却到期, 回 armed
	}
	e.mu.Unlock()

	// 边沿触发判定。
	var shouldTrigger bool
	var edge string
	switch rule.TriggerWindowEdge {
	case models.AutomationWindowEnter:
		// enter: 上次不在窗口内、本次在窗口内 → 触发。
		// 重启后首次 tick (exists=false): 保守处理 — 当前在窗口内则触发。
		shouldTrigger = inside && (!exists || !wasInside)
		edge = models.AutomationWindowEnter
	case models.AutomationWindowExit:
		// exit: 上次在窗口内、本次不在窗口内 → 触发。
		// 重启后首次 tick (exists=false): 当前不在窗口内则触发 (保守: 无法判断是否在窗口内, 不触发)。
		shouldTrigger = !inside && exists && wasInside
		edge = models.AutomationWindowExit
	case models.AutomationWindowInside:
		// inside: 窗口内每次 tick 都参与求值 (受 CooldownSec 抑制)。
		shouldTrigger = inside
		edge = models.AutomationWindowInside
	default:
		return
	}

	if !shouldTrigger {
		return
	}

	// armed → triggered: 记录触发时刻, 提交 Planner。
	e.mu.Lock()
	e.triggered[rule.ID] = now
	e.mu.Unlock()

	if e.handler != nil {
		e.handler.HandleTrigger(TriggerEvent{
			Rule:       rule,
			At:         now,
			WindowEdge: edge,
		})
	}
}

// isInWindow 判定当前时刻是否在时间窗口内。
// start/end 为 "HH:MM" 格式; 窗口可跨零点 (start > end 时跨日)。
// 返回 (是否在窗口内, 解析错误)。
func isInWindow(start, end string, now time.Time) (bool, error) {
	startT, err := parseHHMM(start)
	if err != nil {
		return false, fmt.Errorf("parse start %q: %w", start, err)
	}
	endT, err := parseHHMM(end)
	if err != nil {
		return false, fmt.Errorf("parse end %q: %w", end, err)
	}

	// 当日窗口时间点 (本地时间)。
	year, month, day := now.Date()
	startToday := time.Date(year, month, day, startT.Hour(), startT.Minute(), 0, 0, now.Location())
	endToday := time.Date(year, month, day, endT.Hour(), endT.Minute(), 0, 0, now.Location())

	if startToday.Equal(endToday) {
		// start == end: 视为全天窗口 (24h), 始终在内。
		return true, nil
	}
	if startToday.Before(endToday) {
		// 非跨零点窗口: [start, end)。
		return !now.Before(startToday) && now.Before(endToday), nil
	}
	// 跨零点窗口: [start, 24:00) ∪ [00:00, end)。
	// 等价于: now >= start || now < end。
	return !now.Before(startToday) || now.Before(endToday), nil
}

// parseHHMM 解析 "HH:MM" 格式为 time.Time (仅取时分)。
func parseHHMM(s string) (time.Time, error) {
	return time.Parse("15:04", s)
}

// Evaluate 解析后回调入口: 对 edgeDeviceID 的物理量 fields 逐规则求值。
// 挂接点 = databus consumers_heavy.go 新增 SetAutomationSink, 与 alertSink 并列。
func (e *Evaluator) Evaluate(edgeDeviceID uint, fields []parser.Field, at time.Time) {
	if len(fields) == 0 || edgeDeviceID == 0 {
		return
	}
	// H2 修复 (锁放大): RLock 内只快照 rules 浅拷贝, resolveLogicalID 的 SQL 往返
	// 移到 RUnlock 之后 — 写锁等待会阻塞所有 RLock, DB 慢查询时求值 stall。
	e.mu.RLock()
	rules := make([]models.AutomationRule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, r)
	}
	e.mu.RUnlock()

	target := edgeDeviceID
	var logicalID uint
	resolved := false
	matched := rules[:0]
	for _, r := range rules {
		// 规则匹配: TriggerEdgeDeviceID=0 表示任意设备上报该字段即触发 (不推荐,
		// 文档标注); 否则必须精确命中上报设备 (或其逻辑身份)。
		if r.TriggerEdgeDeviceID != 0 && r.TriggerEdgeDeviceID != target {
			if !resolved {
				logicalID = e.resolveLogicalID(target)
				resolved = true
			}
			if logicalID == 0 || r.TriggerEdgeDeviceID != logicalID {
				continue
			}
		}
		matched = append(matched, r)
	}

	for i := range matched {
		e.evalRule(matched[i], fields, at)
	}
}

// resolveLogicalID 解析边缘设备的最终逻辑身份 (合并链跟随, 与 alert 同链)。
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
		logger.Warn("automation: resolve merge target failed", "edge_device_id", edgeDeviceID, "error", err)
		return 0
	}
	return target
}

// evalRule 单规则状态机: armed → triggered (连续满足 DurationSec) → 冷却到期回 armed。
func (e *Evaluator) evalRule(rule models.AutomationRule, fields []parser.Field, at time.Time) {
	value, ok := matchField(fields, rule.TriggerSensorName)
	if !ok {
		return // 本批无该传感器字段, 不影响窗口
	}

	// 附加条件全部 AND 求值 (fail-closed: 非法 JSON 直接判不触发)。
	conds, err := rule.ParseConditions()
	if err != nil {
		logger.Warn("automation: invalid conditions_json", "rule_id", rule.ID, "error", err)
		return
	}
	for _, c := range conds {
		cv, ok := matchField(fields, c.SensorName)
		if !ok || !compare(c.Comparator, cv, c.Threshold) {
			return // 任一条件不满足 (含字段缺失) 则不触发
		}
	}

	satisfied := compare(rule.TriggerComparator, value, rule.TriggerThreshold)
	cooldown := time.Duration(rule.CooldownSec) * time.Second
	if rule.CooldownSec <= 0 {
		cooldown = defaultCooldown * time.Second
	}

	e.mu.Lock()
	// 冷却到期判定优先于求值: 到期即回 armed, 本批可参与新一轮触发。
	if firedAt, isTriggered := e.triggered[rule.ID]; isTriggered {
		if at.Sub(firedAt) < cooldown {
			e.mu.Unlock()
			e.recordSuppressed(rule, firedAt, at, value) // F3: 冷却命中落审计 (防抖可观测, 同窗节流)
			return // 冷却期内, 不更新窗口不重复触发
		}
		delete(e.triggered, rule.ID) // 冷却到期, 回 armed
	}

	win := append(e.windows[rule.ID], sample{at: at, satisfied: satisfied})
	win = pruneWindow(win, rule.TriggerDurationSec, at)
	e.windows[rule.ID] = win

	continuous := satisfied && windowSatisfied(win, rule.TriggerDurationSec, at)
	if !continuous {
		e.mu.Unlock()
		return
	}

	// armed → triggered: 记录触发时刻, 本批提交 Planner。
	e.triggered[rule.ID] = at
	e.mu.Unlock()

	if e.handler != nil {
		e.handler.HandleTrigger(TriggerEvent{Rule: rule, Value: value, At: at})
	}
}

// recordSuppressed F3 修复 (方案 §3.4①): 冷却命中时落一条 result=suppressed_cooldown
// 审计事件 — 此前该常量全仓 0 写入点, 触发历史看不到被冷却压制的触发 (防抖不可观测)。
// 字段写法对齐 planner.recordRet (fail-open: 写失败只告警不阻塞求值)。
//
// 同窗节流 (主 Agent 复审补): Evaluate 每帧上报都触发 (databus consumers_heavy.go:337),
// 冷却期内高频越阈会每帧落一行刷量 (1s 上报 × 1h 冷却 = 3600 行)。故同一冷却窗
// (自窗起点 windowStart 起) 只落首条 suppressed_cooldown, 后续命中查询到已有即跳过。
func (e *Evaluator) recordSuppressed(rule models.AutomationRule, windowStart, at time.Time, value float64) {
	// 同一冷却窗内已有 suppressed_cooldown 行则跳过 (节流, 防高频上报刷量)。
	var cnt int64
	if err := e.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ? AND triggered_at >= ?",
			rule.ID, models.AutomationResultSuppressedCooldown, windowStart).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      models.AutomationResultSuppressedCooldown,
		Detail:      "cooldown active",
		CreatedAt:   at,
	}
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if err := e.db.Create(&ev).Error; err != nil {
		logger.Warn("automation: failed to record suppressed_cooldown event",
			"rule_id", rule.ID, "error", err)
	}
}

// matchField / compare / pruneWindow / windowSatisfied 与 alert 同源同语义
// (避免 alert→automation 或反向的编译期依赖, 各自持有私有副本)。

func matchField(fields []parser.Field, name string) (float64, bool) {
	for i := range fields {
		if fields[i].Name == name {
			return fields[i].Value, true
		}
	}
	return 0, false
}

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
// 滚动窗口语义 (2026-08-23 探针实锤修复): 保留 cutoff 前最后一个样本作为窗口
// 起点 —— 否则稀疏采样 (如每 5s 一点, duration=30s) 时窗口恰跨满 duration 的
// 边界样本被裁, windowSatisfied 的 span 判定 (now-win[0].at >= duration) 永不满足。
// 对照 alert pruneWindow 同位置缺陷 (alert 测试用密集采样未暴露)。
func pruneWindow(win []sample, durationSec int, now time.Time) []sample {
	if len(win) == 0 {
		return win
	}
	if durationSec <= 0 {
		// DurationSec=0 直通: 只保留最新样本 (窗口无意义)。
		return win[len(win)-1:]
	}
	cutoff := now.Add(-time.Duration(durationSec) * time.Second)
	keep := 0
	for keep < len(win) && win[keep].at.Before(cutoff) {
		keep++
	}
	// 保留 cutoff 前最后一个样本: 它是窗口的实际起点 (span 判定基准)。
	if keep > 0 {
		keep--
	}
	win = win[keep:]
	if len(win) > maxWindowSamples {
		win = win[len(win)-maxWindowSamples:]
	}
	return win
}

// windowSatisfied 连续满足判定: DurationSec=0 直通 (本点满足即触发);
// 否则窗口内所有样本都满足且窗口跨度 ≥ DurationSec。
func windowSatisfied(win []sample, durationSec int, now time.Time) bool {
	if durationSec <= 0 {
		return true
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
