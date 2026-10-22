package alert

// 告警引擎的「数据来源」接入（方案《规则数据来源与复合来源》§5 / §12.2）。
//
// 与 automation 侧同构、共用 sourceref 快照（契约禁止两份解析实现），差异：
//   - 告警没有 result 列（AlertEvent 只有 state），来源失败只记日志/指标；
//   - firing 是布尔状态，来源切换时必须**关闭陈旧 firing**（S4，复用 onResolve 的
//     resolveUpdatedFiring 同款语义：就地置 resolved、不发"已恢复"通知）；
//   - 事件列只写来源快照（source_kind/group_id/edge_device_id/members_json）。
//
// 红线：sourceref 只通过接口注入（nil=未接线 ⇒ 组规则 fail-closed 并 Warn），
// 存量单设备/逻辑设备规则行为不变（G-1）。

import (
	"encoding/json"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/logger"
)

// SourceResolver 是 sourceref.Registry 在本包的最小依赖（与 automation 同款接口，
// 两边都不得各自查组表：契约 SG-18 唯一解析器）。
type SourceResolver interface {
	MatchFrame(edgeDeviceID uint, kind string, groupID uint) sourceref.FrameMatch
	Group(id uint) (*sourceref.GroupSnapshot, bool)
}

// SetSourceResolver 注入来源组解析器（nil = 未接线，组规则 fail-closed）。
func (e *Evaluator) SetSourceResolver(r SourceResolver) { e.sourceResolver = r }

// SetDeviceIntervalFn 注入设备采集间隔（S8 gap 上限 = 2×间隔；nil/0 ⇒ 不启用）。
func (e *Evaluator) SetDeviceIntervalFn(fn func(edgeDeviceID uint) time.Duration) {
	e.deviceIntervalFn = fn
}

// SetLatestValueBySensorFn 注入「按 (设备, 物理名) 取最新值」回调（复合组逐成员取值）。
func (e *Evaluator) SetLatestValueBySensorFn(fn func(deviceID uint, sensorName string) (models.UnifiedData, bool)) {
	e.latestValueBySensorFn = fn
}

// OnSourceTransition 是来源组切换的引擎侧入口：清窗口 + 关闭陈旧 firing（S4）。
//
// 快照正确性不依赖它（S3）：钩子丢失只影响清窗时延；下一次求值 identity 不匹配
// 也会清窗（S1）。清窗**保留** e.firing？不：告警的 firing 是"当前条件成立"状态，
// 来源切换后旧触发成员已不代表当前来源 ⇒ 必须关闭（置 resolved、不发恢复通知），
// 否则会永久停在 firing（陈旧假警，与规则内容变更同款处理）。
func (e *Evaluator) OnSourceTransition(groupID uint) {
	e.mu.Lock()
	ids := e.rulesByGroup[groupID]
	for _, id := range ids {
		delete(e.windows, id)
	}
	var closeIDs []uint
	for _, id := range ids {
		if e.firing[id] {
			closeIDs = append(closeIDs, id)
			delete(e.firing, id)
			delete(e.lastNotifyAt, id)
		}
	}
	e.mu.Unlock()
	for _, id := range closeIDs {
		if rule, ok := e.ruleByID(id); ok {
			e.resolveUpdatedFiring(rule)
		}
	}
	if len(ids) > 0 {
		logger.Info("alert: source transition cleared windows",
			"group_id", groupID, "rules", len(ids), "closed_firing", len(closeIDs))
	}
}

// ruleByID 读规则快照（Lock 内只做浅拷贝，避免调用方在锁内做 DB 写）。
func (e *Evaluator) ruleByID(id uint) (models.AlertRule, bool) {
	e.mu.RLock()
	r, ok := e.rules[id]
	e.mu.RUnlock()
	return r, ok
}

// ── 窗口来源身份（S1/S2）───────────────────────────────────────────────────

// sourceIdentity 与 automation 同构但**独立定义**（两包各自持有私有副本，避免
// alert→automation 编译期依赖；判据本身必须一致，见共享用例表）。
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

// identityForFrame 按 kind 分流：主备含触发成员，复合只含组身份（S7 的前提）。
func identityForFrame(m sourceref.FrameMatch, edgeDeviceID uint) sourceIdentity {
	if m.Kind == sourceref.KindComposite {
		return sourceIdentity{kind: identityComposite, groupID: m.GroupID, revision: m.Revision,
			membersHash: membersHashOf(m.Group)}
	}
	return sourceIdentity{kind: identityBackup, groupID: m.GroupID, edgeDeviceID: edgeDeviceID,
		revision: m.Revision, membersHash: membersHashOf(m.Group)}
}

func membersHashOf(g *sourceref.GroupSnapshot) string {
	if g == nil {
		return ""
	}
	return g.MembersHash
}

// sourceGap / groupGap：S8 的 gap 上限（2×采集间隔；取不到 ⇒ 0=不启用）。
func (e *Evaluator) sourceGap(edgeDeviceID uint) time.Duration {
	if e.deviceIntervalFn == nil {
		return 0
	}
	if d := e.deviceIntervalFn(edgeDeviceID); d > 0 {
		return 2 * d
	}
	return 0
}

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

// alertSourceEvidence 是写进 AlertEvent 的来源快照。
type alertSourceEvidence struct {
	kind         string
	groupID      uint
	edgeDeviceID uint
	membersJSON  string
}

// apply 把来源快照写进事件行（NULL = 存量事件；方案 §4.4）。
func (s *alertSourceEvidence) apply(ev *models.AlertEvent) {
	if s == nil || ev == nil {
		return
	}
	if s.kind == "" && s.groupID == 0 && s.edgeDeviceID == 0 {
		return
	}
	kind := s.kind
	if kind == "" {
		kind = string(models.SourceKindEdgeDevice)
	}
	ev.SourceKind = &kind
	if s.groupID != 0 {
		gid := s.groupID
		ev.SourceGroupID = &gid
	}
	if s.edgeDeviceID != 0 {
		eid := s.edgeDeviceID
		ev.SourceEdgeDeviceID = &eid
	}
	if s.membersJSON != "" {
		ev.MembersJSON = s.membersJSON
	}
}

// matchSourceRule 判断该帧是否驱动"绑来源组"的告警规则（按 kind 分流）。
//
// 返回 ok=false 时：组缺失/未启用/未接线 ⇒ 已记 Warn（fail-closed，不更新窗口）；
// 主备组的备成员帧 ⇒ 静默丢弃（S1 前提）。存量规则不经过本函数。
func (e *Evaluator) matchSourceRule(rule models.AlertRule, ref models.SourceRef,
	edgeDeviceID uint, at time.Time) (sourceref.FrameMatch, bool) {
	if e.sourceResolver == nil {
		logger.Warn("alert: source resolver not configured, group rule skipped (fail-closed)",
			"rule_id", rule.ID, "group_id", ref.GroupID)
		return sourceref.FrameMatch{}, false
	}
	m := e.sourceResolver.MatchFrame(edgeDeviceID, string(ref.Kind), ref.GroupID)
	if !m.Configured {
		logger.Warn("alert: source group unavailable, rule skipped (fail-closed)",
			"rule_id", rule.ID, "group_id", ref.GroupID, "reason", m.Reason)
		return m, false
	}
	if !m.Accepts() {
		return m, false
	}
	return m, true
}

// evalComposite 处理 composite 的"任一成员帧驱动整组求值"（逐分量求值 + CombineGroup）。
//
// 返回值与 evalRule 的单帧路径对齐：(ok, satisfied, value)。
// 失败（unbound/降级不允许/特殊 selector）⇒ ok=false，且必须可见（Warn）。
func (e *Evaluator) evalComposite(rule models.AlertRule, m sourceref.FrameMatch,
	at time.Time) (bool, bool, float64, *alertSourceEvidence) {
	g := m.Group
	if g == nil {
		logger.Warn("alert: composite group snapshot missing", "rule_id", rule.ID)
		return false, false, 0, nil
	}
	if isSpecialSelector(rule.SensorName) {
		logger.Warn("alert: composite with special selector unsupported (fail-closed)",
			"rule_id", rule.ID, "sensor", rule.SensorName)
		return false, false, 0, nil
	}
	res := sourceref.CombineGroup(g, func(ms sourceref.MemberSnapshot) sourceref.Component {
		if e.latestValueBySensorFn == nil {
			return sourceref.Component{State: sourceref.StateNotConfigured}
		}
		rec, ok := e.latestValueBySensorFn(ms.EdgeDeviceID, rule.SensorName)
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
		logger.Warn("alert: composite source unbound (fail-closed)",
			"rule_id", rule.ID, "reason", res.Reason,
			"revision", res.Revision, "transition_seq", res.TransitionSeq)
		return false, false, 0, nil
	}
	if res.Degraded {
		// use_available：允许告警但必须可见（方案 §7.1 E-5 / 12.9.2）。
		logger.Warn("alert: composite source degraded (use_available)",
			"rule_id", rule.ID, "reason", res.Reason,
			"revision", res.Revision, "transition_seq", res.TransitionSeq)
	}
	ev := &alertSourceEvidence{
		kind:         sourceref.GroupKindSourceGroup,
		groupID:      g.ID,
		edgeDeviceID: g.ActiveEdgeDeviceID(), // composite 无 active ⇒ 0；写 0 表示"组值"
		membersJSON:  alertComponentsJSON(res.Components),
	}
	return true, compare(rule.Comparator, res.Value, rule.Threshold), res.Value, ev
}

// alertComponentsJSON 序列化复合分量快照（alert_events.members_json）。
func alertComponentsJSON(comps []sourceref.Component) string {
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

// sourceKindEq / uintPtrEq 是 LoadRules 内容白名单用的可空列比较
// （NULL 与显式 edge_device/0 等价 = 存量语义），两引擎各自持有私有副本。
func sourceKindEq(a, b *string) bool {
	norm := func(v *string) string {
		if v == nil || *v == "" {
			return string(models.SourceKindEdgeDevice)
		}
		return *v
	}
	return norm(a) == norm(b)
}

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
