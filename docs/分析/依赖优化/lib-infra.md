# 基础设施组件新能力分析（PostgreSQL 18 / Prometheus 3 / Alertmanager 0.34 / Alpine 3.24 / Node 24 / Actions v7 / ESP-IDF 6.1）

> 日期：**2026-10-23** ｜ 范围：本次升级引入的基础设施组件**能力**（不是版本号罗列）
> 前置盘点：[依赖升级盘点/infra-firmware.md](../依赖升级盘点/infra-firmware.md)
> 性质：**只分析，未修改仓库任何业务代码**；所有探针、临时容器与脚本在 `.tmp-deps/infra-probe/`（可复跑）
> 数据可信度：每条结论标注 **【实测】**（本机跑出）/ **【官方文档】**（给 URL）/ **【计算】**（给算式）/ **【未能核实】**（给原因）

---

## 0. 结论速览

| 组件 | 新能力 | 本仓库是否受益 | 最强证据 | 落点 |
|---|---|---|---|---|
| **PG 18 skip scan** | 复合 btree 索引前导列未被约束时，可跳过前导列做多次索引搜索 | ✅ **是，唯一直接受益点** | 【实测】同数据同索引，PG18 出现 `Index Searches` 并改用 `idx_unified_logical_sensor_ts`；`handler_sensor_fields` 查询形态 **27.1 ms → 13.0 ms（-52%）**，索引读块 629→109 | `handler_sensor_fields.go:364-367`；**前置条件**：`idx_unified_logical_sensor_ts` 必须先建（当前 dev 库**不存在**） |
| **PG 18 AIO** | `io_method=worker`（默认）/ `io_uring`，顺序扫/位图堆扫/VACUUM 并发读 | ✅ 冷缓存顺序扫 **-25%~-27%**；⚠ 对**采集写入无影响**（分区 insert 1960 vs 1984 tps，差值在噪声内） | 【实测】636 MB 表冷缓存三轮 A/B | 无需配置，升级即得（默认 worker） |
| PG 18 `uuidv7()` | 时间有序 UUID | ❌ 不适用 | 主键全为 `BIGSERIAL`（`models.go:301`、`:315`） | 无（实测 uuidv7 做主键确实省 21% 索引体积，但本仓不用 uuid 主键） |
| PG 18 虚拟生成列 | 读时计算，且成为默认 | ❌ 不适用 | 全仓无 `GENERATED` 列（grep 0） | 无 |
| PG 18 OAuth 2.0 | pg_hba `oauth` 认证 | ❌ 不适用 | 只有密码认证（`docker-compose.yml:11-12` → `EHOME_DB_PASSWORD`） | 无 |
| **PG 18 EXPLAIN 增强** | `ANALYZE` 默认带 BUFFERS、`Index Searches`、小数行数、`MEMORY`/`WAL` 选项 | ✅ 诊断能力直接可用 | 【实测】PG18 `explain (analyze, memory, wal)` 可用；PG16 报 `unrecognized EXPLAIN option "memory"` | 运维/慢查询诊断；**无代码改动** |
| **Prometheus 3 UTF-8 名** | 指标/标签名不再限定 `[a-zA-Z_:]` | ⚠ 能力有、当前不需要，且方向是**放松**（v2 会整批拒收） | 【实测】伪造 exporter 暴露 `ehome.采集.温度`：v3.15.1 target=`up` 并入库；v2.53.0 target=**down**（`invalid metric name or label names`） | `pkg/metrics/metrics.go` 全 ASCII ⇒ 无变化；注意"以后写错也不报错"的风险面 |
| **Prometheus 3 OTLP 原生接收** | `--web.enable-otlp-receiver` | ⚠ 可用但**当前无生产者** | 【实测】v3.15.1 起该 flag 后 `POST /api/v1/otlp/v1/metrics` 返回 200 且可查到样本 | `backend/go.mod` 无 otel 依赖 ⇒ 接入需引入 otel-go，**不建议**（与 promhttp 双管线） |
| **Prometheus 3 PromQL 变更** | `holt_winters` 改名、range 左开右闭、正则匹配换行 | ✅ **12 条告警规则零风险（已实测）** | 【实测】`promtool check rules` → 12 rules SUCCESS；用 v3.15.1 真实加载并抓活体 `/metrics`，4 组规则 `health=ok`、0 条 lastError | `deploy/monitoring/alert_rules.yml` 只用 `rate/increase/count/sum/and/>` |
| Prometheus 3 `auto-gomemlimit` 变默认 | GOMEMLIMIT 跟随容器内存限制 | ⚠ **本仓无 memory 限制 ⇒ 读到系统内存 ×0.9，等于没约束** | 【实测】`docker-compose.yml` 全文无 `mem_limit`/`memory:` | 要让该特性生效必须先加 `mem_limit`；否则"内存曲线变化"担忧不成立 |
| **Alertmanager 0.34** | slog 迁移、可模板化 route labels、`toDate`、eventrecorder | ⚠ 配置语法零风险，但**当前告警邮件根本发不出去**（既有缺陷） | 【实测】`amtool check-config` SUCCESS；`/api/v2/status` 里配置仍是字面量 `to: ${ALERT_EMAIL}` | `deploy/monitoring/alertmanager.yml:31-35`：**AM 不做环境变量展开**，compose 也未注入 |
| **Alpine 3.24** | musl 1.2.6、apk-tools 3.x | ✅ 对 `CGO_ENABLED=0` 静态二进制**零影响（实测）** | 【实测】静态 Go 探针在 3.21/3.24：证书池均 **118**、`Asia/Shanghai` 均成功、bundle 均 **178,791 B** | `Dockerfile:51` 的 `apk --no-cache add ca-certificates tzdata` 在 3.24 上原样可用 |
| **Node 24** | corepack 仍随镜像发布（26 已移除）、`--experimental-strip-types` | ✅ 停在 24 的决定**已实测证实** | 【实测】`node:24-alpine`=v24.22.1、corepack 0.36.0、`corepack prepare pnpm@latest` → **12.15.0**；`node:26-alpine`=v26.12.1 且 **`corepack: not found`** | `Dockerfile:36` 在 26 上必然失败 ⇒ **不可升 26** |
| **Actions v7** | node24 运行时、upload-artifact `archive: false` 直传 | ✅ 已全 `node24`；`archive:false` 对 1 处单文件上传可用 | 【实测】9 个 action 的 `action.yml` 均 `runs: using: node24` | `test.yml:88`（单文件 `backend/coverage.out`）可设 `archive: false` |
| **ESP-IDF 6.1** | DMA 预留池降优先级、`min_free` 语义修正、`idf.py flash` 快速重刷 | ✅ 3 个真实落点（1 已自动生效 / 1 是行为变更风险 / 1 是候选） | 【实测】git `v6.0..v6.1` 提交 + 本地 IDF 源码 | 见 §7.2 / §7.3 |

---

## 1. PostgreSQL 18（`docker-compose.yml:4` = `postgres:18.6-alpine`）

### 1.1 环境与口径

| 项 | 值 | 来源 |
|---|---|---|
| 生产/开发镜像 | `postgres:18.6-alpine` | `docker-compose.yml:4`、`docker-compose.example.yml:8` |
| 本机实际运行版本 | **PostgreSQL 18.4**（`x86_64-pc-linux-musl`） | 【实测】`select version()` on 运行中的 `ehome-postgres` |
| 对照基线 | `postgres:16.14-alpine`（同 DDL、同数据、同 host） | 【实测】本机无 17 镜像；skip scan 是 **18 新增**，16 与 17 在此特性上行为一致，故 16 vs 18 的差异**可以**归因于 18 |
| 数据规模（dev 库实测） | `unified_data` 179,532 行 / `device_data` 568,617 行；分区 `unified_data_202609/202610/202611/202612/202701` | 【实测】`pg_class relkind='p'` = **2**（`unified_data`、`device_data`） |
| 生产规模声明 | `unified_data` 约 **4.4 GB / 2400 万行** | 仓库注释 `internal/datalifecycle/indexes.go:83` |

### 1.2 【重点】skip scan 能否解决"复合索引前导列未指定"——**能，且本仓库正好踩中**

#### 1.2.1 索引与查询的错配（本报告最重要的落点）

复合索引定义（**PG 侧带覆盖列**）：

```
internal/datalifecycle/indexes.go:57-59
  {IndexUnifiedLogicalSensorTS, "unified_data",
      "(logical_device_id, sensor_name, timestamp)",
      `(logical_device_id, sensor_name, "timestamp") INCLUDE ("value")`},
```

即生产索引列序 = **`(logical_device_id, sensor_name, timestamp) INCLUDE (value)`** —— 前导列是 `logical_device_id`。

而有一条查询**只按 `sensor_name` + 时间过滤、完全不提 `logical_device_id`**：

```
internal/api/handler_sensor_fields.go:364-367
  q := db.Model(&models.UnifiedData{}).
      Where("device_id = ? AND timestamp >= ? AND sensor_name IN ?", req.EdgeDeviceID, since, matched).
      Order("timestamp ASC")
```

⇒ 前导列 `logical_device_id` **没有任何条件**，过滤用的是 `device_id`（该表上 `device_id` 是独立单列索引）。**这正是 PG18 skip scan 的目标场景。**

> **对 Lead 提示的一处修正（按实测口径，不套用假设）**：`handler_data.go:88` 与 `:210` 那两处的 `query`/`q` 上游已经带了 `dataScopeCond(qs)`（`handler_data.go:80` / `:204`），而 `dataScopeCond` 会产出 `logical_device_id IN (...)`（见 `scope_union.go:1-27` 的长注释），**前导列其实是被约束的** ⇒ 那两处不是 skip scan 受益点。真正裸奔的是 `handler_sensor_fields.go:364`（`device_id` 打头）。

#### 1.2.2 实测一：纯 skip scan 机制（受控最小复现）

同一条 `create table sk(a int, b timestamptz)` + `create index sk_ab on sk(a, b desc)` + 50 万行，两容器各建一份，`set enable_seqscan=off` 后查 `where b > now()-1h`（**完全不提 a**）：

| 版本 | 计划 | `Index Searches` | 共享块读 | 执行时间 |
|---|---|---|---|---|
| PG **16.14** | Bitmap Index Scan on `sk_ab` | 无此字段 | **1,918** | **15.997 ms** |
| PG **18.4** | Bitmap Index Scan on `sk_ab` | **11** | **45** | **0.898 ms** |

⇒ 【实测】**17.8× 更快，块读少 42×**。机制写在 PG18 自己的输出里：`Index Searches: 11`（`a` 有 10 个不同值 ⇒ 10~11 次索引搜索，每次只搜一个 `a` 值的时间范围）。
【官方文档】PG18 release notes：「Allow skip scans of btree indexes … when there are no restrictions on the first or early indexed columns」（https://www.postgresql.org/docs/18/release-18.html §E.6.3.1.2）。

#### 1.2.3 实测二：按**本仓库真实索引列序**建 300 万行表做 A/B

DDL 与 `partition_mgr.go:410-421` 的 `unified_data` 母表一致（`id bigserial`、`device_id`、`sensor_name varchar(32)`、`value`、`unit`、`ts`、`logical_device_id`、`PRIMARY KEY (id, ts)`），索引含 `(logical_device_id, sensor_name, ts) INCLUDE (value)`，两容器各 300 万行 + `analyze`：

| 查询（= 仓库真实形态） | PG16.14 | PG18.4 | 差异 |
|---|---|---|---|
| `select avg(value) from ud where sensor_name='pv_power' and ts > now()-'1 day'` | BitmapAnd(`ud_sensor_name` + `ud_ts`)，**read=629**，堆扫 actual **18.565 ms** | **Index Only Scan on `ud_logical_sensor_ts`**，`Index Searches: 4`，**read=109**，**执行 1.735 ms** | 计划形态改变；**块读 -83%**，耗时约 **-90%** |
| `select * from ud where device_id=1 and ts >= now()-'1 day' and sensor_name in ('pv_power','battery_soc') order by ts limit 5001`（≈ `handler_sensor_fields.go:364-367` 原样） | Execution **27.098 ms**，BitmapAnd 两索引 | Execution **12.994 ms**，单索引 `Index Searches: 6` | **-52%** |
| `select avg(value) … sensor_name='battery_soc' and ts > now()-'4 hours'` | 两次 Bitmap Index Scan（`ud_ts` 0.540 ms + `ud_sensor_name` **8.935 ms**） | 单次 on `ud_logical_sensor_ts` **0.333 ms** | 索引访问时间 **-96%** |
| `select count(*) … where ts >= date_trunc('day', now())` | Bitmap Heap Scan（`ud_ts`） | 同左 | **无变化**（skip scan 与此无关，如实标注） |

**结论**：PG18 让 `idx_unified_logical_sensor_ts` 在"只给 `sensor_name` + 时间"的查询上**从不可用变为可用**。这是本仓库**唯一**能直接吃到 PG18 红利的点。

#### 1.2.4 三个必须先说清的前提（否则收益为 0 或为负）

1. **该索引在生产/开发库里目前不存在。** 【实测】在运行中的 `ehome-postgres` 上查 `pg_class`：`idx_unified_logical_sensor_ts`、`idx_devdata_logical_created`、`idx_unified_legacy_null_ts` 命中数 = **0**；实际存在的只有 `idx_unified_logical_ts (logical_device_id, "timestamp" DESC)` 与 `idx_devdata_logical_ts`。
   ⇒ 建索引入口是 `ehomectl datalifecycle ensure-indexes`（`indexes.go:90`），且 `indexes.go:82-88` 明确写：分区母表**禁止 `CONCURRENTLY`**，4.4 GB/2400 万行上首次建索引持 `SHARE` 锁、**期间写入阻塞数十秒~数分钟**，必须走维护窗口。
2. **skip scan 的代价与被跳过列的不同值数成正比。** 【实测】10 个不同 `a` → `Index Searches: 11`；dev 库 `count(distinct logical_device_id)=2` → `Index Searches: 4`。若生产逻辑设备数（merge 后目标数）上百，单次查询的索引搜索次数同步上升。
   ⇒ **建议**：在 4.4 GB 生产快照上跑 `EXPLAIN (ANALYZE, BUFFERS)`，看 `Index Searches` 是否 ≈ `distinct(logical_device_id)`，并与现有 BitmapAnd 双索引路径比耗时。**低基数（≤几十）大概率净赢，高基数需实测确认**——本报告不替生产拍板。
3. **分区继承**：索引建成后由 PG 自动传播到新分区（`indexes.go:88-89` 注释），不需重跑全量。

### 1.3 AIO 对"高并发采集写入"的影响——**实测：没有可测收益**

#### 1.3.1 机制与开关

【官方文档】PG18 新增 `io_method`（**只能启动时设置**）、`io_combine_limit`、`io_max_combine_limit`、`io_max_concurrency`、`io_workers`，新增视图 `pg_aios`（release-18 §E.6.3.1.3）。默认 `io_method=worker`。

【实测】运行中的 `ehome-postgres`（18.4）：
```
io_method = worker        io_workers = 3
io_combine_limit = 16     io_max_combine_limit = 16     io_max_concurrency = 64
effective_io_concurrency / maintenance_io_concurrency 默认已提到 16
```
⇒ **升级后无需任何配置即启用**（compose 未传任何 `io_*` 参数，`docker-compose.yml:59`）。

#### 1.3.2 读路径（冷缓存 636 MB 表，3 轮 × 3 查询）

每次查询前用 privileged 容器 `echo 3 > /proc/sys/vm/drop_caches` 清页缓存；三容器同参数（`shared_buffers=128MB, work_mem=16MB, max_parallel_workers_per_gather=0`），唯一差异是版本 / `io_method`：

| 查询 | PG16.14（无 AIO） | PG18 `io_method=sync` | PG18 `worker`（默认） | PG18 `io_uring` |
|---|---|---|---|---|
| seq scan + `payload like` | 666 / 662 / 660 | 1284 / 774 / 686 | **488 / 486 / 482** | 581 / 577 / 577 |
| `count(*) where ts > now()-2h` | 480 / 490 / 475 | 718 / 588 / 496 | **383 / 376 / 349** | 395 / 389 / 387 |
| `avg(val) where sensor=…` | 493 / 498 / 495 | 692 / 579 / 435 | **373 / 371 / 379** | 380 / 385 / 382 |

（单位 ms；每格 3 轮实测）

| 结论 | 数值 |
|---|---|
| **AIO 净收益**（同版本 18：worker vs sync） | 稳定轮次约 **-15%~-30%**（首轮 sync 有建池冷启动尖峰，不计入） |
| **PG16→PG18 总收益**（worker vs 16） | 约 **-25%~-27%**（q0 662→482、q1 481→376、q2 495→374） |
| **io_uring vs worker** | 本机 io_uring **略慢 3%~20%**（未做深度调参，不据此否定 io_uring） |

⚠ **`io_uring` 在 Docker 默认安全配置下起不来**：【实测】`postgres:18-alpine -c io_method=io_uring` 在默认容器里 **FATAL: could not setup io_uring queue: Operation not permitted**（`/proc/sys/kernel/io_uring_disabled = 0`，内核允许，是 **Docker 默认 seccomp** 拦的）；加 `--security-opt seccomp=unconfined` 才能启动。
⇒ 本仓**不要**开 io_uring：既不更快，又要放松容器安全边界。

#### 1.3.3 写路径（"高并发采集写入"）

模拟本仓写形状：分区母表 + 月分区 + 5 个索引（对齐 `partition_mgr.go:436-453` 的 `partitionParentIndexes`），`pgbench` 单条 INSERT，8 客户端 × 2000 事务：

| 主机 | tps | 平均延迟 | 失败 |
|---|---|---|---|
| PG16.14 | 1,983.6 | 4.033 ms | 0 |
| PG18 `io_method=worker` | **1,960.2** | **4.081 ms** | 0 |
| PG18 `io_method=io_uring` | 1,999.1 | 4.002 ms | 0 |

⇒ 【实测】**差异 < 2%，在噪声内**。原因：AIO 服务的是 sequential/bitmap heap scan、VACUUM 这类**批量读**；采集写入是短事务随机写 + WAL，走不到 AIO 路径。
⇒ **如实结论**：AIO 对本仓"高并发采集写入"**没有可测的正收益**，也不拖慢。真正的收益在历史查询/图表聚合（§1.2 读路径）与将来大表 VACUUM。

### 1.4 其余 PG18 能力逐条判定

| 能力 | 验证方式 | 判定 | 理由 |
|---|---|---|---|
| **`uuidv7()`** | 【实测】`select uuidv7()` → `01a1683c-6f64-7b7d-…`；`uuid_extract_timestamp()` 返回当前时刻 | **不适用** | 主键全为 `BIGSERIAL`（`models.go:301` `UnifiedData.ID uint`、`models.go:315` 复合主键 `(id,timestamp)`）。实测对照（50 万行 uuid 主键）：uuidv7 插入 **1230 ms** vs `gen_random_uuid()` **1539 ms**（-20%），pkey 索引 **15 MB vs 19 MB**（-21%），堆同为 25 MB —— 收益真实，但本仓没有 uuid 主键可换 |
| **虚拟生成列** | 【实测】PG18 `create table gc(a int, b int generated always as (a*2) virtual)` 成功、`EXPLAIN` 显示 `Output: a, (a * 2)`；PG16 报 `syntax error at or near "virtual"` | **不适用** | 全仓无 `GENERATED` 列 |
| **OAuth 2.0 认证** | 【官方文档】release-18 §E.6.3.1.1 | **不适用** | 连接只走密码（`docker-compose.yml:11-12`），无 IdP |
| **`EXPLAIN` 增强** | 【实测】见 §1.5 | **✅ 可用（诊断侧，零代码改动）** | — |
| **initdb 默认开 data checksums** | 【官方文档】§E.6.2 | ⚠ 只影响**新建**集群 | 本仓用命名卷 `ehome-pgdata`（`docker-compose.yml:16-17`）；已存在的卷不受影响；`pg_upgrade` 要求 checksum 设置一致 |
| **`VACUUM`/`ANALYZE` 默认处理继承子表** | 【官方文档】§E.6.2 | ⚠ 行为变更（对分区表**更好**） | 18 起默认含子表，可用 `ONLY` 恢复旧行为。本仓用分区表 ⇒ 净收益（autovacuum 不再漏掉分区） |
| **MD5 密码弃用** | 【官方文档】§E.6.2 | ❌ 不适用 | 本仓走默认/密码认证；弃用只在设置 MD5 时告警 |
| **`RETURNING OLD/NEW`** | 【官方文档】§E.6.1 | ❌ 不适用 | 无 `RETURNING` 使用点 |

### 1.5 `EXPLAIN` 增强的具体落点（**诊断能力，不改代码**）

【实测】同 session 对比：

| 特性 | PG16 | PG18 | 对本仓价值 |
|---|---|---|---|
| `ANALYZE` 自动带 `Buffers:` | 需显式 `(ANALYZE, BUFFERS)` | **默认就有** | 运维/评审抄 EXPLAIN 时不再漏缓存证据 |
| `Index Searches` | 无此字段 | **有** | **验证 skip scan 是否真的生效的唯一直接证据**；`docs/分析/BMS页慢修复-实施契约-2026-10-20.md` 一类"计划形态断言"应改为断言该字段 |
| `MEMORY` / `WAL` 选项 | `ERROR: unrecognized EXPLAIN option "memory"` | 可用（`Memory: used=15kB allocated=16kB`） | 定位排序/哈希溢出、WAL 放大 |
| 小数行数与 `Planning:` 段 | 整数行数、无规划期 buffer | 有 | 判断规划期自身开销（分区多时可达毫秒级，【实测】7 分区 Append 的 `Planning Time: 9.243 ms`） |

落点（**文档/脚本层**，无 Go 代码改动）：`scripts/` 里固化 EXPLAIN 输出的对比脚本应断言 `Index Searches` 而不是只看 `Index Scan`/`Bitmap` 名字；生产升级验收应在 PG18 上重跑历史查询，更新"旧索引缺失"的结论。

---

## 2. Prometheus 3.x（`docker-compose.yml:59` = `prom/prometheus:v3.15.1`）

### 2.1 先给结论：**现有 12 条规则与抓取配置在 v3 上全部健康**

【实测】三条独立证据：

```
$ docker run --rm -v deploy/monitoring:/cfg:ro --entrypoint promtool prom/prometheus:v3.15.1 check config /cfg/prometheus.yml
  SUCCESS: 1 rule files found
  SUCCESS: /cfg/prometheus.yml is valid prometheus config file syntax

$ … promtool check rules /cfg/alert_rules.yml
  SUCCESS: 12 rules found

$ … amtool (v0.34.1) check-config alertmanager.yml
  SUCCESS / 2 receivers / 0 templates
```

并且**用 v3.15.1 真实起来、抓活体后端**（`/metrics` → `172.17.0.1:8080`，即本机运行中的 `ehome-web`），加载仓库那份 `alert_rules.yml`：

```
targets: ehome-server  health=up  err=(none)
rules:  ehome-data-ingestion 8 条 health=ok
        ehome-data-persistence 2 条 health=ok
        ehome-device-health 1 条 health=ok
        ehome-mqtt 1 条 health=ok        ← 共 12 条，0 条 lastError
```

⇒ **12 条告警规则在 v3 上无语法/语义风险**（`deploy/monitoring/alert_rules.yml:21/37/55/70/84/98/112/126/139/153/172/192`）。规则只用 `rate()`、`increase()`、`sum by`、`count()`、`and`、`>`、`for:`，全部属于 v3 无变更的核心 PromQL。

### 2.2 抓取协议收紧（v3 的**真 breaking change**）——本仓**恰好安全**

【官方文档】v3 对 `Content-Type` 变严格：v2 在缺失/无法解析时**静默**回落 text 协议，v3 **直接 fail 该次抓取**（https://prometheus.io/docs/prometheus/latest/migration/ §Scrape protocols）。

【实测】本仓 `/metrics` 由 `promhttp.Handler()` 提供（`routes.go:114`），响应头合规：

```
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4; charset=utf-8; escaping=underscores
```

且 v2.53.0 与 v3.15.1 分别抓同一活体端点，**两者 health 均 = up**（`count(ehome_nodes_online)` 均返回 1）。⇒ 无需 `fallback_scrape_protocol`。

⚠ 另一个易忽略的保护：`backend/cmd/server/main.go:638-642` 把 `/metrics` 与 `/api/v1/metrics/prometheus` 从 gzip 中间件排除，注释写明"否则 gzip 解码器读不了、破坏真实 Prometheus 抓取"。**不要**因为升级而删。

### 2.3 UTF-8 指标名：能力存在，但本仓**不需要**，且方向是"放松"

【官方文档】v3 支持指标名/标签名 UTF-8；可用 `global.metric_name_validation_scheme: legacy` 或 per-scrape 保留旧校验。

【实测】伪造 exporter 暴露三种名字（`ehome_legacy_total` 合法、`ehome.采集.温度`、`ehome.utf8.metric`），让两版本抓同一目标：

| 版本 | target health | 入库序列 |
|---|---|---|
| v2.53.0 | **down** —— `invalid metric name or label names: {__name__="ehome.采集.温度", …}` | 只有 `ehome_legacy_total` + `up`/`scrape_*` |
| v3.15.1 | **up** | **`ehome.采集.温度`、`ehome.utf8.metric` 均入库** |

⇒ 两面都写清：
- **收益**：`pkg/metrics/metrics.go` 全 ASCII（`ehome_*`），**当前零变化**；将来指标名带点/中文不会整批抓取失败。
- **风险**：v2 会**整批拒收**（错误可见），v3 会**静默接受**（名字变了、看板/告警对不上也不报错）。若想保住"非法名立刻爆红"，应在 `prometheus.yml` 显式加 `global: metric_name_validation_scheme: legacy`（当前**没有**，见 `deploy/monitoring/prometheus.yml:15-17`）。
- **建议：不加**。本仓指标名由 `pkg/metrics/metrics.go` 的 `promauto` 常量写死且有测试守护，不需要 Prometheus 兜底。

### 2.4 OTLP 原生接收：能用，但**本仓没有生产者**

【实测】`v3.15.1 --help` 确认 flag 存在：

```
--[no-]web.enable-otlp-receiver   Enable API endpoint accepting OTLP write requests.
--enable-feature  … otlp-deltatocumulative, otlp-native-delta-ingestion …
```

启动带该 flag 的容器后 `POST /api/v1/otlp/v1/metrics`（`Content-Type: application/x-protobuf`，手工编码的 `ExportMetricsServiceRequest`）→ **HTTP 200**，随后 `/api/v1/query` 可查到样本。

顺带实测到 **OTLP → Prometheus 的名字改写规则**（对将来接 OTel 有直接价值）：

| OTLP 录入 | Prometheus 里看到 | 规则 |
|---|---|---|
| Sum `ehome_otlp_probe_counter_test` | `ehome_otlp_probe_counter_test_total` | Counter 自动补 `_total` |
| Sum `ehome_otlp_probe_counter_total` | `ehome_otlp_probe_counter_total` | 已有 `_total` 不重复补 |
| Gauge `ehome_otlp_probe_gauge_total` | `ehome_otlp_probe_gauge_total_ratio` | ⚠ **非 Counter 却以 `_total` 结尾 → 追加 `_ratio`** |
| Gauge `ehome.otlp_probe_gauge_plain`（含点） | `ehome_otlp_probe_gauge_plain_ratio` | 点 → 下划线，并追加 `_ratio` |

⇒ **落点判定：不适用（当前无 OTLP 生产者）**。证据：`backend/go.mod:13-14` 只有 `prometheus/client_golang` 与 `client_model`，**无任何 otel 依赖**；`/metrics` 走 `promhttp` 文本协议。为"能收 OTLP"引入 `go.opentelemetry.io/otel` 会变成双管线，且要吃上表改名规则 —— **不建议**。本仓是单体 Go 服务，OTLP 收益为 0。

### 2.5 PromQL 变更逐条核对（对 12 条规则的实际影响）

| v3 变更（官方 migration guide） | 本仓是否踩到 | 实测/证据 |
|---|---|---|
| `holt_winters` → `double_exponential_smoothing`，且需 `--enable-feature=promql-experimental-functions` | ❌ 未使用 | 【实测】v3.15.1 上 `holt_winters(...)` → `parse error: unknown function with name "holt_winters"`；`double_exponential_smoothing(...)` → `function … is not enabled`。12 条规则 0 处使用 |
| 正则 `.` 现在匹配换行 | ❌ 未使用带 `.` 的查询 matcher | 规则里只有 `alertname =~ "Ehome.*"`（`alertmanager.yml:20`）与 `severity =~ "critical"`（`:26`）；对单行标识符**无行为差异** |
| range selector 左边界改开区间 | ⚠ 语义微变，**不影响可用性** | `rate(x[1m])` 在 `scrape_interval=15s`（`prometheus.yml:16`）下样本数 5→4；`rate` 需 ≥2 样本 ⇒ 仍可算。**不会**出现 v3 文档里 `foo[1m:1m]` 只剩 1 点导致 No Data（本仓无子查询） |
| `le`/`quantile` 标签值归一化（`le="1"`→`le="1.0"`） | ❌ 无直方图标签硬编码 | 12 条规则无 `le=`/`quantile=` 字面量；`PingRTT` 是 Histogram 但无规则引用其 bucket |
| 不再支持 Alertmanager v1 API | ❌ 未用 | `prometheus.yml:22-26` 未写 `api_version` ⇒ 默认 v2 |
| `no-default-scrape-port` 变默认（不再补 :80/:443） | ❌ 不受影响 | `prometheus.yml:32-33` 目标是 `ehome-web:8080`，**显式带端口** |
| `scrape_classic_histograms` → `always_scrape_classic_histograms` | ❌ 未使用 | 配置无 native histogram 项 |
| 日志换 `log/slog`（`ts=`/`caller=` → `time=`/`source=`） | ⚠ 仅影响看日志的人/脚本 | 【实测】v3 日志形如 `time=… level=INFO source=main.go:1523 msg="TSDB started"`；v2.53 形如 `ts=… caller=main.go:1402 level=info`。**本报告未在 `deploy/`、`tools/` 发现解析 Prometheus 日志的脚本** ⇒ 标为"未发现落点" |
| **`auto-gomemlimit` 变默认** | ⚠ 本仓**实际不生效** | 【实测】`docker-compose.yml` 全文**无 `mem_limit`/`memory:`** ⇒ 读不到容器限制，回落"系统内存 ×0.9"（本机 30 GB ⇒ GOMEMLIMIT ≈ 27 GB，等于没有约束）。所以"内存曲线变化"担忧在本仓**不成立**，除非同时加 `mem_limit`；若加了限制，需用 `--no-auto-gomemlimit` 或显式 `--auto-gomemlimit.ratio` 控制 |

### 2.6 运维风险：TSDB 不可向下降级

【官方文档】v3 的 TSDB 格式只能被 **v2.55+** 读取。本仓现状是 **v2.53.0 → v3.15.1**。
⇒ 一旦 v3 向 `ehome-prometheus-data` 卷写入，**回滚到 v2.53 会丢数据**。
⇒ 落地建议（**不改代码**）：升级前备份/重命名卷；回滚预案停在 v2.55+，不是 v2.53。

---

## 3. Alertmanager v0.34.1（`docker-compose.yml:74`）

### 3.1 配置零风险（实测）

1. 【实测】`amtool check-config deploy/monitoring/alertmanager.yml`（v0.34.1）→ `SUCCESS`，识别 `global config / route / 0 inhibit rules / 2 receivers / 0 templates`。
2. 【实测】用仓库配置在 **v0.34.1** 与 **v0.27.0** 各起容器 → **两者都正常加载并 running**（`route/group_by/group_wait/repeat_interval/matchers/email_configs` 这套语法跨 7 个 minor 稳定）。
3. 【实测】日志已变 slog（`time=… level=INFO source=app.go:196 msg="Build context"`）。

### 3.2 【重要发现】告警邮件**当前发不出去** —— 与升级无关的既有缺陷

`deploy/monitoring/alertmanager.yml:29-36`：

```yaml
  - name: email-alerts
    email_configs:
      - to: ${ALERT_EMAIL}
        from: ${ALERT_EMAIL_FROM}
        smarthost: ${SMTP_HOST}:${SMTP_PORT}
        auth_username: ${SMTP_USER}
        auth_password: ${SMTP_PASSWORD}
```

**Alertmanager 不对配置文件做环境变量展开**（只有 Prometheus 的 `external_labels` 支持环境变量引用）。【实测】用 v0.34.1 加载该文件后查 `/api/v2/status`：

```
config.original 里仍是:  '    to: ${ALERT_EMAIL}'   '    smarthost: ${SMTP_HOST}:${SMTP_PORT}'
```

而 `docker-compose.yml:73-84` 的 `alertmanager` 服务**没有 `environment:` 段** ⇒ 连变量都没注入。
⇒ `Ehome*` 系列告警会路由到 `email-alerts`，但收件人是字面量 ⇒ **投递必然失败/被 SMTP 拒**，只在 AM 日志里报，界面看起来一切正常。
⇒ 落地建议（非本次升级范围，**优先级高于版本升级**）：容器入口用 `envsubst` 生成实际配置再启动（`amtool` 不做这件事），或改用 `--web.config.file` + 外部模板。
**这是"静默失效"型缺陷**，与本报告其他条目同属可观测性债。

### 3.3 0.27 → 0.34 的能力增量

来源：官方 `CHANGELOG.md`（https://github.com/prometheus/alertmanager/blob/main/CHANGELOG.md，本机 curl 实测可达）。

| 版本 | 变更 | 本仓 |
|---|---|---|
| **0.28.0** | `log/slog` 取代 `go-kit/log`（日志格式变） | 仅影响看日志的人 |
| 0.28.0 | **Templating 错误行为变更**：SNS 集成模板错误改为返回错误 | ❌ 本仓用 email，不受影响 |
| 0.28.0 | 静默数量/大小上限（`--silences.max-silences` 等） | 可选 |
| 0.30.0 | dispatch `start delay`、inhibit 规则命名、模板 `tojson`/`url` 函数、API HTTP metrics | 可选 |
| 0.33.0 | **`--enable-feature=auto-gomaxprocs` 被移除**（0.29 起无效） | ❌ `docker-compose.yml:82-83` 只有 `--config.file` |
| 0.33.0 | eventrecorder（file/webhook/kafka）、`now` 模板函数 | 可选 |
| **0.34.0** | **`alertmanager_notifications_failed_total` 的 `reason` 拆出 `authError`/`rateLimited`**（匹配 `reason="clientError"` 的看板/告警必须改） | ❌ **12 条规则无一条引用 `alertmanager_*`**（全是 `ehome_*`）⇒ 不踩 |
| 0.34.0 | **route 支持可模板化 labels**、`toDate`/`mustToDate` 模板函数、silences 服务端状态过滤 | 可选新能力 |
| 0.34.1 | inhibit 相关三个 bugfix（告警被错误解除静音） | 净收益（本仓 0 条 inhibit，影响有限） |

⚠ **未核实**：0.30.0 起的 `phantom_threading`（邮件归线程）与 smtp 的 `auth_secret_file` —— 本仓用明文 `auth_password`，若将来改文件方式需确认 0.34 的字段名。本报告未实测该项。

---

## 4. Alpine 3.24（`Dockerfile:45`）

### 4.1 实测差异总表

| 项 | alpine:3.21 | alpine:3.24 | 影响 |
|---|---|---|---|
| 镜像版本 | 3.21.8 | **3.24.3** | — |
| `apk` 版本 | **apk-tools 2.14.6** | **apk-tools 3.0.8**（大版本） | 见 §4.2 |
| musl | **1.2.5** | **1.2.6** | 见 §4.3 |
| 本地镜像体积（`docker images` 实报） | 21.1 MB（content 6.88 MB） | **13 MB（content 3.94 MB）** | runtime 镜像更小 |
| `ca-certificates`+`tzdata` 安装后 | `/etc/ssl/certs` 540 K / 237 项，`zoneinfo` 1.5 M | **完全相同**（540 K / 237 项，1.5 M） | 无膨胀 |
| `ca-certificates.crt` | 178,791 B | 178,791 B | **完全一致** |

### 4.2 apk-tools 3.x 对本仓的影响：**无**

本仓只用一条 apk 命令，`Dockerfile:51`：

```
RUN apk --no-cache add ca-certificates tzdata && rm -rf /var/cache/apk/*
```

【实测】在 `alpine:3.24` 上原样执行 → 成功，`/etc/ssl/certs` 237 项、`TZ=Asia/Shanghai date` 正常输出 CST。
⇒ **`--no-cache` 语义在 apk-tools 3.x 未变**，`Dockerfile:51` **无需改动**。

### 4.3 musl 1.2.5 → 1.2.6 对 Go 静态二进制 / TLS 的影响：**实测无回归**

本仓后端是 `CGO_ENABLED=0 GOOS=linux` 静态二进制（`Dockerfile:17-18`），**不链接 musl**（`ldd` 报 `Not a valid dynamic program`，【实测】），所以 musl 变更理论上不影响。为不"理论上"，实测了一个 `CGO_ENABLED=0` 的静态 Go 探针（用仓库自己的 `.tools/gotool` 编译）在两个镜像里的行为：

| 检查项 | alpine:3.21 | alpine:3.24 |
|---|---|---|
| 只装 `ca-certificates tzdata` 后 `x509.SystemCertPool()` | **118** 个根证书 | **118** 个 |
| `time.LoadLocation("Asia/Shanghai")`（**装了 tzdata**） | ✅ `2026-10-23T18:36:54+08:00` | ✅ `2026-10-23T18:37:00+08:00` |
| `time.LoadLocation`（**不装 tzdata**） | ❌ `unknown time zone Asia/Shanghai` | ❌ 同左 |
| `ca-certificates.crt` 字节 | 178,791 | 178,791 |

⇒ **TLS 出站与本地时区在 3.21/3.24 上完全等价**（也再次证明 `Dockerfile:51` 装这两个包是必需的、且足够）。
⚠ **未能核实**：真实 HTTPS 出站握手（本机容器网络受限，直连外网超时；代理在容器内不可用），只做到"证书池/时区加载"层。要闭环请在有外网的环境跑一次 OTA 下载或 `curl` 探针。
⚠ **EOL 时间压力**：Alpine 3.21 的 EOL 是 **2026-11-01**（见 [infra-firmware.md](../依赖升级盘点/infra-firmware.md) §B.1 引 endoflife.date），本报告写作时距 EOL 约 **9 天** ⇒ 升级是**时间敏感项**，不是可选优化。

---

## 5. Node 24（`Dockerfile:25` = `node:24-alpine`）

### 5.1 【实测】"必须停在 24"这一次有硬证据了

前一份盘点（[infra-firmware.md](../依赖升级盘点/infra-firmware.md) §D.2）把"Node 26 是否还带 corepack"标为**需 CI 实测确认**。现已实测闭环：

| 镜像 | node | corepack | `corepack prepare pnpm@latest --activate` | npm |
|---|---|---|---|---|
| `node:24-alpine` | **v24.22.1** | ✅ **0.36.0** | ✅ 装出 **pnpm 12.15.0** | 11.19.1 |
| `node:26-alpine` | **v26.12.1** | ❌ **`sh: corepack: not found`** | ❌ 不可用 | 11.20.0 |

⇒ **`Dockerfile:36`（`RUN corepack enable && corepack prepare pnpm@latest --activate`）在 Node 26 上必然构建失败**。停在 24 是**正确决策，且现在有实测依据**，不再是推断。建议把这条证据补进 `.github/workflows/test.yml:34-38` 的注释（该注释目前写"见 docs/… §D.2"）。

### 5.2 Node 24 能力清单与落点

| 能力 | 实测 | 本仓落点 |
|---|---|---|
| **V8 13.6.233.17**（`node -p process.versions.v8`） | ✅ | 只影响运行时性能，Vite 构建不依赖新语法；**无落点** |
| **`--experimental-strip-types`** | ✅ `node --experimental-strip-types -e 'const x: number = 1'` → `ok 1`；`process.features.typescript === 'strip'` | **不适用**：类型检查由 `vue-tsc`（`package.json` 的 `"typecheck": "vue-tsc --noEmit"`）负责，构建由 `vite`。用 Node 直跑 TS 会**绕过** vue-tsc 与 Vite 对 `.vue` 的处理，收益为负 |
| `process.versions.uv` = 1.52.1 | ✅ | 无落点 |
| npm 11.19.1 | ✅ | 本仓用 pnpm，不涉及 |
| **corepack 0.36.0 仍在** | ✅ | 见 §5.1；⚠ `pnpm@latest` **未固定版本**：今天解析到 12.15.0，明天可能 13 ⇒ 构建不可复现 |

### 5.3 建议落点（**配置层，1 行改动**，本报告不改）

1. `frontend-shared/package.json` **没有** `packageManager` 字段，也**没有** `engines`（【实测】`grep -n 'engines' package.json` → 0 命中）⇒ corepack 无法锁 pnpm 版本、CI 也无法在 Node 版本不符时快速失败。
   建议加 `"packageManager": "pnpm@12.15.0"` + `"engines": {"node": ">=24 <25"}`。这样 `corepack enable` 会**按 package.json 精确**激活 pnpm，消除 `Dockerfile:36` 的 `@latest` 漂移。
2. 三处 pnpm 版本口径仍不一致（`Dockerfile:36` 的 `latest`、`test.yml:39` 的 `PNPM_VERSION: "12"`、本机 12.14.0）；`lockfileVersion: '9.0'`（`pnpm-lock.yaml:1`），pnpm 12 写出的仍是 9.0 ⇒ **不含破坏性**，但应统一。

---

## 6. GitHub Actions v7 系列

### 6.1 【实测】9 个 action 全部 `runs: using: node24`

逐个 curl 对应 major tag 的 `action.yml`：

| workflow 行 | action | `runs.using` | 备注 |
|---|---|---|---|
| `test.yml:53/116/163/269/323/356/384`、`docker-publish.yml:35` | `actions/checkout@v7` | **node24** | `main: dist/index.js` + `post` |
| `test.yml:55/118/165/358` | `actions/setup-go@v7` | **node24** | 另有 `post: dist/cache-save/index.js` |
| `test.yml:276` | `actions/setup-node@v7` | **node24** | — |
| `test.yml:88/255/307` | `actions/upload-artifact@v7` | **node24** | — |
| `test.yml:272` | `pnpm/action-setup@v6` | **node24** | — |
| `docker-publish.yml:38` | `docker/login-action@v4` | **node24** | `dist/index.cjs` |
| `docker-publish.yml:46` | `docker/metadata-action@v6` | **node24** | — |
| `docker-publish.yml:63` | `docker/setup-buildx-action@v4` | **node24** | — |
| `docker-publish.yml:74` | `docker/build-push-action@v7` | **node24** | — |

⇒ **共性要求：runner ≥ v2.327.1**（node24 运行时）。本仓全部用 `ubuntu-latest` 托管 runner（`test.yml:48` 等）⇒ 无风险。
⚠ 若将来引入 self-hosted runner 且 < v2.327.1，会直接报 `unsupported runtime` —— 这是**唯一的运维硬门槛**。

### 6.2 `upload-artifact@v7` 的 `archive: false` 直传（新能力，本仓 1 处可用）

【实测】`action.yml` 的 `archive` 输入描述：
> `If false, the artifact will be uploaded as-is without archiving.` / `When archive is false, only a single file can be …` / `the name of the file uploaded will be the artifact name`

本仓三处上传：

| 行 | artifact | path | 单文件？ | `archive: false` 适用性 |
|---|---|---|---|---|
| `test.yml:88-91` | `backend-coverage` | `backend/coverage.out` | ✅ **单文件** | ✅ **可用**：省一次 zip/unzip，下载侧拿到原始文件 |
| `test.yml:255-258` | `simulation-evidence` | `.logs/simulation/` | ❌ 目录 | ❌ 不适用 |
| `test.yml:307-310` | `frontend-coverage` | `frontend-shared/coverage/` | ❌ 目录 | ❌ 不适用 |

⇒ 建议只改 `test.yml:88`；**代价**：该模式下 `name` 被忽略（artifact 名 = 文件名）。本仓无 `download-artifact` 步骤（grep 0），不会破坏消费方。
⇒ 其他新输入（`overwrite`、`include-hidden-files`、`compression-level`，输出 `artifact-id/artifact-url/artifact-digest`）本仓均未使用，**保持默认**。

### 6.3 `metadata-action@v6` 的 tag 语义（Lead 点名项）

【实测】v6 的变更点在「列表类输入里 `#` 的语义」：值里的 `#` 保留，**只有行首 `#` 才是注释**。

本仓 `docker-publish.yml:52-59` 的 `tags:` 块：

```yaml
          tags: |
            type=ref,event=branch
            type=ref,event=tag
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}}
            type=sha,prefix=sha-,format=short
            type=raw,value=latest,enable={{is_default_branch}}
```

7 行**全部是 `type=` 值行、无任何 `#`**；说明注释（`docker-publish.yml:49-51`）位于 `tags: |` **之前**。⇒ **行为不变，无需改**。（如实标注：按"块内是否含 `#`"核对，未实际执行 action；语义依据 v6 CHANGELOG 与 `action.yml`。）

### 6.4 其余 v7 变更与本仓的关系

| action | v7 相关变更 | 本仓 |
|---|---|---|
| `checkout@v7` | `node24`；v7.0.0 收紧 **`pull_request_target`/`workflow_run` 下 fork PR 的 checkout** | 【实测】本仓 workflow **无** `pull_request_target`/`workflow_run`（grep 0）⇒ 不受影响 |
| `setup-node@v7` | 移除 dummy `NODE_AUTH_TOKEN` 导出；新增 `package-manager-cache` | 不发布 npm 包 ⇒ 无影响；`cache: pnpm`（`test.yml:279`）照旧 |
| `setup-go@v7` | `go-version-file`/`cache-dependency-path`（`test.yml:57`）语义不变 | 无影响 |
| `docker/build-push-action@v7` | 移除 `DOCKER_BUILD_NO_SUMMARY` 等环境变量与 legacy export-build | 只用 `context/push/tags/labels/build-args/cache-from/cache-to` ⇒ 全部保留 |
| `docker/setup-buildx-action@v4` | 移除 deprecated inputs/outputs；`endpoint/status/flags` 输出弃用 | 无入参 ⇒ 无影响 |
| `pnpm/action-setup@v6` | **v6 才支持 pnpm 12**（与 `test.yml:39 PNPM_VERSION: "12"` 硬耦合） | 已对齐（`test.yml:272` v6 + `:39` 12）✅ |

⚠ **必须回归验证的一条**：`docker-publish.yml:78-80` 的 `build-args: GIT_COMMIT=${ github.sha }}` —— 其注释（`:64-73`）记录了 2026-10-23 发现的缺口：缺这行时镜像 label 有 commit 但 `/health` 回显 `unknown`。v7 的 `build-push-action` 保留 `build-args`，但**要在 CI 跑一次后核对 `curl /health` 的 commit 字段**，不能只看 action 成功。

---

## 7. ESP-IDF 6.1（`esp32-collector/dependencies.lock` = `6.1.0`）

### 7.1 环境与基线

| 项 | 值 | 来源 |
|---|---|---|
| lock 声明 | `idf: version: 6.1.0` | 【实测】读 `dependencies.lock` |
| 本机 IDF | `git describe` → **`v6.1`**；`esp_idf_version.h` → 6/1/0 | 【实测】`/home/sun/env/esp-idf` |
| 升级提交 | `f156fa6b feat(firmware): 升级到 ESP-IDF 6.1（S3/C6 四 profile 实编 + C6 实机启动验证）`，lock 里 `6.0.1 → 6.1.0` | 【实测】`git log -p --follow dependencies.lock` |
| 构建产物可区分版本 | `.fwbuild/c6-n8` → `IDF_VER="v6.0.2"`；`.fwbuild-prod/c6-n8`、`.fwbuild-prod/s3p-n16`、`.fwbuild_c6_61`、`.fwbuild-6.1-syncfix` → `IDF_VER="v6.1"` | 【实测】解析各 `compile_commands.json` |
| 静态 SRAM（C6 profile，nm 归集 `.bss`） | 6.0.2 构建 **87.8 KiB** → 6.1 构建 **77.8 KiB** | 【实测】`riscv32-esp-elf-nm --print-size` 求和 |

⚠ **口径铁律（必须写明，否则数字会被误读）**：上面那对 87.8→77.8 KiB 来自**两个不同提交**的构建目录（`.fwbuild` 2026-10-04 vs `.fwbuild-prod` 2026-10-18），中间还有业务代码变更 ⇒ **不能把 -10 KiB 归因给 IDF 6.1**，只能作为"升级后实测绝对值"。要做纯 IDF A/B，需在同一 commit 上用两套 IDF 各编一次（本报告未做，如需要应另立项）。

### 7.2 【重点】IDF 6.1 更省 RAM 的选项 —— 3 个真实落点

#### 落点 1（✅ **已自动生效，零改动**）：DMA 预留池降为低优先级

【实测】git `v6.0..v6.1` 提交 `38421b8db76 change(heap): reserve DMA pool with low priority MALLOC_CAP_DEFAULT caps`：

```diff
 caps[] = {0, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL,
-          MALLOC_CAP_8BIT | MALLOC_CAP_32BIT};
+          MALLOC_CAP_DEFAULT | MALLOC_CAP_8BIT | MALLOC_CAP_32BIT};
```
Kconfig 帮助文字同步改为：`a normal malloc() can fall back to the pool only after all other internal are exhausted.`

本仓对应配置：`esp32-collector/sdkconfig.defaults.esp32s3psram:51`
```
CONFIG_SPIRAM_MALLOC_RESERVE_INTERNAL=32768
```

⇒ **语义变化**：升级前那 32 KiB 对普通 `malloc()` **完全不可见**（永久扣掉）；升级后普通分配在**其它内部堆耗尽后可以回落到这个池**，而 `MALLOC_CAP_DMA|INTERNAL` 的保障仍在。
⇒ **对 s3p 的意义**：内部 RAM 紧张时"最后 32 KiB"从**死锁式不可用**变成**可借用的应急额度**。这是 IDF 6.1 给本仓最实在的 RAM 收益，**但要标注为"应急余量"**：真正吃满它意味着 DMA 池被挤压，`largest` 门禁（`main/mem_guard.c:102`）反而可能更难满足 ⇒ **不要因此下调 s3p 的 8 KiB 地板**（`mem_guard.c:76`）。

#### 落点 2（⚠ **行为变更，需记录**）：`min_free` 不再统计"启动后新注册的堆"

【实测】提交 `aaae14008aa fix(heap): Do not consider newly registered heap in minimum free size calcualtion`（在 `v6.1` 内），改 `heap_caps.c`/`heap_caps_init.c`，新增 `esp_heap_caps.h` 条目 + 文档更新。语义：`app_main` 之后调用 `heap_caps_add_region_with_caps()` 新增的堆，**不再拉低** `heap_caps_get_minimum_free_size()`。

本仓使用点：
- `main/main.c:97`、`:100`（启动横幅打印 `minimum_free_size`）
- `main/mem_guard.c:107` `mem_guard_min_ever()` → `heap_caps_get_minimum_free_size(MEM_GUARD_CAPS)`
- `components/msg_handler/handler_data.c:257`（上报 `perf_heap_min`）与 `:260`（`esp_get_minimum_free_heap_size()`）

⇒ 影响判定：
- 本仓**没有**运行期 `heap_caps_add_region` 调用（grep 0）⇒ 该修正对本仓**当前无行为影响**；
- s3p 的 PSRAM 堆在 `app_main` **之前**初始化（`CONFIG_SPIRAM_BOOT_INIT=y`，`sdkconfig.defaults.esp32s3psram:41`）⇒ 仍计入 min，语义不变；
- 但**必须记录**：以后若有"运行期扩容堆"的改动，`min_ever` 会**不再反映**它 ⇒ 门禁指标偏乐观。这是**静默弱化**，值得写进 `mem_guard.c` 注释或门禁说明。

#### 落点 3（✅ 候选，需按 S3 的 ISR 审查流程走）：C6 profile 缺 `HEAP_PLACE_FUNCTION_INTO_FLASH`

【实测】当前配置分布：

| 配置 | S3 profile | C6 profile |
|---|---|---|
| `CONFIG_HEAP_PLACE_FUNCTION_INTO_FLASH` | ✅ `sdkconfig.defaults.esp32s3:116`（=y，带完整 ISR 安全论证注释 `:89-107`） | ❌ **未设置**（`sdkconfig:2522` = `not set`；`sdkconfig.defaults.esp32c6` 全文无此项） |
| `CONFIG_LIBC_LOCKS_PLACE_IN_IRAM` | n（`:114`） | 未设置（默认 y） |
| `CONFIG_SPI_SLAVE_ISR_IN_IRAM` | n（`:113`） | 未设置 |
| `CONFIG_ESP_EVENT_POST_FROM_IRAM_ISR` | n（`:115`） | 未设置 |

⇒ C6 是**另一颗芯片但同一套业务**（`sdkconfig.defaults.esp32c6` 只配 target/USB/UART/flash/分区），S3 上实测 `HEAP_PLACE_FUNCTION_INTO_FLASH=y` 带来 **free +20.9 KiB / largest +20.0 KiB**（`sdkconfig.defaults.esp32s3:60-66` 的实测记录）。
⇒ **建议**：把同一组 4 项（`HEAP_PLACE_FUNCTION_INTO_FLASH=y` + 三个 IRAM 选项 =n）下推到 C6，但**必须**先过 S3 那份 ISR 审查清单（SPI 三处 heap 调用不在 ISR、无 SPI slave、`esp_event_post` 0 处、无 cache-disable ISR 用 libc 锁）。C6 的驱动集与 S3 不同（Kconfig 互斥依赖不同），**不能直接复制，必须逐条复核**。
⚠ 本报告**未实编验证** C6 加这组配置后的 `largest` 改善（需真机/编译 + 内存门禁），因此标为**候选**而非结论。

#### 其他 IDF 6.1 新增的可选 RAM 相关 Kconfig

【实测】`git diff v6.0 v6.1 -- 'components/*/Kconfig'` 新增项里与本仓相关的：

| 新配置 | 默认 | 对本仓 |
|---|---|---|
| `CONFIG_ESP_EVENT_POST_FROM_ISR_SIZE` | 4 | 本仓 `esp_event_post` 0 处（grep 0）⇒ 无落点 |
| `CONFIG_ESP_EVENT_LOOP_IN_EXT_RAM` | n | **s3p 可用**：把 event loop 分配到 PSRAM，省内部 RAM。本仓是否用 event loop 本报告**未核实到使用点** ⇒ 标"未核实" |
| `CONFIG_ESP_HTTP_CLIENT_STRICT_HEADER` | **y（新）** | ⚠ **OTA 风险面**：`components/ota/ota.c:553-557` 用 `esp_http_client` 做 HTTPS OTA。新默认"header 装不下就 fail 请求"可能让 header 较大的服务端响应直接失败 ⇒ 升级后**必须**跑一次真实 OTA 验证（本报告未跑，标**需实测**） |
| `CONFIG_ESP_HTTPS_OTA_VERIFY_SPI_MODE` | y | 本仓 OTA 走 `esp_http_client` 自实现（`ota.c:461` `build_ota_http_config`），不用 `esp_https_ota` ⇒ 无落点 |
| `CONFIG_ESP_SLEEP_SET_FLASH_DPD` | 视 SOC | 本仓无 light sleep ⇒ 无落点 |
| `CONFIG_CORDIC_ONESHOT_CTRL_FUNC_IN_IRAM` | n | 未用 CORDIC ⇒ 无落点 |

### 7.3 【行为变更】`idf.py flash` 默认变成"快速重刷"

【实测】本地 IDF 6.1 文档 `docs/en/migration-guides/release-6.x/6.1/tools.rst`：
> The default behavior of `idf.py flash` has changed. When `*_flashed.bin` files exist in the build directory … now performs **fast reflashing**: it only writes the flash regions that changed … After flashing, it verifies that the device flash content matches the expected binaries; if verification fails, a full flash is performed.

本仓落点：`esp32-collector/flash_and_verify.sh:45`
```bash
idf.py -p "$PORT" flash
```
⇒ **风险**：产线/验证烧录若复用同一 build 目录，6.1 起会**只写变化区域**。虽然它会校验并回退全量，但"产线烧录必须确定性地整片写入"是更稳的口径。
⇒ **建议**：改 `idf.py -p "$PORT" flash -a`（=`--all`，强制全量）。
⇒ 附带正面收益：`idf.py flash` 现在自带 verify；`:52` 那句独立 `esptool.py verify_flash 0x10000 …` 变成**冗余**（可保留作双保险，但别以为它是唯一校验）。

### 7.4 驱动迁移债：**已清，无 6.x 迁移工作**

| 驱动 | IDF 6.x 状态 | 本仓 |
|---|---|---|
| 旧 I2C（`driver/i2c.h`） | 6.0 标 **EOL**，**7.0 移除**（【实测】`6.0/peripherals.rst:203-209`） | ✅ 已用新驱动：`components/bus_dma/bus_dma.c:1853` `i2c_new_master_bus(&bus_cfg, …)` |
| 旧 ADC/DAC/I2S/Timer Group | 6.0 移除 | ✅ 无使用 |
| UART wakeup API | 6.1 弃用 `uart_set_wakeup_threshold`/`uart_get_wakeup_threshold` → `uart_wakeup_setup`（【实测】`6.1/peripherals.rst`） | ✅ 0 处调用（grep 无命中）⇒ **不适用** |
| SPI | 无迁移要求 | ✅ `spi_bus_initialize`/`spi_device_transmit`（`bus_dma.c:1505/1632/1659`） |
| LCD/DSI | 6.1 回调重命名（`on_refresh_done` → `on_frame_buf_complete`） | ✅ 无 LCD ⇒ 不适用 |

### 7.5 picolibc：本次升级**没有**默认值切换（纠正一个易误读点）

【实测】`git show v6.0:components/esp_libc/Kconfig` 与当前（v6.1）**都**是：
```
choice LIBC
    default LIBC_NEWLIB if IDF_TOOLCHAIN_CLANG
    default LIBC_PICOLIBC
```
⇒ picolibc 作为默认 libc 在 **6.0 就已经是默认**（提交 `31810ae` 虽被 `v6.1` 包含，但 Kconfig 语义两侧一致），**不是**本次 6.1 升级带来的变化。
本仓当前实际配置【实测】`sdkconfig:1801-1803`：`CONFIG_LIBC_PICOLIBC=y` + `CONFIG_LIBC_PICOLIBC_NEWLIB_COMPATIBILITY=y`。
⇒ 结论：**无落点**；不要把它写成"6.1 新增"。

---

## 8. 逐条"不适用"清单（集中说明，避免硬凑）

| 能力 | 判定 | 原因（可复核） |
|---|---|---|
| PG18 `uuidv7()` | 不适用 | 主键是 `BIGSERIAL`/`uint`（`models.go:301/315`）；无 uuid 主键 |
| PG18 虚拟生成列 | 不适用 | 全仓无 `GENERATED` 列（grep 0） |
| PG18 OAuth 2.0 | 不适用 | 仅密码认证（`docker-compose.yml:11-12`） |
| PG18 `RETURNING OLD/NEW` | 不适用 | 无 `RETURNING` 使用点 |
| Prometheus UTF-8 指标名 | 不适用（能力保留） | `pkg/metrics/metrics.go` 名称全 ASCII；且方向是放松校验 |
| Prometheus OTLP 原生接收 | 不适用（当前无生产者） | `backend/go.mod` 无 otel 依赖；`/metrics` 是 promhttp 文本 |
| Alertmanager `reason=clientError` 拆分 | 不适用 | 12 条规则无 `alertmanager_*` 指标引用 |
| Alertmanager `auto-gomaxprocs` flag 移除 | 不适用 | `docker-compose.yml:82-83` 只有 `--config.file` |
| Alpine apk-tools 3.x 迁移 | 不适用 | 只有一条 `apk --no-cache add`（`Dockerfile:51`），实测原样可用 |
| Node `--experimental-strip-types` | 不适用 | 类型检查在 `vue-tsc`、构建在 `vite`；用 Node 直跑 TS 需绕过两者 |
| Actions `archive: false` | 仅 1/3 处适用 | `test.yml:88` 单文件；`:255`/`:307` 是目录 |
| ESP-IDF UART wakeup API 迁移 | 不适用 | 0 处调用 |
| ESP-IDF LCD/DSI 回调重命名 | 不适用 | 无 LCD |
| ESP-IDF picolibc "新默认" | 不适用（非本版变化） | v6.0 已是默认 |

---

## 9. 建议执行顺序（按"风险 × 收益"排序；本报告未执行任何一项）

| 优先级 | 动作 | 落点 | 依据 | 成本 |
|---|---|---|---|---|
| **P0（配置）** | 修 Alertmanager 环境变量不展开 | `deploy/monitoring/alertmanager.yml:31-35` + `docker-compose.yml:73-84` | §3.2 —— **当前告警邮件发不出去**（静默失效） | 小 |
| **P0（运维）** | 维护窗口执行 `ehomectl datalifecycle ensure-indexes`，让 `idx_unified_logical_sensor_ts` 真正存在 | `indexes.go:57-59`（定义）、`indexes.go:90`（入口）、`indexes.go:82-88`（SHARE 锁警告） | §1.2.4 —— 索引**当前不存在**，skip scan 收益为 0 | 中（4.4 GB 建索引，写入阻塞数十秒~分钟） |
| **P1（验证）** | 生产快照跑 `EXPLAIN (ANALYZE, BUFFERS)`，同时看 `Index Searches` 与耗时 | `handler_sensor_fields.go:364-367` 对应查询 | §1.2.3 —— dev 级 -52%；生产基数不同需确认 | 小 |
| **P1（验证）** | 真实 OTA 一次（验 `ESP_HTTP_CLIENT_STRICT_HEADER` 新默认） | `components/ota/ota.c:553-557` | §7.2 表 | 小 |
| P2 | `flash_and_verify.sh:45` 改 `flash -a` | `esp32-collector/flash_and_verify.sh:45` | §7.3 —— 6.1 默认快速重刷 | 小 |
| P2 | 给 C6 profile 补 `HEAP_PLACE_FUNCTION_INTO_FLASH` 等 4 项（先过 ISR 审查） | `sdkconfig.defaults.esp32c6` | §7.2 落点 3 —— S3 实测 free/largest 各 +20 KiB | 中（需实编 + 门禁） |
| P2 | `frontend-shared/package.json` 加 `packageManager` + `engines` | `package.json` | §5.3 —— 消除 `Dockerfile:36` 的 `pnpm@latest` 漂移 | 小 |
| P3 | `test.yml:88` 加 `archive: false` | `.github/workflows/test.yml:88` | §6.2 —— 单文件直传 | 小 |
| P3 | 决定是否加 `global.metric_name_validation_scheme: legacy` | `deploy/monitoring/prometheus.yml:15-17` | §2.3 —— 保住"非法指标名立刻爆红" | 极小 |
| P3 | 升级前备份 Prometheus 卷；回滚预案停 v2.55+ | `ehome-prometheus-data` | §2.6 —— v3 TSDB 不可降到 2.55 以下 | 小 |

---

## 10. 复跑方法（所有数字都可复现）

探针目录：`.tmp-deps/infra-probe/`（未纳入 git，临时产物）

| 脚本 | 用途 |
|---|---|
| `prom_cmp.sh` | v2.53.0 与 v3.15.1 分别抓活体 `/metrics`，比对 target health |
| `prom-utf8.yml` + `utf8server.py` | 伪造 exporter 暴露 UTF-8 指标名，复现 v2 拒收 / v3 入库 |
| `otlp.py`/ `otlp2.py`/ `otlp3.py` | 手工编码 OTLP protobuf，验证 `--web.enable-otlp-receiver` 与名字改写规则 |
| `aio_bench.sh`/ `aio_bench2.sh` | 冷缓存读路径：PG16 / PG18-sync / PG18-worker / PG18-io_uring 四组对照 |
| `write_bench.sh` | 分区表 8 客户端 pgbench INSERT（写路径） |
| `uuid_bench.sh` | uuidv7 vs gen_random_uuid 插入耗时与索引体积 |
| `alpine_tls2.sh` + `tlsprobe/` | 静态 Go 二进制在 alpine 3.21/3.24 的证书池与时区行为 |
| `nmdiff.sh` | 固件 ELF 的 `.bss`/`.data` SRAM 归集（按符号 type 过滤，符合 memory 分析铁律 C） |

关键复跑命令（PG skip scan 最小复现，约 1 分钟）：
```bash
docker run -d --name pg18 -e POSTGRES_PASSWORD=pw postgres:18-alpine
docker run -d --name pg16 -e POSTGRES_PASSWORD=pw postgres:16-alpine
# 两库各跑同一段：
#   create table sk(a int, b timestamptz); -- 50 万行
#   create index sk_ab on sk(a, b desc); analyze; set enable_seqscan=off;
#   explain (analyze, buffers, costs off) select count(*) from sk where b > now() - interval '1 hour';
# PG16: 1918 块读 / 16.0 ms      PG18: 45 块读 / 0.90 ms（Index Searches: 11）
```

---

## 11. 未核实项汇总（不做假声明）

| 项 | 状态 | 原因 / 补法 |
|---|---|---|
| PG18 skip scan 在 **4.4 GB / 2400 万行生产库**上的实际耗时 | **未能核实** | 本机只有 dev 库（179,532 行）与自造 300 万行表；生产快照不在本机。补法：维护窗口在生产库上跑 §1.2.3 的两条查询并对比 |
| Alpine 3.24 上**真实 HTTPS 出站**握手 | **未能核实** | 本机容器网络受限（直连外网超时、代理在容器内不可用）；只验证了证书池与 bundle 一致。补法：有外网环境跑 OTA 或 curl |
| C6 加 `HEAP_PLACE_FUNCTION_INTO_FLASH` 后的 `largest` 实测改善 | **未实编验证** | 需编译 + 真机内存门禁；本报告只给"S3 上同类改动 +20 KiB"的既有实测作参照 |
| OTLP 在 `otlp-deltatocumulative` / `otlp-native-delta-ingestion` 下的增量语义 | **未测** | 本报告只验证了 cumulative sum + gauge 的基本接收；delta 语义需专门构造请求 |
| Alertmanager `phantom_threading` / `auth_secret_file` 字段名 | **未核实** | 不值得单独起容器验证；若将来改用文件凭据再查 0.34 配置文档 |
| Prometheus v3 日志格式变更是否影响本仓脚本 | **未发现落点** | `deploy/`、`tools/` 未见解析 Prometheus 日志的脚本（grep 未命中）；若有外部日志管道需自查 |
