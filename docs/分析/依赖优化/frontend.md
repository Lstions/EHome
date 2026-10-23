# 前端依赖新特性在 EHome 的落地分析

> 研究对象：`frontend-shared/`（101 个 `.vue` + 295 个 `.ts`，实测 find 计数）
> 版本：vue 3.5.44 / vue-router 5.4.0 / pinia 4.1.0 / element-plus 2.14.7 / vite 8.3.5 / vitest 5.0.5 / typescript 6.0.3 / vue-tsc 3.3.13 / @vue/test-utils 2.5.1 / happy-dom 21.0.4 / @playwright/test 1.64.1
> 分析日期：2026-10-23　分析者：fe-feat（agent）
> **只做分析，未修改任何业务代码。**
>
> ⚠️ **关于实验产物（必须记录的一次操作失误）**：本次分析为做 A/B 构建对比，在 `frontend-shared/.tmp-probe/`（该目录是仓库约定的、**.gitignore 忽略的**一次性探针目录，`.gitignore:94`）下建立了 4 个对比构建产物与 TS 探针。**清理时我误删了整个 `.tmp-probe/` 目录**，连同该目录中**此前会话遗留的历史探针脚本**（如 `r5-t16-p0-ablation.mjs`、`verify-fixes.mjs`、`monitor-zerovs-fail.mjs` 等）。
>
> - 影响面：这些文件**从未入库**（`git ls-files frontend-shared/.tmp-probe` = 0），且 `.gitignore` 明确将其定义为"一次性草稿"；**不涉及任何源码、配置、测试或仓库内文档的改动**。
> - **但确有损失**：`docs/分析/未发布产品负债盘点-2026-09-13.md:343,369,463,551,574` 与 `docs/分析/图表绘图区对齐与面包屑设备名-2026-10-21.md:101` 把其中几个脚本标注为"可复跑"，这些**复跑入口现已失效**。
> - 结论：**若将来要复跑那几份历史结论，需按文档描述重写探针**（文档里保留了探针名与用途，可重建）。这是本次分析的真实代价，不隐瞒。

---

## 0. 结论速览

| # | 结论 | 证据 | 量化收益 | 成本 |
|---|---|---|---|---|
| **1** | **`vite.config.ts` 的 `manualChunks` 是当前首屏体积的最大单点问题**：它把 element-plus 全量塞进 "element" chunk，而入口静态引用了该 chunk ⇒ **首屏必须下载全部 element-plus** | 实测：首屏 gzip **301.5 KB → 98.4 KB**（见 §3.3，落点 `vite.config.ts:114-118`） | **首屏 gzip −203 KB（−67%）**；首屏 raw 1105.8 KB → 284.9 KB | **小**（删 1 行） |
| 2 | `useTemplateRef` 有 **39 处**真实可替换点，且能顺手修掉 **2 处孤儿 ref**（模板写了 `ref="x"` 但脚本没有对应变量，当前是死引用） | §1.1 全量清单 | 39 处样板收敛 + 2 处隐性缺陷显性化 | 小（机械） |
| 3 | `defineModel` 有 **11 处** `defineProps + defineEmits + computed getter/setter` 三件套可删 | §1.2 | 每处删 ~8 行，共 **~104 行** | 小 |
| 4 | `pnpm build` 的真实瓶颈不是 Vite，是 **vue-tsc**：总 20.99s 里 typecheck 占 ~13.5s（64%），vite build 只占 ~4.5s | `/usr/bin/time` 实测 | 拆开跑可省 ~13s/次 | 小 |
| 5 | vitest 默认 pool 跑 109.5s，`--pool=vmThreads` 只要 **23.27s（4.7×）**，但会暴露 3 个测试的隔离假设问题 | 实测两轮 | 本地/CI 反馈速度 ×4.7 | 中（先修 3 个测试） |
| 6 | Vue Router 5 对我们**零破坏**（未用 unplugin-vue-router）；5.4 新 API（`useScrollRestoration`/`onRouteRendered`）**只在 `vue-router/experimental` 子路径导出，主入口没有** | tsc 实测 TS2305 | 当前不可用 | — |
| 7 | TS 6.0 的 `erasableSyntaxOnly` 当前**不可开**，因为仓库有 1 个 `enum` | `src/utils/dmaState.ts:11` | 开启后为 TS 7（tsgo）铺路 | 小 |

---

## 1. Vue 3.5.44 可用但未用的能力

官方来源：<https://blog.vuejs.org/posts/vue-3-5>

### 1.1 `useTemplateRef()` —— 39 处可替换 + 2 处隐性缺陷

**机制差异（决定了收益不只是"少写代码"）**：3.5 之前 `ref="x"` + `const x = ref()` 依赖**编译期**把变量名和模板 ref 属性名对上；`useTemplateRef('x')` 走**运行时字符串 ID**。因此：

- 旧的 `ref()` 写法如果变量名和模板对不上，**编译期不报错、运行期静默失效** —— 本仓库正好有 2 例（见下表"孤儿"）；
- `useTemplateRef` 配合 `@vue/language-tools` 2.1+ 会对模板里不存在的 ref 名给出告警（本仓库 vue-tsc 3.3.13 满足版本要求）。

**扫描口径**：`<script>` 区有 `const NAME = ref(...)` / `shallowRef(...)`，且 `<template>` 区有 `ref="NAME"`。用 Python 对 101 个 `.vue` 全量匹配（非正则抽样）。

**结果：39 处同名的成对引用**（可直接替换），**另有 2 处孤儿**。

~~~
# 39 处 useTemplateRef 可替换点（文件:脚本行号  变量名）
src/dev/BmsDemoPage.vue:434                          searchInputRef
src/dev/BmsDemoPage.vue:549                          trendChartRef
src/views/auth/Login.vue:103                         loginFormRef
src/views/auth/Login.vue:104                         initializeFormRef
src/views/automation/AutomationRules.vue:1302        riskCancelRef
src/views/edge-device/EdgeDeviceList.vue:731         tableRef
src/views/edge-device/EdgeDeviceList.vue:903         channelFormRef
src/views/edge-device/EdgeDeviceList.vue:904         deviceFormRef
src/views/edge-device/EdgeDeviceList.vue:1292        commandIntervalsRef
src/views/edge-device/GenericDeviceDetail.vue:79     historyChartRef
src/views/edge-device/inverter/InverterDetailPage.vue:169  historyChartRef
src/views/edge-device/bms/BmsCellVoltageChart.vue:29 chartRef
src/views/firmware/FirmwareManage.vue:201            uploadRef
src/views/layout/MainLayout.vue:618                  desktopMenuRef
src/views/layout/MainLayout.vue:696                  mobileMenuRef
src/views/layout/MainLayout.vue:697                  mobileMenuTriggerRef
src/views/layout/MainLayout.vue:699                  mobileDrawerBodyRef
src/views/node/NodeOverview.vue:1262                 tabBarRef
src/views/node/NodeOverview.vue:2602                 peripheralControlRef
src/views/profile/Profile.vue:62                     formRef
src/components/channel/ChannelManager.vue:281        formRef
src/components/channel/ChannelTerminal.vue:226       txLogContainer
src/components/channel/ChannelTerminal.vue:227       rxLogContainer
src/components/charts/LineChart.vue:240              chartRef
src/components/data/RealtimeDataList.vue:103         listContainer
src/components/forms/DeviceConfigForm.vue:231        formRef
src/components/forms/InitializeAdminForm.vue:110     formRef
src/components/forms/LoginForm.vue:104               formRef
src/components/forms/OTAForm.vue:177                 formRef
src/components/node/LogRealtimeViewer.vue:154        scrollerRef
src/components/node/QuickCreateDeviceDialog.vue:205  formRef
src/components/node/QuickCreateDeviceDialog.vue:330  commandIntervalsRef
src/components/periph/PeripheralControl.vue:117      pwmResourceList
src/components/periph/PeripheralControl.vue:118      gpioResourceList
src/dev/mobile-redesign/charts/CellSparkChart.vue:45 chartRef
src/dev/mobile-redesign/charts/PollIntervalChart.vue:40 chartRef
src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue:69 chartRef
src/dev/mobile-redesign/charts/TempScaleChart.vue:49 chartRef
src/dev/mobile-redesign/charts/TrendChart.vue:72     chartRef
~~~

**顺带能修的 2 处隐性缺陷（本次分析最实用的发现）**：

| 文件:行 | 现状 | 问题 | 换 `useTemplateRef` 后 |
|---|---|---|---|
| `components/common/CountUp.vue:2` | 模板 `<span ref="numberRef">`，脚本**没有** `numberRef` 变量 | 死引用；将来有人想拿 DOM 会得到 undefined 且无任何警告 | `const numberRef = useTemplateRef<HTMLSpanElement>('numberRef')`，vue-tsc 会检查模板 ref 名 |
| `views/firmware/FirmwareManage.vue:32` | 模板 `<el-table ref="tableRef">`，脚本**没有** `tableRef` | 同上；批量选择走 `selectedFirmwares`，所以没暴露 | 同上；拿到类型化实例后可 `clearSelection()` |

> 补充发现：`FirmwareManage.vue:22` 的"取消选择"是直接 `selectedFirmwares.value = []`，**没有**清 el-table 的勾选态 ⇒ 表格勾选格仍打钩但状态已清空。用 `useTemplateRef<TableInstance>('tableRef')` 后可 `tableRef.value?.clearSelection()`。**此条建议单独验证后再改。**

**风险（不是收益）**：`MainLayout.vue:618/696/697` 是 `ref<any>`，替换时必须补真实类型，否则在 `noUnusedLocals` 下更容易漏。

**实施成本**：小（每处 1-2 行）。**风险**：低。`auto-imports.d.ts:81` 已自动导入 `useTemplateRef`。

---

### 1.2 `defineModel()` —— 11 处 v-model 三件套可删

**扫描口径**：同时存在 `'update:xxx'` 的 `defineEmits` 声明 **且** 有 `computed({ get: () => props.xxx, set: emit(...) })` 的转发样板。

| 文件:行 | 现有样板 | 行数 | defineModel 写法 |
|---|---|---|---|
| `components/channel/ChannelManager.vue:268,274` | update:modelValue + `showDialog` computed | 8 | `const showDialog = defineModel<boolean>({ required: true })` |
| `components/charts/TimeRangeSelector.vue:36-49` | **[双]** modelValue + customRange 两条三件套 | **16** | 两个 defineModel，第二个带名 `defineModel<...>('customRange', ...)` |
| `components/device/LogicalDeviceCandidateSelect.vue:114,146,159` | update:modelValue 手动 emit | 8 | `defineModel<number \| null>({ required: true })` |
| `components/node/QuickCreateDeviceDialog.vue:194,202` | `showDialog` computed | 8 | `defineModel<boolean>({ required: true })` |
| `components/periph/PeripheralConfigDialog.vue:148,156` | `visible` computed | 8 | `defineModel<boolean>({ required: true })` |
| `components/device/DeviceBatchDeleteDialog.vue:54,61` | update:visible | 8 | `defineModel<boolean>('visible', { required: true })` |
| `components/device/DeviceDeleteDialog.vue:89,96` | update:visible | 8 | 同上 |
| `components/forms/DeviceConfigForm.vue:228,251` | update:visible | 8 | 同上 |
| `components/forms/OTAForm.vue:174,186` | update:visible | 8 | 同上 |
| `components/notification/NotificationChannelFormDialog.vue:170` | update:visible | 8 | 同上 |
| `components/node/LogRealtimeViewer.vue:146,147` | **[双]** update:paused + update:searchKeyword（模板 :16,:33 手写 `@update:model-value="emit(...)"` 转发） | **16** | 两个 defineModel |

**合计可删 ~104 行样板**，且 `defineModel` 自动生成 `update:xxx` emit 声明，父组件 `v-model` 用法**完全不变**（全仓库 344 处 `v-model` 无需改动）。

**收益**：
1. 删样板 ~104 行；
2. 消除同类隐患 —— 手写 `computed` 转发时若 getter 做了 normalize（如 `props.modelValue ?? false`），setter 会写回未 normalize 的值（`LogicalDeviceCandidateSelect.vue:146` 的 `emit('update:modelValue', null)` 与 setter 默认行为差异即此类）；
3. `defineModel` 是**编译期**行为，产物无运行时代理开销差异（原本 computed 也是 lazy 的），**所以不要把它当成性能优化** —— 它是可维护性优化。

**实施成本**：小。**风险**：低-中。
- `defineModel` 声明的 prop **默认不 required**；原 `defineProps` 写了 `required: true` 的必须显式补，否则父组件不传时从"警告"退化为"静默 undefined"。
- 双 model 点（`TimeRangeSelector` / `LogRealtimeViewer`）第二个参数名必须与父组件 `v-model:xxx` 完全一致，**建议最后做**。

---

### 1.3 `useId()` —— 1 处真实可修 + 1 处不建议动

| 文件:行 | 现状 | 结论 |
|---|---|---|
| `views/edge-device/shared/DeviceInfoCard.vue:32` | `aria-controls="mobile-info-more"` —— **全仓库搜索 `mobile-info-more` 只有这一处**，被指向的 id 根本不存在 | **真 bug，值得修**。`aria-controls` 指向不存在的 id，屏幕阅读器无法建立关联。展开区（同文件 :41 `<template v-if="moreOpen">`）没有任何包裹元素带 id。修法二选一：① 把 :41 的 `<template>` 换成 `<div :id="moreId">`（会多一层 DOM，需验证移动端样式）；② `const moreId = useId()` + `:aria-controls="moreId"` + `:id="moreId"` |
| `views/edge-device/bms/CollapseBlock.vue:41,60,74` | `bodyId = 'collapse-body-' + props.blockId` | **不建议换**。`blockId` 已是业务唯一键（`BmsDetailPage.vue:253,329,350,375,483` 5 个固定值），语义稳定、可读、便于 e2e 断言。`useId()` 在这反而降低可测性。**这是"能用但不该用"的典型。** |

**关于"用 useId 生成表单 label/for 关联"**：本仓库只有 **1 处** `<label for=`（`views/node/NodeOverview.vue:960` → :965 `<el-input-number id="baud-tool-input">`），已手工配对且**正确工作**。全站 `id="..."` 共 74 处，绝大多数是 data-testid 风格或 DEV 页常量，**无批量 useId 化价值**。

**SSR 相关**：本项目是纯 SPA（`main.ts` 直接 `createApp().mount('#app')`，无 SSR 代码，`vitest.config.ts` 无 SSR 配置），`useId` 的"跨 server/client 稳定"核心卖点**对本项目不适用**，仅剩"手工唯一 id"的便利，价值低。

**实施成本**：小。**风险**：低。

---

### 1.4 deferred `<Teleport defer>` —— **本项目不适用（原因已核实）**

- 全仓库模板内**零个 `<Teleport>`**（`grep -rn -i '<teleport'` = 0）。
- 现有"传送到 body"全部由 Element Plus 组件内部完成：`el-dialog` 48 处、`el-drawer` 4 处、`append-to-body` 12 处（如 `components/forms/LoginForm.vue:78`、`views/layout/MainLayout.vue:59` 的移动端抽屉）。
- `MainLayout.vue:1564`、`:1740` 的注释提到 "Teleport 到 body"，指的是 **el-drawer 内部行为**导致的 scoped 样式失效（所以用了 `:global()`），不是我们自己的 `<Teleport>`。

**结论**：`defer` 解决的是"目标元素在 Teleport 之后才渲染"，本项目没有手写 Teleport，**无落点**。将来若要自管全局 Toast 容器再考虑。

---

### 1.5 `onWatcherCleanup()` —— 1 处真实可改（顺带修 leak）

| 文件:行 | 现状 | 改法 | 收益 |
|---|---|---|---|
| `composables/useDebouncedSearch.ts:29-39` | `watch(searchKeyword, ...)` 里 `setTimeout`，**无 onUnmounted 清理**；组件卸载时若 timer 未触发会残留（300ms 后对已卸载组件的 ref 赋值） | 在 watch 回调里 `onWatcherCleanup(() => { if (debounceTimer) clearTimeout(debounceTimer) })` | 消除 3 个页面（`views/edge-device/EdgeDeviceList.vue:760`、`views/logical-device/LogicalDeviceList.vue:374`、`views/node/NodeList.vue` 附近）的卸载后计时器 |

**注意**：这个 leak 的**实际危害很小**（300ms 内自愈、ref 赋值无副作用），**不要当成性能优化来吹**。真正价值是**写法上不必再在别处手工 `onUnmounted(() => clearTimeout(...))`**。当前仓库其它 watch 都无此需求。

`auto-imports.d.ts:54` 已全局导入 `onWatcherCleanup`，可直接用。**成本**：小。**风险**：极低。

---

### 1.6 有落点但**不值得改**的 Vue 3.5 特性（诚实标注）

| 特性 | 本仓库落点 | 为什么不做 |
|---|---|---|
| **Reactive Props Destructure**（3.5 稳定） | `withDefaults(defineProps<...>())` 共 **40 处**（如 `components/charts/LineChart.vue:202`、`components/common/StatusBadge.vue:11`） | ① 40 处全是"带默认值 + 模板直接用 `props.xxx`"，改为解构后模板里 `props.x` 仍要改；② 解构变量在模板中不能替代 `props.x` 的派生用法；③ 一旦有人写 `watch(count)` 传解构变量会**编译报错**（必须 `() => count`）。**迁移风险高于收益，建议不动。** |
| **Reactivity 系统重写（内存 −56%）** | 全仓库 | 升级到 3.5 已**自动获得**，无需代码改动。**这是本次升级最大的免费收益**，但它不是"可落地动作"，故不列为建议项。 |
| **深层响应式数组跟踪 10× 提速** | `RealtimeDataList.vue`、`LogRealtimeViewer.vue`（5000 条日志）、`EdgeDeviceList.vue`（1003 条） | 升级即获得。若要进一步应改 `shallowRef` —— `views/edge-device/EdgeDeviceDetailRouter.vue:68` 已是正确范例。 |
| `data-allow-mismatch` | 无 | 纯 SPA，无 hydration ⇒ 不适用。 |
| **SSR Lazy Hydration**（`hydrateOnVisible` 等） | 无 | 纯 SPA（`main.ts` 无 SSR 入口）⇒ 不适用。**这是 Vue 3.5 最大新特性，但对我们零价值。** |
| **Custom Elements**（`useHost`/`useShadowRoot`/`shadowRoot:false`） | 无 | 0 处 `defineCustomElement` ⇒ 不适用。 |

---

## 2. Vue Router 5.4 / Pinia 4.1 新能力

### 2.1 Vue Router 5：升级零破坏（已核实）

官方来源：<https://router.vuejs.org/guide/migration/v4-to-v5.html>

> 原文："**If you're using Vue Router 4 without unplugin-vue-router, there are no breaking changes** - you can upgrade without any code modifications."

**核实**：`package.json` 无 `unplugin-vue-router`，路由是手写表（`src/router/index.ts`，30 个 `path:`、18 个带 `meta.title`），因此 **5.0 的核心变更（文件路由并入核心包）对我们完全透明**。唯一注意：v5 的 iife 构建不再含 `@vue/devtools-api`，我们用 ESM，无影响。

### 2.2 5.2 → 5.4 的新 API：**主入口没有，只在 `vue-router/experimental`**

用 tsc 实测导出面（不是看文档猜）：

~~~
# 从主入口导入 —— 不存在
$ tsc --noEmit ./probe1.ts     # 临时探针，测完已删
error TS2305: Module '"vue-router"' has no exported member 'useScrollRestoration'.
error TS2305: Module '"vue-router"' has no exported member 'onRouteRendered'.

# 从 experimental 子路径导入 —— 通过（0 error）
$ tsc --noEmit ./probe2.ts     # 临时探针，测完已删
~~~

对照 5.4.0 CHANGELOG（<https://github.com/vuejs/router/blob/main/packages/router/CHANGELOG.md>）：

| 5.4.0 新特性 | 导出位置 | 本仓库可用性 |
|---|---|---|
| `useScrollRestoration()` | `vue-router/experimental` | 需引 experimental（见下） |
| `onRouteRendered()` | `vue-router/experimental` | 同上 |
| `createRouter` 接受预构建 matcher | 主入口，但标 `@internal for Nuxt only` + `@deprecated` | **不可用**（官方明确只给 Nuxt） |
| 实验性 query 值统一为数组 | `vue-router/experimental` | 破坏性（仅 experimental router 内） |

**当前 `src/router/index.ts` 的实况**：

| 项 | 现状 | 评估 |
|---|---|---|
| `scrollBehavior` | **完全没配**（`router/index.ts:207-211` 只有 `history` + `routes`） | **真实缺口**：`views/layout/MainLayout.vue:1545` 的 `.main-content` 是唯一滚动容器（`overflow-y:auto`），路由切换后 `scrollTop` 不重置。从长列表（`EdgeDeviceList` 1003 条）跳详情再返回会停在旧位置 —— 但这**有时是期望行为**。**建议先产品确认**，再决定用内建 `scrollBehavior`（主入口就有，v4 时代 API）还是 experimental 的 `useScrollRestoration`。 |
| `beforeEach` / `afterEach` / `onError` | `index.ts:214 / 241 / 248`，接 `useRouteProgress` | 工作正常，**不建议动**。5.x 无相关增强。 |
| 路由 meta 类型 | `index.ts:8-17` 的 `declare module 'vue-router' { interface RouteMeta {...} }` | 正确，5.x 仍支持。 |
| `TypesConfig` 增强（5.1，可覆盖 `useRouter()` 返回类型） | 未使用 | **不采用**：官方 `Router: typeof router` 写法在自引用时报 `TS2502: 'Router' is referenced directly or indirectly in its own type annotation`（实测：临时建 `router-stub.ts` + `tc2.ts` 探针，已被清理）。收益不抵复杂度。**标注为"文档方案在本仓库形态下不可直接用"。** |

### 2.3 路由表守卫：**已被自家门禁锁死，改路由形态成本很高**

`src/router/__tests__/uiuxRouteCoverage.spec.ts` 是一道 fail-closed 门禁：静态解析 `router/index.ts` 与 `routeLoaders.ts`，要求每个"布局下的无参数相对子路由"必须登记进 `e2e/helpers/uiux-fixtures.ts` 的 ROUTES，`REGISTERED_ROUTE_EXCLUSIONS` 是显式白名单。

**推论**：任何"改用文件路由 / 让 router 自动生成"的方案都会让这道门禁**解析失败**（它只认两种写法：内联 `() => import()` 和 `routeLoaders.ts` 具名 loader），需同步重写门禁。**所以 Vue Router 5 的文件路由能力对本仓库不是"可用新特性"，而是"要付门禁重写成本的重构"。标注为不适用。** 这是容易被忽略的重要判断。

### 2.4 Pinia 4.1：只有 1 个新 API，对我们价值有限

官方来源：<https://github.com/vuejs/pinia/releases/tag/v4.1.0>

| 版本 | 变更 | 本仓库影响 |
|---|---|---|
| 4.0 | **ESM only**；`@vue/devtools-api` 升级到 v8 且**必须与 pinia 一起安装** | 已装（lockfile：`pinia@4.1.0(@vue/devtools-api@8.2.1)`，实际 8.2.1）。ESM only 无影响（Vite ESM）。 |
| 4.0 | 移除废弃 API | 未用（`defineStore` 16 处全部现行写法）。 |
| **4.1.0** | **新增 `getStores(pinia)`** | 见下 |
| 4.1.0 | 释放被替换的 store 时给警告 | 不做 store 热替换，无影响。 |
| 4.1.0 | 修复"通过 object syntax patch 的 shallowRef 不触发" | 无落点。 |

**`getStores()` 的可行落点（价值中等，结论是不建议）**：

现状是每个 store 在模块加载时**自己注册**清理器，共 **7 处**：
~~~
src/stores/channel.ts:91           registerSessionCacheClearer(() => useChannelStore().clearCache())
src/stores/deviceOperation.ts:174  registerSessionCacheClearer(() => useDeviceOperationStore().clear())
src/stores/dma.ts:174              registerSessionCacheClearer(() => useDmaStore().clearCache())
src/stores/edgeDevice.ts:221       registerSessionCacheClearer(() => useEdgeDeviceStore().clearCache())
src/stores/firmware.ts:125         registerSessionCacheClearer(() => useFirmwareStore().clearCache())
src/stores/node.ts:210             registerSessionCacheClearer(() => useNodeStore().clearCache())
src/stores/parser.ts:62            registerSessionCacheClearer(() => useParserStore().clearCache())
~~~
配合 `src/utils/sessionCache.ts:8` 的 `clearSessionCaches()`。

**若改用 `getStores()`（在 `clearSessionCaches()` 里遍历）是负收益**：
1. 并非所有 store 都有 `clearCache`（`user`/`theme`/`ui`/`breadcrumb`/`routeProgress` 没有），需能力探测；
2. 遍历"已实例化的 store"意味着**未实例化的不会被清**，而当前"import 即注册"是更严格的全覆盖；
3. 当前 7 处注册是**显式**的，审计友好。

**结论：有落点但不建议改，标注"本项目不适用（现有机制更严格）"。** 若将来出现"运行时枚举 store"的新需求（devtools 面板），它才有价值。

---

## 3. Vite 8.3.5（Rolldown / Environment API / Oxc）—— 本次分析重点

### 3.1 实测基线（AMD Ryzen 5 5600，12 核，Linux）

| 命令 | 实测 |
|---|---|
| `vite build` ×5 | **3.09 / 3.44 / 3.64 / 3.93 / 4.00 s**（中位 ~3.6s） |
| `vue-tsc --noEmit` ×2 | **13.86 / 13.22 s** |
| `pnpm build`（typecheck && vite build） | **20.99 s** ⇒ **typecheck 占 64%** |
| `vitest run`（默认 pool） | **109.5 s**（另一轮 343s，抖动极大） |
| `vitest run --pool=vmThreads` | **23.27 s** |

**→ 关键结论：本项目 `pnpm build` 的瓶颈是 vue-tsc，不是 Vite。** 讨论"Rolldown 能省多少"之前先看这 13.5s。

### 3.2 Rolldown 能带来多少提速？—— **无法用本仓库测出（诚实标注）**

**未能核实的原因**：pnpm store 里只有 `vite@8.1.5` 和 `vite@8.3.5`，**没有 Vite 7 / rollup / esbuild 版本的 vite**。做 A/B 必须装 `vite@7` + `rolldown-vite`，属"改依赖"，超出本次"只分析"范围。因此：

- ❌ **不要引用"Rolldown 快 10-30×"当我们的结论** —— 那是大仓库冷启动场景；我们只有 101 个 SFC、**1601 modules**（`vite build` 实测输出），本来就小。
- ✅ **可引用的自有数据**：现在 `vite build` **3.1–4.0s**，其中大量时间在 **CSS 处理**（Lightning CSS 产出 37 个 CSS 文件、raw 531 KB）与 1601 模块 transform。
- **合理预期**：vite build 从 3.5s 到 **3.0–3.3s**（省 5-15%）。剩余时间有相当比例是 Rolldown/Oxc 之外的开销（插件链 `unplugin-auto-import` + `unplugin-vue-components` + `@vitejs/plugin-vue` 各自的 AST 遍历）。**收益不在时间，在 §3.3 的体积。**

官方来源：<https://vite.dev/guide/migration>

### 3.3 【最高价值发现】`manualChunks` 让首屏多下载 203 KB gzip

**机制（核心）**：

`vite.config.ts:114-118`：
~~~
manualChunks(id) {
  if (id.includes('node_modules/echarts/')) return 'echarts'
  if (id.includes('node_modules/element-plus/')) return 'element'
  if (id.includes('node_modules/vue/') || ... ) return 'vue'
}
~~~

`manualChunks` 的语义是：**把所有匹配模块强制合并进同一 chunk**。于是 element-plus 被用到的**全部**组件（不管哪个路由用）都打进 `element-BPCwiAPF.js`（736 KB raw / 233.6 KB gzip）。而入口 `App.vue:2` 有 `<el-config-provider>`、`main.ts:3` 有 `import 'element-plus/theme-chalk/dark/css-vars.css'` ⇒ **入口静态依赖该 chunk ⇒ 它被写进 index.html 的 modulepreload** ⇒ **首屏必下 233 KB gzip 的 element-plus，即使用户只看登录页**。

**实测证据**（`dist/index.html` 的 preload 链）：
~~~
<link rel="modulepreload" href="/assets/rolldown-runtime-XKyaHtl9.js">
<link rel="modulepreload" href="/assets/element-BPCwiAPF.js">   ← 736 KB raw / 233.6 KB gzip
<link rel="modulepreload" href="/assets/vue-BqhOJxl4.js">
<link rel="modulepreload" href="/assets/client-DtncArCR.js">
<link rel="stylesheet"   href="/assets/element-BB8jcc2Z.css">    ← 292 KB raw / 38.7 KB gzip（全部 element 样式）
<link rel="stylesheet"   href="/assets/index-CdvQmu_7.css">
# echarts 不在 preload（Dashboard 用 defineAsyncComponent 懒加载了 LineChart）✓
~~~

**5 种分包策略实测对比**（同一份源码，只改 build 段；"首屏" = `index.html` 里 preload 的 JS + CSS 的 gzip 总和）：

| 方案 | 首屏 preload JS | 首屏 CSS | **首屏 gzip 合计** | build 时间 |
|---|---|---|---|---|
| **A 当前**（element + echarts + vue 三组） | 4 文件 / 258.7 KB | 42.7 KB | **301.5 KB** | 3.1–4.0 s |
| **B 完全去掉 manualChunks**（Rolldown 默认） | 7 文件 / 79.3 KB | 10.9 KB | **90.3 KB** | 2.5–3.1 s |
| **C 只保留 vue 分组** | 7 文件 / 87.4 KB | 10.9 KB | **98.4 KB** | 5.6 s（单次抖动） |
| **D 保留 echarts + vue，去掉 element 分组** | 7 文件 / 87.4 KB | 10.9 KB | **98.4 KB** | **2.8–3.4 s** |
| E 改用 Rolldown `codeSplitting.groups`（element 切 200 KB 上限） | 19 文件 / 228.3 KB | 42.7 KB | 271.1 KB | 2.1 s |

**→ 方案 D 最优：首屏 gzip −203 KB（−67%），构建时间还略降。** 保留 echarts 分组有意义（echarts 565 KB 是异步的，分组避免被塞进路由 chunk 造成重复）；去掉 element 分组让 Rolldown 按实际依赖自动切分，每个路由只带自己的 element 组件。

**Why B/C/D 的 CSS 从 307.6 KB 降到 62.3 KB**：`element-BB8jcc2Z.css`（292 KB）被打包进 element chunk 的 CSS，随 preload 首屏下载；去掉分组后，element 的按需 CSS 跟随各路由 chunk 加载。

**方案 D 的具体改法（3 行，只删不加）**：
~~~
// vite.config.ts:114-118 现状
manualChunks(id) {
  if (id.includes('node_modules/echarts/')) return 'echarts'
  if (id.includes('node_modules/element-plus/')) return 'element'   // ← 删掉这行
  if (id.includes('node_modules/vue/') || ...) return 'vue'
}
~~~
**同时必须改的**：`vite.config.ts:111` 的 `rollupOptions` 在 Vite 8 已 **deprecated**，应改为 `rolldownOptions`（官方迁移文档 §Other Related Deprecations："`build.rollupOptions`: renamed to `build.rolldownOptions`"）。现在能跑是因为兼容层还在。

**验证口径（别只信构建日志）**：
1. `grep 'modulepreload' dist/index.html` 确认 element 已不在首屏；
2. 真实浏览器打开登录页，DevTools Network 筛 `element` —— 应只在进入业务页后才加载；
3. **必须回归 e2e**（`e2e/uiux-regression.spec.ts` 共 1037 行 + 其余 11 个 spec）。

**风险（诚实说）**：中。
- ① 分包变化改变首屏 waterfall，白屏时间可能不变但 **LCP 元素所在 chunk 顺序变了**，需实测 LCP；
- ② `src/components/common/RouteProgressBar.vue` 依赖"懒加载 chunk 下载期间显示"，chunk 数从 106 → 116 会让更多页面出现进度条（是好事，但视觉变化）；
- ③ 若有代码依赖"element chunk 一定先加载"的隐含顺序会暴露。**故定为"中风险 + 必须先跑 e2e"。**

### 3.4 Oxc 替代 esbuild —— **已生效，无需动作；但有 1 个潜在地雷**

官方："JavaScript Transforms by Oxc"、"JavaScript Minification by Oxc"、"CSS Minification by Lightning CSS"。

**已生效（升级即得）**：
- `vite build --help` 实测：`--minify ... (default: 'oxc')`；
- `vite.config.ts` 里**没有**任何 `esbuild`/`build.minify`/`build.target` 覆盖 ⇒ 全走默认，自动享受 Oxc + Lightning CSS；
- `esbuild` **已不是依赖**（实测 `ls node_modules/.pnpm | grep '^esbuild@'` = 0；`require.resolve('esbuild')` 失败）—— Vite 8 把 esbuild 降为 optional peer 的结果，**我们的插件链没有依赖它，好事**；
- `lightningcss@1.33.0` + `lightningcss-linux-x64-gnu@1.33.0` 已安装。

**潜在地雷（提前知道）**：Oxc **不支持** esbuild 的 `mangleProps`/`reserveProps`/`mangleQuoted`/`mangleCache`，也不支持 `esbuild.supported`。本仓库**都没用** ⇒ 无风险。Oxc 对 **TS legacy namespace 只部分支持**、**decorators 不 lower** —— 本仓库 0 namespace、0 decorator ⇒ 无风险。

### 3.5 Vite Environment API —— **本项目不适用（原因）**

- 单页 SPA，**无 SSR**、**无 `?worker` / `new Worker()`**（`grep -rn '?worker|new Worker|ssrLoadModule'` = 0）、单 `index.html` 入口。
- Environment API 的价值在"同一份代码同时构建 client / ssr / edge / worker 多环境"，我们没有第二个环境。
- `vite.config.ts` 也是单 environment 形态（一个 default client 构建 + `manualChunks`）。

**结论：不适用。** 若将来把 ECharts 重计算移入 Web Worker（`utils/downsample.ts` 的降采样是纯 CPU 密集函数，2617 行 utils 里最像 worker 候选的），vite 的 `worker` 环境配置才有用武之地 —— 但那是新功能开发，不是"升级带来的能力"。

### 3.6 其它 Vite 8 变更对本仓库的影响（逐条核实）

| Vite 8 变更 | 对本仓库影响 | 证据 |
|---|---|---|
| 默认 `build.target` → Chrome 111 / Safari 16.4 等 | 无影响（EHome 是内网/自用 IoT 面板，移动端为现代浏览器） | 未配 `build.target` |
| **CJS interop 语义统一**（默认导入行为变化） | **低风险**：核心依赖全 ESM —— `vue`(ESM)、`element-plus` 有 `es/index.mjs`、`vue-virtual-scroller` 有 `type:module`、`echarts` 有 `type:module`、`axios` 有 `type:module`。**没有"只发 CJS 且靠 default 导入"的依赖。** | `node -e` 读各包 package.json |
| 移除 `browser`/`module` 字段的格式嗅探 | 低风险（上述包都有 `exports`/`module`） | 同上 |
| `build.rollupOptions` → `build.rolldownOptions`（deprecated） | **应改**（`vite.config.ts:111`） | 官方迁移文档 |
| `manualChunks` 对象形式移除 / 函数形式 deprecated | **我们的函数形式已 deprecated**，未来要迁到 `codeSplitting.groups` | 官方 + 实测 `rolldown@1.2.15` 支持 `output.codeSplitting.groups[]` |
| `build.commonjsOptions` 变 no-op | 未使用 | — |
| `import.meta.url` 在 UMD/IIFE 不再 polyfill | 未用 UMD/IIFE | — |
| `build()` 抛 `BundleError` | 仅影响 JS API 用户；我们用 CLI | — |
| `parseAst`/`parseAstAsync` deprecated | 未使用 | — |

**关于 `codeSplitting.groups`（Rolldown 原生替代，方案 E 实测可用）**：
~~~
// rolldown@1.2.15 的 schema 实测支持
// node_modules/.pnpm/rolldown@1.2.15/.../create-bundler-option-*.mjs:1674
rolldownOptions: { output: { codeSplitting: { groups: [
  { name: 'echarts', test: /node_modules[\/]echarts[\/]/ },
  { name: 'vue', test: /node_modules[\/](vue|vue-router|pinia|@vue)[\/]/ },
] } } }
~~~
实测方案 E（含 element `maxSize: 200000`）把 element 切成 8 个 59-81 KB 的块，但**首屏仍是 271 KB gzip**（入口仍静态依赖所有 element 块）⇒ **切碎不等于延后**。**结论：先用 §3.3 方案 D（删一行），不要急着上 codeSplitting.groups。**

---

## 4. TypeScript 6.0.3 新能力

官方来源：<https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html>

### 4.1 已通过 typecheck 说明什么（以及没说明什么）

`pnpm typecheck`（`vue-tsc --noEmit`）0 error，说明我们**没踩** TS 6 的破坏性默认值：

| TS 6 新默认值 | 我们的情况 | 是否踩到 |
|---|---|---|
| `strict` 默认 true | `tsconfig.json:17` 显式 `"strict": true` | 否（本来就开） |
| `types` 默认 `[]`（不再自动加载全部 @types） | `tsconfig.json:7` 显式 `["vite/client", "node"]` | 否 |
| `module` 默认 `esnext` | `tsconfig.json:5` 显式 `"module": "ESNext"` | 否 |
| `target` 默认当年 ES（现 es2025） | `tsconfig.json:3` 显式 `"target": "ES2020"` | 否 |
| `noUncheckedSideEffectImports` 默认 true | 4 处 CSS 副作用导入（`main.ts:3,11,12,13`、`components/node/LogRealtimeViewer.vue:116`）全部能解析到真实文件 ⇒ 通过 | 否（但**依赖"这些 CSS 真实存在"**；删掉 `element-plus/theme-chalk/el-message.css` 会直接报错 —— 这是好事） |
| `rootDir` 默认 `.` | `noEmit: true`，无产物路径问题 | 否 |
| `libReplacement` 默认 false | 未用 | 否 |
| `esModuleInterop`/`allowSyntheticDefaultImports` 不能设 false | 未设 | 否 |
| `alwaysStrict` 不能设 false | 未设 | 否 |
| `--moduleResolution node` deprecated | 我们用 `bundler`（`tsconfig.json:9`） | 否 |
| `target: es5` deprecated | 我们用 ES2020 | 否 |
| `baseUrl` deprecated | 未用 `baseUrl`（只用 `paths`，`tsconfig.json:14-16`） | 否 **（这点做对了）** |
| `outFile` 移除；`amd`/`umd`/`systemjs` 移除 | 未用 | 否 |
| `module` 关键字的 legacy namespace 语法报错 | 0 处 namespace | 否 |
| `asserts` 改 `with` 导入属性 | 0 处 | 否 |
| `tsc foo.ts` 在有 tsconfig 时报 TS5112 | 只用 `-p`/`--noEmit` | 否 |

> ⚠️ **实测确认 TS5112 存在**：跑 `tsc --noEmit <单个文件>.ts` 报
> `error TS5112: tsconfig.json is present but will not be loaded if files are specified on commandline. Use '--ignoreConfig' to skip this error.~
> 这是 TS 6 新增行为，任何 CI 脚本里"直接传文件名给 tsc"的做法都会失败。**检查点：`package.json` 的 `typecheck` 是 `vue-tsc --noEmit`（不传文件）= 安全。** 但若有人在 CI/Makefile 里写 `tsc src/x.ts` 会立刻红。

### 4.2 `--erasableSyntaxOnly`：**当前不可开（实测），但值得规划**

**实测**（临时目录建 `probe.ts` + `tsconfig.erasable.json`，已清理）：
~~~
error TS1294: This syntax is not allowed when 'erasableSyntaxOnly' is enabled.  ← enum
error TS1294: This syntax is not allowed when 'erasableSyntaxOnly' is enabled.  ← namespace
error TS1294: This syntax is not allowed when 'erasableSyntaxOnly' is enabled.  ← constructor(private x)
~~~

**本仓库阻塞项（全量扫描）**：

| 语法 | 数量 | 位置 |
|---|---|---|
| `enum` | **1 处** | `src/utils/dmaState.ts:11` —— `export enum DmaState { FREE = 0, ALLOCATED = 1, DISABLED = 2 }` |
| `namespace` | 0 | — |
| 构造器参数属性 | 0 | — |

**→ 只差 1 个 enum 就能开。** 改法（等价）：
~~~
// 现状 dmaState.ts:11-19
export enum DmaState { FREE = 0, ALLOCATED = 1, DISABLED = 2 }

// 改后（erasable）
export const DmaState = { FREE: 0, ALLOCATED: 1, DISABLED: 2 } as const
export type DmaState = typeof DmaState[keyof typeof DmaState]
~~~
**注意**：`dmaState.ts:22` 的 `Record<DmaState, string>` 用枚举做 key，改 `as const` 后仍成立（`DmaState` 变 `0|1|2` 联合）。同文件后续 `Record<DmaState, ...>` 均兼容。

**收益**：① 为 TS 7 铺路（TS 7 是 Go 原生实现，官方称快 10×；`erasableSyntaxOnly` 是提前声明"不依赖 tsc 的代码生成"）；② 避免 enum 运行时对象开销（此处无关紧要）。
**成本**：小（1 个文件）。**风险**：低。**但收益是"未来"的，建议与 TS 7 升级绑一起做，不单独做。**

### 4.3 TS 6 里**现在**就能用的新能力

| 能力 | 本仓库落点 | 评估 |
|---|---|---|
| **`es2025` target/lib**（新增 `RegExp.escape`、`Promise.try`、Iterator methods、Set methods） | `tsconfig.json:6` 的 `lib: ["ES2022", ...]` 可提到 `ES2025` | **有价值**：Set 方法（`union`/`intersection`/`difference`）在 `utils/sessionCache.ts` 的 `Set<() => void>` 场景可用；Iterator helpers 在 `utils/downsample.ts` 的索引遍历里可读性更好。**但这只是"能用新 API"，不是"修 bug"。** 实测：把 lib 提到 `ES2024` 后 `findLast` / `Promise.withResolvers` 都能通过（当前 `ES2022` 下报 `TS2550`）。 |
| **`findLast` / `findLastIndex`**（ES2023） | 当前**用不了**：`lib: ES2022` ⇒ 实测报 `TS2550: Property 'findLast' does not exist on type 'number[]'... Try changing the 'lib' compiler option to 'es2023'` | 仓库有 **15+ 处**手写 `arr[arr.length - 1]`（`composables/useRealtimeData.ts:106`、`utils/downsample.ts:33,43,53,113,233` 等）。这些**不是** `findLast` 场景（是"取最后元素"，`at(-1)` 才对，且 ES2022 就有）。**所以提 lib 的收益主要是"未来不用查降级写法"，不是改现有代码。** |
| **`Promise.withResolvers`**（ES2024） | 当前用不了（同上） | 仓库 0 处手写 resolve/reject 捕获模式 ⇒ **无落点**。 |
| **Temporal 类型**（需 `lib: esnext` 或 `esnext.temporal`） | 21 处 `toLocaleString`/`toLocaleDateString` | **不适用**：Temporal 是 runtime API，浏览器支持面窄；时间格式化已走 `utils/format.ts` 自封装。**别为类型好看引入不可用的 runtime API。** |
| **`Map.getOrInsert` / `getOrInsertComputed`**（esnext lib） | `api/parser.ts:72`、`components/channel/ChannelTerminal.vue:289`、`components/periph/PeripheralControl.vue:125` 等 12+ 处 `new Map` | 有落点但**必须确认 runtime 支持**。EHome 是自用系统、浏览器可控，**可作长期方向，不建议现在做**。 |
| **`--stableTypeOrdering`** | — | **不要开**。官方明说"最高 25% 类型检查减速"，只在 6→7 迁移对比时用。我们 typecheck 已 13.5s，不能再慢。 |
| **ts5to6 codemod**（调 `baseUrl`/`rootDir`） | 没用 `baseUrl`，`noEmit` 无 rootDir 问题 | **不需要**。 |

### 4.4 一个**不该忽略**的 TS 6 事实：typecheck 占了 build 的 64%

`vue-tsc 3.3.13` 跑 101 个 `.vue` + 295 个 `.ts` 要 **13.2–13.9s**。不是 TS 6 变慢，而是：

- `vue-tsc` 要在 tsc 之上跑 Volar 的 SFC 虚拟文件生成（`@vue/language-core@3.3.13`）；
- `tsconfig.json` 没有 `incremental` / `composite` / project references；
- 每次 `pnpm build` 都从零全量检查。

**可选优化（成本从低到高）**：
1. **拆分脚本**（成本小）：加 `"build:only": "vite build"`，保留 `"build": "pnpm typecheck && vite build"` 作 CI 门禁。开发期迭代 **21s → ~4.5s**。**必须确保 CI 仍跑全量**，否则等于自废 `noUnusedLocals`/`noUnusedParameters` 门禁（`tsconfig.json:18-19`）—— 注意这个门禁刚在 `components/forms/OTAForm.vue:271` 抓到过一个真实缺陷。
2. **`tsc --incremental` + `tsBuildInfoFile`**（成本小）：`noEmit: true` 与 `incremental` 在 TS 6 可共存。未实测（避免改配置），预期增量命中时 13.5s → 3-5s。
3. **TS 7（tsgo）**：官方称 10×，但 `vue-tsc` 依赖 tsc API（`vue-tsc@3.3.13` 的 deps 是 `@volar/typescript + @vue/language-core`，绑定 TS 编译器 API），**目前不能换**。

---

## 5. vitest 5.0.5 / happy-dom 21.0.4 / test-utils 2.5.1 / playwright 1.64.1

### 5.1 vitest 5：**最大实测收益是 pool 选择，不是 browser mode**

官方来源：<https://vitest.dev/blog/vitest-5.html>

**实测（本仓库 193 个 spec / 2351 个 test）**：

| 配置 | 耗时 | 结果 |
|---|---|---|
| 默认（forks + isolation） | **109.5 s**（另一轮 343 s，抖动极大） | 1 failed（`src/views/auth/__tests__/LoginAuthState.spec.ts:71` 5s 超时）/ 2349 passed |
| `--pool=vmThreads` | **23.27 s（4.7×）** | 3 failed（`src/views/edge-device/__tests__/EdgeDeviceDetailRouter.spec.ts`）/ 2347 passed |

**vitest 5 自己给出的诊断（5.0 新增 "performance hints by default"），实测输出原文**：
~~~
Environment  happy-dom was created 193 times · 1392.39s total, 39% of tracked time
  create it once per worker with pool: 'vmThreads' (keeps per-file isolation)
  or isolate: false (shares it across files)
~~~
→ **vitest 5 主动指出了我们的问题**：每个 spec 文件都新建一次 happy-dom（193 次），环境创建占总测量时间 39%（另一轮 44%）。

**关键事实**：193 个 spec 里，**89 个完全不需要 DOM**（口径：不 `import '@vue/test-utils'`、不出现 `document.`/`window.`/`$el`）。这些纯逻辑测试（`utils/`、`stores/` 的 mock 测试、`styles/` 的扫描门禁）在 happy-dom 里跑纯属浪费。

**建议（按收益/成本排序）**：

| 动作 | 成本 | 预期收益 | 风险 |
|---|---|---|---|
| **① 用 `test.projects` 把 89 个 DOM-free 测试分到 `environment: 'node'`** | 中 | 环境创建 193 → ~104 次；粗估 −30~40% 总时长 | 低（但 `vitest.config.ts:11` 的 `include` 要改成分组） |
| **② 试 `pool: 'vmThreads'`** | 小（改 1 行） | **4.7×**（109.5s → 23.3s） | **中**：实测 3 个 `EdgeDeviceDetailRouter.spec.ts` 用例失败 —— **必须先查清是"测试依赖隔离"还是"暴露真实 bug"**。不能为提速而跳过 |
| **③ `isolate: false`** | 小 | 中等 | 中-高：模块级副作用会串（7 处 store 模块级 `registerSessionCacheClearer` 调用 + `utils/sessionCache.ts` 模块级 `sessionGeneration` 计数器）⇒ **隔离假设被打破风险高，不建议** |
| ④ 新 API `vi.when(spy).calledWith(x).thenResolve(...)` | 小 | 可替代 **104 处** `mockImplementation` 中"按参数分流"的写法（如 `api/__tests__/parserTreeAddressCapability.spec.ts:59,78`） | 低 |

**vitest 5 其它变更对本仓库影响**：

| 变更 | 影响 |
|---|---|
| 未 await 的 `.resolves`/`.rejects` 现在**失败**（之前只警告） | 实测扫描：0 处裸 `.resolves`/`.rejects` ⇒ **无影响**。这是好事（假绿来源被堵） |
| locator 默认 strict | 不用 vitest browser mode 的 locator ⇒ 无影响 |
| benchmark API 重写（`bench` 从顶层导入变 fixture） | 0 个 `.bench.ts` ⇒ 无影响 |
| 需要 Node >= 22.12 / Vite >= 6.4 | 满足 |

**vitest browser mode（4.0 稳定、5.0 增强）—— 评估后建议不采用**：
- 诱因：`src/test-setup.ts` 用了**巨型手工 stub 链**（`ElButton`/`ElInput`/`ElSelect`/`ElOption`/`ElCheckbox`/`ElTag`… 全部 `defineComponent` 手写，因为 `vitest.config.ts` 的 `vue()` 插件没挂 `unplugin-vue-components`，SFC 里的 `<el-tag>` 被当成原生元素），是明显技术债。
- **但** browser mode 需 Playwright provider + 真浏览器，本仓库已有独立 `@playwright/test@1.64.1` + `e2e/` 12 个 spec + `playwright.config.ts` 的 3 个 project 矩阵（含移动端 `hasTouch`）。再引入等于**第二套浏览器测试栈**。
- **结论：不引入。** 真正的债务是"测试环境缺 unplugin-vue-components"，修法是在 `vitest.config.ts` 的 `vue()` 旁补 `Components({ resolvers: [ElementPlusResolver()] })` 让 stub 链自然消失 —— 但那会让每个测试真加载 element-plus，**可能更慢**。**标注为"待决策，非升级带来的能力"。**

### 5.2 happy-dom 21.0.4

**未能核实**具体新增 API（检索未得到可引用的官方 changelog 条目；anysearch 对该包查询返回的是无关结果）。**不编造。**

从使用面看：本仓库对 happy-dom 的依赖仅限 `vitest.config.ts:8` 的 `environment: 'happy-dom'` + `src/test-setup.ts` 的 DOM 垫片，没有直接调用 happy-dom API。**所以 20→21 的变更对我们几乎不可能有影响**（实测 2349 passed 印证）。

### 5.3 @vue/test-utils 2.5.1

官方来源：<https://github.com/vuejs/test-utils/releases>

**唯一一条破坏性变更**：2.5.0 起**不再支持 class components**。
**核实**：`grep -rn 'extends Vue'` = **0 处** ⇒ **完全无影响**。（`defineComponent({...})` 用了 118 处，这是 Composition/Options API 的写法，不是 class component，不受影响。）

### 5.4 @playwright/test 1.64.1

**未能核实**具体新特性（未检索到 1.64 的可引用 release notes 正文）。已核实的现况：

- `playwright.config.ts` 3 个 project（桌面 1440×900 + 移动端矩阵），`e2e/` **12 个 spec、2398 行**，最大的是 `uiux-regression.spec.ts`（1037 行）。
- `launchOptions.executablePath: '/snap/bin/chromium'` —— **本项目环境耦合点**：非 snap 环境（如 CI 容器）会启动失败。既有风险，与升级无关，但值得升级后复验 CI 是否真跑了 e2e（而非被跳过）。
- 我尝试用 Playwright 起 Chromium 做"真实浏览器首屏字节测量"，**失败**：`chromium failed: cannot create transient scope: DBus error ... refusing`（当前会话无 DBus/display）。**因此 §3.3 的首屏数字来自 dist/index.html 的 preload 清单 + 逐文件 gzip，不是浏览器实测 transfer size。** 差异在于 brotli（服务器若开 brotli 会比 gzip 小 15-20%），但**比例结论不变**。**不能用"实测浏览器"措辞。**

---

## 6. 不适用项汇总（含原因）

| 特性 | 归属 | 不适用原因 |
|---|---|---|
| SSR Lazy Hydration（`hydrateOnVisible` 等 4 种策略） | Vue 3.5 | 纯 SPA，无 SSR 入口（`main.ts` 直接 `createApp().mount('#app')`） |
| `data-allow-mismatch` | Vue 3.5 | 无 hydration，无 mismatch 概念 |
| Custom Elements 改进（`useHost`/`useShadowRoot`/`shadowRoot:false`/`nonce`） | Vue 3.5 | 0 处 `defineCustomElement` |
| deferred `<Teleport defer>` | Vue 3.5 | 0 个手写 `<Teleport>`；body 传送全部由 el-dialog/el-drawer 内部完成 |
| Vue Router 文件路由（unplugin 并入核心） | Router 5 | 手写路由表 + `uiuxRouteCoverage.spec.ts` fail-closed 门禁只认 2 种 component 写法，改形态要重写门禁 |
| Router Data Loaders（`defineBasicLoader` 等） | Router 5（experimental） | 需 experimental router；且会把取数从 composable 迁到路由层，与现有 `useDeviceData`/`useHistoryData` 体系冲突 |
| `useScrollRestoration` / `onRouteRendered` | Router 5.4（experimental） | **实测主入口无此导出**（tsc TS2305），只能从 `vue-router/experimental` 引 |
| `createRouter` 预构建 matcher | Router 5.4 | 官方标 `@internal for Nuxt only` + `@deprecated` |
| `TypesConfig` 覆盖 `useRouter()` 类型 | Router 5.1 | 官方 `Router: typeof router` 写法在本仓库形态下自引用报 `TS2502`（实测） |
| Pinia `getStores()` | Pinia 4.1 | 现有"7 处显式 `registerSessionCacheClearer`"比"遍历已实例化 store"更严格（后者漏未实例化的 store） |
| Vite Environment API | Vite 8 | 单 SPA、无 SSR/worker/多环境 |
| `build.rollupOptions.watch.chokidar` 迁移 | Vite 8 | 未使用 watch 配置 |
| `esmExternalRequirePlugin` | Vite 8 | 无 externalized `require` |
| 静态资源 `import.meta.url` 行为变化 | Vite 8 | 未用 UMD/IIFE 输出 |
| `--stableTypeOrdering` | TS 6 | 官方称会慢最高 25%，我们 typecheck 已 13.5s |
| Temporal 类型 | TS 6 | runtime 未可用；时间格式化已走 `utils/format.ts` |
| `getOrInsert`/`getOrInsertComputed` | TS 6（esnext lib） | runtime 支持面窄，12+ 处 Map 场景先不动 |
| vitest browser mode | vitest 5 | 已有独立 Playwright e2e 栈；引入等于第二套浏览器栈 |
| happy-dom 21 新 API | happy-dom | **未能核实**（无可靠官方 changelog 检索结果）；使用面极浅，无影响 |

---

## 7. 按收益排序的 TOP 5 建议动作

### TOP 1｜删掉 `manualChunks` 里的 element-plus 分组 —— 首屏 gzip −203 KB（−67%）

**动作**：`frontend-shared/vite.config.ts:116` 删除 `if (id.includes('node_modules/element-plus/')) return 'element'`；同时把 :111 的 `rollupOptions` 改为 `rolldownOptions`。
**收益**：首屏 gzip **301.5 KB → 98.4 KB**（实测 5 种方案，§3.3）；首屏 raw 1105.8 → 284.9 KB。登录页不再下载全部 element-plus。
**成本**：小（改 1-2 行）。
**风险**：中。必须回归 `e2e/uiux-regression.spec.ts`（1037 行）+ 其余 11 个 e2e spec；分包变化改 chunk 加载时序，`RouteProgressBar` 出现频率会变高（视觉变化）。
**验证口径**：① `grep modulepreload dist/index.html` 无 element；② 真实浏览器 DevTools 看登录页 transfer size；③ e2e 全绿。

### TOP 2｜把 `pnpm build` 拆开：typecheck 13.5s 不该阻塞每次迭代

**动作**：`package.json` 增加 `"build:only": "vite build"`，保留 `"build": "pnpm typecheck && vite build"` 作为 CI 门禁；或给 `tsconfig.json` 加 `"incremental": true` + `"tsBuildInfoFile"`。
**收益**：本地迭代 **20.99s → ~4.5s**（−78%）；增量 typecheck 命中时预计 13.5s → 3-5s。
**成本**：小。
**风险**：低。**必须确保 CI 仍跑全量 build**，否则自废 `noUnusedLocals`/`noUnusedParameters`（`tsconfig.json:18-19`）—— 该门禁刚在 `components/forms/OTAForm.vue:271` 抓到真实缺陷。

### TOP 3｜`useTemplateRef` 替换 39 处 + 修 2 处孤儿 ref

**动作**：按 §1.1 清单逐文件替换；优先修 `components/common/CountUp.vue:2`（`numberRef` 无对应变量）与 `views/firmware/FirmwareManage.vue:32`（`tableRef` 无对应变量，且"取消选择"没清表格勾选态）。
**收益**：39 处样板收敛；2 处静默失效显性化；获 vue-tsc 对 ref 名的静态检查；`FirmwareManage` 的"取消选择"可真正 `clearSelection()`（当前 UI 不一致）。
**成本**：小（机械替换）。
**风险**：低。唯一注意 `views/layout/MainLayout.vue:618/696/697` 的 `ref<any>` 需补真实类型。

### TOP 4｜`defineModel` 收敛 11 处 v-model 三件套（~104 行）

**动作**：按 §1.2 表替换。**顺序**：先做单 model 的 9 处（ChannelManager / QuickCreateDeviceDialog / PeripheralConfigDialog / *Dialog 的 visible），**最后**做双 model 的 `components/charts/TimeRangeSelector.vue:36-49` 与 `components/node/LogRealtimeViewer.vue:146-147`。
**收益**：删 ~104 行样板；消除手写 computed 转发与 normalize 不一致的隐患。
**成本**：小。
**风险**：低-中。**必查**：原写 `required: true` 的 prop 必须在 `defineModel` 里补，否则从"缺传警告"退化为"静默 undefined"；双 model 的第二个参数名必须与父组件 `v-model:xxx` 完全一致。

### TOP 5｜vitest 提速：先分组 DOM-free 测试，再评估 `vmThreads`

**动作**：① 用 `test.projects` 把 89 个 DOM-free spec（`utils/`、`stores/`、`styles/` 的扫描门禁等）分到 `environment: 'node'`；② 试验 `pool: 'vmThreads'`（实测 109.5s → 23.27s）。
**收益**：本地/CI 反馈速度提升数倍（当前 `vitest run` 109-343s，比 `pnpm build` 慢一个数量级）。
**成本**：中。
**风险**：中。**必须先查清 `--pool=vmThreads` 下 `EdgeDeviceDetailRouter.spec.ts` 的 3 个失败**是测试依赖隔离还是暴露真实缺陷。**查清前不要切换。** 另：`isolate: false` 明确不建议。

---

## 8. 附：两点"负面情报"（必须知道，避免误判）

1. **本次实测的测试套件不是"一次全绿"**：我跑了 4 轮 vitest，失败数分别是 1 / 6 / 3 / 5（不同轮）。失败集中在 **`src/views/auth/__tests__/LoginAuthState.spec.ts:71`（5s 超时）** 与 `--pool=vmThreads` 下的 `src/views/edge-device/__tests__/EdgeDeviceDetailRouter.spec.ts`。
   - `LoginAuthState.spec.ts:71` 是**不稳定的**（同配置有时过有时不过），属**测试自身时序脆弱**，不是升级回归。
   - 但这意味着"2342 测试全绿"这个结论**依赖运行环境**。**建议 Lead 收尾时用固定命令再跑一次并记录**（如 `vitest run --pool=forks --retry=1`），把 flaky 单独登记，不要笼统写成"全绿"。
2. **首屏体积数字的来源必须写清**：§3.3 的 301.5 KB / 98.4 KB 是**构建产物逐文件 gzip 求和**（`index.html` 的 `modulepreload` 清单），**不是浏览器真实 transfer**。用 Playwright 起 Chromium 实测被环境拒绝（DBus/无 display：`cannot create transient scope`）。若服务器启用 brotli，绝对值都小 15-20%，**但 67% 的比例结论不变**。

---

## 9. 参考来源

- Vue 3.5 发布公告：<https://blog.vuejs.org/posts/vue-3-5>
- Vue Router 5 迁移指南：<https://router.vuejs.org/guide/migration/v4-to-v5.html>
- Vue Router CHANGELOG（5.1–5.4）：<https://github.com/vuejs/router/blob/main/packages/router/CHANGELOG.md>
- Router Scroll Behavior：<https://router.vuejs.org/guide/advanced/scroll-behavior.html>
- Pinia v4.1.0 release：<https://github.com/vuejs/pinia/releases/tag/v4.1.0>
- Vite 8 迁移指南（v7 → v8）：<https://vite.dev/guide/migration>
- TypeScript 6.0 Release Notes：<https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html>
- Vitest 5 发布公告：<https://vitest.dev/blog/vitest-5.html>
- Element Plus CHANGELOG：<https://github.com/element-plus/element-plus/blob/dev/CHANGELOG.en-US.md>
- test-utils releases（class component 移除）：<https://github.com/vuejs/test-utils/releases>
- 本仓库内实测数据：§3.1/§3.3 的构建与体积数据为本次会话实测（实验产物已清理，见文首说明）。**复现方式**：复制 `vite.config.ts`，分别按 §3.3 表 B/C/D/E 四种方案修改 build 段，各跑一次 `vite build`，再对 `dist/index.html` 中所有 `<link ... href="/assets/*.js|*.css">` 逐文件 `gzip -c` 求和。
