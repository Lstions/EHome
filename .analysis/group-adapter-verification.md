# 数据源域组适配器独立验证（impl-core）

- **工作树**：`.agents/worktrees/source-group`（分支 `source-group`）
- **验证时 HEAD**：`e7c5df06` → **修复后复验 HEAD `9f94c279`**（§7）（M0 主备链路全量接线；本验证测试文件已随该提交入库，`group_verify_diff_test.go` 有本地产出差量）
- **被测**：`backend/internal/datasource/group_{service,state,scan,crud}.go` + `internal/api/handler_source_group.go`
- **环境**：go1.27.1 linux/amd64，`GOCACHE=/tmp/eh-sg-cache`；验证库用**文件型 SQLite + EnsureSourceGroupSchema**（生产同款部分唯一索引），不用 `:memory:`（见 F-3）
- **只读被测实现，未改实现**；发现的问题先报 ds-analyst/Lead，由 ds 修复。

## 0. 结论

| 项 | 结果 |
|---|---|
| `go build ./...` | **绿** |
| `go vet ./internal/datasource/ ./internal/api/` | **绿** |
| 本验证测试 | **6 文件 / 35 个 `=== RUN` 全绿**（复验 HEAD 982e298e，含后续新增的 staleness 归一/降级可见性覆盖） |
| 整包 `go test ./internal/datasource/` | **ok**（含 ds-analyst 原有测试） |
| `-race`（TestVerify*） | **ok**（4.9s） |
| 等价性差分 | **3 seeds × 40 步状态轨迹逐步相等** |
| 变异自证 | **18 项尝试：17 CAUGHT + 1 被防御性兜底掩盖**（见 §3.1/§3.2/§3.3），恢复后 md5 全一致、整包复绿 |
| 发现 | 1 高风险（R5 双 active）+ 5 处契约/一致性偏差 —— **均已由 ds-analyst 修复/裁决**（§4 状态列） |

## 1. 五个验证重点 → 用例映射

| # | Lead 要求 | 用例 |
|---|---|---|
| 1 | revision bump 白名单（100×MarkSuccess 不变；active 迁移 +1；反证必须存在） | `TestVerifyRevision_100xMarkSuccessNoBump`、`_RuntimeOnlyNoBump`、`_ActiveMigrationBumpsAndHashStable`、`_TopologyChangeBumps`、`TestVerifySnapshotInvariants` |
| 2 | ActiveChange 发射点完整性（R1–R11，每迁移恰 1 条，多/少都红） | `TestVerifyEvents_R1R2_NoEvent`、`_R3_FailoverExactlyOne`、`_R4_DegradedNextZero`、`_R10_CooldownSuppresses`、`_R11_MinResidency`（3 子例）、`_ManualMigrations`（R5/R6/R7/R8/R9 五子例）、`_DeleteGroupNextZero`、`_CompositeNeverEmits` |
| 3 | R4 降级必须 Next=0；无 active 组可被自愈扫描提升 | `TestVerifyEvents_R4_DegradedNextZero`、`_R4_SelfHealClosedLoop` |
| 4 | legacy vs group 等价性差分 | `TestVerifyDiff_LegacyVsGroupEquivalence`（seed 1/7/23 × 40 步） |
| 5 | composite 禁 active（API 400 + 直写 DB 后启动自检报错） | `internal/api/handler_source_group_verify_test.go::TestVerifySourceGroupAPI_CompositeActiveForbidden`、`TestVerifyComposite_NoActiveSemantics`、`TestVerifySelfCheck_CleanPasses/_CompositeActiveRejected/_MultiActiveRejected/_StartFailClosed/_StartBlocksUntilCancel` |

### 关键断言口径（摘）

- **F-1 反向断言**：100 次 `MarkSuccess` 后 DB/快照 `revision` 均不变、`MembersHash` 不变、`last_success` 确实写入；失败计数（未达阈值）、standby→error、error→standby 均不 bump。
- **active 迁移**：`revision+1`、快照 revision 同步、`ActiveMemberID` 更新，而 `MembersHash` **不变**（静态身份稳定）；迁移后 100×MarkSuccess 仍不 bump。
- **事件完整性**：R1/R2/未达阈值/R8/冷却抑制/composite 全程 **0 事件**；R3/R4/R5/R7/R9/R11/自愈/删除 **恰 1 条**；R4 `NextMemberID==0`；R7 幂等再 activate 0 事件且不 bump。
- **R4→自愈闭链**：R4 后快照 `ActiveMemberID=0`、`MatchFrame(Accepts)=false`（规则 fail-closed）；standby 恢复后 `ScanGroups` 自愈提升、`MatchFrame(Accepts)=true`、revision 连续 +2。
- **自检 fail-closed**：composite active、单组多 active（先删部分唯一索引模拟脏库）均返回 error；`PublishAll` 失败且**不替换**快照；`Start` 在脏库立即返回（扫描器不启动），干净库可被 ctx 取消。

## 2. 等价性差分（决策逻辑只有一份的反向证明）

两个独立世界、同一假时钟（真实时刻 + 5 分钟，使 R11 驻留期可稳定判定）：

- legacy：`data_sources` 行 + `Service`（MarkSuccess/MarkFailure/ScanStale/Activate/Deactivate/Reset）
- group：`source_group_members` + `GroupService`（同名语义方法）

每步后比较：active 的 edgeDeviceID、每成员 `status/fail_count/last_success 是否已写`、切换日志条数、扫描返回切换次数、手动操作错误类别（nil/conflict/invalid/notfound）。
结果：**seed 1/7/23 各 40 步，全部逐步相等**（含自动切换、降级、手动切回、停用、恢复、停滞扫描）。

**明示的豁免（不属于漂移）**：

1. **N-21 自愈**是组路径**新增**能力，legacy 没有。差分在「两侧都无 active」时跳过 scan（时钟照常前进），避免把新行为误判为不一致。
2. `revision` / `ActiveChange` / 组健康事件是组路径新增可观测面，不在 legacy 状态内。
3. R11 用 `UpdatedAt` 作驻留期代理是 **legacy 既有口径**（ds 已注释）；差分证明两侧在该口径下行为一致，不视为偏差。

## 3. 变异自证（9/9 CAUGHT）

方法：对每个关键判据做最小语义改坏 → 跑对应验证用例（必须 FAIL）→ 字节恢复 → md5 校验 → 整包复绿。

| # | 改坏点 | 目标用例 | 结果 |
|---|---|---|---|
| M1 | MarkSuccess 里 bump revision（运行态付税） | `TestVerifyRevision_100xMarkSuccessNoBump` | **CAUGHT** |
| M2 | active 迁移不再 bump revision | `TestVerifyRevision_ActiveMigrationBumpsAndHashStable` | **CAUGHT** |
| M3 | R4 事件被丢弃（`if switched` 回归） | `TestVerifyEvents_R4_DegradedNextZero` | **CAUGHT** |
| M4 | R4 降级 `Next` 不为 0 | `TestVerifyEvents_R4_DegradedNextZero` | **CAUGHT** |
| M5 | 关闭 N-21 自愈（无 active 直接返回） | `TestVerifyEvents_R4_SelfHealClosedLoop` | **CAUGHT** |
| M6 | R5 deactivate 顺序反了（先升后降） | `TestVerifyEvents_ManualMigrations/R5` | **CAUGHT** |
| M7 | R10 冷却失效（恒返回不抑制） | `TestVerifyEvents_R10_CooldownSuppresses` | **CAUGHT** |
| M8 | 启动自检放过 composite active | `TestVerifySelfCheck_CompositeActiveRejected` | **CAUGHT** |
| M9 | publishGroup 空转（不更新 registry） | `TestVerifySnapshotInvariants` | **CAUGHT** |

恢复校验（实测 md5，三文件均与验证开始时一致）：

```
group_state.go   0296c9a718ed0425188b13a2194afa7f OK
group_service.go 429e6b93032443ad9b4a275489840640 OK
group_scan.go    f8d73963bd2e215949cbe459b2ab9d36 OK
final full pkg: ok  ehome/backend/internal/datasource  4.8s
```

> 实现文件当时为未跟踪/协作中状态，故用**字节备份 + md5** 而非 `git checkout`（等效手段，已获 Lead 认可）。

### 3.2 追加改动变异（HEAD 982e298e：staleness 零值归一 + 降级可见性）

| # | 改坏点 | 目标用例 | 结果 |
|---|---|---|---|
| M14 | 关闭 StalenessSec=0 归一 | `TestVerifyStalenessNormalization` | **CAUGHT** |
| M15 | 关闭 RecordDegraded 1min 节流 | `TestVerifyRecordDegradedThrottle` | **CAUGHT** |
| M16 | 节流键忽略 reason（不同 reason 互相抑制） | `TestVerifyRecordDegradedThrottle` | **CAUGHT** |
| M17 | 删除组孤儿健康行守卫移除 | `TestVerifyRecordDegradedThrottle` | **CAUGHT** |

恢复后 `group_crud.go`/`group_service.go` md5 一致，整包复绿。

### 3.3 R10 语义变更变异（HEAD `c6014dd0`：B-2/架构终审）

Lead 裁决 R10 抑制语义变更：抑制的是「切换动作」，不是「组失去权威」这个事实。
当前语义 = active 置 error 后无 active ⇒ 必须发 `ActiveChange{Prev=原active, Next=0, Reason=cooldown_suppressed}`、
bump revision、落 `degraded` 健康事件（message 含 `no_active_after_cooldown`）；切换日志仍不得新增。

| # | 改坏点 | 目标用例 | 结果 |
|---|---|---|---|
| M18 | 退回旧语义：冷却抑制分支 `return nil`（不事件） | `TestVerifyEvents_R10_CooldownSuppresses` | **CAUGHT**（等事件超时 / 断言失败） |

恢复后 `group_state.go` md5 一致、整包复绿。

## 4. 发现的实现问题（按严重度）

### F-1【高，已修复 ✅】R5 停用 active 时「先升后降」制造双 active → 事务必失败

**复验**：ds 已按「先降旧→再升新→bump」修复并加 `TestGroupDeactivateActiveSingleActiveInvariant`；本文件 M6 变异证明回退会被抓红。

- **现象**：`DeactivateGroupMember` 在 active+standby 场景直接返回 `UNIQUE constraint failed: source_group_members.group_id`（部分唯一索引 `ux_sgm_one_active`），deactivate 完全不可用。
- **根因**：组适配器把候选置 active 放在原 active 置 disabled **之前**；legacy `Service.Deactivate` 的 D-3 修复注释已写明「先降旧、再升新」铁律。
- **处置**：ds-analyst 已修为「先降后升 → bump revision」，并加回归锁 `TestGroupDeactivateActiveSingleActiveInvariant`；本验证保留 `TestVerifyEvents_ManualMigrations/R5`（M6 变异证明它能抓住回退）。
- **证据**：本轮 14:20 前的 `go test -run TestVerifyEvents` 输出；修复后同用例绿。

### F-2【中，已修复 ✅】删除组的 ActiveChange 缺 GroupID/At/Revision/TransitionSeq

**复验**：ds 改为 `commitGroupChange(groupID, ev)`（删除前 bump+取 seq），并有 `TestGroupDeleteEmitsCompleteEvent`；本验证 `TestVerifyEvents_DeleteGroupNextZero` 已升级为硬断言（GroupID/Revision/TransitionSeq/At 非零），M10b 变异 CAUGHT。

- `DeleteGroup` 绕过 `commitGroupChange`，直接 `registry.Remove + dispatcher.emit` → 事件 = `{GroupID:0, At:zero, Revision:0, TransitionSeq:0, Next:0, Reason:delete}`。
- 消费方按 `GroupID` 清窗/定位规则时拿不到组身份；与 `commitGroupChange` 注释「所有组状态变更的统一收尾」自相矛盾。
- 验证用例：`TestVerifyEvents_DeleteGroupNextZero`（记录为 DEVIATION D-2，非阻断）。
- 建议：删除路径也走 `commitGroupChange(groupID, ev)`（先 bump/填 Revision+TransitionSeq，再 Remove）。

### F-3【中，已修复 ✅】UpdateGroup 在事务内用 `g.db`（非 tx）校验成员

**复验**：ds 让 `validateMemberConfig/categoryObserved` 显式收 db 参数、事务内传 tx；`:memory:` 单连接问题消除。本验证继续用文件型库（生产同款）不复现。

- `UpdateGroup` 事务内调用 `validateMemberConfig(g.db, ...)`；SQLite `:memory:` 下事务占连接后 `g.db` 另开连接 ⇒ `no such table: edge_devices`（原 `testutil.OpenTestDB` 复现）。
- 生产（文件型 SQLite/PG）不报错，但校验读在**事务外**，存在读到未提交状态的窗口；本次验证改用文件型库绕过。
- 建议：`validateMemberConfig(tx *gorm.DB, ...)`，事务内传 tx；同时保护 `:memory:` 测试基建（或 testutil 固定单连接+共享缓存）。

### F-4【低，已修复 ✅】UpdateGroup 仅改 name/description 也 bump revision

**复验**：ds 改为仅 combine/min_members/staleness/on_degrade/members 变化才 bump；本验证新增 `TestVerifyRevision_NonIdentityUpdateNoBump`（name/description/enabled + R8 全部不 bump + priority 正对照），M11 CAUGHT。

- F-1 白名单未含 name/description（它们是展示字段，不影响窗口身份）；当前实现 `len(updates)>0` 即 bump ⇒ 改名会让该组所有窗口 identity 重置一次（DurationSec 重新累计）。
- 验证用例：`TestVerifyRevision_TopologyChangeBumps` 只断言白名单内变更必 bump；本条作为观察记录（未写成红断言）。
- 建议：仅当身份字段（members/priority/required/combine/min/staleness/on_degrade/enabled/kind/category）实际变化时 bump。

### F-5【低，已修复 ✅】R8（停用非 active 成员）也 bump revision

**复验**：同上 `TestGroupNonIdentityUpdateDoesNotBumpRevision` + 本验证 `TestVerifyRevision_NonIdentityUpdateNoBump`，M12 CAUGHT。

- F-1 白名单只列「成员新增/删除/priority/required/…」，成员 status=disabled 属运行态（MembersHash 也不含 status），却 bump 了一次。
- 影响：手动禁用成员会重置一次窗口 identity（一次性、非每帧，风险低）。
- 建议：与 F-4 一起按「身份字段真实变化」收敛。

### F-6【低，已裁决 ✅】composite deactivate 领域层允许（200），handler 注释声明应 400

**裁决**：允许 composite 成员显式 deactivate（R8 → disabled），仅 activate 一律 400；handler 注释已更正。本验证 API/领域两处已改为硬断言（200 + disabled + 无 active），M13 CAUGHT。

- 实测（`TestVerifySourceGroupAPI_CompositeActiveForbidden`，记录 DEVIATION D-3）：`POST /source-groups/:id/deactivate` 对 composite 返回 **200**，成员被置 `disabled`（R8 语义）；handler 文件头注释写「composite 400」。
- 需产品/契约裁决：composite 成员能否被停用（若能，协议注释应改；若不能，领域层应拒绝）。

### F-7【已关闭 ✅】main.go 尚未接线 GroupService/PublishAll

**复验**：`cmd/server/main.go:404-486` 已接线 `NewGroupService/SetGroupRegistry/SetDispatchNotifier/SetRuleSourceGroupService`，`PublishAll` 失败即 `logger.Fatalf` 拒绝启动，`RunDispatcher+Start` 启动，数据面 sink 双喂两引擎、离线钩子双 MarkFailure。

- `grep GroupService|PublishAll cmd/server/main.go` 为空：启动自检/冷加载/扫描器/RunDispatcher 还没进生产启动路径（Lead 的 M0 全量接线提交可能仍在进行）。
- 影响：`PublishAll` 失败拒绝启动的保护在当前 main 上不生效；属接线遗漏，非组适配器逻辑问题。

### 3.1 修复后追加变异（验证新回归锁，HEAD 9f94c279）

| # | 改坏点 | 目标用例 | 结果 |
|---|---|---|---|
| M10 | 删除事件丢 TransitionSeq | `TestVerifyEvents_DeleteGroupNextZero` | **被掩盖**（`commitGroupChange` 的 `TransitionSeq==0 ⇒ 补 seq` 兜底生效，属防御性设计，非测试缺陷） |
| M10b | DeleteGroup 绕过 `commitGroupChange`（F-2 回归：直接 Remove+emit） | `TestVerifyEvents_DeleteGroupNextZero` | **CAUGHT**（GroupID/At/Revision 缺失） |
| M11 | 非身份更新（name/description/enabled）重新 bump（F-4 回归） | `TestVerifyRevision_NonIdentityUpdateNoBump` | **CAUGHT** |
| M12 | R8 停用非 active 重新 bump（F-5 回归） | `TestVerifyRevision_NonIdentityUpdateNoBump` | **CAUGHT** |
| M13 | composite 成员 deactivate 被拒绝（F-6 裁决回归） | `TestVerifyComposite_NoActiveSemantics` | **CAUGHT** |

累计：**12 CAUGHT / 1 被兜底掩盖**；恢复后 `group_crud.go/group_state.go/group_service.go` md5 一致，整包复绿。

## 5. 诚实边界

1. 本验证只覆盖 `internal/datasource` 组适配器 + `handler_source_group` 的 composite 契约；DB 迁移/索引/API 其余端点由 ds/verifier 的门禁覆盖。
2. 差分只覆盖 **primary_backup**；composite 无 legacy 对应物，用独立用例覆盖（禁用永远无 active/MT）。
3. N-21 自愈、revision/ActiveChange 属新增能力，按设计豁免差分（§2）。
4. 变异自证以「目标用例必红」为判据，不追求全包必红（避免无关用例噪声掩盖因果）。
5. `TestVerifyDiff` 覆盖 3 seeds × 40 步；如需更高置信度可加 seeds，但当前已覆盖 R1–R11 全部分支（由用例内断言覆盖矩阵保证）。

## 7. 修复后复验（HEAD `9f94c279` → `982e298e` → `c6014dd0`）

ds-analyst 完成 F-1～F-6 后，按同一套验证重新跑：

| 项 | 结果 |
|---|---|
| `go build ./...` | 绿 |
| `go vet ./internal/datasource/ ./internal/api/` | **绿**（ds 提到的 `source_group_guard.go:101 unreachable` 已不复现；该文件非本验证产出） |
| 本验证测试 | **35 个 `=== RUN` 全绿**（新增 `TestVerifyRevision_NonIdentityUpdateNoBump`/删除/复合硬断言；追加 `TestVerifyStalenessNormalization`/`TestVerifyRecordDegradedThrottle` 覆盖 982e298e 的两块新改动） |
| 整包 `go test ./internal/datasource/` | ok（7.1s，含 ds 新增 20 个组用例） |
| `-race`（TestVerify*） | ok（5.6s） |
| API 验证 | `TestVerifySourceGroupAPI_CompositeActiveForbidden`（activate 400 / deactivate 200+disabled）绿 |
| 差分 | 3 seeds × 40 步仍逐步等价（末态含 logs=5/6、disabled/standby 混合，seed 轨迹不同） |
| 追加变异 | M10b/M11/M12/M13 **CAUGHT**（§3.1）；M14/M15/M16/M17 **CAUGHT**（§3.2）；M18 **CAUGHT**（§3.3，R10 新语义） |

**关于 `source_group_guard.go`**：该文件不是本任务的验证产出（本任务只新增
`internal/api/handler_source_group_verify_test.go`）；它属于规则来源绑定校验的接线文件，
当前 `go vet ./internal/api/` 已无 unreachable 告警，无需本任务处理。

## 6. 复跑命令

```bash
export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH
export GOCACHE=/tmp/eh-sg-cache
cd .agents/worktrees/source-group/backend

go build ./...
go vet ./internal/datasource/ ./internal/api/
go test ./internal/datasource/ -count=1
go test ./internal/datasource/ -run 'TestVerify' -race -count=1
go test ./internal/api/ -run 'TestVerifySourceGroupAPI' -count=1
go test ./internal/datasource/ -run 'TestVerifyDiff' -count=1 -v   # 差分轨迹（t.Logf 末态）
```
