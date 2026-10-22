# 端到端数据流模拟验证（internal/e2e）

回答的问题：模拟边缘设备（BMS）上报真实协议数据后，**数据源管理 → 规则引擎 → 设备动作 → 下行命令 → 告警 → 前端渲染**整条闭环是否正常。

## 与 simulation/ 的分工

| 层 | 机制 | 现状 |
|---|---|---|
| simulation/（黑盒） | 起真实 cmd/server 子进程 + 真实 PG + MQTT | 已作废：MQTT 于 2026-10-08 移除，TestScenarios 不再有效 |
| internal/e2e/（本包） | 进程内组装与 cmd/server 相同的**数据面 + 控制面**（databus 消费者 + 真实 sink 注入 + 真实 datasource/automation/alert/commandexec/dispatcher + 真实 PG） | **有效** |
| frontend-shared/e2e/render-contract.spec.ts | 真实构建产物 + 注入确定性 API 响应，逐元素断言渲染 | **有效** |

本包只跳过 TLS 传输层与 WebSocket 客户端；下行用捕获型 Publisher 替代 TCP session，再用真实 `downlink.Bridge` 校验 3.0 外框。

## 怎么跑

```bash
# 后端（7 场景）
docker exec ehome-postgres psql -U ehome -d postgres -c 'CREATE DATABASE ehome_e2e;'
cd backend
EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=ehome_e2e \
  go test -tags=e2e ./internal/e2e/ -count=1 -v

# 前端渲染契约
cd frontend-shared
npx vite build
CHROME_BIN=~/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome \
  npx playwright test -c playwright.render.config.ts
```

本地 SQLite（快速回归）：`go test -tags=e2e ./internal/e2e/ -count=1`

## 后端场景清单（7 个，全 PASS）

| # | 测试 | 验证链路 |
|---|---|---|
| 1 | `TestEdgeDeviceDataFlow_FullChain` | 帧 → 解析 → unified_data(12 行逐值) → 最新值缓存（复现 AU-12 单槽根因 + BySensor 修复）→ 观测投影 → KnownFields → @soc 解析 → 规则触发 → automation_events + notifications |
| 2 | `TestEdgeDeviceDataFlow_FamilySelectorAndDegraded` | temperature_*:max（动态族）：首帧 unbound 且落审计 → 投影刷新后求值 → 常温不触发/超温触发；缺员降级语义 |
| 3 | `TestEdgeDeviceDataFlow_DatasourceThenRule` | 同一帧驱动数据源域与规则引擎：MarkSuccess → 连续失败 ⇒ error + health → 恢复 ⇒ standby；unified_data 持续入库 |
| 4 | `TestBMS_SunEventSwitchesMOS` | **完整业务闭环**：BMS 帧 → 数据源健康 → 日出 sun_event → set_mos_policy（高风险 device_action，规则级授权）→ commandexec 九道门禁 → dispatcher 出队 → 内层 ChannelCmdV2 + 3.0 外框校验；同日重复 tick 不重复下发 |
| 5 | `TestBMS_SunEventMOS_RequiresRiskAck` | **负对照**：未授权风险动作 ⇒ confirmation_required，**零下行帧** |
| 6 | `TestAlertEngine_ThresholdFireAndResolve` | **告警引擎**：超温 → firing（AlertEvent+Notification）→ 持续 firing 期间 SilenceSec 抑制重复通知且不新建事件 → 恢复 ⇒ 单行就地 resolved → 再次超温 ⇒ 新一轮 firing 重新通知 |
| 7 | `TestDatasource_FailoverToStandby` | **主备切换**：第二台边缘设备作备来源（组键=(logical,category)）→ 主来源失败 3 次 ⇒ error + 自动切换 → failover_logs(方向/reason) + health(failure×3+transition) + 1 条切换通知 → 备设备上报 ⇒ 备 last_success 更新、主保持 error → R10 冷却抑制重复切换 |

## 前端渲染契约（4 个用例）

覆盖 `/data-sources`、`/alerts`、`/automation`：**逐元素**断言状态标签文本、类别、失败计数 `0/3`/`3/3`、传感器名+阈值+级别、sun_event 中文、selector `@soc` 原样可见、行数。

**变异自证**：把 `statusText` 的 `active` 文案从"权威"改为"主用" ⇒ 门禁 FAIL（`Received: "主用"`），恢复后 PASS 且 md5 一致。
> 首版用**整页文本**断言时该变异**不会红**（统计卡与按钮里也含"权威"）—— 故改为元素级断言，这是该 spec 存在的最直接理由。

## 夹具铁律（都是实测踩出来的）

- `set_mos_policy` 是 `bounded_sequence`：节点必须上报 `supports_bounded_batch` 且 `max_batch_steps ≥ 2`，否则 `action is unavailable`
- 节点还需 `Status=online` + `ConfigStatus=applied` + `BootID` + 新鲜 `ResourceReportedAt` + `hardware_info.channels` 含该通道
- 必须有 `system_admin` 用户（Planner 惰性解析 system actor，缺了落 `failed_dispatch`）
- `EdgeDevice` 需 `Enabled=true` + `Status=active`
- 主备的"两个来源"必须是**同一逻辑设备下的两台不同边缘设备**（`uniq_ds_source` 约束）
- 下行断言分两层：Transport 与 Bridge 之间是 2.x 内层（首字节=类型），3.0 外框由 Bridge 加

## 未覆盖（诚实边界）

TLS 握手与设备证书、WebSocket 客户端推送、节点侧真实固件执行（ACK/Final 回传）、前端在**真实后端**上的渲染（当前渲染门禁注入确定性响应；真实后端受登录限流 429 与审计库差异影响）。
## 来源组（source_group）场景 —— task-30（E1–E11，进行中）

文件：`source_group_e2e_test.go`（`//go:build e2e`）。跑法：

~~~bash
cd backend
# SQLite 快速回归
go test -tags=e2e ./internal/e2e/ -run 'TestSourceGroup|TestE1|TestE2|TestE5|TestE6|TestE7|TestE8|TestE9|TestE10|TestE11' -count=1 -v
# PG（与生产同构；e2e 库需先建）
EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=ehome_e2e \
  go test -tags=e2e ./internal/e2e/ -run 'TestSourceGroup|TestE' -count=1 -v
~~~

| # | 场景 | 本文件状态 | 说明 |
|---|---|---|---|
| E1 | 三种组合各触发一次 | **PASS** | 三规则各 1 条事件，复合值 avg=650±1 |
| E2 | 主备跟随且不双计 | **FAIL（真实红）** | 切换后第 2 条事件 source_edge_device_id 为空（疑似切换后走 source_degraded 分支/快照未带上来源） |
| E3 | 复合缺员 unbound | **PASS** | sum + min_members=2 + fail_closed ⇒ unbound |
| E4 | 陈旧剔除 | **PASS** | CombineGroup 对 StateStale 严格 unbound（适配层时钟注入依赖引擎接线，本层已锁契约） |
| E5 | 跨成员交替不伪连续 | **PASS** | 交替帧 4 条、0 事件（identity/S8 生效） |
| E6 | F4 断链回归（ID 分歧夹具） | **PASS** | 逻辑 ID 与 edge ID 显式错位后，绑逻辑设备的规则恰 1 条 executed |
| E7 | 切换瞬间不双计 | **FAIL（真实红）** | 冷却窗内切换后仍产生第 2 条事件（期望 1） |
| E8 | 审计可追溯 | **PASS** | source_kind/source_group_id/source_edge_device_id/members_json 全非空 |
| E9 | pending 期间切换 confirm 409 | **未实现** | prs3001 在 deviceaction 无风险动作，需 BMS 链夹具（不自造 pending 行，避免假绿） |
| E10 | 组自愈 | **PASS** | 主失败→切备→备 stale→备恢复上报→ScanGroups 自愈回 active |
| E11 | 复合交错帧冷却内至多 1 次动作 | **PASS** | 交错 4 帧仅 1 条事件 |

**夹具纪律**：两台 prs3001 光照设备**跨逻辑设备**（各自 logical_device）；设备/逻辑行可 fixture 直写（与既有 newE2EChain 先例一致），来源组/规则必须走产品路径；E6 允许直写 logical/edge 造 ID 分歧（方案 F-6 豁免）。

**反假绿**：SKIP(BLOCKED) 一律不计 PASS；报告必须区分 PASS/SKIP。