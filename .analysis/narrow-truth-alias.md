# 窄复核 ②：216 格真值表闭合性 + 别名配置可加载性

> **复核人**: contract-reviewer（v0.1 评审 C1/C4 提出者）
> **对象**: `docs/设计/数据字段解析层-方案-2026-10-21.md` **v0.2**（848 行；下文 `方案:N` 指该文件行号）
> **冻结契约**: `backend/internal/sensorfield/{doc.go,types.go,errors.go}`（worktree `9354408a`，签名不得改）
> **交叉依据**: v0.1 评审报告（`docs/评审/数据字段解析层-评审报告-2026-10-21.md`）§6 二次复核项 1/3
> **方法**: 读 v0.2 §5.3/§6.1/§4/§12.1/§13.1 + 冻结契约 + 驱动声明/单元 + simulation 场景；用只读脚本（python，仓库零写入）生成 216 格矩阵并校验别名配置；未修改任何生产代码/文档，仅写本文件。
> **范围**: 只做窄复核（真值表 + 别名加载），不重跑全量评审。

---

## 0. 结论与统计

| 项 | 结论 |
|---|---|
| **216 格真值表** | **未完全闭合**。布尔列可唯一判定 **168/216**；**12 格自相矛盾**；**36 格依赖未定义前提（当前不可判定）** ⇒ 严格口径 **48/216 未闭合**。另有 **72 格**（any/all）布尔可定但 `EvalResult.Value` 无定义。 |
| **4 个原反例** | 3 个已按 v0.2 闭合（空集 sum、缺员 4→2 探头、NaN）；**1 个未闭合**（非数值/bitmask：类型来源未定义 + 冻结契约自相矛盾 + literal 作用域歧义）。**另发现 1 个新安全方向问题**：缺员策略对 `min/max` 单调方向过度抑制，会静默废掉保护型规则（见 §3.3）。 |
| **别名配置** | **可加载**：按 §6.1 规则脚本校验 —— 0 冲突、14/14 候选单位与 `expected_unit` 精确匹配、0 死候选（按 drivers 口径）。**但语义不闭环**：① `current` 的评审 H1 最坏场景**没有被修复**（单位校验对"同单位不同物理量"完全无效，techfine 同时产出充电/放电电流 ⇒ 冻结首候选=充电电流，放电过流保护仍失效）；② 删 `soc` 候选的判据"驱动不产出"口径不完整——simulation/ConfigParser 路径**确实产出物理名 `soc`**，删除后 `@soc` 在该类设备上永远 unbound。 |
| **窄复核结论** | **有条件通过**（4 条放行条件见 §6：Q1 空集语义二选一；Q2 非数值类型来源闭环；Q3 缺员策略方向性修订或风险显式接受；Q4 `current` 拆分 + `soc` 候选口径修正）。 |

---

## 1. 方法、基线与矩阵可执行性前提

- 真值表来源：方案 §5.3 定义 1–4（`方案:263-291`）+ §12.1 失败矩阵（`方案:598-614`）+ 冻结契约 `types.go`。
- 脚本校验（只读，未落盘）：从 `backend/internal/drivers/*.go` 提取 `{Name,Unit}` 对与动态族（`fmt.Sprintf("temperature_%d")` 等），从 `backend/simulation/catalog/scene.go` 提取 `sceneField`，按 §6.1 规则做冲突/单位/存在性校验与按机型歧义分析；矩阵由 §5.3 规则程序化求值。
- **矩阵可执行性前提（方案缺失，必须补进测试规格）**：216 格的期望值不是 `(aggregate, comparator)` 的函数，还依赖 ① `F_known` 具体成员 ② 本帧 `F_now` ③ 每个字段的 `value_type` ④ 阈值 `t`。方案 §5.3 只规定"6×6×6 逐格断言"，**没有规定每格必须钉死 F_known/F_now/type**——不钉死则同一格有两套正确结果（例如 S5 的 reducer 是 unbound 还是"对余下有效值归约"，取决于 NaN 字段是否早已在 known 集合中）。本复核为每格显式定义这些输入（§2.1），否则矩阵不可写。

---

## 2. 216 格真值表

### 2.1 图例与 6 个场景的显式输入

- `T`=触发（EvalResult.OK=true 且比较为真）；`F`=不触发（OK=true 且比较为假）；`U`=unbound（OK=false，不触发）；`U/F⚠`=**方案内部两种规定冲突**。
- 假设 A1：known 集合按**字段名**记录，与取值无关（`Observe(deviceID, name, unit, at)` 无 value 参数，`方案:164-176`；且 `FieldObs` 无 value 字段，`方案:140-141`）。
- 假设 A2（S6 专用）：该非数值字段的 `value_type` 已被正确标注为 `bitmask`（本假设**在方案中无实现来源**，见 §2.5）。

| 场景 | F_known | F_now（本帧） | t |
|---|---|---|---|
| S1 空集 | ∅ | ∅ | 40 |
| S2 单元素 | {temperature_1=40} | 同左（40） | 40 |
| S3 多元素齐全 | {temperature_1=40, temperature_2=50, temperature_3=60} | 同左 | 50 |
| S4 多元素缺员 | {temperature_1..4 = 40,50,60,70} | {1,2,3} = 40,50,60 | 50 |
| S5 含 NaN | {temperature_1=NaN, temperature_2=40} | 同左（NaN 为非有限 ⇒ 缺失） | 40 |
| S6 含非数值 | {status_bitmask=7(bitmask), temperature_2=40} | 同左（bitmask 为非数值 ⇒ 缺失） | 40 |

### 2.2 逐格期望值（布尔结果）

**S1 空集（t=40）**

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ |
| all | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ | U/F⚠ |
| min | U | U | U | U | U | U |
| max | U | U | U | U | U | U |
| avg | U | U | U | U | U | U |
| sum | U | U | U | U | U | U |

**S2 单元素（t=40）**

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any | F | T | F | T | T | F |
| all | F | T | F | T | T | F |
| min | F | T | F | T | T | F |
| max | F | T | F | T | T | F |
| avg | F | T | F | T | T | F |
| sum | F | T | F | T | T | F |

**S3 多元素齐全（40,50,60；t=50）**

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any | T | T | T | T | T | T |
| all | F | F | F | F | F | F |
| min=40 | F | F | T | T | F | T |
| max=60 | T | T | F | F | F | T |
| avg=50 | F | T | F | T | T | F |
| sum=150 | T | T | F | F | F | T |

**S4 多元素缺员（F_known 4、本帧 3；t=50）**

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any（不受缺员影响） | T | T | T | T | T | T |
| all | U | U | U | U | U | U |
| min | U | U | U | U | U | U |
| max | U | U | U | U | U | U |
| avg | U | U | U | U | U | U |
| sum | U | U | U | U | U | U |

**S5 含 NaN（NaN 缺失；余 40；t=40）**

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any（只用有效值 40） | F | T | F | T | T | F |
| all | U（缺员） | U | U | U | U | U |
| min/max/avg/sum | U | U | U | U | U | U |

**S6 含非数值（bitmask 缺失；余 40；t=40）** —— **见 §2.5：36 格当前不可判定**，下表是"若类型来源成立"的暂定值

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any（只用有效值 40） | F | T | F | T | T | F |
| all | U | U | U | U | U | U |
| min/max/avg/sum | U | U | U | U | U | U |

### 2.3 统计

| 分类 | 格数 | 说明 |
|---|---|---|
| T | 48 | 可判定 |
| F | 42 | 可判定 |
| U | 114 | 可判定 |
| **U/F⚠（自相矛盾）** | **12** | S1 的 any/all × 6 比较符 |
| **不可判定（严格口径）** | **36** | S6 全 36 格（类型来源未定义；暂定值仅为假设 A2 的结果） |
| **未闭合合计（严格）** | **48** | 12 矛盾 + 36 不可判定；其余 168 格在显式钉死 F_known/F_now/value_type 后可唯一判定 |
| 附加：布尔已定但 `Value` 无定义 | 72 | any/all × 6 比较符 × 6 场景（见 §4-G5） |

### 2.4 12 格自相矛盾：空集的 `any`/`all`（S1）

- **方案 A（`方案:276-277`，定义 3）**："`F_now = ∅` ⇒ **全部聚合都判 unbound（不触发）**，包括 `sum`"。
- **方案 B（`方案:283-284`，定义 4 表）**：`any` 备注"空集 **false**"；`all` 备注"空集 false（不采用空真）"。
- **第三方佐证 A**：§12.1 失败矩阵"空集 ⇒ **全部 unbound**"（`方案:607`）与定义 3 一致，进一步说明定义 4 的"false"是漏改残留。
- **影响不只是文档措辞**：`unbound` 与"比较为 false"在冻结契约里是不同结果 —— `EvalResult{OK:false, Reason:"..."}` vs `{OK:true, Value:...}`；两者都"不触发"，但 ① `Value` 是否可被窗口/事件/复核读取 ② `Reason` 是否落审计 ③ 指标计数（`empty_family`）不同 ③ 单元测试断言字段不同。测试若按定义 4 写 `OK=true` 会与 §12.1 冲突。
- **该矛盾在真实设备上不是仅 S1**：`F_known ≠ ∅ 且 F_now = ∅` 同样触发（定义 2 声明此时 reducer 一律 unbound，但 `any` "不受影响" ⇒ 定义 4 的"空集 false"）。多命令设备（techfine 每帧只来自一条命令：`inverter_techfine_registry.go` 的 HBAT/HTEMP 分离）会对温度规则频繁出现这种帧。
- **修法（必须二选一，建议 A）**：把定义 4 表 `any/all` 的"空集 false"改为"空集 **unbound**"，并删掉 `不采用空真` 的括号备注；同时在 §13.1 的 216 格测试注释中写明"空集一律 OK=false"。若坚持 `any` 空集为 false，则必须同步改定义 3 与 §12.1，并说明 `all` 为何与 `any` 不同（当前无理由）。

### 2.5 36 格不可判定：非数值（S6）的类型来源在方案与冻结契约中都不存在

三个互相独立的问题，任一未裁决，S6 的 36 格就无法写成可执行断言：

1. **数据面拿不到 `value_type`**。冻结 `Plan.Eval(frame []Reading)`（`types.go:112-117`）只收到 `Reading{Name,Value,Unit}`（`types.go:76-80`），没有类型。若在编译期把"已知非数值字段名集合"嵌进 Plan，则来源只能是：`FieldDecl.ValueType` 或观测。但 ① 观测表 `sensor_field_obs` 列只有 `device_id/sensor_name/unit/first_seen/last_seen/samples`（`方案:150-162`），**没有 value_type**；`FieldObs` 结构体同样没有（`方案:141`）；② 丰富层 `sensor_field_meta` 的 `value_type` 被明确限定"唯一消费者是展示 API"（`方案:196`，同 v0.1 `§4.3`）⇒ 解析层不能读；③ `FieldDecl.ValueType`（`方案:140`）没有生产者——驱动 `SensorData` 只有 `Name/Value/Unit/StringValue`（`drivers/registry.go`），`GetSensorDefinitions()` 也只返回 Name+Unit（`jiabaida.go:24-40`、`inverter_techfine.go:218+`）；`bitmask` 类型目前**只以 unit 字符串 "bitmask" 存在**（`jiabaida.go:33-34`：`protection_status/fet_status` Unit="bitmask"）。方案没有定义 `unit→ValueType` 的映射，也没有把 value_type 加进 obs 表。
2. **冻结契约自身两处注释互相矛盾**：`types.go:44` "只有 TypeNumber 参与数值比较；其它类型视为'本帧缺失'" **vs** `types.go:59` "单位与类型只用于展示/校验，**不参与"是否命中"的判断**"。两者对同一字段给出相反行为，且实现者不能改冻结契约。
3. **`TypeUnknown`（空）默认归属未定义**：真实绝大多数字段来自驱动声明/观测，`ValueType` 为空。若按 `types.go:44` 字面实现（"非 TypeNumber ⇒ 缺失"），则**所有未标注字段（含绝大多数温度/电压/电流）都变成缺失** ⇒ 聚合全部 unbound，整个方案失效；若按"空=number"实现，则 S6 的 bitmask 判定也依赖外部标注。两种实现都得不到 §5.3 声称的行为。

**修法（任一，需 lead 决策，因为涉及冻结契约）**：
- 推荐：在 `sensor_field_obs` 增 `value_type VARCHAR(16)`（或解析层使用 `unit→ValueType` 受控映射，把 `bitmask/ack/state/raw` 映射为非数值），并统一 `types.go:44/59` 两处注释（建议改为"TypeNumber 与非空 TypeUnknown 参与数值比较；其余视为本帧缺失"），再补 S6 的 36 格断言。
- 退而求其次：S6 场景从 216 格中剔除（改为 180 格），并在文档明确"非数值字段仅展示、不参与 selector 求值"，同时删掉 §12.1 "非数值 value_type 参与比较 ⇒ 视为缺失"的承诺或改为"仅当丰富层标注可用时"。

### 2.6 反证：S6 若按"bitmask 参与数值比较"则结果完全不同（说明该 36 格不是无关紧要的口径）

S6 变异（bitmask=7 与 40 都参与，t=40）：

| agg | gt | gte | lt | lte | eq | neq |
|---|---|---|---|---|---|---|
| any | F | T | T | T | T | T |
| all | F | F | F | T | F | F |
| min=7 | F | F | T | T | F | T |
| max=40 | F | T | F | T | T | F |
| avg=23.5 | F | F | T | T | F | T |
| sum=47 | T | T | F | F | F | T |

与 §2.2 的 S6（reducer 全 U、any 只余 40）有 30 格不同。**同一帧、同一规则，两种实现给出完全不同的触发结果**——所以这 36 格必须先裁决类型来源，不能留给实现者自由发挥。

### 2.7 S5 的"闭合"依赖 F_known 显式钉死（矩阵规格要求，非方案缺陷但需写死）

`Observe` 无 value 参数 ⇒ known 集合按名字记录，与取值无关；但"该 NaN 字段是否已在此前某帧被观测到/是否被声明"决定它是否属于 `F_known`：属于 ⇒ 缺员 ⇒ reducer unbound；不属于 ⇒ 不触发缺员 ⇒ reducer 对余下有效值归约（数值同 S2 的单元素结果）。**同一测试名"含 NaN"可得两套正确结果**。因此 216 格规格必须给每格显式 `F_known`（§7 给出建议字段）。这不是语义错误，是"测试矩阵不可执行"的风险。

---

## 3. 对 v0.1 四个原反例的逐条核对

| # | 原反例 | v0.2 规定 | 复核结果 | 状态 |
|---|---|---|---|---|
| 1 | 空集 `sum`（`sum<t` 反向触发） | 定义 3：空集全部聚合 unbound（`方案:276-277`） | 通过：S1 的 reducer 24 格全部 U，`sum=0` 陷阱已消除。**但同一节定义的 any/all "空集 false" 新引入 12 格矛盾（§2.4）** | ✅ 基本闭合（± 12 格矛盾） |
| 2 | `F={NaN,3}`，`min<5` 与 `any<5` 的"等价" | 定义 1：NaN 视为缺失；缺员 ⇒ reducer unbound；`any` 只用有效值 | 通过：`min<5` ⇒ unbound（缺员），`any<5` ⇒ T（3<5）；v0.1 的"min↔any 等价"声称已删除，不再有数学反例 | ✅ 闭合 |
| 3 | 已知 4 探头、本帧 2 探头 | 定义 2：缺员 ⇒ all/min/max/avg/sum unbound，any 不受影响 | 规格上通过（S4：any 6 格 T，其余 30 格 U）。**但发现安全方向缺陷**（§3.3）：`max>t`/`min<t` 这类单调方向在部分帧上可**安全**触发（若部分集 max>t，则真实 max>t 必然成立），方案一律 unbound 会**静默抑制保护**；JBD `ntcCount` 随帧变化是真实场景（`jiabaida_parse.go:249/265-268`、`357/374-377`） | ⚠️ 已定义但方向性可疑 |
| 4 | `bitmask` 参与比较 | 定义 1：非数值 ⇒ 缺失；§12.1 同（`方案:609`） | 未闭合：① 类型来源不存在（§2.5）；② 冻结契约 `types.go:44` vs `:59` 自相矛盾；③ 定义 1 的作用域未写：若适用于 literal，则与 §5.2 "literal 与今天 `matchField` 逐字节相同"冲突（今天 `matchField` 不过滤 NaN/类型，`automation/evaluator.go:884-891`、`alert/evaluator.go:401-407`；G-6 要求逐字节不变）。仓库现状：未发现现存 bitmask 规则目标（`grep` simulation/tests：0），故不是存量回归，但契约二义必须裁决 | ❌ 未闭合 |

### 3.3 新发现：缺员策略对 `min/max` 过度抑制（fail-open 方向）

定义 2 对 `all/min/max/avg/sum` 一律 unbound，但按比较符的**单调方向**，部分帧上仍可安全判定：

- `max` + `gt/gte`：若 `max(F_now) > t`，则 `max(F_known) ≥ max(F_now) > t` **必然成立** ⇒ 在 F_now 上求值不会误触发，只会漏掉"缺失字段才是最大者"的情形（那本就是无法判定的）。
- `min` + `lt/lte`：对偶，同理可安全求值。
- 其余组合（`all`、`max` 的 `lt/lte`、`min` 的 `gt/gte`、`avg`、`sum`、reducer+`eq/neq`）确实不可判定 ⇒ unbound 正确。

当前 v0.2 把上述 4 个"可安全求值"的组合也判 unbound。后果：**一个探头缺失/NaN 会让 `temperature_*:max > 45` 的过温保护整帧失效**（BMS 里 NTC 数量随帧变化是固件已知行为），方向是"保护静默失效"——与方案自己"防误动"的动机相反。建议：
- 方案 §5.3 增加"方向性缺员语义"表：`max+gt/gte`、`min+lt/lte` **允许在 F_now 上求值**（附 soundness 证明），其余 unbound；
- 或者明确接受该风险，但必须在 §12.1/§16 记为风险项，并在 `partial_frame` 指标上配告警（尤其是保护型规则），且 §13 加"单探头缺失时 max>t 是否仍触发"的**显式**用例（当前 §13.1 只要求"全部 unbound"，等于把错误行为钉成期望）。

---

## 4. 216 格矩阵之外仍缺定义的场景（建议补进 §5.3/§13.1）

| ID | 缺口 | 证据/影响 | 建议 |
|---|---|---|---|
| G1 | `F_now ⊆ F_known` 不变量与现实冲突 | 定义 2 断言子集关系，但观测投影固定滞后 `observe_interval`=60s（`方案:571/575`）：设备新字段首次出现的那一帧，`F_now` 含 `F_known` 没有的字段 | 定义 `F_now := frame ∩ F_known`（并暴露"新字段未入 known"指标），或允许从本帧扩充 known；矩阵加 S7 场景（36 格） |
| G2 | `F_known≠∅ 且 F_now=∅` 未单列 | 多命令设备（techfine HBAT/HTEMP 帧分离）每帧只含一条命令字段 ⇒ 温度规则的 `F_now` 常为空 | 并入矩阵（同 S1 矛盾；any 的 unbound/false 必须先裁决） |
| G3 | `max_family_size` 截断语义 | §13.1 要求测"max_family_size 截断策略"，但 §5.2/§5.3 **没有定义**截断成员选择/顺序/是否报错；100 芯 BMS 的 `cell_voltage_*` 会超 64 | 定义：按数字后缀升序保留前 N 个 + `family_truncated` 指标 + 保护型规则告警（否则 `max` 会被静默算错） |
| G4 | 帧内同名字段重复 | literal 沿用"取第一个"（§5.2/§7.2），family/reducer 对重复名是累加还是去重未定义（`device_configs.parser.fields` 可由用户写出重复 name） | 明确 `F_now` 为集合（按名去重）或数组；矩阵加重复场景 |
| G5 | `EvalResult.Value` 对 any/all 无定义 | 冻结 `EvalResult{Value,OK,Reason}`（`types.go:99-104`）：reducer 的 Value 明确（归约值），但 any/all 的 Value 应取什么（首个命中值？0？t？）没有规定；unbound 时 Value 是否可信、`Reason` 字符串是否枚举都未定义 | 补契约：any/all 返回"首个满足字段的值"或 0+OK；unbound 的 Value 明确"未定义、忽略"；Reason 用常量枚举（`partial_frame/empty_family/non_numeric/...`），审计依赖它 |
| G6 | 冻结 `Plan` 接口无法在 Eval 时拿到 F_known | `Plan.Eval(frame []Reading)`（`types.go:116`）只有帧；缺员判定需要编译期已知集合 ⇒ 实现必须把 `F_known` 快照嵌进 Plan，但 §7.2 只描述"编译产物=matchField 等价匹配器"，未写这一点 | 在 §7.2/契约注释写死"Plan 编译期捕获 F_known（该 epoch 快照）；epoch 变化必须重编译"，并加"known 变化后下一次 Eval 用新 F_known"的单测 |
| G7 | 定义 1 的作用域（literal 是否也过滤非有限/非数值） | §5.2 承诺 literal 与今天 `matchField` 逐字节相同（今天不过滤任何值：`automation/evaluator.go:884-891`）；§5.3 定义 1 是"某字段的取值…"的通用表述 | 明确"定义 1 仅适用于 family/semantic/virtual 的聚合校验；literal 保持历史行为（原样返回，含 NaN）"或反向修订 G-6 验收 |

---

## 5. 别名配置可加载性（§6.1）

### 5.1 配置（按 §6.1 原样，作为校验输入）

```yaml
soc:            { expected_unit: "%",   candidates: [rsoc, battery_capacity, bms_soc] }
total_voltage:  { expected_unit: "V",   candidates: [total_voltage, battery_voltage, bus_voltage] }
current:        { expected_unit: "A",   candidates: [current, battery_charge_current, battery_discharge_current] }
dc_power:       { expected_unit: "W",   candidates: [output_active_power] }
max_temp:       { expected_unit: "°C",  candidates: [max_temp, inverter_temp, transformer_temp] }
temperature:    { expected_unit: "°C",  candidates: [temperature_1] }
```

### 5.2 按方案自身规则（§6.1 规则 2/3）的校验结果

| 校验 | 结果 | 证据 |
|---|---|---|
| 同一物理名出现在多个语义名下（且无 allow_overlap） | **0 处冲突** | 脚本枚举 14 个候选，无重复归属；`temperature_1` 只属 `temperature`（v0.1 的 C4 冲突已修） |
| 候选 unit 与 expected_unit 不同类 | **0 处不符**（精确匹配） | 逐项实测驱动产出单位：rsoc `%`（`jiabaida_parse.go:257/362`）、battery_capacity `%`（`inverter_techfine.go:241`）、bms_soc `%`（`:289`）；total_voltage/battery_voltage/bus_voltage `V`；current/battery_charge_current/battery_discharge_current `A`；output_active_power `W`；max_temp/inverter_temp/transformer_temp `°C`（`inverter_techfine.go:218-240`）；temperature_1 `°C`（`jiabaida_parse.go:265-268` 动态名 + 单位字面量） |
| 候选是否存在生产者 | **14/14 由 `internal/drivers` 产出**（含动态 `temperature_%d`） | `grep -rhoE '{Name: "...", Unit: "..."}'` 全量提取 |
| 死候选（v0.1 的 `soc`/`battery_soc`/`bms_power`） | `battery_soc`/`bms_power` 全仓确实无产出 ✅；**`soc` 的删除判据不成立**（见 5.3-②） | `backend/simulation/catalog/scene.go:760/855/979` 产出 `soc` |
| §6.1 与 §18.1 合并（单一来源） | `§18.1` 已是"见 §6.1"（`方案:774`）✅；但 **§8.4 的 /schema 响应示例里仍有一份不同的 alias 列表**（`方案:456`：`soc` 候选缺 `bms_soc`）——文档内第三处副本 | 建议 /schema 由加载后的配置生成 + 快照测试，删示例硬编码 |

**加载判定：按 §6.1 规则可加载（0 冲突 / 0 单位不符 / 无缺候选）。**

### 5.3 可加载 ≠ 语义正确：两个必须修的问题

**① `current` 的 H1 最坏场景没有被修复（评审报告 H1 的处置无效）。**
- §6.1 规则 3 声称单位类校验"防的是 @current 在 Techfine 上只解析到充电电流而漏掉放电电流 ⇒ 过流保护失效"（`方案:326`）。但 `battery_charge_current` 与 `battery_discharge_current` **单位都是 A，单位校验必然通过**——该防护对目标场景完全无效。
- 实测 techfine 的 `GetSensorDefinitions()` 同时声明两者（`inverter_techfine.go:242-243`），且同一 HBAT 帧同时产出（`inverter_techfine.go:742-743`）。按 §5.4 规则 3：多候选同时存在 ⇒ `ambiguous=true` + 通知，**冻结首候选 = `battery_charge_current`**（候选序 `current → battery_charge_current → battery_discharge_current`）。于是 `@current` 在 Techfine 上永远只代表**充电电流**，放电过流/反向电流保护规则仍然静默失效——与 v0.1 评审指出的最坏场景**完全一样**。
- 另外 JBD 的 `current` 是**带符号充放电电流**（`jiabaida_parse.go:239-240`：`int16` 有符号解析），与 Techfine 的"单向充电电流"语义不同 ⇒ 同一 `@current` 规则跨厂商不可移植（G-2 不成立），这是单位校验无法表达的。
- **建议**：删除 `current` 语义，拆为 `charge_current` / `discharge_current`（各自单候选），需要"净电流"时用派生字段（`virtual:net_current = charge − discharge`，并在 §6.2 定义符号）；或把候选限定为同一物理量语义并增加 `role`/`sign` 元数据。

**② 删除 `soc` 候选的判据口径不完整。**
- §6.1 规则 5 依据"驱动不产出"删掉 `soc`（`方案:328`）。但"物理名产生路径"不止 `internal/drivers`：`device_configs.parser.fields[].name` 是**无校验自由字符串**（`backend/pkg/parser/parser.go:41-42`，本方案 §4.4 自己也承认），且仓库的 **simulation 场景框架正是通过 DeviceConfig.Parser 生产物理名**：`sceneField` 直接对应 `DeviceConfig.Parser.fields`（`scene.go:135-145`、`253-270`），多个场景道具设备声明 `Name: "soc"`（`scene.go:760/855/979/1383`），对应既有规则也用 `"soc"` 字面量（`scene.go:866/880/1007`）。
- 后果：`@soc` 在仿真设备/任何用 ConfigParser 的厂商设备（字段名为 `soc`）上 **unbound**；而 `@soc` 是方案的主示例（§1.2 G-2、§18.2），SIM-FIELD 跨厂商标证也会在此类设备上失败。
- **建议**：恢复 `soc` 候选（排在 `rsoc` 之后即可，rsoc 优先），并把"死候选"判据改为"全仓物理名产生路径并集（drivers ∪ device_configs.parser ∪ simulation/scene）"，或明确声明候选集只覆盖 drivers 且 ConfigParser 设备走"字面量"。

### 5.4 按机型歧义与冻结结果（脚本实测；§5.4 规则 3 的运行期行为）

已知集合 = 该机型 `GetSensorDefinitions()` 声明 ∪ 动态族 ∪ 生产观测（JBD 观测含 `temperature_1/2` 等 13 个字段）。

| 语义 | jiabaida_bms（BMS） | techfine_inverter（逆变器，唯一逆变器驱动） | 仿真/ConfigParser 设备 |
|---|---|---|---|
| `@soc` | rsoc（唯一，无歧义） | **battery_capacity + bms_soc ⇒ 歧义**；冻结=`battery_capacity` | 无候选（除非恢复 soc） |
| `@total_voltage` | total_voltage（唯一） | **battery_voltage + bus_voltage ⇒ 歧义**；冻结=`battery_voltage` | 无候选 |
| `@current` | current（唯一） | **charge + discharge ⇒ 歧义**；冻结=`battery_charge_current`（**语义错误，见 5.3-①**） | 无候选 |
| `@dc_power` | 无候选（unbound，符合预期） | output_active_power（唯一） | 无候选 |
| `@max_temp` | 无候选（unbound；JBD 无 max_temp） | **max_temp + inverter_temp + transformer_temp ⇒ 歧义**；冻结=`max_temp`（语义可接受） | 无候选 |
| `@temperature` | temperature_1（唯一；但"温度"只等于 1 号探头，语义窄） | **无候选 ⇒ 永远 unbound**（techfine 不产 temperature_1） | 无候选 |

结论：**6 条别名中 4 条在唯一真实逆变器机型上触发"多候选歧义"路径**（每次首解析一条通知 + 冻结）。歧义本身按方案规则是"允许 + 可观测"，但其中 `current` 的冻结结果语义错误；`total_voltage` 的 `bus_voltage` 候选是"同单位不同物理量"（DC 母线电压 vs 电池总压，单位校验不可见），在只产 bus_voltage 的机型上会把母线电压当电池总压（v0.1 同类风险仍在）。建议：别名候选加 `value_range` 或 `role` 约束（如 total_voltage 候选限定电池侧量），并在 /schema 暴露"该设备将冻结到哪个物理名"，让规则编辑器提示。

---

## 6. 放行条件（窄复核视角）

| # | 条件 | 验收判据 |
|---|---|---|
| **Q1** | **空集语义二选一**：推荐把 §5.3 定义 4 的 any/all "空集 false"改为"unbound"，与定义 3、§12.1 一致；同步 §13.1 测试断言 | g|rep 全文无"空集 false"；216 格中 S1 不再有 U/F⚠；§13 有空集 any/all 的 OK=false 断言 |
| **Q2** | **非数值类型来源闭环**（涉及冻结契约，需 lead 决策）：obs 表增 value_type 或采用 unit→ValueType 受控映射；统一 `types.go:44/59` 两处注释；明确 TypeUnknown 默认 | 能构造出 S6 的 36 格期望值并断言；`go test ./internal/sensorfield` 中 S6 用例可运行；一段"未标注字段仍按 number 比较"的回归用例 |
| **Q3** | **缺员策略方向性修订**（或显式接受风险）：`max+gt/gte`、`min+lt/lte` 允许在 F_now 上求值（附 soundness 说明），其余维持 unbound；若接受风险则 §12.1/§16 登记并在 partial_frame 上配告警 | §13 增"单探头缺失时 max>45 仍触发（保护不被抑制）"用例；§5.3 出现方向性表 |
| **Q4** | **别名语义修订**：`current` 拆分或加符号/角色约束（否则 H1 未修复）；恢复 `soc` 候选或明确物理名口径；§8.4 schema 示例改为"由配置生成" | 别名表可加载（已成立）+ 新增"techfine @current 不得只解析到充电电流"的加载/解析测试；SIM-FIELD 场景中 @soc 在 ConfigParser 设备上可命中 |

> 其余 168 格在 Q1/Q2 解决后可作为 §13.1 的 216 格验收基线；§4 的 G1–G7 建议作为 v0.3 的必要补充（不阻塞本次窄复核，但 G1/G3/G5/G6 影响实现正确性，建议随 Q2 一并写入）。

---

## 7. 附：216 格测试矩阵的执行规格（建议直接落 §13.1）

每格断言应显式携带（否则 §2.7/§2.1 的不可判定问题会复现）：

```
scenario           // S1..S6（建议补 S7 F_now⊄F_known、S8 F_known≠∅∧F_now=∅、S9 重复名、S10 max_family_size 溢出）
known_fields[]     // F_known：name, value_type, 是否声明/观测来源
frame[]            // 本帧 Reading（name, value, unit）
threshold
aggregate          // any|all|min|max|avg|sum
comparator         // gt|gte|lt|lte|eq|neq
expected_ok        // bool（U=false；⚠ 两解时必须先裁决）
expected_value     // reducer 必有；any/all 需先用 Q2 定义（见 §4-G5）
expected_reason    // unbound 原因的枚举常量（partial_frame/empty_family/non_numeric/...）
```

断言方式建议：表驱动 `TestTruthTable216` 用上述结构逐格 `t.Run`；另加 `TestTruthTable_S6_TypeUnknown`（验证未标注字段仍按 number 处理）与 `TestTruthTable_EmptyAnyAll`（验证 Q1 裁决）两条专门的"裁决回归"，防止后续再漂移。

---

## 8. 复核方法留档（可复跑）

1. 冻结契约：`read backend/internal/sensorfield/{doc.go,types.go,errors.go}`；`types.go:44/59` 与 `:112-117` 为关键行。
2. 驱动单位：`grep -rhoE '\{Name: "[a-zA-Z0-9_]+"[^}]*Unit: "[^"]*"' backend/internal/drivers/*.go`。
3. 声明集合：`jiabaida.go:24-40`、`inverter_techfine.go:218-260`。
4. 动态族：`jiabaida_parse.go:265-268/302/374-377/388-391`。
5. simulation/ConfigParser 的 `soc`：`scene.go:135-145/253-270/760/855/979/1383` 与规则 `:866/880/1007`。
6. 矩阵与别名校验脚本：只读 python（未落盘、未改仓库），逻辑见 §2.1/§5.2；可在 CI 里以 Go 表驱动复现。

---

## 9. 复核基线说明与并发上游变更（v0.3 工作树）

- 本复核针对 **v0.2**（commit `9354408a`，848 行）执行；复核期间 worktree 出现了**未提交的 v0.3 修订**（`§7.2 / §7.2.1 / §7.2.2 / §20`，见 `git diff`）。
- **逐字核对：§5.3 定义 1–4 与 §6.1 别名表在 v0.3 工作树中未变** ⇒ 本文 §2–§6 的全部结论（Q1–Q4、48/216 未闭合、别名两缺陷）**仍然适用**，不因 v0.3 作废。
- v0.3 §7.2.2 新增 `EvalPredicate/CmpOp/Verdict`，部分缓解 G5（Value/Reason 有了承载结构），但：
  1. 第 423 行仍写 "`family all：∀（空集 false；缺员/未知成员 ⇒ unbound）`"——把 §5.3 定义 3 "空集全部 unbound" 的**矛盾复制进新契约说明**（Q1 未解决）；
  2. 同段第 424 行 "`Plan.Eval 对 family any/all 返回 unbound`" 与第 423 行 "all 空集 false" **在同一 bullet list 内直接冲突**（同一 v0.3 新增段落自相矛盾）；
  3. `EvalPredicate(p, frame, op, threshold)` **无 F_known / value_type 入参** ⇒ G6（Plan 必须编译期嵌入 known 快照）与 Q2（非数值类型来源）仍未定义；
  4. any/all 的 `Verdict.Value` 语义仍未定义（G5 只给了字段，没规定填什么）。
- 建议 lead 把 §20 的 `N-3/N-4`（本文件）回填为本文 §6 的 Q1–Q4，并在 v0.3 的 §5.3/§7.2.2 一并修掉空集矛盾。

