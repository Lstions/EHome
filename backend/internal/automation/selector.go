package automation

// 数据字段解析层（sensorfield）在 automation 求值热路径上的接入。
//
// 设计依据：docs/设计/数据字段解析层-方案-2026-10-21.md
//   §7.2 数据面（旁路 plan、内容缓存键、literal 快速路径、kill switch）
//   §9.2#1/#2 修改点（trigger 与 conditions 都必须走 plan）
//   §10 可观测性（ehome_rule_never_resolved / ehome_rule_never_fired）
//
// 三条硬性裁决（2026-10-21，Lead/窄复核）：
//  1. **缓存不能用 map[ruleID]**：一条规则有 trigger + N 个 conditions（多个 selector），
//     且 TriggerEdgeDeviceID=0 的 any-device 规则会被多台设备交替上报 ⇒
//     key 必须含 (ruleID, slot, deviceID)。slot=0 为 trigger，1..N 为 conditions 下标。
//  2. **deviceID 一律取 Evaluate(edgeDeviceID, ...) 的入参**，不是 rule.TriggerEdgeDeviceID：
//     规则可能绑定逻辑设备（merge-chain），用逻辑 ID 查 KnownFields 会恒 unbound。
//  3. **不用 rule.UpdatedAt 当内容版本**：planner 的 last_triggered_at 簿记会 bump
//     UpdatedAt（触发计数器 ≠ 内容版本）。内容变化由 selectorHash 覆盖；窗口状态由
//     sameSensorTriggerState 白名单在 LoadRules 清理（evaluator.go）。
//
// 比较**必须**走 sensorfield.EvalPredicate：family any/all 的谓词语义与比较符绑定，
// Plan.Eval 在 any/all 上返回 unbound（防静默错语义），绝不能拿来代替。

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

// automation_events.result 的两个解析层专用结果码（设计 §10 + 窄复核 N-2 拆分）。
//
// 为什么拆两个（Lead 2026-10-21 裁决）：unresolved_selector = "语法合法但本帧解析不到"；
// field_resolver_disabled = "开关关闭导致无法求值"（含 enabled=false 的混合规则整条
// fail-closed）。两者运维处置不同（前者查字段/换型，后者是配置/回滚），合并会让
// 告警分不清"配错"与"开关"。models 常量表与前端枚举由 impl-wiring/D5 批次同步
// （models 不在本任务写入范围），本包常量承载同一字符串值。
const (
	ResultUnresolvedSelector    = "unresolved_selector"
	ResultFieldResolverDisabled = "field_resolver_disabled"
)

// unresolvedAuditInterval 是"解析不到"审计的节流周期（设计 §12.1：5 分钟）。
const unresolvedAuditInterval = 5 * time.Minute

// PlanProvider 是解析层控制面在本包的最小依赖（生产实现：*sensorfield.Registry）。
type PlanProvider interface {
	Compile(deviceID uint, selector string) (sensorfield.Plan, error)
	Enabled() bool
	Epoch() uint64
}

// errNoPlanProvider 表示未接线（纯字面量仍可用；特殊形态 fail-closed）。
var errNoPlanProvider = errPlanProviderMissing{}

type errPlanProviderMissing struct{}

func (errPlanProviderMissing) Error() string { return "field resolver is not configured" }

// ruleKey 是编译缓存的 key：slot=0 为 trigger，1..N 为 conditions 下标。
type ruleKey struct {
	ruleID   uint
	slot     uint8
	deviceID uint
}

// compiledPlan 是一条 selector 的编译产物。有效性由 selectorHash 与 plan 自带
// 的编译期 epoch 共同判定（sensorfield.PlanEpoch；避免"先 Compile 再读
// Registry.Epoch"的竞态 —— impl-core 2026-10-21 明确要求）。
type compiledPlan struct {
	selectorHash uint64
	plan         sensorfield.Plan
}

// planCache 是包内旁路缓存（不挂 models.AutomationRule，避免 models→sensorfield 包环）。
//
// resolvedSeen/firedSeen 是**无锁统计去重**（#10a 裁决 (c-轻)）：literal 规则根本不进
// 统计（evalSelector 已短路）；特殊规则每帧 markRuleResolved 先查 sync.Map，命中即返回，
// 不触碰 statsMu；只有"本批次首次"才走锁 + 指标刷新。用 atomic.Pointer 持有 sync.Map，
// reset 整体替换，避免复制已使用的 sync.Map（noCopy）。
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

// reset 清空全部编译产物（LoadRules/Invalidate 调用；设计 §7.2）。
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

// markResolvedSeen 返回是否"本批次首次解析成功"（命中即无锁快路径，不碰 statsMu）。
func (c *planCache) markResolvedSeen(ruleID uint) bool {
	if c == nil {
		return true
	}
	seen := c.resolvedSeen.Load()
	if seen == nil {
		return true // 未经 newPlanCache 的测试替身 ⇒ 走完整路径，语义不变
	}
	_, loaded := seen.LoadOrStore(ruleID, struct{}{})
	return !loaded
}

// markFiredSeen 返回是否"本批次首次触发"。
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

// compileSelector 取缓存或编译一条 selector。deviceID 必须是 Evaluate 的入参。
func (e *Evaluator) compileSelector(ruleID uint, slot uint8, deviceID uint, selector string) (sensorfield.Plan, error) {
	provider := e.planProviderSnapshot()
	if provider == nil {
		return nil, errNoPlanProvider
	}
	hash := selectorHash(selector)
	key := ruleKey{ruleID: ruleID, slot: slot, deviceID: deviceID}
	if entry, ok := e.plans.get(key); ok && entry.selectorHash == hash {
		planEpoch := sensorfield.PlanEpoch(entry.plan)
		// planEpoch==0 表示实现未携带 epoch（测试替身/未来实现）：按"当前 epoch"保守命中。
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

// selectorHash 是 selector 字符串的稳定哈希（FNV-1a 64）。
func selectorHash(selector string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(selector)))
	return h.Sum64()
}

// isSpecialSelector 报告 selector 是否属于**特殊形态**（family/semantic/virtual）。
//
// 解析层不可用/编译失败时，历史**字面量**规则必须保持与今天逐字节相同的
// matchField 行为（G-6）；特殊形态必须 fail-closed（设计 §11 kill switch：
// 不自动降级为字面量，否则 family 会被当不存在的物理名静默失效）。
func isSpecialSelector(selector string) bool {
	// 性能契约（#10a 裁决 (a)）：零分配、单遍字节扫描、最多两次短循环；
	// literal 规则每帧都会走到这里，目标 ≤1ns。语义与历史实现逐字节等价：
	// 前后 ASCII 空白 ≡ TrimSpace（selector 文法本身就是 ASCII）；'@' ⇒ semantic；
	// "virtual:" ⇒ virtual；首处 "_*" 的前缀须合法，其后为空或 ":agg"（至多一个冒号）。
	s := selector
	for len(s) > 0 {
		switch s[0] {
		case ' ', '\t', '\n', '\r':
			s = s[1:]
			continue
		}
		break
	}
	for len(s) > 0 {
		switch s[len(s)-1] {
		case ' ', '\t', '\n', '\r':
			s = s[:len(s)-1]
			continue
		}
		break
	}
	if s == "" {
		return false // 空串：历史行为是"找不到同名字段"，按字面量处理（不审计刷量）
	}
	if s[0] == '@' {
		return true
	}
	if len(s) >= 8 && s[:8] == "virtual:" {
		return true
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '*' || s[i-1] != '_' {
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
		if rest[0] != ':' {
			return false
		}
		for j := 1; j < len(rest); j++ {
			if rest[j] == ':' {
				return false
			}
		}
		return true
	}
	return false
}

// validFamilyPrefix：family 前缀文法 = lower {lower|digit|_} lower-or-digit
// （方案 §5.1；至少 2 字符，不得以 _ 结尾）。
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

// toReadings 把解析后的物理量帧转换为解析层视图（零成本结构转换）。
// 保留原始顺序：literal 的"取第一个同名"语义依赖它。
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

// evalSelector 是 trigger/conditions 的统一求值入口。
//
// 返回 (ok, satisfied, value)：
//   - ok=false 表示本批**无法判定**（历史字面量没该字段 / 特殊 selector unbound /
//     kill switch 关闭 / 编译失败）⇒ 调用方不更新窗口、不触发；
//   - satisfied 是 predicate 结论（ok=true 时有效）；
//   - value 用于 TriggerEvent/审计展示（literal 为原始上报值；特殊 selector 为
//     predicate 参与比较的值）。
//
// literal 路径：走历史 matchField + compare，与今天逐字节等价（含"取第一个同名"、
// 含 NaN/Inf 的旧比较语义）——G-6 的硬要求；特殊 selector 才使用 EvalPredicate。
func (e *Evaluator) evalSelector(ruleID uint, slot uint8, deviceID uint,
	selector, comparator string, threshold float64, fields []parser.Field, at time.Time) (bool, bool, float64) {

	// ① G-6 literal/legacy 快路径（#10a 裁决）：非特殊形态与历史 matchField **逐字节等价**
	// （含"取第一个同名"、含 NaN/Inf 旧比较语义），且**不触碰** provider/编译缓存/统计。
	// 目标 ≤3ns/0 alloc；混合规则里的 literal slot 也走这里。
	//
	// 内联形态快判（与 isSpecialSelector 等价；热路径避免一次函数调用）：
	// 首字节 '@' ⇒ semantic；前 8 字节 "virtual:" ⇒ virtual；首字节为空白 ⇒ 回退完整
	// 判定（legacy 异常值）；否则只需扫一遍找 '*'（family 必含 "*"），没有就是字面量。
	special := false
	if selector != "" {
		c := selector[0]
		switch {
		case c == '@':
			special = true
		case len(selector) >= 8 && selector[:8] == "virtual:":
			special = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			special = isSpecialSelector(selector) // 前导空白：legacy 异常形态，走完整判定
		default:
			for i := 0; i < len(selector); i++ {
				if selector[i] == '*' {
					special = isSpecialSelector(selector)
					break
				}
			}
		}
	}
	if !special {
		value, found := matchField(fields, selector)
		if !found {
			return false, false, 0
		}
		return true, compare(comparator, value, threshold), value
	}

	// ② 以下只处理特殊形态（family/semantic/virtual），全部 fail-closed 方向。
	provider := e.planProviderSnapshot()
	if provider == nil {
		e.recordUnresolvedSelector(ruleID, at, ResultUnresolvedSelector, "field_resolver_not_configured", selector)
		return false, false, 0
	}

	plan, err := e.compileSelector(ruleID, slot, deviceID, selector)
	if err != nil {
		e.recordUnresolvedSelector(ruleID, at, ResultUnresolvedSelector, "compile_failed:"+sensorfield.ErrorCode(err), selector)
		return false, false, 0
	}
	if plan.Kind() == sensorfield.KindLiteral {
		// 防御：provider 对特殊 selector 返回了 literal plan（测试替身/实现不一致）。
		// 语义上按历史字面量处理（与旧实现一致），仅用于非生产路径。
		e.markRuleResolved(ruleID)
		value, found := matchField(fields, selector)
		if !found {
			return false, false, 0
		}
		return true, compare(comparator, value, threshold), value
	}
	if !provider.Enabled() {
		// kill switch（B7）：已编译的特殊 plan 在关闭态也必须拒绝；混合规则
		// （literal trigger + 特殊 conditions）整条 fail-closed，审计用专用 result。
		e.recordUnresolvedSelector(ruleID, at, ResultFieldResolverDisabled, "field_resolver_disabled", selector)
		return false, false, 0
	}

	verdict := sensorfield.EvalPredicate(plan, toReadings(fields), sensorfield.CmpOp(comparator), threshold)
	if !verdict.OK {
		e.recordUnresolvedSelector(ruleID, at, ResultUnresolvedSelector, verdict.Reason, selector)
		return false, false, 0
	}
	e.markRuleResolved(ruleID)
	return true, verdict.Satisfied, verdict.Value
}

// recordUnresolvedSelector 落一条"选择器无法求值"审计。
//
// 去重键 = (ruleID, selectorHash)，窗口 5 分钟（Lead 2026-10-21：否则 30s 扫描 ×
// 规则数 = 2880 条/日/规则；按 selector 去重还能让"同规则 trigger+conditions 两个
// 不同 selector 各自留痕"）。为什么必须落审计而不是静默 return：配错的 selector
// 若只是"永不触发"，正是本方案要消灭的静默失效（G-4）。
func (e *Evaluator) recordUnresolvedSelector(ruleID uint, at time.Time, resultCode, reason, selector string) {
	if e.db == nil {
		return
	}
	if reason == "" {
		reason = "unbound"
	}
	key := unresolvedKey{ruleID: ruleID, selectorHash: selectorHash(selector)}
	e.auditMu.Lock()
	last, seen := e.unresolvedAudit[key]
	if seen && at.Sub(last) < unresolvedAuditInterval {
		e.auditMu.Unlock()
		return
	}
	e.unresolvedAudit[key] = at
	e.auditMu.Unlock()

	ev := models.AutomationEvent{
		RuleID:      ruleID,
		TriggeredAt: at,
		Result:      resultCode,
		Detail:      "field resolution failed: " + reason + " (selector=" + selector + ")",
		CreatedAt:   at,
	}
	if err := e.db.Create(&ev).Error; err != nil {
		logger.Warn("automation: failed to record field-resolution event",
			"rule_id", ruleID, "selector", selector, "result", resultCode, "reason", reason, "error", err)
	}
}

// unresolvedKey 是审计去重键（ruleID + selector 哈希）。
type unresolvedKey struct {
	ruleID       uint
	selectorHash uint64
}

// ── 规则级统计（ehome_rule_never_resolved / ehome_rule_never_fired）──
//
// 指标语义（impl-core 口径，2026-10-21）：当前值 = 当前未达标规则数（不是累计量）。
// 快照持久列（resolve_count/match_count）由 impl-wiring 的 models 批次提供；本批
// 先用进程内状态（LoadRules 重建 + 首次解析/触发置位），语义一致、规则删除自然变小。
// 只统计 sensor_threshold 规则：time_window/sun_event 不做字段解析，纳入会把
// "从未解析"污染成恒真。

// ruleStat 是一条规则的解析/触发状态。
type ruleStat struct {
	hasSpecial bool // 是否含特殊 selector（需要解析层才能判定）
	resolved   bool // 至少一次 predicate OK（"解析成功"）
	fired      bool // 至少触发过一次
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

// markRuleResolved 在第一次解析成功时置位并刷新指标。
//
// 热路径优化（#10a / (c-轻)）：先走 planCache 的 sync.Map 无锁去重；命中即返回，
// 不触碰 statsMu。literal 规则根本不会调用本函数（evalSelector 已短路）。
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

// markRuleFired 在规则第一次触发时置位并刷新指标（同上：sync.Map 无锁快路径）。
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

// rebuildRuleStats 在 LoadRules 时按最新规则集合重建统计（保留仍存在规则的进度）。
func (e *Evaluator) rebuildRuleStats(rules map[uint]models.AutomationRule) {
	e.statsMu.Lock()
	next := make(map[uint]*ruleStat, len(rules))
	for id, rule := range rules {
		if rule.TriggerType != models.AutomationTriggerSensorThreshold {
			continue // 时钟驱动规则没有"字段解析"概念
		}
		stat := &ruleStat{hasSpecial: ruleHasSpecialSelector(rule)}
		if old, ok := e.ruleStats[id]; ok {
			stat.resolved = old.resolved
			stat.fired = old.fired
		}
		next[id] = stat
	}
	e.ruleStats = next
	e.statsMu.Unlock()
	e.refreshRuleGauges()
	// (b) LoadRules 期预编译特殊 selector 的 plan（literal 规则不编译）；
	// 预编译失败只 WARN，求值时仍会按缓存 miss 惰性重试（不改变参与/不参与语义）。
	e.precompileSpecialPlans()
}

// precompileSpecialPlans 在规则装载期把特殊 selector 预编译为 plan（#10a 裁决 (b)）。
//
// 目的：把"首帧编译"移出求值热路径；literal 规则**不编译**（G-6 走 legacy matchField）。
// 跳过 any-device 规则（TriggerEdgeDeviceID==0）：编译必须绑定具体 deviceID，
// 求值时按 Evaluate 入参惰性编译（缓存 key 含 deviceID，语义不变）。
func (e *Evaluator) precompileSpecialPlans() {
	provider := e.planProviderSnapshot()
	if provider == nil {
		return
	}
	e.mu.RLock()
	rules := make([]models.AutomationRule, 0, len(e.rules))
	for _, rule := range e.rules {
		rules = append(rules, rule)
	}
	e.mu.RUnlock()
	for _, rule := range rules {
		if rule.TriggerEdgeDeviceID == 0 {
			continue
		}
		e.precompileSelector(provider, rule.ID, 0, rule.TriggerEdgeDeviceID, rule.TriggerSensorName)
		conds, err := rule.ParseConditions()
		if err != nil {
			continue // 非法 conditions 由求值路径 fail-closed；此处不越权改语义
		}
		for i := range conds {
			e.precompileSelector(provider, rule.ID, uint8(i+1), rule.TriggerEdgeDeviceID, conds[i].SensorName)
		}
	}
}

// precompileSelector 预编译单个特殊 selector slot（幂等：缓存同内容同 epoch 则跳过）。
func (e *Evaluator) precompileSelector(provider PlanProvider, ruleID uint, slot uint8, deviceID uint, selector string) {
	if !isSpecialSelector(selector) {
		return // literal 不编译（G-6）
	}
	hash := selectorHash(selector)
	key := ruleKey{ruleID: ruleID, slot: slot, deviceID: deviceID}
	if entry, ok := e.plans.get(key); ok && entry.selectorHash == hash {
		if planEpoch := sensorfield.PlanEpoch(entry.plan); planEpoch == 0 || planEpoch == provider.Epoch() {
			return
		}
	}
	plan, err := provider.Compile(deviceID, selector)
	if err != nil {
		logger.Warn("automation: precompile special selector failed; will retry lazily at evaluation",
			"rule_id", ruleID, "slot", slot, "device_id", deviceID, "selector", selector, "error", err)
		return
	}
	e.plans.put(key, hash, plan)
}

// refreshRuleGauges 覆盖式 Set 两个 Gauge（promauto 指标由 sensorfield 定义，禁止重复定义）。
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
	sensorfield.RuleNeverResolved.WithLabelValues("automation").Set(float64(neverResolved))
	sensorfield.RuleNeverFired.WithLabelValues("automation").Set(float64(neverFired))
}

// ruleHasSpecialSelector 报告规则的 trigger 或任一附加条件是否使用特殊 selector。
func ruleHasSpecialSelector(rule models.AutomationRule) bool {
	if isSpecialSelector(rule.TriggerSensorName) {
		return true
	}
	conds, err := rule.ParseConditions()
	if err != nil {
		return true // 非法 JSON：按"需要解析"统计（fail-closed，别让它从指标里消失）
	}
	for _, c := range conds {
		if isSpecialSelector(c.SensorName) {
			return true
		}
	}
	return false
}
