package sensorfield

import (
	"fmt"
	"sort"
	"strings"

	"ehome/backend/pkg/logger"
)

// 控制面专用原因码（Resolved.Reason / Verdict.Reason，均有界）。
const (
	reasonNoKnownMember   = "no_known_member"
	reasonUnknownSemantic = "unknown_semantic"
	reasonFamilyTruncated = "family_truncated"
)

// frozenAliasKey 是 (device, semantic) 冻结键（方案 §6.1 规则 4）。
type frozenAliasKey struct {
	deviceID uint
	semantic string
}

// aliasWarnKey 是“一次性通知”去重键。
type aliasWarnKey struct {
	deviceID uint
	semantic string
}

// semanticBinding 是一次语义解析的结果。
type semanticBinding struct {
	rule            *aliasRule
	physical        string   // 冻结/首次解析到的物理名；空表示当前无候选已知
	knownCandidates []string // 当前已知的候选（配置顺序）
	ambiguous       bool     // 多候选同时已知
	nonFirst        bool     // 命中的不是首候选
}

// nonNumericUnit 是“单位列里的值类型伪单位”兜底集合（迁移期）。
//
// 依据 docs/设计/数据语义规范.md §3.2/§4：ack/raw/state/bitmask 不是单位，
// 现网驱动仍有约 27 处技术债；obs 投影表暂无 value_type 列，故解析层兜底：
// unit 命中这四个值 ⇒ 视为非数值（数值聚合中按“本帧缺失”处理，fail-closed）。
func nonNumericUnit(unit string) bool {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "ack", "raw", "state", "bitmask":
		return true
	}
	return false
}

// fieldNumeric 报告字段是否参与数值比较。
//
// 口径（Lead 评审修正 + 数据语义规范 §4）：显式 value_type 优先（只有 number 参与，
// boolean/enum/bitmask/string/raw 视为缺失）；未知类型（兼容存量）再按伪单位兜底，
// 仍无法判定则按 number 处理。
func fieldNumeric(f Field) bool {
	if f.ValueType != TypeUnknown {
		return f.ValueType == TypeNumber
	}
	return !nonNumericUnit(f.Unit)
}

// knownSet 返回该设备的已知集合（声明 ∪ 观测，meta 做单位/类型丰富）。
// 控制面使用（Resolve/Compile），O(#fields)。
func (s *snapshot) knownSet(deviceID uint) map[string]Field {
	out := make(map[string]Field)
	if dt := s.deviceTypes[deviceID]; dt != "" {
		for _, f := range s.decls[dt] {
			f.Declared = true
			out[f.Name] = f
		}
	}
	for name, of := range s.obs[deviceID] {
		cur, ok := out[name]
		if !ok {
			of.Observed = true
			out[name] = of
			continue
		}
		cur.Observed = true
		if cur.Unit == "" {
			cur.Unit = of.Unit
		}
		if cur.ValueType == TypeUnknown {
			cur.ValueType = of.ValueType
		}
		if of.LastSeen.After(cur.LastSeen) {
			cur.LastSeen = of.LastSeen
		}
		if of.Samples > cur.Samples {
			cur.Samples = of.Samples
		}
		out[name] = cur
	}
	for name, cur := range out {
		if mf, ok := s.meta[name]; ok {
			if mf.Unit != "" {
				cur.Unit = mf.Unit
			}
			if mf.ValueType != TypeUnknown {
				cur.ValueType = mf.ValueType
			}
			out[name] = cur
		}
	}
	return out
}

// knownFields 返回该设备已知字段的确定性列表（声明顺序在前，观测增量按名排序）。
// 返回副本，调用方可安全修改。
func (s *snapshot) knownFields(deviceID uint) []Field {
	known := s.knownSet(deviceID)
	declaredOrder := make([]string, 0, len(known))
	seen := make(map[string]struct{}, len(known))
	if dt := s.deviceTypes[deviceID]; dt != "" {
		for _, f := range s.decls[dt] {
			if _, dup := seen[f.Name]; dup {
				continue
			}
			seen[f.Name] = struct{}{}
			declaredOrder = append(declaredOrder, f.Name)
		}
	}
	rest := make([]string, 0, len(known))
	for name := range known {
		if _, ok := seen[name]; ok {
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	declaredOrder = append(declaredOrder, rest...)
	out := make([]Field, 0, len(declaredOrder))
	for _, name := range declaredOrder {
		out = append(out, known[name])
	}
	return out
}

// hasKnownField 报告 (device, name) 是否在声明∪观测里出现（meta 单独不算 known）。
func (s *snapshot) hasKnownField(deviceID uint, name string) bool {
	if dt := s.deviceTypes[deviceID]; dt != "" {
		for i := range s.decls[dt] {
			if s.decls[dt][i].Name == name {
				return true
			}
		}
	}
	_, ok := s.obs[deviceID][name]
	return ok
}

// knownField 返回单个已知字段（声明∪观测，meta 丰富）；不存在返回 false。
func (s *snapshot) knownField(deviceID uint, name string) (Field, bool) {
	var out Field
	found := false
	if dt := s.deviceTypes[deviceID]; dt != "" {
		for i := range s.decls[dt] {
			if s.decls[dt][i].Name == name {
				out = s.decls[dt][i]
				out.Declared = true
				found = true
				break
			}
		}
	}
	if of, ok := s.obs[deviceID][name]; ok {
		if !found {
			out = of
		} else {
			if out.Unit == "" {
				out.Unit = of.Unit
			}
			if out.ValueType == TypeUnknown {
				out.ValueType = of.ValueType
			}
			if of.LastSeen.After(out.LastSeen) {
				out.LastSeen = of.LastSeen
			}
			if of.Samples > out.Samples {
				out.Samples = of.Samples
			}
		}
		out.Observed = true
		found = true
	}
	if !found {
		return Field{}, false
	}
	if mf, ok := s.meta[name]; ok {
		if mf.Unit != "" {
			out.Unit = mf.Unit
		}
		if mf.ValueType != TypeUnknown {
			out.ValueType = mf.ValueType
		}
	}
	return out, true
}

// fieldNumeric 返回字段是否参与数值比较（meta 优先；未知字段按 number 处理）。
func (s *snapshot) fieldNumeric(deviceID uint, name string) bool {
	if f, ok := s.knownField(deviceID, name); ok {
		return fieldNumeric(f)
	}
	if mf, ok := s.meta[name]; ok {
		return fieldNumeric(mf)
	}
	return true
}

// expandFamilyNames 从已知集合展开族成员（严格 ^prefix_[0-9]+$）。
//
// 排序：数字后缀数值升序（_2 在 _10 前），同号按名字；超过 max 时保留前 max 个
// 并返回 truncated=true（确定性截断策略，方案 §13.1）。
func expandFamilyNames(known map[string]Field, prefix string, max int) ([]string, bool) {
	type member struct {
		name string
		idx  uint64
	}
	members := make([]member, 0, 8)
	for name := range known {
		if idx, ok := matchFamilyIndex(prefix, name); ok {
			members = append(members, member{name: name, idx: idx})
		}
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].idx != members[j].idx {
			return members[i].idx < members[j].idx
		}
		return members[i].name < members[j].name
	})
	truncated := false
	if len(members) > max {
		members = members[:max]
		truncated = true
	}
	names := make([]string, len(members))
	for i := range members {
		names[i] = members[i].name
	}
	return names, truncated
}

// expandFamily 是 expandFamilyNames 的快照便捷入口。
func (s *snapshot) expandFamily(deviceID uint, prefix string, max int) ([]string, bool) {
	return expandFamilyNames(s.knownSet(deviceID), prefix, max)
}

// bindSemantic 把语义名解析到物理名（方案 §5.4/§6.1 规则 4）。
//
// 规则：
//   - 别名未定义 ⇒ ErrUnknownSemantic（控制面 400）
//   - 按候选配置顺序取第一个“该设备已知（声明∪观测）”的
//   - 首次解析成功后按 (device, semantic) 冻结；后续复用（防 UI 抖动/语义漂移）
//   - 冻结字段消失时重新解析并 WARN（fail-open + 可观测，不永久死锁）
//   - 非首候选/多候选：指标 + 一次性 WARN（触发前通知的域内落点）
func (r *Registry) bindSemantic(snap *snapshot, deviceID uint, semantic string) (semanticBinding, error) {
	rule := snap.aliases.lookup(semantic)
	if rule == nil {
		return semanticBinding{}, fmt.Errorf("%w: %q", ErrUnknownSemantic, semantic)
	}
	var knownCandidates []string
	for _, cand := range rule.candidates {
		if snap.hasKnownField(deviceID, cand) {
			knownCandidates = append(knownCandidates, cand)
		}
	}
	b := semanticBinding{rule: rule, knownCandidates: knownCandidates}
	key := frozenAliasKey{deviceID: deviceID, semantic: semantic}
	if v, ok := r.frozenAlias.Load(key); ok {
		frozen, _ := v.(string)
		if frozen != "" && snap.hasKnownField(deviceID, frozen) {
			b.physical = frozen
		} else if frozen != "" {
			r.frozenAlias.Delete(key)
			logger.Warn("sensorfield: frozen alias candidate disappeared, re-resolving",
				"device_id", deviceID, "semantic", semantic, "frozen", frozen, "known_candidates", knownCandidates)
		}
	}
	if b.physical == "" && len(knownCandidates) > 0 {
		b.physical = knownCandidates[0]
		r.frozenAlias.Store(key, b.physical)
	}
	if b.physical == "" {
		return b, nil
	}
	b.ambiguous = len(knownCandidates) > 1
	b.nonFirst = b.physical != rule.candidates[0]
	firstLabel := "true"
	if b.nonFirst {
		firstLabel = "false"
	}
	FieldAliasHitTotal.WithLabelValues(semantic, b.physical, firstLabel).Inc()
	if b.ambiguous {
		FieldAliasAmbiguousTotal.WithLabelValues(semantic).Inc()
	}
	if b.nonFirst || b.ambiguous {
		warnKey := aliasWarnKey{deviceID: deviceID, semantic: semantic}
		if _, loaded := r.aliasWarned.LoadOrStore(warnKey, struct{}{}); !loaded {
			logger.Warn("sensorfield: semantic resolved to non-first or ambiguous candidate (notify before trigger)",
				"device_id", deviceID, "semantic", semantic, "physical", b.physical, "known_candidates", knownCandidates)
		}
	}
	return b, nil
}

// Compile 把 selector 编译为不可变 Plan（方案 §7.2：缓存 miss 不查 DB）。
//
// 纪律：编译产物携带编译期 epoch（PlanEpoch 可读），调用方直接用 plan 的 epoch
// 做缓存键即可，无需“先 Compile 再读 Epoch”的竞态规避。
func (r *Registry) Compile(deviceID uint, selector string) (Plan, error) {
	ps, err := parseSelector(selector, r.cfg)
	if err != nil {
		return nil, err
	}
	if ps.kind != KindLiteral && !r.enabled.Load() {
		return nil, fmt.Errorf("%w: selector %q", ErrDisabled, ps.raw)
	}
	snap := r.snap.Load()
	if snap == nil {
		return nil, ErrNotReady
	}
	key := compileKey{deviceID: deviceID, selector: ps.raw}
	if plan, ok := r.lookupCompiled(key); ok {
		return plan, nil
	}
	plan, err := r.compileWithSnapshot(snap, deviceID, ps)
	if err != nil {
		return nil, err
	}
	r.storeCompiled(key, plan)
	return plan, nil
}

// compileWithSnapshot 是 Compile 的实际编译逻辑。
func (r *Registry) compileWithSnapshot(snap *snapshot, deviceID uint, ps parsedSelector) (Plan, error) {
	switch ps.kind {
	case KindLiteral:
		p := &literalPlan{
			selector: ps.raw,
			epoch:    snap.epoch,
			literal:  ps.literal,
			numeric:  snap.fieldNumeric(deviceID, ps.literal),
		}
		return p, nil
	case KindFamily:
		known := snap.knownSet(deviceID)
		names, truncated := expandFamilyNames(known, ps.prefix, r.cfg.MaxFamilySize)
		p := &familyPlan{
			selector:   ps.raw,
			epoch:      snap.epoch,
			prefix:     ps.prefix,
			agg:        ps.aggregate,
			known:      names,
			knownSet:   make(map[string]struct{}, len(names)),
			nonNumeric: make(map[string]struct{}),
			truncated:  truncated,
			enabled:    &r.enabled,
		}
		for _, name := range names {
			p.knownSet[name] = struct{}{}
			if !fieldNumeric(known[name]) {
				p.nonNumeric[name] = struct{}{}
			}
		}
		return p, nil
	case KindSemantic:
		b, err := r.bindSemantic(snap, deviceID, ps.semantic)
		if err != nil {
			return nil, err
		}
		p := &semanticPlan{
			selector: ps.raw,
			epoch:    snap.epoch,
			semantic: ps.semantic,
			physical: b.physical,
			numeric:  true,
			enabled:  &r.enabled,
		}
		if b.physical != "" {
			p.numeric = snap.fieldNumeric(deviceID, b.physical)
		}
		return p, nil
	case KindVirtual:
		node := snap.virtuals[ps.virtual]
		if node == nil {
			return nil, fmt.Errorf("%w: %q", ErrUnknownVirtual, ps.virtual)
		}
		root, err := r.bindVirtual(snap, deviceID, node)
		if err != nil {
			return nil, err
		}
		return &virtualPlan{
			selector: ps.raw,
			epoch:    snap.epoch,
			name:     ps.virtual,
			root:     root,
			enabled:  &r.enabled,
		}, nil
	}
	return nil, syntaxErr(ErrInvalidSyntax, ps.raw, "unreachable selector kind")
}

// bindVirtual 把设备无关的派生节点绑定到具体设备（语义参数→物理名、族参数→已知成员）。
func (r *Registry) bindVirtual(snap *snapshot, deviceID uint, node *virtualNode) (*boundVirtual, error) {
	if node == nil {
		return nil, fmt.Errorf("%w: nil virtual node", ErrVirtualInvalid)
	}
	known := snap.knownSet(deviceID)
	b := &boundVirtual{node: node}
	if node.spec.FamilyArg {
		names, truncated := expandFamilyNames(known, node.family, r.cfg.MaxFamilySize)
		f := &boundVirtualFamily{
			prefix:     node.family,
			known:      names,
			knownSet:   make(map[string]struct{}, len(names)),
			nonNumeric: make(map[string]struct{}),
			truncated:  truncated,
		}
		for _, name := range names {
			f.knownSet[name] = struct{}{}
			if !fieldNumeric(known[name]) {
				f.nonNumeric[name] = struct{}{}
			}
		}
		b.family = f
	}
	for i := range node.scalars {
		arg := node.scalars[i]
		ba := boundVirtualArg{kind: arg.kind, numeric: true}
		switch arg.kind {
		case KindLiteral:
			ba.literalName = arg.literal
			if f, ok := known[arg.literal]; ok {
				ba.numeric = fieldNumeric(f)
			}
		case KindSemantic:
			bnd, err := r.bindSemantic(snap, deviceID, arg.semantic)
			if err != nil {
				return nil, err
			}
			ba.physical = bnd.physical
			ba.bound = bnd.physical != ""
			if bnd.physical != "" {
				ba.numeric = snap.fieldNumeric(deviceID, bnd.physical)
			}
		case KindVirtual:
			nested, err := r.bindVirtual(snap, deviceID, snap.virtuals[arg.virtual])
			if err != nil {
				return nil, err
			}
			ba.nested = nested
		}
		b.scalars = append(b.scalars, ba)
	}
	return b, nil
}

// Resolve 是控制面解析（纯内存，O(#fields)）：selector -> (物理字段集合, 聚合方式)。
//
// 语义（方案 §7.1/§8.2）：
//   - 语法错误/未知语义/未知派生/开关关闭 ⇒ 返回哨兵错误（HTTP 400）
//   - 语法合法但当前无匹配 ⇒ Status=unbound 且 err=nil（HTTP 200）
func (r *Registry) Resolve(deviceID uint, selector string) (Resolved, error) {
	ps, err := parseSelector(selector, r.cfg)
	if err != nil {
		FieldResolveTotal.WithLabelValues("error").Inc()
		return Resolved{}, err
	}
	if ps.kind != KindLiteral && !r.enabled.Load() {
		FieldResolveTotal.WithLabelValues("error").Inc()
		return Resolved{}, fmt.Errorf("%w: selector %q", ErrDisabled, ps.raw)
	}
	snap := r.snap.Load()
	if snap == nil {
		FieldResolveTotal.WithLabelValues("error").Inc()
		return Resolved{}, ErrNotReady
	}
	res := Resolved{Selector: ps.raw, Kind: ps.kind}
	switch ps.kind {
	case KindLiteral:
		if snap.hasKnownField(deviceID, ps.literal) {
			res.Status = StatusResolved
			res.Matched = []string{ps.literal}
		} else {
			res.Status = StatusUnbound
			res.Reason = reasonMissing
		}
	case KindFamily:
		res.Aggregate = ps.aggregate
		names, truncated := snap.expandFamily(deviceID, ps.prefix, r.cfg.MaxFamilySize)
		if len(names) == 0 {
			res.Status = StatusUnbound
			res.Reason = reasonNoKnownMember
			break
		}
		res.Status = StatusResolved
		res.Matched = names
		if truncated {
			res.Reason = reasonFamilyTruncated
		}
	case KindSemantic:
		b, err := r.bindSemantic(snap, deviceID, ps.semantic)
		if err != nil {
			FieldResolveTotal.WithLabelValues("error").Inc()
			return Resolved{}, err
		}
		if b.physical == "" {
			res.Status = StatusUnbound
			res.Reason = reasonNoKnownCandidate
			break
		}
		res.Status = StatusResolved
		res.Matched = []string{b.physical}
		res.Ambiguous = b.ambiguous
		if b.nonFirst {
			res.Reason = "non_first_candidate"
		}
	case KindVirtual:
		node := snap.virtuals[ps.virtual]
		if node == nil {
			FieldResolveTotal.WithLabelValues("error").Inc()
			return Resolved{}, fmt.Errorf("%w: %q", ErrUnknownVirtual, ps.virtual)
		}
		deps, reason := r.staticVirtualDeps(snap, deviceID, node, map[string]struct{}{})
		if reason != "" {
			res.Status = StatusUnbound
			res.Reason = reason
			break
		}
		res.Status = StatusResolved
		res.Matched = deps
	}
	if res.Status == StatusResolved {
		FieldResolveTotal.WithLabelValues("resolved").Inc()
	} else {
		FieldResolveTotal.WithLabelValues("unbound").Inc()
	}
	return res, nil
}

// staticVirtualDeps 静态推导派生的物理依赖（控制面展示用）。
// 返回 (物理名列表, 原因码)；原因非空表示当前有参数不可解析（status=unbound）。
func (r *Registry) staticVirtualDeps(snap *snapshot, deviceID uint, node *virtualNode, visited map[string]struct{}) ([]string, string) {
	set := map[string]struct{}{}
	if node.spec.FamilyArg {
		names, _ := snap.expandFamily(deviceID, node.family, r.cfg.MaxFamilySize)
		if len(names) == 0 {
			return nil, reasonNoKnownMember
		}
		for _, name := range names {
			set[name] = struct{}{}
		}
	}
	for i := range node.scalars {
		arg := node.scalars[i]
		switch arg.kind {
		case KindLiteral:
			if !snap.hasKnownField(deviceID, arg.literal) {
				return nil, reasonMissing
			}
			set[arg.literal] = struct{}{}
		case KindSemantic:
			b, err := r.bindSemantic(snap, deviceID, arg.semantic)
			if err != nil {
				return nil, reasonUnknownSemantic
			}
			if b.physical == "" {
				return nil, reasonNoKnownCandidate
			}
			set[b.physical] = struct{}{}
		case KindVirtual:
			if _, seen := visited[arg.virtual]; seen {
				return nil, reasonMissing // 环已在 SetVirtuals 拒绝，防御性返回
			}
			visited[arg.virtual] = struct{}{}
			dep, reason := r.staticVirtualDeps(snap, deviceID, snap.virtuals[arg.virtual], visited)
			if reason != "" {
				return nil, reason
			}
			for _, name := range dep {
				set[name] = struct{}{}
			}
		}
	}
	deps := make([]string, 0, len(set))
	for name := range set {
		deps = append(deps, name)
	}
	sort.Strings(deps)
	return deps, ""
}
