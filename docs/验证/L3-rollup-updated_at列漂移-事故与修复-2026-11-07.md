# L3 rollup `updated_at` 列漂移 —— 事故与修复（2026-11-07）

> 严重级别：**P0**（L3 写入全线失败，rollup 表 0 行）
> 修复：① 手工 ALTER 止血；② 通用列校正器根治；③ 源码门禁防复发

---

## 1. 现象

生产日志每分钟刷两条：

```
ERROR: column "updated_at" of relation "telemetry_rollup_1m" does not exist (SQLSTATE 42703)
[1.039ms] [rows:0] INSERT INTO telemetry_rollup_1m (device_id, bucket, payload, updated_at)
           VALUES (14, ...) ON CONFLICT ...
```

`telemetry_rollup_1m` 行数 = **0**。L3 预聚合等于完全没工作。

生产实测表结构（事故时）：

```
            Partitioned table "public.telemetry_rollup_1m"
  Column   |           Type           | Nullable | Default 
-----------+--------------------------+----------+---------
 device_id | integer                  | not null | 
 bucket    | timestamp with time zone | not null | 
 payload   | bytea                    | not null | 
Partition key: RANGE (bucket)
```

**只有 3 列。**

---

## 2. 根因

三处代码，两处对、一处缺：

| 位置 | 内容 | 状态 |
|---|---|---|
| `backend/internal/models/telemetry_rollup.go:63` | `UpdatedAt time.Time` ⇒ GORM 隐式列 `updated_at` | 对 |
| `backend/internal/datalifecycle/telemetry_rollup.go:347` | `INSERT ... (device_id, bucket, payload, updated_at)` | 对 |
| `backend/internal/datalifecycle/partition_mgr.go:687` | 裸 DDL **只有 device_id/bucket/payload** | **缺列** |

生产 PG 走裸 DDL 建表 ⇒ 表里没有 `updated_at`；而写入 SQL 带它 ⇒ 每次 UPSERT 都报 42703。

---

## 3. 为什么全部单测都是绿的（最关键的一节）

本仓的时序表有**两条互不相交的建表路径**：

| 环境 | 建表方式 | 列来源 |
|---|---|---|
| SQLite 单测 | `db.AutoMigrate(&models.TelemetryRollup1m{})` | **model 字段** |
| 生产 PG | `partitionParentDDL["telemetry_rollup_1m"]` 裸 DDL | **DDL 模板** |

`newRollupTestDB`（`telemetry_rollup_test.go:23`）走 AutoMigrate ⇒ 模型有 `UpdatedAt` ⇒ 列一定被建出来 ⇒ 读写用例全绿。

而生产走裸 DDL ⇒ 没这列 ⇒ 必炸。

**两条路径各自自洽，交叉点上没有任何用例。** 这正是评审 B-3 说的「四处一致性」（`partitionParentDDL` / `partitionParentIndexes` / `partitionCopyColumns` / `models`）漏了其中一处，而漏的那一处恰好只在生产生效。

同类风险面：本仓有 **4 张**分区母表，每张都有这个交叉点 ——

```
unified_data        ↔ models.UnifiedData
device_data         ↔ models.DeviceData
telemetry_rollup_1m ↔ models.TelemetryRollup1m     ← 本次炸的
telemetry_frame     ↔ models.TelemetryFrame        ← L2 主写入路径，炸了更严重
```

---

## 4. 修复（三层）

### 4.1 止血：手工 ALTER（生产，零读影响）

```sql
ALTER TABLE telemetry_rollup_1m
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- 回滚
ALTER TABLE telemetry_rollup_1m DROP COLUMN IF EXISTS updated_at;
```

**不需要逐分区补。** 在本地 PG 18 用同形状分区表（母表 + 3 分区 + 已有数据）实测：

```
ALTER 前: partitions with updated_at = 0
ALTER TABLE
      relname       | has_updated_at
--------------------+----------------
 t_prop             |              1     ← 母表
 t_prop_202610      |              1     ← 自动传播
 t_prop_202611      |              1     ← 自动传播
 t_prop_202612      |              1     ← 自动传播
存量行: updated_at IS NOT NULL = t        ← 自动取默认值 now()
```

原理：分区母表 `ADD COLUMN` 是元数据操作，通过继承传播到全部 ATTACH 分区；带 DEFAULT 时走 PG 11+ fast default，不重写数据页。

### 4.2 根治：通用列校正器 `ensurePartitionParentColumns`

文件：`backend/internal/datalifecycle/partition_columns.go`（新增）

挂在 `finalizePartitionParentIndexes` 的最前面（迁移幂等路径与建表路径的收口处）：

1. 真源 = `partitionParentDDL`（与建表同一份模板，**不引入第二处列清单**）；
2. `parseDDLColumns` 解析列定义（处理括号配对与顶层逗号切分，跳过 `PRIMARY KEY` 等表级约束）；
3. 对比 `information_schema.columns`；
4. 缺失列发 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS <原样列定义>`。

四张分区表全部受益，**存量生产库在启动时自动自愈**。

失败语义：**Warn 不阻断**（与 `retireDeadTimeseriesIndexes` 同口径，Lead 裁决）。补列修的是 schema 漂移，后果是某张表写不进去，不是服务不可用；挡启动等于把小故障放大成停机。

### 4.3 防复发：源码门禁 `TestPartitionDDLMatchesModels`

文件：`backend/internal/datalifecycle/partition_ddl_model_gate_test.go`（新增）

- **不需要 PG、不依赖环境变量**，常规 `go test ./...` 就执行（PG 用例在默认 CI 会 Skip，等于没有门禁）；
- 反射读 model 的 GORM column tag ↔ 解析 DDL 列清单；
- **双向**断言：
  - model 有而 DDL 没有 ⇒ 生产必炸（本次事故）；
  - DDL 有而 model 没有 ⇒ 该列永远 NULL/默认值，读回来对不上（无声的错）；
- 遍历**全部四张**分区表，不是只盯 rollup；
- 元门禁 `TestPartitionDDLModelPairsCoversAllRegisteredTables`：新加分区表忘了登记也会红。

---

## 5. 变异验证（证明用例真的能抓到这个 bug）

### 5.1 源码门禁

把 `partitionParentDDL[telemetry_rollup_1m]` 的 `updated_at` 删掉后：

```
--- FAIL: TestPartitionDDLMatchesModels/telemetry_rollup_1m
    partition_ddl_model_gate_test.go:175: model 有而 partitionParentDDL[telemetry_rollup_1m] 没有的列: [updated_at]
        这是 2026-11-07 生产事故的形状：SQLite 走 AutoMigrate（有该列）⇒ 单测全绿，
        PG 走裸 DDL（没该列）⇒ 生产报 column does not exist、功能整段失效。
```

恢复后：`--- PASS: TestPartitionDDLMatchesModels (0.00s)`

### 5.2 PG 端到端用例

删掉 DDL 里的列后跑 `TestPGRollupUpsertAgainstBareDDL`：

```
--- FAIL: TestPGRollupUpsertAgainstBareDDL (0.91s)
    telemetry_rollup_pg_test.go:218: upsertRollupRow against bare-DDL partitioned table:
      ERROR: column "updated_at" of relation "telemetry_rollup_1m" does not exist (SQLSTATE 42703)
        这正是 2026-11-07 生产事故的形状：partitionParentDDL 与 upsertRollupRow 的列清单漂移，
        SQLite 用例（AutoMigrate）永远绿，PG（裸 DDL）必炸。
```

**复现了生产上完全相同的错误码 42703。** 恢复后：

```
--- PASS: TestPGRollupUpsertAgainstBareDDL (0.93s)
ok  ehome/backend/internal/datalifecycle  0.936s
```

---

## 6. 帧表是否有同类问题（Lead 点名排查）

**没有。** 逐列核对 + 生产真库 `\d` 双重确认：

| | model（`models/telemetry_frame.go`） | 生产 DDL / 实际表 |
|---|---|---|
| 列 | device_id, ts, seq, frame_kind, slot_id, payload, raw （7） | 同左 （7） |
| 主键 | `(device_id, ts, seq)` | `PRIMARY KEY (device_id, ts, seq)` |
| 分区键 | `ts` | `RANGE (ts)` |
| 索引 | — | `telemetry_frame_pkey` + `telemetry_frame_device_ts_key_idx`（部分索引 WHERE frame_kind=1） |

生产实测（事故期间）：

```
               Partitioned table "public.telemetry_frame"
   Column   |           Type           | Nullable | Default 
------------+--------------------------+----------+---------
 device_id  | integer                  | not null | 
 ts         | timestamp with time zone | not null | 
 seq        | smallint                 | not null | 0
 frame_kind | smallint                 | not null | 
 slot_id    | smallint                 | not null | 
 payload    | bytea                    | not null | 
 raw        | bytea                    |          | 
Indexes:
    "telemetry_frame_pkey" PRIMARY KEY, btree (device_id, ts, seq)
    "telemetry_frame_device_ts_key_idx" btree (device_id, ts) WHERE frame_kind = 1
```

`telemetry_slot` 也正常（7 列，含 `created_at`）—— 它是 AutoMigrate 建的（`MigrateTelemetryFrameToPartitioned` 里），不存在裸 DDL 漂移。

**但帧表恰恰是最危险的下一张**：它是 L2 主写入路径，一旦列漂移，每一帧都写不进去。现在由 `TestPartitionDDLMatchesModels/telemetry_frame` 守住。

---

## 7. 变更文件

| 文件 | 变更 |
|---|---|
| `backend/internal/datalifecycle/partition_mgr.go` | DDL 补 `updated_at`；`finalizePartitionParentIndexes` 首步调用列校正器（Warn 不阻断） |
| `backend/internal/datalifecycle/partition_columns.go` | **新增**：`ensurePartitionParentColumns` + `parseDDLColumns` + `splitTopLevelCommas` + `existingColumns` |
| `backend/internal/datalifecycle/partition_columns_test.go` | **新增**：解析器/切分器单测 + 「四张表 DDL 都可解析」不变量 |
| `backend/internal/datalifecycle/partition_ddl_model_gate_test.go` | **新增**：源码门禁 `TestPartitionDDLMatchesModels` 等 3 条 |
| `backend/internal/datalifecycle/telemetry_rollup_pg_test.go` | **新增**：`TestPGRollupUpsertAgainstBareDDL`（生产同路径端到端） |
| `docs/操作/分区表列一致性与detached分区-运维手册-2026-11-07.md` | **新增**：运维手册（体检 SQL / 补列 / detached 分区检测与处置） |

---

## 8. 踩坑记录（解析器本身也踩了两次）

写 `parseDDLColumns` 时遇到两个真坑，都值得记录（「代码看起来对、跑起来不对」的典型）：

### 坑 1：用 `LastIndexByte(ddl, ')')` 找列清单结尾

模板尾部还有 `PARTITION BY RANGE (bucket)`，取最后一个 `)` 会把 `bucket)` 也吞进列清单：

```
unexpected column definition "bucket)"
unexpected column definition "ts"
```

改成括号配对计数。

### 坑 2：用 `strings.Split(body, ",")` 切列

`PRIMARY KEY (id, timestamp)` 和 `VARCHAR(16)` 里都有逗号：

```
unexpected column definition "timestamp)"
```

改成 `splitTopLevelCommas`（只按括号外的逗号切）。

### 坑 3（门禁侧）：CamelCase → snake_case 不能「每遇大写就插下划线」

`DeviceID` 会变成 `device_i_d`、`DataJSON` 变成 `data_j_s_o_n`，于是门禁对 `unified_data` / `device_data` 报一堆**假红**。假红比没有门禁更糟 —— 维护者会去改 model 迎合错误的门禁。

正确规则（GORM NamingStrategy 的实际行为）：大写字母前插下划线，当且仅当「前一个字符是小写/数字」或「前一个是大写、下一个是小写」。

---

## 9. 复盘：这个 bug 为什么能进生产

1. **两条建表路径没有交叉验证** —— SQLite 走 AutoMigrate、生产走裸 DDL，写测试的人只用过前者。
2. **「四处同改」纪律靠人记** —— 评审 B-3 已点出风险，但当时没有机制，只有注释。
3. **L3 是旁路，失败不影响主写入** —— 这正是设计（失败只 Warn），但也意味着**故障没有用户可见的即时症状**，只能靠日志发现。这是正确的取舍，代价是必须有别的观测手段（`max(updated_at)` 就是那个手段，也是我们选择「加列」而不是「删列」的原因之一）。

防线现在有三层：**源码门禁**（开发期红）→ **PG 端到端用例**（集成期红）→ **运行期列校正器**（存量库自愈）。
