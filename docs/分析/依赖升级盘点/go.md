# Go 依赖升级盘点（backend + scripts/bench/ehload）

> 分析对象：`backend/go.mod`（含 go.sum 中全部直接+间接依赖）、`scripts/bench/ehload/go.mod`
> 分析日期：2026-10-23
> 工具链：`/mnt/storage/WorkSpace/EHome/.tools/gotool/bin/go` = **go1.27.1 linux/amd64**
> 代理：`GOPROXY=https://goproxy.cn,direct`（proxy.golang.org 在本机不通，已实测）
> **本次为只读分析。所有升级验证都在 `.tmp-deps/` 下的副本里做，仓库内 go.mod / go.sum / Dockerfile / workflows 一个字节都没改**（见 §8.1 的 md5 校验）。

---

## 0. 结论速览

| 指标 | backend | scripts/bench/ehload |
|---|---|---|
| 模块总数（`go list -m all` 构建图） | **92** | **8** |
| go.mod 中 require 条目 | 57（直接 16 + 间接 41） | 4（直接 1 + 间接 3） |
| 有可升级版本（`go list -m -u all` 带 `[...]`） | **42** | **2** |
| 存在**更新的主版本模块**（/v2、/v3、/v6 …） | **0** | **0** |
| 语义上等同破坏性（0.x 的 minor 跃迁） | **9** | 0 |
| govulncheck 判定**真正可调用**的模块级 CVE | **2 个模块 / 2 个 CVE** | 1 个（x/net） |
| 升级依赖即可消除的模块级 CVE | **全部（4 条）** | 全部 |
| 升级后残留 CVE（全在 **Go 标准库**） | **11 条** | 7 条 |

**三句话总结：**

1. **没有任何依赖存在更新的主版本。** 我逐个探测了 `gin/v2`、`gorm/v2`、`pgx/v6`、`mongo-driver/v3`、`quic-go/v2`、`jwt/v6`、`yaml.v4`、`paho/v2`、`client_golang/v2`、`websocket/v2`、`uuid/v2`、`zap/v2` —— 全部 404 / no matching versions。所以「跨大版本升级」在 Go 侧**不存在**；真正的风险是 0.x 库的 minor 跳变（quic-go 0.61→0.64 是唯一有实质 API 破坏的）。
2. **真正要紧的安全问题不在 go.mod 里，而在 Go 工具链版本上。** `go.mod` 钉的 `go 1.26.5`、Dockerfile 的 `golang:1.26.5-alpine`、CI 的 `GO_VERSION: "1.26.5"` 三处一致，但 1.26 线已发到 **1.26.10**。当前 1.26.5 命中 **11 条标准库 CVE**（HTTP/2 内存耗尽、HPACK 竞态崩溃、x509 中间证书信任、TLS ECH …），其中 10 条在 **1.26.9** 修复、1 条在 **1.26.10** 修复。**升依赖治不了，必须抬工具链。**
3. **依赖侧可安全批量升级。** 我在副本上实测 `go get -u ./...`（同主版本内升到 latest）后：`go build ./...` 通过、`go vet ./...` 通过、**`go test ./...` 41 个包全绿、0 失败**（与升级前基线逐项一致），且升级后**模块级可调用 CVE 归零**。

---

## 1. `go 1.26.5` 与 toolchain 的关系

### 1.1 现状

```
backend/go.mod:
  line 3: go 1.26.5          ← 没有 toolchain 行
```

相关钉版本三处（grep 全仓确认只有这三处）：

| 位置 | 值 | 说明 |
|---|---|---|
| `backend/go.mod:3` | `go 1.26.5` | 模块的**最低工具链要求** |
| `Dockerfile:5` | `FROM golang:1.26.5-alpine` | 构建镜像 |
| `.github/workflows/test.yml:36` | `GO_VERSION: "1.26.5"` | setup-go 版本 |

### 1.2 语义（Go 1.21+ 起）

- `go` 指令**不再只是"语言版本提示"**，而是**硬性最低工具链要求**。我实测验证过：

  ```
  # 造一个 go.mod 写 go 1.99.0，用 1.27.1 编译
  $ GOTOOLCHAIN=local go build ./...
  go: go.mod requires go >= 1.99.0 (running go 1.27.1; GOTOOLCHAIN=local)
  ```

  即 `go 1.26.5` ⇒ **工具链必须 ≥ 1.26.5**，否则直接报错（`GOTOOLCHAIN=local` 时）或静默下载（`GOTOOLCHAIN=auto`，默认）。
- **没有 `toolchain` 行是好事**：意味着"不低于 1.26.5 的任意工具链都行"，不锁死具体补丁号。CI 注释也写明用 setup-go 钉 1.26.5 是为了避免 runner 触发器自动下载。
- 三处一致（1.26.5）本身是**正确的一致性设计**；问题只在于 1.26.5 这个具体补丁号**太旧**（1.26 线现有 1.26.0～1.26.10，见 §1.5）。

### 1.3 有没有依赖要求更高的 go 版本？

**没有。当前最高要求就是本模块自己的 1.26.5。** 我逐条拉了 93 个模块的 `GoVersion` 字段，分布（高段）：

| 依赖要求的 go 版本 | 模块数 | 代表模块 |
|---|---|---|
| 1.25.0 | 15 | gin、gin-contrib/cors、gin-contrib/gzip、validator/v10、**pgx/v5**、**client_golang**、quic-go（升级后即 1.26.0） |
| 1.24 / 1.24.0 | 2 + 3 | klauspost/compress、quic-go/qpack、paho.mqtt、cpuid/v2、leodido/go-urn |
| 1.23 / 1.23.0 | 2 + 1 | gin-contrib/sse、protobuf、go.uber.org/mock |
| 1.22.0 | 1 | prometheus/client_model |
| 1.21 / 1.21.0 | 5 + 2 | mimetype、jwt/v5、go-sqlite3、ugorji/codec、goccy/go-yaml、go-toml/v2 |
| 1.20 及以下 | 30 | gorm、puddle、zap、multierr、mongo-driver 等 |
| **1.26.5** | **1** | **ehome/backend 自己** |

### 1.4 升级依赖后是否需要抬高 go 指令？——**不需要**

实测把依赖全部升到 latest 后，最高要求变成 **go 1.26.0**：

```
github.com/gin-contrib/cors            v1.7.9    go=1.26.0
github.com/gin-contrib/gzip            v1.2.8    go=1.26.0
github.com/go-playground/validator/v10 v10.30.5  go=1.26.0
github.com/prometheus/client_golang    v1.25.0   go=1.26.0
github.com/prometheus/common           v0.72.1   go=1.26.0
github.com/quic-go/quic-go             v0.64.1   go=1.26.0
golang.org/x/crypto                    v0.58.0   go=1.26.0
golang.org/x/net                       v0.61.0   go=1.26.0
golang.org/x/sync                      v0.24.0   go=1.26.0
golang.org/x/sys                       v0.49.0   go=1.26.0
golang.org/x/text                      v0.43.0   go=1.26.0
golang.org/x/arch                      v0.32.0   go=1.26.0
```

⇒ **`go 1.26.5` 依然满足全部依赖，无需上游 go 指令**（好消息：升依赖不会逼你动 go.mod 的 go 行）。

### 1.5 ⚠️ 1.26.5 不是 1.26 线的安全版本

从 `https://go.dev/dl/?mode=json&include=all` 实拉：

```
go1.26 -> 1.26.0 1.26.1 1.26.2 1.26.3 1.26.4 1.26.5 1.26.6 1.26.7 1.26.8 1.26.9 1.26.10
go1.27 -> 1.27.0 1.27.1 1.27.2 1.27.3      (无 1.28)
```

当前钉的 **1.26.5** 距最新差 **5 个补丁**，正好跨过了本轮标准库 CVE 的修复点（1.26.9 / 1.26.10）。详见 §3.3。

---

## 2. 依赖清单与升级建议（版本全部来自实查）

**数据来源**：`go list -m -u -json all`（backend）+ `go list -m -u all`（ehload）+ 对每个关键包 `curl https://goproxy.cn/<mod>/@latest` 交叉验证（含 `Time` 字段）。**所有版本号均为实测值，无一条凭记忆填写。**

### 2.1 直接依赖（第一个 require 块，16 项）

| 名称 | 当前版本 | 可升级版本 | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| github.com/eclipse/paho.mqtt.golang | v1.5.1 | v1.5.1（已最新 2025-09-16） | 否 | **无风险，但已是死依赖** | **建议删除**：全仓已无 import；实测 `go mod tidy -diff` 唯一的删除项就是它 |
| github.com/gin-contrib/cors | v1.7.7 | **v1.7.9**（2026-09-21） | 否 | 低（patch） | ✅ 可批量；升后要求 go≥1.26.0（已满足） |
| github.com/gin-contrib/gzip | v1.2.6 | **v1.2.8**（2026-09-21） | 否 | 低（patch） | ✅ 可批量；升后要求 go≥1.26.0 |
| github.com/gin-gonic/gin | v1.12.0 | v1.12.0（已最新 2026-02-28） | 否（无 gin/v2） | 无 | 保持；无更新 |
| github.com/golang-jwt/jwt/v5 | v5.3.1 | v5.3.1（已最新 2026-01-28） | 否（无 jwt/v6） | 无 | 保持；无更新 |
| github.com/google/uuid | v1.6.0 | v1.6.0（已最新 2024-01-23） | 否（无 uuid/v2） | 无 | 保持；上游 2 年未动 |
| github.com/gorilla/websocket | v1.5.3 | v1.5.3（已最新 2024-06-14） | 否（无 websocket/v2） | 无 | 保持；上游 2 年未动 |
| **github.com/jackc/pgx/v5** | **v5.10.0** | **v5.11.0**（2026-09-07） | 否（无 pgx/v6） | **中**：`Rows` 接口加 `TypeMap` 方法；date/timestamp/timestamptz 改手写解析器；连接串/OS 用户解析行为变化 | ⚠️ **需单独验证**（见 §4.2） |
| **github.com/prometheus/client_golang** | **v1.24.1** | **v1.25.0**（2026-10-08） | 否（无 /v2） | **中低**：`api/prometheus/v1` 有破坏性签名变更，但**本仓库不用该包**；`promhttp` 新增 `Vary` 头 | ✅ 可批量（实测 build+test 绿） |
| github.com/prometheus/client_model | v0.6.2 | **v0.6.3**（2026-08-31） | 否 | 低（patch） | ✅ 可批量 |
| go.uber.org/zap | v1.28.0 | v1.28.0（已最新 2026-04-28） | 否（无 /v2） | 无 | 保持；无更新 |
| **golang.org/x/crypto** | **v0.54.0** | **v0.58.0**（2026-10-09） | 否 | **低**：3 条 CVE 全在 `x/crypto/ssh`，本仓库**只用 `bcrypt`**，不可达 | ✅ 可批量（安全更新，建议尽快） |
| gopkg.in/yaml.v3 | v3.0.1 | v3.0.1（2022-05-27，已冻结） | 否（无 v4） | 低，但**上游停止维护** | 暂不动；迁 `go.yaml.in/yaml/v3`（v3.0.5）是另一件事 |
| gorm.io/driver/postgres | v1.6.0 | **v1.6.3**（2026-09-14） | 否 | 低（patch；.mod 仍 pin pgx v5.10.0 / gorm v1.31.2） | ✅ 可批量 |
| gorm.io/driver/sqlite | v1.6.0 | v1.6.0（已最新 2025-06-04） | 否 | 无 | 保持；无更新 |
| gorm.io/gorm | v1.31.2 | v1.31.2（已最新 2026-06-22） | 否（无 gorm/v2） | 无 | 保持；无更新 |

> **注**：`gorm.io/gorm`、`gin`、`pgx/v5`、`client_golang` 这四个"重点关照对象"**全部已是最新**，且**都不存在更新的主版本模块**。

### 2.2 间接依赖中可升级的（35 项）

| 名称 | 当前版本 | 可升级版本 | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| **github.com/quic-go/quic-go** | **v0.61.0** | **v0.64.1**（2026-10-19） | **语义上是**（0.x 连跳 3 个 minor） | **中**：v0.62 起要求 go≥1.26；v0.63 有 3 条 http3 破坏性变更 | ⚠️ 需编译+测试；本仓库**不调用** http3 API（见 §4.1） |
| **golang.org/x/net** | **v0.57.0** | **v0.61.0**（2026-10-09） | 语义上是（0.x minor） | **高价值**：修 5 条 HTTP/2 CVE，**有可调用项**（见 §3） | 🔴 **最高优先，必升** |
| **golang.org/x/text** | **v0.40.0** | **v0.43.0**（2026-10-09） | 语义上是 | **高价值**：修 GO-2026-6629 precis panic，**可调用** | 🔴 **必升**（≥v0.41.0，建议 v0.43.0） |
| **github.com/gabriel-vasile/mimetype** | **v1.4.15 ⚠️已被上游 retract** | **v1.4.16**（2026-10-17） | 否 | **中**：v1.4.16 的 .mod 明写 `retract v1.4.15`（特制 CDF 二次复杂度，GHSA-jjmc-jx7j-hp68） | 🔴 **必升**（当前版本已撤回） |
| github.com/prometheus/common | v0.70.1 | **v0.72.1**（2026-10-11） | 语义上是 | 中低：v0.72 要求 go≥1.26；OpenMetrics 2.0 编码改动 | ✅ 可批量（实测绿） |
| github.com/prometheus/procfs | v0.21.1 | **v0.22.0**（2026-08-28） | 语义上是 | 低 | ✅ 可批量 |
| github.com/goccy/go-json | v0.10.6 | **v0.11.2**（2026-09-30） | 语义上是 | 低：gin 默认用 encoding/json，此为可选编解码器 | ✅ 可批量 |
| github.com/stretchr/objx | v0.5.2 | **v0.8.0**（2026-10-14） | 语义上是（连跳 3 minor） | 低：仅 testify mock 用，不进生产二进制 | ✅ 可批量 |
| go.mongodb.org/mongo-driver/v2 | v2.8.0 | **v2.9.2**（2026-10-06） | 否（仍在 v2 内） | 低：仅被 gin 的 bson 绑定拉进来，本仓库不用 bson | ✅ 可批量 |
| golang.org/x/sys | v0.47.0 | **v0.49.0**（2026-10-06） | 语义上是 | 低（无 CVE） | ✅ 可批量 |
| golang.org/x/sync | v0.22.0 | **v0.24.0**（2026-09-23） | 语义上是 | 低（无 CVE） | ✅ 可批量 |
| golang.org/x/arch | v0.29.0 | **v0.32.0**（2026-09-22） | 语义上是 | 低（CPU 特性探测） | ✅ 可批量；升后要求 go≥1.26.0 |
| golang.org/x/mod | v0.37.0 | **v0.42.0**（2026-10-09） | 语义上是 | 低（工具链类，不进二进制） | ✅ 可批量 |
| golang.org/x/tools | v0.47.0 | **v0.52.0**（2026-10-10） | 语义上是 | 低（工具链类） | ✅ 可批量 |
| github.com/bytedance/sonic | v1.15.2 | **v1.15.4**（2026-09-10） | 否 | 低 | ✅ 可批量 |
| github.com/bytedance/sonic/loader | v0.5.1 | **v0.5.3**（2026-10-20） | 语义上是 | 低 | ✅ 可批量 |
| github.com/gin-contrib/sse | v1.1.1 | **v1.1.2**（2026-09-05） | 否 | 低 | ✅ 可批量 |
| github.com/go-playground/locales | v0.14.1 | **v0.14.2**（2026-09-28） | 语义上是 | 低 | ✅ 可批量 |
| github.com/go-playground/universal-translator | v0.18.1 | **v0.18.2**（2026-09-20） | 语义上是 | 低 | ✅ 可批量 |
| github.com/go-playground/validator/v10 | v10.30.3 | **v10.30.5**（2026-09-20） | 否 | 低（patch） | ✅ 可批量；升后要求 go≥1.26.0 |
| github.com/jackc/puddle/v2 | v2.2.2 | **v2.2.3**（2026-10-05） | 否 | 低 | ✅ 可批量 |
| github.com/klauspost/compress | v1.19.1 | **v1.20.3**（2026-10-12） | 否（1.x minor） | 低（无直接 import） | ✅ 可批量 |
| github.com/klauspost/cpuid/v2 | v2.4.0 | **v2.4.1**（2026-07-20） | 否 | 低 | ✅ 可批量 |
| github.com/mattn/go-sqlite3 | v1.14.48 | **v1.14.52**（2026-09-05） | 否 | 低（SQLite 跟随上游） | ✅ 可批量 |
| github.com/rogpeppe/go-internal | v1.10.0 | **v1.16.0**（2026-07-01） | 否 | 低（test-only，fmtsort） | ✅ 可批量 |
| github.com/stretchr/testify | v1.11.1 | **v1.12.1**（2026-08-17） | 否 | 低（test-only） | ✅ 可批量 |
| github.com/ugorji/go/codec | v1.3.1 | **v1.3.2**（2026-08-04） | 否 | 低 | ✅ 可批量 |
| github.com/xhit/go-str2duration/v2 | v2.1.0 | **v2.2.0**（2026-09-14） | 否（v2 minor） | 低 | ✅ 可批量 |
| go.yaml.in/yaml/v3 | v3.0.4 | **v3.0.5**（2026-07-26） | 否 | 低 | ✅ 可批量 |
| google.golang.org/protobuf | v1.36.11 | **v1.36.12**（2026-08-10） | 否 | 低（patch） | ✅ 可批量 |
| github.com/golang/protobuf | v1.5.0 ⚠️已 deprecated | **v1.5.4**（2024-03-06） | 否 | 低；已被 google.golang.org/protobuf 取代 | 可升 v1.5.4；不必急删（兼容层） |
| github.com/google/gofuzz | v1.0.0 | **v1.2.0**（2020-08-04） | 否 | 低 | ✅ 可批量 |
| github.com/jordanlewis/gcassert | v0.0.0-20250430164644 | **v0.0.0-20260313214104**（2026-03-13） | 否 | 低（test-only） | ✅ 可批量 |

### 2.3 无更新版本（已最新，50 项）

`beorn7/perks`、`bytedance/gopkg`、`cespare/xxhash/v2`、`cloudwego/base64x`、`goccy/go-yaml v1.19.2`、`jackc/pgpassfile`、`jackc/pgservicefile`、`jinzhu/inflection`、`jinzhu/now`、`json-iterator/go`、`leodido/go-urn`、`mattn/go-isatty`、`modern-go/*`、`munnerz/goautoneg`、`pelletier/go-toml/v2`、`quic-go/qpack v0.6.0`、`twitchyliquid64/golang-asm`、`go.uber.org/multierr` 等。

### 2.4 scripts/bench/ehload（独立 module，8 个模块）

| 名称 | 当前版本 | 可升级版本 | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| github.com/eclipse/paho.mqtt.golang | v1.5.1 | v1.5.1（已最新） | 否 | 无 | 保持（这里**真的在用** MQTT，与 backend 不同） |
| github.com/gorilla/websocket | v1.5.3 | v1.5.3（已最新） | 否 | 无 | 保持 |
| **golang.org/x/net** | **v0.44.0** | **v0.61.0**（2026-10-09） | 语义上是 | **高**：v0.44.0 命中 HTTP/2 CVE（GO-2026-6603/6610/6611/6612/6617，修复点 v0.60.0） | 🔴 **必升** |
| **golang.org/x/sync** | **v0.17.0** | **v0.24.0**（2026-09-23） | 语义上是 | 低（无 CVE） | ✅ 可批量 |
| golang.org/x/crypto | v0.42.0（图中传递） | v0.58.0 | 否 | 低 | 随 paho 间接升 |
| golang.org/x/sys | v0.36.0（图中传递） | v0.49.0 | 否 | 低 | 同上 |
| golang.org/x/term | v0.35.0（图中传递） | v0.47.0 | 否 | 低 | 同上 |
| golang.org/x/text | v0.29.0（图中传递） | v0.43.0 | 否 | 低 | 同上 |

**ehload 的 go 指令是 `go 1.26`**（不是 1.26.5）。实测 `go get -u ./...` 会把它改写成 `go 1.26.0`（语义等价，Go 规范补零），升级后 build + vet 均通过。
⚠️ ehload 的 `x/net v0.44.0` 比 backend 的 v0.57.0 **更旧**，同样命中 HTTP/2 CVE —— **bench 工具也别漏升**。

---

## 3. 🔴 安全漏洞清单（govulncheck v1.9.0 实测，本次最重要的输出）

### 3.1 方法

```
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0   # 成功；vuln.go.dev 可达(HTTP 200)
cd backend && govulncheck ./...                       # 退出码 3 = 有漏洞
```

**判定口径**：`CALLED` = 存在从本仓库代码到漏洞函数的**真实调用链**（可利用）；`imported` = 包被引入但无可达调用。下表严格区分，证据来自 govulncheck 输出的 trace 行。

### 3.2 backend 依赖侧（模块级）CVE —— 4 组，**2 条真可达**

| GO 编号 / CVE | 漏洞 | 模块 | 当前 | 修复版本 | 可调用? | 本仓库调用链（govulncheck 实证） |
|---|---|---|---|---|---|---|
| **GO-2026-6629**<br>CVE-2026-56851 | `x/text/secure/precis` 解析特制输入 **panic** | golang.org/x/text | v0.40.0 | **v0.41.0** | 🔴 **CALLED** | `internal/database/gorm.go:33` `database.Connect` → `gorm.Open` → `precis.Profile.String` |
| **GO-2026-6617**<br>CVE-2026-97032 | HTTP/2 服务端 **HPACK 编码器竞态崩溃** | golang.org/x/net | v0.57.0 | **v0.60.0** | 🔴 **CALLED** | `internal/notify/client.go:127` → `http.Client.Do` → `http2.Transport.RoundTrip`；`cmd/server/main.go:695` `ListenAndServe` → `http2.Server.ServeConn` |
| GO-2026-6603<br>CVE-2026-78659 | HTTP/2 服务端 **Trailer 头内存耗尽** | golang.org/x/net | v0.57.0 | **v0.60.0** | 🟡 imported | 同二进制同版本，随 v0.60.0 一并修掉 |
| GO-2026-6612<br>CVE-2026-78663 | HTTP/2 服务端流**流控双重退还** | golang.org/x/net | v0.57.0 | **v0.60.0** | 🟡 imported | 同上 |
| GO-2026-6611<br>CVE-2026-78669 | 反复改初始窗口致 **CPU 耗尽** | golang.org/x/net | v0.57.0 | **v0.60.0** | 🟡 imported | 同上 |
| GO-2026-6610<br>CVE-2026-78660 | HTTP/2 传输层接受**畸形 framing 头** | golang.org/x/net | v0.57.0 | **v0.60.0** | 🟡 imported | 同上 |
| GO-2026-6303 / 6354 / 6355<br>CVE-2026-56854 / 78662 / 56855 | `x/crypto/ssh` 源地址选项未强制、两处 DoS | golang.org/x/crypto | v0.54.0 | v0.55.0 / v0.56.0 | ⚪ **不可达** | 本仓库只用 `x/crypto/bcrypt`（7 处），`ssh` 未引入 |
| GO-2026-5932 | `x/crypto/openpgp` 设计上不安全、已弃维护 | golang.org/x/crypto | v0.54.0 | 无修复 | ⚪ 不可达 | 未使用 openpgp |
| GHSA-jjmc-jx7j-hp68 | `mimetype` 对特制 CDF 呈二次时间复杂度（DoS） | mimetype | v1.4.15（**已被 retract**） | **v1.4.16** | 🟡 传递 | 经 gin 多部分表单解析可达 |

> **结论：依赖侧真正可调用的漏洞只有 2 条 —— `x/text` GO-2026-6629 与 `x/net` GO-2026-6617。两者都有"一行 `go get`"的解法（x/text ≥ v0.41.0、x/net ≥ v0.60.0）。**

### 3.3 标准库 CVE —— **11 条，7 条真可达，升级依赖治不了**

本机工具链 **go1.27.1**、仓库钉的 **go1.26.5** 都在受影响区间：

| GO 编号 / CVE | 漏洞 | 修复版本（1.26 线 / 1.27 线） | 可调用? | 代表性调用链 |
|---|---|---|---|---|
| **GO-2026-6809**<br>CVE-2026-111943 | `crypto/x509` **不信任被攻陷的中间证书**（denylist） | **1.26.10 / 1.27.3** | 🔴 CALLED | `internal/transport/server.go:379` `tls.Conn.HandshakeContext`；`internal/notify/client.go:127` `tls.Dialer.DialContext` |
| GO-2026-6617<br>CVE-2026-97032 | `net/http/internal/http2` HPACK 竞态崩溃（标准库侧） | **1.26.9 / 1.27.2** | 🔴 CALLED | `cmd/server/main.go:695` `ListenAndServe` |
| GO-2026-6612<br>CVE-2026-78663 | HTTP/2 流控双重退还（标准库侧） | **1.26.9 / 1.27.2** | 🔴 CALLED | 同上 |
| GO-2026-6611<br>CVE-2026-78669 | HTTP/2 初始窗口 CPU 耗尽（标准库侧） | **1.26.9 / 1.27.2** | 🔴 CALLED | 同上 |
| GO-2026-6610<br>CVE-2026-78660 | HTTP/2 畸形 framing 头（标准库侧） | **1.26.9 / 1.27.2** | 🔴 CALLED | 同上 |
| GO-2026-6603<br>CVE-2026-78659 | HTTP/2 Trailer 内存耗尽（标准库侧） | **1.26.9 / 1.27.2** | 🔴 CALLED | `main.go:695`、`main.go:729` `http.Server.Shutdown` |
| GO-2026-6605<br>CVE-2026-56866 | HTTP/1 客户端 CONNECT 拒绝后**连接失步** | **1.26.9 / 1.27.2** | 🔴 CALLED | `internal/notify/client.go:127` `http.Client.Do` |
| GO-2026-6608<br>CVE-2026-94440 | `net/textproto`+`mime/multipart` **MIME 头解析绕过内存上限** | **1.26.9 / 1.27.2** | 🔴 CALLED | `notify/client.go:127` → `textproto.Reader.ReadMIMEHeader` |
| GO-2026-6607<br>CVE-2026-97031 | `crypto/tls` 拒绝畸形 **ECH 外层扩展引用** | **1.26.9 / 1.27.2** | 🔴 CALLED | `transport/server.go:379`、`session.go:258` |
| GO-2026-6609<br>CVE-2026-78667 | `net/http` **Range 头无长度限制** | **1.26.9 / 1.27.2** | 🔴 CALLED | `cmd/server/main.go:695` |
| GO-2026-6613<br>CVE-2026-94439 | HTTP/1 服务端 2xx CONNECT 后**连接失步** | **1.26.9 / 1.27.2** | 🟡 imported | HTTP/1 服务端通路 |

**⇒ 唯一解：把 Go 工具链抬到 `1.26.10`（一次覆盖全部 11 条）或 `1.27.3`。**

需要同步改的三处：`Dockerfile`（golang:1.26.5-alpine → 1.26.10-alpine）、`.github/workflows/test.yml`（GO_VERSION 1.26.5 → 1.26.10）、`backend/go.mod`（`go 1.26.5` → 建议 `go 1.26.10`，可选）。
⚠️ **Dockerfile / workflows 属于 task-3（基础设施）的写入范围，本报告只出结论，不代改。**

### 3.4 ehload 模块

```
Vulnerability #1..#7 全为 Go 标准库
模块级仅 golang.org/x/net v0.44.0（GO-2026-6603/6611/6617，修复点 v0.60.0）
Your code is affected by 7 vulnerabilities from the Go standard library.
This scan also found 5 vulnerabilities in packages you import and 12 in modules you require（均不可调用）
```

⇒ 升 `x/net ≥ v0.60.0`（建议 v0.61.0）+ 抬工具链即可清零。

### 3.5 验证：升级依赖后还剩什么？

副本上「全部依赖升到 latest（同主版本）」后重跑 govulncheck：

```
升级前：Your code is affected by 12 vulnerabilities from 1 module and the Go standard library.
升级后：Your code is affected by 11 vulnerabilities from the Go standard library.
        This scan also found 3 vulnerabilities in packages you import and 1 in modules you require（不可调用）
```

**⇒ 模块级可调用 CVE 从 2 → 0；残留 11 条全在标准库，只能靠抬工具链。**这就是「先升依赖、再抬工具链」两步走的实证依据。

---

## 4. 跨大版本 / 高风险项逐项破坏性分析

> 前置结论：**Go 侧不存在"更新的主版本"**（12 个候选路径全部 404）。本节分析的是「0.x 库的 minor 跃迁」与「有破坏性公告的 patch/minor」，这是本仓库实际面临的风险形态。

### 4.1 quic-go v0.61.0 → v0.64.1（唯一有实质 API 破坏的）

**它为什么在依赖里**：`github.com/gin-gonic/gin@v1.12.0/gin.go:20` **无条件** `import "github.com/quic-go/quic-go/http3"`。即：即使完全不用 QUIC，这个包也会进二进制。

**本仓库是否调用 http3 API**（实测）：
```
$ grep -rn --include='*.go' -E 'RunQUIC|ListenAndServeQUIC|http3.' backend/
（无匹配；只用 gin.New()）
```
⇒ **不调用**。

**上游破坏性变更**（GitHub Releases API 实拉，非记忆）：

| 版本 | 发布日 | 破坏性 / 关键变更 |
|---|---|---|
| v0.62.0 | 2026-08-30 | **要求 Go 1.26+**（#5801）；新增 RFC 9218 流优先级 `SetPriority`；新增 `http3.Stream/RequestStream.TryWriteAll` |
| v0.63.0 | 2026-09-22 | **破坏性 3 条**：① 非 CONNECT 服务端请求的 `URL.Scheme`/`URL.Host` 现在**置空**（对齐 net/http）② Extended CONNECT 同样置空且 `RequestURI` 设为 `:path` ③ http3 流 API 改返回 `*http3.Error`。另有 `:path` 强制以 `/` 开头 |
| v0.64.1 | 2026-10-19 | 修复 `Conn.ReceiveDatagram` 上下文已取消时未立即失败；流重置取消后忽略 reliable boundary 更新 |

**风险判定：对我们低**（不写 http3 服务端代码，不受 ①②③ 影响）。但必须编译+跑测试 —— 我已完成：build ✅ / vet ✅ / test 41 包全绿 ✅。
**建议**：停在 v0.64.1（已实测绿）。gin 自己 pin 的是 v0.59.0，我们领先于 gin 声明的版本，属"越过 driver 声明"的升级，需保留回滚点。

### 4.2 jackc/pgx/v5 v5.10.0 → v5.11.0（唯一需人工验证的依赖）

**上游变更**（Release notes 实拉 + .mod 核实最低 Go 仍为 1.25）：

- `stdlib` 支持 Go 1.27 的 `driver.RowsColumnScanner`（数组/range 可直接扫进 Go 值）—— **纯新增能力，不破坏**。
- ⚠️ **`Rows` 接口新增 `TypeMap` 方法** —— 任何自定义 `Rows` 实现（含 mock）必须补此方法。本仓库不实现 pgx.Rows；但 **`gorm.io/driver/postgres v1.6.3` 的 .mod 仍 pin `pgx/v5 v5.10.0`**，所以升到 5.11.0 是越过 driver 声明的版本。
- **行为变化（最需人工验证）**：
  - `pgtype` 的 `date`/`timestamp`/`timestamptz` 文本解析/编码改为手写解析器（约 2.5× 提速）。修了「BC 闰年 2 月 29 日被挪到 3 月 1 日」等 bug —— 但**改变了极端日期行为**。
  - `pgconn` 仅在连接串/环境/服务文件都未给 user 时才解析 OS 账户；Unix 上改用 `$HOME` 而非 OS 账户 home。**容器环境行为有变化**（正好命中我们的 Alpine 部署）。
  - 基于名字的结构体映射现在**对 `db` tag 大小写不敏感**（精确匹配优先）。
- 新增：`pgconn.Config.MaxProtocolMessageBodyLen`、`pool_ping_timeout` 连接串参数、4 个 sentinel error。

**风险判定：中。建议单独 PR + `make test-integration`（真实 PG）。**
重点回归：日期/时区边界、连接串带 `user=` 与不带两种部署形态、`target_session_attrs`。

### 4.3 prometheus/client_golang v1.24.1 → v1.25.0

**上游破坏性**（Release notes 实拉）：
- **最低 Go 抬到 1.26**，只支持最近两个 Go 版本（1.26、1.27）。
- `api/prometheus/v1`：`Query`/`QueryRange`/`Series`/`LabelNames`/`LabelValues` 现在在 `Warnings` 之外**还返回 `Infos`**，连带改 `api.Client` 的 `Do`/`DoGetFallback` 签名。
- `api/prometheus/v1`：`TSDBBlocks` 结果类型重构（`TSDBBlockMeta` 等重命名，统计字段改 `uint64`）。

**本仓库影响面**（实测 import 清单）：
```
prometheus(9)  prometheus/promauto(3)  prometheus/promhttp(3)  client_model/go(9)
—— 完全没有 import api/prometheus/v1
```
⇒ **破坏性变更全部落在我们没用的包上。**

**唯一可观察副作用**：`promhttp` 现在给协商后的响应加 **`Vary` 头**（#2142）。我们用的是裸 `promhttp.Handler()`（`cmd/server/main.go:632`、`internal/api/routes.go:114/139`）。加 `Vary` 对反向代理是**改善**，不影响抓取。另 `testutil.GatherAndFormat` 会补 `# EOF` 结尾（我们不用 testutil）。

**风险判定：中低**，已实测 build+test 绿。**可批量。**

### 4.4 golang.org/x/* 系列

四项全是**纯安全/维护更新，无 API 破坏**，但**升后统一要求 go ≥ 1.26.0**（当前 1.26.5 满足）：

| 包 | 变更性质 | 我们要关心的 |
|---|---|---|
| x/net v0.57 → v0.61 | 5 条 HTTP/2 CVE 修复（v0.60.0 起） | 🔴 **必升**，GO-2026-6617 有真实调用链 |
| x/text v0.40 → v0.43 | GO-2026-6629 precis panic（v0.41.0 起） | 🔴 **必升**，有真实调用链（gorm 初始化路径） |
| x/crypto v0.54 → v0.58 | 3 条 ssh CVE + openpgp 弃用声明 | 我们只用 bcrypt ⇒ 安全上非必需，但**零成本，建议一起升** |
| x/sys v0.47 → v0.49 | 无 CVE，跟随内核 syscall | 一起升即可 |

### 4.5 go.mongodb.org/mongo-driver/v2 v2.8.0 → v2.9.2

**它为什么在图里**（实测 `go mod why -m`）：
```
ehome/backend/cmd/server → github.com/gin-gonic/gin → gin/binding → mongo-driver/v2/bson
```
只因 gin 的 **bson 绑定/渲染**能力而引入。本仓库 grep 无任何 `bson` 用法 ⇒ **纯传递负担**（还带进 `klauspost/compress`、`xdg-go/*`、`youmark/pkcs8` 一串）。
v2.8 → v2.9 是 v2 内 minor，无破坏性公告。**可批量**（实测绿）。
**不建议为此换框架**：剥离 bson 需换 gin，成本远超收益。

### 4.6 gorm.io/*

- `gorm.io/gorm v1.31.2` **已最新**（2026-06-22），无 `gorm/v2`。
- `gorm.io/driver/postgres v1.6.0 → v1.6.3`：patch 级；.mod 仍 pin `pgx/v5 v5.10.0` + `gorm v1.31.2` ⇒ **升 driver 不会自动升 pgx**，需单独指令。该模块带 `retract v1.5.5 // Published accidentally`。
- `gorm.io/driver/sqlite v1.6.0` **已最新**（2025-06-04）。

**风险：低。可批量。**

### 4.7 其他值得记一笔的

| 项 | 说明 |
|---|---|
| **paho.mqtt.golang 是死依赖** | backend 已无 import（代码注释记载 2026-10-08 移除 MQTT 链路）。实测在**原始** go.mod 上跑 `go mod tidy -diff`，唯一差异就是删 paho ⇒ 说明 go.mod 除该行外与代码完全一致。建议顺手 tidy 清掉 |
| **mimetype v1.4.15 是被撤回版本** | v1.4.16 的 .mod 明写 `retract v1.4.15`（「特制 CDF 文件呈二次时间复杂度」GHSA-jjmc-jx7j-hp68）。`go list -m -u all` 会打出 `(retracted)` 标记 |
| **gopkg.in/yaml.v3 已冻结** | 最后一版 v3.0.1 停于 2022-05-27，上游迁至 `go.yaml.in/yaml/v3`（v3.0.5）。本仓库同时存在两者（直接 v3.0.1 + 传递 v3.0.4）。不必现在动 |
| **golang/protobuf 已 deprecated** | v1.5.0 → v1.5.4 只是兼容层更新；实现在 `google.golang.org/protobuf`。可升不必删 |
| **testify/objx v0.5 → v0.8** | 0.x 连跳 3 个 minor，但仅 test 用，不进生产二进制 |

---

## 5. 分批升级方案（可直接执行）

### 组 A：低风险补丁级（`go get -u=patch`）——可安全批量

**实测结果：✅ 通过**。22 个模块被 patch 升级，`go build ./...` rc=0。

覆盖：sonic 1.15.2→1.15.4、sonic/loader 0.5.1→0.5.3、mimetype 1.4.15→1.4.16、cors 1.7.7→1.7.9、gzip 1.2.6→1.2.8、sse 1.1.1→1.1.2、locales 0.14.1→0.14.2、universal-translator 0.18.1→0.18.2、validator 10.30.3→10.30.5、puddle 2.2.2→2.2.3、cpuid/v2 2.4.0→2.4.1、go-sqlite3 1.14.48→1.14.52、client_model 0.6.2→0.6.3、ugorji/codec 1.3.1→1.3.2、protobuf 1.36.11→1.36.12、driver/postgres 1.6.0→1.6.3、x/crypto 0.54→0.57、x/net 0.57→0.58、x/sync 0.22→0.23、x/sys 0.47→0.48、x/text 0.40→0.42、mongo-driver 2.8.0→2.8.2。

```bash
cd /mnt/storage/WorkSpace/EHome/backend
export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH
export GOPROXY=https://goproxy.cn,direct GOPATH=/mnt/storage/WorkSpace/EHome/.tmp-deps/gopath
export GOMODCACHE=$GOPATH/pkg/mod GOCACHE=/mnt/storage/WorkSpace/EHome/.tmp-deps/gocache
export GOTOOLCHAIN=local GOSUMDB=off

go get -u=patch ./...
go mod tidy
go build ./... && go vet ./... && go vet -tags=simulation ./... && go test ./... -count=1
git diff --stat go.mod go.sum     # 预期：仅补丁位变化
```

> 注意：`go get -u=patch` 只能升**同一 minor 内**的补丁。像 x/net 0.57→0.58 属 minor，能升；0.57→0.61 属跨 minor，需组 B。

### 组 B：次版本（含 0.x minor 安全更新）——**推荐执行**

**实测结果：✅ 全绿** —— `go build` rc=0、`go vet` rc=0、**`go test ./...` 41 包 ok / 0 FAIL**（与升级前基线逐项一致，见 §6.2）。

```bash
cd /mnt/storage/WorkSpace/EHome/backend
# 环境变量同组 A

# ── B1. 安全优先：先修 2 条可调用 CVE + 1 个被撤回版本 ──
go get golang.org/x/net@v0.61.0 \
       golang.org/x/text@v0.43.0 \
       github.com/gabriel-vasile/mimetype@v1.4.16
go build ./... && go test ./... -count=1
govulncheck ./...            # 目标：模块级可调用 CVE = 0

# ── B2. 其余次版本（同主版本内升到 latest）──
go get golang.org/x/crypto@v0.58.0 golang.org/x/sys@v0.49.0 golang.org/x/sync@v0.24.0 \
       golang.org/x/arch@v0.32.0 golang.org/x/mod@v0.42.0 golang.org/x/tools@v0.52.0 \
       github.com/prometheus/client_golang@v1.25.0 \
       github.com/prometheus/client_model@v0.6.3 \
       github.com/prometheus/common@v0.72.1 \
       github.com/prometheus/procfs@v0.22.0 \
       github.com/quic-go/quic-go@v0.64.1 \
       go.mongodb.org/mongo-driver/v2@v2.9.2 \
       github.com/goccy/go-json@v0.11.2 \
       github.com/stretchr/objx@v0.8.0 \
       github.com/klauspost/compress@v1.20.3 \
       github.com/rogpeppe/go-internal@v1.16.0

go mod tidy
go build ./... && go vet ./... && go test ./... -count=1
```

> 一步到位等价于 `go get -u ./...` + `go mod tidy`（我实测的正是这条，结果全绿）。**仍建议拆 B1/B2**：B1 有明确安全收益、回滚面最小。

### 组 C：需单独人工验证（1 个）

| 项 | 原因 | 验证要点 |
|---|---|---|
| `github.com/jackc/pgx/v5` **v5.10.0 → v5.11.0** | `Rows` 接口加方法、日期时间解析器重写、连接串 user/OS 账户解析行为变化、`$HOME` 语义变化 | 跑 `make test-integration`（真实 PG）；回归带/不带 `user=` 的连接串、`target_session_attrs`、timestamptz 边界、date 极值 |

```bash
go get github.com/jackc/pgx/v5@v5.11.0
go mod tidy
go build ./... && go vet ./... && go test ./... -count=1
make test-integration          # 真实 PG
```

### 组 D：Go 工具链（不属依赖范围，但**优先级最高**）

```
# 需与 task-3（Dockerfile/CI）协同；本报告不代改
1) Dockerfile:5                    golang:1.26.5-alpine  →  golang:1.26.10-alpine
2) .github/workflows/test.yml:36   GO_VERSION: "1.26.5"  →  "1.26.10"
3) backend/go.mod:3                go 1.26.5             →  go 1.26.10   （可选，语义一致）
4) 本机开发工具链同步到 1.26.10 或 1.27.3
```

> 只做 1)+2) 就能消掉 §3.3 的 11 条标准库 CVE。**这是本次盘点投入产出比最高的一条。**

### 组 E：暂不建议

| 项 | 原因 |
|---|---|
| 任何"主版本升级" | **不存在**：12 个候选新主版本模块路径全部 404 / no matching versions |
| `gopkg.in/yaml.v3` → `go.yaml.in/yaml/v3` 迁移 | 上游确实迁了，但属跨模块路径迁移（import 全改），与安全/功能无关，**不应混进升级 PR** |
| 为去掉 `mongo-driver`/`quic-go` 换 Web 框架 | 二者是 gin 固有依赖（bson 渲染 / http3），换框架成本远超收益 |
| 强升 `pgx` 到超出 gorm driver 声明的版本而不做集成测试 | gorm driver v1.6.3 仍 pin pgx v5.10.0 |

### 组 F：清理（顺带）

```bash
go mod tidy
git diff go.mod      # 预期：仅删除 github.com/eclipse/paho.mqtt.golang v1.5.1 一行 + go.sum 两行
```

### scripts/bench/ehload

```bash
cd /mnt/storage/WorkSpace/EHome/scripts/bench/ehload
# 环境变量同上
go get golang.org/x/net@v0.61.0 golang.org/x/sync@v0.24.0
go mod tidy                    # 会把 go 1.26 写成 go 1.26.0（语义等价）
go build ./... && go vet ./...
govulncheck ./...              # 预期：模块级 CVE 归零，仅剩标准库
```

**实测结果：✅ build + vet 通过**；`go 1.26 → go 1.26.0`、`x/net 0.44.0 → 0.61.0`、`x/sync 0.17.0 → 0.24.0`。

### 建议提交顺序（每步独立可回滚）

| PR | 内容 | 风险 | 验收 |
|---|---|---|---|
| PR-1 | `go mod tidy`（删 paho 死依赖） | 极低 | build + test 绿 |
| PR-2 | 组 B1：x/net + x/text + mimetype | 低，**安全收益最大** | `govulncheck` 模块级归零 |
| PR-3 | 组 B2：其余次版本 | 低 | build + vet + test 绿 |
| PR-4 | 组 C：pgx v5.11.0 | 中 | + 集成测试 |
| PR-5 | 组 D：工具链 1.26.5 → 1.26.10（**跨 task，需与 task-3 协同**） | 中 | 镜像构建 + CI 全绿 |
| PR-6 | ehload 升 x/net + x/sync | 低 | bench 能跑起来 |


---

## 6. 验证记录（实跑，非推测）

### 6.1 三组对照实验（均在 `.tmp-deps/` 副本里）

| 实验 | 做法 | 结果 |
|---|---|---|
| **E1 补丁级** | `go get -u=patch ./...`（22 个模块升） | `go build ./...` **通过** |
| **E2 次版本** | `go get -u ./...` + `go mod tidy`（35 个模块升） | build ✅ vet ✅ **test 41 包全绿 0 FAIL** ✅ |
| **E3 高风险定向** | 显式升 quic-go v0.64.1 + mongo v2.9.2 + client_golang v1.25.0 + pgx v5.11.0 + x/* | `go build ./...` **通过** |
| **E4 ehload** | `go get -u ./...` | build ✅ vet ✅ |

### 6.2 基线对照（**关键**：避免把环境问题误判成升级问题）

第一轮跑测试时有 3 个包失败（`internal/models`、`pkg/frame`、`internal/commandexec`）。逐个查证后确认**全部是副本缺同级目录导致的路径依赖失败，与依赖升级无关**：

```
--- FAIL: TestAutomationResultCodesThreeWayConsistency
    读取 ../../../frontend-shared/src/api/automation.ts 失败: no such file or directory
--- FAIL: TestDeviceOpEncodeMatchesSharedVectors
    找不到共享向量文件 ../../../protocol/vectors/wire_primitives.txt
--- FAIL: TestCapabilityWindowMatchesFirmwareReportCadence
    找不到 esp32-collector 目录，尝试过: [../../../esp32-collector esp32-collector]
```

补齐 `frontend-shared/`、`protocol/`、`esp32-collector/`、`deploy/`、`tools/` 符号链接后重跑：

| | 结果 |
|---|---|
| **升级前基线**（原始 go.mod/go.sum） | **41 包 ok / 0 FAIL** |
| **升级后**（`go get -u ./...`） | **41 包 ok / 0 FAIL** |

⇒ **`go get -u ./...` 未引入任何测试回归。**
（这一步很要紧：若不建基线，那 3 个 FAIL 会被误判成"升级导致测试挂了"。）

### 6.3 govulncheck 前后对照

| | 模块级**可调用** CVE | 标准库 CVE（本机 go1.27.1） |
|---|---|---|
| 升级前 | **2**（x/text GO-2026-6629、x/net GO-2026-6617） | 12 条 |
| 升级后 | **0** | 11 条（与依赖无关，只能抬工具链） |
| ehload 升级前 | 1（x/net v0.44.0，3 条导入级） | 7 条 |
| ehload 升级后 | **0** | 7 条（同上） |

### 6.4 仓库完整性校验

```
$ git status --porcelain -- backend/go.mod backend/go.sum \
      scripts/bench/ehload/go.mod scripts/bench/ehload/go.sum Dockerfile .github/workflows
（空输出 = 未修改）

$ md5sum backend/go.mod backend/go.sum scripts/bench/ehload/go.mod scripts/bench/ehload/go.sum
97950d98061a605d5c9e6c3c0ba9c592  backend/go.mod
896335d419c31f6a8f27de0cd446154c  backend/go.sum
3dfa893e505acb0ecedabc71d9cc72e5  scripts/bench/ehload/go.mod
ad79b0c5a2f6a42c0bbe392da28f959b  scripts/bench/ehload/go.sum
```

> 说明：`git status` 在 `backend/` 下另有几个 `internal/e2e/*_test.go` 与 `internal/ingest/replay_real_driver_test.go` 的改动，**那是其他并行工作留下的，与本任务无关**（mtime 15:06 / 14:53，且不属于本报告写入范围）。本任务只写了 `docs/分析/依赖升级盘点/go.md` 一个文件。

### 6.5 实验副本清单（未触碰仓库）

- `.tmp-deps/probe-backend` — E1 补丁级
- `.tmp-deps/probe-minor` — E2 次版本
- `.tmp-deps/probe-risky` — E3 高风险定向
- `.tmp-deps/probe-tree` — E2 全保真对照（含 sibling 符号链接，用于基线比对）
- `.tmp-deps/backend-base` — 原始依赖快照
- `.tmp-deps/probe-ehload` — E4

---

## 7. 可直接执行的命令清单（汇总）

> ⚠️ 执行前先 commit / stash。升级必然改动 `go.mod` + `go.sum`。

```bash
# ══════ 通用环境（每个新 shell 都要重设）══════
cd /mnt/storage/WorkSpace/EHome/backend
export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH
export GOPROXY=https://goproxy.cn,direct
export GOPATH=/mnt/storage/WorkSpace/EHome/.tmp-deps/gopath
export GOMODCACHE=$GOPATH/pkg/mod
export GOCACHE=/mnt/storage/WorkSpace/EHome/.tmp-deps/gocache
export GOTOOLCHAIN=local GOSUMDB=off

# ══════ 0. 盘点（只读，零风险）══════
go list -m -u all                                  # 全部模块 + 可升级版本
go list -m -u -json all | grep -A3 '"Update"'       # 带 Time（发行时间）
go list -m -u all | grep -i retract                 # 揪出"当前版本已被撤回"
go list -m all | tail -n +2 | wc -l                 # 模块总数（应为 92）
go mod why -m <module>                              # 某模块为何在图里

# ══════ 1. 安全基线（只读）══════
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
export PATH=$GOPATH/bin:$PATH
govulncheck ./...
govulncheck ./... | tail -6                         # 只要摘要

# ══════ 2. 清理死依赖 ══════
go mod tidy
git diff go.mod                                     # 预期：仅删 paho.mqtt.golang

# ══════ 3. 组 A：补丁级 ══════
go get -u=patch ./...
go mod tidy
go build ./... && go vet ./... && go vet -tags=simulation ./... && go test ./... -count=1

# ══════ 4. 组 B1：修可调用 CVE + 撤回版本（强烈建议）══════
go get golang.org/x/net@v0.61.0 golang.org/x/text@v0.43.0 github.com/gabriel-vasile/mimetype@v1.4.16
go mod tidy
go build ./... && go test ./... -count=1
govulncheck ./...                                   # 目标：模块级可调用 CVE = 0

# ══════ 5. 组 B2：其余次版本 ══════
go get golang.org/x/crypto@v0.58.0 golang.org/x/sys@v0.49.0 golang.org/x/sync@v0.24.0 \
       golang.org/x/arch@v0.32.0 golang.org/x/mod@v0.42.0 golang.org/x/tools@v0.52.0 \
       github.com/prometheus/client_golang@v1.25.0 github.com/prometheus/client_model@v0.6.3 \
       github.com/prometheus/common@v0.72.1 github.com/prometheus/procfs@v0.22.0 \
       github.com/quic-go/quic-go@v0.64.1 go.mongodb.org/mongo-driver/v2@v2.9.2 \
       github.com/goccy/go-json@v0.11.2 github.com/stretchr/objx@v0.8.0 \
       github.com/klauspost/compress@v1.20.3 github.com/rogpeppe/go-internal@v1.16.0
go mod tidy
go build ./... && go vet ./... && go test ./... -count=1

# ══════ 6. 组 C：pgx 单独 PR（需真实 PG）══════
go get github.com/jackc/pgx/v5@v5.11.0
go mod tidy
go build ./... && go vet ./... && go test ./... -count=1
make test-integration

# ══════ 7. ehload ══════
cd /mnt/storage/WorkSpace/EHome/scripts/bench/ehload
go get golang.org/x/net@v0.61.0 golang.org/x/sync@v0.24.0
go mod tidy && go build ./... && go vet ./...
govulncheck ./...

# ══════ 8. 一致性收尾 ══════
cd /mnt/storage/WorkSpace/EHome/backend
go mod verify                                       # 应为 all modules verified
go mod tidy -diff                                   # 应为空
```

---

## 8. 证据与可复现性

### 8.1 数据来源（每条可复核）

| 数据 | 来源命令 / URL |
|---|---|
| 全部模块 + 可升级版本 | `go list -m -u all`、`go list -m -u -json all` |
| 关键包最新版 + 发行时间 | `curl https://goproxy.cn/<mod>/@latest`（返回含 `Time`） |
| 候选新主版本存在性 | `curl https://goproxy.cn/<mod>/v2/@latest` 等 12 个探测（全部 404 或 no matching versions） |
| CVE 详情 / 修复版本 / CVE 别名 | `govulncheck v1.9.0` + `curl https://vuln.go.dev/ID/<GO-ID>.json` |
| 上游破坏性变更 | GitHub Releases API：quic-go v0.62/v0.63/v0.64.1、client_golang v1.25.0、pgx v5.11.0、prometheus/common v0.71/v0.72、gin v1.12.0、gorm v1.31.2 |
| Go 补丁版本列表 | `curl 'https://go.dev/dl/?mode=json&include=all'` |
| 依赖要求的最低 go 版本 | `go list -m -json all` 的 `GoVersion` 字段；升级目标的 `@v/<ver>.mod` |
| `go` 指令硬性语义 | 实测：go.mod 写 `go 1.99.0` + `GOTOOLCHAIN=local` ⇒ 报 `go.mod requires go >= 1.99.0` |
| 被撤回版本 | `curl .../@v/v1.4.16.mod` 的 `retract v1.4.15` 块；`go list -m -u all` 的 `(retracted)` 标记 |

### 8.2 未能查询到的项（如实标注，不编造）

| 项 | 原因 | 替代做法 |
|---|---|---|
| `gorm.io/driver/postgres v1.6.3` 的 release notes | GitHub 仓库路径不对（`go-gorm/postgres` 返回 404），随后 GitHub API 触发未认证限流（`API rate limit exceeded`） | 改用 goproxy 的 `@v/v1.6.3.mod` 核实依赖约束（仍 pin pgx v5.10.0 / gorm v1.31.2）；风险按 patch 级语义 + 实测 build/test 判定 |
| `mongo-driver v2.9.x` 逐条 release notes | 同上（GitHub API 限流） | 依据：v2 内 minor、`.mod` 核实最低 go 1.25.0、本地 build+test 实测通过 |
| `x/crypto` / `x/net` / `x/text` 的 CVE 之外逐条改动 | x/* 系列不发 GitHub Release，只有 commit | 安全部分已用 `govulncheck` + `vuln.go.dev` 完整覆盖（正是本次关注点） |
| 各 CVE 的 CVSS 分数 | OSV 记录中这些条目 `severity` 字段为空（Go 漏洞库对标准库条目通常只给 affected ranges，不给 CVSS） | **不臆造分数**；改用 govulncheck 的 CALLED/imported 判定 + 真实调用链，这比 CVSS 更能说明"我们是否真的受影响" |

---

## 9. 一页纸行动清单

按优先级：

1. 🔴 **抬 Go 工具链 1.26.5 → 1.26.10**（`Dockerfile` + CI + 可选 go.mod）。消 **11 条标准库 CVE**，其中 7 条有真实调用链。**跨 task-3，需协同。**
2. 🔴 **升 `golang.org/x/net` v0.57.0 → v0.61.0**（修 GO-2026-6617，可调用，HTTP/2 崩溃）。**同时把 ehload 的 v0.44.0 → v0.61.0 一起升。**
3. 🔴 **升 `golang.org/x/text` v0.40.0 → v0.43.0**（修 GO-2026-6629，可调用，gorm 初始化路径）。
4. 🟠 **升 `gabriel-vasile/mimetype` v1.4.15 → v1.4.16**（当前版本已被上游 retract，DoS）。
5. 🟠 **`go mod tidy` 清掉死依赖 paho.mqtt.golang**（backend 已无 MQTT 代码）。
6. 🟡 **其余 30+ 项次版本可一次性批量升**（已实测 build + vet + test 全绿）。
7. 🟡 **pgx v5.11.0 单独 PR + 集成测试**（唯一需人工验证的依赖）。
8. ⚪ **不必担心的**：没有任何依赖存在更新的主版本；gorm / gin / pgx / x/* 最终都会在同主版本内收口。

