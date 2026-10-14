# 交互设计稿：日出日落 BMS 自动化 + 多动作 + 风险二级确认

> **状态**：设计稿（提案）· **写作者**：designer（task-1）· **日期**：2026-10-14
> **需求与验收口径唯一事实源**：[任务书-日出日落与多动作改造-2026-10-14.md](../../docs/分析/任务书-日出日落与多动作改造-2026-10-14.md)
> **本稿不实现任何生产代码**；\`backend/\`、\`frontend-shared/\`、\`esp32-collector/\`、\`scripts/\`、\`Makefile\`、CI 全程只读。
> **本稿含未决项 O-1（风险动作触发时是否自动下发），不自行批准**，见 [§9](#9-待裁决项o-x)。

---

## 1. 交付物

| 文件 | 说明 |
|------|------|
| \`README.md\` | 本文件：信息架构、组件树、状态清单、逐区对照表、设计决策、窄屏与无障碍方案、截图复现命令、待裁决项 |
| \`demo/index.html\` | **单文件自包含**交互 demo（内联 CSS/JS，零外网/零构建/零 element-plus 依赖），双击即可离线打开 |
| \`demo/shots/*.png\` | **chromium headless 实拍**截图，共 **10 张**（6 个必拍状态 + 2 个 O-1 文案变体 + 2 个窄屏 390px） |
| \`demo/capture.mjs\` | 截图 + 断言脚本（复用系统 chrome），**不是生产代码**，仅用于本设计稿自证与复现 |

**自证结果**：\`node demo/capture.mjs\` → \`EXIT=0\`，**21/21 断言通过**，10 张 PNG 全部重拍。断言明细见 [§10](#10-自证记录可复现)。

---

## 2. 信息架构

本设计稿只改**一个入口**（自动化策略页）并**复用一个既有入口**（设备页受控操作），不新增页面、不新增一级导航。

\`\`\`
自动化策略页（既有，AutomationRules.vue）
├── PageHeader「自动化策略 / 传感器数据或时间窗口触发设备动作或通知」
│      （AutomationRules.vue:3；本稿把副标题扩为"…或日出日落触发"，见 §5 对照表 R0）
├── 规则卡片（card / card-head / card-title + 创建规则）
│   └── 规则表：名称 | 触发器 | 动作 | 冷却/熔断 | 确认制 | 启用 | 操作
└── 创建/编辑规则对话框（el-dialog 640px）
    ├── 名称
    ├── 触发器类型（下拉）
    │   ├── 传感器阈值        （既有，不动）
    │   ├── 时间窗口          （既有，不动）
    │   ├── ★ 日出            （新增）
    │   ├── ★ 日落            （新增）—— 二者共用「日出/日落 + 偏移分钟」
    │   └── 事件驱动（暂不支持创建，disabled）  （既有，不动）
    ├── 附加条件（JSON 数组）
    ├── ★ 动作列表（多动作）   ← 由「单动作字段组」升级
    │   ├── 动作卡 #1：目标设备 / 动作 / 参数摘要 / 风险徽标 / 风险原因 / 上移·下移·编辑参数·删除
    │   ├── 动作卡 #2 …
    │   └── + 添加动作
    ├── 冷却时间 / 每日上限 / 启用
    └── 取消 / 保存
        └── ★ 保存前确认弹窗（仅在存在风险动作时出现）
            ├── 风险动作清单（动作名 + 目标设备 + 动作 ID + 参数 + 风险原因）
            ├── 逐项勾选确认（全勾满才解禁「保存规则」）
            └── ★ O-1 待裁决区块（触发时处理方式，本稿不预设）

设备详情页（既有，BmsDetailPage.vue）→ 受控操作（DeviceControlPanel.vue）
└── ★ 风险操作点击 → 参数表单 → 「确认受控操作」二次确认（沿用既有，仅文案收敛为二级）
\`\`\`

★ = 本设计稿新增或改造的部分。

---

## 3. 组件树

### 3.1 设计稿的目标组件树（建议落地形态）

\`\`\`
AutomationRules.vue
├── PageHeader                       （既有，不动）
├── 规则表格                          （既有；「动作」列由单动作摘要改为多动作摘要 + 风险计数）
└── RuleEditorDialog  ← 对应现有 el-dialog（AutomationRules.vue:179-302）
    ├── RuleNameField                （既有 el-input）
    ├── TriggerTypeSelect            （既有 el-select，+2 个 option）
    ├── SunEventFields        ★新增  （日出/日落 + 偏移分钟；文案实时回显"日落前 30 分钟"）
    ├── ConditionsTextarea           （既有 el-input textarea）
    ├── ActionList            ★新增  （替代原 device_action 字段组，AutomationRules.vue:251-273）
    │   ├── ActionToolbar     ★新增  （计数 + 风险计数 + 「+ 添加动作」）
    │   ├── ActionCard[]      ★新增
    │   │   ├── ActionTargetSelect   （目标设备，复用既有 el-select + devices）
    │   │   ├── ActionIdSelect       （动作 ID，复用既有 action_id 语义）
    │   │   ├── RiskBadge      ★新增 （图标 + 文字「风险」，不只用颜色）
    │   │   ├── RiskReason     ★新增 （风险原因文本块）
    │   │   └── ActionCardOps  ★新增 （上移 / 下移 / 编辑参数 / 删除）
    │   └── ActionParamsDialog ★新增 （沿用既有"参数 JSON 文本域"形态，AutomationRules.vue:261-268）
    ├── CooldownField / MaxDailyField / EnabledSwitch   （既有）
    └── SaveRiskConfirmDialog ★新增 （仅当规则含风险动作时弹出）
        ├── RiskSummaryAlert     （"本规则包含 N 个风险动作"）
        ├── HandleModeBlock ★    （O-1 待裁决：选项 A 自动下发 / 选项 B 转待确认）
        ├── RiskItemList[]       （动作名 + 目标设备 + 动作 ID + 参数 + 风险原因 + 逐项勾选）
        └── AckProgress + 取消 + 保存规则（未全勾满则 disabled）

DeviceControlPanel.vue（既有，不动）
├── ActionForm.vue                （既有：参数表单）
└── ActionConfirmationDialog.vue  （既有：二次确认；文案「风险等级：{{risk}}」收敛为二级）
\`\`\`

### 3.2 demo 内的等价实现

\`demo/index.html\` 用同一棵树，只是把每个 Element Plus 组件换成手工复刻的静态节点（无框架）：

| 目标组件 | demo 内的 DOM 等价物 | data-test 钩子 |
|---|---|---|
| \`el-dialog\` | \`.el-overlay > .el-dialog[role=dialog][aria-modal]\` | \`[data-dialog="rule"\|"save-risk"\|"edit-params"\|"manual-form"\|"manual-confirm"]\` |
| \`el-select\` | \`.el-select__wrapper[role=combobox] + ul[role=listbox] > li[role=option]\` | \`[data-select="trigger-type"\|"sun-kind"\|"card-device-N"\|"card-action-N"]\` |
| \`el-input / el-textarea\` | \`.el-input / .el-textarea\` | \`[data-test="field-name"\|"field-sun-offset"\|"manual-reason"\|"params-json"]\` |
| \`el-switch\` | \`.el-switch[role=switch][aria-checked]\` | — |
| \`el-tag\` | \`.el-tag.el-tag--{danger,warning,info,primary}\` | \`[data-test="risk-tag-risk"\|"risk-tag-normal"]\` |
| \`el-button\` | \`.el-button[.el-button--primary\|--danger\|.is-link]\` | \`[data-test="save-rule"\|"add-action"\|"confirm-save"\|"manual-confirm"]\` |
| \`el-alert\` | \`.el-alert.el-alert--{error,warning,info}\` | — |
| \`el-table\` | \`.el-table\`（真实 table） | \`[data-test="rules-table"]\` |
| \`ElMessage\` | \`#toast-root > .el-message[role=status]\` | — |

---

## 4. 状态清单

### 4.1 规则编辑器状态

| # | 状态 | 触发 | 界面表现 | 对应截图 |
|---|------|------|----------|----------|
| S1 | 触发器 = 日出/日落 | 触发器类型选 \`sunrise\`/\`sunset\` | 出现「日出 / 日落」下拉 + 「偏移分钟」数字框；右侧实时文案"日落前 30 分钟" | 01 |
| S2 | 多动作列表（N≥1） | 打开编辑器 | 动作卡列表 + 计数 + 「+ 添加动作」；每卡显示 目标设备/动作/参数 | 02 |
| S3 | 含风险动作 | 任一卡的动作 risk ∈ {high, critical} | 卡片红边 + ⚠ 图标 + 「风险」文字徽标 + 独立「风险原因」块 | 03 |
| S4 | 保存且含风险动作 | 点「保存」 | 弹出「保存前确认」，列出全部风险动作，逐项勾选；未勾满时「保存规则」disabled | 04 / 04b / 04c |
| S5 | 保存且无风险动作 | 点「保存」 | **不弹窗**，直接保存 + 成功提示 | 06 |
| S6 | 普通动作卡 | risk ∈ {low, medium} | 灰色「● 普通」徽标，无红边、无风险原因块 | 06 |
| S7 | 动作参数编辑 | 点卡片「编辑参数」 | 叠一层 JSON 文本域弹窗（沿用既有形态） | — |
| S8 | 动作增删/排序边界 | 第 1 张「上移」/ 最后一张「下移」 | 按钮 \`disabled\`；删除后重排序号 | — |

### 4.2 设备控制面板状态

| # | 状态 | 触发 | 界面表现 | 对应截图 |
|---|------|------|----------|----------|
| D1 | 风险操作点击 | 点「设置充放电 MOS 软件策略」 | 先出参数表单（charge/discharge/priority） | — |
| D2 | 风险操作二次确认 | 参数表单点「继续」 | 「确认受控操作」：风险等级 + 风险原因 + 目标设备/动作/参数回显 + **必填**操作理由 | 05 |
| D3 | 普通操作点击 | 点「读取基本信息」 | **无二次确认**，直接提示"操作已排队" | — |

### 4.3 状态 × 风险处理矩阵（本稿的核心规则）

| 场景 | 风险动作 | 普通动作 |
|------|----------|----------|
| 规则编辑器 · 动作卡 | 红边 + ⚠「风险」+ 风险原因块 | 灰「● 普通」，无风险元素 |
| 规则编辑器 · 保存 | **弹窗** + 逐项勾选（全勾满才可保存） | 无弹窗，直接保存 |
| 设备页 · 手动执行 | **二次确认** + 必填操作理由 | 直接排队 |
| 列表 · 动作列 | 「含 N 个风险动作」红徽标 | 仅显示动作摘要 |
| 列表 · 确认制列 | 「需确认」橙徽标 | \`-\` |

---

## 5. 与既有页面的逐区对照表

对照基准：[AutomationRules.vue](../../frontend-shared/src/views/automation/AutomationRules.vue)（905 行）。列「现状锚点」为 file:line。

### 5.1 逐区对照

| ID | 区域 | 现状（锚点） | 设计稿 | 变化 | 理由 |
|----|------|--------------|--------|------|------|
| R0 | 页头副标题 | 「传感器数据或时间窗口触发设备动作或通知」（:3） | 「传感器数据、时间窗口或日出日落触发设备动作或通知」 | 文案扩写 | 触发器新增两类，副标题必须同步，否则页头与表单不一致 |
| R1 | 规则表·触发器列 | \`el-tag\` + \`triggerSummary\`（:16-23、:784-794） | 触发器标签复用；日出/日落摘要 = \`日落前 30 分钟\` | 新增分支 | 与 S1 文案同源，避免两套措辞 |
| R2 | 规则表·动作列 | 单个 \`el-tag\` + \`action_id\` 摘要（:24-33、:795-800） | \`设备动作 ×N\` + 可选「含 N 个风险动作」红徽标 + 全部 action_id | 扩展 | 多动作后单值摘要无法表达；风险计数让用户在列表页就能发现风险规则 |
| R3 | 规则表·确认制列 | \`require_confirmed\` → 「需确认」（:39-44） | 不变 | 不动 | 该列语义是"规则级确认开关"，与动作级风险是两个维度，本稿不改其含义 |
| R4 | 触发器类型下拉 | 3 项：传感器阈值 / 时间窗口 / 事件驱动(disabled)（:186-191） | **+2 项**：日出、日落 | 新增 | 需求 §1.1；顺序把新项放在「时间窗口」之后、「事件驱动」之前 |
| R5 | time_window 字段组 | 开始时间 / 结束时间 / 触发沿（:218-232） | 保持不变 | 不动 | 静态窗口仍有价值；日出日落是**并列的新选项**而非替换 |
| R6 | ★ 日出/日落字段组 | **无** | 日出/日落下拉 + 偏移分钟（负值=之前）+ 实时文案回显 | 新增 | 需求 §1.1「日落前 30 分钟」；把"提前量"做成一等字段，避免用户手算 HH:MM |
| R7 | 动作区 | **单动作**：目标设备 + 动作 ID + 参数 JSON 文本域 + 需人工确认（:251-273） | **动作卡列表**（增删/排序/每卡三元组摘要） | 结构性替换 | 需求 §1.2；沿用既有字段语义（目标设备 = \`action_device_id\`，动作 = \`action_id\`，参数 = \`action_params_json\`） |
| R8 | 动作参数编辑 | 内联 textarea（:261-268） | 卡片「编辑参数」→ 叠层 JSON 文本域 | 形态保留 | 参数 schema 是后端 \`input_schema\` 驱动的；本稿不改参数录入形态，只改**承载位置** |
| R9 | 「需人工确认」开关 | \`require_confirmed\` switch（:270-272） | **保留**（规则级），并新增动作级风险徽标 | 并存 | 二者语义不同：\`require_confirmed\` 是"规则触发后是否要人确认"，风险徽标是"这个动作本身危险"。本稿不合并它们（见 §9 O-3） |
| R10 | 通知动作字段组 | \`action_level\`（:275-284） | 保持；通知类动作恒为「普通」，不参与风险清单 | 不变 | 通知无设备副作用 |
| R11 | 对话框 footer | 取消 / 保存（:298-301） | 取消 / 保存 → 保存可能先弹「保存前确认」 | 新增拦截 | 需求 §1.3 |
| R12 | 触发历史表 | 结果列 9 态 + 「确认执行」按钮（:98-159、:673-689） | 不变 | 不动 | 本稿不触碰事件/确认链路（属架构线） |
| R13 | 手动触发按钮 | \`feedback.confirm\` 普通确认（:694-722） | 不变 | 不动 | 手动触发是**非破坏性**操作，既有结构性裁决（\`listPageConventions.spec.ts\`）要求它不得改走 \`confirmDanger\` |
| R14 | 删除规则 | \`feedback.confirmDanger\`（:655-671） | 不变 | 不动 | 已符合破坏性确认契约 |

### 5.2 既有组件 → 新组件对照（风险 UI 的延续关系）

| 既有组件/位置 | 既有行为（锚点） | 新组件 | 关系 |
|---|---|---|---|
| \`DeviceControlPanel.vue\` 风险徽标 | \`critical\`→「严重风险」danger、\`high\`→「高风险」warning（:40-41） | \`RiskBadge\` | **收敛**：四级 → 二级，high+critical 合并为「⚠ 风险」，措辞统一 |
| \`DeviceControlPanel.vue\` 确认判定 | \`requiresConfirmation = medium\\|high\\|critical\`（:148） | 同 | **保留不动**（见 §9 O-3） |
| \`ActionConfirmationDialog.vue\` | 「确认受控操作」+「风险等级：{{risk}}」+ 必填操作理由（:2-7） | 设备页二次确认 | **沿用 + 文案收敛**为「风险等级：风险（原四级判定：high）」 |
| \`ActionForm.vue\` | 参数 schema 驱动表单（:2-15） | 设备页参数表单 | 不动 |
| \`ActionForm.vue\` 双列断点 | \`@media (max-width:768px)\` → 单列（:71-75） | 动作卡 \`.action-meta\` | **复用同一断点** |
| \`BmsMosStatus.vue\` | MOS 状态徽标 \`ON/OFF\`（:9、:20） | 设备页 MOS 状态条 | 不动 |
| \`BmsDetailPage.vue\` MOS 提示 | 「开关控制请使用下方「受控操作」」（:112） | 同 | **原样复用**该措辞 |
| \`theme.css\` 移动端弹窗兜底 | \`.el-dialog{max-width:92vw}\`（:729-733） | 同 | 复用，另加 ≤480px 底部抽屉 |
| \`theme.css\` 触控目标 | \`.touch-target{min 36px}\`、窄屏 44px（:801-816） | 窄屏按钮 | 复用同口径 |
| \`theme.css\` 移动端宽表 | \`.mobile-table-wrapper\` + \`.mobile-table-hint\`（:796-799、:818-825） | 规则表窄屏 | 复用 |

---

## 6. 设计决策与理由

### D-1 触发器：新增两个枚举值，而不是复用 \`time_window\`
**决策**：在触发器类型下拉新增 \`日出\` / \`日落\` 两个选项，与既有 \`time_window\` 并列，不替换。
**理由**：既有 \`time_window\` 的物理形态是 \`size:5\` 的 \`"HH:MM"\`，解析固定 \`time.Parse("15:04")\`（\`backend/internal/models/automation.go:117-119\`、\`backend/internal/automation/evaluator.go:429-432\`）；把"日落"塞进该字段会迫使前端在保存时把天文时刻算成静态 HH:MM —— 而日落时刻**每天都在变**，静态值第二天就错了。因此必须由后端按日期求解（属架构线）。
**代价**：新增枚举值 → 需要 DB 迁移与前后端类型同步（架构线负责）；本稿只定义界面与文案。

### D-2 偏移分钟用「有符号整数 + 实时文案」，而不是两个字段
**决策**：一个 \`number\` 输入框，负值 = 之前，正值 = 之后；右侧实时回显"日落前 30 分钟"。
**理由**：用户原始需求的原话就是"日落前 30 分钟"——这是**一句话**，不是两个字段。让界面直接回显这句话，用户不必在脑内把"方向 + 绝对值"翻译成自然语言，也避免"填了 -30 却以为在之后"的方向误读。
**代价**：需要一处实时文案渲染（demo 内已实现，见断言「S1 偏移分钟联动文案」）。

### D-3 多动作：卡片列表，不用表格
**决策**：用纵向卡片列表承载动作，每卡展示 目标设备 / 动作 / 参数摘要。
**理由**：① 卡片天然容纳"风险原因"这种长度不定的说明文本，表格行放不下；② 上移/下移是**顺序语义**操作，纵向列表的视觉顺序与执行顺序一致；③ 复用既有 \`DeviceControlPanel.vue\` 的卡片语言（\`.op-item\` 圆角 8px + \`--el-border-color-lighter\` 边框，:169 内联样式），不引入新视觉。
**代价**：动作数量很大时（>10）纵向滚动变长 —— 本稿以 \`+ 添加动作\` + 计数提示缓解，未做折叠（见 §9 O-4）。

### D-4 风险等级收敛为二级，但**不改既有四级枚举**
**决策**：界面只呈现「普通 / 风险」两级；映射 \`low|medium → 普通\`、\`high|critical → 风险\`；**后端四级枚举原样保留**，界面同时在风险原因里注明"原四级判定：high"。
**理由**：用户要求的是"两个等级"的**呈现口径**，不是要删掉后端枚举。实测四级字面量在驱动层有 **34 处**（见 §8 事实锚点 F5），改枚举会波及 \`allowedRisk\` 校验（\`definition.go:186-193\`）、命令执行确认门、审计、仿真场景。本稿只在**展示层**收敛，把"单一事实源"留给架构线定义映射函数。
**代价**：界面同时出现"风险"和"原四级判定：high"两套词，略显啰嗦 —— 但这是**刻意的**：它让评审者一眼看出映射是否与后端一致，避免设计稿被误当成"后端已改成两级"。

### D-5 保存前弹窗：逐项勾选，而不是一次总确认
**决策**：风险清单里**每个**风险动作各有一个复选框，全部勾满才解禁「保存规则」。
**理由**：需求 §1.3 要求"保存时弹窗列出风险操作并二次确认"。一次总确认在 3 个以上风险动作时会退化成"闭眼点确定"；逐项勾选强制用户逐条读完 动作名 + 目标设备 + 参数。默认焦点落在**安全侧**（取消）也沿用既有 \`feedback.confirmDanger\` 的裁决（\`frontend-shared/src/utils/feedback.ts:52-110\`：不得诱导性确认）。
**代价**：风险动作多时点击成本高 —— 这是刻意的摩擦，且只在"含风险动作"时出现。

### D-6 风险不能只靠颜色：图标 + 文字 + 边框 + 原因块，四重编码
**决策**：风险 = ⚠ 图标 + 文字「风险」+ 红色徽标 + 卡片左侧红边 + 独立「风险原因」文本块；普通 = ● 图标 + 文字「普通」+ 灰色徽标。
**理由**：色觉障碍用户（红绿色盲约占男性 8%）无法区分红/灰徽标；只有颜色时"风险"信息完全丢失。本稿四重编码，任一路径都能读到风险。demo 断言实测徽标文本为 \`⚠风险\`、颜色为 \`rgb(185, 28, 28)\`。
**代价**：视觉密度上升；卡片比纯表格更"重"。这是安全信息的必要成本。

### D-7 沿用既有色彩/间距 token，不发明新色
**决策**：全部颜色取自 \`frontend-shared/src/styles/theme.css\` 亮色块：基色 \`--color-primary:#1f5ad8 / --color-success:#147a3a / --color-warning:#9a5b06 / --color-danger:#b91c1c / --color-info:#5e6673\`（:27-35）、文字四档（:48-51）、边框三档（:54-56）、EP \`-light-9\` 浅底（:212-257）；圆角 \`--radius-md:8px\`；间距 16px 卡片内边距、8px 组件间距；断点 \`768px / 480px\`（:8-13）。
**理由**：该调色板经过对比度实测（\`theme.css:22-25\`：primary 白字 5.98、success 5.42、warning 5.42、danger 6.47、info 5.79），自创色会破坏既有 WCAG AA 结论。

### D-8 【依赖未决项 O-1】触发时"自动下发" vs "转待确认"——本稿不预设
**决策**：demo 的保存前弹窗**默认渲染两个选项并列**，并显式标注"待裁决 O-1，本设计稿不预设"；同时提供两个**已裁决文案变体**（A 自动下发 / B 转待确认）供截图对比，\`?variant=auto|pending\` 切换。
**为什么必须这样**：现状实现与"保存后自动执行"**方向相反**——后端 \`commandexec/service.go:517-531\` 对 high/critical 一律要求确认令牌，system 执行者**只豁免 low/medium**；而自动化 planner 以 system 调用且不带令牌（\`automation/planner.go:232-240\`），且 \`isGateError\` 只认 \`ErrActionUnavailable\`（\`planner.go:376-378\`），⇒ 高风险动作在**当前实现下永远下发失败**，事件落 \`failed_dispatch\` 且前端无提示。设计文档"铁律 4"（\`planner.go:31\`）也明确高风险策略**不直接执行**，只生成"建议执行"通知。
**⇒ 因此本稿的弹窗文案不得断言"风险动作会在规则触发时自动下发到设备"**（初稿曾如此写，已修正为中性表述）。该断言把一个**未批准的裁决**写进了界面。
**依赖登记**：本稿弹窗中的"触发时如何处理"区块整体依赖 **O-1**；在 O-1 裁决前，该区块只能以"两选项并列 + 待裁决标注"的形态出现。变体截图：
- 变体 A（自动下发）：\`shots/04b-variant-A-auto-dispatch.png\`
- 变体 B（转待确认）：\`shots/04c-variant-B-pending-confirm.png\`
- 未裁决（默认，**交付基线**）：\`shots/04-save-confirm-risk-list.png\`

---

## 7. 窄屏（≤480px）方案

断点与仓库同源：\`theme.css:8-13\` 定义 \`--breakpoint-xs:480px\`、\`--breakpoint-md:768px\`；既有组件统一用 \`@media (max-width:768px)\`（\`ActionForm.vue:71-75\`、\`DeviceControlPanel.vue:169\`、\`BmsMosStatus.vue:108\`、\`PageHeader.vue:70\`）。本稿沿用 **768px 主断点 + 480px 窄屏加强档**。

| 元素 | 768px 以下 | 480px 以下 |
|------|-----------|-----------|
| 动作卡 \`.action-meta\`（目标设备/动作/参数） | 双列 → 单列 | 单列 + \`dt/dd\` 改为上下堆叠 |
| 动作卡操作区（上移/下移/编辑参数/删除） | 保持在标题行右侧 | 换行到卡片第二行、右对齐；按钮补足 44px 触控高度 |
| 风险清单 \`.risk-item__meta\` | 单列 | \`dt/dd\` 上下堆叠，避免"风险原因"标签挤压正文 |
| 弹窗（规则编辑器 640px / 确认弹窗 440px） | \`width:92vw\`（与 \`theme.css:729-733\` 的 \`.el-dialog{max-width:92vw}\` 同口径） | **改为底部抽屉**：\`width:100%\`、贴底、顶部圆角 12px、\`max-height:92vh\`、内容区独立滚动 |
| 弹窗 footer | 正常 | 按钮 \`flex:1\` 撑满、\`min-height:44px\`；确认进度文本独占一行 |
| 规则表 | 复用 \`.mobile-table-wrapper\` 横向滚动 + \`.mobile-table-hint\`（\`theme.css:796-799\`、\`818-825\`） | 同左，表格 \`min-width:560px\` |
| 表单标签 | \`.el-form-item\` 由左右布局改为上下 | 同左 |

**为什么弹窗要变底部抽屉**：640px 宽的规则编辑器在 390px 视口下即使 \`92vw\` 也只有 359px 可用宽，表单标签 + 控件会被压到逐字换行；而保存前确认弹窗内容长度不可控（风险动作越多越长），全屏抽屉能让内容区独立滚动且 footer 常驻可达。
**实测**：\`shots/07-narrow-390-save-confirm.png\`（390×844 设备像素比 2）—— 弹窗宽度实测 **390px**（满宽）、\`document.documentElement.scrollWidth === innerWidth\`（无横向溢出）；\`shots/08-narrow-390-action-cards.png\` 动作卡实测**单列**且无横向溢出。

---

## 8. 无障碍说明

| 要求 | 本稿做法 | 验证方式 |
|------|----------|----------|
| **风险不得只靠颜色** | 风险 = ⚠ 图标 + 文字「风险」；普通 = ● 图标 + 文字「普通」；另有卡片红边与「风险原因」文本块 | 断言实测徽标文本 \`⚠风险\`（含图标与文字），见 §10 A3 |
| 徽标对比度 | 风险徽标文字 \`--color-danger:#b91c1c\` 于 \`--el-color-danger-light-9:#f8e8e8\` 上 = **5.45:1**（\`theme.css:247\` 实测值），≥ AA 4.5 | \`theme.css:243-247\` 记录 |
| **键盘可达** | 自绘 combobox = \`role=combobox\` + \`aria-expanded\` + \`aria-controls\` + \`aria-activedescendant\`；选项 = \`role=option\` + \`aria-selected\`；开关 = \`role=switch\` + \`aria-checked\`；所有可点元素为 \`<button>\`/\`<input>\` 原生可聚焦元素或 \`tabindex=0\` | 断言「S1 键盘可达」（Enter 展开 → ↓ 移动 → Enter 选定）；§10 A1 |
| 键盘契约 | \`Enter/Space\` 展开与选定、\`↑/↓\` 移动高亮、\`Esc\` 收起（与 Element Plus \`el-select\` 一致） | 同上 |
| **焦点可见** | 全局 \`:focus-visible{outline:2px solid var(--el-color-primary); outline-offset:1px}\`，覆盖按钮/开关/combobox/复选框/卡片 | 代码内 \`index.html\` 的 \`:focus-visible\` 规则 |
| **默认焦点在安全侧** | 保存前确认弹窗与手动二次确认弹窗打开时，焦点显式落在「取消」 | 断言「S4 默认焦点在安全侧（取消）」实测 \`activeElement.textContent === '取消'\`；沿用 \`feedback.ts:52-110\` 的既有裁决 |
| 弹窗语义 | \`role=dialog\` + \`aria-modal=true\` + \`aria-labelledby\` 指向标题 | \`index.html\` \`overlay()\` |
| 背景隔离 | 弹窗打开时页面根节点加 \`aria-hidden="true"\` | \`index.html\` \`render()\` |
| 语义分组 | 动作列表 \`<ul>/<li>\`；元数据 \`<dl>/<dt>/<dd>\`；进度用 \`role=status\` 播报"已确认 N / M" | 同上 |
| 图标不喧宾夺主 | 所有装饰性图标 \`aria-hidden="true"\`，信息由相邻文本承载 | 同上 |
| 触控目标 | ≤480px 时行内按钮 \`min-height:36px\`（\`.is-link\` 也补足），弹窗主按钮 \`44px\`；与 \`theme.css:801-816\` 的 36px/44px 口径一致 | 断言「窄屏 390」无横向溢出 |

---

## 9. 待裁决项（O-x）

> 本稿**不自我批准**任何未决项。以下问题需要用户/统筹方拍板。

| 编号 | 问题 | 选项 | 代价 | 本稿现状 |
|------|------|------|------|----------|
| **O-1** | **风险动作在规则触发时是"自动下发"还是"转待确认通知"？** 这是本稿最关键的未决项，直接决定保存前弹窗的文案与语义 | **A. 自动下发**：需后端放开 system 执行者的确认门（现 \`commandexec/service.go:521-522\` 只豁免 low/medium），并补齐"保存时勾选"到"触发时授权"的信任链<br>**B. 转待确认**：与现状铁律 4（\`automation/planner.go:31\`）一致，触发落 \`pending_confirm\`，人工在通知中心确认后下发<br>**C. 按规则白名单**：每条规则显式声明"本规则的风险动作已获授权" | A 的代价：把"保存时的一次勾选"变成长期授权，规则被改/被复制时授权边界不清；且需同步改确认门与审计<br>B 的代价：多动作规则在夜间触发时无人值守，动作全部积压待确认，"日落自动断电"实际不生效<br>C 的代价：多一个状态维度，UI 与后端都要加字段 | 弹窗**默认渲染 A/B 两选项并列**并标注"待裁决"；变体截图 04b/04c 仅用于对比，**不代表已批准**。见 §6 D-8 |
| **O-2** | 风险原因文案从哪来？ | **A. 后端新增 \`risk_reason\` 字段**（单一事实源，多端一致）<br>**B. 前端硬编码映射表**<br>**C. 驱动层 \`Description\` 复用** | A 需 DB/接口改动；B 会漂移（多处文案不同步，本仓已有此类债）；C 的 \`Description\` 是**操作说明**不是**危险说明**（如 set_mos_policy 的 description 写的是"一次提交充电/放电两个软件关闭位；bounded 写+读回 fet_status 对账"，见 \`jiabaida_control.go:36\`），直接当风险原因用户读不懂 | demo 用**示例文案**并在界面注明是设计稿文案（\`RISK_REASON\` 常量） |
| **O-3** | 既有的规则级 \`require_confirmed\` 与新的动作级风险是什么关系？ | **A. 并存**（本稿默认）：\`require_confirmed\` 管"触发后要不要人确认"，风险徽标管"动作本身危不危险"<br>**B. 合并**：风险动作强制 \`require_confirmed=true\`<br>**C. 废弃** \`require_confirmed\` | A 的问题：两个开关语义相邻，用户可能困惑；B 的问题：与 O-1 冲突（若选 A 自动下发，就不能强制 require_confirmed）；C 是破坏性变更 | 本稿**并存**并在对照表 R9 注明；不合并 |
| **O-4** | 动作数量上限与超限形态？ | **A. 不设上限**，靠纵向滚动<br>**B. 设上限（如 8 个）**，超出时提示<br>**C. 分组/折叠** | A 在几十个动作时表单不可用；B 需定数字（**本稿不猜数字**）；C 增加交互复杂度 | 本稿**未设上限**，只显示"共 N 个动作"计数 |
| **O-5** | 保存前确认弹窗是否也覆盖 \`medium\`（GPIO/PWM）？ | **A. 不覆盖**（本稿默认，按用户"两级"口径 medium→普通）<br>**B. 覆盖**，medium 也进风险清单 | A 的问题：设备页手动路径**仍会对 medium 弹确认**（\`DeviceControlPanel.vue:148\` 的 \`requiresConfirmation = medium\\|high\\|critical\`），于是出现"手动要确认、规则保存不要确认"的不对称；B 的问题：与用户"两个等级"的口径冲突（medium 就被升格为风险） | 本稿按 A 呈现，并在风险原因里显示原四级判定值，便于评审发现不对称 |

---

## 10. 自证记录（可复现）

### 10.1 环境事实（本机实测，非推测）

| 事实 | 证据 |
|------|------|
| \`/usr/bin/chromium-browser\` **不可用** | 执行输出 \`snap-confine is packaged without necessary permissions and cannot continue / required permitted capability cap_dac_override not found in current capabilities\`；且它只是 snap 包装脚本（\`cat /usr/bin/chromium-browser\` 首行 \`#!/bin/sh\`，内含 \`if ! [ -x /snap/bin/chromium ]\`） |
| 可用浏览器二进制 | \`/snap/chromium/3548/usr/lib/chromium-browser/chrome\`（\`ls -la\` 实测 268553376 字节，可执行；直接调用即绕过 snap-confine） |
| \`~/.cache/ms-playwright\` **无浏览器** | \`ls -la ~/.cache/ms-playwright/\` 只有 \`.links\` 与 \`ffmpeg-1011\`；\`chrome/\`、\`chrome-headless-shell/\` 目录内为空 |
| \`~/.cache/puppeteer\` **无浏览器** | \`chrome/linux-149.0.7827.22/\` 与 \`chrome-headless-shell/linux-149.0.7827.22/\` 目录均为空 |
| \`/usr/bin/firefox\` **不可用** | 输出 \`cannot create XDG_RUNTIME_DIR folder "/run/user/1000/snap.firefox": permission denied\` |
| 可用的驱动库 | \`/home/bcat/workspace/rtk-gui-v2/node_modules/.pnpm/playwright-core@1.62.1/…/playwright-core/index.mjs\`（仅借用驱动，不加载页面依赖） |
| \`frontend-shared/node_modules\` 未安装 | 任务环境事实；本稿全程未执行 npm/pnpm install |

### 10.2 复现命令

\`\`\`bash
# 截图 + 断言（重拍全部 10 张 PNG，21 条断言）
cd /home/bcat/workspace/EHome
export XDG_RUNTIME_DIR=/tmp/xdg-runtime-1000 && mkdir -p "$XDG_RUNTIME_DIR"
node designs/automation-sunrise-mos/demo/capture.mjs
echo "exit=$?"     # 期望 0
\`\`\`

单张状态的手工复现（用系统 chrome 直接截图）：

\`\`\`bash
export XDG_RUNTIME_DIR=/tmp/xdg-runtime-1000 && mkdir -p "$XDG_RUNTIME_DIR" /tmp/prof
CHROME=/snap/chromium/3548/usr/lib/chromium-browser/chrome
DEMO=/home/bcat/workspace/EHome/designs/automation-sunrise-mos/demo/index.html
"$CHROME" --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
  --user-data-dir=/tmp/prof --hide-scrollbars --virtual-time-budget=3000 \
  --window-size=1440,1100 --screenshot=/tmp/out.png "file://$DEMO?bar=0&state=sunrise"
\`\`\`

demo 的 URL 参数：\`?bar=0\` 隐藏演示脚手架（用于截图）、\`?state=<名>\` 直达某个状态、\`?variant=auto|pending\` 切换 O-1 文案变体。

### 10.3 截图清单（10 张，全部实拍）

| 截图 | 覆盖状态 | 直达 URL | 尺寸 |
|------|----------|----------|------|
| \`shots/01-trigger-sunrise-sunset.png\` | **状态 1** 触发器下拉展开，含「日出」「日落」（「事件驱动」仍 disabled），已选日落 | \`?bar=0&state=sunrise\` | 1280×2226 |
| \`shots/02-multi-action-cards.png\` | **状态 2** 3 张动作卡，每卡含 目标设备/动作/参数，含增删排序控件 | \`?bar=0&state=actions\` | 972×1030 |
| \`shots/03-risk-badge-and-reason.png\` | **状态 3** 风险卡：⚠「风险」徽标 + 风险原因块 | \`?bar=0&state=risk\` | 972×458 |
| \`shots/04-save-confirm-risk-list.png\` | **状态 4（交付基线）** 保存前确认：2 个风险动作清单 + 逐项勾选 + O-1 待裁决区块 | \`?bar=0&state=save-risk\` | 1280×1552 |
| \`shots/04b-variant-A-auto-dispatch.png\` | 状态 4 · O-1 变体 A 文案（自动下发） | \`?bar=0&variant=auto&state=save-risk\` | 1280×1310 |
| \`shots/04c-variant-B-pending-confirm.png\` | 状态 4 · O-1 变体 B 文案（转待确认） | \`?bar=0&variant=pending&state=save-risk\` | 1280×1310 |
| \`shots/05-manual-risk-confirm.png\` | **状态 5** 设备页手动操作风险动作的二次确认（含参数回显 + 必填操作理由） | \`?bar=0&state=manual\` | 880×1006 |
| \`shots/06-normal-action-control.png\` | **状态 6** 普通动作对照：无风险色、无风险原因块、保存不弹确认 | \`?bar=0&state=normal\` | 1280×1520 |
| \`shots/07-narrow-390-save-confirm.png\` | 窄屏 390px · 保存前确认=底部抽屉满宽、无横向溢出 | \`?bar=0&state=save-risk\`（viewport 390×844，DPR 2） | 780×1688 |
| \`shots/08-narrow-390-action-cards.png\` | 窄屏 390px · 动作卡单列、无横向溢出 | \`?bar=0&state=actions\`（viewport 390×844，DPR 2） | 780×1688 |

### 10.4 断言明细（\`capture.mjs\`，21/21 通过）

| # | 断言 | 实测 |
|---|------|------|
| A1 | S1 下拉新增 日出/日落，且「事件驱动」仍 disabled | 选项文本 \`传感器阈值\|时间窗口\|日出\|日落\|事件驱动（暂不支持创建）\`，disabled 计数 1 |
| A2 | S1 偏移分钟联动文案 | 选中日出后回显 \`日出前 30 分钟\` |
| A3 | S1 键盘可达（Enter 开 → ↓ 移 → Enter 定） | 选定后触发器值非空 |
| A4 | S2 增(3→4)/删(4→3)/上移生效 | \`3/4/3\`，上移后首卡为「重启 BMS」 |
| A5 | S2 卡片含 目标设备/动作/参数 | 通过 |
| A6 | S3 徽标 = 图标 + 文字（不只靠颜色） | 文本 \`⚠风险\`，颜色 \`rgb(185, 28, 28)\` |
| A7 | S3 风险原因存在且引用原四级 | 含「风险原因」与「原四级判定：high」 |
| A8 | S3 普通卡无风险色/无原因块 | \`0/0\` |
| A9 | S4 列出全部风险动作（动作名+目标设备+参数） | 2 条，字段齐全 |
| A10 | S4 必须逐项勾选才可保存 | \`true/true/false\` |
| A11 | S4 文案不预设裁决（含 O-1 选项 A/B） | 含「待裁决」+「选项 A」+「选项 B」 |
| A12 | S4 弹窗**不再**断言"风险动作会在规则触发时自动下发" | 通过（回归守卫） |
| A13 | S4b 变体 A 文案 | 含「自动下发」+ \`service.go:521-522\` |
| A14 | S4c 变体 B 文案 | 含「待确认」+ \`planner.go:31\` |
| A15 | S5 手动风险操作弹二次确认 + 回显参数 | 含 \`set_mos_policy\` + 设备名 |
| A16 | S5 无操作理由不可提交 | \`true/false\` |
| A17 | S5 普通操作无二次确认 | 无 overlay 且提示"操作已排队" |
| A18 | S6 无风险徽标 / 无原因块 | \`0/0\` |
| A19 | S6 保存不弹二次确认 | 无 save-risk overlay 且提示"未弹出二次确认" |
| A20 | 窄屏 390 保存确认 = 底部抽屉满宽且无横向溢出 | 宽 390px，\`overflow=false\` |
| A21 | 窄屏 390 动作卡单列且无横向溢出 | \`singleCol=true, overflow=false\` |

---

## 11. 事实锚点表

> 以下每条均可由统筹方独立复现（file:line 或命令+输出）。**不含任何"大概/通常/应该"**。

| # | 事实 | 锚点 |
|---|------|------|
| F1 | 触发器类型现状 = \`sensor_threshold\` / \`time_window\` / \`event\`（事件驱动占位） | \`frontend-shared/src/api/automation.ts:12-15\`；\`AutomationRules.vue:186-191\`（下拉含 disabled 的 event） |
| F2 | \`time_window\` 字段组 = 开始时间 / 结束时间 / 触发沿（enter/exit/inside） | \`AutomationRules.vue:218-232\`；\`automation.ts:20-21\` |
| F3 | 动作区现状 = **单**目标设备 + **单**动作 ID + **单**参数 textarea + 「需人工确认」 | \`AutomationRules.vue:251-273\` |
| F4 | 表单模型单动作字段 | \`AutomationRules.vue:467-490\`（\`action_device_id\`/\`action_id\`/\`action_params_json\`/\`require_confirmed\`） |
| F5 | 动作风险四级枚举 \`low\|medium\|high\|critical\` | \`backend/internal/deviceaction/definition.go:186-193\`（\`allowedRisk\`） |
| F6 | 四级字面量分布（命令实测） | \`cd backend/internal/drivers && for f in builtin.go jiabaida_control.go inverter_techfine.go; do grep -o 'Risk: *"<level>"' $f \\| wc -l; done\` ⇒ **low 12 / medium 5 / high 11 / critical 6**（builtin 5/0/4/2，jiabaida 6/5/7/4，inverter 1/0/0/0） |
| F7 | GPIO/PWM 动作 risk = \`medium\` | \`backend/internal/deviceaction/periph.go:42\`、\`:65\` |
| F8 | \`set_mos_policy\`：Name「设置充放电 MOS 软件策略」、Risk \`high\`、参数 \`charge_software_closed\`/\`discharge_software_closed\`/\`priority\` | \`backend/internal/drivers/jiabaida_control.go:34-44\` |
| F9 | \`bms_restart\`：Risk \`critical\` | \`backend/internal/drivers/jiabaida_control.go:60-63\` |
| F10 | `read_basic_info`：Risk `low` | 声明于 `backend/internal/drivers/jiabaida_control.go:18`；等级由 helper 固定，见 `jiabaida_control.go:196-198`（`jiabaidaReadAction` 内 `Semantics: "read", Risk: "low"`） |
| F11 | 确认制门禁：high/critical 一律须 token；system actor **只豁免 low/medium** | \`backend/internal/commandexec/service.go:517-531\`（:520-523 为判定与豁免） |
| F12 | 自动化 planner 以 system 调用且**不带** ConfirmationToken | \`backend/internal/automation/planner.go:232-240\`（CreateInput 无 ConfirmationToken 字段） |
| F13 | \`isGateError\` 只认 \`ErrActionUnavailable\` ⇒ 确认失败落 \`failed_dispatch\` | \`backend/internal/automation/planner.go:376-378\` |
| F14 | 设计文档「铁律 4」：高风险策略触发**不直接执行**，只生成"建议执行"通知 | \`backend/internal/automation/planner.go:31\` |
| F15 | 既有风险徽标：\`critical\`→「严重风险」danger、\`high\`→「高风险」warning | \`frontend-shared/src/views/edge-device/shared/DeviceControlPanel.vue:40-41\` |
| F16 | 既有确认判定：\`medium\|high\|critical\` 需确认 | \`DeviceControlPanel.vue:148\`（\`requiresConfirmation\`） |
| F17 | 既有二次确认弹窗：标题「确认受控操作」、正文「风险等级：{{risk}}」、必填操作理由、确认键 danger | \`frontend-shared/src/views/edge-device/shared/ActionConfirmationDialog.vue:2-7\` |
| F18 | 既有参数表单由 \`input_schema\` 驱动；>6 字段用双列，\`max-width:768px\` 降单列 | \`frontend-shared/src/views/edge-device/shared/ActionForm.vue:2-15\`、\`:71-75\` |
| F19 | 受控操作卡片：\`role=button\` + \`tabindex=0\` + Enter/Space 触发 | \`DeviceControlPanel.vue:35\` |
| F20 | 设备页 MOS 提示文案「开关控制请使用下方「受控操作」」 | \`frontend-shared/src/views/edge-device/bms/BmsDetailPage.vue:112\` |
| F21 | 设计 token：基色/文字/边框 | \`frontend-shared/src/styles/theme.css:27-35\`、\`:48-51\`、\`:54-56\` |
| F22 | EP \`-light-9\` 浅底与对比度实测值 | \`theme.css:212-257\`（danger-light-9 \`:247\` 标注 5.45） |
| F23 | 断点 token 与移动端弹窗兜底 | \`theme.css:8-13\`、\`:729-733\`（\`.el-dialog{max-width:92vw}\`） |
| F24 | 触控目标 36px/44px 口径 | \`theme.css:801-816\` |
| F25 | 移动端宽表包装与滑动提示 | \`theme.css:796-799\`、\`:818-825\`；\`AutomationRules.vue:12-13\` |
| F26 | 危险确认默认焦点须在安全侧（不得诱导性确认） | \`frontend-shared/src/utils/feedback.ts:52-110\`（\`focusDangerConfirmSafeSide\`） |
| F27 | 页头标题/副标题现状 | \`AutomationRules.vue:3\`（title「自动化策略」subtitle「传感器数据或时间窗口触发设备动作或通知」） |
| F28 | 规则表列结构 | \`AutomationRules.vue:14-77\`（名称/触发器/动作/冷却熔断/确认制/启用/操作） |
| F29 | 手动触发走**普通**确认（非 danger） | \`AutomationRules.vue:694-722\` |
| F30 | 删除规则与「确认执行」走 danger 确认 | \`AutomationRules.vue:655-671\`、\`:673-689\` |
| F31 | 事件结果枚举 9 态 | \`frontend-shared/src/api/automation.ts:35-44\`；\`AutomationRules.vue:406-416\` |
| F32 | 截图环境：\`/usr/bin/chromium-browser\` 不可用、真实 chrome 在 snap 内 | 见 §10.1 命令输出 |
