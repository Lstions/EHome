# UI/UX 审计报告 — 域 D2/D4/D6/D7：全局骨架与可访问性

> 审计对象：EHomeSystem `frontend-shared/`（Vue 3 + Element Plus）
> 依据：`docs/分析/UIUX审计契约-2026-09-13.md`（下称契约）+ `docs/规范/前端开发与UIUX设计规范.md`（下称规范，唯一权威）
> 环境：后端 `http://127.0.0.1:8082`（同源托管 `frontend-shared/dist`），审计库 `ehome_uiux`
> 覆盖页面：`/login`（含 `InitializeAdminForm.vue`）、`MainLayout.vue`（全站外壳）、`/profile`、`/403`、不存在路由（404）
> 视口：1440×900 / 390×844 / 360×800；主题：light + dark 全部实测
> 证据目录：`/tmp/uiux-d4/`（37 张 PNG + 5 份 JSON）；基线复用 `/tmp/uiux-evidence/dom-facts.json`（132 条）
> 取证脚本（只读，未改 `src/`）：`tools/uiux-d4-probe.mjs`、`probe2.mjs`、`probe3.mjs`、`probe4.mjs`、`probe5.mjs`、`uiux-d4-menucheck.mjs`、`uiux-d4-menucheck2.mjs`

---

## 0. 结论摘要

本域共发现 **19 个问题**：阻断 0 / 高 4 / 中 11 / 低 4。

规范 §6 的三条历史偏差在本域复核结果：
| §6 条目 | 复核结论 |
|---|---|
| P0 WebSocket 完整 URL 二次追加 | **仍在**（问题 #1，可确定性复现；当前 `.env` 走相对路径故未触发） |
| P0 `NetworkBanner` 读未导出的 `lastError` | **已闭环**（代码锚点 + 全仓 grep 复核，见 §4；WS-only 分支的可见呈现未能端到端复现，见 §5.1） |
| P2 主题 token 已完善但硬编码色仍多 | **仍在**（问题 #5、#17） |

---

## 1. 问题清单（契约 §6 格式）

### #1 【高】WebSocket 完整 URL 被二次追加 `/api/v1/ws`

- **页面**：全站（MainLayout 外壳，所有已登录页） | **视口**：不适用（构建期配置） | **主题**：不适用 | **维度**：D2 配色与主题 / 环境契约
- **现象**：当 `VITE_WS_URL` 被配置为**完整端点**（`wss://host/api/v1/ws`）时，`connect()` 会再追加一次 `/api/v1/ws`，得到 `wss://host/api/v1/ws/api/v1/ws`，WebSocket 永不连接成功，顶栏状态恒为「离线」。
- **证据DOM**：不适用（发生在 `new WebSocket()` 之前）。以逐行复刻 `websocket.ts:131-147` 的确定性运行结果为准：

  | 输入 `VITE_WS_URL` | `VITE_BASE_PATH` | 实际构造出的 URL |
  |---|---|---|
  | `/api/v1/ws`（仓库当前值） | `` | `wss://example.com/api/v1/ws?token=tok` ✅ |
  | `/api/v1/ws` | `/ehome-dev/` | `wss://example.com/ehome-dev/api/v1/ws?token=tok` ✅ |
  | `wss://host/api/v1/ws` | `` | `wss://host/api/v1/ws/api/v1/ws?token=tok` ❌ |
  | `wss://host` | `` | `wss://host/api/v1/ws?token=tok` ✅（恰好正确） |
  | `wss://host/api/v1/ws` | `/ehome-dev/` | `wss://host/api/v1/ws/api/v1/ws?token=tok` ❌ |

- **证据截图**：不适用（无可见差异，因为当前配置未触发）。顶栏在未连接时的实际呈现见 `shell-dark-1440.png`（`.ws-status` 显示「在线」，即当前 `.env` 走相对路径分支、连接正常）。
- **证据代码**：
  - `frontend-shared/src/stores/websocket.ts:139-140`
    ```ts
    if (wsUrl.startsWith('ws://') || wsUrl.startsWith('wss://')) {
      statusUrl = `${wsUrl}/api/v1/ws`      // ← 完整端点被二次追加
    ```
  - 同文件 `131-134` 已先把相对路径拼上 `VITE_BASE_PATH`，但对完整 URL 原样透传，随后在 140 行无条件追加路径——两处逻辑对「完整 URL 的语义」假设不一致。
  - 构建期注入链：`Dockerfile:24-27`（`ARG VITE_WS_URL` → `ENV`），`.env.production:7-9` 注释明确「部署时通过 /etc/ehomesystem/web.env 或 build args 覆盖」。
- **测试覆盖核实**：`src/stores/__tests__/websocket.spec.ts:166-175` 名为「VITE_WS_URL 为完整 URL 时不加前缀」，但断言是
  ```ts
  expect(MockWebSocket.instances[0].url).toContain('wss://example.com/ws')   // :172
  ```
  对缺陷 URL `wss://example.com/ws/api/v1/ws` 该断言**同样通过**（已实测）。即：**该测试无法捕获本缺陷**，§6 建议的「补四类 URL 测试」尚未落实。
- **规范条款**：§3.6.1 / §6 P0「WebSocket store 的完整 URL 拼接会在完整 `VITE_WS_URL` 后再次追加 `/api/v1/ws`」；收敛方向「固化环境变量为完整端点或 base URL 之一，补四类 URL 测试」。
- **严重度**：**高**（违反 §6 明列的 P0 缺陷；触发时实时通道整体失效且顶栏报「离线」，属核心功能不可用。当前仓库 `.env` 与 `.env.production` 均为相对路径，故未实际触发，按契约「实测复核」判为「仍在但未触发」，不给阻断）
- **建议**（最小改动）：
  1. 二选一固化契约——推荐「完整端点」语义：把 139-141 行改为
     ```ts
     statusUrl = wsUrl.startsWith('ws://') || wsUrl.startsWith('wss://') ? wsUrl : `${protocol}//${window.location.host}${wsUrl}`
     ```
     即完整 URL 原样使用，不再追加路径（同时删除硬编码的 `/api/v1/ws`）。
  2. 在 `.env.production` 与 `vite-env.d.ts` 注明 `VITE_WS_URL` 是**完整端点**。
  3. 把 `websocket.spec.ts:172` 的 `toContain` 改为 `toBe` 全等断言，并补四类：相对路径 / 相对+`VITE_BASE_PATH` / 完整端点(`wss://host/api/v1/ws` → 恒等) / 完整端点+`VITE_BASE_PATH`(不受前缀影响)。
- **验收方法**：单测断言 `new WebSocket` 收到的 url 字符串与上表右列**逐字符相等**；浏览器侧可用 `UIUX_WS` 环境变量注入完整端点后观察 `.ws-status` 的 `className` 是否含 `connected`。

---

### #2 【高】桌面侧栏导航整体键盘不可达

- **页面**：全站外壳（所有走 MainLayout 的页面） | **视口**：desktop-1440 | **主题**：light + dark 均复现 | **维度**：D7 可访问性
- **现象**：桌面侧栏的 12 个导航项**无法通过键盘到达**——Tab 键 30 次遍历整页，焦点从未进入 `.sidebar`；唯一进入方式是用鼠标点击。侧栏品牌 Logo 同样不可聚焦。
- **证据DOM**：
  - `document.querySelector('.sidebar-menu')`（`ul.el-menu`）→ `{tabindex: null, tabIndexProp: -1, focusable: false}`
  - 12 个 `.sidebar .el-menu-item` 的 `getAttribute('tabindex')` 全部为 `"-1"`，`role="menuitem"`
  - 真实键盘遍历（`uiux-d4-menucheck2.mjs`）：Tab 第 1→22 次依次落在 `collapse-btn → 全局搜索 input → 通知按钮 → 主题按钮 → user-menu → 4 张 StatCard → 单选 → 选择器 → 查看设备 → 刷新 → 3 个 device-link → BODY → 循环`，**`inSidebar` 恒为 false**；`30 次 Tab 内是否到达侧栏菜单项: false`。
  - 对已聚焦项按 `ArrowDown`：`仪表盘 → {"txt":"仪表盘","ti":"-1"}`（焦点不移动）。
  - 对照：鼠标点击同一项 → `url: http://127.0.0.1:8082/node`（功能本身正常，仅键盘路径缺失）。
- **证据截图**：`shell-light-1440.png`、`shell-dark-1440.png`（侧栏菜单项可见，但无任何焦点样式，因为无法聚焦）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:16-27`
  ```vue
  <el-menu :default-active="activeMenu" :collapse="uiStore.sidebarCollapsed"
           :collapse-transition="false" router class="sidebar-menu">
    <el-menu-item v-for="item in menuItems" :key="item.path" :index="item.path">
  ```
  未给 `<el-menu>` 传任何键盘/焦点相关配置；`.sidebar` 样式（`MainLayout.vue:490-496`）也未提供 `tabindex` 或等效键盘入口。
- **规范条款**：§3.1.3 `MUST`「为可点击的非原生容器补齐 `role`、`tabindex`、明确 `aria-label`，并让 Enter/Space 与 click 调用同一行为」
- **严重度**：**高**（违反 MUST 条款，且导致主布局的核心导航在键盘下完全不可用）
- **建议**：Element Plus 垂直菜单本身是 roving-tabindex 模型，但当前每个 item 都是 `-1` 且容器不可聚焦，等于没有入口。最小改动：把 `<el-menu>` 容器设为可聚焦的菜单入口，或对每个 `<el-menu-item>` 显式声明 `tabindex="0"` + `@keydown.enter`/`@keydown.space` 触发 `router.push(item.path)`，并让首项或当前激活项为 `tabindex="0"`、其余 `-1`（标准 roving 模式）。
- **验收方法**：CDP 从 `body` 连续 Tab，断言在 ≤20 次内 `document.activeElement.closest('.sidebar')` 非空；对侧栏首项按 Enter 后断言 `location.pathname` 变为该 item 的 `index`。

---

### #3 【高】移动端页头图标按钮触控区 32×32，低于 §4.4.5 的 44×44

- **页面**：全站外壳（所有移动端页面） | **视口**：mobile-390、mobile-360 | **主题**：light + dark 均实测 | **维度**：D6 响应式与触控
- **现象**：移动端页头三个图标按钮（打开导航菜单 / 打开通知中心 / 切换主题）实际可点击区域均为 **32×32 px**。
- **证据DOM**（`getBoundingClientRect()`，`mobile_390_shell` / `mobile_360_shell`）：
  | 按钮（aria-label） | 390px 下 box | 360px 下 box |
  |---|---|---|
  | 打开导航菜单 | `{w:32,h:32,x:20,y:14,right:52,bottom:46}` | `{w:32,h:32,x:20,y:14,right:52,bottom:46}` |
  | 打开通知中心 | `{w:32,h:32,x:204,y:14,right:236,bottom:46}` | `{w:32,h:32,x:174,y:14,right:206,bottom:46}` |
  | 切换主题 | `{w:32,h:32,x:252,y:14,right:284,bottom:46}` | `{w:32,h:32,x:222,y:14,right:254,bottom:46}` |
  | user-menu（对照，合规） | `{w:70,h:40,x:300,y:10}` | `{w:70,h:40,x:270,y:10}` |
  基线 `/tmp/uiux-evidence/dom-facts.json` 的 `smallCount` 亦在 `profile` 页把「折叠侧边导航 / 打开通知中心 / 切换主题」三项各计为 `w:32,h:32`。
- **证据截图**：`shell-dark-mobile-390.png`、`shell-dark-mobile-360.png`（三个圆形按钮肉眼可见地小于相邻的头像按钮）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:82-100`（`size="default"`）、`MainLayout.vue:141`（通知 `size="default"`）、`src/components/common/ThemeSwitch.vue:3`（`<el-button :icon="currentIcon" circle aria-label="切换主题" />`，未指定 `size`，取 Element Plus 默认）；`MainLayout.vue:926-942` 的 `@media (max-width:768px)` 只隐藏了搜索/面包屑/用户名/状态文字，**未放大触控区**。
- **规范条款**：§4.4.5 `MUST`「移动端图标按钮和 Switch/Slider 的实际可点击区域不小于 44x44px；仅在极高密度、非主要任务的工具栏中可降到 36px」
- **严重度**：**高**（违反 MUST 条款；页头是全站每页都出现的主工具栏，非「非主要任务的工具栏」，且契约 §7 明确把 360px 与触控列为硬要求）
- **建议**：在 `MainLayout.vue` 的 `@media (max-width: 768px)` 内对 `.main-header .el-button.is-circle` 与 `.header-right .el-button` 设置 `min-width:44px; min-height:44px`（或用 `::after` 扩大热区到 44×44 而不改变视觉尺寸），并给 `ThemeSwitch.vue` 的按钮加 `size="large"`。
- **验收方法**：在 390/360 视口断言三个按钮的 `getBoundingClientRect().width >= 44 && .height >= 44`（若用 `::after` 扩大热区，则断言伪元素盒或 `elementFromPoint` 在 44×44 角落命中该按钮）。

---

### #4 【高】亮色主题 WS 状态徽标文字对比度 2.08:1

- **页面**：全站外壳（`.ws-status`） | **视口**：所有（1440/1024/768/390/360 均相同） | **主题**：**light 不达标 / dark 达标** | **维度**：D2 配色与主题
- **现象**：亮色主题下顶栏「在线/离线」文字 12px，与其自身徽标背景对比度仅 **2.08:1**，低于 WCAG AA 正文 4.5:1，也低于大字 3:1；暗色主题下同一元素为 7.05:1（达标）。即同一状态指示在两种主题下可读性相差 3.4 倍。
- **证据DOM**（`getComputedStyle`，协议 `shell_light_1440` / `shell_dark_1440`）：
  | 主题 | 文字色 | 徽标自身背景 | 计算对比度 | AA 4.5 | AA 3.0 |
  |---|---|---|---|---|---|
  | light | `rgb(103, 194, 58)` | `rgb(240, 249, 235)` | **2.08** | FAIL | FAIL |
  | dark | `rgb(103, 194, 58)` | `rgb(28, 37, 24)` | 7.05 | PASS | PASS |
  离线态同源：`--el-color-danger` `#f56c6c` 配 `--el-color-danger-light-9` `#fef0f0` ≈ **2.61:1**（同属不达标）。
  元素盒：`{w:60,h:29,x:1132,y:15,right:1192,bottom:44}`，`font-size: 12px`。
- **证据截图**：`shell-light-1440.png`（顶栏右侧绿色「在线」胶囊，文字与底色接近）、`shell-dark-1440.png`（同一位置对比清晰）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:670-685`
  ```css
  .ws-status { padding: 6px 12px; background: var(--el-color-danger-light-9);
               border-radius: 20px; font-size: 12px; color: var(--el-color-danger); }
  .ws-status.connected { background: var(--el-color-success-light-9);
                         color: var(--el-color-success); }
  ```
  即直接用 Element Plus 的 `-light-9` 浅底 + 原色文字，未做亮色主题下的对比度校正。
- **规范条款**：§4.5.1 `MUST`「每次触及全局颜色、卡片、表格、弹窗、popover…时，验证亮色与暗色」；§4.2.1 语义色
- **严重度**：**高**（违反 MUST 条款中「亮暗双主题可读」；该元素出现在全部 13 个已登录页面的页头，是用户判断实时通道是否在线的唯一指示，2.08:1 属明显不可读）
- **建议**：亮色主题下把文字色改为更深一档的语义色（如 `--el-color-success-dark-2`）或在 `theme.css` 内定义 `--ws-online-fg` / `--ws-offline-fg` 两套 token 分别给亮暗赋值（亮色取 `#3d8b1f` 量级即可 >4.5:1）；保留 `-light-9` 作为底色。
- **验收方法**：对 `.ws-status` 取 `getComputedStyle(el).color` 与 `backgroundColor`，代入 WCAG 相对亮度公式断言 `ratio >= 4.5`（亮、暗、在线、离线四种组合）。

---

### #5 【中】侧栏渐变硬编码绕过 token，暗色主题下侧栏仍渲染亮色渐变

- **页面**：全站外壳 | **视口**：≥769px（桌面侧栏可见时） | **主题**：light 与 dark **渲染结果完全相同** | **维度**：D2 配色与主题
- **现象**：`theme.css` 为亮/暗各定义了一份 `--sidebar-bg-gradient`（暗色为 `#141414 → #1a1a1a`），但侧栏实际使用的是组件内硬编码的亮色渐变；DOM 实测两种主题下 `backgroundImage` **逐字符相同**，说明暗色 token 是**死代码**。
- **证据DOM**（`getComputedStyle('.sidebar').backgroundImage`）：
  - light → `linear-gradient(rgb(26, 31, 46) 0%, rgb(30, 37, 56) 100%)`
  - dark → `linear-gradient(rgb(26, 31, 46) 0%, rgb(30, 37, 56) 100%)` ← **与亮色完全一致**
  - 同页读取 token：light `--sidebar-bg-gradient: linear-gradient(180deg, #1a1f2e 0%, #1e2538 100%)`；dark `--sidebar-bg-gradient: linear-gradient(180deg, #141414 0%, #1a1a1a 100%)`（token 正确切换，但无人消费）
  - `--sidebar-bg`：light `#1a1f2e` → dark `#141414`（同样未被侧栏背景使用）
- **证据截图**：`sidebar-light-1440.png` 与 `sidebar-dark-1440.png`（侧栏底色在两图中一致；差异只在右侧内容区）
- **证据代码**：
  - 硬编码：`frontend-shared/src/views/layout/MainLayout.vue:490-491`
    ```css
    .sidebar { background: linear-gradient(180deg, #1a1f2e 0%, #1e2538 100%); }
    ```
  - 被绕过的 token：`frontend-shared/src/styles/theme.css:53`（亮）与 `theme.css:151`（暗）
  - 全仓 grep `sidebar-bg-gradient` 仅命中 `theme.css:53` 与 `theme.css:151` 两处定义，**零个消费点**
- **规范条款**：§3.6.1 `MUST`「DOM/CSS 颜色、表面…使用 Element Plus CSS 变量或 `theme.css` 的语义 token」；§3.6.5 `禁止`「在业务 CSS 新增硬编码语义 hex、渐变或阴影以绕开 token」
- **严重度**：**中**（违反 MUST/禁止条款，但侧栏在两种主题下均为深色且文字对比度实测达标——菜单项 6.91:1、激活项 5.90:1——故不影响任务完成，属主题一致性缺陷）
- **建议**：把 `MainLayout.vue:491` 改为 `background: var(--sidebar-bg-gradient);`（或在 `theme.css` 增加 `--sidebar-bg-gradient` 的消费点），删掉组件内 hex。
- **验收方法**：断言亮/暗两次 `getComputedStyle('.sidebar').backgroundImage` **不相等**，且分别等于当次 `--sidebar-bg-gradient` 的计算值。

---

### #6 【中】亮色登录页次要文字与「忘记密码？」链接对比度不达标（2.30–3.08）

- **页面**：`/login` | **视口**：1440（360 同源，颜色一致） | **主题**：**light 不达标 / dark 多数达标** | **维度**：D2 配色与主题
- **现象**：亮色登录卡片内 4 处小字号文字低于 WCAG AA 4.5:1；其中「忘记密码？」是**可点击操作入口**。暗色主题下同位置多数达标（或更高），形成亮暗不对称。
- **证据DOM**（`getComputedStyle`，协议 `login_colors_light` / `login_colors_dark`，底色为卡片 `color(srgb 1 1 1 / 0.97)`）：
  | 元素 | 字号 | light 前景 | light 对比度 | dark 对比度 |
  |---|---|---|---|---|
  | 忘记密码？（`.el-link`） | 14px | `rgb(64, 158, 255)` | **2.78** FAIL | 5.22 PASS |
  | 版本号 `.version` | 12px | `rgb(168, 171, 178)` | **2.30** FAIL | 2.95 FAIL |
  | 品牌描述 `.brand-desc` | 13px | `rgb(144, 147, 153)` | **3.08** FAIL | 4.53 PASS |
  | 输入框 placeholder | 14px | `rgb(168, 171, 178)` | **2.30** FAIL | 3.39（≥3 通过大字线） |
  | 输入框正文（对照） | 14px | `rgb(96, 98, 102)` | 6.11 PASS | 6.84 PASS |
- **证据截图**：`login-colors-light-1440.png`、`login-colors-dark-1440.png`、`login-light-1440.png`（浅色卡片上「忘记密码？」偏淡）
- **证据代码**：
  - `frontend-shared/src/views/auth/Login.vue:374-379`（`.brand-desc { color: var(--el-text-color-secondary); }`）
  - `frontend-shared/src/views/auth/Login.vue:385-388`（`.version { color: var(--el-text-color-placeholder); }`）
  - `frontend-shared/src/components/forms/LoginForm.vue:39`（`<el-link type="primary" …>忘记密码？</el-link>`，取 `--el-color-primary` `#409eff`）
- **规范条款**：§4.5.1 `MUST`（亮暗均可读）；§4.2.1 语义色
- **严重度**：**中**（违反 MUST，但登录页任务（输入凭据→登录）不受阻，属可读性缺陷）
- **建议**：亮色主题下 `.brand-desc`/`.version` 改用更深一档 token（如 `--el-text-color-regular` 6.11:1）；「忘记密码？」改为 `type="primary"` 的深色变体或加下划线（`:underline="true"`）+ 更深前景，使链接在亮色下 ≥4.5:1。
- **验收方法**：同 #4 的对比度断言，逐元素枚举上表四行。

---

### #7 【中】亮色 404 页描述文字对比度 2.87:1

- **页面**：不存在的路由（NotFound） | **视口**：1440、360 均复现 | **主题**：**light 不达标 / dark 达标** | **维度**：D2 配色与主题
- **现象**：404 页描述文案 14px 在亮色主题下对比度 2.87:1；暗色 7.97:1。
- **证据DOM**（协议 `notfound_light_1440` / `notfound_dark_1440`）：
  | 主题 | `.error-desc` color | 页面背景 | 对比度 |
  |---|---|---|---|
  | light | `rgb(144, 147, 153)` | `rgb(245, 247, 250)` | **2.87** FAIL |
  | dark | `rgb(163, 166, 173)` | `rgb(13, 13, 13)` | 7.97 PASS |
  文案原文：`您访问的页面已被移除、重命名或暂时不可用。`
- **证据截图**：`notfound-light-1440.png`、`notfound-light-1440.png`（描述行明显偏淡）
- **证据代码**：`frontend-shared/src/components/common/ErrorPageLayout.vue:79-84`
  ```css
  .error-desc { font-size: 14px; color: var(--text-color-secondary); margin: 0 0 32px; line-height: 1.6; }
  ```
  底色来自同文件 `ErrorPageLayout.vue:44`（`background: var(--bg-color-page)`）。`--text-color-secondary` 在亮色为 `#909399`（`theme.css:38`），却搭在比卡片更暗的 `--bg-color-page` `#f5f7fa` 上——该组合在亮色下天然低于 3:1。
- **规范条款**：§4.5.1 `MUST`；§3.5.2 恢复路径的文案须可读
- **严重度**：**中**（违反 MUST，但不阻断「返回首页/返回上页」的恢复路径）
- **建议**：`.error-desc` 改用 `--text-color-regular`（亮色 `#606266`，对 `#f5f7fa` ≈ 5.0:1），或在 `.error-page` 上改用 `--bg-color` 白色底。
- **验收方法**：对比度断言 `.error-desc` 前景 vs `.error-page` 背景 ≥ 4.5，亮暗各一次。

---

### #8 【中】通知中心 20 个条目可点击但不可聚焦、无 role

- **页面**：全站外壳（通知 popover） | **视口**：1440（数据实测）/ 移动端同组件 | **主题**：light 实测 20 条、dark 同结构 | **维度**：D7 可访问性
- **现象**：通知列表的每个条目都是带 `@click` 的 `div`，`cursor:pointer`，但 `tabIndex=-1`、`role=null`——键盘用户无法选中任何通知，也无法触发其中的跳转逻辑。
- **证据DOM**（协议 `kbd_notification_item`）：
  ```json
  { "count": 20, "anyFocusable": false, "roles": [null] }
  ```
  单条盒：`{"cls":"notification-item.unread","tabIndex":-1,"cursor":"pointer","box":{"w":318,"h":83}}`
- **证据截图**：`notification-popover-light-1440.png`、`notification-popover-dark-1440.png`（条目有 hover 底色，说明设计为可交互）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:154-161`
  ```vue
  <div v-for="item in notifications" :key="item.id"
       class="notification-item" :class="{ unread: !item.read }"
       @click="handleNotificationClick(item)">
  ```
  无 `role`/`tabindex`/`aria-label`；`MainLayout.vue:727-733` 只给了 `cursor: pointer` 与 hover 过渡。
- **规范条款**：§3.1.3 `MUST`「为可点击的非原生容器补齐 `role`、`tabindex`、明确 `aria-label`，并让 Enter/Space 与 click 调用同一行为」
- **严重度**：**中**（违反 MUST；通知是次要但真实的操作入口，键盘用户完全无法到达）
- **建议**：给 `.notification-item` 加 `role="button"`、`tabindex="0"`、`:aria-label="item.title"`，并绑定 `@keydown.enter.prevent`/`@keydown.space.prevent` 到同一个 `handleNotificationClick(item)`（直接照抄同仓 `StatCard.vue:2-10` 的既有参照写法）。
- **验收方法**：断言 20 个条目的 `tabIndex >= 0` 且 `getAttribute('role') === 'button'`；对首条发 Enter 键，断言其跳转副作用与鼠标点击一致。

---

### #9 【中】侧栏 Logo 与移动端 Logo 区域可点击但不可聚焦

- **页面**：全站外壳（桌面包 `.logo-area`、移动包 `.mobile-logo-area`） | **视口**：1440 / 390 | **主题**：light + dark | **维度**：D7 可访问性
- **现象**：两处 Logo 区域均 `@click` 跳转 `/dashboard`，`cursor:pointer`，但不可聚焦、无 role、无 aria-label。
- **证据DOM**：
  - 桌面包 `shell_logo_area`：`{"tag":"div","cls":"logo-area","aria":null,"role":null,"tabindex":null,"focusable":false,"tabIndex":-1,"box":{"w":200,"h":60}}`
  - 指针扫描（`skeleton_pointer_scan`）同样把 `.logo-area`、`.logo-icon`、`img`、`span.logo-text` 四个节点列为 `cursor:pointer, tabIndex:-1, role:null`
  - 键盘实测（协议 `kbd_logo_area`）：`{tabIndex:-1, isActive:false, tag:"DIV", role:null, tabindexAttr:null}`——`.focus()` 调用后 `document.activeElement` **不是**该元素
- **证据截图**：`shell-light-1440.png`（左上 EHomeSystem 品牌区）、`drawer-390-dark-verify.png`（抽屉顶部同款 Logo 区）
- **证据代码**：
  - `frontend-shared/src/views/layout/MainLayout.vue:6`：`<div class="logo-area" @click="router.push('/dashboard')">`
  - `frontend-shared/src/views/layout/MainLayout.vue:48`：`<div class="mobile-logo-area" @click="handleMobileLogoClick">`
  - 样式 `MainLayout.vue:499-512`（`.sidebar .logo-area { cursor: pointer; }`）、`MainLayout.vue:854-867`（移动端同款）
- **规范条款**：§3.1.3 `MUST`
- **严重度**：**中**（违反 MUST；因页头已有汉堡/折叠按钮与面包屑首页图标提供等价导航，故不阻断）
- **建议**：两处 `div` 改为 `<button type="button" class="logo-area" aria-label="返回仪表盘">`（并重置按钮默认样式），或补 `role="link" tabindex="0" aria-label="返回仪表盘"` + Enter/Space 处理。
- **验收方法**：断言 `.logo-area` 的 `tabIndex >= 0` 且 `aria-label` 非空；`.focus()` 后 `document.activeElement === el`；Enter 后 `location.pathname === '/dashboard'`。

---

### #10 【中】面包屑只有单段，详情页的「列表 → 实体」层级缺失，且 `:id` 映射为不可达代码

- **页面**：全站外壳（示例取 `/dashboard`；详情页由代码推导） | **视口**：≥769px（≤768px 面包屑被隐藏） | **主题**：light + dark 同 | **维度**：D1 布局与信息架构
- **现象**：面包屑实际只渲染「🏠 /」或「🏠 / 单个中文名」，不表达层级；规范 §4.1.2 明确要求详情页面包屑「至少表达 列表 → 当前实体」。
- **证据DOM**（协议 `shell_light_1440`）：`crumbText: "/"`、`crumbBox: {w:32,h:22,x:96,y:27}`——即 `/dashboard` 下除首页图标与分隔符外**没有任何**面包屑项。
- **证据截图**：`shell-light-1440.png`、`shell-dark-1440.png`（页头左侧只有 🏠 与一个 `/`）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:341-368`
  ```ts
  const breadcrumbs = computed(() => {
    const pathNames: Record<string, string> = {
      '/dashboard': '仪表盘', '/node': '节点管理', …,
      '/node/:id': '节点详情', '/edge-device/:id': '边缘设备详情',   // :352-353
    }
    const crumbs: string[] = []
    for (const [key, value] of Object.entries(pathNames)) {
      if (path.startsWith(key.replace('/:id', '')) && key !== '/dashboard') { crumbs.push(value); break }
    }
    return crumbs
  })
  ```
  两处结构性缺陷：(a) 只 `push` 一次后 `break`，恒定单段；(b) `Object.entries` 按插入序迭代，`'/node'` 排在 `'/node/:id'` **之前**，故 `/node/123` 会先命中 `'/node'` 并 break，`'/node/:id': '节点详情'` 与 `'/edge-device/:id'` 两条映射**永远不会被使用**（死代码）。
  > 说明：`:id` 死代码一节为源码推导；DOM 实测证据是 `/dashboard` 下 `crumbText="/"` 与单段渲染事实。未进入真实实体详情页采样（见 §5.6）。
- **规范条款**：§4.1.2 `MUST`「面包屑至少表达『列表 -> 当前实体』」
- **严重度**：**中**（违反 MUST；但页面标题与侧栏高亮仍能定位当前位置，不阻断导航）
- **建议**：改为按路由 `matched` 数组生成层级（`route.matched` 天然给出父子链），或显式构造 `[{name:'节点管理',to:'/node'},{name:'节点详情'}]]`；删除不可达的 `:id` 分支。
- **验收方法**：访问 `/node/:id`，断言 `.el-breadcrumb__item` 数量 ≥ 3（首页图标 + 列表 + 实体），且末项 `textContent` 含实体名或「节点详情」。

---

### #11 【中】`document.title` 全站不随路由更新

- **页面**：全部 14 个路由 | **视口**：所有 | **主题**：所有 | **维度**：D7 可访问性与文案
- **现象**：132 条基线记录的 `docTitle` **全部**为同一个字符串；浏览器标签页/历史记录/书签无法区分页面，读屏软件切换页面时没有标题播报。
- **证据DOM**：基线 `/tmp/uiux-evidence/dom-facts.json` 聚合结果——`'EHomeSystem - 家庭数字化系统' → 132 records; pages: [alerts, automation, channel-list, dashboard, data-panel, data-sources, device-configs, edge-device-list, firmware, logical-device-list, login, monitor, node-list, profile]`
- **证据截图**：`shell-light-1440.png` 等浏览器截图不含标签栏，故此项以 DOM 事实为准（截图不适用）
- **证据代码**：
  - `frontend-shared/index.html:18`：`<title>EHomeSystem - 家庭数字化系统</title>`（唯一静态标题）
  - 全仓 grep `document.title` → **0 命中**（`cd frontend-shared && grep -rn 'document.title' src` 无输出）
  - 讽刺的是每个路由都在 `src/router/index.ts` 里声明了 `meta.title`（如 `:44` `meta: { title: '仪表盘', icon: 'Odometer' }`），但没有任何 `afterEach` 消费它——`router/index.ts:167-176` 的 `afterEach` 只调用了 `useRouteProgress().done()`。
- **规范条款**：§4.1.1 `MUST`「使用页面标题或等价的清晰标题」；§5.2.1「正确路由与关键标题存在」
- **严重度**：**中**（违反 MUST；页内 `h1/h2` 标题存在，故不阻断任务）
- **建议**：在 `router/index.ts` 的 `afterEach` 内加一行
  ```ts
  document.title = to.meta.title ? `${to.meta.title} - EHomeSystem` : 'EHomeSystem'
  ```
  （`afterEach` 签名需接收 `to`）。零新增依赖。
- **验收方法**：逐路由断言 `document.title` 互不相同且包含 `route.meta.title`。

---

### #12 【中】移动抽屉未做焦点陷阱，Tab 会穿到抽屉背后的页面

- **页面**：全站外壳（移动端抽屉） | **视口**：mobile-390 | **主题**：dark 实测（light 同结构） | **维度**：D7 可访问性 / D6
- **现象**：抽屉打开后，Tab 键焦点没有被限制在抽屉内，而是直接落到被 `.el-overlay` 遮住的页头按钮与统计卡上——键盘用户会操作到不可见的背景内容。
- **证据DOM**（协议 `drawer_focus_trap`，抽屉打开态连按 6 次 Tab）：
  ```json
  [ {"tag":"BUTTON","cls":"el-button el-button--default is-circle c…","insideDrawer":false},
    {"tag":"BUTTON","cls":"el-button el-button--default is-circle","insideDrawer":false},
    {"tag":"BUTTON","cls":"el-button is-circle el-tooltip__trigger","insideDrawer":false},
    {"tag":"DIV","cls":"user-menu el-tooltip__trigger el-tooltip","insideDrawer":false},
    {"tag":"DIV","cls":"el-card is-hover-shadow stat-card","insideDrawer":false},
    {"tag":"DIV","cls":"el-card is-hover-shadow stat-card","insideDrawer":false} ]
  ```
  `insideDrawer` 六次全为 `false`。抽屉盒本身正常：`{w:240,h:844,x:0,y:0}`，遮罩 `rgba(0,0,0,0.5)`；Escape 可关闭（`drawer_after_escape: {drawerStillInDom:true, drawerVisible:false}`）。
- **证据截图**：`drawer-390-dark-verify.png`（抽屉覆盖左侧 240px，右侧仍可见页头与卡片）
- **证据代码**：`frontend-shared/src/views/layout/MainLayout.vue:38-73`——`<el-drawer v-if="isMobile" v-model="mobileDrawerVisible" direction="ltr" :with-header="false" size="240px" class="mobile-sidebar-drawer">`，未传 `:modal`/焦点管理相关配置；`:global` 样式块（`MainLayout.vue:964-1040`）也只覆盖配色与布局，无 `tabindex`/焦点约束。
- **规范条款**：§3.1.3（可聚焦与行为一致）；§4.5.1 移动抽屉为历史高风险交叉区域
- **严重度**：**中**（违反 §3.1.3 精神；抽屉可用鼠标/Escape 正常操作，不阻断任务）
- **建议**：给 `el-drawer` 开启焦点管理（Element Plus 抽屉默认应限制焦点，可用 `ref` 在 `@opened` 时把 `focus()` 移到抽屉内第一个菜单项），并在 `@closed` 时把焦点还给汉堡按钮。
- **验收方法**：打开抽屉后连按 Tab，断言 `document.activeElement.closest('.el-drawer')` 恒非空。

---

### #13 【中】用户可见文案残留旧称「设备模板」

- **页面**：节点页快速创建设备对话框（由 `/node/:id` 进入） | **视口**：所有 | **主题**：所有 | **维度**：D7 术语合规
- **现象**：对话框提示文案出现术语表明确废弃的旧称「设备模板」，且该字符串**已进入生产构建产物**。
- **证据DOM**：本次探针未渲染该对话框（需先进入节点详情，见 §5.6），故以**生产产物字符串**为机器证据：
  ```
  $ grep -o '.\{80\}设备模板.\{80\}' dist/assets/NodeOverview-BwRwr5OE.js
  (`在节点 `,…,d(t.nodeName||t.nodeId),…,b(` 上直接创建设备，无需预先创建设备模板`…))
  ```
  `grep -l 设备模板 dist/assets/*.js` → 命中 1 个 chunk；`采集器`/`网关` 在 `dist/assets/*.js` 中命中 **0**。
- **证据截图**：不适用（未进入该对话框，见 §5.6）
- **证据代码**：`frontend-shared/src/components/node/QuickCreateDeviceDialog.vue:13`
  ```vue
  <span>在节点 <strong>{{ nodeName || nodeId }}</strong> 上直接创建设备，无需预先创建设备模板</span>
  ```
  （`src/views/node/NodeDetail.vue:400` 也含该词，但位于 HTML 注释内，**对用户不可见，不计为违规**。）
- **规范条款**：§1「术语遵循 术语表.md…禁止在新增中文 UI 中混用『采集器』『设备模板』等旧称」；`docs/设计/术语表.md` §2.3「**易混点**：❌ 不是『设备模板』（旧名）」
- **严重度**：**中**（违反 §1 术语要求；不影响操作，但会把已废弃的旧概念重新暴露给用户）
- **建议**：把该句改为「…直接创建设备，无需预先创建设备配置」（`DeviceConfig` 的规范称法为「设备配置 / 配置模板」，侧栏已用「配置模板」）。同步删除 `NodeDetail.vue:400` 注释中的旧称以免后续被复制。
- **验收方法**：`grep -rn '设备模板' src --include='*.vue' --include='*.ts'` 应仅剩注释或为 0；`grep -c 设备模板 dist/assets/*.js` 应为 0。

---

### #14 【中】`/403` 与 `/profile` 硬编码「系统管理员」角色标签

- **页面**：`/403`、`/profile` | **视口**：1440 / 360 | **主题**：light + dark | **维度**：D3 人机反馈（状态可信）
- **现象**：两页都无条件渲染一个「系统管理员」Tag，但 `UserInfo` 类型里**根本没有角色字段**。在 `/403` 未登录直达时，页面会输出「当前账号 **当前用户**（**系统管理员**）没有访问该页面的权限」——同时展示了占位用户名与一个无从得知的角色。
- **证据DOM**（协议 `forbidden_aria`，360px 暗色直链访问）：
  ```json
  { "tagText": "系统管理员",
    "bodyText": "EHomeSystem 403 无权访问 当前账号 当前用户（ 系统管理员 ）没有访问该页面的权限。 如需访问该功能，请联系系统管理员调整角色权限。 返回首页 切换账号" }
  ```
  同一页面在已登录态（1440）输出 `当前账号 admin（系统管理员）…`（协议 `forbidden_dark_1440`），可见角色字符串与登录态无关，恒为常量。
- **证据截图**：`forbidden-dark-1440.png`、`forbidden-light-1440.png`、`fb360-dark.png`
- **证据代码**：
  - `frontend-shared/src/views/error/Forbidden.vue:10`：`…（<el-tag type="primary" size="default" …>系统管理员</el-tag>）没有访问该页面的权限。`
  - `frontend-shared/src/views/profile/Profile.vue:11`：`<el-tag type="primary" size="default">系统管理员</el-tag>`
  - `frontend-shared/src/stores/user.ts:17-22`：`export interface UserInfo { id: number; username: string; email: string; enabled?: boolean }`——**无 `role` 字段**，故该 Tag 不可能是后端事实
- **规范条款**：§3.2.5 `MUST`「由后端事实驱动状态…不得以本地默认值伪造『正常』『停止』『无占用』」；§3.4.5（未知/未配置优先显示 `—`）
- **严重度**：**中**（违反 MUST，且是**向用户断言了一个未经验证的身份**；不阻断恢复路径）
- **建议**：二选一——(a) 后端 `/auth/account` 返回 `role` 后在 `UserInfo` 增加字段并绑定 `{{ userInfo?.role ?? '—' }}`；(b) 在拿到角色事实之前**移除**该 Tag，只显示用户名。同时把 `Forbidden.vue:31` 的 `|| '当前用户'` 占位与「系统管理员」断言解耦（未登录时不应声称任何角色）。
- **验收方法**：未登录直链 `/403`，断言页面文本**不包含**「系统管理员」；登录后断言 Tag 文本等于 `userInfo.role`。

---

### #15 【中】404「返回上页」在无站内历史时把用户带出应用（`about:blank`）

- **页面**：不存在的路由（NotFound） | **视口**：1440 | **主题**：dark 实测 | **维度**：D3 人机反馈（恢复路径）
- **现象**：在新标签页直接访问一个不存在的路径后点「返回上页」，浏览器跳到了 `about:blank`——用户离开了应用且没有任何内容，比停留在 404 页更差。
- **证据DOM**（协议 `notfound_back_no_history`）：`{ "url": "about:blank" }`
  对照：`/403` 与 404 页的「返回首页/返回上页」按钮盒均正常可点（`{w:113,h:32}` @360，`{w:108,h:32}` @1440）。
- **证据截图**：`notfound-dark-1440.png`、`notfound-light-1440.png`、`nf360-dark.png`
- **证据代码**：`frontend-shared/src/views/error/NotFound.vue:22-28`
  ```ts
  const goBack = () => {
    if (window.history.length > 1) {
      router.back()
    } else {
      router.push('/dashboard')
    }
  }
  ```
  `window.history.length` 在「新标签页首次导航」时也 ≥ 2（初始 `about:blank` 占一位），因此判据失效，`router.back()` 回退了**站外**条目。
- **规范条款**：§3.5.2 `MUST`「路由守卫和 API 401 处理都保留原目标或给出恢复路径，避免用户在会话过期后丢失工作上下文」；§1.2.5「失败可理解、可恢复」
- **严重度**：**中**（违反 MUST 的恢复路径要求；「返回首页」仍可用，故不阻断）
- **建议**：用站内来源判据替代 `history.length`，例如
  ```ts
  const from = window.history.state?.back
  if (typeof from === 'string' && from.startsWith('/')) router.back()
  else router.push('/dashboard')
  ```
- **验收方法**：新开标签页→直链不存在路由→点「返回上页」，断言最终 `location.origin` 仍为本应用且 `pathname === '/dashboard'`。

---

### #16 【低】`App.vue` 重复实现主题副作用，与 `theme.ts` 形成双写入路径

- **页面**：全站外壳 | **视口**：所有 | **主题**：light↔dark 切换 | **维度**：D2 配色与主题
- **现象**：`html.dark` 类被**两处**独立代码写入：`stores/theme.ts` 的 `applyTheme`（`flush:'sync'`）与 `App.vue` 的 `watch`。两者当前结果一致故无可见缺陷，但违反「一处定义，多处复用」且存在后续分歧风险。
- **证据DOM**：切换后状态自洽——`theme_state_after_switch: {localStorageTheme:"dark", themeAttr:"dark", htmlDark:true, bodyClass:"dark-theme", darkThemeEls:1, dataThemeEls:1, dataThemeValues:["HTML=dark"]}`；全仓 `[data-theme]` 元素仅 `<html>` 一个（无第二主题开关、无局部暗色 class）。
- **证据截图**：`shell-light-1440.png` → `shell-dark-1440.png`（切换正常）
- **证据代码**：
  - `frontend-shared/src/stores/theme.ts:12-24`（`applyTheme` 同时写 `data-theme`、`body.className`、`html.dark`）
  - `frontend-shared/src/App.vue:25-31`（`watch(() => themeStore.mode, (mode) => { … classList.add/remove('dark') … }, { immediate: true })`）——与 `theme.ts:18-23` 完全重复
- **规范条款**：§3.6.3 `MUST`「保持一套主题状态…不得新增第二个主题开关或局部暗色 class」；§1.2.2「一处定义，多处复用」
- **严重度**：**低**（当前无可见缺陷，属维护性风险）
- **建议**：删除 `App.vue:25-31` 的 `watch`（`theme.ts` 的 `flush:'sync'` 已覆盖首帧与切换）；`App.vue` 仅保留 `useThemeStore()` 的实例化以触发 store 初始化。
- **验收方法**：删后重跑 `theme.spec.ts` 的 8 条断言（`document.documentElement.getAttribute('data-theme')`、`classList.contains('dark')`）应全绿；浏览器切换主题后断言 `html.dark` 与 `data-theme` 同步。

---

### #17 【低】登录页品牌背景渐变硬编码 hex

- **页面**：`/login` | **视口**：所有 | **主题**：light + dark 均为同一渐变 | **维度**：D2 配色与主题
- **现象**：登录页两处背景使用硬编码的三段渐变，未走 token；因设计上登录页在所有主题下都保持深色品牌底，无重主题差异，属规范禁止项但无可用性影响。
- **证据DOM**：`.login-container` 与 `.login-transition` 的 `background-image` 均为 `linear-gradient(135deg, rgb(26,31,46) 0%, rgb(45,53,72) 50%, rgb(26,31,46) 100%)`（两主题一致）。
- **证据截图**：`login-light-1440.png`、`login-dark-1440.png`
- **证据代码**：`frontend-shared/src/views/auth/Login.vue:275` 与 `Login.vue:400`
  ```css
  background: linear-gradient(135deg, #1a1f2e 0%, #2d3548 50%, #1a1f2e 100%);
  ```
- **规范条款**：§3.6.5 `禁止`「在业务 CSS 新增硬编码语义 hex、渐变或阴影以绕开 token」
- **严重度**：**低**（纯打磨项，不影响任务完成）
- **建议**：在 `theme.css` 增加 `--login-bg-gradient`（亮暗可相同）并在两处引用，使未来换肤只需改 token。
- **验收方法**：`grep -n '#1a1f2e\|#2d3548' src/views/auth/Login.vue` 应为 0，且登录页截图与改前逐像素一致。

---

### #18 【低】版本号等辅助文字对比度不足（2.30–2.70）

- **页面**：`/login`、全站侧栏、移动抽屉 | **视口**：1440 / 390 | **主题**：light 与 dark 均有 | **维度**：D2 配色与主题
- **现象**：`v2.0.0` 之类的版本号在 3 处均低于 AA 4.5:1（12px 小字），最低 2.30:1。
- **证据DOM**（`getComputedStyle` + 合成后计算）：
  | 位置 | 前景 | 背景 | 对比度 |
  |---|---|---|---|
  | 登录页 `.version`（light） | `rgb(168,171,178)` | 卡片白 | **2.30** |
  | 侧栏 `.version-info`（light 主题下的侧栏底） | `rgba(255,255,255,0.3)` → 合成 `rgb(95,98,109)` | `#1a1f2e` | **2.70** |
  | 移动抽屉 `.mobile-version-info`（dark） | `rgb(108,112,128)` | `#1a1a1a` | 3.54 |
  | 移动抽屉 `.mobile-version-info`（light） | `rgb(168,171,178)` | `#ffffff` | **2.30** |
  侧栏版本元素实测：`versionInfo: {bgImage:"none", bgColor:"rgba(0,0,0,0)", color:"rgba(255,255,255,0.3)"}`。
- **证据截图**：`sidebar-light-1440.png`（左下角 v2.0.0 极淡）、`drawer-390-dark-verify.png`（底部 v2.0.0）
- **证据代码**：
  - `frontend-shared/src/views/layout/MainLayout.vue:577-581`：`.sidebar .version-info { font-size: 12px; color: rgba(255, 255, 255, 0.3); }`
  - `frontend-shared/src/views/layout/MainLayout.vue:919-923`（抽屉）：`color: var(--el-text-color-placeholder)`
  - `frontend-shared/src/views/auth/Login.vue:385-388`：`color: var(--el-text-color-placeholder)`
- **规范条款**：§4.5.1 `MUST`（亮暗可读）；§4.2.4 文字层级
- **严重度**：**低**（版本号为非关键辅助信息，不影响任务）
- **建议**：把三处统一到 `--text-color-secondary` 一档（亮色 `#909399` 在卡片白上 3.0:1，仍不足则用 `--text-color-regular`），侧栏把 `0.3` 提到 `0.45` 以上；或明确接受版本号为装饰性文本不再纳入对比度门禁（需在设计评审记录例外）。
- **验收方法**：同上对比度断言；或建立「非必要装饰文本」豁免清单并在规范 §4.5.1 补充例外条款。

---

### #19 【低】`/403` 在应用内没有任何触发入口

- **页面**：`/403` | **视口**：所有 | **主题**：所有 | **维度**：D3 人机反馈（权限态）
- **现象**：403 页实现完整（含两条恢复路径），但全仓找不到任何跳转到它的代码；只有直链输入 URL 才能到达，说明「权限不足」这一状态在真实流程中从未被呈现。
- **证据DOM**：`forbidden_dark_1440` 与 `fb360` 均显示页面自洽（`code:"403"`、`title:"无权访问"`、两个按钮），`hasSidebar:false / hasHeader:false`（独立于主布局）。
- **证据截图**：`forbidden-dark-1440.png`、`forbidden-light-1440.png`、`fb360-dark.png`
- **证据代码**：
  - 路由注册：`frontend-shared/src/router/index.ts:132-137`（`path: '/403'`，`meta: { requiresAuth: false }`）
  - 全仓 grep `/403` 的结果只有上述注册与 `src/router/__tests__/guards.spec.ts:35,188-191` 的测试断言——**没有 `router.push('/403')`、没有 403 响应码拦截**。
  - 对照：`src/api/client.ts:76-84` 只处理 401（`clearSessionCaches()` + 跳 `loginPath()`），无 403 分支。
- **规范条款**：§3.5.2 `MUST`「给出恢复路径」；§4.3「权限不足…必须各自可辨」；§3.4.2 六态可辨
- **严重度**：**低**（页面本身可用且有恢复路径；当前单用户模式下权限拒绝场景尚未出现，属预留能力未接线）
- **建议**：在 `api/client.ts` 的响应拦截器为 `error.response?.status === 403` 增加一次 `router.push('/403')`（或由 `EmptyState permission` 语义就地呈现），使该页真正成为权限态的呈现出口。
- **验收方法**：对任一受保护接口返回 403，断言 `location.pathname === '/403'` 且页面出现两条恢复按钮。

---

## 2. 本域问题总数按严重度分布

| 严重度 | 数量 | 编号 |
|---|---|---|
| 阻断 | **0** | — |
| 高 | **4** | #1、#2、#3、#4 |
| 中 | **11** | #5、#6、#7、#8、#9、#10、#11、#12、#13、#14、#15 |
| 低 | **4** | #16、#17、#18、#19 |
| **合计** | **19** | |

## 3. 按规范条款分布（每问题计其主条款）

| 规范条款 | 数量 | 编号 |
|---|---|---|
| §3.1.3（可点击非原生容器 role/tabindex/aria-label，Enter/Space 同 click） | 4 | #2、#8、#9、#12 |
| §4.5.1（亮暗双主题可读） | 4 | #4、#6、#7、#18 |
| §3.6.1（颜色/表面使用语义 token） | 2 | #1、#5 |
| §3.5.2（保留原目标或恢复路径） | 2 | #15、#19 |
| §4.4.5（移动端触控 ≥44×44） | 1 | #3 |
| §4.1.2（面包屑表达 列表→实体） | 1 | #10 |
| §4.1.1（页面标题） | 1 | #11 |
| §1（术语合规，禁旧称） | 1 | #13 |
| §3.2.5（后端事实驱动状态） | 1 | #14 |
| §3.6.3（一套主题状态） | 1 | #16 |
| §3.6.5（禁止硬编码语义 hex/渐变） | 1 | #17 |
| **合计** | **19** | |

## 4. 复核结论：规范 §6 历史偏差

| §6 条目 | 结论 | 依据 |
|---|---|---|
| P0 WS 完整 URL 二次追加 | **仍在（当前配置未触发）** | 见 #1；确定性复刻 5 组输入，2 组产出重复路径 |
| P0 `NetworkBanner` 读未导出 `lastError` | **已闭环** | `NetworkBanner.vue:26-32` 已改为 `if (!wsStore.connected) return WarningFilled`；全仓 grep `lastError` 仅剩注释（`NetworkBanner.vue:28`）与回归测试（`NetworkBanner.spec.ts:7,44`），无生产读取 |
| P2 硬编码色仍较多 | **仍在** | 见 #5（`MainLayout.vue:491`）、#17（`Login.vue:275,400`） |
| P1 页面标题模式未全站覆盖 | **仍在（本域侧）** | 见 #11——不仅覆盖不全，`document.title` 为 0 命中 |

## 5. 我未能验证的部分

1. **`NetworkBanner` 的 WS-only 断线分支（「与服务器的连接已断开」）未能端到端复现。** 我用 CDP `Network.setBlockedURLs(['*/api/v1/ws*','wss://*','ws://*'])` 阻断后重载 `/dashboard`，1.5s 与 13s 两次采样均为 `{navOnline:true, wsText:"在线", wsCls:"ws-status connected", bannerCount:0}`——拦截未生效或 WS 已重连，故该分支的**可见呈现**无运行时证据。可确认的只有：浏览器离线分支**已闭环**（`banner_offline: {bannerCount:1, bannerClass:"network-banner error", bannerText:"网络已断开请检查您的网络连接", bannerBox:{w:1440,h:40,y:0}}`，恢复在线后归 0），以及代码级已移除 `lastError` 断头依赖。
2. **`InitializeAdminForm.vue` 未在真实 `uninitialized` 状态下渲染。** 审计库 `ehome_uiux` 已初始化（登录接口返回 200），`Login.vue:129-137` 只在 `authState === 'uninitialized'` 时挂载该表单，因此我没有它的 DOM 度量与截图，只有源码审读。该文件中 `el-form label-position="top"`、`size="large"`、错误文案与 `LoginForm.vue` 同构，未发现专有缺陷。
3. **主题下拉（`ThemeSwitch.vue`）在暗色主题下的 popover 未单独度量。** 我只有亮色实测（`theme_dropdown_light: {popoverCol:{bg:"rgb(255,255,255)"}, popoverClass:"el-popper is-pure is-light el-tooltip el-dropdown__popper"}`）与通知 popover 的暗色实测（`notification_popover_dark: bg rgb(41,41,43)`）。两者同为 `el-popper`，但未逐一取证，故不宣称「全部 popover 暗色已验」。
4. **登录锁定 300 秒到期自动解锁未等待到点。** 已验证：第 5 轮 `loginBtnDisabled: true`、两个 `el-alert`（warning「登录已锁定，请 298 秒后重试」+ error「连续 5 次登录失败，已锁定 5 分钟」）、`inputsDisabled: [true,true]`、`localStorage.login_lockout` 已写入、锁定态按 Enter 不提交（`stillLogin: true`）。未验证：计时器归零后警告是否消失、按钮是否恢复可用。
5. **`/403` 无法在真实权限拒绝场景下验证。** 全仓无跳转 `/403` 的代码（见 #19），只能直链访问；因此「权限不足时页面是否携带正确上下文」无法验证。
6. **未进入真实实体详情页采样。** 因此 #10（面包屑层级）中「`:id` 映射为死代码」与 #13（「设备模板」文案）属**源码/构建产物推导**，不是 DOM 实测；DOM 实测部分（`/dashboard` 下 `crumbText="/"`、构建产物含该字符串）已分别标注。
7. **契约 §5 的 D5（业务完整交互流程）不在本域要求内**，我未走创建/编辑/删除流程；本域只覆盖骨架、登录、个人设置、错误页。
8. **`html { overflow: hidden }` 的存在使「水平溢出」在浏览器中表现为裁切而非滚动。** （`App.vue:42-46`；实测 `htmlOverflowX: "hidden"`。）我仍以 `document.documentElement.scrollWidth - clientWidth` 为判据（该差值不受 `overflow` 影响），并额外核对了 `.main-content` 的 `overflow-y: auto` 与逐元素 `getBoundingClientRect().right`。全部实测组合 `deOverflow` 均为 0，未见掩蔽。此项作为方法学说明记录，不作为缺陷。

## 6. 我原本的判断被证据推翻的部分

1. **初判：`profile` 页桌面输入框 14px 违反 §4.4.4（输入控件字体 ≥16px）。**
   **被推翻。** §4.4.4 的适用范围是 `<=768px`。实测 `profile_dark_1440.inputFontSizes = ["14px","14px","14px"]`（桌面，合规），而 `mobile_390_profile.inputFontSizes = ["16px","16px","16px"]`、`profile_360_dark.inputFontSizes = ["16px","16px","16px"]`（窄屏，达标）。`App.vue:62-68` 的 `@media (max-width:768px)` 覆盖确实生效。**本条不成立，未计入问题清单。**

2. **初判：基线探针 `clickableNotFocusable` 全站为 0，说明骨架的可访问性没有缺口。**
   **被推翻（探针假阴性）。** 基线探针（`tools/uiux-audit.mjs:172-173`）只扫描 `[role="button"], .el-button, button` 三类选择器，**完全不覆盖带 `@click` 的 `div`/`li`**。我改用「`getComputedStyle(el).cursor === 'pointer'`」做定向扫描（`skeleton_pointer_scan`），在骨架内识别出 25 个唯一指针元素，其中非原生且不可聚焦的有：`.logo-area`（及其 3 个子节点）、12 个 `.el-menu-item`、20 个 `.notification-item`。契约 §2.2 记录的是「探针会误报（假阳性）」，这里发现的是它的镜像——**探针也会漏报（假阴性）**，凡以「某计数为 0」下结论都必须先核对选择器覆盖范围。

3. **初判：移动抽屉在暗色主题下菜单文字偏灰、疑似不可读（来自 `drawer-390-dark-verify.png` 的肉眼观感）。**
   **被推翻。** DOM 实测暗色抽屉菜单项为 `rgba(255,255,255,0.55)`，在 `#1a1a1a` 上合成后为 `rgb(152,152,152)`，对比度 **6.03:1**，达标；`drawer_light` 对应值为 `rgb(96,98,102)` / `#ffffff` = **6.11:1**，同样达标；暗色激活项 `rgb(83,153,255)` / 20% primary 底 = 4.46:1。§4.5.1 点名的「移动抽屉历史高风险交叉区域」亮暗**均已闭环**。（唯一不达标的是抽屉底部 12px 版本号 2.30–3.54，已单列为 #18。）

4. **初判：暗色主题下侧栏会使用 `--sidebar-bg-gradient` 的暗色值（`#141414 → #1a1a1a`），所以侧栏主题是正确的。**
   **被推翻。** token 确实随主题切换（实测 dark 下 `--sidebar-bg-gradient: linear-gradient(180deg, #141414 0%, #1a1a1a 100%)`），但 **`.sidebar` 的 `backgroundImage` 在两种主题下逐字符相同**（都是 `linear-gradient(rgb(26,31,46) 0%, rgb(30,37,56) 100%)`）——token 是无人消费的死代码。缺陷不是「侧栏配色错误」，而是「token 被绕过」（#5）。

5. **初判：登录页 360px 下品牌描述 `.brand-desc` 会逐字竖排（§4.2.5 中文防竖排）。**
   **被推翻。** 实测 `login_360_dark.brandDescBox = {w:228, h:19}`——228×19 是单行文本；`brandBox = {w:296,h:52}`，`deOverflow = 0`。无逐字竖排、无横向溢出。

6. **初判：`html/body { overflow: hidden }` 会让规范 §5.2.2 的 `scrollWidth <= clientWidth` 检查变成恒真，从而使全站「无横向溢出」的结论不可信。**
   **部分推翻。** `overflow: hidden` 只影响滚动可达性，`scrollWidth` 仍如实报告内容宽度。全部实测组合（`/login`、`/dashboard`、`/profile`、404、403 × 双主题 × 1440/390/360）`deOverflow` **均为 0**，且逐元素 `getBoundingClientRect().right` 均 `<= clientWidth`。该判据在本域有效，但已在 §5.8 记录其局限。

7. **初判：`/dashboard` 的面包屑至少会显示「仪表盘」一项。**
   **被推翻。** 实测 `crumbText: "/"`、`crumbBox: {w:32,h:22}`——`breadcrumbs` 计算属性显式排除了 `'/dashboard'`（`MainLayout.vue:361` 的 `&& key !== '/dashboard'`），故仪表盘下只剩首页图标与分隔符。这反而强化了 #10。

---

## 7. 证据文件清单

**截图（`/tmp/uiux-d4/`，共 37 张，均已 `ls` 校验存在）**
`login-light-1440.png`、`login-dark-1440.png`、`login-colors-light-1440.png`、`login-colors-dark-1440.png`、`login-dialog-light-1440.png`、`login-dialog-dark-1440.png`、`login-locked-light-1440.png`、`login-dark-mobile-360b.png`、`shell-light-1440.png`、`shell-dark-1440.png`、`shell-collapsed-light-1440.png`、`shell-dark-mobile-390.png`、`shell-dark-mobile-360.png`、`sidebar-light-1440.png`、`sidebar-dark-1440.png`、`drawer-dark-mobile-390.png`、`drawer-dark-mobile-360.png`、`drawer-390-dark-verify.png`、`drawer-390-dark-lightcheck.png`、`drawer-390-light-lightcheck.png`、`theme-dropdown-light-1440.png`、`notification-popover-light-1440.png`、`notification-popover-dark-1440.png`、`network-banner-offline-dark-1440.png`、`network-banner-ws-down-dark-1440.png`、`profile-light-1440.png`、`profile-dark-1440.png`、`profile-dark-colors-1440.png`、`profile-dark-mobile-390.png`、`profile-dark-mobile-360.png`、`profile-dark-mobile-360b.png`、`notfound-light-1440.png`、`notfound-dark-1440.png`、`forbidden-light-1440.png`、`forbidden-dark-1440.png`、`nf360-dark.png`、`fb360-dark.png`

**DOM 事实 JSON**
- `/tmp/uiux-d4/d4-facts.json`（骨架/登录/主题一致性/抽屉，29 个度量键）
- `/tmp/uiux-d4/d4-facts2.json`（锁定序列/离线横幅/键盘走查，22 键）
- `/tmp/uiux-d4/d4-facts3.json`（侧栏 token/404-403 小屏/抽屉焦点，8 键）
- `/tmp/uiux-d4/d4-facts4.json`（WS 阻断/aria 语义/恢复路径，7 键）
- `/tmp/uiux-d4/d4-facts5.json`（亮暗抽屉配色，2 键）
- 基线复用：`/tmp/uiux-evidence/dom-facts.json`（132 条，未重复跑全量）

**未修改 `frontend-shared/src/` 任何文件**；新增脚本仅位于 `frontend-shared/tools/`。
