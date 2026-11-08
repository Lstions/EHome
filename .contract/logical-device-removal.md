# 移除逻辑设备 · 实施契约（Lead 定稿）

> 目标：**干净移除 LogicalDevice 概念层**，同时实现产品负责人真正需求（换节点后历史数据不丢）。
> 本文件是唯一口径来源；实现与验收以本文件为准。

## 1. 真正需求（唯一验收基准）

R0：**同一物理设备从节点 A 换到节点 B 后，历史数据不丢、连续可查。**
用户给的两个可接受解法：
- 解法①「历史边缘设备合并」→ 数据级迁移：把旧 `device_id` 的数据改挂到新 `device_id`
- 解法②「直接复用」→ 复用同一条 `edge_devices` 行（原地改 node/channel，或恢复已软删行）

**不实现**：逻辑身份、合并链、保形去重、pending 搬迁、按身份的保留期、逻辑设备页。

## 2. 删除清单（数据层）

| 对象 | 处置 |
|---|---|
| 表 `logical_devices` / `merge_jobs` / `backfill_jobs` | **DROP TABLE IF EXISTS**（幂等，先数行数写日志） |
| `edge_devices.logical_device_id` | **DROP COLUMN** |
| `unified_data.logical_device_id`（父表 + 全部分区） | **DROP COLUMN**（先 DROP 依赖索引） |
| `device_data.logical_device_id`（父表 + 全部分区） | 同上 |
| `data_sources.device_id` | **DROP COLUMN**；组键改为 `(edge_device_id, category)` |
| 相关索引（`*_logical_*`、`*_legacy_null_*`） | 先 DROP，再 DROP COLUMN |
| `alert_rules.target_type = 'logical_device'` | 枚举去掉该值（生产/本机 0 行） |
| 新增列 `edge_devices.data_purge_requested boolean default false` | 承载「删除时同时删数据」（原在 logical_devices.purge_requested） |

迁移必须**幂等 + 可重入 + 有日志**，PG 与 SQLite 都执行（SQLite 用 Migrator().HasColumn 判断）。
分区父表 DROP COLUMN 前必须递归 DROP 各子分区上的同名索引（查 pg_indexes + pg_inherits）。

## 3. 删除清单（代码）

后端：
- 删除 `backend/internal/datalifecycle/`：identity.go、merge.go、merge_chain.go、preview.go、purge.go、scope.go、query_scope.go、migrate.go、backfill.go、indexes.go（及对应 _test）
- 删除 `backend/internal/api/`：handler_logical_device.go、scope_union.go（及对应 _test）
- 删除 models：LogicalDevice、MergeJob、BackfillJob、MergeStatus*、BackfillJobTypeLogicalID
- `datalifecycle/retention_task.go` **保留** 8 个无关表清理器，删除按逻辑设备的循环 / 30-7 通知 / 逻辑保留期推导
- 删除 alert/automation 的 `resolveLogicalID` / `AlertTargetLogicalDevice`
- 删除 ingest/databus 的 `LogicalDeviceID` 字段
- 删除 `source_group_guard.go` 中 `guardLogicalDeviceDelete` / `findGroupReferencesByLogicalDevice`

前端：
- 删除 `views/logical-device/`、`api/logicalDevice.ts`、`components/device/LogicalDeviceCandidateSelect.vue`（及对应 spec）
- 删除菜单/路由/面包屑/通知深链/告警目标选项/来源总览 logical 列/删除弹窗逻辑设备区/批量删除 logicalCount

## 4. 新增能力（满足 R0）

### 4.1 原地换节点（解法②，主路径）
- 后端已支持 `PUT /edge-devices/:id {node_id, channel_id, ...}`（校验：新 channel 属于新 node）
- **前端必须开放**：编辑对话框增加「所属节点」+「通道」选择（编辑模式），提交体带 `channel_id`
- 语义：同一 `edge_devices.id` → `unified_data.device_id` 不变 → 历史天然连续

### 4.2 恢复已删除设备（解法②，删除后重建场景）
- 新端点 `GET /api/v1/edge-devices/deleted`：列出软删设备（id / name / type / hardware_id / 最后数据时间 / 数据量估算）
- 新端点 `POST /api/v1/edge-devices/:id/restore` body `{node_id, channel_id, name?, interval_ms?}`：清除 deleted_at + 改绑定（校验同 4.1），并按驱动补齐 ConfigTemplate
- 前端：创建向导提供「恢复已删除设备并重新绑定」入口

> **2026-10-25 修订**（二审，权威语义见 §14.2）：restore 增加与 purge 的认领互斥（清理中
> → 409）、传输通道 / device_config / 驱动总线 / 地址 / 命令容量门禁、运行态复位
> （status/init_*/enabled/last_data_at）、`hardware_id` 可在恢复请求中修正；ConfigTemplate
> 改为幂等复用（`ensureTemplatesFromDriver`）。

### 4.3 合并历史数据（解法①，清理历史/孤儿数据）
- `GET /api/v1/edge-devices/:id/data-summary?source_ids=1,2`：返回各源行数与时间范围（估算）
- `POST /api/v1/edge-devices/:id/merge-history` body `{source_ids:[...]}`：
  批量 `UPDATE unified_data/device_data SET device_id = :target WHERE device_id IN (:sources)`，
  每批 10000 行独立事务 + 批间 sleep；**幂等**（重复调用第二次 0 行）；返回 `{rows_migrated}`；
  目标设备必须存活；source 允许任意 id（含已软删/悬空）
- 前端：边缘设备页提供「合并历史数据」入口（选择目标设备 + 源设备）

> **2026-10-25 修订（含晚间产品重做，权威语义见 §14.2）**：**只能合并「已删除 + 与目标
> 同型号」的设备**；在役源 409（不再有 `allow_live_source`/自动退役）；无设备行的孤儿 id
> 400（不再做孤儿救援）；跨型号 400。响应 `{rows_migrated, sources:[{id,state:"deleted",device_type}], warnings?}`；
> 与 purge 认领互斥；台账按源逐个迁移（多源同名不撞键）；`data_sources` manual 行冲突保留
> + warning；合并后回写目标 `last_data_at`。`data-summary` 为
> `{target,sources,target_rows,source_rows}`（2026-10-25 起 item 带 `device_type`），旧 `items/total_rows` 已删除。

### 4.4 删除语义（保留既有产品行为）
- `DELETE /edge-devices/:id?delete_data=true`：软删 + 置 `data_purge_requested=true`；后台 purge 任务分批硬删该设备数据后清标记并硬删设备行
- `delete_data=false`（默认）：软删，数据保留（受系统级保留期约束）

### 4.5 保留期
- 系统级 `EHOME_DATA_RETENTION_DAYS`（默认 90）统一生效：每日任务按系统保留期删过期数据 + 按月 DROP 分区
- 不再有 per-device retention

## 5. 数据查询最终形态（性能修复）

- 所有数据读取一律 `WHERE device_id = ?`（边缘设备维度），**无 scope 解析、无 OR 兜底、无保形去重**
- `ResolveDataQueryScope` / `Scope` / `ApplyShapeDedup` / `dataScopeCond` / `historyUnionSQL` 全部删除
- 软删设备的数据：`/edge-devices/:id/data`、`/unified-data/*` 允许读取（device 行用 Unscoped 查，仅配置端点 404）
- 预期：`GET /logical-devices` 端点消失；边缘设备数据端点回到毫秒级

## 6. 数据源域最终形态

- 组键 = `(edge_device_id, category)`；`data_sources` 只留 `edge_device_id`
- `source.SourceKey` = `{EdgeDeviceID, Category}`
- 来源组（SourceGroup）不动：成员仍是 edge_device_id
- 总览端点返回 `{edge_device_id, edge_device_name, category, label_zh, latest_value, age_sec, status}`

## 7. 告警 / 自动化最终形态

- `target_type` 只保留 `edge_device`；`resolveLogicalID` 删除；帧匹配直接用 edge_device_id
- 自动化 `trigger_edge_device_id` 语义不变（本来就是 edge device id）

## 8. 验收（必须全部为效果层证据）

1. `go build ./...` + `go vet ./...` + `go vet -tags=simulation ./...` 通过
2. `go test ./...` 全绿（SQLite）；PG 集成测试在隔离库 `ehome_verify` 通过
3. 迁移在**本机 dev 库副本**上实跑：迁移前 `pg_dump`，跑两遍验证幂等；迁移后旧列/旧表不存在
4. 效果层：本机新二进制启动后
   - `PUT /edge-devices/1 {node_id, channel_id}` → 200，`unified_data` 行数不变，历史端点返回全部行
   - `POST /edge-devices/1/merge-history {source_ids:[2]}` → 行数迁移、第二次调用 0 行
   - `DELETE ?delete_data=true` → 软删 + purge 任务硬删（可用 SetSchedule 缩短验证）
   - `GET /data-sources/overview` 返回 edge_device 维度
   - `GET /logical-devices` → 404
5. 前端：`pnpm test` 全绿；`pnpm build` 成功；页面无「逻辑设备」字样

## 9. 禁止事项

- 禁止保留「逻辑身份」的替代实现（不要新增任何 identity / merge / scope 层）
- 禁止改协议层（protocol/、帧格式）
- 禁止动 esp32-collector/
- 生产库只读；迁移验证只在 `ehome_verify` 副本上做

## 10. 实际执行结果（同日闭环，2026-10-23）

### 10.1 删除范围（实际）

- **DB**：3 张表（`logical_devices` / `merge_jobs` / `backfill_jobs`）+ 4 处逻辑身份列
  （`edge_devices.logical_device_id`、`unified_data.logical_device_id`（父表 + 5 个分区）、
  `device_data.logical_device_id`（父表 + 5 个分区）、`data_sources.device_id`）+ 全部相关索引
- **datalifecycle**：删 10 个文件（identity/merge/merge_chain/preview/purge/scope/query_scope/migrate/backfill/indexes）；
  新增 `row_estimate.go`（设备维度估算）、`batch_delete.go`（共用分批常量）、`purge_edge_device.go`（设备维度 purge）
- **api**：删 3 个文件（handler_logical_device / handler_edge_device_candidates / scope_union）
- **前端**：删 1 个页面目录（views/logical-device/）、1 个 api（logicalDevice.ts）、
  1 个组件（LogicalDeviceCandidateSelect.vue）、1 个死代码页（DataSourceList.vue）
- **依赖域**：alert / automation / ingest / databus / source / datasource / dbschema / config / simulation 全部改 edge_device 维度

### 10.2 新增能力（实际）

| 端点 | 用途 |
|---|---|
| `PUT /edge-devices/:id {node_id, channel_id}` | 原地换节点（主路径；后端本来就支持，前端已开放） |
| `GET /edge-devices/deleted` | 可恢复的已删除设备列表（含数据量估算） |
| `POST /edge-devices/:id/restore {node_id, channel_id}` | 恢复已删除设备并改绑节点（历史天然连续） |
| `GET /edge-devices/:id/data-summary?source_ids=` | 行数估算 + 时间范围（删除确认 / 合并预览） |
| `POST /edge-devices/:id/merge-history {source_ids}` | 把旧 device_id 的数据 UPDATE 到本设备（分批 + 幂等） |
| `GET /edge-devices/:id/failover-logs` | 原 `/devices/:id/failover-logs`，主语改为边缘设备 |
| `ehomectl retire-logical-device` | 维护窗口单独执行数据层退役 |

### 10.3 验证结果（全部为效果层证据）

**数据迁移**（dev 库副本 `ehome_verify`，2026-10-23）：

| 项 | 结果 |
|---|---|
| 首次执行 | dropped column edge_devices.logical_device_id (non-null rows=2) / unified_data (179,532) / device_data (175,946) / data_sources.device_id (0)；dropped table merge_jobs(0) / backfill_jobs(2) / logical_devices(2) |
| 第二遍（幂等） | `rows touched=0`，零 SQL 副作用 |
| 迁移后残留检查 | `logical_device_id` 列 **0** 个；三张表 **0** 张；`%logical%` 索引 **0** 个 |
| 数据保留 | unified_data 179,532 / device_data 568,617 / edge_devices 2 行，与迁移前一致 |
| `data_sources` 索引 | `uniq_ds_source (edge_device_id, category)` + `ux_ds_one_active (edge_device_id, category) WHERE status='active'`，**均在位** |
| 唯一性实测 | 同 `(edge_device_id=1, category='rsoc')` 插第二行 → `duplicate key value violates unique constraint "uniq_ds_source"` |

**门禁**（最终态）：

| 门禁 | 结果 |
|---|---|
| `go build ./...` | 0 error |
| `go vet ./...` | 0 error |
| `go vet -tags=simulation ./...` | 0 error |
| `go vet -tags=e2e ./internal/e2e/` | 0 error |
| `go test ./...`（SQLite） | 全绿 |
| `EHOME_TEST_DB=postgres` 跑 datalifecycle/database/dbschema/datasource/api | 全绿（83s / 23s / 0.9s / 70s / 36s） |
| `pnpm test:run` | 191/191 文件，2298 passed / 1 skipped |
| `pnpm typecheck` | 通过 |
| `pnpm build` | ✓ built；`dist/assets/*.js` 中「逻辑设备」0 次、`logical-device` 路由 0 次 |

**E2E 实测**（verify 库 + 新二进制，真实 HTTP）：

| # | 场景 | 结果 |
|---|---|---|
| ① | 原地换节点 `PUT /edge-devices/2 {node_id, channel_id:1}` | 200；`unified_data` 3,912 行不变；历史端点仍返回数据 |
| ② | 合并历史 `POST /edge-devices/1/merge-history {source_ids:[2]}` | `rows_migrated=4238`；源侧清零（3,912→0 / 326→0）；目标 175,620→179,532 |
| ③ | 幂等重放同一次合并 | `rows_migrated=0` |
| ④ | `DELETE /edge-devices/3`（不带 delete_data） | 200，`data_purge_requested=false` |
| ⑤ | `GET /edge-devices/deleted` | 列出该设备，`row_estimate=20` |
| ⑥ | `POST /edge-devices/3/restore {node_id, channel_id}` | 200，`deleted_at` 清空，绑定改到新通道 |
| ⑦ | 对**存活**设备再调 restore | 409「设备 #3 未被删除，无需恢复」（恢复不是改绑定的后门） |
| ⑧ | `GET /logical-devices` | **404** |
| ⑨ | `GET /edge-devices/1/data-summary` | `{source_id:1, device_name, rows:351635, first_time:2026-09-20T08:18:24Z, last_time:2026-10-01T06:56:53Z}`（真实时间范围，非 null） |
| ⑩ | `DELETE ?delete_data=true` 后 | `data_purge_requested=true`，数据行仍在（异步 purge 不阻塞 API） |

### 10.4 修复的两个实现缺陷（都是本次实测发现）

1. **分区索引 DROP 顺序**：子分区索引不能单独 DROP（`cannot drop index ... because index <parent> requires it`）。
   改为「先删父索引（子索引随之消失），再兜底一轮」——见 `database/retire_logical_device.go`。
2. **迁移必须排在 AutoMigrate 之前**：若先 AutoMigrate 再退役，GORM 会因「同名索引已存在」
   跳过创建新定义的索引，退役随后把旧索引连同列一起删掉 ⇒ **最终库上一个唯一索引都没有**
   （同 `(edge_device_id, category)` 可插两行，唯一性静默消失）。实测已复现并修复：
   验证 `uniq_ds_source` 在迁移后确实在位且冲突会报错。

### 10.5 遗留（明确不做 / 需产品决策）

1. **`data_sources` CRUD 页面不补**（已裁决）：`data_sources` 本来就是**自动管理**的——
   投影器按「设备 × 观测字段」自动派生，启动一次 + 每 60s 对账（生产 71/71 行 `managed_by=auto`）。
   用户手动创建的只有**来源组**（SourceGroup）。
   **这不是本次移除带来的变化**：自动投影是同日更早落地的 M2
   （见 `docs/操作/数据源自动管理-落地说明-2026-10-23.md`），本次移除只把它的组键维度
   从 `(逻辑设备, 类别)` 换成 `(edge_device_id, 类别)`，自动管理本身保持不变
   （实测：移除后在 verify 库灌 3 条观测 → 重启 → 投影器 `created=3`，三行均 `managed_by=auto`）。
   因此「补一个可写 CRUD 页」是反方向：那等于把自动台账变回人工维护。
2. **生产迁移未执行**：runbook 已写（`docs/操作/生产迁移-逻辑设备层移除-v3.5.md`），等维护窗口。
   生产只做过只读采集，**从未写入**。
3. **孤儿历史 292 万行（生产 `device_id=2`）**：迁移不解决；但现在可用
   `merge-history` 救回，否则 2026-12-31 起被 retention 硬删。
4. **simulation 端到端未跑**：`harness.Preflight` 需 PG，容器内不可达；只跑到目录门禁
   `TestCatalogGate`（PASS）。R0 的两条路径由 API 层单测 + 真实 HTTP E2E 覆盖，但**未经仿真场景端到端**。


---

## 11. 独立审核结果与返工（2026-10-24）

三名审核员独立审核（后端 / 前端 / 遗留问题），报告在 `.analysis/review-removal/`。
**审核发现 1 个 P0 + 3 个 P1 是真缺陷，已全部修复并加回归守卫。**

### 11.1 【P0，已修】restore 不清 `data_purge_requested` ⇒ 用户选「不删数据」仍被硬删

**缺陷**：该列是**单向闩锁**。`DELETE?delete_data=true` 只写 `true`，restore 不清它，
于是 `删(删数据) → 恢复 → 再删(不删数据)` 会让 purge 任务硬删用户明确要求保留的历史，
而 API 应答还谎报 `data_purge_requested:false`（回显入参而非读库）。

**修法（三处）**：
- `handler_edge_device.go:1546` restore 的 updates 加 `"data_purge_requested": false`
- `:1074` / `:1152` 单删与批删改为 `Update("data_purge_requested", deleteData)`（两个方向都显式写）
- `:1091-1098` 应答改为**回读库值** `actualPurge`

**回归守卫**：`TestEdgeDevice_PurgeLatch_DeleteRestoreDeleteThenKeepData`。
⚠ 该用例初版**假绿**（变异自证发现）：它靠「第二次 DELETE 也会写 false」掩盖了 restore 的缺陷；
改成**直接读库**断言闩锁后，删掉 restore 那行立即 FAIL，已确认有效。

### 11.2 【P1，已修】`/latest-data` 与 30 天历史图表性能回归

**缺陷**：旧复合索引 `idx_devdata_logical_created` / `idx_unified_logical_sensor_ts`
随逻辑列一并删除，但**没有 device 维度的等价替代** ⇒ 契约 §5「毫秒级」不成立。
生产实测：latest-data 0.39 ms → 400~2000 ms；30 天图表 0.46 s → 5.3 s。

**修法**：`partition_mgr.go` 的 `partitionParentIndexes` 补两条复合索引
（PG 版再追加 `INCLUDE (value)`，走 index-only scan）：
- `unified_data (device_id, sensor_name, timestamp) INCLUDE (value)`
- `device_data (device_id, created_at DESC)`

**A/B 实测（250 万行、90% 数据偏斜、目标设备陈旧 —— 复现生产形态）**：

| 查询 | 无复合索引 | 有复合索引 |
|---|---|---|
| latest-data | **399.6 ms**（跳过 698,569 行） | **0.098 ms** |
| 30 天历史图表 | 96.4 ms（recheck 丢 5,482 行） | 39.0 ms（零 recheck） |

### 11.3 【P1，已修】`merge-history` 只搬 2 张时序表

**缺陷**：合并后同一台设备两个端点互相矛盾 ——
`GET /unified-data/categories?device_pk=1` 走 `unified_data` 列出字段，
`GET /sensor-fields?edge_device_id=1` 走 `sensor_field_obs` 不列出。

**修法**：同时迁移 `sensor_field_obs`（复合主键，先搬不冲突的、再把冲突行的
first/last/samples 合并进目标行）与 `data_sources`。两表不存在时容错跳过（不能让
台账问题让已成功的时序迁移报 500）。

**PG 实测**：`sfo` `rsoc` 100+80→**180**、`voltage` 保留、`current` 改挂、
源设备行全部消失；`data_sources` 无残留。重跑幂等（`rows_migrated=0`）。

### 11.4 审核确认「不是问题」的项（避免后续重复怀疑）

- 迁移必须排在 `AutoMigrate` 之前（正确且必需）
- 分区索引父/子 DROP 顺序（正确）
- `device_id=0` 的 168 万行在新 retention 下（设计已声明；当前无过期行）
- `dropIndexesOfColumn` 对「谓词引用的索引」不会漏（实测 PG 会随 DROP COLUMN 自动清）
- `is_primary` 与 `status` 可以不一致，但**零个读者**（`grep WHERE is_primary` 为空），
  不是第二真相源 ⇒ 属「漂移的死列」，与遗留方案 A 同向（可删列或加不变量）

### 11.5 环境事故（必须披露）

审核期间本机 `ehome-postgres` 容器被销毁。数据卷仍在，但卷里是 PG 16 数据，
与原 PG 18 不符，直接启动失败。**已用我 01:34 的 `pg_dump` 完整恢复 dev 库**：
179,532 / 568,617 / 2 台设备 / 2 个逻辑设备 / `sv=7`，与备份前逐项一致；
dev 库仍保留逻辑设备层（18 个 logical 列），未被本次改动触碰。


---

## 12. 遗留问题处置（方案 A，2026-10-24 执行完毕）

审核确认的「`data_sources` 手工端点 vs 自动投影」冲突，按**方案 A**处置完毕：
删除 4 个注定失败的写端点及其全部死代码，手工干预能力保留在来源组。

### 12.1 删除清单

| 层 | 删除内容 |
|---|---|
| HTTP 路由 | `DELETE /data-sources/:id`、`POST /:id/activate`、`POST /:id/deactivate`、`POST /:id/reset`（含 503 占位分支） |
| Handler | `deleteDataSource` / `activateDataSource` / `deactivateDataSource` / `resetDataSource` |
| Service | `Delete` / `Activate` / `Deactivate` / `Reset` 四个方法 |
| 死代码链 | `recordHealth` / `writeFailoverLog` / `newNotification` / `emitAll` / `emit` / `SetNotifier` / `SetDispatchNotifier` 及 Service 的 `notify`/`notifySink` 字段 |
| 接线 | `main.go` 里 `datasourceSvc.SetDispatchNotifier` 与 `SetNotifier` 两条（该服务已不是通知生产者） |
| 前端 | `dataSourceApi` 的 `remove`/`activate`/`deactivate`/`reset`；store 的 `removeSource`/`activateSource`/`deactivateSource`/`resetSource` 及其 8 条测试 |

**保留**：`GET` 列表/详情/health、`GET /edge-devices/:id/failover-logs`、
以及 `POST`/`PUT`（创建的是 `managed_by=manual` 行，投影器永不覆盖，是数据修正的后路）。

### 12.2 为什么删（证据）

1. **Deactivate 恒 409**：组键 `(edge_device_id, category)` + `uniq_ds_source` ⇒ 一个组恒一行
   ⇒ 候选集恒空。不是偶发，是数学必然。
2. **Reset 恒 400**：它要求 `status='error'`，而该状态**无任何生产写入者**（熔断状态机 M3 已删），
   `fail_count` 同样无人写 ⇒ 整条熔断链路已死。
3. **Delete 无效**：删掉的 auto 行会在下一个 60s ticker 被投影器以**新 id** 重建。
4. **手工改 status 会被覆盖**（实测 25–37s），且 `name/priority/config` 虽存活但与 status 分叉。
5. **无数据面消费者**：规则读 `automation_rules.source_group_id`，告警读 `alert_rules.*`
   ⇒ 手工切主备即使成功也不影响实际供数。
6. **能力没丢**：手工干预全在 `GroupService`（`ActivateGroupMember` / `DeactivateGroupMember` /
   组 CRUD / 健康与切换日志），**且真接入 UI**（`SourceGroupList.vue:515` 调 `activateGroup`）。

### 12.3 新增的门禁（防止死代码复活）

| 门禁 | 位置 | 断言 |
|---|---|---|
| `TestDataSourceWriteEndpointsRemoved` | `internal/api/handler_data_source_test.go` | 4 个已删端点必须 404；GET 与 POST 必须仍可用 |
| `TestNotifyWiringGate_MainWiresAllProducers` | `cmd/server/notify_wiring_gate_test.go` | needle 从 `datasourceSvc` 迁到 `sourceGroupSvc` —— 组域是数据源域现在唯一的通知生产者 |
| `TestGroup*` (notify_wiring_test.go) | `internal/datasource/notify_wiring_test.go` | 3 条通知优先级断言原样迁到 `GroupService`（Dispatcher > 旧回调 > 落库） |
| `TestIsPrimaryIsDerivedFromStatus` | `internal/datasource/service_test.go` | `is_primary` 恒等于 `status==active`，Create/Update 都不得破坏 |

### 12.4 顺带修掉：`is_primary` 漂移死列

审核发现 `is_primary` 与 `status` 可以并存为 `active + false`（POST 创建缺省 false 而首条 status=active），
是「两处记载谁是主来源」的漂移源。处置：**改为 status 的派生量**，Create/Update 均不接收入参
（`UpdateInput.IsPrimary` 字段保留仅为 API 兼容，入参被忽略并在代码注释说明）。

变异自证：把 `IsPrimary: status == StatusActive` 改回 `in.IsPrimary`，
`TestIsPrimaryIsDerivedFromStatus` 立即 FAIL（`active 行 is_primary 必须为 true，实际 false`）。

### 12.5 变异自证汇总（本轮全部做过）

| 守卫 | 变异 | 结果 |
|---|---|---|
| `TestEdgeDevice_PurgeLatch_*` | 删掉 restore 的 `data_purge_requested: false` | FAIL ✅ |
| `getDataSummary` 保留 undefined | 改回 `?? 0` | 2 条断言 FAIL ✅ |
| `TestIsPrimaryIsDerivedFromStatus` | 改回照抄入参 | FAIL ✅（初版守卫曾假绿，已修） |
| `TestGroup*` 通知优先级 | （沿用原 D-1 自检逻辑） | PASS ✅ |

### 12.6 门禁最终状态（全部真实复跑）

```
go build ./...                                  ✅
go vet ./...                                    ✅
go vet -tags=simulation ./...                   ✅
go test ./...                                   ✅ 无 FAIL
pnpm test:run                                   ✅ 191 文件 / 2294 passed / 1 skipped
pnpm typecheck                                  ✅
pnpm build                                      ✅ dist 中「逻辑设备」0 次
gofmt（改动文件）                                ✅ 0 未格式化
```


---

## 13. 生产部署完成（2026-10-24）

生产已从 `ehome-new:sg-c76744d3` 升级到 `ehome-new:lv-removal-dirty`，逻辑设备层在生产库中已彻底消失。

| 项 | 迁移前 | 迁移后 |
|---|---|---|
| `unified_data` | 34,946,938 | **34,948,541**（增量=停写前在途写入，零丢失） |
| `device_data` | 3,317,068 | **3,317,250** |
| `edge_devices` | 10（3 存活） | **10（3 存活）** |
| `data_sources` | 71（全 auto） | **71（全 auto）** |
| 逻辑列 / 表 / 索引 | 3 / 3 / 多个 | **0 / 0 / 0** |
| 数据库体积 | 11 GB | **7.8 GB** |
| 磁盘可用 | 4.4 GB | **9.5 GB** |

服务验证：`/health` 200、`/login` 200、`GET /data-sources` 200、4 个已删端点 404、
设备 60 秒内 122 次 `StatusReport`（设备 16 在线）、无 panic/error。

**执行细节与回滚路径**见 `docs/操作/生产迁移-逻辑设备层移除-v3.5.md` §六。

### 13.1 镜像 tag 已对齐（2026-10-24，dirty 构建已消除）

首次部署为 dirty 构建（改动未提交，`buildCommit=c76744d3+uncommitted-2026-10-24`，
如实标注未伪造 commit）。随后已：

- 改动分 4 个 commit 提交，HEAD = `fe8af2b0f32a813e9d6436f7c858f759ef19f132`
- 按该 commit 重建镜像 `ehome-new:sg-fe8af2b0`（label + 二进制内嵌 + `/health` 三处一致）
- 生产替换并验证：`/health` 返回完整 commit、数据持续增长、schema 仍 0/0/0、0 panic
- 旧镜像 `ehome-new:sg-c76744d3` 与容器 `ehome-prod-web-prev` 保留作回滚

详见 `docs/操作/生产迁移-逻辑设备层移除-v3.5.md` §6.9。


---

## 14. 二审与修复（2026-10-25，用户反馈「功能逻辑还有大问题」）

产品负责人 2026-10-25 反馈：「合并历史数据、恢复已删除设备目前功能逻辑还有大问题！」
本轮采用**三路并行独立审计**（后端 / 前端 / 运行时矩阵，报告在 `.analysis/rr2/`），
共确认 **2 个 P0 + 5 类 P1**，全部修复并加回归守卫。本节是**当前权威语义**；
§4.2 / §4.3 的旧描述与本轮冲突时，以本节为准。

### 14.1 审计证据与结论

| 报告 | 内容 |
|---|---|
| `.analysis/rr2/backend/findings.md` | 后端代码审计 12 条（F-01..F-12），含临时探针复现 |
| `.analysis/rr2/frontend/findings.md` | 前端审计 15 条（F-1..F-15），含 Element Plus 真实探针 |
| `.analysis/rr2/runtime/findings.md` | scratch PG + 真实 HTTP 矩阵 13 条（R-01..R-13） |

两个 P0（互相独立，任一未修都会让「恢复/合并」在用户侧不可用）：

1. **purge 与 restore 竞态（不可恢复数据丢失）**：`DevicePurger` 只在扫描时判断一次
   `deleted_at`，之后按内存快照删到底。用户在 purge 运行期间恢复设备 → 恢复返回 200、
   DB 已清 `deleted_at`/闩锁，但 purge 仍把它的历史和设备行硬删。
2. **前端「合并历史」三处断路**：手输源 id 是 `string`（Go `[]uint` 绑定 400）；源候选
   列表只列存活设备（新语义下每个选项都 409）；data-summary 响应形状改变未同步
   （预览恒「—」+ 空表）。三者独立，只修一处功能仍不可用。

### 14.2 修复后的权威语义

#### restore（`POST /edge-devices/:id/restore`）

- 仅软删设备可恢复；`deleted_at` 为空 → 409；**purge 已认领**（`data_purge_started_at`
  非空）→ 409（清理开始后不可撤销，不能制造「恢复了但数据已被删一半」的假象）。
- 校验与 create/update 对齐：节点存在、通道属于节点、`validateTransportChannel`
  （enabled / 非 GPIO-PWM 外设 / hardware_type==bus_type）、device_config 与通道硬件
  类型兼容、驱动声明的 `HardwareTypes` 覆盖通道、三元唯一性、地址门禁（历史坏地址可在
  请求体带 `hardware_id` 一并修正）、命令容量门禁（目标节点申报的每设备上限）。
- 复活语义：清 `data_purge_requested`/`data_purge_started_at`、`status=pending`、清错误、
  `init_*` 复位、`enabled=true`、`last_data_at=NULL`、清 `config_version`；清 deviceinit
  进程内缓存（否则新节点不重新 init/校准）；按驱动**幂等**补齐 ConfigTemplate。
- 已硬删/不存在的 id → 404。

#### 原地换节点（`PUT /edge-devices/:id` 改 node/channel）

- 保留原语义（同一条行，历史天然连续）；绑定变化时额外复位运行时状态：`status=pending`、
  清错误/`init_*`/`last_data_at`/`config_version`、清 deviceinit 缓存；并执行目标通道的
  传输通道/驱动总线/命令容量门禁。

#### merge-history（`POST /edge-devices/:id/merge-history`）

请求：`{source_ids: number[]}`（2026-10-25 产品重做后**没有** `allow_live_source`）。

**核心不变量：只能合并「已删除 + 与目标同型号」的设备**（产品设计见
[docs/分析/合并历史-产品设计重做-2026-10-25.md](../docs/分析/合并历史-产品设计重做-2026-10-25.md)）。

- 目标必须存活（否则 404）；源 = 目标 → 400；重复 id 去重；上限 100。
- 源准入（缺一不可）：
  - `edge_devices` 行存在（无设备行的孤儿 id → **400**：无法验证型号，不再做孤儿救援）；
  - **已软删**（在役源 → **409**：在役设备历史被搬走会变成空壳、历史永久分裂。要收尾先
    删除该设备（保留数据）再合并；只是换位置请用「编辑」换节点）；
  - `type` 与目标**完全相同**（跨型号 → **400** 并列出双方型号：不同型号的驱动/字段
    集合/单位/解析语义不同，合并会把 A 型号的物理量挂到 B 型号名下）。
- 与 purge 互斥：源被 purge 认领 → 409；源只是「请求删数据」未认领 → 先撤回标记再迁移。
- 迁移 `unified_data`、`device_data`（分批，独立事务 + 批间 sleep）、`sensor_field_obs`、
  `data_sources`（**按源逐个**迁移，避免多源同名撞复合主键）。
- `data_sources.managed_by=manual` 行与目标冲突时**保留在源设备上**并返回 warning，
  不得静默删除（人工配置投影器永不覆盖）。
- 合并后回写目标 `last_data_at = max(现有, 迁移后的最新)`。
- 台账迁移失败 → 200 + `warnings`（时序数据已迁移且幂等，可重试补齐）；响应为
  `{rows_migrated, sources:[{id,state:"deleted",device_type}], warnings?}`。
- 请求级 DB 上下文：客户端断开即中止（分批事务已提交的部分保持已迁移，幂等可重试）。

#### data-summary（`GET /edge-devices/:id/data-summary?source_ids=`）

响应拆成互不混用的两组（旧 `items`/`total_rows` 已删除）：

```json
{
  "target":  {"source_id":1,"device_name":"T","rows":3,"first_time":"...","last_time":"..."},
  "sources": [{"source_id":2,"device_name":"S","state":"deleted|live|missing","rows":1,"first_time":null,"last_time":null}],
  "target_rows": 3,
  "source_rows": 1
}
```

- `target_rows` 只含目标自身；`source_rows` 只含源合计（合并预览的「预计迁移」读它）。
- 两组各自「全部估算成功」才出现；任一失败则省略该组字段（前端显示「—」，不得兜底 0）。
- `state=missing` 表示「没有设备行」（孤儿历史，可被合并救援）。
- 整个请求共用一个 3s 估算截止时间（旧实现每个源各开 3s，100 个源最坏 300s）。

#### 已删除设备列表（`GET /edge-devices/deleted`）

- 分页 `page`/`page_size`（默认 50，上限 200），响应 `{items,total,page,page_size}`。
- 每项新增 `data_purge_requested`、`purge_in_progress`、`purge_started_at?`：清理中的
  设备前端必须禁止恢复并说明原因。

### 14.3 purge（DevicePurger）新协议

1. **原子认领**：`UPDATE edge_devices SET data_purge_started_at=now()`，条件
   「仍软删 + 仍请求删数据 + 尚未认领」；RowsAffected=0 ⇒ 不处理（可能已被恢复）。
2. **restore 行锁互斥**：restore 在事务里对同一行加行锁并检查认领，已认领 ⇒ 409。
3. **每轮只清陈旧认领**（>1h）：避免多实例/滚动发布清掉另一进程在途 claim。
   部署前提：DevicePurger 单实例运行（沿用现行部署形态）。
4. **成功路径**连带清理 `sensor_field_obs`、`data_sources`、`calibration_cache`，再条件删除
   设备行（`deleted_at IS NOT NULL AND data_purge_started_at IS NOT NULL`，RowsAffected=0
   视为 `abandoned_revived`，不谎报成功）。
5. 失败路径释放认领并记录 error 日志（下一轮重试）。

### 14.4 其它修复

| 修复 | 说明 |
|---|---|
| 模板幂等 | 新增 `ensureTemplatesFromDriver`：按 `(node_id, write_data, read_length)` 复用模板，create/PUT/restore 三路径共用。旧 restore 每次无条件新建 ⇒ multi-drop 通道「删→恢复」每次净增 1 条，16 次后节点模板超固件上限，整节点 config sync 500 |
| 模板容量错误语义 | `template reconciliation would create N templates` 归类为 `ManifestValidationError` ⇒ API 返回 400（用户可行动）而非 500 |
| 离线判定 | 「从未上报」设备的超时起点改为 `max(created_at, updated_at)`：恢复/换节点复用旧行时 created_at 可能几个月前，旧逻辑会在恢复后下一个 tick 就判离线 |
| row_estimate 归零 | PG 计划器估算在空设备上会给出 10+ 行；估算为 0 时用 `EXISTS` 探针钉死「真的没有数据」 |
| 孤儿源计数 | 无设备行的源用有界精确 COUNT 而非计划器估算（旧实现给不存在的 id 报 `rows=18`） |
| 前端契约 | 新 data-summary 形状、merge 响应 `sources[]/warnings`、deleted 分页与 purge 状态、手输源 id `string→number` 收敛；**2026-10-25 晚产品重做后**：合并弹窗只列「同型号已删除」历史设备（服务端 `device_type` 过滤）、跨型号手输阻断、空态三路径引导、删除文案与恢复能力对齐；已删除存活源确认/`allow_live_source`/`retired_sources` |

### 14.5 文档修正（上一轮审核结论有误）

- `backend-review.md` §三-10 与本文旧 §11.4 称「`rowsKnown` 只在每项估算成功时为真」——
  **与基线代码相反**：旧代码任一项成功即置真，`total_rows` 是偏小的部分和。本轮已重写为
  `target_rows`/`source_rows` 分组语义并加测试。

### 14.6 门禁（本轮修复后复跑）

- `go build ./...` / `go vet ./...` / `go vet -tags=simulation ./...`
- `go test ./...`（SQLite）全绿；关键新回归：restore 校验/状态复位、merge 多源台账/存活源/purge 互斥/manual 保留、purge 认领与台账清理、容量门禁、离线判定
- `pnpm test:run` / `pnpm typecheck` / `pnpm build`（frontend-shared）全绿
- scratch PG + 真实 HTTP 矩阵复验（`.analysis/rr2/runtime/reverify-*.md`）

### 14.7 部署注意（新增列 / 新索引）

1. **新增列** `edge_devices.data_purge_started_at`（可空，AutoMigrate 创建）：DDL 取
   排他锁但表小（生产 10 行），毫秒级。
2. **新增索引** `unified_data (device_id, timestamp)` / `device_data (device_id, timestamp)`：
   已分区的存量库首次启动会在父表 `CREATE INDEX`（SHARE 锁，逐分区建子索引）。
   34M 行级别可能数十秒~分钟级写阻塞 + 启动变慢。**建议维护窗口升级**，或提前人工
   `CREATE INDEX CONCURRENTLY`（PG18 支持分区表）建好 —— 补齐逻辑按列清单判存在，命中即跳过。
3. **DevicePurger 必须单实例运行**：认领协议（`data_purge_started_at`）按单实例设计，
   每轮只清 >1h 的陈旧认领；多副本需要 owner + 心跳租约改造。
4. 本轮未上生产；升级顺序建议：停旧服务 → 部署新镜像（迁移/建索引）→ 验证 /health、
   `GET /edge-devices/deleted`、purge 日志。

### 14.8 合并历史产品重做（2026-10-25 晚）

**产品负责人实测截图指出根本性设计错误**：目标设备 #14 是 bms，合并弹窗的源候选却是
#15 逆变器 / #16 光学雨量计 —— ① **没有同型号约束**（跨型号历史字段/单位/语义不同，合并
会污染数据）；② **列的是现役设备**而不是历史（已删除）设备。要求「从头分析产品功能设计」。

重做内容（设计文档：[docs/分析/合并历史-产品设计重做-2026-10-25.md](../docs/分析/合并历史-产品设计重做-2026-10-25.md)）：

1. **核心不变量**：merge-history 的源必须「`edge_devices` 行存在 + 已软删 + `type` 与
   目标完全相同」。跨型号 → 400（错误信息列出双方型号）；在役源 → 409；无设备行孤儿 → 400；
   **删除** `allow_live_source` / 自动退役 / 孤儿救援路径。目标仍必须存活。
2. 响应简化：`{rows_migrated, sources:[{id,state:"deleted",device_type}], warnings?}`。
3. `data-summary` 的 target/sources item 增加 `device_type`；`GET /edge-devices/deleted`
   增加 `device_type` 过滤（合并弹窗服务端只取同型号已删除设备）。
4. 前端合并弹窗重做：源候选 = 同型号已删除设备列表（名称/#id/行数/最后数据），现役设备
   完全不出现；手输 ID 用 data-summary 的 `device_type` 预校验并阻断跨型号；空态给出
   三条路径（编辑换节点 / 恢复已删除设备 / 合并仅用于旧记录已删）引导；删除存活源确认、
   "自动停用退役"相关文案与逻辑。
5. 验收：跨型号 400 且一行不搬；在役源 409（旧客户端传 `allow_live_source` 也一样）；
   同型号已删源 200 + 四表迁移 + 幂等；data-summary/deleted 的 `device_type` 正确；
   前端门禁全绿；运行时矩阵复验见
   .analysis/rr2/runtime/reverify-merge-type-2026-10-25.md。

### 15. 批量管理 + 历史设备视图（2026-10-25）

产品设计：[docs/分析/边缘设备批量管理与历史设备-2026-10-25.md](../docs/分析/边缘设备批量管理与历史设备-2026-10-25.md)。

1. **概念**：在役（`deleted_at IS NULL`）/ 历史（`deleted_at` 非空，可恢复，数据保留至系统
   保留期）/ 清理中（`data_purge_requested` 或 `data_purge_started_at` 非空，restore 409）。
   历史设备不能编辑/停用/采集；**不提供批量恢复**（恢复必须逐台选节点/通道）。
2. **批量管理（在役）** `POST /edge-devices/batch-update`：请求
   `{ids:[1..100 去重], enabled?, interval_ms?}`（至少一个字段；`interval_ms` 0..3600000）；
   逐条独立事务 + 逐条结果 `{total,succeeded,failed,results:[{id,success,error?,enabled?,interval_ms?}]}`；
   已删除/不存在 id → 该条失败；被来源组规则引用的设备**停用** → 该条失败（错误含组名/规则名，
   与单台 PUT 停用同一判定）；成功后向设备所在节点发配置变更事件。
3. **历史设备列表** `GET /edge-devices/deleted` 增加 `search`（名称/型号模糊、大小写不敏感），
   与既有 `device_type` / 分页组合；响应字段不变（含 `row_estimate`、`data_purge_requested`、
   `purge_in_progress`）。
4. **历史设备彻底删除** `POST /edge-devices/deleted/batch-delete {ids}`：只接受已软删设备；
   置 `data_purge_requested=true` 并**立即 Wake DevicePurger**，异步分批硬删「设备行 + 四张表数据」，
   开始后不可恢复；逐条结果 `{id,success,error?,queued?}`；不存在/在役 → 该条失败；已提交/清理中 →
   幂等成功；**明确不提供"只删记录保留数据"**（会制造无主孤儿数据）。
5. **DevicePurger.Wake()**：非阻塞、多次请求合并为一个 token；`main.go` 通过
   `api.SetDevicePurger` 注入（未接线时由 24h 定时任务兜底）；清理仍走 RunOnce 认领协议，
   与定时任务并发安全（重复调用幂等）。部署仍要求 **DevicePurger 单实例**。
6. 前端：列表页「在役设备 / 历史设备」双 Tab；历史表格显示数据量/最后采集/删除时间/清理状态，
   行操作 恢复 / 彻底删除，支持批量彻底删除 + 导出；在役表格多选 + 卡片「批量管理」模式，
   批量启用/停用/间隔/删除/导出，失败明细逐条展示。历史「彻底删除」与在役「软删」文案必须区分。
