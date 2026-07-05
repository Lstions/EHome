# EHomeSystem 实现文档

> **位置**: `docs/实现/`
> **对应设计**: `docs/设计/`
> **状态**: v2.5 完整实现 (P0-P3 + BMS/逆变器驱动 + 通道终端 + 逐指令 ConfigManifest + 生产单容器部署)

## 📖 阅读路径

| 你的身份 | 先看 | 然后看 |
|---------|------|--------|
| 🆕 新人 | [设计/总体设计.md](../设计/总体设计.md) | 你负责的 [实现/<功能>.md] |
| 🔍 Review | [实现/<功能>.md] | [设计/<功能>/详细设计.md](../设计/<功能>/详细设计.md) + [验收标准.md](../设计/<功能>/验收标准.md) |
| 🧪 验证 | [实现/<功能>.md] (末段 "测试覆盖") | [验收标准.md](../设计/<功能>/验收标准.md) |
| 🐛 Debug | [实现/<功能>.md] (代码位置) | [设计/<功能>/详细设计.md] 状态机/容错章节 |

## 📦 模块索引

| # | 模块 | 实现文档 | 后端 | 前端 | 节点端 |
|---|------|---------|------|------|----------|
| 1 | **认证授权** | [认证授权.md](认证授权.md) | `internal/api/handler_auth.go` + `handler_user.go` | `views/auth/` + `views/admin/UserList.vue` + `views/profile/` | - |
| 2 | **节点** | [节点.md](节点.md) | `internal/nodemgr/*.go`<br/>`internal/api/handler_node.go` (含 DMA API) | `views/node/` + `views/node/NodeDetail.vue` (DMA 面板) | `main/` (app_state + bus_manager)<br/>`msg_handler/` (含 handler_config/hello/data/writecmd)<br/>`hw_profile/` (含 hw_tables)<br/>`dma_pool/` |
| 3 | **边缘设备** | [边缘设备.md](边缘设备.md) | `handler_edge_device.go` | `views/edge-device/` | `components/proto_engine/`, `msg_handler/` |
| 4 | **通道** | [通道.md](通道.md) | `internal/api/handler_node.go` (通道 DMA 配置)<br/>`internal/api/handler_device.go` (channel 段) | `components/channel/` | `components/bus_dma/`, `config_mgr/`, `bus_manager.c` |
| 5 | **设备配置** | [设备配置.md](设备配置.md) | `handler_device_config.go` | `views/config/DeviceConfigList.vue` | - |
| 6 | **数据采集** | [数据采集.md](数据采集.md) | `internal/nodemgr/handler_data.go` | `views/data/DataPanel.vue` | `components/scheduler/`, `bus_worker.c` (TX/RX 分离) |
| 7 | **固件OTA** | [固件OTA.md](固件OTA.md) | `internal/ota/ota.go` | `views/firmware/FirmwareManage.vue` | `components/ota/` |
| 8 | **通知中心** | [通知中心.md](通知中心.md) | `internal/api/handler_notification.go` | `views/notification/` | - |
| 9 | **系统监控** | [系统监控.md](系统监控.md) | `internal/api/handler_metrics.go` (P2) | `views/monitor/Monitor.vue` (P2) | - |
| 10 | **DMA 资源管理** | — | `internal/api/handler_node.go` (dma-channels/dma-config)<br/>`internal/nodemgr/handler_resources.go` | `views/node/NodeDetail.vue` (DMA 面板) | `components/dma_pool/`<br/>`components/hw_profile/` (ResourceReport field8) |
| 11 | **UART DMA 优化** | [UART-DMA优化.md](UART-DMA优化.md) | — | — | `components/bus_dma/`, `components/uart0_boot/` |
| **12** | **设备驱动 (v2.5)** | — | `internal/drivers/` (BMP280/LKTH01/SN3000/PRS3001/Jiabaida BMS/Techfine 逆变器) | `views/edge-device/` (BMS/逆变器详情) | `components/bus_worker/` |
| **13** | **通道终端 (v2.5)** | — | `internal/terminal/` (WebSocket 终端) | `views/channel/` (ChannelTerminal) | UART 透明管道 |
| **14** | **逐指令 ConfigManifest (v2.5.16+)** | — | `internal/drivers/command_template.go` | `views/channel/` (CommandList) | `components/sync_manager/` |
| **15** | **统一解析器 (v2.5)** | — | `pkg/parser/` (替代旧 3 套解析系统) | — | — |
| **16** | **服务端降采样 (v2.5)** | — | `internal/api/handler_data.go` (/historical-batch + max_points) | `views/data/` (图表降采样) | — |

## 📊 实现完整度总览

| 模块 | 后端代码 | 前端代码 | 端点数 | 测试覆盖 |
|------|---------|---------|--------|---------|
| 认证授权 | ✅ 完整 | ✅ 完整 | 10 | 🟢 23 测试 |
| 节点 | ✅ 完整 | ✅ 完整 | 8+ | 🟢 完整 |
| 边缘设备 | ✅ 完整 | ✅ 完整 | 8 | 🟢 完整 |
| 通道 | ✅ 完整 | ✅ 完整 | 6 | 🟢 完整 |
| 设备配置 | ✅ 完整 (P3) | ✅ 完整 (P3) | 7 | 🟢 E2E 10 |
| 数据采集 | ✅ 完整 | ✅ 完整 | 4+ | 🟢 完整 |
| 固件OTA | ✅ 完整 (P3) | ✅ 完整 | 7 | 🟢 单元 + E2E 10 |
| 通知中心 | ✅ 完整 | ✅ 完整 | 5 | 🟢 完整 |
| 系统监控 | ✅ 完整 (P2) | ✅ 完整 (P2) | 3 | 🟢 完整 |
| 设备驱动 (v2.5) | ✅ 6 驱动 | ✅ BMS/逆变器 | — | 🟢 8 驱动全覆盖 |
| 通道终端 (v2.5) | ✅ 完整 | ✅ 完整 | 1 (WS) | 🟢 完整 |
| 逐指令 Manifest (v2.5.16+) | ✅ 完整 | ✅ 完整 | — | 🟢 完整 |
| 统一解析器 (v2.5) | ✅ 完整 | — | — | 🟢 完整 |
| 降采样+gzip (v2.5) | ✅ 完整 | ✅ 完整 | 2 | 🟢 完整 |
| **总计** | **114 Go 文件** | **144 Vue/TS 文件** | **~60** | **45 后端 + 31 前端测试** |

## 🔧 通用技术栈

### 后端
- 语言: Go 1.25
- Web 框架: Gin
- ORM: GORM 2.x
- DB: PostgreSQL 16 (含 JSONB)
- 缓存: Redis 7
- MQTT Broker: EMQX 5.7
- MQTT 客户端: Paho MQTT Go
- 协议: 手写二进制帧 (protobuf wire 兼容, 无 protobuf 依赖)
- 日志: 结构化 JSON
- 指标: Prometheus client + `/metrics` 端点
- 配置: 环境变量 (EHOME_*) + YAML

### 前端
- 框架: Vue 3.5 (Composition API)
- 构建: Vite 8
- 路由: vue-router 5
- 状态: Pinia 3
- UI: Element Plus 2.13 (按需)
- 图表: ECharts 6 (按需)
- 类型: TypeScript 5.9
- 国际化: vue-i18n 9
- 测试: vitest 4 + @vue/test-utils 2
- 风格: scoped + Tailwind CSS
- HTTP: axios + interceptors

### 节点
- RTOS: FreeRTOS (ESP-IDF v5.x)
- 目标芯片: ESP32-S3 + ESP32-C6 (双目标构建, 独立 sdkconfig/partitions)
- 协议: 手写二进制帧 (跟后端共用头文件概念)
- 持久化: NVS (加密分区)
- 升级: A/B partition + 30s mark valid
- 状态: RGB LED (WS2812)
- WiFi: ESP-IDF 原生 + reconnect task
- MQTT: ESP-IDF 原生
- UART: 透明管道模式 (UART 空闲检测替代帧分隔)
- DMA: 多总线并行, 统一 bus_dma 架构
- 构建: `idf.py set-target esp32s3|esp32c6` → `idf.py build` → `idf.py flash`

## 📦 部署/构建命令

```bash
# 本地开发 (推荐)
make up          # 启动基础设施 (PG/Redis/EMQX) + 前后端
make test        # 全部测试
make lint        # 静态检查

# 后端单独
cd backend && go run ./cmd/server/

# 前端 dev
cd frontend-shared && pnpm dev   # http://localhost:5174

# 前端 build (生产前必须执行)
cd frontend-shared && pnpm build  # → dist/

# ESP32 固件
cd esp32-collector
idf.py set-target esp32s3   # 或 esp32c6
idf.py build
idf.py flash

# 生产部署
docker compose up -d         # 单容器 ehome-web (API+SPA)
docker compose down          # 停止
```

## 🔗 关联文档

- [设计/README.md](../设计/README.md) — 设计文档索引
- [设计/总体设计.md](../设计/总体设计.md) — 架构 + 数据流 + 协议
- 每个模块的 [设计/<功能>/详细设计.md] + [验收标准.md]
