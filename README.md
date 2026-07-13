# EHomeSystem

家庭数字化系统 — IoT 平台，支持多协议传感器采集、BMS 监控、逆变器管理、OTA 升级。

## 架构

```
┌──────────────────────────────────────────────────────────────────┐
│                    EMQX MQTT Broker (5.7)                        │
│                                                                    │
│  devices/{device_id}/up    ← ESP32 上行（数据+状态+确认）          │
│  devices/{device_id}/down  ← Server 下行（配置+命令+OTA）          │
└────────────┬───────────────────────────────────┬──────────────────┘
             │                                   │
  ┌──────────▼──────────┐              ┌─────────▼──────────┐
  │   ESP32 边缘节点     │              │   Go 后端           │
  │   (C/FreeRTOS)      │              │   (Go 1.25+)        │
  │                     │              │                     │
  │  ESP32-S3 / ESP32-C6 │              │  Gin REST API       │
  │  双目标构建           │              │  GORM + PostgreSQL  │
  │                     │              │  WebSocket 实时推送  │
  │  ┌──────────────┐   │              │  ┌──────────────┐   │
  │  │ 二进制帧协议   │   │              │  │ 二进制帧协议  │   │
  │  │ (protobuf wire)│  │              │  │ (protobuf wire)│  │
  │  └──────────────┘   │              │  └──────────────┘   │
  │  ┌──────────────┐   │              │  ┌──────────────┐   │
  │  │ WiFi/MQTT    │   │              │  │ MQTT Handler │   │
  │  │ 采集调度器    │   │              │  │ 设备驱动注册  │   │
  │  │ UART 透明管道 │   │              │  │ OTA 管理      │   │
  │  │ DMA 多总线    │   │              │  │ Prometheus    │   │
  │  └──────────────┘   │              │  └──────────────┘   │
  └─────────────────────┘              └────────┬────────────┘
                                                │
                                     ┌──────────▼──────────┐
                                     │  Vue3 Web 管理界面   │
                                     │  (Element Plus)      │
                                     └─────────────────────┘
```

## 技术栈

| 层 | 技术 |
|----|------|
| 边缘节点 | ESP32-S3 / ESP32-C6, FreeRTOS, WiFi STA, IDF v5.x |
| 消息中间件 | EMQX 5.7 (MQTT 5.0, QoS 1) |
| 服务端 | Go 1.25, Gin, GORM, PostgreSQL 16, Redis |
| 前端 | Vue 3.5, Vite 8, Element Plus 2.13, ECharts 6, Pinia 3, Tailwind CSS |
| 部署 | Docker (Alpine 单服务), Makefile 本地开发 |

## 快速开始

### 前置依赖

- Go 1.25+
- Node.js 22+ / pnpm
- Docker + Docker Compose
- ESP-IDF v5.x (固件编译)

### 1. 克隆并进入项目

```bash
cd /home/sun/workspace/EHomeSystem
```

### 2. 启动开发环境

```bash
# 一键启动：基础设施 (PG/Redis/EMQX) + 本地前后端
make up

# 或分步启动
make infra       # 仅启动基础设施
make backend     # 仅启动后端 (:8080)
make frontend    # 仅启动前端 (:5174)
```

本地开发端口（避免与生产冲突）：

| 服务 | 开发端口 | 生产端口 |
|------|---------|---------|
| PostgreSQL | 5434 | 5432 (容器内) |
| EMQX MQTT | 1884 | 1883 |
| 后端 API | 8080 | 8080 (容器内) |
| 前端 | 5174 | 80 (nginx/容器) |

### 3. 构建 ESP32 固件

```bash
cd esp32-collector

# 独立构建目录，避免芯片和 Flash 容量配置互相污染
./build_firmware.sh c6-n8
./build_firmware.sh c6-n16
./build_firmware.sh s3-n8
./build_firmware.sh s3-n16

# 构建全部支持的固件
./build_firmware.sh all

# 示例：烧录 C6-N8
idf.py -B build/c6-n8 -p /dev/ttyACM0 flash monitor
```

### 4. 生产部署

```bash
# 前端预构建（Docker 内 pnpm 会失败）
cd frontend-shared && pnpm build && cd ..

# 构建并启动
docker compose up -d
```

单容器部署：Go 二进制同时服务 API + Vue SPA，容器名 `ehome-web`，端口 `HOME_PORT`（默认 80）。

## 协议

二进制帧协议（protobuf wire 格式兼容），1 字节 type + key-value 字段。

| Type | Hex | 方向 | 用途 |
|------|-----|------|------|
| Hello | 0x01 | ESP→SVR | 启动握手 |
| StatusRpt | 0x02 | ESP→SVR | 心跳/状态 |
| DataRpt | 0x03 | ESP→SVR | 传感器数据上报 |
| ConfigMfst | 0x04 | SVR→ESP | 全量配置下发 |
| ConfigRslt | 0x05 | ESP→SVR | 配置应用结果 |
| WriteCmd | 0x06 | SVR→ESP | 写操作/透明管道 |
| WriteRsp | 0x07 | ESP→SVR | 写操作响应 |
| Ping | 0x08 | SVR→ESP | 心跳探测 |
| Pong | 0x09 | ESP→SVR | 心跳响应 |
| OtaCmd | 0x0A | SVR→ESP | OTA 升级指令 |
| OtaProg | 0x0B | ESP→SVR | OTA 进度上报 |
| ScanRpt | 0x0C | ESP→SVR | 总线扫描报告 |
| ScanReq | 0x0D | SVR→ESP | 总线扫描请求 |
| QueryReq | 0x0E | SVR→ESP | 查询请求 |
| QueryRsp | 0x0F | ESP→SVR | 查询响应 |
| ConfigQuery | 0x10 | SVR→ESP | 配置查询 (v2.1) |
| ConfigReport | 0x11 | ESP→SVR | 配置上报 (v2.1) |
| HelloAck | 0x12 | SVR→ESP | 握手确认 (v2.1) |
| ConfigSyncReq | 0x13 | ESP→SVR | 配置同步请求 (v2.1) |
| ConfigSyncRsp | 0x14 | SVR→ESP | 配置同步响应 (v2.1) |
| PongAck | 0x18 | SVR→ESP | Pong 确认 (v3) |
| ResourceReport | 0x19 | ESP→SVR | 硬件资源上报 (v3) |
| QueryResources | 0x1A | SVR→ESP | 资源查询 (v3) |

详见 `docs/协议/二进制帧协议.md`

## 设备驱动

| 驱动 | 协议 | 说明 |
|------|------|------|
| BMP280 | SPI/I2C | 温湿度气压传感器 |
| LKTH01 | — | 温湿度传感器 |
| SN3000 | — | 多功能传感器 |
| PRS3001 | — | 雨量+光照传感器 |
| Jiabaida BMS | RS485/Modbus | 电池管理系统 (v2.5+) |
| Techfine 逆变器 | RS485/Modbus | 光伏逆变器 (v2.5.16+) |

驱动使用全局注册表 + ConfigParser 架构，支持 DeviceConfig 级别的解析器覆盖。

## 测试

```bash
# 全部测试
make test

# 后端单元测试 (SQLite)
make test-backend

# 前端单元测试 (vitest)
make test-frontend

# 集成测试 (PostgreSQL)
make test-integration

# 覆盖率报告
make test-coverage

# Lint
make lint
```

## 目录结构

```
EHomeSystem/
├── backend/               # Go 后端服务
│   ├── cmd/server/        # 入口
│   ├── config/            # 配置加载
│   ├── internal/
│   │   ├── api/           # REST API + WebSocket 路由
│   │   ├── config/        # 配置管理
│   │   ├── database/      # GORM + 迁移
│   │   ├── deviceinit/    # 设备初始化
│   │   ├── drivers/       # 设备驱动 (BMP280/BMS/逆变器等)
│   │   ├── events/        # 事件系统
│   │   ├── homeassistant/ # HA 集成
│   │   ├── models/        # 数据模型 (20+ 表)
│   │   ├── mqtt/          # MQTT 客户端
│   │   ├── nodemgr/       # 节点管理器
│   │   ├── offlinedetector/ # 离线检测
│   │   ├── ota/           # OTA 升级管理
│   │   ├── pendingwrite/  # 写操作队列
│   │   ├── redis/         # Redis 客户端
│   │   ├── seed/          # 数据初始化
│   │   ├── terminal/      # 通道终端 WebSocket
│   │   └── websocket/     # WebSocket Hub
│   ├── pkg/
│   │   ├── frame/         # 二进制帧协议
│   │   ├── logger/        # 日志
│   │   ├── metrics/       # Prometheus 指标
│   │   └── parser/        # 统一解析器架构
│   └── testutil/          # 测试工具
├── esp32-collector/        # ESP32 固件
│   ├── main/              # 主程序
│   ├── components/        # 18 个组件
│   │   ├── bus_dma/       # DMA 多总线
│   │   ├── bus_manager/   # 总线管理
│   │   ├── bus_worker/    # 总线工作线程
│   │   ├── config_mgr/    # 配置管理
│   │   ├── frame/         # 帧编解码
│   │   ├── hw_profile/    # 硬件配置
│   │   ├── ota/           # OTA 升级
│   │   ├── scheduler/     # 采集调度器
│   │   ├── sync_manager/  # 配置同步
│   │   ├── transport/     # 传输层
│   │   └── ...            # wifi/mqtt/nvs/rgb_led 等
│   ├── sdkconfig.defaults.esp32s3  # S3 配置
│   ├── sdkconfig.defaults.esp32c6  # C6 配置
│   └── partitions_*.csv   # 分区表 (S3/C6, 4M/8M/16M)
├── frontend-shared/       # Vue3 前端
│   └── src/
│       ├── api/           # 17 个 API 模块
│       ├── components/    # 公共组件
│       ├── composables/   # 组合式函数
│       ├── stores/        # Pinia stores (10 个)
│       ├── views/         # 页面 (13 个模块)
│       └── ...
├── docs/                  # 项目文档
├── scripts/               # 构建脚本
├── Makefile               # 开发工具
├── Dockerfile             # 生产构建
└── docker-compose.yml     # 生产部署
```

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

## License

MIT
