# EHomeSystem 实现文档

> **位置**: `docs/实现/`
> **对应设计**: `docs/设计/`
> **状态**: 完整实现 (P0/P1/P2/P3 全部完成)

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
| 2 | **节点** | [节点.md](节点.md) | `internal/nodemgr/*.go` | `views/collector/` + `components/collector/BusConfigPanel.vue` | `components/wifi_mgr/`, `msg_handler/`, `ehome_mqtt/` |
| 3 | **设备** | [设备.md](设备.md) | `internal/api/handler_device.go` | `views/device/` | `components/proto_engine/`, `msg_handler/` |
| 4 | **通道** | [通道.md](通道.md) | `internal/api/handler_device.go` (channel 段) | `components/channel/` | `components/bus/`, `config_mgr/` |
| 5 | **设备模板** | [设备模板.md](设备模板.md) | `internal/api/handler_device.go` (device-configs 段) | `views/config/DeviceConfigList.vue` | - |
| 6 | **数据采集** | [数据采集.md](数据采集.md) | `internal/nodemgr/handler_data.go` | `views/data/DataPanel.vue` | `components/scheduler/`, `bus/`, `drivers/` |
| 7 | **固件OTA** | [固件OTA.md](固件OTA.md) | `internal/ota/ota.go` (P3 修) | `views/firmware/FirmwareManage.vue` | `components/ota_updater/`, `proto_engine/` |
| 8 | **通知中心** | [通知中心.md](通知中心.md) | `internal/api/handler_notification.go` | `views/notification/` | - |
| 9 | **系统监控** | [系统监控.md](系统监控.md) | `internal/api/handler_metrics.go` (P2) | `views/monitor/Monitor.vue` (P2) | - |

## 📊 实现完整度总览

| 模块 | 后端代码 | 前端代码 | 端点数 | 测试覆盖 |
|------|---------|---------|--------|---------|
| 认证授权 | ✅ 完整 | ✅ 完整 | 10 | 🟢 23 测试 |
| 节点 | ✅ 完整 | ✅ 完整 | 8+ | 🟡 2 测试 |
| 设备 | ✅ 完整 | ✅ 完整 | 8 | 🟡 待补 |
| 通道 | ✅ 完整 | ✅ 完整 | 6 | 🟡 待补 |
| 设备模板 | ✅ 完整 (P3) | ✅ 完整 (P3) | 7 | 🟢 E2E 10 |
| 数据采集 | ✅ 完整 | ✅ 完整 | 4 | 🟡 待补 |
| 固件OTA | ✅ 完整 (P3) | ✅ 完整 | 7 | 🟢 单元 + E2E 10 |
| 通知中心 | ✅ 基础 | ✅ 基础 | 5 | 🔴 待补 |
| 系统监控 | ✅ 完整 (P2) | ✅ 完整 (P2) | 3 | 🟡 待补 |

## 🔧 通用技术栈

### 后端
- 语言: Go 1.23
- Web 框架: Gin 1.10
- ORM: GORM 2.x
- DB: PostgreSQL 15 (含 JSONB)
- 缓存: Redis 7
- MQTT 客户端: Paho MQTT Go
- 协议: 手写二进制帧 (无 protobuf)
- 日志: 结构化 JSON
- 指标: Prometheus client
- 配置: YAML (`config.yaml`)

### 前端
- 框架: Vue 3 (Composition API)
- 构建: Vite 8
- 路由: vue-router 4
- 状态: Pinia 3
- UI: Element Plus 2.x (按需)
- 图表: ECharts 6 (按需)
- 类型: TypeScript 5
- 国际化: vue-i18n 9
- 测试: vitest + @vue/test-utils
- 风格: scoped + BEM-like
- HTTP: axios + interceptors

### 节点
- RTOS: FreeRTOS (ESP-IDF v5.x)
- 协议: 手写二进制帧 (跟后端共用头文件概念)
- 持久化: NVS (加密分区)
- 升级: A/B partition + 30s mark valid
- 状态: RGB LED (WS2812)
- WiFi: ESP-IDF 原生 + reconnect task
- MQTT: ESP-IDF 原生
- 构建: `idf.py build` → `idf.py -p /dev/ttyACM0 flash`

## 📦 部署/构建命令

```bash
# 后端
cd backend
go build -o ehome-server ./cmd/server
./ehome-server

# 前端 dev
cd frontend-shared
pnpm dev  # http://localhost:5174

# 前端 build
pnpm build  # → dist/

# 节点
cd esp32-collector
idf.py build
idf.py -p /dev/ttyACM0 flash
```

## 🔗 关联文档

- [设计/README.md](../设计/README.md) — 设计文档索引
- [设计/总体设计.md](../设计/总体设计.md) — 架构 + 数据流 + 协议
- 每个模块的 [设计/<功能>/详细设计.md] + [验收标准.md]
