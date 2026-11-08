# 后端审核报告 —— 逻辑设备（LogicalDevice）概念层整体移除

审核范围：`git status` 全部 Go 改动（180 files / +4892 / -18095），任务卡 task-11（G 审核）。
审核方式：只读。除本目录报告与探测脚本外未改动任何生产代码 / 测试 / 文档；未 git commit/checkout/stash/reset；未动 `ehome` 库（未迁移）与生产（只读采集）。

---

## 一、结论

**需返工（1 项 P0：数据丢失路径）**。

概念层本身的删除是**干净而且诚实的**：无残留可执行引用、迁移顺序正确、幂等性有据、`go build/vet/vet -tags=simulation/test` 我独立复跑全绿。契约 §2/§3/§6/§7 都做到了。

但本轮**新增的 restore 能力**与 **purge 标记**之间有一个未闭合的状态机：`data_purge_requested` 是单向闩锁，restore 不清它，于是「先勾选删数据 → 恢复 → 再以不删数据删除」会让后台任务**硬删用户明确要求保留的历史**，而且 API 应答还告诉用户 `data_purge_requested: false`。我已端到端复现（3 行数据 + 设备行均消失）。这一条必须修完才能算交付。

其余 P1 是「声称修好的性能没修好，且把已经存在的性能载体删了」，与契约 §5 的「毫秒级」冲突，但不造成数据错误。

---

## 二、逐条发现

### 【P0】restore 不清 `data_purge_requested` ⇒ 用户选「不删数据」仍被硬删（不可恢复）

**位置**
- `backend/internal/api/handler_edge_device.go:1035`（`DELETE /edge-devices/:id`）只写 `true`（:1071），从不写 `false`
- `backend/internal/api/handler_edge_device.go:1521-1530`（restore 的 `updates`）清 `deleted_at`、改绑，**不含** `data_purge_requested`
- 全仓唯一把该列写成 `false` 的地方是 `backend/internal/datalifecycle/purge_edge_device.go:196-198`（复活守卫），而要走到它必须先被 purge 扫到

**证据（端到端复现，verify 库 + 本轮新构建的二进制）**

```
=== 0) 建一次性设备，并插入 3 行属于它的历史数据
INSERT 0 3
ID=6  rows=3
=== 1) 用户第一次删除：勾选「同时删除数据」
  HTTP=200
  flag=t
=== 2) 用户后悔，恢复设备（历史数据应保住）
  HTTP=200
  flag=t  deleted_at=NULL
=== 3) 用户第二次删除，这次明确**不**删数据（delete_data 缺省=false）
{"code":200,"data":{"data_purge_requested":false,"delete_data":false,"deleted":"6"},"message":"deleted"}
  DB 实际 flag=t
=== 4) 重启服务让 purge 跑（真实部署里下一次每日任务/重启即触发）
  设备行还在吗: 0  (0 = 已被硬删)
  历史数据还在吗: 0  (0 = 已被 purge 硬删)
2026/10/24 03:37:17 INFO datalifecycle: purged edge device data edge_device_id=6 rows_deleted=3
```

注意 API 应答与 DB 实际状态**直接矛盾**：应答 `data_purge_requested:false`（来自 `handler_edge_device.go:1088` 的 `deleteData` 入参，而不是读库），库里是 `t`。

**为什么是问题**
1. 用户语义（契约 §4.4）：`delete_data=false`（默认）= 软删，**数据保留**。实际数据被硬删 —— 静默、不可恢复。
2. 这不是理论路径：restore 是本轮**新增**能力（契约 §4.2），「删除→恢复→再删除」是它最主要的使用序列。恢复端点存在的全部理由是「历史数据还在」，而它恰好没清掉那个会消灭历史的标记。
3. `batch-delete` 同源（`handler_edge_device.go:1140-1145` 同样只在 `true` 时写、从不写 `false`），所以批量删除也中招。
4. 复活守卫（`purge_edge_device.go:194-205`）只覆盖「恢复后一直不再删」的场景；一旦恢复后又删（无论 `delete_data` 取何值），守卫失效 —— 我另外单独验证过守卫本身是好的：`purge abandoned (device revived) edge_device_id=5` + `flag=f`。

**建议修法（二选一，建议两个都做）**
- restore 的 `updates` 加 `"data_purge_requested": false`（最小改动，语义最直白：恢复 = 撤回删除意图，与 `AbandonedRevived` 同义）。
- `DELETE` 两个 handler 里，`delete_data` 为 false 时显式 `Update("data_purge_requested", false)`，并把应答字段改成**回读实际值**而不是回显入参。
- 加一条不变量测试：`delete_data=true → restore → delete_data=false` 之后 `purge.RunOnce` 必须 `rows_deleted=0` 且设备行仍在。

---

### 【P1】`/latest-data` 性能回归：0.39 ms → 400~2000 ms（生产最大设备）

**位置**
- `backend/internal/api/handler_edge_device.go:1288-1298`（新实现：`WHERE device_id = ? ORDER BY created_at DESC LIMIT 1`）
- `backend/internal/datalifecycle/partition_mgr.go:441-456`（`partitionParentIndexes` 只建 `device_id` / `sensor_name` / `timestamp` / `edge_device_id` 单列索引，**没有** `(device_id, created_at DESC)`）
- `backend/internal/database/retire_logical_device.go:48-53`（删掉 `idx_devdata_logical_created` 后无替代）

**证据（生产库只读 EXPLAIN ANALYZE，同一台设备 device_id=14：device_data 1,005,932 行）**

```
--- HEAD 形态 (UNION ALL 双分支, 走 idx_devdata_logical_created)
Execution Time: 0.414 ms
Execution Time: 0.381 ms
Execution Time: 0.387 ms
--- 新形态 (device_id 单条件, 无 created_at 索引)
Execution Time: 1950.816 ms
Execution Time: 419.002 ms
Execution Time: 404.683 ms
```

计划差异（同一台设备）：新形态退化为 `Parallel Bitmap Heap Scan`（各分区 `device_id` 索引）→ `Sort Key: created_at DESC` + `top-N heapsort`，读 `193,624` buffers；HEAD 形态是 `Index Scan using device_data_202610_logical_device_id_created_at_idx` → 单行即止。

**为什么是问题**
- 这个端点**每次打开设备详情页都会调**（`frontend-shared/src/composables/useDeviceData.ts:107-117`，`/latest-data` 与 `/latest-values` 并行）。
- 生产上正是「BMS 页慢」的受害者：`frontend-shared/src/views/edge-device/bms/BmsDetailPage.vue:775` 的注释写着「生产实测：历史查询并发时 latest-data 会 10s 超时，页面就常驻一行」。本次改动把这条从「已经踩过坑、已经修好」的路径重新推回扫全表。
- 契约 §5 写明「预期：…边缘设备数据端点**回到毫秒级**」。实测 400~2000 ms，前提不成立。

**建议修法**
在 `partitionParentIndexes` 的 `device_data` 组补 `CREATE INDEX %s_device_id_created_idx ON %s (device_id, created_at DESC)`（对应 `HEAD:indexes.go` 的 `idx_devdata_logical_created` 换成 device 维度），`unified_data` 组同理需要 `(device_id, sensor_name, "timestamp") INCLUDE (value)`（见下一条）。

---

### 【P1】30 天历史图表仍是 5.3 s；旧复合索引被删且无替代 ⇒ 永久失去 0.46 s 的可能

**位置**
- `backend/internal/api/history_points.go:259-264`（`historyDeviceCond` = 纯 `device_id = ?`）
- `backend/internal/datalifecycle/partition_mgr.go:441-456`（无 `(device_id, sensor_name, timestamp)`）
- 契约 §5「所有数据读取一律 `WHERE device_id = ?`…预期回到毫秒级」

**证据（生产，device 14 / rsoc，三个口径各 3 次）**

```
########## 历史图表 30 天 (前端 DataPanel 30d)
--- 旧索引形态 (logical_device_id 等值, 走 idx_unified_logical_sensor_ts+INCLUDE(value))
Execution Time: 932.294 ms / 463.406 ms / 468.007 ms
--- HEAD 生产真实 SQL (OR scope 形态)
Execution Time: 5498.203 ms / 5534.228 ms / 5557.666 ms
--- 新形态 (device_id 等值, 迁移后唯一形态)
Execution Time: 5312.684 ms / 5914.766 ms / 5540.184 ms
```

新形态的执行计划：`Bitmap Index Scan on unified_data_202610_sensor_name_idx`（`sensor_name='rsoc'` 命中 1,121,669 行）→ `Rows Removed by Index Recheck: 9,479,274`，读 458,167 buffers。

**反事实（verify 库事务内建索引后回滚，只改计划不改数据）**

```
=== with composite index
Index Cond: ((device_id = 2) AND (sensor_name = 'rsoc') AND ("timestamp" >= ...))
Execution Time: 0.107 ms     ← 原 0.547 ms
```

**为什么是问题**
- 契约 §5 把「无 OR / 无 scope」当成性能修复交付，但**删掉 OR 只解决了一半**：OR 让复合索引失效（HEAD 5.5 s），删掉 OR 后若没有对应的 device 维度复合索引，仍然是 5.3 s。两个数字在统计上无差别（5.5 vs 5.3）。
- 更要紧的是：`idx_unified_logical_sensor_ts` / `idx_devdata_logical_created` 是本项目**已交付的 BMS 慢修复成果**（见 `docs/分析/BMS慢修复-实施契约-2026-10-20.md` §一），本次删除后没有等价替代，等于把修复的载体撤掉。任何后续调优都得从零重建。
- 生产口径：`unified_data` 34,122,739 行、device 14 独占 29.2M 行 —— 单设备索引选择性极差，这正是当初需要复合索引的原因。

**建议修法**
随迁移一并建 `(device_id, sensor_name, "timestamp") INCLUDE (value)`（unified_data）与 `(device_id, created_at DESC)`（device_data），并在 `RetireLogicalDeviceLayer` 里先删旧索引、后建新索引（顺序与现有 `dropIndexesOfColumn` 一致）。如果暂时不建，必须在契约 §5 把「毫秒级」改成「30 天窗口仍为秒级，待补复合索引」，不能留一个不成立的验收口径。

---

### 【P1】`merge-history` 只搬 2 张表：合并后「字段清单」两个端点互相矛盾

**位置**
- `backend/internal/api/handler_edge_device.go:1625`（`for _, table := range []string{"unified_data", "device_data"}`）
- 未迁移：`sensor_field_obs.device_id`（写入点 `handler_data_source.go:411`、`handler_source_overview.go:254`、`datasource/field_meta_derive.go:194`、`datasource/group_crud.go:148`、`datasource/projection.go:302`；`models/sensor_field.go:26` 注释明确「DeviceID = EdgeDevice.ID」）
- 未迁移：`data_sources.edge_device_id`

**证据（verify 库：把设备 2 并入设备 1）**

```
=== merge-history: 把设备2 的历史并入设备1
{"code":200,"data":{"rows_migrated":4238,"sources":[2]}}
=== AFTER
dd2|0        dd1|175946     ← 时序表搬走了
ud2|0        ud1|179532
sfo1|2       sfo2|1       ← 观测台账没搬（rsoc 仍挂设备 2）
ds1|2        ds2|1        ← 数据源没搬（rsoc 仍挂设备 2）
=== overview 里设备2 是否还挂着来源（数据已空）
items by edge_device_id: Counter({1: 2, 2: 1})
device2 sample: [{'edge_device_id': 2, ..., 'category': 'rsoc', 'latest_value': 85, 'age_sec': 2665487, 'status': 'stale'}]
```

后果（代码可判定，不依赖推断）：
- `GET /unified-data/categories?device_pk=1` 走 `unified_data`（`handler_data.go:229-233`）⇒ 合并后**会**列出 `rsoc`；
- `GET /sensor-fields?edge_device_id=1` 走 `sensor_field_obs`（`handler_sensor_fields.go:142-147`）⇒ **不会**列出 `rsoc`。

两个都叫「这台设备有哪些字段」的端点，在同一台设备上给出不同答案。前端 `DataPanel` 用的是前者（`DataPanel.vue:731`），设备页字段表用的是后者，于是「有数据但字段表里没有」。

**为什么是问题**
- 契约 §4.3 的 merge-history 语义是「把旧 device_id 的数据 UPDATE 到本设备」，实现只覆盖了 2 张时序表；台账类表留在死设备上，产生**半迁移状态**。这不是崩溃，而是两个真相源。
- 同一根因也让 `data-sources/overview` 给一台已经没有任何数据的设备继续挂「stale 来源」（上例 device2 rsoc，`age_sec=2,665,487`≈31 天）。

**建议修法**
merge-history 里补 `UPDATE sensor_field_obs SET device_id = ? WHERE device_id IN (...)`（注意 `(device_id, sensor_name)` 可能撞主键/唯一键 → 用 `INSERT ... ON CONFLICT DO UPDATE` 合并 samples 或先 `DELETE` 目标侧重复行），并处理 `data_sources` 冲突（`uniq_ds_source` 二元组会撞）。若不打算搬，就在端点文档与响应里显式声明「只迁移时序数据，字段台账/数据源需另行处理」，别让调用方以为数据完整搬家了。

---

### 【P2】`GET /edge-devices/deleted`：静默截断 200 条 + 每项 N+1 估算

**位置**：`backend/internal/api/handler_edge_device.go:1426-1431`（`Limit(200)`，无分页、无 total）、`:1453-1457`（每台设备一次 `EstimateDeviceRows`）

**证据**：`grep -n 'Limit(200)' → 1431`；返回体只有 `{"items": [...]}`，无 total。估算段各带 3 s 超时，最坏 200×3 s。

**为什么是问题**：删除设备超过 200 台时列表**静默少显示**，用户无法知道「还有更多」；`row_estimate` 缺失时前端无法区分「0 行」与「估算失败」（同一类问题的前半段已由 Lead 修 `total_rows` 解决，这个端点仍然如此）。

**建议**：加 `page/page_size + total`；`row_estimate` 与 `total_rows` 用同一口径（估不出就省略 + 前端显示「—」）。

---

### 【P2】`data-summary` 的 `source_ids` 无数量上限（`merge-history` 有 100 上限）

**位置**：`backend/internal/api/handler_edge_device.go:1203-1221`（解析循环无 `len` 检查）对比 `:1591-1593`（`at most 100 source_ids`）。

**证据（实测耗时随 source_ids 增长）**
```
source_ids=1   -> items=1   time=0.026064s
source_ids=20  -> items=20  time=0.218021s
source_ids=100 -> items=100 time=0.412566s
```
每个 id 的成本 = 1 次 `EXPLAIN` + 4 次边界 `Pluck`（2 张表 × ASC/DESC）。当前 100 个 id 0.41 s 尚可，但**没有上限**且与 merge-history 的上限不对称（预览能接受的规模应当 ≥ 实际能执行合并的规模）。

**建议**：`source_ids` 数量与 merge-history 对齐（≤100），超出返回 400。

---

### 【P2】`is_primary` 与 `status` 可以不一致（当前无读者，属埋雷）

**这是 Lead 专门问的问题，结论：能不一致，且是确定性的；但它今天不是第二个真相源。**

读写点全量（`grep -rn 'IsPrimary\|is_primary'` 非测试）：
- **写**：`datasource/projection.go:193,231`（auto 行，`is_primary = (status == active)`）；`datasource/service.go:222`（Create，`IsPrimary: in.IsPrimary`，默认 false）；`service.go:288-289`（Update 才改）；`handler_data_source.go:205,233`（透传入参）。
- **读**：**零个**。`grep -rn 'is_primary = \|Where("is_primary'` → 无命中；前端视图/组件也不用（只在 `api/dataSource.ts:32` 的类型声明里）。所有「谁在供数」的判定一律用 `status`（`service.go:483-498` Activate、`:538-575` Deactivate、`projection.go:213`、`group_state.go:136-149`）。

**不一致是必然的**：
`service.go:210-212` 创建时 `groupCount == 0 → status = StatusActive`，而 `:222` `IsPrimary: in.IsPrimary`（`handler_data_source.go:205` 直接透传，缺省 false）。所以任何一条**手工创建**的来源，落库必然是 `status=active` + `is_primary=false`。
实测抓到（创建 `(edge_device_id=1, rsoc)` 的响应）：
```
{"code":201,"data":{"id":24,"category":"rsoc","edge_device_id":1,...,"is_primary":false,"status":"active",...}}
```
另外 `Activate/Deactivate/Delete` 三个状态转移（`service.go:493,497,540,566,569`）只 `Update("status", ...)`，**不同步** `is_primary`，所以切换后仍是旧值。

**判断**：不构成第二个真相源（没有读者按它过滤 ⇒ 不存在「两个答案」），但这是一个**已经漂移的死列**：谁将来写 `WHERE is_primary = true` 就会拿到与 `status` 相反的答案，而且 `unnamed 单条来源 is_primary=false` 是常态。
**建议**（与 req-analyst 方案 A 同向）：删除该列（它也不在契约 §6 保留字段里），或至少在 `Activate/Deactivate/Delete` 三处同步维护并加一条不变量测试 `is_primary == (status == 'active')`。

---

### 【P2】`notify_wiring_gate` 从 6 条 needle 降到 4 条，`sourceGroupSvc` 始终没进清单

**位置**：`backend/cmd/server/notify_wiring_gate_test.go:53-65`（4 条）、`backend/cmd/server/main.go:370,408`（两处真实接线）、`backend/internal/datasource/group_service.go:705-706`（`g.notifySink.Create(...)` 真实产通知）

**证据**：HEAD 的 needle 是 6 条（含 `migrator.SetNotifier` / `retentionTask.SetNotifier`），本轮删 2 条后为 4 条；而 `sourceGroupSvc.SetDispatchNotifier` 在 **HEAD 的 needle 列表里也不存在**（`git show HEAD:...notify_wiring_gate_test.go | grep '{"' ` 只有 6 条，无 group svc），且 `strings.Contains` 不会把 `datasourceSvc.SetDispatchNotifier(notifyDispatcher)` 命中到 `sourceGroupSvc.SetDispatchNotifier(notifyDispatcher)`（两者互不为子串，已用 Python 验证 `False / False`）。

**为什么是问题**：门禁的第 (2) 条自述是「对 4 个通知生产包逐个接线 —— 漏一个就有半个域的通知永不外发」。`GroupService` 是真实生产者且已接线，但没被门禁覆盖 ⇒ 有人删掉 `main.go:408` 时门禁仍然全绿。这是**既有缺口**（非本轮引入），但本轮把 needle 从 6 降到 4、并在 `:57-63` 写了「下次有人新增通知生产者必须回来加 needle」，正好该在这一轮补上。

**建议**：加 `{"来源组接线", "sourceGroupSvc.SetDispatchNotifier(notifyDispatcher)"}`，并在变异自检里加一条「单独删掉 group svc 接线必须判红」。

---

### 【P2】`retire_logical_device.go:77` 的 `DROP COLUMN IF EXISTS` 在 SQLite 上是语法错误

**位置**：`backend/internal/database/retire_logical_device.go:77`

**证据**（本机 SQLite 3.46.1 实测）
```
sqlite: 3.46.1
FAIL: ALTER TABLE t DROP COLUMN IF EXISTS b -> OperationalError near "EXISTS": syntax error
OK  : ALTER TABLE t DROP COLUMN b
```
同一句式已被复制到 `backend/internal/dbschema/source_group_test.go:74-78`（用于清理持久化 public schema 的残留列）。

**实际可达性**：`internal/database/gorm.go:33` 硬编码 `postgres.Open`，生产与 `ehomectl` 都只会是 PG；SQLite 分支只在测试库（`testutil`）里跑，且被 `HasColumn` 守卫包裹，所以**当前不可达**。但这是一个「方言假设没有写下来」的坑：`RetireLogicalDeviceLayer` 的注释声称支持两种方言，实际只有 PG 分支成立。

**建议**：按方言分派（`db.Migrator().DropColumn(...)` 或 PG 用 `IF EXISTS`、SQLite 用裸 `DROP COLUMN`），或在注释里明确「本函数只支持 PostgreSQL」。

---

### 【P2】契约 §5 的验收口径与事实不符（措辞问题，非代码缺陷）

契约 `.contract/logical-device-removal.md:79` 写「预期：…边缘设备数据端点回到毫秒级」。实测（生产，§二 P1 两条）：`/latest-data` 400~2000 ms、30 天历史 5.3 s。
**建议**：要么补索引让这句话成立，要么据实改写（保留「无 OR / 无 scope」的定性收益，删掉未经实测的「毫秒级」预期）。

---

### 【P2】陈旧注释仍以逻辑设备描述组键（4 处）

- `backend/internal/models/source_group.go:8-11`：「组要**跨逻辑设备**…而 `data_sources` 组键是 `(LogicalDevice.ID, category)`」
- `backend/internal/models/source_group.go:99`：「`data_sources` 的唯一索引 `(device_id,category,edge_device_id)`」
- `backend/internal/source/registry.go:17`：「来源键是 `(logicalDevice, category)`」
- `backend/internal/automation/selector.go:15`：「规则可能绑定逻辑设备（merge-chain）」
- `backend/internal/alert/source.go:12`：「存量单设备/逻辑设备规则行为不变」

代码已全部改为 edge_device 维度，但注释仍指向已删除的概念。对维护者的误导成本高于改动成本（尤其 `source_group.go:99` 描述的那个三元组索引已经不存在了）。

---

### 【P2】测试削弱（逐条判断，2 处需要补回）

| 文件 | 变化 | 判断 |
|---|---|---|
| `datalifecycle/partition_mgr_pg_test.go` | -388 行；删 `TestRetention_PartitionDropUsesMaxRetention`、`TestDropPartitionsBefore_CurrentMonthKept` | **前者合理**（`MAX(retention_days)` 数据源已删，对象消失）；**后者不合理**：`DropPartitionsBeforeFor` 仍存活（`partition_mgr.go:123`），「当前月分区不得被 DROP」的正向断言现在只剩 `partition_mgr_generic_pg_test.go:91`「当前月分区必须存在」（那是建分区断言，不是不删断言）。建议补回 |
| `dbschema/source_group_test.go` | +30 行；先 `DROP INDEX uniq_ds_source` 再种 dup | **合理**，注释解释充分（不摘索引就永远进不到 fail-closed 分支，「插不进去」冒充「查得出来」） |
| `datasource/service_test.go` | 删 `TestR5_DeactivateActivePromotesCandidate`、`TestR7_ActivateManualSwitch`、`TestDelete_ActiveWithStandbyCandidate_PromotesWithoutDoubleActive` | **结构上不可能通过**（见误报清单第 5 条：候选集恒空），非「为绿而删」。但结果是 **failover/接替路径现在零测试覆盖**，建议以「期望 409」的形式保留这三条用例 |
| `datalifecycle/notify_wiring_test.go` | 重写为两条源码扫描 | **合理**，且是正向补强（`TestRetentionTask_HasNoNotifierInjectionPoint` 扫源码证明注入点不存在） |
| `api/handler_lifecycle_integration_test.go`、`api/handler_data_lifecycle_test.go`、`datalifecycle/estimate_degrade_test.go` | 删除 | **合理**，对象（scope / logical 维度）已不存在 |

---

## 三、误报清单（怀疑过、核实后不是问题）

1. **`RetireLogicalDeviceLayer` 放在 `DB.AutoMigrate` 之前是否正确**
   → 正确，且**必须**如此。`gorm.go:71-78` 的理由（先 AutoMigrate 会让 GORM 因同名索引已存在而跳过创建，随后退役删掉旧索引 ⇒ 唯一索引静默消失）成立。我另外确认 `dbschema.EnsureSourceGroupSchema` 在 `gorm.go:178`（AutoMigrate **之后**），因此它建 `ux_ds_one_active` 时该列已经删干净，无冲突。幂等：`HasColumn`/`HasTable` 前置守卫，第二遍 `rows touched=0`（契约 §10.3，代码可复核）。

2. **分区索引 DROP 顺序（父 vs 子）**
   → 正确。`dropIndexesOfColumn`（`retire_logical_device.go:112-157`）两轮 + `ORDER BY is_child ASC` + 递归 `pg_inherits` CTE，符合 PG「先删父索引、子索引随之消失」的规则。

3. **`device_id = 0` 的孤儿行（生产 device_data 1,775,109 行）会被新版全表 retention 删掉**
   → HEAD 的 `deleteExpired` 用 `scope.Cond()` 限定行；新版 `deleteExpiredRows` 无 scope（`retention_task.go:343`）。**看似新增暴露面，实为契约与设计已声明的预期**：`docs/设计/边缘设备数据生命周期.md:245-252`「它们只受系统级保留期约束」。且实测未到期：`device_id=0` 最早 timestamp = 2026-10-01，`< now()-90d` 计数 = **0**。生产 `EHOME_DATA_RETENTION_DAYS=90`、`MAX(retention_days)=90`，窗口与旧口径一致 ⇒ 无人在不知不觉中丢数据。

4. **`latest-values` 缓存 miss 回落 303 s**
   → 与 HEAD 同形（HEAD 口径实测 292.8 s，因为它也是 `ORDER BY sensor_name ASC, timestamp DESC` 全量拉取 + Go 侧去重）。**不是本轮回归**，但属既有 P1 缺陷，且与本轮的索引删除叠加后无改善空间。建议单独立项：改成 `SELECT DISTINCT ON (sensor_name) ... ORDER BY sensor_name, timestamp DESC`（PG）。

5. **`data_sources` deactivate 恒空（req-analyst 的结论）**
   → **同意，且我复核了数学必然性**：`service.go:538-546` 候选查询键是 `(edge_device_id = target.EdgeDeviceID AND category = target.Category AND id <> target AND status = 'standby')`，而 `uniq_ds_source`（`models/models.go:322-323`）是 `(edge_device_id, category)` **唯一索引** ⇒ 同键第二行**插不进去** ⇒ 候选集恒空 ⇒ active 行 deactivate **100% 返回 409**。我实测两次：创建同键第二行 → `409 conflict: data source already exists for (edge_device_id=1, category=battery_voltage)`；deactivate 唯一 active → `409 no healthy standby candidate`。
   → 需要补一句 req-analyst 没说的：**来源组路径不受影响**，主备切换走 `source_group_members`（`models/source_group.go:104-106` 的 `ux_sgm_member`/`ux_sgm_one_active`），它的组键是 `(group_id, edge_device_id)`，天然支持多成员。所以「主备能力」没丢，丢的是 `data_sources` 这条 legacy 主备路径。

6. **投影器会覆盖手工配置（req-analyst 说只覆盖 status/is_primary）**
   → 同意。`projection.go:181-184` 对 manual 行 `continue`（跳过、不覆盖）；对 auto 行只写 `status/is_primary/derived_at/last_success`（`:224-239`），`name/priority/description/config/max_fail_count` 不碰。

7. **`retire` 的 `count(*)` 留痕会在生产迁移窗口卡住**
   → 实测生产 `unified_data` 34 M 行：`SELECT count(*) ... WHERE logical_device_id IS NOT NULL` = **5.2 s**（Parallel Seq Scan，读 442,159 buffers）；`device_data` 3.2 M 行 = 752 ms。属于「一次性、可预期」，不构成阻塞，但应写进 runbook（`docs/操作/生产迁移-逻辑设备层移除-v3.5.md`）里，避免运维以为迁移卡死。另一个数据点：`logical_device_id` 列上挂着 **25 个**依赖索引对象（父+分区），DROP COLUMN 会连带删掉它们 —— 这也是 5.2 s 之外的额外窗口成本。

8. **生产 `data_sources` 迁移后是否会撞 `uniq_ds_source`**
   → 不会。生产 71 行、71 个不同 `(edge_device_id, category)` 键、**0 重复**；且全部 `status=active, managed_by=auto`。旧索引形态是三元组 `(device_id, category, edge_device_id)`，退役脚本先删旧索引再 AutoMigrate 重建二元组，顺序正确。

9. **`is_primary` 的语义冲突（数据源真相源）** → 见 P2 条目：能不一致、但是死列，无读者。

10. **`data-summary` 缺 `total_rows`（Lead 已修）** → 复核认可。`handler_edge_device.go:1230-1284`：`rowsKnown` 只在**每一项都估算成功**时为真，任一失败则整体省略 `total_rows`；注释明确「报一个偏小的合计比不报更危险」。这个「宁可省略也不给偏小值」的取向与 `row_estimate.go:28-30`（超时省略）一致，比 `?? 0` 安全。唯一建议：把「省略」与「真 0 行」在响应里区分开（例如同时给 `rows_known: false`），否则前端仍要靠 `total_rows === undefined` 猜。

---

## 四、未覆盖清单（说明原因）

| 事项 | 原因 |
|---|---|
| PG 集成测试复跑（`EHOME_TEST_DB=postgres`） | 审核中途本机 PG 容器（`ehome-postgres`）消失，`127.0.0.1:5432` 已关闭，无法重跑。契约 §10.3 声称的全绿我**未独立验证**（只验证了 SQLite 全绿）。审核前期的 PG 侧结论均来自该容器消失前的实测输出，已逐条贴出原始输出。 |
| simulation E2E / `harness.Preflight` | 需 PG（同上），且契约 §10.5-4 已声明未跑。 |
| 前端审核（`/deleted` 页面、`is_primary` 展示） | 属另一名审核员范围，我只从后端接口契约角度判断。 |
| `esp32-collector/` / `protocol/` | 明确禁止触碰。 |

---

## 五、我做的只读探测（全部未改动生产代码）

1. 用 `database.AutoMigrate` 同路径构建新二进制（`.analysis/review-removal/bin/ehome-server`）在 `ehome_verify` 上起 `ehome-review-srv`（`:8081`）做效果层验证：`/logical-devices`→**404**、`/edge-devices/deleted`→200、`/edge-devices/1/data-summary`→200 带真实首末时间、`/data-sources/overview`→200 edge_device 维度、`/edge-devices/1/data`→200。
2. 生产库只读 `EXPLAIN (ANALYZE, BUFFERS)` A/B（HEAD 形态 vs 新形态），命令与输出见 §二各条。
3. `ehome_test` 库里建临时 schema 验证「谓词引用列（`WHERE logical_device_id IS NULL`）的索引不会被 `dropIndexesOfColumn` 枚举到」这一疑点 —— 结论：**PG 在 `DROP COLUMN` 时会自动连带删掉谓词引用该列的索引**（实测 `pg_indexes` 归 0、列已删、无报错），所以枚举遗漏不构成迁移失败。此为误报，未列入发现。
4. verify 库上的探针数据（一次性设备、假行）**已全部清理并复原**到迁移后基线：`unified_data 179532 / device_data device 行 175946 / edge_devices(1,2) / data_sources(16,17,22) / sensor_field_obs 3 行`。

---

## 六、复跑命令与输出（原样）

```
$ docker run --rm -v $PWD:/src -w /src/backend -e GOFLAGS=-mod=mod \
    -e GOMODCACHE=/src/.tmp-probe/gomodcache -e CGO_ENABLED=1 \
    -e HTTPS_PROXY=http://192.168.20.6:10808 -e GOPROXY=https://proxy.golang.org,direct \
    golang:1.27.3-alpine sh -c 'apk add --no-cache gcc musl-dev >/dev/null 2>&1;
      go build ./...; go vet ./...; go vet -tags=simulation ./...; go test ./... -count=1'

=== go build ./...
build_exit=0
=== go vet ./...
vet_exit=0
=== go vet -tags=simulation ./...
vetsim_exit=0
=== go test ./... -count=1
ok  	ehome/backend/internal/api	13.678s
ok  	ehome/backend/internal/database	2.240s
ok  	ehome/backend/internal/datalifecycle	1.630s
ok  	ehome/backend/internal/datasource	20.521s
ok  	ehome/backend/internal/dbschema	0.037s
```

（完整全包输出在 `.analysis/review-removal/_gate_rerun.log` 与 `_build_test.log`；38 个包全 `ok`，无 `FAIL`。）

```
$ grep -rn 'logical_device\|LogicalDevice\|logicalID' --include=*.go backend/ | grep -v _test | grep -v '// '
backend/cmd/ehomectl/main.go:33:		rows, err := database.RetireLogicalDeviceLayer(db)
backend/internal/database/gorm.go:79:	if _, err := RetireLogicalDeviceLayer(DB); err != nil {
backend/internal/database/retire_logical_device.go:40:var retiredLogicalTables = []string{"merge_jobs", "backfill_jobs", "logical_devices"}
backend/internal/database/retire_logical_device.go:49-51:	{Table: "edge_devices"|"unified_data"|"device_data", Column: "logical_device_id"}
```
⇒ 残留**仅有退役脚本自身的表名/列名字面量**，无任何业务代码引用。删除完整性成立。

```
$ grep -rn 'is_primary = \|is_primary=?\|Where("is_primary' --include=*.go backend/ | grep -v _test
(no output)
```

---

## 七、修复优先级建议

1. **必修（P0）**：restore / delete 两个 handler 清 `data_purge_requested`，应答改为回读实际值，补不变量测试。
2. **应修（P1）**：`partitionParentIndexes` 补 `(device_id, created_at DESC)` 与 `(device_id, sensor_name, timestamp) INCLUDE (value)`；否则契约 §5 的「毫秒级」必须改口径。
3. **应修（P1）**：merge-history 补 `sensor_field_obs`（+ `data_sources` 冲突处理），或显式声明「只迁移时序表」。
4. **可后续（P2）**：`/deleted` 分页、`data-summary` source_ids 上限、`is_primary` 删列、gate 补 group svc needle、陈旧注释、补回两条被删断言、RETIRE 的 SQLite 方言说明。
