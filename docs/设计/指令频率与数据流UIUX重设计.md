# 指令频率配置与实时数据流 UI/UX 重设计

> **状态**: 实施完成，双重独立审查中
> **版本**: v1.1
> **日期**: 2026-08-09
> **关联**: [前端开发与UIUX设计规范.md](../规范/前端开发与UIUX设计规范.md) | [BMS移动端截图缺陷分析报告.md](../评审/BMS移动端截图缺陷分析报告.md)

## 1. 背景与证据

BMS 移动端截图缺陷分析（2026-08-09）定位的两个重灾区：

| # | 表象 | 数学/源码证据 |
|---|------|--------------|
| 1 | 指令频率行移动端裁剪必然 | `CommandList.vue:128/132` `.cmd-info` 固定 180px + `.cmd-controls` 固定 200px + gap ≈ 404px > 可用宽 ≈ 310px（390 视口），且 `nowrap + overflow:hidden`（`:126`） |
| 2 | 禁用行浅灰字+浅灰底对比度过低；启用/禁用仅靠 opacity 区分，无文字状态 | `CommandList.vue:127/130/133` —— 违反规范 4.2.1（状态必须文字+颜色并用） |
| 3 | 加载失败静默吞掉（catch 后 `commands=[]`） | `CommandList.vue:70-72` —— 违反规范 3.4.6 |
| 4 | 首次加载无 skeleton | 规范 4.3 状态 2 要求 |
| 5 | 数据流固定行高 64px，多行明文内容被裁剪 | `RealtimeDataList.vue:31` RecycleScroller `:item-size="64"`，而 `item-content` 是 `pre-wrap` 多行文本 |
| 6 | 仅 1 条数据时容器仍撑 200px 空白框 | `RealtimeDataList.vue:222` `min-height:200px`（截图缺陷 #11） |
| 7 | BMS 折叠面板内 CommandFrequencySection 未传 `embedded`，collapse→card 双层嵌套，标题栏与内容间 80px+ 留白 | `BmsDetailPage.vue:141` 未传 `:embedded`（截图缺陷 #6 的"留白过大"） |

## 2. 设计目标

1. **容器感知**：指令行在窄容器自动降级为两段式布局，数学上不可能裁剪（不靠固定宽度）。
2. **状态可信**：启用/禁用/未知/失败各有文字+颜色双通道表达；禁用态不用 danger（规范 4.2.1）。
3. **行高自适应**：数据流行按内容自然撑高，不再裁剪多行帧。
4. **空态收敛**：复用共享 `EmptyState`，居中小尺寸，不再出现大空白框。
5. **防回退**：关键布局契约用 `?raw` 源码测试钉死。

## 3. 指令频率配置重设计（CommandList.vue）

### 3.1 行布局契约（桌面单行 → 窄容器两段式，flex-wrap 驱动）

```
.command-item  display:flex; flex-wrap:wrap; align-items:center
├─ .cmd-identity   名称(600字重) + 读/写tag + 0xHEX(mono) + 状态tag
├─ .cmd-desc       flex:1 1 160px，ellipsis（桌面）；移动端 normal+keep-all
└─ .cmd-controls   margin-left:auto；interval输入+ms单位+开关
```

- 桌面宽容器：单行 `身份 | 描述 | 控件`。
- ≤768px：`.command-item` 切纵向堆叠（`flex-direction:column; align-items:stretch`），
  控件行 `space-between` 占满宽，interval 输入 `flex:1` 全宽 —— 310px 可用宽下
  任何长度的指令名都放得下，裁剪从结构上不可能。
- ≤768px 输入框字号 ≥16px（iOS 聚焦缩放，规范 4.4.4）；开关命中区 ≥44px（规范 4.4.5）。

### 3.2 状态语义（单一事实源 = interval 数值）

- `interval > 0` ⇔ 启用。状态 tag：启用 = success「轮询中 · N ms」；禁用 = info 灰「已禁用」。
- 开关 ON 且 interval=0 → 恢复模板默认 `interval_ms`（兜底 5000）。
- 手动输入 interval：0 → 自动置为禁用；>0 → 自动启用。输入框始终可编辑（不被开关锁死）。
- 禁用行：opacity 0.6 + 状态 tag 灰，文字可读（对比度靠正常文字色，不靠灰字灰底）。

### 3.3 加载/失败/保存

- 首次加载：`el-skeleton`（与内容结构相近，规范 4.3 状态 2）。
- 失败：`el-alert type=error` + 「重试」按钮（对齐 CreateWizardCommandIntervals 模式），不再静默吞。
- 保存：`dirty` 计算属性（与加载基线比对）；无改动时保存按钮禁用，旁边给中性说明
  「当前配置与节点一致」（规范 3.4.4 禁用给原因）。保存成功后基线刷新，dirty 归零。

### 3.4 BmsDetailPage 接线修复

折叠面板内传 `:embedded="true"`，消除 collapse→card 嵌套与 80px 留白（规范 4.1.5）。
逆变器/通用详情页在折叠外独立成卡，保持 `embedded=false`。

## 4. 实时数据流重设计（RealtimeDataList.vue）

### 4.1 变高行：虚拟滚动 → 普通滚动列表（简化决策）

- 原实现用 RecycleScroller 固定 `item-size=64`，与多行明文内容冲突（裁剪）。
- **决策**：改用普通滚动列表（`.plain-list`，max-height 内 overflow-y:auto）。
  理由：上游 `useRealtimeData` 已按 maxItems（100/200）在数据层截断（规范 4.5.4
  "显示上限策略在数据层定义"），≤200 行轻量 DOM 无需虚拟化；DynamicScroller
  引入 ResizeObserver/IntersectionObserver 依赖属过度设计（规范 §7.3）。
- 行高完全自然：多行帧不裁剪；1 条数据不再撑空白框（容器高度 = 内容高度）。
- 行模板单处内联定义（单一消费者，不抽组件——避免无复用的文件增殖）。
- 顺带移除组件上从未被内部使用的 `maxItems` 死 prop（3 个消费页同步去掉绑定，
  截断职责归数据层 composable）。

### 4.2 内容兜底（领域事实优先）

明文/16进制格式化结果为空时显示「无数据字段」中性占位，不出现裸「–」或疑似渲染失败
（规范 1.2.1、3.4.5）。

### 4.3 头部与空态

- 头部保持 flex-wrap（已有），显示模式 radio + 计数 tag + 清空按钮（空列表禁用）。
- 空态改用共享 `EmptyState kind=initial size=small`，居中紧凑，替换 el-empty。
- 自动滚顶：新数据到达时滚动容器 `scrollTop=0`（两条路径统一处理）。

## 5. 离线验证基建扩展（MockBmsPanel）

- dev-only 路由内对 `edgeDeviceApi.getCommandIntervals/updateCommandIntervals`
  做模块级 mock 补丁（edgeDeviceApi 是纯对象，属性可替换），注入截图同款 5 条 BMS 指令。
- 追加实时数据流 mock：3 条（普通路径）与 60 条（虚拟路径）两档，
  多行 BMS 帧数据验证行高自适应。
- 生产路由不受影响（mock 只存在于 `/dev/mock-bms`）。

## 6. 防回退围栏（?raw 契约测试）

新建 `CommandListAndRealtimeStream.spec.ts`（源码契约，仅断言 CSS/模板结构，
交互由组件行为测试覆盖——规范 3.7.1）：

1. CommandList 样式不含 `width: 180px`/`width: 200px` 固定宽（防回退到裁剪布局）。
2. CommandList ≤768px 媒体查询存在 `flex-direction: column`。
3. RealtimeDataList 不含 `:item-size="64"` 固定行高绑定（DynamicScroller 用 min-item-size）。
4. RealtimeDataList 引用 EmptyState（空态契约）。

## 7. 验收矩阵

| 项 | 方法 |
|---|---|
| 390/360px 无裁剪、无横向溢出 | CDP `scrollWidth<=clientWidth` + 行 getBoundingClientRect |
| 指令行两段式降级 | CDP 量化：控件行宽 = 容器宽，无 overflow |
| 数据流 1 条无空白框；60 条虚拟滚动 | CDP 量化容器高度、行数 |
| 桌面 1440px 单行布局不回退 | CDP 截图 + 量化 |
| 亮/暗主题 | CDP data-theme 切换截图 |
| vitest + typecheck | 直取退出码 |

## 8. 双重独立审查记录（2026-08-10）

### 第一轮（2 组并行，fail-closed）

**指令频率组：APPROVED_WITH_COMMENTS**（无 P0/P1，7 条 P2）
**数据流组：APPROVED_WITH_COMMENTS**（1 条 P1 + 4 条 P2）

主 Agent 逐条核实源码后全部修复：

| # | 严重度 | 问题 | 修复 |
|---|--------|------|------|
| 1 | P1 | `/dev/mock-bms` 路由无 DEV 门禁生产可达，mock 补丁有泄漏风险 | router 改 `import.meta.env.DEV` 条件展开；生产构建验证 chunk 被完全消除 |
| 2 | P2 | hex 空对象帧显示 `7B 7D` 形似真实帧、误导 | 空对象显式走兜底「(无数据字段)」+ 测试 |
| 3 | P2 | watch `items.length` 满额截断后永不触发滚顶 | 改 watch `items[0]?.id` + 满额用例 |
| 4 | P2 | `bytesToHexLocal` 重复实现且行为分叉 | 复用 `format.ts` 的 `bytesToHex` |
| 5 | P2 | 测试哑 stub 覆盖全局交互 stub、注释过时 | test-setup 补交互式 `ElRadioButton`（注入协议），测试改真实点击 |
| 6 | P2 | 切换设备后 `saved` 提示残留 | `loadCommands` 入口重置 `saved.value = false` |
| 7 | P2 | save 的 generation 守卫使 `saving` 永久卡死 | `finally` 无条件复位 `saving`，守卫只留 baseline 写入 |
| 8 | P2 | `catch (e: any)` + 绕过 feedback 工具 + envelope 猜测 | 改 `unknown` + `feedback.handleError`（规范 §3.1.7/§3.4.1/§3.2.9） |
| 9 | P2 | `deviceType` 死 prop 链（CommandList + Section + 3 消费页） | 链式删除，`v-if="deviceType"` 改 `deviceId` |
| 10 | P2 | 清空输入框（undefined）边界未测 | 补 fail-closed 语义用例 |
| 11 | P2 | dirty 用例标题与断言不符 | 补保存按钮断言 + 改名 |

### 修复后门禁
- vitest 909 passed（+3 新用例）｜typecheck 0 错误｜`pnpm build` 通过（直取退出码）
- 生产构建验证：dist/assets 无 MockBmsPanel chunk、无 `dev/mock-bms` 引用

### 第二轮复审（deleg_a8257db5，2 组并行，fail-closed）

**指令频率组：APPROVED** —— 5 条修复逐条验证通过，实跑测试 8/8 passed。
**数据流组：APPROVED** —— 6 条修复逐条验证通过（生产构建 chunk 消除实证、bytesToHexLocal 全仓 0 残留）。

非阻塞意见（记录在案，不阻塞合入）：
- `CommandFrequencySection.vue:3` `v-if="deviceId"` 依赖 required number 真值性（deviceId=0 隐藏卡片），当前无消费页传 0，fail-closed 可接受。
- `CommandListControlBoundary.spec.ts:11` ElMessage mock 为纯对象不可调用，但该套件从不触发 save/feedback 路径，实跑通过；未来该套件加保存用例需同步升级为可调用 mock。
