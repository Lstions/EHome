# EHomeSystem 实现文档

> **位置**: `docs/实现/`
> **状态**: v3.3 已交付 + v3.4 已落地（时序化 5/5 / 告警 3/4 / Redis 退役 4/4）。本目录自 2026-08-22 体系重建起仅存索引——模块实现记录已并入 [../设计/](../设计/) 三合一模块文档（§6 实现记录节）。

## 📖 如何找实现信息

| 你要找 | 去这里 |
|--------|--------|
| 模块代码位置（后端/前端/固件） | [../设计/](../设计/README.md) 模块文档清单 → 对应文档 §6 |
| 模块验收与测试覆盖 | 对应模块文档 §7 |
| 技术栈与构建命令 | 本文 §🔧 |
| 运维操作 | [../操作/](../操作/)（刷机 / 认证运维） |

## 🗺 代码地图（速查）

| 域 | 后端（backend/） | 前端（frontend-shared/） | 固件（esp32-collector/） |
|----|-----------------|------------------------|--------------------------|
| 认证 | internal/api/handler_auth.go, handler_account.go, internal/auth/ | views/auth/, stores/user.ts | — |
| 节点 | internal/nodemgr/, handler_node.go | views/node/（NodeList/NodeDetail/NodeOverview） | main/, components/{msg_handler,config_mgr,sync_manager,hw_profile}/ |
| 通道 | handler_device.go, internal/terminal/ | components/channel/（ChannelTerminal） | components/{bus_dma,bus_manager,bus_worker}/ |
| 设备配置 | handler_device.go, handler_vendor.go | views/config/DeviceConfigList.vue | — |
| 边缘设备 | handler_edge_device.go, handler_edge_device_lifecycle.go | views/edge-device/ | components/scheduler/ |
| 控制域 | internal/{drivers,deviceaction,commandexec}/, handler_device_operation.go | views/edge-device/shared/DeviceControlPanel.vue | components/msg_handler/handler_channel_cmd_v2.c |
| 数据链 | internal/databus/, pkg/parser/, handler_data.go | views/data/DataPanel.vue | components/{scheduler,bus_worker}/ |
| 生命周期 | internal/datalifecycle/ | views/logical-device/ | — |
| OTA | internal/ota/, handler_ota.go | views/firmware/FirmwareManage.vue | components/ota/ |
| 通知 | handler_notification.go | MainLayout.vue 铃铛 | — |
| 监控 | handler_metrics.go, handler_overview.go | views/monitor/Monitor.vue | — |
| 外设直控 | handler_periph.go | components/periph/ | components/{gpio_ctrl,pwm_ctrl,periph_owner}/ |
| 日志流 | internal/logstream/, handler_logstream.go | views/node/LogPanel.vue | components/log_stream/ |

## 🔧 通用技术栈与构建

### 后端

| 后端 | Go（go 1.26）、Gin、GORM、PostgreSQL（compose 用 postgres:18-alpine）、Paho MQTT、手写二进制帧（无 protobuf）、结构化日志、Prometheus、env 配置（EHOME_*）。 |

### 前端

- Vue 3.5（Composition API）、Vite 8、vue-router 5、Pinia 3、Element Plus、ECharts 6、TypeScript strict、vitest 4、happy-dom、axios（统一 client）。pnpm。

### 节点

- ESP-IDF v5.x（⚠️ v6.0 勿用）、FreeRTOS、S3/C6 双目标、NVS、A/B 分区、WS2812 RGB 状态灯、WiFi STA + SoftAP 配网。

### 构建

```bash
make up          # 启动基础设施 + 前后端
make backend     # 仅后端 (:8082)
make frontend    # 仅前端 (:5174)
make test        # 全部测试（后端 SQLite + 前端 vitest）
make test-integration  # PG 特性集成测试（EHOME_TEST_DB=postgres）
make lint        # 静态检查

cd backend && go test ./...
cd frontend-shared && pnpm build && pnpm test:run && pnpm typecheck
cd esp32-collector && idf.py set-target esp32s3|esp32c6 && idf.py build && idf.py flash
```

### 质量门禁

- 覆盖率阈值：后端 35% / 前端 25%（lines/functions/statements）。
- 测试反模式禁止：恒真断言、catch 吞 expect、waitForTimeout 硬等待、wrapper.vm 代替交互。
- 协议变更：scripts/check_protocol_fields.sh 门禁。

### 部署

```bash
docker compose up -d    # 单容器 ehome-web（API+SPA）
# v3.4 演进：Redis 退役后 compose 服务 4→3（postgres/emqx/ehome）
```

## 🔗 关联文档

- [../设计/README.md](../设计/README.md) — 设计文档索引
- [../设计/总体设计.md](../设计/总体设计.md) — 架构 + 数据流 + 协议
- [../README.md](../README.md) — 文档中心
