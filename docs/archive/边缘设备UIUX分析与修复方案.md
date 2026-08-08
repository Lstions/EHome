# EHome System 边缘设备 UI/UX 分析与修复方案

> 编写时间：2026-07-19
> 项目路径：`/home/sun/workspace/EHomeSystem`
> 前端路径：`frontend-shared`
> 状态：列表页 + 通用/BMS/逆变器详情页分析与量化已完成；尚未实施代码修改

---

## 1. 审查范围与方法

### 1.1 已审查文件

| 文件 | 说明 |
|------|------|
| `frontend-shared/src/views/edge-device/EdgeDeviceList.vue` | 边缘设备列表页（统计卡、工具栏、设备卡片） |
| `frontend-shared/src/views/edge-device/GenericDeviceDetail.vue` | 通用设备详情页 |
| `frontend-shared/src/views/edge-device/bms/BmsDetailPage.vue` | BMS 详情页（总电压、实时数据、指令频率） |
| `frontend-shared/src/views/edge-device/inverter/InverterDetailPage.vue` | 逆变器详情页 |
| `frontend-shared/src/views/edge-device/shared/DeviceHeader.vue` | 设备头部组件（实时连接、操作按钮） |
| `frontend-shared/src/views/edge-device/shared/CommandFrequencySection.vue` | 指令频率展示组件 |
| `frontend-shared/src/components/data/RealtimeDataList.vue` | 实时数据列表组件 |
| `frontend-shared/src/components/device/CommandList.vue` | 指令列表组件 |
| `frontend-shared/src/components/common/PageHeader.vue` | 通用页面头部 |
| `frontend-shared/src/views/edge-device/shared/HistoryChartSection.vue` | 历史图表区域 |
| `frontend-shared/src/composables/useDeviceData.ts` | 设备数据逻辑 |

### 1.2 验证方法
- 启动开发环境：`docker compose --env-file .env.dev -f docker-compose.dev.yml -p ehome-dev`（PG 5435 / Redis 6380 / EMQX 1884 / 后端 8082 / 前端 5174）
- 通过 CDP 端口 18800 启动 headless Chromium（`--window-size=1400,1000`）
- 通过 `/api/v1/auth/login` 登录并访问 `/edge-device`
- 使用 CDP `Runtime.evaluate` 获取 DOM bounding box，使用 `Page.captureScreenshot` 截图，并通过 `vision_analyze` 进行可视化确认

### 1.3 可视化验证状态
- 后端、前端、Chromium CDP 服务均已确认可用。
- CDP 服务初始化流程已完成（`bootstrap-database` → `create-initialization-token` → `/api/v1/auth/initialize` → `/api/v1/auth/login`），admin 用户已创建。
- 已成功获取 1400px 和 1100px 视口下边缘设备列表页的 DOM bounding box 量化数据，详见第 7 节。
- 截图证据保存路径：
  - 1400px：`/tmp/edge-device-list-1400.png`
  - 1100px：`/tmp/edge-device-list-1100-ws.png` / `/tmp/edge-device-list-1100v2.png`
  - 1100px 工具栏裁剪：`/tmp/toolbar-1100-ws.png`
  - 1100px 统计卡裁剪：`/tmp/statcards-1100-ws.png`

---

## 2. 发现的问题

### 2.1 边缘设备列表页

| 位置 | 问题 | 源码证据 | 量化验证 |
|------|------|---------|----------|
| **统计卡片区** | 4 张卡高度一致，但在 1100px 视口下会在工具栏上方产生较大的垂直占位，且 1400px 和 1100px 之间布局跳跃明显 | `.stat-card` 使用 flex 行布局，未设置统一 `min-height` 与断点 | 1400px：4 张卡 278×106，单行；1100px：4 张卡 422×106，2×2 网格，尺寸一致 |
| **工具栏** | 1100px 视口下工具栏高度从 66px 膨胀到 98px，"创建边缘设备"按钮被挤到第二行，搜索框占位符被截断 | `.toolbar` 为单行 flex，未做响应式换行 | 1400px：1160×66，单行；1100px：860×98，2 行，第二行仅有创建按钮 |
| **设备卡片网格** | 当前无边缘设备时 `device-grid` 高度为 0，但缺少等宽网格约束 | `device-grid` 使用 flex 而非 CSS Grid，缺少最小宽度约束 | 1400px：1160×0；1100px：860×0 |

### 2.2 设备详情页（通用 / BMS / 逆变器）

| 位置 | 问题 | 源码证据 | 量化验证 |
|------|------|---------|----------|
| **实时连接头部** | tag 仅 ws 连接时显示导致宽度跳动；768px 操作按钮换行 3 层 | `DeviceHeader.vue` `size="large"` + `flex-wrap` | 1400：tag/button 高均 32；768：header 65→145 |
| **BMS 总电压指标卡** | 同排卡高度不一致，总电压最矮 | `BmsDetailPage.vue:57-69` 无 sub/progress | **90 vs 110**（差 20px） |
| **逆变器指标卡** | PV/负载无 sub，电池/电网有 sub | `InverterDetailPage.vue` metric 区 | **90 vs 108** |
| **指令频率区** | BMS 默认折叠；通用/逆变器展开 | `BmsDetailPage.vue:159-166,218` | collapses 实测默认 false |
| **历史图表头部** | 导出按钮硬编码 margin | `HistoryChartSection.vue:12-14` | header-controls 322×32 |

### 2.3 实时数据与指令列表组件

| 位置 | 问题 | 源码证据 | 量化验证 |
|------|------|---------|----------|
| **RealtimeDataList** | 虚拟列表 item-size=80，实际内容约 53，行高浪费 | `RealtimeDataList.vue:31` | data-item h≈53，容器 min-h 400 |
| **CommandList** | cmd-info 180 / cmd-controls 200 固定宽 | `CommandList.vue:148-152` | 1400 可容纳；768 item 454 余量小 |
| **DeviceControlPanel** | 操作历史时间线无截断 | `DeviceControlPanel.vue` timeline | Generic panel 高约 2990 |

---

## 3. 根因分析

1. **响应式策略缺失**：列表页统计卡、工具栏、`device-grid`、`CommandList` 没有针对 900–1200px 及以下的合理断点。
2. **组件高度/尺寸不统一**：`el-tag` 与 `el-button` 混排、指标卡内容行数不同。
3. **过度依赖固定宽度**：`CommandList`、部分卡片区使用固定 px 宽度，未做 `min-width:0` 和响应式断点。
4. **信息层级不统一**：实时数据、指令频率、操作按钮在不同设备类型页中的组织方式不一致。

---

## 4. 修复方案（按优先级排序）

### P0：列表页与工具栏响应式
**文件**：`frontend-shared/src/views/edge-device/EdgeDeviceList.vue`

1. 统计卡改为：
   ```css
   display: grid;
   grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
   gap: 16px;
   ```
   并统一 `min-height: 120px; align-items: flex-start;`。
2. 工具栏使用媒体查询：
   - `>1100px`：单行，搜索占剩余空间；
   - `900–1100px`：搜索独占一行，筛选+操作在第二行；
   - `<600px`：筛选折叠为下拉或按钮抽屉。
3. 设备卡片网格改为：
   ```css
   grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
   ```
   并统一 `min-height`，避免单张卡占满整行。

### P1：详情页头部与指标卡统一
**文件**：`frontend-shared/src/views/edge-device/shared/DeviceHeader.vue`、`BmsDetailPage.vue`、`InverterDetailPage.vue`

1. `DeviceHeader`：
   - 将“实时连接”标签与按钮统一尺寸：标签改 `size="default"` 或按钮加 `size="large"`。
   - 按钮组在 `<768px` 时改为垂直堆叠或折叠到“更多”菜单。
2. BMS/逆变器同排指标卡：
   - 统一卡片结构模板，每张卡至少包含 label + value + 辅助文本/占位。
   - 对“总电压”等无进度条的指标补充单位/状态文本，平衡视觉重心。

### P2：实时数据与指令列表密度
**文件**：`frontend-shared/src/components/data/RealtimeDataList.vue`、`frontend-shared/src/components/device/CommandList.vue`

1. `RealtimeDataList`：
   - 移除固定 80px 行高，改为 `min-height: 64px; padding: 12px 16px;`。
   - 增加工具栏与列表的间距。
2. `CommandList`：
   - 将固定列宽改为 flex 比例：
     ```css
     .cmd-info { flex: 1 1 40%; min-width: 0; }
     .cmd-controls { flex: 1 1 60%; min-width: 0; }
     ```
   - 在 `<768px` 下将每行指令改为垂直布局（名称/控制各占一行）。

### P3：指令频率与历史图表统一
**文件**：`frontend-shared/src/views/edge-device/shared/CommandFrequencySection.vue`、`BmsDetailPage.vue`、`HistoryChartSection.vue`

1. 统一指令频率区为独立卡片，不折叠；BMS 详情页移除 `el-collapse` 包裹。
2. `HistoryChartSection` 导出按钮的 `style="margin-left: 10px"` 改为 `margin-left: var(--spacing-sm, 8px)` 或 Element Plus 的 `ml-2` 类。

### P4：回归验证
1. 重新运行 `make lint`（注意 `make lint-frontend` 预存 exit 2 不视为新错误）。
2. 在 1400×1000、1280×800、768×1024 三种视口下使用 CDP 截图并测量关键元素 bounding box，确认无溢出/错位。
3. 检查 BMS 详情页、逆变器详情页、通用详情页三者布局一致性。

---

## 5. 列表页量化测量（已完成）

### 5.1 视口 1400×857（单行布局）

| 元素 | x | y | w | h | 结论 |
|------|---|---|---|---|------|
| stat-card ×4 | 220/514/808/1102 | 80 | 278 | 106 | 等宽等高，单行 4 卡 |
| toolbar | 220 | 206 | 1160 | 66 | 单行，未换行 |
| device-grid（空） | 220 | 292 | 1160 | 0 | 无设备时高度为 0 |

### 5.2 视口 1100×857（中等宽度）

| 元素 | x | y | w | h | 结论 |
|------|---|---|---|---|------|
| stat-card ×4 | 2×2 网格 | 80/202 | 422 | 106 | 卡本身仍等宽等高 |
| toolbar | 220 | 328 | 860 | **98** | **高度 66→98，确认换行** |

**工具栏视觉确认（裁剪 `/tmp/toolbar-1100-ws.png`）**：
- 第 1 行：搜索 + 3 个筛选 + 图标按钮
- 第 2 行：仅「创建边缘设备」按钮右对齐
- 搜索 placeholder 被截断（`搜索边缘设备名称、…`）

### 5.3 列表页结论修订
1. ~~统计卡高度不一致~~ → **不成立**。1400/1100 下卡高均为 106px。
2. **工具栏换行/挤压** → **成立（P0）**。1100px 高度 66→98。
3. 统计卡 2×2 本身可接受，但与工具栏换行叠加后，中等宽度信息密度下降。

---

## 6. 设备详情页分析（源码 + 运行时量化）

### 6.1 路由与页面分流

| 设备 type | 详情组件 | 路由 |
|-----------|----------|------|
| `jiabaida_bms` / `battery` | `bms/BmsDetailPage.vue` | `/edge-device/:id` via `EdgeDeviceDetailRouter.vue` |
| `inverter` | `inverter/InverterDetailPage.vue` | 同上 |
| 其他（如 `sn3001_rain`） | `GenericDeviceDetail.vue` | 同上 |

验证用设备：
- id=1 `sn3001_rain` → Generic
- id=2 `jiabaida_bms` → BMS（注入模拟 latest-data）
- id=3 `inverter` → 逆变器（注入模拟 latest-data）

### 6.2 通用详情页 `/edge-device/1`（1400×900）

| 区域 | 测量 | 问题 |
|------|------|------|
| page-header | 1152×65 | 正常 |
| header-actions | 433×32，按钮高 32 | **WS 断开时无「实时连接」tag**，仅编辑/同步/刷新/删除 |
| 基本信息卡 | 1152×264 | 正常 |
| 实时数据卡 | 1152×503；list 高 400；data-item 高 **53** | item-size 固定 80，实际内容高仅 53，**虚拟列表行高浪费** |
| 指令频率卡 | y=968，独立展开卡片 | 与 BMS 折叠组织不一致 |
| 受控操作 panel | y=1293，高度约 2990 | 历史时间线过长，**缺少折叠/分页/虚拟列表** |
| 历史数据 | y=4303，header-controls 322×32 | 导出按钮硬编码 margin-left:10px |

1100×900：header 仍单行 65px；实时数据宽 810。  
相对列表页，通用详情在 1100 下头部尚未明显崩坏。

### 6.3 BMS 详情页 `/edge-device/2`（1400×900）— 总电压问题已量化

**指标卡（核心问题）**：

| 卡 | label | value | 辅助内容 | w×h | 结论 |
|----|-------|-------|----------|-----|------|
| 0 | SOC | 78.5% | progress | 273×**99** | 有进度条 |
| 1 | 剩余容量 | 42.30Ah | `/ 54Ah` | 273×**110** | 有 sub |
| 2 | **总电压** | 53.214V | **无** | 273×**90** | **最矮，视觉塌陷** |
| 3 | 电流 | -12.345A | 放电中 | 273×**110** | 有 sub |

同排 4 卡宽度一致（273），高度差 **90 / 99 / 110**，总电压卡因缺少 `metric-sub`/占位，比最高卡矮 **20px**。

源码位置：`BmsDetailPage.vue:57-69`（总电压只有 label+value），对比 SOC 有 `el-progress`、剩余容量/电流有 `metric-sub`。

**头部实时连接**：
- actions 含 tag「实时连接」h=32、w=91，与 button h=32 **同高**（当前 Element Plus 下 large tag 实测也是 32）
- 源码仍写 `size="large"`（`DeviceHeader.vue:6`），与 button 默认尺寸混用，依赖主题巧合对齐，不稳

**信息架构不一致**：
- 实时数据流、指令频率配置默认 **折叠**（`activeCollapses = []`，`BmsDetailPage.vue:218`）
- 通用/逆变器则 **直接展开** 为独立卡片

**768×900 窄屏**：
- page-header 高 65→**145**，header-actions 换行 3 层（编辑/同步 → 刷新 → 删除）
- 指标卡变 2×2：总电压仍 90，其他 99/110，高度差保留
- command-item：infoW=180 + ctrlW=200，容器宽 454，**尚未 overflow**，但余量极小

展开指令频率后（BMS）：
- command-item：h=40, infoW=180, ctrlW=200（固定宽度仍在）
- 实时数据折叠未展开时 realtime box 为 0×0（DOM 存在但不可见）

### 6.4 逆变器详情页 `/edge-device/3`（1400×900）

| 卡 | label | value | sub | h |
|----|-------|-------|-----|---|
| 0 | PV输入 | 2.00kW | 无 | **90** |
| 1 | 电池 | 51.2V | 充电 5.5A | **108** |
| 2 | 电网 | 230V | 50.1Hz | **108** |
| 3 | 负载 | 1.50kW | 无 | **90** |

与 BMS 同类问题：**无 sub 的卡高 90，有 sub 的卡高 108**，同排不等高。

信息架构：
- 实时数据、指令频率、受控操作均为 **独立展开卡片**（不折叠）
- 顺序：指标 → 功率流 → 状态告警 → MPPT → 温度风扇 → 发电量 → **实时数据** → **历史** → **指令频率** → **受控操作**
- BMS 顺序不同，且后两项默认折叠 → **三端详情信息层级不统一**

### 6.5 详情页问题汇总

| ID | 严重度 | 问题 | 证据 |
|----|--------|------|------|
| D1 | P0 | BMS/逆变器同排 metric-card 高度不一致（总电压/PV/负载偏矮） | BMS 90 vs 110；逆变器 90 vs 108 |
| D2 | P1 | 三端详情信息架构不一致（折叠 vs 展开、区块顺序） | BMS collapse vs Generic/Inverter cards |
| D3 | P1 | 768px 头部操作按钮换行至 3 行，header 高 145 | 实测 acts y=94/134/178 |
| D4 | P2 | 实时连接 tag 仅在 wsConnected 时显示，断开时布局左右跳动 | Generic 页无 tag；BMS/Inv 有 tag |
| D5 | P2 | 受控操作历史时间线过长无上限展示 | Generic panel h≈2990 |
| D6 | P3 | HistoryChart 导出按钮硬编码 margin | `style="margin-left: 10px"` |

---

## 7. 组件级分析

### 7.1 DeviceHeader / PageHeader
- 文件：`shared/DeviceHeader.vue`、`components/common/PageHeader.vue`
- tag `size="large"` + button 默认；1400 下实测均 32px，但 768 下整组 wrap 严重
- `header-actions` / `header-actions-group` 均 `flex-wrap: wrap`，无「主操作固定 + 次要收入更多」策略
- PageHeader 仅在 `max-width:768` 做有限适配，**900–1200 无中间态**

### 7.2 RealtimeDataList
- 文件：`components/data/RealtimeDataList.vue`
- `RecycleScroller :item-size="80"`（L31），实测 data-item 内容高约 **53**
- 结果：每行下方空白约 27px，列表“空、疏”
- `min-height: 400` 在空数据/单条数据时仍占满 400，详情页纵向过长
- 建议：`item-size` 改为 56–64，或按 displayMode 动态；容器改 `min-height: 240` + `max-height`

### 7.3 CommandFrequencySection / CommandList
- `CommandFrequencySection` 仅包一层 el-card + `CommandList`
- `CommandList`：`.cmd-info{width:180px}` + `.cmd-controls{width:200px}` 固定宽
- 1400/BMS 展开：item 1086 宽，固定列可容纳
- 768：item 454 宽，180+200=380，仍勉强；再窄必挤
- `.command-item{flex-wrap:nowrap; overflow:hidden}` — 挤出时直接裁切而非换行
- BMS 空指令态文案与 Generic「该设备类型不支持」不一致（BMS 有模板；无模板设备文案分裂）

### 7.4 HistoryChartSection
- 导出按钮 `style="margin-left: 10px"`（L14）
- header-controls 单行 flex，窄屏可能与 TimeRangeSelector 拥挤（待 768 专项，当前 768 未切到历史区测量）

### 7.5 DeviceControlPanel
- 操作按钮 `flex-wrap:wrap` 尚可
- 时间线无虚拟滚动/分页，操作多时详情页极长（Generic 实测 ~2990px）

---

## 8. 修复方案补充（基于量化结果）

### P0-A：metric-card 等高（BMS + 逆变器）
文件：`BmsDetailPage.vue`、`InverterDetailPage.vue`

1. `.metric-card` / `.metric-content` 统一 `min-height: 110px`（或 content 区 `min-height: 62px`）。
2. 无 sub 的卡增加占位：
   - 总电压：补 `metric-sub`（如 `—` / 节数 / 单位说明）
   - PV/负载：补 `metric-sub`（如 `峰值 --` 或空占位 `&nbsp;`）
3. 避免只靠内容行数撑高度。

### P0-B：列表工具栏响应式（维持原方案）
文件：`EdgeDeviceList.vue`  
1100 下确认创建按钮掉行；按原 P0 断点改造。

### P1-A：详情信息架构统一
1. BMS 去掉实时数据/指令频率的默认折叠，或三端统一改为「默认可视卡片 + 可选高级折叠」。
2. 统一区块顺序建议：Header → 基本信息 → 核心指标 → 专业可视化 → **实时数据** → **指令频率** → **受控操作** → 历史。
3. DeviceHeader：ws 断开时显示 `type="info"` 的「未连接」tag，避免宽度跳动。

### P1-B：头部操作在窄屏折叠
- `>1100`：单行
- `768–1100`：保留刷新/编辑，其余进「更多」
- `<768`：主按钮 + dropdown

### P2：RealtimeDataList / CommandList / History / Operations
- Realtime：item-size 80→60；min-height 400→240
- CommandList：固定 180/200 改 flex + `min-width:0`；`<768` 纵向堆叠
- History：去掉硬编码 margin
- Operations：历史默认只显示最近 N 条 +「查看更多」

---

## 9. 当前状态与剩余工作

### 9.1 已完成
- [x] 开发环境恢复（ehome-dev + make up + auth bootstrap）
- [x] 列表页 1400/1100 DOM 量化 + 截图
- [x] 通用详情页 1400/1100 量化
- [x] BMS/逆变器详情页 1400 量化（含模拟数据）
- [x] BMS 768 头部/指标/指令列宽抽样
- [x] 总电压不等高问题实锤（90 vs 110）
- [x] 本文件追加完整分析

### 9.2 仍可后续补充（非阻塞设计）
- [ ] 列表页有真实 device-card 时的网格测量（当前多为空状态）
- [ ] 逆变器 768 全页
- [ ] 历史图表窄屏与导出按钮位置截图
- [ ] 将文档归档副本到 `docs/ui-ux-audit/edge-device-layout.md`（若需要）

### 9.3 结论

边缘设备 UI/UX 主问题已从“猜测”升级为“可测量结论”：

1. **列表 P0**：工具栏中等宽度换行（66→98）。
2. **详情 P0**：BMS/逆变器 metric-card 因内容行数不同导致同排高度差 18–20px（总电压/PV/负载偏矮）。
3. **P1**：三端详情折叠/展开与区块顺序不统一；窄屏头部多行换行。
4. **P2**：Realtime 虚拟行高浪费、CommandList 固定列宽、Operations 时间线过长、History 硬编码间距。

**可以进入实现阶段**；实现时按 P0-A / P0-B → P1 → P2 顺序，每步用 CDP 在 1400/1100/768 回归 metric 高度与 toolbar 高度。
