# BMS 详情页前端请求取证 —— 为什么图表一直在加载、为什么一直重复请求

- 取证人：frontend-request（task-2）
- 页面：生产 http://192.168.20.6:18090/edge-device/14 （BMS 详情页，设备 edge_devices.id=14 / jiabaida_bms / 16 串）
- 生产镜像：ehome-new:ui-fix2（容器 ehome-prod-web）；取证时只读：只发 GET、只在页面内注入 JWT，未做任何写操作
- 取证时段：2026-10-20 21:08–21:19（Asia/Shanghai）= 13:08–13:19 UTC
- 证据纪律：结论标【实测】/【推断】；所有耗时均来自浏览器侧 CDP 录制，不是 curl 串行基线；测量条件（并发、gzip、缓存、生产负载）逐节标注

---

## 0. TL;DR（两个问题的直接答案）

### Q1「为什么图表一直在加载」

【实测】进入页面后 5 个 historical-batch 同时在途（2 个图表卡 + 3 个诊断卡，代码见 §6.1）。它们各自有 10 s axios 超时（frontend-shared/src/api/client.ts:46）。生产在 5 路并发下单个 batch 远超 10 s ⇒ 本次实测 4/5 个 batch 在 10 s 处被浏览器 abort（net::ERR_ABORTED，页面侧 XHR 记录为 timeout）。

超时后 useHistoryData.ts:99-120 的降级分支把 1 个 batch 拆成 N 个 per-category 请求并发重发（cells 16 类 ⇒ 16 个）⇒ 一个降级轮次共 22 个请求（16 cells + 3 temp + 2 rsoc/current + 1 protection），且这些请求在浏览器 HTTP/1.1 的 6 连接/主机上限下排队（实测排队 3.7–6.0 s），继续抢占 10 s 预算。

因此图表卡的 loading 实测持续：
- 电芯电压历史趋势：20.03 s（516 ms → 20,547 ms）
- 运行趋势：8.75 s（516 ms → 9,266 ms）
- 保护触发历史 / 温度趋势 / SOC 与电流：各 20.03 s（516 ms → 20,547 ms）

【实测】20 s 之后，3 张诊断卡不是报错，而是显示「暂无保护状态历史数据」「暂无温度历史数据」「暂无 SOC / 电流历史数据」——因为降级请求也超时、且错误被 useHistoryData.ts:113 的 .catch(() => ({ cat, data: [] })) 与 :122-126 的外层 catch 吞掉，useDiagHistory.ts:89-106 的 error 永远为空字符串，卡片进入空态分支。用户看到的是"加载 20 秒 → 没有数据"，而不是"请求失败"（见 §7）。

### Q2「为什么一直重复请求」

【实测】一次进页 62 s 窗口内共 104 个请求（44 个 /api/），其中历史数据相关 27 个。重复分为两类：

1. 降级放大（同一批数据被拆碎重扫）：5 个 batch → 4 个超时 → 22 个 per-category（cells 1→16）。这一类是"1 个业务需求变成 22 个 HTTP 请求"，也是把服务端压死的直接原因（后端侧 31 个 goroutine 抢 25 条 DB 连接，见 task-1 文档）。
2. 与数据量无关的固定重复（实测同一 endpoint 多份，逐处代码见 §6.2）：
   - GET /edge-devices/14 ×3（t=358 / 479 / 8535 ms）
   - GET /edge-devices/14/commands ×2（t=479 / 479 ms，同毫秒发出）
   - GET /edge-devices/14/operations + /actions 各 ×2（t=480 与 t=11266 ms，第二次是 capability_stale 自愈链）
   - GET /notifications + /notifications/unread-count 各 ×2（t=357 / 372 ms；第二份来自 WS onConnected）
   - GET /nodes?page=1&page_size=20 + /edge-devices?page=1&page_size=24：与本页无关的列表预取（MainLayout.vue:410-413）

【实测】没有发现"超时后自动重试整轮"或"WS 重连触发重取"：整个窗口 WS 只建 1 条连接（open 后无 close，收 326 条消息），27 个历史请求 = 5 batch + 22 fallback，没有第二轮降级。所以"一直重复"是上述 1+2，而不是无限重试循环。

![进入页面 2s：保护触发历史显示"正在加载保护历史…"](.tmp-frontend/shot-A-lan-2000ms.png)
![进入页面 30s：同一张卡变成"暂无保护状态历史数据"（实际是请求失败被吞）](.tmp-frontend/shot-A-lan-30000ms.png)

---

## 1. 测量方法与条件（先看这一段再引用数字）

### 1.1 方法：真实浏览器 + CDP（不是 curl 推算）

- 浏览器：本机 Playwright 自带 Chromium 149（/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome），--headless=new --no-sandbox --remote-debugging-port=9333，独立 --user-data-dir=.tmp-frontend/chrome-profile。
  - 注：snap 版 chromium 在 DSH 的 bwrap 沙箱内启动失败（cannot create transient scope: DBus error ... Process 2 is a kernel thread），改用 Playwright 的 chrome 二进制；这是取证脚本环境差异，不影响页面行为。
- 埋点：CDP Network/Runtime/Page 三域 + 页面内注入探针（Page.addScriptToEvaluateOnNewDocument，脚本 .tmp-frontend/page-probe.js）：
  - Network.requestWillBeSent/responseReceived/dataReceived/loadingFinished/loadingFailed → 每个请求的开始/结束/状态/编码字节/解码字节/TTFB/协议/是否 abort；
  - 页面内 hook XMLHttpRequest → 记录 timeout/error/abort/load 事件，用于把 CDP 的 net::ERR_ABORTED 精确区分为 axios 10 s 超时；
  - 页面内 hook WebSocket → 连接/关闭/消息计数；
  - 每 250 ms 采样 DOM 状态文本（.chart-state / .trend-sub-state / .state-line / .stream-empty）→ 得到每个图表卡 loading 的真实起止；
  - 交叉校验：performance.getEntriesByType('resource') 的 duration/transferSize/encodedBodySize/decodedBodySize（与 CDP 数据一致）。
- 登录绕过：按 .tmp-verify/prod_api.sh 的 HS256 逻辑在脚本内签发 sv=2 JWT（sub=1），在页面 document 上下文 localStorage.setItem('token', jwt)（document-start 注入，先于应用脚本）→ 路由守卫直通，不触发登录请求。
- 视口：1440×900 桌面（Emulation.setDeviceMetricsOverride）。未做移动端视口专项（见 §10 限制）。
- 页面 HTTP/1.1、Accept-Encoding: gzip（浏览器自动 gzip，故下文"字节"默认是编码后传输字节，与 Lead curl 未压缩基线不可直接比）。

### 1.2 生产负载与污染声明（重要）

【Lead 通报】取证期间三路取证并发打生产，生产 loadavg（3 vCPU）一度冲到约 11.0；Lead 基线测量期为 20:57–21:05、loadavg≈3、串行。

【实测，本机 ssh 只读读取，21:19:52 CST】date; cat /proc/loadavg; nproc; free -m 输出：

~~~
2026年 10月 20日 星期二 13:19:52 UTC
6.87 9.76 6.16 1/866 3506730
3
               total        used        free      shared  buff/cache   available
内存：          4096       1005          73         154        3171        3090
~~~

即：收尾时 1 分钟 loadavg=6.87、5 分钟均值=9.76（说明测量窗口内确实有数分钟高负载），nproc=3。

因此本文所有绝对耗时（含 10 s 超时的发生）都发生在"高并发/高负载"条件下，应作为并发上界引用，不能当作"loadavg≈3 时用户也一定 20 s 加载"的基线。但注意：

- 请求数、重复份数、fallback 拆分数、loading 时长量级、静默失败路径由前端代码与 10 s 超时决定，不受负载高低改变；
- "是否恰好超时"受服务端排队影响，会在 10 s 阈值附近抖动（本次 5 次进页中，cells batch 有成功也有超时，见 §4.1）；
- 后端取证（task-1）已证明：5 个 batch 串行合计 21.06 s、单次进页 CPU ≈140 CPU-s（3 vCPU）——即使完全串行，也天然超过每个请求 10 s 的预算，降级不是纯偶发【推断，依据 task-1 §0/§3.3 的实测】。

### 1.3 进页/测量时刻表（每次进页都是一次重负载，共 5 次）

| # | 场景 | 页面加载时刻（CST / UTC） | 条件 | 用途 |
|---|---|---|---|---|
| 1 | A v1（初测） | ≈21:07 / 13:07 | LAN，1440×900 | 仅作口径试跑；原始 JSON 已被 v2 覆盖，不引用其数字 |
| 2 | A-lan（主证据） | 21:08:47 / 13:08:47（录制 62 s） | LAN 直连，1440×900，JSON HTTP 缓存已热 | 首屏请求清单、超时、fallback、loading 时间线 |
| 3 | B-throttle | 21:14:08 / 13:14:08（录制 90 s） | 模拟外部：CDP Network.emulateNetworkConditions latency=500 ms、download=200 KB/s、upload=100 KB/s，热缓存重载 | 限速下的降级（模拟，非真实外部链路） |
| 4 | B2-throttle（主证据） | 21:16:23 / 13:16:23（录制 80 s） | 同上（warm 40 s 后再限速重载，减少上一轮残留请求干扰） | 限速下 22 个 fallback 全失败、终态空数据的证据 |
| 5 | 终态 DOM 快照 | 21:18:31 / 13:18:31（26 s 后读 DOM + 截图） | LAN，1440×900 | 图表终态/静默失败/图表条数的 DOM 证据 |

> 21:18 之后未再对生产发任何请求（仅在 21:19:52 执行一次 cat /proc/loadavg 的只读 ssh）。浏览器进程已关闭、CDP 端口已释放（pgrep -af remote-debugging-port=9333 无输出、/json/version 连接拒绝）。

---

## 2. 生产构建 vs 当前工作树（外部访问看到的是哪个版本）

结论：【实测】生产部署的前端构建 == 当前工作树的构建产物（同一次构建），因此本文的行为结论对当前源码成立。

命令与原始输出（.tmp-frontend/ 内，全部为 GET）：

~~~
$ curl -s -D- -o prod-BmsDetailPage.js http://192.168.20.6:18090/assets/BmsDetailPage-WPUeFQt1.js
HTTP/1.1 200 OK
Content-Length: 78718
Content-Type: text/javascript; charset=utf-8
Last-Modified: Tue, 20 Oct 2026 11:17:53 GMT        # = 2026-10-20 19:17:53 CST

$ sha256sum prod-BmsDetailPage.js frontend-shared/dist/assets/BmsDetailPage-WPUeFQt1.js
939c5dab7c835257ccc192c2a15a29cd0ad3c315a1659369aa710e4b85c60451  prod-BmsDetailPage.js
939c5dab7c835257ccc192c2a15a29cd0ad3c315a1659369aa710e4b85c60451  frontend-shared/dist/assets/BmsDetailPage-WPUeFQt1.js
~~~

- 生产 index.html 引用的 chunk 列表与本地 frontend-shared/dist/index.html 逐行一致（diff 无输出，INDEX_CHUNKS_IDENTICAL）。
- 另外两个关键 chunk 也逐字节一致：
  - useRealtimeData-DK8x2P1a.js（内含 useHistoryData，10,951 B）sha256 cc8bf88a…，其中可读出降级分支（batch → per-category + .catch(()=>({cat,data:[]}))）与源码 useHistoryData.ts:81-120 结构一致；
  - client-A9kBAH-q.js（46,148 B）sha256 6f402b96…，其中 timeout:1e4 ⇒ 生产运行时确为 10 s 超时。
- 构建时间 19:17:53 CST 晚于所有 BMS 源文件 mtime（BmsDetailPage.vue 18:13:49、BmsChartsPanel.vue 16:39、diag/useDiagHistory.ts 16:35、composables/useHistoryData.ts 2026-10-04），且 git status --porcelain 中 BMS 目录无改动 ⇒ 生产构建对应的就是当前工作树源码。
- 无 sourcemap（GET /assets/BmsDetailPage-WPUeFQt1.js.map 返回 text/html 的 index 兜底），所以无法用 sourcemap 逐行回映射；代码行号引用来自工作树源码（与生产构建同源，见上）。

---

## 3. 首屏请求清单（场景 A-lan，LAN，21:08:47 CST 起 62 s）

【实测】总请求 104；/api/ 44；失败 12（全部 net::ERR_ABORTED，页面侧 XHR 全部标记 timeout）。
【实测】API 传输字节：编码后 267,739 B；解码后（JSON 原始体）2,390,210 B（约 2.4 MB）；文档/JS/CSS/图片本次全部命中浏览器 HTTP 缓存（encoded 0），首个访问者还要额外下载约 42 个 script chunk（未计入本次字节）。

### 3.1 首屏 API 时间线（t≤12 s）

| t_start(ms) | dur(ms) | 状态 | enc(B) | dec(B) | ttfb(ms) | 端点 | 类别数 |
|---|---|---|---|---|---|---|---|
| 357 | 5 | 200 | 231 | 37 | 4 | /api/v1/notifications | - |
| 357 | 5 | 200 | 238 | 46 | 3 | /api/v1/notifications/unread-count | - |
| 358 | 8 | 200 | 2297 | 8394 | 7 | /api/v1/edge-devices/14 | - |
| 370 | 7 | 200 | 2806 | 22334 | 4 | /api/v1/nodes (列表预取) | - |
| 370 | 12 | 200 | 3610 | 26438 | 10 | /api/v1/edge-devices (列表预取) | - |
| 372 | 8 | 200 | 231 | 37 | 2 | /api/v1/notifications（第 2 份） | - |
| 372 | 5 | 200 | 238 | 46 | 2 | /api/v1/notifications/unread-count（第 2 份） | - |
| 479 | 7 | 200 | 733 | 1353 | 4 | /api/v1/edge-devices/14/commands | - |
| 479 | 7 | 200 | 2297 | 8394 | 5 | /api/v1/edge-devices/14（第 2 份） | - |
| 479 | 7518 | 200 | 733 | 1353 | 7510 | /api/v1/edge-devices/14/commands（第 2 份） | - |
| 479 | 8659 | 200 | 94,802 | 892,028 | 8638 | historical-batch | 9（metrics） |
| 479 | 10004 | net::ERR_ABORTED | - | - | - | historical-batch | 16（cells） |
| 480 | 7612 | 200 | 231 | 37 | 7604 | /api/v1/edge-devices/14/operations | - |
| 480 | 8053 | 200 | 3252 | 23388 | 8051 | /api/v1/edge-devices/14/actions | - |
| 480 | 9724 | 200 | 779 | 2837 | 9722 | /api/v1/edge-devices/14/latest-data | - |
| 503 | 10001 | net::ERR_ABORTED | - | - | - | historical-batch | 1（protection×30d） |
| 503 | 10002 | net::ERR_ABORTED | - | - | - | historical-batch | 3（temp） |
| 503 | 10002 | net::ERR_ABORTED | - | - | - | historical-batch | 2（rsoc+current） |
| 8535 | 650 | 200 | 2292 | 8395 | 46 | /api/v1/edge-devices/14（第 3 份） | - |
| 9233 | 20 | 200 | 292 | 110 | 19 | /api/v1/nodes/F0F5BD02DD84/query-resources | - |
| 10480–10505 | 3.7s–10.06s | 14×200 + 8×abort | 共 149 KB | 共 1.37 MB | - | historical ×22（降级，见 §4） | 16+6 |
| 11266 | 9342 | 200 | 231 | 37 | 43 | /api/v1/edge-devices/14/operations（第 2 份） | - |
| 11266 | 9388 | 200 | 3183 | 20784 | 89 | /api/v1/edge-devices/14/actions（第 2 份） | - |

要点：
- 【实测】"首屏历史请求"是 5 个 historical-batch（不是 6 个）：2 个来自图表 BmsChartsPanel、3 个来自诊断卡。若把 operations（受控操作历史列表）也算作"历史数据"，则页面上可见 6 个历史类取数点——任务书里的"6"应指这一口径。
- 【实测】这 5 个 batch 全部在同一秒内（479–503 ms）发出，互不等待；3 个诊断卡受 useDiagHistory 的并发信号量 ≤3 约束（diag/useDiagHistory.ts:19-53），但此刻只有 3 个，等于没限流。
- 【实测】同一时刻非历史请求也被拖慢：latest-data 9.72 s（后端串行基线 0.32 s，Lead 基线 0.306 s）、actions 8.05 s、operations 7.61 s、第二次 commands 7.52 s——说明 5 个重查询把整台生产拖到几乎所有请求都 >7 s。这解释了"页面好像卡死"的体感，也是"图表一直加载时其它区域也点不动"的原因。

### 3.2 5 个 batch 的参数（谁发的、查什么）

| 组件（源码） | 触发点 | categories | 时间窗 | max_points |
|---|---|---|---|---|
| BmsChartsPanel.vue:163-174（电芯历史） | onMounted :306-315 | cell_voltage_1..16 | 默认 24 h（useTimeRange.ts:18） | 500 |
| BmsChartsPanel.vue:238-243（运行趋势） | 同上 | total_voltage,current,rsoc,remaining_capacity,temperature_1..3,cell_voltage_max/min（9） | 24 h | 500 |
| diag/ProtectionHistoryCard.vue:158-177 | onMounted(load) :180；watch([deviceId,online]) :182 | protection_status | 30 d（rangeDays=30，:127） | 500 |
| diag/TempTrendCard.vue:146-176 | onMounted(load) :178；watch :179 | temperature_1,2,3 | 24 h | 500 |
| diag/SocCurrentCard.vue:116-141 | onMounted(load) :143；watch :144 | rsoc,current | 24 h | 500 |

---

## 4. 降级放大器：1 个 batch → 22 个请求

### 4.1 机制（源码）

frontend-shared/src/composables/useHistoryData.ts:69-128：

- :81-89 先发 GET /api/v1/unified-data/historical-batch（一个请求带全部 categories）；
- :99-120 catch 任意失败（含 axios 10 s 超时）→ categories.map(...) 对每个 category 各发一个 GET /api/v1/unified-data/historical，Promise.all 并发；
- :113 每个 per-category 请求再 .catch(() => ({ cat, data: [] })) 静默吞掉自身失败；
- :122-126 外层 catch 理论上兜底，但因内层已吞，实际几乎不会把错误抛给调用方。

因此超时不会"失败给用户看"，而是变成更多请求；而降级请求依然是"全窗口无 LIMIT 扫描"（后端 task-1 已量化：单 category 24 h = 扫 281 万行），所以一次降级 = 服务端工作量再翻 N 倍。

### 4.2 实测 A-lan：4/5 batch 超时 → 22 个降级请求

| 降级来源 | 原 batch | 降级请求数 | 其中成功 | 其中 10 s 超时 |
|---|---|---|---|---|
| cells（16 类） | 超时 @10004 ms | 16 | 14（3.71–8.25 s） | 2（cell_voltage_15、16） |
| protection（1 类） | 超时 @10001 ms | 1 | 0 | 1 |
| temp（3 类） | 超时 @10002 ms | 3 | 0 | 3 |
| rsoc+current（2 类） | 超时 @10002 ms | 2 | 0 | 2 |
| metrics（9 类） | 未超时（8659 ms 成功） | 0 | - | - |
| 合计 | 4 个 batch | 22 | 14 | 8 |

【实测】降级发生在 t=10,480–10,505 ms（4 个 batch 各自在 10 s 到期时触发）；页面侧 XHR 探针记录到 22 条降级请求中 8 条 timeout（durMs≈10,000–10,059，textLen=0）；CDP 侧对应 22 条中 8 条 net::ERR_ABORTED。

【实测】排队证据（HTTP/1.1 同主机 6 连接上限）：降级请求的 dur - ttfb = 请求在浏览器连接队列里的等待，实测 14 个成功请求中 10 个排队 3.7–6.0 s：

| category | dur(ms) | ttfb(ms) | 排队=dur-ttfb(ms) |
|---|---|---|---|
| cell_voltage_1 | 3753 | 3751 | 2 |
| cell_voltage_5 | 3706 | 3679 | 27 |
| cell_voltage_7 | 8253 | 4547 | 3706 |
| cell_voltage_11 | 8005 | 4194 | 3811 |
| cell_voltage_12 | 6899 | 3042 | 3857 |
| cell_voltage_13 | 8200 | 2287 | 5913 |
| cell_voltage_14 | 8233 | 2299 | 5934 |
| cell_voltage_15 | 10000 | - | 全部为排队+超时 |
| cell_voltage_16 | 10054 | - | 同上 |
| protection/temp×3/rsoc/current（6 个） | 10058–10059 | - | 排在队尾，10 s 预算耗尽被杀 |

【推断】这 22 个请求里排在 6 连接之外的 16 个，其 10 s 计时从"发出（t≈10.48 s）"开始，而真正拿到连接已在 3.7–6 s 之后 ⇒ 即使在低负载下，"排队 + 单 category 全窗口扫描"也很容易吃满剩余的 4–6 s。这解释了为什么降级后仍有 8 个请求超时——降级不是重试成功路径，而是把失败概率放大并摊到更多请求上。

【实测／推断】A-lan 这轮 cells 降级最终拿到 14/16 串数据（2 串丢失），图表仍正常渲染 14 条线，界面没有任何错误提示（电芯图提示文案由 BmsChartsPanel.vue:197 计算：'已选 ' + cellShown.length + '/' + cellAll.length，而 cellAll 只保留有数据的串 :168-173 ⇒ 会显示"已选 14/14 串"，用户不知道少了 2 串；此为代码推断，同页 DOM 在全部成功时实测为"已选 16/16 串"，见 §7.2）。

### 4.3 实测 B2-throttle（模拟外部访问，可复现且更严重）

条件：CDP 模拟（非真实外部链路）：Network.emulateNetworkConditions{latency:500ms, downloadThroughput:204800 B/s, uploadThroughput:102400 B/s} + 40 s 热缓存预热后重载。属"外部慢网 + 本页并发"的近似，不能替代 task-3 的真实外部链路测量。

| 指标 | A-lan（LAN） | B2-throttle（模拟外部） |
|---|---|---|
| API 请求数 | 44 | 40 |
| 历史请求数 | 27（5+22） | 27（5+22） |
| 失败数（页面侧 timeout） | 12 | 28 |
| batch 超时 | 4/5 | 5/5（cells batch 收到 200 头后仍在 10.004 s 被 abort；TTFB 9.32 s） |
| 降级请求成功 | 14/22 | 0/22 |
| 最终电芯历史 | 14/16 串 | 0/16 → "暂无电芯电压历史数据" |
| 最终诊断卡 | 3 张"暂无" | 3 张"暂无" |
| latest-data | 9.72 s 成功 | 20.001 s 被 abort（该接口单独放宽 20 s，api/edgeDevice.ts:385-389） |
| actions | 8.05 s 成功 | 10.005 s 超时 |
| API 编码字节 | 267,739 B | 108,148 B（大量请求没拿到响应体） |

【实测】限速下每图表 loading 时长（250 ms 采样）：电芯历史 20.0 s、运行趋势 9.06 s、3 张诊断卡 20.0 s；之后电芯图 21.26 s 起显示"暂无电芯电压历史数据"、3 张诊断卡 21.76 s 起显示"暂无…"，一直持续到录制结束（80 s）。截图中可见：进入页面初期（1.26–1.76 s）设备详情尚未返回时，诊断卡先闪 500 ms 的"设备离线，历史数据暂不可用"（deviceOnline 由详情请求决定，BmsDetailPage.vue:786-789；卡片 v-if="!online" 提前返回 ProtectionHistoryCard.vue:160-164），随后才进入 loading。

---

## 5. 图表卡 loading 真实持续时间（DOM 采样，250 ms 粒度）

### 5.1 A-lan（LAN，62 s 录制）

| 图表卡 | loading 出现 | loading 结束 | 持续 | 结束后的终态 |
|---|---|---|---|---|
| 电芯电压历史趋势（.chart-state 正在加载历史数据…） | 516 ms | 20,547 ms | 20,031 ms | 数据（14/16） |
| 运行趋势（5 子图 加载中…） | 516 ms | 9,266 ms | 8,750 ms | 5 张 ECharts（9 类数据） |
| 保护触发历史 | 516 ms | 20,547 ms | 20,031 ms | 暂无保护状态历史数据（静默失败） |
| 温度趋势 | 516 ms | 20,547 ms | 20,031 ms | 暂无温度历史数据（静默失败） |
| SOC 与电流 | 516 ms | 20,547 ms | 20,031 ms | 暂无 SOC / 电流历史数据（静默失败） |
| 受控操作目录（正在加载受控操作目录…） | 516 ms | 20,766 ms | 20,250 ms | 目录就绪（含 capability_stale 自愈往返） |
| 轮询间隔分布 | 516 ms | 8,016 ms | 7,500 ms | 正常（1 条轮询） |
| ECharts canvas 数量 | 0（516 ms） | 1（8,766 ms）→ 6（9,266 ms）→ 7（20,547 ms） | - | - |

### 5.2 B2-throttle（模拟外部，80 s 录制）

| 图表卡 | loading 出现 | loading 结束 | 持续 | 终态 |
|---|---|---|---|---|
| 电芯电压历史趋势 | 1,264 ms | 21,264 ms | 20,000 ms | 暂无电芯电压历史数据 |
| 运行趋势 | 1,264 ms | 10,325 ms | 9,061 ms | 5 张图正常 |
| 保护/温度/SOC（3 张） | 1,764 ms | 21,764 ms | 20,000 ms | 3 张"暂无" |
| 受控操作目录 | 1,264 ms | 11,264 ms | 10,000 ms | 目录就绪（actions 超时，但 operations 成功） |

【实测】loading 时长不是"卡住不动"，而是精确等于 10 s 超时 + 降级轮次（10 s 超时/或成功）的两级叠加；即用户感知的"一直转圈"= 两个 10 s 窗口串联。

![LAN 进入 10 s：保护触发历史仍在"正在加载保护历史…"（第一个 10 s 窗口刚到期）](.tmp-frontend/shot-A-lan-10000ms.png)

---

## 6. 重复请求逐处归因（file:line）

### 6.1 历史数据类（业务取数点，共 5 个 batch + 降级）

| # | 请求 | 发起处（源码） | 说明 |
|---|---|---|---|
| 1 | historical-batch ×1（16 类 cells） | BmsChartsPanel.vue:306-315 → :163-174 | 图表卡 onMounted 直接取数，不受 online 门控 |
| 2 | historical-batch ×1（9 类 metrics） | BmsChartsPanel.vue:306-315 → :238-243 | 同上；与 #1 同 tick 并发 |
| 3 | historical-batch ×1（protection，30 d） | diag/ProtectionHistoryCard.vue:158-182 | onMounted + watch([deviceId, online])；online 由 false→true 时再 load 一次 |
| 4 | historical-batch ×1（temp） | diag/TempTrendCard.vue:146-179 | 同上 |
| 5 | historical-batch ×1（rsoc+current） | diag/SocCurrentCard.vue:116-144 | 同上 |
| 6–27 | historical（per-category）×22 | useHistoryData.ts:99-120 | 由超时触发；cells 16 + temp 3 + rsoc/current 2 + protection 1 |

补充（为什么不重复、别误判）：
- 【实测】3 张诊断卡的 watch(() => [props.deviceId, props.online], load)（ProtectionHistoryCard.vue:182、TempTrendCard.vue:179、SocCurrentCard.vue:144）在本页只在 online false→true 时触发 1 次，没有造成二次请求（A-lan 中 deviceOnline 在 479 ms 变 true，卡片仍在 516 ms 首次渲染并直接取数；B2 中先渲染"离线"500 ms 再取数一次）。BmsChartsPanel 只有 onMounted（:306-315），无重复。
- 【实测】WS 没有重连：A-lan 1 条连接（t=369 ms open，窗口内无 close，收 326 条消息）；B2 1 条（open 1,054 ms，收 430 条）；B-throttle 1 条（open 6,437 ms，收 602 条）。useRealtimeData 的订阅（composables/useRealtimeData.ts:160-174）不发 HTTP。故"WS 重连导致重取"不成立。

### 6.2 非历史类固定重复（与数据量无关）

| 请求 | 次数 | 发起处（源码） | 触发条件 / 结论 |
|---|---|---|---|
| GET /api/v1/edge-devices/14 | 3 | ① EdgeDeviceDetailRouter.vue:86（edgeDeviceStore.fetchDetail(id,true) → stores/edgeDevice.ts:121-128）② BmsDetailPage.vue:806（loadDetail，onMounted :2000）③ stores/deviceOperation.ts:83（refreshResourceSnapshot 内 edgeDeviceApi.getDetail） | ①路由解析设备类型、②页面自身详情、③受控操作 capability_stale 自愈；三处各自独立取同一份详情。实测 t=358/479/8535 ms，返回体均 2,297 B 左右 |
| GET /edge-devices/14/commands | 2 | ① BmsDetailPage.vue:1331-1335（loadPollCommands，onMounted :2001）② diag/PollIntervalCard.vue:118-135（load → edgeDeviceApi.getCommandIntervals，onMounted :135） | 同一份轮询配置被父页与诊断卡各取一次；实测同毫秒 t=479 ms 发出，第二份因服务端争用耗时 7.5 s |
| GET /edge-devices/14/operations + /actions | 各 2 | ① BmsDetailPage.vue:1848-1858（loadOperations → opStore.refresh，onMounted :2003）② stores/deviceOperation.ts:75-99：:77 先取一次 catalog/history，若 catalog 含 capability_stale 则 :83 取详情 → :91 发 query-resources → 等 resourceReportWaitMs=2000（:92）→ :94 再取一次 | 设计内的自愈往返，但代价是 operations/actions 各 ×2 + 详情 ×1 + nodes/.../query-resources ×1。实测时间线完全吻合：480 ms 首对 → 8535 ms 详情 → 9233 ms query-resources → 11266 ms 第二对（9233+20+2000≈11253） |
| GET /notifications + /unread-count | 各 2 | ① MainLayout.vue:480-489 函数，调用点 :523（setup 立即执行）② MainLayout.vue:586（wsStore.onConnected(() => fetchNotifications())） | WS 连上即再拉一次（补断线遗漏）。实测 A-lan：357 ms（首拉）→ 372 ms（WS open 后第二次）；B2：554 ms → 1056 ms（限速下 WS 握手更慢） |
| GET /nodes?page=1&page_size=20、/edge-devices?page=1&page_size=24 | 各 1 | MainLayout.vue:410-413（warmPrimaryListData）+ :896-903（onMounted 里 setTimeout(...,0) 预取） | 与 BMS 详情页无关的列表预热；在 5 个重查询同时在途时仍会发出（实测 370 ms，7–12 ms 返回，因为它们是轻查询） |

---

## 7. 静默失败：为什么"加载 20 秒后变成没有数据"

### 7.1 代码链

1. useHistoryData.ts:99-120：batch 失败 → per-category 并发；每个 per-category 失败在 :113 .catch(() => ({ cat, data: [] })) 被吞掉（不抛出）。
2. useHistoryData.ts:122-126：外层 catch 只在"连降级都没跑起来"时才会执行，实际上拿不到错误。
3. diag/useDiagHistory.ts:89-106：fetchRange 返回 { series, error }；因为第 1 步已吞错，error 恒为 ''（"永不上抛"，见 :82-88 注释自称 fail-open）。
4. 卡片：ProtectionHistoryCard.vue:167-172 只有 error 非空才写 errorMsg（从而渲染 :62-66 的"加载失败 + 重试"）；否则 hasData=false → 渲染 :69-71 的"暂无保护状态历史数据"。TempTrendCard.vue:154-165、SocCurrentCard.vue:124-130 同构。
5. 同理 BmsChartsPanel：cells 降级丢 2 串后 cellAll 只保留 14 条（:168-173），图表照常画 14 条线，无任何提示。

### 7.2 实测 DOM 证据（终态快照，21:18:57 CST，LAN，第 5 次进页 26 s 后）

用 Runtime.evaluate 读 DOM（原始 JSON：.tmp-frontend/ 运行输出）：

~~~
chartCards:
  - 电芯电压历史趋势: hint="已选 16/16 串"   (本次 cells 全部成功)
diagCards:
  - 保护触发历史: state="暂无保护状态历史数据"          <-- 该卡请求实际已超时/失败
  - 温度趋势:     tip="最高 37.5 ℃"                    (成功)
  - SOC 与电流:   state="暂无 SOC / 电流历史数据"       <-- 该卡请求实际已超时/失败
  - 轮询间隔分布: tip="1 条轮询中 · 合计 1.0 帧/秒"      (success)
canvases: 8
~~~

同一时刻页面顶部的实时数据/保护状态正常（WS 1 Hz 在推送，protection_status = 0x00、12 项"正常"），说明设备与后端在线、字段有数据，只是历史接口失败被渲染成"暂无数据"。

【实测】同一天不同进页的终态不一样：A-lan 为保护/温度/SOC 三张"暂无"、cells 14/16；B2 为四张"暂无"、cells 0/16；第 5 次进页为保护/SOC 两张"暂无"、cells 16/16。⇒ 用户每次刷新看到的"缺失数据"是随机的、且没有任何失败提示；这正是"图表一直在加载 / 数据时有时无"的观感来源之一。

![终态：保护触发历史"暂无保护状态历史数据"，而页面上方保护状态全部正常上报](.tmp-frontend/shot-final-diag.png)

---

## 8. 30/60 秒请求数、字节统计

| 指标 | A-lan（LAN，62 s） | B2-throttle（模拟外部，80 s） |
|---|---|---|
| 全窗口总请求（含静态资源，本次全缓存） | 104 | 100 |
| /api/ 请求 | 44 | 40 |
| 其中历史类（batch+per-category） | 27 | 27 |
| 30 s 内 API 请求数 | 44 | 40 |
| 60 s 内 API 请求数 | 44（30 s 后无新增 API） | 40（同） |
| API 编码传输字节（30 s / 60 s） | 267,739 B / 267,739 B | 108,148 B / 108,148 B |
| API 解码后字节（JSON 原始） | 2,390,210 B | 960,528 B |
| 失败（页面侧 timeout） | 12 | 28 |
| 无用户操作时的自动重取（>30 s 后） | 0 | 0 |
| WS 连接数 / 消息数 | 1 / 326 | 1 / 430 |

【实测】30 s 后页面不再发出新请求（无轮询刷新、无自动重试）；"重复"集中在进入后的前 21.8 s。

---

## 9. 与后端取证的交叉核对（不重复其内容）

- task-1 实测串行基线：cells 5.65 s、metrics 3.23 s、protection 8.99 s（30 d）、temp 1.63 s、soc+current 1.24 s，5 batch 串行合计 21.06 s；单请求 10 s 超时的判定点与本文"4/5 超时"一致（并发使其恶化）。
- 本文补充前端侧的两个独立放大器：
  1. 超时 → 降级 → 22 请求（§4），服务端工作量与连接占用再乘数倍；
  2. 超时 → 静默空态（§7），用户不会重试/不会知道失败，问题被"暂无数据"掩盖。
- latest-data 的 20 s 超时覆盖（api/edgeDevice.ts:385-389）在 B2 中实测触发（20.001 s abort），说明慢网下连"实时数据快照兜底"都会失败。

---

## 10. 限制与未覆盖

1. 生产负载污染：全部浏览器测量发生在 loadavg 6–11（3 vCPU）窗口内，绝对耗时只能作"并发上界"引用；基线与串行口径见 Lead 基线与 task-1 文档。请求数/重复份数/降级结构不受负载影响。
2. 限速是模拟：B-throttle / B2-throttle 用 CDP Network.emulateNetworkConditions（500 ms RTT、200/100 KB/s），是"外部慢网"的近似，不是真实外部路径；真实外部链路（IPv6/反代/运营商）由 task-3 负责。
3. 静态资源字节未测：本次浏览器 HTTP 缓存已热（document/42 script/16 css 命中缓存，encoded=0），首次访问的静态资源体积与首字节不在本文范围。
4. 移动端视口未单独测：1440×900 桌面下 CollapseBlock 是零包装（内容必挂载）；窄屏折叠只是 display:none（CollapseBlock.vue 用内联 display:none 保活，不是 v-if），因此窄屏同样在挂载时取全部 5 个 batch，请求集合与桌面一致（此为代码推断，未做窄屏 CDP 复测）。
5. 无 DevTools Network 面板截图：headless CDP 环境无法截 DevTools UI；本文提供 CDP 原始请求表和页面 loading/终态截图，原始 JSON 见 §11。
6. 未测：点击时间范围切换、下拉选电芯、移动端展开折叠块等交互后的请求（本次任务只要求"进入页面"行为）。

---

## 11. 原始产物与复现

### 11.1 产物（.tmp-frontend/，工作区 scratch，未入库）

| 文件 | 内容 |
|---|---|
| cdp-bms-probe.mjs | CDP 驱动：Network 录制 + 注入探针 + 限速模拟 + 截图（用法：node cdp-bms-probe.mjs '{"name":"A-lan","durationMs":62000}'） |
| page-probe.js | document-start 注入脚本：JWT 注入、XHR/WS hook、250 ms DOM 采样 |
| inspect.mjs | 终态 DOM 读取 + 滚动截图脚本 |
| scenario-A-lan.json | 主证据：104 请求全部字段（含 timing/encoded/decoded/initiator）、XHR 探针 44 条、WS 事件、DOM 采样 247 条 |
| scenario-B-throttle.json / scenario-B2-throttle.json | 限速模拟两轮 |
| report.mjs / tables.md | 从 JSON 生成请求表 |
| shot-A-lan-2000ms/10000ms/30000ms.png | LAN 下 2 s/10 s/30 s 页面截图 |
| shot-B-throttle-5000/20000/45000ms.png、shot-B2-throttle-5000/20000/45000ms.png | 模拟外部下截图 |
| shot-final-charts.png / shot-final-diag.png | 终态：图表区 / 诊断卡"暂无数据" |
| prod-BmsDetailPage.js、prod-useRealTimeData.js、prod-client.js | 生产 chunk 副本（用于 §2 比对，只 GET） |

### 11.2 关键复现命令

~~~
# 1) 启动独立 headless Chromium（本机，不碰生产）
/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome \
  --headless=new --no-sandbox --disable-dev-shm-usage --disable-gpu \
  --remote-debugging-address=127.0.0.1 --remote-debugging-port=9333 \
  --remote-allow-origins='*' \
  --user-data-dir=/mnt/storage/WorkSpace/EHome/.tmp-frontend/chrome-profile \
  --window-size=1440,900 about:blank &

# 2) 主证据（LAN 首屏，62 s）
cd .tmp-frontend && node cdp-bms-probe.mjs '{"name":"A-lan","durationMs":62000,"shots":[2000,10000,30000]}'

# 3) 模拟外部（500 ms RTT / 200 KB/s；40 s 预热后限速重载）
node cdp-bms-probe.mjs '{"name":"B2-throttle","durationMs":80000,"warm":true,"warmMs":40000,"shots":[5000,20000,45000],"net":{"latency":500,"downloadThroughput":204800,"uploadThroughput":102400,"connectionType":"cellular3g"}}'
~~~

### 11.3 关键原始输出摘录（A-lan 控制台）

~~~
 479 |   8659 | 200 |    94802 | /api/v1/unified-data/historical-batch?...9 类 (metrics)
 479 |  10004 | net::ERR_ABORTED |  /api/v1/unified-data/historical-batch?...16 类 (cells)
 503 |  10001 | net::ERR_ABORTED |  /api/v1/unified-data/historical-batch?...protection_status (30d)
 503 |  10002 | net::ERR_ABORTED |  /api/v1/unified-data/historical-batch?...temperature_1..3
 503 |  10002 | net::ERR_ABORTED |  /api/v1/unified-data/historical-batch?...rsoc,current
10480 |   3753 | 200 |    10663 | /api/v1/unified-data/historical?category=cell_voltage_1
...
10481 |  10000 | net::ERR_ABORTED |  /api/v1/unified-data/historical?category=cell_voltage_15
10503 |  10059 | net::ERR_ABORTED |  /api/v1/unified-data/historical?category=protection_status
~~~
（页面侧 XHR 探针同窗口：44 条记录中 12 条 timeout=true，textLen=0。）

---

## 12. 结论（前端侧，供后续修复方案使用）

1. 【实测+推断】"图表一直加载"的结构性原因：5 个无 LIMIT 的历史查询在进页瞬间并发 + 每个请求 10 s 超时；超时后降级为 22 个 per-category 全窗口查询，二级 10 s 窗口叠加 ⇒ 单个图表 loading 20 s 起步；在模拟外部（500 ms RTT / 200 KB/s）下 22 个降级请求全部失败，loading 20 s 后终态为"暂无数据"。
2. 【实测】"重复请求"的构成：27 个历史请求（5 个业务 batch + 22 个降级）；另有 7 个固定重复（detail ×3、commands ×2、operations/actions 各 ×2、notifications 各 ×2）+ 2 个无关列表预取。没有无限重试循环，也没有 WS 重连重取。
3. 【实测】失败不可见：降级路径吞掉所有错误，诊断卡把"请求失败"渲染成"暂无数据"，电芯图少 2 串也不提示；用户无法区分"设备没数据"与"前端加载失败"，也就不会重试或反馈。
4. 【推断，需修复验证】最小必要改动方向（不在本次取证范围）：a) 历史查询合并/分页/服务端先降采样（后端 task-1）；b) 超时策略不要与"全量并发"叠加（如串行/限并发 + 单请求超时 > 服务端 P95）；c) 降级路径保留错误上抛，卡片显示"失败 + 重试"而不是"暂无数据"；d) 复用同一份详情/轮询配置/受控操作目录，去掉 detail ×3、commands ×2 等固定重复。
