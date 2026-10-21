// Package sensorfield 实现"数据字段解析层"（Field Resolution Layer）。
//
// # 它解决什么
//
// 设备数据、规则引擎、数据源三者都靠 `sensor_name` 这根裸字符串当"数据身份证"。
// 该字符串在驱动里是内联字面量、在库里是 `varchar`、在规则里是用户手打文本，
// 没有注册表、没有校验、没有单一事实源。结果是一类**静默失效**：
// 规则里写错一个字母，保存成功、永不触发、没有任何报错。
//
// 本包把"物理名字符串"升级为"**选择器契约**"（selector）：
//
//	literal    rsoc                     精确物理名（与历史行为逐字节相同）
//	family     temperature_*:max        `^prefix_[0-9]+$` 匹配的字段集合 + 聚合
//	semantic   @soc                     别名表归一（跨厂商同义字段）
//	virtual    virtual:cell_delta       由本帧多字段计算
//
// # 设计红线（评审定稿，实现必须遵守）
//
//  1. **数据面零依赖**：`internal/databus` 不得 import 本包。观测由 cmd/server 在既有
//     `SetLatestSinkFn` 注入点包装调用（见 `Observe`）。
//  2. **本包不依赖任何 internal 业务包**：只用 stdlib + pkg/logger + pkg/metrics。
//     声明由调用方通过 `RegisterDeclarations` 注入（避免 sensorfield → drivers 依赖）。
//  3. **求值路径零 DB**：编译产物（Plan）只持有内存快照；缓存 miss 时用当前快照即时编译，
//     不回查数据库。滞后上限 = 投影间隔（默认 60s），由指标暴露。
//  4. **不猜测**：解析不到 = 返回 unbound，绝不 fallback 到 0 / 首元素 / 猜测值。
//  5. **不承载业务语义**：本包不存阈值、告警级别、优先级、动作映射。
//  6. **可回滚**：`enabled=false` 时特殊选择器一律拒绝（ErrDisabled），字面量行为不变。
//
// # 两个"集合"必须分清
//
//   - **已知集合**（/known/）：该设备的"声明 ∪ 观测"，解析期用于判断 unbound 与缺员。
//   - **本帧集合**（/frame/）：求值那一帧实际解析出的字段。
//
// 聚合的缺员规则（方案 §5.3 定义 2）：`all/min/max/avg/sum` 在本帧缺失任一"已知成员"时
// 一律 unbound（不触发）；`any` 不受影响。理由：BMS 探头数随帧变化是真实场景，
// 只看本帧的 `all > 45` 会漏掉已知的第 3/4 个探头，属危险方向。
//
// # 聚合的空集与非法值（方案 §5.3 定义 1/3）
//
//   - 有效值 = 有限数（非 NaN、非 ±Inf）。非有限/非数值/缺失 ⇒ 该字段视为"本帧缺失"。
//   - 空集 ⇒ **全部聚合 unbound**（包括 sum：`sum=0` 会让 `sum < 10` 反向触发）。
//   - `min/max/avg/sum` + `neq` 是语义陷阱（几乎恒真），保存期给 warnings。
//
// # 对外 API 一览
//
//	New(Config) *Registry
//	(*Registry).RegisterDeclarations(deviceType string, fields []Field)
//	(*Registry).SetObservations(deviceID uint, fields []Field)   // 冷启动/投影刷新
//	(*Registry).Observe(deviceID uint, name, unit string, at time.Time)
//	(*Registry).Flush(ctx, sink FlushSink) error                 // 投影批次落库
//	(*Registry).Compile(deviceID uint, selector string) (Plan, error)
//	(*Registry).Resolve(deviceID uint, selector string) (Resolved, error)
//	(*Registry).KnownFields(deviceID uint) []Field
//	(*Registry).SetAliases([]AliasRule) error
//	(*Registry).SetVirtuals([]VirtualDef) error
//	(*Registry).Enabled() bool
//	(*Registry).Epoch() uint64
//
// # 实现期新增 API（冻结签名未改，仅新增；见评审窄复核 K1/契约缺口）
//
// 生命周期/配置补充（冻结契约缺 device→type 绑定与丰富层注入途径）：
//
//	(*Registry).SetDeviceType(deviceID uint, deviceType string) // 声明归属；变化 epoch+1
//	(*Registry).SetFieldMeta(fields []Field)                    // 丰富层 value_type/unit（meta > obs > decl）
//	(*Registry).SetEnabled(enabled bool)                        // 仅供测试/运维；配置仍重启生效
//	(*Registry).SnapshotAge() time.Duration
//	(*Registry).Validate() error                                // 启动期跨表门禁（含死候选检测）
//	(*Registry).SemanticsOf(physical string) []string           // /sensor-fields 的 semantics
//	(*Registry).Aliases() []AliasRule                           // /schema（关闭态可读）
//	(*Registry).Virtuals() []VirtualDef                         // /schema（关闭态可读）
//	(*Registry).Config() Config                                 // /schema limits
//
// 数据面求值（冻结的 Plan.Eval 无比较符参数，数学上无法实现 any/all）：
//
//	type CmpOp string            // gt|gte|lt|lte|eq|neq（与 models.AlertComparator* 同值）
//	type Verdict struct { OK, Satisfied bool; Value float64; Reason string }
//	type PredicatePlan interface { Plan; EvalPredicate(frame []Reading, op CmpOp, threshold float64) Verdict }
//	EvalPredicate(plan Plan, frame []Reading, op CmpOp, threshold float64) Verdict
//	Compare(op CmpOp, value, threshold float64) bool
//	PlanEpoch(plan Plan) uint64  // 取编译期 epoch（EpochPlan 扩展）；缓存键用，无“先 Compile 再读 Epoch”竞态
//
// 语义：literal/semantic/virtual 直接取值后比较；family any/all 用 EvalPredicate 才能实现
// ∃/∀ 语义（缺员/空集/非有限按 §5.3 判 unbound）；family min/max/avg/sum 先归约再比较。
// Plan.Eval 对 family any/all 返回 unbound + reason=aggregate_requires_comparator（fail-closed）。
//
// 指标：方案 §10 的 9 个 ehome_field_*/ehome_rule_* 指标全部定义在本包 metrics.go，
// 其它包不得重复定义同名 promauto 指标（init 期重复注册会 panic）；
// RuleNeverResolved / RuleNeverFired 由规则层 Set（kind=automation|alert）。
//
// 详细设计见 docs/设计/数据字段解析层-方案-2026-10-21.md（v0.2，已过三方评审）。
package sensorfield
