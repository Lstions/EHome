# EHomeSystem v2.0

家庭数字化系统 v2.0 — 从零重写的气象站/物联网平台。

## 架构

```
┌──────────────────────────────────────────────────────────────────┐
│                       MQTT Broker (mosquitto)                     │
│                                                                    │
│  devices/{device_id}/up    ← ESP32 上行（数据+状态+确认）          │
│  devices/{device_id}/down  ← Server 下行（配置+命令+OTA）          │
└────────────┬───────────────────────────────────┬──────────────────┘
             │                                   │
  ┌──────────▼──────────┐              ┌─────────▼──────────┐
  │   ESP32 边缘节点     │              │   Go 后端           │
  │   (C/FreeRTOS)      │              │   (Go 1.24+)        │
  │                     │              │                     │
  │  ┌──────────────┐  │              │  ┌──────────────┐   │
  │  │ 二进制帧协议   │  │              │  │ 二进制帧协议  │   │
  │  │ (~200行 C)    │  │              │  │ (~200行 Go)  │   │
  │  └──────────────┘  │              │  └──────────────┘   │
  │  ┌──────────────┐  │              │  ┌──────────────┐   │
  │  │ WiFi/MQTT    │  │              │  │ MQTT Handler │   │
  │  │ 采集调度器    │  │              │  │ REST API     │   │
  │  │ 设备驱动      │  │              │  │ WebSocket    │   │
  │  └──────────────┘  │              │  └──────────────┘   │
  └─────────────────────┘              └─────────────────────┘
                                                    │
                                         ┌──────────▼──────────┐
                                         │   Vue3 Web 管理界面   │
                                         │   (复用 v1.x)        │
                                         └─────────────────────┘
```

## 技术栈

| 层 | 技术 |
|----|------|
| 边缘节点 | ESP32-S3 / ESP32-C6, FreeRTOS, WiFi STA |
| 消息中间件 | MQTT 5.0 (mosquitto), QoS 1 |
| 服务端 | Go 1.24+, Gin, GORM, PostgreSQL, Redis |
| 前端 | Vue 3, Vite, Tailwind CSS, Axios, WebSocket |

## 快速开始

### 1. 克隆并进入项目

```bash
cd /home/sun/workspace/EHomeSystem
```

### 2. 启动后端

```bash
cd backend
go run ./cmd/server/
# 或构建后运行: ./bin/server
```

环境变量 (可选):
- `MQTT_BROKER` - MQTT broker URL (默认: `tcp://localhost:1883`)
- `DATABASE_URL` - PostgreSQL URL (默认: `postgres://localhost:5432/ehome?sslmode=disable`)
- `API_ADDR` - API 监听地址 (默认: `:8080`)

### 3. 启动前端

```bash
cd frontend-shared
pnpm install
pnpm dev
```

### 4. 构建 ESP32 固件

```bash
cd esp32-collector
idf.py set-target esp32s3
idf.py build
idf.py flash
```

## 协议

二进制帧协议，protobuf wire 格式兼容，1 字节 type + key-value 字段。

| Type | Hex | 方向 | 用途 |
|------|-----|------|------|
| Hello | 0x01 | ESP→SVR | 启动握手 |
| StatusRpt | 0x02 | ESP→SVR | 心跳/状态 |
| DataRpt | 0x03 | ESP→SVR | 传感器数据上报 |
| ConfigMfst | 0x04 | SVR→ESP | 全量配置下发 |
| ... | ... | ... | ... |

详见 `docs/protocol/binary-frame.md`

## 测试

```bash
# Python 参考实现
cd scripts && python3 frame_test.py

# Go 后端
cd backend && go test ./... -v

# ESP32 编解码器 (host gcc)
cd esp32-collector/components/frame
gcc -o frame_test frame_codec.c frame_codec_test.c && ./frame_test
```

## 目录结构

```
EHomeSystem/
├── backend/           # Go 后端服务
│   ├── cmd/server/    # 入口
│   ├── pkg/frame/     # 二进制帧协议
│   └── internal/      # 业务逻辑
├── esp32-collector/   # ESP32 固件
│   ├── main/          # 主程序
│   └── components/frame/  # 编解码器
├── frontend-shared/   # Vue3 前端 (复用)
├── docs/              # 文档
└── scripts/           # 构建脚本
```

## 开发状态

| 阶段 | 状态 |
|------|------|
| Phase 0-4: 协议 + 后端 + ESP32 | 完成 |
| Phase 5: OTA + Ping/Pong + Scan | 完成 |
| Phase 6: 前端复用 | 完成 |
| Phase 7: 测试 + 文档 | 完成 |

## License

MIT
