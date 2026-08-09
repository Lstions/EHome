# BMS 移动端截图缺陷分析报告（2026-08-09）

- 输入：`screenshots/mobile/bms/` 5 张实机截图（22.16.03 / .20 / .26 / .29 / .36），覆盖 BMS 详情页首屏 → 历史趋势/温度/MOS → 保护状态/折叠行/历史数据头部 → 实时数据流/指令频率配置展开态。
- 方法：按 `ehome-ui-shared-component-fix` skill 闭环 —— 视觉逐张解析 → 源码取证（行号）→ 数学证伪（不靠目测）→ 根因归并 4 类 → 分级修复方向（本文只设计不实现）。
- 视口基准：截图图像宽约 1080px，对应 CSS 视口 ≈ 390px（缩放系数 ≈ 2.77）。下文所有像素估算均已按此校准。

---

## 一、缺陷清单（截图表象 → 源码证据 → 判定）

### P0 — 语义误导（数据一致性问题，最高优先级）

| # | 表象 | 源码证据 | 判定 |
|---|------|----------|------|
| 1 | 「保护状态」卡头部挂绿色徽章**「全部正常」**，卡主体却是空态**「无保护状态数据」**，自相矛盾（截图 .26 / .29） | `BmsDetailPage.vue:113-114`：`<el-tag v-if="hasActiveProtection">有保护触发</el-tag> <el-tag v-else-if="latestData">全部正常</el-tag>`。只要 `latestData` 对象存在（哪怕没有任何 protection 字段）就判「全部正常」；而 `BmsProtectionGrid.vue:47-50` 的 `hasProtectionData` 要求 `protection_status !== undefined` 才认有数据 → 两处判定标准不一致 | **逻辑 bug**：无数据 ≠ 正常。页面级徽章与网格级空态对「有无数据」的定义分裂 |
| 2 | 「MOS状态」在**无 FET 数据**时仍显示「充电MOS OFF」「放电MOS OFF」+「无FET状态数据」三卡（截图 .26） | `BmsMosStatus.vue:64-75`：`chargeOn`/`dischargeOn` 在 `data` 缺字段时 fallback 为 `false` → 渲染成 OFF；`:34-39` 又追加「无FET状态数据」占位卡 | **语义 bug**：把「未知」伪造为「OFF」。无数据时应只显示中性占位，不应给出确定的 OFF 结论 |
| 3 | 设备头「在线/实时连接」绿色徽章与全部指标 `--`、最后数据时间 `-` 并存（截图 .03） | `DeviceHeader.vue:6-9` wsConnected 标签；`DeviceInfoCard` 健康状态取 device.status | 低危：设备在线 ≠ 有数据。建议空数据时给「已连接·暂无数据」中性提示，属语义优化非 bug |

### P1 — 移动端可用性（容器感知缺失，数学上必然发生）

| # | 表象 | 数学证伪 | 源码证据 |
|---|------|----------|----------|
| 4 | 「历史数据」头部挤爆：标题拆成「历史数/据」；1小时/24小时/7天/自定义 折成 2×2 且上下压边重叠；「1小时」撞标题（截图 .26 / .29） | 可用宽 = 390 − 20(main padding)×2 − 20(el-card body)×2 ≈ **310px**；标题 4 字 ≈ 64px → 控件区只剩 246px。而 TimeRangeSelector 4 按钮 ≈ 264px + 导出CSV ≈ 100px + margin 10px ≈ **374px > 246px** ⇒ 重叠必然 | `HistoryChartSection.vue:4` 头部 `display:flex; justify-content:space-between` **无 flex-wrap**；`:347` `.header-controls { display:flex; align-items:center; }` 同样**无 flex-wrap**。`TimeRangeSelector.vue:67-79` 的 ≤480px 换行媒体查询写了，但被外层不换行的头部废掉 |
| 5 | 「电芯电压历史趋势」标题孤字拆行「…趋/势」，「自定义」按钮掉行（截图 .20） | 标题 8 字 ≈ 128px + 按钮组 264px = **392px > 310px** ⇒ 标题被压缩拆行必然 | `BmsCellVoltageHistoryChart.vue:4` 同款内联 flex 头部，无 wrap —— 与 #4 是**同一模式的重造** |
| 6 | 「指令频率配置」行：指令名/读徽章/十六进制码挤压，输入框贴边，长行溢出风险（截图 .36） | `.cmd-info` 固定 **180px** + `.cmd-controls` 固定 **200px** + gap ≈ **396px > 330px**（390 − 卡片/折叠内边距），且 `flex-wrap: nowrap` + `overflow:hidden` ⇒ 移动端裁剪必然 | `CommandList.vue:126-132`：两处固定宽度 + nowrap。视觉报告称「输入框无单位」系**误判**：`CommandList.vue:21` 有 `<span class="interval-unit">ms</span>`，真实问题是 11px 灰字对比度过低 |
| 7 | 2×2 指标卡**垂直间距≈0**（上下卡几乎粘连），水平间距却 20px（截图 .03） | Element Plus `el-row :gutter` 只产生水平 padding，换行后纵向无 gap；`.metric-card` 无 margin-bottom ⇒ 垂直间隙 = 0 | `BmsDetailPage.vue:22` `<el-row :gutter="20">` + `:xs="12"` 换行两行；`MetricStatCard.vue` 无纵向间距补偿 |

### P2 — 空态契约 / 等高 / 细节

| # | 表象 | 源码证据 | 判定 |
|---|------|----------|------|
| 8 | 空态卡对齐不一致：「电芯电压历史趋势」空态**居中**，「温度探头」空态**靠左**（截图 .20） | `BmsDetailPage.vue:268` `.temp-list { display:flex; flex-direction: row; }` —— el-empty 被塞进行向 flex 容器变成左对齐子项；其他卡 el-empty 默认居中 | 缺共享空态契约：空态渲染位置依赖各容器布局，风格漂移 |
| 9 | 空态卡过高：4 张空态卡（历史趋势/温度/保护/历史数据）堆出 ≈1100px 无效滚动 | 校准后单卡 ≈ 250–290 CSS px（el-empty 默认 padding 40px 0 + image 60 + 卡头 56），非渲染异常但移动端不紧凑 | 缺移动端 compact 空态契约 |
| 10 | SOC 无数据时渲染**空进度条**，易误读为 0%；`--` 用主文本深色；`/ --Ah` 斜杠行首拆行；单位空格不统一（`-- Ah` vs `/ --Ah`）（截图 .03） | `BmsDetailPage.vue:29` 无数据时 `progress=0` 仍渲染；`MetricStatCard.vue:148-157` `.metric-value` 无占位态；`:40` subText 拼接无空格、`.metric-sub` 无 nowrap | 占位态契约缺失 |
| 11 | 「实时数据流」仅 1 条数据时，下方大块空白边框区；数据行内容只有占位「–」（截图 .36） | `RealtimeDataList.vue:133/222` 移动端 `min-height:200px`，RecycleScroller 需要固定视口高 → 1 条(64px) + 136px 空白 | 虚拟滚动约束，可按条数收敛高度；「–」需查 `formatDataPlainText` 对该帧的解析（数据问题，非纯 UI） |
| 12 | 「导出CSV」禁用态浅蓝与选中态蓝同色系，难分辨（截图 .26） | `HistoryChartSection.vue:12-20` disabled 的 `type="primary"` 按钮 | 禁用态应灰化或隐藏 |
| 13 | MOS 第三卡「无FET状态数据」独占半行，网格残缺（截图 .26） | `BmsMosStatus.vue:114-119` 移动端 2 列 grid + 3 子项 | 随 #2 修复（无数据时不渲染 OFF 双卡）自然消除 |

### P3 — 存疑/需 CDP 复测（不目测定论）

| # | 表象 | 存疑原因 |
|---|------|----------|
| 14 | 「实时数据流」「指令频率配置」折叠行被指「贴屏幕左缘、左圆角缺失」（两张截图均报） | `MainLayout.vue:815-820` `.main-content { padding: 20px }` 无移动端归零媒体查询，el-collapse 理论上应内缩 20px。**疑似长截图拼接错位**，需 CDP 实测 collapse 行 getBoundingClientRect.left |
| 15 | 顶部绿色状态胶囊无文字 | `MainLayout.vue:929-931` ≤768px 主动隐藏 `.status-text` —— 是既有设计决策，非缺陷；是否恢复文字待少爷拍板 |

---

## 二、根因归并（skill 四分类）

**根因 A：卡片头部无「换行降级契约」（容器感知缺失）—— #4 #5 #6**
三处头部（HistoryChartSection / BmsCellVoltageHistoryChart / CommandList）各自用内联 flex 或固定宽度硬扛窄屏。TimeRangeSelector 内部已有 ≤480px 降级，但外层头部不 wrap，降级被废。这是典型的「每页重造头部、缺共享组件」。

**根因 B：空态无共享契约（等高/对齐/紧凑度漂移）—— #8 #9 #11**
el-empty 被直接塞进各种容器（row flex / grid / card body），对齐与高度随容器漂移；移动端无 compact 模式，空态卡占用 ≈ 1/3 屏。

**根因 C：「无数据 vs 正常」语义契约缺失 —— #1 #2 #3 #10**
四个组件各自决定「没数据时显示什么」：页面徽章说正常、网格说无数据、MOS 伪造 OFF、进度条伪装 0%。与少爷心算校验习惯直接冲突（绿=正常必须有数据支撑）。

**根因 D：防回退围栏缺口 —— 全部**
`BmsMobileResponsive.spec.ts` 只覆盖了电芯图标签降级；头部 wrap 契约、空态契约、无数据语义契约均无 `?raw` 源码测试保护，下次 rebase 可无声回退。

---

## 三、修复方向（只设计，待少爷授权再实现）

1. **共享 CardSectionHeader 组件**（杠杆最高，治 #4 #5）：标题 `white-space:nowrap; flex-shrink:0`，控件槽 `flex-wrap:wrap` 且窄屏整行下移；HistoryChartSection 与 BmsCellVoltageHistoryChart 统一改用。
2. **共享 EmptyState 契约**（治 #8 #9）：统一居中 + 移动端 compact（image ≤48、padding 收窄，单卡 ≤160px）；温度探头卡空态移出 row flex 容器。
3. **无数据语义收敛**（治 #1 #2 #10，P0）：
   - `BmsDetailPage.vue:114` 改为仅当 `hasProtectionData`（protection_status 字段存在）才显示「全部正常」，无数据时显示 info 色「暂无保护数据」或不显示；
   - `BmsMosStatus` 在 `!hasFetStatus` 时只渲染中性占位卡，不渲染 OFF 双卡；
   - `MetricStatCard` value 为 `--` 时：隐藏 progress、占位符用 placeholder 浅色。
4. **CommandList 容器感知**（治 #6）：移动端 `.cmd-info`/`.cmd-controls` 解除固定宽度改 `flex-wrap`，或行内两段式（上行名称、下行控件）。
5. **指标网格纵向 gap**（治 #7）：el-row 加 `row-gap`（或 col 移动端 margin-bottom），与横向 gutter 对齐。
6. **围栏**：为 1/2/3/4/5 各补 `?raw` 契约测试（断言头部含 flex-wrap、空态居中契约、`v-else-if="latestData"` 不再出现等），挂进现有 vitest。
7. **验证**：纯前端视觉改动，走 skill 免后端闭环 —— 扩展 `/dev/mock-bms`（现有 `MockBmsPanel.vue` 只覆盖指标区，需加空态/头部场景）→ CDP 390×844 移动视口 + 禁缓存 → getBoundingClientRect 量化头部级宽、空态卡高、纵向 gap → 修复前后数据对比。

## 四、视觉误判更正（诚实记录）

- 「指令频率输入框无单位」：误判，`CommandList.vue:21` 有 ms 单位，真问题是 11px 灰字对比度。
- 「空态卡 700–800px」：按截图缩放系数 ≈2.77 校准后为 250–290 CSS px，非渲染异常，属「不够紧凑」而非「高度失控」。
