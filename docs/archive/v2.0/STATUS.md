# EHomeSystem v2.0 - 项目状态

## 完成状态

| 阶段 | 内容 | 状态 |
|------|------|------|
| Phase 0 | 项目基础设施 | 完成 |
| Phase 1 | Python 参考实现 + 测试向量 | 完成 |
| Phase 2 | ESP32 二进制帧编解码器 (~300行 C) | 完成 |
| Phase 3 | Go Server 编解码器 (~400行 Go) | 完成 |
| Phase 4 | 端到端集成 (Hello→Config→DataReport) | 完成 |
| Phase 5 | OTA + Ping/Pong + Scan | 完成 |
| Phase 6 | 前端适配 (API字段名对齐) | 完成 |
| Phase 7 | 全量测试 + 文档 | 完成 |
| **额外** | API 完善 ( collectors/channels/data ) | 完成 |

## 项目结构

```
/home/sun/workspace/EHomeSystem/
├── README.md
├── .gitignore
├── docs/
│   ├── requirements.md          # v2.0 需求文档 (从老项目复制)
│   ├── STATUS.md                # 本文件
│   └── protocol/
│       └── binary-frame.md      # 二进制帧协议规范
├── scripts/
│   ├── frame_test.py            # Python 参考实现 + 测试
│   └── build.sh                 # 构建脚本
├── backend/                     # Go 后端
│   ├── cmd/server/main.go       # 入口
│   ├── pkg/frame/               # 二进制帧协议包
│   │   ├── frame.go             # 编解码器
│   │   ├── frame_test.go        # 测试
│   │   └── frame_codec_test.go  # Varint 测试
│   └── internal/
│       ├── config/              # 配置管理
│       ├── database/            # PostgreSQL + migrations
│       ├── mqtt/                # MQTT 客户端
│       ├── websocket/           # WebSocket Hub
│       ├── collector/           # 采集器管理
│       ├── ota/                 # OTA 管理
│       └── api/                 # REST API 路由 (collectors/channels/data)
├── esp32-collector/             # ESP32 固件
│   ├── CMakeLists.txt
│   ├── sdkconfig.defaults
│   ├── main/
│   │   ├── main.c               # 主程序 (WiFi/MQTT/任务)
│   │   └── CMakeLists.txt
│   └── components/frame/
│       ├── frame_codec.h        # 帧协议头文件
│       ├── frame_codec.c        # 编解码器实现
│       ├── frame_codec_test.c   # 单元测试
│       └── CMakeLists.txt
└── frontend-shared/             # Vue3 前端 (复用)
    ├── package.json
    ├── vite.config.ts
    ├── tsconfig.json
    └── src/
```

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /health | 健康检查 |
| GET | /api/v1/collectors | 采集器列表 |
| GET | /api/v1/collectors/:device_id | 单个采集器详情 |
| GET | /api/v1/collectors/:device_id/channels | 采集器通道列表 |
| GET | /api/v1/collectors/:device_id/data | 采集器数据 (支持 ?limit=) |
| POST | /api/v1/collectors/:device_id/ping | 发送 Ping |
| GET | /api/v1/ws | WebSocket |

## 测试覆盖

- **Python**: Varint 编解码、Hello、DataReport
- **C (ESP32)**: Hello、DataReport、Varint 边界条件 (30 tests)
- **Go**: Hello、DataReport、Varint、Ping/Pong、跨语言兼容性
- **前端构建**: 成功

## 下一步 (可选)

1. 添加设备驱动注册机制 (BMP280, LK-TH01 等)
2. 实现 ConfigManifest 的完整编解码 (templates + channels)
3. 添加 HomeAssistant MQTT Discovery 集成
4. 实现通道终端 (Channel Terminal) WebSocket 协议
5. 添加告警规则引擎
6. 完善 ESP32 的 bus 抽象层 (SPI/I2C/UART)
7. 添加配网模式 (WiFi Provisioning)
8. 实现 RGB LED 状态指示

## 构建

```bash
# 后端
cd backend && go build ./cmd/server/

# 前端
cd frontend-shared && pnpm build

# ESP32
cd esp32-collector && idf.py build
```

## 运行

```bash
# 启动后端 (需要 PostgreSQL + MQTT Broker)
cd backend && ./server

# 启动前端 (开发模式)
cd frontend-shared && pnpm dev
```
