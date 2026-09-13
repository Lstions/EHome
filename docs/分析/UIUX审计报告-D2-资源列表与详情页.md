# UI/UX 审计报告 · 资源列表与详情页域（D2）

> **审计员**: D2 域子代理
> **日期**: 2026-09-13
> **依据**: [UIUX审计契约-2026-09-13.md](UIUX审计契约-2026-09-13.md) §5/§6/§8 ·
> [前端开发与UIUX设计规范.md](../规范/前端开发与UIUX设计规范.md)（**唯一权威**）·
> [前端页面功能基线-节点页.md](前端页面功能基线-节点页.md) · [前端设计风格基线.md](前端设计风格基线.md)
> **环境**: 后端 `http://127.0.0.1:8082`（同源托管 `frontend-shared/dist`）· 审计库 `ehome_uiux` · 系统 chromium
> **红线自查**: 未执行任何 git 命令；未修改 `frontend-shared/src/` 下任何文件；未连接 `ehome`/`ehome_test`；
> 新增脚本仅落在 `frontend-shared/tools/`。

---

## 0. 本域范围与模板归属（契约必答问题 1）

| 页面 | 路由 | 视图文件 | §4.1 模板 | 模板固定顺序合规 |
|---|---|---|---|---|
| 节点列表 | `/node` | `views/node/NodeList.vue` | 资源列表 | 缺"页面标题"（见 D2-06） |
| 节点详情 | `/node/1` | `views/node/NodeOverview.vue` | 资源详情 | 顺序合规，面包屑移动端消失（D2-08） |
| 通道管理 | `/channel` | `views/channel/ChannelList.vue` | 资源列表 | **合规**（PageHeader + 筛选 + 表格） |
| 边缘设备列表 | `/edge-device` | `views/edge-device/EdgeDeviceList.vue` | 资源列表 | 缺"页面标题"（见 D2-06） |
| 边缘设备详情 | `/edge-device/7053`（BMS） | `views/edge-device/bms/BmsDetailPage.vue` | 资源详情 | **合规**（PageHeader + 概览 + 领域分区） |
| 逻辑设备列表 | `/logical-device` | `views/logical-device/LogicalDeviceList.vue` | 资源列表 | 缺"页面标题"（见 D2-06） |
| 配置模板 | `/device-configs` | `views/config/DeviceConfigList.vue` | 资源列表 | 缺"页面标题"（见 D2-06） |
| 系统监控 | `/monitor` | `views/monitor/Monitor.vue` | 数据分析 | 有 `<h2>` 但无 PageHeader（见 D2-06） |

**主控实测复核结论（逐一核实）**：node-list / edge-device-list / logical-device-list / device-configs / monitor
**确实无 PageHeader**。但**严重度需分级**，不能一概而论：

- `monitor`：DOM 实测存在 `<h2>系统监控</h2>`（`x=20,y=80,w=342,h=29,fontSize=20px`），
  `document.querySelector('h1')||...||q('h2')` 命中 → 满足 §4.1.1「使用页面标题**或等价的清晰标题**」。
- 其余 4 页：DOM 中**无 h1/h2**，最深标题是行内卡片标题 `<h3>`（16px，如 `自动化验收虚拟节点`）。
  `headingEls` 实测（mobile-390 light /node）：`[{"tag":"h3","t":"自动化验收虚拟节点","fs":"16px"}]`；
  `/logical-device` 实测 `headingEls: []`（**完全没有标题元素**）→ 违反 §4.1.1 MUST。

---

## 1. 问题清单（契约 §6 格式）

### D2-01 【阻断】接口失败被伪装成"空数据 + 0 指标"
- **页面**: node-list | **视口**: desktop-1440 | **主题**: light | **维度**: D3/D5
- **现象**: 拦截 `GET /api/v1/nodes*` 返回 500 后，页面渲染 4 个 KPI 全为 `0`，并显示空态
  「暂无节点 / 开始添加第一个节点来监控您的设备」，**无任何错误态、无重试入口**。用户会据此判断"系统里没有节点"。
- **证据DOM**（`fail500-node-list` 步骤，`page.evaluate` 原始值）：
  `kpiValues=["0","0","0","0"]`、`kpiCount=4`、`selfEmptyCount=1`、
  `selfEmptyText=["暂无节点开始添加第一个节点来监控您的设备 添加节点"]`、
  `errorStateCount=0`、`retryCount=0`、`rows=0`、`cards=0`
- **同模式复现页**（同一轮注入，全部 500）：
  | 页面 | kpiValues | 空态 | errorState | retry |
  |---|---|---|---|---|
  | node-list | `["0","0","0","0"]` | 暂无节点 | 0 | 0 |
  | edge-device-list | `["0","0","0","0"]` | 暂无边缘设备 | 0 | 0 |
  | device-configs | `["0","0","0","0"]` | 暂无配置模板 | 0 | 0 |
  | logical-device-list | （无 KPI） | el-empty「暂无逻辑设备…请先创建边缘设备」 | 0 | 0 |
  | channel-list | （无 KPI） | 暂无通道 | 0 | 0 |
  | **node-detail** | — | 「节点不存在或加载失败」+ 重试 | 0 | **1（合规）** |
  | **edge-detail** | — | el-result「加载失败」+ 重试 | **1（合规）** | **1（合规）** |
  **对比证明**：同一份代码库中 `/node/1` 与 `/edge-device/:id` 正确区分了错误态 + 提供重试，
  说明"失败→错误态"在本项目已有实现范式，列表页未采用。
- **证据截图**: `fail500-node-list.png`、`fail500-edge-device-list.png`、`fail500-device-configs.png`、
  `fail500-logical-device-list.png`、`fail500-channel-list.png`（均在 `/tmp/uiux-d2-probe/`，实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:314-319`（初始值即 0）：
    ```ts
    const stats = reactive({ total: 0, online: 0, offline: 0, warning: 0 })
    ```
  - `frontend-shared/src/views/node/NodeList.vue:371-376`（失败路径只弹瞬态消息，不置错误态）：
    ```ts
    nodes.value = cached?.items || []          // ← 失败时退回空数组
    total.value = cached?.total || 0           // ← 失败时把"未知"写成 0
    updateStats()                              // ← 0 被当作真实统计渲染
    } catch (error: any) {
      if (sequence === listRequestSequence) ElMessage.error('获取节点列表失败')
    ```
  - `frontend-shared/src/views/logical-device/LogicalDeviceList.vue:308-317`（catch 只弹提示，`items` 保持空）：
    ```ts
    } catch (error: any) {
      ElMessage.error('加载逻辑设备列表失败: ' + (error?.message || '未知错误'))
    } finally { loading.value = false }
    ```
  - `frontend-shared/src/views/config/DeviceConfigList.vue:318-319`、`views/channel/ChannelList.vue`（同模式）
- **规范条款**: §3.4.2（MUST「区分…无数据、筛选无结果、接口失败；**空状态不是错误状态的替代品**」）、
  §3.2.5（MUST「接口缺字段、设备离线、资源未上报时显示未知或不可用，**不得以本地默认值伪造**」）、
  §1.2.1（领域事实优先）、§4.3.1（MUST 使用 `EmptyState` 的 `error` 语义）
- **严重度**: **阻断** —— 违反契约 §6 判据"数据/术语误导用户做出错误判断"：
  接口故障时用户读到的是「暂无节点 / 0 条」这一**伪造的领域事实**，并可能据此发起重复创建。
  （与主控广播 2 中 /dashboard 判定为阻断属**同一缺陷模式**，此处按同一规则定级。）
- **建议**:
  1. 在 `fetchNodes`/`fetchDevices`/`fetchList`/`fetchConfigs` 增加 `error` ref；
     catch 中 `error.value = true`，模板改为 `v-if="error" → <EmptyState kind="error" title="加载失败" .../> + 重试按钮`，
     **优先级高于空态**（`error` 分支放在 `filteredX.length===0` 之前）。
  2. `stats` 数值在 `error===true` 时渲染 `—`（规范 §3.4.5）而非 `0`。
  3. **验收方法**: 用 `page.route(api, r=>r.fulfill({status:500}))` 复跑，断言
     `document.querySelectorAll('[data-test="error-state"]').length === 1` 且
     `[...document.querySelectorAll('.stat-value')].every(e=>e.textContent.trim()==='—')`。

---

### D2-02 【高】逻辑设备列表无分页/虚拟滚动，一次性渲染 1003 行、28541 个 DOM 元素
- **页面**: logical-device-list | **视口**: mobile-390 / mobile-360 / desktop-1440 | **主题**: light | **维度**: D1/D6
- **现象**: 接口 `GET /api/v1/logical-devices` 返回 1003 条，前端**全量渲染**，无分页控件、无虚拟滚动、
  无"显示上限"提示；移动端同样渲染 1003 行。
- **证据DOM**:
  - `mobile-390`：`rows=1003`、`editBtnCount=1003`、`bodyEls=28541`、`pagination=0`、`loadMore=0`
  - `mobile-360`：`rows=1003`、`bodyEls=28541`、`pagination=0`
  - `desktop-1440`：`rows=1003`、`editBtnCount=1003`、`pagination=0`
  - 接口原文（curl）：`{"code":200,"data":{"items":[…1003 条…]}}`，无 `page`/`page_size` 参数被前端发送
- **证据截图**: `mobile-390-light-_logical-device.png`（实测存在，390x844）
- **证据代码**:
  - `frontend-shared/src/views/logical-device/LogicalDeviceList.vue:83-88`（API 不分页）：
    ```ts
    async list(): Promise<LogicalDeviceListResponse> {
      const response = await client.get<unknown, ApiEnvelope<{ items?: LogicalDeviceItem[]; total?: number }>>('/api/v1/logical-devices')
    ```
  - `frontend-shared/src/views/logical-device/LogicalDeviceList.vue:311-312`（无截断直接赋值）：
    ```ts
    const res = await logicalDeviceApi.list()
    items.value = res.items
    ```
  - 对照：`views/node/NodeList.vue:240-248` 与 `views/edge-device/EdgeDeviceList.vue:261-269` **都有** `el-pagination`
- **规范条款**: §4.5.4（SHOULD「高频实时数据做降采样、虚拟列表或**显示上限**，避免长时间打开页面无限增长」）、
  §3.3.5（SHOULD「服务端分页时不得把当前页本地筛选伪装成全局检索」）、§4.3.4（MUST「共 N 条必须与实际渲染记录一致」）
- **严重度**: **高** —— 违反 SHOULD 但在常用视口下明显影响可用性（1003 行 × 每行 1 个操作按钮的移动端长列表）。
- **建议**: 后端 `/api/v1/logical-devices` 增加 `page/page_size`（与其他资源端点一致），
  前端补 `el-pagination`（可直接照抄 `NodeList.vue:240-248`）；
  过渡期先在 `filteredItems` 后加 `.slice(0, PAGE_SIZE)` 并显示"显示前 N 条，共 M 条"。
  **验收**: 复跑断言 `document.querySelectorAll('.el-table__row').length <= 50`。

---

### D2-03 【高】node-list / edge-device-list 表格视图缺 `.mobile-table-wrapper`，390px 下固定操作列占表格宽 80%
- **页面**: node-list、edge-device-list | **视口**: mobile-390 / mobile-360 | **主题**: light+dark | **维度**: D1/D6
- **现象**: 切到表格视图后，表格未被 `.mobile-table-wrapper` 包裹、**无 `.mobile-table-hint` 横滚提示**；
  固定右侧操作列在 300px 表格宽中占 240px（80%），仅剩 60px 可见非固定列。
- **证据DOM**:
  - node-list @390：`table={x:41,w:300,right:341}`、`scrollW=1150`、`clientW=300`、
    `fixedRight={x:101,w:240,right:341}`、`wrappedByMobileTableWrapper=false`、`hasHint=false`、
    `fixedSharePct=80`、`row0Buttons=[{t:"详情",x:113,w:52},{t:"删除",x:177,w:52}]`、`row0ButtonsInViewport=2`
  - edge-device @390：`scrollW=1240`、`clientW=300`、`fixedRight={x:181,w:160,right:341}`、`wrapped=false`、`hasHint=false`
  - 表头遮挡实测（node-list）：`节点{covered:true}`、`设备ID{covered:true}`、`操作{covered:true}` ——
    除固定列外**只有"固件/状态/连接质量/延迟/上线时间"5 列是初始可见的**
  - @360：`table w=270`、`fixedRight w=240`（占比升到 89%）
- **证据截图**: `mobile-390-light-p3-node-table.png`、`mobile-390-light-i-edge-tableview.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:162-163`（`el-card` 直接包 `el-table`，无 wrapper）：
    ```html
    <el-card v-else class="collector-table-card" shadow="hover">
      <el-table
    ```
  - `frontend-shared/src/views/edge-device/EdgeDeviceList.vue:119-120`（同上）
  - 对照合规范例 `frontend-shared/src/views/logical-device/LogicalDeviceList.vue:42-44`：
    ```html
    <div class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
    ```
  - 全局模式定义 `frontend-shared/src/styles/theme.css:378-401`
- **规范条款**: §4.3.1（MUST「宽表格在 <=768px 用 `.mobile-table-wrapper` 和 `.mobile-table-hint` 包裹，
  或经设计评审转换为卡片/行列表」）、§4.3.2（MUST「重要操作列保持可达；**固定操作列、最小宽度、横向滚动提示和分页换行共同验收**」）
- **严重度**: **高**（违反 MUST；操作列本身可达，但**无横滚提示**违反 §4.3.2 的共同验收要求，
  且用户无法得知右侧还有 5 列被固定列遮住）。**注意**：这里没有把"可滚动容器"误报为裁切（契约 §2.2），
  `docOverflow=0`、表格内部横滚是设计允许的 —— 报的是**缺提示 + 操作列过宽**，不是裁切。
- **建议**: 按 `LogicalDeviceList.vue:42-44` 补 wrapper + hint；
  同时把 node-list 操作列 `width="240"` 收窄（`NodeList.vue:231`）或移动端改用卡片视图为唯一形态。
  **验收**: 复跑断言 `wrappedByMobileTableWrapper===true && hasHint===true`。

---

### D2-04 【高】device-configs 工具栏在 360px 视口被容器裁切（"导入"按钮左移出卡片 11px）
- **页面**: device-configs | **视口**: mobile-360 | **主题**: light + dark（两者均复现） | **维度**: D1/D6
- **现象**: `.filter-right` 在 360px 下不换行，"导入"按钮左边界落在卡片内容区之外，**按钮左侧 11px 被裁掉**，
  视觉上只剩「⇤导入」半个图标。截图确认按钮文字被切。
- **证据DOM**（`mobile-360-light` 探测原始值）：
  - `impBtn={x:10,w:77,right:87}`，`cardBody={x:21,w:310,right:331}`，`filterRight={x:41,w:270,right:311}`
  - `btnLeftOfBody=[{t:"导入",x:10,w:77,right:87,h:32}]`、`minBtnX=10`、`bodyLeft=21` → **左溢出 11px**
  - `frSW=270`、`frCW=270`（**不可横向滚动**）、`cardOverflowX="hidden"`、`docOverflow=0`
  - 祖先链实测（`scrollers` 数组）：`el-card__body{ox:auto,scrollable:false}` →
    `toolbar-card{ox:hidden,scrollable:false}` → `main-content{ox:auto,scrollable:false}`
    → **无任何一层可横向滚动** ⇒ 按契约 §2.2 的排除规则，这是**真裁切**，不是可滚动容器
  - @390 同页：`btns=[{t:"导入",x:40},{t:"导出",x:137},{t:"新建模板",x:234,right:341}]`、`btnLeftOfBody=[]` ⇒ **390px 不裁切，360px 才裁切**
- **证据截图**: `mobile-360-light-p13-cfg.png`（实测存在，360x800，可见"导入""导出"左侧被切）
- **证据代码**:
  - `frontend-shared/src/views/config/DeviceConfigList.vue:816-819`（768px 断点下 `filter-right` 只换行不换列）：
    ```css
    .filter-bar { flex-direction: column; align-items: stretch; }
    .filter-left { flex-direction: column; align-items: stretch; }
    .filter-left, .filter-right { width: 100%; }
    .filter-right { justify-content: flex-end; }
    ```
  - 根因：`.filter-right` 内 3 个按钮 `77+77+107+2×gap(8)=277px`，而 360px 视口下
    `.filter-right` 可用宽仅 `270px`（`filter-bar` 宽 270），**277 > 270 溢出 7px**，
    叠加 `justify-content:flex-end` 使溢出全部落在左侧；卡片 `overflow-x:hidden` 直接裁掉。
- **规范条款**: §4.4.1（MUST「先保证 **360px 宽可完成核心操作**」）、§4.4.6（MUST「页头在小屏允许标题与操作区换行」）
- **严重度**: **高**（违反 MUST；360px 是规范硬要求视口，"导入"入口在该视口不可完整命中/识别）
- **建议**: 在 `@media (max-width: 480px)` 给 `.filter-right` 加 `flex-wrap: wrap; justify-content: flex-start;`
  或改用 `el-space` 自动换行；最小改动是把 `.filter-right` 的 `justify-content` 改为 `flex-start` 并允许换行。
  **验收**: 复跑断言 `Math.min(...buttons.map(b=>b.x)) >= cardBody.x`。

---

### D2-05 【高】键盘可达性：`cursor:pointer` 的按钮式卡片 / TAB / 图标无 `role`/`tabindex`/`aria-label`
- **页面**: node-list、edge-device-list、node/1、edge-device/7053 | **视口**: desktop-1440 / mobile-390 | **主题**: light | **维度**: D7
- **现象**: 大量可点击的非原生容器只有 `@click`，**没有 `role`、没有 `tabindex`、没有 `aria-label`**，
  键盘用户无法触达。**已用 `element.focus()` 实测证伪"EL 内部 input 已兜底"的可能**。
- **证据DOM**（`/tmp/uiux-d2-probe/p16-a11y-roots.json`，仅列**已剔除**误报后的真实项）：
  | 页面 | 元素 | 尺寸 | role | tabindex | 键盘实测 |
  |---|---|---|---|---|---|
  | /node | `div.el-card.collector-card`（点卡片进详情） | 387x279 / 342x279 | `null` | `null` | `card.focus(); document.activeElement===card` → **false** |
  | /edge-device | `div.el-card.device-card` | — | `null` | `null` | 同上 |
  | /node/1 | `div.tab-item` ×7（基本信息…通道终端） | 85x48 … 91x48 | `null` | `null` | `t.focus()` → **false**，`tabIndexProp=-1` |
  | /node/1 | `i.el-icon.ph-edit`（改设备名） | **16x16** | `null` | `null` | `focus()` → **false**，`aria=null` |
  | /node/1 | `i.el-icon.copy-icon`（复制设备ID） | **13x13** | `null` | `null` | — |
  | /node/1 | `span.card-link`"查看全部" ×2 | 52x20 | `null` | `null` | — |
  | /node/1 | `div.chan-row` ×N | 1152x44 / 302x44 | `null` | `null` | — |
  | /edge-device | `span.fact-value.copyable`（点击复制节点名）×3 | 117x19 / 59x19 | `null` | `null` | — |
  - 内容区合计（已剔除 MainLayout 侧栏/页头）：`/node` 5、`/channel` 3、`/edge-device` 7、
    `/logical-device` 2、`/node/1` 15、`/edge-device/7053` 16、`/device-configs` 3、`/monitor` 1
- **【自查·剔除的误报】**（响应主控广播 1/3 的纪律：无法回答分母的计数不得作为结论）：
  - `div.el-select__wrapper`（共 11 个）：我首版探针报为不合规，**实测证伪** ——
    其内部 `<input class="el-select__input">` 的 `tabIndex=0`；键盘实测从搜索框连按 Tab 的序列为
    `["input.el-select__input","input.el-select__input","button…卡片视图","button…表格视图","button…添加节点","button…刷新"]`
    ⇒ **两个 el-select 均可 Tab 到达，不是缺陷**，已从清单剔除。
  - `div.stat-card`（第 4 张"本页告警"）：`StatCard.vue:44` 用 `isClickable = Boolean(attrs.onClick)` 判定，
    该卡无 `@click` ⇒ `role=null` 是**正确行为**；实测 `role=null, tabindex=null, tabIndexProp=-1`。
    前 3 张带 `@click` 的卡实测 `role="button", tabindex="0", ariaLabel="查看本页节点"` ⇒ **合规**，已剔除。
  - `div.el-table`（cursor:pointer 来自 EL 默认样式）、`.el-switch__core`、`.el-radio-button__inner`：
    真实语义由内部原生控件承载（`switchHTML` 实测 `<input type="checkbox" role="switch" aria-checked="true" aria-disabled="true" disabled>`）
    ⇒ **合规**，已剔除。
- **证据截图**: `desktop-1440-light-_node.png`、`mobile-390-light-light-_node_1.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:92-99`（卡片只有 `@click`，无 role/tabindex/aria）：
    ```html
    <el-card v-for="node in filteredNodes" :key="node.id" class="collector-card"
      :class="{ offline: node.status === 'offline' }" shadow="hover"
      @click="goToDetail(node.node_id)">
    ```
  - `frontend-shared/src/views/node/NodeOverview.vue:113-119`（TAB 只有 `@click`）：
    ```html
    <div v-for="tab in tabs" :key="tab.label" class="tab-item"
      :class="{ active: activeTab === tab.label }" @click="activateTab(tab.label)">
    ```
  - `frontend-shared/src/views/node/NodeOverview.vue:24`：`<el-icon :size="16" class="ph-edit" @click="renameVisible = true">`
  - `frontend-shared/src/views/edge-device/EdgeDeviceList.vue:212-216`：`<span class="fact-value copyable" @click="copyText(...)">`
  - **合规参照**（同仓已有正确实现）`frontend-shared/src/components/common/StatCard.vue:6-10`：
    ```html
    :role="isClickable ? 'button' : undefined"
    :tabindex="isClickable ? 0 : undefined"
    :aria-label="isClickable ? `查看${label}` : undefined"
    @keydown.enter.prevent="handleKeyboardActivate"
    @keydown.space.prevent="handleKeyboardActivate"
    ```
- **规范条款**: §3.1.3（MUST「为可点击的非原生容器补齐 `role`、`tabindex`、明确 `aria-label`，
  并让 Enter/Space 与 click 调用同一行为。`StatCard.vue:2-10` 是现有参照」）、§1.2.5、§4.4.5（相邻间距与可访问名称）
- **严重度**: **高**（违反 MUST；`StatCard.vue` 已是同仓参照，属未复用既有模式的回归）
- **建议**: 抽出与 `StatCard` 等价的 `clickable-container` 指令或 composable，应用到
  `collector-card` / `device-card` / `tab-item` / `card-link` / `chan-row` / `copyable`；
  小图标（`ph-edit` 16x16、`copy-icon` 13x13）改用 `el-button link` 并补 `aria-label`。
  **验收**: 复跑 `p16`，断言内容区 `nonNativeRows.filter(r=>!r.compliant).length === 0`。

---

### D2-06 【高】亮色主题对比度不达标（本域 15 处；暗色 4 处）
- **页面**: 全域 | **视口**: desktop-1440 | **主题**: **light（重灾区）** + dark | **维度**: D2
- **现象**: 亮色下浅底彩色标签/次级文字与背景对比度低于 WCAG AA 阈值（正文 4.5:1、大字 3:1）。
- **证据DOM**（`p15-a11y-contrast.json`，WCAG 相对亮度公式实算，前景/背景取 `getComputedStyle` 实际色值）：
  | 页面 | 选择器 | 前景色 | 有效背景 | 字号 | 对比度 | 阈值 | 判定 |
  |---|---|---|---|---|---|---|---|
  | /node | `.ws-status`（顶栏 WS 状态） | `rgb(103,194,58)` | `rgb(240,249,235)` | 12px | **2.08** | 4.5 | FAIL |
  | /node | `.status-tag`（离线徽标） | `rgb(144,147,153)` | `rgb(245,247,250)` | 12px | **2.87** | 4.5 | FAIL |
  | /node | `.el-tag` | `rgb(64,158,255)` | `rgb(236,245,255)` | 12px | **2.53** | 4.5 | FAIL |
  | /node | `.page-header-subtitle` | `rgb(144,147,153)` | `rgb(255,255,255)` | 13px | **3.08** | 4.5 | FAIL |
  | /channel | `.el-tag` / `.text-muted` / `.el-table__header th .cell` | `rgb(144,147,153)` | `rgb(255,255,255)` | 12–14px | **3.08** | 4.5 | FAIL |
  | /edge-device | `.status-indicator`（离线） | `rgb(144,147,153)` | `rgb(245,247,250)` | 12px | **2.87** | 4.5 | FAIL |
  | /edge-device | `.el-tag`（success `离线` 反白） | `rgb(255,255,255)` | `rgb(144,147,153)` | 12px | **3.08** | 4.5 | FAIL |
  | /logical-device | `.empty-description` | `rgb(144,147,153)` | `rgb(255,255,255)` | 14px | **3.08** | 4.5 | FAIL |
  | /device-configs | `.empty-description` | `rgb(144,147,153)` | `rgb(245,247,250)` | 14px | **2.87** | 4.5 | FAIL |
  | /monitor | `.el-tag`（warning） | `rgb(230,162,60)` | `rgb(253,246,236)` | 12px | **2.04** | 4.5 | FAIL |
  | /node/1 | `.badge`（离线徽标） | `rgb(105,119,139)` | `rgb(242,244,247)` | 12px | **4.13** | 4.5 | FAIL |
  | /node/1 | `.metrics-offline-tag` | `rgb(105,119,139)` | `rgb(242,244,247)` | 11px | **4.13** | 4.5 | FAIL |
  | — | **暗色对照** | | | | | | |
  | /node/1 | `.badge`（dark） | `rgb(141,144,149)` | `rgb(44,44,44)` | 12px | **4.39** | 4.5 | FAIL |
  | /node/1 | `.metrics-offline-tag`（dark） | `rgb(141,144,149)` | `rgb(54,54,56)` | 11px | **3.77** | 4.5 | FAIL |
  | /node/1 | `.tab-item` 激活态（dark） | `rgb(77,127,255)` | `rgb(41,41,43)` | 14px | **4.01** | 4.5 | FAIL |
  | /edge-device | `.el-tag`（dark, 白字灰底） | `rgb(255,255,255)` | `rgb(144,147,153)` | 12px | **3.08** | 4.5 | FAIL |
  - 合规对照：`/node/1` 亮色 `.page-header-subtitle` 4.55、`.tab-item` 未激活 6.41、`el-empty__description` 13.02
- **证据截图**: `desktop-1440-light-p15_node.png`、`desktop-1440-dark-p15_node_1.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:706-709`（离线徽标用次级灰 + 浅底）：
    ```css
    .status-tag.offline { background: var(--el-fill-color-light); color: var(--el-text-color-secondary); }
    ```
  - `frontend-shared/src/views/node/NodeList.vue:110-113`（模板同时输出文字"离线"+ 颜色）
  - `frontend-shared/src/views/edge-device/EdgeDeviceList.vue` `.status-indicator`（同模式）
  - 全局 token 定义 `frontend-shared/src/styles/theme.css:18-107`
- **规范条款**: §4.2.1（MUST 颜色语义）、§4.2.2（MUST 状态同时使用文字和颜色）、
  §4.5.1（MUST「每次触及全局颜色…验证亮色与暗色」）、§5.2.5（亮暗主题均可读）
- **严重度**: **高**（违反 MUST；与主控广播 2 中 D2-01 同源，属"亮色浅底彩色徽标"系统性缺陷）
- **建议**: 离线/中性徽标不要用 `--el-text-color-secondary`(#909399) 作前景色配 `--el-fill-color-light`；
  改用 `--el-text-color-regular`(#606266，对 #f5f7fa ≈ 5.9:1)。
  `.ws-status` 绿色文字改为 `--el-color-success-dark-2`。**验收**: 复跑 `p15`，
  断言所有采样点 `ratio >= (large?3:4.5)`。

---

### D2-07 【高】移动触控目标远低于 44px（行操作 24px 高；图标按钮 24x11）
- **页面**: node-list、edge-device-list、channel-list、node/1、edge-device/7053 | **视口**: mobile-390 / mobile-360 | **维度**: D6
- **现象**: 移动端主要任务（进详情/编辑/删除）的实际可点区高度只有 24px；`el-input-number` 加减按钮仅 24x11。
- **证据DOM**（`/tmp/uiux-d2-probe/probe-mobile-390-light.json` 等，`getBoundingClientRect` 原始值）：
  - **移动端主要任务（需 44px）**：
    | 页面 | label | 尺寸 | 位置 |
    |---|---|---|---|
    | /node | 配置 / 升级 / **删除** ×2 | **67x24** 各 | 卡片 footer (100,673)/(187,673)/(274,673) |
    | /edge-device | 详情 / 编辑 / **删除** ×3 | **71x24** 各 | 卡片 actions (80,658)…(254,1248) |
    | /node | 表格视图"详情"/"删除" | **52x24** | fixed 列内 |
    | /edge-device | 表格视图 3 个图标按钮 | **39x24** | fixed 列内 |
  - **高密度工具栏/组件（可 36px，但 24px 仍不达标）**：
    | 页面 | label | 尺寸 |
    |---|---|---|
    | /edge-device/7053 | 人工处置 ×7 | 82x24 |
    | /edge-device/7053 | 减少数值 / 增加数值 ×5 各 | **24x11** |
    | /edge-device/7053 | 更多操作（⋯） | 28x28 |
    | /edge-device/7053 | 导出CSV / 清空 / 保存 | 109x32 / 73x24 / 54x24 |
    | /channel | el-switch（行内启用状态） | **30x24**（core 30x16） |
    | 全域 | 页头 3 图标（菜单/通知/主题） | 32x32（**已由主控 D4 报为高，此处不重复计**） |
    | /logical-device | 全选 checkbox | 14x14 |
  - 首屏工具栏按钮高度普遍 32px（`添加节点` 107x32、`刷新` 77x32、`创建边缘设备` 137x32）
- **证据截图**: `mobile-390-light-_node.png`（可见卡片底部"配置/升级/删除"三枚 24px 高文字按钮）、
  `mobile-390-light-_edge-device.png`
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:144-157`（`size="small"` + `text` ⇒ 24px 高，无尺寸放大）：
    ```html
    <div class="card-footer">
      <el-button size="small" text @click.stop="handleQuickAction('config', node)">…配置</el-button>
      <el-button size="small" text @click.stop="handleQuickAction('ota', node)">…升级</el-button>
      <el-button size="small" text type="danger" @click.stop="handleDelete(node)">…删除</el-button>
    </div>
    ```
  - `frontend-shared/src/views/node/NodeList.vue:770-775`（`.card-footer` 只有 `padding-top:12px`，无移动端放大）
  - `frontend-shared/src/views/edge-device/EdgeDeviceList.vue:240-244`（同模式）
  - **反向核对**：全仓 grep `pointer:\s*coarse` **0 命中** ⇒ 规范 §4.4.7 的"粗指针增大点击区"策略**全站未实现**
- **规范条款**: §4.4.5（MUST「移动端图标按钮和 Switch/Slider 的实际可点击区域不小于 44x44px；
  仅在极高密度、非主要任务的工具栏中可降到 36px」）、§4.4.7（SHOULD `pointer: coarse`）
- **严重度**: **高**（违反 MUST；"删除"是破坏性操作却只有 67x24，误触风险与可达性同时受损）
- **建议**: 在 `@media (max-width: 768px)` 为 `.card-footer .el-button`/`.card-actions .el-button`
  设 `min-height:44px; min-width:44px`；`el-input-number` 用 `size="large"`；
  `el-switch` 移动端加 `--el-switch-height` 或外层 `padding` 扩到 44。
  **验收**: 复跑断言 `under36.filter(t=>t.inRow).length === 0`。

---

### D2-08 【高】节点详情面包屑在 <=768px 被 `display:none`，丢失"列表 → 当前实体"路径
- **页面**: node-detail | **视口**: mobile-390 / mobile-360 | **主题**: light | **维度**: D1/D7
- **现象**: 移动端进入节点详情后，页面内面包屑与顶栏面包屑**同时消失**，用户无法从界面判断当前实体属于哪个列表。
- **证据DOM**（`p6.json`）：
  - `mobile-390`：`local={w:0,h:0,x:0,y:0}, localDisplay="none", localText="首页/节点管理/测试C6N8/"`；
    `global={w:0,h:0}, globalDisplay="none", globalText="/节点管理/"`
  - `mobile-360`：同上
  - `desktop-1440` 对照：`local={w:1192,h:14,x:220,y:80}, localDisplay="block"`；
    `global={w:92,h:16,x:268,y:22}, globalDisplay="block"` ⇒ **仅窄屏丢失**
- **证据截图**: `mobile-390-light-p6-node1-breadcrumb.png`（实测存在）、`desktop-1440-light-p6-node1-breadcrumb.png`
- **证据代码**:
  - `frontend-shared/src/views/node/NodeOverview.vue:7-11`（页面内面包屑）：
    ```html
    <el-breadcrumb separator="/" class="no-breadcrumb">
      <el-breadcrumb-item :to="{ path: '/dashboard' }">首页</el-breadcrumb-item>
      <el-breadcrumb-item :to="{ path: '/node' }">节点管理</el-breadcrumb-item>
      <el-breadcrumb-item>{{ pageTitle }}</el-breadcrumb-item>
    ```
  - `frontend-shared/src/views/node/NodeOverview.vue:2002-2003`（根因）：
    ```css
    @media (max-width: 768px) {
      .no-breadcrumb { display: none; }
    ```
  - 顶栏面包屑 `views/layout/MainLayout.vue:619/931` 同样在窄屏隐藏（`p6.json` 实测 `globalDisplay="none"`）
- **规范条款**: §4.1.2（MUST「详情页标题优先使用用户可识别的实体名称…**面包屑至少表达"列表 -> 当前实体"**」）
- **严重度**: **高**（违反 MUST；实体名 `h1` 仍可见，故非阻断，但导航路径在移动端完全缺失）
- **建议**: `@media (max-width: 768px)` 改为保留面包屑并压缩（隐藏"首页"一项，保留"节点管理 / 测试C6N8"），
  而非 `display:none`。**验收**: 复跑断言 `localDisplay !== 'none' && local.w > 0`。

---

### D2-09 【中】列表页"创建"入口不闭环：`?action=add` 无消费者，空态 CTA 只弹提示
- **页面**: node-list | **视口**: desktop-1440 / mobile-390 | **主题**: light | **维度**: D5
- **现象**: 工具栏"添加节点"跳转 `/node?action=add`，但**全仓无任何代码读取 `query.action`**
  （grep 仅命中写入端）；空态 CTA"添加节点"点击后只弹一条 `ElMessage.info('跳转添加页面')`，**不跳转**。
  从节点列表页**无法进入创建流程**。
- **证据DOM/运行时**:
  - 点击空态 CTA 后：`url="http://127.0.0.1:8082/node"`（与 `urlBefore` 相同）、
    `messages=["跳转添加页面"]`、`emptyTitle="暂无节点"`、`kindClass="empty-state default empty"`
  - 点击工具栏"添加节点"后 URL 变为 `/node?action=add`，页面无任何弹窗（`overlays=1` 仅布局层）
  - grep `action=add` / `query.action` / `action === 'add'` 在 `frontend-shared/src/` **写入 1 处、读取 0 处**
- **证据截图**: `mobile-390-light-p7-empty-cta.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:71`（写入端）：
    ```html
    <el-button type="primary" @click="router.push('/node?action=add')">
    ```
  - `frontend-shared/src/views/node/NodeList.vue:258-260`（空态 CTA 是占位实现）：
    ```ts
    :quick-actions="[
      { label: '添加节点', icon: Plus, type: 'primary', handler: () => ElMessage.info('跳转添加页面') }
    ]"
    ```
  - 对照：`views/edge-device/EdgeDeviceList.vue:87-90` 的"创建边缘设备"**直接打开对话框**（`showCreateDialog = true`），
    是同仓的正确范式
- **规范条款**: §4.1（资源列表模板固定顺序"筛选与操作"）、§3.4.1（MUST 统一反馈）、§1.2.5（失败可理解、可恢复）
- **严重度**: **中**（不阻断浏览，但创建是列表页核心操作，两个入口均不闭环；`ElMessage.info('跳转添加页面')` 属调试残留文案）
- **建议**: 照搬 EdgeDeviceList 的 `el-dialog` 创建流程；空态 `handler` 改为
  `() => { showCreateDialog = true }`，并删除 `?action=add` 写法。
  **验收**: 点击后断言 `document.querySelector('.el-dialog') !== null`。

---

### D2-10 【中】节点卡片"配置"深链失效：`?tab=config` 被详情页忽略，落在"基本信息"
- **页面**: node-list → node-detail | **视口**: mobile-390 | **主题**: light | **维度**: D5
- **现象**: 卡片"配置"按钮跳 `/node/SIM-ESP32-01?tab=config`，到达后激活 TAB 仍是"基本信息"（第一个 TAB）。
- **证据DOM/运行时**：点击后 `url="http://127.0.0.1:8082/node/SIM-ESP32-01?tab=config"`、
  `activeTab="基本信息"`；`/node/1` 实测 TAB 列表 `["基本信息","总线配置","DMA 通道","关联设备","OTA 历史","系统日志","通道终端"]`，
  **无名为 config 的 TAB**。
- **证据截图**: `mobile-390-light-p5-node1-tabs.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:436-440`：
    ```ts
    if (action === 'config') { router.push(`/node/${node.node_id}?tab=config`) }
    ```
  - `frontend-shared/src/views/node/NodeOverview.vue:775`（`activeTab` 固定初值，无 query 消费）：
    ```ts
    const activeTab = ref('基本信息')
    ```
  - grep `route.query` 在 NodeOverview.vue **0 命中** ⇒ query 参数从未被读取
- **规范条款**: §3.1.1（MUST props/emits 是契约）、§4.1（信息架构）、§3.4.1
- **严重度**: **中**（不阻断，但用户点击"配置"后被送到错误 TAB，属可复核的交互缺陷）
- **建议**: `NodeOverview` 挂载时读 `route.query.tab`，把 `config` 映射到"总线配置"TAB
  （`activeTab.value = { config: '总线配置', ... }[String(route.query.tab)] ?? '基本信息'`）。
  **验收**: 直接访问 `/node/1?tab=config` 断言 `document.querySelector('.tab-item.active').textContent.trim()==='总线配置'`。

---

### D2-11 【中】编辑表单仍展示/提交创建专属字段（驱动/设备型号/硬件类型）
- **页面**: device-configs、edge-device-list | **视口**: desktop-1440 | **主题**: light | **维度**: D5
- **现象**: 配置模板的**编辑**对话框复用创建表单，仍显示"传感器驱动"（型号）与"硬件类型"，
  且提交时一并 `update`；edge-device 编辑弹窗 DOM 中亦残留创建专属字段节点。
- **证据DOM**:
  - edge-device 编辑弹窗实测：`dialogTitle="编辑边缘设备"`、
    `dialogLabelsVisible=["边缘设备名称","采集间隔 (ms)"]`、
    **`dialogLabelsHidden=["所属节点","硬件类型","硬件ID","从机地址","采集间隔 (ms)"]`**、
    `dialogStepsVisible=0`（编辑模式不复用向导，合规）
    ⇒ 创建专属字段**存在于 DOM 但不可见**（`v-show` 隐藏），**未实际展示给用户**
  - 创建弹窗对照：`dialogTitle="创建边缘设备"`、`dialogStepsVisible=1`、`dialogLabelsVisible=[]`（step0 为单选）
- **证据截图**: `mobile-390-light-p4-edge-edit.png`、`mobile-390-light-p4-edge-create.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/components/forms/DeviceConfigForm.vue:4`（标题区分，但表单体不区分）：
    ```html
    :title="isEdit ? '编辑配置模板' : '新建配置模板'"
    ```
  - `frontend-shared/src/components/forms/DeviceConfigForm.vue:26-49`（"传感器驱动"无 `v-if="!isEdit"` 守卫）
  - `frontend-shared/src/components/forms/DeviceConfigForm.vue:345-356`（编辑时同样提交型号/硬件）：
    ```ts
    const submitData = { name, description, device_type: form.device_type, hardware_type: form.hardware_type, … }
    if (isEdit.value) { await deviceConfigApi.update(props.config!.id, submitData) }
    ```
- **规范条款**: §4.3.2（MUST「创建与编辑的交互模型分离：**编辑不得展示或接受创建专属的节点、解析器、设备型号等字段**，
  除非后端确实支持修改且设计明确」）、§4.3.5（SHOULD 简单编辑直接显示表单）
- **严重度**: **中**（edge-device 侧仅 DOM 残留、用户不可见；device-configs 侧**确实展示并提交**型号字段，
  是否合法取决于后端是否支持修改 —— 见"未能验证"节）
- **建议**: `DeviceConfigForm` 中给"传感器驱动"与"硬件类型"加 `:disabled="isEdit"`，
  并在 `submitData` 中 `if (isEdit) { delete submitData.device_type; delete submitData.hardware_type }`。
  edge-device 侧把 `v-show` 改为 `v-if`，避免创建字段进入编辑态 DOM。

---

### D2-12 【中】`el-switch` 行内启用状态禁用但无原因（`disabled` 无 tooltip/aria 说明）
- **页面**: channel-list | **视口**: mobile-390 / desktop-1440 | **主题**: light | **维度**: D3/D7
- **现象**: 表格"启用状态"列的 `el-switch` 恒为 `disabled`，用户无法切换，**界面上没有任何原因说明**。
- **证据DOM**（`p8.json` `switchHTML` 原文）：
  ```html
  <div class="el-switch el-switch--small is-disabled is-checked">
    <input class="el-switch__input" type="checkbox" role="switch" aria-checked="true"
           aria-disabled="true" disabled id="el-id-…">
  ```
  - 外层 `div`：`title=null`、`aria-label=null`；`disabledReport` 扫描 5 层祖先 `hasReasonAttr=false`
  - 尺寸：outer `30x24`、core `30x16`
  - 单元格文字：`cellText="启用"`（**文字+颜色双重表达合规**，见 §4.2.2）
- **证据截图**: `mobile-390-light-p4-channel-disabled.png`（实测存在）
- **证据代码**: `frontend-shared/src/views/channel/ChannelList.vue:118-131`：
  ```html
  <el-switch :model-value="row.enabled" size="small" disabled />
  <span class="status-text" :class="{ off: !row.enabled }">{{ row.enabled ? '启用' : '禁用' }}</span>
  ```
- **规范条款**: §3.4.4（MUST「**禁用操作给出原因**，尤其是设备离线、数据未准备、权限不足或前置条件缺失。
  用 tooltip 包裹 disabled Element Plus 控件时，应按 `NodeDetail.vue:10-49` 用可接收 hover 的外层元素」）、§4.4.5（可访问名称）
- **严重度**: **中**（违反 MUST；但旁边有文字标签，用户不会误判状态，只缺"为何不能操作"）
- **建议**: 按 `NodeDetail.vue:10-49` 的参照用 `<el-tooltip content="通道启用状态由节点上报决定，请到节点详情修改">`
  包裹外层 `<span>`，并给 switch 补 `aria-label="通道启用状态（只读）"`。

---

### D2-13 【中】节点详情顶部图标为 13–16px 可点目标，且无 `aria-label`
- **页面**: node-detail | **视口**: desktop-1440 / mobile-390 | **主题**: light | **维度**: D6/D7
- **现象**: "编辑设备名称"(`ph-edit`) 与"复制设备ID"(`copy-icon`) 是裸 `<el-icon @click>`，
  尺寸 16x16 / 13x13，无 aria-label，键盘不可达。
- **证据DOM**（`p18.json`）：`phEdit={focused:false, tabIndexProp:-1, role:null, aria:null, w:16, h:16}`；
  `p16` 实测 `i.el-icon.copy-icon 13x13, role=None, tab=None, aria=None`
- **证据截图**: `mobile-390-light-light-_node_1.png`（实测存在）
- **证据代码**: `frontend-shared/src/views/node/NodeOverview.vue:24`、`:39-42`
- **规范条款**: §4.4.5（MUST 44x44）、§3.1.3（MUST role/tabindex/aria-label）
- **严重度**: **中**（非主要任务，但有明确替代做法）
- **建议**: 改为 `<el-button link :icon="EditPen" aria-label="编辑设备名称">`，并在移动端补 44px 命中区。

---

### D2-14 【低】术语旧称残留："设备模板"
- **页面**: node-detail（快速创建设备对话框） | **视口**: mobile-390 | **主题**: light | **维度**: D7
- **现象**: 用户可见文案出现规范禁止的旧称"设备模板"。
- **证据DOM**: `frontend-shared/src/components/node/QuickCreateDeviceDialog.vue:13` 原文：
  ```html
  <span>在节点 <strong>{{ nodeName || nodeId }}</strong> 上直接创建设备，无需预先创建设备模板</span>
  ```
- **grep 结果**: "设备模板" 在 `frontend-shared/src/` 命中 **2** 处
  （`components/node/QuickCreateDeviceDialog.vue:13` 用户可见；`views/node/NodeDetail.vue:400` 注释不可见）；
  "采集器" 命中 5 处但**全部在 `__tests__/` 内**（非用户可见）；"网关设备"/"采集节点"/"传感器模板" **0 命中**
- **证据截图**: 该文案仅在节点详情的"快速创建设备"对话框内，本次未截到该对话框（见"未能验证"节）
- **规范条款**: §1（「禁止在新增中文 UI 中混用"采集器""设备模板"等旧称」）
- **严重度**: **低**（单处文案，不影响任务完成）
- **建议**: 改为"无需预先创建配置模板"（与路由 `配置模板` 的 `meta.title` 一致）。

---

### D2-15 【低】`el-table` 表格视图首屏无"共 N 条"口径说明
- **页面**: logical-device-list | **视口**: mobile-390 | **主题**: light | **维度**: D1
- **现象**: 表格渲染 1003 行但无分页/无总数文案；`.mobile-table-hint` 只说可滑动，用户不知道总量。
- **证据DOM**: `pagination=0`、`mergeBtn="合并所选（0）"`、`hint="← 左右滑动查看完整表格 →"`；
  `tableText` 前缀 `"名称类型实例数（含已删）…"` 无"共 N 条"
- **证据截图**: `mobile-390-light-p3-ld-table.png`（实测存在）
- **规范条款**: §4.3.4（MUST「'共 N 条'必须与实际渲染记录一致」）、§4.3.1（MUST 统计范围）
- **严重度**: **低**（与 D2-02 同源，作为其验收补丁）
- **建议**: 在 `.mobile-table-hint` 同层加"共 {{ total }} 条"。

---

## 2. 本域必答问题逐条结论（契约交付物）

| # | 问题 | 结论 | 证据编号 |
|---|---|---|---|
| 1 | 页面模板合规 / PageHeader | monitor 有等价 `h2`（合规）；node/edge/logical/configs 4 页无任何 h1/h2，仅 16px `h3`，`/logical-device` 连 h3 都没有 | D2-06 |
| 2 | 容器套娃（§4.1.5） | **未发现违规**。实测最深链（mobile-390 node-list 卡片）：`body → el-main → (0 层) → el-card.collector-card` = **2 层**；BMS 详情 11 张 `el-card` 中 **1 张嵌套**（`command-card` 内 `command-frequency`），行容器 `temp-item` 深度 4（`body→main-container→el-main→el-card→temp-item`），**无"折叠面板→卡片头→灰面板→行卡"四层** | p13-bms |
| 3 | 表格移动横滚（§4.3.1） | **缺**：node-list、edge-device-list（无 wrapper、无 hint）；**有**：channel-list、logical-device-list（均有 wrapper+hint，且 logical-device `hasFixedRight=true`）。390px 操作列可达性：node-list `row0ButtonsInViewport=2/2`、edge-device `3/3`、**channel-list `0/1`**（"查看节点"位于 `x=819,right=901`，**视口 390 外，初始不可见**，需横滚，且 channel 表格本身**无 fixed 操作列**） | D2-03 |
| 4 | 触控目标 <36px | node-list 15 个、edge-device-list 18 个、node/1 3 个、edge-device/7053 30 个、logical-device 8 个、configs 10 个、channel 10 个 —— 逐项见 D2-07 | D2-07 |
| 5 | 行操作反馈（§4.3.4） | **缺 `aria-busy`**。刷新中实测 `ariaBusyCount=0`、`ariaBusyTrue=0`；但**锁定范围正确**：`btnLoadingClass="el-button el-button--primary is-loading"`、`btnDisabled=true`、`spinner=2`，而 `cardBtnsDisabled=0 / cardBtns=6` ⇒ **只锁刷新按钮、未锁整表**，符合 §3.3.1 | p11 |
| 6 | 编辑 vs 创建（§4.3.2） | edge-device 编辑：创建专属字段在 DOM 但 `v-show` 隐藏（`dialogLabelsHidden` 5 项）、向导 `stepsVisible=0` ⇒ **未展示**；device-configs 编辑：**确实展示并提交** 驱动/硬件类型 ⇒ 见 D2-11 | D2-11 |
| 7 | 删除危险操作（§3.4.3） | **二次确认有**：`dlgKind="el-message-box"`、`title="警告"`、`allText=["确定要删除节点 \"自动化验收虚拟节点\" 吗？此操作不可恢复。"]` ⇒ **含对象身份 + 不可逆影响，合规**。但 **①确认按钮是 primary 蓝色而非 danger**（`bg="rgb(64, 158, 255)"`，暗色同）；**②默认焦点落在"删除"上**（`activeElement="button.el-button el-button--primary"`, `activeText="删除"`）；**③触发按钮仅 67x24 / 71x24** | D2-07 / D2-16 |
| 8 | 状态表达（§4.2.2） | **文字+颜色双表达合规**：node-list `.status-tag` `text="离线"` + `color=rgb(144,147,153)`；edge-device `.status-indicator` 同；channel `.status-text` `"启用"`。**离线数据时效与操作限制未说明**：`offlineMentions` 在 node-list 仅 `["离线"]`（无"最后更新/数据时效/不可操作"），仅 node-detail 有 tooltip「设备离线，无法操作」（`disabledTooltip=["设备离线，无法操作"]`，且**需 hover 才可见，移动端无 hover**） | D2-17 |

---

### D2-16 【高】危险确认按钮用 primary 蓝色、默认焦点在"删除"
- **页面**: node-list（`ElMessageBox`） | **视口**: mobile-390 / mobile-360 / desktop | **主题**: light + dark | **维度**: D3
- **现象**: 删除节点的二次确认框中，**"删除"按钮是 Element Plus 默认蓝色 primary**（非 danger），
  且 **默认焦点直接落在"删除"上**，用户按 Enter 即执行破坏性操作。
- **证据DOM**（`p3-*.json` 三组一致）：
  - light：`{t:"取消", bg:"rgb(255,255,255)"}`、`{t:"删除", bg:"rgb(64, 158, 255)", color:"rgb(255,255,255)"}`
  - dark：`{t:"删除", bg:"rgb(64, 158, 255)"}`（仍是蓝色，非 danger）
  - `activeElement="button.el-button el-button--primary"`、`activeText="删除"`
  - 对照合规实现：edge-device 删除对话框 `{t:"确认删除", cls:"el-button el-button--danger", bg:"rgb(245, 108, 108)"}`
- **证据截图**: `mobile-390-light-p3-node-delete.png`、`mobile-360-light-p3-node-delete.png`、
  `mobile-390-dark-p3-node-delete.png`（均实测存在）
- **证据代码**: `frontend-shared/src/views/node/NodeList.vue:447-451`：
  ```ts
  await ElMessageBox.confirm(
    `确定要删除节点 "${row.name}" 吗？此操作不可恢复。`,
    '警告',
    { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }   // ← 无 confirmButtonClass:'el-button--danger'
  )
  ```
  （对比 `views/automation/AutomationRules.vue:525` 同用 `ElMessageBox.confirm` 亦无 danger class；
  `components/device/DeviceDeleteDialog.vue:70` 用独立 dialog + `type="danger"` 是正确范式）
- **规范条款**: §3.4.3（MUST「破坏性动作使用 **danger 视觉层级**、二次确认和影响说明；
  删除必须与常规主操作隔离，**不能靠颜色或图标单独承担风险**」）、
  §4.3.4（MUST「危险确认使用 danger 确认按钮；**默认焦点和文案应避免诱导性确认**」）
- **严重度**: **高**（违反 MUST；"删除"是最高风险操作，蓝色确认按钮 + 默认聚焦构成诱导性确认）
- **建议**: `{ confirmButtonText:'删除', confirmButtonClass:'el-button--danger', type:'warning',
  autofocus:false }`（或统一迁移到 `DeviceDeleteDialog.vue` 模式）。
  **验收**: 复跑断言确认按钮 `getComputedStyle(btn).backgroundColor === 'rgb(245, 108, 108)'`
  且 `document.activeElement.textContent.trim() !== '删除'`。

---

### D2-17 【中】离线设备的数据时效与操作限制未在列表页说明
- **页面**: node-list、edge-device-list、channel-list | **视口**: mobile-390 | **主题**: light | **维度**: D3/D7
- **现象**: 列表页所有实体均为 `offline`，但页面只显示"离线"二字，
  **不说明数据最后更新时间、也不说明离线导致哪些操作受限**；卡片上的"配置/升级"仍可点击进入。
- **证据DOM**：
  - node-list `offlineMentions=["离线"]`（`bodyText` 关键词筛查，未命中"数据时效/最后更新/不可操作/心跳"）
  - edge-device `offlineMentions=["离线"]`
  - channel-list `offlineMentions=[]`
  - node-detail 有 tooltip：`disabledTooltip=["设备离线，无法操作"]`，
    且 `actionBtns=[{t:"同步配置",disabled:true},{t:"OTA 升级",disabled:true},{t:"测延迟",disabled:true},{t:"刷新",disabled:false}]`
    ⇒ **详情页合规**，列表页缺失
- **证据截图**: `mobile-390-light-_node.png`、`mobile-390-light-_edge-device.png`（实测存在）
- **证据代码**:
  - `frontend-shared/src/views/node/NodeList.vue:110-113`（只有状态标签，无时效）
  - `frontend-shared/src/views/node/NodeList.vue:138-141`（`上线时间` 用 `formatRelativeTime`，
    但离线时该值可能为陈旧数据，未标注"最后上报"）
  - `frontend-shared/src/views/node/NodeList.vue:472-487`（`formatRelativeTime` 无"数据时效"语义）
- **规范条款**: §4.3.3（MUST「**离线说明数据时效和操作限制**；离线、占用、未知、失败均不是空状态」）、
  §4.2.2、§1.2.1
- **严重度**: **中**（违反 MUST；详情页已实现，属列表页未对齐）
- **建议**: 离线卡片的状态徽标后缀加"· 最后上报 {{相对时间}}"，并在卡片上禁用"升级"或加原因 tooltip；
  移动端不能依赖 hover，需用可见文字。
  **验收**: 断言离线卡片 `textContent` 匹配 `/最后(上报|更新)/`。

---

## 3. 严重度分布

| 严重度 | 条数 | 编号 |
|---|---|---|
| **阻断** | **1** | D2-01 |
| **高** | **8** | D2-02, D2-03, D2-04, D2-05, D2-06, D2-07, D2-08, D2-16 |
| **中** | **6** | D2-09, D2-10, D2-11, D2-12, D2-13, D2-17 |
| **低** | **2** | D2-14, D2-15 |
| **合计** | **17** | |

### 按页面分布（一条问题跨多页时计入主责页）

| 页面 | 阻断 | 高 | 中 | 低 | 小计 |
|---|---|---|---|---|---|
| /node 节点列表 | 1 | 3 | 2 | 0 | 6 |
| /node/1 节点详情 | 0 | 3 | 2 | 1 | 6 |
| /edge-device 边缘设备列表 | 1 | 3 | 1 | 0 | 5 |
| /edge-device/:id BMS 详情 | 0 | 2 | 0 | 0 | 2 |
| /channel 通道管理 | 0 | 1 | 1 | 0 | 2 |
| /logical-device 逻辑设备列表 | 1 | 2 | 0 | 1 | 4 |
| /device-configs 配置模板 | 1 | 2 | 1 | 0 | 4 |
| /monitor 系统监控 | 0 | 1 | 0 | 0 | 1 |

> 说明：D2-01（失败伪装成正常）与 D2-06（对比度）跨多页，已在分布表内按页计数，
> 故各页小计之和大于 17。

## 4. 规范条款分布

| 规范条款 | 条数 | 涉及编号 |
|---|---|---|
| §3.1.3（可点击非原生容器 role/tabindex/aria-label，MUST） | 3 | D2-05, D2-12, D2-13 |
| §3.4.2（空态不是错误态替代，MUST） | 1 | D2-01 |
| §3.4.3（危险操作 danger 层级，MUST） | 1 | D2-16 |
| §3.4.4（禁用给出原因，MUST） | 1 | D2-12 |
| §3.2.5（不得以默认值伪造事实，MUST） | 1 | D2-01 |
| §4.1.1（页面标题，MUST） | 1 | D2-06（表内） |
| §4.1.2（面包屑表达"列表→实体"，MUST） | 1 | D2-08 |
| §4.3.1（mobile-table-wrapper + hint，MUST） | 1 | D2-03 |
| §4.3.2（编辑不得展示创建专属字段，MUST） | 1 | D2-11 |
| §4.3.3（离线说明时效与限制，MUST） | 1 | D2-17 |
| §4.3.4（危险确认 danger + 默认焦点，MUST） | 1 | D2-16 |
| §4.2.1 / §4.5.1（亮暗主题可读，MUST） | 1 | D2-06（对比度） |
| §4.4.1（360px 可完成核心操作，MUST） | 1 | D2-04 |
| §4.4.5（44x44 / 工具栏 36，MUST） | 1 | D2-07 |
| §4.5.4（降采样/虚拟列表/显示上限，SHOULD） | 1 | D2-02 |
| §1（术语旧称） | 1 | D2-14 |
| §3.4.1 / §1.2.5（反馈统一与可恢复） | 1 | D2-09 |
| §4.3.4（"共 N 条"一致，MUST） | 1 | D2-15 |
| §3.1.1 / §4.1（交互契约） | 1 | D2-10 |
| §4.3.2 / §4.3.5（编辑与创建分离，SHOULD） | 1 | D2-11 |

## 5. 规范 §6「当前整改基线」实测复核（本域相关条目）

| §6 条目 | 本域实测 | 结论 |
|---|---|---|
| P1「26 个 Vue 文件含 dialog、多个列表仍需核对移动横滚模式」 | node-list / edge-device-list 表格视图 @390 实测 `wrapped=false, hasHint=false`；channel-logical 已包裹 | **仍在**，见 D2-03 |
| P1「页面标题模式未全站覆盖，NodeList 与 EdgeDeviceList 等需按列表模板持续收敛」 | `/node`、`/edge-device`、`/logical-device`、`/device-configs` 均无 h1/h2；`/channel` 有 PageHeader | **仍在**，见 §0 |
| P0 WebSocket URL 二次追加 | **不在本域范围**（已由 D4 域实测确认仍在，本报告不重复计） | 转交 |
| P2「主题 token 已完善，但页面 inline style 和硬编码色仍较多」 | 本域 `/node/1` 实测 `inlineStyled` 存在；`NodeList.vue:675/678-679` 有 `color:#fff` 与 `linear-gradient` | **仍在**（未单列条目，归入 D2-06 验收） |
| P2 i18n 未使用 `$t` | 本域 `grep $t/useI18n` 无命中 | **已闭环（与本域无关，规范已豁免局部改造）** |

## 6. 未能验证的部分（契约 §7.8）

1. **BMS / 逆变器详情页在 light 主题、五视口全矩阵下的表现未穷尽**。本次 BMS 详情
   （`/edge-device/7053`）只在 `desktop-1440-light` 与 `mobile-390-light` 做了完整度量与截图；
   `tablet-768`、`laptop-1024`、暗色三视口只有基线 `dom-facts.json` 的计数（`smallCount` 等），
   **未逐项复核**。逆变器详情（`/edge-device/7054` 实为 `sn3001_rain`，走 `GenericDeviceDetail`）
   在 390 下只有 4 个 <36px 目标 + 1 个"重试"按钮，**因该实体接口异常而渲染了错误态，
   未能审计其正常态**。
2. **`ehome_uiux` 审计库无 `device_configs` 数据**（`GET /api/v1/device-configs` 返回
   `{"list":[],"total":0}`），因此**配置模板的编辑对话框只审计了创建态**；
   D2-11 中"编辑展示驱动/硬件类型"的依据是**源码**（`DeviceConfigForm.vue:26-49, 345-356`）
   与 `isEdit` 计算属性，**未在真实编辑态截图**。
3. **逻辑设备"合并"完整流程未走通**：`/logical-device` 实测 1003 行但**全选 checkbox 为
   `is-disabled`**（`p16` 实测 `span.el-checkbox__input.is-disabled 14x14`），
   选中 ≥2 个同类型设备的多选、合并预览、冲突对话框、搬迁进度轮询**均未触发**，
   该路径的 DOM 事实缺失。
4. **删除操作的真实执行未验证**：所有删除确认弹窗均以 Escape 取消，
   **未发出任何 DELETE 请求**（保护审计库数据）。因此"删除成功后列表刷新/统计更新"
   只有源码依据（`NodeList.vue:453-463`），无运行时证据。
5. **未做屏幕阅读器实测**：`role`/`aria-*` 结论均来自 DOM 属性原文与
   `element.focus()` 的 `document.activeElement` 判定，**未用 NVDA/VoiceOver 复核**
   可访问名称的可朗读性。
6. **性能结论未量化**：`/logical-device` 的 1003 行/28541 元素的**渲染耗时与滚动帧率
   未用 Performance API 测量**，D2-02 的"影响可用性"依据是 DOM 规模与无分页控件，并非实测卡顿。
7. **`el-message-box` 的 `role`/`aria-modal` 缺失**（`p3` 实测 `dialogRole=null, dialogAriaModal=null`）
   仅在 node-list 删除确认上取样，**未确认是否为 Element Plus 全局行为**，故未单列为问题条目。
8. **`/monitor` 只审了标题与触控目标**（4~5 个），其数据分析模板顺序、图表主题、
   KPI 四列紧凑卡等**未纳入本域深度审计**（该页归属偏 D3/D4 域）。

## 7. 被证据推翻的初判（契约 §7.8）

1. **【推翻】"编辑弹窗泄露创建专属字段"（edge-device）** —— 初判依据是
   `el-dialog` 内 DOM 存在"所属节点/硬件类型/硬件ID/从机地址"标签，怀疑违反 §4.3.2。
   实测（`p4`）这 5 项全部落在 `dialogLabelsHidden`（`offsetParent===null`、宽度 0），
   编辑态 `dialogLabelsVisible=["边缘设备名称","采集间隔 (ms)"]`、`dialogStepsVisible=0`。
   ⇒ **未展示给用户**，降级为"DOM 残留"并只按 §4.3.2 提"建议改 `v-show` 为 `v-if`"。
2. **【推翻】"device-configs 对话框 footer 不可达"** —— 初判依据
   `footerBottom=1032 > vh=844` 且 `overlayScrollable=false`。复测（`p8`）发现我**测错了 overlay**：
   页面上有 5 个 `.el-overlay` 层，最后一个 `.el-overlay-dialog` 的
   `scrollHeight=1098, clientHeight=844, scrollTop=254`。执行 `mouse.wheel(0,600)` 后
   `footerTop=730, footerBottom=778 ≤ 844` ⇒ **footer 可滚动到达，不是缺陷**。已从清单移除。
3. **【推翻】"el-select 不可键盘访问"（11 处）** —— 首版探针按
   `cursor:pointer && tabIndex<0` 判定为违规。实测内部
   `<input class="el-select__input">` 的 `tabIndex=0`，且键盘序列
   `["input.el-select__input","input.el-select__input","button…卡片视图",…]`
   证明两个 select 都能 Tab 到达 ⇒ **误报，已剔除**。这正是契约 §2.2、主控广播 1/3
   "恒为 0 或恒为大的计数都要先看分母/选择器"的同类教训。
4. **【推翻】"`el-switch` 无 role/aria"** —— 外层 `div` 确实无，但 `outerHTML` 原文显示
   内部 `<input role="switch" aria-checked="true" aria-disabled="true">` 语义完整 ⇒ **合规**，
   只保留"尺寸 30x24"与"禁用无原因"两条。
5. **【推翻】"StatCard 第 4 张卡缺 role 是缺陷"** —— 该卡（"本页告警"）源码
   （`NodeList.vue:29-32`）**无 `@click`**，`StatCard.vue:44` 的
   `isClickable=Boolean(attrs.onClick)` 使其正确地不输出 role/tabindex。
   前 3 张带点击的卡实测 `role="button" tabindex="0" aria-label="查看本页节点"` ⇒ **合规**。
6. **【推翻】"pageHeaderPresent=false 一律违规"** —— `/monitor` 虽无 `[class*="page-header"]`，
   但实测存在 `<h2>系统监控</h2>`（20px），满足 §4.1.1 的"或**等价的清晰标题**"表述
   ⇒ 不计入违规，只在 §0 表格标注。
7. **【修正】触控目标总量低于主控基线** —— 主控基线记录 node-list `smallCount=13`、
   edge-device-list `smallCount=15`；本域探针（`hasTouch:true, isMobile:true` + 更宽选择器）
   实测 **15 / 18**。差异来自选择器与触控上下文不同，**不是数据冲突**；
   报告统一采用本域实测值并已注明探针参数。

## 8. 证据目录清单

**目录**: `/tmp/uiux-d2-probe/`（本域定向复跑）+ `/tmp/uiux-evidence/`（主控全量基线，已读未重跑）
**主控基线复用**: `/tmp/uiux-evidence/dom-facts.json`（132 条记录）、`*.png`（161 张）—— **未重复跑全量**
**本域产出**:
- **PNG 77 张**（`ls /tmp/uiux-d2-probe/*.png | wc -l` 实测 = 77）
  - 主控基线同参数复跑：`/tmp/uiux-d2/`（40 张）+ `/tmp/uiux-d2-devcfg/`（16 张）
  - 定向取证：`mobile-390-light-p3-*.png`、`mobile-360-light-p3-*.png`、`mobile-390-dark-p3-*.png`、
    `*-p6-*.png`、`*-p8-*.png`、`*-i-*.png`、`desktop-1440-*-p15*.png`、`fail500-*.png`
- **JSON 15 个**：`p3-mobile-390-light.json`、`p3-mobile-360-light.json`、`p3-mobile-390-dark.json`、
  `p4-mobile-390-light.json`、`p5-mobile-390-light.json`、`p6.json`、`p7.json`、`p8.json`、
  `interact-mobile-390-light.json`、`interact-mobile-360-light.json`、
  `probe-mobile-390-light.json`、`probe-mobile-390-light-light.json`、`probe-desktop-1440-light-light.json`、
  `p15-a11y-contrast.json`、`p16-a11y-roots.json`、`p17-failure-injection.json`
  （外加 `/tmp/uiux-d2/dom-facts.json` 与 `/tmp/uiux-d2-devcfg/dom-facts.json` 两份标准探针产物）
- **新增取证脚本**（仅在 `frontend-shared/tools/`，未触碰 `src/`）：
  `uiux-d2-probe.mjs`、`uiux-d2-interact.mjs`、`uiux-d2-p3.mjs` … `uiux-d2-p18.mjs`、`uiux-d2-kpi.mjs`

## 9. 临时数据声明

- **未创建任何临时数据**：全部探测为只读（GET + 页面交互），删除确认一律 Escape 取消，
  **未发出任何 POST/PUT/DELETE**，因此 `ehome_uiux` 库内容与审计开始时一致，**无需还原**。
- 失败注入通过 Playwright `page.route().fulfill()` 在**浏览器侧**伪造 500 响应，
  **未修改后端、未写库**。

---

## 10. 优化方案（按严重度排序，含验收方法）

| 序 | 编号 | 最小改动点 | 验收度量 |
|---|---|---|---|
| 1 | D2-01 | 4 个列表页加 `error` ref + `EmptyState kind="error"` 分支，优先于空态；KPI 失败时渲染 `—` | `page.route(500)` 后 `[data-test="error-state"]`.length===1 且所有 `.stat-value` 为 `—` |
| 2 | D2-16 | `NodeList.vue:450` 加 `confirmButtonClass:'el-button--danger'`，`autofocus:false` | 确认按钮 `backgroundColor==='rgb(245, 108, 108)'`；`activeElement.textContent!=='删除'` |
| 3 | D2-05 | 抽 `clickableContainer` 指令（照 `StatCard.vue:6-10`），应用到卡片/TAB/图标 | `p16` 探针 `nonNativeRows.filter(r=>!r.compliant).length===0` |
| 4 | D2-07 | `@media(max-width:768px)` 卡片操作按钮 `min-height:44px`；input-number `size="large"` | `under36.filter(t=>t.inRow).length===0` |
| 5 | D2-03 | node-list / edge-device-list 表格按 `LogicalDeviceList.vue:42-44` 补 wrapper+hint；操作列 240→160 | `wrappedByMobileTableWrapper===true && hasHint===true`；`fixedSharePct<60` |
| 6 | D2-04 | `DeviceConfigList.vue:819` `justify-content:flex-end`→`flex-start` + `flex-wrap:wrap` | `Math.min(...btn.x) >= cardBody.x` @360 |
| 7 | D2-06 | 离线徽标前景改 `--el-text-color-regular`；`.ws-status` 绿字改 `--el-color-success-dark-2` | `p15` 全部采样 `ratio >= (large?3:4.5)` |
| 8 | D2-02 | 后端加分页；过渡期 `.slice(0,50)` + "显示前 N 条/共 M 条" | `.el-table__row`.length <= 50 |
| 9 | D2-08 | `NodeOverview.vue:2003` 改压缩而非 `display:none` | `localDisplay!=='none' && local.w>0` @390 |
| 10 | D2-17 | 离线卡片加"· 最后上报 X"；禁用"升级"并给可见原因 | 离线卡片文本匹配 `/最后(上报|更新)/` |
| 11 | D2-09 | 空态 CTA 改为 `showCreateDialog=true`；`?action=add` 改为直接开弹窗 | 点击后 `.el-dialog` 存在 |
| 12 | D2-10 | `NodeOverview` 读 `route.query.tab` 映射到"总线配置" | `/node/1?tab=config` → `.tab-item.active` 文本为"总线配置" |
| 13 | D2-11 | 编辑态禁用驱动/硬件类型并从 `submitData` 剔除 | 编辑弹窗 `querySelectorAll('.el-form-item__label')` 不含"传感器驱动" |
| 14 | D2-12 | switch 外层 `el-tooltip` + `aria-label` | 5 层祖先扫描 `hasReasonAttr===true` |
| 15 | D2-13 | 图标改 `el-button link` + `aria-label` | `phEdit.focused===true && aria!==null` |
| 16 | D2-14 | 文案"设备模板"→"配置模板" | grep "设备模板" 在生产代码中 0 命中 |
| 17 | D2-15 | `.mobile-table-hint` 同层加"共 N 条" | 文本匹配 `/共 \d+ 条/` |
