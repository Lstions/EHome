# EHomeSystem 前端 UI/UX 参考文档结构化摘要

> 基于以下三份核心文档综合：
> 1. `边缘设备UIUX分析与修复方案.md`（2026-07-19，状态：分析完成，未实施）
> 2. `docs/设计/前端UIUX改进设计.md`（2026-07-13，状态：P0–P3 已实施）
> 3. `docs/设计/GPIO_PWM_UI重设计规格.md`（状态：规格待实施）

---

## 一、项目技术栈与前端结构

| 维度 | 内容 |
|------|------|
| 框架 | Vue 3.5+ Composition API (`<script setup>`) |
| 语言 | TypeScript 5+ |
| 构建 | Vite 7+ |
| UI 库 | Element Plus 2+ |
| 状态 | Pinia |
| 路由 | Vue Router |
| 图表 | ECharts |
| 前端目录 | `frontend-shared/src/{api,components,composables,router,stores,utils,views}` |
| 开发端口 | Vite `:5174`，后端 `:8082`，CDP `:18800` |

---

## 二、文档一：前端 UI/UX 改进设计（已实施）

### 2.1 核心问题（8 项）
1. 小屏响应式覆盖不全（Dashboard KPI 在 390px 被压缩到 69px）
2. 顶部“全局搜索”实为关键词跳转，列表页未消费 `route.query.search`
3. Dashboard 状态时间线为临时构造的“模拟数据”
4. 主题变量存在但核心页面硬编码亮色，暗色主题不一致
5. 统计卡混用全量总数与当前页统计，数字不闭合
6. 通道管理不在侧栏；`PageHeader` 插槽不匹配导致刷新按钮不渲染
7. 图标按钮缺 `aria-label` / tooltip
8. 数据面板写死类别，无法适配 BMS/逆变器/动态传感器

### 2.2 信息架构与页面模板

**导航结构：**
```
仪表盘
资源管理：节点 / 边缘设备 / 通道
数据中心：数据面板
配置与发布：配置模板 / 固件管理
系统管理：系统监控 / 用户管理
```

**三种页面模板：**
| 模板 | 适用页面 | 固定结构 |
|------|---------|---------|
| 资源列表 | 节点、边缘设备、通道、配置、固件、用户 | PageHeader → 筛选/操作栏 → 统计 → 列表/卡片 → 分页/空状态 |
| 资源详情 | 节点详情、边缘设备详情 | 返回 + 名称/状态/操作 → 概览 → 分区卡片 |
| 数据分析 | 仪表盘、数据面板、系统监控 | PageHeader → 时间/对象筛选 → KPI → 图表 → 明细 |

### 2.3 分阶段实施（已全部完成）

| 阶段 | 主题 | 关键产出 |
|------|------|---------|
| **P0 可信与可用** | 移动 KPI 栅格、快速跳转语义、Dashboard 真实运维信息、导航与 PageHeader 修复 | Dashboard 响应式网格；顶部入口更名为“快速跳转”；删除模拟时间线；通道入主导航；`PageHeader` 统一 `extra` 插槽 |
| **P1 资源管理一致性** | 统计口径、筛选/分页/视图切换、空状态 | 统计卡统一标明“本页”范围；筛选 chips + 清除全部；EmptyState 支持 5 类（initial/filtered/error/permission/empty） |
| **P2 主题与组件系统** | 主题令牌、图标与可访问性、日志/终端小屏适配 | theme.css 语义 token；emoji → Element Plus 图标 + aria-label；LogPanel/ChannelTerminal 小屏纵向布局 |
| **P3 数据分析深化** | 动态类别、真实状态历史 | DataPanel 由设备可用传感器类别驱动；新增 `GET /api/v1/unified-data/categories`、`GET /api/v1/nodes/status-history` 等真实接口 |

### 2.4 组件契约

- **PageHeader**：`title`、`subtitle?`、`showBack?`，仅 `extra` 插槽；返回按钮需可访问名称。
- **StatItem**：`label`、`value`、`unit?`、`icon`、`tone?`、`scope?: 'global'|'filtered'|'page'`、`onClick?`；可点击必须有明确导航结果。
- **EmptyState**：`kind: 'initial'|'filtered'|'error'|'permission'|'empty'`；每类独立插图/文案/操作。

### 2.5 验收标准
- 浏览器矩阵：1440px / 768px / 390px / 暗色模式，覆盖 Dashboard、NodeList、EdgeDeviceList、DataPanel、ChannelList、NodeDetail/LogPanel/ChannelTerminal。
- 检查项：无错误边界 `<pre>`、无未捕获异常、无页面级横向溢出、关键文本/按钮存在、角色页面受限正确。

---

## 三、文档二：边缘设备 UI/UX 分析与修复方案（分析完成，未实施）

### 3.1 审查范围
- 列表页：`EdgeDeviceList.vue`
- 详情页：`GenericDeviceDetail.vue`、`BmsDetailPage.vue`、`InverterDetailPage.vue`
- 共享组件：`DeviceHeader.vue`、`CommandFrequencySection.vue`、`HistoryChartSection.vue`
- 通用组件：`RealtimeDataList.vue`、`CommandList.vue`、`PageHeader.vue`
- 逻辑：`useDeviceData.ts`

### 3.2 量化验证方法
- CDP 启动 headless Chromium（1400×1000 / 1100×857 / 768×900）
- `Runtime.evaluate` 获取 DOM bounding box
- `Page.captureScreenshot` + `vision_analyze` 可视化确认

### 3.3 发现的问题（按优先级）

**P0：列表页工具栏响应式**
- 1100px 视口下工具栏高度 66→98px，“创建边缘设备”按钮被挤到第二行，搜索 placeholder 被截断。
- 统计卡 1400px 单行 4 卡 / 1100px 2×2 网格，卡本身等高（106px），但叠加工具栏换行后中等宽度信息密度下降。

**P0：详情页指标卡高度不一致**
- BMS：SOC 99 / 剩余容量 110 / **总电压 90** / 电流 110（差 20px）
- 逆变器：PV 90 / 电池 108 / 电网 108 / 负载 90
- 根因：无 `metric-sub` 或进度条的卡缺少内容行撑高。

**P1：三端详情信息架构不一致**
- BMS：实时数据 + 指令频率默认折叠（`activeCollapses = []`）
- 通用/逆变器：独立展开卡片
- 区块顺序也不统一（BMS vs 逆变器 vs 通用）。

**P1：窄屏头部操作换行**
- 768px 下 `page-header` 高 65→145，header-actions 换行 3 层。

**P2：组件级密度与宽度问题**
- `RealtimeDataList`：虚拟列表 `item-size=80`，实际内容高 53，行高浪费 27px；`min-height:400` 空数据仍占满。
- `CommandList`：`.cmd-info{width:180px}` + `.cmd-controls{width:200px}` 固定宽，768px 余量极小。
- `DeviceControlPanel`：操作历史时间线无截断，Generic 页高约 2990px。
- `HistoryChartSection`：导出按钮硬编码 `margin-left:10px`。

### 3.4 根因
1. 响应式策略缺失：无 900–1200px 中间断点。
2. 组件高度/尺寸不统一：`el-tag` 与 `el-button` 混排、指标卡内容行数不同。
3. 过度依赖固定宽度。
4. 信息层级不统一：折叠 vs 展开、区块顺序在三端不一致。

### 3.5 修复方案（待实施）

| 优先级 | 文件 | 方案 |
|--------|------|------|
| P0-A | `BmsDetailPage.vue`、`InverterDetailPage.vue` | `.metric-card` 统一 `min-height:110px`；无 sub 的卡补占位（`metric-sub` 或 `&nbsp;`） |
| P0-B | `EdgeDeviceList.vue` | 统计卡改 `grid-template-columns: repeat(auto-fit, minmax(220px,1fr))`；工具栏 900–1100 搜索独占一行；设备网格 `repeat(auto-fill, minmax(300px,1fr))` |
| P1 | `DeviceHeader.vue`、三端详情页 | tag 与 button 统一尺寸；BMS 移除默认折叠；统一区块顺序；ws 断开时显示“未连接”tag；768 下操作按钮折叠到“更多” |
| P2 | `RealtimeDataList.vue`、`CommandList.vue`、`HistoryChartSection.vue`、`DeviceControlPanel.vue` | item-size 80→60；min-height 400→240；固定列改 flex + `min-width:0`；去硬编码 margin；历史默认显示最近 N 条 + “查看更多” |
| P4 | 回归 | `make lint`；1400/1280/768 CDP 截图 + bounding box 测量；三端详情一致性检查 |

---

## 四、文档三：GPIO/PWM UI 重设计规格（规格待实施）

### 4.1 目标
- 同一视图快速判断“有哪些引脚、谁占用、当前值、能做什么”，安全完成配置与即时控制。
- **拒绝 GPIO/PWM 合并视图**：两类硬件资源独立上报，不互相投影。

### 4.2 页面 IA
- 位于：节点详情 → 总线配置 → 硬件资源
- GPIO 与 PWM 分为两个独立 `el-collapse-item`
- 数据源：`capabilities.buses.gpio` / `capabilities.buses.pwm` 为唯一事实源

### 4.3 统一行容器规范

```html
<ul class="pin-resource-list" aria-label="GPIO 与 PWM 引脚资源">
  <li class="pin-resource-row" data-state="available|gpio|pwm|occupied|offline|error">
    <div class="pin-identity">...</div>
    <div class="pin-configuration">...</div>
    <div class="pin-runtime">...</div>
    <div class="pin-actions">...</div>
    <div class="pin-feedback" role="status">...</div>
  </li>
</ul>
```

- 不用 `el-table`（行内有 Switch、Slider、菜单，需自然重排）。
- 桌面最小行高 64px、padding 12px 16px、行间 `border-bottom` 分隔。
- 配置成功行用 3px 左色条：GPIO `primary`，PWM `success`；可用/占用不整行彩底。

### 4.4 GPIO 行信息顺序（从左到右）
1. `.pin-identity`：`GPIO 12`（等宽数字）→ 用户标签 → `GPIO` 类型 tag
2. `.pin-configuration`：方向 `OUTPUT/INPUT/INPUT_PULLUP/INPUT_PULLDOWN` → 初始电平/上下拉
3. `.pin-runtime`：当前电平（状态点 + `HIGH/LOW/未知`）→ INPUT 后接“读取”按钮；OUTPUT 后接 `el-switch`（`active-text="HIGH"` / `inactive-text="LOW"`）
4. `.pin-actions`：`编辑` → `el-dropdown` 更多菜单 → `移除配置`（危险项）
5. `.pin-feedback`：仅请求中/失败时出现

**关键约束：**
- 删除 ON/OFF/TOGGLE 三按钮并列；一个 Switch 完整表达二元输出。
- INPUT 不显示禁用 Switch；用“读取”主动作，保留最后一次值；未知不能默认为 LOW。
- 行自身不响应点击，避免误触。

### 4.5 PWM 行信息顺序
1. `.pin-identity`：`GPIO 12` → 用户标签 → `PWM` tag
2. `.pin-configuration`：`1 kHz` → `14 bit` → `自动启动`（只读紧凑字段）
3. `.pin-runtime`：运行状态 tag（运行中/已停止/未知）→ 占空比数值 `37.50%` → `el-slider`（数值在滑块前，aria-label 含 pin）
4. `.pin-actions`：`启动/停止`（互斥，默认按钮，不用 danger）→ `编辑` → `el-dropdown` → `移除配置`
5. `.pin-feedback`：调整中显示“待应用”/ loading；松手后 300ms 提交，失败回滚到服务端确认值并提供“重试”

### 4.6 操作层级
| 层级 | 规则 |
|------|------|
| 主操作 | 每上下文最多一个蓝色实心：可用行“配置 GPIO”/“启用 PWM”；INPUT 的“读取”；PWM 已停止的“启动” |
| 次操作 | 刷新、编辑、Switch、Slider、停止；default/text 语义 |
| 危险操作 | 仅“移除配置”；放入更多菜单，红色菜单项 + `ElMessageBox.confirm`（含引脚、用途、影响） |
| 禁止 | 红色不用于 LOW/OFF/停止；单行 pending 只锁定该行 |

### 4.7 未配置与已配置同屏策略
- 先以硬件能力表生成唯一行，再合并 `gpioConfigs`、`pwmConfigs`；同一 pin 互斥状态只在一行呈现。
- 默认筛选“全部”；排序为引脚号，不按状态分区。
- 可用行 48–56px 紧凑态；已配置行 64–72px 完整态。
- 被占用显示 `已占用` warning tag + 用途（如 `UART TX`）；操作区只给“查看占用”。

### 4.8 响应式
| 断点 | 布局 |
|------|------|
| ≥769px | CSS Grid：`minmax(150px,1.1fr) minmax(180px,1.2fr) minmax(280px,2fr) auto`；四区同一行；Slider 140–240px；操作区右对齐不换行 |
| ~430px | 两层：首层身份+状态标签+更多菜单；次层配置摘要占满一行，运行值与控件下一行；Slider 100% 宽；触控目标 ≥44px；工具条 `flex-wrap:wrap`，按钮保留文字 |

### 4.9 状态规格
- `loading`：首次 `el-skeleton` 6 行；刷新保留列表仅按钮 loading。
- `empty`：`el-empty`“该节点未报告 GPIO 资源”，不显示配置 CTA。
- `offline`：分组顶部不可关闭 `el-alert type="warning"`；行完全可读，仅禁用写/读/编辑/移除；禁止 `opacity:0.5` 或 `pointer-events:none`。
- `error`：`el-alert type="error"` + “重试”；有缓存保留数据并标“可能已过期”。
- `occupied`：warning tag 明确占用方；不是 error。
- 运行态未知：info tag“状态未知” + `—`；不把 PWM 默认“已停止”，不把 GPIO 未读取当 LOW。
- 并发：单行 `aria-busy="true"`；WS 刷新保持滚动位置与筛选。

### 4.10 Element Plus 与 CSS Token
- 复用：`el-collapse`、`el-alert`、`el-skeleton`、`el-empty`、`el-switch`、`el-slider`、`el-button`、`el-dropdown`、`el-input`、`el-dialog`、`el-form`、`el-tag`、`ElMessage`、`ElMessageBox`、`el-tooltip`。
- token：`--el-color-primary/success/warning/danger`、`--el-text-color-*`、`--el-border-color/light/lighter`、`--el-fill-color-light/lighter`、`--el-bg-color`。
- 禁止新增硬编码品牌色或 `#fff/#303133/#67c23a`。
- 圆角 8px；动效 0.2s，仅颜色变化。
- 可访问性：Switch/Slider 设含 pin 的 `aria-label`；颜色之外必须有文字/Tag；键盘焦点用 Element Plus 默认 focus ring。

### 4.11 逐文件修改建议
| 文件 | 动作 |
|------|------|
| `NodeDetail.vue` | 不新增 GPIO/PWM 页面级卡片；继续通过 `ChannelPanel` |
| `ChannelPanel.vue` | 分别展示 GPIO pin 与 PWM hardware channel 两个折叠组；PWM 行仅把 GPIO 作为 route 显示 |
| `PeripheralControl.vue` | 改为同一行式列表容器；缺硬件能力清单时仅展示已配置行，空态“暂无已配置外设” |
| `GPIOPinCard.vue` | 重命名/重构为 `GPIOPinRow.vue`；输出改单 Switch；输入保留读取；移除 ✕ 改更多菜单 + 确认 |
| `PWMChannelCard.vue` | 重命名/重构为 `PWMChannelRow.vue`；状态不得本地固定 `false`；停止按钮去 danger；Slider 失败回滚；清理 timer |
| 新增 | `GPIOResourceList.vue`（由 `hardware.gpio` 驱动）、`PWMResourceList.vue`（由 `hardware.pwm` 驱动） |

### 4.12 验收 Checklist（16 项）
- 无小卡片网格；资源单列行式列表
- 每个物理 GPIO 只出现一次，互斥关系同行可判断
- GPIO/PWM 行信息顺序符合规格
- 可用行紧凑、已配置行完整；默认同屏可过滤
- OUTPUT 仅一个 Switch；LOW、停止不用 danger
- 移除配置藏于更多菜单 + 二次确认，成功后行位置不跳动
- 430px 无横向滚动、无溢出；Slider 满宽；触控 ≥44px
- 桌面操作区不换行，Slider 合理宽度
- loading/empty/offline/error/occupied/未知 均可复现
- 离线可读；只禁用不可执行操作；顶部原因说明
- 单行 API 失败行内反馈；PWM 回滚；其他行可操作
- loading 锁定粒度单行/单控件；重复提交被阻止
- 全部颜色/边框/文本用 Element Plus token，无硬编码语义色
- Switch/Slider/菜单可键盘操作，含 GPIO 编号可访问名称
- ChannelPanel 与 PeripheralControl 复用统一列表/行组件
- 配置对话框校验、取消、提交 loading、错误反馈完整
- WS/API 刷新保留筛选/搜索/滚动位置，不闪回空态
- 现有 I2C/UART/SPI/ADC、通道终端、保存配置流程无回归

---

## 五、跨文档共性结论

### 5.1 已完成的系统性改进（前端UIUX改进设计.md）
- 响应式栅格、统计口径统一、空状态分类、主题 token、可访问性、动态数据驱动、真实状态历史 API。
- 浏览器矩阵验证（1440/768/390/暗色）已覆盖核心页面。

### 5.2 待实施的边缘设备专项（边缘设备UIUX分析与修复方案.md）
- 列表页工具栏响应式（P0）
- BMS/逆变器指标卡等高（P0）
- 三端详情信息架构统一（P1）
- 组件密度与固定宽度修复（P2）

### 5.3 待实施的 GPIO/PWM 重设计（GPIO_PWM_UI重设计规格.md）
- 从小卡片网格改为行式资源控制面板
- 统一 GPIO/PWM 行 DOM 信息顺序与操作层级
- 建立 `GPIOResourceList` / `PWMResourceList` 独立列表
- 明确状态规格（loading/empty/offline/error/occupied/未知）

### 5.4 设计原则收敛
1. **响应式**：优先 CSS Grid `auto-fit/auto-fill` + `minmax`；避免固定 px 宽度；900–1200px 需要中间断点。
2. **一致性**：同排卡片等高（`min-height` + 占位）；三端详情区块顺序与折叠策略统一。
3. **信息密度**：虚拟列表行高按实际内容设定；时间线/历史记录默认截断 + “查看更多”。
4. **操作层级**：主操作唯一蓝色实心；危险操作仅“移除配置”且藏入更多菜单 + 确认；红色不用于常规状态。
5. **可访问性**：icon-only 控件必须有 `aria-label` 与 tooltip；状态表达文字 + 色彩 + 图形；键盘可操作。
6. **主题 token**：全面使用 Element Plus CSS 变量，禁止硬编码语义色。
7. **数据真实性**：不伪造数据；统计口径明确标注范围；空状态区分场景。
