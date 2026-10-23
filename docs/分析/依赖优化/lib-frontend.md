# 前端**业务库**能力深挖（element-plus / echarts / axios / pinia / vue-router / vue-virtual-scroller）

> 仓库：`/mnt/storage/WorkSpace/EHome`　范围：`frontend-shared/src/`（101 个 `.vue` + 295 个 `.ts`，`find` 实测）
> 版本（`node -e require('pkg/package.json').version` 逐个实测，非猜）：element-plus **2.14.7** / echarts **6.1.0** / axios **1.20.0** / pinia **4.1.0** / vue-router **5.4.0** / vue-virtual-scroller **3.0.5** / vue 3.5.44 / vite 8.3.5
> 分析日期：2026-10-23　**只分析，未改任何业务代码**（唯一写入：`.tmp-probe/` 下的探针与 3 份对比构建产物，全部在 gitignore 覆盖范围内）
> 所有体积数字均来自**本机实跑构建**（`vite build` + 自写 gzip 统计脚本），非文档抄录。

---

## 0. 结论速览

| 库 | 当前版本 | 一句话定位 |
|---|---|---|
| element-plus | **2.14.7** | 按需引入对了、chunk 分组错了 —— **本次最大收益点** |
| echarts | **6.1.0** | 早已按需引入，565 KB 是真实下限，**没有大压缩空间** |
| axios | **1.20.0** | 无内建 retry；1 个错误身份保真问题值得修 |
| pinia | **4.1.0** | 12 setup / 5 options，无 Vuex 残留，健康 |
| vue-router | **5.4.0** | 100% 懒加载；meta 有 4 个死字段 |
| vue-virtual-scroller | **3.0.5** | 只用 1 处（用对了），另有 1 处该用没用 |

| # | 库 | 现在怎么用（证据） | 更好的方式 | 实测收益 | 成本 |
|---|---|---|---|---|---|
| **1** | **element-plus 2.14.7** | 已正确按需引入（`ElementPlusResolver`，[vite.config.ts:35](frontend-shared/vite.config.ts#L35)、[:41](frontend-shared/vite.config.ts#L41)）。**但** `manualChunks` 把**全部**用到的 EP 组件塞进单个 `element` chunk 并挂在首屏 preload（[vite.config.ts:116](frontend-shared/vite.config.ts#L116)） | 删掉这一行分组，让 EP 跟随路由懒加载 | **首屏 JS gzip 267.9 → 105.2 KB（−60.7%）**；含 CSS 的完整首屏 **310.2 → 116.1 KB（−62.6%）**。`/login` 路由闭包 272.7 → 144.1 KB | **小**（删 1 行） |
| 2 | element-plus：废弃 API | 全仓扫描：`type="text"` **0 处**、`#title` 用在 dialog/drawer 上 **0 处**（其余 `#title` 是 el-alert / el-sub-menu / el-collapse-item，未废弃）、`el-checkbox` 同时给 `:value` 与 `:label` ⇒ 不触发废弃告警（[use-checkbox.mjs:38-44](frontend-shared/node_modules/element-plus/es/components/checkbox/src/composables/use-checkbox.mjs#L38)） | — | **无债可还**（已合规） | — |
| 3 | element-plus：新组件 | `el-watermark`/`el-tour`/`el-anchor`/`el-segmented`/`el-splitter`/`el-table-v2` **0 处使用**（grep 实测） | 只有 **`el-segmented`** 有真实落点：替换 6 个文件里的 **23 个 `<el-radio-button>`** + 21 行 `:deep(.el-radio-button__inner)` CSS | 每处省 ~2–4 行模板 + CSS 选择器改名；**净代码收益小**，收益在「少一层 radio-group 语义」 | 低 |
| 4 | **echarts 6.1.0** | **不是全量引入**——8 个文件共 34 条 import（生产侧仅 2 文件 10 条），全部走 `echarts/core` + `echarts/charts`/`components`/`renderers` 子路径（见 §2.1 全量清单）。565,524 B / 188,657 B gzip 的 chunk 是「Canvas + Line + Bar + Grid + Tooltip + Legend + Title + MarkLine」这个**组合本身**的体积 | 已是最优形态。**实测**：全量 `import * as echarts from 'echarts'` 会是 **1,115.94 KB raw / 370.16 KB gzip**（现在的 1.97×） | 按需引入**已经在省** 550 KB raw / 181 KB gzip；**剩余可压只有 ~10–20 KB**（Legend 18 KB + Title 2.4 KB），不值当 | — |
| 5 | **echarts 6 新特性** | 主题靠**运行时读 CSS 变量** + `MutationObserver` 监听 `class`/`data-theme`（[LineChart.vue:247-268](frontend-shared/src/components/charts/LineChart.vue#L247)、[:695-696](frontend-shared/src/components/charts/LineChart.vue#L695)），7 个文件重复了这套管线（共 50 行） | ECharts 6 内置 `dark` 主题（[lib/core/echarts.js:2487](frontend-shared/node_modules/echarts/lib/core/echarts.js#L2487) `registerTheme('dark', darkTheme)`）+ `setTheme()` 动态切换 | **性能无收益**（实测 `setTheme` 7.93 ms vs 全量 `setOption({notMerge:true})` 7.62 ms，**慢 4%**）。收益在**代码**：50 行重复管线 → 1 处注册 + 1 行切换 | 中（要重做配色来源） |
| 6 | **axios 1.20.0** | 拦截器把**所有**错误（含取消）统一包成 `ApiError`（[client.ts:101](frontend-shared/src/api/client.ts#L101)）⇒ 下游只能用 5 路启发式猜「是不是取消」（[useHistoryData.ts:118-125](frontend-shared/src/composables/useHistoryData.ts#L118)） | 拦截器里 `if (axios.isCancel(error)) return Promise.reject(error)` 提前放行；下游改 `axios.isCancel(e)` | 8 行启发式 → 1 行；消除「靠中文/英文字符串正则判断取消」的脆弱点 | 小 |
| 7 | axios：重试/超时/去重 | 手写「失败原样重试 1 次 + 400 ms 退避」（[useHistoryData.ts:292-306](frontend-shared/src/composables/useHistoryData.ts#L292)、`waitForRetry` [:243](frontend-shared/src/composables/useHistoryData.ts#L243)）；并发去重手写在 [edgeDevice.ts:32,85-114](frontend-shared/src/stores/edgeDevice.ts#L32) | **不适用**：axios 1.20.0 **没有内建 retry**（`grep -rn retry lib/` 只命中 `parseHeaders.js` 的 `retry-after`）。AbortSignal 也已用（[:163-173](frontend-shared/src/composables/useHistoryData.ts#L163)） | 无可替换项 | — |
| 8 | pinia 4.1.0 | 18 个 store：**12 setup / 5 options**（逐文件实测）；**无 Vuex 风格代码**（`mapState/mapActions/mapGetters` 仅出现在自动生成的 `auto-imports.d.ts`）；`storeToRefs` **0 处**（setup store 直接解构即可，合理） | `getStores()`（pinia 4.1 新增，[`dist/pinia.js` 已导出](frontend-shared/node_modules/pinia/dist/pinia.js)）可收敛「7 个 store 各自 `registerSessionCacheClearer`」 | 省 7 行 import 期副作用注册 + [sessionCache.ts](frontend-shared/src/utils/sessionCache.ts)（19 行）一部分；**属可读性收益，非体积** | 小 |
| 9 | vue-router 5.4.0 | 26 条路由 **100% 懒加载**（24 内联 + 2 具名 loader，[routeLoaders.ts:1-2](frontend-shared/src/router/routeLoaders.ts#L1)）；`RouteMeta` 已扩展但 **`title`/`icon`/`hidden`/`hiddenInMenu` 4 个字段声明后无人读**（唯一被读的是 `requiresAuth`，[index.ts:229](frontend-shared/src/router/index.ts#L229)） | 要么消费这些 meta 生成菜单，要么删掉声明 | `title` 写了 **18 次**、`icon` **15 次**，与 [menuModel.ts](frontend-shared/src/views/layout/menuModel.ts) 的 14 条 `NAV_DESTINATIONS` 构成**两套真值源** | 中（门禁断言绑在 menuModel 上） |
| 10 | vue-router：滚动/预取 | **`scrollBehavior` 0 处**——滚动容器是 `.main-content`（[MainLayout.vue:1543-1549](frontend-shared/src/views/layout/MainLayout.vue#L1543) `overflow-y:auto`），不是 window；数据预取**刻意不用 `beforeResolve`**（有门禁锁死：[primaryRouteReadiness.spec.ts:6](frontend-shared/src/router/__tests__/primaryRouteReadiness.spec.ts#L6)），改用 `preloadPrimaryRoutes()`（[MainLayout.vue:961](frontend-shared/src/views/layout/MainLayout.vue#L961)）+ [prefetch.ts](frontend-shared/src/utils/prefetch.ts) | `scrollBehavior` **不适用**（只作用于 window/document）；可读性更高的做法是在已有 `afterEach`（[index.ts:241](frontend-shared/src/router/index.ts#L241)）里重置容器 `scrollTop` | 修「从滚动过的列表进详情页仍停在旧位置」；**3 行** | 小 |
| 11 | vue-virtual-scroller 3.0.5 | **只用 1 处**：[LogRealtimeViewer.vue:84-104](frontend-shared/src/components/node/LogRealtimeViewer.vue#L84)（`RecycleScroller`，`ROW_HEIGHT=28` [:152](frontend-shared/src/components/node/LogRealtimeViewer.vue#L152)），测试证明 5000 条只挂 <100 行 | 两个该用没用的长列表：`ChannelTerminal` 的 tx/rx 日志（[v-for :85/:109](frontend-shared/src/components/channel/ChannelTerminal.vue#L85)，上限 **1000→800 条**，[:322](frontend-shared/src/components/channel/ChannelTerminal.vue#L322)）、`RealtimeDataList`（[v-for :38](frontend-shared/src/components/data/RealtimeDataList.vue#L38)，上限 200） | 800 行 → 约 28 行在 DOM（`窗口高度/28`），**节点数 −96%**；200 行那条属边界收益 | 中（ChannelTerminal 有自动滚底逻辑要接） |
| 12 | vue-virtual-scroller：可替换性 | — | element-plus 自带 `ElVirtualList`（`FixedSizeList`/`DynamicSizeList`，从 [es/index.mjs:121](frontend-shared/node_modules/element-plus/es/index.mjs#L121) 导出）可顶掉整个 vvs 依赖 | 少 1 个依赖（vvs `dist` 525 KB 落盘）；**体积收益近似 0**（vvs 已被 tree-shake 进 NodeOverview chunk） | 中（要用 `RecycleScrollerExposed` 的滚动 API） |

**一句话总结**：本次唯一「删一行、首屏 −62%」的高确定性收益在 **element-plus 的 `manualChunks` 分组**（§1.1）；**echarts 的最大误解必须纠正**——它早就不是全量引入了，565 KB 是按需引入后的真实下限，没有 565→200 的空间；其余 4 个库基本健康，剩下的是**代码整洁度**而非性能。

---

## 1. element-plus 2.14.7

### 1.0 一句话现状

**按需引入做对了；chunk 分组做错了。** 组件层面几乎无技术债（废弃 API 一处都没踩），真正的钱在 `vite.config.ts` 的 `manualChunks`。

### 1.1 【最高价值】`manualChunks` 把 element-plus 全量塞进首屏 —— 实测首屏 gzip −62.6%

**现在怎么用（证据）**

[vite.config.ts:108-120](frontend-shared/vite.config.ts#L108)：

```ts
build: {
  chunkSizeWarningLimit: 1500,
  rollupOptions: {                     // ← 第 111 行
    output: {
      manualChunks(id) {               // ← 第 114 行（rolldown 已标 @deprecated）
        if (id.includes('node_modules/echarts/')) return 'echarts'    // ← 第 115 行：保留
        if (id.includes('node_modules/element-plus/')) return 'element'   // ← 第 116 行：元凶
        if (id.includes('node_modules/vue/') || ...) return 'vue'
      },
    },
  },
}
```

**机制（为什么这一行会伤首屏）**：`manualChunks` 把**所有被应用代码引用到的** EP 模块合并成**一个** `element` chunk。入口 `main.ts` 静态引用了 `element-plus/theme-chalk/dark/css-vars.css`（[main.ts:3](frontend-shared/src/main.ts#L3)），且 `App.vue` 静态 import 了 `ElConfigProvider` 与 `element-plus/es/locale/lang/zh-cn`（[App.vue:16](frontend-shared/src/App.vue#L16)）⇒ **入口 chunk 直接 import 了 element chunk** ⇒ 该 chunk 进 `index.html` 的 `modulepreload`。

**实测证据**（一次 `vite build` 的产物）：

```
$ cd frontend-shared/dist && tail -8 index.html     # 现网 dist/
<link rel="modulepreload" href="/assets/rolldown-runtime-*.js">
<link rel="modulepreload" href="/assets/element-BPCwiAPF.js">   ← 718.8 KB
<link rel="modulepreload" href="/assets/vue-*.js">
<link rel="modulepreload" href="/assets/client-*.js">
```

而 `element` chunk 里**真的**是「全站全部用到的组件」。登录页路径（`/login`）实际只需要 **9 个** EP 组件：el-button / el-input / el-form / el-form-item / el-dialog / el-alert / el-checkbox / el-link / el-config-provider（`grep -rhoE '<el-[a-z0-9-]+' views/auth/Login.vue components/forms/*.vue App.vue` 实测）。**gzip 差额 162.7 KB 就是「为这 9 个组件预付了全站 50+ 个组件的下载量」**。

**更好的方式**：删掉第 115 行（echarts 那行**保留**，见 §2.4）。

**能省多少（硬数字，本机实跑 A/B）**

| 指标 | 现状（有 element 分组） | 去掉该分组 | 变化 |
|---|---|---|---|
| 首屏 `modulepreload` JS raw | 822.9 KB（5 个文件） | **290.9 KB**（8 个文件） | **−532.0 KB（−64.6%）** |
| 首屏 JS **gzip** | **267.9 KB** | **105.2 KB** | **−162.7 KB（−60.7%）** |
| 首屏 CSS raw | 307.6 KB | **62.3 KB** | **−245.3 KB（−79.7%）** |
| 首屏 CSS **gzip** | **42.3 KB** | **10.9 KB** | **−31.4 KB（−74.2%）** |
| 首屏 JS+CSS **gzip 合计** | **310.2 KB** | **116.1 KB** | **−194.1 KB（−62.6%）** |
| `/login` 路由闭包 gzip | 272.7 KB | **144.1 KB** | −128.6 KB（−47.2%） |

会话构建产物：`.tmp-probe/build-base`（现状）、`.tmp-probe/build-noel`（只删这一行）。

> ⚠️ 注意与旧结论的口径差异：本仓 [frontend.md](docs/分析/依赖优化/frontend.md) §3.3 报的是「首屏 301.5 → 98.4 KB gzip（删掉 element 分组）」。本次在**当前代码状态**（EP 2.14.7 / vite 8.3.5，即 2026-10-23 的 `package.json`）重测得到 310.2 → 116.1 KB（含 CSS）与 267.9 → 105.2 KB（仅 JS）。**方向与结论一致，绝对数不同**，差异来自旧报告只计 JS、且当时 dist 构建产物不同。以本次双构建同口径对比为准。

**成本**：删 1 行。**风险**：首屏请求数从 5 个变 8 个（HTTP/2 下影响可忽略）；EP 样式改为随路由懒加载注入后，**首屏若有用到未加载组件会 FOUC** —— 实测首屏（登录页）用到的 9 个组件都在入口闭包里，无此风险。

### 1.2 废弃 API 审计：一处都没踩（不是"没查"，是逐条对过 EP 源码里的 `useDeprecated`）

element-plus 2.14.7 的 `es/` 里共 **8 个** `useDeprecated` 调用点，我逐条对到仓库：

| EP 废弃项（源码证据） | 仓库命中 | 判定 |
|---|---|---|
| `type="text"` → `link`（[use-button.mjs:9-15](frontend-shared/node_modules/element-plus/es/components/button/src/use-button.mjs#L9)） | `grep -rn 'type="text"' src` = **0** | ✅ 未踩。注意仓库大量用的 `<el-button text ...>`（12 文件 32 处）是 **`text` 布尔 prop**（[button.mjs:77](frontend-shared/node_modules/element-plus/es/components/button/src/button.mjs#L77)），**未废弃**，别误改 |
| `el-radio` 的 `label` 当 value（[use-radio.mjs:32-33](frontend-shared/node_modules/element-plus/es/components/radio/src/use-radio.mjs#L32)） | 仓库所有 `<el-radio-button>` 都用 `value=` | ✅ 未踩 |
| `el-checkbox` 的 `label` 当 value（[use-checkbox.mjs:38-44](frontend-shared/node_modules/element-plus/es/components/checkbox/src/composables/use-checkbox.mjs#L38)，条件是 `isGroup && isPropAbsent(value)`） | [BmsChartsPanel.vue:27](frontend-shared/src/views/edge-device/bms/BmsChartsPanel.vue#L27)/[:52](frontend-shared/src/views/edge-device/bms/BmsChartsPanel.vue#L52) 同时给了 `:value` 与 `:label` ⇒ `isPropAbsent(value)` 为 false ⇒ **不告警** | ✅ 未踩（**但这是靠 coincidence 过的**：:label 此处是展示文案，恰好也没丢 :value —— 建议加注释钉住，否则后人删掉 :value 就静默变成"值=#1"的废弃语义） |
| `true-label`/`false-label`（[use-checkbox.mjs:45-51](frontend-shared/node_modules/element-plus/es/components/checkbox/src/composables/use-checkbox.mjs#L45)） | 0 处 | ✅ |
| dialog/drawer 的 `#title` → `#header`（[dialog…mjs:26-32](frontend-shared/node_modules/element-plus/es/components/dialog/src/dialog.vue_vue_type_script_setup_true_lang.mjs#L26)、[drawer…mjs:31-37](frontend-shared/node_modules/element-plus/es/components/drawer/src/drawer.vue_vue_type_script_setup_true_lang.mjs#L31)） | 脚本扫 101 个 `.vue`：`#title` 出现在 el-alert / el-sub-menu / el-collapse-item 内，**在 el-dialog/el-drawer 上 0 处** | ✅ 未踩 |
| `el-link` 的 `underline` 布尔值（[`link.vue…mjs:19-25`](frontend-shared/node_modules/element-plus/es/components/link/src/link.vue_vue_type_script_setup_true_lang.mjs#L19)，仅**布尔**才告警） | [LoginForm.vue:60](frontend-shared/src/components/forms/LoginForm.vue#L60)、[MainLayout.vue:189](frontend-shared/src/views/layout/MainLayout.vue#L189) 都用 `underline="never"`（字符串，合法值域见 [link.mjs:26-36](frontend-shared/node_modules/element-plus/es/components/link/src/link.mjs#L26)） | ✅ 未踩 |
| `el-pagination` 的 `small`（[pagination.mjs](frontend-shared/node_modules/element-plus/es/components/pagination/src/pagination.mjs)） | 0 处（14 处 `<el-pagination>` 都没写 `small`） | ✅ |
| `v-infinite-scroll` 指令 | 0 处 | ✅ |

**结论**：**这一项没有可还的债**。唯一值得加固的是 §1.2 表格第 3 行那个「靠 coincidence 过的」checkpoint —— 属于防御性注释，不是修复。

### 1.3 新组件盘点：只有 `el-segmented` 有真实落点，`el-watermark`/`el-tour`/`el-anchor` 都不适用

EP 2.14.7 已装但仓库 0 处使用的新组件（`ls es/components` + `grep` 双向核实）：`watermark`(2.4.0)、`tour`(2.5.0)、`anchor`(2.6.0)、`segmented`(2.7.0)、`splitter`(2.10.0)、`table-v2`(2.2.0)、`virtual-list`、`statistic`(2.2.30)、`input-tag`(2.9.0)、`mention`(2.8.0)、`input-otp`(2.14.0)。版本号取自 EP 官方 [组件总览页](https://element-plus.org/en-US/component/overview) 的角标。

| 组件 | 仓库有落点吗 | 证据 / 裁决 |
|---|---|---|
| **`el-segmented` 2.7.0** | **有**，6 个文件 23 处 `<el-radio-button>` | 候选：[TimeRangeSelector.vue:3-7](frontend-shared/src/components/charts/TimeRangeSelector.vue#L3)（1h/24h/7d/custom，最典型的分段控制器）、[RealtimeDataList.vue:7-8](frontend-shared/src/components/data/RealtimeDataList.vue#L7)（明文/16进制）、[GPIOResourceList.vue:26-28](frontend-shared/src/components/periph/GPIOResourceList.vue#L26)（全部/已配置/未配置）、[ChannelTerminal.vue:44-45](frontend-shared/src/components/channel/ChannelTerminal.vue#L44)、[ChannelManager.vue:76-202](frontend-shared/src/components/channel/ChannelManager.vue#L76)（8 组）、[MobileRedesignDemo.vue:40](frontend-shared/src/dev/MobileRedesignDemo.vue#L40)。**收益量化**：模板 23 行→约 6–8 行；CSS 侧 7 个文件里 21 行 `:deep(.el-radio-button__inner)` 相关规则（其中 [TimeRangeSelector.vue:91-171](frontend-shared/src/components/charts/TimeRangeSelector.vue#L91) 独占 9 条规则 / 95 行）全部改名/可删。**但注意**：TimeRangeSelector 那 95 行 CSS 有一半是**44px 触控目标与对比度档位**（注释明确写了 0,1,0 特异性推导），换组件后这些规则要重写而非删除 ⇒ **净减行数远小于 95，实际约 −40 行**，且触控/对比度门禁需重跑。**裁决：可以做，但排在 §1.1 之后，且必须带视觉回归。** |
| `el-watermark` 2.4.0 | **不适用** | 仓库唯一的"水印"是 [BmsDemoPage.vue:6](frontend-shared/src/dev/BmsDemoPage.vue#L6) 的 `dev-watermark` —— 它是一个**底部常驻说明条**（[:774-775](frontend-shared/src/dev/BmsDemoPage.vue#L774) `position: fixed` 的 CSS 块），**不是图纸水印**，且 [BmsDemoGating.spec.ts:21-22](frontend-shared/src/dev/__tests__/BmsDemoGating.spec.ts#L21) 用文本内容断言它。业务侧无"防截图/防外泄"需求 ⇒ 用 `el-watermark` 是**为用而用**。不适用。 |
| `el-tour` 2.5.0 | **不适用** | 无新手引导/分步漫游需求。现有"引导"全是**空态文案**（如 [LogicalDeviceCandidateSelect.vue:123](frontend-shared/src/components/device/LogicalDeviceCandidateSelect.vue#L123) "type 未就绪显示引导"），与 tour 的"锚定元素高亮+分步"不是同一件事。 |
| `el-anchor` 2.6.0 | **不适用** | 无长文档/页内目录跳转页。注意别被 grep 里的 "锚点" 误导：`grep 锚点` 命中的是 `watermark_id` 字段、`overflow-anchor` CSS 属性、测试里的"锚点"= 断言基准行，**没有一个是目录锚点**。 |
| `el-table-v2` 2.2.0（虚拟化表格） | **不建议** | 见 §6.3：仓库 19 张 `el-table` 全部**服务端分页**（最多 100 行/页），没有一张是万行表。EP 文档自己把 table-v2 标为 beta（"该组件仍在测试中，生产环境使用可能有风险"，见 [官方 table-v2 页](https://element-plus.org/en-US/component/table-v2)），为 0 收益引入 beta 组件不值。**不适用（原因：无大数据量表格）** |
| `el-splitter` 2.10.0 | 不适用 | 无拖拽分栏需求；`resize` 相关 20 处全是 `ResizeObserver` 驱动 echarts `resize()`（如 [LineChart.vue:693-694](frontend-shared/src/components/charts/LineChart.vue#L693)），与分栏无关。 |
| `el-statistic` 2.2.30 | **部分落点，但不建议** | 自研 [CountUp.vue](frontend-shared/src/components/common/CountUp.vue)（125 行）做数字滚动动画。EP 的 `el-statistic` **只做格式化不做动画**（源码 [statistic.vue…mjs:13](frontend-shared/node_modules/element-plus/es/components/statistic/src/statistic.vue_vue_type_script_setup_true_lang.mjs#L13) 里 `displayValue` 是纯 `computed`，无 rAF）。替换会**丢掉动画**⇒ 不是优化是退化。**不适用（原因：功能不等价）** |

### 1.4 主题 / CSS 变量 / 暗色机制：用了标准机制，但**覆盖层过厚**（130 行 `html.dark .el-*`）

**现在怎么用（证据）**

- **标准做法已用**：[main.ts:3](frontend-shared/src/main.ts#L3) `import 'element-plus/theme-chalk/dark/css-vars.css'` —— 这正是 EP 官方暗色入口。切主题靠给 `<html>` 加 `.dark` 类：[theme.ts:20](frontend-shared/src/stores/theme.ts#L20) `html.classList.add('dark')`，同时设 `data-theme`（[:14](frontend-shared/src/stores/theme.ts#L14)）。
- **EP 变量桥接**：[theme.css:500-535](frontend-shared/src/styles/theme.css#L500) 起把 216 个 `--el-*` 变量映射到项目 token，含完整的 `--el-color-primary-light-3/5/7/8/9` 调色阶（[theme.css:263-272](frontend-shared/src/styles/theme.css#L263) 亮色 + [:535](frontend-shared/src/styles/theme.css#L535) 暗色）。
- **`el-config-provider`**：[App.vue:2](frontend-shared/src/App.vue#L2) 只传了 `:locale`，**没传 `namespace`/`size`/`z-index`** —— 这是绝大多数项目的正常用法，无问题。

**问题（值得改的）**

1. **暗色覆盖有 130 行是"按组件重新赋变量"**：[theme.css:492-790](frontend-shared/src/styles/theme.css#L492) 区间内含 **37 条** `html.dark .el-<component>` 规则（`grep -c 'html.dark .el-' theme.css` = 37），加上 [:446](frontend-shared/src/styles/theme.css#L446) 的"暗色安全网"用 **`!important`** 钉住 9 个变量（[:477-490](frontend-shared/src/styles/theme.css#L477)，全文 `!important` 共 **64 处**）。
   - **根因在文档里已写透**（[theme.css:446-476](frontend-shared/src/styles/theme.css#L446)）：EP 的 `dark/css-vars.css` 把变量定义在 `html.dark`（特异性 0,1,1），高于项目 `:root`（0,1,0）⇒ 项目桥接被静默架空。我**实测确认了这段描述**：`element-plus/theme-chalk/dark/css-vars.css` 全文只有 **1 行**、**82 个变量**，选择器就是 `html.dark`（见下）。
   - **更省的方式**：项目桥接块改成与 EP **同特异性**的 `html.dark` 且**排在 EP 之后**即可，不需要 `!important`；`!important` 只在 dev 顺序相反时才必要。但这属"能改但不该轻易改"——现网的 64 处 `!important` 是**已被验证过的生产行为**，改动需要完整的暗色对比度回归。**建议：不动，只补注释指路**（现有注释已经写得很到位）。
2. **`MASK` 类覆盖率问题已修过**，不再赘述（[theme.css:446](frontend-shared/src/styles/theme.css#L446) 的长注释就是那次事故的记录）。

**EP 侧的"最新方式"核查（诚实标注）**：EP 2.14.7 的 `dark/css-vars.css` 里 **`color-mix()` 出现 0 次**（`grep -c` 实测）—— 它仍是旧的"预生成 light-3/5/7/8/9 十六进制阶"。也就是说**EP 没有更现代的 CSS 变量生成方式可用**，仓库手写 12 个 `--el-color-*-light-N`（[theme.css:263-272](frontend-shared/src/styles/theme.css#L263)）是**跟随 EP 的既有形态**，不是落后。**这一项没有"新方式"可换。**

---

## 2. echarts 6.1.0　【重点】

### 2.1 先回答最关键的误解：**仓库不是全量引入，早就是按需引入了**

`grep -rn "from 'echarts'" frontend-shared/src` → **0 命中**。逐条清单（`grep -rn "from 'echarts"` 全量，含 dev）：

| # | 文件:行 | import 语句 | 生产环境是否入包 |
|---|---|---|---|
| 1 | [components/charts/LineChart.vue:170](frontend-shared/src/components/charts/LineChart.vue#L170) | `import * as echarts from 'echarts/core'` | ✅ 入包 |
| 2 | [components/charts/LineChart.vue:171](frontend-shared/src/components/charts/LineChart.vue#L171) | `import { CanvasRenderer } from 'echarts/renderers'` | ✅ |
| 3 | [components/charts/LineChart.vue:172](frontend-shared/src/components/charts/LineChart.vue#L172) | `import { LineChart as LineChartSeries } from 'echarts/charts'` | ✅ |
| 4 | [components/charts/LineChart.vue:173-178](frontend-shared/src/components/charts/LineChart.vue#L173) | `import { TitleComponent, TooltipComponent, LegendComponent, GridComponent } from 'echarts/components'` | ✅ |
| 5 | [components/charts/LineChart.vue:179](frontend-shared/src/components/charts/LineChart.vue#L179) | `import type { EChartsCoreOption } from 'echarts/core'`（纯类型，不入包） | — |
| 6 | [views/edge-device/bms/BmsCellVoltageChart.vue:7](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L7) | `import * as echarts from 'echarts/core'` | ✅ 入包 |
| 7 | [BmsCellVoltageChart.vue:8](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L8) | `{ CanvasRenderer } from 'echarts/renderers'` | ✅ |
| 8 | [BmsCellVoltageChart.vue:9](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L9) | `{ BarChart as BarChartSeries } from 'echarts/charts'` | ✅ |
| 9 | [BmsCellVoltageChart.vue:10](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L10) | `{ GridComponent, TooltipComponent, TitleComponent, MarkLineComponent } from 'echarts/components'` | ✅ |
| 10 | [BmsCellVoltageChart.vue:14](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L14) | `{ LabelLayout } from 'echarts/features'` | ✅ |
| 11 | [dev/BmsDemoPage.vue:396](frontend-shared/src/dev/BmsDemoPage.vue#L396) | `import * as echarts from 'echarts/core'` | ❌ DEV 路由门禁 |
| 12 | [dev/BmsDemoPage.vue:397](frontend-shared/src/dev/BmsDemoPage.vue#L397) | `{ CanvasRenderer } from 'echarts/renderers'` | ❌ |
| 13 | [dev/BmsDemoPage.vue:398](frontend-shared/src/dev/BmsDemoPage.vue#L398) | `{ LineChart as LineChartSeries } from 'echarts/charts'` | ❌ |
| 14 | [dev/BmsDemoPage.vue:399](frontend-shared/src/dev/BmsDemoPage.vue#L399) | `{ TooltipComponent, LegendComponent, GridComponent } from 'echarts/components'` | ❌ |
| 15 | [dev/mobile-redesign/charts/CellSparkChart.vue:21](frontend-shared/src/dev/mobile-redesign/charts/CellSparkChart.vue#L21) | `echarts/core` | ❌ |
| 16 | [.../CellSparkChart.vue:22](frontend-shared/src/dev/mobile-redesign/charts/CellSparkChart.vue#L22) | `CanvasRenderer` | ❌ |
| 17 | [.../CellSparkChart.vue:23](frontend-shared/src/dev/mobile-redesign/charts/CellSparkChart.vue#L23) | `BarChart` | ❌ |
| 18 | [.../CellSparkChart.vue:24](frontend-shared/src/dev/mobile-redesign/charts/CellSparkChart.vue#L24) | `GridComponent, TooltipComponent, MarkLineComponent` | ❌ |
| 19 | [dev/mobile-redesign/charts/PollIntervalChart.vue:19](frontend-shared/src/dev/mobile-redesign/charts/PollIntervalChart.vue#L19) | `echarts/core` | ❌ |
| 20 | [.../PollIntervalChart.vue:20](frontend-shared/src/dev/mobile-redesign/charts/PollIntervalChart.vue#L20) | `CanvasRenderer` | ❌ |
| 21 | [.../PollIntervalChart.vue:21](frontend-shared/src/dev/mobile-redesign/charts/PollIntervalChart.vue#L21) | `BarChart` | ❌ |
| 22 | [.../PollIntervalChart.vue:22](frontend-shared/src/dev/mobile-redesign/charts/PollIntervalChart.vue#L22) | `GridComponent, TooltipComponent` | ❌ |
| 23 | [dev/mobile-redesign/charts/ProtectionHistoryChart.vue:48](frontend-shared/src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue#L48) | `echarts/core` | ❌ |
| 24 | [.../ProtectionHistoryChart.vue:49](frontend-shared/src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue#L49) | `CanvasRenderer` | ❌ |
| 25 | [.../ProtectionHistoryChart.vue:50](frontend-shared/src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue#L50) | `BarChart` | ❌ |
| 26 | [.../ProtectionHistoryChart.vue:51](frontend-shared/src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue#L51) | `GridComponent, TooltipComponent` | ❌ |
| 27 | [dev/mobile-redesign/charts/TempScaleChart.vue:18](frontend-shared/src/dev/mobile-redesign/charts/TempScaleChart.vue#L18) | `echarts/core` | ❌ |
| 28 | [.../TempScaleChart.vue:19](frontend-shared/src/dev/mobile-redesign/charts/TempScaleChart.vue#L19) | `CanvasRenderer` | ❌ |
| 29 | [.../TempScaleChart.vue:20](frontend-shared/src/dev/mobile-redesign/charts/TempScaleChart.vue#L20) | `BarChart` | ❌ |
| 30 | [.../TempScaleChart.vue:21](frontend-shared/src/dev/mobile-redesign/charts/TempScaleChart.vue#L21) | `GridComponent, TooltipComponent, MarkLineComponent` | ❌ |
| 31 | [dev/mobile-redesign/charts/TrendChart.vue:26](frontend-shared/src/dev/mobile-redesign/charts/TrendChart.vue#L26) | `echarts/core` | ❌ |
| 32 | [.../TrendChart.vue:27](frontend-shared/src/dev/mobile-redesign/charts/TrendChart.vue#L27) | `CanvasRenderer` | ❌ |
| 33 | [.../TrendChart.vue:28](frontend-shared/src/dev/mobile-redesign/charts/TrendChart.vue#L28) | `LineChart` | ❌ |
| 34 | [.../TrendChart.vue:29](frontend-shared/src/dev/mobile-redesign/charts/TrendChart.vue#L29) | `GridComponent, TooltipComponent, LegendComponent` | ❌ |

**DEV 门禁已实测生效**：`ls dist/assets | grep -icE 'mobileredesign|bmsdemo|mockbms|mockpowerflow'` = **0**。dev 那 6 个文件的 import **完全不进生产包**（路由 [router/index.ts:172-197](frontend-shared/src/router/index.ts#L172) 用 `import.meta.env.DEV` 三元静态消除）。

⇒ **生产环境 echarts import 点只有 2 个文件、10 条语句**，全部是 `echarts/core` + 子路径的按需形态。

### 2.2 那么 565,524 字节的 chunk 是什么？—— 它就是「按需引入后的真实下限」

**它不在首屏**。实测 `dist/index.html` 的 `modulepreload` 列表只有 `rolldown-runtime / element / vue / client`；`grep 'from"./echarts-'` 命中者**只有 2 个 chunk**：`LineChart-*.js` 与 `BmsDetailPage-*.js`。也就是说 echarts 只在**渲染图表的页面**（`/data`、`/edge-device/:id` 的 BMS/逆变器）才下载。

**体积是怎么构成的？我用探针逐模块做减法（`vite build` 实跑，minify=oxc）**

| 探针组合 | raw | gzip | 增量（相对上一行） |
|---|---|---|---|
| A. `core` + `CanvasRenderer` + `LineChart` + `GridComponent` | 467.12 kB | 158.38 kB | 基线（echarts 内核 + 坐标系） |
| B. A + `TooltipComponent` | 492.10 kB | 166.46 kB | **+24.98 kB / +8.08 kB** |
| C. B + `LegendComponent` | 510.01 kB | 172.06 kB | **+17.91 kB / +5.60 kB** |
| D. C + `TitleComponent` | 512.39 kB | 172.68 kB | **+2.38 kB / +0.62 kB** |
| E. D + `BarChart` + `MarkLineComponent` | 552.87 kB | 186.10 kB | **+40.48 kB / +13.42 kB** |
| **F. E + `LabelLayout`（= 仓库生产侧全部需要的并集）** | **564.88 kB** | **190.61 kB** | **+12.01 kB / +4.51 kB** |
| 〔对照〕全量 `import * as echarts from 'echarts'` | **1,115.94 kB** | **370.16 kB** | — |

**F 行 564.88 kB vs 实产 chunk 565,524 字节（=552.3 KiB=565.5 kB）—— 差 0.1%。** 这证明：**这个 chunk 恰好就是「仓库 import 的模块并集」编译产物，里面没有任何多余的全量引入。** 探针产物在 `.tmp-probe/ec-probe/out-*`。

**所以：按需引入能省多少？**

- **已经省掉了**：550.06 kB raw / **179.55 kB gzip**（1,115.94 − 564.88；370.16 − 190.61）。也就是"如果当年写成全量，现在会多付 1.97 倍"。
- **还能再省多少**：把 Legend（17.91 kB）+ Title（2.38 kB）也去掉 ⇒ 最多 **20.29 kB raw / 6.22 kB gzip**，但**图例和标题是 LineChart 的功能**（[LineChart.vue:536](frontend-shared/src/components/charts/LineChart.vue#L536) `title`、[:607-614](frontend-shared/src/components/charts/LineChart.vue#L607) legend），删了就是功能退化。**结论：没有 565→200 的空间，别按这个预期排期。**

**SVG vs Canvas 渲染器（有人会问"换 SVG 会不会更小"）**：实测 `SVGRenderer` 版本 474.68 kB / 162.09 kB vs `CanvasRenderer` 467.12 kB / 158.38 kB ⇒ **SVG 反而大 7.56 kB**。且本仓是 1 帧/秒实时曲线（[BmsDetailPage.vue:762](frontend-shared/src/views/edge-device/bms/BmsDetailPage.vue#L762) 实时追加），Canvas 才是对的选择。**渲染器无需切换。**（SVG 的运行时渲染耗时本次未测，不编造。）

### 2.3 ECharts 6 提供了什么更好的方式 —— 逐条对到本仓

来源：[ECharts 6 Features](https://echarts.apache.org/handbook/en/basics/release-note/v6-feature/)、[v5→v6 Upgrade Guide](https://echarts.apache.org/handbook/en/basics/release-note/v6-upgrade-guide/)、本机 `node_modules/echarts` 源码。

| ECharts 6 特性 | 本仓现状 | 裁决 |
|---|---|---|
| **① 全新默认主题**（design token 重构，图例默认位置改到画布**底部**） | **已经踩过并已修**：[LineChart.vue:607-614](frontend-shared/src/components/charts/LineChart.vue#L607) 的注释记录「ECharts 6.1.0 的 legend 默认值里带 `bottom: tokens.size.m`，只给 `top` 时图例被画到画布外（文字 bbox y≈-9）」，故显式写 `bottom: 'auto'`。**我核对了源码坐实**：[LegendModel.js:250](frontend-shared/node_modules/echarts/lib/component/legend/LegendModel.js#L250) `bottom: tokens.size.m`。 | ✅ 已消化，**无需动作**（这条注释极有价值，别删） |
| ② 保留 v5 配色：`echarts/theme/v5.js` + `echarts.init(el, 'v5')` | 仓库**不用** v5 主题，而是把颜色全量映射到**自家 CSS 变量**（[LineChart.vue:251-266](frontend-shared/src/components/charts/LineChart.vue#L251) `cssToken('--color-primary')` 等 8 个系列色 + 5 个语义色）。`ls node_modules/echarts/theme/` 确认 v5.js 存在 | **不需要**：走 CSS 变量比套 v5 主题更贴合本仓的亮/暗 token 体系 |
| **③ 动态换主题 `chart.setTheme()`** | 仓库用「读 CSS 变量 → 拼 option → `setOption({notMerge:true})`」重画整图，且**每个图表组件各写一遍** `MutationObserver` 监听 `class`/`data-theme`（[LineChart.vue:695-696](frontend-shared/src/components/charts/LineChart.vue#L695)、[BmsCellVoltageChart.vue:236](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L236)、5 个 dev 图表）。**共 7 个文件、50 行重复管线** | ⚠️ **收益是代码而非性能**。我用 SSR 模式跑了对照（300 点 × 4 系列、SVG renderer，`.tmp-probe/ec-probe/bench-theme.mjs`）：`setOption(full, notMerge:true)` = **7.62 ms**，`setTheme()` = **7.93 ms**（**还慢 4%**），`setOption(merge)` = 6.85 ms。⇒ **"换 setTheme 能提速"是假的**；能省的是 50 行重复管线 + 7 处 MutationObserver（换成 1 处 `registerTheme` + 1 行 `setTheme`），**约 −45 行、−6 个 observer**。成本：要把 216 个 CSS 变量里的图表色域抽成一份 theme 对象。**建议：低优先，别为性能做** |
| **④ 暗色模式支持（内置 `dark` 主题）** | 仓库未用。**实测 ECharts 6.1.0 已内置注册**：[lib/core/echarts.js:2487](frontend-shared/node_modules/echarts/lib/core/echarts.js#L2487) `registerTheme('dark', darkTheme)`，所以 `echarts.init(el, 'dark')` / `setTheme('dark')` **开箱可用，零额外体积** | 与 ③ 同一动作，合并评估 |
| ⑤ 新图表类型：Chord / Beeswarm / Scatter Jitter / 断轴 Broken Axis / 矩阵坐标系 Matrix / 增强 custom series | 仓库图表类型只有 **Line + Bar**（[LineChart.vue:172](frontend-shared/src/components/charts/LineChart.vue#L172)、[BmsCellVoltageChart.vue:9](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L9)） | **不适用**（无对应业务场景）。唯一**可能**有场景的是**断轴**（BMS 单体电压 3.18V 与总电压 50.5V 量级差 16 倍）——但仓库已用「多 Y 轴 + 量级比 ≥10 拆轴」解决（[LineChart.vue:279](frontend-shared/src/components/charts/LineChart.vue#L279)/[:322-325](frontend-shared/src/components/charts/LineChart.vue#L322)），断轴是**同轴内**折中，属可选增强非替代 |
| ⑥ 树摇优化 | **机制未变**：`echarts@6.1.0/package.json` 的 `sideEffects` 仍把 `lib/chart/*.js`、`lib/component/*.js` 标为有副作用 ⇒ 必须显式 `echarts.use([...])` 才入包，仓库已这么做（8 处 `echarts.use`）。**ECharts 6 没有"更激进的树摇"这回事** | 已最优 |
| ⑦ 轴标签防溢出/防重叠**默认开启**（`grid.outerBoundsMode`、`nameMoveOverlap`） | 仓库显式 `containLabel: false` + 像素级边距（[LineChart.vue:660-670](frontend-shared/src/components/charts/LineChart.vue#L660)），目的是"跨分组绘图区严格等宽" | ⚠️ **注意**：v6 默认开启防溢出，可能让仓库精心算的像素边距产生 ±px 偏移。因已 `containLabel:false` 且实测对齐门禁（`ChartPlotAlignment.spec`）全绿，**当前无回归**；但升级小版本时这条要进回归清单 |
| ⑧ `rich` 富文本继承 `label` 样式（v6 行为变更，`richInheritPlainLabel`） | `grep -rn "rich:" src` = **0** | **不适用** |

### 2.4 附带发现：`manualChunks` 里 echarts 那行**保留**（有实测依据，别一起删）

我另建了一份"只删 echarts 分组、保留 element 分组"的构建（`.tmp-probe/build-noec`）做对照：

| 指标 | BASE（echarts 独立 chunk） | NOEC（不分组） |
|---|---|---|
| `/data`（DataPanel）路由闭包 gzip | **470.4 KB** | 454.4 KB |
| `/edge-device/:id`（BmsDetailPage）闭包 gzip | 497.1 KB | 498.2 KB |
| LineChart 闭包 gzip | 455.3 KB | **439.3 KB** |
| 最大 chunk | `echarts` 552.3 KB | `LineChart` 512.7 KB |

**结论**：删掉 echarts 分组能让"只画一条线的页面"少 16 KB，但在 BmsDetailPage（Line+Bar 都用）上**反而多 1.1 KB**，且失去"552 KB 巨库独立可缓存"的好处（图表页之间跳转命中同一 chunk）。**裁决：保留 echarts 分组，删 element 分组**（§1.1）。这两行的处置**不同**，不能一起删。

### 2.5 一处可低风险收敛：8 处 `echarts.use()` 合并为 1 个模块

现状：8 个文件各自 `echarts.use([...])`（[LineChart.vue:182](frontend-shared/src/components/charts/LineChart.vue#L182)、[BmsCellVoltageChart.vue:18](frontend-shared/src/views/edge-device/bms/BmsCellVoltageChart.vue#L18) + 6 处 dev）。`echarts.use` 是**幂等**的（重复注册不报错），所以目前**不是 bug**；但存在漂移风险（例如 dev 的 TrendChart 注册了 `LegendComponent` 而 BmsCellVoltageChart 没有，各自能跑）。

**建议**：新建 `src/utils/echarts.ts` 统一 `echarts.use([...])` 并 re-export，8 处改成 `import echarts from '@/utils/echarts'`。**体积收益 0（模块并集不变）**，收益是「注册集合唯一真值源」，**约 −14 行**。低优先。

---

## 3. axios 1.20.0

### 3.1 现在怎么用（证据）

全部集中在 [api/client.ts](frontend-shared/src/api/client.ts)（105 行，全站唯一 axios 实例）：

- **实例**：[:44-49](frontend-shared/src/api/client.ts#L44) `axios.create({ baseURL, timeout: 10000 })`，刻意不设 `Content-Type`（FormData 需要浏览器生成 boundary）。
- **请求拦截器**：[:52-63](frontend-shared/src/api/client.ts#L52)，从 `localStorage`/`sessionStorage` 取 token 写 `Authorization`。
- **响应拦截器**：[:66-103](frontend-shared/src/api/client.ts#L66)。成功分支把业务码 `>=400` 转 `ApiError`（[:70-72](frontend-shared/src/api/client.ts#L70)）；失败分支做 401 清会话+硬跳转（[:79-99](frontend-shared/src/api/client.ts#L79)），**其余一律** `new ApiError(...)`（[:101](frontend-shared/src/api/client.ts#L101)）。

### 3.2 发现 1：取消错误被吞进 `ApiError`，下游被迫用 5 路启发式猜

**问题链**：[:101](frontend-shared/src/api/client.ts#L101) 对**所有** `AxiosError` 无差别包装 ⇒ `axios.isCancel()` 与 `ERR_CANCELED`/`CanceledError` 的身份信息全部丢失 ⇒ [useHistoryData.ts:118-125](frontend-shared/src/composables/useHistoryData.ts#L118) 只能写成：

```ts
function isCanceled(e, signal) {
  if (signal?.aborted) return true                                     // 启发式 1
  if (anyErr?.code === 'ERR_CANCELED') return true                     // 启发式 2
  if (anyErr?.name === 'CanceledError' || name === 'AbortError') ...   // 启发式 3
  return /cancel(?:l)?ed|aborted/i.test(String(message))               // 启发式 4：正则匹配中英文
}
```

第 4 路最脆弱：**服务端若返回含 "cancelled" 字样的业务错误，会被误判为"用户取消"从而静默吞掉**（[useHistoryData.ts:313](frontend-shared/src/composables/useHistoryData.ts#L313) 取消分支直接 `return`，不报错不提示）。

**axios 1.20.0 提供了什么**：`axios.isCancel()` 与 `CanceledError` 类都从包根导出（`grep -c CanceledError dist/axios.js` = 19 处，`isCancel` 4 处；`lib/adapters/xhr.js:253-256` 是浏览器侧 abort 的落点）。

**更好的方式（2 行）**：

```ts
// client.ts 响应拦截器错误分支开头
if (axios.isCancel(error)) return Promise.reject(error)   // 保持原始身份，不包 ApiError
```

下游 `isCanceled` 从 8 行压到 `axios.isCancel(e) || signal?.aborted` **1 行**。

**能省多少**：`useHistoryData.ts` 55 行 → 约 48 行（**−7**）；更重要的是**消灭一类静默失败**（业务错误含 cancelled 字样时被吞）。**注意**：这不只是"省行数"——它是「错误身份保真」的正确性问题。**风险**：需确认所有依赖 `ApiError` 类型判断的调用方（`error instanceof ApiError` 共 19 个文件 104 处 `client.get<unknown, …>` 双泛型调用）在取消路径上不会 NPE。**建议先在 `useHistoryData` 单测里加一条断言。**

### 3.3 发现 2：手写重试/超时 —— **axios 没有内建 retry，无可替换项（诚实标注）**

先排除误解：`grep -rn retry node_modules/axios/lib/` **只命中 `lib/helpers/parseHeaders.js:23` 的 `'retry-after'` 响应头名字**。axios 1.20.0 **不提供自动重试**（无 `retry` 配置项，无退避策略）。ECharts/axios 官方文档对 `axios-retry` 的建议也是"另装插件"。

所以仓库 [useHistoryData.ts](frontend-shared/src/composables/useHistoryData.ts) 里这套手写逻辑**已经是合理实现**，且质量高于一般手写：

| 手写点 | 位置 | 是否值得换成库 |
|---|---|---|
| 失败原样重试 1 次 | [:292-306](frontend-shared/src/composables/useHistoryData.ts#L292)（`waitForRetry` 在 [:243-260](frontend-shared/src/composables/useHistoryData.ts#L243)） | ❌ 不换。它带 `signal` 感知（abort 时立即返回，不白等 400 ms），`axios-retry` 没有这个语义 |
| 逐测点降级（并发 ≤2） | [:212-240](frontend-shared/src/composables/useHistoryData.ts#L212) | ❌ 不换，业务特有 |
| 请求序号护栏（只让最新请求写状态） | [:271-283](frontend-shared/src/composables/useHistoryData.ts#L271) | ❌ 不换，注释里记录了真实竞态（刷新时旧请求 finally 熄灭新请求 loading） |
| **全站是否有"统一的超时/重试"可复用** | 分散在 6 处：[:90](frontend-shared/src/composables/useHistoryData.ts#L90) `timeoutMs=10000`、[edgeDevice.ts:388](frontend-shared/src/api/edgeDevice.ts#L388) `20000`、[logicalDevice.ts:114](frontend-shared/src/api/logicalDevice.ts#L114) 逐请求覆盖、[nodeDeviceOp.ts:63/82](frontend-shared/src/api/nodeDeviceOp.ts#L63)、[DataPanel.vue:913](frontend-shared/src/views/data/DataPanel.vue#L913) `8000`、[LogicalDeviceList.vue:411](frontend-shared/src/views/logical-device/LogicalDeviceList.vue#L411) | ⚠️ **这是唯一真正可收敛的点**：6 处魔法数字无统一出处。但每处都有实测依据注释（如 [LogicalDeviceList.vue:355](frontend-shared/src/views/logical-device/LogicalDeviceList.vue#L355) "生产实测该接口 42.4s"），**收敛反而会丢失上下文 ⇒ 建议不收敛，仅集中导出常量** |

### 3.4 发现 3：AbortController 用得对，且比多数项目细致

- `AbortSignal` 已透传到 axios（[useHistoryData.ts:172](frontend-shared/src/composables/useHistoryData.ts#L172) `signal` 直接放进 config），axios 1.20 的 xhr adapter 会挂 `abort` 监听（`lib/adapters/xhr.js:253-256`），另有 [composeSignals.js](frontend-shared/node_modules/axios/lib/helpers/composeSignals.js) 支持多 signal 合并。
- `AbortSignal.timeout(4000)` 已用（[websocket.ts:163](frontend-shared/src/stores/websocket.ts#L163)）—— 用于 WS 会话探测器，**刻意不用 axios**（避免触发 401 拦截器的硬跳转），这个设计是对的。
- **并发去重手写在 [edgeDevice.ts:32](frontend-shared/src/stores/edgeDevice.ts#L32)**（`inFlight: Map<string, {promise, sequence}>`，[:85-114](frontend-shared/src/stores/edgeDevice.ts#L85)）：axios **无内建去重**，手写合理且带 sequence 防陈旧回写。**无可替换项。**

### 3.5 axios 1.20.0 自身的新能力对本仓有用吗？（逐条核实）

参考 [v1.20.0 release notes](https://github.com/axios/axios/releases/tag/v1.20.0)（2026-08-19，从 GitHub releases atom feed 取原文）：

| 1.20.0 变更 | 对本仓 |
|---|---|
| 新增 `ContentTooLarge`(413) / `UnprocessableContent`(422) 状态码别名 | 不适用（仓库不按状态码枚举分支，只用 `status === 401` 与业务 `code`） |
| 修复 XHR：**导航取消的请求现在 reject `ECONNABORTED` 而非 resolve status 0** | ⚠️ **相关但无回归**：仓库 `isCanceled` 第 2 路认 `ERR_CANCELED` 不认 `ECONNABORTED`；但**导航取消**伴随页面卸载，无下游处理。若做 §3.2 改造，`isCancel` 能一并覆盖 |
| 拦截器生命周期：修剪被 eject 的 handler，防数组无限增长 | **本仓有实际收益**：`interceptors.response.use` 在 [client.ts:66](frontend-shared/src/api/client.ts#L66) 是模块级单次调用，不涉及 eject ⇒ 无收益也不受害 |
| 运行时选项原型污染加固（`#11141`） | 安全收益，自动获得 |
| 修复 timeoutErrorMessage 合并策略 | 无感 |

**结论：axios 这一项没有"新版能力带来大改造"的空间。** 唯一建议是 §3.2 那 2 行（错误身份保真）。

---

## 4. pinia 4.1.0

### 4.1 现在怎么用（逐文件实测，不是抽样）

`cd src/stores && for f in *.ts` 判定 `defineStore('x', {…})`（options）vs `defineStore('x', () => …)`（setup）：

| 风格 | 数量 | 文件 |
|---|---|---|
| **setup store** | **12** | alert / dataSource / deviceOperation / dma / edgeDevice / firmware / node / notificationChannel / notificationDelivery / routeProgress* / sourceGroup / theme / websocket |
| **options store** | **5** | breadcrumb(31行) / channel(91) / parser(64) / ui(18) / user(80) |

\* `routeProgress.ts` 不是 pinia store（104 行模块级单例 `ref`，自己写了 `useRouteProgress()` 订阅，注释说明"替代 nprogress，零新依赖"）—— **它是文本判定之外的例外，已核实**。

**是否还有 Vuex 风格代码？** —— **没有**。
- `mapState / mapActions / mapGetters / mapStores` 在整个 `src/` 的真实代码里 **0 处**；唯一出现是在自动生成的 [auto-imports.d.ts:32-36](frontend-shared/src/auto-imports.d.ts#L32)（`unplugin-auto-import` 按 `imports: ['pinia']` 全量列出 pinia 导出，**不是使用**）。
- `mutations` / `commit` / `dispatch` **0 处**。
- `$subscribe` / `$onAction` / `$patch` / `storeToRefs` **全 0 处**（`grep` 实测）。`storeToRefs` 为 0 是**合理的**：12 个 store 是 setup 风格，返回的已是 `ref`，直接解构不丢响应性。

**结论：pinia 用法健康，无迁移债。**

### 4.2 pinia 4.1 的 `getStores()` 提供了什么更好的方式

**核实**：pinia 4.1.0 的 `dist/pinia.js` 导出列表里有 `getStores`（`grep -oE "export {[^}]*}" dist/pinia.js` 实测命中），且与 `getActivePinia / setActivePinia / storeToRefs / skipHydrate / shouldHydrate` 并列导出。来源：[pinia v4.1.0 release](https://github.com/vuejs/pinia/releases)（2026-10-14，"Add getStores()"）。

**本仓的真实落点**：不是"访问所有 store 数据"，而是**「会话清空」这批注册**——

现状：7 个文件在**模块顶层**各自 `registerSessionCacheClearer(() => useXxxStore().clearCache())`：

```
stores/channel.ts:91            stores/dma.ts:174          stores/node.ts:210
stores/deviceOperation.ts:174   stores/edgeDevice.ts:221   stores/firmware.ts:125
stores/parser.ts:62
```

`sessionCaches` 本体只有 19 行（[utils/sessionCache.ts](frontend-shared/src/utils/sessionCache.ts)），机制是「Set 存回调 + 逐个调」。用 `getStores()` 的正确用法需要 store **自己暴露** `clearCache`，然后：

```ts
// 仍需 7 个 store 各自实现 clearCache，但省掉 7 行「模块顶层副作用注册」
for (const store of getStores()) store.$dispose?.()
```

**能省多少（诚实量化）**：**约 −7 行 import 期副作用 + 可删 sessionCache.ts 中 `cacheClearers` Set 的 11 行**，合计 **−18 行左右**。但**代价**：`getStores()` 需要在 pinia **已安装后**调用（现在 7 处注册是模块 import 期，早于 `app.use(createPinia())`），改造要挪到 `main.ts` 时机 —— **这会引入时序风险，收益 18 行不值得**。

**裁决：不建议改。** 现有 `registerSessionCacheClearer` 模式虽然啰嗦，但它**在 import 期就完成注册**、与 store 定义同文件、可被单测独立 import，是**比 `getStores()` 更稳的形态**。这一项属于「新 API 存在但本仓不该用」。

### 4.3 一个**真**问题：5 个 options store 与 12 个 setup store 混用

不是错，但造成两套心智模型。最明显的代价是 **`clearCache` 之类的通用契约无法统一**（options store 里要写 `this.xxx = []`，setup 里写 `xxx.value = []`）。

- 若要收敛：**口径应统一到 setup**（12:5 的多数派，且 setup 对 TS 更友好）。
- 成本：5 个文件共 **284 行**（breadcrumb 31 + channel 91 + parser 64 + ui 18 + user 80），其中 [user.ts](frontend-shared/src/stores/user.ts) 是登录核心、[channel.ts](frontend-shared/src/stores/channel.ts) 有分页/缓存 epoch 逻辑 ⇒ **实际改写风险中高**。
- **收益**：纯一致性，**0 体积 0 性能**。**裁决：不做**（除非将来要重写这几个 store）。

---

## 5. vue-router 5.4.0

### 5.1 路由懒加载：**26 条路由 100% 懒加载**，且已做「首跳预取」

- `grep -c "component: () => import" src/router/index.ts` = **24**，`grep -c "component: load"` = **2**（具名 loader），`grep -c "component:"` = **26** ⇒ **没有一条静态 import**。
- 具名 loader 存在的目的写在 [routeLoaders.ts:1-2](frontend-shared/src/router/routeLoaders.ts#L1)：给 [MainLayout.vue:961](frontend-shared/src/views/layout/MainLayout.vue#L961) 的 `preloadPrimaryRoutes()` 复用同一 chunk（`Promise.allSettled([loadNodeList(), loadEdgeDeviceList()])`）。
- 登录页**额外做了首跳预取**：[prefetch.ts](frontend-shared/src/utils/prefetch.ts) 17 行两个 `import()`（`prefetchMainLayout`/`prefetchDashboard`），注释明确"字面量路径必须与 router 懒加载定义完全一致，保证命中同一 chunk"。
- 生产构建实测符合预期：`dist/assets` 有 **116 个 .js chunk**，路由页各自成 chunk（`Dashboard-*.js` 19.87 kB、`BmsDetailPage-*.js` 68.46 kB…），首屏只有 5 个 preload。

**裁决：懒加载已是最佳实践，无优化空间。**

### 5.2 数据预取：`beforeResolve` **刻意不用**，且有门禁锁死 —— 这是**正确**的取舍

[router/index.ts](frontend-shared/src/router/index.ts) 里守卫只有 3 个：`beforeEach`（[:214](frontend-shared/src/router/index.ts#L214)，认证）、`afterEach`（[:241](frontend-shared/src/router/index.ts#L241)，进度条收敛）、`onError`（[:247](frontend-shared/src/router/index.ts#L247)，失败收敛）。**没有 `beforeResolve`，没有 `router.beforeResolve` 任何出现**（`grep` 全仓 0 命中）。

而且 [primaryRouteReadiness.spec.ts:6](frontend-shared/src/router/__tests__/primaryRouteReadiness.spec.ts#L6) 有一条**反向门禁**：

```ts
expect(mainSource).not.toContain('router.beforeResolve')
expect(mainSource).not.toContain('preparePrimaryRoute')
```

**为什么这是对的**（不是"没做"）：`beforeResolve` 里等 API 会让**每次导航都阻塞在网络上**，首屏白屏时间 = 路由解析 + 最慢接口。仓库选了「先渲染骨架 + 组件内取数」+「登录页期间预下载 chunk」，这是更好的取舍。**裁决：不适用（原因：已用更优方案，且有门禁防回退）。**

### 5.3 滚动行为：`scrollBehavior` **不适用**（滚动容器不是 window）

`grep -rn scrollBehavior src` = **0 命中**。原因不是遗漏：

- 滚动容器是 `<el-main class="main-content">`，CSS `overflow-y: auto`（[MainLayout.vue:1545](frontend-shared/src/views/layout/MainLayout.vue#L1545)），**不是 window**。而 vue-router 的 `scrollBehavior` 只对 `window`/`document.documentElement` 生效（它对自定义容器无效）。
- 且该容器被**永久关闭 scroll anchoring**（[MainLayout.vue:1549](frontend-shared/src/views/layout/MainLayout.vue#L1549) `overflow-anchor: none`），注释里记录了真机实测（[:1527-1541](frontend-shared/src/views/layout/MainLayout.vue#L1527)："dashboard 滚到 1500 再等异步内容沉降，出现 +76/+60 px 的 scrollTop 自增，而 JS 侧录到 0 次程序化滚动"）。

**但仍然存在一个真实缺口**：**从滚动过的列表页进入详情页，返回后`.main-content` 停在旧位置**（因为容器 `scrollTop` 无任何重置逻辑，`grep -rn scrollTop src` 只命中 ChannelTerminal/RealtimeDataList/LogRealtimeViewer 的内部容器，**没有一处重置 `.main-content`**）。

**更好的方式（3 行，落在已有 `afterEach`）**：

```ts
router.afterEach(() => {
  useRouteProgress().done()
  document.querySelector('.main-content')?.scrollTo({ top: 0 })   // 新增
})
```

- 若要**保留后退位置**，vue-router 5.4 提供了 `useScrollRestoration` / `onRouteRendered`，但**实测它们不在主入口**：[dist/vue-router.js](frontend-shared/node_modules/vue-router/dist/vue-router.js) 的导出列表里没有这两个名字（只在 [dist/experimental/index.d.ts](frontend-shared/node_modules/vue-router/dist/experimental/index.d.ts) 里），主入口导出为 `NavigationFailureType, RouterLink, RouterView, START_LOCATION, createMemoryHistory, createRouter, createRouterMatcher, createWebHashHistory, createWebHistory, isNavigationFailure, loadRouteLocation, matchedRouteKey, onBeforeRouteLeave, onBeforeRouteUpdate, parseQuery, routeLocationKey, routerKey, routerViewLocationKey, stringifyQuery, useLink, useRoute, useRouter, viewDepthKey`（实测）。
  ⇒ **要用就得引 `vue-router/experimental`（未稳定），不建议。** 与 [frontend.md](docs/分析/依赖优化/frontend.md) §2.2 结论一致。
- **裁决：加 3 行容器 scrollTop 重置**（不带位置记忆），收益是"返回列表回到顶部"这个已知 UX 缺口。

### 5.4 路由 meta 类型安全：`RouteMeta` 已扩展，**但 4/5 个字段是死声明**

**现在怎么用**：[router/index.ts:8-17](frontend-shared/src/router/index.ts#L8)：

```ts
declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    title?: string          // 写了 18 处
    icon?: string           // 写了 15 处
    hiddenInMenu?: boolean  // 1 处
    hidden?: boolean        // 4 处（DEV 路由）
  }
}
```

**实测消费情况**（`grep -rn "meta\.title|meta\.icon|meta\.hidden|requiresAuth"` 排除测试）：**只有 `requiresAuth` 被读**（[index.ts:229](frontend-shared/src/router/index.ts#L229)、[:233](frontend-shared/src/router/index.ts#L233)）。`title`/`icon`/`hidden`/`hiddenInMenu` **全仓 0 处读取**。

**为什么没用**：侧栏菜单的真值源被独立成了 [menuModel.ts](frontend-shared/src/views/layout/menuModel.ts) 的 `NAV_DESTINATIONS`（14 条，[:43-58](frontend-shared/src/views/layout/menuModel.ts#L43) 附近）—— 该文件开头注释解释了原因（门禁把菜单项数硬编码成 14，改布局会让断言整批变红，故把"有哪些导航目标"从 DOM 里抽出来）。

⇒ **现在有「两套真值源」**：路由表里 18 个 `title` + 15 个 `icon`，与 menuModel 的 14 条 `{path, title}`。两者**互不校验**，靠人肉同步。

**更好的方式（二选一）**

| 方案 | 做法 | 收益 | 成本/风险 |
|---|---|---|---|
| A（推荐）**删死字段** | 路由 meta 只留 `requiresAuth` / `hidden`（DEV 用），`title`/`icon`/`hiddenInMenu` 删除 | **−34 行**声明（18+15+1），且**消除两套真值源的误导** | 低：`title`/`icon` 无人读 ⇒ 纯删。但 [uiuxRouteCoverage.spec.ts](frontend-shared/src/router/__tests__/uiuxRouteCoverage.spec.ts) 可能断言了 title 存在，需核验 |
| B **消费 meta 生成菜单** | `NAV_DESTINATIONS` 改由 `router.getRoutes()` 按 meta 派生 | 单一真值源 | **高**：`menuModel` 的 `NAV_GROUPS` 有手工分组与顺序契约，且 roving tabindex 依赖"数组顺序 == DOM 顺序"（[:37-42](frontend-shared/src/views/layout/menuModel.ts#L37) 注释）⇒ 自动化后会破坏键盘可达性不变量 |

**裁决：走 A（删死字段）**，并在 `routeLoaders`/`menuModel` 之间补一条"路由表 ↔ NAV_DESTINATIONS 覆盖一致"的门禁（若还没有）。**不建议 B。**

---

## 6. vue-virtual-scroller 3.0.5

### 6.1 现在怎么用：**只用 1 处**，但用得对

唯一使用点 [components/node/LogRealtimeViewer.vue](frontend-shared/src/components/node/LogRealtimeViewer.vue)：

- 模板 [:84-104](frontend-shared/src/components/node/LogRealtimeViewer.vue#L84) `<RecycleScroller :items="filteredLogs" :item-size="ROW_HEIGHT" key-field="id" :buffer="ROW_HEIGHT * 4">`
- 常量 [:152](frontend-shared/src/components/node/LogRealtimeViewer.vue#L152) `ROW_HEIGHT = 28`（固定行高 ⇒ 用 `RecycleScroller` 而非 `DynamicScroller`，选型正确）
- 类型化 ref [:154](frontend-shared/src/components/node/LogRealtimeViewer.vue#L154) `RecycleScrollerExposed<RealtimeLogLine>`（v3 才有的导出类型，说明是按 v3 API 写的）
- CSS [:116](frontend-shared/src/components/node/LogRealtimeViewer.vue#L116) 显式 import `vue-virtual-scroller/dist/vue-virtual-scroller.css`
- **测试已量化**：[LogRealtimeViewer.spec.ts:95-101](frontend-shared/src/components/node/__tests__/LogRealtimeViewer.spec.ts#L95) 断言「5000 条日志只挂载 >0 且 <100 个 `.log-line`」

### 6.2 该用没用的长列表 #1：`ChannelTerminal` 的 TX/RX 日志（**收益最大**）

**现状**：
- 两个 `v-for` 直接铺全量：[ChannelTerminal.vue:85](frontend-shared/src/components/channel/ChannelTerminal.vue#L85)（`txEntries`）、[:109](frontend-shared/src/components/channel/ChannelTerminal.vue#L109)（`rxEntries`）。
- **上限靠"砍数据"回避性能问题**：[:322](frontend-shared/src/components/channel/ChannelTerminal.vue#L322) `if (logEntries.value.length > 1000) logEntries.value = logEntries.value.slice(-800)` —— 即 DOM 里最多 **800 个**日志行。
- 每行含时间戳+方向+HEX/ASCII 内容（[:120-121](frontend-shared/src/components/channel/ChannelTerminal.vue#L120) 还有 `entry-meta` 徽标），**800 行 ≈ 2400–4000 个 DOM 节点**。

**能省多少**：`RecycleScroller` 在 28–32px 行高、典型 400px 视口下只挂 **13–15 行** + buffer ≈ **20–28 行** ⇒ 节点数 **800 → 约 25（−96.9%）**。同时**解锁「不再砍数据」**：上限可从 800 提到 5000+ 而不增加 DOM 成本 —— 这正好修一个潜在 UX 债（当前滚动到 800 条之前就丢历史）。

**成本/风险（必须点明）**：
1. ChannelTerminal 有**自动滚底**逻辑 [:324-325](frontend-shared/src/components/channel/ChannelTerminal.vue#L324)（`scrollTop = scrollHeight`），换 `RecycleScroller` 后要改用 `scrollerRef.value.scrollToBottom()`（v3 暴露 API），且 `scrollHeight` 语义变了（虚拟列表的容器高度是估算值）。
2. 逐条 `scrollTop` 的行为已有 [LogRealtimeViewer](frontend-shared/src/components/node/LogRealtimeViewer.vue) 的成熟参考（跟随/暂停/未读计数，[:226](frontend-shared/src/components/node/LogRealtimeViewer.vue#L226)），可直接抄架构。
3. 需要新增 e2e/单测断言（参照 [LogRealtimeViewer.spec.ts:95](frontend-shared/src/components/node/__tests__/LogRealtimeViewer.spec.ts#L95) 的"<100 行"口径）。

### 6.3 该用没用的长列表 #2：`RealtimeDataList`（边界收益，**不建议动**）

**现状**：[RealtimeDataList.vue:38](frontend-shared/src/components/data/RealtimeDataList.vue#L38) `v-for="item in items"`，普通滚动容器（[:35](frontend-shared/src/components/data/RealtimeDataList.vue#L35) `class="plain-list"`），数据上限 **200**（[useRealtimeData.ts:55](frontend-shared/src/composables/useRealtimeData.ts#L55) `maxItems = 200` / [useDeviceData.ts:37](frontend-shared/src/composables/useDeviceData.ts#L37) 同）。且**行高不固定**（多行帧不裁剪，[:24-25](frontend-shared/src/components/data/RealtimeDataList.vue#L24) 注释明确"行高随内容自然撑开"）。

**裁决：不适用（原因三条）**
1. 200 行 × 约 4 节点 = 800 节点，**低于虚拟化的收益阈值**（一般 ≥1000 行才明显）；
2. **行高不定** ⇒ 必须用 `DynamicScroller`（体积与复杂度都高于 `RecycleScroller`），且要引入 `DynamicScrollerItem` 包裹；
3. [RealtimeDataListSource.spec.ts:13](frontend-shared/src/components/data/__tests__/RealtimeDataListSource.spec.ts#L13) 有**反向门禁**：`expect(realtimeListSource).not.toContain('RecycleScroller')` —— 说明这个决定**已被显式做过并固化**（不是遗漏）。

### 6.4 用 `el-table` 的 19 处：**没有一处需要虚拟滚动**（全服务端分页）

`grep -rln "el-table" src` = 19 个文件。逐个核对数据来源：

| 页面 | 分页方式 | 单页行数 |
|---|---|---|
| [NotificationDeliveries.vue:73](frontend-shared/src/views/notification/NotificationDeliveries.vue#L73) | 真分页（[notificationDelivery.ts:34](frontend-shared/src/stores/notificationDelivery.ts#L34) "page/page_size 原样发给后端，绝不本地切片"） | 20（默认） |
| [NotificationChannels.vue:40](frontend-shared/src/views/notification/NotificationChannels.vue#L40) | 真分页 | 20 |
| [AlertRules.vue:20/:180](frontend-shared/src/views/alert/AlertRules.vue#L20) | 真分页（[alert.ts:92](frontend-shared/src/stores/alert.ts#L92)） | 20 |
| [AutomationRules.vue:25/:170](frontend-shared/src/views/automation/AutomationRules.vue#L25) | 真分页 | 20 |
| [Channels/DataSource/SourceGroup/Firmware/LogicalDevice/NodeList/EdgeDeviceList](frontend-shared/src/views/node/NodeList.vue#L173) | 真分页（`CHANNEL_LIST_MAX_PAGE_SIZE` 上界 200，[channel.ts:32](frontend-shared/src/api/channel.ts#L32)） | ≤200 |
| [LogHistoryPanel.vue:94](frontend-shared/src/components/node/LogHistoryPanel.vue#L94) | 真分页 `pageSize = 100`（[:140](frontend-shared/src/components/node/LogHistoryPanel.vue#L140)） | 100 |
| [DataPanel.vue:281](frontend-shared/src/views/data/DataPanel.vue#L281) | 分页 `pageSize = 20`（[:573](frontend-shared/src/views/data/DataPanel.vue#L573)） | 20 |
| [Dashboard.vue:294](frontend-shared/src/views/dashboard/Dashboard.vue#L294) | 概览摘要（`overview.latest_data`，后端限量） | 小 |

**结论：19 张表全部 ≤200 行/页，没有一张需要 `el-table-v2`。** 且 EP 官方把 `el-table-v2` 标为 beta（[官方文档](https://element-plus.org/en-US/component/table-v2)：生产使用有风险）⇒ **不适用（原因：无大数据量表格，且组件未 stable）**。

### 6.5 要不要用 EP 内置的 `ElVirtualList` 替掉 vvs？

**核实**：EP 2.14.7 从包根导出 `DynamicSizeList`/`DynamicSizeGrid`（[es/index.mjs:121-122](frontend-shared/node_modules/element-plus/es/index.mjs#L121)），但**没有导出名为 `ElVirtualList` 的 withInstall 包装**（`grep -o "ElVirtualList" es/index.mjs` 为空），需直接用 `FixedSizeList`/`DynamicSizeList` 组件。

**收益**：少一个依赖（vvs 的 `dist` 落盘 525 KB，但**进包的只有被 tree-shake 的部分**——实测 vvs 代码落在 `NodeOverview` chunk 里，该 chunk 178.85 KB / 55.54 KB gzip 含全部业务代码，**vvs 自身占比无法单独测出，不编造数字**）。

**裁决：不换。** 理由：① vvs 已在这 1 处稳定运行且有完整测试覆盖；② EP 的 list 组件需手动传 `height`/`width`（无 `AutoResizer` 包裹时），改动是**重构而非优化**；③ 零可量化收益。

---

## 7. TOP 5 建议（按「收益 ÷ 成本」排序）

### TOP 1｜删掉 `manualChunks` 里的 element-plus 分组 —— 首屏 gzip **−62.6%**

- **落点**：[vite.config.ts:116](frontend-shared/vite.config.ts#L116)（`if (id.includes('node_modules/element-plus/')) return 'element'`，删这一行）
- **实测收益**：首屏 JS+CSS gzip **310.2 → 116.1 KB**（−194.1 KB）；仅 JS **267.9 → 105.2 KB**；`/login` 闭包 272.7 → 144.1 KB
- **成本**：1 行。**风险**：低（首屏用到的 9 个 EP 组件都在入口闭包内，无 FOUC）
- **保留** echarts 那行（[vite.config.ts:115](frontend-shared/vite.config.ts#L115)），§2.4 有实测依据
- **顺带**：[vite.config.ts:111](frontend-shared/vite.config.ts#L111) 的 `rollupOptions` 在 **Vite 8 已 deprecated**（`node_modules/vite/dist/node/index.d.ts:952` 明写 @deprecated Use rolldownOptions instead），建议改名 `rolldownOptions`；`manualChunks` 本身在 rolldown 里也标 `@deprecated`（`rolldown/dist/shared/define-config-*.d.mts:828`，建议改 `output.codeSplitting.groups`）。改名零行为变化，可同批做。

### TOP 2｜`ChannelTerminal` 日志列表接 `RecycleScroller` —— DOM 节点 −96%，并解锁"不再砍历史"

- **落点**：[ChannelTerminal.vue:85](frontend-shared/src/components/channel/ChannelTerminal.vue#L85)、[:109](frontend-shared/src/components/channel/ChannelTerminal.vue#L109)（两个 v-for）、[:322](frontend-shared/src/components/channel/ChannelTerminal.vue#L322)（`slice(-800)` 的权宜之计要改）、[:324-325](frontend-shared/src/components/channel/ChannelTerminal.vue#L324)（自动滚底要改 API）
- **收益**：800 行 → 约 25 行（**−96.9%** 节点）；上限可从 800 提到 5000+ 而不加 DOM 成本
- **成本**：中（要接滚底/跟随语义，可整段参考 [LogRealtimeViewer.vue](frontend-shared/src/components/node/LogRealtimeViewer.vue) 的成熟实现 + 抄它的测试口径）
- **注意**：这是**唯一有真实性能收益的运行时改造**，其余都是代码整洁度

### TOP 3｜axios 取消错误保真 —— 2 行，消灭一类静默失败

- **落点**：[api/client.ts:101](frontend-shared/src/api/client.ts#L101) 之前插入 `if (axios.isCancel(error)) return Promise.reject(error)`
- **收益**：[useHistoryData.ts:118-125](frontend-shared/src/composables/useHistoryData.ts#L118) 的 5 路启发式（8 行）→ 1 行；**根除**"业务错误文案含 cancelled 字样就被当成用户取消、静默吞掉"的缺陷（[:313](frontend-shared/src/composables/useHistoryData.ts#L313) 取消分支直接 return 不报错）
- **成本**：小。**风险**：中低 —— 需核验 19 个文件里 104 处 `client.get<unknown, …>` 双泛型调用在"拿到原始 CanceledError"时不会误判类型（`instanceof ApiError` 检查会变 false）。建议先在 `useHistoryData` 单测加断言

### TOP 4｜路由 meta 删死字段 —— −34 行，消除两套真值源

- **落点**：[router/index.ts:10-16](frontend-shared/src/router/index.ts#L10) 删 `title`/`icon`/`hiddenInMenu`（全仓 0 处读取，只有 `requiresAuth` 被 [index.ts:229](frontend-shared/src/router/index.ts#L229) 读），并删掉 18 处 `meta: { title: … }` / 15 处 `icon`
- **收益**：**−34 行**；消除"路由表 title 与 [menuModel.ts](frontend-shared/src/views/layout/menuModel.ts) 的 `NAV_DESTINATIONS` 两套真值源互不校验"的隐患
- **成本**：小。**风险**：需先核验 [uiuxRouteCoverage.spec.ts](frontend-shared/src/router/__tests__/uiuxRouteCoverage.spec.ts) / [listPageConventions.spec.ts](frontend-shared/src/views/__tests__/listPageConventions.spec.ts) 是否断言了 title 存在
- **不要做**反面方案（用 meta 自动生成菜单）：会破坏 `NAV_GROUPS` 的 roving tabindex 顺序不变量

### TOP 5｜`.main-content` 导航后重置 scrollTop —— 3 行，补一个已知 UX 缺口

- **落点**：已有 [router/index.ts:241](frontend-shared/src/router/index.ts#L241) 的 `afterEach` 里加 `document.querySelector('.main-content')?.scrollTo({ top: 0 })`
- **收益**：修"从滚动过的列表进详情、返回仍停在旧位置"。**`scrollBehavior` 在这里不适用**（滚动容器不是 window，是 [MainLayout.vue:1545](frontend-shared/src/views/layout/MainLayout.vue#L1545) 的 `overflow-y:auto` 元素）
- **成本**：3 行。**风险**：低；若要保留后退位置需引 `vue-router/experimental`（**不建议**，未稳定，见 §5.3）

### 明确**不做**的（附原因，避免被反复提出）

| 项 | 不做原因（关键证据） |
|---|---|
| **echarts "全量引入改按需引入"** | **已经是按需引入**（§2.1：34 条 import 全走 `echarts/core` 子路径，生产只 2 文件 10 条）；565 KB 就是按需后的真实下限（§2.2 探针 F 行 564.88 kB 与实产 565.5 kB 差 0.1%）。改成全量会变成 1,115.94 kB（**+1.97×**） |
| echarts 换 SVG 渲染器 | 实测 SVGRenderer 比 Canvas 大 **+7.56 kB**，且实时曲线场景 Canvas 更合适（§2.2） |
| echarts 换 `setTheme` 求性能 | 实测 `setTheme` 7.93 ms **慢于** 全量 `setOption` 7.62 ms（§2.3③）。只有代码整洁收益（−45 行 / −6 observer），可做但别当性能优化 |
| 引入 `el-table-v2` | 19 张表全部服务端分页 ≤200 行/页（§6.4），且组件仍是 beta |
| 引入 `el-watermark` / `el-tour` / `el-anchor` / `el-splitter` | 无对应场景（§1.3 逐条给了证据；"锚点/水印"的 grep 命中全是字段名与 CSS 属性，不是组件需求） |
| `el-statistic` 替 `CountUp.vue` | EP 的 statistic **无动画**（源码 `displayValue` 是纯 computed），替换会丢功能 |
| axios 换内建 retry | **axios 1.20.0 没有内建 retry**（`grep retry lib/` 只命中 `parseHeaders.js` 的 `retry-after` 头名），现手写实现带 signal 感知，优于 `axios-retry` |
| pinia 用 `getStores()` 重整会话清理 | 需把 import 期注册挪到 `app.use` 之后，**引入时序风险换 −18 行**，不值（§4.2） |
| 5 个 options store 改 setup | 纯一致性，**0 体积 0 性能**，且涉及登录/分页核心逻辑（§4.3） |
| 用 `vue-router` 的 `useScrollRestoration` | **不在主入口**，只在 `dist/experimental/`（§5.3 实测导出列表） |
| 用 `el-segmented` 替换 23 处 radio-button | **可以做但不急**：净减约 40 行，但 [TimeRangeSelector.vue:91-171](frontend-shared/src/components/charts/TimeRangeSelector.vue#L91) 的 95 行 CSS 里有一半是 44px 触控与对比度门禁推导，换组件要重写且需视觉回归（§1.3） |

---

## 8. 本次分析的证据与可复现入口

**探针与产物**（全部在 gitignore 覆盖的 `.tmp-probe/` 下，**未改任何业务代码**）：

| 路径 | 用途 |
|---|---|
| `.tmp-probe/build-base/` | 现状基线构建（`npx vite build` 原配置） |
| `.tmp-probe/build-noel/` | 仅删 element 分组（§1.1 的 A/B） |
| `.tmp-probe/build-noec/` | 仅删 echarts 分组（§2.4 的 A/B） |
| `.tmp-probe/build-noep/` | 两者都删（保留供交叉验证） |
| `.tmp-probe/ec-probe/` | echarts 逐模块体积探针（`v-*.js` + `vite.config.mjs`）、`bench-theme.mjs`（setTheme vs setOption 基准） |
| `.tmp-probe/closure2.py` | chunk 依赖图闭包 + gzip 统计（首屏/路由闭包口径） |
| `.tmp-probe/noec.py` | echarts 分组的路由级闭包对比 |

**复跑方式**：

```bash
# 体积 A/B（约 5 s/次）
cd frontend-shared && npx vite build --outDir ../.tmp-probe/build-base --emptyOutDir
# 首屏闭包 + gzip
cd ../.tmp-probe && python3 closure2.py
# echarts 换主题基准
cd ec-probe && node bench-theme.mjs
```

**口径声明（避免误读）**

1. 所有 gzip 数字为 **gzip -9 单文件独立压缩后求和**（不是 HTTP 传输实测，未计头部与 HTTP/2 多路复用差异）。
2. 首屏口径 = `index.html` 里 `modulepreload` + 入口 `script` 的 **JS 闭包**；CSS 另列（§1.1 表格两行分开给了 JS 与 CSS）。
3. 构建环境：本机 Node v24.21.0、vite 8.3.5（rolldown 1.2.15）、minify=oxc，**单次构建**，未做多次取中位数（构建产物 hash 稳定，重复构建体积一致）。
4. 未测项（**不编造**）：echarts SVG vs Canvas 的**运行时渲染耗时**、`RecycleScroller` 化 ChannelTerminal 后的**实际帧率**、ep 暗色 64 处 `!important` 去掉后的对比度回归 —— 这三项都需要浏览器实测，本次未做。

**与既有分析的关系**：[frontend.md](docs/分析/依赖优化/frontend.md) 覆盖 Vue 3.5 / Vite 8 / TS 6 / 测试栈；本文件专攻**业务库**（element-plus / echarts / axios / pinia / vue-router / vue-virtual-scroller）。两者对 `manualChunks` 的结论**方向一致、绝对数不同**（口径差异已在 §1.1 说明），建议以本文件的**双构建同口径对比**为准。

---

## 9. 参考来源

- ECharts 6 新特性：<https://echarts.apache.org/handbook/en/basics/release-note/v6-feature/>
- ECharts v5→v6 升级指南：<https://echarts.apache.org/handbook/en/basics/release-note/v6-upgrade-guide/>
- ECharts 本地源码：`frontend-shared/node_modules/echarts`（`lib/core/echarts.js:2487` 内置 dark 主题、`lib/component/legend/LegendModel.js:250` v6 legend 默认 bottom）
- Element Plus 组件总览（含各组件引入版本角标）：<https://element-plus.org/en-US/component/overview>
- Element Plus Virtualized Table（beta 声明）：<https://element-plus.org/en-US/component/table-v2>
- Element Plus 本地源码：`frontend-shared/node_modules/element-plus/es`（`useDeprecated` 8 处调用点，逐条见 §1.2 表格）
- axios v1.20.0 release notes：<https://github.com/axios/axios/releases/tag/v1.20.0>
- Pinia v4.1.0 release（`getStores()`）：<https://github.com/vuejs/pinia/releases>
- Vite 8 `rollupOptions` → `rolldownOptions` 弃用：`node_modules/vite/dist/node/index.d.ts:952`
- rolldown `manualChunks` → `output.codeSplitting.groups` 弃用：`node_modules/.pnpm/rolldown@1.2.15/.../define-config-*.d.mts:828`

