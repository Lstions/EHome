# BMS 详情页设计风格分析（2026-08-17）

> 对象：`frontend-shared/src/views/edge-device/bms/BmsDetailPage.vue`（cc40e3c6 生产级升级后）+ 共享组件群（`views/edge-device/shared/`）。
> 用途：为节点总览页（NodeOverview）新增 TAB 及后续页面定设计基调。

## 1. 风格定位：双体系并存

项目当前存在两套页面风格体系，**BMS 详情页属于"Element Plus 原生体系"**，与节点总览页的"设计稿 token 体系"不同：

| 维度 | BMS 详情页（EP 原生体系） | NodeOverview（设计稿体系） |
|---|---|---|
| 卡片 | `el-card shadow="hover"` | 自绘 `.card`（白底+8px 圆角+双层浅阴影） |
| 栅格 | `el-row :gutter="20"` + `el-col :xs/:sm/:md` | flex/grid 自绘 + 自定义断点 |
| 颜色 | EP CSS 变量（`--el-color-*`、`--el-fill-color-lighter`、`--el-text-color-*`） | 页面级 `--no-*` token（含 `html.dark` 暗色变体） |
| 间距 | 行间距 `style="margin-top: 20px"` 内联 | `.card-row { margin-bottom: 16px }` |
| 字号 | 跟随 EP 默认（14px 基准） | 页面统一 13px/20px，标题 16px/600 |

**结论：两套体系都是"现行合法"。** BMS 页证明 EP 原生体系 + 语义色变量可以做出生产级页面；NodeOverview 证明设计稿体系用于像素级复刻场景。新页面按归属选择：设备详情族（BMS/逆变器/通用）用 EP 体系，节点总览族用 `--no-*` 体系。

## 2. BMS 页设计 DNA（可复用模式）

### 2.1 布局骨架
```
DeviceHeader（标题/返回/操作）
DeviceInfoCard（基本信息：桌面 el-descriptions 2列 / 移动 mobile-info-list）
MetricStatCard × 4（el-row :gutter=20，xs:12 sm:12 md:6 → 移动端 2 列、桌面 4 列）
温度探头 + MOS状态（el-row，xs:24 sm:12 → 移动单列、桌面双列）
保护状态（整行卡）
底部双列：实时数据流(md:14) + 指令频率/受控操作(md:10)，≤992px 堆叠
```

### 2.2 关键设计决策

1. **指标卡统一走 MetricStatCard**（`components/common/MetricStatCard.vue`）：透明底彩色图标、渐变在组件内写不出（防回退）、辅助槽恒占位保证等高。放电=danger 红 / 充电=success 绿 / 静止=中性，只标文字不污染整卡。
2. **语义色全部走 EP 变量**：`.is-success { color: var(--el-color-success) }` 模式（BmsDetailPage.vue:294-296），暗色主题自动适配，零额外代码。
3. **温度探头分档卡**：`grid-template-columns: repeat(auto-fill, minmax(120px, 1fr))` 自适应网格，`--el-fill-color-lighter` 底 + 8px 圆角，数值 18px/600 + 状态 el-tag。
4. **卡片头弹性布局**：`.xxx-header { display:flex; justify-content:space-between; flex-wrap:wrap; gap:8px }` + 标题 `white-space:nowrap; flex-shrink:0` + 摘要 `margin-left:auto`——长摘要在移动端自动换到第二行，防逐字断行。
5. **响应式策略**：优先用 el-col 的 xs/sm/md 断点属性（声明式），页面级 `@media` 只补栅格表达不了的（如 992px 堆叠后补间距，BmsDetailPage.vue:313-315）。
6. **移动端信息卡**：DeviceInfoCard 的 `mobile-info-list` 模式——标签列 `minmax(max-content, 106px)` 由最长字段推导不硬编码、标签禁折行（nowrap+ellipsis 防拆字）、值列 `overflow-wrap:anywhere`。

### 2.3 交互与工程纪律（与视觉同等重要）

- **受控操作卡片化**：DeviceControlPanel 的 `.op-list`（`repeat(auto-fill, minmax(220px,1fr))`）+ `.op-item`（图标+名称+描述+风险标签，hover 升 border/shadow，`:focus-visible` 键盘可达 outline）。不可用操作折叠区逐条给 fail-closed 原因。
- **空态**：el-empty 带 `:image-size="60"` 收敛尺寸。
- **参数表单**：ActionForm 数字感知排序（resistance_1..30）+ >6 参数自动 640px 双列。
- **测试契约**：MetricStatCard.spec 断言源码不含 `linear-gradient`（防风格回退）——样式约定用测试锁死。

## 3. 对 NodeOverview 新增 TAB 的约束（本次执行采用）

NodeOverview 属设计稿体系，新增 TAB 必须：
- 用 `.card`/`.card-head`/`.card-title` 自绘卡片，不用 el-card；
- 颜色只用 `--no-*` token，不写死色值；
- 宽表用 `.bus-table` 原生表格 + 容器内横向滚动，不用 el-table；
- 借鉴 BMS 页的模式语言：统计 chips（≈温度探头分档卡）、列表行 hover、状态徽标语义色、卡片头"标题左+操作右"、空态/加载态/错误态三态区分；
- 响应式沿用页面既有断点（1200/1440/900/768），≤768px 无横向溢出。

## 4. 风险与注意

- **风格漂移风险**：同一项目两套体系并存，新页面归属判断错误会导致视觉不一致。判断依据：页面是否源自 designs/ 像素稿。
- **EP 变量 vs `--no-*` 混用**：在 NodeOverview 内用 `--el-color-*` 会破坏设计稿色板（如 `--no-primary: #2E6BFF` ≠ EP 默认蓝），属缺陷。
- **暗色模式**：`--no-*` 体系需手工维护 `html.dark` 变体；EP 体系自动适配。NodeOverview 新增区域若引入新语义色，必须同步补暗色 token。
