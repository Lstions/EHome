# GPIO / PWM UI 重设计规格：行式资源控制面板

## 0. 目标与边界

- 目标：让用户在同一视图快速判断“有哪些引脚、谁占用、当前值、能做什么”，并能安全完成 GPIO/PWM 配置与即时控制。
- **明确拒绝 GPIO/PWM 合并视图**：GPIO 与 PWM 是 ESP32 独立上报的两类硬件资源，不以物理 GPIO 行投影或推导 PWM 资源。
- 采用与现有总线资源、日志控制一致的“分组标题 + 纵向行列表 + 内联控件”语言；保留 Element Plus 的卡片、折叠、标签、按钮与反馈体系。
- 本规格以 `capabilities.buses.gpio` / `capabilities.buses.pwm` 为唯一资源事实源；没有报告时显示等待节点硬件资源上报。

## 1. 现状依据与问题

- `NodeDetail.vue`：页面主体是纵向 `el-card`，章节间距 20px；总线配置已由 `ChannelPanel` 承载，GPIO/PWM 应继续位于“硬件资源”内，而非再加页面级卡片。
- `ChannelPanel.vue`：I2C/UART/SPI/ADC 已使用折叠分组及纵向资源项；GPIO/PWM 却按 GPIO 清单各渲染一套小卡片，造成重复、扫描困难与占用关系割裂。
- `LogPanel.vue`：控制项使用可换行 flex、明确标签、16px 节奏；这是窄屏控件排列的直接参考。
- `Dashboard.vue`：Element Plus 语义色与 token 已形成产品语言；统计卡片适合摘要，不适合高密度资源控制。
- `PeripheralControl.vue`、`GPIOPinCard.vue`、`PWMChannelCard.vue`：均依赖小卡片网格；操作可用，但“删除 ✕”、GPIO ON/OFF 双危险色、整体离线 `pointer-events:none` 均弱化了层级或状态解释。

## 2. 页面 IA

节点详情 / 总线配置 / 硬件资源：

1. 顶部工具条：`刷新`（次操作）、`保存配置`（仅总线配置变更时主操作）、`新建通道`（主操作）。GPIO/PWM 即时配置不混入“保存配置”的待保存语义。
2. 通信资源：I2C、UART、SPI、ADC，沿用现有 `el-collapse` 分组。
- GPIO 与 PWM 分为两个独立 `el-collapse-item`：GPIO 标题汇总 `buses.gpio`，PWM 标题汇总 `buses.pwm`；两者数量都只来自当前 ESP32 报告。
- GPIO 列表每行身份是物理 GPIO；PWM 列表每行身份是 `PWM0/PWM1...`，已配置时显示 `PWM0 → GPIO6`。
- PWM 配置先锁定硬件资源，再从 ESP32 报告且未被 GPIO/PWM/UART/I2C/SPI 占用的 GPIO 中选择输出路由。
- 配置记录不在当前报告中时仅显示“无效配置”，不得补入有效资源行。

## 3. 统一行容器

建议语义 DOM（组件名可调整）：

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

- 不用 `el-table`：行内有 Switch、Slider、菜单及动态反馈，移动端需要自然重排，不适合固定列。
- 每行桌面最小高 64px、padding 12px 16px、行间无卡片 gap，以 `border-bottom` 分隔；hover 仅用 `var(--el-fill-color-light)`，不抬升、不加重阴影。
- 配置成功行用 3px 左色条表达类型：GPIO `var(--el-color-primary)`，PWM `var(--el-color-success)`；可用/占用不使用整行彩底。

## 4. GPIO 行 DOM 信息顺序

从左到右、从高优先级到低优先级：

1. `.pin-identity`：`GPIO 12`（主标识，等宽数字）→ 用户标签（次文本，可省略）→ `GPIO` 类型 `el-tag`。
2. `.pin-configuration`：方向标签 `OUTPUT / INPUT / INPUT_PULLUP / INPUT_PULLDOWN` → 初始电平（仅 OUTPUT，`初始 HIGH`）或上下拉信息。
3. `.pin-runtime`：当前电平先于控件；状态点 + `HIGH / LOW / 未知`。INPUT 后接“读取”按钮；OUTPUT 后接一个带 `active-text="HIGH"`、`inactive-text="LOW"` 的 `el-switch`。
4. `.pin-actions`：`编辑`文本按钮 → `el-dropdown` 更多菜单 → `移除配置`危险项。
5. `.pin-feedback`：仅在请求中/失败时出现；如“正在写入 HIGH…”或“写入失败 · 重试”。成功不永久占位，以 `ElMessage` 简短反馈。

约束：

- 删除现有 ON / OFF / TOGGLE 三按钮并列；一个 Switch 已完整表达二元输出，避免 OFF 被误当成危险操作。
- INPUT 不显示禁用 Switch；用“读取”主动作且保留最后一次值。未知不能默认为 LOW。
- 行自身不响应点击，避免滑块/开关误触；编辑必须点击明确按钮。

## 5. PWM 行 DOM 信息顺序

1. `.pin-identity`：`GPIO 12` → 用户标签 → `PWM` 类型 `el-tag`。
2. `.pin-configuration`：`1 kHz` → `14 bit` → `自动启动`（无则省略），均为只读紧凑字段。
3. `.pin-runtime`：运行状态 `el-tag`（运行中/已停止/未知）→ 占空比数值 `37.50%` → `el-slider`；数值必须在滑块前，屏幕阅读器标签为“GPIO 12 PWM 占空比”。
4. `.pin-actions`：运行时主控为一个 `启动/停止`按钮（根据状态互斥显示）→ `编辑` → `el-dropdown` → `移除配置`。
5. `.pin-feedback`：调整中显示“待应用”或 loading；松手后 300ms 提交，失败时回滚到服务端确认值并提供“重试”。

约束：

- 停止是常规可逆控制，用默认按钮，不用 danger；“移除 PWM 配置”才是危险操作。
- 已停止时 slider disabled，但保持当前占空比可读；离线时同理。
- 频率/分辨率编辑进入现有 `el-dialog`，不把多个数字输入塞进资源行。

## 6. 操作层级

- 主操作（每个上下文最多一个蓝色实心）：可用行的“配置 GPIO”或“启用 PWM”；GPIO INPUT 的“读取”；PWM 已停止时的“启动”。
- 次操作：刷新、编辑、GPIO 输出 Switch、PWM Slider、PWM 停止；使用 default/text/控件默认语义，不抢主操作。
- 危险操作：仅“移除配置”。放入 `el-dropdown` 的更多菜单，红色菜单项；触发 `ElMessageBox.confirm`，文案包含引脚、当前用途与影响。
- 禁止以红色表达 LOW/OFF/停止；红色只表示错误或破坏性变更。
- 任一行请求进行中，只锁定该行相关操作，不阻塞整个列表；避免多个按钮同时出现 loading。

## 7. 未配置与已配置同屏策略

- 数据源先以硬件 GPIO 能力表生成唯一行，再合并 `gpioConfigs`、`pwmConfigs`；同一 pin 的互斥状态只在一行呈现。
- 默认筛选“全部”，保证已配置与可用资源同屏；排序为引脚号，不按状态分区，避免位置随操作跳动。
- 可用行保持 48–56px 紧凑态：`GPIO n` + “可用”标签 + 右侧 `配置 GPIO` 主按钮 + `启用 PWM`次按钮，不渲染空字段占位。
- 已配置行展开到 64–72px，显示配置与运行控件；信息密度来自列对齐，不来自缩小字号。
- 被其他硬件/总线占用时显示 `已占用` warning 标签和用途（如 `UART TX`、`GPIO 配置`）；操作区只给“查看占用”，不提供会失败的启用按钮。
- 若能力数据仅能确认“被 GPIO/PWM 互斥占用”，至少显示 `GPIO 占用`/`PWM 占用`；未知占用方不得猜测。
- 列表很长时依靠筛选与搜索降噪；不隐藏可用资源到另一个 tab，也不复制为第二套 PWM 清单。

## 8. 响应式布局

### 桌面（建议容器宽度 ≥ 769px）

- 行使用 CSS Grid：`minmax(150px, 1.1fr) minmax(180px, 1.2fr) minmax(280px, 2fr) auto`。
- 身份、配置、运行、操作四区同一行；Slider 可用宽度 140–240px；操作区右对齐且不换行。
- 分组工具条同一行；搜索框 220px。使用容器实际宽度优先；若暂无 container query，则沿用项目 `@media (max-width: 768px)`。

### 约 430px

- 外层保持 NodeDetail 现有页面留白；行改为两层，不横向滚动：
  1. 首层：身份 + 状态标签；更多菜单靠右。
  2. 次层：配置摘要占满一行；运行值与控件下一行；主操作按钮最小 88px。
- `.pin-resource-row` grid 为 `1fr auto`；configuration、runtime、feedback 均 `grid-column: 1 / -1`。
- PWM Slider 宽度 100%；数值置于 slider 上方右侧。GPIO Switch 与电平值并排，触控目标不小于 44px。
- 工具条 `flex-wrap: wrap`：筛选占一整行并可水平滚动；搜索框与刷新下一行。按钮不缩成纯图标，保留文字。
- 长标签单行省略并提供 `el-tooltip`；频率等关键值不省略。

## 9. 状态规格

- `loading`：首次加载用 `el-skeleton` 6 行，骨架形状模拟横向资源行；刷新已有数据时保留列表，仅刷新按钮 loading，避免清空闪烁。
- `empty`：硬件能力返回成功但 GPIO 清单为 0，使用 `el-empty`：“该节点未报告 GPIO 资源”，不显示配置 CTA。
- `offline`：分组顶部显示不可关闭的 `el-alert type="warning"`：“节点离线，显示最后已知配置”；行保持完全可读，仅禁用写入、读取、编辑、移除。禁止整行 opacity 0.5 或 `pointer-events:none`。
- `error`：能力/配置加载失败时显示 `el-alert type="error"` + “重试”；若有缓存数据，保留数据并标“可能已过期”。单行操作失败只在 `.pin-feedback` 呈现，不清空列表。
- `occupied`：行可读但不可配置，warning `el-tag` 明确占用方；“查看占用”可定位到对应资源组。占用不是 error，不用 danger。
- 运行态未知：使用 info 标签“状态未知”和占位 `—`；不能把 PWM 默认初始化为“已停止”，也不能把 GPIO 未读取值当 LOW。
- 并发：单行 pending 时设置 `aria-busy="true"`；WebSocket 回报刷新该行，保持滚动位置与筛选条件。

## 10. Element Plus 与 CSS token

优先复用：

- 结构：`el-collapse`、`el-collapse-item`、`el-alert`、`el-skeleton`、`el-empty`。
- 控件：`el-switch`、`el-slider`、`el-button`、`el-dropdown`、`el-input`、`el-segmented/el-radio-group`、`el-dialog`、`el-form`。
- 状态/反馈：`el-tag`、`ElMessage`、`ElMessageBox`、`el-tooltip`。
- token：`--el-color-primary/success/warning/danger`，`--el-text-color-primary/regular/secondary/placeholder/disabled`，`--el-border-color/light/lighter`，`--el-fill-color-light/lighter`，`--el-bg-color`。
- 不新增硬编码品牌色或 `#fff/#303133/#67c23a`；将 ChannelPanel 相关局部硬编码逐步替换为 token。圆角沿用 8px；动效 0.2s，只做颜色变化。
- 可访问性：按钮有可见文本；Switch/Slider 设置包含 pin 的 `aria-label`；颜色之外必须有文字/Tag；键盘焦点使用 Element Plus 默认 focus ring。

## 11. 逐文件修改建议

- `src/views/node/NodeDetail.vue`：不新增 GPIO/PWM 页面级卡片；继续通过 `ChannelPanel` 提供节点状态。清理未实际使用的 peripherals/旧外设样式时需另行确认，不属于本次视觉重构必做。
- `src/components/node/ChannelPanel.vue`：分别展示 GPIO pin 与 PWM hardware channel 两个折叠组；PWM 行仅把 GPIO 作为 route 显示，复用现有 GPIO/PWM 对话框与 API handler。
- `src/components/node/LogPanel.vue`：不改业务；复用其 `.log-controls` 的 wrap 思路作为引脚工具条样式基准。
- `src/views/dashboard/Dashboard.vue`：不改；仅沿用状态色与 768px 响应式断点，不复制 stat-card 视觉到资源列表。
- `src/components/periph/PeripheralControl.vue`：改为同一行式列表容器；若它缺少硬件能力清单，则仅展示已配置行，并明确空态“暂无已配置外设”，不要伪造可用资源。
- `src/components/periph/GPIOPinCard.vue`：重命名/重构为 `GPIOPinRow.vue`（可保留兼容导出过渡）；输出改为单 Switch、输入保留读取；移除 ✕ 改更多菜单 + 确认；离线仅禁用操作。
- `src/components/periph/PWMChannelCard.vue`：重命名/重构为 `PWMChannelRow.vue`；状态不得本地固定 `false` 冒充真实值；停止按钮去 danger；Slider 失败回滚；清理 timer（unmount 时取消）。
- 使用独立 `GPIOResourceList.vue` 和 `PWMResourceList.vue`：前者由 `hardware.gpio` 驱动，后者由 `hardware.pwm` 驱动；禁止统一 pin 行冒充 PWM 资源。

## 12. 开发验收 Checklist

- [ ] 页面不存在 GPIO/PWM 小卡片网格；资源始终为单列行式列表。
- [ ] 每个物理 GPIO 只出现一次，GPIO/PWM/occupied 互斥关系可在同一行判断。
- [ ] GPIO 行与 PWM 行信息顺序符合第 4、5 节，桌面列对齐。
- [ ] 可用行紧凑，已配置行信息完整；默认同屏且可按状态/关键词过滤。
- [ ] GPIO OUTPUT 仅一个 HIGH/LOW Switch；LOW、PWM 停止不使用 danger。
- [ ] 移除配置藏于更多菜单，二次确认包含引脚和影响，成功后行位置不跳动。
- [ ] 430px 宽无横向滚动、无控件溢出；Slider 满宽，触控目标 ≥44px。
- [ ] 桌面操作区不换行，Slider 有合理宽度，列表扫描路径稳定。
- [ ] 首次 loading、保留数据刷新、empty、offline、error、occupied、运行态未知均按规格可复现。
- [ ] 离线内容仍清晰可读；只禁用不可执行操作，并有顶部原因说明。
- [ ] 单行 API 失败有行内反馈；PWM 占空比回滚；其他行仍可操作。
- [ ] loading 锁定粒度为单行/单控件，重复提交被阻止。
- [ ] 全部颜色、边框、文本使用 Element Plus token，无新增硬编码语义色。
- [ ] Switch、Slider、菜单可键盘操作，有包含 GPIO 编号的可访问名称。
- [ ] ChannelPanel 与 PeripheralControl 复用统一列表/行组件，无两套行为分叉。
- [ ] GPIO/PWM 配置对话框继续可用，校验、取消、提交 loading 与错误反馈完整。
- [ ] WebSocket/API 刷新后保留筛选、搜索和滚动位置，不闪回空态。
- [ ] 现有 I2C/UART/SPI/ADC、通道终端、保存配置流程无回归。
