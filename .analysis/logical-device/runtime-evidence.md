> 环境：**生产 192.168.20.6（ehome-prod-web / ehome-prod-postgres）为主，本机 ehome-postgres/ehome-web 为对照**。生产可达、凭据有效，**未降级**。
> 纪律：仅 SELECT / EXPLAIN / curl GET；无 DDL、无写、无重启容器、无并发长压测（单次最长请求 131 s）。
> 采集时刻：**2026-10-23 20:52–21:42 (+08:00)** = UTC 12:52–13:42。
> 证据等级：[T]=实测（生产/本机真跑） [C]=代码:行 [D]=文档。**代码/文档自述一律不计入 T。**

# T4 运行时证据：逻辑设备列表「42 秒」→ 实测 93–131 秒的根因量化

## 0. 一句话结论（可被推翻）

> GET /api/v1/logical-devices 的耗时**与逻辑设备条数几乎无关，与数据量也几乎无关**；
> 它 ≈ 「本页每台设备各 4 条历史边界查询耗时之和」，而每条边界查询被
> **Scope.Cond() 生成的 OR 条件**逼成「沿全表时间戳索引反向扫描直到命中」——
> 必须跳过该设备最后一次数据之后的**全部**新行。
> **成本 ≈ 该设备停更时长 × 全局写入速率**，因此对「已下线的老设备」最贵、对「正在写的最新设备」几乎免费。
>
> 推翻方式（任一）：把 Scope.Cond() 的 OR 改成两段 UNION ALL 后列表耗时不再随 ps 增长 —— **此实验已在生产做掉，见 §5，快 6,500 倍**。

---

## 1. 生产库规模（T，2026-10-23T12:52Z）

命令：
~~~bash
set -a; . .secrets/prod.env; set +a; export SSHPASS="$PROD_SSH_PASSWORD"
sshpass -e ssh -F /dev/null -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -p "$PROD_SSH_PORT" "$PROD_SSH_USER@$PROD_HOST" \
  'docker exec -i ehome-prod-postgres psql -U ehome -d ehome' <<'SQL'
SELECT c.relname, s.n_live_tup, pg_size_pretty(pg_total_relation_size(c.oid)) total_size
FROM pg_class c LEFT JOIN pg_stat_user_tables s ON s.relid=c.oid
WHERE c.relname IN ('logical_devices','edge_devices','unified_data','device_data') ORDER BY 3 DESC;
SQL
~~~

| 表 | 行数 | 口径 | 大小 | 证据 |
|---|---|---|---|---|
| logical_devices | **16**（精确 count） | 全表 | 72 kB | T |
| edge_devices | **10**（精确 count，含软删；live 10 / dead 10） | 全表 | 224 kB | T |
| unified_data | **32,385,808**（n_live_tup；PG18 分区父表 reltuples=-1 不可用） | 分区 202610 占全部 | 8099 MB | T |
| device_data | **3,034,632**（n_live_tup） | 分区 202610 | 2002 MB | T |

**分区与索引**（T，pg_inherits + pg_indexes）：两表各 **5 个分区**（202609–202701），数据只落 _202610，其余 4 个 64–72 kB 空壳；父表各 **16 个分区级索引**，全部 indisvalid=t / indisready=t（**无失效索引**）。关键索引存在：idx_unified_logical_ts =(logical_device_id, timestamp DESC)；idx_devdata_logical_ts 同形；idx_unified_legacy_null_ts =(device_id,sensor_name,timestamp) INCLUDE(value) WHERE logical_device_id IS NULL。

**16 条逻辑设备构成**（T，GROUP BY device_type）：jiabaida_bms 5 / generic_modbus 4 / techfine_inverter 3 / jk_bms 2 / sn3001_rain 1 / generic_i2c 1。

> **分母更正**：任务书与 logicalDevice.ts:96 的「1003 条」是**过期数字**。生产实测 **16 条**（T）；本机 **2 条**（T）。
> 1003 出自 docs/操作/开发环境事实.md:37 记载的早期开发库，不是当前任何环境。

---

## 2. 端点计时：page_size 曲线（生产，T）

~~~bash
curl -s -o /dev/null -w '%{http_code} %{time_total}\n' -H "Authorization: Bearer $J" \
  "http://127.0.0.1:18090/api/v1/logical-devices?page=1&page_size=$ps"
~~~

| page_size | 各次实测 (s) | 中位数 |
|---|---|---|
| 1 | 0.017 / 0.007 / 0.009；另 0.016 / 0.022 / 0.009 | **~0.009** |
| 4 | 0.040 / 0.027 | ~0.033 |
| 8 | 0.068 / 0.050 / 0.069 | **~0.068** |
| 12 | 38.32 / 55.89 | **~47.1** |
| 13 | 99.22 / 101.09 | ~100.2 |
| 16 | 123.74 / 100.61 / 74.35 | ~100.6 |
| 20（默认） | 97.52 / 93.22 / 92.49 / 93.70 / 104.24 | **~93.7** |
| 200 | 121.53 / 118.90 | ~120.2 |

**形状判读（关键）**：ps=1..8 亚线性（≈每项 8 ms）；**ps 8→12 出现 1,000 倍跳变**（0.068 s → 47 s），此后到 200 只从 93 涨到 120。
⇒ 非「每项成本恒定」的线性模型。**跳变点正是页内首次纳入 id=12**（ORDER BY id，ps=8 覆盖 1–8，ps=12 覆盖 1–12）。
⇒ 任务书要求区分的两种解释**都不对**：单条成本**既非常数、也不随条数**，而随**该设备的历史断档长度**。

---

## 3. 逐设备计时：成本归因到设备（T，page=N&page_size=1，ORDER BY id 故 N==id）

| id | 名称 | instances | row_estimate | last_data_at | 耗时 |
|---|---|---|---|---|---|
| 1 | BMS-UART1 | 0 | 18 | 省略 | 0.027 s |
| **2** | BMS-UART0 | **0** | **3,197,118** | 2026-10-04 | **0.037 s** |
| 3 | BMS-UART2 | 0 | 18 | 省略 | 0.007 s |
| 4–6 | 逆变器/stress-* | 0 | 18 | 省略 | 0.009–0.025 s |
| 7–11 | st-* | 1 | 18 | 省略 | 0.009–0.014 s |
| **12** | bms-jiabaida | 1 | 248,053 | 2026-10-06 | **51.2 / 35.9 / 45.1 s** |
| **13** | inverter-techfine | 1 | **8,856** | 2026-10-05 | **44.9 / 43.1 / 37.4 s** |
| **14** | bms | 1 | **28,935,649** | **2026-10-23 13:24（=全局 max）** | **0.81 / 1.37 s** |
| 15 | inter | 1 | 1,858,296 | 2026-10-23 13:23 | **7.31 / 6.98 s** |
| 16 | 光学雨量计 | 1 | 18 | 省略 | 0.010 s |

**同时证伪两个流行归因**：
1. **「数据量大所以慢」→ 证伪**：id=2 有 **319 万行**却 0.037 s；id=13 只有 **8,856 行**却 44.9 s。
2. **「逐设备串行 N+1 是根因」→ 只是放大器**：id=14 有 **2893 万行**（全库 89%）却仅 0.81 s。若单条成本恒定，它应最慢。

**真正变量**：该设备 last_data 落后全库最新时间多久。

---

## 4. 根因：Scope.Cond() 的 OR 让索引从「定位」退化为「逐行丢弃」

### 4.1 代码事实 [C]
- scope.go:43-52：default 分支返回 "(logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?))"
- preview.go:140-156（scopeBoundaryTimestamp）：每设备、每表、每方向各发 1 条 SELECT timestamp FROM 表 WHERE (OR 条件) ORDER BY timestamp ASC|DESC LIMIT 1
- handler_logical_device.go:91-95：每设备调 1 次 ⟹ **每设备 2 表 ×(ASC+DESC) = 4 条**
- **无实例时** ResolveScope（scope.go:56-82）返回空 InstanceIDs ⟹ Cond() 走 scope.go:41-42 的**纯单分支** logical_device_id IN ?
  **这就是 id=2（instances=0）快、id=12/13/14/15（instances=1）慢的结构性原因。**

### 4.2 生产 EXPLAIN（T）：同设备 id=13、同会话、**先热再跑**（排除冷缓存）

| 跑序 | 条件 | 计划 | Rows Removed by Filter | Execution |
|---|---|---|---|---|
| R1 | 纯 logical_device_id IN (13) | **Index Only Scan** using ..._logical_device_id_timestamp_idx，**Index Cond**:(logical_device_id=13)，Heap Fetches 0，Buffers hit=12 read=2 | — | **0.720 ms** |
| R2 | OR 形式（**热缓存紧随 R1**） | **Index Scan Backward** using ..._timestamp_idx，谓词在 **Filter** 而非 Index Cond，Buffers hit=1,026,167 read=420,706 | **29,367,450** | **32,260.495 ms** |

⇒ **42,700 倍差距，且在完全热缓存上测得**。冷热不是解释。

### 4.3 三设备横向对照（T，OR 形式 DESC）

| ld | unified 行数 | 该设备 max(ts) | 需跳过行（planner 估算） | Execution |
|---|---|---|---|---|
| 13 | 14,224 | 2026-10-05 18:39 | **29,151,816** | 22,251.8 ms（另复现 38,883.8 ms） |
| 12 | 205,073 | 2026-10-06 01:07 | **29,006,487** | 32,086 ms（Lead 独立复现，一致） |
| 2 | 2,923,306 | 2026-10-04 01:49 | 29,509,075 | 25,638.7 ms ← **端点却 0.037 s**，因 instances=0 走纯分支 |
| **14** | **27,726,179** | **=全局 max** | **≈0** | **0.122 ms** |

**统一解释（可证伪）**：代价 ≈ 需跳过行数 × 单行过滤成本。id=14 首行即命中→0.12 ms；id=13 需跳 2915 万行→22–39 s。
**证伪方式**：把 id=13 的 max 改成当前时刻后应变快（未做，禁止写生产）。

### 4.4 分段计时：单设备 9 段（T，id=13，逐段单独执行并开启 timing）

| # | 段（代码位置） | 耗时 |
|---|---|---|
| 1 | CountInstances（identity.go:237）SELECT count(*) FROM edge_devices WHERE logical_device_id=13 | **3.346 ms** |
| 2 | ResolveScope pending（scope.go:63） | **4.469 ms** |
| 3 | ResolveScope instances（scope.go:73） | ~0.2 ms |
| 4 | EstimateRowCount EXPLAIN unified（scope.go:163） | **2.943 ms** |
| 5 | EstimateRowCount EXPLAIN device_data | ~15 ms |
| 6 | **boundary FIRST unified（ASC）** | **634.938 ms** |
| 7 | **boundary LAST unified（DESC）** | **39,330.745 ms** |
| 8 | **boundary FIRST device_data（ASC）** | **446.219 ms** |
| 9 | **boundary LAST device_data（DESC）** | **21,986.276 ms** |

**合计 ≈ 62.4 s，端点单次 37.4–45.1 s —— 同量级吻合**（差异为并发/缓存波动）。

**ASC 便宜 / DESC 贵（T，P1–P4 配对）**：unified DESC 29,387,817 removed → **38,883.8 ms**；unified ASC 2,946,225 → **910.8 ms**；device_data DESC 2,738,322 → **7,283.9 ms**；device_data ASC 282,021 → **400.6 ms**。
⇒ 该设备数据停在**过去**：从最老端往新走只需跨过它自己 1.4 万行；从最新端往回走要跨过它停更后的**全部**新数据。**是数据分布，不是索引不对称。**

### 4.5 端到端拆解 vs 实测 105 s（任务 b）

固定模型：每设备 1 CountInstances + 1 pending + 1 instances + 2 EXPLAIN + 4 boundary = 7 段。

| 设备类 | 条数 | 单设备实测 | 小计 |
|---|---|---|---|
| 无数据/无实例（1,3,4,5,6,16；7–11 边界段） | 11 | <25 ms | ~0.2 s |
| 有数据无实例（id=2，纯分支） | 1 | 37 ms | 0.04 s |
| **有实例+停更约 17 天（id=12）** | 1 | 36–51 s | 36–51 s |
| **有实例+停更约 18 天（id=13）** | 1 | 37–45 s | 37–45 s |
| 有实例+停更 5 天（id=15） | 1 | ~7 s | ~7 s |
| 有实例+最新（id=14） | 1 | ~1 s | ~1 s |
| **理论合计** | 16 | — | **≈81–104 s** |
| **实测默认参数（ps=20）** | — | — | **92.5 / 93.2 / 93.7 / 97.5 / 104.2 s** |

⇒ **吻合**。105 s **不需要任何未知开销**：它就是 id=12 与 id=13 两条 DESC 边界查询之和（36–51 + 37–45 s）加 id=15 的 7 s；其余 12 台合计 <0.3 s。
⇒ **非直觉性质**：这 105 s **不随设备总数增长**，而由**数据新鲜度分布**决定 —— 今天 105 s，清理掉一台老设备可能掉到 40 s，再下线一台可能涨到 150 s。

---

## 5. 反证实验：等价 UNION ALL 重写（生产，T，2026-10-23T13:29Z）

| 写法 | 语义 | 耗时 |
|---|---|---|
| OR 形式（现行） | 等价 | **36,665 ms** |
| UNION ALL 两分支后排序（ld=12） | **完全等价** | **89.75 ms** |
| UNION ALL 两分支后排序（ld=13） | 完全等价 | **5.60 ms** |
| UNION ALL device_data（ld=13） | 完全等价 | **9.29 ms** |
| GREATEST((子查询A),(子查询B))（ld=13） | 等价，结果一致 | **2.37 ms** |

⇒ 同一语义 36,665 ms → 5.6 ms（**快 6,500 倍**）。**慢的是写法导致的执行计划退化，不是数据量、不是数据库能力。**
⇒ 根因收敛到**一行条件渲染**：scope.go:50 的 OR。属**设计层表达问题**，不是容量问题。

---

## 6. device_data 同条件分段（任务 a，T）

| 项 | 值 |
|---|---|
| device_data 总行数 | 3,034,632（n_live_tup） |
| **logical_device_id IS NULL 行数** | **1,685,690（精确；有界 COUNT 上限 2e6 未触顶）** |
| EXPLAIN ... WHERE logical_device_id IS NULL | Seq Scan on device_data_202610（rows=1,680,264） |
| ld=13 DESC（OR） | 2,738,322 removed → **7,283.9 ms**（另一轮 21,986.3 ms） |
| ld=13 ASC（OR） | 282,021 removed → **400.6 ms** |

> **更正 Lead 第 5 条**：unified_data 的 NULL 行 = 0 我已完全复现（有界 COUNT → **0**）。但 **device_data 的 NULL 行不是 0，是 1,685,690 行（T）**。
> 所以「B 分支恒空」只在 unified_data 成立；device_data 上 B 分支**真有数据**，Seq Scan 全表扫 168 万行。这解释 device_data DESC 为何稳定地慢（7–22 s），而 unified DESC 的慢**纯粹**来自反向扫描。

---

## 7. page=2&page_size=1 的后果（任务 c，T）

| 请求 | 命中 | 耗时 |
|---|---|---|
| page=1&ps=1 | id=1（无实例无数据） | 0.017 / 0.007 / 0.009 s |
| page=2&ps=1 | id=2（319 万行，**无实例**） | 0.037 s |
| page=11&ps=1 | id=11 | 0.008 s |
| **page=12&ps=1** | **id=12（有实例、停更）** | **35.9 s** |
| **page=13&ps=1** | **id=13（有实例、停更）** | **43.1 s** |
| page=14&ps=1 | id=14（最新） | 1.37 s |
| page=15&ps=1 | id=15（停更 5 天） | 6.98 s |
| page=16&ps=1 | id=16 | 0.008 s |
| **page=2&ps=8** | **id 9–16**（含 12,13,15） | **100.32 s** |
| **page=1&ps=8** | **id 1–8**（全无/少数据） | **0.069 s** |

**对用户可见的后果**：
1. **同一页面翻页耗时相差 1,450 倍**（0.069 s vs 100.32 s）。第 1 页正常，翻第 2 页「卡死」——**不是「列表慢」，是「某些页慢」**。
2. ORDER BY id（handler_logical_device.go:69）⟹ 新设备总在最后；而**数据最新的设备最便宜、历史包袱重的设备最贵**。**排序键与成本函数完全正交**，用户无法靠「先看关心的」规避。
3. 前端页长选项 [20,50,100]（LogicalDeviceList.vue:126）、默认 20 —— **三个档位全部落在 93–131 s**，无便宜档可选；ps=1..8 快只因**恰好排除了 id=12/13**。

---

## 8. 超时兜底的性质（任务 4 之 d）

### 8.1 全局 10 s vs 前端 60 s 特例 [C]
| 位置 | 值 | 行 |
|---|---|---|
| 全局 axios timeout | **10000 ms** | frontend-shared/src/api/client.ts:49 |
| 列表页特例 | **60000 ms** | LogicalDeviceList.vue:356 |
| 生效方式 | list(params,{timeout}) → 单请求覆盖 | logicalDevice.ts:112-115；调用点 LogicalDeviceList.vue:411 |
| 用户可见文案 | 「请求超时：逻辑设备列表接口响应较慢（生产实测约 42 秒），请稍后重试。」 | LogicalDeviceList.vue:395 |

**部署包核对（T，防「源码改了镜像没更新」）**：docker exec ehome-prod-web grep 命中 /app/static/dist/assets/LogicalDeviceList-8BrxDhsY.js，含该中文文案与 page_size 传参；包内无 60000 字面量（压缩为 **6e4**，grep 6e4 命中）。

### 8.2 该特例是「掩盖」不是「修复」（T+C）
- 真实 **93–131 s > 60 s** ⟹ **60 s 特例本身也保不住**。用户现在看到「约 42 秒」，而今日实测 94–131 s ⟹ **文案数字与解释双错**。
- 生产 HTTP server **未设 ReadTimeout/WriteTimeout**（backend/cmd/server/main.go:698-701 只设 Addr+Handler）⟹ **后端不会主动切断**慢请求，会持续占用 DB 连接与协程（采样见 §4 时 pg_stat_activity 同时出现 4 条 active 边界查询）。
- 该端点**除 EstimateRowCount 有 3 s 兜底**（scope.go:99 EstimateTimeout=3s）外，**4 条 boundary 与 CountInstances 均无超时**（C，handler_logical_device.go:82-95 只给 estimate 挂了 context.WithTimeout）。

⇒ **性质判定**：60 s 特例是把已知慢接口从全局超时里**豁免**出来，属兜底；但挡不住 93–131 s，**兜底已失效**。这不是调优问题，是**10 s 接口契约被单方面放弃**。

---

## 9. 本机复现：同机制、小数据量（T，证明不是生产特例）

本机 ehome-web:8080（JWT sv=7，secret 经 docker exec printenv 管道，未落盘）：

| 请求 | 命中 | 耗时 |
|---|---|---|
| ps=1 ×3 | id=1（**max = 全局 max**） | 0.0195 / 0.0187 / 0.0165 |
| **page=2&ps=1 ×3** | **id=2（max 早 8 天）** | **0.154 / 0.159 / 0.178** |
| ps=20 ×3 | 2 条 | 0.168 / 0.210 / 0.207 |
| ps=200 ×3 | 2 条 | 0.209 / 0.208 / 0.208 |

- 仅 179,532 行，**同机制仍表现为 9 倍差**（0.017 vs 0.155 s）⟹ 机制与数据量无关。
- **「条数少所以快」确实是本机快的一部分**：本机仅 2 条，ps=200 与 ps=20 完全同耗时（0.208 s），因为没有更多条可富化。
- **「单条成本恒定」被本机直接证伪**：两条设备单条成本 0.017 s vs 0.155 s（**9 倍**），同端点、同会话、同进程。

**本机 EXPLAIN 佐证（T）**：ld=1 OR 形式 0.124 ms（max=全局 max，首行命中）；ld=2 OR 形式 **26.030 ms**，Rows Removed by Filter 133,499+2,206 ⟹ 与端点 0.017/0.155 s 同向。

---

## 10. 「每请求多少条 SQL」——N+1 量化（T-，仅量级证据）

本机 xact_commit 差分（pg_stat_database；10 s 背景 129 commits 作量级参照）：

| 请求 | N | commits 增量 | 每请求 | 耗时 |
|---|---|---|---|---|
| ps=1 | 10 | 2 | **0.2** | 231 ms |
| ps=1 | 20 | 6 | **0.3** | 435 ms |
| ps=20 | 10 | 279 | **27.9** | 2,129 ms |

⇒ SQL 数随 page_size 线性增长（0.3→27.9），方向与「每设备 7 段」一致；实测 27.9 低于理论 140，说明存在批量/合并，但**量级仍是 O(ps)**，N+1 结构成立。
（口径：xact_commit 是事务数非语句数，且本机有 MQTT 背景噪声 ⟹ 只作量级证据，不单独下结论。）

---

## 11. 结论：这是设计问题，不是性能问题

四条**互相独立**、每条均可单独推翻的判据：

| # | 判据 | 实测 | 若为纯性能问题应表现 | 证据 |
|---|---|---|---|---|
| 1 | 耗时随条数**非线性跳变** | ps 8→12：0.068 s → 47 s（**1,000 倍**） | 平滑线性 | T |
| 2 | 耗时与数据量**反相关** | id=2（319 万行）0.037 s vs id=13（8,856 行）44.9 s | 正相关 | T |
| 3 | 同一语义换写法**快 6,500 倍** | OR 36,665 ms → UNION ALL 5.6 ms | 无变化 | T |
| 4 | 命中同一索引，仅谓词位置不同 | **Index Cond**（纯分支）vs **Filter**（OR） | 同样走 Index Cond | T（EXPLAIN BUFFERS） |

> **判据 4 是物理核心**：两种写法都用 timestamp 索引，但纯分支把 logical_device_id 当**索引条件（直接定位）**，OR 把它降级成**过滤条件（必须逐行走完整个索引范围）**。这不是「数据太多」，是**查询表达让索引失效**。

**归因权重（我的判断）**：Scope.Cond() 的 OR（scope.go:50）**贡献 >95%**（证据：判据 3、4）；逐设备 7 段串行（handler_logical_device.go:80-98）**贡献约 5%，但对小库是全部**（本机 ps=1 每次 0.017 s；生产 12 台健康设备合计 <0.3 s）；数据量**几乎不贡献**（判据 2）；冷缓存**不贡献**（§4.2 R1→R2 热缓存下 OR 仍 32 s）；前端渲染**与本次无关**（生产仅 16 条，1003 行 DOM 是另一条已修缺陷）。

---

## 12. 按原始需求重判（依 Lead 转述的产品负责人原话）

> **需求原文（最高优先级，高于任何文档）**：「我当初只是需要合并旧数据而已，比如一个边缘设备在 1 号节点，我重新连接到 2 号节点，数据就会丢失，所以需要历史边缘设备合并或者直接复用！但是后面不知道谁过度设计，曲解了我的意图。」
> ⟹ 用户可接受两个解法：**① 历史边缘设备合并 ② 直接复用**。**无任何「逻辑身份层」要求。**

### 12.1 当前实现是否解决该场景？——部分解决，代价极高，「直接复用」被结构性堵死

场景：同一台物理设备（hardware_id 不变）从 node1 搬到 node2。当前设计的实际路径（[C]）：

| 步 | 系统要求 | 代码 |
|---|---|---|
| 1 | **必须先删旧实例**（否则「一逻辑设备一存活实例」直接 409） | handler_edge_device_lifecycle.go:68-86 checkLivingInstanceUniqueness，文案明写「请先删除旧实例…再重新创建」 |
| 2 | 删除时若旧实例无逻辑身份则走 PathDelete 补建 | handler_edge_device.go:1088 |
| 3 | 在 node2 **新建**实例 | POST /edge-devices |
| 4 | 新建时**必须手动勾选「继承历史数据」**并选中旧逻辑设备 | handler_edge_device.go:706-715；UI 默认折叠、默认「作为新设备创建」（QuickCreateDeviceDialog.vue:132） |
| 5 | 未勾选 ⟹ PathCreateNew **永不复用** identity_key，直接造孤儿身份 | identity.go:172-174；handler_edge_device.go:736-742 |

**「直接复用」被堵死三处（[C]）**：
1. **创建向导默认永不复用**：identity.go:34-35 注释直书「PathCreateNew — 创建向导'作为新设备创建': 永不复用, 直接序号 key」。用户不主动勾选必得新身份 ⟹ **用户必须先理解「逻辑身份」这个需求里不存在的概念才能保住历史**。
2. **「一逻辑设备一存活实例」强制先删后建**：checkLivingInstanceUniqueness（handler_edge_device_lifecycle.go:64-86）。想「直接复用」在创建路径必然 409 —— **这正是堵死「直接复用」的关键约束**。
3. **PUT /edge-devices/:id 其实已支持原地改 node_id/channel_id**（handler_edge_device.go:886-891），**这才是用户要的「直接复用」**；但前端编辑模式**不提供节点字段**：EdgeDeviceList.vue:541 明写「设备型号与通道绑定不可在编辑中变更」，编辑区内「所属节点」字段出现次数 = **0**（[C]，grep 计数）。**能力在后端存在、在 UI 被隐藏。**

**用户实际操作成本**：至少 4 步跨 3 个页面（删旧→去 node2 建新→找到折叠的「继承历史数据」→从候选列表认出旧设备），且必须理解逻辑设备概念；漏掉一步历史即丢。**需求本体只需 1 步（原地改 node_id）。**

### 12.2 「最简方案」对照

| 方案 | 新增实体 | 新增状态 | 路径数 | 关键改动 |
|---|---|---|---|---|
| **A. 原地复用**（用户原话「直接复用」） | **0 表** | 0 | 1 | UI 暴露 PUT /edge-devices/:id 的 node_id（后端**已支持**，handler_edge_device.go:886-891） |
| **B. 稳定物理身份** | **0 表** | 0 | 1 | unified_data 加 physical_key=type:hardware_id（或直接用不变的血缘），查询按它聚合 |
| **C. 当前实现** | **2 表**（logical_devices、merge_jobs） | **8 个状态位** | **3 条身份路径** | 保形去重+合并链跟随+retention 双层+分区搬迁+重试水位 |

**分母（可复跑，2026-10-23T21:40Z）**：
~~~bash
cd backend/internal/datalifecycle && cat $(ls *.go | grep -v _test) | wc -l   # 6007
cat $(ls *_test.go) | wc -l                                                    # 8964
wc -l ../../api/handler_logical_device.go ../../api/handler_edge_device_lifecycle.go \
      ../../api/handler_edge_device_candidates.go                              # 227+244+240 = 711
wc -l ../../models/merge_job.go                                                # 42
wc -l frontend-shared/src/views/logical-device/LogicalDeviceList.vue \
      frontend-shared/src/api/logicalDevice.ts                                 # 1070+156 = 1226
wc -l "docs/archive/v4.0-重建前归档/设计/边缘设备数据生命周期管理方案.md"        # 650
~~~

| 口径 | 数量 | 证据 |
|---|---|---|
| datalifecycle 包非测试代码 | **6,007 行 / 24 文件** | T |
| datalifecycle 包测试代码 | **8,964 行 / 28 文件**（测试比实现多 **49%**） | T |
| 身份+合并+作用域+保留+搬迁 核心 12 文件 | **3,823 行**（+3,167 行测试） | T |
| 相关 API handler | **711 行 / 3 文件** | T |
| MergeJob 模型 | **42 行 / 1 文件** | T |
| 前端列表页 + API | **1,226 行 / 2 文件** | T |
| 方案文档 | **650 行**（另有 113 行现行设计文档） | T |
| 新增表 | **2** | T（pg_class） |
| 新增状态/标志位 | **8**：merged_into / merge_status 属{NULL,pending,done} / purge_requested / MergeJob.status 属{pending,running,done,failed} / watermark_phase 属{unified_data,device_data} | T（models.go:190-210、merge_job.go:30-40） |
| 身份路径分支 | **3**（PathStartup/PathCreateNew/PathDelete） | T（identity.go:30-39） |

### 12.3 是否 over-engineering？——是，且有量化证据

**用户需求的最小实现面：1 个字段变更（允许 edge_devices 原地改 node_id，后端已支持）。**
**实际交付：2 张新表 + 8 个状态位 + 3 条身份路径 + 3,823 行核心逻辑 + 8,964 行测试 + 650 行方案文档 + 711 行 handler。**

| 维度 | 最小方案 | 当前实现 | 倍数 |
|---|---|---|---|
| 新增表 | 0 | 2 | 从无到有 |
| 新增状态位 | 0 | 8 | 从无到有 |
| 核心逻辑行数 | 约 10 行（UI 暴露 node_id） | 3,823 行 | **约 380 倍** |
| 测试行数 | 0 | 8,964 行 | 从无到有 |
| 用户需理解的新实体 | 0 | 1（逻辑设备）+3 附属状态 | — |
| 运行时列表成本 | 约 0 | **93–131 s** | — |

**而且它没有真正解决需求**（§12.1）：默认路径 PathCreateNew 永不复用，用户必须主动勾选「继承历史数据」——**需求被实现了，却藏在默认关闭的折叠区后，并要求用户先理解一个需求里不存在的概念。**

**overshoot 来源（把「用户需求」与「方案自述需求」分开）**：

| 用户原始需求（1 条） | 方案自述需求（v3.3 的「4 条约束」） | 差距 |
|---|---|---|
| 同物理设备换节点后历史连续可查 | 跨节点合并 / 合并可选 / 保留期可配 / purge 可标记 | 用户从未要求「多源合并」，只要求「不丢」；「合并」是实现方选的**方案**，却被写成**需求** |
| 两个可接受解法：历史合并 / 直接复用 | 只实现「历史合并」且默认关闭；「直接复用」被「一逻辑设备一存活实例」堵死 | **需求给了两条路，实现选了最难的一条并把简单的堵死了** |

⟹ **v3.3 文档不是需求，是实现方的自我论证。** 650 行文档把「我们打算怎么做」写成「需求是什么」，随后所有对照审查都以该文档为基准 ⟹ **审的是「实现是否符合方案」，而不是「方案是否符合需求」**。这正是「曲解意图」的机制。

---

## 13. 需 Lead 裁决的矛盾点

### 矛盾 1（最高优先）：是否存在「不引入逻辑身份层」即可满足需求的路，且实现方是否知情？
- **事实**：PUT /edge-devices/:id **已支持**原地改 node_id/channel_id（handler_edge_device.go:886-891）——正是用户说的「直接复用」。
- **但**：前端编辑模式明确禁止改通道绑定（EdgeDeviceList.vue:541 文案），且无节点字段（grep 计数=0）。
- **矛盾**：**后端已有能力，前端刻意关闭。** 需裁决：是「遗漏」还是「为防止绕过逻辑设备层而刻意关闭」？
  - 若遗漏 ⟹ 开放它即可满足需求，logical_devices 整层可作为**可选增强**而非必需层。
  - 若刻意 ⟹ 需实现方给出**用户需求依据**（目前只有 v3.3 自述，无用户原话支持）。

### 矛盾 2：42 秒的归因已被证伪，但错误归因已写进生产代码注释与**用户可见文案**
- **事实**：logicalDevice.ts:100、LogicalDeviceList.vue:355 注释称「逐设备串行估算」是 42 s 主因；LogicalDeviceList.vue:395 的**用户可见文案**写「生产实测约 42 秒」。
- **实测**：主因是 scope.go:50 的 OR 导致索引失效（判据 4，贡献 >95%）；「串行估算」仅贡献 <0.3 s。
- **矛盾**：**错误归因已进入用户可见文案**（「约 42 秒」今日实测 94–131 s，数字与解释双错）。需裁决：是否允许本轮直接修正文案与注释（涉及前端 2 个文件）——**超出我的写权限（我只能写 .analysis/logical-device/runtime-evidence.md），故仅报告不动手。**

---

## 附录 A：未做的事与复核方式（不留空）

| 项 | 状态 | 复核方式 |
|---|---|---|
| 验证「把 id=13 的 max 改成当前时刻后变快」 | **未做**（需写生产数据，纪律禁止） | 影子库 UPDATE 后重跑 §4.2 的 R2 |
| 并发/长压测 | **未做**（纪律禁止） | 需 Lead 批准窗口；单次最长已 131 s |
| pg_stat_statements 端点级统计 | **未取到**（生产未安装；安装需重启容器=禁止） | pg_available_extensions 有、installed_version 空（T）；需重启才可，故不做 |
| 16→1600 台扩容曲线 | **未做**（需造数据/DDL） | 影子库灌 edge_devices 后按 ps 扫描 |
| 真实用户访问频率 | **未取到**（无端点级日志留存；pg_stat_user_tables 只有表级 seq_scan/idx_scan） | 需访问日志或 APM |
| device_data 1,685,690 条 NULL 行来源 | **未溯源**（超本任务） | 按 created_at 分组回溯写入时间 |

## 附录 B：凭据处理声明

EHOME_JWT_SECRET / PROD_SSH_PASSWORD 全程只经 docker exec printenv 与 .secrets/prod.env 环境变量进入进程内存，**未写入任何产出文件、未出现在命令回显**；本文件 JWT 仅以 $J 占位。JWT 的 sv 取自 users.session_version（生产=2、本机=7），签名密钥取自对应 web 容器环境变量。
