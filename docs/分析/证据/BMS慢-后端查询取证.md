# BMS 详情页慢 — 后端查询成本取证

- 设备: edge_devices.id=14（`bms` / jiabaida_bms / 16 串 / 1 Hz / 29 测点），逻辑身份 logical_device_id=14
- 生产: http://192.168.20.6:18090（容器 ehome-prod-web，镜像 ehome-new:ui-fix2）+ ehome-prod-postgres（库 ehome）
- 取证人: backend-query（task-1）；取证时段: 2026-10-20 21:00–21:17（Asia/Shanghai）= 13:00–13:17 UTC
- 证据纪律: 每条结论标【实测】/【推断】；所有 `EXPLAIN (ANALYZE, BUFFERS)`、`SELECT`、`GET` 均为只读；未改任何配置/数据。

## 0. TL;DR（谁在花时间）

【实测】BMS 详情页首屏 5 个 `historical-batch` 请求 + `latest-data`，**串行**合计 **21.06 s**（Lead 基线 20.6 s，本取证复测 21.06 s，一致）。时间几乎全部花在数据库：**一条 24 h 单测点查询就要读 281 万行**（返回 8.6 万行），**一次页面加载 PG 侧共约读 9130 万行**；`max_points=500` 不减少任何 DB 读取（先全量载入再抽样）。31 个 category 查询同时打到 3 vCPU/128MB shared_buffers 的 PG 上时，**并发实测 8 个请求中 6 个 > 10 s**（cells 33.2 s、protection 55.1 s），超过前端 axios 10 s 超时。

| 请求 | 串行实测（本取证） | Lead 基线 | gzip 后体积 | 服务端主要成本 |
|---|---:|---:|---:|---|
| detail `GET /edge-devices/14` | 6.4 ms / 8,395 B | 4.5 ms | 2,094 B | edge_devices 9 行 seq scan（0.025 ms） |
| latest-data | 320 ms / 2,834 B | 306 ms | 617 B | device_data **并行 seq scan 204 万行**、读 1.03 GB 块 |
| cells 16 类 × 24 h | 5.65 s / 1,571,275 B | 5.64 s | 165,226 B | 16 个并发 SQL，各扫 **281 万行** → 合计 **4496 万行访问** |
| metrics 9 类 × 24 h | 3.23 s / 892,112 B | 3.64 s | 约 94 KB | 9 × 281 万 = **2529 万行访问** |
| protection 1 类 × 30 d | 8.99 s / 100,213 B | 9.87 s | 10,314 B | bitmap( sensor_name ) → **heaps 705 万行、读 2.64 GB 块**、JIT 567 ms |
| temp 3 类 × 24 h | 1.63 s / 311,050 B | 1.44 s | 约 31 KB | 3 × 281 万 = **842 万行访问** |
| soc+current 2 类 × 24 h | 1.24 s / 185,497 B | 0.88 s | 约 21 KB | 2 × 281 万 = **562 万行访问** |

**根因链**：分区表 `unified_data`（2026-10 分区 4.42 GB / 约 2284 万行）只有 `(logical_device_id, timestamp DESC)` 与 `sensor_name` 等**单列/双列索引**，没有 `(logical_device_id, sensor_name, timestamp)` 复合索引 ⇒ 带 `sensor_name` 过滤的 24 h 查询被规划成 `timestamp_idx` 全窗口扫描（每查 1 个测点要扫该 24 h 内**全部 29 个测点 + 其它设备**的行），30 d 查询被规划成 `sensor_name` bitmap（因 30 d 窗口≈整个分区，`logical_device_id+timestamp` 不具选择性）。`max_points` 降采样发生在 GORM `Find` 之后，因此完全不削减 DB 与 Go 侧成本。

---

## 1. 请求形态与测量条件

### 1.1 前端实际发出的参数（源码核对）

- 5 个 batch 请求都走 `useHistoryData` → `GET /api/v1/unified-data/historical-batch`，`max_points: 500`（`frontend-shared/src/composables/useHistoryData.ts:81-89`；diag 卡另有 `MAX_POINTS=500` 与并发≤3 信号量，`bms/diag/useDiagHistory.ts:18-14`）。
- category 组成（源码）：
  - cells: `cell_voltage_1..16`（`BmsChartsPanel.vue:158`），时间窗默认 24 h（`composables/useTimeRange.ts:18`）；
  - metrics: `total_voltage,current,rsoc,remaining_capacity,temperature_1,temperature_2,temperature_3,cell_voltage_max,cell_voltage_min` 共 9 类（`BmsChartsPanel.vue:203-213`）；
  - protection: `protection_status` × 30 d（`diag/ProtectionHistoryCard.vue:127` 默认 `rangeDays=30`）；
  - temp: `temperature_1,2,3` × 24 h（`diag/TempTrendCard.vue:154-157`）；
  - soc/current: `rsoc,current` × 24 h（`diag/SocCurrentCard.vue:124`）。
- `client.ts:46` 默认 `timeout: 10000`（10 s）；仅 `edgeDevice.ts:388` 的 latest-data 单独放宽到 20 s。

### 1.2 实际执行的 SQL（pg_stat_activity 抓取，非重构）

【实测】在 cells 16 类请求在途时对 `pg_stat_activity` 每 0.15 s 采样（`.tmp-backend/watch.sql` / `capture_cells.log`），抓到 GORM 实际 SQL 形态（16 条同形查询 + 每查询 2 个并行 worker）：

```sql
SELECT * FROM "unified_data"
WHERE (logical_device_id IN ($1) OR (logical_device_id IS NULL AND device_id IN ($2)))
  AND sensor_name = $3
  AND timestamp BETWEEN $4 AND $5
ORDER BY timestamp ASC
```

- WHERE 由 `handler_data.go:372,391-397`（`dataScopeCond` + `sensor_name` + `timestamp BETWEEN` + `ORDER BY timestamp ASC`）生成；scope 条件来自 `datalifecycle/scope.go:32-53`。
- 本设备 `merged_into` 全为 NULL ⇒ `ResolveDataQueryScope` 的 `DedupNeeded=false`（`query_scope.go:77`），因此**没有** `ApplyShapeDedup` 的 ROW_NUMBER 窗口函数，SQL 就是上面 5 个条件。
- 采样显示 16 条查询在同一毫秒（13:05:30.718-719）同时发出，另有 7-8 个 parallel worker 进程，**一条 HTTP 请求 → 24 个 PG 进程**（16 leader + 8 worker，受 `max_worker_processes=8` 限制）。
- `handler_data.go:390` 每个 category 单独 `db.Session(&gorm.Session{})`，即 16 个 goroutine 各自占一条连接；`database/gorm.go:50` 连接池上限 `MaxOpenConns=25`。5 个 batch 请求共 31 个 goroutine 抢 25 条连接 ⇒ 有 6 个排队（【推断】由连接池配置+pg_stat_activity 连接数推出）。

### 1.3 测量条件（务必按此引用，勿混用）

| 组 | 时刻（本地/UTC） | 条件 | 用途 |
|---|---|---|---|
| Lead 基线 | 20:57–21:05 / 12:57–13:05 | loadavg≈3.0，LAN 直连，串行，未压缩 | 基线耗时/体积 |
| 本取证串行复测 | 21:10:07–21:10:28 / 13:10:07–13:10:28 | loadavg 3.56（起始），LAN 直连，`Accept-Encoding: identity`，串行 | 与基线交叉验证（吻合） |
| EXPLAIN ANALYZE | 21:05:30–21:08 / 13:05:30–13:08 | 单条 SQL 串行；13:04 时 loadavg 0.45；`track_io_timing=off`（只有 BUFFERS 计数，无 I/O 耗时） | DB 侧行数/块数/执行时间 |
| gzip 对照 | 21:11:37 / 13:11:37 | loadavg 2.11，串行 | 压缩比/头 |
| 并发上界实验 | 21:13:17–21:14:16 / 13:13:17–13:14:16 | loadavg 起始 1.45、结束 15.63；一轮页面级并发（8 请求同时） | **仅作并发上界，不作基线** |
| 逐请求 CPU 采样 | 21:15:38–21:16:58 / 13:15:38–13:16:58 | loadavg 10.44（继承并发实验），**受干扰，作废** | 不引用其耗时 |

> 污染声明：并发实验本身把生产 loadavg 推到 15.6（3 vCPU），且同期存在其它取证流量。按 Lead 纪律，并发组数值只能表述为「并发下的上界/量级」，文档中不以它做基线。逐请求 CPU 采样受残余负载影响，**其耗时数据不采用**，只采用其中 cgroup 计数用于并发聚合核算（见 §3.3）。

---

## 2. 数据库实测成本（EXPLAIN ANALYZE, BUFFERS）

### 2.1 数据分布与索引（先看分母）

【实测，13:05 UTC】

- `unified_data` 按 `timestamp` 月分区：`unified_data_202610` = **4,421 MB**，`reltuples=2.284e7`（统计值，偏旧）；`device_data_202610` = **1,295 MB**，`reltuples=2.04e6`。
- `unified_data` 索引（父表 + 分区同构）：`(id,timestamp)` 主键、`(logical_device_id, timestamp DESC)`、`device_id`、`edge_device_id`、`sensor_name`、`timestamp` —— **无 `(logical_device_id, sensor_name, timestamp)`**。
- 设备 14：`unified_data` 最早时间 2026-10-06 04:48 UTC；最近 24 h 每测点 **86,251 行**（29 个测点均相同，1 Hz）；30 d 窗口内 `protection_status` 实际命中 **697,519 行**（≈8.09 天 × 86,251，【推断】该测点是 10-12 后才开始上报）。**注意：Lead 简报里「30 d protection = 2.59M 行」是 30×86,251 的理论值，实际库里只有 69.8 万行，本节约 2.6 倍；成本结论不变，但引用行数请用实测值。**
- PG 参数：`max_parallel_workers_per_gather=2`、`max_parallel_workers=8`、`max_worker_processes=8`、`work_mem=4MB`、`shared_buffers=128MB`、`jit=on`（`jit_above_cost=100000`）、`track_io_timing=off`。

### 2.2 单 category 24 h（cell_voltage_1）—— 每条 24 h 查询都是全窗口扫描

【实测】`EXPLAIN (ANALYZE, BUFFERS)`，时间窗 2026-10-19 13:05:29Z ~ 2026-10-20 13:05:29Z（与线上请求同窗）：

```
Gather Merge  (cost=1000.46..54468.27 rows=41608 width=72) (actual time=0.421..2611.815 rows=86251.00 loops=1)
  Workers Planned: 2
  Workers Launched: 0
  Buffers: shared hit=119677 read=25317 written=24
  ->  Parallel Index Scan using unified_data_202610_timestamp_idx on unified_data_202610
        (cost=0.44..48665.65 rows=17337 width=72) (actual time=0.047..2576.087 rows=86251.00 loops=1)
        Index Cond: (("timestamp" >= '2026-10-19 13:05:29+00') AND ("timestamp" <= '2026-10-20 13:05:29+00'))
        Filter: (((sensor_name)::text = 'cell_voltage_1'::text)
                 AND ((logical_device_id = 14) OR ((logical_device_id IS NULL) AND (device_id = 14))))
        Rows Removed by Filter: 2722700
        Buffers: shared hit=119677 read=25317 written=24
Planning Time: 3.919 ms
Execution Time: 2615.837 ms
```

| 指标 | 值 |
|---|---|
| 返回行数 rows | **86,251** |
| 索引扫描访问行数 | 86,251 + **2,722,700**（Rows Removed by Filter）= **2,808,951 行** |
| shared hit / read | 119,677 / **25,317 块**（≈198 MB；疑为较冷缓存，故 2.6 s 中相当部分是 I/O） |
| Execution Time | **2,615.8 ms**（单条、串行、无并发） |
| 选择性来源 | 索引只按 24 h 时间过滤；`sensor_name` 在 heap 上过滤，**1 行有效数据要顺带读 32.6 行** |

**要点**：24 h 窗口内全表（全设备/全测点）约 281 万行；每查 1 个测点都要把这 281 万行走一遍。cells 有 16 个测点 ⇒ 该请求 PG 侧共 16 × 281 万 = **4496 万行访问**；一天全部 24 h category 查询（cells16+metrics9+temp3+soc2=30 条）合计 **8427 万行访问**。

### 2.3 30 d protection_status —— bitmap lossy 放大 10 倍

【实测】窗口 2026-09-20 13:06:00Z ~ 2026-10-20 13:06:00Z：

```
Gather Merge  (cost=573037.40..650694.69 rows=666778 width=72) (actual time=3687.806..3856.589 rows=697519.00 loops=1)
  Workers Planned: 2
  Workers Launched: 2
  Buffers: shared hit=63 read=322866 dirtied=20 written=244, temp read=8370 written=8391
  ->  Sort ... Sort Method: external merge  Disk: 22216kB  (每个 worker 22 MB 外部排序)
        ->  Parallel Bitmap Heap Scan on unified_data_202610
              Recheck Cond: ((sensor_name)::text = 'protection_status'::text)
              Rows Removed by Index Recheck: 6316788
              Filter: (时间窗 AND scope)
              Rows Removed by Filter: 38554
              Heap Blocks: exact=19119 lossy=88250
              ->  Bitmap Index Scan on unified_data_202610_sensor_name_idx
                    Index Cond: ((sensor_name)::text = 'protection_status'::text)
                    rows=813180
JIT:
  Timing: Generation 3.348 ms (Deform 1.276 ms), Inlining 252.389 ms, Optimization 174.575 ms,
          Emission 137.032 ms, Total 567.345 ms
Execution Time: 3905.083 ms
```

| 指标 | 值 |
|---|---|
| 返回行数 rows | **697,519** |
| heap 元组访问 | 6,316,788（Index Recheck，lossy bitmap）+ 38,554（Filter）+ 697,519 = **约 705 万行** |
| bitmap 索引命中 | 813,180 行；`lossy=88,250` 块（`work_mem=4MB` 不够，位图退化） |
| shared hit / read | 63 / **322,866 块 ≈ 2.64 GB** |
| 临时文件 | sort 外部排序 8,370/8,391 块（每 worker ~22 MB） |
| JIT 开销 | **567 ms**（占执行时间 14.5%） |
| Execution Time | **3,905.1 ms** |

**要点**：30 d 窗口≈整个 10 月分区，`(logical_device_id, timestamp)` 索引不具选择性，规划器只能走 `sensor_name` 单列 bitmap；而 4.4 GB 表里同一 sensor 的行分散在 20 天数据中，bitmap 一超 `work_mem` 就 lossy，**为了 69.8 万行有效数据重新检查了 631 万行 heap 元组、读了 2.64 GB 块**。这是 30 d 请求比 24 h 请求更贵的主因。

### 2.4 「16 类批量」的真实执行形态

【实测】cells 请求在途时 `pg_stat_activity` 采样（`.tmp-backend/capture_cells.log`，0.15 s 间隔，565 个快照）：

- 16 条同形 SQL **同一毫秒并发发出**；峰值 **24 个 PG 进程**同时 active（16 leader + 8 parallel worker；worker 受 `max_worker_processes=8` 封顶）。
- 进程级时间线（pid 首个/末个采样）：16 条查询从 13:05:30.718 起，直到 13:05:34.7–36.2 —— 即**并发下每条查询 wall 时间被拉长到 4–5.5 s**（单条独立执行实测 2.6 s，见 §2.2；并发共享缓存 + CPU 争用后单条变慢/整体吞吐受限）。
- 等待事件分布（745 个 historical 行）：无等待(纯 CPU)=501、`IPC/MessageQueueReceive`=112（并行 worker 通信）、`LWLock/BufferMapping`=53（共享缓冲区争用）、`IO/DataFileRead`=33、`IPC/MessageQueueSend`=15、`IO/AioIoCompletion`=18。
- HTTP 层：本取证 16 类请求 5.65 s / 1,571,275 B；与 Lead 基线 5.64 s / 1,571,075 B 一致。

---

## 3. Go 侧成本

### 3.1 `models.UnifiedData` 结构与大小（Go 实测）

【实测】用 `go run`（本地 golang:1.26.5 容器，源码逐字段复制 `backend/internal/models/models.go:315-329`，`.tmp-backend/gosize/main.go`）：

```
sizeof(UnifiedData) = 120 bytes
sizeof(DeviceData)  = 112 bytes
sizeof(time.Time)   = 24
sizeof(string)      = 16
json_row_bytes = 198   (单行响应 JSON，含 id/device_id/sensor_name/value/unit/timestamp/created_at/edge_device_id/logical_device_id)
```

- 结构体 9 字段 = 120 B（不含 `SensorName`/`Unit` 指向的字符串堆数据）。
- 降采样后的响应行数：16 类 × 500 = 8,000 行 × 198 B ≈ **1.58 MB** —— 与实测响应 1,571,275 B 吻合，证明响应体积由 `max_points` 决定。

### 3.2 全量载入的堆量级

【实测行数】24 h 每测点 86,251 行；【实测 sizeof】120 B/行。

| 请求 | Go 全量载入行数 | 结构体切片（仅 120 B/行） | 加上每行 string 堆分配（16 B sensor_name + 8~16 B unit，【推断】） |
|---|---:|---:|---:|
| cells 16 × 24 h | 1,380,016 | **157.9 MiB** | ≈ +33 MiB → ≈ 191 MiB |
| metrics 9 × 24 h | 776,259 | 88.8 MiB | ≈ 107 MiB |
| temp 3 × 24 h | 258,753 | 29.6 MiB | ≈ 36 MiB |
| soc 2 × 24 h | 172,502 | 19.7 MiB | ≈ 24 MiB |
| protection 1 × 30 d | 697,638 | 79.8 MiB | ≈ 96 MiB |
| **页面合计（若 5 个 batch 并行在途）** | **3,285,168** | **≈ 394.2 MiB** | **≈ 475 MiB（【推断】）** |

【实测】旁证：并发实验期间 cgroup `memory.current` web 容器 400→**728.4 MiB**（宕峰值采样），`memory.peak`= **971.1 MiB**（本轮取证窗口内记录的历史峰值，早于并发实验）；而 21:04 未压测时仅 46.8 MiB。

### 3.3 `downsampleUnifiedData`：全量载入后才等距抽样

`backend/internal/api/handler_data.go:35-50`：

```go
func downsampleUnifiedData(data []models.UnifiedData, maxPoints int) []models.UnifiedData {
    if maxPoints <= 0 || len(data) <= maxPoints { return data }
    step := len(data) / maxPoints
    ...
    result := make([]models.UnifiedData, 0, maxPoints+2)
    result = append(result, data[0])
    for i := step; i < len(data)-1; i += step { result = append(result, data[i]) }
    result = append(result, data[len(data)-1])
}
```

调用点在 `handler_data.go:397-398`（batch）与 `:322-323`（单 category）：**先 `q.Order(...).Find(&data)` 把 86,251 行全部读进 Go，再抽样到 500 行**。因此 `max_points` 只改变响应大小（1.57 MB → 几百字节），**不改变 SQL、不减少 DB 扫描、不减少 Go 堆分配**，只多一次 502 行的小切片分配。

【实测旁证】同一 cells 16 类请求：`max_points=500` 响应 1,571,275 B / 串行 5.65 s；`max_points=1` 响应 **515 B（gzip）**，而该次测量处于并发实验后的高负载窗口（loadavg 10.4），耗时 6.65 s —— 响应缩小 3000 倍、耗时同量级。该次耗时因受干扰不作为基线，**仅用于证明「响应大小与 DB 读取解耦」**；SQL 层面证据更硬：实际 SQL 中没有任何 LIMIT（§1.2 原文）。

---

## 4. 并发争用（页面级并发实测，作「上界」引用）

### 4.1 实验方法

【实测】21:13:17 UTC 起，用与浏览器相同的 8 个 GET 同时发出（共享 JWT，barrier 同步启动；`Accept-Encoding: gzip` 模拟浏览器），并同时采样 `pg_stat_activity`（0.15 s）与两个容器的 cgroup（0.15 s，`.tmp-backend/storm.sh`/`sampler_remote.sh`）。生产 loadavg 起始 1.45 → 结束 15.63。

### 4.2 串行 vs 并发（同一请求、同一参数）

| 请求 | 串行（21:10，loadavg 3.56） | 并发（21:13，页面级 8 并发） | 放大 | 是否 > axios 10 s |
|---|---:|---:|---:|---|
| cells 16 × 24 h | 5.65 s | **33.23 s** | 5.9× | 是 |
| metrics 9 × 24 h | 3.23 s | **26.42 s** | 8.2× | 是 |
| protection 30 d | 8.99 s | **55.14 s** | 6.1× | 是 |
| temp 3 × 24 h | 1.63 s | **32.34 s** | 19.8× | 是 |
| soc 2 × 24 h | 1.24 s | **24.81 s** | 20× | 是 |
| latest-data | 0.32 s | **32.89 s** | 103× | 是 |
| detail | 6.4 ms | 8.66 s | 1350× | 否（刚好 <10 s） |
| commands | 4.9 ms | 7.67 s | 1560× | 否 |

- **8 个请求中 6 个超过前端默认 10 s 超时**（`client.ts:46`）；其中 latest-data 虽在 `edgeDevice.ts:388` 放宽到 20 s，33 s 仍会超时。
- 违反直觉但可解释：**便宜请求被饿死**。31 个 batch 子查询（16+9+1+3+2）瞬间占满 PG 的 3 vCPU 和 Go 连接池（`MaxOpenConns=25`），latest-data（本来只要 320 ms 的 204 万行 seq scan）与 detail（9 行 seq scan）被排到队尾，耗时被放大 100–1500 倍。这正是用户感知「整页卡死」的机制。
- 【推断，归前端同事负责】超时后 `useHistoryData.ts:99-115` 会降级为每 category 单发 `/unified-data/historical`，5 个 batch 卡可能再放大成 31 个单请求，进一步加剧争用。

### 4.3 并发时的 PG 与容器状态【实测】

`pg_stat_activity`（`.tmp-backend/storm/pgact.log`，333 个快照）：

| 指标 | 值 |
|---|---|
| historical(含 worker) active 峰值 | **33**（页面级并发期间均值 **29.0**） |
| 总 active 峰值 / idle | 34 / 最低 2 |
| 等待分布（窗口内 active 采样） | 纯 CPU 运行 53%、`LWLock` 13%、`Client/ClientWrite` 12%（PG 向 Go 写结果集阻塞）、`IO` 11%、`IPC`（并行 worker）11% |
| 最长单条查询 | protection 请求在途 55.1 s（HTTP `time_total`） |

容器（cgroup v2，宿主 `/sys/fs/cgroup/system.slice/docker-*.scope`）：

| 指标（并发窗口 49.65 s） | ehome-prod-web | ehome-prod-postgres |
|---|---:|---:|
| CPU 消耗（cgroup `cpu.stat usage_usec` 差值） | **48.07 CPU-s（0.97 core）** | **91.36 CPU-s（1.84 core）** |
| 峰值内存（采样） | 728.4 MiB（起始 400.2） | 3.31 GiB（起始 2.85） |
| `docker stats` @2.5 s | CPU 88.87% / MEM 454.2 MiB | CPU **197.42%** / MEM 2.317 GiB |

- 两个容器合计 **2.81 core / 3 vCPU = 94%**，主机 loadavg 峰值 15.6；并发结束后主机 swap 已用 **648 MB**（`SwapTotal` 4 GB），`MemAvailable` 3.0 GB —— 一次页面加载把 4 GB 主机打到内存/交换压力区（PG 3.3 GB + web 0.73 GB + 其它容器）。
- **结论（可作为上界引用）**：一次 BMS 页面加载在生产上消耗约 **140 CPU-s**（web 48 + PG 91）、**约 4 GB 级瞬时内存压力**，并发上界耗时 25–55 s；该组数值包含本轮取证负载，**不能当作用户基线**。

---

## 5. latest-data 306 ms 的根因（与 commands 6 ms 对比）

### 5.1 Handler 与 SQL

`backend/internal/api/handler_edge_device.go:1269-1285`：

```go
e.GET("/:id/latest-data", func(c *gin.Context) {
    qs, _ := datalifecycle.ResolveDataQueryScope(db, uint(id))
    var data models.DeviceData
    cond, args := dataScopeCond(qs)
    db.Where(cond, args...).Order("created_at DESC").First(&data)   // LIMIT 1
    Success(c, data)
})
```

- 查的是 **`device_data`**（原始行表），不是 `unified_data`。
- `device_data` 索引：`(id,timestamp)` PK、`device_id`、`edge_device_id`、`node_id`、`timestamp`、`(logical_device_id, timestamp DESC)` —— **没有 `created_at` 索引**，而查询是 `ORDER BY created_at DESC LIMIT 1`；scope 条件是 OR，也无法直接走 `(logical_device_id, timestamp)`。
- `device_data_202610` = 1,295 MB / 约 204 万行；设备 14 占 **697,636 行**（【实测】`SELECT count(*)`）。

### 5.2 EXPLAIN 实测

```
Limit  (cost=147831.84..147831.95 rows=1 width=462) (actual time=332.323..340.414 rows=1.00 loops=1)
  Buffers: shared hit=74 read=129328 dirtied=2 written=2
  ->  Gather Merge ... Workers Launched: 2
        ->  Sort (Sort Key: device_data.created_at DESC)
              Sort Method: top-N heapsort  Memory: 28kB   (每 worker)
              ->  Parallel Seq Scan on device_data_202610
                    Filter: ((logical_device_id = 14) OR ((logical_device_id IS NULL) AND (device_id = 14)))
                    Rows Removed by Filter: 481159 ×3
Execution Time: 341.588 ms
```

| 指标 | 值 |
|---|---|
| 扫描行数 | 并行 seq scan **约 214 万行**（697,640 命中 + 1,443,477 过滤掉 ×3 loops 口径） |
| shared hit / read | 74 / **129,328 块 ≈ 1.03 GB** |
| Execution Time | **341.6 ms** |
| HTTP 实测 | 320 ms / 2,834 B（Lead 基线 306 ms / 2,860 B，吻合） |

**根因**：只取 1 行，但要 **seq scan 整个 10 月分区 204 万行 + 每进程 top-N 排序**（无 created_at 索引可终止扫描）。306–341 ms 就是这张 1.3 GB 表一次顺序扫描的成本。

### 5.3 commands（command-intervals）6 ms 对比

`backend/internal/api/handler_driver_commands.go:39-80` `GET /api/v1/edge-devices/:id/commands`：只做一次 `db.First(&dev, id)`（`edge_devices` 9 行，seq scan 3 块）+ 内存里叠 command template。

【实测】`EXPLAIN ANALYZE SELECT * FROM edge_devices WHERE id=14 LIMIT 1` → `Execution Time: 0.025 ms`，shared hit=3；HTTP 4.9 ms / 1,353 B。**同一页面里最快的请求和最慢的请求差 3 个数量级，成本差异全部来自数据表扫描方式。**

---

## 6. gzip 中间件与传输量

`backend/cmd/server/main.go:468-479`：`gin.New()` 第 2 个全局中间件 `gzip.Gzip(gzip.DefaultCompression, ...)`（仅排除 `/metrics` 与 `/api/v1/metrics/prometheus`），**对 JSON API 生效**。

【实测】同一请求带/不带 `Accept-Encoding`：

| 请求 | 未压缩 body | gzip body | 压缩比 |
|---|---:|---:|---:|
| cells 16 × 24 h | 1,570,881 B | **165,226 B** | 9.5×（10.5%） |
| metrics 9 × 24 h | 892,112 B | ≈94,384 B | 9.5× |
| protection 30 d | 100,213 B | **10,314 B** | 9.7× |
| detail | 8,395 B | 2,094 B | 4.0× |
| latest-data | 2,834 B | 617 B | 4.6× |

响应头【实测】：`Content-Encoding: gzip`、`Vary: Accept-Encoding`（`hdr_cells_gzip.txt`）。未压缩时为 `Transfer-Encoding: chunked`、无 `Content-Encoding`。

- 压缩只省**外部线路传输量**（页面总量 ~3.07 MB → ~0.32 MB），**不减少 DB 读取与 Go 扫描**；实测 gzip 后 cells 耗时 5.83 s vs 未压缩 5.64 s（+0.18 s，在噪声范围内）——压缩 CPU 成本存在但小。
- 因此「浏览器 10 s 超时」是**服务端处理慢**，不是传输慢；网络/外部链路因素由 network-path 同事负责，不在本取证结论内。

---

## 7. 服务端每进一次 BMS 页的实际成本汇总（数量级）

> 口径：一次页面加载 = 5 个 `historical-batch` + latest-data（+ detail/commands 可忽略）。DB 行数为 `EXPLAIN` 实测推导；Go 堆为 `sizeof` + 实测行数推导；CPU 为 cgroup 实测（并发上界）。

| 维度 | 数值 | 依据 |
|---|---|---|
| PG heap 元组访问 | **约 9,132 万行**（24 h 查询 30×280.9 万=8,427 万 + protection 约 705 万） | §2.2/2.3 EXPLAIN `rows + Rows Removed` |
| 返回给 Go 的行（全量载入） | **3,285,168 行**（30×86,251 + 697,638） | §2 实测 count |
| PG 读取块数（估算合计） | 24 h 单条 14.5 万块 × 30 + protection 32.3 万块 ≈ **467 万块（≈37 GB 逻辑块访问）** | §2.2/2.3 BUFFERS；缓存命中率高时其中多数为 shared hit |
| Go 堆（结构体切片） | **≈394 MiB**（若 5 个 batch 并行在途）；实际采样 web 容器 400→728 MiB | §3.2 + cgroup |
| Go 堆峰值（容器历史峰值） | **971.1 MiB**（`memory.peak`，本轮窗口内记录） | cgroup `memory.peak` |
| 服务端 CPU | **≈140 CPU-s**（web 48 + PG 91，页面级并发窗口实测，**上界/含干扰**）；串行基线按单条 EXPLAIN 外推约 40–80 CPU-s（【推断】） | §4.3 cgroup；§2 EXPLAIN |
| 响应体积（未压缩/ gzip） | ≈3.07 MB / ≈0.32 MB | §6 |
| 耗时（串行/并发上界） | **21.06 s / 25–55 s** | §0/§4.2 |

**一句话回答「为什么 6 个请求 20+ 秒、谁在花时间」**：时间不在前端渲染，也不在 gzip/网络，而在 `historical-batch` 这个「按测点全窗口拉全量、Go 侧再抽样」的查询模式：缺少 `(logical_device_id, sensor_name, timestamp)` 复合索引，导致每个测点查询都把整个 24 h 窗口（281 万行）或 30 d 窗口（bitmap 705 万行）扫一遍；一次页面加载在 PG 侧约 9130 万行访问、Go 侧约 3.29M 行/394 MiB 物化，串行 21 s，并发下因 31 条子查询争抢 3 vCPU/25 连接而涨到 25–55 s，超过前端 10 s 超时。

---

## 8. 关键源码/脚本索引（可复现）

| 事项 | 位置 |
|---|---|
| batch handler（并行 fan-out、无 LIMIT） | `backend/internal/api/handler_data.go:333-408`（查询 372,391-397；降采样 398） |
| 降采样实现 | `backend/internal/api/handler_data.go:35-50` |
| scope 条件 | `backend/internal/datalifecycle/scope.go:32-53`；`query_scope.go:45-78` |
| latest-data handler | `backend/internal/api/handler_edge_device.go:1269-1285` |
| commands handler | `backend/internal/api/handler_driver_commands.go:39-80` |
| gzip 中间件 | `backend/cmd/server/main.go:468-479` |
| 连接池 | `backend/internal/database/gorm.go:50`（MaxOpenConns=25） |
| axios 超时 | `frontend-shared/src/api/client.ts:46`（10000 ms）；latest-data 例外 `edgeDevice.ts:388`（20000 ms） |
| 前端 batch 参数 | `frontend-shared/src/composables/useHistoryData.ts:81-89`；`bms/diag/useDiagHistory.ts`（≤3 并发、max_points=500） |
| 本次取证脚本与原始输出 | `.tmp-backend/`（db.sh, watch*.sql, capture_cells.log, explain_cell_24h.sql, explain_prot_30d.sql, explain_latest.sql, storm/, measure_cpu.sh, gzip 头文件等） |

### 附：原始测量输出（本机命令）

- 串行：`./.tmp-backend/serial_measure.sh`（2026-10-20 13:10:07–13:10:28 UTC）
- EXPLAIN：`./.tmp-backend/db.sh < .tmp-backend/explain_cell_24h.sql`（13:05 UTC）；`explain_prot_30d.sql`（13:06 UTC）；`explain_latest.sql`（13:07 UTC）
- 并发：`./.tmp-backend/storm.sh`（13:13:17–13:14:16 UTC，loadavg 1.45→15.63）
- gzip：`./.tmp-backend/gzip_measure.sh`（13:11:37 UTC）
