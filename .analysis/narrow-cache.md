# 窄复核 ③a：五元组编译缓存键完备性（方案 v0.2 §7.2）

> 复核人: impl-reviewer｜任务: task-11｜工作树: .agents/worktrees/field-resolver（分支 field-resolver，基于 d406dc2d + 9354408a 冻结契约）
> 依据: 方案 v0.2（唯一权威）、评审报告、冻结契约 backend/internal/sensorfield/{doc.go,types.go,errors.go}（未改动）
> 结论先行: **条件完备（conditional complete）**——五元组对"plan 身份"本身成立，但存在 **2 个反例 + 2 个契约缺口**，需按下文 4 条修订后才可判"完备"。

---

## 0. 结论

| 维度 | 判定 |
|---|---|
| 五元组作为 plan 身份的**逻辑键** | 成立：Plan 只依赖 (deviceID, 规范化 selector, snapshot epoch)；ruleID 是存储命名空间，ruleVersion 是冗余保险（见 §4） |
| 五元组作为**实现规格**（§7.2 struct/map 写法） | **有反例**：map[uint]rulePlan 一个 ruleID 只装得下 1 个 plan，无法承载 any-device 规则与 trigger+conditions 多 selector（反例 R1） |
| 任务列出的 8 个失效场景 | 覆盖 5，部分 1，未覆盖 2（改规则其它字段的求值状态、设备换型），见 §1 场景表 |
| 冻结契约是否足以承载失效语义 | 基本足够（Plan 不可变、Eval 可表达 unbound、Registry.Enabled/Epoch 存在），但缺 **Plan 编译时 epoch** 与 **device→deviceType 映射 API** 两项（§3） |
| automation 与 alert 的 LoadRules 行为一致性 | 结构相同（全量替换 + Invalidate=LoadRules + 30s ticker），但两者都**未**在规则内容变化时清传感器阈值窗口状态，且 §9.2 只写了"alert 求值走 plan"未写"alert.LoadRules 清 plans"（§5） |

---

## 1. 八个场景逐条判定（任务清单）

| # | 场景 | 覆盖 | 证据 / 说明 |
|---|---|---|---|
| 1 | 改 selector | 是 | selectorHash 变化（且 LoadRules 清空 plans 兜底）。要求 selectorHash 输入是**解析后的规范化形式**（方案 §5.1 已规定 trim；建议 hash 由 kind/prefix/agg/physical 字段生成，避免 temperature_*:any 与 temperature_* 落不同 key——只影响重编译次数，不影响正确性） |
| 2 | 改规则其它字段（阈值/比较符/DurationSec/动作/cooldown） | **否（反例 R2）** | plan 会正确重编译（LoadRules 清空覆盖），但 **求值器窗口里缓存的是旧字段下算出的 satisfied 布尔**：automation evaluator.go:830-832 存 sample{satisfied: satisfied}、alert evaluator.go:204-207 存 sample{value, satisfied}；LoadRules 只在规则**被移除**时删窗口（automation :270-276、alert :105-112），内容变化时窗口保留。结果：改阈值/比较符后，最长 TriggerDurationSec 内仍按旧阈值的历史 satisfied 判定（阈值调高可能仍判"连续满足"→ 误触发；调低可能漏触发） |
| 3 | 改 trigger 设备 | 条件式覆盖 | deviceID 进 key 即可；但 §7.2 **未定义 deviceID 取哪个**。必须是**本帧字段来源的 edgeDeviceID**，不能用 rule.TriggerEdgeDeviceID/alert.TargetID（可为 logical ID）。见反例 R3 |
| 4 | 别名（重启生效） | 是 | v0.2 §11 明确无热加载，配置变更须重启；进程重启后 plans 为空、epoch 重新计。**前提**：别名在首个规则编译前加载完成（init 顺序要求，§9.2#10 应写明 main 内顺序：New Registry → SetAliases/SetVirtuals → 构造 evaluator/启动 nodemgr） |
| 5 | 派生配置变更 | 是 | 同上（重启生效 + SetVirtuals 先于首帧） |
| 6 | LoadRules 30s 兜底刷新 | 是 | 方案 §7.2 明确"LoadRules()/Invalidate 后清空 plans"；automation evaluator.go:252-341 / :339 Invalidate 与 alert :98-119 / :121 行为同构（全量替换、只加载 enabled、30s ticker 同调用）。**需补**：清空必须与 rules swap 在同一把锁内；§9.2 只给 alert 写"求值走 plan"，需明确**两包都清 plans**，否则 alert 侧只能靠 ruleVersion 兜底（能兜住但不该依赖） |
| 7 | kill switch 翻转 | 不由五元组覆盖 | 五元组不含 enabled；覆盖来自 §11"配置变更需重启"（重启→缓存空）+ §7.2"Eval 前检查 enabled（原子）"。**前提**：line 389 的检查是对**特殊 selector 逐帧**生效（Plan.Kind() != KindLiteral && !registry.Enabled()），而不是只在编译时查一次。若未来加配置热加载，必须让 enabled 进快照/代际，否则五元组过期 |
| 8 | 设备换型 | **否（同 ID 换型）** | ①换新 edge_device（新 ID）：是——新 deviceID 新 plan，旧 plan 由 LoadRules 清掉。②**同 ID 换 device_type**（改 DeviceConfigID / DeviceConfig.DeviceType）：否——冻结契约没有任何"deviceID→deviceType"注册 API（§3 缺口 K2），Registry 的 decl[deviceType] 并集无从关联/更新，epoch 也不会因此自增 → 旧 plan 的 known-set（声明族成员）继续生效。方案 §7.2 未提设备类型变更 |

---

## 2. 反例（按严重度排序）

### R1（高）map[uint]rulePlan 结构装不下多 plan/target——五元组的 deviceID 无处安放
- **位置**: 方案 §7.2（v0.2 行 375-387 代码块）。
- **问题**:
  1. map[uint]rulePlan 的 key 只有 ruleID，value 里带一个 deviceID。对 TriggerEdgeDeviceID=0（"任意设备上报该字段即触发"，模型注释 models/automation.go:157 明确支持）的规则，A/B 两台设备交替上报时同一 ruleID 只有一个槽：要么每帧重编译（热路径分配 + 指标噪声），要么实现者错误地按 rule.TriggerEdgeDeviceID 编译（=0 或 logical ID → known-set 空 → 恒 unbound）。
  2. 一条规则有 **1 个 trigger selector + N 个 conditions selector**（models.AutomationCondition.SensorName automation.go:97、求值点 automation/evaluator.go:807-811；方案 §9.2#2 明确 conditions 也走 plan）。struct 只有一组 selectorHash/plan，表示不了 N+1 个 plan。
  3. 五元组本身没问题，问题在"复合键给出来了，存储结构却退化成 ruleID 单键"。
- **修改建议**: 把存储显式写成 slot 化（下方为示意，不引入新包）：

      type planSlot uint8 // 0=trigger, 1..N=conditions[i-1]
      type planKey struct{ ruleID uint; slot planSlot }
      type cachedPlan struct{ deviceID uint; selectorHash uint64; epoch uint64; ruleVersion int64; plan sensorfield.Plan }
      evaluator.plans map[planKey]cachedPlan   // 或 map[planKey]map[uint]cachedPlan（deviceID 维度）

  key 中的 deviceID 仅在规则绑逻辑设备/任意设备时可能多于一个，故 deviceID 应进 map key 而不是只放 value。**五元组缺的是 slot**。

### R2（高）"改规则其它字段"只重编译 plan，不清求值器派生状态 → 最长 DurationSec 内用旧阈值历史判定
- 见 §1 场景 2。这是"五元组覆盖了 selector 变更、但覆盖不了非 selector 字段变更"的直接反例；任务清单里"改规则其它字段"因此判未覆盖。
- **证据**: automation evaluator.go:830-832、:270-276（只在 rule 被移除时删 windows/triggered）；alert evaluator.go:204-207、:105-112；automation 的 A-17 白名单 sameWindowStateFields（evaluator.go:288-301）**刻意只覆盖 time_window/sun 字段**，不覆盖 sensor_threshold 的 TriggerSensorName/Comparator/Threshold/TriggerDurationSec。
- **修改建议**: 在 automation/alert 两包 LoadRules 里对"仍在缓存但内容变化"的 sensor_threshold 规则，按**扩展白名单**（TriggerType/TriggerSensorName/TriggerComparator/TriggerThreshold/TriggerDurationSec/Enabled）清 windows/triggered/firing；或把 sample 从派生布尔改为原始 value 再按当前阈值重算。列入 §13.1"缓存五元组失效"用例（§6 T2）。

### R3（中）deviceID 语义未定义 → 逻辑设备/合并链规则可能恒 unbound
- **问题**: 现有求值器对绑定逻辑身份的规则做 merge-chain 匹配：automation evaluator.go:753-767、alert evaluator.go:152-170。若 plan 的 deviceID 取自 rule.TriggerEdgeDeviceID/alert.TargetID（logical_device 时是 LogicalDevice.ID），KnownFields(logicalID) 为空 → 所有特殊 selector unbound（静默不触发，正是本方案要消灭的失效模式）。
- **修改建议**: §7.2 写明"**deviceID = Evaluate(edgeDeviceID) 的入参（字段来源的 EdgeDevice.ID）**；触发目标只影响规则是否参与本帧求值，不参与 plan 键"。并加测试 T5。

### R4（低-中）ruleVersion 不是"内容版本"，且对 plan 身份冗余
- **问题**: ruleVersion = rule.UpdatedAt.UnixNano()；但 planner 日常簿记会更新 automation_rules 行：planner.go:470（触发后 last_triggered_at）、:1054（手动触发）、:1183（确认制回锚）、evaluator.go:208（启动回填冷却锚点）——均使用 Update("last_triggered_at", ...) 而非 UpdateColumn；GORM 默认 AutoUpdateTime 会同时改写 UpdatedAt（建议用一条单测确认本仓 gorm 配置未禁用该行为）。后果：ruleVersion 随"触发/手动执行/回填"漂移，不代表规则内容版本。功能上**不构成正确性缺陷**（规则变更已由 LoadRules 清空覆盖；plan 也不依赖阈值/动作字段），但：
  - §13.4 变异"缓存键去掉 ruleVersion ⇒ 缓存失效用例必须红"**无法用行为断言**实现（去掉后行为不变）。必须改为断言**重编译计数**（fake registry compile call count），或删掉该变异。
  - 若把 ruleVersion 当"编辑即重编译"依据，会因 last_triggered_at 漂移产生无谓重编译（每 30s LoadRules 已清空，实际影响可忽略）。
- **修改建议**: 保留五元组（无害），但在 §7.2 注明"ruleVersion 仅为冗余保险，plan 身份 = deviceID+selectorHash+epoch；权威失效路径是 LoadRules 清空"，并把 §13.4/§13.1 的断言方式改为计数断言。

---

## 3. 附带核对：冻结契约（sensorfield/types.go 的 Plan 接口）是否足以承载失效语义

**结论: 基本足够，但需 2 项扩展 + 1 项文档纪律（不改则必须在方案里写死调用纪律）。**

已具备的：
- Plan.Selector()/Kind()/Eval(frame)（types.go:112-117）：不可变、可并发；EvalResult.OK=false 可表达 unbound，足以让"unbound plan"作为缓存值而非错误（避免每帧重编译）；Kind() 足以做"特殊 selector + enabled=false ⇒ 不触发"的 kill switch 判定。
- Registry.Enabled()/Epoch()（doc.go:56-57）：kill switch 与失效所需。

**缺口 K1（必须：扩展或写死纪律）—Plan 不带"编译时的 epoch"**：
- 竞态：缓存填充典型写法是 p, err := reg.Compile(dev, sel); cached.epoch = reg.Epoch()。Compile 内部快照 S1 后、读 Epoch() 前若投影任务发布 S2（新字段），缓存条目标记 epoch=S2 而 plan 来自 S1 → 后续所有帧 epoch 相等直接命中旧 plan，**新发现字段永久不进该 plan 的 known-set，直到下一次 epoch 变化**（对 all/min/max/avg/sum 的缺员判定与 family 展开是 fail-open 方向：保护型规则可能漏触发）。
- 建议扩展（二选一）：
  1. 契约加 Plan.Epoch() uint64（编译时快照代际），缓存直接存该值；或
  2. 契约加 Compile(deviceID, selector) (Plan, epoch uint64, err)。
- 若坚持冻结不改契约：必须在 doc.go 写明纪律"**调用方必须先读 Epoch() 再调 Compile()，并以先读值作为缓存键**"——最坏只是一次多余重编译（安全方向），不会出现"新 epoch 旧 plan 永久命中"。请 lead 在契约冻结评审中明确选哪种。

**缺口 K2（必须：扩展）—没有 deviceID→deviceType 注册/换型 API**：
- 契约 API 只有 RegisterDeclarations(deviceType, fields) 与 SetObservations(deviceID, fields)（doc.go:47-48），**没有任何把 deviceID 关联到 deviceType 的方法**，而 KnownFields(deviceID) = decl[type] ∪ obs[deviceID] 正是 G-1/声明并集的基础；方案 §4.1 Registry 草图也只有 decl map[string][]FieldDecl。
- 影响：①D1 无法实现"设备→声明"（G-1 的 decl 兜底不可用）；②"设备换型"（同 ID 改 type）无法表达，epoch 也不会因类型变化自增（场景 8）。
- 建议扩展：SetDeviceType(deviceID uint, deviceType string)（同值 no-op；变化时 epoch+1，并触发该设备 known-set 重算）。换型时调用方（api handler 或配置同步路径）需接线，但至少契约要有这个出口。

**契约文档级错误（非契约缺口，但会误导实现）**：方案 §7.2 代码块写 plan sensorfield.Compiled，冻结契约里**不存在** Compiled 类型（只有 Plan 接口，types.go:112、doc.go:51）。照抄无法编译；请把方案文本改为 plan sensorfield.Plan（不要为了对齐文档去改冻结契约新增 Compiled 别名）。

---

## 4. 五元组本身的数学判断

Plan 的语义 = f(该设备 known-set 快照, 规范化 selector)。因此：
- **充分**：只要 epoch 严格覆盖一切影响 known-set/别名/派生的变更，(deviceID, selectorHash, epoch) 相同 ⇒ 编译结果必然相同。
- **必要**：这三个都必须进键；ruleID 仅命名空间；ruleVersion 对 plan 内容无关（冗余保险）。
- **尚缺的显式定义**（写进方案 §7.2 或契约 doc.go 即可，不需改结构）：
  1. epoch 触发集：RegisterDeclarations（声明变化）、Observe/SetObservations（**仅当该设备 known-set 实际新增字段时** bump；不要每次 Observe 都 bump，否则每 60s 全量重编译）、SetAliases/SetVirtuals（重启语义下实际只在启动发生，仍应 bump 以便未来/测试）、SetDeviceType（K2 新增）；
  2. selectorHash 输入 = 规范化 selector（trim 后原串或解析后结构）；
  3. ruleVersion 的语义与用途如上。

---

## 5. automation 与 alert LoadRules/Invalidate 行为一致性核对（代码事实）

| 项 | automation | alert | 一致性 |
|---|---|---|---|
| LoadRules 位置 | evaluator.go:252 | :98 | — |
| 加载条件 | enabled=true AND trigger_type IN (sensor_threshold,time_window,sun_event) | enabled=true | 有差异但合理（alert 无多触发类型） |
| 缓存替换 | e.mu.Lock(); e.rules=next; e.mu.Unlock() | 同 | 一致 |
| 30s 兜底 | Start():344-357 | :126-140 | 一致 |
| Invalidate | :339 = LoadRules | :121 = LoadRules | 一致 |
| 清理已移除规则状态 | windows/triggered（:270-276） | windows/firing（:105-112） | 同构 |
| 清理**内容变化**规则状态 | 仅 A-17 白名单（time_window/sun，:278-301） | 无 | **一致缺陷（R2）** |
| 加载后是否清 plans | 方案要求有，代码尚未有（实现期新增） | **方案 §9.2#3 只写求值走 plan，未写清 plans** | 需在两包都写明 |

**结论**：两包结构相同、可实现同构；风险在"§9.2 只提求值点不提缓存清理"，实施者很可能只改 evalRule 而不在 LoadRules 清 plans。由于五元组含 ruleVersion/selectorHash，alert 侧即使漏清也能在规则更新后重编译（UpdatedAt 变化）——能兜住，但不该把正确性押在冗余键上；且 R2 的窗口状态问题两包都会出现。

---

## 6. 建议增补的测试（把"五元组失效"从一句话变成可执行断言）

| ID | 用例 | 断言方式 |
|---|---|---|
| T1 | 改 selector → 下一帧生效 | 行为断言 + fake registry compile 计数 |
| T2 | DurationSec>0 规则改阈值/比较符 → 窗口状态被清 | 构造旧阈值下连续满足窗口，改阈值后下一帧不得凭旧 satisfied 触发（**R2 回归锁**） |
| T3 | TriggerEdgeDeviceID=0，A/B 两设备交替上报 | 两设备各自 plan，compile 计数不随帧增长；两设备都正确求值（R1 回归） |
| T4 | trigger + conditions 双 selector，分别编辑 | 两条 slot 独立失效，互不串（R1 回归） |
| T5 | alert TargetType=logical_device + merge chain | plan 用帧设备编译；特殊 selector 能触发（R3 回归） |
| T6 | LoadRules 每 30s 兜底 + 并发求值 | -race 下无竞态；清空后惰性重编译；旧规则回填不污染新键 |
| T7 | kill switch 重启（enabled=false） | 特殊 selector 不触发 + 落审计；literal 照旧；补"不得每帧重复落审计"节流断言 |
| T8 | Compile 与 Epoch 竞态 | 允许一次多余重编译，禁止"epoch 已新、plan 仍旧"被永久命中（K1） |

---

## 7. 最终判定

**条件完备**：五元组（补 slot、明确 deviceID 语义后）可以作为 plan 缓存键的完备规格；但按当前方案 §7.2 的 struct 写法与失效时机，会被以下 2 个反例击中：R1（多 plan 装不下）、R2（非 selector 规则字段改动不清求值状态）。另有 2 个契约缺口（K1 Plan 编译时 epoch、K2 device→type 映射）需 lead 在契约冻结评审中裁决。若 R1/R2/K1/K2 全部按上文修订，本项可判**完备**。

> 本文件未修改任何代码/方案/契约；仅新增 .analysis/narrow-cache.md。git 未提交（.analysis 不在仓库跟踪范围；如需入库请 lead 指示）。

---

## R2-followup（追加复核）：R2 修法完备性——"扩展 sameWindowStateFields + 内容变化清窗口"够不够

> 结论: **不完备**。原修法只对 ①②④⑥ 成立；③ 是隐含误触发；⑤ 会真误触发（窗口不分设备）；另有 4 个结构性问题（载体错误、alert 缺 prev 快照、sample 无原始值、白名单必须排除簿记字段）。逐项见下表。

### F1. 逐项判定（①–⑥）

| # | 变更 | 清窗口？ | 清哪些 map | 残留 / 说明 |
|---|---|---|---|---|
| ① | TriggerThreshold | **必须清** | automation alert: windows | satisfied 布尔是旧阈值派生值。级联: triggered(automation 冷却锚)/lastNotifyAt 保留；alert firing 需裁决（见 F3，否则"陈旧 firing 永挂"） |
| ② | TriggerComparator | **必须清** | windows | 同① |
| ③ | TriggerDurationSec | **建议清**（严格说可不 清） | windows | 窗口原始数据 (at,satisfied) 与 duration 无关，理论可保留；但**缩短 DurationSec 会凭旧样本立即触发**（误触发方向）——清窗口把语义变为"改后重新积累"，可预测。若不清必须在方案写明"即时生效" |
| ④ | ConditionsJSON | **必须清** | automation: windows | conditions 是窗口更新的门控（evaluator.go:807-811 不满足即 return，不写窗口）；旧 samples 反映旧条件。alert 无 conditions。另需同时有 R1 的 conditions plan 重编译（LoadRules 清 plans + slot 重编译）——两者别混为一谈 |
| ⑤ | TriggerEdgeDeviceID（automation）/ TargetType+TargetID（alert） | **必须清** | windows | **原修法最大漏洞**：窗口按 ruleID 存、不按 device 分区。规则从设备 A 改绑 B 后，A 的旧样本继续参与 B 的 windowSatisfied 判定 → 可用旧设备的连续满足 + B 的单条满足凑出触发（**真误触发**）。级联: triggered/lastNotifyAt 保留（规则级限速）；残留：新设备首触发可能被旧设备的冷却/日配额压制（安全方向，建议文档化；若产品要求"换设备重置配额"另立裁决） |
| ⑥ | TriggerSensorName（alert SensorName） | **必须清** | windows | satisfied 来自旧物理量；plan 由五元组失效重编译（已修）。triggered/lastNotifyAt 保留 |
| 附 | TriggerType 变化（sensor_threshold ↔ time_window/sun_event） | **必须清 windows** | automation: windows（必要时 windowStates/sunStates 由 A-17 比较器处理） | 现有 LoadRules 只对"规则被移除"清 windows（evaluator.go:270-276）；类型变化的规则仍留在 e.rules，旧 windows 残留，改回 sensor_threshold 时**旧窗口复活** |

### F2. 四个结构性问题（原修法表述的直接缺陷）

1. **载体错误**：sameWindowStateFields（automation evaluator.go:312-324）服务的是 time_window/sun 的 windowStates/sunStates 边沿态，**不能**把 sensor_threshold 字段直接塞进去——否则传感器字段漂移会清 time_window/sun 状态（A-17 同类"边沿反复触发"风险，注释 :253-259 已警告）。应**新增独立比较器** sameSensorTriggerState(old,new)（或按 TriggerType 分支）。
2. **alert 没有 prev 快照**：alert.LoadRules（evaluator.go:98-119）只做 e.rules = next + 删除"已移除规则"的 windows/firing，没有 prev。要实现"内容变化清窗口"必须先 prev := e.rules 再比较。
3. **sample 结构差异**：automation sample{at, satisfied}（:46-49）**没有原始值**，阈值/比较符变化后无法重算，只能清窗口；alert sample{at, value, satisfied}（:39-43）有 value，可**重算 satisfied** 以保留 DurationSec 连续性（可选优化：重算优于清，避免"改比较符导致告警状态丢失"）。若统一选"清"，建议在方案写明理由。
4. **白名单必须显式排除簿记字段**：UpdatedAt（触发/手动/回填都会 bump，见 task-11 R4）、Name、CooldownSec、MaxDailyExec、动作集合、last_triggered_at——否则 30s 兜底 LoadRules 会每 30s 清一次窗口 → DurationSec 永不满足（**漏触发**，A-17 教训的镜像）。同理 automation 的 triggered（冷却锚）与 planner 持久锚点 last_triggered_at 不能随内容变化清空，否则绕过冷却 → 可能重复下发真实设备动作。

### F3. 必须显式裁决的两个级联策略（否则修法仍不完备）

- **automation triggered（冷却）**：内容变化时**保留**（清它等于绕过 CooldownSec，方向危险）；窗口清空 + 冷却保留的结果是"冷却到期后需重新积累 DurationSec"，安全方向。文档写明"内容变更不重置冷却/日配额"。
- **alert firing / lastNotifyAt**：若保留 firing，改阈值后若新条件下永不满足、且设备离线 → 该告警**永久停在 firing**（陈旧假警，无 resolved）。建议：内容变化时若 firing=true，写一条 resolved 事件（detail=rule_updated）并删 firing + 删 lastNotifyAt；代价是若条件仍满足会再发一次 firing 通知（可见、可接受）。若不采用，必须在方案显式接受"陈旧 firing"并给出恢复路径。

### F4. 建议的最终修法（可直接落 §9.2/§13）

1. 新增两个比较器（不要复用 sameWindowStateFields）：
   - automation sameSensorTriggerState: TriggerType, TriggerSensorName, TriggerComparator, TriggerThreshold, TriggerDurationSec, TriggerEdgeDeviceID, ConditionsJSON
   - alert sameSensorTriggerState: SensorName, Comparator, Threshold, DurationSec, TargetType, TargetID
2. 两包 LoadRules 在 e.mu.Lock() 内 swap 后：if old,ok := prev[id]; ok && !sameSensorTriggerState(old, next[id]) { delete(e.windows, id) }；alert 需新增 prev。
3. 按 F3 处理 triggered / firing / lastNotifyAt。
4. 新增测试（接 task-11 §6 的 T 编号）：
   - T2a–T2f：①–⑥ 每项一条"内容变更后不得凭旧窗口立即触发 / 不得漏触发"；
   - T2g：30s 兜底 LoadRules 在仅 UpdatedAt/last_triggered_at 漂移时**不得**清窗口（A-17 回归，误清方向）；
   - T2h：TriggerType 来回切换后旧窗口不复活；
   - T2i：规则从设备 A 改绑 B 后，A 的旧样本不参与 B 的判定（⑤ 回归，误触发方向）；
   - T2j：firing 中的规则改阈值 + 设备离线，不得永久 firing（F3 方案）。
5. 残留清单（不在窗口机制内，方案须写明"有意不清"）：planner DB 冷却锚点 last_triggered_at（planner.go:470/1055/1184）与日熔断/配额状态；已发送通知不可撤回；在途 TriggerEvent 用触发时 rule 拷贝复核（planner 侧）；pending confirmation 事件按 rule_id 关联。

### F5. 判定

- 若只按原表述"扩展 sameWindowStateFields + 内容变化清窗口"实施：**不完备**——⑤ 真误触发（窗口不分设备）、③ 隐含误触发（缩短 DurationSec 用旧样本）、TriggerType 回切残留、alert 无 prev 无法检测变化、alert 陈旧 firing、且直接扩展 sameWindowStateFields 会误伤 time_window/sun 边沿态。
- 若采纳 F4 的 1–4（独立比较器 + prev 快照 + 级联策略 + T2a–T2j）：**完备**（6 项 + 类型切换全覆盖，残留项明确为"有意不清"）。

---

## F5-verdict：残留清单 → D4 可核对放行判据

> 用途：lead 用它做 D4 验收。原则：下列状态是**有意不清**（清它们会绕过冷却/配额/审计或制造重复动作），验收判据不是"重置"，而是"**行为被钉住 + 可观测 + 文档化**"。
> 通用要求：① 每条判据同时给"造数据 → 执行 → 断言"；② 涉及并发的用例跑 `-race`；③ Y1a/Y2a/Y5a 至少在 PG（`EHOME_TEST_DB=postgres`）跑一遍（SQLite 时间/精度差异会掩盖锚点问题）；④ 5 条全部通过 + 文档落位，才放行 D4。

### 总表

| # | 残留项 | 不清的理由（方向） | 验证方法 | 放行判据（期望结果） |
|---|---|---|---|---|
| Y1 | planner DB 冷却锚 `last_triggered_at` | 冷却按"触发过"计，与规则内容无关；清锚=绕过 CooldownSec → 可能重复下发真实设备动作 | 代码断言 + D4-Y1a/b/c（见下） | 内容更新后锚点时刻不变、仍落 `suppressed_cooldown` |
| Y2 | 日配额/日熔断（MaxDailyExec） | 配额按"当日已执行"计（automation_events 行）；重置=可越过日熔断 | 代码断言 + D4-Y2a/b/c | 内容更新后当日 executed 计数不重置、仍落 `suppressed_daily_limit`；日切正常恢复 |
| Y3 | 已发送通知/告警事件 | 历史不可撤回；改规则只应对未来生效 | 代码断言 + D4-Y3a/b | 历史 notifications/alert_events 行**字节不变**（只增不改） |
| Y4 | 在途 TriggerEvent | 事件携带触发时 rule 拷贝，回溯改写会造"半新半旧"执行 | 代码断言 + D4-Y4a/b | 本次执行严格用旧拷贝；下一帧用新内容；无 panic/race |
| Y5 | pending confirmation | 事件按 rule_id 存续；改规则不回溯已生成事件 | 代码断言 + D4-Y5a/b/c/d（含 TOCTOU 裁决） | 删除/超时 fail-closed；动作变更行为**显式裁决**（推荐快照比对拒绝） |

### Y1 planner DB 冷却锚（`automation_rules.last_triggered_at`）

- **代码断言**（CI 可跑）：
  - `grep -rn 'Update("last_triggered_at"' backend/internal --include=*.go | grep -v _test` ⇒ 期望恰好 4 处：`automation/planner.go:471`（自动 executed）、`:1055`（手动）、`:1184`（confirm）、`automation/evaluator.go:208`（启动回填）；**`backend/internal/api/` 下 0 处**（PUT/PATCH 不得写锚点）。
  - `grep -n 'First(&rule' backend/internal/automation/planner.go` ⇒ 仅 `:862`（手动 TriggerRule）与 `:1153`（ConfirmEvent）；自动路径 `HandleTrigger` 用 `ev.Rule`（:122-123）。
  - `grep -rn 'rebuildCooldowns' backend/internal --include=*.go | grep -v _test` ⇒ 仅 `NewEvaluator` 调用（启动恢复，不因内容变化重建）。
- **D4-Y1a（阈值/selector 变更）**：CooldownSec=300 规则触发一次 → `PUT /automation-rules/:id` 改阈值/selector → 立即再满足 → 期望事件 result=`suppressed_cooldown`（非 executed）；SQL 断言 `last_triggered_at` == 首次触发时刻（更新前后逐字节相同）。
- **D4-Y1b（换设备）**：规则绑 A 处于冷却 → PUT 改绑 B → B 首帧满足 → 期望仍 `suppressed_cooldown`（安全方向）；若产品裁决"换设备重置冷却"，则改为 PUT 时显式清锚点 + 反向测试"不得再 suppressed"，并在方案记录裁决。
- **D4-Y1c（重启）**：Y1a 场景后重启（`NewEvaluator` + `rebuildCooldowns`）→ 期望锚点恢复、冷却仍生效。
- **红判据**：内容更新导致 `last_triggered_at` 被清/改变，或不再落 `suppressed_cooldown` ⇒ **红**（绕过冷却，可能重复动作）。

### Y2 日配额 / 日熔断

- **代码断言**：配额计数来自 `automation_events`（`planner.go:129-141`，`triggered_at >= dayStart AND result=executed AND rule_id=?`），**不是** rule 字段/内存计数器；`notifyDailyLimitOnce` 按 (source, source_id, title) 幂等（`:683-720`）；`pending_confirm` 不占额度（F6，`:134`）。
- **D4-Y2a**：MaxDailyExec=3 + CooldownSec=0，执行 3 次 → PUT 改阈值/selector → 再触发 → 期望 `suppressed_daily_limit`，当日 `count(executed)=3` 不变。
- **D4-Y2b**：注入 nowFn 到次日再触发 → 期望恢复执行（配额按日切重置），且内容变化不影响日切口径。
- **D4-Y2c**：require_confirmed 规则连续触发产生多条 pending_confirm → 期望它们**不**消耗 MaxDailyExec；confirm 成功后才占额度；内容变化不改变此口径。
- **红判据**：改规则后配额被清零/绕过日熔断 ⇒ **红**。

### Y3 已发送通知 / 历史告警事件

- **代码断言**：`grep -rn 'Delete(&models.Notification\|Update.*Notification' backend/internal/api/handler_automation.go backend/internal/api/handler_alert.go backend/internal/automation --include=*.go | grep -v _test` ⇒ 期望 0 处（告警 evaluator 只 UPDATE `alert_events` 的 value/notified_at，`alert/evaluator.go:265,296`，不碰 notifications 历史行）。
- **D4-Y3a**：制造一条已发通知（如 F5 日熔断或 pending_confirm 通知）→ PUT 改规则 → 断言该 `notifications` 行全字段（id/title/source/source_id/created_at/read）不变、行数不减少。
- **D4-Y3b（F3 采纳后）**：firing 中告警规则改阈值 → 历史 firing 行 `alert_events` 不被 update/delete；**新增**一条 resolved（detail 含 `rule_updated`）；通知只增一条（如设计为通知）。
- **红判据**：改规则导致历史通知/告警事件被修改或删除 ⇒ **红**。

### Y4 在途 TriggerEvent

- **代码断言**：自动路径 `HandleTrigger` 全程用 `ev.Rule`（`planner.go:122-123`）不查 DB；读库白名单仅 `:862`/`:1153`（见 Y1）。
- **D4-Y4a**：注入可阻塞的 commandexec/fake handler，触发进入 planner 动作分发后阻塞；并发 PUT 改阈值/动作 + 触发 LoadRules；释放后断言：本次下发参数完全来自**触发时** rule 拷贝；`-race` 干净；下一帧按新内容执行。
- **D4-Y4b（规则被删）**：事件已入 planner 后删除规则 → 断言本次仍按旧拷贝执行（或按裁决改为不执行）；至少断言不会读到 nil/零值 rule、不 panic、事件记录完整。
- **红判据**：出现"部分字段新、部分旧"的执行、或 panic/数据竞争 ⇒ **红**。

### Y5 pending confirmation（含一个需 lead 裁决的现存 TOCTOU）

- **现状事实（已核代码）**：`ConfirmEvent` 重读 DB 规则并用**当前**动作集合执行（`planner.go:1153⟶ruleActions(rule)`，:1160/:1164 仅在 rule 缺失/action_type 非 device_action/动作空时 fail-closed）；`automation_events` **没有动作快照字段**（`models/automation.go:265-281`）。⇒ **pending 期间修改动作，confirm 会执行新动作**，而用户在确认 UI 看到的是旧动作（现存 TOCTOU，非本方案引入，但 D4 必须裁决）。
- **D4-Y5a（动作变更，二选一，须 lead 裁决）**：
  - 方案 A（现行为+文档化）：执行**当前**动作集合；测试锁定该行为，并断言 confirm 响应/事件 detail 不得声称执行了历史动作；方案 §9.2/§12 记录该 TOCTOU 与风险（安全上不推荐）。
  - 方案 B（推荐，安全）：pending 事件落动作快照（或 action hash 列），confirm 时比对，不一致 ⇒ `ErrConfirmActionChanged` 拒绝；断言旧动作零下发。
- **D4-Y5b（规则删除）**：pending 后删除规则 → confirm ⇒ `ErrConfirmRuleMissing`，零 `command_executions`。
- **D4-Y5c（超时）**：注入 nowFn +24h → `sweepExpiredPending` ⇒ result=`expired`；confirm ⇒ `ErrConfirmEventExpired`；内容变化不改变 TTL。
- **D4-Y5d（冷却锚一致）**：confirm 成功后 `last_triggered_at = ev.TriggeredAt`（非 now，`:1168`/`:1184`）；内容变化不改变该锚点。
- **红判据**：方案 A 未文档化 / 方案 B 未拒绝，或 confirm 无审计地执行了用户未确认的新动作 ⇒ **红**。

### 文档落位（与测试同等重要）

建议在方案新增一节（如 §9.2.X "内容变更不重置的运行时状态"）逐条写明：Y1 冷却锚、Y2 日配额、Y3 历史通知不可撤回、Y4 在途事件用触发时快照、Y5 待确认事件的 TOCTOU 裁决——并把本条 §F5-verdict 的 Y1–Y5 原样作为 D4 验收清单勾选项。

---

## D4 验收补充（auto-analyst 执行口径，2026-10-21 23:20；Lead 已批准措辞）

> 本节由 task-8 实现者追加，用于避免后续复核把"实现口径差异"误判为缺陷。F5 表中
> Y3b 的"新增一条 resolved（detail 含 rule_updated）"建议**未采纳**，理由是模型与既有语义约束（见下）。

| 项 | 最终口径 | 依据/回归锁 |
|---|---|---|
| **Y3b（偏离，已批准）** | 告警规则内容变更时，陈旧 firing **就地 update 为 resolved**（state/resolved_at/value 三列），**不新增 detail 列、不新增 resolved 行**；fired_at 与历史 notifications 逐字段不变；条件仍满足会重新 firing（可见、可接受） | `models.AlertEvent` 无 detail 列；既有语义是单行事件（`alert/evaluator.go` onResolve 就地 update）；新增行会改变"未恢复告警"计数与前端告警历史语义。回归锁：`backend/internal/alert/f5_y3b_test.go` |
| Y1a/Y1b/Y1c | 内容变更**不清** `last_triggered_at`；换设备保持冷却（安全方向）；重启由 `rebuildCooldowns` 从锚点恢复 | `automation/pg_y_cascade_test.go`（Y1a，PG 实跑）、`automation/f5_residue_test.go`（Y1b/Y1c） |
| Y2a/Y2b/Y2c | 内容变更**不重置**日配额；配额按日切恢复；pending_confirm 不占额度 | Y2a PG（pg_y_cascade_test.go）、Y2b/Y2c（f5_residue_test.go） |
| Y3a | 历史通知不可撤回（逐字段不变、行数不增不减） | f5_residue_test.go |
| Y4a/Y4b | 在途 TriggerEvent 严格用触发时 rule 拷贝（断言复核阈值来自拷贝而非 DB）；规则中途删除按旧拷贝执行、不 panic、留事件 | f5_residue_test.go |
| Y5a/Y5b/Y5c/Y5d | Y5 采用**方案 B**：pending 落 `action_hash`（归一化，忽略展示字段），confirm 不一致 ⇒ ErrConfirmActionChanged 且事件保持 pending；规则删除 ⇒ ErrConfirmRuleMissing；24h ⇒ expired + ErrConfirmEventExpired；成功锚点取 `ev.TriggeredAt` | Y5a PG（pg_y_cascade_test.go）、Y5b/Y5c（f5_residue_test.go）、Y5d 由既有 `cooldown_anchor_test.go` 覆盖（PG 全包已跑） |

**PG 假红（既有缺陷，非本批引入）**：`planner_multi_action_test.go` 原用 `Equal` 比
`last_triggered_at` 与内存 `ev.TriggeredAt`；PG timestamp 精度微秒、SQLite 纳秒 ⇒ PG 恒红。
已在两侧 `Truncate(time.Microsecond)` 后比较。该文件在本批之前最后改动为 `6cdb30e6`（main 同样存在）。

**-race 双模式实测（task-8 收口）**：
- SQLite：`go test ./internal/automation/ ./internal/alert/ -race -count=1` → ok 4.862s / 1.833s
- PG：`EHOME_TEST_DB=postgres ... -race -count=1`（全包）→ ok 62.987s / 1.817s

