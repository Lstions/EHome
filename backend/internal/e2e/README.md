# 端到端数据流模拟验证（internal/e2e）

**回答的问题**：模拟一台边缘设备上报一帧真实协议数据后，整条数据流与各功能反馈是否正常。

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

1. **完整链路**：jiabaida 0x03 真实帧 → databus → 驱动解析 → unified_data（12 行）→
   最新值缓存 → 观测投影 sensor_field_obs → 解析层已知字段 → @soc 语义解析 →
   规则触发 → automation_events(result=notification) + notifications 行。
2. **族选择器 + 冷启动**：temperature_*:max 在无静态声明的动态族上：首帧 unbound
   并落审计（不静默）→ 投影刷新后正常求值 → 常温不触发 / 超温触发；并直接验证
   "缺员降级求值"语义。

## 未覆盖（诚实边界）

TLS 握手与设备证书、WebSocket 实时推送、device_action → commandexec 下行、
阈值告警引擎、数据源健康、前端渲染。
