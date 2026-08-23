// Package automation 实现自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)。
//
// 挂接点裁决 (同 alert §5.1.2): 复用 SensorParserConsumer 解析后回调,
// 与 alert.Evaluator 并列不合并 — 告警=人感知只通知 (firing/resolved),
// 策略=系统处置 (armed→triggered→cooldown 三态)。求值器只判定"是否触发",
// 触发后的冷却/熔断/确认分流/动作执行全部委托 Planner, 保持单向数据流。
package automation

import (
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
	// rules 规则缓存 (仅 enabled 且 trigger_type=sensor_threshold), key: rule ID。
	rules map[uint]models.AutomationRule
	// windows 每规则滑动窗口, key: rule ID。
	windows map[uint][]sample
	// triggered 规则当前是否处于 triggered 态 (冷却中)。
	triggered map[uint]time.Time // rule ID → 触发时刻

	stopCh chan struct{}
	once   sync.Once
}

// NewEvaluator 构造求值器并全量加载 enabled 规则缓存。
func NewEvaluator(db *gorm.DB, handler TriggerHandler) *Evaluator {
	e := &Evaluator{
		db:        db,
		handler:   handler,
		rules:     make(map[uint]models.AutomationRule),
		windows:   make(map[uint][]sample),
		triggered: make(map[uint]time.Time),
		stopCh:    make(chan struct{}),
	}
	e.LoadRules()
	return e
}

// LoadRules 全量加载 enabled 的 sensor_threshold 规则 (CRUD 写路径经 Invalidate
// 即时调用; Start 兜底刷新仅作保险)。
func (e *Evaluator) LoadRules() {
	var rules []models.AutomationRule
	if err := e.db.Where("enabled = ? AND trigger_type = ?", true, models.AutomationTriggerSensorThreshold).
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

// Stop 停止兜底刷新。
func (e *Evaluator) Stop() {
	e.once.Do(func() { close(e.stopCh) })
}

// Evaluate 解析后回调入口: 对 edgeDeviceID 的物理量 fields 逐规则求值。
// 挂接点 = databus consumers_heavy.go 新增 SetAutomationSink, 与 alertSink 并列。
func (e *Evaluator) Evaluate(edgeDeviceID uint, fields []parser.Field, at time.Time) {
	if len(fields) == 0 || edgeDeviceID == 0 {
		return
	}
	e.mu.RLock()
	target := edgeDeviceID
	var logicalID uint
	rules := make([]models.AutomationRule, 0, len(e.rules))
	for _, r := range e.rules {
		// 规则匹配: TriggerEdgeDeviceID=0 表示任意设备上报该字段即触发 (不推荐,
		// 文档标注); 否则必须精确命中上报设备 (或其逻辑身份)。
		if r.TriggerEdgeDeviceID != 0 && r.TriggerEdgeDeviceID != target {
			if logicalID == 0 {
				logicalID = e.resolveLogicalID(target)
			}
			if logicalID == 0 || r.TriggerEdgeDeviceID != logicalID {
				continue
			}
		}
		rules = append(rules, r)
	}
	e.mu.RUnlock()

	for i := range rules {
		e.evalRule(rules[i], fields, at)
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
