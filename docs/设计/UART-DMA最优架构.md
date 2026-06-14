# ESP32 串口数据传输最优架构设计

## 一、整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         MQTT Event Task                         │
│  on_mqtt_msg() → msg_handler_process()                         │
│    WriteCommand → xQueueSend(cmd_queue, non-blocking)  <1μs    │
│    ConfigManifest → config_mgr_apply()                          │
│    Ping → msg_handler_send_pong()                               │
│  永不阻塞                                                        │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   WriteCommand Queue (xQueue)                   │
│  深度: 16, 元素: {ch_id, tx_data, tx_len, read_size, req_id}    │
│  满时策略: 丢弃最旧 / 返回 BUSY 错误                              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   UART Worker Task (priority=8)                 │
│                                                                  │
│  loop:                                                          │
│    1. xQueueReceive(cmd_queue, timeout=portMAX_DELAY)           │
│    2. uart_transact_dma(ch, cmd.tx, cmd.tx_len,                │
│                          cmd.read_size, &rx_buf)                │
│    3. msg_handler_send_write_rsp(cmd.req_id, ...)               │
│    4. msg_handler_send_data_report(rx_buf, ...)                  │
│                                                                  │
│  也处理 scheduler 的周期性采样请求（同一队列）                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│               UART DMA + Ring Buffer (硬件层)                    │
│                                                                  │
│  TX: uart_write_bytes() → DMA 引擎 → GPIO                       │
│      → uart_wait_tx_done(10ms)   非阻塞等待                      │
│                                                                  │
│  RX: DMA → 硬件 RX FIFO → 环形缓冲区 (1024B)                     │
│      RX 超时中断 → 协议帧分隔 (3.5字符 @ 9600baud ≈ 3.6ms)      │
│      → xEventGroupSetBits(rx_event)                              │
│                                                                  │
│  关键: 不再 flush_input，数据零丢失                               │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Scheduler Task (priority=5)                   │
│                                                                  │
│  vTaskDelayUntil(&wake_time, pdMS_TO_TICKS(10))  ← 精确周期     │
│  for each channel:                                              │
│    if due: xQueueSend(cmd_queue, sample_req, timeout=0)         │
│                                                                  │
│  不再直接调用 bus_transact，纯定时器角色                           │
└─────────────────────────────────────────────────────────────────┘
```

## 二、核心组件设计

### 2.1 DMA UART 事务引擎 (uart_dma.c)

```c
// === 数据结构 ===
typedef struct {
    uart_port_t port;
    int         tx_pin, rx_pin;
    uint32_t    baud_rate;
    
    // DMA 环形缓冲区
    uint8_t    *rx_ring_buf;
    size_t      rx_ring_size;
    volatile size_t rx_head;   // ISR 写入位置
    volatile size_t rx_tail;   // 应用读取位置
    
    // 帧分隔: RX 超时中断
    // UART_RX_TOUT_THRESH = 波特率适配的字节超时
    // 9600baud: 1字节≈1ms, 3.5字符≈4ms → 超时设为 5ms
    uint32_t    rx_timeout_ms;
    
    // 同步
    EventGroupHandle_t evt_group;
    #define UART_EVT_RX_DATA    (1 << 0)
    #define UART_EVT_TX_DONE    (1 << 1)
    
    // 当前事务上下文
    uint8_t    *current_rx_buf;
    size_t      current_rx_size;
    size_t     *current_rx_len;
    bool        transaction_active;
} uart_dma_ctx_t;

// === API ===
// 初始化: 安装 DMA 驱动 + RX 超时中断
esp_err_t uart_dma_init(uart_dma_ctx_t *ctx);

// 异步事务: 发送 TX，等待 RX 超时或指定字节数
// 返回: ESP_OK(收到数据), ESP_ERR_TIMEOUT(超时), 其他错误
esp_err_t uart_dma_transact(uart_dma_ctx_t *ctx,
                            const uint8_t *tx_data, size_t tx_len,
                            uint32_t read_timeout_ms,
                            uint8_t *rx_buf, size_t rx_buf_size,
                            size_t *rx_len);

// === 中断服务例程 ===
static void IRAM_ATTR uart_rx_isr(void *arg) {
    uart_dma_ctx_t *ctx = arg;
    // 从 UART FIFO 读取到 ring buffer
    size_t avail = uart_get_buffered_data_len(ctx->port);
    while (avail-- && ctx->rx_head - ctx->rx_tail < ctx->rx_ring_size) {
        uint8_t byte;
        uart_read_bytes(ctx->port, &byte, 1, 0);
        ctx->rx_ring_buf[ctx->rx_head++ % ctx->rx_ring_size] = byte;
    }
    // 通知 worker task
    BaseType_t higher_prio = pdFALSE;
    xEventGroupSetBitsFromISR(ctx->evt_group, UART_EVT_RX_DATA, &higher_prio);
    portYIELD_FROM_ISR(higher_prio);
}

// RX 超时中断: 协议帧结束标志
static void IRAM_ATTR uart_rx_timeout_isr(void *arg) {
    uart_dma_ctx_t *ctx = arg;
    BaseType_t higher_prio = pdFALSE;
    xEventGroupSetBitsFromISR(ctx->evt_group, UART_EVT_RX_DATA, &higher_prio);
    portYIELD_FROM_ISR(higher_prio);
}
```

### 2.2 命令队列 (cmd_queue.h)

```c
typedef struct {
    uint32_t  request_id;    // WriteResponse 原样返回
    uint32_t  channel_id;    // 目标通道
    uint8_t   tx_data[128];  // 发送数据
    size_t    tx_len;
    uint32_t  read_size;     // 期望读取字节数
    uint32_t  read_timeout_ms; // 读取超时 (0=使用通道默认值)
    bool      is_sample;     // true=周期性采样, false=WriteCommand
} uart_cmd_t;

// 全局命令队列
extern QueueHandle_t g_uart_cmd_queue;  // 深度 16
```

### 2.3 集成到现有代码

```c
// === main.c 新增 ===
#define UART_WORKER_STACK  4096
#define UART_WORKER_PRIO   8        // 高于 scheduler(5) 和 MQTT(5)

static void uart_worker_task(void *pvParameters) {
    uart_cmd_t cmd;
    
    while (1) {
        // 阻塞等待命令（低功耗，无需轮询）
        if (xQueueReceive(g_uart_cmd_queue, &cmd, portMAX_DELAY) != pdTRUE) {
            continue;
        }
        
        uint8_t rx_buf[256];
        size_t rx_len = 0;
        
        // 执行 DMA UART 事务
        esp_err_t err = uart_dma_transact(
            get_channel_ctx(cmd.channel_id),
            cmd.tx_data, cmd.tx_len,
            cmd.read_timeout_ms ?: 50,  // 默认 50ms 超时
            rx_buf, sizeof(rx_buf), &rx_len
        );
        
        // 构建响应
        bool success = (err == ESP_OK);
        msg_handler_send_write_rsp(cmd.request_id, success, 
                                   success ? 0 : (uint32_t)err,
                                   success ? NULL : "uart error");
        
        // 有数据则上报 DataReport
        if (rx_len > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0,
                                         rx_buf, rx_len, 0, cmd.request_id);
        }
        
        // TX/RX 记录到终端
        terminal_record_tx(cmd.channel_id, cmd.tx_data, cmd.tx_len);
        if (rx_len > 0) {
            terminal_record_rx(cmd.channel_id, rx_buf, rx_len);
        }
    }
}

// === on_write_cmd_received (main.c) 修改为 ===
void on_write_cmd_received(uint32_t request_id, uint32_t channel_id,
                           const uint8_t *data, size_t len, uint32_t read_size) {
    uart_cmd_t cmd = {
        .request_id = request_id,
        .channel_id = channel_id,
        .tx_len     = len < sizeof(cmd.tx_data) ? len : sizeof(cmd.tx_data),
        .read_size  = read_size,
        .read_timeout_ms = 50,   // WriteCommand 使用较短超时
        .is_sample  = false,
    };
    memcpy(cmd.tx_data, data, cmd.tx_len);
    
    // 非阻塞入队
    if (xQueueSend(g_uart_cmd_queue, &cmd, 0) != pdTRUE) {
        // 队列满: 返回错误
        msg_handler_send_write_rsp(request_id, false, 0xFFFF, "queue full");
    }
}
```

## 三、DMA 配置关键参数

```c
// UART DMA 初始化
uart_config_t uart_cfg = {
    .baud_rate  = 9600,
    .data_bits  = UART_DATA_8_BITS,
    .parity     = UART_PARITY_DISABLE,
    .stop_bits  = UART_STOP_BITS_1,
    .flow_ctrl  = UART_HW_FLOWCTRL_DISABLE,
    .source_clk = UART_SCLK_DEFAULT,
};

// DMA 驱动安装: RX ring buffer 1024B, TX ring buffer 256B
// 参数说明: rx_buffer_size, tx_buffer_size, queue_size, intr_alloc_flags
uart_driver_install(port, 1024, 256, 10, &uart_rx_queue, 0);

// 设置 UART 模式为普通（非 RS485）
uart_set_mode(port, UART_MODE_UART);

// 配置 RX 超时中断 — 用于协议帧分隔
// 参数: 超时字符数（在设定波特率下 N 个字符时间后触发）
// Modbus RTU 帧间间隔 3.5 字符 → 设为 4 字符
uart_set_rx_timeout(port, 4);

// 启用 RX FIFO 满中断 + RX 超时中断
uart_enable_rx_intr(port);

// 注册 ISR
uart_isr_register(port, uart_rx_isr, ctx,
                  ESP_INTR_FLAG_IRAM | ESP_INTR_FLAG_SHARED,
                  NULL);
```

**RX 超时阈值计算公式：**
```
timeout_chars = ceil(protocol_frame_gap_ms / char_time_ms)
             = ceil(3.5 / (1000 / (9600 / 10)))    // Modbus @ 9600
             = ceil(3.5 / 1.04)
             = 4 个字符
```

## 四、worker task 等待策略

```c
// uart_dma_transact 内部实现
esp_err_t uart_dma_transact(...) {
    // 1. 发送 TX (DMA)
    if (tx_len > 0) {
        uart_write_bytes(ctx->port, tx_data, tx_len);
        uart_wait_tx_done(ctx->port, pdMS_TO_TICKS(10));
    }
    
    // 2. 等待 RX 数据 (事件驱动, 不轮询)
    EventBits_t bits = xEventGroupWaitBits(
        ctx->evt_group,
        UART_EVT_RX_DATA,
        pdTRUE,          // 自动清除
        pdFALSE,         // 任意位置位即返回
        pdMS_TO_TICKS(read_timeout_ms)
    );
    
    if (bits & UART_EVT_RX_DATA) {
        // 从 ring buffer 读取数据
        return uart_dma_read_ring(ctx, rx_buf, rx_buf_size, rx_len);
    }
    
    return ESP_ERR_TIMEOUT;
}
```

## 五、性能对比

| 指标 | 当前实现 | 最优架构 |
|------|----------|----------|
| MQTT 回调阻塞时间 | 500ms | <1μs (仅 xQueueSend) |
| UART 事务模式 | 同步阻塞 | 异步 DMA + 事件驱动 |
| RX 超时 | 固定 500ms | 可配置 (10-200ms) |
| CPU 利用率 | 100Hz 轮询 + 500ms 阻塞 | 纯事件驱动 (ISR → EventGroup) |
| 数据丢失 | uart_flush_input 丢弃 | 零丢失 (ring buffer) |
| 命令队列深度 | 无 (串行阻塞) | 16 条 (削峰填谷) |
| DMA 支持 | 无 | TX/RX 双 DMA 通道 |
| 吞吐 (全双工) | ~15Hz | ~80Hz (物理极限) |
| 吞吐 (TX-only) | ~50Hz | ~500Hz+ |
| 功耗 | 高 (持续唤醒) | 低 (事件唤醒) |

## 六、实施步骤

1. **Phase 1**: 创建 `uart_dma.c/h` — DMA 事务引擎 + ISR
2. **Phase 2**: 创建 `cmd_queue.h` — 命令队列定义
3. **Phase 3**: 修改 `main.c` — 添加 uart_worker_task, 重写 on_write_cmd_received
4. **Phase 4**: 修改 `scheduler.c` — scheduler_task 通过队列提交采样请求
5. **Phase 5**: 删除 `bus.c` 中 UART 分支的 `uart_flush_input` 和阻塞读
6. **Phase 6**: 性能验证 — 吞吐测试, 数据完整性测试

**工作量估算**: ~300 行新代码 (DMA 引擎 + worker), ~100 行修改 (main + scheduler), 2-3 小时。
