package sensorfield

import (
	"math"
	"sync/atomic"
)

// CmpOp 是比较符（与 internal/models 的 AlertComparator* 同值，避免两端漂移）。
//
// 为什么比较符在域包里：冻结的 Plan.Eval(frame) 没有比较符参数，
// 数学上无法实现 any（∃）与 all（∀）—— 它们必须与 cmp+threshold 一起求值。
// 因此新增 EvalPredicate 承接 §5.3 定义 4 的完整语义；Plan.Eval 仅用于
// literal/semantic/virtual 与 reducer 的“取值”路径（family any/all 返回 unbound）。
type CmpOp string

const (
	CmpGT  CmpOp = "gt"
	CmpGTE CmpOp = "gte"
	CmpLT  CmpOp = "lt"
	CmpLTE CmpOp = "lte"
	CmpEQ  CmpOp = "eq"
	CmpNEQ CmpOp = "neq"
)

// ValidCmpOp 报告 op 是否在比较符白名单内（未知比较符 fail-closed）。
func ValidCmpOp(op CmpOp) bool {
	switch op {
	case CmpGT, CmpGTE, CmpLT, CmpLTE, CmpEQ, CmpNEQ:
		return true
	}
	return false
}

// Compare 求值比较符；未知比较符返回 false（fail-closed，与既有 compare 一致）。
func Compare(op CmpOp, value, threshold float64) bool {
	switch op {
	case CmpGT:
		return value > threshold
	case CmpGTE:
		return value >= threshold
	case CmpLT:
		return value < threshold
	case CmpLTE:
		return value <= threshold
	case CmpEQ:
		return value == threshold
	case CmpNEQ:
		return value != threshold
	default:
		return false
	}
}

// Verdict 是带比较符的求值结论（数据面触发判定的唯一出口）。
type Verdict struct {
	OK        bool    // false = unbound（不触发，Reason 说明原因）
	Satisfied bool    // OK=true 时：比较结论
	Value     float64 // 参与比较的值（any/all 时为第一个有效成员值；reducer 为归约值）
	Reason    string  // OK=false 时的原因码（有界字符串，可直接做指标 label）
}

// PredicatePlan 是可做比较符求值的编译产物。所有内置 Plan 都实现它；
// EvalPredicate 对其它 Plan 实现退化为 Eval + Compare（仍 fail-closed）。
type PredicatePlan interface {
	Plan
	EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict
}

// EpochPlan 是携带编译期快照 epoch 的 Plan（冻结契约之外的只读扩展）。
//
// 为什么需要：调用方“先 Compile 再读 Registry.Epoch()”存在竞态——可能拿到
// S1 编译的 plan 却以 S2 为缓存键，谓词把新发现的字段永久挡在 plan 外（fail-open 方向）。
// 有了 Epoch()，缓存键直接取 plan 自身的编译 epoch，无竞态。
type EpochPlan interface {
	Plan
	Epoch() uint64
}

// PlanEpoch 返回 plan 的编译期 epoch；未知实现返回 0（调用方按 0 处理即保守缓存）。
func PlanEpoch(p Plan) uint64 {
	if ep, ok := p.(EpochPlan); ok {
		return ep.Epoch()
	}
	return 0
}

// EvalPredicate 对任意 Plan 按比较符求值（数据面唯一推荐入口）。
//
// 语义（方案 §5.3 定义 4）：
//
//	literal/semantic/virtual: OK=找到有效值，Satisfied=Compare(op, Value, threshold)
//	family any:   ∃ f∈F_now : cmp(v(f), t)；空集 unbound；不受缺员影响
//	family all:   ∀ f∈F_now : cmp(v(f), t)；空集 unbound（不采用空真）；缺员/非数值 unbound
//	family min/max/avg/sum: 先归约（缺员即 unbound），再 Compare
func EvalPredicate(p Plan, frame []Reading, op CmpOp, threshold float64) Verdict {
	if p == nil {
		return Verdict{Reason: reasonNotReady}
	}
	if pp, ok := p.(PredicatePlan); ok {
		return pp.EvalPredicate(frame, op, threshold)
	}
	r := p.Eval(frame)
	if !r.OK {
		return Verdict{Reason: r.Reason}
	}
	if !ValidCmpOp(op) {
		return Verdict{Reason: reasonInvalidComparator}
	}
	return Verdict{OK: true, Satisfied: Compare(op, r.Value, threshold), Value: r.Value}
}

// 有界原因码（可直接做 Prometheus label，禁止拼接字段名/用户输入）。
const (
	reasonMissing                  = "missing"
	reasonNonNumeric               = "non_numeric"
	reasonNonFinite                = "non_finite"
	reasonEmptyFrame               = "empty_frame"
	reasonPartialFrame             = "partial_frame"
	reasonNan                      = "nan"
	reasonInf                      = "inf"
	reasonDiv0                     = "div0"
	reasonNegativeDenominator      = "negative_denominator"
	reasonDisabled                 = "disabled"
	reasonInvalidComparator        = "invalid_comparator"
	reasonAggregateNeedsComparator = "aggregate_requires_comparator"
	reasonNoKnownCandidate         = "no_known_candidate"
	reasonNotReady                 = "not_ready"
	reasonUnknownVirtual           = "unknown_virtual"
)

// isFiniteValue 报告 v 是否为有限数（定义 1：只有有限数算“有值”）。
func isFiniteValue(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// finiteValue 把非有限输出转成原因码（方案 §6.2 统一不变量 2）。
func finiteValue(v float64) (float64, string) {
	if math.IsNaN(v) {
		return 0, reasonNan
	}
	if math.IsInf(v, 0) {
		return 0, reasonInf
	}
	return v, ""
}

// findReading 返回 frame 中第一个同名读数的值（与历史 matchField 逐字节等价：
// 取第一个同名，不聚合、不跳过）。
func findReading(frame []Reading, name string) (float64, bool) {
	for i := range frame {
		if frame[i].Name == name {
			return frame[i].Value, true
		}
	}
	return 0, false
}

// familyScan 是族成员的一次遍历结果（口径见方案 §5.3 定义 1/2/3）。
type familyScan struct {
	count          int     // F_now 大小（有效、已知、数值成员数）
	sum            float64 // 有效成员求和
	min            float64 // 有效成员最小值（count=0 时为 +Inf）
	max            float64 // 有效成员最大值（count=0 时为 -Inf）
	first          float64 // 第一个有效成员值（审计展示用）
	foundKnown     int     // 已知成员中“有效出现”的个数
	nonFiniteKnown int     // 已知成员出现但值非有限的个数
	missing        int     // 已知成员缺失数 = len(known) - foundKnown（clamp ≥0）
}

// scanMembers 遍历一帧，按“已知集合 ∩ 本帧、数值类型、有限值”统计族成员。
//
// 关键口径（评审 B8/C1）：
//   - F_now ⊆ F_known：本帧出现、但不在已知集合里的家族字段**不计入**（已知集合是权威）
//   - 非数值类型（bitmask/enum/string/raw）视为缺失（定义 1）
//   - NaN/±Inf 视为缺失（定义 1）
//   - 缺员（已知有、本帧无效/缺失）⇒ missing>0，由调用方判 unbound
//
// 副作用：missing>0 时累加 ehome_field_partial_frame_total{family}（§10 口径）。
func scanMembers(prefix string, known []string, knownSet map[string]struct{}, nonNumeric map[string]struct{}, frame []Reading) familyScan {
	s := familyScan{min: math.Inf(1), max: math.Inf(-1)}
	for i := range frame {
		name := frame[i].Name
		if _, ok := matchFamilyIndex(prefix, name); !ok {
			continue
		}
		if _, knownMember := knownSet[name]; !knownMember {
			continue
		}
		if _, bad := nonNumeric[name]; bad {
			continue
		}
		v := frame[i].Value
		if !isFiniteValue(v) {
			s.nonFiniteKnown++
			continue
		}
		if s.count == 0 {
			s.first = v
		}
		s.count++
		s.sum += v
		if v < s.min {
			s.min = v
		}
		if v > s.max {
			s.max = v
		}
		s.foundKnown++
	}
	s.missing = len(known) - s.foundKnown
	if s.missing < 0 {
		s.missing = 0
	}
	if s.missing > 0 {
		FieldPartialFrameTotal.WithLabelValues(prefix).Inc()
	}
	return s
}

// familyUnboundReason 把一次 scan 的“为什么不能归约”翻译成有界原因码。
// missingByNonFinite 为 true 表示缺失全部由“出现但非有限”造成（审计可分辨）。
func familyUnboundReason(s familyScan) string {
	if s.missing > 0 {
		if s.nonFiniteKnown > 0 && s.nonFiniteKnown >= s.missing {
			return reasonNonFinite
		}
		return reasonPartialFrame
	}
	if s.count == 0 {
		return reasonEmptyFrame
	}
	return ""
}

// anyUnboundReason 是 any 的空集原因（any 不受缺员影响，只受“没有有效值”影响）。
func anyUnboundReason(s familyScan) string {
	if s.nonFiniteKnown > 0 {
		return reasonNonFinite
	}
	return reasonEmptyFrame
}

// reduceFamily 按聚合方式归约（调用方已保证 count>0 且 missing==0）。
func reduceFamily(agg Aggregate, s familyScan) (float64, string) {
	switch agg {
	case AggMin:
		return finiteValue(s.min)
	case AggMax:
		return finiteValue(s.max)
	case AggAvg:
		return finiteValue(s.sum / float64(s.count))
	case AggSum:
		return finiteValue(s.sum)
	}
	return 0, reasonAggregateNeedsComparator
}

// literalPlan 是 literal 选择器的编译产物。
//
// 热路径要求：Eval 零分配（for + 字符串比较，等价历史 matchField 语义）。
type literalPlan struct {
	selector string
	epoch    uint64
	literal  string
	numeric  bool // 已知类型是否参与数值比较（未知类型按数值处理）
}

func (p *literalPlan) Selector() string { return p.selector }
func (p *literalPlan) Kind() Kind       { return KindLiteral }
func (p *literalPlan) Epoch() uint64    { return p.epoch }

func (p *literalPlan) Eval(frame []Reading) EvalResult {
	if !p.numeric {
		return EvalResult{Reason: reasonNonNumeric}
	}
	for i := range frame {
		if frame[i].Name == p.literal {
			v := frame[i].Value
			if !isFiniteValue(v) {
				return EvalResult{Reason: reasonNonFinite}
			}
			return EvalResult{Value: v, OK: true}
		}
	}
	return EvalResult{Reason: reasonMissing}
}

func (p *literalPlan) EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict {
	if !ValidCmpOp(op) {
		return Verdict{Reason: reasonInvalidComparator}
	}
	r := p.Eval(frame)
	if !r.OK {
		return Verdict{Reason: r.Reason}
	}
	return Verdict{OK: true, Satisfied: Compare(op, r.Value, threshold), Value: r.Value}
}

// familyPlan 是 family 选择器的编译产物：绑定编译期已知成员集合。
type familyPlan struct {
	selector   string
	epoch      uint64
	prefix     string
	agg        Aggregate
	known      []string
	knownSet   map[string]struct{}
	nonNumeric map[string]struct{}
	truncated  bool
	enabled    *atomic.Bool
}

func (p *familyPlan) Selector() string { return p.selector }
func (p *familyPlan) Kind() Kind       { return KindFamily }
func (p *familyPlan) Epoch() uint64    { return p.epoch }

func (p *familyPlan) disabled() bool { return p.enabled != nil && !p.enabled.Load() }

func (p *familyPlan) scan(frame []Reading) familyScan {
	return scanMembers(p.prefix, p.known, p.knownSet, p.nonNumeric, frame)
}

// memberValue 返回 frame[i] 作为一个“有效族成员”的值（any/all 的比较用）。
func (p *familyPlan) memberValue(frame []Reading, i int) (float64, bool) {
	name := frame[i].Name
	if _, ok := matchFamilyIndex(p.prefix, name); !ok {
		return 0, false
	}
	if _, ok := p.knownSet[name]; !ok {
		return 0, false
	}
	if _, bad := p.nonNumeric[name]; bad {
		return 0, false
	}
	v := frame[i].Value
	if !isFiniteValue(v) {
		return 0, false
	}
	return v, true
}

// Eval 实现冻结契约。family 的 any/all 无法脱离比较符求值，返回 unbound +
// reasonAggregateNeedsComparator（fail-closed；调用方必须使用 EvalPredicate）。
func (p *familyPlan) Eval(frame []Reading) EvalResult {
	if p.disabled() {
		return EvalResult{Reason: reasonDisabled}
	}
	if p.agg == AggAny || p.agg == AggAll {
		return EvalResult{Reason: reasonAggregateNeedsComparator}
	}
	s := p.scan(frame)
	if reason := familyUnboundReason(s); reason != "" {
		return EvalResult{Reason: reason}
	}
	v, reason := reduceFamily(p.agg, s)
	if reason != "" {
		return EvalResult{Reason: reason}
	}
	return EvalResult{Value: v, OK: true}
}

func (p *familyPlan) EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict {
	if p.disabled() {
		return Verdict{Reason: reasonDisabled}
	}
	if !ValidCmpOp(op) {
		return Verdict{Reason: reasonInvalidComparator}
	}
	s := p.scan(frame)
	switch p.agg {
	case AggAny:
		if s.count == 0 {
			return Verdict{Reason: anyUnboundReason(s)}
		}
		for i := range frame {
			if v, ok := p.memberValue(frame, i); ok && Compare(op, v, threshold) {
				return Verdict{OK: true, Satisfied: true, Value: s.first}
			}
		}
		return Verdict{OK: true, Satisfied: false, Value: s.first}
	case AggAll:
		if reason := familyUnboundReason(s); reason != "" {
			return Verdict{Reason: reason}
		}
		for i := range frame {
			if v, ok := p.memberValue(frame, i); ok && !Compare(op, v, threshold) {
				return Verdict{OK: true, Satisfied: false, Value: s.first}
			}
		}
		return Verdict{OK: true, Satisfied: true, Value: s.first}
	default:
		if reason := familyUnboundReason(s); reason != "" {
			return Verdict{Reason: reason}
		}
		v, reason := reduceFamily(p.agg, s)
		if reason != "" {
			return Verdict{Reason: reason}
		}
		return Verdict{OK: true, Satisfied: Compare(op, v, threshold), Value: v}
	}
}

// semanticPlan 是 semantic 选择器的编译产物：绑定“该设备已知的第一候选”物理名。
type semanticPlan struct {
	selector string
	epoch    uint64
	semantic string
	physical string // 空 = 当前没有任何候选已知（求值 unbound，绝不 fallback）
	numeric  bool
	enabled  *atomic.Bool
}

func (p *semanticPlan) Selector() string { return p.selector }
func (p *semanticPlan) Kind() Kind       { return KindSemantic }
func (p *semanticPlan) Epoch() uint64    { return p.epoch }

func (p *semanticPlan) Eval(frame []Reading) EvalResult {
	if p.enabled != nil && !p.enabled.Load() {
		return EvalResult{Reason: reasonDisabled}
	}
	if p.physical == "" {
		return EvalResult{Reason: reasonNoKnownCandidate}
	}
	if !p.numeric {
		return EvalResult{Reason: reasonNonNumeric}
	}
	v, ok := findReading(frame, p.physical)
	if !ok {
		return EvalResult{Reason: reasonMissing}
	}
	if !isFiniteValue(v) {
		return EvalResult{Reason: reasonNonFinite}
	}
	return EvalResult{Value: v, OK: true}
}

func (p *semanticPlan) EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict {
	if !ValidCmpOp(op) {
		return Verdict{Reason: reasonInvalidComparator}
	}
	r := p.Eval(frame)
	if !r.OK {
		return Verdict{Reason: r.Reason}
	}
	return Verdict{OK: true, Satisfied: Compare(op, r.Value, threshold), Value: r.Value}
}

// virtualPlan 是 virtual 选择器的编译产物：绑定到设备的派生运行树。
type virtualPlan struct {
	selector string
	epoch    uint64
	name     string
	root     *boundVirtual
	enabled  *atomic.Bool
}

func (p *virtualPlan) Selector() string { return p.selector }
func (p *virtualPlan) Kind() Kind       { return KindVirtual }
func (p *virtualPlan) Epoch() uint64    { return p.epoch }

func (p *virtualPlan) Eval(frame []Reading) EvalResult {
	if p.enabled != nil && !p.enabled.Load() {
		return EvalResult{Reason: reasonDisabled}
	}
	v, reason := evalBoundVirtual(p.root, frame)
	if reason != "" {
		FieldVirtualUndefinedTotal.WithLabelValues(p.name, reason).Inc()
		return EvalResult{Reason: reason}
	}
	return EvalResult{Value: v, OK: true}
}

func (p *virtualPlan) EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict {
	if !ValidCmpOp(op) {
		return Verdict{Reason: reasonInvalidComparator}
	}
	r := p.Eval(frame)
	if !r.OK {
		return Verdict{Reason: r.Reason}
	}
	return Verdict{OK: true, Satisfied: Compare(op, r.Value, threshold), Value: r.Value}
}

// evalBoundArg 求值一个标量参数。
func evalBoundArg(arg *boundVirtualArg, frame []Reading) (float64, string) {
	switch arg.kind {
	case KindLiteral:
		if !arg.numeric {
			return 0, reasonNonNumeric
		}
		v, ok := findReading(frame, arg.literalName)
		if !ok {
			return 0, reasonMissing
		}
		if !isFiniteValue(v) {
			return 0, reasonNonFinite
		}
		return v, ""
	case KindSemantic:
		if !arg.bound || arg.physical == "" {
			return 0, reasonMissing
		}
		if !arg.numeric {
			return 0, reasonNonNumeric
		}
		v, ok := findReading(frame, arg.physical)
		if !ok {
			return 0, reasonMissing
		}
		if !isFiniteValue(v) {
			return 0, reasonNonFinite
		}
		return v, ""
	case KindVirtual:
		return evalBoundVirtual(arg.nested, frame)
	}
	return 0, reasonMissing
}

// evalBoundVirtual 递归求值派生树（无环/深度已在校验期保证）。
// 返回 (value, reason)：reason=="" 表示成功。
func evalBoundVirtual(b *boundVirtual, frame []Reading) (float64, string) {
	if b == nil || b.node == nil {
		return 0, reasonMissing
	}
	n := b.node
	switch n.fn {
	case "scale":
		v, reason := evalBoundArg(&b.scalars[0], frame)
		if reason != "" {
			return 0, reason
		}
		return finiteValue(v * n.scale)
	case "product":
		acc := 1.0
		for i := range b.scalars {
			v, reason := evalBoundArg(&b.scalars[i], frame)
			if reason != "" {
				return 0, reason
			}
			acc *= v
		}
		return finiteValue(acc)
	case "sum":
		acc := 0.0
		for i := range b.scalars {
			v, reason := evalBoundArg(&b.scalars[i], frame)
			if reason != "" {
				return 0, reason
			}
			acc += v
		}
		return finiteValue(acc)
	case "diff":
		acc, reason := evalBoundArg(&b.scalars[0], frame)
		if reason != "" {
			return 0, reason
		}
		for i := 1; i < len(b.scalars); i++ {
			v, reason := evalBoundArg(&b.scalars[i], frame)
			if reason != "" {
				return 0, reason
			}
			acc -= v
		}
		return finiteValue(acc)
	case "abs_diff":
		a, reason := evalBoundArg(&b.scalars[0], frame)
		if reason != "" {
			return 0, reason
		}
		c, reason := evalBoundArg(&b.scalars[1], frame)
		if reason != "" {
			return 0, reason
		}
		return finiteValue(math.Abs(a - c))
	case "ratio":
		a, reason := evalBoundArg(&b.scalars[0], frame)
		if reason != "" {
			return 0, reason
		}
		den, reason := evalBoundArg(&b.scalars[1], frame)
		if reason != "" {
			return 0, reason
		}
		if den == 0 {
			return 0, reasonDiv0
		}
		if den < 0 {
			// 负数分母也拒绝（方案 §6.2 不变量 3：避免符号歧义）。
			return 0, reasonNegativeDenominator
		}
		return finiteValue(a / den)
	case "sum_of", "max_of", "min_of":
		if b.family == nil {
			return 0, reasonMissing
		}
		f := b.family
		s := scanMembers(f.prefix, f.known, f.knownSet, f.nonNumeric, frame)
		if reason := familyUnboundReason(s); reason != "" {
			return 0, reason
		}
		switch n.fn {
		case "sum_of":
			return finiteValue(s.sum)
		case "max_of":
			return finiteValue(s.max)
		default:
			return finiteValue(s.min)
		}
	}
	return 0, reasonMissing
}
