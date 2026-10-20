# BMS 移动端改版 demo · 方案对比（2026-10-20）

> **状态**：设计稿（demo）已交付，等待评审。**评审通过前不进入实现**。
> **demo 路由**：`/dev/mobile-redesign`（DEV 门禁，生产构建静态消除）
> **页面文件**：[MobileRedesignDemo.vue](../../frontend-shared/src/dev/MobileRedesignDemo.vue)
> **截图**：`.logs/ui-fix/demo-shots/`
> **基准视口**：393 × 875（iPhone 14/15 逻辑宽度，即用户截图的机型）

---

## 0. 一句话结论

> **推荐方案 C（平衡态）**：首屏放"结论"（指标 / 保护 / MOS），次屏放诊断图表，
> 末屏放配置与操作。它首屏能看到 3 块完整内容（内容底边 967px），
> 只比 A 少 1 块（A 用四卡压成 54px 单行换来第 4 块），
> 但避免了 A 砍掉副文本与降字号的代价；
> 相比 B 少 726px 总高（-23%），且保护状态从 233px 降到 145px。

---

## 1. 三个方案

| | 方案 A · 密度优先 | 方案 B · 可读性优先 | **方案 C · 平衡态（推荐）** |
|---|---|---|---|
| **核心思路** | 一屏尽量装完「状态 + 配置」，图表全部 sparkline 化 | 每块只讲一件事，图表给足高度与标签，长列表折叠分层 | 首屏=结论，次屏=诊断，末屏=配置；密度与可读各取一半 |
| **页头** | stacked（名称行 + 元信息折行） | compact（名称 + 时间戳同行） | sheet（一行摘要，详情进抽屉） |
| **指标卡** | strip（单行四列，52px） | tiles（2×2，含副文本与底条） | sheet（2×2 + SOC 健康下限刻线） |
| **保护状态** | heat 条（12 格一行，26px） | cells-3 网格（3 列 × 4 行，常驻名称） | heat 条 + 「查看 12 项明细」按需展开 |
| **MOS** | strip（两格一行） | row（44px 行） | **grid（两个方块，像电灯开关）** |
| **温度** | 刻度条 20–36℃ 放大 | 刻度条 0–60℃ 物理量程 | 刻度条 0–60℃（与电芯同卡） |
| **新增图表数** | 4 个 | 6 个 | 5 个 |
| **总高（393px）** | **2064px** | 3182px | 2456px |
| **首屏完整可见的卡** | 4 块（内容底边 1149px） | 3 块（1007px） | 3 块（967px） |
| **适合谁** | 熟练运维、高频扫读 | 现场逐项核对、新人上手 | **兼顾两者（默认上线形态）** |

### 方案 A：密度优先

**一句话**：一屏装完「状态 + 配置」：指标压成单行带、保护用 12 格热力条、图表全部 sparkline 化。

- 指标 → 单行四列（规范 §4.3「统计卡」第 3 条推荐形态），四卡总高 **54px**（原 386px）
- 保护 + MOS 合并成「运行状态」一张卡，两块都是"现在能不能充放"的判断依据
- 电芯 64px spark + 温度 84px 刻度条，两块合计 246px
- 轮询用条行（量 + 编辑同一行），**无输入框**
- 风险：数值只剩 17px、去掉副文本，SOC 7% 的健康区间提示没了

### 方案 B：可读性优先

**一句话**：每块只讲一件事，图表给足高度与标签，长列表用折叠分层而不是压缩。

- 保护状态用 cells-3 网格（**12 项名称常驻**，最保守，不隐藏任何字样）
- 图例、轴名、极值标签全部保留；趋势图 217px / 168px
- 轮询保留精确输入（`el-input-number` 104px，生产为 234px）
- 代价：总高 3182px，比生产页（约 3900px）只省 18%

### 方案 C：平衡态（推荐）

**一句话**：首屏=结论（指标/保护/MOS），次屏=诊断图表，末屏=配置与操作。

- 保护：heat 条（全绿时一行结论 + 12 格），明细按需展开 —— **全绿时明细零信息量**
- MOS：两个方块并排，开关放大 —— 直接对应"像电灯开关一样操作"的原话
- 电芯 + 温度合成「电芯与温度」卡（都是电芯本体的物理量）
- 轮询：条行合并，387px（生产 762px）
- 页头：sheet 形态，**89px**（生产约 300px）

---

## 2. 逐条回应六条批评

| # | 用户批评 | 改造前（生产实测） | 改造后（方案 C 实测） | 做法 |
|---|---|---|---|---|
| 1 | 没利用满空间 | 卡片 321/393 = **82%**（左 32 右 40 空白） | 卡片 **377/393 = 96%**（左右各 8px） | 页面 padding 20→8，卡片 padding 16/20→10 |
| 2 | 超出手机空间 | 横向溢出（`scrollWidth 432 > clientWidth 382`） | **`docScrollWidth 393 = docClientWidth 393`**，且 `.page-body` 内 **0 个** 元素 `scrollWidth > clientWidth` | 全部横向排列用 `minmax(0,1fr)` + `min-width:0` |
| 3 | 顶部重复内容 | 页头 ~300px，面包屑/标题/副行三处重复设备身份，4 按钮占两行 | 页头 **89px**，名称+在线+刷新+⋯ 一行，元信息按需进抽屉 | 删除面包屑（独立路由本无「上一级」语义）、元信息折叠 |
| 4 | UI 设计不规范 | 页面渲染开发者注释（「着色规则：…」「复用旧 BMS 页柱状图口径…」「沿用旧页…」） | **0 处**开发注释文案；状态一律「文字 + 颜色」双通道 | 注释全部删除或收进 `title`/`aria-label` |
| 5 | 轮询配置空间利用差 | 762px（0.87 屏），输入框 234px（占屏 60%），11 行 | **387px**（-49%），输入框 **0 个**（快捷档位），5 行 | 条行合并：量 + 编辑同一行 |
| 6 | 移动端用满两侧 | 同上 1 | 同上 1 | 同上 1 |

---

## 3. 区块高度：改造前 → 改造后

> 生产实测数据由 Lead 提供（393×875）。改造后为本次 demo 在**同一视口**下
> Playwright `getBoundingClientRect().height` 实测。

| 区块 | 改造前 | 方案 A | 方案 B | **方案 C（推荐）** |
|---|---:|---:|---:|---:|
| 页头（含面包屑+4 按钮） | ~300px | 112px | 112px | **89px** |
| 4 个指标卡 | 386px | **54px** | 168px | 170px |
| 保护状态 12 项 | **670px** | 含在运行状态 227px | 233px | **145px** |
| MOS 直控 | 364px | 含在运行状态 227px | 181px | 199px（grid 方块） |
| 温度探头 | 261px | 133px | 165px | 含在电芯与温度 248px |
| 电芯电压卡 | 535px | 113px | 178px | 含在电芯与温度 248px |
| **轮询指令配置** | **762px** | **387px** | 603px + 199px 分布图 | **387px** |
| 受控操作 | 1280px | 518px（默认收起 4/5 组） | 518px | 518px |
| **页面总高** | ~3900px | **2064px** | 3182px | **2456px** |

**关键改善（推荐方案 C）**：
- 保护状态 **670 → 145px（-78%）**
- 轮询配置 **762 → 387px（-49%）**
- 页头 **300 → 89px（-70%）**
- 卡片宽度 **321 → 377px（+17.4%）**
- 页面总高 **~3900 → 2456px（-37%）**

---

## 4. 新增图表：信息增益说明

> Lead 已核实：设备 14（BMS）24h 内 `total_voltage / current / rsoc /
> remaining_capacity / protection_status / fet_status / temperature_1..3 /
> cell_voltage_max / cell_voltage_min` **各 504 个点位**，全部有真实历史数据。
> 所以下列图表**不会出现空图**。取数接口 `/api/v1/unified-data/historical-batch`，
> 前端封装 `src/composables/useHistoryData.ts`。

### 4.1 温度趋势（3 路，新增）
- **数据源**：`temperature_1..3`（各 504 点）
- **省了什么**：原来只有 3 个瞬时数字，要判断"是否某一路单独偏高"必须**翻历史数据页**
- **多了什么判断力**：能看出"探头 1 在 14 点后单独爬升 29.6℃、另两路平稳" ——
  这是**局部发热/探头故障**的典型形态，截面数据永远看不出来
- **信息增益**：从 0 个时间维判断 → 1 类故障可识别

### 4.2 剩余电量趋势（新增）
- **数据源**：`rsoc`、`current`（各 504 点）
- **省了什么**：SOC 7% 是个孤立数字，读者不知道"掉得快不快"
- **多了什么判断力**：12.4% → 7.0% 的斜率 + 18 点起转充电，可判断
  "掉电正常 / 充电是否真的灌进去"
- **信息增益**：把「当前值」升级为「趋势 + 拐点」

### 4.3 保护触发历史（新增，本方案最"新"的信息）
- **数据源**：`protection_status` 位掩码（504 点）按 bit 展开 + `read_protection_count` 累计次数
- **关键洞察**：当前位掩码 `0x00` 全绿，但历史 30 天有 **6 次触发**
  （放电过流 3 次 / 放电过温 2 次 / 单体欠压 1 次）
  → **只看当前位掩码会漏掉全部历史信息**
- **为什么不做折线**：位掩码是整数枚举，画折线无意义；按 bit 展开后只有
  "触发过几次 / 多长时间"有意义，故用条形
- **信息增益**：把「现在没事」纠正为「现在没事，但这 3 类有问题」

### 4.4 电芯电压 spark（改造现有）
- **数据源**：`cell_voltage_1..16`（最新值）
- **省了什么**：生产页 535px → 113px（A）/ 84px 高（C 卡内）
- **多了什么**：极值与压差从「图内 10px 标签」改为**图外正文**（`压差 11 mV`）
  —— 手机上 10px 刻度值等于没有
- **降级**：单柱 < 34px 时只标最高/最低（复用 `BmsCellVoltageChart` 的
  `LABEL_DEGRADE_THRESHOLD` 思路与并列极值去重）

### 4.5 温度刻度条（新增）
- **数据源**：`temperature_1..3`（最新值）
- **省了什么**：3 个 44px 方块 = 261px → 133px
- **多了什么判断力**：0–60℃ 量程 + 45℃ 告警虚线，
  「现在很凉」不需要算就知道 —— 文字给不了这个
- **注意**：方案 A 用 20–36℃ 放大显示（突出三路差异），
  量程刻度必须画出来，否则读者无法判断长度含义（误读风险 > 省下的空间）

### 4.6 轮询间隔（**未用 ECharts**，改为 CSS 条）
- 初版做了独立的 `PollIntervalChart`（ECharts 条形图），评审时发现硬伤：
  **同一批指令名出现两次**（上图读快慢、下图改间隔），且两卡合计 460px
- 改为**行内 CSS 条**：每行同时是 状态 / 条形量 / 快捷档位
- 结果：387px、0 个输入框、指令名只出现一次
- 方案 B 仍保留独立条形图（可读性优先，允许两处），并明确两块职责不重叠

---

## 5. 轮询配置：两个硬约束的落地

Lead 实测的逆变器页缺陷：状态标签写「轮询中 · 10000 ms」，而输入框右侧**已有**「ms」
→ 同一行两个 ms，读屏也读两个单位。

**本 demo 的规则**：

| 规则 | 落地 |
|---|---|
| 单位只出现一次 | 数值列写「1 秒 / 5 秒」（`formatInterval` 带出），条形不带单位，状态标签不带数值 |
| 状态与数值语义分离 | 状态标签只写「轮询中 / 未启用」；数值独立成列 |
| 不出现"轮询中 · 1000ms"混装 | 状态与数值分属两个 DOM 节点、两列 |

三种形态：
- **A / C**：`PollBarRows`（条行，无输入框）
- **B**：`PollCommandList mode="stacked"`（保留 `el-input-number`，宽 **104px**，生产 234px）

---

## 6. 技术实现

### 文件清单（全部新建，未改任何既有文件）

| 文件 | 作用 |
|---|---|
| `src/dev/MobileRedesignDemo.vue` | demo 主页面（A/B/C 切换 + 三套编排） |
| `src/dev/mobile-redesign/mockBms.ts` | 静态 mock 数据（单一数据源） |
| `src/dev/mobile-redesign/parts/DeviceHead.vue` | 页头（stacked/compact/sheet） |
| `src/dev/mobile-redesign/parts/MetricTiles.vue` | 指标卡（tiles/strip/sheet） |
| `src/dev/mobile-redesign/parts/ProtectionMatrix.vue` | 保护状态（heat / cells-3 / cells-4） |
| `src/dev/mobile-redesign/parts/MosDirect.vue` | MOS 直控（row/grid/strip） |
| `src/dev/mobile-redesign/parts/PollBarRows.vue` | 轮询条行（A/C） |
| `src/dev/mobile-redesign/parts/PollCommandList.vue` | 轮询输入行（B） |
| `src/dev/mobile-redesign/parts/ControlledOps.vue` | 受控操作分组 |
| `src/dev/mobile-redesign/charts/CellSparkChart.vue` | 电芯电压 spark |
| `src/dev/mobile-redesign/charts/TempScaleChart.vue` | 温度刻度条 |
| `src/dev/mobile-redesign/charts/PollIntervalChart.vue` | 轮询间隔条形（B 用） |
| `src/dev/mobile-redesign/charts/TrendChart.vue` | 多序列趋势（支持双轴） |
| `src/dev/mobile-redesign/charts/ProtectionHistoryChart.vue` | 保护触发历史 |
| `src/router/index.ts` | **仅新增 1 条 DEV 路由**，未改其它逻辑 |

### 图表技术约束

- 全部 `import * as echarts from 'echarts/core'` + 按需 `use([...])`，
  与 `BmsCellVoltageChart.vue` 同一模式，**未引入新依赖**
- 颜色走 `getThemeColors()` / `getThemeSurfaceColors()`（Canvas 拿不到 CSS 变量）
- 主题切换：`MutationObserver` 监听 `documentElement` 的 `class / data-theme`
- 容器感知：`ResizeObserver` 维护 `chartWidth`，低于阈值降级标签
- 手机字号：图内最小 **10px**
- 单位：轴名或数值后缀**各出现一次**；双轴场景左右各一个单位

### 已修的 demo 自身缺陷（防止后人重蹈）

1. **`computed` 在 `<script setup>` 的普通函数里不会自动解包**
   → 把 `ComputedRef` 传给 echarts，内部 clone 沿引用图无限递归
   → `RangeError: Maximum call stack size exceeded`，整页被 ErrorBoundary 接管。
   必须写 `.value`。
2. **独立 DEV 路由必须自带滚动容器**
   → `App.vue` 是「应用外壳」模式（`html,body,#app{height:100%;overflow:hidden}`），
   没有 MainLayout 就没有元素承担滚动，内容既滚不动、整页截图也被裁在 875px。
3. **同一批指令名不要出现两次**（方案 A 初版的图 + 列表双份）
4. **12 项 0 值不要各占一行**（保护历史 v1 的 12 行里 9 行恒为 0）

---

## 7. 自检结果

| 项 | 结果 |
|---|---|
| `npx vue-tsc --noEmit` | **exit 0** |
| 横向溢出（三方案） | `docScrollWidth 393 = docClientWidth 393`；`.page-body` 内 **0** 个元素溢出 |
| 控制台错误 | **0**（`pageerror` / `console.error` 均无） |
| 卡片宽度 | 377 / 393 = **96%** |
| 暗色主题 | 已截图验证（`scheme-C-dark-fold.png`） |
| 页面滚动 | 正常（自带滚动容器，实测可滚到 2456px） |
| 开发注释残留 | **0 处** |

### 截图

`.logs/ui-fix/demo-shots/`

| 文件 | 内容 |
|---|---|
| `scheme-A-fold.png` / `scheme-A-full.png` | 方案 A 首屏 / 整页（393×875） |
| `scheme-B-fold.png` / `scheme-B-full.png` | 方案 B 首屏 / 整页 |
| `scheme-C-fold.png` / `scheme-C-full.png` | 方案 C 首屏 / 整页 |
| `scheme-C-dark-fold.png` | 方案 C 暗色主题 |
| `shoot.mjs` / `measure-final.mjs` | 截图与量化脚本（可复跑） |

---

## 8. 需要用户/Lead 拍板的取舍

1. **保护状态是否默认展开 12 项明细？**
   C 方案默认收起（全绿时明细零信息量），点一次可展开。
   若运维习惯"必须看到 12 个名字才安心"，则改用 B 的 cells-3。
2. **指标卡要不要保留副文本？**
   A 砍掉（省 334px），C 保留。副文本（"健康区间 20–90%"）对**新手**有用，
   对熟练运维是噪声。
3. **轮询是否接受"无输入框"？**
   A/C 用快捷档位（关 / 1s / 5s / 30s），覆盖常见需求但**无法设 7s 这类任意值**。
   若业务上确需任意毫秒，则 C 需补一个"自定义"入口（会多 1 行）。
4. **受控操作是否要按使用频率重排？**
   当前沿用后端 5 组分类。若实际运维 90% 只用「读取信息」，可把该组提到首屏末尾。
5. **是否要在真机上验证？**
   本 demo 只在 Chromium 393×875 验证。Safari（iOS）的 `el-input-number` 与
   `el-drawer` 行为建议真机复核后再实现。

---

## 9. 复现方式

```bash
# 1. 起 dev server（5175）
cd frontend-shared && pnpm dev --port 5175

# 2. 浏览器打开
#    http://localhost:5175/dev/mobile-redesign
#    DevTools 切到 393×875 或直接用手机访问

# 3. 顶部工具条切换 A / B / C

# 4. 复跑截图与量化
cd .logs/ui-fix/demo-shots && node shoot.mjs && node measure-final.mjs
```