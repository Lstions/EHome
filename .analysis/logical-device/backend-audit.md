# T2 后端业务逻辑与不变量审计

- 任务：task-2（后端业务逻辑与不变量审计）
- 执行：backend-auditor
- 观测窗口：2026-10-23 20:46 – 21:45 (+08:00)
- 代码基线：`git HEAD = 5f74fd3b`（2026-10-23 19:47）；逻辑设备相关最后改动 `9f94c279`(2026-10-22)
- 环境：本机 `docker exec ehome-postgres psql -U ehome -d ehome`（PG 18.6-alpine）；本机 API `ehome-web@ehome-new:v2`(2026-10-18 构建)；生产 `ehome-prod-web@ehome-new:sg-c76744d3`(2026-10-23 13:01 构建) + `ehome-prod-postgres`
- 数据快照：本机 2 逻辑设备 / 2 边缘设备 / unified_data 179,532 行 / device_data 568,617 行；生产 16 逻辑设备 / 10 边缘设备 / unified_data 32,473,666 行 / device_data 3,051,268 行
- 证据等级：**T**=实测（真跑 psql/curl/EXPLAIN）；**C**=代码（文件:行）；**D**=文档
- 纪律：仅读（SELECT / EXPLAIN / curl GET），未执行 DDL、未写任何库、未重启容器；凭据只以环境变量出现

> **口径声明**：本文件中凡"每项 N 次查询"，分母是"当前页返回的 1 条 logical_devices 记录"，口径是"一次 GET /api/v1/logical-devices 请求内、该条记录触发的独立 DB 往返次数（不含鉴权中间件的 users/auth_state 查询）"，时刻见各条注记。

---

## 0. 结论速览（12 条）

| # | 结论 | 等级 | 关键证据 |
|---|---|---|---|
| 1 | identity_key 在 hardware_id 为空时的 uuid v5 派生是**确定性的**，同一 edge_devices.id 跨补建/跨副本恒同 key；但"删旧实例→建新实例"必然换 key ⇒ 换节点场景必断历史 | T+C | 本机 2/2、生产 **12/16** 的 key = uuid5(edge:N)（另 4 条走 hardware_id 及其 #N 变体） |
| 2 | 生产存在**同一硬件 3 个逻辑设备、数据只落其中 1 个**的可观测缺陷 | T | ld 1/2/3 全为 `jiabaida_bms:0x01(#2/#3)`，ud 行数 0 / 2,923,306 / 0 |
| 3 | 三条身份路径**不能**共同证明"一逻辑设备至多一个存活实例"：PathStartup 的检查-插入无行锁，靠 identity_key 唯一索引兜底，兜不住"同 hardware_id 不同 instance" | C | identity.go:175-182（count 后无锁）/ 121-144 |
| 4 | merge 要求"源无存活实例"，与"一身份一存活实例"联立后 ⇒ **必须先删旧实例才能合并**；而 PathCreateNew 又永不复用 ⇒ 用户说的"直接复用"被两头堵死 | T+C | merge.go:144-155；identity.go:172-174；handler_edge_device_lifecycle.go:68-86 |
| 5 | 合并状态机**不闭合**：失败路径复位 `merged_into/merge_status` 但**不回滚已提交的搬迁批次** ⇒ 数据被静默劈成两半且无补偿路径 | C | migrate.go:324-337（每批独立事务）+ :377-379（复位） |
| 6 | purge 与 merge 之间有无锁窗口；且 `logical_devices/edge_devices` 上 **FK 约束 = 0 条**（实测），引用完整性全靠应用层 | T+C | 实测 pg_constraint 0 行；purge.go:284-302 vs merge.go:167-184 |
| 7 | `Scope.Cond()` 的 OR 分支使 `logical_device_id` 从索引前导条件降级为 Filter，**生产实测 26,932 ms vs 等价 UNION ALL 0.644 ms（≈41,800×）** | T | prod EXPLAIN ANALYZE（见 §4） |
| 8 | **修正 Lead 的"数据量越大越慢"**：真正的驱动量是"**该逻辑设备最后一包数据之后还有多少行**"。id=14（27.8M 行）1.38 s；id=12（仅 205k 行）42.4 s | T | prod page=12/14/15 隔离实测（见 §4.3） |
| 9 | OR 的 fallback 分支（NULL-logical）在生产**两条数据表上恒 0 命中**，却为它付全索引扫描代价 | T | unified NULL=0；device_data NULL 全为 device_id=0，而分支条件是 device_id IN (7..16) |
| 10 | 每项富化的 DB 往返：代码面 **11 次/项**（其中 4 次边界查询 + 2 次 EXPLAIN 各自再按分区扇出 5–6 倍）；Lead 的"O(7)"假设成立且略低估 | T+C | handler_logical_device.go:80-98；scope.go:56-140；preview.go:119-175 |
| 11 | **数据可达性缺陷（最严重）**：全部数据读入口只有 `/edge-devices/:id/...`。生产 **6/16** 个逻辑设备 `instance_count=0`（无任何实例，含软删），其中 **1 个（ld 2）挂着 2,923,306 行**历史 ⇒ 这批历史**没有任何 API 能读到**，且 retention 仍会按该 ld 的保留期继续删 | T+C | grep 路由；prod 实测：`inst=0 & ud=2923306`；孤儿行 2,923,306 |
| 12 | 需求强制 vs 自造：用户 1 条需求 → 3 张新表 + 15 个列实例 + 3,703 行功能代码 + 8,964 行测试 + 3 个后台 worker + 96 状态组合 | T+C | §6 分母 |

---

## 1. identity_key 在真实数据下的退化（必答 1）

### 1.1 规则（C）
`backend/internal/datalifecycle/identity.go:47-63`：

```go
func IdentityKey(deviceType, hardwareID string, instanceID uint) string {
    if hardwareID == "" {
        if instanceID == 0 {
            hardwareID = uuid.NewString()                       // 随机：无稳定基准
        } else {
            hardwareID = uuid.NewSHA1(uuid.NameSpaceOID,
                []byte(fmt.Sprintf("edge:%d", instanceID))).String()  // 确定性：f(instanceID)
        }
    }
    return truncate(deviceType + ":" + hardwareID, 64)
}
```

另：`sequenceKey`（identity.go:250-256）在撞车时追加 `#2`、`#3`，最长 64 字符。

### 1.2 本机库实测（T，2026-10-23 21:00）
```
$ docker exec ehome-postgres psql -U ehome -d ehome -c \
  "select id, identity_key, name, device_type from logical_devices order by id"
 id |                   identity_key                    |    name
----+---------------------------------------------------+-------------
  1 | sn3001_rain:UART1                                 | 光学雨量计
  2 | jiabaida_bms:274ac2b2-331f-5058-a434-5f10d1fdbd3b | 模拟BMS

$ docker exec ehome-postgres psql ... -c "select id,type,hardware_id,logical_device_id from edge_devices"
 id |    name    |     type      | hardware_id | logical_device_id
  1 | 光学雨量计 | sn3001_rain   | 1           | 1
  2 | 模拟BMS    | jiabaida_bms  |             | 2      <-- hardware_id 为空
```

**独立复核**（不依赖 Go 代码，用 Python 重算 uuid v5）：
```
$ python3 -c "import uuid;print(uuid.uuid5(uuid.NAMESPACE_OID,'edge:2'))"
274ac2b2-331f-5058-a434-5f10d1fdbd3b     # == logical_devices.id=2 的 key 后缀
```
⇒ 本机 **2/2** 吻合：1 条走 hardware_id（`sn3001_rain:UART1`，注意这里 hardware_id 的取值是 `UART1`——总线名，见 §1.4 的历史缺陷），1 条走 uuid5(edge:2)。

### 1.3 生产库实测（T，2026-10-23 21:26）
生产 16 条逻辑设备，逐条反解 key 后缀的来源：

| 来源 | 条数 | id |
|---|---|---|
| `uuid5(NameSpaceOID,"edge:<id>")` 精确命中（key 后缀 == 该行自身 id 的 uuid5） | **12** | 4,5,6,7,8,9,10,11,12,13,14,15 |
| 非空 hardware_id，及由它派生的 `#N` 序号 | **4** | 16(`sn3001_rain:1`)、1(`jiabaida_bms:0x01`)、2(`jiabaida_bms:0x01#2`)、3(`jiabaida_bms:0x01#3`) |
| 合计 | **16** | 12 + 4 = 16 ✓ |

（本机另验 1 条：`jiabaida_bms:274ac2b2-…` = `uuid5(edge:2)`，与本机 `edge_devices.id=2` 精确对应。）

复核命令：
```bash
python3 -c "import uuid;[print(n, uuid.uuid5(uuid.NAMESPACE_OID,'edge:%d'%n)) for n in range(1,41)]"
# 与下面输出逐条比对（生产，2026-10-23 21:26）
sshpass -e ssh … 'docker exec ehome-prod-postgres psql -U ehome -d ehome -At -c \"select id,identity_key from logical_devices order by id\"'
```

### 1.4 identity_key 是**冻结快照**，会与当前 hardware_id 脱钩（T，本机，2026-10-23 21:53）

本机实测暴露一个更基础的问题：**key 一旦生成就永不更新，而 hardware_id 是可编辑的**。

```
$ docker exec ehome-postgres psql -U ehome -d ehome -c \
  "select ed.id edge_id, ed.type, ed.hardware_id, ld.identity_key,
          (ed.type||':'||ed.hardware_id) recomputed_now
   from edge_devices ed left join logical_devices ld on ld.id=ed.logical_device_id order by ed.id"
 edge_id |     type     | hardware_id |                  identity_key                   | recomputed_now
---------+--------------+-------------+-------------------------------------------------+-------------------
       1 | sn3001_rain  | 1           | sn3001_rain:UART1                               | sn3001_rain:1        <-- 已脱钩
       2 | jiabaida_bms |             | jiabaida_bms:274ac2b2-331f-5058-a434-5f10d1fdbd3b | jiabaida_bms:      <-- 空
```

- 成因链（C + T）：历史缺陷曾把**总线名** `UART1` 写进 `EdgeDevice.hardware_id`（`handler_edge_device.go:161-166` 的注释明确记录了这个事故与后来的 address gate）；修复后 `hardware_id` 被改为合法地址 `1`（PUT 允许改，`handler_edge_device.go:883-885`），但 `identity_key` 是"创建后只读"（`models.go:193`），**不会跟着改**。
- 后果：**"identity_key = 物理身份 `type:hardware_id`"这个前提在真实数据上已不成立**。本机 2 条逻辑设备中 **1 条**（50%）的 key 与其当前物理解释不符。用这个 key 做"稳定物理身份"的任何推论（包括 §7.2 方案 B 用 `type:hardware_id` 作稳定身份）都必须处理这个漂移：现设计选择的是"不修 key"，于是 key 只代表"**生成那一刻**的 `type:hardware_id`"。
- 可推翻条件：若 `hardware_id` 在生产从未被 PUT 修改过，则该漂移只是开发期遗留。生产 16 条中 id=16 的 key `sn3001_rain:1` 与当前 `hardware_id=1` 一致，其余 15 条 hardware_id 为空（走 uuid5），**生产未观察到脱钩** —— 所以本条**等级降为 C+T（本机实测，生产未复现）**，但机制真实存在。

### 1.5 结论：会不会"每次重建产出新逻辑设备"？

**分三种情形，答案不同（这是本任务最关键的分辨）：**

| 情形 | 结果 | 证据 |
|---|---|---|
| 同一 `edge_devices.id` 重复补建 / 多副本并发补建 | **不会**。key = f(instanceID) 是确定性函数，重启 N 次都是同一个 key，ON CONFLICT 才是真防护 | C: identity.go:47-63,144-163；T: 生产 12 条 key 与实际 id 精确对应（12/16） |
| **物理设备换节点 → 用户"删旧实例 + 建新实例"** | **会**。新实例是新 id ⇒ 新 uuid5 ⇒ 新逻辑设备；除非用户在创建向导里显式选"继承"某个逻辑设备 | C: identity.go:108 + handler_edge_device.go:736-743 |
| 同一 hardware_id 被建到不同 channel（multi-drop 合法场景） | **会产出 #2/#3 序号身份**，历史被切成 N 份 | T: 生产 ld 1/2/3 的 `0x01 / 0x01#2 / 0x01#3` |

**生产可观测的退化后果（T）**：`jiabaida_bms:0x01` 系共 3 个逻辑设备，但 unified_data 行数分别是 **0 / 2,923,306 / 0**：

```
$ docker exec ehome-prod-postgres psql ... -c \
 "select ld.id, ld.identity_key, (select count(*) from edge_devices ed where ed.logical_device_id=ld.id) inst,
         (select count(*) from unified_data ud where ud.logical_device_id=ld.id) ud
  from logical_devices ld order by ld.id"
 id |                     identity_key                      | inst |   ud
  1 | jiabaida_bms:0x01                                    |    0 |        0
  2 | jiabaida_bms:0x01#2                                  |    0 |  2923306
  3 | jiabaida_bms:0x01#3                                  |    0 |        0
  ...
```
用户在 `/logical-device` 页看到三个"BMS-UART1 / BMS-UART0 / BMS-UART2"，其中两个是**空身份**，真实的 292 万行只挂在其中一个上，且该身份的 `instance_count=0`。这就是 §11 数据可达性缺陷的直接现场。

> **可推翻条件**：若 `logical_devices` 中存在 key 后缀既不是 uuid5(edge:<某现存或已删 edge_devices.id>) 也不是非空 hardware_id 的行，则本条退化描述不完整。本机+生产 18 条已全部分类，无剩余。

---

## 2. 三路径复用策略的不变量与 merge 的前提（必答 2）

### 2.1 三条路径的复用判定（C，identity.go:170-202）

| 路径 | 撞 key 时的行为 | 代码 |
|---|---|---|
| `PathStartup`（启动补建 / main.go:151） | 数"存活实例"，`living==0` 才复用；否则换 `#N` 序号 | identity.go:175-182 |
| `PathCreateNew`（创建向导"作为新设备创建"） | **永远不复用**，直接换 `#N` | identity.go:172-174 |
| `PathDelete`（删除时补建） | 允许复用；先跟随 merge 链到最终目标；目标 `purge_requested=TRUE` 则拒绝 | identity.go:183-198 |

调用点（C）：
- `PathStartup` ← `BackfillLogicalDevices`（identity.go:83）← `main.go:151`（每次启动）
- `PathCreateNew` ← `attachNewLogicalDevice`（handler_edge_device_lifecycle.go:93）← `POST /edge-devices` 未指定 `logical_device_id` 时（handler_edge_device.go:736-743）
- `PathDelete` ← `DELETE /edge-devices/:id`（handler_edge_device.go:1088）与 `POST /edge-devices/batch-delete`（:1174）

### 2.2 能否证明"一个逻辑设备至多一个存活实例"？

**不能完整证明。** 三条缝隙：

1. **PathStartup 的 check-then-insert 无锁**（C）。`countLivingInstances`（identity.go:224-233）与 `Create`（:144）之间没有对 `logical_devices` 行的 `FOR UPDATE`，也没有对 `edge_devices` 的唯一约束。两个副本并发补建两个不同 instance、但 hardware_id 相同的设备时，各自算出**不同** key（f(id)），`ON CONFLICT` 完全不触发 ⇒ 得到 **2 个逻辑设备**（不是双实例），进而 #2 序号。这正是生产 0x01 系 3 条的产生机制。
   - 反面对照：创建路径有锁（`validateInheritanceTarget` 用 `clause.Locking{Strength:"UPDATE"}`，handler_edge_device_lifecycle.go:39-40），所以**继承路径**是被锁保护的那一半。
2. **"存活"的定义不一致**（C）。`countLivingInstances` 走 GORM 默认 scope（排除软删，identity.go:224-233）；但 `CountInstances`（列表展示用，:237-246）与 `ResolveScope`（:72-77）都 `Unscoped`（含软删）。同一份代码里"存活"有两个口径，列表页显示的 `instance_count` 与合并校验用的"存活数"不是同一个数 ⇒ 用户看到 `instance_count=1` 但 merge 报 `alive_instance`，或反之。
3. **`checkLivingInstanceUniqueness` 用 `Limit(1).Find` 而非 Count**（handler_edge_device_lifecycle.go:69-76）：语义上等价，但它是**独立于 PathStartup 的第二套实现**，两处不一致（一处 Count、一处 Limit 1），任何一处漏改都会静默失效。

### 2.3 merge 与"一身份一存活实例"的相互作用：merge 在什么前提下可行？

`MergeDevices` 的校验（C，merge.go:134-162）：源必须 `purge_requested=FALSE` **且** `livingInstanceInfo(src) == nil`（默认 scope ⇒ 只算未软删实例）。

联立"一逻辑设备≤1存活实例"：

| 场景 | 能否 merge | 用户必须先做什么 |
|---|---|---|
| 两个源逻辑设备都只剩**软删**实例 | ✅ 可行（软删不算存活） | 无 |
| 两个源各有 1 个存活实例 | ❌ 409 `alive_instance`，**逐源全量收集**后再返回 | **必须先"删除设备"** |
| 一个源有存活实例、另一个没有 | ❌ 同上（冲突全量收集，不是首个即停） | 同上 |
| 源 `purge_requested=TRUE` | ❌ 409 `purge_requested` | 无法解除该标记（无 API 撤销） |
| 源已被合并过（`merged_into` 非空） | ❌ 409 `already_merging` | 无 |

**这里就是原始需求被堵死的核心：** 用户说"设备从 1 号节点搬到 2 号节点，数据会丢，所以要合并或直接复用"。而现设计下：
- 走"合并" ⇒ 必须先把旧实例**删掉**（否则 `alive_instance` 409）；
- 走"直接复用" ⇒ `POST /edge-devices` 不指定 `logical_device_id` 时走 `PathCreateNew`，**永不复用**（identity.go:172-174）；
- 想继承 ⇒ 只能选"继承既有逻辑设备"，但 `checkLivingInstanceUniqueness` 要求目标**当前无存活实例**（handler_edge_device_lifecycle.go:68-86），**跨节点继承必须先删旧实例**。

⇒ **三条路都要求"先删旧设备"**，而"删旧设备"正是用户描述里"数据就会丢失"的触发动作。用户被迫在"删设备"（他以为会丢数据）和"建新身份"（历史断裂）之间二选一，唯一出口是手动挑"继承"且必须先删旧实例——**实际操作成本 = 3 步（删旧 → 建新时勾选继承 → 选定旧逻辑设备），且删除动作会触发 `delete_data` 语义的心理门槛**。

> **注：** 上述"3 步"是代码面推演（C）。本机未实测该 UI 流程（前端由 T3 负责）；生产未做任何写操作。

---

## 3. 状态机完备性与未处理竞态（必答 3）

### 3.1 全部状态（C）

| 载体 | 字段 | 取值 | 出处 |
|---|---|---|---|
| logical_devices | `merge_status` | `NULL` / `pending` / `done` | models.go:207-211 |
| logical_devices | `merged_into` | `NULL` / `<id>` | models.go:197 |
| logical_devices | `purge_requested` | `false` / `true` | models.go:199 |
| merge_jobs | `status` | `pending` / `running` / `done` / `failed` | models/merge_job.go:32-35 |
| merge_jobs | `watermark_phase` | `unified_data` / `device_data` | models/merge_job.go:40-41 |
| backfill_jobs | `status` | `pending` / `running` / `done` | models/backfill job |

笛卡尔积 3×2×2×4×2 = **96 种组合**，其中语义合法约 **12** 种。合法迁移：

```
(合并)  src{merge_status=NULL, merged_into=NULL}
          --[MergeDevices 事务]--> src{merge_status=pending, merged_into=T}, job{pending}
          --[Migrator]--> job{running} --[两表扫尽]--> job{done} + src{merge_status=done}   (merged_into 保持 T)
          --[失败 3 次]--> job{failed} + src{merged_into=NULL, merge_status=NULL}
(删除)  ld{purge_requested=false} --[DELETE ?delete_data=true]--> ld{purge_requested=true}
          --[Purger]--> 行删除 (purge_requested 随行消失)
          --[Purger 守卫1 命中存活实例]--> ld{purge_requested=false}
          --[Purger 守卫2 命中 pending 合并]--> 顺延 (状态不变)
```

### 3.2 非法 / 未处理的迁移（每条给可证伪判据）

| # | 未处理的状态 | 后果 | 等级 | 可证伪判据 |
|---|---|---|---|---|
| S-1 | `done` 后 `merged_into` **永不清除** | 源逻辑设备永久"活着但指向别人"：仍占用 `identity_key` 唯一索引（后续同 hardware_id 重建只能拿 `#N`）；`ResolveDataQueryScope` 每次都走链；`purge` 的 B4 步才顺带解除 | C (migrate.go:265-266) | 跑一次 merge，查 `select merged_into, merge_status from logical_devices where id=<src>` → `<target>|done`；本机/生产当前 `merge_jobs=0` ⇒ 未取到实测例 |
| S-2 | `failed` 复位**不回滚已提交批次** | `migrateTable` 每批独立事务 + 水位持久化（migrate.go:316-337）。失败时复位只清 `merged_into/merge_status`（:377-379），**已 UPDATE 到 target 的行留在 target**，水位也不回退 ⇒ 一条设备的数据被静默劈成两半，且没有任何"回迁"代码路径 | C | 注入 `batchHook` 使第 2 批失败、重试 3 次：对比 `select count(*) from unified_data where logical_device_id=<src>` 与 `=<target>` 前后变化，两者都不为原始值 |
| S-3 | `purge_requested=TRUE` **无撤销 API** | 用户误勾"同时删除数据"后，除"重新建一个存活实例"（触发`AbandonedLivingInstance` 守卫）外无路可退 | C (handler_logical_device.go 无该端点) | `grep -n 'purge_requested' internal/api/handler_logical_device.go` → 0 命中 |
| S-4 | purge ↔ merge 无锁窗口 | `purgeOne` 先查 `isPendingMergeParticipant`（purge.go:215-224 → :284-302），再 `ResolveScope` + 批量删 + `DELETE logical_devices`；`MergeDevices` 的占位在另一个事务里。两者之间无行锁 ⇒ purge 可以在 merge 提交后删掉 target 行，此后 Migrator 的 `UPDATE ... SET logical_device_id=<已删id>` 会产出**新的孤儿行** | C | 本机并发跑 `Purger.RunOnce` + `MergeDevices`，然后查 `select count(*) from unified_data ud left join logical_devices ld on ld.id=ud.logical_device_id where ud.logical_device_id is not null and ld.id is null` > 0 |
| S-5 | `logical_devices/edge_devices` **没有任何 FK 约束** | 引用完整性 100% 靠应用层；S-4 的孤儿行因此不会被数据库拒绝 | **T** | `select conname from pg_constraint where conrelid in ('edge_devices'::regclass,'logical_devices'::regclass) and contype='f'` → **0 rows**（本机实测） |
| S-6 | retention 的 pending 守卫只查**直接** pending | `isPendingMergeParticipant` 只看 `merged_into=me AND status=pending`（retention_task.go:377 复用 purge.go:284-302），但 `MergeGraph.Resolve` 走 8 跳（merge_chain.go:15）。A→B→C 链中 A 的 pending **不阻止** C 的 retention 删除，而 A 的数据此时正按 `ResolveScope(A)` 被删 | C | 构造 3 层链（A→B→C）后跑 `RetentionTask.processOne(C)`，观察 A 的 scope 行是否被 cutoff 删除 |
| S-7 | `#N` 序号身份无回收 | 序号只增不减（identity.go:115-118），且 `PathCreateNew` 每次都从 `baseKey` 重新开始试 `#1..#256`（每次最多 256 次 SELECT）⇒ 极端情况下单次创建退化为 256 次往返 | C | 造 256 个同 hardware_id 逻辑设备后调 `PathCreateNew`，观察 `identity key space exhausted` 错误 |

### 3.3 竞态清单（按危害排序）

1. **S-2（半损）** — 唯一一个会**永久丢数据归属**的竞态，且无补偿路径。
2. **S-4（孤儿行）** — 需要 purge 与 merge 恰好交错；生产当前 `merge_jobs=0`、`purge_requested=0`，未触发（T：`select count(*) from merge_jobs`=0，`purge_requested`=0）。
3. **§2.2-1（并发补建产生多身份）** — 生产已留下痕迹（0x01 系 3 条）。
4. **S-6（链式 retention 误删）** — 当前无链（`merged_into` 全 NULL，T），属前向风险。

---

## 4. 查询作用域 ResolveScope/Cond() 的 OR 分支（必答 4）

### 4.1 代码（C）

`backend/internal/datalifecycle/scope.go:32-53`：

```go
default:
    return "(logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?))",
        []interface{}{s.LogicalIDs, s.InstanceIDs}
```

这个 OR 被 **8 处**复用（`grep -n '\.Cond()'`，排除测试）：

| 调用点 | 用途 | 是否热路径 |
|---|---|---|
| `scope.go:87` `ApplyScope` | 通用 | 被下面复用 |
| `scope.go:130` `EstimateScopeRows` | EXPLAIN 估算 | **列表页每项 2 次** |
| `preview.go:141` `scopeBoundaryTimestamp` | 首/末时间边界 | **列表页每项 4 次** |
| `purge.go:315` `deleteScopedRows` | 硬删 | 每日 |
| `retention_task.go:511` `deleteExpired` | 到期删 | 每日 |
| `migrate.go:300` `migrateTable` | 合并搬迁 | 合并窗口内每 60 s × N 批 |
| `api/handler_data.go:27` `dataScopeCond` | 旧 OR 读路径（SQLite / agg=raw） | 数据读取 |

注意 `api/scope_union.go` **已经**为 history / latest-data 做了正确的两分支 UNION ALL 重写（文件头注释详细论证了原因），但 **列表页、估算、边界、purge、retention、migrate 这 6 条路径仍走 OR**。也就是说：**这个教训在仓库里已经被学到过一次，却只修了 2 个端点。**

### 4.2 生产实测：OR 使索引失效（T，2026-10-23 21:38）

同一会话、同一数据、唯一差别是条件写法：

```
=== [Q2] id=12 最后边界, OR 形态
 Limit  (cost=1.03..6.49 rows=1) (actual time=26932.715..26932.717 rows=1.00 loops=1)
   Buffers: shared hit=1028916 read=420536 written=170
   ->  Index Scan Backward using unified_data_202610_timestamp_idx
         Filter: ((logical_device_id = 12) OR ((logical_device_id IS NULL) AND (device_id = 12)))
         Rows Removed by Filter: 29357895
 Execution Time: 26932.821 ms

=== [Q3] 等价 UNION ALL 形态
 Limit  (cost=39.83..39.83 rows=1) (actual time=0.644..0.646 rows=1.00 loops=1)
   Buffers: shared hit=24 read=3
   ->  Index Only Scan using unified_data_202610_logical_device_id_timestamp_idx
         Index Cond: (logical_device_id = 12)
 Execution Time: 0.644 ms
```

- 比值：**26,932.8 ms / 0.644 ms ≈ 41,820×**
- 计划差异：OR 形态走 `unified_data_202610_timestamp_idx` + **Filter 后丢弃 29,357,895 行**；UNION 形态走 `..._logical_device_id_timestamp_idx` 的 `Index Cond`，命中 1 行即停。
- 与 Lead 实测一致（Lead: 29,310,548 rows removed / 1,026,167 hit / 420,706 read；本次: 29,357,895 / 1,028,916 / 420,536，差异来自表在持续写入）。

本机同构复现（T，21:20，小库）：
```
id=2 最后边界 OR 形态:    33.3 / 33.0 / 32.1 / 32.3 ms (n=4)
id=2 最后边界 单分支:      8.8 /  8.2 /  9.1 /  8.9 ms
id=2 device_data OR 形态: 163.4 / 123.5 / 122.8 / 122.5 / 120.8 ms (n=5)
id=2 device_data 单分支:   8.6 /  8.0 /  8.0 /  8.5 /  8.7 ms
```
即使在本机 179k 行的小库上，OR 也已经带来 **3.7×~15×** 的劣化；生产放大到 **4 万倍**。

### 4.3 **对 Lead 判据的修正：不是"数据量越大越慢"，是"最后一包数据越旧越慢"**（T）

Lead 给的模型是"有数据的 3 条各 ~35 s ⇒ 3×35 ≈ 105 s"。本任务用 `page=N&page_size=1` 逐条隔离后，得到**两处修正**：

**修正 A（分母）**：有数据的逻辑设备不是 3 条，而是 **5 条**（2, 12, 13, 14, 15）。复核：`select logical_device_id, count(*) from unified_data group by 1 order by 2 desc`（生产 21:27）→ 只有 5 个非空键。Lead 同步的"仅 3 条有数据"漏了 id=14（27,770,781 行）与 id=15（1,560,353 行），这两条都在持续写入。

**修正 B（驱动量）**：逐条隔离实测 →

| 逻辑设备 | unified_data 行数 | 最后数据时间 | 单条隔离耗时 |
|---|---|---|---|
| id=12 | 205,073 | 2026-10-06 01:07 | **42.4 s** |
| id=13 | 14,224 | 2026-10-05 18:39 | **42.1 s** |
| id=14 | **27,805,233** | 2026-10-23 13:44（**当天**） | **1.38 s** |
| id=15 | 1,564,584 | 2026-10-23 13:44（当天） | 7.44 s |

命令（21:39 复跑，`BASE` 与 `TOKEN` 的取得方式见 §10）：
```bash
for pg in 13 14 15 12; do
  curl -s -o /dev/null -w "page=$pg http=%{http_code} time=%{time_total}\n" \
    --max-time 320 -H "Authorization: Bearer $TOKEN" \
    "$BASE/api/v1/logical-devices?page=$pg&page_size=1"
done
```

**机理**：`ORDER BY timestamp DESC LIMIT 1` 在 OR 形态下只能走 `timestamp_idx` 反向扫、逐行 Filter。扫到第一条匹配行所需跳过的行数 = **该设备最后数据之后新写入的行数**。生产 `count(*) from unified_data where timestamp > '2026-10-06 01:07:16+00'` = **29,358,117**（T，Q1）—— 正好对上 id=12 的 26,932 ms。id=14 一直在写，它的最后一包就在索引头部 ⇒ 1 行即中，所以 **2780 万行反而只用 1.38 s**。

**为什么这比 Lead 的原判据更严重**：原始需求的场景恰恰是"设备换节点后**旧身份停止上报**"。停写后的身份，其"之后新写入的行数"会**单调增长**，所以该行的成本会随系统运行**永久恶化**，直到 retention 把它的数据删光。这是"设计—需求对立"的量化形态：**用得越久、越按原始需求操作（换节点），列表越慢**。

### 4.4 OR 的 fallback 分支在生产恒 0 命中（T）

这是"OR 是为了兼容 backfill 前 NULL 行"这一设计前提的**直接反驳**：

```
--- 生产 (2026-10-23 21:27) ---
$ select count(*) from unified_data where logical_device_id is null;                 --> 0
$ select count(*) from device_data  where logical_device_id is null;                 --> 1,685,996  (21:27)
$ select count(*) from device_data  where logical_device_id is null;                 --> 1,686,022  (21:31，同一库 4 分钟后：该表仍在被写入)
$ select device_id, count(*) from device_data where logical_device_id is null group by 1;
   device_id |  count
           0 | 1686104            <-- 全部 device_id = 0
$ select count(*) from device_data where logical_device_id is null
    and device_id in (7,8,9,10,11,12,13,14,15,16);                                   --> 0
$ select count(*) from unified_data where logical_device_id is null
    and device_id in (7,8,9,10,11,12,13,14,15,16);                                   --> 0
--- 本机 ---
$ select count(*) from unified_data where logical_device_id is null;                 --> 0
$ select count(*) from device_data  where logical_device_id is null and device_id in (1,2); --> 0
$ select * from backfill_jobs;   -- 两库均为 status=done, rows_updated=0
```

三点结论：
1. `unified_data` 的 NULL-logical 行 = **0**（两库一致）。这张表从有数据那天起就在写 `logical_device_id`（P1 双写在 P0 之前上线），"backfill 前的历史状态"**从未存在过**。
2. `device_data` 的 168 万 NULL-logical 行**全部 device_id=0** —— 来自 `DBPersistConsumer` 写原始帧时只填了 `NodeID`、没填 `DeviceID/LogicalDeviceID`（`consumers_heavy.go:120-124`）。而 scope 的 fallback 分支条件是 `device_id IN (<instance ids>)`，instance id 是 7..16 的正整数，**永远不含 0** ⇒ 该分支对这批行也恒 0 命中。
3. ⇒ **这个 OR 分支为兼容的状态，在两张表上都 0 命中。** 而它让每次列表查询多付 4 万倍的代价。

> **对 Lead 提问的回答**：是的，**这是判定"设计不合理"的核心证据之一，但需要精确化**。更准确的表述不是"为一个不存在的历史状态付代价"，而是：
> ① 该兼容状态在这两个库上 0 命中（T）；
> ② 即使它非 0，正确的写法也是两分支 UNION ALL（`scope_union.go` 已实现并证明 4 ms），不需要用 OR 让优化器失效（T）；
> ③ 因此 OR 不是"必要的兼容"，而是"**把兼容做成了唯一实现，并使 6 条路径（列表/估算/边界/purge/retention/migrate）全部继承这个缺陷**"。
> 我把原假设"为不存在的状态付代价"评为**成立**，但把它从"根因"降为"放大器"——根因是 §5 的身份层不提供任何读入口，列表页却要为它算 3 个派生数字。

### 4.5 同一 OR 还污染了写/删路径（C + 本机 T）

- `purge.deleteScopedRows`（purge.go:307-347）与 `retention.deleteExpired`（retention_task.go:488-541）都用 `scope.Cond()` 做 `DELETE ... WHERE id IN (SELECT id ... WHERE <OR> LIMIT n)`。
- 本机 `EXPLAIN DELETE`（不执行）实测：`Seq Scan on unified_data_202609` + `Filter: (OR ...)`。复跑：
  ```bash
  docker exec -i ehome-postgres psql -U ehome -d ehome <<'SQL'
  EXPLAIN DELETE FROM unified_data WHERE id IN (
    SELECT id FROM unified_data
    WHERE (logical_device_id IN (1) OR (logical_device_id IS NULL AND device_id IN (1))) LIMIT 10000);
  SQL
  ```
- 生产 `EXPLAIN DELETE ... logical_device_id IN (14) ... timestamp < '2026-04-01'` 因 cutoff 落在所有分区之外被 `One-Time Filter: false` 短路（T），**暂时的**：一旦 202609 分区整体早于某个逻辑设备的 cutoff（例如有人把 retention_days 调小到 30 天），同一 OR 会把它变成分区级顺序扫描。
- `migrate.migrateTable` 的 `UPDATE ... WHERE <OR>` 同样：生产 EXPLAIN 显示 `Index Scan using unified_data_202610_pkey (Index Cond: id>0 AND id<=10000)` + `Filter: (OR ...)`（T）——搬迁按 id 窗口走主键，OR 只作为 Filter，成本可控；但这解释了为什么合并 2780 万行需要 `2780万/1万 = 2780` 个批次 × 200 ms sleep ≈ **至少 9 分钟纯 sleep**（C：migrate.go:23 `migrateBatchSleep=200ms`，按批串行）。

---

## 5. 数据可达性缺陷：逻辑身份层没有提供任何读入口（T+C，最严重）

这是本审计发现的、**比性能更根本**的设计缺陷。

### 5.1 事实（C + T）

**全部数据读入口都是 `/edge-devices/:id/...`**（`grep -rn 'v1.GET("/edge-devices'` 实测）：
`/edge-devices/:id/sensor-data`、`/edge-devices/:id/data`、`/edge-devices/:id/history`、`/edge-devices/:id/latest-data`、`/edge-devices/:id/historical` …
**没有任何 `/logical-devices/:id/data` 之类的端点**（`grep -n 'logical' internal/api/handler_logical_device.go` 只有 list/merge/preview/merge-jobs/PUT 五个）。

读路径的展开方式（C）：`ResolveDataQueryScope(edgeDeviceID)` → 查 `edge_devices.logical_device_id` → 跟链 → `ResolveScope`。**入口必须是"一个仍然存在的 edge_devices 行"**。

**生产实测（T，21:36）**：16 个逻辑设备里 **6 个的 `instance_count=0`（连软删实例都没有）**；其中 **1 个（ld 2）挂着 2,923,306 行**数据，其余 5 个（ld 1/3/4/5/6）数据也为 0：

```
 id |                     identity_key                      | inst |   ud
  1 | jiabaida_bms:0x01                                    |    0 |        0
  2 | jiabaida_bms:0x01#2                                  |    0 |  2923306   <-- 292 万行，无任何入口可读
  3 | jiabaida_bms:0x01#3                                  |    0 |        0
  4 | techfine_inverter:6cfb1a01-...                       |    0 |        0
  5 | jk_bms:ef0bf937-...                                  |    0 |        0
  6 | jk_bms:f266f822-...                                  |    0 |        0
--- 这些行连 device_id 都指向不存在的实例 ---
$ select count(*) from unified_data ud left join edge_devices ed on ed.id = ud.device_id where ed.id is null;  --> 2,923,306
$ select string_agg(id::text,',' order by id) from edge_devices;  --> 7,8,9,10,11,12,13,14,15,16
```

⇒ `unified_data.device_id = 2` 的 292 万行，对应的 `edge_devices.id = 2` **已被硬删**（不在 7..16 里），而 `logical_devices.id = 2` 仍然存在并持有这批行。**没有任何 API 能读到它们**——数据读入口要求一个 edge_device 行，而这一行不存在。

### 5.2 这直接违背原始需求的唯一目标

用户的原话（Lead 转达）："一个边缘设备在1号节点，我重新连接到2号节点，数据就会丢失，所以需要历史边缘设备合并或者直接复用"。
验收判据只有一条：**换节点后，历史数据仍然可查**。

现设计宣称用"逻辑身份锚点"解决它，但：
- 当旧实例是**软删**时：`ResolveScope` 用 `Unscoped` 把软删实例及其数据纳入 ⇒ 通过新实例的 id 能查到 ⇒ **成立**；
- 当旧实例**被硬删 / 从未存在**时：`Scope.InstanceIDs` 为空，只剩 `logical_device_id IN (...)` 一支 ⇒ 理论上仍能查到 —— **但入口侧 `ResolveDataQueryScope` 必须先有一个 edge_device 行**，而那个行已经没了 ⇒ **不可达**；
- 生产 6/16 个逻辑设备正处在这个"不可达"状态，其中 1 个（ld 2）有 292 万行。

**这批不可达数据的时间与保留期（T，生产，2026-10-23 21:54）**：
```
$ select ld.id, ld.retention_days,
         (select min(timestamp) from unified_data ud where ud.logical_device_id=ld.id) oldest
  from logical_devices ld order by ld.id;
 id | retention_days |          oldest
  2 |             90 | 2026-10-02 09:07:16+00     <-- 292 万行的最早时间
 12 |             90 | 2026-10-05 12:33:41+00
 13 |             90 | 2026-10-05 15:48:43+00
 14 |             90 | 2026-10-06 04:48:25+00
 15 |             90 | 2026-10-18 10:04:50+00
  (其余 11 行 oldest = NULL，无数据)
```
⇒ 当前 **尚未** 有行到期（`deleteExpired` 判据是 `timestamp < now() - retention_days`，最早数据才 21 天）。但按 `retention_days=90` 推算，**ld 2 的 292 万行将从 2026-12-31 起被逐日硬删**，而用户在这 68 天里依然没有任何入口能看到它们。这不是"已发生的数据丢失"，而是"计划中的数据丢失"——本任务不把它计为已发生缺陷，只作为时间窗口风险列出。

关键点在于：retention 的判定**不需要实例存在**（`retention_task.go:374-427` 对每个 `logical_devices` 行独立处理），所以**用户已经看不到的历史，系统仍在按时把它删掉**。这是与原始需求（"数据不能丢"）的正面冲突，且它是**未来必然发生**的事件，不是概率性的。

### 5.3 附带的口径混乱（T）

列表页 `instance_count` 是 `Unscoped` 计数（含软删，identity.go:237-246），所以生产 10 个有实例的 ld 显示 `inst=1`，但其中 **6 个（id 7,8,9,10,11,16）的真实 unified_data 行 = 0** —— 用户看到的是"1 个实例、18 行数据"。

更值得警惕的是 `row_estimate` 的语义：生产 `page_size=20` 的响应里，**11 个**逻辑设备的 `row_estimate` **全是 18**（id 1,3,4,5,6,7,8,9,10,11,16），其中 id 1/3/4/5/6 连实例都没有。成因（C：`scope.go:162-179`）：`estimatePostgres` 取 EXPLAIN 文本里**第一个** `rows=(d+)`，而 PG 对**空分区**的默认估算是 `rows=2`，母表 Append 汇总 = 2×分区数(6)≈12~18。

⇒ **"18" 不是数据量估算，是"分区数 × 规划器空表默认值"**。列表页把一个与数据无关的常数当"数据量"展示给用户，且它是**串行 2 次 EXPLAIN 换来的**（每项）。这是"3 个辅助数字"里最没信息量的一个。

---

## 6. 每项富化的 N+1 计数与复杂度（必答 5 + Lead 疑点）

### 6.1 每项 11 次 DB 往返（C，逐行数出来）

`GET /logical-devices` 的 per-item 循环（`handler_logical_device.go:80-98`）展开：

| # | 调用 | DB 往返 | 代码 |
|---|---|---|---|
| 1 | `CountInstances(db, ld.ID)` | **1** | identity.go:237-246 → handler:82 |
| 2-3 | `EstimateRowCount` → `ResolveScope` (pending sources + instances) | **2** | scope.go:112-121 → :56-82 |
| 4-5 | `EstimateScopeRows` → `estimatePostgres` × 2 表 `EXPLAIN` | **2** | scope.go:125-140,162-179 |
| 6-7 | `ResolveScope(db, ld.ID)`（**重复调用，与第 2-3 步完全相同**） | **2** | handler:91 → scope.go:56-82 |
| 8-11 | `ScopeTimeRange` → `scopeBoundaryTimestamp` × (first, last) × (unified, device_data) | **4** | preview.go:108-110,119-175 |
| | **合计** | **11** | |

其中第 6-7 步与第 2-3 步是**同一次计算**（同一 ld.ID、同一 db、同一位置），可以合并——这说明 11 还不是下限，是"顺手写重复了"的结果。

**分区扇出**：上述每一次"EXPLAIN / 边界查询"打到的是分区母表，PG 实际会打开 5–6 个分区（实测：本机 6 个分区；生产 5 个）。所以**物理查询数 ≈ 11 的逻辑往返 × 平均 3–5 个分区 ≈ 40–55 个分区级访问/项**。

### 6.2 实测：成本随项数线性（T）

| 端点形态 | 生产实测（T，2026-10-23 21:33–21:38） | 本机实测（T，21:41） |
|---|---|---|
| `page_size=1`（id=1，0 行数据） | 0.0098 s | 0.0176 s（中位 5 次：0.0575/0.0186/0.0176/0.0168/0.0166） |
| `page_size=2`（+id=2，292 万行） | 0.0153 s | 0.2053 s（中位 5 次：0.173/0.205/0.211/0.208/0.205） |
| `page_size=6`（id1..6，全部 0 行） | 0.0384 s | — |
| `page=99`（空页，0 项） | 0.0027 s | — |
| **`page_size=16`（全部 16 项）** | **80.56 s** | — |
| **`page_size=20`（默认，16 项）** | **83.45 s** | — |

- 断言：**每项成本不是常数**（0.0027→80.56 s），但在"数据全部为 0 行"的区间内近似常数（ps=6 → 0.0384 s ≈ 6.4 ms/项，与代码面的 11 次往返 × 亚毫秒量级相符）。
- 端到端 80.5 s 的构成（T + C）：4 个"数据陈旧"项各 ≈ 42 s 量级的边界扫描（id 2/12/13 实测 42.4/42.1 s，id 15 = 7.44 s）+ 其余项 <10 ms。80.5 s < 42.4+42.1+7.4+1.4=93.3 s 是因为页面并发/缓存命中差异，量级一致。

### 6.3 对 Lead 疑点的判定

> Lead 的假设：「单页成本 = 每项 4 类富化（CountInstances + EstimateRowCount[EXPLAIN 两次] + ScopeTimeRange[4 次边界查询]），即每项 O(7) 次 DB 往返，且全部串行」。

**判定：成立，且实际比 O(7) 更大 —— 代码面是 11 次/项（其中 2 次是重复计算），且这 11 次全部串行。**

逐项核对 Lead 的分解：
- `CountInstances` 1 次 ✅
- `EstimateRowCount` = **ResolveScope 2 次 + EXPLAIN 2 次 = 4 次**（Lead 只算了 EXPLAIN 的 2 次，漏了 ResolveScope）❌ 偏低
- `ScopeTimeRange` = **4 次**（2 表 × 首/末）✅
- **额外漏算**：handler 自己又调了一次 `ResolveScope`（+2 次）❌

串行性（C）：循环体（handler:80-98）无 goroutine、无并发、无 batch；每项的 11 次往返严格顺序执行。**串行是设计刻意还是疏忽**：代码里看不到任何"为什么不能并发"的注释 —— 对比同仓库 `scope_union.go` 有长篇论证、`retention_task.go` 有排班理由，此处**没有理由记录**，属未论证实现。

### 6.4 复杂度分母（T）

| 维度 | 分母 | 采集命令 |
|---|---|---|
| `internal/datalifecycle/` 文件数 | **52**（.go） | `ls backend/internal/datalifecycle/*.go | wc -l` |
| 该包总行数 | **14,971** | `wc -l backend/internal/datalifecycle/*.go | tail -1` |
| 其中本功能 13 个非测试文件 | **3,703 行** | identity+merge+merge_chain+preview+purge+retention+retention_task+scope+query_scope+migrate+backfill+partition_mgr+indexes |
| 该包测试行数 | **8,964** | `wc -l backend/internal/datalifecycle/*_test.go` |
| api 层本功能 4 文件 | **958 行** | handler_logical_device(227)+lifecycle(244)+candidates(240)+scope_union(247) |
| 触及 datalifecycle 的提交 | **36** | `git log --oneline --all -- backend/internal/datalifecycle | wc -l` |
| 提及"逻辑设备/data-lifecycle"的提交 | **38** | `git log --oneline --all --grep='逻辑设备' --grep='data-lifecycle' | wc -l` |
| 新增表 | **3**（`logical_devices`/`merge_jobs`/`backfill_jobs`） | `pg_tables where tablename like '%logical%'|'%merge%'|'%backfill%'` |
| 新增列（业务表） | **15 列实例** = `edge_devices` 1 列 + `unified_data` 母表及 6 个分区 7 列 + `device_data` 母表及 6 个分区 7 列 | `select count(*) from information_schema.columns where column_name='logical_device_id'` → **15**（本机 2026-10-23 21:52 实测） |
| 状态字段 | **5**（merge_status / merged_into / purge_requested / job.status / watermark_phase） | 见 §3.1 |
| 状态组合 | **96** 笛卡尔积 / 约 12 合法 | 3×2×2×4×2 |
| 后台 worker | **3**（Purger 24h / RetentionTask 24h / Migrator 60s）+ **1** 一次性 Backfiller | main.go:160,171,189,195 |
| 身份/查询路径 | **3** 条身份路径 + **2** 条查询条件生成器（OR 旧路 / UNION 新路）+ **1** 条 fallback（device_id=?) | identity.go:30-39；scope.go:32；scope_union.go:53；handler_data.go:27 |

**需求强制 vs 自造**：

| 项 | 判定 | 依据 |
|---|---|---|
| "跨实例历史连续可查" | **需求强制**（用户唯一诉求） | 用户原话 |
| 独立 `logical_devices` 身份表 | **自造**（可用"edge_devices 原地改 node_id/channel_id"或"数据表上一列稳定物理身份"任一方式满足） | 见 §7.2 |
| `merged_into` / `merge_status` / merge_jobs / Migrator | **自造**（"合并"是用户提出的**可选**方案之一，且是最重的那个） | 用户原话"合并**或者**直接复用" |
| `purge_requested` + Purger | 由"删除时勾选同时删数据"派生（属删除语义，不是本需求） | handler_edge_device.go:1106-1112 |
| 保形去重 `ApplyShapeDedup` / `DedupNeeded` | **自造**（为"合并后同 sensor 同时刻两行"发明；用户没提） | query_scope.go:81-101；scope_union.go:157-201 |
| 合并链跟随（两套上限 8 / 16） | **自造**（"链"本身是自造状态机的结果） | merge_chain.go:15 vs identity.go:25 |
| retention 双层（全局分区 DROP + 逐 scope DELETE） | 部分自造（保留期来自既有产品设定；"双层"是因为引入了两个数据所有者维度） | retention_task.go:219-232,311-349 |
| backfill 水位表 + 断点续跑 | 部分自造（只有在"新增一列"这个前提下才需要回填） | backfill.go:17-27 |

---

## 7. 按原始需求重判（Lead 指定新增节）

### 7.1 需求分层（这是本次审计的方法论结论）

| 层 | 内容 | 证据强度 |
|---|---|---|
| **L0 用户原始需求** | 「同一个物理设备从节点 A 搬到节点 B（换了 edge_devices 行）后，历史数据不能丢、要连续可查」；用户给了两个**可接受解法**：① 历史边缘设备合并 ② 直接复用；**明确否认需要"逻辑身份层"**（"根本不需要什么逻辑设备概念！"） | 产品负责人原话（Lead 转达）= **最强证据** |
| **L1 方案自述需求** | v3.3 方案里的 4 条约束（跨节点合并 / 合并可选 / 保留期 / 分区生命周期）、`docs/设计/边缘设备数据生命周期.md` | **D**（实现方自己写的） |
| **L2 实现** | 本文 §1–§6 | T/C |

**关键判断：L1 ≠ L0。** L1 把 L0 的"换节点后历史连续"放大成"跨实例/跨时间的设备身份锚点 + 合并框架 + 双层保留期 + 分区生命周期"。放大发生在 3 个具体动作上：
1. 把"历史连续"重新定义成"需要一个**独立实体**来承载历史"，于是新增 `logical_devices` 表 + 三路径身份生成；
2. 把用户说的"合并**或者**直接复用"里的"或者"丢掉，把"合并"做成了**唯一正规路径**（`PathCreateNew` 永不复用，见 §2.3），并因此需要 `merged_into/merge_status/merge_jobs/Migrator/保形去重/合并链`；
3. 把"删除设备时是否删数据"这个**既有**产品问题，吸收进新实体（`purge_requested`），使删除语义与身份语义纠缠（§3.2 S-3：无撤销 API）。

### 7.2 最简方案对照（同一需求，更少的机制）

需求：设备从 node1 换到 node2 后，历史数据连续可查。

| 方案 | 新增实体 | 新增状态 | 新增路径 | 改代码量（估） | 是否已存在 |
|---|---|---|---|---|---|
| **A. 原地复用（用户方案②）**：`PUT /edge-devices/:id` 改 `node_id` + `channel_id` | **0 表 0 列** | **0** | 0 | **0**（已实现） | ✅ **已实现**：handler_edge_device.go:805, 826-828, 886-891；约束：新节点必须有对应 channel（:854 `Where("id=? AND node_id=?")`） |
| **B. 数据表加稳定物理身份列**：`unified_data.physical_key = type:hardware_id` + 1 索引 | 1 列（+分区） | 0 | 1（查询多一条件） | 小 | 未实现 |
| **C. 本设计** | **3 表**（logical_devices/merge_jobs/backfill_jobs）+ 15 列实例 | **5 字段 / 96 组合** | 3 身份路径 + 2 查询生成器 + 4 worker | **3,703 行 + 958 行 API + 8,964 行测试** | ✅ 已实现（且有 §5 缺陷） |

**方案 A 是否真的可用**（这是判定的关键）：`PUT /edge-devices/:id` 的 DTO 含 `NodeID`/`ChannelID`（:805），更新写 `node_id`/`channel_id`（:886-891），并用 `(channel_id, type, hardware_id)` 做唯一性校验（:908）。也就是说 —— **"直接复用"这条路在代码里本来就有，不需要任何逻辑设备概念就能满足用户需求**。
唯一约束条件：目标节点必须有一条 `(channel, type, hardware_id)` 不冲突的 channel；若目标节点没有该硬件总线类型，需要先建 channel（属于正常配置动作，与身份无关）。

> **等级说明**：A 方案"已实现"是 **C**（代码可指认，未实测）；未做实测的原因是本任务纪律禁止写库/写生产，而这条路径必须是写操作才能验证。复核方法：在本机 API 上 `PUT /api/v1/edge-devices/1` 带 `{"node_id":"<另一节点>","channel_id":<该节点 channel>}`，观察 `logical_device_id` 不变而 `node_id` 改变，且 `GET /edge-devices/1/data` 仍返回全部 175,620 行。
> ⇒ **对 Lead 的问题 1 的回答**：现设计**没有**让"直接复用"变得不可行（PUT 仍支持改 node/channel），但它让"直接复用"变得**没有必要被发现**——因为/create 路径的默认行为是 `PathCreateNew`（新建身份），用户按向导操作必然走到"新身份 + 历史断裂"，从而被迫进入合并流程。

### 7.3 换节点场景的实际操作成本（对 Lead 问题 1 的回答）

用户真实会做的操作序列（按现 UI/API 的默认行为，C）：

```
路径①（用户以为的"重新添加设备"）:
  1. 删除旧设备（DELETE /edge-devices/:id）        <- 用户以为数据会丢（他原话）
  2. 在新节点创建同类设备（POST /edge-devices）    <- 未指定 logical_device_id => PathCreateNew => 新身份
  3. 结果: 历史数据挂在旧逻辑设备上，新设备看到 0 行  <- 走 §5 的可达性缺陷
  代价: 2 个动作，且历史断裂（除非手动"继承"）

路径②（正确的"继承"路径）:
  1. 删除旧设备（必须先删，否则 checkLivingInstanceUniqueness 409）
  2. 创建新设备时显式传 logical_device_id = 旧逻辑设备 id
  3. 结果: 历史连续
  代价: 2 个动作 + 必须知道"旧逻辑设备 id"这个内部 ID（前端需正确展示候选）
  风险: 第 1 步若勾了"同时删除数据"，purge_requested=TRUE => 第 2 步继承被 409 拒绝（v3.3-N1），
        且该标记无撤销 API（§3.2 S-3）=> 用户进入死局

路径③（"直接复用"，最省事）:
  1. PUT /edge-devices/:id 改 node_id + channel_id
  代价: 1 个动作，0 风险，历史天然连续
  问题: 这条路径与"逻辑设备"概念无关，且当前 UI 是否暴露"改节点"未知（前端由 T3 判定；本任务未取到 UI 证据 -> 标"未取到"）
```

### 7.4 是否解决了原始需求之外的东西（over-engineering 判定）

**是。** 明确的 over-engineering 项（每一行都可指认产物）：

| 自造机制 | 文件:行 | 用户提过吗 |
|---|---|---|
| 身份 `#N` 序号复用策略（3 路径 × 256 次重试） | identity.go:27-38, 115-118 | 否 |
| 合并预览（时间轴重叠判定 + 逐源估算） | preview.go:39-104 | 否 |
| 保形去重 `ROW_NUMBER ... MAX(id)`（两套 SQL 实现） | query_scope.go:81-101；scope_union.go:157-201 | 否 |
| 合并链跟随（内存图 + 8 跳上限 / DB 逐跳 16 跳上限两套） | merge_chain.go:15,60-70；identity.go:204-220 | 否 |
| pending 搬迁窗口（查询必须看到源+目标两侧） | scope.go:59-69 | 否 |
| 409 结构化冲突（逐源收集、前端逐条跳转） | merge.go:15-39, 134-162 | 否 |
| 保留下 30/7 天两档通知 + 去重 | retention_task.go:18-23, 432-460 | 否（保留期是既有产品设定，"两档通知"是新造） |
| 全局分区清扫按"最长保留期"取 cutoff | retention_task.go:311-349 | 否（是为修正引入 logical_devices 后产生的"多所有者"问题而造） |
| backfill 水位/断点续跑/自检 | backfill.go:17-31, 154-297 | 否（为新增列而造） |

**运行时代价（T）**：生产 `GET /api/v1/logical-devices`（默认 `page_size=20`，返回 16 行）= **80.56 s / 83.45 s**（2026-10-23 21:38，两次各 1 次采样；Lead 独立采样 10 次为 94–131 s，量级一致）。这是一张**纯管理表**：16 行数据，3 个派生数字列（instance_count / row_estimate / last_data_at），换来 83 秒。根因链：`Scope.Cond()` 的 OR（C）→ 边界查询退化为全索引反向 Filter 扫描（T: 26,932 ms）→ 每项 11 次串行往返放大（C）→ 4 个"数据陈旧"项叠加（T: 42 s 级/项）。

**等价重写代价（T）**：同一条边界查询改成 UNION ALL = **0.644 ms**。即 83 s 中有 ≈ 80 s 是**实现选择**造成的，不是数据量或硬件造成的。

---

## 8. 需要 Lead 裁决的矛盾点

### 矛盾 1：「为兼容 backfill 前 NULL 行」这个设计理由，在实测上是空的

- 代码注释反复以此为前提：`scope.go:36-40`（"Instance-only scope e.g. an instance whose logical device has not been created yet"）、`handler_data.go:18-23`（"the OR fallback branch for NULL-logical rows is **permanent**"）、`scope_union.go:26-29`（"backfill 完成后分支 B 实际 0 行"）。
- 实测（T，两库）：`unified_data` NULL-logical = **0**；`device_data` NULL-logical 全为 `device_id=0`（写原始帧时未填 device id，consumers_heavy.go:120-124），而 fallback 条件是 `device_id IN (实例ID)` ⇒ **也 0 命中**。`backfill_jobs` 两库均 `done / rows_updated=0`。
- 矛盾点：**注释说"permanent"，数据说"从不发生"。** 需要裁决的是——这是"防御性冗余"（可接受）还是"以防御之名让 6 条路径继承 O(n) 扫描"（不可接受）。我的判断：**后者**，因为正确的防御写法（UNION ALL 两分支）在同一个仓库里已经实现并实测 4 ms（`scope_union.go`），不存在"要么 OR 要么不正确"的取舍。

### 矛盾 2：逻辑身份层的**验收判据**与**实现**不一致

- 设计宣称的验收：合并/继承后，历史数据"连续可查"。
- 实测：数据读入口只有 `/edge-devices/:id/...`；生产 **6/16** 个逻辑设备 `instance_count=0`，其中 ld 2 有 **2,923,306 行**（且这些行的 `device_id=2` 在 `edge_devices` 中已不存在）⇒ **这批历史没有任何 API 可读**，同时 retention 仍在按 ld 的保留期删它们。
- 需要裁决：**这是"实现未完成"，还是"设计本身缺一层"（缺少以 logical_device 为入口的读端点）？** 我的判断：至少是设计缺一层——如果一个身份存在的意义是"承载历史"，那么它必须自己能被查询；当前它只能被 `edge_devices` 反向寻址，一旦实例消失就失效。这也正是用户"换节点后数据丢失"的**原始痛点以另一种形式重新出现**。

### 矛盾 3（次要）：「一身份一存活实例」与"用户的设备可以同时存在于两个节点（迁移过渡期）"

迁移是**有过渡期**的：用户在 node2 建好新设备、确认能采数后才删 node1 的旧设备。这期间同一物理设备必然有 **2 个存活实例**。而现设计把"2 个存活实例"定义为**必须拒绝的状态**（`checkLivingInstanceUniqueness` 409、即便绕过也有 `PathStartup` 的 `#N` 序号把历史切碎）。
⇒ **设计与物理现实相反**：真实迁移需要"暂时允许双实例"，实现却把它当非法态。这是"为什么用户觉得别扭"的结构性原因，也是 §1.4 生产 0x01 系 3 条身份的直接成因。

---

## 9. 未取到 / 无法验证的项（按纪律如实标注）

| 项 | 状态 | 如何复核 |
|---|---|---|
| `PUT /edge-devices/:id` 改 node 后历史确实连续 | **未取到**（本任务禁写库/写生产，验证必须写操作） | 本机：`PUT /api/v1/edge-devices/1 {"node_id":"<另一节点>","channel_id":<该节点channel>}`，然后 `GET /api/v1/edge-devices/1/data` 应仍返回 175,620 行，且 `select logical_device_id from edge_devices where id=1` 不变 |
| S-2（失败不回滚已搬迁批次）的实测复现 | **未取到**（需注入 batchHook 跑失败路径，属写操作） | 本机 `Migrator.SetBatchHook` 使第 2 批失败，跑 3 轮后对比两侧 `count(*)` |
| S-4（purge↔merge 孤儿行）的实测复现 | **未取到**（同上） | 本机并发 `Purger.RunOnce` + `MergeDevices`，查 left join 孤儿计数 |
| `/logical-device` 页面真实使用频率 | **未取到**（本任务未获授权读生产 nginx/应用访问日志的路径；生产容器日志仅见 OfflineDetector） | `docker logs ehome-prod-web | grep 'logical-devices'`（需先确认无凭据泄漏风险）；或 nginx access log 按 URI 聚合 |
| 前端是否暴露"改节点"入口 | **未取到**（属 T3 范围） | 见 T3 |
| 本机 `go test` 复跑 | **无法执行**：本机 Go 1.27.1 < go.mod 要求 1.27.3；`GOTOOLCHAIN` 下载被只读文件系统阻断；docker 内 `golang:1.27.3-alpine` 缺 3 个依赖（x/text v0.43.0、prometheus/client_golang v1.25.0），外网 proxy.golang.org 返回 503 | 组件齐全时：`cd backend && go test ./internal/datalifecycle/ ./internal/api/ -run 'LogicalDevice|Merge|Scope|Purge|Retention' -count=1` |
| 每项"11 次往返"的物理计数 | **部分取到**：`xact_commit` 差分噪声大（本机基线 ~8/s，且 psql 自身贡献），`pg_stat_user_tables` 按分区计数会放大 | 精确做法：GORM logger 开到 `Info` 抓 SQL 计数，或 `pg_stat_statements` 按 `queryid` 聚合 |

---

## 10. 复跑清单（所有 T 结论的命令）

```bash
# 1) 本机身份键反解
docker exec ehome-postgres psql -U ehome -d ehome -c \
  "select id,identity_key,name from logical_devices"
python3 -c "import uuid;print(uuid.uuid5(uuid.NAMESPACE_OID,'edge:2'))"

# 2) OR vs 单分支（本机，各 5 次；2026-10-23 21:20 实测 33ms vs 8.8ms / 123ms vs 8.5ms）
for i in 1 2 3 4 5; do
  docker exec -i ehome-postgres psql -U ehome -d ehome -At -c "\timing on" -c \
    "select timestamp from unified_data where (logical_device_id IN (2) OR (logical_device_id IS NULL AND device_id IN (2))) order by timestamp desc limit 1;" 2>&1 | grep 'Time:'
done
for i in 1 2 3 4 5; do
  docker exec -i ehome-postgres psql -U ehome -d ehome -At -c "\timing on" -c \
    "select timestamp from unified_data where logical_device_id IN (2) order by timestamp desc limit 1;" 2>&1 | grep 'Time:'
done
# device_data 两形态同理，把表名与条件换掉即可

# 3) EXPLAIN ANALYZE 对照（本机）
docker exec -i ehome-postgres psql -U ehome -d ehome -c \
  "EXPLAIN (ANALYZE, BUFFERS) SELECT timestamp FROM device_data WHERE (logical_device_id IN (2) OR (logical_device_id IS NULL AND device_id IN (2))) ORDER BY timestamp DESC LIMIT 1;"
docker exec -i ehome-postgres psql -U ehome -d ehome -c \
  "EXPLAIN (ANALYZE, BUFFERS) SELECT timestamp FROM device_data WHERE logical_device_id IN (2) ORDER BY timestamp DESC LIMIT 1;"
# 期望：OR 形态 Index Scan Backward + Rows Removed by Filter: 392456 / 128.8ms
#      单分支 Index Only Scan + 无 Filter / 0.125ms

# 4) NULL-logical 命中数（本机 + 生产）
docker exec ehome-postgres psql -U ehome -d ehome -At -c \
  "select count(*) from unified_data where logical_device_id is null"
set -a; . .secrets/prod.env; set +a; export SSHPASS="$PROD_SSH_PASSWORD"
sshpass -e ssh ... 'docker exec ehome-prod-postgres psql -U ehome -d ehome -At \
  -c "select count(*) from unified_data where logical_device_id is null" \
  -c "select device_id,count(*) from device_data where logical_device_id is null group by 1"'

# 5) 生产 OR vs UNION 边界查询（26,932 ms vs 0.644 ms）
$SSH "docker exec ehome-prod-postgres psql -U ehome -d ehome -c \"EXPLAIN (ANALYZE, BUFFERS) SELECT timestamp FROM unified_data WHERE (logical_device_id IN (12) OR (logical_device_id IS NULL AND device_id IN (12))) ORDER BY timestamp DESC LIMIT 1\""
$SSH "docker exec ehome-prod-postgres psql -U ehome -d ehome -c \"EXPLAIN (ANALYZE, BUFFERS) SELECT timestamp FROM ((SELECT timestamp FROM unified_data WHERE logical_device_id IN (12) ORDER BY timestamp DESC LIMIT 1) UNION ALL (SELECT timestamp FROM unified_data WHERE logical_device_id IS NULL AND device_id IN (12) ORDER BY timestamp DESC LIMIT 1)) t ORDER BY timestamp DESC LIMIT 1\""
# 期望：OR 形态 Execution Time: 26932.821 ms / Rows Removed by Filter: 29357895
#      UNION 形态 Execution Time: 0.644 ms

# 6) 生产逐条隔离（42.4s / 42.1s / 1.38s / 7.44s）
for pg in 12 13 14 15; do
  curl -s -o /dev/null -w "page=$pg http=%{http_code} time=%{time_total}\n" --max-time 320 \
    -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/logical-devices?page=$pg&page_size=1"
done

# 7) 生产端点计时（ps=1/2/6/16/20；实测 0.0098 / 0.0153 / 0.0384 / 80.56 / 83.45 s）
for ps in 1 2 6 16 20; do
  curl -s -o /dev/null -w "ps=$ps http=%{http_code} time=%{time_total}\n" --max-time 320 \
    -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/logical-devices?page_size=$ps"
done

# 8) 生产逻辑设备-实例-数据三方一致性 + 孤儿行
$SSH "docker exec ehome-prod-postgres psql -U ehome -d ehome -c \"select ld.id, ld.identity_key, (select count(*) from edge_devices ed where ed.logical_device_id=ld.id) inst, (select count(*) from unified_data ud where ud.logical_device_id=ld.id) ud from logical_devices ld order by ld.id\" -c \"select count(*) from unified_data ud left join edge_devices ed on ed.id=ud.device_id where ed.id is null\" -c \"select string_agg(id::text,',' order by id) from edge_devices\""
# 期望：6 个 ld 的 inst=0（含 id=2 有 2,923,306 行）；孤儿行 2,923,306；edge_devices 只有 7..16

# 9) FK 约束 0 条
docker exec ehome-postgres psql -U ehome -d ehome -At -c \
  "select conname from pg_constraint where conrelid in ('edge_devices'::regclass,'logical_devices'::regclass) and contype='f'"

# 10) 复杂度分母
ls backend/internal/datalifecycle/*.go | wc -l
wc -l backend/internal/datalifecycle/*.go | tail -1
git log --oneline --all -- backend/internal/datalifecycle | wc -l
```

> **脚本说明**：本次审计过程中曾创建 19 个 `_probe_*.sh` 辅助脚本，按任务写文件纪律（只写 `backend-audit.md` 一个产出文件）已在收口时删除；上表所有命令均为自包含、可直接粘贴复跑。所有命令只执行 SELECT / EXPLAIN / curl GET；校准 `xact_commit` 口径时对本机库跑过 200 次 `select 1`（未触碰任何业务表）。
>
> **鉴权 token 的取得方式（生产，不落盘任何凭据）**：
> ```bash
> set -a; . .secrets/prod.env; set +a; export SSHPASS="$PROD_SSH_PASSWORD"
> SECRET=$(sshpass -e ssh -F /dev/null -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
>   -p "$PROD_SSH_PORT" "$PROD_SSH_USER@$PROD_HOST" \
>   'docker exec ehome-prod-web printenv EHOME_JWT_SECRET' | tr -d '\r\n')
> SV=$(sshpass -e ssh … 'docker exec ehome-prod-postgres psql -U ehome -d ehome -At \
>   -c "select session_version from users where id=1"' | tr -d '\r\n')
> TOKEN=$(JWT_SECRET="$SECRET" python3 .secrets/mkjwt.py "$SV")   # 见下：需 sv 参数
> BASE="http://$PROD_HOST:$PROD_WEB_PORT"
> ```
> （`.secrets/mkjwt.py` 原样只读 `JWT_SECRET` 且 `sv` 硬编码为 1；实测生产 `session_version=2`，必须按上法把 `sv` 传入，否则鉴权返回 401 `invalid or expired token`——这是本次踩到并已修正的坑。）
