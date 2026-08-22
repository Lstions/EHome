# ESP32 总线数据读写与传输链路分析报告

> 基于 esp32-collector 源码分析，涵盖 UART/SPI/I2C 三种总线的完整数据链路。

---

## 一、总体架构

ESP32 定位为**透明总线桥接器**——后端服务器通过 MQTT 下发指令，ESP32 将指令转发到物理总线（UART/SPI/I2C），并将设备原始响应字节透传回后端。ESP32 不做任何协议解析（Modbus 帧边界、CRC 校验等均由后端负责）。

### 核心组件及职责

| 组件 | 文件 | 职责 |
|------|------|------|
| bus_dma | `bus_dma.c/h` | 硬件抽象层：UART/SPI/I2C 驱动初始化、读写、去初始化 |
| bus_manager | `bus_manager.c/h` | 通道生命周期：注册/注销/查找 `bus_dma_ctx_t`，WriteCommand 入队 |
| bus_worker | `bus_worker.c/h` | 命令执行：4 个 cmd_task + 1 个共享 rx_task |
| scheduler | `scheduler.c/h` | 纯定时器：按配置间隔生成 CMD_SAMPLE 投递到命令队列 |
| frame_codec | `frame_codec.c/h` | 二进制帧编解码（protobuf 兼容 varint/length-delimited） |
| msg_handler | `msg_handler.c` + `handler_*.c` | 消息路由：解码下行帧 → 分发到各 handler；编码上行帧 → 发布 |
| ehome_mqtt | `ehome_mqtt.c/h` | MQTT 客户端：连接管理、发布、订阅、重连恢复 |
| mqtt_transport | `mqtt_transport_adapter.c` | 将 ehome_mqtt 适配为通用 transport 接口 |
| sync_manager | `sync_manager.c/h` | 配置同步决策引擎：7 种同步原因 + 周期同步 |
| dma_pool | `dma_pool.c/h` | DMA 通道资源池：分配/释放/绑定 |
| config_mgr | `config_mgr.c/h` | 配置清单（manifest）解析、NVS 持久化 |

---

## 二、总线类型与硬件抽象（bus_dma）

### 2.1 UART（bus_type=1）

**初始化**（`uart_init`, `bus_dma.c:332-482`）：

- 配置字节布局：`[tx_pin, rx_pin, baud×4(BE)]` + 可选 flags 字节（offset 6, bit0=DMA 开关）
- 引脚校验：范围检查（S3: 0-48, C6: 0-30）+ 保留引脚拒绝（USB D-/D+、RGB LED）
- 端口共享：相同 `(tx_pin, rx_pin, baud)` 的通道复用同一 UART port，`ref_count` 计数
- DMA 模式：`uart_driver_install(port, 1024, 256, 1024, NULL, 0)` + `rx_timeout=4`
- Polled 模式：`uart_driver_install(port, 256, 256, 0, NULL, 0)`
- LP_UART（C6 特有）：不支持 DMA，强制 polled；固定 IO 跳过 `uart_set_pin`
- UART0 可用性：console 在 USB-JTAG 时 UART0 可用于数据；否则从 UART1 开始

**写**（`uart_write`, `bus_dma.c:486-499`）：

- `uart_write_bytes` → `uart_wait_tx_done(100ms)`
- 通过 `tx_mutex` 保护（100ms 超时）

**读**（`uart_read`, `bus_dma.c:503-526`）：

- 非阻塞：`uart_read_bytes(timeout=0)` 取当前 FIFO 内容
- 追加读取：循环 `uart_read_bytes(timeout=2ms)` 直到无更多数据（跨 DMA 读取线性化）

### 2.2 SPI（bus_type=3）

**初始化**（`spi_init`, `bus_dma.c:611-743`）：

- 配置字节布局：`[cs, mode, freq×4(BE), MOSI, MISO, SCLK]` + 可选 flags
- 总线共享：相同 `(MOSI, MISO, SCLK, dma)` 的通道复用同一 SPI host，`ref_count` 计数
- DMA：`spi_bus_initialize(host, &bus_cfg, SPI_DMA_CH_AUTO)`
- Polled：`spi_bus_initialize(host, &bus_cfg, SPI_DMA_DISABLED)`
- 每个通道通过 `spi_bus_add_device` 添加独立设备（独立 CS）

**事务**（`spi_transact`, `bus_dma.c:745-774`）：

- 原子 write+read：`spi_device_transmit` 一次完成
- 支持 TX-only、RX-only、TX+RX 三种模式

### 2.3 I2C（bus_type=2）

**初始化**（`i2c_init`, `bus_dma.c:853-970`）：

- 配置字节布局：`[sda, scl, addr, freq×4(BE)]` + 可选 flags
- 总线共享：相同 `(sda, scl)` 的通道复用同一 I2C bus，`ref_count` 计数
- 使用 ESP-IDF 新 master API：`i2c_new_master_bus` + `i2c_master_bus_add_device`
- 硬件限制：活跃总线数不超过 `HW_I2C_COUNT`

**事务**（`i2c_transact`, `bus_dma.c:972-1002`）：

- DMA 模式：`i2c_master_transmit_receive`（合并 write+read）
- Polled 模式：`i2c_master_transmit` + `i2c_master_receive`（分步）
- 超时 50ms

---

## 三、命令执行引擎（bus_worker）

### 3.1 任务架构

| 任务 | 优先级 | 栈大小 | 职责 |
|------|--------|--------|------|
| cmd_task_uart0 | 6 | 4096 | 消费 `uart0_cmd_queue`，执行 UART0 TX |
| cmd_task_uart1 | 6 | 4096 | 消费 `uart1_cmd_queue`，执行 UART1 TX |
| cmd_task_spi | 6 | 3072 | 消费 `spi_cmd_queue`，执行 SPI transact |
| cmd_task_i2c | 6 | 3072 | 消费 `i2c_cmd_queue`，执行 I2C transact |
| rx_task | 7 | 4096 | 共享：轮询所有 UART 通道 RX，空闲检测，上报 |

每个 cmd_task 有独立队列，消除总线类型间竞争。rx_task 优先级更高（7>6），确保 RX 数据及时读取。

### 3.2 命令类型（cmd_queue.h）

`bus_cmd_t` 结构：

- **CMD_WRITE (0)**：后端 WriteCommand 下发
  - `read_size==0`：仅 TX，立即回 WriteRsp
  - `read_size>0`：TX + turnaround delay + 入队 `pending_cmd_t` 等待 rx_task 关联
- **CMD_SAMPLE (1)**：scheduler 周期采样
  - TX + turnaround + 入队 `pending_cmd_t`（`request_id=0`）

### 3.3 UART 命令流程（uart_cmd_loop, bus_worker.c:140-225）

**CMD_WRITE:**

1. `bus_dma_write(ctx, tx_data, tx_len)` — fire-and-forget
2. 若 `read_size > 0`：
   - `compute_turnaround_us()` — Modbus RTU 3.5 字符间隔（`38500000/baud` us），上限 100ms
   - `apply_turnaround_delay()` — >10ms 用 `vTaskDelay`，1-10ms 用 `ets_delay_us`
   - `enqueue_pending()` — 记录 `tx_timestamp`、`rx_timeout_ms=1000`、`cmd_data` 前 8 字节
3. 回调 `s_write_rsp_cb(request_id, true, 0, NULL)` → 发送 WriteRsp
4. `scheduler_notify_channel_success()`

**CMD_SAMPLE:**

1. `bus_dma_write()` 发送采样帧
2. `enqueue_pending()` 等待响应
3. `apply_turnaround_delay()`

### 3.4 SPI/I2C 命令流程（spi_i2c_cmd_loop, bus_worker.c:231-312）

**CMD_WRITE:**

1. `bus_dma_transact(ctx, tx, tx_len, rx, cap, &rl)` — 原子 write+read
2. 成功：WriteRsp + 若 `read_size>0` 且 `rl>0` → DataReport（带 `request_id`）
3. 失败：WriteRsp(error) + `scheduler_notify_channel_error()`

**CMD_SAMPLE:**

1. `bus_dma_transact()` 完整事务
2. 成功且 `rl>0` → DataReport（`request_id=0`）

> **关键区别**：SPI/I2C 是事务性的（TX+RX 原子完成），不需要 pending 队列和 rx_task。

### 3.5 RX 任务——透明字节管道（rx_task, bus_worker.c:360-488）

**设计原则（P1-8）**：ESP32 是透明 UART 字节管道。不做帧检测、不做协议解析。

**工作流程**（每 5ms 轮询一次）：

1. 遍历所有已初始化的 UART 通道
2. `bus_dma_read()` 非阻塞读取
3. 有数据 → 追加到 per-channel `stream_rx_t` 缓冲区（512 字节），记录 `s_last_rx_us`
4. 无数据且缓冲区非空 → 检查空闲阈值（`UART_IDLE_THRESHOLD_US = 10ms`）
   - 超过 10ms 无新字节 → 响应完整 → 上报
5. 上报逻辑：
   - **有 pending cmd** → `xQueuePeek` 取元数据 → DataReport（带 `request_id`、`edge_device_id` 等）→ `xQueueReceive` 弹出
   - **无 pending cmd** → 作为自发数据上报（`request_id=0, edge_device_id=0`）
6. 清空缓冲区

**RX 超时检测（P1-6, bus_worker.c:441-472）**：

- 遍历所有 pending 队列
- 若 `elapsed_ms > rx_timeout_ms`（默认 1000ms）：
  - `s_rx_timeout_count[i]++`
  - `ESP_LOGW "P1-6: RX timeout reqID=xxx"`
  - DataReport(`error_code=0x01`) 通知后端
  - 丢弃该 pending cmd

**溢出保护**：缓冲区满（512 字节）时重置 `len=0`，打印警告。

---

## 四、调度器（scheduler）

### 4.1 设计

纯定时器，不直接操作总线。所有总线事务通过投递 CMD_SAMPLE 到命令队列完成。

**三级调度模型（v2.3）**：

```
channel → edge_device → command
```

每个 command 有独立 `interval_ms` 和 `last_run_ms`。

无 edge_device 的通道回退到 legacy 路径（`template_ids[0]`）。

### 4.2 调度流程

`scheduler_task`：

1. 遍历所有活跃通道
2. 对每个通道的每个 edge_device 的每个 command：
   - 检查 `xTaskGetTickCount() - last_run_ms >= interval_ms`
   - 构造 `bus_cmd_t`（`type=CMD_SAMPLE`, `tx_data=template.write_data`）
   - 按 `bus_type` 投递到对应队列（uart0/uart1/spi/i2c）
3. `vTaskDelay` 到最近的下一次触发时间

**错误退避**：连续错误时 `skip_count` 递增，降低采样频率。

---

## 五、上行数据链路（设备 → 后端）

完整路径：

```
物理设备
  ↓ UART RX / SPI MISO / I2C SDA
bus_dma_read() / bus_dma_transact()
  ↓
[UART] rx_task 空闲检测 → s_data_rpt_cb()
[SPI/I2C] cmd_task 事务完成 → s_data_rpt_cb()
  ↓
msg_handler_send_data_report()
  ↓ data_report_encode() — 编码为 MSG_DATA_RPT (0x03) 帧
  ↓ 字段：channel_id, timestamp_us, sequence, raw_data, error_code,
  ↓       request_id, edge_device_id, command_template_id, command_index
  ↓
msg_handler_publish() → msg_handler_publish_checked()
  ↓ 优先当前 transport → 广播 → MQTT fallback
  ↓
mqtt_client_publish_impl()
  ↓ QoS 1（LOG_STREAM 用 QoS 0）
  ↓
MQTT Broker → 后端服务器
```

### DataReport 帧格式（MSG_DATA_RPT = 0x03）

| 字段号 | 名称 | 类型 | 说明 |
|--------|------|------|------|
| 1 | channel_id | varint | 通道 ID |
| 2 | timestamp_us | varint | 微秒时间戳 |
| 3 | sequence | varint | 序列号 |
| 4 | raw_data | bytes | 原始总线字节，未解析 |
| 5 | error_code | varint | 0=正常, 0x01=RX超时 |
| 6 | request_id | varint | 关联 WriteCommand |
| 7 | edge_device_id | varint | 边缘设备 ID |
| 8 | command_index | varint | 命令索引 |

---

## 六、下行数据链路（后端 → 设备）

### 6.1 WriteCommand（MSG_WRITE_CMD = 0x06）

```
MQTT Broker
  ↓ 订阅 topic: ehome/v2/{node_id}/down (QoS 0) + ehome/v2/{node_id}/control (QoS 1)
  ↓
mqtt_event_handler → mqtt_adapter_msg_cb → transport msg_cb
  ↓
msg_handler_process(data, len)
  ↓ data[0] == 0x06 → handler_writecmd_process()
  ↓ 解码字段：request_id, channel_id, data, read_size, edge_device_id
  ↓ 特殊：channel_id=0 + data=[0xFC,0x00] → 恢复出厂
  ↓
on_write_cmd_received() [weak callback → main.c]
  ↓
bus_manager_on_write_cmd()
  ↓ 构造 bus_cmd_t（type=CMD_WRITE）
  ↓ 按 bus_type + uart_port 投递到对应队列
  ↓
[UART] cmd_task_uart0/1 → bus_dma_write() → 物理 TX
       若 read_size>0 → enqueue_pending → 等 rx_task
[SPI/I2C] cmd_task_spi/i2c → bus_dma_transact() → 物理 TX+RX
  ↓
WriteRsp (0x07) → 后端（立即，不等 RX）
DataReport (0x03) → 后端（RX 数据到达后）
```

### 6.2 ConfigManifest（MSG_CONFIG_MFST = 0x04）

```
后端推送配置清单 → handler_config_process_manifest()
  → config_mgr 解析并应用
  → bus_manager_setup_from_manifest() — 清理旧通道，注册新通道
  → scheduler_prepare() + scheduler_activate() — 重建调度
  → ConfigResult (0x05) 回复后端
  → sync_manager_on_config_applied() — 更新 epoch/manifest_id
```

### 6.3 其他下行消息

| 消息类型 | 代码 | 处理 |
|----------|------|------|
| HelloAck | 0x12 | 握手确认，记录 server_time |
| Ping | 0x08 | 回复 Pong (0x09) |
| OtaCmd | 0x0A | 启动 OTA 升级 |
| ScanReq | 0x0D | I2C/Modbus 总线扫描 |
| QueryReq | 0x0E | 查询请求 |
| ConfigQuery | 0x10 | 配置查询 |
| QueryResources | 0x1A | 资源查询 → ResourceReport (0x19) |
| PeriphCmd | 0x1B | GPIO/PWM 控制 → PeriphRsp (0x1C) |

---

## 七、MQTT 传输层

### 7.1 连接管理

- 生命周期由 supervisor 任务独占管理（`mqtt_client_owner_step`）
- 状态机：`DISCONNECTED → CONNECTING → CONNECTED`
- 重连：指数退避（1s → 2s → 4s → ... → 30s 上限）
- CONNECTING 超时（30s）→ 升级重建客户端
- 订阅超时 → 退役旧客户端 → 重建

### 7.2 Topic 结构

| 方向 | Topic | QoS |
|------|-------|-----|
| 上行 | `ehome/v2/{node_id}/up` | 1 |
| 下行 | `ehome/v2/{node_id}/down` | 0 |
| 控制 | `ehome/v2/{node_id}/control` | 1 |

### 7.3 QoS 策略

- `LOG_STREAM (0x1D)`：QoS 0（允许丢失）
- 所有其他消息：QoS 1（可靠投递）

### 7.4 Transport 适配

`mqtt_transport_adapter.c` 将 ehome_mqtt 包装为通用 `transport_t` 接口：

- `init/start/stop/send/is_connected/deinit`
- 消息回调和状态回调转发
- `msg_handler` 优先使用当前 transport，失败则广播，最终 fallback 到 MQTT

---

## 八、DMA 资源管理（dma_pool）

- 初始化时从 `hw_tables` 静态表复制 DMA 通道信息
- **分配策略**（`dma_pool_allocate`）：
  1. 已绑定相同 `hw_id` → 直接复用
  2. 已绑定同总线类型不同 `hw_id` → 复用（C6 UHCI 共享约束）
  3. 空闲 + 兼容 + TX+RX 能力 → 分配
  4. 空闲 + 兼容（部分能力）→ 降级分配
  5. 无可用 → `ESP_ERR_NOT_FOUND`
- 用户可通过 ConfigManifest 的 DMA 配置（field 5）指定 `bind_to` 和 `enabled`
- 状态快照/恢复支持配置回滚

---

## 九、配置同步（sync_manager）

### 7 种同步原因

| 原因 | 策略 |
|------|------|
| NO_CONFIG / FORCED / USER_ACTION / EPOCH_LAG / MANIFEST_MISMATCH | 立即允许 |
| PERIODIC | 需间隔 > 600s |
| DOUBT | 需间隔 > 30s（去重窗口） |

### 同步流程

```
sync_manager_request_sync(reason)
  → should_request_sync() 去重检查
  → MQTT 连接检查
  → s_send_hello_cb() → msg_handler_send_hello()
  → 后端收到 Hello → 判断是否需要推送 ConfigManifest
  → 收到 ConfigManifest → sync_manager_on_config_applied()
  → 120s 超时未收到 → LED 显示 SERVER_OFFLINE → 重试
```

### 周期任务

- 无配置：30s 激进重试
- 有配置：60s 检查，>600s 触发周期同步

---

## 十、关键设计特点总结

1. **透明管道**：ESP32 不解析任何设备协议（Modbus/BMS/Inverter），原始字节透传
2. **TX/RX 分离（UART）**：cmd_task 只管发，rx_task 只管收，硬件全双工
3. **事务原子性（SPI/I2C）**：write+read 在一次 transact 中完成
4. **空闲检测（UART RX）**：10ms 无新字节 = 响应完整，适用于所有协议和帧长
5. **端口/总线共享**：相同引脚配置的通道复用同一硬件端口，`ref_count` 管理生命周期
6. **每总线独立队列**：消除 UART0/UART1/SPI/I2C 间的竞争
7. **依赖注入（bus_runtime_t）**：bus_worker/bus_manager 不依赖 `app_state_t`，通过回调和指针注入
8. **DMA 可选**：用户可通过 `bus_config` flags 字节控制 DMA 开关，LP_UART 强制 polled
