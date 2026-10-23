# 新版本能力利用分析（Go 1.27.3 / 依赖 / 前端）

> 日期：2026-10-23 ｜ 前置：[升级执行报告](../依赖升级盘点/执行报告.md)
> 方法：**所有性能数字均由本机实跑 benchmark 得到**（go1.27.3，AMD Ryzen 5 5600），
> 语义断言均用最小可运行程序验证。分报告见 [go127-stdlib.md](go127-stdlib.md)、[go-deps.md](go-deps.md)、[frontend.md](frontend.md)
> **未改动仓库任何业务代码**；所有探针在 `.tmp-deps/` 下。

---

## 0. 关于「backend 0 个泛型 = 设计质量差」这个判断

> **这个推断不成立，但仓库确实有真实短板——只是短板不在泛型上。** 详见 [backend-quality.md](backend-quality.md)。

**硬数据（我直接在 `$GOROOT/src` 实测）**：

| 口径 | 泛型函数 | 非泛型函数 | 泛型占比 |
|---|---|---|---|
| 标准库全部（排除 testdata） | **359** | **40,837** | **0.87%** |
| 仅公开 API | **228** | **16,065** | **1.40%** |

而最核心的包 **泛型全为 0**：`io`、`fmt`、`time`、`context`、`strings`、`bytes`、`sort`、`encoding/json`、`crypto/tls`。`net/http` 全文只有 2 个泛型，且都是**未导出的内部类型**。

> 如果"泛型数量=质量"，那 Go 标准库核心就是最差的设计——这显然荒谬。

**本仓库的质量实测（反而相当扎实）**：

| 指标 | 实测值 | 说明 |
|---|---|---|
| 测试/生产代码比 | **101,838 / 94,913 = 1.07** | 标准库是 0.28，本仓是它的 **3.8 倍** |
| 测试覆盖率 | **71.0%** | statements |
| data race | **0** | `go test -race` |
| 单方法接口占比 | **58.5%**（24/41，含内嵌接口则 68.3%） | 标准库 414 接口单方法占 46.9% |
| 函数长度中位数 | **13 行** | >100 行的仅 3.4% |
| `err.Error()==` 比较 | **0 处** | 用 `errors.Is/As` 185 处、`%w` 519 处 |
| 依赖方向 | **0 环**，models 零反向依赖 | 最长链 9 层 |
| `panic` | 16 处，**11 处在 init/构造器，0 处在请求路径** | 用法正确 |

**但确实有三个真实短板**（这才是建设性结论）：
1. **分页逻辑已分裂成 3 套互相矛盾的默认值**：api 层 12 处逐字重复用上限 200，[datasource/service.go:383](backend/internal/datasource/service.go#L383) 用 100，另有 `parsePositiveInt` 第 3 套。同一请求可能被两层用不同上限归一化——这是真实契约风险。
2. **`cmd/server` 覆盖率仅 21.0%**，而 [main.go](backend/cmd/server/main.go) 里记录过"sourceGroupSvc 漏传导致来源组全 503"的真实事故——正是装配层无测试的代价。
3. **HEAD 上有一个稳定失败的测试**（见下方 §4.1）。

**泛型真正该用的地方**（不是"为了用而用"）：
- 分页样板（14 处，可做成 `parsePagination[T]` 或泛型方法）
- 71 处 `ShouldBindJSON` 的绑定样板
- 而 **8 个 Registry/Cache 不建议泛型化**——逐个看字段是两级 map、双向索引、原子快照、多级异构 map，是带业务语义的索引而非通用容器（backend-quality.md 给了逐条理由）

---

## 0.1 最重要的一条：Go 1.27 的 json/v2 已经在跑

**这条最容易被误读成"待办"，实际是"已完成"。**

Go 1.27 起 `encoding/json`（v1 API）**默认由 v2 实现**，不需要任何迁移动作。验证：

```bash
$ go list -f '{{.GoFiles}}' encoding/json | grep v2_
v2_decode.go v2_encode.go v2_indent.go v2_inject.go v2_options.go v2_scanner.go v2_stream.go
```

对比 `GOEXPERIMENT=nojsonv2`（旧实现），用本仓库真实模型 `models.UnifiedData` 实测：

| 负载 | 默认（v2 实现） | nojsonv2（旧实现） | 结论 |
|---|---|---|---|
| Unmarshal 50 行 | 59,989 ns / 108 allocs | 102,518 ns / 163 allocs | **快 41%** |
| Unmarshal 1000 行 | 1,144,163 ns / 2013 allocs | 1,957,304 ns / 3018 allocs | **快 42%** |
| Marshal 50 行 | 26,554 ns / **2 allocs** | 38,115 ns / **101 allocs** | **快 21%**，allocs **102→3** |
| Marshal 1000 行 | 508,636 ns / **2 allocs** | 696,389 ns / **2001 allocs** | **快 27%**，allocs **2002→3** |
| Marshal 纯 map（WS payload） | 1,624 ns / 18 allocs | 1,036 ns / 14 allocs | **慢 57% ⚠** |
| Marshal 极简 6 字段 struct | 535 ns / 3 allocs | 312 ns / 2 allocs | **慢 71% ⚠** |

### 结论有三层，别只看第一层

1. ✅ **Unmarshal 一律快 40%+** —— 覆盖所有 API 请求解析、gorm 结果反序列化。
2. ✅ **大 payload 的 Marshal 更快，且 allocation 从 O(n) 降到 O(1)** —— 1000 行时 2002→3 allocs。
   这个收益比 ns 数字重要得多：GC 压力降一个数量级。历史数据/列表端点直接受益。
3. ⚠ **小 payload 的 Marshal 反而变慢** —— 这是**净新增的性能回归**，官方 release notes 只说 "broadly at parity"，没提这个不对称性。crossover 在约 2~5 条记录之间。

**为什么第 3 层对 EHome 特别重要**：本仓库 WebSocket 广播**全部是 `map[string]interface{}` 小 payload**（20 处 `BroadcastEvent` 中 15 处传 map），而 map 路径本身就是最慢的形态。两个劣势叠加，正好打在每秒都在跑的 `data_update` 热路径上。

---

## 1. 落地动作清单（按收益排序）

| # | 动作 | 落点 | 实测收益 | 成本 | 风险 |
|---|---|---|---|---|---|
| **1** | **WS 热路径 map → struct** | [consumers_heavy.go:530](backend/internal/databus/consumers_heavy.go#L530)（最高频）、[:450](backend/internal/databus/consumers_heavy.go#L450)、[ota.go](backend/internal/ota/ota.go) 5 处 | **2,900→2,044 ns（快 30%）**，allocs **20→13** | 小 | 低 |
| **2** | **接入 goroutineleak profile** | [main.go:698](backend/cmd/server/main.go#L698) 起独立 loopback debug server | 泄漏 goroutine 连同调用栈直接报出 | 小 | 中低 |
| **3** | **统一 23 处旧式 atomic** | deviceinit / handler_data / sender / pendingwrite / ota / device_mutex | 编译期防护 + 统一已分裂风格 | 小 | 低 |
| **4** | **严格绑定推广到 65 端点** | [bind_json_strict.go:35](backend/internal/api/bind_json_strict.go#L35) 扩成泛型方法 | 消除 60+ 行样板，堵"假成功"缺陷类别 | 中 | **中高** |
| **5** | **修 TrustedProxies**（安全） | [main.go](backend/cmd/server/main.go) 加 `SetTrustedProxies` | 堵限流绕过 | 小 | 低 |

### 动作 1：WS map → struct（最高性价比）

本仓库 WS 事件全是 map payload，最快的一处是 `data_update`：

```go
// 现在
m.wsHub.BroadcastEvent(events.DataUpdate, map[string]interface{}{...})
// 改后
type dataUpdateEvent struct {
    EdgeDeviceID uint    `json:"edge_device_id"`
    SensorName   string  `json:"sensor_name"`
    Value        float64 `json:"value"`
    // ...
}
m.wsHub.BroadcastEvent(events.DataUpdate, dataUpdateEvent{...})
```

**附加收益（比性能更重要）**：struct 让字段名写错在**编译期**暴露。当前 map 字面量写错 key 会**静默失败**——前端收不到数据、后端无任何报错。本仓库已经因这类"假成功"吃过事故（见 `bind_json_strict.go` 顶部注释记录的 `PUT /log-config` 事故）。

**建议先只改 530 这一处**，benchmark 验证后再推广。

### 动作 2：goroutineleak profile（backend 目前 0 处 pprof）

Go 1.27 把 goroutine 泄漏检测从实验转 GA。我实测可用：

```
goroutineleak profile registered: true
goroutineleak profile: total 1
1 @ 0x47d82a ...
#	0x4e07f8	main.leak.func1+0x18	main.go:13   ← 精确定位到泄漏点
```

本仓库完全没有 pprof 注册（`grep pprof = 0`），而风险面不小：**28 处 `go func(`、31 处 Ticker/Timer、7 处 `context.WithCancel`**，WS 每连接还有 2 个 goroutine。且 `device_op_tracker.go:187` 与 `handler_device_op.go:77` 的注释**自己承认过泄漏风险**。

与 `/debug/pprof/goroutine` 的区别：后者只给快照要人肉判读；goroutineleak 基于 GC 可达性，**直接告诉你"这个 goroutine 永远醒不过来了"**。

⚠️ **必须绑 127.0.0.1**——本仓库有整套 `internal/notify/redact.go` 脱敏纪律，说明团队清楚 profile 会泄露 URL 里的 token。

### 动作 3：统一 atomic 风格

仓库当前**风格已分裂**：`atomic.Pointer`（新式，[alert/evaluator.go:68](backend/internal/alert/evaluator.go#L68)、[retention.go:9](backend/internal/datalifecycle/retention.go#L9)）与 `atomic.AddInt64(&x,1)`（旧式，[device_mutex.go:40](backend/internal/api/device_mutex.go#L40)）并存，共 23 处旧式。

Go 1.27 的 `go fix atomictypes` 能自动改。**但我在副本上跑过 `go fix ./...`，它动了 261 个文件**——其中 785 处是 `interface{}`→`any` 的纯噪音。**只接受 atomictypes 那几处，其余必须拒绝**。

### 动作 4：泛型方法承载严格绑定（分两步）

Go 1.27 最大语言特性是**泛型方法**（方法可声明自己的类型参数）。我实测三种形态均可用：

```go
func (b Binder) Bind[T any]() (T, error)   // → 调用点 b.Bind[CreateReq]()
```

**但要先说清楚**：本仓库 backend **当前 0 个泛型函数、0 个泛型类型**，所以这不是"重构现有泛型代码"，而是"首次引入泛型的形态选择"——**优先用方法而非包级函数**。

真正的价值不在泛型本身，而在于：`bindJSONStrict`（拒绝未知字段）**只被 3 个端点用了**，其余 62 个端点仍在 gin 的宽松绑定下。而 `bind_json_strict.go` 的注释记录了一次真实事故：`PUT /log-config {"enabled": false}` 返回 200、开关纹丝未动。

**必须分两步**：① 先只统一形态（语义仍宽松），验证零行为变更；② 再逐端点加严。**直接加严是破坏性变更**——Vue 表单常整对象回传，会让 200 变 400。

### 动作 5：修 TrustedProxies（安全，非本次升级引入）

**实测确认**：仓库**从未调用** `SetTrustedProxies` ⇒ gin 用默认值信任所有代理 ⇒ `c.ClientIP()` 可被 `X-Forwarded-For` 伪造。

受影响点（4 处限流 + 审计 IP）：
- [handler_auth.go:86](backend/internal/api/handler_auth.go#L86)（登录限流）、[:99](backend/internal/api/handler_auth.go#L99)（重置）
- [handler_account.go:111](backend/internal/api/handler_account.go#L111)（二次认证限流）、[:126](backend/internal/api/handler_account.go#L126)
- [handler_device_operation.go:60](backend/internal/api/handler_device_operation.go#L60) 等（审计 SourceIP）

而 `docker-compose.yml:29-30` 里 ehome-web 直接映射 `${HOME_PORT:-80}:8080`，**前面没有 nginx/Traefik 兜底**，伪造的 XFF 会直达应用。

**修法**：`r.SetTrustedProxies(nil)`（不信任任何代理，用 RemoteAddr）或指定反代网段。

> ⚠️ 这是 gin 默认值的**既有风险，不是本次升级引入的**。如实标注。

### 动作 6：⚠️ 上线前必须核查 PG 密码（pgx 5.11 行为变更）

**这是本次升级唯一的"可能直接打挂生产"项。** pgx 5.11 把连接串解析器改为对齐 libpq 语义，我用**同一份探针、只切 go.mod 版本**实测（`.tmp-deps/pgxprobe/`）：

| 密码形态 | pgx 5.10（升级前） | pgx 5.11（升级后） |
|---|---|---|
| `plain` | ✅ 正常 | ✅ 正常 |
| 含反斜杠 `C:\path`（带引号） | ✅ 保留 `C:\path` | ⚠️ **变成 `C:path`** → 认证失败 |
| 未加引号含空格 `my secret` | ⚠️ 静默截断成 `my`，且 **dbname 被吃掉变空** | ⚠️ **解析直接报错**（不再是静默错误） |
| 含 NUL | 接受（危险） | 拒绝（**修好了**） |
| `$` / `@` / `:` / `%20` | ✅ 正常 | ✅ 正常 |

**为什么这是真风险**：本仓库 [gorm.go:29](backend/internal/database/gorm.go#L29) 是**裸 `fmt.Sprintf` 拼 DSN、完全没有加引号也没有转义**：

```go
dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s", ...)
```

⇒ 若 `POSTGRES_PASSWORD` 含**反斜杠**或**未加引号的空格**，升级后 PG 直接连不上。

**上线前必做**：核查生产 `.env` 里 `POSTGRES_PASSWORD` 的字符构成；若不安全，二选一：
- 改密码为不含反斜杠/空格的强随机串（最简单）
- 或把 [gorm.go:29](backend/internal/database/gorm.go#L29) 改为 key/value 显式转义，或改用 URL 形式 DSN

---

## 2. 前端：首屏体积有 60% 的可省空间（实测）

[fe-feat](frontend.md) 发现 [vite.config.ts:114-118](frontend-shared/vite.config.ts#L114-L118) 的 `manualChunks` 把 element-plus 全量合并成一个 chunk，而入口 `App.vue:2` 的 `<el-config-provider>` 静态依赖它，导致这个 chunk 被写进 `index.html` 的 `modulepreload`。

我实测复现（`dist/index.html` 里 preload 的 JS，逐文件 gzip 求和）：

```
修复前（当前配置）：  267.9 KB gzip   （element chunk 单项 736KB raw / 233KB gzip）
删掉 element 分组后： 105.2 KB gzip   ⇒ 省 60.7%
```

**改法**：删除 `if (id.includes('node_modules/element-plus/')) return 'element'` 这一行（[vite.config.ts:116](frontend-shared/vite.config.ts#L116)），让 element-plus 按路由懒加载。**保留 `echarts` 那行**（见下方"两个必须纠正的误解"）。

含 CSS 的完整首屏（Lead 实测）：JS 258.4KB + CSS 42.3KB = **300.7KB gzip**，与 lib-fe 报告的 310.2KB 同量级。

### 两个必须纠正的误解（我先前判断错了，这里改正）

**① 仓库并没有"全量引入 echarts"，565KB 已是按需引入后的下限。**
我最初怀疑 echarts chunk 565KB 是因为全量引入。实测否定：`grep "from 'echarts'"` = **0 命中**，35 条 import 全走 `echarts/core`、`echarts/charts` 等子路径，且 [vite.config.ts:38-46](frontend-shared/vite.config.ts#L38-L46) 配了 `unplugin-vue-components` 按需注册。

lib-fe 做了逐模块减法验证：`core+Canvas+Line+Grid` = 467.12KB，加上 Tooltip/Legend/Title/Bar/MarkLine/LabelLayout 后 = 564.88KB，与实产 565,524B 差 0.1%。对照全量引入是 1,115.94KB（1.97×）——**按需引入已经省下 550KB**。没有"565→200"的空间。

**② echarts 的 manualChunks 分组不能删。**
实测删掉 `echarts` 分组后：只画线的页面省 16KB，但 BmsDetailPage（Line+Bar）反而多 1.1KB，且失去 552KB 巨库跨图表页的复用缓存。**所以只删 element 那行，保留 echarts 和 vue 两行。**

⚠️ 测量口径：`dist/index.html` 中 `href/src` 引用的 JS 文件 gzip 求和，**不是浏览器实测**（本机 Playwright 起 Chromium 被 DBus 拒绝）。

---

## 3. 基础设施：PG18 skip scan 是唯一真受益点，但当前收益为 0

### 3.1 我独立验证了 skip scan 确实生效

在运行中的 `ehome-postgres`（**PG 18.4**）建 200 万行临时表 `(a,b,ts)`，查询只提 `b` 不提前导列 `a`：

```
->  Bitmap Index Scan on skiptest_a_b_ts_idx
      Index Cond: (b = 123)
      Index Searches: 52          ← PG18 skip scan 的标志：跳过 50 个 a 值
      Execution Time: 0.998 ms
```

lib-infra 的 A/B 也验证了量级：300 万行同索引，PG16 27.1ms → PG18 13.0ms（**-52%**）。

### 3.2 但本仓库的复合索引根本不存在

我直接在库里查 `pg_indexes`，结果只有 8 个索引，**代码声称要建的 3 个复合索引一个都没有**：

| 代码要求建（[indexes.go:56-67](backend/internal/datalifecycle/indexes.go#L56-L67)） | 生产库实际 |
|---|---|
| `idx_unified_logical_ts` | ✅ 存在 |
| `idx_unified_logical_sensor_ts` `(logical_device_id, sensor_name, timestamp)` | ❌ **缺失** |
| `idx_devdata_logical_created` | ❌ **缺失** |
| `idx_unified_legacy_null_ts` | ❌ **缺失** |

⇒ **skip scan 的收益目前是 0**，必须先跑 `ehomectl datalifecycle ensure-indexes`。注意 [indexes.go:82-88](backend/internal/datalifecycle/indexes.go#L82-L88) 明确写了：分区母表禁止 `CONCURRENTLY`，4.4GB 表上会持 SHARE 锁阻塞写入，**必须走维护窗口**。

### 3.3 PG18 另两个特性不适用

- **`uuidv7()`**：本仓库主键是 `BIGSERIAL`（[models.go:315](backend/internal/models/models.go#L315)），不用 UUID 做主键 ⇒ 无收益
- **AIO**：lib-infra 实测读快 25~27%，但**写入无收益**（1960 vs 1984 tps，<2% 噪声）；且 `io_uring` 在默认 Docker seccomp 下直接 FATAL，加 `unconfined` 才能起且并不更快 ⇒ **不要开**

### 3.4 Prometheus v3：12 条告警规则零风险（实测）

`promtool check rules` → 12 rules SUCCESS；用 v3.15.1 真实加载仓库规则并抓活体 `/metrics`，4 组规则全部 `health=ok`、0 条 `lastError`。

`auto-gomemlimit` 在本仓**不生效**（compose 无 `mem_limit`，回落系统内存×0.9）。

---

## 4. ⚠️ 发现一个静默失效（与本次升级无关，但比升级更急）

**告警邮件根本发不出去。** 三重证据（我逐一核验）：

1. [alertmanager.yml:31-32](deploy/monitoring/alertmanager.yml#L31-L32) 用了 `to: ${ALERT_EMAIL}` / `from: ${ALERT_EMAIL_FROM}`
2. [docker-compose.yml:73-84](docker-compose.yml#L73-L84) 的 alertmanager 服务**没有 `environment:` 段** ⇒ 容器内无这些变量
3. `.env.example` **也未定义** `ALERT_EMAIL` ⇒ 即使加了 environment 也没值

而 Alertmanager **不做环境变量展开**（需靠 `--config.expand-env` 或 shell envsubst），实测 `/api/v2/status` 里配置仍是字面量 `${ALERT_EMAIL}`。

**影响**：告警触发后邮件投递到字面量地址 `${ALERT_EMAIL}`，静默丢弃。所有依赖邮件告警的运维场景都是空的。

**修法**：compose 加 `environment: [ALERT_EMAIL, ALERT_EMAIL_FROM]` + command 加 `--config.expand-env` + `.env.example` 补字段。

---

## 5. 逐条判定：哪些新特性本项目用不上

诚实标注比硬凑重要。以下均给出理由：

| 特性 | 判定 | 理由 |
|---|---|---|
| struct literal 用 field selector（`Outer{Inner.Field:1}`） | **release notes 措辞易误读** | 实测 `Outer{Inner.Field:1}` **编译失败**（`invalid field name`）。实际实现是**直接用 promoted 字段名** `Outer{Field:1}`，且受 `go 1.27` 门槛约束（降到 go1.26 报 `requires go1.27 or later`）。本仓库 embedlit 落点 0 处 |
| 标准库 `uuid` | **不推荐替换** | 缺 `NewString()`（本仓用 15 次）且缺 `NewSHA1`/`NameSpaceOID`——[identity.go:54](backend/internal/datalifecycle/identity.go#L54) 依赖它做确定性 v5，那是 ON CONFLICT 并发防护的正确性基础。另外本仓 UUID 不落 DB 主键，NewV7 的索引局部性优势拿不到 |
| `strings.CutLast` / `bytes.CutLast` | 不做 | 有效落点仅 3 处且都在 simulation，语义还不完全匹配（用的是 `LastIndexByte` 找分隔符） |
| `URL.Clone` / `Values.Clone` | 不做 | 候选点本就无共享，且两份实现有等价契约 |
| `crypto/mldsa`（后量子） | 不适用 | 无 PQ 签名需求 |
| `simd` / `archsimd` | 不适用 | 仍是 `GOEXPERIMENT`，且本仓无向量计算热点 |
| `database/sql.ConvertAssign` | 不适用 | 用 gorm/pgx，不直接碰 database/sql |
| `testing/synctest.Sleep` | 可选 | 可用于时间相关测试，但收益小 |
| **小对象分配 +30%** | **已生效但有个坑** | 零改动自动生效（二进制 +60KB）。⚠ **对 ESP32 固件完全无效**——固件是 C/ESP-IDF，不经过 Go 编译器。不要误以为能帮到 esp32-collector 的堆紧张问题 |
| `go mod tidy` 合并 require 块 | 已生效 | 仓库 go.mod 已经是规范的两块结构 |
| `go test` 默认 `stdversion` 检查 | 已生效 | CI 下次跑就会带上；若报错说明用了比 go.mod 声明更新的符号 |

---

## 6. 依赖侧新能力（详见 [go-deps.md](go-deps.md)）

⚠️ **先纠正一个容易误解的前提**：`gin 1.12.0`、`gorm 1.31.2`、`zap 1.28.0` 在本次升级**之前就已经是这些版本**，不是本次升级带来的变化。本次真正发生版本变化的只有：

| 库 | 变化 | 可落地新能力 |
|---|---|---|
| gorm.io/driver/postgres | 1.6.0 → 1.6.3 | 错误翻译保留原始 `pgErr`（`fmt.Errorf("%w: %w", ...)`）⇒ 可开 `TranslateError:true` 后用 `errors.Is(gorm.ErrDuplicatedKey)` **替代字符串匹配**。本仓 `migrate_gpio.go` 仍在匹配 `"UNIQUE"` 字符串 |
| jackc/pgx | 5.10.0 → 5.11.0 | `Rows` 接口加 `TypeMap`；日期解析器重写 |
| prometheus/client_golang | 1.24.1 → 1.25.0 | promhttp 增 `Vary` 头；`api/prometheus/v1` 有破坏性签名变更（**本仓不用该包**） |
| x/net | 0.57.0 → 0.61.0 | 修 GO-2026-6617（HTTP/2 HPACK 竞态，**CALLED**） |
| x/text | 0.40.0 → 0.43.0 | 修 GO-2026-6629（precis panic，**CALLED**） |
| mimetype | 1.4.15 → 1.4.16 | 原版被上游 **retract** |
| quic-go | 0.61.0 → 0.64.1 | 间接依赖；v0.62 起要求 go1.26 |
| cors / gzip | 1.7.7→1.7.9 / 1.2.6→1.2.8 | 小版本修复 |

---

## 7. 前端其他新能力（详见 [frontend.md](frontend.md) 与 [lib-frontend.md](lib-frontend.md)）

升级后已通过 typecheck（0 error）、2342 项测试、build。分报告另有：`useTemplateRef` 39 处全量清单（并揪出 2 处孤儿 ref 真实缺陷）、`defineModel` 11 处可删约 104 行样板、以及**构建瓶颈其实是 vue-tsc 不是 Vite**（`pnpm build` 21s 里 typecheck 占 13.5s，vite build 仅 4.5s）。

---

## 8. 建议执行顺序

```
上线前  ── ⚠️ 动作 6（核查 PG 密码字符构成）—— 不做这项别发布
        └─ ⚠️ 修 Alertmanager 邮件（§4，加 environment + --config.expand-env）
        └─ ⚠️ 跑 ehomectl datalifecycle ensure-indexes（§3.2，补 3 个缺失索引，走维护窗口）
第 1 天  ── 动作 1（只改 consumers_heavy.go:530）+ benchmark 验证
        └─ 动作 5（SetTrustedProxies，安全，独立 PR）
        └─ 首屏：删 vite.config.ts 的 element 分组（一行改动，省 60%）
第 2 天  ── 动作 2（goroutineleak profile，独立 loopback 端口）
第 3 天  ── 动作 3（atomictypes，逐包提交）
第 2 周  ── 动作 1 推广到剩余 14 处 WS payload
        └─ 动作 4 第一步（泛型方法统一形态，语义不变）
第 3 周  ── 动作 4 第二步（审计前端请求体后逐端点加严）
```

---

## 4.1 附带发现：HEAD 上有一个稳定失败的测试

不是我升级造成的，但既然发现了必须报告（[backend-quality.md](backend-quality.md) §5.1 有完整取证）：

```
--- FAIL: TestOverview_RealtimeValue
    handler_source_overview_realtime_test.go:110: value_age_sec 为空 —— 前端无法区分实时与陈旧
```

**根因**：commit `5318e716`（18:04，另一开发者）把字段 `ValueAgeSec` 改名为 `AgeSec`（[handler_source_overview.go:73](backend/internal/api/handler_source_overview.go#L73)），只更新了主测试文件，**漏改 `handler_source_overview_realtime_test.go:47,109`**。

**我验证过与升级无关**：在纯净 HEAD 的 `git worktree` 里跑同样失败。修法 10 分钟（改两处字段名）。

⚠️ 顺带说明：仓库**有并行开发者在提交**（最近 2 小时 4 个 commit，来自 bcat），我的升级改动与它们共存于工作区。

---

## 9. 验证与边界（诚实声明）

- ✅ 所有 benchmark 数字为本机实跑（go1.27.3 / Ryzen 5 5600），命令在分报告附录可复现
- ✅ `GOEXPERIMENT` 生效性已验证（v2 文件与旧实现互斥，`go list` 确认）
- ✅ 泛型方法、goroutineleak、promoted 字段名、uuid v7 均用最小程序实跑验证
- ✅ pgx 5.10 vs 5.11 密码解析差异用同一探针切换版本实跑对比
- ✅ 首屏体积用 `dist/index.html` 实际 preload 清单复算（267.9KB → 105.2KB）
- ⚠ **我最初的结论曾经错**：第一次用极简 6 字段 struct 测出"v2 Marshal 慢"，据此以为研究员数据有误；换成仓库真实 `UnifiedData`（含 `time.Time`、指针、slice）复测后结果完全反转。**结论依赖负载形状，不能拿玩具结构体推断生产行为**
- ⚠ 未验证：真实流量下 WS 帧率的端到端收益、goroutineleak 在生产负载下对 GC 的影响
- ⚠ 未改任何业务代码，全部为分析建议
