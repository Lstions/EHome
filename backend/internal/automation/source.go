package automation

// 规则「数据来源」求值接入（方案《规则数据来源与复合来源》§5 / §12.2 / §12.9）。
//
// M0 批次（task-29）在本包落四件事：
//   1. 窗口来源身份 sourceIdentity（S1/S2：窗口不得跨身份拼接；S8 见 pruneWindow）；
//   2. TriggerEvent 的触发证据（SourceEdgeDeviceID/SourceGroupID/SourceKind/
//      TransitionSeq/SourceAt）+ 唯一构造 helper（生产 3 个构造点全走它）；
//   3. evaluator 的 active 过滤与复合整组求值（primary_backup 只收 active 帧，
//      composite 任一成员帧驱动整组 CombineGroup）；
//   4. 求值期的来源失败审计（source_member_changed / source_stale /
//      source_degraded / source_resolver_not_configured）。
//
// 红线（方案 §5.1）：
//   - literal 路径与历史 matchField 逐字节等价（G-6）；来源组只是**匹配与取值**的
//     上层分派，不改变单帧 selector 求值语义；
//   - source_kind 为 NULL 的存量规则除 F4 缺陷修复与 S8 gap 上限外行为不变（G-1）；
//   - resolver 未接线时 spec source_group 规则必须 fail-closed 并落审计，
//     不得静默跳过（否则上线顺序错误=静默失效）。

import (
	"encoding/json"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"
)

// automation_events.result：来源组专用结果码（方案 §12.9 / N-18 的 5 个码）。
//
// models 常量表、仿真白名单（simulation/catalog/audit.go）与前端枚举的同步由
// Lead 的 models/前端批次负责（models 不在本任务写入范围）——本包常量承载同一
// 字符串值，与既有 ResultUnresolvedSelector 同款约定。
const (
	// ResultSourceMemberChanged 触发成员已非 active / 组已删改（F4 FC-1/FC-5）。
	ResultSourceMemberChanged = "source_member_changed"
	// ResultSourceStale 触发证据过期（F4 FC-2）。
	ResultSourceStale = "source_stale"
	// ResultSourceDegraded 复合组带降级求值（有成员缺席但策略允许继续）。
	ResultSourceDegraded = "source_degraded"
	// ResultSourceResolverNotConfigured 来源解析器未接线（F4 FC-4 / 求值期 E-6）。
	ResultSourceResolverNotConfigured = "source_resolver_not_configured"
	// ResultSourceGroupShadow 灰度 audit_only 模式（只落审计、零下发）。
	ResultSourceGroupShadow = "source_group_shadow"
)

// sourceAuditInterval 是来源失败审计的节流周期。与 selector 审计同款 5 分钟，
// 但去重键是 (ruleID, resultCode+reason)：同一帧高频上报只留一条可读证据。
const sourceAuditInterval = 5 * time.Minute

// SourceResolver 是 sourceref.Registry 在本包的最小依赖（与 PlanProvider 同思路，
// 避免把 *Registry 的完整方法集钉进求值器；nil = 未接线，来源组规则 fail-closed）。
type SourceResolver interface {
	MatchFrame(edgeDeviceID uint, kind string, groupID uint) sourceref.FrameMatch
	Group(id uint) (*sourceref.GroupSnapshot, bool)
}

// SetSourceResolver 注入来源组解析器（数据源状态机发布的不可变快照）。
func (e *Evaluator) SetSourceResolver(r SourceResolver) {
	e.sourceResolver = r
}

// SetSourceHealthFn 注入「来源降级可见性」回调（写 source_group_health）。
//
// 为什么用回调而不是依赖 datasource：automation 不得反向依赖 datasource 包
// （方案红线），健康写库能力由 main.go 注入（生产实现建议 datasource/sourcegroup
// 的组健康写入器）。
//
// 调用语义：**每次**求值期判定组来源失败都会调用（含审计节流命中的那几次），
// reason 形如 "source_degraded:no_available_member"（类别:细节），实现方负责
// 按 (groupID, reason) 节流（建议 1 分钟 1 条），否则高频帧会刷爆健康表。
// nil = 未接线：只落 automation_events 审计，不写健康表（既有测试不受影响）。
func (e *Evaluator) SetSourceHealthFn(fn func(groupID uint, reason string)) {
	e.sourceHealthFn = fn
}

// notifySourceHealth 是组来源失败的「可见性」单一出口：审计与健康回调同源判定，
// 避免"审计有、健康表没有"的运维盲区（e2e TestSG_E3 实测缺口）。
func notifySourceHealth(fn func(groupID uint, reason string), groupID uint, resultCode, reason string) {
	if fn == nil || groupID == 0 {
		return
	}
	detail := resultCode
	if reason != "" && reason != resultCode {
		detail = resultCode + ":" + reason
	}
	fn(groupID, detail)
}

// SetDeviceIntervalFn 注入设备采集间隔查询（S8 gap 上限 = 2×采集间隔）。
//
// 为什么用注入而不是查库：Evaluate 是每帧热路径，查 edge_devices 会把 5s 上报
// 放大成一次 DB 往返。main.go 接线时用进程内设备缓存/读取器实现；nil 或返回 0
// 表示"间隔取不到"⇒ 不启用 S8（方案 F-7：保持现状，不猜）。
func (e *Evaluator) SetDeviceIntervalFn(fn func(edgeDeviceID uint) time.Duration) {
	e.deviceIntervalFn = fn
}

// OnSourceTransition 是来源组切换钩子的引擎侧入口（清窗加速，见方案 §12.2.2）。
//
// 快照正确性不依赖本钩子（S3）：钩子只影响"多快清窗"；即使丢失，下一次求值发现
// identity 不一致也会清窗。清窗**保留** triggered（冷却锚，S4：宁可少执行，不可
// 重复动作）。
func (e *Evaluator) OnSourceTransition(groupID uint) {
	e.mu.Lock()
	ids := e.rulesByGroup[groupID]
	for _, id := range ids {
		delete(e.windows, id)
	}
	e.mu.Unlock()
	if len(ids) > 0 {
		logger.Info("automation: source transition cleared windows",
			"group_id", groupID, "rules", len(ids))
	}
}

// ── 触发证据与唯一构造 helper（方案 F-9）──────────────────────────────────

// triggerSource 是求值器填进 TriggerEvent 的来源证据。
//
// 为什么不能由 planner 自己从规则配置列反推：规则配置是"绑了哪个组"，而 F4 需要
// 的是"这次触发到底是哪个组成员、哪一版快照、值有多旧"——配置列无法回答（切换后
// 配置列不变）。planner 用本证据做身份校验，**禁止**回读配置列或当前 active 值。
type triggerSource struct {
	kind          string // "" = 存量单设备；source_group = 来源组
	groupID       uint
	edgeDeviceID  uint
	transitionSeq uint64
	sourceAt      time.Time // 该成员值的时间戳（composite = 组合样本时间）
	membersJSON   string    // composite 各分量快照（审计用，可为空）
}

// newTriggerEvent 是 TriggerEvent 的**唯一构造 helper**（方案 F-9 SG-21）。
//
// 生产构造点共 3 处（sensor_threshold 求值、time_window tick、sun_event tick），
// 全部经本函数；测试构造同一（避免"加字段不会编译错"的静默漏填）。
// src=nil 表示时钟驱动规则（没有上报设备，SourceEdgeDeviceID 保持 0）。
func newTriggerEvent(rule models.AutomationRule, at time.Time, value float64,
	windowEdge string, src *triggerSource) TriggerEvent {
	ev := TriggerEvent{Rule: rule, Value: value, At: at, WindowEdge: windowEdge}
	if src != nil {
		ev.SourceKind = src.kind
		ev.SourceGroupID = src.groupID
		ev.SourceEdgeDeviceID = src.edgeDeviceID
		ev.TransitionSeq = src.transitionSeq
		ev.SourceAt = src.sourceAt
		ev.membersJSON = src.membersJSON
	}
	return ev
}

// legacyTriggerSource 为存量单设备规则构造证据（SourceKind 保持空串 = 老路）。
func legacyTriggerSource(edgeDeviceID uint, at time.Time) *triggerSource {
	return &triggerSource{edgeDeviceID: edgeDeviceID, sourceAt: at}
}

// ── 窗口来源身份（S1/S2）───────────────────────────────────────────────────

// sourceIdentity 是窗口样本的来源身份。**按 kind 分流**（方案裁决 2）：
//
//	存量 edge_device：{kind:0, edgeDeviceID}（revision/membersHash 恒零）
//	primary_backup  ：{kind:1, groupID, edgeDeviceID, revision, membersHash}
//	composite       ：{kind:2, groupID, revision, membersHash}  —— 不含触发成员（S7）
//
// 为什么存量规则也要填 identity：S1 的"窗口不得跨身份拼接"对存量是恒等条件
// （同规则同设备），填上只是让判据统一；不会改变行为。
type sourceIdentity struct {
	kind         uint8
	groupID      uint
	edgeDeviceID uint
	revision     int64
	membersHash  string
}

const (
	identityLegacy    uint8 = 0
	identityBackup    uint8 = 1
	identityComposite uint8 = 2
)

func (a sourceIdentity) equal(b sourceIdentity) bool {
	return a.kind == b.kind && a.groupID == b.groupID && a.edgeDeviceID == b.edgeDeviceID &&
		a.revision == b.revision && a.membersHash == b.membersHash
}

func legacyIdentity(edgeDeviceID uint) sourceIdentity {
	return sourceIdentity{kind: identityLegacy, edgeDeviceID: edgeDeviceID}
}

// identityForFrame 按 kind 生成身份（primary_backup 含触发成员，composite 不含）。
func identityForFrame(m sourceref.FrameMatch, edgeDeviceID uint) sourceIdentity {
	if m.Kind == sourceref.KindComposite {
		return sourceIdentity{
			kind:        identityComposite,
			groupID:     m.GroupID,
			revision:    m.Revision,
			membersHash: snapshotMembersHash(m.Group),
		}
	}
	return sourceIdentity{
		kind:         identityBackup,
		groupID:      m.GroupID,
		edgeDeviceID: edgeDeviceID,
		revision:     m.Revision,
		membersHash:  snapshotMembersHash(m.Group),
	}
}

func snapshotMembersHash(g *sourceref.GroupSnapshot) string {
	if g == nil {
		return ""
	}
	return g.MembersHash
}

// sourceGap 返回该帧所属来源的 S8 gap 上限（2×采集间隔；取不到 ⇒ 0=不启用）。
func (e *Evaluator) sourceGap(edgeDeviceID uint) time.Duration {
	if e.deviceIntervalFn == nil {
		return 0
	}
	if d := e.deviceIntervalFn(edgeDeviceID); d > 0 {
		return 2 * d
	}
	return 0
}

// groupGap 返回组内成员的 S8 gap 上限（取成员间隔最大值；全 0 ⇒ 不启用）。
func (e *Evaluator) groupGap(g *sourceref.GroupSnapshot) time.Duration {
	if e.deviceIntervalFn == nil || g == nil {
		return 0
	}
	var max time.Duration
	for _, m := range g.Members {
		if d := e.deviceIntervalFn(m.EdgeDeviceID); d > max {
			max = d
		}
	}
	if max > 0 {
		return 2 * max
	}
	return 0
}

// ── 求值期：来源帧分派与复合整组求值 ──────────────────────────────────────

// matchSourceRule 把一个上报帧与一条"绑来源组"的规则做匹配。
//
// 返回 (match, ok)：ok=false 表示本帧不应驱动该规则（非成员 / 主备组的备成员帧 /
// 组缺失或未启用）。组缺失/未启用属于**失败**而不是"跳过"：落审计后不更新窗口。
func (e *Evaluator) matchSourceRule(rule models.AutomationRule, ref models.SourceRef,
	edgeDeviceID uint, at time.Time) (sourceref.FrameMatch, bool) {
	if e.sourceResolver == nil {
		// E-6：未接线必须可见地 fail-closed，不得静默跳过。
		e.recordSourceFailure(rule.ID, ref.GroupID, at, ResultSourceResolverNotConfigured,
			"source_resolver_not_configured")
		return sourceref.FrameMatch{}, false
	}
	m := e.sourceResolver.MatchFrame(edgeDeviceID, string(ref.Kind), ref.GroupID)
	if !m.Configured {
		// 组不存在/未启用：spec 规则无法求值 ⇒ 审计（方案 §7.1 E-4/E-6）。
		e.recordSourceFailure(rule.ID, ref.GroupID, at, ResultSourceMemberChanged, m.Reason)
		return m, false
	}
	if !m.Accepts() {
		return m, false // 主备组的备成员帧：丢弃（S1 的前提）
	}
	return m, true
}

// evalCompositeRule 处理"任一成员帧驱动的整组求值"（方案 §5.3）。
//
// 逐分量求值（禁止合并帧）：每个成员用 (成员 edge_device_id, 规则 selector) 取
// 最新值并做 staleness 判定，再交给 sourceref.CombineGroup。附加条件仍用**本次
// 上报帧**求值（与存量语义一致；composite 规则 conditions 见方案 §12.9.1）。
func (e *Evaluator) evalCompositeRule(rule models.AutomationRule, m sourceref.FrameMatch,
	deviceID uint, fields []parser.Field, at time.Time) {
	g := m.Group
	if g == nil {
		e.recordSourceFailure(rule.ID, g.ID, at, ResultSourceMemberChanged, sourceref.ReasonGroupMissing)
		return
	}
	if isSpecialSelector(rule.TriggerSensorName) {
		// M0 口径：复合组的成员读取按字面量物理名（sourceref 不感知 sensorfield）；
		// 特殊 selector 的逐成员解析留到 M1，当前 fail-closed 并可见。
		e.recordSourceFailure(rule.ID, g.ID, at, ResultSourceDegraded, "composite_special_selector_unsupported")
		return
	}
	res := sourceref.CombineGroup(g, func(ms sourceref.MemberSnapshot) sourceref.Component {
		if e.latestValueBySensorFn == nil {
			return sourceref.Component{State: sourceref.StateNotConfigured}
		}
		rec, ok := e.latestValueBySensorFn(ms.EdgeDeviceID, rule.TriggerSensorName)
		if !ok {
			return sourceref.Component{State: sourceref.StateMissing}
		}
		comp := sourceref.Component{Value: rec.Value, At: rec.Timestamp, State: sourceref.StateOK}
		if !rec.Timestamp.IsZero() && at.Sub(rec.Timestamp) > g.Staleness() {
			comp.State = sourceref.StateStale
		}
		return comp
	})
	if !res.OK {
		e.recordSourceFailure(rule.ID, g.ID, at, ResultSourceDegraded, res.Reason)
		return
	}
	if res.Degraded {
		// use_available：允许触发但必须可见（方案 §7.1 E-5 + 12.9.2）。
		e.recordSourceFailure(rule.ID, g.ID, at, ResultSourceDegraded, res.Reason)
	}
	// 附加条件仍按本次上报帧求值（存量语义）。
	if !e.conditionsSatisfied(rule, deviceID, fields, at) {
		return
	}
	satisfied := compare(rule.TriggerComparator, res.Value, rule.TriggerThreshold)
	src := &triggerSource{
		kind:          sourceref.GroupKindSourceGroup,
		groupID:       g.ID,
		edgeDeviceID:  deviceID,
		transitionSeq: g.TransitionSeq,
		sourceAt:      at,
		membersJSON:   componentsJSON(res.Components),
	}
	e.applyTrigger(rule, at, res.Value, satisfied, identityForFrame(m, deviceID), src, e.groupGap(g))
}

// componentsJSON 序列化复合分量快照（automation_events.members_json）。
func componentsJSON(comps []sourceref.Component) string {
	if len(comps) == 0 {
		return ""
	}
	type item struct {
		MemberID     uint    `json:"member_id"`
		EdgeDeviceID uint    `json:"edge_device_id"`
		Value        float64 `json:"value"`
		AgeMS        int64   `json:"age_ms"`
		State        string  `json:"state"`
	}
	out := make([]item, 0, len(comps))
	now := time.Now()
	for _, c := range comps {
		var age int64
		if !c.At.IsZero() {
			age = now.Sub(c.At).Milliseconds()
		}
		out = append(out, item{c.MemberID, c.EdgeDeviceID, c.Value, age, c.State})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}

// recordSourceFailure 落一条来源失败审计（(ruleID, result+reason) 5 分钟节流）。
//
// 与 recordUnresolvedSelector 的区别：键里带 result 码，因为同一条规则在不同阶段
// 可能先 resolver 未接线、后成员切换，两种证据都要留。
func (e *Evaluator) recordSourceFailure(ruleID, groupID uint, at time.Time, resultCode, reason string) {
	// 健康可见性先于审计节流：即使本条审计被 5 分钟去重压掉，健康回调也必须收到
	// （Lead 2026-10-22 e2e TestSG_E3：组已不可用但 source_group_health 0 行）。
	notifySourceHealth(e.sourceHealthFn, groupID, resultCode, reason)
	if e.db == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	e.auditMu.Lock()
	if e.sourceAudit == nil {
		e.sourceAudit = make(map[sourceAuditKey]time.Time)
	}
	key := sourceAuditKey{ruleID: ruleID, result: resultCode, reason: reason}
	if last, seen := e.sourceAudit[key]; seen && at.Sub(last) < sourceAuditInterval {
		e.auditMu.Unlock()
		return
	}
	e.sourceAudit[key] = at
	e.auditMu.Unlock()

	ev := models.AutomationEvent{
		RuleID:      ruleID,
		TriggeredAt: at,
		Result:      resultCode,
		Detail:      "source resolution failed: " + reason,
		CreatedAt:   at,
	}
	if err := e.db.Create(&ev).Error; err != nil {
		logger.Warn("automation: failed to record source event",
			"rule_id", ruleID, "result", resultCode, "reason", reason, "error", err)
	}
}

// sourceAuditKey 是来源失败审计去重键。
type sourceAuditKey struct {
	ruleID uint
	result string
	reason string
}

// SetLatestValueBySensorFn 注入「按 (设备, 物理名) 取最新值」回调（复合组逐成员取值）。
//
// 生产实现是 api.LatestValueBySensor；与 planner 的同名注入点一致，保持 automation
// 对 api 的零编译期依赖。nil 时复合组按 StateNotConfigured fail-closed（可见）。
func (e *Evaluator) SetLatestValueBySensorFn(fn func(deviceID uint, sensorName string) (models.UnifiedData, bool)) {
	e.latestValueBySensorFn = fn
}

// sourceKindEq 比较规则的两列来源绑定是否等价（NULL 与显式 edge_device 等价 =
// 存量单设备语义）。LoadRules 用它判断"来源绑定是否变化"，变化即清窗。
func sourceKindEq(a, b *string) bool {
	return normalizeSourceKind(a) == normalizeSourceKind(b)
}

func normalizeSourceKind(v *string) string {
	if v == nil || *v == "" {
		return string(models.SourceKindEdgeDevice)
	}
	return *v
}

// uintPtrEq 比较两个可空 uint 列（NULL 与 0 等价）。
func uintPtrEq(a, b *uint) bool {
	av, bv := uint(0), uint(0)
	if a != nil {
		av = *a
	}
	if b != nil {
		bv = *b
	}
	return av == bv
}

