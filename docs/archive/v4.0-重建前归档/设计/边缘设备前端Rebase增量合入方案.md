> **状态**: 评审中（第一轮 review 已闭环，双路 APPROVED_WITH_COMMENTS，修订已并入，待执行）
> **版本**: v1.1
> **日期**: 2026-08-09
> **关联**: [00-术语表.md](../设计/00-术语表.md) | [前端图表Tooltip优化](./前端图表Tooltip优化.md)

# 边缘设备前端 Rebase 增量价值评估与合入方案

## 1. 背景

2026-07-26 主分支经历 rebase 事故（提交 `75265880`），使用 `--theirs` 策略解决冲突，
导致 `codex/edge-control-fixes` 分支（收官提交 `6e598b49`）的部分独有增量被整体回退。
经主 Agent + 2 个 subagent 逐文件核对，确认 17 项丢失增量，其中部分为功能缺陷、部分为
移动端样式增强、部分已被后续提交等价替代。

本方案回答两个问题：
1. 结合 main 当前代码，哪些丢失增量**真的有恢复价值**？
2. 有价值的增量如何**分批合入 main**（含验证门禁）？

## 2. 现状核查（源码事实）

以下为方案基于的源码事实，reviewer 须逐条核验。

### 2.1 rememberMe 功能退化（P0）

| 事实 | 证据 |
|------|------|
| main 的 `userStore.login` 调用 `authApi.login({ username, password })`，**未传 rememberMe** | `frontend-shared/src/stores/user.ts:31`（函数签名 :30 已有第三参 `rememberMe = false`，但调用未传递） |
| main 的 `LoginRequest` 接口**无 rememberMe 字段** | `frontend-shared/src/api/auth.ts:3-6`（仅 username/password） |
| 后端 `handler_auth.go` 有 rememberMe → 7 天 TTL 分支 | `backend/internal/api/handler_auth.go:98-102`（`if req.RememberMe { tokenTTL = 7 * 24 * time.Hour }`） |
| 该分支因前端不传参而**永不触发**（死代码） | 同上，`req.RememberMe` 恒为 false |
| `6e598b49` 版本曾正确传递 | `git show 6e598b49:frontend-shared/src/stores/user.ts` 第 31 行 `authApi.login({ username, password, rememberMe })`；`6e598b49:.../api/auth.ts` 第 6 行 `rememberMe?: boolean` |
| LoginForm 仍收集 rememberMe 并传 userStore（3 参） | `frontend-shared/src/views/auth/Login.vue:159/175`（`handleLogin(username, password, rememberMe)` → `userStore.login(username, password, rememberMe)`，签名匹配，**仅 userStore 内部调用 authApi 时丢弃**） |

### 2.2 LineChart 双列 tooltip 丢失（P1）

| 事实 | 证据 |
|------|------|
| main 的 LineChart tooltip 是单列 `<br/>` 堆叠 | `frontend-shared/src/components/charts/LineChart.vue:212-229`（`html += ...<br/>` 逐行拼接） |
| `6e598b49` 版本有双列实现（≤12 单列/>12 双列+降序+confine+溢出省略+"还有X项"） | `git show 6e598b49:frontend-shared/src/components/charts/LineChart.vue` 第 227-263 行 |
| BMS 电芯电压历史图传 16 个 series，全选时 tooltip 16 行 | `frontend-shared/src/views/edge-device/bms/BmsCellVoltageHistoryChart.vue:37-44`（`<LineChart :series="filteredSeries">`，cellCount 默认 16） |
| LineChart 是共享组件（4 处使用） | `LineChart.vue` 被 BmsCellVoltageHistoryChart / HistoryChartSection / DataPanel / Dashboard 引用 |

### 2.3 移动端适配丢失（P1/P2）

| 项 | main 现状 | 证据 |
|----|----------|------|
| TimeRangeSelector 480px 响应式 | 固定 `width:340px` 硬编码、`flex` 无换行 | `frontend-shared/src/components/charts/TimeRangeSelector.vue:17`（`style="margin-left: 10px; width: 340px;"`），`<style scoped>` 无 media query |
| App.vue 字体缩放体系 | 仅剩输入框 16px 防缩放 | `frontend-shared/src/App.vue:60-66`（仅 `@media (max-width:768px) .el-input__inner/.el-textarea__inner/.el-select__wrapper { font-size:16px }`） |
| Monitor descriptions 单列 | 固定 `:column="2"` | `frontend-shared/src/views/monitor/Monitor.vue:145` |
| Inverter 3 组件 480px | 无 media query | `InverterMpptCard.vue:75`（`.mppt-card { min-width:200px; flex:1 }`），其余同 |
| BmsMosStatus 双列 | 无 media query | `BmsMosStatus.vue` `<style>` 无 768px |
| DeviceInfoCard mobile-list | 固定 `:column="2"` | `DeviceInfoCard.vue:4` |
| CommandFrequencySection embedded | 无 embedded prop | `CommandFrequencySection.vue` 全文无 `embedded` |
| GaugeChart tooltip | 无 tooltip 配置 | `GaugeChart.vue:56`（仅有 detail formatter） |
| BmsCellVoltageChart tooltip | 无 confine/position | `BmsCellVoltageChart.vue:54`（tooltip 无 confine/position） |
| RealtimeDataList 响应式 | item-size 80 + min-height 400 固定 | `RealtimeDataList.vue:31,133` |

### 2.4 已被等价替代（不恢复）

| 项 | main 的替代 | 证据 |
|----|-----------|------|
| 手写 stat-card + MobileStatCard | StatCard 共享组件 + 4 列紧凑方案 | `EdgeDeviceList.vue:14-31`（`<StatCard>` + `<CountUp>`），`NodeList.vue:14-26` |
| OperationButtons.vue | DeviceControlPanel + ActionForm + ConfirmationDialog | `views/edge-device/shared/DeviceControlPanel.vue` 存在，OperationButtons.vue 已删除 |
| useHistoryData 内嵌批量请求 | 提取为 composable | `frontend-shared/src/composables/useHistoryData.ts` 存在 |
| api client 401 分类 | v2.2 重构 isLoginAttempt/hasStoredSession | `frontend-shared/src/api/client.ts:63-71` |
| InitializeAdminForm | v2.2 完整保留 | `Login.vue:55-58` + `InitializeAdminForm.vue` 存在 |
| table-wrapper/table-scroll-hint | `.mobile-table-wrapper` 全局类 | `frontend-shared/src/styles/theme.css:378-393` |
| BmsProtectionGrid 手写网格 | StatusItemGrid 共享组件 | `BmsProtectionGrid.vue:3`（`:items="protectionItems"`） |
| CSV 导出 | 保留（改名 deviceTypeText） | `HistoryChartSection.vue:330-335` |
| dialog-mobile-constrained | 全局 `.el-dialog { max-width: 92vw }` | `frontend-shared/src/styles/theme.css` |
| main.go InitializeSystemFromEnvironment | startup_initialization.go（v2.2 演进） | `backend/internal/auth/startup_initialization.go` 存在 |
| config.go ControlConfig/DataRetention | main 存在 | `backend/internal/config/config.go:19,24-30,65-68,107-108` |
| EdgeDeviceDetailRouter shallowRef | main 用 ref + resolveSequence 防竞态，功能完整 | `EdgeDeviceDetailRouter.vue:44-56`（`const targetComponent = ref` + `resolveSequence`） |

## 3. 价值评估结论

### 3.1 分级标准

- **P0 功能缺陷**：用户可见的功能失效/回归，必须恢复
- **P1 明确缺陷**：移动端必现的可用性问题，建议恢复
- **P2 锦上添花**：移动端增强，可选
- **不恢复**：main 已有等价/更好实现，或收益≈0

### 3.2 分级结果

| 级别 | 项 | 理由 |
|------|----|------|
| **P0** | rememberMe → 后端 TTL | 记住我功能名存实亡，7 天 TTL 死代码 |
| **P1** | LineChart 双列 tooltip | BMS 16 电芯全选 tooltip 巨大不可用；main 无 formatTooltipTime，需从 6e598 提取（L2 修正：main LineChart:170 是 xAxis axisLabel formatter，非 tooltip 函数） |
| **P1** | TimeRangeSelector 480px 响应式 | 移动端自定义日期选择器溢出（实际行号 :19 非 :17） |
| **P1** | App.vue 字体缩放体系（**限定 ≤480px 档 + EP 组件字号**） | 移动端整体字体偏小；**保留 main 现有 16px 输入防缩放规则**（iOS 防聚焦放大，6e598 的 15px 档会覆盖它造成回归） |
| **P1** | Monitor descriptions 单列 | 窄屏描述挤压；**共 4 处** `:column="2"`（145/164/258/277），需全改 |
| **P1** | Login.spec.ts 测试补充 | 回归保护（429 限流/网络失败用例，main = 0，6e598 = 3） |
| **P1** | DataPanel 历史表移动端横向滚动 | main:196 裸 el-table（列宽 180px），375px 视口必溢出；6e598 的 mobile-table-wrapper 无等价物。注意 main 的 `v-if=queryForm.deviceId` 守卫合理，仅补表格包裹 |
| **P1** | CommandFrequencySection embedded | **升 P1**：main BmsDetailPage.vue:163 已将组件放入 el-collapse-item，el-card 嵌套（margin+shadow+重复表头）正是"collapse→card 嵌套层级"结构性移动端问题；**需同时改 CommandList.vue**（main 无 embedded prop/样式） |
| **P2** | notification-popover 移动端约束（**移回恢复清单**） | 全局 92vw 仅作用于 `.el-dialog`（theme.css:371-372），**不覆盖 el-popover**（MainLayout.vue:138 `:width="320"`），≤320px 视口通知弹层仍溢出 |
| **P2** | Inverter 3 组件 480px | inverter 页移动端访问较少；**注**：TempCard 用 auto-fill grid 已部分自适应、PowerFlow 有 max-width:600px，增益小于 MpptCard |
| **P2** | BmsMosStatus 双列 | 移动端 MOS 状态网格 |
| **P2** | DeviceInfoCard mobile-list | 移动端信息列表 |
| **P2** | BmsCellVoltageChart tooltip | confine+position 防溢出 |
| **不恢复** | RealtimeDataList 响应式 | main 当前 item-size 80 可用，收益存疑 |
| **不恢复** | EdgeDeviceDetailRouter shallowRef | main resolveSequence 已防竞态，收益≈0 |
| **不恢复** | PageHeader 纵向布局 | main 已有 768px 断点 + flex-wrap |
| **不恢复** | GaugeChart tooltip | **死组件**（main 无任何消费者，grep 0 命中）；且 6e598 用 `token()` 非 getChartTheme，恢复需额外适配反而踩 pitfall |
| **不恢复** | theme.css dialog-mobile-constrained | 已被全局 max-width:92vw 替代 |
| **不恢复** | DataPanel 无条件渲染 | main 的 `v-if=queryForm.deviceId` 更合理（仅表格包裹补回，见 P1） |

## 4. 合入方案（3 批 + 验证门禁）

### 批次 A：P0 功能修复（1 个 commit）

```
feat(auth): 恢复 rememberMe 传后端 — 7 天 TTL 分支重新生效
```

改动：
1. `frontend-shared/src/api/auth.ts`：`LoginRequest` 接口补 `rememberMe?: boolean`（纯增量、向后兼容，`auth.ts:35` 的类型约束链自动生效，client.ts 无需改）
2. `frontend-shared/src/stores/user.ts:31`：`authApi.login({ username, password, rememberMe })`（函数签名 :30 已有第三参，**仅需改调用处**）

调用点全扫描（无遗漏）：`authApi.login` 仅被 `userStore.login`（运行时）与测试调用；`user.spec.ts:58/71/79` 已传 3 参（测试先行适配），`api-wrappers.spec.ts:29` 传 2 参（rememberMe 可选，不受影响）。`Login.vue:175` → `userStore.login` 3 参匹配签名。

验证：
- `pnpm typecheck` + `pnpm test:run`（user.spec.ts 已传 3 参，恢复后自然通过）
- curl 模拟登录：带 `rememberMe: true` → JWT exp 应为 7 天后；不带 → 24h

### 批次 B：图表优化（2 个 commit）

```
B1: fix(charts): LineChart 恢复双列 tooltip（≤12单列/>12双列+降序+confine）
B2: fix(charts): TimeRangeSelector 480px 响应式
```

B1 改动要点：
- 从 `6e598b49:frontend-shared/src/components/charts/LineChart.vue` 提取 formatter 逻辑（formatTooltipTime + 双列 grid + 降序 + confine + position + "还有X项"）
- **替换目标明确**：main 的 LineChart 只有 `applyChartOption` 内联 formatter 一处（`LineChart.vue:217`），不存在第二套 formatter，直接替换该处即可
- **配色适配无需处理**：6e598b49 与 main 均使用 `getChartTheme()` + `getThemePalette()`（命名一致，无 token() 旧写法）
- BMS 16 series > 12 → 自动进入双列（阈值逻辑自洽）

B2 改动要点：
- 恢复 `custom-date-picker` 类 + `flex-wrap` + 480px 断点 radio 换行
- 保持当前 main 的 `v-model`/事件接口不变（组件契约未变）

验证：
- `pnpm test:run` + `pnpm typecheck`
- CDP 打开 BMS 页 → `__echartsInstance.dispatchAction({type:'showTip'})` 验证 16 个 series 双列显示

### 批次 C：移动端适配（3 个 commit）

```
C1: fix(frontend): 移动端字体缩放（≤480px档+EP字号，保留16px防缩放）+ Monitor descriptions isMobile
C2: fix(frontend): DataPanel 历史表横向滚动 + CommandFrequencySection/CommandList embedded 模式
C3: fix(frontend): inverter/BmsMosStatus/DeviceInfoCard 移动端适配 + notification-popover 约束 + Login.spec 测试补充（P2 可选项）
```

C1 改动要点：
- App.vue 恢复字体缩放：**仅 ≤480px 档 + EP 组件字号**（el-button/el-form-item/label/el-table 14px 档），**不得覆盖 main 现有 768px 输入框 16px 防缩放规则**（App.vue:60-67，iOS 防聚焦放大）。普通选择器，不用 `:deep()`（全局 `<style>` 中 :deep 无效）
- Monitor.vue **4 处** `:column="2"`（145/164/258/277）全改 `:column="isMobile ? 1 : 2"`，引入 `useResponsive`
- **预告**：`Monitor.spec.ts` 需同步补 `useResponsive` mock（该 spec 有 16+ mount 用例现无 mock，参考 NodeDetail 19 例失败教训）

C2 改动要点：
- DataPanel 历史表（:196 裸 el-table）包 `.mobile-table-wrapper` + `.mobile-table-hint`（theme.css 全局类已存在）
- CommandFrequencySection 恢复 embedded 模式（无卡片头/免 shadow/body padding 归零）**+ CommandList.vue 同步补 embedded prop/样式**

C3 改动要点（P2 可选）：
- 3 个 inverter 组件补 480px media query（MpptCard 优先，TempCard/PowerFlow 增益小可延后）
- BmsMosStatus 补 768px 双列 grid；DeviceInfoCard 补 isMobile mobile-list
- MainLayout.vue:138 el-popover 补 `popper-class="notification-popover"` + theme.css 恢复 `.notification-popover` 移动端 max-width 约束
- Login.spec.ts 补 429 限流/网络失败不计失败/URL redirect 3 用例（从 6e598 提取）

验证：
- `pnpm test:run` + `pnpm typecheck` + `pnpm build`
- CDP 375×812 逐页量测：无横向溢出、字体 ≥ 移动端标准、descriptions 单列、通知弹层不溢出

### 总验证门禁

1. `make test`（后端）+ `pnpm test:run` + `pnpm typecheck` + `pnpm build`
2. CDP 实机：BMS 页双列 tooltip（dispatchAction showTip）；375/390px 移动端逐页量测
3. rememberMe：curl TTL 验证 + 浏览器勾选"记住我"登录后刷新不掉线（24h 后理论仍有效）

## 5. 决策点（需少爷拍板）

1. **批次 C 范围**：只做 C1（P1），还是 C1+C2（含 P2 可选 3 项）？
2. **P2 清单**：GaugeChart/BmsCellVoltageChart tooltip、CommandFrequencySection embedded 是否纳入？
3. **合入方式**：逐 commit 直接上 main，还是开 feature 分支合入？
4. **RealtimeDataList 响应式**：暂不恢复（main 当前可用），确认？

## 6. 评审要求

- 逐条核验 §2 现状核查的源码事实（read_file/search_files 验证行号）
- 检查价值评估是否合理（P0/P1/P2 分级）
- 检查合入方案的可执行性与风险（依赖、冲突、验证完备性）
- 检查是否有遗漏的丢失增量或过度恢复
- 输出三档结论：APPROVED / APPROVED_WITH_COMMENTS / REJECTED + 严重度分级缺陷清单（CRITICAL/HIGH/MEDIUM/LOW + 证据行号）

## 7. 评审记录

### 7.1 第一轮 review（2026-08-09，双 subagent 并行）

**Reviewer A（源码事实 + 后端正确性）**：APPROVED_WITH_COMMENTS

| ID | 声称的缺陷 | 主 Agent 核实 | 处置 |
|----|-----------|--------------|------|
| C1 | user.ts 签名缺 rememberMe 参数，需改 3 处 | ❌ 误读：签名 `user.ts:30` 已有第三参，仅需改调用处（2 处） | 驳回（已核实签名） |
| H1 | Login.vue 传 3 参被静默丢弃 | ❌ 误读：签名匹配，无丢弃 | 驳回 |
| H2 | LineChart 有两套 formatter | ❌ 误读：main 仅 `LineChart.vue:217` 一处 | 驳回（grep 证实） |
| M1 | user.spec.ts 传 3 参无法 typecheck | ❌ 不成立：`vue-tsc --noEmit` 实测零错误 | 驳回（实测） |
| M2 | BMS 16 series 自动双列自洽 | ✅ 有效 | 已补入 §4 B1 |
| M3 | 6e598 与 main 均用 getChartTheme，无 token 适配 | ✅ 有效 | 已补入 §4 B1 |
| L1-L4 | 行号微调 | ⚠️ 部分有效（30/31 行辨析） | 已修订 §2.1/§4 |
| — | 调用点全扫描（无遗漏，2 处改动即可） | ✅ 有效且有价值 | 已补入 §4 批次 A |

**Reviewer B（前端交互 + 移动端可行性）**：APPROVED_WITH_COMMENTS（27 次工具调用深度核查）

| ID | 声称的缺陷 | 主 Agent 核实 | 处置 |
|----|-----------|--------------|------|
| H1 | notification-popover 判"不恢复"错误（92vw 不覆盖 el-popover） | ✅ 属实：theme.css:371-372 仅 `.el-dialog`，MainLayout:138 为 el-popover :width=320 | 采纳：移回 P2 恢复清单，批次 C3 |
| H2 | DataPanel 历史表横向滚动漏判 | ✅ 属实：main:196 裸 el-table 列宽 180px，375px 溢出 | 采纳：升 P1，批次 C2 |
| H3 | App.vue 恢复字体会覆盖 16px 防缩放 | ✅ 属实：App.vue:60-67 iOS 防聚焦注释明确 | 采纳：限定 ≤480px 档 + EP 字号，保留 16px 规则 |
| H4 | Login.spec 429 用例无落地批次 | ✅ 属实：main=0，6e598=3 | 采纳：并入批次 C3 |
| M1 | GaugeChart 死组件 + token() 适配 | ✅ 属实：main 无消费者；6e598 用 token() 非 getChartTheme | 采纳：并入"不恢复" |
| M2 | Monitor.spec 需补 useResponsive mock | ✅ 合理（skill 有 NodeDetail 19 例教训） | 采纳：C1 预告 |
| M3 | Monitor 有 4 处 column=2 非 1 处 | ✅ 属实：145/164/258/277 | 采纳：C1 全改 |
| M4 | CommandFrequencySection 需同时改 CommandList | ✅ 属实：main CommandList 无 embedded | 采纳：C2 补 CommandList |
| L1 | Inverter 三组件增益不等 | ✅ 属实：TempCard auto-fill 已自适应、PowerFlow 有 max-width | 采纳：P2 注明 MpptCard 优先 |
| L2 | main 已有 formatTooltipTime 无需提取 | ❌ 误读：main LineChart:170 是 xAxis axisLabel formatter，非 tooltip 函数；formatTooltipTime main 中不存在 | 驳回（已核实） |

> **第一轮 review 汇总**：双路 APPROVED_WITH_COMMENTS；Reviewer A 的 4 个 CRITICAL/HIGH 全被证伪（误读），Reviewer B 的 4 个 HIGH 全部属实且高价值（其中 H1 纠正了我方案自身的误判）。评审闭环确认了 3 个原方案盲点（notification-popover 作用域、DataPanel 表格、App.vue 16px 冲突）。所有修订已合入 §3/§4。按收敛规则（双路 APPROVED_WITH_COMMENTS 且修订已并入），剩余意见视为闭环，不再派下一轮。
