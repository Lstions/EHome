//go:build simulation

// 场景目录 · SIM-FIELD 数据字段解析层（设计/数据字段解析层-方案-2026-10-21.md §13.3）。
//
// # 本文件是什么（以及不是什么）
//
// 这是一个**可编译、可枚举、可解释**的场景骨架：8 个场景的结构体（ID/标题/设计依据/
// 前置数据/不变量清单）已经冻结，执行体（HTTP + 数据库断言）由后续集成批次填充。
// 它**不是**占位场景：每个场景至少 2 条不变量，且 TestFieldSkeletonInventory 会断言
// 清单完整性；一旦 sensorfield 接线完成而执行体仍未实现，场景会**显式失败**而不是跳过。
//
// # 为什么允许 t.Skip（与框架 §7 红线 5 的关系）
//
// 框架《场景仿真验证框架》§7 红线 5 规定场景内不使用 t.Skip，那是**终态**约束
// （场景必须真建真断言）。本文件是 D6 骨架：sensorfield 领域包（task-6）与
// 求值接入（task-7/8）尚未完成，此时若直接失败会把"尚未接线"与"实现有缺陷"
// 混为一谈。Lead 2026-10-21 明确允许骨架阶段用 interface + t.Skip 标注，
// 并要求骨架可枚举/可解释。集成完成后：删除 fieldResolverOrSkip 的 Skip 分支，
// 场景自动变成真断言（无 skip 可躲）。
//
// # 覆盖矩阵（方案 §13.3 的 8 个场景 + §5.3 v0.3 缺员方向性）
//
//	SIM-FIELD-001 跨厂商同义（@soc：嘉佰达 rsoc ⇄ 泰琪丰 battery_capacity）
//	SIM-FIELD-002 串数 4→2：any 正常；min/max degraded(partial_frame)；all/avg/sum unbound
//	SIM-FIELD-003 族消失：已知成员本帧全缺 ⇒ unbound；空集不产生 true/false
//	SIM-FIELD-004 别名歧义：同物理名多语义且未 allow_overlap ⇒ 启动失败（ErrAliasConflict）
//	SIM-FIELD-005 派生字段：virtual:cell_delta 缺输入 ⇒ unbound；齐全 ⇒ diff 值
//	SIM-FIELD-006 空集：6 个聚合一律 unbound（包括 any/all/sum）
//	SIM-FIELD-007 partial_frame：缺员原因可区分（partial_frame vs unbound_partial_avg）
//	SIM-FIELD-008 legacy 值：引号/大小写等存量值在 M0 分类为 special→needs_review，
//	                不得被自动升级成 family 求值（M0 special→literal 降级变异点见门禁 9）
//
// 名称纪律（catalog 门禁 8）：本文件包级标识符一律以 field 开头。
package catalog

import (
	"fmt"

	"ehome/backend/internal/sensorfield"
	"ehome/backend/simulation/harness"
)

// fieldDomain 是本域标识（设计 §6 表命名：取 SIM- 前缀后的短名）。
const fieldDomain Domain = "FIELD"

// fieldResolver 是本域与 sensorfield 的**唯一**集成点（窄接口）。
//
// 为什么不直接引用 *sensorfield.Registry：领域包由 impl-core 并行实现，
// 骨架期引用具体类型会让"未实现的方法"把整个 catalog 包编译搞红；
// 窄接口只依赖**已冻结**的 types.go（Resolved/Plan 类型稳定），
// 集成层（task-7/8）注入真实 Registry 即可。
type fieldResolver interface {
	Resolve(deviceID uint, selector string) (sensorfield.Resolved, error)
	Compile(deviceID uint, selector string) (sensorfield.Plan, error)
}

// fieldResolverFactory 由集成层注入；nil 表示 sensorfield 尚未接线（见文件头）。
var fieldResolverFactory func() fieldResolver

// fieldSetResolverFactory 设置集成点并返回旧值（仅供本包测试注入假实现）。
func fieldSetResolverFactory(f func() fieldResolver) func() fieldResolver {
	old := fieldResolverFactory
	fieldResolverFactory = f
	return old
}

// fieldPendingReason 返回"为什么当前不能执行场景"；空串表示已可执行。
func fieldPendingReason() string {
	if fieldResolverFactory == nil {
		return "SIM-FIELD 骨架：sensorfield 领域包尚未接线（task-6/task-7/task-8 进行中，设计 §13.3）"
	}
	return ""
}

// fieldResolverOrSkip 取集成点；未接线时显式 Skip（并说明终态：集成后删除该分支）。
func fieldResolverOrSkip(e *harness.Env) fieldResolver {
	e.T.Helper()
	if reason := fieldPendingReason(); reason != "" {
		e.T.Skip(reason + "；本骨架执行体待集成后填实（届时删除 Skip 分支，变为真断言）")
		return nil
	}
	return fieldResolverFactory()
}

// fieldCheck 是一条可解释的不变量检查（数据化：名称 + 期望结果）。
//
// 骨架阶段它是"清单项"；执行体填实后由 fieldRunChecks 逐条执行并断言。
type fieldCheck struct {
	Invariant string // 要证明的不变量（人类可读，含期望方向）
	Expect    string // 期望结果（resolved/受控值/unbound(reason)/启动失败…）
}

// fieldScenarioSpec 是一个 SIM-FIELD 场景的完整声明（与执行解耦）。
type fieldScenarioSpec struct {
	ID       string
	Title    string
	Doc      string
	Setup    string   // 前置数据（设备/帧/配置）的语义描述，供执行体构造
	Coverage []string // 覆盖标签（TestFieldSkeletonInventory 校验与方案 §13.3 对齐）
	Checks   []fieldCheck
}

// fieldSpecs 是 SIM-FIELD-001..008 的冻结清单（§13.3）。
var fieldSpecs = []fieldScenarioSpec{
	{
		ID:       "SIM-FIELD-001",
		Title:    "同一条 @soc 规则在两种不同厂商设备上都能命中（用户不必记物理名）",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-001（跨厂商同义）；§6.1 别名表",
		Setup:    "设备 A=嘉佰达 BMS（decl=rsoc），设备 B=泰琪丰逆变器（decl=battery_capacity）；别名 soc.candidates=[rsoc, battery_capacity]",
		Coverage: []string{"cross_vendor_semantic"},
		Checks: []fieldCheck{
			{Invariant: "设备 A 上 Resolve(@soc) 的 Kind=semantic 且 Matched=[rsoc]", Expect: "resolved(kind=semantic,matched=[rsoc])"},
			{Invariant: "设备 B 上 Resolve(@soc) 的 Kind=semantic 且 Matched=[battery_capacity]", Expect: "resolved(kind=semantic,matched=[battery_capacity])"},
			{Invariant: "两台设备语义相同、物理名不同（@soc 不把物理名写死）", Expect: "same_semantic_different_physical"},
		},
	},
	{
		ID:       "SIM-FIELD-002",
		Title:    "电池串数从 4 降到 2 时，保护型规则按方向降级而不是静默失效",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-002；§5.3 定义 2（v0.3 方向性修订）",
		Setup:    "已知集合 F_known={cell_voltage_1..4}；本帧 F_now={cell_voltage_1,cell_voltage_2}（JBD 探头数随帧变化，jiabaida_parse.go:249）",
		Coverage: []string{"family_shrink_4_to_2"},
		Checks: []fieldCheck{
			{Invariant: "any 按本帧正常求值（存在性语义不受缺员影响）", Expect: "bound"},
			{Invariant: "all 因缺员 unbound（放行方向不安全）", Expect: "unbound(partial_frame)"},
			{Invariant: "min/max 按本帧求值且 degraded=true + partial_frame 指标（极值保护必须继续工作）", Expect: "bound(degraded,reason=partial_frame)"},
			{Invariant: "avg/sum 因缺员 unbound（统计量失真，无安全解释）", Expect: "unbound(unbound_partial_avg)"},
		},
	},
	{
		ID:       "SIM-FIELD-003",
		Title:    "换型后整族字段消失时，规则不误触发也不报假成功",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-003；§5.3 定义 3",
		Setup:    "已知集合含 cell_voltage_1..4；本帧一个都不出现（族消失）",
		Coverage: []string{"family_disappeared"},
		Checks: []fieldCheck{
			{Invariant: "F_now=∅ ⇒ 6 个聚合全部 unbound（不产生 true/false）", Expect: "unbound(empty_frame)"},
			{Invariant: "unbound 不是错误：规则保存/加载仍成功，仅求值不触发", Expect: "load_ok_eval_unbound"},
		},
	},
	{
		ID:       "SIM-FIELD-004",
		Title:    "同一物理名被两条语义同时引用时启动失败（用户看到明确错误而不是半可用系统）",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-004；§6.1 规则 2/3（冲突与单位类）",
		Setup:    "alias 配置：soc.candidates=[rsoc]、battery.candidates=[rsoc]（同物理名、均未 allow_overlap）",
		Coverage: []string{"alias_conflict"},
		Checks: []fieldCheck{
			{Invariant: "SetAliases 返回 ErrAliasConflict（fail-closed，不放宽为 warning）", Expect: "startup_error(ErrAliasConflict)"},
			{Invariant: "冲突项不被静默采用任一语义（不猜）", Expect: "no_silent_pick"},
			{Invariant: "候选单位类与 ExpectedUnit 不符亦返回 ErrAliasConflict", Expect: "startup_error(unit_class_mismatch)"},
		},
	},
	{
		ID:       "SIM-FIELD-005",
		Title:    "派生字段缺输入时给出「无法计算」而不是拿 0 顶替",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-005；§6.2 统一不变量 1/2/3",
		Setup:    "virtual:cell_delta = diff(cell_voltage_max, cell_voltage_min)；帧 A 两者齐全（max=3.40,min=3.20）；帧 B 缺 cell_voltage_min",
		Coverage: []string{"virtual_missing_input"},
		Checks: []fieldCheck{
			{Invariant: "帧 A：Resolve/求值得到 0.20（差值按物理量计算）", Expect: "value=0.20"},
			{Invariant: "帧 B：任何输入缺失 ⇒ 整体 unbound（不 fallback 到 0）", Expect: "unbound(missing_input)"},
			{Invariant: "输出 NaN/±Inf/ratio 分母≤0 ⇒ unbound + virtual_undefined 指标", Expect: "unbound(non_finite_or_bad_denominator)"},
		},
	},
	{
		ID:       "SIM-FIELD-006",
		Title:    "没有任何匹配字段时六种聚合都不触发（避免 sum=0 / all 空真反向触发）",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-006；§5.3 定义 3（v0.3 统一口径，窄复核 Q1）",
		Setup:    "selector=cell_voltage_*:<agg>，已知集合为空且本帧为空",
		Coverage: []string{"empty_set"},
		Checks: []fieldCheck{
			{Invariant: "any/all 空集 unbound（不采用空真）", Expect: "unbound(empty_frame)"},
			{Invariant: "min/max/avg/sum 空集 unbound（sum≠0、min≠+Inf）", Expect: "unbound(empty_frame)"},
			{Invariant: "6×1 全格断言：空集不产生 bound=true/false", Expect: "no_bound_result"},
		},
	},
	{
		ID:       "SIM-FIELD-007",
		Title:    "缺员与无数据在审计里可区分（运维能说清为什么没触发）",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-007；§5.3 定义 2 注（Reason 区分）",
		Setup:    "同一 family，分别构造：帧齐全 / 帧缺 2 个已知成员 / 帧为空",
		Coverage: []string{"partial_frame_reason"},
		Checks: []fieldCheck{
			{Invariant: "缺员（degraded 路径）Reason 含 partial_frame 与 missing 列表", Expect: "reason=partial_frame(missing=[...])"},
			{Invariant: "avg/sum 缺员 Reason 为 unbound_partial_avg（与'无数据'可区分）", Expect: "reason=unbound_partial_avg"},
			{Invariant: "空集 Reason 为 empty_frame（不与 partial 混用）", Expect: "reason=empty_frame"},
		},
	},
	{
		ID:       "SIM-FIELD-008",
		Title:    "升级前保存的特殊形态值不会被自动当成选择器执行（存量行为可解释）",
		Doc:      "docs/设计/数据字段解析层-方案-2026-10-21.md §13.3 SIM-FIELD-008；§4.4 存量三类处置；§12.3#9 变异点",
		Setup:    "存量行 A='rsoc'（literal）、B='temperature_*'（special，需人工复核）、C='Temperature_*'（special 且 syntax_ok=false）、D='RSOC'（legacy）",
		Coverage: []string{"legacy_value"},
		Checks: []fieldCheck{
			{Invariant: "M0 分类：A=literal、B=special、C=special(syntax_ok=false)、D=legacy（不降级为 literal）", Expect: "classification_exact"},
			{Invariant: "special 默认不生效（needs_review），不得被自动按 family 求值——变异：special→literal 降级后必须变红", Expect: "needs_review_not_evaluated"},
			{Invariant: "legacy 按精确字面量匹配（与升级前一致），落审计告警", Expect: "literal_exact_match"},
		},
	},
}

func init() {
	for i := range fieldSpecs {
		spec := fieldSpecs[i]
		Register(Scenario{
			ID:     spec.ID,
			Title:  spec.Title,
			Domain: fieldDomain,
			Doc:    spec.Doc,
			Run: func(e *harness.Env) {
				fieldRun(e, spec)
			},
		})
	}
}

// fieldRun 执行一个场景。
//
// 骨架阶段语义（刻意 fail-closed）：
//   - resolver 未接线 ⇒ Skip（说明终态；集成后删除该分支）；
//   - resolver 已接线但执行体未填 ⇒ **失败**（不得静默通过，否则骨架会变成假绿）。
func fieldRun(e *harness.Env, spec fieldScenarioSpec) {
	e.T.Helper()
	if len(spec.Checks) == 0 {
		e.Fatalf("SIM-FIELD %s 没有不变量检查（禁止占位场景）", spec.ID)
	}
	resolver := fieldResolverOrSkip(e)
	if resolver == nil {
		return
	}
	e.Fatalf("SIM-FIELD %s 执行体尚未实现：resolver 已注入但场景步骤待集成批次填实。"+
		"不变量(%d 条) 首条=%q；期望=%q。fail-closed：此处必须红，不得跳过。",
		spec.ID, len(spec.Checks), spec.Checks[0].Invariant, spec.Checks[0].Expect)
}

// fieldRunDisposition 返回"当前集成状态下场景应取的行为"（供测试断言与 fieldRun 分支一致）。
//
// skip：resolver 未接线 -> 显式 Skip（骨架阶段）；
// failclosed：resolver 已接线但执行体未实现 -> 必须失败（防骨架变成假绿）。
func fieldRunDisposition(hasResolver bool) string {
	if !hasResolver {
		return "skip"
	}
	return "failclosed"
}

// fieldCheckSummary 返回场景检查清单的一行摘要（诊断/测试用）。
func fieldCheckSummary(spec fieldScenarioSpec) string {
	return fmt.Sprintf("%s: %d checks / coverage=%v", spec.ID, len(spec.Checks), spec.Coverage)
}
