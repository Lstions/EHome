# 部署监控 (Prometheus + Alertmanager) — 可选 profile

本目录承载 EHomeSystem 的 Prometheus 抓取、告警规则与 Alertmanager 路由配置。
**这是监控配置 (deploy 资产), 不是 ehome-server 运行时源码。**

监控以 Docker Compose profile `monitoring` 的形式内嵌在主栈根 `docker-compose.yml`
（单一真源），**默认不启动**，因此不会拖慢主栈；启用后才加入主栈默认网络，
scrape 目标 `ehome-web:8080` 才可解析。

## 文件

| 文件 | 作用 |
|------|------|
| `prometheus.yml` | scrape 配置: 抓取 ehome-server `/metrics`, 加载 alert_rules, 指向 alertmanager |
| `alert_rules.yml` | PromQL 告警规则 (4 组, 含既有 2 条 F6 数据落库规则) |
| `alertmanager.yml` | 告警路由与接收器 (email 示例) |

> 历史上曾存在的独立 monitoring compose 项目已删除: 独立 compose 项目不在 ehome
> 默认网络内, `ehome-web:8080` 无法解析, 且 volume 相对路径依赖运行目录。现统一由
> 根 `docker-compose.yml` 的 `monitoring` profile 承载。

## 启动 / 停止 (可选 profile)

```bash
# 启动主栈 + 监控 (监控是可选 profile, 不随主栈默认启动)
docker compose --profile monitoring up -d

# 只启动监控 (主栈已在运行时)
docker compose --profile monitoring up -d prometheus alertmanager

# Prometheus:   http://localhost:9090   (Status → Rules 可见告警规则)
# Alertmanager: http://localhost:9093
```

端口可用 `.env` 覆盖: `PROMETHEUS_PORT` / `ALERTMANAGER_PORT` (见根 `.env.example`)。

只校验配置、不改变运行态:

```bash
docker compose config --quiet
docker compose --profile monitoring config --quiet
```

## 告警规则清单

规则定义与指标出处详见 `alert_rules.yml` 内注释。指标唯一真源为
`backend/pkg/metrics/metrics.go` (规则注释逐条标注定义行号与递增点)。

| 规则名 | 指标 | 严重级 | 触发条件 |
|--------|------|--------|----------|
| EhomeDataConsumerDBWriteFailures | ehome_data_consumer_db_write_failures_total | warning | rate[1m] > 1, 持续 2m |
| EhomeDataConsumerDBWriteFailuresSustained | ehome_data_consumer_db_write_failures_total | critical | increase[10m] > 100, 持续 1m |
| EhomeDataEventBusDropped | ehome_data_event_bus_dropped_total | critical | rate[5m] > 0, 持续 2m |
| EhomeConfigEventBusDropped | ehome_event_bus_dropped_total | warning | rate[5m] > 0, 持续 5m |
| EhomeLogEventBusDropped | ehome_log_event_bus_dropped_total | warning | rate[5m] > 0, 持续 5m |
| EhomeWorkerPoolQueueBacklog | ehome_worker_pool_queue_size | warning | > 100, 持续 5m |
| EhomeWorkerPoolOverflow | ehome_worker_pool_overflow_total | critical | rate[5m] > 0, 持续 2m |
| EhomeWorkerPoolBackpressureBlock | ehome_worker_pool_backpressure_block_total | warning | rate[5m] > 0, 持续 5m |
| EhomeDataReportErrors | ehome_data_report_errors_total | warning | rate[5m] > 0, 持续 5m |
| EhomePendingWriteTimeouts | ehome_pendingwrite_timeout_total | warning | rate[5m] > 0, 持续 5m |
| EhomeMqttPublishFailures | ehome_mqtt_publish_failures_total | warning | rate[5m] > 0, 持续 2m |
| EhomeAllNodesOffline | ehome_nodes_online | critical | == 0 且有历史上报, 持续 15m |

**未覆盖的故障族: retention / purge / 分区任务失败** —— `backend/internal/datalifecycle`
（含 `partition_mgr.go`）当前未暴露任何 Prometheus 指标, 无法在不编造指标名的前提下
落规则。`ehome_mqtt_publish_failures_total` 已定义但当前无递增点, 对应规则为前向兼容。

## 验证

1. 启动后: `curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[].rules[].name'`
2. 静态校验:
   `docker run --rm -v "$PWD/deploy/monitoring:/c" prom/prometheus:v2.53.0 promtool check config /c/prometheus.yml`
   与 `docker run --rm -v "$PWD/deploy/monitoring:/c" prom/prometheus:v2.53.0 promtool check rules /c/alert_rules.yml`
3. 告警触发后 Alertmanager `http://localhost:9093/#/alerts` 可见。

## 部署注意

- **scrape 端点是公开的**: `GET /metrics` (routes.go:55) 无认证, 仅暴露计数器与有限
  标签, 不含行级数据/PII。若反向代理前置 TLS, 改 `scheme:` 为 https。
- **alertmanager.yml 含 SMTP 占位符**: `${ALERT_EMAIL}` 等需替换为真实凭据, 或为
  Alertmanager 增加 `--config.expand-env` 并注入环境变量。
- **阈值调整**: 正常负载下失败计数器应恒为 0; 各阈值为保守基线, 按实际误报调整。
- **保留期与数据卷**: Prometheus TSDB 保留 15d (`--storage.tsdb.retention.time=15d`),
  数据卷 `ehome-prometheus-data` / `ehome-alertmanager-data` 在根 compose 末尾声明。
