# unified_data 整表退役 — 实施记录（2026-11-13）

> **状态**: 已实施（代码/接口/前端/部署模板；生产库 DROP 由运维显式执行）
> **日期**: 2026-11-13
> **关联**:
> - [unified_data 硬切换-读路径契约变更-2026-11-08](../规范/unified_data硬切换-读路径契约变更-2026-11-08.md)
> - [复盘-日出日落BMS-MOS验证-2026-11-13](复盘-日出日落BMS-MOS验证-2026-11-13.md)
> - [日出日落BMS-MOS-自动化验证记录-2026-11-13](../验证/日出日落BMS-MOS-自动化验证记录-2026-11-13.md)
> - [自动化策略引擎-日出日落与多动作-方案](../设计/自动化策略引擎-日出日落与多动作-方案.md)

## 1. 结论

`unified_data` 已从**写入、读取、迁移、接口、前端**五个层面全部退役：

| 层面 | 退役前 | 退役后 |
|---|---|---|
| 写入 | 每帧解析后 INSERT（含 L1「值不变不入库」门禁） | **无写入路径**；解析值只进 L2 帧表、L3 分钟桶、最新值缓存 |
| 读取 | 历史接口在帧表不可用时回落 unified_data | 回落删除；帧表是唯一数据源（不可用 → 显式 503） |
| 迁移 | 后台把老 unified_data 回填进 telemetry_frame（3,142 行代码） | 整包删除（生产 3.42M 帧的迁移早已完成） |
| 接口 | `GET /api/v1/system/backfill` + 维护页进度卡 | 端点、快照类型、前端卡片/组合式函数/契约测试全部删除 |
| 部署模板 | `EHOME_TELEMETRY_UNIFIED_WRITE`（默认 false） | 变量删除 —— 没有写入路径，开关没有意义 |

保留的唯一引用是 **`models.UnifiedData` 这个值类型**：它现在是历史接口的 DTO
（帧解码结果、最新值缓存、告警/自动化复核回调都用这个形状），**不表示表存在**。
`TableName()` 仍是 GORM 默认映射，但全仓已无任何 `Model(&models.UnifiedData{})`
的查询/写入。

## 2. 前提（都已成立）

1. **读路径硬切换**（2026-11-08）：用户可见接口全部走 `telemetry_frame` /
   rollup / 维度表 / 最新值缓存。
2. **写入默认关闭**（2026-11-13 用户裁决「直接 false」）：即使旧二进制也不会
   在新库上刷 42P01。
3. **节点位置废除**（2026-11-13 用户裁决「不需要节点位置」）：日出/日落规则
   坐标改为规则必填，不再继承节点 —— 与 unified_data 无关，但同日落地，
   一并改变了"规则能不能保存"的前置条件。
4. **老数据回填已完成**：生产 3.42M 帧迁移完成，回填工具没有继续存在的理由。

## 3. 代码变更清单

### 3.1 后端（写入/读取/迁移）

| 文件 | 变更 |
|---|---|
| `internal/databus/consumers_heavy.go` | 删除 `records` 组装、L1 `writeGate`、`unifiedWriteEnabled` 与 INSERT；只保留 latestSink（解析值缓存） |
| `internal/databus/telemetry_write_gate.go` | **删除**（L1 决策器 + 进程内状态表） |
| `internal/databus/telemetry_metrics.go` | **删除**（`ehome_telemetry_write_gate_*` 指标只服务于 unified_data 写入收窄） |
| `pkg/metrics/metrics.go` | 删除 `UnifiedWritesSkippedTotal`（恒 0 的指标比没有更糟） |
| `internal/backfill/`（14 文件 / 约 3.1k 行） | **整包删除**（config/scan/persist/encode/runner/partition_job + 测试） |
| `internal/api/backfill_status.go` + 测试 | **删除**；仅保留 5 个状态字面量到 `backfill_state.go`（L3 rollup 状态端点复用） |
| `internal/datalifecycle/backfill_progress.go` | 收窄为状态常量；`telemetry_backfill_progress` 表模型与 `EnsureBackfillProgressTable` 删除 |
| `cmd/server/main.go` | 删除回填 goroutine、进度端点注册、导入；保留一行"已整包删除"的说明 |
| `internal/api/handler_sensor_fields.go` | 帧表不可用时**不再回落** unified_data；改为 503 + 明确文案 |
| `internal/api/handler_metrics.go` | 帧计数首次失败时不再退化为 unified_data 行数；返回 0（下次轮询重试） |
| `internal/api/handler_edge_device.go` | `latestValuesMissRowsFromFramesOrDB` 收窄为纯帧表（该文件此前已删回落） |

### 3.2 后端（命令）

新增 `ehomectl retire-unified-data`（`cmd/ehomectl/retire_unified_data.go`）：

```bash
ehomectl retire-unified-data           # 只读校验：表/行数/大小/分区数/回填状态表
ehomectl retire-unified-data --yes     # 打印留痕后 DROP TABLE unified_data CASCADE
```

设计要点：

- **默认只读**，且用 `connectDBNoMigrate`（绝不触发 AutoMigrate）；
- DROP 前打印行数与磁盘占用（DROP 后 `pg_class` 查不到，日志是唯一留痕）；
- DROP 用 CASCADE（生产是月分区母表，手工枚举会漏；与 device_data 退役同口径）；
- 复查用 `pg_inherits` 结构性判断，不用名字前缀（避免把
  `telemetry_backfill_progress` 误算成剩余分区）；
- 母表已不存在时仍会清理 `telemetry_backfill_progress`（幂等收尾）。

### 3.3 前端

| 删除项 | 说明 |
|---|---|
| `views/maintenance/BackfillProgressCard.vue` | 维护页早已不引用（死代码），随端点删除 |
| `views/maintenance/useBackfillProgress.ts`、`backfill.ts` | 轮询/取数逻辑 |
| `api/system.ts` 的 `getBackfillStatus` / `BackfillEnvelope` / 形状判据 | 端点不存在 |
| `types/system.ts` 的 `BackfillProgress` / `BackfillState` / 状态常量 | 契约类型 |
| 4 个测试文件（组件/轮询/契约一致性/API 失败语义） | 被测对象删除 |

### 3.4 部署模板

- `.env.example`：删除 `EHOME_TELEMETRY_UNIFIED_WRITE` 与对应注释；
- `docker-compose.yml`：删除显式的 `EHOME_TELEMETRY_UNIFIED_WRITE: "false"`。

## 4. 保留项与理由

| 保留项 | 理由 |
|---|---|
| `models.UnifiedData` 结构体 | 历史接口 DTO：帧解码、最新值缓存、告警/自动化复核都返回这个形状；改名会波及 300+ 引用且无收益 |
| `unified_data` 的索引/分区**模板**条目 | 只对"表仍存在"的旧库有影响（DDL 是惰性的）；DROP 后自然不再生效。留着可让仍持有旧表的部署在迁移窗口内继续受分区管理 |
| `LatestRowsFromFrames` 等帧表函数 | 返回 `[]models.UnifiedData`，是 DTO 复用，不碰表 |

## 5. 验收

### 5.1 单元/包测试

- `internal/databus`：原 9 条依赖"数 unified_data 行"的用例改为**数 L2 帧旁路读数**
  （`captureParsed`），并新增/重写 3 条 sink 接线用例（每帧必达、nil 安全、解析失败不调用）。
- `internal/nodemgr`：datapipe 12 处断言、分片并发用例改为帧旁路计数
  （分片用例从 32s 降到 2.4s，因为它不再等 SQLite 写锁）。
- `internal/api`：删除回填端点测试；其余通过。
- 全量 `go test ./...`：**PASS**（详见 §5.4）。

### 5.2 运行时（demo 库 `ehome_simdemo`）

| 检查 | 结果 |
|---|---|
| `to_regclass('unified_data')` | `NULL`（不存在） |
| `to_regclass('telemetry_backfill_progress')` | `NULL`（命令清掉） |
| `telemetry_frame` 行数（6s 间隔） | 1620 → 1622（持续增长） |
| 规则行 | 2 条（id=2/3），坐标/时区保留 |
| 事件/命令 | 2 / 2（无重复触发） |
| 节点状态 | `sim-bms-01` = `online` |
| 新二进制日志 42P01 | 0 |
| 老回填日志 | 0 |
| 日出/日落 tick | 正常：`首 tick 保守跳过已过事件`（重启不补触发） |

### 5.3 命令

```text
$ ehomectl retire-unified-data
== unified_data 退役校验 ==
  表存在:  否
  回填状态表: true
提示：telemetry_backfill_progress 仍存在（已无写入方），执行 --yes 会一并清理。
unified_data 不存在（全新部署 / 已退役），无需操作。

$ ehomectl retire-unified-data --yes
⏳ telemetry_backfill_progress 已无写入方，清理 ...

$ ehomectl retire-unified-data --yes   # 幂等重跑
  表存在:  否
  回填状态表: false
```

### 5.4 测试与类型检查

- 后端：`go build ./...` / `go vet ./...` 全绿；`go test ./...` **48 个包全部 ok**
  （e2e 包需 `-tags=e2e` 且依赖真实服务，不在默认套件内）。
- 前端：`pnpm typecheck` 全绿；`pnpm vitest run src/views/automation src/api src/views/maintenance`
  = **451 passed**（16 文件）。
- UI 复验（Playwright，真实浏览器 + 运行中的后端）：事件表出现「事件时刻」列；
  日落规则表单显示「坐标与时区为必填」；只填纬度时保存被前端拦下并提示
  「请填写纬度、经度与时区（节点位置已废除，规则坐标必填）」。截图见
  [验证记录 §10.1](../验证/日出日落BMS-MOS-自动化验证记录-2026-11-13.md)。

## 6. 回滚与数据处置

- **DROP 不可回滚**。执行前必须 `pg_dump`（至少包含 `unified_data` 与
  `telemetry_backfill_progress`）。
- 回滚镜像不会让表回来：旧二进制的写入会因表不存在而报 42P01（已被默认停写
  开关挡住）；读取会走帧表（2026-11-08 起的主路径）。真需要旧表只能从备份恢复。
- 生产执行顺序：停旧容器 → `pg_dump` → `ehomectl retire-unified-data --yes`
  → 起新容器（新容器启动日志不应再出现回填/42P01）。

## 7. 未做（明确不做）

- **不 DROP** 节点表的 `latitude/longitude/timezone` 列：2026-11-13 起已无
  读写路径（节点位置废除），但列本身是历史数据；生产 `automation_rules` 为
  0 行、迁移风险低，留给后续独立迁移。
- **不删除** `unified_data` 的索引/分区模板条目（见 §4）。
- **不重命名** `models.UnifiedData`（见 §4）。

## 8. 关联变更（同日）

- 节点位置废除：`sun_location.go` 只接受规则坐标；API/前端必填校验；
  e2e 夹具改为规则级坐标。
- `automation_events.event_at`：sun_event 记录"哪一次日出/日落"（fireAt），
  UI 触发历史新增「事件时刻」列。
- 自动化触发日志：`Planner.recordRet` 在唯一收口输出结构化 INFO/WARN
  （此前只有 DB 行，现场只能查库）。
