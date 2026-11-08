# 前端审核报告（task-13 / 逻辑设备移除 + 新入口）

> **审核者**：独立审核员（非实现者）
> **审核对象**：本轮前端改动（`git status --porcelain` 实测：M 44 / D 8 / ?? 3）
> **代码基线**：工作区工作树（未提交），diff 基准 `HEAD`（前端改动前）
> **后端实测环境**：`ehome-verify-srv`（容器内 8080，连 `ehome_verify` 库）
> **本机库**：**未触碰 `ehome` 库**；仅用 `ehome_verify`（只读查询 + 只读 GET / 无害 POST）
> **生产**：**未触碰**
> **复跑时刻**：2026-10-24 10:29–10:39（Asia/Shanghai）
> **证据等级**：T=实测 / C=代码 / D=文档

---

## 结论

# ⚠️ 有条件接受（需先修 1 个 P0）

**移除本身是干净的**：`dist` 里 `逻辑设备` / `logical-device` 均为 **0** 命中（T），5 项复跑全绿（T），死引用只剩注释（T），7 处“分母缩小”**逐条复核后全部成立**（T+C），UX 三处（换节点/恢复/合并）实现质量高于要求（T+C）。

**但存在 1 个 P0：`data-summary` 的前端契约与后端真实返回不一致，导致两个弹窗对用户讲错话 —— 其中一个还谎报“0 行”。**

这不是风格问题，是“**测试全绿 + 用户看到错数字**”的组合，属于本仓一贯最警惕的假绿。**必须先修 P0 再合并。**

---

## P0 级发现（必须修）

### P0-1 `data-summary` 前端读 `total_rows`，后端从不返回该字段

**级别**：**P0**（用户可见的数字错误 + 谎报 0 行；且现有测试用 mock 掩盖了它）

**证据链（全部可复跑）**：

1. **后端真实返回没有 `total_rows`**（T，curl 实测原文）：
```bash
curl -s -H "Authorization: Bearer $JWT" \
  "http://127.0.0.1:8080/api/v1/edge-devices/1/data-summary"
=> {"code":200,"data":{"items":[{"device_name":"光学雨量计",
     "first_time":"2026-09-20T08:18:24.827456Z",
     "last_time":"2026-10-01T06:56:53.69482Z",
     "rows":352579,"source_id":1}]},"message":"ok"}
```
   后端只发 `{"items": [...]}`（C: `handler_edge_device.go:1268` `Success(c, gin.H{"items": items})`）。
   **全仓后端 `grep total_rows` = 0 命中**（T）。

2. **前端类型声明了该字段**（C: `frontend-shared/src/api/edgeDevice.ts:138` `total_rows: number`），
   并在归一化时把它兜底成 **0**（C: `:436`）：
```ts
   total_rows: typeof data?.total_rows === 'number' ? data.total_rows : 0,
```
   ⇒ 后端真实数据经过这一层后，`total_rows` **恒为 0**。

3. **实测该归一化结果**（T，只 mock HTTP 层、让真实 wrapper 运行的探针）：
```
=== getDataSummary RESULT ===
{ "items": [ { "rows": 352579, ... } ], "total_rows": 0 }
rows of item[0] = 352579
```
   ⇒ `items[0].rows` 正确（352579），但 `total_rows` = **0**。

**造成的用户可见后果（T，均用真实组件 + 真实归一化后数据渲染）**：

| # | 位置 | 用户看到 | 应为 | 严重性 |
|---|---|---|---|---|
| A | 合并历史弹窗（`DeviceMergeHistoryDialog.vue:56`） | `预计迁移 **0** 行`，而下方明细表同一批源显示 `352579` | `预计迁移 352,579 行` | **P0**：同一弹窗内自相矛盾，且“0 行”暗示“没什么可搬”，而这是**不可逆**操作 |
| B | 删除确认弹窗（`DeviceDeleteDialog.vue:139/142`） | `数据量估算：约 **0** 条` | `约 352,579 条` | **P0**：删除不可逆，把“百万行将被删”报成“0 条” |

实测原文（A）：
```
=== A) TEXT ===
... 源设备 ID... #2 模拟BMS **预计迁移 0 行** ← 左右滑动查看完整表格 →
 1 光学雨量计 **352579** 2026-09-20T... 2026-10-01T... 取消 确认合并
```
实测原文（B）：
```
data-info exists = true
TEXT = "删除边缘设备... 本设备历史数据 数据量估算：**约 0 条** 保留期：系统级（默认 90 天）..."
```

**为什么测试没抓到（本条最关键的证据）**：现有测试**自己捏造了后端不存在的字段**，于是“测的是 mock，不是后端”：
- C: `components/device/__tests__/DeviceDeleteDialog.spec.ts:86` `deferred.resolve({ items: [...], total_rows: 1200 })` ⇒ 断言 `约 1,200 条` 通过
- C: `api/__tests__/api-wrappers.spec.ts:830-836` `const summary = { items: [...], total_rows: 10 }` ⇒ `expect(res.total_rows).toBe(10)` 通过
- 讽刺的是：`DeviceDeleteDialog.spec.ts:98-105` 这条用例**名为**「估算降级（total_rows 缺失）时不显示数据量区，不伪造 0 条」，它传 `{ items: [] }`（空数组）。
  但**真实后端的降级不是空数组** —— 是 `items` 有行、`total_rows` 缺失。该测试用“空 items”绕开了真实降级路径，所以它**声称守住的不变量实际没被守住**。

⇒ 这是本仓反复强调的“**假绿**”：191 文件 / 2298 用例全绿（T），但用户看到的数字是错的。

**建议修法（二选一，推荐 ①）**：

① **前端改读 `items` 汇总**（最小改动，不动后端）：
```ts
// api/edgeDevice.ts:434-437
const items = Array.isArray(data?.items) ? data.items : []
return {
  items,
  // 后端不返回 total_rows：由 items[].rows 求和（null 视为未知，不参与求和）
  total_rows: items.reduce((s, it) => s + (typeof it.rows === 'number' ? it.rows : 0), 0),
}
```
   同时把 `EdgeDeviceDataSummaryItem.rows` 改为 `number | null`（后端确实会发 `null`，见 P1-1）。

② 后端补 `total_rows`（改 `handler_edge_device.go:1268`）。**不推荐**：多一个需两边同步的冗余字段，而“合计”本就是纯展示派生量。

**无论选哪个，必须同时**：把 `DeviceDeleteDialog.spec.ts:86`、`api-wrappers.spec.ts:830` 的 mock 改成**真实后端形状**（`items` 有行、无 `total_rows`），
并新增一条“有行但无 total_rows 时不得显示 0 条”的用例。否则修完仍是假绿。

---

## P1 级发现

### P1-1 `rows` 可空但类型声明不可空，模板直接 `.toLocaleString()`

**级别**：**P1**（潜在渲染异常；未在真实浏览器复现，故不升 P0）

**证据**：
- 后端会发 `null`：C: `backend/internal/api/handler_edge_device.go:1224` `item := gin.H{"source_id": deviceID, "rows": nil, ...}` —— 只有 `EstimateDeviceRows` 成功时才覆盖为数字（`:1230-1232`）。估算超时/失败 ⇒ 保持 `nil` ⇒ JSON `null`。
- 前端类型声明为**不可空**：C: `api/edgeDevice.ts:130-131` `rows: number`（无 `| null`）。
- 模板直接调用：C: `DeviceMergeHistoryDialog.vue:67` `{{ asSummaryItem(row).rows.toLocaleString('zh-CN') }}`

**实测**：
- 纯表达式层面 `null.toLocaleString()` **抛 TypeError**（T）：
```
RESULT = [RENDER THROWS] Cannot read properties of null (reading 'toLocaleString')
```
- 但在 **happy-dom 挂载真实组件**时该单元格渲染成字面量 `null` 且**未捕获异常**（T）：
```
first row tds = ["","","","","2 BMS null null null"]
errors = []
```
  （三处 `null` 分别对应 rows/first_time/last_time。）

**为什么仍要修**：happy-dom 与真实浏览器的模板错误处理不同。即使不抛异常，用户看到 **`null` 字符串**也是缺陷。
**建议**：类型改 `number | null`，模板用 `row.rows == null ? '—' : row.rows.toLocaleString('zh-CN')`（与 `DeviceRestoreDialog.vue:101-103` 的既有正确范式一致 —— 那里对 `row_estimate` 已这么做）。

> **诚实标注**：我没能证明它在真实浏览器里会崩。按“防御性正确性 + 已知可抛表达式”记 P1，不夸大。

### P1-2 `LoginAuthState.spec.ts` 注释中的技术论断**事实错误**

**级别**：**P1**（不是断言错误，而是**写进代码的误导性依据**，会让下一个维护者做出错误决策）

**原文**（C: `frontend-shared/src/views/auth/__tests__/LoginAuthState.spec.ts:17-18`）：「这里把同一预算补到**其余三条**上（**vitest 5 无文件级 setConfig(testTimeout)**，官方形态是逐条 options）」

**实测反驳**（T）：`vi.setConfig({ testTimeout })` 在 vitest 5.0.5 下**确实生效**：
```
# 探针：vi.setConfig({testTimeout: 10}) 后跑一个睡 60ms 的用例
× sleeps 60ms with testTimeout=10 12ms
Error: Test timed out in 10ms.
=> 证明 setConfig 全局生效
```
此外 `typeof vi.setConfig = function`，且 `describe(..., { timeout: 15000 }, fn)` 亦被接受（T）。

**为什么算问题**：改动**方向本身是对的**（4 条同源用例统一预算，比逐条补更好），断言确实一字未改（比对过 diff，T）。但它把“我选择这么做”包装成“**做不到别的做法**”，这是**错误的可推翻性**：下一个人想改成文件级设置时会被告知“vitest 不支持”从而放弃。
**建议**：改为「本文件统一用逐条 options 表达（等价做法：文件顶部 `vi.setConfig({ testTimeout: 15000 })` 或 `describe` 级 options，二者在本仓 vitest 5.0.5 实测均生效）；此处选逐条是为了让超时与具体用例可见地绑定」。

### P1-3 `dataSource.ts` + `stores/dataSource.ts` + 后端 9 条路由成了**不可达孤儿**

**级别**：**P1**（作为“遗留事实”记录；**与 task-H 相关，我只陈述事实不越界裁决**）

**证据**（T）：
```bash
# 全仓 .vue 里没有任何组件使用该 store
grep -rn 'useDataSourceStore' frontend-shared/src --include=*.vue   # => 0 命中
# 唯一 import 者是它自己的 store
grep -rn 'stores/dataSource|api/dataSource' frontend-shared/src --include=*.vue --include=*.ts | grep -v __tests__
# => 只有 stores/dataSource.ts 自己 import api/dataSource
```
- 前端 `api/dataSource.ts` 暴露 **9 个方法**（list/get/create/update/remove/activate/deactivate/reset/getHealth/getFailoverLogs）。
- 后端同名 9 条路由**仍然注册**（C: `handler_data_source.go:50` 注释「注册 /api/v1/data-sources 下的 9 条契约路由」）。
- 承载它的页面 `views/data-source/DataSourceList.vue` 已被删除（`git status` = `D`）。

**事实陈述**：这是一组“**前端无入口、后端仍开放**”的能力。它是否该删（还是留给未来/外部脚本），属于 `data_sources` 表的语义裁决，**归 task-H**。我只确认：**本轮前端没有为它保留任何可达入口，也没有在删除页面时同步清理 API 层**。

---

## P2 级发现

### P2-1 `预计迁移 N 行` 的措辞与“估算”语义不符

C: `DeviceMergeHistoryDialog.vue:56` 写 `预计迁移 X 行`，而后端该数字是**估算**（C: `handler_edge_device.go:1170-1171` 注释明说“数据量估算（不精确计数）”，来自 reltuples/EXPLAIN）。
实测证明估算会**离谱**：对**不存在**的 device 999 后端报 `rows: 20`，而实际是 **0 行**（T）：
```
curl ".../data-summary?source_ids=999,888"
=> {"items":[{...,"rows":20,"source_id":999},{"...","rows":20,"source_id":888}]}
psql "select count(*) from unified_data where device_id=999" => 0
EXPLAIN SELECT id FROM unified_data WHERE device_id = 999; => rows=10（分区各报 1-2）
```
即：**PG 规划器对“无行”的等值查询也给下限估计**（~10 行/表，含 device_data 后 ~20），不是 0。
**建议**：文案改「预计迁移约 N 行」；或对极小 rows 标注“估算可能不准”。（不阻塞 P0 修法，但解释了修完 P0 后用户仍可能看到“约 20 行”却实际搬 0 行。）

### P2-2 `row_estimate` 与 `rows` 两个名字指同一件事

`deleted` 端点用 `row_estimate`（C: `handler_edge_device.go:1437`），`data-summary` 用 `rows`（`:1224`）。两者同为 `EstimateDeviceRows` 结果。
前端分别对应 `DeletedEdgeDevice.row_estimate`（`api/edgeDevice.ts:116`）与 `EdgeDeviceDataSummaryItem.rows`（`:130`）。**建议**统一命名（非阻塞）。

---

## 逐条审核点结论

| # | 审核点 | 结论 | 关键证据 |
|---|---|---|---|
| 1 | 类型/路径与后端一致 | **❌ 不通过（P0-1 + P1-1）** | `total_rows` 后端不存在；`rows` 可空却声明不可空 |
| 2 | 原地换节点的 UX | **✅ 通过（质量高于要求）** | 见 §2 |
| 3 | 恢复设备弹窗 | **✅ 通过** | 见 §3 |
| 4 | 合并历史弹窗 | **⚠️ 部分通过**（逻辑正确，数字错） | `rows_migrated=0` 文案正确（T）；但预览行数是 0（P0-1） |
| 5 | 死引用 | **✅ 通过（仅注释）** + P1-3 事实记录 | 7 处非测试命中**全是注释**（T） |
| 6 | 7 处分母缩小 | **✅ 通过（逐条复核，无一为放宽）** | 见 §6 |
| 7 | LoginAuthState 4×timeout | **⚠️ 改动合理，但依据写错（P1-2）** | `vi.setConfig` 实测生效（T） |

### §2 换节点 UX（审核点 2）—— 三问三答，全部正确

1. **切节点是否强制清空已选通道？** ✅ 是。C: `EdgeDeviceList.vue:1574-1582` `handleEditNodeChange()` 第一句即 `deviceForm.channel_id = null`，并按新节点重拉通道（`:1579`）。注释解释这是**语义要求**而非体验优化（旧通道必然不属于新节点 ⇒ 必被 400）。
2. **只改名字会不会误提交 channel_id 导致意外改绑？** ✅ 不会。C: `:1590-1593` 打开时回填**当前** `channel_id`；用户不动 ⇒ 提交同一个值 ⇒ 后端校验通过、绑定不变。
3. **编辑时未选通道会不会 400？前端有没有拦截？** ✅ 双保险：表单规则 `channel_id: [{required:true,...}]`（C: `:969`）+ 提交前显式拦截（C: `:1746-1750`）。

另有一个**我原本没预期到的正确性细节**（C: `:1739-1741, 1756-1758`）：`hardware_id` 只在“用户真的改了地址”时才进请求体，否则该 key **完全不出现** —— 堵住了“打开编辑框→点保存就把总线名写回地址字段”的老坑。加分项。

### §3 恢复设备（审核点 3）—— 三问三答，全部正确

1. **`row_estimate=null` 显示「—」而非「约 0 条」？** ✅ 是。C: `DeviceRestoreDialog.vue:100-103` 显式判空渲染 `—`，注释写明“null = 估算不出来，**不是 0 行**”。（对照：`DeviceDeleteDialog` 恰恰在此出错 —— 见 P0-1 表格 B 行。**同仓库、同语义、两种写法**，是很有说服力的对照证据。）
2. **失败是否有错误态而非伪装空列表？** ✅ 是。C: `:20-25` `v-if="error"` 渲染 `el-alert` + 重试按钮；`:175-177` catch 写 `error.value`。三态互斥（`:27` loading / `:31` v-else）。
3. **恢复成功后列表真的刷新？** ✅ 是。C: `:236` `emit('restored', row.id)` → 父组件 `EdgeDeviceList.vue:879-883` `invalidateLists()` + `invalidateDetail()` + `await fetchDevices(true)`；弹窗自身 `:238` 也 `await load()`（双保险）。

### §6 7 处分母缩小 —— 逐条复核（最容易被“只看注释”骗过的地方）

**我的判据**：不读注释，而是**验证“被移除条目对应的文件是否真的不存在/不在路由上”，以及替换进去的页面是否真的存在且真的被审**。

| # | 位置 | 改动 | 我的核实 | 判定 |
|---|---|---|---|---|
| 1 | `menuModel.ts:43/92` | 导航 14→13，删 `/logical-device` | `router/index.ts` 已无该 path（T: `grep logical-device src/router/index.ts` 0 命中） | ✅ 如实 |
| 2 | `uiuxRouteCoverage.spec.ts:266` | ≥15 → ≥14 | 路由页确实少 1；`toBeGreaterThan(0)` 跑空守卫仍在（C: `:263`） | ✅ 如实 |
| 3 | `listPageConventions.spec.ts:521` | ≥11 → ≥10 | `LogicalDeviceList.vue` 已删（T: `ls src/views/logical-device/` 不存在）；判定器代码未改（diff 仅分母与条目） | ✅ 如实 |
| 4 | `listPageConventions.spec.ts:535` | 条目换页 | 换页而非删条目，分母不缩水 | ✅ 如实 |
| 5 | `MobileTableWrapper.spec.ts:184-190` | 删 2 条、补 2 条 | **补的是真实页面**：`SourceGroupList.vue` 存在且经 `DataSourcePage.vue:30` `<SourceGroupList embedded />` 可达（T）；`OverviewTab.vue` 经 `:16` 可达（T）。**净分母不变** | ✅ 如实（负责任的替换，不是删条目） |
| 6 | `FixedColumnOcclusion.spec.ts:203-227` | 被测页 `DataSourceList` → `SourceGroupList`，宽 330→300 | `SourceGroupList.vue:124` 实测就是 `width="300" :fixed="isMobile ? false : 'right'"`（T）；判据一字未改 | ✅ 如实 |
| 7 | `glossaryTermsGate.spec.ts:41-48` | 13 → 11 | `git show HEAD:...LogicalDeviceList.vue \| grep -c '设备类型'` = **2**（T），与“该文件贡献 2 处”吻合；该门禁是 **fail-closed 精确计数**（`:110-116` `actual !== entry.count` 即红），填错数字测试必红 —— **它通过了，就证明 11 是实测值**（T） | ✅ 如实 |

**总结论**：7 处**全部**属于“页面确实被删 ⇒ 分母必须如实缩小”，**没有发现一处是为了让测试变绿而放宽断言**。
证据不是注释，而是：被删对象确实不存在（3 处 ls/grep 实测）、替换对象确实存在且可达（2 处组件挂载点）、门禁本身是 fail-closed 精确匹配（1 处）、判定器逻辑未改动（diff 逐行比对）。

> **反向检验**：若这 7 处有一处是“为了让测试变绿”，最可能藏在 #5（换页）和 #6（换页 + 改宽度 330→300）。我专门查了这两个：替换页面 `SourceGroupList.vue` **确实在路由链路上**（`DataSourcePage` 第二个 Tab），且其操作列**确实是 300px 且确实是响应式 fixed** —— 与门禁断言完全对应。**不是凑数。**

---

## 误报清单（我一开始怀疑、核实后确认不是问题）

| 我的初始怀疑 | 核实结果 | 结论 |
|---|---|---|
| “删了页面但 `DataSourcePage` 还在引用它” | `DataSourcePage.vue:66` 引用的是 `SourceGroupList`，不是已删的 `DataSourceList` | 误报 |
| “通知深链删分支会跳 404” | C: `MainLayout.vue:899-905` —— 两个 source（`merge_failed`/`retention_expiring`）的生产者已随逻辑设备下线（后端 grep = 0 命中），落回离线规则；注释交代了“历史遗留通知不会跳到 404” | 误报（设计合理） |
| “合并历史源选择没排除目标自身” | C: `DeviceMergeHistoryDialog.vue:40` `:disabled="d.id === targetId"`；`:134` `candidateDevices` 已 filter，双保险 | 误报 |
| “恢复弹窗成功后列表不刷新” | 见 §3 第 3 条（双保险） | 误报 |
| “`rows_migrated=0` 会谎报成功” | C: `:187-193` 显式分支：0 走 `ElMessage.info('没有需要迁移的数据...')`。**实测** `merge-history` target==source 返回 `rows_migrated:0`（T），前端走 info 分支 | 误报（实现正确） |
| “`el-table` 的 `null` 单元格会让整个弹窗崩” | happy-dom 下未捕获异常（T），只渲染出 `null` 字面量 | 降级为 P1-1（不夸大） |

---

## 复跑输出原文

```
$ pnpm test:run
 Test Files  191 passed (191)
      Tests  2298 passed | 1 skipped (2299)
   Duration  87.45s

$ pnpm typecheck
$ vue-tsc --noEmit
TYPECHECK_EXIT=0

$ pnpm build
✓ built in 2.23s
BUILD_EXIT=0

$ grep -rl '逻辑设备' dist/assets/*.js | wc -l
0
$ grep -rl 'logical-device' dist/assets/*.js | wc -l
0
```

> ⚠️ 注意：**5 项复跑全绿，但 P0-1 依然成立。** 这正是“绿 ≠ 对”的实例 —— 绿是因为测试 mock 了后端不存在的字段。

---

## 复跑清单（每条可直接执行）

```bash
# P0-1 核心证据：后端不返回 total_rows
curl -s -H "Authorization: Bearer $JWT" "http://127.0.0.1:8080/api/v1/edge-devices/1/data-summary"
# => data 里只有 {"items":[...]}，无 total_rows

# P0-1 前端兜底成 0
grep -n 'total_rows' frontend-shared/src/api/edgeDevice.ts
# => 138: total_rows: number   /  436: ... : 0,

# P0-1 测试自造字段（假绿来源）
grep -n 'total_rows' frontend-shared/src/components/device/__tests__/DeviceDeleteDialog.spec.ts

# P1-1 rows 可空
sed -n '1224p' backend/internal/api/handler_edge_device.go   # rows:nil
sed -n '130,131p' frontend-shared/src/api/edgeDevice.ts      # rows: number（不可空）

# P1-3 孤儿 API
grep -rn 'useDataSourceStore' frontend-shared/src --include=*.vue   # 0 命中

# 审核点 6 逐条核实
git show HEAD:frontend-shared/src/views/logical-device/LogicalDeviceList.vue | grep -c '设备类型'  # 2
ls frontend-shared/src/views/logical-device/ 2>&1                                                  # 不存在
grep -n 'el-table-column' frontend-shared/src/views/source-group/SourceGroupList.vue | tail -2     # 操作列 300

# 审核点 7
# vi.setConfig({testTimeout:10}) + 睡 60ms 的用例 => 实测超时（证明 setConfig 生效）
```
