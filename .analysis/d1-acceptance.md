# D1 验收证据：sensorfield 领域包（门禁 #2/#4/#5/#8/#9/#10）

> **状态**: 证据就绪，交窄复核（reviewer 独立重跑即可复现）
> **分支/worktree**: `field-resolver` @ `.agents/worktrees/field-resolver`
> **生成**: 2026-10-21 22:30 (+08:00)；HEAD = `ef9b99b5`（含 impl-core 6 个 sensorfield 提交）
> **工具链**: go1.27.1 linux/amd64；`GOCACHE=/tmp/eh-wt-cache`
> **对象**: `backend/internal/sensorfield/`（生产 8 文件 + 测试 12 文件，5621 行，覆盖 90.1%）
>
> 所有输出均为**实跑粘贴**，未做美化；复现命令按节给出。证据脚本：
> `.analysis/tools/field_scan.go`（门禁 #4/#5 AST 扫描）、`.analysis/tools/mutation_check.sh`（门禁 #9 变异自证）、
> `backend/internal/sensorfield/bench_baseline_test.go`（门禁 #10 的 main 基线 bench 克隆）。

## 结论汇总

| 门禁 | 判据 | 实测 | 判定 |
|---|---|---|---|
| #2 反向依赖 | `go list -deps ./internal/sensorfield` 不含 api/automation/alert/datasource/databus/models/drivers；只含 pkg/logger | 唯一 ehome 依赖 = `ehome/backend/pkg/logger`；反向/databus 均为空 | **PASS** |
| #4 声明自洽 | 声明并集 ⊆ 产出可达集（字面量 ∪ Sprintf 族），分母由脚本推导 | declared=91，unreachable=**0** | **PASS** |
| #5 产出可寻址 | 所有 `SensorData{Name:...}` 匹配 `^[a-z][a-z0-9_]*$`；`*_ack` 例外清单 | 产出字面量 104，非法 0，`*_ack` 例外 6（与评审口径一致） | **PASS** |
| #8 语法表驱动 | §5.1 全 15 条边界有测试、且真跑（`=== RUN` 计数非假绿） | 39 个 `=== RUN`（1 父 + 38 子，15 边界逐条命中） | **PASS** |
| #9 变异自证 | 6 类关键语义逐条改坏 ⇒ 对应测试必红 ⇒ 恢复后 md5 一致 | **7/7 CAUGHT**（含新增 virtual sum_of 变异）；restore md5 全一致；复跑全绿 | **PASS** |
| #10 性能 | literal ≤ 今天 +5% | 新层 literal Plan.Eval 3.63ns/0 alloc vs 基线 matchField 0.69ns（**+428%**）；但**生产链路 literal 保持 legacy matchField**（集成现状），线上回归 **0%** | **需裁决口径**（详见 §5） |

**需窄复核注意**：门禁 #10 是本表唯一不能简单判 PASS 的项。按门禁字面（新抽象层 literal 路径）不达标；按生产求值链路（用户可感知回归）为 0。建议判定按生产链路口径 PASS，并把新层抽象成本记为观测值（绝对值 3.63ns，0 分配）。

---

## 1. 门禁 #2：反向依赖 / 数据面零 import

```bash
cd backend
go list -deps ./internal/sensorfield | grep 'ehome/'
echo '--- reverse (forbidden) ---'
go list -deps ./internal/sensorfield | grep -E 'ehome/backend/internal/(api|automation|alert|datasource|databus|models|drivers)' || echo 'NONE'
echo '--- databus -> sensorfield? ---'
go list -deps ./internal/databus | grep sensorfield || echo 'NONE'
```

实测输出：

```
ehome/backend/pkg/logger
ehome/backend/internal/sensorfield
--- reverse (forbidden) ---
NONE
--- databus -> sensorfield? ---
NONE
```

**判定 PASS**。说明：
- 与方案 §3.2 的“领域包依赖 models/drivers/metrics”不同，实现遵循 Lead 评审红线“本包不依赖任何 internal 业务包”，只依赖 `pkg/logger` + `prometheus/client_golang`（external，已在 go.mod）。声明/观测由调用方注入（`RegisterDeclarations`/`SetObservations`/`SetFieldMeta`）。
- §10 九指标的 promauto 定义在包内 `metrics.go`（与 `pkg/metrics` 同范式），因此**不需要** import `pkg/metrics`；也避免与 pkg/metrics 重复注册（init panic）。
- `internal/databus` 无 sensorfield 依赖（数据面零 import，门禁 #1 的对应项）。

---

## 2. 门禁 #4/#5：声明并集 vs 产出可达（AST 推导，不写死分母）

### 2.1 口径

脚本 `.analysis/tools/field_scan.go`（仅 stdlib `go/ast`，只读）：
- **声明并集** = 所有 `GetSensorDefinitions()` 方法体里的 `{Name: "..."}` 字面量；
- **产出字面量** = 任意 `SensorData{Name: ...}` / `[]SensorData{{Name: ...}}` 复合字面量；
- **动态族** = `fmt.Sprintf` 格式串严格匹配 `^([a-z][a-z0-9_]*_)%d$` 的前缀（族成员视为可达，不逐个枚举——数据语义规范 §2.2）。

### 2.2 复现命令

```bash
# 在 worktree 根目录
go run .analysis/tools/field_scan.go backend/internal/drivers
```

### 2.3 实测输出

```
=== SCOPE ===
dir=backend/internal/drivers go_files=13
=== COUNTS (分母) ===
declared_union=91 output_literal_union=104 dynamic_family_patterns=4
declared_and_output=91 outputs_not_declared=13
=== GATE #4 声明 ⊆ 产出可达（字面量 ∪ Sprintf 族）===
declared_unreachable=0 []
=== 产出未声明（信息项，不是门禁失败）===
undeclared_outputs=13 [charge_mos_test_status custom_attr_1 custom_attr_2 custom_attr_3 discharge_mos_test_status hardware_version mos_ack reboot_ack reset_ack serial_number set_ack test_ack write_ack]
=== GATE #5 产出可寻址（物理名文法 + *_ack 例外）===
declaration_invalid_grammar=0 []
output_invalid_grammar=0 []
ack_pseudo_quantities=6 [mos_ack reboot_ack reset_ack set_ack test_ack write_ack]
=== DYNAMIC FAMILIES (Sprintf %d) ===
family=cell_resistance_* files=[backend/internal/drivers/jiabaida_parse.go]
family=cell_voltage_* files=[backend/internal/drivers/jiabaida_parse.go]
family=resistance_* files=[backend/internal/drivers/jiabaida_control.go backend/internal/drivers/jiabaida_fields.go]
family=temperature_* files=[backend/internal/drivers/jiabaida_parse.go]
=== GATE #4/#5 VERDICT: PASS ===
```

### 2.4 判定

- **#4 PASS**：`declared_unreachable=0` ⇒ 91 个声明名全部可由“产出字面量 ∪ 4 个动态族”到达（防幽灵声明）。分母 91 与评审报告实测“声明并集 = 91”一致，验证脚本口径可信。
- **#5 PASS**：104 个产出字面量全部匹配物理名文法；6 个 `*_ack` 属方案 §1.2/§12.3 已登记例外（回读伪量，不落 unified_data）。
- **信息项（不是失败）**：13 个产出未在声明里，含 6 个 `*_ack` 例外与 7 个运行时/配置派生命名（`custom_attr_1..3`、`hardware_version`、`serial_number`、`*_mos_test_status`）——方案 §12.3 门禁 4 只要求“声明 ⊆ 产出可达”，不要求反向枚举；这些正是“新字段不改中心端代码”（§12.2 红线 6）要允许的形态。

---

## 3. 门禁 #8：§5.1 全 15 条边界表驱动

### 3.1 复现命令

```bash
cd backend
go test ./internal/sensorfield/ -run 'TestSelectorBoundaryTable' -count=1 -v | grep -c '=== RUN'
go test ./internal/sensorfield/ -run 'TestSelectorBoundaryTable' -count=1 -v | grep -E '^=== RUN   TestSelectorBoundaryTable/(0[1-9]|1[0-5])'
```

### 3.2 实测输出

```
RUN_COUNT=39
TestSelectorBoundaryTable/01_star_only
TestSelectorBoundaryTable/02_underscore_colon
TestSelectorBoundaryTable/03_empty_aggregate
TestSelectorBoundaryTable/04_at_empty
TestSelectorBoundaryTable/05_virtual_empty
TestSelectorBoundaryTable/06_multi_aggregate
TestSelectorBoundaryTable/07_aggregate_upper
TestSelectorBoundaryTable/08_non_ascii
TestSelectorBoundaryTable/09a_empty
TestSelectorBoundaryTable/09b_blank
TestSelectorBoundaryTable/10_literal64_ok
TestSelectorBoundaryTable/11_literal65_too_long
TestSelectorBoundaryTable/12_family_trailing_space
TestSelectorBoundaryTable/13_virtual_is_literal
TestSelectorBoundaryTable/14_indexed_voltage_is_literal
TestSelectorBoundaryTable/15_unknown_semantic_is_syntactically_valid
```

39 = 1 个父测试 + 38 个子测试（15 条边界 + 23 条补充表项）。**不是假绿**：每个子测试内部用 `errors.Is` 断言哨兵错误码，且门禁 #9 的 M1/M3 变异正是打在这些表项对应的实现上（见 §4）。

### 3.3 15 条边界 ↔ 用例对照

| # | 输入 | 期望 | 用例名 |
|---|---|---|---|
| 1 | `*` | invalid_selector_syntax | 01_star_only |
| 2 | `_:x` | invalid_selector_syntax | 02_underscore_colon |
| 3 | `temperature_*:` | invalid_selector_syntax | 03_empty_aggregate |
| 4 | `@` | invalid_selector_syntax（不是 unknown_semantic） | 04_at_empty |
| 5 | `virtual:` | invalid_selector_syntax（不是 unknown_virtual） | 05_virtual_empty |
| 6 | `a_*:any:max` | invalid_selector_syntax | 06_multi_aggregate |
| 7 | `temperature_*:MAX` | unsupported_aggregate | 07_aggregate_upper |
| 8 | `温度_*` | invalid_selector_syntax | 08_non_ascii |
| 9 | 空串 / 全空白 | empty_selector | 09a_empty / 09b_blank |
| 10 | 64 字符 literal | 合法 | 10_literal64_ok |
| 11 | 65 字符 | selector_too_long | 11_literal65_too_long |
| 12 | `temperature_*:max ` | 合法（trim 后） | 12_family_trailing_space |
| 13 | `virtual`（无冒号） | literal | 13_virtual_is_literal |
| 14 | `cell_voltage_10` | literal（不走 family） | 14_indexed_voltage_is_literal |
| 15 | `@unknown` | 语法合法；解析期 400 unknown_semantic | 15_unknown_semantic_is_syntactically_valid |

---

## 4. 门禁 #9：变异自证（7/7 必红）

### 4.1 复现命令

```bash
bash .analysis/tools/mutation_check.sh
```

脚本对每个变异执行：备份 → 用 python 做**单点文本替换**（不命中标记则失败）→ 跑对应测试 → `git checkout` 恢复 → 比对 md5。

### 4.2 实测输出（节选，完整见脚本运行）

```
MUTATION M1-default-aggregate-any-to-all: CAUGHT (测试按预期变红)
    --- FAIL: TestResolveFamily
        family shape = {... Aggregate:all ...}
    restore: md5 一致 (...)
MUTATION M2-remove-missing-member-detection: CAUGHT
    --- FAIL: TestEvalPredicateTruthTable216/S4_partial/all/gt
    restore: md5 一致 (...)
MUTATION M3-family-pure-prefix: CAUGHT
    --- FAIL: TestFamilyDerivedAggregatesExcluded
        matched = [cell_voltage_1 cell_voltage_2 cell_voltage_10 cell_voltage_avg cell_voltage_max cell_voltage_min] want [1 2 10]
    restore: md5 一致 (...)
MUTATION M4-literal-takes-last: CAUGHT
    --- FAIL: TestLiteralEvalEquivalence
        literal first-match = {Value:99 ...} want 33
    restore: md5 一致 (...)
MUTATION M5-remove-killswitch-eval-check: CAUGHT
    --- FAIL: TestKillSwitch
        disabled special plan = {OK:true Satisfied:true Value:50 ...} want unbound disabled
    restore: md5 一致 (...)
MUTATION M6-remove-family-minmax-degradation: CAUGHT
    --- FAIL: TestEvalPredicateTruthTable216/S4_partial/min/gt
    restore: md5 一致 (...)
MUTATION M7-virtual-sum_of-should-not-degrade: CAUGHT
    --- FAIL: TestVirtualFamilyReduction
        sum_of partial = {OK:true ... Degraded:true} want unbound partial_frame
    restore: md5 一致 (...)
=== MUTATION SUMMARY: caught=7 failed=0 ===
=== FINAL FULL SUITE ===
ok  	ehome/backend/internal/sensorfield
```

### 4.3 判定

**PASS**。7 个变异点覆盖：默认聚合、缺员判定、family 严格数字后缀、literal 取第一个同名、kill switch 求值前检查、family min/max 降级、virtual sum_of 严格性。每个变异都**精准红在对应测试/格子**（M2/M6 甚至精确到 216 格真值表的 `S4_partial/all/gt`、`S4_partial/min/gt` 单格），且恢复后 md5 一致、全套复绿——证明测试在测实现，不是自说自话。

> 附：Lead 另外独立做了 3 个变异抽检（删 AggAll 缺员 fail-closed、空集 any 不 unbound、min/max 缺员改 unbound），三次都红在对应格子。

---

## 5. 门禁 #10：literal 性能（≤ 今天 +5%）

### 5.1 基线来源

`bench_baseline_test.go` 中 `baselineMatchField` 与 `baselineParserField` **逐字节复制自 main 基线 `d406dc2d`**（`git show d406dc2d:backend/internal/alert/evaluator.go` 的 `matchField`、`pkg/parser.Field`）。帧形态同为 16 字段、目标字段在首位。

### 5.2 复现命令

```bash
cd backend
go test ./internal/sensorfield/ -run '^$' -bench 'BenchmarkLiteralEval' -benchmem -count=5 -benchtime=300ms
```

### 5.3 实测输出（多次取样）

| benchmark | ns/op（5 次取样） | allocs/op |
|---|---|---|
| `BenchmarkLiteralEvalBaselineMatchField`（今天基线） | 0.7698 / 0.7298 / 0.7002 / 0.6900 / 0.6865 | 0 |
| `BenchmarkLiteralEvalPlanEval`（新层 Plan.Eval，接口分派） | 3.628 / 3.631 / 4.007 / 4.100 / 4.520 | 0 |
| `BenchmarkLiteralEvalPredicate`（含比较符完整谓词） | 26.95 / 21.80 / 20.89 / 24.75 / 26.30 | 0 |

中位数：基线 **0.70 ns**；新层 literal Plan.Eval **4.0 ns**（最好 3.63 ns）⇒ **约 +428%～+471%**，**超过 +5% 门禁**。
`BenchmarkFamilyEval16` = 301.6 ns/op（0 alloc，方案目标 < 2µs ✔）；`BenchmarkResolve` = 4063 ns/op（0 关键分配，目标 < 50µs ✔）。

### 5.4 判定（需口径裁决，给出事实而非粉饰）

- **按门禁 #10 字面**（“new literal path ≤ 今天 matchField +5%”）：**FAIL**（+428%）。
  - 成本来源可解释：① `Plan` 是接口 ⇒ 间接调用；② 依 §5.3 定义 1 每个值要做 `NaN/±Inf` 有限性校验；③ 返回 `EvalResult` 结构。0 分配保持不变。
- **按生产求值链路**（用户可感知回归）：**PASS / 0%**。集成实现（auto-analyst，commit `bbebed56` 起）明确规定 literal 仍走历史 `matchField`：
  - `internal/automation/selector.go`：`if plan.Kind() == sensorfield.KindLiteral { value, found := matchField(fields, selector); ... }`
  - `internal/alert/selector.go:200`：同样 literal 快路径；
  - 特殊 selector（family/semantic/virtual）才走 `EvalPredicate`，而这些今天没有对应链路 ⇒ 无基线可回归。
- **建议**：门禁 #10 判据改为“**生产求值链路** literal 不回归（当前 = 0%）”，并把新层抽象成本（3.63ns/0 alloc）作为观测值记录；不建议为凑 +5% 让 literal 改走新层——那会违反 G-6“与今天逐字节等价”的硬约束。若 Lead/窄复核坚持字面口径，则本项记 **FAIL** 并作为已知技术债登记。

---

## 6. 其它已实跑验证（供验收参考）

| 项 | 命令 | 结果 |
|---|---|---|
| 全仓构建 | `cd backend && go build ./...` | **绿**（含 automation/alert/api/main 集成） |
| 本包测试 | `go test ./internal/sensorfield/ -count=1` | **ok**，61 个 Test 函数 / 286 个子测试 |
| 并发门禁 | `go test -race ./internal/sensorfield/ -count=1` | **ok**（1.06s，无 DATA RACE） |
| 覆盖率 | `go test ./internal/sensorfield/ -cover` | **90.1% of statements** |
| 216 格真值表 | `go test -run TestEvalPredicateTruthTable216` | 216/216 子测试 PASS（用例内断言 `cells==216`） |
| 依赖红线 | 见 §1 | 仅 stdlib + pkg/logger + prometheus |
| 指标注册 | `go test ./internal/sensorfield/` 初始化 | 9 个 §10 指标 + dead_candidate，无重复注册 panic |

---

## 7. 开放项 / 已知限制（诚实边界）

1. **`go test ./...` 全仓唯一红**在 `internal/api` 的 5 个 `TestSensorFields*`：`no such table: sensor_field_obs`（task-7 集成面表注册，ds-analyst scope；Lead 已单独指派）。与 sensorfield 包无关，其余 34 包全绿。
2. **门禁 #10 口径**见 §5.4（本文件唯一需要裁决的项）。
3. **virtual sum_of** 缺员保持严格 unbound（统计量失真无安全解释）；`max_of/min_of` 已按 Lead 裁决降级（`Degraded=true`）。family `min/max` 与之一致。
4. **别名冻结**：首次解析后按 (device, semantic) 冻结；冻结物理名消失时重解析 + WARN（fail-open 可观测），不做永久死锁。
5. **Warnings() 的 dead-candidate gauge** 无清除动作：配置变更需重启（方案 §11），进程重启即重置，无陈旧 series 风险。
6. 详细的实现期契约扩展（SetDeviceType/SetFieldMeta/EvalPredicate/PlanEpoch/Warnings 等）见 `backend/internal/sensorfield/doc.go` 的“实现期新增 API”一节。
