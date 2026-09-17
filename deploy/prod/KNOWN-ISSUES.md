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

## 处置状态

| 编号 | 状态 | 说明 |
|---|---|---|
| P0 | **已修** | 死列迁移 + 单测 + 变异自证 + 真实 PG 验证；生产已 drop 列 |
| P1 | **未修** | 待用户确认后修（改动一处取数来源） |
| P2 | **未修** | 需先定方向：前端改读 `data_json`，还是后端补字段 |
| P3 | 建议 | 与 P1 合并处理 |
| P4 | **未修** | 视觉缺陷，改动小 |
| P5 | **已定性** | 见 P6：历史 TAB 的脏行是响应信封 bug，非"设计不显示进度" |
| P6 | **已修** | 响应信封多套一层；单测 + 变异自证 |
