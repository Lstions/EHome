# T1 原始需求重建 + 需求→实现对照

> **基线**: git HEAD `5f74fd3b`（2026-10-23 19:47 +0800）
> **产出者**: req-analyst（task-1）
> **证据等级**: **T**=本轮实测（命令+时刻）｜**C**=代码静态事实（文件:行）｜**D**=文档声明
> **冲突取向**: T > C > D。凡 D 与 C/T 冲突，以 C/T 为准并在矩阵标「文档不实」。
> **采集时刻**: 2026-10-23 20:47–21:42 +0800（除注明外）
> **v2 修订要点**: 收到产品负责人原始需求口述（见 §1），据此**降级**了此前基于 v3.3 文档的"合理性"判断。本文把
> 「**用户原始需求**」与「**方案自述需求**」分成两层处理，不再混用。

## 0. 口径声明（先声明分母，否则以下所有数字不可复核）

| 项 | 值 |
|---|---|
| 「用户原始需求」来源 | 产品负责人 2026-10-23 口述（经 Lead 转达）。**这是最强证据**，优先于任何文档推断 |
| 「方案自述需求」分母 | 1 份：`docs/archive/v4.0-重建前归档/设计/边缘设备数据生命周期管理方案.md`（650 行，v3.3 定稿） |
| 该文档入仓方式 | 单次提交 `81300a2b`（2026-08-03）一次性加入 609 行。其 v1→v3.3 的 5 轮评审**在仓内没有对应提交**，评审过程不可复核 |
| 「实现」分母 | `backend/internal/datalifecycle/` + `backend/internal/api/handler_{logical_device,edge_device,edge_device_lifecycle,edge_device_candidates}.go` + `frontend-shared/src/views/logical-device/` + `frontend-shared/src/api/logicalDevice.ts` |
| 提交面 | `git log --all` 共 1538 提交；`--grep='data-lifecycle'` 25 条；`--grep='逻辑设备'` 18 条；`-i --grep='logical'` 58 条（时刻 2026-10-23 21:00） |
| 生产库 | 只读 SELECT/EXPLAIN/curl GET。实测 `logical_devices=16`、`edge_devices=10`（7 条软删）、`unified_data` 约 3.25×10⁷ 行 |
| 本机库 | `ehome-postgres`：`logical_devices=2`、`edge_devices=2`、`unified_data=179532` |

> 本文不写「全仓没有 X」这类断言；凡否定性断言均给出搜索面与命令。

---

## 1. 【最强证据】用户原始需求：只有一条

**产品负责人口述（2026-10-23，经 Lead 转达）**：

> 「逻辑设备我不知道谁抽象出来的，这根本不正确！根本不需要什么逻辑设备概念！我当初只是需要合并旧数据而已，比如一个边缘设备在1号节点，我重新连接到2号节点，数据就会丢失，所以需要历史边缘设备合并或者直接复用！但是后面不知道谁过度设计，曲解了我的意图。」

拆解出**唯一一条需求**（证据等级 **T**，来源=产品负责人直接口述）：

> **R0**：同一个物理设备从节点 A 搬到节点 B 后，历史数据不能丢、要连续可查。
> 用户给出两个**可接受解法**：① 「历史边缘设备合并」 ② 「直接复用」。

**用户明确声明**：没有任何「逻辑身份层」的要求（"根本不需要什么逻辑设备概念"）。

### 1.1 与方案文档自述需求的对照（关键：两层不可混用）

| 层 | 内容 | 来源 |
|---|---|---|
| **用户原始需求** | **仅 R0 一条** | 产品负责人口述（T） |
| **方案自述需求（4 条）** | ①删除后默认保留 ②继承/合并是可选、用户显式触发 ③保存时间可配置、默认 365 天 ④跨节点也能继承/合并、多源用户可选 | `方案.md:15-19`，标注为「需求约束（用户已确认）」（**D**） |

**必须指出的事实**：方案文档把这 4 条写成「用户已确认」，但用户口述显示他只提了 R0。
⇒ 「**用户已确认**」这个标注本身缺乏独立证据支撑：仓内没有对应的需求确认记录、没有 review 评论、没有 issue。
⇒ **这 4 条是方案作者对 R0 的展开与再定义**，不是用户原话。本文以下一律按此分层判定。

---

## 2. 【核心】按原始需求 R0 重判：当前实现到底解不解决这个问题？

### 2.1 R0 场景的三种走法，逐个实测

场景：BMS（`jiabaida_bms`，`hardware_id=0x01`）从 node1/UART1 搬到 node2/UART0。

| 走法 | 用户实际操作 | 当前系统行为 | 判定 |
|---|---|---|---|
| **A. 删旧→建新→选继承** | ① 删除旧实例（选"保留历史数据"）② 新建向导走到步骤 0 ③ 从候选里选目标 ④ 完成创建 | 理论可行。但步骤 ②③ 是**新增的额外认知负担**：用户必须先理解"逻辑设备/候选/继承"三个概念 | ⚠ **能解，但成本远超需求** |
| **B. 原地改 node_id（即用户说的"直接复用"）** | 编辑该边缘设备，把"所属节点"改成 node2，保存 | **失败**。后端 `PUT /edge-devices/:id` 在 `channel_id` 为 nil 时用**旧 channel** 去匹配**新 node**：`Where("id = ? AND node_id = ?", targetChannelID, targetNodeID)`（`handler_edge_device.go:854`）⇒ 必然 "channel does not belong to node" | ❌ **被堵死** |
| **C. 只改 node_id 且同时传新 channel_id（API 直调）** | 直接调 `PUT` 带 `{node_id: 新, channel_id: 新通道}` | **成功，且 `logical_device_id` 原样保留**（update 的 `updates` map 不含 logical_device_id，`handler_edge_device.go:864-894`）。且创建新实例时 `PathCreateNew` 永不复用 key ⇒ 数据仍在旧 logical 上，但**若走 C 则根本不需要新建实例，历史天然连续** | ✅ **可行，但 UI 不暴露** |

### 2.2 决定性结论：用户要的「直接复用」在**能力层已存在**，在**UI 层被堵死**

- **能力层（T/C）**：`PUT /edge-devices/:id` 支持同时改 `node_id` + `channel_id`，而 update 的字段映射
  （`handler_edge_device.go:864-894`）**不包含 `logical_device_id`** ⇒ 身份与历史天然连续，**零新增实体**。
  **这恰好就是 R0 的"直接复用"解法，且它是原生的、不需要逻辑设备概念。**
- **UI 层（C）**：编辑分支提交体为
  `{name, node_id, interval_ms, ...(addressChanged?{hardware_id}:{})}`（`EdgeDeviceList.vue:1638-1645`）——
  **不含 `channel_id`**。因此用户在界面上改"所属节点"后保存，必然命中 §2.1-B 的必败校验。
- ⇒ **R0 的"直接复用"这条路：后端有能力，前端没接，于是用户实际不可达。**

### 2.3 「一逻辑设备一存活实例」额外把解法 A 变复杂

需求要的是"数据连续"，实现却要求"**先删旧实例**才能继承"：
- `checkLivingInstanceUniqueness` 对目标已有存活实例一律 409（`handler_edge_device_lifecycle.go:68-86`）；
- 跨节点继承被强制成「先删旧（节点 A）→ 再建新（节点 B）」的**特定顺序**（`方案.md:213`）。

而用户口中的「合并旧数据」本来是**一步**（"数据别丢"），被拆成了
「删除 → 记住要保留 → 新建 → 进入步骤0 → 选继承 → 完成」六步。
⇒ **实现没有让 R0 变简单，反而把一步需求变成了一条需要理解内部模型的流程。**

---

## 3. 【最简方案对照】满足同一条 R0 所需要的实体/状态/路径数量

### 3.1 本设计引入的全部新增物（分母可核）

| 类别 | 数量 | 明细（文件:行 / 提交） |
|---|---|---|
| 新增表 | **2** | `logical_devices`、`merge_jobs`（`database/gorm.go:123,127`） |
| 现有表加列 | **4** | `edge_devices.logical_device_id`、`unified_data.logical_device_id`、`device_data.logical_device_id`、`config_templates.edge_device_id`（`a880fd85`、`eae2f6da`） |
| `logical_devices` 新增字段 | **9** | id / identity_key / name / device_type / retention_days / merged_into / merge_status / purge_requested / created_at+updated_at（`models.go:LogicalDevice`） |
| 新增状态维度 | **3** | `merge_status`(NULL/pending/done)、`merged_into`(链)、`purge_requested`(bool) |
| 身份生成路径（三路径分流） | **3** | `PathStartup` / `PathCreateNew` / `PathDelete`（`identity.go:27-39,170-202`） |
| 新增 API 端点 | **5** | GET `/logical-devices`、PUT `/logical-devices/:id`、POST `/merge`、POST `/merge/preview`、GET `/merge-jobs/:id`（`handler_logical_device.go:40,105,144,161,181`）+ `GET /edge-devices/candidates` |
| 查询协议新增概念 | **4** | `ResolveDataQueryScope`、`followMergeChain`（≤16 跳）、`dataScopeCondition`(OR 兜底)、保形去重 `ROW_NUMBER()`（`query_scope.go`、`scope.go`） |
| 新增清理/迁移任务 | **3** | retention 每日任务、purge 每日任务、merge 搬迁 worker（+ backfill 脚本） |
| **核心代码量（15 文件）** | **5,656 行** | `wc -l` 实测，命令见 §7 |

### 3.2 最简方案对照（同样满足 R0）

| 方案 | 改动量 | 是否能解 R0 | 代价 |
|---|---|---|---|
| **S1. 暴露"原地改 node_id/channel_id"** | 改 1 处前端提交体 + 1 处后端校验（约 30 行） | ✅ 完全解决（数据行不动，连续可查） | **零新增表/状态/任务** |
| **S2. 数据表上记稳定物理身份** `(device_type, hardware_id)` | 加 1 列 + 1 个唯一索引 + 查询条件（约 100 行） | ✅ 解决，且换 hardware_id 时也可控 | 零新实体，无需 merge 链 |
| **S3. 只做 retention + 删除保留** | 已有（`delete_data=false` + purge 任务） | ⚠ 只解"数据在"，不解"连续可查" | 最小 |
| **本设计** | 2 表 + 4 列 + 5 端点 + 3 状态 + 3 路径 + 5,656 行 | ✅ 解决 | **且引入 §5 的运行时代价** |

⇒ **R0 用 S1 就能解，且 S1 的"直接复用"正是用户自己提出的第二个解法。**
本设计（方案 B）选择了 S2 的加重版（独立身份实体 + 合并链），但**没有先把 S1 堵死这件事说清楚**。

> 注：`方案.md:25` 否决策略 A "数据迁移"（UPDATE device_id）时给的理由是"无法表达 N:1 多源合并血缘"——
> 但 **R0 根本不需要 N:1 多源合并**。这个否决理由针对的是方案作者自己扩展出的需求，不是用户需求。
> 这是「方案自述需求」放大「用户原始需求」的**关键证据点**。

---

## 4. 需求→实现对照矩阵（按 R0 重排）

图例：**✅一致** ｜ **⚠超出需求** ｜ **❌未实现** ｜ **🔁需求被改口**

| # | 需求 | 实现产物 | 判定 | 证据 |
|---|---|---|---|---|
| **R0** | 设备换节点后历史数据不丢、连续可查 | **能力层已有**（`PUT` 可改 node_id+channel_id 且保留 logical_device_id）但 **UI 未暴露**（`EdgeDeviceList.vue:1638-1645` 无 channel_id） | **⚠ 解了但走错路** | **C** |
| R0-解法②「直接复用」 | 用户明说的可接受解法 | 后端支持；前端不暴露；且 `PathCreateNew` 永不复用 key（`identity.go:172-174`） | **❌ 被堵死** | **C** |
| R0-解法①「合并旧数据」 | 用户明说的可接受解法 | 实现为独立实体合并 `POST /logical-devices/merge`，要求 ≥2 个逻辑设备、且源**无存活实例**（`merge.go:113-119,150-180`） | ⚠ 可行但语义已漂移 | **C** |
| 方案自述-① 删除默认保留 | `delete_data` 默认 false（`handler_edge_device.go:1058`） | ✅ | **C** |
| 方案自述-② 继承/合并"可选" | 实际**每个**设备创建/删除都被强制建身份（`handler_edge_device.go:736-742,1084-1099`） | **🔁 被改为强制** | **C** |
| 方案自述-③ 保留期可配、默认 365 天 | 可配 ✅；默认已改 **90 天**（`61490159`，`config.go:253`）；原需求文本仍写 365 | **🔁 改口** | **C**+**T**（生产 16/16 行=90） |
| 方案自述-④ 跨节点合并 | 合并只校验同 device_type，不校验 node（`merge.go:113-119`）；候选权重 40/20 档跨节点（`handler_edge_device_candidates.go:47-49`） | ✅ | **C** |
| 方案 §4.2 到期前 30/7 天通知 | `retention_task.go:20-24,186-201` | ✅（对方案） | **C** |
| 方案 §3.4 P3「疑似重复」分组引导 | `grep -rn '疑似重复\|建议考虑继承\|高度相似' frontend-shared/src` → **0 命中** | **❌未实现**（方案自称 P3 迭代、不阻塞） | **C** |
| 方案 §3.4 merge 失败重试 ≤3 | `migrate.go:24-25` `maxMergeRetries = 3` | ✅ | **C** |
| （方案未要求）逻辑设备管理页 | `LogicalDeviceList.vue` 1070 行 | **⚠超出** | **C** |
| （方案未要求）merged_into 追溯链 | `merge_chain.go` | **⚠超出需求** | **C** |
| （方案未要求）保形去重 | `query_scope.go:ApplyShapeDedup` | **⚠超出需求** | **C** |
| （方案未要求）identity_key 三路径复用 | `identity.go` | **⚠超出需求** | **C** |

### 4.1 「合并/追溯链/身份复用」到底是需求的投射，还是长出来的？

按**用户原始需求**（不是方案自述）判定：

| 概念 | 在 R0 里？ | 在方案自述 4 条里？ | 判定 |
|---|---|---|---|
| 数据不丢/连续可查 | ✅ | ✅ | **需求直接投射** |
| 合并（多源） | ❌（用户说"合并**旧数据**"，指新旧两个实例，不是多源） | ✅ | **方案放大**：从"新旧两个"扩到"N 个多源" |
| merged_into 追溯链 | ❌ | ❌（方案 `:25-26` 以"误合并不可恢复"论证带出） | **实现长出** |
| 保形去重 MAX(id) | ❌ | ❌（为修"合并后图表重复点"副作用，`:358-361`） | **实现长出** |
| identity_key 三路径复用 | ❌ | ❌（为解多副本并发竞态，`:140-146`） | **实现长出** |
| purge 与重建竞态守卫 | ❌ | ❌（v3.3-N1，为修自身引入的 bug） | **实现长出** |
| 「逻辑设备」这个产品名与页面 | ❌ | ❌ | **实现长出**（用户原话：根本不需要） |

⇒ **明确回答任务核心问题**：
- 「合并」在用户需求里有**影子**（用户说"合并旧数据"），但被**放大**成多源合并；
- 「**追溯链 / 保形去重 / 身份复用 / purge 竞态守卫 / 逻辑设备页面**」**全部是实现过程中为解决自身副作用长出来的**，
  用户需求与方案自述需求里都没有。

---

## 5. 运行时代价（Lead 实测 + 本轮复现）

### 5.1 生产 `GET /logical-devices` 默认参数需 80–131 秒，且**只富化 16 条**

口径：生产容器内 curl，自签 JWT（`EHOME_JWT_SECRET` + `users.session_version`），只 GET。时刻 2026-10-23 21:08–21:40。

| page_size | 实测耗时（s） | 返回条目 |
|---|---|---|
| 1 | 0.009 / 0.030 / 0.041 | [1] |
| 2 | 0.016 / 0.041 | [1,2] |
| 4 | 0.022 / 0.034 | [1..4] |
| 8 | 0.042 / 0.055 | [1..8] |
| 10 | 0.056 | [1..10] |
| **12** | **37.28** | [1..12] ← 首次纳入 ld=12 |
| 14 | 72.52 | [1..14] |
| **16** | **80.81 / 87.18** | 全 16 条 |
| **20（默认）** | **85.22 / 90.28 / 109.73 / 126.65** | 全 16 条 |
| 200 | 93.13 / 93.77 / 104.18 | 全 16 条 |

**这是阈值型突变，不是线性成本**：1→10 全程 <0.06s；10→12 跳到 37s。
突变点精确对应「开始富化某个**已停止上报**的逻辑设备」。

### 5.2 根因：`Scope.Cond()` 的 OR 分支使 planner 放弃索引下推

`scope.go:58` 生成的条件是
`(logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?))`。

生产 `EXPLAIN (ANALYZE, BUFFERS)` 实测（2026-10-23 21:41）：

| LD | 写法 | 计划 | 实际耗时 |
|---|---|---|---|
| **ld=12**（已软删，最后数据 2026-10-06，24.6 万行） | **OR**（现设计） | `Index Scan Backward using unified_data_202610_timestamp_idx` + `Filter`，**Rows Removed by Filter: 29,556,938**，Buffers read=424,363 | **35,604 ms** |
| ld=12 | **仅 logical 分支** | `Index Only Scan using unified_data_202610_logical_device_id_timestamp_idx`，Buffers read=5 | **0.867 ms** |
| **ld=14**（存活，最后数据 2026-10-23，29.0M 行） | OR | 同样 Index Scan Backward...，但 `Rows Removed by Filter` ≈ 0 | **0.062 ms** |

**机制**：OR 让 planner 无法对两侧同时走索引，退化为「按 timestamp 倒序扫描 + 逐行 Filter」。
成本 ∝ **比该逻辑设备最新数据更新的行数**——所以
**「停更越久的历史设备越慢」**，而那恰恰是 R0 要保留的「旧设备历史数据」。

### 5.3 UNION ALL 等价重写 4 个数量级差异（本轮复现）

| 写法 | ld=2（292 万行） | ld=12（24.6 万行） |
|---|---|---|
| 现设计 OR 条件 | **27,905 / 32,320 ms** | **36,504 / 32,929 ms** |
| **UNION ALL 等价重写** | **998 / 336 ms** | **163 / 113 ms** |
| 仅 logical 分支（生产 backfill 已完成 ⇒ 完备） | 103 / 102 ms | — |

**生产 backfill 已完成实测**（2026-10-23 21:44）：
`unified_data` 的 `logical_device_id IS NULL` 计数 = **0 / 32,486,147**；
存活实例 `logical_device_id IS NULL` = **0 / 3**。
⇒ 那个 OR 兜底分支在生产**一行也命中不到**，是纯负担。
而方案 `:356` 自述它"成本极低"，**实测 32 秒**——这是一条被文档掩盖的性能债。

---

## 6. 需求层面本身的疑点

### 疑点 1（**最重要**）：「用户已确认」的 4 条需求，用户没确认过

`方案.md:15` 写「需求约束（用户已确认）」，但产品负责人口述显示他只提了 R0。
仓内可复核面：无需求确认提交、无 review 记录、无 issue。
⇒ 这 4 条是**方案作者的自述**。基于这 4 条做"合理性"论证会**循环自证**。

### 疑点 2：方案否决「数据迁移」的理由针对的是自造需求

`方案.md:25` 否决方案 A，理由"device_id 单值无法表达 **N:1 多源合并血缘**"。
但 R0 只需要"新旧两个实例的数据连续"，**不需要 N:1**。
⇒ 用扩展需求的缺陷去否决最简方案，是**过度设计的起点**。

### 疑点 3：「默认保留」与「到期硬删」在同一份文档里语义互斥

- `:16` 「删除设备后默认保留数据」（语义=留）；
- `:298` 「到期硬删后不可恢复」，默认 365→90 天。
⇒ 实际语义是「默认延后 90 天删除」。用户读到"保留"与读到"90 天后不可恢复"是两种预期，方案未裁决这对措辞。

### 疑点 4：需求 ③ 的默认值被单方面改动，原需求文本未回收

- 原文「默认 1 年（365 天）」；
- `61490159`（2026-09-12）改为 90 天，理由「未发布产品不长期囤数据」——**不是用户需求变更**；
- 现行设计文档写 90 天（`边缘设备数据生命周期.md:14`），**原需求文本仍是 365**。两份文档对同一约束给出两个数，无裁决记录。

### 疑点 5：「可选」被实现成「强制」，从未裁决

方案自述 ② 说继承/合并是"可选行为"；实现里**每个**设备创建/删除都会被建/复用 logical_device。
用户即使从不使用继承，也必然被卷入该实体。这是「可选」与「强制身份层」的语义错位。

### 疑点 6：需求 1 的痛点原文是「缺机制」，交付的是「新实体+新页面」

`:13` 的理由是「成为孤儿，**无管理机制**」。若严格按此交付，最小实现 = 删除不删数据 + 保留期清理任务 + 一个改保留期的入口。
实际交付 2 表 + 4 列 + 5 端点 + 5,656 行。
验收看的是「合并/追溯/retention 都跑通了」（`边缘设备数据生命周期.md:100-107` 六项全勾），**不是「R0 是否解决、成本多低」**。

---

## 7. 效果层实测汇总（可复跑）

### T-1 生产数据保留在 UI 侧不可达（**R0 效果层反证**）

口径：生产带 JWT 的 GET，只读。时刻 2026-10-23 21:33。

| 探针 | 结果 |
|---|---|
| `GET /unified-data/historical?device_pk=12`（12=已软删实例，持 205,073 行） | **200**，返回真实数据点 |
| `GET /edge-devices`（DataPanel 设备下拉的数据源） | 返回 id `[14,15,16]`，**不含 12** |
| `GET /edge-devices/12`（详情页） | **404** |
| `GET /edge-devices/12/data` | **404** |

⇒ **数据在库里、API 也能取，但用户在设备列表/数据面板里永远选不到它。**
「保留」停在了数据库层，没到"用户可查见"层。而唯一能看见它的 `LogicalDeviceList` 页本身要 85–131 秒才加载（§5.1）。

### T-2 生产逻辑设备集合（否定性断言给搜索面）

命令：`SELECT count(*) FROM logical_devices` → **16**；`edge_devices` → 10（其中 7 条软删）。
6 条逻辑设备**零实例却持有数据**（`ld=2` 持 2,923,306 行）——即"孤儿数据"在逻辑设备维度**依然存在**，
只是换了个地方躺（这正是 R0 要解决的问题，实测**未解决**）。

### T-3 「1003 条逻辑设备」与生产 16 条**不是同一集合**（防"数字相同≠集合相同"陷阱）

- `docs/分析/UIUX审计报告-D2-资源列表与详情页.md:99-107` 的 1003 条出自**本机开发库**；
- 生产实测仅 16 条。

⇒ 引用 1003 条论证生产性能问题会量错对象。**本文所有性能结论均用生产 16 条实测**。

---

## 8. 给 Lead 的裁决点

1. **R0 已经在能力层被满足，只是 UI 堵死了「直接复用」（改 1 处提交体 + 1 处校验即可解锁）。**
   是先解这个 30 行的堵点，还是继续维护 5,656 行的逻辑设备体系？
2. **`Scope.Cond()` 的 OR 是性能根因，生产 backfill 已完成（NULL 行 = 0/32.5M），兜底分支零命中。**
   删除该分支或改 UNION ALL，实测 32s → 0.1–1s。这是"设计问题而非数据量问题"的**直接证据**。
3. **方案文档标注「用户已确认」的 4 条需求，与产品负责人口述不符。**
   建议：以 R0 为唯一验收基准重新评估，而不是以方案自述的 4 条。

---

## 附录：关键命令

```bash
# 集合规模
docker exec ehome-postgres psql -U ehome -d ehome -c "SELECT count(*) FROM logical_devices;"
# 生产（只读）
sshpass -e ssh ... 'docker exec ehome-prod-postgres psql -U ehome -d ehome -Atc "SELECT count(*) FROM logical_devices;"'
# backfill 是否完成
sshpass -e ssh ... 'docker exec ehome-prod-postgres psql -U ehome -d ehome -c "SELECT count(*) FILTER (WHERE logical_device_id IS NULL), count(*) FROM unified_data;"'
# 计划对比
docker exec ehome-prod-postgres psql -U ehome -d ehome -c "EXPLAIN (ANALYZE, BUFFERS) SELECT timestamp FROM unified_data WHERE (logical_device_id = 12 OR (logical_device_id IS NULL AND device_id = 12)) ORDER BY timestamp DESC LIMIT 1;"
# OR vs UNION ALL
SELECT timestamp FROM (SELECT timestamp FROM unified_data WHERE logical_device_id = 12
  UNION ALL SELECT timestamp FROM unified_data WHERE logical_device_id IS NULL AND device_id = 12) u
  ORDER BY timestamp DESC LIMIT 1;
# 代码量分母
wc -l backend/internal/datalifecycle/{identity,merge,merge_chain,preview,purge,retention,retention_task,scope,query_scope,migrate,backfill,indexes}.go
# P3 未实现的搜索面
grep -rn '疑似重复\|建议考虑继承\|高度相似' frontend-shared/src
```
