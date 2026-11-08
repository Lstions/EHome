# unified_data 残留读点清单（2026-11-08 DROP 后）

> 按**处置动作**分类，不按文件位置。同一个文件里的 4 处可能是 4 个不同动作。
>
> 生成方式：语法位置分类（判断 `unified_data` 前紧邻的 SQL 关键字）+ 人工核实间接形态。
> 全部 `unified_data` 字符串出现 **1,000 处**，其中 **99% 是注释/标识符/日志文案** ——
> 这就是「grep 数命中」这个指标本身无意义的原因。

## 统计

```
全部 unified_data 出现        1,000 处
  纯注释行                     662 处
  Go 标识符/常量                94 处
  日志/错误文案                 94 处
  SQL 表名（字面量直写）         10 处  ← 生产（非测试）
  测试代码 SQL                  48 处
```

生产真读点 = **10 处 SQL 字面量直写 + 2 类「表名先进变量再拼 SQL」形态 = 12 处**。

## ⚠ 「12 处」的可靠性边界（必读）

这是一个**开放集合**的计数 —— 找法的穷尽性取决于「枚举了哪些维度」。
所以正确表述是「**至少 12 处，按下列维度找的**」，不是「全部 12 处」。

**已枚举的维度：**

```
① SQL 字面量紧邻关键字
   判断 unified_data 前面紧邻的 FROM / JOIN / INTO / UPDATE / TABLE / DELETE FROM
   => 覆盖 10 处

② 表名先放变量、再 fmt.Sprintf 拼进 SQL
   形态：for _, table := range []string{"unified_data", ...} { fmt.Sprintf("...%s...", table) }
   => 覆盖 2 类（handler_edge_device.go:2252 / row_estimate.go:65,87）
```

**未枚举的维度（可能漏）：**

```
③ ORM 链式 API 传变量：db.Table(name) / db.Model(x).Table(y)
   —— 我的分类器看到的是变量名，不是表名字面量 => 会判为 OTHER

④ 存储过程 / 数据库函数体内的动态 SQL
   —— 不在 .go 文件里，本次完全没扫

⑤ 拼接后再拼接（表名进 A 变量，A 进 B 模板，B 才进 SQL）
   —— 我只看「本行 + 前 3 行」，跨函数的传递会漏
```

**这一栏的意义**：它把「可能漏」变成「已知会漏什么」。
下一个人发现第 ③ 类时，知道该往哪个维度加，而不是从头重做整个分析。

**为什么这次没漏掉关键点**：实际发现的 6 处生产 42P01 全部落在维度 ①② 内
（DROP 后日志暴露的），所以「至少 12 处」这个下界已经覆盖了「实际在跑的」全部。
而维度 ③④⑤ 若存在读点，它们**未被触发过** —— 那正是本清单的 A/B 类价值：
在用户碰到之前先修掉。

## A 类：必须加存在性前置（用户可见失败，无守卫）

### A1 `backend/internal/api/handler_edge_device.go:2252`

```go
for _, table := range []string{"unified_data", "device_data"} {
    fmt.Sprintf(`UPDATE %s SET device_id = ? WHERE id IN (SELECT id FROM %s WHERE device_id IN ? LIMIT ?)`, table, table)
```

- **路径**：迁移设备归属（merge-history / transfer-device）的分批 UPDATE，属主路径
- **失败形态**：表不存在 => UPDATE 报 42P01 => 整批事务回滚 => 返回 500
- **用户可见**：「已迁移的行保持迁移，重试是幂等的」—— 用户看到迁移失败
- **守卫现状**：无
- **处置**：进入循环前 `Migrator().HasTable(table)`，不存在则跳过并记一次 Warn（说明为什么跳过）
- **注意**：`device_data` 仍存在，所以不能整段删；必须**逐表判断**

### A2 `backend/internal/datalifecycle/row_estimate.go:65` 与 `:87`

```go
for _, table := range []string{"unified_data", "device_data"} {
    n, ok := estimateTable(ctx, db, table, cond, args)
    if !ok { return 0, false }   // ← 这里
}
```

- **路径**：`EstimateDeviceRows`（handler_edge_device.go:1536/1858）、`CountDeviceRowsCapped`（:1532）
- **失败形态**：`EXPLAIN SELECT id FROM unified_data` 报 42P01 => `estimatePostgres` 回落到
  `estimateTruncatedCount` => 同样失败 => 返回 `ok=false`
- **用户可见**：删除设备 / 迁移设备时「预计影响 N 行」不显示（功能失效，非报错）
- **守卫现状**：无（`ok=false` 被上层当成「不知道」而静默）
- **处置**：清单改成动态构造 —— 先 `HasTable` 过滤，再遍历
- **为什么不是「直接删 unified_data 这一项」**：迁移期的历史库仍需要它（见 B 类同款论证）

## B 类：必须删（对象永久不存在，且无替代语义）

### B1 `backend/internal/api/handler_edge_device.go:2675 / 2742 / 2744 / 2773`

这 4 处是**两个函数**的两个兜底：

```go
latestValuesMissRows         SQL: SELECT ... FROM unified_data WHERE device_id = ?
latestValuesMissSensorNames  递归 CTE + DISTINCT sensor_name FROM unified_data
```

- **前置**：`latestValuesMissRowsFromFramesOrDB` 先调 `LatestRowsFromFrames`，
  只有它返回 `ok=false` 才落到这里
- **`ok=false` 的全部触发条件**（我逐个核了 LatestRowsFromFrames 的实现）：
  ```
  db == nil || deviceID == 0
  frameDictionaryFor(db) == nil          ← slot 字典不可用
  max(ts) 查询失败或返回 NULL           ← 该设备在帧表里没有任何帧
  DecodeRangeAnchored 报错或解出 0 点
  ```
- **⚠ 曾写过一个错论证，留此记录**：初稿写的是「`ok=false` 意味着帧表路径整体不可用，
  整个页面都没数据 => 兜底走不到 => 可以删」。**这个前提是错的。**
  `max(ts)` 返回 NULL 是**对单台设备**成立（该设备从没写过帧 / 帧已被回收），
  此时其他设备完全正常 —— 所以这条兜底**会被这台设备触发**。
  而 `TestLatestValuesMissOfflineDeviceStillReturnsLastValues`（造 9003 只写 unified_data、
  不写帧表）就是这个触发条件的现成证明。
  **教训：用「机制不会被触发」论证「删掉它无害」时，必须证明前者 —— 而穷举触发条件永远有漏。**
  更强的论证形式是「它在时和不在时，用户看到的一样」。
- **正确的删除理由（不依赖触发条件）**：`unified_data` 已 DROP，所以这 4 处走到时**必然 42P01**
  => 上层 `logger.Warnf` + `rows` 为 nil => 用户看到「—」（空值）。
  而如果直接 `return nil, nil`，用户也看到「—」。
  **两者用户可见结果完全相同，区别只是有没有一条必现的 42P01 日志。**
- **处置**：删掉这 4 处 + `latestValuesMissRows` / `latestValuesMissSensorNames` 两个函数，
  让 `latestValuesMissRowsFromFramesOrDB` 直接 return `LatestRowsFromFrames` 的结果
  （`ok=false` 时返回空 + nil error，语义是「没有数据」，诚实）
- **⚠ 连带（这是「删一整个功能」，不是「删几行残留」）**：
  `handler_edge_device_latest_values_miss_test.go` 有 5 处直调这两个函数，
  且该文件 `:202` 自己 `AutoMigrate(&models.UnifiedData{})` —— 那个自建表本身就是信号：
  **「这个文件需要 unified_data 才能跑」= 「它测的是 unified_data 分支」** => 整个文件删
- **必须留下的退役记录**（写进代码注释，不是只写这里）：
  ```
  这个能力（离线设备仍显示最后已知值）曾经存在，2026-11-08 随 unified_data DROP 退役。
  如果将来需要它，正确做法是**扩展帧表**（让帧解码能枚举该设备的全部 sensor_name），
  而不是把 unified_data 加回来。
  ```
  理由：下一个人遇到「离线设备显示 —」时第一反应是「加个兜底」，而兜底对象已不存在 ——
  他可能会去重建一张表。

### B2 `backend/simulation/harness/fixture.go:284 / 299`

```go
"SELECT value FROM unified_data WHERE device_id = $1 AND sensor_name = $2 ..."
"SELECT count(*) FROM unified_data WHERE device_id = $1 AND sensor_name = $2",
```

- **路径**：仿真自检（`harness.Env` 的「哨兵上报后校验」与「统计」）
- **生产影响**：**零** —— `backend/simulation/` 不被 `cmd/server` 引用（我核过），
  只在 `-tags=simulation` 构建或测试里用
- **处置**：看它是否还在用：
  - 若仿真仍需要「哨兵值落库」这个自检 => 改成查 `telemetry_frame`（解帧后比对），
    或改成先建一张最小 `unified_data` 表（仿真环境自建，不依赖 AutoMigrate）
  - 若该自检已无意义 => 删
- **优先级**：低（不影响生产），但属于「同一形态的清理」

## C 类：已有守卫，只需确认（无需改动）

### C1 `backend/internal/datalifecycle/rollup_backfill.go:345 / 377`

```go
for _, q := range []struct{ table, sql string }{
    {"unified_data", "SELECT DISTINCT device_id FROM unified_data"},
    {TelemetryFrameTable, "SELECT DISTINCT device_id FROM telemetry_frame"},
    {TelemetryRollupTable, "SELECT DISTINCT device_id FROM telemetry_rollup_1m"},
} {
    if !r.db.Migrator().HasTable(q.table) { continue }   // ← 守卫
```

- **守卫形态**：`HasTable` 前置（结构性判据，探测一次，微秒级）
- **⚠ 重要**：这个守卫是 **l2 工作区新增的，还没提交/部署**。
  部署版 `e7d71902` 里还是旧的写法（`for _, q := range []string{...}` + `.Raw(q)` 不判存在性），
  所以生产日志里的 `rollup_backfill.go:337/365` 是**部署版的行号**，对应工作区的 345/377。
  不是第三处新读点。

### C2 `backend/internal/datalifecycle/rollup_backfill.go:512`

```go
"FROM unified_data " +   // aggregateBatch
```

- **守卫形态**：调用方 `.go:600` 在 `if source == RollupSourceFrames { ... } else { aggregateBatch(...) }`
  的 else 分支里，而 `source` 只可能在 `unifiedDataTablePresent()` 为真时被设成 `RollupSourceUnified`
- **守卫是「数据流」的，不是就近的 `if`** —— 这是 C 类里唯一一处需要**读懂跨函数数据流**才能确认的

### C3 `backend/internal/datalifecycle/rollup_backfill_frames.go:146`

```go
"SELECT count(*) FROM (SELECT 1 FROM unified_data ...
```

- **守卫形态**：函数注释写明「前置条件：调用方必须**先**确认 unified_data 表存在」，
  调用方 `.go:567` 是 `if r.unifiedDataTablePresent() { hasUnified, err := r.dayHasUnifiedRows(...) }`
- **这是「文档化前置条件」，不是强制门禁** —— 若将来有人加了新调用方就会踩
  但那时 `dayHasUnifiedRows` 会返回 error（它不吞错），会被发现

## 汇总表

| 类 | 位置 | 动作 | 用户可见 | 优先级 |
|---|---|---|---|---|
| A1 | handler_edge_device.go:2252 | 加 HasTable 前置 | 迁移设备失败 | **P0** |
| A2 | row_estimate.go:65/87 | 改动态清单 | 估算功能失效 | P1 |
| B1 | handler_edge_device.go:2675/2742/2744/2773 | 删 4 处 + 2 函数 + 5 处测试调用 | 无（走不到） | P1 |
| B2 | simulation/harness/fixture.go:284/299 | 改查帧表 或 仿真自建表 | 无（仿真） | P2 |
| C1 | rollup_backfill.go:345/377 | 已加，等部署 | — | — |
| C2 | rollup_backfill.go:512 | 已有（跨函数数据流） | — | — |
| C3 | rollup_backfill_frames.go:146 | 已有（文档化前置） | — | — |

## 与「生产日志 6 处」的对应关系

```
生产日志暴露（部署版 e7d71902）：
  device_data_sensors_frames.go:314   57 次   ← l2 已修（未部署）
  latest_values_warmup.go:114          1 次   ← l2 已修（未部署）
  handler_overview.go:164              1 次   ← l2 已修（未部署）
  handler_edge_device.go:441           1 次   ← l2 已修（未部署）
  rollup_backfill.go:337               1 次   ← l2 已修（未部署），对应工作区 345
  rollup_backfill.go:365               1 次   ← l2 已修（未部署），对应工作区 377
```

**这 6 处全部在工作区已修**。剩下的 A/B 类 8 处**不在日志里**，因为：

```
A1 迁移设备归属       —— 只在用户点「迁移」时触发（没点过）
A2 行数估算           —— 只在用户点「删除设备」时触发（没点过）
B1 /latest-values 兜底 —— 前置是「帧表整体不可用」，当前帧表可用 => 走不到
B2 仿真自检           —— 生产不引用
```

**这就是静态分析 vs 运行期日志的互补性**：

```
静态分析（12 处）：找到「代码里会发 SQL 的地方」—— 上界，可能含走不到的
运行期日志（6 处）：找到「实际被触发的」      —— 下界，漏掉没被触发的

两者交集 = 日志那 6 处。
差集 = A/B 类 8 处 = 「还没被用户触发的真问题」。
```

## 关于门禁形态的一句话

**不要用静态门禁检查这 12 处。** 理由：

```
今天所有真 bug 的形态都是「写了检查但检查本身失效」：
  · continue 被挪到 EnsurePartitionsFor 之后
  · 空 unit 被当成「没查到」
  · frameDecodeKey 缺 dict

静态门禁检查的是「代码里有没有某种字面量」，它抓不到「检查失效」。
```

**可行的门禁**：gorm callback 捕获所有 SQL + 全端点打一遍 + 断言无 `42P01`。
它检查「实际执行了什么」，而不是「代码看起来怎样」。

漏法：只有「没被任何端点触发」的路径 —— 而那等于死代码，可接受。