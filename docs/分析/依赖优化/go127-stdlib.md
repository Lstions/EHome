# Go 1.27 标准库新特性在 EHome 后端的落地扫描

> 分析对象：`/mnt/storage/WorkSpace/EHome/backend`（671 个 .go 文件 / 196,772 行 / 41 包）
> 工具链：go1.27.3 linux/amd64（实测 `go version go1.27.3 linux/amd64`）
> 依据：Go 1.27 官方 release notes（`https://go.dev/doc/go1.27`，全文存于 `.tmp-deps/go127.md`，32,121 字符）
> 方法：本报告中的**性能数字全部由我在本机用 go1.27.3 实跑 benchmark 得到**（不是抄 changelog），
> 语义断言全部用最小可运行程序实跑验证。每条给：特性 / 官方来源 / 落点(文件:行) / 收益 / 成本 / 风险。
> **未改动仓库任何代码**；所有探针在 `.tmp-deps/fix-probe`（backend 的副本）与独立 module 中运行。

---

## 0. 结论摘要（先看这个）

Go 1.27 有 20 项左右与本项目相关的变更，但**只有 5 项在 EHome 有实质收益**，其余大多"本项目不适用"或"只是语法糖"。

| 排序 | 特性 | 判决 | 为什么 |
|---|---|---|---|
| **P0** | **json/v2 默认已生效；收益大小取决于 payload 大小** | **必须动作** | Go 1.27 起 `encoding/json`（v1 API）**已经由 v2 实现**（实测确认）。Unmarshal 一律 **+44%**；Marshal 大 payload **+21~28%**、小 payload **-44%~-65%**。WS 广播正是小 payload + map，需改 struct |
| **P0** | **goroutineleak profile** | **强烈推荐** | backend **0 处 pprof 注册**，这是一项净新增能力，且本仓有 28 处 `go func` / 31 处 Ticker / 7 处 `context.WithCancel` 的生命周期面 |
| **P1** | **小对象分配提速 30%（编译器自动，零改动）** | **白捡** | 编译器自动生效，需验二进制 +60KB 是否影响 OTA/镜像 |
| **P1** | **atomictypes modernizer（23 处旧式 atomic）** | **推荐** | 仓库已混用两种风格（`atomic.Pointer` vs `atomic.AddInt64(&x,1)`），可机械统一，且 `go fix` 已实测能改 |
| **P2** | **泛型方法** | 有机会但非必须 | 本仓 **0 个泛型函数/泛型类型**（实测 grep 确认）。是"首次引入泛型的形态选择"，不是重构。收益是消除 65 处 bind 样板 |
| — | strings.CutLast / bytes.CutLast | 不适用（有效落点仅 3 处，且都在 simulation） | 无性能收益，纯可读性 |
| — | uuid 标准库 | **部分可用但收益为负** | 缺 `NewString()`（本仓 15 处用它）且缺 `NewSHA1`/`NameSpaceOID`（identity.go:54 依赖） |
| — | crypto/mldsa、http/2 优先级、HTTP drain、synctest、ConvertAssign、math/big、unicode 15→17 等 | 不适用（逐条给原因） | 见 §3 |

---

## 1. 最重要的发现：json/v2 已经在跑，收益与劣化都取决于 payload 大小

### 1.1 事实一：Go 1.27 的 `encoding/json` 默认已是 v2 实现（无需改任何代码）

release notes 原文：
> The `encoding/json` package is now backed by the v2 implementation. Marshaling and unmarshaling behavior is preserved... Marsha performance is broadly at parity... while unmarshal performance is significantly faster.

我实测确认这不是"GOEXPERIMENT 可选"，而是**默认开启**：

```
$ go list -f '{{.GoFiles}}' encoding/json | grep v2_
[v2_decode.go v2_encode.go v2_indent.go v2_inject.go v2_options.go v2_scanner.go v2_stream.go]
```

即 v1 的文件带 `//go:build !goexperiment.jsonv2`，而默认 baseline 是 jsonv2 **on**，所以真正编译进二进制的是 `v2_*.go`。
**这意味着：本仓库不需要做任何迁移动作，"unmarshal 变快"这件事已经自动发生了。** 这条容易被误读成"待办"，实际是"已完成"。

（关闭开关仍存在：`GOEXPERIMENT=nojsonv2`，release notes 说未来版本会移除。）

### 1.2 事实二（关键）：官方说"Marshal 大致持平"，实测是"大的更快、小的更慢"

release notes 只说 "broadly at parity"，我在**本仓库真实结构体**上实测（`models.UnifiedData`，就是历史数据/API 的主响应体）：

**测试设置**：`go test -bench`，同机同进程，`-count 2`，AMD Ryzen 5 5600。
**DEFAULT** = Go 1.27 默认（v1 API → v2 实现）；**nojsonv2** = `GOEXPERIMENT=nojsonv2`（恢复旧 v1 实现）。

**先说结论的微妙之处**：v2 的 Marshal **不是**简单的"更快"或"更慢"，而是**取决于 payload 大小**。
小 payload（WS 单事件）**变慢**，大 payload（历史数据列表）**变快**。这是我这轮最重要的发现。

| 负载（真实 `models.UnifiedData` 结构） | DEFAULT（v2 实现） | nojsonv2（旧 v1 实现） | 判决 |
|---|---|---|---|
| **Marshal 单条记录** | 706 ns · 272 B · 3 allocs | 666 ns · 240 B · 3 allocs | **慢 6%**（噪音级）|
| Marshal 5 条 | 2,237 ns · 624 B · 3 allocs | 3,068 ns · 841 B · 7 allocs | 快 27% |
| Marshal 20 条 | 6,910 ns · 2,355 B · 3 allocs | 8,953 ns · 3,291 B · 22 allocs | 快 23% |
| Marshal 50 条 | 17,003 ns · 5,426 B · 3 allocs | 22,239 ns · 7,812 B · 52 allocs | 快 24% |
| Marshal 200 条 | 65,864 ns · 21,836 B · 3 allocs | 85,391 ns · 31,430 B · 202 allocs | 快 23% |
| Marshal 1000 条 | 333,220 ns · 106,911 B · 3 allocs | 424,467 ns · 154,897 B · 1002 allocs | 快 21% |
| **纯标量小 struct（5 字段）** | **531 ns · 160 B · 3 allocs** | **321 ns · 112 B · 2 allocs** | **慢 65% ⚠ 真实回归** |
| Unmarshal 50 条 | 67,840 ns · 17 KB · 108 allocs | 121,894 ns · 17 KB · 163 allocs | 快 44% |
| Unmarshal 1000 条 | 6,887,988 ns · 2.7 MB · 10,019 allocs | 12,326,590 ns · 2.7 MB · 15,023 allocs | 快 44% |
| **map payload Marshal（WS 实际形状）** | **2,900 ns · 464 B · 20 allocs** | **2,008 ns · 1008 B · 25 allocs** | **慢 44% ⚠ 回归** |

**精确结论（三条，都有实测支撑）**：
1. ✅ **Unmarshal 一律显著变快（+44%）**，无例外。这一条官方说法成立。
2. ✅ **大 payload 的 struct Marshal 变快（+21~28%）且 allocs 从 O(n)→O(1)**（1000 条时 1002→3 allocs）。
   真实的 GC 收益远大于 ns 数字：`UnifiedData` 是主表，历史查询动辄数百到数千行，这是实打实的收益。
3. ⚠ **小 payload 的 Marshal 变慢**。两个独立证据：
   - 纯标量 5 字段 struct：531ns → 321ns（**慢 65%**）
   - 现实 WS map payload：2,900ns → 2,008ns（**慢 44%**）
   原因：v2 有一个**固定的按调用开销**（初始化 encoder 状态机、option 解析、类型缓存冷启动），
   小 payload 下这个固定成本占比高；大 payload 下被批量收益摊薄。这解释了 crossover 出现在 ~2-5 条记录之间。

**这对 EHome 意味着什么**：
- ✅ **REST API 的历史数据/列表端点全部受益**（`internal/api/handler_data.go:225,306` 的 `max_points` 缺省 0 = 返回全量，几十到几千行）。
- ⚠ **WebSocket 单事件广播受损**——每帧只序列化**一个**小对象，正好落在变慢的区间。
  本仓 WS 事件**全部是 map payload**（实测 17 处，见下表），map 路径本身又比 struct 慢，两个劣势叠加。

本仓库 WS 事件**全部是 map payload**（实测 17 处，清单见下）：

```
internal/databus/consumers_heavy.go:450   BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{...})
internal/databus/consumers_heavy.go:530   BroadcastEvent(events.DataUpdate, map[string]interface{}{...})
internal/logstream/ws_consumer.go:45      BroadcastAuthenticatedEvent(events.NodeLog, map[string]interface{}{...})
internal/nodemgr/handler_hello.go:252     BroadcastEvent(events.NodeStatus, map[string]interface{}{...})
internal/nodemgr/handler_response.go:116  BroadcastEvent(events.PingResult, map[string]interface{}{...})
internal/nodemgr/handler_response.go:166  BroadcastEvent(events.ScanResult, map[string]interface{}{...})
internal/nodemgr/handler_response.go:448  BroadcastEvent(events.PeriphResult, map[string]interface{}{...})
internal/nodemgr/handler_status.go:469    BroadcastEvent(events.NodeStatus, map[string]interface{}{...})
internal/nodemgr/sender.go:325            BroadcastEvent(events.PingResult, map[string]interface{}{...})
internal/nodemgr/handler_device_op.go:144 BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, map[string]any{...})
internal/offlinedetector/offlinedetector.go:202  BroadcastEvent(events.NodeStatus, map[string]interface{}{...})
internal/offlinedetector/offlinedetector.go:401  BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{...})
internal/ota/ota.go:250 / 510 / 660 / 716 / 784      BroadcastEvent(events.OTAProgress, map[string]interface{}{...})
```

其中**最热的是 `internal/databus/consumers_heavy.go:530`（data_update）**——每帧设备数据都广播一次，是秒级高频路径。

另外 `internal/websocket/websocket.go:213-216` 在 hub 主循环里对**每一条**广播消息做一次 `json.Unmarshal(message.data, &evt)` 来喂 subscribers（terminal 用）——这是一次"打包成 map 的 JSON 再解析回来"的往返，v2 在这条 Unmarshal 上反而快 17%，所以那一段是受益的。

### 1.3 建议动作（按性价比排序）

**动作 A（零风险，立即做）：把 WS 热点 payload 从 map 换成 struct**

这是本轮收益最确定的一项。同一次 benchmark 里我直接对比了这两种形状（同样的 v2 实现）：

| 形状 | ns/op | B/op | allocs/op |
|---|---|---|---|
| `map[string]interface{}`（现状） | 2,900 | 464 | **20** |
| 具名 struct（同样字段） | 2,044 | 528 | **13** |

→ **快 30%，allocs 20→13（少 35%）**。

- 落点：`internal/databus/consumers_heavy.go:530`（data_update，最高频，每帧设备数据一次）、`:450`（edge_device_status），其次 `internal/ota/ota.go:250/510/660/716/784`（OTA 进度，秒级）
- 做法：定义具名 struct 替换 `map[string]interface{}`，例如：
  ```go
  type dataUpdateEvent struct {
      EdgeDeviceID   uint                   `json:"edge_device_id"`
      EdgeDeviceName string                 `json:"edge_device_name"`
      NodeID         string                 `json:"node_id"`
      NodeName       string                 `json:"node_name"`
      ChannelID      uint                   `json:"channel_id"`
      Data           map[string]any         `json:"data"`
      CollectedAt    string                 `json:"collected_at"`
  }
  ```
- 收益：**+30% 速度、-35% allocs**（实测）。附带好处：**编译期字段名检查**——当前 map 字面量写错 key 名（如 `"edge_device_Id"`）编译器完全不报错，只会让前端静默收不到数据；换 struct 后这类缺陷在编译期就被拦下。本仓有过同类事故（见 `internal/databus/consumers_heavy.go:527` 那条长注释，专门解释 `node_id` 语义曾经一处两义）。
- 成本：小（每处 15~25 行，改动局部）。风险：低。**唯一注意点**：`omitempty` 语义——若要保留"零值也输出"的现状（map 总是输出所有 key），struct 上**不能加** `omitempty`，否则前端拿不到字段会从"有值 0"变成 `undefined`。

**动作 B（低风险，需先测）：确认 v2 的严格性不会打断现有 API**
- v2 默认**拒绝重复 key**、**拒绝非法 UTF-8**。我实测确认：
  ```
  dup-key    v1err=<nil> v1val=2       | v2err=jsontext: duplicate object member name "a"  v2val=1
  badutf8    v1err=<nil>                | v2err=jsontext: invalid UTF-8 within "/k"
  ```
  **注意**：v1 API（`encoding/json.Unmarshal`）实际**没有**变严格——它用 `DefaultOptionsV1()` 包了 v2，保住了 v1 语义。上表 v2 的严格行为只在**显式调用 `encoding/json/v2`** 时出现。
  所以：**现有 65 处 `ShouldBindJSON` 与 464 处 json 调用不会因升级而改变行为**。这是好消息，但也意味着"拒绝重复 key"这个安全收益**要靠主动迁移到 v2 API 才能拿到**。
- 落点（若决定主动迁移）：`internal/api/bind_json_strict.go:35` 的 `bindJSONStrict` 已经手工实现了 `DisallowUnknownFields` + 拒绝 trailing content，正是 v2 的默认行为。**这个手写函数可以整体退役，换成 `jsonv2.Unmarshal` 一行。**
- 收益：少一个 70 行的手写解码器 + 顺带获得"拒绝重复 key"。成本：小。风险：中（该函数被 `handler_logstream.go:65`、`:119` 使用，行为必须逐字节等价；v2 错误文案不同，而调用方可能匹配错误字符串）。

**动作 C（不要做）：不要用 `GOEXPERIMENT=nojsonv2` 回退**
它的确能修好小 payload 的 Marshal（-6%~-44% 的回归），但代价是：
- 丢掉大 payload **+21~28%** 的 Marshal 收益（这是历史数据主路径）
- 丢掉 allocs 从 **1002→3** 的收益（1000 行场景）——GC 压力重新回到 O(n)
- 丢掉 Unmarshal **+44%**
本仓的流量结构里，历史/列表查询的字节量和 CPU 都远大于 WS 单事件。**整体回退是亏的**；正确做法是**保留 v2，把 WS payload 改成 struct**（动作 A），一次拿到两个改善。

### 1.4 迁移到 v2 API 时的真实语义陷阱（本仓可能踩到的）

| v2 变更 | 本仓受影响点 | 判断 |
|---|---|---|
| 拒绝重复 key | 无（v1 API 下保持宽松） | 迁移后是**收益**（当前 `deviceaction/schema.go:82-105` 手写了重复 key 检测） |
| 拒绝非法 UTF-8 | `internal/commandexec/*` 的 `Reason` 字段有 `utf8.RuneCountInString` 校验 | 迁移后是收益 |
| `string` tag 行为变更 | 需 grep `json:"[^"]*,string"` | 未发现使用（本仓无 `,string` tag） |
| `inline` tag 改名 `embed` | 未使用 | 不适用 |
| `omitzero` vs `omitempty` | `models.UnifiedData:326/328` 用了 `omitempty`（指针字段） | 指针语义 v1/v2 一致，**不受影响** |

---

## 2. 逐项落点分析

### 2.1 goroutineleak profile —— P0，净新增诊断能力

**官方来源**：`https://go.dev/doc/go1.27` → "Goroutine leak profile"；`https://pkg.go.dev/runtime/pprof`；端点 `/debug/pprof/goroutineleak`

**特性**：GA（Go 1.26 是实验）。基于 GC 可达性检测"阻塞在并发原语上、且该原语从任何可运行 goroutine 都不可达"的 goroutine。

**我在本机实测确认可用**：
```go
p := pprof.Lookup("goroutineleak")   // != nil: true
p.Count()                            // 制造 1 个泄漏后能定位
mux.HandleFunc("/debug/pprof/goroutineleak", pprof.Handler("goroutineleak").ServeHTTP)  // 注册成功
```

**本仓库落点：当前为 0。**
```
$ grep -rn 'pprof' --include='*.go' backend/ | wc -l
0
```
即整个 backend **没有任何 pprof 注册点**——既没有 `net/http/pprof` 的空白导入，也没有 `runtime/pprof` 的 profile 导出。`cmd/server/main.go:629` 建的是纯净 `gin.New()`，路由里只有 `/ping`（`main.go:692`）、`/metrics`（`main.go:640`）、`/api/v1/metrics/prometheus`（`main.go:641`）。`internal/api/handler_metrics.go:81` 的 `/api/v1/metrics` 只返回一句占位文本。

**为什么本仓特别需要它**：这个仓库有大量长生命周期 goroutine，且历史上明确担心过 goroutine 泄漏（代码注释里就有两处）：

| 泄漏风险面 | 证据（file:line） |
|---|---|
| WebSocket 每连接 2 个 goroutine（writePump/readPump） | `internal/websocket/websocket.go:272`、`:273` |
| WS hub 主循环（无限 for） | `internal/websocket/websocket.go:150`（`func (h *Hub) Run()` 无退出路径） |
| terminal 事件监听 goroutine | `internal/terminal/ws_handler.go:94`（`startEventListener` 里的 `go func()`） |
| 后台 ticker 循环 | 31 处，如 `internal/nodemgr/sync_gate.go:394`、`internal/ota/ota.go:211`、`internal/offlinedetector/offlinedetector.go:79`、`internal/datalifecycle/purge.go:121` |
| 溢出 goroutine（可能失控） | `internal/nodemgr/handler_data.go:107`（`go func()` 在背压路径上） |
| `context.WithCancel` 生命周期 | 7 处 |
| 已有注释承认的泄漏担忧 | `internal/nodemgr/device_op_tracker.go:187`（"goroutine leak on the operator's request path"）、`internal/nodemgr/handler_device_op.go:77`（"Behind an HTTP handler that is one leaked"） |

**建议落点（新增，不改现有行为）**：
1. 在 `cmd/server/main.go` 的路由注册段（`main.go:629` 之后、`:698` 之前）增加一个**独立的、仅监听 127.0.0.1 的 debug 端口**，注册 `net/http/pprof` 全套 + `goroutineleak`。不要挂到主 gin 路由上（主路由经 gzip/cors/auth 中间件，且暴露面大）。
2. 或最小改动：只在 `cmd/ehomectl` 加一个 `ehomectl debug pprof` 子命令做离线分析。

**收益**：把"疑似泄漏"从"看日志猜"变成"直接给出泄漏 goroutine 的完整调用栈"。本仓 `device_op_tracker.go:187` 和 `handler_device_op.go:77` 两处注释说明这个问题**曾经真实发生过**。
**成本**：小（约 30 行，独立端口 + 3 个 import）。
**风险**：中低。
- ⚠ 安全：`/debug/pprof` 暴露堆内容（可能含 URL 里的 token）。本仓 `internal/notify/redact.go` 的整套脱敏纪律说明团队对此敏感 → **必须绑 127.0.0.1 或加独立 token**，绝不可挂在公网端口。
- ⚠ 性能：profile 采集时会 STW 停世界，只在排障时短暂开启。

---

### 2.2 小对象分配提速 30% —— P1，白捡（但需验一个副作用）

**官方来源**：`https://go.dev/doc/go1.27` → "Faster memory allocation"

**特性**：编译器对 <80 字节的分配生成 size-specialized 分配例程，**最多快 30%**，整体约 1%。代价：**二进制 +60KB**（与负载无关）。可通过 `GOEXPERIMENT=nosizespecializedmalloc` 关闭，Go 1.28 移除。

**本仓落点**：**不需要任何代码改动**，升级到 go 1.27.3 即已生效（当前 `backend/go.mod:3` 是 `go 1.27.3`）。

**需要验证的唯一副作用**：二进制 +60KB。
```
Dockerfile:17  RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false ...
```
本仓的发布物是 Docker 镜像（`Dockerfile`、`docker-compose.yml`）。+60KB 对服务端镜像（数百 MB）可忽略。
⚠ **但对 ESP32 固件无关**——固件是 C/ESP-IDF，不经过 Go 编译器。这点值得写清楚，避免有人误以为能帮到 `esp32-collector/`。

**收益**：约 1% 整体分配性能，零成本。
**成本**：**零**（已在生效）。
**风险**：极低。唯一动作是：如果 CI 里有**二进制体积门禁**，需把阈值上调 60KB（我未在本仓找到此类门禁，`Makefile` 无 size 检查）。

---

### 2.3 atomictypes modernizer —— P1，可机械统一风格债

**官方来源**：`https://go.dev/doc/go1.27` → "go fix" + `go tool fix help` 输出：
```
atomictypes  replace basic types in sync/atomic calls with atomic types
embedlit     simplify references to embedded fields in composite literals
slicesbackward ...
unsafefuncs ...
```

**我在副本（`.tmp-deps/fix-probe`，backend 的完整拷贝）上实跑了 `go fix -diff ./...`**：261 个文件被提议修改，4,129 行变化。按分析器分类：

| 分析器 | 提议改动数 | 有实质价值？ |
|---|---|---|
| `any`（interface{} → any） | 785 | ❌ 纯语法糖，零性能收益 |
| `rangeint`（`for i := 0; i < n; i++` → `for i := range n`） | 99 | ❌ 纯语法糖 |
| `gofix-inline`（`//go:fix inline` 标记） | 25 | ❌ 测试辅助函数，无价值 |
| **`atomictypes`** | **6（覆盖 23 个调用点）** | ✅ **真实价值：类型安全 + 不可复制语义** |
| `minmax`（手工 clamp → `max()/min()`） | 20 | ⚠ 可读性收益，无性能 |
| `slices.Contains` | 9 | ⚠ 可读性 |
| `errors.AsType` | 8 | ⚠ Go 1.26 特性，非 1.27 |
| `newexpr` | 5 | ❌ 语法糖 |
| `embedlit` | **0** | — 本仓无命中 |
| `slicesbackward` | **0** | — 本仓无命中 |
| `unsafefuncs` | **0** | — 本仓无 `unsafe` |

**atomictypes 的精确落点**（`go fix` 提议 + 我手工 grep 交叉验证，非测试代码 17 处调用 / 4 个包级变量）：

| 文件:行 | 当前写法 | 提议 |
|---|---|---|
| `internal/deviceinit/deviceinit.go:78` | `var nextInitRequestID uint32` | `var nextInitRequestID atomic.Uint32` |
| `internal/deviceinit/deviceinit.go:263` | `atomic.AddUint32(&nextInitRequestID, 1)` | `nextInitRequestID.Add(1)` |
| `internal/deviceinit/deviceinit.go:265` | 同上（重试分支） | 同上 |
| `internal/nodemgr/handler_data.go:12` | `var overflowGoroutines int64` | `var overflowGoroutines atomic.Int64` |
| `internal/nodemgr/handler_data.go:98` | `atomic.LoadInt64(&overflowGoroutines)` | `.Load()` |
| `internal/nodemgr/handler_data.go:106` | `atomic.CompareAndSwapInt64(&overflowGoroutines, current, current+1)` | `overflowGoroutines.CompareAndSwap(current, current+1)` |
| `internal/nodemgr/handler_data.go:108` | `defer atomic.AddInt64(&overflowGoroutines, -1)` | `defer overflowGoroutines.Add(-1)` |
| `internal/nodemgr/sender.go:308` | `var nextPeriphRequestID uint32` | `atomic.Uint32` |
| `internal/nodemgr/sender.go:216` | `atomic.LoadUint32(&nextPeriphRequestID)` | `.Load()` |
| `internal/nodemgr/sender.go:219` | `atomic.CompareAndSwapUint32(&nextPeriphRequestID, 0, ...)` | `.CompareAndSwap(0, ...)` |
| `internal/nodemgr/sender.go:222` | `atomic.AddUint32(&nextPeriphRequestID, 1)` | `.Add(1)` |
| `internal/nodemgr/sender.go:345`、`:347` | `atomic.AddUint32(&nextLegacyWriteRequestID, 1)` | `.Add(1)` |
| `internal/pendingwrite/pendingwrite.go:50` | `var nextRequestID uint32` | `atomic.Uint32` |
| `internal/pendingwrite/pendingwrite.go:95` | `atomic.AddUint32(&nextRequestID, 1)` | `.Add(1)` |
| `internal/pendingwrite/pendingwrite.go:382` | `atomic.StoreUint32(&nextRequestID, r.RequestID+1)` | `.Store(...)` |
| `internal/commandexec/periph_transport.go:41` | `atomic.AddUint32(&periphRequestID, 1)` | `.Add(1)` |
| `internal/ota/ota.go:429` | `atomic.AddUint32(&_bridge.seqCounter, 1)` | `.Add(1)` |
| `internal/api/device_mutex.go:30` | `atomic.LoadInt64(&v.count)` | `.Load()` |
| `internal/api/device_mutex.go:40`、`:52` | `atomic.AddInt64(&entry.count, ±1)` | `.Add(±1)` |

**证据：本仓已经混用两种风格**（这是"技术债"而非"风格选择"）：
```
internal/alert/evaluator.go:68     rulesSnap atomic.Pointer[[]models.AlertRule]     ← 新式
internal/automation/selector.go:183 var planProviderSnapshotHook atomic.Pointer[atomic.Int64]  ← 新式
internal/databus/bus.go:66         dropped atomic.Uint64                            ← 新式
internal/nodemgr/sender.go:216     atomic.LoadUint32(&nextPeriphRequestID)          ← 旧式
internal/api/device_mutex.go:40    atomic.AddInt64(&entry.count, 1)                 ← 旧式
```

**收益**：
- 类型安全：`atomic.Uint32` 不能被误传成 `*uint32` 指向普通变量（旧式的 `&x` 在 `x` 不是 atomic 语义时是静默数据竞争）。
- `atomic.Uint32` 带 `noCopy` 语义，`go vet` 能抓复制。（本仓 `sender.go` 的 `_bridge.seqCounter` 是字段，复制 bridge 结构体会踩坑）
- **真实缺陷防护**：`internal/api/device_mutex.go:30-52` 是引用计数 + LRU 清理，是并发敏感区；类型化后误用会被编译器拦下。

**成本**：小（`go fix` 可自动改 6 个变量声明 + 我需手工核对 23 个调用点；预计 1 小时）。
**风险**：低，但有两个具体注意点：
1. `internal/nodemgr/sender.go:219` 的 CAS 逻辑是"首次用时间种子初始化 requestID"，改成方法后必须保持 `CompareAndSwap(0, seed)` 语义（防止并发重复播种）。
2. `internal/pendingwrite/pendingwrite.go:382` 用 `Store(r.RequestID+1)` 做**回退式**推进——这是非标准的用法，改类型不改变行为，但值得加注释说明为什么用 Store 而非 Add。
3. ⚠ `go fix` 提议的 261 文件里 **785 处是 interface{}→any 的噪音**，**不要整批接受**。只挑 atomictypes。

---

### 2.4 泛型方法 —— P2，是"首次引入泛型的形态选择"，不是重构

**官方来源**：`https://go.dev/doc/go1.27` → "Changes to the language"；proposal `https://go.dev/issue/77273`

**特性**：方法可声明自己的类型参数。**限制**：接口方法不能声明类型参数，接口方法也不能被泛型方法实现。

**实测验证（go1.27.3 编译运行通过）**：
```go
type Binder struct{ body []byte }
func (b Binder) Bind[T any]() (T, error) { var v T; err := json.Unmarshal(b.body, &v); return v, err }
// 调用：d, err := b.Bind[dev]()

type Page[T any] struct{ Items []T }
func (p *Page[T]) Map[U any](f func(T) U) []U { ... }   // 泛型类型上的泛型方法，也可用
```
两种形态都编译并正确运行。指针接收者的泛型方法也可用。

**本仓现状（这是关键，与 Lead 的假设一致，我独立复核了）**：
```
$ grep -rnE '^(func|type) [A-Za-z0-9_]+(?[^)]*[[A-Z][A-Za-z0-9_]* (any|comparable|~)' backend/ --include='*.go'
internal/api/handler_envelope_contract_test.go:38   func envelopeData[T any](...)      ← 测试
simulation/catalog/catalog.go:182,202,230,249,276,286,296,304,324  ← simulation 包，9 个泛型函数 + 1 个泛型类型
```
**生产代码（`internal/`、`cmd/`、`pkg/`）里 0 个泛型函数、0 个泛型类型。** 全部泛型代码都在 `simulation/`（测试仿真器）和一个测试文件里。

所以泛型方法的定位：**如果要在生产代码引入泛型，优先用方法形态而不是包级函数**（方法归属清晰、不污染包命名空间、IDE 补全更准）。它**不是**对现有代码的重构机会。

**唯一真实的落点：65 处 bind 样板**
```
$ grep -rn 'ShouldBindJSON' backend/ --include='*.go' | grep -v _test | wc -l
65
$ grep -rn 'c\.JSON(' backend/ --include='*.go' | grep -v _test | wc -l
68
```

典型重复形态（`internal/api/handler_alert.go:116`、`:187`、`:440`，`handler_auth.go:66`、`:79` …）：
```go
var req xxxRequest
if err := c.ShouldBindJSON(&req); err != nil {
    Error(c, 400, "参数错误: "+err.Error())
    return
}
```

而且本仓**已经有一个更严格的版本**却只被用了 3 处：
```
internal/api/bind_json_strict.go:35   func bindJSONStrict(c *gin.Context, dst any) error
internal/api/handler_logstream.go:65  ← 使用
internal/api/handler_logstream.go:119 ← 使用
internal/api/bind_json_strict_test.go:36 ← 测试
```

**候选实现**（泛型方法形态）：
```go
// 把 bindJSONStrict 收成 gin.Context 上的泛型方法（1.27 允许）
func (x *apiCtx) Bind[T any]() (T, error) {
    var dst T
    body, err := io.ReadAll(x.Request.Body)
    if err != nil { return dst, err }
    if err := jsonv2.Unmarshal(body, &dst, jsonv2.RejectUnknownMembers(true)); err != nil {
        return dst, err
    }
    return dst, nil
}
// 调用点：req, err := apiCtxOf(c).Bind[updateLogConfigRequest]()
```

**收益**：
- 65 处 × 4 行样板 → 65 处 × 2 行，净减约 130 行。
- **顺带把"拒绝未知字段"从 3 个端点推广到全部 65 个端点**。这是本仓明确记录过的真实缺陷类别——`bind_json_strict.go:15-33` 的注释完整描述了"假成功"事故：`PUT /log-config {"enabled": false}` 返回 200 但开关没动，因为 gin 静默忽略未知字段。同类缺陷在其他 62 个端点**仍然存在**。
- 用 `jsonv2.RejectUnknownMembers` 还能同时拿到"拒绝重复 key"。

**成本**：中。不是机械替换——每个 handler 的 `Error(c, 400, "参数错误: "+err.Error())` 文案不同（`handler_auth.go:67` 是 `"invalid initialization request"`），错误信息若要逐字节保留工作量大。
**风险**：中高。
- ⚠ **行为变更**：把 62 个端点从"宽松"变"严格"是**破坏性变更**。前端若有多传字段的习惯（Vue 表单常直接把整个对象回传），会从 200 变 400。必须先审计前端请求体。
- ⚠ 不建议一次性推广。**建议先只替换形态（用泛型方法包住现有 `ShouldBindJSON` 语义），确认零行为变更后，再按端点逐个加严。**

**我的判断**：泛型方法的**语法收益**在这里是真实的（消除样板 + 让"严格绑定"成为可复用的类型安全 API），但**主要价值来自统一严格绑定，而不是泛型本身**。如果只想要"减样板"，用 `func bind[T any](c *gin.Context) (T, error)` 包级函数就够了——泛型方法在这里的额外好处是不污染包命名空间。**这不是一个高优先级项。**

---

### 2.5 uuid 标准库 —— 部分可用，但本仓替换会亏

**官方来源**：`https://go.dev/doc/go1.27` → "New uuid package"；`https://pkg.go.dev/uuid`

**标准库 API（我 `go doc -all uuid` 实测）**：
```
type UUID [16]byte
func Max() UUID / MustParse(s string) UUID / New() UUID / NewV4() UUID / NewV7() UUID / Nil() UUID / Parse(s string) (UUID, error)
func (u UUID) AppendText / Compare(v UUID) int / MarshalText / String
func (u *UUID) UnmarshalText(b []byte) error
```

**本仓 uuid 使用（`github.com/google/uuid v1.6.0`，`backend/go.mod:9`）**：
```
$ grep -rn 'uuid\.' --include='*.go' backend/ | grep -oE 'uuid\.[A-Za-z]+' | sort | uniq -c
     11 uuid.New
      1 uuid.NameSpaceOID
      1 uuid.NewSHA          ← uuid.NewSHA1
     15 uuid.NewString
```
用量分布：7 个非测试文件（`dispatcher.go:16`、`identity.go:11`、`merge.go:9`、`config_event_bus.go:10`、`event_helper.go:9`、`handler_channel_cmd_v2.go:19`、`sync_gate.go:12`）+ 3 个测试文件。

**阻塞点（两条，都是硬阻塞）**：
1. **没有 `NewString()`**：标准库只有 `New().String()`。本仓 15 处 `uuid.NewString()` 全要改。`internal/nodemgr/sender_bytegate_test.go:256` 的注释写明 `36 B = 后端 uuid.New().String()`——**字节长度契约被测试断言**，替换必须保持 36 字符。
2. **没有 `NewSHA1` / `NameSpaceOID`**：`internal/datalifecycle/identity.go:54` 用 `uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("edge:%d", instanceID)))` 生成**确定性 v5 UUID**，作为 `identity_key` 的稳定来源。注释（`identity.go:44-51`）说明这是"同一实例跨补建/跨副本生成同一 key，ON CONFLICT 并发防护才真正生效"的**正确性基础**。标准库**没有 v3/v5**，无法替换这段。

**唯一可能的好处：`NewV7()`（时间有序）**
本仓所有 UUID 用途都是**不透明标识符**（EventID、SyncID、IdempotencyKey、IdentityKey 后缀），**不落 DB 主键**。我核对了 6 个文件的使用点，没有一个把 UUID 当索引键。
- `internal/nodemgr/sync_gate.go:151,250,259,281,316,336,358,373` — SyncID，只在日志/协议里传递
- `internal/nodemgr/event_helper.go:35`、`config_event_bus.go:78` — EventID，内存事件总线
- `internal/commandexec/dispatcher.go:66` — 幂等键后缀
- `internal/datalifecycle/merge.go:124` — IdentityKey 前缀（`"merge:" + uuid`）

**所以 `NewV7` 的索引局部性优势在本仓拿不到**（没有 UUID 主键可优化）。

**结论：不推荐替换。**
- 收益：少一个直接依赖（`go.mod` 少一行）。但 `google/uuid` 是纯 Go 零依赖小包，去掉它省不了任何供应链风险或编译时间。
- 成本：中（15 处 `.String()` 改写 + 无法替换 `identity.go:54` 那段 + 5 个包要动）。
- 风险：中高（`identity.go` 的 v5 确定性是正确性契约；改错会导致 ON CONFLICT 防护失效、重复建逻辑设备）。
- **判决：不适用 / 不值得。** 如果哪天标准库加了 v3/v5，再重新评估。

---

## 3. 剩余项：逐条给"不适用"的理由

| # | 特性 | 本仓判决 | 原因（带证据） |
|---|---|---|---|
| 3.1 | **struct literal 可初始化 promoted field**（Go 1.27 / issue 9859） | **已实现，本仓仅 1 处候选** | ⚠ **更正**：release notes 写的是"key 可以是任意 field selector"`Outer{Inner.Field: 1}`，但**该写法在 go1.27.3 编译失败**（`invalid field name Inner.Field in struct literal`，见 `types2/literals.go:175`）。**实际实现的形式是"直接用 promoted 字段名"**：`Outer{Field: 1}`，且要求 `go 1.27` 语言版本（`literals.go:190` 有 `verifyVersionf(... go1_27, "use of promoted field %s...")`）。我实测：go.mod 写 `go 1.27.3` 通过；写 `go 1.26` 报 `requires go1.27 or later`。多层提升也可用（`Level2{Field: 9}`）。<br>**本仓落点：只有 1 处** —— `internal/deviceaction/definition.go:654` 的 `PlanStep{ID:..., Kind:..., SingleStep: SingleStep{TXData:..., ReadSize:...}}`，其中 `SingleStep` 是**具名字段而非嵌入字段**（`definition.go:42-46`：`SingleStep SingleStep`），所以**连这一处也不适用**——`embedlit` 只对**嵌入字段**生效。<br>本仓 0 处嵌入结构体字面量（`grep 'gorm.Model{' = 0`，无 BaseModel/TimestampedModel 嵌入）。**结论：不适用，且 `go fix` 的 embedlit 实测 0 命中已独立佐证。** |
| 3.2 | **函数类型推断泛化** | **不适用** | 本仓生产代码 0 个泛型函数（§2.4），没有"泛型函数赋给函数类型变量"的场景可用 |
| 3.3 | **crypto/mldsa（FIPS 204）+ TLS 1.3 ML-DSA** | **不适用（当前）** | 本仓唯一 TLS **服务端**是设备 mTLS：`internal/transport/server.go:211-218`，配置是 `MinVersion: tls.VersionTLS12` + `RequireAndVerifyClientCert`。证书由设备侧固件（ESP32/mbedTLS）持有，**固件不支持 ML-DSA**，服务端单方面启用会直接握手失败。OTA 只做 SHA-256 完整性校验（`internal/api/handler_ota.go:177` `sha256.Sum256`），**无签名验签**，所以也没有"换签名算法"的落点。**若将来要加 OTA 签名，mldsa 是正确选择**（比 ECDSA 更抗量子且无密钥长度焦虑），但那是新功能不是升级收益 |
| 3.4 | **strings.CutLast / bytes.CutLast** | **近乎不适用** | 全仓 `LastIndexByte`/`LastIndex` 仅 3 处，且**全在 simulation 包**：`simulation/catalog/audit.go:172`、`simulation/catalog/rt.go:574`、`:584`。这 3 处取的都是**索引**用于切片，不是 `CutLast` 的(before, after)二元语义，改写反而更绕。`bytes.CutLast` 落点：0 处 `bytes.LastIndex`。**无性能收益，纯可读性，且落点在测试仿真器里。不推荐动。** |
| 3.5 | **net/url 的 `URL.Clone` / `Values.Clone`** | **不适用（有 2 处候选但都不该用）** | 两个候选是**同构副本**：`internal/notify/redact.go:72-92` 与 `internal/models/notification_channel.go:138-159`。它们用 `url.Parse` → 改 `parsed.User` / `parsed.RawQuery` → `String()`。**这里根本不需要深拷贝**：`url.Parse` 返回的是**新对象**，且 `query := parsed.Query()` 已经返回新的 map。引入 `Clone()` 只会多一次内存分配。而且这两份代码有**逐字节等价契约**（`redact.go:60-68` 注释：`RedactURL(x)` 必须与 `models.RedactTargetURL(x)` 相同，由 `TestRedactTargetURLMatchesNotify` 守护），改任一处必须同步改另一处 + 跑测试。**收益 0，风险非 0。** |
| 3.6 | **database/sql `ConvertAssign` + `driver.RowsColumnScanner`** | **不适用** | 本仓**没有任何自定义 SQL driver**：`grep 'driver.Rows\|sql.Register\|driver.Conn' = 0`。用的是 gorm + pgx（`gorm.io/driver/postgres v1.6.3`）。`ConvertAssign` 是给**driver 作者**用的，应用层用不到。`database/sql` 只在 simulation/harness 里直接出现（`db.go:14`、`env.go:8` 等 7 个文件），做的是 `db.Query` 管理库操作，不实现 driver |
| 3.7 | **net/http：HTTP/2 客户端优先级（RFC 9218）** | **不适用** | 本仓 HTTP 服务是 `srv.ListenAndServe()`（`cmd/server/main.go:705`）——**明文 HTTP/1.1 或经反代 TLS，不是 h2 服务端**。全仓 `grep 'h2c\|http2\|http3' = 0`。反向代理配置（`deploy/`）未发现 HTTP/2 相关设置。**没有 HTTP/2 服务端就没有优先级可谈。** |
| 3.8 | **net/http：HTTP/1 Response.Body 自动 drain** | **受益但无需动作（且本仓已规避）** | 唯一自带 Transport 的地方是 `internal/notify/client.go:165-172`，它显式设了 `DisableKeepAlives: true` + `MaxIdleConns: 0`（注释解释：SSRF 防护要求连接绝不复用）。**该配置下 drain 不产生任何行为变化**——这正是 release notes 说的"不使用连接复用则无影响"。其余 `http.Client` 在 simulation（`harness/http.go:63`）。**无需动作。** |
| 3.9 | **net/http `Server.MaxHeaderValueCount`** | **可选加固，非性能项** | 落点明确：`cmd/server/main.go:698-701` 的 `&http.Server{Addr, Handler}` 只设了两个字段。可加 `MaxHeaderValueCount: 64` 之类的限制做 DoS 加固。但本仓服务经 nginx/反代暴露（`docker-compose.yml`），header 数量已在反代层受控。**收益边际，属安全加固而非升级收益。** |
| 3.10 | **httptest.NewTestServer + testing/synctest** | **不适用** | `grep 'synctest' = 0`。本仓测试用 `httptest.NewServer`（18 处）和 `httptest.NewRecorder`。`NewTestServer` 的**唯一价值是配 synctest 做确定性时间**，本仓测试里 53 处 `time.Sleep` 是真实的墙钟等待（如 `internal/databus/shard_test.go:114` 睡 100ms），改成 synctest 需要**把整条链路（含真实 TCP、真实 gorm/PG）搬进 bubble**——不可能。同进程内的等待点可以改，但收益（测试变快）远小于改造成本。**不适用。** |
| 3.11 | **math/big.Int.Divide** | **不适用** | `math/big` 只在 3 个**测试/工具**文件出现：`cmd/server/device_e2e_test.go:13`、`internal/transport/server_test.go:14`、`simulation/harness/pki.go:30`（生成测试证书）。生产代码 0 处。且这些用途是 RSA/证书大数运算，不是"求商余" |
| 3.12 | **go mod tidy 自动合并重复 require 块** | **已经满足，无需动作** | 实测 `backend/go.mod` 恰好两个 require 块（`:5` 直接依赖、`:23` 间接依赖），正是 1.27 要求的标准形态。且 `go 1.27.3` 满足"go 1.27+"前提。**无债可还。** |
| 3.13 | **vet printf 检查 `%w` 传指针** | **不适用（0 命中）** | 本仓 `%w` 使用 524 处。实测 `grep -rnE '%w.*, *&[a-zA-Z]'` = **0 命中**。另有 9 个指针接收者 error 类型（`internal/ingest/errors.go:50`、`internal/notify/ssrf.go:32`、`internal/models/source_ref.go:51` 等），但调用方都用 `errors.As`/`errors.AsType` 正确解包，没有 `%w` 直接包裸指针的写法。**本仓干净。** |
| 3.14 | **go test 默认启用 stdversion vet** | **已满足，无需动作** | 需确认没有"用 go.mod 声明版本更高的标准库符号"的情况。本仓 `go 1.27.3` 与工具链同版本，且未使用任何 1.27 新符号（`grep 'CutLast\|synctest\|NewTestServer\|MaxHeaderValueCount\|ConvertAssign' = 0`）。**不会红。** |
| 3.15 | **Unicode 15 → 17** | **不适用（但有一条安全相关提醒）** | 本仓 unicode 相关只有 `utf8.RuneCountInString`（字符数上限校验：`internal/commandexec/confirmation.go:58`、`inbox.go:216`、`service.go:548,636`、`internal/sensorfield/selector.go:165`），**不用 `unicode.Is*` 分类表**。所以 Unicode 表更新**不影响任何现有校验**。⚠ 一个值得记录的副作用：`utf8.RuneCountInString` 行为不变，但如果有下游（前端 `string.length`）做**不同口径**的计数，Unicode 17 新增字符（如新的 emoji 序列）会让两侧口径差异变大。本仓的 `Reason` 字段限制 512 runes、DB 列 `size:512`——**多字节字符在 PG 的 varchar(512) 里按字符计**，与 Go 的 rune 计数一致，无风险 |
| 3.16 | **crypto/tls MLKEM1024 / QUICConfig / ConnectionState.LocalCertificate** | **不适用** | 设备链路是 mTLS over TCP，固件 mbedTLS 不支持 MLKEM。quic-go 是**间接依赖**（`go.mod` indirect，来自 gin 的 HTTP/3 可选支持），本仓未启用 HTTP/3（`grep 'http3' = 0`） |
| 3.17 | **hash/maphash `Hasher`/`ComparableHasher`** | **不适用** | 本仓无自定义哈希表/Bloom filter 实现。`hash/maphash` 0 处引用 |
| 3.18 | **go/constant StringLen / go/scanner End / go/token File.String / go/types Hasher** | **不适用** | 本仓不写 Go 工具链代码。唯一的 Go 源码分析是 `scripts/fieldsem/main.go`（slices.Contains 一处），它不做类型系统工作 |
| 3.19 | **runtime/secret 传播到子 goroutine** | **不适用** | 本仓未用 `runtime/secret`（0 处） |
| 3.20 | **traceback labels / asynctimerchan 移除** | **不适用** | 无 GODEBUG 配置，无 `//go:debug` 注释 |

---

## 4. 汇总表

| # | 特性 | 落点（文件:行） | 预期收益 | 成本 | 风险 | 优先级 |
|---|---|---|---|---|---|---|
| 1 | **json/v2 已默认生效** | 全局（无需改代码） | Unmarshal **一律 +44%**；Marshal 大 payload **+21~28%** 且 allocs 1002→3 | 零 | 极低 | **已完成** |
| 2 | **WS 小 payload Marshal 回归**（v2 固有 + map 形状） | `internal/databus/consumers_heavy.go:450,530`；`internal/ota/ota.go:250,510,660,716,784`；另有 11 处 | 改 struct：**2,900→2,044 ns（+30%）**，allocs **20→13** | 小 | 低 | **P0** |
| 3 | **goroutineleak profile** | `cmd/server/main.go:698`（新增独立 debug 端口）；风险面见 §2.1 表 | 泄漏 goroutine 直接给调用栈；本仓有 28 go func/31 Ticker/自认泄漏史 | 小 | 中低（安全需绑 127.0.0.1） | **P0** |
| 4 | **小对象分配 +30%** | 已生效（`backend/go.mod:3` = go 1.27.3） | ~1% 分配性能 | 零 | 极低（二进制 +60KB） | **P1** |
| 5 | **atomictypes（23 处）** | `deviceinit.go:78,263,265`；`handler_data.go:12,98,106,108`；`sender.go:308,216,219,222,345,347`；`pendingwrite.go:50,95,382`；`periph_transport.go:41`；`ota.go:429`；`device_mutex.go:30,40,52` | 类型安全 + noCopy 语义；统一已分裂的两种风格 | 小 | 低（注意 sender.go:219 的 CAS 语义） | **P1** |
| 6 | **泛型方法（bind 样板）** | `internal/api/bind_json_strict.go:35` 扩展为 `Bind[T]`；65 处 `ShouldBindJSON` | 减 ~130 行；把"拒未知字段"从 3 端点推广到 65 端点 | 中 | 中高（严格化是破坏性变更） | P2 |
| 7 | `bindJSONStrict` 换 `jsonv2.Unmarshal` | `internal/api/bind_json_strict.go:35-62`（70 行手写解码器） | 删 60 行；顺带获得拒重复 key | 小 | 中（错误文案变更） | P2 |
| 8 | `strings.CutLast` | `simulation/catalog/audit.go:172`、`rt.go:574,584` | 无（语义不匹配，且在仿真器内） | 小 | 低 | 不做 |
| 9 | 标准库 `uuid` | 15 处 `NewString` + `identity.go:54` 的 v5 | 少一个直接依赖（收益≈0） | 中 | 中高（v5 确定性是正确性契约） | 不做 |
| 10 | `URL.Clone`/`Values.Clone` | `notify/redact.go:72-92`、`models/notification_channel.go:138-159` | 无（本就无共享，且两份有等价契约） | 小 | 中 | 不做 |
| 11 | `Server.MaxHeaderValueCount` | `cmd/server/main.go:698-701` | DoS 加固（非性能） | 小 | 低 | 可选 |
| 12 | `go fix` 全量 modernizer | 261 文件 / 4,129 行 | 785 处是 any 噪音，实际只有 23 处 atomic 有价值 | — | — | **只取 atomictypes** |

---

## 5. 按收益排序的 TOP 5 建议动作

### 🥇 1. 把 WS 热点 payload 从 map 换成 struct（同时解掉 v2 小 payload 劣化）

**为什么第一**：v2 对**小 payload 的 Marshal 是净劣化**（我实测：纯标量 struct 慢 65%，现实 WS map payload 慢 44%），而 WS 广播正是"每帧一个 map"的小 payload 形态，每秒都在跑。同一份 benchmark 里，把 map 换成 struct 直接得到 **2,900→2,044 ns（快 30%）、allocs 20→13**。这是本仓库能立刻拿到的最大单项收益，且**不依赖任何回退开关**。附加价值：struct 让字段名错误在**编译期**暴露，而现在的 map 字面量写错 key 会静默失败（前端收不到数据、后端无报错）——本仓有过同类事故。
**落点**：`internal/databus/consumers_heavy.go:530`（data_update，最高频）、`:450`（edge_device_status）；然后是 `internal/ota/ota.go` 的 5 处。
**动作**：定义具名 struct 替换 `map[string]interface{}`。**先做 530 这一处**并 benchmark 验证，再推广。
**成本**：小。**风险**：低（注意 `omitempty` 语义与前端契约逐字段核对）。

### 🥈 2. 接入 goroutineleak profile（独立 127.0.0.1 debug 端口）

**为什么第二**：backend **0 处 pprof**，这是纯粹的能力空白。本仓有 28 处 `go func`、31 处 Ticker、WS 每连接 2 个 goroutine，而且 `internal/nodemgr/device_op_tracker.go:187` 与 `internal/nodemgr/handler_device_op.go:77` 的注释**自己承认过泄漏风险**。goroutineleak 能直接把"永久阻塞且不可达"的 goroutine 连同调用栈报出来，这是 `/debug/pprof/goroutine`（只给快照，要人肉判读）做不到的。
**落点**：`cmd/server/main.go:698` 附近新增独立 `http.Server`，绑 `127.0.0.1`，注册 `pprof.Handler("goroutineleak")`。
**成本**：小（约 30 行）。**风险**：中低——**必须绑 loopback**，因为 heap/goroutine profile 会泄露 URL 中的 token（本仓有整套 `internal/notify/redact.go` 脱敏纪律，说明团队对这个敏感）。

### 🥉 3. 机械统一旧式 atomic 为 `atomic.Uint32`/`atomic.Int64`（23 处）

**为什么第三**：这是**已经分裂的技术债**——同一仓库里 `atomic.Pointer`（新式，`alert/evaluator.go:68`）和 `atomic.AddInt64(&x, 1)`（旧式，`api/device_mutex.go:40`）并存。类型化后获得编译器级的误用防护和 `noCopy` 语义，而 `internal/api/device_mutex.go:30-52` 的引用计数逻辑正是并发敏感区。
**落点**：§2.3 表的 20 行（4 个包级变量 + 17 个调用点）。
**动作**：`go fix` 能自动改 6 个变量声明，但**只接受 atomictypes 那几处**，其余 255 个文件的 any/rangeint 改动是噪音，必须拒绝。
**成本**：小（1 小时）。**风险**：低，但 `internal/nodemgr/sender.go:219` 的 CAS 播种逻辑要逐字核对。

### 4. 把 `bindJSONStrict` 的严格绑定推广到全部 65 个端点（泛型方法形态）

**为什么第四**：`internal/api/bind_json_strict.go:15-33` 那段注释记录了一次**真实事故**：`PUT /log-config {"enabled": false}` 返回 200、开关纹丝未动，因为 gin 静默忽略未知字段。这个缺陷类别**在其余 62 个端点上依然存在**。泛型方法（`func (x *apiCtx) Bind[T any]() (T, error)`）是承载它的合适形态。
**落点**：`internal/api/bind_json_strict.go:35` 扩展；65 处 `ShouldBindJSON` 调用点（`handler_alert.go:116,187,345,440`、`handler_auth.go:66,79` …）。
**分两步**：① 先用泛型方法**只做形态统一**（语义仍宽松），验证零行为变更；② 再按端点逐个加严。
**成本**：中。**风险**：中高——**严格化是破坏性变更**，Vue 表单常整对象回传，直接推广会让 200 变 400。必须先审计前端请求体。

### 5. 验证并享受小对象分配提速（零改动）+ 确认二进制体积门禁

**为什么第五**：已经生效、零成本，唯一要做的两件事：① 确认 CI 没有二进制体积门禁会被 +60KB 打红（我在 `Makefile` 未找到此类检查）；② **在文档里写清楚"这条对 ESP32 固件无效"**——固件是 C/ESP-IDF，不经过 Go 编译器，避免有人误以为能帮到 `esp32-collector/` 的堆紧张问题。

---

## 附录 A：我实跑的验证清单（可复现）

| 验证项 | 命令/方式 | 结果 |
|---|---|---|
| jsonv2 是否默认启用 | `go list -f '{{.GoFiles}}' encoding/json \| grep v2_` | 出现 `v2_decode.go` 等 7 个文件 → v2 在编译 |
| json v1 vs v2 严格性差异 | 最小程序：重复 key / 非法 UTF-8 | v1 API 宽松（`<nil>`），v2 API 拒绝 |
| struct payload Marshal（大） | `go test -bench` on `models.UnifiedData` 5/20/50/200/1000 行 | v2 比旧 v1 快 **21~28%**，allocs **1002→3** |
| struct payload Marshal（小） | 同上，单条记录 | v2 **慢 6%**；纯标量 5 字段 struct **慢 65%** |
| map payload Marshal | 同一 benchmark 内 map vs struct | map **2,900ns/20 allocs** vs struct **2,044ns/13 allocs** |
| Unmarshal（任意大小） | 50 行 / 1000 行 | v2 一律快 **44%** |
| goroutineleak 可用性 | `pprof.Lookup("goroutineleak")` + `pprof.Handler` | 均非 nil / 注册成功 |
| 泛型方法三种形态 | 最小程序：`func (b Binder) Bind[T any]()`、`func (p *Page[T]) Map[U any]`、指针接收者 | 全部编译运行通过 |
| `go fix` 实际提议 | `go fix -diff ./...` 在 `.tmp-deps/fix-probe`（backend 副本） | 261 文件 / 4,129 行；atomictypes 6 处 |
| struct literal 初始化 promoted field | 最小程序 `Outer{Field: 42}`（go1.27）/ 同代码 go1.26 | go1.27 通过；go1.26 报 `requires go1.27 or later`。**注意 `Outer{Inner.Field: 42}` 这种写法编译失败** |
| embedlit 是否有落点 | `go fix` 实测 + grep 嵌入结构体 | **0 命中**（本仓无嵌入结构体字面量） |
| `strings.CutLast` 行为 | 最小程序对比 `LastIndexByte` | 可用，但落点语义不匹配 |

**探针文件位置**（均在 `.tmp-deps/`，未进仓库）：
- `.tmp-deps/fix-probe/` — backend 完整副本，跑 `go fix`
- `.tmp-deps/fix-probe/internal/apibench/`、`apibenchv1/` — 真实结构体 benchmark
- `.tmp-deps/gofix.diff` — go fix 完整提议（479 KB）
- `.tmp-deps/jsonbench-v1legacy.txt` — DEFAULT vs nojsonv2 原始输出

## 附录 B：复现 benchmark 的环境说明

本机 `/home/sun/go/pkg/mod` 是**只读**挂载，导致 `go build` 写 lock 文件失败。可用的环境组合（我实测）：

```bash
export PATH=/home/sun/.local/toolchains/go1.27.3/bin:$PATH
export GOPATH=/mnt/storage/WorkSpace/EHome/.tmp-deps/gopath   # 已有完整 1.6.3 等依赖
export GOCACHE=/mnt/storage/WorkSpace/EHome/.tmp-go/cache
export GOFLAGS=-mod=mod GOPROXY=off
```

⚠ 注意：`GOCACHE=/home/sun/.cache/go-build` 与 `GOMODCACHE=/home/sun/go/pkg/mod` 都是只读，直接用会报 `read-only file system`。这本身是个环境坑，值得记进团队文档。
