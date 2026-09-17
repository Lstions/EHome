# 实测发现的问题登记（2026-09-17）

> 来源：本机生产栈（`ghcr.io/lstions/ehome:latest`，revision `6d99dbb3`）+ 真实 ESP32-C6
> 台架（UART0 模拟器 / UART1 真实雨量计）上的**浏览器黑盒操作**与**数据核对**。
> 每条都附**复现方式**与**判定依据**，不含推测。

---

## P1 — 删除对话框「通道」标签串错（会误导删除确认）

**现象**：删除边缘设备的确认框里，「通道」显示 `UART 1`；被删设备实际在 **UART0**。

**根因**：`frontend-shared/src/components/device/DeviceDeleteDialog.vue:99`

```js
const parts = [props.device.hardware_type?.toUpperCase(), props.device.hardware_id]
```

取的是 **edge_device 顶层**字段：
- 顶层 `hardware_type` 为**空**；
- 顶层 `hardware_id` 语义是 **Modbus 从站地址**（雨量计 = `"1"`），**不是总线名**。

⇒ 拼出 `UART 1`，形似"UART1 总线"，实为"UART + 地址1"。若设备正好在 UART1 且地址为 1
（如真实雨量计）会**碰巧显示对**，掩盖该缺陷。

**正确来源**：关联的 `device.channel.hardware_id`（实测该字段为 `"UART0"` / `"UART1"`）。

**复现**：`node deploy/prod/verify-channel-label-bug.mjs`（只读：打开确认框后取消）。

**影响**：用户正是靠该栏核对"删的是哪个设备、挂哪条总线"，错误总线名可导致删错对象。

---

## P2 — 数据面板「数据 / 原始数据」两列恒显示 `—`

**现象**：数据面板历史表两列都是 `—`，但接口确实返回了数据。

**根因**：字段名不匹配。前端读 `row.parsed_data || row.data` 与 `row.raw_data`
（`frontend-shared/src/views/data/DataPanel.vue:214,219`），
而 `GET /api/v1/edge-devices/:id/data` 只返回 `data_json`（无 `parsed_data`/`data`/`raw_data`）。

实测接口返回键：`created_at, data_json, device_id, id, logical_device_id, node_id, timestamp`。

**与本次改动无关**：`DataPanel.vue` 最后变更为 `606d8e6f`(2026-09-16)，本次提交未触碰。

---

## P3 — 删除确认框缺少"设备在哪个通道"的可核对硬件信息（设计建议）

在同一对话框里，"通道"一栏既可能显示错误值（P1），也不显示通道的**真实硬件 ID**
（`UART0`/`UART1`）与 `bus_config`。建议 P1 修复时一并补齐。

---

---

## P0 — OTA 创建任务必失败：`ota_tasks.collector_id` 死列（已修，见提交）

**现象**：点「OTA 升级 → 开始升级」必失败，红字直接暴露 SQL：

```
升级失败
错误: failed to create task: ERROR: null value in column "collector_id"
of relation "ota_tasks" violates not-null constraint (SQLSTATE 23502)
```

**根因**：v2.3 把 `OTATask.CollectorID` 改名为 `NodeID` 后，代码里已无 `collector_id`
的读写者，但 **PostgreSQL 不会因 AutoMigrate 删列** —— 老库里该列仍是 `NOT NULL`，
而 INSERT 只写 `node_id` ⇒ 违反非空约束。**OTA 功能整体不可用**。

**为什么单测没拦住**：测试默认跑 **SQLite 内存库**（`testutil.OpenTestDB`），
SQLite 按模型现建表，压根没有这列；只有**存量 PG 库**才漂移出该列。
即"模型自洽、代码自洽，唯一的问题是旧库 schema 漂移"。

**修复**：`backend/internal/database/migrate_ota_collector_id.go` 新增幂等迁移
（`ALTER TABLE ota_tasks DROP COLUMN collector_id`），挂在 `AutoMigrate` 尾部，
与既有 `RetireLegacyRollup1m` 等并列。

**验证**：
- 单测 `TestMigrateOTATaskDropLegacyCollectorID*` 先造"带死列的旧表"，
  并**前置证伪**（不删列时该 INSERT 必须失败）；变异自证：把迁移改成空操作 → 必红。
- 真实 PG 隔离栈：造出带 `collector_id NOT NULL` 的旧表 → 启动后端 →
  日志 `[migrate] dropped legacy column ota_tasks.collector_id` → `POST /ota/tasks`
  由 **500 → 201**。

**生产处置**：手工执行等价的 `DROP COLUMN`（立即恢复），后续镜像自带该迁移。

**附带发现**：`ALTER TABLE ... DROP COLUMN` 会让 PG 的**预备语句失效**，
后端出现 `cached plan must not change result type (SQLSTATE 0A000)`，
OTA 进度查询与超时扫描连续报错。**重启后端**（新连接）即恢复 —— 属连接级缓存，
非数据损坏。

---

## P4 — OTA 表单的固件信息弹层文字竖排（视觉缺陷）

**现象**：固件信息区（文件名/大小/MD5/更新日志）表格将 1.35 MB 显示为
**"1.35 / MB" 换行**，MD5 长串挤压；"文件大小""更新日志"等 label 竖排。

**位置**：`frontend-shared/src/components/forms/OTAForm.vue:38`
`<el-descriptions :column="2" border size="small">` —— 两列布局 + 超长 MD5 值，
在弹层宽度受限时把列压到极窄。

**建议**：固件信息区改单列（`:column="1"`），或对 MD5 值加等宽字体 + 可换行/省略，
避免整表被一个 64 字符哈希撑爆。

---

## P5 — OTA 历史 TAB 看不到进度（与"进度条"相关的观察）

**现象**：节点详情「OTA 历史」TAB 中看不到本次升级的进度文案/进度条；
只有点「OTA 升级」在弹出的表单里才有「升级状态」进度条与时间线。

**客观记录（未判定为缺陷）**：本次实测「升级状态」进度条**渲染正常**，
且与设备真实进度一致 —— 后端日志逐级 `downloading 0→10→…→100%` →
`installing 100%`，任务终态 `success`（设备重启后由
`OTA ... auto-completed via Hello` 补齐终态，属自愈设计）。

**待确认**：历史 TAB 是否**设计上**就不显示实时进度（实时进度只在升级表单内）。
若是，则非缺陷；若期望历史 TAB 也显示，则需接线 WS `ota_progress`。

---

## P6 — OTA 历史表显示一行脏数据，真实记录不可见（**已修**）

**现象**（浏览器截图取证）：设备 2.5.21 升级**成功**（DB `status=success, progress=100`），
但节点详情「OTA 历史」表只显示**一行全空**：升级版本 `—`、状态徽标灰块、进度 `0%`、
开始/完成时间均 `—`。

**根因**：响应信封多套了一层。

| 层 | 值 |
|---|---|
| 后端 `getNodeOTAHistory` | `Success(gin.H{"data": tasks})` |
| 实际 JSON | `{code, data: {data: [...]}, message}` |
| API 层 `getOTAHistory`（改前） | `return response.data` ⇒ 拿到**对象**而非数组 |
| `otaHistory.value` | 该对象 |
| `el-table :data` | 把对象当"一行"渲染 ⇒ 脏行 |

**修复**：`frontend-shared/src/api/node.ts` 解包 `response.data.data`，
并**同时兼容**扁平数组形态（防后端将来扁平化后再次回归）。

**验证**：`src/api/__tests__/ota-history.spec.ts` 3 例（嵌套解包 / 扁平兼容 / 永不返回非数组）；
变异自证：改回 `return response.data` ⇒ 2 例 FAIL；恢复 ⇒ PASS。

---

## P7 — StringValue 字符串型读数未纳入展示（**已修**）

**这是什么问题**：有些传感器的**真值根本不是数字**。

后端 `drivers.SensorData`（`backend/internal/drivers/registry.go:13`）有两个值字段：

```go
Value       float64 `json:"value"`
StringValue string  `json:"string_value,omitempty"`   // 硬件版本、序列号等
```

`jiabaida_parse.go` 里两条指令的真值**只在 StringValue**，而 `Value` **恒为 0**：
- `parse0x05` → `{Name:"hardware_version", Value:0, StringValue:string(data)}`
- `parse0xA2` → `{Name:"serial_number", Value:0, StringValue:...}`

驱动单测是铁证（`jiabaida_test.go:677`）：帧 `DD 05 00 03 'V' '1' '9'` 解析出
`StringValue == "V19"`，同时 `Value == 0`。

**缺陷**：前端只取 `s.Value`（`useDeviceData.ts:119`）⇒ 硬件版本显示成
**`hardware_version: 0.00`**。这比"不显示"更危险：`0` 看起来像一个**真实读数**，
用户不会怀疑；而 `—` 至少暗示"取不到"。属**假绿**同族（用无意义的默认值冒充真值）。

**正确范式仓里就有**：`DeviceControlPanel.vue:76` 的 `value.string_value || value.value`。

**修复**：
- 新增 `asDisplayValue()`：`StringValue` 非空优先，否则 `Value`（刻意不做 `Number(str)` 强转）；
- `RowData` 拆成两个视图：`values`（展示，可为字符串）+ `numbers`（**纯数值**，
  供统计卡/趋势图）；
- `parseRowData` 同时产出两者；`useDeviceData.ts` 同步修正。

**为什么必须拆两个字段**：若让字符串读数进趋势/统计，`"V19"` 会被当成 0
⇒ 拉低均值、在趋势图上画出一条掉到 0 的**假线**。

**验证**：`DataPanelRowData.spec.ts` 新增 P7 组 5 例（含"numbers 只含数值型"）；
变异自证：改回只取 `Value` ⇒ 3 例 FAIL，报 `expected +0 to be 'V19'`（精确复现生产现象）。
另：把统计改回用 `values` 会被 **TypeScript 直接拦下**（`TS2339: Property 'toFixed' does not exist on type 'string | number'`）
—— 类型层比测试层更早守住这条不变量。

---

## P5 — OTA 状态映射缺值（**已修**，两处都有）

后端 `ota.go:24-31` 定义 **8 个**状态，前端两处映射表都漏了
`verifying` / `timeout` / `needs_retry`：

| 位置 | 后果 |
|---|---|
| `OTAForm.vue`（升级弹层） | 文案 `TABLE[status] \|\| status` ⇒ 中文界面**原样显示英文**；且 `timeout`/`needs_retry` 不在任何终态分支 ⇒ **轮询永不停止、进度条永远转圈** |
| `NodeDetail.vue`（OTA 历史表） | 文案露英文；颜色回退中性 `info` ⇒ **"超时""需要重试"被显示成中性色**，视觉上与"等待中"无异 |

**修复**：两处都补齐 3 个状态；颜色上 `timeout`/`needs_retry` 给 `danger`（问题态）；
轮询收尾改用 `OTA_TERMINAL_SUCCESS` / `OTA_TERMINAL_FAILURE` 集合。

**验证中的关键发现（否则会写出假测试）**：
先把缺陷改回去跑**既有 108 条** OTA/Node 测试 —— **全部通过**。
即既有测试根本没覆盖状态映射，改坏也全绿。
⇒ 新增 `OTAFormStatusCoverage.spec.ts`（7 例），用 `?raw` 断言
"后端 8 状态 ↔ 前端映射"的**完整性**；变异自证：
删掉 3 个状态 ⇒ 2 例红（`缺少状态映射: verifying`）；删掉 `pre-wrap` ⇒ 2 例红。

---

## P8 — 更新日志换行被折叠（**已修**）

`changelog` 是多行文本（含 `\n`），`.firmware-changelog` 只有
`max-height/overflow-y`，默认 `white-space: normal` 把换行**折叠**成空格
⇒ 多行日志挤成一行（实测 `lineBoxes=1`；滚动区还在但看不到分行）。
修：加 `white-space: pre-wrap`（保留换行且仍允许长行折行）。

---

## 生产 compose 独立化（用户 2026-09-17 指出的部署问题）

**问题**：生产用的 `deploy/prod/compose.ghcr-prod.yml` 只是 **8 行 override**，
必须与仓库根的 `docker-compose.yml` 拼着用。而那根 compose 是**开发用**的：
`ehome` 写的是 `build:`（本地构建）、Postgres 绑 `127.0.0.1:5432`、还带 monitoring profile。
⇒ **生产部署 yaml 不是完整独立的环境**（既无法单独起，语义上也不该叫生产）。

**修复**：新增 `deploy/prod/docker-compose.prod.yml`，**自包含**定义
postgres / emqx / ehome + 卷，无 `build:`（唯一代码来源是 GHCR 正式镜像），
Postgres 不对宿主暴露端口；并**删除**那个 override（避免留下两个入口将来又用错）。

用法：
```bash
docker compose -p ehomesystem --env-file .env -f deploy/prod/docker-compose.prod.yml up -d
```

**验证**：`config --quiet` 通过；解析结果确认**无 build 段**、仅 3 个服务；
实际用它起栈成功（`/health` = `{"status":"ok"}`，镜像为 GHCR，Postgres 无宿主端口映射）。

---

## 处置状态

| 编号 | 状态 | 说明 |
|---|---|---|
| P0 | **已修** | 死列迁移；生产已 drop 列 |
| P1 | **已修** | 取关联通道总线名；**并发现同类 3 处（P1b）一并修** |
| P1b | **已修** | EdgeDeviceList 表格列/卡片/CSV 三处同类拼接 |
| P2 | **已修** | 收敛为唯一 `parseRowData()`，5 处取数统一；区分"0"与"取不到" |
| P3 | 已随 P1 处理 | — |
| P4 | **已修** | 改单列 + MD5 强制断行 + 大小 nowrap |
| P5 | **已定性** | 见 P6 |
| P6 | **已修** | 响应信封多套一层 |

---

## 修复的统筹调度（2026-09-17）

由 4 个并行子代理按**互斥文件集**实施，主控负责分派、审查与实机复验：

| 子代理 | 认领文件 | 结果 |
|---|---|---|
| A | DeviceDeleteDialog.vue / api/edgeDevice.ts | P1 ✅ |
| B | views/data/DataPanel.vue | P2 ✅ |
| C | components/forms/OTAForm.vue | P4 ✅ |
| D | views/edge-device/EdgeDeviceList.vue | P1b ✅ |

**主控独立复验（不采信自报）**：
- `vue-tsc --noEmit` → **0 错误**
- `vitest run` → **145 files / 1573 passed**
- 统一构建 dist 一次（禁止子代理各自 build），确认修复进入产物：
  `channel_hardware_id` / `parseRowData` / `firmware-md5` 均在 dist 中可检出
- **真实浏览器**（隔离栈 :18094 + 种子数据精确复现缺陷形态）：
  P1 通道=`UART0`（不再 `UART 1`）、P2 `rainfall: 0.50`/`0.00` + `7B hex`、
  P4 单列无竖排且 MD5 完整、P1b 卡片与表格「总线」列均为 `UART0` —— **全绿**

**子代理自查出的两个"假绿"（有价值）**：
1. 共享 `ElTable` stub **不求值列插槽**（只 `Object.values(row).join(' ')`），
   直接用它测表格列等于测空气 ⇒ 子代理 D 在 spec 内局部覆盖为真求值替身；
2. 子代理 C 的变异脚本 `ruleBlockOf` 取「首个 `{` 到文件尾最后一个 `}`」，
   规则块被扩到后续规则 ⇒ 删掉 label 的 `nowrap` 也能靠后面规则蒙混过关（假绿）。
   改为括号配平后才真红。

**主控自己踩的同类坑**：读取表格时只取首个 `<table>`，而 Element Plus 把
表头/表体拆成两个 `<table>` ⇒ 误判"没有数据行"。改为读 `tbody tr` 单元格才对。
**"断言/取值方式失效"会伪装成"功能没修好"，与上面两条同源。**
