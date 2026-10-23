# Go 依赖新特性在 EHome 后端的落地分析

> 任务：task-4 ｜ 分析员：go-feat ｜ 时间：2026-10-23
> 对象：`backend/go.mod`（go 1.26.5 → **go 1.27.3** 这一轮升级）
> **只做分析，未修改任何仓库代码/依赖**。所有验证探针都在 `/mnt/storage/WorkSpace/EHome/.tmp-deps/`（未纳入 git）。

## 0. 结论速览

### 0.1 先纠正一个前提（重要）

任务描述把这 13 个库都列为"本次刚升级到的版本"，但用 `git diff HEAD -- backend/go.mod` 核实后，**本次 `go.mod` 实际变动 30 个模块 + `go` 指令**（含 1 个删除、1 个新增），已逐行核对：

- **直接依赖变化（9 个）**：`gin-contrib/cors` 1.7.7→1.7.9、`gin-contrib/gzip` 1.2.6→1.2.8、`jackc/pgx/v5` 5.10.0→5.11.0、`prometheus/client_golang` 1.24.1→1.25.0、`prometheus/client_model` 0.6.2→0.6.3、`gorm.io/driver/postgres` 1.6.0→1.6.3、`golang.org/x/crypto` 0.54.0→0.58.0、`go` 1.26.5→1.27.3、删除 `eclipse/paho.mqtt.golang`
- **间接依赖变化（20 个，随上游升级）**：`quic-go` 0.61.0→0.64.1、`x/net` 0.57→0.61、`x/sync` 0.22→0.24、`x/text` 0.40→0.43、`x/sys` 0.47→0.49、`x/arch` 0.29→0.32、`sonic` 1.15.2→1.15.4、`sonic/loader`、`goccy/go-json` 0.10.6→0.11.2、`validator` 10.30.3→10.30.5、`mimetype` 1.4.15→1.4.16、`mongo-driver/v2` 2.8.0→2.9.2、`puddle/v2` 2.2.2→2.2.3、`sqlite3` 1.14.48→1.14.52、`ugorji/go/codec` 1.3.1→1.3.2、`protobuf` 1.36.11→1.36.12、`prometheus/common` 0.70.1→0.72.1、`prometheus/procfs` 0.21.1→0.22.0、`cpuid` 2.4.0→2.4.1、`gin-contrib/sse` 1.1.1→1.1.2、`locales`/`universal-translator` 0.14.1→0.14.2
- **新增**：`rogpeppe/go-internal` 1.16.0（间接，只因 `mimetype` 的某条依赖链出现，无代码引用）
- **未变**：`gin-gonic/gin` 1.12.0、`gorm.io/gorm` 1.31.2、`go.uber.org/zap` 1.28.0、`gorilla/websocket` 1.5.3、`golang-jwt/jwt/v5` 5.3.1

**其中 gin / gorm / zap / gorilla-websocket / golang-jwt 在 `go.mod` 历史里一个字节都没变**（gin 1.12.0 从仓库首个 commit `512a0ccf` 起就是 1.12.0；gorm 1.31.2 自 `7faa65b5` 起）。报告因此分 A/B 两节：**A 节 = 本次真实 delta，B 节 = 版本未变、属"已具备但未使用"的能力盘点，不计入本次升级收益。**

### 0.2 A 节汇总（本次真实 delta）

| 库 | 版本变化 | 对本项目有用的新能力 | 落点（文件:行） | 收益 | 成本 | 风险 |
|---|---|---|---|---|---|---|
| **gorm.io/driver/postgres** | 1.6.0 → 1.6.3 | 错误翻译改为 `fmt.Errorf("%w: %w", ...)` 保留原始 `*pgconn.PgError` ⇒ 可用 `errors.Is(gorm.ErrDuplicatedKey)` 替代字符串匹配 | `internal/database/migrate_gpio.go:244`（`isUniqueConstraintError` 字符串匹配）、`internal/datasource/service.go:334`、`internal/dbschema/source_group.go:60` | 去掉脆弱的 `strings.Contains("UNIQUE")` 判别，唯一键冲突分支不再依赖文案 | 小 | 需同时开 `gorm.Config{TranslateError:true}`（当前未开），会改变**全部** 340 处查询的错误文案，须回归 |
| **gorm.io/driver/postgres** | 同上 | `Config.OptionOpenDB []stdlib.OptionOpenDB` 可在 Open 时注入 pgx stdlib 选项 | `internal/database/gorm.go:33`（现在只能 `postgres.Open(dsn)`） | 可设 `AfterConnect`（注册 pgx 类型/SearchPath/statement cache），当前完全没入口 | 中 | 需把 `postgres.Open(dsn)` 改成 `postgres.New(postgres.Config{Conn: ...})` 或 `OptionOpenDB`；DSN 拼接逻辑要一并改 |
| **gorm.io/driver/postgres** | 同上 | 整数类型映射抽出 `getSchemaIntType`，`generated` tag 支持 PG 生成列 / IDENTITY | 无落点 | **本项目不适用**（无生成列需求，见 §3） | — | — |
| **pgx/v5** | 5.10.0 → 5.11.0 | `Rows` 接口新增 `TypeMap`；stdlib 支持 Go 1.27 `driver.RowsColumnScanner` ⇒ 数组/range 类型可直接扫进 Go 值 | `simulation/harness/db.go:21-22,86,144`（唯一直接用 pgx 的地方） | 仿真框架可少写 `pgtype.Map.SQLScanner` 适配；生产路径走 gorm 不直接受益 | 小 | pgx 是`//go:build simulation`，只影响仿真层；自定义 `Rows` mock 必须补 `TypeMap` 方法否则编译失败 |
| **pgx/v5** | 同上 | **连接串解析器重写为与 libpq 逐字节对齐**（URI + keyword/value 双解析器，差分 fuzz 验证） | `internal/database/gorm.go:29`（keyword/value DSN）、`internal/config/config.go:466`（`DatabaseURL()` 从未被调用） | 见 §1.4 实测：**含特殊字符的 DB 密码现在会解析失败**（行为变更，不是收益） | — | **中**：见下 |
| **pgx/v5** | 同上 | date/timestamp/timestamptz 文本解析器手写化 | 经 gorm 全量走二进制协议，不经过文本路径 | 官方称 timestamp/timestamptz 文本扫描 **~2.5x**（仅影响文本格式结果） | — | 无（本项目不命中） |
| **prometheus/client_golang** | 1.24.1 → 1.25.0 | `promhttp.HandlerOpts.AcceptedFormats` 可 opt-in OpenMetrics 2.0；协商响应新增 `Vary` 头 | `internal/api/routes.go:114`、`internal/api/routes.go:139`（两处 `gin.WrapH(promhttp.Handler())`） | `Vary` 头自动生效，修 CDN/代理缓存串格式的问题；OpenMetrics 2.0 需显式开 | 小 | 开 OpenMetrics 2.0 属实验格式，Prometheus 3.x 才稳定支持；本项目 Prometheus 是 `v3.15.1`（`docker-compose.yml:59`）可用但无必要 |
| **prometheus/client_golang** | 同上 | `HandlerOpts.CoalesceGather`（1.24.0 引入，本版仍可用）合并并发 scrape | `internal/api/routes.go:114,139` | /metrics 被 Prometheus 高频抓取时不再堆 goroutine | 小 | 无 |
| **prometheus/client_golang** | 同上 | `api/prometheus/v1` 的 `Query/QueryRange/Series/...` 签名加 `Infos` 返回 ⇒ **破坏性** | 无落点 | **本项目不适用**（不 import 该包，只用了 `prometheus` / `promauto` / `promhttp`） | — | 无（未使用即无破坏） |
| **gin-contrib/cors** | 1.7.7 → 1.7.9 | 无新 API | `cmd/server/main.go:658` | **零收益**：两个版本导出的 API 集合完全相同（`Config`/`DefaultConfig`/`Default`/`New`），diff 只有 `"Origin"` → `headerOrigin` 常量化 + `http.Method*` 常量替换 + 测试重构 | — | 无 |
| **gin-contrib/gzip** | 1.2.6 → 1.2.8 | **修了反压/缓冲释放时机的 bug**：`defer` 里的 `if/else if` 改 `switch`（互斥分支语义） | `cmd/server/main.go:639`（全局 gzip 中间件，注释明确写了 20MB JSON→3MB） | 见 §1.2 | — | 无 |
| **gin-contrib/gzip** | 同上 | `DefaultDecompressHandle` 减少响应不带 `Content-Length` 的行为差异（测试从断言空改成断言具体长度） | 无落点（本项目未用 `DefaultDecompressHandle`，grep 0） | 不适用 | — | — |
| **x/net** | 0.57.0 → 0.61.0 | **修 GO-2026-6617**：HTTP/2 HPACK encoder 竞态导致 server crash | `internal/notify/client.go:127`（`http.Client.Do` 走 HTTP/2）、`cmd/server/main.go:705`（`ListenAndServe`）、`internal/websocket/websocket.go:246` | **真实安全收益**（govulncheck 判定 CALLED） | 已完成 | 无 |
| **x/text** | 0.40.0 → 0.43.0 | **修 GO-2026-6629**：`secure/precis` 解析精心构造输入 panic | `internal/database/gorm.go:33`（`gorm.Open` → `precis.Profile.String`，govulncheck 判定 CALLED） | **真实安全收益** | 已完成 | 无 |
| **x/crypto** | 0.54.0 → 0.58.0 | SSH 层新增 ML-DSA（后量子）算法 + knownhosts/cipher/handshake 整片改动 | `internal/auth/login.go:34`、`internal/auth/account.go:31,37,84`、`internal/auth/initialize.go:35`（**只用 bcrypt**） | **无收益**：`ssh` 包未被 import，govulncheck 也没给 x/crypto 报 CALLED。升级属顺手清干净 | 已完成 | 无 |
| **quic-go** | 0.61.0 → 0.64.1（间接，经 gin） | v0.62 起要求 go1.26；v0.63 三条 http3 破坏性变更 | 无直接 import（`grep quic-go` 非测试代码 = 0） | 见 §1.7 | — | 见 §1.7 |
| **mimetype** | 1.4.15 → 1.4.16（间接） | **修 GHSA-jjmc-jx7j-hp68**：1.4.15 解析特制 CDF 文件耗时平方级（DoS），故被上游 retract | 无直接落点（非测试 `grep mimetype` = 0），但经 `validator`→`gin` 链路编进二进制 | 安全修复（见 §1.6 补充） | 已完成 | 无 |
| **goccy/go-json** | 0.10.6 → 0.11.2（间接） | 仅 `go_json` build tag 下启用 | 无落点（未设 build tag，gin 走 `encoding/json`） | 不适用 | — | — |
| **sonic** | 1.15.2 → 1.15.4（间接） | 仅 `sonic` build tag 下启用 | 无落点（同上） | 不适用 | — | — |
| **mongo-driver/v2** | 2.8.0 → 2.9.2（间接） | gin 1.12 的 bson render 需要 | 无落点 | 不适用 | — | — |

### 0.3 B 节汇总（版本未变，非本次 delta）

| 库 | 版本 | 已具备但未使用的能力 | 落点 | 是否值得做 |
|---|---|---|---|---|
| **gin** | 1.12.0（历史从未变过） | `Context.GetError/GetErrorSlice`、`Context.Delete`、`Context.BSON`、uri/query 绑定支持 `encoding.TextUnmarshaler`、`Engine.UseEscapedPath` | `internal/api/bind_json_strict.go:35`、`internal/api/envelope.go:22-92` | 低优先（见 §2.1） |
| **gin** | 1.12.0 | **`ClientIP()` 受 `X-Forwarded-For` 伪造影响 ⇒ 登录限流可绕过** | `internal/api/handler_auth.go:86,99`、`internal/api/handler_account.go:111,126`、`internal/auth/login_limiter.go:32` | **高优先（安全，既有风险）**，见 §2.2 |
| **gorm** | 1.31.2 | 泛型 API `gorm.G[T](db)`（本仓 0 处使用） | 全仓 340 处 `First/Find/Take/Last` | 见 §2.3，建议**不引入** |
| **gorm** | 1.31.2 | `WithResult()` 拿到结构化执行结果 | 无 | 低 |
| **zap** | 1.28.0 | `zapcore.CheckPreWriteHook`、`CheckedEntry.Before` | `pkg/logger/logger.go:60-90` | 低（见 §2.4） |
| **gorilla/websocket** | 1.5.3 | 无变化 | `internal/websocket/websocket.go`、`internal/terminal/ws_handler.go` | 不适用 |
| **golang-jwt/v5** | 5.3.1 | 无变化 | `internal/auth/token.go`、`internal/api/middleware.go` | 不适用 |

---

## 1. A 节：本次真正 delta 的库

### 1.1 gorm.io/driver/postgres 1.6.0 → 1.6.3

官方来源：https://github.com/go-gorm/postgres/releases （v1.6.3 changelog 未单独成文，逐文件 diff `postgres@v1.6.0` vs `postgres@v1.6.3` 得出）

#### (a) 错误翻译保留原始 PgError —— 本项目最有价值的一条

改前（`error_translator.go`）：
```go
if translatedErr, found := errCodes[pgErr.Code]; found {
    return translatedErr            // 原始 PgError 被丢弃
}
```
改后：
```go
return fmt.Errorf("%w: %w", translatedErr, pgErr)   // 两层 %w，errors.Is / errors.As 都成立
```

**为什么对本项目重要**：`internal/database/migrate_gpio.go:244` 现在是纯字符串匹配：

```go
func isUniqueConstraintError(err error) bool {
    msg := err.Error()
    // SQLite: "UNIQUE constraint failed: ..."
    // PostgreSQL: "duplicate key value violates unique constraint ..."
    return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "duplicate")
}
```

这个判别一旦 PG 改文案（或语言环境非英文）就静默失效 ⇒ `migrate_gpio` 的"撞唯一键就跳过"分支退化成"整批迁移失败"。同类脆弱点还有 `internal/dbschema/source_group.go:60`（注释里明确说"unique constraint failed 不带分组信息"）、`internal/datasource/service.go:334-347`（注释在解释 `ux_ds_one_active` 冲突）。

**关键前提（必须一起做，否则这条用不上）**：`gorm.Config{TranslateError: true}` 当前**没有启用**（`grep TranslateError` = 0）。`internal/database/gorm.go:33` 的 `&gorm.Config{` 只设了 Logger 和 `DisableForeignKeyConstraintWhenMigrating`。不启用时 gorm 不做翻译，`errors.Is(err, gorm.ErrDuplicatedKey)` 永远 false。

- 预期收益：把 3 处文案匹配换成类型判别，消除"PG 换文案 ⇒ 迁移/来源组切换静默失败"这一类缺陷（本仓库历史上 `ux_ds_one_active` 撞键造成过 500，见 git `39d8617a`）。
- 实施成本：**小改 + 中回归**。开全局 TranslateError 会影响全部 340 处查询返回的错误文案（`handler_device.go` 等大量 `errors.Is(err, gorm.ErrRecordNotFound)` 不受影响，但依赖 `err.Error()` 文本的地方会被影响，目前 grep 到 0 处这种用法，风险可控）。
- 风险：SQLite 测试库也走同一 `TranslateError`；SQLite driver 1.6.0 是否已带翻译需单独验证（**本次未验证**，属实施前必查项）。

#### (b) `Config.OptionOpenDB` —— 打开 pgx stdlib 选项注入口

v1.6.3 在 `postgres.go:34` 新增：
```go
type Config struct {
    ...
    OptionOpenDB []stdlib.OptionOpenDB
}
```
当前 `internal/database/gorm.go:29-33` 只能是：
```go
dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s", ...)
DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{...})
```
`postgres.Open(dsn)` 内部 new 一个 Dialector，**调用方无法传 OptionOpenDB**。要用上必须改成 `postgres.New(postgres.Config{OptionOpenDB: ...})` 或 `postgres.Config{Conn: stdlib.OpenDB(...)}`。

- 预期收益：目前完全无法在连接建立后注册 pgx 侧钩子（自定义类型、`search_path`、statement cache 调优）。对 EHome 的**直接收益有限**（无自定义 PG 类型、无多 schema 需求，见 `grep search_path` = 0 生产代码）。
- 实施成本：中（DSN 拼接要从字符串改为 Config 结构体，`internal/database/gorm.go:29` 与 `internal/config/config.go` 的 `DatabaseConfig` 要对齐）。
- 结论：**列为储备项，不建议本轮做**。

#### (c) `generated` tag（PG 生成列 / IDENTITY）—— 本项目不适用

v1.6.3 新增 `generatedColumnOf` / `identityMode`（`postgres.go:333-378`），支持 `gorm:"generated:identity"`、`gorm:"generated:price * quantity"`。本仓库 `internal/models/` 里没有任何生成列需求，且 `AutoMigrate` 已在 `internal/database/gorm.go:66` 起被大幅裁剪（`DeviceData`/`UnifiedData` 明确移出，见该处注释，由 `partition_mgr` 直接建分区母表）。**逐条判定：不适用，无落点。**

#### (d) 索引 schema 限定修复（RenameIndex/DropIndex/HasIndex 加 relnamespace）—— 本项目不适用

`migrator.go:172-200` + `migrator.go:760`。本仓库 `grep DropIndex\|RenameIndex\|CreateIndex\|HasIndex` 非测试代码 = **0 处**，全在 `public` schema。**不适用，但也无风险。**

### 1.2 gin-contrib/gzip 1.2.6 → 1.2.8

官方来源：逐文件 diff（`handler.go` 是唯一实质变化，24 天内无 CHANGELOG 条目）

唯一实质改动在 `handler.go:92-110` 的 `defer` 块，三个 `else if` 改成 `switch`：

```go
defer func() {
    switch {
    case gw.status >= 400:          // 错误响应：移除 gzip 头 + 丢弃缓冲
        gw.removeGzipHeaders()
        gz.Reset(io.Discard)
    case !gw.shouldCompress:        // 未达压缩阈值：直写缓冲
        gw.Header().Del(headerContentEncoding)
        ...
    case c.Writer.Size() < 0:       // 空响应：不写 gzip footer
        gz.Reset(io.Discard)
    }
}()
```

**为什么这不算纯风格改动**：`if / else if` 链与 `switch` 在"条件互斥性"上语义相同，但当 `shouldCompress` 为 false **且** `status >= 400` 同时成立时，原写法进入第一个分支（正确），新写法同样进入第一个分支（正确）——**行为等价**。真正的差别是 `gz` 的 `Reset(io.Discard)` 与缓冲直写在三条路径上被显式列全，配合测试里新增的 `TestPrometheusMetricsDoubleCompression`（`gzip_test.go`），是在守护本项目已经踩过的坑。

**本项目落点**：`cmd/server/main.go:639`
```go
r.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{
    "/metrics",
    "/api/v1/metrics/prometheus",
})))
```
该处上方注释（`main.go:631-638`）明确记录了曾经的事故：gzip 中间件与 `promhttp` 各自写 gzip 头 ⇒ 双重帧 ⇒ **真实 Prometheus 抓取失败**。v1.2.8 相关测试（`TestPrometheusMetricsDoubleCompression`）正是这一类的回归护栏，`simulation/harness/http.go:57-58,445-478` 与 `simulation/catalog/rt.go:415,504` 也有对应的仿真断言。

- 预期收益：不是新能力，是**既有隔离逻辑的回归护栏变强**（测试层面），运行时行为无变化。
- 实施成本：0（已升级）。
- 风险：无。**结论：本次升级该库零功能收益，属"跟随上游"维护动作。**

### 1.3 gin-contrib/cors 1.7.7 → 1.7.9

官方来源：逐文件 diff，**导出 API 集合完全一致**（`Config`/`DefaultConfig`/`Default`/`New`）。

实质改动只有：
1. `config.go:72` `c.Request.Header.Get("Origin")` → `Get(headerOrigin)`（常量化）
2. `cors.go:193-204` `DefaultConfig()` 里 `[]string{"GET",...}` → `http.MethodGet` 等常量
3. `utils.go:24,60` `"Vary"` 值 `"Origin"` → `headerOrigin`
4. 测试文件大规模重构（抽 `testOriginGoogle` 等常量），并新增 `testOriginChromeExt`、`chrome-extension://` 场景

**本项目落点**：`cmd/server/main.go:658` `cors.New(cors.Config{...})`。该处 `AllowOrigins` 支持 `"*"` 且 `AllowCredentials: true`（`main.go:653-657` 注释说明 gin-contrib/cors 会自动回显请求 Origin）。

- 预期收益：**零**。无新 API、无行为变化、无 CVE 修复。
- 实施成本：0。
- 风险：无。**结论：维护性升级，无可落地特性。**

### 1.4 pgx/v5 5.10.0 → 5.11.0

官方来源：https://github.com/jackc/pgx/blob/master/CHANGELOG.md （5.11.0 段落，2026-09-07）

#### (a) 连接串解析器重写 —— **这是本次升级唯一可能"踩到"的破坏性变更**

v5.11.0 把 URI 解析从 `net/url` 换成新写的、与 libpq 逐字节对齐的解析器；keyword/value 解析也重写（差分 fuzz 验证）。CHANGELOG 明列的行为变更里，与 EHome 相关的是：

> A backslash escapes whatever character follows it and is dropped... A value containing a backslash must now escape it.
> Whitespace inside a keyword is an error... an unquoted value containing a space: `application_name=my app host=x` previously set neither parameter ... and now fails to parse.

**本项目落点**：`internal/database/gorm.go:29-30`
```go
dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
    cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)
```
密码来自 `internal/config/config.go:341`（`EHOME_DB_PASSWORD` env，`docker-compose.yml:36` 由 `${POSTGRES_PASSWORD}` 注入）。**这里是对裸字符串做格式化，没有任何 quote/escape**。

**我实测了 5.10 vs 5.11 的差异**（探针 `.tmp-deps/pgxprobe/` 与 `.tmp-deps/pgxprobe510/`，同一份 `main.go`，仅 go.mod 版本不同）：

| DSN 场景 | pgx 5.10.0 | pgx 5.11.0 | 判定 |
|---|---|---|---|
| `password=secret` | OK | OK | 无变化 |
| `password='my secret'`（引号包裹含空格） | OK | OK | 无变化 |
| `password=my secret`（**未引号含空格**） | **OK，静默截断**：password=`"my"`，**dbname 变空** | **PARSE ERROR**：`missing "=" after "secret"` | ⚠️ 5.10 是静默错配，5.11 是显式报错。**5.11 更安全**，但若生产密码含空格且未引号，升级后**启动即失败** |
| `password='C:\path'`（反斜杠） | OK，值=`C:\\path`（反斜杠被保留） | OK，值=`C:path`（反斜杠被吃掉，与 libpq 一致） | ⚠️ **密码含反斜杠会被改写** ⇒ 认证失败 |
| `password='ab$cd'` | OK | OK | 无变化 |
| `password='a@b:c'` | OK | OK | 无变化 |
| `password='ab%20cd'` | OK | OK | 无变化（`%` 不再解 %XX，但值本身也没变） |
| `password` 含 **NUL 字节** | **OK（危险）** | **PARSE ERROR**：`forbidden NUL byte` | ✅ 安全修复 |

**结论（务必确认）**：如果生产/测试的 `POSTGRES_PASSWORD` 含**反斜杠**或**未引号的空格**，本次升级会导致 **PG 连接失败**（前者认证失败，后者解析失败）。反之，含 NUL/空格的畸形串现在会被显式拒绝，属安全改善。

- 建议动作：核查 `.secrets/` / `.env` 里的 `POSTGRES_PASSWORD` 是否含 `\` 或空格；若含，改成 `postgres.Config`（结构体，非 DSN 字符串）或用 `'...'` 引号包裹并转义反斜杠。**这是本轮升级最需要落地检查的一条。**
- 成本：小（改 `gorm.go:29`）｜风险：中（不检查可能生产起不来）

#### (b) `Rows.TypeMap` + Go 1.27 `driver.RowsColumnScanner` —— 只影响仿真层

- `Rows.TypeMap`（新方法，破坏性：自定义 `Rows` 实现/mock 必须补）：本仓库 `grep pgx` 非测试只有 `simulation/harness/db.go:21-22`，用 `pgx.ParseConfig` + `stdlib.OpenDB`，**不实现 `Rows` 接口** ⇒ 不受影响。
- `driver.RowsColumnScanner`：让 `database/sql` 能把 PG 数组/range 直接扫进 Go 值，省掉 `pgtype.Map.SQLScanner`。本仓库生产路径全走 gorm（不直接用 `database/sql` 扫 PG 数组），**无落点**。
- `config.MaxProtocolMessageBodyLen`（新字段，防恶意/异常超大协议消息）：需显式设置，本项目未设 ⇒ 无变化。

#### (c) date/timestamp/timestamptz 手写解析器 —— 本项目不命中

CHANGELOG 称文本扫描快 ~2.5x。但 `gorm` 走 pgx 默认的**二进制**协议（`QueryExecModeCacheStatement`），不经过文本解析路径。且本项目 DSN 未设 `prefer_simple_protocol`（`grep PreferSimpleProtocol` 非测试 = 0）。**收益为 0，无落点。**

**行为变更风险**：`timestamptz` 文本扫描现在返回 `time.Local`（或 `ScanLocation`）。本项目全二进制，**不命中**。

### 1.5 prometheus/client_golang 1.24.1 → 1.25.0

官方来源：https://github.com/prometheus/client_golang/blob/main/CHANGELOG.md （1.25.0 / 2026-10-07）

#### (a) `promhttp.HandlerOpts.AcceptedFormats`（OpenMetrics 2.0 opt-in）—— 本项目可用但不必要

新增字段（`promhttp/http.go:637-648`），默认仍是 `expfmt.DefaultAcceptedFormats()`，**不改变现有协商行为**；要 OpenMetrics 2.0 必须显式传。

**本项目落点**：`internal/api/routes.go:114` 与 `:139`
```go
r.GET("/metrics", gin.WrapH(promhttp.Handler()))                       // :114
v1.GET("/metrics/prometheus", gin.WrapH(promhttp.Handler()))           // :139
```
两处都用无参 `promhttp.Handler()`，即默认 `HandlerOpts{}`。

- 预期收益：OpenMetrics 2.0 是实验格式，本项目 Prometheus 是 `prom/prometheus:v3.15.1`（`docker-compose.yml:59`）——**Prometheus 3.x 对 OM2.0 支持仍是实验性的**，开了也没有消费方。**收益 ≈ 0。**
- 实施成本：小（改 `promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{AcceptedFormats: ...})`）。
- 判定：**列为"知道即可"，本轮不做。**

#### (b) 协商响应新增 `Vary` 头 —— 自动生效的真收益

1.25.0 的 `[ENHANCEMENT]`：`promhttp` 协商后的响应带 `Vary` 头。本项目 `/metrics` 与 `/api/v1/metrics/prometheus` 都是**无 gzip 中间件包裹**（`main.go:639` 显式排除），由 `promhttp` 自己协商 gzip ⇒ 现在会正确带 `Vary: Accept-Encoding`。

- 收益：如果将来在 /metrics 前面加 CDN/缓存代理（目前没有，`docker-compose.yml:29-30` 是 app 端口直出），不会串格式。**当前无消费方，收益=潜在。**
- 成本：0（已升级）。

#### (c) `HandlerOpts.CoalesceGather` —— 可选的实际优化

1.24.0 引入，1.25.0 保留。作用是合并并发 `Gather`：多个 scrape 重叠时共享一次采集，避免 goroutine 堆积。

**本项目相关性**：`/metrics` 是**未鉴权**的（`routes.go:112-114` 注释明确"requiring a session token would break scraping"）。如果被外部高频抓取，每个请求都会`Gather()` 全部 collector（`pkg/metrics/metrics.go` + `internal/sensorfield/metrics.go` 合计数十个 Vec）。

- 落点：`routes.go:114`
- 预期收益：低-中（取决于 scrape 频率；当前只有 1 个 Prometheus 实例 15s 抓一次，收益≈0）
- 成本：小｜风险：无
- 判定：**低优先，可在加缓存代理时一起做。**

#### (d) `api/prometheus/v1` 破坏性签名 —— 本项目完全不受影响

`Query/QueryRange/Series/LabelNames/LabelValues` 增加 `Infos` 返回，`api.Client.Do/DoGetFallback` 签名变化。**本仓库只 import 了 `prometheus`、`prometheus/promauto`、`prometheus/promhttp`（+ 测试用 `testutil`），从未 import `api/` 子包**（`grep prometheus/client_golang` 全部落点：`internal/api/handler_metrics.go:12`、`internal/api/routes.go:23`、`internal/sensorfield/metrics.go:4-5`、`pkg/metrics/metrics.go:4-5`）。**不适用，无破坏。**

### 1.6 安全批：go 1.27.3 + x/net + x/text + x/crypto + mimetype —— **本轮最硬的收益**

本地证据：`.tmp-deps/govuln.txt`（`govulncheck -scan=symbol`，基线是**升级前**的 go1.27.1 + x/net@0.57.0 + x/text@0.40.0）。
该次扫描结论原文：

> Your code is affected by **12 vulnerabilities** from 1 module and the Go standard library.

即**升级前有 12 条 CALLED（真实可达）漏洞**，不是 3 条。逐条如下（落点均取自 govulncheck 输出的调用链）：

| # | CVE | 模块 | 升级前 → 修复版本 | 本项目可达调用链（govulncheck 原文落点） |
|---|---|---|---|---|
| 1 | **GO-2026-6809** 不信任被攻陷的中间证书 | **crypto/x509**（标准库） | go1.27.1 → **go1.27.3** | `internal/transport/server.go:379`：`transport.Server.handleConn` → `tls.Conn.HandshakeContext` → `x509.Certificate.Verify` |
| 2 | **GO-2026-6629** `secure/precis` 解析精心构造输入 panic | **golang.org/x/text** | x/text@0.40.0 → **v0.41.0**（本仓升到 0.43.0） | `internal/database/gorm.go:33`：`database.Connect` → `gorm.Open` → `precis.Profile.String` |
| 3 | **GO-2026-6617** HTTP/2 server crash（HPACK encoder 竞态） | **x/net** + 标准库 | x/net@0.57.0 → **v0.60.0**（本仓 0.61.0）；net/http@go1.27.1 → go1.27.2 | `internal/notify/client.go:127`、`cmd/server/main.go:705`、`internal/websocket/websocket.go:246` |
| 4 | **GO-2026-6613** HTTP/1 server 2xx CONNECT 后连接失步 | net/http | go1.27.1 → **go1.27.2** | `cmd/server/main.go:658`（cors）、`internal/notify/client.go:127`、`internal/websocket/websocket.go:246` |
| 5 | **GO-2026-6612** HTTP/2 server 流控重复退还 | net/http | go1.27.1 → go1.27.2 | `cmd/server/main.go:705` |
| 6 | **GO-2026-6611** 反复 initial window change 致 CPU 过耗 | net/http | go1.27.1 → go1.27.2 | `cmd/server/main.go:705` |
| 7 | **GO-2026-6610** HTTP/2 transport 接受畸形 framing 头 | net/http | go1.27.1 → go1.27.2 | `internal/notify/client.go:127` |
| 8 | **GO-2026-6609** Range 头解析无大小上限 | net/http | go1.27.1 → go1.27.2 | `cmd/server/main.go:705`、`cmd/server/static.go` 静态文件服务 |
| 9 | **GO-2026-6608** MIME 头解析绕过内存限制 | net/textproto | go1.27.1 → go1.27.2 | `internal/api/handler_ota.go:140`（`c.FormFile` multipart 上传） |
| 10 | **GO-2026-6607** ECH outer extension 引用校验 | crypto/tls | go1.27.1 → go1.27.2 | `internal/transport/server.go:379` |
| 11 | **GO-2026-6605** HTTP/1 client CONNECT 被拒后连接失步 | net/http | go1.27.1 → go1.27.2 | `internal/notify/client.go:127`（外发通知出站 HTTP） |
| 12 | **GO-2026-6603** HTTP/2 server Trailer 头致内存耗尽 | net/http | go1.27.1 → go1.27.2 | `cmd/server/main.go:705`（`ListenAndServe` → `http2.Server.ServeConn`） |

**逐类判定**：
- **#1 / #10（TLS / x509 / ECH）**：命中 `internal/transport/server.go:379` 的**设备 TCP+TLS 接入握手**。这是 EHome 暴露给 ESP32 的第一道门，**真实生产风险**，不是纸面 CVE。
- **#2（x/text precis）**：命中 `gorm.Open`（DSN 参数解析链）。触发条件苛刻，但升级前确为 CALLED。
- **#3~#12（net/http 全家桶）**：命中 `cmd/server/main.go:705` 的服务端与 `internal/notify/client.go:127` 的出站 HTTP。含 **2 条 DoS**（#6 CPU 过耗、#12 Trailer 内存耗尽）、**2 条连接失步**（#4/#11，可作请求走私前置）、**1 条上传内存绕过**（#9 直指 OTA 固件上传端点）。EHome 的 API 对浏览器/内网开放，**全部属真实风险**。

**x/crypto 0.54.0 → 0.58.0**：本次 diff 的实质内容是 **SSH 层新增 ML-DSA（后量子）支持**（`ssh/keys.go` 新增 `KeyAlgoMLDSA44/65/87`、新文件 `ssh/mldsa.go`）以及 `knownhosts`/`handshake`/`cipher`/`certs` 整片改动。**本项目只用 `bcrypt`**（`internal/auth/login.go:34`、`internal/auth/account.go:31,37,84`、`internal/auth/initialize.go:35` 共 5 处，是 `grep golang.org/x/crypto` 的全部非测试落点），`ssh` 包未 import ⇒ **无触发面**，govulncheck 的 CALLED 列表里也确实没有 x/crypto 条目。

**mimetype 1.4.15 → 1.4.16**：`1.4.15` 被上游 retract，`go.mod` 里写明理由：

```
retract (
    // v1.4.15 spends quadratic time for specially crafted CDF files.
    // GHSA-jjmc-jx7j-hp68
    v1.4.15
)
```

即 **1.4.15 本身是 DoS 漏洞版本**（特制 CDF 文件触发平方级耗时）。本项目非测试代码 0 处直接 import mimetype（仅经 `validator` → `gin` 间接依赖；`validator` 当前只用了 `required`/`min`/`max`，见 §1.8）⇒ **无本项目可触发的路径，但升级确实消掉一个真实 DoS 修复**。

**结论：本次安全批（go1.27.3 + x/net 0.61 + x/text 0.43）消掉了升级前 12 条 CALLED 漏洞中的全部。这是整轮升级里唯一"能明确说修掉了什么"的硬收益，也是验证收尾时必须留档的证据。**

### 1.7 quic-go 0.61.0 → 0.64.1（间接依赖）

官方来源：https://github.com/quic-go/quic-go/releases ；v0.63.0 说明 https://newreleases.io/project/github/quic-go/quic-go/release/v0.63.0

**引入路径**：`gin 1.12.0` → `gin.go:20 imports quic-go/http3` → `quic-go v0.64.1`。本仓库**没有任何直接 import**（`grep quic-go` 非测试 = 0；`grep RunQUIC/http3` = 0），也从未调用 `Engine.RunQUIC`（`cmd/server/main.go:705` 用的是标准 `http.Server.ListenAndServe`）。

关键约束与破坏性变更（均**不影响本项目**）：
- **v0.62 起要求 go1.26**：本项目已 go1.27.3 ✅
- **v0.63 三条 http3 破坏性变更**：① 非 CONNECT 请求 `URL.Scheme/URL.Host` 置空（对齐 `net/http`）；② Extended CONNECT 同上 + `RequestURI` 用 `:path`；③ stream API 错误统一为 `*http3.Error`。本项目**不用 http3 server**，只被动继承 gin 的编译单元。
- **残留在二进制里**：`backend/server` 二进制 `strings | grep -c quic-go` = **4169**。即 quic-go 代码真实被打进产物（gin 无条件 import http3），但**没有任何代码路径抵达**。

- 收益：0（功能）/ 0（性能，未执行）
- 成本：0（间接已升）
- 风险：**低但非零**：v0.62 提高 go 下限到 1.26，若将来有人把 `go.mod` 回退到 1.25 会构建失败；另外 quic-go 是 CVE 高发库（历史多次 DoS），被动跟随意味着**没有主动控制权**，需要靠 `go get -u` 定期跟上。
- 可选的减重动作：给 gin 加 build tag 或用 `replace` 剔除 http3 不现实（gin 无条件 import）。**若要瘦身，唯一正解是等 gin 把 http3 分离**。**本项目不适用任何 quic-go 新 API。**

### 1.8 其余间接依赖（无落点，逐条给原因）

| 库 | 变化 | 为何不适用 |
|---|---|---|
| `bytedance/sonic` 1.15.2→1.15.4 | JSON 加速库，仅 `-tags sonic` 启用 | 本项目未设 build tag。gin 的 `codec/json/json.go` 有 `//go:build !jsoniter && !go_json && !(sonic && ...)`，默认走 `encoding/json`。`Makefile`/CI 均无 `-tags sonic`（grep 确认） |
| `goccy/go-json` 0.10.6→0.11.2 | 同上，仅 `-tags go_json` | 同上。注意 0.11 是 minor 但含 API 调整，**因未启用而无影响** |
| `ugorji/go/codec` 1.3.1→1.3.2 | msgpack 编解码 | 本项目不用 msgpack；ESP32 协议走自定义二进制帧（`pkg/frame`、`pkg/protoframe`） |
| `go-playground/validator` 10.30.3→10.30.5 | 结构体校验 | 本项目只用了 `required`（21 处）/ `min=1`（1 处）/ `max=100`（1 处），全部是稳定 tag。`grep RegisterValidation` = 0（无自定义校验器）⇒ 无迁移风险、无新能力可用 |
| `mongo-driver/v2` 2.8.0→2.9.2 | gin 1.12 bson render 需要 | 本项目不用 bson、不用 mongo。**纯 gin 依赖副作用** |
| `puddle/v2` 2.2.2→2.2.3、`sqlite3` 1.14.48→1.14.52 | 连接池 / SQLite 驱动 | puddle 是 pgx 的连接池（本项目经 gorm→pgx 使用，版本自动跟随）；sqlite3 只用于**测试库**（`testutil/db.go`、大量 `_test.go`） |
| `protobuf` 1.36.11→1.36.12 | gin 1.12 的 ProtoBuf render | 本项目 API 全 JSON，不使用 protobuf render |
| `x/sys` 0.47→0.49、`x/arch` 0.29→0.32、`cpuid` 2.4.0→2.4.1 | 底层 syscall/汇编 | 纯跟随，无 API 变化 |

---

## 2. B 节：版本未变但较新的库（**非本次升级 delta**）

> 这一节的每一条都必须理解为"**仓库早就具备、只是没用**"，不能算进本次升级的收益。

### 2.1 gin 1.12.0 的新 API —— 本项目基本用不上

gin 1.12.0 相对 1.11.0 的新增（官方 https://github.com/gin-gonic/gin/blob/master/CHANGELOG.md ，Gin v1.12.0 段）：

| 新能力 | 官方 PR | 本项目落点 | 判定 |
|---|---|---|---|
| `Context.GetError(key) error` / `GetErrorSlice(key) []error` | #4502 | `internal/api/*.go` | **不适用**：本项目 `c.Error(` 调用数 = **0**（全部错误走 `internal/api/envelope.go:66 Error()` / `:92 AbortError()` 直接写响应），`c.Errors` 从未被填充，取不到东西 |
| `Context.Delete(key)` | 38e7651 | `internal/api/middleware.go:107-108,134-138`（`c.Set` 6 处） | 不适用：本项目只在请求生命周期内读这些 key，从不需要在中间件间"删掉某个 key" |
| `Context.BSON(code, obj)` | #4145 | 无 | 不适用：API 全 JSON |
| uri/query 绑定支持 `encoding.TextUnmarshaler` | #4203 | `grep uri:\|form:` = **0** | 不适用：本项目**完全不用** `ShouldBindQuery/ShouldBindUri`，119 处查询参数都是手工 `c.Query()` + `strconv` 解析（如 `handler_data.go:77-78,183-184,234`） |
| `Engine.UseEscapedPath` | #4420 | 无 | 不适用：无含特殊字符的路由段需求 |
| `ClientIP()` 支持多值 XFF | #4472 | 见 §2.2 | **这是安全相关，见下** |
| 性能：`findCaseInsensitivePath` 减分配、`redirectTrailingSlash` 去正则、recovery 栈读取优化 | #4417/#4414/#4466 | `cmd/server/main.go:629` `gin.New()` | 收益极小：`RedirectFixedPath` 默认 false（本项目未开，`grep RedirectFixedPath` = 0）；recovery 只在 panic 时执行 |

**结论：gin 1.12 对本项目的新增 API，落点全部是"本项目不适用"。唯一实质可用的是 §2.2 的安全项，而它也不是 1.12 新增的能力，是既有默认值问题。**

### 2.2 【高优先｜既有风险，非本次升级引入】`c.ClientIP()` 可被 `X-Forwarded-For` 伪造 ⇒ 登录限流可绕过

#### 事实链（全部有文件:行）

1. **gin 的默认信任所有代理**。`gin.New()`（v1.11.0 `gin.go:215` 与 v1.12.0 `gin.go:225` **两版完全一致**）：
   ```go
   trustedProxies: []string{"0.0.0.0/0", "::/0"},
   ForwardedByClientIP: true,
   RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
   ```
   ⇒ `c.ClientIP()`（v1.12.0 `context.go:975-1024`）在 `trusted == true` 时直接取 `X-Forwarded-For` **最左的非信任 IP**；因为 0.0.0.0/0 全信任，**攻击者随便写一个 XFF 就能决定返回值**。

2. **本项目从未调用 `SetTrustedProxies`**：`grep SetTrustedProxies` = **0**。`cmd/server/main.go:629` 就是裸 `gin.New()`，没有覆盖。

3. **部署形态没有反向代理**：`docker-compose.yml:27-30`（`container_name: ehome-web` 在 :27，端口发布在 :30）
   ```yaml
   container_name: ehome-web
   ports:
     - "${HOME_PORT:-80}:8080"
   ```
   即 app 端口 **直接对外** 发布。`grep nginx|traefik|proxy_pass` 在仓库 = 0。⇒ **没有任何可信代理会替客户端写 XFF**，所以此刻到达 app 的 XFF 只可能来自客户端本身（伪造）。

4. **`ClientIP()` 是登录限流的 key**：
   - `internal/api/handler_auth.go:86`：`limiter.AllowFailure(ctx, c.ClientIP(), req.Username)`
   - `internal/api/handler_auth.go:99`：`limiter.Reset(ctx, c.ClientIP(), req.Username)`
   - `internal/api/handler_account.go:111,126`：同样两处（会话内改密）
   - `internal/auth/login_limiter.go:32`：`AllowFailure(ctx, sourceIP, username)` → :33 `limiterKeys(sourceIP, username)`
   - `internal/auth/login_limiter.go:46-53`：`limiterKeys` 生成三个 key —— `auth:login:ip:<ip>`、`auth:login:user:<user>`、`auth:login:pair:<ip>:<user>`

   ⇒ **攻击者每次请求换一个伪造的 `X-Forwarded-For`，三个 key 里的两个（ip、pair）全部变化，`user:` key 仍存在**。

5. **限流强度**：`internal/api/routes.go:123` `authservice.NewLoginLimiter(5, 15*time.Minute)`。

#### 影响评估（不要夸大，也不要缩小）

- `user:` 这个 key **仍会命中**（它只用 username，不含 IP）⇒ **单纯换 XFF 无法无限爆破同一账号**：每个用户名仍是 15 分钟内 5 次失败上限。
- 真实可利用面是：① **账号枚举**——对大量不同用户名各试 5 次，永不触发 IP 维度熔断；② 若限流器设计意图含"按来源 IP 阻断扫描器"，该意图**完全失效**；③ `handler_account.go` 的改密限流同理。
- 严重度：**中**（不是"登录可无限爆破"，是"IP 维度防护失效 + 枚举不设限"）。

#### 修法（三选一，成本都很小）

| 方案 | 代码 | 适用性 |
|---|---|---|
| **A. 显式声明无代理**（当前部署形态的正确答案） | `cmd/server/main.go:629` 后加 `_ = r.SetTrustedProxies(nil)` | ✅ 推荐。当前 `docker-compose` 是端口直出，无代理 ⇒ 应让 `ClientIP()` 只信 `RemoteAddr` |
| **B. 指定可信网段**（若将来加 nginx） | `r.SetTrustedProxies([]string{"172.16.0.0/12"})`（docker 默认 bridge 网段） | 加反代时用这个 |
| **C. 限流 key 改用 `c.RemoteIP()`** | `context.go:1027` 的 `RemoteIP()` 只解析 `RemoteAddr`，**永不受 XFF 影响** | 兜底最稳，但加反代后所有请求同一 IP，限流粒度变粗 |

**推荐 A + 在 `routes.go:123` 附近加注释说明"限流 key 依赖 ClientIP ⇒ 改了 trusted proxies 必须同步评估"**。实施成本：**小**（1 行 + 1 注释）｜风险：若将来加反代而忘记改回 B，`ClientIP()` 会退化成反代 IP，届时按 IP 的限流变成全局共享——所以必须写注释。

> ⚠️ 归属声明：**这不是本次升级引入的**。gin 1.11.0 与 1.12.0 的 `trustedProxies` 默认值一字不差，本项目的 `gin.New()` 也从来没覆盖过。属"升级过程中顺带发现的既有安全债"。

### 2.3 gorm 1.31.2 的泛型 API —— 建议**不引入**

官方来源：https://gorm.io/docs/ （Generics 章节）/ `gorm.io/gorm/generics.go:147` `func G[T any](db *DB, opts ...clause.Expression) Interface[T]`

现状：`grep 'gorm.G\['` = **0**；全仓 340 处 `First/Find/Take/Last` 都是 `db.First(&x)` 风格。

能力：`gorm.G[User](db).Where("id = ?", id).First(ctx)` 返回 `(User, error)`，免去 `var u User` + `&u`；`gorm.G[T].Find(ctx)` 返回 `([]T, error)`。

**为什么建议不动**：
1. **收益是类型手感，不是性能**。`G[T]` 只是把 `reflect` 目标换成类型参数，底层 `Statement.Dest` 仍走反射。
2. **迁移面 340 处**，且 `G[T]` 的链式 API（`Where/Not/Or/Limit/Offset`）与本项目大量使用的 `Clauses(clause.OnConflict{...})`、`Clauses(clause.Locking{Strength:"UPDATE"})`（如 `handler_device.go:1002`）**在链式接口上不齐**（`ChainInterface` 有 `Where/Not/Or/Limit/Offset/Select/Omit/Group/Having/Order`，**没有 `Clauses`**）。要用 `OnConflict` 只能走 `gorm.G[T](db, clause.OnConflict{...})` 的可变参数入口 ⇒ 一半代码能转、一半不能，反而分裂。
3. 泛型 API 的 `gorm.G[T].Updates(ctx, T)` 要求传整个 T，与本项目大量"只更新部分列"的 `Updates(map[string]interface{})`（`dispatcher.go:353`、`sync_gate.go:428`）语义不匹配。

**结论：本项目不适用**（不是不能，是收益 < 成本，且会制造两套调用风格）。若要引入泛型，正确落点是 §2.6 的 API 层样板，而不是 gorm。

### 2.4 zap 1.28.0 的 `CheckPreWriteHook` / `CheckedEntry.Before` —— 本项目可用但无必要

官方来源：https://github.com/uber-go/zap/blob/master/CHANGELOG.md （1.28.0 / 27 Apr 2026，PR #1534）

能力：`zapcore.CheckPreWriteHook` 允许在 entry 写入任何 Core **之前**统一改写它。

本项目落点：`pkg/logger/logger.go:60-90` `Init()` 用 `zap.Config` + `EncoderConfig`（console 编码、ISO8601 时间、ShortCaller），没有 Core 级钩子；`logger.Infof` 等包装函数直接调 `SugaredLogger`。

- 能用在哪：如果要做"所有日志统一脱敏"或"统一注入 request_id"，这是 insert 点。
- 但本项目**已有**更简单的做法：`pkg/logger` 是唯一出口（全仓 `zap` 直接 import 只有 `pkg/logger/logger.go:6-7` 与 `internal/transport/server.go:35`），要统一改写在包装函数里做即可，不需要 zap 的 hook。
- **判定：不适用（已被项目自身的 logger 包装层覆盖）。** 成本 0，收益 0。

### 2.5 gorilla/websocket 1.5.3 / golang-jwt/v5 5.3.1 —— 版本未变

- `gorilla/websocket v1.5.3` 是当前最新（2024-06 发布后无新版本）。本项目 `internal/websocket/websocket.go`、`internal/terminal/ws_handler.go` 用法稳定（`WriteMessage`/`SetWriteDeadline`/`Hijack`），无新 API 可用。
- `golang-jwt/jwt v5.3.1` 同样是 v5 线最新。本项目 `internal/auth/token.go` + `internal/api/middleware.go:92` 用 `jwt.ParseWithClaims` + `SigningMethodHMAC` 校验，已是推荐写法。
- **两者均不适用（无 delta）。**

### 2.6 【低优先｜但可落地】`internal/api` 的绑定样板 + `bindJSONStrict` 覆盖不足

这是 Go 1.27 **泛型方法**（方法可声明自己的类型参数）在本仓库最自然的落点，但注意：**它属于 Go 1.27 语言特性，不是某个依赖的新特性**，故仅在此列一条，详述交给 `go127-stdlib.md`（task-6）。

现状证据：
- `internal/api` 有 **65 处** `c.ShouldBindJSON`、**68 处** `c.JSON`（非测试，`grep -c`）
- 已有 `internal/api/bind_json_strict.go:35` `func bindJSONStrict(c *gin.Context, dst any) error`（拒绝未知字段 + 拒绝尾随内容），但**只被 2 处调用**（`internal/api/handler_logstream.go:65`、`:119`；另 `bind_json_strict_test.go:36` 是测试）
- 该文件 `bind_json_strict.go:33` 的注释解释了为什么不用全局 `binding.EnableDecoderDisallowUnknownFields`（会改变 65 处 BindJSON 行为）——**这个判断是对的**

可落地形态（示例，**本次不实施**）：
```go
// 泛型方法挂在轻量包装类型上，避免污染 *gin.Context
type Ctx struct{ *gin.Context }
func (x Ctx) Bind[T any]() (T, error) { var v T; err := bindJSONStrict(x.Context, &v); return v, err }
```
- 收益：把 65 处"绑定失败 → `Error(c, 400, ...)` → return"的三行样板压成两行，且**顺带把 strict 语义铺开**（当前只有 2/65 处拒绝未知字段 ⇒ 63 处仍会静默忽略拼错的字段，这正是 `bind_json_strict.go:17-27` 记录的那类"假成功"缺陷的温床）。
- 成本：中（65 处逐点替换 + 每处都要确认未知字段拒绝不会破坏既有前端调用）
- 风险：中（strict 化可能让现存的宽松调用方 400）
- **判定：值得排期，但必须逐域灰度，不能一把梭。**

---

## 3. 不适用项汇总（逐条给原因）

| 特性 | 来源版本 | 不适用原因 |
|---|---|---|
| PG 生成列 / IDENTITY（`generated` tag） | gorm pg 1.6.3 | `internal/models/` 无生成列需求；`AutoMigrate` 已被裁剪为只建基础表（`internal/database/gorm.go:66-130`） |
| `Config.OptionOpenDB` | gorm pg 1.6.3 | 当前 `postgres.Open(dsn)` 形态用不上；且本仓库无自定义 PG 类型/多 schema（`grep search_path` 生产代码 = 0） |
| Index schema 限定修复 | gorm pg 1.6.3 | `grep DropIndex/RenameIndex/CreateIndex/HasIndex` 非测试 = 0 |
| `Rows.TypeMap` / `RowsColumnScanner` | pgx 5.11.0 | 生产走 gorm；仅仿真层 `simulation/harness/db.go` 用 pgx，不实现 `Rows` |
| date/timestamp 手写解析器 | pgx 5.11.0 | gorm 全二进制协议，不走文本路径；未设 `PreferSimpleProtocol` |
| OpenMetrics 2.0（`AcceptedFormats`） | client_golang 1.25.0 | Prometheus v3.15.1 对 OM2.0 支持仍实验；无消费方 |
| `api/prometheus/v1` 新签名 | client_golang 1.25.0 | 本仓库未 import 该子包 |
| http3 三处破坏性变更 | quic-go 0.63 | 无直接 import、无 `RunQUIC`；仅经 gin 被动编译 |
| gin `GetError/GetErrorSlice` | gin 1.12.0 | `c.Error(` 调用数 = 0，`c.Errors` 从未填充 |
| gin `Context.Delete` | gin 1.12.0 | 无需在中间件间删除 key |
| gin `Context.BSON` | gin 1.12.0 | API 全 JSON |
| gin uri/query `TextUnmarshaler` | gin 1.12.0 | `grep uri:\|form:` = 0，119 处参数手工 `c.Query()` |
| gin `UseEscapedPath` | gin 1.12.0 | 无特殊字符路由段需求 |
| gorm 泛型 `G[T]` | gorm 1.31.2 | 340 处迁移成本 + `ChainInterface` 无 `Clauses`，会分裂两套风格（见 §2.3） |
| zap `CheckPreWriteHook` | zap 1.28.0 | 已被 `pkg/logger` 包装层覆盖（见 §2.4） |
| sonic / goccy-go-json 加速 | 间接 | 未设 `-tags sonic` / `-tags go_json`，gin 默认走 `encoding/json` |
| msgpack（ugorji） | 间接 | 不用 msgpack；设备协议是自定义二进制帧（`pkg/frame`、`pkg/protoframe`） |
| bson / mongo | 间接 | 不用 bson；mongo 驱动纯属 gin 依赖 |
| protobuf render | 间接 | API 全 JSON |

---

## 4. 按收益排序的 TOP 5 建议动作

| # | 动作 | 落点 | 收益 | 成本 | 风险 | 优先级 |
|---|---|---|---|---|---|---|
| **1** | **核查 DB 密码是否含反斜杠/未引号空格**——pgx 5.11 的 libpq 对齐解析器会改写反斜杠、拒绝未引号空格；若命中则**升级后 PG 连不上** | `internal/database/gorm.go:29`（DSN 拼接）+ `.env`/`.secrets` 里的 `POSTGRES_PASSWORD`；必要时改 `postgres.Config{Conn: ...}` 走结构体 | 避免生产启动失败（**这是升级验证的必查项**） | 小 | 中（不查可能直接起不来） | **P0** |
| **2** | **`r.SetTrustedProxies(nil)` 修 `ClientIP()` 伪造**——当前 `gin.New()` 默认信任 0.0.0.0/0 且无反向代理，XFF 可任意伪造 ⇒ `login_limiter` 的 IP/pair 维度失效 | `cmd/server/main.go:629` 后加 1 行；注释关联 `internal/api/routes.go:123`、`internal/auth/login_limiter.go:32` | 恢复登录限流的来源维度，堵住账号枚举不设限 | 小（1 行 + 注释） | 低（将来加反代需改回指定网段，注释里写明） | **P0**（既有安全债，非升级引入） |
| **3** | **开 `TranslateError` + 用 `errors.Is(gorm.ErrDuplicatedKey)` 替换字符串匹配** | `internal/database/gorm.go:33` 加 `TranslateError: true`；改 `internal/database/migrate_gpio.go:244`、`internal/datasource/service.go:334`、`internal/dbschema/source_group.go:60` | 消除"PG 换文案 ⇒ 唯一键冲突静默失败"这一类缺陷（`ux_ds_one_active` 撞键曾造成 500，git `39d8617a`） | 小改 + 中回归 | 中（影响全部 340 处查询错误文案；SQLite 测试库翻译能力**未验证**） | **P1** |
| **4** | **重跑 `govulncheck`，把「12 条 CALLED → 0 条」留档** | 升级前基线 `.tmp-deps/govuln.txt`（go1.27.1 + x/net 0.57 + x/text 0.40，**12 条 CALLED**）；升级后重跑对比 | 12 条含 TLS 接入握手（#1/#10）、OTA 上传内存绕过（#9）、2 条 DoS（#6/#12）、2 条连接失步（#4/#11）——**整轮升级唯一可量化证明的收益** | 小（重跑 + 记录） | 无 | **P1**（收尾必做） |
| **5** | **给 `/metrics` 加 `CoalesceGather`**（顺带评估 `AcceptedFormats`） | `internal/api/routes.go:114`（+`:139`） | `/metrics` 未鉴权且外部可达，高频抓取时不再堆 goroutine；OpenMetrics 2.0 **暂不建议开** | 小 | 无 | **P2** |

**不建议做的事**（明确写出，避免后人重复踩）：
- ❌ 引入 `gorm.G[T]` 泛型 API（340 处 + `Clauses` 缺失，见 §2.3）
- ❌ 开 OpenMetrics 2.0（无消费方）
- ❌ 把 gin 换成 sonic/go_json build tag（本项目 65 处 BindJSON + 68 处 c.JSON 的兼容性风险 > 加速收益；且 Go 1.27 的 `encoding/json/v1→v2` 已自动带来 Unmarshal +15%，见 `go127-stdlib.md`）
- ❌ 任何 quic-go 相关改造（无调用路径）

---

## 附录：本次核实方法

| 项 | 方法 | 产物 |
|---|---|---|
| 版本 delta | `git diff HEAD -- backend/go.mod`（逐行） | 30 个模块变动（9 直接 + 20 间接 + 1 新增）+ `go` 指令；5 个未变，见 §0.1 |
| 各库新 API | 从 `proxy.golang.org` 拉 `.zip` 解包后 **逐文件 diff**（非只看 CHANGELOG） | `.tmp-deps/godiff/` |
| 落点证据 | `grep -rn` 于 `backend/`，行号即文件真实行号 | 全文引用 |
| CVE 可达性 | `.tmp-deps/govuln.txt`（`govulncheck -scan=symbol`，**升级前** go1.27.1 + x/net 0.57 + x/text 0.40 基线） | **12 条 CALLED**（逐条见 §1.6） |
| **pgx 5.10 vs 5.11 DSN 行为** | 写同一份探针 `main.go`，仅切 go.mod 版本，实测 8 组特殊字符密码 | `.tmp-deps/pgxprobe/`(5.11) 与 `.tmp-deps/pgxprobe510/`(5.10)，结果见 §1.4(a) |
| **json v1 vs v2 严格性** | 探针实测 5 组畸形输入（重复 key / 非法 UTF-8 / 未知字段 / 尾随内容 / 大小写），`.tmp-deps/jsonstrict/` | 见下表 |


### 附：json v1 vs v2 严格性实测（Go 1.27.3，探针 `.tmp-deps/jsonstrict/`）

| 输入 | `encoding/json`（v1，gin 默认走的路径） | `encoding/json/v2`（显式调用） |
|---|---|---|
| `{"a":"x","a":"y"}` 重复 key | **静默接受**，取后者 → `A:"y"` | **报错** `jsontext: duplicate object member name`，且 **v2 取第一个值** `A:"x"` |
| `{"a":"\xff"}` 非法 UTF-8 | **静默接受**，替换为 `A:"\uFFFD"` | **报错** `jsontext: invalid UTF-8 within "/a"` |
| `{"a":"x","zzz":1}` 未知字段 | 静默忽略 → `A:"x"` | 静默忽略 → `A:"x"`（要拒绝需 `jsonv2.RejectUnknownMembers(true)`） |
| `{"a":"x"}{"a":"y"}` 尾随内容 | **报错** `invalid character '{' after top-level value` | **报错** `jsontext: invalid character '{' after top-level value` |
| `{"A":"x","B":2}` 大小写不匹配 | 匹配成功（v1 默认大小写不敏感）→ `A:"x" B:2` | **不匹配** → `A:"" B:0`（要匹配需 `jsonv2.MatchCaseInsensitiveNames(true)`，但开了之后大小写变体又会被判为 duplicate） |

**对本项目的直接含义**：
1. **gin 默认走 `encoding/json`（v1）**（`codec/json/json.go` 的 build tag 未启用 sonic/jsoniter/go_json），所以 `c.ShouldBindJSON`（65 处）与 `c.JSON`（68 处）**当前仍是宽松行为**——重复 key 静默取后者、非法 UTF-8 静默替换。这正是 `internal/api/bind_json_strict.go:17-27` 记录的那类"假成功"缺陷的同源问题。
2. 若改用 v2（或用 `bindJSONStrict` 铺开），**重复 key 会从"静默取后者"变成"报错"**——这是个**语义反转**（v1 取后、v2 取前），升级前必须先确认没有调用方依赖"重复 key 后者生效"。
3. `bindJSONStrict` 用的是 v1 + `DisallowUnknownFields()`，**拦不住重复 key**（v1 根本不报）。若要把重复 key 也纳入严格语义，只能走 v2 或 `jsontext`。**本条留给 `go127-stdlib.md`（task-6）展开，本文件只记录实测事实。**

> 注：`docs/分析/依赖优化/go127-stdlib.md`（task-6）负责 Go 1.27 语言/标准库层面的落点（json/v2、泛型方法、goroutineleak profile 等），本文件只覆盖**第三方依赖**。两文件在 §2.6 与 §4 P0 有交叉引用。
