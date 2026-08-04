# 部署监控 (Prometheus + Alertmanager) — F6 数据落库失败告警

本目录承载 F6 (边缘设备修复优化方案-v2.md §F6) 的数据落库失败告警配置。
**这是监控配置 (deploy 资产), 不是 ehome-server 运行时源码。**

## 文件

| 文件 | 作用 |
|------|------|
| `prometheus.yml` | scrape 配置: 抓取 ehome-server `/metrics`, 加载 alert_rules, 指向 alertmanager |
| `alert_rules.yml` | PromQL 告警规则: `rate(...[1m]) > 1` 持续 2m (warning) + 10min 累计 >100 (critical) |
| `alertmanager.yml` | 告警路由与接收器 (email 示例) |
| `docker-compose.monitoring.yml` | 可选: 一键起 Prometheus + Alertmanager (见下) |

## 指标与标签

目标指标 `ehome_data_consumer_db_write_failures_total` (backend/pkg/metrics/metrics.go:213):

- 标签: `consumer` (databus 消费者名) + `table` (`device_data` / `unified_data`)
- 递增点: backend/internal/databus/consumers_heavy.go:93/:260/:305 — DB 写失败即 Inc (单调计数)
- 语义: 只计写失败。故障期间持续计数, 恢复后不递增 → `rate()[1m]` 自然回落

## 快速开始 (可选 compose)

```bash
docker compose -f docker-compose.monitoring.yml up -d
# Prometheus:     http://localhost:9090   (规则在 Status → Rules 可见)
# Alertmanager:   http://localhost:9093
```

若不使用本目录 compose, 将三个 yml 挂载到你现有的 Prometheus/Alertmanager 部署:

```yaml
# prometheus 容器 volumes
- ./deploy/monitoring/prometheus.yml:/etc/prometheus/prometheus.yml:ro
- ./deploy/monitoring/alert_rules.yml:/etc/prometheus/alert_rules.yml:ro
# alertmanager 容器 volumes
- ./deploy/monitoring/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro
```

## 验证

1. 启动 compose 后: `curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[0].rules[].name'` 应看到两条 F6 规则
2. 注入 DB 写错误 (停 postgres 或 revoke 权限), 观察:
   `rate(ehome_data_consumer_db_write_failures_total[1m]) > 1` 在 Prometheus 查询页突增
3. 告警触发后 Alertmanager `http://localhost:9093/#/alerts` 可见 `EhomeDataConsumerDBWriteFailures`

## 部署注意

- **scrape 端点是公开的**: `GET /metrics` (routes.go:55) 无认证, 仅暴露计数器与
  有限标签 (consumer/table), 不含行级数据/PII。若反向代理前置 TLS, 改 `scheme:` 为 https。
- **alertmanager.yml 含 SMTP 占位符**: 使用前把 `alert_rules.yml`/`alertmanager.yml`
  中的 `${VAR}` 替换为真实 SMTP 凭据, 或用环境变量注入。
- **阈值调整**: 正常负载下该计数器应恒为 0。`>1` / 100 为保守基线, 按实际误报调整。
- **二期 (可选)**: 需要 node_id 维度时, 在 consumers 写失败日志处加
  (node_id, error_class) 1min 窗口聚合 WARN (方案 F6 二期, 本任务不做)。
