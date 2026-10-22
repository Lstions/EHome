package sourceref

import "time"

// Component 是单个成员在某次求值中的分量结论。
//
// 为什么要有「分量结论」这一层，而不是直接合并再比较：解析层的已知集合是
// **单设备**模型（sensorfield resolve.go 的 known = decl[type] ∪ obs[device]）。
// 把两台设备的帧拼成一个 frame 会产生同名互补 missing（missing 被 clamp 到 0），
// 一台掉线也不报缺员 ⇒ 静默错值。因此必须**逐分量求值再合并**（方案 D-9）。
type Component struct {
	MemberID     uint
	EdgeDeviceID uint
	Required     bool
	Value        float64
	At           time.Time
	// State 取 StateOK / StateMissing / StateStale / StateNotEligible。
	State string
}

// Result 是一次来源组求值的合并结果。
type Result struct {
	OK    bool
	Value float64
	// Degraded 表示「有成员缺席但按策略仍参与了求值」（仅 use_available）。
	// 消费方**必须**把它写进审计，否则用户以为「1+2 融合」其实只有一台在供数。
	Degraded      bool
	Reason        string
	Components    []Component
	Revision      int64
	TransitionSeq uint64
}

// EvalMemberFunc 由调用方实现：给一个成员，返回它的分量结论。
//
// 实现方的责任（方案 §7.1 E-1/E-2/E-3）：
//   - 取不到值 ⇒ StateMissing；
//   - 有值但年龄 > staleness ⇒ StateStale（**不得把旧值当有效值**）；
//   - 主备组成员状态 ∉ {active,standby} ⇒ StateNotEligible；
//   - composite 成员不看 status（E-3b）。
type EvalMemberFunc func(m MemberSnapshot) Component

// CombineGroup 是**纯函数**：给定不可变组快照 + 各分量结论，产出合并结果。
//
// 无锁、无 IO、无 goroutine、不调用 time.Now（时间由调用方经 Component.At 提供），
// 因此可被门禁 SG-18 静态断言（本文件不得出现 os/io/net/sync/gorm）。
//
// 语义（方案 §12.9.2 权威真值表）：
//
//	primary_backup：只看权威成员。无 active ⇒ unbound(no_active)；
//	  active 陈旧 ⇒ unbound(active_stale)；active 无值 ⇒ unbound(no_available_member)。
//	composite + fail_closed：任一分量非 OK ⇒ unbound（Required 不改变结果，仅保存期告警）。
//	composite + use_available：缺 required=true ⇒ unbound(required_missing)；
//	  可用数 < 生效 min_members ⇒ unbound(below_min_members)；
//	  否则按 combine 合并，Degraded = 有缺席。
func CombineGroup(g *GroupSnapshot, eval EvalMemberFunc) Result {
	if g == nil {
		return Result{Reason: ReasonGroupMissing}
	}
	res := Result{Revision: g.Revision, TransitionSeq: g.TransitionSeq}
	if !g.Enabled {
		res.Reason = ReasonGroupDisabled
		return res
	}
	if len(g.Members) == 0 {
		res.Reason = ReasonNoAvailableMember
		return res
	}

	comps := make([]Component, 0, len(g.Members))
	for _, m := range g.Members {
		comps = append(comps, normalizeComponent(m, eval))
	}
	res.Components = comps

	switch g.Kind {
	case KindPrimaryBackup:
		return combinePrimaryBackup(g, comps, res)
	case KindComposite:
		return combineComposite(g, comps, res)
	default:
		res.Reason = ReasonGroupMissing
		return res
	}
}

func normalizeComponent(m MemberSnapshot, eval EvalMemberFunc) Component {
	var c Component
	if eval != nil {
		c = eval(m)
	}
	// 防御：eval 忘记回填身份字段时以成员快照为准（防止串成员）。
	c.MemberID = m.MemberID
	c.EdgeDeviceID = m.EdgeDeviceID
	c.Required = m.Required
	if c.State == "" {
		c.State = StateMissing
	}
	return c
}

func combinePrimaryBackup(g *GroupSnapshot, comps []Component, res Result) Result {
	if g.ActiveMemberID == 0 {
		res.Reason = ReasonNoActive
		return res
	}
	for _, c := range comps {
		if c.MemberID != g.ActiveMemberID {
			continue
		}
		switch c.State {
		case StateOK:
			res.OK = true
			res.Value = c.Value
			return res
		case StateStale:
			res.Reason = ReasonActiveStale
			return res
		default:
			// missing / not_eligible
			res.Reason = ReasonNoAvailableMember
			return res
		}
	}
	// active 成员不在快照成员列表里（数据不一致）⇒ fail-closed
	res.Reason = ReasonNoActive
	return res
}

func combineComposite(g *GroupSnapshot, comps []Component, res Result) Result {
	onDegrade := g.NormalizedOnDegrade()
	avail := make([]Component, 0, len(comps))
	for _, c := range comps {
		if c.State == StateOK {
			avail = append(avail, c)
		}
	}

	if onDegrade == OnDegradeFailClosed {
		// 任一分量缺席即 unbound（Required 不改变结果；保存期会给 warning）。
		if len(avail) != len(comps) {
			res.Reason = ReasonNoAvailableMember
			return res
		}
	} else {
		// use_available：缺 required 成员直接 unbound（方案 F-2）。
		for _, c := range comps {
			if c.Required && c.State != StateOK {
				res.Reason = ReasonRequiredMissing
				return res
			}
		}
		minMembers := g.EffectiveMinMembers()
		if len(avail) < minMembers {
			res.Reason = ReasonBelowMinMembers
			return res
		}
		res.Degraded = len(avail) < len(comps)
	}

	value, ok := combineValues(g.Combine, avail)
	if !ok {
		res.Reason = ReasonNoAvailableMember
		return res
	}
	res.OK = true
	res.Value = value
	return res
}

// combineValues 按算子合并可用分量。avail 已按成员优先级排序（priority DESC）。
func combineValues(combine string, avail []Component) (float64, bool) {
	if len(avail) == 0 {
		return 0, false
	}
	switch combine {
	case CombineFirstAvailable:
		return avail[0].Value, true
	case CombineAvg:
		var sum float64
		for _, c := range avail {
			sum += c.Value
		}
		return sum / float64(len(avail)), true
	case CombineMax:
		v := avail[0].Value
		for _, c := range avail[1:] {
			if c.Value > v {
				v = c.Value
			}
		}
		return v, true
	case CombineMin:
		v := avail[0].Value
		for _, c := range avail[1:] {
			if c.Value < v {
				v = c.Value
			}
		}
		return v, true
	case CombineSum:
		var sum float64
		for _, c := range avail {
			sum += c.Value
		}
		return sum, true
	default:
		return 0, false
	}
}
