# task-8 收口记录：求值接入 + F4 + precheck（auto-analyst）

> 采集时刻：2026-10-21T22:20–23:20 +08:00｜工作树：`.agents/worktrees/field-resolver`（分支 field-resolver）
> 本文件是 task-8 的**交付台账**：范围落点、验证证据、裁决项、交接项。设计依据见
> `docs/设计/数据字段解析层-方案-2026-10-21.md` v0.2 与评审报告 §2 B4/B5/B9。

## 1. 交付范围与落点

| # | 设计条目 | 落点 | 状态 |
|---|---|---|---|
| 1 | §9.2#1/#2 automation trigger + conditions 走 plan | `automation/selector.go`（planCache + evalSelector）、`automation/evaluator.go`（evalRule 改签名带 deviceID） | 完成 |
| 2 | §9.2#3 alert 求值走 plan（评审阻断 B4） | `alert/selector.go`、`alert/evaluator.go`（evalRule 带 deviceID + markRuleFired） | 完成 |
| 3 | §9.2#4 planner F4 按 selector 复核（修 AU-12） | `automation/planner.go`：`checkConditionsStillSatisfiedDetailed` + `evalSelectorLatest` | 完成 |
| 4 | §9.2#6 precheck 端点（修 AU-06） | `api/handler_automation.go`：`precheckAutomationRule` + `POST /automation-rules/precheck` | 完成 |
| 5 | §9.2#7 alert 保存校验 | `api/handler_alert.go`：`validateAlertSensorSelector` | 完成 |
| 6 | R2 触发语义变更清窗口（窄复核追加） | 两包 `sameSensorTriggerState`/`sameAlertTriggerState` + LoadRules 级联 | 完成 |
| 7 | Y5 TOCTOU 裁决 B | `automationActionSetHash` + `recordPendingConfirm` + ConfirmEvent 比对 | 完成 |

## 2. 验证证据（可复跑）

工具链：`/mnt/storage/WorkSpace/EHome/.tools/gotool/bin/go`（go1.27.1），
`GOCACHE=<worktree>/.analysis/tmp-t8/gocache`，`GOFLAGS=-mod=mod GOTOOLCHAIN=local`。

| 命令 | 结果（时刻） |
|---|---|
| `go build ./...`（全仓） | BUILD_OK（22:26） |
| `go test ./internal/automation/ ./internal/alert/ ./internal/api/ -count=1` | ok 1.665s / 0.807s / 7.860s（22:33） |
| `go test ./internal/automation/ ./internal/alert/ -race -count=1` | ok 4.862s / 1.833s（22:57） |
| `EHOME_TEST_DB=postgres EHOME_DB_*=127.0.0.1:5432/ehome_test go test ./internal/automation/ ./internal/alert/ -race -count=1` | **ok 62.987s / 1.817s**（23:10，全包 PG） |
| `go test ./internal/automation/ -run TestPGY -v`（PG） | Y1a 0.91s / Y2a 0.86s / Y5a 0.85s 全 PASS（22:47） |

新增测试文件：`automation/selector_test.go`、`automation/planner_selector_recheck_test.go`、
`automation/f5_residue_test.go`、`automation/pg_y_cascade_test.go`、`alert/selector_test.go`、
`alert/f5_y3b_test.go`、`api/handler_automation_precheck_test.go`、`api/handler_alert_selector_test.go`。

## 3. 裁决项与偏离（供复核直接引用）

1. **F5-Y3b 实现口径差异（Lead 已批准，2026-10-21）**：告警规则内容变更时，陈旧 firing
   **就地 update 为 resolved**（state/resolved_at/value），**不新增** `detail` 列、不新增
   resolved 行。理由：`models.AlertEvent` 无 detail 列；既有语义是单行事件（`onResolve`
   就地 update）；新增行会改变"未恢复告警"计数与前端历史语义。回归锁：
   `alert/f5_y3b_test.go`（含 fired_at 不变 + 历史通知逐字段不变断言）。
2. **F5-Y3a/Y4a/Y4b 口径**：历史通知不可撤回（只增不改）；在途 TriggerEvent 严格用触发时
   rule 拷贝（断言复核阈值来自拷贝而非 DB）；规则中途被删按旧拷贝执行、不 panic。
3. **Y5 TOCTOU 采用方案 B**：pending 落 `action_hash`（按 type/target/params 归一化、忽略
   展示字段），confirm 不一致 ⇒ `ErrConfirmActionChanged`，事件保持 pending。
4. **审计拆分（Lead 裁决）**：automation 落 `unresolved_selector` 与 `field_resolver_disabled`
   两条 result，按 (ruleID, selectorHash) 5 分钟去重；alert **不新增事件类型**，解析失败只
   记日志 + `ehome_rule_never_resolved{kind="alert"}` 指标。
5. **Y2c 口径**：`pending_confirm` 不占 `MaxDailyExec`（只 count executed 行），已用测试钉住。

## 4. 顺带修复的既有缺陷（非本批引入，需在汇总时标注）

**PG 假红：`planner_multi_action_test.go:169` 冷却锚点断言用 `Equal`。**
PostgreSQL 的 `timestamp` 精度为微秒、SQLite 存纳秒；`ConfirmEvent` 是从库里重读 event 后
写锚点，故 PG 下锚点恒为 µs 截断值（实测 `...788932` vs `...78893272`）⇒ 原断言 SQLite 绿、
PG 红。已在两侧统一 `Truncate(time.Microsecond)` 后比较；该文件在本批之前的最后改动是
`6cdb30e6`（**main 上同样存在此缺陷**），本批顺带修复，不改变被测行为（µs 以上偏差仍红）。

## 5. 交接项（不属于 task-8 范围，需他人完成）

1. **main.go 四处注入**（裁决归 ds-analyst）：
   `automationPlanner.SetPlanProvider(registry)`、`SetLatestValueBySensorFn(api.LatestValueBySensor)`、
   `SetLatestValuesFn(api.LatestValues)`、`api.SetFieldSelectorResolver(registry)`，
   以及 `automationEvaluator.SetPlanProvider` / `alertEvaluator.SetPlanProvider`。
   **未接线时行为**：literal 完全照旧（G-6），特殊 selector fail-closed 并落审计。
2. **sensorfield 侧**：`EvalPredicate`/`CmpOp`/`Verdict`/`RuleNeverResolved`/`RuleNeverFired`
   由 impl-core 提供（commit 36c73be1）；本批只 Set、不重复定义。
3. **性能回归修复**（selector 短路，#10a）：`automation/selector.go` 与 `alert/selector.go`
   的优化由 impl-core 实施（Lead 裁决文件所有权移交）；本批仅提供回归锁
   （literal 逐字节等价 / 特殊 selector 语义 / kill switch），impl-core 提交后需回跑：
   `go test ./internal/automation/ ./internal/alert/ -race -count=1` + PG 全包。
4. **models 枚举与前端枚举**：`unresolved_selector` / `field_resolver_disabled` 需在
   `models/automation.go` 常量表与 `frontend-shared/src/api/automation.ts` 的
   `AutomationEventResult` 同步新增（本批以 `automation` 包常量承载同一字符串值，未改 models）。

## 6. 未验证清单（诚实边界）

1. 前端未改，故前端 60 例未重跑（`AutomationEventResult` 新值未加，前端 TS 联合类型暂不认识）。
2. `models/automation.go` 的结果码常量与仿真白名单 `auditIsDefinedResult` 未同步（见 §5.4）。
3. 真实设备链路（ESP32 → mTLS → nodemgr）未跑；本批验证止于 Go 单测 + 真实 PG。
4. `-race` 全仓未跑（只跑 automation/alert 两包及其依赖）。
