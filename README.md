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
| 边缘节点 | ESP32-S3 / ESP32-C6, FreeRTOS, WiFi STA, ESP-IDF 6.0.1 |
| 消息中间件 | EMQX 5.7 (MQTT 5.0, QoS 1) |
| 服务端 | Go 1.25, Gin, GORM, PostgreSQL 16, Redis |
| 前端 | Vue 3.5, Vite 8, Element Plus 2.13, ECharts 6, Pinia 3, Tailwind CSS |
| 部署 | Docker (Alpine 单服务), Makefile 本地开发 |

## 快速开始

### 前置依赖

- Go 1.25+
- Node.js 22+ / pnpm
- Docker + Docker Compose
- ESP-IDF 6.0.1 (固件编译)

### 1. 克隆并进入项目

```bash
cd /home/sun/workspace/EHomeSystem
```

### 2. 启动开发环境

```bash
# 一键启动：独立开发基础设施 + 本地前后端（make 不带参数效果相同）
make dev

# 或分步启动
make infra       # 仅启动基础设施
make backend     # 仅启动后端 (:8082)
make frontend    # 仅启动前端 (:5174)
```

开发与测试共用 `ehome-dev` Compose 项目；其中 PostgreSQL 同时包含 `ehome` 和 `ehome_test` 两个数据库。开发栈使用独立容器、网络、持久卷和主机端口，所有 `make down/clean` 操作均不会管理生产 Compose 项目。

本地开发端口：

| 服务 | 开发/测试端口 | 生产端口 |
|------|---------------|---------|
| PostgreSQL | 5435 | 5432（容器内） |
| Redis | 6380 | 6379（容器内） |
| EMQX MQTT | 1884 | 1883 |
| EMQX WebSocket | 8084 | 8083 |
| EMQX Dashboard | 18084 | 18083 |
| 后端 API | 8082 | 8080 |
| 前端 | 5174 | 80 |

### 3. 构建 ESP32 固件

先安装 ESP-IDF 6.0.1，并在当前终端加载其环境：

```bash
cd esp32-collector
. "$IDF_PATH/export.sh"
```

固件按“芯片 + Flash 容量”选择 profile。每个 profile 使用独立的 `sdkconfig`、依赖锁和构建目录，切换目标时不需要执行 `idf.py set-target` 或 `fullclean`。

| Profile | 芯片 | Flash | 分区布局 | 构建目录 |
|---------|------|------:|----------|----------|
| `c6-n8` | ESP32-C6 | 8MB | 双 OTA，3.5MB × 2 | `build/c6-n8/` |
| `c6-n16` | ESP32-C6 | 16MB | 双 OTA，6.5MB × 2 | `build/c6-n16/` |
| `s3-n8` | ESP32-S3 | 8MB | 双 OTA，3.5MB × 2 | `build/s3-n8/` |
| `s3-n16` | ESP32-S3 | 16MB | 双 OTA，6.5MB × 2 | `build/s3-n16/` |

`N8`、`N16` 只表示模块的 Flash 容量；S3 模块的 PSRAM 类型和容量需要通过额外板型配置单独启用。

```bash
# 构建指定 profile
./build_firmware.sh c6-n8

# 构建全部四种 profile
./build_firmware.sh all
```

主固件生成在 `build/<profile>/ehome_collector.bin`。烧录时必须使用对应 profile 的构建目录：

```bash
# 烧录并监控 C6-N8
idf.py -B build/c6-n8 -p /dev/ttyACM0 flash monitor

# 烧录并监控 S3-N16
idf.py -B build/s3-n16 -p /dev/ttyUSB0 flash monitor
```

### 4. 生产部署

```bash
# 前端预构建（Docker 内 pnpm 会失败）
cd frontend-shared && pnpm build && cd ..

# 构建并启动
docker compose up -d
```

单容器部署：Go 二进制同时服务 API + Vue SPA，容器名 `ehome-web`，端口 `HOME_PORT`（默认 80）。

首次部署可在 `.env` 中设置 `EHOME_ADMIN_USERNAME` 和 `EHOME_ADMIN_PASSWORD`（密码至少 8 位，可选 `EHOME_ADMIN_EMAIL`），容器启动时会仅对空数据库创建管理员一次。已有数据库不会被覆盖；初始化成功后建议移除密码变量。

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
│   ├── build_firmware.sh  # C6/S3、N8/N16 profile 构建入口
│   ├── config/flash/      # N8/N16 Flash 容量配置
│   ├── partitions/        # 按容量共享的双 OTA 分区表
│   ├── sdkconfig.defaults # 芯片间共享配置
│   ├── sdkconfig.defaults.esp32s3  # S3 配置
│   ├── sdkconfig.defaults.esp32c6  # C6 配置
│   └── dependencies.lock  # Component Manager 离线构建种子
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
