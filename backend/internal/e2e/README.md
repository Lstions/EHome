# 端到端数据流模拟验证（internal/e2e）

**回答的问题**：模拟一台边缘设备上报一帧真实协议数据后，整条数据流（边缘设备 → 数据源 → 规则引擎）与各功能反馈是否正常。

## 与 simulation/ 的分工

| 层 | 机制 | 现状 |
|---|---|---|
| simulation/（黑盒） | 起真实 cmd/server 子进程 + 真实 PG + MQTT | 已作废：MQTT 于 2026-10-08 移除，TestScenarios 不再有效 |
| internal/e2e/（本包） | 进程内组装与 cmd/server 相同的数据面（databus 消费者 + 真实 sink 注入 + 真实 PG），设备帧用真实协议布局 | 有效 |

本包只跳过 TLS 传输层（证书/握手），其余全部为生产代码路径。

## 怎么跑

真 PG（推荐，与生产一致）：

```bash
docker exec ehome-postgres psql -U ehome -d postgres -c 'CREATE DATABASE ehome_e2e;'
cd backend
EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=ehome_e2e \
  go test -tags=e2e ./internal/e2e/ -count=1 -v
```

本地 SQLite（快速回归；观测 UPSERT 自动切 MIN/MAX 方言）：

```bash
go test -tags=e2e ./internal/e2e/ -count=1
```

## 覆盖的场景

| # | 测试 | 验证链路 |
|---|---|---|
| 1 | `TestEdgeDeviceDataFlow_FullChain` | 帧 → databus → 解析 → unified_data(12 行逐值) → 最新值缓存（复现 AU-12 单槽根因 + BySensor 修复）→ 观测投影 → KnownFields → @soc 解析 → 规则触发 → automation_events + notifications |
| 2 | `TestEdgeDeviceDataFlow_FamilySelectorAndDegraded` | temperature_*:max（无静态声明的动态族）：首帧 unbound 且落审计 → 投影刷新后正常求值 → 常温不触发/超温触发；缺员降级求值语义 |
| 3 | `TestEdgeDeviceDataFlow_DatasourceThenRule` | **同一帧**驱动数据源域与规则引擎：MarkSuccess 更新 last_success/fail_count → 连续 3 次 MarkFailure ⇒ error + 3 条 health 行、单来源不产生切换日志 → 恢复上报 ⇒ standby + 计数归零；期间 unified_data 持续入库 |

## 未覆盖（诚实边界）

TLS 握手与设备证书、WebSocket 实时推送、device_action → commandexec 下行、
阈值告警引擎、前端渲染。
