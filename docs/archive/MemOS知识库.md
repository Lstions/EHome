# EHomeSystem 项目知识库

## 项目概述

EHomeSystem（家庭数字化系统）是一个物联网边缘设备管理系统（能源管理方向），支持多协议传感器采集、BMS 监控、逆变器管理、OTA 升级。

- **项目路径**: `/home/sun/workspace/EHomeSystem`
- **项目状态**: 活跃开发中，版本 v2.5~v2.7+（文档版本混乱）
- **描述**: 管理、监控、配置边缘设备（逆变器、BMS、传感器等）的 IoT 平台

## 技术栈

### 后端
- **语言**: Go 1.25
- **Web 框架**: Gin
- **ORM**: GORM 2.x
- **数据库**: PostgreSQL 16（含 JSONB）
- **缓存**: Redis 7
- **MQTT Broker**: EMQX 5.7（MQTT 5.0, QoS 1）
- **MQTT 客户端**: Paho MQTT Go
- **协议**: 手写二进制帧（protobuf wire 兼容，无 protobuf 依赖）
- **日志**: 结构化 JSON
- **指标**: Prometheus client + `/metrics` 端点
- **配置**: 环境变量 (`EHOME_*`) + YAML
- **API 前缀**: `/api/v1`
- **后端文件**: 114 Go 文件

### 前端
- **框架**: Vue 3.5（Composition API）+ TypeScript 5.9
- **构建**: Vite 8
- **路由**: vue-router 5
- **状态管理**: Pinia 3
- **UI 库**: Element Plus 2.13（按需）
- **图表**: ECharts 6（按需）
- **国际化**: vue-i18n 9
- **测试**: vitest 4 + @vue/test-utils 2
- **HTTP**: axios + interceptors
- **风格**: scoped CSS + Tailwind CSS
- **前端文件**: 144 Vue/TS 文件
- **Pinia stores**: 10 个
- **API 模块**: 17 个

### 固件
- **RTOS**: FreeRTOS（ESP-IDF v6.0.1）
- **目标芯片**: ESP32-S3 + ESP32-C6（双目标构建）
- **协议**: 手写二进制帧（与后端共用头文件概念）
- **持久化**: NVS（加密分区）
- **升级**: A/B partition + 30s mark valid
- **状态指示**: RGB LED (WS2812)
- **WiFi**: ESP-IDF 原生 + reconnect task
- **MQTT**: ESP-IDF 原生
- **UART**: 透明管道模式（UART 空闲检测替代帧分隔）
- **DMA**: 多总线并行，统一 bus_dma 架构
- **构建 Profile**: c6-n8 / c6-n16 / s3-n8 / s3-n16

### 开发环境
| 服务 | 开发/测试端口 | 生产端口 |
|------|--------------|---------|
| PostgreSQL | 5435 | 5432 |
| Redis | 6380 | 6379 |
| EMQX MQTT | 1884 | 1883 |
| EMQX WebSocket | 8084 | 8083 |
| EMQX Dashboard | 18084 | 18083 |
| 后端 API | 8082 | 8080 |
| 前端 | 5174 | 80 |

### 目录结构
```
EHomeSystem/
├── backend/                  # Go 后端服务
│   ├── cmd/server/           # 入口
│   ├── config/               # 配置加载
│   ├── internal/
│   │   ├── api/              # REST API + WebSocket 路由
│   │   ├── config/           # 配置管理
│   │   ├── database/         # GORM + 迁移
│   │   ├── deviceinit/       # 设备初始化
│   │   ├── drivers/          # 设备驱动
│   │   ├── events/           # 事件系统
│   │   ├── homeassistant/    # HA 集成
│   │   ├── models/           # 数据模型 (20+ 表)
│   │   ├── mqtt/             # MQTT 客户端
│   │   ├── nodemgr/          # 节点管理器
│   │   ├── offlinedetector/  # 离线检测
│   │   ├── ota/              # OTA 升级管理
│   │   ├── pendingwrite/     # 写操作队列
│   │   ├── redis/            # Redis 客户端
│   │   ├── seed/             # 数据初始化
│   │   ├── terminal/         # 通道终端 WebSocket
│   │   └── websocket/        # WebSocket Hub
│   └── pkg/
│       ├── frame/            # 二进制帧协议
│       ├── logger/           # 日志
│       ├── metrics/          # Prometheus 指标
│       └── parser/           # 统一解析器架构
├── esp32-collector/           # ESP32 固件
│   ├── main/                 # 主程序
│   ├── components/           # 18 个组件
│   ├── build_firmware.sh     # C6/S3、N8/N16 profile 构建入口
│   └── sdkconfig.defaults*
├── frontend-shared/          # Vue3 前端
│   └── src/
│       ├── api/              # 17 个 API 模块
│       ├── components/       # 公共组件
│       ├── composables/      # 组合式函数
│       ├── stores/           # Pinia stores (10 个)
│       └── views/            # 页面 (13 个模块)
├── docs/                     # 项目文档
├── scripts/                  # 构建脚本
├── Makefile                  # 开发工具
├── Dockerfile                # 生产构建
└── docker-compose.yml        # 生产部署
```

## 架构

### 四层概念模型（v2.2 权威定义）

```
Layer 4: 平台 (Platform)        — 整个系统 (EHomeSystem)
Layer 3: 中心端 (Center)        — Go 后端 + 数据库 + 规则引擎
Layer 2: 节点 (Node)            — ESP32 物理边缘设备
Layer 1: 边缘设备 (Edge Device)  — 节点上接入的具体传感器/执行器
```

**关键原则**: 每一层只与它的直接上下层对话。

### 核心概念（8 个核心术语）

1. **节点 (Node)**: 物理嵌入式设备（ESP32-C6/S3），运行 EHomeSystem 固件，通过 MQTT 与中心端通信。唯一 ID: `node_id`（如 `esp32c6_404CCA57B7BC`）。旧名"采集器/Collector"已废弃。

2. **通道 (Channel)**: 节点上的物理通信端点（UART/I2C/SPI/GPIO/ADC），描述引脚、速率、模式等物理参数。1 节点最多 32 通道。通道 ≠ 挂着的设备。

3. **设备配置 (Device Config)**: 一种设备型号的协议无关元数据定义。包含连接参数、数据解析器(parser)、初始化流程(init_flow)。与具体节点/通道解耦。旧名"设备模板/Device Template"已废弃。

4. **边缘设备 (Edge Device)**: Node + Channel + DeviceConfig 三元组的实例化。数据采集的实际载体（所有 DataReport 关联到 EdgeDevice）。有自己的 `interval_ms`（采集频率）和 `enabled` 状态。

5. **数据中心/中心端 (Center)**: Go 后端，通过 ConfigEventBus + SyncGate 推送到节点，接收节点的 DataReport/StatusReport。

6. **设备驱动 (Driver)**: 全局注册表 + ConfigParser 架构，支持 DeviceConfig 级别的解析器覆盖。

### 已注册驱动
- BMP280（Bosch 温湿度气压传感器）- SPI/I2C
- LKTH01（蓝控 温湿度传感器）
- SN3000（多功能传感器）
- PRS3001（雨量+光照传感器）
- Jiabaida BMS（佳百利电池管理系统）- RS485/Modbus
- Techfine 逆变器（泰琪丰光伏逆变器）- RS485/Modbus

### 审计数据
- Driver 元数据冲突时以用户声明为准
- lk_th01 OEM = "蓝控"，techfine = "泰琪丰", HW = ["uart"]

### 关键架构决策
- **多总线事件驱动**: 已实现并合入 main，包含 bus_worker 事件驱动 RX + 异步报告、bus_manager 控制器租约、scheduler 队列分发
- **GPIO/PWM 独立控制**: 不经过通道系统，使用 PeriphCmd(0x1B)/PeriphRsp(0x1C) 协议，独立 GPIOConfig/PWMConfig API
- **ESP32 硬件资源**: 必须完全来自 ResourceReport 上报，后端不得硬编码 fallback
- **ConfigEventBus + SyncGate**: v2.1 同步机制
- **数据总线**: DataEventBus 已在 databus/bus.go 实现
- **节点同步**: sync_state 包括 idle(0) / syncing(1) / error(2) 三态
- **在线时长**: online_duration = ESP32 uptime（非会话时长），前端用 now - last_online_time 计算

### 认证
- 单用户/单管理员模式
- 密码要求 ≥8 位
- JWT: 24h 有效期，"记住我" 7 天
- 401 分类: 登录页 401 不清除会话，非登录页 401 才跳转登录
- 429 限流: 需解析 Retry-After 头

## 二进制帧协议

### 帧格式
1 字节 type + protobuf wire 格式 key-value 字段。field_number ≤ 15 时 tag 为 1 字节，≥ 16 时为多字节 varint（v2.7+）。

### 消息类型（共 28 种）
| Type | Hex | 方向 | 用途 |
|------|-----|------|------|
| Hello | 0x01 | ESP→SVR | 启动握手 |
| StatusRpt | 0x02 | ESP→SVR | 心跳/状态 |
| DataRpt | 0x03 | ESP→SVR | 传感器数据上报 |
| ConfigMfst | 0x04 | SVR→ESP | 全量配置下发 |
| ConfigRslt | 0x05 | ESP→SVR | 配置应用确认 |
| WriteCmd | 0x06 | SVR→ESP | 交互命令 |
| WriteRsp | 0x07 | ESP→SVR | 命令确认 |
| Ping | 0x08 | SVR→ESP | 延迟测量 |
| Pong | 0x09 | ESP→SVR | 延迟响应 |
| OtaCmd | 0x0A | SVR→ESP | 固件升级命令 |
| OtaProg | 0x0B | ESP→SVR | OTA 进度 |
| ScanRpt | 0x0C | ESP→SVR | I2C 扫描结果 |
| ScanReq | 0x0D | SVR→ESP | I2C 扫描请求 |
| QueryReq | 0x0E | SVR→ESP | 通用查询请求 |
| QueryRsp | 0x0F | ESP→SVR | 通用查询响应 |
| ConfigQuery | 0x10 | SVR→ESP | 配置查询请求 |
| ConfigReport | 0x11 | ESP→SVR | 保存的配置上报 |
| HelloAck | 0x12 | SVR→ESP | Hello 确认/握手关联 |
| ConfigSyncReq | 0x13 | ESP→SVR | v2.1 配置同步请求 |
| ConfigSyncRsp | 0x14 | SVR→ESP | v2.1 配置同步响应 |
| ChannelCmdV2 | 0x15 | SVR→ESP | 版本化单步控制命令 (v2.7+) |
| ChannelCmdV2Ack | 0x16 | ESP→SVR | V2 命令接纳确认 (v2.7+) |
| ChannelCmdV2Final | 0x17 | ESP→SVR | V2 命令终态回执 (v2.7+) |
| ResourceReport | 0x19 | ESP→SVR | 硬件资源上报 |
| QueryResources | 0x1A | SVR→ESP | 查询硬件资源 |
| PeriphCmd | 0x1B | SVR→ESP | GPIO/PWM 外设控制命令 |
| PeriphRsp | 0x1C | ESP→SVR | GPIO/PWM 外设控制响应 |
| LogStream | 0x1D | ESP→SVR | 日志流 |

### MQTT Topics
- `nodes/{node_id}/up` — 节点→中心端（QoS 1，日志流为 0）
- `nodes/{node_id}/down` — 中心端→节点（QoS 0）
- `nodes/{node_id}/control` — 中心端→节点（QoS 1，可靠控制入口）

### 节点连接与会话流程
```
ESP32 → MQTT connect + subscribe → Hello(0x01, nonce) 
     → HelloAck(0x12, 同 nonce) → ResourceReport(0x19) 
     → ConfigManifest(0x04) → ConfigResult(0x05) → StatusReport(0x02, 每5秒)
```

### 节点固件组件（22 个）
bus_dma, bus_manager, bus_worker, config_mgr, ehome_mqtt, frame, gpio_ctrl, hw_profile, led_strip, msg_handler, ota, pwm_ctrl, periph_owner, rgb_led, scheduler, sync_manager, transport, uart, wifi_mgr, nvs, log_stream, dma_pool

### 字段约定
- Hello: `1 node_id, 2 firmware_version, 3 model, 4 channel_count, 5 config_epoch, 6 nvs_has_config, 7 last_manifest, 8 protocol_version, 9 handshake_nonce`
- 协议版本: 当前固件 `proto_version = "2.6"`
- ConfigManifest 容量上限: 16 模板、8 通道、每通道 5 边缘设备、每边缘设备 3 命令、8 DMA 配置、12 GPIO、8 PWM

## 路由清单（14 个页面）

| 路由 | 页面名 | 类型 | 菜单可见 |
|------|--------|------|---------|
| `/login` | 登录页 | 认证 | - |
| `/dashboard` | 仪表盘 | 核心 | ✓ |
| `/node` | 节点列表 | 核心 | ✓ |
| `/node/:id` | 节点详情 | 详情 | hidden |
| `/channel` | 通道管理 | 核心 | ✓ |
| `/edge-device` | 边缘设备列表 | 核心 | ✓ |
| `/edge-device/:id` | 边缘设备详情 | 详情 | hidden |
| `/data` | 数据面板 | 核心 | ✓ |
| `/firmware` | 固件管理 | 核心 | ✓ |
| `/device-configs` | 配置模板 | 核心 | ✓ |
| `/monitor` | 系统监控 | 核心 | ✓ |
| `/profile` | 个人设置 | 设置 | hiddenInMenu |
| `/403` | 无权限页 | 异常 | - |
| `/:pathMatch(.*)*` | 404 页 | 异常 | - |

## Makefile 构建命令

| 命令 | 用途 |
|------|------|
| `make dev` | 一键启动完整开发环境 |
| `make infra` | 仅启动基础设施 (PG/Redis/EMQX) |
| `make backend` | 仅启动后端 (:8082) |
| `make frontend` | 仅启动前端 (:5174) |
| `make test` | 全部测试（Go + Vitest） |
| `make test-backend` | 后端单元测试 |
| `make test-frontend` | 前端 Vitest |
| `make test-integration` | 集成测试 (PostgreSQL) |
| `make test-coverage` | 覆盖率报告 |
| `make lint` | 代码检查 (go vet + tsc --noEmit) |
| `make build` | 生产构建 |
| `make up` | 生产 Docker Compose 部署 |
| `make down` | 停止生产环境 |
| `pnpm dev` | 前端开发服务器 |
| `pnpm build` | 前端生产构建 |

## 测试基础设施

### 测试状态
- **前端 Vitest**: 718 通过 / 1 跳过 / 0 失败，70 个测试文件 100% 通过
- **后端 Go**: 27 个包全部 ok，覆盖率 60.7%
- **固件 Host 测试**: ~6000 行（多总线事件驱动）
- **覆盖率阈值**: 后端 35%，前端 25%
- **前端语句覆盖率**: 46.35%

### 测试级别（L0-L2）
- L0: 格式、静态检查、Go/TS/C 编译、固件目标构建
- L1: 后端、固件 Host、前端白盒单元测试
- L2: 真实 DB schema + fake MQTT/transport + host firmware

### 测试规范
- Vitest 配置 setupFiles 注册 Element Plus stub
- 禁止反模式: 恒真断言(||true)、catch 吞没 expect、waitForTimeout 硬等待、wrapper.vm 替代用户交互
- 用 CDP (browser_*) 做前端验证，vision_analyze 做图像理解
- 方案审查闭环: delegate_task 派 subagent → 并行审查 → 修订 → 复审确认

### 后端主要 handler 文件
- `handler_auth.go` — 登录/JWT 认证
- `handler_node.go` — 节点 CRUD + 状态 + DMA 配置
- `handler_device_config.go` — 设备配置 CRUD
- `handler_edge_device.go` — 边缘设备 CRUD + 操作
- `handler_channel.go` — 通道管理
- `handler_data.go` — 数据查询 + 历史批量/降采样
- `handler_firmware.go` — 固件 OTA 管理
- `handler_monitor.go` — 系统监控
- `handler_profile.go` — 个人设置
- `handler_notification.go` — 通知中心
- `handler_metrics.go` — Prometheus 指标

## 开发状态

| 阶段 | 状态 |
|------|------|
| Phase 0-4: 协议 + 后端 + ESP32 | ✅ 完成 |
| Phase 5: OTA + Ping/Pong + Scan | ✅ 完成 |
| Phase 6: 前端复用 | ✅ 完成 |
| Phase 7: 测试 + 文档 | ✅ 完成 |
| v2.5: BMS 驱动 + 多指令采集 + ConfigManifest | ✅ 完成 |
| v2.5.16+: Techfine 逆变器 + 逐指令配置 + 通道终端 | ✅ 完成 |
| 生产部署: 单容器 + EMQX + 降采样 + gzip | ✅ 完成 |
| 多总线事件驱动架构 (v2.7) | ✅ 合入 main |
| 前端测试大规模修复 | ✅ 228 失败 → 0 |
| 移动端四卡紧凑布局 | ✅ 完成 |

### 待完成 (P0)
1. 对话框 92vw 全局兜底（58 处固定宽度对话溢出风险）
2. NodeDetail 移动端适配
3. 统计卡片统一 auto-fit（消除三页不一致）
4. iOS 输入防缩放 16px
5. 文档约定治理与已实现设计文档归档

## 文档现状

### 规模
- 总计 93 个文档（89 跟踪 + 4 未跟踪）
- 分布在 docs/设计/、docs/实现/、docs/操作/、docs/发布/、docs/协议/、docs/验证/、docs/评审/ 等目录

### 发现的问题
- **版本号混乱**: v2.0~v2.7 并存，无统一基线
- **8 份已实现设计文档未归档**: 头部标注"已实现"但仍留在 docs/设计/，应移至 docs/实现/
- **边缘设备控制文档三重重复**: 架构设计(80KB) + 统一方案(101KB) + 开发计划(42KB) = 224KB 高度重叠
- **前端 UIUX 文档三重重复**: 改进设计 + 参考摘要 + 质量研究报告
- **通道设计与 GPIO 重构设计直接矛盾**: 通道详细设计仍将 GPIO 列为通道类型

## 已知 MemOS 配置

- `search_memory` 和 `add_message` 工作正常
- `get_user_profile` 返回 40103 "User does not exist"
- 知识库 ID: `base33258005-878b-41c3-85bc-0e748946d332`
- 注意: 搜索 EHomeSystem 相关内容时，请传入 `knowledgebase_ids: ["base33258005-878b-41c3-85bc-0e748946d332"]` 参数到 `search_memory`