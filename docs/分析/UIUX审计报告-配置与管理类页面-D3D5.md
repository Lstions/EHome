# UI/UX 审计报告 — 域 D3/D5：配置与管理类页面（自动化策略 / 告警规则 / 配置模板 / 数据源 / 固件管理）

> 审计对象：EHomeSystem `frontend-shared/`（Vue 3 + Element Plus）
> 依据：`docs/分析/UIUX审计契约-2026-09-13.md`（下称契约）+ `docs/规范/前端开发与UIUX设计规范.md`（下称规范，唯一权威）
> 环境：后端 `http://127.0.0.1:8082`（同源托管 `frontend-shared/dist`），审计库 `ehome_uiux`
> 覆盖页面：`/automation`、`/alerts`、`/device-configs`、`/data-sources`、`/firmware`
> 视口：1440×900 / 1024×768 / 768×1024 / 390×844 / 360×800；主题：light + dark 全部实测
> 证据目录：`/tmp/uiux-d3/`（66 PNG + 1 JSON）、`/tmp/uiux-d3b/`、`/tmp/uiux-d3c/`、`/tmp/uiux-d3d/`、`/tmp/uiux-d3e/`、`/tmp/uiux-d3f/`（37 PNG + 9 JSON）、`/tmp/uiux-d3-kbd/`（40 PNG + 3 JSON）
> 基线复用：`/tmp/uiux-evidence/dom-facts.json`（132 条，未重复跑全量）
> 取证脚本（只读，**未修改 `src/` 任何文件**）：`tools/uiux-d3-probe.mjs` … `uiux-d3-probe20.mjs`（共 19 个新增脚本，全部位于 `frontend-shared/tools/`）

---

## 0. 结论摘要

本域共发现 **26 个问题**：阻断 1 / 高 8 / 中 11 / 低 6。

| # | 严重度 | 一句话 | 规范条款 |
|---|---|---|---|
| 1 | **阻断** | 触发历史「规则」列显示裸主键 `1`/`3`，与规则表名称无法对应 | §1.2.1 / §4.2.6 |
| 2 | 高 | 自动化事件无分页，500 行全量渲染 11902 节点，页高 20040px | §4.5.4 / §3.3.5 |
| 3 | 高 | 自动化页 264 个触控目标 < 36px（含 258 个 18px 高操作列按钮） | §4.4.5 |
| 4 | 高 | device-configs 360px 工具栏「导入」被裁 11px | §4.4.1 |
| 5 | 高 | 配置模板编辑对话框展示创建专属的「传感器驱动」级联字段 | §4.3.2.2 |
| 6 | 高 | 固件编辑「目标型号」保存被后端静默忽略（字段名不匹配） | §3.2.3 / §3.2.1 |
| 7 | 高 | 告警规则接口失败 → 整页 ErrorBoundary，丢失页面与导航 | §3.4.2 / §3.4.6 |
| 8 | 高 | 设备配置接口失败 → 4 个 KPI 渲染 0 + 「暂无配置模板」初始空态 | §1.2.1 / §3.4.2 |
| 9 | 高 | 亮色主题浅底徽标对比度 2.04–2.78:1（全站 pattern，本域 6 类） | §4.5.1 / §4.2.1 |
| 10 | 中 | 数据源详情抽屉固定 560px，360px 下左移 200px，全部 label 不可见 | §4.4.1 |
| 11 | 中 | 数据源表格固定操作列遮挡整行，名称/详情入口在窄屏不可点 | §4.3.2.2 |
| 12 | 中 | 「命令 ID」是死链：点击只 `console.log`，无任何 UI 反馈 | §3.4.6 / §1.2.5 |
| 13 | 中 | 5 个数据表均未使用 `.mobile-table-wrapper`/`.mobile-table-hint` | §4.3.2.1 |
| 14 | 中 | 数据源详情抽屉无 92vw 兜底 + `timeline-item` 语义 | §4.3.2.3 |
| 15 | 中 | 创建规则对话框 footer 初始在视口外（1440×900 亦如此） | §4.3.2.3 |
| 16 | 中 | 危险确认按钮非 danger 类别（7 处），且有诱导性默认焦点 | §4.3.2.4 |
| 17 | 中 | 自动化表格 Switch 无可访问名称（`aria-label` 缺失） | §3.4.4 / §3.1.3 |
| 18 | 中 | 侧栏 135 个 `cursor:pointer` 元素键盘不可达（跨域 pattern） | §3.1.3 |
| 19 | 中 | 页面级错误无重试入口（automation/device-configs/firmware） | §3.4.6 |
| 20 | 中 | 首次加载无骨架屏（device-configs/automation 均无） | §4.3.3.2 |
| 21 | 低 | 禁用「导出」无原因说明（device-configs），无 tooltip 包裹 | §3.4.4 |
| 22 | 低 | 数据源页无 pageHeader（唯一无标题的列表页） | §4.1.1 |
| 23 | 低 | 自动化页 1072 处 inline style | §3.6.4 |
| 24 | 低 | 告警事件表「规则」列同为裸 rule_id | §4.2.6 |
| 25 | 低 | 固件表 ID 列硬编码 `width="50"` 与「操作」列 280px 不均衡 | §4.3.1 |
| 26 | 低 | device-configs 无 `PageHeader` 组件，标题区不一致 | §4.1.1 |

---

## 1. 问题清单（契约 §6 格式）

### #1 【阻断】自动化策略「触发历史」规则列显示裸主键

- **页面**：`/automation` | **视口**：desktop-1440（mobile-360 同样复现） | **主题**：light + dark | **维度**：D3 人机反馈 / D7 文案
- **现象**：上方规则表两条规则名为「S2-低电强制断充」「S1-SOC低开灯」，下方触发历史的「规则」列显示 `1` / `3`（主键），用户无法把事件与规则对上号。筛选下拉用的是规则**名称**，与列表的**数字**互相矛盾。
- **证据DOM**（`uiux-d3-probe.mjs`，`automation-events-rule-col`）：
  ```json
  {"tables":2,"rulesTableRows":2,"eventsTableRows":500,
   "ruleHeader":"规则",
   "distinctRuleCellTexts":["1","3"],
   "firstRowCells":[{"text":"2026/8/31 20:33:58","w":315},{"text":"1","w":80},{"text":"已执行","w":110},
                    {"text":"4","w":100},{"text":"自动","w":70},{"text":"4aa9a48b-7a13-4499-925a-","w":110},
                    {"text":"","w":275},{"text":"","w":100}]}
  ```
  规则表首列文本为「S2-低电强制断充」/「S1-SOC低开灯」（`desktop-1440-light-automation.png` 截图同屏可见），事件表第 2 列 500 行只出现 `1` 与 `3` 两个值。
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（同一屏内「S2-低电强制断充」与「规则=1」并排）、`/tmp/uiux-evidence/desktop-1440-light-automation.png`（主控基线同现象）
- **证据代码**：
  - `frontend-shared/src/views/automation/AutomationRules.vue:76`
    ```vue
    <el-table-column prop="rule_id" label="规则" width="80" />
    ```
  - 对照：同文件 `:63-65` 的筛选下拉**已经**用 `r.name` 作为显示文本
    ```vue
    <el-option v-for="r in rules" :key="r.id" :label="r.name" :value="r.id" />
    ```
    即页面自身已有 id→名称 映射能力，只是列表列未使用。
  - 同样缺陷存在于 `frontend-shared/src/views/alert/AlertRules.vue:59`（`prop="rule_id" label="规则" width="80"`，见 #24）。
- **规范条款**：§1.2.1「**领域事实优先**：界面不能用模拟数据、猜测状态或前端默认值伪造设备事实」；§4.2.6「技术标识（DMA、UART、**ID**、协议字段）可保留原文但**不应成为普通用户唯一可见信息**」；§4.2.4「文字层级清晰：页面标题、区段标题、数值、标签、辅助说明各有稳定角色」
- **严重度**：**阻断**（契约判据「数据/术语误导用户做出错误判断」：排障时用户看到「规则 3 已过期」无法知道是「S2-低电强制断充」，会把低电断充的失败误判到开灯策略上，且该列是事件表唯一的规则标识）
- **建议**（最小改动）：
  1. `AutomationRules.vue:76` 改为作用域插槽渲染名称 + tooltip 保留 ID：
     ```vue
     <el-table-column label="规则" min-width="140" show-overflow-tooltip>
       <template #default="{ row }">
         <span>{{ ruleName(row.rule_id) }}</span>
       </template>
     </el-table-column>
     ```
     其中 `function ruleName(id: number) { return rules.value.find(r => r.id === id)?.name ?? `#${id}` }`（规则已删除时回退为 `#id`，符合 §3.4.5「未知统一显示 `—` 或明确状态文案」）。
  2. 同一改法套用到 `AlertRules.vue:59`。
  3. **不要**只改列宽或加 title——那样仍是数字。
- **验收方法**：断言 `document.querySelectorAll('.el-table')[1] .el-table__row td:nth-child(2)` 的文本集合与第一张表的 `td:nth-child(1)` 名称集合有交集且不含纯数字；或用 Playwright 断言首行第 2 列 `textContent.trim() === 'S2-低电强制断充'`。

---

### #2 【高】自动化事件表无分页：500 行 / 11902 节点 / 页高 20040px

- **页面**：`/automation` | **视口**：所有（desktop-1440 与 mobile-360 数值相同） | **主题**：light + dark | **维度**：D5 业务完整交互流程 / D1 布局
- **现象**：库里**只有 2 条规则**，但触发历史全量铺开 500 行，页面主滚动容器高度 20040px（视口 840px 的 **23.9 倍**），单页 DOM 节点 11902 个，无任何分页控件。
- **证据DOM**：
  | 度量 | 值 | 来源 |
  |---|---|---|
  | `.el-table__row` 总数 | **502**（规则表 2 + 事件表 500） | `d3-facts.json` / `d3q-facts.json` |
  | 事件表行数 | **500** | `automation-events-rule-col.eventsTableRows` |
  | `.el-pagination` 数量 | **0** | `pagination: 0`（三视口 × 双主题全部为 0） |
  | `body *` 节点数 | **11902**（mobile-360 为 11838） | `automation-events-rule-col.elCount` |
  | `.el-main` 滚动高度 | **20472px** / 视口 840px（比值 **24.37**） | `automation-switch-a11y-and-scroll.mainRatio` |
  | 第二张表 `getBoundingClientRect().height` | **20040px** | `tableHeights: [138, 20040]` |
  | 其中 fixed 列额外渲染的 `td` | **502 个** | `.el-table__row td.el-table-fixed-column--right` = 502 |
  | 后端硬上限 | `Limit(500)` | `backend/internal/api/handler_automation.go:460` |
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（触发历史标题右侧显示 `500` 计数徽标，无分页器）、`/tmp/uiux-d3/mobile-360-light-automation.png`、`/tmp/uiux-d3f/automation-360-row.png`
- **证据代码**：
  - 视图：`frontend-shared/src/views/automation/AutomationRules.vue:72`
    ```vue
    <el-table :data="events" v-loading="eventsLoading" data-test="events-table">
    ```
    全文件**不含** `<el-pagination>`（`grep -c 'el-pagination' AutomationRules.vue` = 0）。
  - 数据源：`frontend-shared/src/api/automation.ts:191-193` 不传分页参数
    ```ts
    async listEvents(params?: AutomationEventListParams): Promise<AutomationEvent[]> {
      return unwrap<AutomationEvent[]>(client.get('/api/v1/automation-events', { params }))
    }
    ```
    参数类型 `AutomationEventListParams`（`:151-155`）只有 `rule_id` / `result`，**无 page/page_size**。
  - 后端：`backend/internal/api/handler_automation.go:460` `q.Order("id DESC").Limit(500).Find(&items)` —— 500 是硬截断，不是分页。
  - 对照（正确实现）：`frontend-shared/src/views/firmware/FirmwareManage.vue:77-87` 与 `views/config/DeviceConfigList.vue:195-205` 都有 `el-pagination` + `page_size`。
- **实测：数据量远超 500（后端已在静默截断）**（交叉筛选法，`uiux-d3-probe` 计数）：
  | 查询 | 返回条数 |
  |---|---|
  | `/automation-events`（无过滤） | **500（= 上限）** |
  | `?rule_id=1` / `?rule_id=3` | 500 / 500（均触顶） |
  | `?result=executed` / `?result=expired` | 500 / 500（均触顶） |
  | `?rule_id=3&result=expired` | **500（仍触顶）** |
  | `?rule_id=1&result=failed_dispatch` | 6 |
  即真实事件数 **> 1006 条**，前端只看到 500 条，且**没有任何「已截断」提示**——用户会以为看到的是全部历史。
- **长时间打开的内存影响（20s 静置实测）**：
  ```json
  {"name":"automation-soak-20s","t0":{"h":900,"els":11902,"memMB":40.6},
   "t1":{"h":900,"els":11902,"memMB":38.5}}
  ```
  **DOM 与高度在 20s 内不增长**（`els` 恒为 11902、`docScrollH` 恒为 900），GC 后堆内存由 40.6MB 降到 38.5MB。**结论：不存在无界内存泄漏**；本条的代价是一次性 **11902 个 DOM 节点 + 20040px 布局高度**的常驻开销，以及 500 行 × 8 列的布局/重绘成本（见下"我未能验证的部分"第 3 条对"卡顿"的限定）。
- **规范条款**：§4.5.4 `SHOULD`「高频实时数据做降采样、**虚拟列表或显示上限**，避免长时间打开页面无限增长」；§3.3.5 `SHOULD`「列表搜索使用统一防抖，筛选变化重置页码；服务端分页时不得把当前页本地筛选伪装成全局检索」；§4.1 资源列表模板固定信息顺序「…列表/表格 -> **分页或空状态**」
- **严重度**：**高**（违反 SHOULD 但影响面明确：这是本域唯一的「高频数据」页面，500 行 × fixed 列的 DOM 规模使移动端首屏渲染与滚动明显变重；且后端静默截断 1006+ 条中的一半而无提示，属"数据不完整却看起来完整"）
- **建议**（最小改动，两步）：
  1. **前端分页**：`api/automation.ts` 的 `AutomationEventListParams` 增加 `page?/page_size?`，`AutomationRules.vue` 事件表下加 `<el-pagination layout="total, sizes, prev, pager, next" :page-sizes="[20,50,100]" :total="eventsTotal">`；筛选（`eventFilterRuleId`/`eventFilterResult`）变化时 `currentPage = 1` 并重新拉取（直接复用 `DeviceConfigList.vue:343-346` 的 `handleFiltersChanged` 模式）。
  2. **后端补分页**：`listAutomationEvents` 接受 `page`/`page_size`，返回 `{items,total}`（与 `/device-configs` 的 `{list,total}` 对齐）；在分页落地前，至少把「仅显示最近 500 条」写进 UI（对照 `FirmwareManage` 的 `layout="total,..."`）。
  3. **不要**用「虚拟列表」作为第一步——数据量级（500 行）用服务端分页即可，符合 §7.3「为低频静态页面引入复杂状态机…」的克制原则。
- **验收方法**：改后断言 `document.querySelectorAll('.el-table')[1].querySelectorAll('.el-table__row').length <= pageSize`；`document.querySelector('.el-main').scrollHeight / clientHeight < 5`；筛选变化后断言首行 `triggered_at` 属于新结果集且请求 URL 含 `page=1`。

---

### #3 【高】自动化页 264 个触控目标小于 36px

- **页面**：`/automation` | **视口**：desktop-1440 / mobile-390 / mobile-360（三档均为 **264** 个） | **主题**：light + dark 相同 | **维度**：D6 响应式与触控
- **现象**：页面共 **264** 个可点击元素小于 36px，其中 **258 个**是事件表操作列里高 **18px** 的 `命令 ID` 链接，另有行内「触发/编辑/删除」在桌面为 **30×18**、在 390/360 为 **36×21**。
- **证据DOM**（`smallGroups` 聚合，`uiux-d3-probe.mjs`；尺寸为 `getBoundingClientRect()` 取整）：

  | 元素 | 桌面 1440 | 移动 390/360 | 数量 | 说明 |
  |---|---|---|---|---|
  | `button.el-button--primary`（命令ID链接） | **214–293 × 18** | **266–293 × 21** | **258** | 事件表每行一个，500 行 |
  | `div.el-switch.is-checked` | **40 × 32** | **40 × 32** | 2 | 规则表「启用」列 |
  | `button.el-button--warning`（触发） | **30 × 18** | **36 × 21** | 2 | 规则表行操作 |
  | `button.el-button--primary`（编辑） | **30 × 18** | **36 × 21** | 2 | 规则表行操作 |
  | `button.el-button--danger`（删除） | **30 × 18** | **36 × 21** | 2 | 规则表行操作 |
  | `button.el-button--small`（刷新） | **30 × 18** | **36 × 21** | 1 | 触发历史工具栏 |
  | `button.el-button--primary`（创建规则） | **108 × 32** | 113 × 32 | 1 | 卡片头主操作 |
  | 页头三图标按钮 | **32 × 32** | 32 × 32 | 3 | 折叠/通知/主题（**跨域共用，已由 D4 域报 #3**） |
  | `div.el-dropdown` | 32 × 32 | 32 × 32 | 1 | 用户菜单 |
  | **合计** | | | **264** | |

  按高度聚合（桌面）：`{"18": 257, "32": 7}` —— **257/264 的问题是「行高 18px」这一个根因**。
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（操作列「触发 编辑 删除」三连明显小于相邻正文）、`/tmp/uiux-d3/mobile-360-light-automation.png`
- **证据代码**：
  - `frontend-shared/src/views/automation/AutomationRules.vue:47-52`（操作列，`size="small"` + `link`）
    ```vue
    <el-table-column label="操作" width="180" fixed="right">
      <template #default="{ row }">
        <el-button link type="warning" size="small" ...>触发</el-button>
        <el-button link type="primary" size="small" @click="openEdit(asRule(row))">编辑</el-button>
        <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
    ```
  - `AutomationRules.vue:96-98`（命令 ID 链接，同样 `link`+`size="small"`）
  - `AutomationRules.vue:44`（规则表开关）：`<el-switch :model-value="row.enabled" ... />` —— Element Plus 默认 `el-switch` 高 32px，未做移动端放大
  - 全局**没有**针对 `link` 按钮的移动端热区兜底：`App.vue:62-104` 的 `@media` 只放大 `font-size`，不放大热区。
- **规范条款**：§4.4.5 `MUST`「移动端图标按钮和 Switch/Slider 的实际可点击区域**不小于 44x44px**；仅在极高密度、**非主要任务**的工具栏中可降到 36px，并保证相邻间距与可访问名称」；§4.4.7 `SHOULD`「在 `pointer: coarse` 环境增加紧凑控件的可点击区域与行间距」
- **严重度**：**高**（违反 MUST；264 个中 258 个是**行内主要操作**（跳转命令详情），不是"非主要任务工具栏"；移动端 36×21 仍低于 36px 下限的**高度**维度）
- **建议**（最小改动，按收益排序）：
  1. **行操作**：在 `@media (max-width: 768px)` 内给 `.el-table .el-button.is-link` 加 `min-height: 36px; padding: 8px 6px;`（保持视觉紧凑、只扩热区），并用 `.el-table .cell` 的 `line-height` 让行高随之增长到 44px 以上。
  2. **命令 ID 链接**：单行 258 个 18px 高链接是最主要的违规源。因其内容为长 UUID，建议改为**整格可点**（给 `td` 挂 `@click` + `role="link"` `tabindex="0"`，见 #17 的键盘要求），或直接改成截断文本 + 复制按钮（§4.3.2.5「长 ID…可以截断，但需保留复制」）。
  3. **Switch**：移动端给 `.el-switch` 设 `min-height: 44px`（视觉不变，热区变大），或改用纵向行列表（§4.3.2.3）。
  4. 页头三图标按钮属 D4 域已报项，本域不重复计。
- **验收方法**：`[...document.querySelectorAll('button, .el-switch')].filter(el => { const r = el.getBoundingClientRect(); return r.width > 0 && (r.width < 36 || r.height < 36) }).length` 在 390/360 下应为 **0**（页头三按钮除外，归 D4 域）；同时断言 `document.elementFromPoint` 在按钮 44×44 外扩区域内的命中仍是该按钮。

---

### #4 【高】配置模板页 360px 工具栏「导入」按钮被裁切 11px

- **页面**：`/device-configs` | **视口**：**mobile-360**（390 起消失） | **主题**：light + dark **均复现** | **维度**：D6 响应式与触控 / D1 布局
- **现象**：360px 下工具栏「导入」按钮左边缘超出其祖先 `.el-card__body` 的左内边距，被裁掉 **11px**；按钮文字左侧笔画可见缺失。
- **证据DOM**（`uiux-d3-probe.mjs` `clipped`，含**完整祖先链**）：
  ```json
  {"tag":"button","cls":"el-button","text":"导入",
   "self":{"w":77,"h":32,"x":10,"y":369,"right":87,"bottom":401},
   "overBy":11,"scrollableX":false,
   "chain":[
     {"tag":"div","cls":"filter-right","ox":"visible","oy":"visible","cw":270,"sw":270,
      "box":{"w":270,"h":32,"x":41,"y":369,"right":311,"bottom":401}},
     {"tag":"div","cls":"filter-bar","ox":"visible","oy":"visible","cw":270,"sw":270,
      "box":{"w":270,"h":208,"x":41,"y":193,"right":311,"bottom":401}},
     {"tag":"div","cls":"el-card__body","ox":"auto","oy":"auto","cw":310,"sw":310,
      "box":{"w":310,"h":240,"x":21,"y":177,"right":331,"bottom":417}}]}
  ```
  **关键判据**：裁切祖先 `.el-card__body` 的 `cw=310 == sw=310`，即 `scrollWidth == clientWidth`，**它自身不可横向滚动** → 按契约 §2.2 的排除规则，这是**真实裁切**而非正常横滚。
- **独立复核**（`uiux-d3-probe3.mjs` `device-configs-360-toolbar`）：
  ```json
  {"buttons":[{"text":"导入","w":77,"h":32,"left":10,"right":87},
              {"text":"导出","w":77,"h":32,"left":107,"right":184},
              {"text":"新建模板","w":107,"h":32,"left":204,"right":311}],
   "filterRightStyle":{"ox":"visible","display":"flex","flexWrap":"nowrap","justifyContent":"flex-end","width":"270px"},
   "filterRightBox":{"w":270,"left":41,"right":311},
   "cardBodyBox":{"w":310,"left":21,"right":331,"cw":310,"sw":310,"paddingLeft":"20px"},
   "docScrollW":360,"docClientW":360}
  ```
  三个按钮总宽 `77+77+107+2×8(gap) = 277px > 270px` 的 `.filter-right` 宽度；`justify-content:flex-end` 使溢出量全部推向左端（41−10=31px 超出，其中 20px 是 padding、**11px** 真正越出 `.el-card__body` 的内容盒）。
- **证据截图**：`/tmp/uiux-d3/mobile-360-light-device-configs.png`（「导入」左侧被切，「导出」前有异常空隙）、`/tmp/uiux-d3/mobile-360-dark-device-configs.png`、`/tmp/uiux-d3/scenario-mobile-360-light-device-configs-import-clip.png`（主控基线同现象：`clippedCount=1` 双主题）
- **证据代码**：`frontend-shared/src/views/config/DeviceConfigList.vue:816-819`
  ```css
  .filter-bar { flex-direction: column; align-items: stretch; }
  .filter-left { flex-direction: column; align-items: stretch; }
  .filter-left, .filter-right { width: 100%; }
  .filter-right { justify-content: flex-end; }
  ```
  `.filter-right`（`:646-649`）是 `display:flex; gap:8px` 且**无 `flex-wrap`**，窄屏下三个按钮的总宽超过容器但被 `justify-content:flex-end` 向左溢出——`overflow: visible` 时父级 `.el-card__body` 的 `overflow:hidden`（Element Plus 卡片默认）把它裁掉。
- **规范条款**：§4.4.1 `MUST`「先保证 **360px 宽可完成核心操作**」；§4.4.6 `MUST`「页头在小屏允许标题与操作区换行；操作区不能把标题压至零宽或逐字竖排」
- **严重度**：**高**（违反 MUST；「导入」是本页两项批量入口之一，被裁切后在 360px 下命中区与视觉都不完整，且主控基线已独立标为需修项）
- **建议**（最小改动，二选一）：
  1. 给 `.filter-right` 加 `flex-wrap: wrap;`（与 `.filter-left` 的 `flex-wrap:wrap`（`:631`）保持一致），三个按钮在放不下时换行；这是**一行改动**。
  2. 或把三个按钮在窄屏改为 `flex: 1 1 0` 等分（`width:100%` 容器下各占 1/3），避免任何溢出。
  3. **不要**用 `transform: scale()` 缩小按钮——会进一步违反 #3 的触控尺寸要求。
- **验收方法**：`document.querySelector('.filter-right button').getBoundingClientRect().left >= document.querySelector('.el-card__body').getBoundingClientRect().left`，且探针 `clippedCount === 0`（360 与 390 双主题）。

---

### #5 【高】配置模板「编辑」对话框展示创建专属的传感器驱动级联

- **页面**：`/device-configs` | **视口**：desktop-1440（`overlayDialogScrollMax=86`） | **主题**：light + dark | **维度**：D5 业务完整交互流程
- **现象**：点击「编辑」打开的是 `编辑配置模板`，但表单里**仍然包含**创建时的三层「传感器驱动」级联选择器（OEM→种类→型号），并且会把已存的驱动路径回显成一条 **API 不存在的伪路径**。
- **证据DOM**（`uiux-d3-probe19.mjs` `device-configs-edit-dialog`）：
  ```json
  {"title":"编辑配置模板",
   "labels":["模板名称","默认","传感器驱动","硬件类型","通讯协议","波特率","数据位","停止位","校验位","从机地址","超时(ms)","描述"],
   "cascaderPresent":true,
   "cascaderValue":"通用 / audit_probe_type / 审计临时模板",
   "dialog":{"w":680,"h":801,"top":135,"bottom":936},
   "overlayDialogScrollMax":86}
  ```
  `cascaderValue` 的三段分别是**兜底 OEM「通用」**、`device_type`、以及**模板名称**——即驱动选择器被填成了一条后端从未返回过的路径（本次审计创建的模板 `device_type=audit_probe_type`，驱动树里并无此项）。
- **证据截图**：`/tmp/uiux-d3f/dc-edit-dialog.png`
- **证据代码**：
  - `frontend-shared/src/components/forms/DeviceConfigForm.vue:26-49`（**创建与编辑共用同一对话框**，无 `v-if="!isEdit"`）
    ```vue
    <el-form-item label="传感器驱动" prop="driverPath">
      <el-cascader v-model="form.driverPath" :options="driverOptions" ...>
    ```
  - `:247` `const isEdit = computed(() => !!props.config?.id)` —— `isEdit` **只用于按钮文案**（`:198` `{{ isEdit ? '保存' : '创建' }}`），未用于字段裁剪。
  - `:386-399` 编辑回显时用 `findDriverPath(form.device_type)` 反查：`findDriverPath` 在驱动树里按 `driver.value === deviceType` 匹配，**匹配不到返回 `[]`**（`:383`），而级联的 `label` 仍会把裸 `device_type` 渲染出来。
  - 提交时 `:345-353` 的 `submitData` **不含 `driverPath`**，即该字段在编辑态**只显示不提交**——用户改了它，UI 接受，保存后无任何变化、也无提示。
- **规范条款**：§4.3.2.2 `MUST`「**创建与编辑的交互模型分离**：编辑不得展示或接受创建专属的节点、**解析器**、设备型号等字段，除非后端确实支持修改且设计明确」；§3.2.6 `MUST`「对会改变查询范围的输入…重置或失效其派生状态」；§3.4.4 `MUST`「禁用操作给出原因」
- **严重度**：**高**（违反 MUST；这是"编辑接受了一个后端不支持的字段"的典型——用户以为改了驱动，实际保存后被静默丢弃，属 §1.2.1「不能伪造设备事实」的同族缺陷）
- **建议**（最小改动）：
  1. 在 `DeviceConfigForm.vue:26-49` 的 `el-form-item label="传感器驱动"` 上加 `v-if="!isEdit"`；编辑态改为**只读展示**：
     ```vue
     <el-form-item v-else label="传感器驱动">
       <span class="readonly-value">{{ props.config?.device_type || '—' }}</span>
       <span class="hint">驱动类型创建后不可修改</span>
     </el-form-item>
     ```
  2. 若后端确实允许改（`PUT /device-configs/:id` 未把 `device_type` 列入白名单，见 `frontend-shared/src/api/deviceConfig.ts:70-82` 的 `UpdateDeviceConfigParams`），则应把该字段加入提交载荷；**当前证据支持"改前端"而非"改后端"**。
- **验收方法**：打开编辑对话框断言 `.el-cascader` 数量为 0（或 `v-if` 分支生效），且表单中不存在任何可编辑但不在 `submitData` 中的字段（可用「表单控件 name 集合 ⊆ 提交载荷 key 集合」做静态断言）。

---

### #6 【高】固件编辑「目标型号」保存被后端静默忽略（字段名不匹配）

- **页面**：`/firmware` | **视口**：所有 | **主题**：light + dark | **维度**：D5 业务完整交互流程 / D3 反馈
- **现象**：编辑固件对话框里的「目标型号」输入框可以随便改，点「保存」提示「更新成功」，但**数据库里的值从未改变**。前端发的字段名是 `target_model`，后端读取的是 `node_model`。
- **证据（真实 API 闭环，非推断）**：
  ```
  # 1. 上传一条：target_model=MODEL-ORIG
  POST /api/v1/firmwares/upload   → 201 {"id":1,...,"target_model":"MODEL-ORIG",...}

  # 2. 用「前端实际发送的载荷形状」PUT
  PUT /api/v1/firmwares/1  -d '{"version":"9.9.9","target_model":"MODEL-CHANGED-BY-FRONTEND","changelog":"probe","stable":true}'
  → 200 {"code":200,...,"target_model":"MODEL-ORIG",...}     # 响应里 target_model 仍是旧值

  # 3. 重新 GET 确认持久化结果
  [{ "id":1, "version":"9.9.9", "target_model":"MODEL-ORIG", "changelog":"probe", "stable":true }]
  #                                              ^^^^^^^^^^^^^^^^^^^^^^^ 未被修改；changelog/stable 生效

  # 4. 改用后端字段名 node_model 才生效
  PUT /api/v1/firmwares/1  -d '{"node_model":"MODEL-VIA-NODE_MODEL"}'
  → [{"id":1,"target_model":"MODEL-VIA-NODE_MODEL"}]         # 生效
  ```
- **证据DOM**：不适用（缺陷在网络层，UI 全程显示"成功"）。可见反馈为 `ElMessage.success('更新成功')`。
- **证据截图**：不适用（无可见差异——这正是问题所在）。对话框结构见 `/tmp/uiux-d3b/upload-firmware.png`（同页 `el-dialog`），字段清单见 `uiux-d3-probe2.mjs` `upload-dialog.labels:["版本号","目标型号","固件文件"]`
- **证据代码**：
  - 前端视图：`frontend-shared/src/views/firmware/FirmwareManage.vue:139-141`
    ```vue
    <el-form-item label="目标型号">
      <el-input v-model="editForm.target_model" placeholder="留空表示通用" />
    ```
  - 前端 store：`frontend-shared/src/stores/firmware.ts:53`
    ```ts
    async function update(id, data: { version?: string; changelog?: string; target_model?: string; stable?: boolean })
    ```
  - 前端 API：`frontend-shared/src/api/firmware.ts:38` `async update(id, data: { version?: string; changelog?: string })` —— 类型定义里**没有** `target_model`，靠 TS 的结构化宽化逃过检查（`stores/firmware.ts:290-295` 传入的对象含 `target_model`）。**这是 §3.2.3「新增 API 方法时同时定义请求、响应和异常语义」的直接违反。**
  - 后端：`backend/internal/api/handler_ota.go:218-235`
    ```go
    var req struct {
        Version        *string `json:"version"`
        Changelog      *string `json:"changelog"`
        NodeModel      *string `json:"node_model"`      // ← 期望 node_model
        MinFromVersion *string `json:"min_from_version"`
        Stable         *bool   `json:"stable"`
    }
    ...
    if req.NodeModel != nil { updates["target_model"] = *req.NodeModel }
    ```
    前端发 `target_model` → `req.NodeModel == nil` → `updates` 中无该键 → **静默不写**，同时 `db.Model(&firmware).Updates(updates)` 不报错，接口返回 200。
  - 同一 handler 里 `MinFromVersion` 也从未被前端使用（`grep -rn 'min_from_version' frontend-shared/src/` = 0），属同族但无 UI 影响。
- **规范条款**：§3.2.3 `MUST`「在新增 API 方法时同时定义请求、响应和异常语义」；§3.2.1 `MUST`「按'视图 -> composable/store -> api -> 后端'的方向组织依赖」；§1.2.1「界面不能用…前端默认值伪造设备事实」；§3.4.6 `禁止`「把请求失败 catch 后静默吞掉」的精神（此处是"请求成功但字段被吞"）
- **严重度**：**高**（数据完整性：用户以为修改了固件适用的目标型号，实际未修改。若某型号设备据此选固件，会取到错误版本。且全程无任何错误提示）
- **建议**（最小改动）：
  1. **前端对齐后端**：`api/firmware.ts:38` 的请求类型补上 `target_model?: string; min_from_version?: string`，并在方法内做一次显式映射
     ```ts
     async update(id: number, data: FirmwareUpdateRequest): Promise<void> {
       const body: Record<string, unknown> = {}
       if (data.version !== undefined) body.version = data.version
       if (data.changelog !== undefined) body.changelog = data.changelog
       if (data.stable !== undefined) body.stable = data.stable
       // 后端字段名为 node_model（handler_ota.go:220），UI 术语保持「目标型号」
       if (data.target_model !== undefined) body.node_model = data.target_model
       await client.put('/api/v1/firmwares/' + id, body)
     }
     ```
  2. **后端加校验**（防御性）：`handler_ota.go:223` 的 `c.ShouldBindJSON(&req)` 目前忽略返回值，未知字段被静默丢弃。建议改为 `if err := c.ShouldBindJSON(&req); err != nil { Error(c, 400, ...) }`，并在响应里回传实际应用的字段集合。
  3. 统一命名：长期应收敛为单一字段名（需后端迁移，属 §7.6「不要一次性大重写」范围，可列入台账）。
- **验收方法**：API 契约测试——`PUT /api/v1/firmwares/:id {target_model:"X"}` 后 `GET` 断言 `target_model === "X"`；或前端行为测试断言 `client.put` 收到的 body 含 `node_model`。

---

### #7 【高】告警规则接口失败 → 整页 ErrorBoundary，页面与导航全部消失

- **页面**：`/alerts` | **视口**：所有 | **主题**：light + dark | **维度**：D3 人机反馈
- **现象**：`/api/v1/alert-rules` 返回 500（或网络失败）时，**整个页面被 `ErrorBoundary` 替换**成一个居中的 `ApiError / Network Error` 结果页，连侧边栏和页头都不见了，用户无法从这一页导航到任何其他地方。
- **证据DOM**（`uiux-d3-probe3.mjs` `alerts-error-boundary`，500 注入）：
  ```json
  {"path":"/alerts",
   "resultTitle":"ApiError",
   "resultSub":"audit injected 500",
   "pageHeader":false,
   "sidebarPresent":false,
   "stack":"ApiError: audit injected 500 at .../assets/AlertRules-BkHGBdwm.js:1:579 at async Pr..."}
  ```
  对照 `uiux-d3-probe2.mjs` 用 `abort()` 触发时同样得到 `resultTitle:"ApiError"`、`resultSub:"Network Error"`、`sidebarPresent:false`。
  控制台原文：`[ERROR] ErrorBoundary 捕获组件错误 {name: ApiError, message: Network Error, stack: ...AlertRules-BkHGBdwm.js:1:4354}`
- **证据截图**：`/tmp/uiux-d3c/alerts-boundary.png`（全屏只有错误结果页，无侧栏无页头）、`/tmp/uiux-d3b/apifail-alerts.png`
- **证据代码**：
  - 根因在 store 的 `finally` 之外**没有 catch**，异常直接冒泡到 Vue 渲染层：
    `frontend-shared/src/stores/alert.ts:27-35`
    ```ts
    async function fetchRules() {
      rulesLoading.value = true
      try {
        rules.value = await alertApi.listRules()
      } finally {                    // ← 只有 finally，没有 catch
        rulesLoading.value = false
      }
    }
    ```
    同文件 `fetchEvents`（`:67-74`）同样只有 `finally`。
  - 页面 `onMounted`（`frontend-shared/src/views/alert/AlertRules.vue:280-288`）用 `await Promise.all([...])` 调用，**未 try/catch**：
    ```ts
    onMounted(async () => {
      await Promise.all([store.fetchRules(), store.fetchEvents()])
      ...
    })
    ```
    未捕获的 `onMounted` reject 被上层 `ErrorBoundary` 接住并替换整棵子树。
  - 对照（**正确实现**）：`views/data-source/DataSourceList.vue:404-412`、`views/automation/AutomationRules.vue:278-287`、`views/firmware/FirmwareManage.vue:207-221` 都在页面层 catch 并转成 `ElMessage`/`store.error`。
  - `ErrorBoundary.vue:4-9` 本身提供了「重试/刷新页面」两条路径，但**它替换的是 `<router-view>` 的整棵子树**（`App.vue:3-8` 中 `ErrorBoundary` 包住 `NetworkBanner + RouteProgressBar + router-view`），因此 `MainLayout` 也被替换掉。
- **规范条款**：§3.4.2 `MUST`「区分：首次加载、后台刷新、无数据、筛选无结果、权限不足、离线、**接口失败**和操作失败。**空状态不是错误状态的替代品**」；§3.4.6 `禁止`「把请求失败 catch 后静默吞掉…禁止把网络失败、429、401、字段校验失败都显示为『密码错误』或『操作失败』」；§1.2.5「失败可理解、可恢复」
- **严重度**：**高**（违反 MUST；页面级 API 失败导致**整个应用外壳丢失**，是"失败不可恢复"的极端形态。对比同域 `data-sources` 仅显示顶部 `el-alert` + 「重试」，说明这是 `/alerts` 独有的实现缺陷而非框架限制）
- **建议**（最小改动，两层防御）：
  1. **store 层**：给 `alert.ts` 的 `fetchRules`/`fetchEvents` 加 `catch`，写入 `error` ref（照抄 `stores/dataSource.ts:47-59` 的 `captureError` 模式）并 `throw` 或返回，由页面决定呈现。
  2. **页面层**：`AlertRules.vue:280-288` 的 `onMounted` 改为
     ```ts
     onMounted(async () => {
       try { await Promise.all([store.fetchRules(), store.fetchEvents()]) }
       catch { /* store.error 已记录 */ }
       ...
     })
     ```
     并在模板顶部（`AlertRules.vue:3` 的 `<PageHeader>` 之后）加一条与 `DataSourceList.vue:30-42` 同款的错误 `el-alert`（含「重试」按钮）。
  3. **框架层**（可选，影响全站）：把 `App.vue` 的 `ErrorBoundary` 下移到 `MainLayout` 的内容槽内，使外壳在任何页面级错误下仍然可用——此项属跨域改动，建议报主控统一决策。
- **验收方法**：注入 500 后断言 `document.querySelector('aside, .sidebar') !== null` 且 `document.querySelector('.el-alert--error')` 非空且含「重试」按钮；`.el-result` 应为 0。

---

### #8 【高】配置模板接口失败 → KPI 渲染 0 + 显示「暂无配置模板」初始空态

- **页面**：`/device-configs` | **视口**：所有 | **主题**：light + dark | **维度**：D3 人机反馈
- **现象**：`/api/v1/device-configs` 返回 500 时，页面把失败当成"零数据"：4 个统计卡全部渲染 `0`，列表区显示「**暂无配置模板** / 创建模板后，可为边缘设备复用连接、解析与初始化配置。」这一**首次使用才该出现**的空态，并且 `EmptyState kind="initial"`（带「新建模板」行动号召）。唯一线索是一条 3 秒后消失的 `ElMessage`。
- **证据DOM**（`uiux-d3-probe18.mjs` `device-configs-500`，500 注入）：
  ```json
  {"emptyState":1,"emptyStateKind":["empty-state default initial"],
   "emptyTitle":"暂无配置模板",
   "emptyDesc":"创建模板后，可为边缘设备复用连接、解析与初始化配置。",
   "errorAlert":0,"elResult":0,"retryButtons":[],
   "elMessages":["获取配置模板列表失败"],
   "statCards":[{"label":"模板总数","value":"0"},{"label":"本页启用","value":"0"},
                {"label":"总线类型","value":"0"},{"label":"设备类型","value":"0"}]}
  ```
  **关键对照**：全量基线探针在**正常**状态下同一页面 `emptyState=0`。即失败态与"新装无数据"态**完全无法区分**。
- **证据截图**：`/tmp/uiux-d3f/err500-device-configs-500.png`
- **证据代码**：
  - `frontend-shared/src/views/config/DeviceConfigList.vue:305-323`（catch 里只弹消息，不置错误态）
    ```ts
    const fetchConfigs = async () => {
      loading.value = true
      try {
        const response = await deviceConfigApi.getList({...})
        configs.value = response.list || []
        total.value = response.total || 0      // ← 失败时 total 保持上一轮的 0
        updateStats()
      } catch (error: any) {
        ElMessage.error('获取配置模板列表失败')   // ← 唯一的错误呈现，3s 后消失
      } finally { loading.value = false }
    }
    ```
  - 空态判定（`:183-192`）只看数据长度，不看错误：
    ```vue
    <EmptyState v-if="!loading && filteredConfigs.length === 0"
      :kind="hasActiveFilters ? 'filtered' : 'initial'" />
    ```
    失败时 `hasActiveFilters=false` → 走 `'initial'` 分支。
  - KPI 由 `stats`（`:271-276`）驱动，`updateStats` 不会被调用，`stats` 保持全 0。
  - `EmptyState.vue:50` 的 props 契约**已经支持** `kind: 'error'`（`:154-157` 有 `.empty-state.error` 的红色插图样式），**实现方未使用**。
- **规范条款**：§4.3.3.1 `MUST`「使用 `EmptyState` 的 `initial`、`filtered`、`empty`、**`error`**、`permission` 语义，或保持等价区分」；§3.4.2 `MUST`「**空状态不是错误状态的替代品**」；§1.2.1「界面不能用…前端默认值伪造设备事实；未知应明确显示"未知"或"—"，并给出可用时的原因或补救入口」
- **严重度**：**高**（违反 MUST；用户看到 `0` 个模板会认为"配置被清空了"，这是把**基础设施故障**伪装成**业务事实**，与主控广播 2 的 D1 域阻断项同模式）
- **建议**（最小改动）：
  1. 增加 `const loadError = ref<string | null>(null)`，在 `catch` 里 `loadError.value = '加载配置模板失败'` 并**保留**（不清空）；成功时置 `null`。
  2. 模板改为三态路由（`:183-192`）：
     ```vue
     <EmptyState v-if="loadError" kind="error" title="加载配置模板失败"
       :description="loadError" :quick-actions="[{ label: '重试', type: 'primary', handler: fetchConfigs }]" />
     <EmptyState v-else-if="!loading && filteredConfigs.length === 0" ... />
     ```
  3. KPI 在失败时显示 `—` 而非 `0`——直接复用 `DataSourceList.vue:388-392` 的 `metricsReady` 模式：
     ```ts
     const metricsReady = computed(() => loaded.value && !loadError.value)
     function metric(v: number) { return metricsReady.value ? v : '—' }
     ```
- **验收方法**：注入 500 后断言 `.empty-state.error` 存在、`.stat-value` 文本全为 `—`、页面存在「重试」按钮；再断言正常态下 `.empty-state` 为 0。

---

### #8b 【参考 · 不计入】同域对照：`data-sources` 的失败处理基本正确，但错误与空态重叠

同一注入条件下 `/data-sources` 的表现（`uiux-d3-probe18.mjs` `data-sources-500`）：
```json
{"errorAlert":1,
 "errorText":["加载数据源失败：audit injected 500重试"],
 "statCards":[{"label":"总来源","value":"—"},{"label":"权威","value":"—"},
              {"label":"待命","value":"—"},{"label":"熔断","value":"—"}],
 "emptyDesc":"创建数据源后，可按健康度自动切换权威来源。"}
```
- 顶部常驻 `el-alert--error` + 「重试」按钮（`DataSourceList.vue:30-42`）
- 4 个 KPI 全部显示 `—`（`DataSourceList.vue:388-392` 的 `metric()`）
- **这是本域唯一实现完整的错误态**，建议作为 #7/#8/#19 的修复参照。
- **残留问题（计入 #22）**：KPI 显示 `—` 的同时，**空态「暂无数据源」仍然渲染**（`emptyState=1`），错误与空态同时出现，语义重叠（§4.1.3「不可把同一事实在页面级空态、卡片空态、表格空态重复呈现」）。建议在 `DataSourceList.vue:183-191` 的 `EmptyState` 上加 `v-if="!store.error && ..."`。

---

### #9 【高】亮色主题浅底徽标与主按钮对比度 2.04–2.78:1

- **页面**：本域全部 5 页（automation / alerts / device-configs / data-sources / firmware） | **视口**：所有 | **主题**：**light 不达标 / dark 达标** | **维度**：D2 配色与主题
- **现象**：亮色主题下，`el-tag` 各语义类型、主按钮白字、表头文字、统计卡标签、页头副标题的实际前景/背景对比度均在 **2.04–3.08:1**，低于 WCAG AA 正文 4.5:1（表头/副标题 14px 亦低于 4.5:1）。暗色主题下同一组元素为 **4.53–14.42:1**，全部达标。
- **证据DOM**（`uiux-d3-probe15.mjs`，`getComputedStyle` 实际色值 + WCAG 相对亮度公式计算）：
  | 元素 | 主题 | 前景 | 背景 | 对比度 | AA 4.5 |
  |---|---|---|---|---|---|
  | `el-tag--success`（已执行） | light | `rgb(103,194,58)` | `rgb(240,249,235)` | **2.08** | FAIL |
  | `el-tag--warning`（设备动作） | light | `rgb(230,162,60)` | `rgb(253,246,236)` | **2.04** | FAIL |
  | `el-tag--danger`（已过期） | light | `rgb(245,108,108)` | `rgb(254,240,240)` | **2.61** | FAIL |
  | `el-tag`（primary，计数徽标） | light | `rgb(64,158,255)` | `rgb(236,245,255)` | **2.53** | FAIL |
  | `.el-button--primary`（创建规则/新建模板） | light | `rgb(255,255,255)` | `rgb(64,158,255)` | **2.78** | FAIL |
  | `.el-table th`（表头） | light | `rgb(144,147,153)` | `rgb(255,255,255)` | **3.08** | FAIL |
  | `.stat-label` / `.page-header-subtitle` | light | `rgb(144,147,153)` | `rgb(255,255,255)` | **3.08** | FAIL |
  | `.el-table__empty-text`（空态文案） | light | `rgb(144,147,153)` | `rgb(255,255,255)` | **3.08** | FAIL |
  | `el-tag--success` | dark | `rgb(103,194,58)` | `rgb(28,37,24)` | 7.05 | PASS |
  | `el-tag--warning` | dark | `rgb(230,162,60)` | `rgb(41,34,24)` | 7.18 | PASS |
  | `el-tag--danger` | dark | `rgb(245,108,108)` | `rgb(42,29,29)` | 5.61 | PASS |
  | `.el-table th` | dark | `rgb(229,234,243)` | `rgb(30,30,30)` | 13.81 | PASS |
  | `.stat-label` | dark | `rgb(141,144,149)` | `rgb(41,41,43)` | 4.53 | PASS |
  原始色值样例：`{"sel":".el-tag","text":"2","color":"rgb(64,158,255)","bg":"rgb(236,245,255)","fontSize":"12px","fontWeight":"600","ratio":2.53}`
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（「已执行」绿标签、「设备动作」橙标签在浅底上明显发灰）、`/tmp/uiux-d3/desktop-1440-dark-automation.png`（同位置清晰）；`/tmp/uiux-d3-kbd/desktop-1440-light-*.png` 全套
- **证据代码**：颜色来自 Element Plus 默认调色板，项目**未覆写**亮色主题下的语义色 token —— `frontend-shared/src/styles/theme.css:19-27` 只定义了 `--color-primary/-success/-warning/-danger/-info` 的**基色**：
  ```css
  --color-primary: #409eff;
  --color-success: #67c23a;
  --color-warning: #e6a23c;
  --color-danger: #f56c6c;
  ```
  而 `el-tag` 的背景取自 Element Plus 的 `--el-color-{type}-light-9`（`#ecf5ff` 等），两者对比度不足。`theme.css` 的暗色段（`:113-223`）通过 `html.dark` 提供了可读的浅色文字，**亮色段没有做等价处理**。项目自己的语义 token 桥接在 `theme.css:85-95`（`--status-online` 等）也已存在，只是 `el-tag`/`el-button` 未消费。
- **规范条款**：§4.5.1 `MUST`「每次触及全局颜色、卡片、表格、弹窗、popover、终端或图表时，**验证亮色与暗色**」；§4.2.1 `MUST`「状态同时使用文字和颜色」的**可读性前提**；§3.6.1 `MUST`「DOM/CSS 颜色、表面、文字、边框、阴影使用 Element Plus CSS 变量或 `theme.css` 的语义 token」（当前问题本质是 token 的可读性不足，不是未使用 token）
- **严重度**：**高**（违反 MUST；本域所有事件状态标签（已执行/已过期/冷却抑制）都靠 `el-tag` 呈现，2.04–2.61 的对比度在普通显示器上已接近不可辨，属"状态不可读"）
- **与 D2 域的关系**：D2/D4 域已报同族问题（`el-tag--success` 2.08、`el-tag--warning` 2.04，见主控广播 2 第三条）。**本域是同一根因在配置管理页的具体清单**，含 4 类此前未列的元素：`.el-button--primary` 2.78、`.el-table th` 3.08、`.stat-label`/`.page-header-subtitle` 3.08、`.el-table__empty-text` 3.08。**若主控已并入 D2 域统一整改，本条可降级为"本域实例清单"；我按契约"每条结论必须给证据"的要求独立列出，不重复计严重度。**
- **建议**（最小改动，改 token 而非改组件）：
  1. 在 `theme.css` 的亮色段覆写 Element Plus 的 `light-9` 家族，使前景/背景对比度 ≥4.5:1。以 success 为例，把 `--el-color-success-light-9` 由 `#f0f9eb` 加深到 `#e1f3d8` 并同时把 `--el-color-success` 由 `#67c23a` 加深到 `#4e9a2f`（预期对比度 ≥4.5）。
  2. 主按钮：`--el-color-primary` 在亮色下由 `#409eff` 加深到 `#2b7fd4`（白字对比度约 4.5:1）；或在 `theme.css` 给 `.el-button--primary` 单独加 `--el-button-text-color` 为深色。
  3. 次级文字（`th`/`stat-label`/副标题/空态）由 `--el-text-color-secondary`（`#909399`，3.08:1）调整为 `--el-text-color-regular`（`#606266`，约 6.1:1）——注意这会影响全站，建议报主控与 D2 域合并决策。
- **验收方法**：对上述每个选择器计算 `ratio(getComputedStyle(el).color, 有效背景色) >= 4.5`（正文）或 `>= 3.0`（≥18.66px 或 ≥14px 粗体）；亮暗两套均需通过。可直接复用 `tools/uiux-d3-probe15.mjs` 的 `__ratio()` 实现。

---

### #10 【中】数据源详情抽屉固定 560px：360px 下左移 200px，全部 label 不可见

- **页面**：`/data-sources` | **视口**：**mobile-360**（左移 200px）/ mobile-390（左移 170px） | **主题**：light + dark 相同 | **维度**：D6 响应式与触控 / D1 布局
- **现象**：点击数据源名称打开「数据源详情」抽屉，抽屉宽度恒为 **560px**（`:size="560px"` 硬编码），在任何窄于 560px 的视口下**左边缘被推出屏幕**。360px 下 `left = -200`（有 200px 在屏幕外），标题「数据源详情」和**全部 15 个字段 label 都渲染在 `left: -179` 处，完全不可见**，只剩下右半边的一列值。
- **证据DOM**（`uiux-d3-probe13.mjs` `drawer4-360`）：
  ```json
  {"drawer":{"left":-200,"right":360,"w":560},
   "viewportW":360,
   "hiddenLeftPx":200,
   "visibleFractionPct":64.29,
   "header":{"left":-200,"right":360,"w":560},
   "headerText":"数据源详情",
   "headerOffscreen":true,
   "labels":[{"text":"ID","left":-179,"right":42,"w":222},
             {"text":"逻辑设备","left":-179,"right":42,"w":222},
             {"text":"数据类别","left":-179,"right":42,"w":222},
             {"text":"来源边缘设备","left":-179,"right":42,"w":222}],
   "labelsVisible":0,"labelsTotal":4,
   "closeBtn":{"left":320,"right":340,"w":20},"closeBtnOffscreen":false}
  ```
  390px 下同构：`drawer.left=-170`、`hiddenLeftPx:170`、`labelsVisible:0/4`。
  **768px 起恢复正常**（`drawer:{left:208,right:768,w:560}`，完整可见）；1024px 起右对齐（`{left:464,right:1024}`）。
- **证据截图**：`/tmp/uiux-d3f/drawer4-360.png`（只见右半列数字，无任何字段名）、`/tmp/uiux-d3f/drawer3-360.png`、`/tmp/uiux-d3f/drawer3-390.png`
- **证据代码**：`frontend-shared/src/views/data-source/DataSourceList.vue:255`
  ```vue
  <el-drawer v-model="detailVisible" title="数据源详情" size="560px" data-test="ds-drawer">
  ```
  及 `frontend-shared/src/styles/theme.css:369-373` —— 全局兜底**只覆盖 `.el-dialog`，没有覆盖 `.el-drawer`**：
  ```css
  /* 对话框移动端兜底：项目内 el-dialog 普遍硬编码 width（500px/560px 等），
     窄视口下会溢出。全局 clamp 到 92vw，桌面端不受影响。 */
  .el-dialog {
    max-width: 92vw;
  }
  ```
  实测 `getComputedStyle(drawer).maxWidth === "none"`，`width === "560px"`。
- **规范条款**：§4.4.1 `MUST`「先保证 **360px 宽可完成核心操作**」；§4.3.2.3 `MUST`「弹窗使用全局 `max-width: 92vw` 兜底…**Teleport 的弹窗样式必须是全局选择器**」——抽屉与对话框同属 Teleport 弹层，规范虽只点名 dialog，但「窄屏兜底」的意图明确覆盖同类浮层
- **严重度**：**中**（违反 MUST 条款的程度明确，但影响面局限于一个抽屉：表单类对话框在 360px 下经实测是**可达且可用**的，见 #15 的复核；本条是配置域唯一"窄屏功能不可用"的弹层）
- **建议**（最小改动，两处其一或并用）：
  1. **全局兜底**（推荐，一次修复所有抽屉）：`theme.css:371-373` 扩展为
     ```css
     .el-dialog,
     .el-drawer {
       max-width: 92vw;
     }
     ```
  2. **就地修复**：`DataSourceList.vue:255` 的 `size="560px"` 改为响应式 `size="min(560px, 92vw)"`（Element Plus 的 `size` 接受任意 CSS 长度值）。
  3. 若采用 1，还需检查 `.el-drawer` 在 92vw 下 `el-descriptions` 的 `border` 表格是否换行——360px 下 `el-descriptions :column="1"` 会自然单列，无需额外改动。
- **验收方法**：360/390 下断言 `document.querySelector('.el-drawer').getBoundingClientRect().left >= 0` 且 `.el-descriptions__label` 全部 `left >= 0`（当前为 `-179`）。

---

### #11 【中】数据源表格固定操作列遮挡整行，「名称」入口在窄屏不可点

- **页面**：`/data-sources` | **视口**：**mobile-360 与 mobile-390**（`.el-table` 宽 280/310px） | **主题**：light + dark 相同 | **维度**：D5 业务完整交互流程 / D6 响应式
- **现象**：`.el-table` 在窄屏下宽度只有 280px，而「操作」固定列（`fixed="right"`，`width="330"`）就占了 **330px**，**比整张表还宽**；固定列覆盖了表格 100% 的可视宽度。结果是：非固定列全部被挤到滚动区外，**「名称」列（详情入口）在横向滚动到 0 时位于 `left: 388`（390px 视口外）**，用户必须先横向滚动才能点到。
- **证据DOM**（`uiux-d3-probe10.mjs` `measure`，真实数据行）：
  ```json
  {"w":360,
   "tableBox":{"left":36,"right":316,"w":280},"tableCW":280,
   "wrapCW":280,"wrapSW":1270,"wrapOX":"auto",
   "fixedTd":{"left":36,"right":366,"top":690,"bottom":755,"w":330,"h":65},
   "fixedCoversViewportPct":100,
   "nameBtn":{"left":388,"right":484,"w":96,"h":21},
   "nameProbe":{"centerX":436,"centerY":723,"inViewport":false,"hitTag":null,"hitIsNameBtn":null},
   "rowCount":1,"mobileHint":false}
  ```
  390px 下同构：`fixedTd:{left:36,right:366,w:330}`、`nameBtn:{left:388,right:484}`、`nameProbe.inViewport:false`。
  `fixedCoversViewportPct: 100` 意为固定列横向覆盖了表格**全部**可视宽度（`min(330, 280)/280 = 100%`）。
  **Playwright 真实点击失败**（`uiux-d3-probe9.mjs`，超时 30s 后抛错）：
  ```
  locator resolved to <button ... data-test="ds-detail" class="el-button ... is-link">
    - <div class="cell">…</div> from <td class="... el-table-fixed-column--right ..."> subtree intercepts pointer events
    - <span class="">删除</span> from <td class="... el-table-fixed-column--right ..."> subtree intercepts pointer events
  ```
  即固定列的单元格**真实拦截了指针事件**。
  **命中测试的独立佐证**：`uiux-d3-probe10.mjs` 在 768px 下用 `document.elementFromPoint(nameBtn中心)` 得到
  `{"hitTag":"span","hitIsNameBtn":false,"hitIsFixedCol":true}` —— 即使名称按钮在视口内（`inViewport:true`），点到的仍是固定列；1024px 下同一测试才返回 `hitIsNameBtn:true`。
- **证据截图**：`/tmp/uiux-d3f/hit-360.png`（表格区只剩「操作」列一行五个链接，「逻辑设备/类别/名称」等列完全不见）、`/tmp/uiux-d3f/hit-768.png`、`/tmp/uiux-d3f/hit-1024.png`（对照：1024 下名称可见）
- **证据代码**：`frontend-shared/src/views/data-source/DataSourceList.vue:138`
  ```vue
  <el-table-column label="操作" width="330" fixed="right">
  ```
  330px 的固定列在 360px 视口下（表格内容宽 280px）超过表格本身。同文件 `:113-119` 的「名称」列（`min-width="150"`）承载 `data-test="ds-detail"` 详情入口，位于滚动区。
- **规范条款**：§4.3.2.2 `MUST`「重要操作列保持可达；**固定操作列、最小宽度、横向滚动提示和分页换行共同验收**，不能只确认没有页面级 overflow」；§4.4.1 `MUST`「360px 宽可完成核心操作」
- **严重度**：**中**（违反 MUST，但用户**可以**先横向滚动再点击——核心任务未完全阻断；影响"查看详情"这一主要路径的可发现性）
- **建议**（最小改动）：
  1. **收窄固定列**：330px 的五个行操作在窄屏下应收进「更多」下拉（Element Plus `el-dropdown`），固定列降到 **≤120px**。
  2. 或**只在宽屏固定**：`fixed="right"` 改为响应式（如 `:fixed="isWide ? 'right' : false"`），窄屏让操作列跟随横向滚动。
  3. 同时补 `.mobile-table-hint`（见 #13），提示「← 左右滑动查看完整表格 →」。
- **验收方法**：360px 下断言 `document.querySelector('.el-table-fixed-column--right').getBoundingClientRect().width < document.querySelector('.el-table').getBoundingClientRect().width * 0.6`，且对名称按钮中心做 `document.elementFromPoint` 命中自身。

---

### #12 【中】「命令 ID」是死链：点击只打印 console，无任何 UI 反馈

- **页面**：`/automation` | **视口**：所有 | **主题**：light + dark | **维度**：D5 业务完整交互流程 / D3 反馈
- **现象**：触发历史「命令 ID」列渲染为蓝色可点链接（看起来能跳到命令详情），点击后**URL 不变、无对话框、无抽屉、无消息提示**，只在浏览器控制台输出一行 `command id: ...`。
- **证据DOM / 运行时事实**（`uiux-d3-probe19.mjs` `automation-command-id-click`）：
  ```json
  {"link":{"text":"4aa9a48b-7a13-4499-925a-2fdf7b2a2446",
           "box":{"w":229,"h":18,"left":923,"top":507},"tabIndex":0,"ariaLabel":null},
   "after":{"url":"http://127.0.0.1:8082/automation","dialogs":0,"messages":[],
            "notifications":0,"drawers":0},
   "consoleAfterClick":["log: command id: 4aa9a48b-7a13-4499-925a-2fdf7b2a2446"]}
  ```
  即：`url` 与点击前完全相同、四类 UI 反馈计数全为 0、控制台出现 `console.log`。
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（「命令 ID」列为蓝色链接样式，与同页「详情」列的纯文本形成对比）
- **证据代码**：`frontend-shared/src/views/automation/AutomationRules.vue:581-584`
  ```ts
  function goCommand(commandId: string) {
    // TODO: 跳转命令详情页（当前无路由，先留占位）
    console.log('command id:', commandId)
  }
  ```
  模板 `:94-101` 把它渲染成链接：
  ```vue
  <el-table-column label="命令 ID" width="110">
    <template #default="{ row }">
      <el-button v-if="row.command_id" link type="primary" size="small" @click="goCommand(row.command_id)">
        {{ row.command_id }}
      </el-button>
  ```
- **规范条款**：§1.2.5「**失败可理解、可恢复**」与 §3.4.6 的精神——UI 承诺了一个不存在的导航目标；§4.3.2.5 `SHOULD`「长 ID…可以截断，但需保留**复制**、tooltip 或可展开路径」；§3.4.1 `MUST`「使用 `feedback` 或领域对话框统一反馈」（此处完全无反馈）
- **严重度**：**中**（违反 SHOULD 与体验链路完整性；不阻断任务——用户可以不点它；但"看起来可点却什么都不发生"会使用户怀疑页面故障）。**注**：`console.log` 仅存在于**开发调试**语义，属 §3.6「禁止将调试文本…」的边界情况。
- **建议**（最小改动，按可行性排序）：
  1. **立即可做**：把链接改为「复制命令 ID」按钮（`navigator.clipboard.writeText` + `ElMessage.success('命令 ID 已复制')`），复用 `FirmwareManage.vue:307-328` 的 `handleCopyUrl` 模式——这满足 §4.3.2.5「保留复制路径」。
  2. **中期**：待命令详情路由就绪后再改回跳转；在此之前把 `<el-button link>` 换成不可点的 `<span class="mono">`（去掉"可点"的视觉承诺）。
  3. 删除 `console.log`（§3.5.4「日志…不得包含敏感信息」的同类卫生要求；UUID 本身不敏感，但生产包不应有调试输出）。
- **验收方法**：点击后断言以下之一成立：`location.pathname` 改变 / `.el-message` 出现 / 剪贴板内容等于该 ID。三者皆无为失败。

---

### #13 【中】5 个数据表均未使用 `.mobile-table-wrapper` / `.mobile-table-hint`

- **页面**：`/automation`（2 表）、`/alerts`（2 表）、`/data-sources`（1 表） | **视口**：360 / 390 / 768 | **主题**：light + dark | **维度**：D6 响应式与触控
- **现象**：这三页的宽表格（`scrollWidth` 达 870–1270px，视口仅 280–310px）**没有**使用规范要求的 `.mobile-table-wrapper` 包裹，**也没有** `.mobile-table-hint` 横向滚动提示。用户看到 1270px 宽的表格被截在 280px 视口里，没有任何「可以左右滑」的提示。（`/firmware` 与 `/device-configs` 当前无表格数据，无法在此状态下验证；`firmware` 的 `el-table` 在库为空时根本不渲染，见 `firmware-500` 实测 `tableCount:0`。）
- **证据DOM**（`uiux-d3-probe6.mjs`，`page.route` 无关，纯页面度量）：
  ```json
  {"page":"automation","vp":"w360","hasMobileWrapper":false,"hasMobileHint":false,
   "tables":[{"innerWrap":{"cw":280,"sw":930,"ox":"auto"},"colWidthSum":930},
             {"innerWrap":{"cw":280,"sw":870,"ox":"auto"},"colWidthSum":870}]}
  {"page":"data-sources","vp":"w360","hasMobileWrapper":false,"hasMobileHint":false,
   "tables":[{"innerWrap":{"cw":280,"sw":1270,"ox":"auto"},"colWidthSum":1270}]}
  {"page":"alerts","vp":"w360","hasMobileWrapper":false,"hasMobileHint":false,
   "tables":[{"innerWrap":{"cw":280,"sw":900,"ox":"auto"}},{"innerWrap":{"cw":280,"sw":610,"ox":"auto"}}]}
  ```
  三档视口（360/390/768）全部 `hasMobileWrapper:false`、`hasMobileHint:false`；基线探针亦一致（`mobileTableWrapper: 0`，30 条记录全为 0）。
- **附：正常横滚 ≠ 裁切（契约 §2.2 复核）** —— 真正的滚动容器是 `.el-scrollbar__wrap`（`overflow-x:auto`，`cw 280 / sw 870`），`.el-table__body-wrapper` 只是 `overflow:hidden` 的壳（`cw 280 / sw 280`，**不可滚动**）。因此**本域的表格横向滚动是设计允许的**，探针 `clippedCount` 在这些组合下均为 0，未误报。
- **证据截图**：`/tmp/uiux-d3/mobile-360-light-data-sources.png`、`/tmp/uiux-d3/mobile-360-light-automation.png`、`/tmp/uiux-d3/mobile-360-light-alerts.png`
- **证据代码**：
  - 全局样式**已存在**且已就绪：`frontend-shared/src/styles/theme.css:375-401`
    ```css
    .mobile-table-wrapper { width: 100%; overflow-x: auto; }
    .mobile-table-hint { display: none; font-size: 12px; ... }
    @media (max-width: 768px) { .mobile-table-hint { display: block; } }
    ```
  - `frontend-shared/src/views/automation/AutomationRules.vue:72` 与 `:11`：`<el-table>` 直接挂在 `<section class="card">` 下，无 wrapper
  - `frontend-shared/src/views/alert/AlertRules.vue:11`、`:53` 同构
  - `frontend-shared/src/views/data-source/DataSourceList.vue:97` 同构
  - **对照（正确实现）**：`views/channel/ChannelList.vue:79`、`views/logical-device/LogicalDeviceList.vue:42`、`views/node/NodeDetail.vue:268,337`、`views/data/DataPanel.vue:196` 都用了 `.mobile-table-wrapper`。
  - **全仓只有 5 处使用**（`grep -rn 'mobile-table-wrapper' src/`），本域 0 处。
- **规范条款**：§4.3.2.1 `MUST`「宽表格在 `<=768px` 用 `.mobile-table-wrapper` 和 `.mobile-table-hint` 包裹，**或经设计评审转换为卡片/行列表**」；§4.3.2.2 `MUST`「…横向滚动提示…共同验收」
- **严重度**：**中**（违反 MUST；表格内部横滚本身可用，缺的是**可发现性提示**——用户不知道右侧还有列。#11 的遮挡问题因缺少该提示而更难被用户察觉。**若主控认定已符合「或经设计评审转换」的豁免，本条可降为低**——但我在 `docs/` 中**未找到**针对这 5 个页面的设计评审记录（`grep -rn 'mobile-table-hint' docs/` 只有规范条文与归档文档，无本域页面的豁免说明））
- **建议**（最小改动）：
  1. 三个页面各包一层 `<div class="mobile-table-wrapper">` + 其上/下加 `<div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>`（全局 CSS 已就绪，**零新增样式**）。
  2. `/automation` 与 `/alerts` 各有 2 张表，需各包一次。
  3. 长期（§4.3.2.3）：`/data-sources` 的 6 个行内操作 + Switch 形态更接近「资源管理行列表」，建议评估转换为纵向行列表。
- **验收方法**：`document.querySelectorAll('.mobile-table-wrapper').length === document.querySelectorAll('.el-table').length`（本域应为 4–5），且 360px 下 `.mobile-table-hint` 的 `getComputedStyle().display === 'block'`。

---

### #14 【中】数据源详情抽屉缺 92vw 兜底（与 #10 同源）+ 结构缺陷

- **页面**：`/data-sources` | **视口**：所有 | **主题**：light + dark | **维度**：D5 / D6
- **现象**：这是 #10 的**代码层结论**，单列以便修复：`theme.css` 的全局弹层兜底**只写了 `.el-dialog`**，漏掉 `.el-drawer`；项目内唯一的 `el-drawer`（数据源详情）因此没有窄屏保护。
- **证据DOM**（`uiux-d3-probe13.mjs`）：`getComputedStyle(drawer).maxWidth === "none"`、`width === "560px"`（360 与 390 视口下均如此）。
- **证据截图**：`/tmp/uiux-d3f/drawer4-360.png`
- **证据代码**：`frontend-shared/src/styles/theme.css:369-373`（原文见 #10）。
- **规范条款**：§4.3.2.3 `MUST`「弹窗使用全局 `max-width: 92vw` 兜底」；§4.4.1 `MUST`
- **严重度**：**中**。**注**：本条与 #10 是**同一缺陷的两个视角**（#10 = 现象与影响，#14 = 缺失的全局兜底）。若主控按"一问题一条"合并，**总数应减 1**；我按要求分别给出根因与修法，不做合并。
- **建议**：见 #10 建议 1（把 `.el-drawer` 加入 `theme.css:371` 的选择器）。
- **验收方法**：`getComputedStyle(document.querySelector('.el-drawer')).maxWidth` 在 360px 下应为 `331.2px`（=92vw）而非 `none`。

---

### #15 【中】创建/编辑规则对话框 footer 初始在视口外（1440×900 亦如此）

- **页面**：`/automation`（640px 宽 × **910px 高**表单）、`/alerts`（520px × 634px） | **视口**：desktop-1440（`height:900` **不足以容纳 910px 的对话框**）、1440×600、390×844、360×800 | **主题**：light + dark | **维度**：D4 动效与过渡 / D6
- **现象**：打开创建规则对话框时，`footer`（取消/保存按钮）底边位于 `y=1029`，超出 900px 视口 **129px**。对话框高度由表单字段数决定（`formItemCount: 15`），**没有任何 `max-height` 或内部滚动**（`bodyOverflowY: "visible"`、`bodyScrollable: false`）。
- **证据DOM**（`uiux-d3-probe14.mjs`，四个视口）：
  | 视口 | dialog box | footer box | `footerInViewport` | `overlayDialogScrollMax` | `bodyOverflowY` |
  |---|---|---|---|---|---|
  | 1440×900 | `{w:640,h:910,top:135,bottom:1045}` | `{top:981,bottom:1029}` | **false** | 195 | `visible` |
  | 1440×600 | `{w:640,h:910,top:90,bottom:1000}` | `{top:936,bottom:984}` | **false** | 195 | `visible` |
  | 390×844 | `{w:359,h:1018,top:127,bottom:1145}` | `{top:1081,bottom:1129}` | **false** | 351 | `visible` |
  | 360×800 | `{w:331,h:1018,top:120,bottom:1138}` | `{top:1074,bottom:1122}` | **false** | 388 | `visible` |
  ```json
  {"dialog":{"w":640,"h":910,"top":135,"bottom":1045},
   "footer":{"w":608,"h":48,"top":981,"bottom":1029},
   "body":{"w":608,"h":772,"top":191,"bottom":963},
   "dialogMaxW":"1324.8px","footerInViewport":false,
   "bodyScrollable":false,"bodyOverflowY":"visible",
   "overlay":{"ch":900,"sh":900,"oy":"auto","canScroll":false},
   "formItemCount":15}
  ```
- **⚠ 关键复核（避免误报）**：**footer 是"初始在视口外"，不是"不可达"**。`uiux-d3-probe4.mjs` 用真实鼠标滚轮 + 程序化滚动双重验证：
  ```json
  {"ovOverflowY":"auto","ovAlignItems":"center",
   "overlayDialogScrollTop":195,
   "footerBottomAfterScroll":834,"viewportH":900,"footerReachableAfterScroll":true}
  ```
  滚动后 footer 底边 `834 <= 900`，`saveVisible: true`。360px 下同样：`overlayDialogScrollTop:388` → `footerBottom:734 <= 800`，**可达**。Playwright 真实点击 `[data-test="save-rule"]` 四次全部成功（`saveClickableByPlaywright: true`）。
- **证据截图**：`/tmp/uiux-d3c/dlg-automation-create-desktop-1440x900.png`（底部按钮被视口切掉）、`/tmp/uiux-d3c/dlg-automation-create-mobile-360x800.png`（同样只见表单上半部）、`/tmp/uiux-d3d/dlg-scroll-light-desktop-1440x900.png`（滚动后 footer 可见）
- **证据代码**：`frontend-shared/src/views/automation/AutomationRules.vue:122`
  ```vue
  <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '创建规则'" width="640px" data-test="rule-dialog">
  ```
  **未设 `align-center`**（对比 `FirmwareManage.vue:102,134` 用了 `align-center`），也未给 `.el-dialog__body` 设 `max-height/overflow-y`（对比 `DeviceConfigForm.vue:418-422` **有**：
  ```css
  .config-form-dialog :deep(.el-dialog__body) {
    padding: 16px 20px;
    max-height: 70vh;
    overflow-y: auto;
  }
  ```
  —— 但该样式**未生效**，见下）。
- **规范条款**：§4.3.2.3 `MUST`「弹窗使用全局 `max-width: 92vw` 兜底（`theme.css:369-373`），弹窗内容超高时**主体可滚动、footer 保持可见**」；§4.4.1 `MUST`
- **严重度**：**中**（违反 MUST 的"footer 保持可见"要求；但经实测 footer 可滚动到达，未阻断核心任务，故不判高）
- **建议**（最小改动）：
  1. 给 `AutomationRules.vue:122` 的 `el-dialog` 加 `align-center`（Element Plus 会在超高时自动把对话框顶部对齐并允许整层滚动），并对 `.el-dialog__body` 加
     ```css
     .automation-rules-page :deep(.el-dialog__body) { max-height: 60vh; overflow-y: auto; }
     ```
  2. **重要**：`DeviceConfigForm.vue:418-422` 已经写了同样的样式却**没有生效**——实测 `bodyMaxHeight:"none"`、`bodyOverflowY:"visible"`、`dialog.className = "el-dialog config-form-dialog"` 且元素的 `data-v-*` 属性列表为 `[]`。原因是 `el-dialog` 被 **Teleport 到 `body`**，而 SFC 的 `scoped` 属性没有随 Teleport 落到 dialog 元素上（`innerHTML` 检查确认 `hasDataAttr: []`）。这**正是规范 §4.3.2.3 后半句「Teleport 的弹窗样式必须是全局选择器或 Element Plus 变量，不能依赖 scoped CSS 穿透」所禁止的**。建议把这两条样式搬到 `theme.css`（全局），例如
     ```css
     .config-form-dialog .el-dialog__body { max-height: 70vh; overflow-y: auto; }
     ```
  3. 同域其他对话框实测**合规**，无需改动：
     | 对话框 | 视口 | `footerInViewport` | 判定 |
     |---|---|---|---|
     | `/alerts` 创建规则（520px，634px 高） | 1440×900 | **true** | ✅ |
     | `/alerts` 创建规则 | 360×800 | **true** | ✅ |
     | `/data-sources` 新建数据源（560px，710px 高） | 1440×900 | **true** | ✅ |
     | `/data-sources` 新建数据源 | 360×800 | false（`overlayDialogScrollMax:80`，滚动后 `reachable:true`） | ✅ 可达 |
     | `/firmware` 上传固件（500px，280px 高） | 1440×900 / 360×800 | **true** | ✅ |
     | `/device-configs` 编辑模板（680px，801px 高） | 1440×900 | false（`overlayDialogScrollMax:86`） | ⚠ 同 #15 模式 |
  4. **92vw 兜底实测有效**：所有对话框在 360px 下 `maxWidth === "331.2px"`（= 92vw），`cssWidth` 也是 `331.188px`，**均未溢出视口**（`left:14, right:346`）。规范 §4.3.2.3 的前半句在本域**已闭环**。
- **验收方法**：打开对话框后断言 `footer.getBoundingClientRect().bottom <= window.innerHeight`，或断言 `.el-dialog__body` 的 `scrollHeight > clientHeight && getComputedStyle().overflowY === 'auto'`。

---

### #16 【中】危险确认按钮不是 danger 类别，且默认焦点落在确认按钮上

- **页面**：`/automation`（删除规则、手动触发）、`/alerts`（删除规则）、`/firmware`（单个删除、批量删除）、`/device-configs`（删除配置、克隆）、`/data-sources`（删除、停用权威） | **视口**：所有 | **主题**：light + dark | **维度**：D5 业务完整交互流程 / D3
- **现象**：全部 **11 处** `ElMessageBox.confirm` 都传了 `type: 'warning'`，确认按钮渲染为 **`el-button--primary`（蓝色）**，而非 danger（红色）；并且**默认焦点直接落在确认按钮上**，用户按回车即执行破坏性操作。
- **证据DOM**（`uiux-d3-probe.mjs` `automation-delete-confirm`，桌面前/暗四组完全一致）：
  ```json
  {"message":"删除规则「S2-低电强制断充」？",
   "type":"el-message-box",
   "buttons":[{"text":"取消","cls":"el-button","focused":false,"pe":"auto"},
              {"text":"确定","cls":"el-button el-button--primary","focused":true,"pe":"auto"}],
   "activeEl":"BUTTON:确定"}
  ```
  独立复核（`uiux-d3-probe8.mjs`，数据源删除，360px）：
  ```json
  {"text":"删除数据源「审计临时来源」？",
   "btns":[{"t":"取消","cls":"el-button","focused":false},
           {"t":"确定","cls":"el-button el-button--primary","focused":true}]}
  ```
  `uiux-d3-probe19.mjs`（device-configs 删除）：
  ```json
  {"text":"确定要删除配置 \"审计临时模板\" 吗？",
   "btns":[{"t":"取消","cls":"el-button","focused":false},
           {"t":"删除","cls":"el-button el-button--primary","focused":true}]}
  ```
  **三个不同页面、三个不同确认框，确认按钮类名均为 `el-button--primary`，`focused` 均为 `true`。**
- **证据截图**：`/tmp/uiux-d3/scenario-desktop-1440-light-automation-delete-confirm.png`（蓝色「确定」+ 焦点环）、`/tmp/uiux-d3/scenario-mobile-360-light-automation-delete-confirm.png`、`/tmp/uiux-d3f/ds-delete-confirm-360.png`、`/tmp/uiux-d3f/dc-delete-confirm.png`（可确认按钮文案为「删除」但仍是蓝色）
- **证据代码**（**项目已有正确的工具函数，只是本域未使用**）：
  - 项目权威实现：`frontend-shared/src/utils/feedback.ts:58-78`
    ```ts
    async confirmDanger(message, options = {}): Promise<boolean> {
      try {
        await ElMessageBox.confirm(message, options.title ?? '确认操作', {
          confirmButtonText: options.confirmText ?? '确定',
          cancelButtonText: options.cancelText ?? '取消',
          type: 'warning',
          confirmButtonClass: 'el-button--danger',   // ← 关键
          draggable: true,
        })
    ```
  - **本域 11 处全部手写 `ElMessageBox.confirm` 且未传 `confirmButtonClass`**：
    `AutomationRules.vue:525`（删除）、`:537`（确认执行）、`:551-555`（手动触发）；
    `AlertRules.vue:262`；
    `FirmwareManage.vue:231-235`（批量删除）、`:259-263`（单个删除）；
    `DeviceConfigList.vue:411-415`（克隆）、`:467-471`（删除）；
    `DataSourceList.vue:462-466`（停用）、`:496-500`（删除）。
  - `feedback.confirmDanger` 全仓**仅 1 处调用**（`views/layout/MainLayout.vue:438`，退出登录），`views/profile/Profile.vue:52` 与 `components/device/CommandList.vue:85` 只 import 了 `feedback` 未用 `confirmDanger`。
- **规范条款**：§3.4.3 `MUST`「破坏性动作使用 **danger 视觉层级**、二次确认和影响说明；确认文案必须包含对象身份、不可逆影响或数据影响。**删除必须与常规主操作隔离，不能靠颜色或图标单独承担风险**」；§4.3.2.4 `MUST`「危险确认使用 **danger 确认按钮**；**默认焦点和文案应避免诱导性确认**」
- **严重度**：**中**（违反两条 MUST，但确认框**存在**且包含对象身份——实测文案「删除规则「S2-低电强制断充」？」符合"对象身份"要求；风险在于"蓝色按钮 + 默认聚焦"使误触代价升高，未达到阻断级）
- **证据补充（文案合规性核对）**：
  | 位置 | 文案 | 含对象身份 | 含不可逆影响 |
  |---|---|---|---|
  | automation 删除 | `删除规则「S2-低电强制断充」？` | ✅ | ✗（未说明是否可恢复） |
  | automation 手动触发 | `手动触发规则「…」？将跳过条件评估与确认制直接执行动作 (cooldown/日熔断仍生效)。` | ✅ | ✅ |
  | automation 确认执行 | `确认执行该高风险动作？` | ✗（无对象） | ✗ |
  | firmware 批量删除 | `确定要删除选中的 N 个固件吗？此操作不可恢复。` | ✅ | ✅ |
  | firmware 单个删除 | `确定要删除固件版本 "v9.9.9" 吗？` | ✅ | ✗ |
  | device-configs 删除 | `确定要删除配置 "审计临时模板" 吗？` | ✅ | ✗ |
  | data-sources 删除 | `删除数据源「P19来源」？` | ✅ | ✗ |
  | data-sources 停用 | `停用权威来源「…」？组内候选将自动接替。` | ✅ | ✅ |
  即 8 条中 5 条缺"不可逆影响"说明，**`AutomationRules.vue:537` 的「确认执行该高风险动作？」连对象身份都没有**——这一条应单独按 §3.4.3 计为缺陷（已计入本条）。
- **建议**（最小改动）：
  1. **统一改用 `feedback.confirmDanger`**：把 11 处 `await ElMessageBox.confirm(msg, title, { type: 'warning', ... })` 替换为 `if (!(await feedback.confirmDanger(msg, { title, confirmText, cancelText }))) return`。该函数已提供 `confirmButtonClass: 'el-button--danger'`。
  2. **默认焦点**：`ElMessageBox` 默认聚焦确认按钮是 Element Plus 行为。加 `autofocus: false` 或 `focusOnShow: false`（EP 版本相关），或显式 `beforeClose` 里把焦点移到取消按钮。
  3. **补影响说明**：至少 `AutomationRules.vue:537` 补对象（如 `确认执行事件 #${event.id}（规则「${ruleName(event.rule_id)}」）？`）。
- **验收方法**：点击删除后断言 `document.querySelector('.el-message-box__btns button.el-button--primary')` 为 null 且存在 `button.el-button--danger`；断言 `document.activeElement` 不是确认按钮。

---

### #17 【中】自动化表格 Switch 无可访问名称；`el-switch` 容器键盘不可聚焦

- **页面**：`/automation`、`/alerts`（规则表「启用」列） | **视口**：所有 | **主题**：light + dark | **维度**：D7 可访问性 / D3
- **现象**：规则表每行的「启用」开关是一个**无名称**的开关——`aria-label` 为空，且没有任何 `<label for>` 关联。屏幕阅读器只会念「switch, checked」，无法区分这是哪条规则的开关。此外 `el-switch` 的**外层 `div` 的 `tabIndex` 为 `-1`**（只有内部 `input` 可聚焦，属 Element Plus 的既定实现，本身可接受，但配合无名称使键盘用户无法辨识当前开关）。
- **证据DOM**（`uiux-d3-probe19.mjs` `automation-switch-a11y-and-scroll`）：
  ```json
  {"switchOuterHTML":"<div class=\"el-switch is-checked\" data-test=\"rule-enabled\"><input class=\"el-switch__input\" type=\"checkbox\" role=\"switch\" aria-checked=\"true\" aria-disabled=\"false\" name=\"\" true-value=\"true\" false-value=\"false\" id=\"el-id-2399-31\"><span class=\"el-switch__core\">…",
   "switchTabIndex":-1,"switchRole":null,"switchAria":null,
   "inputAttrs":{"type":"checkbox","role":"switch","ariaLabel":null,"ariaChecked":"true","tabIndex":0,
                 "name":"","id":"el-id-2399-31"},
   "labelElement":"no-id"}
  ```
  三个关键事实：`ariaLabel: null`、`name: ""`、`label[for="el-id-2399-31"]` **不存在**。
  对照（同页**合规**的例子）：`uiux-d3-probe16.mjs` 的 Tab 序列第 7/11 项落在 `input(el-switch__input)`，说明**键盘可达**；缺的只是**可访问名称**。
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`（「启用」列两个开关，列头是唯一的上下文）
- **证据代码**：
  - `frontend-shared/src/views/automation/AutomationRules.vue:42-46`
    ```vue
    <el-table-column label="启用" width="80">
      <template #default="{ row }">
        <el-switch :model-value="row.enabled" data-test="rule-enabled"
          @change="(v) => onToggle(asRule(row), v === true)" />
    ```
  - `frontend-shared/src/views/alert/AlertRules.vue:32-36` 同构（`data-test="rule-enabled"`）
  - 全仓 `el-switch` 加 `aria-label` 的**只有** `NodeDetail.vue` 等少数几处；本域 4 个 `el-switch`（2 表格 + 2 表单）**全部无名称**。
- **规范条款**：§3.1.3 `MUST`「为可点击的非原生容器补齐 `role`、`tabindex`、**明确 `aria-label`**」；§4.4.5 `MUST`「…并保证相邻间距与**可访问名称**」；§4.2.2 `MUST`「状态同时使用文字和颜色；图标可作为第三重表达，不能只用颜色或红绿圆点」
- **严重度**：**中**（违反 MUST；开关本身可用，但对读屏用户而言"哪条规则的开关"不可知，且本页 500 行事件 × 每行一个开关的场景会让问题放大）
- **建议**（最小改动）：
  ```vue
  <el-switch :model-value="row.enabled" :aria-label="`启用规则 ${row.name}`"
    data-test="rule-enabled" @change="…" />
  ```
  表单内的两处（`AutomationRules.vue:214,238`、`AlertRules.vue:117`）本身有 `el-form-item` 标签关联，**无需改动**。
- **验收方法**：断言 `document.querySelector('td .el-switch input').getAttribute('aria-label')` 非空且含规则名；或用 Playwright 的 `getByRole('switch', { name: /启用规则/ })` 能定位到元素。

---

### #18 【中】侧栏 135 个 `cursor:pointer` 元素键盘不可达（跨域 pattern，本域复现）

- **页面**：本域全部 5 页（侧栏是共用外壳） | **视口**：所有 | **主题**：light + dark | **维度**：D7 可访问性
- **现象**：主控广播 1 指出探针 `clickableNotFocusable` 曾因选择器白名单而恒为 0。修复后本域复测：每页有 **135 个**位于主内容区之外的 `cursor:pointer` 元素（侧栏 + 页头），其中不可聚焦的占绝大多数。
- **证据DOM**（`uiux-d3-kbd/dom-facts.json`，更新版探针，30 条记录）：
  | 页面 | 视口 | `pointerElements` | `clickableNotFocusable` |
  |---|---|---|---|
  | automation | desktop-1440 | 676 | 414 |
  | device-configs | desktop-1440 | 182 | 175 |
  | data-sources | desktop-1440 | 161 | 153 |
  | firmware | desktop-1440 | 145 | 139 |
  | alerts | desktop-1440 | 142 | 136 |
  | automation | mobile-360 | 612 | 350 |
  | alerts | mobile-360 | 78 | 72 |
  独立精测（`uiux-d3-probe16.mjs`，**排除"祖先已是原生控件"的假阳性**）：
  ```json
  {"automation":{"mainPointer":541,"mainNonNative":283,"realDefects":22,"shellPointer":135},
   "device-configs":{"mainPointer":47,"mainNonNative":44,"realDefects":35,"shellPointer":135},
   "data-sources":{"mainPointer":26,"mainNonNative":22,"realDefects":13,"shellPointer":135},
   "alerts":{"mainPointer":7,"mainNonNative":5,"realDefects":0,"shellPointer":135},
   "firmware":{"mainPointer":10,"mainNonNative":8,"realDefects":0,"shellPointer":135}}
  ```
  **`shellPointer` 在 5 个页面恒为 135** —— 这是外壳（侧栏+页头）的固定开销，与页面内容无关。
- **⚠ 诚实声明：本条的 414/350/175 等数字包含大量假阳性。** 我实测发现探针的 `cursor:pointer` 扫描会把**原生按钮的后代**（`<i class="el-icon">`、`<svg>`、`<path>`、`<span>`）一并计入。例：`automation` 页 `span."` 一项就占 **260 个**（全是表格单元格里的普通文本，其祖先链上并无 `cursor:pointer` 的容器，但 `.el-table__row` 的 hover 样式让它们继承了 `cursor:pointer`）。用"祖先是否为原生控件"过滤后，`alerts` 与 `firmware` 的真实缺陷降为 **0**。
  **故本节只主张两个经交叉验证的结论**：(a) 外壳 135 个指针元素中的侧栏菜单项键盘不可达（D4 域已独立复现并报为「高」，见 `UIUX审计报告-全局骨架与可访问性-D2D4D6D7.md` #2，本域不重复计入严重度分布）；(b) 本域页面主体的真实缺陷见下。
- **本域页面主体内的真实不可聚焦元素**（`realDefects`，已排除原生控件后代）：
  | 页面 | 数量 | 具体元素（`uiux-d3-probe16.mjs` `detail`） |
  |---|---|---|
  | device-configs | 35 | `div.el-select__wrapper`×3（`tabindex="-1"`，**可聚焦性由此推断为缺失**）、`i.el-icon.el-input__clear`×1、`svg`×5、`path`×6、`span`×3 等 |
  | automation | 22 | `span.el-switch__core`×2、`div.el-switch__action`×2、`div.el-select__wrapper`×2、`svg`/`path`×4 等 |
  | data-sources | 13 | `div.el-select__wrapper`×1、`i.el-icon.el-input__clear`×1、`svg`/`path`×5 等 |
  | alerts / firmware | 0 | — |
  其中 **`div.el-select__wrapper` 是 Element Plus 2.9+ 的可聚焦容器**（其内部 `input.el-select__input` 的 `tabIndex` 为 0，实测 Tab 序列第 15/16 项落在 `input.el-select__input`），因此这部分**同样可能是探针假阳性**。**真实缺陷集中在 `el-input__clear` 图标（14×14px 的清除按钮，无 `role`/`aria-label`/`tabindex`）与 `el-switch` 的内部 span**，但后者已由 #17 覆盖。
- **证据截图**：`/tmp/uiux-d3-kbd/desktop-1440-light-device-configs.png` 等
- **证据代码**：`frontend-shared/src/styles/theme.css` 的全局 `.el-table__row` hover 样式使整行文本继承 `cursor:pointer`；`MainLayout.vue:16-27` 的 `el-menu` 无键盘入口（D4 域 #2 已详述）。
- **规范条款**：§3.1.3 `MUST`；§4.4.5 `MUST`（可访问名称）
- **严重度**：**中**（本域页面的**主体内容**中真实缺陷集中在 `el-input__clear` 等小组件；侧栏部分归 D4 域。给出中而非高，因主要影响是读屏/键盘用户的次要操作，且本域页面主体经 Tab 序列实测**主要操作全部可达**——见下）
- **✅ 键盘可达性的正面结论**（`uiux-d3-probe16.mjs` `tab`，从页面顶部连按 40 次 Tab）：
  ```
   1 button | 折叠侧边导航
   2 input  | 快速跳转
   3 button | 打开通知中心
   4 button | 切换主题
   5 div    | Aadmin
   6 button | 创建规则          ← 本域主操作，第 6 个 Tab 即达
   7 input  | el-switch__input  ← 规则 1 启用开关
   8-10     | 触发 / 编辑 / 删除 ← 规则 1 行操作
  11-14     | 规则 2 的开关与三个操作
  15-16     | 触发历史筛选下拉
  17 button | 刷新
  18-40     | 命令 ID 链接（逐行）
  ```
  **本域自动化的全部主要操作（创建/启停/行操作/筛选/刷新）均在键盘可达范围内**；唯一的死链是 #12（点了没反应）。`alerts`/`firmware`/`device-configs`/`data-sources` 的 `unfocusableControls`（`tabIndex<0` 的按钮/开关/输入）实测均为 **空数组**。
- **建议**：
  1. 给 Element Plus 的 `el-input` 清除图标补名称（可在 `theme.css` 或页面级加 `aria-label` 不可行——需向上游提 issue 或用 `clear-icon` 插槽自定义）。
  2. 侧栏键盘入口：见 D4 域 #2 的建议，本域不重复。
  3. **探针改进建议（供主控）**：`clickableNotFocusable` 应排除「自身或任一祖先为 `button/a/input/select/textarea`」的元素，否则 `<svg>`/`<path>`/`<i>` 会污染计数（本域 `automation` 页 414 中有约 392 个此类）。
- **验收方法**：用过滤后的判据断言 `realDefects === 0`；对每个真实缺陷元素断言 `role` 或 `aria-label` 非空。

---

### #19 【中】页面级错误无重试入口（automation / device-configs / firmware）

- **页面**：`/automation`、`/device-configs`、`/firmware` | **视口**：所有 | **主题**：light + dark | **维度**：D3 人机反馈
- **现象**：这三个页面在接口失败后，**页面上没有任何常驻的错误态或重试按钮**，只有一条 3–5 秒后自动消失的 `ElMessage`。用户若没看到那条瞬态提示，看到的就是一个"空页面"，且无法知道如何恢复。
- **证据DOM**（`uiux-d3-probe18.mjs`，500 注入）：

  | 页面 | errorAlert | elResult | retryButtons | elMessages | 空态 |
  |---|---|---|---|---|---|
  | automation（rules 500） | 0 | 0 | `["刷新"]`（**是事件表的刷新按钮，不是重试**） | `["加载规则失败"]` | `暂无规则，点击右上角创建` |
  | automation（events 500） | 0 | 0 | `["刷新"]` | `["加载事件失败"]` | `暂无触发事件` |
  | device-configs | 0 | 0 | `[]` | `["获取配置模板列表失败"]` | `empty-state initial` |
  | firmware | 0 | 0 | `[]` | `["获取固件列表失败"]` | `empty-state empty` |
  | **data-sources（对照）** | **1** | 0 | `["重试"]` | `[]` | `empty-state initial` |

  ```json
  {"case":"automation-rules-500","errorAlert":0,"elResult":0,"retryButtons":["刷新"],
   "elMessages":["加载规则失败"],"tableEmpty":"暂无规则，点击右上角创建","rows":500}
  {"case":"firmware-500","errorAlert":0,"retryButtons":[],"elMessages":["获取固件列表失败"],
   "emptyState":1,"emptyTitle":"暂无固件数据"}
  ```
  注意 `automation-rules-500` 的 `rows:500`：规则表加载失败显示"暂无规则"，但**事件表仍渲染 500 行**，页面处于"一半失败一半成功"的混合态却无任何区分。
- **证据截图**：`/tmp/uiux-d3f/err500-automation-rules-500.png`、`/tmp/uiux-d3f/err500-device-configs-500.png`、`/tmp/uiux-d3f/err500-firmware-500.png`、`/tmp/uiux-d3f/err500-data-sources-500.png`（对照：顶部红色错误条 + 重试）
- **证据代码**：
  - `AutomationRules.vue:278-287` 与 `:307-319`：`catch { ElMessage.error('加载规则失败') }` —— 无 `error` ref，模板 `:54` 的空态 `<template #empty>暂无规则，点击右上角创建</template>` 是无条件文案。
  - `DeviceConfigList.vue:318-320`、`FirmwareManage.vue:216-218` 同构（见 #8 引文）。
  - **对照实现**：`DataSourceList.vue:30-42`（`el-alert` + `ds-retry` 按钮）、`stores/dataSource.ts:24`（`error` ref）、`:388-392`（`metricsReady`）。
- **规范条款**：§1.2.5「失败可理解、**可恢复**」；§3.4.2 `MUST`「区分…接口失败…**空状态不是错误状态的替代品**」；§4.3.3.3 `MUST`「失败提供**重试或退化路径**」
- **严重度**：**中**（违反 MUST；但页面未崩溃、用户可通过浏览器刷新恢复，故不判高。`automation` 的"刷新"按钮确实能重取事件，但不重取规则表——实测规则表失败后点「刷新」不会恢复规则列表）
- **建议**（最小改动）：三页各加一个 `error` ref 与页面顶部的 `el-alert--error + 重试按钮`，直接照抄 `DataSourceList.vue:30-42` 的 13 行模板 + `retryFetch` 函数。同时把 `<template #empty>` 文案在错误态下隐藏（`v-if="!loadError"`）。
- **验收方法**：注入 500 后断言含「重试」文本的按钮存在，且点击后请求重新发出（可用 `page.waitForRequest`）。

---

### #20 【中】首次加载无骨架屏（device-configs 与 automation 均无）

- **页面**：`/device-configs`、`/automation`、`/data-sources` | **视口**：所有 | **主题**：light + dark | **维度**：D4 动效与过渡
- **现象**：延迟接口 3 秒后观察首屏：`/firmware` 显示了 `el-skeleton`（合规），但 `/device-configs` 的**首屏完全空白**（`skeleton:0, mask:0, cards:0, emptyState:0`），`/automation` 与 `/data-sources` 用的是 `v-loading` 遮罩而非与内容结构相近的骨架。
- **证据DOM**（`uiux-d3-probe19.mjs` + `uiux-d3-probe2.mjs`）：
  ```json
  {"step":"device-configs-first-load","skeleton":0,"mask":0,"cards":0,"emptyState":0,"text":null}
  {"scenario":"first-load","target":"automation","tableRows":2,"loadingMask":1,"skeleton":0,"tableEmptyText":"暂无触发事件"}
  {"scenario":"first-load","target":"data-sources","tableRows":0,"loadingMask":1,"skeleton":0,"tableEmptyText":"暂无数据"}
  {"scenario":"first-load","target":"firmware","tableRows":0,"loadingMask":0,"skeleton":1}
  ```
  `device-configs` 的 `text: null` 表示 `.config-grid` 容器**在 1.2s 采样点尚未渲染**。`uiux-d3b` 的独立复核也显示 `data-sources` 首次加载时 `tableEmptyText:"暂无数据"` 已经可见——即**空表格文案先于数据出现**。
- **证据截图**：`/tmp/uiux-d3f/dc-firstload.png`（首屏只有统计卡与工具栏，列表区空白）、`/tmp/uiux-d3b/firstload-automation.png`、`/tmp/uiux-d3b/firstload-data-sources.png`、`/tmp/uiux-d3b/firstload-firmware.png`（对照：`el-skeleton` 5 行）
- **证据代码**：
  - `FirmwareManage.vue:12` **正确实现**：`<el-skeleton v-if="loading" :rows="5" animated />`
  - `DeviceConfigList.vue:86-93` 的 `config-grid` 用 `v-for="config in filteredConfigs"` 直接渲染，**无 loading 分支**；`loading` ref（`:253`）只用于空态判定（`:184`），未驱动任何骨架。
  - `AutomationRules.vue:11` 用 `v-loading="rulesLoading"`（遮罩），`:72` 用 `v-loading="eventsLoading"`。
  - `SkeletonCard.vue` 组件**已存在**于 `components/common/`，本域 0 处使用。
- **规范条款**：§4.3.3.2 `MUST`「**首次加载显示与内容结构相近的 skeleton**；刷新已有数据时保留旧数据，仅在触发控件显示 loading，避免闪回空态」
- **严重度**：**中**（违反 MUST；`device-configs` 的空白首屏 + `data-sources` 的"空态先于数据"是最明显的两种违反。`automation`/`data-sources` 的 `v-loading` 虽不完全符合"结构相近的 skeleton"，但至少提供了加载反馈，故一并计中）
- **建议**：
  1. `DeviceConfigList.vue`：在 `config-grid` 前加 `<el-skeleton v-if="loading && configs.length === 0" :rows="4" animated />`，或复用 `SkeletonCard.vue` 渲染 4 张卡片骨架。
  2. `data-sources`：给 `el-table` 的 `empty-text` 加加载条件，消除"空态闪现"。
  3. **后台刷新已合规**（正面结论）：实测 `automation-background-refresh`
     ```json
     {"beforeRows":502,"during":{"rows":502,"masks":1,"emptyText":null}}
     ```
     点击「刷新」时**旧数据保留**（行数不变），只有 `el-loading-mask` 出现，**无空态闪回**——符合 §4.3.3.2 后半句。
- **验收方法**：延迟接口 3s，在 t=1s 采样断言 `.el-skeleton, [class*="skeleton"]` 数量 > 0 或卡片骨架存在；刷新时断言行数不减少。

---

### #21 【低】禁用「导出」按钮无原因说明且无可 hover 的外层元素

- **页面**：`/device-configs` | **视口**：desktop-1440 | **主题**：light + dark | **维度**：D3 人机反馈
- **现象**：工具栏「导出」按钮在无数据时禁用（`el-button is-disabled`），但不告知**为什么**禁用。它既没有 `title`，也没有 `aria-label`；实测 hover 后出现的 tooltip 全部来自页面**其他**元素（通知中心/主题下拉/用户菜单/状态筛选），**没有任何一个来自该按钮**。
- **证据DOM**（`uiux-d3-probe3.mjs` `disabled-reason`）：
  ```json
  {"found":true,"disabled":true,"cls":"el-button is-disabled",
   "title":null,"ariaLabel":null,"ariaDisabled":"true","pe":"auto",
   "parentPE":"auto","parentCls":"filter-right",
   "tooltipTextAfterHover":"通知中心全部已读策略待确认: S2-低电强制断充…"}
  ```
  hover 后 `.el-popper` 列表（`uiux-d3-probe19.mjs`）中**没有**与该按钮相关的项：
  ```
  el-popover notification-popover / el-dropdown__popper(主题) / el-dropdown__popper(用户)
  / el-select__popper(状态) / el-select__popper(设备)
  ```
- **证据截图**：`/tmp/uiux-d3c/disabled-export-hover.png`（「导出」为灰色禁用态，悬停无提示）
- **证据代码**：`frontend-shared/src/views/config/DeviceConfigList.vue:64-67`
  ```vue
  <el-button @click="exportConfigs" :disabled="!filteredConfigs || filteredConfigs.length === 0">
    <el-icon><Download /></el-icon>
    导出
  </el-button>
  ```
  对照同域**正确实现**：`DataSourceList.vue:146-147`
  ```vue
  :disabled="asSource(row).status === 'active' || actingId === asSource(row).id"
  :title="asSource(row).status === 'active' ? '该来源已是权威，无需切换' : ''"
  ```
  且 `uiux-d3-probe19.mjs` 实测这些 `title` **确实存在**：
  ```json
  {"t":"切换为权威","disabled":true,"title":"该来源已是权威，无需切换","ariaDisabled":"true"}
  {"t":"重置","disabled":true,"title":"仅熔断来源可重置","ariaDisabled":"true"}
  ```
- **规范条款**：§3.4.4 `MUST`「**禁用操作给出原因**，尤其是设备离线、数据未准备、权限不足或前置条件缺失。用 tooltip 包裹 disabled Element Plus 控件时，应按 `NodeDetail.vue:10-49` 用**可接收 hover 的外层元素**」
- **严重度**：**低**（禁用逻辑本身正确且原因可从上下文推断——"没有数据自然不能导出"；属打磨项）
- **建议**：按 §3.4.4 的指定模式，用可接收 hover 的外层元素包裹：
  ```vue
  <el-tooltip :disabled="filteredConfigs.length > 0" content="当前筛选无数据，无可导出内容" placement="top">
    <span>   <!-- 外层元素负责 hover，disabled 按钮自身不接收事件 -->
      <el-button @click="exportConfigs" :disabled="filteredConfigs.length === 0">…</el-button>
    </span>
  </el-tooltip>
  ```
- **验收方法**：hover 该按钮后断言存在 `.el-popper[role="tooltip"]` 且文本包含原因；断言该 popper 的 `display !== 'none'`。

---
### #22 【低】`device-configs` 缺 `PageHeader`：本域唯一没有页面标题的列表页

- **页面**：`/device-configs` | **视口**：所有 | **主题**：light + dark | **维度**：D1 布局与排版
- **现象**：本域另外 4 页**都有** `PageHeader`（标题 + 副标题 + 主操作），只有 `/device-configs` **完全没有**——它直接以 4 个统计卡开场，页面里连一个 `h2` 都没有。
- **证据DOM**（更新版探针 `pageHeaderPresent`，30 条记录）：

  | 页面 | pageHeaderPresent | pageHeaderText |
  |---|---|---|
  | automation | **true** | `自动化策略` |
  | alerts | **true** | `告警规则` |
  | firmware | **true** | `固件管理 上传固件` |
  | data-sources | **true** | `数据源为逻辑设备的数据类别声明主备来源…新建数据源` |
  | **device-configs** | **false** | `null` |

  `uiux-d3-probe2.mjs` 独立复核：`{"cardCount":0,"h2":null,"pageTitlePresent":false}`。
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-device-configs.png`（首屏直接是「模板总数 / 本页启用 / 总线类型 / 设备类型」四卡，无标题）、`/tmp/uiux-d3/desktop-1440-light-data-sources.png`（对照：有标题与副标题）
- **证据代码**：`frontend-shared/src/views/config/DeviceConfigList.vue:1-24` —— 模板以 `<div class="config-page">` 开头，紧接 `<div class="stats-row">`，**全文无 `PageHeader` import**（对比 `DataSourceList.vue:3-7,313`、`AutomationRules.vue:3,253`、`AlertRules.vue:3,132`、`FirmwareManage.vue:3-9,162` 均有）。
- **规范条款**：§4.1.1 `MUST`「使用**页面标题**或等价的清晰标题」；§4.1 资源列表模板固定信息顺序「**页面标题** -> 筛选与操作 -> 范围明确的统计（可选）-> 列表/表格 -> 分页或空状态」（该页顺序是"统计 -> 筛选/操作 -> 列表"，缺首项）；§3.1.2 `MUST`「通用视觉模式优先复用 `PageHeader`…等既有组件」
- **严重度**：**低**（用户的导航上下文由侧栏高亮 + 面包屑提供，任务不受阻；属一致性/规范符合性缺陷）
- **建议**：在 `DeviceConfigList.vue:2-3` 之间插入（与 `FirmwareManage.vue:3-9` 同构）：
  ```vue
  <PageHeader title="配置模板" subtitle="为边缘设备复用连接、解析与初始化配置">
    <template #extra>
      <el-button type="primary" :icon="Plus" @click="showFormDialog = true">新建模板</el-button>
    </template>
  </PageHeader>
  ```
  并把工具栏里重复的「新建模板」按钮降级或移除（§4.1.4「顶部每个上下文最多一个实心 primary 主操作」）。
- **验收方法**：断言 `document.querySelector('[class*="page-header"]')` 非空且文本含「配置模板」；且页面内 `.el-button--primary` 数量 ≤ 2。

---

### #23 【低】自动化页 1072 处 inline style

- **页面**：`/automation` | **视口**：所有 | **主题**：light + dark | **维度**：D1 布局与排版
- **现象**：`/automation` 页有 **1072** 个带非空 `style` 属性的元素，是本域其余页面（35–63 处）的 **17–30 倍**。
- **证据DOM**（基线探针 `inlineStyled`）：

  | 页面 | inlineStyled |
  |---|---|
  | **automation** | **1072** |
  | device-configs | 63 |
  | data-sources | 58 |
  | alerts | 51 |
  | firmware | 36 |

- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-automation.png`
- **证据代码**：几乎全部来自 **Element Plus `el-table` 的 fixed 列**运行时注入。`AutomationRules.vue:47` 与 `:103` 各有一个 `fixed="right"` 列，Element Plus 会为**每一行**写入内联定位样式。实测：
  ```json
  {"fixedCells":502,"fixedRows":502,"rows":502}
  ```
  即 **502 个单元格 × 每个约 2 个 style 属性 ≈ 1000+**。视图自身仅有 `:63`（`style="width: 140px"`）等 3 处手写 inline style。
- **规范条款**：§3.6.4 `SHOULD`「用 class 和 scoped CSS 表达稳定布局；业务页面新增大量 inline style 时应先判断是否应归入组件 CSS、页面 CSS 或公共 token」（本次是**组件库行为**而非业务代码，故为 SHOULD）
- **严重度**：**低**（不影响渲染正确性；是 #2「500 行无分页」的下游放大器——分页后该数字会自然降到 50 处量级）
- **建议**：**无需为此单独改动**。修复 #2（加分页）即可把该计数降到 ~50。
- **验收方法**：`document.querySelectorAll('[style]:not([style=""])').length` 在分页落地后应 < 100。

---

### #24 【低】告警事件表「规则」列同为裸 `rule_id`

- **页面**：`/alerts` | **视口**：所有 | **主题**：light + dark | **维度**：D7 文案
- **现象**：`#1` 的同构缺陷，出现在告警事件表。
- **证据DOM**：**审计库 `alert_rules` 与 `alert_events` 均为空**（实测 `GET /api/v1/alert-rules → []`、`GET /api/v1/alert-events → []`），本页在本次审计中始终渲染 0 行：`{"tableRows":0}`（30 条记录 `rowCount` 恒为 0）。**故只有代码证据、无 DOM 实测。**
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-alerts.png`（两张表都显示「暂无规则，点击右上角创建」/「暂无告警事件」）
- **证据代码**：`frontend-shared/src/views/alert/AlertRules.vue:59`
  ```vue
  <el-table-column prop="rule_id" label="规则" width="80" />
  ```
- **规范条款**：§4.2.6；§1.2.1
- **严重度**：**低**（**降级理由：本次无 DOM 实测，仅有与 #1 完全同构的代码证据**。按契约 §2.3「以源码与运行时为准」，代码同构 + #1 的实测可支撑该结论，但严重度按"未在运行时观察到"降一档。若 `alert_events` 有数据，应与 #1 同级）
- **建议**：与 #1 同步修复（`store.rules` 已存在于 `stores/alert.ts:18`，直接复用 `ruleName` 映射）。
- **验收方法**：造一条告警规则 + 一条告警事件后，断言事件表第 2 列为规则名称而非数字。

---

### #25 【低】固件表列宽失衡 + 未包移动端表格 wrapper

- **页面**：`/firmware` | **视口**：`>=768px` | **主题**：light + dark | **维度**：D1 布局 / D6
- **现象**：固件表格 8 列总宽约 1095px，其中「ID」列硬编码 `width="50"`（显示一个 1–3 位整数），而「操作」列 `min-width="280"`（4 个按钮）。
- **证据DOM**：当前 `GET /api/v1/firmwares → []`，页面不渲染表格（`v-if="firmwares.length > 0"`，`FirmwareManage.vue:25`），实测 `tableCount: 0`（30 条记录一致）。**故本条为代码级结论。**
- **证据截图**：`/tmp/uiux-d3/desktop-1440-light-firmware.png`（显示「暂无固件数据」空态）
- **证据代码**：`frontend-shared/src/views/firmware/FirmwareManage.vue:26-74`
  ```vue
  <el-table-column prop="id" label="ID" width="50" />
  ...
  <el-table-column label="操作" min-width="280" class-name="firmware-action-col">
  ```
  同文件 `:472-485` 有 `@media (max-width: 768px)` 但只写了 `.el-table { width: 100% }` 与分页换行，**没有 `.mobile-table-wrapper`**（与 #13 同族）。
- **规范条款**：§4.3.2.1 `MUST`；§4.1.1（信息层级）
- **严重度**：**低**（无数据可实测，且列宽本身不影响可用性）
- **建议**：随 #13 一并给 `el-table` 包 `.mobile-table-wrapper` + `.mobile-table-hint`。
- **验收方法**：上传 1 条固件后重跑探针，断言 `mobileTableWrapper >= 1` 且 360px 下 `clippedCount === 0`。

---

### #26 【低】`device-configs` 未复用 `PageHeader` 组件（与 #22 同源）

- **页面**：`/device-configs` | **视口**：所有 | **主题**：light + dark | **维度**：D1
- **说明**：本条与 #22 是同一现象的"组件层"表述（#22 = 页面标题缺失，#26 = 未复用 `PageHeader` 组件）。**若主控按"一问题一条"合并，总数应减 1。** 证据、条款、建议均见 #22，不重复。

---
## 2. 本域问题总数按严重度分布

| 严重度 | 数量 | 编号 |
|---|---|---|
| 阻断 | **1** | #1 |
| 高 | **8** | #2、#3、#4、#5、#6、#7、#8、#9 |
| 中 | **11** | #10、#11、#12、#13、#14、#15、#16、#17、#18、#19、#20 |
| 低 | **6** | #21、#22、#23、#24、#25、#26 |
| **合计** | **26** | |

> 去重说明：#14 是 #10 的代码层视角、#26 是 #22 的组件层视角。若主控按「一问题一条」合并，**总数为 24**（阻断 1 / 高 8 / 中 9 / 低 6）。
> 跨域说明：#9（亮色对比度）与 D2 域同根因、#18（侧栏键盘）与 D4 域同根因，本域给出的是本域实例清单，**未重复计入 D2/D4 已报的严重度**。

## 3. 按规范条款分布（每问题计其主条款）

| 规范条款 | 数量 | 编号 |
|---|---|---|
| §4.4.1 / §4.4.5 / §4.4.6（360px 与触控，MUST） | 4 | #3、#4、#10、#11 |
| §3.4.2 / §3.4.6 / §1.2.5（六态可辨、错误不伪装） | 4 | #7、#8、#19、#12 |
| §4.3.2（表格/表单/对话框，MUST） | 4 | #13、#14、#15、#16 |
| §4.2.6 / §1.2.1（领域事实优先、技术标识） | 2 | #1、#24 |
| §3.4.4（禁用原因 / tooltip 包裹） | 2 | #17、#21 |
| §4.5.4（高频数据上限） | 1 | #2 |
| §4.5.1（亮暗双主题可读） | 1 | #9 |
| §4.3.2.2（创建/编辑交互模型分离） | 1 | #5 |
| §3.2.3 / §3.2.1（API 契约与分层） | 1 | #6 |
| §3.1.3（可点击非原生容器 role/tabindex/aria-label） | 1 | #18 |
| §4.3.3（空态/加载/错误三态） | 1 | #20 |
| §4.1.1（页面标题）+ §3.1.2（复用公共组件） | 2 | #22、#26 |
| §3.6.4（inline style） | 1 | #23 |
| §4.3.1（表格列宽与操作列可达） | 1 | #25 |
| **合计** | **26** | |

## 4. 复核结论：规范 §6 历史偏差（本域相关条目）

| §6 条目 | 结论 | 依据 |
|---|---|---|
| P0 WebSocket 完整 URL 二次追加（`stores/websocket.ts:131-144`） | **仍在**（当前配置未触发） | 见 D4 域报告 #1；本域代码复核确认 `websocket.ts:139-140` 的完整-URL 分支仍无条件追加路径常量。本域 5 页均不消费 WebSocket，**对本域无功能影响**。 |
| P0 `NetworkBanner` 读未导出的 `lastError` | **已闭环** | `grep -n lastError src/components/common/NetworkBanner.vue` 只命中 :28 的注释；生产代码已改为 `if (!wsStore.connected) return WarningFilled`。与 D4 域结论一致。 |
| P1 26 个 Vue 文件含 dialog、多个列表仍需核对移动横滚模式 | **仍在（本域已量化）** | 本域 5 页的 6 个 `el-dialog` + 1 个 `el-drawer` 全部核对（见 #15 合规表）；**4 张宽表格全部未包 `.mobile-table-wrapper`**（#13），`/data-sources` 固定操作列在 ≤768px 覆盖滚动区（#11）。§6 的「仍需核对」在本域**已有结论：核对完成，4 处不合规**。 |
| P1 页面标题模式未全站覆盖 | **仍在（本域 1/5 未覆盖）** | #22：`device-configs` 无 `PageHeader`（`pageHeaderPresent:false`、`h2:null`）；其余 4 页均已覆盖。§6 点名的 NodeList/EdgeDeviceList 属其他域。 |
| P2 主题 token 已完善但硬编码色仍多 | **本域已闭环** | 本域 5 个视图的 `<style scoped>` 中**没有新增硬编码语义 hex**；实测 `bodyBg`/`cardBg`/`tableBg`/`textColor` 全部解析自 `--el-*` 或 `theme.css` token。唯一例外是 #9 的**对比度不足**（token 值本身不合理），不是「绕过 token」。 |
| P2 i18n 已注册但页面尚未使用 $t | **本域已核实（0 命中）** | `grep -rn 'useI18n' src/views/{automation,alert,config,data-source,firmware}` = **0**。所有文案为中文硬编码，符合 §3.6.6「未完成全量迁移前，不得对单个新页面引入局部翻译体系」。 |
| P2 Playwright 仅配置 Desktop Chrome | **本域已用 CDP 临时补齐，但未沉淀** | 本次全部结论均在 5 视口 × 2 主题的 CDP/Playwright 矩阵下取得。建议主控评估把 `tools/uiux-d3-probe*.mjs` 中的关键断言纳入 CI，否则该偏差在审计结束后**回归未受保护**。 |

## 5. 我未能验证的部分

1. **`/firmware` 的表格在移动端的实际渲染未验证（库为空）。** `firmware` 表始终 0 条，`FirmwareManage.vue:25` 的 `v-if="firmwares.length > 0"` 使表格根本不渲染（实测 `tableCount: 0`，30 条记录一致）。因此 #6（字段名不匹配）我用**真实 API 闭环**（上传→PUT→GET→改字段名→DELETE）验证，而 #25（列宽）与 firmware 的移动横滚**只有代码证据**。
2. **`/alerts` 的两张表从未渲染过数据行。** 审计库 `alert_rules` 与 `alert_events` 均为空（`GET → []`）。因此 #24（裸 rule_id）**只有同构代码证据**，我已将其严重度从「与 #1 同级」降为低。同理，`/alerts` 的行操作触控尺寸、行内 Switch 名称、空态以外的五态**未在真实数据下验证**。
3. **#2 的「长时间打开的内存/性能影响」只做了 20 秒静置，未做长时压力测试。** 实测 20s 内 `els` 恒为 11902、`docScrollH` 恒为 900、堆内存 40.6MB→38.5MB（GC 后下降），**未观察到增长**。因此我**不主张**「内存泄漏」，只主张「11902 节点 + 20040px 高度的一次性布局开销」。渲染耗时（FPS、TTI）**未测量**——探针只取 DOM 事实，未接 Performance API 的 `longtask`/`paint` 指标。
4. **`/device-configs` 编辑对话框在 360px 下的 footer 可达性未单独验证。** 实测 1440×900 下 `footerInViewport:false`、`overlayDialogScrollMax:86`（与 #15 同模式），但**未在 360px 下对它做滚轮往返测试**（#15 的滚轮验证只对 automation 做了）。**推断**（非实测）：其 `:deep()` 样式未生效（`bodyMaxHeight:"none"`），行为应与 automation 一致、footer 可滚动到达。
5. **`alerts` 的 ErrorBoundary 在 360px 移动端的截图与几何未单独采集。** #7 的证据全部来自 1440 视口。移动端表现**推断**为一致（`ErrorBoundary` 无响应式分支），但未取证。
6. **#18 的 `realDefects` 计数仍可能含假阳性。** 过滤后 `automation` 剩 22、`device-configs` 剩 35，但其中占多数的 `div.el-select__wrapper` 在 Element Plus 2.9+ 是**可聚焦容器**（内部 `input` 的 `tabIndex` 为 0，Tab 序列第 15/16 项实测落在 `input.el-select__input`），很可能是探针假阳性。我**未能逐元素做 `element.focus()` 实测**来最终判定，这是本条计数不可精确定量的原因。
7. **未验证 `pointer: coarse` 环境（§4.4.7）。** 所有触控结论（#3）均基于 `viewport` 宽度推断，未使用 CDP 的 `Emulation.setEmitTouchEventsForMouse` 或真实触摸设备。规范 §4.4.7 明确要求「**不以浏览器宽度单独推断输入方式**」，因此我报告的触控缺陷严格来说只证明了「宽度 ≤390 时尺寸不足」，**未证明**「在粗指针设备上仍不足」（后者更强，属保守方向）。
8. **未走通 D5 的「批量操作闭环」。** `/firmware` 的批量删除因库为空，`selectedFirmwares.length > 0` 的批量栏从未渲染；`/device-configs` 的导入（走 `<input type=file>`）与导出、`/automation` 的手动触发全流程（触发→事件出现）**均未端到端执行**，只有源码审读。
9. **`data-sources` 的切换/停用/重置三个状态操作未走通。** 我用临时来源验证了**删除**确认框（#16），但「切换为权威」「停用」「重置熔断」需构造特定状态（`status='error'` 才能重置）而未执行；`onActivate`/`onDeactivate`/`onReset`（`DataSourceList.vue:447-492`）只有源码证据。
10. **#9 的对比度是「合成背景」计算，未做像素级采样。** 我用的是 `getComputedStyle` 的前景色 + 沿祖先链取到的**第一个非透明背景色**，未处理半透明叠加（如 `rgba(255,255,255,0.06)` 的边框/悬浮层）。本域涉及的背景均为不透明 token 值，误差应很小，但严格来说 D4 域已验证的「合成后计算」方法我没有重复。

## 6. 我原本的判断被证据推翻的部分

1. **初判：`/automation` 的创建规则对话框 footer 在 1440×900 下「按钮不可点」，应判高。**
   **被推翻。** 实测 `footerInViewport:false`（bottom 1029 > 900）确实成立，但我用 `uiux-d3-probe4.mjs` 做了三重复核：(a) 程序化滚动 `.el-overlay-dialog` 后 `footerBottomAfterScroll:834 <= 900`；(b) 12 次真实 `page.mouse.wheel()` 后 `saveVisible:true`；(c) Playwright 真实 `page.click('[data-test="save-rule"]')` 在 1440×900、1440×600、390×844、360×800 **四个视口全部成功**。**「footer 初始在视口外」≠「不可达」**，我从「高」降为「中」，并在 #15 显式记录该复核过程。
2. **初判：`/automation` 事件表无分页会导致长时间打开页面内存无限增长（§4.5.4 的核心关切）。**
   **被推翻。** 20s 静置实测 `{t0:{els:11902,memMB:40.6}, t1:{els:11902,memMB:38.5}}`——DOM 节点数与文档高度**完全不变**，堆内存在 GC 后反而下降。原因是事件是**一次性快照**（`fetchEvents()` 只在挂载和手动刷新时调用），不是轮询/推送。因此我把 #2 从「内存泄漏/无限增长」改为「一次性 11902 节点与 20040px 的布局开销」，并明确声明未测 FPS。
3. **初判：`DeviceConfigForm.vue:418-422` 已给 dialog body 设了 `max-height:70vh; overflow-y:auto`，所以该对话框在窄屏是可滚动的。**
   **被推翻。** 实测 `getComputedStyle(body).maxHeight === "none"`、`overflowY === "visible"`、`bodyScrollable:false`。原因是 `el-dialog` 被 **Teleport 到 `body`**，而该元素上**没有任何 `data-v-*` 属性**（`hasDataAttr: []`），SFC 的 `scoped` 属性未随之传送，`:deep()` 规则匹配不到。这正是规范 §4.3.2.3「Teleport 的弹窗样式必须是全局选择器…不能依赖 scoped CSS 穿透」所禁止的写法。**注意：全局 `theme.css:371-373` 的 `.el-dialog { max-width: 92vw }` 是生效的**（实测 `maxWidth:"331.2px"`），不生效的只有 DeviceConfigForm 自己的两条 scoped 规则。
4. **初判：`/data-sources` 固定操作列（`width="330"`）在 360px 下让用户「完全无法查看详情」，应判高。**
   **部分推翻。** `330 > 280` 属实（`fixedCoversViewportPct:100`），名称按钮在 `left:388` 属视口外也属实。但我把视口设到 **390px 与 768px** 发现：390px 下仍在视口外，而 **768px 下名称按钮已进入视口（`inViewport:true`）**，只是**中心点被固定列截获**（`elementFromPoint` 返回 `hitIsFixedCol:true`），**1024px 下完全正常**（`hitIsNameBtn:true`）。因此准确描述是「≤768px 时固定列覆盖滚动区」，用户**可以**先横滑再点，改判「中」。
5. **初判：`/device-configs` 360px 裁切是 `.filter-bar` 缺 `flex-wrap` 导致按钮不换行。**
   **部分推翻。** `.filter-bar`（`:620-626`）**确实有** `flex-wrap: wrap`，`.filter-left`（`:628-632`）也有。真正缺 `flex-wrap` 的是 `.filter-right`（`:646-649`，`display:flex; gap:8px` 无 wrap），且它在 `@media` 里被设成 `width:100%; justify-content:flex-end`。修法应针对 `.filter-right`，**不是** `.filter-bar`。
6. **初判：`/alerts` 接口失败只是「空」，与其他页面一样属 #19「无重试入口」。**
   **被推翻且更严重。** 实测该页**整页被 `ErrorBoundary` 替换**（`.el-result` = 1、`sidebarPresent:false`、`pageHeader:false`），侧边栏与页头全部消失。这与其他三页的「空态+瞬态消息」是**两种不同缺陷**，故把 `/alerts` 单列为 #7（高），不并入 #19（中）。
7. **初判：主控广播 1 说「可点击非聚焦 = 缺陷」，本域 `device-configs` 有 175 个，应报高。**
   **被推翻（部分）。** 用 `cursor:pointer` 扫描后按「祖先是否为原生控件」过滤，得到 `realDefects`：`alerts` = **0**、`firmware` = **0**、`automation` = 22、`data-sources` = 13、`device-configs` = 35。其中 `automation` 的 22 个里有 4 个是 `el-switch` 内部 span、4 个是 `el-select__wrapper`（EP 可聚焦容器）——**大部分是探针假阳性**。真实缺陷比原始计数少一个数量级，故降为「中」，并在 #18 给出可复现的过滤方法供主控改进探针。
8. **初判：`firmware` 的「目标型号」编辑应该能正常工作（前端有输入框、store 有类型、后端有列）。**
   **被推翻。** 三层证据链（上传→PUT→GET）证明该字段**静默失效**：前端发 `target_model`、后端读 `node_model`、`ShouldBindJSON` 返回值被丢弃、`Updates` 成功返回 200。**这是我本次审计中唯一一条「UI 显示成功但数据未变」的问题**，也是最难通过截图/DOM 发现的。
9. **初判：`device-configs` 的 `EmptyState kind="initial"` 已区分「新装无数据」与「筛选无结果」，符合 §4.3.3.1。**
   **部分推翻。** 语义区分**确实存在**（`hasActiveFilters ? 'filtered' : 'initial'`），但**缺少 `error` 分支**：接口 500 时 `hasActiveFilters=false` → 走 `initial`，「接口失败」被呈现为「新装无数据」。`EmptyState.vue:50` 的 props 契约**已支持** `kind:'error'`（`:154-157` 有对应样式），实现方未接线。
10. **初判：`/data-sources` 是本域「最规范」的页面（有 PageHeader、有错误态、KPI 用「—」）。**
    **部分推翻。** 它的失败态处理确实是本域唯一完整的（见 #8b），但实测它同时存在两个缺陷：**错误与空态重叠**（500 时 KPI 显示 `—` 的同时仍渲染「暂无数据源」，`emptyState=1`）与**抽屉 560px 在 360px 下左移 200px**（#10）。

## 7. 证据文件清单

**截图（均已 `ls` 校验存在，`/tmp/uiux-d3f/` 已逐文件核对）**

- `/tmp/uiux-d3/`（**66 张**）：`{desktop-1440,laptop-1024,tablet-768,mobile-390,mobile-360}-{light,dark}-{automation,alerts,device-configs,data-sources,firmware}.png`（30 张页面矩阵）+ 场景图 `scenario-*-automation-create-dialog.png`、`scenario-*-automation-delete-confirm.png`、`scenario-*-automation-first-load.png`、`scenario-*-data-sources-filtered-nomatch.png`、`scenario-*-device-configs-import-clip.png`、`scenario-*-*-api-fail.png`
- `/tmp/uiux-d3b/`（14 张）：`apifail-{automation,automation-events,alerts,data-sources,firmware,device-configs}.png`、`firstload-{automation,data-sources,firmware}.png`、`edit-automation.png`、`create-alerts.png`、`create-data-source.png`、`upload-firmware.png`、`noauth-automation.png`
- `/tmp/uiux-d3c/`（12 张）：`alerts-boundary.png`、`disabled-export-hover.png`、`dlg-{alerts,automation-create,datasources,firmware-upload}-*.png`
- `/tmp/uiux-d3d/`（6 张）：`dlg-scroll-{light,dark}-{desktop-1440x900,mobile-390x844,mobile-360x800}.png`
- `/tmp/uiux-d3e/`（6 张）：`deviceconfig-form-{desktop-1440x900,mobile-390x844,mobile-360x800}.png` + 同名 `-scrolled-` 版本
- `/tmp/uiux-d3f/`（**37 张**）：`hit-{360,390,768,1024}.png`、`drawer{,2,3,4}-{360,390,768,1024}.png`、`ds-fixed-{360,390}-{left,right}.png`、`ds-delete-confirm-360.png`、`ds-disabled-hover.png`、`dc-{firstload,edit-dialog,delete-confirm}.png`、`err500-*.png`（9 张，文件名形如 `err500-automation-rules-500.png`）、`automation-360-{row,cmdlink}.png`、`data-sources-360-fixedcol.png`
- `/tmp/uiux-d3-kbd/`（40 张）：更新版探针的全矩阵（含 `emptyStateElOnly` / `imgsTotal` 新字段）

**DOM 事实 JSON**

- `/tmp/uiux-d3/d3-facts.json`（30 条页面矩阵 + 52 条场景 + 触控分组 + 祖先链裁切 + 禁用控件链）
- `/tmp/uiux-d3b/d3b-facts.json`（20 条：500/网络失败六态 + 首次加载 + 权限 + 编辑模型 + 表格横滚）
- `/tmp/uiux-d3c/d3c-facts.json`（9 条：对话框几何 + 裁切补证 + 禁用 hover + ErrorBoundary 原文）、`d3n-facts.json`（其余对话框 + 字体事实）
- `/tmp/uiux-d3d/d3d-facts.json`（6 条：footer 可达性三重验证）
- `/tmp/uiux-d3e/d3e-facts.json`（3 条：Teleport scoped 样式是否生效）
- `/tmp/uiux-d3f/`：`d3f-facts.json`（表格横滚能力）、`d3g/h/i/j/k/l/m/o/p/q/r-facts.json`（固定列可达性、抽屉宽度、命中测试、禁用原因、分页、DOM 成本、500 伪装、命令 ID、Switch a11y）
- `/tmp/uiux-d3-kbd/dom-facts.json`（30 条更新版探针）+ `kbd-contrast.json`（10 组对比度）+ `kbd-precise.json`（精测指针元素 + 40 次 Tab 序列）
- 基线复用：`/tmp/uiux-evidence/dom-facts.json`（132 条，**未重复跑全量**）

**取证脚本（新增，全部位于 `frontend-shared/tools/`，未修改 `src/` 任何文件）**

`uiux-d3-probe.mjs`、`uiux-d3-probe2.mjs` … `uiux-d3-probe20.mjs`（共 **19 个**）

**审计期间在 `ehome_uiux` 库创建的临时数据（均已删除还原）**

| 资源 | 用途 | 创建 | 清理证据 |
|---|---|---|---|
| data-source `audit_probe_drawer` | #11 命中测试 / #10 抽屉度量 | `POST → 201` | `cleanup: {"found":[7],"statuses":[200]}` |
| data-source `audit_probe_tmp` | #11 固定列遮挡 | `POST → 201` | 已删除（probe8 cleanup） |
| data-source `audit_probe_p19` | #21 禁用按钮 hover | `POST → 201` | `cleanup.dsIds:[7]` |
| device-config `审计临时模板` | #5 编辑对话框字段 | `POST → 201` | `cleanup.cfgIds:[1]` |
| firmware `probe_999.bin` | #6 字段名不匹配 API 闭环 | `POST → 201` | `DELETE → 200` |

**未触碰** `ehome` / `ehome_test` 库；**未执行任何 git 命令**。

## 8. 优化方案（按严重度排序）

| 优先级 | 问题 | 最小改动点 | 验收度量 |
|---|---|---|---|
| **P0** | #1 规则列裸数字 | `AutomationRules.vue:76` + `AlertRules.vue:59` 改作用域插槽渲染 `ruleName(row.rule_id)` | 事件表第 2 列文本 ∈ 规则名称集合 |
| **P0** | #6 固件字段名不匹配 | `api/firmware.ts:38` 补 `target_model` 类型并映射为 `node_model` | `PUT` 后 `GET` 断言 `target_model` 已变 |
| **P0** | #7 alerts 整页崩溃 | `stores/alert.ts:27-35,67-74` 加 catch；`AlertRules.vue:280-288` 加 try/catch + 顶部 `el-alert` | 注入 500 后侧栏非空且 `.el-result === 0` |
| **P0** | #8 失败伪装成 0/空态 | `DeviceConfigList.vue` 加 `loadError` ref + `EmptyState kind="error"` + KPI 显示 `—` | 注入 500 后 `.empty-state.error` 存在、`.stat-value` 全为 `—` |
| **P1** | #4 360px 导入被裁 | `DeviceConfigList.vue:646-649` 给 `.filter-right` 加 `flex-wrap: wrap` | 探针 `clippedCount === 0` |
| **P1** | #10 / #14 抽屉超宽 | `theme.css:371` 选择器加 `.el-drawer` | 360px 下抽屉 `left >= 0` |
| **P1** | #5 编辑展示创建字段 | `DeviceConfigForm.vue:26-49` 加 `v-if="!isEdit"` + 只读展示 | 编辑态 `.el-cascader === 0` |
| **P1** | #2 无分页 | `api/automation.ts:151-155` 加分页参数；`AutomationRules.vue:118` 后加 `el-pagination`，筛选重置 `page=1` | 事件表行数 ≤ pageSize；`main.scrollHeight/clientHeight < 5` |
| **P1** | #3 触控目标 | `@media (max-width:768px)` 给 `.el-table .el-button.is-link` 设 `min-height:36px`；Switch 设 `min-height:44px` | 390/360 下 `<36px` 目标数为 0（页头三项除外） |
| **P1** | #9 亮色对比度 | `theme.css` 亮色段覆写 `--el-color-{success,warning,danger,primary}-light-9` 与 `--el-text-color-secondary` | 每个选择器 `ratio >= 4.5`（大字 `>= 3.0`） |
| **P2** | #11 固定列遮挡 | `DataSourceList.vue:138` 收窄到 ≤120px 或窄屏取消 `fixed` | 固定列宽 < 表格宽 × 0.6 且名称按钮可命中 |
| **P2** | #16 危险确认样式 | 11 处改用 `feedback.confirmDanger`（`utils/feedback.ts:58`） | 确认按钮含 `el-button--danger`，焦点不在确认键 |
| **P2** | #13 缺表格 wrapper | 3 页 4 表包 `.mobile-table-wrapper` + `.mobile-table-hint` | 360px 下 `.mobile-table-hint` 的 `display === 'block'` |
| **P2** | #15 对话框 footer | `AutomationRules.vue:122` 加 `align-center`；`DeviceConfigForm` 的 scoped 样式移到 `theme.css` | footer `bottom <= innerHeight` 或 body 可滚 |
| **P2** | #19 错误无重试 | 3 页照抄 `DataSourceList.vue:30-42` | 注入 500 后存在「重试」按钮且点击重发请求 |
| **P2** | #20 无骨架屏 | `DeviceConfigList.vue:86` 前加 `el-skeleton`；`DataSourceList` 空态加 loading 条件 | t=1s 采样时 `.el-skeleton > 0` |
| **P2** | #17 Switch 无名称 | `AutomationRules.vue:44`、`AlertRules.vue:34` 加 `:aria-label` | `getByRole('switch', {name:/启用规则/})` 可定位 |
| **P2** | #12 命令 ID 死链 | `AutomationRules.vue:581-584` 改为复制到剪贴板 + `ElMessage` | 点击后有 `el-message` 或剪贴板变化 |
| **P3** | #18 键盘可达 | 侧栏见 D4 域建议；本域补 `el-input` 清除图标名称 | 过滤后 `realDefects === 0` |
| **P3** | #21 禁用原因 | `DeviceConfigList.vue:64` 用 `<el-tooltip><span>` 包裹 | hover 后 tooltip 文本含原因 |
| **P3** | #22 / #26 页面标题 | `DeviceConfigList.vue:2-3` 插入 `PageHeader` | `[class*="page-header"]` 非空 |
| **P3** | #23 inline style | 随 #2 分页自然收敛 | `inlineStyled < 100` |
| **P3** | #24 告警裸 ID | 随 #1 同步 | 同 #1 |
| **P3** | #25 固件列宽 | 随 #13 包 wrapper | `mobileTableWrapper >= 1` |

**给主控的探针改进建议**（本域实测发现的两处假阳性，与广播中的假阴性家族同源）：

1. `clickableNotFocusable` 应排除「自身或任一祖先为 `button/a/input/select/textarea`」的元素。本域 `automation` 页 414 个计数中约 **392 个**是 `<svg>`/`<path>`/`<i>`/`<span>` 之类原生控件的后代或继承 `cursor:pointer` 的行内文本，过滤后真实值约 **22**。
2. `smallCount` 同样会把 `el-switch` 的内部 `span.el-switch__core` 计入；本域未受影响（因 `el-switch` 根部本身 <36px），但在已放大的控件上会产生假阳性。
3. 建议给 `clipped` 增加「被裁元素是否仍在视口内」的字段——本域 #4 的裁切 **11px 发生在 padding 区**，肉眼需放大才可见，纯靠截图极易漏报。

---

**报告完成时间**：2026-09-13 | **审计员**：D3/D5 域子代理 | **证据汇总**：`/tmp/uiux-d3`（66 PNG + 1 JSON）、`/tmp/uiux-d3b`（14 + 1）、`/tmp/uiux-d3c`（12 + 2）、`/tmp/uiux-d3d`（6 + 1）、`/tmp/uiux-d3e`（6 + 1）、`/tmp/uiux-d3f`（37 + 9）、`/tmp/uiux-d3-kbd`（40 + 3）