# 端到端数据流模拟验证（internal/e2e）

回答的问题：模拟一台边缘设备（BMS）上报一帧真实协议数据后，**数据源管理 → 规则引擎 → 设备动作 → 下行命令**整条闭环是否正常。

## 与 simulation/ 的分工

| 层 | 机制 | 现状 |
|---|---|---|
| simulation/（黑盒） | 起真实 cmd/server 子进程 + 真实 PG + MQTT | 已作废：MQTT 于 2026-10-08 移除，TestScenarios 不再有效 |
| internal/e2e/（本包） | 进程内组装与 cmd/server 相同的数据面与控制面（databus 消费者 + 真实 sink 注入 + 真实 datasource/automation/commandexec/dispatcher + 真实 PG） | **有效** |

本包只跳过 TLS 传输层（证书/握手）与 WebSocket 客户端，其余全部为生产代码路径。
下行用捕获型 Publisher 替代 TCP session，再用真实 `downlink.Bridge` 校验 3.0 外框。

## 怎么跑

```bash
docker exec ehome-postgres psql -U ehome -d postgres -c 'CREATE DATABASE ehome_e2e;'
cd backend
EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=ehome_e2e \
  go test -tags=e2e ./internal/e2e/ -count=1 -v
```

本地 SQLite（快速回归）：`go test -tags=e2e ./internal/e2e/ -count=1`

## 场景清单（5 个，全 PASS）

| # | 测试 | 验证链路 |
|---|---|---|
| 1 | `TestEdgeDeviceDataFlow_FullChain` | 帧 → databus → 解析 → unified_data(12 行逐值) → 最新值缓存（复现 AU-12 单槽根因 + BySensor 修复）→ 观测投影 → KnownFields → @soc 解析 → 规则触发 → automation_events + notifications |
| 2 | `TestEdgeDeviceDataFlow_FamilySelectorAndDegraded` | temperature_*:max（动态族）：首帧 unbound 且落审计 → 投影刷新后求值 → 常温不触发/超温触发；缺员降级语义 |
| 3 | `TestEdgeDeviceDataFlow_DatasourceThenRule` | 同一帧驱动数据源域与规则引擎：MarkSuccess → 连续 3 次失败 ⇒ error + health 行 → 恢复 ⇒ standby + 清零；unified_data 持续入库 |
| **4** | **`TestBMS_SunEventSwitchesMOS`** | **完整业务闭环**：BMS 帧 → 数据源健康 → **日出 sun_event 触发** → `set_mos_policy`（高风险 device_action，规则级授权 O-2）→ commandexec 九道门禁 → dispatcher 出队 → 内层 ChannelCmdV2 payload + 3.0 外框校验；并验证同日重复 tick 不重复下发 |
| **5** | **`TestBMS_SunEventMOS_RequiresRiskAck`** | **负对照**：同一规则未授权风险动作 ⇒ `confirmation_required`，**零下行帧** |

### 场景 4/5 的夹具要求（踩过的坑）

`set_mos_policy` 是 `bounded_sequence` 动作，commandexec 门禁要求节点上报
`{"supports_channel_cmd_v2":true,"supports_bounded_batch":true,"supports_finally":true,"max_batch_steps":8,...}`，
并且节点需 `Status=online` + `ConfigStatus=applied` + `BootID` + 新鲜 `ResourceReportedAt`
+ `hardware_info.channels` 含该通道 + 存在 `system_admin` 用户（Planner 惰性解析 system actor）。
缺任何一项都会落 `failed_gate` / `failed_dispatch` —— 那是夹具不全，不是被测对象的问题。

## 未覆盖（诚实边界）

TLS 握手与设备证书、WebSocket 客户端推送、节点侧真实固件执行（ACK/Final 回传）、
阈值告警引擎、数据源主备切换（需要两个来源）、前端渲染。
