# unified_data 停写 — 藏表演练清单（task-24）

> 演练脚本 + 逐接口预期结果 + 时间维度验证。**先写后跑** —— 上一轮就是因为「到那时才想该看什么」，
> 漏了 `/edge-devices/:id/data`，差点带着一个会永久静默丢数据的 bug 上线。
>
> **修订 2026-11-07（boot-api 审查）**：扩到四条 grep、补 8 个读点、修 3 处错、
> 加时间维度演练（§0.2）、停写切点验真（§0.3）、coverage 推进判据、可逆性（§5）。
> 修订依据逐条标注在对应位置。

## 生成方式（决定这份清单会漏什么）

本清单由**四条互补的 grep** 生成。**每一条都有自己的盲区，四条缺一不可。**

```bash
# ① 原生 SQL —— 抓裸 SQL 读者
grep -rn "FROM unified_data" --include="*.go" internal/ cmd/ | grep -v _test.go

# ② GORM Model 显式 —— 抓 Model/Table 链路
grep -rn "Model(&models.UnifiedData{})" --include="*.go" internal/ cmd/ | grep -v _test.go

# ③ GORM **类型推断** —— 抓「既无原生 SQL、也无 Model 调用」的读者
#    db.Where(...).Find(&rows) 其中 rows 是 []models.UnifiedData：
#    GORM 从**目标结构体类型**推断表名，SQL 里既没有表名也没有 Model()
grep -rn "\[\]models.UnifiedData" --include="*.go" internal/ cmd/ | grep -v _test.go

# ④ 接口路径注册 —— 列全部端点，用于反查调用链
grep -rn "v1.GET(\|v1.POST(" --include="*.go" internal/api/ | grep -v _test.go
```

### 每条抓不到什么（这比列四条命令有用）

| grep | 抓得到 | **抓不到** |
|---|---|---|
| ① `FROM unified_data` | 原生 SQL | 一切 GORM 链路（Model 与类型推断都不含 `FROM`） |
| ② `Model(&models.UnifiedData{})` | GORM 显式调用 | **类型推断**（`var rows []models.UnifiedData` + `.Find()`）—— 本仓 8 处 |
| ③ `[]models.UnifiedData` | `var` / `Find` / 函数签名全形态，**含假阳性**（零值字面量 `models.UnifiedData{}`、字段声明、返回值类型） | 原生 SQL（返回值可能是别的类型）；**不区分「读」与「只是拿了个变量」** |
| ④ 路由注册 | 全部端点 | **任何读点** —— 它只列端点，不证明谁读了表。作用只是「拿端点清单反查调用链」 |

**教训（四次同形状，逐次递进）**：

```
第 1 次  只跑 ①              => 漏 6 处 GORM Model 读者
第 2 次  跑 ① + ②            => 漏 8 处 GORM 类型推断读者（本清单第一版）
第 3 次  只演练图表            => 漏了列表接口 /edge-devices/:id/data
第 4 次  DETACH 演练只跑几分钟  => 漏了时间维度（coverage 停止推进要等 2 天才显形）
```

**共同形状：穷举的「维度」不完整，而每个维度都有自己的盲区。**
补第 N 条 grep 只解决「同一个维度漏了一类写法」；第 3、4 次是**换了维度**
（接口维度、时间维度），靠加 grep 发现不了 —— 这两类只能靠**顺着数据流推演**。

## 0. 演练怎么做

> **有脚本了**：本清单已落成可执行版本 —— `scripts/drill_unified_stop.sh`。
> 优先用它（它会自动打勾、区分三态、trap 保证恢复），本文档作为它的依据与排障手册。
> ```bash
> bash scripts/drill_unified_stop.sh --verify-manifest   # 先核对 MANIFEST 与本文档 §2 是否一致
> bash scripts/drill_unified_stop.sh --all               # 完整演练（25 项）
> bash scripts/drill_unified_stop.sh --phase=time        # 只跑某个相位
> ```
> 退出码：0=全部执行且通过 / 1=有失败或有未执行 / 2=有未执行但已 --allow-skip 或只跑了部分相位 / 3=恢复失败。
> 脚本的 EXPECTED 清单**硬编码**在 MANIFEST 里（不从执行计数自增），否则「未执行」会恒为 0
> —— 那是反模式 8 的第四层，见 §3 末段。

### 0.1 空间维度：把 unified_data 藏起来（DETACH）

```sql
-- 1. 记录演练前状态（逐接口存一份点数/行数基线，后面要对比）
SELECT count(*) FROM unified_data;

-- 2. 把**所有** unified_data 分区 DETACH（可逆，不删）
ALTER TABLE unified_data DETACH PARTITION unified_data_202510;
ALTER TABLE unified_data DETACH PARTITION unified_data_202511;
-- …逐个执行

-- 3. 跑 §2 的逐接口回归（预期结果见下表）

-- 4. 无论成败都要恢复
ALTER TABLE unified_data ATTACH PARTITION unified_data_202510 FOR VALUES FROM ('2025-10-01') TO ('2025-11-01');
```

⚠ DETACH 期间**不能重启服务**（启动路径会读 unified_data 做 warmup，见 §1 #9）。
演练完成后核对行数与演练前一致再收工。

#### 0.1.1 ⚠⚠ DETACH 会阻塞生产写 —— 这是演练本身最大的风险（2026-11-07 实测）

`DETACH PARTITION` 需要 **ACCESS EXCLUSIVE** 锁，与**所有**其它锁模式冲突。
而生产环境永远有并发写（每 5 秒一帧）。所以只要库上有一个慢查询：

```
慢查询持 AccessShareLock     => DETACH 拿不到锁，进队列等待
等锁的 DETACH 排在队列里      => 后面所有写者的 RowExclusiveLock 也排队
=> 遥测写入被阻塞（实测 15 分钟）
```

**真正伤人的不是「DETACH 慢」，是「DETACH 排队时把写者也拦住」。**
所以解决办法不是「耐心等」，是**别排队**。

三层护栏（脚本已实现）：

| 层 | 做法 | 为什么 |
|---|---|---|
| ① 预检（只读） | `pg_stat_activity` 查 `GREATEST(query_start, xact_start)` > 10s 的会话，有就**拒绝演练** | 不盲目抢锁。也用 `xact_start` 是因为：长事务里反复跑短语句时 `query_start` 永远很新，但锁从头就持有 |
| ② 快速失败 | 单事务 `SET LOCAL lock_timeout='5s'` + 把所有 DETACH 放一个事务 | 拿不到锁 5 秒报错退出，队列立刻释放。单事务还保证「要么全藏、要么全不藏」，不出现藏一半 |
| ③ 失败即中止 | DETACH 失败 => `exit 1`，**不执行 §2 的 25 项回归** | 分区没藏起来时那 25 项测的是**正常状态**，会全绿，然后被当成「演练通过」。这是本演练能犯的最严重错误 |

**「预检」为什么不能省成「直接加 lock_timeout」**：超时能保证不挂住，但掩盖了根因
——你会以为「演练偶尔失败」，而不知道是「有个 52 分钟的查询在跑」。预检把那句话打印出来。

**`--force`**：确实要带阻塞者硬上时用。它把阻塞者快照写进**独立审计文件**
（`/tmp/ehome-drill/forced-<db>-<ts>.log`，不会被删），并且 DETACH 仍受超时保护。

> 审计文件为什么不写进状态文件：状态文件在 DETACH 失败或恢复成功时都会被删，
> 而「谁当时在跑」这条信息**恰恰在 DETACH 失败后最需要**。写在会被删的文件里 = 写到没有。

#### 0.1.2 ⚠⚠ 演练会**自己制造**阻塞 DETACH 的长查询（2026-11-07 第二次演练失败）

第一次演练成功、第二次失败。差别不在代码，在缓存冷热：

```
drill 的「采集基线」会逐个调 22 个接口
其中有 #10 /metrics/summary
⇒ 该接口的 points_stored 缓存过期 ⇒ 同步重算（全表 GROUP BY）
⇒ 生产实测跑了 5 分 37 秒，全程持 AccessShareLock(unified_data)
⇒ **drill 自己的基线采集创造了阻塞 DETACH 的查询**
```

三处必须的做法：

| # | 做法 | 为什么 |
|---|---|---|
| ① | 采完基线后 `wait_for_quiet`，等到没有 >5s 的持锁会话再往下走 | **curl 超时 != 查询结束**。`-m` 只是客户端不等了，服务端 SQL 还在跑、还持着锁 |
| ② | 预检**跑两次**：采基线之前一次、采完之后再一次 | 第一次检查的是「采基线之前」的状态，采基线可能制造新阻塞者 |
| ③ | `NO_BASELINE_IDS` 把「数值本就不作判据」的接口摘出基线 | #9 `data_count_today`、#10 `points_stored` 在 §3 第 4 类里明确不作正确性判据，不该为它付 33 秒 |

顺序必须是：

```
预检 → 采基线 → 结算(wait_for_quiet) → 重跑预检 → DETACH → 回归 → 结算 → 恢复
```

> **判据要看锁，不能看 query 文本。** 实测：一个会话先 `SELECT ... FROM unified_data`
> 再 `SELECT pg_sleep(25)`，它当前语句是 pg_sleep、**query 里不含 unified_data**，
> 按文本过滤抓不到 —— 但它的事务仍持 AccessShareLock，DETACH 照样超时。
> 所以要查 `pg_locks`，不是 `pg_stat_activity.query ILIKE`。

**恢复侧同样要结算**：`ATTACH` 要 ACCESS EXCLUSIVE，会被回归阶段超时接口留下的
服务端查询挡住。那是最坏情况 —— 数据保持隐藏 + 恢复也卡住。

**恢复侧同样要有超时**：`ATTACH` 也要 ACCESS EXCLUSIVE，也会挂住、也会挡写。
但恢复必须成功，所以超时给得更长（30s）并重试 3 次 —— 宁可多等，也不能让数据保持隐藏。
这一段的取舍与 DETACH 相反，是刻意的：**失败要快（演练），恢复要成（数据）**。

### 0.2 时间维度：DETACH 演练**覆盖不到**的那一类（本次新增）

DETACH 演练只持续几分钟，而**停写引入的最主要故障要等 2 天才显形**。原因：

```
rollup 读路径的门禁是 telemetry_rollup_coverage（回填写下的元数据），不是数据本身。
而 coverage 的推进依赖 rollup_backfill 的单元枚举：
    SELECT min(timestamp), max(timestamp) FROM unified_data   (rollup_backfill.go:279)
停写后 max(timestamp) 冻结在 T_stop ⇒ T_stop 之后的日子永不进 units
⇒ coverage 表永远没有那些天的行
⇒ RollupWindowCovered 对「查不到行」返回 false（rollup_coverage.go 的既定行为）
⇒ 回落 unified_data，而它最新一行是 T_stop
⇒ >6h 窗口图表画到 T_stop 就断，HTTP 200，无任何告警
```

**而且这不是停写才引入的**：`rollup_backfill` 只在**进程启动时跑一次**
（`main.go` 的 `go func(){ RunOnce() }`，没有 ticker、也没有定时任务），
所以服务连续运行超过 2 天，coverage 就已经停止推进了 —— 现在只是因为回落能拿到满的
unified_data，图表正常，看不出来。

**生产实测（Lead 2026-11-07）**：
```
telemetry_rollup_coverage  max(day) = 2026-11-06
今天 UTC                            = 2026-11-07
容器启动                            = 11-07T11:59Z
rollup 表最晚桶                     = 11-07 14:10Z
=> coverage 停在昨天，rollup 已写到今天 —— 链条成立
```

**时间维度演练步骤（必须单独跑一轮）**：

```sql
-- ① 演练前记录基线
SELECT max(day) AS coverage_max_day FROM telemetry_rollup_coverage;
SELECT count(*) AS unified_rows FROM unified_data;

-- ② 判据：coverage 是否还在推进
SELECT max(day) FROM telemetry_rollup_coverage;   -- 正常态 ≈ UTC 昨天（回填排除今天）
SELECT max(bucket) FROM telemetry_rollup_1m;      -- 正常态 ≈ 当前分钟（live collector 在写）
```

**判据：`max(coverage.day)` 必须 = UTC 昨天**（回填排除今天，所以正常态就是「昨天」）。
若明显早于「今天−1」，说明回填上次跑完后再没推进过 —— **这本身就是缺陷**（与停写无关），
必须先修掉，否则停写后症状会从「静默回落」升级成「静默截断」。

```sql
-- ③ 驱动回填推进（模拟 max(ts) 前进，不真停写）
--    给最多 3 台设备各打一行假数据，只为驱动单元枚举
--    sensor_name 用不可能出现的标记值，方便精确回滚
WITH devs AS (SELECT DISTINCT device_id FROM unified_data LIMIT 3)
INSERT INTO unified_data (device_id, sensor_name, value, timestamp, created_at)
SELECT device_id, '__drill_marker__', 0, now(), now() FROM devs;

-- 演练后必须回滚（先查再删，确认只删标记行）
SELECT count(*) FROM unified_data WHERE sensor_name = '__drill_marker__';
DELETE FROM unified_data WHERE sensor_name = '__drill_marker__';
```
然后**重启服务**触发一次回填，再查 `max(day)` 是否推进到 UTC 昨天，最后删掉标记行。

⚠ `partitionParentDDL` 里 unified_data 的列顺序是
`id / device_id / sensor_name / value / unit / timestamp / created_at / edge_device_id`，
`id` 有 `nextval` 默认值、`unit` 与 `edge_device_id` 可空 —— 上面的 INSERT 依赖这三点，改表结构时要同步。

这一步**必须在停写上线前通过**：它验证「停写后 coverage 仍能推进」这个前提；
不通过就说明修复（live 写 coverage / 枚举脱钩）没做到位。

### 0.3 停写切点验真：三个 sink 必须都还活着（本次新增，来自实现审查）

停写 unified_data 只有一种正确切法。`consumers_heavy.go` 里解析成功后到写库之间有
4 个副作用，顺序固定：

```
:462  c.telemetrySink(...)        // L2 帧表
:465  c.telemetryRollupSink(...)  // L3 分钟桶
:484  c.latestSink(...)           // 最新值缓存      ← 在 L1 门控之前
:490  persist := c.writeGate.filter(...)   // L1 收窄
:495  c.db.Create(&persist)       // unified_data INSERT   ← 停写只该砍这一行
:509  c.writeGate.commit(...)     // 只在 Create 成功后调
```

| 切法 | 缓存 | 帧表 | rollup | 判定 |
|---|:--:|:--:|:--:|---|
| A. 只跳 `:495` | ✓ | ✓ | ✓ | ❌ `:509` 永不调用 ⇒ `g.last` 不推进 ⇒ 每帧判 First ⇒ 指标失真 |
| **B. 跳 `:490` + `:495`** | ✓ | ✓ | ✓ | ✅ **推荐**：L1 门控的唯一目的就是减少 INSERT，没有 INSERT 就没有门控 |
| C. 跳过整段 | ✗ | ✗ | ✗ | ❌ **自毁**：L2/L3 正是停写的目的所在 |

**C 的伪装度最高**：「图表没数据」看起来像「停写生效了」，实际是把唯一的数据源也停了。
现有代码 :427 的注释已专门写过同一件事：
> 「L2 帧级编码: L1 开启 write_on_change 后被跳过入库的帧仍然是一次真实采样，
>   必须进帧表 —— 否则增量链出现空洞」

**验收断言（必须做成用例，不能靠人工看日志）**：

```
停写开关打开后 60 秒：
  telemetry_frame     新增帧数 > 0
  telemetry_rollup_1m 新增桶数 > 0（每分钟）
  latest 缓存         命中（/latest-values 返回运行期值而非回落值）
  unified_data        行数不增长            ← 唯一应该停的
```

## 1. 从代码穷举的 unified_data 读者

穷举方法：**§0 生成方式的四条 grep 全部跑一遍**，再逐条确认是「真读」还是假阳性。

> ⚠ **行号基线 = commit `5889a6f9`（2026-11-07 23:0x，impl-l2 合并后）**。行号会随代码演进而腐烂，
> 而**腐烂时不会报错** —— 这是《测试判据反模式》§8 第 6 条说的那类缺陷。
> 演练前先跑一次自检**（这是「非代码引用腐烂」的自动化判据，不是可选项）**：
> ```bash
> cd backend
> grep -oE '[a-z_]+\.go:[0-9]+(,[0-9]+)*' \
>   ../docs/验证/unified_data停写-藏表演练清单-2026-11-07.md | sort -u | \
> while IFS=: read -r ff ls; do
>     src=$(find internal cmd -name "$ff" -not -name "*_test.go" | head -1)
>     [ -z "$src" ] && { echo "MISSING  $ff"; continue; }
>     for l in ${ls//,/ }; do
>       echo "$ff:$l => $(sed -n "${l}p" "$src" | sed "s/^[[:space:]]*//" | cut -c1-56)"
>     done
>   done
> ```
> **判据**：每条输出都必须含 `unified_data` 或 `UnifiedData`。
> 出现 MISSING、或输出的是别的代码 ⇒ 行号已腐烂，以新行号为准更新本表。
>
> **已知会漂的一组**：`rollup_backfill.go` 的 #10 与 `main.go` 的 #9 —— 这两处恰恰是
> 本任务要改的地方（coverage 推进脱钩 + warmup 加窗）。改完之后**必须回来更新行号**，
> 否则演练时按旧行号会查到注释行 —— 那不是「没问题」，是「找错地方」。
> 不要把「跑不通的自检」当成「没问题」（反模式 8：没验成被统计成验了且通过）。

### 1.1 读点总表（29 处真读，按 grep 来源分组）

> 标 **🆕** 的是本轮新增的 —— 8 处来自 grep ③（类型推断）盲区，4 处来自 impl-l2 的
> skip-scan 修复（把一个无界全表扫拆成 3 个索引读点）与帧表聚合的日期探测。
> **读点数增加不一定变糟**：那个 1 → 3 是 33s → 2.8ms 的代价，静态成本换运行时事故。

| # | 位置 | 触发的 HTTP 接口 | 读什么 | 停写后的正确来源 |
|---|---|---|---|---|
| **① 原生 SQL（14 组 / 22 行代码 + 2 行注释）** ||||
| 1 | `handler_data.go:135,139` | GET /nodes/:id/latest | 全历史 `GROUP BY device_id` 取 `MAX(ts)`，**无时间界** | 缓存（步骤 1 已完成，832fea00）；冷缓存回落此 SQL |
| 2 | `history_points.go:319` | GET /devices/:id/history、/unified-data/historical(-batch) | 聚合 SQL（帧表/rollup 都不可用时的**第三级回落**） | 帧表；停写后此处返回旧数据 ⇒ §3 第 1 类 |
| 3 | `device_data_sensors.go:167,168` | GET /edge-devices/:id/latest-data | 每 sensor 最新值（**有界**：近 2h） | 缓存（步骤 3）；冷缓存回落 |
| 4 | `device_data_sensors.go:175,176` | 同上 | 同上（**无界**：设备停报超 2h 时的「最后已知状态」） | 同上 |
| 5 | `device_data_sensors.go:369,370` | GET /edge-devices/:id/data | 列表 as-of 基线（帧表路径失败时的回落） | **帧表为主路径**，见 1.2 |
| 6 | `device_data_sensors_frames.go:225` | 同上（单位补全） | `sensor_name → unit` | `sensor_field_obs`（已迁），unified 仅过渡补全 |
| 7 | `handler_edge_device.go:439` | GET /edge-devices（列表） | `DISTINCT ON (device_id) created_at DESC` 富化，**无时间界** | 缓存；冷缓存回落 |
| 7b | `handler_edge_device.go:2660` 🆕 | GET /:id/latest-values（miss 回落 UNION） | 每 sensor 最新一行（skip-scan 后） | 缓存；停写后返回旧值 |
| 7c | `handler_edge_device.go:2705,2707` 🆕 | 同上（skip-scan CTE） | 递归取 sensor 名 | 同上 |
| 7d | `handler_edge_device.go:2736` 🆕 | 同上（DISTINCT 兜底） | sensor 名清单 | 同上 |
| 8 | `handler_overview.go:94,96` | GET /overview | 同上富化 | 缓存；冷缓存回落 |
| 9 | `main.go:517` → `latest_values_warmup.go:109,110` | **启动 warmup** | `DISTINCT ON (device_id) created_at DESC` | 步骤 2（已加 24h 时间窗，`EHOME_LATEST_WARMUP_LOOKBACK`） |
| 10 | `rollup_backfill.go:326,354,486` 🆕 | 后台回填（无 HTTP） | 设备清单 / 起点 / 聚合 | 见 §5；**枚举已脱钩、聚合按日期选源**（`07e060a4`） |
| 10b | `rollup_backfill_frames.go:96` 🆕 | 同上（`dayHasUnifiedRows` 探测） | 该 (device, day) 在 unified_data 有无行（用来**选源**） | 这是停写后的选源判据；停写后恒返回 false ⇒ 走帧表 |
| **② GORM Model 显式（7 处）** ||||
| 11 | `handler_data.go:230` | **GET /unified-data/categories** | `GROUP BY sensor_name` 取 `MAX(unit)` | `sensor_field_obs`（维度表） |
| 12 | `handler_data_source.go:370` | GET /data-source/... | 字段是否有数据（`Count > 0`） | 帧表 / `sensor_field_obs` |
| 13 | `datasource/group_crud.go:153` | 数据源分组（同 #12 判据） | 同上 | 同上 |
| 14 | `handler_metrics.go:119` | GET /metrics/summary | 首次失败时退化为行数 | 帧表（#15 已是帧表口径） |
| 15 | `handler_metrics.go:137` `countStoredFrames` | GET /metrics/summary | `COUNT(DISTINCT device_id, timestamp)` | 帧表；停写后**冻结在 T_stop** ⇒ §3 第 4 类 |
| 16 | `handler_overview.go:160` | GET /overview | 今日帧数 | 帧表；停写后**恒为 0** ⇒ §3 第 4 类 |
| 17 | `handler_sensor_fields.go:364` | GET /sensor-fields/resolve | 最近 N 小时的原始样本 | 帧表 / `sensor_field_obs` |
| **③ GORM 类型推断（8 组，第一版全漏 🆕）** ||||
| 18 | `handler_data.go:86` 🆕 | **GET /devices/:id/sensor-data** | 原始行 `ORDER BY timestamp DESC LIMIT N` | 帧表（需新增）；停写后返回 T_stop 前的旧行 |
| 19 | `handler_data.go:199` 🆕 | **GET /devices/:id/history（raw 分支）** | 原始样本序列 | 帧表（raw 模式帧表给不了那个形状，需专门处理） |
| 20 | `handler_data.go:324` 🆕 | **GET /unified-data/historical（raw 分支）** | 同上 | 同上 |
| 21 | `handler_data.go:443` 🆕 | **GET /unified-data/historical-batch（`useAgg=false` 分支）** | 同上 | 同上 |
| 22 | `handler_data_report.go:21` 🆕 | **GET /data-reports** | 历史报表数据 | 帧表（需新增） |
| 23 | `handler_edge_device.go:2665` 🆕 | **GET /edge-devices/:id/latest-values（miss 回落 Scan 目标）** | 修复后是 skip-scan 的结果集（不是全表）；历史上这里曾是全表 | 缓存（步骤 3）；见 §1.3 |
| 24 | `handler_sensor_fields.go:363` 🆕 | GET /sensor-fields/resolve | 同 #17（同一段代码的 `var rows` 形态，`Model()` 在 :364） | 帧表 / `sensor_field_obs` |
| 25 | `device_data_sensors.go:291` 🆕 | **GET /edge-devices/:id/data（增量路径）** | as-of 基线之后的行 | **帧表为主路径**，见 1.2 |

> #24 与 #17 是同一段代码的两种写法痕迹（`Model()` 在 :364、`var rows` 在 :363），
> 不是两个独立读点；保留两行是为了让「哪条 grep 抓到它」可追溯。

### 1.2 ⚠ `/edge-devices/:id/data` 的真实结构（本次修正）

第一版清单第 4 行写「`device_data_sensors.go:369` = 列表 as-of 基线，帧表（已迁）」。
**方向对，但不完整** —— 同一函数里是「**帧表主路径 + unified 回落**」的两段式：

```go
// device_data_sensors.go:277 附近
if snaps, ok := latestSensorSnapshotsFromFrames(db, deviceID, earliest, latest); ok {
    // ← 帧表是**主路径**（:277），一次解码服务整页
    return
}
state := latestSensorViewsAt(db, deviceID, earliest)      // ← 回落（:290）：as-of 基线
var deltas []models.UnifiedData
db.Where("...timestamp > earliest...").Find(&deltas)      // ← 回落里的增量（:291）
```

**停写后的故障形态**：帧表路径**失败时**，回落变成「空/旧基线 + 空增量」⇒ **整页 sensors 空**。
（帧表路径失败的情形：slot 字典缺失、该时段无帧、窗口超限。）

**这正是上一轮出问题的同一个接口、同一种故障形态**（历史行丢 sensors）。

### 1.3 附带的现有隐患：`/:id/latest-values` 的回落是无界全表扫（**已修，2026-11-07**）

> **状态：已修（`856a6c9a`，33s → 2.8ms）。** 保留本节是因为它示范了一类问题：
> 「只在缓存 miss 时进」的分支可以潜伏很久。修法是把回落改成索引 skip-scan，
> 代价是读点数 +2（原 1 处变 3 处：miss 回落 UNION / skip-scan CTE / DISTINCT 兜底），
> 但那笔交易很划算 —— 读点数是**静态**成本，OOM 是**运行时**事故。

**修复前**（`handler_edge_device.go` 当时的 :1684）的回落：

```go
var rows []models.UnifiedData
db.WithContext(c.Request.Context()).
    Where("device_id = ?", uint(id)).      // 无 Limit
    Order("sensor_name ASC, timestamp DESC").
    Find(&rows)                            // 无 timestamp 条件
```

**把该设备全部历史行拉到 Go 内存**，再在应用侧去重取每 sensor 最新一条。
bench2 实测单设备 12,765,394 行。只在缓存 miss 时进这个分支，所以一直没暴露
—— 但**重启后第一次请求**就会进。

与停写无关就该修（加 `DISTINCT ON` 下推 + 时间窗）；已单独报告，不在本清单执行范围。

## 2. 逐接口回归表（演练时逐项打勾）

> 预期结果按「本任务完成后应是」写；**演练时未达标的项就是工作项**。
> 标 🆕 的是本次新增。

| # | 接口 | 预期 | 判定 |
|---|---|---|---|
| 1 | GET /nodes/:id/latest | 200；values 非空，但**节点下设备全软删时允许 null**（脚本查库分辨：活跃设备数=0 即为预期行为） | ☐ |
| 2 | GET /devices/:id/history?hours=1 | 200，点数 **>= 基线**。窗口 1h <= 6h => 走帧表。这是**唯一不带 max_points 的点数判据**（15 MB，可承受），负责抓「数据少一半」 | ☐ |
| 3 | 同上 hours=24&max_points=5000 | 200，点数 **>= 基线**（基线也是 cap 后的）。2026-11-08 已改分段解码（4 段 x 6h）。⚠ 带 cap 后点数恒为 5002，**只能测「有没有」不能测「少没少」**；不带 cap 要下 435 MB，演练不划算 —— 「少一半」由 #2 覆盖。 ⚠⚠ **余量很薄**：29 字段设备 24h = 2,505,600 行，占 300 万行护栏的 **84%**（1.2x 余量）。35 字段以上的设备，24h 就会被 413 拒 —— 那时 #3 会失败，但那是**设备字段数**问题，不是回归 | ☐ |
| 4 | 同上 hours=48 | **413 被内存护栏拒绝**（P0 修复验证）。护栏 = maxHistoryRowsPerRequest = 300 万行（帧 x 字段），见 history_frame_budget.go。 29 字段设备 48h = 172,800 帧 x 29 = 5,011,200 行 > 300 万 => 必然 413。⚠ 这一项断言的**不是**「长窗能返回数据」（那条路已被护栏封死：3,000,000 / 29 / 3600 = 28h，29 字段设备最大只能查 28 小时），而是**护栏真的生效**。返回 200 反而判失败 —— 说明 P0 修复没上线。 ⚠ 默认不发（DRILL_LONG_HISTORY=0）；护栏上线后设 =1 才有意义 | ☐ |
| 5 | **GET /unified-data/historical?agg=change**（变更点模式） | 200，点数 > 0。⚠ /devices/:id/history **完全不读 agg 参数**（全文 0 处），原来的「同上 agg=change」写得不对 ——那个请求和 #3 完全一样，**鉴别力为 0**。真正读 agg 的是 /unified-data/historical；且 change 只对 changeCategories（protection_status / fet_status）生效，用 rsoc 传会落回 auto | ☐ |
| 6 | GET /edge-devices/:id/latest-data | 200，sensors ≥ 1。⚠ sensors 在 **`.data.data_json` 字符串内部**（要 `fromjson` 才看得到），不在 `.data.sensors` | ☐ |
| 7 | GET /edge-devices/:id/data（历史页） | 200，**每行 sensors 非空** | ☐ |
| 8 | GET /edge-devices（列表） | 200，列表非空 | ☐ |
| 9 | GET /overview | 200；**`data_count_today` 不作「正确」判据**（见 §3 第 4 类） | ☐ |
| 10 | GET /metrics/summary | 200；**`points_stored` 不作「正确」判据**（见 §3 第 4 类） | ☐ |
| 11 | GET /sensor-fields?**edge_device_id=** | 200，字段列表非空。**参数名是 edge_device_id 不是 device_pk**（`handler_sensor_fields.go:87` parseEdgeDeviceIDQuery） | ☐ |
| 12 | 图表 30 组（29 字段 × 长短窗） | 全部 200 且**点数与演练前一致** | ☐ |
| 13 | GET /latest-values | 200（缓存路径，不受影响）。⚠ **`.data` 是数组**不是对象 | ☐ |
| 13b | **软删设备在 /nodes/:id/latest 里消失** | 符合预期（有意的行为变更，见 handler_node_latest_cache.go 文件头）；若前端依赖「软删设备仍显示」则是发现的真 bug | ☐ |
| 14 | 重启服务 | 能正常启动（warmup 不依赖 unified_data） | ☐ |
| **15 🆕** | **GET /unified-data/categories** | 200，字段列表非空 | ☐ |
| **16 🆕** | **GET /devices/:id/sensor-data?limit=50** | 200；**点数与演练前一致**（不是只看非空）。 2026-11-08 该接口已接帧表（自适应窗口 1h→6h→24h），并接了内存护栏。⚠ 护栏已按 Lead 要求改成**跟着自适应窗口走**：只有「最小窗口也超限」才 413，较大窗口超限就跳过、继续试下一个 —— 因为本接口契约是「最近 limit 条」，不是「必须覆盖 24h」。 所以 device14 上必然 200（1h 窗口 3,600 帧 x 29 = 104,400 行，占阈值 3.5%）。 演练时此项若 413，说明连 1h 都超限，那是设备字段数极端的情况 | ☐ |
| **17 🆕** | **GET /devices/:id/history?hours=24&max_points=100**（降采样分支） | 200，点数 **<= 102**（上界判据）。`downsampleUnifiedData` 恒定保留**首行 + 尾行**，中间按 step 抽 => 输出最多 maxPoints+2。⚠ 原来写 `?raw=1`（该接口**没有 raw 参数**，被忽略 => 等价 #3），已改 | ☐ |
| **18 🆕** | **GET /unified-data/historical**（raw 分支） | 200；同上。必填 `device_pk` + `category` + `start_time` + `end_time` + `agg=raw`（`handler_data.go:253-263`）。⚠⚠ **2026-11-07 实测：raw 分支没有 rollup 回落，返 0 行；同接口的 batch 分支有回落。同一接口两个分支一个接回落一个没接 —— 已派 impl-l2** | ☐ |
| **19 🆕** | **GET /unified-data/historical-batch（非聚合）** | 200；同上。必填 `device_pk` + `categories` + `start_time` + `end_time` + `agg=raw`（`handler_data.go:343-360`） | ☐ |
| **20 🆕** | **GET /data-reports** | 200；同上 | ☐ |
| **21 🆕** | **GET /data-sources**（**复数**） | 200，列表非空。⚠ 单数 `/api/v1/data-source` 不存在，会走 NoRoute 的 SPA 回退 ⇒ **200 + HTML**（见 §3 第 6 类） | ☐ |
| **22 🆕** | **GET /sensor-fields/resolve?edge_device_id=&selector=** | 200，样本非空。**selector 必填**（`:247`）。⚠ 若返 400 `field resolver is disabled` => 属「**配置未开**」（见 §3.3），不是失败也不是通过 | ☐ |
| **23 🆕** | **三个 sink 存活断言**（§0.3） | **五条判据全中才通过**：① L2 帧表 最近 W 秒有新增；② L3 rollup max(bucket) 前进；③ latest 缓存 前进；④ unified_data 冻结；⑤ **停写计数器 `ehome_unified_writes_skipped_total` 增长**（这条同时证明「开关生效」+「上游有数据」，缺了它 ①③ 也能在开关没开时靠心跳假象凑出 PASS）。 ⚠ **开关证据改为直接观测，不再靠自述**：设 `APP_CONTAINER=<应用容器名>` 后脚本读 `docker inspect` 的 `.Config.Env`（开关在构造时读取 ⇒ 容器当前 env == 进程实际生效值）+ `.State.StartedAt`（证明真的重建过）。读得到 env=false 就直接放行；读不到才回落 `DRILL_CUT_CONFIRMED=1`，且报告标注「证据等级：自述」。 ⚠ **容器已运行 > 4×采样窗口时告警**：那是「改了 compose 但没重建容器」的典型形态，env 是 false 却从未生效。 ⚠ **组合判定已抽成纯函数 `cut_verdict` + 穷举自检 10 组边界**：真实演练一轮几分钟，边界分支永远没人验 —— 而「永远返回 PASS 的判定函数」和「没有判定函数」一样。自检不通过则直接判失败，不给它产出 PASS 的机会。 ⚠ **采样窗口默认 150s**，不是 60s：L1 心跳默认 60s（unified 本来就可能 60 秒不写），L3 rollup 是 60s flush 且**只落已结束的桶**（最坏滞后 120s）。采 60s 会把「正好没到心跳」误判成「开关生效」。 ⚠ 判据全部用 max(时间戳) 而非 count(*)：1700 万行 count 要 3 秒+，max 走索引毫秒级（生产实测执行计划是逐分区 Index Only Scan Backward），且「时间戳冻结」比「行数不变」更精确（并发写入时 count 有跨语句抖动）。 ⚠ **新增「无结论」态**：跑了但证据不足（设备安静 / 读不到判据值）既不是通过也不是失败，单列一态；退出码与「未执行」同档（非 0）。 | ☐ |
| **24 🆕** | **coverage 推进判据**（§0.2） | `max(coverage.day)` = UTC 昨天 | ☐ |
| **25 🆕** | **GET /devices/:id/history?hours=24**（**不传** max_points） | **只记录，不判定**（OBSERVE），且**默认不发请求**。⚠ 这一项本身**就是那个 P0 的形态**：下载 435 MB + 服务端物化约 440 MB 堆。护栏未上线前默认关闭（DRILL_LONG_HISTORY=0），显式设 =1 才测。量级已由 Lead 实测（435 MB 响应 / 439.7 MB 堆），写入本行即可，无需每次复测。产品决策：文档写明「长窗必须传 max_points」，不做服务端静默 cap | ☐ |

## 3. 四类「看起来正常但其实假绿」的形态

演练必须专门盯这四类。前两轮抓到的 bug 都属第 1、3 类。

1. **半截数据**：接口 200、有数据，但比演练前少一段（覆盖判据误判「已覆盖」）。
   判定法：**逐接口比较演练前后的点数**，不是只看非空。
2. **静默回落**：回落目标已 DETACH ⇒ 回落到空表 ⇒ 返回空却不报错。
   判定法：日志搜 `falling back`，演练期间不应出现大面积回落。
3. **字段级缺失**：同一接口某些字段有、某些没有（上一轮 `/edge-devices/:id/data` 就是这样）。
   判定法：**按字段**比较，不是只比总数。
4. 🆕 **数字型假绿 —— 它们是数字，不是空**：接口 200、字段存在、值是合法数字，但那个数字已经错了。
   这是最隐蔽的一类，因为 §2 的「非空」判据对它完全无效。

| 位置 | 停写后表现 | 为什么判「非空」抓不到 |
|---|---|---|
| `handler_overview.go:160` `data_count_today` | 恒为 **0** | 前端 `typeof raw === "number"` ⇒ 0 是 number ⇒ 渲染 `0` 而非 `—`(UNKNOWN)。用户读成「设备全挂了」。**该字段的 D2 注释专门写过「缺字段不得伪装成 0」，但这里字段存在且为 0，那个防护不触发** |
| `handler_metrics.go:137` `points_stored` | **冻结**在 T_stop 的值，60s 缓存继续返回 | 面板显示一个不再增长的数字，`/metrics` 一直 200 |
| `handler_data.go:86` `/devices/:id/sensor-data` | 返回 T_stop 前最后 N 行 | 200 + 有数据 + 时间戳是旧的，**没有「数据止于何时」标记** |
| `history_points.go:319` 历史回落 | `[start,end]` 跨 T_stop ⇒ 只返回 T_stop 之前那段 | 图到 T_stop 就断，横轴还在 |

**第 4 类的判定法**：不看「有没有」，看**时间戳是否覆盖到窗口末端**。
对每个返回时间序列的接口，断言 `max(timestamp) >= window_end - 容差`。

### 3.1 第五层：未执行的项不计入「未执行」计数

写 `scripts/drill_unified_stop.sh` 时发现的第四层之上还有一层：

```
自计式：CHECKS++ 执行到哪算哪
  => 被跳过的项根本不进计数
  => 「未执行 = 0」恒成立
  => 输出「共 12 项，执行 12 项，未执行 0 项，0 失败」——看起来完美
  => 而清单实际要求 25 项
```

**「零未执行」和「零检查」在自计式里同样同形。**

修法：把 EXPECTED 清单**硬编码**（脚本里的 `MANIFEST` 数组），末尾做集合差：

```
未执行 = MANIFEST − 已登记结果的 id
```

并且 `--verify-manifest` 会把 MANIFEST 与本文档 §2 的表格逐条核对 ——
两者脱钩时脚本**拒绝演练**（否则「脚本说 25 项全过」与「文档要求 27 项」会同时成立而没人发现）。

### 3.2 第六类：HTTP 200 但**根本没有 API 响应**（2026-11-07 生产实测）

演练跑完报「16/18 通过」。**那 16 项里至少 9 项是假的**：

| 形态 | 实际发生 | 为什么「只看状态码」抓不到 |
|---|---|---|
| **SPA 回退** | GET /api/v1/data-source（单数，路由不存在）⇒ NoRoute 返回**前端首页 HTML** | 状态码是 **200**。演练把「拿到一个网页」判成「API 正常」 |
| **空数组** | /devices/:id/history 等 8 个端点纯读 unified_data、无任何回落 ⇒ 藏表后 data: [] | 状态码是 **200**，Success(c, []) 完全合法 |

**这两类都满足 code == 200。** 判据只看状态码 ⇒ 全部放行。

#### 修法：四关判定，缺一不可

| 关 | 断言 | 抓什么 |
|---|---|---|
| ① | HTTP 状态 == 200 | 服务不可用 |
| ② | Content-Type 含 application/json | **SPA 回退 / 静态页**（返回 HTML） |
| ③ | body.code（若有）== 200 | HTTP 200 但业务码失败 |
| ④ | 声明路径的「尺寸」>= 下界 | 空数组 / 半截数据 |

第 ④ 关的下界来自**演练前基线**（capture_baseline 在 DETACH 之前采样）：

    ratio=0    => 只要求非空
    ratio=0.8  => 要求 >= 基线的 80%

为什么必须用基线而不是写死下界：写死 100 会在数据增长后失败，写死 1 又退化成
「1 条也算过」。**「点数与演练前一致」这句话只有相对基线才有意义。**

#### 配套：判定逻辑自己也要有负向对照

judge_selftest 在接口回归之前喂 4 个**必须被判失败**的输入：

    SPA 回退返回 HTML          => 必须判失败（关②）
    空数组（数据被藏）          => 必须判失败（关④）
    HTTP 200 但 body.code=500  => 必须判失败（关③）
    非 200 状态                => 必须判失败（关①）

任一被判「通过」⇒ 判定逻辑失效 ⇒ **中止演练**，不产出任何结论。

> **一个永远返回通过的判定函数，和没有判定函数是一样的。**
> 这条是 §3.1「未执行不计入未执行」的姊妹：那里是「没跑 != 通过」，
> 这里是「跑了但判据没判 != 通过」。

#### 一个反面教训

写这段时我先只加了「状态码 + Content-Type」，没加第 ④ 关。跑测试时
「空数组」那个负向对照**被判通过了** —— 因为空数组的 Content-Type 是合法的
application/json。**是负向对照自己发现了这个漏洞**，不是我想出来的。

### 3.3 第三个维度：「配置未开」既不是通过也不是失败

2026-11-07 生产演练里 #22 返 **400**，body 是：

```
sensorfield: field resolver is disabled
```

**配置项没开。** 接口本身没坏，与 unified_data 退休也毫无关系。

如果把它记成「失败」，看报告的人会去查一个不存在的 bug；
如果记成「通过」，那更是假绿（什么都没验）。

所以脚本有**第 5 种状态**：

| 状态 | 含义 | 退出码 |
|---|---|---|
| 通过 | 验了，通过了 | 不影响 |
| 失败 | 验了，没通过 | 1 |
| 未执行 | 请求了但没跑成 | 1（或 --allow-skip 时 2） |
| 未请求 | 本次没要求这个相位 | 2 |
| **配置未开** | **功能没启用，这项没被验证** | 1（或 --allow-skip 时 2） |

**「配置未开」绝不返回 0** —— 它和「未执行」一样是「没有证据」。
区别只在于原因：一个是没法跑，一个是功能压根没开。

> 这和第 3.1 节是同一个原理的第三个面：
> 3.1 说「没跑 != 通过」，3.3 说「跑不了 != 失败」。
> **两种错误都有害**：前者让人误以为安全，后者让人去查不存在的问题。

### 3.4 第七类：**演练工具自己的行为影响了被测系统**（2026-11-08 第三次踩到）

这一条与前六类不同：前六类都是「判据说谎」，这一类是**工具本身在制造故障**。

同一个形态已经踩到三次：

| # | 时间 | 触发方式 | 后果 | 症状 |
|---|---|---|---|---|
| 1 | 11-07 | 基线采集调 `/metrics/summary`（全表 GROUP BY 33.8s） | 持 AccessShareLock 5 分 37 秒，把 DETACH 堵死 | **卡住**（可见） |
| 2 | 11-07 | 脚本自己的只读查询没有 `statement_timeout` | 挂进锁队列，挡住写者 | **卡住**（可见） |
| 3 | 11-08 | 基线采集按原样请求 `hours=24`/`hours=168` | 下载 7 GB；服务端物化 3 GB 堆 | **慢/占资源**（不可见） |

**共同形状：工具的正确性判据只覆盖了「被测系统」，没有覆盖「工具自己」。**

为什么第 3 次最危险：

- 前两次是「卡住」—— 演练跑不动，立刻发现；
- 第三次只是「慢」和「占内存」—— 演练**照常跑完、照常出全绿报告**，
  但它在生产上制造了一次 3 GB 堆分配。
- 那 3 GB 恰好就是我们要验证的那个 P0（§5.3）。

#### ⚠ ⚠ 「不判定」不等于「不执行」——这是最容易搞混的一点

2026-11-08 Lead 建议「#4 先只记录不判定（OBSERVE）」，隐含假设是「记录 = 安全」。
实际上**「记录」和「发请求」是两件事**：

| 改动 | 改了判定口径 | 改了请求行为 | 降低风险 |
|---|---|---|---|
| 把项改成 OBSERVE | ✓ | ✗ | **✗** |
| 加 `DRILL_LONG_HISTORY=0` 闸 | ✗ | ✓ | ✓ |

`run_observe_check` 里仍然是 `curl` —— 请求照发，服务端照解 3 GB 堆，
只是报告里不判它失败。**风险一点没减，只是看不见了。**

同族错误：

```
「不判定」  != 「不执行」        -- 观察项仍然会发请求
「观察项」  != 「安全项」        -- 不判失败 != 不造成伤害
「判据覆盖了」!=「覆盖范围足够」  -- 断言存在 != 断言有效（8.2）
「未执行」  != 「通过」          -- 没跑 != 没问题（3.1）
「跑不了」  != 「失败」          -- 配置未开 != 接口坏了（3.3）
```

这五条的共同结构：**两个概念被同一个词/同一个动作混淆了。**
防法不是「更小心」，而是**把两件事显式拆成两个独立的东西**：

- 判定口径（PASS/FAIL/SKIP/CONFIG/OBSERVE）
- 执行行为（发不发请求、发多大、什么时候发）

本脚本现在把它们拆开了：OBSERVE 管前者，`DRILL_LONG_HISTORY` 管后者。

#### 判据

写任何演练/压测/巡检脚本时，除了「被测系统是否符合预期」，还必须问：

```
① 这个动作会不会改变被测系统的状态？   （会 => 要不要先备份/后恢复？）
② 这个动作的资源开销是多少？           （请求量 x 响应大小 x 服务端物化量）
③ 这个开销是否在「低峰期跑」就能接受？ （低峰 != 空闲）
④ 如果它把系统搞挂了，演练能发现吗？   （发现了叫事故，没发现叫假绿）
```

第 ④ 问是关键：**如果工具造成的故障不在工具的判据里，那它就是隐形的。**

#### 具体到本脚本的三条护栏

```
# 1) 会拉大响应的项，默认不发（DRILL_LONG_HISTORY=0）
#    #4 hours=48      服务端约 894 MB 堆
#    #25 hours=24 无cap 下载 435 MB + 堆 440 MB

# 2) 能 cap 的加 cap（#3 hours=24&max_points=5000 => 下载 700 KB）
#    但要记住 cap 只减响应体，不减解码堆

# 3) 会触发重计算的项不采基线（NO_BASELINE_IDS = 4 9 10 25）
#    基线采集本身就是「把 22 个接口串一遍」，它自己就是一个负载源
```

> **一个只验证被测系统、不验证自己的演练脚本，本身就是一次故障注入 ——
> 只是没人给它写断言。**

## 4. 不在本次演练范围

- DROP 分区（演练只 DETACH）
- 重算 rollup 历史精度：当前 rollup 历史是**收窄后口径**（从 unified_data 聚合），
  而 live / 帧表重建是全采样口径。修正需单独立项用帧表重算，**不在本轮混做**，
  否则无法归因。
- `/edge-devices/:id/latest-values` 的无界全表扫（§1.3）：与停写无关，已单独派任务。

## 5. 可逆性（本次新增，第一版此处是悬空引用）

第一版 #16 写「帧表重建（见可逆性证明）」，但全文没有这一节。补上。

**结论：帧表能重建 rollup，且比现在从 unified_data 重建的更准。**

```
rollup 需要：Avg / Min / Max / Last / Count
帧表产出：  Point{DeviceID, TS, Seq, Name, Value}
=> 按 (device, 分钟, Name) 分组即可算出全部 5 个量 ✓

且帧表在 L1 门控**之前**（全采样），unified_data 在门控**之后**（收窄行）
=> 从帧表重建的 Avg/Count 与 live collector 同口径，比现状更准
```

**读路径只用 Last**（`rollup_read.go:334,352` 只取 `stats[i].Last`；
`Avg/Min/Max/Count` 在非测试代码里零读者）。所以 correctness 门槛只有一条：
**Last 必须对**，而 Last 在三种源下天然一致（L1 只跳过值未变的行）。

**两条会咬人的限制**：

| 限制 | 内容 | 后果 |
|---|---|---|
| A. 窗口 = 帧表保留期 = 45 天 | 帧表与 unified_data 共用同一个系统级 `EHOME_DATA_RETENTION_DAYS`；rollup **不在**保留期清单（永久保留） | 停写后 45 天内可重建；**45 天后 rollup 是唯一副本，不可重建** |
| B. 必须按 batch 解码，不能按天 | `MaxDecodeFrames = 32768` 硬编码不可配；单设备单日实测 56k~86k 帧；`decodeRangeBounded` 超限时**不返回 error**（`decode_anchored.go:129-138`） | 按「天」解码会静默丢数据；按 6h 段（`framesBatchLimit`）在 1 Hz 下约 21.6k 帧，余量 1.52x。**但保留哪一段与我们先前写的相反，见 §5.1** |

**停写前必须先做的两件事**（否则「可逆」是纸面的）：

1. `rollup_backfill` 的单元枚举改为不依赖 `unified_data.max(ts)`（见 §0.2）；
2. live collector 也写 coverage（解决「当天漏几分钟、次日补不上」的现有残余风险）。

### 5.1 ⚠ 实测更正：截断保留的是**最早**那段（丢尾巴），且计数器恒报 1

**先前此处写的是「只保留最后 32768 帧」（= 丢头部）。用探针实测后是反的。**
原因在查询方向：

```
queryFrames(..., limit = maxFrames + 1)   // ORDER BY ts ASC, seq ASC LIMIT 32769
  => 拿到的是窗口**最早**的 32769 帧（不管窗口里实际有多少）
decodeRangeBounded:
  frames = frames[len(frames)-maxFrames:]  // 保留「已取到那批」的尾部
  => 仍落在窗口**开头**
```

探针实测（500 个 1 Hz 采样，值 0..499，`maxFrames=100`，窗口 13:11:53..13:20:13）：

```
产出点数=100   首点值=1   末点值=100
点覆盖 13:11:54 .. 13:13:33   ← 只有窗口的前 100 秒
真实丢弃 400 帧，stats.FramesSkippedBrokenChain = 1
```

两个后果都要记：

1. **保留窗口开头、丢窗口结尾。** 对图表是「后半段凭空消失」，比丢头更不容易认出来
   —— 横轴还在，只是后一半没数据。
2. **计数器结构性封顶在 1。** `LIMIT maxFrames+1` 决定了
   `len(frames) - maxFrames <= 1`，所以无论实际丢弃 400 帧还是 40 万帧都只能报 1。
   **它无法用来发现截断。** 而且这个值被加进了 `FramesSkippedBrokenChain`（断链 = 数据损坏），
   归类也是错的 —— 看指标的人会以为是数据坏了。

所以「6h 段安全」这个结论**只靠余量成立，代码里没有任何护栏**：

| 采样间隔 | 6h 帧数 | 离 32768 |
|---|---|---|
| 1.0 s | 21,600 | 1.52x（bench 实测 dev14 = 21,585，吻合） |
| 0.8 s | 27,000 | 1.21x |
| 0.7 s | 30,857 | 1.06x |
| 0.65 s | 33,230 | **超限 ⇒ 静默丢后半段** |

且 `KeyFrameAnchor` 会把解码起点再往前拉到最近关键帧（默认 60s），
所以真实帧数是 `6h + 60s` 的量，上表还要再紧一点。

**演练时必须加的判据**：任意设备在任意 6h 段内的帧数 `< 32768`（留 1.3x 余量则 `< 25000`）。
这条要写进 drill 的 `--phase=time`：它是**唯一**能在截断发生前发现它的办法。
### 5.2 ⚠ 长窗（>6h）要**分段解码**，不是走 rollup（2026-11-07 定案）

帧表直读有硬窗口（生产 `EHOME_TELEMETRY_FRAME_DECODE_MAX_WINDOW=6h`，代码默认 1h）。
超过这个窗口，当前实现回落 unified_data —— 那正是要退休的东西。

#### 方案：分段解码

```
24h = 4 段 × 6h，每段独立解码，再按时间拼接
6h × 1Hz = 21,600 帧 < MaxDecodeFrames=32,768  =>  单段安全（1.52x 余量）
```

**为什么不用 rollup**：rollup 是 1 分钟聚合。走它意味着**长窗返回聚合点、短窗返回原始行** ——
同一个接口两种粒度，前端无法区分。分段解码保住「原始样本」这个契约。

**分段的代价**：24h = 4 次解码 + 4 次查询。换来语义一致，值得。

**分段的前提**：每段都不撞 `MaxDecodeFrames`。1Hz 下 6h 已经 21,600 帧（余量 1.52x），
采样频率一旦提高就会撞线 —— 撞线时 `decodeRangeBounded` **不报错、静默丢尾部**
（且计数器恒报 1，见 §5.1）。所以分段长度不能盲目加长。

#### 判据：短窗比数量（>= 基线），长窗比覆盖范围

**短窗（<= 6h）**：点数 **>= 演练前基线**，不是「一致」。

```
#2 hours=1  实测 .data = 104,081   vs 基线 58,333   => 涨 78%
```

原因是**数据源变了**：帧表是**全采样**（L1 收窄之前），unified_data 是收窄后的行
⇒ 帧路径点数本来就更多。**这是数据变好了，不是变差。**

判据若写成「一致」，会在改进发生时报警 —— 那和假绿一样有害：
它会训练人忽略这条告警。所以：

```
点数 < 基线   => 真丢数据 => 失败
点数 >= 基线  => 通过，但把增量打出来（可追溯，不静默放行）
```

（同一判据用在恢复核验①：`行数 >= 演练前`。同一个道理。）

**长窗（> 6h）**：比**时间跨度**，不比点数。

断言 `max(timestamp) - min(timestamp)` 覆盖整个查询窗口（允许少量容差），
以及点数与「窗口内采样数」量级相符。

为什么不能比点数：分段解码后点数取决于段边界与采样密度，
而 baseline 是 unified_data 收窄后的行数 —— 两个数本来就不同量纲。

> **一个接口两种粒度，必须在响应里能区分。**
> 若最终采用 rollup（而非分段），必须加 `granularity: "raw" | "1m"` 之类的字段，
> 或复用 `max_points` 语义（传了就说明接受降采样）。
> 否则前端无法判断拿到的是原始点还是聚合点。
> 这条是产品侧要求，不在本清单的验证范围内。


### 5.3 ⚠ 分段解码**没有分段释放** —— 168h 仍是 3 GB 堆（2026-11-08 发现）

`frameHistoryRowsDownsampled` 的第一步就是 `decodeHistoryByName`（**全量**解码）：

```
byName, units, ok := decodeHistoryByName(ctx, db, deviceID, sensorName, start, end)  // 全量
total := 0
for _, pts := range byName { total += len(pts) }
if maxPoints <= 0 || total <= maxPoints { return assembleAllRows(...), true }
// 只有这里之后才是「按 maxPoints 抽样」
```

**所以 `max_points` 只减响应体，不减解码堆。**

按 Lead 实测的 24h => 439.7 MB / 2.46M 行（约 187 B/行）估算：

| 窗口 | 行数 | 解码堆 | 响应体（无 cap / cap=5000） |
|---|---|---|---|
| 1h  | 10.4 万 | 约 19 MB | 15 MB / - |
| 24h | 246 万 | 约 440 MB | 435 MB / 约 700 KB |
| 168h | 1720 万 | **约 3 GB** | **约 3 GB** / 约 700 KB |

这意味着：

- 前端调 24h+max_points=500 时，**服务端仍要解 440 MB 堆**，只是响应小；
- 168h 任何请求（带不带 max_points）都会解 **3 GB 堆** —— 这在容器里可能直接 OOM；
- 分段解码解决的是 `MaxDecodeFrames` 截断（§5.1），**没有**解决峰值内存。

正解是**分段解码 + 分段组装 + 分段释放**（或流式输出），不是加 cap。
`max_points` 只能优化传输，不能优化内存。

#### ⚠⚠ 生产实测：这是**活的 DoS**，不是性能优化项（2026-11-08 Lead 升级为 P0）

```
容器内存限制    Memory=0（**无限制！**）
主机总内存      4.0 GiB
主机当前可用    2.9 GiB
容器当前用量    333.9 MiB
```

| 窗口 | 行数 | 解码堆 | 主机可用占比 |
|---|---|---|---|
| 1h | 10.4 万 | 19 MB | 0.6% |
| 24h | 246 万 | 440 MB | 15% |
| 48h | 501 万 | 894 MB | 30% |
| 168h | 1720 万 | **3.1 GB** | **107%** |

**168h 请求 => 1720 万行 ≈ 3 GB 堆，主机可用只有 2.9 GiB。**

- 容器 `Memory=0`（无限制）=> OOM 会打到**主机**，不是容器；
- PostgreSQL 在同一台机 => 可能被一起杀掉 => **整站不可用**；
- **一个有效 token 就能触发** —— 不需要管理员、不需要特殊参数，只是 `hours=168`。

所以：`max_points` 只能优化**传输**，优化不了**内存**。
正解是「分段解码 + 分段组装 + 分段释放」或流式输出，并加**解码帧数护栏**。

#### 对演练的影响（已经改了）

```
#4 从「判定项」改成「观察项」，且**默认不发请求**：
  DRILL_LONG_HISTORY=0（默认）=> 只打印「未发请求：护栏未上线」
  DRILL_LONG_HISTORY=1        => 发 hours=48 请求，只记录耗时与量级
```

**关键区分：OBSERVE 只改「判定口径」，不改「请求行为」。**
把 #4 改成观察项本身**不会**降低 OOM 风险 —— 必须默认不发。
这两件事很容易搞混，写在这里。


