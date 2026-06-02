# 家庭数字化系统 V2.0 需求文档

> 基线: v1 (基于 develop @ 57d9a02)
> 目标: 不兼容旧版，从零重新设计。协议层砍掉 protobuf，用精简二进制帧替代。产品层保持所有现有功能。
> 状态: 草稿，待修改
> 编制: 2026-05-31

---

## 一、产品定义

### 1.1 系统定位

**家庭数字化系统**是一个以 ESP32 为边缘采集节点、Go 服务端为核心、Vue3 Web 前端为管理界面的通用物联网平台。系统本身不绑定任何特定设备类型——只要 ESP32 能通过 SPI/I2C/UART 与之通信，就能作为"通道"接入系统，由 Server 端驱动完成数据解析、存储、展示。

当前已接入的设备类型是第一批：温度、气压、湿度、风向等气象传感器。后续可扩展到：光照、CO₂、PM2.5、继电器控制、红外遥控、RS485 设备、Modbus 设备等。

### 1.2 核心架构不变

```
┌──────────────────────────────────────────────────────────────────┐
│                       MQTT Broker (mosquitto)                     │
│                                                                    │
│  devices/{device_id}/up    ← ESP32 上行（数据+状态+确认）          │
│  devices/{device_id}/down  ← Server 下行（配置+命令+OTA）          │
└────────────┬───────────────────────────────────┬──────────────────┘
             │                                   │
  ┌──────────▼──────────┐              ┌─────────▼──────────┐
  │   ESP32 边缘节点      │              │   Go 服务端          │
  │                      │              │                      │
  │  ┌────────────────┐  │              │  ┌────────────────┐  │
  │  │ 二进制帧协议     │  │              │  │ 二进制帧协议     │  │
  │  │ (~300行 C)      │  │              │  │ (~400行 Go)     │  │
  │  └────────────────┘  │              │  └────────────────┘  │
  │  ┌────────────────┐  │              │  ┌────────────────┐  │
  │  │ 采集调度器       │  │              │  │ MQTT Handler   │  │
  │  │ 总线抽象层       │  │              │  │ + 数据流水线    │  │
  │  │ 设备驱动         │  │              │  │ + 设备初始化    │  │
  │  └────────────────┘  │              │  └────────────────┘  │
  │                      │              │  ┌────────────────┐  │
  │  SPI / I2C / UART    │              │  │ REST API        │  │
  │  ──────────────────  │              │  │ + WebSocket      │  │
  │  各类传感器/外设      │              │  └────────┬───────┘  │
  └──────────────────────┘              └───────────┼──────────┘
                                                    │
                                         ┌──────────▼──────────┐
                                         │   Vue3 Web 管理界面   │
                                         │   数据看板 / 设备管理  │
                                         │   配置管理 / OTA 升级 │
                                         └─────────────────────┘
```

### 1.3 技术栈不变

| 层 | 技术 |
|----|------|
| 边缘节点 | ESP32-S3 / ESP32-C6, FreeRTOS, WiFi STA |
| 消息中间件 | MQTT 5.0 (mosquitto), QoS 1 |
| 服务端 | Go 1.24+, Gin, GORM, PostgreSQL, Redis |
| 前端 | Vue 3, Vite, Tailwind CSS, Axios, WebSocket |
| 设备驱动 | 插件化注册, 每种设备类型一个 Go 驱动文件 |

### 1.4 V2.0 目标

V2.0 在不牺牲现有功能的前提下：

1. **协议精简** — 砍掉 protobuf，手写约 500 行二进制帧编解码，flash 省 82%，RAM 省 97%
2. **平台通用化** — 协议不依赖特定设备类型，新设备接入只需写 Go 驱动 + 配置模板
3. **可维护性** — 编解码器是手写的普通代码，改字段直接在源文件中改
4. **可扩展性** — 不依赖 protoc/nanopb 工具链

---

## 二、系统设计原则

### 2.1 三层分离

```
┌─────────────────────────────────────────────────────────┐
│  前端层     │  展示/管理/交互                              │
│  (Vue3)    │  不感知协议，通过 REST API + WebSocket 通信    │
├────────────┼─────────────────────────────────────────────┤
│  服务层     │  数据处理/存储/业务逻辑                      │
│  (Go)      │  驱动注册/数据解析/配置管理/OTA/HA 集成        │
├────────────┼─────────────────────────────────────────────┤
│  边缘层     │  物理采集/执行                               │
│  (ESP32)   │  传感器通信/数据上报/命令执行/固件升级         │
└────────────┴─────────────────────────────────────────────┘
```

### 2.2 设备模型

```
采集器 (Collector)
  ├── 通道 1 (Channel)   ← SPI2, pin 10-13
  │   └── 设备: BMP280   ← device_type="bmp280"
  ├── 通道 2 (Channel)   ← UART1, pin 17-18
  │   └── 设备: LK-TH01  ← device_type="lk_th01"
  └── ...

通道 = 一个物理总线接口（SPI/I2C/UART/GPIO/ADC）
设备 = 挂在通道上的具体传感器/外设
模板 = 通道的通信命令（SPI: 写地址+读响应, UART: 发命令+等回复）
```

### 2.3 数据模型

```
原始数据: ESP32 上报的 raw_data (hex bytes)
  └── DataPipeline 解析
        ├── 设备驱动: 把 bytes 解析为 sensor_name:value 对
        │   BMP280: [7 bytes] → {"temperature": 25.3, "pressure": 1013.2}
        │   LK-TH01: [8 bytes] → {"temperature": 25.1, "humidity": 65.0}
        └── 存入 unified_data 表 (device_id, sensor_name, value, unit)
              ├── 前端图表读取
              ├── HomeAssistant MQTT Discovery + State 推送
              └── 告警规则（未来）
```

---

## 三、协议层 — 二进制帧协议

### 3.1 设计原则

1. **1 字节 type 分派** — 替代 proto oneof，不需要包装层
2. **proto-style field 编码** — key = (field_num << 3) | wire_type，与 proto wire 格式完全兼容
3. **varint 编码整数** — 小整数省带宽
4. **length-delimited 编码 bytes/string**
5. **optional 字段零开销** — 值为默认值时跳过编码
6. **ESP32 raw_data 回调编码** — 从总线接收缓冲区直接写入 MQTT，零拷贝

### 3.2 Frame 格式

```
┌──────────┬─────────────────────────────────────┐
│  type    │  fields...                          │
│  (1 B)   │  (key-value pairs, proto-style)     │
└──────────┴─────────────────────────────────────┘

type:    1 字节消息类型 (0x01-0x0D)
fields:  每个 field = [tag_byte] [value]

tag = (field_number << 3) | wire_type
  wire_type 0 = varint (整数/bool/enum)
  wire_type 2 = length-delimited (string/bytes/packed repeated)
```

### 3.3 消息类型

```
Type        Hex  方向        用途            频率
────────────────────────────────────────────────────
Hello       0x01 ESP→SVR   启动握手          启动时 + 重连时
StatusRpt   0x02 ESP→SVR   心跳/状态         每 5s
DataRpt     0x03 ESP→SVR   传感器数据上报     每采集周期
ConfigMfst  0x04 SVR→ESP   全量配置下发       首次/配置变更
ConfigRslt  0x05 ESP→SVR   配置应用确认       配置下发后
WriteCmd    0x06 SVR→ESP   交互命令           按需
WriteRsp    0x07 ESP→SVR   命令确认           命令执行后
Ping        0x08 SVR→ESP   延迟测量           按需
Pong        0x09 ESP→SVR   延迟响应           收到 Ping 后
OtaCmd      0x0A SVR→ESP   固件升级命令       按需
OtaProg     0x0B ESP→SVR   OTA 进度          升级中周期性
ScanRpt     0x0C ESP→SVR   I2C 扫描结果       扫描后
ScanReq     0x0D SVR→ESP   I2C 扫描请求       按需

注: 配网模式 (F1.4) 和恢复出厂设置 (F1.5) 走本地流程, 不占用协议帧 type。
恢复出厂可通过 WriteCmd(channel_id=0, data=[0xFC 0x00]) 远程触发。
```

---

## 四、功能需求

### F1: 节点接入与注册

#### F1.1 Hello 握手 (type=0x01, ESP→SVR)

节点上电/WiFi重连时发送，宣告自己的存在。

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| device_id | string | 1 | 节点唯一标识 (MAC 地址) |
| firmware_version | string | 2 | 固件版本号 |
| model | string | 3 | 硬件型号 ("ESP32S3" / "ESP32C6") |
| channel_count | uint8 | 4 | 已配置通道数 |

**Server 处理流程:**

```
Hello 到达
  ├── 查 DB collectors 表，按 device_id 匹配
  │   ├── 不存在 → 自动注册新节点 (status=online)
  │   └── 已存在 → 更新 firmware_version, model, status=online
  ├── 比对 ConfigManifest hash (templates + channels 的 CRC32)
  │   ├── 匹配 → 跳过下发 (30s 防重入检查)
  │   └── 不匹配 → 下发 ConfigManifest (type=0x04)
  ├── 触发设备初始化编排器 (恢复缓存的校准数据)
  └── 异步 Ping 测量延迟
```

#### F1.2 StatusReport 心跳 (type=0x02, ESP→SVR)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| uptime_sec | uint32 | 1 | 运行时长（秒） |
| status | string | 2 | "online" / "offline" |
| channel_count | uint8 | 3 | 当前活跃通道数 |

**处理流程:**

```
每 5s 自动发送
  ├── Server 更新 collector.last_seen, status, uptime_seconds
  ├── Redis heartbeat TTL 续期 (15s)
  ├── 检测 offline→online 转换 → 触发设备初始化 (校准恢复)
  ├── 记录 collector_events 表 (状态变更时间线)
  └── WebSocket 推送前端
```

---

### F1.3: RGB LED 状态指示

编译时可选开启（Kconfig `CONFIG_COLLECTOR_RGB_LED=y`）。通过板载 RGB LED (如 WS2812) 展示节点运行状态。

**状态对照表:**

| 阶段 | LED 颜色 | 模式 | 说明 |
|------|---------|------|------|
| 启动中 | 白色 | 呼吸 | 上电初始化阶段 |
| WiFi 未配置 | 黄色 | 快闪 | NVS 无 WiFi 凭据 → 进入配网模式 |
| WiFi 连接中 | 蓝色 | 慢闪 | 正在连接 WiFi (1s 周期) |
| WiFi 连接失败 | 红色 | 双闪 | 超过 30s 连不上 → 重新扫描或等待配网 |
| MQTT 连接中 | 青色 | 慢闪 | WiFi 已通, 正在连 MQTT broker |
| MQTT 连接失败 | 红色 | 三闪 | broker 不可达或认证失败 |
| Server 未响应 (离线) | 橙色 | 慢闪 | MQTT 已连但 30s 内未收到下行消息 |
| 正常运行 | 绿色 | 常亮 | 数据正常上报, 一切正常 |
| OTA 升级中 | 紫色 | 呼吸 | 固件下载/刷写过程中 |
| 采集错误 | 黄色 | 单闪 | 某个通道采集失败 (不影响其他通道) |
| 恢复出厂中 | 红色 | 快闪 | 正在清除 NVS |

**依赖:**
- 需要 Kconfig 开关 `CONFIG_COLLECTOR_RGB_LED`, 默认关闭
- 需要硬件支持 (GPIO 连接 WS2812 或简单 RGB LED)
- 无需新增协议消息, 纯本地行为

**说明:** 无 RGB 硬件的 ESP32 (如裸 C6 模块) 关闭 Kconfig 后完全不编译 LED 代码, flash 零开销。

---

### F1.4: 配网模式 (WiFi Provisioning)

节点首次上电或 WiFi 凭据丢失时, 自动进入配网模式。

**触发条件:**
1. NVS 中无 WiFi SSID/密码
2. 连续 3 次 WiFi 连接失败
3. 用户通过物理按钮触发 (长按 5s)

**配网方式 (优先级从高到低):**

| 方式 | 说明 |
|------|------|
| **BLE 配网** | ESP32 开启 BLE 广播, 手机 App 通过 BLE 发送 WiFi 凭据。兼容 ESP-IDF Unified Provisioning 协议, 可用 ESP SoftAP Provisioning App |
| **SoftAP 配网** | ESP32 开启热点 `HomeStation-XXXX`, 手机连接后访问 192.168.4.1 网页输入 WiFi 信息 |
| **串口配网** | 通过 USB 串口发送 AT 命令: `AT+WIFI=ssid,password` |

**配网成功后:**
- WiFi 凭据写入 NVS, 自动连接
- LED 变为蓝色慢闪 (WiFi 连接中)
- 连接成功后走正常 Hello → ConfigManifest → 数据上报流程

**安全考虑:**
- BLE 配网使用 Proof-of-Possession (PoP) 验证
- 配网完成后关闭 BLE/SoftAP, 恢复正常 WiFi STA 模式
- 配网超时 5 分钟无操作 → 关闭配网模式, 继续尝试用已保存凭据连接

---

### F1.5: 恢复出厂设置

清除所有 NVS 数据, 恢复到出厂状态。

**触发方式:**
1. **物理按钮:** 长按 10s (LED 红色快闪确认)
2. **下行命令:** Server 发送 WriteCommand (channel_id=0, 特殊 data 字段)
3. **串口命令:** `AT+FACTORY`

**清除内容:**
- WiFi 凭据 (ssid/password)
- ConfigManifest 缓存
- sequence 计数器
- device_id (如有自定义)
- 所有 NVS namespace 数据

**执行流程:**

```
触发恢复出厂
  ├── LED: 红色快闪
  ├── 停止所有采集 (scheduler + bus_io_task)
  ├── 断开 MQTT
  ├── nvs_flash_erase() → 清除全部 NVS
  ├── 重启 ESP32
  └── 重启后:
        ├── NVS 为空 → 无 WiFi 凭据 → 进入配网模式 (F1.4)
        ├── LED: 黄色快闪 (WiFi 未配置)
        └── 等待用户配网
```

**协议消息 (可选, 下行):**

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| data | bytes | 1 | 魔数 `[0xFC, 0x00]` (FACTORY_RESET) |

通过 WriteCommand (type=0x06, channel_id=0, data=[0xFC 0x00]) 下发。ESP32 收到后先回复 WriteResponse 确认, 再执行清除+重启。

---

### F2: 设备数据采集与上报

#### F2.1 DataReport 数据上报 (type=0x03, ESP→SVR)

这是系统最高频的消息。每个通道每次采集发一条。

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| channel_id | uint32 | 1 | 通道 ID |
| timestamp_us | uint64 | 2 | 采集时刻 (微秒) |
| sequence | uint32 | 3 | per-channel 递增计数器 (NVS 持久化) |
| raw_data | bytes | 4 | 原始设备数据 (回调编码, 零拷贝) |
| error_code | uint32 | 5 | 错误码 (optional, 仅非零时编码) |
| request_id | uint32 | 6 | 交互命令确认 ID (optional, 仅非零时编码) |

**ESP32 端采集流程:**

```
Scheduler (FreeRTOS task, per-bus)
  ├── 遍历该总线上的活跃通道
  ├── hal_bus_transact(template)  ← 发送模板命令, 接收响应
  │   SPI: CS↓ → 写地址 → 读数据 → CS↑
  │   I2C: START → 写地址 → 读 → STOP
  │   UART: 发命令 → 等超时 → 读响应
  ├── 编码 DataReport (raw_data 走回调, 直接从 rx_buf 到 MQTT)
  └── mqtt_publish(devices/{id}/up, frame)
```

**Server 端处理流程:**

```
DataReport 到达 (MQTT 回调线程)
  ├── 即时处理: request_id 路由
  │   ├── PendingWriteManager.HandleDataReportAck()
  │   └── DeviceInitOrch.HandleDataReportAck()
  └── 异步投递 worker pool:
        ├── DB 存储 (device_data + unified_data)
        ├── DataPipeline 解析 (设备驱动 ParseData)
        ├── 校准缺失时触发 ForceReinit
        ├── Redis heartbeat TTL 续期
        ├── HomeAssistant MQTT State 推送
        └── WebSocket 推送到前端
```

**关键改动 vs v1:**
- raw_data 改用回调编码: 521B → 16B RAM (省 97%)
- 删除 BatchDataReport (MQTT 本身做批量, 4KB RAM 代价不值)
- 删除 batch_id/batch_seq/is_historical 死字段

---

### F3: 配置管理

配置由 Server 统一管理，通过 ConfigManifest 下发到 ESP32。

#### F3.1 ConfigManifest 配置下发 (type=0x04, SVR→ESP)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| manifest_id | string | 1 | 配置唯一标识 (version-timestamp-hash) |
| templates | repeated | 2 | 命令模板列表 |
| channels | repeated | 3 | 通道配置列表 |

**Template 子结构 (命令模板):**

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| id | uint32 | 1 | 模板 ID (全局唯一) |
| write_data | bytes | 2 | 下发数据 (≤64B) |
| read_length | uint32 | 3 | 预期读回字节数 |
| delay_ms | uint32 | 4 | 命令后等待时间 |

**Channel 子结构 (通道配置):**

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| id | uint32 | 1 | 通道 ID |
| hardware_id | uint32 | 2 | 硬件总线 ID |
| template_ids | uint32[] | 3 | 关联的模板 ID 列表 |
| interval_ms | uint32 | 4 | 采集间隔 |
| enabled | bool | 5 | 是否启用 |
| bus_type | uint8 | 6 | 总线类型 (1=UART, 2=I2C, 3=SPI, 4=GPIO, 5=ADC) |
| bus_config | bytes | 7 | 总线配置 (JSON 字符串) |

**bus_config JSON 示例:**

```json
// SPI
{"freq_hz": 1000000, "mode": 0, "cs_pin": 13}

// UART
{"baud_rate": 9600, "data_bits": 8, "stop_bits": 1, "parity": "none"}

// I2C
{"freq_hz": 100000, "slave_address": 118}
```

**工作流程:**

```
触发条件:
  ├── Hello 后 config_hash 不匹配
  └── StatusReport 检测到 offline→online 转换

防重入: 同一节点 30s 内已下发则跳过

ESP32 收到后:
  ├── 解码 ConfigManifest → 注册模板到 template_registry
  ├── 配置通道到 scheduler → 启动 bus_io_task
  ├── NVS 持久化 (断电重启后恢复)
  └── 发送 ConfigResult (type=0x05) 确认
```

#### F3.2 ConfigResult 配置确认 (type=0x05, ESP→SVR)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| manifest_id | string | 1 | 应用的配置 ID |
| success | bool | 2 | 是否成功 |

Server 收到后更新 collector.config_version。

---

### F4: 交互命令

用于设备初始化等需要"发一条命令→等响应→再发下一条"的场景。

#### F4.1 WriteCommand (type=0x06, SVR→ESP)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| request_id | uint32 | 1 | 请求 ID (关联 WriteResponse) |
| channel_id | uint32 | 2 | 目标通道 |
| data | bytes | 3 | 下发数据 |
| read_size | uint32 | 4 | 预期读回字节数 (0=仅写, optional) |

#### F4.2 WriteResponse (type=0x07, ESP→SVR)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| request_id | uint32 | 1 | 对应 WriteCommand.request_id |
| success | bool | 2 | 是否成功 |
| error_code | uint32 | 3 | 错误码 (optional) |
| error_msg | string | 4 | 错误描述 (optional) |

**超时+重试机制 (PendingWriteManager):**

```
Server 发送 WriteCommand
  ├── 注册 pending entry (request_id → channel)
  ├── 等待 WriteResponse (5s 超时)
  │   ├── 收到 → return success
  │   └── 超时 → 重试 (最多 3 次)
  └── 3 次均失败 → 返回 error
```

**典型场景 — BMP280 设备初始化:**

```
Server (DeviceInitOrch)
  ├── WriteCommand: request_id=1, data=[E0 B6]        → ESP32 复位芯片
  │   └── WriteResponse: request_id=1, success=true
  ├── WriteCommand: request_id=2, data=[D0], read_size=1 → 读 chip_id
  │   └── DataReport: request_id=2, raw_data=[58]       → chip_id=0x58 ✓
  ├── WriteCommand: request_id=3, data=[88], read_size=25 → 读校准数据
  │   └── DataReport: request_id=3, raw_data=[25 bytes]
  ├── WriteCommand: request_id=4, data=[F4 37]        → 设工作模式
  ├── WriteCommand: request_id=5, data=[F5 00]        → 设配置
  └── 校准数据缓存到 DB + calibCache
```

---

### F5: 延迟测量

#### Ping (type=0x08) / Pong (type=0x09)

| 字段 | 类型 | # |
|------|------|---|
| timestamp_us | uint64 | 1 |

Server 发送 Ping → ESP32 原样带 timestamp 回复 Pong → Server 计算 RTT。
Redis 存储 ping timestamp 防伪造。结果推送到前端展示。

---

### F6: OTA 固件升级

#### F6.1 OtaCommand (type=0x0A, SVR→ESP)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| ota_id | string | 1 | 升级任务 ID |
| firmware_url | string | 2 | 固件下载 URL |
| checksum | string | 3 | SHA256 校验和 (hex) |
| size_bytes | uint64 | 4 | 固件大小 |
| version | string | 5 | 目标版本号 |

#### F6.2 OtaProgress (type=0x0B, ESP→SVR)

| 字段 | 类型 | # | 说明 |
|------|------|---|------|
| ota_id | string | 1 | 任务 ID |
| status | uint8 | 2 | 0=下载中, 1=完成, 2=失败 |
| progress_pct | uint8 | 3 | 进度百分比 |
| error_msg | string | 4 | 错误消息 (optional) |

**流程:** 前端触发 → Server 下发 OtaCommand → ESP32 下载+验证 checksum+刷写+重启 → 每 5% 上报进度 → 结果推送到前端。

---

### F7: 总线设备扫描

#### ScanRequest (type=0x0D) / ScanReport (type=0x0C)

| ScanRequest | 类型 | # |
|-------------|------|---|
| request_id | string | 1 |
| hardware_id | uint32 | 2 |

| ScanReport | 类型 | # |
|------------|------|---|
| request_id | string | 1 |
| hardware_id | uint32 | 2 |
| success | bool | 3 |
| addresses | uint32[] | 4 |

前端触发 I2C 总线扫描 → Server 下发 ScanRequest → ESP32 执行 hal_i2c_scan → 返回发现的设备地址列表。

---

### F8: 前端管理界面 (完全不变)

V2.0 不改变前端任何功能。协议层改变对前端完全透明。

| 模块 | 功能 |
|------|------|
| **Dashboard** | 概览卡片 (在线节点数/设备数/最新数据) + 通知铃铛 |
| **节点管理** | 列表 (状态筛选/分页) / 详情 (配置同步/OTA/测延迟/通道管理) |
| **设备管理** | CRUD / 批量操作 / 筛选联动 / 状态监控 |
| **数据看板** | 最新 / 历史 / 聚合查询 + 时间范围选择 |
| **固件管理** | 上传 / 版本管理 / OTA 任务 / 升级历史 |
| **驱动管理** | 驱动树 / 驱动列表 / 驱动详情 (插件化注册的设备驱动浏览) |
| **厂商管理** | 厂商 / 设备型号 / 数据定义 |
| **数据源管理** | 主备切换 / 故障转移 / 健康检查 |
| **HomeAssistant** | MQTT Discovery / Retained / 状态推送 / 实体同步 |
| **通知中心** | API 驱动的通知, 标记已读 |
| **WebSocket** | 实时状态/数据/OTA 进度推送 |

---

### F9: WebSocket 实时推送 (不变)

| 事件 | 触发条件 | payload |
|------|---------|---------|
| `collector_status` | StatusReport 上线/离线 | collector_id, status, uptime |
| `channel_data` | DataReport 收到 | channel_id, data_hex, timestamp |
| `ota_progress` | OtaProgress 收到 | ota_id, status, progress_pct |
| `ping_result` | Ping/Pong 完成 | collector_id, latency_ms |
| `notification` | 新通知 | notification_id, type, message |

---

### F10: HomeAssistant 集成 (不变)

```
MQTT Discovery (设备上线时):
  homeassistant/sensor/{device_id}_{sensor}/config → {
    name, device_class, unit_of_measurement,
    state_topic, value_template, ...
    device: {identifiers, name, model, manufacturer}
  }

MQTT State (每次 DataReport 解析后):
  homeassistant/sensor/{device_id}_{sensor}/state → 25.3
```

---

### F11: 通道终端 (Channel Terminal) — 新增

通道终端是位于 ESP32 和 Server 之间的数据流监听器，用于调试和交互测试。

**核心能力：**
- 监听：实时查看指定通道上所有收发的原始数据
- 发送：手动向 ESP32 发送调试命令

**前端入口：** 通道管理页面，每个通道行有"打开终端"按钮

**WebSocket 连接：**
```
ws://localhost:8080/api/v1/ws/terminal?channel_id={channel_id}&token={jwt}
```

**消息格式：**

| 方向 | type | 说明 |
|------|------|------|
| Server→Client | `terminal_data` | 通道数据推送（direction + raw_hex + parsed） |
| Server→Client | `terminal_history` | 连接后推送最近 N 条历史 |
| Server→Client | `terminal_ack` | 命令发送确认 |
| Client→Server | `terminal_send` | 客户端发送命令（data_hex + read_size） |

**数据流：**
```
ESP32  ←──────────────────────→  Server
          │
          ▼
     通道终端 (监听 + 发送)
```

**详细设计：** [channel-terminal.md](./channel-terminal.md)

---

## 五、数据库 (不变)

| 表 | 说明 |
|----|------|
| **collectors** | 节点注册信息 (id/device_id/model/firmware/status/config_version) |
| **channels** | 通道配置 (collector_id/hardware_type/hardware_id/config) |
| **devices** | 设备定义 (name/type/parser_id/channel_id/status) |
| **device_configs** | 设备配置模板 (device_type/parser_id/channel_template) |
| **device_data** | 原始数据 (device_pk/collector_id/data_json/timestamp) |
| **unified_data** | 统一数据 (device_id/sensor_name/value/unit/timestamp) |
| **data_sources** | 数据源主备管理 + 故障转移 |
| **ota_tasks / firmwares** | OTA 升级任务 + 固件版本 |
| **notifications** | 通知 |
| **users / operation_logs** | 用户 + 审计日志 |
| **vendors / device_models** | 厂商 + 设备型号 + 数据字段定义 |
| **collector_events** | 节点状态变更事件 (online/offline 时间线) |
| **calibration_cache** | 设备校准数据缓存 (内存 + DB) |

---

## 六、保留的核心算法 (不变)

### 6.1 config_hash 比对

```
Hello 上报 → Server CalcConfigHash(templates + channels) →
  CRC32 匹配 → 跳过下发 | 不匹配 → 下发 ConfigManifest
```

### 6.2 配置下发防重入

```
publishConfigIfAllowed → 30s 内同节点已发则跳过
```

### 6.3 DataReport worker pool

```
MQTT 回调 → 即时 request_id 路由 →
  sendToWorkerPool → N 个 goroutine 并行处理:
    DB 存储 + 驱动解析 + HA 推送 + WS 推送 + Redis 续期
```

### 6.4 设备初始化编排器

```
offline→online 检测 → ClearInitCache → InitIfNeeded →
  交互式步骤链 (sendAndWait):
    写命令 → 等 5s → 读响应 → 写下一条 → ...
  → 校准数据缓存到 DB + calibCache
```

### 6.5 三层离线检测

```
L1: StatusReport 5s 周期 → Redis heartbeat TTL 15s
L2: OfflineDetector 5s 扫描 → Redis SCAN collector:status:*
L3: DB fallback 60s → SQL last_seen < NOW()-90s 兜底
```

### 6.6 设备驱动插件化

```
init() 阶段:
  devices.Register(DriverInfo{Meta, Driver})

Server 启动时:
  自动加载所有 init() 注册的驱动 → 按 type 索引

DataPipeline 解析时:
  查 device_type → 取 Driver → ParseData(raw_bytes) → SensorData
```

---

## 七、删除清单

### 7.1 删除的 proto 类型 (52 → ~13)

| 删除 | 理由 |
|------|------|
| HardwareCapabilities 族 (7 types) | Server 存了但从未使用 |
| HardwareConfig / BusConfig 族 (10 types) | 改为 Channel.bus_config JSON |
| BatchDataReport | MQTT 做批量, 省 4KB RAM |
| Downstream/DownstreamConfig/Upstream/UpstreamReport | oneof 包装层, type 字节替代 |
| SyncCommands | 动态模板从未实现 |
| QueryResources/QueryChannels/ReportResources/ReportChannels | 低频, 可后续以独立消息加入 |
| Stats, UpdateStrategy, PinInfo, PinRole | 不消费 |
| 多个 Enum (Parity, FlowControl, GPIODirection, ADCAttenuation, SPIMode) | 内嵌到字段值 |

### 7.2 删除的字段 (22 个)

| 字段 | 来源 |
|------|------|
| batch_id, batch_seq, is_historical | DataReport |
| baudrate, clock_hz, operation | WriteCommand |
| data, channel_id | WriteResponse |
| clear_dynamic | SyncCommands |
| free_heap_bytes, min_free_heap_bytes, connection_quality | StatusReport |
| platform, protocol_version, caps | Hello |
| active_channels, total_channels, timestamp_us | StatusReport |
| error_code, error_message, stats | ConfigResult |
| strategy, templates_only | ConfigManifest |

---

## 八、开发计划

| 阶段 | 内容 | 估计工作量 |
|------|------|-----------|
| Phase 1 | Python 参考实现 + 测试向量 | 1 天 |
| Phase 2 | ESP32 二进制帧编解码器 (~300 行 C) | 2 天 |
| Phase 3 | Go Server 编解码器 (~400 行 Go) | 2 天 |
| Phase 4 | 端到端集成: Hello → Config → DataReport 闭环 | 2 天 |
| Phase 5 | OTA + Ping/Pong + Scan | 1 天 |
| Phase 6 | 前端适配 (API 字段名对齐, 少量改动) | 1 天 |
| Phase 7 | 全量测试 + 文档 | 2 天 |
| **合计** | | **约 11 天** |

---

## 九、待讨论

1. **connection_quality** — v1 中存了 DB 但前端未展示。保留？删除？还是改为前端可展示的 RSSI？

2. **Resource 发现 (ReportResources/Channels)** — v1 功能完整但极低频。v2 删除还是合并到 Hello？

3. **bus_config JSON** — ESP32 端需要 cJSON 库 (~2KB flash)，接受吗？还是有更好的替代方案？

4. **DataReport.request_id 复用** — 当前同时用于 PendingWriteManager 和 DeviceInitOrch。v2 是否拆分为两个独立字段？

5. **新设备类型接入流程** — 是否需要在协议/文档中明确定义？

6. **MQTT topic 结构** — v2 简化为两个 topic (`up`/`down`)，是否需要保留 v1 的 `up/report` 和 `down/config` 独立 topic？