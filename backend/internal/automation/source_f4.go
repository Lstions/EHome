package automation

// Planner 侧的来源证据处理：F4 身份校验（FC-1…FC-5）、事件审计列、pending 来源快照。
//
// 方案依据：《规则数据来源与复合来源》§7.2（F4 复核期）、§4.4（审计列）、D-8（source_hash）。
// 核心红线（Lead 裁决 4 / 方案 F-9）：
//   - F4 必须钉住**触发时成员**（TriggerEvent.SourceEdgeDeviceID），不得回读规则配置列，
//     也不得用"当前 active 的最新值"为旧决策背书（fail-open）；
//   - 组规则在解析器未接线 / 组缺失 / 成员已切走 / 证据过期时一律 fail-closed + 审计；
//   - 存量单设备规则（source_kind IS NULL）除 F4 口径修复外行为不变（G-1 / D-6）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
)

// applySourceColumns 把触发证据写进事件行（方案 §4.4 审计列）。
//
// 存量规则（无来源证据）保持四列 NULL —— 迁移期历史事件与新事件可用
// source_kind IS NULL 区分，前端/盘点脚本无需猜测。
func applySourceColumns(ev *models.AutomationEvent, src *TriggerEvent) {
	if ev == nil || src == nil {
		return
	}
	if src.SourceKind == "" && src.SourceGroupID == 0 && src.SourceEdgeDeviceID == 0 {
		return
	}
	kind := src.SourceKind
	if kind == "" {
		kind = string(models.SourceKindEdgeDevice)
	}
	ev.SourceKind = &kind
	if src.SourceGroupID != 0 {
		gid := src.SourceGroupID
		ev.SourceGroupID = &gid
	}
	if src.SourceEdgeDeviceID != 0 {
		eid := src.SourceEdgeDeviceID
		ev.SourceEdgeDeviceID = &eid
	}
	if src.membersJSON != "" {
		ev.MembersJSON = src.membersJSON
	}
}

// checkConditionsStillSatisfiedWithSource 是 F4 的**生产入口**（planner.HandleTrigger 调用）。
//
// 返回 (result, reason, ok)：ok=true 表示可以执行；ok=false 时 result 已按失败原因
// 分类（"" = condition_changed，条件本身不满足），调用方据 result 落事件。
//
// fail-closed 判据（方案 §7.2 权威版）：
//
//	FC-1 触发成员已非 active（主备组切走）⇒ source_member_changed
//	FC-2 触发证据过期（now-SourceAt > staleness）⇒ source_stale
//	FC-3 值取不到 / unbound / kill switch ⇒ unresolved_selector / field_resolver_disabled（既有）
//	FC-4 解析器未接线 ⇒ unresolved_selector（detail 保留 f4_fail_reason=source_resolver_not_configured）
//	FC-5 组/成员行在 F4 时已不存在 ⇒ source_member_changed（f4_fail_reason=member_row_missing）
func (p *Planner) checkConditionsStillSatisfiedWithSource(rule models.AutomationRule, ev TriggerEvent) (string, string, bool) {
	// 未接线（旧构造/单测）：跳过复核，保持既有行为（G-1）。
	if p.latestValueFn == nil && p.latestValueBySensorFn == nil &&
		p.latestValuesFn == nil && p.planProvider == nil && p.sourceResolver == nil {
		return "", "", true
	}

	at := ev.At
	if at.IsZero() {
		at = p.nowFn()
	}

	ref := rule.SourceRef()
	deviceID := ev.SourceEdgeDeviceID
	var group *sourceref.GroupSnapshot
	// F4 失败同样意味着"该组来源已不可用于本决策"，必须让运维可见
	// （方案 §7.1 E-1/E-2；e2e TestSG_E3 缺口：审计有、健康表没有）。
	health := func(code, reason string) (string, string, bool) {
		notifySourceHealth(p.sourceHealthFn, ref.GroupID, code, reason)
		return code, reason, false
	}

	if ref.IsGroup() {
		// FC-4：未接线必须可见地 fail-closed（不能静默跳过组规则）。
		if p.sourceResolver == nil {
			return health(ResultUnresolvedSelector, "f4_fail_reason=source_resolver_not_configured")
		}
		g, ok := p.sourceResolver.Group(ref.GroupID)
		if !ok || g == nil {
			// FC-5：组行已不存在。
			return health(ResultSourceMemberChanged, "f4_fail_reason=group_missing")
		}
		if !g.Enabled {
			return health(ResultSourceMemberChanged, "f4_fail_reason=group_disabled")
		}
		if ev.SourceGroupID != 0 && ev.SourceGroupID != ref.GroupID {
			return health(ResultSourceMemberChanged, "f4_fail_reason=group_changed")
		}
		// F-9：证据缺失是**显式失败**，不得回落到配置列（把漏填从静默变显式）。
		if deviceID == 0 {
			return health(ResultSourceMemberChanged, "f4_fail_reason=source_edge_device_missing")
		}
		member, found := g.MemberOf(deviceID)
		if !found {
			// FC-5：成员行已不存在（组被改绑/成员被删）。
			return health(ResultSourceMemberChanged, "f4_fail_reason=member_row_missing")
		}
		// FC-1：主备组只认 active 成员；触发后切走 ⇒ 拒绝执行（不取新成员的值）。
		if g.Kind == sourceref.KindPrimaryBackup && g.ActiveMemberID != member.MemberID {
			return health(ResultSourceMemberChanged, "f4_fail_reason=member_changed")
		}
		// FC-2：证据过期 ⇒ 拒绝（陈旧数据为旧决策背书是 fail-open 方向）。
		if !ev.SourceAt.IsZero() && at.Sub(ev.SourceAt) > g.Staleness() {
			return health(ResultSourceStale, fmt.Sprintf("f4_fail_reason=source_stale age=%s",
				at.Sub(ev.SourceAt).Round(time.Millisecond)))
		}
		group = g
	} else {
		// 存量兼容：生产路径 evaluator 已填 SourceEdgeDeviceID；直接调用（单测/旧路径）
		// 未带证据时回落到配置列，保持 G-1 零回归。
		if deviceID == 0 {
			deviceID = rule.TriggerEdgeDeviceID
		}
		if rule.TriggerEdgeDeviceID == 0 && deviceID == 0 {
			return "", "", true
		}
	}

	conds, err := rule.ParseConditions()
	if err != nil {
		return "", fmt.Sprintf("invalid conditions_json: %v", err), false
	}

	// Trigger 条件复核（仅 sensor_threshold；time_window 由 evaluator 保证）。
	if rule.TriggerType == models.AutomationTriggerSensorThreshold {
		if group != nil && group.Kind == sourceref.KindComposite {
			// 复合组：重新按整组最新值合并（不是取触发成员单值）。
			res, cerr := p.combineGroupLatest(group, rule.TriggerSensorName, at)
			if cerr != nil {
				return health(ResultSourceDegraded, "f4_fail_reason="+cerr.Error())
			}
			if !res.OK {
				return health(ResultSourceDegraded, "f4_fail_reason="+res.Reason)
			}
			if !compare(rule.TriggerComparator, res.Value, rule.TriggerThreshold) {
				return "", fmt.Sprintf("trigger condition no longer satisfied: %s %.2f vs threshold %.2f",
					rule.TriggerComparator, res.Value, rule.TriggerThreshold), false
			}
		} else {
			out := p.evalSelectorLatest(deviceID, rule.TriggerSensorName, rule.TriggerComparator, rule.TriggerThreshold)
			if out.unresolved {
				return resolverResultCode(out.reason), out.reason, false
			}
			if !out.ok {
				return "", out.reason, false
			}
			if !out.satisfied {
				return "", fmt.Sprintf("trigger condition no longer satisfied: %s %.2f vs threshold %.2f",
					rule.TriggerComparator, out.value, rule.TriggerThreshold), false
			}
		}
	}

	// 附加条件复核（全部 AND）。
	for _, c := range conds {
		out := p.evalSelectorLatest(deviceID, c.SensorName, c.Comparator, c.Threshold)
		if out.unresolved {
			return resolverResultCode(out.reason), out.reason, false
		}
		if !out.ok {
			// 历史语义：literal 附加条件无法复核时跳过（不阻断执行）；但特殊 selector
			// 的"取不到"就是解析失败，必须 fail-closed（AU-12 的另一半）。
			if isSpecialSelector(c.SensorName) {
				return ResultUnresolvedSelector, out.reason, false
			}
			continue
		}
		if !out.satisfied {
			return "", fmt.Sprintf("condition %s %s %.2f no longer satisfied: latest=%.2f",
				c.SensorName, c.Comparator, c.Threshold, out.value), false
		}
	}
	return "", "", true
}

// combineGroupLatest 按组快照 + 逐成员最新值做一次 CombineGroup（F4 复核用）。
func (p *Planner) combineGroupLatest(g *sourceref.GroupSnapshot, selector string, at time.Time) (sourceref.Result, error) {
	if isSpecialSelector(selector) {
		return sourceref.Result{}, fmt.Errorf("composite_special_selector_unsupported")
	}
	if p.latestValueBySensorFn == nil {
		return sourceref.Result{}, fmt.Errorf("latest_value_by_sensor_not_configured")
	}
	res := sourceref.CombineGroup(g, func(ms sourceref.MemberSnapshot) sourceref.Component {
		rec, ok := p.latestValueBySensorFn(ms.EdgeDeviceID, selector)
		if !ok {
			return sourceref.Component{State: sourceref.StateMissing}
		}
		comp := sourceref.Component{Value: rec.Value, At: rec.Timestamp, State: sourceref.StateOK}
		if !rec.Timestamp.IsZero() && at.Sub(rec.Timestamp) > g.Staleness() {
			comp.State = sourceref.StateStale
		}
		return comp
	})
	return res, nil
}

// pendingSourceSnapshot 生成 pending_confirm 的来源快照（Y5 + D-8）。
//
// 哈希覆盖**组状态**（kind/group/revision/active_member/members_hash/transition_seq），
// 不含触发成员：触发成员作为独立列（source_edge_device_id）落库用于展示/归因；
// confirm 的判据就是"组状态没变"——主备切换/成员变更 ⇒ revision/members_hash 变化 ⇒ 拒绝。
func (p *Planner) pendingSourceSnapshot(rule models.AutomationRule, src *TriggerEvent) (string, string) {
	if src == nil || src.SourceKind == "" {
		return "", ""
	}
	evidence := map[string]any{
		"source_kind":           src.SourceKind,
		"source_group_id":       src.SourceGroupID,
		"source_edge_device_id": src.SourceEdgeDeviceID,
		"transition_seq":        src.TransitionSeq,
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d", src.SourceKind, src.SourceGroupID)
	if ref := rule.SourceRef(); ref.IsGroup() && p.sourceResolver != nil {
		if g, ok := p.sourceResolver.Group(ref.GroupID); ok && g != nil {
			fmt.Fprintf(h, "|%d|%d|%d|%s|%d", g.ID, g.Revision, g.ActiveMemberID, g.MembersHash, g.TransitionSeq)
			evidence["group_revision"] = g.Revision
			evidence["active_member_id"] = g.ActiveMemberID
			evidence["members_hash"] = g.MembersHash
		}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return hex.EncodeToString(h.Sum(nil)), ""
	}
	return hex.EncodeToString(h.Sum(nil)), string(raw)
}

// currentSourceSnapshot 计算 **当前**来源状态哈希（ConfirmEvent 比对用）。
//
// 与 pendingSourceSnapshot 必须用同一算法与同一前缀（kind|groupID|...），否则
// 正常情况也会稳定假阳性拒绝。
func (p *Planner) currentSourceSnapshot(rule models.AutomationRule) (string, bool) {
	ref := rule.SourceRef()
	if !ref.IsGroup() || p.sourceResolver == nil {
		return "", false
	}
	g, ok := p.sourceResolver.Group(ref.GroupID)
	if !ok || g == nil || !g.Enabled {
		return "", false
	}
	// 格式与 pendingSourceSnapshot 严格对齐：kind|group|group_id|revision|active|members|seq
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d", sourceref.GroupKindSourceGroup, ref.GroupID)
	fmt.Fprintf(h, "|%d|%d|%d|%s|%d", g.ID, g.Revision, g.ActiveMemberID, g.MembersHash, g.TransitionSeq)
	return hex.EncodeToString(h.Sum(nil)), true
}
