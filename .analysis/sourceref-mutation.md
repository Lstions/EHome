# sourceref 独立规格测试与变异自证（task-32）

- **工作树**：`.agents/worktrees/source-group`（分支 `source-group`，基线 `7bc63e3f` = 方案 v0.3）
- **被测包**：`backend/internal/sourceref/`（Lead 实现；本任务**只写测试，不改实现**）
- **测试文件**：`backend/internal/sourceref/{helpers,types,combine,registry}_test.go`
- **环境**：go1.27.1 linux/amd64；`GOCACHE=/tmp/eh-sg-cache`
- **实现文件 md5（自证前后一致，运行时态）**：
  - `types.go`   = `caf290ce411295c8bfe076e5e42a9821`
  - `combine.go` = `4bed90368614f4258a9c30ee8fa981b9`
  - `registry.go`= `e78ebb5afd2fa6b7a16f0162d2991a11`

> 说明：本任务交付时 `backend/internal/sourceref/` 三个实现文件在 git 中**尚未被跟踪**（`?? internal/sourceref/`），
> 因此变异自证不能用 `git checkout -- <file>` 恢复；改用**字节级备份 + md5 校验**（等效且更严格：
> `git checkout` 对未跟踪文件会失败）。这是与任务书「git checkout 恢复」的诚实差异。

## 1. 结论汇总

| 项 | 结果 |
|---|---|
| 测试规模 | **88 个 `=== RUN`**（含子测试；父测试 30 个） |
| 覆盖率 | **97.6% of statements**（目标 ≥90% ⇒ **PASS**） |
| `-race` | **ok**（含并发 Publish/ReplaceAll/Remove/Group/MatchFrame 压测） |
| `go vet ./internal/sourceref/` | **ok** |
| 变异自证 | **11/11 CAUGHT**，恢复后 md5 全部一致，全套复绿（见 §3） |
| 发现实现缺口 | **1 处**（first_available 归一缺失）→ 已报 Lead，按裁决 (c) 修复（见 §4） |

## 2. 覆盖范围（与任务书 ①–⑥ 对照）

| # | 要求 | 落点 |
|---|---|---|
| ① | Combine 真值表逐行（§12.9.2） | `TestCombineTruthTable12_9_2`（7 行逐条子测试 + 2 变体）+ `TestCombineGroupPrimaryBackup`（7 情形）+ `TestCombineGroupGuards`（nil/disabled/空成员/未知 kind） |
| ② | MatchFrame 矩阵 | `TestMatchFrameKindDispatch`（6 种非 source_group kind + groupID=0）、`TestMatchFrameGroupMissing`、`TestMatchFrameGroupDisabled`、`TestMatchFramePrimaryBackup`（active/standby/非成员/无 active）、`TestMatchFrameComposite`、`TestFrameMatchAccepts`（7 行真值表）、`TestMatchFrameZeroAllocs`（0 alloc/op） |
| ③ | Registry 并发 | `TestRegistryConcurrentAccess`（4 读 + 1 写 goroutine，-race）、`TestRegistryPublishGroupRemoveLen`、`TestRegistryReplaceAll`（nil/ID0 过滤、重复 ID、清空/恢复）、`TestRegistryPublishVisibility`、`TestRegistryNilReceiver` |
| ④ | ComputeMembersHash 只含静态字段 | `TestComputeMembersHash_StaticOnly`：改 Status/LastSuccess/FailCount/**MemberID** ⇒ hash 不变；改 EdgeDeviceID/Priority/Required ⇒ hash 必变。另有 `_DeterministicAndOrderIndependent`、`_Format`（格式钉死） |
| ⑤ | SortMembers 顺序 | `TestSortMembers`（priority DESC、同优先级 memberID ASC、幂等、nil/单元素） |
| ⑥ | GroupSnapshot 辅助函数 | `TestActiveEdgeDeviceID`、`TestIsMemberAndMemberOf`、`TestStaleness`、`TestEffectiveMinMembers`（7 情形）、`TestNormalizedOnDegrade`（含 first_available 归一） |
| 附加 | 纯函数/责任边界 | `TestCombineTimeIndependence`（同输入 100 次结果一致，证明无 time.Now）、`TestCombineComponentNormalization`（身份回填、空 State→missing、nil eval）、`TestCombineStateNotConfigured`、`TestCombineResultComponentsOrder`、`TestCombineAvgPrecision`、`TestCombineStalenessBoundary`、`TestConstantsAreStable`（常量字符串与 models/DB 同值） |

### §12.9.2 权威真值表逐行映射

| 行 | 判据 | 测试子用例 |
|---|---|---|
| ① `fail_closed` + 任意缺席 | unbound | `row1_fail_closed_any_missing_unbound`、`row1b_fail_closed_optional_missing_unbound` |
| ② `use_available` + 含 required 缺席 | unbound(required_missing) | `row2_use_available_required_missing_unbound`、`row2b_use_available_required_stale_unbound` |
| ③ `use_available` + 仅 optional 缺席 + 可用≥min | 合并 + degraded | `row3_use_available_optional_missing_degraded_ok`、`row3b_use_available_sum_only_available` |
| ④ `use_available` + 可用<min | unbound(below_min_members) | `row4_use_available_below_min_unbound` |
| ⑤ `first_available` + 可用≥1 | 取 priority DESC,id ASC 首个 + degraded | `row5_first_available_picks_highest_priority_available`、`row5b_first_available_all_available_no_degrade`、`TestFirstAvailableNormalization`（3 种 OnDegrade 输入） |
| ⑥ `first_available` + 可用=0 | unbound | `row6_first_available_no_available_unbound` |
| ⑦ 全部可用 | 按 combine 合并，无降级 | `row7_all_available_merge_no_degrade` |

## 3. 变异自证（11/11 CAUGHT）

方法：对每个关键判据做一处**最小语义改坏** → 跑对应测试（必须 FAIL）→ 字节恢复 → md5 与原始一致 → 下一项。
最后整包 `go test ./internal/sourceref/ -count=1` 必须绿。

| # | 改坏点（文件） | 判据 | 目标测试 | 结果 |
|---|---|---|---|---|
| M1 | `types.go`：hash 混入运行态 `fail_count` | 「不含运行态」不变量（F-1 最后一道门） | `TestComputeMembersHash_StaticOnly` | **CAUGHT** |
| M2 | `types.go`：`SortMembers` 优先级反向 | R3 候选顺序 | `TestSortMembers` | **CAUGHT** |
| M3 | `types.go`：去掉 `first_available ⇒ use_available` 归一 | §12.9.2 行⑤ 归一口径 | `TestFirstAvailableNormalization` | **CAUGHT** |
| M4 | `types.go`：去掉 `first_available ⇒ min=1` | D-4 / §12.9.2 行⑤ | `TestEffectiveMinMembers` | **CAUGHT** |
| M5 | `combine.go`：`fail_closed` 缺员不再 unbound | 真值表行① | `TestCombineTruthTable12_9_2` | **CAUGHT** |
| M6 | `combine.go`：`use_available` 忽略 required 缺席 | 真值表行② | `TestCombineTruthTable12_9_2` | **CAUGHT** |
| M7 | `combine.go`：`Degraded` 恒 false | 真值表行③（静态错值风险） | `TestCombineTruthTable12_9_2` | **CAUGHT** |
| M8 | `combine.go`：`first_available` 取最后一个可用 | 真值表行⑤ 顺序语义 | `TestCombineTruthTable12_9_2`/`TestCombineOperators` | **CAUGHT** |
| M9 | `combine.go`：未知 combine 静默接受 | 白名单 fail-closed | `TestCombineOperators` | **CAUGHT** |
| M10 | `registry.go`：`Accepts` 忽略 primary 的 IsActive | S1（备帧不得驱动求值） | `TestFrameMatchPrimaryBackup` | **CAUGHT** |
| M11 | `registry.go`：`MatchFrame` 忽略组禁用 | fail-closed（不得静默退回任意设备） | `TestMatchFrameGroupDisabled` | **CAUGHT** |

恢复校验（实测 md5）：

```
types.go   caf290ce411295c8bfe076e5e42a9821 OK
combine.go 4bed90368614f4258a9c30ee8fa981b9 OK
registry.go e78ebb5afd2fa6b7a16f0162d2991a11 OK
final full test: rc=0  ok ehome/backend/internal/sourceref  0.005s
```

## 4. 发现与处置：first_available 归一缺失（已修复，Lead 裁决 (c)）

**发现**：测试对照方案 §12.3 D-5 / §12.9.2 行⑤「`first_available`（归一 use_available…）」时发现
`NormalizedOnDegrade()` 只按 `OnDegrade` 字段映射，不看 `Combine`。若快照 `OnDegrade` 为空串（DB 默认），
`CombineGroup` 会走 `fail_closed` ⇒ 缺任一成员直接 unbound，与「任一台在就够」的产品语义相反。

**处置**：按 Lead 裁决 **(c) 双保险**：
1. `sourceref/types.go`：`NormalizedOnDegrade()` 对 `Combine == first_available` **强制**返回 `use_available`
   （与 OnDegrade 字段无关；显式 fail_closed 也归一），作为运行时最后防线；
2. 保存期（API/状态机）由 ds-analyst 同步归一；
3. `EffectiveMinMembers()` 注释与归一同源（已是 1）。

**测试更新**：原 `TestFirstAvailableRequiresNormalizedSnapshot`（记录旧边界）改为
`TestFirstAvailableNormalization`：`OnDegrade` ∈ {"", fail_closed, use_available, bogus} 四种输入都必须
`OK=true, Degraded=true, Value=最高优先级可用成员`；并反证聚合类 combine 不受归一影响（显式 fail_closed 仍 unbound）。

> 该修复已由 M3 变异覆盖：去掉归一 ⇒ 测试必红。

## 5. 诚实边界（未覆盖 / 观测）

1. **`MemberOf` 非 nil 安全**：`ActiveEdgeDeviceID/IsMember/Staleness/NormalizedOnDegrade/EffectiveMinMembers`
   都做了 `g == nil` 防御，`MemberOf` 没有（nil 调用会 panic）。当前生产路径只对非 nil 快照调用；
   是否补防御属实现决策，测试按现有契约只覆盖非 nil（如需统一 nil 语义请裁决）。
2. **未知 `Kind` 的行为**：`CombineGroup` 返回 `group_missing`（fail-closed）；`FrameMatch.Accepts` 对未知 kind
   按 composite 处理（只要求 Configured+IsMember）。契约只定义 primary_backup/composite，本测试未把未知 kind
   当规格钉死（避免把未定义行为固化成契约），仅 `TestCombineGroupGuards` 记录 fail-closed 观测。
3. **`go build ./...` 状态**：本任务期间共享工作树的 `backend/internal/automation/planner.go` 处于
   其他 teammate 的未提交中间态（`undefined: checkConditionsStillSatisfiedWithSource / applySourceColumns`），
   全仓 build 暂时不可用；**sourceref 包自身** `go build ./internal/sourceref/` + `go vet` 绿。
   最终门禁请在干净树复跑（本包不受 planner.go 影响）。
4. 未测 DB/HTTP/事件发射层（不属 sourceref 包；由 ds-analyst/verifier 的 SG-* 门禁覆盖）。
