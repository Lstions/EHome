# 场景仿真验证框架（backend/simulation）

一层**黑盒 + 真实组合根**的端到端仿真验证。它编译并启动真正的 `./cmd/server` 子进程，
连真实的 PostgreSQL 与 EMQX，扮演真实用户与真实固件协议去操作系统。

契约文件：`docs/设计/场景仿真验证框架.md`（唯一权威）。本文件讲**怎么用、为什么这样设计**。

---

## 1. 怎么跑

```bash
# 方式一：Makefile（自动带上全部必需环境变量）
make test-scenarios                 # 全量
make test-scenarios SCENARIO=SIM-DS # 只跑某个域（-run 过滤）

# 方式二：直接调用
cd backend
EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 EHOME_DB_USER=ehome \
EHOME_DB_PASSWORD=ehome123 MQTT_BROKER=tcp://127.0.0.1:1883 \
  go test -tags=simulation -count=1 -timeout=30m -run TestScenarios ./simulation/...
```

必需的环境变量（缺省值见 `harness.Options.withDefaults`）：

| 变量 | 缺省 | 用途 |
|---|---|---|
| `EHOME_DB_HOST` / `_PORT` / `_USER` / `_PASSWORD` | 127.0.0.1 / 5432 / ehome / ehome123 | 组装维护库 DSN |
| `MQTT_BROKER` | tcp://127.0.0.1:1883 | 仿真器与服务子进程共用 |
| `EHOME_REPO_ROOT` | 自动向上查找 `backend/go.mod` | 证据目录与构建的工作目录 |
| `EHOME_SIM_LOCK_WAIT` | `45m` | run 级排他锁的**等待上限**（Go duration）。只影响「抢不到锁时愿意等多久」，不参与「谁能拿到锁」的判定（见坑 5）。验证排他性或 CI 里希望快速失败时设为 `3s`；值非法会立即失败而不是静默退回缺省 |

**全部 `backend/simulation/**/*.go` 首行都是 `//go:build simulation`**，
因此 `go build ./...` / `go test ./...`（不带 tag）完全不受影响。

## 2. 为什么必须真实 PG + EMQX

设计 §1 列出了现有三层测试各自的盲区，其中反复出问题的一类缺陷**只在真实组合根上暴露**：

- 消费者 sink 未二阶段注入 → 引擎永不触发（SQLite 单测全绿）；
- 分区迁移水位推进错误 → 整月只搬一个批次（`ehome_test` 全绿，真数据才暴露）；
- 整分区 DROP 建立在单设备保留期上 → 会删掉别人的历史。

这些缝的共同点是：**被验证对象是"接线"本身**，而不是某个函数。
所以 harness 绝不复制 `main.go` 的接线逻辑，而是 `go build -buildvcs=false ./cmd/server`
后作为子进程启动（设计 §3 原则 1）。一旦在 harness 里重写一遍接线，这层就退化成"又一个集成测试"。

因此前置条件是硬的：

- **PostgreSQL 可达**：场景库 `ehome_sim_<runid>` 由 harness 自行 `DROP`/`CREATE`；
- **EMQX 可达**：节点仿真器要走真实 MQTT 收/发二进制帧；
- 先执行 `make infra` 确保两者在跑。

基础设施缺失时 `TestMain` 的预检会**直接失败**并打印 `make infra` 提示 —— 不静默跳过（设计 §5.5）。

## 3. 子进程的环境变量：为什么是这几个

harness 在启动子进程时注入的变量都对应一个**已踩过的坑**：

| 变量 | 为什么必须注入 |
|---|---|
| `EHOME_DB_NAME=ehome_sim_<runid>` | 隔离场景库。库名在建立任何连接前用 `^ehome_sim_[a-z0-9_]+$` 校验（设计 §7-1） |
| `GIN_MODE=debug` | **`middleware.go:31-33` 的 `isDevelopmentMode()` 是 `EHOME_ENV=development` 且 `GIN_MODE=debug` 的与运算**。该函数注释自称"GIN_MODE unset 也算 dev"，**与实现不符**（产品侧注释缺陷，本框架不改产品代码）。缺了它服务会走生产分支 |
| `EHOME_EXTERNAL_HOST=127.0.0.1:<port>` | `handler_ota.go:174-183`：该变量为空且非开发模式时 `POST /firmwares/upload` 直接 **500**。同时让固件下载 URL 的 host 与 `Env.BaseURL` 一致，带票据下载的场景才能直接对该 URL 发真实请求 |
| `CONFIG_PATH=<rundir>/no-config.yaml` | **隔离仓库里的 `backend/config.yaml`** —— 它指向 `localhost:5434/ehome`（开发库）。`config.Load()` 的优先级是 env > yaml > 默认值，若不隔离，未显式覆盖的字段会从该文件读到开发库参数 |
| `EHOME_MQTT_CLIENT_ID=ehome-sim-<runid>` | 与开发/生产实例的 MQTT 客户端区分开 |
| `EHOME_JWT_SECRET=<随机>` | 每次运行随机，仿真签发的令牌绝不与任何真实环境通用 |
| `SEED_TEST_DATA=`（空） | 显式关闭测试数据播种，保证场景从干净库出发 |
| `EHOME_ALLOWED_ORIGINS=`（空） | 与生产同源部署一致，不启用 CORS 中间件 |

**子进程 cwd 是临时目录**（`os.MkdirTemp`），不是仓库根：固件上传会写 `<cwd>/firmwares/`，
放仓库里会污染工作区。临时目录随收尾一并删除。

## 4. 启动序列（设计 §5.1）

```
1  校验库名 ^ehome_sim_[a-z0-9_]+$（不通过则拒绝启动）
2  DROP（若存在）→ CREATE 场景库（连维护库 postgres 执行）
3  net.Listen(":0") 取空闲端口
4  go build -buildvcs=false -o <tmp>/ehome-sim-server ./cmd/server（整轮只编译一次）
5  启动子进程（注入上表环境变量；stdout/stderr 全量进 LogBuffer）
6  轮询 GET /health 直到 200（上限 60s）
7  从启动日志解析一次性凭据：Initialization credential (valid for 10 minutes): <cred>
7.5 轮询直到服务端确实开始消费 MQTT 上行（发非法 Hello 探针，见下）
8  错误凭据探针 → POST /auth/initialize → POST /auth/login 得到 Env.Admin
9  t.Cleanup：杀子进程（SIGTERM→超时 SIGKILL）→ 按 KeepDB DROP 场景库 → 写 summary
```

**第 7.5 步是本实现新增的**（设计文档原有 1~9 步未列）：服务端的 MQTT SUBACK 何时完成
不与 `/health` 同步，首批 QoS1 上行会落在订阅建立之前被 broker 直接丢弃（无持久会话），
表现为随机的"Hello 无响应"。这一步用协议版本非法的 Hello 作探针（服务端会打
`Rejecting invalid Hello before ACK`），既不产生任何节点记录，又能确定"消费已开始"。

## 5. 三层测试的分工：什么时候该往这一层加场景

| 关注点 | 归属 |
|---|---|
| 单函数/单分支逻辑、边界值 | 单元测试（`internal/**/*_test.go`，内存 SQLite + mock） |
| SQL 方言差异、分区 DDL、迁移 | 集成测试（`-tags=integration`，真实 PG） |
| **组合根接线、真实时序、真实设备行为** | **场景仿真（本框架）** |
| 页面交互、表单校验提示 | Playwright E2E |

设计 §10 的裁决原则：**新增缺陷时，优先在能捕获它的最低层加回归；只有该层够不到时才上提到场景仿真。**
本层的成本是真实子进程 + 真实基础设施（一轮数十秒到分钟级），不要用它去覆盖单函数边界值。

判断标准很简单：**如果这个缺陷在 harness 里复制一遍接线就会被掩盖，它才属于这一层。**

## 6. 一次运行的产物与失败排查

```
.logs/simulation/<runid>/
  summary.json   # 每个场景一条：ID/标题/域/状态/耗时/证据/失败详情
  server.log     # 服务子进程的完整 stdout+stderr
```

- 测试失败时输出里会**自动附带服务子进程日志尾部**（`Env.finish`）；
- `summary.json` 的 `failure` 字段是 harness 记录的失败原文，含
  **method / path / status / body / 耗时**（`Response.context()` 统一渲染）；
- 场景里的 `e.Evidence(name, value)` 会进入该场景的 `evidence` 数组，用于离线复盘。

| 症状 | 先看哪里 |
|---|---|
| `场景仿真预检失败` | `make infra`；确认 PG 5432 / EMQX 1883 在跑 |
| `解析一次性初始化凭据失败` | `server.log` 里有没有 `Initialization credential` 行（生产行文若改动，harness 的正则要同步） |
| `期望 HTTP x 实际 y` | 失败信息里的信封 `code/message/error_code` 与响应体 |
| `等待帧 0x.. 超时` | 提示里会列出期间**实际收到过**的帧类型与时刻 |
| `哨兵上报后 N 内 unified_data 未出现预期行` | 夹具自检失败：通道 / 边缘设备 / 解析器 / field 7 逐项排查 |
| 场景间互相污染 | 场景必须 `t.Cleanup` 清理；改密/登出类必须恢复 `Env.Admin` 与 `Env.AdminPass` |

## 7. 怎么加一个场景

1. 找到对应域的文件（`catalog/dep.go` / `auth.go` / `node.go` / …）；
2. 在 `init()` 里 `Register(Scenario{...})`，填 `ID`/`Title`/`Doc`/`Run` 四项；
3. 场景函数只接收 `*harness.Env`，用 `e.Admin` / `e.DeviceFor` / `e.Eventually` 写黑盒断言；
4. 自我清理：`t.Cleanup` 删除自己创建的资源（设计 §5.6）。

硬性约定：

- **标题写真实用户口吻**，不写实现细节（"管理员下发 ping，节点收到对应帧" 而不是 "走 down 主题"）；
- **禁止 `t.Skip`**；基础设施缺失时应当直接失败；
- **禁止用 `time.Sleep` 同步断言**，一律 `Eventually` / `AwaitFrame`；
- **节点命名必须用 `e.DeviceFor(scenarioID, suffix)`**（设计 §5.6 v1.3）。
  它拼出 `sim-<runid>-<紧凑场景码>-<suffix>`，例如
  `DeviceFor("SIM-AUTO-006", "manual")` → `sim-20260912-9485-at006-manual`。
  后缀只需**场景内**唯一。`e.Device(local)` 仅适用于确实需要自定义名字、且能自行保证
  **全运行唯一**的场合 —— 只按场景内唯一取名会跨场景撞名（实测 SIM-ALERT-001 与
  SIM-AUTO-001 都用了 `rule`，直接 409）；
- **命名前缀（设计 §4.1）**：域文件的包级标识符必须带本域小写前缀（`dep…` / `auth…` / `node…`），
  跨域共用助手放 `catalog.go` 并用 `sim` 前缀 —— 由 `TestCatalogGate` 门禁 8 强制。

## 8. harness API 速查

```go
env := harness.Start(t, harness.DefaultOptions())     // 建库→编译→起服务→初始化管理员
env.Admin                                             // 已登录的管理员会话
env.NewSession()                                      // 匿名会话（验 401 / 探针）
env.Session(user, pass)                               // 独立登录会话
env.DeviceFor("SIM-AUTO-006", "manual")               // 节点仿真器（推荐）
env.Device("custom-local")                            // 自定名字（需自行保证全运行唯一）

// HTTP
resp := env.Admin.Get("/api/v1/nodes")
resp.Expect(200)                                      // 失败打印 method/path/status/body/耗时
resp.DataInt("id") / DataString / DataFloat / DataBool / DataSlice
resp.ExpectError(409, "AUTH_INITIALIZATION_REJECTED")
resp.String("token")                                  // 非致命取值（配合 Eventually）

// 等待（禁 sleep）
env.Eventually(30*time.Second, func() error { ... })
dev.AwaitFrame(frame.MsgHelloAck, 5*time.Second)
dev.AwaitRawAfter(frame.MsgPing, after, 5*time.Second)

// 节点仿真（真实 MQTT + pkg/frame）
dev.Connect(); dev.Hello("2.6.0", "ESP32-C6", 2)
dev.HelloThenReport(...)                              // Hello + ResourceReport（顺序敏感！）
dev.DataReport(channelID, tsMillis, payload)
dev.StatusReport(uptime, "online", -55)
dev.ResourceReport(harness.ResourceReportData{...})   // 命令门禁能力的唯一写入路径
dev.ConfigResult(manifestID, syncID, true)
dev.ConfigReport(manifestID, channelCount)
dev.Pong(tsMillis)

// 数据落库夹具
fx, err := env.ProvisionSimpleDevice("SIM-DATA-001", "n0")  // 节点+通道+配置+边缘设备
fx.Report(harness.FixtureTempCategory, 23.5)                // 真实上报，真入库
fx.AwaitValue(harness.FixtureTempCategory, 23.5, 10*time.Second)
fx.Cleanup()

// WebSocket / multipart
ws, _ := env.DialWSConn("/api/v1/ws"); ws.AwaitEvent("data_update", 5*time.Second)
env.DialWS("/api/v1/ws")                              // 原始拨号，可拿到握手 401
env.Admin.Upload("/api/v1/firmwares/upload", fields, "file", "fw.bin", content)

// 证据
env.Evidence("node_status", "online")                 // 进 summary.json
```

### 五个必须知道的坑

1. **`Hello()` 会清空节点的命令能力字段。**
   `nodemgr.handleHello` 每次握手都会重置 `BootID` / `ResourceReportedAt` /
   `CommandEngineRevision` / `CommandEngineCapabilities`，
   而 `commandexec.currentCapabilities()` 要求这些字段齐全且新鲜（5 分钟内）。
   → **每次 `Hello()` 之后都必须重新发 `ResourceReport`**（直接用 `HelloThenReport`）。

2. **`DataReport` 必须携带 `edge_device_id`（帧内 field 7）。**
   `databus.DataEvent.IsPassive()` 在 `request_id == 0 && edge_device_id == 0` 时为真，
   这类上行会被消费者直接丢弃，**数据不会落库**。
   `Device.DataReport` 默认按 `(node_id, channel_id)` 反查并填入，无需手工传。
   另注意：`edge_device_id` 是**结果**不是原因 —— 解析路径由 `EdgeDevice.Type`
   是否为 CalibrationAware 驱动决定（详见 `harness/fixture.go` 顶部的选型说明）。

3. **HTTP 客户端关闭了压缩（`DisableCompression`）。**
   原因见 `harness/http.go` 的注释：`main.go` 的 gin-contrib/gzip 与
   `promhttp.Handler` 会对 `/metrics` 叠加两层压缩，导致客户端无法解出可读文本。
   压缩路径由浏览器 E2E 层覆盖；本层的断言必须建立在确定的响应体上。

4. **夹具自检是「落库即返回」，不保证哨兵帧的求值与广播已经完成。**
   `ProvisionSimpleDevice` 的哨兵只等到 `unified_data` 出现该行就返回
   （见 `harness/fixture.go` 的 `selfCheck`），而服务端的处理顺序是
   **落库 → alertSink → automationSink → WebSocket 广播**。
   因此哨兵那一帧的告警求值 / 自动化求值 / WS 推送**可能发生在夹具返回之后**，
   下游场景会收到一条「幽灵」事件（实测 RT-001 拿到的是哨兵值 21.37 而非自己上报的值）。

   **受影响的写法**：依赖"第一条事件"、事件计数、或"规则必须立刻触发"的断言。
   **规避方式**（场景内解决，不需要改 harness）：
   - 断言锚定**自己上报的**特征值，而不是"任意一条事件"；
   - 阈值/条件**不要与哨兵值 21.37 重叠**；
   - 需要从干净状态起算时，先建**禁用**的规则再启用，或先排空既有事件。

5. **并发 run 会通过共享 EMQX 互相污染 —— 因此 harness 用排他锁强制串行。**
   PG 和 EMQX 是**所有 run 共用**的基础设施，而 EMQX 上没有任何 run 作用域：

   - 服务端订阅的是无 run 作用域的通配主题 `nodes/+/up`（`mqtt.go:116`），
     A run 的 server 会收到 B run 的节点上行；
   - 消费者按**数字 edge_device_id** 查库（`consumers_heavy.go:189-193`），
     不校验帧里的 `node_id`；
   - 每个 run 一个空库、ID 从 1 自增 → **两个 run 的第 k 个边缘设备几乎必然同号**。

   后果：B run 的一帧被 A run 当成自己设备的上报 → 落库、更新最新值缓存、
   **触发 A 的告警与自动化规则**。此时「套件全绿」不再说明任何事。

   harness 因此把排他性做成前置条件：`harness.Start` 的第一步就是抢
   `<repo>/.logs/simulation/.run.lock` 的 `flock(LOCK_EX|LOCK_NB)`，
   抢不到就每秒重试（上限 45 分钟），超时则以「另一个场景仿真 run 正在执行
   （PID x，已持有 y）」终止测试 —— **绝不静默并发**。用 flock 而不是 PID 文件，
   是因为持有者崩溃时内核会自动释放，不会留下需要人工清理的死锁。
   拿到锁之后 harness 还会顺手 DROP 掉孤儿场景库，但**只删它自己生成的库**
   （`ehome_sim_sim_<8位日期>_<4位十六进制>`，即默认 RunID 形态）。
   手工命名的 `ehome_sim_*`（例如 `ehome_sim_recon`）与 `Options.RunID`
   自定义出来的库**一律不会被碰**，只会在日志里列出"疑似手工库，未清理"——
   `^ehome_sim_[a-z0-9_]+$` 是设计 §7-1 给**人**划定的命名空间，不是删除凭据。

   **绕过它（例如改了锁路径、或在锁之外直接连那套 PG/EMQX 起仿真）得到的结果
   不可信**：你拿到的绿色可能来自另一个 run 的帧。

## 9. 安全红线（设计 §7）

- harness 只允许 `CREATE`/`DROP` 匹配 `^ehome_sim_[a-z0-9_]+$` 的库，
  校验发生在**建立任何连接之前**（`harness.ValidateDatabaseName`）；
- 节点仿真器只发布/订阅 `nodes/sim-*/...`，
  `Connect` 与每次 `publish` 之前都会强校验 `NodeID` 前缀 ——
  同网段可能有真实设备在跑，这条校验是"绝不干扰真实设备"的唯一执行点；
- 一次性初始化凭据只存在于内存，**不写入 `summary.json`**（只记 selector 前缀）；
- 子进程 cwd 是临时目录、`CONFIG_PATH` 指向不存在的文件 —— 仿真不污染工作区，
  也读不到指向开发库的仓库 `config.yaml`。
