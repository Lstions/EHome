package ingest

// Adapter 是接入层的**唯一扩展点**。
//
// 新增解析后端（Modbus 寄存器表 / MQTT JSON / OPC UA / ...）= 实现本接口 +
// 在 main.go 的 NewResolver 里注册一行。**consumer 不需要任何改动。**
//
// # 三条硬约束（违反会导致静默失效，评审必拒）
//
//  1. Name() 必须是有界枚举常量 —— 它进指标标签，拼接设备 ID 会造成指标爆炸。
//  2. Supports() 必须是**纯判断**：不查库、不写库、无副作用。它在每帧路径上被调用。
//  3. Decode() 失败必须返回 *DecodeError（带 Reason），不要返回裸 error ——
//     否则消费方无法分类归因，只能按 decode_failed 一刀切。
type Adapter interface {
	// Name 返回后端标识，用于 Sample.Method 与指标标签。
	Name() string

	// Supports 报告本 adapter 能否处理该设备。
	//
	// 约束：纯判断，不查库（上下文由调用方准备好后传入）。
	Supports(ctx SupportContext) bool

	// Decode 把原始字节解析为统一 Sample。
	Decode(ctx DecodeContext) (Sample, error)
}

// Resolver 按**注册顺序**选择 adapter。
//
// # 为什么顺序就是语义
//
// 这不是「随便挑一个能用的」：既有代码里 ConfigParser **必须**优先于 Driver，
// 否则标定驱动会被通用解析绕过（calibration 是解码不变量的一部分，
// 见 consumers_heavy.go:270 附近的原始注释）。把顺序显式化为「注册顺序」，
// 比藏在 if/else 里更难误改。
//
// # 并发安全
//
// 构造后只读，可被多 goroutine 并发调用 Select/Decode。
type Resolver struct {
	adapters []Adapter
}

// NewResolver 按给定顺序注册 adapter（顺序即优先级）。
//
// 传 nil 元素会被跳过（便于 main.go 按实际情况条件注册）。
func NewResolver(adapters ...Adapter) *Resolver {
	kept := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		if a != nil {
			kept = append(kept, a)
		}
	}
	return &Resolver{adapters: kept}
}

// Adapters 返回已注册的 adapter 名列表（只读，供启动日志与门禁断言）。
func (r *Resolver) Adapters() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a.Name())
	}
	return out
}

// Select 返回第一个 Supports 成功的 adapter。
//
// 返回 ok=false 表示无 adapter 可处理 ⇒ 调用方必须按 ReasonNoAdapter 处理
// （落审计 + 指标），**不得静默丢弃**。
func (r *Resolver) Select(ctx SupportContext) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	for _, a := range r.adapters {
		if a.Supports(ctx) {
			return a, true
		}
	}
	return nil, false
}

// Decode 是 Select + Decode 的便捷方法：按注册顺序尝试，返回**第一个成功**的结果。
//
// 语义细节（与改造前 consumer 的行为逐条对齐）：
//   - 按顺序尝试，一个成功即返回（不继续尝试后面的）；
//   - 全部失败 ⇒ 返回 *MultiError（含每个 adapter 的 Reason），便于归因；
//   - 无 adapter 可处理 ⇒ 返回 *MultiError 且 errors.Is 可判 ReasonNoAdapter。
func (r *Resolver) Decode(ctx DecodeContext, sctx SupportContext) (Sample, error) {
	if r == nil || len(r.adapters) == 0 {
		return Sample{}, &MultiError{Errs: []*DecodeError{
			NewDecodeError("", ReasonNoAdapter, nil),
		}}
	}
	var errs []*DecodeError
	attempted := 0
	for _, a := range r.adapters {
		if !a.Supports(sctx) {
			continue
		}
		attempted++
		s, err := a.Decode(ctx)
		if err == nil && len(s.Fields) > 0 {
			return s, nil
		}
		if err == nil {
			// adapter 说成功但零字段 —— 按 empty_result 归类（比 decode_failed 更准确）。
			errs = append(errs, NewDecodeError(a.Name(), ReasonEmptyResult, nil))
			continue
		}
		reason, ok := ReasonOf(err)
		if !ok {
			reason = ReasonDecodeFailed
		}
		errs = append(errs, NewDecodeError(a.Name(), reason, err))
	}
	if attempted == 0 {
		return Sample{}, &MultiError{Errs: []*DecodeError{
			NewDecodeError("", ReasonNoAdapter, nil),
		}}
	}
	return Sample{}, &MultiError{Errs: errs}
}
