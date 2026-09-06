# 设备指令与操作体系演进 Phase 0+1 接口契约与测试设计

> **状态**: 契约/测试设计（与开发计划配套）
> **版本**: v1.0
> **日期**: 2026-09-06
> **母本**: [设备指令与操作体系演进方案.md](设备指令与操作体系演进方案.md)
> **配套**: [设备指令与操作体系演进-Phase0-1开发计划.md](设备指令与操作体系演进-Phase0-1开发计划.md)
> **范围**: Phase 0（C1–C5）+ Phase 1（C6–C7）涉及的全部对外契约、内部 helper API、清洗工具接口、审计 SQL 与测试设计。

---

## 1. 前后端契约变更表（对外 API）

### 1.1 三条不变量（本方案契约总纲）

> **I-1**：`edge_devices.command_intervals` 的键集 ⊆ 驱动 Schedulable 模板 ID 集。
> **I-2**：GET 端点只返回 Schedulable 模板。
> **I-3**：PUT 与 POST 的校验语义完全对称。

### 1.2 端点契约变更

| 端点 | 变更前（基线 f15f9c30） | 变更后（C2/C3 落地） | 对应 commit |
|------|------------------------|----------------------|-------------|
| `GET /api/v1/drivers/:type/commands` | 返回驱动全部模板（含 Schedulable=false） | 只返回 Schedulable=true 模板，顺序保持；techfine 返回 `[]` | C3 |
| `GET /api/v1/edge-devices/:id/commands` | 返回全部模板 + `current_interval_ms` 覆盖 | 只返回 Schedulable=true 模板（覆盖逻辑不变） | C3 |
| `PUT /api/v1/edge-devices/:id/commands` | merge + 负数归零，无键校验 | merge → **对 merge 后全量 map 校验**（未知 ID / 非 schedulable ID → 400，不落库）→ 负数归零 → 落库 | C2 |
| `POST /api/v1/edge-devices`（command_intervals 字段） | 已有校验 | **不变**（C1 后由同一 helper 实现） | C1 |

### 1.3 响应 schema 与错误语义

**GET /drivers/:type/commands**（C3 后）：
```json
{"code": 200, "data": [{"id":"read_a","name":"读A","type":"read","cmd_byte":0,
  "write_data":"AA01","read_length":4,"delay_ms":0,"interval_ms":5000,
  "schedulable":true,"description":"..."}]}
```
- 404（driver 不存在）：`{"code":404,"message":"driver not found: <type>"}`（不变）
- 变更点：`data` 数组不再含 `schedulable:false` 元素。

**GET /edge-devices/:id/commands**（C3 后）：
```json
{"code": 200, "data": [{"id":"read_a", ..., "schedulable":true, "current_interval_ms":3000}]}
```
- 404（设备/驱动不存在）：语义不变。
- 变更点：数组只含 schedulable 模板；`current_interval_ms` 覆盖优先级不变（stored > edge.IntervalMs > 模板默认）。

**PUT /edge-devices/:id/commands**（C2 后）：
- 请求体不变：`{"intervals": {"<command_id>": <interval_ms>}}`（0=禁用；空 map → 400 "intervals is required"，不变）。
- 200：`{"code":200,"message":"ok","data":{"command_intervals":{...merge 后全量 map...}}}`（不变）。
- **新增 400 语义**（校验失败，**不落库、不发 ConfigChange 事件**）：
  - 未知 ID：`{"code":400,"message":"command_intervals: command \"<id>\" is not a schedulable command of driver \"<type>\""}`
  - 非 schedulable ID：同上（同一错误文案，I-3 对称）。
  - 设备类型无注册驱动 / 无模板 provider：`{"code":400,"message":"cannot validate command_intervals: driver for type \"<type>\" is not registered"}`（或 `provides no command templates`）。
- **语义变更（有意收口）**：PUT 是部分更新，校验对象是 **merge 后全量 map**——存量脏键会让 PUT 400。因此 C2 的存量清洗必须先于校验上线（同 commit，部署顺序见开发计划 C2）。

### 1.4 前端契约影响（本次前端零改动）

| 前端消费点 | 行为 | 处置 |
|-----------|------|------|
| `CommandList.vue:131` `schedulableCommands = commands.filter(c => c.schedulable)` | 客户端过滤 | **保留**（防御性，后端过滤后为幂等操作） |
| `CreateWizardCommandIntervals.vue:109` 同款过滤 | 客户端过滤 | **保留** |
| `CommandList.vue:70-77` 空态文案 | techfine 设备 C6 后 `GetCommandTemplates` 返回空 → 后端返回 `[]` → 前端显示"无可配置的轮询指令；一次性读取请使用下方受控操作" | 已是既有文案，无需改 |
| 锚定测试 `CommandListControlBoundary.spec.ts:54`、`sn3001-real.spec.ts:24-28` | "触发指令不得出现" | 后端过滤后依然成立，**文件不得修改** |

---

## 2. 内部 helper API 设计（C1 提取）

文件：`backend/internal/api/command_intervals.go`（新建，完整代码见开发计划 C1）。

### 2.1 签名与职责

```go
// 键集权威（I-1 的"合法键集"唯一来源）
func SchedulableCommandIDs(driverRegistry *drivers.Registry, devType string) (map[string]struct{}, error)

// 校验：拒绝未知 ID 与非 schedulable ID（POST/PUT 共用）
func ValidateCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) error

// 归一化：负数归零，纯函数，不校验键
func NormalizeCommandIntervals(intervals map[string]int) map[string]int

// 创建路径组合入口（行为与基线完全一致）
func validateAndNormalizeCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) (json.RawMessage, error)
```

### 2.2 职责分离理由（为什么拆两个）

1. **PUT 的校验对象是 merge 后全量 map，归一化对象也是它**——但 merge 发生在 handler 内（需要读 DB），helper 不能包办 merge。因此 helper 必须暴露"校验"与"归一化"两个可独立调用的原语，PUT 流程 = merge（handler）→ Validate（helper）→ Normalize（helper）→ marshal（handler）。
2. **C4 契约测试需要单独取"合法键集"**（`SchedulableCommandIDs`）与端点返回集、manifest 候选集比对——一个返回 map 的纯查询函数是四集相等断言的必要条件。
3. **单一权威**：`SchedulableCommandIDs` 是三处消费（POST 校验、PUT 校验、契约测试）的唯一键集来源，杜绝"每处自己写一遍循环"的漂移（这正是蓝本 I-1 要防的）。
4. 归一化是纯函数：不依赖 registry，可独立单测；校验不触碰值（负数是否合法与键校验无关）。

### 2.3 错误文案契约（前后端/日志可检索）

| 场景 | 错误文案（精确） |
|------|------------------|
| 驱动未注册 | `cannot validate command_intervals: driver for type %q is not registered` |
| 无模板 provider | `cannot validate command_intervals: driver for type %q provides no command templates` |
| 键非法 | `command_intervals: command %q is not a schedulable command of driver %q` |

> 执行者注意：C1 不得改文案（与基线逐字一致，`handler_edge_device_command_intervals_test.go` 只断言状态码不断言文案，但保持文案稳定是纪律）。

---

## 3. 数据清洗工具接口（C2）

### 3.1 CLI 接口

```
ehomectl command-intervals cleanup
```
- 无参数；exit 0 = 成功，exit 1 = 失败（复用 `fatal()`）。
- 输出一行：`command-intervals cleanup: devices_scanned=N devices_cleaned=M keys_removed=K`
- 连接复用 `connectDB()`（config.yaml 的 DB 配置），注册全部内置驱动。

### 3.2 幂等性定义（可验证判据）

> 对同一数据库状态连续执行两次 `cleanup`：
> 1. 第二次 `devices_cleaned == 0` 且 `keys_removed == 0`；
> 2. 两次执行后的 `edge_devices.command_intervals` 逐行相等。
>
> 测试锁定：`TestCleanup_Idempotent`（datalifecycle/command_intervals_cleanup_test.go）。

### 3.3 清洗语义（三种存量形态）

| 存量形态 | 处置 |
|----------|------|
| `command_intervals IS NULL` | 不动 |
| 空 map `{}` | 不动 |
| 含非 schedulable 键 | 删除非 schedulable 键；删空后写 `NULL`（不写 `{}`） |
| 设备类型无驱动/无 provider | schedulable 集视为空 → 全部键删除 → `NULL` |

> 注意：清洗**不**触碰 interval 值（负数归零是 PUT/POST 的职责，不是清洗的职责）；清洗**不**动 `config_templates` 表。

### 3.4 部署顺序（写进 C2 commit message）

1. 合并 C2（代码 + 清洗工具 + 测试）
2. 停服（或低峰窗口）
3. `ehomectl command-intervals cleanup` → 记录输出
4. 启动带 PUT 校验的服务
5. 验证：对曾含脏键的设备 PUT 合法键 → 200

---

## 4. 审计 SQL（C6，原文）

### 4.1 审计 SQL 原文（一次性，不落库）

```sql
SELECT id, node_id, write_data
FROM config_templates
WHERE edge_device_id IS NULL
  AND upper(translate(write_data,' ','')) IN
  ('485354530D','48475249440D','484F500D','484241540D','4850560D','485056420D',
   '4854454D500D','4847454E0D','48424D53310D','48454550310D','48494D5347310D');
```
> 2026-09-06 实测修正：config_templates 列集为 id/node_id/write_data/read_length/delay_ms/edge_device_id/created_at/updated_at（**无 channel_id**）；translate 去空格防御 hex 存储形态差异。
> **主 Agent 已执行本审计（生产库 ehome-postgres）：0 行命中（全表 11 行，unowned 0 行）**——C6 删除获准，审计证据以此为准。

11 帧 hex 来源（`asciiToHex("<ASCII>\r")`，python3 实测）：
| 命令 | ASCII | hex（大写） |
|------|-------|-------------|
| query_status | HSTS\r | 485354530D |
| query_grid | HGRID\r | 48475249440D |
| query_output | HOP\r | 484F500D |
| query_battery | HBAT\r | 484241540D |
| query_pv1 | HPV\r | 4850560D |
| query_pv2 | HPVB\r | 485056420D |
| query_temperature | HTEMP\r | 4854454D500D |
| query_energy | HGEN\r | 4847454E0D |
| query_bms | HBMS1\r | 48424D53310D |
| query_eeprom | HEEP1\r | 48454550310D |
| query_version | HIMSG1\r | 48494D5347310D |

### 4.2 审计判定规则

- **0 行** → 允许删除；SQL 原文 + "0 行" 写入 C6 commit message。（**2026-09-06 生产库实测 = 本情况**）
- **>0 行** → **停止**，上报主 Agent；先人工归属（写 `edge_device_id`）或归档，再重跑审计；不得自行删除、不得自行归属。

### 4.3 审计的必要性（为什么先审计后删除）

`template_backfill.driverTemplateMatches`（template_backfill.go:108-116）以 `GetCommandTemplates()` 的 WriteData 为匹配源，且**循环不看 Schedulable**（注释声称 schedulable 但实现按 WriteData 全量匹配——主 Agent 已实锤该差异）。删除 11 条后，任何 WriteData 属于该帧集的 unowned `config_templates` 行将永远无法归属 → 留 NULL → 删除设备时孤立模板不回收（宁留勿删）。审计确认无此类行，删除才安全。

---

## 5. 测试设计总表（新增/改写）

### 5.1 新增测试文件与用例

| 文件 | 用例 | 锁定目标 | commit |
|------|------|----------|--------|
| `api/command_intervals_test.go` | TestSchedulableCommandIDs_ReturnsSchedulableOnly | helper 键集 | C1 |
| 同上 | TestSchedulableCommandIDs_UnknownTypeErrors | 错误语义 | C1 |
| 同上 | TestValidateCommandIntervals_RejectsUnknownId / RejectsNonSchedulableId / AcceptsSchedulableIds | 校验契约 | C1 |
| 同上 | TestNormalizeCommandIntervals_ClampsNegatives | 归一化纯函数 | C1 |
| `api/handler_driver_commands_test.go` | TestPutCommands_RejectsNonSchedulableId / RejectsUnknownId | PUT 400 不落库（M3） | C2 |
| 同上 | TestPutCommands_MergesPartialUpdate / NormalizesNegative | PUT 正常语义回归 | C2 |
| 同上 | TestPutCommands_RejectsLegacyDirtyStoredKey | "清洗先于校验"前提锁定 | C2 |
| 同上 | TestPutCommands_DriverNotFound | 无驱动类型 PUT 语义 | C2 |
| 同上 | TestGetDriverCommands_FiltersNonSchedulable / TestGetEdgeDeviceCommands_FiltersNonSchedulable / OverlayStillWorks / UnknownDriver404 | GET 过滤（M2） | C3 |
| `api/command_contract_test.go` | TestCommandContract_FourSetEquality | A==B==C==D（W5） | C4 |
| 同上 | TestCommandContract_IntervalZeroDropsFromDOnly | 候选集谓词边界 | C4 |
| `datalifecycle/command_intervals_cleanup_test.go` | TestCleanup_NullStaysNull / RemovesNonSchedulableKeepsSchedulable / EmptyAfterCleanBecomesNull / DriverlessTypeRemovesAll / Idempotent | 清洗语义 + 幂等（M4） | C2 |
| `drivers/drivers_test.go` | TestBuiltInDriversTemplatesAllSchedulable | 第三态防线（W2） | C7 |

### 5.2 改写测试

| 文件 | 用例 | 改写内容 | commit |
|------|------|----------|--------|
| `drivers/inverter_techfine_test.go` | TestTechfine_CommandTemplates（555-592 行） | 断言 `GetCommandTemplates()` 返回空（len==0） | C6 |

### 5.3 删除测试

| 文件 | 用例 | 删除理由 | commit |
|------|------|----------|--------|
| `drivers/inverter_techfine_test.go` | TestTechfine_LegacyUnsafeTemplatesFailClosed（631-635 行） | 被测函数删除后失去意义 | C5 |

### 5.4 四集相等契约的数学定义（C4）

设驱动 D、设备 E（type=D）、存储 intervals S、快照模板表 T：
- **A** = `{t.ID | t ∈ GET /drivers/D/commands 响应}` = `{t.ID | t ∈ D.GetCommandTemplates() ∧ t.Schedulable}`
- **B** = `{t.ID | t ∈ GET /edge-devices/E/commands 响应}` = A（同一过滤谓词）
- **C** = `SchedulableCommandIDs(registry, D)` = A（同一循环）
- **D** = `{t.ID | t ∈ D.GetCommandTemplates() ∧ CommandIsManifestCandidate(t, S, T)}` = `{t ∈ A | effectiveInterval(t,S) > 0 ∧ ∃τ∈T: upper(τ.WriteData)==upper(t.WriteData)}`

**断言**：A == B == C；D ⊆ C 恒成立；当 S 覆盖 A 且 interval>0、T 覆盖 A 的 WriteData 时 D == C（测试用全量配置构造该前提）。非 schedulable 模板 ID 不属于任何一集。

### 5.5 测试基建说明（执行者必读）

- `api` 包测试：`setupDriverCommandsTest(t)` 照抄 `setupEdgeDeviceTest`（handler_edge_device_crud_test.go:26-51）骨架，但 registry 用 `newFakeRegistry()`（`fakeMultiDriver`：read_a/read_b schedulable + one_shot 非 schedulable），路由注册 `registerDriverCommandRoutes(v1, db, mgr, registry)`。
- **禁止用 techfine 当"非 schedulable fixture"**：C6 会删掉它的模板，用 techfine 的测试在 C6 后必红。既有 `TestEdgeDevice_CommandIntervals_RejectsNonSchedulableId`（handler_edge_device_command_intervals_test.go:130-151）用的是 techfine——它是**锚定测试**，C6 后其语义变为"techfine 无 schedulable 模板 → query_status 是未知 ID → 仍 400"，断言不变仍绿，**不得修改**。
- `datalifecycle` 包测试：`testutil.OpenTestDB(t)`（backend/testutil/db.go），registry 用 `drivers.NewRegistry()` + `RegisterBuiltInDrivers` + 测试内注册假驱动（若需要非 schedulable 键 fixture）。
- `drivers` 包测试：直接同包访问 `NewRegistry` / `RegisterBuiltInDrivers` / `Registry.List`。

---

## 6. 锚定资产清单（全程不得修改）

| 资产 | 路径 | 锚点 |
|------|------|------|
| 前端单元测试 | `frontend-shared/src/components/device/__tests__/CommandListControlBoundary.spec.ts` | :54 "触发指令不得出现" |
| 前端 E2E | `frontend-shared/e2e/sn3001-real.spec.ts` | :24-28 同断言（实机 rig） |
| 前端 E2E | `frontend-shared/e2e/sn3001-real-writes.spec.ts` | 全文件 |
| 后端创建路径测试 | `backend/internal/api/handler_edge_device_command_intervals_test.go` | 全部 9 用例（C1 后必须原样全绿） |
| 后端 nodemgr 测试 | `backend/internal/nodemgr/sender_snapshot_test.go` | C4 谓词提取后必须原样全绿 |

每个 commit 后自查：`git diff --name-only HEAD~1 HEAD` 不得包含上述文件。

---

## 7. 与蓝本的行号差异记录（实测）

| 蓝本引用 | 实测 | 差异 |
|----------|------|------|
| handler_driver_commands.go :32 / :56-77 / :88-130 | 32 / 56-77 / 88-133 | PUT 段到 133 |
| handler_edge_device.go:66-107 | 66-107 | 一致 |
| sender_snapshot.go:167-185 | 167-185 | 一致 |
| template_backfill.go:105-114 | 106-116 | +1 |
| inverter_techfine.go:849-859 / 863-1096 | 849-859 / 863-1096 | 一致（228 行实测） |
| inverter_techfine_test.go:632 附近 | 631-635 | 一致 |
| TestTechfine_CommandTemplates 555-585 | 555-592 | 实测到 592 |
| builtin.go:98/140/272/… | 98/140/272/283/355；jiabaida.go:1892；generic_modbus.go:45；generic_i2c.go:44 | 补全 9 处 |
| RegisterBuiltInDrivers | builtin.go:844 | 一致 |
| CommandList.vue:131 / CreateWizardCommandIntervals.vue:109 | 131 / 109 | 一致 |
