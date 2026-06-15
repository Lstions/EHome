# 统一总线 DMA 架构设计

> **版本**: v2.4
> **状态**: 设计中
> **关联**: [节点资源上报/详细设计.md](节点资源上报/详细设计.md) | [通道/详细设计.md](通道/详细设计.md)

## 1. 概述

将 ESP32 的 UART/SPI/I2C 三种总线通信统一为异步队列驱动模型，支持 **动态 DMA 开关** 配置。每种总线提供 DMA（高性能）和非 DMA（兼容/低功耗）两条路径，由 bus_config flags 字节在运行时选择。

### 1.1 解决的问题

| 问题 | 根因 | 本设计解决方式 |
|------|------|---------------|
| MQTT 回调阻塞 500ms | UART 事务在 MQTT 回调线程同步执行 | 命令队列 + Worker Task，MQTT 回调 <1μs |
| SPI 未实现 | bus_transact 返回 OK 但 rx_len=0 | bus_dma 完整实现 SPI DMA + polled 双路径 |
| I2C 并发竞态 | scheduler 和 WriteCommand 同时操作同一 I2C 总线 | 统一 Worker Task 串行处理 + mutex |
| UART 端口硬编码 | 全部 UART_NUM_1，多通道冲突 | hw_profile 声明可用端口，bus_config 指定 |
| uart_flush_input 丢数据 | 每次事务前清空 RX buffer | DMA 路径不 flush，循环读取 |
| uart_cmd_t 重复定义 | main.c 和 scheduler.c 各自定义 | 抽取到 cmd_queue.h 共享 |
| scheduler 反向依赖 | extern g_cmd_queue + scheduler_register_uart | cmd_queue.h 声明，回调注入 |
| DMA 无开关 | 固定使用 ESP-IDF 内置 DMA | bus_config flags bit0 控制 DMA/非 DMA |

### 1.2 ESP32-C6 各总线 DMA 能力

| 总线 | DMA 机制 | ESP-IDF v6 API | 非 DMA 方案 |
|------|----------|----------------|-------------|
| **UART** | DMA ring buffer 自动搬运 | `uart_driver_install(port, 1024, 256, ...)` | 小 buffer + 轮询 |
| **SPI** | SPI DMA 控制器 | `spi_bus_initialize(host, &cfg, SPI_DMA_CH_AUTO)` | `SPI_DMA_DISABLED` 轮询模式 |
| **I2C** | v6 新 API 内部队列化 | `i2c_new_master_bus()` + `i2c_master_transmit_receive()` | 旧 API `i2c_driver_install()` 阻塞 |

> 注意: I2C 在 ESP32-C6 上没有真正的 DMA 通道，但 v6 新 `i2c_master` API 是非阻塞队列化的，比旧 API 高效。"DMA enabled" 对 I2C 的含义是：使用新 API vs 旧 API。

## 2. 架构

```
┌─────────────────────────────────────────────────────────────────┐
│  MQTT Event Task (永不阻塞)                                       │
│  on_mqtt_msg() → msg_handler_process()                           │
│  WriteCommand → xQueueSend(g_cmd_queue, non-blocking) <1μs      │
│  ConfigManifest → config_mgr_apply()                              │
└───────────────────────────┬─────────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Command Queue (xQueue, 深度 16, bus_cmd_t)                       │
│  CMD_WRITE (高优) / CMD_SAMPLE (低优)                             │
│  统一处理 UART/SPI/I2C 所有总线类型                                 │
└───────────────────────────┬─────────────────────────────────────┘
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  Bus Worker Task (priority=8, 最高优先级)                         │
│                                                                  │
│  xQueueReceive → 按 bus_type 分发:                                │
│    UART → uart_transact (DMA / polled)                            │
│    SPI  → spi_transact (DMA / polled)                             │
│    I2C  → i2c_transact (new API / old API)                        │
│                                                                  │
│  → msg_handler_send_write_rsp / send_data_report                 │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  Scheduler Task (priority=5, 纯定时器)                            │
│  vTaskDelayUntil(10ms) 精确周期                                   │
│  所有总线类型 → xQueueSend(CMD_SAMPLE)                            │
│  不再直接操作任何硬件                                               │
└─────────────────────────────────────────────────────────────────┘
```

## 3. 组件设计

### 3.1 命令队列 (cmd_queue.h)

```c
/* 共享命令队列类型 — 消除 main.c / scheduler.c 重复定义 */
#define CMD_QUEUE_DEPTH  16
#define CMD_TX_MAX       128

typedef enum {
    CMD_WRITE  = 0,   /* WriteCommand: 服务端下发写入 */
    CMD_SAMPLE = 1,   /* 周期性采样 */
} cmd_type_t;

typedef struct {
    uint32_t   request_id;      /* WriteResponse 原样返回 */
    uint32_t   channel_id;      /* 目标通道 */
    uint8_t    bus_type;        /* 1=UART, 2=I2C, 3=SPI */
    uint8_t    tx_data[CMD_TX_MAX];
    size_t     tx_len;
    uint32_t   read_size;       /* 期望读取字节数 */
    uint32_t   timeout_ms;      /* 读取超时 */
    cmd_type_t type;
} bus_cmd_t;

/* 全局命令队列 — 在 main.c 中创建 */
extern QueueHandle_t g_cmd_queue;
```

### 3.2 统一 DMA 引擎 (bus_dma)

#### 3.2.1 接口 (bus_dma.h)

```c
typedef struct {
    uint8_t  bus_type;        /* 1=UART, 2=I2C, 3=SPI */
    bool     dma_enabled;     /* 运行时 DMA 开关 */
    bool     initialized;
    SemaphoreHandle_t mutex;  /* 保护并发访问 */
    union {
        struct {
            uart_port_t port;
            uint32_t baud;
            int tx_pin, rx_pin;
        } uart;
        struct {
            spi_host_device_t host;
            spi_device_handle_t dev;
            int cs_pin;
            uint32_t freq;
            uint8_t mode;
        } spi;
        struct {
            i2c_port_t port;
            uint8_t addr;
            uint32_t freq;
            int sda_pin, scl_pin;
            /* DMA (新 API) 时使用 */
            void *bus_handle;   /* i2c_master_bus_handle_t */
            void *dev_handle;   /* i2c_master_dev_handle_t */
        } i2c;
    } cfg;
} bus_dma_ctx_t;

esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len);
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len, uint32_t timeout_ms,
                           uint8_t *rx, size_t rx_size, size_t *rx_len);
void bus_dma_deinit(bus_dma_ctx_t *ctx);
```

#### 3.2.2 DMA 开关实现

```c
esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled, ...) {
    ctx->bus_type = bus_type;
    ctx->dma_enabled = dma_enabled;
    ctx->mutex = xSemaphoreCreateMutex();

    switch (bus_type) {
    case BUS_TYPE_UART:
        /* DMA: ring buffer 1024B + rx_timeout 4字符 */
        /* 非DMA: 小 buffer 256B, 无 rx_timeout */
        uart_driver_install(port, dma_enabled ? 1024 : 256,
                            dma_enabled ? 256 : 0, 0, NULL, 0);
        if (dma_enabled) uart_set_rx_timeout(port, 4);
        break;

    case BUS_TYPE_SPI:
        /* DMA: SPI_DMA_CH_AUTO 自动通道分配 */
        /* 非DMA: SPI_DMA_DISABLED 轮询模式 */
        spi_bus_initialize(SPI2_HOST, &bus_cfg,
                           dma_enabled ? SPI_DMA_CH_AUTO : SPI_DMA_DISABLED);
        break;

    case BUS_TYPE_I2C:
        if (dma_enabled) {
            /* v6 新 API: 非阻塞, 内部队列化 */
            i2c_new_master_bus(&bus_cfg, &bus_handle);
            i2c_master_bus_add_device(bus_handle, &dev_cfg, &dev_handle);
        } else {
            /* 旧 API: 阻塞式 cmd_begin */
            i2c_driver_install(port, I2C_MODE_MASTER, 0, 0, 0);
        }
        break;
    }
}
```

#### 3.2.3 事务分发

```c
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx, ...) {
    xSemaphoreTake(ctx->mutex, pdMS_TO_TICKS(500));
    esp_err_t ret;

    switch (ctx->bus_type) {
    case BUS_TYPE_UART:
        ret = ctx->dma_enabled
            ? uart_dma_transact(ctx, ...)    /* 循环读取, 不 flush */
            : uart_polled_transact(ctx, ...); /* 单次读取 */
        break;
    case BUS_TYPE_SPI:
        ret = spi_transact(ctx, ...);  /* DMA/非DMA 由 spi_bus_initialize 决定 */
        break;
    case BUS_TYPE_I2C:
        ret = ctx->dma_enabled
            ? i2c_new_api_transact(ctx, ...)  /* i2c_master_transmit_receive */
            : i2c_old_api_transact(ctx, ...); /* i2c_cmd_link + cmd_begin */
        break;
    }

    xSemaphoreGive(ctx->mutex);
    return ret;
}
```

### 3.3 UART DMA 事务（增强版）

```c
/* DMA 路径: 循环读取, 不 flush, 帧分隔靠 rx_timeout */
static esp_err_t uart_dma_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                    uint32_t timeout_ms, uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;

    /* TX */
    if (tx && tx_len > 0) {
        uart_write_bytes(ctx->cfg.uart.port, tx, tx_len);
        uart_wait_tx_done(ctx->cfg.uart.port, pdMS_TO_TICKS(100));
    }

    /* RX: 首次等首字节 (500ms), 然后循环读取直到 4字符间隔 */
    int total = 0;
    int first = uart_read_bytes(ctx->cfg.uart.port, rx, rx_sz,
                                pdMS_TO_TICKS(500));
    if (first <= 0) return ESP_ERR_TIMEOUT;
    total = first;

    /* 继续读取直到帧间隔 (rx_timeout=4字符) */
    while (total < (int)rx_sz) {
        int more = uart_read_bytes(ctx->cfg.uart.port, rx + total,
                                   rx_sz - total, pdMS_TO_TICKS(50));
        if (more <= 0) break;
        total += more;
    }

    *rx_len = total;
    return ESP_OK;
}

/* 非 DMA 路径: 单次阻塞读取 */
static esp_err_t uart_polled_transact(bus_dma_ctx_t *ctx, ...) {
    *rx_len = 0;
    uart_flush_input(ctx->cfg.uart.port);  /* 非 DMA 允许 flush */
    if (tx && tx_len > 0) {
        uart_write_bytes(ctx->cfg.uart.port, tx, tx_len);
        uart_wait_tx_done(ctx->cfg.uart.port, pdMS_TO_TICKS(100));
    }
    int bytes = uart_read_bytes(ctx->cfg.uart.port, rx, rx_sz,
                                pdMS_TO_TICKS(timeout_ms));
    if (bytes > 0) { *rx_len = bytes; return ESP_OK; }
    return ESP_ERR_TIMEOUT;
}
```

### 3.4 SPI 事务

```c
static esp_err_t spi_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                               uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    spi_transaction_t txn = {
        .length = tx_len * 8,          /* 单位: bit */
        .tx_buffer = tx,
        .rxlength = rx_sz * 8,
        .rx_buffer = rx,
    };
    esp_err_t err = spi_device_transmit(ctx->cfg.spi.dev, &txn);
    if (err == ESP_OK) {
        *rx_len = txn.rxlength / 8;
    }
    return err;
}
```

### 3.5 I2C 事务

```c
/* DMA (新 API) 路径 */
static esp_err_t i2c_new_api_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                       uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    i2c_master_dev_handle_t dev = ctx->cfg.i2c.dev_handle;

    if (tx_len > 0 && rx_sz > 0) {
        /* 写+读 (combined) */
        esp_err_t err = i2c_master_transmit_receive(dev, tx, tx_len, rx, rx_sz, pdMS_TO_TICKS(100));
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    } else if (tx_len > 0) {
        return i2c_master_transmit(dev, tx, tx_len, pdMS_TO_TICKS(100));
    } else if (rx_sz > 0) {
        esp_err_t err = i2c_master_receive(dev, rx, rx_sz, pdMS_TO_TICKS(100));
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    }
    return ESP_OK;
}

/* 非 DMA (旧 API) 路径 */
static esp_err_t i2c_old_api_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                                       uint8_t *rx, size_t rx_sz, size_t *rx_len)
{
    *rx_len = 0;
    i2c_port_t port = ctx->cfg.i2c.port;
    uint8_t addr = ctx->cfg.i2c.addr;

    /* Write phase */
    if (tx_len > 0) {
        i2c_cmd_handle_t cmd = i2c_cmd_link_create();
        i2c_master_start(cmd);
        i2c_master_write_byte(cmd, (addr << 1) | I2C_MASTER_WRITE, true);
        i2c_master_write(cmd, tx, tx_len, true);
        i2c_master_stop(cmd);
        esp_err_t err = i2c_master_cmd_begin(port, cmd, pdMS_TO_TICKS(100));
        i2c_cmd_link_delete(cmd);
        if (err != ESP_OK) return err;
    }

    /* Read phase */
    if (rx_sz > 0) {
        i2c_cmd_handle_t cmd = i2c_cmd_link_create();
        i2c_master_start(cmd);
        i2c_master_write_byte(cmd, (addr << 1) | I2C_MASTER_READ, true);
        if (rx_sz > 1) i2c_master_read(cmd, rx, rx_sz - 1, I2C_MASTER_ACK);
        i2c_master_read_byte(cmd, rx + rx_sz - 1, I2C_MASTER_NACK);
        i2c_master_stop(cmd);
        esp_err_t err = i2c_master_cmd_begin(port, cmd, pdMS_TO_TICKS(100));
        i2c_cmd_link_delete(cmd);
        if (err == ESP_OK) *rx_len = rx_sz;
        return err;
    }
    return ESP_OK;
}
```

### 3.6 Scheduler 改造

```c
/* scheduler_task: 所有总线类型走队列 */
static void scheduler_task(void *p) {
    TickType_t wake = xTaskGetTickCount();
    while (s_running) {
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(10));
        TickType_t now = xTaskGetTickCount();

        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled) continue;
            if (now - s_channels[i].last_sample_time < pdMS_TO_TICKS(s_channels[i].config.interval_ms)) continue;
            s_channels[i].last_sample_time = now;

            /* 所有总线类型统一入队 */
            bus_cmd_t cmd = {
                .channel_id = s_channels[i].config.id,
                .bus_type   = s_channels[i].config.bus_type,
                .tx_len     = 0,
                .read_size  = 0,
                .timeout_ms = 30,
                .type       = CMD_SAMPLE,
            };

            /* 从 template 填充 tx_data */
            if (s_channels[i].config.template_count > 0) {
                const config_template_t *t = config_mgr_get_template(
                    s_channels[i].config.template_ids[0]);
                if (t && t->write_data_len > 0) {
                    cmd.tx_len = t->write_data_len < CMD_TX_MAX
                               ? t->write_data_len : CMD_TX_MAX;
                    memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
                    cmd.read_size = t->read_length;
                }
            }
            xQueueSend(g_cmd_queue, &cmd, 0);  /* 非阻塞 */
        }
    }
    vTaskDelete(NULL);
}
```

### 3.7 Bus Worker Task (main.c)

```c
static void bus_worker_task(void *p) {
    bus_cmd_t cmd;
    uint8_t rx[256];

    while (1) {
        if (!xQueueReceive(g_cmd_queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = find_ctx(cmd.channel_id);
        if (!ctx) {
            if (cmd.type == CMD_WRITE)
                msg_handler_send_write_rsp(cmd.request_id, false, 4, "no ctx");
            continue;
        }

        size_t rl = 0;
        esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                        cmd.timeout_ms ? cmd.timeout_ms : 50,
                                        rx, sizeof(rx), &rl);

        if (cmd.type == CMD_WRITE) {
            bool ok = (e == ESP_OK);
            msg_handler_send_write_rsp(cmd.request_id, ok, ok ? 0 : (uint32_t)e,
                                       ok ? NULL : "bus err");
            if (rl > 0) {
                uint64_t ts = esp_timer_get_time();
                msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, cmd.request_id);
            }
        }
        if (cmd.type == CMD_SAMPLE && rl > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, 0);
        }
    }
}
```

## 4. bus_config 二进制格式

```
═══════════════════════════════════════════════════════════════
 bus_config 二进制格式 (big-endian)
═══════════════════════════════════════════════════════════════

UART (bus_type=1):
  标准: [tx_pin, rx_pin, baud×4(BE)]           = 6 bytes
  扩展: [tx_pin, rx_pin, baud×4(BE), flags]    = 7 bytes

I2C (bus_type=2):
  标准: [sda, scl, addr, freq×4(BE)]           = 7 bytes
  扩展: [sda, scl, addr, freq×4(BE), flags]    = 8 bytes

SPI (bus_type=3):
  标准: [cs, mode, freq×4(BE)]                 = 6 bytes
  扩展: [cs, mode, freq×4(BE), flags]          = 7 bytes

GPIO (bus_type=4): [pin, direction]             = 2 bytes
ADC  (bus_type=5): [unit, channel, attenuation] = 3 bytes

flags 字节:
  bit 0: dma_enabled (1=DMA/新API, 0=轮询/旧API)
  bits 1-7: reserved

向后兼容: config_len 不含 flags 字节时, 默认 dma_enabled=1
═══════════════════════════════════════════════════════════════
```

解析辅助函数:
```c
/* config_mgr.h */
bool bus_config_get_dma_enabled(const config_channel_t *ch);
int  bus_config_get_uart_port(const config_channel_t *ch);
```

## 5. 文件变更清单

```
新增:
  components/hw_profile/
    include/hw_profile.h          芯片能力常量 + build 函数声明
    hw_profile.c                  ESP32-C6 静态 profile + 二进制编码
    CMakeLists.txt                REQUIRES frame, config_mgr

  components/bus_dma/             ← 重命名自 uart_dma
    include/cmd_queue.h           统一 bus_cmd_t 类型 + g_cmd_queue extern
    include/bus_dma.h             统一 DMA 引擎接口
    bus_dma.c                     UART/SPI/I2C DMA + 非DMA 实现
    CMakeLists.txt                REQUIRES esp_driver_uart/spi/i2c

修改:
  components/frame/frame_codec.h
    + #define MSG_RESOURCE_REPORT  0x19
    + #define MSG_QUERY_RESOURCES  0x1A

  components/msg_handler/
    msg_handler.h   + msg_handler_send_resource_report(void)
    msg_handler.c   + case MSG_QUERY_RESOURCES → 触发上报
                    + HelloAck 处理末尾设置标志

  components/scheduler/
    scheduler.c     重写: #include "cmd_queue.h", 所有总线上队列
    scheduler.h     删除 scheduler_execute_write

  components/config_mgr/
    config_mgr.h    + bus_config_get_dma_enabled()
    config_mgr.c    实现 flags 解析

  main/
    main.c          重写: bus_worker_task + 注册所有总线到 bus_dma
    CMakeLists.txt  uart_dma → bus_dma, + hw_profile

删除:
  components/uart_dma/            (整体移入 bus_dma)
```

## 6. 内存估算

```
bus_cmd_t:          ~148B × 16 depth = 2.4KB (队列)
bus_dma_ctx_t:      ~80B × 8 channels = 640B (上下文)
SPI DMA buffer:     4KB (spi_bus max_transfer_sz)
I2C new API:        ~500B internal
总计额外:           ~7.5KB RAM (ESP32-C6 有 512KB SRAM)
```

## 7. 性能对比

| 指标 | 旧实现 (v2.3) | 新实现 (v2.4) |
|------|---------------|---------------|
| MQTT 回调阻塞 | UART 500ms, I2C 200ms | <1μs (仅 xQueueSend) |
| SPI 支持 | 未实现 | DMA + polled 双路径 |
| I2C 并发安全 | mutex + 错误路径 bug | Worker Task 串行 + mutex |
| UART 帧分隔 | flush_input + 单次读取 | rx_timeout=4字符 + 循环读取 |
| DMA 可配置 | 固定 DMA | flags bit0 运行时开关 |
| 吞吐 (全双工) | ~15Hz (UART only) | ~80Hz (所有总线) |
| 功耗 | 高 (持续轮询) | 低 (事件驱动) |

## 8. 实施步骤

1. **Phase 1**: frame_codec.h 添加消息类型常量
2. **Phase 2**: hw_profile 组件 (常量表 + 二进制编码)
3. **Phase 3**: bus_dma 组件 (UART/SPI/I2C DMA + 非DMA)
4. **Phase 4**: cmd_queue.h (统一命令类型)
5. **Phase 5**: scheduler 重写 (所有总线上队列)
6. **Phase 6**: main.c 重构 (bus_worker_task + 总线注册)
7. **Phase 7**: msg_handler 添加 ResourceReport + QueryResources
8. **Phase 8**: config_mgr bus_config flags 解析
9. **Phase 9**: Go 后端 handler_resources 二进制解码
10. **Phase 10**: Go 后端 query-resources POST 实现

**工作量估算**: ~800 行新代码, ~200 行修改, 3-4 小时。
