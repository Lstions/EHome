package alert

// 数据字段解析层（sensorfield）在告警求值热路径上的接入。
//
// 为什么必须改这里（评审阻断 B4）：alert/evaluator.go 有独立 matchField，
// 只给 automation 加 selector 支持会让"选择器告警永不触发"——正是本方案要消灭的
// 静默失效。设计 §9.2#3 明确列入必改。
//
// 与 automation 的差异（Lead 2026-10-21 裁决，审计按引擎拆分）：
//   - alert_events.State 只有 firing/resolved，**不新增事件类型**；
//   - 解析失败**只记日志 + 指标**（RuleNeverResolved/RuleNeverFired，kind="alert"），
//     不写 DB 事件（否则会污染告警状态机）。
//
// 比较走 sensorfield.EvalPredicate（不得用 Plan.Eval：family any/all 需要比较符）。

import (
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"
)

// unresolvedLogInterval 是"解析失败"日志的节流周期（与 automation 审计同口径）。
const unresolvedLogInterval = 5 * time.Minute

// PlanProvider 是解析层控制面在 alert 包的最小依赖（生产实现：*sensorfield.Registry）。
type PlanProvider interface {
	Compile(deviceID uint, selector string) (sensorfield.Plan, error)
	Enabled() bool
	Epoch() uint64
}

// ruleKey 是编译缓存 key。alert 只有 trigger 一个 selector，slot 恒为 0；
// 保留 slot 字段是为了与 automation 同构、未来支持条件时不改结构。
type ruleKey struct {
	ruleID   uint
	slot     uint8
	deviceID uint
}

type compiledPlan struct {
	selectorHash uint64
	plan         sensorfield.Plan
}

type planCache struct {
	mu    sync.RWMutex
	plans map[ruleKey]compiledPlan

	resolvedSeen atomic.Pointer[sync.Map] // map[uint]struct{}
	firedSeen    atomic.Pointer[sync.Map] // map[uint]struct{}
}

func newPlanCache() *planCache {
	c := &planCache{plans: make(map[ruleKey]compiledPlan)}
	c.resolvedSeen.Store(&sync.Map{})
	c.firedSeen.Store(&sync.Map{})
	return c
}

func (c *planCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.plans = make(map[ruleKey]compiledPlan)
	c.mu.Unlock()
	c.resolvedSeen.Store(&sync.Map{})
	c.firedSeen.Store(&sync.Map{})
}

func (c *planCache) markResolvedSeen(ruleID uint) bool {
	if c == nil {
		return true
	}
	seen := c.resolvedSeen.Load()
	if seen == nil {
		return true
	}
	_, loaded := seen.LoadOrStore(ruleID, struct{}{})
	return !loaded
}

func (c *planCache) markFiredSeen(ruleID uint) bool {
	if c == nil {
		return true
	}
	seen := c.firedSeen.Load()
	if seen == nil {
		return true
	}
	_, loaded := seen.LoadOrStore(ruleID, struct{}{})
	return !loaded
}

func (c *planCache) get(key ruleKey) (compiledPlan, bool) {
	if c == nil {
		return compiledPlan{}, false
	}
	c.mu.RLock()
	entry, ok := c.plans[key]
	c.mu.RUnlock()
	return entry, ok
}

func (c *planCache) put(key ruleKey, selectorHash uint64, plan sensorfield.Plan) {
	if c == nil || plan == nil {
		return
	}
	c.mu.Lock()
	c.plans[key] = compiledPlan{selectorHash: selectorHash, plan: plan}
	c.mu.Unlock()
}

// compileSelector 取缓存或编译 selector；deviceID 必须是 Evaluate 的入参。
func (e *Evaluator) compileSelector(ruleID uint, deviceID uint, selector string) (sensorfield.Plan, error) {
	provider := e.planProviderSnapshot()
	if provider == nil {
		return nil, errPlanProviderMissing{}
	}
	hash := selectorHash(selector)
	key := ruleKey{ruleID: ruleID, slot: 0, deviceID: deviceID}
	if entry, ok := e.plans.get(key); ok && entry.selectorHash == hash {
		planEpoch := sensorfield.PlanEpoch(entry.plan)
		if planEpoch == 0 || planEpoch == provider.Epoch() {
			return entry.plan, nil
		}
	}
	plan, err := provider.Compile(deviceID, selector)
	if err != nil {
		return nil, err
	}
	e.plans.put(key, hash, plan)
	return plan, nil
}

type errPlanProviderMissing struct{}

func (errPlanProviderMissing) Error() string { return "field resolver is not configured" }

func selectorHash(selector string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(selector)))
	return h.Sum64()
}

// isSpecialSelector 报告 selector 是否属于特殊形态（family/semantic/virtual）。
// 与 automation 同源实现（各自持有副本，避免 alert→automation 依赖）。
func isSpecialSelector(selector string) bool {
	// 性能契约（#10a 裁决 (a)）：零分配、单遍字节扫描；文法与历史实现逐字节等价。
	s := selector
	for len(s) > 0 {
		c := s[0]
		if c != 32 && c != 9 && c != 10 && c != 13 {
			break
		}
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != 32 && c != 9 && c != 10 && c != 13 {
			break
		}
		s = s[:len(s)-1]
	}
	if s == "" {
		return false
	}
	if s[0] == 64 {
		return true
	}
	if len(s) >= 8 && s[:8] == "virtual:" {
		return true
	}
	for i := 1; i < len(s); i++ {
		if s[i] != 42 || s[i-1] != 95 {
			continue
		}
		prefix := s[:i-1]
		if !validFamilyPrefix(prefix) {
			return false
		}
		rest := s[i+1:]
		if rest == "" {
			return true
		}
		if rest[0] != 58 {
			return false
		}
		for j := 1; j < len(rest); j++ {
			if rest[j] == 58 {
				return false
			}
		}
		return true
	}
	return false
}

func validFamilyPrefix(prefix string) bool {
	if len(prefix) < 2 {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	last := prefix[len(prefix)-1]
	return (last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')
}

func toReadings(fields []parser.Field) []sensorfield.Reading {
	if len(fields) == 0 {
		return nil
	}
	out := make([]sensorfield.Reading, len(fields))
	for i := range fields {
		out[i] = sensorfield.Reading{Name: fields[i].Name, Value: fields[i].Value, Unit: fields[i].Unit}
	}
	return out
}

// evalSelector 是告警单字段求值入口（literal 路径与历史 matchField 逐字节等价）。
func (e *Evaluator) evalSelector(rule models.AlertRule, deviceID uint, fields []parser.Field, at time.Time) (bool, bool, float64) {
	selector := rule.SensorName
	// 内联形态快判（与 isSpecialSelector 等价）。
	special := false
	if selector != "" {
		c := selector[0]
		switch {
		case c == 64:
			special = true
		case len(selector) >= 8 && selector[:8] == "virtual:":
			special = true
		case c == 32 || c == 9 || c == 10 || c == 13:
			special = isSpecialSelector(selector)
		default:
			for i := 0; i < len(selector); i++ {
				if selector[i] == 42 {
					special = isSpecialSelector(selector)
					break
				}
			}
		}
	}
	// G-6 literal/legacy 快路径：与历史 matchField+compare 逐字节等价，
	// 不触碰 provider/编译缓存/统计。
	if !special {
		value, found := matchField(fields, selector)
		if !found {
			return false, false, 0
		}
		return true, compare(rule.Comparator, value, rule.Threshold), value
	}
	provider := e.planProviderSnapshot()
	if provider == nil {
		e.logUnresolved(rule, at, "field_resolver_not_configured", selector)
		return false, false, 0
	}
	plan, err := e.compileSelector(rule.ID, deviceID, selector)
	if err != nil {
		e.logUnresolved(rule, at, "compile_failed:"+sensorfield.ErrorCode(err), selector)
		return false, false, 0
	}
	if plan.Kind() == sensorfield.KindLiteral {
		e.markRuleResolved(rule.ID)
		value, found := matchField(fields, selector)
		if !found {
			return false, false, 0
		}
		return true, compare(rule.Comparator, value, rule.Threshold), value
	}
	if !provider.Enabled() {
		// kill switch：已编译的特殊 plan 在关闭态拒绝（B7）；不降级为字面量。
		e.logUnresolved(rule, at, "field_resolver_disabled", selector)
		return false, false, 0
	}
	verdict := sensorfield.EvalPredicate(plan, toReadings(fields), sensorfield.CmpOp(rule.Comparator), rule.Threshold)
	if !verdict.OK {
		e.logUnresolved(rule, at, verdict.Reason, selector)
		return false, false, 0
	}
	e.markRuleResolved(rule.ID)
	return true, verdict.Satisfied, verdict.Value
}

// logUnresolved 按 (ruleID, selectorHash) 5 分钟节流记一条 warn。
//
// 告警引擎不写事件表（Lead 裁决：alert_events 只有 firing/resolved），
// 因此解析失败的可观测性 = 日志 + RuleNeverResolved 指标。
func (e *Evaluator) logUnresolved(rule models.AlertRule, at time.Time, reason, selector string) {
	if reason == "" {
		reason = "unbound"
	}
	key := unresolvedKey{ruleID: rule.ID, selectorHash: selectorHash(selector)}
	e.auditMu.Lock()
	last, seen := e.unresolvedLog[key]
	if seen && at.Sub(last) < unresolvedLogInterval {
		e.auditMu.Unlock()
		return
	}
	e.unresolvedLog[key] = at
	e.auditMu.Unlock()
	logger.Warn("alert: field selector evaluation failed",
		"rule_id", rule.ID, "rule_name", rule.Name, "selector", selector, "reason", reason)
}

type unresolvedKey struct {
	ruleID       uint
	selectorHash uint64
}

// ruleStat 与 automation 同构：hasSpecial/resolved/fired。
type ruleStat struct {
	hasSpecial bool
	resolved   bool
	fired      bool
}

func (e *Evaluator) ruleStatLocked(ruleID uint) *ruleStat {
	if e.ruleStats == nil {
		e.ruleStats = make(map[uint]*ruleStat)
	}
	stat, ok := e.ruleStats[ruleID]
	if !ok {
		stat = &ruleStat{}
		e.ruleStats[ruleID] = stat
	}
	return stat
}

func (e *Evaluator) markRuleResolved(ruleID uint) {
	if !e.plans.markResolvedSeen(ruleID) {
		return
	}
	e.statsMu.Lock()
	stat := e.ruleStatLocked(ruleID)
	first := stat.hasSpecial && !stat.resolved
	stat.resolved = true
	e.statsMu.Unlock()
	if first {
		e.refreshRuleGauges()
	}
}

func (e *Evaluator) markRuleFired(ruleID uint) {
	if !e.plans.markFiredSeen(ruleID) {
		return
	}
	e.statsMu.Lock()
	stat := e.ruleStatLocked(ruleID)
	first := stat.hasSpecial && !stat.fired
	stat.fired = true
	e.statsMu.Unlock()
	if first {
		e.refreshRuleGauges()
	}
}

// rebuildRuleStats 在 LoadRules 时按最新规则集合重建（保留仍存在规则的进度）。
func (e *Evaluator) rebuildRuleStats(rules map[uint]models.AlertRule) {
	e.statsMu.Lock()
	next := make(map[uint]*ruleStat, len(rules))
	for id, rule := range rules {
		stat := &ruleStat{hasSpecial: isSpecialSelector(rule.SensorName)}
		if old, ok := e.ruleStats[id]; ok {
			stat.resolved = old.resolved
			stat.fired = old.fired
		}
		next[id] = stat
	}
	e.ruleStats = next
	e.statsMu.Unlock()
	e.refreshRuleGauges()
	// LoadRules 期预编译特殊 selector（literal 不编译；#10a 裁决 (b)）。
	e.precompileSpecialPlans()
}

// refreshRuleGauges 覆盖式 Set（指标由 sensorfield 定义，禁止重复定义）。
func (e *Evaluator) refreshRuleGauges() {
	e.statsMu.Lock()
	neverResolved, neverFired := 0, 0
	for _, stat := range e.ruleStats {
		if !stat.hasSpecial {
			continue
		}
		if !stat.resolved {
			neverResolved++
			continue
		}
		if !stat.fired {
			neverFired++
		}
	}
	e.statsMu.Unlock()
	sensorfield.RuleNeverResolved.WithLabelValues("alert").Set(float64(neverResolved))
	sensorfield.RuleNeverFired.WithLabelValues("alert").Set(float64(neverFired))
}

func (e *Evaluator) precompileSpecialPlans() {
	provider := e.planProviderSnapshot()
	if provider == nil {
		return
	}
	e.mu.RLock()
	rules := make([]models.AlertRule, 0, len(e.rules))
	for _, rule := range e.rules {
		rules = append(rules, rule)
	}
	e.mu.RUnlock()
	for _, rule := range rules {
		selector := rule.SensorName
		if !isSpecialSelector(selector) {
			continue
		}
		hash := selectorHash(selector)
		key := ruleKey{ruleID: rule.ID, slot: 0, deviceID: rule.TargetID}
		if entry, ok := e.plans.get(key); ok && entry.selectorHash == hash {
			if planEpoch := sensorfield.PlanEpoch(entry.plan); planEpoch == 0 || planEpoch == provider.Epoch() {
				continue
			}
		}
		plan, err := provider.Compile(rule.TargetID, selector)
		if err != nil {
			logger.Warn("alert: precompile special selector failed; will retry lazily at evaluation",
				"rule_id", rule.ID, "target_id", rule.TargetID, "selector", selector, "error", err)
			continue
		}
		e.plans.put(key, hash, plan)
	}
}
